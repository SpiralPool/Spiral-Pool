// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Stratum Boundary Audit — Protocol layer tests
// Vectors: S7 (Job state machine), S12-S14 (Session FSM), S15 (Pre-auth counter)
package protocol

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// S7 — Job State Machine Terminal Transitions
// =============================================================================

// TestS7_JobState_InvalidatedIsTerminal verifies that Invalidated is a terminal
// state: once a Job reaches Invalidated, no further transitions are allowed.
func TestS7_JobState_InvalidatedIsTerminal(t *testing.T) {
	t.Parallel()

	job := &Job{ID: "s7-invalidated-terminal", CreatedAt: time.Now()}

	// Created → Active (valid)
	if ok := job.SetState(JobStateActive, ""); !ok {
		t.Fatal("SetState Created→Active must succeed")
	}

	// Active → Invalidated (valid, enters terminal)
	if ok := job.SetState(JobStateInvalidated, "new block arrived"); !ok {
		t.Fatal("SetState Active→Invalidated must succeed")
	}

	// Invalidated → Active (must be rejected — terminal)
	if ok := job.SetState(JobStateActive, ""); ok {
		t.Fatal("SetState Invalidated→Active must return false; Invalidated is terminal")
	}

	// State must still be Invalidated
	if got := job.GetState(); got != JobStateInvalidated {
		t.Fatalf("expected state Invalidated, got %v", got)
	}
}

// TestS7_JobState_SolvedIsTerminal verifies that Solved is a terminal state:
// once a Job reaches Solved, no further transitions are allowed.
func TestS7_JobState_SolvedIsTerminal(t *testing.T) {
	t.Parallel()

	job := &Job{ID: "s7-solved-terminal", CreatedAt: time.Now()}

	// Created → Active
	if ok := job.SetState(JobStateActive, ""); !ok {
		t.Fatal("SetState Created→Active must succeed")
	}

	// Active → Solved (valid, enters terminal)
	if ok := job.SetState(JobStateSolved, "block found"); !ok {
		t.Fatal("SetState Active→Solved must succeed")
	}

	// Solved → Invalidated (must be rejected — terminal)
	if ok := job.SetState(JobStateInvalidated, "should not work"); ok {
		t.Fatal("SetState Solved→Invalidated must return false; Solved is terminal")
	}

	// State must still be Solved
	if got := job.GetState(); got != JobStateSolved {
		t.Fatalf("expected state Solved, got %v", got)
	}
}

// TestS7_JobState_ValidTransitions walks the full happy-path lifecycle:
// Created → Issued → Active → Invalidated, verifying each transition succeeds.
func TestS7_JobState_ValidTransitions(t *testing.T) {
	t.Parallel()

	job := &Job{ID: "s7-valid-chain", CreatedAt: time.Now()}

	transitions := []struct {
		state  JobState
		reason string
	}{
		{JobStateIssued, "queued for broadcast"},
		{JobStateActive, "sent to miners"},
		{JobStateInvalidated, "new block detected"},
	}

	for _, tr := range transitions {
		if ok := job.SetState(tr.state, tr.reason); !ok {
			t.Fatalf("SetState to %v must succeed, got false", tr.state)
		}
		if got := job.GetState(); got != tr.state {
			t.Fatalf("expected state %v after transition, got %v", tr.state, got)
		}
	}
}

// TestS7_JobState_ConcurrentTransitions launches 100 goroutines that all race
// to move the same Job into a terminal state. Exactly one must succeed for each
// terminal-entering CAS, and the test must not panic or trigger the race detector.
func TestS7_JobState_ConcurrentTransitions(t *testing.T) {
	t.Parallel()

	job := &Job{ID: "s7-concurrent", CreatedAt: time.Now()}

	// Move to Active so the terminal transition is meaningful
	if ok := job.SetState(JobStateActive, ""); !ok {
		t.Fatal("SetState Created→Active must succeed")
	}

	const numGoroutines = 100
	var (
		wg       sync.WaitGroup
		winners  atomic.Int64
		startGun = make(chan struct{}) // barrier so all goroutines start together
	)

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			<-startGun // wait for barrier

			// Half try Invalidated, half try Solved
			var ok bool
			if id%2 == 0 {
				ok = job.SetState(JobStateInvalidated, "concurrent invalidation")
			} else {
				ok = job.SetState(JobStateSolved, "concurrent solve")
			}
			if ok {
				winners.Add(1)
			}
		}(i)
	}

	close(startGun) // release all goroutines at once
	wg.Wait()

	// Exactly one goroutine should have won the terminal transition
	if w := winners.Load(); w != 1 {
		t.Fatalf("expected exactly 1 winner for terminal transition, got %d", w)
	}

	// Final state must be one of the terminal states
	finalState := job.GetState()
	if finalState != JobStateInvalidated && finalState != JobStateSolved {
		t.Fatalf("expected terminal state, got %v", finalState)
	}
}

