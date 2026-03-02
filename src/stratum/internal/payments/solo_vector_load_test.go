// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Vector V7: Load tests for SOLO mode payment processor.
//
// These tests target the block-loss vector identified in audit: GetPendingBlocks
// has no LIMIT clause, so processing N blocks requires O(3N+1) RPC calls per
// cycle — 1 initial GetBlockchainInfo snapshot, plus 1 TOCTOU GetBlockchainInfo
// and 1 GetBlockHash per block. Under sustained load (e.g., fast block-time
// coins like DGB accumulating blocks during downtime), this can cause RPC
// timeouts and liveness failures.
package payments

import (
	"context"
	"testing"
)

// TestSOLO_Vector_V7_1000PendingBlocks_RPCCallCount verifies that processing
// 1000 pending blocks in a single updateBlockConfirmations cycle produces
// exactly 2001 RPC calls: 1 snapshot GetBlockchainInfo + 1000 TOCTOU
// GetBlockchainInfo calls + 1000 GetBlockHash calls.
//
// V7: Processing 1000 pending blocks requires 2001 RPC calls per cycle. With
// 10-minute blocks at default interval, this is manageable. With 15-second
// blocks (DGB) accumulating blocks during downtime, this can cause timeouts.
func TestSOLO_Vector_V7_1000PendingBlocks_RPCCallCount(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	const blockCount = 1000

	// Add 1000 pending blocks at heights 1-1000 with matching hashes.
	for h := uint64(1); h <= blockCount; h++ {
		store.addPendingBlock(makePendingBlock("BTC", h))
		rpc.setBlockHash(h, makeBlockHash("BTC", h))
	}

	// Chain at height 2000 (well above maturity for all blocks).
	rpc.setChainTip(2000, makeChainTip(2000))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run ONE cycle of updateBlockConfirmations.
	if err := proc.updateBlockConfirmations(ctx); err != nil {
		t.Fatalf("updateBlockConfirmations failed: %v", err)
	}

	bcInfoCalls := rpc.getBlockchainInfoCalls.Load()
	hashCalls := rpc.getBlockHashCalls.Load()
	totalCalls := bcInfoCalls + hashCalls

	// Expected: 1 snapshot + 1000 TOCTOU = 1001 GetBlockchainInfo calls.
	expectedBCInfoCalls := int64(1 + blockCount)
	if bcInfoCalls != expectedBCInfoCalls {
		t.Errorf("V7: expected %d GetBlockchainInfo calls (1 snapshot + %d TOCTOU), got %d",
			expectedBCInfoCalls, blockCount, bcInfoCalls)
	}

	// Expected: 1000 GetBlockHash calls (one per block).
	expectedHashCalls := int64(blockCount)
	if hashCalls != expectedHashCalls {
		t.Errorf("V7: expected %d GetBlockHash calls (one per block), got %d",
			expectedHashCalls, hashCalls)
	}

	// Total: 2001 RPC calls for 1000 blocks.
	expectedTotal := int64(2001)
	if totalCalls != expectedTotal {
		t.Errorf("V7: expected %d total RPC calls, got %d", expectedTotal, totalCalls)
	}

	t.Logf("V7 LOAD: %d pending blocks required %d GetBlockchainInfo + %d GetBlockHash = %d total RPC calls",
		blockCount, bcInfoCalls, hashCalls, totalCalls)
}

