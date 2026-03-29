// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package payments handles block maturity tracking and payout processing.
//
// This implements the SOLO payout scheme where 100% of block rewards go
// to the miner who found the block.
//
// CRITICAL FIXES IMPLEMENTED:
// 1. Chain snapshot validation - all RPC calls within a cycle use same tip
// 2. Delayed orphaning - requires N consecutive mismatches before orphaning
// 3. Stability window - requires N stable checks before confirming
// 4. TOCTOU protection - aborts cycle if tip changes mid-processing
package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

const (
	// DefaultBlockMaturityConfirmations is the default number of confirmations
	// required before a block is considered mature. Can be overridden per-coin
	// via the payments.blockMaturity config setting.
	// Standard values: BTC=100, BCH=100, DGB=100
	DefaultBlockMaturityConfirmations = 100

	// DeepReorgCheckInterval determines how often to re-verify confirmed blocks.
	// This catches deep chain reorganizations that orphan previously confirmed blocks.
	// Default: check every 10 processing cycles (typically ~100 minutes)
	DeepReorgCheckInterval = 10

	// DeepReorgMaxAge is the default maximum age (in confirmations) to re-verify.
	// Blocks beyond this depth are not re-verified (diminishing returns vs. performance cost).
	// Deep reorganizations beyond this depth are blockchain-level risks outside software control.
	// See WARNINGS.md for information about chain reorganization risks.
	// V16 FIX: Now configurable via payments.deepReorgMaxAge and auto-scaled to chain speed.
	// Use getDeepReorgMaxAge() method for the effective runtime value.
	// Default: 1000 confirmations (~1 week for 10-min blocks, ~4 hours for 15-sec blocks)
	DeepReorgMaxAge = 1000

	// CRITICAL FIX: Delayed orphaning threshold
	// OrphanMismatchThreshold is the number of consecutive hash mismatches required
	// before marking a block as orphaned. This prevents false orphaning due to
	// temporary node desync or minority fork observation.
	// Industry practice: 3-6 consecutive mismatches
	OrphanMismatchThreshold = 3

	// CRITICAL FIX: Stability window
	// StabilityWindowChecks is the number of consecutive stable checks required
	// before confirming a block. A "stable check" means the block is at/above
	// maturity AND hash matches AND tip hasn't changed since last check.
	// This prevents premature confirmation during chain instability.
	StabilityWindowChecks = 3

	// StatusPending indicates a block is awaiting confirmations.
	StatusPending = "pending"

	// StatusConfirmed indicates a block has reached maturity.
	StatusConfirmed = "confirmed"

	// StatusOrphaned indicates a block was orphaned/reorged.
	StatusOrphaned = "orphaned"

	// StatusPaid indicates the block reward has been paid out.
	StatusPaid = "paid"

	// V15 FIX: ConsecutiveFailureThreshold is the number of consecutive
	// failed processing cycles before logging a CRITICAL warning.
	// This catches silent processor death (e.g., VM suspend, network partition,
	// daemon crash) that would leave blocks stuck in "pending" indefinitely.
	ConsecutiveFailureThreshold = 5

	// V28 FIX: StaleBlockAgeThreshold is the duration after which a pending
	// block is considered "stale" and an operator warning is emitted.
	// A block still pending after 24 hours of expected confirmation time
	// likely indicates a systemic issue requiring operator intervention.
	// Computed per-block based on maturity * expected block time, but we use
	// a fixed 24-hour floor for chains where we don't know block time.
	StaleBlockAgeHours = 24
)

// BlockStore abstracts database operations needed by the payment processor.
type BlockStore interface {
	GetPendingBlocks(ctx context.Context) ([]*database.Block, error)
	GetConfirmedBlocks(ctx context.Context) ([]*database.Block, error)
	GetBlocksByStatus(ctx context.Context, status string) ([]*database.Block, error)
	UpdateBlockStatus(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64) error
	UpdateBlockOrphanCount(ctx context.Context, height uint64, hash string, mismatchCount int) error
	UpdateBlockStabilityCount(ctx context.Context, height uint64, hash string, stabilityCount int, lastTip string) error
	UpdateBlockConfirmationState(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64, orphanMismatchCount int, stabilityCheckCount int, lastVerifiedTip string) error
	GetBlockStats(ctx context.Context) (*database.BlockStats, error)
}

// DaemonRPC abstracts daemon RPC operations needed by the payment processor.
type DaemonRPC interface {
	GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error)
	GetBlockHash(ctx context.Context, height uint64) (string, error)
}

// AdvisoryLocker abstracts PostgreSQL advisory lock operations for payment fencing.
// This provides database-level single-writer guarantee as defense-in-depth
// against split-brain double-payment, even if VIP fencing fails.
type AdvisoryLocker interface {
	TryAdvisoryLock(ctx context.Context, lockID int64) (bool, error)
	ReleaseAdvisoryLock(ctx context.Context, lockID int64) error
}

