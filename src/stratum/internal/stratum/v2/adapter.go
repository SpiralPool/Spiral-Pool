// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"math/big"
)

// NBitsToTarget converts the compact nBits representation to a full 256-bit target.
// This follows the Bitcoin protocol specification for compact target encoding.
// nBits format: [1 byte exponent][3 bytes mantissa]
// Target = mantissa * 2^(8*(exponent-3))
func NBitsToTarget(nBits uint32) *big.Int {
	// Extract exponent (first byte) and mantissa (lower 3 bytes)
	exponent := int(nBits >> 24)
	mantissa := nBits & 0x00FFFFFF

	// SECURITY: Validate exponent range to prevent overflow
	// Exponent 0 is invalid (would be target = 0)
	if exponent == 0 {
		return big.NewInt(0)
	}

	// SECURITY: Validate exponent upper bound
	// For 256-bit targets, exponent cannot exceed 33 (32 + 1 for the mantissa bytes)
	// Values above this would create targets > 2^256, which are invalid
	// This prevents attacks using nBits like 0xFF7FFFFF that would accept any hash
	if exponent > 33 {
		return big.NewInt(0)
	}

	// SECURITY: Check for negative target (high bit of mantissa set with exponent <= 3)
	// Per Bitcoin protocol, this indicates a negative number which is invalid
	if mantissa&0x800000 != 0 {
		if exponent <= 3 {
			return big.NewInt(0)
		}
		// For exponent > 3, clear the high bit and adjust
		mantissa &= 0x7FFFFF
	}

	// Calculate target = mantissa * 2^(8*(exponent-3))
	target := new(big.Int).SetUint64(uint64(mantissa))

	if exponent > 3 {
		// Shift left by 8*(exponent-3) bits
		// Maximum shift: 8*(33-3) = 240 bits, which keeps target < 2^256
		shift := uint(8 * (exponent - 3))
		target.Lsh(target, shift)
	} else if exponent < 3 {
		// Shift right by 8*(3-exponent) bits
		shift := uint(8 * (3 - exponent))
		target.Rsh(target, shift)
	}
	// If exponent == 3, no shift needed

	return target
}
