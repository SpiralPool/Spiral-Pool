// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Vector V3: Non-atomic multi-step state mutation tests.
//
// The processor makes 2-3 separate DB calls per block per cycle:
//   - UpdateBlockStabilityCount (counter update)
//   - UpdateBlockStatus (status transition)
//   - UpdateBlockOrphanCount (orphan counter update)
//
// A crash or error between these calls leaves inconsistent state. These tests
// exercise partial-write scenarios to verify that the processor recovers on
// subsequent cycles rather than silently losing blocks.
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

// =============================================================================
// Test 1: Stability counter persists but status write fails across all cycles.
// After DB recovery, the processor picks up the persisted stability count and
// confirms on the next successful cycle.
//
// V3: After partial write (stability persisted, status not), processor recovers
// on next successful cycle.
// =============================================================================

func TestSOLO_Vector_V3_StabilityWriteSucceeds_StatusWriteFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block at height 800, correct hash, chain at 1000 (200 confs >= 100 maturity).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// Inject: all status writes fail. Stability writes still succeed.
	store.errUpdateStatus = fmt.Errorf("injected: status write I/O error")

	// Run StabilityWindowChecks (3) cycles. Each cycle:
	//   - Stability counter increments and is persisted via UpdateBlockStabilityCount (succeeds)
	//   - UpdateBlockStatus is called but fails (errUpdateStatus set)
	//   - Block remains "pending" in the mock because status write is rejected
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("cycle %d: updateBlockConfirmations returned error: %v", i+1, err)
		}
	}

	// Verify: stability counter was written to the mock successfully.
	// The mock's UpdateBlockStabilityCount mutates the in-memory block pointer,
	// so the stored block should reflect all stability increments.
	store.mu.Lock()
	stabilityCount := store.pendingBlocks[0].StabilityCheckCount
	status := store.pendingBlocks[0].Status
	store.mu.Unlock()

	if stabilityCount != StabilityWindowChecks {
		t.Errorf("StabilityCheckCount = %d; want %d (stability writes should succeed)",
			stabilityCount, StabilityWindowChecks)
	}

	// Verify: status is still "pending" because every status write failed.
	if status != StatusPending {
		t.Errorf("Status = %q; want %q (status writes all failed)", status, StatusPending)
	}

	// Verify: no status updates were recorded (they all failed).
	if n := store.statusUpdateCount(); n != 0 {
		t.Errorf("statusUpdateCount = %d; want 0 (all status writes failed)", n)
	}

	// --- Simulate DB recovery: clear the error ---
	store.mu.Lock()
	store.errUpdateStatus = nil
	store.mu.Unlock()

	// Run one more cycle. The processor loads the block with stability=3 (from
	// the mock pointer). It then increments: stability becomes 4. Since 4 >= 3
	// (StabilityWindowChecks), it sets status = "confirmed" and calls
	// UpdateBlockStatus which now succeeds.
	if err := proc.updateBlockConfirmations(ctx); err != nil {
		t.Fatalf("recovery cycle: updateBlockConfirmations returned error: %v", err)
	}

	// Verify: the block is now confirmed.
	if !store.hasStatusUpdateFor(800, StatusConfirmed) {
		t.Errorf("expected StatusConfirmed update for height 800 after DB recovery")
	}

	store.mu.Lock()
	finalStatus := store.pendingBlocks[0].Status
	store.mu.Unlock()

	if finalStatus != StatusConfirmed {
		t.Errorf("final Status = %q; want %q (recovery cycle should confirm)", finalStatus, StatusConfirmed)
	}

	t.Logf("V3: After partial write (stability persisted, status not), processor recovers on next successful cycle")
}

// =============================================================================
// Test 2: Orphan counter reaches threshold but the status write to mark
// "orphaned" fails. The processor recovers on the next cycle.
//
// V3: Orphan threshold reached but status write failed. Processor recovers on
// next cycle.
// =============================================================================

