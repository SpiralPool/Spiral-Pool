// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// =============================================================================
// AUDIT TEST SUITE: Block WAL Gaps
// =============================================================================
// Tests covering gaps T1.2/T7.1, T3.4, T6.4, T9.4 from PARANOID_TEST_PLAN.md.
//
// T1.2/T7.1: Pre-submission WAL write with "submitting" status is recoverable.
// T3.4:      Concurrent WAL writes from multiple goroutines.
// T6.4:      RecoverUnsubmittedBlocks scans multiple WAL files across dates.
// T9.4:      WAL entry survives independently of DB insert outcome.

// -----------------------------------------------------------------------------
// T1.2/T7.1: Pre-Submission WAL Write ("submitting" status) Recovery
// -----------------------------------------------------------------------------

// TestPreSubmissionWAL_SubmittingStatusRecoverable verifies that a WAL entry
// with status "submitting" (written by the P0 audit fix before RPC call) is
// returned by RecoverUnsubmittedBlocks. This covers the crash-during-RPC
// scenario where the process is OOM-killed after the pre-submission WAL write
// but before the post-submission WAL write.
func TestPreSubmissionWAL_SubmittingStatusRecoverable(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Simulate pre-submission WAL write with "submitting" status.
	// This is exactly what handleBlock() now writes before calling
	// SubmitBlockWithVerification().
	entry := &BlockWALEntry{
		Height:        900001,
		BlockHash:     "0000000000000000000aaaa1111122222333344444555566667777888899990000",
		PrevHash:      "0000000000000000000ffff0000011111222223333344444555566667777888899",
		BlockHex:      "deadbeefcafe0123456789abcdef",
		MinerAddress:  "bc1qaudittest",
		WorkerName:    "rig1",
		JobID:         "job_presubmit",
		CoinbaseValue: 312500000,
		Status:        "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	// Simulate crash: no further WAL writes occurred.
	// RecoverUnsubmittedBlocks must find this "submitting" entry.
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block (submitting status), got %d", len(unsubmitted))
	}

	if unsubmitted[0].Status != "submitting" {
		t.Errorf("Expected status 'submitting', got %q", unsubmitted[0].Status)
	}
	if unsubmitted[0].BlockHex != entry.BlockHex {
		t.Errorf("BlockHex mismatch: recovered entry missing block data for resubmission")
	}
	if unsubmitted[0].Height != 900001 {
		t.Errorf("Height mismatch: got %d, want 900001", unsubmitted[0].Height)
	}
}

// TestPreSubmissionWAL_SubmittingSupersededBySubmitted verifies that a
// post-submission WAL write with "submitted" status supersedes the earlier
// "submitting" entry. RecoverUnsubmittedBlocks uses the latest entry per
// BlockHash, so the final status must take precedence.
func TestPreSubmissionWAL_SubmittingSupersededBySubmitted(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "0000000000000000000bbbb2222233333444455555666677778888999900001111"

	// Phase 1: Pre-submission WAL write (status="submitting")
	preEntry := &BlockWALEntry{
		Height:    900002,
		BlockHash: blockHash,
		BlockHex:  "cafebabe",
		Status:    "submitting",
	}
	if err := wal.LogBlockFound(preEntry); err != nil {
		t.Fatalf("LogBlockFound (pre) failed: %v", err)
	}

	// Phase 2: Post-submission WAL write (status="submitted")
	postEntry := &BlockWALEntry{
		Height:    900002,
		BlockHash: blockHash,
		BlockHex:  "cafebabe",
		Status:    "submitted",
	}
	if err := wal.LogBlockFound(postEntry); err != nil {
		t.Fatalf("LogBlockFound (post) failed: %v", err)
	}
	wal.Close()

	// Recovery should NOT find this block — latest status is "submitted".
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block %s should NOT be in unsubmitted list (latest status is 'submitted')",
				blockHash[:16])
		}
	}
}

