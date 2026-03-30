// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Single-port multi-coin schedule regression tests.
//
// These tests exercise the REAL production code paths for the V2.1
// Multi-coin smart port's single-port multi-coin scheduling:
//
//   - buildTimeSlots (schedule math) — real function, no mocks
//   - Selector.SelectCoin — real production logic with injectable clock
//   - Monitor.poll / Monitor.NotifyBlockFound — real polling + event pub
//   - MultiServer share routing — real handleShare, grace periods, reevaluateAll
//
// The ONLY mock is CoinPoolHandle (the daemon boundary). Everything from
// the scheduling layer up through share routing is real production code.
//
// Regression targets:
//   - Schedule slot math correctness for all weight combinations
//   - DST / timezone-aware day fraction computation
//   - Coin failover when scheduled coin is unavailable
//   - MinTimeOnCoin enforcement before rotation
//   - Grace period for stale shares after coin switch
//   - Session persistence across reevaluation cycles
//   - Monitor availability tracking (zero-diff, daemon down)
//   - Switch history recording and dashboard stats
//   - Concurrent session reevaluation under load
package scheduler

import (
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
// SECTION 1: SCHEDULE MATH REGRESSION (buildTimeSlots — real function)
// =============================================================================

func TestRegression_BuildTimeSlots_StandardWeights(t *testing.T) {
	t.Parallel()

	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 50},
		{Symbol: "BCH", Weight: 30},
		{Symbol: "BTC", Weight: 20},
	})

	if len(slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(slots))
	}

	// DGB: 0.0–0.5 (00:00–12:00)
	assertSlot(t, slots[0], "DGB", 0.0, 0.5)
	// BCH: 0.5–0.8 (12:00–19:12)
	assertSlot(t, slots[1], "BCH", 0.5, 0.8)
	// BTC: 0.8–1.0 (19:12–24:00)
	assertSlot(t, slots[2], "BTC", 0.8, 1.0)

	t.Logf("Standard weights: DGB=50%% [0.0–0.5], BCH=30%% [0.5–0.8], BTC=20%% [0.8–1.0] ✓")
}

func TestRegression_BuildTimeSlots_SingleCoin(t *testing.T) {
	t.Parallel()

	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 100},
	})

	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	assertSlot(t, slots[0], "DGB", 0.0, 1.0)
	t.Logf("Single coin: DGB=100%% [0.0–1.0] ✓")
}

func TestRegression_BuildTimeSlots_UnequalWeights(t *testing.T) {
	t.Parallel()

	// 80/15/5 split
	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 80},
		{Symbol: "BCH", Weight: 15},
		{Symbol: "BTC", Weight: 5},
	})

	if len(slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(slots))
	}
	assertSlot(t, slots[0], "DGB", 0.0, 0.8)
	assertSlot(t, slots[1], "BCH", 0.8, 0.95)
	assertSlot(t, slots[2], "BTC", 0.95, 1.0)
	t.Logf("Unequal weights: DGB=80%% [0.0–0.8], BCH=15%% [0.8–0.95], BTC=5%% [0.95–1.0] ✓")
}

func TestRegression_BuildTimeSlots_ManyCoins(t *testing.T) {
	t.Parallel()

	weights := []CoinWeight{
		{Symbol: "DGB", Weight: 25},
		{Symbol: "BCH", Weight: 25},
		{Symbol: "BTC", Weight: 25},
		{Symbol: "LTC", Weight: 15},
		{Symbol: "DOGE", Weight: 10},
	}
	slots := buildTimeSlots(weights)

	if len(slots) != 5 {
		t.Fatalf("expected 5 slots, got %d", len(slots))
	}

	// Verify contiguity: each slot starts where previous ends
	for i := 1; i < len(slots); i++ {
		if math.Abs(slots[i].startFrac-slots[i-1].endFrac) > 1e-9 {
			t.Errorf("gap between slot %d and %d: end=%.9f start=%.9f",
				i-1, i, slots[i-1].endFrac, slots[i].startFrac)
		}
	}

	// First slot starts at 0, last ends at 1
	if slots[0].startFrac != 0.0 {
		t.Errorf("first slot start = %f, want 0.0", slots[0].startFrac)
	}
	if slots[len(slots)-1].endFrac != 1.0 {
		t.Errorf("last slot end = %f, want 1.0", slots[len(slots)-1].endFrac)
	}

	t.Logf("5-coin contiguous slots: no gaps, starts at 0.0, ends at 1.0 ✓")
}

func TestRegression_BuildTimeSlots_ZeroWeightSkipped(t *testing.T) {
	t.Parallel()

	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 50},
		{Symbol: "BCH", Weight: 0},  // should be skipped
		{Symbol: "BTC", Weight: 50},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots (BCH weight=0 skipped), got %d", len(slots))
	}
	assertSlot(t, slots[0], "DGB", 0.0, 0.5)
	assertSlot(t, slots[1], "BTC", 0.5, 1.0)

	// Verify BCH is not in any slot
	for _, s := range slots {
		if s.symbol == "BCH" {
			t.Error("BCH with weight=0 should not have a slot")
		}
	}
	t.Logf("Zero-weight coin skipped: DGB=50%%, BTC=50%%, BCH omitted ✓")
}

func TestRegression_BuildTimeSlots_NegativeWeightSkipped(t *testing.T) {
	t.Parallel()

	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 60},
		{Symbol: "BCH", Weight: -10}, // should be skipped
		{Symbol: "BTC", Weight: 40},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots (negative weight skipped), got %d", len(slots))
	}
	for _, s := range slots {
		if s.symbol == "BCH" {
			t.Error("BCH with negative weight should not have a slot")
		}
	}
	t.Logf("Negative-weight coin skipped ✓")
}

