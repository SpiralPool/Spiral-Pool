// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mockCoinSource is a test implementation of CoinDifficultySource.
type mockCoinSource struct {
	symbol  string
	poolID  string
	diff    atomic.Value // float64
	running atomic.Bool
}

func newMockSource(symbol, poolID string, diff float64) *mockCoinSource {
	m := &mockCoinSource{symbol: symbol, poolID: poolID}
	m.diff.Store(diff)
	m.running.Store(true)
	return m
}

func (m *mockCoinSource) Symbol() string              { return m.symbol }
func (m *mockCoinSource) PoolID() string              { return m.poolID }
func (m *mockCoinSource) IsRunning() bool             { return m.running.Load() }
func (m *mockCoinSource) GetNetworkDifficulty() float64 {
	return m.diff.Load().(float64)
}
func (m *mockCoinSource) SetDifficulty(d float64) { m.diff.Store(d) }
func (m *mockCoinSource) SetRunning(r bool)       { m.running.Store(r) }

// =============================================================================
// MONITOR TESTS
// =============================================================================

func TestMonitorRegisterAndGetState(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour, // won't fire in tests
		Logger:       zap.NewNop(),
	})

	src := newMockSource("DGB", "pool_dgb", 1234.5)
	mon.RegisterCoin(src, 15)

	state, ok := mon.GetState("DGB")
	if !ok {
		t.Fatal("expected DGB to be registered")
	}
	if state.Symbol != "DGB" {
		t.Errorf("symbol = %q, want DGB", state.Symbol)
	}
	if state.BlockTime != 15 {
		t.Errorf("blockTime = %f, want 15", state.BlockTime)
	}

	// Unknown coin
	_, ok = mon.GetState("NONEXISTENT")
	if ok {
		t.Error("expected NONEXISTENT to not be found")
	}
}

func TestMonitorPollUpdatesState(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("BTC", "pool_btc", 50000.0)
	mon.RegisterCoin(src, 600)

	// Manual poll
	mon.poll()

	state, ok := mon.GetState("BTC")
	if !ok {
		t.Fatal("BTC should be registered")
	}
	if state.NetworkDiff != 50000.0 {
		t.Errorf("diff = %f, want 50000", state.NetworkDiff)
	}
	if !state.Available {
		t.Error("should be available")
	}
}

func TestMonitorPublishesDifficultyChangeEvent(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("DGB", "pool_dgb", 1000.0)
	mon.RegisterCoin(src, 15)

	ch := mon.Subscribe()

	// First poll sets baseline
	mon.poll()

	// Change difficulty by 30%
	src.SetDifficulty(1300.0)
	mon.poll()

	// Should receive an event
	select {
	case event := <-ch:
		if event.Symbol != "DGB" {
			t.Errorf("event symbol = %q, want DGB", event.Symbol)
		}
		if event.OldDiff != 1000.0 {
			t.Errorf("oldDiff = %f, want 1000", event.OldDiff)
		}
		if event.NewDiff != 1300.0 {
			t.Errorf("newDiff = %f, want 1300", event.NewDiff)
		}
		expectedChange := 30.0
		if math.Abs(event.ChangePercent-expectedChange) > 0.01 {
			t.Errorf("changePct = %f, want ~%f", event.ChangePercent, expectedChange)
		}
	default:
		t.Error("expected a difficulty change event, got none")
	}
}

func TestMonitorNoEventOnSmallChange(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("BTC", "pool_btc", 100000.0)
	mon.RegisterCoin(src, 600)

	ch := mon.Subscribe()

	// Baseline
	mon.poll()

	// Tiny change (0.05%) — should NOT fire an event
	src.SetDifficulty(100050.0)
	mon.poll()

	select {
	case event := <-ch:
		t.Errorf("unexpected event: %+v", event)
	default:
		// Good — no event
	}
}