// ProcessorMetrics abstracts the Prometheus metrics needed by the payment processor.
// This avoids a direct dependency on the metrics package.
type ProcessorMetrics interface {
	SetPaymentProcessorFailedCycles(count int)
	RecordPaidBlockReorg()
	RecordBlockConfirmed()           // AUDIT FIX (ISSUE-5): Track confirmed block count
	RecordBlockOrphaned()            // AUDIT FIX (ISSUE-5): Track orphaned block count
	RecordBlockConfirmedForCoin(coin string)  // AUDIT FIX (ISSUE-4): Per-coin confirmed
	RecordBlockOrphanedForCoin(coin string)   // AUDIT FIX (ISSUE-4): Per-coin orphaned
	SetBlocksPendingMaturityCount(count int)       // Sentinel: pending block count for maturity stall detection
	SetBlocksOldestPendingAgeSec(seconds float64)  // Sentinel: oldest pending block age for maturity stall detection
}

// Processor handles payment processing.
type Processor struct {
	cfg          *config.PaymentsConfig
	poolCfg      *config.PoolConfig
	logger       *zap.SugaredLogger
	db           BlockStore
	daemonClient DaemonRPC

	// State
	running    bool
	mu         sync.Mutex
	stopCh     chan struct{}
	cycleCount int // Tracks processing cycles for periodic deep reorg checks

	// V15 FIX: Track consecutive failed cycles to detect silent processor death.
	// Reset to 0 on any successful cycle. Warns at ConsecutiveFailureThreshold.
	consecutiveFailedCycles int

	// HA: Payment fencing
	// Only the master node should process payments to prevent double-processing
	// in split-brain scenarios. When haEnabled is true and isMaster is false,
	// processCycle is a no-op.
	isMaster  atomic.Bool
	haEnabled atomic.Bool

	// HA: Advisory lock for database-level payment fencing (defense-in-depth).
	// When set, processCycle acquires a PostgreSQL advisory lock before processing.
	// This prevents double-payment even in split-brain scenarios where both nodes
	// believe they are master. The lock is released after each cycle completes.
	advisoryLocker AdvisoryLocker

	// Prometheus metrics for observability (optional — nil-safe)
	metrics ProcessorMetrics
}

// NewProcessor creates a new payment processor.
func NewProcessor(cfg *config.PaymentsConfig, poolCfg *config.PoolConfig, db BlockStore, daemonClient DaemonRPC, logger *zap.Logger) *Processor {
	p := &Processor{
		cfg:          cfg,
		poolCfg:      poolCfg,
		logger:       logger.Sugar(),
		db:           db,
		daemonClient: daemonClient,
		stopCh:       make(chan struct{}),
	}
	// Default: payments are enabled (non-HA mode or master node)
	p.isMaster.Store(true)
	return p
}

// SetHAEnabled enables HA payment fencing.
// When enabled, the processor will only run payment cycles if this node is master.
func (p *Processor) SetHAEnabled(enabled bool) {
	p.haEnabled.Store(enabled)
}

// SetMasterRole sets whether this node should process payments.
// Called by the VIP role change handler when HA role changes.
func (p *Processor) SetMasterRole(isMaster bool) {
	p.isMaster.Store(isMaster)
	if isMaster {
		p.logger.Info("Payment processor: ENABLED (this node is master)")
	} else {
		p.logger.Info("Payment processor: PAUSED (this node is backup)")
	}
}

// SetAdvisoryLocker sets the database advisory locker for payment fencing.
// When set, processCycle will acquire a PostgreSQL advisory lock (defense-in-depth)
// to prevent double-payment processing in split-brain scenarios.
// Pass the *database.PostgresDB or *database.DatabaseManager.GetActiveDB() here.
func (p *Processor) SetAdvisoryLocker(locker AdvisoryLocker) {
	p.advisoryLocker = locker
}

// SetMetrics sets the Prometheus metrics for observability.
// When set, the processor exports consecutive failure count and paid block reorg count.
func (p *Processor) SetMetrics(m ProcessorMetrics) {
	p.metrics = m
}

// Start begins the payment processing loop.
func (p *Processor) Start(ctx context.Context) error {
	if !p.cfg.Enabled {
		p.logger.Info("Payment processing disabled (SOLO mode - rewards go directly to your wallet)")
		return nil
	}

	// V39 FIX: Warn if blockMaturity is dangerously low.
	// An operator setting blockMaturity=1 would cause payments after 1 confirmation,
	// making a simple 2-block reorg cause fund loss with no recourse.
	// Log a prominent warning but allow it — regtest configs need low maturity for fast testing,
	// and blocking startup entirely prevents integration tests from validating the pipeline.
	maturity := p.getBlockMaturity()
	const MinSafeBlockMaturity = 10
	if maturity < MinSafeBlockMaturity {
		p.logger.Warnw("⚠️  blockMaturity is below recommended minimum — risk of paying for orphaned blocks in production",
			"blockMaturity", maturity,
			"recommended", MinSafeBlockMaturity,
			"default", DefaultBlockMaturityConfirmations,
		)
	}

	p.mu.Lock()
	p.running = true
	p.mu.Unlock()

	go p.processLoop(ctx)

	p.logger.Infow("Payment processor started",
		"interval", p.getEffectiveInterval(),
		"scheme", p.cfg.Scheme,
		"minimumPayment", p.cfg.MinimumPayment,
		"orphanThreshold", OrphanMismatchThreshold,
		"stabilityWindow", StabilityWindowChecks,
		"blockTime", p.cfg.BlockTime,
		"deepReorgMaxAge", p.getDeepReorgMaxAge(),
	)

	p.logger.Info("SOLO mode: block rewards go directly to your configured coinbase address")

	return nil
}