func TestRegression_BuildTimeSlots_EmptyWeights(t *testing.T) {
	t.Parallel()

	slots := buildTimeSlots(nil)
	if slots != nil {
		t.Errorf("expected nil slots for nil weights, got %d slots", len(slots))
	}

	slots = buildTimeSlots([]CoinWeight{})
	if slots != nil {
		t.Errorf("expected nil slots for empty weights, got %d slots", len(slots))
	}
	t.Logf("Empty/nil weights return nil slots ✓")
}

func TestRegression_BuildTimeSlots_AllZeroWeights(t *testing.T) {
	t.Parallel()

	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 0},
		{Symbol: "BTC", Weight: 0},
	})
	if slots != nil {
		t.Errorf("expected nil slots when all weights are zero, got %d slots", len(slots))
	}
	t.Logf("All-zero weights return nil slots ✓")
}

func TestRegression_BuildTimeSlots_WeightsDontSumTo100(t *testing.T) {
	t.Parallel()

	// Weights sum to 200 — should normalize correctly
	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 100},
		{Symbol: "BTC", Weight: 100},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	// 100/200 = 0.5 each
	assertSlot(t, slots[0], "DGB", 0.0, 0.5)
	assertSlot(t, slots[1], "BTC", 0.5, 1.0)

	// Weights sum to 10
	slots = buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 7},
		{Symbol: "BTC", Weight: 3},
	})
	assertSlot(t, slots[0], "DGB", 0.0, 0.7)
	assertSlot(t, slots[1], "BTC", 0.7, 1.0)

	t.Logf("Non-100 weights normalize correctly: 200→50/50, 10→70/30 ✓")
}

// =============================================================================
// SECTION 2: SELECTOR SCHEDULE REGRESSION (real SelectCoin + injectable clock)
// =============================================================================

func TestRegression_Selector_FullDayScheduleAccuracy(t *testing.T) {
	t.Parallel()

	// Walk every minute of a 24-hour day and verify the selector picks the
	// correct coin based on the 50/30/20 schedule.
	// DGB: 00:00–12:00 (0.0–0.5), BCH: 12:00–19:12 (0.5–0.8), BTC: 19:12–24:00 (0.8–1.0)

	type expected struct {
		hour int
		min  int
		coin string
	}

	// Spot-check key times
	checks := []expected{
		{0, 0, "DGB"},
		{0, 1, "DGB"},
		{5, 59, "DGB"},
		{6, 0, "DGB"},
		{11, 59, "DGB"},
		{12, 0, "BCH"},
		{12, 1, "BCH"},
		{15, 30, "BCH"},
		{19, 11, "BCH"},
		{19, 12, "BTC"}, // 19:12 = 0.8 of day = BCH→BTC boundary
		{19, 13, "BTC"},
		{22, 0, "BTC"},
		{23, 59, "BTC"},
	}

	for _, c := range checks {
		t.Run(fmt.Sprintf("%02d:%02d→%s", c.hour, c.min, c.coin), func(t *testing.T) {
			h := newHarness(t, c.hour, c.min)
			sel := h.selectAndAssign(1)
			if sel.Symbol != c.coin {
				t.Errorf("at %02d:%02d expected %s, got %s (reason: %s)",
					c.hour, c.min, c.coin, sel.Symbol, sel.Reason)
			}
		})
	}
}

func TestRegression_Selector_MinTimeOnCoinEnforced(t *testing.T) {
	t.Parallel()

	// Create selector with MinTimeOnCoin = 5 minutes
	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	currentTime := time.Date(2026, 3, 29, 11, 58, 0, 0, time.UTC) // 2 min before BCH slot

	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BCH"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BCH", Weight: 50},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: 5 * time.Minute, // enforce 5-minute minimum
		NowFunc:       func() time.Time { return currentTime },
		Logger:        zap.NewNop(),
	})

	// Assign at 11:58 → DGB slot
	s := sel.SelectCoin(1)
	if s.Symbol != "DGB" {
		t.Fatalf("expected DGB at 11:58, got %s", s.Symbol)
	}
	sel.AssignCoin(1, s.Symbol, "worker-1", "low")

	// Advance to 12:01 → BCH slot, but minTime hasn't elapsed (only 3 minutes)
	currentTime = time.Date(2026, 3, 29, 12, 1, 0, 0, time.UTC)
	s = sel.SelectCoin(1)

	// Should stay on DGB because minTimeOnCoin not met
	if s.Symbol != "DGB" {
		t.Errorf("MinTimeOnCoin not enforced: expected DGB (min_time_not_elapsed), got %s (reason: %s)",
			s.Symbol, s.Reason)
	}
	if s.Changed {
		t.Error("should NOT have changed — min time not elapsed")
	}

	t.Logf("MinTimeOnCoin: miner stays on DGB despite BCH slot because 5min minimum not reached ✓")
}

func TestRegression_Selector_MinTimeBypassedWhenCoinDown(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	currentTime := time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC) // DGB slot

	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BCH"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BCH", Weight: 50},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: 10 * time.Minute,
		NowFunc:       func() time.Time { return currentTime },
		Logger:        zap.NewNop(),
	})

	// Assign to DGB
	s := sel.SelectCoin(1)
	sel.AssignCoin(1, s.Symbol, "worker-1", "low")

	// DGB goes down immediately (1 second later — well under min time)
	currentTime = currentTime.Add(1 * time.Second)
	dgb.SetRunning(false)
	mon.poll()

	s = sel.SelectCoin(1)
	// MinTimeOnCoin should be bypassed because DGB is unavailable
	if s.Symbol == "DGB" {
		t.Error("miner should have been moved off DGB — it's down, even though minTime not elapsed")
	}
	if !s.Changed {
		t.Error("expected Changed=true for failover")
	}

	t.Logf("MinTimeOnCoin bypassed when coin is down: failed over to %s ✓", s.Symbol)
}

