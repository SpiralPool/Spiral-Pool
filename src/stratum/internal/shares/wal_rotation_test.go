// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares — WAL rotation and archive cleanup tests.
//
// These tests exercise the Write-Ahead Log's filesystem operations using
// real temp directories. They cover:
//   - NewWAL creates directory structure and initial current.wal with magic header
//   - Write() persists entries with correct binary format
//   - rotate() archives current file and opens a fresh one
//   - cleanupArchives() keeps only the 3 most recent archives
//   - Close() flushes and shuts down cleanly
//   - Multiple rotations produce correct archive counts
package shares

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// =============================================================================
// NewWAL: initialization
// =============================================================================

// TestNewWAL_CreatesDirectoryAndFile verifies that NewWAL creates the WAL
// directory structure and initial current.wal file with magic header.
func TestNewWAL_CreatesDirectoryAndFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "test_pool", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	// Verify directory was created: dir/wal/test_pool/
	walDir := filepath.Join(dir, "wal", "test_pool")
	info, err := os.Stat(walDir)
	if err != nil {
		t.Fatalf("WAL directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("WAL path should be a directory")
	}

	// Verify current.wal exists
	walPath := filepath.Join(walDir, "current.wal")
	finfo, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("current.wal not created: %v", err)
	}

	// Magic header is "SPWAL001" = 8 bytes
	if finfo.Size() < int64(len(WALMagic)) {
		t.Errorf("current.wal size %d too small for magic header (%d bytes)", finfo.Size(), len(WALMagic))
	}

	// Verify WAL internal state
	if wal.poolID != "test_pool" {
		t.Errorf("poolID = %q, want %q", wal.poolID, "test_pool")
	}
	if wal.entryCount != 0 {
		t.Errorf("entryCount = %d, want 0 after initialization", wal.entryCount)
	}
}

// TestNewWAL_MagicHeaderContent verifies the actual magic header bytes.
func TestNewWAL_MagicHeaderContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "magic_test", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	// Flush the writer so magic header is on disk.
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync() failed: %v", err)
	}
	wal.Close()

	// Read and verify magic header
	walPath := filepath.Join(dir, "wal", "magic_test", "current.wal")
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("Failed to read current.wal: %v", err)
	}

	if len(data) < len(WALMagic) {
		t.Fatalf("File too short: %d bytes", len(data))
	}
	magic := string(data[:len(WALMagic)])
	if magic != WALMagic {
		t.Errorf("Magic header = %q, want %q", magic, WALMagic)
	}
}

// =============================================================================
// Write: entry persistence
// =============================================================================

// TestWALWrite_IncreasesEntryCount verifies that each Write() call
// increments the entry count and grows the file.
func TestWALWrite_IncreasesEntryCount(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "write_test", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	initialSize := wal.currentSize

	// Write 10 entries
	for i := 0; i < 10; i++ {
		share := &protocol.Share{
			MinerAddress: "test_miner",
			WorkerName:   "rig0",
			Difficulty:   1.0,
		}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Write() failed at entry %d: %v", i, err)
		}
	}

	if wal.entryCount != 10 {
		t.Errorf("entryCount = %d, want 10", wal.entryCount)
	}
	if wal.currentSize <= initialSize {
		t.Errorf("currentSize should have grown: initial=%d current=%d", initialSize, wal.currentSize)
	}
	if wal.written != 10 {
		t.Errorf("written metric = %d, want 10", wal.written)
	}
}

// TestWALWrite_NilFileReturnsError verifies that Write() returns an error
// when the WAL file is not open.
func TestWALWrite_NilFileReturnsError(t *testing.T) {
	t.Parallel()

	wal := &WAL{
		file: nil,
	}

	share := &protocol.Share{MinerAddress: "test"}
	err := wal.Write(share)
	if err == nil {
		t.Error("Write() should return error when file is nil")
	}
	if !strings.Contains(err.Error(), "WAL not open") {
		t.Errorf("Error should mention 'WAL not open', got: %v", err)
	}
}

// =============================================================================
// rotate(): archive creation and fresh file
// =============================================================================

