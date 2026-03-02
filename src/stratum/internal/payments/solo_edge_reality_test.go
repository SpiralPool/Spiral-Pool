// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Edge case and boundary condition test suite for SOLO mode payment processor.
//
// Tests extreme values, bulk processing, exact threshold boundaries, and
// mixed-state scenarios that exercise the limits of updateBlockConfirmations
// and verifyConfirmedBlocks.
package payments

import (
	"context"
	"testing"
)

// TestSOLO_Edge_100PendingBlocks_SingleCycle adds 100 pending blocks at heights
// 500000-500099 with chain tip at 500200 (all have 100+ confirmations). After
// StabilityWindowChecks cycles with matching hashes, all 100 must be confirmed.
func TestSOLO_Edge_100PendingBlocks_SingleCycle(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	baseHeight := uint64(500000)
	chainHeight := uint64(500200)
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	// Add 100 pending blocks and set matching hashes for each.
	for i := uint64(0); i < 100; i++ {
		h := baseHeight + i
		block := makePendingBlock("TEST", h)
		store.addPendingBlock(block)
		rpc.setBlockHash(h, makeBlockHash("TEST", h))
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	ctx := context.Background()

	// Run StabilityWindowChecks cycles so all blocks accumulate enough
	// stability observations to be confirmed.
	for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", cycle, err)
		}
	}

	// Verify ALL 100 blocks were confirmed.
	confirmedCount := 0
	for i := uint64(0); i < 100; i++ {
		h := baseHeight + i
		if store.hasStatusUpdateFor(h, StatusConfirmed) {
			confirmedCount++
		}
	}

	if confirmedCount != 100 {
		t.Fatalf("expected all 100 blocks confirmed, got %d", confirmedCount)
	}
}

// TestSOLO_Edge_50ConfirmedBlocks_DeepReorgCheck adds 50 confirmed blocks at
// heights 600000-600049 with chain tip at 600500 (within DeepReorgMaxAge).
// All hashes match, so verifyConfirmedBlocks must produce zero orphans and
// GetBlockHash must have been called 50 times.
func TestSOLO_Edge_50ConfirmedBlocks_DeepReorgCheck(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	baseHeight := uint64(600000)
	chainHeight := uint64(600500)
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	for i := uint64(0); i < 50; i++ {
		h := baseHeight + i
		block := makeConfirmedBlock("TEST", h)
		store.addConfirmedBlock(block)
		rpc.setBlockHash(h, makeBlockHash("TEST", h))
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	ctx := context.Background()
	err := proc.verifyConfirmedBlocks(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify zero orphans.
	if store.orphanUpdateCount() != 0 {
		t.Errorf("expected 0 orphan updates, got %d", store.orphanUpdateCount())
	}
	for i := uint64(0); i < 50; i++ {
		h := baseHeight + i
		if store.hasStatusUpdateFor(h, StatusOrphaned) {
			t.Errorf("block at height %d was incorrectly orphaned", h)
		}
	}

	// Verify GetBlockHash was called exactly 50 times.
	hashCalls := rpc.getBlockHashCalls.Load()
	if hashCalls != 50 {
		t.Errorf("expected 50 GetBlockHash calls, got %d", hashCalls)
	}
}

// TestSOLO_Edge_BlockHeightZero adds a pending block at height 0. With chain
// tip at 200 and a matching hash, it must be confirmable after stability cycles.
func TestSOLO_Edge_BlockHeightZero(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(0)
	chainHeight := uint64(200)
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	block := makePendingBlock("TEST", blockHeight)
	store.addPendingBlock(block)
	rpc.setBlockHash(blockHeight, makeBlockHash("TEST", blockHeight))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", cycle, err)
		}
	}

	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block at height 0 was not confirmed after stability cycles")
	}
}

// TestSOLO_Edge_BlockHeightOne adds a pending block at height 1. With chain
// tip at 200 and a matching hash, it must be confirmable after stability cycles.
func TestSOLO_Edge_BlockHeightOne(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(1)
	chainHeight := uint64(200)
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	block := makePendingBlock("TEST", blockHeight)
	store.addPendingBlock(block)
	rpc.setBlockHash(blockHeight, makeBlockHash("TEST", blockHeight))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", cycle, err)
		}
	}

	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block at height 1 was not confirmed after stability cycles")
	}
}

