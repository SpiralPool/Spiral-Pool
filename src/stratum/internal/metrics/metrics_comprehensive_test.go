// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Comprehensive metrics tests covering all helper methods and the
// authentication middleware.
//
// Strategy:
//   - Tests that need exact value assertions create test-local Prometheus
//     metrics (not registered with the default registry). This avoids the
//     global-registry constraint that prevents calling New() more than once.
//   - Auth middleware tests create minimal Metrics instances with only cfg
//     and logger set — no Prometheus registration needed.
package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test-local Metrics factories
// ═══════════════════════════════════════════════════════════════════════════════

// newConnectionMetrics returns a Metrics with only connection-related fields.
func newConnectionMetrics() *Metrics {
	return &Metrics{
		ConnectionsTotal:  prometheus.NewCounter(prometheus.CounterOpts{Name: "t_conn_total"}),
		ConnectionsActive: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_conn_active"}),
	}
}

// newShareMetrics returns a Metrics with only share-related fields.
func newShareMetrics() *Metrics {
	return &Metrics{
		SharesSubmitted: prometheus.NewCounter(prometheus.CounterOpts{Name: "t_shares_submitted"}),
		SharesAccepted:  prometheus.NewCounter(prometheus.CounterOpts{Name: "t_shares_accepted"}),
		SharesRejected:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_shares_rejected"}, []string{"reason"}),
		SharesStale:     prometheus.NewCounter(prometheus.CounterOpts{Name: "t_shares_stale"}),
	}
}

// newBatchMetrics returns a Metrics with batch loss fields.
func newBatchMetrics() *Metrics {
	return &Metrics{
		ShareBatchesDropped:  prometheus.NewCounter(prometheus.CounterOpts{Name: "t_batch_dropped"}),
		SharesInDroppedBatch: prometheus.NewCounter(prometheus.CounterOpts{Name: "t_shares_in_dropped"}),
		ShareBatchRetries:    prometheus.NewCounter(prometheus.CounterOpts{Name: "t_batch_retries"}),
		ShareBatchLossRate:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_batch_loss_rate"}),
	}
}

// newBestShareMetrics returns a Metrics with BestShareDiff for CAS testing.
func newBestShareMetrics() *Metrics {
	return &Metrics{
		BestShareDiff: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_best_share_diff"}),
	}
}

// newBlockCoinMetrics returns a Metrics with per-coin block fields.
func newBlockCoinMetrics() *Metrics {
	return &Metrics{
		BlocksFound:                  prometheus.NewCounter(prometheus.CounterOpts{Name: "t_blocks_found"}),
		BlocksSubmitted:              prometheus.NewCounter(prometheus.CounterOpts{Name: "t_blocks_submitted"}),
		BlocksOrphaned:               prometheus.NewCounter(prometheus.CounterOpts{Name: "t_blocks_orphaned"}),
		BlocksConfirmed:              prometheus.NewCounter(prometheus.CounterOpts{Name: "t_blocks_confirmed"}),
		BlocksSubmissionFailed:       prometheus.NewCounter(prometheus.CounterOpts{Name: "t_blocks_failed"}),
		BlocksFoundByCoin:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_blocks_found_coin"}, []string{"coin"}),
		BlocksSubmittedByCoin:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_blocks_submitted_coin"}, []string{"coin"}),
		BlocksOrphanedByCoin:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_blocks_orphaned_coin"}, []string{"coin"}),
		BlocksConfirmedByCoin:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_blocks_confirmed_coin"}, []string{"coin"}),
		BlocksSubmissionFailedByCoin: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_blocks_failed_coin"}, []string{"coin"}),
	}
}

// newWorkerMetrics returns a Metrics with worker-related fields.
func newWorkerMetrics() *Metrics {
	return &Metrics{
		WorkerConnected:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "t_worker_connected"}, []string{"miner", "worker"}),
		WorkerHashrate:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "t_worker_hashrate"}, []string{"miner", "worker"}),
		WorkerDifficulty:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "t_worker_difficulty"}, []string{"miner", "worker"}),
		WorkerShares:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "t_worker_shares"}, []string{"miner", "worker", "status"}),
		ActiveWorkerCount: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_active_workers"}),
	}
}

// newHAMetrics returns a Metrics with HA cluster fields.
func newHAMetrics() *Metrics {
	return &Metrics{
		HAClusterState:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "t_ha_cluster_state"}, []string{"state"}),
		HANodeRole:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "t_ha_node_role"}, []string{"role"}),
		HAFailoverTotal:     prometheus.NewCounter(prometheus.CounterOpts{Name: "t_ha_failover"}),
		HAElectionTotal:     prometheus.NewCounter(prometheus.CounterOpts{Name: "t_ha_election"}),
		HAFlapDetected:      prometheus.NewCounter(prometheus.CounterOpts{Name: "t_ha_flap"}),
		HAPartitionDetected: prometheus.NewCounter(prometheus.CounterOpts{Name: "t_ha_partition"}),
		HANodesTotal:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_ha_nodes_total"}),
		HANodesHealthy:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_ha_nodes_healthy"}),
	}
}

// newRedisMetrics returns a Metrics with Redis fields.
func newRedisMetrics() *Metrics {
	return &Metrics{
		RedisHealthy:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_redis_healthy"}),
		RedisReconnects:     prometheus.NewCounter(prometheus.CounterOpts{Name: "t_redis_reconnects"}),
		RedisFallbackActive: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_redis_fallback"}),
	}
}

// newBackpressureMetrics returns a Metrics with backpressure fields.
func newBackpressureMetrics() *Metrics {
	return &Metrics{
		BackpressureLevel:          prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_bp_level"}),
		BackpressureLevelChanges:   prometheus.NewCounter(prometheus.CounterOpts{Name: "t_bp_changes"}),
		BackpressureBufferFill:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_bp_fill"}),
		BackpressureDiffMultiplier: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_bp_mult"}),
	}
}