// getBlockMaturity returns the configured block maturity or the default.
func (p *Processor) getBlockMaturity() int {
	if p.cfg.BlockMaturity > 0 {
		return p.cfg.BlockMaturity
	}
	return DefaultBlockMaturityConfirmations
}

// Stop stops the payment processor.
func (p *Processor) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		p.running = false
		close(p.stopCh)
	}

	p.logger.Info("Payment processor stopped")
	return nil
}

// getEffectiveInterval returns the payment processing interval, auto-scaling
// to the chain's block time if no explicit interval is configured.
// V14 FIX: On fast chains like DGB (15s blocks), the default 600s interval means
// a block reaches 100 confirmations in ~25 min but the processor only checks every
// 10 min. Auto-scaling to blockTime*10 (min 60s) ensures timely confirmation.
func (p *Processor) getEffectiveInterval() time.Duration {
	if p.cfg.Interval > 0 {
		return p.cfg.Interval
	}
	if p.cfg.BlockTime > 0 {
		interval := time.Duration(p.cfg.BlockTime*10) * time.Second
		if interval < 60*time.Second {
			interval = 60 * time.Second
		}
		// AUDIT FIX: Cap at 10 minutes to prevent excessively long intervals.
		// For BTC (600s blocks), blockTime*10 = 6000s = 100 minutes, which means
		// a miner waits 100 minutes between maturity checks. Cap at 10 minutes
		// so maturity tracking remains responsive on slow chains.
		if interval > 10*time.Minute {
			interval = 10 * time.Minute
		}
		p.logger.Infow("V14 FIX: Auto-scaled payment processing interval from chain block time",
			"blockTime", p.cfg.BlockTime,
			"interval", interval,
		)
		return interval
	}
	// Fallback: 10 minutes (safe default for BTC-like chains)
	return 10 * time.Minute
}

// getDeepReorgMaxAge returns the configured deep reorg max age, auto-scaling
// to the chain's block time if not explicitly configured.
// V16 FIX: DeepReorgMaxAge=1000 is only ~4 hours on DGB (15s blocks) but ~7 days
// on BTC (600s blocks). Auto-scaling to max(1000, 86400/blockTime) ensures at
// least 24 hours of verification depth on all chains.
func (p *Processor) getDeepReorgMaxAge() uint64 {
	if p.cfg.DeepReorgMaxAge > 0 {
		return uint64(p.cfg.DeepReorgMaxAge)
	}
	if p.cfg.BlockTime > 0 {
		// Ensure at least 24 hours of verification depth
		minAge := 86400 / p.cfg.BlockTime // 24h worth of blocks
		if minAge > DeepReorgMaxAge {
			p.logger.Debugw("V16 FIX: Auto-scaled deep reorg max age for fast chain",
				"blockTime", p.cfg.BlockTime,
				"deepReorgMaxAge", minAge,
			)
			return uint64(minAge)
		}
	}
	return DeepReorgMaxAge
}

// processLoop runs the payment processing cycle.
func (p *Processor) processLoop(ctx context.Context) {
	ticker := time.NewTicker(p.getEffectiveInterval())
	defer ticker.Stop()

	// Run immediately on start
	p.processCycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.processCycle(ctx)
		}
	}
}

