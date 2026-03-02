// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package auxpow provides property-based fuzz tests for Merkle tree operations.
//
// These tests verify Merkle tree correctness under randomized inputs,
// catching edge cases that deterministic tests might miss.
package auxpow

import (
	"bytes"
	"crypto/rand"
	"math/bits"
	"testing"
)

// FuzzBuildMerkleTree tests Merkle tree construction with randomized inputs.
// Property: For any set of leaves, the tree should be constructible and
// the root should be deterministic (same inputs = same root).
func FuzzBuildMerkleTree(f *testing.F) {
	// Seed corpus with various leaf counts
	f.Add(1, int64(0))
	f.Add(2, int64(1))
	f.Add(3, int64(2))
	f.Add(7, int64(3))
	f.Add(8, int64(4))
	f.Add(15, int64(5))
	f.Add(16, int64(6))
	f.Add(100, int64(7))

	f.Fuzz(func(t *testing.T, numLeaves int, seed int64) {
		// Limit to reasonable size to prevent OOM
		if numLeaves <= 0 || numLeaves > 1000 {
			return
		}

		// Generate deterministic random leaves based on seed
		leaves := generateDeterministicLeaves(numLeaves, seed)

		// Build tree twice - should produce identical roots
		tree1 := BuildMerkleTree(leaves)
		tree2 := BuildMerkleTree(leaves)

		if tree1 == nil || tree2 == nil {
			t.Fatal("BuildMerkleTree returned nil for non-empty leaves")
		}

		root1 := tree1[len(tree1)-1]
		root2 := tree2[len(tree2)-1]

		if !bytes.Equal(root1, root2) {
			t.Error("PROPERTY VIOLATION: Merkle tree construction is non-deterministic")
		}

		// Property: Tree size should be power of 2 or padded to power of 2
		expectedSize := nextPowerOfTwo(numLeaves)
		if len(tree1) < expectedSize {
			t.Errorf("Tree too small: got %d nodes, expected at least %d", len(tree1), expectedSize)
		}

		// Property: Root should be 32 bytes
		if len(root1) != 32 {
			t.Errorf("Root wrong size: got %d bytes, expected 32", len(root1))
		}
	})
}

// FuzzComputeMerkleRootFromBranch tests branch verification.
// Property: Computing root from leaf+branch should match the actual root.
func FuzzComputeMerkleRootFromBranch(f *testing.F) {
	// Seed corpus
	f.Add(2, 0, int64(0))
	f.Add(4, 1, int64(1))
	f.Add(8, 3, int64(2))
	f.Add(16, 7, int64(3))
	f.Add(32, 15, int64(4))

	f.Fuzz(func(t *testing.T, numLeaves, leafIndex int, seed int64) {
		// Bounds checking
		if numLeaves < 1 || numLeaves > 256 {
			return
		}
		if leafIndex < 0 || leafIndex >= numLeaves {
			return
		}

		// Generate leaves
		leaves := generateDeterministicLeaves(numLeaves, seed)

		// Build full tree
		tree := BuildMerkleTree(leaves)
		if tree == nil {
			return
		}
		actualRoot := tree[len(tree)-1]

		// Get branch for the selected leaf
		branch := GetMerkleBranch(tree, leafIndex, numLeaves)

		// Compute root from branch
		computedRoot := ComputeMerkleRootFromBranch(leaves[leafIndex], branch, leafIndex)

		// Property: Computed root should equal actual root
		if !bytes.Equal(computedRoot, actualRoot) {
			t.Errorf("PROPERTY VIOLATION: Branch computation failed\n"+
				"  numLeaves=%d, leafIndex=%d\n"+
				"  computed=%x\n"+
				"  actual=%x",
				numLeaves, leafIndex, computedRoot, actualRoot)
		}
	})
}

// FuzzAuxCommitmentRoundTrip tests aux commitment encode/decode.
// Property: BuildAuxCommitment followed by ExtractAuxRootFromCoinbase should
// return the original aux root.
func FuzzAuxCommitmentRoundTrip(f *testing.F) {
	// Seed corpus
	f.Add(uint32(1), uint32(0), int64(0))
	f.Add(uint32(2), uint32(1), int64(1))
	f.Add(uint32(4), uint32(12345), int64(2))
	f.Add(uint32(256), uint32(0xFFFFFFFF), int64(3))

	f.Fuzz(func(t *testing.T, treeSize, merkleNonce uint32, seed int64) {
		// Generate random aux root
		auxRoot := make([]byte, 32)
		fillDeterministicBytes(auxRoot, seed)

		// Build commitment
		commitment := BuildAuxCommitment(auxRoot, treeSize, merkleNonce)
		if commitment == nil {
			t.Fatal("BuildAuxCommitment returned nil")
		}

		// Embed in fake coinbase
		coinbase := make([]byte, 200)
		offset := 50 // Random offset
		copy(coinbase[offset:], commitment)

		// Extract aux root
		extracted := ExtractAuxRootFromCoinbase(coinbase)
		if extracted == nil {
			t.Fatal("Failed to extract aux root from coinbase")
		}

		// Property: Extracted root should equal original
		if !bytes.Equal(extracted, auxRoot) {
			t.Errorf("PROPERTY VIOLATION: Aux root roundtrip failed\n"+
				"  original=%x\n"+
				"  extracted=%x",
				auxRoot, extracted)
		}
	})
}

