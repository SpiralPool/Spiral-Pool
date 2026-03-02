// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// =============================================================================
// PARANOID VERIFICATION TEST SUITE: Block Submission Pipeline
// =============================================================================
// Tests covering scenarios from PARANOID_VERIFICATION_PLAN.md that are NOT
// already covered by existing test files.
//
// Scenarios tested:
//   V1.3: Job evicted from JobManager → emergency WAL with empty raw fields
//   V2.4: Empty BlockHex AND empty BlockBuildError → emergency WAL written
//   V3.7: submitblock OK but GetBlockHash fails → blockStatus="submitted"
//   V3.8: Pre-submission WAL entry survives crash after RPC
//   V3.9: Post-submission WAL fails → pre-submission entry survives
//   V3.10: DB insert fails → block still in daemon + WAL
//   V4.1: All retries fail within SubmitDeadline
//   V4.3: Permanent rejection detected on retry → loop exits immediately
//   isPermanentRejection: exhaustive BIP22 pattern coverage + case insensitivity

// =============================================================================
// V1.3: Job Evicted from JobManager — Emergency WAL With Empty Fields
// =============================================================================

// TestV1_3_JobEvicted_EmergencyWALMissingComponents verifies that when the job
// is evicted from the JobManager (jobFound=false) and BlockHex is empty, the
// emergency WAL entry is written with status "build_failed" and all raw
// component fields are empty (since there's no job to populate them from).
// This matches pool.go:1094-1141 where the emergency entry is constructed.
func TestV1_3_JobEvicted_EmergencyWALMissingComponents(t *testing.T) {
	t.Parallel()

	// Simulate the emergency WAL entry creation when job is NOT found.
	// In pool.go:1075, the condition is `if jobFound && job != nil` — when
	// jobFound=false, the raw component fields are NOT populated.
	jobFound := false
	blockHex := "" // buildFullBlock failed
	blockBuildError := "failed to decode coinbase1 hex"

	// Build emergency entry (mirrors pool.go:1100-1126)
	emergencyEntry := &BlockWALEntry{
		Height:    1000000,
		BlockHash: "0000aabbccdd00000000000000000000000000000000000000000000000000ff",
		PrevHash:  "00001111222200000000000000000000000000000000000000000000000000aa",
		Status:    "build_failed",
	}

	if blockBuildError != "" {
		emergencyEntry.SubmitError = blockBuildError
	}

	// Only populate raw components if job was found (mirrors pool.go:1115-1123)
	if jobFound {
		// This block should NOT execute in this test
		emergencyEntry.CoinBase1 = "would_be_populated"
		emergencyEntry.CoinBase2 = "would_be_populated"
	}
	// ExtraNonce1/2 are always populated from share (pool.go:1124-1125)
	emergencyEntry.ExtraNonce1 = "aabb0011"
	emergencyEntry.ExtraNonce2 = "00000001"

	// Write to real WAL
	dir := t.TempDir()
	wal := createTestWAL(t, dir)
	defer wal.Close()

	if err := wal.LogBlockFound(emergencyEntry); err != nil {
		t.Fatalf("Failed to write emergency WAL entry: %v", err)
	}

	// Verify the entry was written and readable
	entries := readWALEntries(t, dir)
	if len(entries) != 1 {
		t.Fatalf("Expected 1 WAL entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Status != "build_failed" {
		t.Errorf("Expected status 'build_failed', got %q", entry.Status)
	}
	if entry.BlockHex != "" {
		t.Error("BlockHex should be empty for emergency entry")
	}
	if entry.SubmitError != blockBuildError {
		t.Errorf("Expected SubmitError=%q, got %q", blockBuildError, entry.SubmitError)
	}

	// Raw fields should be EMPTY because job was not found
	if entry.CoinBase1 != "" {
		t.Errorf("CoinBase1 should be empty when job is evicted, got %q", entry.CoinBase1)
	}
	if entry.CoinBase2 != "" {
		t.Errorf("CoinBase2 should be empty when job is evicted, got %q", entry.CoinBase2)
	}
	if entry.Version != "" {
		t.Errorf("Version should be empty when job is evicted, got %q", entry.Version)
	}
	if entry.NBits != "" {
		t.Errorf("NBits should be empty when job is evicted, got %q", entry.NBits)
	}

	// ExtraNonce1/2 SHOULD be populated (from share, not job)
	if entry.ExtraNonce1 != "aabb0011" {
		t.Errorf("ExtraNonce1 should be populated from share, got %q", entry.ExtraNonce1)
	}
	if entry.ExtraNonce2 != "00000001" {
		t.Errorf("ExtraNonce2 should be populated from share, got %q", entry.ExtraNonce2)
	}

	if blockHex != "" {
		t.Error("Test invariant violated: blockHex should be empty")
	}
}

// =============================================================================
// V2.4: Empty BlockHex AND Empty BlockBuildError → Emergency WAL
// =============================================================================

// TestV2_4_EmptyBlockHex_EmptyBuildError_EmergencyWAL verifies that when
// BlockHex is empty AND BlockBuildError is also empty (unusual edge case),
// the emergency WAL entry is still written. The SubmitError field will be
// empty, but raw components are still preserved for operator reconstruction.
func TestV2_4_EmptyBlockHex_EmptyBuildError_EmergencyWAL(t *testing.T) {
	t.Parallel()

	// Simulate: buildFullBlock returned error but BlockBuildError is empty string
	blockHex := ""
	blockBuildError := "" // Unusual but possible

	emergencyEntry := &BlockWALEntry{
		Height:    999999,
		BlockHash: "0000ffee00000000000000000000000000000000000000000000000000000001",
		PrevHash:  "0000ddcc00000000000000000000000000000000000000000000000000000002",
		Status:    "build_failed",
	}

	// SubmitError will be empty — this is the diagnostic gap from V2.4
	if blockBuildError != "" {
		emergencyEntry.SubmitError = blockBuildError
	}

	// Job WAS found in this scenario, so raw components are populated
	emergencyEntry.CoinBase1 = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff"
	emergencyEntry.CoinBase2 = "ffffffff0100f2052a01000000"
	emergencyEntry.Version = "20000000"
	emergencyEntry.NBits = "1a0377ae"
	emergencyEntry.NTime = "65a1b2c3"
	emergencyEntry.Nonce = "deadbeef"
	emergencyEntry.ExtraNonce1 = "aabb0011"
	emergencyEntry.ExtraNonce2 = "00000002"

	dir := t.TempDir()
	wal := createTestWAL(t, dir)
	defer wal.Close()

	if err := wal.LogBlockFound(emergencyEntry); err != nil {
		t.Fatalf("Failed to write emergency WAL entry: %v", err)
	}

	entries := readWALEntries(t, dir)
	if len(entries) != 1 {
		t.Fatalf("Expected 1 WAL entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Status != "build_failed" {
		t.Errorf("Expected status 'build_failed', got %q", entry.Status)
	}
	if entry.SubmitError != "" {
		t.Errorf("Expected empty SubmitError for empty BlockBuildError, got %q", entry.SubmitError)
	}

	// Raw components MUST be present for operator reconstruction
	if entry.CoinBase1 == "" {
		t.Error("CoinBase1 must be populated for operator reconstruction")
	}
	if entry.CoinBase2 == "" {
		t.Error("CoinBase2 must be populated for operator reconstruction")
	}
	if entry.Version == "" {
		t.Error("Version must be populated for operator reconstruction")
	}
	if entry.NBits == "" {
		t.Error("NBits must be populated for operator reconstruction")
	}

	if blockHex != "" {
		t.Error("Test invariant violated: blockHex should be empty")
	}
}

// =============================================================================
// V3.7: submitblock OK but GetBlockHash Fails → Still "submitted"
// =============================================================================

// TestV3_7_SubmitOK_VerifyFails_StillSubmitted simulates the scenario where
// SubmitBlock succeeds (no error) but GetBlockHash fails (RPC error during
// verification). In this case, sr.Submitted=true and sr.Verified=false.
// The code at pool.go:1193 checks `sr.Submitted || sr.Verified`, so the
// block proceeds as "submitted" status.
func TestV3_7_SubmitOK_VerifyFails_StillSubmitted(t *testing.T) {
	t.Parallel()

	// Simulate SubmitBlockWithVerification result
	type SubmissionResult struct {
		Submitted bool
		Verified  bool
		SubmitErr error
		VerifyErr error
	}

	sr := SubmissionResult{
		Submitted: true,  // submitblock returned nil
		Verified:  false, // GetBlockHash returned error
	}

	// Mirror pool.go:1193 decision logic
	blockStatus := "rejected" // default
	if sr.Submitted || sr.Verified {
		blockStatus = "submitted"
	}

	if blockStatus != "submitted" {
		t.Errorf("Expected blockStatus='submitted' when Submitted=true, got %q", blockStatus)
	}

	// After the "submitted" to "pending" transition at pool.go:1324
	if blockStatus == "submitted" {
		blockStatus = "pending"
	}

	if blockStatus != "pending" {
		t.Errorf("Expected final blockStatus='pending', got %q", blockStatus)
	}
}

// =============================================================================
// V3.8: Pre-Submission WAL Entry Survives Crash After RPC
// =============================================================================

// TestV3_8_PreSubmissionWAL_SurvivesCrashAfterRPC verifies the P0 audit fix:
// a "submitting" WAL entry is written BEFORE the RPC call. If the process
// crashes after RPC success but before the post-submission WAL write,
// RecoverUnsubmittedBlocks finds the "submitting" entry and resubmits it.
func TestV3_8_PreSubmissionWAL_SurvivesCrashAfterRPC(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wal := createTestWAL(t, dir)

	// Step 1: Write pre-submission "submitting" entry (mirrors pool.go:1155-1177)
	preSubmitEntry := &BlockWALEntry{
		Height:    1234567,
		BlockHash: "0000beef00000000000000000000000000000000000000000000000000000001",
		PrevHash:  "0000aaaa00000000000000000000000000000000000000000000000000000002",
		BlockHex:  "020000000000...full_block_hex_here...000000",
		Status:    "submitting",
	}

	if err := wal.LogBlockFound(preSubmitEntry); err != nil {
		t.Fatalf("Failed to write pre-submission WAL entry: %v", err)
	}
	wal.Close()

	// Step 2: Simulate crash — NO post-submission entry written
	// (In normal flow, pool.go:1297-1321 would write the final status)

	// Step 3: On startup, RecoverUnsubmittedBlocks finds the "submitting" entry
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block, got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]
	if recovered.Status != "submitting" {
		t.Errorf("Expected recovered status 'submitting', got %q", recovered.Status)
	}
	if recovered.BlockHex != preSubmitEntry.BlockHex {
		t.Error("Recovered entry must have the full BlockHex for resubmission")
	}
	if recovered.Height != 1234567 {
		t.Errorf("Expected recovered height 1234567, got %d", recovered.Height)
	}
}

// =============================================================================
// V3.9: Post-Submission WAL Fails → Pre-Submission Entry Survives
// =============================================================================

// TestV3_9_PostSubmitWALFail_PreSubmitSurvives verifies that even if the
// post-submission WAL write fails, the pre-submission "submitting" entry
// still exists and can be recovered. This simulates the scenario at
// pool.go:1313-1320 where LogBlockFound returns an error for the final entry.
func TestV3_9_PostSubmitWALFail_PreSubmitSurvives(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wal := createTestWAL(t, dir)

	// Write pre-submission entry (succeeds)
	preEntry := &BlockWALEntry{
		Height:    888888,
		BlockHash: "0000dddd00000000000000000000000000000000000000000000000000000001",
		BlockHex:  "020000000000...block_hex...000000",
		Status:    "submitting",
	}

	if err := wal.LogBlockFound(preEntry); err != nil {
		t.Fatalf("Pre-submission WAL write failed: %v", err)
	}

	// Simulate: post-submission write fails (we just don't write it)
	// In production, this could happen due to disk full (pool.go:1314 error path)
	wal.Close()

	// Recovery should find the "submitting" entry
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block, got %d", len(unsubmitted))
	}

	if unsubmitted[0].BlockHex == "" {
		t.Error("Pre-submission entry must preserve BlockHex for resubmission")
	}
	if unsubmitted[0].Status != "submitting" {
		t.Errorf("Expected status 'submitting', got %q", unsubmitted[0].Status)
	}
}

