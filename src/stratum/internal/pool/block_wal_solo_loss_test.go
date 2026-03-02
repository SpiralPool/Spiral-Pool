// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"fmt"
	"math"
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
// SOLO MINING BLOCK LOSS PREVENTION: WAL TEST SUITE
// =============================================================================
//
// These tests exercise every code path where a valid solved block could be
// silently lost due to WAL failures, crash timing, or concurrent access.
//
// Risk vectors covered:
//   1. WAL pre-submission crash recovery ("submitting" + "pending" statuses)
//   2. buildFullBlock failure → emergency WAL entry with raw components
//   3. Concurrent WAL writes from multiple goroutines
//   4. Disk/filesystem write errors during WAL logging
//   5. Edge-case block data (high heights, uint64 boundary, empty fields)
//   6. WAL recovery across multiple date-rotated files
//   7. Exactly-once submission guarantee under concurrency

// =============================================================================
// RISK VECTOR 1: WAL Pre-Submission Crash Recovery
// =============================================================================

// TestSOLO_WAL_CrashDuringSubmission_SubmittingStatusRecovered verifies that
// if the process crashes DURING SubmitBlockWithVerification (after the
// pre-submission WAL write but before the post-submission WAL write), the
// block is recoverable via RecoverUnsubmittedBlocks.
//
// This is the P0 block-loss scenario: block hex exists only in memory during
// the RPC call. Without the pre-submission "submitting" WAL entry, an OOM kill
// at this point would lose the block forever.
func TestSOLO_WAL_CrashDuringSubmission_SubmittingStatusRecovered(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Simulate the exact pre-submission WAL write from handleBlock:
	//   preSubmitEntry.Status = "submitting"
	//   p.blockWAL.LogBlockFound(preSubmitEntry)
	//   sr := p.daemonClient.SubmitBlockWithVerification(...)  ← CRASH HERE
	preSubmitEntry := &BlockWALEntry{
		Height:        20000001,
		BlockHash:     "00000000000000000001aabbccddeeff00112233445566778899aabbccddeeff",
		PrevHash:      "00000000000000000000ffeeddccbbaa99887766554433221100ffeeddccbbaa",
		BlockHex:      "0200000001" + strings.Repeat("ab", 500), // realistic-ish block hex
		MinerAddress:  "DGBsolominer1",
		WorkerName:    "bitaxe_rig1",
		JobID:         "job_crash_test_1",
		CoinbaseValue: 27700000000, // ~277 DGB
		Status:        "submitting",
	}

	if err := wal.LogBlockFound(preSubmitEntry); err != nil {
		t.Fatalf("LogBlockFound (pre-submission) failed: %v", err)
	}

	// Simulate crash: close WAL without writing post-submission entry.
	wal.Close()

	// On restart, RecoverUnsubmittedBlocks must find this block.
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block after crash, got %d", len(unsubmitted))
	}

	recovered := unsubmitted[0]

	// CRITICAL: BlockHex must be preserved — it's the only thing needed for resubmission
	if recovered.BlockHex == "" {
		t.Fatal("BLOCK LOSS: Recovered entry has empty BlockHex — block cannot be resubmitted!")
	}
	if recovered.BlockHex != preSubmitEntry.BlockHex {
		t.Error("BlockHex corrupted during recovery")
	}
	if recovered.Status != "submitting" {
		t.Errorf("Expected status 'submitting', got %q", recovered.Status)
	}
	if recovered.Height != preSubmitEntry.Height {
		t.Errorf("Height mismatch: got %d, want %d", recovered.Height, preSubmitEntry.Height)
	}
	if recovered.BlockHash != preSubmitEntry.BlockHash {
		t.Error("BlockHash mismatch — needed for deduplication on resubmission")
	}
}