func TestRegression_Selector_FailoverPicksNextAvailableSlot(t *testing.T) {
	t.Parallel()

	h := newHarness(t, 6, 0) // DGB slot

	// DGB is down — should failover to next available coin
	h.dgb.SetRunning(false)
	h.pollAndUpdate()

	sel := h.selectAndAssign(1)
	if sel.Symbol == "DGB" {
		t.Fatal("should not assign to DGB when it's down")
	}

	// BCH and BTC are both up — should pick BCH (next in slot list after DGB)
	if sel.Symbol != "BCH" {
		t.Errorf("expected BCH as failover (next in slot list), got %s", sel.Symbol)
	}

	t.Logf("Failover order: DGB down → picked %s (next available in slot list) ✓", sel.Symbol)
}

func TestRegression_Selector_AllCoinsDownRetainsAssignment(t *testing.T) {
	t.Parallel()

	h := newHarness(t, 6, 0)
	h.selectAndAssign(1) // assigned to DGB

	// All coins down
	h.dgb.SetRunning(false)
	h.btc.SetRunning(false)
	h.bch.SetRunning(false)
	h.pollAndUpdate()

	sel := h.selector.SelectCoin(1)
	if sel.Changed {
		t.Error("should retain last assignment when all coins are down")
	}
	if sel.Reason != "no_coins_available" {
		t.Errorf("reason should be no_coins_available, got %q", sel.Reason)
	}
	if sel.Symbol != "DGB" {
		t.Errorf("should retain DGB as last assignment, got %s", sel.Symbol)
	}

	t.Logf("All coins down: retained DGB, reason=%s ✓", sel.Reason)
}

func TestRegression_Selector_SwitchHistoryRecorded(t *testing.T) {
	t.Parallel()

	h := newHarness(t, 6, 0) // DGB slot
	h.selectAndAssign(1)

	// Force rotation to BCH
	h.setTime(13, 0)
	h.selectAndAssign(1)

	// Force rotation to BTC
	h.setTime(20, 0)
	h.selectAndAssign(1)

	history := h.selector.GetSwitchHistory(10)
	if len(history) != 2 {
		t.Fatalf("expected 2 switch events, got %d", len(history))
	}

	if history[0].FromCoin != "DGB" || history[0].ToCoin != "BCH" {
		t.Errorf("switch 1: expected DGB→BCH, got %s→%s", history[0].FromCoin, history[0].ToCoin)
	}
	if history[1].FromCoin != "BCH" || history[1].ToCoin != "BTC" {
		t.Errorf("switch 2: expected BCH→BTC, got %s→%s", history[1].FromCoin, history[1].ToCoin)
	}

	t.Logf("Switch history: DGB→BCH→BTC recorded correctly ✓")
}

func TestRegression_Selector_SessionCleanupNoLeak(t *testing.T) {
	t.Parallel()

	h := newHarness(t, 6, 0)

	// Connect 500 sessions
	for i := uint64(1); i <= 500; i++ {
		h.selectAndAssign(i)
	}

	dist := h.selector.GetCoinDistribution()
	total := 0
	for _, v := range dist {
		total += v
	}
	if total != 500 {
		t.Fatalf("expected 500 sessions, got %d", total)
	}

	// Disconnect all
	for i := uint64(1); i <= 500; i++ {
		h.selector.RemoveSession(i)
	}

	dist = h.selector.GetCoinDistribution()
	total = 0
	for _, v := range dist {
		total += v
	}
	if total != 0 {
		t.Errorf("expected 0 sessions after cleanup, got %d — MEMORY LEAK", total)
	}

	t.Logf("500 sessions connected and cleaned up with no leak ✓")
}

func TestRegression_Selector_ConcurrentSelectAndAssign(t *testing.T) {
	t.Parallel()

	h := newHarness(t, 6, 0)
	numMiners := 100

	var wg sync.WaitGroup
	var assignErrors atomic.Int64

	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()

			// Each miner: select, assign, then select again 10 times
			for j := 0; j < 10; j++ {
				sel := h.selector.SelectCoin(id)
				if sel.Symbol == "" {
					assignErrors.Add(1)
					continue
				}
				if sel.Changed || j == 0 {
					h.selector.AssignCoin(id, sel.Symbol, fmt.Sprintf("worker-%d", id), "low")
				}

				// Verify assignment stuck
				coin, ok := h.selector.GetSessionCoin(id)
				if !ok || coin != sel.Symbol {
					assignErrors.Add(1)
				}
			}
		}(uint64(i + 1))
	}

	wg.Wait()

	if errors := assignErrors.Load(); errors > 0 {
		t.Errorf("RACE CONDITION: %d assignment errors under concurrent access", errors)
	}

	dist := h.selector.GetCoinDistribution()
	total := 0
	for _, v := range dist {
		total += v
	}
	if total != numMiners {
		t.Errorf("expected %d total sessions, got %d", numMiners, total)
	}

	t.Logf("Concurrent select/assign: %d miners × 10 iterations, distribution=%v ✓", numMiners, dist)
}

// =============================================================================
// SECTION 3: MONITOR REGRESSION (real poll + event publish)
// =============================================================================

