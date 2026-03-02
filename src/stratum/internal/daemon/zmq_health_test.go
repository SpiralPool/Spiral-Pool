// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build !nozmq

// Package daemon - Test coverage audit item #10: ZMQ health monitoring tests.
//
// These tests exercise the health monitoring subsystem of ZMQListener:
//   - isDuplicateBlock() ring buffer behavior and cache eviction
//   - Health status transitions via recordSuccess/recordFailure/checkHealth
//   - Stats() counter accuracy
//   - Status() accessor consistency
//
// Note: These tests operate on the ZMQListener struct directly without
// establishing real ZMQ connections. The health monitoring logic uses
// atomic fields and is fully testable in isolation.
package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// =============================================================================
// HELPERS
// =============================================================================

// newHealthTestListener creates a ZMQListener suitable for health monitoring
// tests. It has a valid config but does NOT start a real ZMQ connection.
// The running flag is set to true so that setStatus callbacks fire normally.
func newHealthTestListener(t *testing.T) *ZMQListener {
	t.Helper()
	logger := zap.NewNop()
	cfg := &config.ZMQConfig{
		Enabled:             true,
		Endpoint:            "tcp://127.0.0.1:28332",
		FailureThreshold:    1 * time.Second,  // short for tests
		StabilityPeriod:     1 * time.Second,   // short for tests
		HealthCheckInterval: 100 * time.Millisecond,
	}
	z := NewZMQListener(cfg, logger)
	z.running.Store(true)
	z.stopCh = make(chan struct{})
	return z
}

// =============================================================================
// PART 1: isDuplicateBlock() — ring buffer duplicate detection
// =============================================================================

// TestIsDuplicateBlock_FirstOccurrence verifies that the first time a hash
// is checked it is NOT flagged as a duplicate.
func TestIsDuplicateBlock_FirstOccurrence(t *testing.T) {
	t.Parallel()
	z := &ZMQListener{}

	hash := "0000000000000000000000000000000000000000000000000000000000000001"
	if z.isDuplicateBlock(hash) {
		t.Error("first occurrence of a hash should NOT be flagged as duplicate")
	}
}

// TestIsDuplicateBlock_SameHashAgain verifies that the same hash seen twice
// in succession IS flagged as a duplicate on the second call.
func TestIsDuplicateBlock_SameHashAgain(t *testing.T) {
	t.Parallel()
	z := &ZMQListener{}

	hash := "0000000000000000000000000000000000000000000000000000000000000002"
	z.isDuplicateBlock(hash) // first: stores it
	if !z.isDuplicateBlock(hash) {
		t.Error("second occurrence of the same hash should be flagged as duplicate")
	}
}

// TestIsDuplicateBlock_DifferentHash verifies that a different hash is not
// confused with a previously stored one.
func TestIsDuplicateBlock_DifferentHash(t *testing.T) {
	t.Parallel()
	z := &ZMQListener{}

	hashA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	z.isDuplicateBlock(hashA)
	if z.isDuplicateBlock(hashB) {
		t.Error("a different hash should NOT be flagged as duplicate")
	}
}

// TestIsDuplicateBlock_CacheEviction verifies that after filling the ring
// buffer (size 8), the oldest entry is evicted and no longer detected as
// a duplicate.
func TestIsDuplicateBlock_CacheEviction(t *testing.T) {
	t.Parallel()
	z := &ZMQListener{}

	// The ring buffer has 8 slots (recentBlockHashes [8]string).
	const ringSize = 8

	// Insert ringSize+1 unique hashes so that the first one is evicted.
	hashes := make([]string, ringSize+1)
	for i := range hashes {
		hashes[i] = padHash(i)
	}

	for _, h := range hashes {
		if z.isDuplicateBlock(h) {
			t.Fatalf("first insertion of %s should not be a duplicate", h[:16])
		}
	}

	// The first hash was at index 0; after 9 inserts the write pointer
	// wrapped and overwrote index 0. The first hash should be evicted.
	if z.isDuplicateBlock(hashes[0]) {
		t.Error("first hash should have been evicted from the ring buffer")
	}

	// A hash that was NOT evicted should still be detected. After the
	// 9 inserts above, index 0 holds hash[8] and the pointer is at 1.
	// After the re-insert of hash[0] above, index 1 holds hash[0] and
	// pointer is at 2. hash[2] still occupies index 2 and should be detected.
	if !z.isDuplicateBlock(hashes[2]) {
		t.Error("non-evicted hash should still be detected as duplicate")
	}
}