// TestSOLO_WAL_CrashBeforeSubmission_PendingStatusRecovered verifies the
// legacy code path where blocks are logged as "pending" before submission.
func TestSOLO_WAL_CrashBeforeSubmission_PendingStatusRecovered(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	entry := &BlockWALEntry{
		Height:    20000002,
		BlockHash: "00000000000000000002aabbccddeeff00112233445566778899aabbccddeeff",
		BlockHex:  "deadbeefcafe1234",
		Status:    "pending",
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
	if unsubmitted[0].Status != "pending" {
		t.Errorf("Expected 'pending', got %q", unsubmitted[0].Status)
	}
}

// TestSOLO_WAL_SuccessfulSubmission_NotRecovered verifies that after a
// successful submission, the post-submission WAL entry supersedes the
// pre-submission entry and the block is NOT returned by recovery.
func TestSOLO_WAL_SuccessfulSubmission_NotRecovered(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "00000000000000000003aabbccddeeff00112233445566778899aabbccddeeff"

	// Phase 1: Pre-submission ("submitting")
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    20000003,
		BlockHash: blockHash,
		BlockHex:  "cafebabe",
		Status:    "submitting",
	}); err != nil {
		t.Fatalf("Pre-submission WAL write failed: %v", err)
	}

	// Phase 2: Post-submission — different final statuses that should all
	// indicate "block was handled, do not resubmit"
	for _, finalStatus := range []string{"submitted", "accepted", "rejected"} {
		if err := wal.LogBlockFound(&BlockWALEntry{
			Height:    20000003,
			BlockHash: blockHash,
			BlockHex:  "cafebabe",
			Status:    finalStatus,
		}); err != nil {
			t.Fatalf("Post-submission WAL write (%s) failed: %v", finalStatus, err)
		}
	}

	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block %s should NOT be recovered — latest status was %q",
				blockHash[:16], entry.Status)
		}
	}
}

// =============================================================================
// RISK VECTOR 2: buildFullBlock Failure → Emergency WAL Entry
// =============================================================================

// TestSOLO_WAL_BuildFailed_AllRawComponentsPreserved verifies that when
// buildFullBlock fails and the emergency WAL path is taken, ALL raw block
// components needed for manual reconstruction are preserved.
func TestSOLO_WAL_BuildFailed_AllRawComponentsPreserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Simulate the emergency WAL entry created by handleBlock when
	// buildFullBlock fails AND recovery rebuild also fails.
	emergencyEntry := &BlockWALEntry{
		Height:        20000010,
		BlockHash:     "00000000000000000010aabbccddeeff00112233445566778899aabbccddeeff",
		PrevHash:      "00000000000000000009ffeeddccbbaa99887766554433221100ffeeddccbbaa",
		MinerAddress:  "DGBsolominer1",
		WorkerName:    "worker1",
		JobID:         "job_build_fail",
		CoinbaseValue: 27700000000,
		Status:        "build_failed",
		SubmitError:   "invalid coinbase1: encoding/hex: invalid byte 0x5a ('Z')",

		// ALL raw components for manual reconstruction
		CoinBase1:   "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:   "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		ExtraNonce1: "aabbccdd",
		ExtraNonce2: "11223344",
		Version:     "20000000",
		NBits:       "1a0377ae",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		TransactionData: []string{
			"0100000001" + strings.Repeat("00", 50),
			"0100000001" + strings.Repeat("ff", 50),
		},
	}

	if err := wal.LogBlockFound(emergencyEntry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Read back and verify every field
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("WAL file has no entries")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Check every reconstruction field
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"CoinBase1", recovered.CoinBase1, emergencyEntry.CoinBase1},
		{"CoinBase2", recovered.CoinBase2, emergencyEntry.CoinBase2},
		{"ExtraNonce1", recovered.ExtraNonce1, emergencyEntry.ExtraNonce1},
		{"ExtraNonce2", recovered.ExtraNonce2, emergencyEntry.ExtraNonce2},
		{"Version", recovered.Version, emergencyEntry.Version},
		{"NBits", recovered.NBits, emergencyEntry.NBits},
		{"NTime", recovered.NTime, emergencyEntry.NTime},
		{"Nonce", recovered.Nonce, emergencyEntry.Nonce},
		{"Status", recovered.Status, "build_failed"},
		{"SubmitError", recovered.SubmitError, emergencyEntry.SubmitError},
		{"BlockHash", recovered.BlockHash, emergencyEntry.BlockHash},
		{"PrevHash", recovered.PrevHash, emergencyEntry.PrevHash},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("BLOCK LOSS RISK: %s mismatch: got %q, want %q", c.name, c.got, c.want)
		}
	}

	if len(recovered.TransactionData) != len(emergencyEntry.TransactionData) {
		t.Fatalf("TransactionData count: got %d, want %d",
			len(recovered.TransactionData), len(emergencyEntry.TransactionData))
	}
	for i, tx := range recovered.TransactionData {
		if tx != emergencyEntry.TransactionData[i] {
			t.Errorf("TransactionData[%d] mismatch", i)
		}
	}
}

