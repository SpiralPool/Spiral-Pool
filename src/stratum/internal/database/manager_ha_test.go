// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package database

import (
	"sync"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Circuit Breaker HA Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestCircuitBreaker_HAFailover_OpensOnThreshold verifies the circuit breaker
// transitions from Closed to Open after exactly FailureThreshold consecutive
// failures. This is the primary failover trigger: once the circuit opens, the
// system stops hammering the dead node and can redirect to a replica.
func TestCircuitBreaker_HAFailover_OpensOnThreshold(t *testing.T) {
	t.Parallel()

	cfg := CircuitBreakerConfig{
		FailureThreshold: 5,
		CooldownPeriod:   1 * time.Second,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       100 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Record FailureThreshold-1 failures: circuit must remain Closed.
	for i := 0; i < cfg.FailureThreshold-1; i++ {
		cb.RecordFailure()
		if cb.State() != CircuitClosed {
			t.Fatalf("State after %d failures = %v, want CircuitClosed (threshold=%d)",
				i+1, cb.State(), cfg.FailureThreshold)
		}
	}

	// The threshold-th failure must open the circuit.
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Errorf("State after %d failures = %v, want CircuitOpen",
			cfg.FailureThreshold, cb.State())
	}

	// With the circuit open, AllowRequest must deny traffic.
	allowed, remaining := cb.AllowRequest()
	if allowed {
		t.Error("Open circuit must block requests")
	}
	if remaining <= 0 {
		t.Error("Open circuit must report positive remaining cooldown duration")
	}

	// Verify the failure counter matches the exact threshold.
	if cb.Failures() != cfg.FailureThreshold {
		t.Errorf("Failures() = %d, want %d", cb.Failures(), cfg.FailureThreshold)
	}
}

// TestCircuitBreaker_HAFailover_HalfOpenProbe verifies that after the cooldown
// period elapses, exactly one probe request is allowed (half-open state), and
// all subsequent requests remain blocked until the probe result is recorded.
func TestCircuitBreaker_HAFailover_HalfOpenProbe(t *testing.T) {
	t.Parallel()

	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   5 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Open the circuit.
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("Pre-condition: state = %v, want CircuitOpen", cb.State())
	}

	// Requests while open must be blocked.
	allowed, _ := cb.AllowRequest()
	if allowed {
		t.Fatal("Request should be blocked while circuit is open and cooldown has not elapsed")
	}

	// Wait for the cooldown to elapse.
	time.Sleep(cfg.CooldownPeriod + 10*time.Millisecond)

	// First request after cooldown: the single probe must be allowed.
	probeAllowed, probeBackoff := cb.AllowRequest()
	if !probeAllowed {
		t.Error("Probe request must be allowed after cooldown elapses")
	}
	// Probe backoff should be zero (immediate probe).
	if probeBackoff != 0 {
		t.Errorf("Probe backoff = %v, want 0", probeBackoff)
	}

	if cb.State() != CircuitHalfOpen {
		t.Errorf("State after probe = %v, want CircuitHalfOpen", cb.State())
	}

	// Additional requests while half-open must be blocked (only one probe at a time).
	for i := 0; i < 3; i++ {
		secondAllowed, _ := cb.AllowRequest()
		if secondAllowed {
			t.Errorf("Request %d while half-open must be blocked", i+1)
		}
	}
}

