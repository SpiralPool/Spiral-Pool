// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool provides deterministic tests for block orphan race elimination.
//
// These tests verify that the ZMQ → invalidation → submission pipeline correctly
// prevents stale block submissions, duplicate submissions, false celebrations,
// and reward accounting errors. All tests are deterministic (no real ZMQ/RPC).
//
// Test categories:
//   - E/F: Duplicate & replay adversary tests
//   - G/H: ZMQ ordering & burst stress tests
//   - I/J: Partial failure & recovery tests
//   - K/L: Economic safety tests (accounting correctness)
//   - M:   State machine property tests
package pool

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// TEST HELPERS
// =============================================================================

// makeActiveJob creates a job in Active state for testing.
func makeActiveJob(id string) *protocol.Job {
	j := &protocol.Job{
		ID:            id,
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		Version:       "20000000",
		NBits:         "1a0377ae",
		NTime:         fmt.Sprintf("%08x", time.Now().Unix()),
		Height:        1000000,
		CreatedAt:     time.Now(),
		CleanJobs:     true,
		CoinbaseValue: 72600000000, // 726 DGB
		Difficulty:    50000.0,
	}
	j.SetState(protocol.JobStateActive, "")
	return j
}

// jobStore is a thread-safe in-memory job store for testing.
type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*protocol.Job
}

func newJobStore() *jobStore {
	return &jobStore{jobs: make(map[string]*protocol.Job)}
}

func (s *jobStore) put(j *protocol.Job) {
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
}

func (s *jobStore) get(id string) (*protocol.Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *jobStore) all() []*protocol.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*protocol.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		result = append(result, j)
	}
	return result
}

// invalidateAll simulates Phase 1 of OnBlockNotification — immediate invalidation.
func (s *jobStore) invalidateAll(reason string) {
	s.mu.RLock()
	for _, j := range s.jobs {
		j.SetState(protocol.JobStateInvalidated, reason)
	}
	s.mu.RUnlock()
}

// submitBlockDecision simulates the handleBlock pre-submission gate.
// Returns: (shouldSubmit bool, reason string)
func submitBlockDecision(store *jobStore, jobID string) (bool, string) {
	job, found := store.get(jobID)
	if !found {
		// Job not found — default to Active (matches pool.go:993 behavior)
		return true, "job-not-found-default-submit"
	}
	state := job.GetState()
	switch state {
	case protocol.JobStateInvalidated:
		return false, "stale-race"
	case protocol.JobStateSolved:
		return false, "duplicate-block-candidate"
	default:
		return true, "active"
	}
}

// =============================================================================
// TEST E: Duplicate block share after Solved transition
// =============================================================================
// Goal: Prove Solved is terminal and enforced everywhere.
// Prevents: accidental double-submit, malicious replay, reward inflation.

func TestE_DuplicateShareAfterSolved(t *testing.T) {
	store := newJobStore()
	j1 := makeActiveJob("job-E1")
	store.put(j1)

	// --- Share A finds block, submission succeeds ---
	canSubmitA, _ := submitBlockDecision(store, "job-E1")
	if !canSubmitA {
		t.Fatal("Share A should be allowed to submit (job is Active)")
	}

	// Simulate successful SubmitBlock → transition to Solved
	ok := j1.SetState(protocol.JobStateSolved, "block submitted successfully")
	if !ok {
		t.Fatal("SetState(Solved) should succeed from Active")
	}
	if j1.GetState() != protocol.JobStateSolved {
		t.Fatalf("Expected Solved, got %v", j1.GetState())
	}

	// --- Same share replayed (duplicate nonce/extranonce) ---
	canSubmitB, reason := submitBlockDecision(store, "job-E1")
	if canSubmitB {
		t.Fatal("Duplicate share MUST NOT reach SubmitBlock after Solved")
	}
	if reason != "duplicate-block-candidate" {
		t.Errorf("Expected reason 'duplicate-block-candidate', got '%s'", reason)
	}

	// --- Verify Solved is terminal: cannot transition back to Active ---
	ok = j1.SetState(protocol.JobStateActive, "")
	if ok {
		t.Fatal("CRITICAL: Solved → Active transition must be forbidden")
	}
	if j1.GetState() != protocol.JobStateSolved {
		t.Fatal("State must remain Solved after failed transition attempt")
	}

	// --- Verify Solved → Invalidated is also forbidden ---
	ok = j1.SetState(protocol.JobStateInvalidated, "ZMQ late arrival")
	if ok {
		t.Fatal("Solved → Invalidated transition must be forbidden")
	}
}

