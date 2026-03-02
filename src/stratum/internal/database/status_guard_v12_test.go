// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package database

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// =============================================================================
// TEST SUITE: V12 Status Guard, V6 RowsAffected, V26 Callback
// =============================================================================
// These tests verify the status guard WHERE clause logic (V12), RowsAffected
// detection for nonexistent blocks (V6), the OnStatusGuardRejection callback
// mechanism (V26), and pool ID SQL injection prevention via validPoolID regex.

// -----------------------------------------------------------------------------
// Status Guard Helper
// -----------------------------------------------------------------------------

// statusGuardAllows mirrors the SQL WHERE clause from UpdateBlockStatus:
//
//	WHERE (status = 'pending' OR (status = 'confirmed' AND $1 IN ('orphaned', 'paid')))
//
// It returns true if the transition from currentStatus to newStatus is allowed
// by the guard. This is a pure-logic mirror of the SQL predicate — it does NOT
// touch any database.
func statusGuardAllows(currentStatus, newStatus string) bool {
	if currentStatus == "pending" {
		return true
	}
	if currentStatus == "confirmed" && (newStatus == "orphaned" || newStatus == "paid") {
		return true
	}
	return false
}

// =============================================================================
// V12 — Status Guard SQL WHERE Clause Logic
// =============================================================================

// TestV12_StatusGuard_PendingAllowsAll verifies that a pending block can
// transition to any other status. The SQL WHERE clause unconditionally passes
// when status = 'pending'.
func TestV12_StatusGuard_PendingAllowsAll(t *testing.T) {
	t.Parallel()

	targets := []string{"pending", "confirmed", "orphaned", "paid"}
	for _, target := range targets {
		if !statusGuardAllows("pending", target) {
			t.Errorf("pending → %s should be ALLOWED", target)
		}
	}
}

// TestV12_StatusGuard_ConfirmedAllowsOrphanedAndPaid verifies that a confirmed
// block can transition to orphaned (deep reorg) or paid (payment processed).
func TestV12_StatusGuard_ConfirmedAllowsOrphanedAndPaid(t *testing.T) {
	t.Parallel()

	if !statusGuardAllows("confirmed", "orphaned") {
		t.Error("confirmed → orphaned should be ALLOWED (deep reorg)")
	}
	if !statusGuardAllows("confirmed", "paid") {
		t.Error("confirmed → paid should be ALLOWED (payment)")
	}
}

// TestV12_StatusGuard_ConfirmedBlocksPending verifies that a confirmed block
// cannot be demoted back to pending. This is the core V12 fix — a stale
// process must not reverse confirmation.
func TestV12_StatusGuard_ConfirmedBlocksPending(t *testing.T) {
	t.Parallel()

	if statusGuardAllows("confirmed", "pending") {
		t.Error("confirmed → pending should be BLOCKED (stale demotion)")
	}
}

// TestV12_StatusGuard_OrphanedIsTerminal verifies that an orphaned block
// cannot transition to any other status. Once orphaned, it stays orphaned.
func TestV12_StatusGuard_OrphanedIsTerminal(t *testing.T) {
	t.Parallel()

	targets := []string{"pending", "confirmed", "orphaned", "paid"}
	for _, target := range targets {
		if statusGuardAllows("orphaned", target) {
			t.Errorf("orphaned → %s should be BLOCKED (terminal state)", target)
		}
	}
}

// TestV12_StatusGuard_PaidIsTerminal verifies that a paid block cannot
// transition to any other status. Once paid, it stays paid.
func TestV12_StatusGuard_PaidIsTerminal(t *testing.T) {
	t.Parallel()

	targets := []string{"pending", "confirmed", "orphaned", "paid"}
	for _, target := range targets {
		if statusGuardAllows("paid", target) {
			t.Errorf("paid → %s should be BLOCKED (terminal state)", target)
		}
	}
}