// processCycle runs a single payment processing cycle.
func (p *Processor) processCycle(ctx context.Context) {
	// HA payment fencing: skip processing on backup nodes
	if p.haEnabled.Load() && !p.isMaster.Load() {
		p.logger.Debug("Payment cycle skipped: this node is not master")
		return
	}

	// HA defense-in-depth: acquire PostgreSQL advisory lock before processing.
	// This prevents double-payment even in split-brain scenarios where both nodes
	// believe they are master (VIP fencing failure). The advisory lock is a
	// database-level single-writer guarantee that only one process can hold.
	if p.advisoryLocker != nil {
		lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
		// AUDIT FIX (PF-2): Use per-pool lock ID so multi-coin processors
		// don't serialize. Each coin gets its own advisory lock.
		lockID := database.PaymentAdvisoryLockIDForPool(p.poolCfg.ID)
		acquired, err := p.advisoryLocker.TryAdvisoryLock(lockCtx, lockID)
		lockCancel()
		if err != nil {
			p.logger.Warnw("Failed to acquire payment advisory lock (continuing with VIP-only fencing)",
				"error", err,
			)
			// Don't skip the cycle — VIP fencing is the primary guard.
			// Advisory lock is defense-in-depth; if the DB is down, VIP fencing
			// is still enforced by the isMaster check above.
		} else if !acquired {
			p.logger.Warnw("Payment advisory lock held by another process — skipping cycle to prevent double-payment",
				"lockID", lockID,
			)
			return
		} else {
			// Lock acquired — re-check master status to close TOCTOU window.
			// A VIP failover could have occurred during the lock acquisition timeout,
			// making this node a backup while it still holds the advisory lock.
			if p.haEnabled.Load() && !p.isMaster.Load() {
				p.logger.Warnw("Lost master role during advisory lock acquisition — releasing lock and aborting cycle",
					"lockID", lockID,
				)
				releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = p.advisoryLocker.ReleaseAdvisoryLock(releaseCtx, lockID)
				releaseCancel()
				return
			}

			// Lock acquired and still master — ensure we release it when the cycle completes
			defer func() {
				releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
				if err := p.advisoryLocker.ReleaseAdvisoryLock(releaseCtx, lockID); err != nil {
					p.logger.Warnw("Failed to release payment advisory lock", "error", err)
				}
				releaseCancel()
			}()
		}
	}

	p.logger.Debug("Starting payment processing cycle")
	p.mu.Lock()
	p.cycleCount++
	p.mu.Unlock()

	// V15 FIX: Track whether this cycle's critical operations succeed.
	// If confirmations update fails, blocks stay in "pending" indefinitely.
	cycleFailed := false

	// 1. Update block confirmations (checks pending blocks)
	if err := p.updateBlockConfirmations(ctx); err != nil {
		p.logger.Errorw("Failed to update block confirmations", "error", err)
		cycleFailed = true
	}

	// 2. Deep reorg detection - periodically re-verify confirmed blocks
	// This catches rare but catastrophic deep chain reorganizations
	if p.cycleCount%DeepReorgCheckInterval == 0 {
		if err := p.verifyConfirmedBlocks(ctx); err != nil {
			p.logger.Errorw("Failed to verify confirmed blocks", "error", err)
			cycleFailed = true // AUDIT FIX (SF-7): Track all step failures
		}
	}

	// 3. Process mature blocks
	if err := p.processMatureBlocks(ctx); err != nil {
		p.logger.Errorw("Failed to process mature blocks", "error", err)
		cycleFailed = true // AUDIT FIX (SF-7): Track all step failures
	}

	// 4. Execute pending payments (SOLO scheme pays immediately)
	if err := p.executePendingPayments(ctx); err != nil {
		p.logger.Errorw("Failed to execute payments", "error", err)
		cycleFailed = true // AUDIT FIX (SF-7): Track all step failures
	}

	// V15 FIX: Track consecutive failures and warn operator.
	// A processor that silently fails cycle after cycle leaves blocks
	// stuck in "pending" forever — the miner is never paid.
	// Lock protects consecutiveFailedCycles (read by health-check goroutine).
	p.mu.Lock()
	if cycleFailed {
		p.consecutiveFailedCycles++
		if p.consecutiveFailedCycles >= ConsecutiveFailureThreshold {
			p.logger.Errorw("🚨 CRITICAL: Payment processor has failed multiple consecutive cycles!",
				"consecutiveFailures", p.consecutiveFailedCycles,
				"threshold", ConsecutiveFailureThreshold,
				"impact", "Blocks may be stuck in 'pending' — check daemon connectivity and database health",
			)
		}
	} else {
		if p.consecutiveFailedCycles > 0 {
			p.logger.Infow("Payment processor recovered after consecutive failures",
				"previousFailures", p.consecutiveFailedCycles,
			)
		}
		p.consecutiveFailedCycles = 0
	}
	failedCycles := p.consecutiveFailedCycles
	p.mu.Unlock()
	// Export consecutive failure count to Prometheus for alerting
	if p.metrics != nil {
		p.metrics.SetPaymentProcessorFailedCycles(failedCycles)
	}

	// V28 FIX: Check for stale pending blocks and warn operator.
	// A block stuck in "pending" beyond StaleBlockAgeHours likely indicates
	// a systemic issue (daemon down, DB unreachable, or operator inattention).
	p.checkStalePendingBlocks(ctx)

	// Record pending maturity metrics for Sentinel block maturity stall detection
	p.recordPendingMaturityMetrics(ctx)

	p.logger.Debug("Payment processing cycle complete")
}

// ConsecutiveFailedCycles returns the number of consecutive failed processing cycles.
// Used by health checks to detect a silently-dying payment processor.
func (p *Processor) ConsecutiveFailedCycles() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.consecutiveFailedCycles
}

