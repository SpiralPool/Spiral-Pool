// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Integration test harness for the multi-coin smart port (v2.1).
//
// This file simulates the full multi-port flow WITHOUT requiring live nodes:
//   - Mock coin pools (DGB, BTC, BCH) with configurable availability
//   - 24-hour UTC schedule with injectable clock
//   - Share routing to the correct coin pool
//   - Block creation routed to the correct pool
//   - Coin failover when a daemon goes down
//   - All-coins-down fallback behavior
//   - Concurrent miner connection/disconnection stress
package scheduler

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// =============================================================================
// MOCK COIN POOL (implements CoinPoolHandle + CoinDifficultySource)
// =============================================================================

type mockCoinPool struct {
	mu sync.Mutex

	symbol    string
	poolID    string
	port      int
	running   atomic.Bool
	diff      atomic.Value // float64
	blockTime float64

	currentJob atomic.Pointer[protocol.Job]

	receivedShares []*protocol.Share
	shareCount     atomic.Int64

	blocksFound []*protocol.Share
	blockCount  atomic.Int64

	nextShareResult *protocol.ShareResult
}

func newMockCoinPool(symbol, poolID string, port int, diff, blockTime float64) *mockCoinPool {
	p := &mockCoinPool{
		symbol:    symbol,
		poolID:    poolID,
		port:      port,
		blockTime: blockTime,
	}
	p.diff.Store(diff)
	p.running.Store(true)

	job := &protocol.Job{
		ID:            fmt.Sprintf("%s-job-1", symbol),
		PrevBlockHash: fmt.Sprintf("0000000000000000000000000000000000000000000000000000000000%s", symbol),
		Height:        100000,
		Difficulty:    diff,
		CreatedAt:     time.Now(),
	}
	p.currentJob.Store(job)

	return p
}

// CoinDifficultySource interface
func (p *mockCoinPool) Symbol() string                { return p.symbol }
func (p *mockCoinPool) PoolID() string                { return p.poolID }
func (p *mockCoinPool) IsRunning() bool               { return p.running.Load() }
func (p *mockCoinPool) GetNetworkDifficulty() float64 { return p.diff.Load().(float64) }

// CoinPoolHandle interface
func (p *mockCoinPool) GetStratumPort() int    { return p.port }
func (p *mockCoinPool) GetCurrentJob() *protocol.Job { return p.currentJob.Load() }

func (p *mockCoinPool) HandleMultiPortShare(share *protocol.Share) *protocol.ShareResult {
	p.mu.Lock()
	p.receivedShares = append(p.receivedShares, share)
	result := p.nextShareResult
	p.mu.Unlock()
	p.shareCount.Add(1)

	if result != nil {
		if result.IsBlock {
			p.mu.Lock()
			p.blocksFound = append(p.blocksFound, share)
			p.mu.Unlock()
			p.blockCount.Add(1)
		}
		return result
	}

	return &protocol.ShareResult{
		Accepted:         true,
		ActualDifficulty: share.Difficulty,
	}
}

// Test helpers
func (p *mockCoinPool) SetDifficulty(d float64) { p.diff.Store(d) }
func (p *mockCoinPool) SetRunning(r bool)       { p.running.Store(r) }

func (p *mockCoinPool) SetBlockResult(hash string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextShareResult = &protocol.ShareResult{
		Accepted:      true,
		IsBlock:       true,
		BlockHash:     hash,
		CoinbaseValue: 1000_000_000,
	}
}

func (p *mockCoinPool) ClearBlockResult() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextShareResult = nil
}

func (p *mockCoinPool) GetReceivedShares() []*protocol.Share {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]*protocol.Share, len(p.receivedShares))
	copy(result, p.receivedShares)
	return result
}

func (p *mockCoinPool) GetBlocksFound() []*protocol.Share {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]*protocol.Share, len(p.blocksFound))
	copy(result, p.blocksFound)
	return result
}

func (p *mockCoinPool) AdvanceBlock() {
	old := p.currentJob.Load()
	newJob := &protocol.Job{
		ID:            fmt.Sprintf("%s-job-%d", p.symbol, old.Height+1),
		PrevBlockHash: fmt.Sprintf("block-%s-%d", p.symbol, old.Height),
		Height:        old.Height + 1,
		Difficulty:    p.diff.Load().(float64),
		CleanJobs:     true,
		CreatedAt:     time.Now(),
	}
	p.currentJob.Store(newJob)
}

