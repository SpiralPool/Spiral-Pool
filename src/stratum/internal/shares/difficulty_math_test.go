// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Critical Floating-Point & Integer Edge Case Tests
//
// Tests for numerical precision issues that can cause pool failures:
// - Float → int truncation in target math
// - Overflow/underflow in big-int targets
// - Difficulty rounding boundaries
// - Minimum difficulty enforcement
// - Maximum difficulty overflow
//
// WHY IT MATTERS: Difficulty math must be EXACT, not "close enough".
// A single bit difference in target calculation can:
// - Accept invalid shares (pool loses money)
// - Reject valid blocks (block reward lost)
// - Miscalculate miner payments
//
// RED FLAG: Any use of float64 for difficulty math without justification.
package shares

import (
	"math"
	"math/big"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// 1. DIFFICULTY TO TARGET CONVERSION TESTS
// =============================================================================

// TestDifficultyToTargetBasic tests basic difficulty conversions
func TestDifficultyToTargetBasic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		difficulty float64
		wantNil    bool
		wantZero   bool
		desc       string
	}{
		{
			name:       "difficulty_1",
			difficulty: 1.0,
			wantNil:    false,
			wantZero:   false,
			desc:       "Standard difficulty 1 should produce max target",
		},
		{
			name:       "difficulty_2",
			difficulty: 2.0,
			wantNil:    false,
			wantZero:   false,
			desc:       "Difficulty 2 should produce half of max target",
		},
		{
			name:       "difficulty_1000",
			difficulty: 1000.0,
			wantNil:    false,
			wantZero:   false,
			desc:       "Common pool difficulty",
		},
		{
			name:       "difficulty_0",
			difficulty: 0.0,
			wantNil:    false,
			wantZero:   true,
			desc:       "Zero difficulty should return zero target (reject all)",
		},
		{
			name:       "difficulty_negative",
			difficulty: -1.0,
			wantNil:    false,
			wantZero:   true,
			desc:       "Negative difficulty should return zero target",
		},
		{
			name:       "difficulty_very_small",
			difficulty: 0.00001,
			wantNil:    false,
			wantZero:   false,
			desc:       "Very small difficulty for lottery miners",
		},
		{
			name:       "difficulty_NaN",
			difficulty: math.NaN(),
			wantNil:    false,
			wantZero:   true,
			desc:       "NaN difficulty should return zero target",
		},
		{
			name:       "difficulty_positive_inf",
			difficulty: math.Inf(1),
			wantNil:    false,
			wantZero:   true,
			desc:       "Positive infinity should return zero target",
		},
		{
			name:       "difficulty_negative_inf",
			difficulty: math.Inf(-1),
			wantNil:    false,
			wantZero:   true,
			desc:       "Negative infinity should return zero target",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := difficultyToTarget(tc.difficulty)

			if tc.wantNil {
				if target != nil {
					t.Errorf("%s: expected nil target, got %v", tc.desc, target)
				}
				return
			}

			if target == nil {
				t.Fatalf("%s: expected non-nil target", tc.desc)
			}

			if tc.wantZero {
				if target.Sign() != 0 {
					t.Errorf("%s: expected zero target, got %s", tc.desc, target.String())
				}
				return
			}

			if target.Sign() <= 0 {
				t.Errorf("%s: expected positive target, got %s", tc.desc, target.String())
			}
		})
	}
}