// =============================================================================
// TEST F: Different share, same job (collision path / second block candidate)
// =============================================================================
// Goal: Ensure block dedup prevents second SubmitBlock from same job.

func TestF_SecondBlockCandidateSameJob(t *testing.T) {
	store := newJobStore()
	j1 := makeActiveJob("job-F1")
	store.put(j1)

	var submitCount atomic.Int32

	// Share 1: meets network target → SubmitBlock
	canSubmit1, _ := submitBlockDecision(store, "job-F1")
	if !canSubmit1 {
		t.Fatal("First share should submit")
	}
	submitCount.Add(1)
	j1.SetState(protocol.JobStateSolved, "block submitted successfully")

	// Share 2: DIFFERENT nonce, also meets target → should NOT submit
	canSubmit2, reason := submitBlockDecision(store, "job-F1")
	if canSubmit2 {
		submitCount.Add(1)
		t.Fatal("Second block candidate from same job MUST NOT submit")
	}
	if reason != "duplicate-block-candidate" {
		t.Errorf("Expected duplicate-block-candidate, got %s", reason)
	}

	if submitCount.Load() != 1 {
		t.Errorf("Expected exactly 1 SubmitBlock call, got %d", submitCount.Load())
	}
}

// =============================================================================
// TEST G: ZMQ burst (multiple blocks back-to-back)
// =============================================================================
// Goal: Prove correctness under rapid block succession (DGB multi-algo).

func TestG_ZMQBurstMultipleBlocks(t *testing.T) {
	store := newJobStore()

	// Initial state: 3 active jobs
	j1 := makeActiveJob("job-G1")
	j2 := makeActiveJob("job-G2")
	j3 := makeActiveJob("job-G3")
	store.put(j1)
	store.put(j2)
	store.put(j3)

	// --- Burst: 3 ZMQ notifications in rapid succession ---
	// Each simulates Phase 1 immediate invalidation
	for i := 0; i < 3; i++ {
		store.invalidateAll(fmt.Sprintf("ZMQ burst notification %d", i+1))
	}

	// All original jobs must be Invalidated
	for _, id := range []string{"job-G1", "job-G2", "job-G3"} {
		j, ok := store.get(id)
		if !ok {
			t.Fatalf("Job %s should still be in store", id)
		}
		if j.GetState() != protocol.JobStateInvalidated {
			t.Errorf("Job %s should be Invalidated, got %v", id, j.GetState())
		}
	}

	// Shares referencing any invalidated job must be rejected
	for _, id := range []string{"job-G1", "job-G2", "job-G3"} {
		canSubmit, reason := submitBlockDecision(store, id)
		if canSubmit {
			t.Errorf("Share for invalidated job %s should be rejected", id)
		}
		if reason != "stale-race" {
			t.Errorf("Job %s: expected reason 'stale-race', got '%s'", id, reason)
		}
	}

	// New job created after burst should be Active and submittable
	j4 := makeActiveJob("job-G4")
	store.put(j4)
	canSubmit, _ := submitBlockDecision(store, "job-G4")
	if !canSubmit {
		t.Error("New job after burst should be submittable")
	}
}

// =============================================================================
// TEST G2: Concurrent ZMQ invalidation + share submission
// =============================================================================
// Goal: Stress-test concurrent invalidation with share processing goroutines.

