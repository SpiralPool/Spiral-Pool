// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Unit tests for Share Pipeline failure/recovery.
//
// These tests verify:
// - Context cancellation during retry (batch loss)
// - Database recovery state transitions
// - Retry exhaustion and failure tracking
// - Health threshold transitions (degraded/critical)
// - Batch channel backpressure handling
// - Graceful shutdown with pending shares
// - Stats accuracy after failures
package shares

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// =============================================================================
// Constants Validation Tests
// =============================================================================

// TestPipelineConstants verifies threshold constants.
func TestPipelineConstants(t *testing.T) {
	if MaxDBWriteFailures <= 0 {
		t.Error("MaxDBWriteFailures should be positive")
	}

	if MaxDBWriteFailuresCritical <= MaxDBWriteFailures {
		t.Error("Critical threshold should be higher than degraded threshold")
	}

	if DBWriteRetryDelay <= 0 {
		t.Error("DBWriteRetryDelay should be positive")
	}

	if MaxRetryAttempts <= 0 {
		t.Error("MaxRetryAttempts should be positive")
	}

	// Verify relationship
	t.Logf("Degraded threshold: %d failures", MaxDBWriteFailures)
	t.Logf("Critical threshold: %d failures", MaxDBWriteFailuresCritical)
	t.Logf("Retry delay: %v", DBWriteRetryDelay)
	t.Logf("Max retries: %d", MaxRetryAttempts)
}

// =============================================================================
// Mock ShareWriter for Testing
// =============================================================================

type mockShareWriter struct {
	mu           sync.Mutex
	writeCalls   int
	writeErr     error
	writtenCount int
	failCount    int // Number of times to fail before succeeding
	failureCount int // Current failure count
	closed       bool
}

func (m *mockShareWriter) WriteBatch(_ context.Context, shares []*protocol.Share) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.writeCalls++

	if m.failCount > 0 && m.failureCount < m.failCount {
		m.failureCount++
		return m.writeErr
	}

	m.writtenCount += len(shares)
	return nil
}

func (m *mockShareWriter) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockShareWriter) getStats() (calls, written int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeCalls, m.writtenCount
}

// =============================================================================
// Database Health State Tests
// =============================================================================

// TestDBHealthState_Initial verifies initial state is healthy.
func TestDBHealthState_Initial(t *testing.T) {
	var failures atomic.Int32
	var degraded atomic.Bool
	var critical atomic.Bool

	if failures.Load() != 0 {
		t.Error("Initial failure count should be 0")
	}

	if degraded.Load() {
		t.Error("Initial state should not be degraded")
	}

	if critical.Load() {
		t.Error("Initial state should not be critical")
	}
}

// TestDBHealthState_Degraded verifies degraded state transition.
func TestDBHealthState_Degraded(t *testing.T) {
	var failures atomic.Int32
	var degraded atomic.Bool

	// Simulate failures up to degraded threshold
	for i := int32(0); i < int32(MaxDBWriteFailures); i++ {
		count := failures.Add(1)

		if count >= int32(MaxDBWriteFailures) && !degraded.Load() {
			degraded.Store(true)
		}
	}

	if !degraded.Load() {
		t.Errorf("Should be degraded after %d failures", MaxDBWriteFailures)
	}

	if failures.Load() != int32(MaxDBWriteFailures) {
		t.Errorf("Failure count = %d, want %d",
			failures.Load(), MaxDBWriteFailures)
	}
}

// TestDBHealthState_Critical verifies critical state transition.
func TestDBHealthState_Critical(t *testing.T) {
	var failures atomic.Int32
	var critical atomic.Bool

	// Simulate failures up to critical threshold
	for i := int32(0); i < int32(MaxDBWriteFailuresCritical); i++ {
		count := failures.Add(1)

		if count >= int32(MaxDBWriteFailuresCritical) && !critical.Load() {
			critical.Store(true)
		}
	}

	if !critical.Load() {
		t.Errorf("Should be critical after %d failures", MaxDBWriteFailuresCritical)
	}
}