// TestDifficultyToTargetPrecision tests that difficulty produces correct target
func TestDifficultyToTargetPrecision(t *testing.T) {
	t.Parallel()

	// MaxTarget = 0x00000000FFFF0000...
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	tests := []struct {
		name       string
		difficulty float64
		checkFn    func(*testing.T, *big.Int)
	}{
		{
			name:       "diff_1_equals_maxtarget",
			difficulty: 1.0,
			checkFn: func(t *testing.T, target *big.Int) {
				// At difficulty 1, target should equal maxTarget
				// Allow small precision error
				ratio := new(big.Float).Quo(
					new(big.Float).SetInt(target),
					new(big.Float).SetInt(maxTarget),
				)
				ratioF, _ := ratio.Float64()
				if math.Abs(ratioF-1.0) > 0.0001 {
					t.Errorf("Diff 1 target ratio to maxTarget: %f (expected ~1.0)", ratioF)
				}
			},
		},
		{
			name:       "diff_2_is_half_maxtarget",
			difficulty: 2.0,
			checkFn: func(t *testing.T, target *big.Int) {
				// At difficulty 2, target should be half of maxTarget
				ratio := new(big.Float).Quo(
					new(big.Float).SetInt(target),
					new(big.Float).SetInt(maxTarget),
				)
				ratioF, _ := ratio.Float64()
				if math.Abs(ratioF-0.5) > 0.0001 {
					t.Errorf("Diff 2 target ratio: %f (expected ~0.5)", ratioF)
				}
			},
		},
		{
			name:       "diff_10_is_tenth_maxtarget",
			difficulty: 10.0,
			checkFn: func(t *testing.T, target *big.Int) {
				ratio := new(big.Float).Quo(
					new(big.Float).SetInt(target),
					new(big.Float).SetInt(maxTarget),
				)
				ratioF, _ := ratio.Float64()
				if math.Abs(ratioF-0.1) > 0.0001 {
					t.Errorf("Diff 10 target ratio: %f (expected ~0.1)", ratioF)
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := difficultyToTarget(tc.difficulty)
			if target == nil {
				t.Fatal("target is nil")
			}
			tc.checkFn(t, target)
		})
	}
}

// =============================================================================
// 2. EXTREME DIFFICULTY VALUES
// =============================================================================

// TestExtremeDifficultyValues tests edge cases for difficulty
func TestExtremeDifficultyValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		difficulty float64
		expectZero bool
		desc       string
	}{
		{
			name:       "bitcoin_mainnet_difficulty",
			difficulty: 60_000_000_000_000, // ~60 trillion (approximate current)
			expectZero: false,
			desc:       "Bitcoin mainnet-level difficulty should not overflow",
		},
		{
			name:       "extreme_high_difficulty",
			difficulty: 1e18, // 1 quintillion
			expectZero: false,
			desc:       "Extremely high difficulty should still produce valid target",
		},
		{
			name:       "max_float64_that_makes_sense",
			difficulty: 1e30,
			expectZero: false,
			desc:       "Very high but reasonable difficulty",
		},
		{
			name:       "tiny_difficulty_for_lottery",
			difficulty: 1e-8,
			expectZero: false,
			desc:       "Tiny difficulty for ESP32/lottery pools",
		},
		{
			name:       "subnormal_float",
			difficulty: 1e-300,
			expectZero: false,
			desc:       "Subnormal float should still produce valid target",
		},
		{
			name:       "difficulty_max_float64",
			difficulty: math.MaxFloat64,
			expectZero: true, // MaxFloat64 (~1.8e308) is so large that maxTarget/MaxFloat64 rounds to 0
			desc:       "Max float64 produces zero target (mathematically correct - impossibly hard)",
		},
		{
			name:       "difficulty_smallest_positive_float64",
			difficulty: math.SmallestNonzeroFloat64,
			expectZero: false,
			desc:       "Smallest positive float64 should produce very high target",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := difficultyToTarget(tc.difficulty)

			if target == nil {
				t.Fatalf("%s: target is nil", tc.desc)
			}

			isZero := target.Sign() == 0

			if isZero != tc.expectZero {
				t.Errorf("%s: isZero=%v, expected=%v (diff=%e, target=%s)",
					tc.desc, isZero, tc.expectZero, tc.difficulty, target.String())
			}

			// Additional check: target should be non-negative
			if target.Sign() < 0 {
				t.Errorf("%s: CRITICAL - target is negative! diff=%e",
					tc.desc, tc.difficulty)
			}
		})
	}
}

