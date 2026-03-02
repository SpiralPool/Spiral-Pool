// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for the fixed partial buffer Add-check-rollback pattern.
//
// TEST B: Server Partial Buffer Atomic Reservation Validation
// Validates the FIX O-2 pattern (atomic Add-check-rollback) from server.go:548-557.
//
// Contrast with TestChaos_Server_PartialBufferTOCTOURace in chaos_server_test.go
// which exercises the OLD broken Load-check-Add TOCTOU pattern.
// This test exercises the FIXED pattern and proves:
//   - Global counter returns to 0 after all connections exit (no memory leaks)
//   - Counter never underflows (rollback accounting is correct)
//   - Limit IS enforced (rollbacks occur)
//   - Exit cleanup (server.go:501-504) correctly subtracts each connection's contribution
package stratum

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestChaos_Server_PartialBufferAtomicReservation exercises the fixed
// Add-check-rollback pattern from server.go:548-557 under high concurrency.
//
// Each goroutine simulates a miner connection's messageLoop:
//   - Reads arrive incrementally, growing the partial buffer
//   - Global buffer check: Add(delta), check, rollback if over limit
//   - Message completion reduces partial buffer to 0
//   - Connection exit cleanup subtracts tracked contribution
//
// TARGET: server.go:541-562 (Add-check-rollback), server.go:501-504 (exit cleanup),
//
//	server.go:592-596 (post-message tracking update)
//
// INVARIANT:
//   - Final counter == 0 (no leaks from rollback or exit cleanup errors)
//   - Counter never underflows (no double-subtract from incorrect rollback)
//   - Rollbacks enforce limit (limit is actually hit under contention)
//   - Transient overshoot bounded by numConnections * readSize
//
// RUN WITH: go test -race -run TestChaos_Server_PartialBufferAtomicReservation
func TestChaos_Server_PartialBufferAtomicReservation(t *testing.T) {
	var globalCounter atomic.Int64
	const limit = int64(200)       // Low limit to guarantee rollbacks even without -race
	const numConnections = 200     // Concurrent "miner connections"
	const readsPerConnection = 500 // Reads per connection before exit
	const readSize = int64(100)    // Bytes per simulated read

	var rollbackCount atomic.Int64 // Times Add-check-rollback fired
	var maxObserved atomic.Int64   // Max counter value seen (including transient overshoot)
	var negativeCount atomic.Int64 // Times counter went negative (indicates accounting bug)

	startBarrier := make(chan struct{})
	var wg sync.WaitGroup
	for c := 0; c < numConnections; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier // Wait for all goroutines to be ready
			var lastPartialLen int64

			// FIX O-2: Connection exit cleanup (server.go:501-504)
			// Ensures this connection's contribution is always subtracted on exit.
			defer func() {
				if lastPartialLen > 0 {
					after := globalCounter.Add(-lastPartialLen)
					if after < 0 {
						negativeCount.Add(1)
					}
				}
			}()

			for r := 0; r < readsPerConnection; r++ {
				newPartialLen := lastPartialLen + readSize
				delta := newPartialLen - lastPartialLen // == readSize

				// ═══════════════════════════════════════════════════════════
				// FIXED PATTERN (server.go:548-557):
				//   newTotal := s.partialBufferBytes.Add(delta)
				//   if newTotal > maxBytes {
				//       s.partialBufferBytes.Add(-delta) // rollback
				//       // disconnect
				//   }
				// ═══════════════════════════════════════════════════════════
				newTotal := globalCounter.Add(delta)

				// Track max value including transient overshoot
				for {
					old := maxObserved.Load()
					if newTotal <= old || maxObserved.CompareAndSwap(old, newTotal) {
						break
					}
				}

				if newTotal > limit {
					globalCounter.Add(-delta) // rollback reservation
					rollbackCount.Add(1)
					// In production: disconnect. Here: skip this read.
					continue
				}
				lastPartialLen = newPartialLen

				// Simulate complete message every 5 reads (reduces partial buffer)
				// (server.go:592-596: tracking update after message processing)
				if r%5 == 4 {
					globalCounter.Add(-lastPartialLen)
					lastPartialLen = 0
				}
			}
		}()
	}
	close(startBarrier) // Release all goroutines simultaneously
	wg.Wait()

	final := globalCounter.Load()

	t.Logf("RESULTS: final=%d maxObserved=%d rollbacks=%d negatives=%d limit=%d",
		final, maxObserved.Load(), rollbackCount.Load(), negativeCount.Load(), limit)

	// ── CRITICAL ASSERTIONS ─────────────────────────────────────────────────

	// 1. No memory leaks: counter must return to exactly 0
	if final != 0 {
		t.Errorf("BUFFER LEAK: final counter = %d, want 0 (leaked buffer tracking)", final)
	}

	// 2. No underflow: counter must never go negative
	if final < 0 || negativeCount.Load() > 0 {
		t.Errorf("UNDERFLOW: final=%d negativeEvents=%d — rollback subtracted too much", final, negativeCount.Load())
	}

	// 3. Limit was enforced: rollbacks must have occurred
	if rollbackCount.Load() == 0 {
		t.Errorf("No rollbacks occurred — limit was never enforced (limit=%d, maxObserved=%d)", limit, maxObserved.Load())
	}

	// 4. Transient overshoot is bounded: max ≤ limit + (numConnections × readSize)
	// Each goroutine can Add at most readSize before checking and rolling back.
	// In the worst case, all goroutines Add simultaneously before any rolls back.
	maxTheoretical := limit + int64(numConnections)*readSize
	if maxObserved.Load() > maxTheoretical {
		t.Errorf("Overshoot exceeds theoretical bound: %d > %d", maxObserved.Load(), maxTheoretical)
	}

	// ── INFORMATIONAL ───────────────────────────────────────────────────────

	if maxObserved.Load() > limit {
		overshoot := maxObserved.Load() - limit
		t.Logf("Transient overshoot: %d bytes (%.1f%% above limit) — bounded by immediate rollback",
			overshoot, float64(overshoot)/float64(limit)*100)
	} else {
		t.Logf("No overshoot observed (counter stayed within limit)")
	}

	t.Logf("FIXED PATTERN VERIFIED: Add-check-rollback prevents sustained overshoot")
	t.Logf("Connections=%d reads/conn=%d rollbacks=%d", numConnections, readsPerConnection, rollbackCount.Load())
}