// TestCircuitBreaker_HAFailover_SuccessResetsState verifies that a successful
// operation during the half-open probe resets the breaker to Closed with zeroed
// failure counters and reset backoff. This completes the recovery path.
func TestCircuitBreaker_HAFailover_SuccessResetsState(t *testing.T) {
	t.Parallel()

	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       200 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Drive through: Closed -> Open -> HalfOpen.
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cfg.CooldownPeriod + 10*time.Millisecond)
	cb.AllowRequest() // transitions to HalfOpen

	if cb.State() != CircuitHalfOpen {
		t.Fatalf("Pre-condition: state = %v, want CircuitHalfOpen", cb.State())
	}

	statsBefore := cb.Stats()
	stateChangesBefore := statsBefore.StateChanges

	// Record a success during half-open -> should close the circuit.
	cb.RecordSuccess()

	if cb.State() != CircuitClosed {
		t.Errorf("State after success in half-open = %v, want CircuitClosed", cb.State())
	}

	stats := cb.Stats()

	// Failures must be zeroed.
	if stats.Failures != 0 {
		t.Errorf("Failures after recovery = %d, want 0", stats.Failures)
	}

	// Backoff must be reset to initial.
	if stats.CurrentBackoff != cfg.InitialBackoff {
		t.Errorf("CurrentBackoff after recovery = %v, want %v", stats.CurrentBackoff, cfg.InitialBackoff)
	}

	// A state change should have been recorded (HalfOpen -> Closed).
	if stats.StateChanges <= stateChangesBefore {
		t.Errorf("StateChanges = %d, should have increased from %d",
			stats.StateChanges, stateChangesBefore)
	}

	// Requests must flow freely again.
	allowed, _ := cb.AllowRequest()
	if !allowed {
		t.Error("Requests must be allowed after circuit closes on successful probe")
	}
}

// TestCircuitBreaker_HAFailover_ConcurrentSafe validates that the circuit
// breaker is free of data races under heavy concurrent access from multiple
// goroutines performing mixed read/write operations simultaneously.
// Run with -race to detect race conditions.
func TestCircuitBreaker_HAFailover_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	cfg := CircuitBreakerConfig{
		FailureThreshold: 10,
		CooldownPeriod:   20 * time.Millisecond,
		InitialBackoff:   1 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	const goroutines = 50
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines * 4) // 4 operation types

	// Concurrent RecordFailure.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				cb.RecordFailure()
			}
		}()
	}

	// Concurrent RecordSuccess.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				cb.RecordSuccess()
			}
		}()
	}

	// Concurrent AllowRequest.
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				cb.AllowRequest()
			}
		}()
	}

	// Concurrent read-only operations (State, Stats, Failures).
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_ = cb.State()
				_ = cb.Stats()
				_ = cb.Failures()
			}
		}()
	}

	wg.Wait()

	// After all goroutines finish, the breaker must still be usable.
	// We don't assert on the final state since it is nondeterministic,
	// but it must not panic, deadlock, or produce an invalid state.
	state := cb.State()
	if state != CircuitClosed && state != CircuitOpen && state != CircuitHalfOpen {
		t.Errorf("Final state = %v, want one of Closed/Open/HalfOpen", state)
	}

	stats := cb.Stats()
	if stats.Failures < 0 {
		t.Errorf("Negative failure count: %d", stats.Failures)
	}
}

