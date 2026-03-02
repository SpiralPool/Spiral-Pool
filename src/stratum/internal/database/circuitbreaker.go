// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database provides circuit breaker for database operations.
//
// Implements the circuit breaker pattern to prevent cascading failures
// when the database becomes unavailable. States:
//   - Closed: Normal operation, requests flow through
//   - Open: Failures exceeded threshold, requests blocked for cooldown
//   - HalfOpen: After cooldown, allow single probe request
package database

import (
	"sync"
	"time"
)

// CircuitState represents the circuit breaker state.
type CircuitState int32

const (
	// CircuitClosed is the normal operational state.
	CircuitClosed CircuitState = iota
	// CircuitOpen blocks requests after failure threshold exceeded.
	CircuitOpen
	// CircuitHalfOpen allows a single probe request after cooldown.
	CircuitHalfOpen
)

// String returns a human-readable state name.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig defines circuit breaker behavior.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of failures before opening the circuit.
	FailureThreshold int

	// CooldownPeriod is how long the circuit stays open before allowing a probe.
	CooldownPeriod time.Duration

	// InitialBackoff is the initial retry delay after a failure.
	InitialBackoff time.Duration

	// MaxBackoff is the maximum retry delay (caps exponential growth).
	MaxBackoff time.Duration

	// BackoffFactor is the multiplier for exponential backoff (typically 2.0).
	BackoffFactor float64
}

// DefaultCircuitBreakerConfig returns production defaults.
// Matches existing pipeline thresholds: 20 failures = critical.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 20,               // Match MaxDBWriteFailuresCritical
		CooldownPeriod:   30 * time.Second, // Time before probe attempt
		InitialBackoff:   1 * time.Second,  // Start with 1s delay
		MaxBackoff:       16 * time.Second, // Cap at 16s (1->2->4->8->16)
		BackoffFactor:    2.0,              // Double each time
	}
}

// CircuitBreakerStats contains circuit breaker statistics for monitoring.
type CircuitBreakerStats struct {
	State          CircuitState
	Failures       int
	StateChanges   uint64
	TotalBlocked   uint64
	CurrentBackoff time.Duration
}

// CircuitBreaker implements the circuit breaker pattern for database operations.
// It is safe for concurrent use.
type CircuitBreaker struct {
	cfg CircuitBreakerConfig

	mu              sync.RWMutex
	state           CircuitState
	failures        int
	lastFailureTime time.Time
	openedAt        time.Time
	currentBackoff  time.Duration

	// Metrics counters (accessed under mutex)
	stateChanges uint64
	totalBlocked uint64
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		cfg:            cfg,
		state:          CircuitClosed,
		currentBackoff: cfg.InitialBackoff,
	}
}

// AllowRequest checks if a request should proceed.
//
// Returns:
//   - allowed: true if the request can proceed
//   - backoff: the delay to use before the next retry (if allowed)
//
// When the circuit is open, backoff contains the remaining cooldown time.
// When half-open, additional requests are blocked to allow single probe.
func (cb *CircuitBreaker) AllowRequest() (bool, time.Duration) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true, cb.currentBackoff

	case CircuitOpen:
		// Check if cooldown has passed
		elapsed := time.Since(cb.openedAt)
		if elapsed >= cb.cfg.CooldownPeriod {
			// Transition to half-open for probe
			cb.state = CircuitHalfOpen
			cb.stateChanges++
			return true, 0 // Probe request, no initial backoff
		}
		remaining := cb.cfg.CooldownPeriod - elapsed
		cb.totalBlocked++
		return false, remaining

	case CircuitHalfOpen:
		// Only one probe at a time - block additional requests
		cb.totalBlocked++
		return false, cb.cfg.CooldownPeriod / 2
	}

	return true, cb.currentBackoff
}

// RecordSuccess records a successful operation.
// Resets the circuit to closed state and clears failure counters.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	wasOpen := cb.state != CircuitClosed

	cb.failures = 0
	cb.currentBackoff = cb.cfg.InitialBackoff
	cb.state = CircuitClosed

	if wasOpen {
		cb.stateChanges++
	}
}

// RecordFailure records a failed operation.
// Increases backoff exponentially and may open the circuit.
// Returns the backoff duration to wait before the next retry.
func (cb *CircuitBreaker) RecordFailure() time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailureTime = time.Now()

	// Capture current backoff before increasing
	backoff := cb.currentBackoff

	// Calculate next backoff with exponential increase
	cb.currentBackoff = time.Duration(float64(cb.currentBackoff) * cb.cfg.BackoffFactor)
	if cb.currentBackoff > cb.cfg.MaxBackoff {
		cb.currentBackoff = cb.cfg.MaxBackoff
	}

	// Check if we should open the circuit
	if cb.failures >= cb.cfg.FailureThreshold && cb.state == CircuitClosed {
		cb.state = CircuitOpen
		cb.openedAt = time.Now()
		cb.stateChanges++
	}

	return backoff
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Failures returns the current consecutive failure count.
func (cb *CircuitBreaker) Failures() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.failures
}

// Stats returns circuit breaker statistics for monitoring.
func (cb *CircuitBreaker) Stats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:          cb.state,
		Failures:       cb.failures,
		StateChanges:   cb.stateChanges,
		TotalBlocked:   cb.totalBlocked,
		CurrentBackoff: cb.currentBackoff,
	}
}

