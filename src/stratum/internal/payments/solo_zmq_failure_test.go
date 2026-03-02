// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// ZMQ failure mode test suite for SOLO mining block loss prevention.
//
// ZMQ (ZeroMQ) is the primary mechanism for instant block notifications from
// the daemon. When ZMQ fails (drop, duplicate, stall, disabled), blocks are
// discovered later via RPC polling fallback. From the payment processor's
// perspective, ZMQ failures affect *when* blocks appear in the database, not
// *whether* the processor handles them correctly.
//
// KEY GUARANTEE: The processor correctly tracks all blocks through the
// confirmation lifecycle (pending -> confirmed) regardless of how or when
// they were discovered. No block is silently lost.
//
// Risk vectors tested:
//   1. ZMQ drop - block discovered late via polling, delayed DB entry
//   2. ZMQ duplicate - same block appears twice in pending blocks
//   3. ZMQ stall - blocks accumulate without processing, then bulk catch-up
//   4. ZMQ disabled - pure polling mode, all coins, all block intervals
//   5. ZMQ recovery mid-cycle - blocks during transition don't get stuck
//   6. Multiple blocks during ZMQ outage - 3+ blocks all reach confirmed
package payments

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

// newZMQTestProcessor creates a Processor using the shared mockBlockStore and
// mockDaemonRPC from solo_mocks_test.go, configured for ZMQ failure testing.
func newZMQTestProcessor(db *mockBlockStore, daemon *mockDaemonRPC, maturity int) *Processor {
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
		db:           db,
		daemonClient: daemon,
		stopCh:       make(chan struct{}),
	}
}

// =============================================================================
// RISK VECTOR 1: ZMQ Drop (Block Discovered Late via Polling)
// =============================================================================
//
// When ZMQ drops a hashblock notification, the block is discovered later by
// the RPC polling fallback. The block appears in the database with a delayed
// timestamp. The processor must still track it through the full confirmation
// lifecycle without loss.

// TestSOLO_ZMQFailure_AllCoins_Drop exercises every supported coin to verify
// that a block discovered late (simulating ZMQ drop -> RPC polling fallback)
// is correctly tracked through pending -> confirmed.
//
// Risk vector: ZMQ drop (block notification lost, discovered via polling)
// For each coin: block interval varies, algorithm varies (sha256d/scrypt)
func TestSOLO_ZMQFailure_AllCoins_Drop(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin // capture range variable
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Simulate: block found at height 50000, discovered late via polling.
			// By the time it enters the DB, the chain has advanced well past maturity.
			blockHeight := uint64(50000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 20 // 20 extra confirmations beyond maturity

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run StabilityWindowChecks cycles - block should progress to confirmed.
			// The late discovery doesn't matter; the processor sees it as a normal
			// pending block that already has enough confirmations.
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			// Assert: block must reach confirmed status
			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block at height %d was not confirmed after ZMQ drop "+
					"(algorithm=%s, blockInterval=%ds)",
					coin.Symbol, blockHeight, coin.Algorithm, coin.BlockTimeSec)
			}

			// Assert: block must NOT be orphaned
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block at height %d was falsely orphaned after ZMQ drop",
					coin.Symbol, blockHeight)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 2: ZMQ Duplicate Notification
// =============================================================================
//
// When ZMQ sends the same hashblock notification twice, the block submission
// path may (defensively) insert duplicate pending entries. The processor must
// handle both entries without confusion, double-confirmation, or panic.

// TestSOLO_ZMQFailure_AllCoins_Duplicate verifies that duplicate pending
// block entries (same height, same hash) caused by ZMQ duplicate notifications
// are handled without block loss or status corruption.
//
// Risk vector: ZMQ duplicate (same hashblock notification received twice)
// Coins: dynamic loop over all 13 supported coins
func TestSOLO_ZMQFailure_AllCoins_Duplicate(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(75000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 5

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			// Add the SAME block twice - simulating duplicate ZMQ notification
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run stability cycles. Both duplicate entries will be processed.
			// The processor calls UpdateBlockStatus for each entry it sees.
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			// Assert: the block height was updated to confirmed (at least once)
			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block at height %d not confirmed despite duplicate entries "+
					"(algorithm=%s, blockInterval=%ds)",
					coin.Symbol, blockHeight, coin.Algorithm, coin.BlockTimeSec)
			}

			// Assert: no orphaned status for this height
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: duplicate entry caused false orphaning at height %d",
					coin.Symbol, blockHeight)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 3: ZMQ Stall (No Notifications for Extended Period)