// TestSOLO_Edge_OrphanThresholdExactBoundary verifies the precise orphan
// mismatch boundary. After exactly OrphanMismatchThreshold-1 cycles with a
// wrong hash, the block must NOT be orphaned. On the next cycle it must be
// orphaned.
func TestSOLO_Edge_OrphanThresholdExactBoundary(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(800000)
	chainHeight := blockHeight + 50
	tip := makeChainTip(chainHeight)
	wrongHash := "ffffffffdeadbeef0000000000000000aaaaaaaabbbbbbbbccccccccdddddddd"

	rpc.setChainTip(chainHeight, tip)

	block := makePendingBlock("TEST", blockHeight)
	store.addPendingBlock(block)
	rpc.setBlockHash(blockHeight, wrongHash) // Wrong hash.

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run exactly OrphanMismatchThreshold-1 cycles.
	for i := 0; i < OrphanMismatchThreshold-1; i++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// Verify NOT orphaned yet.
	if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
		t.Fatalf("block orphaned after only %d mismatches (threshold is %d)",
			OrphanMismatchThreshold-1, OrphanMismatchThreshold)
	}

	// Run one more cycle to hit the exact threshold.
	err := proc.updateBlockConfirmations(ctx)
	if err != nil {
		t.Fatalf("threshold cycle: unexpected error: %v", err)
	}

	// Verify IS orphaned now.
	if !store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
		t.Fatalf("block NOT orphaned after exactly %d mismatches", OrphanMismatchThreshold)
	}
}

// TestSOLO_Edge_StabilityThresholdExactBoundary verifies the precise stability
// window boundary. After exactly StabilityWindowChecks-1 stable cycles at
// maturity, the block must NOT be confirmed. On the next cycle it must be
// confirmed.
func TestSOLO_Edge_StabilityThresholdExactBoundary(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(900000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	block := makePendingBlock("TEST", blockHeight)
	store.addPendingBlock(block)
	rpc.setBlockHash(blockHeight, makeBlockHash("TEST", blockHeight))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run exactly StabilityWindowChecks-1 cycles.
	for i := 0; i < StabilityWindowChecks-1; i++ {
		err := proc.updateBlockConfirmations(ctx)
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// Verify NOT confirmed yet.
	if store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatalf("block confirmed after only %d stability checks (need %d)",
			StabilityWindowChecks-1, StabilityWindowChecks)
	}

	// Run one more cycle to hit the exact threshold.
	err := proc.updateBlockConfirmations(ctx)
	if err != nil {
		t.Fatalf("threshold cycle: unexpected error: %v", err)
	}

	// Verify IS confirmed now.
	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatalf("block NOT confirmed after exactly %d stability checks", StabilityWindowChecks)
	}
}

// TestSOLO_Edge_DeepReorgMaxAgeBoundary tests the exact boundary condition
// block.Height + DeepReorgMaxAge < bcInfo.Blocks (strict less-than).
//
// Case A: height=1000, chain=1000+DeepReorgMaxAge (2000)
//
//	1000+1000 < 2000 -> 2000 < 2000 -> false -> NOT skipped -> checked
//
// Case B: height=1000, chain=1000+DeepReorgMaxAge+1 (2001)
//
//	1000+1000 < 2001 -> 2000 < 2001 -> true -> skipped
func TestSOLO_Edge_DeepReorgMaxAgeBoundary(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000)
	wrongHash := "ffffffffdeadbeef0000000000000000aaaaaaaabbbbbbbbccccccccdddddddd"

	// Case A: chain at exactly height + DeepReorgMaxAge -> NOT skipped.
	t.Run("ExactBoundary_NotSkipped", func(t *testing.T) {
		t.Parallel()

		chainHeightA := blockHeight + DeepReorgMaxAge // 2000
		tipA := makeChainTip(chainHeightA)

		storeA := newErrInjectBlockStore()
		rpcA := newErrInjectDaemonRPC()

		rpcA.setChainTip(chainHeightA, tipA)

		blockA := makeConfirmedBlock("TEST", blockHeight)
		storeA.addConfirmedBlock(blockA)
		rpcA.setBlockHash(blockHeight, wrongHash) // Wrong hash to detect orphan.

		procA := newParanoidTestProcessor(storeA, rpcA, DefaultBlockMaturityConfirmations)
		ctx := context.Background()

		err := procA.verifyConfirmedBlocks(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Block should be checked and orphaned because hash is wrong.
		if !storeA.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
			t.Fatal("block at exact boundary (height+MaxAge == chainHeight) should be checked and orphaned")
		}
	})

	// Case B: chain at height + DeepReorgMaxAge + 1 -> skipped.
	t.Run("BeyondBoundary_Skipped", func(t *testing.T) {
		t.Parallel()

		chainHeightB := blockHeight + DeepReorgMaxAge + 1 // 2001
		tipB := makeChainTip(chainHeightB)

		storeB := newErrInjectBlockStore()
		rpcB := newErrInjectDaemonRPC()

		rpcB.setChainTip(chainHeightB, tipB)

		blockB := makeConfirmedBlock("TEST", blockHeight)
		storeB.addConfirmedBlock(blockB)
		rpcB.setBlockHash(blockHeight, wrongHash) // Wrong hash, but should not matter.

		procB := newParanoidTestProcessor(storeB, rpcB, DefaultBlockMaturityConfirmations)
		ctx := context.Background()

		err := procB.verifyConfirmedBlocks(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Block should be skipped (too old) and NOT orphaned.
		if storeB.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
			t.Fatal("block beyond boundary (height+MaxAge < chainHeight) should be skipped, not orphaned")
		}

		// GetBlockHash should NOT have been called for the skipped block.
		hashCalls := rpcB.getBlockHashCalls.Load()
		if hashCalls != 0 {
			t.Errorf("expected 0 GetBlockHash calls for skipped block, got %d", hashCalls)
		}
	})
}

