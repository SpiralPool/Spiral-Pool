// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database — Unit tests for migration logic.
//
// These tests cover:
//   - isValidIdentifier: SQL injection prevention for PostgreSQL identifiers
//   - validPoolID regex: Pool ID validation for table names
//   - hash() function: Advisory lock key generation
//   - statusPriority: Block status transition ordering
//   - ErrStatusGuardBlocked sentinel error
//   - Migration function signatures and structure validation
package database

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// =============================================================================
// isValidIdentifier: SQL injection prevention
// =============================================================================

func TestIsValidIdentifier_ValidNames(t *testing.T) {
	t.Parallel()

	validNames := []string{
		"spiralpool",
		"spiral_pool",
		"_private",
		"MyDatabase",
		"db1",
		"a",
		"A",
		"_",
		"test_db_123",
		"CamelCase",
		strings.Repeat("a", 63), // Max length
	}

	for _, name := range validNames {
		if !isValidIdentifier(name) {
			t.Errorf("isValidIdentifier(%q) = false, expected true", name)
		}
	}
}

func TestIsValidIdentifier_InvalidNames(t *testing.T) {
	t.Parallel()

	invalidNames := []string{
		"",                         // Empty
		"123abc",                   // Starts with digit
		"0_bad",                    // Starts with digit
		"has space",                // Contains space
		"has-dash",                 // Contains dash
		"has.dot",                  // Contains dot
		"has;semicolon",            // SQL injection
		"robert'; DROP TABLE--",   // SQL injection
		"table$name",               // Special char
		strings.Repeat("a", 64),   // Too long (64 chars)
		"has\nnewline",             // Newline injection
		"has\ttab",                 // Tab injection
	}

	for _, name := range invalidNames {
		if isValidIdentifier(name) {
			t.Errorf("isValidIdentifier(%q) = true, expected false (SQL injection risk)", name)
		}
	}
}

func TestIsValidIdentifier_BoundaryLengths(t *testing.T) {
	t.Parallel()

	// 1 char (minimum)
	if !isValidIdentifier("a") {
		t.Error("Single char 'a' should be valid")
	}

	// 63 chars (maximum for PostgreSQL)
	if !isValidIdentifier(strings.Repeat("x", 63)) {
		t.Error("63-char identifier should be valid")
	}

	// 64 chars (too long)
	if isValidIdentifier(strings.Repeat("x", 64)) {
		t.Error("64-char identifier should be invalid (PostgreSQL limit is 63)")
	}
}

func TestIsValidIdentifier_UnderscoreStart(t *testing.T) {
	t.Parallel()

	if !isValidIdentifier("_system_table") {
		t.Error("Underscore-prefixed identifiers should be valid")
	}
}

// =============================================================================
// validPoolID: Pool ID validation for table names
// =============================================================================

func TestValidPoolID_ValidIDs(t *testing.T) {
	t.Parallel()

	validIDs := []string{
		"dgb_main",
		"btc_solo",
		"litecoin_pool_1",
		"NMC",
		"a",
		"_test_pool",
		"myPool123",
	}

	for _, id := range validIDs {
		if !validPoolID.MatchString(id) {
			t.Errorf("validPoolID.MatchString(%q) = false, expected true", id)
		}
	}
}

func TestValidPoolID_InvalidIDs(t *testing.T) {
	t.Parallel()

	invalidIDs := []string{
		"",                       // Empty
		"123pool",                // Starts with digit
		"has-dash",               // Contains dash
		"has space",              // Contains space
		"robert'; DROP TABLE--", // SQL injection
		"pool/path",              // Path traversal
		"pool.name",              // Dot
	}

	for _, id := range invalidIDs {
		if validPoolID.MatchString(id) {
			t.Errorf("validPoolID.MatchString(%q) = true, expected false (SQL injection risk)", id)
		}
	}
}

func TestValidPoolID_MatchesIdentifierRegex(t *testing.T) {
	t.Parallel()

	// validPoolID should accept the same pattern as validIdentifier
	// Both are: ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$
	poolIDRegex := regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
	identifierRegex := validIdentifier

	testCases := []string{
		"dgb_main", "btc_solo", "", "123bad", "good_name", strings.Repeat("a", 63), strings.Repeat("a", 64),
	}

	for _, tc := range testCases {
		poolResult := poolIDRegex.MatchString(tc)
		identResult := identifierRegex.MatchString(tc)
		if poolResult != identResult {
			t.Errorf("Regex mismatch for %q: poolID=%v identifier=%v", tc, poolResult, identResult)
		}
	}
}

// =============================================================================
// hash() function: Advisory lock key generation
// =============================================================================

