// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Block timing edge-case tests for SOLO mode payment processing.
//
// These tests verify that block timing differences across supported coins
// do not cause silent block loss. Coins have vastly different block intervals:
//   - Fast blocks:   DGB (15s), DGB-SCRYPT (15s), FBTC (30s)
//   - Medium blocks: DOGE (60s), SYS (60s), LTC (150s)
//   - Slow blocks:   BTC (600s), BCH (600s), CAT (600s)
//
// Risk vectors tested:
//   1. Fast block coins: rapid confirmation accumulation with 100+ blocks
//   2. Slow block coins: long pending duration with no premature orphaning
//   3. Medium block coins: standard progression path
//   4. Block interval vs maturity: DGB 15s blocks * 100 confirmations = 25 min
//   5. All coins at maturity edge: maturity-1 stays pending, maturity enters stability
//   6. Rapid successive blocks: 5 blocks found quickly, all tracked independently
//   7. Per-coin maturity differences: custom BlockMaturity vs default
package payments

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// =============================================================================
// HELPERS
// =============================================================================

// newTimingProcessor creates a Processor configured for timing tests with the
// shared mock types from solo_mocks_test.go.
func newTimingProcessor(store *mockBlockStore, rpc *mockDaemonRPC, maturity int) *Processor {
	cfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: maturity,
	}
	logger := zap.NewNop()
	return &Processor{
		cfg:          cfg,
		poolCfg:      &config.PoolConfig{ID: "timing-test", Coin: "TEST"},
		logger:       logger.Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}
}

// fastBlockCoins returns coins with block times <= 30 seconds.
func fastBlockCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.BlockTimeSec <= 30 {
			coins = append(coins, c)
		}
	}
	return coins
}

// slowBlockCoins returns coins with block times >= 600 seconds.
func slowBlockCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.BlockTimeSec >= 600 {
			coins = append(coins, c)
		}
	}
	return coins
}

// mediumBlockCoins returns coins with block times between 31 and 599 seconds.
func mediumBlockCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.BlockTimeSec > 30 && c.BlockTimeSec < 600 {
			coins = append(coins, c)
		}
	}
	return coins
}

// =============================================================================
// RISK VECTOR 1: Fast Block Coins (DGB 15s, DGB-SCRYPT 15s, FBTC 30s)
// =============================================================================
//
// Fast block coins accumulate confirmations rapidly. With 15-second blocks,
// 100 confirmations arrive in ~25 minutes. This tests that rapid confirmation
// accumulation does not confuse the processor's progress tracking.