func TestMonitorMarksUnavailableWhenPoolStops(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("DGB", "pool_dgb", 1000.0)
	mon.RegisterCoin(src, 15)

	// Initial poll
	mon.poll()
	state, _ := mon.GetState("DGB")
	if !state.Available {
		t.Error("should be available initially")
	}

	// Stop the pool
	src.SetRunning(false)
	mon.poll()

	state, _ = mon.GetState("DGB")
	if state.Available {
		t.Error("should be unavailable after pool stops")
	}
}

func TestMonitorNotifyBlockFound(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("DGB", "pool_dgb", 1000.0)
	mon.RegisterCoin(src, 15)

	ch := mon.Subscribe()

	// Baseline poll
	mon.poll()

	// Difficulty changed due to new block
	src.SetDifficulty(1500.0)
	mon.NotifyBlockFound("DGB")

	select {
	case event := <-ch:
		if event.NewDiff != 1500.0 {
			t.Errorf("newDiff = %f, want 1500", event.NewDiff)
		}
	default:
		t.Error("expected event from NotifyBlockFound")
	}
}

func TestMonitorStartStop(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: 50 * time.Millisecond,
		Logger:       zap.NewNop(),
	})

	src := newMockSource("DGB", "pool_dgb", 1000.0)
	mon.RegisterCoin(src, 15)

	ctx, cancel := context.WithCancel(context.Background())
	mon.Start(ctx)

	// Let it poll a couple times
	time.Sleep(150 * time.Millisecond)

	state, _ := mon.GetState("DGB")
	if !state.Available {
		t.Error("should be available after polling")
	}

	cancel()
	mon.Stop()
}

func TestMonitorGetAllStates(t *testing.T) {
	mon := NewMonitor(MonitorConfig{
		PollInterval: time.Hour,
		Logger:       zap.NewNop(),
	})

	mon.RegisterCoin(newMockSource("DGB", "pool_dgb", 1000.0), 15)
	mon.RegisterCoin(newMockSource("BTC", "pool_btc", 50000.0), 600)
	mon.RegisterCoin(newMockSource("BCH", "pool_bch", 300.0), 600)

	mon.poll()

	states := mon.GetAllStates()
	if len(states) != 3 {
		t.Errorf("expected 3 states, got %d", len(states))
	}
	for _, sym := range []string{"DGB", "BTC", "BCH"} {
		if _, ok := states[sym]; !ok {
			t.Errorf("missing state for %s", sym)
		}
	}
}

// =============================================================================
// EXPECTED SHARE TIME CALCULATION
// =============================================================================

func TestExpectedShareTime(t *testing.T) {
	tests := []struct {
		name        string
		networkDiff float64
		hashrate    float64
		wantInf     bool
		wantApprox  float64
	}{
		{
			name:        "zero hashrate",
			networkDiff: 1000,
			hashrate:    0,
			wantInf:     true,
		},
		{
			name:        "zero difficulty",
			networkDiff: 0,
			hashrate:    1e12,
			wantInf:     true,
		},
		{
			name:        "BitAxe on DGB",
			networkDiff: 1000,
			hashrate:    500e9, // 500 GH/s
			wantApprox:  8.59,  // 1000 * 2^32 / 500e9 ≈ 8.59s
		},
		{
			name:        "S19 on BTC",
			networkDiff: 50e12,       // 50 trillion
			hashrate:    150e12,      // 150 TH/s
			wantApprox:  1431655765.3, // 50e12 * 2^32 / 150e12 ≈ 1.43 billion seconds
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpectedShareTime(tt.networkDiff, tt.hashrate)
			if tt.wantInf {
				if !math.IsInf(result, 1) {
					t.Errorf("expected +Inf, got %f", result)
				}
				return
			}
			// Allow 1% tolerance
			ratio := result / tt.wantApprox
			if ratio < 0.99 || ratio > 1.01 {
				t.Errorf("expected ~%f, got %f (ratio=%f)", tt.wantApprox, result, ratio)
			}
		})
	}
}
