// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Round-trip tests for WAL (Write-Ahead Log) clean operations.
//
// The chaos tests in chaos_wal_test.go and chaos_wal_truncation_test.go cover
// failure modes (disk exhaustion, crash simulation, truncation). This file
// covers normal write→commit→replay behavior and boundary conditions.
package shares

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// TestWAL_NewWAL_CreatesDirectory verifies that NewWAL creates the expected
// directory structure {dir}/wal/{poolID}/ and that current.wal exists inside it.
func TestWAL_NewWAL_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "test-pool", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}
	defer wal.Close()

	// Verify the WAL directory was created
	walDir := filepath.Join(dir, "wal", "test-pool")
	info, err := os.Stat(walDir)
	if err != nil {
		t.Fatalf("WAL directory does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("Expected %s to be a directory", walDir)
	}

	// Verify current.wal file exists
	walFile := filepath.Join(walDir, "current.wal")
	finfo, err := os.Stat(walFile)
	if err != nil {
		t.Fatalf("current.wal does not exist: %v", err)
	}
	if finfo.IsDir() {
		t.Fatalf("Expected current.wal to be a file, not a directory")
	}
}

// TestWAL_NewWAL_MagicHeader verifies that a freshly created WAL file starts
// with the 8-byte magic header "SPWAL001".
func TestWAL_NewWAL_MagicHeader(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "magic-test", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Read raw bytes from the WAL file
	walFile := filepath.Join(dir, "wal", "magic-test", "current.wal")
	data, err := os.ReadFile(walFile)
	if err != nil {
		t.Fatalf("Failed to read WAL file: %v", err)
	}

	if len(data) < len(WALMagic) {
		t.Fatalf("WAL file too small: %d bytes, need at least %d for magic header",
			len(data), len(WALMagic))
	}

	got := string(data[:len(WALMagic)])
	if got != WALMagic {
		t.Errorf("WAL magic header mismatch: got %q, want %q", got, WALMagic)
	}
}