// TestSOLO_Timing_FastCoins_RapidConfirmationAccumulation verifies that for
// fast-block coins (DGB 15s, DGB-SCRYPT 15s), running 100+ update cycles
// with incrementing chain height correctly tracks progress and reaches
// confirmation without losing the block.
//
// Risk vector: rapid confirmation accumulation
// Algorithm:   sha256d / scrypt (varies by coin)
// Block interval: 15-30s
func TestSOLO_Timing_FastCoins_RapidConfirmationAccumulation(t *testing.T) {
	t.Parallel()

	for _, coin := range fastBlockCoins() {
		coin := coin // capture range variable
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			// Simulate a block found at height 20,000,000 (realistic for DGB)
			blockHeight := uint64(20_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0) // 0 = use default maturity

			// Simulate 100+ blocks arriving in rapid succession.
			// Each cycle, the chain height advances by 1 (one new block mined).
			// For a 15s-block coin, this represents ~25 minutes of real time.
			// Once at maturity, hold the tip stable so the stability window can complete.
			totalCycles := maturity + StabilityWindowChecks + 5 // extra margin
			stableHeight := blockHeight + uint64(maturity)
			stableTip := makeChainTip(stableHeight)
			for cycle := 0; cycle < totalCycles; cycle++ {
				chainHeight := blockHeight + uint64(cycle) + 1
				tip := makeChainTip(chainHeight)
				if cycle >= maturity {
					chainHeight = stableHeight
					tip = stableTip
				}
				rpc.setChainTip(chainHeight, tip)

				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Verify the block was confirmed, not lost or orphaned
			updates := store.getStatusUpdates()
			var lastStatus string
			var lastProgress float64
			for _, u := range updates {
				if u.Height == blockHeight {
					lastStatus = u.Status
					lastProgress = u.Progress
				}
			}

			if lastStatus != StatusConfirmed {
				t.Errorf("[%s] expected block to be confirmed after %d rapid cycles, got status=%q progress=%.2f",
					coin.Symbol, totalCycles, lastStatus, lastProgress)
			}

			// Verify no orphan updates were recorded (block hash always matched)
			orphanUpdates := store.getOrphanUpdates()
			for _, o := range orphanUpdates {
				if o.Height == blockHeight && o.MismatchCount > 0 {
					t.Errorf("[%s] unexpected orphan mismatch count %d for correctly-hashed block",
						coin.Symbol, o.MismatchCount)
				}
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 2: Slow Block Coins (BTC 600s, BCH 600s, CAT 600s)
// =============================================================================
//
// Slow block coins take ~16.7 hours to reach 100 confirmations (600s * 100).
// The block stays pending for many processing cycles before reaching maturity.
// This tests that patience is respected: no premature orphaning occurs.

// TestSOLO_Timing_SlowCoins_LongPendingNoPrematureOrphan verifies that for
// slow-block coins (BTC 600s, BCH 600s, CAT 600s), a block that stays
// pending for many cycles is never prematurely orphaned. The processor must
// patiently wait for confirmations to accumulate.
//
// Risk vector: premature orphaning of slow-confirming blocks
// Algorithm:   sha256d / scrypt (varies by coin)
// Block interval: 600s
func TestSOLO_Timing_SlowCoins_LongPendingNoPrematureOrphan(t *testing.T) {
	t.Parallel()

	for _, coin := range slowBlockCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(800_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0)

			// Simulate many processing cycles where the chain barely advances.
			// With 10-minute blocks and 10-minute processing interval, each cycle
			// adds roughly 1 confirmation. Run cycles for half of maturity to
			// verify the block stays pending and is never orphaned.
			halfMaturity := maturity / 2
			for cycle := 0; cycle < halfMaturity; cycle++ {
				chainHeight := blockHeight + uint64(cycle)
				tip := makeChainTip(chainHeight)
				rpc.setChainTip(chainHeight, tip)

				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block must still be pending, NOT orphaned
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusOrphaned {
					t.Fatalf("[%s] BLOCK LOSS: block prematurely orphaned at cycle %d (only %d/%d confirmations)",
						coin.Symbol, halfMaturity, halfMaturity, maturity)
				}
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					t.Fatalf("[%s] block should NOT be confirmed at %d/%d confirmations",
						coin.Symbol, halfMaturity, maturity)
				}
			}

			// Verify progress is approximately halfMaturity/maturity
			var lastProgress float64
			for _, u := range updates {
				if u.Height == blockHeight {
					lastProgress = u.Progress
				}
			}
			expectedProgress := float64(halfMaturity-1) / float64(maturity)
			if math.Abs(lastProgress-expectedProgress) > 0.05 {
				t.Errorf("[%s] expected progress ~%.2f, got %.2f",
					coin.Symbol, expectedProgress, lastProgress)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 3: Medium Block Coins (DOGE 60s, SYS 60s, LTC 150s)
// =============================================================================
//
// Medium block coins represent the standard confirmation path. With 60-second
// blocks, 100 confirmations arrive in ~100 minutes. This tests the normal
// happy-path progression from pending through stability window to confirmed.

// TestSOLO_Timing_MediumCoins_StandardProgression verifies that medium-block
// coins progress correctly from 0% to confirmed through the full lifecycle:
// pending -> maturity reached -> stability window -> confirmed.
//
// Risk vector: incorrect progression tracking
// Algorithm:   sha256d / scrypt (varies by coin)
// Block interval: 60-150s
func TestSOLO_Timing_MediumCoins_StandardProgression(t *testing.T) {
	t.Parallel()

	for _, coin := range mediumBlockCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(5_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0)

			// Phase 1: Advance to maturity
			// Use a stable tip for the maturity phase so the stability window works
			stableTip := fmt.Sprintf("%064x", blockHeight+uint64(maturity)+50)
			stableHeight := blockHeight + uint64(maturity) + 50
			rpc.setChainTip(stableHeight, stableTip)

			// Run StabilityWindowChecks cycles at maturity to pass stability window
			for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block should be confirmed
			updates := store.getStatusUpdates()
			var lastStatus string
			for _, u := range updates {
				if u.Height == blockHeight {
					lastStatus = u.Status
				}
			}

			if lastStatus != StatusConfirmed {
				t.Errorf("[%s] expected confirmed after standard progression, got %q",
					coin.Symbol, lastStatus)
			}

			// Verify progress was 1.0 at confirmation
			var confirmedProgress float64
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					confirmedProgress = u.Progress
				}
			}
			if confirmedProgress != 1.0 {
				t.Errorf("[%s] expected progress 1.0 at confirmation, got %.2f",
					coin.Symbol, confirmedProgress)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 4: Block Interval vs Maturity
// =============================================================================
//
// For DGB with 15-second blocks, 100 confirmations = 25 minutes of real time.
// For BTC with 600-second blocks, 100 confirmations = ~16.7 hours.
// This tests that the processor correctly handles blocks at exactly the
// maturity boundary regardless of the underlying block interval.

// TestSOLO_Timing_AllCoins_MaturityBoundaryExact verifies that for every
// supported coin, a block at exactly the maturity boundary is handled
// correctly: it enters the stability window but does NOT confirm until
// the stability window is satisfied.
//
// Risk vector: off-by-one at maturity boundary
// Coins: all 13 supported coins
// Block interval: 15s - 600s
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_AllCoins_MaturityBoundaryExact(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(10_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			// Chain height is exactly at maturity boundary
			chainHeight := blockHeight + uint64(maturity)
			tip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0)

			// Run exactly ONE cycle at the maturity boundary
			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", coin.Symbol, err)
			}

			// Block should NOT be confirmed yet (stability window = 1/3)
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					t.Fatalf("[%s] PREMATURE CONFIRMATION: block confirmed after 1 cycle "+
						"(need %d stability checks)", coin.Symbol, StabilityWindowChecks)
				}
			}

			// But progress should be 1.0 (confirmations >= maturity)
			var lastProgress float64
			for _, u := range updates {
				if u.Height == blockHeight {
					lastProgress = u.Progress
				}
			}
			if lastProgress != 1.0 {
				t.Errorf("[%s] expected progress 1.0 at exact maturity, got %.2f",
					coin.Symbol, lastProgress)
			}

			// Stability counter should have been incremented to 1
			stabilityUpdates := store.getStabilityUpdates()
			var stabilityCount int
			for _, s := range stabilityUpdates {
				if s.Height == blockHeight {
					stabilityCount = s.StabilityCount
				}
			}
			if stabilityCount != 1 {
				t.Errorf("[%s] expected stability count 1 after first cycle at maturity, got %d",
					coin.Symbol, stabilityCount)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 5: All Coins at Maturity Edge
// =============================================================================
//
// Tests the boundary between maturity-1 (stays pending) and maturity (enters
// stability window) for every supported coin.

// TestSOLO_Timing_AllCoins_MaturityMinusOne_StaysPending verifies that for
// every coin, a block at exactly maturity-1 confirmations remains pending
// and does NOT enter the stability window.
//
// Risk vector: off-by-one confirms block one confirmation too early
// Coins: all 13 supported coins
// Block interval: 15s - 600s
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_AllCoins_MaturityMinusOne_StaysPending(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(15_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			// Chain height is maturity - 1 confirmations
			chainHeight := blockHeight + uint64(maturity) - 1
			tip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0)

			// Run multiple cycles at maturity-1 (chain doesn't advance)
			for cycle := 0; cycle < StabilityWindowChecks+2; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block must NOT be confirmed (maturity-1 < maturity)
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					t.Fatalf("[%s] BLOCK CONFIRMED TOO EARLY: confirmed at maturity-1 (%d/%d)",
						coin.Symbol, maturity-1, maturity)
				}
			}

			// Stability window should NOT have been entered
			stabilityUpdates := store.getStabilityUpdates()
			for _, s := range stabilityUpdates {
				if s.Height == blockHeight {
					t.Fatalf("[%s] stability window entered at maturity-1 (count=%d)",
						coin.Symbol, s.StabilityCount)
				}
			}

			// Progress should be (maturity-1)/maturity = 0.99
			var lastProgress float64
			for _, u := range updates {
				if u.Height == blockHeight {
					lastProgress = u.Progress
				}
			}
			expectedProgress := float64(maturity-1) / float64(maturity)
			if math.Abs(lastProgress-expectedProgress) > 0.01 {
				t.Errorf("[%s] expected progress %.4f at maturity-1, got %.4f",
					coin.Symbol, expectedProgress, lastProgress)
			}
		})
	}
}