// checkStalePendingBlocks warns when pending blocks have been waiting beyond
// the expected confirmation time. This is a V28 safety net that catches operator
// inattention — if a block has been pending for 24+ hours, something is wrong.
// This runs every cycle but only logs when stale blocks are actually found.
func (p *Processor) checkStalePendingBlocks(ctx context.Context) {
	blocks, err := p.db.GetPendingBlocks(ctx)
	if err != nil {
		// Don't log error here — updateBlockConfirmations already handles this
		return
	}

	if len(blocks) == 0 {
		return
	}

	// AUDIT FIX: Scale stale threshold to chain speed instead of hardcoded 24h.
	// For fast chains like DGB (15s blocks), 100 confirmations = 25 min, so waiting
	// 24h before alerting is far too late. Use maturity * blockTime * 3 as the
	// expected-plus-margin threshold, with a 24h floor for chains without blockTime config.
	staleThreshold := time.Duration(StaleBlockAgeHours) * time.Hour
	if p.cfg.BlockTime > 0 {
		expectedConfirmTime := time.Duration(p.cfg.BlockTime*int(p.getBlockMaturity())) * time.Second
		scaledThreshold := expectedConfirmTime * 3 // 3x expected time as safety margin
		if scaledThreshold < staleThreshold {
			staleThreshold = scaledThreshold
		}
	}
	now := time.Now()
	staleCount := 0
	var oldestAge time.Duration

	for _, block := range blocks {
		age := now.Sub(block.Created)
		if age > oldestAge {
			oldestAge = age
		}
		if age > staleThreshold {
			staleCount++
			// Log each stale block (but only once per threshold crossing
			// since we log at Warn level, operators will see it in normal logging)
			if staleCount <= 3 { // Limit individual block logs to avoid spam
				p.logger.Warnw("⚠️ V28: Pending block exceeds expected confirmation time",
					"height", block.Height,
					"hash", block.Hash,
					"miner", block.Miner,
					"age", age.Round(time.Minute).String(),
					"staleThreshold", staleThreshold.String(),
				)
			}
		}
	}

	if staleCount > 0 {
		p.logger.Errorw("🚨 STALE BLOCKS: Pending blocks exceeded expected confirmation time!",
			"staleCount", staleCount,
			"totalPending", len(blocks),
			"oldestAge", oldestAge.Round(time.Minute).String(),
			"staleThreshold", staleThreshold.String(),
			"action", "Check daemon sync status, network connectivity, and block explorer for these heights",
		)
	}
}

// recordPendingMaturityMetrics exports pending block count and oldest pending age
// to Prometheus for Sentinel block maturity stall detection.
func (p *Processor) recordPendingMaturityMetrics(ctx context.Context) {
	if p.metrics == nil {
		return
	}

	blocks, err := p.db.GetPendingBlocks(ctx)
	if err != nil {
		return // Don't spam logs — updateBlockConfirmations handles errors
	}

	p.metrics.SetBlocksPendingMaturityCount(len(blocks))

	if len(blocks) == 0 {
		p.metrics.SetBlocksOldestPendingAgeSec(0)
		return
	}

	now := time.Now()
	var oldestAge time.Duration
	for _, block := range blocks {
		age := now.Sub(block.Created)
		if age > oldestAge {
			oldestAge = age
		}
	}
	p.metrics.SetBlocksOldestPendingAgeSec(oldestAge.Seconds())
}

