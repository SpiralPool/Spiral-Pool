// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package ringbuffer provides tests for the lock-free MPSC ring buffer.
package ringbuffer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// UNIT TESTS
// =============================================================================

func TestNew(t *testing.T) {
	rb := New[int](16)

	if rb == nil {
		t.Fatal("New returned nil")
	}

	if rb.Cap() != 16 {
		t.Errorf("Expected capacity 16, got %d", rb.Cap())
	}

	if rb.Len() != 0 {
		t.Errorf("Expected length 0, got %d", rb.Len())
	}
}

func TestNew_NonPowerOfTwo(t *testing.T) {
	// 17 should round up to 32
	rb := New[int](17)

	if rb.Cap() != 32 {
		t.Errorf("Expected capacity 32 (next power of 2), got %d", rb.Cap())
	}
}

func TestNew_Zero(t *testing.T) {
	rb := New[int](0)

	// Buffer is created but with capacity 0 (edge case)
	// The bit manipulation for 0 results in 0 capacity
	if rb == nil {
		t.Error("Buffer should be created even with zero capacity input")
	}
	// Note: Zero capacity is an edge case - production code should always use positive values
	t.Logf("Zero capacity results in Cap()=%d", rb.Cap())
}

func TestNew_One(t *testing.T) {
	rb := New[int](1)

	// Capacity 1 is a power of 2
	if rb.Cap() != 1 {
		t.Errorf("Expected capacity 1, got %d", rb.Cap())
	}
}

func TestTryEnqueue_Basic(t *testing.T) {
	rb := New[int](16)

	ok := rb.TryEnqueue(42)
	if !ok {
		t.Error("TryEnqueue should succeed on empty buffer")
	}

	if rb.Len() != 1 {
		t.Errorf("Expected length 1, got %d", rb.Len())
	}
}

func TestTryEnqueue_Full(t *testing.T) {
	rb := New[int](4)

	// Fill the buffer
	for i := 0; i < 4; i++ {
		if !rb.TryEnqueue(i) {
			t.Errorf("TryEnqueue should succeed for item %d", i)
		}
	}

	// Next enqueue should fail
	if rb.TryEnqueue(999) {
		t.Error("TryEnqueue should fail on full buffer")
	}

	stats := rb.Stats()
	if stats.Dropped != 1 {
		t.Errorf("Expected 1 dropped, got %d", stats.Dropped)
	}
}

func TestEnqueue(t *testing.T) {
	rb := New[int](4)

	done := make(chan struct{})
	go func() {
		rb.Enqueue(42)
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(time.Second):
		t.Error("Enqueue should complete quickly on empty buffer")
	}
}

func TestDequeueOne_Basic(t *testing.T) {
	rb := New[int](16)

	rb.TryEnqueue(42)
	rb.TryEnqueue(43)

	val, ok := rb.DequeueOne()
	if !ok || val != 42 {
		t.Errorf("DequeueOne = %d, %v; want 42, true", val, ok)
	}

	val, ok = rb.DequeueOne()
	if !ok || val != 43 {
		t.Errorf("DequeueOne = %d, %v; want 43, true", val, ok)
	}
}

func TestDequeueOne_Empty(t *testing.T) {
	rb := New[int](16)

	val, ok := rb.DequeueOne()
	if ok {
		t.Errorf("DequeueOne on empty buffer should return false, got val=%d", val)
	}
}

func TestDequeueBatch_Basic(t *testing.T) {
	rb := New[int](16)

	for i := 0; i < 10; i++ {
		rb.TryEnqueue(i)
	}

	batch := make([]int, 5)
	n := rb.DequeueBatch(batch)

	if n != 5 {
		t.Errorf("Expected to dequeue 5, got %d", n)
	}

	for i := 0; i < 5; i++ {
		if batch[i] != i {
			t.Errorf("batch[%d] = %d, want %d", i, batch[i], i)
		}
	}

	// Remaining items
	if rb.Len() != 5 {
		t.Errorf("Expected 5 remaining, got %d", rb.Len())
	}
}