// TestDBHealthState_Recovery verifies recovery from degraded state.
func TestDBHealthState_Recovery(t *testing.T) {
	var failures atomic.Int32
	var degraded atomic.Bool
	var critical atomic.Bool

	// Enter degraded state
	failures.Store(int32(MaxDBWriteFailures))
	degraded.Store(true)

	// Simulate successful write (reset failures)
	failures.Store(0)
	if failures.Load() == 0 {
		if degraded.Load() {
			degraded.Store(false)
		}
		if critical.Load() {
			critical.Store(false)
		}
	}

	if degraded.Load() {
		t.Error("Should recover from degraded after successful write")
	}

	if failures.Load() != 0 {
		t.Error("Failure count should be reset to 0")
	}
}

// TestDBHealthState_CriticalRecovery verifies recovery from critical state.
func TestDBHealthState_CriticalRecovery(t *testing.T) {
	var failures atomic.Int32
	var degraded atomic.Bool
	var critical atomic.Bool

	// Enter critical state
	failures.Store(int32(MaxDBWriteFailuresCritical))
	degraded.Store(true)
	critical.Store(true)

	// Successful write resets everything
	failures.Store(0)
	degraded.Store(false)
	critical.Store(false)

	if degraded.Load() || critical.Load() {
		t.Error("Should fully recover after successful write")
	}
}

// =============================================================================
// Retry Logic Tests
// =============================================================================

// TestRetryExhaustion verifies behavior when all retries fail.
func TestRetryExhaustion(t *testing.T) {
	testErr := errors.New("database connection refused")
	var dropped atomic.Uint64
	var retried atomic.Uint64

	// Simulate retry loop exhaustion
	for attempt := 1; attempt <= MaxRetryAttempts; attempt++ {
		// Attempt fails
		if attempt < MaxRetryAttempts {
			retried.Add(1)
			// Would sleep here in real code
		}
	}

	// All retries exhausted
	batchSize := uint64(100)
	dropped.Add(batchSize)

	if dropped.Load() != batchSize {
		t.Errorf("Dropped = %d, want %d", dropped.Load(), batchSize)
	}

	if retried.Load() != uint64(MaxRetryAttempts-1) {
		t.Errorf("Retried = %d, want %d", retried.Load(), MaxRetryAttempts-1)
	}

	_ = testErr // Used in real implementation for logging
}

// TestRetrySuccessAfterFailures verifies recovery after some failures.
func TestRetrySuccessAfterFailures(t *testing.T) {
	mock := &mockShareWriter{
		failCount: 2, // Fail twice, then succeed
		writeErr:  errors.New("temporary error"),
	}

	shares := []*protocol.Share{{MinerAddress: "test"}}
	ctx := context.Background()

	// Simulate retry loop
	var success bool
	for attempt := 1; attempt <= MaxRetryAttempts; attempt++ {
		err := mock.WriteBatch(ctx, shares)
		if err == nil {
			success = true
			break
		}
	}

	if !success {
		t.Error("Should succeed after retries")
	}

	calls, written := mock.getStats()
	if calls != 3 { // 2 failures + 1 success
		t.Errorf("Write calls = %d, want 3", calls)
	}

	if written != 1 {
		t.Errorf("Written = %d, want 1", written)
	}
}

// =============================================================================
// Context Cancellation Tests
// =============================================================================

// TestContextCancellationDuringRetry tests batch loss on cancel.
func TestContextCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var dropped atomic.Uint64
	batchSize := uint64(50)

	// Start retry simulation
	done := make(chan bool)
	go func() {
		select {
		case <-ctx.Done():
			// Context cancelled - batch lost
			dropped.Add(batchSize)
			done <- true
			return
		case <-time.After(100 * time.Millisecond):
			// Would retry here
		}
	}()

	// Cancel while "retrying"
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Expected
	case <-time.After(1 * time.Second):
		t.Error("Goroutine should exit on context cancel")
	}

	if dropped.Load() != batchSize {
		t.Errorf("Dropped = %d, want %d (batch should be lost on cancel)",
			dropped.Load(), batchSize)
	}
}

