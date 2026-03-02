// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Stratum Boundary Audit — Share validation layer tests
// Vectors: S3-S6 (Duplicate replay, Job invalidation), S17 (Concurrent dedup)
package shares

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// S3 — Duplicate Share Detection (RecordIfNew)
// =============================================================================

// TestS3_DuplicateShare_FirstIsNew verifies that RecordIfNew returns true
// on the very first call for a given share, meaning it is recorded as new.
func TestS3_DuplicateShare_FirstIsNew(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()
	isNew := dt.RecordIfNew("job01", "en1_aa", "en2_bb", "64000000", "deadbeef")
	if !isNew {
		t.Fatal("RecordIfNew returned false on first call — expected true (new share)")
	}
}

// TestS3_DuplicateShare_SecondIsDuplicate verifies that RecordIfNew returns
// false on the second identical call, correctly detecting the duplicate.
func TestS3_DuplicateShare_SecondIsDuplicate(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	first := dt.RecordIfNew("job01", "en1_aa", "en2_bb", "64000000", "deadbeef")
	if !first {
		t.Fatal("first RecordIfNew returned false — expected true")
	}

	second := dt.RecordIfNew("job01", "en1_aa", "en2_bb", "64000000", "deadbeef")
	if second {
		t.Fatal("second RecordIfNew returned true — expected false (duplicate)")
	}
}

// TestS3_DuplicateShare_DifferentNonce verifies that two shares with the same
// jobID, extranonce1, extranonce2, and ntime but DIFFERENT nonces are both
// treated as new (not duplicates). The nonce is part of the dedup key.
func TestS3_DuplicateShare_DifferentNonce(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	first := dt.RecordIfNew("job01", "en1_aa", "en2_bb", "64000000", "deadbeef")
	if !first {
		t.Fatal("first share (nonce=deadbeef) should be new")
	}

	second := dt.RecordIfNew("job01", "en1_aa", "en2_bb", "64000000", "cafebabe")
	if !second {
		t.Fatal("second share (nonce=cafebabe) should also be new — different nonce")
	}
}

// TestS3_DuplicateShare_DifferentJob verifies that two shares with identical
// share parameters but different jobIDs are both treated as new. The jobID
// scopes duplicate detection.
func TestS3_DuplicateShare_DifferentJob(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	first := dt.RecordIfNew("job01", "en1_aa", "en2_bb", "64000000", "deadbeef")
	if !first {
		t.Fatal("share for job01 should be new")
	}

	second := dt.RecordIfNew("job02", "en1_aa", "en2_bb", "64000000", "deadbeef")
	if !second {
		t.Fatal("share for job02 should also be new — different jobID")
	}
}

// =============================================================================
// S4 — Cross-Session Duplicate Isolation
// =============================================================================

// TestS4_CrossSession_DifferentExtranonce1 verifies that two shares with
// the same jobID, extranonce2, ntime, and nonce but DIFFERENT extranonce1
// values are both treated as new. ExtraNonce1 is assigned per-session by the
// pool, so shares from different sessions must not collide in dedup tracking.
func TestS4_CrossSession_DifferentExtranonce1(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	// Session A: extranonce1 = "aaa00001"
	first := dt.RecordIfNew("job01", "aaa00001", "en2_bb", "64000000", "deadbeef")
	if !first {
		t.Fatal("share from session A (en1=aaa00001) should be new")
	}

	// Session B: extranonce1 = "bbb00002", everything else identical
	second := dt.RecordIfNew("job01", "bbb00002", "en2_bb", "64000000", "deadbeef")
	if !second {
		t.Fatal("share from session B (en1=bbb00002) should also be new — different extranonce1")
	}

	// Verify session A's original share is still tracked as duplicate
	replay := dt.RecordIfNew("job01", "aaa00001", "en2_bb", "64000000", "deadbeef")
	if replay {
		t.Fatal("replayed share from session A should be detected as duplicate")
	}
}

// =============================================================================
// S5 — Post-Eviction Replay
// =============================================================================

