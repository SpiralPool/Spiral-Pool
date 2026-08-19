// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package vardiff

import (
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
)

// =============================================================================
// TEST SUITE: Idle Descent
// =============================================================================
// IdleDescend closes the one gap every other retarget path leaves open: a session
// issued a difficulty above its hardware's reach produces no accepted shares, and
// every other path needs an accepted share to act. These tests use a sub-second
// target time so a full idle window elapses in milliseconds.

const (
	testIdleTarget = 0.01 // seconds between shares
	testIdleFactor = 6.0  // window = 60ms
)

func newIdleSession(t *testing.T, initial, min float64) (*Engine, *SessionState) {
	t.Helper()
	e := NewEngine(config.VarDiffConfig{
		MinDiff:         min,
		MaxDiff:         100,
		TargetTime:      testIdleTarget,
		RetargetTime:    60,
		VariancePercent: 50,
	})
	return e, e.NewSessionStateWithProfile(initial, min, 100, testIdleTarget)
}

// waitOutIdleWindow sleeps past idleFactor x targetTime with margin.
func waitOutIdleWindow() {
	time.Sleep(time.Duration(testIdleFactor*testIdleTarget*1000)*time.Millisecond + 40*time.Millisecond)
}

// TestIdleDescend_DisabledByDefault ensures the behaviour is strictly opt-in.
// Enabling it globally would fight the deliberate work-in-progress tolerance that
// cgminer-based classes depend on.
func TestIdleDescend_DisabledByDefault(t *testing.T) {
	e, state := newIdleSession(t, 0.004, 0.0001)

	if IdleDescentEnabled(state) {
		t.Fatal("idle descent must default to off")
	}

	waitOutIdleWindow()

	if diff, changed := e.IdleDescend(state, testIdleFactor); changed {
		t.Errorf("descended while disabled: got %v", diff)
	}
}

// TestIdleDescend_HalvesAfterIdleWindow covers the case the feature exists for:
// a session that has never landed a share still finds its way down. lastShareNano
// is zero until the first accepted share, so this also pins the fallback to
// lastRetargetNano (stamped at session creation).
func TestIdleDescend_HalvesAfterIdleWindow(t *testing.T) {
	e, state := newIdleSession(t, 0.004, 0.0001)
	SetIdleDescent(state, true)

	if state.lastShareNano.Load() != 0 {
		t.Fatal("precondition: session should have no shares yet")
	}

	waitOutIdleWindow()

	diff, changed := e.IdleDescend(state, testIdleFactor)
	if !changed {
		t.Fatal("expected descent after a full idle window")
	}
	if diff != 0.002 {
		t.Errorf("expected halving to 0.002, got %v", diff)
	}
	if got := GetDifficulty(state); got != 0.002 {
		t.Errorf("session difficulty not updated: got %v", got)
	}
}

// TestIdleDescend_NotYetIdle guards against descending on a session that is simply
// between shares.
func TestIdleDescend_NotYetIdle(t *testing.T) {
	e, state := newIdleSession(t, 0.004, 0.0001)
	SetIdleDescent(state, true)

	if _, changed := e.IdleDescend(state, testIdleFactor); changed {
		t.Error("descended before the idle window elapsed")
	}
}

// TestIdleDescend_WindowRestartsAfterDescent verifies each halving costs a full
// window, so a stranded session walks down rather than dropping to the floor at once.
func TestIdleDescend_WindowRestartsAfterDescent(t *testing.T) {
	e, state := newIdleSession(t, 0.004, 0.0001)
	SetIdleDescent(state, true)

	waitOutIdleWindow()
	if _, changed := e.IdleDescend(state, testIdleFactor); !changed {
		t.Fatal("expected first descent")
	}
	if _, changed := e.IdleDescend(state, testIdleFactor); changed {
		t.Error("second descent should require another full idle window")
	}

	waitOutIdleWindow()
	diff, changed := e.IdleDescend(state, testIdleFactor)
	if !changed {
		t.Fatal("expected second descent after another window")
	}
	if diff != 0.001 {
		t.Errorf("expected 0.001 after two halvings, got %v", diff)
	}
}

// TestIdleDescend_ClampsToMinDiff ensures descent never undercuts the class floor.
func TestIdleDescend_ClampsToMinDiff(t *testing.T) {
	e, state := newIdleSession(t, 0.0003, 0.0002)
	SetIdleDescent(state, true)

	waitOutIdleWindow()

	diff, changed := e.IdleDescend(state, testIdleFactor)
	if !changed {
		t.Fatal("expected descent")
	}
	if diff != 0.0002 {
		t.Errorf("expected clamp to MinDiff 0.0002, got %v", diff)
	}
}

// TestIdleDescend_NoOpAtFloor stops a session already at MinDiff from churning
// retarget timestamps every tick.
func TestIdleDescend_NoOpAtFloor(t *testing.T) {
	e, state := newIdleSession(t, 0.0001, 0.0001)
	SetIdleDescent(state, true)

	waitOutIdleWindow()

	if diff, changed := e.IdleDescend(state, testIdleFactor); changed {
		t.Errorf("descended while already at floor: got %v", diff)
	}
}

// TestIdleDescend_RejectsNonPositiveFactor covers the misconfiguration guard.
func TestIdleDescend_RejectsNonPositiveFactor(t *testing.T) {
	e, state := newIdleSession(t, 0.004, 0.0001)
	SetIdleDescent(state, true)

	waitOutIdleWindow()

	if _, changed := e.IdleDescend(state, 0); changed {
		t.Error("descended with a zero idle factor")
	}
	if _, changed := e.IdleDescend(state, -1); changed {
		t.Error("descended with a negative idle factor")
	}
}

// TestIdleDescend_AcceptedShareResetsWindow verifies a session that is producing
// shares is never treated as stranded.
func TestIdleDescend_AcceptedShareResetsWindow(t *testing.T) {
	e, state := newIdleSession(t, 0.004, 0.0001)
	SetIdleDescent(state, true)

	waitOutIdleWindow()
	e.RecordShare(state) // stamps lastShareNano

	if _, changed := e.IdleDescend(state, testIdleFactor); changed {
		t.Error("descended despite a share arriving within the window")
	}
}
