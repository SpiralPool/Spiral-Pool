// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Vector V1: Height-collision tests for SOLO mode payment processor.
//
// These tests target the block-loss vector identified in audit: UpdateBlockStatus,
// UpdateBlockOrphanCount, and UpdateBlockStabilityCount use WHERE blockheight=$N
// without hash qualification. When two blocks exist at the same height with
// different hashes (e.g., after a fork), all height-based updates affect BOTH
// blocks indiscriminately, causing false confirmations, orphan count cross-
// contamination, threshold interference, and stability count leakage.
//
// Each test documents the bug by asserting the expected-correct behavior and
// logging the actual (buggy) behavior when the assertion fails.
package payments

import (
	"context"
	"fmt"
	"testing"
)

// TestSOLO_Vector_V1_HeightCollision_FalseConfirmation verifies that when two
// pending blocks exist at the same height with different hashes, confirming
// the valid block does NOT also confirm the invalid block.
//
// V1 BUG: UpdateBlockStatus(height, "confirmed", ...) sets ALL blocks at that
// height to "confirmed", including the one whose hash does NOT match the chain.
func TestSOLO_Vector_V1_HeightCollision_FalseConfirmation(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Two pending blocks at height 800, different hashes.
	// blockA has the WRONG hash (not on chain); blockB has the CORRECT hash.
	blockA := makePendingBlock("BTC", 800)
	blockA.Hash = fmt.Sprintf("aaaa%060d", 800) // 64-char hash, different from chain
	blockB := makePendingBlock("BTC", 800)       // hash = makeBlockHash("BTC", 800)

	store.addPendingBlock(blockA)
	store.addPendingBlock(blockB)

	// Chain reports blockB's hash at height 800 (blockA is orphaned on chain).
	rpc.setBlockHash(800, blockB.Hash)

	// Chain at height 1000 with a stable tip.
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run StabilityWindowChecks cycles so blockB accumulates enough stability.
	for i := 0; i < StabilityWindowChecks; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	// Read final statuses under lock.
	store.mu.Lock()
	statusA := blockA.Status
	statusB := blockB.Status
	store.mu.Unlock()

	// blockB SHOULD be confirmed (correct hash, at maturity, stable).
	if statusB != StatusConfirmed {
		t.Errorf("blockB (correct hash) should be confirmed, got %q", statusB)
	}

	// blockA should NOT be confirmed — its hash doesn't match the chain.
	if statusA == StatusConfirmed {
		t.Errorf("V1 BUG CONFIRMED: block with wrong hash falsely confirmed by height-based UPDATE "+
			"(blockA.Status=%q, want %q or %q)", statusA, StatusPending, StatusOrphaned)
	}
}

// TestSOLO_Vector_V1_HeightCollision_OrphanCountCrossContamination verifies
// that incrementing the orphan mismatch counter for one block does NOT affect
// another block at the same height.
//
// V1 BUG: UpdateBlockOrphanCount(800, 1) updates ALL blocks at height 800,
// so blockB (correct hash) receives blockA's orphan mismatch count.
func TestSOLO_Vector_V1_HeightCollision_OrphanCountCrossContamination(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// blockA: wrong hash (mismatch); blockB: correct hash.
	blockA := makePendingBlock("BTC", 800)
	blockA.Hash = fmt.Sprintf("aaaa%060d", 800)
	blockB := makePendingBlock("BTC", 800)

	store.addPendingBlock(blockA)
	store.addPendingBlock(blockB)

	rpc.setBlockHash(800, blockB.Hash)
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run ONE cycle so blockA gets a mismatch increment.
	_ = proc.updateBlockConfirmations(ctx)

	store.mu.Lock()
	orphanCountB := blockB.OrphanMismatchCount
	store.mu.Unlock()

	// blockB has the correct hash — its orphan count should remain 0.
	if orphanCountB != 0 {
		t.Errorf("V1 BUG: blockB orphan count contaminated by blockA's mismatch (got %d, want 0)",
			orphanCountB)
	}
}

// TestSOLO_Vector_V1_HeightCollision_OrphanNeverReachesThreshold verifies that
// the orphan mismatch counter for blockA (wrong hash) can reach the threshold
// even when blockB (correct hash) exists at the same height.
//
// V1 BUG: When blockB is processed, UpdateBlockOrphanCount(800, 0) resets ALL
// blocks at height 800, including blockA. This means blockA's mismatch counter
// is reset every cycle by blockB's "hash matches" path, and blockA is never
// orphaned despite being mismatched every single cycle.
func TestSOLO_Vector_V1_HeightCollision_OrphanNeverReachesThreshold(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockA := makePendingBlock("BTC", 800)
	blockA.Hash = fmt.Sprintf("aaaa%060d", 800)
	blockB := makePendingBlock("BTC", 800)

	store.addPendingBlock(blockA)
	store.addPendingBlock(blockB)

	rpc.setBlockHash(800, blockB.Hash)
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run OrphanMismatchThreshold * 2 cycles (well over the threshold).
	totalCycles := OrphanMismatchThreshold * 2
	for i := 0; i < totalCycles; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	// Check if blockA was ever marked orphaned.
	wasOrphaned := store.hasStatusUpdateFor(800, StatusOrphaned)

	store.mu.Lock()
	statusA := blockA.Status
	store.mu.Unlock()

	if !wasOrphaned && statusA != StatusOrphaned {
		t.Errorf("V1 BUG: orphan counter for blockA reset every cycle by blockB processing at same height "+
			"(ran %d cycles, blockA.Status=%q, expected %q after %d mismatches)",
			totalCycles, statusA, StatusOrphaned, OrphanMismatchThreshold)
	}
}

// TestSOLO_Vector_V1_HeightCollision_StabilityCrossContamination verifies that
// stability counter increments for one block do NOT affect another block at the
// same height.
//
// V1 BUG: UpdateBlockStabilityCount(800, N, tip) updates ALL blocks at height
// 800, so blockA (wrong hash, should have stability 0) inherits blockB's
// stability count.
func TestSOLO_Vector_V1_HeightCollision_StabilityCrossContamination(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockA := makePendingBlock("BTC", 800)
	blockA.Hash = fmt.Sprintf("aaaa%060d", 800)
	blockB := makePendingBlock("BTC", 800)

	store.addPendingBlock(blockA)
	store.addPendingBlock(blockB)

	rpc.setBlockHash(800, blockB.Hash)
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run StabilityWindowChecks cycles so blockB accumulates full stability.
	for i := 0; i < StabilityWindowChecks; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	store.mu.Lock()
	stabilityA := blockA.StabilityCheckCount
	store.mu.Unlock()

	// blockA's hash doesn't match — its stability count should remain 0.
	if stabilityA != 0 {
		t.Errorf("V1 BUG: blockA stability count contaminated by blockB's stability "+
			"(got %d, want 0; UpdateBlockStabilityCount uses height without hash qualifier)",
			stabilityA)
	}
}