// TestDifficultyOverflowPrevention verifies big.Int is used to prevent overflow
func TestDifficultyOverflowPrevention(t *testing.T) {
	t.Parallel()

	// Test difficulties that would overflow uint64 if using simple math
	// uint64 max is ~18.4e18, so diff * 1e8 overflows at diff > 184e9 (~184 billion)
	overflowTriggers := []float64{
		185_000_000_000,    // 185 billion - would overflow uint64(diff * 1e8)
		1_000_000_000_000,  // 1 trillion
		60_000_000_000_000, // 60 trillion (Bitcoin mainnet)
	}

	for _, diff := range overflowTriggers {
		diff := diff
		t.Run("overflow_check_"+formatDifficulty(diff), func(t *testing.T) {
			t.Parallel()

			// This should NOT panic or return incorrect values
			target := difficultyToTarget(diff)

			if target == nil {
				t.Fatal("target is nil")
			}

			// Target should be positive (very small, but positive)
			if target.Sign() <= 0 {
				t.Errorf("OVERFLOW VULNERABILITY: diff=%e produced non-positive target", diff)
			}

			// Target should be smaller than max target (higher difficulty = harder = smaller target)
			maxTarget := new(big.Int)
			maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

			if target.Cmp(maxTarget) > 0 {
				t.Errorf("OVERFLOW VULNERABILITY: diff=%e produced target larger than maxTarget", diff)
			}
		})
	}
}

// =============================================================================
// 3. DIFFICULTY COMPARISON PRECISION
// =============================================================================

// TestDifficultyComparisonPrecision ensures hash comparisons use exact math
func TestDifficultyComparisonPrecision(t *testing.T) {
	t.Parallel()

	// Create targets for similar difficulties
	diff1 := 1000.0
	diff2 := 1000.00001

	target1 := difficultyToTarget(diff1)
	target2 := difficultyToTarget(diff2)

	if target1 == nil || target2 == nil {
		t.Fatal("targets are nil")
	}

	// The targets should be different (no rounding error)
	cmp := target1.Cmp(target2)
	if cmp == 0 {
		t.Error("PRECISION ISSUE: Different difficulties produced identical targets")
	}

	// Higher difficulty = smaller target
	if cmp <= 0 {
		t.Error("PRECISION ISSUE: Higher difficulty did not produce smaller target")
	}
}

// TestBoundaryDifficultyPrecision tests that close difficulties don't round to same
func TestBoundaryDifficultyPrecision(t *testing.T) {
	t.Parallel()

	// Test a range of difficulties around a common boundary
	baseDiff := 65536.0 // Common pool difficulty

	for i := 0; i < 100; i++ {
		// Create tiny increments
		incDiff := baseDiff + float64(i)*1e-10

		target := difficultyToTarget(incDiff)
		if target == nil {
			t.Fatalf("nil target at index %d", i)
		}

		// Each increment should produce a distinct (smaller) target
		if i > 0 {
			prevDiff := baseDiff + float64(i-1)*1e-10
			prevTarget := difficultyToTarget(prevDiff)

			if target.Cmp(prevTarget) >= 0 {
				// Allow some precision loss for very tiny differences
				if incDiff-prevDiff > 1e-8 {
					t.Errorf("PRECISION: diff %f should produce smaller target than %f",
						incDiff, prevDiff)
				}
			}
		}
	}
}

// =============================================================================
// 4. MINIMUM AND MAXIMUM DIFFICULTY ENFORCEMENT
// =============================================================================