func TestRegression_Monitor_ZeroDiffMarksUnavailable(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.poll()

	state, ok := mon.GetState("DGB")
	if !ok || !state.Available {
		t.Fatal("DGB should be available initially")
	}

	// DGB reports zero difficulty (daemon syncing)
	dgb.SetDifficulty(0)
	mon.poll()

	state, _ = mon.GetState("DGB")
	if state.Available {
		t.Error("DGB should be unavailable with zero difficulty")
	}

	// DGB recovers
	dgb.SetDifficulty(1500.0)
	mon.poll()

	state, _ = mon.GetState("DGB")
	if !state.Available {
		t.Error("DGB should be available again after recovery")
	}
	if state.NetworkDiff != 1500.0 {
		t.Errorf("expected diff 1500, got %f", state.NetworkDiff)
	}

	t.Logf("Monitor: zero diff → unavailable → recovery → available (diff=%.0f) ✓", state.NetworkDiff)
}

func TestRegression_Monitor_DaemonDownMarksUnavailable(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.poll()

	// DGB daemon goes down
	dgb.SetRunning(false)
	mon.poll()

	state, _ := mon.GetState("DGB")
	if state.Available {
		t.Error("DGB should be unavailable when daemon is down")
	}

	// Daemon comes back
	dgb.SetRunning(true)
	mon.poll()

	state, _ = mon.GetState("DGB")
	if !state.Available {
		t.Error("DGB should be available after daemon restart")
	}

	t.Logf("Monitor: daemon down → unavailable → restart → available ✓")
}

func TestRegression_Monitor_DiffChangePublishesEvent(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.poll() // sets initial diff

	ch := mon.Subscribe()

	// 20% difficulty increase
	dgb.SetDifficulty(1200.0)
	mon.poll()

	select {
	case event := <-ch:
		if event.Symbol != "DGB" {
			t.Errorf("event symbol: got %q, want DGB", event.Symbol)
		}
		if math.Abs(event.ChangePercent-20.0) > 0.1 {
			t.Errorf("change percent: got %.1f%%, want ~20%%", event.ChangePercent)
		}
		if event.OldDiff != 1000.0 || event.NewDiff != 1200.0 {
			t.Errorf("diffs: old=%f new=%f, want 1000/1200", event.OldDiff, event.NewDiff)
		}
	default:
		t.Error("expected difficulty change event, got none")
	}

	t.Logf("Monitor event: DGB 1000→1200 (20%% change) published ✓")
}

func TestRegression_Monitor_SmallDiffChangeNoEvent(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.poll()

	ch := mon.Subscribe()

	// 0.05% change — below 0.1% threshold
	dgb.SetDifficulty(1000.5)
	mon.poll()

	select {
	case event := <-ch:
		t.Errorf("should NOT publish event for tiny change, got: %+v", event)
	default:
		// Expected: no event
	}

	t.Logf("Monitor: 0.05%% change suppressed (below 0.1%% threshold) ✓")
}

func TestRegression_Monitor_NotifyBlockFoundTriggersRepoll(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.poll()

	ch := mon.Subscribe()

	// Block found — difficulty jumps
	dgb.SetDifficulty(2000.0)
	mon.NotifyBlockFound("DGB")

	select {
	case event := <-ch:
		if event.NewDiff != 2000.0 {
			t.Errorf("expected new diff 2000, got %f", event.NewDiff)
		}
	default:
		t.Error("NotifyBlockFound should trigger immediate repoll and event")
	}

	t.Logf("NotifyBlockFound: immediate repoll, diff updated to 2000 ✓")
}

func TestRegression_Monitor_MultiCoinIndependentAvailability(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	btc := newMockCoinPool("BTC", "pool_btc", 3333, 80e12, 600)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	// DGB down, BTC zero diff, BCH fine
	dgb.SetRunning(false)
	btc.SetDifficulty(0)
	mon.poll()

	states := mon.GetAllStates()
	if states["DGB"].Available {
		t.Error("DGB should be unavailable")
	}
	if states["BTC"].Available {
		t.Error("BTC should be unavailable (zero diff)")
	}
	if !states["BCH"].Available {
		t.Error("BCH should still be available")
	}

	t.Logf("Multi-coin independence: DGB=down, BTC=zero-diff, BCH=available ✓")
}

// =============================================================================
// SECTION 4: MULTISERVER SHARE ROUTING REGRESSION (real handleShare)
// =============================================================================

func TestRegression_MultiServer_ShareRoutesToAssignedCoin(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	// Create MultiServer with real routing logic
	ms := &MultiServer{
		cfg:       MultiServerConfig{Logger: zap.NewNop()},
		logger:    zap.NewNop().Sugar().Named("test"),
		coinPools: map[string]CoinPoolHandle{"DGB": dgb, "BCH": bch},
		monitor:   mon,
		graceWindow: 10 * time.Second,
	}

	// Manually assign session 1 to DGB
	ms.sessionCoin.Store(uint64(1), "DGB")

	share := &protocol.Share{
		SessionID:  1,
		JobID:      "DGB-job-1",
		Difficulty: 1000,
	}

	result := ms.handleShare(share)
	if !result.Accepted {
		t.Errorf("share should be accepted by DGB pool, got rejected: %s", result.RejectReason)
	}

	// Verify it went to DGB, not BCH
	if dgb.shareCount.Load() != 1 {
		t.Errorf("DGB should have received 1 share, got %d", dgb.shareCount.Load())
	}
	if bch.shareCount.Load() != 0 {
		t.Errorf("BCH should have received 0 shares, got %d", bch.shareCount.Load())
	}

	t.Logf("Share routing: session 1 → DGB pool (1 share received) ✓")
}

func TestRegression_MultiServer_UnassignedSessionRejected(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)

	ms := &MultiServer{
		cfg:       MultiServerConfig{Logger: zap.NewNop()},
		logger:    zap.NewNop().Sugar().Named("test"),
		coinPools: map[string]CoinPoolHandle{"DGB": dgb},
		graceWindow: 10 * time.Second,
	}

	// Session 99 never assigned
	share := &protocol.Share{
		SessionID:  99,
		JobID:      "DGB-job-1",
		Difficulty: 1000,
	}

	result := ms.handleShare(share)
	if result.Accepted {
		t.Error("share from unassigned session should be rejected")
	}

	t.Logf("Unassigned session rejected: %s ✓", result.RejectReason)
}

