// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"math"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// HELPER: injectable time
// =============================================================================

func mockTime(hour, minute int) time.Time {
	return time.Date(2026, 3, 29, hour, minute, 0, 0, time.UTC)
}

func makeNowFunc(hour, minute int) func() time.Time {
	t := mockTime(hour, minute)
	return func() time.Time { return t }
}

// =============================================================================
// SELECTOR TESTS (24-hour weighted scheduling)
// =============================================================================

func setupSelectorAt(t *testing.T, hour, minute int) (*Monitor, *Selector, *mockCoinSource, *mockCoinSource, *mockCoinSource) {
	t.Helper()

	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	btc := newMockSource("BTC", "pool_btc", 50000.0)
	bch := newMockSource("BCH", "pool_bch", 500000.0)

	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	// BCH=15%, BTC=5%, DGB=80% (alphabetical order after sort)
	// Time slots: BCH 00:00–03:36, BTC 03:36–04:48, DGB 04:48–24:00
	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BTC", "BCH"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 80},
			{Symbol: "BCH", Weight: 15},
			{Symbol: "BTC", Weight: 5},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		NowFunc:       makeNowFunc(hour, minute),
		Logger:        zap.NewNop(),
	})

	return mon, sel, dgb, btc, bch
}

func TestSelectorInitialAssignment(t *testing.T) {
	// At 10:00 UTC → DGB slot (00:00–19:12)
	_, sel, _, _, _ := setupSelectorAt(t, 10, 0)

	selection := sel.SelectCoin(1)
	if !selection.Changed {
		t.Error("expected Changed=true for initial assignment")
	}
	if selection.Reason != "initial_assignment" {
		t.Errorf("reason = %q, want initial_assignment", selection.Reason)
	}
	if selection.Symbol != "DGB" {
		t.Errorf("at 10:00 UTC: got %s, want DGB", selection.Symbol)
	}
}

func TestSelectorTimeSlots(t *testing.T) {
	// Sorted alphabetically: BCH=15%, BTC=5%, DGB=80%
	// BCH: 00:00–03:36, BTC: 03:36–04:48, DGB: 04:48–24:00

	tests := []struct {
		hour, minute int
		wantCoin     string
		desc         string
	}{
		{0, 0, "BCH", "midnight — start of BCH slot"},
		{2, 0, "BCH", "02:00 — middle of BCH slot"},
		{3, 40, "BTC", "03:40 — BTC slot (03:36–04:48)"},
		{5, 0, "DGB", "05:00 — DGB slot (04:48–24:00)"},
		{10, 0, "DGB", "10:00 — middle of DGB slot"},
		{19, 0, "DGB", "19:00 — DGB slot"},
		{23, 59, "DGB", "23:59 — end of DGB slot"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			_, sel, _, _, _ := setupSelectorAt(t, tt.hour, tt.minute)
			selection := sel.SelectCoin(1)
			if selection.Symbol != tt.wantCoin {
				t.Errorf("at %02d:%02d UTC: got %s, want %s", tt.hour, tt.minute, selection.Symbol, tt.wantCoin)
			}
		})
	}
}

func TestSelector5050Split(t *testing.T) {
	// 50/50 DGB/BTC → sorted: BTC 00:00–12:00, DGB 12:00–24:00
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	btc := newMockSource("BTC", "pool_btc", 50000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.poll()

	tests := []struct {
		hour     int
		wantCoin string
	}{
		{0, "BTC"},
		{6, "BTC"},
		{11, "BTC"},
		{12, "DGB"},
		{18, "DGB"},
		{23, "DGB"},
	}

	for _, tt := range tests {
		t.Run(tt.wantCoin, func(t *testing.T) {
			sel := NewSelector(SelectorConfig{
				Monitor:      mon,
				AllowedCoins: []string{"DGB", "BTC"},
				CoinWeights: []CoinWeight{
					{Symbol: "DGB", Weight: 50},
					{Symbol: "BTC", Weight: 50},
				},
				PreferCoin:    "DGB",
				MinTimeOnCoin: -1,
				NowFunc:       makeNowFunc(tt.hour, 0),
				Logger:        zap.NewNop(),
			})

			selection := sel.SelectCoin(1)
			if selection.Symbol != tt.wantCoin {
				t.Errorf("at %02d:00 UTC: got %s, want %s", tt.hour, selection.Symbol, tt.wantCoin)
			}
		})
	}
}

func TestSelectorScheduledRotation(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	bch := newMockSource("BCH", "pool_bch", 500000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(bch, 600)
	mon.poll()

	// Sorted: BCH 00:00–12:00, DGB 12:00–24:00
	currentTime := mockTime(2, 0) // Start in BCH slot
	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BCH"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BCH", Weight: 50},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		NowFunc:       func() time.Time { return currentTime },
		Logger:        zap.NewNop(),
	})

	// Initial: BCH (00:00–12:00 slot)
	sel1 := sel.SelectCoin(1)
	if sel1.Symbol != "BCH" {
		t.Fatalf("at 02:00: got %s, want BCH", sel1.Symbol)
	}
	sel.AssignCoin(1, sel1.Symbol, "worker1", "low")

	// Same time: no change
	sel2 := sel.SelectCoin(1)
	if sel2.Changed {
		t.Error("same time slot — should not change")
	}

	// Advance to 13:00 (DGB slot)
	currentTime = mockTime(13, 0)
	sel3 := sel.SelectCoin(1)
	if !sel3.Changed {
		t.Error("crossed into DGB slot — should switch")
	}
	if sel3.Symbol != "DGB" {
		t.Errorf("at 13:00: got %s, want DGB", sel3.Symbol)
	}
	if sel3.Reason != "scheduled_rotation" {
		t.Errorf("reason = %q, want scheduled_rotation", sel3.Reason)
	}
}

