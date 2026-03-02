// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package payments — Timing auto-scaling tests.
//
// These tests exercise the pure logic of getEffectiveInterval() and
// getDeepReorgMaxAge() — the V14/V16 auto-scaling functions that adapt
// payment processing cadence to the chain's block time.
//
// Covered scenarios:
//   - Explicit interval/DeepReorgMaxAge overrides auto-scaling
//   - DGB (15s blocks): interval auto-scales to 150s, deep reorg to 5760
//   - BTC (600s blocks): interval capped at 10min, deep reorg stays at 1000
//   - Very fast chains (1s blocks): interval floored at 60s
//   - Zero BlockTime: fallback to safe defaults
package payments

import (
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// newTimingTestProcessor creates a Processor with specific payment config
// for testing getEffectiveInterval() and getDeepReorgMaxAge().
func newTimingTestProcessor(cfg *config.PaymentsConfig) *Processor {
	logger := zap.NewNop()
	return &Processor{
		cfg:     cfg,
		poolCfg: &config.PoolConfig{},
		logger:  logger.Sugar(),
		stopCh:  make(chan struct{}),
	}
}

// =============================================================================
// getEffectiveInterval() tests
// =============================================================================

// TestGetEffectiveInterval_ExplicitOverride verifies that an explicit Interval
// in config takes precedence over auto-scaling.
func TestGetEffectiveInterval_ExplicitOverride(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		Interval:  5 * time.Minute,
		BlockTime: 15, // Would auto-scale to 150s, but explicit takes priority
	})

	got := proc.getEffectiveInterval()
	if got != 5*time.Minute {
		t.Errorf("getEffectiveInterval() = %v, want %v (explicit override)", got, 5*time.Minute)
	}
}

// TestGetEffectiveInterval_DGB_AutoScale verifies auto-scaling for DGB (15s blocks).
// Expected: 15 * 10 = 150s.
func TestGetEffectiveInterval_DGB_AutoScale(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 15,
	})

	got := proc.getEffectiveInterval()
	expected := 150 * time.Second
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (DGB 15s blocks → 150s)", got, expected)
	}
}

// TestGetEffectiveInterval_BTC_CappedAt10Min verifies that BTC (600s blocks)
// gets capped at 10 minutes instead of the naive 600*10=6000s=100 minutes.
func TestGetEffectiveInterval_BTC_CappedAt10Min(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 600,
	})

	got := proc.getEffectiveInterval()
	expected := 10 * time.Minute
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (BTC 600s blocks → capped at 10min)", got, expected)
	}
}

// TestGetEffectiveInterval_FastChain_FlooredAt60s verifies that very fast chains
// (e.g., 1s blocks) get floored at 60 seconds.
func TestGetEffectiveInterval_FastChain_FlooredAt60s(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 1, // 1s blocks → 1*10=10s → floored to 60s
	})

	got := proc.getEffectiveInterval()
	expected := 60 * time.Second
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (1s blocks → floored at 60s)", got, expected)
	}
}

// TestGetEffectiveInterval_5sBlocks_FlooredAt60s verifies 5-second blocks also
// get floored (5*10=50s < 60s floor).
func TestGetEffectiveInterval_5sBlocks_FlooredAt60s(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 5, // 5*10=50s → floored to 60s
	})

	got := proc.getEffectiveInterval()
	expected := 60 * time.Second
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (5s blocks → floored at 60s)", got, expected)
	}
}

// TestGetEffectiveInterval_7sBlocks_AutoScales verifies 7-second blocks auto-scale
// correctly (7*10=70s, above the 60s floor and below the 10min cap).
func TestGetEffectiveInterval_7sBlocks_AutoScales(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 7, // 7*10=70s → above floor, below cap
	})

	got := proc.getEffectiveInterval()
	expected := 70 * time.Second
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (7s blocks → 70s)", got, expected)
	}
}