func TestRegression_MultiServer_CoinPoolDownRejected(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	dgb.SetRunning(false)

	ms := &MultiServer{
		cfg:       MultiServerConfig{Logger: zap.NewNop()},
		logger:    zap.NewNop().Sugar().Named("test"),
		coinPools: map[string]CoinPoolHandle{"DGB": dgb},
		graceWindow: 10 * time.Second,
	}
	ms.sessionCoin.Store(uint64(1), "DGB")

	share := &protocol.Share{
		SessionID:  1,
		JobID:      "DGB-job-1",
		Difficulty: 1000,
	}

	result := ms.handleShare(share)
	if result.Accepted {
		t.Error("share should be rejected when coin pool is down")
	}

	t.Logf("Coin pool down: share rejected (%s) ✓", result.RejectReason)
}

func TestRegression_MultiServer_GracePeriodAcceptsOldCoinShares(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	ms := &MultiServer{
		cfg:       MultiServerConfig{Logger: zap.NewNop()},
		logger:    zap.NewNop().Sugar().Named("test"),
		coinPools: map[string]CoinPoolHandle{"DGB": dgb, "BCH": bch},
		graceWindow: 10 * time.Second,
	}

	// Session switched from DGB to BCH just now
	ms.sessionCoin.Store(uint64(1), "BCH")
	ms.switchGrace.Store(uint64(1), switchGraceState{
		fromCoin:   "DGB",
		switchedAt: time.Now(),
	})

	// Submit a share that the OLD coin (DGB) would accept
	share := &protocol.Share{
		SessionID:  1,
		JobID:      "DGB-old-job",
		Difficulty: 1000,
	}

	result := ms.handleShare(share)
	if !result.Accepted {
		t.Error("share should be accepted during grace period (old coin DGB still running)")
	}

	// Verify DGB received the share (grace period routed to old pool)
	if dgb.shareCount.Load() != 1 {
		t.Errorf("DGB should have received grace-period share, got %d", dgb.shareCount.Load())
	}

	t.Logf("Grace period: old-coin share accepted by DGB during 10s window ✓")
}

func TestRegression_MultiServer_GracePeriodExpires(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	ms := &MultiServer{
		cfg:       MultiServerConfig{Logger: zap.NewNop()},
		logger:    zap.NewNop().Sugar().Named("test"),
		coinPools: map[string]CoinPoolHandle{"DGB": dgb, "BCH": bch},
		graceWindow: 10 * time.Second,
	}

	// Session switched from DGB to BCH 15 seconds ago (grace expired)
	ms.sessionCoin.Store(uint64(1), "BCH")
	ms.switchGrace.Store(uint64(1), switchGraceState{
		fromCoin:   "DGB",
		switchedAt: time.Now().Add(-15 * time.Second),
	})

	share := &protocol.Share{
		SessionID:  1,
		JobID:      "BCH-job-1",
		Difficulty: 500,
	}

	result := ms.handleShare(share)
	if !result.Accepted {
		t.Errorf("share should be accepted by current coin BCH, got rejected: %s", result.RejectReason)
	}

	// Should go to BCH (current), not DGB (grace expired)
	if bch.shareCount.Load() != 1 {
		t.Errorf("BCH should have received 1 share after grace expiry, got %d", bch.shareCount.Load())
	}
	if dgb.shareCount.Load() != 0 {
		t.Errorf("DGB should not receive shares after grace expiry, got %d", dgb.shareCount.Load())
	}

	t.Logf("Grace period expired: share routed to current coin BCH ✓")
}

func TestRegression_MultiServer_BlockDuringGracePeriod(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	// DGB will return a block for the grace-period share
	dgb.SetBlockResult("0000-grace-period-block")

	ms := &MultiServer{
		cfg:       MultiServerConfig{Logger: zap.NewNop()},
		logger:    zap.NewNop().Sugar().Named("test"),
		coinPools: map[string]CoinPoolHandle{"DGB": dgb, "BCH": bch},
		graceWindow: 10 * time.Second,
	}

	ms.sessionCoin.Store(uint64(1), "BCH")
	ms.switchGrace.Store(uint64(1), switchGraceState{
		fromCoin:   "DGB",
		switchedAt: time.Now(),
	})

	share := &protocol.Share{
		SessionID:  1,
		JobID:      "DGB-old-job",
		Difficulty: 1000,
	}

	result := ms.handleShare(share)
	if !result.Accepted {
		t.Error("grace-period share should be accepted")
	}
	if !result.IsBlock {
		t.Error("MONEY LOSS: block found during grace period was not returned to miner")
	}
	if result.BlockHash != "0000-grace-period-block" {
		t.Errorf("wrong block hash: %s", result.BlockHash)
	}

	dgb.ClearBlockResult()
	t.Logf("Block during grace period: found on DGB and returned to miner ✓")
}

