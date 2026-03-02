// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// SOLO OPERATOR PROCEDURES TEST SUITE
// =============================================================================
//
// These tests codify operator runbook procedures as executable tests:
//   - WAL backup and restore
//   - Post-restore reconciliation
//   - Monitoring threshold verification
//   - Block lifecycle integrity
//   - WAL file rotation and locking
//   - Large-scale recovery performance
//
// Every test uses t.TempDir() for isolation and t.Parallel() for speed.

// makeTestEntry creates a BlockWALEntry with the given height, hash, and status.
// Helper to reduce boilerplate across tests.
func makeTestEntry(height uint64, hash, status string) *BlockWALEntry {
	return &BlockWALEntry{
		Height:        height,
		BlockHash:     hash,
		PrevHash:      "000000000000000000000000000000000000000000000000000000000000prev",
		BlockHex:      fmt.Sprintf("deadbeef%04x", height),
		MinerAddress:  "bc1qoperator",
		WorkerName:    fmt.Sprintf("rig-%d", height%100),
		JobID:         fmt.Sprintf("job_%d", height),
		JobAge:        2 * time.Second,
		CoinbaseValue: 312500000,
		Status:        status,
	}
}

// writeWALEntriesFile writes a slice of BlockWALEntry as JSONL to the given path.
func writeWALEntriesFile(t *testing.T, filePath string, entries []BlockWALEntry) {
	t.Helper()
	var lines []string
	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		lines = append(lines, string(data))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", filePath, err)
	}
}

// -----------------------------------------------------------------------------
// 1. TestOperator_WALBackupRestore
// -----------------------------------------------------------------------------

// TestOperator_WALBackupRestore simulates the operator backup procedure:
// create WAL, write entries, close, copy to backup directory, then verify the
// backup file preserves all entries with correct data and original write order.
func TestOperator_WALBackupRestore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Define 5 entries: 3 pending, 2 submitted.
	type entrySpec struct {
		height uint64
		hash   string
		status string
	}
	specs := []entrySpec{
		{100001, "0000000000000000000aaa1111111111111111111111111111111111111111aaa1", ""},          // defaults to pending
		{100002, "0000000000000000000bbb2222222222222222222222222222222222222222bbb2", ""},          // defaults to pending
		{100003, "0000000000000000000ccc3333333333333333333333333333333333333333ccc3", ""},          // defaults to pending
		{100004, "0000000000000000000ddd4444444444444444444444444444444444444444ddd4", "submitted"}, // pre-set
		{100005, "0000000000000000000eee5555555555555555555555555555555555555555eee5", "submitted"}, // pre-set
	}

	// Write entries via WAL API.
	for i, s := range specs {
		entry := makeTestEntry(s.height, s.hash, s.status)
		if s.status == "submitted" {
			// For submitted entries, first write as pending then log submission result.
			entry.Status = "submitted"
			if err := wal.LogBlockFound(entry); err != nil {
				t.Fatalf("LogBlockFound #%d failed: %v", i, err)
			}
		} else {
			if err := wal.LogBlockFound(entry); err != nil {
				t.Fatalf("LogBlockFound #%d failed: %v", i, err)
			}
		}
	}

	// Close WAL before backup (releases file lock).
	if err := wal.Close(); err != nil {
		t.Fatalf("WAL Close failed: %v", err)
	}

	// --- Operator backup procedure: copy WAL file to backup directory ---
	walPath := wal.FilePath()
	originalData, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("ReadFile (original) failed: %v", err)
	}

	backupDir := filepath.Join(dir, "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("MkdirAll backup dir: %v", err)
	}
	backupPath := filepath.Join(backupDir, filepath.Base(walPath))
	if err := os.WriteFile(backupPath, originalData, 0644); err != nil {
		t.Fatalf("WriteFile (backup) failed: %v", err)
	}

	// --- Parse backup and verify ---
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile (backup) failed: %v", err)
	}

	lines := splitLines(backupData)
	var recovered []BlockWALEntry
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("JSON unmarshal backup line failed: %v", err)
		}
		recovered = append(recovered, entry)
	}

	// Verify all 5 entries present.
	if len(recovered) != 5 {
		t.Fatalf("Expected 5 entries in backup, got %d", len(recovered))
	}

	// Verify entry order matches original write order.
	for i, s := range specs {
		if recovered[i].Height != s.height {
			t.Errorf("Entry %d: height mismatch: got %d, want %d", i, recovered[i].Height, s.height)
		}
		if recovered[i].BlockHash != s.hash {
			t.Errorf("Entry %d: hash mismatch", i)
		}
	}

	// Verify first 3 are pending, last 2 are submitted.
	for i := 0; i < 3; i++ {
		if recovered[i].Status != "pending" {
			t.Errorf("Entry %d: expected status 'pending', got %q", i, recovered[i].Status)
		}
	}
	for i := 3; i < 5; i++ {
		if recovered[i].Status != "submitted" {
			t.Errorf("Entry %d: expected status 'submitted', got %q", i, recovered[i].Status)
		}
	}
}