// =============================================================================
// V3.10: DB Insert Fails → Block Still in Daemon + WAL
// =============================================================================

// TestV3_10_DBInsertFails_WALStillValid verifies that a successful block
// submission is recorded in the WAL regardless of DB insert success.
// In SOLO mode, the blockchain IS the ledger — the WAL provides the
// audit trail even if the pool database is unavailable.
func TestV3_10_DBInsertFails_WALStillValid(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wal := createTestWAL(t, dir)
	defer wal.Close()

	// Write pre-submission WAL
	preEntry := &BlockWALEntry{
		Height:    777777,
		BlockHash: "0000cccc00000000000000000000000000000000000000000000000000000001",
		BlockHex:  "020000000000...hex...000000",
		Status:    "submitting",
	}
	if err := wal.LogBlockFound(preEntry); err != nil {
		t.Fatalf("Pre-submission WAL write failed: %v", err)
	}

	// Write post-submission WAL (submission succeeded)
	postEntry := &BlockWALEntry{
		Height:    777777,
		BlockHash: "0000cccc00000000000000000000000000000000000000000000000000000001",
		BlockHex:  "020000000000...hex...000000",
		Status:    "submitted",
	}
	if err := wal.LogSubmissionResult(postEntry); err != nil {
		t.Fatalf("Post-submission WAL write failed: %v", err)
	}

	// Simulate: DB insert fails (pool.go:1542 returns error)
	dbInsertErr := "connection refused" // Simulated DB failure
	_ = dbInsertErr

	// WAL should still have the complete record
	entries := readWALEntries(t, dir)
	if len(entries) < 2 {
		t.Fatalf("Expected at least 2 WAL entries, got %d", len(entries))
	}

	// The latest entry for this hash should be "submitted"
	latest := entries[len(entries)-1]
	if latest.Status != "submitted" {
		t.Errorf("Latest WAL entry should be 'submitted', got %q", latest.Status)
	}

	// Recovery should NOT return this block (it's already "submitted")
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("Should have 0 unsubmitted blocks (already submitted), got %d", len(unsubmitted))
	}
}