// =============================================================================
// S12 / S13 / S14 — Session FSM Atomics
// =============================================================================

// TestS12_SessionFSM_SubscribedDefault verifies a new Session starts unsubscribed.
func TestS12_SessionFSM_SubscribedDefault(t *testing.T) {
	t.Parallel()

	s := &Session{}
	if s.IsSubscribed() {
		t.Fatal("new Session must have IsSubscribed() == false")
	}
}

// TestS12_SessionFSM_AuthorizedDefault verifies a new Session starts unauthorized.
func TestS12_SessionFSM_AuthorizedDefault(t *testing.T) {
	t.Parallel()

	s := &Session{}
	if s.IsAuthorized() {
		t.Fatal("new Session must have IsAuthorized() == false")
	}
}

// TestS12_SessionFSM_SetSubscribed verifies that SetSubscribed(true) flips the flag.
func TestS12_SessionFSM_SetSubscribed(t *testing.T) {
	t.Parallel()

	s := &Session{}
	s.SetSubscribed(true)

	if !s.IsSubscribed() {
		t.Fatal("IsSubscribed() must return true after SetSubscribed(true)")
	}

	// Also verify round-trip back to false
	s.SetSubscribed(false)
	if s.IsSubscribed() {
		t.Fatal("IsSubscribed() must return false after SetSubscribed(false)")
	}
}

// TestS14_SessionFSM_SetAuthorized verifies that SetAuthorized(true) flips the flag.
func TestS14_SessionFSM_SetAuthorized(t *testing.T) {
	t.Parallel()

	s := &Session{}
	s.SetAuthorized(true)

	if !s.IsAuthorized() {
		t.Fatal("IsAuthorized() must return true after SetAuthorized(true)")
	}

	// Also verify round-trip back to false
	s.SetAuthorized(false)
	if s.IsAuthorized() {
		t.Fatal("IsAuthorized() must return false after SetAuthorized(false)")
	}
}

// TestS14_SessionFSM_ConcurrentAccess launches 100 goroutines that simultaneously
// read and write subscribed/authorized flags. The test must not panic and must be
// clean under -race.
func TestS14_SessionFSM_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	s := &Session{}

	const numGoroutines = 100
	var (
		wg       sync.WaitGroup
		startGun = make(chan struct{})
	)

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			<-startGun

			for j := 0; j < 100; j++ {
				// Writers
				s.SetSubscribed(id%2 == 0)
				s.SetAuthorized(id%3 == 0)

				// Readers — results are non-deterministic but must not race
				_ = s.IsSubscribed()
				_ = s.IsAuthorized()
			}
		}(i)
	}

	close(startGun)
	wg.Wait()
	// Pass criterion: no panics, no data races (verified by -race flag)
}

// =============================================================================
// S15 — Pre-auth Message Counter
// =============================================================================

// TestS15_PreAuthMessages_Increment verifies that IncrementPreAuthMessages returns
// monotonically incrementing values starting from 1.
func TestS15_PreAuthMessages_Increment(t *testing.T) {
	t.Parallel()

	s := &Session{}

	for i := uint32(1); i <= 10; i++ {
		got := s.IncrementPreAuthMessages()
		if got != i {
			t.Fatalf("IncrementPreAuthMessages call %d: expected %d, got %d", i, i, got)
		}
	}

	if final := s.GetPreAuthMessages(); final != 10 {
		t.Fatalf("expected GetPreAuthMessages() == 10, got %d", final)
	}
}

// TestS15_PreAuthMessages_ConcurrentIncrement launches 100 goroutines, each
// incrementing N times. The final count must equal 100*N exactly (no lost updates).
func TestS15_PreAuthMessages_ConcurrentIncrement(t *testing.T) {
	t.Parallel()

	s := &Session{}

	const (
		numGoroutines         = 100
		incrementsPerRoutine  = 50
	)
	var (
		wg       sync.WaitGroup
		startGun = make(chan struct{})
	)

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			<-startGun

			for j := 0; j < incrementsPerRoutine; j++ {
				s.IncrementPreAuthMessages()
			}
		}()
	}

	close(startGun)
	wg.Wait()

	expected := uint32(numGoroutines * incrementsPerRoutine)
	if got := s.GetPreAuthMessages(); got != expected {
		t.Fatalf("expected final pre-auth count %d, got %d", expected, got)
	}
}

