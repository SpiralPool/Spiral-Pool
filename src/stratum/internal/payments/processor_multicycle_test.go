// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package payments — Multi-cycle processor tests.
//
// These tests exercise processCycle() across multiple invocations to verify:
//   - OrphanMismatchCount accumulates correctly through the full processCycle path
//   - Block transitions pending → orphaned after threshold consecutive mismatches
//   - Block transitions pending → confirmed after stability window through processCycle
//   - consecutiveFailedCycles tracks daemon/DB failures across cycles
//   - Mismatch recovery (hash match after partial mismatches) through processCycle
//   - Multiple blocks in different states are handled independently per cycle
package payments

import (
	"context"
	"fmt"
	"testing"

	"github.com/spiralpool/stratum/internal/database"
)

// =============================================================================
// Multi-cycle orphan accumulation through processCycle()
// =============================================================================

// TestProcessCycle_OrphanAccumulationThroughFullPath verifies that running
// processCycle() multiple times with persistent hash mismatches causes
// OrphanMismatchCount to accumulate and eventually orphan the block.
// This is the P0 gap: the full processCycle path (not just updateBlockConfirmations).
func TestProcessCycle_OrphanAccumulationThroughFullPath(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(8000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
	tip := fmt.Sprintf("%064d", chainHeight)
	wrongHash := "ffff999999999999eeee888888888888" // Different from blockHash

	store := newMockBlockStore()
	store.addPendingBlock(&database.Block{
		Height: blockHeight,
		Hash:   blockHash,
		Status: StatusPending,
		Miner:  "miner1",
	})

	rpc := newMockDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, wrongHash)

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run processCycle() OrphanMismatchThreshold times.
	// Each cycle: processCycle → updateBlockConfirmations → hash mismatch → increment
	for i := 1; i <= OrphanMismatchThreshold; i++ {
		proc.processCycle(context.Background())

		if i < OrphanMismatchThreshold {
			// Block should still be pending — not enough mismatches yet.
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("Block orphaned after only %d cycles (threshold=%d)", i, OrphanMismatchThreshold)
			}

			// Verify mismatch count incremented via processCycle path.
			orphanUpdates := store.getOrphanUpdates()
			if len(orphanUpdates) == 0 {
				t.Fatalf("Cycle %d: no orphan count updates recorded", i)
			}
			lastUpdate := orphanUpdates[len(orphanUpdates)-1]
			if lastUpdate.MismatchCount != i {
				t.Errorf("Cycle %d: expected mismatch count %d, got %d", i, i, lastUpdate.MismatchCount)
			}
		}
	}

	// After OrphanMismatchThreshold cycles, block should be orphaned.
	if !store.hasStatusUpdate(blockHeight, StatusOrphaned) {
		t.Error("Block should be orphaned after OrphanMismatchThreshold consecutive processCycle() calls with hash mismatch")
	}

	// Verify cycleCount was incremented correctly.
	if proc.cycleCount != OrphanMismatchThreshold {
		t.Errorf("cycleCount = %d, expected %d", proc.cycleCount, OrphanMismatchThreshold)
	}
}

// TestProcessCycle_ConfirmationThroughFullPath verifies that a block reaches
// confirmed status after StabilityWindowChecks consecutive stable processCycle() calls.
func TestProcessCycle_ConfirmationThroughFullPath(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(9000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newMockBlockStore()
	store.addPendingBlock(&database.Block{
		Height: blockHeight,
		Hash:   blockHash,
		Status: StatusPending,
		Miner:  "miner1",
	})

	rpc := newMockDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, blockHash) // Hash matches

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run processCycle() StabilityWindowChecks times.
	for i := 1; i <= StabilityWindowChecks; i++ {
		proc.processCycle(context.Background())
	}

	// Block should be confirmed after stability window.
	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus to be called for confirmation")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("Expected status %q, got %q", StatusConfirmed, call.Status)
	}
}