// TestSOLO_WAL_BuildFailed_NotAutoRecoverable verifies that build_failed
// entries are NOT returned by RecoverUnsubmittedBlocks — they require
// manual operator intervention since the block hex couldn't be constructed.
func TestSOLO_WAL_BuildFailed_NotAutoRecoverable(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// build_failed entry — requires manual reconstruction
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:      20000020,
		BlockHash:   "00000000000000000020aabbccddeeff00112233445566778899aabbccddeeff",
		Status:      "build_failed",
		SubmitError: "corrupt coinbase",
		CoinBase1:   "deadbeef",
		ExtraNonce1: "00000001",
	}); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Normal pending entry — should be auto-recoverable
	if err := wal.LogBlockFound(&BlockWALEntry{
		Height:    20000021,
		BlockHash: "00000000000000000021aabbccddeeff00112233445566778899aabbccddeeff",
		BlockHex:  "validhex",
		Status:    "submitting",
	}); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 auto-recoverable block, got %d", len(unsubmitted))
	}
	if unsubmitted[0].Height != 20000021 {
		t.Errorf("Wrong block recovered: height=%d, want 20000021", unsubmitted[0].Height)
	}
}

// =============================================================================
// RISK VECTOR 3: Concurrent WAL Writes
// =============================================================================

// TestSOLO_WAL_ConcurrentSubmissions_NoBlockLoss verifies that when multiple
// goroutines write to the WAL simultaneously (e.g., multiple solved shares
// arriving at the same time), no entries are lost or corrupted.
func TestSOLO_WAL_ConcurrentSubmissions_NoBlockLoss(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	const numGoroutines = 50
	var wg sync.WaitGroup
	var writeErrors atomic.Int32

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			entry := &BlockWALEntry{
				Height:    uint64(30000000 + id),
				BlockHash: fmt.Sprintf("%064x", id),
				BlockHex:  fmt.Sprintf("blockhex_%d", id),
				Status:    "submitting",
				WorkerName: fmt.Sprintf("worker_%d", id),
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
	validEntries := 0
	corruptLines := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			corruptLines++
			t.Errorf("Corrupt JSON line (data loss!): %s", string(line[:min(80, len(line))]))
			continue
		}
		validEntries++
	}

	if corruptLines > 0 {
		t.Errorf("BLOCK LOSS: %d corrupt WAL lines from concurrent writes", corruptLines)
	}
	if validEntries != numGoroutines {
		t.Errorf("BLOCK LOSS: Expected %d WAL entries, got %d (%d entries lost!)",
			numGoroutines, validEntries, numGoroutines-validEntries)
	}
}

// TestSOLO_WAL_ConcurrentPreAndPostSubmission verifies that interleaved
// pre-submission (LogBlockFound) and post-submission (LogSubmissionResult)
// writes from different goroutines don't corrupt the WAL.
func TestSOLO_WAL_ConcurrentPreAndPostSubmission(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	const numBlocks = 25
	var wg sync.WaitGroup

	for i := 0; i < numBlocks; i++ {
		wg.Add(2)

		// Goroutine 1: Pre-submission write
		go func(blockID int) {
			defer wg.Done()
			entry := BlockWALEntry{
				Height:    uint64(31000000 + blockID),
				BlockHash: fmt.Sprintf("%064x", blockID),
				BlockHex:  fmt.Sprintf("hex_%d", blockID),
				Status:    "submitting",
			}
			wal.LogBlockFound(&entry)
		}(i)

		// Goroutine 2: Post-submission result write
		go func(blockID int) {
			defer wg.Done()
			entry := BlockWALEntry{
				Height:    uint64(31000000 + blockID),
				BlockHash: fmt.Sprintf("%064x", blockID),
				BlockHex:  fmt.Sprintf("hex_%d", blockID),
				Status:    "submitted",
			}
			wal.LogSubmissionResult(&entry)
		}(i)
	}

	wg.Wait()
	wal.Close()

	// Verify every line is valid JSON (no partial/interleaved writes)
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
			t.Errorf("Corrupt JSON from concurrent pre/post writes: %v", err)
			continue
		}
		validCount++
	}

	// Expect at least numBlocks*2 entries (one pre + one post per block)
	expectedMin := numBlocks * 2
	if validCount < expectedMin {
		t.Errorf("Expected at least %d valid entries, got %d", expectedMin, validCount)
	}
}