// TestSOLO_Edge_ProcessCycleCount_Increment verifies that cycleCount starts at
// 0, increments correctly through processCycle calls, and that the deep reorg
// check triggers at the correct interval (multiples of DeepReorgCheckInterval).
func TestSOLO_Edge_ProcessCycleCount_Increment(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	chainHeight := uint64(10000)
	tip := makeChainTip(chainHeight)
	rpc.setChainTip(chainHeight, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Verify cycleCount starts at 0.
	if proc.cycleCount != 0 {
		t.Fatalf("expected initial cycleCount 0, got %d", proc.cycleCount)
	}

	// Run 5 cycles.
	for i := 0; i < 5; i++ {
		proc.processCycle(ctx)
	}

	if proc.cycleCount != 5 {
		t.Fatalf("expected cycleCount 5 after 5 cycles, got %d", proc.cycleCount)
	}

	// Verify deep reorg was NOT triggered (cycles 1-5, none are multiples of 10).
	store.mu.Lock()
	calledAfter5 := store.getConfirmedCalled
	store.mu.Unlock()

	if calledAfter5 {
		t.Error("deep reorg check should NOT trigger on cycles 1-5")
	}

	// Run 5 more cycles (total 10). Cycle 10 is a multiple of DeepReorgCheckInterval.
	for i := 0; i < 5; i++ {
		proc.processCycle(ctx)
	}

	if proc.cycleCount != 10 {
		t.Fatalf("expected cycleCount 10 after 10 cycles, got %d", proc.cycleCount)
	}

	// Verify deep reorg WAS triggered on cycle 10.
	store.mu.Lock()
	calledAfter10 := store.getConfirmedCalled
	store.mu.Unlock()

	if !calledAfter10 {
		t.Error("deep reorg check should trigger on cycle 10 (multiple of DeepReorgCheckInterval)")
	}
}

// TestSOLO_Edge_NoPendingBlocks_ShortCircuit verifies that when there are no
// pending blocks, updateBlockConfirmations returns nil immediately without
// calling GetBlockchainInfo (short circuit at the empty check).
func TestSOLO_Edge_NoPendingBlocks_ShortCircuit(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	chainHeight := uint64(10000)
	tip := makeChainTip(chainHeight)
	rpc.setChainTip(chainHeight, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// No pending blocks added.
	err := proc.updateBlockConfirmations(ctx)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// GetBlockchainInfo should NOT have been called (short circuit).
	infoCalls := rpc.getBlockchainInfoCalls.Load()
	if infoCalls != 0 {
		t.Errorf("expected 0 GetBlockchainInfo calls (short circuit), got %d", infoCalls)
	}
}

// TestSOLO_Edge_NoConfirmedBlocks_ShortCircuit verifies that when there are no
// confirmed blocks, verifyConfirmedBlocks returns nil immediately without
// calling GetBlockHash.
func TestSOLO_Edge_NoConfirmedBlocks_ShortCircuit(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	chainHeight := uint64(10000)
	tip := makeChainTip(chainHeight)
	rpc.setChainTip(chainHeight, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// No confirmed blocks added.
	err := proc.verifyConfirmedBlocks(ctx)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// GetBlockHash should NOT have been called.
	hashCalls := rpc.getBlockHashCalls.Load()
	if hashCalls != 0 {
		t.Errorf("expected 0 GetBlockHash calls (short circuit), got %d", hashCalls)
	}
}

// TestSOLO_Edge_ProgressNeverExceeds1 verifies that ConfirmationProgress is
// capped at 1.0 even when the block has more confirmations than the maturity
// threshold (e.g., 200 confirmations with maturity of 100).
func TestSOLO_Edge_ProgressNeverExceeds1(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(700000)
	chainHeight := blockHeight + 200 // 200 confirmations, maturity is 100.
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	block := makePendingBlock("TEST", blockHeight)
	store.addPendingBlock(block)
	rpc.setBlockHash(blockHeight, makeBlockHash("TEST", blockHeight))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run one cycle to get a status update with progress.
	err := proc.updateBlockConfirmations(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Inspect statusUpdates to check that progress is exactly 1.0, not 2.0.
	store.mu.Lock()
	updates := make([]statusUpdateRecord, len(store.statusUpdates))
	copy(updates, store.statusUpdates)
	store.mu.Unlock()

	found := false
	for _, u := range updates {
		if u.Height == blockHeight {
			found = true
			if u.Progress != 1.0 {
				t.Errorf("expected ConfirmationProgress capped at 1.0, got %f", u.Progress)
			}
			if u.Progress > 1.0 {
				t.Errorf("ConfirmationProgress exceeds 1.0: %f (200 confirmations / 100 maturity)", u.Progress)
			}
		}
	}

	if !found {
		t.Fatal("no status update found for the block")
	}
}

// TestSOLO_Edge_MixedPendingAndConfirmed adds 3 pending blocks and 2 confirmed
// blocks. The processor runs a processCycle where cycleCount is set so that the
// deep reorg check triggers. Pending blocks have matching hashes and progress
// toward confirmation. One confirmed block has a wrong hash and should be
// orphaned by the deep reorg check.
func TestSOLO_Edge_MixedPendingAndConfirmed(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	pendingBase := uint64(400000)
	chainHeight := pendingBase + uint64(DefaultBlockMaturityConfirmations) + 10
	confirmedBase := chainHeight - 500
	tip := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tip)

	// Add 3 pending blocks with matching hashes.
	for i := uint64(0); i < 3; i++ {
		h := pendingBase + i
		block := makePendingBlock("TEST", h)
		store.addPendingBlock(block)
		rpc.setBlockHash(h, makeBlockHash("TEST", h))
	}

	// Add 2 confirmed blocks. First has matching hash, second has wrong hash.
	confirmedHeight0 := confirmedBase
	confirmedHeight1 := confirmedBase + 1

	block0 := makeConfirmedBlock("TEST", confirmedHeight0)
	store.addConfirmedBlock(block0)
	rpc.setBlockHash(confirmedHeight0, makeBlockHash("TEST", confirmedHeight0))

	block1 := makeConfirmedBlock("TEST", confirmedHeight1)
	store.addConfirmedBlock(block1)
	wrongHash := "ffffffffdeadbeef0000000000000000aaaaaaaabbbbbbbbccccccccdddddddd"
	rpc.setBlockHash(confirmedHeight1, wrongHash) // Wrong hash triggers orphan.

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Set cycleCount so that on the next processCycle call, cycleCount becomes
	// a multiple of DeepReorgCheckInterval, triggering verifyConfirmedBlocks.
	proc.cycleCount = DeepReorgCheckInterval - 1

	ctx := context.Background()
	proc.processCycle(ctx)

	// Verify: pending blocks should have had progress status updates.
	pendingUpdated := 0
	for i := uint64(0); i < 3; i++ {
		h := pendingBase + i
		if store.hasStatusUpdateFor(h, StatusPending) {
			pendingUpdated++
		}
	}
	if pendingUpdated == 0 {
		t.Error("expected at least some pending block status updates (progress tracking)")
	}

	// Verify: confirmed block with wrong hash should be orphaned by deep reorg.
	if !store.hasStatusUpdateFor(confirmedHeight1, StatusOrphaned) {
		t.Error("confirmed block with wrong hash should be orphaned by deep reorg check")
	}

	// Verify: confirmed block with correct hash should NOT be orphaned.
	if store.hasStatusUpdateFor(confirmedHeight0, StatusOrphaned) {
		t.Error("confirmed block with matching hash should NOT be orphaned")
	}
}