// -----------------------------------------------------------------------------
// 2. TestOperator_PostRestoreReconciliation
// -----------------------------------------------------------------------------

// TestOperator_PostRestoreReconciliation simulates post-restore WAL reconciliation.
// Writes a JSONL file manually with 4 entries of mixed statuses, then verifies
// RecoverUnsubmittedBlocks and RecoverSubmittedBlocks return the correct subsets.
func TestOperator_PostRestoreReconciliation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	entries := []BlockWALEntry{
		{Height: 200001, BlockHash: "0000000000000000000aaa1111111111111111111111111111111111111111aa01", Status: "pending", BlockHex: "hex1"},
		{Height: 200002, BlockHash: "0000000000000000000bbb2222222222222222222222222222222222222222bb02", Status: "pending", BlockHex: "hex2"},
		{Height: 200003, BlockHash: "0000000000000000000ccc3333333333333333333333333333333333333333cc03", Status: "submitted", BlockHex: "hex3"},
		{Height: 200004, BlockHash: "0000000000000000000ddd4444444444444444444444444444444444444444dd04", Status: "accepted", BlockHex: "hex4"},
	}

	walFile := filepath.Join(dir, "block_wal_2026-01-30.jsonl")
	writeWALEntriesFile(t, walFile, entries)

	// RecoverUnsubmittedBlocks should find the 2 "pending" entries.
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 unsubmitted, got %d", len(unsubmitted))
	}

	unsubHeights := map[uint64]bool{}
	for _, e := range unsubmitted {
		unsubHeights[e.Height] = true
	}
	if !unsubHeights[200001] || !unsubHeights[200002] {
		t.Errorf("Unsubmitted heights mismatch: got %v", unsubHeights)
	}

	// RecoverSubmittedBlocks should find the "submitted" and "accepted" entries.
	submitted, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("Expected 2 submitted, got %d", len(submitted))
	}

	subHeights := map[uint64]bool{}
	subHashes := map[string]bool{}
	for _, e := range submitted {
		subHeights[e.Height] = true
		subHashes[e.BlockHash] = true
	}
	if !subHeights[200003] || !subHeights[200004] {
		t.Errorf("Submitted heights mismatch: got %v", subHeights)
	}
	if !subHashes[entries[2].BlockHash] || !subHashes[entries[3].BlockHash] {
		t.Errorf("Submitted hashes mismatch: got %v", subHashes)
	}
}

// -----------------------------------------------------------------------------
// 3. TestOperator_WALReconciliation_SubmissionOverridesPending
// -----------------------------------------------------------------------------

// TestOperator_WALReconciliation_SubmissionOverridesPending writes two entries
// for the same block hash: first "pending", then "submitted". Verifies that
// RecoverUnsubmittedBlocks does NOT return this block because the latest status
// (by block hash) is "submitted".
func TestOperator_WALReconciliation_SubmissionOverridesPending(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	blockHash := "0000000000000000000fff9999999999999999999999999999999999999999ff01"

	entries := []BlockWALEntry{
		{Height: 300001, BlockHash: blockHash, Status: "pending", BlockHex: "hex_pending"},
		{Height: 300001, BlockHash: blockHash, Status: "submitted", BlockHex: "hex_pending"},
	}

	walFile := filepath.Join(dir, "block_wal_2026-01-30.jsonl")
	writeWALEntriesFile(t, walFile, entries)

	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	for _, e := range unsubmitted {
		if e.BlockHash == blockHash {
			t.Errorf("Block %s should NOT be unsubmitted -- submission result overrides pending", blockHash[:16])
		}
	}
}