// TestPreSubmissionWAL_SubmittingSupersededByRejected verifies that a
// "rejected" post-submission entry supersedes the "submitting" pre-entry.
// Rejected blocks should NOT be resubmitted on recovery.
func TestPreSubmissionWAL_SubmittingSupersededByRejected(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "0000000000000000000cccc3333344444555566666777788889999000011112222"

	// Pre-submission
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    900003,
		BlockHash: blockHash,
		BlockHex:  "deadcafe",
		Status:    "submitting",
	}); err != nil {
		t.Fatalf("LogBlockFound (pre) failed: %v", err)
	}

	// Post-submission: rejected
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:      900003,
		BlockHash:   blockHash,
		BlockHex:    "deadcafe",
		Status:      "rejected",
		SubmitError: "inconclusive",
	}); err != nil {
		t.Fatalf("LogBlockFound (post) failed: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Rejected block should NOT be in unsubmitted list")
		}
	}
}

// TestPreSubmissionWAL_PendingAndSubmittingBothRecovered verifies that both
// legacy "pending" entries (from the original code path) and new "submitting"
// entries (from the P0 fix) are recovered.
func TestPreSubmissionWAL_PendingAndSubmittingBothRecovered(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Legacy entry with "pending" status
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    900010,
		BlockHash: "0000000000000000000dddd4444455555666677777888899990000111122223333",
		BlockHex:  "legacy_pending_hex",
		Status:    "pending",
	}); err != nil {
		t.Fatalf("LogBlockFound (pending) failed: %v", err)
	}

	// New entry with "submitting" status
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    900011,
		BlockHash: "0000000000000000000eeee5555566666777788888999900001111222233334444",
		BlockHex:  "new_submitting_hex",
		Status:    "submitting",
	}); err != nil {
		t.Fatalf("LogBlockFound (submitting) failed: %v", err)
	}

	// Completed entry (should NOT be recovered)
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    900012,
		BlockHash: "0000000000000000000ffff6666677777888899999000011112222333344445555",
		BlockHex:  "submitted_hex",
		Status:    "submitted",
	}); err != nil {
		t.Fatalf("LogBlockFound (submitted) failed: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 unsubmitted blocks (pending + submitting), got %d", len(unsubmitted))
	}

	// Verify both statuses are present
	statuses := map[string]bool{}
	for _, entry := range unsubmitted {
		statuses[entry.Status] = true
	}
	if !statuses["pending"] {
		t.Error("Missing 'pending' entry in recovery results")
	}
	if !statuses["submitting"] {
		t.Error("Missing 'submitting' entry in recovery results")
	}
}

// -----------------------------------------------------------------------------
// T3.4: Concurrent WAL Writes
// -----------------------------------------------------------------------------

// TestConcurrentWALWrites_NoDataLoss verifies that concurrent goroutines
// writing to the same BlockWAL do not lose entries. The WAL uses a sync.Mutex
// to serialize writes, so all entries must survive.
func TestConcurrentWALWrites_NoDataLoss(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	const numGoroutines = 20
	const entriesPerGoroutine = 10
	totalEntries := numGoroutines * entriesPerGoroutine

	var wg sync.WaitGroup
	errs := make(chan error, totalEntries)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < entriesPerGoroutine; i++ {
				entry := &BlockWALEntry{
					Height:    uint64(goroutineID*1000 + i),
					BlockHash: "0000000000000000000aabbccddeeff001122334455667788aabbccddeeff0011",
					BlockHex:  "deadbeef",
					Status:    "pending",
					WorkerName: func() string {
						// Unique worker to identify entries
						return "worker_" + string(rune('A'+goroutineID)) + "_" + string(rune('0'+i))
					}(),
				}
				if err := wal.LogBlockFound(entry); err != nil {
					errs <- err
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)
	wal.Close()

	// Check for any write errors
	for err := range errs {
		t.Errorf("Concurrent WAL write error: %v", err)
	}

	// Read back and count all entries
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			// Verify each line is valid JSON
			var entry BlockWALEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				t.Errorf("Corrupt JSON line: %v", err)
				continue
			}
			nonEmpty++
		}
	}

	if nonEmpty != totalEntries {
		t.Errorf("Expected %d WAL entries, got %d (data loss detected!)", totalEntries, nonEmpty)
	}
}