// =============================================================================
//
// When ZMQ stalls (socket connected but no messages delivered), blocks
// accumulate in the database via polling. When the processor finally runs,
// it must handle all accumulated blocks in a single cycle without loss.

// TestSOLO_ZMQFailure_AllCoins_Stall verifies that when multiple blocks
// accumulate during a ZMQ stall (discovered in bulk via polling), the
// processor correctly tracks all of them through confirmation.
//
// Risk vector: ZMQ stall (connected but no messages for extended period)
// Simulates: 5 blocks accumulate during stall, all at different heights
func TestSOLO_ZMQFailure_AllCoins_Stall(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// 5 blocks found during ZMQ stall, spaced by the coin's block interval.
			// All discovered at once when polling catches up.
			baseHeight := uint64(100000)
			blockCount := 5
			maturity := DefaultBlockMaturityConfirmations

			// Chain has advanced far enough that all blocks have maturity+10 confirmations
			chainHeight := baseHeight + uint64(blockCount) + uint64(maturity) + 10
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)

			// Add all accumulated blocks to pending
			for i := 0; i < blockCount; i++ {
				height := baseHeight + uint64(i)
				store.addPendingBlock(makePendingBlock(coin.Symbol, height))
				daemon.setBlockHash(height, makeBlockHash(coin.Symbol, height))
			}

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run StabilityWindowChecks cycles - all blocks should reach confirmed
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			// Assert: every single block must reach confirmed status
			for i := 0; i < blockCount; i++ {
				height := baseHeight + uint64(i)
				if !store.hasStatusUpdate(height, StatusConfirmed) {
					t.Errorf("[%s] BLOCK LOSS: block at height %d not confirmed after ZMQ stall "+
						"(block %d of %d, algorithm=%s, blockInterval=%ds)",
						coin.Symbol, height, i+1, blockCount, coin.Algorithm, coin.BlockTimeSec)
				}
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 4: ZMQ Disabled (Pure Polling Mode)
// =============================================================================
//
// When ZMQ is entirely disabled, blocks are discovered only via RPC polling.
// The processor must work identically in this mode. This tests every coin
// and every block interval to ensure no coin-specific edge case is missed.

// TestSOLO_ZMQFailure_AllCoins_Disabled verifies that with ZMQ completely
// disabled (pure polling), the processor correctly confirms blocks for every
// supported coin. The key invariant: block discovery method is irrelevant
// to the processor; only DB contents matter.
//
// Risk vector: ZMQ disabled (pure RPC polling fallback for all block discovery)
// Coverage: all 13 coins, all algorithms (sha256d, scrypt), all block intervals
func TestSOLO_ZMQFailure_AllCoins_Disabled(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Simulate pure polling: block discovered and inserted into DB.
			// Chain is well past maturity. Processor should confirm normally.
			blockHeight := uint64(200000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 50

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run confirmation cycles
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block at height %d not confirmed in pure polling mode "+
					"(algorithm=%s, blockInterval=%ds)",
					coin.Symbol, blockHeight, coin.Algorithm, coin.BlockTimeSec)
			}
		})
	}
}

// TestSOLO_ZMQFailure_DisabledSHA256d_AllCoins tests the SHA-256d subset
// specifically, since these coins share the BTC merge-mining parent chain
// and block interval characteristics matter for polling timing.
//
// Risk vector: ZMQ disabled, SHA-256d algorithm coins only
// Coins: BTC, BCH, DGB, BC2, NMC, SYS, XMY, FBTC
func TestSOLO_ZMQFailure_DisabledSHA256d_AllCoins(t *testing.T) {
	t.Parallel()

	for _, coin := range sha256dCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_%ds", coin.Symbol, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(300000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 30

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] SHA-256d coin not confirmed in polling mode (interval=%ds, parent=%v, aux=%v)",
					coin.Symbol, coin.BlockTimeSec, coin.IsParent, coin.IsAux)
			}
		})
	}
}

