// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package jobs manages mining job generation and distribution.
//
// Job management implements standard Stratum protocol job distribution,
// including block template handling via getblocktemplate RPC (BIP 22/23),
// job ID generation, and merkle tree construction per Bitcoin specifications.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/auxpow"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// RPC health thresholds
const (
	// MaxRPCFailures is the number of consecutive RPC failures before degraded state
	MaxRPCFailures = 3
	// MaxRPCFailuresCritical is the threshold for critical failure (stop serving jobs)
	// Increased from 10 to 25 for multi-algo coins like DGB where rapid block succession
	// can temporarily overwhelm the daemon during high-activity periods
	MaxRPCFailuresCritical = 25
	// MaxTimeDrift is the maximum acceptable time drift between daemon and pool
	MaxTimeDrift = 2 * time.Minute
)

// Default template staleness thresholds (can be overridden per-manager based on coin block time)
const (
	// DefaultMaxTemplateAge is the default maximum age of a template before it's considered stale.
	// Formula: blockTime × 4, min 1 minute, max 10 minutes
	// For DGB (15s): 1 min, for BTC (600s): 10 min (capped)
	DefaultMaxTemplateAge = 1 * time.Minute
	// DefaultTemplateStaleGracePeriod allows recovery before circuit breaker trips.
	// Formula: blockTime, min 15 seconds, max 2 minutes
	// For DGB (15s): 15s, for BTC (600s): 2 min (capped)
	DefaultTemplateStaleGracePeriod = 15 * time.Second
)

// Manager handles job generation and lifecycle.
type Manager struct {
	cfg          *config.PoolConfig
	stratumCfg   *config.StratumConfig // Stratum config for version rolling mask
	daemonClient *daemon.Client
	logger       *zap.SugaredLogger

	// Coin implementation for address handling
	coinImpl coin.Coin

	// Current job (atomic pointer for lock-free access)
	currentJob atomic.Pointer[protocol.Job]

	// Job history for share validation
	jobsMu sync.RWMutex
	jobs   map[string]*protocol.Job

	// Job ID counter
	jobCounter atomic.Uint64

	// FIX M-1: Pool ID prefix for job IDs to prevent cross-coin collision
	// Different coins could generate the same job ID counter, causing share
	// submission to wrong coin if miner connects to wrong port
	jobIDPrefix string

	// Broadcast callback
	onNewJob func(*protocol.Job)

	// Coinbase configuration
	coinbaseText string
	poolAddress  string
	outputScript []byte // SECURITY: Cached and validated at startup to avoid runtime panics

	// SOLO mining: Direct coinbase to miner's wallet (Option A)
	// When set, coinbase rewards go directly to the miner's address from their
	// stratum username, not to the pool's configured address. This is trustless
	// SOLO mining - the miner receives rewards without pool intermediary.
	soloMinerMu           sync.RWMutex
	soloMinerAddress      string // The validated miner address
	soloMinerOutputScript []byte // Pre-built output script for the miner's address

	// State (protected by stateMu for thread-safe access)
	stateMu       sync.RWMutex
	lastBlockHash string
	lastHeight    uint64

	// RPC health tracking (for dependency failure detection)
	rpcFailures       atomic.Int32 // Consecutive RPC failures
	lastSuccessfulRPC atomic.Int64 // Unix timestamp of last successful RPC
	isDegraded        atomic.Bool  // True if RPC health is degraded
	isCritical        atomic.Bool  // True if RPC has failed critically (stop serving)
	rpcRecovered      atomic.Bool  // V37 FIX: Set on recovery from degraded/critical — signals cache refresh

	// Template staleness circuit breaker
	// If templates are stale for longer than maxTemplateAge + templateStaleGracePeriod,
	// the circuit breaker trips to prevent miners from wasting hashrate.
	templateStaleStart       atomic.Int64  // Unix timestamp when staleness started (0 = not stale)
	isTemplateStale          atomic.Bool   // True if template circuit breaker is open
	maxTemplateAge           time.Duration // Scaled by coin block time
	templateStaleGracePeriod time.Duration // Scaled by coin block time

	// Merge mining (AuxPoW) support
	// When auxManager is non-nil, jobs will include aux block commitments
	auxManager    *auxpow.Manager
	auxMerkleNonce uint32 // Nonce for aux merkle tree slot calculation

	// Height epoch for submission context cancellation.
	// Cancels in-flight block submissions when the chain tip advances,
	// preventing stale RPC calls after a new block is found on the network.
	heightEpoch *HeightEpoch

	// FIX O-3: Scaled job history limit based on coin block time
	// BTC (600s) needs more history than DGB (15s) because rebroadcast intervals
	// are proportional to block time, and miners may have older jobs in-flight
	maxJobHistory int
}

// NewManager creates a new job manager.
// Returns an error if the pool address is invalid (fails early instead of panicking at runtime).
func NewManager(cfg *config.PoolConfig, stratumCfg *config.StratumConfig, daemonClient *daemon.Client, logger *zap.Logger) (*Manager, error) {
	log := logger.Sugar()

	// Determine coin symbol from config
	// cfg.Coin format is like "digibyte-sha256", "bitcoin", "bitcoincash"
	coinSymbol := extractCoinSymbol(cfg.Coin)

	// Create coin implementation for address handling
	coinImpl, err := coin.Create(coinSymbol)
	if err != nil {
		return nil, fmt.Errorf("unsupported coin '%s' (extracted symbol: %s): %w - check config.yaml coin setting", cfg.Coin, coinSymbol, err)
	}

	// SECURITY: Validate pool address and cache output script at startup
	// This prevents runtime panics if the address is invalid
	outputScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
		PoolAddress: cfg.Address,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid pool address '%s' for coin %s: %w - blocks would be rejected",
			cfg.Address, coinImpl.Symbol(), err)
	}

	// Calculate template staleness thresholds based on coin block time
	// Formula: blockTime × 4, min 1 minute, max 10 minutes for maxTemplateAge
	// Formula: blockTime, min 15 seconds, max 2 minutes for gracePeriod
	blockTime := time.Duration(coinImpl.BlockTime()) * time.Second

	// maxTemplateAge: How old a template can be before circuit breaker considers it stale
	// - 4 blocks gives reasonable buffer for daemon hiccups
	// - DGB (15s): 1 min, BTC (600s): 10 min (capped)
	maxTemplateAge := blockTime * 4
	if maxTemplateAge < 1*time.Minute {
		maxTemplateAge = 1 * time.Minute
	}
	if maxTemplateAge > 10*time.Minute {
		maxTemplateAge = 10 * time.Minute
	}

	// templateGracePeriod: How long to wait before tripping circuit breaker
	// - 1 block time gives daemon chance to recover
	// - DGB (15s): 15s, BTC (600s): 2 min (capped)
	templateGracePeriod := blockTime
	if templateGracePeriod < 15*time.Second {
		templateGracePeriod = 15 * time.Second
	}
	if templateGracePeriod > 2*time.Minute {
		templateGracePeriod = 2 * time.Minute
	}

	log.Infow("Job manager initialized with coin",
		"coin", coinImpl.Symbol(),
		"coinName", coinImpl.Name(),
		"poolAddress", cfg.Address,
		"outputScriptLen", len(outputScript),
		"coinbaseText", cfg.CoinbaseText,
		"blockTime", blockTime,
		"maxTemplateAge", maxTemplateAge,
		"templateGracePeriod", templateGracePeriod,
	)

	// FIX M-1: Generate 2-char prefix from coin symbol for unique job IDs
	// This ensures job IDs from different coins never collide
	jobIDPrefix := strings.ToLower(coinImpl.Symbol())
	if len(jobIDPrefix) > 2 {
		jobIDPrefix = jobIDPrefix[:2] // Use first 2 chars (e.g., "dg" for DGB, "bt" for BTC)
	}

	// FIX O-3: Scale job history limit based on block time
	// Formula: max(10, 2 * blockTime / rebroadcastInterval), capped at 50
	// This ensures miners with in-flight jobs on slow chains aren't rejected
	maxJobHistory := 10
	if coinImpl.BlockTime() >= 60 { // 60s+ chains need more history
		rebroadcastSec := float64(coinImpl.BlockTime()) / 3.0
		if rebroadcastSec < 5 {
			rebroadcastSec = 5
		}
		scaled := int(2.0 * float64(coinImpl.BlockTime()) / rebroadcastSec)
		if scaled > maxJobHistory {
			maxJobHistory = scaled
		}
	}
	if maxJobHistory > 50 {
		maxJobHistory = 50 // Cap to prevent unbounded memory
	}

	log.Infow("Job manager configured",
		"jobIDPrefix", jobIDPrefix,
		"maxJobHistory", maxJobHistory,
	)

	// Multi-algo: tell daemon client which algorithm we're mining
	if mac, ok := coinImpl.(coin.MultiAlgoCoin); ok {
		daemonClient.SetMultiAlgoParam(mac.MultiAlgoGBTParam())
		log.Infow("Multi-algo coin detected", "gbtParam", mac.MultiAlgoGBTParam())
	}

	// Custom GBT rules: some coins require additional rules (e.g., Litecoin needs "mweb")
	if grc, ok := coinImpl.(coin.GBTRulesCoin); ok {
		daemonClient.SetGBTRules(grc.GBTRules())
		log.Infow("Custom GBT rules configured", "rules", grc.GBTRules())
	}

	return &Manager{
		cfg:                      cfg,
		stratumCfg:               stratumCfg,
		daemonClient:             daemonClient,
		logger:                   log,
		coinImpl:                 coinImpl,
		jobs:                     make(map[string]*protocol.Job),
		coinbaseText:             cfg.CoinbaseText,
		poolAddress:              cfg.Address,
		outputScript:             outputScript,
		maxTemplateAge:           maxTemplateAge,
		templateStaleGracePeriod: templateGracePeriod,
		heightEpoch:              NewHeightEpoch(),
		jobIDPrefix:              jobIDPrefix,
		maxJobHistory:            maxJobHistory,
	}, nil
}

