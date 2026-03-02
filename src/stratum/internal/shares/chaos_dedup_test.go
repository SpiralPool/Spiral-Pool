// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for DuplicateTracker under extreme concurrent load.
//
// TEST 4: Redis Dedup Throughput Degradation Under Sustained Latency
// When Redis is slow/unavailable, all dedup falls back to the local DuplicateTracker.
// This test verifies the local tracker handles massive concurrent load correctly.
package shares

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestChaos_DedupTracker_ConcurrentFloodStress floods the DuplicateTracker with
// concurrent RecordIfNew calls from many goroutines, mixing unique and duplicate shares.
// Verifies: no panics, no deadlocks, correct duplicate detection, and memory bounds
// (maxTrackedJobs enforced under load).
//
// TARGET: validator.go:921-1015 (DuplicateTracker), redis_dedup.go:165-209 (fallback path)
// INVARIANT: No operations lost, no deadlocks, maxTrackedJobs enforced.
func TestChaos_DedupTracker_ConcurrentFloodStress(t *testing.T) {
	tracker := NewDuplicateTracker()

	const numGoroutines = 100
	const opsPerGoroutine = 10000

	var newCount atomic.Uint64
	var dupCount atomic.Uint64

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gIdx int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				// Force cross-goroutine duplicates: use en1 modulo 10
				// so goroutines 0,10,20,... share en1="00000000" and collide
				jobID := fmt.Sprintf("job-%d", i%100)
				en1 := fmt.Sprintf("%08x", gIdx%10)
				en2 := fmt.Sprintf("%08x", i%5000)
				ntime := "65a8b1c0"
				nonce := fmt.Sprintf("%08x", i%5000)

				if tracker.RecordIfNew(jobID, en1, en2, ntime, nonce) {
					newCount.Add(1)
				} else {
					dupCount.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	jobs, trackedShares := tracker.Stats()
	totalOps := newCount.Load() + dupCount.Load()

	t.Logf("RESULTS: totalOps=%d new=%d dup=%d", totalOps, newCount.Load(), dupCount.Load())
	t.Logf("TRACKER STATE: jobs=%d shares=%d", jobs, trackedShares)

	// Verify all operations completed (no panics, no deadlocks)
	expectedOps := uint64(numGoroutines * opsPerGoroutine)
	if totalOps != expectedOps {
		t.Errorf("Operations lost! got %d, want %d (panic or deadlock occurred)", totalOps, expectedOps)
	}

	// Verify maxTrackedJobs limit is enforced even under extreme load
	if jobs > maxTrackedJobs {
		t.Errorf("Job count %d exceeds maxTrackedJobs %d (cleanup broken under concurrent load)",
			jobs, maxTrackedJobs)
	}

	// Verify duplicates were actually detected (sanity check)
	if dupCount.Load() == 0 {
		t.Errorf("No duplicates detected (expected cross-goroutine collisions with shared nonce patterns)")
	}

	t.Logf("Duplicate detection rate: %.2f%% (%d/%d)",
		float64(dupCount.Load())/float64(totalOps)*100, dupCount.Load(), totalOps)
}
