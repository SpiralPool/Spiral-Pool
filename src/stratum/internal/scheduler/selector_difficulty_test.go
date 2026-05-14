// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// newDifficultySelector creates a Selector in DIFFICULTY mode backed by the
// given monitor. minTime=-1 disables the min-time guard for tests that don't
// need it.
func newDifficultySelector(t *testing.T, mon *Monitor, allowedCoins []string, preferCoin string, minTime time.Duration) *Selector {
	t.Helper()
	return NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  allowedCoins,
		CoinWeights:   []CoinWeight{{Symbol: allowedCoins[0], Weight: 100}}, // weights ignored in DIFFICULTY mode
		PreferCoin:    preferCoin,
		MinTimeOnCoin: minTime,
		Mode:          RoutingModeDifficulty,
		Logger:        zap.NewNop(),
	})
}

// pollAndRegister registers sources with the monitor and triggers an immediate poll.
func pollAndRegister(mon *Monitor, sources ...*mockCoinSource) {
	for _, src := range sources {
		mon.RegisterCoin(src, 600)
	}
	mon.poll()
}

// =============================================================================
// TestDifficultyMode_SelectsLowestDiff
// Two coins: A (diff=1000) and B (diff=50000). Expect A is always selected.
// =============================================================================

func TestDifficultyMode_SelectsLowestDiff(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	easy := newMockSource("EASY", "pool_easy", 1000.0)
	hard := newMockSource("HARD", "pool_hard", 50000.0)
	pollAndRegister(mon, easy, hard)

	sel := newDifficultySelector(t, mon, []string{"EASY", "HARD"}, "EASY", -1)

	got := sel.SelectCoin(1)
	if got.Symbol != "EASY" {
		t.Errorf("expected EASY (lowest diff), got %s", got.Symbol)
	}
	if !got.Changed {
		t.Error("first assignment should have Changed=true")
	}
	if got.Reason != "initial_assignment_difficulty" {
		t.Errorf("expected reason initial_assignment_difficulty, got %s", got.Reason)
	}
}

// =============================================================================
// TestDifficultyMode_StillSelectsLowestAfterAssignment
// After initial assignment, SelectCoin should keep returning the easiest coin.
// =============================================================================

func TestDifficultyMode_StillSelectsLowestAfterAssignment(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	easy := newMockSource("EASY", "pool_easy", 1000.0)
	hard := newMockSource("HARD", "pool_hard", 50000.0)
	pollAndRegister(mon, easy, hard)

	sel := newDifficultySelector(t, mon, []string{"EASY", "HARD"}, "EASY", -1)

	// Assign and call again — should still be EASY with Changed=false.
	first := sel.SelectCoin(1)
	sel.AssignCoin(1, first.Symbol, "worker1", "mid")

	second := sel.SelectCoin(1)
	if second.Symbol != "EASY" {
		t.Errorf("expected EASY on second call, got %s", second.Symbol)
	}
	if second.Changed {
		t.Error("same coin should have Changed=false")
	}
	if second.Reason != "difficulty_same" {
		t.Errorf("expected reason difficulty_same, got %s", second.Reason)
	}
}

// =============================================================================
// TestDifficultyMode_SwitchesWhenEasierCoinAppears
// After assignment, if the other coin becomes easier, selector should switch.
// =============================================================================

func TestDifficultyMode_SwitchesWhenEasierCoinAppears(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	coinA := newMockSource("A", "pool_a", 50000.0)
	coinB := newMockSource("B", "pool_b", 1000.0)
	pollAndRegister(mon, coinA, coinB)

	sel := newDifficultySelector(t, mon, []string{"A", "B"}, "B", -1)

	// Initial selection: B is easiest.
	first := sel.SelectCoin(1)
	if first.Symbol != "B" {
		t.Fatalf("expected B initially, got %s", first.Symbol)
	}
	sel.AssignCoin(1, first.Symbol, "worker1", "mid")

	// Now A becomes much easier than B.
	coinA.SetDifficulty(100.0)
	mon.poll()

	second := sel.SelectCoin(1)
	if second.Symbol != "A" {
		t.Errorf("expected switch to A (now easier), got %s", second.Symbol)
	}
	if !second.Changed {
		t.Error("coin switch should have Changed=true")
	}
	if second.Reason != "difficulty_rotation" {
		t.Errorf("expected reason difficulty_rotation, got %s", second.Reason)
	}
}

// =============================================================================
// TestDifficultyMode_FallbackOnNoData
// When no coins have difficulty data, return preferCoin.
// =============================================================================