// TestS5_DuplicateTracker_LRUEviction fills the tracker beyond its capacity
// (maxTrackedJobs = 1000) and verifies that:
//  1. The earliest job's share can be re-added after eviction (it was forgotten).
//  2. The most recent job's share is still tracked as a duplicate.
func TestS5_DuplicateTracker_LRUEviction(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	// Fill tracker with 1001 unique jobs (exceeds maxTrackedJobs=1000 by 1).
	// Each job gets one share recorded.
	for i := 0; i < 1001; i++ {
		jobID := fmt.Sprintf("evict-job-%04d", i)
		isNew := dt.RecordIfNew(jobID, "en1", "en2", "64000000", "00000000")
		if !isNew {
			t.Fatalf("job %d initial share should be new", i)
		}
	}

	// The first job (evict-job-0000) should have been evicted when job 1001
	// was added (capacity was 1000, so the oldest was evicted).
	// Re-adding the same share should return true (new) because it was forgotten.
	reAdd := dt.RecordIfNew("evict-job-0000", "en1", "en2", "64000000", "00000000")
	if !reAdd {
		t.Fatal("share for evicted job (evict-job-0000) should be treated as new after LRU eviction")
	}

	// The last job (evict-job-1000) should still be tracked.
	// The same share should return false (duplicate).
	lastDup := dt.RecordIfNew("evict-job-1000", "en1", "en2", "64000000", "00000000")
	if lastDup {
		t.Fatal("share for most recent job (evict-job-1000) should still be tracked as duplicate")
	}
}

// =============================================================================
// S17 — Concurrent Duplicate Submission
// =============================================================================

// TestS17_DuplicateTracker_ConcurrentRecordIfNew launches 200 goroutines that
// all call RecordIfNew with the SAME share parameters simultaneously. Exactly
// one goroutine should win (return true), and all others should get false.
// This validates the atomic check-and-record guarantee under contention.
func TestS17_DuplicateTracker_ConcurrentRecordIfNew(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	const goroutines = 200

	var (
		wg       sync.WaitGroup
		barrier  = make(chan struct{}) // Start barrier for simultaneous launch
		newCount int64
		dupCount int64
		mu       sync.Mutex // Protects newCount/dupCount (not strictly needed with atomics, but clear)
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// Wait at the barrier until all goroutines are ready
			<-barrier
			isNew := dt.RecordIfNew("race-job", "en1_race", "en2_race", "64000000", "ffffffff")
			mu.Lock()
			if isNew {
				newCount++
			} else {
				dupCount++
			}
			mu.Unlock()
		}()
	}

	// Release all goroutines simultaneously
	close(barrier)
	wg.Wait()

	// Exactly ONE goroutine must win the race
	if newCount != 1 {
		t.Fatalf("expected exactly 1 new share, got %d (dupCount=%d) — atomic dedup broken", newCount, dupCount)
	}
	if dupCount != int64(goroutines-1) {
		t.Fatalf("expected %d duplicates, got %d — goroutine accounting error", goroutines-1, dupCount)
	}
}

// =============================================================================
// S6 — Validator Rejects Invalidated Job
// =============================================================================