func TestSelectorCoinGoesDown(t *testing.T) {
	mon, sel, dgb, _, _ := setupSelectorAt(t, 10, 0)

	sel.AssignCoin(1, "DGB", "worker1", "low")

	dgb.SetRunning(false)
	mon.poll()

	selection := sel.SelectCoin(1)
	if selection.Symbol == "DGB" {
		t.Error("should not select unavailable DGB")
	}
	if !selection.Changed {
		t.Error("should switch away from unavailable DGB")
	}
}

func TestSelectorMinTimeOnCoin(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	btc := newMockSource("BTC", "pool_btc", 50000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.poll()

	// Sorted: BTC 00:00–12:00, DGB 12:00–24:00
	// Time at 13:00 (DGB slot), miner on BTC with 1h min_time → should NOT switch
	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BTC"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 50},
			{Symbol: "BTC", Weight: 50},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: 1 * time.Hour,
		NowFunc:       makeNowFunc(13, 0),
		Logger:        zap.NewNop(),
	})

	sel.AssignCoin(1, "BTC", "worker1", "low")

	selection := sel.SelectCoin(1)
	if selection.Changed {
		t.Fatal("should not switch — min_time_on_coin not elapsed")
	}
	if selection.Reason != "min_time_not_elapsed" {
		t.Fatalf("reason = %q, want min_time_not_elapsed", selection.Reason)
	}
}

func TestSelectorNoCoinAvailable(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	btc := newMockSource("BTC", "pool_btc", 50000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)

	dgb.SetRunning(false)
	btc.SetRunning(false)
	mon.poll()

	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BTC"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 80},
			{Symbol: "BTC", Weight: 20},
		},
		PreferCoin: "DGB",
		NowFunc:    makeNowFunc(10, 0),
		Logger:     zap.NewNop(),
	})

	selection := sel.SelectCoin(1)
	if selection.Changed {
		t.Error("should not change when no coins available")
	}
	if selection.Reason != "no_coins_available" {
		t.Errorf("reason = %q, want no_coins_available", selection.Reason)
	}
}

func TestSelectorCoinDistribution(t *testing.T) {
	_, sel, _, _, _ := setupSelectorAt(t, 10, 0)

	sel.AssignCoin(1, "DGB", "w1", "low")
	sel.AssignCoin(2, "DGB", "w2", "low")
	sel.AssignCoin(3, "BTC", "w3", "pro")

	dist := sel.GetCoinDistribution()
	if dist["DGB"] != 2 {
		t.Errorf("DGB count = %d, want 2", dist["DGB"])
	}
	if dist["BTC"] != 1 {
		t.Errorf("BTC count = %d, want 1", dist["BTC"])
	}
}

func TestSelectorSwitchHistory(t *testing.T) {
	_, sel, _, _, _ := setupSelectorAt(t, 10, 0)

	sel.AssignCoin(1, "DGB", "w1", "low")
	sel.AssignCoin(1, "BTC", "w1", "low")

	history := sel.GetSwitchHistory(10)
	if len(history) != 1 {
		t.Fatalf("expected 1 switch event, got %d", len(history))
	}
	if history[0].FromCoin != "DGB" || history[0].ToCoin != "BTC" {
		t.Errorf("switch: %s -> %s, want DGB -> BTC", history[0].FromCoin, history[0].ToCoin)
	}
}