// TestGetEffectiveInterval_ZeroBlockTime_Fallback verifies fallback to 10 minutes
// when BlockTime is 0 (not configured).
func TestGetEffectiveInterval_ZeroBlockTime_Fallback(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 0,
	})

	got := proc.getEffectiveInterval()
	expected := 10 * time.Minute
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (zero BlockTime → 10min fallback)", got, expected)
	}
}

// TestGetEffectiveInterval_BothZero_Fallback verifies fallback when both
// Interval and BlockTime are zero.
func TestGetEffectiveInterval_BothZero_Fallback(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{})

	got := proc.getEffectiveInterval()
	expected := 10 * time.Minute
	if got != expected {
		t.Errorf("getEffectiveInterval() = %v, want %v (both zero → 10min fallback)", got, expected)
	}
}

// =============================================================================
// getDeepReorgMaxAge() tests
// =============================================================================

// TestGetDeepReorgMaxAge_ExplicitOverride verifies that an explicit DeepReorgMaxAge
// in config takes precedence over auto-scaling.
func TestGetDeepReorgMaxAge_ExplicitOverride(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		DeepReorgMaxAge: 2000,
		BlockTime:       15, // Would auto-scale to 5760, but explicit takes priority
	})

	got := proc.getDeepReorgMaxAge()
	if got != 2000 {
		t.Errorf("getDeepReorgMaxAge() = %d, want 2000 (explicit override)", got)
	}
}

// TestGetDeepReorgMaxAge_DGB_AutoScaled verifies auto-scaling for DGB (15s blocks).
// Expected: 86400/15 = 5760 (24 hours of blocks), which exceeds default 1000.
func TestGetDeepReorgMaxAge_DGB_AutoScaled(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 15,
	})

	got := proc.getDeepReorgMaxAge()
	expected := uint64(86400 / 15) // 5760
	if got != expected {
		t.Errorf("getDeepReorgMaxAge() = %d, want %d (DGB 15s → 24h coverage)", got, expected)
	}
}

// TestGetDeepReorgMaxAge_BTC_StaysAtDefault verifies that BTC (600s blocks)
// keeps the default 1000 because 86400/600=144 < 1000.
func TestGetDeepReorgMaxAge_BTC_StaysAtDefault(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 600,
	})

	got := proc.getDeepReorgMaxAge()
	if got != DeepReorgMaxAge {
		t.Errorf("getDeepReorgMaxAge() = %d, want %d (BTC 600s → stays at default)", got, DeepReorgMaxAge)
	}
}

// TestGetDeepReorgMaxAge_VeryFastChain verifies a 1-second block chain gets
// much higher deep reorg max age (86400 blocks = 24 hours).
func TestGetDeepReorgMaxAge_VeryFastChain(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 1,
	})

	got := proc.getDeepReorgMaxAge()
	expected := uint64(86400) // 86400/1 = 86400
	if got != expected {
		t.Errorf("getDeepReorgMaxAge() = %d, want %d (1s blocks → full 24h depth)", got, expected)
	}
}

// TestGetDeepReorgMaxAge_60sBlocks verifies 60-second blocks.
// 86400/60 = 1440 > 1000, so auto-scaled.
func TestGetDeepReorgMaxAge_60sBlocks(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 60,
	})

	got := proc.getDeepReorgMaxAge()
	expected := uint64(86400 / 60) // 1440
	if got != expected {
		t.Errorf("getDeepReorgMaxAge() = %d, want %d (60s blocks → 1440 for 24h coverage)", got, expected)
	}
}

// TestGetDeepReorgMaxAge_ZeroBlockTime_Default verifies fallback to DeepReorgMaxAge
// constant when BlockTime is 0.
func TestGetDeepReorgMaxAge_ZeroBlockTime_Default(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{
		BlockTime: 0,
	})

	got := proc.getDeepReorgMaxAge()
	if got != DeepReorgMaxAge {
		t.Errorf("getDeepReorgMaxAge() = %d, want %d (zero BlockTime → default)", got, DeepReorgMaxAge)
	}
}