// TestIsDuplicateBlock_FullCycleReplacement fills the buffer twice over
// and verifies all old entries are evicted.
func TestIsDuplicateBlock_FullCycleReplacement(t *testing.T) {
	t.Parallel()
	z := &ZMQListener{}

	const ringSize = 8

	// Fill the buffer with one set of hashes
	firstBatch := make([]string, ringSize)
	for i := range firstBatch {
		firstBatch[i] = padHash(100 + i)
		z.isDuplicateBlock(firstBatch[i])
	}

	// Overwrite the entire buffer with a second set
	secondBatch := make([]string, ringSize)
	for i := range secondBatch {
		secondBatch[i] = padHash(200 + i)
		z.isDuplicateBlock(secondBatch[i])
	}

	// All first-batch hashes should be gone
	for _, h := range firstBatch {
		if z.isDuplicateBlock(h) {
			t.Errorf("hash %s from first batch should have been evicted", h[:16])
		}
	}
}

// padHash creates a zero-padded 64-char hex string from an integer value.
func padHash(n int) string {
	const hexDigits = "0123456789abcdef"
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = '0'
	}
	// Write the number as hex in the trailing bytes
	pos := 63
	val := n
	if val == 0 {
		buf[pos] = '0'
	} else {
		for val > 0 && pos >= 0 {
			buf[pos] = hexDigits[val%16]
			val /= 16
			pos--
		}
	}
	return string(buf)
}

// =============================================================================
// PART 2: Health status transitions
// =============================================================================

// TestHealthTransition_SuccessLeadsToHealthy verifies that calling
// recordSuccess() transitions the listener to ZMQStatusHealthy.
func TestHealthTransition_SuccessLeadsToHealthy(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Starts in Disabled (from NewZMQListener)
	if z.Status() != ZMQStatusDisabled {
		t.Fatalf("initial status = %v, want Disabled", z.Status())
	}

	// Record a success -- should move to Healthy
	z.recordSuccess()
	if z.Status() != ZMQStatusHealthy {
		t.Errorf("after recordSuccess: status = %v, want Healthy", z.Status())
	}
}

// TestHealthTransition_FailureLeadsToDegraded verifies that calling
// recordFailure() transitions the listener to ZMQStatusDegraded.
func TestHealthTransition_FailureLeadsToDegraded(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Start healthy first
	z.recordSuccess()
	if z.Status() != ZMQStatusHealthy {
		t.Fatalf("precondition: status = %v, want Healthy", z.Status())
	}

	// Record a failure -- should move to Degraded
	z.recordFailure()
	if z.Status() != ZMQStatusDegraded {
		t.Errorf("after recordFailure: status = %v, want Degraded", z.Status())
	}
}

// TestHealthTransition_ProlongedFailureLeadsToFailed verifies that when the
// failure duration exceeds the configured threshold, checkHealth() transitions
// the status to ZMQStatusFailed.
func TestHealthTransition_ProlongedFailureLeadsToFailed(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Configure a very short failure threshold
	z.cfg.FailureThreshold = 50 * time.Millisecond

	// Record a failure (sets failureStartTime and status to Degraded)
	z.recordFailure()
	if z.Status() != ZMQStatusDegraded {
		t.Fatalf("after recordFailure: status = %v, want Degraded", z.Status())
	}

	// Wait for the failure threshold to elapse
	time.Sleep(100 * time.Millisecond)

	// checkHealth should transition to Failed
	z.checkHealth()
	if z.Status() != ZMQStatusFailed {
		t.Errorf("after checkHealth (failure > threshold): status = %v, want Failed", z.Status())
	}
}

// TestHealthTransition_RecoveryFromDegraded verifies that a success after
// degraded state returns to Healthy.
func TestHealthTransition_RecoveryFromDegraded(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Go Healthy -> Degraded -> Healthy
	z.recordSuccess()
	z.recordFailure()
	if z.Status() != ZMQStatusDegraded {
		t.Fatalf("precondition: status = %v, want Degraded", z.Status())
	}

	z.recordSuccess()
	if z.Status() != ZMQStatusHealthy {
		t.Errorf("after recovery: status = %v, want Healthy", z.Status())
	}
}

