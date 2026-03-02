// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"crypto/subtle"

	"github.com/spiralpool/stratum/internal/crypto"
)

// BuildMerkleTree constructs a full merkle tree from leaves.
// Returns the tree as a flat array: [leaves..., level1..., level2..., root]
// The tree is padded to a power of 2 with zero hashes if needed.
//
// For n leaves (padded to power of 2), the returned array contains:
// - Indices 0 to n-1: leaf nodes
// - Indices n to 2n-2: internal nodes and root
// - The last element is always the root
func BuildMerkleTree(leaves [][]byte) [][]byte {
	if len(leaves) == 0 {
		return nil
	}

	// Pad to power of 2
	treeSize := MerkleTreeSize(len(leaves))

	// Create padded leaves
	paddedLeaves := make([][]byte, treeSize)
	for i := 0; i < treeSize; i++ {
		if i < len(leaves) {
			paddedLeaves[i] = leaves[i]
		} else {
			paddedLeaves[i] = make([]byte, 32) // Zero hash for padding
		}
	}

	// Build tree - store all levels
	tree := make([][]byte, 0, 2*treeSize-1)
	tree = append(tree, paddedLeaves...)

	currentLevel := paddedLeaves
	for len(currentLevel) > 1 {
		nextLevel := make([][]byte, len(currentLevel)/2)
		for i := 0; i < len(currentLevel); i += 2 {
			combined := append(currentLevel[i], currentLevel[i+1]...)
			nextLevel[i/2] = crypto.SHA256d(combined)
		}
		tree = append(tree, nextLevel...)
		currentLevel = nextLevel
	}

	return tree
}

// BuildAuxMerkleRoot constructs the aux merkle tree root from aux block hashes.
//
// For a single aux chain, the root is simply the aux block hash.
// For multiple aux chains, a merkle tree is constructed.
//
// The tree size must be a power of 2. Empty slots are filled with zero hashes.
//
// Returns:
//   - root: The 32-byte merkle root
//   - branch: The merkle branch for the first aux block (for proof construction)
func BuildAuxMerkleRoot(auxBlocks []AuxBlockData) (root []byte, branch [][]byte) {
	if len(auxBlocks) == 0 {
		return nil, nil
	}

	// Single aux chain - root is the block hash, no branch needed
	if len(auxBlocks) == 1 {
		return auxBlocks[0].Hash, nil
	}

	// Multiple aux chains - build merkle tree
	treeSize := MerkleTreeSize(len(auxBlocks))

	// Create leaves (fill empty slots with zero hashes)
	leaves := make([][]byte, treeSize)
	for i := 0; i < treeSize; i++ {
		leaves[i] = make([]byte, 32) // Initialize with zeros
	}

	// Place aux block hashes at their respective chain indices
	for _, auxBlock := range auxBlocks {
		if auxBlock.ChainIndex >= 0 && auxBlock.ChainIndex < treeSize {
			leaves[auxBlock.ChainIndex] = auxBlock.Hash
		}
	}

	// Build merkle tree and collect branches for first aux block
	branch = make([][]byte, 0)
	pos := auxBlocks[0].ChainIndex

	for len(leaves) > 1 {
		// Get sibling for branch
		siblingPos := pos ^ 1
		if siblingPos < len(leaves) {
			branch = append(branch, leaves[siblingPos])
		}

		// Build next level
		nextLevel := make([][]byte, len(leaves)/2)
		for i := 0; i < len(leaves); i += 2 {
			left := leaves[i]
			right := leaves[i+1]
			combined := append(left, right...)
			nextLevel[i/2] = crypto.SHA256d(combined)
		}
		leaves = nextLevel
		pos = pos / 2
	}

	return leaves[0], branch
}

// ComputeMerkleRootFromBranch computes the merkle root given a hash and branch.
// This is used to verify merkle proofs.
//
// Parameters:
//   - hash: The starting hash (leaf node)
//   - branch: The sibling hashes at each level
//   - index: The position index (determines left/right at each level)
//
// Returns the computed merkle root.
func ComputeMerkleRootFromBranch(hash []byte, branch [][]byte, index int) []byte {
	result := make([]byte, len(hash))
	copy(result, hash)

	for i, sibling := range branch {
		var combined []byte
		if (index>>i)&1 == 0 {
			// Hash is on the left, sibling on right
			combined = append(result, sibling...)
		} else {
			// Hash is on the right, sibling on left
			combined = append(sibling, result...)
		}
		result = crypto.SHA256d(combined)
	}

	return result
}

// BuildCoinbaseMerkleBranch extracts the merkle branch for the coinbase transaction.
// This is needed for AuxPoW proof construction.
//
// The coinbase is always at index 0 in the transaction merkle tree.
//
// Parameters:
//   - txHashes: Transaction hashes (excluding coinbase, which is computed separately)
//
// Returns the merkle branch from coinbase position to root.
func BuildCoinbaseMerkleBranch(txHashes [][]byte) [][]byte {
	if len(txHashes) == 0 {
		// No transactions besides coinbase - no branch needed
		return nil
	}

	// Build full leaves including placeholder for coinbase at index 0
	leaves := make([][]byte, len(txHashes)+1)
	leaves[0] = nil // Coinbase placeholder - we're tracking the branch, not computing root
	copy(leaves[1:], txHashes)

	// Collect branches
	branches := make([][]byte, 0)
	pos := 0 // Coinbase is at position 0

	for len(leaves) > 1 {
		// Get sibling
		siblingPos := pos ^ 1
		if siblingPos < len(leaves) && leaves[siblingPos] != nil {
			branches = append(branches, leaves[siblingPos])
		}

		// Build next level
		nextLen := (len(leaves) + 1) / 2
		nextLevel := make([][]byte, nextLen)

		for i := 0; i < len(leaves); i += 2 {
			left := leaves[i]
			var right []byte
			if i+1 < len(leaves) {
				right = leaves[i+1]
			} else {
				right = left // Duplicate for odd count
			}

			// If either is nil (coinbase placeholder), result is nil
			if left == nil || right == nil {
				nextLevel[i/2] = nil
			} else {
				combined := append(left, right...)
				nextLevel[i/2] = crypto.SHA256d(combined)
			}
		}
		leaves = nextLevel
		pos = pos / 2
	}

	return branches
}

// VerifyMerkleBranch verifies a merkle branch leads to the expected root.
//
// Parameters:
//   - hash: The leaf hash to verify
//   - branch: The merkle branch (sibling hashes)
//   - index: The position index in the tree
//   - expectedRoot: The expected merkle root
//
// Returns true if the computed root matches the expected root.
func VerifyMerkleBranch(hash []byte, branch [][]byte, index int, expectedRoot []byte) bool {
	computedRoot := ComputeMerkleRootFromBranch(hash, branch, index)

	if len(computedRoot) != len(expectedRoot) {
		return false
	}

	// SECURITY: Use constant-time comparison to prevent timing attacks
	// The previous implementation used a loop that could be optimized by the compiler
	// to return early, leaking information about the position of the first mismatch.
	return subtle.ConstantTimeCompare(computedRoot, expectedRoot) == 1
}
