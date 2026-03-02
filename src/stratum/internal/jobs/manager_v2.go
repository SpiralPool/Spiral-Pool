// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package jobs - V2 job manager adapter for NodeManager integration.
//
// This provides a V2-compatible job manager that uses NodeManager
// instead of a single daemon client for multi-node failover support.
package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/nodemanager"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// ManagerV2 is the V2 job manager with NodeManager support.
// It embeds the base Manager but overrides template fetching to use NodeManager.
type ManagerV2 struct {
	*Manager
	nodeManager *nodemanager.Manager
}

// NewManagerV2 creates a new V2 job manager using NodeManager for daemon operations.
// Returns an error if coin initialization fails (invalid coin or address).
func NewManagerV2(cfg *config.PoolConfig, stratumCfg *config.StratumConfig, nodeManager *nodemanager.Manager, logger *zap.Logger) (*ManagerV2, error) {
	log := logger.Sugar()

	// Get the primary node's client to satisfy the base Manager's interface
	primary := nodeManager.GetPrimary()
	if primary == nil {
		log.Warn("No primary node available, ManagerV2 may not function correctly")
	}

	var daemonClient *daemon.Client
	if primary != nil {
		daemonClient = primary.Client
	}

	// CRITICAL FIX: Initialize coin implementation and output script
	// Without this, buildCoinbase() returns nil output script causing invalid blocks
	coinSymbol := extractCoinSymbol(cfg.Coin)
	coinImpl, err := coin.Create(coinSymbol)
	if err != nil {
		return nil, fmt.Errorf("unsupported coin '%s' (extracted symbol: %s): %w", cfg.Coin, coinSymbol, err)
	}

	// Build and cache output script - validates pool address at startup
	outputScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
		PoolAddress: cfg.Address,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid pool address '%s' for coin %s: %w - blocks would be rejected",
			cfg.Address, coinImpl.Symbol(), err)
	}

	log.Infow("ManagerV2 initialized with coin",
		"coin", coinImpl.Symbol(),
		"coinName", coinImpl.Name(),
		"poolAddress", cfg.Address,
		"outputScriptLen", len(outputScript),
		"coinbaseText", cfg.CoinbaseText,
	)

	// BUG FIX: Calculate template staleness thresholds and job history limits.
	// Previously these were zero-valued, disabling RPC health tracking, template
	// staleness detection, and causing unbounded job history memory growth.
	blockTime := time.Duration(coinImpl.BlockTime()) * time.Second

	maxTemplateAge := blockTime * 4
	if maxTemplateAge < 1*time.Minute {
		maxTemplateAge = 1 * time.Minute
	}
	if maxTemplateAge > 10*time.Minute {
		maxTemplateAge = 10 * time.Minute
	}

	templateGracePeriod := blockTime
	if templateGracePeriod < 15*time.Second {
		templateGracePeriod = 15 * time.Second
	}
	if templateGracePeriod > 2*time.Minute {
		templateGracePeriod = 2 * time.Minute
	}

	jobIDPrefix := strings.ToLower(coinImpl.Symbol())
	if len(jobIDPrefix) > 2 {
		jobIDPrefix = jobIDPrefix[:2]
	}

	maxJobHistory := 10
	if coinImpl.BlockTime() >= 60 {
		rebroadcastSec := float64(coinImpl.BlockTime()) / 3.0
		if rebroadcastSec < 5 {
			rebroadcastSec = 5
		}
		scaled := int(float64(coinImpl.BlockTime()) * 2 / rebroadcastSec)
		if scaled > maxJobHistory {
			maxJobHistory = scaled
		}
	}
	if maxJobHistory > 50 {
		maxJobHistory = 50
	}

	// Create the base manager with all required fields including coinImpl and outputScript
	baseManager := &Manager{
		cfg:                      cfg,
		stratumCfg:               stratumCfg,
		daemonClient:             daemonClient,
		logger:                   log,
		coinImpl:                 coinImpl,
		jobs:                     make(map[string]*protocol.Job),
		coinbaseText:             cfg.CoinbaseText,
		poolAddress:              cfg.Address,
		outputScript:             outputScript,
		heightEpoch:              NewHeightEpoch(),
		maxTemplateAge:           maxTemplateAge,
		templateStaleGracePeriod: templateGracePeriod,
		jobIDPrefix:              jobIDPrefix,
		maxJobHistory:            maxJobHistory,
	}

	// Multi-algo: tell daemon clients which algorithm we're mining
	if mac, ok := coinImpl.(coin.MultiAlgoCoin); ok {
		nodeManager.SetMultiAlgoParam(mac.MultiAlgoGBTParam())
		log.Infow("Multi-algo coin detected", "gbtParam", mac.MultiAlgoGBTParam())
	}

	// Custom GBT rules: some coins require additional rules (e.g., Litecoin needs "mweb")
	if grc, ok := coinImpl.(coin.GBTRulesCoin); ok {
		nodeManager.SetGBTRules(grc.GBTRules())
		log.Infow("Custom GBT rules configured", "rules", grc.GBTRules())
	}

	return &ManagerV2{
		Manager:     baseManager,
		nodeManager: nodeManager,
	}, nil
}