func TestDifficultyMode_FallbackOnNoData(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	// Register coins but don't poll — state.Available remains false.
	coinA := newMockSource("A", "pool_a", 1000.0)
	coinB := newMockSource("B", "pool_b", 2000.0)
	mon.RegisterCoin(coinA, 600)
	mon.RegisterCoin(coinB, 600)
	// No mon.poll() — NetworkDiff stays 0, Available stays false.

	sel := newDifficultySelector(t, mon, []string{"A", "B"}, "A", -1)

	got := sel.SelectCoin(42)
	if got.Symbol != "A" {
		t.Errorf("expected preferCoin A as fallback, got %s", got.Symbol)
	}
	if got.Reason != "no_coins_available" {
		t.Errorf("expected reason no_coins_available, got %s", got.Reason)
	}
}

// =============================================================================
// TestDifficultyMode_UnavailableCoinSkipped
// If the easiest coin's node goes down, the next easiest should be selected.
// =============================================================================

func TestDifficultyMode_UnavailableCoinSkipped(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	easy := newMockSource("EASY", "pool_easy", 1000.0)
	hard := newMockSource("HARD", "pool_hard", 50000.0)
	pollAndRegister(mon, easy, hard)

	sel := newDifficultySelector(t, mon, []string{"EASY", "HARD"}, "HARD", -1)

	// First call selects EASY.
	first := sel.SelectCoin(1)
	if first.Symbol != "EASY" {
		t.Fatalf("expected EASY initially, got %s", first.Symbol)
	}
	sel.AssignCoin(1, first.Symbol, "worker1", "mid")

	// EASY's node goes down.
	easy.SetRunning(false)
	mon.poll() // Monitor marks EASY unavailable.

	got := sel.SelectCoin(1)
	if got.Symbol != "HARD" {
		t.Errorf("expected HARD (next easiest after EASY went down), got %s", got.Symbol)
	}
}

// =============================================================================
// TestDifficultyMode_MinTimeOnCoinRespected
// If the session was assigned less than minTimeOnCoin ago, no switch even if
// an easier coin is available.
// =============================================================================

func TestDifficultyMode_MinTimeOnCoinRespected(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	coinA := newMockSource("A", "pool_a", 1000.0)
	coinB := newMockSource("B", "pool_b", 2000.0)
	pollAndRegister(mon, coinA, coinB)

	// minTimeOnCoin = 10 minutes — session was just assigned.
	sel := newDifficultySelector(t, mon, []string{"A", "B"}, "A", 10*time.Minute)

	first := sel.SelectCoin(1)
	sel.AssignCoin(1, first.Symbol, "worker1", "mid")

	// A is now harder, B is easiest — but min-time hasn't elapsed.
	coinA.SetDifficulty(99999.0)
	mon.poll()

	got := sel.SelectCoin(1)
	// Should stay on A because min_time_on_coin hasn't elapsed.
	if got.Symbol != first.Symbol {
		t.Errorf("expected to stay on %s (min_time not elapsed), got %s", first.Symbol, got.Symbol)
	}
	if got.Reason != "min_time_not_elapsed" {
		t.Errorf("expected reason min_time_not_elapsed, got %s", got.Reason)
	}
}

// =============================================================================
// TestDifficultyMode_SingleCoin
// With only one available coin, always select it.
// =============================================================================

func TestDifficultyMode_SingleCoin(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	only := newMockSource("ONLY", "pool_only", 12345.0)
	pollAndRegister(mon, only)

	sel := newDifficultySelector(t, mon, []string{"ONLY"}, "ONLY", -1)

	got := sel.SelectCoin(7)
	if got.Symbol != "ONLY" {
		t.Errorf("expected ONLY, got %s", got.Symbol)
	}
}

// =============================================================================
// TestTimeModeUnchanged_AfterAddingModeField
// Confirm that passing Mode="" or Mode="TIME" still behaves exactly as before
// (regression guard for the time-based path).
// =============================================================================

func TestTimeModeUnchanged_EmptyModeDefaultsToTime(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	btc := newMockSource("BTC", "pool_btc", 50000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.poll()

	// Mode="" should default to TIME, not DIFFICULTY.
	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  []string{"DGB", "BTC"},
		CoinWeights:   []CoinWeight{{Symbol: "DGB", Weight: 80}, {Symbol: "BTC", Weight: 20}},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		Mode:          "",
		NowFunc:       func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
		Logger:        zap.NewNop(),
	})

	if sel.Mode() != RoutingModeTime {
		t.Errorf("empty mode should default to RoutingModeTime, got %s", sel.Mode())
	}

	// At 12:00 UTC, DGB occupies 80% of the day (00:00–19:12), so it should be selected.
	got := sel.SelectCoin(1)
	if got.Symbol != "DGB" {
		t.Errorf("time-based: expected DGB at 12:00, got %s", got.Symbol)
	}
}

