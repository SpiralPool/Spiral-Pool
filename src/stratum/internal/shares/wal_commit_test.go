// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Tests for WAL CommitBatch, CommitBatchVerified, and Write→Commit→Replay
// cycle correctness. These tests verify the commit marker format, sync
// durability, and that uncommitted shares are correctly replayed on restart.
package shares

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test helpers
// ═══════════════════════════════════════════════════════════════════════════════

// testShare creates a minimal protocol.Share for WAL testing.
func testShare(miner string, diff float64) *protocol.Share {
	return &protocol.Share{
		MinerAddress: miner,
		WorkerName:   "rig1",
		Difficulty:   diff,
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CommitBatch marker format
// ═══════════════════════════════════════════════════════════════════════════════

func TestCommitBatch_MarkerFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	wal, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	shares := []*protocol.Share{
		testShare("miner1", 1.0),
		testShare("miner2", 2.0),
	}
	for _, s := range shares {
		if err := wal.Write(s); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	if err := wal.CommitBatch(shares); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}
	if err := wal.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Read raw WAL file and find the commit marker
	walPath := filepath.Join(dir, "wal", "test-pool", "current.wal")
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("failed to read WAL file: %v", err)
	}

	// Search for WALCommitMagic (0x434D4954 = "CMIT")
	found := false
	for i := len(WALMagic); i <= len(data)-16; i++ {
		magic := binary.LittleEndian.Uint32(data[i : i+4])
		if magic == WALCommitMagic {
			// Format: [commit_magic:4][count:4][timestamp:8]
			count := binary.LittleEndian.Uint32(data[i+4 : i+8])
			timestamp := binary.LittleEndian.Uint64(data[i+8 : i+16])

			if count != 2 {
				t.Errorf("commit marker count = %d, want 2", count)
			}
			if timestamp == 0 {
				t.Error("commit marker timestamp should be non-zero")
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("commit marker (WALCommitMagic) not found in WAL file")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CommitBatchVerified sync verification
// ═══════════════════════════════════════════════════════════════════════════════

func TestCommitBatchVerified_ForcesSync(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	wal, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer wal.Close()

	shares := []*protocol.Share{testShare("miner1", 1.0)}
	if err := wal.Write(shares[0]); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	committed, err := wal.CommitBatchVerified(shares)
	if err != nil {
		t.Fatalf("CommitBatchVerified failed: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true")
	}

	// Verify the commit marker is on disk (not just in bufio buffer)
	walPath := filepath.Join(dir, "wal", "test-pool", "current.wal")
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("failed to read WAL file: %v", err)
	}

	found := false
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:i+4]) == WALCommitMagic {
			found = true
			break
		}
	}
	if !found {
		t.Error("commit marker not found on disk after CommitBatchVerified — sync may not have happened")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Write → Commit → Replay cycle
// ═══════════════════════════════════════════════════════════════════════════════

func TestWAL_WriteCommitReplay_NoUncommitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Phase 1: Write 3 shares and commit all
	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}

	shares := []*protocol.Share{
		testShare("miner1", 1.0),
		testShare("miner2", 2.0),
		testShare("miner3", 3.0),
	}
	for _, s := range shares {
		if err := wal1.Write(s); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	committed, err := wal1.CommitBatchVerified(shares)
	if err != nil {
		t.Fatalf("CommitBatchVerified failed: %v", err)
	}
	if !committed {
		t.Fatal("CommitBatchVerified returned false")
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Phase 2: Replay should find 0 uncommitted
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 0 {
		t.Errorf("expected 0 uncommitted after full commit, got %d", len(uncommitted))
	}
}

func TestWAL_WriteNoCommit_ReplayAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Phase 1: Write 3 shares, sync, but do NOT commit
	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}

	shares := []*protocol.Share{
		testShare("miner1", 1.0),
		testShare("miner2", 2.0),
		testShare("miner3", 3.0),
	}
	for _, s := range shares {
		if err := wal1.Write(s); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if err := wal1.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Phase 2: Replay should find all 3 uncommitted
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 3 {
		t.Errorf("expected 3 uncommitted shares, got %d", len(uncommitted))
	}

	// Verify share data survived the round-trip
	if len(uncommitted) >= 1 && uncommitted[0].MinerAddress != "miner1" {
		t.Errorf("uncommitted[0].MinerAddress = %q, want miner1", uncommitted[0].MinerAddress)
	}
	if len(uncommitted) >= 3 && uncommitted[2].Difficulty != 3.0 {
		t.Errorf("uncommitted[2].Difficulty = %f, want 3.0", uncommitted[2].Difficulty)
	}
}

func TestWAL_PartialCommit_ReplayUncommitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Phase 1: Write 5 shares, commit first 3
	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}

	allShares := make([]*protocol.Share, 5)
	for i := 0; i < 5; i++ {
		allShares[i] = testShare(fmt.Sprintf("miner%d", i), float64(i+1))
		if err := wal1.Write(allShares[i]); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	committed, err := wal1.CommitBatchVerified(allShares[:3])
	if err != nil {
		t.Fatalf("CommitBatchVerified failed: %v", err)
	}
	if !committed {
		t.Fatal("CommitBatchVerified returned false")
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Phase 2: Replay should find exactly 2 uncommitted (5 written - 3 committed)
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 2 {
		t.Errorf("expected 2 uncommitted shares (wrote 5, committed 3), got %d", len(uncommitted))
	}
}

func TestWAL_MultipleCommits_CumulativeTracking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}

	// Write 6 shares
	for i := 0; i < 6; i++ {
		if err := wal1.Write(testShare(fmt.Sprintf("miner%d", i), float64(i))); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	// Commit batch 1 (2 shares)
	if err := wal1.CommitBatch(make([]*protocol.Share, 2)); err != nil {
		t.Fatalf("CommitBatch 1 failed: %v", err)
	}
	// Commit batch 2 (2 shares)
	if err := wal1.CommitBatch(make([]*protocol.Share, 2)); err != nil {
		t.Fatalf("CommitBatch 2 failed: %v", err)
	}
	// Shares 4-5 remain uncommitted

	if err := wal1.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Replay: 6 written - 4 committed = 2 uncommitted
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 2 {
		t.Errorf("expected 2 uncommitted (6 written, 4 committed in 2 batches), got %d", len(uncommitted))
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Edge cases
// ═══════════════════════════════════════════════════════════════════════════════

func TestCommitBatch_ClosedWAL_NoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	wal, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	wal.Close()

	// CommitBatch on a closed WAL (w.file == nil) should return nil
	err = wal.CommitBatch([]*protocol.Share{testShare("miner1", 1.0)})
	if err != nil {
		t.Errorf("CommitBatch on closed WAL should be no-op, got: %v", err)
	}
}

func TestWAL_EmptyReplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create and immediately close (empty WAL with only magic header)
	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Replay should find nothing
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 0 {
		t.Errorf("expected 0 uncommitted from empty WAL, got %d", len(uncommitted))
	}
}

