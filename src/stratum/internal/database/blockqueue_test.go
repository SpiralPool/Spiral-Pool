// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database - Comprehensive tests for BlockQueue.
//
// The BlockQueue (defined in circuitbreaker.go) holds blocks during
// circuit breaker outages so they are not lost. These tests cover:
//   - Enqueue/Dequeue FIFO ordering
//   - DequeueWithCommit crash-safety semantics
//   - DrainAll bulk processing
//   - UpdateEntryError retry tracking
//   - Capacity/overflow behavior with Dropped() counter
//   - Empty queue edge cases
//   - Concurrent access safety
//   - Constructor edge cases (zero/negative maxSize)
//
// NOTE: Some basic BlockQueue tests already exist in manager_ha_test.go.
// This file extends coverage with additional edge cases and scenarios
// not covered there.
package database

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Enqueue tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_Enqueue_SingleItem(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	block := &Block{Height: 42, Hash: "abc123", Miner: "miner1", Status: "pending"}
	ok := q.Enqueue(block)

	if !ok {
		t.Fatal("Enqueue returned false, expected true for non-full queue")
	}
	if q.Len() != 1 {
		t.Errorf("Len() = %d, want 1", q.Len())
	}

	// Verify the entry wraps the block correctly.
	entry := q.Peek()
	if entry == nil {
		t.Fatal("Peek() returned nil after Enqueue")
	}
	if entry.Block != block {
		t.Error("Peek().Block is not the same pointer as enqueued block")
	}
	if entry.Block.Height != 42 {
		t.Errorf("Peek().Block.Height = %d, want 42", entry.Block.Height)
	}
	if entry.Attempts != 0 {
		t.Errorf("Peek().Attempts = %d, want 0 (fresh enqueue)", entry.Attempts)
	}
	if entry.LastError != "" {
		t.Errorf("Peek().LastError = %q, want empty string", entry.LastError)
	}
}

func TestBlockQueue_Enqueue_QueuedAtIsSet(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	before := time.Now()
	q.Enqueue(&Block{Height: 1})
	after := time.Now()

	entry := q.Peek()
	if entry == nil {
		t.Fatal("Peek() returned nil")
	}
	if entry.QueuedAt.Before(before) || entry.QueuedAt.After(after) {
		t.Errorf("QueuedAt = %v, should be between %v and %v", entry.QueuedAt, before, after)
	}
}

func TestBlockQueue_Enqueue_MultipleItemsFIFO(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(100)

	for i := 0; i < 20; i++ {
		ok := q.Enqueue(&Block{Height: uint64(1000 + i)})
		if !ok {
			t.Fatalf("Enqueue failed at index %d", i)
		}
	}

	if q.Len() != 20 {
		t.Errorf("Len() = %d, want 20", q.Len())
	}

	// Verify FIFO: oldest item should be at the front.
	head := q.Peek()
	if head == nil || head.Block.Height != 1000 {
		height := uint64(0)
		if head != nil {
			height = head.Block.Height
		}
		t.Errorf("Peek().Block.Height = %d, want 1000 (FIFO ordering)", height)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Dequeue tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_DequeueWithCommit_FIFO(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 100})
	q.Enqueue(&Block{Height: 200})
	q.Enqueue(&Block{Height: 300})

	// DequeueWithCommit should return items in FIFO order.
	entry1, commit1 := q.DequeueWithCommit()
	if entry1 == nil || entry1.Block.Height != 100 {
		t.Errorf("First DequeueWithCommit: height = %v, want 100", entryHeight(entry1))
	}
	commit1()

	entry2, commit2 := q.DequeueWithCommit()
	if entry2 == nil || entry2.Block.Height != 200 {
		t.Errorf("Second DequeueWithCommit: height = %v, want 200", entryHeight(entry2))
	}
	commit2()

	entry3, commit3 := q.DequeueWithCommit()
	if entry3 == nil || entry3.Block.Height != 300 {
		t.Errorf("Third DequeueWithCommit: height = %v, want 300", entryHeight(entry3))
	}
	commit3()

	// Queue should now be empty.
	if q.Len() != 0 {
		t.Errorf("Len() = %d after draining all items, want 0", q.Len())
	}
}