// =============================================================================
// Session Difficulty Grace Window
// =============================================================================

// TestSession_DifficultyGraceWindow verifies that after rapid vardiff ramp-up
// within the grace window, GetDifficultyForJob returns the minimum difficulty
// seen in the window (protecting cgminer/Avalon miners with work-in-progress
// at the original difficulty).
func TestSession_DifficultyGraceWindow(t *testing.T) {
	t.Parallel()

	s := &Session{}
	s.SetBlockTime(15) // 15s blocks → 30s grace window

	// First difficulty set (initialises grace window)
	s.SetDifficulty(500)

	// Rapid vardiff ramp-up within the same grace window
	s.SetDifficulty(2000)

	// Job 0 was issued before any change; the minimum in the grace window is
	// still 500. GetDifficultyForJob must return the minimum (500) because
	// miners may have in-flight shares at the original difficulty.
	got := s.GetDifficultyForJob(0)
	if got != 500 {
		t.Fatalf("expected GetDifficultyForJob(0) == 500 (min in grace window), got %v", got)
	}
}

// TestSession_GracePeriodCalculation verifies the dynamic grace period formula:
//   grace = clamp(blockTime * 2, 30s, 120s)
func TestSession_GracePeriodCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		blockTimeSec    int
		expectedGraceSec int64
	}{
		{
			name:            "DGB 15s blocks → 30s grace (minimum)",
			blockTimeSec:    15,
			expectedGraceSec: 30,
		},
		{
			name:            "DOGE 60s blocks → 120s grace (2×60 capped)",
			blockTimeSec:    60,
			expectedGraceSec: 120,
		},
		{
			name:            "LTC 150s blocks → 120s grace (capped at max)",
			blockTimeSec:    300,
			expectedGraceSec: 120,
		},
	}

	for _, tc := range tests {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := &Session{}
			s.SetBlockTime(tc.blockTimeSec)

			// getGracePeriod is unexported but returns nanoseconds.
			// We can verify indirectly through the exported API: set two diffs
			// close together (within grace), confirm min is returned; or we
			// can call the method directly since this is same-package testing.
			gotNano := s.getGracePeriod()
			expectedNano := tc.expectedGraceSec * int64(time.Second)

			if gotNano != expectedNano {
				t.Fatalf("expected grace period %d ns (%d s), got %d ns (%d s)",
					expectedNano, tc.expectedGraceSec,
					gotNano, gotNano/int64(time.Second))
			}
		})
	}
}

// =============================================================================
// Job IsValid
// =============================================================================

// TestJob_IsValid_ActiveStates verifies that Jobs in Created, Issued, and Active
// states are considered valid (i.e., they accept shares).
func TestJob_IsValid_ActiveStates(t *testing.T) {
	t.Parallel()

	validStates := []struct {
		name  string
		state JobState
	}{
		{"Created", JobStateCreated},
		{"Issued", JobStateIssued},
		{"Active", JobStateActive},
	}

	for _, tc := range validStates {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			job := &Job{ID: "isvalid-" + tc.name, CreatedAt: time.Now()}
			// Transition through the chain to reach the desired state
			// (SetState enforces no-exit from terminal, but non-terminal
			//  transitions are unrestricted in the current implementation)
			if tc.state != JobStateCreated {
				if ok := job.SetState(tc.state, ""); !ok {
					t.Fatalf("SetState to %v must succeed from Created", tc.state)
				}
			}

			if !job.IsValid() {
				t.Fatalf("Job in state %v must be valid", tc.state)
			}
		})
	}
}

// TestJob_IsValid_TerminalStates verifies that Jobs in Invalidated and Solved
// states are NOT valid (i.e., shares are rejected).
func TestJob_IsValid_TerminalStates(t *testing.T) {
	t.Parallel()

	terminalStates := []struct {
		name  string
		state JobState
	}{
		{"Invalidated", JobStateInvalidated},
		{"Solved", JobStateSolved},
	}

	for _, tc := range terminalStates {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			job := &Job{ID: "isvalid-terminal-" + tc.name, CreatedAt: time.Now()}
			// Move to Active first, then to the terminal state
			if ok := job.SetState(JobStateActive, ""); !ok {
				t.Fatal("SetState to Active must succeed")
			}
			if ok := job.SetState(tc.state, "terminal reason"); !ok {
				t.Fatalf("SetState to %v must succeed from Active", tc.state)
			}

			if job.IsValid() {
				t.Fatalf("Job in terminal state %v must NOT be valid", tc.state)
			}
		})
	}
}
