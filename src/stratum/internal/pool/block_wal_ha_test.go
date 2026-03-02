// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// WAL HA RECOVERY TEST SUITE
// =============================================================================
//
// Tests covering WAL (Write-Ahead Log) HA recovery features for the block WAL.
// These tests exercise crash recovery, durability guarantees, HA reconciliation
// scenarios, concurrent safety, and edge cases.
//
// Test categories:
//   1. WAL Recovery Tests (1-8)
//   2. WAL Durability Tests (9-12)
//   3. WAL HA Scenarios (13-18)
//   4. Edge Cases (19-20)

// =============================================================================
// WAL RECOVERY TESTS
// =============================================================================

// TestBlockWAL_Recovery_PendingBlocks verifies that RecoverUnsubmittedBlocks
// finds entries with "pending" status. This is the most common recovery
// scenario: block was logged but submission never started.
func TestBlockWAL_Recovery_PendingBlocks(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	entry := &BlockWALEntry{
		Height:        850001,
		BlockHash:     "0000000000000000000aaa1111111111222222222222333333333333444444aa",
		PrevHash:      "0000000000000000000fff0000000000111111111111222222222222333333ff",
		BlockHex:      "02000000deadbeefcafebabe01234567890abcdef",
		MinerAddress:  "bc1qpendingtest",
		WorkerName:    "rig_pending_01",
		JobID:         "job_pending_001",
		CoinbaseValue: 312500000,
		Status:        "pending",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block, got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]
	if recovered.Status != "pending" {
		t.Errorf("Expected status 'pending', got %q", recovered.Status)
	}
	if recovered.BlockHash != entry.BlockHash {
		t.Errorf("BlockHash mismatch: got %q, want %q", recovered.BlockHash, entry.BlockHash)
	}
	if recovered.BlockHex != entry.BlockHex {
		t.Errorf("BlockHex mismatch: recovered entry missing block data for resubmission")
	}
	if recovered.Height != 850001 {
		t.Errorf("Height mismatch: got %d, want 850001", recovered.Height)
	}
	if recovered.MinerAddress != entry.MinerAddress {
		t.Errorf("MinerAddress mismatch: got %q, want %q", recovered.MinerAddress, entry.MinerAddress)
	}
}

// TestBlockWAL_Recovery_SubmittingBlocks verifies that RecoverUnsubmittedBlocks
// finds entries with "submitting" status. This represents a crash during the
// submitblock RPC call -- the pre-submission WAL write succeeded but the
// post-submission result was never written.
func TestBlockWAL_Recovery_SubmittingBlocks(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	entry := &BlockWALEntry{
		Height:        850002,
		BlockHash:     "0000000000000000000bbb2222222222333333333333444444444444555555bb",
		PrevHash:      "0000000000000000000eee1111111111222222222222333333333333444444ee",
		BlockHex:      "02000000cafebabe0123456789abcdef01020304",
		MinerAddress:  "bc1qsubmittingtest",
		WorkerName:    "rig_submitting_01",
		JobID:         "job_submitting_001",
		CoinbaseValue: 625000000,
		Status:        "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Simulate crash: close WAL without writing post-submission result.
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block (submitting status), got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]
	if recovered.Status != "submitting" {
		t.Errorf("Expected status 'submitting', got %q", recovered.Status)
	}
	if recovered.BlockHex == "" {
		t.Fatal("BLOCK LOSS: Recovered entry has empty BlockHex -- cannot resubmit")
	}
	if recovered.BlockHex != entry.BlockHex {
		t.Errorf("BlockHex corrupted during recovery")
	}
}