// TestSOLO_WAL_ExactlyOnceSubmission_DuplicatePreventedByLatestStatus
// verifies that RecoverUnsubmittedBlocks returns each block at most once,
// using the latest WAL entry per block hash. This prevents double-submission.
func TestSOLO_WAL_ExactlyOnceSubmission_DuplicatePreventedByLatestStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	blockHash := "00000000000000000050aabbccddeeff00112233445566778899aabbccddeeff"

	// Write 5 entries for the same block with escalating statuses
	statuses := []string{"pending", "submitting", "submitting", "submitted", "pending"}
	for _, s := range statuses {
		if err := wal.LogBlockFound(&BlockWALEntry{
			Height:    32000000,
			BlockHash: blockHash,
			BlockHex:  "somehex",
			Status:    s,
		}); err != nil {
			t.Fatalf("LogBlockFound failed: %v", err)
		}
	}
	wal.Close()

	// The latest entry is "pending" — but recovery uses the LAST entry per hash.
	// This tests that the map-based deduplication works correctly.
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Count how many times this block hash appears
	count := 0
	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			count++
		}
	}

	if count > 1 {
		t.Errorf("DOUBLE SUBMISSION RISK: Block appears %d times in recovery (should be 0 or 1)", count)
	}
}

// =============================================================================
// RISK VECTOR 4: Disk/Filesystem Write Errors
// =============================================================================

// TestSOLO_WAL_ClosedFileWrite_ReturnsError verifies that writing to a
// closed WAL returns an error rather than silently dropping the block.
func TestSOLO_WAL_ClosedFileWrite_ReturnsError(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Close the WAL (simulating a resource cleanup error)
	wal.Close()

	// Attempt to write after close — must return error, not silently lose block
	entry := &BlockWALEntry{
		Height:    33000000,
		BlockHash: "00000000000000000060aabbccddeeff00112233445566778899aabbccddeeff",
		BlockHex:  "importantblock",
		Status:    "submitting",
	}

	err = wal.LogBlockFound(entry)
	if err == nil {
		// If no error, the block was silently lost! This is a block loss scenario.
		t.Error("BLOCK LOSS: LogBlockFound succeeded on closed WAL — block data may be lost!")
	}
}

// TestSOLO_WAL_ReadOnlyDirectory_CreationFails verifies that creating a WAL
// in a read-only directory fails explicitly rather than silently.
func TestSOLO_WAL_ReadOnlyDirectory_CreationFails(t *testing.T) {
	t.Parallel()

	// Use a path that cannot be created
	impossiblePath := filepath.Join(t.TempDir(), "nonexistent_file", "subdir", "another")
	// Create a file where a directory is expected
	parentDir := filepath.Dir(filepath.Dir(impossiblePath))
	os.MkdirAll(parentDir, 0755)
	blockingFile := filepath.Join(parentDir, "subdir")
	os.WriteFile(blockingFile, []byte("I am a file, not a directory"), 0444)

	logger := zap.NewNop()
	_, err := NewBlockWAL(blockingFile, logger)
	if err == nil {
		t.Error("Expected error creating WAL in blocked path, got nil")
	}
}

// =============================================================================
// RISK VECTOR 5: Edge-Case Block Data
// =============================================================================