// RefreshJob overrides the base implementation to use NodeManager.
// This provides automatic failover to healthy nodes.
// FIX JOB-H1: Added RPC health tracking, template staleness circuit breaker,
// and height monotonicity / reorg detection — matching V1 Manager safety checks.
func (m *ManagerV2) RefreshJob(ctx context.Context, force bool) error {
	// Use NodeManager to get block template (with automatic failover)
	template, err := m.nodeManager.GetBlockTemplate(ctx)
	if err != nil {
		// FIX JOB-H1: Track RPC failure (mirrors V1 lines 451-474)
		failures := m.rpcFailures.Add(1)

		if failures >= MaxRPCFailuresCritical {
			if !m.isCritical.Load() {
				m.isCritical.Store(true)
				m.logger.Errorw("CRITICAL: RPC failures exceeded critical threshold - STOPPING JOB SERVING",
					"consecutiveFailures", failures,
					"threshold", MaxRPCFailuresCritical,
					"lastSuccess", time.Since(time.Unix(m.lastSuccessfulRPC.Load(), 0)).Round(time.Second),
				)
			}
		} else if failures >= MaxRPCFailures {
			if !m.isDegraded.Load() {
				m.isDegraded.Store(true)
				m.logger.Warnw("WARNING: RPC health degraded - consecutive failures detected",
					"consecutiveFailures", failures,
					"threshold", MaxRPCFailures,
				)
			}
		}

		return fmt.Errorf("RPC failure (consecutive: %d): %w", failures, err)
	}

	// FIX JOB-H1: RPC success — reset failure tracking (mirrors V1 lines 477-489)
	m.rpcFailures.Store(0)
	m.lastSuccessfulRPC.Store(time.Now().Unix())
	if m.isDegraded.Load() {
		m.isDegraded.Store(false)
		m.rpcRecovered.Store(true)
		m.logger.Infow("RPC health recovered from degraded state")
	}
	if m.isCritical.Load() {
		m.isCritical.Store(false)
		m.rpcRecovered.Store(true)
		m.logger.Infow("RPC health recovered from critical state - RESUMING JOB SERVING")
	}

	// FIX JOB-H1: Template freshness validation with circuit breaker (mirrors V1 lines 491-556)
	templateTime := time.Unix(template.CurTime, 0)
	now := time.Now()
	timeDrift := templateTime.Sub(now)

	if timeDrift < -m.maxTemplateAge {
		staleStart := m.templateStaleStart.Load()
		if staleStart == 0 {
			m.templateStaleStart.Store(now.Unix())
			m.logger.Warnw("STALE TEMPLATE DETECTED: Starting grace period",
				"templateTime", templateTime.Format(time.RFC3339),
				"now", now.Format(time.RFC3339),
				"age", now.Sub(templateTime).Round(time.Second),
				"maxAge", m.maxTemplateAge,
				"gracePeriod", m.templateStaleGracePeriod,
			)
		} else {
			staleDuration := time.Since(time.Unix(staleStart, 0))
			if staleDuration > m.templateStaleGracePeriod {
				if !m.isTemplateStale.Load() {
					m.isTemplateStale.Store(true)
					m.logger.Errorw("TEMPLATE CIRCUIT BREAKER OPEN: Templates stale beyond grace period",
						"staleDuration", staleDuration.Round(time.Second),
						"gracePeriod", m.templateStaleGracePeriod,
						"templateAge", now.Sub(templateTime).Round(time.Second),
						"action", "MINERS SHOULD NOT RECEIVE NEW JOBS - hashrate would be wasted",
					)
				}
			} else {
				m.logger.Warnw("STALE TEMPLATE: Still within grace period",
					"staleDuration", staleDuration.Round(time.Second),
					"remainingGrace", (m.templateStaleGracePeriod - staleDuration).Round(time.Second),
					"templateAge", now.Sub(templateTime).Round(time.Second),
				)
			}
		}
		return fmt.Errorf("stale template: age %v exceeds maximum %v", now.Sub(templateTime), m.maxTemplateAge)
	}

	// Template is fresh — reset staleness tracking
	if m.templateStaleStart.Load() != 0 {
		m.templateStaleStart.Store(0)
		if m.isTemplateStale.Load() {
			m.isTemplateStale.Store(false)
			m.logger.Infow("TEMPLATE CIRCUIT BREAKER CLOSED: Fresh templates restored",
				"templateTime", templateTime.Format(time.RFC3339),
			)
		} else {
			m.logger.Infow("Template staleness resolved within grace period")
		}
	}

	if timeDrift > MaxTimeDrift {
		m.logger.Warnw("TIME DRIFT DETECTED: Template timestamp is in the future",
			"templateTime", templateTime.Format(time.RFC3339),
			"now", now.Format(time.RFC3339),
			"drift", timeDrift.Round(time.Second),
			"maxDrift", MaxTimeDrift,
		)
	}

	// Check if we need a new job (thread-safe read)
	m.stateMu.RLock()
	isNewBlock := template.PreviousBlockHash != m.lastBlockHash
	prevHeight := m.lastHeight
	m.stateMu.RUnlock()
	cleanJobs := isNewBlock || force

	// FIX JOB-H1: Height monotonicity + reorg detection (mirrors V1 lines 565-581)
	if prevHeight > 0 && template.Height < prevHeight {
		m.logger.Warnw("CHAIN REORG DETECTED: Template height decreased",
			"prevHeight", prevHeight,
			"newHeight", template.Height,
			"depth", prevHeight-template.Height,
			"newPrevHash", template.PreviousBlockHash,
		)
	} else if prevHeight > 0 && template.Height == prevHeight && isNewBlock {
		m.logger.Warnw("TIP REORG: Same height but different previous block hash",
			"height", template.Height,
			"newPrevHash", template.PreviousBlockHash,
		)
	}

	// Generate job using the base implementation
	// CRITICAL FIX: Pass context for proper shutdown propagation
	job, err := m.generateJob(ctx, template, cleanJobs)
	if err != nil {
		return fmt.Errorf("generate job: %w", err)
	}

	// Update state (thread-safe write)
	m.stateMu.Lock()
	m.lastBlockHash = template.PreviousBlockHash
	m.lastHeight = template.Height
	m.stateMu.Unlock()

	// Advance height epoch — cancels any in-flight submission contexts for older heights
	// OR for same height with different tip (same-height reorg protection).
	// CRITICAL FIX: Use AdvanceWithTip to detect same-height competing tips.
	m.heightEpoch.AdvanceWithTip(template.Height, template.PreviousBlockHash)

	// Store job
	m.currentJob.Store(job)
	m.storeJob(job)

	// Broadcast
	if m.onNewJob != nil {
		m.onNewJob(job)
	}

	primary := m.nodeManager.GetPrimary()
	primaryID := "unknown"
	if primary != nil {
		primaryID = primary.ID
	}

	m.logger.Debugw("Generated new job via NodeManager",
		"jobId", job.ID,
		"height", job.Height,
		"cleanJobs", cleanJobs,
		"primaryNode", primaryID,
	)

	return nil
}

