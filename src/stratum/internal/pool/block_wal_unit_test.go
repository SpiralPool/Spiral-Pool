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
// NewBlockWAL — Initialization, file creation, locking
// =============================================================================

func TestNewBlockWAL_CreatesDirectoryAndFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	walDir := filepath.Join(dir, "wal", "nested")

	logger, _ := zap.NewDevelopment()
	wal, err := NewBlockWAL(walDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	// Directory should exist
	if _, err := os.Stat(walDir); os.IsNotExist(err) {
		t.Error("WAL directory was not created")
	}

	// File should exist
	if _, err := os.Stat(wal.FilePath()); os.IsNotExist(err) {
		t.Error("WAL file was not created")
	}

	// Filename should contain today's date
	base := filepath.Base(wal.FilePath())
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(base, today) {
		t.Errorf("WAL filename %q should contain today's date %q", base, today)
	}

	// Should be .jsonl extension
	if !strings.HasSuffix(base, ".jsonl") {
		t.Errorf("WAL filename %q should end with .jsonl", base)
	}
}

func TestNewBlockWAL_FileLockPreventsSecondInstance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal1, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("first WAL creation failed: %v", err)
	}
	defer wal1.Close()

	// Second instance should fail with lock error
	_, err = NewBlockWAL(dir, logger)
	if err == nil {
		t.Fatal("expected error creating second WAL instance (file lock), got nil")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("expected lock error, got: %v", err)
	}
}

func TestNewBlockWAL_FailsOnInvalidPath(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	// NUL is invalid on Windows, /dev/null/subdir invalid on Unix
	_, err := NewBlockWAL(string([]byte{0}), logger)
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

// =============================================================================
// LogBlockFound — Entry writing, fsync, status defaults
// =============================================================================

func TestLogBlockFound_WritesValidJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:       100000,
		BlockHash:    "0000000000000000000abcdef1234567890abcdef1234567890abcdef12345678",
		PrevHash:     "000000000000000000011111111111111111111111111111111111111111111111",
		BlockHex:     "deadbeef",
		MinerAddress: "DTestAddress",
		WorkerName:   "worker1",
		JobID:        "job123",
		JobAge:       5 * time.Second,
		CoinbaseValue: 100000000,
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound error: %v", err)
	}

	// Read the file and verify it's valid JSONL
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var parsed BlockWALEntry
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("invalid JSON in WAL: %v", err)
	}

	if parsed.Height != 100000 {
		t.Errorf("expected height 100000, got %d", parsed.Height)
	}
	if parsed.BlockHash != entry.BlockHash {
		t.Errorf("block hash mismatch")
	}
	if parsed.MinerAddress != "DTestAddress" {
		t.Errorf("miner address mismatch")
	}
}

func TestLogBlockFound_DefaultsStatusToPending(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    1,
		BlockHash: "abc123",
		BlockHex:  "ff",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound error: %v", err)
	}

	// Status should default to "pending"
	if entry.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", entry.Status)
	}

	// Timestamp should be set
	if entry.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

func TestLogBlockFound_PreservesExplicitStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    1,
		BlockHash: "abc123",
		BlockHex:  "ff",
		Status:    "submitting",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound error: %v", err)
	}

	if entry.Status != "submitting" {
		t.Errorf("expected status 'submitting' preserved, got %q", entry.Status)
	}
}

func TestLogBlockFound_MultipleEntriesAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	for i := 0; i < 5; i++ {
		entry := &BlockWALEntry{
			Height:    uint64(100 + i),
			BlockHash: "hash" + string(rune('A'+i)),
			BlockHex:  "ff",
		}
		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("LogBlockFound error on entry %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

// =============================================================================
// LogSubmissionResult — Result writing
// =============================================================================

func TestLogSubmissionResult_WritesResultEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	// Write initial entry
	entry := &BlockWALEntry{
		Height:    100,
		BlockHash: "hash100",
		BlockHex:  "ff",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound error: %v", err)
	}

	// Write result
	entry.Status = "accepted"
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult error: %v", err)
	}

	// Should have SubmittedAt set
	if entry.SubmittedAt == "" {
		t.Error("expected SubmittedAt to be set")
	}

	// File should have 2 lines
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (found + result), got %d", len(lines))
	}
}

// =============================================================================
// RecoverUnsubmittedBlocks — Recovery logic
// =============================================================================