// extractCoinSymbol extracts the coin symbol from a coin config string.
// Examples: "digibyte-sha256" -> "DGB", "bitcoin" -> "BTC", "bitcoincash" -> "BCH", "bitcoinii" -> "BC2"
func extractCoinSymbol(coinConfig string) string {
	// Map common coin config names to symbols
	coinMap := map[string]string{
		// SHA256d coins
		"digibyte":        "DGB",
		"digibyte-sha256": "DGB",
		"dgb":             "DGB",
		"bitcoin":         "BTC",
		"btc":             "BTC",
		"bitcoincash":     "BCH",
		"bitcoin-cash":    "BCH",
		"bch":             "BCH",
		"bitcoinii":       "BC2",
		"bitcoin-ii":      "BC2",
		"bitcoin2":        "BC2",
		"bc2":             "BC2",
		"namecoin":        "NMC",
		"nmc":             "NMC",
		"syscoin":         "SYS",
		"sys":             "SYS",
		"myriad":          "XMY",
		"myriadcoin":      "XMY",
		"xmy":             "XMY",
		"fractalbitcoin":  "FBTC",
		"fractal-bitcoin": "FBTC",
		"fractal":         "FBTC",
		"fbtc":            "FBTC",
		// Scrypt coins
		"digibyte-scrypt": "DGB-SCRYPT",
		"dgb-scrypt":      "DGB-SCRYPT",
		"dgb_scrypt":      "DGB-SCRYPT",
		"litecoin":        "LTC",
		"ltc":             "LTC",
		"dogecoin":        "DOGE",
		"doge":            "DOGE",
		// Additional Scrypt meme coins
		"pepecoin":  "PEP",
		"pep":       "PEP",
		"catcoin":   "CAT",
		"cat":       "CAT",
	}

	// Normalize to lowercase for matching
	normalized := strings.ToLower(coinConfig)

	if symbol, ok := coinMap[normalized]; ok {
		return symbol
	}

	// If no match, try to use the config value directly (uppercase)
	return strings.ToUpper(coinConfig)
}

// SetJobCallback sets the callback for new job broadcasts.
func (m *Manager) SetJobCallback(callback func(*protocol.Job)) {
	m.onNewJob = callback
}

// SetAuxManager enables merge mining by setting the AuxPoW manager.
// Once set, all generated jobs will include aux block commitments.
// The auxManager handles fetching aux templates and building merkle trees.
func (m *Manager) SetAuxManager(auxManager *auxpow.Manager) {
	m.auxManager = auxManager
	if auxManager != nil {
		m.logger.Infow("Merge mining enabled",
			"auxChains", auxManager.AuxChainCount(),
		)
	}
}

// GetAuxManager returns the AuxPoW manager (nil if merge mining is disabled).
func (m *Manager) GetAuxManager() *auxpow.Manager {
	return m.auxManager
}

// SetSoloMinerAddress sets the miner's wallet address for direct coinbase routing.
// This enables trustless SOLO mining where block rewards go directly to the miner's
// wallet, not to the pool's configured address. The address is validated against
// the coin's address format before being accepted.
//
// Returns an error if the address is invalid for this coin.
// Returns nil if the address was successfully set (or cleared if empty).
func (m *Manager) SetSoloMinerAddress(address string) error {
	// Empty address clears the SOLO miner (reverts to pool address)
	if address == "" {
		m.soloMinerMu.Lock()
		m.soloMinerAddress = ""
		m.soloMinerOutputScript = nil
		m.soloMinerMu.Unlock()
		m.logger.Infow("SOLO miner address cleared - using pool address for coinbase")
		return nil
	}

	// Validate and build output script for the miner's address
	outputScript, err := m.coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
		PoolAddress: address,
	})
	if err != nil {
		return fmt.Errorf("invalid miner address '%s' for %s: %w", address, m.coinImpl.Symbol(), err)
	}

	// Store the validated address and script
	m.soloMinerMu.Lock()
	m.soloMinerAddress = address
	m.soloMinerOutputScript = outputScript
	m.soloMinerMu.Unlock()

	m.logger.Infow("SOLO miner address set - coinbase rewards go directly to miner",
		"minerAddress", address,
		"coin", m.coinImpl.Symbol(),
		"outputScriptLen", len(outputScript),
	)

	return nil
}

// GetSoloMinerAddress returns the current SOLO miner's address, or empty if not set.
func (m *Manager) GetSoloMinerAddress() string {
	m.soloMinerMu.RLock()
	defer m.soloMinerMu.RUnlock()
	return m.soloMinerAddress
}

// Start begins the job manager's update loop.
// P-2 FIX: Retries initial job generation with backoff instead of dying on first failure.
// This protects against brief daemon unavailability at boot (e.g., no peers yet, RPC -9).
func (m *Manager) Start(ctx context.Context) error {
	const (
		maxStartupRetries    = 18               // 18 × 10s = 3 minutes max
		startupRetryInterval = 10 * time.Second // Match dashboard R-10 retry cadence
	)

	var lastErr error
	for attempt := 1; attempt <= maxStartupRetries; attempt++ {
		if err := m.RefreshJob(ctx, true); err != nil {
			lastErr = err
			m.logger.Warnw("Initial job generation failed, retrying...",
				"error", err,
				"attempt", attempt,
				"maxAttempts", maxStartupRetries,
				"retryIn", startupRetryInterval,
			)

			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during startup retry: %w", ctx.Err())
			case <-time.After(startupRetryInterval):
				continue
			}
		}
		lastErr = nil
		break
	}

	if lastErr != nil {
		return fmt.Errorf("failed to generate initial job after %d attempts: %w", maxStartupRetries, lastErr)
	}

	// Start periodic refresh
	go m.refreshLoop(ctx)

	m.logger.Info("Job manager started")
	return nil
}

// refreshLoop periodically refreshes jobs.
func (m *Manager) refreshLoop(ctx context.Context) {
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
				m.logger.Errorw("Failed to refresh job", "error", err)
			}
		}
	}
}