// -----------------------------------------------------------------------------
// 4. TestOperator_WALReconciliation_MultipleFiles
// -----------------------------------------------------------------------------

// TestOperator_WALReconciliation_MultipleFiles creates 3 WAL files simulating
// 3 days of operation with pending entries in each. Verifies that
// RecoverUnsubmittedBlocks scans ALL files and returns pending entries from every file.
func TestOperator_WALReconciliation_MultipleFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Day 1: 2 pending entries.
	day1 := []BlockWALEntry{
		{Height: 400001, BlockHash: "0000000000000000000aaa1111111111111111111111111111111111111111a401", Status: "pending", BlockHex: "d1h1"},
		{Height: 400002, BlockHash: "0000000000000000000bbb2222222222222222222222222222222222222222b402", Status: "pending", BlockHex: "d1h2"},
	}
	writeWALEntriesFile(t, filepath.Join(dir, "block_wal_2026-01-28.jsonl"), day1)

	// Day 2: 1 pending, 1 submitted (submitted should not appear in unsubmitted).
	day2 := []BlockWALEntry{
		{Height: 400003, BlockHash: "0000000000000000000ccc3333333333333333333333333333333333333333c403", Status: "pending", BlockHex: "d2h1"},
		{Height: 400004, BlockHash: "0000000000000000000ddd4444444444444444444444444444444444444444d404", Status: "submitted", BlockHex: "d2h2"},
	}
	writeWALEntriesFile(t, filepath.Join(dir, "block_wal_2026-01-29.jsonl"), day2)

	// Day 3: 1 pending entry.
	day3 := []BlockWALEntry{
		{Height: 400005, BlockHash: "0000000000000000000eee5555555555555555555555555555555555555555e405", Status: "pending", BlockHex: "d3h1"},
	}
	writeWALEntriesFile(t, filepath.Join(dir, "block_wal_2026-01-30.jsonl"), day3)

	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Expect 4 pending entries total across all 3 files.
	if len(unsubmitted) != 4 {
		t.Fatalf("Expected 4 unsubmitted from 3 files, got %d", len(unsubmitted))
	}

	pendingHeights := map[uint64]bool{}
	for _, e := range unsubmitted {
		pendingHeights[e.Height] = true
	}

	for _, h := range []uint64{400001, 400002, 400003, 400005} {
		if !pendingHeights[h] {
			t.Errorf("Missing pending entry for height %d", h)
		}
	}
	if pendingHeights[400004] {
		t.Error("Submitted entry (height 400004) should NOT appear in unsubmitted results")
	}
}

// -----------------------------------------------------------------------------
// 5. TestOperator_MonitoringThresholds_MinDiskSpace
// -----------------------------------------------------------------------------

// TestOperator_MonitoringThresholds_MinDiskSpace verifies the MinDiskSpaceBytes
// constant equals exactly 100MB (100 * 1024 * 1024). Operators rely on this
// threshold value for monitoring alert configuration.
func TestOperator_MonitoringThresholds_MinDiskSpace(t *testing.T) {
	t.Parallel()

	const expected uint64 = 100 * 1024 * 1024 // 100MB = 104857600 bytes
	if MinDiskSpaceBytes != expected {
		t.Errorf("MinDiskSpaceBytes = %d, want %d (100MB)", MinDiskSpaceBytes, expected)
	}

	// Verify the human-readable value.
	if MinDiskSpaceBytes != 104857600 {
		t.Errorf("MinDiskSpaceBytes = %d, want 104857600", MinDiskSpaceBytes)
	}
}

// -----------------------------------------------------------------------------
// 6. TestOperator_WALEntryIntegrity_JSONRoundTrip
// -----------------------------------------------------------------------------

