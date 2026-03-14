// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package metrics provides Prometheus metrics for pool monitoring.
//
// These metrics provide comprehensive observability into pool operations,
// including connection counts, share rates, hashrate, and latencies.
package metrics

import (
	"context"
	"crypto/subtle"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// Metrics holds all Prometheus metrics for the pool.
type Metrics struct {
	cfg    *config.MetricsConfig
	logger *zap.SugaredLogger
	server *http.Server

	// Connection metrics
	ConnectionsTotal    prometheus.Counter
	ConnectionsActive   prometheus.Gauge
	ConnectionsRejected prometheus.Counter

	// Share metrics
	SharesSubmitted prometheus.Counter
	SharesAccepted  prometheus.Counter
	SharesRejected  *prometheus.CounterVec
	SharesStale     prometheus.Counter

	// Share batch loss metrics (HA failover observability)
	ShareBatchesDropped   prometheus.Counter   // Total batches that failed permanently
	SharesInDroppedBatch  prometheus.Counter   // Total shares lost due to batch drops
	ShareBatchRetries     prometheus.Counter   // Total batch retry attempts
	ShareBatchLossRate    prometheus.Gauge     // Current loss rate (shares lost / shares processed)

	// Block metrics
	BlocksFound            prometheus.Counter
	BlocksConfirmed        prometheus.Counter
	BlocksOrphaned         prometheus.Counter
	BlocksSubmissionFailed prometheus.Counter // Tracks block submission failures (post-retry)
	BlocksSubmitted        prometheus.Counter // Blocks accepted by daemon (submit or verify)
	BlocksFalseRejection   prometheus.Counter // Blocks rejected by submitblock but verified in chain
	BlockSubmitRetries     prometheus.Counter // Total retry attempts for block submission
	BlockStaleRace         prometheus.Counter // Blocks detected stale via direct chain tip check
	BlockSubmitLatency     prometheus.Histogram  // Latency from block found to submission result
	BlockRejectionsByReason *prometheus.CounterVec // Rejection breakdown by reason (stale, high_hash, duplicate, etc.)
	BlockHeightAborts       prometheus.Counter     // Submissions canceled because chain tip advanced (height epoch)
	BlockDeadlineAborts     prometheus.Counter     // Submissions canceled because submit deadline expired
	BlockDeadlineUsage      prometheus.Histogram   // Fraction of deadline consumed (0.0-1.0) for monitoring deadline tightness
	TargetSourceUsed       *prometheus.CounterVec  // Which target source was used for block detection (gbt, compact_bits, float64_diff, none)

	// AUDIT FIX (ISSUE-4): Per-coin block metrics for V2 multi-coin observability.
	// These complement the aggregate counters above with a "coin" label, enabling
	// per-chain alerting without breaking existing dashboards.
	BlocksFoundByCoin            *prometheus.CounterVec
	BlocksSubmittedByCoin        *prometheus.CounterVec
	BlocksOrphanedByCoin         *prometheus.CounterVec
	BlocksConfirmedByCoin        *prometheus.CounterVec
	BlocksSubmissionFailedByCoin *prometheus.CounterVec

	// Hashrate metrics
	PoolHashrate    prometheus.Gauge
	NetworkHashrate prometheus.Gauge

	// Difficulty metrics
	NetworkDifficulty  prometheus.Gauge
	VardiffAdjustments prometheus.Counter
	BestShareDiff      prometheus.Gauge // Highest share difficulty ever submitted
	AvgShareDiff       prometheus.Gauge // Running average share difficulty
	JobsDispatched     prometheus.Counter // Total jobs dispatched to miners

	// Latency metrics
	ShareValidationLatency prometheus.Histogram
	JobBroadcastLatency    prometheus.Histogram
	DBWriteLatency         prometheus.Histogram
	RPCCallLatency         prometheus.Histogram

	// Payment metrics
	PaymentsPending             prometheus.Gauge
	PaymentsSent                prometheus.Counter
	PaymentsFailed              prometheus.Counter
	PaymentProcessorFailedCycles prometheus.Gauge // Consecutive failed payment cycles (alert at >= 5)
	PaidBlockReorgs             prometheus.Counter // Paid blocks detected as orphaned by deep reorg

	// System metrics
	GoroutineCount prometheus.Gauge
	MemoryAlloc    prometheus.Gauge
	BufferUsage    prometheus.Gauge

	// ZMQ metrics
	ZMQConnected        prometheus.Gauge // 1 if connected, 0 if not
	ZMQMessagesReceived prometheus.Counter
	ZMQReconnects       prometheus.Counter
	BlockNotifyMode     prometheus.Gauge // 1 for ZMQ, 0 for polling
	ZMQHealthStatus     prometheus.Gauge // ZMQ health: 0=disabled, 1=connecting, 2=healthy, 3=degraded, 4=failed
	ZMQLastMessageAge   prometheus.Gauge // Seconds since last ZMQ message

	// Worker metrics (per-worker labels for Grafana dashboards)
	WorkerHashrate    *prometheus.GaugeVec   // Labels: miner, worker
	WorkerShares      *prometheus.CounterVec // Labels: miner, worker, status
	WorkerConnected   *prometheus.GaugeVec   // Labels: miner, worker (1=connected, 0=disconnected)
	WorkerDifficulty  *prometheus.GaugeVec   // Labels: miner, worker
	ActiveWorkerCount prometheus.Gauge       // Total active workers

	// Circuit breaker metrics (database write protection)
	CircuitBreakerState       prometheus.Gauge   // 0=closed, 1=open, 2=half-open
	CircuitBreakerBlocked     prometheus.Counter // Batches blocked by open circuit
	CircuitBreakerTransitions prometheus.Counter // State transitions

	// V26 FIX: Block status guard rejection metrics
	// Tracks when V12 status guards block a status update (e.g., preventing
	// confirmed→pending demotion from a stale process). Non-zero rate indicates
	// either a multi-instance race or a stale process still running.
	BlockStatusGuardRejections prometheus.Counter

	// WAL (Write-Ahead Log) metrics for recovery observability
	WALReplayCount    prometheus.Counter // Shares replayed from WAL on startup
	WALReplayDuration prometheus.Gauge   // Duration of last WAL replay in seconds
	WALWriteErrors    prometheus.Counter // WAL write failures
	WALCommitErrors   prometheus.Counter // WAL commit failures
	WALSyncErrors     prometheus.Counter // WAL sync failures
	WALFileSize       prometheus.Gauge   // Current WAL file size in bytes

	// V43 FIX: Aux block submission metrics (merge mining observability)
	AuxBlocksSubmitted *prometheus.CounterVec // Labels: coin
	AuxBlocksFailed    *prometheus.CounterVec // Labels: coin, reason

	// V44 FIX: Metrics server health tracking
	serverFailed atomic.Bool // True if ListenAndServe returned a non-clean error

	// V45 FIX: Health check callback — wired by pool.go to report real subsystem health
	healthCheck func() (healthy bool, details string)

	// Backpressure metrics for share pipeline observability
	BackpressureLevel          prometheus.Gauge   // Current level: 0=none, 1=warn, 2=critical, 3=emergency
	BackpressureLevelChanges   prometheus.Counter // Total backpressure level transitions
	BackpressureBufferFill     prometheus.Gauge   // Buffer fill percentage (0-100)
	BackpressureDiffMultiplier prometheus.Gauge   // Suggested difficulty multiplier from backpressure

	// HA cluster metrics
	HAClusterState      *prometheus.GaugeVec // labels: state (running/election/failover/degraded)
	HANodeRole          *prometheus.GaugeVec // labels: role (master/backup/observer)
	HAFailoverTotal     prometheus.Counter
	HAElectionTotal     prometheus.Counter
	HAFlapDetected      prometheus.Counter
	HAPartitionDetected prometheus.Counter
	HANodesTotal        prometheus.Gauge
	HANodesHealthy      prometheus.Gauge

	// DB HA metrics
	DBFailoverTotal        prometheus.Counter
	DBActiveNode           *prometheus.GaugeVec // labels: node_id
	DBCircuitBreakerState  *prometheus.GaugeVec // labels: state (closed/open/halfopen)
	DBBlockQueueLen        prometheus.Gauge

	// Redis HA metrics
	RedisHealthy        prometheus.Gauge   // 1=healthy, 0=unhealthy
	RedisReconnects     prometheus.Counter
	RedisFallbackActive prometheus.Gauge   // 1=using local fallback

	// Daemon health metrics (Sentinel: chain-tip stall and peer count monitoring)
	DaemonBlockHeight       *prometheus.GaugeVec // Labels: coin — current best block height
	DaemonPeerCount         *prometheus.GaugeVec // Labels: coin — peer connections on primary node
	DaemonChainTipStaleSec  *prometheus.GaugeVec // Labels: coin — seconds since height last advanced

	// Block maturity metrics (Sentinel: maturity stall monitoring)
	BlocksPendingMaturityCount prometheus.Gauge // Number of blocks awaiting maturity
	BlocksOldestPendingAgeSec  prometheus.Gauge // Age in seconds of oldest pending block

	// Sentinel alerting metrics
	SentinelAlertsTotal    *prometheus.CounterVec // Labels: type, severity, coin
	SentinelAlertsFiring   prometheus.Gauge       // Currently active (non-cooldown) alerts
	SentinelCheckDuration  prometheus.Histogram   // How long each sentinel check takes
	SentinelWebhookErrors  prometheus.Counter     // Failed webhook deliveries
	SentinelWALPendingAge  prometheus.Gauge       // Max age of any pending WAL entry (seconds)
	SentinelNodesDown      *prometheus.GaugeVec   // Labels: coin — 1 if all nodes down, 0 otherwise
	SentinelHashrateChange *prometheus.GaugeVec   // Labels: coin — percent change since last check
	SentinelConnectionDrop *prometheus.GaugeVec   // Labels: coin — percent drop since last check

	// Internal counters for atomic updates
	activeConns   atomic.Int64
	activeWorkers atomic.Int64
	bestShareDiff atomic.Uint64 // Stored as bits of float64
}

// New creates a new Metrics instance and registers all metrics.
func New(cfg *config.MetricsConfig, logger *zap.Logger) *Metrics {
	m := &Metrics{
		cfg:    cfg,
		logger: logger.Sugar(),
	}

	// Connection metrics
	m.ConnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_connections_total",
		Help: "Total number of connections ever",
	})
	m.ConnectionsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_connections_active",
		Help: "Current active connections",
	})
	m.ConnectionsRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_connections_rejected_total",
		Help: "Total rejected connections",
	})

	// Share metrics
	m.SharesSubmitted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_shares_submitted_total",
		Help: "Total shares submitted",
	})
	m.SharesAccepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_shares_accepted_total",
		Help: "Total accepted shares",
	})
	m.SharesRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_shares_rejected_total",
		Help: "Total rejected shares by reason",
	}, []string{"reason"})
	m.SharesStale = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_shares_stale_total",
		Help: "Total stale shares",
	})

	// Share batch loss metrics (HA failover observability)
	m.ShareBatchesDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_share_batches_dropped_total",
		Help: "Total share batches that failed permanently (after all retries exhausted). Non-zero indicates data loss during failover.",
	})
	m.SharesInDroppedBatch = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_shares_in_dropped_batch_total",
		Help: "Total individual shares lost due to batch drops. Alert threshold: > 0 during stable operation.",
	})
	m.ShareBatchRetries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_share_batch_retries_total",
		Help: "Total batch retry attempts due to database write failures. High values indicate DB connectivity issues.",
	})
	m.ShareBatchLossRate = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_share_batch_loss_rate",
		Help: "Share loss rate (shares lost / shares processed). Should be 0 under normal operation.",
	})

	// Block metrics
	m.BlocksFound = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_blocks_found_total",
		Help: "Total blocks found",
	})
	m.BlocksConfirmed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_blocks_confirmed_total",
		Help: "Total confirmed blocks",
	})
	m.BlocksOrphaned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_blocks_orphaned_total",
		Help: "Total orphaned blocks",
	})
	m.BlocksSubmissionFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_blocks_submission_failed_total",
		Help: "Total block submissions that failed after all retries. Non-zero indicates potential block loss. Alert immediately.",
	})
	m.BlocksSubmitted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_blocks_submitted_total",
		Help: "Total blocks accepted by daemon (via submitblock or chain verification)",
	})
	m.BlocksFalseRejection = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_blocks_false_rejection_total",
		Help: "Blocks where submitblock failed but getblockhash confirmed acceptance",
	})
	m.BlockSubmitRetries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_block_submit_retries_total",
		Help: "Total retry attempts for transient block submission failures",
	})
	m.BlockStaleRace = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_block_stale_race_total",
		Help: "Blocks detected stale before submission (ZMQ invalidation or chain tip mismatch)",
	})
	m.BlockSubmitLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "stratum_block_submit_latency_ms",
		Help: "Latency from block detection to submission result (milliseconds)",
		// Covers all supported coins from 15s blocks (DGB) to 600s blocks (BTC/BCH/NMC).
		// Measures daemon RPC round-trip, not block staleness — a healthy local daemon
		// responds in <50ms regardless of coin. Higher values indicate daemon issues.
		//
		// Staleness thresholds vary by coin (% of block time consumed):
		//   15s coins (DGB):        250ms=1.7%  1s=6.7%   5s=33%   15s=stale
		//   30s coins (FBTC):       250ms=0.8%  1s=3.3%   5s=17%   30s=stale
		//   60s coins (DOGE, SYS):  250ms=0.4%  1s=1.7%   5s=8%    60s=stale
		//   150s coins (LTC):       250ms=0.2%  1s=0.7%   5s=3%    30s=20%
		//   600s coins (BTC, BCH, QBX): 250ms=0.04% 1s=0.2% 5s=0.8% 30s=5%
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2000, 5000, 15000, 30000},
	})
	m.BlockRejectionsByReason = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_block_rejections_total",
		Help: "Block rejections by reason. Labels: stale_race, duplicate_candidate, chain_tip_moved, high_hash, prev_blk_not_found, duplicate, timeout, build_failed, unknown",
	}, []string{"reason"})
	m.BlockHeightAborts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_block_height_aborts_total",
		Help: "Block submissions aborted because chain tip advanced (height epoch cancellation)",
	})
	m.BlockDeadlineAborts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_block_deadline_aborts_total",
		Help: "Block submissions aborted because submit deadline expired before success",
	})
	m.BlockDeadlineUsage = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratum_block_deadline_usage_ratio",
		Help:    "Fraction of submit deadline consumed (0.0-1.0). Values near 1.0 indicate deadline is too tight.",
		Buckets: []float64{0.01, 0.05, 0.10, 0.25, 0.50, 0.75, 0.90, 0.95, 0.99, 1.0},
	})
	m.TargetSourceUsed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_target_source_used_total",
		Help: "Which target source was used for block detection. Labels: gbt, compact_bits, float64_diff, none",
	}, []string{"source"})

	// AUDIT FIX (ISSUE-4): Per-coin block metrics for V2 multi-coin observability.
	m.BlocksFoundByCoin = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_blocks_found_by_coin_total",
		Help: "Blocks found per coin (V2 multi-coin breakdown)",
	}, []string{"coin"})
	m.BlocksSubmittedByCoin = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_blocks_submitted_by_coin_total",
		Help: "Blocks submitted per coin (V2 multi-coin breakdown)",
	}, []string{"coin"})
	m.BlocksOrphanedByCoin = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_blocks_orphaned_by_coin_total",
		Help: "Blocks orphaned per coin (V2 multi-coin breakdown)",
	}, []string{"coin"})
	m.BlocksConfirmedByCoin = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_blocks_confirmed_by_coin_total",
		Help: "Blocks confirmed per coin (V2 multi-coin breakdown)",
	}, []string{"coin"})
	m.BlocksSubmissionFailedByCoin = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_blocks_submission_failed_by_coin_total",
		Help: "Block submission failures per coin (V2 multi-coin breakdown)",
	}, []string{"coin"})

	// Hashrate metrics
	m.PoolHashrate = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_hashrate_pool_hps",
		Help: "Pool hashrate in H/s",
	})
	m.NetworkHashrate = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_hashrate_network_hps",
		Help: "Network hashrate in H/s",
	})

	// Difficulty metrics
	m.NetworkDifficulty = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_network_difficulty",
		Help: "Current network difficulty",
	})
	m.VardiffAdjustments = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_vardiff_adjustments_total",
		Help: "Total VARDIFF adjustments",
	})
	m.BestShareDiff = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_best_share_difficulty",
		Help: "Highest share difficulty ever submitted to the pool",
	})
	m.AvgShareDiff = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_avg_share_difficulty",
		Help: "Running average share difficulty across all miners",
	})
	m.JobsDispatched = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_jobs_dispatched_total",
		Help: "Total mining jobs dispatched to connected miners",
	})

	// Latency metrics
	m.ShareValidationLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratum_share_validation_seconds",
		Help:    "Share validation latency",
		Buckets: prometheus.ExponentialBuckets(0.0001, 2, 12), // 100μs to ~400ms
	})
	m.JobBroadcastLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratum_job_broadcast_seconds",
		Help:    "Job broadcast latency",
		Buckets: prometheus.ExponentialBuckets(0.0001, 2, 12),
	})
	m.DBWriteLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratum_db_write_seconds",
		Help:    "Database write latency",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 10), // 1ms to ~1s
	})
	m.RPCCallLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratum_rpc_call_seconds",
		Help:    "Daemon RPC call latency",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 10), // 10ms to ~10s
	})

	// Payment metrics
	m.PaymentsPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_payments_pending_count",
		Help: "Pending payments count",
	})
	m.PaymentsSent = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_payments_sent_total",
		Help: "Total payments sent",
	})
	m.PaymentsFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_payments_failed_total",
		Help: "Total failed payments",
	})
	m.PaymentProcessorFailedCycles = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_payment_processor_failed_cycles",
		Help: "Consecutive failed payment processing cycles. Alert when >= 5 — indicates processor may be dead (daemon down, DB unreachable).",
	})
	m.PaidBlockReorgs = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_paid_block_reorgs_total",
		Help: "CRITICAL: Paid blocks detected as orphaned by deep reorg. Non-zero means financial loss — manual intervention required.",
	})

	// System metrics
	m.GoroutineCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_goroutines_count",
		Help: "Current goroutine count",
	})
	m.MemoryAlloc = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_memory_alloc_bytes",
		Help: "Allocated memory in bytes",
	})
	m.BufferUsage = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_buffer_usage_ratio",
		Help: "Share buffer usage ratio",
	})

	// ZMQ metrics
	m.ZMQConnected = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_zmq_connected",
		Help: "ZMQ connection status (1=connected, 0=disconnected)",
	})
	m.ZMQMessagesReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_zmq_messages_received_total",
		Help: "Total ZMQ messages received",
	})
	m.ZMQReconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_zmq_reconnects_total",
		Help: "Total ZMQ reconnection attempts",
	})
	m.BlockNotifyMode = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_block_notify_mode",
		Help: "Block notification mode (1=ZMQ, 0=RPC polling)",
	})
	m.ZMQHealthStatus = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_zmq_health_status",
		Help: "ZMQ health status (0=disabled, 1=connecting, 2=healthy, 3=degraded, 4=failed). Alert when > 2.",
	})
	m.ZMQLastMessageAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_zmq_last_message_age_seconds",
		Help: "Seconds since last ZMQ message received. High values indicate ZMQ disconnection.",
	})

	// Worker metrics (for Grafana dashboards)
	// CARDINALITY WARNING: These use miner+worker labels which can grow unbounded.
	// Consider using recording rules in Prometheus to aggregate high-cardinality metrics.
	m.WorkerHashrate = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_worker_hashrate_hps",
		Help: "Per-worker hashrate in H/s",
	}, []string{"miner", "worker"})

	m.WorkerShares = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_worker_shares_total",
		Help: "Per-worker share count by status",
	}, []string{"miner", "worker", "status"})

	m.WorkerConnected = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_worker_connected",
		Help: "Worker connection status (1=connected, 0=disconnected)",
	}, []string{"miner", "worker"})

	m.WorkerDifficulty = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_worker_difficulty",
		Help: "Current worker difficulty",
	}, []string{"miner", "worker"})

	m.ActiveWorkerCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_workers_active",
		Help: "Current active worker count",
	})

	// Circuit breaker metrics
	m.CircuitBreakerState = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_circuit_breaker_state",
		Help: "Database circuit breaker state (0=closed, 1=open, 2=half-open). Alert when > 0.",
	})
	m.CircuitBreakerBlocked = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_circuit_breaker_blocked_total",
		Help: "Total batches blocked by open circuit breaker",
	})
	m.CircuitBreakerTransitions = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_circuit_breaker_transitions_total",
		Help: "Total circuit breaker state transitions",
	})

	// V43 FIX: Aux block submission metrics for merge mining observability
	m.AuxBlocksSubmitted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_aux_blocks_submitted_total",
		Help: "Total aux blocks submitted to aux chain daemons. Labels: coin symbol. Zero rate indicates silent aux chain failure.",
	}, []string{"coin"})
	m.AuxBlocksFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_aux_blocks_failed_total",
		Help: "Total aux block submission failures. Labels: coin symbol, reason. Non-zero indicates aux chain revenue loss.",
	}, []string{"coin", "reason"})

	// V26 FIX: Block status guard rejection metric
	m.BlockStatusGuardRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_block_status_guard_rejections_total",
		Help: "Total block status updates blocked by V12 status guards. Non-zero rate indicates multi-instance race or stale process.",
	})

	// WAL (Write-Ahead Log) metrics for recovery observability
	m.WALReplayCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_wal_replay_count_total",
		Help: "Total shares replayed from WAL on startup.",
	})
	m.WALReplayDuration = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_wal_replay_duration_seconds",
		Help: "Duration of last WAL replay operation in seconds.",
	})
	m.WALWriteErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_wal_write_errors_total",
		Help: "Total WAL write failures.",
	})
	m.WALCommitErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_wal_commit_errors_total",
		Help: "Total WAL commit failures.",
	})
	m.WALSyncErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_wal_sync_errors_total",
		Help: "Total WAL fsync failures.",
	})
	m.WALFileSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_wal_file_size_bytes",
		Help: "Current WAL file size in bytes. Large values may indicate commit issues.",
	})

	// Backpressure metrics for share pipeline observability
	m.BackpressureLevel = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_backpressure_level",
		Help: "Current backpressure level: 0=none, 1=warn (70%% buffer), 2=critical (90%%), 3=emergency (98%%). Alert when >= 2.",
	})
	m.BackpressureLevelChanges = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_backpressure_level_changes_total",
		Help: "Total backpressure level transitions. High values indicate unstable pipeline throughput.",
	})
	m.BackpressureBufferFill = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_backpressure_buffer_fill_percent",
		Help: "Share buffer fill percentage (0-100). Alert when > 70.",
	})
	m.BackpressureDiffMultiplier = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_backpressure_difficulty_multiplier",
		Help: "Suggested difficulty multiplier from backpressure system. 1.0=normal, >1.0=increase difficulty.",
	})

	// HA cluster metrics
	m.HAClusterState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_ha_cluster_state",
		Help: "HA cluster state. Label 'state' set to 1 when active: running, election, failover, degraded.",
	}, []string{"state"})
	m.HANodeRole = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_ha_node_role",
		Help: "HA node role. Label 'role' set to 1 when active: master, backup, observer.",
	}, []string{"role"})
	m.HAFailoverTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_ha_failovers_total",
		Help: "Total HA failover events. Non-zero rate indicates cluster instability.",
	})
	m.HAElectionTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_ha_elections_total",
		Help: "Total HA election events.",
	})
	m.HAFlapDetected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_ha_flap_detected_total",
		Help: "Total VIP flap detections. Non-zero indicates rapid failover oscillation.",
	})
	m.HAPartitionDetected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_ha_partition_detected_total",
		Help: "Total network partition detections.",
	})
	m.HANodesTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_ha_nodes_total",
		Help: "Total number of nodes in the HA cluster.",
	})
	m.HANodesHealthy = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_ha_nodes_healthy",
		Help: "Number of healthy nodes in the HA cluster.",
	})

	// DB HA metrics
	m.DBFailoverTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_db_failovers_total",
		Help: "Total database failover events.",
	})
	m.DBActiveNode = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_db_active_node",
		Help: "Active database node. Label 'node_id' set to 1 when active.",
	}, []string{"node_id"})
	m.DBCircuitBreakerState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_db_circuit_breaker_state",
		Help: "Database circuit breaker state per-node. Label 'state': closed=0, open=1, halfopen=2.",
	}, []string{"state"})
	m.DBBlockQueueLen = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_db_block_queue_length",
		Help: "Number of blocks queued for database write during failover.",
	})

	// Redis HA metrics
	m.RedisHealthy = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_redis_healthy",
		Help: "Redis health status (1=healthy, 0=unhealthy). Alert when 0.",
	})
	m.RedisReconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_redis_reconnects_total",
		Help: "Total Redis reconnection attempts.",
	})
	m.RedisFallbackActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_redis_fallback_active",
		Help: "Whether local fallback is active (1=fallback, 0=Redis). Alert when 1.",
	})

	// Daemon health metrics (Sentinel: chain-tip stall and peer count monitoring)
	m.DaemonBlockHeight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_daemon_block_height",
		Help: "Current best block height from daemon. Flatline indicates chain-tip stall.",
	}, []string{"coin"})
	m.DaemonPeerCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_daemon_peer_count",
		Help: "Peer connections on primary daemon node. Low values indicate network isolation.",
	}, []string{"coin"})
	m.DaemonChainTipStaleSec = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_daemon_chain_tip_stale_seconds",
		Help: "Seconds since daemon block height last advanced. High values indicate chain-tip stall — miners working on stale templates.",
	}, []string{"coin"})

	// Block maturity metrics (Sentinel: maturity stall monitoring)
	m.BlocksPendingMaturityCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_blocks_pending_maturity_count",
		Help: "Number of found blocks awaiting maturity confirmations.",
	})
	m.BlocksOldestPendingAgeSec = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_blocks_oldest_pending_age_seconds",
		Help: "Age in seconds of the oldest pending block. High values indicate maturity stall — block not gaining confirmations.",
	})

	// Sentinel alerting metrics
	m.SentinelAlertsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "stratum_sentinel_alerts_total",
		Help: "Total Sentinel alerts fired by type, severity, and coin.",
	}, []string{"type", "severity", "coin"})
	m.SentinelAlertsFiring = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_sentinel_alerts_firing",
		Help: "Number of currently active (non-cooldown) Sentinel alerts.",
	})
	m.SentinelCheckDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "stratum_sentinel_check_duration_seconds",
		Help:    "Duration of each Sentinel check cycle.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
	})
	m.SentinelWebhookErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stratum_sentinel_webhook_errors_total",
		Help: "Total failed Sentinel webhook deliveries.",
	})
	m.SentinelWALPendingAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "stratum_sentinel_wal_pending_age_seconds",
		Help: "Max age of any pending WAL entry in seconds. High values indicate stuck block submissions.",
	})
	m.SentinelNodesDown = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_sentinel_nodes_down",
		Help: "Whether all nodes are down for a coin (1=all down, 0=at least one healthy).",
	}, []string{"coin"})
	m.SentinelHashrateChange = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_sentinel_hashrate_change_percent",
		Help: "Hashrate percent change since last Sentinel check. Negative = drop.",
	}, []string{"coin"})
	m.SentinelConnectionDrop = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratum_sentinel_connection_drop_percent",
		Help: "Connection drop percent since last Sentinel check. Negative = drop.",
	}, []string{"coin"})

	// Register all metrics
	prometheus.MustRegister(
		m.ConnectionsTotal,
		m.ConnectionsActive,
		m.ConnectionsRejected,
		m.SharesSubmitted,
		m.SharesAccepted,
		m.SharesRejected,
		m.SharesStale,
		m.ShareBatchesDropped,
		m.SharesInDroppedBatch,
		m.ShareBatchRetries,
		m.ShareBatchLossRate,
		m.BlocksFound,
		m.BlocksConfirmed,
		m.BlocksOrphaned,
		m.BlocksSubmissionFailed,
		m.BlocksSubmitted,
		m.BlocksFalseRejection,
		m.BlockSubmitRetries,
		m.BlockStaleRace,
		m.BlockSubmitLatency,
		m.BlockRejectionsByReason,
		m.BlockHeightAborts,
		m.BlockDeadlineAborts,
		m.BlockDeadlineUsage,
		m.TargetSourceUsed,
		m.BlocksFoundByCoin,
		m.BlocksSubmittedByCoin,
		m.BlocksOrphanedByCoin,
		m.BlocksConfirmedByCoin,
		m.BlocksSubmissionFailedByCoin,
		m.PoolHashrate,
		m.NetworkHashrate,
		m.NetworkDifficulty,
		m.VardiffAdjustments,
		m.BestShareDiff,
		m.AvgShareDiff,
		m.JobsDispatched,
		m.ShareValidationLatency,
		m.JobBroadcastLatency,
		m.DBWriteLatency,
		m.RPCCallLatency,
		m.PaymentsPending,
		m.PaymentsSent,
		m.PaymentsFailed,
		m.PaymentProcessorFailedCycles,
		m.PaidBlockReorgs,
		m.GoroutineCount,
		m.MemoryAlloc,
		m.BufferUsage,
		m.ZMQConnected,
		m.ZMQMessagesReceived,
		m.ZMQReconnects,
		m.BlockNotifyMode,
		m.ZMQHealthStatus,
		m.ZMQLastMessageAge,
		m.WorkerHashrate,
		m.WorkerShares,
		m.WorkerConnected,
		m.WorkerDifficulty,
		m.ActiveWorkerCount,
		m.CircuitBreakerState,
		m.CircuitBreakerBlocked,
		m.CircuitBreakerTransitions,
		// V26: Status guard rejection metric
		m.BlockStatusGuardRejections,
		// V43: Aux block submission metrics
		m.AuxBlocksSubmitted,
		m.AuxBlocksFailed,
		// WAL metrics
		m.WALReplayCount,
		m.WALReplayDuration,
		m.WALWriteErrors,
		m.WALCommitErrors,
		m.WALSyncErrors,
		m.WALFileSize,
		// Backpressure metrics
		m.BackpressureLevel,
		m.BackpressureLevelChanges,
		m.BackpressureBufferFill,
		m.BackpressureDiffMultiplier,
		// HA cluster metrics
		m.HAClusterState,
		m.HANodeRole,
		m.HAFailoverTotal,
		m.HAElectionTotal,
		m.HAFlapDetected,
		m.HAPartitionDetected,
		m.HANodesTotal,
		m.HANodesHealthy,
		// DB HA metrics
		m.DBFailoverTotal,
		m.DBActiveNode,
		m.DBCircuitBreakerState,
		m.DBBlockQueueLen,
		// Redis HA metrics
		m.RedisHealthy,
		m.RedisReconnects,
		m.RedisFallbackActive,
		// Daemon health metrics
		m.DaemonBlockHeight,
		m.DaemonPeerCount,
		m.DaemonChainTipStaleSec,
		// Block maturity metrics
		m.BlocksPendingMaturityCount,
		m.BlocksOldestPendingAgeSec,
		// Sentinel alerting metrics
		m.SentinelAlertsTotal,
		m.SentinelAlertsFiring,
		m.SentinelCheckDuration,
		m.SentinelWebhookErrors,
		m.SentinelWALPendingAge,
		m.SentinelNodesDown,
		m.SentinelHashrateChange,
		m.SentinelConnectionDrop,
	)

	return m
}