// RefreshJob fetches a new block template and generates a job.
// Includes RPC health tracking and template freshness validation.
func (m *Manager) RefreshJob(ctx context.Context, force bool) error {
	template, err := m.daemonClient.GetBlockTemplate(ctx)
	if err != nil {
		// Track RPC failure
		failures := m.rpcFailures.Add(1)

		// Check degradation thresholds
		if failures >= MaxRPCFailuresCritical {
			if !m.isCritical.Load() {
				m.isCritical.Store(true)
				m.logger.Errorw("🚨 CRITICAL: RPC failures exceeded critical threshold - STOPPING JOB SERVING",
					"consecutiveFailures", failures,
					"threshold", MaxRPCFailuresCritical,
					"lastSuccess", time.Since(time.Unix(m.lastSuccessfulRPC.Load(), 0)).Round(time.Second),
				)
			}
		} else if failures >= MaxRPCFailures {
			if !m.isDegraded.Load() {
				m.isDegraded.Store(true)
				m.logger.Warnw("⚠️ WARNING: RPC health degraded - consecutive failures detected",
					"consecutiveFailures", failures,
					"threshold", MaxRPCFailures,
				)
			}
		}

		return fmt.Errorf("RPC failure (consecutive: %d): %w", failures, err)
	}

	// RPC success - reset failure tracking
	m.rpcFailures.Store(0)
	m.lastSuccessfulRPC.Store(time.Now().Unix())
	if m.isDegraded.Load() {
		m.isDegraded.Store(false)
		m.rpcRecovered.Store(true) // V37 FIX: Signal cache refresh needed
		m.logger.Infow("✅ RPC health recovered from degraded state")
	}
	if m.isCritical.Load() {
		m.isCritical.Store(false)
		m.rpcRecovered.Store(true) // V37 FIX: Signal cache refresh needed
		m.logger.Infow("✅ RPC health recovered from critical state - RESUMING JOB SERVING")
	}

	// TEMPLATE FRESHNESS VALIDATION WITH CIRCUIT BREAKER
	// Check if template timestamp is reasonable (not too old, not future)
	templateTime := time.Unix(template.CurTime, 0)
	now := time.Now()
	timeDrift := templateTime.Sub(now)

	if timeDrift < -m.maxTemplateAge {
		// Template is stale - start or continue tracking staleness
		staleStart := m.templateStaleStart.Load()
		if staleStart == 0 {
			// First stale template - start tracking
			m.templateStaleStart.Store(now.Unix())
			m.logger.Warnw("⚠️ STALE TEMPLATE DETECTED: Starting grace period",
				"templateTime", templateTime.Format(time.RFC3339),
				"now", now.Format(time.RFC3339),
				"age", now.Sub(templateTime).Round(time.Second),
				"maxAge", m.maxTemplateAge,
				"gracePeriod", m.templateStaleGracePeriod,
			)
		} else {
			// Check if grace period has expired
			staleDuration := time.Since(time.Unix(staleStart, 0))
			if staleDuration > m.templateStaleGracePeriod {
				// CIRCUIT BREAKER TRIPS - mark templates as stale
				if !m.isTemplateStale.Load() {
					m.isTemplateStale.Store(true)
					m.logger.Errorw("🚨 TEMPLATE CIRCUIT BREAKER OPEN: Templates stale beyond grace period",
						"staleDuration", staleDuration.Round(time.Second),
						"gracePeriod", m.templateStaleGracePeriod,
						"templateAge", now.Sub(templateTime).Round(time.Second),
						"action", "MINERS SHOULD NOT RECEIVE NEW JOBS - hashrate would be wasted",
					)
				}
			} else {
				m.logger.Warnw("⚠️ STALE TEMPLATE: Still within grace period",
					"staleDuration", staleDuration.Round(time.Second),
					"remainingGrace", (m.templateStaleGracePeriod - staleDuration).Round(time.Second),
					"templateAge", now.Sub(templateTime).Round(time.Second),
				)
			}
		}
		return fmt.Errorf("stale template: age %v exceeds maximum %v", now.Sub(templateTime), m.maxTemplateAge)
	}

	// Template is fresh - reset staleness tracking
	if m.templateStaleStart.Load() != 0 {
		m.templateStaleStart.Store(0)
		if m.isTemplateStale.Load() {
			m.isTemplateStale.Store(false)
			m.logger.Infow("✅ TEMPLATE CIRCUIT BREAKER CLOSED: Fresh templates restored",
				"templateTime", templateTime.Format(time.RFC3339),
			)
		} else {
			m.logger.Infow("✅ Template staleness resolved within grace period")
		}
	}

	if timeDrift > MaxTimeDrift {
		m.logger.Warnw("⚠️ TIME DRIFT DETECTED: Template timestamp is in the future",
			"templateTime", templateTime.Format(time.RFC3339),
			"now", now.Format(time.RFC3339),
			"drift", timeDrift.Round(time.Second),
			"maxDrift", MaxTimeDrift,
		)
		// Don't reject, but log for operator awareness
	}

	// Check if we need a new job (thread-safe read)
	m.stateMu.RLock()
	isNewBlock := template.PreviousBlockHash != m.lastBlockHash
	prevHeight := m.lastHeight
	m.stateMu.RUnlock()
	cleanJobs := isNewBlock || force

	// HEIGHT MONOTONICITY CHECK: Detect chain reorganizations.
	// On 15s blockchains (DGB), reorgs happen more frequently than on BTC (600s).
	// A height decrease means the chain reorganized — all jobs at the old height are invalid.
	if prevHeight > 0 && template.Height < prevHeight {
		m.logger.Warnw("⚠️ CHAIN REORG DETECTED: Template height decreased",
			"prevHeight", prevHeight,
			"newHeight", template.Height,
			"depth", prevHeight-template.Height,
			"newPrevHash", template.PreviousBlockHash,
		)
	} else if prevHeight > 0 && template.Height == prevHeight && isNewBlock {
		// Same height but different prev hash = competing block replaced ours (reorg at tip)
		m.logger.Warnw("⚠️ TIP REORG: Same height but different previous block hash",
			"height", template.Height,
			"newPrevHash", template.PreviousBlockHash,
		)
	}

	// Generate job
	// CRITICAL FIX: Pass context for proper shutdown propagation in aux block refresh
	job, err := m.generateJob(ctx, template, cleanJobs)
	if err != nil {
		return err
	}

	// Update state (thread-safe write)
	m.stateMu.Lock()
	m.lastBlockHash = template.PreviousBlockHash
	m.lastHeight = template.Height
	m.stateMu.Unlock()

	// Advance height epoch — cancels any in-flight submission contexts for older heights
	// OR for same height with different tip (same-height reorg protection).
	// CRITICAL FIX: Use AdvanceWithTip to detect same-height competing tips.
	// template.PreviousBlockHash is the current chain tip (we're mining on top of it).
	// This is the RPC-polling path (250ms interval); ZMQ path calls Advance() separately.
	m.heightEpoch.AdvanceWithTip(template.Height, template.PreviousBlockHash)

	// Store job
	m.currentJob.Store(job)
	m.storeJob(job)

	// Broadcast
	if m.onNewJob != nil {
		m.onNewJob(job)
	}

	m.logger.Debugw("Generated new job",
		"jobId", job.ID,
		"height", job.Height,
		"cleanJobs", cleanJobs,
		"templateAge", now.Sub(templateTime).Round(time.Second),
	)

	return nil
}

// IsCritical returns true if RPC health is in critical failure state.
// When critical, job serving should be paused to prevent mining on stale data.
func (m *Manager) IsCritical() bool {
	return m.isCritical.Load()
}

// IsDegraded returns true if RPC health is degraded.
func (m *Manager) IsDegraded() bool {
	return m.isDegraded.Load()
}

// RPCHealthStatus returns the current RPC health status for monitoring.
func (m *Manager) RPCHealthStatus() (failures int32, lastSuccess time.Time, degraded, critical bool) {
	failures = m.rpcFailures.Load()
	lastSuccessTS := m.lastSuccessfulRPC.Load()
	if lastSuccessTS > 0 {
		lastSuccess = time.Unix(lastSuccessTS, 0)
	}
	degraded = m.isDegraded.Load()
	critical = m.isCritical.Load()
	return
}

