// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool — Production tests for CoinPool reconciliation and WAL recovery.
//
// These tests exercise the REAL reconcileSubmittingBlocks() and recoverWALBlocks()
// methods with mock dependencies, covering crash recovery scenarios that could
// cause silent block loss if handled incorrectly.
//
// Test naming convention:
//   R-N = reconcileSubmittingBlocks test N
//   W-N = recoverWALBlocks test N
//   P-N = recoverWALAfterPromotion test N
package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Helper: WAL test fixtures
// ═══════════════════════════════════════════════════════════════════════════════

// writeWALEntries creates a WAL JSONL file with the given entries in dir.
// Uses today's date format to match RecoverUnsubmittedBlocks/RecoverSubmittedBlocks glob.
func writeWALEntries(t *testing.T, dir string, entries []BlockWALEntry) {
	t.Helper()
	filename := fmt.Sprintf("block_wal_%s.jsonl", time.Now().Format("2006-01-02"))
	filePath := filepath.Join(dir, filename)
	var buf []byte
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("writeWALEntries: marshal failed: %v", err)
		}
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(filePath, buf, 0644); err != nil {
		t.Fatalf("writeWALEntries: write failed: %v", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// reconcileSubmittingBlocks tests
// ═══════════════════════════════════════════════════════════════════════════════

// Test R-1: No blocks in "submitting" state → no-op.
func TestReconcileSubmittingBlocks_NoBlocks(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	db := newMockDB()
	db.blocksByStatus = map[string][]*database.Block{}
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err != nil {
		t.Errorf("expected nil error for no submitting blocks, got %v", err)
	}
	if db.statusUpdateCount() != 0 {
		t.Errorf("expected 0 status updates, got %d", db.statusUpdateCount())
	}
}

// Test R-2: Single block, daemon has matching hash → "pending".
func TestReconcileSubmittingBlocks_DaemonAccepted(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[testHeight] = testBlockHash

	db := newMockDB()
	db.blocksByStatus = map[string][]*database.Block{
		"submitting": {{Height: testHeight, Hash: testBlockHash}},
	}
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	height, hash, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected 'pending', got %q (height=%d, hash=%s)", status, height, hash)
	}
}

// Test R-3: Single block, daemon has different hash → "orphaned".
func TestReconcileSubmittingBlocks_DaemonOrphaned(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"

	db := newMockDB()
	db.blocksByStatus = map[string][]*database.Block{
		"submitting": {{Height: testHeight, Hash: testBlockHash}},
	}
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected 'orphaned', got %q", status)
	}
}

// Test R-4: Multiple blocks with mixed outcomes.
func TestReconcileSubmittingBlocks_MultipleBlocks_MixedResults(t *testing.T) {
	t.Parallel()
	hash1 := "0000000000000000000000000000000000000000000000000000000000001111"
	hash2 := "0000000000000000000000000000000000000000000000000000000000002222"

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[100001] = hash1                                                              // matches
	nm.blockHashByHeight[100002] = "000000000000000000000000000000000000000000000000000000000000ffff" // differs

	db := newMockDB()
	db.blocksByStatus = map[string][]*database.Block{
		"submitting": {
			{Height: 100001, Hash: hash1},
			{Height: 100002, Hash: hash2},
		},
	}
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if db.statusUpdateCount() != 2 {
		t.Fatalf("expected 2 status updates, got %d", db.statusUpdateCount())
	}
	db.mu.Lock()
	update0 := db.statusUpdates[0]
	update1 := db.statusUpdates[1]
	db.mu.Unlock()

	if update0.status != "pending" {
		t.Errorf("block 1: expected 'pending', got %q", update0.status)
	}
	if update1.status != "orphaned" {
		t.Errorf("block 2: expected 'orphaned', got %q", update1.status)
	}
}

// Test R-5: GetBlocksByStatus returns error → propagated.
func TestReconcileSubmittingBlocks_GetBlocksByStatus_Error(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	db := newMockDB()
	db.getBlocksByStatusErr = errors.New("connection refused")
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err == nil {
		t.Error("expected error from GetBlocksByStatus failure, got nil")
	}
}

// Test R-6: GetBlockHash error → block skipped, error reported.
func TestReconcileSubmittingBlocks_GetBlockHash_Error(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashErr = errors.New("daemon unreachable")

	db := newMockDB()
	db.blocksByStatus = map[string][]*database.Block{
		"submitting": {{Height: testHeight, Hash: testBlockHash}},
	}
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err == nil {
		t.Error("expected error from GetBlockHash failure, got nil")
	}
	// Status should NOT have been updated (error skips the block)
	if db.statusUpdateCount() != 0 {
		t.Errorf("expected 0 status updates (error case), got %d", db.statusUpdateCount())
	}
}

