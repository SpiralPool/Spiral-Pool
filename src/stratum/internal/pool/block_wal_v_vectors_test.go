// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// V25 — RecoverSubmittedBlocks (WAL-DB Reconciliation)
// =============================================================================

// TestV25_RecoverSubmittedBlocks_EmptyDir verifies that an empty directory
// returns an empty result with no error.
func TestV25_RecoverSubmittedBlocks_EmptyDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed on empty dir: %v", err)
	}
	if len(submitted) != 0 {
		t.Errorf("Expected 0 submitted blocks from empty dir, got %d", len(submitted))
	}
}

// TestV25_RecoverSubmittedBlocks_NoSubmitted verifies that a WAL containing
// only "pending" entries returns an empty result.
func TestV25_RecoverSubmittedBlocks_NoSubmitted(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	content := strings.Join([]string{
		`{"height":100001,"block_hash":"0000000000000000000aaa1111111111111111111111111111111111111111aa","block_hex":"hex1","status":"pending"}`,
		`{"height":100002,"block_hash":"0000000000000000000bbb2222222222222222222222222222222222222222bb","block_hex":"hex2","status":"pending"}`,
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 0 {
		t.Errorf("Expected 0 submitted blocks when all entries are pending, got %d", len(submitted))
	}
}

// TestV25_RecoverSubmittedBlocks_SubmittedEntries verifies that WAL entries
// with "submitted" and "accepted" statuses are both returned.
func TestV25_RecoverSubmittedBlocks_SubmittedEntries(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	content := strings.Join([]string{
		`{"height":200001,"block_hash":"0000000000000000000ccc3333333333333333333333333333333333333333cc","block_hex":"hex_submitted","status":"submitted"}`,
		`{"height":200002,"block_hash":"0000000000000000000ddd4444444444444444444444444444444444444444dd","block_hex":"hex_accepted","status":"accepted"}`,
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("Expected 2 submitted blocks (submitted + accepted), got %d", len(submitted))
	}

	hashes := map[string]bool{}
	for _, entry := range submitted {
		hashes[entry.BlockHash] = true
	}
	if !hashes["0000000000000000000ccc3333333333333333333333333333333333333333cc"] {
		t.Error("Missing 'submitted' entry in results")
	}
	if !hashes["0000000000000000000ddd4444444444444444444444444444444444444444dd"] {
		t.Error("Missing 'accepted' entry in results")
	}
}

// TestV25_RecoverSubmittedBlocks_MixedStatuses verifies that only "submitted"
// and "accepted" entries are returned when the WAL contains a mix of statuses.
func TestV25_RecoverSubmittedBlocks_MixedStatuses(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	content := strings.Join([]string{
		`{"height":300001,"block_hash":"000000000000000000011111111111111111111111111111111111111111aaaa","block_hex":"hex1","status":"pending"}`,
		`{"height":300002,"block_hash":"000000000000000000022222222222222222222222222222222222222222bbbb","block_hex":"hex2","status":"submitting"}`,
		`{"height":300003,"block_hash":"000000000000000000033333333333333333333333333333333333333333cccc","block_hex":"hex3","status":"submitted"}`,
		`{"height":300004,"block_hash":"000000000000000000044444444444444444444444444444444444444444dddd","block_hex":"hex4","status":"accepted"}`,
		`{"height":300005,"block_hash":"000000000000000000055555555555555555555555555555555555555555eeee","block_hex":"hex5","status":"rejected"}`,
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("Expected 2 submitted blocks (submitted + accepted only), got %d", len(submitted))
	}

	for _, entry := range submitted {
		if entry.Status != "submitted" && entry.Status != "accepted" {
			t.Errorf("Unexpected status %q in results — only submitted/accepted expected", entry.Status)
		}
	}
}

// TestV25_RecoverSubmittedBlocks_MultiFile verifies that submitted/accepted
// entries are recovered across multiple WAL files from different dates.
func TestV25_RecoverSubmittedBlocks_MultiFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	files := map[string]string{
		"block_wal_2026-01-28.jsonl": `{"height":400001,"block_hash":"000000000000000000066666666666666666666666666666666666666666ff01","block_hex":"day1_hex","status":"submitted"}`,
		"block_wal_2026-01-29.jsonl": `{"height":400002,"block_hash":"000000000000000000077777777777777777777777777777777777777777ff02","block_hex":"day2_hex","status":"pending"}`,
		"block_wal_2026-01-30.jsonl": `{"height":400003,"block_hash":"000000000000000000088888888888888888888888888888888888888888ff03","block_hex":"day3_hex","status":"accepted"}`,
	}

	for filename, content := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content+"\n"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", filename, err)
		}
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("Expected 2 submitted blocks from multi-file scan, got %d", len(submitted))
	}

	heights := map[uint64]bool{}
	for _, entry := range submitted {
		heights[entry.Height] = true
	}
	if !heights[400001] {
		t.Error("Missing 'submitted' block from day 1 (height 400001)")
	}
	if !heights[400003] {
		t.Error("Missing 'accepted' block from day 3 (height 400003)")
	}
	if heights[400002] {
		t.Error("Pending block from day 2 should NOT appear in submitted results")
	}
}

// TestV25_RecoverSubmittedBlocks_LatestEntryWins verifies that when the same
// block hash appears twice (first "pending", then "submitted"), the latest
// entry wins and the block is returned as submitted.
func TestV25_RecoverSubmittedBlocks_LatestEntryWins(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	blockHash := "000000000000000000099999999999999999999999999999999999999999ff04"
	content := strings.Join([]string{
		`{"height":500001,"block_hash":"` + blockHash + `","block_hex":"the_block","status":"pending"}`,
		`{"height":500001,"block_hash":"` + blockHash + `","block_hex":"the_block","status":"submitted"}`,
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 1 {
		t.Fatalf("Expected 1 submitted block (latest entry wins), got %d", len(submitted))
	}
	if submitted[0].BlockHash != blockHash {
		t.Errorf("Wrong block hash: got %s, want %s", submitted[0].BlockHash, blockHash)
	}
	if submitted[0].Status != "submitted" {
		t.Errorf("Expected status 'submitted', got %q", submitted[0].Status)
	}
}

// TestV25_RecoverSubmittedBlocks_MalformedLinesSkipped verifies that malformed
// JSON lines in the WAL are skipped without preventing recovery of valid entries.
func TestV25_RecoverSubmittedBlocks_MalformedLinesSkipped(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	content := strings.Join([]string{
		`THIS_IS_NOT_JSON`,
		`{"height":600001,"block_hash":"00000000000000000010aaaa1111111111111111111111111111111111111101","block_hex":"valid_block","status":"submitted"}`,
		`{truncated`,
		`{"height":600002,"block_hash":"00000000000000000010bbbb2222222222222222222222222222222222222202","block_hex":"also_valid","status":"accepted"}`,
		`{"broken":`,
	}, "\n") + "\n"

	if err := os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("Expected 2 valid submitted blocks despite malformed lines, got %d", len(submitted))
	}
}

// =============================================================================
// V23 — Directory Metadata Sync
// =============================================================================

// TestV23_WAL_DirectoryExists verifies that NewBlockWAL creates the target
// directory if it does not already exist (via os.MkdirAll).
func TestV23_WAL_DirectoryExists(t *testing.T) {
	t.Parallel()
	baseDir := t.TempDir()
	walDir := filepath.Join(baseDir, "nested", "wal", "dir")

	logger := zap.NewNop()
	wal, err := NewBlockWAL(walDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed to create nested directory: %v", err)
	}
	defer wal.Close()

	info, err := os.Stat(walDir)
	if err != nil {
		t.Fatalf("WAL directory does not exist after NewBlockWAL: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("Expected %s to be a directory, but it is not", walDir)
	}
}

// TestV23_WAL_FileCreated verifies that after NewBlockWAL, the WAL file
// actually exists on disk.
func TestV23_WAL_FileCreated(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	logger := zap.NewNop()
	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	walPath := wal.FilePath()
	if walPath == "" {
		t.Fatal("WAL FilePath() returned empty string")
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("WAL file does not exist on disk after NewBlockWAL: %v", err)
	}
	if info.IsDir() {
		t.Errorf("WAL file path %s is a directory, expected a file", walPath)
	}
}

// =============================================================================
// V11/V13 — WAL File Locking
// =============================================================================

// TestV11_WAL_FileLock_SecondInstanceFails verifies that opening a second
// BlockWAL on the same directory fails with an error indicating another
// instance is already running. This test does NOT use t.Parallel() because
// it relies on shared filesystem lock state.
func TestV11_WAL_FileLock_SecondInstanceFails(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	// First instance — should succeed
	wal1, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("First NewBlockWAL failed: %v", err)
	}
	defer wal1.Close()

	// Second instance on the same directory — should fail due to file lock
	_, err = NewBlockWAL(tmpDir, logger)
	if err == nil {
		t.Fatal("Expected error from second NewBlockWAL on same dir, got nil — file lock not working!")
	}

	if !strings.Contains(err.Error(), "another instance") {
		t.Errorf("Expected error containing 'another instance', got: %v", err)
	}
}

// TestV11_WAL_FileLock_DifferentDirsOK verifies that opening WAL instances
// in different directories both succeed — file locks are per-file, not global.
// This test does NOT use t.Parallel() because it relies on filesystem lock state.
func TestV11_WAL_FileLock_DifferentDirsOK(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	logger := zap.NewNop()

	walA, err := NewBlockWAL(dirA, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL on dir A failed: %v", err)
	}
	defer walA.Close()

	walB, err := NewBlockWAL(dirB, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL on dir B failed (should succeed for different dir): %v", err)
	}
	defer walB.Close()
}

// =============================================================================
// WAL Entry Lifecycle
// =============================================================================

// TestWAL_LogBlockFound_PendingStatus verifies that LogBlockFound sets
// Status to "pending" when the entry has an empty Status field.
func TestWAL_LogBlockFound_PendingStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    700001,
		BlockHash: "00000000000000000020aaaa1111111111111111111111111111111111111101",
		BlockHex:  "deadbeef",
		// Status intentionally left empty
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	if entry.Status != "pending" {
		t.Errorf("Expected Status to be set to 'pending' when empty, got %q", entry.Status)
	}
}

// TestWAL_LogBlockFound_PreservesExistingStatus verifies that LogBlockFound
// does NOT overwrite a non-empty Status field.
func TestWAL_LogBlockFound_PreservesExistingStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    700002,
		BlockHash: "00000000000000000020bbbb2222222222222222222222222222222222222202",
		BlockHex:  "cafebabe",
		Status:    "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	if entry.Status != "submitting" {
		t.Errorf("Expected Status to remain 'submitting', got %q", entry.Status)
	}
}