func TestTimeModeUnchanged_ExplicitTimeModeIgnoresDifficulty(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	// BTC has much lower difficulty — but we're in TIME mode so it shouldn't matter.
	dgb := newMockSource("DGB", "pool_dgb", 1000000.0) // high diff
	btc := newMockSource("BTC", "pool_btc", 1.0)       // very low diff
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.poll()

	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  []string{"DGB", "BTC"},
		CoinWeights:   []CoinWeight{{Symbol: "DGB", Weight: 95}, {Symbol: "BTC", Weight: 5}},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		Mode:          RoutingModeTime,
		// 12:00 UTC falls solidly in DGB's 95% window (00:00–22:48)
		NowFunc: func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
		Logger:  zap.NewNop(),
	})

	got := sel.SelectCoin(1)
	// DGB has higher difficulty but TIME mode ignores that — it's 12:00 in DGB's slot.
	if got.Symbol != "DGB" {
		t.Errorf("time mode: expected DGB (scheduled), got %s (difficulty should be ignored)", got.Symbol)
	}
}

// =============================================================================
// TestDifficultyMode_ModeAccessor
// Confirm Mode() returns the correct enum value.
// =============================================================================

func TestDifficultyMode_ModeAccessor(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	src := newMockSource("A", "pool_a", 1.0)
	mon.RegisterCoin(src, 600)

	selDiff := NewSelector(SelectorConfig{
		Monitor: mon, AllowedCoins: []string{"A"},
		CoinWeights: []CoinWeight{{Symbol: "A", Weight: 100}},
		Mode: RoutingModeDifficulty, Logger: zap.NewNop(),
	})
	if selDiff.Mode() != RoutingModeDifficulty {
		t.Errorf("expected RoutingModeDifficulty, got %s", selDiff.Mode())
	}

	selTime := NewSelector(SelectorConfig{
		Monitor: mon, AllowedCoins: []string{"A"},
		CoinWeights: []CoinWeight{{Symbol: "A", Weight: 100}},
		Mode: RoutingModeTime, Logger: zap.NewNop(),
	})
	if selTime.Mode() != RoutingModeTime {
		t.Errorf("expected RoutingModeTime, got %s", selTime.Mode())
	}
}

// =============================================================================
// TestDifficultyMode_ExcludedCoinSkipped
// Excluded coin is never selected even when it has the lowest difficulty.
// =============================================================================

func TestDifficultyMode_ExcludedCoinSkipped(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	easy := newMockSource("EASY", "pool_easy", 100.0)   // lowest diff — but excluded
	medium := newMockSource("MED", "pool_med", 5000.0)  // should be selected
	hard := newMockSource("HARD", "pool_hard", 50000.0) // highest diff
	pollAndRegister(mon, easy, medium, hard)

	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  []string{"EASY", "MED", "HARD"},
		ExcludeCoins:  []string{"EASY"},
		CoinWeights:   []CoinWeight{{Symbol: "MED", Weight: 100}},
		PreferCoin:    "MED",
		MinTimeOnCoin: -1,
		Mode:          RoutingModeDifficulty,
		Logger:        zap.NewNop(),
	})

	got := sel.SelectCoin(1)
	if got.Symbol != "MED" {
		t.Errorf("expected MED (lowest non-excluded diff), got %s", got.Symbol)
	}
	if !got.Changed {
		t.Error("first assignment should have Changed=true")
	}
}

// =============================================================================
// TestDifficultyMode_ExcludedCurrentCoinBypassesMinTime
// When a miner is already on an excluded coin, min_time_on_coin is bypassed
// so it switches to an eligible coin immediately.
// =============================================================================

func TestDifficultyMode_ExcludedCurrentCoinBypassesMinTime(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	excluded := newMockSource("EXCL", "pool_excl", 100.0)
	eligible := newMockSource("GOOD", "pool_good", 5000.0)
	pollAndRegister(mon, excluded, eligible)

	// min_time = 10 minutes — would normally block a switch
	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  []string{"EXCL", "GOOD"},
		ExcludeCoins:  []string{"EXCL"},
		CoinWeights:   []CoinWeight{{Symbol: "GOOD", Weight: 100}},
		PreferCoin:    "GOOD",
		MinTimeOnCoin: 10 * time.Minute,
		Mode:          RoutingModeDifficulty,
		Logger:        zap.NewNop(),
	})

	// Manually place session on the excluded coin (simulates config change after assignment)
	sel.AssignCoin(1, "EXCL", "worker1", "asic")

	// Immediately re-evaluate — min_time should be bypassed because EXCL is excluded
	got := sel.SelectCoin(1)
	if got.Symbol != "GOOD" {
		t.Errorf("expected GOOD (excluded coin bypasses min_time), got %s", got.Symbol)
	}
}