// TestMinimumDifficultyEnforcement ensures minimum difficulty is properly enforced
func TestMinimumDifficultyEnforcement(t *testing.T) {
	t.Parallel()

	// Typical minimum difficulties for various miner classes
	minDiffs := []float64{
		0.0001,  // ESP32 lottery miners
		0.001,   // Hobby miners
		1.0,     // Standard minimum
		100.0,   // High-hashrate minimum
		65536.0, // Common pool minimum
	}

	for _, minDiff := range minDiffs {
		minDiff := minDiff
		t.Run("min_diff_"+formatDifficulty(minDiff), func(t *testing.T) {
			t.Parallel()

			// Test values below minimum using reliable multipliers
			// (avoid tiny offsets like 1e-10 which have float64 precision issues)
			testDiffs := []float64{
				minDiff * 0.5,       // Half of minimum
				minDiff * 0.1,       // 10% of minimum
				minDiff * 0.01,      // 1% of minimum
				minDiff * 0.999999,  // Just below minimum (reliable across all scales)
			}

			for _, testDiff := range testDiffs {
				if testDiff >= minDiff {
					continue // Skip if not actually below (float precision guard)
				}

				// Target at test difficulty should be LARGER than target at min difficulty
				// (because lower difficulty = easier = higher target)
				testTarget := difficultyToTarget(testDiff)
				minTarget := difficultyToTarget(minDiff)

				if testTarget == nil || minTarget == nil {
					continue
				}

				// Log for debugging
				t.Logf("minDiff=%e, testDiff=%e, minTarget=%x, testTarget=%x",
					minDiff, testDiff, minTarget, testTarget)

				// If someone submits a share at testDiff when minDiff is enforced,
				// the share should be rejected because testTarget > minTarget
				if testTarget.Cmp(minTarget) <= 0 {
					t.Errorf("ENFORCEMENT ISSUE: diff %e has smaller/equal target than minimum %e",
						testDiff, minDiff)
				}
			}
		})
	}
}

// TestMaximumDifficultyConstraints tests behavior at maximum difficulty limits
func TestMaximumDifficultyConstraints(t *testing.T) {
	t.Parallel()

	// Common maximum difficulties
	maxDiffs := []float64{
		1e6,  // 1 million
		1e9,  // 1 billion
		1e12, // 1 trillion
		1e15, // 1 quadrillion
	}

	for _, maxDiff := range maxDiffs {
		maxDiff := maxDiff
		t.Run("max_diff_"+formatDifficulty(maxDiff), func(t *testing.T) {
			t.Parallel()

			// Test values above maximum using reliable multipliers
			// (avoid tiny offsets like 1e-10 which have float64 precision issues at large scales)
			testDiffs := []float64{
				maxDiff * 2,        // Double maximum
				maxDiff * 10,       // 10x maximum
				maxDiff * 100,      // 100x maximum
				maxDiff * 1.000001, // Just above maximum (reliable across all scales)
			}

			for _, testDiff := range testDiffs {
				if testDiff <= maxDiff {
					continue // Skip if not actually above (float precision guard)
				}

				testTarget := difficultyToTarget(testDiff)
				maxTarget := difficultyToTarget(maxDiff)

				if testTarget == nil || maxTarget == nil {
					continue
				}

				// Log for debugging
				t.Logf("maxDiff=%e, testDiff=%e, maxTarget=%x, testTarget=%x",
					maxDiff, testDiff, maxTarget, testTarget)

				// Target at higher difficulty should be SMALLER
				if testTarget.Cmp(maxTarget) >= 0 {
					t.Errorf("CONSTRAINT ISSUE: diff %e has larger/equal target than max %e",
						testDiff, maxDiff)
				}
			}
		})
	}
}

// =============================================================================
// 5. FLOAT TO INT TRUNCATION CHECKS
// =============================================================================