func TestRecoverUnsubmittedBlocks_FindsPendingEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Write a WAL file with mixed statuses
	walFile := filepath.Join(dir, "block_wal_2026-01-25.jsonl")
	entries := []BlockWALEntry{
		{Height: 100, BlockHash: "hash100", Status: "pending", Timestamp: time.Now().Add(-3 * time.Minute)},
		{Height: 101, BlockHash: "hash101", Status: "accepted", Timestamp: time.Now().Add(-2 * time.Minute)},
		{Height: 102, BlockHash: "hash102", Status: "submitting", Timestamp: time.Now().Add(-1 * time.Minute)},
		{Height: 103, BlockHash: "hash103", Status: "rejected", Timestamp: time.Now()},
	}

	f, err := os.Create(walFile)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	for _, e := range entries {
		data, _ := json.Marshal(e)
		f.Write(append(data, '\n'))
	}
	f.Close()

	recovered, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks error: %v", err)
	}

	// Should find "pending" (hash100) and "submitting" (hash102)
	if len(recovered) != 2 {
		t.Fatalf("expected 2 unsubmitted entries, got %d", len(recovered))
	}

	// Should be sorted by timestamp ascending (oldest first)
	if recovered[0].Height != 100 {
		t.Errorf("expected oldest entry first (height 100), got %d", recovered[0].Height)
	}
	if recovered[1].Height != 102 {
		t.Errorf("expected second entry (height 102), got %d", recovered[1].Height)
	}
}

func TestRecoverUnsubmittedBlocks_LatestEntryWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Same block hash appears twice — latest entry should win
	walFile := filepath.Join(dir, "block_wal_2026-01-25.jsonl")
	entries := []BlockWALEntry{
		{Height: 100, BlockHash: "samehash", Status: "pending", Timestamp: time.Now().Add(-5 * time.Minute)},
		{Height: 100, BlockHash: "samehash", Status: "accepted", Timestamp: time.Now()}, // Later: accepted
	}

	f, _ := os.Create(walFile)
	for _, e := range entries {
		data, _ := json.Marshal(e)
		f.Write(append(data, '\n'))
	}
	f.Close()

	recovered, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks error: %v", err)
	}

	// Should find 0 — the latest entry is "accepted", overriding "pending"
	if len(recovered) != 0 {
		t.Errorf("expected 0 unsubmitted (latest is accepted), got %d", len(recovered))
	}
}

func TestRecoverUnsubmittedBlocks_EmptyDirReturnsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	recovered, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks error: %v", err)
	}

	if len(recovered) != 0 {
		t.Errorf("expected 0 entries from empty dir, got %d", len(recovered))
	}
}

func TestRecoverUnsubmittedBlocks_SkipsMalformedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	walFile := filepath.Join(dir, "block_wal_2026-01-25.jsonl")
	content := `{"height":100,"block_hash":"hash100","status":"pending","timestamp":"2026-01-25T12:00:00Z"}
this is not json
{"height":101,"block_hash":"hash101","status":"submitting","timestamp":"2026-01-25T12:01:00Z"}
`
	os.WriteFile(walFile, []byte(content), 0644)

	recovered, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks error: %v", err)
	}

	// Should recover 2 valid entries, skipping the malformed line
	if len(recovered) != 2 {
		t.Errorf("expected 2 recovered entries (skipping malformed), got %d", len(recovered))
	}
}

func TestRecoverUnsubmittedBlocks_MultipleWALFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Write entries across multiple WAL files
	for _, date := range []string{"2026-01-23", "2026-01-24", "2026-01-25"} {
		walFile := filepath.Join(dir, "block_wal_"+date+".jsonl")
		entry := BlockWALEntry{
			Height:    100,
			BlockHash: "hash_" + date,
			Status:    "pending",
			Timestamp: time.Now(),
		}
		data, _ := json.Marshal(entry)
		os.WriteFile(walFile, append(data, '\n'), 0644)
	}

	recovered, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if len(recovered) != 3 {
		t.Errorf("expected 3 entries from 3 WAL files, got %d", len(recovered))
	}
}

// =============================================================================
// RecoverSubmittedBlocks — Submitted/accepted block recovery
// =============================================================================