// TestHealthTransition_RecoveryFromFailed verifies that recordSuccess()
// after a Failed state transitions back to Healthy and notifies the
// fallback handler.
func TestHealthTransition_RecoveryFromFailed(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Force into Failed state
	z.status.Store(int32(ZMQStatusFailed))
	z.failureStartTime.Store(time.Now().Add(-1 * time.Minute).Unix())

	var fallbackCalled bool
	z.SetFallbackHandler(func(usePoll bool) {
		if !usePoll {
			fallbackCalled = true
		}
	})

	// Recover
	z.recordSuccess()
	if z.Status() != ZMQStatusHealthy {
		t.Errorf("after recovery from Failed: status = %v, want Healthy", z.Status())
	}
	if !fallbackCalled {
		t.Error("fallback handler should have been called with usePoll=false on recovery from Failed")
	}
}

// TestHealthTransition_StatusChangeCallback verifies that the status change
// callback fires on transitions and does NOT fire on no-op transitions.
func TestHealthTransition_StatusChangeCallback(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	var mu sync.Mutex
	var transitions []ZMQStatus
	z.SetStatusChangeHandler(func(status ZMQStatus) {
		mu.Lock()
		transitions = append(transitions, status)
		mu.Unlock()
	})

	z.setStatus(ZMQStatusConnecting)
	z.setStatus(ZMQStatusHealthy)
	z.setStatus(ZMQStatusHealthy) // same -- should NOT fire
	z.setStatus(ZMQStatusDegraded)

	mu.Lock()
	defer mu.Unlock()

	// Expect 3 transitions: Connecting, Healthy, Degraded
	// The duplicate Healthy->Healthy should be suppressed.
	if len(transitions) != 3 {
		t.Errorf("expected 3 status change callbacks, got %d: %v", len(transitions), transitions)
	}
	expected := []ZMQStatus{ZMQStatusConnecting, ZMQStatusHealthy, ZMQStatusDegraded}
	for i, want := range expected {
		if i < len(transitions) && transitions[i] != want {
			t.Errorf("transition[%d] = %v, want %v", i, transitions[i], want)
		}
	}
}

// TestHealthTransition_StabilityReached verifies that after the stability
// period elapses with no failures, checkHealth() sets stabilityReached and
// calls the fallback handler with usePoll=false.
func TestHealthTransition_StabilityReached(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Configure short stability period
	z.cfg.StabilityPeriod = 50 * time.Millisecond

	// Record success to start the healthy period
	z.recordSuccess()

	var fallbackCalled bool
	z.SetFallbackHandler(func(usePoll bool) {
		if !usePoll {
			fallbackCalled = true
		}
	})

	// Wait for stability period
	time.Sleep(100 * time.Millisecond)

	z.checkHealth()
	if !z.stabilityReached.Load() {
		t.Error("stabilityReached should be true after stability period")
	}
	if !fallbackCalled {
		t.Error("fallback handler should have been called with usePoll=false when stability reached")
	}
}

// TestHealthTransition_FailureResetsStability verifies that a failure resets
// the stability tracking (healthyStartTime set to 0).
func TestHealthTransition_FailureResetsStability(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Start healthy
	z.recordSuccess()
	if z.healthyStartTime.Load() == 0 {
		t.Fatal("healthyStartTime should be set after recordSuccess")
	}

	// Failure should clear healthy tracking
	z.recordFailure()
	if z.healthyStartTime.Load() != 0 {
		t.Error("healthyStartTime should be reset to 0 after recordFailure")
	}
}

// TestHealthTransition_CheckHealthNoFailureIsNoop verifies that checkHealth()
// does nothing when there are no active failures.
func TestHealthTransition_CheckHealthNoFailureIsNoop(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	z.recordSuccess() // Healthy, no failures
	status := z.Status()

	z.checkHealth()
	if z.Status() != status {
		t.Errorf("checkHealth with no failures should not change status: was %v, now %v", status, z.Status())
	}
}

// TestHealthTransition_FallbackOnFailed verifies the fallback handler is
// called with usePoll=true when checkHealth transitions to Failed.
func TestHealthTransition_FallbackOnFailed(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)
	z.cfg.FailureThreshold = 10 * time.Millisecond

	var usePollValue bool
	var called bool
	z.SetFallbackHandler(func(usePoll bool) {
		called = true
		usePollValue = usePoll
	})

	z.recordFailure()
	time.Sleep(50 * time.Millisecond)
	z.checkHealth()

	if !called {
		t.Error("fallback handler should have been called on transition to Failed")
	}
	if !usePollValue {
		t.Error("fallback handler should have been called with usePoll=true")
	}
}