// TestBlockWAL_Recovery_SubmittedBlocks verifies that RecoverSubmittedBlocks
// finds entries with "submitted" and "accepted" statuses. These are blocks
// that were successfully sent to the daemon and may need DB reconciliation.
func TestBlockWAL_Recovery_SubmittedBlocks(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Write a "submitted" entry
	submittedEntry := &BlockWALEntry{
		Height:        850003,
		BlockHash:     "0000000000000000000ccc3333333333444444444444555555555555666666cc",
		BlockHex:      "submittedblockdata",
		MinerAddress:  "bc1qsubmittedtest",
		CoinbaseValue: 312500000,
		Status:        "submitted",
	}
	if err := wal.LogBlockFound(submittedEntry); err != nil {
		t.Fatalf("LogBlockFound (submitted) failed: %v", err)
	}

	// Write an "accepted" entry
	acceptedEntry := &BlockWALEntry{
		Height:        850004,
		BlockHash:     "0000000000000000000ddd4444444444555555555555666666666666777777dd",
		BlockHex:      "acceptedblockdata",
		MinerAddress:  "bc1qacceptedtest",
		CoinbaseValue: 312500000,
		Status:        "accepted",
	}
	if err := wal.LogBlockFound(acceptedEntry); err != nil {
		t.Fatalf("LogBlockFound (accepted) failed: %v", err)
	}
	wal.Close()

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
		if entry.Status != "submitted" && entry.Status != "accepted" {
			t.Errorf("Unexpected status %q in RecoverSubmittedBlocks results", entry.Status)
		}
	}
	if !hashes[submittedEntry.BlockHash] {
		t.Error("Missing 'submitted' entry in results")
	}
	if !hashes[acceptedEntry.BlockHash] {
		t.Error("Missing 'accepted' entry in results")
	}

	// Verify these blocks do NOT appear in unsubmitted recovery
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == submittedEntry.BlockHash || entry.BlockHash == acceptedEntry.BlockHash {
			t.Errorf("Submitted/accepted block should NOT appear in unsubmitted results")
		}
	}
}

// TestBlockWAL_Recovery_LatestEntryWins verifies that when multiple entries
// exist for the same block hash, the latest entry's status is used for
// recovery decisions. This is critical for the pre/post submission WAL pattern.
func TestBlockWAL_Recovery_LatestEntryWins(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "0000000000000000000eee5555555555666666666666777777777777888888ee"

	// Write escalating statuses for the same block
	statuses := []string{"pending", "submitting", "submitted"}
	for _, status := range statuses {
		entry := &BlockWALEntry{
			Height:    850005,
			BlockHash: blockHash,
			BlockHex:  "escalating_block_hex",
			Status:    status,
		}
		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound(%s) failed: %v", status, err)
		}
	}
	wal.Close()

	// Latest status is "submitted" -- should NOT appear in unsubmitted
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block should NOT be in unsubmitted list -- latest status is 'submitted'")
		}
	}

	// It SHOULD appear in submitted recovery
	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}

	found := false
	for _, entry := range submitted {
		if entry.BlockHash == blockHash {
			found = true
			if entry.Status != "submitted" {
				t.Errorf("Expected latest status 'submitted', got %q", entry.Status)
			}
		}
	}
	if !found {
		t.Error("Block with latest status 'submitted' should appear in RecoverSubmittedBlocks")
	}
}

// TestBlockWAL_Recovery_SkipRejected verifies that rejected and build_failed
// blocks do not appear in the unsubmitted recovery list. These blocks should
// not be automatically resubmitted.
func TestBlockWAL_Recovery_SkipRejected(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Write a rejected block
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:       850010,
		BlockHash:    "0000000000000000000111aaaaaaaaaabbbbbbbbbbbbccccccccccccdddddd11",
		BlockHex:     "rejected_block_hex",
		Status:       "rejected",
		RejectReason: "high-hash",
	}); err != nil {
		t.Fatalf("LogBlockFound (rejected) failed: %v", err)
	}

	// Write a build_failed block
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:      850011,
		BlockHash:   "0000000000000000000222bbbbbbbbbbbccccccccccccddddddddddddeeeeee22",
		Status:      "build_failed",
		SubmitError: "invalid coinbase1",
		CoinBase1:   "01000000",
	}); err != nil {
		t.Fatalf("LogBlockFound (build_failed) failed: %v", err)
	}

	// Write a pending block that SHOULD be recovered
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    850012,
		BlockHash: "0000000000000000000333cccccccccccddddddddddddeeeeeeeeeeeefffff33",
		BlockHex:  "recoverable_hex",
		Status:    "pending",
	}); err != nil {
		t.Fatalf("LogBlockFound (pending) failed: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block (pending only), got %d", len(unsubmitted))
	}

	if unsubmitted[0].Height != 850012 {
		t.Errorf("Wrong block recovered: height=%d, want 850012", unsubmitted[0].Height)
	}
	if unsubmitted[0].Status != "pending" {
		t.Errorf("Expected status 'pending', got %q", unsubmitted[0].Status)
	}
}