// DrainRPCRecovery returns true (once) if the daemon RPC just recovered from
// a degraded or critical failure. Callers should force-refresh cached data
// (network difficulty, stats) to avoid operating on stale pre-outage values.
// V37 FIX: Prevents stale cached data after daemon recovery.
func (m *Manager) DrainRPCRecovery() bool {
	return m.rpcRecovered.CompareAndSwap(true, false)
}

// IsTemplateStale returns true if the template circuit breaker is open.
// When true, job manager should not broadcast new jobs as they would be stale.
func (m *Manager) IsTemplateStale() bool {
	return m.isTemplateStale.Load()
}

// TemplateHealthStatus returns template staleness status for monitoring.
func (m *Manager) TemplateHealthStatus() (stale bool, staleDuration time.Duration) {
	stale = m.isTemplateStale.Load()
	staleStart := m.templateStaleStart.Load()
	if staleStart > 0 {
		staleDuration = time.Since(time.Unix(staleStart, 0))
	}
	return
}

// ShouldServeJobs returns true if the job manager is healthy enough to serve jobs.
// This is the combined health check - returns false if RPC is critical OR templates are stale.
func (m *Manager) ShouldServeJobs() bool {
	return !m.isCritical.Load() && !m.isTemplateStale.Load()
}

// HeightContext returns a context that cancels when the chain height advances.
// Used by block submission paths (pool.go) to abort in-flight submissions when
// a new block arrives, preventing stale RPC calls.
// THREAD SAFETY: Safe for concurrent use.
func (m *Manager) HeightContext(parent context.Context) (context.Context, context.CancelFunc) {
	return m.heightEpoch.HeightContext(parent)
}

// GetLastBlockHash returns the current chain tip's previous block hash.
// Used by handleBlock to detect stale jobs without relying on ZMQ propagation timing.
// THREAD SAFETY: Safe for concurrent use.
func (m *Manager) GetLastBlockHash() string {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.lastBlockHash
}

// generateJob creates a new job from a block template.
// CRITICAL FIX: Added ctx parameter for proper shutdown propagation in aux block refresh
func (m *Manager) generateJob(ctx context.Context, template *daemon.BlockTemplate, cleanJobs bool) (*protocol.Job, error) {
	jobID := m.generateJobID()

	// Fetch aux blocks if merge mining is enabled
	var auxBlocks []auxpow.AuxBlockData
	var auxMerkleRoot []byte
	var auxMerkleBranch [][]byte
	var auxTreeSize uint32

	if m.auxManager != nil {
		// Refresh aux block templates
		// CRITICAL FIX: Use parent context for proper shutdown propagation
		// context.Background() ignores shutdown signals, causing goroutine leaks
		auxCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		fetchedAux, err := m.auxManager.RefreshAuxBlocks(auxCtx)
		cancel()

		if err != nil {
			// Log but don't fail - parent chain can still be mined without aux
			m.logger.Warnw("Failed to refresh aux blocks, mining parent only",
				"error", err,
			)
		} else if len(fetchedAux) > 0 {
			auxBlocks = fetchedAux
			auxMerkleRoot, auxMerkleBranch, auxTreeSize = m.auxManager.BuildAuxMerkleData(fetchedAux)

			m.logger.Debugw("Aux blocks included in job",
				"count", len(fetchedAux),
				"treeSize", auxTreeSize,
				"rootHex", hex.EncodeToString(auxMerkleRoot),
			)
		}
	}

	// Build coinbase transaction (with aux commitment if merge mining)
	coinbase1, coinbase2 := m.buildCoinbaseWithAux(template, auxMerkleRoot, auxTreeSize)

	// Build merkle branches
	merkleBranches := m.buildMerkleBranches(template)
	if merkleBranches == nil {
		return nil, fmt.Errorf("failed to build merkle branches: invalid transaction hashes in template")
	}

	// Extract and VALIDATE transaction data for block submission
	// CRITICAL: Invalid hex here would cause silent block submission failures
	txData := make([]string, len(template.Transactions))
	for i, tx := range template.Transactions {
		// Validate hex is decodable - catches node bugs or data corruption early
		if _, err := hex.DecodeString(tx.Data); err != nil {
			m.logger.Errorw("CRITICAL: Invalid transaction hex in block template - block would fail to build",
				"txIndex", i,
				"txid", tx.TxID,
				"error", err,
				"dataLen", len(tx.Data),
				"dataPreview", truncateString(tx.Data, 100),
			)
			return nil, fmt.Errorf("invalid transaction hex at index %d (txid=%s): %w", i, tx.TxID, err)
		}
		txData[i] = tx.Data
	}

	// Calculate network difficulty from bits - fail if invalid
	difficulty, err := m.bitsToTarget(template.Bits)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block template bits '%s': %w", template.Bits, err)
	}

	now := time.Now()
	formattedPrevHash := m.formatPrevHash(template.PreviousBlockHash)
	job := &protocol.Job{
		ID:             jobID,
		PrevBlockHash:  formattedPrevHash,
		CoinBase1:      hex.EncodeToString(coinbase1),
		CoinBase2:      hex.EncodeToString(coinbase2),
		MerkleBranches: merkleBranches,
		Version:        fmt.Sprintf("%08x", template.Version),
		NBits:          template.Bits,
		NTime:          fmt.Sprintf("%08x", template.CurTime),
		CleanJobs:      cleanJobs,

		Height:     template.Height,
		Difficulty: difficulty,
		CreatedAt:  now,

		// Explicit state tracking for formal verification
		State:          protocol.JobStateCreated,
		StateChangedAt: now,

		VersionRollingAllowed: m.stratumCfg.VersionRolling.Enabled,
		VersionRollingMask:    m.stratumCfg.VersionRolling.Mask,

		TransactionData:  txData,
		CoinbaseValue:    template.CoinbaseValue,
		RawPrevBlockHash: template.PreviousBlockHash,
		NetworkTarget:    template.Target,
	}

	// Validate NetworkTarget — empty means block detection falls back to compact bits
	if job.NetworkTarget == "" {
		m.logger.Warnw("Job created without GBT target — block detection using compact bits fallback",
			"jobId", jobID, "height", template.Height, "bits", template.Bits)
	}

	// Add merge mining data if present
	if len(auxBlocks) > 0 {
		job.IsMergeJob = true
		job.AuxMerkleRoot = hex.EncodeToString(auxMerkleRoot)
		job.AuxTreeSize = auxTreeSize
		job.AuxMerkleNonce = m.auxMerkleNonce

		// Convert aux blocks to protocol format
		job.AuxBlocks = make([]protocol.AuxBlockData, len(auxBlocks))
		for i, ab := range auxBlocks {
			// Convert *big.Int target to 32-byte big-endian
			var targetBytes []byte
			if ab.Target != nil {
				targetBytes = ab.Target.FillBytes(make([]byte, 32))
			}

			job.AuxBlocks[i] = protocol.AuxBlockData{
				Symbol:        ab.Symbol,
				ChainID:       ab.ChainID,
				Hash:          ab.Hash,
				Target:        targetBytes,
				Height:        ab.Height,
				CoinbaseValue: ab.CoinbaseValue,
				ChainIndex:    ab.ChainIndex,
				Difficulty:    ab.Difficulty,
				Bits:          ab.Bits,
			}
		}

		// Store aux merkle branch as hex strings (for AuxPoW proof construction)
		job.AuxMerkleBranch = make([]string, len(auxMerkleBranch))
		for i, branch := range auxMerkleBranch {
			job.AuxMerkleBranch[i] = hex.EncodeToString(branch)
		}

		m.logger.Debugw("Generated merge mining job",
			"jobId", jobID,
			"height", template.Height,
			"auxChains", len(auxBlocks),
		)
	}

	return job, nil
}