// TestConcurrentWALWrites_MixedOperations verifies concurrent LogBlockFound
// and LogSubmissionResult calls don't corrupt the WAL.
func TestConcurrentWALWrites_MixedOperations(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	var wg sync.WaitGroup
	const numPairs = 10

	// Simulate concurrent pre-submission + post-submission writes
	for i := 0; i < numPairs; i++ {
		wg.Add(2)

		entry := &BlockWALEntry{
			Height:    uint64(800000 + i),
			BlockHash: "0000000000000000000aabbccddeeff001122334455667788aabbccddeeff0011",
			BlockHex:  "deadbeef",
			Status:    "submitting",
		}

		// Writer 1: LogBlockFound
		go func(e *BlockWALEntry) {
			defer wg.Done()
			// Use a copy to avoid race on Status field
			found := *e
			found.Status = "submitting"
			wal.LogBlockFound(&found)
		}(entry)

		// Writer 2: LogSubmissionResult
		go func(e *BlockWALEntry) {
			defer wg.Done()
			result := *e
			result.Status = "submitted"
			wal.LogSubmissionResult(&result)
		}(entry)
	}

	wg.Wait()
	wal.Close()

	// Verify file is valid JSONL (no interleaved partial writes)
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
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("Corrupt JSON line from concurrent writes: %s", string(line[:min(80, len(line))]))
			continue
		}
		validCount++
	}

	expectedMin := numPairs * 2 // At least one LogBlockFound + one LogSubmissionResult per pair
	if validCount < expectedMin {
		t.Errorf("Expected at least %d valid entries, got %d", expectedMin, validCount)
	}
}

// -----------------------------------------------------------------------------
// T6.4: Multi-File WAL Recovery
// -----------------------------------------------------------------------------

// TestRecoverUnsubmittedBlocks_MultipleFiles verifies that recovery scans
// all block_wal_*.jsonl files in the data directory, not just the most recent.
// This covers the case where WAL files span multiple dates due to daily rotation.
func TestRecoverUnsubmittedBlocks_MultipleFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Simulate WAL files from different dates
	files := map[string]string{
		"block_wal_2026-01-28.jsonl": `{"height":800001,"block_hash":"0000000000000000000aaaa111112222233333444445555566666777788889999","block_hex":"day1_hex","status":"pending"}`,
		"block_wal_2026-01-29.jsonl": `{"height":800002,"block_hash":"0000000000000000000bbbb222223333344444555556666677777888899990000","block_hex":"day2_hex","status":"submitted"}`,
		"block_wal_2026-01-30.jsonl": `{"height":800003,"block_hash":"0000000000000000000cccc333334444455555666667777788889999aaaa0000","block_hex":"day3_hex","status":"submitting"}`,
	}

	for filename, content := range files {
		path := filepath.Join(tmpDir, filename)
		if err := os.WriteFile(path, []byte(content+"\n"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", filename, err)
		}
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Should find 2 blocks: "pending" from day 1 and "submitting" from day 3.
	// The "submitted" block from day 2 should NOT be recovered.
	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 unsubmitted blocks from multi-file recovery, got %d", len(unsubmitted))
	}

	heights := map[uint64]bool{}
	for _, entry := range unsubmitted {
		heights[entry.Height] = true
	}
	if !heights[800001] {
		t.Error("Missing pending block from day 1 (height 800001)")
	}
	if !heights[800003] {
		t.Error("Missing submitting block from day 3 (height 800003)")
	}
	if heights[800002] {
		t.Error("Submitted block from day 2 should NOT be recovered")
	}
}