// TestOperator_WALEntryIntegrity_JSONRoundTrip creates a BlockWALEntry with
// every field populated (including TransactionData with 5 entries), marshals
// to JSON, unmarshals back, and verifies all fields match. This validates that
// operator tools parsing WAL files will get correct data.
func TestOperator_WALEntryIntegrity_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := BlockWALEntry{
		Timestamp:     time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC),
		Height:        500001,
		BlockHash:     "0000000000000000000abcdef0123456789abcdef0123456789abcdef01234567",
		PrevHash:      "000000000000000000fedcba9876543210fedcba9876543210fedcba987654321",
		BlockHex:      "01000000deadbeefcafebabe0123456789abcdef",
		MinerAddress:  "bc1qoperatorroundtrip",
		WorkerName:    "worker-roundtrip-42",
		JobID:         "job_roundtrip_99",
		JobAge:        5*time.Second + 123*time.Millisecond,
		CoinbaseValue: 312500000,
		Status:        "submitted",
		SubmitError:   "",
		SubmittedAt:   "2026-01-30T12:00:01.000000000Z",
		RejectReason:  "",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac",
		ExtraNonce1:   "aabbccdd",
		ExtraNonce2:   "11223344",
		Version:       "20000000",
		NBits:         "1a0377ae",
		NTime:         "679b0000",
		Nonce:         "deadbeef",
		TransactionData: []string{
			"tx_data_0_0100000001000000000000000000000000000000000000000000000000000000",
			"tx_data_1_0200000002abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"tx_data_2_0300000003fedcba9876543210fedcba9876543210fedcba9876543210fedcba",
			"tx_data_3_0400000004deadbeefcafebabe0123456789abcdef0123456789abcdef012345",
			"tx_data_4_0500000005aabbccddeeff00112233445566778899aabbccddeeff0011223344",
		},
	}

	// Marshal to JSON.
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Unmarshal back.
	var roundtripped BlockWALEntry
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Verify every field.
	if !roundtripped.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", roundtripped.Timestamp, original.Timestamp)
	}
	if roundtripped.Height != original.Height {
		t.Errorf("Height mismatch: got %d, want %d", roundtripped.Height, original.Height)
	}
	if roundtripped.BlockHash != original.BlockHash {
		t.Errorf("BlockHash mismatch: got %q, want %q", roundtripped.BlockHash, original.BlockHash)
	}
	if roundtripped.PrevHash != original.PrevHash {
		t.Errorf("PrevHash mismatch: got %q, want %q", roundtripped.PrevHash, original.PrevHash)
	}
	if roundtripped.BlockHex != original.BlockHex {
		t.Errorf("BlockHex mismatch: got %q, want %q", roundtripped.BlockHex, original.BlockHex)
	}
	if roundtripped.MinerAddress != original.MinerAddress {
		t.Errorf("MinerAddress mismatch: got %q, want %q", roundtripped.MinerAddress, original.MinerAddress)
	}
	if roundtripped.WorkerName != original.WorkerName {
		t.Errorf("WorkerName mismatch: got %q, want %q", roundtripped.WorkerName, original.WorkerName)
	}
	if roundtripped.JobID != original.JobID {
		t.Errorf("JobID mismatch: got %q, want %q", roundtripped.JobID, original.JobID)
	}
	if roundtripped.JobAge != original.JobAge {
		t.Errorf("JobAge mismatch: got %v, want %v", roundtripped.JobAge, original.JobAge)
	}
	if roundtripped.CoinbaseValue != original.CoinbaseValue {
		t.Errorf("CoinbaseValue mismatch: got %d, want %d", roundtripped.CoinbaseValue, original.CoinbaseValue)
	}
	if roundtripped.Status != original.Status {
		t.Errorf("Status mismatch: got %q, want %q", roundtripped.Status, original.Status)
	}
	if roundtripped.SubmitError != original.SubmitError {
		t.Errorf("SubmitError mismatch: got %q, want %q", roundtripped.SubmitError, original.SubmitError)
	}
	if roundtripped.SubmittedAt != original.SubmittedAt {
		t.Errorf("SubmittedAt mismatch: got %q, want %q", roundtripped.SubmittedAt, original.SubmittedAt)
	}
	if roundtripped.RejectReason != original.RejectReason {
		t.Errorf("RejectReason mismatch: got %q, want %q", roundtripped.RejectReason, original.RejectReason)
	}
	if roundtripped.CoinBase1 != original.CoinBase1 {
		t.Errorf("CoinBase1 mismatch: got %q, want %q", roundtripped.CoinBase1, original.CoinBase1)
	}
	if roundtripped.CoinBase2 != original.CoinBase2 {
		t.Errorf("CoinBase2 mismatch: got %q, want %q", roundtripped.CoinBase2, original.CoinBase2)
	}
	if roundtripped.ExtraNonce1 != original.ExtraNonce1 {
		t.Errorf("ExtraNonce1 mismatch: got %q, want %q", roundtripped.ExtraNonce1, original.ExtraNonce1)
	}
	if roundtripped.ExtraNonce2 != original.ExtraNonce2 {
		t.Errorf("ExtraNonce2 mismatch: got %q, want %q", roundtripped.ExtraNonce2, original.ExtraNonce2)
	}
	if roundtripped.Version != original.Version {
		t.Errorf("Version mismatch: got %q, want %q", roundtripped.Version, original.Version)
	}
	if roundtripped.NBits != original.NBits {
		t.Errorf("NBits mismatch: got %q, want %q", roundtripped.NBits, original.NBits)
	}
	if roundtripped.NTime != original.NTime {
		t.Errorf("NTime mismatch: got %q, want %q", roundtripped.NTime, original.NTime)
	}
	if roundtripped.Nonce != original.Nonce {
		t.Errorf("Nonce mismatch: got %q, want %q", roundtripped.Nonce, original.Nonce)
	}

	// TransactionData.
	if len(roundtripped.TransactionData) != len(original.TransactionData) {
		t.Fatalf("TransactionData length mismatch: got %d, want %d",
			len(roundtripped.TransactionData), len(original.TransactionData))
	}
	for i, tx := range roundtripped.TransactionData {
		if tx != original.TransactionData[i] {
			t.Errorf("TransactionData[%d] mismatch: got %q, want %q", i, tx, original.TransactionData[i])
		}
	}
}

