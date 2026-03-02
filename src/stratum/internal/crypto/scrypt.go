// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package crypto - Scrypt implementation for Litecoin-compatible mining.
//
// Scrypt is a memory-hard key derivation function used by Litecoin, Dogecoin,
// and other cryptocurrencies. It was designed by Colin Percival (2009) to make
// large-scale custom hardware attacks costly by requiring significant memory.
//
// Parameters for Litecoin-compatible Scrypt:
//   - N = 1024 (CPU/memory cost parameter, must be power of 2)
//   - r = 1    (block size parameter)
//   - p = 1    (parallelization parameter)
//   - dkLen = 32 (output length in bytes)
//
// Memory requirement: 128 * N * r * p = 128 * 1024 * 1 * 1 = 128 KB per hash
//
// References:
//   - Litecoin Core: https://github.com/litecoin-project/litecoin
//   - Scrypt parameters (N=1024, r=1, p=1) are the Litecoin network standard
//   - Uses golang.org/x/crypto/scrypt (BSD-3-Clause)
package crypto

import (
	"golang.org/x/crypto/scrypt"
)

// Scrypt parameters for Litecoin-compatible coins (LTC, DOGE)
const (
	ScryptN      = 1024 // CPU/memory cost (must be power of 2)
	ScryptR      = 1    // Block size
	ScryptP      = 1    // Parallelization
	ScryptKeyLen = 32   // Output hash length (256 bits)
)

// ScryptHash computes the Scrypt hash of the input data using Litecoin-compatible parameters.
//
// For block header hashing, the 80-byte block header is used as both the password
// and salt inputs to the Scrypt function. This matches the Litecoin reference
// implementation behavior.
//
// SECURITY: The Scrypt function is memory-hard, requiring 128 KB of memory per hash.
// This makes it significantly more resistant to GPU and ASIC optimization compared
// to SHA256d, though ASICs now exist for Scrypt as well.
func ScryptHash(data []byte) []byte {
	// In Litecoin's Scrypt implementation, the block header is used as both
	// password and salt. This is intentional and matches the reference impl.
	result, err := scrypt.Key(data, data, ScryptN, ScryptR, ScryptP, ScryptKeyLen)
	if err != nil {
		// scrypt.Key only returns an error for invalid parameters
		// With our fixed, valid parameters, this should never happen
		// Return all zeros as a safe fallback (will fail validation)
		return make([]byte, ScryptKeyLen)
	}
	return result
}

// ScryptHashBytes is an alias for ScryptHash that returns a fixed-size array.
func ScryptHashBytes(data []byte) [32]byte {
	result := ScryptHash(data)
	var arr [32]byte
	copy(arr[:], result)
	return arr
}

// IsScryptAlgorithm returns true if the algorithm name indicates Scrypt.
func IsScryptAlgorithm(algorithm string) bool {
	return algorithm == "scrypt"
}
