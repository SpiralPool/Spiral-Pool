// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package ringbuffer provides a lock-free MPSC (Multi-Producer Single-Consumer) ring buffer
// optimized for high-throughput share processing in mining pools.
package ringbuffer

import (
	"runtime"
	"sync/atomic"
)

// RingBuffer is a lock-free MPSC ring buffer.
// Multiple goroutines can safely enqueue items concurrently,
// while a single goroutine consumes items in batches.
type RingBuffer[T any] struct {
	buffer    []T
	mask      uint64
	head      atomic.Uint64 // Write position (producers)
	tail      atomic.Uint64 // Read position (consumer)
	committed atomic.Uint64 // Last committed write position

	// Metrics
	enqueued atomic.Uint64
	dropped  atomic.Uint64
}

// New creates a new RingBuffer with the specified capacity.
// Capacity must be a power of 2 for efficient modulo operations.
// Minimum capacity is 1 (rounded up to 1).
func New[T any](capacity int) *RingBuffer[T] {
	// Ensure capacity is at least 1 to prevent zero-size buffer
	if capacity <= 0 {
		capacity = 1
	}
	// Ensure capacity is power of 2
	if (capacity & (capacity - 1)) != 0 {
		// Round up to next power of 2
		capacity--
		capacity |= capacity >> 1
		capacity |= capacity >> 2
		capacity |= capacity >> 4
		capacity |= capacity >> 8
		capacity |= capacity >> 16
		capacity++
	}

	rb := &RingBuffer[T]{
		buffer: make([]T, capacity),
		mask:   uint64(capacity - 1),
	}
	rb.committed.Store(^uint64(0)) // Initialize to max uint64 (wraps to 0 on first commit)
	return rb
}

// TryEnqueue attempts to add an item to the buffer.
// Returns true if successful, false if the buffer is full.
// This method is lock-free and safe for concurrent use by multiple producers.
func (rb *RingBuffer[T]) TryEnqueue(item T) bool {
	for {
		head := rb.head.Load()
		tail := rb.tail.Load()

		// Check if buffer is full
		if head-tail >= uint64(len(rb.buffer)) {
			rb.dropped.Add(1)
			return false
		}

		// Try to claim a slot
		if rb.head.CompareAndSwap(head, head+1) {
			// Write item to claimed slot
			rb.buffer[head&rb.mask] = item

			// Wait for previous writes to commit, then commit ours
			for {
				expected := head - 1
				if head == 0 {
					expected = ^uint64(0) // Handle wraparound
				}
				if rb.committed.CompareAndSwap(expected, head) {
					break
				}
				runtime.Gosched()
			}

			rb.enqueued.Add(1)
			return true
		}
		// CAS failed, retry
		runtime.Gosched()
	}
}

// Enqueue adds an item to the buffer, spinning until successful.
// Use TryEnqueue for non-blocking behavior with backpressure.
func (rb *RingBuffer[T]) Enqueue(item T) {
	for !rb.TryEnqueue(item) {
		runtime.Gosched()
	}
}

// DequeueBatch retrieves up to len(batch) items from the buffer.
// Returns the number of items dequeued.
// This method should only be called by a single consumer goroutine.
func (rb *RingBuffer[T]) DequeueBatch(batch []T) int {
	tail := rb.tail.Load()
	committed := rb.committed.Load()

	// Handle uninitialized case (no items committed yet)
	if committed == ^uint64(0) {
		return 0
	}

	// Handle case where tail has caught up to or passed committed
	// This uses signed comparison to handle wraparound correctly
	if int64(committed)-int64(tail) < 0 {
		return 0
	}

	// Calculate available items (committed is inclusive, so +1)
	available := committed - tail + 1

	n := min(available, uint64(len(batch)))
	for i := uint64(0); i < n; i++ {
		batch[i] = rb.buffer[(tail+i)&rb.mask]
	}

	rb.tail.Add(n)
	return int(n)
}

// DequeueOne retrieves a single item from the buffer.
// Returns the item and true if successful, zero value and false if empty.
func (rb *RingBuffer[T]) DequeueOne() (T, bool) {
	tail := rb.tail.Load()
	committed := rb.committed.Load()

	var zero T
	if committed == ^uint64(0) || tail > committed {
		return zero, false
	}

	item := rb.buffer[tail&rb.mask]
	rb.tail.Add(1)
	return item, true
}

// Len returns the current number of items in the buffer.
func (rb *RingBuffer[T]) Len() int {
	committed := rb.committed.Load()
	tail := rb.tail.Load()
	if committed == ^uint64(0) {
		return 0
	}
	// Use signed comparison to handle wraparound correctly
	diff := int64(committed) - int64(tail)
	if diff < 0 {
		return 0
	}
	return int(diff + 1)
}

// Cap returns the buffer capacity.
func (rb *RingBuffer[T]) Cap() int {
	return len(rb.buffer)
}

// Stats returns buffer statistics.
type Stats struct {
	Enqueued uint64
	Dropped  uint64
	Current  int
	Capacity int
}

func (rb *RingBuffer[T]) Stats() Stats {
	return Stats{
		Enqueued: rb.enqueued.Load(),
		Dropped:  rb.dropped.Load(),
		Current:  rb.Len(),
		Capacity: rb.Cap(),
	}
}

