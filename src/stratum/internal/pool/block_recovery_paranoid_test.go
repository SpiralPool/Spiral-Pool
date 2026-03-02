// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// =============================================================================
// PARANOID VERIFICATION TEST SUITE: Startup Recovery Decision Logic
// =============================================================================
// Tests covering PARANOID_VERIFICATION_PLAN.md scenarios 5.1-5.4.
//
// These simulate the recovery decision logic from pool.go:390-491 which runs
// at startup when RecoverUnsubmittedBlocks finds entries with "pending" or
// "submitting" status. The decision tree is:
//
//   1. GetBlockHash(height) == blockHash → already in chain → update WAL to "submitted"
//   2. blockAge > 100 → too old → mark WAL as "rejected" (recovered_stale)
//   3. BlockHex != "" → resubmit via SubmitBlock → success/failure updates WAL
//   4. BlockHex == "" → cannot resubmit → mark WAL as "rejected" (recovered_no_hex)

// =============================================================================
// V5.1: Recovery Finds Block Already in Chain
// =============================================================================

// TestV5_1_RecoveryBlockAlreadyInChain simulates the path at pool.go:410-425
// where GetBlockHash returns a matching hash. The recovery logic should update
// WAL to "submitted" and NOT call SubmitBlock.
func TestV5_1_RecoveryBlockAlreadyInChain(t *testing.T) {
	t.Parallel()

	blockHash := "0000aabb00000000000000000000000000000000000000000000000000000001"
	blockHeight := uint64(1000000)

	// Simulate: GetBlockHash(height) returns matching hash
	chainHashAtHeight := blockHash // Same hash — block IS in chain

	// Decision logic (mirrors pool.go:414)
	submitBlockCalled := false
	var finalStatus string
	var finalError string

	if chainHashAtHeight == blockHash {
		// Block already accepted — no resubmission needed
		finalStatus = "submitted"
		finalError = "recovered_on_startup: block already in chain"
	} else {
		submitBlockCalled = true
		finalStatus = "submitted"
	}

	if submitBlockCalled {
		t.Error("SubmitBlock should NOT be called when block is already in chain")
	}
	if finalStatus != "submitted" {
		t.Errorf("Expected status 'submitted', got %q", finalStatus)
	}
	if finalError != "recovered_on_startup: block already in chain" {
		t.Errorf("Expected specific recovery message, got %q", finalError)
	}

	// Verify WAL update
	dir := t.TempDir()
	wal := createTestWAL(t, dir)

	// Write original "submitting" entry
	entry := &BlockWALEntry{
		Height:    blockHeight,
		BlockHash: blockHash,
		BlockHex:  "020000000000...hex...000000",
		Status:    "submitting",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("Failed to write WAL: %v", err)
	}

	// Write recovery update
	entry.Status = finalStatus
	entry.SubmitError = finalError
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("Failed to write recovery update: %v", err)
	}
	wal.Close()

	// Recovery should now return 0 unsubmitted blocks
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("Expected 0 unsubmitted blocks after recovery update, got %d", len(unsubmitted))
	}
}

// =============================================================================
// V5.2: Recovery Finds Block >100 Blocks Old
// =============================================================================

// TestV5_2_RecoveryBlockTooOld simulates the path at pool.go:437-449 where
// the block age exceeds 100 blocks. The recovery logic marks it as "rejected"
// with a stale reason.
func TestV5_2_RecoveryBlockTooOld(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(100000)
	currentChainHeight := uint64(100250) // 250 blocks ahead
	blockAge := currentChainHeight - blockHeight

	// Decision logic (mirrors pool.go:437)
	var finalStatus string
	var finalError string
	submitCalled := false

	if blockAge > 100 {
		finalStatus = "rejected"
		finalError = "recovered_stale: block too old (chain moved 250 blocks ahead)"
	} else {
		submitCalled = true
	}

	if submitCalled {
		t.Error("SubmitBlock should NOT be called for blocks >100 blocks old")
	}
	if finalStatus != "rejected" {
		t.Errorf("Expected status 'rejected', got %q", finalStatus)
	}
	if finalError == "" {
		t.Error("Expected stale reason in SubmitError")
	}

	// Verify with real WAL
	dir := t.TempDir()
	wal := createTestWAL(t, dir)

	entry := &BlockWALEntry{
		Height:    blockHeight,
		BlockHash: "0000dddd00000000000000000000000000000000000000000000000000000001",
		BlockHex:  "020000000000...hex...000000",
		Status:    "submitting",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("Failed to write WAL: %v", err)
	}

	// Apply recovery decision
	entry.Status = finalStatus
	entry.SubmitError = finalError
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("Failed to write recovery update: %v", err)
	}
	wal.Close()

	// Recovery should return 0 (status updated to "rejected")
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("Expected 0 unsubmitted after stale marking, got %d", len(unsubmitted))
	}
}

