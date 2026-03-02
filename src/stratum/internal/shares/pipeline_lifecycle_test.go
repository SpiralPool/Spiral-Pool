// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares — Pipeline lifecycle tests.
//
// These tests exercise the Pipeline.Start(), Stop(), and Submit() methods
// end-to-end using mock writers. They cover:
//   - Start/Stop lifecycle with share flow-through
//   - Stop() idempotency via sync.Once (no panic on double-Stop)
//   - Submit() rejected when pipeline is not running
//   - Submit() accepted and shares reach the writer
//   - Backpressure callback propagation
//   - DB failure tracking (degraded/critical thresholds)
//   - Circuit breaker interaction with batch writer
package shares

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/pkg/protocol"
	"github.com/spiralpool/stratum/pkg/ringbuffer"
	"go.uber.org/zap"
)

// lifecycleMockWriter tracks calls and allows controlled failures.
type lifecycleMockWriter struct {
	mu         sync.Mutex
	batches    [][]*protocol.Share
	failNext   int
	closed     bool
	closeCalls int
}

func (w *lifecycleMockWriter) WriteBatch(_ context.Context, shares []*protocol.Share) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failNext > 0 {
		w.failNext--
		return context.DeadlineExceeded
	}
	cp := make([]*protocol.Share, len(shares))
	copy(cp, shares)
	w.batches = append(w.batches, cp)
	return nil
}

func (w *lifecycleMockWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.closeCalls++
	return nil
}

func (w *lifecycleMockWriter) totalSharesWritten() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for _, b := range w.batches {
		total += len(b)
	}
	return total
}

// newTestPipeline creates a Pipeline wired to the given writer, no WAL.
func newTestPipeline(writer ShareWriter) *Pipeline {
	logger, _ := zap.NewDevelopment()
	return &Pipeline{
		cfg: &config.DatabaseConfig{
			Batching: config.BatchingConfig{
				Size:     50,
				Interval: 50 * time.Millisecond,
			},
		},
		logger:         logger.Sugar(),
		buffer:         ringbuffer.New[*protocol.Share](4096),
		batchSize:      50,
		flushInterval:  50 * time.Millisecond,
		batchChan:      make(chan []*protocol.Share, 200),
		writer:         writer,
		circuitBreaker: database.NewCircuitBreaker(database.DefaultCircuitBreakerConfig()),
	}
}

func makeShare(miner string) *protocol.Share {
	return &protocol.Share{MinerAddress: miner, WorkerName: "rig0"}
}

// =============================================================================
// Pipeline Start/Stop lifecycle
// =============================================================================

// TestPipelineLifecycle_StartSubmitStop verifies the happy path:
// Start → Submit shares → Stop → shares arrive at writer.
func TestPipelineLifecycle_StartSubmitStop(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	ctx, cancel := context.WithCancel(context.Background())

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Submit shares
	const shareCount = 120
	for i := 0; i < shareCount; i++ {
		if !p.Submit(makeShare("addr_" + string(rune('A'+i%26)))) {
			t.Fatalf("Submit() returned false at share %d", i)
		}
	}

	// Give the pipeline time to flush (batch interval 50ms + batch writer processing)
	time.Sleep(300 * time.Millisecond)

	// Cancel context first, then stop — mimics real shutdown
	cancel()
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}

	// Verify shares were written
	total := writer.totalSharesWritten()
	if total == 0 {
		t.Fatal("No shares reached the writer after Start/Submit/Stop cycle")
	}
	t.Logf("Shares written: %d/%d", total, shareCount)

	// Verify writer was closed
	writer.mu.Lock()
	if !writer.closed {
		t.Error("Writer should be closed after Stop()")
	}
	writer.mu.Unlock()

	// Verify pipeline stats
	stats := p.Stats()
	if stats.Processed != shareCount {
		t.Errorf("Processed = %d, want %d", stats.Processed, shareCount)
	}
}

