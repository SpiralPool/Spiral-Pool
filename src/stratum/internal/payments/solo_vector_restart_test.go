// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Vector V9: Restart-related tests for SOLO mode payment processor.
//
// These tests target the block-loss vector identified in audit: cycleCount is
// not persisted across restarts. When the processor restarts, cycleCount resets
// to 0, and processCycle increments it to 1 before checking cycleCount%10==0
// for deep reorg verification. This means verifyConfirmedBlocks does NOT run
// on the first cycle after a restart, leaving a window of up to 10 cycles
// where deep chain reorganizations during downtime go undetected.
package payments

import (
	"context"
	"testing"
)

// TestSOLO_Vector_V9_FreshProcessor_DeepReorgNotOnFirstCycle verifies that a
// fresh processor (simulating a restart) does NOT run verifyConfirmedBlocks on
// its first cycle, because cycleCount starts at 0, increments to 1, and
// 1%10!=0.
//
// V9: Fresh processor starts cycleCount at 0. First processCycle increments to
// 1. 1%10!=0 so verifyConfirmedBlocks is NOT called. Deep reorgs during
// downtime are not detected for 10 cycles.
func TestSOLO_Vector_V9_FreshProcessor_DeepReorgNotOnFirstCycle(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// One confirmed block at height 800 with a WRONG hash (deep reorg scenario).
	block := makeConfirmedBlock("BTC", 800)
	store.addConfirmedBlock(block)
	wrongHash := "ffff" + block.Hash[4:]
	rpc.setBlockHash(800, wrongHash)

	// Chain at height 1000 (within DeepReorgMaxAge of block 800).
	rpc.setChainTip(1000, makeChainTip(1000))

	// Fresh processor — cycleCount starts at 0.
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run exactly ONE processCycle.
	proc.processCycle(ctx)

	// After one cycle, cycleCount should be 1 (incremented from 0).
	if proc.cycleCount != 1 {
		t.Fatalf("V9: expected cycleCount=1 after first cycle, got %d", proc.cycleCount)
	}

	// Deep reorg check should NOT have run (1%10 != 0).
	store.mu.Lock()
	confirmedCalled := store.getConfirmedCalled
	store.mu.Unlock()

	if confirmedCalled {
		t.Errorf("V9: getConfirmedCalled should be false on first cycle (cycleCount=1, 1%%10!=0), "+
			"but verifyConfirmedBlocks was called")
	} else {
		t.Logf("V9 BUG: deep reorg check did not run on first cycle after restart "+
			"(cycleCount=%d, %d%%10=%d != 0)", proc.cycleCount, proc.cycleCount, proc.cycleCount%DeepReorgCheckInterval)
	}

	// The confirmed block with the wrong hash should NOT be orphaned yet.
	if store.hasStatusUpdateFor(800, StatusOrphaned) {
		t.Errorf("V9: block at height 800 should NOT be orphaned on first cycle — "+
			"deep reorg check did not run")
	}
}

// TestSOLO_Vector_V9_DeepReorgDetectedOnCycle10 verifies that the deep reorg
// check eventually runs at cycle 10 (cycleCount=10, 10%10==0) and detects
// the orphaned confirmed block.
//
// V9: Deep reorg is eventually detected at cycle 10, but NOT cycles 1-9.
func TestSOLO_Vector_V9_DeepReorgDetectedOnCycle10(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Confirmed block at height 800 with a WRONG hash.
	block := makeConfirmedBlock("BTC", 800)
	store.addConfirmedBlock(block)
	wrongHash := "ffff" + block.Hash[4:]
	rpc.setBlockHash(800, wrongHash)

	// Chain at height 1000 (within DeepReorgMaxAge).
	rpc.setChainTip(1000, makeChainTip(1000))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run exactly DeepReorgCheckInterval (10) processCycles.
	for i := 0; i < DeepReorgCheckInterval; i++ {
		proc.processCycle(ctx)
	}

	// After 10 cycles, cycleCount should be 10.
	if proc.cycleCount != DeepReorgCheckInterval {
		t.Fatalf("V9: expected cycleCount=%d after %d cycles, got %d",
			DeepReorgCheckInterval, DeepReorgCheckInterval, proc.cycleCount)
	}

	// Deep reorg check SHOULD have run on cycle 10 (10%10==0).
	store.mu.Lock()
	confirmedCalled := store.getConfirmedCalled
	store.mu.Unlock()

	if !confirmedCalled {
		t.Errorf("V9: verifyConfirmedBlocks should have been called at cycle 10 "+
			"(cycleCount=%d, %d%%10=%d)", proc.cycleCount, proc.cycleCount, proc.cycleCount%DeepReorgCheckInterval)
	}

	// The block should now be orphaned.
	if !store.hasStatusUpdateFor(800, StatusOrphaned) {
		t.Errorf("V9: block at height 800 should be orphaned after deep reorg check at cycle 10, "+
			"but no orphan status update was recorded")
	} else {
		t.Logf("V9: deep reorg correctly detected at cycle %d — block at height 800 orphaned",
			DeepReorgCheckInterval)
	}
}