// Start begins serving the metrics endpoint.
func (m *Metrics) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// SECURITY: Wrap /metrics with authentication middleware if configured
	// This prevents unauthorized access to sensitive operational metrics
	metricsHandler := m.metricsAuthMiddleware(promhttp.Handler())
	mux.Handle("/metrics", metricsHandler)

	// V45 FIX: /health endpoint performs real subsystem checks instead of static "OK".
	// Load balancers and Kubernetes probes use this to route traffic away from
	// unhealthy instances. A static "OK" masks daemon/DB/ZMQ failures.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if m.healthCheck != nil {
			healthy, details := m.healthCheck()
			if !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("UNHEALTHY: " + details)) // #nosec G104
				return
			}
			_, _ = w.Write([]byte("OK: " + details)) // #nosec G104
			return
		}
		_, _ = w.Write([]byte("OK")) // #nosec G104
	})

	// V45 FIX: /ready endpoint for Kubernetes readiness probes.
	// Returns 503 if metrics server itself is degraded.
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if m.serverFailed.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("NOT READY: metrics server failed")) // #nosec G104
			return
		}
		_, _ = w.Write([]byte("READY")) // #nosec G104
	})

	m.server = &http.Server{
		Addr:         m.cfg.Listen,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Log security configuration
	if m.cfg.AuthToken != "" {
		m.logger.Infow("Metrics server starting with authentication",
			"address", m.cfg.Listen,
			"authEnabled", true,
			"ipWhitelist", len(m.cfg.AllowedIPs) > 0,
		)
	} else if len(m.cfg.AllowedIPs) > 0 {
		m.logger.Infow("Metrics server starting with IP whitelist",
			"address", m.cfg.Listen,
			"allowedIPs", m.cfg.AllowedIPs,
		)
	} else {
		m.logger.Warnw("SECURITY: Metrics server starting WITHOUT authentication - set SPIRAL_METRICS_TOKEN or configure allowedIPs",
			"address", m.cfg.Listen,
		)
	}

	go func() {
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// V44 FIX: Mark server as failed so /ready and health checks can report it.
			// Without this, a port conflict or TLS failure is silently swallowed and
			// all Prometheus-based alerting goes blind.
			m.serverFailed.Store(true)
			m.logger.Errorw("🚨 CRITICAL: Metrics server FAILED — Prometheus alerting is BLIND!",
				"error", err,
				"address", m.cfg.Listen,
				"impact", "All Prometheus-based monitoring and alerting is non-functional",
				"action", "Check if another process is using the metrics port, or fix TLS configuration",
			)
		}
	}()

	return nil
}