// TestSOLO_WAL_HighBlockHeight_MaxUint64 verifies WAL handles extreme heights.
func TestSOLO_WAL_HighBlockHeight_MaxUint64(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Test near-max uint64 height
	entry := &BlockWALEntry{
		Height:    math.MaxUint64 - 1,
		BlockHash: "00000000000000000070aabbccddeeff00112233445566778899aabbccddeeff",
		BlockHex:  "maxheight",
		Status:    "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed for max height: %v", err)
	}

	// Read back and verify height survived JSON roundtrip
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if recovered.Height != math.MaxUint64-1 {
		t.Errorf("Height lost precision: got %d, want %d", recovered.Height, uint64(math.MaxUint64-1))
	}
}

// TestSOLO_WAL_EmptyBlockHex_StillLogged verifies that even when BlockHex is
// empty (buildFullBlock failed), the WAL entry is still created with raw
// components for manual reconstruction.
func TestSOLO_WAL_EmptyBlockHex_StillLogged(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:      34000000,
		BlockHash:   "00000000000000000080aabbccddeeff00112233445566778899aabbccddeeff",
		BlockHex:    "", // Empty — buildFullBlock failed
		Status:      "build_failed",
		SubmitError: "invalid coinbase1",
		CoinBase1:   "01000000",
		CoinBase2:   "ffffffff",
		ExtraNonce1: "aabb",
		ExtraNonce2: "ccdd",
		Version:     "20000000",
		NBits:       "1a0377ae",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Verify entry was logged even with empty BlockHex
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL file has no entries despite logging")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if recovered.CoinBase1 == "" || recovered.ExtraNonce1 == "" {
		t.Error("BLOCK LOSS: Raw reconstruction components missing from WAL entry")
	}
}

// TestSOLO_WAL_LargeTransactionData verifies WAL handles blocks with many
// transactions (realistic mainnet blocks can have thousands).
func TestSOLO_WAL_LargeTransactionData(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Simulate a block with 500 transactions
	txData := make([]string, 500)
	for i := range txData {
		txData[i] = fmt.Sprintf("0100000001%064x00000000ffffffff0100000000000000000000000000", i)
	}

	entry := &BlockWALEntry{
		Height:          35000000,
		BlockHash:       "00000000000000000090aabbccddeeff00112233445566778899aabbccddeeff",
		Status:          "build_failed",
		TransactionData: txData,
		CoinBase1:       "01000000",
		ExtraNonce1:     "aabb",
		ExtraNonce2:     "ccdd",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed for large tx set: %v", err)
	}

	// Read back and verify transaction count
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if len(recovered.TransactionData) != 500 {
		t.Errorf("BLOCK LOSS: Transaction data truncated: got %d, want 500",
			len(recovered.TransactionData))
	}
}

// TestSOLO_WAL_SpecialCharactersInWorkerName verifies WAL handles unusual
// but valid worker names without JSON encoding issues.
func TestSOLO_WAL_SpecialCharactersInWorkerName(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Worker names with characters that need JSON escaping
	testNames := []string{
		`worker"with"quotes`,
		"worker\twith\ttabs",
		"worker\\with\\backslashes",
		"worker/with/slashes",
		"rig_日本語",
		"",
		strings.Repeat("x", 1000),
	}

	for i, name := range testNames {
		entry := &BlockWALEntry{
			Height:     uint64(36000000 + i),
			BlockHash:  fmt.Sprintf("%064x", i+100),
			BlockHex:   "deadbeef",
			WorkerName: name,
			Status:     "pending",
		}
		if err := wal.LogBlockFound(entry); err != nil {
			t.Errorf("LogBlockFound failed for worker name %q: %v", name, err)
		}
	}

	// Read back and verify all entries are valid JSON
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
			t.Errorf("Corrupt JSON from special char worker name: %v", err)
			continue
		}
		validCount++
	}

	if validCount != len(testNames) {
		t.Errorf("Expected %d valid entries, got %d", len(testNames), validCount)
	}
}