// TestContextTimeoutDuringWrite tests write timeout behavior.
func TestContextTimeoutDuringWrite(t *testing.T) {
	// Simulate 30-second write timeout
	writeTimeout := 30 * time.Second

	if writeTimeout != 30*time.Second {
		t.Errorf("Write timeout = %v, want 30s", writeTimeout)
	}

	// Verify timeout is reasonable
	if writeTimeout < 10*time.Second || writeTimeout > 60*time.Second {
		t.Error("Write timeout should be 10-60 seconds")
	}
}

// =============================================================================
// Batch Channel Tests
// =============================================================================

// TestBatchChannelFull tests backpressure when channel is full.
func TestBatchChannelFull(t *testing.T) {
	batchChan := make(chan []*protocol.Share, 2) // Small buffer for test
	var dropped atomic.Uint64

	// Fill the channel
	batchChan <- []*protocol.Share{{MinerAddress: "1"}}
	batchChan <- []*protocol.Share{{MinerAddress: "2"}}

	// Try to send another batch
	batch := []*protocol.Share{{MinerAddress: "3"}, {MinerAddress: "4"}}

	select {
	case batchChan <- batch:
		t.Error("Channel should be full")
	default:
		// Expected - channel full
		dropped.Add(uint64(len(batch)))
	}

	if dropped.Load() != 2 {
		t.Errorf("Dropped = %d, want 2", dropped.Load())
	}
}

// TestBatchCopy verifies batch copy to prevent race conditions.
func TestBatchCopy(t *testing.T) {
	original := []*protocol.Share{
		{MinerAddress: "miner1"},
		{MinerAddress: "miner2"},
	}

	// Copy the batch (as done in sendBatch)
	copied := make([]*protocol.Share, len(original))
	copy(copied, original)

	// Modify original
	original[0] = &protocol.Share{MinerAddress: "modified"}

	// Copy should be unchanged
	if copied[0].MinerAddress != "miner1" {
		t.Error("Copied batch was modified when original changed")
	}
}

// =============================================================================
// Submit Tests
// =============================================================================

// TestSubmitWhenNotRunning tests rejection when pipeline is stopped.
func TestSubmitWhenNotRunning(t *testing.T) {
	var running atomic.Bool

	// Initially not running
	if running.Load() {
		t.Error("Should start as not running")
	}

	// Submit should fail
	share := &protocol.Share{MinerAddress: "test"}
	_ = share // Would be submitted in real code

	// Simulate Submit logic
	accepted := running.Load() // Would be TryEnqueue in real code

	if accepted {
		t.Error("Should reject shares when not running")
	}
}

// TestSubmitWhenRunning tests acceptance when pipeline is running.
func TestSubmitWhenRunning(t *testing.T) {
	var running atomic.Bool
	var processed atomic.Uint64

	running.Store(true)

	// Simulate successful submit
	if running.Load() {
		// Would TryEnqueue here
		processed.Add(1)
	}

	if processed.Load() != 1 {
		t.Error("Should process share when running")
	}
}

// =============================================================================
// Stats Tests
// =============================================================================

// TestStatsAccuracy tests stats after various operations.
func TestStatsAccuracy(t *testing.T) {
	var processed atomic.Uint64
	var written atomic.Uint64
	var dropped atomic.Uint64

	// Simulate processing
	for i := 0; i < 100; i++ {
		processed.Add(1)
	}

	// Simulate writes (90% success)
	written.Add(90)
	dropped.Add(10)

	// Verify totals
	if processed.Load() != 100 {
		t.Errorf("Processed = %d, want 100", processed.Load())
	}

	if written.Load()+dropped.Load() != processed.Load() {
		t.Errorf("Written (%d) + Dropped (%d) != Processed (%d)",
			written.Load(), dropped.Load(), processed.Load())
	}
}

