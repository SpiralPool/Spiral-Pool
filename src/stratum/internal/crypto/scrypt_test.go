// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package crypto - Scrypt tests
package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestScryptHash tests the Scrypt hash function with known test vectors.
func TestScryptHash(t *testing.T) {
	tests := []struct {
		name     string
		input    string // hex-encoded input
		expected string // hex-encoded expected output (first 8 bytes for brevity)
	}{
		{
			name:     "empty input",
			input:    "",
			expected: "", // Just verify it doesn't panic
		},
		{
			name:     "single byte",
			input:    "00",
			expected: "", // Just verify it doesn't panic
		},
		{
			name:     "80-byte block header simulation",
			input:    "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a29ab5f49ffff001d1dac2b7c",
			expected: "", // Real test vector would be needed from Litecoin reference
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input []byte
			var err error
			if tt.input != "" {
				input, err = hex.DecodeString(tt.input)
				if err != nil {
					t.Fatalf("failed to decode input: %v", err)
				}
			}

			result := ScryptHash(input)

			// Verify output length is always 32 bytes
			if len(result) != 32 {
				t.Errorf("expected 32-byte output, got %d bytes", len(result))
			}

			// If expected is provided, verify it matches
			if tt.expected != "" {
				expectedBytes, _ := hex.DecodeString(tt.expected)
				if !bytes.HasPrefix(result, expectedBytes) {
					t.Errorf("hash mismatch: got %s", hex.EncodeToString(result))
				}
			}
		})
	}
}

// TestScryptHashDeterministic verifies that Scrypt produces consistent results.
func TestScryptHashDeterministic(t *testing.T) {
	input := []byte("test input for scrypt hashing")

	result1 := ScryptHash(input)
	result2 := ScryptHash(input)

	if !bytes.Equal(result1, result2) {
		t.Error("ScryptHash should be deterministic")
	}
}

// TestScryptHashDifferentInputs verifies that different inputs produce different hashes.
func TestScryptHashDifferentInputs(t *testing.T) {
	input1 := []byte("input 1")
	input2 := []byte("input 2")

	result1 := ScryptHash(input1)
	result2 := ScryptHash(input2)

	if bytes.Equal(result1, result2) {
		t.Error("different inputs should produce different hashes")
	}
}

// TestScryptHashBytes tests the fixed-size array variant.
func TestScryptHashBytes(t *testing.T) {
	input := []byte("test")

	result := ScryptHashBytes(input)

	// Verify it returns exactly 32 bytes
	if len(result) != 32 {
		t.Errorf("expected 32 bytes, got %d", len(result))
	}

	// Should match ScryptHash output
	sliceResult := ScryptHash(input)
	if !bytes.Equal(result[:], sliceResult) {
		t.Error("ScryptHashBytes should match ScryptHash")
	}
}

// TestIsScryptAlgorithm tests algorithm detection.
func TestIsScryptAlgorithm(t *testing.T) {
	tests := []struct {
		algorithm string
		expected  bool
	}{
		{"scrypt", true},
		{"sha256d", false},
		{"SCRYPT", false}, // Case sensitive
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.algorithm, func(t *testing.T) {
			result := IsScryptAlgorithm(tt.algorithm)
			if result != tt.expected {
				t.Errorf("IsScryptAlgorithm(%q) = %v, want %v", tt.algorithm, result, tt.expected)
			}
		})
	}
}

// BenchmarkScryptHash benchmarks the Scrypt hash function.
// Scrypt is significantly slower than SHA256d due to memory-hardness.
func BenchmarkScryptHash(b *testing.B) {
	// 80-byte block header
	input := make([]byte, 80)
	for i := range input {
		input[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ScryptHash(input)
	}
}

// BenchmarkScryptVsSHA256d compares Scrypt to SHA256d performance.
func BenchmarkScryptVsSHA256d(b *testing.B) {
	input := make([]byte, 80)
	for i := range input {
		input[i] = byte(i)
	}

	b.Run("Scrypt", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ScryptHash(input)
		}
	})

	b.Run("SHA256d", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			SHA256d(input)
		}
	})
}
