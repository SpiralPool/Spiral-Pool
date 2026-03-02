// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestEmergencyWALEntry_RoundTrip verifies that an emergency WAL entry with
// all raw reconstruction components survives write + JSON roundtrip.
// This is the last-resort path when buildFullBlock fails AND recovery rebuild fails.
func TestEmergencyWALEntry_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:        800001,
		BlockHash:     "0000000000000000000123456789abcdef0123456789abcdef0123456789abcdef",
		PrevHash:      "000000000000000000fedcba9876543210fedcba9876543210fedcba98765432",
		MinerAddress:  "bc1qtest",
		WorkerName:    "worker1",
		JobID:         "job_42",
		JobAge:        3 * time.Second,
		CoinbaseValue: 312500000, // 3.125 BTC
		Status:        "build_failed",
		SubmitError:   "invalid coinbase1: encoding/hex: invalid byte 0x5a ('Z') in decodestring",

		// Raw reconstruction components — the whole point of this test
		CoinBase1:   "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:   "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		Version:     "20000000",
		NBits:       "1a0377ae",
		NTime:       "64000000",
		Nonce:       "12345678",
		TransactionData: []string{
			"0100000001000000000000000000000000000000000000000000000000000000000000000000000000ffffffff0100000000000000000000000000",
			"0100000001abcdef00000000000000000000000000000000000000000000000000000000000000000ffffffff0100000000000000000000000000",
		},
	}

	// Write to WAL
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Read back the raw JSONL
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

	// Verify all reconstruction fields survived roundtrip
	if recovered.CoinBase1 != entry.CoinBase1 {
		t.Errorf("CoinBase1 mismatch: got %q, want %q", recovered.CoinBase1, entry.CoinBase1)
	}
	if recovered.CoinBase2 != entry.CoinBase2 {
		t.Errorf("CoinBase2 mismatch: got %q, want %q", recovered.CoinBase2, entry.CoinBase2)
	}
	if recovered.ExtraNonce1 != entry.ExtraNonce1 {
		t.Errorf("ExtraNonce1 mismatch: got %q, want %q", recovered.ExtraNonce1, entry.ExtraNonce1)
	}
	if recovered.ExtraNonce2 != entry.ExtraNonce2 {
		t.Errorf("ExtraNonce2 mismatch: got %q, want %q", recovered.ExtraNonce2, entry.ExtraNonce2)
	}
	if recovered.Version != entry.Version {
		t.Errorf("Version mismatch: got %q, want %q", recovered.Version, entry.Version)
	}
	if recovered.NBits != entry.NBits {
		t.Errorf("NBits mismatch: got %q, want %q", recovered.NBits, entry.NBits)
	}
	if recovered.NTime != entry.NTime {
		t.Errorf("NTime mismatch: got %q, want %q", recovered.NTime, entry.NTime)
	}
	if recovered.Nonce != entry.Nonce {
		t.Errorf("Nonce mismatch: got %q, want %q", recovered.Nonce, entry.Nonce)
	}
	if len(recovered.TransactionData) != len(entry.TransactionData) {
		t.Fatalf("TransactionData length mismatch: got %d, want %d",
			len(recovered.TransactionData), len(entry.TransactionData))
	}
	for i, tx := range recovered.TransactionData {
		if tx != entry.TransactionData[i] {
			t.Errorf("TransactionData[%d] mismatch: got %q, want %q", i, tx, entry.TransactionData[i])
		}
	}

	// Verify metadata fields
	if recovered.Status != "build_failed" {
		t.Errorf("Status mismatch: got %q, want %q", recovered.Status, "build_failed")
	}
	if recovered.SubmitError != entry.SubmitError {
		t.Errorf("SubmitError mismatch: got %q, want %q", recovered.SubmitError, entry.SubmitError)
	}
	if recovered.Height != 800001 {
		t.Errorf("Height mismatch: got %d, want %d", recovered.Height, 800001)
	}
}

// TestEmergencyWALEntry_NotRecoveredAsUnsubmitted verifies that build_failed
// entries are NOT returned by RecoverUnsubmittedBlocks. Emergency entries require
// manual operator intervention — they cannot be automatically resubmitted because
// the block hex could not be constructed.
func TestEmergencyWALEntry_NotRecoveredAsUnsubmitted(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Write a build_failed entry
	emergency := &BlockWALEntry{
		Height:      800001,
		BlockHash:   "0000000000000000000abcdef123456789abcdef123456789abcdef123456789a",
		Status:      "build_failed",
		SubmitError: "invalid coinbase1",
		CoinBase1:   "deadbeef",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}
	if err := wal.LogBlockFound(emergency); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Also write a normal pending entry
	pending := &BlockWALEntry{
		Height:    800002,
		BlockHash: "0000000000000000000fedcba987654321fedcba987654321fedcba987654321f",
		BlockHex:  "deadbeef",
	}
	if err := wal.LogBlockFound(pending); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	wal.Close()

	// Recover unsubmitted blocks
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Should only find the "pending" entry, NOT the "build_failed" one
	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block, got %d", len(unsubmitted))
	}
	if unsubmitted[0].Height != 800002 {
		t.Errorf("Wrong block recovered: height=%d, want 800002", unsubmitted[0].Height)
	}
}