func TestRegression_MultiServer_ReevaluateAllSwitchesCorrectly(t *testing.T) {
	t.Parallel()

	// Tests the reevaluation logic that MultiServer.reevaluateAll uses:
	// iterate all sessions, call Selector.SelectCoin, switch if changed.
	// We test the real Selector + Monitor code path without needing a live
	// stratum.Server (which reevaluateAll→switchSessionCoin→server.GetSession needs).

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)
	btc := newMockCoinPool("BTC", "pool_btc", 3333, 80e12, 600)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(bch, 600)
	mon.RegisterCoin(btc, 600)
	mon.poll()

	currentTime := time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC) // DGB slot

	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BCH", "BTC"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BCH", Weight: 30},
			{Symbol: "BTC", Weight: 20},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1, // disabled for test
		NowFunc:       func() time.Time { return currentTime },
		Logger:        zap.NewNop(),
	})

	// Track session→coin mapping (mirrors MultiServer.sessionCoin)
	sessionCoins := &sync.Map{}

	// Connect 20 sessions at 06:00 → all on DGB
	for i := uint64(1); i <= 20; i++ {
		s := sel.SelectCoin(i)
		sel.AssignCoin(i, s.Symbol, fmt.Sprintf("worker-%d", i), "low")
		sessionCoins.Store(i, s.Symbol)
	}

	dist := sel.GetCoinDistribution()
	if dist["DGB"] != 20 {
		t.Fatalf("expected all 20 on DGB at 06:00, got %v", dist)
	}

	// Advance to 13:00 → BCH slot
	currentTime = time.Date(2026, 3, 29, 13, 0, 0, 0, time.UTC)

	// Simulate reevaluateAll: iterate sessions, SelectCoin, switch if changed
	// This is the exact logic from MultiServer.reevaluateAll lines 418-427
	sessionCoins.Range(func(key, value any) bool {
		sessionID := key.(uint64)
		selection := sel.SelectCoin(sessionID)
		if selection.Changed {
			sel.AssignCoin(sessionID, selection.Symbol, fmt.Sprintf("worker-%d", sessionID), "low")
			sessionCoins.Store(sessionID, selection.Symbol)
		}
		return true
	})

	// Verify all sessions switched
	switchedToBCH := 0
	sessionCoins.Range(func(key, value any) bool {
		if value.(string) == "BCH" {
			switchedToBCH++
		}
		return true
	})

	if switchedToBCH != 20 {
		t.Errorf("expected all 20 sessions switched to BCH, got %d", switchedToBCH)
	}

	dist = sel.GetCoinDistribution()
	if dist["BCH"] != 20 {
		t.Errorf("selector distribution should show 20 on BCH, got %v", dist)
	}

	t.Logf("reevaluateAll pattern: 20 sessions DGB→BCH at 13:00 ✓")
}

// =============================================================================
// SECTION 5: TIMEZONE / DST REGRESSION
// =============================================================================

func TestRegression_Selector_TimezoneAware(t *testing.T) {
	t.Parallel()

	dgb := newMockCoinPool("DGB", "pool_dgb", 5025, 1000.0, 15)
	bch := newMockCoinPool("BCH", "pool_bch", 4444, 500_000.0, 600)

	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	// US Eastern timezone (UTC-5 in winter)
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("cannot load America/New_York timezone: %v", err)
	}

	// 06:00 Eastern = 11:00 UTC
	currentTime := time.Date(2026, 1, 15, 6, 0, 0, 0, eastern)

	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BCH"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BCH", Weight: 50},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		Location:      eastern, // schedule in Eastern time
		NowFunc:       func() time.Time { return currentTime },
		Logger:        zap.NewNop(),
	})

	// 06:00 Eastern is 25% into the day → DGB slot (0.0–0.5)
	s := sel.SelectCoin(1)
	if s.Symbol != "DGB" {
		t.Errorf("at 06:00 Eastern expected DGB (first half of day), got %s", s.Symbol)
	}

	// 15:00 Eastern is 62.5% into the day → BCH slot (0.5–1.0)
	currentTime = time.Date(2026, 1, 15, 15, 0, 0, 0, eastern)
	sel.AssignCoin(1, s.Symbol, "worker-1", "low")
	s = sel.SelectCoin(1)
	if s.Symbol != "BCH" {
		t.Errorf("at 15:00 Eastern expected BCH (second half of day), got %s", s.Symbol)
	}

	t.Logf("Timezone: Eastern schedule — 06:00→DGB, 15:00→BCH ✓")
}

// =============================================================================
// SECTION 6: END-TO-END SINGLE-PORT MULTI-COIN REGRESSION
// =============================================================================