// =============================================================================
// TestDifficultyMode_AllCoinsExcluded_FallbackToPrefer
// When all eligible coins are excluded, falls back to prefer_coin.
// =============================================================================

func TestDifficultyMode_AllCoinsExcluded_FallbackToPrefer(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})
	a := newMockSource("A", "pool_a", 100.0)
	b := newMockSource("B", "pool_b", 200.0)
	pollAndRegister(mon, a, b)

	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  []string{"A", "B"},
		ExcludeCoins:  []string{"A", "B"}, // everything excluded
		CoinWeights:   []CoinWeight{{Symbol: "A", Weight: 100}},
		PreferCoin:    "A",
		MinTimeOnCoin: -1,
		Mode:          RoutingModeDifficulty,
		Logger:        zap.NewNop(),
	})

	got := sel.SelectCoin(1)
	if got.Symbol != "A" {
		t.Errorf("expected fallback to prefer_coin A, got %s", got.Symbol)
	}
	if got.Reason != "no_coins_available" {
		t.Errorf("expected reason no_coins_available, got %s", got.Reason)
	}
}

// =============================================================================
// TestDifficultyMode_LiveDifficultyDrop_TriggersSwitch
//
// Scenario: 4 coins. DELTA is excluded. Miner starts on ALPHA (lowest eligible).
// ALPHA's difficulty then spikes while BETA's drops below it.
// Expected: miner switches to BETA. DELTA is never selected despite having the
// absolute lowest difficulty of all four coins throughout.
// =============================================================================

func TestDifficultyMode_LiveDifficultyDrop_TriggersSwitch(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})

	// Initial difficulties: ALPHA easiest eligible, DELTA lower but excluded
	alpha := newMockSource("ALPHA", "pool_alpha", 2000.0)
	beta  := newMockSource("BETA",  "pool_beta",  8000.0)
	gamma := newMockSource("GAMMA", "pool_gamma", 15000.0)
	delta := newMockSource("DELTA", "pool_delta", 100.0) // excluded — lowest overall
	pollAndRegister(mon, alpha, beta, gamma, delta)

	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  []string{"ALPHA", "BETA", "GAMMA", "DELTA"},
		ExcludeCoins:  []string{"DELTA"},
		CoinWeights:   []CoinWeight{{Symbol: "ALPHA", Weight: 100}},
		PreferCoin:    "ALPHA",
		MinTimeOnCoin: -1, // disabled so we can observe switches immediately
		Mode:          RoutingModeDifficulty,
		Logger:        zap.NewNop(),
	})

	// Step 1 — initial assignment: ALPHA should win (lowest eligible)
	first := sel.SelectCoin(1)
	if first.Symbol != "ALPHA" {
		t.Fatalf("step 1: expected ALPHA (lowest eligible, diff=2000), got %s (diff unknown)", first.Symbol)
	}
	if first.Reason != "initial_assignment_difficulty" {
		t.Errorf("step 1: expected reason initial_assignment_difficulty, got %s", first.Reason)
	}
	sel.AssignCoin(1, first.Symbol, "worker1", "asic")

	// Step 2 — ALPHA's difficulty spikes; BETA drops to 500 (new lowest eligible)
	// DELTA stays at 100 but is excluded — must never be selected.
	alpha.SetDifficulty(999999.0)
	beta.SetDifficulty(500.0)
	mon.poll()

	second := sel.SelectCoin(1)
	if second.Symbol != "BETA" {
		t.Errorf("step 2: expected switch to BETA (diff=500, now lowest eligible), got %s", second.Symbol)
	}
	if !second.Changed {
		t.Error("step 2: Changed should be true — coin switched from ALPHA to BETA")
	}
	if second.Reason != "difficulty_rotation" {
		t.Errorf("step 2: expected reason difficulty_rotation, got %s", second.Reason)
	}
	sel.AssignCoin(1, second.Symbol, "worker1", "asic")

	// Step 3 — GAMMA drops to 50 (new lowest eligible). DELTA still at 100 but excluded.
	gamma.SetDifficulty(50.0)
	mon.poll()

	third := sel.SelectCoin(1)
	if third.Symbol != "GAMMA" {
		t.Errorf("step 3: expected switch to GAMMA (diff=50, now lowest eligible), got %s", third.Symbol)
	}
	if !third.Changed {
		t.Error("step 3: Changed should be true — coin switched from BETA to GAMMA")
	}

	// Step 4 — verify DELTA was never selected at any point
	// (implicitly proven by the above, but make it explicit)
	if first.Symbol == "DELTA" || second.Symbol == "DELTA" || third.Symbol == "DELTA" {
		t.Error("DELTA was selected at some point — it should always be excluded")
	}
}