func TestSelectorRemoveSession(t *testing.T) {
	_, sel, _, _, _ := setupSelectorAt(t, 10, 0)

	sel.AssignCoin(1, "DGB", "w1", "low")

	_, ok := sel.GetSessionCoin(1)
	if !ok {
		t.Error("session should exist")
	}

	sel.RemoveSession(1)

	_, ok = sel.GetSessionCoin(1)
	if ok {
		t.Error("session should be removed")
	}
}

func TestSelectorGetCoinWeights(t *testing.T) {
	_, sel, _, _, _ := setupSelectorAt(t, 10, 0)

	weights := sel.GetCoinWeights()
	if len(weights) != 3 {
		t.Fatalf("expected 3 weights, got %d", len(weights))
	}

	weightMap := make(map[string]int)
	for _, w := range weights {
		weightMap[w.Symbol] = w.Weight
	}
	if weightMap["DGB"] != 80 {
		t.Errorf("DGB weight = %d, want 80", weightMap["DGB"])
	}
	if weightMap["BTC"] != 5 {
		t.Errorf("BTC weight = %d, want 5", weightMap["BTC"])
	}
	if weightMap["BCH"] != 15 {
		t.Errorf("BCH weight = %d, want 15", weightMap["BCH"])
	}
}

func TestSelectorZeroWeightExcluded(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	btc := newMockSource("BTC", "pool_btc", 50000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(btc, 600)
	mon.poll()

	// DGB=100, BTC=0 → DGB gets the full 24 hours
	sel := NewSelector(SelectorConfig{
		Monitor:      mon,
		AllowedCoins: []string{"DGB", "BTC"},
		CoinWeights: []CoinWeight{
			{Symbol: "DGB", Weight: 100},
			{Symbol: "BTC", Weight: 0},
		},
		PreferCoin:    "DGB",
		MinTimeOnCoin: -1,
		NowFunc:       makeNowFunc(23, 59),
		Logger:        zap.NewNop(),
	})

	selection := sel.SelectCoin(1)
	if selection.Symbol != "DGB" {
		t.Fatalf("expected DGB, got %s", selection.Symbol)
	}
}

func TestSelectorBuildTimeSlots(t *testing.T) {
	// Input order doesn't matter — buildTimeSlots sorts alphabetically
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 80},
		{Symbol: "BCH", Weight: 15},
		{Symbol: "BTC", Weight: 5},
	})

	if len(slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(slots))
	}
	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0 (no start_hour set)", anchor)
	}

	// BCH: 0.0–0.15 (alphabetically first)
	if slots[0].symbol != "BCH" || slots[0].startFrac != 0.0 {
		t.Errorf("slot 0: got %s@%.2f, want BCH@0.00", slots[0].symbol, slots[0].startFrac)
	}
	if math.Abs(slots[0].endFrac-0.15) > 0.001 {
		t.Errorf("BCH endFrac = %.4f, want 0.15", slots[0].endFrac)
	}

	// BTC: 0.15–0.20
	if slots[1].symbol != "BTC" {
		t.Errorf("slot 1: got %s, want BTC", slots[1].symbol)
	}
	if math.Abs(slots[1].startFrac-0.15) > 0.001 {
		t.Errorf("BTC startFrac = %.4f, want 0.15", slots[1].startFrac)
	}
	if math.Abs(slots[1].endFrac-0.20) > 0.001 {
		t.Errorf("BTC endFrac = %.4f, want 0.20", slots[1].endFrac)
	}

	// DGB: 0.20–1.0
	if slots[2].symbol != "DGB" {
		t.Errorf("slot 2: got %s, want DGB", slots[2].symbol)
	}
	if slots[2].endFrac != 1.0 {
		t.Errorf("DGB endFrac = %.4f, want 1.00", slots[2].endFrac)
	}

	for _, s := range slots {
		startH := s.startFrac * 24
		endH := s.endFrac * 24
		t.Logf("%s: %.1fh – %.1fh (%.0f%% of day)",
			s.symbol, startH, endH, (s.endFrac-s.startFrac)*100)
	}
}

// =============================================================================
// START_HOUR TESTS — proves the fix for the timing bug
// =============================================================================

func floatPtr(v float64) *float64 { return &v }