// TestSOLO_Vector_V9_CycleCountResetSimulatesRestart verifies that creating a
// new processor (simulating a restart) resets cycleCount to 0, delaying deep
// reorg detection by up to 10 cycles.
//
// V9: Restart resets cycleCount to 0, delaying deep reorg detection by up to
// 10 cycles.
func TestSOLO_Vector_V9_CycleCountResetSimulatesRestart(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Confirmed block with wrong hash at height 800.
	block800 := makeConfirmedBlock("BTC", 800)
	store.addConfirmedBlock(block800)
	rpc.setBlockHash(800, "ffff"+block800.Hash[4:])

	// Chain at height 1000.
	rpc.setChainTip(1000, makeChainTip(1000))

	// First processor: set cycleCount to DeepReorgCheckInterval-1 (9).
	proc1 := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc1.cycleCount = DeepReorgCheckInterval - 1
	ctx := context.Background()

	// One cycle: cycleCount 9 -> 10, 10%10==0 -> deep reorg runs -> block 800 orphaned.
	proc1.processCycle(ctx)

	if !store.hasStatusUpdateFor(800, StatusOrphaned) {
		t.Fatalf("V9 SETUP: block 800 should be orphaned after cycle 10 on first processor")
	}

	// Reset the store for the restart simulation: clear status updates,
	// reset getConfirmedCalled, and add a new confirmed block with wrong hash.
	store.mu.Lock()
	store.statusUpdates = nil
	store.getConfirmedCalled = false
	store.mu.Unlock()

	block900 := makeConfirmedBlock("BTC", 900)
	store.addConfirmedBlock(block900)
	rpc.setBlockHash(900, "eeee"+block900.Hash[4:])

	// Simulate restart: create a NEW processor (cycleCount = 0 automatically).
	proc2 := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Verify fresh processor starts at cycleCount 0.
	if proc2.cycleCount != 0 {
		t.Fatalf("V9: new processor should start with cycleCount=0, got %d", proc2.cycleCount)
	}

	// Run ONE processCycle on the new processor.
	proc2.processCycle(ctx)

	// Block 900 should NOT be orphaned (cycleCount=1 after restart, 1%10!=0).
	if store.hasStatusUpdateFor(900, StatusOrphaned) {
		t.Errorf("V9: block at height 900 should NOT be orphaned after first cycle on restarted processor "+
			"(cycleCount=%d)", proc2.cycleCount)
	} else {
		t.Logf("V9: block 900 correctly NOT orphaned after 1 cycle on restarted processor (cycleCount=%d)",
			proc2.cycleCount)
	}

	// Run 9 more processCycles (total 10 on new processor).
	for i := 0; i < DeepReorgCheckInterval-1; i++ {
		proc2.processCycle(ctx)
	}

	// Now cycleCount should be 10, and deep reorg check should have run.
	if proc2.cycleCount != DeepReorgCheckInterval {
		t.Fatalf("V9: expected cycleCount=%d after %d total cycles on restarted processor, got %d",
			DeepReorgCheckInterval, DeepReorgCheckInterval, proc2.cycleCount)
	}

	// Block 900 should NOW be orphaned.
	if !store.hasStatusUpdateFor(900, StatusOrphaned) {
		t.Errorf("V9: block at height 900 should be orphaned after cycle 10 on restarted processor, "+
			"but no orphan status update was recorded")
	} else {
		t.Logf("V9: restart reset cycleCount to 0 — deep reorg detection delayed by %d cycles, "+
			"block 900 orphaned at cycle %d", DeepReorgCheckInterval, proc2.cycleCount)
	}
}

// TestSOLO_Vector_V9_PendingBlocksProcessedImmediately verifies that pending
// block processing (updateBlockConfirmations) works immediately on restart,
// even though deep reorg detection is delayed.
//
// V9: Pending block processing works immediately on restart — only deep reorg
// detection is delayed.
func TestSOLO_Vector_V9_PendingBlocksProcessedImmediately(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Pending block at height 800 with CORRECT hash, at maturity.
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)
	rpc.setBlockHash(800, makeBlockHash("BTC", 800))

	// Chain at height 1000 (800 + 200 confirmations, well above maturity of 100).
	rpc.setChainTip(1000, makeChainTip(1000))

	// Fresh processor (simulating restart).
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run one processCycle.
	proc.processCycle(ctx)

	// Even though deep reorg doesn't run on cycle 1, updateBlockConfirmations
	// DOES run and should process the pending block.
	updateCount := store.statusUpdateCount()
	if updateCount == 0 {
		t.Errorf("V9: pending block at height 800 should have received a status update "+
			"on first cycle after restart, but statusUpdateCount=%d", updateCount)
	} else {
		t.Logf("V9: pending block correctly processed on first cycle after restart "+
			"(statusUpdateCount=%d)", updateCount)
	}

	// Verify it got at least a progress update for pending status.
	hasPendingUpdate := store.hasStatusUpdateFor(800, StatusPending)
	hasConfirmedUpdate := store.hasStatusUpdateFor(800, StatusConfirmed)

	if !hasPendingUpdate && !hasConfirmedUpdate {
		t.Errorf("V9: block at height 800 should have a pending or confirmed status update, "+
			"but neither was recorded")
	} else {
		t.Logf("V9: block at height 800 received status update on restart "+
			"(pending=%v, confirmed=%v) — updateBlockConfirmations runs immediately",
			hasPendingUpdate, hasConfirmedUpdate)
	}
}