// generateJobID creates a unique job ID.
// FIX M-1: Includes coin-specific prefix to prevent cross-coin collision.
// Format: "XX" + 6 hex chars (e.g., "dg000001" for DGB, "bt000001" for BTC)
// This ensures if a miner accidentally submits to wrong port, the job lookup fails.
func (m *Manager) generateJobID() string {
	counter := m.jobCounter.Add(1)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	// Prefix with coin identifier for cross-coin isolation
	return m.jobIDPrefix + hex.EncodeToString(buf[5:]) // 2-char prefix + 3-byte counter = 8 chars
}

// buildCoinbaseWithAux constructs the coinbase transaction with optional aux commitment.
// If auxMerkleRoot is nil or empty, this behaves identically to buildCoinbase.
//
// For merge mining, the aux commitment is embedded in the scriptsig after the coinbase text:
//   [height][coinbase_text][aux_commitment][extranonce1][extranonce2]
//
// The aux commitment format (44 bytes):
//   - Magic marker: 4 bytes (0xfabe6d6d)
//   - Aux merkle root: 32 bytes
//   - Tree size: 4 bytes (uint32 LE)
//   - Merkle nonce: 4 bytes (uint32 LE)
//
// CONSENSUS-CRITICAL: The aux commitment must be placed BEFORE extranonces
// so that miners can modify extranonces without affecting the aux root position.
func (m *Manager) buildCoinbaseWithAux(template *daemon.BlockTemplate, auxMerkleRoot []byte, auxTreeSize uint32) (coinbase1, coinbase2 []byte) {
	// If no aux data, use standard coinbase
	if len(auxMerkleRoot) == 0 {
		return m.buildCoinbase(template)
	}

	// Coinbase1: version + input count + prevout + scriptsig (up to extranonce)
	cb1 := make([]byte, 0, 200)

	// Version (4 bytes, little-endian)
	cb1 = append(cb1, 0x01, 0x00, 0x00, 0x00)

	// Input count (1 byte)
	cb1 = append(cb1, 0x01)

	// Previous output (null for coinbase)
	cb1 = append(cb1, make([]byte, 32)...)    // 32 zero bytes
	cb1 = append(cb1, 0xff, 0xff, 0xff, 0xff) // -1 index

	// Block height (BIP34)
	heightBytes := encodeHeight(template.Height)

	// Use local copy to avoid race condition
	coinbaseText := m.coinbaseText

	// Build aux commitment (44 bytes)
	auxCommitment := auxpow.BuildAuxCommitment(auxMerkleRoot, auxTreeSize, m.auxMerkleNonce)

	// Scriptsig = height + coinbase text + aux commitment + extranonce1 (4 bytes) + extranonce2 (4 bytes)
	scriptsigLen := len(heightBytes) + len(coinbaseText) + len(auxCommitment) + 8

	// Validate scriptsig length doesn't exceed Bitcoin limit (100 bytes max for coinbase)
	const maxScriptsigLen = 100
	if scriptsigLen > maxScriptsigLen {
		m.logger.Errorw("CRITICAL: Merge mining scriptsig too long",
			"scriptsigLen", scriptsigLen,
			"maxLen", maxScriptsigLen,
			"heightBytes", len(heightBytes),
			"coinbaseTextBytes", len(coinbaseText),
			"auxCommitmentBytes", len(auxCommitment),
		)
		// Truncate coinbase text to fit - aux commitment takes priority for merge mining
		maxCoinbaseText := maxScriptsigLen - len(heightBytes) - len(auxCommitment) - 8
		if maxCoinbaseText < 0 {
			maxCoinbaseText = 0
		}
		if len(coinbaseText) > maxCoinbaseText {
			coinbaseText = coinbaseText[:maxCoinbaseText]
			scriptsigLen = len(heightBytes) + len(coinbaseText) + len(auxCommitment) + 8
			m.logger.Warnw("Coinbase text truncated for merge mining",
				"newLen", len(coinbaseText),
				"newScriptsigLen", scriptsigLen,
			)
		}
	}

	cb1 = append(cb1, byte(scriptsigLen))
	cb1 = append(cb1, heightBytes...)
	cb1 = append(cb1, []byte(coinbaseText)...)
	cb1 = append(cb1, auxCommitment...)

	// Space for extranonce1 (4 bytes) + extranonce2 (4 bytes) will be inserted by miner

	coinbase1 = cb1

	// Coinbase2 is identical to non-merge-mining version
	_, coinbase2 = m.buildCoinbase2Only(template)

	return coinbase1, coinbase2
}

// buildCoinbase2Only builds only the coinbase2 portion (outputs + locktime).
// This is used internally by buildCoinbaseWithAux to avoid duplicating code.
func (m *Manager) buildCoinbase2Only(template *daemon.BlockTemplate) (coinbase1Unused, coinbase2 []byte) {
	// Coinbase2: sequence + output count + outputs + locktime
	cb2 := make([]byte, 0, 128)

	// Sequence
	cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff)

	// Output count
	outputCount := byte(0x01)
	if template.DefaultWitnessCommitment != "" {
		outputCount = 0x02
	}
	cb2 = append(cb2, outputCount)

	// Output 1: Pool reward
	if template.CoinbaseValue < 0 {
		m.logger.Errorw("CRITICAL: Negative coinbase value from node", "value", template.CoinbaseValue)
		template.CoinbaseValue = 0
	}
	valueBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(valueBytes, uint64(template.CoinbaseValue))
	cb2 = append(cb2, valueBytes...)

	// Output script (pay to pool address)
	script := m.buildOutputScript()
	cb2 = append(cb2, byte(len(script)))
	cb2 = append(cb2, script...)

	// Output 2: Witness commitment (if present)
	if template.DefaultWitnessCommitment != "" {
		witnessScript, err := hex.DecodeString(template.DefaultWitnessCommitment)
		if err != nil {
			m.logger.Errorw("CRITICAL: Invalid witness commitment hex",
				"error", err,
				"commitment", template.DefaultWitnessCommitment,
			)
			// Rebuild without witness
			cb2 = make([]byte, 0, 128)
			cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff)
			cb2 = append(cb2, 0x01)
			cb2 = append(cb2, valueBytes...)
			cb2 = append(cb2, byte(len(script)))
			cb2 = append(cb2, script...)
		} else if len(witnessScript) < 2 || witnessScript[0] != 0x6a {
			m.logger.Errorw("CRITICAL: Invalid witness commitment format",
				"firstByte", fmt.Sprintf("0x%02x", witnessScript[0]),
			)
			cb2 = make([]byte, 0, 128)
			cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff)
			cb2 = append(cb2, 0x01)
			cb2 = append(cb2, valueBytes...)
			cb2 = append(cb2, byte(len(script)))
			cb2 = append(cb2, script...)
		} else {
			// Zero value for witness commitment output
			cb2 = append(cb2, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
			cb2 = append(cb2, crypto.EncodeVarInt(uint64(len(witnessScript)))...)
			cb2 = append(cb2, witnessScript...)
		}
	}

	// Locktime
	cb2 = append(cb2, 0x00, 0x00, 0x00, 0x00)

	return nil, cb2
}