// TestSOLO_WAL_NegativeCoinbaseValue verifies WAL preserves coinbase values
// including edge cases (zero, very large).
func TestSOLO_WAL_CoinbaseValueEdgeCases(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	testValues := []int64{
		0,
		1,
		5000000000,           // 50 BTC
		27700000000,          // ~277 DGB
		2100000000000000,     // 21M BTC in satoshis
		math.MaxInt64,        // Extreme value
	}

	for i, val := range testValues {
		entry := &BlockWALEntry{
			Height:        uint64(37000000 + i),
			BlockHash:     fmt.Sprintf("%064x", i+200),
			BlockHex:      "deadbeef",
			CoinbaseValue: val,
			Status:        "pending",
		}
		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound failed for coinbase value %d: %v", val, err)
		}
	}

	// Read back and verify values
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	idx := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("JSON unmarshal failed: %v", err)
			continue
		}
		if idx < len(testValues) && entry.CoinbaseValue != testValues[idx] {
			t.Errorf("CoinbaseValue[%d]: got %d, want %d", idx, entry.CoinbaseValue, testValues[idx])
		}
		idx++
	}
}

// =============================================================================
// RISK VECTOR 6: Multi-File WAL Recovery
// =============================================================================

// TestSOLO_WAL_Recovery_AcrossDateRotation verifies that crash recovery
// correctly scans all WAL files when the pool has been running across
// multiple days (daily file rotation).
func TestSOLO_WAL_Recovery_AcrossDateRotation(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Day 1: Block found, status pending — process crashed before submission
	day1Content := `{"height":40000001,"block_hash":"00000000000000000100aabbccddeeff00112233445566778899aabbccddeeff","block_hex":"day1_block","status":"pending","coinbase_value":27700000000}` + "\n"

	// Day 2: Different block submitted successfully
	day2Content := `{"height":40000002,"block_hash":"00000000000000000200aabbccddeeff00112233445566778899aabbccddeeff","block_hex":"day2_block","status":"submitted"}` + "\n"

	// Day 3: Another block in submitting status — process crashed during RPC
	day3Content := `{"height":40000003,"block_hash":"00000000000000000300aabbccddeeff00112233445566778899aabbccddeeff","block_hex":"day3_block","status":"submitting","coinbase_value":27700000000}` + "\n"

	files := map[string]string{
		"block_wal_2026-01-28.jsonl": day1Content,
		"block_wal_2026-01-29.jsonl": day2Content,
		"block_wal_2026-01-30.jsonl": day3Content,
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", name, err)
		}
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Should recover 2 blocks: day1 (pending) and day3 (submitting)
	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 unsubmitted blocks, got %d", len(unsubmitted))
	}

	heights := map[uint64]bool{}
	for _, entry := range unsubmitted {
		heights[entry.Height] = true
		// CRITICAL: Each recovered entry must have BlockHex for resubmission
		if entry.BlockHex == "" {
			t.Errorf("BLOCK LOSS: Recovered block at height %d has empty BlockHex", entry.Height)
		}
	}

	if !heights[40000001] {
		t.Error("Missing pending block from day 1")
	}
	if !heights[40000003] {
		t.Error("Missing submitting block from day 3")
	}
	if heights[40000002] {
		t.Error("Submitted block from day 2 should NOT be recovered")
	}
}

// TestSOLO_WAL_Recovery_CrossFileSupersede verifies that when the same block
// hash appears in multiple WAL files, the LATEST entry's status is used.
// This handles the case where a block is logged "pending" in day 1's WAL
// but submitted and logged "submitted" in day 2's WAL.
func TestSOLO_WAL_Recovery_CrossFileSupersede(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	blockHash := "00000000000000000400aabbccddeeff00112233445566778899aabbccddeeff"

	// Day 1: Block found, pending
	day1 := fmt.Sprintf(`{"height":41000001,"block_hash":"%s","block_hex":"the_block","status":"pending"}`, blockHash)
	os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-28.jsonl"), []byte(day1+"\n"), 0644)

	// Day 2: Same block, now submitted
	day2 := fmt.Sprintf(`{"height":41000001,"block_hash":"%s","block_hex":"the_block","status":"submitted"}`, blockHash)
	os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-29.jsonl"), []byte(day2+"\n"), 0644)

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block should NOT be recovered — later WAL file has 'submitted' status")
		}
	}
}