// Start starts the V2 job manager with NodeManager integration.
func (m *ManagerV2) Start(ctx context.Context) error {
	// Ensure we have the latest primary node's client
	m.updateDaemonClient()

	// Register for failover notifications
	m.nodeManager.SetFailoverHandler(func(event nodemanager.FailoverEvent) {
		m.logger.Infow("Node failover detected, updating daemon client",
			"from", event.FromNodeID,
			"to", event.ToNodeID,
			"reason", event.Reason,
		)
		m.updateDaemonClient()
	})

	// Do initial job refresh
	if err := m.RefreshJob(ctx, true); err != nil {
		m.logger.Warnw("Initial job refresh failed", "error", err)
		// Don't return error - we'll retry in the refresh loop
	}

	// Start the refresh loop
	go m.refreshLoop(ctx)

	m.logger.Info("ManagerV2 started with NodeManager integration")
	return nil
}

// updateDaemonClient updates the internal daemon client to the current primary.
func (m *ManagerV2) updateDaemonClient() {
	primary := m.nodeManager.GetPrimary()
	if primary != nil && primary.Client != nil {
		m.daemonClient = primary.Client
	}
}

// refreshLoop runs the periodic job refresh.
func (m *ManagerV2) refreshLoop(ctx context.Context) {
	// Use configured job rebroadcast interval, with coin-aware fallback
	// For fast coins like DigiByte (15s blocks), we need faster rebroadcast
	interval := m.stratumCfg.JobRebroadcast
	if interval <= 0 {
		// Fallback: use 1/3 of block time, minimum 5 seconds
		if m.coinImpl != nil {
			blockTime := time.Duration(m.coinImpl.BlockTime()) * time.Second
			interval = blockTime / 3
			if interval < 5*time.Second {
				interval = 5 * time.Second
			}
		} else {
			interval = 30 * time.Second // Safe fallback
		}
	}

	// V18 FIX: When merge mining is enabled with fast aux chains, scale the
	// refresh interval down to the fastest aux chain's block time.
	// Without this, a BTC parent (600s blocks → 200s rebroadcast) with DGB aux
	// (15s blocks) would mine aux templates that are up to 200s (~13 blocks) stale,
	// wasting aux chain work. By refreshing at the aux chain's block time,
	// we keep aux templates reasonably fresh.
	if m.auxManager != nil {
		minAuxBT := m.auxManager.MinAuxBlockTime()
		if minAuxBT > 0 {
			auxInterval := time.Duration(minAuxBT) * time.Second
			if auxInterval < interval {
				m.logger.Infow("V18 FIX: Scaling job rebroadcast to fastest aux chain block time",
					"parentInterval", interval,
					"auxBlockTime", auxInterval,
					"note", "Prevents stale aux templates when parent chain is slower",
				)
				interval = auxInterval
			}
		}
	}

	m.logger.Infow("Job rebroadcast configured", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.RefreshJob(ctx, false); err != nil {
				m.logger.Warnw("Job refresh failed", "error", err)
				// OPTIMIZATION: Aggressive retry on failure (daemon recovery scenario)
				// Instead of waiting 30s for next poll, retry with exponential backoff
				// This helps the pool recover quickly when daemon comes back online
				m.aggressiveRetry(ctx)
			}
		}
	}
}