// TestEmergencyWALEntry_SubmissionResultUpdatesStatus verifies that after
// writing a build_failed entry, a subsequent LogSubmissionResult with the same
// block hash correctly shows in the WAL as the latest state.
func TestEmergencyWALEntry_SubmissionResultUpdatesStatus(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	blockHash := "0000000000000000000aaaaabbbbccccddddeeeeffffaaaaabbbbccccddddeeee"

	// Phase 1: Log build_failed emergency entry
	emergency := &BlockWALEntry{
		Height:      800001,
		BlockHash:   blockHash,
		Status:      "build_failed",
		SubmitError: "invalid coinbase1",
		CoinBase1:   "deadbeef",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}
	if err := wal.LogBlockFound(emergency); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Phase 2: Operator manually reconstructed and submitted — log result
	emergency.Status = "submitted"
	emergency.SubmitError = ""
	if err := wal.LogSubmissionResult(emergency); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}

	wal.Close()

	// RecoverUnsubmittedBlocks should see "submitted" as the latest state
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Should find zero unsubmitted blocks (latest state is "submitted")
	for _, entry := range unsubmitted {
		if entry.BlockHash == blockHash {
			t.Errorf("Block %s should not be in unsubmitted list (status was updated to 'submitted')", blockHash[:16])
		}
	}
}

// TestBlockWAL_FsyncOnEveryWrite verifies the WAL file is durable after each write.
// We check this by verifying the file exists and has content after LogBlockFound.
func TestBlockWAL_FsyncOnEveryWrite(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    800001,
		BlockHash: "0000000000000000000111122223333444455556666777788889999aaaabbbbcc",
		BlockHex:  "deadbeefcafe",
		Status:    "pending",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Read the file immediately — fsync guarantees it's on disk
	info, err := os.Stat(wal.FilePath())
	if err != nil {
		t.Fatalf("WAL file not found after write: %v", err)
	}
	if info.Size() == 0 {
		t.Error("WAL file is empty after LogBlockFound")
	}
}

// TestBlockWAL_MultipleEntriesAppend verifies append-only behavior.
func TestBlockWAL_MultipleEntriesAppend(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Write 3 entries
	for i := uint64(0); i < 3; i++ {
		entry := &BlockWALEntry{
			Height:    800000 + i,
			BlockHash: "0000000000000000000aabbccddeeff00aabbccddeeff00aabbccddeeff00aabb",
			BlockHex:  "deadbeef",
		}
		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound #%d failed: %v", i, err)
		}
	}

	// Read all lines
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

	if nonEmpty != 3 {
		t.Errorf("Expected 3 entries, got %d", nonEmpty)
	}
}

// TestRecoverUnsubmittedBlocks_EmptyDir verifies graceful handling of empty data dir.
func TestRecoverUnsubmittedBlocks_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed on empty dir: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("Expected 0 unsubmitted blocks from empty dir, got %d", len(unsubmitted))
	}
}

// TestRecoverUnsubmittedBlocks_MalformedLines verifies that corrupt JSONL lines
// are skipped without error, and valid entries are still recovered.
func TestRecoverUnsubmittedBlocks_MalformedLines(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a mix of valid and invalid JSONL manually
	walFile := filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl")
	validEntry := `{"height":800001,"block_hash":"0000000000000000000aabbccddeeff00aabbccddeeff00aabbccddeeff00aabb","block_hex":"deadbeef","status":"pending"}`
	content := "THIS_IS_NOT_JSON\n" + validEntry + "\n{broken json\n"

	if err := os.WriteFile(walFile, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed with malformed lines: %v", err)
	}

	if len(unsubmitted) != 1 {
		t.Fatalf("Expected 1 unsubmitted block (ignoring corrupt lines), got %d", len(unsubmitted))
	}
	if unsubmitted[0].Height != 800001 {
		t.Errorf("Wrong height: got %d, want 800001", unsubmitted[0].Height)
	}
}

// TestLogBlockFound_PreservesPresetStatus verifies that LogBlockFound preserves
// a pre-set status (e.g. "build_failed") instead of overwriting it to "pending".
// This is critical for the emergency WAL path where handleBlock sets status
// before calling LogBlockFound.
func TestLogBlockFound_PreservesPresetStatus(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Entry with pre-set build_failed status (as handleBlock would set it)
	entry := &BlockWALEntry{
		Height:      800001,
		BlockHash:   "0000000000000000000aabbccddeeff001122334455667788aabbccddeeff0011",
		Status:      "build_failed",
		SubmitError: "invalid coinbase1",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	// Verify the entry was written with "build_failed", not overwritten to "pending"
	if entry.Status != "build_failed" {
		t.Errorf("LogBlockFound overwrote status: got %q, want %q", entry.Status, "build_failed")
	}

	// Also verify from file
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

	if recovered.Status != "build_failed" {
		t.Errorf("Recovered status mismatch: got %q, want %q", recovered.Status, "build_failed")
	}
}

// TestLogBlockFound_DefaultsToPending verifies that LogBlockFound defaults to
// "pending" status when no status is pre-set (the normal path).
func TestLogBlockFound_DefaultsToPending(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}
	defer wal.Close()

	// Entry with no status set (normal pre-submission path)
	entry := &BlockWALEntry{
		Height:    800001,
		BlockHash: "0000000000000000000aabbccddeeff001122334455667788aabbccddeeff0011",
		BlockHex:  "deadbeef",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}

	if entry.Status != "pending" {
		t.Errorf("Expected default status 'pending', got %q", entry.Status)
	}

	// Verify from file
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

	if recovered.Status != "pending" {
		t.Errorf("Recovered status mismatch: got %q, want %q", recovered.Status, "pending")
	}
}