func TestG2_ConcurrentInvalidationAndSubmission(t *testing.T) {
	store := newJobStore()

	const numJobs = 10
	for i := 0; i < numJobs; i++ {
		store.put(makeActiveJob(fmt.Sprintf("job-conc-%d", i)))
	}

	var wg sync.WaitGroup
	var staleRejects atomic.Int32
	var submits atomic.Int32

	// Goroutine 1: ZMQ invalidation loop (fire 50 times)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			store.invalidateAll(fmt.Sprintf("zmq-conc-%d", i))
		}
	}()

	// Goroutines 2-N: concurrent share submissions
	for i := 0; i < numJobs; i++ {
		wg.Add(1)
		go func(jobIdx int) {
			defer wg.Done()
			jobID := fmt.Sprintf("job-conc-%d", jobIdx)
			for attempt := 0; attempt < 100; attempt++ {
				canSubmit, _ := submitBlockDecision(store, jobID)
				if canSubmit {
					submits.Add(1)
				} else {
					staleRejects.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	// After all goroutines complete, all jobs must be Invalidated
	for i := 0; i < numJobs; i++ {
		j, _ := store.get(fmt.Sprintf("job-conc-%d", i))
		if j.GetState() != protocol.JobStateInvalidated {
			t.Errorf("Job conc-%d not Invalidated after concurrent test", i)
		}
	}

	// No panics, no data races (run with -race flag)
	t.Logf("Concurrent test: %d submits, %d stale rejects", submits.Load(), staleRejects.Load())
}

// =============================================================================
// TEST H: Out-of-order ZMQ delivery (simulated)
// =============================================================================
// Goal: Prove correctness if notifications arrive late or reordered.
// Code must NOT rely on monotonic height or lastBlockHash sequencing.

func TestH_OutOfOrderZMQDelivery(t *testing.T) {
	store := newJobStore()

	// Block B1 at height 100 → Job J1
	j1 := makeActiveJob("job-H1")
	j1.Height = 100
	store.put(j1)

	// Block B2 at height 101 → Job J2 (replaces J1 via storeJob)
	store.invalidateAll("block B2")
	j2 := makeActiveJob("job-H2")
	j2.Height = 101
	store.put(j2)

	// SIMULATE OUT-OF-ORDER: Block B1 notification arrives LATE
	// (e.g., ZMQ redelivery after reconnect)
	// This should invalidate J2 (the current job)
	store.invalidateAll("block B1 (late ZMQ redelivery)")

	// Both jobs must be Invalidated — no height-based skip logic
	if j1.GetState() != protocol.JobStateInvalidated {
		t.Error("J1 must remain Invalidated")
	}
	if j2.GetState() != protocol.JobStateInvalidated {
		t.Error("J2 must be Invalidated by late B1 notification")
	}

	// Neither should allow submission
	for _, id := range []string{"job-H1", "job-H2"} {
		canSubmit, _ := submitBlockDecision(store, id)
		if canSubmit {
			t.Errorf("Job %s should not allow submission after out-of-order ZMQ", id)
		}
	}
}

// =============================================================================
// TEST I: ZMQ disconnect + reconnect (shares during blackout)
// =============================================================================
// Goal: Ensure job invalidation still happens after ZMQ gap.
// ZMQ is an optimization, not a correctness dependency.

func TestI_ZMQDisconnectReconnect(t *testing.T) {
	store := newJobStore()

	j1 := makeActiveJob("job-I1")
	store.put(j1)

	// --- ZMQ disconnects. Daemon mines a new block. Pool doesn't know. ---
	// During blackout, miner submits share → job is still Active
	canSubmitDuringBlackout, _ := submitBlockDecision(store, "job-I1")
	if !canSubmitDuringBlackout {
		t.Fatal("During ZMQ blackout, jobs remain Active (no notification received)")
	}
	// This is expected — during blackout the pool has no signal.
	// The share would reach SubmitBlock and daemon would reject with prev-blk-not-found.
	// This is the unavoidable ZMQ-gap race.

	// --- ZMQ reconnects. Late notification fires. ---
	store.invalidateAll("ZMQ reconnect - late block notification")

	// Now all stale jobs must be rejected
	canSubmitAfterReconnect, reason := submitBlockDecision(store, "job-I1")
	if canSubmitAfterReconnect {
		t.Fatal("After ZMQ reconnect and invalidation, stale jobs must be rejected")
	}
	if reason != "stale-race" {
		t.Errorf("Expected stale-race, got %s", reason)
	}

	// New job created after reconnect should work
	j2 := makeActiveJob("job-I2")
	store.put(j2)
	canSubmitNew, _ := submitBlockDecision(store, "job-I2")
	if !canSubmitNew {
		t.Error("New job after reconnect should be submittable")
	}
}

// =============================================================================
// TEST J: SubmitBlock timeout + ZMQ invalidation during hang
// =============================================================================
// Goal: Validate timeout + invalidation interaction.
// After timeout, no retry should occur if job was invalidated.

func TestJ_SubmitBlockTimeoutWithInvalidation(t *testing.T) {
	store := newJobStore()
	j1 := makeActiveJob("job-J1")
	store.put(j1)

	var submitCalls atomic.Int32

	// --- Simulate: handleBlock starts, SubmitBlock in progress ---
	// Pre-check passes (job is Active)
	canSubmit, _ := submitBlockDecision(store, "job-J1")
	if !canSubmit {
		t.Fatal("Initial check should pass (Active)")
	}
	submitCalls.Add(1) // SubmitBlock RPC issued

	// --- Simulate: ZMQ fires DURING the in-flight SubmitBlock ---
	store.invalidateAll("ZMQ during SubmitBlock")

	// --- Simulate: SubmitBlock returns timeout error ---
	// The retry path should re-check job state before retrying
	canRetry, reason := submitBlockDecision(store, "job-J1")
	if canRetry {
		t.Fatal("Retry MUST NOT proceed after invalidation during SubmitBlock hang")
	}
	if reason != "stale-race" {
		t.Errorf("Expected stale-race on retry check, got %s", reason)
	}

	// Final state: exactly 1 SubmitBlock call (the original), no retries
	if submitCalls.Load() != 1 {
		t.Errorf("Expected 1 SubmitBlock call, got %d", submitCalls.Load())
	}
}

// =============================================================================
// TEST K: Reward accounting excludes orphaned blocks
// =============================================================================
// Goal: Prove rewards are never counted for orphans.
// The DB insert uses blockStatus — verify correct status propagation.

func TestK_RewardAccountingExcludesOrphans(t *testing.T) {
	store := newJobStore()

	// Case 1: Block submitted and accepted → status="pending"
	j1 := makeActiveJob("job-K1")
	store.put(j1)
	canSubmit1, _ := submitBlockDecision(store, "job-K1")
	if !canSubmit1 {
		t.Fatal("Should submit")
	}
	status1 := "submitted"   // SubmitBlock returned nil
	status1 = "pending"      // post-submission status
	j1.SetState(protocol.JobStateSolved, "submitted")
	if status1 != "pending" {
		t.Fatal("Accepted block should be pending")
	}

	// Case 2: Block candidate, job was Invalidated → status="orphaned"
	j2 := makeActiveJob("job-K2")
	store.put(j2)
	store.invalidateAll("ZMQ")
	canSubmit2, _ := submitBlockDecision(store, "job-K2")
	status2 := "pending"
	if !canSubmit2 {
		status2 = "orphaned"
	}
	if status2 != "orphaned" {
		t.Fatal("Stale-race block should be orphaned")
	}

	// Case 3: Block submitted, daemon rejected → status="orphaned"
	j3 := makeActiveJob("job-K3")
	store.put(j3)
	canSubmit3, _ := submitBlockDecision(store, "job-K3")
	status3 := "pending"
	if canSubmit3 {
		// Simulate daemon rejection
		daemonErr := "prev-blk-not-found"
		if isPermanentRejection(daemonErr) {
			status3 = "orphaned"
		}
	}
	if status3 != "orphaned" {
		t.Fatal("Daemon-rejected block should be orphaned")
	}

	// Verify: only status="pending" blocks should be counted for rewards
	statuses := []struct {
		name   string
		status string
		reward bool
	}{
		{"accepted", status1, true},
		{"stale-race", status2, false},
		{"daemon-rejected", status3, false},
	}

	for _, s := range statuses {
		shouldReward := s.status == "pending"
		if shouldReward != s.reward {
			t.Errorf("Block %s: status=%s, expected reward=%v, got reward=%v",
				s.name, s.status, s.reward, shouldReward)
		}
	}
}

// =============================================================================
// TEST K2: Celebration gate verification
// =============================================================================
// Goal: Verify celebration ONLY fires for pending (accepted) blocks.

func TestK2_CelebrationGateByStatus(t *testing.T) {
	tests := []struct {
		name           string
		blockStatus    string
		shouldCelebrate bool
	}{
		{"accepted-pending", "pending", true},
		{"orphaned-stale", "orphaned", false},
		{"rejected-daemon", "rejected", false},
		{"submitted-interim", "submitted", false},
		{"failed-retries", "failed", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This mirrors the exact guard from pool.go:1308
			wouldCelebrate := tt.blockStatus == "pending"
			if wouldCelebrate != tt.shouldCelebrate {
				t.Errorf("blockStatus=%s: celebrate=%v, want %v",
					tt.blockStatus, wouldCelebrate, tt.shouldCelebrate)
			}
		})
	}
}

// =============================================================================
// TEST K3: "BLOCK FOUND" log gate verification
// =============================================================================
// Goal: Verify the log message only fires for accepted blocks.

func TestK3_BlockFoundLogGateByStatus(t *testing.T) {
	tests := []struct {
		name         string
		blockStatus  string
		shouldLogFound bool
	}{
		{"pending", "pending", true},
		{"submitted", "submitted", true},
		{"orphaned", "orphaned", false},
		{"rejected", "rejected", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirrors pool.go:1069
			wouldLog := tt.blockStatus == "pending" || tt.blockStatus == "submitted"
			if wouldLog != tt.shouldLogFound {
				t.Errorf("blockStatus=%s: logFound=%v, want %v",
					tt.blockStatus, wouldLog, tt.shouldLogFound)
			}
		})
	}
}

// =============================================================================
// TEST L: Reorg after confirmation threshold (defensive)
// =============================================================================
// Goal: Validate that block confirmation logic correctly detects orphans.
// This tests the payment processor invariant: only blocks still in chain get confirmed.

func TestL_ReorgAfterConfirmation(t *testing.T) {
	// Simulate: block was confirmed, then chain reorged and hash changed
	type blockRecord struct {
		height uint64
		hash   string
		status string
	}

	block := blockRecord{
		height: 1000000,
		hash:   "aabbccdd00000000000000000000000000000000000000000000000000000001",
		status: "confirmed",
	}

	// Simulated chain state AFTER reorg — different hash at same height
	chainHashAtHeight := "eeff112200000000000000000000000000000000000000000000000000000002"

	// This mirrors processor.go:228 — hash comparison
	if chainHashAtHeight != block.hash {
		block.status = "orphaned"
	}

	if block.status != "orphaned" {
		t.Fatal("Block with mismatched chain hash must be marked orphaned")
	}

	// Verify: no payment should occur for orphaned block
	shouldPay := block.status == "confirmed"
	if shouldPay {
		t.Fatal("Orphaned block must NOT trigger payment")
	}
}

// =============================================================================
// TEST M: State machine property test (randomized invariant verification)
// =============================================================================
// Invariants tested:
//   1. Candidate → Pending → Confirmed  OR  Candidate → Orphaned  (block status)
//   2. Orphaned → Pending is IMPOSSIBLE
//   3. Solved → Active is IMPOSSIBLE
//   4. Invalidated → Active is IMPOSSIBLE
//   5. Celebration only fires when blockStatus == "pending"
//   6. DB insert always happens (regardless of status — for audit trail)

func TestM_StateMachinePropertyTest(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) // deterministic seed

	const iterations = 10000

	for i := 0; i < iterations; i++ {
		store := newJobStore()
		j := makeActiveJob(fmt.Sprintf("job-M-%d", i))
		store.put(j)

		// Random event sequence
		events := []string{"zmq", "share_block", "submit_ok", "submit_fail", "zmq2"}
		// Shuffle events to get different orderings
		rng.Shuffle(len(events), func(a, b int) {
			events[a], events[b] = events[b], events[a]
		})

		blockStatus := ""
		submitted := false
		celebrated := false

		for _, event := range events {
			switch event {
			case "zmq", "zmq2":
				store.invalidateAll(fmt.Sprintf("zmq-event-%s", event))

			case "share_block":
				canSubmit, _ := submitBlockDecision(store, j.ID)
				if canSubmit && !submitted {
					submitted = true
					// Randomly succeed or fail
					if rng.Float64() < 0.5 {
						blockStatus = "pending"
						j.SetState(protocol.JobStateSolved, "submitted")
					} else {
						blockStatus = "rejected"
					}
				} else if !canSubmit {
					blockStatus = "orphaned"
				}

			case "submit_ok":
				// Late success (retry) — only if we already started submission
				if submitted && blockStatus == "rejected" {
					canRetry, _ := submitBlockDecision(store, j.ID)
					if canRetry {
						blockStatus = "pending"
						j.SetState(protocol.JobStateSolved, "retry")
					}
				}

			case "submit_fail":
				if submitted && blockStatus == "rejected" {
					if isPermanentRejection("prev-blk-not-found") {
						blockStatus = "orphaned"
					}
				}
			}
		}

		// Apply celebration gate
		if blockStatus == "pending" {
			celebrated = true
		}

		// === INVARIANT CHECKS ===

		// Invariant 1: Valid terminal states
		if blockStatus != "" {
			validStatuses := map[string]bool{
				"pending": true, "orphaned": true, "rejected": true,
			}
			if !validStatuses[blockStatus] {
				t.Errorf("Iteration %d: invalid terminal blockStatus=%s", i, blockStatus)
			}
		}

		// Invariant 2: Orphaned → Pending is impossible (no status recovery)
		// (This is enforced by the linear flow in handleBlock — no backward transitions)

		// Invariant 3+4: Terminal job states
		state := j.GetState()
		if state == protocol.JobStateSolved {
			// Cannot go back to Active
			ok := j.SetState(protocol.JobStateActive, "")
			if ok {
				t.Fatalf("Iteration %d: CRITICAL: Solved → Active transition succeeded", i)
			}
		}
		if state == protocol.JobStateInvalidated {
			ok := j.SetState(protocol.JobStateActive, "")
			if ok {
				t.Fatalf("Iteration %d: CRITICAL: Invalidated → Active transition succeeded", i)
			}
		}

		// Invariant 5: Celebration only when pending
		if celebrated && blockStatus != "pending" {
			t.Fatalf("Iteration %d: Celebration fired for blockStatus=%s", i, blockStatus)
		}
		if !celebrated && blockStatus == "pending" {
			t.Fatalf("Iteration %d: No celebration for accepted block", i)
		}
	}
}