// TestV12_StatusGuard_ComprehensiveMatrix exhaustively tests all 16
// combinations of the 4 statuses (pending, confirmed, orphaned, paid).
func TestV12_StatusGuard_ComprehensiveMatrix(t *testing.T) {
	t.Parallel()

	type transition struct {
		from    string
		to      string
		allowed bool
	}

	// Full 4×4 matrix — every combination explicitly enumerated.
	matrix := []transition{
		// From pending: all transitions allowed
		{"pending", "pending", true},
		{"pending", "confirmed", true},
		{"pending", "orphaned", true},
		{"pending", "paid", true},

		// From confirmed: only orphaned and paid allowed
		{"confirmed", "pending", false},
		{"confirmed", "confirmed", false},
		{"confirmed", "orphaned", true},
		{"confirmed", "paid", true},

		// From orphaned: terminal — nothing allowed
		{"orphaned", "pending", false},
		{"orphaned", "confirmed", false},
		{"orphaned", "orphaned", false},
		{"orphaned", "paid", false},

		// From paid: terminal — nothing allowed
		{"paid", "pending", false},
		{"paid", "confirmed", false},
		{"paid", "orphaned", false},
		{"paid", "paid", false},
	}

	for _, tc := range matrix {
		tc := tc
		t.Run(tc.from+"→"+tc.to, func(t *testing.T) {
			t.Parallel()
			got := statusGuardAllows(tc.from, tc.to)
			if got != tc.allowed {
				verb := "ALLOWED"
				if !tc.allowed {
					verb = "BLOCKED"
				}
				t.Errorf("%s → %s: expected %s (got %v)", tc.from, tc.to, verb, got)
			}
		})
	}
}

// =============================================================================
// V6 — RowsAffected == 0 Detection
// =============================================================================

// TestV6_RowsAffected_NonexistentBlock verifies that updating a block that
// does not exist in the store returns false, mirroring RowsAffected == 0.
func TestV6_RowsAffected_NonexistentBlock(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	// No blocks inserted — every update should return false (0 rows).
	if store.UpdateBlockStatus(42, "confirmed", 1.0) {
		t.Error("UpdateBlockStatus on nonexistent block should return false (0 rows affected)")
	}
	if store.UpdateBlockOrphanCount(42, 3) {
		t.Error("UpdateBlockOrphanCount on nonexistent block should return false (0 rows affected)")
	}
	if store.UpdateBlockStabilityCount(42, 2, "tip_abc") {
		t.Error("UpdateBlockStabilityCount on nonexistent block should return false (0 rows affected)")
	}
}

// TestV6_RowsAffected_ExistingBlock verifies that updating a block that exists
// returns true, mirroring RowsAffected == 1.
func TestV6_RowsAffected_ExistingBlock(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	store.InsertBlock(&MockDBBlock{
		Height: 500,
		Hash:   "hash_existing",
		Status: "pending",
	})

	if !store.UpdateBlockStatus(500, "confirmed", 1.0) {
		t.Error("UpdateBlockStatus on existing block should return true (1 row affected)")
	}
	if !store.UpdateBlockOrphanCount(500, 1) {
		t.Error("UpdateBlockOrphanCount on existing block should return true (1 row affected)")
	}
	if !store.UpdateBlockStabilityCount(500, 1, "tip_xyz") {
		t.Error("UpdateBlockStabilityCount on existing block should return true (1 row affected)")
	}
}

// =============================================================================
// V26 — OnStatusGuardRejection Callback
// =============================================================================

// TestV26_Callback_Fires sets the OnStatusGuardRejection callback to a
// counting function, invokes it, and verifies the counter increments.
// NOTE: NOT parallel — mutates package-level OnStatusGuardRejection.
func TestV26_Callback_Fires(t *testing.T) {
	var count atomic.Int64

	// Restore nil callback after test.
	defer SetOnStatusGuardRejection(nil)

	SetOnStatusGuardRejection(func() {
		count.Add(1)
	})

	// Simulate three guard rejections.
	CallOnStatusGuardRejection()
	CallOnStatusGuardRejection()
	CallOnStatusGuardRejection()

	if count.Load() != 3 {
		t.Errorf("expected callback count 3, got %d", count.Load())
	}
}