func TestBlockQueue_DequeueWithCommit_Empty(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	entry, commit := q.DequeueWithCommit()
	if entry != nil {
		t.Errorf("DequeueWithCommit() on empty queue returned %+v, want nil", entry)
	}
	if commit() {
		t.Error("commit() on empty dequeue should return false")
	}
}

func TestBlockQueue_DequeueWithCommit_RemovesItem(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 1})

	if q.Len() != 1 {
		t.Fatalf("Pre-dequeue Len() = %d, want 1", q.Len())
	}

	_, commit := q.DequeueWithCommit()
	commit()

	if q.Len() != 0 {
		t.Errorf("Post-dequeue Len() = %d, want 0", q.Len())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DequeueWithCommit tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_DequeueWithCommit_NotRemovedBeforeCommit(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 500})
	q.Enqueue(&Block{Height: 600})

	entry, commit := q.DequeueWithCommit()
	if entry == nil {
		t.Fatal("DequeueWithCommit returned nil entry")
	}
	if entry.Block.Height != 500 {
		t.Errorf("DequeueWithCommit entry height = %d, want 500", entry.Block.Height)
	}

	// Before commit: queue should still have both items.
	if q.Len() != 2 {
		t.Errorf("Len() before commit = %d, want 2", q.Len())
	}

	// Peek should still return the same entry.
	peeked := q.Peek()
	if peeked == nil || peeked.Block.Height != 500 {
		t.Error("Peek should still show the uncommitted entry")
	}

	// Commit.
	committed := commit()
	if !committed {
		t.Error("commit() should return true on first call")
	}

	// After commit: only second item remains.
	if q.Len() != 1 {
		t.Errorf("Len() after commit = %d, want 1", q.Len())
	}

	next := q.Peek()
	if next == nil || next.Block.Height != 600 {
		t.Error("After commit, Peek should return second entry (height 600)")
	}
}

func TestBlockQueue_DequeueWithCommit_DoubleCommit(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 100})
	q.Enqueue(&Block{Height: 200})

	_, commit := q.DequeueWithCommit()

	// First commit succeeds.
	if !commit() {
		t.Error("First commit should return true")
	}

	// Double commit should return false and not remove another entry.
	if commit() {
		t.Error("Double commit should return false")
	}

	// Should still have the second entry.
	if q.Len() != 1 {
		t.Errorf("Len() after double commit = %d, want 1", q.Len())
	}
}

func TestBlockQueue_DequeueWithCommit_EmptyQueue(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	entry, commit := q.DequeueWithCommit()
	if entry != nil {
		t.Errorf("DequeueWithCommit on empty queue returned non-nil entry: %+v", entry)
	}
	if commit() {
		t.Error("commit() on empty dequeue should return false")
	}
}