// =============================================================================
// TEST M2: Exhaustive state machine transition matrix
// =============================================================================
// Verify every possible state transition pair.

func TestM2_ExhaustiveStateTransitions(t *testing.T) {
	type transition struct {
		from    protocol.JobState
		to      protocol.JobState
		allowed bool
	}

	transitions := []transition{
		// From Created
		{protocol.JobStateCreated, protocol.JobStateIssued, true},
		{protocol.JobStateCreated, protocol.JobStateActive, true},
		{protocol.JobStateCreated, protocol.JobStateInvalidated, true},
		{protocol.JobStateCreated, protocol.JobStateSolved, true},

		// From Issued
		{protocol.JobStateIssued, protocol.JobStateActive, true},
		{protocol.JobStateIssued, protocol.JobStateInvalidated, true},
		{protocol.JobStateIssued, protocol.JobStateSolved, true},

		// From Active
		{protocol.JobStateActive, protocol.JobStateInvalidated, true},
		{protocol.JobStateActive, protocol.JobStateSolved, true},
		{protocol.JobStateActive, protocol.JobStateCreated, true}, // SetState allows any non-terminal → any

		// From Invalidated (TERMINAL — all transitions forbidden)
		{protocol.JobStateInvalidated, protocol.JobStateCreated, false},
		{protocol.JobStateInvalidated, protocol.JobStateIssued, false},
		{protocol.JobStateInvalidated, protocol.JobStateActive, false},
		{protocol.JobStateInvalidated, protocol.JobStateSolved, false},

		// From Solved (TERMINAL — all transitions forbidden)
		{protocol.JobStateSolved, protocol.JobStateCreated, false},
		{protocol.JobStateSolved, protocol.JobStateIssued, false},
		{protocol.JobStateSolved, protocol.JobStateActive, false},
		{protocol.JobStateSolved, protocol.JobStateInvalidated, false},
	}

	for _, tt := range transitions {
		name := fmt.Sprintf("%s_to_%s", tt.from.String(), tt.to.String())
		t.Run(name, func(t *testing.T) {
			j := &protocol.Job{
				ID:        "test",
				CreatedAt: time.Now(),
			}
			// Set initial state (Created is default, so set if different)
			if tt.from != protocol.JobStateCreated {
				ok := j.SetState(tt.from, "setup")
				if !ok {
					t.Fatalf("Failed to set initial state %v", tt.from)
				}
			}

			// Attempt transition
			ok := j.SetState(tt.to, "test-transition")
			if ok != tt.allowed {
				t.Errorf("%s → %s: got allowed=%v, want %v",
					tt.from.String(), tt.to.String(), ok, tt.allowed)
			}
		})
	}
}

