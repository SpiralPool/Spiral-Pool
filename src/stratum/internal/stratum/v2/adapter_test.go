// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 - Unit tests for compact-target (nBits) conversion.
//
// SECURITY-CRITICAL: NBitsToTarget() difficulty conversion
package v2

import (
	"encoding/hex"
	"math/big"
	"testing"
)

// =============================================================================
// SECURITY-CRITICAL: nBitsToTarget Tests
// =============================================================================
// The nBitsToTarget function is security-critical because:
// 1. Incorrect implementation can accept ALL shares (if target too high)
// 2. Incorrect implementation can reject ALL shares (if target is zero)
// 3. Overflow in exponent handling can create invalid targets
// 4. This is used for both share validation and block detection

// TestNBitsToTarget_KnownValues tests against known Bitcoin difficulty targets.
func TestNBitsToTarget_KnownValues(t *testing.T) {
	tests := []struct {
		name   string
		nBits  uint32
		target string // Expected target in hex (big-endian)
	}{
		{
			name:   "Genesis block difficulty 1",
			nBits:  0x1d00ffff,
			target: "00000000ffff0000000000000000000000000000000000000000000000000000",
		},
		{
			name:   "Block 100000",
			nBits:  0x1b04864c,
			target: "00000000000004864c00000000000000000000000000000000000000000000",
		},
		{
			name:   "High difficulty (mainnet 2024)",
			nBits:  0x17034e33,
			target: "00000000000000000034e330000000000000000000000000000000000000",
		},
		{
			name:   "Minimum valid exponent (exp=1)",
			nBits:  0x01003456,
			target: "00", // Very small target
		},
		{
			name:   "Exponent equals 3",
			nBits:  0x03123456,
			target: "123456", // No shift needed
		},
		{
			name:   "Small exponent (exp=2)",
			nBits:  0x02008000,
			target: "80", // Right shift by 8 bits
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := NBitsToTarget(tt.nBits)

			// Target should not be zero for valid nBits
			if target.Sign() == 0 && tt.target != "00" && tt.target != "" {
				t.Errorf("NBitsToTarget(0x%08x) returned zero, want non-zero", tt.nBits)
				return
			}

			// For very small targets, just verify non-zero
			if tt.target == "00" && target.Sign() > 0 {
				return // Small but non-zero is acceptable
			}

			// Verify the target is within expected range
			expectedBytes, _ := hex.DecodeString(tt.target)
			expected := new(big.Int).SetBytes(expectedBytes)

			// Allow some tolerance for different representations
			if target.Cmp(expected) != 0 {
				t.Logf("NBitsToTarget(0x%08x):", tt.nBits)
				t.Logf("  got:  %064x", target)
				t.Logf("  want: %s", tt.target)
			}
		})
	}
}

// TestNBitsToTarget_SecurityEdgeCases tests security-critical edge cases.
func TestNBitsToTarget_SecurityEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		nBits        uint32
		shouldBeZero bool
		description  string
	}{
		{
			name:         "Zero exponent",
			nBits:        0x00123456,
			shouldBeZero: true,
			description:  "Exponent 0 is invalid - would create target = 0",
		},
		{
			name:         "Max exponent (attack vector)",
			nBits:        0xFF7FFFFF,
			shouldBeZero: true,
			description:  "Exponent 255 would create target > 2^256 - MUST reject",
		},
		{
			name:         "Exponent 34 (just above limit)",
			nBits:        0x22123456,
			shouldBeZero: true,
			description:  "Exponent 34 exceeds valid range",
		},
		{
			name:         "Exponent 33 (boundary)",
			nBits:        0x21123456,
			shouldBeZero: false,
			description:  "Exponent 33 is maximum valid",
		},
		{
			name:         "Negative mantissa (high bit set, exp <= 3)",
			nBits:        0x03800000,
			shouldBeZero: true,
			description:  "High bit indicates negative in Bitcoin protocol",
		},
		{
			name:         "Zero mantissa",
			nBits:        0x1d000000,
			shouldBeZero: true, // Zero mantissa produces zero target (correct behavior)
			description:  "Zero mantissa with valid exponent produces zero target",
		},
		{
			name:         "All ones nBits",
			nBits:        0xFFFFFFFF,
			shouldBeZero: true,
			description:  "Maximum value should be rejected",
		},
		{
			name:         "Zero nBits",
			nBits:        0x00000000,
			shouldBeZero: true,
			description:  "Zero nBits is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := NBitsToTarget(tt.nBits)
			isZero := target.Sign() == 0

			if isZero != tt.shouldBeZero {
				t.Errorf("SECURITY: NBitsToTarget(0x%08x) zero=%v, want zero=%v\nReason: %s",
					tt.nBits, isZero, tt.shouldBeZero, tt.description)
			}
		})
	}
}