// TestSOLO_Timing_AllCoins_ExactMaturity_EntersStabilityWindow verifies that
// for every coin, a block at exactly maturity confirmations enters the
// stability window and eventually confirms after StabilityWindowChecks cycles.
//
// Risk vector: maturity boundary not entering stability window
// Coins: all 13 supported coins
// Block interval: 15s - 600s
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_AllCoins_ExactMaturity_EntersStabilityWindow(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(18_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			chainHeight := blockHeight + uint64(maturity)
			tip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0)

			// Run exactly StabilityWindowChecks cycles at maturity
			for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block should now be confirmed
			confirmed := store.hasStatusUpdate(blockHeight, StatusConfirmed)
			if !confirmed {
				t.Errorf("[%s] block should be confirmed after %d stability cycles at exact maturity",
					coin.Symbol, StabilityWindowChecks)
			}

			// Block should NOT be orphaned
			orphaned := store.hasStatusUpdate(blockHeight, StatusOrphaned)
			if orphaned {
				t.Fatalf("[%s] BLOCK LOSS: block orphaned at exact maturity boundary",
					coin.Symbol)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 6: Rapid Successive Blocks Found
// =============================================================================
//
// For fast-block coins, a solo miner may find multiple blocks in quick
// succession. All blocks must be tracked independently without any being
// silently dropped.

// TestSOLO_Timing_FastCoins_RapidSuccessiveBlocks verifies that when 5 blocks
// are found in rapid succession for a fast-block coin, all 5 are tracked
// independently and none are silently lost.
//
// Risk vector: blocks lost when multiple found in rapid succession
// Coins: fast-block coins (DGB 15s, DGB-SCRYPT 15s, FBTC 30s)
// Block interval: 15-30s
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_FastCoins_RapidSuccessiveBlocks(t *testing.T) {
	t.Parallel()

	for _, coin := range fastBlockCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			baseHeight := uint64(25_000_000)
			numBlocks := 5
			maturity := DefaultBlockMaturityConfirmations

			store := newMockBlockStore()
			rpc := newMockDaemonRPC()

			// Create 5 successive pending blocks (found within seconds of each other)
			blockHeights := make([]uint64, numBlocks)
			for i := 0; i < numBlocks; i++ {
				height := baseHeight + uint64(i)
				blockHeights[i] = height
				hash := makeBlockHash(coin.Symbol, height)
				store.addPendingBlock(makePendingBlock(coin.Symbol, height))
				rpc.setBlockHash(height, hash)
			}

			proc := newTimingProcessor(store, rpc, 0)

			// Advance the chain well past maturity for all blocks.
			// The chain tip must be far enough that even the earliest block
			// has >= maturity confirmations.
			chainHeight := baseHeight + uint64(numBlocks) + uint64(maturity) + 50
			stableTip := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, stableTip)

			// Run StabilityWindowChecks cycles to confirm all blocks
			for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// ALL 5 blocks must be confirmed
			for i, height := range blockHeights {
				confirmed := store.hasStatusUpdate(height, StatusConfirmed)
				if !confirmed {
					t.Errorf("[%s] BLOCK LOSS: block %d at height %d was not confirmed "+
						"(silent loss of rapid successive block)", coin.Symbol, i, height)
				}

				orphaned := store.hasStatusUpdate(height, StatusOrphaned)
				if orphaned {
					t.Errorf("[%s] BLOCK LOSS: block %d at height %d was orphaned "+
						"(should be confirmed)", coin.Symbol, i, height)
				}
			}

			// Verify each block got independent progress tracking
			updates := store.getStatusUpdates()
			confirmedCount := 0
			for _, u := range updates {
				for _, h := range blockHeights {
					if u.Height == h && u.Status == StatusConfirmed {
						confirmedCount++
						break
					}
				}
			}
			if confirmedCount < numBlocks {
				t.Errorf("[%s] expected %d confirmed blocks, found %d in status updates",
					coin.Symbol, numBlocks, confirmedCount)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 7: Per-Coin Maturity Differences
// =============================================================================
//
// Some deployments may configure custom BlockMaturity values per coin.
// For example, a DGB pool might use 240 (1 hour) instead of the default 100.
// This tests that custom maturity is respected.

// TestSOLO_Timing_CustomMaturity_VsDefault verifies that a custom
// BlockMaturity configuration is respected over the default. A block that
// would be confirmed with default maturity should remain pending with a
// higher custom maturity.
//
// Risk vector: custom maturity ignored, using default instead
// Coins: all 13 supported coins
// Block interval: varies
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_CustomMaturity_VsDefault(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(30_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			customMaturity := 200 // Higher than default 100

			// Chain height is between default maturity and custom maturity
			// (default=100, custom=200, so use 150 confirmations)
			chainHeight := blockHeight + 150
			tip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, customMaturity)

			// Run enough cycles for stability window (if it were at maturity)
			for cycle := 0; cycle < StabilityWindowChecks+2; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block must NOT be confirmed (150 < 200 custom maturity)
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					t.Fatalf("[%s] PREMATURE CONFIRMATION: block confirmed at 150/%d confirmations "+
						"(custom maturity ignored!)", coin.Symbol, customMaturity)
				}
			}

			// Verify progress is 150/200 = 0.75
			var lastProgress float64
			for _, u := range updates {
				if u.Height == blockHeight {
					lastProgress = u.Progress
				}
			}
			expectedProgress := 150.0 / float64(customMaturity)
			if math.Abs(lastProgress-expectedProgress) > 0.01 {
				t.Errorf("[%s] expected progress %.2f with custom maturity %d, got %.2f",
					coin.Symbol, expectedProgress, customMaturity, lastProgress)
			}

			// Stability window should NOT have been entered
			stabilityUpdates := store.getStabilityUpdates()
			for _, s := range stabilityUpdates {
				if s.Height == blockHeight {
					t.Fatalf("[%s] stability window entered before custom maturity reached (count=%d)",
						coin.Symbol, s.StabilityCount)
				}
			}
		})
	}
}

