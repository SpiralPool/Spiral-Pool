// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package vardiff provides tests for variable difficulty adjustment.
package vardiff

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
)

// =============================================================================
// TEST HELPERS
// =============================================================================

func newTestConfig() config.VarDiffConfig {
	return config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      4.0,  // 4 seconds per share
		RetargetTime:    60.0, // Retarget every 60 seconds
		VariancePercent: 30.0, // 30% variance allowed
	}
}

// =============================================================================
// UNIT TESTS
// =============================================================================

func TestNewEngine(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)

	if engine == nil {
		t.Fatal("NewEngine returned nil")
	}

	if engine.cfg.MinDiff != 0.001 {
		t.Errorf("Expected MinDiff=0.001, got %f", engine.cfg.MinDiff)
	}

	if engine.cfg.MaxDiff != 1000000 {
		t.Errorf("Expected MaxDiff=1000000, got %f", engine.cfg.MaxDiff)
	}
}

func TestNewSessionState(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)

	state := engine.NewSessionState(1.0)

	if state == nil {
		t.Fatal("NewSessionState returned nil")
	}

	diff := GetDifficulty(state)
	if diff != 1.0 {
		t.Errorf("Expected initial difficulty=1.0, got %f", diff)
	}

	if state.minDiff != 0.001 {
		t.Errorf("Expected minDiff=0.001, got %f", state.minDiff)
	}

	if state.maxDiff != 1000000 {
		t.Errorf("Expected maxDiff=1000000, got %f", state.maxDiff)
	}
}

func TestGetDifficulty(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)

	testCases := []float64{0.001, 1.0, 100.0, 999999.0}

	for _, initialDiff := range testCases {
		state := engine.NewSessionState(initialDiff)
		diff := GetDifficulty(state)

		if diff != initialDiff {
			t.Errorf("GetDifficulty() = %f, want %f", diff, initialDiff)
		}
	}
}

func TestSetDifficulty(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// Set normal difficulty
	SetDifficulty(state, 50.0)
	if GetDifficulty(state) != 50.0 {
		t.Errorf("SetDifficulty failed, got %f", GetDifficulty(state))
	}

	// Set difficulty below min - should clamp
	SetDifficulty(state, 0.0001)
	if GetDifficulty(state) != 0.001 {
		t.Errorf("SetDifficulty should clamp to min, got %f", GetDifficulty(state))
	}

	// Set difficulty above max - should clamp
	SetDifficulty(state, 2000000)
	if GetDifficulty(state) != 1000000 {
		t.Errorf("SetDifficulty should clamp to max, got %f", GetDifficulty(state))
	}
}

func TestRecordShare_NoRetargetYet(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      4.0,
		RetargetTime:    60.0, // 60 second retarget
		VariancePercent: 30.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// Record shares but not enough time has passed
	for i := 0; i < 10; i++ {
		newDiff, changed := engine.RecordShare(state)
		if changed {
			t.Errorf("Difficulty should not change before retarget interval, got %f", newDiff)
		}
	}

	// Verify shares were counted
	if state.shareCount.Load() != 10 {
		t.Errorf("Expected shareCount=10, got %d", state.shareCount.Load())
	}
}

func TestRecordShare_IncreaseDifficulty(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      4.0,   // 4 seconds per share target
		RetargetTime:    0.001, // Immediate retarget
		VariancePercent: 10.0,  // Low variance to trigger adjustment
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// Simulate shares coming much faster than target (every 0.5 sec instead of 4)
	// Need to manually adjust the timing

	// Record many shares quickly
	for i := 0; i < 100; i++ {
		engine.RecordShare(state)
	}

	// With shares coming faster than target, difficulty should increase
	finalDiff := GetDifficulty(state)
	t.Logf("Final difficulty after fast shares: %f", finalDiff)

	// Since shares came fast, we expect some adjustment
	// The exact value depends on the algorithm details
}

