// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Advanced RPC failure pattern tests for SOLO mode payment processor.
//
// These tests exercise the errInjectDaemonRPC mock from solo_mocks_test.go,
// covering countdown failures, custom per-call functions, TOCTOU simulation
// via changeTipAfterCalls, and combined DB+RPC failure scenarios. These
// patterns are NOT covered by solo_rpc_failure_test.go (which uses mockDaemonRPC).
package payments

import (
	"context"
	"fmt"
	"testing"

	"github.com/spiralpool/stratum/internal/daemon"
)

// ---------------------------------------------------------------------------
// Test 1: Countdown BlockchainInfo failures recover after N calls
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_CountdownBlockchainInfo_RecoverAfterN sets
// failBlockchainInfoN=3 so the first 3 calls to GetBlockchainInfo return an
// error. After the countdown expires, subsequent calls succeed. A pending
// block at maturity with a matching hash should eventually confirm once the
// StabilityWindowChecks stability cycles complete.
func TestSOLO_RPCAdvanced_CountdownBlockchainInfo_RecoverAfterN(t *testing.T) {
	t.Parallel()

	coin := "BTC"
	blockHeight := uint64(100000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newErrInjectBlockStore()
	block := makePendingBlock(coin, blockHeight)
	store.addPendingBlock(block)

	rpc := newErrInjectDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, makeBlockHash(coin, blockHeight))
	rpc.failBlockchainInfoN = 3

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// First 3 calls: GetBlockchainInfo fails on the snapshot call, so
	// updateBlockConfirmations returns an error each time.
	for i := 0; i < 3; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err == nil {
			t.Fatalf("cycle %d: expected error from countdown GetBlockchainInfo failure, got nil", i)
		}
	}

	// Block must still be pending -- no status updates during failures.
	if store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should not be confirmed during RPC failures")
	}
	if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
		t.Fatal("block should not be orphaned during RPC failures")
	}

	// Now run StabilityWindowChecks successful cycles.
	for i := 0; i < StabilityWindowChecks; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("recovery cycle %d: unexpected error: %v", i, err)
		}
	}

	// Block should now be confirmed.
	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should be confirmed after countdown recovery + stability window")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Countdown BlockHash failures recover after N calls
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_CountdownBlockHash_RecoverAfterN sets failBlockHashN=2
// so the first 2 GetBlockHash calls fail (block skipped each time). After
// the countdown expires, hash matches succeed and the block eventually confirms
// through the stability window.
func TestSOLO_RPCAdvanced_CountdownBlockHash_RecoverAfterN(t *testing.T) {
	t.Parallel()

	coin := "LTC"
	blockHeight := uint64(200000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newErrInjectBlockStore()
	block := makePendingBlock(coin, blockHeight)
	store.addPendingBlock(block)

	rpc := newErrInjectDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, makeBlockHash(coin, blockHeight))
	rpc.failBlockHashN = 2

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Each updateBlockConfirmations call with 1 pending block makes:
	//   1 GetBlockchainInfo (snapshot) + 1 GetBlockchainInfo (TOCTOU) + 1 GetBlockHash
	// failBlockHashN=2 means the first 2 GetBlockHash calls fail.
	// The block is skipped (stays pending) on those cycles.
	for i := 0; i < 2; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// Block must not be confirmed or orphaned yet.
	if store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should not be confirmed while GetBlockHash is failing")
	}
	if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
		t.Fatal("block should not be orphaned due to GetBlockHash failure")
	}

	// Now run StabilityWindowChecks cycles -- GetBlockHash works now.
	for i := 0; i < StabilityWindowChecks; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("recovery cycle %d: unexpected error: %v", i, err)
		}
	}

	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should be confirmed after GetBlockHash countdown recovery + stability window")
	}
}

