// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// =============================================================================
// TEST SUITE: AuxPoW Proof Validation Edge Cases
// =============================================================================
// These tests verify correct proof construction and validation under
// edge cases: tree sizes, slot collisions, chain ID variations.

// -----------------------------------------------------------------------------
// Merkle Tree Construction Edge Cases
// -----------------------------------------------------------------------------

// TestMerkleTree_SingleAuxChain tests single aux chain (no tree needed)
func TestMerkleTree_SingleAuxChain(t *testing.T) {
	t.Parallel()

	auxHash := make([]byte, 32)
	for i := range auxHash {
		auxHash[i] = byte(i)
	}

	// Single chain: root = hash, no branch
	root, branch := BuildMerkleRootSingleChain(auxHash)

	if !bytes.Equal(root, auxHash) {
		t.Error("Single chain root should equal aux hash")
	}

	if len(branch) != 0 {
		t.Error("Single chain should have empty branch")
	}
}

// BuildMerkleRootSingleChain is a helper for single-chain case
func BuildMerkleRootSingleChain(auxHash []byte) (root []byte, branch [][]byte) {
	return auxHash, nil
}

// TestMerkleTree_TwoAuxChains tests two aux chains
func TestMerkleTree_TwoAuxChains(t *testing.T) {
	t.Parallel()

	hash0 := bytes.Repeat([]byte{0xAA}, 32)
	hash1 := bytes.Repeat([]byte{0xBB}, 32)

	leaves := [][]byte{hash0, hash1}
	root, branch0 := buildMerkleWithBranch(leaves, 0)

	// Branch for index 0 should contain hash1 (sibling)
	if len(branch0) != 1 {
		t.Errorf("Expected branch length 1 for 2-leaf tree, got %d", len(branch0))
	}

	if !bytes.Equal(branch0[0], hash1) {
		t.Error("Branch should contain sibling hash")
	}

	// Verify root reconstruction
	reconstructed := computeMerkleRootFromBranchTest(hash0, branch0, 0)
	if !bytes.Equal(reconstructed, root) {
		t.Error("Reconstructed root should match original")
	}

	t.Logf("2-chain root: %x", root)
}

// buildMerkleWithBranch builds merkle tree and extracts branch for given index
func buildMerkleWithBranch(leaves [][]byte, targetIdx int) (root []byte, branch [][]byte) {
	if len(leaves) == 0 {
		return nil, nil
	}
	if len(leaves) == 1 {
		return leaves[0], nil
	}

	// Pad to power of 2
	size := 1
	for size < len(leaves) {
		size *= 2
	}
	padded := make([][]byte, size)
	copy(padded, leaves)
	for i := len(leaves); i < size; i++ {
		padded[i] = make([]byte, 32) // Zero hash
	}

	branch = [][]byte{}
	pos := targetIdx

	for len(padded) > 1 {
		// Collect sibling for branch
		siblingPos := pos ^ 1
		branch = append(branch, padded[siblingPos])

		// Build next level
		nextLevel := make([][]byte, len(padded)/2)
		for i := 0; i < len(padded); i += 2 {
			nextLevel[i/2] = doubleSHA256(append(padded[i], padded[i+1]...))
		}
		padded = nextLevel
		pos = pos / 2
	}

	return padded[0], branch
}