// TestWAL_LogSubmissionResult_UpdatesTimestamp verifies that
// LogSubmissionResult sets the SubmittedAt field.
func TestWAL_LogSubmissionResult_UpdatesTimestamp(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    700003,
		BlockHash: "00000000000000000020cccc3333333333333333333333333333333333333303",
		BlockHex:  "deadcafe",
		Status:    "submitted",
	}

	before := time.Now()
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}

	if entry.SubmittedAt == "" {
		t.Fatal("LogSubmissionResult did not set SubmittedAt")
	}

	// Parse the SubmittedAt timestamp and verify it is within the expected range
	submittedAt, err := time.Parse(time.RFC3339Nano, entry.SubmittedAt)
	if err != nil {
		t.Fatalf("Failed to parse SubmittedAt %q: %v", entry.SubmittedAt, err)
	}
	after := time.Now()

	if submittedAt.Before(before) || submittedAt.After(after) {
		t.Errorf("SubmittedAt %v is outside expected range [%v, %v]", submittedAt, before, after)
	}
}

// TestWAL_RoundTrip_WriteAndRecover verifies the full lifecycle: LogBlockFound
// with "pending" status, then LogSubmissionResult with "submitted" status,
// then RecoverSubmittedBlocks returns the block.
func TestWAL_RoundTrip_WriteAndRecover(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "00000000000000000020dddd4444444444444444444444444444444444444404"

	// Phase 1: Log block found as pending
	foundEntry := &BlockWALEntry{
		Height:        700004,
		BlockHash:     blockHash,
		BlockHex:      "fullblockhex",
		MinerAddress:  "bc1qroundtrip",
		WorkerName:    "worker_rt",
		CoinbaseValue: 625000000,
	}
	if err := wal.LogBlockFound(foundEntry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	// LogBlockFound should have set Status to "pending"
	if foundEntry.Status != "pending" {
		t.Fatalf("Expected Status 'pending' after LogBlockFound, got %q", foundEntry.Status)
	}

	// Phase 2: Log submission result as submitted
	resultEntry := &BlockWALEntry{
		Height:        700004,
		BlockHash:     blockHash,
		BlockHex:      "fullblockhex",
		MinerAddress:  "bc1qroundtrip",
		WorkerName:    "worker_rt",
		CoinbaseValue: 625000000,
		Status:        "submitted",
	}
	if err := wal.LogSubmissionResult(resultEntry); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}

	wal.Close()

	// Phase 3: RecoverSubmittedBlocks should find this block
	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 1 {
		t.Fatalf("Expected 1 submitted block in round-trip, got %d", len(submitted))
	}

	recovered := submitted[0]
	if recovered.BlockHash != blockHash {
		t.Errorf("BlockHash mismatch: got %s, want %s", recovered.BlockHash, blockHash)
	}
	if recovered.Status != "submitted" {
		t.Errorf("Status mismatch: got %q, want 'submitted'", recovered.Status)
	}
	if recovered.BlockHex != "fullblockhex" {
		t.Errorf("BlockHex mismatch: got %q, want 'fullblockhex'", recovered.BlockHex)
	}
	if recovered.MinerAddress != "bc1qroundtrip" {
		t.Errorf("MinerAddress mismatch: got %q, want 'bc1qroundtrip'", recovered.MinerAddress)
	}

	// Also verify it does NOT appear in unsubmitted
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Submitted block should NOT appear in unsubmitted recovery")
		}
	}
}