// TestSOLO_Vector_V7_500PendingBlocks_AllProcessedSingleCycle verifies that
// the processor correctly handles bulk confirmation of 500 blocks across
// StabilityWindowChecks cycles.
//
// V7: Processor correctly handles bulk confirmation of 500 blocks.
func TestSOLO_Vector_V7_500PendingBlocks_AllProcessedSingleCycle(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	const blockCount = 500

	// Add 500 pending blocks at heights 1-500, all with matching hashes.
	for h := uint64(1); h <= blockCount; h++ {
		store.addPendingBlock(makePendingBlock("BTC", h))
		rpc.setBlockHash(h, makeBlockHash("BTC", h))
	}

	// Chain at height 1000 (well above maturity).
	rpc.setChainTip(1000, makeChainTip(1000))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run StabilityWindowChecks cycles so all blocks accumulate enough stability.
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("updateBlockConfirmations cycle %d failed: %v", i+1, err)
		}
	}

	// Assert: ALL 500 blocks are confirmed.
	notConfirmed := 0
	for h := uint64(1); h <= blockCount; h++ {
		if !store.hasStatusUpdateFor(h, StatusConfirmed) {
			notConfirmed++
			if notConfirmed <= 5 {
				t.Errorf("V7: block at height %d not confirmed after %d stability cycles",
					h, StabilityWindowChecks)
			}
		}
	}
	if notConfirmed > 5 {
		t.Errorf("V7: %d additional blocks not confirmed (total %d/%d unconfirmed)",
			notConfirmed-5, notConfirmed, blockCount)
	}
	if notConfirmed == 0 {
		t.Logf("V7 BULK: all %d blocks confirmed after %d stability cycles", blockCount, StabilityWindowChecks)
	}
}

// TestSOLO_Vector_V7_MixedLoad_MatchAndMismatch verifies that the processor
// correctly handles a mixed batch of 200 blocks where the first half have
// matching hashes and the second half have wrong hashes.
//
// V7: Processor correctly handles mixed batch of matching and mismatching blocks.
func TestSOLO_Vector_V7_MixedLoad_MatchAndMismatch(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	const matchCount = 100
	const mismatchCount = 100
	const totalBlocks = matchCount + mismatchCount

	// Heights 1-100: matching hashes.
	for h := uint64(1); h <= matchCount; h++ {
		store.addPendingBlock(makePendingBlock("BTC", h))
		rpc.setBlockHash(h, makeBlockHash("BTC", h))
	}

	// Heights 101-200: WRONG hashes (first 4 chars differ).
	for h := uint64(matchCount + 1); h <= totalBlocks; h++ {
		store.addPendingBlock(makePendingBlock("BTC", h))
		wrongHash := "ffff" + makeBlockHash("BTC", h)[4:]
		rpc.setBlockHash(h, wrongHash)
	}

	// Chain at height 1000.
	rpc.setChainTip(1000, makeChainTip(1000))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run max(StabilityWindowChecks, OrphanMismatchThreshold) = 3 cycles.
	cycles := StabilityWindowChecks
	if OrphanMismatchThreshold > cycles {
		cycles = OrphanMismatchThreshold
	}
	for i := 0; i < cycles; i++ {
		if err := proc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("updateBlockConfirmations cycle %d failed: %v", i+1, err)
		}
	}

	// Assert: heights 1-100 are confirmed.
	confirmedMissing := 0
	for h := uint64(1); h <= matchCount; h++ {
		if !store.hasStatusUpdateFor(h, StatusConfirmed) {
			confirmedMissing++
			if confirmedMissing <= 5 {
				t.Errorf("V7 MIXED: block at height %d (matching hash) not confirmed after %d cycles",
					h, cycles)
			}
		}
	}
	if confirmedMissing > 5 {
		t.Errorf("V7 MIXED: %d additional matching blocks not confirmed (total %d/%d)",
			confirmedMissing-5, confirmedMissing, matchCount)
	}

	// Assert: heights 101-200 are orphaned.
	orphanMissing := 0
	for h := uint64(matchCount + 1); h <= totalBlocks; h++ {
		if !store.hasStatusUpdateFor(h, StatusOrphaned) {
			orphanMissing++
			if orphanMissing <= 5 {
				t.Errorf("V7 MIXED: block at height %d (wrong hash) not orphaned after %d cycles",
					h, cycles)
			}
		}
	}
	if orphanMissing > 5 {
		t.Errorf("V7 MIXED: %d additional mismatched blocks not orphaned (total %d/%d)",
			orphanMissing-5, orphanMissing, mismatchCount)
	}

	if confirmedMissing == 0 && orphanMissing == 0 {
		t.Logf("V7 MIXED: correctly confirmed %d matching blocks and orphaned %d mismatched blocks in %d cycles",
			matchCount, mismatchCount, cycles)
	}
}