// -----------------------------------------------------------------------------
// 7. TestOperator_WALEntryIntegrity_MalformedLine
// -----------------------------------------------------------------------------

// TestOperator_WALEntryIntegrity_MalformedLine creates a WAL file with 3 valid
// entries and 1 malformed line. RecoverUnsubmittedBlocks should recover the
// valid entries and skip the malformed one (graceful degradation).
func TestOperator_WALEntryIntegrity_MalformedLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	line1 := `{"height":600001,"block_hash":"0000000000000000000aaa1111111111111111111111111111111111111111a601","block_hex":"h1","status":"pending"}`
	line2 := `THIS IS NOT VALID JSON AT ALL`
	line3 := `{"height":600002,"block_hash":"0000000000000000000bbb2222222222222222222222222222222222222222b602","block_hex":"h2","status":"pending"}`
	line4 := `{"height":600003,"block_hash":"0000000000000000000ccc3333333333333333333333333333333333333333c603","block_hex":"h3","status":"pending"}`

	content := line1 + "\n" + line2 + "\n" + line3 + "\n" + line4 + "\n"
	walFile := filepath.Join(dir, "block_wal_2026-01-30.jsonl")
	if err := os.WriteFile(walFile, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 3 {
		t.Fatalf("Expected 3 valid entries (skipping malformed line), got %d", len(unsubmitted))
	}

	heights := map[uint64]bool{}
	for _, e := range unsubmitted {
		heights[e.Height] = true
	}
	for _, h := range []uint64{600001, 600002, 600003} {
		if !heights[h] {
			t.Errorf("Missing recovered entry for height %d", h)
		}
	}
}

// -----------------------------------------------------------------------------
// 8. TestOperator_WALEntryIntegrity_EmptyFile
// -----------------------------------------------------------------------------

// TestOperator_WALEntryIntegrity_EmptyFile creates an empty WAL file and verifies
// RecoverUnsubmittedBlocks returns an empty slice with no error.
func TestOperator_WALEntryIntegrity_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	walFile := filepath.Join(dir, "block_wal_2026-01-30.jsonl")
	if err := os.WriteFile(walFile, []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed on empty file: %v", err)
	}

	if len(unsubmitted) != 0 {
		t.Errorf("Expected 0 entries from empty WAL file, got %d", len(unsubmitted))
	}
}

// -----------------------------------------------------------------------------
// 9. TestOperator_WALEntryIntegrity_EmptyLines
// -----------------------------------------------------------------------------