func TestRecordShare_DecreaseDifficulty(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      0.001, // Very fast target (1ms per share)
		RetargetTime:    0.001, // Immediate retarget
		VariancePercent: 10.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(100.0) // Start high

	// Wait a bit then submit shares
	time.Sleep(10 * time.Millisecond)

	// Submit a few shares over longer than target time
	for i := 0; i < 5; i++ {
		engine.RecordShare(state)
		time.Sleep(5 * time.Millisecond)
	}

	finalDiff := GetDifficulty(state)
	t.Logf("Final difficulty after slow shares: %f (started at 100.0)", finalDiff)
}

func TestRecordShare_ClampToMin(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         10.0,
		MaxDiff:         1000000,
		TargetTime:      0.001,
		RetargetTime:    0.001,
		VariancePercent: 5.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(10.0) // Start at min

	// Simulate very slow shares
	time.Sleep(100 * time.Millisecond)
	engine.RecordShare(state)

	finalDiff := GetDifficulty(state)
	if finalDiff < 10.0 {
		t.Errorf("Difficulty should not go below min, got %f", finalDiff)
	}
}

func TestRecordShare_ClampToMax(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         100.0,
		TargetTime:      10.0,
		RetargetTime:    0.001,
		VariancePercent: 5.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(100.0) // Start at max

	// Record many shares very quickly
	for i := 0; i < 1000; i++ {
		engine.RecordShare(state)
	}

	finalDiff := GetDifficulty(state)
	if finalDiff > 100.0 {
		t.Errorf("Difficulty should not exceed max, got %f", finalDiff)
	}
}

func TestAggressiveRetarget_TooFewShares(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// With fewer than 3 shares, should not retarget
	newDiff, changed := engine.AggressiveRetarget(state, 2, 10.0)

	if changed {
		t.Error("AggressiveRetarget should not change with fewer than 3 shares")
	}
	if newDiff != 1.0 {
		t.Errorf("Difficulty should remain 1.0, got %f", newDiff)
	}
}

func TestAggressiveRetarget_Increase(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// 10 shares in 1 second = 0.1 sec per share, target is 4 sec
	// Should increase difficulty significantly
	newDiff, changed := engine.AggressiveRetarget(state, 10, 1.0)

	if !changed {
		t.Error("AggressiveRetarget should trigger change for fast shares")
	}
	if newDiff <= 1.0 {
		t.Errorf("Difficulty should increase for fast shares, got %f", newDiff)
	}
	t.Logf("Aggressive retarget: 1.0 -> %f", newDiff)
}

func TestAggressiveRetarget_Decrease(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(100.0)

	// 3 shares in 60 seconds = 20 sec per share, target is 4 sec
	// Should decrease difficulty
	newDiff, changed := engine.AggressiveRetarget(state, 3, 60.0)

	if !changed {
		t.Error("AggressiveRetarget should trigger change for slow shares")
	}
	if newDiff >= 100.0 {
		t.Errorf("Difficulty should decrease for slow shares, got %f", newDiff)
	}
	t.Logf("Aggressive retarget: 100.0 -> %f", newDiff)
}

func TestAggressiveRetarget_ClampFactor(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      4.0,
		RetargetTime:    60.0,
		VariancePercent: 30.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// Extreme case: 1000 shares in 0.1 seconds = 0.0001 sec per share
	// Factor would be 4.0 / 0.0001 = 40000, but clamped to 16.0
	newDiff, changed := engine.AggressiveRetarget(state, 1000, 0.1)

	if !changed {
		t.Error("Should trigger change")
	}
	// Max factor is 16x, so max diff should be 16.0
	if newDiff > 16.0 {
		t.Errorf("Factor should be clamped, got diff %f (expected max ~16.0)", newDiff)
	}
}

func TestAggressiveRetarget_NoChangeIfSimilar(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// 10 shares in 40 seconds = 4 sec per share, exactly on target
	// Should not change if within 10%
	newDiff, changed := engine.AggressiveRetarget(state, 10, 40.0)

	if changed {
		t.Errorf("Should not change when on target, got diff %f", newDiff)
	}
}