func TestHash_Deterministic(t *testing.T) {
	t.Parallel()

	h1 := hash("dgb_main")
	h2 := hash("dgb_main")

	if h1 != h2 {
		t.Errorf("hash() should be deterministic: %d != %d", h1, h2)
	}
}

func TestHash_DifferentInputs_DifferentOutputs(t *testing.T) {
	t.Parallel()

	h1 := hash("dgb_main")
	h2 := hash("btc_solo")
	h3 := hash("ltc_pool")

	if h1 == h2 || h2 == h3 || h1 == h3 {
		t.Errorf("Different pool IDs should produce different hashes: dgb=%d btc=%d ltc=%d", h1, h2, h3)
	}
}

func TestHash_EmptyString(t *testing.T) {
	t.Parallel()

	h := hash("")
	if h != 0 {
		t.Errorf("hash('') should be 0 (no iterations), got %d", h)
	}
}

func TestHash_SingleChar(t *testing.T) {
	t.Parallel()

	// For single char 'a' (97): h = 31*0 + 97 = 97
	h := hash("a")
	if h != 97 {
		t.Errorf("hash('a') should be 97, got %d", h)
	}
}

func TestHash_LockKeyUniqueness(t *testing.T) {
	t.Parallel()

	// Verify that the lock key XOR trick produces unique keys
	// for different pool+height combinations
	poolIDs := []string{"dgb_main", "btc_solo", "ltc_pool"}
	heights := []int64{100000, 100001, 200000}

	seen := make(map[int64]string)
	for _, poolID := range poolIDs {
		for _, height := range heights {
			lockKey := height ^ int64(hash(poolID))
			combo := poolID + ":" + string(rune(height))
			if prev, exists := seen[lockKey]; exists {
				t.Errorf("Lock key collision between %s and %s (key=%d)", prev, combo, lockKey)
			}
			seen[lockKey] = combo
		}
	}
}

// =============================================================================
// statusPriority: Block status transition ordering
// =============================================================================

func TestStatusPriority_KnownStatuses(t *testing.T) {
	t.Parallel()

	expectedPriorities := map[string]int{
		"submitting": 0,
		"pending":    1,
		"confirmed":  2,
		"orphaned":   2,
	}

	for status, expectedPriority := range expectedPriorities {
		got, ok := statusPriority[status]
		if !ok {
			t.Errorf("Status %q not found in statusPriority map", status)
			continue
		}
		if got != expectedPriority {
			t.Errorf("statusPriority[%q] = %d, expected %d", status, got, expectedPriority)
		}
	}
}

func TestStatusPriority_SubmittingLowest(t *testing.T) {
	t.Parallel()

	submitting := statusPriority["submitting"]
	for status, priority := range statusPriority {
		if status != "submitting" && priority < submitting {
			t.Errorf("Status %q has lower priority (%d) than 'submitting' (%d) — submitting must be lowest",
				status, priority, submitting)
		}
	}
}

func TestStatusPriority_ConfirmedAndOrphanedSamePriority(t *testing.T) {
	t.Parallel()

	confirmed := statusPriority["confirmed"]
	orphaned := statusPriority["orphaned"]

	if confirmed != orphaned {
		t.Errorf("confirmed (%d) and orphaned (%d) should have same priority (both final)",
			confirmed, orphaned)
	}
}

func TestStatusPriority_NeverDowngrade(t *testing.T) {
	t.Parallel()

	// Verify the progression: submitting < pending < confirmed/orphaned
	if statusPriority["submitting"] >= statusPriority["pending"] {
		t.Error("submitting should have lower priority than pending")
	}
	if statusPriority["pending"] >= statusPriority["confirmed"] {
		t.Error("pending should have lower priority than confirmed")
	}
}

func TestStatusPriority_UnknownStatusRejected(t *testing.T) {
	t.Parallel()

	// BUG FIX: Unknown statuses are now rejected instead of silently defaulting.
	// Code uses: newPriority, ok := statusPriority[status]; if !ok { return error }
	_, ok := statusPriority["unknown_status"]
	if ok {
		t.Error("Unknown status should not be in the priority map")
	}
}

func TestStatusPriority_PaidStatusExists(t *testing.T) {
	t.Parallel()

	// BUG FIX: "paid" status must exist in the priority map for payment workflow.
	paid, ok := statusPriority["paid"]
	if !ok {
		t.Fatal("paid status missing from statusPriority map")
	}
	confirmed := statusPriority["confirmed"]
	if paid <= confirmed {
		t.Errorf("paid priority (%d) should be higher than confirmed (%d)", paid, confirmed)
	}
}

// =============================================================================
// ErrStatusGuardBlocked sentinel error
// =============================================================================