// newZMQMetrics returns a Metrics with ZMQ fields.
func newZMQMetrics() *Metrics {
	return &Metrics{
		ZMQConnected:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_zmq_connected"}),
		ZMQMessagesReceived: prometheus.NewCounter(prometheus.CounterOpts{Name: "t_zmq_messages"}),
		ZMQReconnects:       prometheus.NewCounter(prometheus.CounterOpts{Name: "t_zmq_reconnects"}),
		BlockNotifyMode:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_block_notify_mode"}),
		ZMQHealthStatus:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_zmq_health"}),
		ZMQLastMessageAge:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_zmq_last_age"}),
	}
}

// newAuthMetrics creates a minimal Metrics with only cfg and logger
// for auth middleware tests. No Prometheus registration occurs.
func newAuthMetrics(token string, allowedIPs []string) *Metrics {
	return &Metrics{
		cfg: &config.MetricsConfig{
			AuthToken:  token,
			AllowedIPs: allowedIPs,
		},
		logger: zap.NewNop().Sugar(),
	}
}

// testOKHandler is a simple handler that writes 200 OK.
var testOKHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// ═══════════════════════════════════════════════════════════════════════════════
// Connection metrics tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordConnection_IncrementsAll(t *testing.T) {
	t.Parallel()
	m := newConnectionMetrics()

	m.RecordConnection()
	m.RecordConnection()
	m.RecordConnection()

	if v := testutil.ToFloat64(m.ConnectionsTotal); v != 3 {
		t.Errorf("ConnectionsTotal = %v, want 3", v)
	}
	if v := testutil.ToFloat64(m.ConnectionsActive); v != 3 {
		t.Errorf("ConnectionsActive = %v, want 3", v)
	}
	if v := m.GetActiveConnections(); v != 3 {
		t.Errorf("GetActiveConnections() = %d, want 3", v)
	}
}

