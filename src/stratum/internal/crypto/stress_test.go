// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package crypto provides stress tests for cryptographic operations.
//
// These tests validate:
// - Merkle tree construction under stress
// - SHA256d determinism
// - Memory safety during concurrent hashing
// - Large transaction set handling
package crypto

import (
	"bytes"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
)

// TestMerkleTreeLargeSet tests merkle tree with large transaction sets.
func TestMerkleTreeLargeSet(t *testing.T) {
	sizes := []int{1, 2, 3, 10, 100, 1000, 5000, 10000}

	for _, size := range sizes {
		t.Run(string(rune(size))+"_txs", func(t *testing.T) {
			// Generate random hashes
			hashes := make([][]byte, size)
			for i := range hashes {
				hashes[i] = make([]byte, 32)
				rand.Read(hashes[i])
			}

			// Compute merkle root
			root := MerkleRoot(hashes)

			// Validate result
			if root == nil && size > 0 {
				t.Error("Merkle root is nil for non-empty input")
			}
			if root != nil && len(root) != 32 {
				t.Errorf("Merkle root length = %d, want 32", len(root))
			}

			// Verify determinism
			root2 := MerkleRoot(hashes)
			if !bytes.Equal(root, root2) {
				t.Error("Merkle root is not deterministic")
			}
		})
	}
}

// TestMerkleRootDeterminism validates merkle root determinism under stress.
func TestMerkleRootDeterminism(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const iterations = 100
	const txCount = 100

	// Generate fixed hashes
	hashes := make([][]byte, txCount)
	for i := range hashes {
		hashes[i] = make([]byte, 32)
		for j := range hashes[i] {
			hashes[i][j] = byte(i*32 + j)
		}
	}

	// Compute reference root
	referenceRoot := MerkleRoot(hashes)

	// Verify determinism across iterations
	for i := 0; i < iterations; i++ {
		root := MerkleRoot(hashes)
		if !bytes.Equal(root, referenceRoot) {
			t.Errorf("Iteration %d: merkle root differs from reference", i)
		}
	}
}

// TestMerkleTreeImmutability validates that merkle tree doesn't modify input.
func TestMerkleTreeImmutability(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const iterations = 100
	const txCount = 50

	for iter := 0; iter < iterations; iter++ {
		// Create hashes with known values
		original := make([][]byte, txCount)
		backup := make([][]byte, txCount)
		for i := range original {
			original[i] = make([]byte, 32)
			backup[i] = make([]byte, 32)
			for j := range original[i] {
				original[i][j] = byte(iter*1000 + i*32 + j)
				backup[i][j] = original[i][j]
			}
		}

		// Compute merkle root
		MerkleRoot(original)

		// Verify original unchanged
		for i := range original {
			if !bytes.Equal(original[i], backup[i]) {
				t.Errorf("Iteration %d: hash %d was modified", iter, i)
			}
		}
	}
}

// TestConcurrentMerkleRoot tests merkle root computation under concurrent access.
func TestConcurrentMerkleRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 100
	const txCount = 20

	// Create shared input (immutable)
	sharedHashes := make([][]byte, txCount)
	for i := range sharedHashes {
		sharedHashes[i] = make([]byte, 32)
		for j := range sharedHashes[i] {
			sharedHashes[i][j] = byte(i*32 + j)
		}
	}

	// Compute reference root
	referenceRoot := MerkleRoot(sharedHashes)

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				root := MerkleRoot(sharedHashes)
				if !bytes.Equal(root, referenceRoot) {
					errors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("Merkle root inconsistencies: %d", errors.Load())
	}
}

// TestConcurrentSHA256d tests SHA256d under concurrent access.
func TestConcurrentSHA256d(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 100
	const iterations = 1000

	// Shared input
	input := []byte("test input for concurrent SHA256d hashing")
	referenceHash := SHA256d(input)

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				hash := SHA256d(input)
				if !bytes.Equal(hash, referenceHash) {
					errors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("SHA256d inconsistencies: %d", errors.Load())
	}
}

// TestSHA256dInputImmutability validates SHA256d doesn't modify input.
func TestSHA256dInputImmutability(t *testing.T) {
	input := []byte("test input that should not be modified")
	backup := make([]byte, len(input))
	copy(backup, input)

	// Hash multiple times
	for i := 0; i < 100; i++ {
		SHA256d(input)

		if !bytes.Equal(input, backup) {
			t.Errorf("Iteration %d: input was modified", i)
		}
	}
}

