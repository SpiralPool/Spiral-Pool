// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos tests for WAL (Write-Ahead Log) edge cases.
//
// TEST 1: WAL Rotation Crash Under Disk Exhaustion
// TEST 7: WAL Commit Failure Causing Replay Duplicates
package shares

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// TestChaos_WAL_RotationDiskExhaustion verifies WAL behavior when rotation
// fails due to disk exhaustion (read-only directory). Exposes a bug where
// rotate() closes the file before Rename, leaving WAL in a broken state
// when Rename fails - subsequent writes go to a closed fd's buffer.
//
// TARGET: wal.go:366-399 (rotate), wal.go:182-186 (rotation trigger in Write)
// INVARIANT: Shares written before rotation failure must be recoverable via Replay.
func TestChaos_WAL_RotationDiskExhaustion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping disk permission test on Windows")
	}

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "chaos-rotation", logger)
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	// Write initial shares and sync to disk
	const preRotationShares = 10
	for i := 0; i < preRotationShares; i++ {
		if err := wal.Write(chaosTestShare(i)); err != nil {
			t.Fatalf("Initial write %d failed: %v", i, err)
		}
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Force currentSize past MaxWALSize to trigger rotation on next Write
	wal.mu.Lock()
	wal.currentSize = MaxWALSize + 1
	wal.mu.Unlock()

	// Make WAL directory read-only to block rotation's Rename + new file creation
	walDir := filepath.Join(dir, "wal", "chaos-rotation")
	if err := os.Chmod(walDir, 0555); err != nil {
		t.Fatalf("Failed to make dir read-only: %v", err)
	}
	defer os.Chmod(walDir, 0750) // nolint: errcheck

	// This Write triggers rotation. Rotation sequence:
	// 1. rotate() Flush/Sync/Close current file (succeeds - fd already open)
	// 2. rotate() os.Rename (FAILS - dir is read-only)
	// 3. rotate() returns error, Write() logs warning and continues
	// BUG: After Close(), w.file is closed but w.writer wraps the closed fd.
	// Writes go to the bufio buffer but can never be flushed.
	err = wal.Write(chaosTestShare(100))
	t.Logf("Write after rotation failure: err=%v", err)

	// Try more writes to fill the 64KB bufio buffer and expose the closed-fd issue
	var postRotationWrites int
	var firstWriteErr error
	for i := 101; i < 500; i++ {
		if werr := wal.Write(chaosTestShare(i)); werr != nil {
			firstWriteErr = werr
			break
		}
		postRotationWrites++
	}
	t.Logf("Post-rotation writes before error: %d, firstErr=%v", postRotationWrites, firstWriteErr)

	// Restore permissions for cleanup
	os.Chmod(walDir, 0750) // nolint: errcheck

	// Force-close WAL (Close may fail due to broken state)
	closeErr := wal.Close()
	t.Logf("WAL close: err=%v", closeErr)

	// Reopen WAL and replay - verify pre-rotation shares survived
	wal2, err := NewWAL(dir, "chaos-rotation", logger)
	if err != nil {
		t.Fatalf("Failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	// Pre-rotation shares (0-9) were flushed+synced to disk and MUST survive.
	// Post-rotation shares (100+) are likely lost (written to closed fd's buffer).
	if len(replayed) < preRotationShares {
		t.Errorf("CRITICAL: Lost pre-rotation shares! Replayed %d, want >= %d",
			len(replayed), preRotationShares)
	}

	t.Logf("RESULTS: Replayed %d shares total (%d pre-rotation expected)",
		len(replayed), preRotationShares)

	if postRotationWrites > 0 && len(replayed) <= preRotationShares {
		t.Logf("FINDING: %d post-rotation writes appeared to succeed but data is lost",
			postRotationWrites)
		t.Logf("ROOT CAUSE: rotate() closes file before Rename, leaving WAL broken on Rename failure")
	}
}

// TestChaos_WAL_CommitSyncFailureDuplicates verifies that when a WAL commit
// marker is buffered but NOT synced to disk (simulating crash), replay returns
// all shares as uncommitted, causing duplicates if the DB already has them.
//
// TARGET: wal.go:213-252 (CommitBatch/CommitBatchVerified), wal.go:256-363 (Replay)
// INVARIANT: Crash between CommitBatch and Sync produces replay duplicates, not data loss.
func TestChaos_WAL_CommitSyncFailureDuplicates(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "chaos-commit", logger)
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	// Write shares and sync them to disk (shares are durable after sync)
	const shareCount = 50
	shares := make([]*protocol.Share, shareCount)
	for i := 0; i < shareCount; i++ {
		shares[i] = chaosTestShare(i)
		if err := wal.Write(shares[i]); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Commit batch using CommitBatch (NOT CommitBatchVerified).
	// This writes the commit marker to the bufio.Writer buffer only - NOT synced to disk.
	if err := wal.CommitBatch(shares); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}

	// SIMULATE CRASH:
	// 1. Stop the sync goroutine cleanly
	// 2. Close the file descriptor WITHOUT flushing the bufio buffer
	// The commit marker is in the buffer but NOT on disk.
	close(wal.closeChan)
	wal.syncTicker.Stop()
	wal.wg.Wait()

	wal.mu.Lock()
	if wal.file != nil {
		// Bypass bufio.Writer flush - simulates crash losing buffered data
		wal.file.Close()
		wal.file = nil
		wal.writer = nil
	}
	wal.mu.Unlock()

	// Reopen WAL and replay - simulates restart after crash
	wal2, err := NewWAL(dir, "chaos-commit", logger)
	if err != nil {
		t.Fatalf("Failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	// All shares should replay as uncommitted because the commit marker
	// was in the buffer, not on disk. In production, these would be
	// re-inserted into the DB, creating duplicates.
	if len(replayed) != shareCount {
		t.Errorf("Replay returned %d shares, want %d (commit marker should be lost)",
			len(replayed), shareCount)
	}

	if len(replayed) == shareCount {
		t.Logf("CONFIRMED: Crash between CommitBatch and Sync loses commit marker.")
		t.Logf("All %d shares replay as uncommitted = potential duplicates if already in DB.", shareCount)
		t.Logf("MITIGATION: Production uses CommitBatchVerified() which forces sync.")
	} else if len(replayed) == 0 {
		t.Logf("UNEXPECTED: Commit marker survived crash simulation (buffer was flushed?)")
	}
}
