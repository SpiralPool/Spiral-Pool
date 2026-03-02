// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package crypto provides tests for cryptographic primitives.
//
// These tests validate:
// - SHA256d (double SHA256) for DigiByte SHA256d algorithm
// - Merkle tree construction
// - Byte reversal utilities
package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestSHA256d validates double SHA256 hash computation.
// This is the standard hash function for DigiByte SHA256d algorithm.
func TestSHA256d(t *testing.T) {
	tests := []struct {
		name     string
		input    string // hex encoded
		expected string // hex encoded
	}{
		{
			name:     "empty",
			input:    "",
			expected: "5df6e0e2761359d30a8275058e299fcc0381534545f55cf43e41983f5d4c9456",
		},
		{
			name:     "hello",
			input:    "68656c6c6f", // "hello" in hex
			expected: "9595c9df90075148eb06860365df33584b75bff782a510c6cd4883a419833d50",
		},
		{
			name:     "block header pattern",
			input:    "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a29ab5f49ffff001d1dac2b7c",
			expected: "6fe28c0ab6f1b372c1a6a246ae63f74f931e8365e15a089c68d6190000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := hex.DecodeString(tt.input)
			expected, _ := hex.DecodeString(tt.expected)

			result := SHA256d(input)

			if !bytes.Equal(result, expected) {
				t.Errorf("SHA256d(%s) = %x, want %x", tt.input, result, expected)
			}
		})
	}
}

// TestSHA256dBytes validates the fixed-size array version.
func TestSHA256dBytes(t *testing.T) {
	input := []byte("test data")
	result := SHA256dBytes(input)
	resultSlice := SHA256d(input)

	if !bytes.Equal(result[:], resultSlice) {
		t.Errorf("SHA256dBytes and SHA256d produce different results")
	}
}

// TestSHA256Single validates single SHA256 computation.
func TestSHA256Single(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty",
			input:    "",
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:     "hello",
			input:    "hello",
			expected: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SHA256Single([]byte(tt.input))
			expected, _ := hex.DecodeString(tt.expected)

			if !bytes.Equal(result, expected) {
				t.Errorf("SHA256Single(%s) = %x, want %x", tt.input, result, expected)
			}
		})
	}
}

// TestMerkleRoot validates merkle root computation.
func TestMerkleRoot(t *testing.T) {
	tests := []struct {
		name    string
		hashes  [][]byte
		hasRoot bool
	}{
		{
			name:    "empty",
			hashes:  [][]byte{},
			hasRoot: false,
		},
		{
			name:    "single hash",
			hashes:  [][]byte{make32Bytes(1)},
			hasRoot: true,
		},
		{
			name:    "two hashes",
			hashes:  [][]byte{make32Bytes(1), make32Bytes(2)},
			hasRoot: true,
		},
		{
			name:    "three hashes (odd)",
			hashes:  [][]byte{make32Bytes(1), make32Bytes(2), make32Bytes(3)},
			hasRoot: true,
		},
		{
			name:    "four hashes",
			hashes:  [][]byte{make32Bytes(1), make32Bytes(2), make32Bytes(3), make32Bytes(4)},
			hasRoot: true,
		},
		{
			name:    "five hashes (odd)",
			hashes:  [][]byte{make32Bytes(1), make32Bytes(2), make32Bytes(3), make32Bytes(4), make32Bytes(5)},
			hasRoot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := MerkleRoot(tt.hashes)

			if tt.hasRoot {
				if root == nil {
					t.Error("Expected non-nil root")
					return
				}
				if len(root) != 32 {
					t.Errorf("Root length = %d, want 32", len(root))
				}
			} else {
				if root != nil {
					t.Error("Expected nil root for empty input")
				}
			}
		})
	}
}

// TestMerkleRootOddCount validates odd transaction count handling.
func TestMerkleRootOddCount(t *testing.T) {
	// With 3 transactions, the last one should be duplicated
	hashes := [][]byte{
		make32Bytes(1),
		make32Bytes(2),
		make32Bytes(3),
	}

	root := MerkleRoot(hashes)
	if root == nil || len(root) != 32 {
		t.Fatal("Failed to compute merkle root for odd count")
	}

	// Manually compute expected:
	// Level 0: [h1, h2, h3]
	// Level 1: [H(h1+h2), H(h3+h3)]
	// Level 2: [H(level1[0] + level1[1])]

	h12 := SHA256d(append(hashes[0], hashes[1]...))
	h33 := SHA256d(append(hashes[2], hashes[2]...)) // Duplicated
	expected := SHA256d(append(h12, h33...))

	if !bytes.Equal(root, expected) {
		t.Errorf("Merkle root mismatch for odd count\nGot:      %x\nExpected: %x", root, expected)
	}
}

// TestMerkleRootSingleHash validates single hash returns itself.
func TestMerkleRootSingleHash(t *testing.T) {
	hash := make32Bytes(42)
	root := MerkleRoot([][]byte{hash})

	if !bytes.Equal(root, hash) {
		t.Errorf("Single hash should return itself\nGot:      %x\nExpected: %x", root, hash)
	}
}