func TestSelectorStartHour_BuildTimeSlots(t *testing.T) {
	// starts at 22:00 with 8% weight (~1.92h), DGB gets the rest (92%)
	// This is the exact scenario from the production bug.
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 92},
		{Symbol: "XEC", Weight: 8, StartHour: floatPtr(22.0)},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}

	// has start_hour so it sorts first; anchor = 22/24
	expectedAnchor := 22.0 / 24.0
	if math.Abs(anchor-expectedAnchor) > 0.0001 {
		t.Errorf("anchor = %f, want %f (22:00)", anchor, expectedAnchor)
	}

	// XEC: slot 0 in anchor-relative space, 0.00–0.08
	if slots[0].symbol != "XEC" {
		t.Errorf("slot 0: got %s, want XEC", slots[0].symbol)
	}
	if math.Abs(slots[0].endFrac-0.08) > 0.001 {
		t.Errorf("XEC endFrac = %.4f, want 0.08 (8%%)", slots[0].endFrac)
	}

	// DGB: slot 1, 0.08–1.00
	if slots[1].symbol != "DGB" {
		t.Errorf("slot 1: got %s, want DGB", slots[1].symbol)
	}
	if slots[1].endFrac != 1.0 {
		t.Errorf("DGB endFrac = %.4f, want 1.00", slots[1].endFrac)
	}

	// In wall-clock terms with anchor at 22:00:
	// XEC: 22:00 – 23:55 (0.08 * 24 = 1.92 hours)
	// DGB: 23:55 – 22:00 next day (0.92 * 24 = 22.08 hours)
	xecHours := (slots[0].endFrac - slots[0].startFrac) * 24
	dgbHours := (slots[1].endFrac - slots[1].startFrac) * 24
	t.Logf("XEC: %.2f hours starting at 22:00", xecHours)
	t.Logf("DGB: %.2f hours starting at %.2f:00", dgbHours, 22.0+xecHours)

	if math.Abs(xecHours-1.92) > 0.01 {
		t.Errorf("XEC duration = %.2f hours, want 1.92", xecHours)
	}
}

func TestSelectorStartHour_CoinSelection(t *testing.T) {
	// Reproduce the exact production scenario:
	// XEC: start_hour=22, weight=8 → 22:00–23:55 EST
	// DGB: weight=92 → 23:55–22:00 EST
	// Timezone: America/New_York

	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})
	dgb := newMockSource("DGB", "pool_dgb", 1000.0)
	xec := newMockSource("XEC", "pool_xec", 5000.0)
	mon.RegisterCoin(dgb, 15)
	mon.RegisterCoin(xec, 150)
	mon.poll()

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("failed to load timezone: %v", err)
	}

	tests := []struct {
		hour, minute int
		wantCoin     string
		desc         string
	}{
		// XEC window: 22:00–23:55 EST
		{22, 0, "XEC", "22:00 — XEC slot starts"},
		{22, 30, "XEC", "22:30 — middle of XEC slot"},
		{23, 0, "XEC", "23:00 — still XEC"},
		{23, 50, "XEC", "23:50 — near end of XEC slot"},
		// DGB window: 23:55–22:00 next day
		{23, 56, "DGB", "23:56 — DGB slot starts"},
		{0, 0, "DGB", "midnight — DGB slot"},
		{3, 8, "DGB", "03:08 — should NOT be XEC (the bug)"},
		{10, 0, "DGB", "10:00 — DGB slot"},
		{15, 0, "DGB", "15:00 — DGB slot"},
		{21, 0, "DGB", "21:00 — DGB slot (just before XEC)"},
		{21, 59, "DGB", "21:59 — last minute before XEC"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			mockNow := time.Date(2026, 4, 13, tt.hour, tt.minute, 0, 0, loc)
			sel := NewSelector(SelectorConfig{
				Monitor:      mon,
				AllowedCoins: []string{"DGB", "XEC"},
				CoinWeights: []CoinWeight{
					{Symbol: "DGB", Weight: 92},
					{Symbol: "XEC", Weight: 8, StartHour: floatPtr(22.0)},
				},
				PreferCoin:    "DGB",
				MinTimeOnCoin: -1,
				Location:      loc,
				NowFunc:       func() time.Time { return mockNow },
				Logger:        zap.NewNop(),
			})

			selection := sel.SelectCoin(1)
			if selection.Symbol != tt.wantCoin {
				t.Errorf("at %02d:%02d EST: got %s, want %s",
					tt.hour, tt.minute, selection.Symbol, tt.wantCoin)
			}
		})
	}
}

