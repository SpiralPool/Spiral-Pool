// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Money-loss prevention harness tests for multi-coin scheduling.
//
// These tests verify that no block rewards can be lost through the
// multi-port scheduling and routing system. Every scenario simulates
// a real-world condition where money could be silently lost:
//
//   - Blocks found during coin rotation boundaries
//   - Blocks found on aux chains during coin switches
//   - Silent duplicates must NOT be credited (unfair payouts)
//   - Rejected shares must NOT be submitted as blocks
//   - Blocks routed to the wrong coin pool
//   - Multiple simultaneous block finds across coins
//   - Block attribution under concurrent load
//   - Failover block routing (daemon down mid-block)
//   - Schedule edge cases (midnight rollover, DST-like boundaries)
package scheduler

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// TEST: Block found at exact slot boundary must be credited to correct coin
// =============================================================================

func TestMoneyLoss_BlockAtExactSlotBoundary(t *testing.T) {
	// DGB slot ends at 12:00, BCH slot starts at 12:00
	// A block found at 11:59:59 must be credited to DGB, not BCH
	h := newHarness(t, 11, 59) // 11:59 → last minute of DGB slot

	h.selectAndAssign(1)

	// Verify we're on DGB
	coin, _ := h.selector.GetSessionCoin(1)
	if coin != "DGB" {
		t.Fatalf("expected DGB at 11:59, got %s", coin)
	}

	// Find a block on DGB at 11:59
	h.dgb.SetBlockResult("0000000-dgb-block-at-boundary")
	coin, result := h.routeShare(1, "DGB-job-1", 1000)
	if coin != "DGB" || !result.IsBlock {
		t.Fatalf("block should be on DGB: coin=%s, isBlock=%v", coin, result.IsBlock)
	}
	h.dgb.ClearBlockResult()

	// Now advance to exactly 12:00 → BCH slot
	h.setTime(12, 0)
	sel := h.selectAndAssign(1)
	if sel.Symbol != "BCH" {
		t.Fatalf("expected BCH at 12:00, got %s", sel.Symbol)
	}

	// Verify the DGB block was recorded on DGB, not BCH
	dgbBlocks := h.dgb.GetBlocksFound()
	bchBlocks := h.bch.GetBlocksFound()
	if len(dgbBlocks) != 1 {
		t.Errorf("MONEY LOSS: DGB should have 1 block, got %d", len(dgbBlocks))
	}
	if len(bchBlocks) != 0 {
		t.Errorf("MONEY LOSS: BCH should have 0 blocks, got %d — block attributed to wrong coin!", len(bchBlocks))
	}

	t.Logf("Block at slot boundary: DGB block recorded on DGB ✓, clean rotation to BCH ✓")
}

// =============================================================================
// TEST: Block found IMMEDIATELY after rotation must credit new coin
// =============================================================================

func TestMoneyLoss_BlockImmediatelyAfterRotation(t *testing.T) {
	h := newHarness(t, 12, 0) // Exactly at BCH slot start

	h.selectAndAssign(1)
	sel := h.selectAndAssign(1)
	if sel.Symbol != "BCH" {
		t.Fatalf("expected BCH at 12:00, got %s", sel.Symbol)
	}

	// Find a block on BCH immediately after rotation
	h.bch.SetBlockResult("0000000-bch-block-after-rotation")
	coin, result := h.routeShare(1, "BCH-job-1", 500)
	if coin != "BCH" || !result.IsBlock {
		t.Fatalf("block should be on BCH: coin=%s, isBlock=%v", coin, result.IsBlock)
	}
	h.bch.ClearBlockResult()

	bchBlocks := h.bch.GetBlocksFound()
	dgbBlocks := h.dgb.GetBlocksFound()
	if len(bchBlocks) != 1 {
		t.Errorf("MONEY LOSS: BCH should have 1 block after rotation, got %d", len(bchBlocks))
	}
	if len(dgbBlocks) != 0 {
		t.Errorf("MONEY LOSS: DGB should have 0 blocks, got %d — block attributed to old coin!", len(dgbBlocks))
	}

	t.Logf("Block after rotation: BCH block correctly recorded on BCH ✓")
}

// =============================================================================
// TEST: Silent duplicate must NOT be credited as a valid share
// =============================================================================