func TestBlockQueue_DequeueWithCommit_NoCommitPreservesEntry(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 42})

	// Get entry but don't commit (simulating a crash before DB write).
	entry, _ := q.DequeueWithCommit()
	if entry == nil {
		t.Fatal("DequeueWithCommit returned nil")
	}

	// The entry should still be in the queue (not removed).
	if q.Len() != 1 {
		t.Errorf("Len() = %d after uncommitted DequeueWithCommit, want 1", q.Len())
	}

	// A subsequent DequeueWithCommit should return the same entry.
	entry2, commit2 := q.DequeueWithCommit()
	if entry2 == nil || entry2.Block.Height != 42 {
		t.Error("Second DequeueWithCommit should return the same entry")
	}

	// Now commit it.
	if !commit2() {
		t.Error("commit2() should return true")
	}
	if q.Len() != 0 {
		t.Errorf("Len() after commit = %d, want 0", q.Len())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DrainAll tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_DrainAll_ReturnsAllInOrder(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(50)
	const count = 15
	for i := 0; i < count; i++ {
		q.Enqueue(&Block{Height: uint64(i)})
	}

	drained := q.DrainAll()
	if len(drained) != count {
		t.Fatalf("DrainAll returned %d entries, want %d", len(drained), count)
	}

	for i, entry := range drained {
		if entry.Block.Height != uint64(i) {
			t.Errorf("Drained[%d].Height = %d, want %d", i, entry.Block.Height, i)
		}
	}

	if q.Len() != 0 {
		t.Errorf("Len() after DrainAll = %d, want 0", q.Len())
	}
}

func TestBlockQueue_DrainAll_EmptyQueue(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	result := q.DrainAll()
	if result != nil {
		t.Errorf("DrainAll on empty queue should return nil, got %v (len=%d)", result, len(result))
	}
}

func TestBlockQueue_DrainAll_ThenEnqueue(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 1})
	q.Enqueue(&Block{Height: 2})

	_ = q.DrainAll()

	// Queue should be reusable after drain.
	ok := q.Enqueue(&Block{Height: 3})
	if !ok {
		t.Fatal("Enqueue after DrainAll should succeed")
	}
	if q.Len() != 1 {
		t.Errorf("Len() after drain+enqueue = %d, want 1", q.Len())
	}

	entry := q.Peek()
	if entry == nil || entry.Block.Height != 3 {
		t.Error("Peek after drain+enqueue should return height 3")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// UpdateEntryError tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_UpdateEntryError_IncrementsAttempts(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 100})

	// Initial state: 0 attempts, empty error.
	entry := q.Peek()
	if entry.Attempts != 0 {
		t.Fatalf("Initial Attempts = %d, want 0", entry.Attempts)
	}
	if entry.LastError != "" {
		t.Fatalf("Initial LastError = %q, want empty", entry.LastError)
	}

	// First error.
	q.UpdateEntryError("connection refused")
	entry = q.Peek()
	if entry.Attempts != 1 {
		t.Errorf("Attempts after first error = %d, want 1", entry.Attempts)
	}
	if entry.LastError != "connection refused" {
		t.Errorf("LastError = %q, want %q", entry.LastError, "connection refused")
	}

	// Second error with different message.
	q.UpdateEntryError("timeout")
	entry = q.Peek()
	if entry.Attempts != 2 {
		t.Errorf("Attempts after second error = %d, want 2", entry.Attempts)
	}
	if entry.LastError != "timeout" {
		t.Errorf("LastError = %q, want %q", entry.LastError, "timeout")
	}
}

func TestBlockQueue_UpdateEntryError_EmptyQueueNoOp(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)

	// Should not panic on empty queue.
	q.UpdateEntryError("some error")

	if q.Len() != 0 {
		t.Errorf("Len() = %d after UpdateEntryError on empty queue, want 0", q.Len())
	}
}