// buildCoinbase constructs the coinbase transaction.
func (m *Manager) buildCoinbase(template *daemon.BlockTemplate) (coinbase1, coinbase2 []byte) {
	// Simplified coinbase construction
	// In production, this would follow the exact Bitcoin/DigiByte coinbase format

	// Coinbase1: version + input count + prevout + scriptsig (up to extranonce)
	cb1 := make([]byte, 0, 128)

	// Version (4 bytes, little-endian)
	cb1 = append(cb1, 0x01, 0x00, 0x00, 0x00)

	// Input count (1 byte)
	cb1 = append(cb1, 0x01)

	// Previous output (null for coinbase)
	cb1 = append(cb1, make([]byte, 32)...)    // 32 zero bytes
	cb1 = append(cb1, 0xff, 0xff, 0xff, 0xff) // -1 index

	// Scriptsig length placeholder (will be filled by miner)
	// Block height (BIP34)
	heightBytes := encodeHeight(template.Height)

	// Use local copy to avoid race condition - m.coinbaseText could be accessed concurrently
	coinbaseText := m.coinbaseText

	// Scriptsig = height + coinbase text + extranonce1 (4 bytes) + extranonce2 (4 bytes)
	scriptsigLen := len(heightBytes) + len(coinbaseText) + 8 // +8 for extranonce

	// Validate scriptsig length doesn't exceed Bitcoin limit (100 bytes max for coinbase)
	// This catches configuration errors early rather than producing invalid blocks
	const maxScriptsigLen = 100
	if scriptsigLen > maxScriptsigLen {
		m.logger.Errorw("CRITICAL: Scriptsig too long - coinbase text must be shortened",
			"scriptsigLen", scriptsigLen,
			"maxLen", maxScriptsigLen,
			"heightBytes", len(heightBytes),
			"coinbaseTextBytes", len(coinbaseText),
			"coinbaseText", coinbaseText,
		)
		// Truncate local copy to fit - don't modify shared state
		// Better to produce valid blocks with truncated text than reject all blocks
		maxCoinbaseText := maxScriptsigLen - len(heightBytes) - 8
		if maxCoinbaseText < 0 {
			maxCoinbaseText = 0
		}
		if len(coinbaseText) > maxCoinbaseText {
			coinbaseText = coinbaseText[:maxCoinbaseText]
			scriptsigLen = len(heightBytes) + len(coinbaseText) + 8
			m.logger.Warnw("Coinbase text truncated to fit scriptsig limit",
				"newLen", len(coinbaseText),
				"newScriptsigLen", scriptsigLen,
			)
		}
	}

	cb1 = append(cb1, byte(scriptsigLen))
	cb1 = append(cb1, heightBytes...)

	// Coinbase text
	cb1 = append(cb1, []byte(coinbaseText)...)

	// Space for extranonce1 (4 bytes) + extranonce2 (4 bytes) will be inserted by miner

	coinbase1 = cb1

	// Coinbase2: sequence + output count + outputs + locktime
	cb2 := make([]byte, 0, 128)

	// Sequence
	cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff)

	// Output count - MUST be calculated BEFORE appending outputs
	// 1 output for pool reward, optionally +1 for witness commitment
	outputCount := byte(0x01)
	if template.DefaultWitnessCommitment != "" {
		outputCount = 0x02
	}
	cb2 = append(cb2, outputCount)

	// Output 1: Pool reward (coinbase reward in satoshis, little-endian)
	// SECURITY: Validate coinbase value is non-negative (G115 fix)
	// Bitcoin max supply is 21M BTC = 2.1e15 satoshis, fits easily in int64/uint64
	if template.CoinbaseValue < 0 {
		m.logger.Errorw("CRITICAL: Negative coinbase value from node - possible attack or bug",
			"value", template.CoinbaseValue,
		)
		// Use 0 to fail safely rather than overflow
		template.CoinbaseValue = 0
	}
	valueBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(valueBytes, uint64(template.CoinbaseValue))
	cb2 = append(cb2, valueBytes...)

	// Output script (pay to pool address) - uses coin-specific script builder
	script := m.buildOutputScript()
	cb2 = append(cb2, byte(len(script)))
	cb2 = append(cb2, script...)

	// Output 2: Witness commitment (if present)
	// This is required for blocks containing SegWit transactions
	// IMPORTANT: default_witness_commitment from getblocktemplate is the FULL output script
	// (e.g., "6a24aa21a9ed..." = OP_RETURN + PUSH(36) + commitment), NOT just the commitment hash.
	// Reference: BIP 145 - https://github.com/bitcoin/bips/blob/master/bip-0145.mediawiki
	if template.DefaultWitnessCommitment != "" {
		witnessScript, err := hex.DecodeString(template.DefaultWitnessCommitment)
		if err != nil {
			// CRITICAL: If we can't decode the witness commitment, we MUST NOT
			// create a block with outputCount=2 but only 1 output. This would
			// create an invalid transaction structure.
			m.logger.Errorw("CRITICAL: Invalid witness commitment hex - block would be invalid!",
				"error", err,
				"commitment", template.DefaultWitnessCommitment,
			)
			// Revert output count to 1 - rebuild cb2 without witness commitment
			// This loses segwit transaction fees but produces a valid block
			cb2 = make([]byte, 0, 128)
			cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff) // Sequence
			cb2 = append(cb2, 0x01)                   // Output count = 1 (no witness)
			cb2 = append(cb2, valueBytes...)
			cb2 = append(cb2, byte(len(script)))
			cb2 = append(cb2, script...)
		} else {
			// Validate witness script format (should start with 0x6a = OP_RETURN)
			if len(witnessScript) < 2 || witnessScript[0] != 0x6a {
				m.logger.Errorw("CRITICAL: Invalid witness commitment format - expected OP_RETURN script",
					"firstByte", fmt.Sprintf("0x%02x", witnessScript[0]),
					"expected", "0x6a (OP_RETURN)",
				)
				// Revert to single output
				cb2 = make([]byte, 0, 128)
				cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff)
				cb2 = append(cb2, 0x01)
				cb2 = append(cb2, valueBytes...)
				cb2 = append(cb2, byte(len(script)))
				cb2 = append(cb2, script...)
			} else {
				// Value: 0 satoshis (witness commitment outputs have zero value)
				cb2 = append(cb2, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
				// Script: The full witness commitment script from the template
				// Use VarInt for script length (could be > 252 bytes in theory)
				cb2 = append(cb2, crypto.EncodeVarInt(uint64(len(witnessScript)))...)
				cb2 = append(cb2, witnessScript...)
			}
		}
	}

	// Locktime
	cb2 = append(cb2, 0x00, 0x00, 0x00, 0x00)

	coinbase2 = cb2

	return coinbase1, coinbase2
}

// buildMerkleBranches constructs the merkle branch hashes for Stratum.
// The merkle branches are the sibling hashes needed to compute the merkle root
// starting from the coinbase transaction hash.
//
// In Stratum, the miner computes: merkle_root = H(H(H(coinbase, branch[0]), branch[1]), ...)
// Each branch entry is concatenated on the RIGHT side of the running hash.
//
// For a block with transactions [coinbase, tx1, tx2, tx3]:
//
//	         root
//	        /    \
//	     H01      H23
//	    /   \    /   \
//	coinbase tx1 tx2 tx3
//
// Branches = [tx1, H23] because:
// - First, hash(coinbase, tx1) = H01
// - Then, hash(H01, H23) = root
func (m *Manager) buildMerkleBranches(template *daemon.BlockTemplate) []string {
	if len(template.Transactions) == 0 {
		// No transactions besides coinbase - no merkle branches needed
		// The merkle root is just the coinbase hash
		// IMPORTANT: Return empty slice, not nil, so JSON encodes as [] not null
		// Some older cgminer versions (like Avalon's 4.11.1) don't handle null correctly
		return []string{}
	}

	// Get transaction hashes (leaves excluding coinbase, which pool constructs separately)
	// Template transactions are everything except coinbase
	// Use a separate slice to handle errors properly without leaving nil gaps
	txHashes := make([][]byte, 0, len(template.Transactions))
	for _, tx := range template.Transactions {
		txHash, err := hex.DecodeString(tx.TxID)
		if err != nil {
			m.logger.Errorw("CRITICAL: Invalid transaction hash in template - block will be invalid",
				"txid", tx.TxID,
				"error", err,
			)
			// Return nil to signal error - better to not send job than send invalid one
			return nil
		}
		// SECURITY: Validate transaction hash length
		// All transaction hashes must be exactly 32 bytes (256 bits)
		// A corrupted daemon could return malformed hashes causing invalid merkle roots
		if len(txHash) != 32 {
			m.logger.Errorw("CRITICAL: Transaction hash wrong length - possible daemon corruption",
				"txid", tx.TxID,
				"length", len(txHash),
				"expected", 32,
			)
			return nil
		}
		// TxIDs from RPC are in display order (big-endian), but merkle tree uses little-endian
		txHashes = append(txHashes, crypto.ReverseBytes(txHash))
	}

	// Build full set of leaves: [placeholder_for_coinbase, tx1, tx2, ...]
	// We use a nil placeholder for coinbase since we only need the branches
	leaves := make([][]byte, len(txHashes)+1)
	leaves[0] = nil // Coinbase placeholder - we're tracking branches, not computing
	copy(leaves[1:], txHashes)

	// Collect branches: at each level, record the sibling of the coinbase path
	branches := []string{}
	pos := 0 // Coinbase starts at position 0

	for len(leaves) > 1 {
		// The sibling of position 'pos' at this level
		siblingPos := pos ^ 1

		// Only add a branch if the sibling exists and is not nil
		if siblingPos < len(leaves) {
			sibling := leaves[siblingPos]
			if sibling != nil {
				branches = append(branches, hex.EncodeToString(sibling))
			}
			// If sibling is nil (shouldn't happen with proper input), skip
		}
		// If siblingPos >= len(leaves), we're at an odd position at the end
		// In this case, the merkle tree duplicates the node with itself
		// but we don't need to add a branch - the miner will duplicate automatically

		// Build next level
		nextLevel := make([][]byte, (len(leaves)+1)/2)
		for i := 0; i < len(leaves); i += 2 {
			left := leaves[i]
			var right []byte
			if i+1 < len(leaves) {
				right = leaves[i+1]
			} else {
				right = left // Duplicate for odd count
			}

			// If either is nil (coinbase placeholder), result is nil (we only track path)
			if left == nil || right == nil {
				nextLevel[i/2] = nil
			} else {
				combined := append(left, right...)
				nextLevel[i/2] = crypto.SHA256d(combined)
			}
		}
		leaves = nextLevel
		pos = pos / 2
	}

	return branches
}