// =============================================================================
// TestDifficultyMode_FiveCoins_TwoExcluded_PicksLowestEligible
//
// Scenario: 5 coins. COIN4 and COIN5 are excluded and have the two lowest
// difficulties of all five. Verifies that across multiple difficulty changes
// the selector always picks the lowest-difficulty coin among the 3 eligible
// coins only, and never touches the excluded pair regardless of their ranking.
// =============================================================================

func TestDifficultyMode_FiveCoins_TwoExcluded_PicksLowestEligible(t *testing.T) {
	mon := NewMonitor(MonitorConfig{PollInterval: time.Hour, Logger: zap.NewNop()})

	coin1 := newMockSource("COIN1", "pool_1", 1000.0)
	coin2 := newMockSource("COIN2", "pool_2", 5000.0)
	coin3 := newMockSource("COIN3", "pool_3", 10000.0)
	coin4 := newMockSource("COIN4", "pool_4", 50.0)   // excluded — lowest overall
	coin5 := newMockSource("COIN5", "pool_5", 200.0)  // excluded — second lowest overall
	pollAndRegister(mon, coin1, coin2, coin3, coin4, coin5)

	allowed  := []string{"COIN1", "COIN2", "COIN3", "COIN4", "COIN5"}
	excluded := []string{"COIN4", "COIN5"}
	sel := NewSelector(SelectorConfig{
		Monitor:       mon,
		AllowedCoins:  allowed,
		ExcludeCoins:  excluded,
		CoinWeights:   []CoinWeight{{Symbol: "COIN1", Weight: 100}},
		PreferCoin:    "COIN1",
		MinTimeOnCoin: -1,
		Mode:          RoutingModeDifficulty,
		Logger:        zap.NewNop(),
	})

	assertNeverExcluded := func(t *testing.T, got CoinSelection, step string) {
		t.Helper()
		if got.Symbol == "COIN4" || got.Symbol == "COIN5" {
			t.Errorf("%s: selected excluded coin %s", step, got.Symbol)
		}
	}

	// Round 1 — COIN1 is lowest eligible (1000 < 5000 < 10000)
	r1 := sel.SelectCoin(1)
	assertNeverExcluded(t, r1, "round 1")
	if r1.Symbol != "COIN1" {
		t.Errorf("round 1: expected COIN1 (diff=1000), got %s", r1.Symbol)
	}
	sel.AssignCoin(1, r1.Symbol, "miner", "asic")

	// Round 2 — COIN1 spikes; COIN2 drops to 300 → lowest eligible
	coin1.SetDifficulty(999999.0)
	coin2.SetDifficulty(300.0)
	mon.poll()

	r2 := sel.SelectCoin(1)
	assertNeverExcluded(t, r2, "round 2")
	if r2.Symbol != "COIN2" {
		t.Errorf("round 2: expected COIN2 (diff=300, new lowest eligible), got %s", r2.Symbol)
	}
	if !r2.Changed {
		t.Error("round 2: expected Changed=true (switched from COIN1 to COIN2)")
	}
	sel.AssignCoin(1, r2.Symbol, "miner", "asic")

	// Round 3 — COIN3 drops to 100 → lowest eligible; COIN4 still at 50 (excluded)
	coin3.SetDifficulty(100.0)
	mon.poll()

	r3 := sel.SelectCoin(1)
	assertNeverExcluded(t, r3, "round 3")
	if r3.Symbol != "COIN3" {
		t.Errorf("round 3: expected COIN3 (diff=100, lowest eligible), got %s", r3.Symbol)
	}
	if !r3.Changed {
		t.Error("round 3: expected Changed=true (switched from COIN2 to COIN3)")
	}
	sel.AssignCoin(1, r3.Symbol, "miner", "asic")

	// Round 4 — COIN2 drops to 10 → lowest eligible again; excluded still excluded
	coin2.SetDifficulty(10.0)
	mon.poll()

	r4 := sel.SelectCoin(1)
	assertNeverExcluded(t, r4, "round 4")
	if r4.Symbol != "COIN2" {
		t.Errorf("round 4: expected COIN2 (diff=10, lowest eligible), got %s", r4.Symbol)
	}
	if !r4.Changed {
		t.Error("round 4: expected Changed=true (switched from COIN3 to COIN2)")
	}
}