// =============================================================================
// TEST HARNESS
// =============================================================================

type multiPortTestHarness struct {
	t       *testing.T
	monitor *Monitor
	selector *Selector
	pools    map[string]*mockCoinPool

	dgb *mockCoinPool
	btc *mockCoinPool
	bch *mockCoinPool

	// Injectable clock
	currentTime time.Time
}

// newHarness creates a test environment with 3 coins and a 24h schedule.
// Default weights: DGB=50%, BCH=30%, BTC=20%
// Time slots: DGB 00:00–12:00, BCH 12:00–19:12, BTC 19:12–24:00
func newHarness(t *testing.T, hour, minute int) *multiPortTestHarness {
	t.Helper()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	btc := newMockCoinPool("BTC", "pool_btc", 3333, 80_000_000_000_000.0, 600)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	h := &multiPortTestHarness{
		t:           t,
		monitor:     mon,
		pools:       map[string]*mockCoinPool{"DGB": dgb, "BTC": btc, "BCH": bch},
		dgb:         dgb,
		btc:         btc,
		bch:         bch,
		currentTime: time.Date(2026, 3, 29, hour, minute, 0, 0, time.UTC),
	}

	h.selector = NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BCH", "BTC"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BCH", Weight: 30},
			{Symbol: "BTC", Weight: 20},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		NowFunc:       func() time.Time { return h.currentTime },
		Logger:        zap.NewNop(),
	})

	return h
}

func (h *multiPortTestHarness) setTime(hour, minute int) {
	h.currentTime = time.Date(2026, 3, 29, hour, minute, 0, 0, time.UTC)
}

func (h *multiPortTestHarness) selectAndAssign(sessionID uint64) CoinSelection {
	sel := h.selector.SelectCoin(sessionID)
	if sel.Changed || sel.Reason == "initial_assignment" {
		h.selector.AssignCoin(sessionID, sel.Symbol, fmt.Sprintf("worker-%d", sessionID), "low")
	}
	return sel
}

func (h *multiPortTestHarness) routeShare(sessionID uint64, jobID string, diff float64) (string, *protocol.ShareResult) {
	coin, ok := h.selector.GetSessionCoin(sessionID)
	if !ok {
		h.t.Fatalf("session %d has no coin assignment", sessionID)
	}

	pool, exists := h.pools[coin]
	if !exists {
		h.t.Fatalf("no pool for coin %s", coin)
	}

	share := &protocol.Share{
		SessionID:    sessionID,
		JobID:        jobID,
		MinerAddress: fmt.Sprintf("addr-%d", sessionID),
		WorkerName:   fmt.Sprintf("worker-%d", sessionID),
		Difficulty:   diff,
		SubmittedAt:  time.Now(),
	}

	result := pool.HandleMultiPortShare(share)
	return coin, result
}

func (h *multiPortTestHarness) pollAndUpdate() {
	h.monitor.poll()
}

// =============================================================================
// INTEGRATION TEST: FULL SCHEDULED FLOW
// =============================================================================