// TestSOLO_ZMQFailure_DisabledScrypt_AllCoins tests the Scrypt subset,
// which includes LTC-based merge-mined coins with shorter block intervals.
//
// Risk vector: ZMQ disabled, Scrypt algorithm coins only
// Coins: LTC, DOGE, DGB-SCRYPT, PEP, CAT
func TestSOLO_ZMQFailure_DisabledScrypt_AllCoins(t *testing.T) {
	t.Parallel()

	for _, coin := range scryptCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_%ds", coin.Symbol, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(400000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 30

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] Scrypt coin not confirmed in polling mode (interval=%ds, parent=%v, aux=%v)",
					coin.Symbol, coin.BlockTimeSec, coin.IsParent, coin.IsAux)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 5: ZMQ Recovery Mid-Cycle
// =============================================================================
//
// When ZMQ recovers mid-processing-cycle, blocks found during the transition
// period may have inconsistent discovery timestamps. The processor must not
// leave any block stuck in the wrong state.

// TestSOLO_ZMQFailure_AllCoins_RecoveryMidCycle verifies that blocks found
// during a ZMQ outage/recovery transition are correctly tracked. Simulates:
// - Block A: found during ZMQ outage (polled), already in DB when processor runs
// - Block B: found right as ZMQ recovers, also in DB
// Both must reach confirmed without interference.
//
// Risk vector: ZMQ recovery mid-cycle (transition between polling and ZMQ)
// Coins: dynamic loop over all supported coins
func TestSOLO_ZMQFailure_AllCoins_RecoveryMidCycle(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Block A: found during outage at height 60000 (polling)
			// Block B: found during recovery at height 60001 (ZMQ just came back)
			heightA := uint64(60000)
			heightB := uint64(60001)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := heightB + uint64(maturity) + 15

			hashA := makeBlockHash(coin.Symbol, heightA)
			hashB := makeBlockHash(coin.Symbol, heightB)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightA))
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightB))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(heightA, hashA)
			daemon.setBlockHash(heightB, hashB)

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Both blocks must confirm
			if !store.hasStatusUpdate(heightA, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block A (height=%d, found during outage) not confirmed after ZMQ recovery",
					coin.Symbol, heightA)
			}
			if !store.hasStatusUpdate(heightB, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block B (height=%d, found during recovery) not confirmed after ZMQ recovery",
					coin.Symbol, heightB)
			}

			// Neither block should be orphaned
			if store.hasStatusUpdate(heightA, StatusOrphaned) {
				t.Fatalf("[%s] block A falsely orphaned during ZMQ recovery", coin.Symbol)
			}
			if store.hasStatusUpdate(heightB, StatusOrphaned) {
				t.Fatalf("[%s] block B falsely orphaned during ZMQ recovery", coin.Symbol)
			}
		})
	}
}

// TestSOLO_ZMQFailure_RecoveryMidCycle_StabilityNotCorrupted verifies that
// the stability window counter is not corrupted when blocks from different
// discovery modes (polling vs ZMQ) are processed in the same cycle.
//
// Risk vector: ZMQ recovery mid-cycle with interleaved discovery modes
// Scenario: partially stable block has stability counter preserved across
//           the ZMQ transition boundary.
func TestSOLO_ZMQFailure_RecoveryMidCycle_StabilityNotCorrupted(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(80000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 10

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			// Block already has partial stability progress (from before ZMQ outage)
			block := makePendingBlock(coin.Symbol, blockHeight)
			block.StabilityCheckCount = StabilityWindowChecks - 1
			block.LastVerifiedTip = tipHash // Same tip - simulates stable chain
			store.addPendingBlock(block)

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			// One more cycle should push it over the stability threshold
			if err := proc.updateBlockConfirmations(context.Background()); err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Block should now be confirmed (StabilityWindowChecks-1 + 1 = StabilityWindowChecks)
			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] block should confirm after ZMQ recovery completes stability window "+
					"(had %d/%d checks before recovery)",
					coin.Symbol, StabilityWindowChecks-1, StabilityWindowChecks)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 6: Multiple Blocks During ZMQ Outage
// =============================================================================
//
// During an extended ZMQ outage, a solo miner may find multiple blocks.
// All of these blocks are discovered via polling and inserted into the DB.
// The processor must confirm every single one without loss.