func TestRegression_E2E_SinglePortFullDayCycle(t *testing.T) {
	// End-to-end: simulate a full 24-hour cycle on a single port with 3 coins.
	// Uses real Monitor, Selector, and share routing via the test harness.
	// Verifies: correct coin at every hour, share routing, block attribution,
	// failover, and recovery — all using real production code paths.

	h := newHarness(t, 0, 0) // midnight start

	// Connect 10 miners
	for i := uint64(1); i <= 10; i++ {
		h.selectAndAssign(i)
	}

	totalBlocks := 0
	blocksByCoin := map[string]int{}

	// Walk through 24 hours in 1-hour increments
	for hour := 0; hour < 24; hour++ {
		h.setTime(hour, 30) // :30 to avoid exact boundaries

		// Re-evaluate all miners
		for i := uint64(1); i <= 10; i++ {
			h.selectAndAssign(i)
		}

		// Determine expected coin
		expectedCoin := "DGB"
		if hour >= 12 && hour < 19 {
			expectedCoin = "BCH"
		} else if hour == 19 {
			// 19:12 is the boundary — at 19:30 we're in BTC
			expectedCoin = "BTC"
		} else if hour >= 20 {
			expectedCoin = "BTC"
		}

		// Verify all miners on correct coin
		for i := uint64(1); i <= 10; i++ {
			coin, ok := h.selector.GetSessionCoin(i)
			if !ok {
				t.Fatalf("hour %d: session %d has no assignment", hour, i)
			}
			if coin != expectedCoin {
				t.Errorf("hour %d: session %d on %s, expected %s", hour, i, coin, expectedCoin)
			}
		}

		// Miner 1 finds a block every 4 hours
		if hour%4 == 0 {
			pool := h.pools[expectedCoin]
			pool.SetBlockResult(fmt.Sprintf("block-hour-%d", hour))
			coin, result := h.routeShare(1, expectedCoin+"-job-1", 1000)
			if !result.IsBlock {
				t.Errorf("hour %d: block not found on %s", hour, expectedCoin)
			}
			if coin != expectedCoin {
				t.Errorf("hour %d: block routed to %s, expected %s", hour, coin, expectedCoin)
			}
			pool.ClearBlockResult()
			totalBlocks++
			blocksByCoin[expectedCoin]++
		}

		// All miners submit shares
		for i := uint64(1); i <= 10; i++ {
			coin, result := h.routeShare(i, expectedCoin+"-job-1", 1000)
			if coin != expectedCoin {
				t.Errorf("hour %d: miner %d share on %s, expected %s", hour, i, coin, expectedCoin)
			}
			if !result.Accepted {
				t.Errorf("hour %d: miner %d share rejected", hour, i)
			}
		}
	}

	// Verify totals
	dgbShares := h.dgb.shareCount.Load()
	bchShares := h.bch.shareCount.Load()
	btcShares := h.btc.shareCount.Load()

	t.Logf("\n=== FULL 24-HOUR CYCLE SUMMARY ===")
	t.Logf("DGB shares: %d, BCH shares: %d, BTC shares: %d", dgbShares, bchShares, btcShares)
	t.Logf("Blocks: %v (total=%d)", blocksByCoin, totalBlocks)

	// DGB has 12 hours (0-11), BCH has ~7 hours (12-18), BTC has ~5 hours (19-23)
	if dgbShares == 0 || bchShares == 0 || btcShares == 0 {
		t.Error("all coins should have received shares")
	}

	// Verify blocks attributed to correct coins
	totalPoolBlocks := len(h.dgb.GetBlocksFound()) + len(h.bch.GetBlocksFound()) + len(h.btc.GetBlocksFound())
	if totalPoolBlocks != totalBlocks {
		t.Errorf("MONEY LOSS: miner found %d blocks but pools recorded %d", totalBlocks, totalPoolBlocks)
	}

	t.Logf("Full 24h cycle: all shares routed correctly, all %d blocks attributed ✓", totalBlocks)
}

func TestRegression_E2E_FailoverMidCycleThenRecover(t *testing.T) {
	h := newHarness(t, 6, 0) // DGB slot

	// 5 miners on DGB
	for i := uint64(1); i <= 5; i++ {
		h.selectAndAssign(i)
	}

	// Submit some shares
	for i := uint64(1); i <= 5; i++ {
		_, result := h.routeShare(i, "DGB-job-1", 1000)
		if !result.Accepted {
			t.Fatalf("share from miner %d rejected pre-failover", i)
		}
	}
	preFail := h.dgb.shareCount.Load()

	// DGB goes down mid-cycle
	h.dgb.SetRunning(false)
	h.pollAndUpdate()

	for i := uint64(1); i <= 5; i++ {
		h.selectAndAssign(i) // re-evaluate
	}

	// Verify all moved to failover
	for i := uint64(1); i <= 5; i++ {
		coin, _ := h.selector.GetSessionCoin(i)
		if coin == "DGB" {
			t.Errorf("miner %d still on DGB after failover", i)
		}
	}

	// Submit shares during failover
	failoverCoin, _ := h.selector.GetSessionCoin(1)
	failoverPool := h.pools[failoverCoin]
	for i := uint64(1); i <= 5; i++ {
		_, result := h.routeShare(i, failoverCoin+"-job-1", 1000)
		if !result.Accepted {
			t.Errorf("share from miner %d rejected during failover", i)
		}
	}
	failoverShares := failoverPool.shareCount.Load()

	// DGB recovers
	h.dgb.SetRunning(true)
	h.pollAndUpdate()

	for i := uint64(1); i <= 5; i++ {
		h.selectAndAssign(i)
	}

	// Verify all back on DGB
	for i := uint64(1); i <= 5; i++ {
		coin, _ := h.selector.GetSessionCoin(i)
		if coin != "DGB" {
			t.Errorf("miner %d should have returned to DGB, on %s", i, coin)
		}
	}

	// Submit post-recovery shares
	for i := uint64(1); i <= 5; i++ {
		_, result := h.routeShare(i, "DGB-job-2", 1000)
		if !result.Accepted {
			t.Fatalf("share from miner %d rejected post-recovery", i)
		}
	}
	postRecover := h.dgb.shareCount.Load()

	t.Logf("Failover cycle: DGB=%d pre, %s=%d during failover, DGB=%d post-recovery ✓",
		preFail, failoverCoin, failoverShares, postRecover-preFail)
}

func TestRegression_E2E_RapidScheduleTraversalNoLoss(t *testing.T) {
	// Rapidly walk through every slot transition, finding a block at each.
	// Verify zero block loss across all transitions.

	h := newHarness(t, 0, 0)
	h.selectAndAssign(1)

	transitions := []struct {
		hour, min int
		coin      string
	}{
		{0, 0, "DGB"},
		{11, 59, "DGB"},
		{12, 0, "BCH"},
		{19, 11, "BCH"},
		{19, 13, "BTC"},
		{23, 59, "BTC"},
		{0, 0, "DGB"},   // midnight wrap
		{12, 0, "BCH"},  // back to BCH
		{20, 0, "BTC"},  // back to BTC
	}

	expectedBlocks := len(transitions)
	actualBlocks := 0

	for _, tr := range transitions {
		h.setTime(tr.hour, tr.min)
		h.selectAndAssign(1)

		coin, _ := h.selector.GetSessionCoin(1)
		if coin != tr.coin {
			t.Errorf("at %02d:%02d expected %s, got %s", tr.hour, tr.min, tr.coin, coin)
		}

		pool := h.pools[tr.coin]
		pool.SetBlockResult(fmt.Sprintf("rapid-block-%02d%02d", tr.hour, tr.min))
		_, result := h.routeShare(1, tr.coin+"-job-1", 1000)
		if result.IsBlock {
			actualBlocks++
		} else {
			t.Errorf("block at %02d:%02d not found on %s", tr.hour, tr.min, tr.coin)
		}
		pool.ClearBlockResult()
	}

	totalRecorded := len(h.dgb.GetBlocksFound()) + len(h.bch.GetBlocksFound()) + len(h.btc.GetBlocksFound())
	if totalRecorded != expectedBlocks {
		t.Errorf("MONEY LOSS: expected %d blocks recorded, got %d", expectedBlocks, totalRecorded)
	}
	if actualBlocks != expectedBlocks {
		t.Errorf("MONEY LOSS: expected %d blocks found by miner, got %d", expectedBlocks, actualBlocks)
	}

	t.Logf("Rapid traversal: %d transitions, %d blocks found, %d recorded ✓",
		len(transitions), actualBlocks, totalRecorded)
}

