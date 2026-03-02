// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package crypto provides cryptographic primitives for mining operations.
//
// The SHA256d (double SHA256) implementation follows the Bitcoin protocol
// specification as defined in Satoshi Nakamoto's original whitepaper.
package crypto

import (
	"crypto/sha256"
)

// SHA256d computes the double SHA256 hash of the input data.
// This is the standard hashing algorithm for Bitcoin and DigiByte SHA256d.
//
// SHA256d(x) = SHA256(SHA256(x))
func SHA256d(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// SHA256dBytes is an alias for SHA256d that returns a fixed-size array.
func SHA256dBytes(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

// SHA256Single computes a single SHA256 hash.
func SHA256Single(data []byte) []byte {
	hash := sha256.Sum256(data)
	return hash[:]
}

// MerkleRoot computes the merkle root from a list of transaction hashes.
// The first hash should be the coinbase transaction.
func MerkleRoot(hashes [][]byte) []byte {
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	// Make a copy to avoid modifying the input
	level := make([][]byte, len(hashes))
	copy(level, hashes)

	for len(level) > 1 {
		nextLevel := make([][]byte, 0, (len(level)+1)/2)

		for i := 0; i < len(level); i += 2 {
			var combined []byte
			if i+1 < len(level) {
				combined = append(level[i], level[i+1]...)
			} else {
				// Odd number of hashes - duplicate the last one
				combined = append(level[i], level[i]...)
			}
			nextLevel = append(nextLevel, SHA256d(combined))
		}

		level = nextLevel
	}

	return level[0]
}

// ReverseBytes returns a reversed copy of the input byte slice.
// Used for converting between big-endian and little-endian representations.
func ReverseBytes(b []byte) []byte {
	result := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		result[i] = b[len(b)-1-i]
	}
	return result
}

// ReverseBytesInPlace reverses a byte slice in place.
func ReverseBytesInPlace(b []byte) {
	for i := 0; i < len(b)/2; i++ {
		j := len(b) - 1 - i
		b[i], b[j] = b[j], b[i]
	}
}

// EncodeVarInt encodes an integer as a Bitcoin-style CompactSize variable length integer.
//   - 0-252: 1 byte (value)
//   - 253-65535: 3 bytes (0xfd + uint16 LE)
//   - 65536-4294967295: 5 bytes (0xfe + uint32 LE)
//   - larger: 9 bytes (0xff + uint64 LE)
func EncodeVarInt(n uint64) []byte {
	if n < 0xfd {
		return []byte{byte(n)}
	} else if n <= 0xffff {
		return []byte{0xfd, byte(n), byte(n >> 8)}
	} else if n <= 0xffffffff {
		return []byte{0xfe, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	}
	return []byte{0xff, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24),
		byte(n >> 32), byte(n >> 40), byte(n >> 48), byte(n >> 56)}
}
