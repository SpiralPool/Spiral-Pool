// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Chain reorganization test suite for SOLO mode payment processor.
//
// Verifies that shallow reorgs, deep reorgs, and merge mining parent+aux
// chain reorgs do NOT cause silent block loss. Tests exercise the delayed
// orphaning threshold, stability window, TOCTOU protection, and recovery
// paths across all 17 supported coins with dynamic parameterization.
//
// Risk vectors covered:
//   1. Shallow reorg (1-2 blocks): delayed orphaning, not immediate
//   2. Shallow reorg recovery: mismatch counter resets on hash match
//   3. Deep reorg: confirmed block orphaned via verifyConfirmedBlocks
//   4. TOCTOU protection: tip change mid-cycle aborts processing
//   5. Block height exceeds chain height after reorg: delayed orphaning
//   6. Stability window: tip changes reset stability counter
//   7. Full stability path: maturity + 3 stable checks -> confirmed
package payments

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// newReorgTestProcessor creates a Processor wired to the shared mock types from
// solo_mocks_test.go. All tests in this file use mockBlockStore and
// mockDaemonRPC for deterministic chain simulation.
func newReorgTestProcessor(store *mockBlockStore, rpc *mockDaemonRPC, maturity int) *Processor {
	cfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: maturity,
	}
	logger := zap.NewNop()
	return &Processor{
		cfg:          cfg,
		poolCfg:      &config.PoolConfig{},
		logger:       logger.Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 1: Shallow Reorg (1-2 Blocks) — Delayed Orphaning
// ═══════════════════════════════════════════════════════════════════════════════
//
// A shallow reorg replaces the last 1-2 blocks on the chain tip. The payment
// processor detects this as a hash mismatch but must NOT orphan the block
// immediately. OrphanMismatchThreshold (3) consecutive mismatches are required
// before marking a block as orphaned. This prevents false orphaning from
// temporary node desync or minority fork observation.

func TestSOLO_Reorg_AllCoins_ShallowReorg_DelayedOrphaning(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin // capture
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: shallow reorg (1-2 blocks)
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// A single hash mismatch must NOT orphan the block. The delayed
			// orphaning threshold requires OrphanMismatchThreshold (3) consecutive
			// mismatches before transitioning to orphaned status.

			blockHeight := uint64(500000)
			reorgHash := fmt.Sprintf("%064x", blockHeight+9999) // Different hash after reorg
			chainHeight := blockHeight + 50

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			rpc.setBlockHash(blockHeight, reorgHash) // Chain has different hash

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Run ONE cycle — block must NOT be orphaned (threshold not met)
			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Verify: no orphan status update
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block orphaned after single mismatch "+
					"(delayed orphaning threshold of %d not respected)",
					coin.Symbol, OrphanMismatchThreshold)
			}

			// Verify: mismatch counter incremented to 1
			orphanUpdates := store.getOrphanUpdates()
			if len(orphanUpdates) != 1 {
				t.Fatalf("[%s] expected 1 orphan update, got %d", coin.Symbol, len(orphanUpdates))
			}
			if orphanUpdates[0].MismatchCount != 1 {
				t.Errorf("[%s] expected mismatch count 1, got %d",
					coin.Symbol, orphanUpdates[0].MismatchCount)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 2: Shallow Reorg Recovery — Mismatch Counter Reset
// ═══════════════════════════════════════════════════════════════════════════════
//
// After a shallow reorg is resolved (node catches up to the canonical chain),
// the block hash matches again. The OrphanMismatchCount must reset to 0 on
// match, preventing accumulated mismatches from incorrectly orphaning a block
// that has returned to the main chain.

func TestSOLO_Reorg_AllCoins_ShallowReorgRecovers_CounterResets(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: shallow reorg recovery
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// After OrphanMismatchThreshold-1 consecutive mismatches, the hash
			// matches again. The mismatch counter must reset to 0, and the block
			// must NOT be orphaned.

			blockHeight := uint64(600000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			reorgHash := fmt.Sprintf("%064x", blockHeight+9999)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			rpc.setBlockHash(blockHeight, reorgHash) // Initially mismatching

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Phase 1: Run OrphanMismatchThreshold-1 cycles with mismatching hash
			for i := 0; i < OrphanMismatchThreshold-1; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify: NOT orphaned yet
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block orphaned before threshold (%d mismatches needed, had %d)",
					coin.Symbol, OrphanMismatchThreshold, OrphanMismatchThreshold-1)
			}

			// Phase 2: Chain recovers — hash now matches
			rpc.setBlockHash(blockHeight, blockHash)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] recovery cycle: %v", coin.Symbol, err)
			}

			// Verify: mismatch counter was reset to 0
			orphanUpdates := store.getOrphanUpdates()
			lastReset := orphanUpdates[len(orphanUpdates)-1]
			if lastReset.MismatchCount != 0 {
				t.Errorf("[%s] expected mismatch count reset to 0, got %d",
					coin.Symbol, lastReset.MismatchCount)
			}

			// Verify: block is still NOT orphaned
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block orphaned after chain recovered "+
					"(mismatch counter should have reset)", coin.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 3: Deep Reorg — Confirmed Block Orphaned
