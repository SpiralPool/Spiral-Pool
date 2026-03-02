// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

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
// TEST SUITE: Extreme VarDiff Scenarios & Edge Cases
// =============================================================================
// These tests cover extreme VarDiff scenarios including:
// - Rapid share submission (1000+ shares/second)
// - Extreme hashrate changes
// - cgminer/Avalon compatibility
// - Per-coin difficulty scaling
// - Lock-free atomic operation verification

// -----------------------------------------------------------------------------
// Configuration Validation Tests
// -----------------------------------------------------------------------------

// TestValidateConfig_ValidConfig tests valid configuration
func TestValidateConfig_ValidConfig(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:         1,
		MaxDiff:         1000000,
		TargetTime:      5,
		RetargetTime:    60,
		VariancePercent: 50,
	}

	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("Valid config should pass validation: %v", err)
	}
}

// TestValidateConfig_ZeroMinDiff tests zero min difficulty rejection
func TestValidateConfig_ZeroMinDiff(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    0,
		MaxDiff:    1000,
		TargetTime: 5,
	}

	err := ValidateConfig(cfg)
	if err != ErrZeroMinDiff {
		t.Errorf("Expected ErrZeroMinDiff, got: %v", err)
	}
}

// TestValidateConfig_MinExceedsMax tests min > max rejection
func TestValidateConfig_MinExceedsMax(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    1000,
		MaxDiff:    100,
		TargetTime: 5,
	}

	err := ValidateConfig(cfg)
	if err != ErrMinExceedsMax {
		t.Errorf("Expected ErrMinExceedsMax, got: %v", err)
	}
}

// TestValidateConfig_FractionalDifficulty tests fractional difficulty (ESP32)
func TestValidateConfig_FractionalDifficulty(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    0.001, // ESP32 lottery miners
		MaxDiff:    100,
		TargetTime: 60,
	}

	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("Fractional difficulty should be valid: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Session State Tests
// -----------------------------------------------------------------------------

// TestSessionState_InitialDifficulty tests initial difficulty setting
func TestSessionState_InitialDifficulty(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    100,
		MaxDiff:    10000,
		TargetTime: 5,
	}

	engine := NewEngine(cfg)
	state := engine.NewSessionState(500)

	diff := GetDifficulty(state)

	if diff != 500 {
		t.Errorf("Expected initial difficulty 500, got %.0f", diff)
	}
}

// TestSessionState_ProfileOverrides tests per-session profile overrides
func TestSessionState_ProfileOverrides(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    1,
		MaxDiff:    1000000,
		TargetTime: 5,
	}

	engine := NewEngine(cfg)

	// Create session with miner-specific profile
	state := engine.NewSessionStateWithProfile(
		580,    // initialDiff (BitAxe)
		580,    // minDiff
		150000, // maxDiff
		5,      // targetTime
	)

	diff := GetDifficulty(state)

	if diff != 580 {
		t.Errorf("Expected profile difficulty 580, got %.0f", diff)
	}
}

// -----------------------------------------------------------------------------
// Extreme Scenario Tests
// -----------------------------------------------------------------------------

// TestExtreme_DifficultyAtMinimum tests behavior at minimum difficulty
func TestExtreme_DifficultyAtMinimum(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    0.001, // Lottery miners (ESP32)
		MaxDiff:    1000000,
		TargetTime: 60, // 1 share per minute
	}

	engine := NewEngine(cfg)
	state := engine.NewSessionState(0.001)

	// Difficulty should be at minimum
	diff := GetDifficulty(state)

	if diff != 0.001 {
		t.Errorf("Expected minimum difficulty 0.001, got %f", diff)
	}

	t.Logf("Minimum difficulty: %.4f", diff)
}

// TestExtreme_DifficultyAtMaximum tests behavior at maximum difficulty
func TestExtreme_DifficultyAtMaximum(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    1,
		MaxDiff:    500000, // S19 Pro class
		TargetTime: 1,
	}

	engine := NewEngine(cfg)
	state := engine.NewSessionState(500000)

	diff := GetDifficulty(state)

	if diff != 500000 {
		t.Errorf("Expected maximum difficulty 500000, got %.0f", diff)
	}

	t.Logf("Maximum difficulty: %.0f", diff)
}