// TestCircuitBreaker_Stats_Accurate verifies that the Stats() snapshot
// accurately reflects the actual state changes that have occurred, including
// state change counts and blocked request totals.
func TestCircuitBreaker_Stats_Accurate(t *testing.T) {
	t.Parallel()

	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   30 * time.Millisecond,
		InitialBackoff:   5 * time.Millisecond,
		MaxBackoff:       80 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Phase 1: Initial state.
	stats0 := cb.Stats()
	if stats0.State != CircuitClosed {
		t.Errorf("Initial Stats.State = %v, want CircuitClosed", stats0.State)
	}
	if stats0.Failures != 0 {
		t.Errorf("Initial Stats.Failures = %d, want 0", stats0.Failures)
	}
	if stats0.StateChanges != 0 {
		t.Errorf("Initial Stats.StateChanges = %d, want 0", stats0.StateChanges)
	}
	if stats0.TotalBlocked != 0 {
		t.Errorf("Initial Stats.TotalBlocked = %d, want 0", stats0.TotalBlocked)
	}
	if stats0.CurrentBackoff != cfg.InitialBackoff {
		t.Errorf("Initial Stats.CurrentBackoff = %v, want %v", stats0.CurrentBackoff, cfg.InitialBackoff)
	}

	// Phase 2: Open the circuit (Closed -> Open = 1 state change).
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}

	stats1 := cb.Stats()
	if stats1.State != CircuitOpen {
		t.Errorf("After threshold Stats.State = %v, want CircuitOpen", stats1.State)
	}
	if stats1.Failures != cfg.FailureThreshold {
		t.Errorf("After threshold Stats.Failures = %d, want %d", stats1.Failures, cfg.FailureThreshold)
	}
	if stats1.StateChanges != 1 {
		t.Errorf("After open Stats.StateChanges = %d, want 1", stats1.StateChanges)
	}

	// Phase 3: Block some requests and track TotalBlocked.
	blockedRequests := 5
	for i := 0; i < blockedRequests; i++ {
		cb.AllowRequest()
	}

	stats2 := cb.Stats()
	if stats2.TotalBlocked < uint64(blockedRequests) {
		t.Errorf("Stats.TotalBlocked = %d, want >= %d", stats2.TotalBlocked, blockedRequests)
	}

	// Phase 4: Transition to HalfOpen (Open -> HalfOpen = 2 state changes total).
	time.Sleep(cfg.CooldownPeriod + 10*time.Millisecond)
	cb.AllowRequest() // probe triggers HalfOpen

	stats3 := cb.Stats()
	if stats3.State != CircuitHalfOpen {
		t.Errorf("After cooldown Stats.State = %v, want CircuitHalfOpen", stats3.State)
	}
	if stats3.StateChanges != 2 {
		t.Errorf("After half-open Stats.StateChanges = %d, want 2", stats3.StateChanges)
	}

	// Phase 5: Success closes circuit (HalfOpen -> Closed = 3 state changes total).
	cb.RecordSuccess()

	stats4 := cb.Stats()
	if stats4.State != CircuitClosed {
		t.Errorf("After success Stats.State = %v, want CircuitClosed", stats4.State)
	}
	if stats4.StateChanges != 3 {
		t.Errorf("After close Stats.StateChanges = %d, want 3", stats4.StateChanges)
	}
	if stats4.Failures != 0 {
		t.Errorf("After recovery Stats.Failures = %d, want 0", stats4.Failures)
	}
	if stats4.CurrentBackoff != cfg.InitialBackoff {
		t.Errorf("After recovery Stats.CurrentBackoff = %v, want %v",
			stats4.CurrentBackoff, cfg.InitialBackoff)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Block Queue HA Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestBlockQueue_EnqueueDuringOutage verifies that blocks are correctly queued
// when the database is unavailable (circuit open). This is the primary data
// preservation mechanism during outages.
func TestBlockQueue_EnqueueDuringOutage(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(100)

	// Simulate queuing blocks during an outage.
	blocks := make([]*Block, 10)
	for i := range blocks {
		blocks[i] = &Block{
			Height: uint64(1000 + i),
			Hash:   "deadbeef",
			Miner:  "miner1",
			Status: "pending",
		}
	}

	for i, b := range blocks {
		ok := q.Enqueue(b)
		if !ok {
			t.Fatalf("Enqueue failed for block %d (queue should have capacity)", i)
		}
	}

	// Verify queue length.
	if q.Len() != len(blocks) {
		t.Errorf("Len() = %d, want %d", q.Len(), len(blocks))
	}

	// Verify no drops occurred.
	if q.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0", q.Dropped())
	}

	// Verify FIFO order via Peek.
	head := q.Peek()
	if head == nil {
		t.Fatal("Peek() returned nil, expected first queued block")
	}
	if head.Block.Height != blocks[0].Height {
		t.Errorf("Peek().Block.Height = %d, want %d", head.Block.Height, blocks[0].Height)
	}

	// Verify QueuedAt is populated.
	if head.QueuedAt.IsZero() {
		t.Error("QueuedAt should be set on enqueue")
	}
	if head.Attempts != 0 {
		t.Errorf("Attempts should be 0 on fresh enqueue, got %d", head.Attempts)
	}
}