// TestSOLO_ZMQFailure_AllCoins_MultipleBlocksDuringOutage verifies that
// finding 3+ blocks while ZMQ is down results in all blocks reaching
// confirmed status through normal processor cycles.
//
// Risk vector: Multiple blocks during ZMQ outage (3+ blocks, all via polling)
// Coverage: all coins, 4 blocks per coin at consecutive heights
func TestSOLO_ZMQFailure_AllCoins_MultipleBlocksDuringOutage(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			baseHeight := uint64(150000)
			blockCount := 4
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := baseHeight + uint64(blockCount) + uint64(maturity) + 25

			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)

			heights := make([]uint64, blockCount)
			for i := 0; i < blockCount; i++ {
				h := baseHeight + uint64(i)
				heights[i] = h
				store.addPendingBlock(makePendingBlock(coin.Symbol, h))
				daemon.setBlockHash(h, makeBlockHash(coin.Symbol, h))
			}

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run StabilityWindowChecks cycles
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Assert ALL blocks confirmed, none lost
			for idx, h := range heights {
				if !store.hasStatusUpdate(h, StatusConfirmed) {
					t.Errorf("[%s] BLOCK LOSS: block %d/%d at height %d not confirmed during ZMQ outage "+
						"(algorithm=%s, blockInterval=%ds)",
						coin.Symbol, idx+1, blockCount, h, coin.Algorithm, coin.BlockTimeSec)
				}
				if store.hasStatusUpdate(h, StatusOrphaned) {
					t.Fatalf("[%s] block %d/%d at height %d falsely orphaned during ZMQ outage",
						coin.Symbol, idx+1, blockCount, h)
				}
			}
		})
	}
}

// TestSOLO_ZMQFailure_MultipleBlocks_PartialMaturity verifies correct
// handling when blocks found during ZMQ outage have varying confirmation
// depths - some at maturity, some not yet.
//
// Risk vector: Multiple blocks during outage with mixed maturity states
// Scenario: 3 blocks - one mature, one near maturity, one very recent
func TestSOLO_ZMQFailure_MultipleBlocks_PartialMaturity(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			maturity := 50 // Smaller maturity for clearer testing
			chainHeight := uint64(100200)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			// Block A: well past maturity (found early in outage)
			heightA := uint64(100000) // 200 confirmations
			// Block B: exactly at maturity boundary
			heightB := chainHeight - uint64(maturity) // exactly 50 confirmations
			// Block C: very recent (found just before polling caught up)
			heightC := chainHeight - 5 // only 5 confirmations

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightA))
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightB))
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightC))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(heightA, makeBlockHash(coin.Symbol, heightA))
			daemon.setBlockHash(heightB, makeBlockHash(coin.Symbol, heightB))
			daemon.setBlockHash(heightC, makeBlockHash(coin.Symbol, heightC))

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run StabilityWindowChecks cycles
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Block A (200 confirmations >> maturity): MUST be confirmed
			if !store.hasStatusUpdate(heightA, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block A (height=%d, 200 confs) should be confirmed",
					coin.Symbol, heightA)
			}

			// Block B (exactly at maturity): MUST be confirmed after stability window
			if !store.hasStatusUpdate(heightB, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block B (height=%d, exact maturity) should be confirmed",
					coin.Symbol, heightB)
			}

			// Block C (5 confirmations < maturity=50): must NOT be confirmed yet
			if store.hasStatusUpdate(heightC, StatusConfirmed) {
				t.Errorf("[%s] block C (height=%d, only 5 confs) should NOT be confirmed yet (maturity=%d)",
					coin.Symbol, heightC, maturity)
			}

			// Block C must still be tracked (pending status update recorded)
			if !store.hasStatusUpdate(heightC, StatusPending) {
				t.Errorf("[%s] block C (height=%d) should have pending status update (still being tracked)",
					coin.Symbol, heightC)
			}

			// NO blocks should be orphaned
			for _, h := range []uint64{heightA, heightB, heightC} {
				if store.hasStatusUpdate(h, StatusOrphaned) {
					t.Fatalf("[%s] BLOCK LOSS: block at height %d falsely orphaned", coin.Symbol, h)
				}
			}
		})
	}
}