// formatPrevHash formats the previous block hash for stratum.
// Stratum protocol requires that after miners apply their standard per-word
// byte-swap (reverse_endianness_per_word / bswap32), the result is the correct
// internal byte order (little-endian) for the block header.
//
// To achieve this, we reverse the order of 4-byte (8 hex char) groups from the
// daemon's big-endian display format. When the miner (or buildBlockHeader) then
// byte-swaps within each group, the final result is the full byte-reversal of
// the display format — i.e., the correct internal/little-endian prevhash.
//
// Input  (daemon, big-endian display): [G0][G1][G2][G3][G4][G5][G6][G7]
// Output (stratum, group-reversed):    [G7][G6][G5][G4][G3][G2][G1][G0]
// After miner bswap32 each group:      [rev(G7)][rev(G6)]...[rev(G0)] = internal LE
func (m *Manager) formatPrevHash(hash string) string {
	if len(hash) != 64 {
		return hash
	}

	// Reverse 4-byte (8 hex char) group order.
	// Miners byte-swap within each group, producing correct internal byte order.
	var result strings.Builder
	result.Grow(64)
	for i := 56; i >= 0; i -= 8 {
		result.WriteString(hash[i : i+8])
	}

	formatted := result.String()
	m.logger.Debugw("formatPrevHash",
		"input", hash[:16]+"..."+hash[48:],
		"output", formatted[:16]+"..."+formatted[48:],
		"method", "reverse_group_order",
	)
	return formatted
}

// bitsToTarget converts the compact "bits" format to difficulty.
// The bits field is a compact representation of the target threshold.
// Format: 0xNNHHHHHH where NN is the exponent and HHHHHH is the mantissa.
// Target = mantissa * 256^(exponent-3)
// Difficulty = maxTarget / target
// Returns an error if the bits format is invalid (prevents silent fallback to 1.0).
func (m *Manager) bitsToTarget(bits string) (float64, error) {
	if len(bits) != 8 {
		return 0, fmt.Errorf("invalid bits length: expected 8 hex chars, got %d", len(bits))
	}

	bitsBytes, err := hex.DecodeString(bits)
	if err != nil {
		return 0, fmt.Errorf("invalid bits hex '%s': %w", bits, err)
	}

	// Bits are in big-endian from RPC
	compact := binary.BigEndian.Uint32(bitsBytes)

	// Extract exponent and mantissa
	exponent := compact >> 24

	// FIX: Check negative flag (bit 23) BEFORE masking to 23 bits.
	// The old code masked with 0x007FFFFF first, which already cleared bit 23,
	// making the subsequent negative flag check dead code.
	if compact&0x00800000 != 0 {
		return 0, fmt.Errorf("bits has negative flag set (invalid): 0x%08x", compact)
	}
	mantissa := compact & 0x007FFFFF

	// Calculate target = mantissa * 256^(exponent-3)
	var target big.Int
	target.SetUint64(uint64(mantissa))

	if exponent <= 3 {
		target.Rsh(&target, uint(8*(3-exponent)))
	} else {
		target.Lsh(&target, uint(8*(exponent-3)))
	}

	if target.Sign() == 0 {
		return 0, fmt.Errorf("bits resulted in zero target: 0x%08x", compact)
	}

	// MaxTarget for Bitcoin/DigiByte (difficulty 1)
	// 0x00000000FFFF0000000000000000000000000000000000000000000000000000
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Difficulty = maxTarget / target
	// Use floating point for the division
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	targetFloat := new(big.Float).SetInt(&target)

	difficulty := new(big.Float).Quo(maxTargetFloat, targetFloat)
	result, _ := difficulty.Float64()

	// Sanity check the result
	if result <= 0 {
		return 0, fmt.Errorf("calculated difficulty is non-positive: %f", result)
	}

	return result, nil
}

// storeJob saves a job for later share validation.
func (m *Manager) storeJob(job *protocol.Job) {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()

	// CRITICAL: If cleanJobs is set (new block found), invalidate ALL existing jobs.
	// This prevents accepting shares on the old blockchain branch after a reorg.
	// NOTE: We invalidate but do NOT delete from the map. The ZMQ handler already
	// marks jobs as invalidated, and keeping them in the map allows the validator
	// to return "stale" (proper rejection) instead of "job-not-found" (lost context).
	// The maxJobHistory overflow eviction below handles cleanup.
	if job.CleanJobs {
		for _, oldJob := range m.jobs {
			oldJob.SetState(protocol.JobStateInvalidated, "new block found - cleanJobs")
		}
	}

	// Transition job to Active state when stored
	job.SetState(protocol.JobStateActive, "")

	m.jobs[job.ID] = job

	// FIX O-3: Cleanup old jobs (scaled by block time, default 10 if not configured)
	maxHistory := m.maxJobHistory
	if maxHistory <= 0 {
		maxHistory = 10
	}
	if len(m.jobs) > maxHistory {
		var oldest string
		var oldestTime time.Time
		first := true // SECURITY: Track first iteration to properly initialize oldestTime

		for id, j := range m.jobs {
			// On first iteration, always set oldest regardless of time
			// On subsequent iterations, only update if this job is older
			if first || j.CreatedAt.Before(oldestTime) {
				oldest = id
				oldestTime = j.CreatedAt
				first = false
			}
		}

		if oldest != "" {
			// Mark old job as invalidated before removal
			if oldJob, exists := m.jobs[oldest]; exists {
				oldJob.SetState(protocol.JobStateInvalidated, "evicted from job history")
			}
			delete(m.jobs, oldest)
		}
	}
}

// GetJob returns a job by ID.
func (m *Manager) GetJob(id string) (*protocol.Job, bool) {
	m.jobsMu.RLock()
	defer m.jobsMu.RUnlock()
	job, ok := m.jobs[id]
	return job, ok
}

// GetCurrentJob returns the current job.
func (m *Manager) GetCurrentJob() *protocol.Job {
	return m.currentJob.Load()
}

// OnBlockNotification handles a new block notification (from ZMQ).
// CRITICAL FIX: Retry loop to handle node template update latency.
// When a new block is found on the network, the node needs time (~100-1000ms)
// to update its internal state before GetBlockTemplate returns the new template.
// Without retries, we'd broadcast stale jobs causing "prev-blk-not-found" rejections.
func (m *Manager) OnBlockNotification(ctx context.Context) {
	m.OnBlockNotificationWithHash(ctx, "")
}