// TestS6_Validator_RejectsInvalidatedJob creates a Validator with a getJob
// function that returns a job in JobStateInvalidated. The Validator must
// reject the share as stale before attempting header construction.
func TestS6_Validator_RejectsInvalidatedJob(t *testing.T) {
	t.Parallel()

	job := &protocol.Job{
		ID:        "00000001",
		CreatedAt: time.Now(),
	}
	// Transition through valid states to reach Invalidated
	job.SetState(protocol.JobStateActive, "")
	job.SetState(protocol.JobStateInvalidated, "new block")

	// Sanity: confirm the job is actually invalidated
	if job.GetState() != protocol.JobStateInvalidated {
		t.Fatalf("job state = %v, want JobStateInvalidated", job.GetState())
	}

	v := NewValidator(func(jobID string) (*protocol.Job, bool) {
		if jobID == "00000001" {
			return job, true
		}
		return nil, false
	})

	share := &protocol.Share{
		JobID:       "00000001",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	result := v.Validate(share)
	if result.Accepted {
		t.Fatal("Validator accepted a share for an invalidated job — must reject")
	}
	if result.RejectReason != protocol.RejectReasonStale {
		t.Fatalf("RejectReason = %q, want %q", result.RejectReason, protocol.RejectReasonStale)
	}
}

// =============================================================================
// S6 — Validator Rejects Stale Job (maxJobAge)
// =============================================================================

// TestS6_Validator_RejectsStaleJob creates a job with CreatedAt 20 minutes ago
// and sets maxJobAge to 1 minute. The Validator must reject the share as stale
// based on the time-based staleness check (isJobStale).
func TestS6_Validator_RejectsStaleJob(t *testing.T) {
	t.Parallel()

	job := &protocol.Job{
		ID:        "00000001",
		CreatedAt: time.Now().Add(-20 * time.Minute), // 20 minutes old
	}

	v := NewValidator(func(jobID string) (*protocol.Job, bool) {
		if jobID == "00000001" {
			return job, true
		}
		return nil, false
	})
	v.SetMaxJobAge(1 * time.Minute) // Jobs older than 1 minute are stale

	share := &protocol.Share{
		JobID:       "00000001",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	result := v.Validate(share)
	if result.Accepted {
		t.Fatal("Validator accepted a share for a stale job (20 min old, maxJobAge=1 min) — must reject")
	}
	if result.RejectReason != protocol.RejectReasonStale {
		t.Fatalf("RejectReason = %q, want %q", result.RejectReason, protocol.RejectReasonStale)
	}
}

// =============================================================================
// S3 — Validator Silent Duplicate Handling
// =============================================================================

// TestS3_Validator_SilentDuplicate verifies the silent duplicate path in the
// Validator. When a duplicate share is submitted, the Validator returns
// Accepted=true (to appease cgminer) but with SilentDuplicate=true and
// RejectReason="duplicate" so the share is not double-credited.
//
// This test uses a job that triggers the invalidated-state rejection on the
// first call so we can isolate the duplicate tracker behavior. Instead, we
// test the DuplicateTracker directly in combination with the Validator's
// duplicate path by submitting a stale job twice — the second submission
// should hit the duplicate check before the stale check because duplicates
// are checked at step 2 in Validate (after job lookup and state/staleness).
//
// Actually, looking at the Validate code flow:
//   1. Job lookup
//   1.25. Job state check (invalidated)
//   1.5. Job staleness check (maxJobAge)
//   2. Duplicate check (RecordIfNew)
//
// The duplicate check happens AFTER state/staleness, so for a valid job that
// passes state and staleness checks, the second identical share hits the
// duplicate tracker and gets the silent duplicate treatment.
//
// We use a fresh (non-stale, non-invalidated) job so both submissions reach
// the duplicate check. The first may fail at header construction (step 4),
// but RecordIfNew is called at step 2 regardless. The second submission
// will be caught as duplicate at step 2 and return the silent duplicate result.
func TestS3_Validator_SilentDuplicate(t *testing.T) {
	t.Parallel()

	job := &protocol.Job{
		ID:        "00000001",
		CreatedAt: time.Now(),
	}

	v := NewValidator(func(jobID string) (*protocol.Job, bool) {
		if jobID == "00000001" {
			return job, true
		}
		return nil, false
	})

	share := &protocol.Share{
		JobID:       "00000001",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	// First submission: passes job lookup + state + staleness + duplicate check (new).
	// It will likely fail at header construction (invalid PrevBlockHash, etc.),
	// but the share IS recorded in the duplicate tracker at step 2.
	_ = v.Validate(share)

	// Second submission: same share. Should be caught at step 2 (duplicate check)
	// and return the silent duplicate result.
	result := v.Validate(share)

	if !result.Accepted {
		t.Fatal("Validator rejected a duplicate share — expected Accepted=true (silent duplicate)")
	}
	if !result.SilentDuplicate {
		t.Fatal("Validator did not flag the duplicate share with SilentDuplicate=true")
	}
	if result.RejectReason != protocol.RejectReasonDuplicate {
		t.Fatalf("RejectReason = %q, want %q", result.RejectReason, protocol.RejectReasonDuplicate)
	}
}