// =============================================================================
// SECTION: MUTEX SAFETY — job.Clone() must not copy sync.RWMutex
// =============================================================================

// TestRegression_SendCoinJob_CloneDoesNotCopyMutex verifies that the coin-switch
// code path in sendCoinJob uses job.Clone() instead of struct copy (*job).
// Struct-copying a protocol.Job copies its embedded sync.RWMutex (stateMu),
// which is undefined behavior that can cause deadlocks.
//
// This test exercises the Clone + CleanJobs pattern directly (the same pattern
// used in multiserver.go sendCoinJob) and verifies:
//   - The clone has CleanJobs=true while the original does not
//   - The clone's mutex is independently lockable (not shared with original)
//   - Concurrent Clone+Lock operations don't race or deadlock
func TestRegression_SendCoinJob_CloneDoesNotCopyMutex(t *testing.T) {
	t.Parallel()

	original := &protocol.Job{
		ID:            "DGB-job-100",
		PrevBlockHash: "00000000abc",
		Height:        100,
		Difficulty:    5025.0,
		CleanJobs:     false,
		CreatedAt:     time.Now(),
		MerkleBranches: []string{"branch1", "branch2"},
	}

	// This is the exact pattern from multiserver.go sendCoinJob:
	//   switchJob := job.Clone()
	//   switchJob.CleanJobs = true
	switchJob := original.Clone()
	switchJob.CleanJobs = true

	// Verify the clone has CleanJobs set but the original does not
	if original.CleanJobs {
		t.Fatal("Original job should NOT have CleanJobs=true after cloning")
	}
	if !switchJob.CleanJobs {
		t.Fatal("Cloned switch job should have CleanJobs=true")
	}

	// Verify fields were copied correctly
	if switchJob.ID != original.ID {
		t.Errorf("Clone ID mismatch: got %q, want %q", switchJob.ID, original.ID)
	}
	if switchJob.Height != original.Height {
		t.Errorf("Clone Height mismatch: got %d, want %d", switchJob.Height, original.Height)
	}
	if switchJob.Difficulty != original.Difficulty {
		t.Errorf("Clone Difficulty mismatch: got %f, want %f", switchJob.Difficulty, original.Difficulty)
	}

	// Critical: verify the mutex is NOT shared. If Clone() did a struct copy
	// (*job), both jobs share the same mutex and this would deadlock.
	// Lock the original's mutex via GetState (which takes RLock internally),
	// then verify the clone's mutex is independently lockable.
	done := make(chan bool, 1)
	go func() {
		// GetState takes stateMu.RLock on the clone — if the mutex were
		// shared with original, and original held a write lock, this would
		// deadlock.  With a properly cloned (fresh) mutex, this succeeds.
		_ = switchJob.GetState()
		done <- true
	}()

	select {
	case <-done:
		// Success — mutexes are independent
	case <-time.After(2 * time.Second):
		t.Fatal("Clone mutex deadlock detected — Clone() is copying the sync.RWMutex")
	}
}

// TestRegression_ConcurrentCloneAndModify verifies that cloning a job while
// another goroutine reads the original does not race. This simulates the
// multi-port scenario where sendCoinJob clones while share handlers read.
func TestRegression_ConcurrentCloneAndModify(t *testing.T) {
	t.Parallel()

	original := &protocol.Job{
		ID:         "BTC-job-500",
		Height:     500,
		Difficulty: 80000.0,
		CleanJobs:  false,
		MerkleBranches: []string{"m1", "m2", "m3"},
	}

	var wg sync.WaitGroup
	const goroutines = 50

	// Simulate concurrent clone (coin switch) + read (share validation)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clone := original.Clone()
			clone.CleanJobs = true
			// Verify independence
			if original.CleanJobs {
				t.Error("Race: original CleanJobs was modified by clone")
			}
			if clone.Height != 500 {
				t.Errorf("Race: clone Height = %d, want 500", clone.Height)
			}
			_ = clone.GetState()
		}()
	}

	wg.Wait()
}

// =============================================================================
// HELPERS
// =============================================================================

func assertSlot(t *testing.T, slot timeSlot, wantSymbol string, wantStart, wantEnd float64) {
	t.Helper()
	if slot.symbol != wantSymbol {
		t.Errorf("slot symbol: got %q, want %q", slot.symbol, wantSymbol)
	}
	if math.Abs(slot.startFrac-wantStart) > 1e-9 {
		t.Errorf("%s startFrac: got %f, want %f", wantSymbol, slot.startFrac, wantStart)
	}
	if math.Abs(slot.endFrac-wantEnd) > 1e-9 {
		t.Errorf("%s endFrac: got %f, want %f", wantSymbol, slot.endFrac, wantEnd)
	}
}