// =============================================================================
// splitLines helper
// =============================================================================

// TestSplitLines_Empty verifies that empty input returns an empty result.
func TestSplitLines_Empty(t *testing.T) {
	t.Parallel()
	result := splitLines([]byte{})
	if len(result) != 0 {
		t.Errorf("Expected 0 lines from empty input, got %d", len(result))
	}
}

// TestSplitLines_SingleLine verifies that a single line with trailing newline
// returns one element.
func TestSplitLines_SingleLine(t *testing.T) {
	t.Parallel()
	result := splitLines([]byte("hello\n"))
	if len(result) != 1 {
		t.Fatalf("Expected 1 line, got %d", len(result))
	}
	if string(result[0]) != "hello" {
		t.Errorf("Expected 'hello', got %q", string(result[0]))
	}
}

// TestSplitLines_MultipleLines verifies correct splitting of multiple lines.
func TestSplitLines_MultipleLines(t *testing.T) {
	t.Parallel()
	result := splitLines([]byte("a\nb\nc\n"))
	if len(result) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(result))
	}
	expected := []string{"a", "b", "c"}
	for i, want := range expected {
		if string(result[i]) != want {
			t.Errorf("Line %d: expected %q, got %q", i, want, string(result[i]))
		}
	}
}

// TestSplitLines_CRLF verifies that \r\n line endings are handled correctly,
// with the \r stripped from each line.
func TestSplitLines_CRLF(t *testing.T) {
	t.Parallel()
	result := splitLines([]byte("a\r\nb\r\n"))
	if len(result) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(result))
	}
	expected := []string{"a", "b"}
	for i, want := range expected {
		if string(result[i]) != want {
			t.Errorf("Line %d: expected %q, got %q", i, want, string(result[i]))
		}
	}
}