// Reset forces the circuit breaker back to initial closed state.
// Use with caution - primarily for testing.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = CircuitClosed
	cb.failures = 0
	cb.currentBackoff = cb.cfg.InitialBackoff
}

// ═══════════════════════════════════════════════════════════════════════════════
// P2 AUDIT FIX: BlockQueue for holding blocks during circuit breaker open state
// ═══════════════════════════════════════════════════════════════════════════════
// When the database circuit breaker is open, blocks would normally be lost.
// This queue holds pending block inserts until the DB is available again.
// The queue has a max size to prevent unbounded memory growth.

// BlockQueueEntry represents a block waiting to be inserted into the database.
type BlockQueueEntry struct {
	Block     *Block    // The block to insert
	QueuedAt  time.Time // When the block was queued
	Attempts  int       // Number of insertion attempts
	LastError string    // Last error message
}

// BlockQueue holds blocks during circuit breaker outages.
// It is safe for concurrent use.
type BlockQueue struct {
	mu       sync.Mutex
	entries  []*BlockQueueEntry
	maxSize  int
	dropped  uint64 // Counter for dropped blocks due to queue overflow
}

// NewBlockQueue creates a new block queue with the given max size.
// Default maxSize of 100 is reasonable for most solo pools.
func NewBlockQueue(maxSize int) *BlockQueue {
	if maxSize <= 0 {
		maxSize = 100 // Default max queue size
	}
	return &BlockQueue{
		entries: make([]*BlockQueueEntry, 0),
		maxSize: maxSize,
	}
}

// Enqueue adds a block to the queue.
// Returns false if the queue is full (block will be lost - log this!).
func (bq *BlockQueue) Enqueue(block *Block) bool {
	bq.mu.Lock()
	defer bq.mu.Unlock()

	if len(bq.entries) >= bq.maxSize {
		bq.dropped++
		return false // Queue full, block would be lost
	}

	bq.entries = append(bq.entries, &BlockQueueEntry{
		Block:    block,
		QueuedAt: time.Now(),
		Attempts: 0,
	})
	return true
}

// Dequeue removes and returns the oldest block from the queue.
// Returns nil if the queue is empty.
// DEPRECATED: Use DequeueWithCommit for crash-safe processing.
func (bq *BlockQueue) Dequeue() *BlockQueueEntry {
	bq.mu.Lock()
	defer bq.mu.Unlock()

	if len(bq.entries) == 0 {
		return nil
	}

	entry := bq.entries[0]
	bq.entries = bq.entries[1:]
	return entry
}

// DequeueWithCommit provides crash-safe block processing.
// FIX C-1: Returns the oldest block and a commit function.
// The block is NOT removed until commit() is called.
// commit() returns true if the entry was actually removed, false if another
// consumer already committed it (prevents double-count in concurrent drains).
// Usage:
//
//	entry, commit := queue.DequeueWithCommit()
//	if entry != nil {
//	    err := db.InsertBlock(entry.Block)
//	    if err == nil {
//	        commit() // Only remove after successful write
//	    }
//	}
//
// This prevents block loss if crash occurs between dequeue and DB write.
func (bq *BlockQueue) DequeueWithCommit() (*BlockQueueEntry, func() bool) {
	bq.mu.Lock()
	defer bq.mu.Unlock()

	if len(bq.entries) == 0 {
		return nil, func() bool { return false }
	}

	entry := bq.entries[0]

	// Return commit function that removes the entry
	commit := func() bool {
		bq.mu.Lock()
		defer bq.mu.Unlock()
		// Verify entry is still first (defensive against double-dequeue)
		if len(bq.entries) > 0 && bq.entries[0] == entry {
			bq.entries = bq.entries[1:]
			return true
		}
		return false
	}

	return entry, commit
}

// Peek returns the oldest block without removing it.
// Returns nil if the queue is empty.
func (bq *BlockQueue) Peek() *BlockQueueEntry {
	bq.mu.Lock()
	defer bq.mu.Unlock()

	if len(bq.entries) == 0 {
		return nil
	}
	return bq.entries[0]
}

// Len returns the current queue length.
func (bq *BlockQueue) Len() int {
	bq.mu.Lock()
	defer bq.mu.Unlock()
	return len(bq.entries)
}

// Dropped returns the count of blocks dropped due to queue overflow.
func (bq *BlockQueue) Dropped() uint64 {
	bq.mu.Lock()
	defer bq.mu.Unlock()
	return bq.dropped
}

// DrainAll removes and returns all entries from the queue.
// Useful for bulk processing when the circuit breaker closes.
func (bq *BlockQueue) DrainAll() []*BlockQueueEntry {
	bq.mu.Lock()
	defer bq.mu.Unlock()

	if len(bq.entries) == 0 {
		return nil
	}

	all := bq.entries
	bq.entries = make([]*BlockQueueEntry, 0)
	return all
}

// UpdateEntryError updates the last error for the oldest entry.
// Used after a failed insertion attempt.
func (bq *BlockQueue) UpdateEntryError(err string) {
	bq.mu.Lock()
	defer bq.mu.Unlock()

	if len(bq.entries) > 0 {
		bq.entries[0].Attempts++
		bq.entries[0].LastError = err
	}
}
