// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for Circuit Breaker + Block Queue compound stress.
//
// TEST 10: Multi-Coin Simultaneous Block + Circuit Breaker Open + Backpressure Emergency
// Exercises CircuitBreaker and BlockQueue simultaneously under opposing forces:
// one goroutine opens the circuit while another recovers it, while the block
// queue is concurrently enqueued/drained using DequeueWithCommit.
package database

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestChaos_CircuitBreaker_BlockQueue_CompoundStress exercises the CircuitBreaker
// and BlockQueue under simultaneous opposing forces:
// - Recovery goroutine: RecordSuccess to close circuit
// - Failure goroutine: RecordFailure to open circuit
// - Drain goroutines: DequeueWithCommit to process blocks
// - Enqueue goroutines: Enqueue new blocks under queue pressure
// - AllowRequest goroutines: check circuit state under rapid transitions
//
// TARGET: circuitbreaker.go:117-191 (AllowRequest/RecordSuccess/RecordFailure)
// TARGET: circuitbreaker.go:270-391 (BlockQueue Enqueue/DequeueWithCommit)
// INVARIANT: No lost blocks. Total blocks in = drained + remaining + dropped.
// RUN WITH: go test -race -run TestChaos_CircuitBreaker_BlockQueue_CompoundStress
func TestChaos_CircuitBreaker_BlockQueue_CompoundStress(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 5, // Low threshold for rapid state changes
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       100 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)
	bq := NewBlockQueue(50) // Small queue for saturation testing

	// Phase 1: Open the circuit breaker
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("Circuit should be open after %d failures, got %s",
			cfg.FailureThreshold, cb.State())
	}

	// Phase 2: Fill the block queue
	const initialBlocks = 45
	for i := 0; i < initialBlocks; i++ {
		block := &Block{
			Height: uint64(i),
			Hash:   fmt.Sprintf("initial-block-%d", i),
			Miner:  "chaos-miner",
			Status: "pending",
		}
		if !bq.Enqueue(block) {
			t.Fatalf("Failed to enqueue block %d", i)
		}
	}
	if bq.Len() != initialBlocks {
		t.Fatalf("Queue length = %d, want %d", bq.Len(), initialBlocks)
	}

	// Phase 3: Concurrent compound stress
	var drainedBlocks atomic.Int32
	var enqueueFailed atomic.Int32
	var enqueueSuccess atomic.Int32
	var allowedRequests atomic.Int32
	var blockedRequests atomic.Int32

	var wg sync.WaitGroup

	// Recovery goroutine: rapidly closes the circuit
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			cb.RecordSuccess()
			time.Sleep(500 * time.Microsecond)
		}
	}()

	// Failure goroutine: rapidly opens the circuit
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			cb.RecordFailure()
			time.Sleep(time.Millisecond)
		}
	}()

	// Drain goroutines: use DequeueWithCommit for crash-safe processing
	for d := 0; d < 5; d++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				entry, commit := bq.DequeueWithCommit()
				if entry != nil {
					if commit() { // Only count if this goroutine actually removed the entry
						drainedBlocks.Add(1)
					}
				}
				time.Sleep(500 * time.Microsecond)
			}
		}()
	}

	// Enqueue goroutines: add blocks while queue is being drained
	const newBlocksPerGoroutine = 50
	for e := 0; e < 3; e++ {
		wg.Add(1)
		go func(eIdx int) {
			defer wg.Done()
			for i := 0; i < newBlocksPerGoroutine; i++ {
				block := &Block{
					Height: uint64(1000 + eIdx*100 + i),
					Hash:   fmt.Sprintf("stress-block-%d-%d", eIdx, i),
					Miner:  "chaos-stress-miner",
					Status: "pending",
				}
				if bq.Enqueue(block) {
					enqueueSuccess.Add(1)
				} else {
					enqueueFailed.Add(1)
				}
			}
		}(e)
	}

	// AllowRequest goroutines: check circuit state under rapid transitions
	for r := 0; r < 10; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				allowed, _ := cb.AllowRequest()
				if allowed {
					allowedRequests.Add(1)
				} else {
					blockedRequests.Add(1)
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
	}

	wg.Wait()

	// Collect final state
	stats := cb.Stats()
	qLen := bq.Len()
	dropped := bq.Dropped()

	t.Logf("CIRCUIT BREAKER: state=%s failures=%d stateChanges=%d blocked=%d backoff=%v",
		stats.State, stats.Failures, stats.StateChanges, stats.TotalBlocked, stats.CurrentBackoff)
	t.Logf("BLOCK QUEUE: remaining=%d drained=%d dropped=%d",
		qLen, drainedBlocks.Load(), dropped)
	t.Logf("ENQUEUE: success=%d failed=%d (of %d attempted)",
		enqueueSuccess.Load(), enqueueFailed.Load(), 3*newBlocksPerGoroutine)
	t.Logf("REQUESTS: allowed=%d blocked=%d",
		allowedRequests.Load(), blockedRequests.Load())

	// INVARIANT: blocks that entered the queue = drained + remaining
	// NOTE: Dropped() counts blocks rejected by Enqueue (never entered queue).
	// These are the SAME blocks counted by enqueueFailed - do NOT double-count.
	totalIn := int32(initialBlocks) + enqueueSuccess.Load()
	totalOut := drainedBlocks.Load() + int32(qLen)

	t.Logf("ACCOUNTING: entered_queue=%d (initial=%d + enqueued=%d) vs exited=%d (drained=%d + remaining=%d)",
		totalIn, initialBlocks, enqueueSuccess.Load(),
		totalOut, drainedBlocks.Load(), qLen)
	t.Logf("REJECTED AT DOOR: enqueueFailed=%d bq.Dropped=%d (should match)",
		enqueueFailed.Load(), dropped)

	if totalIn != totalOut {
		t.Errorf("BLOCK LEAK: entered=%d exited=%d diff=%d (blocks unaccounted for!)",
			totalIn, totalOut, totalIn-totalOut)
	}

	// Circuit breaker should have had state changes from the competing forces
	if stats.StateChanges == 0 {
		t.Logf("WARNING: No circuit state changes. Increase iteration counts.")
	} else {
		t.Logf("Circuit breaker transitioned %d times under competing success/failure forces",
			stats.StateChanges)
	}
}