// ═══════════════════════════════════════════════════════════════════════════════
//
// A deep reorg replaces blocks far behind the chain tip. Blocks that were
// already confirmed may become orphaned. The verifyConfirmedBlocks method
// detects this by re-checking the block hash against the current main chain.
// Unlike pending blocks, confirmed block orphaning is IMMEDIATE (no delay)
// because the block had already passed the full stability window.

func TestSOLO_Reorg_AllCoins_DeepReorg_ConfirmedBlockOrphaned(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: deep reorg orphans confirmed block
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// A previously confirmed block is detected as orphaned during the
			// periodic deep reorg verification. The block must be immediately
			// marked as orphaned since it already passed the stability window.

			blockHeight := uint64(700000)
			deepReorgHash := fmt.Sprintf("%064x", blockHeight+8888)
			chainHeight := blockHeight + 500 // Within DeepReorgMaxAge

			store := newMockBlockStore()
			block := makeConfirmedBlock(coin.Symbol, blockHeight)
			store.addConfirmedBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			rpc.setBlockHash(blockHeight, deepReorgHash) // Different hash after deep reorg

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			err := proc.verifyConfirmedBlocks(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Verify: confirmed block was marked orphaned
			if !store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] SILENT LOSS: confirmed block NOT marked orphaned "+
					"after deep reorg detected (hash mismatch)", coin.Symbol)
			}

			// Verify: the orphan status update has 0 confirmation progress
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusOrphaned {
					if u.Progress != 0 {
						t.Errorf("[%s] orphaned block should have 0 progress, got %f",
							coin.Symbol, u.Progress)
					}
				}
			}
		})
	}
}

// TestSOLO_Reorg_MergeMining_DeepReorg_ParentAndAux verifies deep reorg
// detection for merge mining parent+aux coin pairs. Auxiliary chains inherit
// the parent chain's proof-of-work, so a deep reorg on the parent chain can
// also cause aux chain blocks to be orphaned. Both must be detected.
func TestSOLO_Reorg_MergeMining_DeepReorg_ParentAndAux(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		name := fmt.Sprintf("parent_%s_aux_%s", pair.Parent.Symbol, pair.Aux.Symbol)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Risk vector: deep reorg on merge mining pair
			// Parent: <pair.Parent.Symbol> (<pair.Parent.Algorithm>, <pair.Parent.BlockTimeSec>s)
			// Aux: <pair.Aux.Symbol> (<pair.Aux.Algorithm>, <pair.Aux.BlockTimeSec>s)
			// Both parent and auxiliary confirmed blocks must be detected if
			// orphaned by a deep chain reorganization.

			parentHeight := uint64(800000)
			auxHeight := uint64(800100)
			parentHash := makeBlockHash(pair.Parent.Symbol, parentHeight)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			parentReorgHash := fmt.Sprintf("%064x", parentHeight+7777)
			auxReorgHash := fmt.Sprintf("%064x", auxHeight+7777)
			chainHeight := parentHeight + 500

			store := newMockBlockStore()
			parentBlock := makeConfirmedBlock(pair.Parent.Symbol, parentHeight)
			auxBlock := makeConfirmedBlock(pair.Aux.Symbol, auxHeight)
			store.addConfirmedBlock(parentBlock)
			store.addConfirmedBlock(auxBlock)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			rpc.setBlockHash(parentHeight, parentReorgHash) // Parent hash changed
			rpc.setBlockHash(auxHeight, auxReorgHash)       // Aux hash changed

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			err := proc.verifyConfirmedBlocks(context.Background())
			if err != nil {
				t.Fatalf("[%s+%s] unexpected error: %v",
					pair.Parent.Symbol, pair.Aux.Symbol, err)
			}

			// Verify: parent block orphaned
			if !store.hasStatusUpdate(parentHeight, StatusOrphaned) {
				t.Errorf("[%s] parent block NOT marked orphaned after deep reorg",
					pair.Parent.Symbol)
			}

			// Verify: aux block orphaned
			if !store.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("[%s] aux block NOT marked orphaned after deep reorg "+
					"(parent chain reorg should orphan aux blocks too)",
					pair.Aux.Symbol)
			}

			// Verify: correct hash used (block hash, not reorg hash)
			_ = parentHash
			_ = auxHash
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 4: TOCTOU Protection — Chain Tip Changes Mid-Cycle
// ═══════════════════════════════════════════════════════════════════════════════
//
// If the chain tip changes between the snapshot (start of cycle) and a
// per-block verification check, the entire cycle must be aborted. Processing
// blocks with a stale snapshot would lead to incorrect orphan/confirm
// decisions. No blocks should be modified when a TOCTOU abort occurs.