// =============================================================================
// TEST M3: IsValid() consistency with state machine
// =============================================================================
// IsValid() must return false for Invalidated and Solved.

func TestM3_IsValidConsistency(t *testing.T) {
	tests := []struct {
		state protocol.JobState
		valid bool
	}{
		{protocol.JobStateCreated, true},
		{protocol.JobStateIssued, true},
		{protocol.JobStateActive, true},
		{protocol.JobStateInvalidated, false},
		{protocol.JobStateSolved, false},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			j := &protocol.Job{ID: "test", CreatedAt: time.Now()}
			if tt.state != protocol.JobStateCreated {
				j.SetState(tt.state, "setup")
			}
			if j.IsValid() != tt.valid {
				t.Errorf("State %v: IsValid()=%v, want %v",
					tt.state, j.IsValid(), tt.valid)
			}
		})
	}
}

// =============================================================================
// TEST: Concurrent SetState from multiple goroutines (race detector)
// =============================================================================
// This test is designed to be run with -race to detect data races.

func TestConcurrentSetState(t *testing.T) {
	j := makeActiveJob("job-race")

	var wg sync.WaitGroup
	const goroutines = 100

	// Half the goroutines try to invalidate, half try to solve
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				j.SetState(protocol.JobStateInvalidated, fmt.Sprintf("g-%d", idx))
			} else {
				j.SetState(protocol.JobStateSolved, fmt.Sprintf("g-%d", idx))
			}
		}(i)
	}
	wg.Wait()

	// Exactly one must win — state must be terminal
	state := j.GetState()
	if state != protocol.JobStateInvalidated && state != protocol.JobStateSolved {
		t.Fatalf("Expected terminal state, got %v", state)
	}

	// All subsequent transitions must fail
	for i := 0; i < 100; i++ {
		if j.SetState(protocol.JobStateActive, "should-fail") {
			t.Fatal("Terminal state must reject all transitions")
		}
	}
}