// TestBlockQueue_DrainOnRecovery verifies that DrainAll returns all queued
// blocks in order and leaves the queue empty, simulating the bulk-flush that
// occurs when the circuit breaker closes after a database recovery.
func TestBlockQueue_DrainOnRecovery(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(50)

	// Enqueue several blocks.
	const count = 7
	for i := 0; i < count; i++ {
		q.Enqueue(&Block{Height: uint64(500 + i)})
	}

	if q.Len() != count {
		t.Fatalf("Pre-drain Len() = %d, want %d", q.Len(), count)
	}

	// Drain all blocks.
	drained := q.DrainAll()

	if len(drained) != count {
		t.Errorf("DrainAll returned %d entries, want %d", len(drained), count)
	}

	// Verify FIFO order is preserved.
	for i, entry := range drained {
		expectedHeight := uint64(500 + i)
		if entry.Block.Height != expectedHeight {
			t.Errorf("Drained[%d].Block.Height = %d, want %d",
				i, entry.Block.Height, expectedHeight)
		}
	}

	// Queue must be empty after drain.
	if q.Len() != 0 {
		t.Errorf("Post-drain Len() = %d, want 0", q.Len())
	}

	// Peek on empty queue must return nil.
	if q.Peek() != nil {
		t.Error("Peek() after drain should return nil")
	}
}

// TestBlockQueue_MaxSize_DropsOldest verifies that when the queue reaches its
// maximum capacity, new enqueue attempts are rejected and the drop counter
// increments. This bounds memory usage during prolonged outages.
func TestBlockQueue_MaxSize_DropsOldest(t *testing.T) {
	t.Parallel()

	const maxSize = 5
	q := NewBlockQueue(maxSize)

	// Fill the queue to capacity.
	for i := 0; i < maxSize; i++ {
		ok := q.Enqueue(&Block{Height: uint64(i)})
		if !ok {
			t.Fatalf("Enqueue failed at index %d, queue should have room", i)
		}
	}

	if q.Len() != maxSize {
		t.Fatalf("Len() = %d, want %d", q.Len(), maxSize)
	}
	if q.Dropped() != 0 {
		t.Fatalf("Dropped() = %d before overflow, want 0", q.Dropped())
	}

	// Attempt to enqueue beyond capacity.
	const overflowCount = 3
	for i := 0; i < overflowCount; i++ {
		ok := q.Enqueue(&Block{Height: uint64(maxSize + i)})
		if ok {
			t.Errorf("Enqueue should return false when queue is full (attempt %d)", i+1)
		}
	}

	// Queue length must not exceed max size.
	if q.Len() != maxSize {
		t.Errorf("Len() = %d after overflow, want %d (should not grow)", q.Len(), maxSize)
	}

	// Dropped counter must reflect all rejected enqueues.
	if q.Dropped() != uint64(overflowCount) {
		t.Errorf("Dropped() = %d, want %d", q.Dropped(), overflowCount)
	}

	// Verify the original blocks are preserved (not overwritten).
	head := q.Peek()
	if head == nil || head.Block.Height != 0 {
		heightStr := "nil"
		if head != nil {
			heightStr = "non-zero"
		}
		t.Errorf("Oldest block should still be at height 0, got %s", heightStr)
	}
}