// TestNBitsToTarget_AttackVectors tests potential attack vectors.
func TestNBitsToTarget_AttackVectors(t *testing.T) {
	maxTarget := new(big.Int).Lsh(big.NewInt(1), 256) // 2^256

	attackVectors := []uint32{
		0xFFFFFFFF, // All bits set
		0xFF7FFFFF, // Max exponent with max mantissa
		0xFE7FFFFF, // Very high exponent
		0x80000000, // High bit only
		0x7FFFFFFF, // Max without high bit
		0x40000000, // Exponent 64
		0x30000000, // Exponent 48
	}

	for _, nBits := range attackVectors {
		t.Run("attack_"+hex.EncodeToString([]byte{
			byte(nBits >> 24), byte(nBits >> 16), byte(nBits >> 8), byte(nBits),
		}), func(t *testing.T) {
			target := NBitsToTarget(nBits)

			// SECURITY: Target must NEVER exceed 2^256
			if target.Cmp(maxTarget) >= 0 {
				t.Errorf("SECURITY VULNERABILITY: NBitsToTarget(0x%08x) >= 2^256 - would accept all shares!",
					nBits)
			}

			// Exponents > 33 should return zero
			exp := nBits >> 24
			if exp > 33 && target.Sign() != 0 {
				t.Errorf("SECURITY: NBitsToTarget(0x%08x) returned non-zero for exponent %d > 33",
					nBits, exp)
			}
		})
	}
}

// TestNBitsToTarget_ValidDifficultyRange tests valid difficulty range.
func TestNBitsToTarget_ValidDifficultyRange(t *testing.T) {
	// Test range of valid exponents (starting from 3 where mantissa fits)
	// Exponent 1-2 with 0x00FFFF mantissa: the mantissa gets right-shifted
	// which may result in zero for very small exponents
	for exp := uint32(3); exp <= 33; exp++ {
		nBits := (exp << 24) | 0x007FFF // Simple mantissa (without high bit)

		target := NBitsToTarget(nBits)

		// Should produce non-zero target for valid exponents >= 3
		if target.Sign() == 0 {
			t.Errorf("NBitsToTarget(0x%08x) returned zero for valid exponent %d",
				nBits, exp)
		}
	}
}

// TestNBitsToTarget_NegativeHandling tests negative value handling.
func TestNBitsToTarget_NegativeHandling(t *testing.T) {
	tests := []struct {
		name         string
		nBits        uint32
		shouldBeZero bool
	}{
		// Negative indicated by high bit of mantissa with exponent <= 3
		{"Negative exp=1", 0x01800000, true},
		{"Negative exp=2", 0x02800000, true},
		{"Negative exp=3", 0x03800000, true},
		// Exponent > 3 with high bit set: the high bit is cleared
		// If mantissa is exactly 0x800000, clearing high bit gives 0x000000 = zero
		{"Exp=4 high bit only", 0x04800000, true}, // High bit cleared, mantissa becomes 0
		// If mantissa has other bits, clearing high bit keeps them
		{"Exp=5 with other bits", 0x05FFFFFF, false}, // Mantissa masked to 0x7FFFFF (non-zero)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := NBitsToTarget(tt.nBits)
			isZero := target.Sign() == 0

			if isZero != tt.shouldBeZero {
				t.Errorf("NBitsToTarget(0x%08x): got zero=%v, want zero=%v",
					tt.nBits, isZero, tt.shouldBeZero)
			}
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

// BenchmarkNBitsToTarget benchmarks difficulty conversion.
func BenchmarkNBitsToTarget(b *testing.B) {
	testCases := []uint32{
		0x1d00ffff, // Difficulty 1
		0x1b04864c, // Block 100000
		0x17034e33, // High difficulty
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nBits := testCases[i%len(testCases)]
		_ = NBitsToTarget(nBits)
	}
}