// ---------------------------------------------------------------------------
// Test 3: Custom blockchainInfoFunc returns inconsistent height (TOCTOU)
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_CustomFunc_InconsistentHeight uses blockchainInfoFunc
// to return height X on the snapshot call (call 1) and height X+1 with a
// different tip on the TOCTOU check (call 2). This triggers a TOCTOU abort
// and no blocks should be processed.
func TestSOLO_RPCAdvanced_CustomFunc_InconsistentHeight(t *testing.T) {
	t.Parallel()

	coin := "DGB"
	blockHeight := uint64(300000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	snapshotTip := fmt.Sprintf("%064d", chainHeight)
	differentTip := fmt.Sprintf("%064d", chainHeight+1)

	store := newErrInjectBlockStore()
	store.addPendingBlock(makePendingBlock(coin, blockHeight))

	rpc := newErrInjectDaemonRPC()
	rpc.setBlockHash(blockHeight, makeBlockHash(coin, blockHeight))

	// Custom function: call 1 (snapshot) returns height X, call 2 (TOCTOU)
	// returns same height but different tip (reorg).
	rpc.blockchainInfoFunc = func(_ context.Context, callIndex int64) (*daemon.BlockchainInfo, error) {
		if callIndex == 1 {
			return &daemon.BlockchainInfo{
				BestBlockHash: snapshotTip,
				Blocks:        chainHeight,
			}, nil
		}
		// All subsequent calls return same height but different tip.
		return &daemon.BlockchainInfo{
			BestBlockHash: differentTip,
			Blocks:        chainHeight,
		}, nil
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	// TOCTOU abort returns nil (not an error).
	if err != nil {
		t.Fatalf("expected nil on TOCTOU abort, got: %v", err)
	}

	// No blocks should have been processed.
	if store.statusUpdateCount() > 0 {
		t.Errorf("expected zero status updates on TOCTOU abort, got %d", store.statusUpdateCount())
	}
	if store.orphanUpdateCount() > 0 {
		t.Errorf("expected zero orphan updates on TOCTOU abort, got %d", store.orphanUpdateCount())
	}
}

// ---------------------------------------------------------------------------
// Test 4: Custom blockHashFunc flips hash per call
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_CustomFunc_HashFlipsPerCall uses blockHashFunc to
// return the CORRECT hash on odd GetBlockHash calls and a WRONG hash on even
// calls. Over 6 cycles, the block accumulates a mismatch on even calls but
// resets to 0 on the next odd call (hash match). The block should never reach
// OrphanMismatchThreshold and must NOT be orphaned.
func TestSOLO_RPCAdvanced_CustomFunc_HashFlipsPerCall(t *testing.T) {
	t.Parallel()

	coin := "BCH"
	blockHeight := uint64(400000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)
	correctHash := makeBlockHash(coin, blockHeight)
	wrongHash := fmt.Sprintf("%064d", 9999999)

	store := newErrInjectBlockStore()
	store.addPendingBlock(makePendingBlock(coin, blockHeight))

	rpc := newErrInjectDaemonRPC()
	rpc.setChainTip(chainHeight, tip)

	// Custom: odd GetBlockHash calls return correct hash, even return wrong.
	rpc.blockHashFunc = func(_ context.Context, _ uint64, callIndex int64) (string, error) {
		if callIndex%2 == 1 {
			return correctHash, nil
		}
		return wrongHash, nil
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run 6 cycles. Pattern of GetBlockHash call indices: 1(correct), 2(wrong),
	// 3(correct), 4(wrong), 5(correct), 6(wrong).
	// On correct-hash cycles: mismatch resets to 0.
	// On wrong-hash cycles: mismatch increments to 1.
	// Never reaches threshold (3).
	for i := 0; i < 6; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// Block must NOT be orphaned.
	if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
		t.Fatal("block should NOT be orphaned -- hash flips prevent reaching mismatch threshold")
	}
}

// ---------------------------------------------------------------------------
// Test 5: changeTipAfterCalls triggers TOCTOU abort
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_TOCTOU_ChangeTipAfterN uses changeTipAfterCalls=1 so
// the snapshot (call 1) sees the original tip, but the TOCTOU check (call 2)
// sees a new tip. With 3 pending blocks, the cycle should abort immediately
// on the first block's TOCTOU check with zero status updates.
func TestSOLO_RPCAdvanced_TOCTOU_ChangeTipAfterN(t *testing.T) {
	t.Parallel()

	blockHeights := []uint64{500000, 500001, 500002}
	chainHeight := uint64(500200)
	originalTip := fmt.Sprintf("%064d", chainHeight)
	changedTip := fmt.Sprintf("%064d", chainHeight+1)

	store := newErrInjectBlockStore()
	for _, h := range blockHeights {
		store.addPendingBlock(makePendingBlock("BTC", h))
	}

	rpc := newErrInjectDaemonRPC()
	rpc.setChainTip(chainHeight, originalTip)
	rpc.changeTipAfterCalls = 1
	rpc.newTip = changedTip
	rpc.newHeight = chainHeight

	for _, h := range blockHeights {
		rpc.setBlockHash(h, makeBlockHash("BTC", h))
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("expected nil on TOCTOU abort, got: %v", err)
	}

	// Zero status updates -- cycle aborted before processing any block.
	if store.statusUpdateCount() != 0 {
		t.Errorf("expected zero status updates, got %d", store.statusUpdateCount())
	}
	if store.orphanUpdateCount() != 0 {
		t.Errorf("expected zero orphan updates, got %d", store.orphanUpdateCount())
	}
}

// ---------------------------------------------------------------------------
// Test 6: All coins with countdown recovery
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_AllCoins_CountdownRecovery verifies that for every
// coin in allTestCoins, a failBlockchainInfoN=2 countdown causes the first
// 2 cycles to fail, after which the block eventually confirms through the
// stability window.
func TestSOLO_RPCAdvanced_AllCoins_CountdownRecovery(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(600000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
			tip := fmt.Sprintf("%064d", chainHeight)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))
			rpc.failBlockchainInfoN = 2

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// First 2 cycles fail (GetBlockchainInfo countdown).
			for i := 0; i < 2; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err == nil {
					t.Fatalf("[%s] cycle %d: expected error from countdown failure, got nil", coin.Symbol, i)
				}
			}

			// Next StabilityWindowChecks cycles should succeed and confirm.
			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] recovery cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block at height %d should be confirmed after countdown recovery",
					coin.Symbol, blockHeight)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 7: Combined DB stability error + RPC countdown failure
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_CombinedDBAndRPCFailure sets both
// store.errUpdateStability and rpc.failBlockHashN=1. First cycle: GetBlockHash
// fails so the block is skipped. Subsequent cycles: hash matches and the block
// is at maturity, but stability DB writes fail. The block should STILL confirm
// because stability tracking is in-memory on the Block struct and the
// UpdateBlockStabilityCount error is logged but not fatal.
func TestSOLO_RPCAdvanced_CombinedDBAndRPCFailure(t *testing.T) {
	t.Parallel()

	coin := "DOGE"
	blockHeight := uint64(700000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newErrInjectBlockStore()
	store.addPendingBlock(makePendingBlock(coin, blockHeight))
	store.errUpdateStability = fmt.Errorf("injected stability write failure")

	rpc := newErrInjectDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, makeBlockHash(coin, blockHeight))
	rpc.failBlockHashN = 1

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Cycle 1: GetBlockHash fails -> block skipped.
	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("cycle 1: unexpected error: %v", err)
	}
	if store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should not be confirmed on first cycle (GetBlockHash failed)")
	}

	// Cycles 2 through 1+StabilityWindowChecks: GetBlockHash succeeds, hash
	// matches, block at maturity, stability DB write fails but in-memory
	// tracking still works.
	for i := 0; i < StabilityWindowChecks; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i+2, err)
		}
	}

	// Block should be confirmed -- in-memory stability tracking succeeded
	// despite DB write failures.
	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should be confirmed despite stability DB write failures (in-memory tracking)")
	}
}