func TestIntegration_FullScheduledFlow(t *testing.T) {
	// DGB=50% (00:00–12:00), BCH=30% (12:00–19:12), BTC=20% (19:12–24:00)
	h := newHarness(t, 6, 0) // Start at 06:00 → DGB slot

	// Step 1: Connect miner → should be assigned to DGB (current slot)
	sel := h.selectAndAssign(1)
	if sel.Symbol != "DGB" || !sel.Changed {
		t.Fatalf("Step 1: expected DGB initial_assignment, got %s (changed=%v)", sel.Symbol, sel.Changed)
	}
	t.Logf("Step 1: Miner assigned to %s at 06:00 UTC ✓", sel.Symbol)

	// Step 2: Mine shares on DGB
	for i := 0; i < 10; i++ {
		coin, result := h.routeShare(1, "DGB-job-1", 1000)
		if coin != "DGB" || !result.Accepted {
			t.Fatalf("Step 2: share %d: coin=%s, accepted=%v", i, coin, result.Accepted)
		}
	}
	t.Logf("Step 2: 10 shares mined on DGB ✓")

	// Step 3: Time advances to 13:00 → BCH slot
	h.setTime(13, 0)
	sel = h.selectAndAssign(1)
	if sel.Symbol != "BCH" || !sel.Changed {
		t.Fatalf("Step 3: expected BCH scheduled_rotation, got %s (changed=%v, reason=%s)", sel.Symbol, sel.Changed, sel.Reason)
	}
	t.Logf("Step 3: Scheduled rotation → BCH at 13:00 UTC ✓")

	// Step 4: Mine shares on BCH
	for i := 0; i < 5; i++ {
		coin, result := h.routeShare(1, "BCH-job-1", 500)
		if coin != "BCH" || !result.Accepted {
			t.Fatalf("Step 4: share %d: coin=%s, accepted=%v", i, coin, result.Accepted)
		}
	}
	t.Logf("Step 4: 5 shares mined on BCH ✓")

	// Step 5: Find a block on BCH
	h.bch.SetBlockResult("0000000000000000000bch-block-found")
	coin, result := h.routeShare(1, "BCH-job-1", 500)
	if coin != "BCH" || !result.IsBlock {
		t.Fatalf("Step 5: block not found on BCH: coin=%s, isBlock=%v", coin, result.IsBlock)
	}
	h.bch.ClearBlockResult()
	t.Logf("Step 5: Block found on BCH ✓ (hash: %s)", result.BlockHash)

	// Step 6: Time advances to 20:00 → BTC slot
	h.setTime(20, 0)
	sel = h.selectAndAssign(1)
	if sel.Symbol != "BTC" || !sel.Changed {
		t.Fatalf("Step 6: expected BTC, got %s (reason=%s)", sel.Symbol, sel.Reason)
	}
	t.Logf("Step 6: Scheduled rotation → BTC at 20:00 UTC ✓")

	// Step 7: Verify share counts
	dgbShares := len(h.dgb.GetReceivedShares())
	bchShares := len(h.bch.GetReceivedShares())
	bchBlocks := len(h.bch.GetBlocksFound())
	t.Logf("\n=== FULL FLOW SUMMARY ===")
	t.Logf("DGB shares: %d, BCH shares: %d (1 block), BTC shares: 0", dgbShares, bchShares)
	if dgbShares != 10 || bchShares != 6 || bchBlocks != 1 {
		t.Errorf("unexpected counts: DGB=%d BCH=%d bchBlocks=%d", dgbShares, bchShares, bchBlocks)
	}
}

// =============================================================================
// INTEGRATION TEST: BLOCK CREATION ROUTING
// =============================================================================

func TestIntegration_BlockCreationRoutedToCorrectPool(t *testing.T) {
	h := newHarness(t, 6, 0) // DGB slot

	// Two miners, both on DGB
	h.selectAndAssign(1)
	h.selectAndAssign(2)

	// Session 1 finds a block on DGB
	h.dgb.SetBlockResult("0000dgb-block-by-session-1")
	coin1, result1 := h.routeShare(1, "DGB-job-1", 1000)
	if coin1 != "DGB" || !result1.IsBlock {
		t.Fatalf("session 1 block not on DGB: coin=%s, isBlock=%v", coin1, result1.IsBlock)
	}
	h.dgb.ClearBlockResult()

	// Session 2 submits a normal share on DGB
	coin2, result2 := h.routeShare(2, "DGB-job-2", 1000)
	if coin2 != "DGB" || !result2.Accepted || result2.IsBlock {
		t.Fatalf("session 2 normal share wrong: coin=%s, accepted=%v, isBlock=%v", coin2, result2.Accepted, result2.IsBlock)
	}

	// Move to BCH slot
	h.setTime(13, 0)
	h.selectAndAssign(1)
	h.selectAndAssign(2)

	// Session 2 finds a block on BCH
	h.bch.SetBlockResult("0000bch-block-by-session-2")
	coin2b, result2b := h.routeShare(2, "BCH-job-1", 500)
	if coin2b != "BCH" || !result2b.IsBlock {
		t.Fatalf("session 2 block not on BCH: coin=%s, isBlock=%v", coin2b, result2b.IsBlock)
	}
	h.bch.ClearBlockResult()

	// Verify block attribution
	dgbBlocks := h.dgb.GetBlocksFound()
	bchBlocks := h.bch.GetBlocksFound()
	if len(dgbBlocks) != 1 || dgbBlocks[0].SessionID != 1 {
		t.Errorf("DGB block attribution wrong: %d blocks", len(dgbBlocks))
	}
	if len(bchBlocks) != 1 || bchBlocks[0].SessionID != 2 {
		t.Errorf("BCH block attribution wrong: %d blocks", len(bchBlocks))
	}

	t.Logf("Block routing: session 1 → DGB block ✓, session 2 → BCH block ✓")
}