func TestSOLO_Vector_V3_OrphanCountWriteSucceeds_StatusWriteFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block at height 800 with WRONG hash (chain has a different hash).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, makeBlockHash("WRONG", 800)) // Different from block.Hash

	// Inject: status writes fail. Orphan count writes succeed.
	store.errUpdateStatus = fmt.Errorf("injected: status write failure")

	// Run OrphanMismatchThreshold (3) cycles.
	//
	// Cycle 1: block.OrphanMismatchCount++ -> 1. 1 < 3. UpdateBlockOrphanCount(800, 1) succeeds.
	// Cycle 2: GetPendingBlocks returns same pointer (count=1). ++ -> 2. UpdateBlockOrphanCount(800, 2) succeeds.
	// Cycle 3: GetPendingBlocks returns same pointer (count=2). ++ -> 3. 3 >= 3.
	//          UpdateBlockStatus(800, "orphaned") -> FAILS (errUpdateStatus).
	//          Block stays "pending" in mock.
	for i := 0; i < OrphanMismatchThreshold; i++ {
		if err := proc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("cycle %d: updateBlockConfirmations returned error: %v", i+1, err)
		}
	}

	// Verify: orphan counter has reached (or exceeded) the threshold.
	store.mu.Lock()
	orphanCount := store.pendingBlocks[0].OrphanMismatchCount
	status := store.pendingBlocks[0].Status
	store.mu.Unlock()

	if orphanCount < OrphanMismatchThreshold {
		t.Errorf("OrphanMismatchCount = %d; want >= %d (counter should have reached threshold)",
			orphanCount, OrphanMismatchThreshold)
	}

	// Verify: status is still "pending" because the status write failed on cycle 3.
	if status != StatusPending {
		t.Errorf("Status = %q; want %q (status write failed at threshold)", status, StatusPending)
	}

	// --- Simulate DB recovery: clear the error ---
	store.mu.Lock()
	store.errUpdateStatus = nil
	store.mu.Unlock()

	// Run one more cycle. The processor loads the block with OrphanMismatchCount=3
	// (from mock pointer). It detects hash mismatch again: ++ -> 4. 4 >= 3.
	// UpdateBlockStatus(800, "orphaned") now succeeds.
	if err := proc.updateBlockConfirmations(ctx); err != nil {
		t.Fatalf("recovery cycle: updateBlockConfirmations returned error: %v", err)
	}

	// Verify: block is now marked orphaned.
	if !store.hasStatusUpdateFor(800, StatusOrphaned) {
		t.Errorf("expected StatusOrphaned update for height 800 after DB recovery")
	}

	store.mu.Lock()
	finalStatus := store.pendingBlocks[0].Status
	store.mu.Unlock()

	if finalStatus != StatusOrphaned {
		t.Errorf("final Status = %q; want %q (recovery cycle should orphan)", finalStatus, StatusOrphaned)
	}

	t.Logf("V3: Orphan threshold reached but status write failed. Processor recovers on next cycle.")
}

// =============================================================================
// Test 3: Stability counter is NOT reset when the chain tip changes
// between cycles (deliberate design for fast-block chains). Even when
// the status write fails, the pointer-sharing mock retains the in-memory
// mutation.
//
// V3: Partial write + tip change — stability continues accumulating.
// =============================================================================

func TestSOLO_Vector_V3_StabilityTipInconsistencyAfterPartialWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Block at height 800, correct hash, chain at 1000 (200 confs >= 100 maturity).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, block.Hash)

	// Run 2 cycles normally. Stability should reach 2.
	for i := 0; i < 2; i++ {
		if err := proc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("normal cycle %d: updateBlockConfirmations returned error: %v", i+1, err)
		}
	}

	// Verify stability is 2 after normal cycles.
	store.mu.Lock()
	stabilityAfterNormal := store.pendingBlocks[0].StabilityCheckCount
	store.mu.Unlock()

	if stabilityAfterNormal != 2 {
		t.Fatalf("StabilityCheckCount after 2 normal cycles = %d; want 2", stabilityAfterNormal)
	}

	// Inject: status writes fail for the remaining cycle.
	store.mu.Lock()
	store.errUpdateStatus = fmt.Errorf("injected: status write timeout")
	store.mu.Unlock()

	// Change the chain tip. The processor no longer resets stability on tip
	// change (deliberate design for fast-block chains). Stability continues
	// from 2 and increments to 3.
	rpc.setChainTip(1001, makeChainTip(1001))

	// Run 1 cycle with the new tip and failing status writes.
	//
	// Processor logic:
	//   1. GetPendingBlocks -> block with StabilityCheckCount=2, LastVerifiedTip=old_tip
	//   2. GetBlockchainInfo -> new tip (1001)
	//   3. TOCTOU check passes (tip stable within cycle)
	//   4. Hash matches (rpc still has correct hash for height 800)
	//   5. confirmations = 1001 - 800 = 201 >= 100 (maturity)
	//   6. block.StabilityCheckCount++ -> 3
	//   7. 3 >= 3 -> status set to "confirmed"
	//   8. UpdateBlockConfirmationState(800, ..., "confirmed", ..., 3, ...) -> FAILS
	//   9. Pointer-sharing: in-memory mutation persists (stability=3, status=confirmed)
	if err := proc.updateBlockConfirmations(ctx); err != nil {
		t.Fatalf("tip-change cycle: updateBlockConfirmations returned error: %v", err)
	}

	// Verify: stability incremented to 3 (no reset on tip change).
	store.mu.Lock()
	stabilityAfterTipChange := store.pendingBlocks[0].StabilityCheckCount
	statusAfterTipChange := store.pendingBlocks[0].Status
	store.mu.Unlock()

	if stabilityAfterTipChange != 3 {
		t.Errorf("StabilityCheckCount after tip change = %d; want 3 (2 + 1, no reset on tip change)",
			stabilityAfterTipChange)
	}

	// Status stays pending: the processor passes the new status to
	// UpdateBlockConfirmationState (which failed), so block.Status is never
	// mutated directly — only StabilityCheckCount is incremented in-memory.
	if statusAfterTipChange != StatusPending {
		t.Errorf("Status after tip change = %q; want %q", statusAfterTipChange, StatusPending)
	}

	t.Logf("V3: Partial write + tip change — stability continues (stability=%d, status=%q)",
		stabilityAfterTipChange, statusAfterTipChange)
}