// updateBlockConfirmations checks pending blocks and updates their confirmation status.
//
// CRITICAL FIXES IMPLEMENTED:
// 1. Chain snapshot - captures tip at start, validates it hasn't changed
// 2. Delayed orphaning - requires OrphanMismatchThreshold consecutive mismatches
// 3. Stability window - requires StabilityWindowChecks stable observations before confirming
// 4. TOCTOU protection - aborts if tip changes mid-cycle
func (p *Processor) updateBlockConfirmations(ctx context.Context) error {
	blocks, err := p.db.GetPendingBlocks(ctx)
	if err != nil {
		return fmt.Errorf("failed to get pending blocks: %w", err)
	}

	if len(blocks) == 0 {
		return nil
	}

	// CRITICAL FIX #1: Capture chain snapshot at START of cycle
	// All decisions within this cycle must be based on this snapshot
	bcInfo, err := p.daemonClient.GetBlockchainInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blockchain info: %w", err)
	}

	// V19 FIX: Skip cycle if daemon is in Initial Block Download (IBD).
	// During IBD, block heights and hashes are unreliable — processing blocks
	// against a partially-synced chain would cause false orphaning.
	if bcInfo.InitialBlockDownload {
		p.logger.Warnw("Daemon in Initial Block Download - skipping confirmation cycle",
			"blocks", bcInfo.Blocks,
			"headers", bcInfo.Headers,
			"progress", bcInfo.VerificationProgress,
		)
		return nil
	}

	snapshotTip := bcInfo.BestBlockHash
	snapshotHeight := bcInfo.Blocks

	p.logger.Debugw("Orphan detection cycle starting",
		"snapshotTip", safeHashPrefix(snapshotTip),
		"snapshotHeight", snapshotHeight,
		"pendingBlocks", len(blocks),
	)

	for _, block := range blocks {
		// CRITICAL FIX #4: TOCTOU Protection
		// Before each block check, verify tip hasn't changed
		currentInfo, err := p.daemonClient.GetBlockchainInfo(ctx)
		if err != nil {
			p.logger.Warnw("Failed to verify chain tip - aborting cycle",
				"error", err,
			)
			return nil // Abort cycle, retry next time
		}
		if currentInfo.BestBlockHash != snapshotTip {
			// Only abort on reorgs (height decrease or same-height with different hash).
			// Normal chain advancement (new blocks mined) is safe — the pending blocks
			// being verified are deeper in the chain and unaffected by new tips.
			// Without this distinction, fast-block coins (DGB 15s, regtest <5s) can
			// never complete a confirmation cycle because the tip changes too quickly.
			if currentInfo.Blocks <= snapshotHeight {
				p.logger.Warnw("Chain reorg during orphan check - aborting cycle to prevent TOCTOU",
					"snapshotTip", safeHashPrefix(snapshotTip),
					"currentTip", safeHashPrefix(currentInfo.BestBlockHash),
					"snapshotHeight", snapshotHeight,
					"currentHeight", currentInfo.Blocks,
				)
				return nil // Abort and retry next cycle with fresh snapshot
			}
			// Chain advanced normally — update snapshot and continue
			p.logger.Debugw("Chain advanced during confirmation cycle — updating snapshot",
				"oldHeight", snapshotHeight,
				"newHeight", currentInfo.Blocks,
			)
			snapshotTip = currentInfo.BestBlockHash
			snapshotHeight = currentInfo.Blocks
		}

		// Check for uint64 underflow before subtraction
		if block.Height > snapshotHeight {
			p.logger.Warnw("Block height exceeds current chain height - possible reorg",
				"blockHeight", block.Height,
				"chainHeight", snapshotHeight,
				"hash", block.Hash,
			)
			// Increment mismatch counter instead of immediate orphaning
			block.OrphanMismatchCount++
			if block.OrphanMismatchCount >= OrphanMismatchThreshold {
				p.logger.Warnw("Block ahead of chain for too long - marking orphaned",
					"height", block.Height,
					"mismatchCount", block.OrphanMismatchCount,
					"threshold", OrphanMismatchThreshold,
				)
				if err := p.db.UpdateBlockStatus(ctx, block.Height, block.Hash, StatusOrphaned, 0); err != nil {
					p.logger.Errorw("Failed to mark orphaned block", "error", err)
				}
				// AUDIT FIX (ISSUE-5): Increment BlocksOrphaned metric
				if p.metrics != nil {
					p.metrics.RecordBlockOrphanedForCoin(p.poolCfg.Coin)
				}
			} else {
				p.logger.Debugw("Block ahead of chain - incrementing mismatch counter",
					"height", block.Height,
					"mismatchCount", block.OrphanMismatchCount,
					"threshold", OrphanMismatchThreshold,
				)
				// Update mismatch count in DB
				if err := p.db.UpdateBlockOrphanCount(ctx, block.Height, block.Hash, block.OrphanMismatchCount); err != nil {
					p.logger.Errorw("Failed to update orphan mismatch count", "error", err)
				}
			}
			continue
		}

		confirmations := snapshotHeight - block.Height

		// Verify block hash is still in main chain
		currentHash, err := p.daemonClient.GetBlockHash(ctx, block.Height)
		if err != nil {
			p.logger.Warnw("Failed to get block hash for verification",
				"height", block.Height,
				"error", err,
			)
			continue // Retry next cycle
		}

		if currentHash != block.Hash {
			// CRITICAL FIX #2: Delayed orphaning
			// Don't immediately mark as orphaned - could be temporary node desync
			block.OrphanMismatchCount++
			p.logger.Warnw("Block hash mismatch detected",
				"height", block.Height,
				"ourHash", safeHashPrefix(block.Hash),
				"chainHash", safeHashPrefix(currentHash),
				"mismatchCount", block.OrphanMismatchCount,
				"threshold", OrphanMismatchThreshold,
			)

			if block.OrphanMismatchCount >= OrphanMismatchThreshold {
				// Confirmed orphan - N consecutive mismatches
				p.logger.Warnw("Block ORPHANED - consecutive mismatch threshold reached",
					"height", block.Height,
					"ourHash", block.Hash,
					"chainHash", currentHash,
					"miner", block.Miner,
					"mismatchCount", block.OrphanMismatchCount,
				)
				if err := p.db.UpdateBlockStatus(ctx, block.Height, block.Hash, StatusOrphaned, 0); err != nil {
					p.logger.Errorw("Failed to mark block as orphaned",
						"height", block.Height,
						"error", err,
					)
				}
				// AUDIT FIX (ISSUE-5): Increment BlocksOrphaned metric
				if p.metrics != nil {
					p.metrics.RecordBlockOrphanedForCoin(p.poolCfg.Coin)
				}
			} else {
				// Not yet orphaned - update mismatch count and wait
				if err := p.db.UpdateBlockOrphanCount(ctx, block.Height, block.Hash, block.OrphanMismatchCount); err != nil {
					p.logger.Errorw("Failed to update orphan mismatch count", "error", err)
				}
			}
			// Reset stability counter - hash mismatch means unstable
			block.StabilityCheckCount = 0
			continue
		}

		// Hash matches - reset orphan mismatch counter
		if block.OrphanMismatchCount > 0 {
			p.logger.Infow("Block hash now matches - resetting mismatch counter",
				"height", block.Height,
				"previousMismatchCount", block.OrphanMismatchCount,
			)
			block.OrphanMismatchCount = 0
			if err := p.db.UpdateBlockOrphanCount(ctx, block.Height, block.Hash, 0); err != nil {
				p.logger.Errorw("Failed to reset orphan mismatch count", "error", err)
			}
		}

		// Block is in main chain - update confirmation progress
		maturity := p.getBlockMaturity()
		progress := float64(confirmations) / float64(maturity)
		if progress > 1.0 {
			progress = 1.0
		}

		// CRITICAL FIX #3: Stability window for confirmation
		status := StatusPending
		if confirmations >= uint64(maturity) {
			// Block has enough confirmations, but is it stable?
			// The hash comparison at line 681 already verified the block is in the main chain.
			// We just need N consecutive successful verifications to confirm stability.
			// NOTE: We removed the "tip changed = reset" logic because on fast-block chains
			// (DGB 15s, regtest <5s), the tip changes every few seconds, which would reset
			// stability forever. The hash check is sufficient — if the block's hash at its
			// height still matches, the block is stable regardless of new blocks on top.

			// Increment stability counter
			block.StabilityCheckCount++
			block.LastVerifiedTip = snapshotTip

			if block.StabilityCheckCount >= StabilityWindowChecks {
				// Block is confirmed - stable for N consecutive checks
				status = StatusConfirmed
				p.logger.Infow("Block confirmed - stability window passed",
					"height", block.Height,
					"hash", block.Hash,
					"confirmations", confirmations,
					"miner", block.Miner,
					"stabilityChecks", block.StabilityCheckCount,
				)
				// AUDIT FIX (ISSUE-5): Increment BlocksConfirmed metric
				if p.metrics != nil {
					p.metrics.RecordBlockConfirmedForCoin(p.poolCfg.Coin)
				}
			} else {
				p.logger.Debugw("Block at maturity - awaiting stability",
					"height", block.Height,
					"confirmations", confirmations,
					"stabilityChecks", block.StabilityCheckCount,
					"required", StabilityWindowChecks,
				)
			}

			// Stability count updated in-memory; written atomically below.
		}

		// ATOMIC FIX: Update status, progress, stability count, orphan count, and tip
		// in a single transaction. Previously these were separate SQL calls — a crash
		// between UpdateBlockStabilityCount and UpdateBlockStatus could leave a block
		// with stability=3 but status still "pending". Now all fields commit together.
		if err := p.db.UpdateBlockConfirmationState(ctx, block.Height, block.Hash, status, progress, block.OrphanMismatchCount, block.StabilityCheckCount, block.LastVerifiedTip); err != nil {
			if errors.Is(err, database.ErrStatusGuardBlocked) {
				// Expected in HA mode: status guard prevented transition (stale process or block already advanced)
				p.logger.Warnw("Block status update blocked by guard (expected in HA mode)",
					"height", block.Height,
					"hash", block.Hash,
					"attemptedStatus", status,
				)
			} else {
				p.logger.Errorw("Failed to update block confirmation state",
					"height", block.Height,
					"error", err,
				)
			}
			continue
		}
	}

	return nil
}