// metricsAuthMiddleware provides authentication for the /metrics endpoint.
// SECURITY: Protects sensitive operational metrics from unauthorized access.
// Supports: Bearer token authentication and IP whitelist.
func (m *Metrics) metricsAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check IP whitelist first (if configured)
		// SECURITY: Use RemoteAddr directly for whitelist checks — X-Forwarded-For is
		// attacker-controlled and must not be trusted for security decisions.
		if len(m.cfg.AllowedIPs) > 0 {
			remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
			if remoteIP == "" {
				remoteIP = r.RemoteAddr
			}
			if !m.isIPAllowed(remoteIP) {
				m.logger.Warnw("SECURITY: Metrics access denied - IP not in whitelist",
					"clientIP", remoteIP,
					"path", r.URL.Path,
				)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			// IP is whitelisted - allow access without token
			next.ServeHTTP(w, r)
			return
		}

		// Check Bearer token authentication (if configured)
		if m.cfg.AuthToken != "" {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				m.logger.Debugw("Metrics access denied - no Authorization header",
					"clientIP", m.extractClientIP(r),
				)
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Extract Bearer token
			const bearerPrefix = "Bearer "
			if !strings.HasPrefix(authHeader, bearerPrefix) {
				http.Error(w, "Invalid authorization format", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(authHeader, bearerPrefix)

			// Constant-time comparison to prevent timing attacks
			if subtle.ConstantTimeCompare([]byte(token), []byte(m.cfg.AuthToken)) != 1 {
				m.logger.Warnw("SECURITY: Metrics access denied - invalid token",
					"clientIP", m.extractClientIP(r),
				)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		// Authenticated (or no auth configured)
		next.ServeHTTP(w, r)
	})
}

// extractClientIP extracts the real client IP from the request.
func (m *Metrics) extractClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for proxied requests)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the chain
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isIPAllowed checks if the given IP is in the allowed list.
func (m *Metrics) isIPAllowed(clientIP string) bool {
	parsedIP := net.ParseIP(clientIP)
	if parsedIP == nil {
		return false
	}

	for _, allowed := range m.cfg.AllowedIPs {
		// Check if it's a CIDR range
		if strings.Contains(allowed, "/") {
			_, network, err := net.ParseCIDR(allowed)
			if err == nil && network.Contains(parsedIP) {
				return true
			}
		} else {
			// Direct IP comparison
			allowedIP := net.ParseIP(allowed)
			if allowedIP != nil && allowedIP.Equal(parsedIP) {
				return true
			}
		}
	}
	return false
}

// Stop gracefully shuts down the metrics server.
func (m *Metrics) Stop() error {
	if m.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.server.Shutdown(ctx)
	}
	return nil
}