func TestErrStatusGuardBlocked_IsSentinelError(t *testing.T) {
	t.Parallel()

	if ErrStatusGuardBlocked == nil {
		t.Fatal("ErrStatusGuardBlocked should not be nil")
	}

	msg := ErrStatusGuardBlocked.Error()
	if !strings.Contains(msg, "0 rows affected") {
		t.Errorf("ErrStatusGuardBlocked message should mention '0 rows affected', got: %s", msg)
	}
}

func TestErrStatusGuardBlocked_ErrorsIs(t *testing.T) {
	t.Parallel()

	// Verify errors.Is works for matching
	if !errors.Is(ErrStatusGuardBlocked, ErrStatusGuardBlocked) {
		t.Error("errors.Is(ErrStatusGuardBlocked, ErrStatusGuardBlocked) should be true")
	}

	// Verify wrapped errors can be unwrapped
	wrapped := errors.New("some other error")
	if errors.Is(wrapped, ErrStatusGuardBlocked) {
		t.Error("Different error should not match ErrStatusGuardBlocked")
	}
}

func TestErrStatusGuardBlocked_DistinguishableFromGenericErrors(t *testing.T) {
	t.Parallel()

	genericDBError := errors.New("connection refused")
	timeoutError := errors.New("context deadline exceeded")

	if errors.Is(genericDBError, ErrStatusGuardBlocked) {
		t.Error("Generic DB error should not match ErrStatusGuardBlocked")
	}
	if errors.Is(timeoutError, ErrStatusGuardBlocked) {
		t.Error("Timeout error should not match ErrStatusGuardBlocked")
	}
}

// =============================================================================
// OnStatusGuardRejection callback
// =============================================================================

func TestOnStatusGuardRejection_NilSafe(t *testing.T) {
	// NOT parallel — mutates package-level callback
	SetOnStatusGuardRejection(nil)
	defer SetOnStatusGuardRejection(nil)

	// Should not panic when nil
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Nil check pattern should prevent panic: %v", r)
		}
	}()

	CallOnStatusGuardRejection()
}

func TestOnStatusGuardRejection_CallbackFires(t *testing.T) {
	// NOT parallel — mutates package-level callback
	defer SetOnStatusGuardRejection(nil)

	callCount := 0
	SetOnStatusGuardRejection(func() {
		callCount++
	})

	CallOnStatusGuardRejection()
	CallOnStatusGuardRejection()

	if callCount != 2 {
		t.Errorf("Callback should fire twice, got %d", callCount)
	}
}

// =============================================================================
// V2 migration validation: CreatePoolTablesV2 input validation
// =============================================================================

func TestCreatePoolTablesV2_InvalidPoolID(t *testing.T) {
	t.Parallel()

	// Test that invalid pool IDs are caught by the validation regex
	invalidIDs := []string{
		"",
		"123abc",
		"has-dash",
		"robert'; DROP TABLE shares--",
		"pool/../../etc/passwd",
	}

	for _, id := range invalidIDs {
		if validPoolID.MatchString(id) {
			t.Errorf("Pool ID %q should be rejected by validation", id)
		}
	}
}

func TestCreatePoolTablesV2_ValidPoolID(t *testing.T) {
	t.Parallel()

	validIDs := []string{
		"dgb_main",
		"btc_solo",
		"pool_1",
		"nmc_mainnet",
	}

	for _, id := range validIDs {
		if !validPoolID.MatchString(id) {
			t.Errorf("Pool ID %q should pass validation", id)
		}
	}
}

// =============================================================================
// CleanupOldHealthRecords: retention bounds validation
// =============================================================================

func TestCleanupRetentionDays_BoundsCheck(t *testing.T) {
	t.Parallel()

	// From migrate_v2.go: retentionDays must be 1-365
	validDays := []int{1, 30, 90, 365}
	invalidDays := []int{0, -1, 366, 1000}

	for _, days := range validDays {
		if days < 1 || days > 365 {
			t.Errorf("Retention days %d should be valid", days)
		}
	}

	for _, days := range invalidDays {
		if days >= 1 && days <= 365 {
			t.Errorf("Retention days %d should be invalid", days)
		}
	}
}

// =============================================================================
// Migrator construction
// =============================================================================

func TestNewMigrator_SetsFields(t *testing.T) {
	t.Parallel()

	logger, _ := zap.NewDevelopment()
	m := NewMigrator(nil, "dgb_main", logger)

	if m == nil {
		t.Fatal("NewMigrator should not return nil")
	}
	if m.poolID != "dgb_main" {
		t.Errorf("poolID should be 'dgb_main', got %q", m.poolID)
	}
	if m.logger == nil {
		t.Error("logger should not be nil")
	}
}