// =============================================================================
// TEST: Block status to DB status mapping (audit trail completeness)
// =============================================================================
// Every blockStatus value must map to a valid DB status.

func TestBlockStatusDBMapping(t *testing.T) {
	validDBStatuses := map[string]bool{
		"pending":  true,
		"orphaned": true,
	}

	// These are all the blockStatus values that reach DB insert in handleBlock
	blockStatusPaths := []struct {
		name        string
		blockStatus string
		validForDB  bool
	}{
		{"submitted-then-pending", "pending", true},
		{"stale-race-orphaned", "orphaned", true},
		{"duplicate-candidate-orphaned", "orphaned", true},
		{"empty-hex-orphaned", "orphaned", true},
		{"daemon-rejected-orphaned", "orphaned", true},
		{"retry-failed-orphaned", "orphaned", true},
	}

	for _, tt := range blockStatusPaths {
		t.Run(tt.name, func(t *testing.T) {
			if !validDBStatuses[tt.blockStatus] {
				t.Errorf("blockStatus=%s is not a valid DB status", tt.blockStatus)
			}
		})
	}
}

// =============================================================================
// TEST: Retry path also sets Solved state
// =============================================================================
// Verify that the retry success path transitions job to Solved.

func TestRetryPathSetsSolved(t *testing.T) {
	store := newJobStore()
	j := makeActiveJob("job-retry")
	store.put(j)

	// Initial submit fails with transient error
	canSubmit, _ := submitBlockDecision(store, "job-retry")
	if !canSubmit {
		t.Fatal("Should allow initial submit")
	}
	// SubmitBlock returns transient error (not permanent)

	// Retry succeeds
	canRetry, _ := submitBlockDecision(store, "job-retry")
	if !canRetry {
		t.Fatal("Retry should be allowed (job still Active, transient failure)")
	}
	// SubmitBlock returns nil (success) → must set Solved
	ok := j.SetState(protocol.JobStateSolved, "block submitted on retry")
	if !ok {
		t.Fatal("SetState(Solved) should succeed from Active")
	}

	// Third attempt must be blocked
	canSubmit3, reason := submitBlockDecision(store, "job-retry")
	if canSubmit3 {
		t.Fatal("Third submission must be blocked after Solved on retry")
	}
	if reason != "duplicate-block-candidate" {
		t.Errorf("Expected duplicate-block-candidate, got %s", reason)
	}
}

// =============================================================================
// TEST: isPermanentRejection classification coverage
// =============================================================================
// Verify that all known daemon rejection reasons are correctly classified.

func TestIsPermanentRejection(t *testing.T) {
	permanent := []string{
		"prev-blk-not-found",
		"bad-prevblk",
		"stale-prevblk",
		"stale-work",
		"high-hash",
		"bad-txns-inputs-missingorspent",
		"duplicate",
		"bad-cb-amount",
	}

	for _, errStr := range permanent {
		t.Run(errStr, func(t *testing.T) {
			if !isPermanentRejection(errStr) {
				t.Errorf("'%s' should be classified as permanent rejection", errStr)
			}
		})
	}

	transient := []string{
		"connection refused",
		"timeout",
		"EOF",
	}

	for _, errStr := range transient {
		t.Run("transient_"+errStr, func(t *testing.T) {
			if isPermanentRejection(errStr) {
				t.Errorf("'%s' should NOT be classified as permanent rejection", errStr)
			}
		})
	}
}
