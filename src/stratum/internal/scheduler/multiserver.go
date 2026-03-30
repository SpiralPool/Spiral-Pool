// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/stratum"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// CoinPoolHandle provides the interface that MultiServer needs from each CoinPool.
// This decouples the multi-server from the concrete CoinPool type.
type CoinPoolHandle interface {
	Symbol() string
	PoolID() string
	IsRunning() bool
	GetNetworkDifficulty() float64
	GetStratumPort() int

	// Job access: get the current job template from this coin's job manager
	GetCurrentJob() *protocol.Job

	// Share processing: submit a share to this coin's share pipeline
	HandleMultiPortShare(share *protocol.Share) *protocol.ShareResult
}

// MultiServerConfig holds configuration for the multi-coin stratum server.
type MultiServerConfig struct {
	// Network
	Port    int
	TLSPort int

	// Scheduling
	CheckInterval  time.Duration // how often to check schedule (default 30s)

	// Coin routing
	AllowedCoins  []string     // which coins participate
	CoinWeights   []CoinWeight // per-coin weights (maps to 24h UTC time slots)
	PreferCoin    string       // tie-breaker / default
	MinTimeOnCoin time.Duration // minimum time before switch (default 60s)

	// Stratum settings (shared from global or first coin)
	Stratum *config.StratumConfig

	Logger *zap.Logger
}