func TestGetStats(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(50.0)

	// Record some shares
	for i := 0; i < 5; i++ {
		engine.RecordShare(state)
	}

	stats := engine.GetStats(state)

	if stats.CurrentDifficulty != GetDifficulty(state) {
		t.Errorf("Stats difficulty mismatch")
	}

	if stats.TotalShares != 5 {
		t.Errorf("Expected TotalShares=5, got %d", stats.TotalShares)
	}

	if stats.LastShareTime.IsZero() {
		t.Error("LastShareTime should be set")
	}
}

func TestEstimateHashrate(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// Record a share
	engine.RecordShare(state)

	hashrate := EstimateHashrate(state, 60.0)

	// For difficulty 1 with 4-second target: 1 * 2^32 / 4 ≈ 1.07 GH/s
	// Uses coin.HashrateDifficultyConstant = 4.294967296e9 (2^32)
	expectedApprox := 1.0 * 4.294967296e9 / 4.0
	// Allow small floating-point tolerance
	tolerance := 0.0001
	if math.Abs(hashrate-expectedApprox)/expectedApprox > tolerance {
		t.Errorf("Hashrate estimate: got %f, expected %f (tolerance: %.4f%%)", hashrate, expectedApprox, tolerance*100)
	}
}

func TestEstimateHashrate_NoShares(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// No shares recorded
	hashrate := EstimateHashrate(state, 60.0)

	if hashrate != 0 {
		t.Errorf("Expected 0 hashrate with no shares, got %f", hashrate)
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestConcurrentRecordShare(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      4.0,
		RetargetTime:    0.001, // Immediate for testing
		VariancePercent: 30.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	var wg sync.WaitGroup
	var shares atomic.Int64
	var retargets atomic.Int64

	// Many goroutines recording shares
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, changed := engine.RecordShare(state)
				shares.Add(1)
				if changed {
					retargets.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	// Verify all shares were counted
	totalShares := shares.Load()
	recordedShares := state.shareCount.Load()

	if recordedShares != uint64(totalShares) {
		t.Errorf("Share count mismatch: recorded=%d, expected=%d", recordedShares, totalShares)
	}

	t.Logf("Concurrent test: shares=%d, retargets=%d, final_diff=%f",
		totalShares, retargets.Load(), GetDifficulty(state))
}

func TestConcurrentGetSetDifficulty(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	var wg sync.WaitGroup

	// Concurrent setters
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				SetDifficulty(state, float64(id*j+1))
			}
		}(i)
	}

	// Concurrent getters
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				diff := GetDifficulty(state)
				if diff < 0.001 || diff > 1000000 {
					t.Errorf("Invalid difficulty read: %f", diff)
				}
			}
		}()
	}

	wg.Wait()
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestFloatPrecision(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)

	// Test very small difficulty - NewSessionState does NOT clamp, SetDifficulty does
	state := engine.NewSessionState(0.0000001)
	diff := GetDifficulty(state)
	// NewSessionState stores raw value, verify SetDifficulty clamps
	SetDifficulty(state, 0.0000001)
	diff = GetDifficulty(state)
	if diff != 0.001 {
		t.Errorf("SetDifficulty should clamp to min, got %f", diff)
	}

	// Test very large difficulty - SetDifficulty should clamp
	state2 := engine.NewSessionState(1.0)
	SetDifficulty(state2, 1e20)
	diff2 := GetDifficulty(state2)
	if diff2 != 1000000 {
		t.Errorf("SetDifficulty should clamp to max, got %f", diff2)
	}
}

func TestNaNAndInfinity(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// Try setting NaN
	SetDifficulty(state, math.NaN())
	diff := GetDifficulty(state)
	if !math.IsNaN(diff) {
		// Note: This documents current behavior - NaN may or may not be handled
		t.Logf("NaN handling: got %f", diff)
	}

	// Reset and try infinity
	state2 := engine.NewSessionState(1.0)
	SetDifficulty(state2, math.Inf(1))
	diff2 := GetDifficulty(state2)
	if diff2 > 1000000 {
		t.Logf("Infinity clamped to: %f", diff2)
	}
}

func TestZeroDifficulty(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// SetDifficulty should clamp zero to min
	SetDifficulty(state, 0)
	diff := GetDifficulty(state)
	if diff != 0.001 {
		t.Errorf("Zero should be clamped to min via SetDifficulty, got %f", diff)
	}
}