func TestRecoverSubmittedBlocks_FindsAcceptedAndSubmitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	walFile := filepath.Join(dir, "block_wal_2026-01-25.jsonl")
	entries := []BlockWALEntry{
		{Height: 100, BlockHash: "hash100", Status: "submitted", Timestamp: time.Now().Add(-2 * time.Minute)},
		{Height: 101, BlockHash: "hash101", Status: "accepted", Timestamp: time.Now().Add(-1 * time.Minute)},
		{Height: 102, BlockHash: "hash102", Status: "pending", Timestamp: time.Now()},
		{Height: 103, BlockHash: "hash103", Status: "rejected", Timestamp: time.Now()},
	}

	f, _ := os.Create(walFile)
	for _, e := range entries {
		data, _ := json.Marshal(e)
		f.Write(append(data, '\n'))
	}
	f.Close()

	submitted, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if len(submitted) != 2 {
		t.Fatalf("expected 2 submitted entries, got %d", len(submitted))
	}

	// Should be sorted by timestamp
	if submitted[0].Height != 100 {
		t.Errorf("expected oldest first (100), got %d", submitted[0].Height)
	}
}

// =============================================================================
// splitLines — Line splitting with \n and \r\n
// =============================================================================

func TestSplitLines_UnixNewlines(t *testing.T) {
	t.Parallel()
	data := []byte("line1\nline2\nline3\n")
	lines := splitLines(data)

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if string(lines[0]) != "line1" {
		t.Errorf("line 0: expected 'line1', got %q", string(lines[0]))
	}
	if string(lines[2]) != "line3" {
		t.Errorf("line 2: expected 'line3', got %q", string(lines[2]))
	}
}

func TestSplitLines_WindowsNewlines(t *testing.T) {
	t.Parallel()
	data := []byte("line1\r\nline2\r\nline3\r\n")
	lines := splitLines(data)

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	// Should strip \r
	if string(lines[0]) != "line1" {
		t.Errorf("line 0: expected 'line1', got %q", string(lines[0]))
	}
}

func TestSplitLines_MixedNewlines(t *testing.T) {
	t.Parallel()
	data := []byte("line1\nline2\r\nline3\n")
	lines := splitLines(data)

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if string(lines[1]) != "line2" {
		t.Errorf("line 1: expected 'line2', got %q", string(lines[1]))
	}
}

func TestSplitLines_NoTrailingNewlinePreservesLast(t *testing.T) {
	t.Parallel()
	data := []byte("line1\nline2")
	lines := splitLines(data)

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if string(lines[1]) != "line2" {
		t.Errorf("line 1: expected 'line2', got %q", string(lines[1]))
	}
}

func TestSplitLines_EmptyInput(t *testing.T) {
	t.Parallel()
	lines := splitLines([]byte{})
	if len(lines) != 0 {
		t.Errorf("expected 0 lines from empty input, got %d", len(lines))
	}
}

func TestSplitLines_SingleNewline(t *testing.T) {
	t.Parallel()
	lines := splitLines([]byte("\n"))
	// Should produce one empty line before the newline
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(lines))
	}
}

// =============================================================================
// CountWALFiles — File counting
// =============================================================================

func TestCountWALFiles_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	count, err := CountWALFiles(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestCountWALFiles_CountsCorrectly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create WAL files
	for _, name := range []string{
		"block_wal_2026-01-23.jsonl",
		"block_wal_2026-01-24.jsonl",
		"block_wal_2026-01-25.jsonl",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0644)
	}

	// Create a non-WAL file (should be ignored)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("ignored"), 0644)

	count, err := CountWALFiles(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

// =============================================================================
// CleanupOldWALFiles — Date-based retention
// =============================================================================

func TestCleanupOldWALFiles_RemovesOldFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create files with various dates
	oldDate := time.Now().AddDate(0, 0, -60).Format("2006-01-02")
	recentDate := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	todayDate := time.Now().Format("2006-01-02")

	os.WriteFile(filepath.Join(dir, "block_wal_"+oldDate+".jsonl"), []byte("{}"), 0644)
	os.WriteFile(filepath.Join(dir, "block_wal_"+recentDate+".jsonl"), []byte("{}"), 0644)
	os.WriteFile(filepath.Join(dir, "block_wal_"+todayDate+".jsonl"), []byte("{}"), 0644)

	cleaned, err := CleanupOldWALFiles(dir, 30)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Only the 60-day-old file should be cleaned
	if cleaned != 1 {
		t.Errorf("expected 1 file cleaned, got %d", cleaned)
	}

	// Verify the old file is gone
	if _, err := os.Stat(filepath.Join(dir, "block_wal_"+oldDate+".jsonl")); !os.IsNotExist(err) {
		t.Error("old WAL file should have been deleted")
	}

	// Verify recent files remain
	if _, err := os.Stat(filepath.Join(dir, "block_wal_"+recentDate+".jsonl")); os.IsNotExist(err) {
		t.Error("recent WAL file should NOT have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "block_wal_"+todayDate+".jsonl")); os.IsNotExist(err) {
		t.Error("today's WAL file should NOT have been deleted")
	}
}

func TestCleanupOldWALFiles_DefaultRetention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// retentionDays <= 0 should default to DefaultWALRetentionDays (30)
	cleaned, err := CleanupOldWALFiles(dir, 0)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned from empty dir, got %d", cleaned)
	}
}

