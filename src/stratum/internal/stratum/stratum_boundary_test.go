// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Stratum Boundary Audit — Server layer tests
// Vectors: S1-S2 (Extranonce uniqueness), S8 (Job invalidation/history), S16 (Connection tracking)
package stratum

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// ---------------------------------------------------------------------------
// S1 — Extranonce1 Counter Wrap-Around
// Tests the atomic.Uint32 wrap behavior that server.go:416 detects.
// We cannot instantiate a full Server (requires config, logger, etc.)
// so we test the raw atomic counter that extranonce1Gen uses.
// ---------------------------------------------------------------------------

func TestS1_Extranonce1_WrapDetection(t *testing.T) {
	t.Parallel()
	var gen atomic.Uint32
	gen.Store(0xFFFFFFFE) // Two before wrap

	v1 := gen.Add(1) // 0xFFFFFFFF
	if v1 != 0xFFFFFFFF {
		t.Errorf("expected 0xFFFFFFFF, got %08x", v1)
	}

	v2 := gen.Add(1) // 0x00000000 (wrapped)
	if v2 != 0 {
		t.Errorf("expected 0 (wrap), got %08x", v2)
	}

	// Verify extranonce1 format string still works after wrap
	en1 := fmt.Sprintf("%08x", v2)
	if en1 != "00000000" {
		t.Errorf("extranonce1 format failed: %s", en1)
	}
}

// ---------------------------------------------------------------------------
// S2 — Extranonce1 Concurrent Uniqueness
// 1000 goroutines each call Add(1) on the same counter.  Every value must
// be unique — any collision means two miners would share an extranonce1.
// ---------------------------------------------------------------------------