// TestWALRotate_CreatesArchiveAndNewFile verifies that rotate() renames
// current.wal to an archive file and creates a fresh current.wal.
func TestWALRotate_CreatesArchiveAndNewFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "rotate_test", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	walDir := filepath.Join(dir, "wal", "rotate_test")

	// Write some entries so the file has content
	for i := 0; i < 5; i++ {
		share := &protocol.Share{MinerAddress: "miner", WorkerName: "rig0"}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Write() failed: %v", err)
		}
	}

	preRotateEntryCount := wal.entryCount

	// Perform rotation
	if err := wal.rotate(); err != nil {
		t.Fatalf("rotate() failed: %v", err)
	}

	// Verify archive file was created
	archives, err := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if len(archives) != 1 {
		t.Errorf("Expected 1 archive file after rotation, got %d", len(archives))
	}

	// Verify new current.wal exists
	currentPath := filepath.Join(walDir, "current.wal")
	info, err := os.Stat(currentPath)
	if err != nil {
		t.Fatalf("New current.wal not created after rotation: %v", err)
	}

	// New file should have just the magic header
	if info.Size() != int64(len(WALMagic)) {
		t.Errorf("New current.wal size = %d, want %d (magic header only)", info.Size(), len(WALMagic))
	}

	// Entry count should have been reset to 0
	if wal.entryCount != 0 {
		t.Errorf("entryCount = %d after rotation, want 0", wal.entryCount)
	}

	// Verify pre-rotation entry count was non-zero
	if preRotateEntryCount != 5 {
		t.Errorf("preRotateEntryCount = %d, want 5", preRotateEntryCount)
	}
}

// TestWALRotate_ArchiveContainsData verifies that the archive file
// contains the data from before rotation.
func TestWALRotate_ArchiveContainsData(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "archive_data_test", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	walDir := filepath.Join(dir, "wal", "archive_data_test")

	// Write entries and capture file size before rotation
	for i := 0; i < 20; i++ {
		share := &protocol.Share{MinerAddress: "miner", WorkerName: "rig0", Difficulty: float64(i)}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Write() failed: %v", err)
		}
	}
	preRotateSize := wal.currentSize

	if err := wal.rotate(); err != nil {
		t.Fatalf("rotate() failed: %v", err)
	}

	// Archive should have approximately the same size as the pre-rotation file
	archives, _ := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))
	if len(archives) == 0 {
		t.Fatal("No archive files found")
	}

	archiveInfo, err := os.Stat(archives[0])
	if err != nil {
		t.Fatalf("Failed to stat archive: %v", err)
	}

	// Archive should contain all the pre-rotation data
	if archiveInfo.Size() != preRotateSize {
		t.Errorf("Archive size = %d, expected %d (pre-rotation size)", archiveInfo.Size(), preRotateSize)
	}
}

// TestWALRotate_WritesAfterRotation verifies that new writes succeed
// after a rotation.
func TestWALRotate_WritesAfterRotation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "post_rotate_write", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	// Write, rotate, write more
	for i := 0; i < 5; i++ {
		share := &protocol.Share{MinerAddress: "miner", WorkerName: "rig0"}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Pre-rotation Write() failed: %v", err)
		}
	}

	if err := wal.rotate(); err != nil {
		t.Fatalf("rotate() failed: %v", err)
	}

	// Write more entries after rotation
	for i := 0; i < 5; i++ {
		share := &protocol.Share{MinerAddress: "miner_after", WorkerName: "rig1"}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Post-rotation Write() failed at entry %d: %v", i, err)
		}
	}

	// Entry count should reflect only post-rotation writes
	if wal.entryCount != 5 {
		t.Errorf("entryCount = %d after post-rotation writes, want 5", wal.entryCount)
	}
}

// =============================================================================
// cleanupArchives(): keeps only 3 most recent
// =============================================================================

// TestCleanupArchives_KeepsThreeMostRecent verifies that cleanupArchives()
// removes old archives and keeps only the 3 most recent ones.
func TestCleanupArchives_KeepsThreeMostRecent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "cleanup_test", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	walDir := filepath.Join(dir, "wal", "cleanup_test")

	// Perform 5 rotations (creates 5 archive files).
	// Need a small sleep between rotations so timestamps in filenames differ.
	for i := 0; i < 5; i++ {
		// Write at least one entry before each rotation
		share := &protocol.Share{MinerAddress: "miner", WorkerName: "rig0"}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Write() failed before rotation %d: %v", i, err)
		}

		if err := wal.rotate(); err != nil {
			t.Fatalf("rotate() %d failed: %v", i, err)
		}
		// Small sleep to ensure unique archive timestamps
		time.Sleep(2 * time.Millisecond)
	}

	// cleanupArchives is called by rotate(), so after 5 rotations,
	// it should have cleaned up old archives and kept at most 3.
	archives, err := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}

	if len(archives) > 3 {
		t.Errorf("Expected at most 3 archive files, got %d", len(archives))
		for _, a := range archives {
			t.Logf("  archive: %s", filepath.Base(a))
		}
	}
}