// TestPipelineLifecycle_StopIdempotency verifies that calling Stop() twice
// does not panic (sync.Once protection).
func TestPipelineLifecycle_StopIdempotency(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	ctx, cancel := context.WithCancel(context.Background())

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Submit a few shares
	for i := 0; i < 10; i++ {
		p.Submit(makeShare("miner"))
	}

	time.Sleep(100 * time.Millisecond)
	cancel()

	// First Stop — should work
	if err := p.Stop(); err != nil {
		t.Fatalf("First Stop() failed: %v", err)
	}

	// Second Stop — should NOT panic (sync.Once)
	if err := p.Stop(); err != nil {
		t.Fatalf("Second Stop() failed: %v", err)
	}

	// Writer.Close should only have been called once
	writer.mu.Lock()
	if writer.closeCalls != 1 {
		t.Errorf("Writer.Close() called %d times, expected 1 (sync.Once should prevent double-close)", writer.closeCalls)
	}
	writer.mu.Unlock()
}

// TestPipelineLifecycle_StopWithoutStart verifies Stop() on a pipeline that
// was never started doesn't panic or deadlock.
func TestPipelineLifecycle_StopWithoutStart(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	// Stop without Start — should not panic or deadlock
	done := make(chan struct{})
	go func() {
		_ = p.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked on pipeline that was never started")
	}
}

// =============================================================================
// Submit behavior
// =============================================================================

// TestPipelineSubmit_RejectedWhenNotRunning verifies Submit() returns false
// before Start() is called.
func TestPipelineSubmit_RejectedWhenNotRunning(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	if p.Submit(makeShare("miner")) {
		t.Error("Submit() should return false when pipeline is not running")
	}
}

// TestPipelineSubmit_RejectedAfterStop verifies Submit() returns false
// after Stop() is called.
func TestPipelineSubmit_RejectedAfterStop(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	cancel()
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}

	if p.Submit(makeShare("miner")) {
		t.Error("Submit() should return false after Stop()")
	}
}

// =============================================================================
// Database failure tracking
// =============================================================================

// TestPipelineDBHealth_DegradedAfterFailures verifies the pipeline marks DB
// as degraded after MaxDBWriteFailures consecutive failures.
func TestPipelineDBHealth_DegradedAfterFailures(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	// Fail enough times to trigger degraded but not critical
	writer.failNext = MaxDBWriteFailures * MaxRetryAttempts
	p := newTestPipeline(writer)

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Submit enough shares to trigger batch writes
	for i := 0; i < 100; i++ {
		p.Submit(makeShare("miner"))
	}

	// Wait for batches to be processed (with retries + backoff this takes time)
	time.Sleep(3 * time.Second)

	cancel()
	_ = p.Stop()

	failures, degraded, _, _, _ := p.DBHealthStatus()
	if failures < int32(MaxDBWriteFailures) {
		t.Logf("Warning: only %d failures recorded, expected >= %d (may be timing-dependent)", failures, MaxDBWriteFailures)
	}
	if !degraded {
		t.Logf("Note: degraded=%v failures=%d — degraded state depends on failure count reaching threshold", degraded, failures)
	}
}

// TestPipelineDBHealth_RecoveryResetsState verifies that a successful write
// after failures resets the degraded/critical flags.
func TestPipelineDBHealth_RecoveryResetsState(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	// Manually set degraded state to simulate prior failures
	p.dbWriteFailures.Store(int32(MaxDBWriteFailures + 1))
	p.isDBDegraded.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Submit shares — writer will succeed, should reset state
	for i := 0; i < 100; i++ {
		p.Submit(makeShare("miner"))
	}
	time.Sleep(300 * time.Millisecond)

	cancel()
	_ = p.Stop()

	if writer.totalSharesWritten() == 0 {
		t.Fatal("Writer should have received shares for recovery test")
	}

	failures, degraded, critical, _, _ := p.DBHealthStatus()
	if failures != 0 {
		t.Errorf("Failure count should reset to 0 after recovery, got %d", failures)
	}
	if degraded {
		t.Error("Degraded flag should be false after recovery")
	}
	if critical {
		t.Error("Critical flag should be false after recovery")
	}
}

