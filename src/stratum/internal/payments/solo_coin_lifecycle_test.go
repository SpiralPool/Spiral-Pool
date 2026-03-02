// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Complete coin lifecycle tests for SOLO mode payment processor.
//
// Verifies every coin type through the full pending -> confirmed and
// pending -> orphaned lifecycle using the errInject mocks. Exercises
// confirmation with custom maturity, bulk block processing, deep reorg
// detection on parent coins, cross-chain isolation for merge mining
// pairs, and near-orphan recovery across all 13 supported coins.
package payments

import (
	"context"
	"fmt"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 1: All Coins — Pending to Confirmed
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each of the 13 coins: add a pending block at height 800000, set the
// chain tip at 800000 + maturity + 10, set a matching block hash, and run
// StabilityWindowChecks cycles. The block must transition to confirmed.

func TestSOLO_CoinLifecycle_AllCoins_PendingToConfirmed(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			tipHash := makeChainTip(chainHeight)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block at height %d NOT confirmed after %d stable cycles",
					coin.Symbol, blockHeight, StabilityWindowChecks)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 2: All Coins — Pending to Orphaned
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each coin: add a pending block, set chain tip above maturity, set a
// WRONG block hash, and run OrphanMismatchThreshold cycles. The block must
// transition to orphaned.

func TestSOLO_CoinLifecycle_AllCoins_PendingToOrphaned(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			tipHash := makeChainTip(chainHeight)
			wrongHash := fmt.Sprintf("%064x", blockHeight+99999)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, wrongHash)

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			for i := 0; i < OrphanMismatchThreshold; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] block at height %d NOT orphaned after %d mismatch cycles",
					coin.Symbol, blockHeight, OrphanMismatchThreshold)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 3: SHA-256d Coins — Confirm With Countdown RPC Failure
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each SHA-256d coin: set failBlockchainInfoN=1 so the first
// GetBlockchainInfo call fails, then add a pending block at maturity with
// a matching hash. Run 1 + StabilityWindowChecks cycles. The block must
// confirm despite the initial RPC failure.

func TestSOLO_CoinLifecycle_SHA256d_ConfirmWithCountdown(t *testing.T) {
	t.Parallel()

	for _, coin := range sha256dCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			tipHash := makeChainTip(chainHeight)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.failBlockchainInfoN = 1
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// First cycle: GetBlockchainInfo fails, updateBlockConfirmations
			// returns an error. Block stays pending.
			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s] expected error from initial RPC failure, got nil", coin.Symbol)
			}

			// Subsequent cycles: RPC succeeds, stability window accumulates.
			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block NOT confirmed after initial RPC failure + %d stable cycles",
					coin.Symbol, StabilityWindowChecks)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 4: Scrypt Coins — Confirm With Countdown RPC Failure
// ═══════════════════════════════════════════════════════════════════════════════
//
// Same as test 3 but for Scrypt coins.

func TestSOLO_CoinLifecycle_Scrypt_ConfirmWithCountdown(t *testing.T) {
	t.Parallel()

	for _, coin := range scryptCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			tipHash := makeChainTip(chainHeight)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.failBlockchainInfoN = 1
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// First cycle: GetBlockchainInfo fails.
			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s] expected error from initial RPC failure, got nil", coin.Symbol)
			}

			// Subsequent cycles: RPC succeeds, stability window accumulates.
			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block NOT confirmed after initial RPC failure + %d stable cycles",
					coin.Symbol, StabilityWindowChecks)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 5: Aux Coins — Independent Confirmation
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each aux coin: create an independent store+rpc, add a pending block
// at a unique aux chain height. Set the aux chain tip at auxHeight + maturity
// + 10. Verify each aux coin confirms independently with its own processor.