// TestGetDeepReorgMaxAge_BothZero_Default verifies default when both
// DeepReorgMaxAge and BlockTime are zero.
func TestGetDeepReorgMaxAge_BothZero_Default(t *testing.T) {
	t.Parallel()

	proc := newTimingTestProcessor(&config.PaymentsConfig{})

	got := proc.getDeepReorgMaxAge()
	if got != DeepReorgMaxAge {
		t.Errorf("getDeepReorgMaxAge() = %d, want %d (both zero → default)", got, DeepReorgMaxAge)
	}
}

// TestGetDeepReorgMaxAge_BoundaryBlockTime verifies the boundary where
// auto-scaling switches from default to computed.
// At BlockTime=86 → 86400/86=1004 > 1000 → auto-scaled.
// At BlockTime=87 → 86400/87=993 < 1000 → default.
func TestGetDeepReorgMaxAge_BoundaryBlockTime(t *testing.T) {
	t.Parallel()

	// BlockTime=86 → 86400/86 = 1004 → exceeds 1000 → auto-scaled
	proc86 := newTimingTestProcessor(&config.PaymentsConfig{BlockTime: 86})
	got86 := proc86.getDeepReorgMaxAge()
	expected86 := uint64(86400 / 86) // 1004
	if got86 != expected86 {
		t.Errorf("BlockTime=86: getDeepReorgMaxAge() = %d, want %d", got86, expected86)
	}

	// BlockTime=87 → 86400/87 = 993 → below 1000 → default
	proc87 := newTimingTestProcessor(&config.PaymentsConfig{BlockTime: 87})
	got87 := proc87.getDeepReorgMaxAge()
	if got87 != DeepReorgMaxAge {
		t.Errorf("BlockTime=87: getDeepReorgMaxAge() = %d, want %d (below threshold → default)", got87, DeepReorgMaxAge)
	}
}

// TestGetEffectiveInterval_ExactFloorBoundary verifies the exact boundary
// where the 60s floor activates.
// BlockTime=6 → 6*10=60s (exactly at floor)
// BlockTime=5 → 5*10=50s → floored to 60s
func TestGetEffectiveInterval_ExactFloorBoundary(t *testing.T) {
	t.Parallel()

	// Exactly at floor: 6*10 = 60s
	proc6 := newTimingTestProcessor(&config.PaymentsConfig{BlockTime: 6})
	got6 := proc6.getEffectiveInterval()
	if got6 != 60*time.Second {
		t.Errorf("BlockTime=6: getEffectiveInterval() = %v, want %v", got6, 60*time.Second)
	}

	// Just below floor: 5*10 = 50s → floored
	proc5 := newTimingTestProcessor(&config.PaymentsConfig{BlockTime: 5})
	got5 := proc5.getEffectiveInterval()
	if got5 != 60*time.Second {
		t.Errorf("BlockTime=5: getEffectiveInterval() = %v, want %v (floored)", got5, 60*time.Second)
	}
}

// TestGetEffectiveInterval_ExactCapBoundary verifies the exact boundary
// where the 10-minute cap activates.
// BlockTime=60 → 60*10=600s=10min (exactly at cap)
// BlockTime=61 → 61*10=610s → capped to 600s
func TestGetEffectiveInterval_ExactCapBoundary(t *testing.T) {
	t.Parallel()

	// Exactly at cap: 60*10 = 600s = 10min
	proc60 := newTimingTestProcessor(&config.PaymentsConfig{BlockTime: 60})
	got60 := proc60.getEffectiveInterval()
	if got60 != 10*time.Minute {
		t.Errorf("BlockTime=60: getEffectiveInterval() = %v, want %v", got60, 10*time.Minute)
	}

	// Just above cap: 61*10 = 610s → capped to 600s
	proc61 := newTimingTestProcessor(&config.PaymentsConfig{BlockTime: 61})
	got61 := proc61.getEffectiveInterval()
	if got61 != 10*time.Minute {
		t.Errorf("BlockTime=61: getEffectiveInterval() = %v, want %v (capped)", got61, 10*time.Minute)
	}
}
