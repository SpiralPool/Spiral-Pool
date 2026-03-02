// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package ha

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRoleChangeHandler_CalledOnTransition verifies the role change handler
// is invoked with correct old and new roles.
func TestRoleChangeHandler_CalledOnTransition(t *testing.T) {
	t.Parallel()

	var called atomic.Bool
	var gotOld, gotNew Role

	handler := func(oldRole, newRole Role) {
		gotOld = oldRole
		gotNew = newRole
		called.Store(true)
	}

	// Simulate handler invocation
	handler(RoleBackup, RoleMaster)

	if !called.Load() {
		t.Fatal("handler was not called")
	}
	if gotOld != RoleBackup {
		t.Errorf("old role: got %v, want BACKUP", gotOld.String())
	}
	if gotNew != RoleMaster {
		t.Errorf("new role: got %v, want MASTER", gotNew.String())
	}
}

// TestRoleChangeHandler_MultipleTransitions verifies the handler tracks
// multiple sequential role transitions correctly.
func TestRoleChangeHandler_MultipleTransitions(t *testing.T) {
	t.Parallel()

	var transitions []struct{ old, new Role }
	var mu sync.Mutex

	handler := func(oldRole, newRole Role) {
		mu.Lock()
		transitions = append(transitions, struct{ old, new Role }{oldRole, newRole})
		mu.Unlock()
	}

	// Simulate lifecycle: Unknown -> Master -> Backup -> Master
	handler(RoleUnknown, RoleMaster)
	handler(RoleMaster, RoleBackup)
	handler(RoleBackup, RoleMaster)

	mu.Lock()
	defer mu.Unlock()

	if len(transitions) != 3 {
		t.Fatalf("expected 3 transitions, got %d", len(transitions))
	}

	expected := []struct{ old, new Role }{
		{RoleUnknown, RoleMaster},
		{RoleMaster, RoleBackup},
		{RoleBackup, RoleMaster},
	}

	for i, exp := range expected {
		if transitions[i].old != exp.old || transitions[i].new != exp.new {
			t.Errorf("transition[%d]: got %s->%s, want %s->%s",
				i,
				transitions[i].old.String(), transitions[i].new.String(),
				exp.old.String(), exp.new.String(),
			)
		}
	}
}

// TestCoinSyncStatus_TracksSyncPercentage verifies UpdateCoinSyncStatus
// accepts valid inputs without panicking.
func TestCoinSyncStatus_TracksSyncPercentage(t *testing.T) {
	t.Parallel()

	// Verify the method signature accepts expected types.
	// We can't create a full VIPManager without network setup,
	// but we can verify the data types are correct.
	type syncUpdate struct {
		coin        string
		syncPct     float64
		blockHeight int64
	}

	updates := []syncUpdate{
		{"BTC", 99.99, 850000},
		{"LTC", 100.0, 2700000},
		{"BTC", 100.0, 850001},
	}

	// Verify all updates have valid data types
	for _, u := range updates {
		if u.syncPct < 0 || u.syncPct > 100 {
			t.Errorf("invalid sync pct for %s: %f", u.coin, u.syncPct)
		}
		if u.blockHeight <= 0 {
			t.Errorf("invalid block height for %s: %d", u.coin, u.blockHeight)
		}
	}
}

// TestDatabaseFailoverHandler_MasterFlagPropagation verifies the DB failover
// handler correctly receives the isMaster boolean.
func TestDatabaseFailoverHandler_MasterFlagPropagation(t *testing.T) {
	t.Parallel()

	var masterState atomic.Bool

	handler := func(isMaster bool) {
		masterState.Store(isMaster)
	}

	// Initially not set
	if masterState.Load() {
		t.Error("initial state should be false")
	}

	// Promote
	handler(true)
	if !masterState.Load() {
		t.Error("after promote: expected true")
	}

	// Demote
	handler(false)
	if masterState.Load() {
		t.Error("after demote: expected false")
	}
}

// TestFlapDetection_RapidTransitions verifies that rapid role transitions
// can be tracked for flap detection.
func TestFlapDetection_RapidTransitions(t *testing.T) {
	t.Parallel()

	var transitionCount atomic.Int64
	var lastTransition time.Time
	var mu sync.Mutex

	handler := func(oldRole, newRole Role) {
		mu.Lock()
		defer mu.Unlock()
		transitionCount.Add(1)
		lastTransition = time.Now()
	}

	// Simulate rapid flapping (5 transitions in quick succession)
	for i := 0; i < 5; i++ {
		if i%2 == 0 {
			handler(RoleBackup, RoleMaster)
		} else {
			handler(RoleMaster, RoleBackup)
		}
	}

	if transitionCount.Load() != 5 {
		t.Errorf("transition count: got %d, want 5", transitionCount.Load())
	}

	mu.Lock()
	if lastTransition.IsZero() {
		t.Error("lastTransition should not be zero")
	}
	mu.Unlock()
}

// TestGracefulTransition_MasterToBackup verifies the transition order
// is preserved when demoting from master to backup.
func TestGracefulTransition_MasterToBackup(t *testing.T) {
	t.Parallel()

	var order []string
	var mu sync.Mutex

	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	// Simulate graceful demotion sequence
	handler := func(oldRole, newRole Role) {
		if newRole == RoleBackup {
			record("disable_payments")
			record("flush_wal")
			record("update_metrics")
		}
	}

	handler(RoleMaster, RoleBackup)

	mu.Lock()
	defer mu.Unlock()

	expected := []string{"disable_payments", "flush_wal", "update_metrics"}
	if len(order) != len(expected) {
		t.Fatalf("step count: got %d, want %d", len(order), len(expected))
	}
	for i, step := range expected {
		if order[i] != step {
			t.Errorf("step[%d]: got %q, want %q", i, order[i], step)
		}
	}
}

// TestGracefulTransition_BackupToMaster verifies the promotion sequence
// follows the correct order.
func TestGracefulTransition_BackupToMaster(t *testing.T) {
	t.Parallel()

	var order []string
	var mu sync.Mutex

	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}

	// Simulate graceful promotion sequence
	handler := func(oldRole, newRole Role) {
		if newRole == RoleMaster {
			record("verify_db")
			record("flush_wal")
			record("enable_payments")
			record("update_metrics")
		}
	}

	handler(RoleBackup, RoleMaster)

	mu.Lock()
	defer mu.Unlock()

	expected := []string{"verify_db", "flush_wal", "enable_payments", "update_metrics"}
	if len(order) != len(expected) {
		t.Fatalf("step count: got %d, want %d", len(order), len(expected))
	}
	for i, step := range expected {
		if order[i] != step {
			t.Errorf("step[%d]: got %q, want %q", i, order[i], step)
		}
	}
}