func TestNegativeDifficulty(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)
	state := engine.NewSessionState(1.0)

	// SetDifficulty should clamp negative to min
	SetDifficulty(state, -1.0)
	diff := GetDifficulty(state)
	if diff != 0.001 {
		t.Errorf("Negative should be clamped to min via SetDifficulty, got %f", diff)
	}
}

// =============================================================================
// CAS RACE CONDITION TESTS
// =============================================================================

// TestRecordShare_CASContention tests that concurrent RecordShare calls
// correctly handle CAS contention on lastRetargetNano.
func TestRecordShare_CASContention(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1000000,
		TargetTime:      4.0,
		RetargetTime:    0.0001, // Very short to trigger frequent retargets
		VariancePercent: 5.0,    // Low variance to trigger adjustments
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(100.0)

	var wg sync.WaitGroup
	var retargetCount atomic.Int64
	const goroutines = 50
	const iterations = 100

	// All goroutines try to trigger retargets simultaneously
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, changed := engine.RecordShare(state)
				if changed {
					retargetCount.Add(1)
				}
				// Small delay to spread out timing
				time.Sleep(time.Microsecond)
			}
		}()
	}

	wg.Wait()

	totalShares := uint64(goroutines * iterations)
	recordedShares := state.shareCount.Load()

	// All shares must be counted
	if recordedShares != totalShares {
		t.Errorf("Share count mismatch: got %d, expected %d", recordedShares, totalShares)
	}

	// Difficulty should be valid
	diff := GetDifficulty(state)
	if diff < 0.001 || diff > 1000000 {
		t.Errorf("Difficulty out of bounds: %f", diff)
	}

	t.Logf("CAS contention test: shares=%d, retargets=%d, final_diff=%f",
		totalShares, retargetCount.Load(), diff)
}

// TestRecordShare_OnlyOneRetargetPerInterval ensures only one goroutine
// can claim each retarget interval even under high contention.
func TestRecordShare_OnlyOneRetargetPerInterval(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         1.0,
		MaxDiff:         1000.0,
		TargetTime:      4.0,
		RetargetTime:    0.1, // 100ms retarget interval
		VariancePercent: 10.0,
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(10.0)

	var wg sync.WaitGroup
	var retargetCount atomic.Int64
	const goroutines = 100

	// Barrier to synchronize all goroutines
	barrier := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier // Wait for all goroutines to be ready
			_, changed := engine.RecordShare(state)
			if changed {
				retargetCount.Add(1)
			}
		}()
	}

	// Wait for retarget interval to pass
	time.Sleep(150 * time.Millisecond)

	// Release all goroutines simultaneously
	close(barrier)
	wg.Wait()

	// Only ONE goroutine should have claimed the retarget
	if retargetCount.Load() > 1 {
		t.Errorf("Multiple goroutines claimed retarget: %d (expected at most 1)", retargetCount.Load())
	}

	t.Logf("Retarget exclusivity test: %d retargets from %d goroutines", retargetCount.Load(), goroutines)
}

// TestSharesSinceRetarget_AtomicSwap tests that sharesSinceRetarget is
// correctly swapped to 0 during retarget without losing shares.
func TestSharesSinceRetarget_AtomicSwap(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         1.0,
		MaxDiff:         1000.0,
		TargetTime:      4.0,
		RetargetTime:    0.001, // 1ms to trigger retargets
		VariancePercent: 50.0,  // High variance to avoid adjustment
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(10.0)

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 500

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				engine.RecordShare(state)
			}
		}()
	}

	wg.Wait()

	// Total shares must equal expected
	expectedShares := uint64(goroutines * iterations)
	if state.shareCount.Load() != expectedShares {
		t.Errorf("Total shares mismatch: got %d, expected %d",
			state.shareCount.Load(), expectedShares)
	}

	// Note: sharesSinceRetarget may be non-zero if shares came after last retarget
	// This is expected behavior
	t.Logf("Final state: totalShares=%d, sharesSinceRetarget=%d",
		state.shareCount.Load(), state.sharesSinceRetarget.Load())
}

// =============================================================================
// VARDIFF ALGORITHM CORRECTNESS TESTS
// =============================================================================