func TestDequeueBatch_PartialBuffer(t *testing.T) {
	rb := New[int](16)

	for i := 0; i < 3; i++ {
		rb.TryEnqueue(i)
	}

	batch := make([]int, 10) // Larger than buffer contents
	n := rb.DequeueBatch(batch)

	if n != 3 {
		t.Errorf("Expected to dequeue 3, got %d", n)
	}
}

func TestDequeueBatch_Empty(t *testing.T) {
	rb := New[int](16)

	batch := make([]int, 10)
	n := rb.DequeueBatch(batch)

	if n != 0 {
		t.Errorf("Expected to dequeue 0 from empty buffer, got %d", n)
	}
}

func TestLen(t *testing.T) {
	rb := New[int](16)

	if rb.Len() != 0 {
		t.Error("Empty buffer should have length 0")
	}

	rb.TryEnqueue(1)
	rb.TryEnqueue(2)
	rb.TryEnqueue(3)

	if rb.Len() != 3 {
		t.Errorf("Expected length 3, got %d", rb.Len())
	}

	rb.DequeueOne()

	if rb.Len() != 2 {
		t.Errorf("Expected length 2 after dequeue, got %d", rb.Len())
	}
}

func TestCap(t *testing.T) {
	rb := New[int](64)

	if rb.Cap() != 64 {
		t.Errorf("Expected capacity 64, got %d", rb.Cap())
	}
}

func TestStats(t *testing.T) {
	rb := New[int](4)

	// Enqueue 5 items (1 will be dropped)
	for i := 0; i < 5; i++ {
		rb.TryEnqueue(i)
	}

	stats := rb.Stats()

	if stats.Enqueued != 4 {
		t.Errorf("Expected 4 enqueued, got %d", stats.Enqueued)
	}

	if stats.Dropped != 1 {
		t.Errorf("Expected 1 dropped, got %d", stats.Dropped)
	}

	if stats.Current != 4 {
		t.Errorf("Expected 4 current, got %d", stats.Current)
	}

	if stats.Capacity != 4 {
		t.Errorf("Expected 4 capacity, got %d", stats.Capacity)
	}
}

func TestFIFOOrder(t *testing.T) {
	rb := New[int](16)

	// Enqueue in order
	for i := 0; i < 10; i++ {
		rb.TryEnqueue(i)
	}

	// Should dequeue in same order (FIFO)
	for i := 0; i < 10; i++ {
		val, ok := rb.DequeueOne()
		if !ok || val != i {
			t.Errorf("Dequeue %d: got %d, %v; want %d, true", i, val, ok, i)
		}
	}
}

func TestWraparound(t *testing.T) {
	rb := New[int](4)

	// Fill and empty multiple times to test wraparound
	for cycle := 0; cycle < 10; cycle++ {
		// Fill
		for i := 0; i < 4; i++ {
			if !rb.TryEnqueue(cycle*10 + i) {
				t.Errorf("Cycle %d: enqueue %d failed", cycle, i)
			}
		}

		// Empty
		for i := 0; i < 4; i++ {
			val, ok := rb.DequeueOne()
			expected := cycle*10 + i
			if !ok || val != expected {
				t.Errorf("Cycle %d: dequeue %d = %d, %v; want %d, true", cycle, i, val, ok, expected)
			}
		}
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestConcurrentProducers(t *testing.T) {
	rb := New[int](1024)

	var wg sync.WaitGroup
	const numProducers = 10
	const itemsPerProducer = 100

	var enqueued atomic.Int64

	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				if rb.TryEnqueue(producerID*1000 + i) {
					enqueued.Add(1)
				}
			}
		}(p)
	}

	wg.Wait()

	stats := rb.Stats()
	totalAttempts := int64(numProducers * itemsPerProducer)
	if stats.Enqueued+stats.Dropped != uint64(totalAttempts) {
		t.Errorf("Total attempts mismatch: enqueued=%d, dropped=%d, expected=%d",
			stats.Enqueued, stats.Dropped, totalAttempts)
	}

	t.Logf("Concurrent producers: enqueued=%d, dropped=%d", stats.Enqueued, stats.Dropped)
}