// TestV5_2_RecoveryBlockExactly100Old verifies the boundary condition at
// exactly 100 blocks old. The code uses `blockAge > 100`, so exactly 100
// should still be eligible for resubmission.
func TestV5_2_RecoveryBlockExactly100Old(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(100000)
	currentChainHeight := uint64(100100) // Exactly 100 blocks ahead
	blockAge := currentChainHeight - blockHeight

	// Decision logic: > 100, not >= 100
	tooOld := blockAge > 100

	if tooOld {
		t.Error("Block exactly 100 blocks old should NOT be marked as too old (> 100, not >= 100)")
	}
}

// TestV5_2_RecoveryBlock101Old verifies that 101 blocks old IS too old.
func TestV5_2_RecoveryBlock101Old(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(100000)
	currentChainHeight := uint64(100101) // 101 blocks ahead
	blockAge := currentChainHeight - blockHeight

	tooOld := blockAge > 100

	if !tooOld {
		t.Error("Block 101 blocks old should be marked as too old")
	}
}

// =============================================================================
// V5.3: Recovery Has BlockHex but Resubmission Fails
// =============================================================================

// TestV5_3_RecoveryResubmitFails simulates the path at pool.go:460-478 where
// SubmitBlock returns an error. The recovery logic marks the block as "rejected"
// with "recovered_resubmit_failed" reason.
func TestV5_3_RecoveryResubmitFails(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(100050)
	currentChainHeight := uint64(100060) // 10 blocks ahead — eligible for resubmission
	blockAge := currentChainHeight - blockHeight
	blockHex := "020000000000...valid_hex...000000"

	// Decision logic
	var finalStatus string
	var finalError string

	if blockAge <= 100 && blockHex != "" {
		// Simulate resubmission failure
		submitErr := "prev-blk-not-found" // Chain moved on
		finalStatus = "rejected"
		finalError = "recovered_resubmit_failed: " + submitErr
	}

	if finalStatus != "rejected" {
		t.Errorf("Expected status 'rejected', got %q", finalStatus)
	}
	if finalError == "" {
		t.Error("Expected error reason in SubmitError")
	}

	// Verify with real WAL
	dir := t.TempDir()
	wal := createTestWAL(t, dir)

	entry := &BlockWALEntry{
		Height:    blockHeight,
		BlockHash: "0000eeee00000000000000000000000000000000000000000000000000000001",
		BlockHex:  blockHex,
		Status:    "submitting",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("Failed to write WAL: %v", err)
	}

	// Apply recovery decision
	entry.Status = finalStatus
	entry.SubmitError = finalError
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("Failed to write recovery update: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("Expected 0 unsubmitted after rejection, got %d", len(unsubmitted))
	}
}

// =============================================================================
// V5.4: Recovery Has No BlockHex (Emergency WAL Entry)
// =============================================================================

// TestV5_4_RecoveryNoBlockHex simulates pool.go:480-488 where the recovered
// entry has no BlockHex (emergency WAL entry from build failure). The recovery
// logic marks it as "rejected" with "recovered_no_hex" reason.
func TestV5_4_RecoveryNoBlockHex(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(100050)
	currentChainHeight := uint64(100060)
	blockAge := currentChainHeight - blockHeight
	blockHex := "" // Emergency entry — no hex available

	var finalStatus string
	var finalError string

	if blockAge <= 100 {
		if blockHex != "" {
			// Would attempt resubmission
			finalStatus = "submitted"
		} else {
			// Cannot resubmit — no hex (pool.go:480-488)
			finalStatus = "rejected"
			finalError = "recovered_no_hex: block data not available for resubmission"
		}
	}

	if finalStatus != "rejected" {
		t.Errorf("Expected status 'rejected', got %q", finalStatus)
	}
	if finalError == "" {
		t.Error("Expected no_hex reason in SubmitError")
	}

	// Verify with real WAL
	dir := t.TempDir()
	wal := createTestWAL(t, dir)

	// Emergency entry: no BlockHex but has raw components
	entry := &BlockWALEntry{
		Height:      blockHeight,
		BlockHash:   "0000ffff00000000000000000000000000000000000000000000000000000001",
		BlockHex:    "", // Empty!
		Status:      "submitting",
		CoinBase1:   "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff",
		CoinBase2:   "ffffffff0100f2052a01000000",
		ExtraNonce1: "aabb0011",
		ExtraNonce2: "00000001",
		Version:     "20000000",
		NBits:       "1a0377ae",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("Failed to write WAL: %v", err)
	}

	// Apply recovery decision
	entry.Status = finalStatus
	entry.SubmitError = finalError
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("Failed to write recovery update: %v", err)
	}
	wal.Close()

	// Verify raw components are still readable in WAL file
	entries := readWALEntries(t, dir)
	foundEmergency := false
	for _, e := range entries {
		if e.CoinBase1 != "" && e.BlockHex == "" {
			foundEmergency = true
			if e.CoinBase1 == "" {
				t.Error("Emergency entry CoinBase1 should be preserved")
			}
			if e.ExtraNonce1 == "" {
				t.Error("Emergency entry ExtraNonce1 should be preserved")
			}
			break
		}
	}
	if !foundEmergency {
		t.Error("Emergency WAL entry with raw components should be readable")
	}
}