// TestBlockWAL_Recovery_EmptyDirectory verifies that RecoverUnsubmittedBlocks
// and RecoverSubmittedBlocks return empty slices (not errors) when there are
// no WAL files in the directory.
func TestBlockWAL_Recovery_EmptyDirectory(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks on empty dir failed: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("Expected 0 unsubmitted blocks from empty dir, got %d", len(unsubmitted))
	}

	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks on empty dir failed: %v", err)
	}
	if len(submitted) != 0 {
		t.Errorf("Expected 0 submitted blocks from empty dir, got %d", len(submitted))
	}
}

// TestBlockWAL_Recovery_MalformedEntries verifies that malformed JSON lines
// are skipped gracefully during recovery without preventing valid entries
// from being recovered. This covers partial writes due to truncation.
func TestBlockWAL_Recovery_MalformedEntries(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	content := strings.Join([]string{
		`THIS_IS_NOT_VALID_JSON`,
		`{"height":860001,"block_hash":"0000000000000000000aaa1111111111222222222222333333333333444444a1","block_hex":"valid_block_1","status":"pending"}`,
		`{truncated_json_entry`,
		``,
		`{"height":860002,"block_hash":"0000000000000000000bbb2222222222333333333333444444444444555555b2","block_hex":"valid_block_2","status":"submitting"}`,
		`{"incomplete":`,
		`{"height":860003,"block_hash":"0000000000000000000ccc3333333333444444444444555555555555666666c3","block_hex":"valid_block_3","status":"submitted"}`,
	}, "\n") + "\n"

	walFile := filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl")
	if err := os.WriteFile(walFile, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Recovery should succeed and find valid entries despite malformed lines
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 valid unsubmitted blocks despite malformed lines, got %d", len(unsubmitted))
	}

	heights := map[uint64]bool{}
	for _, entry := range unsubmitted {
		heights[entry.Height] = true
	}
	if !heights[860001] {
		t.Error("Missing pending block (height 860001) from recovery")
	}
	if !heights[860002] {
		t.Error("Missing submitting block (height 860002) from recovery")
	}

	// RecoverSubmittedBlocks should also skip malformed lines
	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 1 {
		t.Fatalf("Expected 1 submitted block despite malformed lines, got %d", len(submitted))
	}
	if submitted[0].Height != 860003 {
		t.Errorf("Expected submitted block height 860003, got %d", submitted[0].Height)
	}
}

// TestBlockWAL_Recovery_MultipleFiles verifies that recovery aggregates entries
// across multiple date-rotated WAL files. The WAL uses daily file rotation,
// so recovery must scan all block_wal_*.jsonl files.
func TestBlockWAL_Recovery_MultipleFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	files := map[string]string{
		"block_wal_2026-01-27.jsonl": `{"height":870001,"block_hash":"0000000000000000000aaa1111111111222222222222333333333333444444a1","block_hex":"day1_block","status":"pending"}`,
		"block_wal_2026-01-28.jsonl": `{"height":870002,"block_hash":"0000000000000000000bbb2222222222333333333333444444444444555555b2","block_hex":"day2_block","status":"submitted"}`,
		"block_wal_2026-01-29.jsonl": `{"height":870003,"block_hash":"0000000000000000000ccc3333333333444444444444555555555555666666c3","block_hex":"day3_block","status":"submitting"}`,
		"block_wal_2026-01-30.jsonl": `{"height":870004,"block_hash":"0000000000000000000ddd4444444444555555555555666666666666777777d4","block_hex":"day4_block","status":"accepted"}`,
	}

	for filename, content := range files {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte(content+"\n"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", filename, err)
		}
	}

	// RecoverUnsubmittedBlocks: should find pending (day1) + submitting (day3)
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 unsubmitted blocks from multi-file recovery, got %d", len(unsubmitted))
	}

	unsubHeights := map[uint64]bool{}
	for _, entry := range unsubmitted {
		unsubHeights[entry.Height] = true
	}
	if !unsubHeights[870001] {
		t.Error("Missing pending block from day 1 (height 870001)")
	}
	if !unsubHeights[870003] {
		t.Error("Missing submitting block from day 3 (height 870003)")
	}

	// RecoverSubmittedBlocks: should find submitted (day2) + accepted (day4)
	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("Expected 2 submitted blocks from multi-file recovery, got %d", len(submitted))
	}

	subHeights := map[uint64]bool{}
	for _, entry := range submitted {
		subHeights[entry.Height] = true
	}
	if !subHeights[870002] {
		t.Error("Missing submitted block from day 2 (height 870002)")
	}
	if !subHeights[870004] {
		t.Error("Missing accepted block from day 4 (height 870004)")
	}
}

