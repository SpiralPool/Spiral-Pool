// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package nodemanager

import (
	"context"
	"fmt"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// DaemonConfig wraps config for creating daemon clients.
// This adapter allows NodeManager to create daemon clients from its NodeConfig.
type DaemonConfig = config.DaemonConfig

// CreateDaemonClient creates a daemon client from a NodeConfig.
func CreateDaemonClient(node *NodeConfig, logger *zap.Logger) *daemon.Client {
	daemonCfg := &config.DaemonConfig{
		Host:     node.Host,
		Port:     node.Port,
		User:     node.User,
		Password: node.Password,
	}
	return daemon.NewClient(daemonCfg, logger)
}

// CreateZMQListener creates a ZMQ listener from NodeConfig if enabled.
// CRITICAL: Must copy ALL timing fields - these are tuned per-coin based on block time.
// Fast coins (DGB 15s) need aggressive timing, slow coins (BTC 600s) use relaxed timing.
func CreateZMQListener(node *NodeConfig, logger *zap.Logger) *daemon.ZMQListener {
	if node.ZMQ == nil || !node.ZMQ.Enabled {
		return nil
	}

	zmqCfg := &config.ZMQConfig{
		Enabled:             true,
		Endpoint:            node.ZMQ.Endpoint,
		ReconnectInitial:    node.ZMQ.ReconnectInitial,
		ReconnectMax:        node.ZMQ.ReconnectMax,
		ReconnectFactor:     node.ZMQ.ReconnectFactor,
		FailureThreshold:    node.ZMQ.FailureThreshold,
		StabilityPeriod:     node.ZMQ.StabilityPeriod,
		HealthCheckInterval: node.ZMQ.HealthCheckInterval,
	}
	return daemon.NewZMQListener(zmqCfg, logger)
}

// NewManagerFromConfigs creates a NodeManager from configuration structs.
func NewManagerFromConfigs(coin string, configs []NodeConfig, logger *zap.Logger) (*Manager, error) {
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
		client := CreateDaemonClient(&cfgCopy, logger)

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

		// Setup ZMQ if configured
		if cfg.ZMQ != nil && cfg.ZMQ.Enabled {
			node.ZMQ = CreateZMQListener(&cfgCopy, logger)
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

	// Sort and set primary
	m.sortNodesByPriority()
	m.primary = m.nodes[0]
	log.Infow("Initial primary node set", "nodeId", m.primary.ID)

	return m, nil
}

// sortNodesByPriority sorts nodes by priority (lower = better).
func (m *Manager) sortNodesByPriority() {
	// Simple bubble sort for small slice
	for i := 0; i < len(m.nodes)-1; i++ {
		for j := 0; j < len(m.nodes)-i-1; j++ {
			if m.nodes[j].Priority > m.nodes[j+1].Priority {
				m.nodes[j], m.nodes[j+1] = m.nodes[j+1], m.nodes[j]
			}
		}
	}
}

// SetMultiAlgoParam configures the algorithm parameter on all managed nodes.
// This propagates to each daemon client for multi-algo getblocktemplate calls.
func (m *Manager) SetMultiAlgoParam(param string) {
	for _, node := range m.nodes {
		node.Client.SetMultiAlgoParam(param)
	}
	m.logger.Infow("Multi-algo parameter set on all nodes", "algo", param, "nodeCount", len(m.nodes))
}

// SetGBTRules configures the GBT rules on all managed nodes.
// This propagates to each daemon client for custom getblocktemplate rules.
func (m *Manager) SetGBTRules(rules []string) {
	for _, node := range m.nodes {
		node.Client.SetGBTRules(rules)
	}
	m.logger.Infow("GBT rules set on all nodes", "rules", rules, "nodeCount", len(m.nodes))
}

// GetBlockTemplate gets a block template from the best available node.
func (m *Manager) GetBlockTemplate(ctx context.Context) (*daemon.BlockTemplate, error) {
	node, err := m.SelectNode(OpRead)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	template, err := node.Client.GetBlockTemplate(ctx)
	responseTime := time.Since(start)

	if err != nil {
		m.monitor.RecordFailure(node, err)
		// BUG FIX: Only try other nodes if there ARE other nodes.
		// For single-node setups, getBlockTemplateFromOthers would skip the
		// only node (it's excluded) and return ErrAllNodesFailed, which is
		// misleading. Just return the actual RPC error instead.
		if len(m.nodes) > 1 {
			return m.getBlockTemplateFromOthers(ctx, node.ID)
		}
		return nil, err
	}

	m.monitor.RecordSuccess(node, responseTime)
	return template, nil
}

// getBlockTemplateFromOthers tries to get template from nodes other than the failed one.
func (m *Manager) getBlockTemplateFromOthers(ctx context.Context, excludeNodeID string) (*daemon.BlockTemplate, error) {
	for _, node := range m.nodes {
		if node.ID == excludeNodeID {
			continue
		}

		start := time.Now()
		template, err := node.Client.GetBlockTemplate(ctx)
		responseTime := time.Since(start)

		if err != nil {
			m.monitor.RecordFailure(node, err)
			continue
		}

		m.monitor.RecordSuccess(node, responseTime)
		return template, nil
	}

	return nil, ErrAllNodesFailed
}

// SubmitBlock submits a block to all healthy nodes for redundancy.
// Returns on first success, but continues trying others if primary fails.
func (m *Manager) SubmitBlock(ctx context.Context, blockHex string) error {
	m.mu.RLock()
	primary := m.primary
	m.mu.RUnlock()

	// Try primary first
	if primary != nil {
		start := time.Now()
		err := primary.Client.SubmitBlock(ctx, blockHex)
		responseTime := time.Since(start)

		if err == nil {
			m.monitor.RecordSuccess(primary, responseTime)
			m.logger.Infow("Block submitted successfully via primary node",
				"nodeId", primary.ID,
				"responseTime", responseTime,
			)
			return nil
		}

		m.monitor.RecordFailure(primary, err)
		m.logger.Warnw("Primary node block submission failed, trying others",
			"nodeId", primary.ID,
			"error", err,
		)
	}

	// Try all other nodes
	for _, node := range m.nodes {
		if primary != nil && node.ID == primary.ID {
			continue
		}

		start := time.Now()
		err := node.Client.SubmitBlock(ctx, blockHex)
		responseTime := time.Since(start)

		if err == nil {
			m.monitor.RecordSuccess(node, responseTime)
			m.logger.Infow("Block submitted successfully via backup node",
				"nodeId", node.ID,
				"responseTime", responseTime,
			)
			return nil
		}

		m.monitor.RecordFailure(node, err)
		m.logger.Warnw("Backup node block submission failed",
			"nodeId", node.ID,
			"error", err,
		)
	}

	return ErrAllNodesFailed
}

// GetBlockchainInfo gets blockchain info from the best available node.
func (m *Manager) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	node, err := m.SelectNode(OpRead)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	info, err := node.Client.GetBlockchainInfo(ctx)
	responseTime := time.Since(start)

	if err != nil {
		m.monitor.RecordFailure(node, err)
		return nil, err
	}

	m.monitor.RecordSuccess(node, responseTime)
	return info, nil
}

// GetDifficulty gets current network difficulty.
func (m *Manager) GetDifficulty(ctx context.Context) (float64, error) {
	node, err := m.SelectNode(OpRead)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	diff, err := node.Client.GetDifficulty(ctx)
	responseTime := time.Since(start)

	if err != nil {
		m.monitor.RecordFailure(node, err)
		return 0, err
	}

	m.monitor.RecordSuccess(node, responseTime)
	return diff, nil
}

// ValidateAddress validates an address using any healthy node.
func (m *Manager) ValidateAddress(ctx context.Context, address string) (bool, error) {
	node, err := m.SelectNode(OpRead)
	if err != nil {
		return false, err
	}

	start := time.Now()
	valid, err := node.Client.ValidateAddress(ctx, address)
	responseTime := time.Since(start)

	if err != nil {
		m.monitor.RecordFailure(node, err)
		return false, err
	}

	m.monitor.RecordSuccess(node, responseTime)
	return valid, nil
}

// Ping checks if any node is responsive.
func (m *Manager) Ping(ctx context.Context) error {
	for _, node := range m.nodes {
		start := time.Now()
		err := node.Client.Ping(ctx)
		responseTime := time.Since(start)

		if err == nil {
			m.monitor.RecordSuccess(node, responseTime)
			return nil
		}

		m.monitor.RecordFailure(node, err)
	}

	return ErrNoHealthyNodes
}

// GetBlockHash gets block hash at height from best node.
func (m *Manager) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	node, err := m.SelectNode(OpRead)
	if err != nil {
		return "", err
	}

	start := time.Now()
	hash, err := node.Client.GetBlockHash(ctx, height)
	responseTime := time.Since(start)

	if err != nil {
		m.monitor.RecordFailure(node, err)
		return "", err
	}

	m.monitor.RecordSuccess(node, responseTime)
	return hash, nil
}