func TestBlockQueue_UpdateEntryError_OnlyAffectsFirst(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 100})
	q.Enqueue(&Block{Height: 200})

	q.UpdateEntryError("failed")

	// First entry should have the error.
	first := q.Peek()
	if first.Attempts != 1 || first.LastError != "failed" {
		t.Errorf("First entry: Attempts=%d LastError=%q, want 1/failed",
			first.Attempts, first.LastError)
	}

	// Dequeue first, check second is untouched.
	_, commitFirst := q.DequeueWithCommit()
	commitFirst()
	second := q.Peek()
	if second.Attempts != 0 || second.LastError != "" {
		t.Errorf("Second entry: Attempts=%d LastError=%q, want 0/empty",
			second.Attempts, second.LastError)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Capacity and overflow tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_Overflow_ReturnsFalse(t *testing.T) {
	t.Parallel()

	const maxSize = 3
	q := NewBlockQueue(maxSize)

	// Fill to capacity.
	for i := 0; i < maxSize; i++ {
		ok := q.Enqueue(&Block{Height: uint64(i)})
		if !ok {
			t.Fatalf("Enqueue failed at index %d (should have room)", i)
		}
	}

	// Overflow: should return false.
	ok := q.Enqueue(&Block{Height: 999})
	if ok {
		t.Error("Enqueue beyond capacity should return false")
	}

	if q.Len() != maxSize {
		t.Errorf("Len() = %d after overflow, want %d", q.Len(), maxSize)
	}
}

func TestBlockQueue_Overflow_DroppedCounter(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(2)
	q.Enqueue(&Block{Height: 1})
	q.Enqueue(&Block{Height: 2})

	if q.Dropped() != 0 {
		t.Fatalf("Dropped() = %d before overflow, want 0", q.Dropped())
	}

	// 5 overflow attempts.
	for i := 0; i < 5; i++ {
		q.Enqueue(&Block{Height: uint64(100 + i)})
	}

	if q.Dropped() != 5 {
		t.Errorf("Dropped() = %d, want 5", q.Dropped())
	}

	// Original items should be preserved.
	head := q.Peek()
	if head == nil || head.Block.Height != 1 {
		t.Error("Original first entry should be preserved after overflow")
	}
}

func TestBlockQueue_Overflow_OriginalItemsPreserved(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(3)
	q.Enqueue(&Block{Height: 10})
	q.Enqueue(&Block{Height: 20})
	q.Enqueue(&Block{Height: 30})

	// Try to add more.
	q.Enqueue(&Block{Height: 40})

	// Drain and verify original items.
	drained := q.DrainAll()
	if len(drained) != 3 {
		t.Fatalf("DrainAll returned %d entries, want 3", len(drained))
	}

	expectedHeights := []uint64{10, 20, 30}
	for i, entry := range drained {
		if entry.Block.Height != expectedHeights[i] {
			t.Errorf("Drained[%d].Height = %d, want %d", i, entry.Block.Height, expectedHeights[i])
		}
	}
}

func TestBlockQueue_DequeueFreesCapacity(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(2)
	q.Enqueue(&Block{Height: 1})
	q.Enqueue(&Block{Height: 2})

	// Queue is full.
	ok := q.Enqueue(&Block{Height: 3})
	if ok {
		t.Fatal("Should not enqueue when full")
	}

	// Dequeue one item.
	_, commitOne := q.DequeueWithCommit()
	commitOne()

	// Now there should be room.
	ok = q.Enqueue(&Block{Height: 3})
	if !ok {
		t.Error("Should be able to enqueue after dequeuing from full queue")
	}

	if q.Len() != 2 {
		t.Errorf("Len() = %d, want 2", q.Len())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Constructor edge cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestNewBlockQueue_ZeroMaxSize(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(0)

	// maxSize <= 0 should default to 100.
	// Verify by enqueuing 100 items successfully.
	for i := 0; i < 100; i++ {
		ok := q.Enqueue(&Block{Height: uint64(i)})
		if !ok {
			t.Fatalf("Enqueue failed at index %d (default capacity should be 100)", i)
		}
	}

	// 101st should fail.
	ok := q.Enqueue(&Block{Height: 100})
	if ok {
		t.Error("Enqueue beyond default capacity (100) should return false")
	}
}

func TestNewBlockQueue_NegativeMaxSize(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(-5)

	// Should default to 100.
	if q.Len() != 0 {
		t.Errorf("Len() on new queue = %d, want 0", q.Len())
	}

	// Verify default capacity works.
	ok := q.Enqueue(&Block{Height: 1})
	if !ok {
		t.Error("Enqueue should succeed on queue with negative maxSize (defaults to 100)")
	}
}

func TestNewBlockQueue_MaxSizeOne(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(1)

	ok := q.Enqueue(&Block{Height: 1})
	if !ok {
		t.Fatal("First enqueue should succeed with maxSize=1")
	}

	ok = q.Enqueue(&Block{Height: 2})
	if ok {
		t.Error("Second enqueue should fail with maxSize=1")
	}

	if q.Dropped() != 1 {
		t.Errorf("Dropped() = %d, want 1", q.Dropped())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Peek tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_Peek_DoesNotRemove(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	q.Enqueue(&Block{Height: 42})

	// Peek multiple times.
	for i := 0; i < 5; i++ {
		entry := q.Peek()
		if entry == nil || entry.Block.Height != 42 {
			t.Fatalf("Peek #%d: expected height 42, got %v", i+1, entryHeight2(entry))
		}
	}

	// Queue length should not change.
	if q.Len() != 1 {
		t.Errorf("Len() = %d after multiple Peeks, want 1", q.Len())
	}
}

func TestBlockQueue_Peek_Empty(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(10)
	if q.Peek() != nil {
		t.Error("Peek() on empty queue should return nil")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent access tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_ConcurrentEnqueueDequeue(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(1000)

	var wg sync.WaitGroup
	const goroutines = 20
	const opsPerGoroutine = 50

	var enqueued atomic.Int64
	var dequeued atomic.Int64

	// Concurrent enqueue.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				if q.Enqueue(&Block{Height: uint64(id*1000 + i)}) {
					enqueued.Add(1)
				}
			}
		}(g)
	}

	// Concurrent dequeue.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				entry, commit := q.DequeueWithCommit()
				if entry != nil {
					commit()
					dequeued.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	// Verify: enqueued - dequeued == remaining in queue.
	remaining := q.Len()
	expectedRemaining := int(enqueued.Load() - dequeued.Load())
	if remaining != expectedRemaining {
		t.Errorf("Remaining = %d, want %d (enqueued=%d, dequeued=%d)",
			remaining, expectedRemaining, enqueued.Load(), dequeued.Load())
	}
}

func TestBlockQueue_ConcurrentDequeueWithCommit(t *testing.T) {
	t.Parallel()

	const queueSize = 100
	q := NewBlockQueue(queueSize)

	for i := 0; i < queueSize; i++ {
		q.Enqueue(&Block{Height: uint64(i)})
	}

	var (
		mu             sync.Mutex
		committedCount int
		heights        = make(map[uint64]int) // Track how many times each height is committed.
		wg             sync.WaitGroup
	)

	const goroutines = 30
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for {
				entry, commit := q.DequeueWithCommit()
				if entry == nil {
					return
				}
				if committed := commit(); committed {
					mu.Lock()
					committedCount++
					heights[entry.Block.Height]++
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	if committedCount != queueSize {
		t.Errorf("committedCount = %d, want %d", committedCount, queueSize)
	}

	// Each height should be committed exactly once.
	for height, count := range heights {
		if count != 1 {
			t.Errorf("Height %d committed %d times, want exactly 1", height, count)
		}
	}

	if q.Len() != 0 {
		t.Errorf("Len() after concurrent drain = %d, want 0", q.Len())
	}
}

func TestBlockQueue_ConcurrentMixedOperations(t *testing.T) {
	t.Parallel()

	q := NewBlockQueue(500)

	var wg sync.WaitGroup
	const goroutines = 10
	const opsPerGoroutine = 50

	// Concurrent Enqueue.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				q.Enqueue(&Block{Height: uint64(id*1000 + i)})
			}
		}(g)
	}

	// Concurrent Peek.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_ = q.Peek()
			}
		}()
	}

	// Concurrent Len/Dropped.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_ = q.Len()
				_ = q.Dropped()
			}
		}()
	}

	// Concurrent UpdateEntryError.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				q.UpdateEntryError("concurrent error")
			}
		}()
	}

	wg.Wait()

	// Should not panic or deadlock. Just verify usability.
	_ = q.Len()
	_ = q.Dropped()
	_ = q.Peek()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Full lifecycle test: outage -> queue -> recovery -> drain