// =============================================================================
// PART 3: Stats() — verify counters
// =============================================================================

// TestStats_InitialValues verifies that a freshly created listener reports
// zero counters and Disabled status.
func TestStats_InitialValues(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)
	// Override: newHealthTestListener sets running=true but status is still Disabled
	z.running.Store(false)
	z.status.Store(int32(ZMQStatusDisabled))

	stats := z.Stats()
	if stats.Status != "disabled" {
		t.Errorf("initial Status = %q, want %q", stats.Status, "disabled")
	}
	if stats.MessagesReceived != 0 {
		t.Errorf("initial MessagesReceived = %d, want 0", stats.MessagesReceived)
	}
	if stats.ErrorsCount != 0 {
		t.Errorf("initial ErrorsCount = %d, want 0", stats.ErrorsCount)
	}
	if stats.StabilityReached {
		t.Error("initial StabilityReached should be false")
	}
	if stats.FailureDuration != 0 {
		t.Errorf("initial FailureDuration = %v, want 0", stats.FailureDuration)
	}
}

// TestStats_CountersAfterActivity verifies that messagesReceived and
// errorsCount are properly reflected in Stats().
func TestStats_CountersAfterActivity(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	// Simulate message and error counts
	z.messagesReceived.Add(42)
	z.errorsCount.Add(7)
	z.recordSuccess() // moves to Healthy

	stats := z.Stats()
	if stats.MessagesReceived != 42 {
		t.Errorf("MessagesReceived = %d, want 42", stats.MessagesReceived)
	}
	if stats.ErrorsCount != 7 {
		t.Errorf("ErrorsCount = %d, want 7", stats.ErrorsCount)
	}
	if stats.Status != "healthy" {
		t.Errorf("Status = %q, want %q", stats.Status, "healthy")
	}
}

// TestStats_LastMessageAge verifies that LastMessageAge is populated and
// reflects a recent timestamp after recordSuccess().
func TestStats_LastMessageAge(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	z.recordSuccess()
	time.Sleep(20 * time.Millisecond)

	stats := z.Stats()
	if stats.LastMessageAge < 10*time.Millisecond {
		t.Errorf("LastMessageAge = %v, expected >= 10ms", stats.LastMessageAge)
	}
	if stats.LastMessageAge > 5*time.Second {
		t.Errorf("LastMessageAge = %v, expected < 5s (something is wrong)", stats.LastMessageAge)
	}
}

// TestStats_FailureDuration verifies that FailureDuration is populated
// when there is an active failure.
func TestStats_FailureDuration(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	z.recordFailure()
	time.Sleep(50 * time.Millisecond)

	stats := z.Stats()
	if stats.FailureDuration < 30*time.Millisecond {
		t.Errorf("FailureDuration = %v, expected >= 30ms", stats.FailureDuration)
	}
}

// TestStats_HealthyDuration verifies that HealthyDuration is populated
// when the listener is in a healthy period.
func TestStats_HealthyDuration(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	z.recordSuccess()
	time.Sleep(50 * time.Millisecond)

	stats := z.Stats()
	if stats.HealthyDuration < 30*time.Millisecond {
		t.Errorf("HealthyDuration = %v, expected >= 30ms", stats.HealthyDuration)
	}
}

// TestStats_StabilityReachedFlag verifies that the StabilityReached flag
// is correctly reflected in Stats().
func TestStats_StabilityReachedFlag(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	stats := z.Stats()
	if stats.StabilityReached {
		t.Error("StabilityReached should start as false")
	}

	z.stabilityReached.Store(true)
	stats = z.Stats()
	if !stats.StabilityReached {
		t.Error("StabilityReached should be true after setting atomic flag")
	}
}

// =============================================================================
// PART 4: Status() — accessor consistency
// =============================================================================

// TestStatus_ReturnsCurrentValue verifies that Status() returns whatever
// was last stored in the atomic status field.
func TestStatus_ReturnsCurrentValue(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	statuses := []ZMQStatus{
		ZMQStatusDisabled,
		ZMQStatusConnecting,
		ZMQStatusHealthy,
		ZMQStatusDegraded,
		ZMQStatusFailed,
	}

	for _, s := range statuses {
		z.status.Store(int32(s))
		if z.Status() != s {
			t.Errorf("Status() = %v after storing %v", z.Status(), s)
		}
	}
}