// =============================================================================
// WAL DURABILITY TESTS
// =============================================================================

// TestBlockWAL_Write_ImmediateSync verifies that LogBlockFound calls fsync,
// ensuring data is immediately readable from disk after each write.
// This is the durability guarantee that prevents data loss on power failure.
func TestBlockWAL_Write_ImmediateSync(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	for i := 0; i < 5; i++ {
		entry := &BlockWALEntry{
			Height:    uint64(880000 + i),
			BlockHash: fmt.Sprintf("%064x", i+500),
			BlockHex:  fmt.Sprintf("fsync_test_block_%d", i),
			Status:    "pending",
		}

		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound #%d failed: %v", i, err)
		}

		// Immediately verify data is on disk (fsync should guarantee this)
		data, err := os.ReadFile(wal.FilePath())
		if err != nil {
			t.Fatalf("ReadFile after write #%d failed: %v", i, err)
		}

		lines := splitLines(data)
		nonEmpty := 0
		for _, line := range lines {
			if len(line) > 0 {
				nonEmpty++
			}
		}

		expected := i + 1
		if nonEmpty != expected {
			t.Errorf("After write #%d: expected %d entries on disk, got %d (fsync not durable!)",
				i, expected, nonEmpty)
		}
	}
}

// TestBlockWAL_Write_AppendOnly verifies that multiple entries written to the
// WAL do not overwrite each other. The WAL is append-only by design.
func TestBlockWAL_Write_AppendOnly(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	const numEntries = 10
	for i := 0; i < numEntries; i++ {
		entry := &BlockWALEntry{
			Height:    uint64(890000 + i),
			BlockHash: fmt.Sprintf("%064x", i+600),
			BlockHex:  fmt.Sprintf("append_test_%d", i),
			Status:    "pending",
		}
		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound #%d failed: %v", i, err)
		}
	}

	// Read back and verify all entries are present
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			nonEmpty++
		}
	}

	if nonEmpty != numEntries {
		t.Errorf("Expected %d entries (append-only), got %d -- entries may have been overwritten",
			numEntries, nonEmpty)
	}

	// Verify each entry has the expected height (ordered by write time)
	idx := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("JSON unmarshal failed for line %d: %v", idx, err)
			continue
		}
		expectedHeight := uint64(890000 + idx)
		if entry.Height != expectedHeight {
			t.Errorf("Entry %d: expected height %d, got %d", idx, expectedHeight, entry.Height)
		}
		idx++
	}
}

// TestBlockWAL_Write_JSONLFormat verifies that each entry is valid JSON on its
// own line, conforming to the JSONL (JSON Lines) format specification. This
// ensures entries can be parsed individually during recovery.
func TestBlockWAL_Write_JSONLFormat(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entries := []*BlockWALEntry{
		{
			Height:    900001,
			BlockHash: fmt.Sprintf("%064x", 701),
			BlockHex:  "jsonl_test_1",
			Status:    "pending",
		},
		{
			Height:    900002,
			BlockHash: fmt.Sprintf("%064x", 702),
			BlockHex:  "jsonl_test_2",
			Status:    "submitting",
		},
		{
			Height:    900003,
			BlockHash: fmt.Sprintf("%064x", 703),
			BlockHex:  "jsonl_test_3",
			Status:    "submitted",
		},
	}

	for _, entry := range entries {
		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound failed: %v", err)
		}
	}

	// Read raw file content
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	validCount := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Each line must be valid JSON
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("Invalid JSON on line: %v (content: %s)", err, string(line[:min(80, len(line))]))
			continue
		}

		// Each line must NOT contain embedded newlines (JSONL requirement)
		if strings.Contains(string(line), "\n") {
			t.Errorf("JSONL violation: line contains embedded newline")
		}

		validCount++
	}

	if validCount != len(entries) {
		t.Errorf("Expected %d valid JSONL entries, got %d", len(entries), validCount)
	}
}