// TestSOLO_ZMQFailure_MultipleBlocks_GradualConfirmation verifies that
// blocks found during ZMQ outage gradually reach confirmation as the chain
// progresses, not all at once.
//
// Risk vector: staggered confirmation of blocks at different depths
// Scenario: 3 blocks at different heights processed over increasing chain heights
func TestSOLO_ZMQFailure_MultipleBlocks_GradualConfirmation(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			maturity := 20 // Small maturity for controlled testing
			heightOld := uint64(1000)
			heightMid := uint64(1015)
			heightNew := uint64(1025)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightOld))
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightMid))
			store.addPendingBlock(makePendingBlock(coin.Symbol, heightNew))

			daemon := newMockDaemonRPC()

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Phase 1: Chain at 1025 - only heightOld has enough confirmations (25 >= 20)
			// heightMid has 10, heightNew has 0
			phase1Tip := fmt.Sprintf("%064x", uint64(1025))
			daemon.setChainTip(1025, phase1Tip)
			daemon.setBlockHash(heightOld, makeBlockHash(coin.Symbol, heightOld))
			daemon.setBlockHash(heightMid, makeBlockHash(coin.Symbol, heightMid))
			daemon.setBlockHash(heightNew, makeBlockHash(coin.Symbol, heightNew))

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] phase1 cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(heightOld, StatusConfirmed) {
				t.Errorf("[%s] heightOld (%d) should be confirmed at chain=1025 (25 confs >= maturity=%d)",
					coin.Symbol, heightOld, maturity)
			}
			if store.hasStatusUpdate(heightMid, StatusConfirmed) {
				t.Errorf("[%s] heightMid (%d) should NOT be confirmed at chain=1025 (10 confs < maturity=%d)",
					coin.Symbol, heightMid, maturity)
			}

			// Phase 2: Chain advances to 1040 - heightMid now has 25 confirmations
			phase2Tip := fmt.Sprintf("%064x", uint64(1040))
			daemon.setChainTip(1040, phase2Tip)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] phase2 cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(heightMid, StatusConfirmed) {
				t.Errorf("[%s] heightMid (%d) should be confirmed at chain=1040 (25 confs >= maturity=%d)",
					coin.Symbol, heightMid, maturity)
			}

			// Phase 3: Chain advances to 1050 - heightNew now has 25 confirmations
			phase3Tip := fmt.Sprintf("%064x", uint64(1050))
			daemon.setChainTip(1050, phase3Tip)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] phase3 cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(heightNew, StatusConfirmed) {
				t.Errorf("[%s] heightNew (%d) should be confirmed at chain=1050 (25 confs >= maturity=%d)",
					coin.Symbol, heightNew, maturity)
			}

			// Final: no blocks orphaned
			for _, h := range []uint64{heightOld, heightMid, heightNew} {
				if store.hasStatusUpdate(h, StatusOrphaned) {
					t.Fatalf("[%s] BLOCK LOSS: block at height %d falsely orphaned during gradual confirmation",
						coin.Symbol, h)
				}
			}
		})
	}
}

// =============================================================================
// AUXILIARY TESTS: Merge-Mining and Edge Cases with ZMQ Failures
// =============================================================================

// TestSOLO_ZMQFailure_AuxCoins_DropDuringMergeBlock verifies that auxiliary
// (merge-mined) coins correctly confirm blocks discovered late via polling.
// Merge-mined blocks have a parent chain dependency but from the payment
// processor's perspective, they are identical to standalone blocks.
//
// Risk vector: ZMQ drop on auxiliary merge-mined coins
// Coins: NMC, SYS, XMY, FBTC (BTC parent), DOGE, PEP (LTC parent)
func TestSOLO_ZMQFailure_AuxCoins_DropDuringMergeBlock(t *testing.T) {
	t.Parallel()

	for _, coin := range auxCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_parent_%s_%ds", coin.Symbol, coin.ParentChain, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(500000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 10

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: aux coin block not confirmed after ZMQ drop "+
					"(parent=%s, algorithm=%s, blockInterval=%ds)",
					coin.Symbol, coin.ParentChain, coin.Algorithm, coin.BlockTimeSec)
			}
		})
	}
}

// TestSOLO_ZMQFailure_ParentCoins_StallWithChildBlocks verifies that parent
// chain coins (BTC, LTC) correctly handle ZMQ stall scenarios. Parent chains
// are the ZMQ subscription source for their child coins.
//
// Risk vector: ZMQ stall on parent chain coins
// Coins: BTC (sha256d), LTC (scrypt)
func TestSOLO_ZMQFailure_ParentCoins_StallWithChildBlocks(t *testing.T) {
	t.Parallel()

	for _, coin := range parentCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			// Parent chain stall: 3 blocks accumulate
			baseHeight := uint64(800000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := baseHeight + 3 + uint64(maturity) + 10

			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)

			for i := 0; i < 3; i++ {
				h := baseHeight + uint64(i)
				store.addPendingBlock(makePendingBlock(coin.Symbol, h))
				daemon.setBlockHash(h, makeBlockHash(coin.Symbol, h))
			}

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			for i := 0; i < 3; i++ {
				h := baseHeight + uint64(i)
				if !store.hasStatusUpdate(h, StatusConfirmed) {
					t.Errorf("[%s] BLOCK LOSS: parent chain block %d at height %d not confirmed after stall",
						coin.Symbol, i+1, h)
				}
			}
		})
	}
}