func TestS2_Extranonce1_ConcurrentUniqueness(t *testing.T) {
	t.Parallel()
	var gen atomic.Uint32
	const numConnections = 1000

	seen := &sync.Map{}
	var collisions atomic.Int64

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			val := gen.Add(1)
			if _, loaded := seen.LoadOrStore(val, true); loaded {
				collisions.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if collisions.Load() != 0 {
		t.Errorf("COLLISION: %d extranonce1 collisions detected", collisions.Load())
	}
}

// ---------------------------------------------------------------------------
// S8 — Job Invalidation on CleanJobs
// Simulates BroadcastJob with CleanJobs=true (server.go:799-804).
// All pre-existing jobs must transition to Invalidated and be removed from
// the jobs map; only the new job remains Active.
// ---------------------------------------------------------------------------

func TestS8_JobInvalidation_CleanJobs(t *testing.T) {
	t.Parallel()

	// Simulate server.jobs map
	jobs := make(map[string]*protocol.Job)

	// Add 5 active jobs
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("%08x", i+1)
		j := &protocol.Job{
			ID:        id,
			CreatedAt: time.Now(),
		}
		j.SetState(protocol.JobStateActive, "")
		jobs[id] = j
	}

	// Verify all 5 are active
	for _, j := range jobs {
		if j.GetState() != protocol.JobStateActive {
			t.Fatalf("expected Active, got %v", j.GetState())
		}
	}

	// Simulate CleanJobs broadcast (server.go:799-804)
	oldJobs := make([]*protocol.Job, 0, len(jobs))
	for id, oldJob := range jobs {
		oldJob.SetState(protocol.JobStateInvalidated, "new block - cleanJobs broadcast")
		oldJobs = append(oldJobs, oldJob)
		delete(jobs, id)
	}

	// Add the new job
	newJob := &protocol.Job{
		ID:        "00000006",
		CleanJobs: true,
		CreatedAt: time.Now(),
	}
	newJob.SetState(protocol.JobStateActive, "")
	jobs[newJob.ID] = newJob

	// Verify all old jobs are invalidated
	for _, j := range oldJobs {
		if j.GetState() != protocol.JobStateInvalidated {
			t.Errorf("old job %s should be Invalidated, got %v", j.ID, j.GetState())
		}
	}

	// Verify new job is active
	if newJob.GetState() != protocol.JobStateActive {
		t.Errorf("new job should be Active, got %v", newJob.GetState())
	}

	// Verify map only has new job
	if len(jobs) != 1 {
		t.Errorf("expected 1 job in map, got %d", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// S8 — Job History Limit (max 10 jobs)
// Simulates the server.go:808-819 eviction logic: after each insert, if the
// map exceeds 10 entries the oldest job is removed.  Adding 15 jobs must
// never let the map exceed 10 entries.
// ---------------------------------------------------------------------------

func TestS8_JobHistoryLimit(t *testing.T) {
	t.Parallel()

	const maxJobs = 10
	jobs := make(map[string]*protocol.Job)

	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("%08x", i+1)
		j := &protocol.Job{
			ID:        id,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		j.SetState(protocol.JobStateActive, "")
		jobs[id] = j

		// Eviction logic (mirrors server.go:808-819)
		if len(jobs) > maxJobs {
			var oldest string
			var oldestTime time.Time
			for jid, jj := range jobs {
				if oldest == "" || jj.CreatedAt.Before(oldestTime) {
					oldest = jid
					oldestTime = jj.CreatedAt
				}
			}
			delete(jobs, oldest)
		}

		if len(jobs) > maxJobs {
			t.Errorf("after inserting job %d: map size %d exceeds max %d", i+1, len(jobs), maxJobs)
		}
	}

	// Final size must be exactly maxJobs
	if len(jobs) != maxJobs {
		t.Errorf("expected %d jobs at end, got %d", maxJobs, len(jobs))
	}
}

// ---------------------------------------------------------------------------
// S16 — Connection Count Tracking (atomic.Int64)
// 100 goroutines each increment connCount, then decrement.
// Final value must be 0 (no leaks) and must never go negative.
// ---------------------------------------------------------------------------

func TestS16_ConnectionCount_Atomic(t *testing.T) {
	t.Parallel()

	var connCount atomic.Int64
	const numConns = 100

	// Track minimum observed value to detect negative dips
	var minObserved atomic.Int64

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			// Simulate handleConnection: Add(1) on entry, Add(-1) on exit
			connCount.Add(1)

			// Simulate some work
			time.Sleep(time.Microsecond)

			newVal := connCount.Add(-1)

			// Atomic compare-and-swap loop to track minimum
			for {
				cur := minObserved.Load()
				if newVal >= cur {
					break
				}
				if minObserved.CompareAndSwap(cur, newVal) {
					break
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	finalCount := connCount.Load()
	if finalCount != 0 {
		t.Errorf("connection count leak: expected 0, got %d", finalCount)
	}

	if minObserved.Load() < 0 {
		t.Errorf("connection count went negative: min observed = %d", minObserved.Load())
	}
}

// ---------------------------------------------------------------------------
// S8 — Concurrent Job Invalidation
// A single Active job is hammered by 50 writers (SetState → Invalidated)
// and 50 readers (GetState) simultaneously.  Must not panic or race.
// The job must end in Invalidated state.
// ---------------------------------------------------------------------------

func TestS8_ConcurrentJobInvalidation(t *testing.T) {
	t.Parallel()

	job := &protocol.Job{
		ID:        "deadbeef",
		CreatedAt: time.Now(),
	}
	job.SetState(protocol.JobStateActive, "")

	const writers = 50
	const readers = 50

	start := make(chan struct{})
	var wg sync.WaitGroup

	// Writers — transition to Invalidated
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			job.SetState(protocol.JobStateInvalidated, fmt.Sprintf("writer-%d", n))
		}(i)
	}

	// Readers — observe current state
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			state := job.GetState()
			// State must be either Active (not yet invalidated) or Invalidated
			if state != protocol.JobStateActive && state != protocol.JobStateInvalidated {
				t.Errorf("unexpected state during concurrent access: %v", state)
			}
		}()
	}

	close(start)
	wg.Wait()

	// After all writers finish, the job must be Invalidated
	if job.GetState() != protocol.JobStateInvalidated {
		t.Errorf("expected Invalidated after concurrent writes, got %v", job.GetState())
	}
}
