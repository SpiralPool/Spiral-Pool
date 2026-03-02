// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// TestBlockWAL_FlushToDisk_NoData verifies FlushToDisk succeeds on an
// empty WAL (no entries written yet).
func TestBlockWAL_FlushToDisk_NoData(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}
	defer wal.Close()

	// FlushToDisk on empty WAL should succeed
	if err := wal.FlushToDisk(); err != nil {
		t.Errorf("FlushToDisk on empty WAL: %v", err)
	}
}

// TestBlockWAL_FlushToDisk_WithEntry verifies FlushToDisk persists data
// after writing a WAL entry.
func TestBlockWAL_FlushToDisk_WithEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}
	defer wal.Close()

	// Write an entry
	entry := &BlockWALEntry{
		Height:       100,
		BlockHash:    "abc123",
		BlockHex:     "deadbeef",
		MinerAddress: "miner1",
		WorkerName:   "worker1",
		JobID:        "job1",
		Status:       "submitted",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound: %v", err)
	}

	// FlushToDisk should succeed
	if err := wal.FlushToDisk(); err != nil {
		t.Errorf("FlushToDisk after write: %v", err)
	}

	// Verify the WAL file exists and has content
	walPath := wal.FilePath()
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("WAL file stat: %v", err)
	}
	if info.Size() == 0 {
		t.Error("WAL file should have content after write + flush")
	}
}

// TestBlockWAL_FlushToDisk_MultipleFlushes verifies multiple consecutive
// FlushToDisk calls don't corrupt the WAL.
func TestBlockWAL_FlushToDisk_MultipleFlushes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}
	defer wal.Close()

	// Write entry
	entry := &BlockWALEntry{
		Height:       200,
		BlockHash:    "def456",
		BlockHex:     "cafebabe",
		MinerAddress: "miner2",
		WorkerName:   "worker2",
		JobID:        "job2",
		Status:       "pending",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound: %v", err)
	}

	// Multiple flushes should all succeed
	for i := 0; i < 5; i++ {
		if err := wal.FlushToDisk(); err != nil {
			t.Errorf("FlushToDisk attempt %d: %v", i+1, err)
		}
	}
}

// TestBlockWAL_FlushToDisk_AfterClose verifies FlushToDisk after Close
// returns nil (graceful no-op when file is nil).
func TestBlockWAL_FlushToDisk_AfterClose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}

	// Close the WAL
	wal.Close()

	// FlushToDisk after Close should return nil (no file to flush)
	if err := wal.FlushToDisk(); err != nil {
		t.Errorf("FlushToDisk after Close: %v", err)
	}
}

// TestBlockWAL_FilePath_InDirectory verifies the WAL file is created
// in the specified directory.
func TestBlockWAL_FilePath_InDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}
	defer wal.Close()

	walPath := wal.FilePath()
	if walPath == "" {
		t.Fatal("FilePath() returned empty string")
	}

	// Verify the WAL is in the expected directory
	walDir := filepath.Dir(walPath)
	if walDir != dir {
		t.Errorf("WAL directory: got %q, want %q", walDir, dir)
	}
}