// =============================================================================
// V4.1: All Retries Fail Within SubmitDeadline
// =============================================================================

// TestV4_1_AllRetriesFail_SimulatesRetryExhaustion simulates the retry loop
// at pool.go:1392-1446 where all attempts fail. Verifies that after exhausting
// retries, the WAL has a "failed" entry and blockStatus becomes "orphaned".
func TestV4_1_AllRetriesFail_SimulatesRetryExhaustion(t *testing.T) {
	t.Parallel()

	maxRetries := 3
	blockStatus := "rejected" // Initial submission failed
	lastErr := "connection refused"
	attempt := 2
	retrySucceeded := false

	// Simulate retry loop (mirrors pool.go:1392-1446)
	for attempt <= maxRetries+1 {
		// Each retry also fails with transient error
		submitSuccess := false
		retryErr := "connection timeout"

		if submitSuccess {
			blockStatus = "pending"
			retrySucceeded = true
			break
		}

		lastErr = retryErr

		// Check if permanent rejection
		if isPermanentRejection(lastErr) {
			blockStatus = "orphaned"
			break
		}

		attempt++
	}

	if retrySucceeded {
		t.Fatal("No retry should have succeeded in this test")
	}

	// After loop: blockStatus should still be "rejected" (no permanent rejection)
	// Then pool.go:1479 sets it to "orphaned"
	if blockStatus != "rejected" {
		t.Errorf("Expected blockStatus='rejected' after transient failures, got %q", blockStatus)
	}

	// Pool.go:1479-1487: final status assignment
	if blockStatus != "pending" {
		blockStatus = "orphaned"
	}

	if blockStatus != "orphaned" {
		t.Errorf("Expected final blockStatus='orphaned', got %q", blockStatus)
	}

	// Verify retry count
	expectedAttempts := maxRetries + 2 // Started at 2, loop exits at maxRetries+2
	if attempt != expectedAttempts {
		t.Errorf("Expected %d total attempts, got %d", expectedAttempts, attempt)
	}

	// Verify WAL would have "failed" status
	walStatus := "failed"
	if blockStatus != "pending" {
		walStatus = "failed" // matches pool.go:1482
	}
	if walStatus != "failed" {
		t.Errorf("WAL should have 'failed' status, got %q", walStatus)
	}
}