// TestCleanupArchives_NoArchives_NoPanic verifies that cleanupArchives()
// doesn't panic when there are no archive files.
func TestCleanupArchives_NoArchives_NoPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "no_archives", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	// Call cleanupArchives directly — should not panic
	wal.cleanupArchives()

	// Verify no archives exist (and none were erroneously created)
	walDir := filepath.Join(dir, "wal", "no_archives")
	archives, _ := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))
	if len(archives) != 0 {
		t.Errorf("Expected 0 archives, got %d", len(archives))
	}
}

// TestCleanupArchives_ExactlyThree_NoRemoval verifies that cleanupArchives()
// does not remove any files when exactly 3 archives exist.
func TestCleanupArchives_ExactlyThree_NoRemoval(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "three_archives", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	walDir := filepath.Join(dir, "wal", "three_archives")

	// Manually create exactly 3 archive files
	for i := 0; i < 3; i++ {
		archivePath := filepath.Join(walDir, "archive_"+time.Now().Add(time.Duration(i)*time.Millisecond).Format("20060102150405.000000000")+".wal")
		if err := os.WriteFile(archivePath, []byte("test"), 0640); err != nil {
			t.Fatalf("Failed to create test archive: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	archivesBefore, _ := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))

	wal.cleanupArchives()

	archivesAfter, _ := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))
	if len(archivesAfter) != len(archivesBefore) {
		t.Errorf("cleanupArchives removed files when count was %d (should keep all ≤3)", len(archivesBefore))
	}
}

// =============================================================================
// Close: clean shutdown
// =============================================================================

// TestWALClose_FlushesAndCloses verifies that Close() properly flushes
// buffered data and closes the file.
func TestWALClose_FlushesAndCloses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "close_test", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}

	// Write some entries
	for i := 0; i < 10; i++ {
		share := &protocol.Share{MinerAddress: "miner", WorkerName: "rig0"}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Write() failed: %v", err)
		}
	}

	// Close should not error
	if err := wal.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Verify file still exists with data
	walPath := filepath.Join(dir, "wal", "close_test", "current.wal")
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("current.wal should exist after Close: %v", err)
	}
	if info.Size() <= int64(len(WALMagic)) {
		t.Errorf("current.wal should contain data after writes + close, size=%d", info.Size())
	}
}

// TestWALClose_DoubleCloseNoPanic verifies that calling Close() twice
// does not panic (graceful handling of double-close).
func TestWALClose_DoubleCloseNoPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "double_close", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}

	// First close
	if err := wal.Close(); err != nil {
		t.Fatalf("First Close() failed: %v", err)
	}

	// Second close — should not panic
	// (may return error, but should not panic)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Second Close() panicked: %v", r)
		}
	}()
	_ = wal.Close()
}

// =============================================================================
// Multiple rotations: stress test
// =============================================================================

// TestWALMultipleRotations_ArchiveCleanupWorks verifies correct behavior
// through many consecutive rotations.
func TestWALMultipleRotations_ArchiveCleanupWorks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "multi_rotate", logger)
	if err != nil {
		t.Fatalf("NewWAL() failed: %v", err)
	}
	defer wal.Close()

	walDir := filepath.Join(dir, "wal", "multi_rotate")

	// Perform 10 rotations
	for i := 0; i < 10; i++ {
		share := &protocol.Share{MinerAddress: "miner", WorkerName: "rig0"}
		if err := wal.Write(share); err != nil {
			t.Fatalf("Write() %d failed: %v", i, err)
		}
		if err := wal.rotate(); err != nil {
			t.Fatalf("rotate() %d failed: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// After 10 rotations with cleanup, should have at most 3 archives
	archives, _ := filepath.Glob(filepath.Join(walDir, "archive_*.wal"))
	if len(archives) > 3 {
		t.Errorf("After 10 rotations, expected ≤3 archives, got %d", len(archives))
	}

	// Plus current.wal should exist
	if _, err := os.Stat(filepath.Join(walDir, "current.wal")); err != nil {
		t.Errorf("current.wal should exist after rotations: %v", err)
	}

	// Total file count: archives + current.wal
	totalFiles := len(archives) + 1
	if totalFiles > 4 {
		t.Errorf("Total WAL files = %d, expected ≤4 (3 archives + current)", totalFiles)
	}
}