func TestWAL_CommitMoreThanWritten_ReplayZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}

	// Write 2 shares but commit 5 (count-based commit marker)
	for i := 0; i < 2; i++ {
		if err := wal1.Write(testShare(fmt.Sprintf("miner%d", i), 1.0)); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	// CommitBatch uses len(shares) for the count — pass 5 shares
	if err := wal1.CommitBatch(make([]*protocol.Share, 5)); err != nil {
		t.Fatalf("CommitBatch failed: %v", err)
	}
	if err := wal1.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Replay: 2 entries - 5 committed = -3 → clamped to 0
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 0 {
		t.Errorf("expected 0 uncommitted when committed > written, got %d", len(uncommitted))
	}
}

func TestWAL_ShareDataRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	wal1, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 1: %v", err)
	}

	// Write share with specific data
	original := &protocol.Share{
		MinerAddress: "DJv2x5L5yjjqGZXQBp5SsSYHaqP2grRSPd",
		WorkerName:   "antminer-42",
		Difficulty:   256.0,
	}
	if err := wal1.Write(original); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := wal1.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Replay and verify all fields survived
	wal2, err := NewWAL(dir, "test-pool", zap.NewNop())
	if err != nil {
		t.Fatalf("NewWAL 2: %v", err)
	}
	defer wal2.Close()

	uncommitted, err := wal2.Replay()
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if len(uncommitted) != 1 {
		t.Fatalf("expected 1 uncommitted, got %d", len(uncommitted))
	}

	got := uncommitted[0]
	if got.MinerAddress != original.MinerAddress {
		t.Errorf("MinerAddress = %q, want %q", got.MinerAddress, original.MinerAddress)
	}
	if got.WorkerName != original.WorkerName {
		t.Errorf("WorkerName = %q, want %q", got.WorkerName, original.WorkerName)
	}
	if got.Difficulty != original.Difficulty {
		t.Errorf("Difficulty = %f, want %f", got.Difficulty, original.Difficulty)
	}
}