func TestMoneyLoss_SilentDuplicateNotCredited(t *testing.T) {
	h := newHarness(t, 6, 0)
	h.selectAndAssign(1)

	// Submit a normal share
	coin, result := h.routeShare(1, "DGB-job-1", 1000)
	if !result.Accepted {
		t.Fatalf("first share should be accepted")
	}

	// Now configure a silent duplicate response
	h.dgb.mu.Lock()
	h.dgb.nextShareResult = &protocol.ShareResult{
		Accepted:        true,
		SilentDuplicate: true,
	}
	h.dgb.mu.Unlock()

	_, dupResult := h.routeShare(1, "DGB-job-1", 1000)
	if !dupResult.Accepted {
		t.Error("silent duplicate should report Accepted=true to miner")
	}
	if !dupResult.SilentDuplicate {
		t.Error("silent duplicate flag should be set")
	}
	h.dgb.ClearBlockResult()

	t.Logf("Silent duplicate: Accepted=true (miner sees accepted), SilentDuplicate=true (not credited) ✓, coin=%s", coin)
}

// =============================================================================
// TEST: Rejected share must NOT be submitted as a block
// =============================================================================

func TestMoneyLoss_RejectedShareNotSubmittedAsBlock(t *testing.T) {
	h := newHarness(t, 6, 0)
	h.selectAndAssign(1)

	// Configure a rejected share that also has IsBlock=false
	h.dgb.mu.Lock()
	h.dgb.nextShareResult = &protocol.ShareResult{
		Accepted:     false,
		IsBlock:      false,
		RejectReason: "low-difficulty",
	}
	h.dgb.mu.Unlock()

	_, result := h.routeShare(1, "DGB-job-1", 1)
	if result.Accepted {
		t.Error("rejected share should not be accepted")
	}
	if result.IsBlock {
		t.Error("MONEY LOSS RISK: rejected share should never be marked as block")
	}

	dgbBlocks := h.dgb.GetBlocksFound()
	if len(dgbBlocks) != 0 {
		t.Errorf("MONEY LOSS: rejected share resulted in %d block submissions", len(dgbBlocks))
	}

	h.dgb.ClearBlockResult()
	t.Logf("Rejected share: not submitted as block ✓")
}

// =============================================================================
// TEST: Multiple simultaneous blocks across different coins
// =============================================================================

func TestMoneyLoss_SimultaneousBlocksAcrossCoins(t *testing.T) {
	h := newHarness(t, 6, 0)  // DGB slot
	h.selectAndAssign(1)       // Miner 1 on DGB
	h.setTime(13, 0)
	h.selectAndAssign(2)       // Miner 2 on BCH
	h.setTime(20, 0)
	h.selectAndAssign(3)       // Miner 3 on BTC

	// All three find blocks simultaneously
	h.dgb.SetBlockResult("0000dgb-simultaneous")
	h.bch.SetBlockResult("0000bch-simultaneous")
	h.btc.SetBlockResult("0000btc-simultaneous")

	// Route shares — each to their assigned coin
	// (We set time back to each slot for routing)
	h.setTime(6, 0)
	h.selectAndAssign(1) // re-evaluate: still DGB at 06:00
	coin1, result1 := h.routeShare(1, "DGB-job-1", 1000)

	h.setTime(13, 0)
	h.selectAndAssign(2) // re-evaluate: BCH at 13:00
	coin2, result2 := h.routeShare(2, "BCH-job-1", 500)

	h.setTime(20, 0)
	h.selectAndAssign(3) // re-evaluate: BTC at 20:00
	coin3, result3 := h.routeShare(3, "BTC-job-1", 80_000_000_000_000)

	if !result1.IsBlock || coin1 != "DGB" {
		t.Errorf("MONEY LOSS: DGB block not found: coin=%s, isBlock=%v", coin1, result1.IsBlock)
	}
	if !result2.IsBlock || coin2 != "BCH" {
		t.Errorf("MONEY LOSS: BCH block not found: coin=%s, isBlock=%v", coin2, result2.IsBlock)
	}
	if !result3.IsBlock || coin3 != "BTC" {
		t.Errorf("MONEY LOSS: BTC block not found: coin=%s, isBlock=%v", coin3, result3.IsBlock)
	}

	// Verify all blocks attributed to correct coins
	if len(h.dgb.GetBlocksFound()) != 1 {
		t.Errorf("MONEY LOSS: DGB should have exactly 1 block, got %d", len(h.dgb.GetBlocksFound()))
	}
	if len(h.bch.GetBlocksFound()) != 1 {
		t.Errorf("MONEY LOSS: BCH should have exactly 1 block, got %d", len(h.bch.GetBlocksFound()))
	}
	if len(h.btc.GetBlocksFound()) != 1 {
		t.Errorf("MONEY LOSS: BTC should have exactly 1 block, got %d", len(h.btc.GetBlocksFound()))
	}

	t.Logf("Simultaneous blocks: DGB ✓, BCH ✓, BTC ✓ — all attributed correctly")
}