// TestSOLO_WAL_Recovery_MalformedLines_SkippedGracefully verifies that
// corrupt JSONL lines (truncated writes, disk errors) don't prevent
// recovery of valid entries in the same file.
func TestSOLO_WAL_Recovery_MalformedLines_SkippedGracefully(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	content := strings.Join([]string{
		`NOT_JSON_AT_ALL`,
		`{"height":42000001,"block_hash":"00000000000000000500aabbccddeeff00112233445566778899aabbccddeeff","block_hex":"valid_block","status":"pending"}`,
		`{broken json truncated`,
		`{"height":42000002,"block_hash":"00000000000000000600aabbccddeeff00112233445566778899aabbccddeeff","block_hex":"another_valid","status":"submitting"}`,
		``,
		`{"invalid":`,
	}, "\n")

	os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl"), []byte(content+"\n"), 0644)

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != 2 {
		t.Fatalf("Expected 2 valid blocks recovered despite corrupt lines, got %d", len(unsubmitted))
	}
}

// =============================================================================
// RISK VECTOR 7: Exactly-Once Submission Under Concurrency
// =============================================================================

// TestSOLO_WAL_RecoveryDeduplication_SameBlockMultipleEntries verifies that
// when the same block has multiple WAL entries (normal in the pre/post
// submission pattern), RecoverUnsubmittedBlocks returns at most one entry
// per block hash, using the latest status.
func TestSOLO_WAL_RecoveryDeduplication_SameBlockMultipleEntries(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	hash1 := "00000000000000000700aabbccddeeff00112233445566778899aabbccddeeff"
	hash2 := "00000000000000000800aabbccddeeff00112233445566778899aabbccddeeff"

	// Block 1: submitting → submitted (should NOT be recovered)
	wal.LogBlockFound(&BlockWALEntry{Height: 43000001, BlockHash: hash1, BlockHex: "hex1", Status: "submitting"})
	wal.LogBlockFound(&BlockWALEntry{Height: 43000001, BlockHash: hash1, BlockHex: "hex1", Status: "submitted"})

	// Block 2: submitting → no post-submission (should BE recovered, once)
	wal.LogBlockFound(&BlockWALEntry{Height: 43000002, BlockHash: hash2, BlockHex: "hex2", Status: "submitting"})
	// Simulate concurrent duplicate write (race condition)
	wal.LogBlockFound(&BlockWALEntry{Height: 43000002, BlockHash: hash2, BlockHex: "hex2", Status: "submitting"})

	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Block 1: submitted → NOT recovered
	// Block 2: submitting → recovered EXACTLY ONCE
	block2Count := 0
	for _, entry := range unsubmitted {
		if entry.BlockHash == hash1 {
			t.Error("Block 1 (submitted) should not be recovered")
		}
		if entry.BlockHash == hash2 {
			block2Count++
		}
	}

	if block2Count != 1 {
		t.Errorf("EXACTLY-ONCE VIOLATION: Block 2 recovered %d times (should be 1)", block2Count)
	}
}

// TestSOLO_WAL_FsyncDurability verifies that every LogBlockFound call
// results in the data being immediately readable from disk (fsync guarantee).
func TestSOLO_WAL_FsyncDurability(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	for i := 0; i < 10; i++ {
		entry := &BlockWALEntry{
			Height:    uint64(44000000 + i),
			BlockHash: fmt.Sprintf("%064x", i+300),
			BlockHex:  fmt.Sprintf("block_%d", i),
			Status:    "submitting",
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

// TestSOLO_WAL_TimestampPreserved verifies that LogBlockFound sets a
// non-zero timestamp on every entry.
func TestSOLO_WAL_TimestampPreserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	before := time.Now()

	entry := &BlockWALEntry{
		Height:    45000000,
		BlockHash: "00000000000000001000aabbccddeeff00112233445566778899aabbccddeeff",
		BlockHex:  "timestamptest",
		Status:    "submitting",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	after := time.Now()

	// Verify timestamp was set
	if entry.Timestamp.IsZero() {
		t.Error("LogBlockFound did not set timestamp")
	}
	if entry.Timestamp.Before(before) || entry.Timestamp.After(after) {
		t.Errorf("Timestamp %v is outside expected range [%v, %v]",
			entry.Timestamp, before, after)
	}
}