// TestSOLO_Timing_CustomMaturity_ConfirmsAtCustomThreshold verifies that
// a block confirms at the custom maturity threshold, not the default.
//
// Risk vector: block never confirms because custom maturity is ignored
// Coins: all 13 supported coins
// Block interval: varies
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_CustomMaturity_ConfirmsAtCustomThreshold(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(32_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			customMaturity := 50 // Lower than default 100

			// Chain height at exactly custom maturity
			chainHeight := blockHeight + uint64(customMaturity)
			tip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, customMaturity)

			// Run StabilityWindowChecks cycles
			for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block should be confirmed at custom maturity
			confirmed := store.hasStatusUpdate(blockHeight, StatusConfirmed)
			if !confirmed {
				t.Errorf("[%s] block should be confirmed at custom maturity %d after stability window",
					coin.Symbol, customMaturity)
			}
		})
	}
}

// TestSOLO_Timing_DefaultMaturity_UsedWhenZero verifies that when
// BlockMaturity config is 0, the default maturity (100) is used.
//
// Risk vector: zero config causes zero maturity = instant confirmation
// Coins: representative subset
// Block interval: varies
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_DefaultMaturity_UsedWhenZero(t *testing.T) {
	t.Parallel()

	// Test with a representative fast, medium, and slow coin
	representativeCoins := []testCoinConfig{
		{Symbol: "DGB", Algorithm: "sha256d", BlockTimeSec: 15},
		{Symbol: "DOGE", Algorithm: "scrypt", BlockTimeSec: 60},
		{Symbol: "BTC", Algorithm: "sha256d", BlockTimeSec: 600},
	}

	for _, coin := range representativeCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(35_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)

			// Only 10 confirmations - should NOT be enough for default maturity (100)
			chainHeight := blockHeight + 10
			tip := makeChainTip(chainHeight)

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setChainTip(chainHeight, tip)
			rpc.setBlockHash(blockHeight, blockHash)

			// BlockMaturity = 0 → should use DefaultBlockMaturityConfirmations (100)
			proc := newTimingProcessor(store, rpc, 0)

			for cycle := 0; cycle < StabilityWindowChecks+2; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block must NOT be confirmed (10 < default 100)
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusConfirmed {
					t.Fatalf("[%s] CRITICAL: block confirmed with only 10 confirmations "+
						"(zero BlockMaturity treated as zero, not default!)", coin.Symbol)
				}
			}

			// Progress should be 10/100 = 0.10
			var lastProgress float64
			for _, u := range updates {
				if u.Height == blockHeight {
					lastProgress = u.Progress
				}
			}
			expectedProgress := 10.0 / float64(DefaultBlockMaturityConfirmations)
			if math.Abs(lastProgress-expectedProgress) > 0.01 {
				t.Errorf("[%s] expected progress %.2f with default maturity, got %.2f",
					coin.Symbol, expectedProgress, lastProgress)
			}
		})
	}
}