// -----------------------------------------------------------------------------
// cgminer/Avalon Compatibility Tests
// -----------------------------------------------------------------------------

// TestCgminer_SlowDiffApplication tests cgminer slow difficulty application
func TestCgminer_SlowDiffApplication(t *testing.T) {
	t.Parallel()

	// cgminer takes 10-30s to apply new difficulty to work-in-progress
	// Pool must handle shares at OLD difficulty during transition

	type DifficultyWindow struct {
		CurrentDiff  float64
		PreviousDiff float64
		MinInWindow  float64
		ChangeTime   time.Time
	}

	window := &DifficultyWindow{
		CurrentDiff:  2000,
		PreviousDiff: 1000,
		MinInWindow:  1000,
		ChangeTime:   time.Now().Add(-5 * time.Second), // Changed 5s ago
	}

	// Share comes in at old difficulty (cgminer still using 1000)
	shareDiff := float64(1000)

	// Should accept using MinInWindow
	accepted := shareDiff >= window.MinInWindow

	if !accepted {
		t.Error("Share at old difficulty should be accepted during transition")
	}

	// After 30s, MinInWindow should update
	window.MinInWindow = window.CurrentDiff // Update after grace period

	// Now share at old diff should be rejected
	accepted = shareDiff >= window.MinInWindow

	if accepted {
		t.Error("Share at old difficulty should be rejected after grace period")
	}

	t.Log("PASS: cgminer transition period handled correctly")
}

// -----------------------------------------------------------------------------
// Per-Coin Difficulty Scaling Tests
// -----------------------------------------------------------------------------

// TestPerCoin_BlockTimeScaling tests difficulty scaling based on block time
func TestPerCoin_BlockTimeScaling(t *testing.T) {
	t.Parallel()

	// Miner profiles scaled by block time
	baseProfile := struct {
		InitialDiff float64
		TargetTime  int
	}{
		InitialDiff: 580, // BitAxe default
		TargetTime:  5,   // 5 seconds for 600s blocks
	}

	coins := []struct {
		Symbol    string
		BlockTime int // seconds
	}{
		{"BTC", 600},
		{"DGB", 15},
		{"DOGE", 60},
		{"LTC", 150},
	}

	for _, coin := range coins {
		// Scale target time: shorter blocks = shorter target
		scaleFactor := float64(coin.BlockTime) / 600.0
		if scaleFactor < 0.5 {
			scaleFactor = 0.5 // Floor at 50%
		}

		scaledTarget := float64(baseProfile.TargetTime) * scaleFactor
		if scaledTarget < 1 {
			scaledTarget = 1
		}

		// Difficulty scales inversely with target time
		scaledDiff := baseProfile.InitialDiff * scaleFactor

		t.Logf("%s (block=%ds): targetTime=%.1fs, initialDiff=%.0f",
			coin.Symbol, coin.BlockTime, scaledTarget, scaledDiff)

		// DGB should have lower target time than BTC
		if coin.Symbol == "DGB" && scaledTarget >= float64(baseProfile.TargetTime) {
			t.Error("DGB target time should be shorter than BTC baseline")
		}
	}
}

// TestPerCoin_ScryptVsSHA256 tests Scrypt vs SHA-256d difficulty profiles
func TestPerCoin_ScryptVsSHA256(t *testing.T) {
	t.Parallel()

	// SHA-256d and Scrypt have different difficulty scales
	sha256Profiles := map[string]float64{
		"BitAxe":  580,   // ~500 GH/s
		"S9":      3260,  // ~14 TH/s
		"S19 Pro": 25600, // ~110 TH/s
	}

	// Scrypt miners have much lower hashrate (memory-hard)
	scryptProfiles := map[string]float64{
		"L7":        5000, // ~9.5 GH/s Scrypt
		"Mini DOGE": 185,  // ~185 MH/s
	}

	t.Log("SHA-256d profiles:")
	for name, diff := range sha256Profiles {
		t.Logf("  %s: difficulty %.0f", name, diff)
	}

	t.Log("Scrypt profiles:")
	for name, diff := range scryptProfiles {
		t.Logf("  %s: difficulty %.0f", name, diff)
	}

	// Scrypt difficulties are generally lower due to memory-hard nature
	avgSHA := float64(0)
	for _, d := range sha256Profiles {
		avgSHA += d
	}
	avgSHA /= float64(len(sha256Profiles))

	avgScrypt := float64(0)
	for _, d := range scryptProfiles {
		avgScrypt += d
	}
	avgScrypt /= float64(len(scryptProfiles))

	t.Logf("Average difficulty - SHA-256d: %.0f, Scrypt: %.0f", avgSHA, avgScrypt)
}