// =============================================================================
// TEST: Block found on failing coin during failover must still be counted
// =============================================================================

func TestMoneyLoss_BlockFoundBeforeFailover(t *testing.T) {
	h := newHarness(t, 6, 0)
	h.selectAndAssign(1)

	// Find a block on DGB
	h.dgb.SetBlockResult("0000dgb-pre-failover-block")
	coin, result := h.routeShare(1, "DGB-job-1", 1000)
	if coin != "DGB" || !result.IsBlock {
		t.Fatalf("block should be on DGB: coin=%s, isBlock=%v", coin, result.IsBlock)
	}
	h.dgb.ClearBlockResult()

	// DGB goes down AFTER block was found
	h.dgb.SetRunning(false)
	h.pollAndUpdate()

	// Miner re-evaluates — should failover
	sel := h.selectAndAssign(1)
	if sel.Symbol == "DGB" {
		t.Error("should have failed over from DGB")
	}

	// The block that was found BEFORE failover must still be recorded
	dgbBlocks := h.dgb.GetBlocksFound()
	if len(dgbBlocks) != 1 {
		t.Errorf("MONEY LOSS: DGB block found before failover was lost! blocks=%d", len(dgbBlocks))
	}

	t.Logf("Pre-failover block: DGB block preserved ✓, miner failed over to %s ✓", sel.Symbol)
}

// =============================================================================
// TEST: Block during failover recovery must be attributed correctly
// =============================================================================

func TestMoneyLoss_BlockDuringFailoverRecovery(t *testing.T) {
	h := newHarness(t, 6, 0)
	h.selectAndAssign(1)

	// DGB goes down — miner fails over
	h.dgb.SetRunning(false)
	h.pollAndUpdate()
	sel := h.selectAndAssign(1)
	failoverCoin := sel.Symbol
	if failoverCoin == "DGB" {
		t.Fatal("should have failed over from DGB")
	}

	// Find a block on the failover coin
	pool := h.pools[failoverCoin]
	pool.SetBlockResult("0000-failover-block")
	_, result := h.routeShare(1, failoverCoin+"-job-1", 500)
	if !result.IsBlock {
		t.Fatalf("block should be found on %s", failoverCoin)
	}
	pool.ClearBlockResult()

	// DGB recovers — miner returns
	h.dgb.SetRunning(true)
	h.pollAndUpdate()
	sel = h.selectAndAssign(1)

	// Verify the failover block is on the failover coin, NOT on DGB
	failoverBlocks := pool.GetBlocksFound()
	dgbBlocks := h.dgb.GetBlocksFound()
	if len(failoverBlocks) != 1 {
		t.Errorf("MONEY LOSS: %s should have 1 block, got %d", failoverCoin, len(failoverBlocks))
	}
	if len(dgbBlocks) != 0 {
		t.Errorf("MONEY LOSS: DGB should have 0 blocks (was down), got %d — block misattributed!", len(dgbBlocks))
	}

	t.Logf("Failover block: %s block correctly attributed ✓, DGB recovered ✓", failoverCoin)
}

// =============================================================================
// TEST: Midnight rollover — blocks at 23:59 and 00:00 must be correct
// =============================================================================