// =============================================================================
// ADDITIONAL: DGB-specific block interval vs maturity timing
// =============================================================================

// TestSOLO_Timing_DGB_15sBlocks_100Confirmations verifies the specific DGB
// scenario: 15-second blocks need 100 confirmations (= 25 minutes). This
// simulates the full lifecycle from block found to confirmed, advancing
// the chain by 1 block per cycle (representing 15-second real-time intervals).
//
// Risk vector: DGB's fast blocks cause confirmation to happen before processor
//              has time to track all intermediate states
// Coin: DGB
// Block interval: 15s
// Algorithm: sha256d
func TestSOLO_Timing_DGB_15sBlocks_100Confirmations(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(20_500_000) // Realistic DGB height
	blockHash := makeBlockHash("DGB", blockHeight)
	maturity := DefaultBlockMaturityConfirmations // 100

	store := newMockBlockStore()
	store.addPendingBlock(makePendingBlock("DGB", blockHeight))

	rpc := newMockDaemonRPC()
	rpc.setBlockHash(blockHeight, blockHash)

	proc := newTimingProcessor(store, rpc, 0)

	// Track progress milestones
	type milestone struct {
		confirmations uint64
		progress      float64
	}
	var milestones []milestone

	// Simulate 100 + StabilityWindowChecks + 5 cycles (one block per cycle)
	// Once at maturity, hold the tip stable so the stability window can complete.
	totalCycles := maturity + StabilityWindowChecks + 5
	stableHeight := blockHeight + uint64(maturity)
	stableTip := makeChainTip(stableHeight)
	for cycle := 0; cycle < totalCycles; cycle++ {
		chainHeight := blockHeight + uint64(cycle) + 1
		tip := makeChainTip(chainHeight)
		if cycle >= maturity {
			chainHeight = stableHeight
			tip = stableTip
		}
		rpc.setChainTip(chainHeight, tip)

		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("DGB cycle %d: unexpected error: %v", cycle, err)
		}

		// Record milestone progress at key points
		if cycle == 0 || cycle == 25 || cycle == 50 || cycle == 75 || cycle == 99 || cycle == 100 {
			updates := store.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight {
					milestones = append(milestones, milestone{
						confirmations: uint64(cycle),
						progress:      u.Progress,
					})
				}
			}
		}
	}

	// Verify block reached confirmed status
	confirmed := store.hasStatusUpdate(blockHeight, StatusConfirmed)
	if !confirmed {
		t.Fatal("DGB block not confirmed after 100+ confirmations and stability window")
	}

	// Verify no orphan was ever recorded
	orphaned := store.hasStatusUpdate(blockHeight, StatusOrphaned)
	if orphaned {
		t.Fatal("BLOCK LOSS: DGB block orphaned during rapid 15s block accumulation")
	}

	// Verify progress was monotonically increasing (no regression)
	var prevProgress float64
	updates := store.getStatusUpdates()
	for _, u := range updates {
		if u.Height == blockHeight && u.Status == StatusPending {
			if u.Progress < prevProgress {
				t.Errorf("DGB progress regression: %.4f -> %.4f", prevProgress, u.Progress)
			}
			prevProgress = u.Progress
		}
	}
}