// TestBlockWAL_LogSubmissionResult_UpdatesStatus verifies that
// LogSubmissionResult populates the SubmittedAt field with a valid timestamp,
// and the entry is written to the WAL with the updated status.
func TestBlockWAL_LogSubmissionResult_UpdatesStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:        910001,
		BlockHash:     "0000000000000000000aaa1111111111222222222222333333333333444444a1",
		BlockHex:      "submission_result_test",
		MinerAddress:  "bc1qresulttest",
		CoinbaseValue: 312500000,
		Status:        "submitted",
	}

	before := time.Now()
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}

	// Verify SubmittedAt was populated on the entry object
	if entry.SubmittedAt == "" {
		t.Fatal("LogSubmissionResult did not set SubmittedAt")
	}

	// Parse and validate the timestamp
	submittedAt, err := time.Parse(time.RFC3339Nano, entry.SubmittedAt)
	if err != nil {
		t.Fatalf("Failed to parse SubmittedAt %q: %v", entry.SubmittedAt, err)
	}
	after := time.Now()

	if submittedAt.Before(before) || submittedAt.After(after) {
		t.Errorf("SubmittedAt %v is outside expected range [%v, %v]", submittedAt, before, after)
	}

	// Verify the entry was written to the WAL file with SubmittedAt
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("WAL file has no entries after LogSubmissionResult")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}
	if recovered.SubmittedAt == "" {
		t.Error("SubmittedAt not persisted in WAL file")
	}
	if recovered.Status != "submitted" {
		t.Errorf("Expected status 'submitted' in WAL, got %q", recovered.Status)
	}
}

// =============================================================================
// WAL HA SCENARIOS
// =============================================================================

// TestBlockWAL_HAReconciliation_FindsMissing verifies that
// RecoverSubmittedBlocks returns blocks for DB reconciliation. In an HA
// scenario, a failover may cause the DB insert to be missed even though
// the block was successfully submitted. The WAL provides the authoritative
// record for reconciliation.
func TestBlockWAL_HAReconciliation_FindsMissing(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Simulate: block was submitted successfully to daemon but DB insert
	// was lost during HA failover. The WAL is the only record.
	entry := &BlockWALEntry{
		Height:        920001,
		BlockHash:     "0000000000000000000aaa1111111111222222222222333333333333444444a1",
		PrevHash:      "0000000000000000000fff0000000000111111111111222222222222333333ff",
		BlockHex:      "ha_reconciliation_block",
		MinerAddress:  "bc1qhareconcile",
		WorkerName:    "ha_worker_01",
		JobID:         "job_ha_001",
		CoinbaseValue: 312500000,
		Status:        "submitted",
	}

	// Write the pre-submission entry
	preEntry := *entry
	preEntry.Status = "submitting"
	if err := wal.LogBlockFound(&preEntry); err != nil {
		t.Fatalf("LogBlockFound (pre) failed: %v", err)
	}

	// Write the post-submission result
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}
	wal.Close()

	// On HA failover restart, RecoverSubmittedBlocks provides blocks for reconciliation
	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}

	if len(submitted) != 1 {
		t.Fatalf("Expected 1 submitted block for reconciliation, got %d", len(submitted))
	}

	recovered := submitted[0]
	if recovered.BlockHash != entry.BlockHash {
		t.Errorf("BlockHash mismatch in reconciliation")
	}
	if recovered.MinerAddress != entry.MinerAddress {
		t.Errorf("MinerAddress mismatch: got %q, want %q", recovered.MinerAddress, entry.MinerAddress)
	}
	if recovered.CoinbaseValue != entry.CoinbaseValue {
		t.Errorf("CoinbaseValue mismatch: got %d, want %d", recovered.CoinbaseValue, entry.CoinbaseValue)
	}
	if recovered.SubmittedAt == "" {
		t.Error("SubmittedAt should be populated for reconciliation entries")
	}
}

// TestBlockWAL_CrashBeforeSubmit verifies the pre-submission crash scenario:
// block is logged as "pending", then the process crashes (simulated by closing
// the WAL). On recovery, the block must be found.
func TestBlockWAL_CrashBeforeSubmit(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "0000000000000000000bbb2222222222333333333333444444444444555555b2"
	entry := &BlockWALEntry{
		Height:        930001,
		BlockHash:     blockHash,
		PrevHash:      "0000000000000000000aaa1111111111222222222222333333333333444444a1",
		BlockHex:      "crash_before_submit_hex_data_with_full_block_content",
		MinerAddress:  "bc1qcrashbeforesubmit",
		WorkerName:    "rig_crash_01",
		JobID:         "job_crash_before_001",
		CoinbaseValue: 312500000,
		Status:        "pending",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Simulate crash: close the WAL abruptly.
	// Because LogBlockFound calls fsync, data should be durable.
	wal.Close()

	// Simulate restart: recover from WAL
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block after pre-submission crash, got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]
	if recovered.BlockHash != blockHash {
		t.Errorf("BlockHash mismatch after recovery")
	}
	if recovered.BlockHex != entry.BlockHex {
		t.Errorf("BlockHex mismatch -- block data lost during crash recovery")
	}
	if recovered.Status != "pending" {
		t.Errorf("Expected status 'pending', got %q", recovered.Status)
	}
	if recovered.MinerAddress != entry.MinerAddress {
		t.Errorf("MinerAddress lost during recovery: got %q, want %q",
			recovered.MinerAddress, entry.MinerAddress)
	}
}