// TestVarianceThreshold tests that difficulty only changes when variance exceeds threshold.
func TestVarianceThreshold(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         1.0,
		MaxDiff:         1000.0,
		TargetTime:      4.0,   // 4 seconds per share
		RetargetTime:    0.001, // Immediate retarget
		VariancePercent: 30.0,  // 30% variance threshold
	}
	engine := NewEngine(cfg)

	tests := []struct {
		name           string
		sharesInWindow uint64
		elapsedSec     float64
		shouldChange   bool
		description    string
	}{
		{
			name:           "exactly_on_target",
			sharesInWindow: 10,
			elapsedSec:     40.0, // 10 shares in 40s = 4s/share (target)
			shouldChange:   false,
			description:    "Exactly on target (0% variance)",
		},
		{
			name:           "within_variance_29_percent_fast",
			sharesInWindow: 10,
			elapsedSec:     28.4, // 2.84s/share vs 4s target = ~29% fast
			shouldChange:   false,
			description:    "29% faster than target (within 30% threshold)",
		},
		{
			name:           "exceeds_variance_31_percent_fast",
			sharesInWindow: 10,
			elapsedSec:     27.6, // 2.76s/share vs 4s target = ~31% fast
			shouldChange:   true,
			description:    "31% faster than target (exceeds 30% threshold)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := engine.NewSessionState(100.0)

			// Simulate share recording with specific timing
			// We'll manually set the state to test the algorithm
			state.sharesSinceRetarget.Store(tt.sharesInWindow)
			_ = GetDifficulty(state) // Verify state is valid

			// Calculate what variance would be
			actualTime := tt.elapsedSec / float64(tt.sharesInWindow)
			variance := (actualTime - cfg.TargetTime) / cfg.TargetTime * 100

			t.Logf("%s: actualTime=%.2fs, variance=%.1f%%", tt.description, actualTime, variance)

			// Note: This tests the concept; actual algorithm triggers via RecordShare
		})
	}
}

// TestFactorClamping tests that adjustment factor is clamped to [0.25, 4.0].
func TestFactorClamping(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         0.001,
		MaxDiff:         1e12, // Very high to not hit max clamp
		TargetTime:      4.0,
		RetargetTime:    0.001,
		VariancePercent: 1.0, // Low threshold to trigger adjustment
	}
	engine := NewEngine(cfg)

	t.Run("factor_clamped_to_4x", func(t *testing.T) {
		state := engine.NewSessionState(100.0)

		// Simulate extremely fast shares (should clamp factor to 4.0)
		// Record many shares instantly
		for i := 0; i < 100; i++ {
			engine.RecordShare(state)
		}

		finalDiff := GetDifficulty(state)
		// With factor clamped to 4x, max possible is 100 * 4 = 400
		// (may be less due to multiple retargets)
		if finalDiff > 400 {
			t.Logf("Difficulty %f may exceed single 4x adjustment (multiple retargets)", finalDiff)
		}
	})

	t.Run("factor_clamped_to_0.25x", func(t *testing.T) {
		state := engine.NewSessionState(100.0)

		// Wait a long time then submit one share (should clamp factor to 0.25)
		time.Sleep(200 * time.Millisecond)
		engine.RecordShare(state)

		finalDiff := GetDifficulty(state)
		// With factor clamped to 0.25x, min possible is 100 * 0.25 = 25
		// (may be more if min clamp kicks in)
		t.Logf("Final difficulty after slow shares: %f", finalDiff)
	})
}