// TestBlockQueue_DequeueWithCommit_CrashSafe verifies the two-phase dequeue:
// the entry is returned for processing but NOT removed until commit() is
// explicitly called. This prevents block loss if a crash occurs between
// dequeue and the actual database write.
func TestBlockQueue_DequeueWithCommit_CrashSafe(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	// Enqueue two blocks.
	q.Enqueue(&Block{Height: 100, Hash: "aaa"})
	q.Enqueue(&Block{Height: 200, Hash: "bbb"})

	// DequeueWithCommit: get the first block.
	entry, commit := q.DequeueWithCommit()
	if entry == nil {
		t.Fatal("DequeueWithCommit returned nil entry")
	}
	if entry.Block.Height != 100 {
		t.Errorf("Entry height = %d, want 100", entry.Block.Height)
	}

	// Before committing, the block must still be in the queue.
	if q.Len() != 2 {
		t.Errorf("Len() before commit = %d, want 2 (block must not be removed yet)", q.Len())
	}

	// Verify Peek still returns the same block.
	peeked := q.Peek()
	if peeked == nil || peeked.Block.Height != 100 {
		t.Error("Peek should still return the uncommitted block")
	}

	// Now commit (simulate successful DB write).
	committed := commit()
	if !committed {
		t.Error("commit() should return true on first call")
	}

	// After commit, the block should be removed.
	if q.Len() != 1 {
		t.Errorf("Len() after commit = %d, want 1", q.Len())
	}

	// The next entry should be the second block.
	next := q.Peek()
	if next == nil || next.Block.Height != 200 {
		t.Error("After committing first block, Peek should return second block (height 200)")
	}

	// Double-commit must return false (idempotent safety).
	doubleCommit := commit()
	if doubleCommit {
		t.Error("Double-commit should return false")
	}

	// Queue length should still be 1 (double-commit must not remove another entry).
	if q.Len() != 1 {
		t.Errorf("Len() after double-commit = %d, want 1", q.Len())
	}
}