// =============================================================================
// INTEGRATION TEST: COIN FAILOVER
// =============================================================================

func TestIntegration_CoinPoolGoesDown_MinersFailover(t *testing.T) {
	h := newHarness(t, 6, 0) // DGB slot

	// 5 miners on DGB
	for i := uint64(1); i <= 5; i++ {
		h.selectAndAssign(i)
	}
	dist := h.selector.GetCoinDistribution()
	if dist["DGB"] != 5 {
		t.Fatalf("expected all 5 on DGB, got %v", dist)
	}

	// DGB goes down
	h.dgb.SetRunning(false)
	h.pollAndUpdate()

	// Re-evaluate — should failover to BCH or BTC
	for i := uint64(1); i <= 5; i++ {
		sel := h.selectAndAssign(i)
		if sel.Symbol == "DGB" {
			t.Errorf("session %d still on DGB after failover", i)
		}
	}
	dist = h.selector.GetCoinDistribution()
	t.Logf("After DGB down — distribution: %v", dist)
	if dist["DGB"] > 0 {
		t.Error("no sessions should be on DGB")
	}

	// DGB recovers — miners should return on next re-eval (still in DGB slot)
	h.dgb.SetRunning(true)
	h.pollAndUpdate()
	for i := uint64(1); i <= 5; i++ {
		sel := h.selectAndAssign(i)
		if sel.Symbol != "DGB" {
			t.Errorf("session %d should return to DGB, got %s", i, sel.Symbol)
		}
	}
	t.Logf("DGB recovered — all miners returned ✓")
}

// =============================================================================
// INTEGRATION TEST: ALL COINS DOWN
// =============================================================================

func TestIntegration_AllCoinsDown_GracefulDegradation(t *testing.T) {
	h := newHarness(t, 6, 0)

	h.selectAndAssign(1)

	// All coins down
	h.dgb.SetRunning(false)
	h.btc.SetRunning(false)
	h.bch.SetRunning(false)
	h.pollAndUpdate()

	sel := h.selector.SelectCoin(1)
	if sel.Changed {
		t.Error("should retain last assignment")
	}
	if sel.Reason != "no_coins_available" {
		t.Errorf("reason = %q, want no_coins_available", sel.Reason)
	}
	t.Logf("All coins down — graceful degradation ✓ (retained: %s, reason: %s)", sel.Symbol, sel.Reason)
}

// =============================================================================
// INTEGRATION TEST: SHARE ROUTING ISOLATION
// =============================================================================

func TestIntegration_SharesRoutedToCorrectPool_Isolated(t *testing.T) {
	// Session 1 mines during DGB slot, session 2 mines during BCH slot
	h := newHarness(t, 6, 0) // DGB slot

	h.selectAndAssign(1)

	// 10 shares on DGB
	for i := 0; i < 10; i++ {
		coin, result := h.routeShare(1, "DGB-job-1", 1000)
		if coin != "DGB" || !result.Accepted {
			t.Fatalf("share %d: coin=%s, accepted=%v", i, coin, result.Accepted)
		}
	}

	// Move to BCH slot
	h.setTime(13, 0)
	h.selectAndAssign(2) // new session during BCH slot

	for i := 0; i < 10; i++ {
		coin, result := h.routeShare(2, "BCH-job-1", 500)
		if coin != "BCH" || !result.Accepted {
			t.Fatalf("share %d: coin=%s, accepted=%v", i, coin, result.Accepted)
		}
	}

	dgbShares := len(h.dgb.GetReceivedShares())
	bchShares := len(h.bch.GetReceivedShares())
	btcShares := len(h.btc.GetReceivedShares())

	if dgbShares != 10 || bchShares != 10 || btcShares != 0 {
		t.Errorf("share isolation failed: DGB=%d, BCH=%d, BTC=%d", dgbShares, bchShares, btcShares)
	}
	t.Logf("Share isolation: DGB=%d (session 1), BCH=%d (session 2), BTC=%d ✓", dgbShares, bchShares, btcShares)
}

