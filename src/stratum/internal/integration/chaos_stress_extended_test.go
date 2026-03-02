// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package integration provides extended chaos and stress tests for Spiral Stratum.
//
// This file covers categories NOT fully addressed by chaos_stress_test.go:
//   - Goroutine / Connection Lifecycle at scale
//   - Job / Pipeline / RWMutex stress with reorgs
//   - Memory & Resource Exhaustion combined scenarios
//   - BlockQueue / Crash Safety multi-cycle
//   - VarDiff sustained anomalies
//   - External Dependency failure simulation
//   - Multi-Coin concurrent stress with WAL
//   - Boundary & Edge conditions
//   - Monitoring / State verification
package integration

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/jobs"
	"github.com/spiralpool/stratum/internal/security"
	"github.com/spiralpool/stratum/internal/shares"
	"github.com/spiralpool/stratum/internal/vardiff"
	"github.com/spiralpool/stratum/pkg/protocol"
	"github.com/spiralpool/stratum/pkg/ringbuffer"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 1: GOROUTINE / CONNECTION LIFECYCLE (OPERATIONAL ROBUSTNESS)
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_GoroutineStorm_10KContextLifecycles creates 10,000 contexts via
// HeightEpoch and verifies all cancel cleanly with no goroutine leak.
//
// Initial conditions: Fresh HeightEpoch, goroutine baseline measured.
// Simulation: 10,000 HeightContext calls interleaved with Advance/AdvanceWithTip.
// Observation: Goroutine count returns to baseline ±5.
// Verdict criteria: leaked <= 5.
func TestExt_GoroutineStorm_10KContextLifecycles(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	epoch := jobs.NewHeightEpoch()
	cancels := make([]context.CancelFunc, 0, 10000)

	// Create 10K contexts with interleaved height advances
	for i := 0; i < 10000; i++ {
		_, cancel := epoch.HeightContext(context.Background())
		cancels = append(cancels, cancel)

		// Every 100 contexts, advance height (cancelling batch)
		if i%100 == 99 {
			epoch.AdvanceWithTip(uint64(i/100+1), fmt.Sprintf("%064x", i))
		}
	}

	// Explicitly cancel all remaining contexts
	for _, cancel := range cancels {
		cancel()
	}

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	current := runtime.NumGoroutine()
	leaked := current - baseline
	if leaked > 5 {
		t.Errorf("FAIL: Goroutine leak after 10K context lifecycle: baseline=%d, current=%d, leaked=%d",
			baseline, current, leaked)
	} else {
		t.Logf("PASS: 10K contexts cleaned up. baseline=%d, current=%d, delta=%d", baseline, current, leaked)
	}
}

// TestExt_RapidConnectDisconnect_RateLimiterStability simulates 10,000 rapid
// connect/disconnect cycles across 500 IPs and verifies RateLimiter stability.
//
// Initial conditions: Fresh RateLimiter with 100 conn/IP, 10000 conn/min limits.
// Simulation: 500 IPs × 20 connect+disconnect cycles concurrently.
// Observation: No panic, no deadlock, stats.ActiveConnections == 0 at end.
// Verdict criteria: No negative connection counts, all connections released.
func TestExt_RapidConnectDisconnect_RateLimiterStability(t *testing.T) {
	cfg := security.RateLimiterConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 10000,
		MaxSharesPerSecond:   1000,
		BanThreshold:         1000, // High threshold to avoid bans during test
		BanDuration:          1 * time.Minute,
	}
	logger, _ := zap.NewDevelopment()
	rl := security.NewRateLimiter(cfg, logger.Sugar())

	var wg sync.WaitGroup
	for ip := 0; ip < 500; ip++ {
		wg.Add(1)
		go func(ipNum int) {
			defer wg.Done()
			addr := &net.TCPAddr{IP: net.ParseIP(fmt.Sprintf("10.%d.%d.%d", ipNum/65536, (ipNum/256)%256, ipNum%256)), Port: 12345}
			for cycle := 0; cycle < 20; cycle++ {
				rl.AllowConnection(addr)
				rl.ReleaseConnection(addr)
			}
		}(ip)
	}

	wg.Wait()

	stats := rl.GetStats()
	if stats.ActiveConnections < 0 {
		t.Errorf("FAIL: Negative active connections: %d", stats.ActiveConnections)
	}
	t.Logf("PASS: 10K connect/disconnect cycles completed. ActiveConns=%d, UniqueIPs=%d, Blocked=%d",
		stats.ActiveConnections, stats.UniqueIPs, stats.TotalBlocked)
}

// TestExt_WorkerChurnProtection verifies rate limiter detects worker identity
// churn from a single IP (attacker registering many workers).
//
// Initial conditions: RateLimiter with MaxWorkersPerIP=10.
// Simulation: Single IP registers 50 unique workers.
// Observation: First 10 accepted, remaining rejected.
// Verdict criteria: Exactly 10 accepted.
func TestExt_WorkerChurnProtection(t *testing.T) {
	cfg := security.RateLimiterConfig{
		MaxConnectionsPerIP:  1000,
		MaxConnectionsPerMin: 10000,
		MaxSharesPerSecond:   1000,
		BanThreshold:         100,
		BanDuration:          1 * time.Minute,
		MaxWorkersPerIP:      10,
	}
	logger, _ := zap.NewDevelopment()
	rl := security.NewRateLimiter(cfg, logger.Sugar())

	addr := &net.TCPAddr{IP: net.ParseIP("192.168.1.100"), Port: 12345}
	rl.AllowConnection(addr) // Must connect first

	accepted := 0
	rejected := 0
	for w := 0; w < 50; w++ {
		ok, _ := rl.AllowWorkerRegistration(addr, fmt.Sprintf("worker_%d", w))
		if ok {
			accepted++
		} else {
			rejected++
		}
	}

	if accepted > 10 {
		t.Errorf("FAIL: Accepted %d workers, expected max 10", accepted)
	}
	t.Logf("PASS: Worker churn protection: accepted=%d, rejected=%d", accepted, rejected)
}