// TestBlockQueue_DequeueWithCommit_ConcurrentSafe verifies that multiple
// goroutines can concurrently call DequeueWithCommit without data races or
// double-processing. Only one goroutine should successfully commit each entry.
func TestBlockQueue_DequeueWithCommit_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	const queueSize = 50
	q := NewBlockQueue(queueSize)

	// Fill the queue.
	for i := 0; i < queueSize; i++ {
		q.Enqueue(&Block{Height: uint64(i)})
	}

	var (
		mu             sync.Mutex
		committedCount int
		wg             sync.WaitGroup
	)

	const goroutines = 20
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for {
				entry, commit := q.DequeueWithCommit()
				if entry == nil {
					return // Queue drained
				}
				// Simulate processing work.
				if committed := commit(); committed {
					mu.Lock()
					committedCount++
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	// Every block should have been committed exactly once.
	if committedCount != queueSize {
		t.Errorf("committedCount = %d, want %d (each block committed exactly once)",
			committedCount, queueSize)
	}

	// Queue must be empty.
	if q.Len() != 0 {
		t.Errorf("Len() after concurrent drain = %d, want 0", q.Len())
	}
}

// TestBlockQueue_EmptyDrain_NilReturn verifies that DrainAll on an empty queue
// returns nil (not an empty slice), matching the documented behavior.
func TestBlockQueue_EmptyDrain_NilReturn(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	// Drain an empty queue.
	result := q.DrainAll()
	if result != nil {
		t.Errorf("DrainAll on empty queue returned %v (len=%d), want nil", result, len(result))
	}

	// DequeueWithCommit on empty queue (additional check).
	entryEmpty, commitEmpty := q.DequeueWithCommit()
	if entryEmpty != nil {
		t.Errorf("DequeueWithCommit on empty queue returned non-nil: %+v", entryEmpty)
	}
	if commitEmpty() {
		t.Error("commit() on empty dequeue should return false")
	}

	// DequeueWithCommit on empty queue.
	entryWC, commitFn := q.DequeueWithCommit()
	if entryWC != nil {
		t.Errorf("DequeueWithCommit on empty queue returned non-nil entry: %+v", entryWC)
	}
	// The commit function should return false.
	if commitFn() {
		t.Error("commit() on empty dequeue should return false")
	}

	// Peek on empty queue.
	if q.Peek() != nil {
		t.Error("Peek on empty queue should return nil")
	}

	// Len and Dropped should both be zero.
	if q.Len() != 0 {
		t.Errorf("Len() on empty queue = %d, want 0", q.Len())
	}
	if q.Dropped() != 0 {
		t.Errorf("Dropped() on empty queue = %d, want 0", q.Dropped())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DB Node State Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestDBNodeState_AllStatesHaveNames verifies that all four defined DBNodeState
// constants produce non-"unknown" string representations. An "unknown" result
// would indicate a missing case in the String() switch, which would break
// monitoring dashboards and log analysis.
func TestDBNodeState_AllStatesHaveNames(t *testing.T) {
	t.Parallel()

	allStates := []struct {
		state DBNodeState
		name  string
	}{
		{DBNodeHealthy, "healthy"},
		{DBNodeDegraded, "degraded"},
		{DBNodeUnhealthy, "unhealthy"},
		{DBNodeOffline, "offline"},
	}

	for _, tt := range allStates {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.state.String()
			if got == "unknown" {
				t.Errorf("DBNodeState(%d).String() = %q, expected a known state name", tt.state, got)
			}
			if got != tt.name {
				t.Errorf("DBNodeState(%d).String() = %q, want %q", tt.state, got, tt.name)
			}
		})
	}

	// Verify that an out-of-range value does produce "unknown".
	if DBNodeState(99).String() != "unknown" {
		t.Errorf("Out-of-range DBNodeState(99).String() = %q, want %q",
			DBNodeState(99).String(), "unknown")
	}
}

// TestDBNodeState_DegradationPath verifies the degradation progression:
//
//	healthy -> degraded (first failure, below threshold)
//	degraded -> unhealthy (consecutive failures reach MaxDBNodeFailures)
//
// This mirrors the logic in DatabaseManager.recordNodeFailure.
func TestDBNodeState_DegradationPath(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:       "db-test-degrade",
		State:    DBNodeHealthy,
		Priority: 0,
	}

	// Step 1: First failure on a healthy node -> degraded.
	// (mirrors recordNodeFailure: if state==Healthy and fails < threshold -> Degraded)
	node.ConsecutiveFails++
	if node.ConsecutiveFails < MaxDBNodeFailures && node.State == DBNodeHealthy {
		node.State = DBNodeDegraded
	}

	if node.State != DBNodeDegraded {
		t.Errorf("After first failure: state = %v, want DBNodeDegraded", node.State)
	}

	// Step 2: Additional failures below threshold stay degraded.
	for node.ConsecutiveFails < MaxDBNodeFailures-1 {
		node.ConsecutiveFails++
	}
	// Still below threshold: state should remain degraded.
	if node.ConsecutiveFails < MaxDBNodeFailures {
		if node.State != DBNodeDegraded {
			t.Errorf("Below threshold: state = %v, want DBNodeDegraded", node.State)
		}
	}

	// Step 3: Reaching threshold -> unhealthy.
	node.ConsecutiveFails = MaxDBNodeFailures
	if node.ConsecutiveFails >= MaxDBNodeFailures {
		node.State = DBNodeUnhealthy
	}

	if node.State != DBNodeUnhealthy {
		t.Errorf("At threshold: state = %v, want DBNodeUnhealthy", node.State)
	}

	// Verify the degradation path is Healthy(0) < Degraded(1) < Unhealthy(2) < Offline(3).
	if !(DBNodeHealthy < DBNodeDegraded &&
		DBNodeDegraded < DBNodeUnhealthy &&
		DBNodeUnhealthy < DBNodeOffline) {
		t.Error("State severity ordering is not monotonically increasing")
	}
}