// =============================================================================
// INTEGRATION TEST: EXPECTED SHARE TIME CALCULATION
// =============================================================================

func TestIntegration_ExpectedShareTimeRealistic(t *testing.T) {
	tests := []struct {
		coin        string
		networkDiff float64
		minerName   string
		hashrate    float64
		wantRange   [2]float64
	}{
		{"DGB", 1000, "BitAxe Ultra", 500e9, [2]float64{5, 15}},
		{"DGB", 1000, "NerdMiner", 50e3, [2]float64{50_000_000, 120_000_000}},
		{"BTC", 80e12, "S19", 110e12, [2]float64{2_000_000_000, 4_000_000_000}},
		{"BCH", 500_000, "BitAxe Ultra", 500e9, [2]float64{3000, 5000}},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_on_%s", tt.minerName, tt.coin), func(t *testing.T) {
			et := ExpectedShareTime(tt.networkDiff, tt.hashrate)
			if math.IsInf(et, 1) {
				t.Fatalf("expected finite share time, got Inf")
			}
			if et < tt.wantRange[0] || et > tt.wantRange[1] {
				t.Errorf("expected share time %.1fs outside range [%.1f, %.1f]",
					et, tt.wantRange[0], tt.wantRange[1])
			}
			t.Logf("%s on %s (diff=%.0f): %.1fs between shares",
				tt.minerName, tt.coin, tt.networkDiff, et)
		})
	}
}

// =============================================================================
// INTEGRATION TEST: MONITOR EVENT PROPAGATION
// =============================================================================

func TestIntegration_DifficultyMonitorEvents(t *testing.T) {
	h := newHarness(t, 6, 0)

	ch := h.monitor.Subscribe()

	// Change DGB difficulty by 30%
	h.dgb.SetDifficulty(1300.0)
	h.pollAndUpdate()

	select {
	case event := <-ch:
		if event.Symbol != "DGB" {
			t.Errorf("event symbol = %q, want DGB", event.Symbol)
		}
		if math.Abs(event.ChangePercent-30.0) > 0.1 {
			t.Errorf("change = %.1f%%, want ~30%%", event.ChangePercent)
		}
		t.Logf("Received event: DGB difficulty changed %.1f%% (%.0f → %.0f)",
			event.ChangePercent, event.OldDiff, event.NewDiff)
	default:
		t.Error("expected difficulty change event, got none")
	}

	// Block notification
	h.dgb.SetDifficulty(2000.0)
	h.monitor.NotifyBlockFound("DGB")

	select {
	case event := <-ch:
		if event.NewDiff != 2000.0 {
			t.Errorf("newDiff = %f, want 2000", event.NewDiff)
		}
		t.Logf("Block notification event: DGB → %.0f ✓", event.NewDiff)
	default:
		t.Error("expected event from NotifyBlockFound")
	}
}

// =============================================================================
// INTEGRATION TEST: CONCURRENT MINERS STRESS TEST
// =============================================================================

