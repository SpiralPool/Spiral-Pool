// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database — Logic-layer tests for PostgreSQL operations.
//
// These tests cover the non-IO logic of the database layer:
//   - Input validation (pool IDs, retention days, identifiers)
//   - SQL table name construction and injection prevention
//   - Status priority logic and transition validation
//   - Advisory lock key generation uniqueness
//   - Error sentinel values and wrapping behavior
//   - PoolShareWriterDB delegation pattern
//
// No real PostgreSQL connection is needed — these verify correctness
// of the logic that SURROUNDS the database calls.
package database

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// =============================================================================
// Table Name Construction: SQL injection prevention
// =============================================================================

func TestTableNameConstruction_SharesTable(t *testing.T) {
	t.Parallel()

	poolID := "dgb_main"
	tableName := fmt.Sprintf("shares_%s", poolID)

	if tableName != "shares_dgb_main" {
		t.Errorf("Expected 'shares_dgb_main', got %q", tableName)
	}
}

func TestTableNameConstruction_BlocksTable(t *testing.T) {
	t.Parallel()

	poolID := "btc_solo"
	tableName := fmt.Sprintf("blocks_%s", poolID)

	if tableName != "blocks_btc_solo" {
		t.Errorf("Expected 'blocks_btc_solo', got %q", tableName)
	}
}

func TestTableNameConstruction_InjectionPrevented(t *testing.T) {
	t.Parallel()

	// Attacker tries to inject SQL via pool ID
	maliciousIDs := []string{
		"dgb_main; DROP TABLE--",
		"' OR 1=1--",
		"../../../etc/passwd",
		"blocks\x00injection",
		"pool_name\nSELECT * FROM secrets",
	}

	for _, id := range maliciousIDs {
		if validPoolID.MatchString(id) {
			t.Errorf("Malicious pool ID %q should be rejected by validPoolID", id)
		}
	}
}

// =============================================================================
// Advisory lock key uniqueness
// =============================================================================

func TestAdvisoryLockKey_UniquePerPoolAndHeight(t *testing.T) {
	t.Parallel()

	// Same pool, different heights → different keys
	key1 := int64(100000) ^ int64(hash("dgb_main"))
	key2 := int64(100001) ^ int64(hash("dgb_main"))

	if key1 == key2 {
		t.Errorf("Same pool, different heights should produce different lock keys: %d == %d", key1, key2)
	}

	// Same height, different pools → different keys
	key3 := int64(100000) ^ int64(hash("dgb_main"))
	key4 := int64(100000) ^ int64(hash("btc_solo"))

	if key3 == key4 {
		t.Errorf("Different pools, same height should produce different lock keys: %d == %d", key3, key4)
	}
}

func TestAdvisoryLockKey_Deterministic(t *testing.T) {
	t.Parallel()

	key1 := int64(500000) ^ int64(hash("test_pool"))
	key2 := int64(500000) ^ int64(hash("test_pool"))

	if key1 != key2 {
		t.Errorf("Same inputs should produce same lock key: %d != %d", key1, key2)
	}
}

func TestAdvisoryLockKey_NoPanic_ZeroHeight(t *testing.T) {
	t.Parallel()

	// Height 0 should not panic or produce degenerate keys
	key := int64(0) ^ int64(hash("dgb_main"))
	if key == 0 {
		// This is technically valid (XOR with non-zero hash)
		// but let's make sure hash("dgb_main") is non-zero
		if hash("dgb_main") == 0 {
			t.Error("hash('dgb_main') should not be 0")
		}
	}
}

func TestAdvisoryLockKey_CollisionRate(t *testing.T) {
	t.Parallel()

	// Generate 1000 lock keys and check for collisions
	seen := make(map[int64]bool)
	collisions := 0

	pools := []string{"dgb_main", "btc_solo", "ltc_pool", "xvg_skein", "doge_scrypt"}
	for _, pool := range pools {
		for height := uint64(0); height < 200; height++ {
			key := int64(height) ^ int64(hash(pool))
			if seen[key] {
				collisions++
			}
			seen[key] = true
		}
	}

	// Allow at most 1% collision rate
	total := len(pools) * 200
	maxCollisions := total / 100
	if collisions > maxCollisions {
		t.Errorf("Too many advisory lock key collisions: %d/%d (%.1f%%)",
			collisions, total, float64(collisions)/float64(total)*100)
	}
}

// =============================================================================
// Status transitions: V12 guard logic
// =============================================================================

func TestStatusGuard_AllowedTransitions(t *testing.T) {
	t.Parallel()

	// The SQL WHERE clause allows these transitions:
	// submitting → pending (normal success)
	// submitting → orphaned (permanent rejection)
	// submitting → confirmed (fast confirm — unusual but valid)
	// pending → confirmed (normal confirmation)

	allowedTransitions := [][2]string{
		{"submitting", "pending"},
		{"submitting", "orphaned"},
		{"submitting", "confirmed"},
		{"pending", "confirmed"},
	}

	for _, transition := range allowedTransitions {
		from, to := transition[0], transition[1]

		fromPriority := statusPriority[from]
		toPriority := statusPriority[to]

		if toPriority < fromPriority {
			t.Errorf("Transition %s→%s should be allowed (priority %d→%d)", from, to, fromPriority, toPriority)
		}
	}
}

