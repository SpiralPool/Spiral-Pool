// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for WAL truncated mid-entry recovery.
//
// TEST A: WAL Truncated Mid-Entry Recovery
// Simulates a file truncated during write (power loss, disk error) and verifies
// Replay recovers all complete entries and cleanly stops at the truncation.
// Neither TestChaos_WAL_RotationDiskExhaustion nor TestChaos_WAL_CommitSyncFailureDuplicates
// tests this scenario — both operate on structurally complete WAL files.
package shares

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// TestChaos_WAL_TruncatedMidEntryRecovery verifies Replay behavior when the
// WAL file is truncated mid-entry, simulating power loss during fsync.
//
// TARGET: wal.go:256-363 (Replay), specifically:
//   - Line 292-299: header ReadFull (truncated header path)
//   - Line 305-309: data ReadFull (truncated data path)
//
// INVARIANT:
//   - All complete entries before truncation are recovered
//   - No partial or corrupt entries returned
//   - No panics
//   - No duplicate entries
//
// RUN WITH: go test -race -run TestChaos_WAL_TruncatedMidEntryRecovery
func TestChaos_WAL_TruncatedMidEntryRecovery(t *testing.T) {
	t.Run("MidData", func(t *testing.T) {
		// Truncation cuts entry data: header ReadFull succeeds, data ReadFull fails
		testWALTruncation(t, false)
	})
	t.Run("MidHeader", func(t *testing.T) {
		// Truncation cuts entry header: header ReadFull fails with ErrUnexpectedEOF
		testWALTruncation(t, true)
	})
}

// testWALTruncation writes shares, truncates the WAL file at a precise point,
// and verifies Replay recovers all complete entries.
func testWALTruncation(t *testing.T, truncateHeader bool) {
	t.Helper()

	dir := t.TempDir()
	logger := zap.NewNop()

	const safeShares = 19

	// Phase 1: Write safeShares entries and sync to disk
	wal, err := NewWAL(dir, "chaos-truncate", logger)
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	for i := 0; i < safeShares; i++ {
		if err := wal.Write(chaosTestShare(i)); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync after safe writes failed: %v", err)
	}

	// Record file size with all safe entries persisted
	wal.mu.Lock()
	sizeAfterSafe := wal.currentSize
	wal.mu.Unlock()

	// Write one more entry (this will be the truncation victim)
	if err := wal.Write(chaosTestShare(safeShares)); err != nil {
		t.Fatalf("Write %d failed: %v", safeShares, err)
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync after last write failed: %v", err)
	}

	wal.mu.Lock()
	fullSize := wal.currentSize
	wal.mu.Unlock()

	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Phase 2: Truncate the WAL file to simulate crash mid-write
	walPath := filepath.Join(dir, "wal", "chaos-truncate", "current.wal")

	var truncateSize int64
	if truncateHeader {
		// Cut in the middle of the last entry's header.
		// Entry header is 8 bytes: [magic:4][length:4].
		// Leave only 4 bytes → io.ReadFull(reader, header) gets ErrUnexpectedEOF.
		truncateSize = sizeAfterSafe + 4
	} else {
		// Cut in the middle of the last entry's JSON data.
		// Header (8 bytes) is intact but data is incomplete.
		// io.ReadFull(reader, data) gets ErrUnexpectedEOF.
		lastEntryDataSize := fullSize - sizeAfterSafe - 8
		truncateSize = sizeAfterSafe + 8 + lastEntryDataSize/2
	}

	if err := os.Truncate(walPath, truncateSize); err != nil {
		t.Fatalf("Truncate to %d failed: %v", truncateSize, err)
	}

	stat, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("Stat after truncation failed: %v", err)
	}
	mode := "mid-data"
	if truncateHeader {
		mode = "mid-header"
	}
	t.Logf("Truncated WAL: %d → %d bytes (-%d, mode=%s)",
		fullSize, stat.Size(), fullSize-stat.Size(), mode)

	// Phase 3: Reopen WAL and replay — must not panic
	wal2, err := NewWAL(dir, "chaos-truncate", logger)
	if err != nil {
		t.Fatalf("Failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}

	t.Logf("Replayed %d shares from truncated WAL (expected safe: %d)", len(replayed), safeShares)

	// ASSERTION 1: All complete entries before truncation point are recovered
	if len(replayed) < safeShares {
		t.Errorf("Lost safe entries: got %d, want >= %d", len(replayed), safeShares)
	}

	// ASSERTION 2: Truncated entry must NOT be returned
	if len(replayed) > safeShares {
		t.Errorf("Truncated entry leaked through: got %d, want <= %d", len(replayed), safeShares)
	}

	// ASSERTION 3: No nil shares (partial entry leaked as nil)
	for i, s := range replayed {
		if s == nil {
			t.Errorf("Replayed share %d is nil (partial entry leak)", i)
		}
	}

	// ASSERTION 4: No duplicate entries
	seen := make(map[string]bool)
	for i, s := range replayed {
		if s == nil {
			continue
		}
		key := s.WorkerName + "|" + s.ExtraNonce1
		if seen[key] {
			t.Errorf("Duplicate share at index %d: %s", i, key)
		}
		seen[key] = true
	}
}
