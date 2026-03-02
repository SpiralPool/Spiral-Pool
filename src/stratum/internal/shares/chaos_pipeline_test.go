// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos tests for the Share Pipeline under extreme conditions.
//
// TEST 2: Pipeline Ring Buffer + batchChan Dual Saturation
// TEST 5: Pipeline Graceful Shutdown With Active Retry Backoff
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
	"github.com/spiralpool/stratum/pkg/ringbuffer"
	"go.uber.org/zap"
)

// =============================================================================
// Mock writers for chaos tests
// =============================================================================

// chaosSaturationWriter simulates a slow database writer that creates backlog.
type chaosSaturationWriter struct {
	mu      sync.Mutex
	delay   time.Duration
	written int
}

func (w *chaosSaturationWriter) WriteBatch(_ context.Context, shares []*protocol.Share) error {
	time.Sleep(w.delay)
	w.mu.Lock()
	w.written += len(shares)
	w.mu.Unlock()
	return nil
}

func (w *chaosSaturationWriter) Close() error { return nil }

func (w *chaosSaturationWriter) getWritten() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written
}

// chaosAlwaysFailWriter always returns an error, trapping the batchWriter in retries.
type chaosAlwaysFailWriter struct {
	calls atomic.Int32
}

func (w *chaosAlwaysFailWriter) WriteBatch(_ context.Context, _ []*protocol.Share) error {
	w.calls.Add(1)
	return errors.New("chaos: database unavailable")
}

func (w *chaosAlwaysFailWriter) Close() error { return nil }

// =============================================================================
// Chaos Tests
// =============================================================================

// TestChaos_Pipeline_DualBufferSaturation floods the pipeline with shares from
// many goroutines, saturating both the ring buffer and batchChan simultaneously.
// Verifies the accounting invariant: every submitted share is either written,
// dropped (counted), or still in the buffer.
//
// TARGET: pipeline.go:256-293 (Submit), pipeline.go:296-352 (batchCollector/sendBatch)
// INVARIANT: submitted = written + dropped + buffered (no silent losses)
func TestChaos_Pipeline_DualBufferSaturation(t *testing.T) {
	writer := &chaosSaturationWriter{
		delay: 50 * time.Millisecond, // Slow writer creates backlog
	}

	// Construct pipeline with small buffers to trigger saturation quickly
	p := &Pipeline{
		cfg: &config.DatabaseConfig{
			Batching: config.BatchingConfig{
				Size:     10,
				Interval: 50 * time.Millisecond,
			},
		},
		logger:         zap.NewNop().Sugar(),
		buffer:         ringbuffer.New[*protocol.Share](256), // Small ring buffer
		batchSize:      10,
		flushInterval:  50 * time.Millisecond,
		batchChan:      make(chan []*protocol.Share, 2), // Tiny batch channel
		writer:         writer,
		circuitBreaker: database.NewCircuitBreaker(database.DefaultCircuitBreakerConfig()),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Pipeline start failed: %v", err)
	}

	// Flood shares from multiple goroutines
	const numGoroutines = 20
	const sharesPerGoroutine = 500
	var submitted atomic.Uint64
	var rejected atomic.Uint64

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gIdx int) {
			defer wg.Done()
			for i := 0; i < sharesPerGoroutine; i++ {
				share := chaosTestShare(gIdx*sharesPerGoroutine + i)
				if p.Submit(share) {
					submitted.Add(1)
				} else {
					rejected.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	// Allow pipeline to process remaining shares
	time.Sleep(3 * time.Second)

	cancel()
	if err := p.Stop(); err != nil {
		t.Logf("Pipeline stop error (expected during drain): %v", err)
	}

	stats := p.Stats()
	totalAttempted := submitted.Load() + rejected.Load()
	totalAccountedFor := stats.Written + stats.Dropped + uint64(stats.BufferCurrent)

	t.Logf("FLOOD: attempted=%d submitted=%d rejected_by_submit=%d",
		totalAttempted, submitted.Load(), rejected.Load())
	t.Logf("PIPELINE: processed=%d written=%d dropped=%d buffered=%d",
		stats.Processed, stats.Written, stats.Dropped, stats.BufferCurrent)
	t.Logf("DB WRITER: written=%d", writer.getWritten())
	t.Logf("ACCOUNTING: submitted=%d vs accounted=%d (written+dropped+buffered)",
		submitted.Load(), totalAccountedFor)

	// INVARIANT: Every submitted share must be accounted for
	if totalAccountedFor < submitted.Load() {
		t.Errorf("SHARE LEAK: %d shares unaccounted! submitted=%d accounted=%d",
			submitted.Load()-totalAccountedFor, submitted.Load(), totalAccountedFor)
	}

	// Verify saturation actually occurred (otherwise test is meaningless)
	if stats.Dropped == 0 && rejected.Load() == 0 {
		t.Logf("NOTE: No saturation occurred. Increase numGoroutines or decrease buffer sizes.")
	}
}

// TestChaos_Pipeline_ShutdownDuringRetryBackoff verifies that context cancellation
// breaks the batchWriter out of its exponential backoff sleep during retries.
// If the select on ctx.Done() is missing, shutdown blocks until the full backoff elapses.
//
// TARGET: pipeline.go:454-464 (retry backoff select with ctx.Done)
// INVARIANT: Shutdown completes within a reasonable time, not stuck in backoff.
func TestChaos_Pipeline_ShutdownDuringRetryBackoff(t *testing.T) {
	failWriter := &chaosAlwaysFailWriter{}

	p := &Pipeline{
		cfg: &config.DatabaseConfig{
			Batching: config.BatchingConfig{
				Size:     5,
				Interval: 50 * time.Millisecond,
			},
		},
		logger:    zap.NewNop().Sugar(),
		buffer:    ringbuffer.New[*protocol.Share](1024),
		batchSize: 5,
		flushInterval: 50 * time.Millisecond,
		batchChan:     make(chan []*protocol.Share, 10),
		writer:        failWriter,
		circuitBreaker: database.NewCircuitBreaker(database.CircuitBreakerConfig{
			FailureThreshold: 100,                  // High threshold: don't open circuit
			CooldownPeriod:   30 * time.Second,
			InitialBackoff:   500 * time.Millisecond,
			MaxBackoff:       5 * time.Second,
			BackoffFactor:    2.0,
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Submit shares to create batches that will fail and enter retry backoff
	for i := 0; i < 20; i++ {
		p.Submit(chaosTestShare(i))
	}

	// Wait for batchWriter to enter retry backoff
	time.Sleep(2 * time.Second)

	calls := failWriter.calls.Load()
	if calls == 0 {
		t.Fatalf("Writer never called - shares didn't reach batchWriter")
	}
	t.Logf("Writer called %d times (retries in progress)", calls)

	// Cancel context and measure shutdown time
	shutdownStart := time.Now()
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- p.Stop()
	}()

	select {
	case err := <-done:
		elapsed := time.Since(shutdownStart)
		t.Logf("Shutdown completed in %v (err=%v)", elapsed, err)
		if elapsed > 5*time.Second {
			t.Errorf("SHUTDOWN TOO SLOW: %v (want < 5s). Stuck in backoff sleep?", elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("DEADLOCK: Shutdown did not complete in 30s! Pipeline stuck in retry backoff.")
	}
}