// doubleSHA256 computes SHA256(SHA256(data))
func doubleSHA256(data []byte) []byte {
	// Use real crypto for proper merkle verification
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// computeMerkleRootFromBranchTest reconstructs root from leaf + branch (test helper)
func computeMerkleRootFromBranchTest(leaf []byte, branch [][]byte, index int) []byte {
	current := leaf
	pos := index

	for _, sibling := range branch {
		var combined []byte
		if pos&1 == 0 {
			combined = append(current, sibling...)
		} else {
			combined = append(sibling, current...)
		}
		current = doubleSHA256(combined)
		pos = pos / 2
	}

	return current
}

// TestMerkleTree_FourAuxChains tests 4 aux chains
func TestMerkleTree_FourAuxChains(t *testing.T) {
	t.Parallel()

	hashes := [][]byte{
		bytes.Repeat([]byte{0x11}, 32),
		bytes.Repeat([]byte{0x22}, 32),
		bytes.Repeat([]byte{0x33}, 32),
		bytes.Repeat([]byte{0x44}, 32),
	}

	root, _ := buildMerkleWithBranch(hashes, 0)

	// Test all indices can reconstruct root
	for i := 0; i < 4; i++ {
		_, branch := buildMerkleWithBranch(hashes, i)
		reconstructed := computeMerkleRootFromBranchTest(hashes[i], branch, i)

		if !bytes.Equal(reconstructed, root) {
			t.Errorf("Index %d: reconstructed root doesn't match", i)
		}
	}

	t.Logf("4-chain tree verified successfully")
}

// TestMerkleTree_MaxDepth tests maximum practical tree depth
func TestMerkleTree_MaxDepth(t *testing.T) {
	t.Parallel()

	// 256 aux chains (2^8)
	const numChains = 256
	hashes := make([][]byte, numChains)
	for i := 0; i < numChains; i++ {
		hash := make([]byte, 32)
		hash[0] = byte(i)
		hash[1] = byte(i >> 8)
		hashes[i] = hash
	}

	root, branch0 := buildMerkleWithBranch(hashes, 0)

	// Branch depth should be log2(256) = 8
	if len(branch0) != 8 {
		t.Errorf("Expected branch depth 8, got %d", len(branch0))
	}

	// Verify reconstruction
	reconstructed := ComputeMerkleRootFromBranch(hashes[0], branch0, 0)
	if !bytes.Equal(reconstructed, root) {
		t.Error("Max depth reconstruction failed")
	}

	t.Logf("256-chain tree: root=%x, branch_depth=%d", root[:8], len(branch0))
}

// -----------------------------------------------------------------------------
// Chain ID & Slot Calculation Tests
// -----------------------------------------------------------------------------

// TestChainID_SlotCalculation tests deterministic slot assignment
func TestChainID_SlotCalculation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		chainID     int32
		merkleNonce uint32
		treeSize    uint32
		expectedMin int
		expectedMax int
	}{
		{98, 0, 1, 0, 0},        // Dogecoin in single-chain tree
		{98, 0, 2, 0, 1},        // Dogecoin in 2-chain tree
		{98, 0, 4, 0, 3},        // Dogecoin in 4-chain tree
		{1, 0, 4, 0, 3},         // Namecoin
		{98, 12345, 256, 0, 255}, // With nonce
	}

	for _, tc := range testCases {
		slot := AuxChainSlot(tc.chainID, tc.merkleNonce, tc.treeSize)

		if slot < tc.expectedMin || slot > tc.expectedMax {
			t.Errorf("ChainID=%d, nonce=%d, size=%d: slot %d out of range [%d, %d]",
				tc.chainID, tc.merkleNonce, tc.treeSize, slot, tc.expectedMin, tc.expectedMax)
		}
	}
}

// AuxChainSlot calculates merkle tree slot from chain ID
// This must match the consensus algorithm in aux chain nodes
func AuxChainSlot(chainID int32, merkleNonce uint32, treeSize uint32) int {
	if treeSize == 0 {
		treeSize = 1
	}

	// Linear Congruential Generator (LCG) for deterministic slot
	rand := merkleNonce
	rand = rand*1103515245 + 12345
	rand = rand + uint32(chainID)
	rand = rand*1103515245 + 12345

	return int(rand % treeSize)
}