// =============================================================================
// DEFENSIVE EDGE CASES
// =============================================================================

// TestSOLO_ZMQFailure_DropThenReorg verifies the worst case: ZMQ drops a
// notification, the block is discovered late, AND a reorg has occurred in
// the meantime. The delayed orphaning mechanism must still protect against
// false orphaning while correctly identifying genuine orphans.
//
// Risk vector: ZMQ drop + chain reorganization (compound failure)
// Scenario: block discovered late, but chain reorged away from our block
func TestSOLO_ZMQFailure_DropThenReorg(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(90000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 5

			// Chain reorged: hash at our height is now different from what makePendingBlock sets
			reorgHash := fmt.Sprintf("%064x", blockHeight+99999)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, reorgHash) // Reorged!

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Run cycles less than OrphanMismatchThreshold: should NOT orphan yet
			for i := 0; i < OrphanMismatchThreshold-1; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Block should NOT be orphaned yet (delayed orphaning protects it)
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Errorf("[%s] block orphaned too early (before threshold of %d mismatches)",
					coin.Symbol, OrphanMismatchThreshold)
			}

			// Verify mismatch counter was incremented
			orphanUpdates := store.getOrphanUpdates()
			foundMismatch := false
			for _, u := range orphanUpdates {
				if u.Height == blockHeight && u.MismatchCount > 0 {
					foundMismatch = true
				}
			}
			if !foundMismatch {
				t.Errorf("[%s] expected mismatch counter increments for reorged block", coin.Symbol)
			}

			// Run remaining cycles to reach threshold
			if err := proc.updateBlockConfirmations(context.Background()); err != nil {
				t.Fatalf("[%s] final cycle: %v", coin.Symbol, err)
			}

			// NOW the block should be orphaned (genuine orphan after threshold)
			if !store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Errorf("[%s] block should be orphaned after %d consecutive mismatches "+
					"(ZMQ drop + reorg = genuine orphan)", coin.Symbol, OrphanMismatchThreshold)
			}

			// Verify it was NOT confirmed (contradictory states would be a bug)
			if store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Fatalf("[%s] BUG: block was both confirmed and orphaned", coin.Symbol)
			}
		})
	}
}

// TestSOLO_ZMQFailure_DropThenReorgRecovery verifies the recovery case:
// ZMQ drops a notification, a temporary reorg occurs, but then the chain
// recovers to include our block. The block should eventually confirm.
//
// Risk vector: ZMQ drop + temporary reorg + recovery (compound failure with happy path)
func TestSOLO_ZMQFailure_DropThenReorgRecovery(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(95000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 10

			ourHash := makeBlockHash(coin.Symbol, blockHeight)
			reorgHash := fmt.Sprintf("%064x", blockHeight+99999)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			// Phase 1: reorg active - hash doesn't match
			daemon.setBlockHash(blockHeight, reorgHash)
			for i := 0; i < OrphanMismatchThreshold-1; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] reorg cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Block should NOT be orphaned (under threshold)
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] block orphaned before threshold during temporary reorg", coin.Symbol)
			}

			// Phase 2: chain recovers - our hash is back
			daemon.setBlockHash(blockHeight, ourHash)

			// Run StabilityWindowChecks cycles with correct hash
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] recovery cycle %d: %v", coin.Symbol, i, err)
				}
			}

			// Block should be confirmed after recovery
			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block should confirm after temporary reorg recovery "+
					"(ZMQ drop + temp reorg + recovery)", coin.Symbol)
			}
		})
	}
}

// TestSOLO_ZMQFailure_EmptyPendingNoPanic verifies that when ZMQ is down
// and no blocks have been found yet (empty DB), the processor handles
// the empty state gracefully without panics or errors.
//
// Risk vector: ZMQ failure with empty pending block set
func TestSOLO_ZMQFailure_EmptyPendingNoPanic(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	daemon := newMockDaemonRPC()
	// Default daemon has chain at height 1000

	proc := newZMQTestProcessor(store, daemon, DefaultBlockMaturityConfirmations)

	// Should complete without error or panic
	if err := proc.updateBlockConfirmations(context.Background()); err != nil {
		t.Fatalf("unexpected error with empty pending blocks: %v", err)
	}

	// No status updates should have been made
	updates := store.getStatusUpdates()
	if len(updates) != 0 {
		t.Errorf("expected 0 status updates with empty pending, got %d", len(updates))
	}
}