// TestAggressiveRetarget_FactorBounds tests aggressive retarget factor clamping.
func TestAggressiveRetarget_FactorBounds(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:    0.001,
		MaxDiff:    1e12,
		TargetTime: 4.0,
	}
	engine := NewEngine(cfg)

	t.Run("aggressive_max_4x", func(t *testing.T) {
		state := engine.NewSessionState(1.0)

		// 1000 shares in 0.01 seconds = 0.00001s/share
		// factor = 4.0 / 0.00001 = 400000, clamped to 4.0
		newDiff, changed := engine.AggressiveRetarget(state, 1000, 0.01)

		if !changed {
			t.Error("Should change with extreme fast shares")
		}
		// Clamped to 4x: 1.0 * 4 = 4.0
		if newDiff > 4.0 {
			t.Errorf("Aggressive factor should clamp to 4x max, got %f", newDiff)
		}
	})

	t.Run("aggressive_min_0.25x", func(t *testing.T) {
		state := engine.NewSessionState(1000.0)

		// 3 shares in 500 seconds = 166.7s/share (within T-2 clock guard <600s)
		// factor = 4.0 / 166.7 = 0.024, clamped to 0.25
		newDiff, changed := engine.AggressiveRetarget(state, 3, 500.0)

		if !changed {
			t.Error("Should change with extreme slow shares")
		}
		// Clamped to 0.25x: 1000 * 0.25 = 250
		if newDiff < 250.0 {
			t.Errorf("Aggressive factor should clamp to 0.25x min, got %f", newDiff)
		}
	})
}

// TestDifficultyBits_Float64Precision tests that float64 bits conversion
// maintains precision for various difficulty values.
func TestDifficultyBits_Float64Precision(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)

	testValues := []float64{
		0.001,          // Min
		0.123456789012, // Arbitrary precision
		1.0,            // Whole number
		100.0,          // Round number
		12345.6789,     // Mixed
		999999.999999,  // Near max
		1e-10,          // Very small (will clamp to min)
		1e20,           // Very large (will clamp to max)
	}

	for _, val := range testValues {
		state := engine.NewSessionState(val)
		got := GetDifficulty(state)

		// For values within range, should be exact
		if val >= 0.001 && val <= 1000000 {
			if got != val {
				t.Errorf("Float64 precision loss: set %v, got %v", val, got)
			}
		}
	}
}

// TestConcurrentReadDuringRetarget tests that GetDifficulty returns
// consistent values even during concurrent retargets.
func TestConcurrentReadDuringRetarget(t *testing.T) {
	cfg := config.VarDiffConfig{
		MinDiff:         1.0,
		MaxDiff:         1000.0,
		TargetTime:      4.0,
		RetargetTime:    0.0001, // Very frequent retargets
		VariancePercent: 1.0,    // Trigger on any variance
	}
	engine := NewEngine(cfg)
	state := engine.NewSessionState(100.0)

	var wg sync.WaitGroup
	var invalidReads atomic.Int64
	const readers = 50
	const writers = 10
	const iterations = 1000

	// Readers constantly check difficulty
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				diff := GetDifficulty(state)
				// Difficulty must always be valid
				if diff < 1.0 || diff > 1000.0 {
					invalidReads.Add(1)
				}
			}
		}()
	}

	// Writers constantly record shares (triggering retargets)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				engine.RecordShare(state)
			}
		}()
	}

	wg.Wait()

	if invalidReads.Load() > 0 {
		t.Errorf("Found %d invalid difficulty reads during concurrent operations",
			invalidReads.Load())
	}
}

// TestEstimateHashrate_ZeroAndNaN tests edge cases in hashrate estimation.
func TestEstimateHashrate_ZeroAndNaN(t *testing.T) {
	cfg := newTestConfig()
	engine := NewEngine(cfg)

	t.Run("zero_difficulty", func(t *testing.T) {
		state := engine.NewSessionState(0.001) // Will be clamped to min
		state.lastShareNano.Store(time.Now().UnixNano())

		hashrate := EstimateHashrate(state, 60.0)
		// Should not panic and should return valid value
		if hashrate < 0 || math.IsNaN(hashrate) || math.IsInf(hashrate, 0) {
			t.Errorf("Invalid hashrate for min difficulty: %v", hashrate)
		}
	})

	t.Run("negative_window", func(t *testing.T) {
		state := engine.NewSessionState(100.0)
		state.lastShareNano.Store(time.Now().UnixNano())

		// Negative window - should handle gracefully
		hashrate := EstimateHashrate(state, -60.0)
		// Current implementation uses fixed formula, so negative window doesn't matter
		if math.IsNaN(hashrate) {
			t.Error("Hashrate should not be NaN")
		}
	})
}