// TestBlockWAL_CrashDuringSubmit verifies the mid-submission crash scenario:
// block is logged as "submitting" (pre-submission WAL write), then the process
// crashes during the RPC call (simulated by closing the WAL without writing
// a post-submission result). Recovery must find the block.
func TestBlockWAL_CrashDuringSubmit(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "0000000000000000000ccc3333333333444444444444555555555555666666c3"
	entry := &BlockWALEntry{
		Height:        930002,
		BlockHash:     blockHash,
		PrevHash:      "0000000000000000000bbb2222222222333333333333444444444444555555b2",
		BlockHex:      "crash_during_submit_hex_data_with_full_block" + strings.Repeat("ab", 100),
		MinerAddress:  "bc1qcrashduringsubmit",
		WorkerName:    "rig_crash_02",
		JobID:         "job_crash_during_001",
		CoinbaseValue: 625000000,
		Status:        "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound (pre-submission) failed: %v", err)
	}

	// Simulate crash during RPC: close WAL without post-submission write.
	wal.Close()

	// Simulate restart: recover
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block after mid-submission crash, got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]
	if recovered.BlockHash != blockHash {
		t.Errorf("BlockHash mismatch after mid-submission crash recovery")
	}
	if recovered.BlockHex == "" {
		t.Fatal("BLOCK LOSS: BlockHex empty after mid-submission crash -- cannot resubmit")
	}
	if recovered.BlockHex != entry.BlockHex {
		t.Errorf("BlockHex corrupted during recovery")
	}
	if recovered.Status != "submitting" {
		t.Errorf("Expected status 'submitting', got %q", recovered.Status)
	}
}

// TestBlockWAL_FullLifecycle verifies the complete block lifecycle through
// the WAL: pending -> submitting -> accepted. At each stage, the recovery
// functions return the correct results.
func TestBlockWAL_FullLifecycle(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "0000000000000000000ddd4444444444555555555555666666666666777777d4"
	blockHex := "full_lifecycle_block_hex_data"

	// Phase 1: Block found -- status "pending"
	pendingEntry := &BlockWALEntry{
		Height:        940001,
		BlockHash:     blockHash,
		BlockHex:      blockHex,
		MinerAddress:  "bc1qlifecycle",
		WorkerName:    "lifecycle_rig",
		CoinbaseValue: 312500000,
	}
	if err := wal.LogBlockFound(pendingEntry); err != nil {
		t.Fatalf("LogBlockFound (pending) failed: %v", err)
	}
	// LogBlockFound should have set status to "pending"
	if pendingEntry.Status != "pending" {
		t.Fatalf("Expected Status 'pending' after LogBlockFound, got %q", pendingEntry.Status)
	}

	// Phase 2: Pre-submission -- status "submitting"
	submittingEntry := &BlockWALEntry{
		Height:        940001,
		BlockHash:     blockHash,
		BlockHex:      blockHex,
		MinerAddress:  "bc1qlifecycle",
		WorkerName:    "lifecycle_rig",
		CoinbaseValue: 312500000,
		Status:        "submitting",
	}
	if err := wal.LogBlockFound(submittingEntry); err != nil {
		t.Fatalf("LogBlockFound (submitting) failed: %v", err)
	}

	// Phase 3: Post-submission -- status "accepted"
	acceptedEntry := &BlockWALEntry{
		Height:        940001,
		BlockHash:     blockHash,
		BlockHex:      blockHex,
		MinerAddress:  "bc1qlifecycle",
		WorkerName:    "lifecycle_rig",
		CoinbaseValue: 312500000,
		Status:        "accepted",
	}
	if err := wal.LogSubmissionResult(acceptedEntry); err != nil {
		t.Fatalf("LogSubmissionResult (accepted) failed: %v", err)
	}
	wal.Close()

	// After full lifecycle, block should NOT be in unsubmitted
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block should NOT be unsubmitted after full lifecycle (latest status: accepted)")
		}
	}

	// Block SHOULD be in submitted recovery (for DB reconciliation)
	submitted, err := RecoverSubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks failed: %v", err)
	}

	found := false
	for _, entry := range submitted {
		if entry.BlockHash == blockHash {
			found = true
			if entry.Status != "accepted" {
				t.Errorf("Expected final status 'accepted', got %q", entry.Status)
			}
			if entry.BlockHex != blockHex {
				t.Errorf("BlockHex mismatch in lifecycle recovery")
			}
			if entry.SubmittedAt == "" {
				t.Error("SubmittedAt should be populated after LogSubmissionResult")
			}
		}
	}
	if !found {
		t.Error("Block not found in RecoverSubmittedBlocks after full lifecycle")
	}
}