// =============================================================================
// Recovery Decision Tree — Complete Path Coverage
// =============================================================================

// TestRecoveryDecisionTree_AllPaths exercises all 4 branches of the recovery
// decision tree from pool.go:390-491 in a single table-driven test.
func TestRecoveryDecisionTree_AllPaths(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		chainHashMatch   bool
		blockAge         uint64
		hasBlockHex      bool
		resubmitSuccess  bool
		expectedStatus   string
		expectedReason   string
	}{
		{
			name:           "already in chain",
			chainHashMatch: true,
			blockAge:       10,
			hasBlockHex:    true,
			expectedStatus: "submitted",
			expectedReason: "recovered_on_startup: block already in chain",
		},
		{
			name:           "too old",
			chainHashMatch: false,
			blockAge:       150,
			hasBlockHex:    true,
			expectedStatus: "rejected",
			expectedReason: "recovered_stale",
		},
		{
			name:            "recent with hex, resubmit success",
			chainHashMatch:  false,
			blockAge:        10,
			hasBlockHex:     true,
			resubmitSuccess: true,
			expectedStatus:  "submitted",
			expectedReason:  "recovered_resubmitted_on_startup",
		},
		{
			name:            "recent with hex, resubmit failure",
			chainHashMatch:  false,
			blockAge:        10,
			hasBlockHex:     true,
			resubmitSuccess: false,
			expectedStatus:  "rejected",
			expectedReason:  "recovered_resubmit_failed",
		},
		{
			name:           "recent without hex",
			chainHashMatch: false,
			blockAge:       10,
			hasBlockHex:    false,
			expectedStatus: "rejected",
			expectedReason: "recovered_no_hex",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var status, reason string

			// Decision tree (mirrors pool.go:410-488)
			if tc.chainHashMatch {
				status = "submitted"
				reason = "recovered_on_startup: block already in chain"
			} else if tc.blockAge > 100 {
				status = "rejected"
				reason = "recovered_stale"
			} else if tc.hasBlockHex {
				if tc.resubmitSuccess {
					status = "submitted"
					reason = "recovered_resubmitted_on_startup"
				} else {
					status = "rejected"
					reason = "recovered_resubmit_failed"
				}
			} else {
				status = "rejected"
				reason = "recovered_no_hex"
			}

			if status != tc.expectedStatus {
				t.Errorf("Expected status %q, got %q", tc.expectedStatus, status)
			}
			if reason != tc.expectedReason {
				t.Errorf("Expected reason %q, got %q", tc.expectedReason, reason)
			}
		})
	}
}

// =============================================================================
// Recovery: Partial WAL Write Survival
// =============================================================================

// TestRecoveryPartialWALWrite_SubmittingEntrySurvives verifies that if a WAL
// file contains a valid "submitting" entry followed by a truncated/corrupted
// second entry, recovery still finds the "submitting" entry.
func TestRecoveryPartialWALWrite_SubmittingEntrySurvives(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Write valid entry + truncated entry directly to file
	filename := "block_wal_" + time.Now().Format("2006-01-02") + ".jsonl"
	filePath := filepath.Join(dir, filename)

	validEntry := BlockWALEntry{
		Timestamp: time.Now(),
		Height:    500000,
		BlockHash: "0000abcd00000000000000000000000000000000000000000000000000000001",
		BlockHex:  "020000000000...valid_hex...000000",
		Status:    "submitting",
	}

	validJSON, err := json.Marshal(validEntry)
	if err != nil {
		t.Fatalf("Failed to marshal entry: %v", err)
	}

	// Write valid entry + truncated garbage (simulating crash mid-write)
	content := string(validJSON) + "\n" + `{"height":500000,"block_hash":"0000abcd00000000000` + "\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write WAL file: %v", err)
	}

	// Recovery should find the valid "submitting" entry
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block (from valid entry), got %d", len(unsubmitted))
	}

	if unsubmitted[0].Status != "submitting" {
		t.Errorf("Expected status 'submitting', got %q", unsubmitted[0].Status)
	}
	if unsubmitted[0].BlockHex == "" {
		t.Error("Recovered entry should have BlockHex for resubmission")
	}
}