// aggressiveRetry attempts to refresh the job with exponential backoff.
// This is called after a GBT failure to quickly recover when daemon comes back.
// Backoff: 1s -> 2s -> 4s -> 8s -> 15s (max), stops after 5 attempts or success.
func (m *ManagerV2) aggressiveRetry(ctx context.Context) {
	backoff := 1 * time.Second
	maxBackoff := 15 * time.Second
	maxAttempts := 5

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if err := m.RefreshJob(ctx, false); err != nil {
				m.logger.Warnw("Aggressive retry failed",
					"attempt", attempt,
					"maxAttempts", maxAttempts,
					"nextBackoff", backoff*2,
					"error", err,
				)
				// Exponential backoff
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			} else {
				m.logger.Infow("Aggressive retry succeeded - job recovered",
					"attempt", attempt,
				)
				return // Success, exit retry loop
			}
		}
	}
	m.logger.Warnw("Aggressive retry exhausted, will wait for next poll interval")
}

// GetNodeManager returns the underlying NodeManager for direct access if needed.
func (m *ManagerV2) GetNodeManager() *nodemanager.Manager {
	return m.nodeManager
}

// AUDIT FIX (JM-1): Override OnBlockNotification to call ManagerV2's version.
// Go embedding does NOT provide virtual dispatch. Without this override,
// Manager.OnBlockNotificationWithHash calls Manager.RefreshJob (single daemon)
// instead of ManagerV2.RefreshJob (NodeManager with failover).
// This completely defeats multi-node failover during ZMQ block notifications —
// the most time-sensitive event in the mining cycle.
func (m *ManagerV2) OnBlockNotification(ctx context.Context) {
	m.OnBlockNotificationWithHash(ctx, "")
}