// TestOperator_WALEntryIntegrity_EmptyLines creates a WAL file with valid entries
// interspersed with blank lines. Recovery should skip blanks and return only
// valid entries.
func TestOperator_WALEntryIntegrity_EmptyLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	line1 := `{"height":700001,"block_hash":"0000000000000000000aaa1111111111111111111111111111111111111111a701","block_hex":"h1","status":"pending"}`
	line2 := `{"height":700002,"block_hash":"0000000000000000000bbb2222222222222222222222222222222222222222b702","block_hex":"h2","status":"pending"}`
	line3 := `{"height":700003,"block_hash":"0000000000000000000ccc3333333333333333333333333333333333333333c703","block_hex":"h3","status":"pending"}`

	// Intersperse with empty lines and whitespace-only lines.
	content := "\n" + line1 + "\n\n\n" + line2 + "\n\n" + line3 + "\n\n"
	walFile := filepath.Join(dir, "block_wal_2026-01-30.jsonl")
	if err := os.WriteFile(walFile, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 3 {
		t.Fatalf("Expected 3 entries (skipping blank lines), got %d", len(unsubmitted))
	}

	heights := map[uint64]bool{}
	for _, e := range unsubmitted {
		heights[e.Height] = true
	}
	for _, h := range []uint64{700001, 700002, 700003} {
		if !heights[h] {
			t.Errorf("Missing recovered entry for height %d", h)
		}
	}
}

// -----------------------------------------------------------------------------
// 10. TestOperator_BlockLifecycleStateMachine
// -----------------------------------------------------------------------------