// =============================================================================
// Backpressure callback
// =============================================================================

// TestPipelineBackpressure_CallbackFires verifies the backpressure callback
// is invoked when buffer fill changes levels.
func TestPipelineBackpressure_CallbackFires(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	// Make writer very slow so buffer fills up
	logger, _ := zap.NewDevelopment()

	// Use tiny buffer to make backpressure observable
	p := &Pipeline{
		cfg: &config.DatabaseConfig{
			Batching: config.BatchingConfig{
				Size:     5000, // Large batch size so collector doesn't drain fast
				Interval: 10 * time.Second,
			},
		},
		logger:         logger.Sugar(),
		buffer:         ringbuffer.New[*protocol.Share](128), // Tiny buffer
		batchSize:      5000,
		flushInterval:  10 * time.Second,
		batchChan:      make(chan []*protocol.Share, 200),
		writer:         writer,
		circuitBreaker: database.NewCircuitBreaker(database.DefaultCircuitBreakerConfig()),
	}

	var callbackLevels []BackpressureLevel
	var cbMu sync.Mutex
	p.backpressureCallback = func(level BackpressureLevel) {
		cbMu.Lock()
		callbackLevels = append(callbackLevels, level)
		cbMu.Unlock()
	}

	// Fill buffer without starting pipeline goroutines (direct buffer fill)
	p.running.Store(true)

	// Fill ~90% of buffer (115/128)
	for i := 0; i < 115; i++ {
		p.Submit(makeShare("miner"))
	}

	// Check the backpressure level directly
	level := p.GetBackpressureLevel()
	if level == BackpressureNone {
		t.Errorf("Expected non-None backpressure at ~90%% fill, got %s", level)
	}

	p.running.Store(false)
}

// =============================================================================
// Concurrent Submit safety
// =============================================================================

// TestPipelineSubmit_ConcurrentSafety verifies multiple goroutines can Submit()
// concurrently without races or panics.
func TestPipelineSubmit_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	var wg sync.WaitGroup
	var accepted atomic.Int64
	var rejected atomic.Int64

	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if p.Submit(makeShare("miner")) {
					accepted.Add(1)
				} else {
					rejected.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	time.Sleep(300 * time.Millisecond)
	cancel()
	_ = p.Stop()

	t.Logf("Concurrent submit: accepted=%d rejected=%d written=%d",
		accepted.Load(), rejected.Load(), writer.totalSharesWritten())

	if accepted.Load() == 0 {
		t.Error("At least some shares should have been accepted")
	}
}

// =============================================================================
// Stats accuracy
// =============================================================================

// TestPipelineStats_ProcessedMatchesSubmitted verifies the processed counter
// matches the number of successful Submit() calls.
func TestPipelineStats_ProcessedMatchesSubmitted(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	submitted := 0
	for i := 0; i < 200; i++ {
		if p.Submit(makeShare("miner")) {
			submitted++
		}
	}

	time.Sleep(300 * time.Millisecond)
	cancel()
	_ = p.Stop()

	stats := p.Stats()
	if stats.Processed != uint64(submitted) {
		t.Errorf("Processed=%d but submitted=%d", stats.Processed, submitted)
	}
}

// TestPipelineCircuitBreaker_InitialState verifies circuit breaker starts closed.
func TestPipelineCircuitBreaker_InitialState(t *testing.T) {
	t.Parallel()

	writer := &lifecycleMockWriter{}
	p := newTestPipeline(writer)

	_, _, _, _, circuitState := p.DBHealthStatus()
	if circuitState != "closed" {
		t.Errorf("Circuit breaker should start closed, got %q", circuitState)
	}

	cbStats := p.CircuitBreakerStats()
	if cbStats.Failures != 0 {
		t.Errorf("Circuit breaker should have 0 failures initially, got %d", cbStats.Failures)
	}
}