func TestIntegration_ConcurrentMiners_StressTest(t *testing.T) {
	h := newHarness(t, 6, 0)

	const numMiners = 50
	var wg sync.WaitGroup
	var totalAccepted atomic.Int64

	// All miners connect and get assigned
	for i := uint64(1); i <= numMiners; i++ {
		h.selectAndAssign(i)
	}

	// All submit shares concurrently
	for i := uint64(1); i <= numMiners; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				coin, ok := h.selector.GetSessionCoin(id)
				if !ok {
					continue
				}
				pool, exists := h.pools[coin]
				if !exists {
					continue
				}
				share := &protocol.Share{
					SessionID:    id,
					JobID:        fmt.Sprintf("%s-job-1", coin),
					MinerAddress: fmt.Sprintf("addr-%d", id),
					WorkerName:   fmt.Sprintf("worker-%d", id),
					Difficulty:   1000,
					SubmittedAt:  time.Now(),
				}
				result := pool.HandleMultiPortShare(share)
				if result.Accepted {
					totalAccepted.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	accepted := totalAccepted.Load()
	totalShares := h.dgb.shareCount.Load() + h.bch.shareCount.Load() + h.btc.shareCount.Load()

	if accepted != int64(numMiners*20) {
		t.Errorf("expected %d accepted, got %d", numMiners*20, accepted)
	}
	if totalShares != int64(numMiners*20) {
		t.Errorf("expected %d total shares, got %d", numMiners*20, totalShares)
	}

	dist := h.selector.GetCoinDistribution()
	t.Logf("Stress test: %d miners, %d total shares accepted, %d total received across pools",
		numMiners, accepted, totalShares)
	t.Logf("Distribution: DGB=%d, BCH=%d, BTC=%d", dist["DGB"], dist["BCH"], dist["BTC"])
}

// =============================================================================
// INTEGRATION TEST: BLOCK FOUND DURING COIN SWITCH
// =============================================================================

func TestIntegration_BlockFoundDuringCoinSwitch(t *testing.T) {
	h := newHarness(t, 6, 0) // DGB slot

	h.selectAndAssign(1)

	// Miner finds a block on DGB
	h.dgb.SetBlockResult("0000000-dgb-block-during-switch")
	coin, result := h.routeShare(1, "DGB-job-1", 1000)
	if coin != "DGB" || !result.IsBlock {
		t.Fatalf("block not on DGB: coin=%s, isBlock=%v", coin, result.IsBlock)
	}
	t.Logf("Block found on DGB ✓ (hash: %s)", result.BlockHash)

	// Now switch to BCH slot
	h.dgb.ClearBlockResult()
	h.setTime(13, 0)
	sel := h.selectAndAssign(1)
	if sel.Symbol != "BCH" {
		t.Fatalf("expected BCH after switch, got %s", sel.Symbol)
	}

	// Verify block was recorded on DGB
	dgbBlocks := h.dgb.GetBlocksFound()
	if len(dgbBlocks) != 1 {
		t.Errorf("DGB should have 1 block, got %d", len(dgbBlocks))
	}
	t.Logf("Block on DGB recorded ✓, then switched to %s ✓", sel.Symbol)
}

// =============================================================================
// INTEGRATION TEST: SESSION CLEANUP
// =============================================================================

func TestIntegration_SessionCleanup(t *testing.T) {
	h := newHarness(t, 6, 0)

	// Connect 100 sessions
	for i := uint64(1); i <= 100; i++ {
		h.selectAndAssign(i)
	}

	dist := h.selector.GetCoinDistribution()
	total := 0
	for _, count := range dist {
		total += count
	}
	if total != 100 {
		t.Fatalf("expected 100 sessions, got %d", total)
	}

	// Disconnect all
	for i := uint64(1); i <= 100; i++ {
		h.selector.RemoveSession(i)
	}

	dist = h.selector.GetCoinDistribution()
	total = 0
	for _, count := range dist {
		total += count
	}
	if total != 0 {
		t.Errorf("expected 0 sessions after cleanup, got %d", total)
	}
	t.Logf("Session cleanup: 100 connected, 100 disconnected, %d remaining ✓", total)
}

// =============================================================================
// INTEGRATION TEST: MONITOR START/STOP
// =============================================================================

func TestIntegration_MonitorStartStop(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: 50 * time.Millisecond,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("DGB", "pool_dgb", 1000.0)
	mon.RegisterCoin(src, 15)

	ctx, cancel := context.WithCancel(context.Background())
	mon.Start(ctx)

	time.Sleep(150 * time.Millisecond)

	state, _ := mon.GetState("DGB")
	if !state.Available {
		t.Error("should be available after polling")
	}

	cancel()
	mon.Stop()
}