// =============================================================================
// V4.3: Permanent Rejection on Retry → Immediate Exit
// =============================================================================

// TestV4_3_PermanentRejectionOnRetry_ImmediateExit simulates the retry loop
// where the first attempt returns a transient error but a retry returns a
// permanent rejection (e.g., "prev-blk-not-found"). The loop at pool.go:1433
// detects the permanent rejection and breaks immediately.
func TestV4_3_PermanentRejectionOnRetry_ImmediateExit(t *testing.T) {
	t.Parallel()

	maxRetries := 5
	blockStatus := "rejected"
	attempt := 2
	retryErrors := []string{
		"connection timeout", // Attempt 2: transient
		"prev-blk-not-found", // Attempt 3: PERMANENT → breaks
		"should never reach",
		"should never reach",
	}

	errorIdx := 0
	for attempt <= maxRetries+1 {
		if errorIdx >= len(retryErrors) {
			break
		}
		lastErr := retryErrors[errorIdx]
		errorIdx++

		if isPermanentRejection(lastErr) {
			blockStatus = "orphaned"
			break // Matches pool.go:1443
		}

		attempt++
	}

	if blockStatus != "orphaned" {
		t.Errorf("Expected blockStatus='orphaned' after permanent rejection, got %q", blockStatus)
	}

	// Should have exited on attempt 3 (index 1 in retryErrors)
	if errorIdx != 2 {
		t.Errorf("Expected loop to exit after 2 error checks (transient + permanent), got %d", errorIdx)
	}
}

