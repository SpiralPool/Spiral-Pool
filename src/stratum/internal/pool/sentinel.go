// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// API Sentinel — internal pool health monitoring goroutine.
//
// APISentinel runs inside the Coordinator, checks all CoinPools every check interval,
// evaluates alert conditions (P0 block money, P1 operational, P2 hygiene), logs alerts,
// updates metrics, and exposes them via API for Spiral Sentinel to consume.
// This is complementary to the external Python Spiral Sentinel which monitors miner
// hardware — this Go sentinel covers internal pool state that is not accessible via
// external APIs. Spiral Sentinel polls /api/sentinel/alerts to bridge these alerts
// to Discord/Telegram notifications.
package pool

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/ha"
	"github.com/spiralpool/stratum/internal/metrics"
	"github.com/spiralpool/stratum/internal/shares"
	"go.uber.org/zap"
)

// alertSeverity represents alert priority levels.
type alertSeverity string

const (
	severityCritical alertSeverity = "critical"
	severityWarning  alertSeverity = "warning"
	severityInfo     alertSeverity = "info"

	// maxRecentAlerts is the ring buffer capacity for recent alerts exposed via API.
	maxRecentAlerts = 100
)

// SentinelAlert represents a single alert fired by the API Sentinel.
// Exposed via /api/sentinel/alerts for the Python Spiral Sentinel to consume.
type SentinelAlert struct {
	AlertType string    `json:"alert_type"`
	Severity  string    `json:"severity"`
	Coin      string    `json:"coin,omitempty"`
	PoolID    string    `json:"pool_id,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// Sentinel monitors internal pool health, logs alerts, updates metrics,
// and exposes them via API for Spiral Sentinel to consume.
type Sentinel struct {
	coordinator *Coordinator
	logger      *zap.SugaredLogger
	metrics     *metrics.Metrics
	cfg         *config.SentinelConfig

	// Recent alerts ring buffer (exposed via /api/sentinel/alerts for Python Sentinel)
	recentAlerts   []SentinelAlert
	recentAlertsMu sync.RWMutex

	// State tracking for rate-of-change alerts
	prevHashrates   map[string]float64   // coin -> last hashrate
	prevConnections map[string]int64     // coin -> last connection count
	lastBlockCount  map[string]float64   // coin -> last blocks_found counter value
	lastBlockTime   map[string]time.Time // coin -> last time a block was observed

	// State tracking for daemon health alerts
	heightTracker   map[string]uint64    // coin -> last known block height
	heightChangedAt map[string]time.Time // coin -> when height last changed

	// State tracking for expanded alerts
	prevDropped        map[string]uint64 // coin -> previous share batch dropped count
	paymentPending     map[string]int    // poolID -> previous pending blocks
	paymentStallCount  map[string]int    // poolID -> consecutive stall checks
	prevOrphaned       map[string]int    // poolID -> previous orphaned blocks count
	prevDBFailovers    uint64            // previous DB failover count
	haRoleHistory      []time.Time       // timestamps of HA role changes (for flap detection)
	prevHARole         string            // last observed HA role
	baselineGoroutines int               // goroutine count at first check (0 = not yet set)

	// State tracking for cross-pool metric-based alerts
	prevFalseRejections float64 // previous BlocksFalseRejection counter value
	prevSubmitted       float64 // previous BlocksSubmitted counter value
	prevRetries         float64 // previous BlockSubmitRetries counter value

	// Alert deduplication: alertKey -> last fire time
	cooldowns  map[string]time.Time
	cooldownMu sync.Mutex // Protects cooldowns map (separate from mu to prevent deadlock)

	mu sync.Mutex // Protects state tracking maps
}

// NewSentinel creates a new Sentinel instance.
func NewSentinel(coord *Coordinator, cfg *config.SentinelConfig, m *metrics.Metrics, logger *zap.Logger) *Sentinel {
	return &Sentinel{
		coordinator:     coord,
		logger:          logger.Sugar(),
		metrics:         m,
		cfg:             cfg,
		prevHashrates:   make(map[string]float64),
		prevConnections: make(map[string]int64),
		lastBlockCount:  make(map[string]float64),
		lastBlockTime:   make(map[string]time.Time),
		heightTracker:   make(map[string]uint64),
		heightChangedAt: make(map[string]time.Time),
		prevDropped:       make(map[string]uint64),
		paymentPending:    make(map[string]int),
		paymentStallCount: make(map[string]int),
		prevOrphaned:      make(map[string]int),
		haRoleHistory:     make([]time.Time, 0, 16),
		cooldowns:       make(map[string]time.Time),
	}
}

// Run starts the sentinel monitoring loop. Blocks until ctx is cancelled.
func (s *Sentinel) Run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.CheckInterval)
	defer ticker.Stop()

	s.logger.Infow("API Sentinel monitoring started",
		"checkInterval", s.cfg.CheckInterval,
		"alertBufferSize", maxRecentAlerts,
		"walStuckThreshold", s.cfg.WALStuckThreshold,
		"alertCooldown", s.cfg.AlertCooldown,
	)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("API Sentinel stopped")
			return
		case <-ticker.C:
			// AUDIT FIX: Panic recovery prevents a single check failure from
			// killing the entire sentinel goroutine. Without this, any panic
			// in a check function (e.g., nil metric, bad type assertion) silently
			// stops ALL health monitoring — the most dangerous failure mode.
			func() {
				defer func() {
					if r := recover(); r != nil {
						s.logger.Errorw("Sentinel check panicked (recovered)",
							"panic", r,
						)
					}
				}()
				s.check(ctx)
			}()
		}
	}
}

// check evaluates all alert conditions across all coin pools.
func (s *Sentinel) check(ctx context.Context) {
	start := time.Now()

	// Track HA role changes BEFORE the gate — must observe ALL transitions
	// (including demotions to BACKUP) to detect flapping. If we only tracked
	// while MASTER, the MASTER→BACKUP→MASTER cycle would be invisible.
	if s.coordinator.vipManager != nil {
		status := s.coordinator.vipManager.GetStatus()
		s.trackHARoleChange(status.LocalRole)

		// HA gate: only fire alerts when MASTER (or no HA configured)
		if status.LocalRole != ha.RoleMaster.String() {
			s.logger.Debugw("Sentinel skipping check (not MASTER)",
				"role", status.LocalRole,
			)
			return
		}
	}

	s.coordinator.poolsMu.RLock()
	pools := make(map[string]*CoinPool, len(s.coordinator.pools))
	for k, v := range s.coordinator.pools {
		pools[k] = v
	}
	s.coordinator.poolsMu.RUnlock()

	for poolID, pool := range pools {
		if !pool.IsRunning() {
			continue
		}

		coin := pool.Symbol()

		// P0: Block money alerts
		s.checkWALStuckEntries(ctx, pool, coin)
		s.checkZeroBlocksDrought(pool, coin)
		s.checkShareDBCritical(pool, coin)
		s.checkShareBatchLoss(pool, coin)
		s.checkCircuitBreaker(pool, coin)

		// P1: Operational alerts
		s.checkAllNodesDown(pool, poolID, coin)
		s.checkChainTipStall(pool, poolID, coin)
		s.checkDaemonPeerCount(pool, poolID, coin)
		s.checkWALRecoveryStuck(pool, coin)
		s.checkMinerDisconnectSpike(pool, coin)
		s.checkHashrateDrop(pool, coin)
		s.checkBackpressure(pool, coin)
		s.checkZMQHealth(pool, coin)
		s.checkNodeHealthScores(pool, coin)

		// P2: Hygiene alerts
		s.checkWALDiskSpace(pool, coin)
		s.checkWALFileCount(pool, coin)
	}

	// Cross-pool checks
	s.checkFalseRejectionRate()
	s.checkRetryStorm()
	s.checkPaymentProcessors(ctx)
	s.checkDBFailover()
	s.checkHAFlapping()
	s.checkOrphanRate(ctx)
	s.checkGoroutineCount()
	s.checkBlockMaturityStall()

	duration := time.Since(start)
	if s.metrics != nil {
		s.metrics.SentinelCheckDuration.Observe(duration.Seconds())
	}

	s.logger.Debugw("Sentinel check complete",
		"duration", duration.Round(time.Millisecond),
		"pools", len(pools),
	)
}

// ═══════════════════════════════════════════════════════════════════════════════
// P0: BLOCK MONEY ALERTS
// ═══════════════════════════════════════════════════════════════════════════════

// checkWALStuckEntries scans for WAL entries stuck in pending/submitting state.
func (s *Sentinel) checkWALStuckEntries(ctx context.Context, pool *CoinPool, coin string) {
	walDir := pool.BlockWALDir()
	if walDir == "" {
		return
	}

	entries, err := ScanPendingEntries(walDir)
	if err != nil {
		s.logger.Warnw("Sentinel: failed to scan WAL pending entries",
			"coin", coin,
			"error", err,
		)
		return
	}

	// Track max pending age for metrics
	var maxAge time.Duration
	for _, entry := range entries {
		// Skip entries with zero/unset timestamp to avoid overflow
		// (time.Since(time.Time{}) ≈ math.MaxInt64 nanoseconds)
		if entry.Timestamp.IsZero() {
			continue
		}
		age := time.Since(entry.Timestamp)
		if age > maxAge {
			maxAge = age
		}

		if age > s.cfg.WALStuckThreshold {
			s.fireAlert(ctx, "wal_stuck_entry", severityCritical, coin, pool.PoolID(),
				fmt.Sprintf("WAL entry stuck for %s (threshold: %s): height=%d hash=%s status=%s",
					age.Round(time.Second), s.cfg.WALStuckThreshold,
					entry.Height, truncateHash(entry.BlockHash), entry.Status),
				map[string]interface{}{
					"height":     entry.Height,
					"block_hash": entry.BlockHash,
					"status":     entry.Status,
					"age_sec":    int(age.Seconds()),
				},
			)
		}
	}

	if s.metrics != nil {
		s.metrics.SentinelWALPendingAge.Set(maxAge.Seconds())
	}
}

// checkZeroBlocksDrought fires when no block has been found by THIS POOL for too long.
// FIX: Previously tracked GetBlockHeight() (network chain tip) which advances every block
// time regardless of whether this pool found any blocks — making the alert dead code.
// Now tracks the BlocksFoundByCoin Prometheus counter which only increments when this pool
// actually finds a block.
func (s *Sentinel) checkZeroBlocksDrought(pool *CoinPool, coin string) {
	if s.cfg.BlockDroughtHours <= 0 {
		return // Disabled
	}

	// Read the pool's blocks-found counter (only increments on OUR blocks, not network blocks)
	var currentCount float64
	if s.metrics != nil && s.metrics.BlocksFoundByCoin != nil {
		currentCount = readMetricValue(s.metrics.BlocksFoundByCoin.WithLabelValues(coin))
	}

	s.mu.Lock()

	// Initialize last-seen time if not tracked yet
	if _, ok := s.lastBlockTime[coin]; !ok {
		s.lastBlockTime[coin] = time.Now() // Start tracking from now
		s.lastBlockCount[coin] = currentCount
		s.mu.Unlock()
		return
	}

	lastCount, ok := s.lastBlockCount[coin]
	if !ok {
		s.lastBlockCount[coin] = currentCount
		s.mu.Unlock()
		return
	}

	if currentCount > lastCount {
		// Pool found a new block — reset drought timer
		s.lastBlockTime[coin] = time.Now()
		s.lastBlockCount[coin] = currentCount
		s.mu.Unlock()
		return
	}

	threshold := time.Duration(s.cfg.BlockDroughtHours) * time.Hour
	elapsed := time.Since(s.lastBlockTime[coin])
	s.mu.Unlock()

	if elapsed > threshold {
		s.fireAlert(context.Background(), "block_drought", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("No new blocks found by pool for %s (threshold: %s)",
				elapsed.Round(time.Minute), threshold),
			map[string]interface{}{
				"hours_elapsed":   elapsed.Hours(),
				"threshold_hours": s.cfg.BlockDroughtHours,
			},
		)
	}
}

// checkShareDBCritical fires when the share pipeline's database connection is critical.
// CRITICAL: When the DB is in critical state (20+ consecutive failures), shares are being lost.
func (s *Sentinel) checkShareDBCritical(pool *CoinPool, coin string) {
	if pool.sharePipeline == nil {
		return
	}

	failures, degraded, critical, dropped, circuitState := pool.sharePipeline.DBHealthStatus()

	if critical {
		s.fireAlert(context.Background(), "share_db_critical", severityCritical, coin, pool.PoolID(),
			fmt.Sprintf("Share pipeline database CRITICAL on %s: %d consecutive failures, circuit=%s, %d batches dropped",
				coin, failures, circuitState, dropped),
			map[string]interface{}{
				"failures":      failures,
				"circuit_state": circuitState,
				"dropped":       dropped,
				"degraded":      degraded,
			},
		)
	} else if degraded {
		s.fireAlert(context.Background(), "share_db_degraded", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("Share pipeline database DEGRADED on %s: %d consecutive failures, circuit=%s",
				coin, failures, circuitState),
			map[string]interface{}{
				"failures":      failures,
				"circuit_state": circuitState,
			},
		)
	}
}

// checkShareBatchLoss fires CRITICAL when share batches have been permanently dropped.
// Dropped shares = miners not credited = revenue loss.
func (s *Sentinel) checkShareBatchLoss(pool *CoinPool, coin string) {
	if pool.sharePipeline == nil {
		return
	}

	stats := pool.sharePipeline.Stats()

	s.mu.Lock()
	prev, ok := s.prevDropped[coin]
	s.prevDropped[coin] = stats.Dropped
	s.mu.Unlock()

	if !ok {
		return // First check
	}

	newDropped := stats.Dropped - prev
	if newDropped > 0 {
		s.fireAlert(context.Background(), "share_batch_dropped", severityCritical, coin, pool.PoolID(),
			fmt.Sprintf("Share batches DROPPED on %s: %d new drops (total: %d) — miner shares permanently lost!",
				coin, newDropped, stats.Dropped),
			map[string]interface{}{
				"new_dropped":   newDropped,
				"total_dropped": stats.Dropped,
				"buffer_usage":  fmt.Sprintf("%d/%d", stats.BufferCurrent, stats.BufferCapacity),
			},
		)
	}
}

// checkCircuitBreaker fires when the database circuit breaker is open or half-open.
// Open circuit = database unreachable, all share persistence queued or dropped.
func (s *Sentinel) checkCircuitBreaker(pool *CoinPool, coin string) {
	if pool.sharePipeline == nil {
		return
	}

	cbStats := pool.sharePipeline.CircuitBreakerStats()

	switch cbStats.State {
	case database.CircuitOpen:
		s.fireAlert(context.Background(), "circuit_breaker_open", severityCritical, coin, pool.PoolID(),
			fmt.Sprintf("Circuit breaker OPEN on %s: database unreachable, %d failures, %d requests blocked",
				coin, cbStats.Failures, cbStats.TotalBlocked),
			map[string]interface{}{
				"state":           cbStats.State.String(),
				"failures":        cbStats.Failures,
				"total_blocked":   cbStats.TotalBlocked,
				"state_changes":   cbStats.StateChanges,
				"current_backoff": cbStats.CurrentBackoff.String(),
			},
		)
	case database.CircuitHalfOpen:
		s.fireAlert(context.Background(), "circuit_breaker_halfopen", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("Circuit breaker HALF-OPEN on %s: probing database recovery, %d total blocked",
				coin, cbStats.TotalBlocked),
			map[string]interface{}{
				"state":         cbStats.State.String(),
				"total_blocked": cbStats.TotalBlocked,
				"state_changes": cbStats.StateChanges,
			},
		)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// P1: OPERATIONAL ALERTS
// ═══════════════════════════════════════════════════════════════════════════════

// checkChainTipStall fires when the daemon block height hasn't advanced for too long.
// A stalled chain tip means miners are working on stale templates — new blocks will be orphaned.
func (s *Sentinel) checkChainTipStall(pool *CoinPool, poolID, coin string) {
	if s.cfg.ChainTipStallMinutes <= 0 {
		return // Disabled
	}

	stats := pool.GetNodeStats()
	height := stats.BlockHeight

	// Record metric regardless of alert state
	if s.metrics != nil {
		s.metrics.DaemonBlockHeight.WithLabelValues(coin).Set(float64(height))
	}

	s.mu.Lock()

	prevHeight, hasPrev := s.heightTracker[coin]
	if !hasPrev {
		// First observation — initialize and return
		s.heightTracker[coin] = height
		s.heightChangedAt[coin] = time.Now()
		s.mu.Unlock()
		if s.metrics != nil {
			s.metrics.DaemonChainTipStaleSec.WithLabelValues(coin).Set(0)
		}
		return
	}

	if height != prevHeight {
		// Height changed — update tracker
		s.heightTracker[coin] = height
		s.heightChangedAt[coin] = time.Now()
		s.mu.Unlock()
		if s.metrics != nil {
			s.metrics.DaemonChainTipStaleSec.WithLabelValues(coin).Set(0)
		}
		return
	}

	// Height unchanged — calculate stale duration
	staleDuration := time.Since(s.heightChangedAt[coin])
	s.mu.Unlock()

	staleSec := staleDuration.Seconds()
	if s.metrics != nil {
		s.metrics.DaemonChainTipStaleSec.WithLabelValues(coin).Set(staleSec)
	}

	threshold := time.Duration(s.cfg.ChainTipStallMinutes) * time.Minute
	if staleDuration > threshold {
		s.fireAlert(context.Background(), "chain_tip_stall", severityCritical, coin, poolID,
			fmt.Sprintf("Daemon chain tip stalled for %s on %s — height %d unchanged (threshold: %s). Miners are working on stale templates!",
				staleDuration.Round(time.Second), coin, height, threshold),
			map[string]interface{}{
				"height":      height,
				"stale_sec":   int(staleSec),
				"threshold_m": s.cfg.ChainTipStallMinutes,
			},
		)
	}
}

// checkDaemonPeerCount fires when the daemon has too few peer connections.
// Zero peers means the daemon is fully isolated from the network.
func (s *Sentinel) checkDaemonPeerCount(pool *CoinPool, poolID, coin string) {
	if s.cfg.MinPeerCount <= 0 {
		return // Disabled
	}

	stats := pool.GetNodeStats()
	peerCount := stats.PeerCount

	// Record metric regardless of alert state
	if s.metrics != nil && peerCount >= 0 {
		s.metrics.DaemonPeerCount.WithLabelValues(coin).Set(float64(peerCount))
	}

	// PeerCount == -1 means unavailable (GetNetworkInfo failed)
	if peerCount < 0 {
		return
	}

	if peerCount == 0 {
		s.fireAlert(context.Background(), "daemon_no_peers", severityCritical, coin, poolID,
			fmt.Sprintf("Daemon has ZERO peers on %s — fully isolated from network! Blocks cannot propagate.",
				coin),
			map[string]interface{}{
				"peer_count":  peerCount,
				"primary_node": stats.PrimaryNodeID,
			},
		)
	} else if peerCount < s.cfg.MinPeerCount {
		s.fireAlert(context.Background(), "daemon_low_peers", severityWarning, coin, poolID,
			fmt.Sprintf("Daemon has only %d peers on %s (minimum: %d) — may be partially isolated",
				peerCount, coin, s.cfg.MinPeerCount),
			map[string]interface{}{
				"peer_count":     peerCount,
				"min_peer_count": s.cfg.MinPeerCount,
				"primary_node":   stats.PrimaryNodeID,
			},
		)
	}
}

// checkAllNodesDown fires CRITICAL when all daemon nodes are unhealthy.
func (s *Sentinel) checkAllNodesDown(pool *CoinPool, poolID, coin string) {
	stats := pool.GetNodeStats()

	allDown := stats.HealthyNodes == 0 && stats.TotalNodes > 0

	if s.metrics != nil {
		val := float64(0)
		if allDown {
			val = 1
		}
		s.metrics.SentinelNodesDown.WithLabelValues(coin).Set(val)
	}

	if allDown {
		s.fireAlert(context.Background(), "all_nodes_down", severityCritical, coin, poolID,
			fmt.Sprintf("All %d daemon nodes are DOWN for %s — block submissions impossible!",
				stats.TotalNodes, coin),
			map[string]interface{}{
				"total_nodes":   stats.TotalNodes,
				"healthy_nodes": stats.HealthyNodes,
				"node_healths":  stats.NodeHealths,
			},
		)
	}
}

// checkWALRecoveryStuck fires when WAL recovery has been running too long.
func (s *Sentinel) checkWALRecoveryStuck(pool *CoinPool, coin string) {
	if !pool.IsWALRecoveryRunning() {
		return
	}

	// WAL recovery shouldn't take more than 5 minutes
	s.fireAlert(context.Background(), "wal_recovery_stuck", severityCritical, coin, pool.PoolID(),
		fmt.Sprintf("WAL recovery has been running for an extended period on %s — may be stuck", coin),
		nil,
	)
}

// checkMinerDisconnectSpike detects sudden drops in connected miners.
func (s *Sentinel) checkMinerDisconnectSpike(pool *CoinPool, coin string) {
	current := pool.GetConnections()

	s.mu.Lock()
	prev, ok := s.prevConnections[coin]
	s.prevConnections[coin] = current
	s.mu.Unlock()

	if !ok || prev == 0 {
		return // First check or no previous data
	}

	dropPercent := float64(prev-current) / float64(prev) * 100

	if s.metrics != nil {
		s.metrics.SentinelConnectionDrop.WithLabelValues(coin).Set(-dropPercent)
	}

	if dropPercent >= float64(s.cfg.DisconnectDropPercent) {
		s.fireAlert(context.Background(), "miner_disconnect_spike", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("Miner connections dropped %.0f%% in one interval (%d -> %d) on %s",
				dropPercent, prev, current, coin),
			map[string]interface{}{
				"previous":     prev,
				"current":      current,
				"drop_percent": dropPercent,
				"threshold":    s.cfg.DisconnectDropPercent,
			},
		)
	}
}

// checkHashrateDrop detects sudden hashrate drops.
func (s *Sentinel) checkHashrateDrop(pool *CoinPool, coin string) {
	current := pool.GetHashrate()

	s.mu.Lock()
	prev, ok := s.prevHashrates[coin]
	s.prevHashrates[coin] = current
	s.mu.Unlock()

	if !ok || prev == 0 {
		return // First check or no previous data
	}

	dropPercent := (prev - current) / prev * 100

	if s.metrics != nil {
		s.metrics.SentinelHashrateChange.WithLabelValues(coin).Set(-dropPercent)
	}

	if dropPercent >= float64(s.cfg.HashrateDropPercent) {
		s.fireAlert(context.Background(), "hashrate_drop", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("Pool hashrate dropped %.0f%% in one interval (%.2f -> %.2f H/s) on %s",
				dropPercent, prev, current, coin),
			map[string]interface{}{
				"previous_hps":  prev,
				"current_hps":   current,
				"drop_percent":  dropPercent,
				"threshold":     s.cfg.HashrateDropPercent,
			},
		)
	}
}

// checkBackpressure fires when the share pipeline buffer is filling up.
// Backpressure indicates the pipeline is falling behind; emergency level means imminent overflow.
func (s *Sentinel) checkBackpressure(pool *CoinPool, coin string) {
	if pool.sharePipeline == nil {
		return
	}

	bpStats := pool.sharePipeline.GetBackpressureStats()

	switch {
	case bpStats.Level >= shares.BackpressureEmergency:
		s.fireAlert(context.Background(), "backpressure_emergency", severityCritical, coin, pool.PoolID(),
			fmt.Sprintf("Share buffer EMERGENCY on %s: %.1f%% full (%d/%d) — overflow imminent!",
				coin, bpStats.FillPercent, bpStats.BufferCurrent, bpStats.BufferCapacity),
			map[string]interface{}{
				"level":        bpStats.Level.String(),
				"fill_percent": bpStats.FillPercent,
				"buffer_used":  bpStats.BufferCurrent,
				"buffer_cap":   bpStats.BufferCapacity,
			},
		)
	case bpStats.Level >= shares.BackpressureCritical:
		s.fireAlert(context.Background(), "backpressure_critical", severityCritical, coin, pool.PoolID(),
			fmt.Sprintf("Share buffer CRITICAL on %s: %.1f%% full (%d/%d)",
				coin, bpStats.FillPercent, bpStats.BufferCurrent, bpStats.BufferCapacity),
			map[string]interface{}{
				"level":        bpStats.Level.String(),
				"fill_percent": bpStats.FillPercent,
				"buffer_used":  bpStats.BufferCurrent,
				"buffer_cap":   bpStats.BufferCapacity,
			},
		)
	case bpStats.Level >= shares.BackpressureWarn:
		s.fireAlert(context.Background(), "backpressure_warn", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("Share buffer filling on %s: %.1f%% full (%d/%d)",
				coin, bpStats.FillPercent, bpStats.BufferCurrent, bpStats.BufferCapacity),
			map[string]interface{}{
				"level":        bpStats.Level.String(),
				"fill_percent": bpStats.FillPercent,
				"buffer_used":  bpStats.BufferCurrent,
				"buffer_cap":   bpStats.BufferCapacity,
			},
		)
	}
}

// checkZMQHealth fires when ZMQ on the primary node is degraded or failed.
// Failed ZMQ = block notifications delayed (falls back to polling), increasing orphan risk.
func (s *Sentinel) checkZMQHealth(pool *CoinPool, coin string) {
	if pool.nodeManager == nil {
		return
	}

	primary := pool.nodeManager.GetPrimary()
	if primary == nil || primary.ZMQ == nil {
		return // No ZMQ configured
	}

	zmqStatus := primary.ZMQ.Status()

	switch zmqStatus {
	case daemon.ZMQStatusFailed:
		zmqStats := primary.ZMQ.Stats()
		s.fireAlert(context.Background(), "zmq_failed", severityCritical, coin, pool.PoolID(),
			fmt.Sprintf("ZMQ FAILED on primary node %s for %s: falling back to polling, failure duration %s",
				primary.ID, coin, zmqStats.FailureDuration.Round(time.Second)),
			map[string]interface{}{
				"node_id":           primary.ID,
				"messages_received": zmqStats.MessagesReceived,
				"errors":            zmqStats.ErrorsCount,
				"failure_duration":  zmqStats.FailureDuration.String(),
				"last_message_age":  zmqStats.LastMessageAge.String(),
			},
		)
	case daemon.ZMQStatusDegraded:
		zmqStats := primary.ZMQ.Stats()
		s.fireAlert(context.Background(), "zmq_degraded", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("ZMQ DEGRADED on primary node %s for %s: last message %s ago, %d errors",
				primary.ID, coin, zmqStats.LastMessageAge.Round(time.Second), zmqStats.ErrorsCount),
			map[string]interface{}{
				"node_id":          primary.ID,
				"last_message_age": zmqStats.LastMessageAge.String(),
				"errors":           zmqStats.ErrorsCount,
			},
		)
	}
}

// checkNodeHealthScores fires when individual daemon nodes have low health scores.
// This catches degrading nodes before they fully die (caught by checkAllNodesDown).
func (s *Sentinel) checkNodeHealthScores(pool *CoinPool, coin string) {
	stats := pool.GetNodeStats()

	threshold := s.cfg.NodeHealthThreshold
	if threshold <= 0 {
		threshold = 0.5
	}

	var degradedNodes []string
	for nodeID, score := range stats.NodeHealths {
		if score < threshold {
			degradedNodes = append(degradedNodes, fmt.Sprintf("%s(%.2f)", nodeID, score))
		}
	}

	if len(degradedNodes) == 0 {
		return
	}

	// If ALL nodes are below threshold, checkAllNodesDown covers it — skip duplicate
	if stats.HealthyNodes == 0 {
		return
	}

	s.fireAlert(context.Background(), "node_health_low", severityWarning, coin, pool.PoolID(),
		fmt.Sprintf("%d/%d nodes below health threshold (%.1f) on %s: %v",
			len(degradedNodes), stats.TotalNodes, threshold, coin, degradedNodes),
		map[string]interface{}{
			"degraded_nodes": degradedNodes,
			"threshold":      threshold,
			"total_nodes":    stats.TotalNodes,
			"healthy_nodes":  stats.HealthyNodes,
		},
	)
}

// ═══════════════════════════════════════════════════════════════════════════════
// P2: HYGIENE ALERTS
// ═══════════════════════════════════════════════════════════════════════════════

// checkWALDiskSpace warns when disk space on the WAL directory is low.
func (s *Sentinel) checkWALDiskSpace(pool *CoinPool, coin string) {
	walDir := pool.BlockWALDir()
	if walDir == "" {
		return
	}

	freeBytes, err := checkDiskSpaceAvailable(walDir)
	if err != nil {
		return // Can't check — don't alert
	}

	warningBytes := uint64(s.cfg.WALDiskSpaceWarningMB) * 1024 * 1024
	if freeBytes < warningBytes {
		freeMB := freeBytes / (1024 * 1024)
		s.fireAlert(context.Background(), "wal_disk_space_low", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("WAL disk space low: %d MB free (warning threshold: %d MB) on %s",
				freeMB, s.cfg.WALDiskSpaceWarningMB, coin),
			map[string]interface{}{
				"free_mb":      freeMB,
				"threshold_mb": s.cfg.WALDiskSpaceWarningMB,
				"wal_dir":      walDir,
			},
		)
	}
}

// checkWALFileCount warns when too many WAL files have accumulated.
func (s *Sentinel) checkWALFileCount(pool *CoinPool, coin string) {
	walDir := pool.BlockWALDir()
	if walDir == "" {
		return
	}

	count, err := CountWALFiles(walDir)
	if err != nil {
		return
	}

	if count > s.cfg.WALMaxFiles {
		s.fireAlert(context.Background(), "wal_file_count_high", severityWarning, coin, pool.PoolID(),
			fmt.Sprintf("WAL file count (%d) exceeds threshold (%d) on %s — consider running cleanup",
				count, s.cfg.WALMaxFiles, coin),
			map[string]interface{}{
				"count":     count,
				"threshold": s.cfg.WALMaxFiles,
				"wal_dir":   walDir,
			},
		)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CROSS-POOL CHECKS
// ═══════════════════════════════════════════════════════════════════════════════

// checkFalseRejectionRate monitors the ratio of false rejections to total submissions.
// A false rejection occurs when submitblock returns an error but the block is later
// found in the chain via getblockhash — meaning the daemon accepted it despite the error.
// A high false rejection rate indicates daemon RPC instability.
func (s *Sentinel) checkFalseRejectionRate() {
	if s.metrics == nil {
		return
	}

	currentFR := readMetricValue(s.metrics.BlocksFalseRejection)
	currentSubmitted := readMetricValue(s.metrics.BlocksSubmitted)

	s.mu.Lock()
	prevFR := s.prevFalseRejections
	prevSubmitted := s.prevSubmitted
	s.prevFalseRejections = currentFR
	s.prevSubmitted = currentSubmitted
	s.mu.Unlock()

	// First check — record baseline, don't alert
	if prevFR == 0 && prevSubmitted == 0 {
		return
	}

	newFR := currentFR - prevFR
	newSubmitted := currentSubmitted - prevSubmitted

	// Need submissions in this interval to evaluate
	if newSubmitted <= 0 {
		return
	}

	// Rate = false rejections / total submissions in this interval
	rate := newFR / newSubmitted

	if rate >= s.cfg.FalseRejectionThreshold {
		s.fireAlert(context.Background(), "false_rejection_rate", severityWarning, "", "",
			fmt.Sprintf("Block false rejection rate %.1f%% exceeds threshold %.0f%% (%.0f false rejections / %.0f submissions in last interval)",
				rate*100, s.cfg.FalseRejectionThreshold*100, newFR, newSubmitted),
			map[string]interface{}{
				"rate":       rate,
				"threshold":  s.cfg.FalseRejectionThreshold,
				"new_false":  newFR,
				"new_submit": newSubmitted,
				"total_false": currentFR,
				"total_submit": currentSubmitted,
			},
		)
	}
}

// checkRetryStorm detects abnormally high block submission retry rates.
// Retries indicate transient daemon RPC failures during block submission — a few are
// normal, but a sustained storm suggests daemon instability or network issues that
// risk block loss.
func (s *Sentinel) checkRetryStorm() {
	if s.metrics == nil {
		return
	}

	current := readMetricValue(s.metrics.BlockSubmitRetries)

	s.mu.Lock()
	prev := s.prevRetries
	s.prevRetries = current
	s.mu.Unlock()

	// First check — record baseline, don't alert
	if prev == 0 && current == 0 {
		return
	}

	newRetries := current - prev
	if newRetries <= 0 {
		return
	}

	// Project the interval retry count to an hourly rate for threshold comparison.
	// RetryRateThreshold is defined as max retries per hour.
	intervalHours := s.cfg.CheckInterval.Hours()
	if intervalHours <= 0 {
		return
	}
	hourlyRate := newRetries / intervalHours

	if hourlyRate > float64(s.cfg.RetryRateThreshold) {
		s.fireAlert(context.Background(), "retry_storm", severityWarning, "", "",
			fmt.Sprintf("Block submit retry storm: %.0f retries projected to %.0f/hr (threshold: %d/hr)",
				newRetries, hourlyRate, s.cfg.RetryRateThreshold),
			map[string]interface{}{
				"new_retries":     newRetries,
				"hourly_rate":     hourlyRate,
				"threshold_per_h": s.cfg.RetryRateThreshold,
				"total_retries":   current,
				"check_interval":  s.cfg.CheckInterval.String(),
			},
		)
	}
}

// checkPaymentProcessors detects stalled payment processors.
// If pending blocks aren't being confirmed/paid for multiple consecutive checks, something is wrong.
func (s *Sentinel) checkPaymentProcessors(ctx context.Context) {
	if s.cfg.PaymentStallChecks <= 0 {
		return
	}

	for poolID, proc := range s.coordinator.paymentProcessors {
		stats, err := proc.Stats(ctx)
		if err != nil {
			s.logger.Debugw("Sentinel: failed to get payment stats",
				"poolId", poolID,
				"error", err,
			)
			continue
		}

		s.mu.Lock()
		prevPending, hasPrev := s.paymentPending[poolID]
		s.paymentPending[poolID] = stats.PendingBlocks

		if !hasPrev {
			s.mu.Unlock()
			continue
		}

		// If we have pending blocks and the count hasn't decreased (no blocks confirmed)
		if stats.PendingBlocks > 0 && stats.PendingBlocks >= prevPending {
			s.paymentStallCount[poolID]++
		} else {
			s.paymentStallCount[poolID] = 0
		}
		stallCount := s.paymentStallCount[poolID]
		s.mu.Unlock()

		if stallCount >= s.cfg.PaymentStallChecks {
			severity := severityWarning
			if stallCount >= s.cfg.PaymentStallChecks*2 {
				severity = severityCritical
			}

			coin := s.poolIDToCoin(poolID)

			s.fireAlert(ctx, "payment_processor_stalled", severity, coin, poolID,
				fmt.Sprintf("Payment processor stalled for %s: %d pending blocks unchanged for %d checks",
					poolID, stats.PendingBlocks, stallCount),
				map[string]interface{}{
					"pending_blocks": stats.PendingBlocks,
					"stall_checks":  stallCount,
					"threshold":     s.cfg.PaymentStallChecks,
				},
			)
		}
	}
}

// checkDBFailover detects when the database manager has failed over to a different node.
// This is informational but operationally important — the failed primary needs investigation.
func (s *Sentinel) checkDBFailover() {
	if s.coordinator.dbManager == nil {
		return
	}

	stats := s.coordinator.dbManager.Stats()

	s.mu.Lock()
	prevFailovers := s.prevDBFailovers
	s.prevDBFailovers = stats.Failovers
	s.mu.Unlock()

	// Only alert on new failovers (not the initial read)
	if prevFailovers > 0 && stats.Failovers > prevFailovers {
		newFailovers := stats.Failovers - prevFailovers
		s.fireAlert(context.Background(), "db_failover", severityWarning, "", "",
			fmt.Sprintf("Database failover detected: %d new failover(s), now on node %s (%d/%d healthy)",
				newFailovers, stats.ActiveNode, stats.HealthyNodes, stats.TotalNodes),
			map[string]interface{}{
				"failover_count": stats.Failovers,
				"active_node":    stats.ActiveNode,
				"healthy_nodes":  stats.HealthyNodes,
				"total_nodes":    stats.TotalNodes,
				"write_failures": stats.WriteFailures,
				"read_failures":  stats.ReadFailures,
			},
		)
	}
}

// trackHARoleChange records HA role transitions for flap detection.
// Called BEFORE the HA gate in check() so it observes ALL transitions,
// including demotions to BACKUP that would otherwise be invisible.
func (s *Sentinel) trackHARoleChange(currentRole string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Detect role change
	if s.prevHARole != "" && currentRole != s.prevHARole {
		s.haRoleHistory = append(s.haRoleHistory, time.Now())
	}
	s.prevHARole = currentRole

	// Trim old entries outside the flap window
	cutoff := time.Now().Add(-s.cfg.HAFlapWindow)
	trimIdx := 0
	for trimIdx < len(s.haRoleHistory) && s.haRoleHistory[trimIdx].Before(cutoff) {
		trimIdx++
	}
	if trimIdx > 0 {
		s.haRoleHistory = s.haRoleHistory[trimIdx:]
	}
}

// checkHAFlapping fires an alert when too many role transitions have been recorded.
// The actual role tracking happens in trackHARoleChange() which runs before the HA gate.
// This function only reads the tracked state and fires if the threshold is exceeded.
func (s *Sentinel) checkHAFlapping() {
	if s.coordinator.vipManager == nil {
		return
	}

	s.mu.Lock()
	roleChanges := len(s.haRoleHistory)
	currentRole := s.prevHARole
	s.mu.Unlock()

	if roleChanges >= s.cfg.HAFlapThreshold {
		s.fireAlert(context.Background(), "ha_flapping", severityCritical, "", "",
			fmt.Sprintf("HA role flapping detected: %d role changes in %s window (threshold: %d)",
				roleChanges, s.cfg.HAFlapWindow, s.cfg.HAFlapThreshold),
			map[string]interface{}{
				"role_changes": roleChanges,
				"window":       s.cfg.HAFlapWindow.String(),
				"threshold":    s.cfg.HAFlapThreshold,
				"current_role": currentRole,
			},
		)
	}
}

// checkOrphanRate monitors the overall orphan rate from payment processors.
// A high orphan rate may indicate network issues, slow block propagation, or misconfiguration.
func (s *Sentinel) checkOrphanRate(ctx context.Context) {
	if s.cfg.OrphanRateThreshold <= 0 {
		return
	}

	for poolID, proc := range s.coordinator.paymentProcessors {
		stats, err := proc.Stats(ctx)
		if err != nil {
			continue
		}

		totalBlocks := stats.PendingBlocks + stats.ConfirmedBlocks + stats.OrphanedBlocks + stats.PaidBlocks

		// Need minimum sample size to avoid false alarms from normal variance
		if totalBlocks < 10 {
			continue
		}

		orphanRate := float64(stats.OrphanedBlocks) / float64(totalBlocks)

		if orphanRate >= s.cfg.OrphanRateThreshold {
			coin := s.poolIDToCoin(poolID)

			s.fireAlert(ctx, "orphan_rate_high", severityWarning, coin, poolID,
				fmt.Sprintf("High orphan rate for %s: %.1f%% (%d/%d blocks orphaned, threshold: %.0f%%)",
					poolID, orphanRate*100, stats.OrphanedBlocks, totalBlocks, s.cfg.OrphanRateThreshold*100),
				map[string]interface{}{
					"orphan_rate":     orphanRate,
					"orphaned_blocks": stats.OrphanedBlocks,
					"total_blocks":    totalBlocks,
					"threshold":       s.cfg.OrphanRateThreshold,
				},
			)
		}
	}
}

// checkBlockMaturityStall detects found blocks that are stuck in pending status for too long.
// This is a cross-pool check that reads Prometheus metrics set by the payment processor.
// A block stuck in pending for hours means it's not gaining confirmations — likely indicates
// daemon desync, chain stall, or orphaned block that wasn't detected.
func (s *Sentinel) checkBlockMaturityStall() {
	if s.cfg.MaturityStallHours <= 0 || s.metrics == nil {
		return // Disabled or no metrics
	}

	pendingCount := readMetricValue(s.metrics.BlocksPendingMaturityCount)
	oldestAgeSec := readMetricValue(s.metrics.BlocksOldestPendingAgeSec)

	if pendingCount <= 0 {
		return // No pending blocks
	}

	thresholdSec := float64(s.cfg.MaturityStallHours) * 3600.0
	if oldestAgeSec > thresholdSec {
		s.fireAlert(context.Background(), "block_maturity_stall", severityWarning, "", "",
			fmt.Sprintf("Found block stuck in pending for %.1f hours (%.0f pending blocks, threshold: %dh) — not gaining confirmations",
				oldestAgeSec/3600.0, pendingCount, s.cfg.MaturityStallHours),
			map[string]interface{}{
				"pending_count":      int(pendingCount),
				"oldest_age_hours":   oldestAgeSec / 3600.0,
				"threshold_hours":    s.cfg.MaturityStallHours,
			},
		)
	}
}

// checkGoroutineCount detects potential goroutine leaks by monitoring the runtime goroutine count.
// Goroutine leaks indicate connection cleanup failures or stuck handlers; eventually leads to OOM.
func (s *Sentinel) checkGoroutineCount() {
	current := runtime.NumGoroutine()

	s.mu.Lock()
	if s.baselineGoroutines == 0 {
		s.baselineGoroutines = current
		s.mu.Unlock()
		return
	}
	baseline := s.baselineGoroutines
	s.mu.Unlock()

	// Alert if absolute limit exceeded
	if current > s.cfg.GoroutineLimit {
		s.fireAlert(context.Background(), "goroutine_limit", severityWarning, "", "",
			fmt.Sprintf("Goroutine count %d exceeds limit %d (baseline: %d) — possible goroutine leak",
				current, s.cfg.GoroutineLimit, baseline),
			map[string]interface{}{
				"current":   current,
				"baseline":  baseline,
				"limit":     s.cfg.GoroutineLimit,
				"growth_2x": current > baseline*2,
			},
		)
		return
	}

	// Alert if count has more than doubled from baseline
	if baseline > 0 && current > baseline*2 {
		s.fireAlert(context.Background(), "goroutine_growth", severityWarning, "", "",
			fmt.Sprintf("Goroutine count %d is >2x baseline %d — possible goroutine leak",
				current, baseline),
			map[string]interface{}{
				"current":  current,
				"baseline": baseline,
				"ratio":    float64(current) / float64(baseline),
			},
		)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ALERT DISPATCH
// ═══════════════════════════════════════════════════════════════════════════════

// fireAlert sends an alert if not in cooldown. Handles deduplication and metrics.
// NOTE: Uses cooldownMu (not mu) to avoid deadlock when called from check functions
// that hold mu for state tracking.
func (s *Sentinel) fireAlert(ctx context.Context, alertType string, severity alertSeverity, coin, poolID, message string, details interface{}) {
	// AUDIT FIX: Include poolID in cooldown key so alerts from different pools
	// (even for the same coin) are deduplicated independently. Without this,
	// a legitimate alert from pool B could be suppressed because pool A fired
	// the same alert type recently.
	key := alertType + ":" + coin + ":" + poolID

	s.cooldownMu.Lock()
	lastFired, hasCooldown := s.cooldowns[key]
	if hasCooldown && time.Since(lastFired) < s.cfg.AlertCooldown {
		s.cooldownMu.Unlock()
		return // Still in cooldown
	}
	s.cooldowns[key] = time.Now()
	s.cooldownMu.Unlock()

	// Log the alert
	logFn := s.logger.Warnw
	if severity == severityCritical {
		logFn = s.logger.Errorw
	}
	logFn("Sentinel alert fired",
		"alertType", alertType,
		"severity", string(severity),
		"coin", coin,
		"poolId", poolID,
		"message", message,
	)

	// Update metrics
	if s.metrics != nil {
		s.metrics.SentinelAlertsTotal.WithLabelValues(alertType, string(severity), coin).Inc()
	}

	// Append to ring buffer (exposed via /api/sentinel/alerts for Python Spiral Sentinel)
	alert := SentinelAlert{
		AlertType: alertType,
		Severity:  string(severity),
		Coin:      coin,
		PoolID:    poolID,
		Message:   message,
		Timestamp: time.Now(),
	}
	s.recentAlertsMu.Lock()
	s.recentAlerts = append(s.recentAlerts, alert)
	if len(s.recentAlerts) > maxRecentAlerts {
		// Drop oldest entries to maintain ring buffer size
		s.recentAlerts = s.recentAlerts[len(s.recentAlerts)-maxRecentAlerts:]
	}
	s.recentAlertsMu.Unlock()
}

// GetRecentAlerts returns alerts newer than the given timestamp.
// Used by the API endpoint /api/sentinel/alerts so the Python Spiral Sentinel
// can poll and forward alerts to Discord/Telegram.
func (s *Sentinel) GetRecentAlerts(since time.Time) []SentinelAlert {
	s.recentAlertsMu.RLock()
	defer s.recentAlertsMu.RUnlock()

	var result []SentinelAlert
	for _, a := range s.recentAlerts {
		if a.Timestamp.After(since) {
			result = append(result, a)
		}
	}
	return result
}

// ═══════════════════════════════════════════════════════════════════════════════
// HELPERS
// ═══════════════════════════════════════════════════════════════════════════════

// truncateHash returns a safe-to-log prefix of a block hash.
func truncateHash(hash string) string {
	if len(hash) > 16 {
		return hash[:16] + "..."
	}
	return hash
}

// poolIDToCoin maps a pool ID back to its coin symbol via the coordinator's pool map.
func (s *Sentinel) poolIDToCoin(poolID string) string {
	s.coordinator.poolsMu.RLock()
	defer s.coordinator.poolsMu.RUnlock()
	if pool, ok := s.coordinator.pools[poolID]; ok {
		return pool.Symbol()
	}
	return poolID // Fallback to poolID if not found
}

// readMetricValue reads the current float64 value from a prometheus Counter or Gauge.
// Uses the prometheus Metric.Write() interface to extract the value via protobuf.
// Returns 0 if the metric cannot be read.
func readMetricValue(m interface{ Write(*dto.Metric) error }) float64 {
	if m == nil {
		return 0
	}
	var pm dto.Metric
	if err := m.Write(&pm); err != nil {
		return 0
	}
	if c := pm.GetCounter(); c != nil {
		return c.GetValue()
	}
	if g := pm.GetGauge(); g != nil {
		return g.GetValue()
	}
	return 0
}