// TestMerkleRootOddEvenCounts tests merkle root with various odd/even tx counts.
func TestMerkleRootOddEvenCounts(t *testing.T) {
	// Test counts that exercise odd/even duplication logic
	counts := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 15, 16, 17, 31, 32, 33, 63, 64, 65}

	for _, count := range counts {
		t.Run(string(rune(count)), func(t *testing.T) {
			hashes := make([][]byte, count)
			for i := range hashes {
				hashes[i] = make([]byte, 32)
				for j := range hashes[i] {
					hashes[i][j] = byte(i*32 + j)
				}
			}

			root := MerkleRoot(hashes)

			if root == nil {
				t.Error("Merkle root is nil")
				return
			}
			if len(root) != 32 {
				t.Errorf("Root length = %d, want 32", len(root))
			}

			// Verify determinism
			root2 := MerkleRoot(hashes)
			if !bytes.Equal(root, root2) {
				t.Error("Not deterministic")
			}
		})
	}
}

// TestReverseBytesImmutability validates ReverseBytes doesn't modify input.
func TestReverseBytesImmutability(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const iterations = 1000

	original := make([]byte, 32)
	for i := range original {
		original[i] = byte(i)
	}

	backup := make([]byte, 32)
	copy(backup, original)

	for i := 0; i < iterations; i++ {
		_ = ReverseBytes(original)

		if !bytes.Equal(original, backup) {
			t.Errorf("Iteration %d: input was modified", i)
		}
	}
}

// TestConcurrentReverseBytes tests ReverseBytes under concurrent access.
func TestConcurrentReverseBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 1000

	// Shared input
	input := make([]byte, 32)
	for i := range input {
		input[i] = byte(i)
	}

	expected := make([]byte, 32)
	for i := range expected {
		expected[i] = byte(31 - i)
	}

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				result := ReverseBytes(input)
				if !bytes.Equal(result, expected) {
					errors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("ReverseBytes inconsistencies: %d", errors.Load())
	}
}

// FuzzMerkleTreeLargeSet fuzz tests merkle tree with varying sizes.
func FuzzMerkleTreeLargeSet(f *testing.F) {
	f.Add(1)
	f.Add(2)
	f.Add(3)
	f.Add(10)
	f.Add(100)
	f.Add(1000)

	f.Fuzz(func(t *testing.T, numTx int) {
		if numTx <= 0 || numTx > 50000 {
			return
		}

		hashes := make([][]byte, numTx)
		for i := range hashes {
			hashes[i] = make([]byte, 32)
			for j := range hashes[i] {
				hashes[i][j] = byte((i + j) % 256)
			}
		}

		root := MerkleRoot(hashes)

		if root == nil {
			t.Error("Root is nil")
		}
		if len(root) != 32 {
			t.Errorf("Root length = %d", len(root))
		}

		// Verify determinism
		root2 := MerkleRoot(hashes)
		if !bytes.Equal(root, root2) {
			t.Error("Not deterministic")
		}
	})
}

// BenchmarkMerkleRootStress benchmarks merkle root for various tx counts.
func BenchmarkMerkleRootStress(b *testing.B) {
	sizes := []int{1, 10, 100, 1000}

	for _, size := range sizes {
		hashes := make([][]byte, size)
		for i := range hashes {
			hashes[i] = make([]byte, 32)
			rand.Read(hashes[i])
		}

		b.Run(string(rune(size)), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				MerkleRoot(hashes)
			}
		})
	}
}

// BenchmarkSHA256dStress benchmarks SHA256d for various input sizes.
func BenchmarkSHA256dStress(b *testing.B) {
	sizes := []int{32, 64, 80, 256, 1024}

	for _, size := range sizes {
		input := make([]byte, size)
		rand.Read(input)

		b.Run(string(rune(size)), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				SHA256d(input)
			}
		})
	}
}

// BenchmarkConcurrentMerkleRoot benchmarks parallel merkle root computation.
func BenchmarkConcurrentMerkleRoot(b *testing.B) {
	hashes := make([][]byte, 100)
	for i := range hashes {
		hashes[i] = make([]byte, 32)
		rand.Read(hashes[i])
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			MerkleRoot(hashes)
		}
	})
}

// BenchmarkConcurrentSHA256d benchmarks parallel SHA256d.
func BenchmarkConcurrentSHA256d(b *testing.B) {
	input := make([]byte, 80) // Block header size
	rand.Read(input)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			SHA256d(input)
		}
	})
}