// TestSOLO_ZMQFailure_VerifyConfirmedAfterOutage verifies that the deep
// reorg check correctly re-verifies confirmed blocks after a ZMQ outage.
// Blocks confirmed during the outage should remain confirmed if still in
// the main chain.
//
// Risk vector: ZMQ outage affecting deep reorg verification of confirmed blocks
func TestSOLO_ZMQFailure_VerifyConfirmedAfterOutage(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(600000)
			chainHeight := blockHeight + 500 // Well within DeepReorgMaxAge

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			store.addConfirmedBlock(makeConfirmedBlock(coin.Symbol, blockHeight))

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash) // Still in main chain

			proc := newZMQTestProcessor(store, daemon, DefaultBlockMaturityConfirmations)

			// Run deep reorg verification
			if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Block should NOT be orphaned (still in main chain)
			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: confirmed block falsely orphaned during post-outage verification",
					coin.Symbol)
			}
		})
	}
}

// TestSOLO_ZMQFailure_HighFrequencyCoins_BulkBlocks specifically tests
// high-frequency coins (short block intervals like DGB=15s, FBTC=30s) which
// accumulate more blocks during a ZMQ outage of the same duration.
//
// Risk vector: ZMQ stall on high-frequency block coins
// Scenario: 10 blocks accumulated (simulating ~150s outage for 15s block coin)
func TestSOLO_ZMQFailure_HighFrequencyCoins_BulkBlocks(t *testing.T) {
	t.Parallel()

	// Select only coins with block intervals <= 60 seconds
	var fastCoins []testCoinConfig
	for _, c := range allTestCoins {
		if c.BlockTimeSec <= 60 {
			fastCoins = append(fastCoins, c)
		}
	}

	for _, coin := range fastCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%ds", coin.Symbol, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			baseHeight := uint64(250000)
			// More blocks for faster coins
			blockCount := 10
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := baseHeight + uint64(blockCount) + uint64(maturity) + 20

			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()
			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)

			for i := 0; i < blockCount; i++ {
				h := baseHeight + uint64(i)
				store.addPendingBlock(makePendingBlock(coin.Symbol, h))
				daemon.setBlockHash(h, makeBlockHash(coin.Symbol, h))
			}

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			lostCount := 0
			for i := 0; i < blockCount; i++ {
				h := baseHeight + uint64(i)
				if !store.hasStatusUpdate(h, StatusConfirmed) {
					lostCount++
					t.Errorf("[%s] BLOCK LOSS: block %d/%d at height %d not confirmed "+
						"(high-frequency coin, blockInterval=%ds)",
						coin.Symbol, i+1, blockCount, h, coin.BlockTimeSec)
				}
			}

			if lostCount > 0 {
				t.Errorf("[%s] %d/%d blocks lost during ZMQ stall on high-frequency coin",
					coin.Symbol, lostCount, blockCount)
			}
		})
	}
}

// TestSOLO_ZMQFailure_MergePairs_SimultaneousDrop verifies that when ZMQ
// drops notifications for both a parent and auxiliary coin simultaneously
// (e.g., BTC+NMC merge-mined block), both blocks are correctly tracked.
//
// Risk vector: ZMQ drop affecting merge-mining parent+aux pair simultaneously
// Pairs: BTC+NMC, BTC+SYS, BTC+XMY, BTC+FBTC, LTC+DOGE, LTC+PEP
func TestSOLO_ZMQFailure_MergePairs_SimultaneousDrop(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s+%s", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Both parent and aux block found at same time via merge mining.
			// ZMQ dropped both notifications.
			parentHeight := uint64(700000)
			auxHeight := uint64(700000) // Aux chain may have different height in practice

			maturity := DefaultBlockMaturityConfirmations
			chainHeight := parentHeight + uint64(maturity) + 15

			parentHash := makeBlockHash(pair.Parent.Symbol, parentHeight)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			// Each coin has its own processor, store, and daemon in practice.
			// Test them independently to verify neither is lost.

			// Parent coin
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(chainHeight, tipHash)
			parentDaemon.setBlockHash(parentHeight, parentHash)
			parentProc := newZMQTestProcessor(parentStore, parentDaemon, maturity)

			// Aux coin
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(chainHeight, tipHash)
			auxDaemon.setBlockHash(auxHeight, auxHash)
			auxProc := newZMQTestProcessor(auxStore, auxDaemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := parentProc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			if !parentStore.hasStatusUpdate(parentHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: parent chain block not confirmed after simultaneous ZMQ drop",
					pair.Parent.Symbol)
			}
			if !auxStore.hasStatusUpdate(auxHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: aux chain block not confirmed after simultaneous ZMQ drop "+
					"(parent=%s)", pair.Aux.Symbol, pair.Parent.Symbol)
			}
		})
	}
}

