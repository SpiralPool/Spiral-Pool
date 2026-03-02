// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package nodemanager

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// ManagedNode wraps a daemon client with health tracking.
type ManagedNode struct {
	ID       string              // Node identifier
	Client   *daemon.Client      // RPC client
	ZMQ      *daemon.ZMQListener // Optional ZMQ listener
	Config   *NodeConfig         // Original configuration
	Priority int                 // Lower = preferred
	Weight   int                 // For load balancing

	// Health tracking (protected by mu)
	mu     sync.RWMutex
	Health HealthScore
}

// Manager manages multiple daemon nodes for a single coin.
type Manager struct {
	coin   string         // Coin symbol (e.g., "DGB")
	nodes  []*ManagedNode // All configured nodes
	logger *zap.SugaredLogger

	// Primary node (protected by mu)
	mu      sync.RWMutex
	primary *ManagedNode

	// Health monitoring
	monitor *HealthMonitor

	// Best known block height (updated by checkAllNodes)
	bestHeight uint64

	// Failover tracking
	failoverHistory []FailoverEvent
	failoverCount   int

	// Callbacks
	onFailover        func(event FailoverEvent)
	onBlockNotify     func(blockHash []byte)
	onZMQStatusChange func(status daemon.ZMQStatus)

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewManager creates a new node manager for a coin.
func NewManager(coin string, configs []NodeConfig, logger *zap.Logger) (*Manager, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("at least one node must be configured")
	}

	log := logger.Sugar().With("coin", coin)

	m := &Manager{
		coin:    coin,
		nodes:   make([]*ManagedNode, 0, len(configs)),
		logger:  log,
		monitor: DefaultHealthMonitor(),
	}

	// Create managed nodes from configs
	for _, cfg := range configs {
		cfgCopy := cfg // Copy for closure safety

		// Create daemon client
		daemonCfg := &config.DaemonConfig{
			Host:     cfg.Host,
			Port:     cfg.Port,
			User:     cfg.User,
			Password: cfg.Password,
		}
		client := daemon.NewClient(daemonCfg, logger)

		node := &ManagedNode{
			ID:       cfg.ID,
			Client:   client,
			Config:   &cfgCopy,
			Priority: cfg.Priority,
			Weight:   cfg.Weight,
			Health: HealthScore{
				Score:       0.5, // Start neutral
				State:       NodeStateUnknown,
				SuccessRate: 0.5,
				LastSuccess: time.Now(), // BUG FIX: Initialize to now, not zero value (year 1)
			},
		}

		// Setup ZMQ if configured (but don't start yet)
		// CRITICAL: Copy all timing fields - these are tuned per-coin based on block time
		// Fast coins (DGB 15s) need aggressive timing, slow coins (BTC 600s) use relaxed timing
		if cfg.ZMQ != nil && cfg.ZMQ.Enabled {
			zmqCfg := &config.ZMQConfig{
				Enabled:             true,
				Endpoint:            cfg.ZMQ.Endpoint,
				ReconnectInitial:    cfg.ZMQ.ReconnectInitial,
				ReconnectMax:        cfg.ZMQ.ReconnectMax,
				ReconnectFactor:     cfg.ZMQ.ReconnectFactor,
				FailureThreshold:    cfg.ZMQ.FailureThreshold,
				StabilityPeriod:     cfg.ZMQ.StabilityPeriod,
				HealthCheckInterval: cfg.ZMQ.HealthCheckInterval,
			}
			node.ZMQ = daemon.NewZMQListener(zmqCfg, logger)
		}

		m.nodes = append(m.nodes, node)
		log.Infow("Added node",
			"nodeId", cfg.ID,
			"host", cfg.Host,
			"port", cfg.Port,
			"priority", cfg.Priority,
			"zmq", cfg.ZMQ != nil && cfg.ZMQ.Enabled,
		)
	}

	// Sort nodes by priority
	sort.Slice(m.nodes, func(i, j int) bool {
		return m.nodes[i].Priority < m.nodes[j].Priority
	})

	// Set initial primary as highest priority node
	m.primary = m.nodes[0]
	log.Infow("Initial primary node set", "nodeId", m.primary.ID)

	return m, nil
}