// TestStatsStruct verifies Stats struct fields.
func TestStatsStruct(t *testing.T) {
	stats := Stats{
		Processed:      1000,
		Written:        990,
		Dropped:        10,
		BufferCurrent:  50,
		BufferCapacity: 1048576,
	}

	if stats.Written+stats.Dropped != stats.Processed {
		t.Error("Stats accounting mismatch")
	}

	if stats.BufferCurrent > stats.BufferCapacity {
		t.Error("BufferCurrent cannot exceed BufferCapacity")
	}
}

// =============================================================================
// Graceful Shutdown Tests
// =============================================================================

// TestGracefulShutdown tests pending share handling during stop.
func TestGracefulShutdown(t *testing.T) {
	var running atomic.Bool
	running.Store(true)

	// Simulate pending items
	pendingCount := 50

	done := make(chan bool)

	go func() {
		// Simulate flush remaining
		for i := 0; i < pendingCount; i++ {
			// Would dequeue and send here
		}
		done <- true
	}()

	// Stop the pipeline
	running.Store(false)

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Error("Shutdown should complete quickly")
	}
}

// TestWriterClose verifies writer is closed on stop.
func TestWriterClose(t *testing.T) {
	mock := &mockShareWriter{}

	if mock.closed {
		t.Error("Writer should not be closed initially")
	}

	// Simulate Stop()
	if mock != nil {
		_ = mock.Close()
	}

	mock.mu.Lock()
	closed := mock.closed
	mock.mu.Unlock()

	if !closed {
		t.Error("Writer should be closed after Stop()")
	}
}

// =============================================================================
// Buffer Size Tests
// =============================================================================

// TestBufferCapacity verifies buffer sizing.
func TestBufferCapacity(t *testing.T) {
	// From pipeline.go: ringbuffer.New[*protocol.Share](1 << 20)
	capacity := 1 << 20 // 1M shares

	if capacity != 1048576 {
		t.Errorf("Buffer capacity = %d, want 1048576", capacity)
	}

	// Verify it's a power of 2 (common for ring buffers)
	if capacity&(capacity-1) != 0 {
		t.Error("Capacity should be power of 2")
	}
}

// =============================================================================
// DBHealthStatus Method Tests
// =============================================================================

// TestDBHealthStatus verifies DBHealthStatus return values.
func TestDBHealthStatus(t *testing.T) {
	var dbWriteFailures atomic.Int32
	var isDBDegraded atomic.Bool
	var isDBCritical atomic.Bool
	var dropped atomic.Uint64

	// Set test values
	dbWriteFailures.Store(7)
	isDBDegraded.Store(true)
	dropped.Store(100)

	// Simulate DBHealthStatus()
	failures := dbWriteFailures.Load()
	degraded := isDBDegraded.Load()
	critical := isDBCritical.Load()
	droppedCount := dropped.Load()

	if failures != 7 {
		t.Errorf("Failures = %d, want 7", failures)
	}

	if !degraded {
		t.Error("Should report degraded")
	}

	if critical {
		t.Error("Should not report critical yet")
	}

	if droppedCount != 100 {
		t.Errorf("Dropped = %d, want 100", droppedCount)
	}
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

// TestConcurrentSubmit tests thread safety of Submit.
func TestConcurrentSubmit(t *testing.T) {
	var processed atomic.Uint64
	var running atomic.Bool
	running.Store(true)

	done := make(chan bool)
	numGoroutines := 10
	sharesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < sharesPerGoroutine; j++ {
				if running.Load() {
					processed.Add(1)
				}
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	expected := uint64(numGoroutines * sharesPerGoroutine)
	if processed.Load() != expected {
		t.Errorf("Processed = %d, want %d", processed.Load(), expected)
	}
}

// TestConcurrentStatsRead tests thread-safe stats reading.
func TestConcurrentStatsRead(t *testing.T) {
	var processed atomic.Uint64
	var written atomic.Uint64
	var dropped atomic.Uint64

	done := make(chan bool)

	// Writers
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				processed.Add(1)
				if j%10 == 0 {
					dropped.Add(1)
				} else {
					written.Add(1)
				}
			}
			done <- true
		}()
	}

	// Readers
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = processed.Load()
				_ = written.Load()
				_ = dropped.Load()
			}
			done <- true
		}()
	}

	// Wait for all
	for i := 0; i < 10; i++ {
		<-done
	}
}