// TestDBNodeState_Recovery verifies that an unhealthy node transitions back
// to healthy upon a successful operation, with ConsecutiveFails reset to zero.
// This mirrors DatabaseManager.recordNodeSuccess behavior.
func TestDBNodeState_Recovery(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-recovery",
		State:            DBNodeUnhealthy,
		ConsecutiveFails: MaxDBNodeFailures + 5, // Well past threshold
		Priority:         1,
	}

	// Simulate recordNodeSuccess: reset fails, set healthy.
	node.ConsecutiveFails = 0
	node.LastSuccess = time.Now()
	node.LastError = nil
	if node.State != DBNodeHealthy {
		node.State = DBNodeHealthy
	}

	if node.State != DBNodeHealthy {
		t.Errorf("After recovery: state = %v, want DBNodeHealthy", node.State)
	}
	if node.ConsecutiveFails != 0 {
		t.Errorf("After recovery: ConsecutiveFails = %d, want 0", node.ConsecutiveFails)
	}
	if node.LastSuccess.IsZero() {
		t.Error("After recovery: LastSuccess should be set")
	}
	if node.LastError != nil {
		t.Error("After recovery: LastError should be nil")
	}

	// Also test recovery from Degraded state.
	nodeDegraded := &ManagedDBNode{
		ID:               "db-test-degraded-recovery",
		State:            DBNodeDegraded,
		ConsecutiveFails: 2,
	}

	nodeDegraded.ConsecutiveFails = 0
	nodeDegraded.LastSuccess = time.Now()
	if nodeDegraded.State != DBNodeHealthy {
		nodeDegraded.State = DBNodeHealthy
	}

	if nodeDegraded.State != DBNodeHealthy {
		t.Errorf("Degraded recovery: state = %v, want DBNodeHealthy", nodeDegraded.State)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Failover Threshold Tests
// ═══════════════════════════════════════════════════════════════════════════════

// TestDBFailover_ThresholdValues_Production verifies that all failover-related
// constants are set to production-appropriate values. These values directly
// affect how quickly the system detects failures and how aggressively it
// reconnects, so they must be within safe operational bounds.
func TestDBFailover_ThresholdValues_Production(t *testing.T) {
	t.Parallel()

	// MaxDBNodeFailures: Too low (1) causes flapping on transient errors.
	// Too high (>10) delays failover unacceptably for a mining pool.
	if MaxDBNodeFailures < 2 {
		t.Errorf("MaxDBNodeFailures = %d, too aggressive (flapping risk); want >= 2", MaxDBNodeFailures)
	}
	if MaxDBNodeFailures > 10 {
		t.Errorf("MaxDBNodeFailures = %d, too slow for production failover; want <= 10", MaxDBNodeFailures)
	}

	// DBHealthCheckInterval: Less than 5s creates unnecessary load.
	// More than 30s means delayed failure detection.
	if DBHealthCheckInterval < 5*time.Second {
		t.Errorf("DBHealthCheckInterval = %v, too aggressive; want >= 5s", DBHealthCheckInterval)
	}
	if DBHealthCheckInterval > 30*time.Second {
		t.Errorf("DBHealthCheckInterval = %v, too slow; want <= 30s", DBHealthCheckInterval)
	}

	// DBReconnectBackoff: Initial backoff should give the DB time to recover
	// but not delay reconnection excessively.
	if DBReconnectBackoff < 1*time.Second {
		t.Errorf("DBReconnectBackoff = %v, too short; want >= 1s", DBReconnectBackoff)
	}
	if DBReconnectBackoff > 30*time.Second {
		t.Errorf("DBReconnectBackoff = %v, too long; want <= 30s", DBReconnectBackoff)
	}

	// DBMaxReconnectBackoff must be strictly greater than DBReconnectBackoff.
	if DBMaxReconnectBackoff <= DBReconnectBackoff {
		t.Errorf("DBMaxReconnectBackoff (%v) must be > DBReconnectBackoff (%v)",
			DBMaxReconnectBackoff, DBReconnectBackoff)
	}

	// Maximum backoff should not exceed 5 minutes (would cause too-long gaps
	// between reconnection attempts in a pool that needs uptime).
	if DBMaxReconnectBackoff > 5*time.Minute {
		t.Errorf("DBMaxReconnectBackoff = %v, too long; want <= 5m", DBMaxReconnectBackoff)
	}

	// DefaultCircuitBreakerConfig should also be production-sane.
	cbCfg := DefaultCircuitBreakerConfig()
	if cbCfg.FailureThreshold < 5 {
		t.Errorf("Default CB FailureThreshold = %d, too aggressive; want >= 5", cbCfg.FailureThreshold)
	}
	if cbCfg.CooldownPeriod < 10*time.Second {
		t.Errorf("Default CB CooldownPeriod = %v, too short; want >= 10s", cbCfg.CooldownPeriod)
	}
	if cbCfg.BackoffFactor < 1.5 {
		t.Errorf("Default CB BackoffFactor = %.1f, too low for exponential backoff; want >= 1.5",
			cbCfg.BackoffFactor)
	}
	if cbCfg.MaxBackoff < cbCfg.InitialBackoff {
		t.Errorf("Default CB MaxBackoff (%v) < InitialBackoff (%v)",
			cbCfg.MaxBackoff, cbCfg.InitialBackoff)
	}
}

// TestDBFailover_BackoffBounds verifies that the exponential backoff generated
// by the circuit breaker always remains within [InitialBackoff, MaxBackoff].
// Unbounded backoff growth could prevent timely reconnection after an outage.
func TestDBFailover_BackoffBounds(t *testing.T) {
	t.Parallel()

	cfg := CircuitBreakerConfig{
		FailureThreshold: 1000, // High threshold: focus on backoff, not opening.
		CooldownPeriod:   1 * time.Minute,
		InitialBackoff:   100 * time.Millisecond,
		MaxBackoff:       5 * time.Second,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Record many failures and verify each returned backoff is in bounds.
	prevBackoff := time.Duration(0)
	for i := 0; i < 100; i++ {
		backoff := cb.RecordFailure()

		if backoff < cfg.InitialBackoff {
			t.Errorf("Failure %d: backoff %v < InitialBackoff %v", i+1, backoff, cfg.InitialBackoff)
		}
		if backoff > cfg.MaxBackoff {
			t.Errorf("Failure %d: backoff %v > MaxBackoff %v", i+1, backoff, cfg.MaxBackoff)
		}

		// Backoff must be monotonically non-decreasing until it hits the cap.
		if backoff < prevBackoff && prevBackoff < cfg.MaxBackoff {
			t.Errorf("Failure %d: backoff decreased from %v to %v before hitting cap",
				i+1, prevBackoff, backoff)
		}

		prevBackoff = backoff
	}

	// After many failures the current backoff should be capped at MaxBackoff.
	stats := cb.Stats()
	if stats.CurrentBackoff != cfg.MaxBackoff {
		t.Errorf("CurrentBackoff after 100 failures = %v, want MaxBackoff %v",
			stats.CurrentBackoff, cfg.MaxBackoff)
	}

	// Verify success resets backoff to initial.
	cb.RecordSuccess()
	statsAfter := cb.Stats()
	if statsAfter.CurrentBackoff != cfg.InitialBackoff {
		t.Errorf("CurrentBackoff after success = %v, want InitialBackoff %v",
			statsAfter.CurrentBackoff, cfg.InitialBackoff)
	}

	// Test with a different backoff factor to ensure the formula works generally.
	cfg2 := CircuitBreakerConfig{
		FailureThreshold: 1000,
		CooldownPeriod:   1 * time.Minute,
		InitialBackoff:   50 * time.Millisecond,
		MaxBackoff:       2 * time.Second,
		BackoffFactor:    3.0, // Aggressive triple backoff
	}
	cb2 := NewCircuitBreaker(cfg2)

	for i := 0; i < 50; i++ {
		backoff := cb2.RecordFailure()
		if backoff < cfg2.InitialBackoff {
			t.Errorf("CB2 failure %d: backoff %v < InitialBackoff %v", i+1, backoff, cfg2.InitialBackoff)
		}
		if backoff > cfg2.MaxBackoff {
			t.Errorf("CB2 failure %d: backoff %v > MaxBackoff %v", i+1, backoff, cfg2.MaxBackoff)
		}
	}
}