// Start begins health monitoring and manages node lifecycle.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	// Perform initial health check on all nodes
	m.checkAllNodes(m.ctx)

	// Select best initial primary
	m.selectBestPrimary()

	// Start ZMQ on primary if available
	if m.primary.ZMQ != nil {
		m.startZMQOnPrimary()
	}

	// Start health monitoring loop
	m.wg.Add(1)
	go m.healthMonitorLoop()

	m.logger.Info("Node manager started")
	return nil
}

// Stop gracefully shuts down the node manager.
func (m *Manager) Stop() error {
	m.cancel()
	m.wg.Wait()

	// Stop ZMQ listeners
	for _, node := range m.nodes {
		if node.ZMQ != nil {
			_ = node.ZMQ.Stop() // #nosec G104
		}
	}

	m.logger.Info("Node manager stopped")
	return nil
}

// SelectNode returns the best available node for an operation.
func (m *Manager) SelectNode(op OperationType) (*ManagedNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Critical operations require primary
	if op.RequiresPrimary() {
		if m.primary != nil {
			m.primary.mu.RLock()
			score := m.primary.Health.Score
			m.primary.mu.RUnlock()
			if score > 0.3 {
				return m.primary, nil
			}
		}
		// Fall through to find any healthy node
	}

	// BUG FIX: If only one node configured, always return it.
	// Single-node setups have no fallback, so refusing to try the only node
	// guarantees failure. Better to try and fail than to not try at all.
	if len(m.nodes) == 1 {
		return m.nodes[0], nil
	}

	// Find best healthy node
	var best *ManagedNode
	bestScore := 0.0

	for _, node := range m.nodes {
		node.mu.RLock()
		score := node.Health.Score
		node.mu.RUnlock()

		if score > bestScore {
			best = node
			bestScore = score
		}
	}

	// BUG FIX: Always return the best node if one exists.
	// The score threshold was causing failures when all nodes had low scores
	// (e.g., after daemon restart). If we have nodes, we should try them.
	// The score is used for SELECTION (pick the best), not REJECTION.
	if best == nil {
		return nil, ErrNoHealthyNodes
	}

	return best, nil
}

// GetPrimary returns the current primary node.
func (m *Manager) GetPrimary() *ManagedNode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.primary
}

// GetNode returns a specific node by ID.
func (m *Manager) GetNode(id string) (*ManagedNode, error) {
	for _, node := range m.nodes {
		if node.ID == id {
			return node, nil
		}
	}
	return nil, ErrNodeNotFound
}

// SetBlockHandler sets the callback for block notifications.
func (m *Manager) SetBlockHandler(handler func(blockHash []byte)) {
	m.mu.Lock()
	m.onBlockNotify = handler
	// BUG FIX: Snapshot primary under lock to prevent race with failover
	primary := m.primary
	m.mu.Unlock()

	// Wire up to primary's ZMQ if active
	if primary != nil && primary.ZMQ != nil {
		primary.ZMQ.SetBlockHandler(handler)
	}
}

// SetFailoverHandler sets the callback for failover events.
func (m *Manager) SetFailoverHandler(handler func(event FailoverEvent)) {
	m.mu.Lock()
	m.onFailover = handler
	m.mu.Unlock()
}

// SetZMQStatusHandler sets the callback for ZMQ status changes.
// This allows CoinPool to update Prometheus metrics when ZMQ status changes.
func (m *Manager) SetZMQStatusHandler(handler func(status daemon.ZMQStatus)) {
	m.mu.Lock()
	m.onZMQStatusChange = handler
	// BUG FIX: Snapshot primary under lock to prevent race with failover
	primary := m.primary
	m.mu.Unlock()

	// Wire up to primary's ZMQ if active
	if primary != nil && primary.ZMQ != nil {
		primary.ZMQ.SetStatusChangeHandler(handler)
	}
}