// =============================================================================
// ADDITIONAL: BTC-specific slow block patience test
// =============================================================================

// TestSOLO_Timing_BTC_600sBlocks_ExtendedPendingPeriod verifies that BTC's
// 10-minute blocks do not cause the processor to prematurely orphan a block
// that is simply waiting for slow confirmations. Simulates a realistic
// scenario where the processor runs many cycles while the chain barely moves.
//
// Risk vector: processor misinterprets slow confirmation as stuck/dead block
// Coin: BTC
// Block interval: 600s
// Algorithm: sha256d
func TestSOLO_Timing_BTC_600sBlocks_ExtendedPendingPeriod(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(850_000) // Realistic BTC height
	blockHash := makeBlockHash("BTC", blockHeight)

	store := newMockBlockStore()
	store.addPendingBlock(makePendingBlock("BTC", blockHeight))

	rpc := newMockDaemonRPC()
	rpc.setBlockHash(blockHeight, blockHash)

	proc := newTimingProcessor(store, rpc, 0)

	// Simulate 200 processing cycles with slow chain advancement.
	// With 10-minute blocks and 1-minute processing interval, the chain
	// advances roughly 1 block every 10 cycles.
	// After 200 cycles (~200 min = ~3.3 hours), chain has ~20 new blocks.
	for cycle := 0; cycle < 200; cycle++ {
		// Chain advances 1 block every 10 cycles (600s blocks / 60s interval)
		chainHeight := blockHeight + uint64(cycle/10)
		tip := makeChainTip(chainHeight)
		rpc.setChainTip(chainHeight, tip)

		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("BTC cycle %d: unexpected error: %v", cycle, err)
		}
	}

	// After 200 cycles, chain should have advanced ~20 blocks.
	// 20 < 100 maturity, so block should still be pending.
	updates := store.getStatusUpdates()
	for _, u := range updates {
		if u.Height == blockHeight && u.Status == StatusOrphaned {
			t.Fatal("BLOCK LOSS: BTC block orphaned during slow confirmation period")
		}
		if u.Height == blockHeight && u.Status == StatusConfirmed {
			t.Fatal("BTC block confirmed too early (only ~20/100 confirmations)")
		}
	}

	// Verify progress is approximately 20/100 = 0.20
	// At cycle 200: chainHeight = blockHeight + 200/10 = blockHeight + 20
	// But tip changes each cycle; the last cycle has chain at blockHeight + 19.
	// The progress calculation does (chainHeight - blockHeight) / maturity.
	var lastProgress float64
	for _, u := range updates {
		if u.Height == blockHeight {
			lastProgress = u.Progress
		}
	}
	// chainHeight at cycle 199 = blockHeight + 199/10 = blockHeight + 19
	expectedProgress := 19.0 / float64(DefaultBlockMaturityConfirmations)
	if math.Abs(lastProgress-expectedProgress) > 0.02 {
		t.Errorf("BTC expected progress ~%.2f after slow period, got %.2f",
			expectedProgress, lastProgress)
	}
}