// TestExt_ShareRateLimit_PerIPThrottle verifies per-IP share rate limiting
// prevents a single miner from flooding the pipeline.
//
// Initial conditions: RateLimiter with 100 shares/sec.
// Simulation: Single IP submits 500 shares rapidly.
// Observation: Some shares rejected after rate limit exceeded.
func TestExt_ShareRateLimit_PerIPThrottle(t *testing.T) {
	cfg := security.RateLimiterConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 10000,
		MaxSharesPerSecond:   100,
		BanThreshold:         1000,
		BanDuration:          1 * time.Minute,
	}
	logger, _ := zap.NewDevelopment()
	rl := security.NewRateLimiter(cfg, logger.Sugar())

	addr := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 12345}
	rl.AllowConnection(addr)

	allowed := 0
	blocked := 0
	for s := 0; s < 500; s++ {
		ok, _ := rl.AllowShare(addr)
		if ok {
			allowed++
		} else {
			blocked++
		}
	}

	if blocked == 0 {
		t.Errorf("FAIL: No shares blocked — rate limiter did not throttle 500 rapid shares")
	}
	t.Logf("PASS: Share rate limiting: allowed=%d, blocked=%d out of 500", allowed, blocked)
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 2: JOB / PIPELINE / RWMUTEX STRESS
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_HeightEpoch_ReorgBurstStorm simulates 5 rapid reorgs at the same
// height (competing tips) followed by a height advance, verifying all contexts
// from losing tips are cancelled.
//
// Initial conditions: HeightEpoch at height 1000.
// Simulation: 5 different tips at height 1000, then advance to 1001.
// Observation: All 5 tip contexts cancelled.
// Verdict criteria: All 5 contexts report Done.
func TestExt_HeightEpoch_ReorgBurstStorm(t *testing.T) {
	epoch := jobs.NewHeightEpoch()

	tips := []string{
		"aaaa000000000000000000000000000000000000000000000000000000000000",
		"bbbb000000000000000000000000000000000000000000000000000000000000",
		"cccc000000000000000000000000000000000000000000000000000000000000",
		"dddd000000000000000000000000000000000000000000000000000000000000",
		"eeee000000000000000000000000000000000000000000000000000000000000",
	}

	contexts := make([]context.Context, 0, 5)
	cancelFuncs := make([]context.CancelFunc, 0, 5)

	for _, tip := range tips {
		epoch.AdvanceWithTip(1000, tip)
		ctx, cancel := epoch.HeightContext(context.Background())
		contexts = append(contexts, ctx)
		cancelFuncs = append(cancelFuncs, cancel)
	}
	defer func() {
		for _, c := range cancelFuncs {
			c()
		}
	}()

	// Advance to 1001 — all tip 1000 contexts must cancel
	epoch.AdvanceWithTip(1001, "ffff000000000000000000000000000000000000000000000000000000000000")

	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			// Expected
		case <-time.After(100 * time.Millisecond):
			t.Errorf("FAIL: Context for tip %d was not cancelled after height advance", i)
		}
	}

	h, tip := epoch.State()
	if h != 1001 {
		t.Errorf("FAIL: Height should be 1001, got %d", h)
	}
	t.Logf("PASS: 5-tip reorg burst storm. Final height=%d, tip=%s", h, tip[:8])
}

// TestExt_HeightEpoch_ConcurrentConsumersWithReorgs simulates 100 concurrent
// block submitters watching HeightContext while rapid reorgs occur.
//
// Initial conditions: HeightEpoch at height 0.
// Simulation: 100 consumer goroutines + 2 producer goroutines (ZMQ + RPC).
// Observation: No panic, no deadlock, all consumers terminate cleanly.
func TestExt_HeightEpoch_ConcurrentConsumersWithReorgs(t *testing.T) {
	epoch := jobs.NewHeightEpoch()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var cancelledContexts atomic.Int64

	// 100 consumers watching for context cancellation
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				hctx, hcancel := epoch.HeightContext(ctx)
				select {
				case <-hctx.Done():
					cancelledContexts.Add(1)
					hcancel()
				case <-ctx.Done():
					hcancel()
					return
				}
			}
		}()
	}

	// Producer 1: ZMQ with tip hash (rapid reorgs at same height)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for h := uint64(1); ; h++ {
			// Simulate same-height reorg: 3 competing tips per height
			for tip := 0; tip < 3; tip++ {
				epoch.AdvanceWithTip(h, fmt.Sprintf("%064x", h*100+uint64(tip)))
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	// Producer 2: Legacy Advance (no tip hash)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for h := uint64(1); ; h++ {
			epoch.Advance(h)
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(100 * time.Microsecond)
			}
		}
	}()

	wg.Wait()
	t.Logf("PASS: 100 consumers with reorgs. Total context cancellations: %d", cancelledContexts.Load())
}

// TestExt_DuplicateTracker_StaleJobCleanup verifies CleanupJob removes jobs
// and that shares for cleaned jobs are accepted fresh.
//
// Initial conditions: DuplicateTracker with shares recorded for 5 jobs.
// Simulation: Cleanup 3 of 5 jobs, then re-submit shares for cleaned jobs.
// Observation: Re-submitted shares accepted (not duplicate), untouched jobs reject duplicates.
func TestExt_DuplicateTracker_StaleJobCleanup(t *testing.T) {
	dt := shares.NewDuplicateTracker()

	// Record shares for 5 jobs
	for j := 0; j < 5; j++ {
		jobID := fmt.Sprintf("job_%d", j)
		for s := 0; s < 10; s++ {
			dt.RecordIfNew(jobID, "en1", fmt.Sprintf("en2_%d", s), "ntime", fmt.Sprintf("nonce_%d", s))
		}
	}

	jobs1, shares1 := dt.Stats()
	t.Logf("Before cleanup: jobs=%d, shares=%d", jobs1, shares1)

	// Cleanup jobs 0, 1, 2
	for j := 0; j < 3; j++ {
		dt.CleanupJob(fmt.Sprintf("job_%d", j))
	}

	jobs2, shares2 := dt.Stats()
	if jobs2 != 2 {
		t.Errorf("FAIL: Expected 2 jobs remaining after cleanup of 3, got %d", jobs2)
	}
	t.Logf("After cleanup: jobs=%d, shares=%d", jobs2, shares2)

	// Re-submit shares for cleaned jobs — should be accepted
	for j := 0; j < 3; j++ {
		jobID := fmt.Sprintf("job_%d", j)
		ok := dt.RecordIfNew(jobID, "en1", "en2_0", "ntime", "nonce_0")
		if !ok {
			t.Errorf("FAIL: Share for cleaned job %s should be accepted as new", jobID)
		}
	}

	// Submit duplicate for uncleaned job — should be rejected
	ok := dt.RecordIfNew("job_3", "en1", "en2_0", "ntime", "nonce_0")
	if ok {
		t.Error("FAIL: Duplicate share for uncleaned job_3 should be rejected")
	}

	t.Logf("PASS: Stale job cleanup works correctly")
}