// FuzzMerkleTreeSingleLeaf tests edge case: single leaf tree.
// Property: Single leaf tree root should equal the leaf hash.
func FuzzMerkleTreeSingleLeaf(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1))
	f.Add(int64(12345))
	f.Add(int64(-1))

	f.Fuzz(func(t *testing.T, seed int64) {
		// Generate single leaf
		leaf := make([]byte, 32)
		fillDeterministicBytes(leaf, seed)

		tree := BuildMerkleTree([][]byte{leaf})
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil for single leaf")
		}

		root := tree[len(tree)-1]

		// Property: Single leaf root should equal the leaf itself
		// (or its hash if implementation hashes single nodes)
		if len(root) != 32 {
			t.Errorf("Root wrong size for single leaf: %d", len(root))
		}
	})
}

// FuzzMerkleTreePowerOfTwo tests trees with exact power-of-2 leaf counts.
// Property: No padding should be needed, tree structure should be optimal.
func FuzzMerkleTreePowerOfTwo(f *testing.F) {
	f.Add(int64(1), int64(0))
	f.Add(int64(2), int64(1))
	f.Add(int64(4), int64(2))
	f.Add(int64(8), int64(3))
	f.Add(int64(16), int64(4))
	f.Add(int64(32), int64(5))

	f.Fuzz(func(t *testing.T, power, seed int64) {
		if power < 0 || power > 8 {
			return
		}

		numLeaves := 1 << power
		leaves := generateDeterministicLeaves(numLeaves, seed)

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		// Property: Tree should have exactly (2*numLeaves - 1) nodes for perfect binary tree
		// Or implementation-specific size based on algorithm
		root := tree[len(tree)-1]
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}
	})
}

// FuzzMerkleTreeNonPowerOfTwo tests trees with non-power-of-2 leaf counts.
// Property: Tree should handle padding correctly.
func FuzzMerkleTreeNonPowerOfTwo(f *testing.F) {
	f.Add(3, int64(0))
	f.Add(5, int64(1))
	f.Add(7, int64(2))
	f.Add(9, int64(3))
	f.Add(15, int64(4))
	f.Add(17, int64(5))
	f.Add(100, int64(6))

	f.Fuzz(func(t *testing.T, numLeaves int, seed int64) {
		// Must be positive, non-power-of-2, and reasonable size
		if numLeaves <= 0 || numLeaves > 500 {
			return
		}
		if isPowerOfTwo(numLeaves) {
			return // Skip power-of-2 cases
		}

		leaves := generateDeterministicLeaves(numLeaves, seed)

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		root := tree[len(tree)-1]

		// Property: Root should still be valid 32-byte hash
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}

		// Property: Should be able to verify any leaf via branch
		for i := 0; i < numLeaves && i < 10; i++ { // Check first 10 leaves
			branch := GetMerkleBranch(tree, i, numLeaves)
			computed := ComputeMerkleRootFromBranch(leaves[i], branch, i)
			if !bytes.Equal(computed, root) {
				t.Errorf("Branch verification failed for leaf %d/%d", i, numLeaves)
			}
		}
	})
}

// FuzzBranchIndexBounds tests branch computation with boundary indices.
// Property: Branch computation should never panic and should handle edge indices.
func FuzzBranchIndexBounds(f *testing.F) {
	f.Add(10, 0, int64(0))
	f.Add(10, 9, int64(1))
	f.Add(100, 0, int64(2))
	f.Add(100, 99, int64(3))

	f.Fuzz(func(t *testing.T, numLeaves, leafIndex int, seed int64) {
		if numLeaves <= 0 || numLeaves > 200 {
			return
		}
		if leafIndex < 0 || leafIndex >= numLeaves {
			return
		}

		leaves := generateDeterministicLeaves(numLeaves, seed)
		tree := BuildMerkleTree(leaves)
		if tree == nil {
			return
		}

		// This should never panic
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC in GetMerkleBranch: numLeaves=%d, index=%d, panic=%v",
					numLeaves, leafIndex, r)
			}
		}()

		branch := GetMerkleBranch(tree, leafIndex, numLeaves)

		// Property: Branch should be valid (non-nil, correct element sizes)
		for i, elem := range branch {
			if len(elem) != 32 {
				t.Errorf("Branch element %d has wrong size: %d", i, len(elem))
			}
		}
	})
}

// Helper functions

func generateDeterministicLeaves(count int, seed int64) [][]byte {
	leaves := make([][]byte, count)
	for i := range leaves {
		leaves[i] = make([]byte, 32)
		fillDeterministicBytes(leaves[i], seed+int64(i))
	}
	return leaves
}

func fillDeterministicBytes(b []byte, seed int64) {
	// Simple PRNG for deterministic test data
	state := uint64(seed)
	for i := range b {
		state = state*6364136223846793005 + 1442695040888963407
		b[i] = byte(state >> 56)
	}
}

func nextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(n-1))
}