// TestSOLO_ZMQFailure_DuplicateWithDifferentTimestamps verifies that
// duplicate pending entries with different Created timestamps (as might
// happen if the same block is inserted via both ZMQ and polling) are
// handled without confusion.
//
// Risk vector: ZMQ duplicate with timing mismatch
func TestSOLO_ZMQFailure_DuplicateWithDifferentTimestamps(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(120000)
			maturity := DefaultBlockMaturityConfirmations
			chainHeight := blockHeight + uint64(maturity) + 10

			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			tipHash := fmt.Sprintf("%064x", chainHeight)

			store := newMockBlockStore()

			// First entry: via ZMQ (earlier timestamp)
			block1 := makePendingBlock(coin.Symbol, blockHeight)
			block1.Created = time.Now().Add(-10 * time.Minute)
			store.addPendingBlock(block1)

			// Second entry: via polling (later timestamp)
			block2 := makePendingBlock(coin.Symbol, blockHeight)
			block2.Created = time.Now().Add(-5 * time.Minute)
			store.addPendingBlock(block2)

			daemon := newMockDaemonRPC()
			daemon.setChainTip(chainHeight, tipHash)
			daemon.setBlockHash(blockHeight, blockHash)

			proc := newZMQTestProcessor(store, daemon, maturity)

			for i := 0; i < StabilityWindowChecks; i++ {
				if err := proc.updateBlockConfirmations(context.Background()); err != nil {
					t.Fatalf("[%s] cycle %d: %v", coin.Symbol, i, err)
				}
			}

			if !store.hasStatusUpdate(blockHeight, StatusConfirmed) {
				t.Errorf("[%s] BLOCK LOSS: block with duplicate timestamps not confirmed", coin.Symbol)
			}

			if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block with duplicate timestamps falsely orphaned", coin.Symbol)
			}
		})
	}
}

// TestSOLO_ZMQFailure_BestBlockHash64Chars ensures that all test helpers
// produce 64-character hex BestBlockHash values, matching real daemon output.
// ZMQ notifications use these hashes, and truncation would cause mismatch.
func TestSOLO_ZMQFailure_BestBlockHash64Chars(t *testing.T) {
	t.Parallel()

	// Verify makeBlockHash produces 64-char hashes
	for _, coin := range allTestCoins {
		hash := makeBlockHash(coin.Symbol, 12345)
		if len(hash) != 64 {
			t.Errorf("[%s] makeBlockHash produced %d chars, want 64: %q",
				coin.Symbol, len(hash), hash)
		}
	}

	// Verify makeChainTip produces 64-char hashes
	for _, height := range []uint64{0, 1, 100, 999999, 18446744073709551615} {
		tip := makeChainTip(height)
		if len(tip) != 64 {
			t.Errorf("makeChainTip(%d) produced %d chars, want 64: %q",
				height, len(tip), tip)
		}
	}

	// Verify the default mock daemon BestBlockHash is 64 chars
	daemon := newMockDaemonRPC()
	if len(daemon.bestBlockHash) != 64 {
		t.Errorf("default mock BestBlockHash is %d chars, want 64: %q",
			len(daemon.bestBlockHash), daemon.bestBlockHash)
	}

	// Verify our test tip format produces 64 chars
	testTip := fmt.Sprintf("%064x", uint64(50000))
	if len(testTip) != 64 {
		t.Errorf("test tip format produced %d chars, want 64: %q",
			len(testTip), testTip)
	}
}

// Suppress unused import warnings for database package (used by mock types
// from solo_mocks_test.go which are referenced in this file).
var _ = (*database.Block)(nil)