// TestMerkleRootDeterministic validates consistent results.
func TestMerkleRootDeterministic(t *testing.T) {
	hashes := [][]byte{
		make32Bytes(1),
		make32Bytes(2),
		make32Bytes(3),
		make32Bytes(4),
	}

	root1 := MerkleRoot(hashes)
	root2 := MerkleRoot(hashes)

	if !bytes.Equal(root1, root2) {
		t.Error("MerkleRoot not deterministic")
	}
}

// TestMerkleRootDoesNotModifyInput validates input preservation.
func TestMerkleRootDoesNotModifyInput(t *testing.T) {
	original := [][]byte{
		make32Bytes(1),
		make32Bytes(2),
	}

	// Make copies to compare
	copy1 := make([]byte, 32)
	copy2 := make([]byte, 32)
	copy(copy1, original[0])
	copy(copy2, original[1])

	MerkleRoot(original)

	if !bytes.Equal(original[0], copy1) {
		t.Error("MerkleRoot modified input[0]")
	}
	if !bytes.Equal(original[1], copy2) {
		t.Error("MerkleRoot modified input[1]")
	}
}

// TestReverseBytes validates byte reversal.
func TestReverseBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{"empty", []byte{}, []byte{}},
		{"single", []byte{0x01}, []byte{0x01}},
		{"two", []byte{0x01, 0x02}, []byte{0x02, 0x01}},
		{"four", []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x04, 0x03, 0x02, 0x01}},
		{"32 bytes", make32Bytes(1), reverse32(make32Bytes(1))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ReverseBytes(tt.input)
			if !bytes.Equal(result, tt.expected) {
				t.Errorf("ReverseBytes(%x) = %x, want %x", tt.input, result, tt.expected)
			}
		})
	}
}

// TestReverseBytesDoesNotModifyInput validates input preservation.
func TestReverseBytesDoesNotModifyInput(t *testing.T) {
	original := []byte{0x01, 0x02, 0x03, 0x04}
	copyOriginal := make([]byte, len(original))
	copy(copyOriginal, original)

	ReverseBytes(original)

	if !bytes.Equal(original, copyOriginal) {
		t.Error("ReverseBytes modified input")
	}
}

// TestReverseBytesInPlace validates in-place reversal.
func TestReverseBytesInPlace(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{"empty", []byte{}, []byte{}},
		{"single", []byte{0x01}, []byte{0x01}},
		{"two", []byte{0x01, 0x02}, []byte{0x02, 0x01}},
		{"four", []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x04, 0x03, 0x02, 0x01}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy since we're modifying in place
			input := make([]byte, len(tt.input))
			copy(input, tt.input)

			ReverseBytesInPlace(input)
			if !bytes.Equal(input, tt.expected) {
				t.Errorf("ReverseBytesInPlace(%x) = %x, want %x", tt.input, input, tt.expected)
			}
		})
	}
}

// TestDoubleReverseIsIdentity validates reversal is its own inverse.
func TestDoubleReverseIsIdentity(t *testing.T) {
	original := make32Bytes(42)
	reversed := ReverseBytes(original)
	doubleReversed := ReverseBytes(reversed)

	if !bytes.Equal(original, doubleReversed) {
		t.Error("Double reverse should return original")
	}
}

// Helper functions

func make32Bytes(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}

func reverse32(b []byte) []byte {
	result := make([]byte, 32)
	for i := 0; i < 32; i++ {
		result[i] = b[31-i]
	}
	return result
}

// Benchmark tests

func BenchmarkSHA256d(b *testing.B) {
	data := make([]byte, 80) // Block header size
	for i := range data {
		data[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SHA256d(data)
	}
}

func BenchmarkMerkleRoot(b *testing.B) {
	// Simulate a block with 1000 transactions
	hashes := make([][]byte, 1000)
	for i := range hashes {
		hashes[i] = make32Bytes(byte(i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MerkleRoot(hashes)
	}
}

func BenchmarkReverseBytes32(b *testing.B) {
	data := make32Bytes(1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReverseBytes(data)
	}
}

// Fuzz tests (Go 1.18+)

func FuzzSHA256d(f *testing.F) {
	// Add seed corpus
	f.Add([]byte{})
	f.Add([]byte("hello"))
	f.Add(make([]byte, 80))

	f.Fuzz(func(t *testing.T, data []byte) {
		result := SHA256d(data)
		if len(result) != 32 {
			t.Errorf("SHA256d returned %d bytes, want 32", len(result))
		}

		// Result should be deterministic
		result2 := SHA256d(data)
		if !bytes.Equal(result, result2) {
			t.Error("SHA256d not deterministic")
		}
	})
}

func FuzzMerkleRoot(f *testing.F) {
	// Add seed corpus with varying sizes
	f.Add(1)
	f.Add(2)
	f.Add(3)
	f.Add(100)

	f.Fuzz(func(t *testing.T, numHashes int) {
		if numHashes <= 0 || numHashes > 10000 {
			return // Skip invalid or too large inputs
		}

		hashes := make([][]byte, numHashes)
		for i := range hashes {
			hashes[i] = make32Bytes(byte(i % 256))
		}

		root := MerkleRoot(hashes)
		if root == nil {
			t.Error("MerkleRoot returned nil for non-empty input")
		}
		if len(root) != 32 {
			t.Errorf("MerkleRoot returned %d bytes, want 32", len(root))
		}
	})
}