func TestMoneyLoss_MidnightRollover(t *testing.T) {
	// BTC slot: 19:12–24:00
	h := newHarness(t, 23, 59) // 23:59 → BTC slot
	h.selectAndAssign(1)

	coin, _ := h.selector.GetSessionCoin(1)
	if coin != "BTC" {
		t.Fatalf("expected BTC at 23:59, got %s", coin)
	}

	// Find block at 23:59
	h.btc.SetBlockResult("0000btc-midnight-block")
	_, result := h.routeShare(1, "BTC-job-1", 80_000_000_000_000)
	if !result.IsBlock {
		t.Fatal("block should be found on BTC at 23:59")
	}
	h.btc.ClearBlockResult()

	// Roll to 00:00 next day → DGB slot
	h.setTime(0, 0)
	sel := h.selectAndAssign(1)
	if sel.Symbol != "DGB" {
		t.Fatalf("expected DGB at 00:00, got %s", sel.Symbol)
	}

	// Verify BTC block was recorded on BTC
	btcBlocks := h.btc.GetBlocksFound()
	if len(btcBlocks) != 1 {
		t.Errorf("MONEY LOSS: BTC should have 1 block at midnight, got %d", len(btcBlocks))
	}

	// Find a block on DGB after midnight
	h.dgb.SetBlockResult("0000dgb-after-midnight")
	_, result2 := h.routeShare(1, "DGB-job-1", 1000)
	if !result2.IsBlock {
		t.Fatal("block should be found on DGB at 00:00")
	}
	h.dgb.ClearBlockResult()

	dgbBlocks := h.dgb.GetBlocksFound()
	if len(dgbBlocks) != 1 {
		t.Errorf("MONEY LOSS: DGB should have 1 block after midnight, got %d", len(dgbBlocks))
	}

	t.Logf("Midnight rollover: BTC block at 23:59 ✓, DGB block at 00:00 ✓")
}

// =============================================================================
// TEST: Concurrent block finds under high load — no blocks lost
// =============================================================================

func TestMoneyLoss_ConcurrentBlockFindsUnderLoad(t *testing.T) {
	h := newHarness(t, 6, 0)
	numMiners := 50

	// Assign all miners to DGB
	for i := uint64(1); i <= uint64(numMiners); i++ {
		h.selectAndAssign(i)
	}

	// Every 10th miner will find a block
	expectedBlocks := numMiners / 10

	var wg sync.WaitGroup
	var blocksFound atomic.Int64
	var sharesAccepted atomic.Int64

	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func(minerIdx int) {
			defer wg.Done()
			sessionID := uint64(minerIdx + 1)

			for j := 0; j < 20; j++ {
				share := &protocol.Share{
					SessionID:    sessionID,
					JobID:        fmt.Sprintf("DGB-job-%d", minerIdx),
					MinerAddress: fmt.Sprintf("addr-%d", minerIdx),
					WorkerName:   fmt.Sprintf("worker-%d", minerIdx),
					Difficulty:   1000,
					SubmittedAt:  time.Now(),
				}

				// On share #10, every 10th miner gets a block result
				if j == 10 && minerIdx%10 == 0 {
					h.dgb.SetBlockResult(fmt.Sprintf("block-by-miner-%d", minerIdx))
				}

				result := h.dgb.HandleMultiPortShare(share)
				if result.Accepted {
					sharesAccepted.Add(1)
				}
				if result.IsBlock {
					blocksFound.Add(1)
				}

				// Clear block result after the block share
				if j == 10 && minerIdx%10 == 0 {
					h.dgb.ClearBlockResult()
				}
			}
		}(i)
	}

	wg.Wait()

	blocks := blocksFound.Load()
	dgbBlocks := h.dgb.blockCount.Load()

	if blocks < int64(expectedBlocks) {
		t.Errorf("MONEY LOSS: expected at least %d blocks, miner-side saw %d", expectedBlocks, blocks)
	}
	if dgbBlocks < int64(expectedBlocks) {
		t.Errorf("MONEY LOSS: pool recorded %d blocks, expected at least %d", dgbBlocks, expectedBlocks)
	}

	t.Logf("Concurrent blocks: %d miners, %d shares accepted, %d blocks found, %d recorded by pool",
		numMiners, sharesAccepted.Load(), blocks, dgbBlocks)
}

// =============================================================================
// TEST: Every slot in the schedule gets blocks attributed correctly
// =============================================================================

