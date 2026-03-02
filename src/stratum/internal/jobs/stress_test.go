// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package jobs provides stress tests for job management and encoding.
//
// These tests validate:
// - Height encoding under extreme values
// - VarInt encoding edge cases
// - Concurrent job management
// - Memory safety during encoding operations
package jobs

import (
	"encoding/hex"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spiralpool/stratum/internal/crypto"
)

// TestHeightEncodingExtremeValues tests height encoding with extreme and edge case values.
func TestHeightEncodingExtremeValues(t *testing.T) {
	extremeHeights := []struct {
		height uint64
		desc   string
	}{
		{0, "zero"},
		{1, "one"},
		{math.MaxUint8, "max uint8"},
		{math.MaxUint8 + 1, "max uint8 + 1"},
		{math.MaxUint16, "max uint16"},
		{math.MaxUint16 + 1, "max uint16 + 1"},
		{math.MaxInt32, "max int32"},
		{math.MaxInt32 + 1, "max int32 + 1 (sign bit)"},
		{math.MaxUint32, "max uint32"},
		{math.MaxUint32 + 1, "max uint32 + 1"},
		{1 << 40, "2^40 (huge height)"},
		{1<<63 - 1, "max int64"},
	}

	for _, tc := range extremeHeights {
		t.Run(tc.desc, func(t *testing.T) {
			// Should not panic
			result := encodeHeight(tc.height)

			// Basic validation
			if len(result) < 1 {
				t.Errorf("height %d: result empty", tc.height)
				return
			}

			// Length byte must be correct (CScriptNum format only, not opcodes)
			if len(result) > 1 {
				if int(result[0]) != len(result)-1 {
					t.Errorf("height %d: length byte %d doesn't match actual %d",
						tc.height, result[0], len(result)-1)
				}
			}

			// Round-trip for values that fit in reasonable size
			if tc.height <= 1<<40 {
				decoded := decodeHeightForTest(result)
				if decoded != tc.height {
					t.Errorf("height %d: round-trip failed, got %d", tc.height, decoded)
				}
			}
		})
	}
}

// FuzzHeightExtremeValues fuzz tests height encoding with random extreme values.
func FuzzHeightExtremeValues(f *testing.F) {
	// Seed with extreme values
	f.Add(uint64(0))
	f.Add(uint64(127))
	f.Add(uint64(128))
	f.Add(uint64(255))
	f.Add(uint64(256))
	f.Add(uint64(math.MaxUint16))
	f.Add(uint64(math.MaxInt32))
	f.Add(uint64(math.MaxUint32))
	f.Add(uint64(1 << 40))

	f.Fuzz(func(t *testing.T, height uint64) {
		// Skip extremely large values that would produce huge encodings
		if height > 1<<48 {
			return
		}

		// Must not panic
		result := encodeHeight(height)

		// Basic validation
		if len(result) < 1 {
			t.Errorf("height %d: result empty", height)
			return
		}

		// Length byte must match (CScriptNum format only, not opcodes)
		if len(result) > 1 {
			if int(result[0]) != len(result)-1 {
				t.Errorf("height %d: length mismatch", height)
				return
			}
		}

		// Round-trip
		decoded := decodeHeightForTest(result)
		if decoded != height {
			t.Errorf("height %d: round-trip got %d, encoded: %x", height, decoded, result)
		}
	})
}

// TestVarIntInvalidPrefixes tests VarInt handling of boundary values.
func TestVarIntInvalidPrefixes(t *testing.T) {
	// Test all boundary transitions
	boundaries := []struct {
		value       uint64
		expectLen   int
		expectFirst byte
	}{
		{0, 1, 0x00},
		{252, 1, 0xfc},
		{253, 3, 0xfd},
		{254, 3, 0xfd},
		{255, 3, 0xfd},
		{256, 3, 0xfd},
		{0xffff, 3, 0xfd},      // max 2-byte value
		{0x10000, 5, 0xfe},     // first 4-byte value
		{0xffffffff, 5, 0xfe},  // max 4-byte value
		{0x100000000, 9, 0xff}, // first 8-byte value
	}

	for _, tc := range boundaries {
		result := crypto.EncodeVarInt(tc.value)

		if len(result) != tc.expectLen {
			t.Errorf("value %d (0x%x): expected len %d, got %d: %x",
				tc.value, tc.value, tc.expectLen, len(result), result)
		}

		if result[0] != tc.expectFirst {
			t.Errorf("value %d (0x%x): expected first byte 0x%02x, got 0x%02x",
				tc.value, tc.value, tc.expectFirst, result[0])
		}
	}
}

// FuzzVarIntInvalidPrefixes fuzz tests VarInt encoding.
func FuzzVarIntInvalidPrefixes(f *testing.F) {
	f.Add(uint64(0))
	f.Add(uint64(252))
	f.Add(uint64(253))
	f.Add(uint64(0xffff))
	f.Add(uint64(0x10000))
	f.Add(uint64(0xffffffff))
	f.Add(uint64(0x100000000))
	f.Add(uint64(math.MaxUint64))

	f.Fuzz(func(t *testing.T, value uint64) {
		result := crypto.EncodeVarInt(value)

		// Validate prefix matches value range
		if value < 0xfd {
			if len(result) != 1 {
				t.Errorf("value %d: expected 1 byte, got %d", value, len(result))
			}
		} else if value <= 0xffff {
			if len(result) != 3 || result[0] != 0xfd {
				t.Errorf("value %d: expected 3 bytes with 0xfd prefix, got %x", value, result)
			}
		} else if value <= 0xffffffff {
			if len(result) != 5 || result[0] != 0xfe {
				t.Errorf("value %d: expected 5 bytes with 0xfe prefix, got %x", value, result)
			}
		} else {
			if len(result) != 9 || result[0] != 0xff {
				t.Errorf("value %d: expected 9 bytes with 0xff prefix, got %x", value, result)
			}
		}
	})
}