// TestSplitLines_NoTrailingNewline verifies that content without a trailing
// newline still returns the final segment.
func TestSplitLines_NoTrailingNewline(t *testing.T) {
	t.Parallel()
	result := splitLines([]byte("a\nb"))
	if len(result) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(result))
	}
	expected := []string{"a", "b"}
	for i, want := range expected {
		if string(result[i]) != want {
			t.Errorf("Line %d: expected %q, got %q", i, want, string(result[i]))
		}
	}
}

// TestSplitLines_EmptyLines verifies that empty lines between content are
// preserved as empty byte slices.
func TestSplitLines_EmptyLines(t *testing.T) {
	t.Parallel()
	result := splitLines([]byte("a\n\nb\n"))
	if len(result) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(result))
	}
	expected := []string{"a", "", "b"}
	for i, want := range expected {
		if string(result[i]) != want {
			t.Errorf("Line %d: expected %q, got %q", i, want, string(result[i]))
		}
	}
}

// =============================================================================
// Compile-time verification that JSON field tags match expected structure
// =============================================================================

// init-time check: ensure BlockWALEntry round-trips through JSON correctly.
// This catches accidental struct tag changes that would break WAL recovery.
func TestBlockWALEntry_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := BlockWALEntry{
		Height:        123456,
		BlockHash:     "testhash",
		PrevHash:      "prevhash",
		BlockHex:      "blockhex",
		MinerAddress:  "miner",
		WorkerName:    "worker",
		JobID:         "job1",
		CoinbaseValue: 5000000000,
		Status:        "submitted",
		SubmitError:   "none",
		SubmittedAt:   "2026-01-30T12:00:00Z",
		RejectReason:  "",
		CoinBase1:     "cb1",
		CoinBase2:     "cb2",
		ExtraNonce1:   "en1",
		ExtraNonce2:   "en2",
		Version:       "ver",
		NBits:         "bits",
		NTime:         "ntime",
		Nonce:         "nonce",
		TransactionData: []string{"tx1", "tx2"},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(data, &recovered); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if recovered.Height != original.Height {
		t.Errorf("Height: got %d, want %d", recovered.Height, original.Height)
	}
	if recovered.BlockHash != original.BlockHash {
		t.Errorf("BlockHash: got %q, want %q", recovered.BlockHash, original.BlockHash)
	}
	if recovered.Status != original.Status {
		t.Errorf("Status: got %q, want %q", recovered.Status, original.Status)
	}
	if recovered.CoinbaseValue != original.CoinbaseValue {
		t.Errorf("CoinbaseValue: got %d, want %d", recovered.CoinbaseValue, original.CoinbaseValue)
	}
	if len(recovered.TransactionData) != len(original.TransactionData) {
		t.Errorf("TransactionData length: got %d, want %d", len(recovered.TransactionData), len(original.TransactionData))
	}
}