// TestV26_Callback_NilSafe verifies that a nil OnStatusGuardRejection does
// not cause a panic when checked with the standard nil-guard pattern.
// NOTE: NOT parallel — mutates package-level OnStatusGuardRejection.
func TestV26_Callback_NilSafe(t *testing.T) {
	// Restore nil callback after test.
	defer SetOnStatusGuardRejection(nil)

	SetOnStatusGuardRejection(nil)

	// CallOnStatusGuardRejection handles nil internally.
	// Verify it does NOT panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil OnStatusGuardRejection caused panic: %v", r)
		}
	}()

	CallOnStatusGuardRejection()
}

// TestV26_Callback_ConcurrentSafe fires 100 goroutines that each call the
// callback once, and verifies the total count equals 100.
// NOTE: NOT parallel — mutates package-level OnStatusGuardRejection.
func TestV26_Callback_ConcurrentSafe(t *testing.T) {
	var count atomic.Int64

	// Restore nil callback after test.
	defer SetOnStatusGuardRejection(nil)

	SetOnStatusGuardRejection(func() {
		count.Add(1)
	})

	var wg sync.WaitGroup
	const numGoroutines = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			CallOnStatusGuardRejection()
		}()
	}

	wg.Wait()

	if count.Load() != int64(numGoroutines) {
		t.Errorf("expected callback count %d, got %d", numGoroutines, count.Load())
	}
}

// =============================================================================
// PoolID SQL Injection Prevention (validPoolID regex)
// =============================================================================

// TestPoolID_ValidPatterns verifies that well-formed pool IDs pass the
// validPoolID regex.
func TestPoolID_ValidPatterns(t *testing.T) {
	t.Parallel()

	validIDs := []struct {
		id   string
		desc string
	}{
		{"pool_1", "lowercase with underscore and digit"},
		{"my_pool", "lowercase with underscore"},
		{"_private", "starts with underscore"},
		{"ABC123", "uppercase with digits"},
		{"a", "single letter"},
		{"_", "single underscore"},
		{"Pool_v2_test", "mixed case with underscores and digits"},
	}

	for _, tc := range validIDs {
		tc := tc
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			if !validPoolID.MatchString(tc.id) {
				t.Errorf("validPoolID should accept %q (%s)", tc.id, tc.desc)
			}
		})
	}
}

// TestPoolID_InvalidPatterns verifies that malformed or dangerous pool IDs
// are rejected by the validPoolID regex.
func TestPoolID_InvalidPatterns(t *testing.T) {
	t.Parallel()

	invalidIDs := []struct {
		id   string
		desc string
	}{
		{"", "empty string"},
		{"pool-1", "contains hyphen"},
		{"123pool", "starts with digit"},
		{"pool;DROP TABLE", "SQL injection attempt"},
		{strings.Repeat("a", 64), "too long (64 chars)"},
		{"pool name", "contains space"},
		{"pool.id", "contains dot"},
		{"pool\ttab", "contains tab"},
		{"pool\nline", "contains newline"},
		{"$pool", "starts with dollar sign"},
	}

	for _, tc := range invalidIDs {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			if validPoolID.MatchString(tc.id) {
				t.Errorf("validPoolID should reject %q (%s)", tc.id, tc.desc)
			}
		})
	}
}

// TestPoolID_MaxLength verifies the boundary condition: exactly 63 characters
// (maximum allowed) passes, and 64 characters fails.
func TestPoolID_MaxLength(t *testing.T) {
	t.Parallel()

	// 63 chars: 1 leading letter + 62 trailing = total 63, should pass.
	maxValid := "a" + strings.Repeat("b", 62)
	if len(maxValid) != 63 {
		t.Fatalf("test setup error: expected 63 chars, got %d", len(maxValid))
	}
	if !validPoolID.MatchString(maxValid) {
		t.Errorf("validPoolID should accept a 63-character pool ID")
	}

	// 64 chars: 1 leading letter + 63 trailing = total 64, should fail.
	tooLong := "a" + strings.Repeat("b", 63)
	if len(tooLong) != 64 {
		t.Fatalf("test setup error: expected 64 chars, got %d", len(tooLong))
	}
	if validPoolID.MatchString(tooLong) {
		t.Errorf("validPoolID should reject a 64-character pool ID")
	}
}