// Test R-7: UpdateBlockStatusForPool error → continues, error reported.
func TestReconcileSubmittingBlocks_UpdateStatus_Error(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[testHeight] = testBlockHash

	db := newMockDB()
	db.blocksByStatus = map[string][]*database.Block{
		"submitting": {{Height: testHeight, Hash: testBlockHash}},
	}
	db.updateErr = errors.New("DB write failed")
	cp := newTestCoinPool(jm, nm, db)

	err := cp.reconcileSubmittingBlocks(context.Background())
	if err == nil {
		t.Error("expected error from UpdateBlockStatusForPool failure, got nil")
	}
	// Update was attempted even though it failed
	if db.statusUpdateCount() != 1 {
		t.Errorf("expected 1 status update attempt, got %d", db.statusUpdateCount())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// recoverWALBlocks tests
// ═══════════════════════════════════════════════════════════════════════════════

// Test W-1: Empty WAL directory → no-op.
func TestRecoverWALBlocks_EmptyDir(t *testing.T) {
	t.Parallel()
	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	walDir := t.TempDir()
	cp.recoverWALBlocks(context.Background(), walDir)

	if db.insertCount() != 0 {
		t.Errorf("expected 0 DB inserts for empty WAL, got %d", db.insertCount())
	}
}

// Test W-2: Unsubmitted block already in chain → DB insert, no resubmit.
func TestRecoverWALBlocks_BlockAlreadyInChain(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "pending",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[testHeight] = testBlockHash // daemon has our block

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Should have inserted block into DB
	if db.insertCount() != 1 {
		t.Errorf("expected 1 DB insert for recovered block, got %d", db.insertCount())
	}
	// Should NOT have attempted resubmission
	if nm.SubmitBlockCallCount() != 0 {
		t.Errorf("expected 0 SubmitBlock calls (block already in chain), got %d", nm.SubmitBlockCallCount())
	}
}

// Test W-3: Unsubmitted block too old → marked stale, no resubmit, no DB insert.
func TestRecoverWALBlocks_BlockTooOld(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			BlockHex:      testBlockHex,
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "submitting",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	// Different hash at height → block not in chain
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"
	// Chain is 200 blocks ahead → block is too old (>100)
	nm.blockchainInfo = &daemon.BlockchainInfo{Blocks: testHeight + 200}

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Should NOT have attempted resubmission (too old)
	if nm.SubmitBlockCallCount() != 0 {
		t.Errorf("expected 0 SubmitBlock calls (block too old), got %d", nm.SubmitBlockCallCount())
	}
	// Should NOT have inserted into DB (stale block gets no record)
	if db.insertCount() != 0 {
		t.Errorf("expected 0 DB inserts for stale block, got %d", db.insertCount())
	}
}

// Test W-4: Recent unsubmitted block with hex → resubmit succeeds → DB insert.
func TestRecoverWALBlocks_RecentBlock_ResubmitSuccess(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			BlockHex:      testBlockHex,
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "submitting",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	// Different hash at height → block not in chain
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"
	// Chain is only 10 blocks ahead (recent enough for resubmit, ≤100)
	nm.blockchainInfo = &daemon.BlockchainInfo{Blocks: testHeight + 10}
	// SubmitBlock succeeds (default: submitBlockErr = nil)

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Should have attempted resubmission
	if nm.SubmitBlockCallCount() != 1 {
		t.Errorf("expected 1 SubmitBlock call, got %d", nm.SubmitBlockCallCount())
	}
	// Should have inserted block into DB after successful resubmit
	if db.insertCount() != 1 {
		t.Errorf("expected 1 DB insert for resubmitted block, got %d", db.insertCount())
	}
}

// Test W-5: Recent unsubmitted block → resubmit fails → no DB insert.
func TestRecoverWALBlocks_RecentBlock_ResubmitFails(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			BlockHex:      testBlockHex,
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "submitting",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"
	nm.blockchainInfo = &daemon.BlockchainInfo{Blocks: testHeight + 10}
	nm.submitBlockErr = errors.New("block-already-known") // resubmit rejected

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Resubmission was attempted
	if nm.SubmitBlockCallCount() != 1 {
		t.Errorf("expected 1 SubmitBlock call, got %d", nm.SubmitBlockCallCount())
	}
	// No DB insert on failed resubmit
	if db.insertCount() != 0 {
		t.Errorf("expected 0 DB inserts for failed resubmit, got %d", db.insertCount())
	}
}

// Test W-6: Recent unsubmitted block without hex → cannot resubmit, no DB insert.
func TestRecoverWALBlocks_RecentBlock_NoHex(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			BlockHex:      "", // no hex data
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "pending",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"
	nm.blockchainInfo = &daemon.BlockchainInfo{Blocks: testHeight + 10}

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Should NOT have attempted resubmission (no hex)
	if nm.SubmitBlockCallCount() != 0 {
		t.Errorf("expected 0 SubmitBlock calls (no hex), got %d", nm.SubmitBlockCallCount())
	}
	// No DB insert (cannot verify block was accepted)
	if db.insertCount() != 0 {
		t.Errorf("expected 0 DB inserts (no hex, cannot resubmit), got %d", db.insertCount())
	}
}

// Test W-7: Phase 2 — WAL has "submitted" entry → DB reconciliation insert.
func TestRecoverWALBlocks_Phase2_SubmittedBlockReconciliation(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "submitted", // already submitted, needs DB reconciliation
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Phase 2 should have called InsertBlockForPool for the submitted block
	if db.insertCount() != 1 {
		t.Errorf("expected 1 DB insert for WAL-DB reconciliation, got %d", db.insertCount())
	}
	// Verify the inserted block has correct fields
	db.mu.Lock()
	inserted := db.insertedBlocks[0]
	db.mu.Unlock()

	if inserted.Height != testHeight {
		t.Errorf("expected inserted block height %d, got %d", testHeight, inserted.Height)
	}
	if inserted.Hash != testBlockHash {
		t.Errorf("expected inserted block hash %s, got %s", testBlockHash, inserted.Hash)
	}
	if inserted.Status != "pending" {
		t.Errorf("expected inserted block status 'pending', got %q", inserted.Status)
	}
	if inserted.Miner != "TTestMiner123" {
		t.Errorf("expected inserted block miner 'TTestMiner123', got %q", inserted.Miner)
	}
}

// Test W-8: Phase 2 — DB insert fails → logged but doesn't panic.
func TestRecoverWALBlocks_Phase2_DBInsertFails(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()
	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			Height:        testHeight,
			BlockHash:     testBlockHash,
			MinerAddress:  "TTestMiner123",
			CoinbaseValue: 1000000000,
			Status:        "submitted",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	db := newMockDB()
	db.insertErr = errors.New("unique constraint violation")
	cp := newTestCoinPool(jm, nm, db)

	// Should not panic
	cp.recoverWALBlocks(context.Background(), walDir)

	// Insert was attempted even though it failed
	if db.insertCount() != 1 {
		t.Errorf("expected 1 insert attempt, got %d", db.insertCount())
	}
}

// Test W-9: Phase 1 block already in chain AND Phase 2 submitted block
// both exercise DB inserts in a single recoverWALBlocks call.
func TestRecoverWALBlocks_CombinedPhase1And2(t *testing.T) {
	t.Parallel()
	walDir := t.TempDir()

	hash1 := "0000000000000000000000000000000000000000000000000000000000001111"
	hash2 := "0000000000000000000000000000000000000000000000000000000000002222"

	writeWALEntries(t, walDir, []BlockWALEntry{
		{
			// Phase 1: unsubmitted block that's already in chain
			Height:        100001,
			BlockHash:     hash1,
			MinerAddress:  "TMiner1",
			CoinbaseValue: 500000000,
			Status:        "pending",
			Timestamp:     time.Now().Add(-1 * time.Minute),
		},
		{
			// Phase 2: submitted block needing DB reconciliation
			Height:        100002,
			BlockHash:     hash2,
			MinerAddress:  "TMiner2",
			CoinbaseValue: 600000000,
			Status:        "submitted",
			Timestamp:     time.Now(),
		},
	})

	jm := newMockJobMgr()
	nm := newMockNodeMgr()
	nm.blockHashByHeight[100001] = hash1 // Phase 1 block is in chain

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.recoverWALBlocks(context.Background(), walDir)

	// Phase 1 inserts for hash1 + Phase 2 inserts for hash2 = 2 total
	if db.insertCount() != 2 {
		t.Errorf("expected 2 DB inserts (Phase 1 + Phase 2), got %d", db.insertCount())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// recoverWALAfterPromotion tests (guard behavior only — component logic tested above)
// ═══════════════════════════════════════════════════════════════════════════════

// Test P-1: Nil blockWAL → early return (no panic, no sleep).
func TestRecoverWALAfterPromotion_NilBlockWAL(t *testing.T) {
	t.Parallel()
	cp := newTestCoinPool(newMockJobMgr(), newMockNodeMgr(), newMockDB())
	// blockWAL is nil by default from newTestCoinPool
	cp.recoverWALAfterPromotion() // should return immediately
}

// Test P-2: Re-entrancy guard — walRecoveryRunning already set → skip.
func TestRecoverWALAfterPromotion_ReentrancyGuard(t *testing.T) {
	t.Parallel()
	cp := newTestCoinPool(newMockJobMgr(), newMockNodeMgr(), newMockDB())
	cp.blockWAL = &BlockWAL{}            // non-nil to pass first check
	cp.walRecoveryRunning.Store(true)     // simulate already running

	// Should return immediately (CompareAndSwap fails) without sleeping
	cp.recoverWALAfterPromotion()
}