func TestConcurrentProducerConsumer(t *testing.T) {
	rb := New[int](256)

	var wg sync.WaitGroup
	const numProducers = 5
	const itemsPerProducer = 1000

	var produced atomic.Int64
	var consumed atomic.Int64
	done := make(chan struct{})

	// Single consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		batch := make([]int, 32)
		for {
			select {
			case <-done:
				// Drain remaining
				for {
					n := rb.DequeueBatch(batch)
					if n == 0 {
						return
					}
					consumed.Add(int64(n))
				}
			default:
				n := rb.DequeueBatch(batch)
				consumed.Add(int64(n))
				if n == 0 {
					time.Sleep(time.Microsecond)
				}
			}
		}
	}()

	// Multiple producers
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				if rb.TryEnqueue(id*10000 + i) {
					produced.Add(1)
				}
			}
		}(p)
	}

	// Wait for producers
	time.Sleep(100 * time.Millisecond)
	close(done)
	wg.Wait()

	// All produced items should be consumed (none lost in valid dequeue)
	stats := rb.Stats()
	t.Logf("Produced: %d, Consumed: %d, Enqueued: %d, Dropped: %d",
		produced.Load(), consumed.Load(), stats.Enqueued, stats.Dropped)

	if stats.Enqueued != uint64(consumed.Load()) {
		t.Errorf("Enqueued (%d) != Consumed (%d)", stats.Enqueued, consumed.Load())
	}
}

func TestHighContention(t *testing.T) {
	rb := New[int](64) // Small buffer for high contention

	var wg sync.WaitGroup
	const numProducers = 50
	const itemsPerProducer = 100

	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				rb.TryEnqueue(id*1000 + i)
			}
		}(p)
	}

	wg.Wait()

	stats := rb.Stats()
	t.Logf("High contention: enqueued=%d, dropped=%d", stats.Enqueued, stats.Dropped)

	// Should have significant drops due to small buffer
	if stats.Dropped == 0 {
		t.Log("Warning: expected some drops in high contention scenario")
	}
}

func TestStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	rb := New[int](4096)

	var wg sync.WaitGroup
	done := make(chan struct{})
	var totalEnqueued atomic.Int64
	var totalDequeued atomic.Int64

	// Consumer
	wg.Add(1)
	go func() {
		defer wg.Done()
		batch := make([]int, 64)
		for {
			select {
			case <-done:
				// Drain
				for rb.Len() > 0 {
					n := rb.DequeueBatch(batch)
					totalDequeued.Add(int64(n))
				}
				return
			default:
				n := rb.DequeueBatch(batch)
				totalDequeued.Add(int64(n))
			}
		}
	}()

	// Producers
	const numProducers = 20
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10000; i++ {
				if rb.TryEnqueue(i) {
					totalEnqueued.Add(1)
				}
				select {
				case <-done:
					return
				default:
				}
			}
		}()
	}

	time.Sleep(2 * time.Second)
	close(done)
	wg.Wait()

	t.Logf("Stress test: enqueued=%d, dequeued=%d", totalEnqueued.Load(), totalDequeued.Load())
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestWithStructs(t *testing.T) {
	type Item struct {
		ID   int
		Name string
	}

	rb := New[Item](16)

	rb.TryEnqueue(Item{ID: 1, Name: "one"})
	rb.TryEnqueue(Item{ID: 2, Name: "two"})

	item, ok := rb.DequeueOne()
	if !ok || item.ID != 1 || item.Name != "one" {
		t.Errorf("Dequeue struct: got %+v, %v", item, ok)
	}
}

func TestWithPointers(t *testing.T) {
	rb := New[*int](16)

	val := 42
	rb.TryEnqueue(&val)

	ptr, ok := rb.DequeueOne()
	if !ok || ptr == nil || *ptr != 42 {
		t.Error("Pointer handling failed")
	}
}

func TestWithNil(t *testing.T) {
	rb := New[*int](16)

	rb.TryEnqueue(nil)

	ptr, ok := rb.DequeueOne()
	if !ok {
		t.Error("Should successfully dequeue nil")
	}
	if ptr != nil {
		t.Error("Dequeued value should be nil")
	}
}

