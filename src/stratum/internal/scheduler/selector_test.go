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

	// DGB=80%, BCH=15%, BTC=5%
	// Time slots: DGB 00:00–19:12, BCH 19:12–22:48, BTC 22:48–24:00
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
	// DGB=80%, BCH=15%, BTC=5%
	// DGB: 00:00–19:12, BCH: 19:12–22:48, BTC: 22:48–24:00

	tests := []struct {
		hour, minute int
		wantCoin     string
		desc         string
	}{
		{0, 0, "DGB", "midnight — start of DGB slot"},
		{10, 0, "DGB", "mid-morning — middle of DGB slot"},
		{19, 0, "DGB", "19:00 — still DGB (ends at 19:12)"},
		{19, 15, "BCH", "19:15 — BCH slot (19:12–22:48)"},
		{21, 0, "BCH", "21:00 — middle of BCH slot"},
		{22, 50, "BTC", "22:50 — BTC slot (22:48–24:00)"},
		{23, 59, "BTC", "23:59 — end of BTC slot"},
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
	// 50/50 DGB/BTC → DGB 00:00–12:00, BTC 12:00–24:00
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
		{0, "DGB"},
		{6, "DGB"},
		{11, "DGB"},
		{12, "BTC"},
		{18, "BTC"},
		{23, "BTC"},
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

	// 50/50: DGB 00:00–12:00, BCH 12:00–24:00
	currentTime := mockTime(10, 0) // Start in DGB slot
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

	// Initial: DGB
	sel1 := sel.SelectCoin(1)
	if sel1.Symbol != "DGB" {
		t.Fatalf("at 10:00: got %s, want DGB", sel1.Symbol)
	}
	sel.AssignCoin(1, sel1.Symbol, "worker1", "low")

	// Same time: no change
	sel2 := sel.SelectCoin(1)
	if sel2.Changed {
		t.Error("same time slot — should not change")
	}

	// Advance to 13:00 (BCH slot)
	currentTime = mockTime(13, 0)
	sel3 := sel.SelectCoin(1)
	if !sel3.Changed {
		t.Error("crossed into BCH slot — should switch")
	}
	if sel3.Symbol != "BCH" {
		t.Errorf("at 13:00: got %s, want BCH", sel3.Symbol)
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

	// 50/50, time at 13:00 (BTC slot), miner on DGB with 1h min_time
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

	sel.AssignCoin(1, "DGB", "worker1", "low")

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
	slots := buildTimeSlots([]CoinWeight{
		{Symbol: "DGB", Weight: 80},
		{Symbol: "BCH", Weight: 15},
		{Symbol: "BTC", Weight: 5},
	})

	if len(slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(slots))
	}

	// DGB: 0.0–0.8
	if slots[0].symbol != "DGB" || slots[0].startFrac != 0.0 {
		t.Errorf("slot 0: got %s@%.2f, want DGB@0.00", slots[0].symbol, slots[0].startFrac)
	}
	if math.Abs(slots[0].endFrac-0.80) > 0.001 {
		t.Errorf("DGB endFrac = %.4f, want 0.80", slots[0].endFrac)
	}

	// BCH: 0.8–0.95
	if slots[1].symbol != "BCH" {
		t.Errorf("slot 1: got %s, want BCH", slots[1].symbol)
	}
	if math.Abs(slots[1].startFrac-0.80) > 0.001 {
		t.Errorf("BCH startFrac = %.4f, want 0.80", slots[1].startFrac)
	}
	if math.Abs(slots[1].endFrac-0.95) > 0.001 {
		t.Errorf("BCH endFrac = %.4f, want 0.95", slots[1].endFrac)
	}

	// BTC: 0.95–1.0
	if slots[2].symbol != "BTC" {
		t.Errorf("slot 2: got %s, want BTC", slots[2].symbol)
	}
	if slots[2].endFrac != 1.0 {
		t.Errorf("BTC endFrac = %.4f, want 1.00", slots[2].endFrac)
	}

	for _, s := range slots {
		startH := s.startFrac * 24
		endH := s.endFrac * 24
		t.Logf("%s: %.1fh – %.1fh (%.0f%% of day)",
			s.symbol, startH, endH, (s.endFrac-s.startFrac)*100)
	}
}