// TestBlockWAL_ConcurrentWrites verifies that multiple goroutines writing
// entries to the same WAL simultaneously do not lose entries or corrupt
// the JSONL format. The WAL uses sync.Mutex for serialization.
func TestBlockWAL_ConcurrentWrites(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	const numGoroutines = 30
	var wg sync.WaitGroup
	var writeErrors atomic.Int32

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			entry := &BlockWALEntry{
				Height:     uint64(950000 + id),
				BlockHash:  fmt.Sprintf("%064x", id+800),
				BlockHex:   fmt.Sprintf("concurrent_block_%d", id),
				WorkerName: fmt.Sprintf("concurrent_worker_%d", id),
				Status:     "pending",
			}
			if err := wal.LogBlockFound(entry); err != nil {
				writeErrors.Add(1)
			}
		}(g)
	}

	wg.Wait()
	wal.Close()

	if writeErrors.Load() > 0 {
		t.Errorf("%d WAL write errors during concurrent access", writeErrors.Load())
	}

	// Read back and verify all entries survived
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	validCount := 0
	corruptCount := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			corruptCount++
			maxLen := len(line)
			if maxLen > 80 {
				maxLen = 80
			}
			t.Errorf("Corrupt JSON line from concurrent writes: %s", string(line[:maxLen]))
			continue
		}
		validCount++
	}

	if corruptCount > 0 {
		t.Errorf("BLOCK LOSS: %d corrupt WAL lines from concurrent writes", corruptCount)
	}
	if validCount != numGoroutines {
		t.Errorf("BLOCK LOSS: Expected %d WAL entries, got %d (%d entries lost!)",
			numGoroutines, validCount, numGoroutines-validCount)
	}
}

// TestBlockWAL_RawComponents_Preserved verifies that raw block construction
// components (CoinBase1, CoinBase2, ExtraNonce1, ExtraNonce2, Version, NBits,
// NTime, Nonce, TransactionData) survive a full WAL round-trip. These fields
// are critical for manual block reconstruction when buildFullBlock fails.
func TestBlockWAL_RawComponents_Preserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	original := &BlockWALEntry{
		Height:        960001,
		BlockHash:     "0000000000000000000aaa1111111111222222222222333333333333444444a1",
		PrevHash:      "0000000000000000000fff0000000000111111111111222222222222333333ff",
		BlockHex:      "", // Empty -- buildFullBlock failed
		MinerAddress:  "bc1qrawcomponents",
		WorkerName:    "raw_worker_01",
		JobID:         "job_raw_001",
		CoinbaseValue: 312500000,
		Status:        "build_failed",
		SubmitError:   "invalid coinbase1 hex encoding",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff04030ea00d",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		ExtraNonce1:   "aabbccdd",
		ExtraNonce2:   "11223344",
		Version:       "20000000",
		NBits:         "1a0377ae",
		NTime:         "64a1b2c3",
		Nonce:         "deadbeef",
		TransactionData: []string{
			"0100000001abcdef0000000000000000000000000000000000000000000000000000000000ffffffff",
			"0100000002fedcba0000000000000000000000000000000000000000000000000000000000ffffffff",
			"0100000003112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00ffffffff",
		},
	}

	if err := wal.LogBlockFound(original); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Read back from disk and verify every raw component field
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("WAL file has no entries")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Check every raw component field
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"CoinBase1", recovered.CoinBase1, original.CoinBase1},
		{"CoinBase2", recovered.CoinBase2, original.CoinBase2},
		{"ExtraNonce1", recovered.ExtraNonce1, original.ExtraNonce1},
		{"ExtraNonce2", recovered.ExtraNonce2, original.ExtraNonce2},
		{"Version", recovered.Version, original.Version},
		{"NBits", recovered.NBits, original.NBits},
		{"NTime", recovered.NTime, original.NTime},
		{"Nonce", recovered.Nonce, original.Nonce},
		{"Status", recovered.Status, "build_failed"},
		{"SubmitError", recovered.SubmitError, original.SubmitError},
		{"BlockHash", recovered.BlockHash, original.BlockHash},
		{"PrevHash", recovered.PrevHash, original.PrevHash},
		{"MinerAddress", recovered.MinerAddress, original.MinerAddress},
		{"WorkerName", recovered.WorkerName, original.WorkerName},
		{"JobID", recovered.JobID, original.JobID},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("BLOCK LOSS RISK: %s mismatch: got %q, want %q", c.name, c.got, c.want)
		}
	}

	if recovered.CoinbaseValue != original.CoinbaseValue {
		t.Errorf("CoinbaseValue mismatch: got %d, want %d", recovered.CoinbaseValue, original.CoinbaseValue)
	}

	if len(recovered.TransactionData) != len(original.TransactionData) {
		t.Fatalf("TransactionData count mismatch: got %d, want %d",
			len(recovered.TransactionData), len(original.TransactionData))
	}
	for i, tx := range recovered.TransactionData {
		if tx != original.TransactionData[i] {
			t.Errorf("TransactionData[%d] mismatch: got %q, want %q", i, tx, original.TransactionData[i])
		}
	}
}