// Helper methods for common metric updates

// RecordConnection increments connection counters.
func (m *Metrics) RecordConnection() {
	m.ConnectionsTotal.Inc()
	m.ConnectionsActive.Inc()
	m.activeConns.Add(1)
}

// RecordDisconnection decrements active connections.
func (m *Metrics) RecordDisconnection() {
	m.ConnectionsActive.Dec()
	m.activeConns.Add(-1)
}

// RecordShare records a share submission.
func (m *Metrics) RecordShare(accepted bool, rejectReason string) {
	m.SharesSubmitted.Inc()
	if accepted {
		m.SharesAccepted.Inc()
	} else {
		m.SharesRejected.WithLabelValues(rejectReason).Inc()
		if rejectReason == "stale" {
			m.SharesStale.Inc()
		}
	}
}

// RecordBlock records a block find.
func (m *Metrics) RecordBlock() {
	m.BlocksFound.Inc()
}

// RecordBlockSubmissionFailed records a failed block submission (after all retries).
// CRITICAL: Any non-zero value indicates potential block loss - alert immediately.
func (m *Metrics) RecordBlockSubmissionFailed() {
	m.BlocksSubmissionFailed.Inc()
}

// AUDIT FIX (ISSUE-4): Per-coin block metric helpers for V2 multi-coin observability.
// These increment both the aggregate counter and the per-coin CounterVec.