// ---------------------------------------------------------------------------
// Test 8: blockchainInfoFunc returns error -> error propagated
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_BlockchainInfoFunc_ReturnsNil sets blockchainInfoFunc
// to return (nil, error). The snapshot GetBlockchainInfo call should fail
// and updateBlockConfirmations should return an error. No blocks processed.
func TestSOLO_RPCAdvanced_BlockchainInfoFunc_ReturnsNil(t *testing.T) {
	t.Parallel()

	coin := "NMC"
	blockHeight := uint64(800000)

	store := newErrInjectBlockStore()
	store.addPendingBlock(makePendingBlock(coin, blockHeight))

	rpc := newErrInjectDaemonRPC()
	rpc.blockchainInfoFunc = func(_ context.Context, _ int64) (*daemon.BlockchainInfo, error) {
		return nil, fmt.Errorf("injected blockchainInfoFunc error")
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err == nil {
		t.Fatal("expected error from blockchainInfoFunc returning nil, got nil")
	}

	// No blocks should have been processed.
	if store.statusUpdateCount() > 0 {
		t.Errorf("expected zero status updates, got %d", store.statusUpdateCount())
	}
	if store.orphanUpdateCount() > 0 {
		t.Errorf("expected zero orphan updates, got %d", store.orphanUpdateCount())
	}
}

// ---------------------------------------------------------------------------
// Test 9: Merge mining pairs with countdown recovery
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_MergePairs_CountdownRecovery tests that for each
// merge mining parent+aux pair, both parent and aux processors with
// failBlockchainInfoN=1 have their blocks eventually confirmed independently
// after the countdown expires.
func TestSOLO_RPCAdvanced_MergePairs_CountdownRecovery(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s+%s", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			parentHeight := uint64(900000)
			auxHeight := uint64(900100)
			parentChainHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations)
			auxChainHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations)
			parentTip := fmt.Sprintf("%064d", parentChainHeight)
			auxTip := fmt.Sprintf("%064d", auxChainHeight)

			// Parent processor.
			parentStore := newErrInjectBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))

			parentRPC := newErrInjectDaemonRPC()
			parentRPC.setChainTip(parentChainHeight, parentTip)
			parentRPC.setBlockHash(parentHeight, makeBlockHash(pair.Parent.Symbol, parentHeight))
			parentRPC.failBlockchainInfoN = 1

			parentProc := newParanoidTestProcessor(parentStore, parentRPC, DefaultBlockMaturityConfirmations)

			// Aux processor.
			auxStore := newErrInjectBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))

			auxRPC := newErrInjectDaemonRPC()
			auxRPC.setChainTip(auxChainHeight, auxTip)
			auxRPC.setBlockHash(auxHeight, makeBlockHash(pair.Aux.Symbol, auxHeight))
			auxRPC.failBlockchainInfoN = 1

			auxProc := newParanoidTestProcessor(auxStore, auxRPC, DefaultBlockMaturityConfirmations)

			// Cycle 1: both fail (countdown).
			parentErr := parentProc.updateBlockConfirmations(context.Background())
			if parentErr == nil {
				t.Fatalf("[%s] expected error from parent countdown, got nil", pair.Parent.Symbol)
			}
			auxErr := auxProc.updateBlockConfirmations(context.Background())
			if auxErr == nil {
				t.Fatalf("[%s] expected error from aux countdown, got nil", pair.Aux.Symbol)
			}

			// Run StabilityWindowChecks successful cycles for both.
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := parentProc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] parent recovery cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] aux recovery cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			if !parentStore.hasStatusUpdateFor(parentHeight, StatusConfirmed) {
				t.Fatalf("[%s] parent block should be confirmed after countdown recovery",
					pair.Parent.Symbol)
			}
			if !auxStore.hasStatusUpdateFor(auxHeight, StatusConfirmed) {
				t.Fatalf("[%s] aux block should be confirmed after countdown recovery",
					pair.Aux.Symbol)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 10: High call count with no overflow
// ---------------------------------------------------------------------------

// TestSOLO_RPCAdvanced_HighCallCount_NoOverflow runs updateBlockConfirmations
// 1000 times with a pending block at maturity whose hash matches. The block
// should confirm after StabilityWindowChecks cycles and remain confirmed for
// the remaining cycles. Verifies getBlockchainInfoCalls and getBlockHashCalls
// atomic counters show expected values without overflow.
func TestSOLO_RPCAdvanced_HighCallCount_NoOverflow(t *testing.T) {
	t.Parallel()

	coin := "SYS"
	blockHeight := uint64(1000000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := fmt.Sprintf("%064d", chainHeight)

	store := newErrInjectBlockStore()
	store.addPendingBlock(makePendingBlock(coin, blockHeight))

	rpc := newErrInjectDaemonRPC()
	rpc.setChainTip(chainHeight, tip)
	rpc.setBlockHash(blockHeight, makeBlockHash(coin, blockHeight))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	totalCycles := 1000
	for i := 0; i < totalCycles; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	// Block should be confirmed after StabilityWindowChecks cycles.
	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Fatal("block should be confirmed after stability window")
	}

	// Verify call counts.
	// Each cycle with a pending block: 1 snapshot + 1 TOCTOU = 2 GetBlockchainInfo calls.
	// After confirmation, the block is no longer pending, so subsequent cycles
	// see 0 pending blocks and make only 0 GetBlockchainInfo calls (no snapshot
	// needed when there are no pending blocks -- the function returns early).
	//
	// Cycles 1..StabilityWindowChecks: block is pending -> 2 calls each.
	// Cycles StabilityWindowChecks+1..1000: block is confirmed -> 0 pending -> 0 calls.
	//
	// Wait -- actually, after confirmation, the block's Status is set to
	// "confirmed" in the store, so GetPendingBlocks returns nothing. Then
	// updateBlockConfirmations returns early before even calling GetBlockchainInfo.
	//
	// So: StabilityWindowChecks * 2 = expected GetBlockchainInfo calls.
	// And: StabilityWindowChecks * 1 = expected GetBlockHash calls.
	expectedInfoCalls := int64(StabilityWindowChecks * 2)
	expectedHashCalls := int64(StabilityWindowChecks)

	actualInfoCalls := rpc.getBlockchainInfoCalls.Load()
	actualHashCalls := rpc.getBlockHashCalls.Load()

	if actualInfoCalls != expectedInfoCalls {
		t.Errorf("expected %d GetBlockchainInfo calls, got %d", expectedInfoCalls, actualInfoCalls)
	}
	if actualHashCalls != expectedHashCalls {
		t.Errorf("expected %d GetBlockHash calls, got %d", expectedHashCalls, actualHashCalls)
	}

	// Verify no overflow: counters must be positive and reasonable.
	if actualInfoCalls < 0 {
		t.Error("getBlockchainInfoCalls overflowed to negative")
	}
	if actualHashCalls < 0 {
		t.Error("getBlockHashCalls overflowed to negative")
	}
}
