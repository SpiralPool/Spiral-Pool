// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Vectors V5 (concurrent reconciliation + processor) and V10 (two processor
// instances racing on the same block store).
//
// These tests verify behavior when multiple goroutines or processor instances
// concurrently modify block state. The errInjectBlockStore and errInjectDaemonRPC
// mocks are already mutex-protected, so concurrent access is safe from a memory
// perspective. The tests document the semantic consequences of concurrent state
// mutations, which are real production risks.
//
// These tests use the error-injecting mocks from solo_mocks_test.go
// (errInjectBlockStore, errInjectDaemonRPC) and exercise Processor methods
// directly since we are in the same package (white-box testing).
package payments

import (
	"context"
	"sync"
	"testing"
)

// =============================================================================
// Test 1: Two processor instances race to confirm the same block.
//
// V10: Without locking, two processors both confirm the same block independently.
// Both processors share the same store and RPC mock. Each runs
// StabilityWindowChecks cycles. Both will attempt to confirm the block. The
// number of "confirmed" status updates recorded may exceed what a single
// processor would produce, demonstrating that dual processing duplicates work.
// =============================================================================

func TestSOLO_Vector_V10_DualProcessorRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Block at height 800, correct hash, chain at 1000 (200 confs >= 100 maturity).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// Create two independent processor instances sharing the same store and RPC.
	procA := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	procB := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: processorA runs StabilityWindowChecks cycles.
	go func() {
		defer wg.Done()
		for i := 0; i < StabilityWindowChecks; i++ {
			_ = procA.updateBlockConfirmations(ctx)
		}
	}()

	// Goroutine B: processorB runs StabilityWindowChecks cycles concurrently.
	go func() {
		defer wg.Done()
		for i := 0; i < StabilityWindowChecks; i++ {
			_ = procB.updateBlockConfirmations(ctx)
		}
	}()

	wg.Wait()

	// Count total status updates for height 800 with "confirmed" status.
	// Both processors independently modify the same block's in-memory state
	// (via the shared pointer returned by GetPendingBlocks). Depending on
	// interleaving, the stability counter may be incremented by both processors,
	// leading to early confirmation and potentially duplicate status writes.
	store.mu.Lock()
	var confirmedCount int
	for _, u := range store.statusUpdates {
		if u.Height == 800 && u.Status == StatusConfirmed {
			confirmedCount++
		}
	}
	totalStatusUpdates := len(store.statusUpdates)
	finalStatus := store.pendingBlocks[0].Status
	store.mu.Unlock()

	// The test PASSES either way. The point is documenting the race.
	// With two processors sharing state, we may see:
	//   - Multiple confirmed updates (both processors confirmed the block)
	//   - Or a single confirmed update (one processor confirmed, the other
	//     found the block no longer pending on subsequent GetPendingBlocks)
	t.Logf("V10: Dual processor race result: confirmedUpdates=%d, totalStatusUpdates=%d, finalStatus=%q",
		confirmedCount, totalStatusUpdates, finalStatus)

	if confirmedCount == 0 && finalStatus != StatusConfirmed {
		t.Errorf("expected at least one confirmed status update or final status=confirmed; got confirmedUpdates=%d, finalStatus=%q",
			confirmedCount, finalStatus)
	}

	t.Logf("V10: Without locking, two processors both confirm the same block independently")
}

// =============================================================================
// Test 2: External status modification (reconciliation) orphans a confirmed
// block. Once orphaned via external write, the confirmation processor never
// re-examines it because GetPendingBlocks filters by Status==StatusPending.
//
// V5: External status modification (reconciliation) can orphan a previously
// confirmed block. Once orphaned, the confirmation processor never re-examines it.
// =============================================================================