// TestOperator_BlockLifecycleStateMachine is a table-driven test of valid block
// status transitions. Each transition is written to the WAL and verified.
func TestOperator_BlockLifecycleStateMachine(t *testing.T) {
	t.Parallel()

	transitions := []struct {
		name       string
		fromStatus string
		toStatus   string
	}{
		{"pending_to_submitted", "pending", "submitted"},
		{"pending_to_build_failed", "pending", "build_failed"},
		{"submitted_to_accepted", "submitted", "accepted"},
		{"submitted_to_rejected", "submitted", "rejected"},
	}

	for _, tc := range transitions {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			logger := zap.NewNop()

			wal, err := NewBlockWAL(dir, logger)
			if err != nil {
				t.Fatalf("NewBlockWAL failed: %v", err)
			}

			blockHash := fmt.Sprintf("00000000000000000000%s%s0000000000000000000000000000000000000000",
				tc.fromStatus[:4], tc.toStatus[:4])

			// Phase 1: Write entry with initial status.
			entry := &BlockWALEntry{
				Height:    800000,
				BlockHash: blockHash,
				BlockHex:  "deadbeef",
				Status:    tc.fromStatus,
			}
			if err := wal.LogBlockFound(entry); err != nil {
				t.Fatalf("LogBlockFound (%s) failed: %v", tc.fromStatus, err)
			}

			// Phase 2: Transition to new status via LogSubmissionResult.
			entry.Status = tc.toStatus
			if tc.toStatus == "rejected" {
				entry.RejectReason = "duplicate-inconclusive"
			}
			if tc.toStatus == "build_failed" {
				entry.SubmitError = "invalid coinbase assembly"
			}
			if err := wal.LogSubmissionResult(entry); err != nil {
				t.Fatalf("LogSubmissionResult (%s) failed: %v", tc.toStatus, err)
			}

			if err := wal.Close(); err != nil {
				t.Fatalf("WAL Close failed: %v", err)
			}

			// Read WAL file and verify both entries are present and valid.
			data, err := os.ReadFile(wal.FilePath())
			if err != nil {
				t.Fatalf("ReadFile failed: %v", err)
			}

			lines := splitLines(data)
			var entries []BlockWALEntry
			for _, line := range lines {
				if len(line) == 0 {
					continue
				}
				var e BlockWALEntry
				if err := json.Unmarshal(line, &e); err != nil {
					t.Fatalf("JSON unmarshal failed: %v", err)
				}
				entries = append(entries, e)
			}

			if len(entries) != 2 {
				t.Fatalf("Expected 2 WAL entries (before + after transition), got %d", len(entries))
			}

			// First entry: initial status.
			if entries[0].Status != tc.fromStatus {
				t.Errorf("First entry status: got %q, want %q", entries[0].Status, tc.fromStatus)
			}

			// Second entry: transitioned status.
			if entries[1].Status != tc.toStatus {
				t.Errorf("Second entry status: got %q, want %q", entries[1].Status, tc.toStatus)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 11. TestOperator_WALFileRotation
// -----------------------------------------------------------------------------

// TestOperator_WALFileRotation verifies WAL files use date-stamped names.
// Creates a WAL and confirms the filename matches the block_wal_YYYY-MM-DD.jsonl
// pattern using today's date.
func TestOperator_WALFileRotation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	filename := filepath.Base(wal.FilePath())
	today := time.Now().Format("2006-01-02")
	expected := fmt.Sprintf("block_wal_%s.jsonl", today)

	if filename != expected {
		t.Errorf("WAL filename mismatch: got %q, want %q", filename, expected)
	}

	// Also verify the pattern matches the general format.
	if !strings.HasPrefix(filename, "block_wal_") {
		t.Errorf("WAL filename missing prefix 'block_wal_': %q", filename)
	}
	if !strings.HasSuffix(filename, ".jsonl") {
		t.Errorf("WAL filename missing suffix '.jsonl': %q", filename)
	}
}

// -----------------------------------------------------------------------------
// 12. TestOperator_WALFileLock_PreventsDualInstance
// -----------------------------------------------------------------------------

// TestOperator_WALFileLock_PreventsDualInstance creates a WAL in a directory.
// Without closing it, attempts to create a second WAL in the same directory.
// The second creation should fail because the file lock prevents dual instance.
// After closing the first, the second creation should succeed.
func TestOperator_WALFileLock_PreventsDualInstance(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	// First instance acquires the lock.
	wal1, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("First NewBlockWAL failed: %v", err)
	}

	// Second instance should fail because of file lock.
	wal2, err := NewBlockWAL(dir, logger)
	if err == nil {
		// If it unexpectedly succeeded, clean up.
		wal2.Close()
		wal1.Close()
		t.Fatal("Second NewBlockWAL should have failed due to file lock, but succeeded")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("Expected lock-related error, got: %v", err)
	}

	// Close first instance to release the lock.
	if err := wal1.Close(); err != nil {
		t.Fatalf("First WAL Close failed: %v", err)
	}

	// Now the second instance should succeed.
	wal3, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("Third NewBlockWAL (after first closed) failed: %v", err)
	}
	defer wal3.Close()

	// Verify it is functional by writing an entry.
	entry := makeTestEntry(999001, "0000000000000000000lock111111111111111111111111111111111111111lock1", "")
	if err := wal3.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound on re-acquired WAL failed: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 13. TestOperator_WALRecovery_LargeScale
// -----------------------------------------------------------------------------

// TestOperator_WALRecovery_LargeScale creates a WAL file with 1000 entries
// (mixed statuses) and verifies RecoverUnsubmittedBlocks and RecoverSubmittedBlocks
// return the correct counts. Must complete in under 5 seconds.
func TestOperator_WALRecovery_LargeScale(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Build 1000 entries with deterministic status distribution:
	// - 400 pending (i % 5 == 0 or 1)
	// - 200 submitted (i % 5 == 2)
	// - 200 accepted (i % 5 == 3)
	// - 200 rejected (i % 5 == 4)
	var entries []BlockWALEntry
	var expectedPending, expectedSubmitted, expectedAccepted int

	for i := 0; i < 1000; i++ {
		var status string
		switch i % 5 {
		case 0, 1:
			status = "pending"
			expectedPending++
		case 2:
			status = "submitted"
			expectedSubmitted++
		case 3:
			status = "accepted"
			expectedAccepted++
		case 4:
			status = "rejected"
		}

		hash := fmt.Sprintf("00000000000000000000%04d00000000000000000000000000000000%04d0000", i, i)
		entries = append(entries, BlockWALEntry{
			Height:    uint64(1000000 + i),
			BlockHash: hash,
			BlockHex:  fmt.Sprintf("hex_%04d", i),
			Status:    status,
		})
	}

	walFile := filepath.Join(dir, "block_wal_2026-01-30.jsonl")
	writeWALEntriesFile(t, walFile, entries)

	start := time.Now()

	// Recover unsubmitted (pending).
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Recover submitted (submitted + accepted).
	submitted, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}

	elapsed := time.Since(start)

	// Performance check.
	if elapsed > 5*time.Second {
		t.Errorf("Large-scale recovery took %v, exceeds 5s threshold", elapsed)
	}

	// Verify counts.
	if len(unsubmitted) != expectedPending {
		t.Errorf("Unsubmitted count: got %d, want %d", len(unsubmitted), expectedPending)
	}

	expectedSubmittedTotal := expectedSubmitted + expectedAccepted
	if len(submitted) != expectedSubmittedTotal {
		t.Errorf("Submitted count: got %d, want %d (submitted=%d + accepted=%d)",
			len(submitted), expectedSubmittedTotal, expectedSubmitted, expectedAccepted)
	}

	t.Logf("Large-scale recovery: %d entries processed in %v", len(entries), elapsed)
}