// Stats returns current manager statistics.
func (m *Manager) Stats() ManagerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := ManagerStats{
		Coin:          m.coin,
		TotalNodes:    len(m.nodes),
		HealthyNodes:  0,
		FailoverCount: m.failoverCount,
		NodeHealths:   make(map[string]float64),
		BlockHeight:   m.bestHeight,
		PeerCount:     -1, // Default: unavailable
	}

	if m.primary != nil {
		stats.PrimaryNodeID = m.primary.ID
		m.primary.mu.RLock()
		stats.PeerCount = m.primary.Health.Connections
		m.primary.mu.RUnlock()
	}

	for _, node := range m.nodes {
		node.mu.RLock()
		stats.NodeHealths[node.ID] = node.Health.Score
		if node.Health.State == NodeStateHealthy || node.Health.State == NodeStateDegraded {
			stats.HealthyNodes++
		}
		node.mu.RUnlock()
	}

	if len(m.failoverHistory) > 0 {
		// BUG FIX: Take address of slice element directly, not a local copy.
		// The local copy would become invalid after function returns.
		stats.LastFailover = &m.failoverHistory[len(m.failoverHistory)-1]
	}

	return stats
}

// healthMonitorLoop continuously monitors node health.
func (m *Manager) healthMonitorLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkAllNodes(m.ctx)
			m.maybeFailover()
		}
	}
}

// checkAllNodes performs health checks on all nodes.
func (m *Manager) checkAllNodes(ctx context.Context) {
	// Find best known height for sync comparison
	bestHeight := uint64(0)
	for _, node := range m.nodes {
		node.mu.RLock()
		if node.Health.BlockHeight > bestHeight {
			bestHeight = node.Health.BlockHeight
		}
		node.mu.RUnlock()
	}

	// Check each node
	for _, node := range m.nodes {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result := m.monitor.PerformHealthCheck(checkCtx, node)
		cancel()

		// Update best height
		if result.Success && result.BlockHeight > bestHeight {
			bestHeight = result.BlockHeight
		}
	}

	// Store best height for Stats() access
	m.mu.Lock()
	m.bestHeight = bestHeight
	m.mu.Unlock()

	// Recalculate scores with updated best height
	for _, node := range m.nodes {
		score := m.monitor.CalculateHealth(node, bestHeight)

		node.mu.Lock()
		node.Health.Score = score
		node.mu.Unlock()

		m.monitor.UpdateState(node)
	}
}

// maybeFailover checks if primary should be changed.
func (m *Manager) maybeFailover() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if current primary is healthy enough
	if m.primary != nil {
		m.primary.mu.RLock()
		primaryScore := m.primary.Health.Score
		primaryState := m.primary.Health.State
		m.primary.mu.RUnlock()

		// Primary is fine if healthy or degraded with decent score
		if primaryState == NodeStateHealthy ||
			(primaryState == NodeStateDegraded && primaryScore >= 0.5) {
			return
		}
	}

	// Find new primary
	var best *ManagedNode
	bestScore := 0.0

	for _, node := range m.nodes {
		node.mu.RLock()
		score := node.Health.Score
		node.mu.RUnlock()

		if score > bestScore {
			best = node
			bestScore = score
		}
	}

	// Don't switch if no better option
	if best == nil || best == m.primary {
		return
	}

	// Don't switch unless new node is significantly better
	if m.primary != nil {
		m.primary.mu.RLock()
		primaryScore := m.primary.Health.Score
		m.primary.mu.RUnlock()

		// Require at least 0.2 improvement to avoid flapping
		if bestScore < primaryScore+0.2 {
			return
		}
	}

	// Perform failover
	m.performFailover(best, "health_score")
}