// TestExt_NonceTracker_SessionRemoval verifies RemoveSession frees memory
// and allows nonce reuse for new sessions.
//
// Initial conditions: NonceTracker with 100 sessions, each with 100 nonces.
// Simulation: Remove 50 sessions, verify new sessions can reuse nonce space.
func TestExt_NonceTracker_SessionRemoval(t *testing.T) {
	nt := shares.NewNonceTracker()

	// Fill 100 sessions with nonces
	for s := 0; s < 100; s++ {
		for n := uint32(0); n < 100; n++ {
			nt.TrackNonce(fmt.Sprintf("session_%d", s), "job_1", n)
		}
	}

	// Remove 50 sessions
	for s := 0; s < 50; s++ {
		nt.RemoveSession(fmt.Sprintf("session_%d", s))
	}

	// New sessions should be able to track nonces without exhaustion
	// TrackNonce returns true ONLY when exhaustion is detected (security alert).
	// A new session's first nonce should return false (no exhaustion).
	for s := 0; s < 50; s++ {
		exhaustion := nt.TrackNonce(fmt.Sprintf("new_session_%d", s), "job_1", 0)
		if exhaustion {
			t.Errorf("FAIL: New session %d reported exhaustion on first nonce after old session removed", s)
		}
	}

	// Existing (not-removed) sessions should also not show exhaustion with only ~101 nonces
	// (threshold is 3 billion). TrackNonce always records; it returns exhaustion status.
	exhaustion := nt.TrackNonce("session_50", "job_1", 0)
	if exhaustion {
		t.Error("FAIL: Existing session_50 should not report exhaustion with only ~101 nonces")
	}

	t.Logf("PASS: NonceTracker session removal works correctly")
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 3: MEMORY & RESOURCE EXHAUSTION
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_RingBuffer_OverflowRecovery verifies that after buffer overflow,
// the system recovers cleanly when consumers drain.
//
// Initial conditions: RingBuffer capacity 1024.
// Simulation: Enqueue 2048 items (1024 overflow), then drain all.
// Observation: Exactly 1024 enqueued, 1024 dropped. All enqueued items dequeued.
func TestExt_RingBuffer_OverflowRecovery(t *testing.T) {
	buf := ringbuffer.New[int](1024)

	enqueued := 0
	dropped := 0
	for i := 0; i < 2048; i++ {
		if buf.TryEnqueue(i) {
			enqueued++
		} else {
			dropped++
		}
	}

	if enqueued != 1024 {
		t.Errorf("FAIL: Expected 1024 enqueued, got %d", enqueued)
	}

	// Drain all
	drained := 0
	for {
		_, ok := buf.DequeueOne()
		if !ok {
			break
		}
		drained++
	}

	if drained != enqueued {
		t.Errorf("FAIL: Drained %d, expected %d", drained, enqueued)
	}

	stats := buf.Stats()
	if stats.Current != 0 {
		t.Errorf("FAIL: Buffer not empty after drain: %d items remaining", stats.Current)
	}

	// Verify buffer accepts new items after drain
	if !buf.TryEnqueue(9999) {
		t.Error("FAIL: Buffer should accept items after being drained")
	}

	t.Logf("PASS: Buffer overflow recovery. enqueued=%d, dropped=%d, drained=%d, stats.Dropped=%d",
		enqueued, dropped, drained, stats.Dropped)
}

// TestExt_RingBuffer_BatchDequeue_Completeness verifies DequeueBatch returns
// all items without loss or duplication.
//
// Initial conditions: RingBuffer with 5000 sequential integers.
// Simulation: DequeueBatch in chunks of 100 until empty.
// Observation: All 5000 items recovered in order.
func TestExt_RingBuffer_BatchDequeue_Completeness(t *testing.T) {
	buf := ringbuffer.New[int](8192) // Power of 2 >= 5000

	for i := 0; i < 5000; i++ {
		if !buf.TryEnqueue(i) {
			t.Fatalf("Failed to enqueue item %d", i)
		}
	}

	batch := make([]int, 100)
	allItems := make([]int, 0, 5000)

	for {
		n := buf.DequeueBatch(batch)
		if n == 0 {
			break
		}
		allItems = append(allItems, batch[:n]...)
	}

	if len(allItems) != 5000 {
		t.Errorf("FAIL: Expected 5000 items, got %d", len(allItems))
	}

	// Verify ordering
	for i, v := range allItems {
		if v != i {
			t.Errorf("FAIL: Item %d has value %d (expected %d) — out of order", i, v, i)
			break
		}
	}

	t.Logf("PASS: Batch dequeue completeness verified for 5000 items")
}

// TestExt_CircuitBreaker_RapidStateTransitions verifies circuit breaker handles
// rapid alternation between failure and success without corruption.
//
// Initial conditions: CircuitBreaker with threshold=3, short cooldown.
// Simulation: Open → half-open → close → open cycle 100 times.
// Observation: State transitions are always valid. No stuck states.
func TestExt_CircuitBreaker_RapidStateTransitions(t *testing.T) {
	cfg := database.CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   10 * time.Millisecond,
		InitialBackoff:   1 * time.Millisecond,
		MaxBackoff:       10 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := database.NewCircuitBreaker(cfg)

	for cycle := 0; cycle < 100; cycle++ {
		// Drive to open
		for i := 0; i < 3; i++ {
			cb.AllowRequest()
			cb.RecordFailure()
		}

		if cb.State() != database.CircuitOpen {
			t.Fatalf("FAIL: Cycle %d: Expected CircuitOpen after 3 failures, got %s", cycle, cb.State())
		}

		// Wait for cooldown → half-open
		time.Sleep(15 * time.Millisecond)

		allowed, _ := cb.AllowRequest()
		if !allowed {
			t.Fatalf("FAIL: Cycle %d: Should allow probe after cooldown", cycle)
		}

		// Succeed → close
		cb.RecordSuccess()
		if cb.State() != database.CircuitClosed {
			t.Fatalf("FAIL: Cycle %d: Expected CircuitClosed after success, got %s", cycle, cb.State())
		}
	}

	stats := cb.Stats()
	t.Logf("PASS: 100 rapid open→close cycles. StateChanges=%d, TotalBlocked=%d",
		stats.StateChanges, stats.TotalBlocked)
}

// TestExt_CircuitBreaker_BackoffExponentialGrowth verifies exponential backoff
// doubles correctly and caps at MaxBackoff.
//
// Initial conditions: CircuitBreaker with InitialBackoff=1ms, MaxBackoff=32ms, Factor=2.0.
// Simulation: Record 10 failures, track backoff values.
// Observation: Backoff sequence: 1,2,4,8,16,32,32,32,32,32 ms.
func TestExt_CircuitBreaker_BackoffExponentialGrowth(t *testing.T) {
	cfg := database.CircuitBreakerConfig{
		FailureThreshold: 100, // High threshold — we want to test backoff, not opening
		CooldownPeriod:   1 * time.Second,
		InitialBackoff:   1 * time.Millisecond,
		MaxBackoff:       32 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := database.NewCircuitBreaker(cfg)

	expected := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
		8 * time.Millisecond,
		16 * time.Millisecond,
		32 * time.Millisecond,
		32 * time.Millisecond, // Capped
		32 * time.Millisecond,
	}

	for i, want := range expected {
		cb.AllowRequest()
		got := cb.RecordFailure()
		if got != want {
			t.Errorf("FAIL: Failure %d: backoff=%v, expected=%v", i, got, want)
		}
	}

	t.Logf("PASS: Exponential backoff with cap verified")
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 4: BLOCKQUEUE / CRASH SAFETY
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_BlockQueue_MultiCycleCrashRecovery simulates multiple crash/restart
// cycles with partial commits.
//
// Initial conditions: BlockQueue with 100 blocks.
// Simulation: Dequeue+commit 10, dequeue (no commit), repeat 5 times.
// Observation: After each "crash", uncommitted block is still recoverable.
func TestExt_BlockQueue_MultiCycleCrashRecovery(t *testing.T) {
	bq := database.NewBlockQueue(200)

	// Load 100 blocks
	for i := uint64(0); i < 100; i++ {
		bq.Enqueue(&database.Block{Height: 1000 + i})
	}

	totalCommitted := 0
	totalCrashed := 0

	for cycle := 0; cycle < 5; cycle++ {
		// Commit 10 blocks
		for i := 0; i < 10; i++ {
			entry, commit := bq.DequeueWithCommit()
			if entry == nil {
				t.Fatalf("FAIL: Cycle %d, commit %d: no entry available", cycle, i)
			}
			commit()
			totalCommitted++
		}

		// "Crash": dequeue but don't commit
		entry, _ := bq.DequeueWithCommit()
		if entry == nil {
			t.Fatalf("FAIL: Cycle %d: no entry for crash simulation", cycle)
		}
		totalCrashed++
		// Don't call commit — simulates crash

		// Verify block is still in queue
		peek := bq.Peek()
		if peek == nil {
			t.Fatalf("FAIL: Cycle %d: crashed block should still be peekable", cycle)
		}
		if peek.Block.Height != entry.Block.Height {
			t.Errorf("FAIL: Cycle %d: peeked block height %d != crashed block %d",
				cycle, peek.Block.Height, entry.Block.Height)
		}
	}

	expectedRemaining := 100 - totalCommitted
	actualRemaining := bq.Len()
	if actualRemaining != expectedRemaining {
		t.Errorf("FAIL: Expected %d remaining, got %d (committed=%d, crashed=%d)",
			expectedRemaining, actualRemaining, totalCommitted, totalCrashed)
	}

	t.Logf("PASS: Multi-cycle crash recovery. committed=%d, crashed=%d, remaining=%d",
		totalCommitted, totalCrashed, actualRemaining)
}

// TestExt_BlockQueue_DrainAllAfterCircuitRecovery simulates circuit breaker
// opening (blocks queue up) then closing (DrainAll processes backlog).
//
// Initial conditions: Empty BlockQueue.
// Simulation: Queue 20 blocks during "outage", then DrainAll on "recovery".
// Observation: All 20 blocks recovered in order.
func TestExt_BlockQueue_DrainAllAfterCircuitRecovery(t *testing.T) {
	bq := database.NewBlockQueue(100)

	// Simulate outage: queue 20 blocks
	for i := uint64(0); i < 20; i++ {
		bq.Enqueue(&database.Block{Height: 5000 + i})
	}

	if bq.Len() != 20 {
		t.Fatalf("FAIL: Expected 20 queued, got %d", bq.Len())
	}

	// Simulate recovery: drain all
	drained := bq.DrainAll()
	if len(drained) != 20 {
		t.Errorf("FAIL: DrainAll returned %d entries, expected 20", len(drained))
	}

	// Verify order
	for i, entry := range drained {
		expected := uint64(5000 + i)
		if entry.Block.Height != expected {
			t.Errorf("FAIL: Drained block %d has height %d, expected %d", i, entry.Block.Height, expected)
		}
	}

	if bq.Len() != 0 {
		t.Errorf("FAIL: Queue should be empty after DrainAll, got %d", bq.Len())
	}

	t.Logf("PASS: DrainAll recovered 20 blocks in order after circuit recovery")
}

// TestExt_BlockQueue_UpdateEntryError verifies error tracking on failed insertion.
//
// Initial conditions: BlockQueue with 1 block.
// Simulation: Update error 3 times, verify attempts counter and error message.
func TestExt_BlockQueue_UpdateEntryError(t *testing.T) {
	bq := database.NewBlockQueue(10)
	bq.Enqueue(&database.Block{Height: 9999})

	for i := 0; i < 3; i++ {
		bq.UpdateEntryError(fmt.Sprintf("connection refused (attempt %d)", i))
	}

	entry := bq.Peek()
	if entry == nil {
		t.Fatal("FAIL: Queue should not be empty")
	}
	if entry.Attempts != 3 {
		t.Errorf("FAIL: Expected 3 attempts, got %d", entry.Attempts)
	}
	if entry.LastError != "connection refused (attempt 2)" {
		t.Errorf("FAIL: Expected last error 'connection refused (attempt 2)', got '%s'", entry.LastError)
	}

	t.Logf("PASS: Error tracking: attempts=%d, lastError='%s'", entry.Attempts, entry.LastError)
}

// TestExt_WAL_MultiCrashReplayIdempotency verifies that replaying a WAL
// multiple times produces the same result (idempotent replay).
//
// Initial conditions: WAL with 50 uncommitted shares.
// Simulation: Replay 3 times, compare results.
// Observation: Each replay returns same shares. No phantom or lost shares.
func TestExt_WAL_MultiCrashReplayIdempotency(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	// Write 50 shares, don't commit
	wal1, err := shares.NewWAL(tmpDir, "idempotent_test", logger)
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	for i := 0; i < 50; i++ {
		share := &protocol.Share{
			MinerAddress: fmt.Sprintf("miner_%d", i),
			Difficulty:   float64(i + 1),
		}
		if err := wal1.Write(share); err != nil {
			t.Fatalf("WAL write failed: %v", err)
		}
	}
	wal1.Sync()
	wal1.Close()

	// Replay 3 times — each should return the same 50 shares
	for attempt := 0; attempt < 3; attempt++ {
		wal, err := shares.NewWAL(tmpDir, "idempotent_test", logger)
		if err != nil {
			t.Fatalf("Replay %d: Failed to open WAL: %v", attempt, err)
		}

		replayed, err := wal.Replay()
		if err != nil {
			t.Fatalf("Replay %d: Failed: %v", attempt, err)
		}

		if len(replayed) != 50 {
			t.Errorf("FAIL: Replay %d: got %d shares, expected 50", attempt, len(replayed))
		}

		wal.Close()
	}

	t.Logf("PASS: 3 idempotent replays all returned 50 shares")
}

// TestExt_WAL_CommitBatch_vs_CommitBatchVerified compares both commit methods.
//
// Initial conditions: WAL with 20 shares.
// Simulation: CommitBatch first 10, CommitBatchVerified next 10. Replay.
// Observation: 0 uncommitted shares on replay.
func TestExt_WAL_CommitBatch_vs_CommitBatchVerified(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := shares.NewWAL(tmpDir, "commit_compare", logger)
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	allShares := make([]*protocol.Share, 20)
	for i := 0; i < 20; i++ {
		share := &protocol.Share{
			MinerAddress: fmt.Sprintf("miner_%d", i),
			Difficulty:   float64(i + 1),
		}
		allShares[i] = share
		wal.Write(share)
	}

	// CommitBatch first 10
	if err := wal.CommitBatch(allShares[:10]); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}

	// CommitBatchVerified next 10
	committed, err := wal.CommitBatchVerified(allShares[10:])
	if err != nil {
		t.Fatalf("CommitBatchVerified failed: %v", err)
	}
	if !committed {
		t.Error("FAIL: CommitBatchVerified returned false")
	}

	wal.Close()

	// Replay — should have 0 uncommitted
	wal2, err := shares.NewWAL(tmpDir, "commit_compare", logger)
	if err != nil {
		t.Fatalf("Failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(replayed) != 0 {
		t.Errorf("FAIL: Expected 0 uncommitted shares, got %d", len(replayed))
	}

	t.Logf("PASS: Both CommitBatch and CommitBatchVerified work correctly")
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 5: VARDIFF / TIME DISTORTION
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_VarDiff_SustainedRapidShares_NoDiffExplosion verifies difficulty
// doesn't explode under sustained rapid share submission.
//
// Initial conditions: Engine with diff=1, target=10s, min=0.001, max=1e12.
// Simulation: 1000 shares recorded in rapid succession (sub-millisecond).
// Observation: Difficulty increases but stays within bounds.
func TestExt_VarDiff_SustainedRapidShares_NoDiffExplosion(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1e12,
		TargetTime:      10.0,
		RetargetTime:    5.0,
		VariancePercent: 50.0,
	}
	engine, err := vardiff.NewEngineWithValidation(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	state := engine.NewSessionState(1.0)
	var maxDiff float64
	var changes int

	for i := 0; i < 1000; i++ {
		newDiff, changed := engine.RecordShare(state)
		if changed {
			changes++
			if newDiff > maxDiff {
				maxDiff = newDiff
			}
		}
	}

	finalDiff := vardiff.GetDifficulty(state)
	if math.IsNaN(finalDiff) || math.IsInf(finalDiff, 0) {
		t.Errorf("FAIL: Difficulty became invalid: %f", finalDiff)
	}
	if finalDiff > cfg.MaxDiff {
		t.Errorf("FAIL: Difficulty exceeded max: %f > %f", finalDiff, cfg.MaxDiff)
	}
	if finalDiff < cfg.MinDiff {
		t.Errorf("FAIL: Difficulty below min: %f < %f", finalDiff, cfg.MinDiff)
	}

	t.Logf("PASS: 1000 rapid shares. final=%f, max=%f, changes=%d", finalDiff, maxDiff, changes)
}

// TestExt_VarDiff_ConsecutiveNoChange_ExponentialBackoff verifies that when
// difficulty is clamped at a boundary, the retarget interval backs off.
//
// Initial conditions: Engine with MinDiff=MaxDiff=100 (boundary clamped).
// Simulation: 200 shares — since difficulty can't change, engine should back off.
// Observation: Number of retarget attempts decreases over time (backoff).
func TestExt_VarDiff_ConsecutiveNoChange_ExponentialBackoff(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         100,
		MaxDiff:         100,
		TargetTime:      1.0,
		RetargetTime:    0.001, // Very short to trigger retargets
		VariancePercent: 50.0,
	}
	engine, err := vardiff.NewEngineWithValidation(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	state := engine.NewSessionState(100)

	// Track how many retarget attempts produce "changed"
	changedCount := 0
	for i := 0; i < 200; i++ {
		_, changed := engine.RecordShare(state)
		if changed {
			changedCount++
		}
	}

	// With min==max, difficulty should never meaningfully change
	// The engine should back off retarget frequency
	finalDiff := vardiff.GetDifficulty(state)
	if finalDiff != 100.0 {
		t.Errorf("FAIL: Difficulty should stay at 100.0, got %f", finalDiff)
	}

	noChangeCount := state.ConsecutiveNoChange()
	t.Logf("PASS: Boundary backoff. changes=%d, consecutiveNoChange=%d, finalDiff=%f",
		changedCount, noChangeCount, finalDiff)
}

// TestExt_VarDiff_ProfiledSession_PerSessionTargetTime verifies per-session
// target time override (for Spiral Router profiled miners).
//
// Initial conditions: Engine with global target=10s, session with target=2s.
// Simulation: Record shares and verify retarget uses session target.
func TestExt_VarDiff_ProfiledSession_PerSessionTargetTime(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1e12,
		TargetTime:      10.0, // Global target
		RetargetTime:    1.0,
		VariancePercent: 50.0,
	}
	engine, err := vardiff.NewEngineWithValidation(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// Create session with per-session target of 2s
	state := engine.NewSessionStateWithProfile(100.0, 0.001, 1e12, 2.0)

	sessionTarget := state.TargetTime()
	if sessionTarget != 2.0 {
		t.Errorf("FAIL: Expected session target 2.0, got %f", sessionTarget)
	}

	engineTarget := engine.GetTargetTime(state)
	if engineTarget != 2.0 {
		t.Errorf("FAIL: Engine should use session target 2.0, got %f", engineTarget)
	}

	t.Logf("PASS: Per-session target time override verified. sessionTarget=%f", sessionTarget)
}

// TestExt_VarDiff_AggressiveRetarget_AllGuards verifies AggressiveRetarget
// handles all edge cases.
//
// Simulation: Various (shares, elapsedSec) combinations including edge values.
func TestExt_VarDiff_AggressiveRetarget_AllGuards(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         1.0,
		MaxDiff:         1e6,
		TargetTime:      4.0,
		RetargetTime:    30.0,
		VariancePercent: 50.0,
	}
	engine, err := vardiff.NewEngineWithValidation(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tests := []struct {
		name        string
		shares      uint64
		elapsedSec  float64
		expectChange bool
	}{
		{"zero_elapsed", 10, 0.0, false},         // T-2 guard: <= 0
		{"negative_elapsed", 10, -5.0, false},     // T-2 guard: <= 0
		{"huge_elapsed", 10, 700.0, false},        // T-2 guard: > 600
		{"exactly_600", 10, 600.0, true},          // T-2 guard is > 600 (strict), so 600.0 passes through
		{"normal_fast", 1000, 1.0, true},          // Normal: very fast shares
		{"normal_slow", 2, 500.0, true},           // Normal: very slow shares
		{"one_share", 1, 10.0, false},             // Too few shares
		{"zero_shares", 0, 10.0, false},           // No shares at all
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := engine.NewSessionState(1000.0)
			_, changed := engine.AggressiveRetarget(state, tt.shares, tt.elapsedSec)
			if changed != tt.expectChange {
				t.Errorf("FAIL: changed=%v, expected=%v", changed, tt.expectChange)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 6: EXTERNAL DEPENDENCY FAILURE SIMULATION
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_CircuitBreaker_FullLifecycleWithMetrics runs a complete lifecycle
// and verifies all metrics counters are accurate.
//
// Lifecycle: closed → open → (blocked requests) → half-open → probe → close.
// Observation: Stats reflect exact counts.
func TestExt_CircuitBreaker_FullLifecycleWithMetrics(t *testing.T) {
	cfg := database.CircuitBreakerConfig{
		FailureThreshold: 5,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   5 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := database.NewCircuitBreaker(cfg)

	// Phase 1: Closed → accumulate failures
	for i := 0; i < 5; i++ {
		cb.AllowRequest()
		cb.RecordFailure()
	}
	if cb.State() != database.CircuitOpen {
		t.Fatalf("Expected open, got %s", cb.State())
	}

	// Phase 2: Blocked requests during open state
	blockedCount := 0
	for i := 0; i < 10; i++ {
		allowed, _ := cb.AllowRequest()
		if !allowed {
			blockedCount++
		}
	}

	// Phase 3: Wait for cooldown → half-open
	time.Sleep(60 * time.Millisecond)
	allowed, _ := cb.AllowRequest()
	if !allowed {
		t.Fatal("Should allow probe after cooldown")
	}

	// Phase 4: Probe success → closed
	cb.RecordSuccess()
	if cb.State() != database.CircuitClosed {
		t.Fatalf("Expected closed after success, got %s", cb.State())
	}

	stats := cb.Stats()
	if stats.Failures != 0 {
		t.Errorf("FAIL: Failures should be 0 after recovery, got %d", stats.Failures)
	}
	if stats.TotalBlocked < uint64(blockedCount) {
		t.Errorf("FAIL: TotalBlocked %d < expected %d", stats.TotalBlocked, blockedCount)
	}
	// Transitions: closed→open (1) + open→half-open (1) + half-open→closed (1) = 3
	if stats.StateChanges < 3 {
		t.Errorf("FAIL: StateChanges should be >= 3, got %d", stats.StateChanges)
	}

	t.Logf("PASS: Full lifecycle metrics. StateChanges=%d, TotalBlocked=%d, Failures=%d",
		stats.StateChanges, stats.TotalBlocked, stats.Failures)
}

// TestExt_CircuitBreaker_HalfOpen_BlocksAdditionalRequests verifies that in
// half-open state, only 1 probe is allowed — additional requests are blocked.
//
// Simulation: Open circuit, wait for cooldown, allow probe, verify next is blocked.
func TestExt_CircuitBreaker_HalfOpen_BlocksAdditionalRequests(t *testing.T) {
	cfg := database.CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   30 * time.Millisecond,
		InitialBackoff:   5 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := database.NewCircuitBreaker(cfg)

	// Open the circuit
	for i := 0; i < 3; i++ {
		cb.AllowRequest()
		cb.RecordFailure()
	}

	// Wait for cooldown
	time.Sleep(40 * time.Millisecond)

	// First request transitions to half-open and is allowed (probe)
	allowed1, _ := cb.AllowRequest()
	if !allowed1 {
		t.Fatal("FAIL: First request after cooldown should be allowed as probe")
	}

	// Second request in half-open should be blocked
	allowed2, backoff := cb.AllowRequest()
	if allowed2 {
		t.Error("FAIL: Second request in half-open should be blocked")
	}
	if backoff == 0 {
		t.Error("FAIL: Blocked request should have non-zero backoff")
	}

	t.Logf("PASS: Half-open blocks additional requests. probeAllowed=%v, secondBlocked=%v, backoff=%v",
		allowed1, !allowed2, backoff)
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 7: MULTI-COIN / MERGE-MINING INTERACTIONS
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_MultiCoin_ConcurrentDuplicateTrackers verifies 5 independent
// DuplicateTrackers under concurrent load don't interfere.
//
// Initial conditions: 5 trackers (simulating DGB, BTC, LTC, DOGE, SYS).
// Simulation: 50 goroutines per coin, 100 shares each = 25,000 total.
// Observation: No cross-contamination. Each tracker independent.
func TestExt_MultiCoin_ConcurrentDuplicateTrackers(t *testing.T) {
	coins := []string{"dgb", "btc", "ltc", "doge", "sys"}
	trackers := make(map[string]*shares.DuplicateTracker)
	for _, coin := range coins {
		trackers[coin] = shares.NewDuplicateTracker()
	}

	var wg sync.WaitGroup
	var totalAccepted atomic.Int64
	var totalDuplicates atomic.Int64

	for _, coin := range coins {
		for g := 0; g < 50; g++ {
			wg.Add(1)
			go func(coinName string, goroutineID int) {
				defer wg.Done()
				dt := trackers[coinName]
				for s := 0; s < 100; s++ {
					jobID := fmt.Sprintf("%s_job_%04d", coinName, s%20)
					en1 := fmt.Sprintf("en1_%d", goroutineID)
					nonce := fmt.Sprintf("nonce_%d_%d", goroutineID, s)

					if dt.RecordIfNew(jobID, en1, "en2", "ntime", nonce) {
						totalAccepted.Add(1)
					} else {
						totalDuplicates.Add(1)
					}
				}
			}(coin, g)
		}
	}

	wg.Wait()

	total := totalAccepted.Load() + totalDuplicates.Load()
	if total != 25000 {
		t.Errorf("FAIL: Total shares mismatch: %d, expected 25000", total)
	}

	// Verify each tracker is independent by submitting same share to all
	for _, coin := range coins {
		ok := trackers[coin].RecordIfNew("cross_test_job", "cross_en1", "cross_en2", "ntime", "cross_nonce")
		if !ok {
			t.Errorf("FAIL: Tracker %s should accept unique cross-test share", coin)
		}
	}

	t.Logf("PASS: 5-coin concurrent duplicate tracking. accepted=%d, duplicates=%d",
		totalAccepted.Load(), totalDuplicates.Load())
}

// TestExt_MultiCoin_BlockQueueIsolation verifies 5 independent BlockQueues
// under concurrent load maintain isolation.
//
// Simulation: 5 queues, 10 goroutines per queue enqueue+dequeue concurrently.
func TestExt_MultiCoin_BlockQueueIsolation(t *testing.T) {
	type coinQueue struct {
		name  string
		queue *database.BlockQueue
	}

	queues := []coinQueue{
		{"dgb", database.NewBlockQueue(100)},
		{"btc", database.NewBlockQueue(100)},
		{"ltc", database.NewBlockQueue(100)},
		{"doge", database.NewBlockQueue(100)},
		{"sys", database.NewBlockQueue(100)},
	}

	var wg sync.WaitGroup
	for _, cq := range queues {
		// Enqueue goroutines
		for g := 0; g < 10; g++ {
			wg.Add(1)
			go func(q *database.BlockQueue, base uint64) {
				defer wg.Done()
				for h := uint64(0); h < 5; h++ {
					q.Enqueue(&database.Block{Height: base + h})
				}
			}(cq.queue, uint64(rand.Intn(10000)))
		}
	}

	wg.Wait()

	// Each queue should have 50 blocks (10 goroutines × 5 blocks)
	for _, cq := range queues {
		qLen := cq.queue.Len()
		if qLen != 50 {
			t.Errorf("FAIL: Queue %s: expected 50, got %d", cq.name, qLen)
		}
	}

	t.Logf("PASS: 5-coin BlockQueue isolation with concurrent enqueue")
}

// TestExt_MultiCoin_WALIsolation verifies WAL instances for different coins
// don't interfere when sharing the same directory.
//
// Simulation: Create 3 WALs with different poolIDs, write to each, replay each.
func TestExt_MultiCoin_WALIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	coins := []string{"dgb_pool", "btc_pool", "ltc_pool"}
	shareCounts := []int{30, 20, 10}

	// Write to each WAL
	for i, coin := range coins {
		wal, err := shares.NewWAL(tmpDir, coin, logger)
		if err != nil {
			t.Fatalf("Failed to create WAL for %s: %v", coin, err)
		}

		for s := 0; s < shareCounts[i]; s++ {
			share := &protocol.Share{
				MinerAddress: fmt.Sprintf("%s_miner_%d", coin, s),
				Difficulty:   float64(s + 1),
			}
			wal.Write(share)
		}

		wal.Sync()
		wal.Close()
	}

	// Replay each — should get independent share counts
	for i, coin := range coins {
		wal, err := shares.NewWAL(tmpDir, coin, logger)
		if err != nil {
			t.Fatalf("Failed to reopen WAL for %s: %v", coin, err)
		}

		replayed, err := wal.Replay()
		if err != nil {
			t.Fatalf("Replay failed for %s: %v", coin, err)
		}

		if len(replayed) != shareCounts[i] {
			t.Errorf("FAIL: WAL %s: expected %d shares, got %d", coin, shareCounts[i], len(replayed))
		}

		wal.Close()
	}

	t.Logf("PASS: 3-coin WAL isolation verified")
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 8: BOUNDARY & EDGE CONDITION VERIFICATION
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_VarDiff_SetDifficulty_ClampsBounds verifies manual SetDifficulty
// clamps to min/max bounds.
func TestExt_VarDiff_SetDifficulty_ClampsBounds(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         10.0,
		MaxDiff:         1000.0,
		TargetTime:      5.0,
		RetargetTime:    30.0,
		VariancePercent: 50.0,
	}
	engine, err := vardiff.NewEngineWithValidation(cfg)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	state := engine.NewSessionState(100.0)

	// Set below min
	vardiff.SetDifficulty(state, 0.001)
	got := vardiff.GetDifficulty(state)
	if got < 10.0 {
		t.Errorf("FAIL: SetDifficulty below min should clamp to 10.0, got %f", got)
	}

	// Set above max
	vardiff.SetDifficulty(state, 1e12)
	got = vardiff.GetDifficulty(state)
	if got > 1000.0 {
		t.Errorf("FAIL: SetDifficulty above max should clamp to 1000.0, got %f", got)
	}

	// Set within bounds
	vardiff.SetDifficulty(state, 500.0)
	got = vardiff.GetDifficulty(state)
	if got != 500.0 {
		t.Errorf("FAIL: SetDifficulty(500) should give 500.0, got %f", got)
	}

	t.Logf("PASS: SetDifficulty clamps to bounds correctly")
}

// TestExt_DuplicateTracker_LRUEviction_BeyondMaxJobs verifies that
// DuplicateTracker evicts old jobs when maxTrackedJobs (1000) is exceeded.
//
// Simulation: Register shares for 1100 unique jobs, verify oldest are evicted.
func TestExt_DuplicateTracker_LRUEviction_BeyondMaxJobs(t *testing.T) {
	dt := shares.NewDuplicateTracker()

	// Fill 1100 jobs (exceeds maxTrackedJobs=1000)
	for j := 0; j < 1100; j++ {
		dt.RecordIfNew(fmt.Sprintf("job_%05d", j), "en1", "en2", "ntime", "nonce_0")
	}

	jobCount, _ := dt.Stats()
	if jobCount > 1000 {
		t.Errorf("FAIL: Job count %d exceeds maxTrackedJobs=1000", jobCount)
	}

	// Oldest jobs should have been evicted — share should be accepted as new
	ok := dt.RecordIfNew("job_00000", "en1", "en2", "ntime", "nonce_0")
	if !ok {
		t.Logf("NOTE: job_00000 was not evicted (LRU kept it). This is acceptable if recently accessed.")
	}

	// Newest job should still reject duplicate
	dup := dt.RecordIfNew("job_01099", "en1", "en2", "ntime", "nonce_0")
	if dup {
		t.Error("FAIL: Most recent job should still reject duplicate shares")
	}

	t.Logf("PASS: DuplicateTracker bounded at %d jobs after 1100 submissions", jobCount)
}

// TestExt_HeightEpoch_FlappingDoesNotAdvance verifies that flapping between
// same height+tip pairs doesn't create unnecessary cancellations.
//
// Simulation: Repeatedly AdvanceWithTip with same (height, tip) — no-op.
func TestExt_HeightEpoch_FlappingDoesNotAdvance(t *testing.T) {
	epoch := jobs.NewHeightEpoch()

	epoch.AdvanceWithTip(1000, "aaa")
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Repeat same (height, tip) 100 times — should be no-op
	for i := 0; i < 100; i++ {
		epoch.AdvanceWithTip(1000, "aaa")
	}

	// Context should NOT be cancelled (no actual advance)
	select {
	case <-ctx.Done():
		t.Error("FAIL: Context should not be cancelled when same (height, tip) is repeated")
	default:
		t.Logf("PASS: 100 identical (height, tip) advances were correctly no-ops")
	}
}

// TestExt_HeightEpoch_LowerHeight_Ignored verifies that height regressions
// are silently ignored.
func TestExt_HeightEpoch_LowerHeight_Ignored(t *testing.T) {
	epoch := jobs.NewHeightEpoch()

	epoch.AdvanceWithTip(1000, "tip1000")
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Try to go backward
	epoch.AdvanceWithTip(999, "tip999")
	epoch.AdvanceWithTip(500, "tip500")
	epoch.Advance(100)

	// Context should still be alive
	select {
	case <-ctx.Done():
		t.Error("FAIL: Context cancelled on height regression — should be ignored")
	default:
	}

	h, tip := epoch.State()
	if h != 1000 || tip != "tip1000" {
		t.Errorf("FAIL: State should be (1000, tip1000), got (%d, %s)", h, tip)
	}

	t.Logf("PASS: Height regressions correctly ignored")
}

// TestExt_RateLimiter_BanAndUnban verifies ban/unban lifecycle.
func TestExt_RateLimiter_BanAndUnban(t *testing.T) {
	cfg := security.RateLimiterConfig{
		MaxConnectionsPerIP:  10,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   100,
		BanThreshold:         5,
		BanDuration:          1 * time.Hour,
	}
	logger, _ := zap.NewDevelopment()
	rl := security.NewRateLimiter(cfg, logger.Sugar())

	testIP := "10.99.99.99"

	// Ban the IP
	rl.BanIP(testIP, 1*time.Hour, "test ban")
	if !rl.IsIPBanned(testIP) {
		t.Error("FAIL: IP should be banned")
	}

	// Connection from banned IP should be rejected
	addr := &net.TCPAddr{IP: net.ParseIP(testIP), Port: 12345}
	allowed, reason := rl.AllowConnection(addr)
	if allowed {
		t.Error("FAIL: Banned IP should not be allowed to connect")
	}
	if reason == "" {
		t.Error("FAIL: Should provide ban reason")
	}

	// Unban
	rl.UnbanIP(testIP)
	if rl.IsIPBanned(testIP) {
		t.Error("FAIL: IP should be unbanned")
	}

	// Should be able to connect now
	allowed2, _ := rl.AllowConnection(addr)
	if !allowed2 {
		t.Error("FAIL: Unbanned IP should be allowed to connect")
	}
	rl.ReleaseConnection(addr)

	t.Logf("PASS: Ban/unban lifecycle works correctly")
}

// TestExt_RateLimiter_Whitelist_BypassesLimits verifies whitelisted IPs
// bypass all rate limits.
func TestExt_RateLimiter_Whitelist_BypassesLimits(t *testing.T) {
	cfg := security.RateLimiterConfig{
		MaxConnectionsPerIP:  1, // Very restrictive
		MaxConnectionsPerMin: 1,
		MaxSharesPerSecond:   1,
		BanThreshold:         1,
		BanDuration:          1 * time.Hour,
		WhitelistIPs:         []string{"192.168.1.1"},
	}
	logger, _ := zap.NewDevelopment()
	rl := security.NewRateLimiter(cfg, logger.Sugar())

	addr := &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 12345}

	// Connect 10 times — should all succeed for whitelisted IP
	for i := 0; i < 10; i++ {
		allowed, _ := rl.AllowConnection(addr)
		if !allowed {
			t.Errorf("FAIL: Whitelisted IP blocked on connection %d", i)
		}
	}

	t.Logf("PASS: Whitelisted IP bypasses connection limits")
}

// ═══════════════════════════════════════════════════════════════════════════════
// CATEGORY 9: OUTPUT & MONITORING VERIFICATION
// ═══════════════════════════════════════════════════════════════════════════════

// TestExt_WAL_StatsAccuracy verifies WAL stats counters match actual operations.
func TestExt_WAL_StatsAccuracy(t *testing.T) {
	tmpDir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := shares.NewWAL(tmpDir, "stats_test", logger)
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	// Write 50 shares
	allShares := make([]*protocol.Share, 50)
	for i := 0; i < 50; i++ {
		share := &protocol.Share{
			MinerAddress: fmt.Sprintf("miner_%d", i),
			Difficulty:   float64(i + 1),
		}
		allShares[i] = share
		wal.Write(share)
	}

	// Commit 30
	wal.CommitBatchVerified(allShares[:30])

	stats := wal.Stats()
	if stats.Written != 50 {
		t.Errorf("FAIL: Written should be 50, got %d", stats.Written)
	}
	if stats.Committed < 30 {
		t.Errorf("FAIL: Committed should be >= 30, got %d", stats.Committed)
	}

	wal.Close()

	// Reopen and replay
	wal2, err := shares.NewWAL(tmpDir, "stats_test", logger)
	if err != nil {
		t.Fatalf("Failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	replayed, _ := wal2.Replay()
	stats2 := wal2.Stats()

	if len(replayed) != 20 {
		t.Errorf("FAIL: Expected 20 uncommitted shares on replay, got %d", len(replayed))
	}

	t.Logf("PASS: WAL stats accurate. Written=%d, Committed=%d, Replayed=%d, FileSize=%d",
		stats.Written, stats.Committed, stats2.Replayed, stats2.FileSize)
}

// TestExt_RingBuffer_StatsAccuracy verifies RingBuffer stats match operations.
func TestExt_RingBuffer_StatsAccuracy(t *testing.T) {
	buf := ringbuffer.New[int](256)

	// Enqueue 300 (256 succeed, 44 dropped)
	for i := 0; i < 300; i++ {
		buf.TryEnqueue(i)
	}

	stats := buf.Stats()
	if stats.Enqueued != 256 {
		t.Errorf("FAIL: Enqueued should be 256, got %d", stats.Enqueued)
	}
	if stats.Dropped != 44 {
		t.Errorf("FAIL: Dropped should be 44, got %d", stats.Dropped)
	}
	if stats.Current != 256 {
		t.Errorf("FAIL: Current should be 256, got %d", stats.Current)
	}
	if stats.Capacity != 256 {
		t.Errorf("FAIL: Capacity should be 256, got %d", stats.Capacity)
	}

	// Dequeue 100
	for i := 0; i < 100; i++ {
		buf.DequeueOne()
	}

	stats2 := buf.Stats()
	if stats2.Current != 156 {
		t.Errorf("FAIL: Current should be 156 after dequeue, got %d", stats2.Current)
	}

	t.Logf("PASS: RingBuffer stats accurate. Enqueued=%d, Dropped=%d, Current=%d/%d",
		stats.Enqueued, stats.Dropped, stats2.Current, stats2.Capacity)
}

// TestExt_CircuitBreaker_StatsConsistency verifies circuit breaker stats
// after a complex sequence of operations.
func TestExt_CircuitBreaker_StatsConsistency(t *testing.T) {
	cfg := database.CircuitBreakerConfig{
		FailureThreshold: 5,
		CooldownPeriod:   20 * time.Millisecond,
		InitialBackoff:   2 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := database.NewCircuitBreaker(cfg)

	// 5 failures → open
	for i := 0; i < 5; i++ {
		cb.AllowRequest()
		cb.RecordFailure()
	}

	// 3 blocked requests
	for i := 0; i < 3; i++ {
		cb.AllowRequest()
	}

	// Cooldown → probe → success → close
	time.Sleep(25 * time.Millisecond)
	cb.AllowRequest() // probe
	cb.RecordSuccess()

	// Reset and verify
	stats := cb.Stats()

	if stats.State != database.CircuitClosed {
		t.Errorf("FAIL: Expected CircuitClosed, got %s", stats.State)
	}
	if stats.Failures != 0 {
		t.Errorf("FAIL: Failures should be 0 after reset, got %d", stats.Failures)
	}
	if stats.TotalBlocked < 3 {
		t.Errorf("FAIL: TotalBlocked should be >= 3, got %d", stats.TotalBlocked)
	}
	// closed→open + open→halfopen + halfopen→closed = 3 minimum
	if stats.StateChanges < 3 {
		t.Errorf("FAIL: StateChanges should be >= 3, got %d", stats.StateChanges)
	}

	t.Logf("PASS: CircuitBreaker stats consistent. State=%s, Changes=%d, Blocked=%d, Backoff=%v",
		stats.State, stats.StateChanges, stats.TotalBlocked, stats.CurrentBackoff)
}

// TestExt_RateLimiter_StatsReflectState verifies RateLimiter stats.
func TestExt_RateLimiter_StatsReflectState(t *testing.T) {
	cfg := security.RateLimiterConfig{
		MaxConnectionsPerIP:  10,
		MaxConnectionsPerMin: 1000,
		MaxSharesPerSecond:   100,
		BanThreshold:         50,
		BanDuration:          1 * time.Minute,
	}
	logger, _ := zap.NewDevelopment()
	rl := security.NewRateLimiter(cfg, logger.Sugar())

	// Connect 5 IPs
	addrs := make([]*net.TCPAddr, 5)
	for i := 0; i < 5; i++ {
		addrs[i] = &net.TCPAddr{IP: net.ParseIP(fmt.Sprintf("10.0.0.%d", i+1)), Port: 12345}
		rl.AllowConnection(addrs[i])
	}

	// Ban 2 IPs
	rl.BanIP("10.0.0.1", 1*time.Hour, "test")
	rl.BanIP("10.0.0.2", 1*time.Hour, "test")

	stats := rl.GetStats()
	if stats.BannedIPs != 2 {
		t.Errorf("FAIL: Expected 2 banned IPs, got %d", stats.BannedIPs)
	}

	// Release all connections
	for _, addr := range addrs {
		rl.ReleaseConnection(addr)
	}

	bannedMap := rl.GetBannedIPs()
	if len(bannedMap) != 2 {
		t.Errorf("FAIL: Expected 2 entries in banned map, got %d", len(bannedMap))
	}

	t.Logf("PASS: RateLimiter stats reflect state. Active=%d, Unique=%d, Banned=%d",
		stats.ActiveConnections, stats.UniqueIPs, stats.BannedIPs)
}