// GetBlock gets block info by hash from best node.
func (m *Manager) GetBlock(ctx context.Context, blockHash string) (map[string]interface{}, error) {
	node, err := m.SelectNode(OpRead)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	blockInfo, err := node.Client.GetBlock(ctx, blockHash)
	responseTime := time.Since(start)

	if err != nil {
		m.monitor.RecordFailure(node, err)
		return nil, err
	}

	m.monitor.RecordSuccess(node, responseTime)
	return blockInfo, nil
}

// HasZMQ returns true if any node has ZMQ enabled.
func (m *Manager) HasZMQ() bool {
	for _, node := range m.nodes {
		if node.ZMQ != nil {
			return true
		}
	}
	return false
}

// IsZMQStable returns true if the primary node's ZMQ is stable.
func (m *Manager) IsZMQStable() bool {
	m.mu.RLock()
	primary := m.primary
	m.mu.RUnlock()

	if primary == nil || primary.ZMQ == nil {
		return false
	}

	if !primary.ZMQ.IsRunning() {
		return false
	}

	stats := primary.ZMQ.Stats()
	return stats.StabilityReached
}

// IsZMQFailed returns true if the primary node's ZMQ has failed.
func (m *Manager) IsZMQFailed() bool {
	m.mu.RLock()
	primary := m.primary
	m.mu.RUnlock()

	if primary == nil || primary.ZMQ == nil {
		return false
	}

	return primary.ZMQ.IsFailed()
}