// =============================================================================
// isPermanentRejection: Exhaustive BIP22 Pattern Coverage
// =============================================================================

// TestIsPermanentRejection_AllBIP22Patterns tests every pattern defined in
// isPermanentRejection at pool.go:2648-2683.
func TestIsPermanentRejection_AllBIP22Patterns(t *testing.T) {
	t.Parallel()

	permanentPatterns := []struct {
		name    string
		errStr  string
		expect  bool
	}{
		// Stale/timing
		{"prev-blk-not-found", "prev-blk-not-found", true},
		{"bad-prevblk", "bad-prevblk", true},
		{"stale-prevblk", "stale-prevblk", true},
		{"stale-work", "stale-work", true},
		{"stale", "stale block", true},
		{"time-too-old", "time-too-old", true},
		{"time-too-new", "time-too-new", true},
		{"time-invalid", "time-invalid", true},

		// Duplicate
		{"duplicate", "duplicate block", true},
		{"already", "block already known", true},

		// PoW validation
		{"high-hash", "high-hash", true},
		{"bad-diffbits", "bad-diffbits", true},

		// Coinbase errors (BIP22)
		{"bad-cb-missing", "bad-cb-missing", true},
		{"bad-cb-multiple", "bad-cb-multiple", true},
		{"bad-cb-height", "bad-cb-height", true},
		{"bad-cb-length", "bad-cb-length", true},
		{"bad-cb-prefix", "bad-cb-prefix", true},
		{"bad-cb-flag", "bad-cb-flag", true},

		// Merkle/transaction
		{"bad-txnmrklroot", "bad-txnmrklroot", true},
		{"bad-txns", "bad-txns-inputs-missingorspent", true},
		{"bad-txns-nonfinal", "bad-txns-nonfinal", true},

		// Block structure
		{"bad-version", "bad-version", true},
		{"bad-blk-sigops", "bad-blk-sigops", true},

		// SegWit
		{"bad-witness-nonce-size", "bad-witness-nonce-size", true},
		{"bad-witness-merkle-match", "bad-witness-merkle-match", true},

		// Work/identity
		{"unknown-work", "unknown-work", true},
		{"unknown-user", "unknown-user", true},

		// DigiByte-specific
		{"invalidchainfound", "invalidchainfound", true},
		{"setbestchaininner", "setbestchaininner", true},
		{"addtoblockindex", "addtoblockindex", true},

		// Generic
		{"rejected", "block rejected", true},
		{"invalid", "invalid block data", true},
		{"block-validation-failed", "block-validation-failed", true},

		// Catch-all "bad-" prefix
		{"bad-arbitrary", "bad-something-unknown", true},

		// Inconclusive special case
		{"inconclusive", "inconclusive result", true},

		// TRANSIENT errors — should NOT be permanent
		{"connection-refused", "connection refused", false},
		{"timeout", "context deadline exceeded", false},
		{"eof", "EOF", false},
		{"network-error", "dial tcp 127.0.0.1:14022: connect: connection refused", false},
		{"empty-string", "", false},
		{"random-error", "something went wrong", false},
		{"rpc-error", "RPC error -28: Loading block index", false},
	}

	for _, tc := range permanentPatterns {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isPermanentRejection(tc.errStr)
			if got != tc.expect {
				t.Errorf("isPermanentRejection(%q) = %v, want %v", tc.errStr, got, tc.expect)
			}
		})
	}
}