// RecordBlockForCoin records a block found for a specific coin.
func (m *Metrics) RecordBlockForCoin(coin string) {
	m.BlocksFound.Inc()
	m.BlocksFoundByCoin.WithLabelValues(coin).Inc()
}

// RecordBlockSubmittedForCoin records a block submitted for a specific coin.
func (m *Metrics) RecordBlockSubmittedForCoin(coin string) {
	m.BlocksSubmitted.Inc()
	m.BlocksSubmittedByCoin.WithLabelValues(coin).Inc()
}

// RecordBlockOrphanedForCoin records a block orphaned for a specific coin.
func (m *Metrics) RecordBlockOrphanedForCoin(coin string) {
	m.BlocksOrphaned.Inc()
	m.BlocksOrphanedByCoin.WithLabelValues(coin).Inc()
}

// RecordBlockConfirmedForCoin records a block confirmed for a specific coin.
func (m *Metrics) RecordBlockConfirmedForCoin(coin string) {
	m.BlocksConfirmed.Inc()
	m.BlocksConfirmedByCoin.WithLabelValues(coin).Inc()
}

// RecordBlockSubmissionFailedForCoin records a failed block submission for a specific coin.
func (m *Metrics) RecordBlockSubmissionFailedForCoin(coin string) {
	m.BlocksSubmissionFailed.Inc()
	m.BlocksSubmissionFailedByCoin.WithLabelValues(coin).Inc()
}