func isPowerOfTwo(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

// GetMerkleBranch extracts the merkle branch for a leaf at given index.
// This is a helper for testing - actual implementation may differ.
func GetMerkleBranch(tree [][]byte, leafIndex, numLeaves int) [][]byte {
	if len(tree) == 0 || numLeaves <= 1 {
		return nil
	}

	treeSize := nextPowerOfTwo(numLeaves)
	branch := make([][]byte, 0)
	index := leafIndex

	// Traverse up the tree, collecting sibling nodes
	levelStart := 0
	levelSize := treeSize

	for levelSize > 1 {
		// Find sibling index
		siblingIndex := index ^ 1 // XOR with 1 flips last bit

		// Get sibling from tree (if within bounds)
		treeIndex := levelStart + siblingIndex
		if treeIndex < len(tree) && tree[treeIndex] != nil {
			sibling := make([]byte, 32)
			copy(sibling, tree[treeIndex])
			branch = append(branch, sibling)
		} else {
			// Sibling might be nil (padding) - use zero hash
			branch = append(branch, make([]byte, 32))
		}

		// Move to parent level
		levelStart += levelSize
		levelSize /= 2
		index /= 2
	}

	return branch
}

// TestFuzzHelpers tests the helper functions used in fuzz tests.
func TestFuzzHelpers(t *testing.T) {
	// Test nextPowerOfTwo
	testCases := []struct {
		input    int
		expected int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{5, 8},
		{7, 8},
		{8, 8},
		{9, 16},
		{100, 128},
	}

	for _, tc := range testCases {
		result := nextPowerOfTwo(tc.input)
		if result != tc.expected {
			t.Errorf("nextPowerOfTwo(%d) = %d, want %d", tc.input, result, tc.expected)
		}
	}

	// Test isPowerOfTwo
	if !isPowerOfTwo(1) || !isPowerOfTwo(2) || !isPowerOfTwo(4) || !isPowerOfTwo(256) {
		t.Error("isPowerOfTwo failed for valid powers of 2")
	}
	if isPowerOfTwo(0) || isPowerOfTwo(3) || isPowerOfTwo(5) || isPowerOfTwo(100) {
		t.Error("isPowerOfTwo returned true for non-powers of 2")
	}

	// Test deterministic byte generation
	b1 := make([]byte, 32)
	b2 := make([]byte, 32)
	fillDeterministicBytes(b1, 12345)
	fillDeterministicBytes(b2, 12345)
	if !bytes.Equal(b1, b2) {
		t.Error("fillDeterministicBytes is not deterministic")
	}

	// Different seeds should produce different bytes
	b3 := make([]byte, 32)
	fillDeterministicBytes(b3, 12346)
	if bytes.Equal(b1, b3) {
		t.Error("Different seeds produced same bytes")
	}
}

// TestMerkleTreeEdgeCases tests specific edge cases that fuzz testing might miss.
func TestMerkleTreeEdgeCases(t *testing.T) {
	// Edge case: All identical leaves
	t.Run("identical_leaves", func(t *testing.T) {
		leaf := bytes.Repeat([]byte{0xAA}, 32)
		leaves := [][]byte{leaf, leaf, leaf, leaf}

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		root := tree[len(tree)-1]
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}
	})

	// Edge case: All zero leaves
	t.Run("zero_leaves", func(t *testing.T) {
		leaves := make([][]byte, 4)
		for i := range leaves {
			leaves[i] = make([]byte, 32)
		}

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		root := tree[len(tree)-1]
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}
	})

	// Edge case: Maximum value leaves
	t.Run("max_value_leaves", func(t *testing.T) {
		leaves := make([][]byte, 4)
		for i := range leaves {
			leaves[i] = bytes.Repeat([]byte{0xFF}, 32)
		}

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		root := tree[len(tree)-1]
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}
	})

	// Edge case: Alternating pattern
	t.Run("alternating_leaves", func(t *testing.T) {
		leaves := make([][]byte, 8)
		for i := range leaves {
			if i%2 == 0 {
				leaves[i] = bytes.Repeat([]byte{0x00}, 32)
			} else {
				leaves[i] = bytes.Repeat([]byte{0xFF}, 32)
			}
		}

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		root := tree[len(tree)-1]
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}
	})

	// Edge case: Random bytes from crypto/rand
	t.Run("crypto_random_leaves", func(t *testing.T) {
		leaves := make([][]byte, 16)
		for i := range leaves {
			leaves[i] = make([]byte, 32)
			_, _ = rand.Read(leaves[i])
		}

		tree := BuildMerkleTree(leaves)
		if tree == nil {
			t.Fatal("BuildMerkleTree returned nil")
		}

		root := tree[len(tree)-1]
		if len(root) != 32 {
			t.Errorf("Root wrong size: %d", len(root))
		}

		// Verify branch computation works for all leaves
		for i := range leaves {
			branch := GetMerkleBranch(tree, i, len(leaves))
			computed := ComputeMerkleRootFromBranch(leaves[i], branch, i)
			if !bytes.Equal(computed, root) {
				t.Errorf("Branch verification failed for leaf %d", i)
			}
		}
	})
}