// TestStatus_IsHealthyConsistency verifies that IsHealthy() agrees with
// the documented statuses: Healthy and Connecting are healthy.
func TestStatus_IsHealthyConsistency(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	tests := []struct {
		status  ZMQStatus
		healthy bool
	}{
		{ZMQStatusDisabled, false},
		{ZMQStatusConnecting, true},
		{ZMQStatusHealthy, true},
		{ZMQStatusDegraded, false},
		{ZMQStatusFailed, false},
	}

	for _, tt := range tests {
		z.status.Store(int32(tt.status))
		if z.IsHealthy() != tt.healthy {
			t.Errorf("IsHealthy() = %v for status %v, want %v",
				z.IsHealthy(), tt.status, tt.healthy)
		}
	}
}

// TestStatus_IsFailedConsistency verifies that IsFailed() is true only
// for ZMQStatusFailed.
func TestStatus_IsFailedConsistency(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	for _, s := range []ZMQStatus{
		ZMQStatusDisabled,
		ZMQStatusConnecting,
		ZMQStatusHealthy,
		ZMQStatusDegraded,
	} {
		z.status.Store(int32(s))
		if z.IsFailed() {
			t.Errorf("IsFailed() should be false for status %v", s)
		}
	}

	z.status.Store(int32(ZMQStatusFailed))
	if !z.IsFailed() {
		t.Error("IsFailed() should be true for ZMQStatusFailed")
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

// TestHealthTransition_ConcurrentRecordSuccess verifies that concurrent
// calls to recordSuccess do not cause data races or panics.
func TestHealthTransition_ConcurrentRecordSuccess(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			z.recordSuccess()
		}()
	}
	wg.Wait()

	if z.Status() != ZMQStatusHealthy {
		t.Errorf("status after concurrent recordSuccess = %v, want Healthy", z.Status())
	}
}

// TestHealthTransition_ConcurrentRecordFailure verifies that concurrent
// calls to recordFailure do not cause data races or panics.
func TestHealthTransition_ConcurrentRecordFailure(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			z.recordFailure()
		}()
	}
	wg.Wait()

	// Status should be Degraded (first recordFailure sets it)
	if z.Status() != ZMQStatusDegraded {
		t.Errorf("status after concurrent recordFailure = %v, want Degraded", z.Status())
	}
}

// TestHealthTransition_MixedConcurrentActivity verifies that interleaved
// recordSuccess and recordFailure calls do not panic or deadlock.
func TestHealthTransition_MixedConcurrentActivity(t *testing.T) {
	t.Parallel()
	z := newHealthTestListener(t)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if n%2 == 0 {
				z.recordSuccess()
			} else {
				z.recordFailure()
			}
		}(i)
	}
	wg.Wait()

	// Status should be one of the valid states
	s := z.Status()
	switch s {
	case ZMQStatusHealthy, ZMQStatusDegraded:
		// expected
	default:
		t.Errorf("unexpected status after mixed activity: %v", s)
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

// BenchmarkIsDuplicateBlock measures the cost of the ring-buffer dedup check.
func BenchmarkIsDuplicateBlock(b *testing.B) {
	z := &ZMQListener{}
	hash := "0000000000000000000000000000000000000000000000000000000000000abc"
	z.isDuplicateBlock(hash) // prime the buffer

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		z.isDuplicateBlock(hash)
	}
}

// BenchmarkZMQRecordSuccess measures the cost of recording a successful message.
func BenchmarkZMQRecordSuccess(b *testing.B) {
	logger := zap.NewNop()
	cfg := &config.ZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	z := NewZMQListener(cfg, logger)
	z.running.Store(true)
	z.stopCh = make(chan struct{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		z.recordSuccess()
	}
}

// BenchmarkStats measures the cost of collecting stats.
func BenchmarkStats(b *testing.B) {
	logger := zap.NewNop()
	cfg := &config.ZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	z := NewZMQListener(cfg, logger)
	z.messagesReceived.Add(1000)
	z.errorsCount.Add(50)
	z.lastMessageTime.Store(time.Now().Unix())
	z.healthyStartTime.Store(time.Now().Unix())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = z.Stats()
	}
}