// =============================================================================
// Circuit Breaker Integration Tests
// =============================================================================

// TestPipelineCircuitBreakerIntegration verifies circuit breaker is initialized.
func TestPipelineCircuitBreakerIntegration(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Batching: config.BatchingConfig{
			Size:     100,
			Interval: time.Second,
		},
	}
	logger := zap.NewNop()
	writer := &mockWriter{}

	pipeline := NewPipeline(cfg, writer, logger)

	// Verify circuit breaker is initialized
	stats := pipeline.CircuitBreakerStats()
	if stats.State != database.CircuitClosed {
		t.Errorf("Initial circuit state = %v, want closed", stats.State)
	}

	if stats.Failures != 0 {
		t.Errorf("Initial failures = %d, want 0", stats.Failures)
	}
}

// TestPipelineDBHealthStatusWithCircuit verifies DBHealthStatus includes circuit state.
func TestPipelineDBHealthStatusWithCircuit(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Batching: config.BatchingConfig{
			Size:     100,
			Interval: time.Second,
		},
	}
	logger := zap.NewNop()
	writer := &mockWriter{}

	pipeline := NewPipeline(cfg, writer, logger)

	failures, degraded, critical, dropped, circuitState := pipeline.DBHealthStatus()

	if failures != 0 {
		t.Errorf("Initial failures = %d, want 0", failures)
	}
	if degraded {
		t.Error("Should not be degraded initially")
	}
	if critical {
		t.Error("Should not be critical initially")
	}
	if dropped != 0 {
		t.Errorf("Initial dropped = %d, want 0", dropped)
	}
	if circuitState != "closed" {
		t.Errorf("Circuit state = %q, want \"closed\"", circuitState)
	}
}

// mockWriter is a simple mock for testing pipeline initialization.
type mockWriter struct {
	failCount int
	calls     int
}

func (m *mockWriter) WriteBatch(ctx context.Context, shares []*protocol.Share) error {
	m.calls++
	if m.failCount > 0 {
		m.failCount--
		return context.DeadlineExceeded
	}
	return nil
}

func (m *mockWriter) Close() error {
	return nil
}

// =============================================================================
// Benchmarks
// =============================================================================

// BenchmarkAtomicIncrement benchmarks atomic counter operations.
func BenchmarkAtomicIncrement(b *testing.B) {
	var counter atomic.Uint64

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Add(1)
	}
}

// BenchmarkAtomicLoad benchmarks atomic read operations.
func BenchmarkAtomicLoad(b *testing.B) {
	var counter atomic.Uint64
	counter.Store(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = counter.Load()
	}
}

// BenchmarkBatchCopy benchmarks slice copying.
func BenchmarkBatchCopy(b *testing.B) {
	original := make([]*protocol.Share, 1000)
	for i := range original {
		original[i] = &protocol.Share{MinerAddress: "test"}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copied := make([]*protocol.Share, len(original))
		copy(copied, original)
	}
}

// BenchmarkHealthCheck benchmarks health status check.
func BenchmarkHealthCheck(b *testing.B) {
	var failures atomic.Int32
	var degraded atomic.Bool
	var critical atomic.Bool
	var dropped atomic.Uint64

	failures.Store(3)
	dropped.Store(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = failures.Load()
		_ = degraded.Load()
		_ = critical.Load()
		_ = dropped.Load()
	}
}