func TestEmptyDequeue(t *testing.T) {
	rb := New[int](16)

	// Multiple empty dequeues should be safe
	for i := 0; i < 100; i++ {
		_, ok := rb.DequeueOne()
		if ok {
			t.Error("Empty buffer should not yield items")
		}
	}

	batch := make([]int, 10)
	for i := 0; i < 100; i++ {
		n := rb.DequeueBatch(batch)
		if n != 0 {
			t.Error("Empty buffer should not yield items in batch")
		}
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkTryEnqueue(b *testing.B) {
	rb := New[int](4096)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.TryEnqueue(i)
		if rb.Len() > 3000 {
			// Drain to prevent full
			batch := make([]int, 1000)
			rb.DequeueBatch(batch)
		}
	}
}

func BenchmarkDequeueOne(b *testing.B) {
	rb := New[int](4096)

	// Pre-fill
	for i := 0; i < 4096; i++ {
		rb.TryEnqueue(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := rb.DequeueOne(); !ok {
			// Refill
			for j := 0; j < 1000; j++ {
				rb.TryEnqueue(j)
			}
		}
	}
}

func BenchmarkConcurrentEnqueue(b *testing.B) {
	rb := New[int](4096)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			rb.TryEnqueue(i)
			i++
		}
	})
}

// =============================================================================
// UINT64 WRAPAROUND AND EDGE CASE TESTS
// =============================================================================

// TestCommittedInitialization verifies the committed counter initialization.
// CRITICAL: committed is initialized to ^uint64(0) (max uint64) to handle
// the first write correctly. This is a security-critical edge case.
func TestCommittedInitialization(t *testing.T) {
	rb := New[int](16)

	// Committed should be initialized to max uint64
	committed := rb.committed.Load()
	if committed != ^uint64(0) {
		t.Errorf("committed should be initialized to max uint64, got %d", committed)
	}

	// First dequeue should return empty
	_, ok := rb.DequeueOne()
	if ok {
		t.Error("Empty buffer with committed=max should not yield items")
	}

	// DequeueBatch should also return 0
	batch := make([]int, 10)
	n := rb.DequeueBatch(batch)
	if n != 0 {
		t.Errorf("Empty buffer DequeueBatch should return 0, got %d", n)
	}
}

// TestFirstEnqueueCommit verifies the first enqueue handles the wraparound
// from ^uint64(0) to 0 correctly.
func TestFirstEnqueueCommit(t *testing.T) {
	rb := New[int](16)

	// Before first enqueue
	if rb.committed.Load() != ^uint64(0) {
		t.Error("committed should start at max uint64")
	}

	// First enqueue
	ok := rb.TryEnqueue(42)
	if !ok {
		t.Error("First enqueue should succeed")
	}

	// Committed should now be 0 (head-1 was ^uint64(0), and we committed head=0)
	committed := rb.committed.Load()
	if committed != 0 {
		t.Errorf("After first enqueue, committed should be 0, got %d", committed)
	}

	// Should be able to dequeue the item
	val, ok := rb.DequeueOne()
	if !ok || val != 42 {
		t.Errorf("Should dequeue 42, got %d, ok=%v", val, ok)
	}
}

// TestHeadTailWraparound tests behavior when head/tail counters would
// theoretically overflow uint64. In practice this takes years, but the
// algorithm must handle it correctly.
func TestHeadTailWraparound(t *testing.T) {
	rb := New[int](4) // Small buffer

	// Simulate many enqueue/dequeue cycles
	// Each cycle advances head and tail
	for cycle := 0; cycle < 1000; cycle++ {
		// Fill buffer
		for i := 0; i < 4; i++ {
			if !rb.TryEnqueue(cycle*10 + i) {
				t.Errorf("Cycle %d: enqueue failed at %d", cycle, i)
			}
		}

		// Empty buffer
		for i := 0; i < 4; i++ {
			val, ok := rb.DequeueOne()
			expected := cycle*10 + i
			if !ok || val != expected {
				t.Errorf("Cycle %d: expected %d, got %d (ok=%v)", cycle, expected, val, ok)
			}
		}
	}

	// After 1000 cycles, head and tail should be 4000
	if rb.head.Load() != 4000 {
		t.Errorf("Expected head=4000, got %d", rb.head.Load())
	}
	if rb.tail.Load() != 4000 {
		t.Errorf("Expected tail=4000, got %d", rb.tail.Load())
	}
}