// verifyConfirmedBlocks re-verifies previously confirmed blocks to detect deep reorgs.
// This is a safety measure against rare but catastrophic chain reorganizations that
// could orphan blocks after they've already been marked as confirmed.
//
// DEEP REORG DETECTION (Audit Recommendation #2):
// While pending blocks are verified every cycle, confirmed blocks were previously
// not re-verified. This function adds periodic re-verification to catch deep
// reorganizations that could orphan confirmed blocks.
func (p *Processor) verifyConfirmedBlocks(ctx context.Context) error {
	blocks, err := p.db.GetConfirmedBlocks(ctx)
	if err != nil {
		return fmt.Errorf("failed to get confirmed blocks: %w", err)
	}

	// Capture chain snapshot (needed by both confirmed and paid block checks)
	bcInfo, err := p.daemonClient.GetBlockchainInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blockchain info: %w", err)
	}
	snapshotHeight := bcInfo.Blocks
	snapshotTipHash := bcInfo.BestBlockHash

	reorgedCount := 0
	verifiedCount := 0

	for _, block := range blocks {
		// Skip blocks beyond deep reorg max age - re-verification cost exceeds benefit
		if block.Height+p.getDeepReorgMaxAge() < bcInfo.Blocks {
			continue
		}

		// TOCTOU check — abort on actual reorg, not new blocks extending the chain.
		// AUDIT FIX (PF-3): Don't abort on chain extension (height increase with new
		// blocks). Fast chains like DGB (15s blocks) would never complete the scan.
		// But DO abort if: (1) height decreased, or (2) same height but different tip
		// hash — the latter indicates a same-height reorg (block replaced at tip).
		currentInfo, err := p.daemonClient.GetBlockchainInfo(ctx)
		if err != nil {
			p.logger.Warnw("Failed to get blockchain info during deep reorg check - aborting", "error", err)
			return nil
		}
		if currentInfo.Blocks < snapshotHeight {
			// Height decreased — genuine reorg in progress, unsafe to continue
			p.logger.Warnw("Chain height decreased during deep reorg check - aborting (possible reorg)",
				"snapshotHeight", snapshotHeight,
				"currentHeight", currentInfo.Blocks,
			)
			return nil
		}
		if currentInfo.Blocks == snapshotHeight && currentInfo.BestBlockHash != snapshotTipHash {
			// Same height but different tip hash — reorg at current height, abort
			p.logger.Warnw("Chain tip changed at same height during deep reorg check - aborting (possible reorg)",
				"snapshotHeight", snapshotHeight,
				"snapshotTip", snapshotTipHash,
				"currentTip", currentInfo.BestBlockHash,
			)
			return nil
		}
		// Chain extended normally — update snapshot and continue
		snapshotHeight = currentInfo.Blocks
		snapshotTipHash = currentInfo.BestBlockHash

		// Verify block is still in main chain
		currentHash, err := p.daemonClient.GetBlockHash(ctx, block.Height)
		if err != nil {
			p.logger.Warnw("Failed to verify confirmed block hash",
				"height", block.Height,
				"error", err,
			)
			continue
		}

		if currentHash != block.Hash {
			// CRITICAL: Previously confirmed block was orphaned by deep reorg!
			p.logger.Errorw("🚨 DEEP REORG DETECTED - Confirmed block ORPHANED!",
				"height", block.Height,
				"ourHash", block.Hash,
				"chainHash", currentHash,
				"miner", block.Miner,
				"confirmationsWhenConfirmed", p.getBlockMaturity(),
			)

			if err := p.db.UpdateBlockStatus(ctx, block.Height, block.Hash, StatusOrphaned, 0); err != nil {
				p.logger.Errorw("Failed to mark deep-reorged block as orphaned",
					"height", block.Height,
					"error", err,
				)
			}
			// AUDIT FIX (ISSUE-5): Increment BlocksOrphaned metric on deep reorg
			if p.metrics != nil {
				p.metrics.RecordBlockOrphanedForCoin(p.poolCfg.Coin)
			}
			reorgedCount++
		} else {
			verifiedCount++
		}
	}

	if reorgedCount > 0 {
		p.logger.Warnw("Deep reorg verification complete - ORPHANS DETECTED",
			"verified", verifiedCount,
			"orphaned", reorgedCount,
		)
	} else if verifiedCount > 0 {
		p.logger.Debugw("Deep reorg verification complete - all blocks valid",
			"verified", verifiedCount,
		)
	}

	// PAID BLOCK DEEP REORG DETECTION (alert-only)
	// Status guard blocks paid→orphaned transitions, so we cannot reverse payments.
	// But we MUST detect and alert when a paid block is orphaned by a deep reorg,
	// so the operator knows about the financial loss and can take manual action.
	paidBlocks, paidErr := p.db.GetBlocksByStatus(ctx, "paid")
	if paidErr != nil {
		p.logger.Warnw("Failed to get paid blocks for deep reorg check", "error", paidErr)
	} else if len(paidBlocks) > 0 {
		paidReorged := 0
		for _, block := range paidBlocks {
			// Skip blocks beyond deep reorg max age
			if block.Height+p.getDeepReorgMaxAge() < bcInfo.Blocks {
				continue
			}

			paidHash, paidHashErr := p.daemonClient.GetBlockHash(ctx, block.Height)
			if paidHashErr != nil {
				continue // Can't verify, skip
			}

			if paidHash != block.Hash {
				p.logger.Errorw("🚨🚨 CRITICAL: PAID block orphaned by deep reorg — FINANCIAL LOSS DETECTED",
					"height", block.Height,
					"paidHash", block.Hash,
					"chainHash", paidHash,
					"miner", block.Miner,
					"reward", block.Reward,
					"status", "paid",
					"action", "MANUAL INTERVENTION REQUIRED — payment was already sent for orphaned block",
				)
				paidReorged++
				if p.metrics != nil {
					p.metrics.RecordPaidBlockReorg()
				}
			}
		}
		if paidReorged > 0 {
			p.logger.Errorw("🚨 PAID BLOCK REORG ALERT — operator intervention required",
				"paidBlocksOrphaned", paidReorged,
				"totalPaidChecked", len(paidBlocks),
			)
		}
	}

	return nil
}