func TestCleanupOldWALFiles_SkipsMalformedFilenames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a WAL file with non-date name
	os.WriteFile(filepath.Join(dir, "block_wal_notadate.jsonl"), []byte("{}"), 0644)

	cleaned, err := CleanupOldWALFiles(dir, 1)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("malformed filename should be skipped, got cleaned=%d", cleaned)
	}

	// File should still exist
	if _, err := os.Stat(filepath.Join(dir, "block_wal_notadate.jsonl")); os.IsNotExist(err) {
		t.Error("malformed WAL file should NOT have been deleted")
	}
}

// =============================================================================
// Close / FlushToDisk / FilePath — Lifecycle
// =============================================================================

func TestClose_SyncsAndClosesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}

	fp := wal.FilePath()
	if fp == "" {
		t.Error("FilePath should not be empty")
	}

	if err := wal.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// Double close should not error
	if err := wal.Close(); err != nil {
		t.Errorf("second Close should not error, got: %v", err)
	}
}

func TestFlushToDisk_DoesNotErrorOnOpenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}
	defer wal.Close()

	if err := wal.FlushToDisk(); err != nil {
		t.Errorf("FlushToDisk error: %v", err)
	}
}

// =============================================================================
// End-to-end: Write -> Close -> Recover
// =============================================================================

func TestWAL_EndToEnd_WriteCloseRecover(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logger, _ := zap.NewDevelopment()

	// Phase 1: Write entries
	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL error: %v", err)
	}

	pendingEntry := &BlockWALEntry{
		Height:       50000,
		BlockHash:    "deadbeef50000",
		BlockHex:     "ffff",
		MinerAddress: "DMiner1",
		Status:       "submitting",
	}
	if err := wal.LogBlockFound(pendingEntry); err != nil {
		t.Fatalf("LogBlockFound error: %v", err)
	}

	acceptedEntry := &BlockWALEntry{
		Height:    50001,
		BlockHash: "deadbeef50001",
		BlockHex:  "aaaa",
	}
	if err := wal.LogBlockFound(acceptedEntry); err != nil {
		t.Fatalf("LogBlockFound error: %v", err)
	}
	acceptedEntry.Status = "accepted"
	if err := wal.LogSubmissionResult(acceptedEntry); err != nil {
		t.Fatalf("LogSubmissionResult error: %v", err)
	}

	// Phase 2: Close (simulates process exit)
	wal.Close()

	// Phase 3: Recover
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks error: %v", err)
	}

	// Should find the "submitting" entry but not the "accepted" one
	if len(unsubmitted) != 1 {
		t.Fatalf("expected 1 unsubmitted, got %d", len(unsubmitted))
	}
	if unsubmitted[0].BlockHash != "deadbeef50000" {
		t.Errorf("expected deadbeef50000, got %s", unsubmitted[0].BlockHash)
	}
	if unsubmitted[0].MinerAddress != "DMiner1" {
		t.Errorf("expected DMiner1, got %s", unsubmitted[0].MinerAddress)
	}

	// Also verify submitted blocks recovery
	submitted, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks error: %v", err)
	}
	if len(submitted) != 1 {
		t.Fatalf("expected 1 submitted, got %d", len(submitted))
	}
	if submitted[0].BlockHash != "deadbeef50001" {
		t.Errorf("expected deadbeef50001, got %s", submitted[0].BlockHash)
	}
}