// RecordShareValidation records share validation latency.
func (m *Metrics) RecordShareValidation(duration time.Duration) {
	m.ShareValidationLatency.Observe(duration.Seconds())
}

// RecordJobBroadcast records job broadcast latency.
func (m *Metrics) RecordJobBroadcast(duration time.Duration) {
	m.JobBroadcastLatency.Observe(duration.Seconds())
}

// RecordDBWrite records database write latency.
func (m *Metrics) RecordDBWrite(duration time.Duration) {
	m.DBWriteLatency.Observe(duration.Seconds())
}

// RecordBatchDrop records a dropped share batch (HA failover observability).
// This is called when a batch permanently fails after all retry attempts.
// CRITICAL: Any non-zero value indicates potential share loss - alert immediately.
func (m *Metrics) RecordBatchDrop(sharesInBatch int) {
	m.ShareBatchesDropped.Inc()
	m.SharesInDroppedBatch.Add(float64(sharesInBatch))
}

// RecordBatchRetry records a batch retry attempt.
// High retry rates indicate database connectivity issues (often during failover).
func (m *Metrics) RecordBatchRetry() {
	m.ShareBatchRetries.Inc()
}

// UpdateShareLossRate updates the share loss rate metric.
// Called periodically to calculate: shares_lost / shares_processed
func (m *Metrics) UpdateShareLossRate(processed, lost uint64) {
	if processed == 0 {
		m.ShareBatchLossRate.Set(0)
		return
	}
	rate := float64(lost) / float64(processed)
	m.ShareBatchLossRate.Set(rate)
}

// RecordRPCCall records RPC call latency.
func (m *Metrics) RecordRPCCall(duration time.Duration) {
	m.RPCCallLatency.Observe(duration.Seconds())
}