// TestWAL_WriteAndReplay_Uncommitted verifies that shares written but never
// committed are all returned by Replay as uncommitted.
func TestWAL_WriteAndReplay_Uncommitted(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "uncommitted-test", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	const shareCount = 5
	for i := 0; i < shareCount; i++ {
		if err := wal.Write(chaosTestShare(i)); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen and replay
	wal2, err := NewWAL(dir, "uncommitted-test", logger)
	if err != nil {
		t.Fatalf("Reopen NewWAL failed: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if len(replayed) != shareCount {
		t.Errorf("Replay returned %d shares, want %d (all uncommitted)", len(replayed), shareCount)
	}
}

// TestWAL_WriteCommitReplay_AllCommitted verifies that when all written shares
// are committed, Replay returns nil (no uncommitted shares to recover).
func TestWAL_WriteCommitReplay_AllCommitted(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "all-committed", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	const shareCount = 5
	shares := make([]*protocol.Share, shareCount)
	for i := 0; i < shareCount; i++ {
		shares[i] = chaosTestShare(i)
		if err := wal.Write(shares[i]); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	if err := wal.CommitBatch(shares); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen and replay
	wal2, err := NewWAL(dir, "all-committed", logger)
	if err != nil {
		t.Fatalf("Reopen NewWAL failed: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if len(replayed) != 0 {
		t.Errorf("Replay returned %d shares, want 0 (all committed)", len(replayed))
	}
}

// TestWAL_WriteCommitReplay_PartialCommit verifies that when only some shares
// are committed, Replay returns exactly the uncommitted remainder.
func TestWAL_WriteCommitReplay_PartialCommit(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "partial-commit", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	const totalShares = 10
	const committedShares = 7
	const expectedUncommitted = totalShares - committedShares

	shares := make([]*protocol.Share, totalShares)
	for i := 0; i < totalShares; i++ {
		shares[i] = chaosTestShare(i)
		if err := wal.Write(shares[i]); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	// Commit only the first 7
	if err := wal.CommitBatch(shares[:committedShares]); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen and replay
	wal2, err := NewWAL(dir, "partial-commit", logger)
	if err != nil {
		t.Fatalf("Reopen NewWAL failed: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if len(replayed) != expectedUncommitted {
		t.Errorf("Replay returned %d shares, want %d uncommitted", len(replayed), expectedUncommitted)
	}
}

// TestWAL_CommitBatchVerified_SyncsToFile verifies that CommitBatchVerified
// durably persists the commit marker. After reopen, Replay should return empty
// because the verified commit was synced to disk.
func TestWAL_CommitBatchVerified_SyncsToFile(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "verified-commit", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	const shareCount = 5
	shares := make([]*protocol.Share, shareCount)
	for i := 0; i < shareCount; i++ {
		shares[i] = chaosTestShare(i)
		if err := wal.Write(shares[i]); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	committed, err := wal.CommitBatchVerified(shares)
	if err != nil {
		t.Fatalf("CommitBatchVerified failed: %v", err)
	}
	if !committed {
		t.Fatalf("CommitBatchVerified returned committed=false, want true")
	}

	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen and replay - verified commit must be durable
	wal2, err := NewWAL(dir, "verified-commit", logger)
	if err != nil {
		t.Fatalf("Reopen NewWAL failed: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if len(replayed) != 0 {
		t.Errorf("Replay returned %d shares after verified commit, want 0", len(replayed))
	}
}

// TestWAL_Stats_TracksWrittenAndCommitted verifies that Stats() accurately
// tracks written shares, committed shares, and file size.
func TestWAL_Stats_TracksWrittenAndCommitted(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "stats-test", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}
	defer wal.Close()

	const totalShares = 10
	const commitCount = 5

	shares := make([]*protocol.Share, totalShares)
	for i := 0; i < totalShares; i++ {
		shares[i] = chaosTestShare(i)
		if err := wal.Write(shares[i]); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	stats := wal.Stats()
	if stats.Written != uint64(totalShares) {
		t.Errorf("Stats().Written = %d, want %d", stats.Written, totalShares)
	}

	if err := wal.CommitBatch(shares[:commitCount]); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}

	stats = wal.Stats()
	if stats.Committed != uint64(commitCount) {
		t.Errorf("Stats().Committed = %d, want %d", stats.Committed, commitCount)
	}
	if stats.FileSize <= 0 {
		t.Errorf("Stats().FileSize = %d, want > 0", stats.FileSize)
	}
}

// TestWAL_EmptyReplay_NoShares verifies that Replay on a fresh WAL with no
// written shares returns nil, nil.
func TestWAL_EmptyReplay_NoShares(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "empty-replay", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}
	defer wal.Close()

	replayed, err := wal.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if replayed != nil {
		t.Errorf("Replay returned %d shares on empty WAL, want nil", len(replayed))
	}
}

// TestWAL_WALConstants verifies that WAL protocol constants have their expected
// values. These are part of the on-disk format and must never change without a
// corresponding WALMagic version bump.
func TestWAL_WALConstants(t *testing.T) {
	if WALMagic != "SPWAL001" {
		t.Errorf("WALMagic = %q, want %q", WALMagic, "SPWAL001")
	}
	if WALEntryMagic != 0x53504C45 {
		t.Errorf("WALEntryMagic = 0x%08X, want 0x53504C45", WALEntryMagic)
	}
	if WALCommitMagic != 0x434D4954 {
		t.Errorf("WALCommitMagic = 0x%08X, want 0x434D4954", WALCommitMagic)
	}
	if MaxWALSize != 100*1024*1024 {
		t.Errorf("MaxWALSize = %d, want %d", MaxWALSize, 100*1024*1024)
	}
}

// TestWAL_ShareDataIntegrity verifies that share field values survive a full
// write→close→reopen→replay cycle without data loss or corruption.
func TestWAL_ShareDataIntegrity(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "integrity-test", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	// Use a share with distinctive field values that we can verify after replay
	original := chaosTestShare(42)

	if err := wal.Write(original); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen and replay
	wal2, err := NewWAL(dir, "integrity-test", logger)
	if err != nil {
		t.Fatalf("Reopen NewWAL failed: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if len(replayed) != 1 {
		t.Fatalf("Replay returned %d shares, want 1", len(replayed))
	}

	got := replayed[0]

	if got.MinerAddress != original.MinerAddress {
		t.Errorf("MinerAddress = %q, want %q", got.MinerAddress, original.MinerAddress)
	}
	if got.WorkerName != original.WorkerName {
		t.Errorf("WorkerName = %q, want %q", got.WorkerName, original.WorkerName)
	}
	if got.JobID != original.JobID {
		t.Errorf("JobID = %q, want %q", got.JobID, original.JobID)
	}
	if got.ExtraNonce1 != original.ExtraNonce1 {
		t.Errorf("ExtraNonce1 = %q, want %q", got.ExtraNonce1, original.ExtraNonce1)
	}
	if got.ExtraNonce2 != original.ExtraNonce2 {
		t.Errorf("ExtraNonce2 = %q, want %q", got.ExtraNonce2, original.ExtraNonce2)
	}
	if got.NTime != original.NTime {
		t.Errorf("NTime = %q, want %q", got.NTime, original.NTime)
	}
	if got.Nonce != original.Nonce {
		t.Errorf("Nonce = %q, want %q", got.Nonce, original.Nonce)
	}
	if got.Difficulty != original.Difficulty {
		t.Errorf("Difficulty = %f, want %f", got.Difficulty, original.Difficulty)
	}
	if got.MinDifficulty != original.MinDifficulty {
		t.Errorf("MinDifficulty = %f, want %f", got.MinDifficulty, original.MinDifficulty)
	}
}

// TestWAL_MultipleCommitBatches verifies correct replay behavior when multiple
// commit batches are written in sequence. Only shares after the last commit
// should be returned as uncommitted.
func TestWAL_MultipleCommitBatches(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "multi-commit", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	// Batch 1: write 3 shares, commit 3
	batch1 := make([]*protocol.Share, 3)
	for i := 0; i < 3; i++ {
		batch1[i] = chaosTestShare(i)
		if err := wal.Write(batch1[i]); err != nil {
			t.Fatalf("Batch1 write %d failed: %v", i, err)
		}
	}
	if err := wal.CommitBatch(batch1); err != nil {
		t.Fatalf("CommitBatch 1 failed: %v", err)
	}

	// Batch 2: write 4 more shares, commit 4
	batch2 := make([]*protocol.Share, 4)
	for i := 0; i < 4; i++ {
		batch2[i] = chaosTestShare(100 + i)
		if err := wal.Write(batch2[i]); err != nil {
			t.Fatalf("Batch2 write %d failed: %v", i, err)
		}
	}
	if err := wal.CommitBatch(batch2); err != nil {
		t.Fatalf("CommitBatch 2 failed: %v", err)
	}

	// Batch 3: write 2 more shares, do NOT commit
	for i := 0; i < 2; i++ {
		if err := wal.Write(chaosTestShare(200 + i)); err != nil {
			t.Fatalf("Batch3 write %d failed: %v", i, err)
		}
	}

	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen and replay
	wal2, err := NewWAL(dir, "multi-commit", logger)
	if err != nil {
		t.Fatalf("Reopen NewWAL failed: %v", err)
	}
	defer wal2.Close()

	replayed, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	// Total written: 3 + 4 + 2 = 9
	// Total committed: 3 + 4 = 7
	// Uncommitted: 9 - 7 = 2
	const expectedUncommitted = 2
	if len(replayed) != expectedUncommitted {
		t.Errorf("Replay returned %d shares, want %d uncommitted", len(replayed), expectedUncommitted)
	}
}

// TestWAL_ConcurrentWrites verifies that concurrent goroutines can safely write
// shares to the WAL without data races or panics. The WAL's internal mutex must
// serialize all writes correctly.
func TestWAL_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewWAL(dir, "concurrent-test", logger)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}

	const goroutines = 10
	const sharesPerGoroutine = 10
	const totalShares = goroutines * sharesPerGoroutine

	var wg sync.WaitGroup
	errs := make(chan error, totalShares)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(goroutineIdx int) {
			defer wg.Done()
			for i := 0; i < sharesPerGoroutine; i++ {
				shareIdx := goroutineIdx*sharesPerGoroutine + i
				if werr := wal.Write(chaosTestShare(shareIdx)); werr != nil {
					errs <- fmt.Errorf("goroutine %d, share %d: %w", goroutineIdx, i, werr)
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for e := range errs {
		t.Errorf("Concurrent write error: %v", e)
	}

	stats := wal.Stats()
	if stats.Written != uint64(totalShares) {
		t.Errorf("Stats().Written = %d, want %d", stats.Written, totalShares)
	}

	if err := wal.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