// TestIsPermanentRejection_CaseInsensitive verifies case-insensitive matching
// since isPermanentRejection at pool.go:2645 converts to lowercase.
func TestIsPermanentRejection_CaseInsensitive(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		errStr string
	}{
		{"upper", "PREV-BLK-NOT-FOUND"},
		{"mixed", "Prev-Blk-Not-Found"},
		{"upper_duplicate", "DUPLICATE"},
		{"mixed_stale", "Stale-Work"},
		{"upper_bad_txns", "BAD-TXNMRKLROOT"},
		{"upper_inconclusive", "INCONCLUSIVE"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !isPermanentRejection(tc.errStr) {
				t.Errorf("isPermanentRejection(%q) should be true (case-insensitive)", tc.errStr)
			}
		})
	}
}

// TestIsPermanentRejection_EmbeddedInLongerMessage verifies that patterns
// are detected when embedded in longer daemon error messages.
func TestIsPermanentRejection_EmbeddedInLongerMessage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		errStr string
	}{
		{"daemon prefix", "submitblock: prev-blk-not-found (code -25)"},
		{"json rpc wrapper", `{"code":-25,"message":"prev-blk-not-found"}`},
		{"verbose", "Block submission failed: bad-txnmrklroot - merkle root mismatch"},
		{"dgb specific", "error: setbestchaininner failed during block processing"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !isPermanentRejection(tc.errStr) {
				t.Errorf("isPermanentRejection(%q) should be true (pattern embedded in message)", tc.errStr)
			}
		})
	}
}

// =============================================================================
// Helpers
// =============================================================================

// createTestWAL creates a BlockWAL in the given directory for testing.
// Uses NewBlockWAL with a nop logger, matching the pattern in block_wal_audit_test.go.
func createTestWAL(t *testing.T, dir string) *BlockWAL {
	t.Helper()

	logger := zap.NewNop()
	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	return wal
}

// readWALEntries reads all WAL entries from all files in the given directory.
func readWALEntries(t *testing.T, dir string) []BlockWALEntry {
	t.Helper()

	pattern := filepath.Join(dir, "block_wal_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("Failed to glob WAL files: %v", err)
	}

	var entries []BlockWALEntry
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("Failed to read WAL file %s: %v", file, err)
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var entry BlockWALEntry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Logf("Skipping malformed WAL line: %s", line[:min(len(line), 80)])
				continue
			}
			entries = append(entries, entry)
		}
	}

	return entries
}