// UpdateHashrate updates the pool hashrate gauge.
func (m *Metrics) UpdateHashrate(hashrate float64) {
	m.PoolHashrate.Set(hashrate)
}

// UpdateNetworkInfo updates network-related metrics.
func (m *Metrics) UpdateNetworkInfo(difficulty, hashrate float64) {
	m.NetworkDifficulty.Set(difficulty)
	m.NetworkHashrate.Set(hashrate)
}

// IncJobsDispatched increments the jobs dispatched counter.
func (m *Metrics) IncJobsDispatched() {
	m.JobsDispatched.Inc()
}

// UpdateAvgShareDiff updates the running average share difficulty gauge.
func (m *Metrics) UpdateAvgShareDiff(avgDiff float64) {
	m.AvgShareDiff.Set(avgDiff)
}

// UpdateBestShareDiff updates the best share difficulty if the new value is higher.
func (m *Metrics) UpdateBestShareDiff(difficulty float64) {
	// Use compare-and-swap loop to atomically update if new value is higher
	for {
		oldBits := m.bestShareDiff.Load()
		oldDiff := math.Float64frombits(oldBits)
		if difficulty <= oldDiff {
			return // Current value is already higher or equal
		}
		newBits := math.Float64bits(difficulty)
		if m.bestShareDiff.CompareAndSwap(oldBits, newBits) {
			m.BestShareDiff.Set(difficulty)
			return
		}
		// CAS failed, retry
	}
}

// GetActiveConnections returns the current active connection count.
func (m *Metrics) GetActiveConnections() int64 {
	return m.activeConns.Load()
}

// SetZMQConnected sets the ZMQ connection status.
func (m *Metrics) SetZMQConnected(connected bool) {
	if connected {
		m.ZMQConnected.Set(1)
	} else {
		m.ZMQConnected.Set(0)
	}
}

// RecordZMQMessage increments the ZMQ message counter.
func (m *Metrics) RecordZMQMessage() {
	m.ZMQMessagesReceived.Inc()
}

// RecordZMQReconnect increments the ZMQ reconnect counter.
func (m *Metrics) RecordZMQReconnect() {
	m.ZMQReconnects.Inc()
}

// SetBlockNotifyMode sets the block notification mode (1=ZMQ, 0=polling).
func (m *Metrics) SetBlockNotifyMode(useZMQ bool) {
	if useZMQ {
		m.BlockNotifyMode.Set(1)
	} else {
		m.BlockNotifyMode.Set(0)
	}
}

// SetZMQHealthStatus updates the ZMQ health status metric.
// Status values: 0=disabled, 1=connecting, 2=healthy, 3=degraded, 4=failed
// SECURITY: This enables alerting on silent ZMQ disconnection.
func (m *Metrics) SetZMQHealthStatus(status int) {
	m.ZMQHealthStatus.Set(float64(status))
}

// SetZMQLastMessageAge updates the seconds since last ZMQ message.
// SECURITY: High values (> 120s) indicate ZMQ may be disconnected silently.
func (m *Metrics) SetZMQLastMessageAge(seconds float64) {
	m.ZMQLastMessageAge.Set(seconds)
}

// Worker metric helpers

// RecordWorkerConnection records a worker connecting.
func (m *Metrics) RecordWorkerConnection(miner, worker string) {
	m.WorkerConnected.WithLabelValues(miner, worker).Set(1)
	m.activeWorkers.Add(1)
	m.ActiveWorkerCount.Set(float64(m.activeWorkers.Load()))
}

// RecordWorkerDisconnection records a worker disconnecting.
func (m *Metrics) RecordWorkerDisconnection(miner, worker string) {
	m.WorkerConnected.WithLabelValues(miner, worker).Set(0)
	m.activeWorkers.Add(-1)
	m.ActiveWorkerCount.Set(float64(m.activeWorkers.Load()))
}

// UpdateWorkerHashrate updates a worker's hashrate.
func (m *Metrics) UpdateWorkerHashrate(miner, worker string, hashrate float64) {
	m.WorkerHashrate.WithLabelValues(miner, worker).Set(hashrate)
}

// UpdateWorkerDifficulty updates a worker's difficulty.
func (m *Metrics) UpdateWorkerDifficulty(miner, worker string, difficulty float64) {
	m.WorkerDifficulty.WithLabelValues(miner, worker).Set(difficulty)
}

// RecordWorkerShare records a share for a specific worker.
func (m *Metrics) RecordWorkerShare(miner, worker string, accepted bool, stale bool) {
	status := "accepted"
	if !accepted {
		if stale {
			status = "stale"
		} else {
			status = "rejected"
		}
	}
	m.WorkerShares.WithLabelValues(miner, worker, status).Inc()
}

// GetActiveWorkerCount returns the current active worker count.
func (m *Metrics) GetActiveWorkerCount() int64 {
	return m.activeWorkers.Load()
}

// Circuit breaker metric helpers

// SetCircuitBreakerState updates the circuit breaker state metric.
// State values: 0=closed, 1=open, 2=half-open
// SECURITY: Alert when > 0 - indicates database connectivity issues.
func (m *Metrics) SetCircuitBreakerState(state int) {
	m.CircuitBreakerState.Set(float64(state))
}

// RecordCircuitBreakerBlocked increments the blocked batch counter.
func (m *Metrics) RecordCircuitBreakerBlocked() {
	m.CircuitBreakerBlocked.Inc()
}

// RecordCircuitBreakerTransition increments the state transition counter.
func (m *Metrics) RecordCircuitBreakerTransition() {
	m.CircuitBreakerTransitions.Inc()
}

// WAL metric helpers

// RecordWALReplay records shares replayed from WAL on startup.
// Called once during pipeline startup after WAL replay completes.
// count: number of shares replayed
// duration: time taken to replay in seconds
func (m *Metrics) RecordWALReplay(count int, duration float64) {
	m.WALReplayCount.Add(float64(count))
	m.WALReplayDuration.Set(duration)
}

// RecordWALWriteError increments the WAL write error counter.
// Called when a share fails to write to WAL (share at risk of loss).
func (m *Metrics) RecordWALWriteError() {
	m.WALWriteErrors.Inc()
}

// RecordWALCommitError increments the WAL commit error counter.
// Called when batch commit to WAL fails (shares may replay on restart).
func (m *Metrics) RecordWALCommitError() {
	m.WALCommitErrors.Inc()
}

// RecordWALSyncError increments the WAL sync error counter.
// Called when fsync fails after WAL write/commit.
func (m *Metrics) RecordWALSyncError() {
	m.WALSyncErrors.Inc()
}

// SetWALFileSize updates the current WAL file size metric.
// Called periodically to track WAL growth.
func (m *Metrics) SetWALFileSize(sizeBytes int64) {
	m.WALFileSize.Set(float64(sizeBytes))
}

// Backpressure metric helpers

// SetBackpressureLevel updates the current backpressure level.
// level: 0=none, 1=warn, 2=critical, 3=emergency
func (m *Metrics) SetBackpressureLevel(level int) {
	m.BackpressureLevel.Set(float64(level))
}