// OnBlockNotificationWithHash handles a new block notification with the new tip hash.
// FIX D-6: When ZMQ provides the block hash, use AdvanceWithTip for immediate same-height detection.
func (m *Manager) OnBlockNotificationWithHash(ctx context.Context, newTipHash string) {
	// IMMEDIATE HEIGHT EPOCH ADVANCE: Cancel any in-flight block submission contexts.
	m.stateMu.RLock()
	currentHeight := m.lastHeight
	m.stateMu.RUnlock()

	// FIX D-6: If we have the new tip hash from ZMQ, use AdvanceWithTip for
	// immediate same-height reorg detection. Otherwise fall back to legacy Advance.
	if newTipHash != "" {
		// ZMQ provided the hash - use it for same-height reorg detection
		m.heightEpoch.AdvanceWithTip(currentHeight+1, newTipHash)
	} else {
		// Legacy path - optimistically advance height only
		m.heightEpoch.Advance(currentHeight + 1)
	}

	// IMMEDIATE INVALIDATION: Mark all current jobs as stale the moment ZMQ fires.
	// This closes the race window where shares validated during the template refresh
	// pipeline (250ms-1750ms+) would pass validation against still-Active jobs, get
	// detected as blocks, and then be rejected by the daemon with "prev-blk-not-found"
	// because the prevBlockHash is already outdated.
	// NOTE: We set state to Invalidated but do NOT delete from the map. This way
	// GetJob() still returns the job, and the validator sees JobStateInvalidated
	// and rejects the share as stale rather than accepting it.
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
	// Fast retries initially (50ms) for quick response, slower later (300ms) to reduce
	// daemon load during high-activity periods on fast-block coins like DGB
	for i := 0; i < 20; i++ {
		// Only force cleanJobs on the first attempt. Subsequent retries poll for the
		// updated template without burning intermediate job IDs. The ZMQ handler
		// already invalidated all existing jobs, so force=false retries won't
		// accidentally accept stale shares.
		if err := m.RefreshJob(ctx, i == 0); err != nil {
			m.logger.Errorw("Failed to refresh job", "error", err, "attempt", i+1)
		}

		// Check if template actually updated (new prevBlockHash)
		m.stateMu.RLock()
		newHash := m.lastBlockHash
		m.stateMu.RUnlock()

		if newHash != oldHash {
			m.logger.Infow("Template updated after ZMQ", "attempts", i+1)

			// V48 FIX: Cross-RPC consistency check.
			// If ZMQ gave us the new tip hash, verify it matches what getblocktemplate
			// returned as previousBlockHash. A mismatch indicates RPC proxy split-brain
			// or ZMQ/RPC desync (different daemon backends returning inconsistent state).
			if newTipHash != "" && newHash != newTipHash {
				m.logger.Warnw("⚠️ V48: Cross-RPC inconsistency — ZMQ tip hash differs from template prevBlockHash",
					"zmqTipHash", newTipHash,
					"templatePrevHash", newHash,
					"note", "possible daemon proxy split-brain or ZMQ/RPC desync — verify daemon topology",
				)
			}

			return
		}

		// Exponential backoff: very fast initially, slower when daemon is struggling
		// Attempts 1-10: 25ms (250ms total) - aggressive polling for fast-block coins
		// Attempts 11-20: 150ms (1500ms total) - back off for overloaded daemon
		var delay time.Duration
		if i < 10 {
			delay = 25 * time.Millisecond
		} else {
			delay = 150 * time.Millisecond
		}
		time.Sleep(delay)
	}

	// Template never updated - node is slow or stuck
	m.logger.Warnw("Template unchanged after ZMQ - node slow")

	// V49 FIX: Force a job refresh anyway to prevent miner starvation.
	// BCH and some other daemons can be slow to update templates after ZMQ.
	// A stale job is better than no job - the miner will keep hashing and
	// submit shares once the template eventually updates on the next poll.
	if err := m.RefreshJob(ctx, true); err != nil {
		m.logger.Errorw("Failed to force job refresh after slow template", "error", err)
	} else {
		m.logger.Infow("Forced job refresh despite unchanged template - miner won't starve")
	}
}

// encodeHeight encodes a block height for the coinbase scriptsig (BIP34).
// The encoding must match Bitcoin Core's CScript() << nHeight, which uses:
//   - OP_0 (0x00) for height 0
//   - OP_1..OP_16 (0x51..0x60) for heights 1-16
//   - CScriptNum data push for heights > 16
//
// This is critical because BIP34 validation does a raw byte comparison
// against CScript() << nHeight. Using a data push (e.g., 0x01 0x02) for
// height 2 instead of OP_2 (0x52) causes "bad-cb-height" rejection.
//
// Examples:
//   - Height 0 → [0x00]           (OP_0)
//   - Height 1 → [0x51]           (OP_1)
//   - Height 16 → [0x60]          (OP_16)
//   - Height 17 → [0x01 0x11]     (push 1 byte)
//   - Height 127 → [0x01 0x7f]
//   - Height 128 → [0x02 0x80 0x00]  (0x80 has high bit set, needs padding)
//   - Height 255 → [0x02 0xff 0x00]
//   - Height 256 → [0x02 0x00 0x01]
//   - Height 32768 → [0x03 0x00 0x80 0x00] (0x8000 has high bit set in MSB)
func encodeHeight(height uint64) []byte {
	if height == 0 {
		return []byte{0x00} // OP_0
	}
	if height <= 16 {
		return []byte{byte(0x50 + height)} // OP_1 (0x51) through OP_16 (0x60)
	}

	// For heights > 16, use CScriptNum data push format:
	// [length] [little-endian value bytes]
	var buf []byte
	h := height
	for h > 0 {
		buf = append(buf, byte(h&0xff))
		h >>= 8
	}

	// BIP34 sign extension: if the most significant byte has bit 7 set,
	// we must append 0x00 to prevent it being interpreted as negative
	if buf[len(buf)-1]&0x80 != 0 {
		buf = append(buf, 0x00)
	}

	// Prepend length byte
	return append([]byte{byte(len(buf))}, buf...)
}

// buildOutputScript returns the output script for the coinbase transaction.
// For SOLO mining, this returns the miner's output script (direct coinbase).
// Falls back to the pool's configured address if no SOLO miner is set.
//
// SECURITY: All scripts are validated before being cached to prevent runtime panics.
// This supports all address formats including:
// - Legacy P2PKH (1..., D..., etc.)
// - P2SH (3..., S..., etc.)
// - Native SegWit bech32 (bc1q..., dgb1q..., etc.)
// - Bitcoin Cash CashAddr (bitcoincash:q..., q..., etc.)
func (m *Manager) buildOutputScript() []byte {
	// SOLO mining: prefer miner's address for direct coinbase
	m.soloMinerMu.RLock()
	soloScript := m.soloMinerOutputScript
	m.soloMinerMu.RUnlock()

	if soloScript != nil {
		return soloScript
	}

	// Fallback to pool's configured address
	return m.outputScript
}

// Base58 alphabet used by Bitcoin/DigiByte
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Decode decodes a Base58 encoded string to bytes. Test-only.
func base58Decode(s string) ([]byte, error) {
	// Build a map for quick lookups
	alphabetMap := make(map[rune]int64)
	for i, c := range base58Alphabet {
		alphabetMap[c] = int64(i)
	}

	// Decode to big int
	result := big.NewInt(0)
	for _, c := range s {
		val, ok := alphabetMap[c]
		if !ok {
			return nil, fmt.Errorf("invalid Base58 character: %c", c)
		}
		result.Mul(result, big.NewInt(58))
		result.Add(result, big.NewInt(val))
	}

	// Convert to bytes
	decoded := result.Bytes()

	// Add leading zeros for each '1' in the original string
	leadingZeros := 0
	for _, c := range s {
		if c != '1' {
			break
		}
		leadingZeros++
	}

	// Pad with leading zeros
	if leadingZeros > 0 {
		padding := make([]byte, leadingZeros)
		decoded = append(padding, decoded...)
	}

	return decoded, nil
}

// doubleSHA256 performs SHA256(SHA256(data))
// This is the standard hash function for DigiByte SHA256d algorithm (same as Bitcoin's double SHA256)
func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
// Used for logging long hex strings without flooding the logs.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