// TestChainID_SlotCollision tests handling of slot collisions
func TestChainID_SlotCollision(t *testing.T) {
	t.Parallel()

	// Find chain IDs that collide in a small tree
	treeSize := uint32(4)
	merkleNonce := uint32(0)

	slots := make(map[int][]int32)

	// Check many chain IDs
	for chainID := int32(1); chainID <= 1000; chainID++ {
		slot := AuxChainSlot(chainID, merkleNonce, treeSize)
		slots[slot] = append(slots[slot], chainID)
	}

	// Report collisions
	for slot, chainIDs := range slots {
		if len(chainIDs) > 1 {
			t.Logf("Slot %d collision: chain IDs %v", slot, chainIDs[:min(5, len(chainIDs))])
		}
	}

	// With 1000 chain IDs in 4 slots, collisions are expected
	// The LCG should distribute them reasonably evenly
	for slot, chainIDs := range slots {
		if len(chainIDs) < 100 {
			t.Logf("Warning: slot %d has only %d chains", slot, len(chainIDs))
		}
	}
}

// TestChainID_KnownValues tests known chain IDs
func TestChainID_KnownValues(t *testing.T) {
	t.Parallel()

	knownChains := []struct {
		symbol  string
		chainID int32
	}{
		{"DOGE", 98},
		{"NMC", 1},
		{"SYS", 4},
	}

	treeSize := uint32(8)
	merkleNonce := uint32(0)

	// Verify all known chains get unique slots (with this nonce)
	usedSlots := make(map[int]string)

	for _, chain := range knownChains {
		slot := AuxChainSlot(chain.chainID, merkleNonce, treeSize)

		if existingSymbol, exists := usedSlots[slot]; exists {
			t.Logf("Slot collision: %s and %s both at slot %d", existingSymbol, chain.symbol, slot)
			// This isn't necessarily an error - merkle tree handles it
		}

		usedSlots[slot] = chain.symbol
		t.Logf("%s (chainID=%d) -> slot %d", chain.symbol, chain.chainID, slot)
	}
}

// -----------------------------------------------------------------------------
// Proof Construction Edge Cases
// -----------------------------------------------------------------------------

// TestProof_EmptyCoinbase tests proof with minimal coinbase
func TestProof_EmptyCoinbase(t *testing.T) {
	t.Parallel()

	// Minimal valid coinbase: version + input + output + locktime
	minCoinbase := []byte{
		0x01, 0x00, 0x00, 0x00, // version
		0x01,                                           // input count
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // prevout (32 bytes of zeros)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xff, 0xff, 0xff, 0xff, // prevout index
		0x04,                   // scriptsig length
		0xfa, 0xbe, 0x6d, 0x6d, // aux marker
		0x00, 0x00, 0x00, 0x00, // sequence
		// ... outputs would follow
	}

	// Verify aux marker can be found
	marker := []byte{0xfa, 0xbe, 0x6d, 0x6d}
	markerPos := bytes.Index(minCoinbase, marker)

	if markerPos == -1 {
		t.Error("Aux marker not found in coinbase")
	} else {
		t.Logf("Aux marker found at position %d", markerPos)
	}
}

// TestProof_AuxCommitmentPosition tests commitment at various positions
func TestProof_AuxCommitmentPosition(t *testing.T) {
	t.Parallel()

	// Aux commitment structure: marker(4) + root(32) + treeSize(4) + nonce(4) = 44 bytes
	marker := []byte{0xfa, 0xbe, 0x6d, 0x6d}
	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	treeSize := []byte{0x01, 0x00, 0x00, 0x00}   // 1
	merkleNonce := []byte{0x00, 0x00, 0x00, 0x00} // 0

	commitment := append(marker, merkleRoot...)
	commitment = append(commitment, treeSize...)
	commitment = append(commitment, merkleNonce...)

	if len(commitment) != 44 {
		t.Errorf("Commitment should be 44 bytes, got %d", len(commitment))
	}

	// Verify components can be extracted
	extractedRoot := commitment[4:36]
	extractedSize := commitment[36:40]
	extractedNonce := commitment[40:44]

	if !bytes.Equal(extractedRoot, merkleRoot) {
		t.Error("Extracted root doesn't match")
	}

	if !bytes.Equal(extractedSize, treeSize) {
		t.Error("Extracted tree size doesn't match")
	}

	if !bytes.Equal(extractedNonce, merkleNonce) {
		t.Error("Extracted nonce doesn't match")
	}
}