// TestRecoverUnsubmittedBlocks_CrossFileStatusUpdate verifies that a status
// update in a later WAL file supersedes an entry in an earlier file.
// Example: block written as "pending" on day 1, then "submitted" on day 2.
func TestRecoverUnsubmittedBlocks_CrossFileStatusUpdate(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	blockHash := "0000000000000000000dddd444445555566666777778888899990000aaaa1111"

	// Day 1: block found, status pending
	day1 := `{"height":850001,"block_hash":"` + blockHash + `","block_hex":"original_hex","status":"pending"}`
	if err := os.WriteFile(
		filepath.Join(tmpDir, "block_wal_2026-01-28.jsonl"),
		[]byte(day1+"\n"), 0644,
	); err != nil {
		t.Fatal(err)
	}

	// Day 2: same block, status updated to submitted
	day2 := `{"height":850001,"block_hash":"` + blockHash + `","block_hex":"original_hex","status":"submitted"}`
	if err := os.WriteFile(
		filepath.Join(tmpDir, "block_wal_2026-01-29.jsonl"),
		[]byte(day2+"\n"), 0644,
	); err != nil {
		t.Fatal(err)
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// The "submitted" entry from day 2 should supersede the "pending" from day 1.
	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block %s should NOT be unsubmitted (cross-file status update to 'submitted')",
				blockHash[:16])
		}
	}
}

// TestRecoverUnsubmittedBlocks_IgnoresNonWALFiles verifies that non-WAL files
// in the data directory are not accidentally parsed.
func TestRecoverUnsubmittedBlocks_IgnoresNonWALFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Create a WAL file with a pending entry
	walContent := `{"height":800010,"block_hash":"0000000000000000000eeee555556666677777888889999900001111222233334","block_hex":"real_hex","status":"pending"}`
	if err := os.WriteFile(
		filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"),
		[]byte(walContent+"\n"), 0644,
	); err != nil {
		t.Fatal(err)
	}

	// Create non-WAL files that should be ignored
	if err := os.WriteFile(
		filepath.Join(tmpDir, "other_data.jsonl"),
		[]byte(`{"height":999999,"block_hash":"fake","status":"pending"}`+"\n"), 0644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(tmpDir, "block_wal_corrupted.txt"),
		[]byte(`NOT_JSON_AT_ALL`), 0644,
	); err != nil {
		t.Fatal(err)
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected exactly 1 unsubmitted block from WAL file only, got %d", len(unsubmitted))
	}
	if unsubmitted[0].Height != 800010 {
		t.Errorf("Wrong block recovered: height=%d, want 800010", unsubmitted[0].Height)
	}
}

// -----------------------------------------------------------------------------
// T9.4: WAL Entry Independence from DB
// -----------------------------------------------------------------------------

// TestWALEntry_ContainsAllDataForResubmission verifies that a WAL entry with
// status "submitting" contains sufficient data for standalone resubmission,
// independent of any database state.
func TestWALEntry_ContainsAllDataForResubmission(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	entry := &BlockWALEntry{
		Height:        900100,
		BlockHash:     "0000000000000000000ffff6666677777888899990000aaaa1111bbbb2222cccc",
		PrevHash:      "0000000000000000000eeee555554444433332222111100009999888877776666",
		BlockHex:      "0100000001deadbeefcafebabe",
		MinerAddress:  "bc1qresubmittest",
		WorkerName:    "worker_42",
		JobID:         "job_99",
		CoinbaseValue: 312500000,
		Status:        "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	// Recover and verify all fields needed for resubmission
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]

	// BlockHex is the critical field — without it, resubmission is impossible
	if recovered.BlockHex == "" {
		t.Fatal("CRITICAL: Recovered entry has empty BlockHex — cannot resubmit")
	}
	if recovered.BlockHex != entry.BlockHex {
		t.Error("BlockHex mismatch between written and recovered entry")
	}
	if recovered.BlockHash == "" {
		t.Error("BlockHash missing — needed for deduplication")
	}
	if recovered.Height == 0 {
		t.Error("Height missing — needed for logging and chain verification")
	}
}