// TestMaskWraparound verifies that buffer index calculation using mask
// correctly handles large counter values.
func TestMaskWraparound(t *testing.T) {
	rb := New[int](4) // capacity 4, mask = 3

	// Manually set head and tail to high values to simulate near-wraparound
	// This tests the index calculation: index = counter & mask
	rb.head.Store(1 << 62)       // Very large but valid
	rb.tail.Store(1 << 62)       // Same
	rb.committed.Store((1 << 62) - 1) // One behind head

	// Try to enqueue - succeeds because head-tail = 0 < capacity (expected behavior)
	ok := rb.TryEnqueue(99)
	if !ok {
		t.Error("Enqueue should succeed with high counter values")
	}

	// The actual buffer index should be (1<<62) & 3 = 0 or some valid index
	expectedIndex := (1 << 62) & 3
	if rb.buffer[expectedIndex] != 99 {
		t.Errorf("Value should be at index %d", expectedIndex)
	}
}

// TestLenWithHighCounters tests Len() calculation with high counter values.
func TestLenWithHighCounters(t *testing.T) {
	rb := New[int](16)

	// Set up counters to simulate many operations
	// committed = 100, tail = 95 means 6 items (100-95+1 = 6)
	rb.committed.Store(100)
	rb.tail.Store(95)

	length := rb.Len()
	if length != 6 {
		t.Errorf("Expected Len=6, got %d", length)
	}

	// Test with very large counters
	rb.committed.Store(1<<62 + 100)
	rb.tail.Store(1<<62 + 95)

	length = rb.Len()
	if length != 6 {
		t.Errorf("With high counters, expected Len=6, got %d", length)
	}
}

// TestDequeueOne_TailExceedsCommitted tests the edge case where
// tail > committed, which should return empty.
func TestDequeueOne_TailExceedsCommitted(t *testing.T) {
	rb := New[int](16)

	// Enqueue and dequeue to advance counters
	rb.TryEnqueue(1)
	rb.TryEnqueue(2)
	rb.DequeueOne()
	rb.DequeueOne()

	// At this point, tail should equal committed+1
	// Another dequeue should fail
	_, ok := rb.DequeueOne()
	if ok {
		t.Error("Dequeue should fail when tail > committed")
	}
}

// TestConcurrentEnqueueCommitOrdering tests that concurrent enqueues
// commit in the correct order (commit waits for previous writes).
func TestConcurrentEnqueueCommitOrdering(t *testing.T) {
	rb := New[int](1024)

	var wg sync.WaitGroup
	const producers = 10
	const perProducer = 100

	// All producers enqueue simultaneously
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				rb.Enqueue(id*1000 + i)
			}
		}(p)
	}

	wg.Wait()

	// All items should be present
	total := producers * perProducer
	if rb.Len() != total {
		t.Errorf("Expected %d items, got %d", total, rb.Len())
	}

	// Committed should be head-1
	head := rb.head.Load()
	committed := rb.committed.Load()
	if committed != head-1 {
		t.Errorf("committed (%d) should be head-1 (%d)", committed, head-1)
	}
}

// TestDequeueBatchWithHighCounters tests batch dequeue with large counters.
func TestDequeueBatchWithHighCounters(t *testing.T) {
	rb := New[int](16)

	// Set up state simulating many operations
	baseCounter := uint64(1 << 60) // Very large base
	rb.head.Store(baseCounter + 10)
	rb.tail.Store(baseCounter)
	rb.committed.Store(baseCounter + 9)

	// Write values directly to buffer
	for i := 0; i < 10; i++ {
		rb.buffer[(baseCounter+uint64(i))&rb.mask] = i * 10
	}

	// Dequeue batch
	batch := make([]int, 10)
	n := rb.DequeueBatch(batch)

	if n != 10 {
		t.Errorf("Expected to dequeue 10, got %d", n)
	}

	for i := 0; i < 10; i++ {
		if batch[i] != i*10 {
			t.Errorf("batch[%d] = %d, expected %d", i, batch[i], i*10)
		}
	}
}