// =============================================================================
// ADDITIONAL: Cross-coin maturity timing sanity check
// =============================================================================

// TestSOLO_Timing_AllCoins_NoBlockLostDuringFullLifecycle is the comprehensive
// cross-coin test that verifies the complete block lifecycle (found -> pending
// -> maturity reached -> stability window -> confirmed) for every supported
// coin. This is the final safety net to catch any coin-specific timing issue.
//
// Risk vector: any coin-specific behavior causing silent block loss
// Coins: all 13 supported coins
// Block interval: 15s - 600s
// Algorithm: sha256d / scrypt
func TestSOLO_Timing_AllCoins_NoBlockLostDuringFullLifecycle(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			blockHeight := uint64(40_000_000)
			blockHash := makeBlockHash(coin.Symbol, blockHeight)
			maturity := DefaultBlockMaturityConfirmations

			store := newMockBlockStore()
			store.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.setBlockHash(blockHeight, blockHash)

			proc := newTimingProcessor(store, rpc, 0)

			// Phase 1: pre-maturity - advance chain gradually
			for confs := uint64(0); confs < uint64(maturity); confs++ {
				chainHeight := blockHeight + confs
				tip := makeChainTip(chainHeight)
				rpc.setChainTip(chainHeight, tip)

				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] pre-maturity cycle %d: %v", coin.Symbol, confs, err)
				}

				// Verify block is never orphaned during pre-maturity
				if store.hasStatusUpdate(blockHeight, StatusOrphaned) {
					t.Fatalf("[%s] BLOCK LOSS: orphaned at %d/%d confirmations",
						coin.Symbol, confs, maturity)
				}

				// Verify block is never confirmed during pre-maturity
				if store.hasStatusUpdate(blockHeight, StatusConfirmed) {
					t.Fatalf("[%s] PREMATURE: confirmed at %d/%d confirmations",
						coin.Symbol, confs, maturity)
				}
			}

			// Phase 2: at maturity - run stability window with stable tip
			stableChainHeight := blockHeight + uint64(maturity) + 10
			stableTip := makeChainTip(stableChainHeight)
			rpc.setChainTip(stableChainHeight, stableTip)

			for cycle := 0; cycle < StabilityWindowChecks; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] stability cycle %d: %v", coin.Symbol, cycle, err)
				}
			}

			// Phase 3: verify confirmed
			confirmed := store.hasStatusUpdate(blockHeight, StatusConfirmed)
			if !confirmed {
				t.Errorf("[%s] block NOT confirmed after full lifecycle "+
					"(maturity=%d, stability=%d)",
					coin.Symbol, maturity, StabilityWindowChecks)
			}

			// Verify never orphaned
			orphaned := store.hasStatusUpdate(blockHeight, StatusOrphaned)
			if orphaned {
				t.Fatalf("[%s] BLOCK LOSS: block orphaned during full lifecycle test",
					coin.Symbol)
			}
		})
	}
}