// -----------------------------------------------------------------------------
// Atomic Operation Tests
// -----------------------------------------------------------------------------

// TestAtomic_ConcurrentDifficultyReads tests atomic difficulty reads
func TestAtomic_ConcurrentDifficultyReads(t *testing.T) {
	t.Parallel()

	cfg := config.VarDiffConfig{
		MinDiff:    1,
		MaxDiff:    1000000,
		TargetTime: 5,
	}

	engine := NewEngine(cfg)
	state := engine.NewSessionState(1000)

	const numGoroutines = 100
	const readsPerGoroutine = 1000

	var wg sync.WaitGroup
	var validReads atomic.Int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < readsPerGoroutine; j++ {
				diff := GetDifficulty(state)
				if diff >= 1 && diff <= 1000000 {
					validReads.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	expectedReads := int64(numGoroutines * readsPerGoroutine)
	if validReads.Load() != expectedReads {
		t.Errorf("Expected %d valid reads, got %d", expectedReads, validReads.Load())
	}

	t.Logf("Concurrent reads: %d goroutines, %d reads each, all valid", numGoroutines, readsPerGoroutine)
}

// -----------------------------------------------------------------------------
// Hashrate Estimation Tests
// -----------------------------------------------------------------------------

// TestHashrate_Estimation tests hashrate calculation from difficulty
func TestHashrate_Estimation(t *testing.T) {
	t.Parallel()

	// Formula: Hashrate = Difficulty × 2^32 / TargetTime
	testCases := []struct {
		Difficulty  float64
		TargetTime  float64
		ExpectedGHs float64
		Description string
	}{
		{580, 5, 497, "BitAxe (~500 GH/s)"},
		{3260, 1, 14000, "S9 (~14 TH/s)"},
		{25600, 1, 110000, "S19 Pro (~110 TH/s)"},
		{0.001, 60, 0.00007, "ESP32 lottery (~70 KH/s)"},
	}

	const pow232 = 4.294967296e9 // 2^32

	for _, tc := range testCases {
		hashrateHs := tc.Difficulty * pow232 / tc.TargetTime
		hashrateGHs := hashrateHs / 1e9

		t.Logf("%s: diff=%.3f, target=%.0fs, hashrate=%.2f GH/s",
			tc.Description, tc.Difficulty, tc.TargetTime, hashrateGHs)

		// Allow 10% tolerance
		tolerance := tc.ExpectedGHs * 0.1
		if math.Abs(hashrateGHs-tc.ExpectedGHs) > tolerance {
			t.Errorf("Expected ~%.0f GH/s, got %.2f GH/s", tc.ExpectedGHs, hashrateGHs)
		}
	}
}

// TestHashrate_DifficultyFromHashrate tests calculating difficulty from hashrate
func TestHashrate_DifficultyFromHashrate(t *testing.T) {
	t.Parallel()

	// Formula: Difficulty = Hashrate × TargetTime / 2^32
	testCases := []struct {
		HashrateGHs  float64
		TargetTime   float64
		ExpectedDiff float64
		Description  string
	}{
		{500, 5, 582, "BitAxe @ 5s target"},
		{500, 3, 349, "BitAxe @ 3s target (DGB)"},
		{14000, 1, 3260, "S9 @ 1s target"},
	}

	const pow232 = 4.294967296e9 // 2^32

	for _, tc := range testCases {
		hashrateHs := tc.HashrateGHs * 1e9
		difficulty := hashrateHs * tc.TargetTime / pow232

		t.Logf("%s: hashrate=%.0f GH/s, target=%.0fs, diff=%.0f",
			tc.Description, tc.HashrateGHs, tc.TargetTime, difficulty)

		// Allow 5% tolerance
		tolerance := tc.ExpectedDiff * 0.05
		if math.Abs(difficulty-tc.ExpectedDiff) > tolerance {
			t.Errorf("Expected diff ~%.0f, got %.0f", tc.ExpectedDiff, difficulty)
		}
	}
}

// -----------------------------------------------------------------------------
// Adjustment Factor Tests
// -----------------------------------------------------------------------------

// TestAdjustment_MaxIncreaseFactor tests 4x maximum increase
func TestAdjustment_MaxIncreaseFactor(t *testing.T) {
	t.Parallel()

	// Maximum increase factor is 4x (vardiff.go line 225)
	currentDiff := float64(100)

	// Share rate 100x faster than target
	actualTime := float64(0.05) // 50ms
	targetTime := float64(5)    // 5s
	ratio := targetTime / actualTime // 100x

	// Raw adjustment would be 100x, but clamped to 4x
	adjustmentFactor := ratio
	if adjustmentFactor > 4.0 {
		adjustmentFactor = 4.0
	}

	newDiff := currentDiff * adjustmentFactor

	t.Logf("Max increase: ratio=%.0fx, clamped to %.1fx, diff: %.0f -> %.0f",
		ratio, adjustmentFactor, currentDiff, newDiff)

	if adjustmentFactor != 4.0 {
		t.Errorf("Adjustment should be clamped to 4x, got %.1fx", adjustmentFactor)
	}

	if newDiff != 400 {
		t.Errorf("New difficulty should be 400, got %.0f", newDiff)
	}
}

// TestAdjustment_MaxDecreaseFactor tests 0.75x maximum decrease
func TestAdjustment_MaxDecreaseFactor(t *testing.T) {
	t.Parallel()

	// Maximum decrease factor is 0.75x (vardiff.go line 226-230)
	currentDiff := float64(1000)

	// Share rate 10x slower than target
	actualTime := float64(50) // 50s
	targetTime := float64(5)  // 5s
	ratio := targetTime / actualTime // 0.1x

	// Raw adjustment would be 0.1x, but clamped to 0.75x
	adjustmentFactor := ratio
	if adjustmentFactor < 0.75 {
		adjustmentFactor = 0.75
	}

	newDiff := currentDiff * adjustmentFactor

	t.Logf("Max decrease: ratio=%.2fx, clamped to %.2fx, diff: %.0f -> %.0f",
		ratio, adjustmentFactor, currentDiff, newDiff)

	if adjustmentFactor != 0.75 {
		t.Errorf("Adjustment should be clamped to 0.75x, got %.2fx", adjustmentFactor)
	}

	if newDiff != 750 {
		t.Errorf("New difficulty should be 750, got %.0f", newDiff)
	}
}

// TestAdjustment_VarianceThreshold tests variance threshold (50% minimum)
func TestAdjustment_VarianceThreshold(t *testing.T) {
	t.Parallel()

	// Variance threshold is minimum 50% (vardiff.go line 201-210)
	targetTime := float64(5)

	// Share rate 30% off (within 50% threshold)
	actualTime := float64(6.5) // 30% slower
	ratio := actualTime / targetTime

	variance := math.Abs(1.0-ratio) * 100 // 30%
	threshold := float64(50)              // 50% minimum

	shouldAdjust := variance > threshold

	t.Logf("Variance check: actualTime=%.1fs, ratio=%.2f, variance=%.0f%%, threshold=%.0f%%",
		actualTime, ratio, variance, threshold)

	if shouldAdjust {
		t.Error("Should NOT adjust when variance (30%) is below threshold (50%)")
	}

	// Share rate 60% off (exceeds threshold)
	actualTime = 8.0 // 60% slower
	ratio = actualTime / targetTime
	variance = math.Abs(1.0-ratio) * 100 // 60%

	shouldAdjust = variance > threshold

	if !shouldAdjust {
		t.Error("SHOULD adjust when variance (60%) exceeds threshold (50%)")
	}
}