// RecordBackpressureLevelChange increments the level change counter.
// Called each time the backpressure level transitions.
func (m *Metrics) RecordBackpressureLevelChange() {
	m.BackpressureLevelChanges.Inc()
}

// SetBackpressureBufferFill updates the buffer fill percentage.
// percent: 0-100 representing buffer utilization
func (m *Metrics) SetBackpressureBufferFill(percent float64) {
	m.BackpressureBufferFill.Set(percent)
}

// SetBackpressureDiffMultiplier updates the suggested difficulty multiplier.
// multiplier: 1.0=normal, >1.0=increase difficulty to reduce share rate
func (m *Metrics) SetBackpressureDiffMultiplier(multiplier float64) {
	m.BackpressureDiffMultiplier.Set(multiplier)
}

// SetHealthCheck registers a callback for the /health endpoint.
// V45 FIX: The callback should check daemon RPC, database, and ZMQ status.
func (m *Metrics) SetHealthCheck(check func() (healthy bool, details string)) {
	m.healthCheck = check
}

// IsServerFailed returns true if the metrics HTTP server failed to start.
// V44 FIX: Pool can check this to degrade its own health status.
func (m *Metrics) IsServerFailed() bool {
	return m.serverFailed.Load()
}

// RecordAuxBlockSubmitted increments the aux block submission counter for a coin.
// V43 FIX: Enables alerting on aux chain revenue loss.
func (m *Metrics) RecordAuxBlockSubmitted(coin string) {
	m.AuxBlocksSubmitted.WithLabelValues(coin).Inc()
}

// RecordAuxBlockFailed increments the aux block failure counter for a coin.
// V43 FIX: Non-zero rate means aux chain blocks are being lost.
func (m *Metrics) RecordAuxBlockFailed(coin, reason string) {
	m.AuxBlocksFailed.WithLabelValues(coin, reason).Inc()
}

// UpdateBackpressureMetrics updates all backpressure metrics at once.
// Convenience method for pipeline to report backpressure state.
func (m *Metrics) UpdateBackpressureMetrics(level int, bufferFillPercent, diffMultiplier float64, levelChanged bool) {
	m.BackpressureLevel.Set(float64(level))
	m.BackpressureBufferFill.Set(bufferFillPercent)
	m.BackpressureDiffMultiplier.Set(diffMultiplier)
	if levelChanged {
		m.BackpressureLevelChanges.Inc()
	}
}

// HA metric helpers

// SetHAClusterState sets the current HA cluster state.
// Resets all state labels to 0 and sets the active one to 1.
func (m *Metrics) SetHAClusterState(state string) {
	for _, s := range []string{"running", "election", "failover", "degraded"} {
		m.HAClusterState.WithLabelValues(s).Set(0)
	}
	m.HAClusterState.WithLabelValues(state).Set(1)
}

// SetHANodeRole sets the current HA node role.
// Resets all role labels to 0 and sets the active one to 1.
func (m *Metrics) SetHANodeRole(role string) {
	for _, r := range []string{"master", "backup", "observer"} {
		m.HANodeRole.WithLabelValues(r).Set(0)
	}
	m.HANodeRole.WithLabelValues(role).Set(1)
}

// IncHAFailover increments the HA failover counter.
func (m *Metrics) IncHAFailover() {
	m.HAFailoverTotal.Inc()
}

// IncHAElection increments the HA election counter.
func (m *Metrics) IncHAElection() {
	m.HAElectionTotal.Inc()
}

// IncHAFlapDetected increments the VIP flap detection counter.
func (m *Metrics) IncHAFlapDetected() {
	m.HAFlapDetected.Inc()
}

// IncHAPartitionDetected increments the network partition detection counter.
func (m *Metrics) IncHAPartitionDetected() {
	m.HAPartitionDetected.Inc()
}

// SetHANodesTotal sets the total number of nodes in the HA cluster.
func (m *Metrics) SetHANodesTotal(count int) {
	m.HANodesTotal.Set(float64(count))
}

// SetHANodesHealthy sets the number of healthy nodes in the HA cluster.
func (m *Metrics) SetHANodesHealthy(count int) {
	m.HANodesHealthy.Set(float64(count))
}

// IncDBFailover increments the database failover counter.
func (m *Metrics) IncDBFailover() {
	m.DBFailoverTotal.Inc()
}

// SetDBActiveNode sets the active database node.
// Resets all node labels to 0 and sets the active one to 1.
func (m *Metrics) SetDBActiveNode(nodeID string) {
	// Reset clears all previously registered label values,
	// ensuring only the active node shows 1 in Prometheus.
	m.DBActiveNode.Reset()
	m.DBActiveNode.WithLabelValues(nodeID).Set(1)
}

// SetDBBlockQueueLen sets the number of blocks queued during failover.
func (m *Metrics) SetDBBlockQueueLen(length int) {
	m.DBBlockQueueLen.Set(float64(length))
}

// SetRedisHealth sets the Redis health status.
func (m *Metrics) SetRedisHealth(healthy bool) {
	if healthy {
		m.RedisHealthy.Set(1)
		m.RedisFallbackActive.Set(0)
	} else {
		m.RedisHealthy.Set(0)
		m.RedisFallbackActive.Set(1)
	}
}

// IncRedisReconnects increments the Redis reconnect counter.
func (m *Metrics) IncRedisReconnects() {
	m.RedisReconnects.Inc()
}

// SetPaymentProcessorFailedCycles sets the consecutive failed payment cycle count.
// Alert when >= ConsecutiveFailureThreshold (5) — indicates silent processor death.
func (m *Metrics) SetPaymentProcessorFailedCycles(count int) {
	m.PaymentProcessorFailedCycles.Set(float64(count))
}

// RecordPaidBlockReorg increments the paid block reorg counter.
// CRITICAL: Any non-zero value means financial loss from deep reorg after payment.
func (m *Metrics) RecordPaidBlockReorg() {
	m.PaidBlockReorgs.Inc()
}

// RecordBlockConfirmed increments the confirmed block counter.
// AUDIT FIX (ISSUE-5): This metric was registered but never called.
func (m *Metrics) RecordBlockConfirmed() {
	m.BlocksConfirmed.Inc()
}

// RecordBlockOrphaned increments the orphaned block counter.
// AUDIT FIX (ISSUE-5): This metric was registered but never called.
func (m *Metrics) RecordBlockOrphaned() {
	m.BlocksOrphaned.Inc()
}

// SetBlocksPendingMaturityCount sets the number of blocks awaiting maturity.
// Used by Sentinel for block maturity stall detection.
func (m *Metrics) SetBlocksPendingMaturityCount(count int) {
	m.BlocksPendingMaturityCount.Set(float64(count))
}

// SetBlocksOldestPendingAgeSec sets the age of the oldest pending block in seconds.
// Used by Sentinel for block maturity stall detection.
func (m *Metrics) SetBlocksOldestPendingAgeSec(seconds float64) {
	m.BlocksOldestPendingAgeSec.Set(seconds)
}
