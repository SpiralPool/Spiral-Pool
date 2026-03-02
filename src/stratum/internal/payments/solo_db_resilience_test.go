// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Tests that the payment processor is resilient to database write failures.
// No valid block should be silently lost, falsely orphaned, or falsely confirmed
// when the database layer returns errors on write operations.
//
// These tests use the error-injecting mocks from solo_mocks_test.go
// (errInjectBlockStore, errInjectDaemonRPC) and exercise Processor methods
// directly since we are in the same package (white-box testing).
package payments

import (
	"context"
	"fmt"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test 1: UpdateBlockStatus fails — block must NOT be lost or falsely orphaned
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_UpdateStatusFails_BlockNotLost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Add a pending block at height 800 (below maturity from chain height 1000).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// Inject: all UpdateBlockStatus calls will fail.
	store.errUpdateStatus = fmt.Errorf("injected: disk full")

	// Run the confirmation cycle — should NOT panic.
	err := proc.updateBlockConfirmations(ctx)
	if err != nil {
		t.Fatalf("updateBlockConfirmations returned error: %v (expected nil — per-block errors are logged, not propagated)", err)
	}

	// Block must remain pending — the status write failed so no mutation occurred.
	store.mu.Lock()
	status := store.pendingBlocks[0].Status
	store.mu.Unlock()
	if status != StatusPending {
		t.Errorf("block status = %q; want %q (failed status write must not mutate)", status, StatusPending)
	}

	// No status updates should have been recorded (they all failed).
	if n := store.statusUpdateCount(); n != 0 {
		t.Errorf("statusUpdateCount = %d; want 0 (all writes failed)", n)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 2: UpdateOrphanCount fails — mismatch still detected, no silent corruption
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_UpdateOrphanCountFails_NoSilentCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Pending block at height 800 with a hash that does NOT match the chain.
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	// Set a DIFFERENT hash on the chain so there is a mismatch.
	rpc.setBlockHash(800, makeBlockHash("MISMATCH", 800))

	// Inject: orphan count writes fail.
	store.errUpdateOrphan = fmt.Errorf("injected: orphan count write error")

	// Run OrphanMismatchThreshold cycles. Each cycle detects mismatch and
	// increments the in-memory OrphanMismatchCount. The DB write for orphan
	// count fails, but the in-memory counter still advances because the
	// processor mutates block.OrphanMismatchCount before calling the DB.
	for i := 0; i < OrphanMismatchThreshold; i++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// After threshold cycles, the processor should have called UpdateBlockStatus
	// to mark the block as orphaned (errUpdateStatus is NOT set, so it succeeds).
	if !store.hasStatusUpdateFor(800, StatusOrphaned) {
		t.Error("block at height 800 was not marked orphaned after reaching mismatch threshold")
	}

	// Orphan count DB writes all failed, so none should be recorded.
	if n := store.orphanUpdateCount(); n != 0 {
		t.Errorf("orphanUpdateCount = %d; want 0 (all orphan writes failed)", n)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 3: UpdateStabilityCount fails — block still confirms (in-memory tracking)
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_UpdateStabilityFails_NoFalseConfirmation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block at height 800, chain at 1000 → 200 confirmations ≥ maturity (100).
	// Hash matches so stability window applies.
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// Inject: stability count DB writes fail.
	store.errUpdateStability = fmt.Errorf("injected: stability write error")

	// Run StabilityWindowChecks cycles. The in-memory StabilityCheckCount
	// increments each cycle (processor mutates block struct directly).
	// Even though the DB write fails, the in-memory state progresses.
	for i := 0; i < StabilityWindowChecks; i++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// The block should have been confirmed despite stability DB write failures.
	// UpdateBlockStatus(confirmed) is called and succeeds (errUpdateStatus is NOT set).
	if !store.hasStatusUpdateFor(800, StatusConfirmed) {
		t.Error("block at height 800 was not confirmed after stability window — DB stability write failure should not prevent confirmation")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 4: GetPendingBlocks fails — error IS propagated
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_GetPendingFails_ErrorPropagated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	store.errGetPending = fmt.Errorf("injected: connection refused")

	err := proc.updateBlockConfirmations(ctx)
	if err == nil {
		t.Fatal("updateBlockConfirmations returned nil; expected error when GetPendingBlocks fails")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 5: GetConfirmedBlocks fails — deep reorg check aborts with error
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_GetConfirmedFails_DeepReorgAborts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	store.errGetConfirmed = fmt.Errorf("injected: table locked")

	err := proc.verifyConfirmedBlocks(ctx)
	if err == nil {
		t.Fatal("verifyConfirmedBlocks returned nil; expected error when GetConfirmedBlocks fails")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 6: Countdown GetPending — recovers after N failures
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_CountdownGetPending_RecoverAfterN(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block at maturity: height 800, chain 1000 → 200 confirmations ≥ 100.
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// First 3 calls to GetPendingBlocks will fail, then succeed.
	store.failGetPendingN = 3

	totalCycles := 3 + StabilityWindowChecks
	for i := 0; i < totalCycles; i++ {
		err := proc.updateBlockConfirmations(ctx)
		if i < 3 {
			// First 3 cycles should fail (GetPendingBlocks error).
			if err == nil {
				t.Errorf("cycle %d: expected error from countdown failure, got nil", i)
			}
		} else {
			// Subsequent cycles should succeed.
			if err != nil {
				t.Errorf("cycle %d: unexpected error: %v", i, err)
			}
		}
	}

	// After recovery + StabilityWindowChecks successful cycles, block should confirm.
	if !store.hasStatusUpdateFor(800, StatusConfirmed) {
		t.Error("block at height 800 was not confirmed after countdown recovery + stability window")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 7: Countdown GetConfirmed — eventual deep reorg detection
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_CountdownGetConfirmed_EventualDeepReorg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Add a confirmed block whose hash does NOT match the chain (deep reorg).
	block := makeConfirmedBlock("BTC", 800)
	store.addConfirmedBlock(block)
	// Chain has a different hash at this height.
	rpc.setBlockHash(800, makeBlockHash("REORGED", 800))

	// First 2 GetConfirmedBlocks calls fail, 3rd succeeds.
	store.failGetConfirmedN = 2

	// Run processCycle 3 times, each time ensuring the deep reorg check triggers.
	// processCycle increments cycleCount and triggers deep reorg when cycleCount % 10 == 0.
	for i := 0; i < 3; i++ {
		// Set cycleCount so that after increment it equals DeepReorgCheckInterval.
		proc.cycleCount = DeepReorgCheckInterval - 1
		proc.processCycle(ctx)
	}

	// First 2 deep reorg checks failed (GetConfirmed countdown).
	// 3rd succeeded and detected the hash mismatch → orphaned.
	if !store.hasStatusUpdateFor(800, StatusOrphaned) {
		t.Error("confirmed block at height 800 was not orphaned by deep reorg after countdown recovery")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 8: All coins — UpdateStatus fails never produces false orphan
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_AllCoins_UpdateStatusFails_NeverFalseOrphan(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin // capture range variable
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			store := newErrInjectBlockStore()
			rpc := newErrInjectDaemonRPC()
			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			block := makePendingBlock(coin.Symbol, 800)
			store.addPendingBlock(block)
			rpc.setBlockHash(800, block.Hash)

			// All status writes fail.
			store.errUpdateStatus = fmt.Errorf("injected: status write error for %s", coin.Symbol)

			// Run multiple cycles.
			for cycle := 0; cycle < 5; cycle++ {
				err := proc.updateBlockConfirmations(ctx)
				if err != nil {
					t.Fatalf("cycle %d: unexpected error: %v", cycle, err)
				}
			}

			// Block must NEVER be orphaned. Since errUpdateStatus is set,
			// no status update can succeed, so the block stays pending.
			if store.hasStatusUpdateFor(800, StatusOrphaned) {
				t.Errorf("coin %s: block was falsely orphaned despite status write failures", coin.Symbol)
			}

			// Verify block is still pending.
			store.mu.Lock()
			status := store.pendingBlocks[0].Status
			store.mu.Unlock()
			if status != StatusPending {
				t.Errorf("coin %s: block status = %q; want %q", coin.Symbol, status, StatusPending)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 9: GetBlockStats fails — Stats returns error
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_GetStatsFails_ReturnsError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	store.errGetStats = fmt.Errorf("injected: stats query timeout")

	stats, err := proc.Stats(ctx)
	if err == nil {
		t.Fatal("Stats returned nil error; expected error when GetBlockStats fails")
	}
	if stats != nil {
		t.Errorf("Stats returned non-nil result %+v; expected nil on error", stats)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test 10: Multiple blocks — partial stability write failure
// ═══════════════════════════════════════════════════════════════════════════════

func TestSOLO_DBResilience_MultiBlock_PartialWriteFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block A: at maturity (height 800, chain 1000 → 200 confs ≥ 100). Hash matches.
	blockA := makePendingBlock("BTC", 800)
	store.addPendingBlock(blockA)
	rpc.setBlockHash(800, blockA.Hash)

	// Block B: at maturity (height 850, chain 1000 → 150 confs ≥ 100). Hash matches.
	blockB := makePendingBlock("LTC", 850)
	store.addPendingBlock(blockB)
	rpc.setBlockHash(850, blockB.Hash)

	// Block C: below maturity (height 950, chain 1000 → 50 confs < 100). Hash matches.
	blockC := makePendingBlock("DGB", 950)
	store.addPendingBlock(blockC)
	rpc.setBlockHash(950, blockC.Hash)

	// Inject: stability count writes fail for all blocks.
	store.errUpdateStability = fmt.Errorf("injected: stability write I/O error")

	// Run StabilityWindowChecks cycles so the at-maturity blocks can confirm.
	for i := 0; i < StabilityWindowChecks; i++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// Block A and B should be confirmed (stability tracked in-memory despite DB failure).
	if !store.hasStatusUpdateFor(800, StatusConfirmed) {
		t.Error("block A (height 800) was not confirmed — stability DB failure should not prevent confirmation")
	}
	if !store.hasStatusUpdateFor(850, StatusConfirmed) {
		t.Error("block B (height 850) was not confirmed — stability DB failure should not prevent confirmation")
	}

	// Block C should have received progress updates (still pending, below maturity).
	// It should have status updates with "pending" status (progress updates).
	if !store.hasStatusUpdateFor(950, StatusPending) {
		t.Error("block C (height 950) did not receive any progress update")
	}

	// Verify block C is still pending (not falsely confirmed or orphaned).
	store.mu.Lock()
	var blockCStatus string
	for _, b := range store.pendingBlocks {
		if b.Height == 950 {
			blockCStatus = b.Status
			break
		}
	}
	store.mu.Unlock()
	if blockCStatus != StatusPending {
		t.Errorf("block C status = %q; want %q", blockCStatus, StatusPending)
	}
}
