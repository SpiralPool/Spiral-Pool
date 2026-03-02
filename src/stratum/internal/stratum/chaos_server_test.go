// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for the global partial buffer TOCTOU race in messageLoop.
//
// TEST 6: Global Partial Buffer TOCTOU Race
// The messageLoop checks partial buffer limits with Load(), then adds with Add().
// Between Load and Add, other goroutines can also pass the check, causing the
// global counter to exceed the configured limit.
package stratum

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestChaos_Server_PartialBufferTOCTOURace simulates the TOCTOU race in
// messageLoop's global partial buffer memory check (server.go:543-560).
//
// Pattern:
//
//	globalTotal := s.partialBufferBytes.Load() - lastPartialLen + newPartialLen
//	if maxBytes > 0 && globalTotal > maxBytes { disconnect }
//	s.partialBufferBytes.Add(delta)
//
// Race: between Load() and Add(), other goroutines can also pass the check,
// causing the counter to exceed the intended limit.
//
// TARGET: server.go:543-560 (global partial buffer check in messageLoop)
// INVARIANT: Global memory limit should not be significantly exceeded.
// RUN WITH: go test -race -run TestChaos_Server_PartialBufferTOCTOURace
func TestChaos_Server_PartialBufferTOCTOURace(t *testing.T) {
	// Simulate the TOCTOU pattern without needing a full Server instance.
	// This directly tests the atomic Load/check/Add pattern from messageLoop.
	var globalCounter atomic.Int64
	limit := int64(1000)

	var violations atomic.Int64
	var maxObserved atomic.Int64

	const numGoroutines = 100
	const opsPerGoroutine = 10000

	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var localTracking int64

			for j := 0; j < opsPerGoroutine; j++ {
				increment := int64(50) // Simulates partial message bytes

				// Replicate the TOCTOU pattern from server.go:543-546
				newLocalLen := localTracking + increment
				globalTotal := globalCounter.Load() - localTracking + newLocalLen

				if globalTotal > limit {
					// Production: would disconnect this connection
					continue
				}

				// TOCTOU GAP: between Load and Add, others may have added
				globalCounter.Add(increment)
				localTracking = newLocalLen

				// Check if the actual total now exceeds the limit
				actual := globalCounter.Load()
				if actual > limit {
					violations.Add(1)
					// Track maximum observed value
					for {
						old := maxObserved.Load()
						if actual <= old || maxObserved.CompareAndSwap(old, actual) {
							break
						}
					}
				}

				// Simulate message processing: release the bytes
				globalCounter.Add(-increment)
				localTracking -= increment
			}
		}()
	}
	wg.Wait()

	v := violations.Load()
	maxOver := maxObserved.Load()

	t.Logf("RESULTS: %d TOCTOU violations where counter exceeded limit (%d)", v, limit)
	if maxOver > 0 {
		t.Logf("MAX OBSERVED: %d (limit=%d, overshoot=%d bytes, %.1fx)",
			maxOver, limit, maxOver-limit, float64(maxOver)/float64(limit))
	}

	if v > 0 {
		t.Logf("TOCTOU CONFIRMED: %d race windows allowed limit to be exceeded", v)
		t.Logf("Production impact: partialBufferBytes can temporarily exceed maxPartialBufferMB")
		t.Logf("This is a bounded overshoot - each connection adds at most 16KB per message")
	} else {
		t.Logf("No violations in this run. Race is timing-dependent.")
		t.Logf("Run with -count=100 for higher detection probability.")
	}
}