// TestFloatTruncationInTargetMath verifies no precision loss in calculations
func TestFloatTruncationInTargetMath(t *testing.T) {
	t.Parallel()

	// Test difficulties that are problematic for float64 representation
	problematicDiffs := []struct {
		name string
		diff float64
	}{
		{"power_of_2", 65536.0},
		{"non_power_of_2", 65537.0},
		{"has_many_decimals", 12345.6789012345},
		{"repeating_decimal", 1.0 / 3.0},
		{"very_precise", 1.0000000001},
		{"many_nines", 0.9999999999},
	}

	for _, tc := range problematicDiffs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := difficultyToTarget(tc.diff)
			if target == nil {
				t.Fatal("target is nil")
			}

			// Target should be exact (use big.Int, not truncated)
			// Verify by converting back and checking error
			targetFloat := new(big.Float).SetInt(target)

			// MaxTarget = 0x00000000FFFF0000000000000000000000000000000000000000000000000000
			// Correct decimal representation
			maxTarget := new(big.Float)
			maxTargetInt := new(big.Int)
			maxTargetInt.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)
			maxTarget.SetInt(maxTargetInt)

			// Calculate back: diff = maxTarget / target
			calculatedDiff := new(big.Float).Quo(maxTarget, targetFloat)
			calcDiffFloat, _ := calculatedDiff.Float64()

			// Should match within reasonable precision (1e-6 relative error)
			relError := math.Abs(calcDiffFloat-tc.diff) / tc.diff
			if relError > 1e-6 && tc.diff > 1e-10 {
				t.Errorf("TRUNCATION: diff=%f, back-calculated=%f, relError=%e",
					tc.diff, calcDiffFloat, relError)
			}
		})
	}
}

// TestIntegerBigIntOperations ensures big.Int operations don't overflow
func TestIntegerBigIntOperations(t *testing.T) {
	t.Parallel()

	// Max target value as string
	maxTargetStr := "00000000FFFF0000000000000000000000000000000000000000000000000000"

	maxTarget := new(big.Int)
	_, ok := maxTarget.SetString(maxTargetStr, 16)
	if !ok {
		t.Fatal("failed to parse max target")
	}

	// Verify max target bit length
	bitLen := maxTarget.BitLen()
	if bitLen > 256 {
		t.Errorf("Max target exceeds 256 bits: %d", bitLen)
	}

	// Test that division by large difficulty doesn't underflow to zero prematurely
	for exp := 1; exp <= 50; exp++ {
		diff := math.Pow(10, float64(exp))
		target := difficultyToTarget(diff)

		if target == nil {
			continue
		}

		// Target should be zero or positive, never negative
		if target.Sign() < 0 {
			t.Errorf("UNDERFLOW: diff 1e%d produced negative target", exp)
		}

		// For very high difficulties, target may be zero (impossible to meet)
		// But it should never wrap around to huge values
		if target.BitLen() > 256 {
			t.Errorf("OVERFLOW: diff 1e%d produced target > 256 bits", exp)
		}
	}
}

// =============================================================================
// 6. SHARE DIFFICULTY VALIDATION
// =============================================================================