// TestProcessCycle_MismatchRecoveryThenConfirmation verifies that partial
// mismatches followed by hash recovery still leads to confirmation,
// all through the processCycle() path.
func TestProcessCycle_MismatchRecoveryThenConfirmation(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(10000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)
	wrongHash := "ffff999999999999eeee888888888888"

	store := newMockBlockStore()
	store.addPendingBlock(&database.Block{
		Height: blockHeight,
		Hash:   blockHash,
		Status: StatusPending,
		Miner:  "miner1",
	})

	rpc := newMockDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, wrongHash) // Start with mismatch

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run OrphanMismatchThreshold-1 cycles with mismatch (just under threshold).
	for i := 0; i < OrphanMismatchThreshold-1; i++ {
		proc.processCycle(context.Background())
	}

	// Block should NOT be orphaned yet.
	if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
		t.Fatal("Block should NOT be orphaned before reaching threshold")
	}

	// Fix the hash — simulate chain reorganization settling.
	rpc.setBlockHash(blockHeight, blockHash)

	// Run StabilityWindowChecks cycles with matching hash → should confirm.
	for i := 0; i < StabilityWindowChecks; i++ {
		proc.processCycle(context.Background())
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus to be called after recovery")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("Expected confirmed after mismatch recovery, got %q", call.Status)
	}
}

// TestProcessCycle_ConsecutiveFailedCyclesTracking verifies that when
// updateBlockConfirmations fails, consecutiveFailedCycles increments,
// and resets on success.
func TestProcessCycle_ConsecutiveFailedCyclesTracking(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	// Need a pending block so updateBlockConfirmations actually calls GetBlockchainInfo.
	store.addPendingBlock(&database.Block{
		Height: 14000,
		Hash:   "aaaa111111111111bbbb222222222222",
		Status: StatusPending,
		Miner:  "miner1",
	})

	rpc := newMockDaemonRPC()
	// Inject error into GetBlockchainInfo to make updateBlockConfirmations fail.
	rpc.errGetBlockchainInfo = fmt.Errorf("daemon connection refused")

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run 5 failing cycles.
	for i := 0; i < 5; i++ {
		proc.processCycle(context.Background())
	}

	if proc.consecutiveFailedCycles != 5 {
		t.Errorf("Expected 5 consecutive failed cycles, got %d", proc.consecutiveFailedCycles)
	}

	// Clear the error — next cycle should succeed and reset counter.
	rpc.mu.Lock()
	rpc.errGetBlockchainInfo = nil
	rpc.mu.Unlock()

	// Also need to set up valid chain state for the success cycle.
	rpc.setChainTip(14100, fmt.Sprintf("%064d", 14100))
	rpc.setBlockHash(14000, "aaaa111111111111bbbb222222222222")

	proc.processCycle(context.Background())

	if proc.consecutiveFailedCycles != 0 {
		t.Errorf("Expected consecutive failures to reset to 0 after success, got %d", proc.consecutiveFailedCycles)
	}
}

// TestProcessCycle_TwoBlocksDifferentFates verifies that two pending blocks
// in the same cycle can have different outcomes: one confirmed, one orphaned.
func TestProcessCycle_TwoBlocksDifferentFates(t *testing.T) {
	t.Parallel()

	height1 := uint64(11000)
	hash1 := "aaaa111111111111bbbb222222222222"
	height2 := uint64(11001)
	hash2 := "cccc333333333333dddd444444444444"
	chainHeight := height2 + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newMockBlockStore()
	store.addPendingBlock(&database.Block{
		Height: height1,
		Hash:   hash1,
		Status: StatusPending,
		Miner:  "miner1",
	})
	store.addPendingBlock(&database.Block{
		Height: height2,
		Hash:   hash2,
		Status: StatusPending,
		Miner:  "miner2",
	})

	rpc := newMockDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(height1, hash1)                              // Matches → will confirm
	rpc.setBlockHash(height2, "ffff888888888888aaaa777777777777") // Mismatches → will orphan

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Need max(StabilityWindowChecks, OrphanMismatchThreshold) cycles.
	maxCycles := StabilityWindowChecks
	if OrphanMismatchThreshold > maxCycles {
		maxCycles = OrphanMismatchThreshold
	}

	for i := 0; i < maxCycles; i++ {
		proc.processCycle(context.Background())
	}

	// Block 1 should be confirmed (hash matched every cycle).
	call1, ok1 := store.lastStatusFor(height1)
	if !ok1 {
		t.Fatal("Expected UpdateBlockStatus call for block 1")
	}
	if call1.Status != StatusConfirmed {
		t.Errorf("Block 1 expected confirmed, got %q", call1.Status)
	}

	// Block 2 should be orphaned (hash mismatched every cycle).
	if !store.hasStatusUpdate(height2, StatusOrphaned) {
		t.Error("Block 2 expected orphaned after consistent hash mismatches")
	}
}