func TestSOLO_CoinLifecycle_AuxCoins_IndependentConfirmation(t *testing.T) {
	t.Parallel()

	for _, coin := range auxCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			auxHeight := uint64(900000)
			auxChainHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			auxTip := makeChainTip(auxChainHeight)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, auxHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(auxChainHeight, auxTip)
			rpc.setBlockHash(auxHeight, makeBlockHash(coin.Symbol, auxHeight))

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(auxHeight, StatusConfirmed) {
				t.Fatalf("[%s] aux coin block at height %d NOT confirmed independently "+
					"(parent chain: %s)", coin.Symbol, auxHeight, coin.ParentChain)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 6: Merge Pairs — Cross-Chain Isolation
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each merge pair: create TWO independent processors (parent + aux),
// each with their own store and rpc. Parent block at height 800000 on the
// parent chain, aux block at height 900000 on the aux chain. Confirm the
// parent and orphan the aux. Verify they do not affect each other.

func TestSOLO_CoinLifecycle_MergePairs_CrossChainIsolation(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		name := fmt.Sprintf("%s_parent_%s_aux", pair.Parent.Symbol, pair.Aux.Symbol)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Parent chain: confirm block at height 800000
			parentHeight := uint64(800000)
			parentChainHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			parentTip := makeChainTip(parentChainHeight)

			parentStore := newErrInjectBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))

			parentRPC := newErrInjectDaemonRPC()
			parentRPC.setChainTip(parentChainHeight, parentTip)
			parentRPC.setBlockHash(parentHeight, makeBlockHash(pair.Parent.Symbol, parentHeight))

			parentProc := newParanoidTestProcessor(parentStore, parentRPC, DefaultBlockMaturityConfirmations)

			// Aux chain: orphan block at height 900000
			auxHeight := uint64(900000)
			auxChainHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			auxTip := makeChainTip(auxChainHeight)
			auxWrongHash := fmt.Sprintf("%064x", auxHeight+77777)

			auxStore := newErrInjectBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))

			auxRPC := newErrInjectDaemonRPC()
			auxRPC.setChainTip(auxChainHeight, auxTip)
			auxRPC.setBlockHash(auxHeight, auxWrongHash)

			auxProc := newParanoidTestProcessor(auxStore, auxRPC, DefaultBlockMaturityConfirmations)

			// Run parent through stability window to confirm
			for i := 0; i < StabilityWindowChecks; i++ {
				err := parentProc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
			}

			// Run aux through orphan threshold to orphan
			for i := 0; i < OrphanMismatchThreshold; i++ {
				err := auxProc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Verify parent confirmed
			if !parentStore.hasStatusUpdateFor(parentHeight, StatusConfirmed) {
				t.Fatalf("[%s] parent block NOT confirmed", pair.Parent.Symbol)
			}

			// Verify aux orphaned
			if !auxStore.hasStatusUpdateFor(auxHeight, StatusOrphaned) {
				t.Fatalf("[%s] aux block NOT orphaned", pair.Aux.Symbol)
			}

			// Verify isolation: parent store has no orphan, aux store has no confirm
			if parentStore.hasStatusUpdateFor(parentHeight, StatusOrphaned) {
				t.Fatalf("[%s] parent block incorrectly orphaned", pair.Parent.Symbol)
			}
			if auxStore.hasStatusUpdateFor(auxHeight, StatusConfirmed) {
				t.Fatalf("[%s] aux block incorrectly confirmed", pair.Aux.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 7: All Coins — Custom Maturity 50
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each coin: create a processor with maturity=50 (not default 100). Add
// a pending block at height H=800000, set chain at H+50+10. Run stability
// cycles. Verify confirmed at the custom maturity.

func TestSOLO_CoinLifecycle_AllCoins_CustomMaturity50(t *testing.T) {
	t.Parallel()

	customMaturity := 50

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + uint64(customMaturity) + 10
			tipHash := makeChainTip(chainHeight)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

			proc := newParanoidTestProcessor(store, rpc, customMaturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block NOT confirmed with custom maturity %d after %d stable cycles",
					coin.Symbol, customMaturity, StabilityWindowChecks)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 8: Fast Coins — 5 Blocks Bulk Confirmation
// ═══════════════════════════════════════════════════════════════════════════════
//
// For coins with BlockTimeSec <= 30 (DGB, DGB-SCRYPT, FBTC): add 5
// pending blocks at consecutive heights. Set chain tip well above all blocks
// + maturity. Set matching hashes for all. Run stability cycles. Verify ALL
// 5 blocks confirmed.

func TestSOLO_CoinLifecycle_FastCoins_5BlocksBulk(t *testing.T) {
	t.Parallel()

	var fastCoins []testCoinConfig
	for _, c := range allTestCoins {
		if c.BlockTimeSec <= 30 {
			fastCoins = append(fastCoins, c)
		}
	}

	if len(fastCoins) == 0 {
		t.Fatal("expected at least one fast-block coin in allTestCoins")
	}

	for _, coin := range fastCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			baseHeight := uint64(800000)
			blockCount := 5
			// Chain tip well above the highest block + maturity
			chainHeight := baseHeight + uint64(blockCount) + uint64(DefaultBlockMaturityConfirmations) + 50
			tipHash := makeChainTip(chainHeight)

			store := newErrInjectBlockStore()
			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tipHash)

			for j := 0; j < blockCount; j++ {
				h := baseHeight + uint64(j)
				store.addPendingBlock(makePendingBlock(coin.Symbol, h))
				rpc.setBlockHash(h, makeBlockHash(coin.Symbol, h))
			}

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			for j := 0; j < blockCount; j++ {
				h := baseHeight + uint64(j)
				if !store.hasStatusUpdateFor(h, StatusConfirmed) {
					t.Fatalf("[%s] block at height %d NOT confirmed in bulk "+
						"(expected all %d blocks confirmed)", coin.Symbol, h, blockCount)
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 9: Parent Coins — Deep Reorg Detection
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each parent coin (BTC, LTC): add a confirmed block. Set chain tip at
// block + 500 (within DeepReorgMaxAge). Set a WRONG hash. Call
// verifyConfirmedBlocks. Verify the block is orphaned by deep reorg.

func TestSOLO_CoinLifecycle_ParentCoins_DeepReorgDetection(t *testing.T) {
	t.Parallel()

	for _, coin := range parentCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + 500
			tipHash := makeChainTip(chainHeight)
			wrongHash := fmt.Sprintf("%064x", blockHeight+88888)

			store := newErrInjectBlockStore()
			store.addConfirmedBlock(makeConfirmedBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, wrongHash)

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			err := proc.verifyConfirmedBlocks(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			if !store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] confirmed parent block NOT orphaned by deep reorg "+
					"(hash mismatch at height %d)", coin.Symbol, blockHeight)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 10: All Coins — Recover From Near Orphan
// ═══════════════════════════════════════════════════════════════════════════════
//
// For each coin: add a pending block at maturity. First: set a wrong hash and
// run OrphanMismatchThreshold-1 cycles (near orphan). Then: fix the hash and
// run StabilityWindowChecks cycles. Verify the block confirms (the mismatch
// counter resets when the hash matches again).

func TestSOLO_CoinLifecycle_AllCoins_RecoverFromNearOrphan(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			tipHash := makeChainTip(chainHeight)
			wrongHash := fmt.Sprintf("%064x", blockHeight+55555)

			store := newErrInjectBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newErrInjectDaemonRPC()
			rpc.setChainTip(chainHeight, tipHash)
			rpc.setBlockHash(blockHeight, wrongHash)

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Phase 1: Run OrphanMismatchThreshold-1 cycles with wrong hash
			// (near orphan, but not quite at the threshold)
			for i := 0; i < OrphanMismatchThreshold-1; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] near-orphan cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify NOT orphaned yet
			if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] block orphaned prematurely during near-orphan phase "+
					"(%d cycles, threshold is %d)",
					coin.Symbol, OrphanMismatchThreshold-1, OrphanMismatchThreshold)
			}

			// Phase 2: Fix the hash, run StabilityWindowChecks cycles
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] recovery cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify block is confirmed after recovery
			if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block NOT confirmed after near-orphan recovery "+
					"(mismatch counter should have reset on hash match)",
					coin.Symbol)
			}

			// Verify it was never orphaned
			if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] block was orphaned despite recovering from near-orphan",
					coin.Symbol)
			}
		})
	}
}