// SubmitBlockWithVerification submits a block via the primary node with full verification.
// Combines submitblock + preciousblock + getblockhash verification in a single call (V1 parity).
// If the primary node fails both submit and verify, falls back to other healthy nodes.
func (m *Manager) SubmitBlockWithVerification(ctx context.Context, blockHex string, blockHash string, height uint64, timeouts *daemon.SubmitTimeouts) *daemon.BlockSubmitResult {
	m.mu.RLock()
	primary := m.primary
	m.mu.RUnlock()

	// Try primary first
	if primary != nil {
		start := time.Now()
		result := primary.Client.SubmitBlockWithVerification(ctx, blockHex, blockHash, height, timeouts)
		responseTime := time.Since(start)

		if result.Submitted || result.Verified {
			m.monitor.RecordSuccess(primary, responseTime)
			m.logger.Infow("Block submitted with verification via primary node",
				"nodeId", primary.ID,
				"submitted", result.Submitted,
				"verified", result.Verified,
				"responseTime", responseTime,
			)
			return result
		}

		m.monitor.RecordFailure(primary, result.SubmitErr)
		m.logger.Warnw("Primary node block submission+verification failed, trying others",
			"nodeId", primary.ID,
			"submitErr", result.SubmitErr,
			"verifyErr", result.VerifyErr,
		)
	}

	// Try all other nodes
	for _, node := range m.nodes {
		if primary != nil && node.ID == primary.ID {
			continue
		}

		start := time.Now()
		result := node.Client.SubmitBlockWithVerification(ctx, blockHex, blockHash, height, timeouts)
		responseTime := time.Since(start)

		if result.Submitted || result.Verified {
			m.monitor.RecordSuccess(node, responseTime)
			m.logger.Infow("Block submitted with verification via backup node",
				"nodeId", node.ID,
				"submitted", result.Submitted,
				"verified", result.Verified,
				"responseTime", responseTime,
			)
			return result
		}

		m.monitor.RecordFailure(node, result.SubmitErr)
	}

	// All nodes failed
	return &daemon.BlockSubmitResult{
		SubmitErr: ErrAllNodesFailed,
	}
}