// TestProcessCycle_DeepReorgOrphansConfirmedBlock verifies that the deep reorg
// check (every DeepReorgCheckInterval cycles) can orphan a previously confirmed block.
func TestProcessCycle_DeepReorgOrphansConfirmedBlock(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(12000)
	blockHash := "aaaa111111111111bbbb222222222222"
	reorgedHash := "ffff999999999999eeee888888888888"
	chainHeight := blockHeight + 500
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newMockBlockStore()
	store.addConfirmedBlock(&database.Block{
		Height: blockHeight,
		Hash:   blockHash,
		Status: StatusConfirmed,
		Miner:  "miner1",
	})

	rpc := newMockDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, reorgedHash) // Deep reorg: chain hash changed

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	// Set cycleCount so next cycle triggers deep reorg check.
	proc.cycleCount = DeepReorgCheckInterval - 1

	proc.processCycle(context.Background())

	// Block should be orphaned via deep reorg detection.
	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus call for deep reorg orphaning")
	}
	if call.Status != StatusOrphaned {
		t.Errorf("Expected orphaned after deep reorg, got %q", call.Status)
	}
}

// TestProcessCycle_CycleCountMonotonicallyIncreases verifies cycleCount
// always increments, even across failure cycles.
func TestProcessCycle_CycleCountMonotonicallyIncreases(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()
	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run 20 cycles — mix of success and failure.
	rpc.errGetBlockchainInfo = fmt.Errorf("fail")
	for i := 0; i < 5; i++ {
		proc.processCycle(context.Background())
	}
	rpc.mu.Lock()
	rpc.errGetBlockchainInfo = nil
	rpc.mu.Unlock()
	for i := 0; i < 15; i++ {
		proc.processCycle(context.Background())
	}

	if proc.cycleCount != 20 {
		t.Errorf("Expected cycleCount=20 after 20 processCycle() calls, got %d", proc.cycleCount)
	}
}

// TestProcessCycle_HABackupNodeSkipsCycle verifies that when haEnabled=true
// and isMaster=false, processCycle is a no-op.
func TestProcessCycle_HABackupNodeSkipsCycle(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	store.addPendingBlock(&database.Block{
		Height: 13000,
		Hash:   "aaaa111111111111bbbb222222222222",
		Status: StatusPending,
		Miner:  "miner1",
	})

	rpc := newMockDaemonRPC()
	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.haEnabled.Store(true)
	proc.isMaster.Store(false) // Backup node

	proc.processCycle(context.Background())

	// cycleCount should NOT have incremented (cycle was skipped).
	if proc.cycleCount != 0 {
		t.Errorf("HA backup node should skip cycle, but cycleCount=%d", proc.cycleCount)
	}

	// No DB calls should have been made.
	rpc.mu.Lock()
	calls := rpc.getBlockchainInfoCalls
	rpc.mu.Unlock()
	if calls != 0 {
		t.Errorf("HA backup node should make no RPC calls, got %d", calls)
	}
}