// MultiServer is the multi-coin "smart port" stratum server.
// It wraps a standard stratum.Server and routes miners to the optimal coin
// based on network difficulty, hot-swapping job templates when conditions change.
type MultiServer struct {
	cfg    MultiServerConfig
	logger *zap.SugaredLogger

	// Underlying stratum server for the multi port
	server *stratum.Server

	// Coin pool handles indexed by symbol
	coinPools   map[string]CoinPoolHandle
	coinPoolsMu sync.RWMutex

	// Difficulty monitoring and coin selection
	monitor  *Monitor
	selector *Selector

	// Per-session coin tracking
	// Maps session ID -> symbol of the coin currently assigned
	sessionCoin sync.Map // map[uint64]string

	// Per-session miner class tracking (for re-evaluation)
	sessionClass sync.Map // map[uint64]stratum.MinerClass

	// Stale share grace period: maps session ID -> time of last coin switch
	// Shares submitted within graceWindow after a switch are accepted at old coin
	switchGrace sync.Map // map[uint64]switchGraceState
	graceWindow time.Duration

	// Metrics
	totalSwitches atomic.Uint64
	activeSessions atomic.Int64

	// Lifecycle
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type switchGraceState struct {
	fromCoin  string
	switchedAt time.Time
}

// NewMultiServer creates a new multi-coin stratum server.
func NewMultiServer(cfg MultiServerConfig, monitor *Monitor, selector *Selector) *MultiServer {
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 30 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	ms := &MultiServer{
		cfg:         cfg,
		logger:      logger.Sugar().Named("multi-server"),
		coinPools:   make(map[string]CoinPoolHandle),
		monitor:     monitor,
		selector:    selector,
		graceWindow: 10 * time.Second, // accept stale shares for 10s after switch
	}

	return ms
}

// RegisterCoinPool adds a coin pool as a routing target.
func (ms *MultiServer) RegisterCoinPool(pool CoinPoolHandle) {
	ms.coinPoolsMu.Lock()
	defer ms.coinPoolsMu.Unlock()
	ms.coinPools[pool.Symbol()] = pool
	ms.logger.Infow("Registered coin pool for multi-port routing",
		"symbol", pool.Symbol(),
		"poolId", pool.PoolID(),
	)
}

// Start creates and starts the multi-port stratum server.
func (ms *MultiServer) Start(ctx context.Context) error {
	ctx, ms.cancel = context.WithCancel(ctx)

	// Create the stratum config for the multi port
	stratumCfg := ms.cfg.Stratum
	if stratumCfg == nil {
		return fmt.Errorf("stratum config is required for multi-server")
	}

	// Override the listen address with the multi port
	stratumCfg.Listen = fmt.Sprintf("0.0.0.0:%d", ms.cfg.Port)

	// Create the stratum server
	ms.server = stratum.NewServer(stratumCfg, ms.cfg.Logger)

	// Wire up handlers
	ms.server.SetShareHandler(ms.handleShare)
	ms.server.SetConnectHandler(ms.handleConnect)
	ms.server.SetDisconnectHandler(ms.handleDisconnect)
	ms.server.SetMinerClassifiedHandler(ms.handleMinerClassified)

	// Start the stratum server
	if err := ms.server.Start(ctx); err != nil {
		return fmt.Errorf("failed to start multi-port stratum: %w", err)
	}

	// Start the re-evaluation loop
	ms.wg.Add(1)
	go ms.evaluationLoop(ctx)

	// Subscribe to difficulty events for immediate re-evaluation
	ms.wg.Add(1)
	go ms.difficultyEventLoop(ctx)

	ms.logger.Infow("Multi-coin stratum server started",
		"port", ms.cfg.Port,
		"coins", ms.cfg.AllowedCoins,
		"checkInterval", ms.cfg.CheckInterval,
	)

	return nil
}

// Stop gracefully shuts down the multi-port server.
func (ms *MultiServer) Stop() error {
	if ms.cancel != nil {
		ms.cancel()
	}
	ms.wg.Wait()

	if ms.server != nil {
		return ms.server.Stop()
	}
	return nil
}

// handleConnect is called when a new miner connects to the multi port.
func (ms *MultiServer) handleConnect(session *protocol.Session) {
	ms.activeSessions.Add(1)

	// Assign initial coin (prefer coin or first running)
	ms.coinPoolsMu.RLock()
	initialCoin := ms.cfg.PreferCoin
	if pool, ok := ms.coinPools[initialCoin]; !ok || !pool.IsRunning() {
		// Prefer coin not available or not running, pick first running
		initialCoin = ""
		for _, sym := range ms.cfg.AllowedCoins {
			if pool, exists := ms.coinPools[sym]; exists && pool.IsRunning() {
				initialCoin = sym
				break
			}
		}
	}
	ms.coinPoolsMu.RUnlock()

	if initialCoin == "" {
		ms.logger.Errorw("No coin pools available for multi-port session",
			"sessionId", session.ID,
		)
		return
	}

	ms.sessionCoin.Store(session.ID, initialCoin)
	ms.selector.AssignCoin(session.ID, initialCoin, session.WorkerName, "unknown")

	// Send the initial coin's current job
	ms.sendCoinJob(session, initialCoin, true)

	ms.logger.Infow("Multi-port session connected",
		"sessionId", session.ID,
		"initialCoin", initialCoin,
	)
}

// handleDisconnect cleans up when a miner disconnects.
func (ms *MultiServer) handleDisconnect(session *protocol.Session) {
	ms.activeSessions.Add(-1)
	ms.sessionCoin.Delete(session.ID)
	ms.sessionClass.Delete(session.ID)
	ms.switchGrace.Delete(session.ID)
	ms.selector.RemoveSession(session.ID)
}

// handleMinerClassified is called when Spiral Router classifies the miner.
// This triggers a re-evaluation of the optimal coin for this miner's hashrate class.
func (ms *MultiServer) handleMinerClassified(sessionID uint64, profile stratum.MinerProfile) {
	ms.sessionClass.Store(sessionID, profile.Class)

	// Re-evaluate coin assignment now that we know the miner class
	selection := ms.selector.SelectCoin(sessionID)
	if selection.Changed {
		ms.switchSessionCoin(sessionID, selection.Symbol, selection.Reason)
	}
}

// handleShare routes a share to the correct coin pool's share pipeline.
func (ms *MultiServer) handleShare(share *protocol.Share) *protocol.ShareResult {
	sessionID := share.SessionID

	// Determine which coin this share is for
	coinSymbol, ok := ms.sessionCoin.Load(sessionID)
	if !ok {
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: "session not assigned to any coin",
		}
	}
	symbol := coinSymbol.(string)

	// Check grace period: if the miner was recently switched, allow shares
	// for the previous coin during the grace window
	if graceVal, ok := ms.switchGrace.Load(sessionID); ok {
		grace := graceVal.(switchGraceState)
		if time.Since(grace.switchedAt) < ms.graceWindow {
			// Accept share for either old or new coin
			// Route to the old coin if the job matches
			ms.coinPoolsMu.RLock()
			if oldPool, exists := ms.coinPools[grace.fromCoin]; exists && oldPool.IsRunning() {
				// Try old pool first — if the job belongs to it, it'll accept
				result := oldPool.HandleMultiPortShare(share)
				if result != nil && result.Accepted {
					ms.coinPoolsMu.RUnlock()
					return result
				}
			}
			ms.coinPoolsMu.RUnlock()
		} else {
			// Grace period expired, clean up
			ms.switchGrace.Delete(sessionID)
		}
	}

	// Route to the assigned coin's pool
	// Hold RLock through HandleMultiPortShare to prevent TOCTOU: pool could be
	// stopped or removed between the existence check and the share submission.
	ms.coinPoolsMu.RLock()
	pool, exists := ms.coinPools[symbol]
	if !exists || !pool.IsRunning() {
		ms.coinPoolsMu.RUnlock()
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: fmt.Sprintf("coin pool %s not available", symbol),
		}
	}
	result := pool.HandleMultiPortShare(share)
	ms.coinPoolsMu.RUnlock()

	return result
}