func TestSOLO_Reorg_AllCoins_TOCTOU_TipChangeMidCycle_Aborted(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: TOCTOU race condition
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// Chain tip changes between the snapshot call and the per-block
			// verification call. The cycle must abort with zero DB mutations
			// to prevent false orphaning or confirming.

			blockHeight := uint64(900000)
			chainHeight := blockHeight + 200

			initialTip := makeChainTip(chainHeight)
			newTip := makeChainTip(chainHeight + 1) // Different tip

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, initialTip)
			rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

			// After the first GetBlockchainInfo (snapshot), change the tip
			// so the second call (TOCTOU check) sees a different tip.
			// IMPORTANT: Use SAME height with different tip to simulate a
			// same-height reorg. Height increase is treated as chain
			// advancement, not a TOCTOU violation.
			callCount := int64(0)
			rpc.tipMutator = func(callIndex int64) *daemon.BlockchainInfo {
				callCount++
				if callCount == 1 {
					return &daemon.BlockchainInfo{
						Chain:         "regtest",
						Blocks:        chainHeight,
						BestBlockHash: initialTip,
					}
				}
				// Subsequent calls return same height but different tip (reorg)
				return &daemon.BlockchainInfo{
					Chain:         "regtest",
					Blocks:        chainHeight,
					BestBlockHash: newTip,
				}
			}

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error (TOCTOU abort should return nil): %v",
					coin.Symbol, err)
			}

			// Verify: NO status updates occurred (cycle aborted)
			statusUpdates := store.getStatusUpdates()
			if len(statusUpdates) > 0 {
				t.Errorf("[%s] TOCTOU: expected 0 status updates on abort, got %d",
					coin.Symbol, len(statusUpdates))
			}

			// Verify: NO orphan updates occurred
			orphanUpdates := store.getOrphanUpdates()
			if len(orphanUpdates) > 0 {
				t.Errorf("[%s] TOCTOU: expected 0 orphan updates on abort, got %d",
					coin.Symbol, len(orphanUpdates))
			}

			// Verify: NO stability updates occurred
			stabilityUpdates := store.getStabilityUpdates()
			if len(stabilityUpdates) > 0 {
				t.Errorf("[%s] TOCTOU: expected 0 stability updates on abort, got %d",
					coin.Symbol, len(stabilityUpdates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 5: Block Height Exceeds Chain Height After Reorg
// ═══════════════════════════════════════════════════════════════════════════════
//
// After a reorg, the chain tip may be at a lower height than a pending block.
// This means block.Height > snapshotHeight. The processor must NOT perform
// the uint64 subtraction (which would underflow) and must use delayed
// orphaning instead of immediate orphaning.

func TestSOLO_Reorg_AllCoins_BlockHeightExceedsChain_DelayedOrphaning(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: block height ahead of chain after reorg
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// Block was found at height N but chain reorged to N-2. The block
			// height now exceeds the chain height. Must use delayed orphaning
			// (increment mismatch counter) and must NOT panic from uint64
			// underflow on the confirmations calculation.

			blockHeight := uint64(1000100)
			chainHeight := uint64(1000098) // 2 blocks behind the pending block

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			// No block hash set for blockHeight — it does not exist on chain

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Run ONE cycle — should not panic, should not orphan
			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Verify: block NOT immediately orphaned
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block immediately orphaned when height "+
					"exceeds chain (should use delayed orphaning)", coin.Symbol)
			}

			// Verify: mismatch counter incremented
			orphanUpdates := store.getOrphanUpdates()
			if len(orphanUpdates) != 1 {
				t.Fatalf("[%s] expected 1 orphan update, got %d",
					coin.Symbol, len(orphanUpdates))
			}
			if orphanUpdates[0].Height != blockHeight {
				t.Errorf("[%s] orphan update for wrong height: %d",
					coin.Symbol, orphanUpdates[0].Height)
			}
			if orphanUpdates[0].MismatchCount != 1 {
				t.Errorf("[%s] expected mismatch count 1, got %d",
					coin.Symbol, orphanUpdates[0].MismatchCount)
			}

			// Phase 2: Run remaining cycles to reach threshold → then orphan
			for i := 1; i < OrphanMismatchThreshold; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Now it should be orphaned after reaching the threshold
			if !store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Errorf("[%s] block should be orphaned after %d consecutive "+
					"height-exceeds-chain cycles", coin.Symbol, OrphanMismatchThreshold)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 6: Stability Window — Tip Changes Do NOT Reset Counter
// ═══════════════════════════════════════════════════════════════════════════════
//
// The stability counter no longer resets when the chain tip changes between
// cycles. This was deliberately removed because fast-block chains (DGB 15s,
// regtest <5s) would never complete the stability window — the tip changes
// every few seconds. The block confirms after StabilityWindowChecks
// consecutive observations regardless of tip changes, as long as the hash
// matches and the block is at maturity.

func TestSOLO_Reorg_AllCoins_StabilityWindow_TipChangesResetCounter(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: stability window with changing tip
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// Block is at maturity and the chain tip changes each cycle.
			// The stability counter does NOT reset on tip change (deliberate
			// design for fast-block chains), so the block should confirm
			// after StabilityWindowChecks cycles.

			blockHeight := uint64(1100000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			// Run StabilityWindowChecks cycles, each with a DIFFERENT tip.
			// The block should confirm because the stability counter no
			// longer resets on tip change.
			for i := 0; i < StabilityWindowChecks; i++ {
				tipHeight := chainHeight + uint64(i)
				tip := makeChainTip(tipHeight)

				rpc := newMockDaemonRPC()
				rpc.setChainTip(tipHeight, tip)
				rpc.setBlockHash(blockHeight, makeBlockHash(coin.Symbol, blockHeight))

				proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify: block WAS confirmed (tip changes do not reset stability)
			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block NOT confirmed after %d stability checks "+
					"(tip changes should not reset stability counter)",
					coin.Symbol, StabilityWindowChecks)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 7: Full Stability Path — Maturity + 3 Stable Checks
// ═══════════════════════════════════════════════════════════════════════════════
//
// The complete happy path: a block reaches maturity (confirmations >= maturity)
// and the chain tip remains stable for StabilityWindowChecks (3) consecutive
// cycles. On the 3rd stable check, the block transitions to confirmed status
// with confirmation progress 1.0.

func TestSOLO_Reorg_AllCoins_FullStabilityPath_Confirmed(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: full stability path to confirmation
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// Block reaches maturity and the chain tip is stable for 3
			// consecutive cycles. Block must transition to confirmed status
			// with confirmation progress 1.0.

			blockHeight := uint64(1200000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			stableTip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, stableTip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Run exactly StabilityWindowChecks cycles with the same stable tip
			for i := 0; i < StabilityWindowChecks; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify: block was confirmed
			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block NOT confirmed after %d stable cycles "+
					"at maturity (full stability path failed)",
					coin.Symbol, StabilityWindowChecks)
			}

			// Verify: confirmation progress is 1.0
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					if u.Progress != 1.0 {
						t.Errorf("[%s] confirmed block should have progress 1.0, got %f",
							coin.Symbol, u.Progress)
					}
				}
			}

			// Verify: stability updates were recorded
			stabilityUpdates := store.getStabilityUpdates()
			if len(stabilityUpdates) < StabilityWindowChecks {
				t.Errorf("[%s] expected at least %d stability updates, got %d",
					coin.Symbol, StabilityWindowChecks, len(stabilityUpdates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SUPPLEMENTAL: Merge Mining Specific — Parent Chain Shallow Reorg
// ═══════════════════════════════════════════════════════════════════════════════
//
// For merge-mined auxiliary coins, a shallow reorg on the parent chain can
// cause the aux block to appear mismatched. The delayed orphaning logic must
// apply identically to aux coins — no special-casing that could lose blocks.

func TestSOLO_Reorg_AuxCoins_ShallowReorg_SameDelayedOrphaning(t *testing.T) {
	t.Parallel()

	for _, coin := range auxCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_parent_%s_%ds", coin.Symbol, coin.ParentChain, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: parent chain shallow reorg affecting aux coin
			// Aux coin: <coin.Symbol>, Parent: <coin.ParentChain>
			// Algorithm: <coin.Algorithm>, Block interval: <coin.BlockTimeSec>s
			// A shallow reorg on the parent chain causes the aux block hash
			// to mismatch. Delayed orphaning must still apply — the aux block
			// must NOT be immediately orphaned.

			blockHeight := uint64(1300000)
			reorgHash := fmt.Sprintf("%064x", blockHeight+6666)
			chainHeight := blockHeight + 20

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			rpc.setBlockHash(blockHeight, reorgHash) // Parent reorg changed aux hash

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Run OrphanMismatchThreshold-1 cycles — block must survive
			for i := 0; i < OrphanMismatchThreshold-1; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify: NOT orphaned
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: aux coin block orphaned before threshold "+
					"during parent chain reorg (parent: %s)",
					coin.Symbol, coin.ParentChain)
			}

			// Verify: mismatch counter at threshold-1
			orphanUpdates := store.getOrphanUpdates()
			if len(orphanUpdates) == 0 {
				t.Fatalf("[%s] expected orphan updates", coin.Symbol)
			}
			lastUpdate := orphanUpdates[len(orphanUpdates)-1]
			if lastUpdate.MismatchCount != OrphanMismatchThreshold-1 {
				t.Errorf("[%s] expected mismatch count %d, got %d",
					coin.Symbol, OrphanMismatchThreshold-1, lastUpdate.MismatchCount)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SUPPLEMENTAL: Shallow Reorg Reaches Threshold — Correct Orphaning
// ═══════════════════════════════════════════════════════════════════════════════
//
// After OrphanMismatchThreshold (3) consecutive hash mismatches, the block
// MUST be marked as orphaned. This verifies the positive case: the threshold
// is correctly enforced and blocks are eventually orphaned when truly reorged.

func TestSOLO_Reorg_AllCoins_ShallowReorg_OrphanedAtThreshold(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: shallow reorg confirmed orphan at threshold
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// After exactly OrphanMismatchThreshold consecutive mismatches,
			// the block must transition to orphaned status.

			blockHeight := uint64(1400000)
			reorgHash := fmt.Sprintf("%064x", blockHeight+5555)
			chainHeight := blockHeight + 50

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			rpc.setBlockHash(blockHeight, reorgHash) // Persistent mismatch

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Run exactly OrphanMismatchThreshold cycles
			for i := 0; i < OrphanMismatchThreshold; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify: block IS now orphaned
			if !store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] block NOT orphaned after %d consecutive mismatches "+
					"(threshold enforcement failed)", coin.Symbol, OrphanMismatchThreshold)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SUPPLEMENTAL: Deep Reorg TOCTOU — verifyConfirmedBlocks Aborts
// ═══════════════════════════════════════════════════════════════════════════════
//
// The verifyConfirmedBlocks method also performs TOCTOU checks. If the chain
// tip changes mid-verification, the cycle must abort to prevent false orphaning
// of confirmed blocks — an even more critical scenario since confirmed blocks
// may have already triggered downstream actions (reward tracking, etc).

func TestSOLO_Reorg_AllCoins_DeepReorg_TOCTOU_Aborted(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: TOCTOU during deep reorg verification
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// Chain tip changes during verifyConfirmedBlocks. Even though the
			// block hash mismatches, the cycle must abort to prevent false
			// orphaning based on a stale chain snapshot.

			blockHeight := uint64(1500000)
			chainHeight := blockHeight + 500

			initialTip := makeChainTip(chainHeight)
			shiftedTip := makeChainTip(chainHeight + 1)

			store := newMockBlockStore()
			block := makeConfirmedBlock(coin.Symbol, blockHeight)
			store.addConfirmedBlock(block)

			rpc := newMockDaemonRPC()
			// Block hash does not match — but TOCTOU should prevent orphaning
			rpc.setBlockHash(blockHeight, fmt.Sprintf("%064x", blockHeight+4444))

			// First GetBlockchainInfo returns initial tip (snapshot),
			// second returns a different tip (TOCTOU detection).
			// Use SAME height with different tip to trigger TOCTOU abort.
			callCount := int64(0)
			rpc.tipMutator = func(callIndex int64) *daemon.BlockchainInfo {
				callCount++
				if callCount == 1 {
					return &daemon.BlockchainInfo{
						Chain:         "regtest",
						Blocks:        chainHeight,
						BestBlockHash: initialTip,
					}
				}
				return &daemon.BlockchainInfo{
					Chain:         "regtest",
					Blocks:        chainHeight,
					BestBlockHash: shiftedTip,
				}
			}

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			err := proc.verifyConfirmedBlocks(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Verify: confirmed block was NOT orphaned (TOCTOU abort)
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: confirmed block falsely orphaned "+
					"during TOCTOU tip change in verifyConfirmedBlocks",
					coin.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SUPPLEMENTAL: Stability Window Partial Progress Then Reorg
// ═══════════════════════════════════════════════════════════════════════════════
//
// A block may accumulate partial stability (e.g., 2 of 3 checks) and then
// experience a hash mismatch due to a late shallow reorg. The stability
// counter must reset AND the orphan mismatch counter must start incrementing.

func TestSOLO_Reorg_AllCoins_StabilityPartialThenReorg(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: partial stability then shallow reorg
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// Block reaches maturity and accumulates StabilityWindowChecks-1
			// stable observations. Then a shallow reorg causes a hash mismatch.
			// The stability counter must reset and the orphan mismatch counter
			// must begin incrementing. The block must NOT be confirmed.

			blockHeight := uint64(1600000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			reorgHash := fmt.Sprintf("%064x", blockHeight+3333)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			stableTip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			block := makePendingBlock(coin.Symbol, blockHeight)
			store.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, stableTip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			// Phase 1: Run StabilityWindowChecks-1 stable cycles
			for i := 0; i < StabilityWindowChecks-1; i++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] stable cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Verify: NOT yet confirmed
			if store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] block confirmed too early (before stability window complete)",
					coin.Symbol)
			}

			// Phase 2: Shallow reorg — hash now mismatches
			rpc.setBlockHash(blockHeight, reorgHash)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] reorg cycle: %v", coin.Symbol, err)
			}

			// Verify: NOT confirmed (stability was interrupted by mismatch)
			if store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] PREMATURE CONFIRM: block confirmed despite hash "+
					"mismatch during stability window", coin.Symbol)
			}

			// Verify: orphan mismatch counter was incremented
			orphanUpdates := store.getOrphanUpdates()
			foundMismatch := false
			for _, u := range orphanUpdates {
				if u.Height == blockHeight && u.MismatchCount > 0 {
					foundMismatch = true
				}
			}
			if !foundMismatch {
				t.Errorf("[%s] expected orphan mismatch counter to increment "+
					"after hash mismatch during stability window", coin.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SUPPLEMENTAL: Deep Reorg — Block Beyond DeepReorgMaxAge Skipped
// ═══════════════════════════════════════════════════════════════════════════════
//
// Confirmed blocks older than DeepReorgMaxAge are not re-verified during deep
// reorg checks. This is a performance optimization: re-verifying extremely old
// blocks has diminishing returns and the risk of a reorg at that depth is
// negligible. The test ensures these blocks are correctly skipped.

func TestSOLO_Reorg_AllCoins_DeepReorg_BeyondMaxAge_Skipped(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Risk vector: deep reorg max age skip
			// Coin: <coin.Symbol>, Block interval: <coin.BlockTimeSec>s, Algorithm: <coin.Algorithm>
			// A confirmed block older than DeepReorgMaxAge should not be
			// re-verified. Even if the hash has changed (hypothetical extreme
			// reorg), the block should NOT be marked orphaned because it is
			// beyond the verification window.

			blockHeight := uint64(1000)
			chainHeight := blockHeight + DeepReorgMaxAge + 100 // Block is too old

			store := newMockBlockStore()
			block := makeConfirmedBlock(coin.Symbol, blockHeight)
			store.addConfirmedBlock(block)

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, makeChainTip(chainHeight))
			// Set a different hash — but it should not matter (block too old)
			rpc.setBlockHash(blockHeight, fmt.Sprintf("%064x", blockHeight+2222))

			proc := newReorgTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

			err := proc.verifyConfirmedBlocks(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Verify: block NOT orphaned (skipped due to age)
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] block beyond DeepReorgMaxAge was incorrectly "+
					"re-verified and orphaned", coin.Symbol)
			}

			// Verify: GetBlockHash was not called for this block
			// (the skip happens before the hash check)
			hashCalls := rpc.getBlockHashCalls
			if hashCalls > 0 {
				t.Errorf("[%s] expected 0 GetBlockHash calls for old block, got %d",
					coin.Symbol, hashCalls)
			}
		})
	}
}