// performFailover switches to a new primary node.
func (m *Manager) performFailover(newPrimary *ManagedNode, reason string) {
	oldPrimary := m.primary

	var oldScore, newScore float64
	var oldID string

	if oldPrimary != nil {
		oldPrimary.mu.RLock()
		oldScore = oldPrimary.Health.Score
		oldPrimary.mu.RUnlock()
		oldID = oldPrimary.ID

		// Stop ZMQ on old primary asynchronously to avoid deadlock.
		// BUG FIX: performFailover is called with m.mu held. ZMQ.Stop() waits
		// for goroutines to exit, and if those goroutines try to acquire m.mu
		// (through callbacks or other paths), we get a deadlock. Running Stop()
		// in a goroutine prevents this while still ensuring cleanup happens.
		if oldPrimary.ZMQ != nil {
			oldZMQ := oldPrimary.ZMQ
			go func() {
				_ = oldZMQ.Stop() // #nosec G104
			}()
		}
	}

	newPrimary.mu.RLock()
	newScore = newPrimary.Health.Score
	newPrimary.mu.RUnlock()

	// Record event
	event := FailoverEvent{
		FromNodeID: oldID,
		ToNodeID:   newPrimary.ID,
		Reason:     reason,
		OldScore:   oldScore,
		NewScore:   newScore,
		OccurredAt: time.Now(),
	}
	m.failoverHistory = append(m.failoverHistory, event)
	m.failoverCount++

	// Keep only last 100 failover events
	if len(m.failoverHistory) > 100 {
		m.failoverHistory = m.failoverHistory[len(m.failoverHistory)-100:]
	}

	// Switch primary
	m.primary = newPrimary

	m.logger.Infow("Failover completed",
		"from", oldID,
		"to", newPrimary.ID,
		"reason", reason,
		"oldScore", fmt.Sprintf("%.2f", oldScore),
		"newScore", fmt.Sprintf("%.2f", newScore),
	)

	// Start ZMQ on new primary
	if newPrimary.ZMQ != nil {
		m.startZMQOnPrimary()
	}

	// Notify callback
	if m.onFailover != nil {
		go m.onFailover(event)
	}
}

// selectBestPrimary selects the best initial primary.
func (m *Manager) selectBestPrimary() {
	m.mu.Lock()
	defer m.mu.Unlock()

	var best *ManagedNode
	bestScore := 0.0

	for _, node := range m.nodes {
		node.mu.RLock()
		score := node.Health.Score
		node.mu.RUnlock()

		if score > bestScore {
			best = node
			bestScore = score
		}
	}

	if best != nil && best != m.primary {
		m.logger.Infow("Selected primary node",
			"nodeId", best.ID,
			"score", fmt.Sprintf("%.2f", bestScore),
		)
		m.primary = best
	}
}

// startZMQOnPrimary starts ZMQ listener on the primary node.
func (m *Manager) startZMQOnPrimary() {
	if m.primary == nil || m.primary.ZMQ == nil {
		return
	}

	// Wire up block handler
	if m.onBlockNotify != nil {
		m.primary.ZMQ.SetBlockHandler(m.onBlockNotify)
	}

	// Wire up ZMQ status change handler for metrics
	if m.onZMQStatusChange != nil {
		m.primary.ZMQ.SetStatusChangeHandler(m.onZMQStatusChange)
	}

	// Start ZMQ — capture primary into local to prevent race if failover changes m.primary
	// before the goroutine executes.
	primaryNode := m.primary
	go func() {
		if err := primaryNode.ZMQ.Start(m.ctx); err != nil {
			m.logger.Warnw("Failed to start ZMQ on primary",
				"nodeId", primaryNode.ID,
				"error", err,
			)
		}
	}()
}

// ExecuteOnAll executes a function on all nodes and returns results.
// This is useful for operations like block submission that should try all nodes.
func (m *Manager) ExecuteOnAll(ctx context.Context, fn func(node *ManagedNode) error) error {
	// Sort by health score (best first)
	nodes := make([]*ManagedNode, len(m.nodes))
	copy(nodes, m.nodes)

	sort.Slice(nodes, func(i, j int) bool {
		nodes[i].mu.RLock()
		scoreI := nodes[i].Health.Score
		nodes[i].mu.RUnlock()

		nodes[j].mu.RLock()
		scoreJ := nodes[j].Health.Score
		nodes[j].mu.RUnlock()

		return scoreI > scoreJ
	})

	var lastErr error
	for _, node := range nodes {
		if err := fn(node); err == nil {
			return nil // Success
		} else {
			lastErr = err
			m.logger.Warnw("Operation failed on node",
				"nodeId", node.ID,
				"error", err,
			)
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return ErrAllNodesFailed
}