// OnBlockNotificationWithHash handles ZMQ block notifications using NodeManager.
// This is a copy of Manager.OnBlockNotificationWithHash but calls m.RefreshJob
// which resolves to ManagerV2.RefreshJob (uses NodeManager with failover).
func (m *ManagerV2) OnBlockNotificationWithHash(ctx context.Context, newTipHash string) {
	// IMMEDIATE HEIGHT EPOCH ADVANCE: Cancel any in-flight block submission contexts.
	m.stateMu.RLock()
	currentHeight := m.lastHeight
	m.stateMu.RUnlock()

	if newTipHash != "" {
		m.heightEpoch.AdvanceWithTip(currentHeight+1, newTipHash)
	} else {
		m.heightEpoch.Advance(currentHeight + 1)
	}

	// IMMEDIATE INVALIDATION: Mark all current jobs as stale.
	m.jobsMu.RLock()
	for _, job := range m.jobs {
		job.SetState(protocol.JobStateInvalidated, "ZMQ block notification - immediate invalidation")
	}
	m.jobsMu.RUnlock()

	// Capture current block hash to detect when template actually updates
	m.stateMu.RLock()
	oldHash := m.lastBlockHash
	m.stateMu.RUnlock()

	// Retry up to 20 times with exponential backoff waiting for fresh template
	for i := 0; i < 20; i++ {
		// m.RefreshJob resolves to ManagerV2.RefreshJob (uses NodeManager)
		if err := m.RefreshJob(ctx, i == 0); err != nil {
			m.logger.Errorw("Failed to refresh job", "error", err, "attempt", i+1)
		}

		m.stateMu.RLock()
		newHash := m.lastBlockHash
		m.stateMu.RUnlock()

		if newHash != oldHash {
			m.logger.Infow("Template updated after ZMQ", "attempts", i+1)

			if newTipHash != "" && newHash != newTipHash {
				m.logger.Warnw("V48: Cross-RPC inconsistency — ZMQ tip hash differs from template prevBlockHash",
					"zmqTipHash", newTipHash,
					"templatePrevHash", newHash,
				)
			}
			return
		}

		var delay time.Duration
		if i < 10 {
			delay = 25 * time.Millisecond
		} else {
			delay = 150 * time.Millisecond
		}
		time.Sleep(delay)
	}

	// Template never updated — force refresh anyway to prevent miner starvation
	m.logger.Warnw("Template unchanged after ZMQ - node slow")
	if err := m.RefreshJob(ctx, true); err != nil {
		m.logger.Errorw("Failed to force job refresh after slow template", "error", err)
	} else {
		m.logger.Infow("Forced job refresh despite unchanged template - miner won't starve")
	}
}