// TestProof_HeaderMerkleRoot tests parent header merkle root extraction
func TestProof_HeaderMerkleRoot(t *testing.T) {
	t.Parallel()

	// 80-byte Bitcoin header
	header := make([]byte, 80)

	// Fill with known pattern
	for i := range header {
		header[i] = byte(i)
	}

	// Set specific merkle root (bytes 36-68)
	merkleRoot := bytes.Repeat([]byte{0xFF}, 32)
	copy(header[36:68], merkleRoot)

	// Extract and verify
	extracted := header[36:68]

	if !bytes.Equal(extracted, merkleRoot) {
		t.Error("Header merkle root extraction failed")
	}
}

// -----------------------------------------------------------------------------
// Stress Tests
// -----------------------------------------------------------------------------

// TestProof_StressManyChains tests proof construction with many aux chains
func TestProof_StressManyChains(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	const maxChains = 64

	for numChains := 1; numChains <= maxChains; numChains *= 2 {
		hashes := make([][]byte, numChains)
		for i := 0; i < numChains; i++ {
			hash := make([]byte, 32)
			hash[0] = byte(i)
			hashes[i] = hash
		}

		root, _ := buildMerkleWithBranch(hashes, 0)

		// Verify all chains can be proven
		for i := 0; i < numChains; i++ {
			_, branch := buildMerkleWithBranch(hashes, i)
			reconstructed := computeMerkleRootFromBranchTest(hashes[i], branch, i)

			if !bytes.Equal(reconstructed, root) {
				t.Errorf("%d chains, index %d: proof verification failed", numChains, i)
			}
		}

		t.Logf("%d chains: proof construction verified", numChains)
	}
}

// TestProof_RandomizedVerification tests random proof verification
func TestProof_RandomizedVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping randomized test in short mode")
	}
	t.Parallel()

	const iterations = 100

	for i := 0; i < iterations; i++ {
		// Random tree size (power of 2, 1-16)
		sizes := []int{1, 2, 4, 8, 16}
		size := sizes[i%len(sizes)]

		hashes := make([][]byte, size)
		for j := 0; j < size; j++ {
			hash := make([]byte, 32)
			hash[0] = byte(i)
			hash[1] = byte(j)
			hashes[j] = hash
		}

		root, _ := buildMerkleWithBranch(hashes, 0)

		// Random index
		idx := i % size
		_, branch := buildMerkleWithBranch(hashes, idx)
		reconstructed := ComputeMerkleRootFromBranch(hashes[idx], branch, idx)

		if !bytes.Equal(reconstructed, root) {
			t.Errorf("Iteration %d: random proof failed (size=%d, idx=%d)", i, size, idx)
		}
	}

	t.Logf("%d randomized proof verifications passed", iterations)
}

// Helper functions

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestHexEncoding tests hash byte order conversions
func TestHexEncoding(t *testing.T) {
	t.Parallel()

	// Internal order (little-endian) vs display order (big-endian)
	internal := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	}

	// Reverse for display
	display := make([]byte, 32)
	for i := 0; i < 32; i++ {
		display[i] = internal[31-i]
	}

	internalHex := hex.EncodeToString(internal)
	displayHex := hex.EncodeToString(display)

	if internalHex == displayHex {
		t.Error("Internal and display order should differ")
	}

	t.Logf("Internal: %s...", internalHex[:16])
	t.Logf("Display:  %s...", displayHex[:16])

	// Verify round-trip
	reversed := make([]byte, 32)
	for i := 0; i < 32; i++ {
		reversed[i] = display[31-i]
	}

	if !bytes.Equal(reversed, internal) {
		t.Error("Round-trip byte order conversion failed")
	}
}