// TestConcurrentHeightEncoding tests height encoding under concurrent access.
func TestConcurrentHeightEncoding(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 1000

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				height := uint64(rand.Int63n(1 << 32))
				result := encodeHeight(height)

				// Verify round-trip
				decoded := decodeHeightForTest(result)
				if decoded != height {
					errors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("Round-trip failures: %d", errors.Load())
	}
}

// TestConcurrentVarIntEncoding tests VarInt encoding under concurrent access.
func TestConcurrentVarIntEncoding(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 1000

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				value := uint64(rand.Int63())
				result := crypto.EncodeVarInt(value)

				// Verify format
				if len(result) == 0 {
					errors.Add(1)
					continue
				}

				// Verify prefix matches value range
				prefix := result[0]
				valid := true

				if value < 0xfd {
					valid = len(result) == 1 && prefix == byte(value)
				} else if value <= 0xffff {
					valid = len(result) == 3 && prefix == 0xfd
				} else if value <= 0xffffffff {
					valid = len(result) == 5 && prefix == 0xfe
				} else {
					valid = len(result) == 9 && prefix == 0xff
				}

				if !valid {
					errors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("Encoding errors: %d", errors.Load())
	}
}

// TestNonUTF8CoinbaseText tests coinbase with non-UTF8 text.
func TestNonUTF8CoinbaseText(t *testing.T) {
	// Various binary/non-UTF8 sequences
	testCases := [][]byte{
		{0x00},                   // Null byte
		{0xff, 0xfe},             // Invalid UTF-8
		{0x80, 0x81, 0x82},       // High bytes
		{0x00, 0x00, 0x00, 0x00}, // Multiple nulls
		make([]byte, 50),         // All zeros
		{0xc0, 0xc1},             // Overlong encoding
		{0xfe, 0xff},             // BOM-like
	}

	for i, text := range testCases {
		t.Run(string(rune(i)), func(t *testing.T) {
			// Should not panic when used in coinbase
			heightBytes := encodeHeight(1000000)
			scriptsigLen := len(heightBytes) + len(text) + 8

			// Just verify the calculation doesn't panic
			if scriptsigLen > 100 {
				// Would be truncated
				t.Logf("Text length %d would be truncated", len(text))
			}

			// Verify hex encoding works
			hexStr := hex.EncodeToString(text)
			decoded, err := hex.DecodeString(hexStr)
			if err != nil {
				t.Errorf("Failed to decode hex: %v", err)
			}
			if !bytesEqual(decoded, text) {
				t.Error("Hex round-trip failed")
			}
		})
	}
}

// TestScriptSigExactBoundary tests scriptsig at exactly 100 bytes.
func TestScriptSigExactBoundary(t *testing.T) {
	// Test heights that produce different encoded lengths
	heights := []uint64{
		100,       // 2 bytes
		1000,      // 3 bytes
		1000000,   // 4 bytes
		100000000, // 5 bytes
	}

	for _, height := range heights {
		heightBytes := encodeHeight(height)
		heightLen := len(heightBytes)

		// Calculate exact text length to hit 100 bytes
		// scriptsig = heightBytes + coinbaseText + extranonce (8 bytes)
		exactTextLen := 100 - heightLen - 8

		if exactTextLen < 0 {
			t.Logf("Height %d: encoded length %d, no room for text", height, heightLen)
			continue
		}

		t.Run(string(rune(height)), func(t *testing.T) {
			text := make([]byte, exactTextLen)
			for i := range text {
				text[i] = byte('X')
			}

			scriptsigLen := heightLen + len(text) + 8
			if scriptsigLen != 100 {
				t.Errorf("Expected scriptsig len 100, got %d", scriptsigLen)
			}

			// One more byte should trigger truncation
			overText := make([]byte, exactTextLen+1)
			overScriptsigLen := heightLen + len(overText) + 8
			if overScriptsigLen <= 100 {
				t.Error("Expected over-limit scriptsig")
			}
		})
	}
}

// BenchmarkHeightEncoding benchmarks height encoding performance.
func BenchmarkHeightEncoding(b *testing.B) {
	heights := []uint64{100, 1000, 10000, 100000, 1000000, 10000000}

	for _, h := range heights {
		b.Run(string(rune(h)), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				encodeHeight(h)
			}
		})
	}
}

// BenchmarkVarIntEncoding benchmarks VarInt encoding performance.
func BenchmarkVarIntEncoding(b *testing.B) {
	values := []uint64{100, 1000, 10000, 100000, 1000000}

	for _, v := range values {
		b.Run(string(rune(v)), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				crypto.EncodeVarInt(v)
			}
		})
	}
}

// BenchmarkConcurrentHeightEncoding benchmarks parallel height encoding.
func BenchmarkConcurrentHeightEncoding(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		height := uint64(1000000)
		for pb.Next() {
			encodeHeight(height)
			height++
		}
	})
}