// switchSessionCoin hot-swaps a session from one coin to another.
func (ms *MultiServer) switchSessionCoin(sessionID uint64, newCoin, reason string) {
	oldCoinVal, ok := ms.sessionCoin.Load(sessionID)
	if !ok {
		return
	}
	oldCoin := oldCoinVal.(string)
	if oldCoin == newCoin {
		return
	}

	// Record grace period for in-flight shares
	ms.switchGrace.Store(sessionID, switchGraceState{
		fromCoin:   oldCoin,
		switchedAt: time.Now(),
	})

	// Update assignment
	ms.sessionCoin.Store(sessionID, newCoin)

	// Get worker name for logging
	workerName := ""
	minerClass := "unknown"
	if classVal, ok := ms.sessionClass.Load(sessionID); ok {
		minerClass = classVal.(stratum.MinerClass).String()
	}

	ms.selector.AssignCoin(sessionID, newCoin, workerName, minerClass)
	ms.totalSwitches.Add(1)

	// Send new coin's job to the miner with clean_jobs=true
	if session, ok := ms.server.GetSession(sessionID); ok {
		ms.sendCoinJob(session, newCoin, true)
	}

	ms.logger.Infow("Switched miner to new coin",
		"sessionId", sessionID,
		"from", oldCoin,
		"to", newCoin,
		"reason", reason,
		"minerClass", minerClass,
	)
}

// sendCoinJob sends the current job from a coin pool to a session.
func (ms *MultiServer) sendCoinJob(session *protocol.Session, coinSymbol string, cleanJobs bool) {
	// Hold RLock through GetCurrentJob to prevent TOCTOU: pool could be
	// stopped between the existence check and the job fetch.
	ms.coinPoolsMu.RLock()
	pool, exists := ms.coinPools[coinSymbol]
	if !exists || !pool.IsRunning() {
		ms.coinPoolsMu.RUnlock()
		return
	}
	job := pool.GetCurrentJob()
	ms.coinPoolsMu.RUnlock()

	if job == nil {
		return
	}

	// Override clean_jobs flag for coin switches
	if cleanJobs {
		// Clone the job to avoid copying the embedded sync.RWMutex
		switchJob := job.Clone()
		switchJob.CleanJobs = true
		ms.server.SendJobToSession(session, switchJob)
	} else {
		ms.server.SendJobToSession(session, job)
	}
}

// evaluationLoop periodically re-evaluates coin assignments for all connected miners.
func (ms *MultiServer) evaluationLoop(ctx context.Context) {
	defer ms.wg.Done()

	ticker := time.NewTicker(ms.cfg.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ms.reevaluateAll()
		}
	}
}

// difficultyEventLoop subscribes to difficulty changes and triggers re-evaluation.
func (ms *MultiServer) difficultyEventLoop(ctx context.Context) {
	defer ms.wg.Done()

	ch := ms.monitor.Subscribe()
	defer ms.monitor.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			// Significant difficulty change — re-evaluate all sessions
			if math.Abs(event.ChangePercent) > 5 {
				ms.logger.Infow("Significant difficulty change, re-evaluating all miners",
					"symbol", event.Symbol,
					"changePct", event.ChangePercent,
				)
				ms.reevaluateAll()
			}
		}
	}
}

// reevaluateAll checks all connected sessions against the current schedule.
func (ms *MultiServer) reevaluateAll() {
	ms.sessionCoin.Range(func(key, value any) bool {
		sessionID := key.(uint64)

		selection := ms.selector.SelectCoin(sessionID)
		if selection.Changed {
			ms.switchSessionCoin(sessionID, selection.Symbol, selection.Reason)
		}
		return true
	})
}

// Stats returns multi-server statistics for the dashboard.
type MultiServerStats struct {
	Port             int
	ActiveSessions   int64
	TotalSwitches    uint64
	CoinDistribution map[string]int
	AllowedCoins     []string
	CoinWeights      map[string]int // symbol → weight %
}

func (ms *MultiServer) Stats() MultiServerStats {
	stats := MultiServerStats{
		Port:             ms.cfg.Port,
		ActiveSessions:   ms.activeSessions.Load(),
		TotalSwitches:    ms.totalSwitches.Load(),
		CoinDistribution: ms.selector.GetCoinDistribution(),
		AllowedCoins:     ms.cfg.AllowedCoins,
	}
	if len(ms.cfg.CoinWeights) > 0 {
		stats.CoinWeights = make(map[string]int, len(ms.cfg.CoinWeights))
		for _, cw := range ms.cfg.CoinWeights {
			stats.CoinWeights[cw.Symbol] = cw.Weight
		}
	}
	return stats
}

// GetSwitchHistory returns recent coin switch events.
func (ms *MultiServer) GetSwitchHistory(limit int) []SwitchEvent {
	return ms.selector.GetSwitchHistory(limit)
}

// GetServer returns the underlying stratum server (for metrics/monitoring).
func (ms *MultiServer) GetServer() *stratum.Server {
	return ms.server
}