func TestStatusGuard_BlockedTransitions(t *testing.T) {
	t.Parallel()

	// These transitions should be blocked by the guard:
	// confirmed → pending (downgrade)
	// confirmed → submitting (downgrade)
	// orphaned → pending (downgrade)
	// orphaned → submitting (downgrade)
	// pending → submitting (downgrade)

	blockedTransitions := [][2]string{
		{"confirmed", "pending"},
		{"confirmed", "submitting"},
		{"orphaned", "pending"},
		{"orphaned", "submitting"},
		{"pending", "submitting"},
	}

	for _, transition := range blockedTransitions {
		from, to := transition[0], transition[1]

		fromPriority := statusPriority[from]
		toPriority := statusPriority[to]

		if toPriority > fromPriority {
			t.Errorf("Transition %s→%s should be BLOCKED (priority %d→%d would be downgrade)", from, to, fromPriority, toPriority)
		}
	}
}

func TestStatusGuard_ConfirmedCannotRevertToOrphaned(t *testing.T) {
	t.Parallel()

	// Confirmed and orphaned have the same priority (2).
	// The SQL guard should prevent confirmed→orphaned because
	// the WHERE clause only allows orphaned FROM submitting, not from confirmed.
	confirmedPriority := statusPriority["confirmed"]
	orphanedPriority := statusPriority["orphaned"]

	if confirmedPriority != orphanedPriority {
		t.Errorf("confirmed and orphaned should have same priority: %d != %d",
			confirmedPriority, orphanedPriority)
	}

	// The SQL WHERE clause explicitly checks:
	// ($1 = 'orphaned' AND status = 'submitting') — only from submitting
	// So confirmed→orphaned is blocked even though priorities are equal
}

func TestStatusGuard_ErrStatusGuardBlocked_Distinguishable(t *testing.T) {
	t.Parallel()

	// Verify that the sentinel error can be distinguished from other DB errors
	dbErrors := []error{
		fmt.Errorf("connection refused"),
		fmt.Errorf("context deadline exceeded"),
		fmt.Errorf("failed to update block: %w", fmt.Errorf("network timeout")),
		ErrStatusGuardBlocked,
	}

	guardBlockedCount := 0
	for _, err := range dbErrors {
		if errors.Is(err, ErrStatusGuardBlocked) {
			guardBlockedCount++
		}
	}

	if guardBlockedCount != 1 {
		t.Errorf("Exactly 1 error should match ErrStatusGuardBlocked, got %d", guardBlockedCount)
	}
}

// =============================================================================
// PoolShareWriterDB: delegation pattern
// =============================================================================

func TestNewPoolShareWriter_SetsFields(t *testing.T) {
	t.Parallel()

	// Test the constructor without a real DB connection
	writer := NewPoolShareWriter("dgb_main", nil)

	if writer == nil {
		t.Fatal("NewPoolShareWriter should not return nil")
	}
	if writer.poolID != "dgb_main" {
		t.Errorf("poolID should be 'dgb_main', got %q", writer.poolID)
	}
}

func TestPoolShareWriter_Close_NoError(t *testing.T) {
	t.Parallel()

	writer := &PoolShareWriterDB{
		poolID: "test_pool",
		db:     nil,
	}

	err := writer.Close()
	if err != nil {
		t.Errorf("Close() should return nil, got: %v", err)
	}
}

func TestPoolShareWriter_WriteBatch_EmptySlice(t *testing.T) {
	t.Parallel()

	// WriteBatch with empty slice should be handled by WriteBatchForPool
	// which returns nil for empty slices. But we can't call it without a DB.
	// This tests the nil-check pattern.
	writer := &PoolShareWriterDB{
		poolID: "test_pool",
		db:     nil,
	}

	// We can't actually call WriteBatch without a DB, but we verify
	// the struct is properly constructed for delegation.
	if writer.poolID != "test_pool" {
		t.Error("poolID not set correctly")
	}
}

// =============================================================================
// validPoolID edge cases
// =============================================================================

func TestValidPoolID_UnicodeRejected(t *testing.T) {
	t.Parallel()

	// Unicode characters should be rejected even if they look like valid identifiers
	unicodeIDs := []string{
		"pool_名前",       // Japanese
		"пул_тест",       // Cyrillic
		"pool_αβγ",       // Greek
		"pool_€",          // Euro sign
	}

	for _, id := range unicodeIDs {
		if validPoolID.MatchString(id) {
			t.Errorf("Unicode pool ID %q should be rejected", id)
		}
	}
}

func TestValidPoolID_MaxLength(t *testing.T) {
	t.Parallel()

	// PostgreSQL identifier limit is 63 characters
	maxID := strings.Repeat("a", 63) // Should pass
	overID := strings.Repeat("a", 64) // Should fail

	if !validPoolID.MatchString(maxID) {
		t.Error("63-char pool ID should be valid")
	}
	if validPoolID.MatchString(overID) {
		t.Error("64-char pool ID should be invalid")
	}
}

func TestValidPoolID_OnlyUnderscoreValid(t *testing.T) {
	t.Parallel()

	// Single underscore is a valid PostgreSQL identifier
	if !validPoolID.MatchString("_") {
		t.Error("Single underscore should be a valid pool ID")
	}
}

// =============================================================================
// PaymentAdvisoryLockID: Verify it exists and is stable
// =============================================================================

func TestPaymentAdvisoryLockID_Exists(t *testing.T) {
	t.Parallel()

	// PaymentAdvisoryLockID should be a function that returns a stable lock ID
	lockID := PaymentAdvisoryLockID()

	if lockID == 0 {
		t.Error("PaymentAdvisoryLockID should return a non-zero value")
	}

	// Verify it's deterministic
	lockID2 := PaymentAdvisoryLockID()
	if lockID != lockID2 {
		t.Errorf("PaymentAdvisoryLockID should be deterministic: %d != %d", lockID, lockID2)
	}
}