// =============================================================================
// EDGE CASES
// =============================================================================

// TestBlockWAL_ShortBlockHash verifies that a block hash shorter than 16
// characters does not cause a panic in LogBlockFound. The implementation
// uses a hash preview for logging that truncates to 16 chars -- a short
// hash must be handled without index-out-of-range.
func TestBlockWAL_ShortBlockHash(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Test various short hash lengths, all under 16 chars
	shortHashes := []string{
		"a",
		"abcdef",
		"0000000000",
		"123456789012345", // exactly 15 chars
		"1234567890123456", // exactly 16 chars
	}

	for i, hash := range shortHashes {
		entry := &BlockWALEntry{
			Height:    uint64(970000 + i),
			BlockHash: hash,
			BlockHex:  fmt.Sprintf("short_hash_block_%d", i),
			Status:    "pending",
		}

		// This must not panic
		if err := wal.LogBlockFound(entry); err != nil {
			t.Errorf("LogBlockFound panicked or failed for short hash %q (len=%d): %v",
				hash, len(hash), err)
		}
	}

	// Verify all entries were written
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			nonEmpty++
		}
	}

	if nonEmpty != len(shortHashes) {
		t.Errorf("Expected %d entries for short hashes, got %d", len(shortHashes), nonEmpty)
	}

	// Verify recovery works with short hashes
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed with short hashes: %v", err)
	}
	if len(unsubmitted) != len(shortHashes) {
		t.Errorf("Expected %d recovered entries, got %d", len(shortHashes), len(unsubmitted))
	}
}

// TestBlockWAL_EmptyBlockHash verifies that an empty block hash is handled
// gracefully -- no panic in LogBlockFound, and the entry can be recovered.
// This covers the case where block hash computation fails but the block
// hex is still available for submission.
func TestBlockWAL_EmptyBlockHash(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:        980001,
		BlockHash:     "", // Empty hash
		BlockHex:      "block_with_empty_hash_but_valid_hex",
		MinerAddress:  "bc1qemptyhash",
		WorkerName:    "rig_empty_hash",
		CoinbaseValue: 312500000,
		Status:        "pending",
	}

	// This must not panic
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed for empty block hash: %v", err)
	}

	// Verify entry was written
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty != 1 {
		t.Fatalf("Expected 1 entry, got %d", nonEmpty)
	}

	// Verify the entry can be parsed back
	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if recovered.BlockHash != "" {
		t.Errorf("Expected empty BlockHash, got %q", recovered.BlockHash)
	}
	if recovered.BlockHex != entry.BlockHex {
		t.Errorf("BlockHex mismatch: got %q, want %q", recovered.BlockHex, entry.BlockHex)
	}

	// Recovery should find it (keyed by empty string in the map)
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	found := false
	for _, u := range unsubmitted {
		if u.BlockHash == "" && u.BlockHex == entry.BlockHex {
			found = true
		}
	}
	if !found {
		t.Error("Entry with empty block hash not recovered by RecoverUnsubmittedBlocks")
	}
}