func TestSelectorStartHour_MultipleCoinsWithStartHour(t *testing.T) {
	// Multiple coins with explicit start_hours
	// DGB: start_hour=0, weight=80 → 00:00–19:12
	// BCH: start_hour=19.2, weight=15 → 19:12–22:48
	// BTC: start_hour=22.8, weight=5 → 22:48–24:00
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "BTC", Weight: 5, StartHour: floatPtr(22.8)},
		{Symbol: "DGB", Weight: 80, StartHour: floatPtr(0.0)},
		{Symbol: "BCH", Weight: 15, StartHour: floatPtr(19.2)},
	})

	if len(slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(slots))
	}
	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0", anchor)
	}

	// Sorted by start_hour: DGB(0.0), BCH(19.2), BTC(22.8)
	if slots[0].symbol != "DGB" {
		t.Errorf("slot 0: got %s, want DGB", slots[0].symbol)
	}
	if slots[1].symbol != "BCH" {
		t.Errorf("slot 1: got %s, want BCH", slots[1].symbol)
	}
	if slots[2].symbol != "BTC" {
		t.Errorf("slot 2: got %s, want BTC", slots[2].symbol)
	}
}

// =============================================================================
// DEFENSE IN DEPTH: StartHour edge cases
// =============================================================================

func TestSelectorStartHour_NegativeClampedToZero(t *testing.T) {
	// Negative start_hour should be clamped to 0 (midnight)
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "XEC", Weight: 10, StartHour: floatPtr(-5.0)},
		{Symbol: "DGB", Weight: 90},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	// Anchor should be clamped to 0.0, not -5/24
	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0 (negative clamped)", anchor)
	}
	t.Logf("Negative start_hour clamped to 0.0 ✓ (anchor=%f)", anchor)
}

func TestSelectorStartHour_GreaterThan24ClampedToZero(t *testing.T) {
	// start_hour >= 24 should be clamped to 0 (midnight)
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "XEC", Weight: 10, StartHour: floatPtr(25.0)},
		{Symbol: "DGB", Weight: 90},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0 (>=24 clamped)", anchor)
	}
	t.Logf("start_hour>=24 clamped to 0.0 ✓ (anchor=%f)", anchor)
}

func TestSelectorStartHour_Exactly24ClampedToZero(t *testing.T) {
	// start_hour == 24.0 should be clamped to 0 (24 == midnight next day == 0)
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "XEC", Weight: 10, StartHour: floatPtr(24.0)},
		{Symbol: "DGB", Weight: 90},
	})

	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0 (24.0 clamped)", anchor)
	}
	t.Logf("start_hour==24 clamped to 0.0 ✓ (anchor=%f)", anchor)
}

func TestSelectorStartHour_23_99IsValid(t *testing.T) {
	// 23.99 is the max valid start_hour — should NOT be clamped
	_, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "XEC", Weight: 10, StartHour: floatPtr(23.99)},
		{Symbol: "DGB", Weight: 90},
	})

	expected := 23.99 / 24.0
	if math.Abs(anchor-expected) > 1e-9 {
		t.Errorf("anchor = %f, want %f (23.99 is valid)", anchor, expected)
	}
	t.Logf("start_hour=23.99 accepted ✓ (anchor=%f)", anchor)
}

func TestSelectorStartHour_ZeroIsValid(t *testing.T) {
	// start_hour=0.0 means midnight — explicit zero should work
	_, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "XEC", Weight: 10, StartHour: floatPtr(0.0)},
		{Symbol: "DGB", Weight: 90},
	})

	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0", anchor)
	}
	t.Logf("start_hour=0.0 (midnight) ✓ (anchor=%f)", anchor)
}

func TestSelectorStartHour_NilStartHourDefaultsToAlphabetical(t *testing.T) {
	// No coins have start_hour — should sort alphabetically and anchor at 0
	slots, anchor := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 50},
		{Symbol: "BCH", Weight: 30},
		{Symbol: "BTC", Weight: 20},
	})

	if anchor != 0.0 {
		t.Errorf("anchor = %f, want 0.0 (no start_hours)", anchor)
	}
	if slots[0].symbol != "BCH" {
		t.Errorf("slot 0: got %s, want BCH (alphabetical)", slots[0].symbol)
	}
	if slots[1].symbol != "BTC" {
		t.Errorf("slot 1: got %s, want BTC (alphabetical)", slots[1].symbol)
	}
	if slots[2].symbol != "DGB" {
		t.Errorf("slot 2: got %s, want DGB (alphabetical)", slots[2].symbol)
	}
	t.Logf("No start_hour → alphabetical sort, anchor=0 ✓")
}