func TestSOLO_Vector_V5_ReconciliationOverwritesConfirmation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block at height 800, correct hash, chain at 1000 (200 confs >= 100 maturity).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// Run StabilityWindowChecks cycles to confirm the block.
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("cycle %d: updateBlockConfirmations returned error: %v", i+1, err)
		}
	}

	// Verify the block was confirmed.
	if !store.hasStatusUpdateFor(800, StatusConfirmed) {
		t.Fatalf("block at height 800 should be confirmed after %d stability cycles", StabilityWindowChecks)
	}

	// Simulate external reconciliation: directly overwrite status to "orphaned".
	// This represents a concurrent reconciliation process or admin action that
	// marks the block as orphaned after the processor already confirmed it.
	store.mu.Lock()
	for _, b := range store.pendingBlocks {
		if b.Height == 800 {
			b.Status = StatusOrphaned
		}
	}
	store.statusUpdates = append(store.statusUpdates, statusUpdateRecord{
		Height: 800, Status: StatusOrphaned, Progress: 0,
	})
	store.mu.Unlock()

	// Run one more processCycle. The processor calls GetPendingBlocks which
	// filters by Status==StatusPending. Since the block is now "orphaned",
	// it will NOT appear in the pending list. The processor will not process it.
	proc.processCycle(ctx)

	// Verify: the block remains orphaned. The processor cannot fix it because
	// the block is excluded from GetPendingBlocks.
	store.mu.Lock()
	finalStatus := store.pendingBlocks[0].Status
	store.mu.Unlock()

	if finalStatus != StatusOrphaned {
		t.Errorf("final Status = %q; want %q (orphaned block should remain orphaned after processCycle)",
			finalStatus, StatusOrphaned)
	}

	// Verify the processor did not re-confirm the block. Check that the last
	// status update for height 800 is "orphaned", not "confirmed".
	store.mu.Lock()
	var lastStatusForBlock string
	for _, u := range store.statusUpdates {
		if u.Height == 800 {
			lastStatusForBlock = u.Status
		}
	}
	store.mu.Unlock()

	if lastStatusForBlock != StatusOrphaned {
		t.Errorf("last status update for height 800 = %q; want %q", lastStatusForBlock, StatusOrphaned)
	}

	t.Logf("V5: External status modification (reconciliation) can orphan a previously confirmed block. Once orphaned, the confirmation processor never re-examines it.")
}

// =============================================================================
// Test 3: Two processors concurrently process divergent blocks.
// blockA (correct hash) should be confirmed. blockB (wrong hash) should be
// orphaned. Despite dual processing, the final states converge correctly.
//
// V10: Dual processors with divergent blocks converge to correct final states.
// =============================================================================

func TestSOLO_Vector_V10_DualProcessorDivergentView(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// blockA at height 800: correct hash (will confirm).
	blockA := makePendingBlock("BTC", 800)
	store.addPendingBlock(blockA)
	rpc.setBlockHash(800, blockA.Hash)

	// blockB at height 900: WRONG hash (will orphan).
	blockB := makePendingBlock("BTC", 900)
	store.addPendingBlock(blockB)
	rpc.setBlockHash(900, makeBlockHash("WRONG", 900)) // Different from blockB.Hash

	// Create two independent processor instances sharing the same store and RPC.
	procA := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	procB := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Both processors need enough cycles for:
	//   - blockA: StabilityWindowChecks (3) cycles to confirm
	//   - blockB: OrphanMismatchThreshold (3) cycles to orphan
	// Use the max of both plus a margin for safety.
	totalCycles := StabilityWindowChecks
	if OrphanMismatchThreshold > totalCycles {
		totalCycles = OrphanMismatchThreshold
	}
	totalCycles += 2 // Extra cycles for margin

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < totalCycles; i++ {
			_ = procA.updateBlockConfirmations(ctx)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < totalCycles; i++ {
			_ = procB.updateBlockConfirmations(ctx)
		}
	}()

	wg.Wait()

	// Check final states.
	store.mu.Lock()
	var blockAStatus, blockBStatus string
	for _, b := range store.pendingBlocks {
		if b.Height == 800 {
			blockAStatus = b.Status
		}
		if b.Height == 900 {
			blockBStatus = b.Status
		}
	}
	var confirmedCountA, orphanedCountB int
	for _, u := range store.statusUpdates {
		if u.Height == 800 && u.Status == StatusConfirmed {
			confirmedCountA++
		}
		if u.Height == 900 && u.Status == StatusOrphaned {
			orphanedCountB++
		}
	}
	totalUpdates := len(store.statusUpdates)
	store.mu.Unlock()

	// blockA should eventually be confirmed.
	if blockAStatus != StatusConfirmed {
		t.Errorf("blockA (height 800) Status = %q; want %q", blockAStatus, StatusConfirmed)
	}

	// blockB should eventually be orphaned.
	if blockBStatus != StatusOrphaned {
		t.Errorf("blockB (height 900) Status = %q; want %q", blockBStatus, StatusOrphaned)
	}

	t.Logf("V10: Dual processors with divergent blocks: blockA=%q (confirmedUpdates=%d), blockB=%q (orphanedUpdates=%d), totalStatusUpdates=%d",
		blockAStatus, confirmedCountA, blockBStatus, orphanedCountB, totalUpdates)

	// Both processors independently process both blocks. With shared pointer
	// state, both may contribute to the stability/orphan counters. Log the
	// total updates to show redundant work.
	if confirmedCountA > 0 && orphanedCountB > 0 {
		t.Logf("V10: Both blocks reached correct terminal states despite concurrent dual processing")
	}
}