// processMatureBlocks processes blocks that have reached maturity.
// In SOLO mode, the coinbase transaction IS the payment — no further action needed.
func (p *Processor) processMatureBlocks(ctx context.Context) error {
	return nil
}

// executePendingPayments sends pending payments to miners.
// In SOLO mode, the coinbase transaction IS the payment — no further action needed.
func (p *Processor) executePendingPayments(ctx context.Context) error {
	return nil
}

// Stats returns payment processor statistics.
type Stats struct {
	PendingBlocks   int       `json:"pendingBlocks"`
	ConfirmedBlocks int       `json:"confirmedBlocks"`
	OrphanedBlocks  int       `json:"orphanedBlocks"`
	PaidBlocks      int       `json:"paidBlocks"`
	BlockMaturity   int       `json:"blockMaturity"`
	TotalPaid       float64   `json:"totalPaid"`
	LastPaymentTime time.Time `json:"lastPaymentTime,omitempty"`
}

func (p *Processor) Stats(ctx context.Context) (*Stats, error) {
	blockStats, err := p.db.GetBlockStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get block stats: %w", err)
	}

	return &Stats{
		PendingBlocks:   blockStats.Pending,
		ConfirmedBlocks: blockStats.Confirmed,
		OrphanedBlocks:  blockStats.Orphaned,
		PaidBlocks:      blockStats.Paid,
		BlockMaturity:   p.getBlockMaturity(),
		TotalPaid:       0, // FUTURE: Sum from payments table
		LastPaymentTime: time.Time{},
	}, nil
}

// safeHashPrefix returns the first 16 chars of a hash with "..." suffix,
// or the full string if shorter than 16 chars. Prevents panics on empty
// or short hash strings in log statements.
func safeHashPrefix(hash string) string {
	if len(hash) > 16 {
		return hash[:16] + "..."
	}
	return hash
}