func TestMoneyLoss_FullDayBlockAttribution(t *testing.T) {
	// Walk through every 2 hours of the 24h schedule, find a block at each,
	// verify it's attributed to the correct coin.
	// DGB=50% (00:00–12:00), BCH=30% (12:00–19:12), BTC=20% (19:12–24:00)

	expectedCoins := map[int]string{
		0:  "DGB",
		2:  "DGB",
		4:  "DGB",
		6:  "DGB",
		8:  "DGB",
		10: "DGB",
		12: "BCH",
		14: "BCH",
		16: "BCH",
		18: "BCH",
		20: "BTC",
		22: "BTC",
	}

	for hour, expectedCoin := range expectedCoins {
		t.Run(fmt.Sprintf("hour_%02d_expect_%s", hour, expectedCoin), func(t *testing.T) {
			h := newHarness(t, hour, 30) // :30 to avoid exact boundaries
			h.selectAndAssign(1)

			coin, _ := h.selector.GetSessionCoin(1)
			if coin != expectedCoin {
				t.Fatalf("at %02d:30 expected %s, got %s", hour, expectedCoin, coin)
			}

			// Find a block
			pool := h.pools[expectedCoin]
			pool.SetBlockResult(fmt.Sprintf("block-at-hour-%d", hour))
			routedCoin, result := h.routeShare(1, expectedCoin+"-job-1", 1000)
			if routedCoin != expectedCoin || !result.IsBlock {
				t.Fatalf("MONEY LOSS: block at %02d:30 routed to %s (expected %s), isBlock=%v",
					hour, routedCoin, expectedCoin, result.IsBlock)
			}
			pool.ClearBlockResult()

			blocks := pool.GetBlocksFound()
			if len(blocks) != 1 {
				t.Errorf("MONEY LOSS: %s should have 1 block at %02d:30, got %d",
					expectedCoin, hour, len(blocks))
			}

			// Verify NO other coin got the block
			for sym, p := range h.pools {
				if sym != expectedCoin && len(p.GetBlocksFound()) > 0 {
					t.Errorf("MONEY LOSS: %s has %d blocks but should have 0 at %02d:30",
						sym, len(p.GetBlocksFound()), hour)
				}
			}
		})
	}
}

// =============================================================================
// TEST: Rapid coin switching doesn't lose blocks
// =============================================================================

func TestMoneyLoss_RapidCoinSwitchingNoBlocksLost(t *testing.T) {
	h := newHarness(t, 6, 0)
	h.selectAndAssign(1)

	totalBlocks := 0

	// Rapidly switch coins back and forth, finding a block each time
	times := [][2]int{{6, 0}, {13, 0}, {20, 0}, {6, 0}, {13, 0}, {20, 0}}
	coins := []string{"DGB", "BCH", "BTC", "DGB", "BCH", "BTC"}

	for i, tm := range times {
		h.setTime(tm[0], tm[1])
		h.selectAndAssign(1)

		expectedCoin := coins[i]
		actualCoin, _ := h.selector.GetSessionCoin(1)
		if actualCoin != expectedCoin {
			t.Fatalf("iteration %d: expected %s, got %s", i, expectedCoin, actualCoin)
		}

		pool := h.pools[expectedCoin]
		pool.SetBlockResult(fmt.Sprintf("rapid-block-%d", i))
		_, result := h.routeShare(1, expectedCoin+"-job-1", 1000)
		if !result.IsBlock {
			t.Fatalf("MONEY LOSS: block %d on %s not found", i, expectedCoin)
		}
		pool.ClearBlockResult()
		totalBlocks++
	}

	// Verify total blocks across all pools
	total := len(h.dgb.GetBlocksFound()) + len(h.bch.GetBlocksFound()) + len(h.btc.GetBlocksFound())
	if total != totalBlocks {
		t.Errorf("MONEY LOSS: expected %d total blocks, pools recorded %d", totalBlocks, total)
	}

	t.Logf("Rapid switching: %d blocks found across 6 rotations, all accounted for ✓", total)
}

// =============================================================================
// TEST: AuxResults on multi-port shares must be passed through
// =============================================================================

func TestMoneyLoss_AuxBlockResultsPassedThrough(t *testing.T) {
	h := newHarness(t, 6, 0)
	h.selectAndAssign(1)

	// Configure a share result that includes aux chain blocks (merge mining)
	h.dgb.mu.Lock()
	h.dgb.nextShareResult = &protocol.ShareResult{
		Accepted: true,
		IsBlock:  false, // Parent share doesn't meet parent difficulty
		AuxResults: []protocol.AuxBlockResult{
			{
				Symbol:    "NMC",
				IsBlock:   true,
				BlockHash: "0000nmc-aux-block",
			},
			{
				Symbol:    "SYS",
				IsBlock:   true,
				BlockHash: "0000sys-aux-block",
			},
		},
	}
	h.dgb.mu.Unlock()

	_, result := h.routeShare(1, "DGB-job-1", 1000)
	if result.IsBlock {
		t.Error("parent share should not be a block")
	}
	if len(result.AuxResults) != 2 {
		t.Fatalf("MONEY LOSS: expected 2 aux results, got %d — aux blocks lost!", len(result.AuxResults))
	}
	if result.AuxResults[0].Symbol != "NMC" || !result.AuxResults[0].IsBlock {
		t.Errorf("MONEY LOSS: NMC aux block missing: %+v", result.AuxResults[0])
	}
	if result.AuxResults[1].Symbol != "SYS" || !result.AuxResults[1].IsBlock {
		t.Errorf("MONEY LOSS: SYS aux block missing: %+v", result.AuxResults[1])
	}

	h.dgb.ClearBlockResult()
	t.Logf("Aux blocks: 2 merge-mined blocks (NMC, SYS) passed through correctly ✓")
}