// ═══════════════════════════════════════════════════════════════════════════════

func TestBlockQueue_OutageRecoveryLifecycle(t *testing.T) {
	t.Parallel()

	// Simulate: circuit breaker opens, blocks queued, breaker closes, drain all.
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   5 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	})
	q := NewBlockQueue(100)

	// Phase 1: Normal operation.
	if cb.State() != CircuitClosed {
		t.Fatalf("Initial state should be CircuitClosed")
	}

	// Phase 2: DB failures trigger circuit open.
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("Circuit should be open after 3 failures")
	}

	// Phase 3: Blocks found during outage get queued.
	blocksFoundDuringOutage := 5
	for i := 0; i < blocksFoundDuringOutage; i++ {
		allowed, _ := cb.AllowRequest()
		if allowed {
			t.Fatal("Circuit should block requests while open")
		}
		// Queue the block instead of dropping it.
		ok := q.Enqueue(&Block{
			Height: uint64(10000 + i),
			Hash:   "outage_block",
			Status: "pending",
		})
		if !ok {
			t.Fatalf("Failed to enqueue block %d during outage", i)
		}
	}

	if q.Len() != blocksFoundDuringOutage {
		t.Errorf("Queue length during outage = %d, want %d", q.Len(), blocksFoundDuringOutage)
	}

	// Phase 4: Cooldown elapses, probe succeeds.
	time.Sleep(cb.Stats().CurrentBackoff + 60*time.Millisecond)
	allowed, _ := cb.AllowRequest()
	if !allowed {
		t.Fatal("Probe should be allowed after cooldown")
	}
	cb.RecordSuccess()

	if cb.State() != CircuitClosed {
		t.Fatalf("Circuit should be closed after successful probe")
	}

	// Phase 5: Drain queued blocks (simulate bulk flush).
	drained := q.DrainAll()
	if len(drained) != blocksFoundDuringOutage {
		t.Errorf("Drained %d blocks, want %d", len(drained), blocksFoundDuringOutage)
	}

	// Verify order preserved.
	for i, entry := range drained {
		expectedHeight := uint64(10000 + i)
		if entry.Block.Height != expectedHeight {
			t.Errorf("Drained[%d].Height = %d, want %d", i, entry.Block.Height, expectedHeight)
		}
	}

	// Phase 6: Queue is empty, ready for next outage.
	if q.Len() != 0 {
		t.Errorf("Queue should be empty after drain, got %d", q.Len())
	}
	if q.Dropped() != 0 {
		t.Errorf("No blocks should have been dropped, got %d", q.Dropped())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkBlockQueue_Enqueue(b *testing.B) {
	q := NewBlockQueue(b.N + 1)
	block := &Block{Height: 1}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Enqueue(block)
	}
}

func BenchmarkBlockQueue_Dequeue(b *testing.B) {
	q := NewBlockQueue(b.N + 1)
	block := &Block{Height: 1}
	for i := 0; i < b.N; i++ {
		q.Enqueue(block)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry, commit := q.DequeueWithCommit()
		if entry != nil {
			commit()
		}
	}
}

func BenchmarkBlockQueue_DequeueWithCommit(b *testing.B) {
	q := NewBlockQueue(b.N + 1)
	block := &Block{Height: 1}
	for i := 0; i < b.N; i++ {
		q.Enqueue(block)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry, commit := q.DequeueWithCommit()
		if entry != nil {
			commit()
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════════════

// entryHeight returns the block height from a BlockQueueEntry, or "nil" for nil entries.
func entryHeight(e *BlockQueueEntry) interface{} {
	if e == nil {
		return "nil"
	}
	return e.Block.Height
}

// entryHeight2 returns the block height or "nil" as a string for display.
func entryHeight2(e *BlockQueueEntry) interface{} {
	if e == nil {
		return "nil"
	}
	return e.Block.Height
}