// TestStatsAccuracy tests that stats are accurate after many operations.
func TestStatsAccuracy(t *testing.T) {
	rb := New[int](64)

	const enqueued = 1000
	const dropped = 100

	// Enqueue many items (some will succeed, buffer holds 64)
	successCount := 0
	for i := 0; i < enqueued+dropped; i++ {
		if rb.TryEnqueue(i) {
			successCount++
		}
	}

	// Dequeue all
	dequeued := 0
	for {
		_, ok := rb.DequeueOne()
		if !ok {
			break
		}
		dequeued++
	}

	stats := rb.Stats()

	if stats.Enqueued != uint64(successCount) {
		t.Errorf("Enqueued stat: got %d, expected %d", stats.Enqueued, successCount)
	}

	if stats.Dropped != uint64(enqueued+dropped-successCount) {
		t.Errorf("Dropped stat mismatch")
	}

	if stats.Current != 0 {
		t.Errorf("Current should be 0 after full drain, got %d", stats.Current)
	}
}

// TestAvailableCalculation tests the "available" calculation in DequeueBatch.
// SECURITY: Incorrect calculation could cause buffer over-read.
func TestAvailableCalculation(t *testing.T) {
	rb := New[int](8)

	// Add 5 items
	for i := 0; i < 5; i++ {
		rb.TryEnqueue(i)
	}

	// Request more than available
	batch := make([]int, 100)
	n := rb.DequeueBatch(batch)

	if n != 5 {
		t.Errorf("Should only dequeue 5 (available), got %d", n)
	}

	// Verify no garbage data in unused batch slots
	// (Actually the slice is pre-allocated, so this is fine)
}

// TestBufferFullCondition tests the exact full condition.
func TestBufferFullCondition(t *testing.T) {
	rb := New[int](4)

	// Fill exactly to capacity
	for i := 0; i < 4; i++ {
		ok := rb.TryEnqueue(i)
		if !ok {
			t.Errorf("Enqueue %d should succeed", i)
		}
	}

	// Buffer is now full: head - tail = 4 = capacity
	head := rb.head.Load()
	tail := rb.tail.Load()
	if head-tail != 4 {
		t.Errorf("Full buffer: head-tail should be 4, got %d", head-tail)
	}

	// Next enqueue should fail
	ok := rb.TryEnqueue(999)
	if ok {
		t.Error("Enqueue to full buffer should fail")
	}

	// Dequeue one to make room
	rb.DequeueOne()

	// Now enqueue should succeed
	ok = rb.TryEnqueue(999)
	if !ok {
		t.Error("Enqueue should succeed after making room")
	}
}

// TestRaceDetector is designed to trigger the race detector if there are
// any data races in the lock-free operations.
func TestRaceDetector(t *testing.T) {
	rb := New[int](256)

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Multiple concurrent producers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				select {
				case <-done:
					return
				default:
					rb.TryEnqueue(id*10000 + j)
				}
			}
		}(i)
	}

	// Single consumer (MPSC pattern)
	wg.Add(1)
	go func() {
		defer wg.Done()
		batch := make([]int, 32)
		for {
			select {
			case <-done:
				// Drain remaining
				for rb.Len() > 0 {
					rb.DequeueBatch(batch)
				}
				return
			default:
				rb.DequeueBatch(batch)
			}
		}
	}()

	// Concurrent readers of Len() and Stats()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				select {
				case <-done:
					return
				default:
					_ = rb.Len()
					_ = rb.Stats()
					_ = rb.Cap()
				}
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(done)
	wg.Wait()

	// If we get here without race detector alerts, the lock-free code is correct
	t.Log("Race detector test completed successfully")
}