// -----------------------------------------------------------------------------
// 14. TestOperator_SplitLines_Comprehensive
// -----------------------------------------------------------------------------

// TestOperator_SplitLines_Comprehensive tests splitLines with various edge cases
// including empty input, single lines, LF, CRLF, trailing newlines, and
// multiple empty lines.
func TestOperator_SplitLines_Comprehensive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty_input",
			input:    "",
			expected: nil,
		},
		{
			name:     "single_line_no_newline",
			input:    "hello",
			expected: []string{"hello"},
		},
		{
			name:     "single_line_with_LF",
			input:    "hello\n",
			expected: []string{"hello"},
		},
		{
			name:     "multiple_lines_LF",
			input:    "line1\nline2\nline3\n",
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "multiple_lines_CRLF",
			input:    "line1\r\nline2\r\nline3\r\n",
			expected: []string{"line1", "line2", "line3"},
		},
		{
			name:     "trailing_newline_no_empty_last",
			input:    "a\nb\n",
			expected: []string{"a", "b"},
		},
		{
			name:     "no_trailing_newline",
			input:    "a\nb",
			expected: []string{"a", "b"},
		},
		{
			name:     "multiple_empty_lines",
			input:    "a\n\n\nb\n",
			expected: []string{"a", "", "", "b"},
		},
		{
			name:     "only_newlines",
			input:    "\n\n\n",
			expected: []string{"", "", ""},
		},
		{
			name:     "mixed_LF_CRLF",
			input:    "line1\nline2\r\nline3\n",
			expected: []string{"line1", "line2", "line3"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := splitLines([]byte(tc.input))

			// Convert to strings for comparison.
			var got []string
			for _, line := range result {
				got = append(got, string(line))
			}

			if len(got) != len(tc.expected) {
				t.Fatalf("splitLines(%q): got %d lines %v, want %d lines %v",
					tc.input, len(got), got, len(tc.expected), tc.expected)
			}

			for i := range tc.expected {
				if got[i] != tc.expected[i] {
					t.Errorf("splitLines(%q)[%d]: got %q, want %q",
						tc.input, i, got[i], tc.expected[i])
				}
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 15. TestOperator_WALEntry_AllStatusValues
// -----------------------------------------------------------------------------

// TestOperator_WALEntry_AllStatusValues creates entries with every valid status
// and verifies JSON marshaling preserves each status exactly.
func TestOperator_WALEntry_AllStatusValues(t *testing.T) {
	t.Parallel()

	statuses := []string{"pending", "submitted", "accepted", "rejected", "build_failed"}

	for _, status := range statuses {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			entry := BlockWALEntry{
				Height:    900000,
				BlockHash: fmt.Sprintf("000000000000000000000000%s000000000000000000000000000000000000", status),
				BlockHex:  "deadbeef",
				Status:    status,
			}

			data, err := json.Marshal(entry)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}

			var recovered BlockWALEntry
			if err := json.Unmarshal(data, &recovered); err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}

			if recovered.Status != status {
				t.Errorf("Status mismatch after JSON roundtrip: got %q, want %q", recovered.Status, status)
			}

			// Verify status appears literally in JSON string.
			if !strings.Contains(string(data), fmt.Sprintf(`"status":"%s"`, status)) {
				t.Errorf("JSON output does not contain expected status field: %s", string(data))
			}
		})
	}
}