func TestRecordDisconnection_Decrements(t *testing.T) {
	t.Parallel()
	m := newConnectionMetrics()

	m.RecordConnection()
	m.RecordConnection()
	m.RecordDisconnection()

	if v := testutil.ToFloat64(m.ConnectionsActive); v != 1 {
		t.Errorf("ConnectionsActive = %v, want 1 (2 connected, 1 disconnected)", v)
	}
	if v := m.GetActiveConnections(); v != 1 {
		t.Errorf("GetActiveConnections() = %d, want 1", v)
	}
	// Total should still be 2 (connection total is never decremented)
	if v := testutil.ToFloat64(m.ConnectionsTotal); v != 2 {
		t.Errorf("ConnectionsTotal = %v, want 2 (total never decrements)", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Share metrics tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordShare_Accepted(t *testing.T) {
	t.Parallel()
	m := newShareMetrics()

	m.RecordShare(true, "")

	if v := testutil.ToFloat64(m.SharesSubmitted); v != 1 {
		t.Errorf("SharesSubmitted = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesAccepted); v != 1 {
		t.Errorf("SharesAccepted = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesStale); v != 0 {
		t.Errorf("SharesStale = %v, want 0", v)
	}
}

func TestRecordShare_RejectedNonStale(t *testing.T) {
	t.Parallel()
	m := newShareMetrics()

	m.RecordShare(false, "low_difficulty")

	if v := testutil.ToFloat64(m.SharesSubmitted); v != 1 {
		t.Errorf("SharesSubmitted = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesAccepted); v != 0 {
		t.Errorf("SharesAccepted = %v, want 0 (rejected share)", v)
	}
	if v := testutil.ToFloat64(m.SharesRejected.WithLabelValues("low_difficulty")); v != 1 {
		t.Errorf("SharesRejected[low_difficulty] = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesStale); v != 0 {
		t.Errorf("SharesStale = %v, want 0 (not stale)", v)
	}
}

func TestRecordShare_RejectedStale(t *testing.T) {
	t.Parallel()
	m := newShareMetrics()

	m.RecordShare(false, "stale")

	if v := testutil.ToFloat64(m.SharesSubmitted); v != 1 {
		t.Errorf("SharesSubmitted = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesRejected.WithLabelValues("stale")); v != 1 {
		t.Errorf("SharesRejected[stale] = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesStale); v != 1 {
		t.Errorf("SharesStale = %v, want 1 (stale shares counted separately)", v)
	}
}

func TestRecordShare_MultipleReasons(t *testing.T) {
	t.Parallel()
	m := newShareMetrics()

	m.RecordShare(false, "stale")
	m.RecordShare(false, "stale")
	m.RecordShare(false, "low_difficulty")
	m.RecordShare(false, "duplicate")
	m.RecordShare(true, "")

	if v := testutil.ToFloat64(m.SharesSubmitted); v != 5 {
		t.Errorf("SharesSubmitted = %v, want 5", v)
	}
	if v := testutil.ToFloat64(m.SharesAccepted); v != 1 {
		t.Errorf("SharesAccepted = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesRejected.WithLabelValues("stale")); v != 2 {
		t.Errorf("SharesRejected[stale] = %v, want 2", v)
	}
	if v := testutil.ToFloat64(m.SharesRejected.WithLabelValues("low_difficulty")); v != 1 {
		t.Errorf("SharesRejected[low_difficulty] = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesRejected.WithLabelValues("duplicate")); v != 1 {
		t.Errorf("SharesRejected[duplicate] = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SharesStale); v != 2 {
		t.Errorf("SharesStale = %v, want 2", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Batch drop and loss rate tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordBatchDrop(t *testing.T) {
	t.Parallel()
	m := newBatchMetrics()

	m.RecordBatchDrop(50)
	m.RecordBatchDrop(25)

	if v := testutil.ToFloat64(m.ShareBatchesDropped); v != 2 {
		t.Errorf("ShareBatchesDropped = %v, want 2", v)
	}
	if v := testutil.ToFloat64(m.SharesInDroppedBatch); v != 75 {
		t.Errorf("SharesInDroppedBatch = %v, want 75 (50+25)", v)
	}
}

func TestRecordBatchRetry(t *testing.T) {
	t.Parallel()
	m := newBatchMetrics()

	m.RecordBatchRetry()
	m.RecordBatchRetry()
	m.RecordBatchRetry()

	if v := testutil.ToFloat64(m.ShareBatchRetries); v != 3 {
		t.Errorf("ShareBatchRetries = %v, want 3", v)
	}
}

func TestUpdateShareLossRate_Normal(t *testing.T) {
	t.Parallel()
	m := newBatchMetrics()

	m.UpdateShareLossRate(1000, 5)

	if v := testutil.ToFloat64(m.ShareBatchLossRate); v != 0.005 {
		t.Errorf("ShareBatchLossRate = %v, want 0.005", v)
	}
}

func TestUpdateShareLossRate_ZeroProcessed(t *testing.T) {
	t.Parallel()
	m := newBatchMetrics()

	// Should not divide by zero
	m.UpdateShareLossRate(0, 0)

	if v := testutil.ToFloat64(m.ShareBatchLossRate); v != 0 {
		t.Errorf("ShareBatchLossRate = %v, want 0 (zero-division protection)", v)
	}
}

func TestUpdateShareLossRate_NoLoss(t *testing.T) {
	t.Parallel()
	m := newBatchMetrics()

	m.UpdateShareLossRate(10000, 0)

	if v := testutil.ToFloat64(m.ShareBatchLossRate); v != 0 {
		t.Errorf("ShareBatchLossRate = %v, want 0", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// UpdateBestShareDiff (atomic CAS loop)
// ═══════════════════════════════════════════════════════════════════════════════

func TestUpdateBestShareDiff_HigherUpdates(t *testing.T) {
	t.Parallel()
	m := newBestShareMetrics()

	m.UpdateBestShareDiff(100.0)
	if v := testutil.ToFloat64(m.BestShareDiff); v != 100.0 {
		t.Errorf("BestShareDiff = %v, want 100.0", v)
	}

	m.UpdateBestShareDiff(200.0)
	if v := testutil.ToFloat64(m.BestShareDiff); v != 200.0 {
		t.Errorf("BestShareDiff = %v, want 200.0", v)
	}
}

func TestUpdateBestShareDiff_LowerIgnored(t *testing.T) {
	t.Parallel()
	m := newBestShareMetrics()

	m.UpdateBestShareDiff(500.0)
	m.UpdateBestShareDiff(100.0) // Should be ignored

	if v := testutil.ToFloat64(m.BestShareDiff); v != 500.0 {
		t.Errorf("BestShareDiff = %v, want 500.0 (lower value should be ignored)", v)
	}
}

func TestUpdateBestShareDiff_EqualIgnored(t *testing.T) {
	t.Parallel()
	m := newBestShareMetrics()

	m.UpdateBestShareDiff(300.0)
	m.UpdateBestShareDiff(300.0) // Equal should be ignored

	if v := testutil.ToFloat64(m.BestShareDiff); v != 300.0 {
		t.Errorf("BestShareDiff = %v, want 300.0", v)
	}
}

func TestUpdateBestShareDiff_Concurrent(t *testing.T) {
	t.Parallel()
	m := newBestShareMetrics()

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(val float64) {
			defer wg.Done()
			m.UpdateBestShareDiff(val)
		}(float64(i + 1))
	}
	wg.Wait()

	v := testutil.ToFloat64(m.BestShareDiff)
	if v != float64(goroutines) {
		t.Errorf("BestShareDiff = %v, want %v (highest value from concurrent updates)", v, float64(goroutines))
	}

	// Verify the atomic store matches the gauge
	storedBits := m.bestShareDiff.Load()
	storedDiff := math.Float64frombits(storedBits)
	if storedDiff != float64(goroutines) {
		t.Errorf("atomic bestShareDiff = %v, want %v", storedDiff, float64(goroutines))
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Per-coin block metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordBlockForCoin_IncrementsBothCounters(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockForCoin("DGB")
	m.RecordBlockForCoin("DGB")
	m.RecordBlockForCoin("BTC")

	// Aggregate counter
	if v := testutil.ToFloat64(m.BlocksFound); v != 3 {
		t.Errorf("BlocksFound = %v, want 3", v)
	}
	// Per-coin counters
	if v := testutil.ToFloat64(m.BlocksFoundByCoin.WithLabelValues("DGB")); v != 2 {
		t.Errorf("BlocksFoundByCoin[DGB] = %v, want 2", v)
	}
	if v := testutil.ToFloat64(m.BlocksFoundByCoin.WithLabelValues("BTC")); v != 1 {
		t.Errorf("BlocksFoundByCoin[BTC] = %v, want 1", v)
	}
}

func TestRecordBlockSubmittedForCoin(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockSubmittedForCoin("LTC")

	if v := testutil.ToFloat64(m.BlocksSubmitted); v != 1 {
		t.Errorf("BlocksSubmitted = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.BlocksSubmittedByCoin.WithLabelValues("LTC")); v != 1 {
		t.Errorf("BlocksSubmittedByCoin[LTC] = %v, want 1", v)
	}
}

func TestRecordBlockOrphanedForCoin(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockOrphanedForCoin("DOGE")

	if v := testutil.ToFloat64(m.BlocksOrphaned); v != 1 {
		t.Errorf("BlocksOrphaned = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.BlocksOrphanedByCoin.WithLabelValues("DOGE")); v != 1 {
		t.Errorf("BlocksOrphanedByCoin[DOGE] = %v, want 1", v)
	}
}

func TestRecordBlockConfirmedForCoin(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockConfirmedForCoin("BTC")

	if v := testutil.ToFloat64(m.BlocksConfirmed); v != 1 {
		t.Errorf("BlocksConfirmed = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.BlocksConfirmedByCoin.WithLabelValues("BTC")); v != 1 {
		t.Errorf("BlocksConfirmedByCoin[BTC] = %v, want 1", v)
	}
}

func TestRecordBlockSubmissionFailedForCoin(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockSubmissionFailedForCoin("NMC")

	if v := testutil.ToFloat64(m.BlocksSubmissionFailed); v != 1 {
		t.Errorf("BlocksSubmissionFailed = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.BlocksSubmissionFailedByCoin.WithLabelValues("NMC")); v != 1 {
		t.Errorf("BlocksSubmissionFailedByCoin[NMC] = %v, want 1", v)
	}
}

// Standalone block counters (not per-coin variants)
func TestRecordBlock_Standalone(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlock()
	m.RecordBlock()

	if v := testutil.ToFloat64(m.BlocksFound); v != 2 {
		t.Errorf("BlocksFound = %v, want 2", v)
	}
}

func TestRecordBlockConfirmed_Standalone(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockConfirmed()

	if v := testutil.ToFloat64(m.BlocksConfirmed); v != 1 {
		t.Errorf("BlocksConfirmed = %v, want 1", v)
	}
}

func TestRecordBlockOrphaned_Standalone(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockOrphaned()

	if v := testutil.ToFloat64(m.BlocksOrphaned); v != 1 {
		t.Errorf("BlocksOrphaned = %v, want 1", v)
	}
}

func TestRecordBlockSubmissionFailed_Standalone(t *testing.T) {
	t.Parallel()
	m := newBlockCoinMetrics()

	m.RecordBlockSubmissionFailed()

	if v := testutil.ToFloat64(m.BlocksSubmissionFailed); v != 1 {
		t.Errorf("BlocksSubmissionFailed = %v, want 1", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Worker metrics tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordWorkerConnection(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.RecordWorkerConnection("miner1", "rig1")
	m.RecordWorkerConnection("miner1", "rig2")
	m.RecordWorkerConnection("miner2", "rig1")

	if v := testutil.ToFloat64(m.WorkerConnected.WithLabelValues("miner1", "rig1")); v != 1 {
		t.Errorf("WorkerConnected[miner1,rig1] = %v, want 1", v)
	}
	if v := m.GetActiveWorkerCount(); v != 3 {
		t.Errorf("GetActiveWorkerCount() = %d, want 3", v)
	}
	if v := testutil.ToFloat64(m.ActiveWorkerCount); v != 3 {
		t.Errorf("ActiveWorkerCount gauge = %v, want 3", v)
	}
}

func TestRecordWorkerDisconnection(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.RecordWorkerConnection("miner1", "rig1")
	m.RecordWorkerConnection("miner1", "rig2")
	m.RecordWorkerDisconnection("miner1", "rig1")

	if v := testutil.ToFloat64(m.WorkerConnected.WithLabelValues("miner1", "rig1")); v != 0 {
		t.Errorf("WorkerConnected[miner1,rig1] = %v, want 0 (disconnected)", v)
	}
	if v := testutil.ToFloat64(m.WorkerConnected.WithLabelValues("miner1", "rig2")); v != 1 {
		t.Errorf("WorkerConnected[miner1,rig2] = %v, want 1 (still connected)", v)
	}
	if v := m.GetActiveWorkerCount(); v != 1 {
		t.Errorf("GetActiveWorkerCount() = %d, want 1", v)
	}
}

func TestRecordWorkerShare_Accepted(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.RecordWorkerShare("miner1", "rig1", true, false)

	if v := testutil.ToFloat64(m.WorkerShares.WithLabelValues("miner1", "rig1", "accepted")); v != 1 {
		t.Errorf("WorkerShares[accepted] = %v, want 1", v)
	}
}

func TestRecordWorkerShare_Rejected(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.RecordWorkerShare("miner1", "rig1", false, false)

	if v := testutil.ToFloat64(m.WorkerShares.WithLabelValues("miner1", "rig1", "rejected")); v != 1 {
		t.Errorf("WorkerShares[rejected] = %v, want 1", v)
	}
}

func TestRecordWorkerShare_Stale(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.RecordWorkerShare("miner1", "rig1", false, true)

	if v := testutil.ToFloat64(m.WorkerShares.WithLabelValues("miner1", "rig1", "stale")); v != 1 {
		t.Errorf("WorkerShares[stale] = %v, want 1", v)
	}
}

func TestUpdateWorkerHashrate(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.UpdateWorkerHashrate("miner1", "rig1", 1e12) // 1 TH/s

	if v := testutil.ToFloat64(m.WorkerHashrate.WithLabelValues("miner1", "rig1")); v != 1e12 {
		t.Errorf("WorkerHashrate = %v, want 1e12", v)
	}
}

func TestUpdateWorkerDifficulty(t *testing.T) {
	t.Parallel()
	m := newWorkerMetrics()

	m.UpdateWorkerDifficulty("miner1", "rig1", 16384)

	if v := testutil.ToFloat64(m.WorkerDifficulty.WithLabelValues("miner1", "rig1")); v != 16384 {
		t.Errorf("WorkerDifficulty = %v, want 16384", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// HA cluster metrics — label reset pattern
// ═══════════════════════════════════════════════════════════════════════════════

func TestSetHAClusterState_ResetsAllLabels(t *testing.T) {
	t.Parallel()
	m := newHAMetrics()

	// Set to "running" first
	m.SetHAClusterState("running")
	if v := testutil.ToFloat64(m.HAClusterState.WithLabelValues("running")); v != 1 {
		t.Errorf("HAClusterState[running] = %v, want 1", v)
	}

	// Switch to "failover" — "running" should reset to 0
	m.SetHAClusterState("failover")
	if v := testutil.ToFloat64(m.HAClusterState.WithLabelValues("running")); v != 0 {
		t.Errorf("HAClusterState[running] = %v, want 0 (should be reset)", v)
	}
	if v := testutil.ToFloat64(m.HAClusterState.WithLabelValues("failover")); v != 1 {
		t.Errorf("HAClusterState[failover] = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.HAClusterState.WithLabelValues("election")); v != 0 {
		t.Errorf("HAClusterState[election] = %v, want 0", v)
	}
	if v := testutil.ToFloat64(m.HAClusterState.WithLabelValues("degraded")); v != 0 {
		t.Errorf("HAClusterState[degraded] = %v, want 0", v)
	}
}

func TestSetHANodeRole_ResetsAllLabels(t *testing.T) {
	t.Parallel()
	m := newHAMetrics()

	m.SetHANodeRole("master")
	if v := testutil.ToFloat64(m.HANodeRole.WithLabelValues("master")); v != 1 {
		t.Errorf("HANodeRole[master] = %v, want 1", v)
	}

	m.SetHANodeRole("backup")
	if v := testutil.ToFloat64(m.HANodeRole.WithLabelValues("master")); v != 0 {
		t.Errorf("HANodeRole[master] = %v, want 0 (should be reset)", v)
	}
	if v := testutil.ToFloat64(m.HANodeRole.WithLabelValues("backup")); v != 1 {
		t.Errorf("HANodeRole[backup] = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.HANodeRole.WithLabelValues("observer")); v != 0 {
		t.Errorf("HANodeRole[observer] = %v, want 0", v)
	}
}

func TestHACounters(t *testing.T) {
	t.Parallel()
	m := newHAMetrics()

	m.IncHAFailover()
	m.IncHAElection()
	m.IncHAElection()
	m.IncHAFlapDetected()
	m.IncHAPartitionDetected()

	if v := testutil.ToFloat64(m.HAFailoverTotal); v != 1 {
		t.Errorf("HAFailoverTotal = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.HAElectionTotal); v != 2 {
		t.Errorf("HAElectionTotal = %v, want 2", v)
	}
	if v := testutil.ToFloat64(m.HAFlapDetected); v != 1 {
		t.Errorf("HAFlapDetected = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.HAPartitionDetected); v != 1 {
		t.Errorf("HAPartitionDetected = %v, want 1", v)
	}
}

func TestSetHANodesCount(t *testing.T) {
	t.Parallel()
	m := newHAMetrics()

	m.SetHANodesTotal(3)
	m.SetHANodesHealthy(2)

	if v := testutil.ToFloat64(m.HANodesTotal); v != 3 {
		t.Errorf("HANodesTotal = %v, want 3", v)
	}
	if v := testutil.ToFloat64(m.HANodesHealthy); v != 2 {
		t.Errorf("HANodesHealthy = %v, want 2", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Redis metrics — dual-metric update
// ═══════════════════════════════════════════════════════════════════════════════

func TestSetRedisHealth_Healthy(t *testing.T) {
	t.Parallel()
	m := newRedisMetrics()

	m.SetRedisHealth(true)

	if v := testutil.ToFloat64(m.RedisHealthy); v != 1 {
		t.Errorf("RedisHealthy = %v, want 1 (healthy)", v)
	}
	if v := testutil.ToFloat64(m.RedisFallbackActive); v != 0 {
		t.Errorf("RedisFallbackActive = %v, want 0 (not in fallback)", v)
	}
}

func TestSetRedisHealth_Unhealthy(t *testing.T) {
	t.Parallel()
	m := newRedisMetrics()

	m.SetRedisHealth(false)

	if v := testutil.ToFloat64(m.RedisHealthy); v != 0 {
		t.Errorf("RedisHealthy = %v, want 0 (unhealthy)", v)
	}
	if v := testutil.ToFloat64(m.RedisFallbackActive); v != 1 {
		t.Errorf("RedisFallbackActive = %v, want 1 (fallback active)", v)
	}
}

func TestSetRedisHealth_Toggle(t *testing.T) {
	t.Parallel()
	m := newRedisMetrics()

	m.SetRedisHealth(true)
	m.SetRedisHealth(false)
	m.SetRedisHealth(true)

	if v := testutil.ToFloat64(m.RedisHealthy); v != 1 {
		t.Errorf("RedisHealthy = %v, want 1 after toggle back to healthy", v)
	}
	if v := testutil.ToFloat64(m.RedisFallbackActive); v != 0 {
		t.Errorf("RedisFallbackActive = %v, want 0 after toggle back to healthy", v)
	}
}

func TestIncRedisReconnects(t *testing.T) {
	t.Parallel()
	m := newRedisMetrics()

	m.IncRedisReconnects()
	m.IncRedisReconnects()

	if v := testutil.ToFloat64(m.RedisReconnects); v != 2 {
		t.Errorf("RedisReconnects = %v, want 2", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Backpressure metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestUpdateBackpressureMetrics_WithLevelChange(t *testing.T) {
	t.Parallel()
	m := newBackpressureMetrics()

	m.UpdateBackpressureMetrics(2, 85.5, 1.5, true)

	if v := testutil.ToFloat64(m.BackpressureLevel); v != 2 {
		t.Errorf("BackpressureLevel = %v, want 2", v)
	}
	if v := testutil.ToFloat64(m.BackpressureBufferFill); v != 85.5 {
		t.Errorf("BackpressureBufferFill = %v, want 85.5", v)
	}
	if v := testutil.ToFloat64(m.BackpressureDiffMultiplier); v != 1.5 {
		t.Errorf("BackpressureDiffMultiplier = %v, want 1.5", v)
	}
	if v := testutil.ToFloat64(m.BackpressureLevelChanges); v != 1 {
		t.Errorf("BackpressureLevelChanges = %v, want 1 (level changed)", v)
	}
}

func TestUpdateBackpressureMetrics_WithoutLevelChange(t *testing.T) {
	t.Parallel()
	m := newBackpressureMetrics()

	m.UpdateBackpressureMetrics(1, 72.0, 1.2, false)

	if v := testutil.ToFloat64(m.BackpressureLevel); v != 1 {
		t.Errorf("BackpressureLevel = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.BackpressureLevelChanges); v != 0 {
		t.Errorf("BackpressureLevelChanges = %v, want 0 (level NOT changed)", v)
	}
}

func TestSetBackpressureIndividual(t *testing.T) {
	t.Parallel()
	m := newBackpressureMetrics()

	m.SetBackpressureLevel(3)
	m.RecordBackpressureLevelChange()
	m.SetBackpressureBufferFill(98.0)
	m.SetBackpressureDiffMultiplier(4.0)

	if v := testutil.ToFloat64(m.BackpressureLevel); v != 3 {
		t.Errorf("BackpressureLevel = %v, want 3", v)
	}
	if v := testutil.ToFloat64(m.BackpressureLevelChanges); v != 1 {
		t.Errorf("BackpressureLevelChanges = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.BackpressureBufferFill); v != 98.0 {
		t.Errorf("BackpressureBufferFill = %v, want 98.0", v)
	}
	if v := testutil.ToFloat64(m.BackpressureDiffMultiplier); v != 4.0 {
		t.Errorf("BackpressureDiffMultiplier = %v, want 4.0", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ZMQ metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestSetZMQConnected_True(t *testing.T) {
	t.Parallel()
	m := newZMQMetrics()

	m.SetZMQConnected(true)

	if v := testutil.ToFloat64(m.ZMQConnected); v != 1 {
		t.Errorf("ZMQConnected = %v, want 1", v)
	}
}

func TestSetZMQConnected_False(t *testing.T) {
	t.Parallel()
	m := newZMQMetrics()

	m.SetZMQConnected(true)
	m.SetZMQConnected(false)

	if v := testutil.ToFloat64(m.ZMQConnected); v != 0 {
		t.Errorf("ZMQConnected = %v, want 0", v)
	}
}

func TestSetBlockNotifyMode(t *testing.T) {
	t.Parallel()
	m := newZMQMetrics()

	m.SetBlockNotifyMode(true)
	if v := testutil.ToFloat64(m.BlockNotifyMode); v != 1 {
		t.Errorf("BlockNotifyMode = %v, want 1 (ZMQ)", v)
	}

	m.SetBlockNotifyMode(false)
	if v := testutil.ToFloat64(m.BlockNotifyMode); v != 0 {
		t.Errorf("BlockNotifyMode = %v, want 0 (polling)", v)
	}
}

func TestSetZMQHealthStatus(t *testing.T) {
	t.Parallel()
	m := newZMQMetrics()

	m.SetZMQHealthStatus(2) // healthy
	if v := testutil.ToFloat64(m.ZMQHealthStatus); v != 2 {
		t.Errorf("ZMQHealthStatus = %v, want 2", v)
	}

	m.SetZMQHealthStatus(4) // failed
	if v := testutil.ToFloat64(m.ZMQHealthStatus); v != 4 {
		t.Errorf("ZMQHealthStatus = %v, want 4", v)
	}
}

func TestZMQCounters(t *testing.T) {
	t.Parallel()
	m := newZMQMetrics()

	m.RecordZMQMessage()
	m.RecordZMQMessage()
	m.RecordZMQReconnect()

	if v := testutil.ToFloat64(m.ZMQMessagesReceived); v != 2 {
		t.Errorf("ZMQMessagesReceived = %v, want 2", v)
	}
	if v := testutil.ToFloat64(m.ZMQReconnects); v != 1 {
		t.Errorf("ZMQReconnects = %v, want 1", v)
	}
}

func TestSetZMQLastMessageAge(t *testing.T) {
	t.Parallel()
	m := newZMQMetrics()

	m.SetZMQLastMessageAge(45.5)

	if v := testutil.ToFloat64(m.ZMQLastMessageAge); v != 45.5 {
		t.Errorf("ZMQLastMessageAge = %v, want 45.5", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Latency recording (histograms — verify no panic)
// ═══════════════════════════════════════════════════════════════════════════════

func TestLatencyRecording(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		ShareValidationLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "t_share_val_lat", Buckets: prometheus.DefBuckets,
		}),
		JobBroadcastLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "t_job_broad_lat", Buckets: prometheus.DefBuckets,
		}),
		DBWriteLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "t_db_write_lat", Buckets: prometheus.DefBuckets,
		}),
		RPCCallLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "t_rpc_call_lat", Buckets: prometheus.DefBuckets,
		}),
	}

	// These methods should not panic
	m.RecordShareValidation(100 * time.Microsecond)
	m.RecordJobBroadcast(500 * time.Microsecond)
	m.RecordDBWrite(5 * time.Millisecond)
	m.RecordRPCCall(50 * time.Millisecond)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Network metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestUpdateHashrate(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		PoolHashrate: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_pool_hashrate"}),
	}

	m.UpdateHashrate(1.5e12)

	if v := testutil.ToFloat64(m.PoolHashrate); v != 1.5e12 {
		t.Errorf("PoolHashrate = %v, want 1.5e12", v)
	}
}

func TestUpdateNetworkInfo(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		NetworkDifficulty: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_net_diff"}),
		NetworkHashrate:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_net_hash"}),
	}

	m.UpdateNetworkInfo(1234567.89, 3.5e15)

	if v := testutil.ToFloat64(m.NetworkDifficulty); v != 1234567.89 {
		t.Errorf("NetworkDifficulty = %v, want 1234567.89", v)
	}
	if v := testutil.ToFloat64(m.NetworkHashrate); v != 3.5e15 {
		t.Errorf("NetworkHashrate = %v, want 3.5e15", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Circuit breaker metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestCircuitBreakerMetrics(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		CircuitBreakerState:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_cb_state"}),
		CircuitBreakerBlocked:     prometheus.NewCounter(prometheus.CounterOpts{Name: "t_cb_blocked"}),
		CircuitBreakerTransitions: prometheus.NewCounter(prometheus.CounterOpts{Name: "t_cb_transitions"}),
	}

	m.SetCircuitBreakerState(0) // closed
	if v := testutil.ToFloat64(m.CircuitBreakerState); v != 0 {
		t.Errorf("CircuitBreakerState = %v, want 0", v)
	}

	m.SetCircuitBreakerState(1) // open
	if v := testutil.ToFloat64(m.CircuitBreakerState); v != 1 {
		t.Errorf("CircuitBreakerState = %v, want 1", v)
	}

	m.RecordCircuitBreakerBlocked()
	m.RecordCircuitBreakerBlocked()
	if v := testutil.ToFloat64(m.CircuitBreakerBlocked); v != 2 {
		t.Errorf("CircuitBreakerBlocked = %v, want 2", v)
	}

	m.RecordCircuitBreakerTransition()
	if v := testutil.ToFloat64(m.CircuitBreakerTransitions); v != 1 {
		t.Errorf("CircuitBreakerTransitions = %v, want 1", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// WAL metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestWALMetrics(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		WALReplayCount:    prometheus.NewCounter(prometheus.CounterOpts{Name: "t_wal_replay_count"}),
		WALReplayDuration: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_wal_replay_dur"}),
		WALWriteErrors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "t_wal_write_err"}),
		WALCommitErrors:   prometheus.NewCounter(prometheus.CounterOpts{Name: "t_wal_commit_err"}),
		WALSyncErrors:     prometheus.NewCounter(prometheus.CounterOpts{Name: "t_wal_sync_err"}),
		WALFileSize:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_wal_file_size"}),
	}

	m.RecordWALReplay(150, 2.5)
	if v := testutil.ToFloat64(m.WALReplayCount); v != 150 {
		t.Errorf("WALReplayCount = %v, want 150", v)
	}
	if v := testutil.ToFloat64(m.WALReplayDuration); v != 2.5 {
		t.Errorf("WALReplayDuration = %v, want 2.5", v)
	}

	m.RecordWALWriteError()
	m.RecordWALCommitError()
	m.RecordWALSyncError()
	m.RecordWALSyncError()

	if v := testutil.ToFloat64(m.WALWriteErrors); v != 1 {
		t.Errorf("WALWriteErrors = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.WALCommitErrors); v != 1 {
		t.Errorf("WALCommitErrors = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.WALSyncErrors); v != 2 {
		t.Errorf("WALSyncErrors = %v, want 2", v)
	}

	m.SetWALFileSize(1024 * 1024)
	if v := testutil.ToFloat64(m.WALFileSize); v != 1048576 {
		t.Errorf("WALFileSize = %v, want 1048576", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Payment metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestPaymentMetrics(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		PaymentProcessorFailedCycles: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_pay_failed_cycles"}),
		PaidBlockReorgs:              prometheus.NewCounter(prometheus.CounterOpts{Name: "t_paid_reorgs"}),
	}

	m.SetPaymentProcessorFailedCycles(5)
	if v := testutil.ToFloat64(m.PaymentProcessorFailedCycles); v != 5 {
		t.Errorf("PaymentProcessorFailedCycles = %v, want 5", v)
	}

	// Overwrite with lower value (reset after recovery)
	m.SetPaymentProcessorFailedCycles(0)
	if v := testutil.ToFloat64(m.PaymentProcessorFailedCycles); v != 0 {
		t.Errorf("PaymentProcessorFailedCycles = %v, want 0 (reset after recovery)", v)
	}

	m.RecordPaidBlockReorg()
	if v := testutil.ToFloat64(m.PaidBlockReorgs); v != 1 {
		t.Errorf("PaidBlockReorgs = %v, want 1", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DB HA metrics
// ═══════════════════════════════════════════════════════════════════════════════

func TestDBHAMetrics(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		DBFailoverTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: "t_db_failover"}),
		DBActiveNode:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "t_db_active_node"}, []string{"node_id"}),
		DBBlockQueueLen: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_db_queue_len"}),
	}

	m.IncDBFailover()
	if v := testutil.ToFloat64(m.DBFailoverTotal); v != 1 {
		t.Errorf("DBFailoverTotal = %v, want 1", v)
	}

	m.SetDBActiveNode("node-2")
	if v := testutil.ToFloat64(m.DBActiveNode.WithLabelValues("node-2")); v != 1 {
		t.Errorf("DBActiveNode[node-2] = %v, want 1", v)
	}

	m.SetDBBlockQueueLen(42)
	if v := testutil.ToFloat64(m.DBBlockQueueLen); v != 42 {
		t.Errorf("DBBlockQueueLen = %v, want 42", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Block maturity metrics (Sentinel)
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockMaturityMetrics(t *testing.T) {
	t.Parallel()
	m := &Metrics{
		BlocksPendingMaturityCount: prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_pending_maturity"}),
		BlocksOldestPendingAgeSec:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "t_oldest_pending"}),
	}

	m.SetBlocksPendingMaturityCount(7)
	m.SetBlocksOldestPendingAgeSec(3600.0)

	if v := testutil.ToFloat64(m.BlocksPendingMaturityCount); v != 7 {
		t.Errorf("BlocksPendingMaturityCount = %v, want 7", v)
	}
	if v := testutil.ToFloat64(m.BlocksOldestPendingAgeSec); v != 3600.0 {
		t.Errorf("BlocksOldestPendingAgeSec = %v, want 3600.0", v)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Stop (nil server safety)
// ═══════════════════════════════════════════════════════════════════════════════

func TestStop_NilServer(t *testing.T) {
	t.Parallel()
	m := &Metrics{} // No server started

	err := m.Stop()
	if err != nil {
		t.Errorf("Stop() with nil server should return nil, got %v", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Authentication middleware — real HTTP tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAuthMiddleware_NoAuth_AllowsAll(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("", nil) // No auth configured
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when no auth configured, got %d", rec.Code)
	}
}

func TestAuthMiddleware_BearerToken_Valid(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("secret-token-123", nil)
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret-token-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", rec.Code)
	}
}

func TestAuthMiddleware_BearerToken_Invalid(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("secret-token-123", nil)
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid token, got %d", rec.Code)
	}
}

func TestAuthMiddleware_BearerToken_Missing(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("secret-token-123", nil)
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	// No Authorization header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with missing token, got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header in 401 response")
	}
}

func TestAuthMiddleware_BearerToken_WrongFormat(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("secret-token-123", nil)
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with Basic auth format, got %d", rec.Code)
	}
}

func TestAuthMiddleware_BearerToken_CaseSensitive(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("secret-token-123", nil)
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "bearer secret-token-123") // lowercase "bearer"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for lowercase 'bearer' prefix, got %d", rec.Code)
	}
}

func TestAuthMiddleware_IPWhitelist_Allowed(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("", []string{"192.168.1.0/24"})
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for whitelisted IP, got %d", rec.Code)
	}
}

func TestAuthMiddleware_IPWhitelist_Denied(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("", []string{"192.168.1.0/24"})
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-whitelisted IP, got %d", rec.Code)
	}
}

func TestAuthMiddleware_IPWhitelist_BypassesToken(t *testing.T) {
	t.Parallel()
	// Both IP whitelist and token configured — whitelisted IP bypasses token
	m := newAuthMetrics("required-token", []string{"10.0.0.0/8"})
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "10.0.0.50:12345"
	// No Authorization header — but IP is whitelisted
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for whitelisted IP (no token needed), got %d", rec.Code)
	}
}

func TestAuthMiddleware_IPWhitelist_NonWhitelistedNeedsToken(t *testing.T) {
	t.Parallel()
	// IP whitelist configured but client IP not whitelisted — should be denied
	// (IP whitelist takes precedence and returns forbidden, not falling through to token)
	m := newAuthMetrics("required-token", []string{"192.168.1.0/24"})
	handler := m.metricsAuthMiddleware(testOKHandler)

	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer required-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// When IP whitelist is configured, non-whitelisted IPs are denied regardless of token
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-whitelisted IP even with valid token, got %d", rec.Code)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// extractClientIP — real HTTP request parsing
// ═══════════════════════════════════════════════════════════════════════════════

func TestMetricsExtractClientIP(t *testing.T) {
	t.Parallel()
	m := newAuthMetrics("", nil)

	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		expected   string
	}{
		{"direct_ipv4", "", "", "192.168.1.100:12345", "192.168.1.100"},
		{"xff_single", "10.0.0.1", "", "192.168.1.100:12345", "10.0.0.1"},
		{"xff_chain_takes_first", "10.0.0.1, 172.16.0.1, 192.168.1.1", "", "192.168.1.100:12345", "10.0.0.1"},
		{"xri", "", "10.0.0.2", "192.168.1.100:12345", "10.0.0.2"},
		{"xff_precedence_over_xri", "10.0.0.1", "10.0.0.2", "192.168.1.100:12345", "10.0.0.1"},
		{"ipv6_direct", "", "", "[::1]:12345", "::1"},
		{"xff_with_spaces", "  10.0.0.1  ,  172.16.0.1  ", "", "192.168.1.100:12345", "10.0.0.1"},
		{"xff_ipv6", "2001:db8::1", "", "192.168.1.100:12345", "2001:db8::1"},
		{"xri_trimmed", "", "  10.0.0.3  ", "192.168.1.100:12345", "10.0.0.3"},
		{"remoteaddr_no_port", "", "", "192.168.1.100", "192.168.1.100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}

			got := m.extractClientIP(req)
			if got != tt.expected {
				t.Errorf("extractClientIP() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// isIPAllowed — real CIDR matching
// ═══════════════════════════════════════════════════════════════════════════════

func TestMetricsIsIPAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		clientIP  string
		whitelist []string
		allowed   bool
	}{
		{"exact_match", "192.168.1.100", []string{"192.168.1.100"}, true},
		{"no_match", "192.168.1.100", []string{"10.0.0.1"}, false},
		{"cidr_match", "192.168.1.100", []string{"192.168.1.0/24"}, true},
		{"cidr_no_match", "192.168.2.100", []string{"192.168.1.0/24"}, false},
		{"ipv6_exact", "::1", []string{"::1"}, true},
		{"ipv6_cidr", "2001:db8::1", []string{"2001:db8::/32"}, true},
		{"multiple_entries_second", "10.0.0.50", []string{"192.168.1.0/24", "10.0.0.0/24"}, true},
		{"empty_whitelist", "192.168.1.100", []string{}, false},
		{"invalid_client_ip", "not-an-ip", []string{"0.0.0.0/0"}, false},
		{"any_ipv4", "192.168.1.100", []string{"0.0.0.0/0"}, true},
		{"localhost_cidr", "127.0.0.1", []string{"127.0.0.0/8"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Metrics{
				cfg: &config.MetricsConfig{AllowedIPs: tt.whitelist},
			}

			got := m.isIPAllowed(tt.clientIP)
			if got != tt.allowed {
				t.Errorf("isIPAllowed(%q) = %v, want %v", tt.clientIP, got, tt.allowed)
			}
		})
	}
}