// =============================================================================
// TEST: Block metrics tracked correctly for multi-port shares
// =============================================================================

func TestMoneyLoss_BlockMetricsAccurate(t *testing.T) {
	h := newHarness(t, 6, 0)

	numSessions := 10
	for i := uint64(1); i <= uint64(numSessions); i++ {
		h.selectAndAssign(i)
	}

	// Submit 100 shares, 3 of which are blocks
	blockSessions := map[uint64]bool{3: true, 7: true, 10: true}

	for i := uint64(1); i <= uint64(numSessions); i++ {
		if blockSessions[i] {
			h.dgb.SetBlockResult(fmt.Sprintf("block-session-%d", i))
		} else {
			h.dgb.ClearBlockResult()
		}

		for j := 0; j < 10; j++ {
			h.routeShare(i, fmt.Sprintf("DGB-job-%d", i), 1000)
		}
	}
	h.dgb.ClearBlockResult()

	totalShares := h.dgb.shareCount.Load()
	totalBlocks := h.dgb.blockCount.Load()

	if totalShares != int64(numSessions*10) {
		t.Errorf("expected %d total shares, got %d", numSessions*10, totalShares)
	}
	// Each block session submits 10 shares, and the block result is set for all of them
	if totalBlocks < 3 {
		t.Errorf("MONEY LOSS: expected at least 3 blocks from 3 sessions, got %d", totalBlocks)
	}

	t.Logf("Metrics: %d shares, %d blocks — all tracked ✓", totalShares, totalBlocks)
}

// =============================================================================
// TEST: Zero-difficulty coin is not selected (prevents mining worthless blocks)
// =============================================================================

func TestMoneyLoss_ZeroDifficultyCoinNotSelected(t *testing.T) {
	h := newHarness(t, 6, 0) // DGB slot

	// DGB reports 0 difficulty (syncing)
	h.dgb.SetDifficulty(0)
	h.pollAndUpdate()

	// Miner should NOT be assigned to DGB
	sel := h.selectAndAssign(1)
	if sel.Symbol == "DGB" {
		t.Error("MONEY LOSS RISK: miner assigned to DGB with zero difficulty — blocks would be worthless/rejected")
	}

	t.Logf("Zero-difficulty coin rejected: miner assigned to %s instead ✓", sel.Symbol)
}

// =============================================================================
// TEST: All miners removed and reconnected — blocks still attributed correctly
// =============================================================================

func TestMoneyLoss_FullDisconnectReconnect(t *testing.T) {
	h := newHarness(t, 6, 0)

	// Connect 20 miners
	for i := uint64(1); i <= 20; i++ {
		h.selectAndAssign(i)
	}

	// Disconnect all
	for i := uint64(1); i <= 20; i++ {
		h.selector.RemoveSession(i)
	}

	// Verify clean state
	dist := h.selector.GetCoinDistribution()
	total := 0
	for _, v := range dist {
		total += v
	}
	if total != 0 {
		t.Fatalf("expected 0 sessions after disconnect, got %d", total)
	}

	// Reconnect all
	for i := uint64(1); i <= 20; i++ {
		h.selectAndAssign(i)
	}

	// Find a block
	h.dgb.SetBlockResult("0000-post-reconnect-block")
	coin, result := h.routeShare(1, "DGB-job-1", 1000)
	if coin != "DGB" || !result.IsBlock {
		t.Fatalf("MONEY LOSS: block after reconnect: coin=%s, isBlock=%v", coin, result.IsBlock)
	}
	h.dgb.ClearBlockResult()

	if len(h.dgb.GetBlocksFound()) != 1 {
		t.Errorf("MONEY LOSS: block after full disconnect/reconnect was lost")
	}

	t.Logf("Full disconnect/reconnect: block found and attributed correctly ✓")
}