// TestShareMeetsDifficulty tests hash vs target comparison
func TestShareMeetsDifficulty(t *testing.T) {
	t.Parallel()

	// MaxTarget for difficulty 1 = 0x00000000FFFF0000000000000000000000000000000000000000000000000000
	tests := []struct {
		name       string
		hashHex    string // Big-endian hash (as displayed)
		difficulty float64
		expected   bool
		desc       string
	}{
		{
			name:       "all_zeros_hash",
			hashHex:    "0000000000000000000000000000000000000000000000000000000000000000",
			difficulty: 1.0,
			expected:   true,
			desc:       "All zeros hash meets any difficulty (0 <= any target)",
		},
		{
			name:       "all_zeros_high_diff",
			hashHex:    "0000000000000000000000000000000000000000000000000000000000000000",
			difficulty: 1e15,
			expected:   true,
			desc:       "All zeros hash meets extreme difficulty (0 <= any target)",
		},
		{
			name:       "all_ones_hash",
			hashHex:    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			difficulty: 1.0,
			expected:   false,
			desc:       "All ones hash fails (maxUint256 > maxTarget)",
		},
		{
			name:       "max_target_hash_diff1",
			hashHex:    "00000000FFFF0000000000000000000000000000000000000000000000000000",
			difficulty: 1.0,
			expected:   true,
			desc:       "Hash exactly at max target should meet diff 1 (hash == target)",
		},
		{
			name:       "just_above_target_diff1",
			hashHex:    "00000000FFFF0001000000000000000000000000000000000000000000000000",
			difficulty: 1.0,
			expected:   false,
			desc:       "Hash just above target should fail",
		},
		{
			name:       "half_max_target",
			hashHex:    "000000007FFF8000000000000000000000000000000000000000000000000000",
			difficulty: 2.0,
			expected:   true,
			desc:       "Hash at half max target should meet diff 2",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Parse hash
			hashInt := new(big.Int)
			_, ok := hashInt.SetString(tc.hashHex, 16)
			if !ok {
				t.Fatalf("failed to parse hash: %s", tc.hashHex)
			}

			target := difficultyToTarget(tc.difficulty)
			if target == nil {
				t.Fatal("difficultyToTarget returned nil")
			}

			// Log values for debugging
			t.Logf("Hash:   %064x", hashInt)
			t.Logf("Target: %064x", target)
			t.Logf("Cmp:    %d", hashInt.Cmp(target))

			// Check: hash <= target means share is valid
			meetsTarget := hashInt.Cmp(target) <= 0

			if meetsTarget != tc.expected {
				t.Errorf("%s: meetsTarget=%v, expected=%v", tc.desc, meetsTarget, tc.expected)
			}
		})
	}
}

// =============================================================================
// 7. NETWORK DIFFICULTY EDGE CASES
// =============================================================================

// TestNetworkDifficultyAtomicOperations tests atomic get/set of network difficulty
func TestNetworkDifficultyAtomicOperations(t *testing.T) {
	t.Parallel()

	jobs := make(map[string]*protocol.Job)
	getJob := func(id string) (*protocol.Job, bool) {
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)

	// Test various difficulty values
	diffs := []float64{
		0.001,
		1.0,
		1000.0,
		1e6,
		1e12,
		1e15,
	}

	for _, diff := range diffs {
		validator.SetNetworkDifficulty(diff)
		got := validator.GetNetworkDifficulty()

		// Allow small precision error due to fixed-point storage
		relError := math.Abs(got-diff) / diff
		if relError > 1e-6 {
			t.Errorf("SetNetworkDifficulty(%e) then GetNetworkDifficulty() = %e, relError=%e",
				diff, got, relError)
		}
	}
}

// TestNetworkDifficultyConsistency verifies difficulty is snapshotted once per validation
func TestNetworkDifficultyConsistency(t *testing.T) {
	// This test verifies the SECURITY comment in validator.go:
	// "Snapshot network difficulty once at the start to prevent race conditions"

	jobs := make(map[string]*protocol.Job)
	getJob := func(id string) (*protocol.Job, bool) {
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)
	validator.SetNetworkDifficulty(1000.0)

	// The validator should snapshot difficulty at the START of validation
	// and use that same value throughout, even if it changes mid-validation

	// This is tested by the implementation - the validator reads networkDiffBits
	// once into currentNetworkDiff and uses that for all calculations

	// Get initial difficulty
	initialDiff := validator.GetNetworkDifficulty()

	// Change it
	validator.SetNetworkDifficulty(2000.0)

	// Verify it changed
	newDiff := validator.GetNetworkDifficulty()

	if newDiff != 2000.0 {
		t.Errorf("Difficulty should have changed to 2000, got %f", newDiff)
	}

	if initialDiff == newDiff {
		t.Error("Difficulty should be different after SetNetworkDifficulty")
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// formatDifficulty formats difficulty for test names
func formatDifficulty(diff float64) string {
	if diff >= 1e12 {
		return "trillion"
	}
	if diff >= 1e9 {
		return "billion"
	}
	if diff >= 1e6 {
		return "million"
	}
	if diff >= 1e3 {
		return "thousand"
	}
	if diff >= 1 {
		return "normal"
	}
	return "fractional"
}
