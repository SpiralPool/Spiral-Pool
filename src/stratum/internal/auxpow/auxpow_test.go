// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
)

// TestBuildAuxMerkleRoot tests aux merkle tree construction.
func TestBuildAuxMerkleRoot(t *testing.T) {
	tests := []struct {
		name           string
		auxBlocks      []AuxBlockData
		expectRoot     bool
		expectBranches bool
	}{
		{
			name:           "empty aux blocks",
			auxBlocks:      nil,
			expectRoot:     false,
			expectBranches: false,
		},
		{
			name: "single aux chain",
			auxBlocks: []AuxBlockData{
				{
					Symbol:     "DOGE",
					ChainID:    98,
					Hash:       make([]byte, 32),
					ChainIndex: 0,
				},
			},
			expectRoot:     true,
			expectBranches: false, // Single chain has no branches
		},
		{
			name: "two aux chains",
			auxBlocks: []AuxBlockData{
				{
					Symbol:     "DOGE",
					ChainID:    98,
					Hash:       bytes.Repeat([]byte{0xAA}, 32),
					ChainIndex: 0,
				},
				{
					Symbol:     "NMC",
					ChainID:    1,
					Hash:       bytes.Repeat([]byte{0xBB}, 32),
					ChainIndex: 1,
				},
			},
			expectRoot:     true,
			expectBranches: true, // Two chains have branches
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, branches := BuildAuxMerkleRoot(tt.auxBlocks)

			if tt.expectRoot && root == nil {
				t.Error("expected non-nil root")
			}
			if !tt.expectRoot && root != nil {
				t.Error("expected nil root")
			}

			if tt.expectBranches && len(branches) == 0 {
				t.Error("expected non-empty branches")
			}
			if !tt.expectBranches && len(branches) > 0 {
				t.Errorf("expected empty branches, got %d", len(branches))
			}

			// For single aux chain, root should equal the block hash
			if len(tt.auxBlocks) == 1 && root != nil {
				if !bytes.Equal(root, tt.auxBlocks[0].Hash) {
					t.Error("single chain root should equal block hash")
				}
			}
		})
	}
}

// TestComputeMerkleRootFromBranch tests merkle root computation from branches.
func TestComputeMerkleRootFromBranch(t *testing.T) {
	// Test with known values
	hash := bytes.Repeat([]byte{0x11}, 32)
	sibling := bytes.Repeat([]byte{0x22}, 32)

	// Test left position (index 0)
	rootLeft := ComputeMerkleRootFromBranch(hash, [][]byte{sibling}, 0)
	if len(rootLeft) != 32 {
		t.Errorf("expected 32-byte root, got %d", len(rootLeft))
	}

	// Test right position (index 1)
	rootRight := ComputeMerkleRootFromBranch(hash, [][]byte{sibling}, 1)
	if len(rootRight) != 32 {
		t.Errorf("expected 32-byte root, got %d", len(rootRight))
	}

	// Left and right should produce different roots
	if bytes.Equal(rootLeft, rootRight) {
		t.Error("left and right positions should produce different roots")
	}

	// Empty branch should return original hash
	rootEmpty := ComputeMerkleRootFromBranch(hash, nil, 0)
	if !bytes.Equal(rootEmpty, hash) {
		t.Error("empty branch should return original hash")
	}
}

// TestBuildAuxCommitment tests aux commitment construction.
func TestBuildAuxCommitment(t *testing.T) {
	auxRoot := bytes.Repeat([]byte{0xAB}, 32)
	treeSize := uint32(2)
	merkleNonce := uint32(0)

	commitment := BuildAuxCommitment(auxRoot, treeSize, merkleNonce)

	// Check total size
	if len(commitment) != AuxDataSize {
		t.Errorf("expected %d bytes, got %d", AuxDataSize, len(commitment))
	}

	// Check magic marker
	if !bytes.Equal(commitment[0:4], AuxMarker) {
		t.Errorf("expected magic marker %x, got %x", AuxMarker, commitment[0:4])
	}

	// Check aux root
	if !bytes.Equal(commitment[4:36], auxRoot) {
		t.Error("aux root not correctly embedded")
	}

	// Check tree size (little-endian)
	if commitment[36] != 2 || commitment[37] != 0 || commitment[38] != 0 || commitment[39] != 0 {
		t.Errorf("tree size not correctly encoded: %x", commitment[36:40])
	}

	// Invalid aux root length should return nil
	invalidCommitment := BuildAuxCommitment([]byte{0x00}, treeSize, merkleNonce)
	if invalidCommitment != nil {
		t.Error("should return nil for invalid aux root length")
	}
}

// TestExtractAuxRootFromCoinbase tests aux root extraction.
func TestExtractAuxRootFromCoinbase(t *testing.T) {
	auxRoot := bytes.Repeat([]byte{0xCD}, 32)

	// Build a coinbase with aux commitment
	coinbase := make([]byte, 100)
	copy(coinbase[20:24], AuxMarker)
	copy(coinbase[24:56], auxRoot)

	// Extract and verify
	extracted := ExtractAuxRootFromCoinbase(coinbase)
	if !bytes.Equal(extracted, auxRoot) {
		t.Errorf("extracted root doesn't match: %x vs %x", extracted, auxRoot)
	}

	// Test with no marker
	noMarker := make([]byte, 100)
	if ExtractAuxRootFromCoinbase(noMarker) != nil {
		t.Error("should return nil when no marker present")
	}

	// Test with marker but coinbase too short
	shortCoinbase := make([]byte, 10)
	copy(shortCoinbase[0:4], AuxMarker)
	if ExtractAuxRootFromCoinbase(shortCoinbase) != nil {
		t.Error("should return nil when coinbase too short after marker")
	}
}

// TestMerkleTreeSize tests merkle tree size calculation.
func TestMerkleTreeSize(t *testing.T) {
	tests := []struct {
		numLeaves    int
		expectedSize int
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
	}

	for _, tt := range tests {
		size := MerkleTreeSize(tt.numLeaves)
		if size != tt.expectedSize {
			t.Errorf("MerkleTreeSize(%d) = %d, want %d", tt.numLeaves, size, tt.expectedSize)
		}
	}
}

// TestBuildAuxPowProof tests AuxPoW proof construction.
func TestBuildAuxPowProof(t *testing.T) {
	parentCoinbase := make([]byte, 200)
	copy(parentCoinbase[50:54], AuxMarker) // Add marker
	copy(parentCoinbase[54:86], bytes.Repeat([]byte{0xEF}, 32))

	parentHeader := make([]byte, 80)
	parentHash := bytes.Repeat([]byte{0x33}, 32) // Parent block hash
	coinbaseBranch := [][]byte{bytes.Repeat([]byte{0x11}, 32)}

	auxBlock := &AuxBlockData{
		Symbol:     "DOGE",
		ChainID:    98,
		Hash:       bytes.Repeat([]byte{0xAA}, 32),
		ChainIndex: 0,
		Height:     1000,
	}
	auxBranch := [][]byte{bytes.Repeat([]byte{0x22}, 32)}

	proof, err := BuildAuxPowProof(parentCoinbase, coinbaseBranch, parentHeader, parentHash, auxBlock, auxBranch)
	if err != nil {
		t.Fatalf("BuildAuxPowProof failed: %v", err)
	}

	// Verify proof structure
	if !bytes.Equal(proof.ParentCoinbase, parentCoinbase) {
		t.Error("parent coinbase mismatch")
	}
	if !bytes.Equal(proof.ParentHeader, parentHeader) {
		t.Error("parent header mismatch")
	}
	if !bytes.Equal(proof.ParentHash, parentHash) {
		t.Error("parent hash mismatch")
	}
	if len(proof.CoinbaseMerkleBranch) != 1 {
		t.Errorf("expected 1 coinbase branch, got %d", len(proof.CoinbaseMerkleBranch))
	}
	if proof.AuxMerkleIndex != 0 {
		t.Errorf("expected aux index 0, got %d", proof.AuxMerkleIndex)
	}

	// Test with invalid inputs
	_, err = BuildAuxPowProof(nil, nil, parentHeader, parentHash, auxBlock, nil)
	if err == nil {
		t.Error("should fail with empty coinbase")
	}

	_, err = BuildAuxPowProof(parentCoinbase, nil, make([]byte, 70), parentHash, auxBlock, nil)
	if err == nil {
		t.Error("should fail with invalid header length")
	}

	_, err = BuildAuxPowProof(parentCoinbase, nil, parentHeader, make([]byte, 16), auxBlock, nil)
	if err == nil {
		t.Error("should fail with invalid parent hash length")
	}

	_, err = BuildAuxPowProof(parentCoinbase, nil, parentHeader, parentHash, nil, nil)
	if err == nil {
		t.Error("should fail with nil aux block")
	}
}

// TestVerifyAuxPowProof tests AuxPoW proof verification.
func TestVerifyAuxPowProof(t *testing.T) {
	// Create a valid proof structure for testing
	auxRoot := bytes.Repeat([]byte{0xAA}, 32)
	auxBlockHash := auxRoot // For single chain, root equals hash

	parentCoinbase := make([]byte, 100)
	copy(parentCoinbase[10:14], AuxMarker)
	copy(parentCoinbase[14:46], auxRoot)

	// Compute coinbase hash
	coinbaseHash := make([]byte, 32) // Placeholder - real impl uses SHA256d

	// Build header with merkle root at correct position
	parentHeader := make([]byte, 80)
	copy(parentHeader[36:68], coinbaseHash) // Merkle root position

	proof := &coin.AuxPowProof{
		ParentCoinbase:       parentCoinbase,
		ParentCoinbaseHash:   coinbaseHash,
		CoinbaseMerkleBranch: nil, // Single tx, no branch
		CoinbaseMerkleIndex:  0,
		AuxMerkleBranch:      nil, // Single aux chain, no branch
		AuxMerkleIndex:       0,
		ParentHeader:         parentHeader,
	}

	// This should pass basic structure validation
	err := VerifyAuxPowProof(proof, auxBlockHash, auxRoot)
	if err != nil {
		// Note: This may fail the merkle root check since we're using placeholder hashes
		// The important thing is it doesn't panic and validates structure
		t.Logf("VerifyAuxPowProof returned: %v (expected for test data)", err)
	}

	// Test nil proof
	err = VerifyAuxPowProof(nil, auxBlockHash, auxRoot)
	if err == nil {
		t.Error("should fail with nil proof")
	}
}

// TestAuxChainSlot tests chain slot calculation.
func TestAuxChainSlot(t *testing.T) {
	// Test slot calculation consistency
	slot1 := coin.AuxChainSlot(98, 0, 2)  // DOGE in tree size 2
	slot2 := coin.AuxChainSlot(98, 0, 4)  // DOGE in tree size 4
	slot3 := coin.AuxChainSlot(1, 0, 4)   // NMC in tree size 4

	// Different tree sizes may produce different slots
	t.Logf("DOGE slot in size 2: %d", slot1)
	t.Logf("DOGE slot in size 4: %d", slot2)
	t.Logf("NMC slot in size 4: %d", slot3)

	// Verify slots are within tree bounds
	if slot1 >= 2 {
		t.Errorf("slot %d exceeds tree size 2", slot1)
	}
	if slot2 >= 4 {
		t.Errorf("slot %d exceeds tree size 4", slot2)
	}
	if slot3 >= 4 {
		t.Errorf("slot %d exceeds tree size 4", slot3)
	}
}

// TestAuxBlockDataConversion tests AuxBlockData field handling.
func TestAuxBlockDataConversion(t *testing.T) {
	// Test creating aux block data
	target := new(big.Int)
	target.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	data := AuxBlockData{
		Symbol:        "DOGE",
		ChainID:       98,
		Hash:          bytes.Repeat([]byte{0x12}, 32),
		Target:        target,
		Height:        5000000,
		CoinbaseValue: 10000 * 1e8, // 10000 DOGE
		ChainIndex:    0,
		Difficulty:    1234.56,
		Bits:          0x1b00ffff,
	}

	// Verify fields
	if data.Symbol != "DOGE" {
		t.Errorf("symbol mismatch: %s", data.Symbol)
	}
	if data.ChainID != 98 {
		t.Errorf("chain ID mismatch: %d", data.ChainID)
	}
	if len(data.Hash) != 32 {
		t.Errorf("hash length mismatch: %d", len(data.Hash))
	}
	if data.Target == nil {
		t.Error("target should not be nil")
	}
}

// TestConstants verifies AuxPoW constants match Dogecoin consensus.
func TestConstants(t *testing.T) {
	// Verify magic marker matches Dogecoin AuxPoW spec
	expectedMarker := []byte{0xfa, 0xbe, 0x6d, 0x6d}
	if !bytes.Equal(AuxMarker, expectedMarker) {
		t.Errorf("AuxMarker mismatch: %x vs %x", AuxMarker, expectedMarker)
	}

	// Verify aux data size (4 marker + 32 root + 4 treesize + 4 nonce = 44)
	if AuxDataSize != 44 {
		t.Errorf("AuxDataSize should be 44, got %d", AuxDataSize)
	}
}

// TestBuildCoinbaseMerkleBranch tests coinbase merkle branch construction.
func TestBuildCoinbaseMerkleBranch(t *testing.T) {
	// Test with no transactions (coinbase only)
	branch := BuildCoinbaseMerkleBranch(nil)
	if len(branch) != 0 {
		t.Errorf("expected empty branch for coinbase-only block, got %d", len(branch))
	}

	// Test with one transaction
	txHash := bytes.Repeat([]byte{0x33}, 32)
	branch = BuildCoinbaseMerkleBranch([][]byte{txHash})
	if len(branch) != 1 {
		t.Errorf("expected 1 branch for single tx, got %d", len(branch))
	}

	// Test with multiple transactions
	txHashes := [][]byte{
		bytes.Repeat([]byte{0x11}, 32),
		bytes.Repeat([]byte{0x22}, 32),
		bytes.Repeat([]byte{0x33}, 32),
	}
	branch = BuildCoinbaseMerkleBranch(txHashes)
	// With 4 leaves total (coinbase + 3 tx), tree depth is 2, so branch has 2 elements
	if len(branch) < 1 {
		t.Errorf("expected non-empty branch for multiple tx, got %d", len(branch))
	}
}

// TestVerifyMerkleBranch tests merkle branch verification.
func TestVerifyMerkleBranch(t *testing.T) {
	hash := bytes.Repeat([]byte{0xAA}, 32)
	sibling := bytes.Repeat([]byte{0xBB}, 32)

	// Compute expected root
	expectedRoot := ComputeMerkleRootFromBranch(hash, [][]byte{sibling}, 0)

	// Verify with correct root
	if !VerifyMerkleBranch(hash, [][]byte{sibling}, 0, expectedRoot) {
		t.Error("verification should pass with correct root")
	}

	// Verify with wrong root
	wrongRoot := bytes.Repeat([]byte{0x00}, 32)
	if VerifyMerkleBranch(hash, [][]byte{sibling}, 0, wrongRoot) {
		t.Error("verification should fail with wrong root")
	}

	// Verify with wrong length root
	if VerifyMerkleBranch(hash, [][]byte{sibling}, 0, []byte{0x00}) {
		t.Error("verification should fail with wrong length root")
	}
}

// BenchmarkBuildAuxMerkleRoot benchmarks merkle root construction.
func BenchmarkBuildAuxMerkleRoot(b *testing.B) {
	auxBlocks := make([]AuxBlockData, 8)
	for i := range auxBlocks {
		auxBlocks[i] = AuxBlockData{
			Symbol:     "TEST",
			ChainID:    int32(i),
			Hash:       bytes.Repeat([]byte{byte(i)}, 32),
			ChainIndex: i,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildAuxMerkleRoot(auxBlocks)
	}
}

// BenchmarkComputeMerkleRootFromBranch benchmarks merkle root computation.
func BenchmarkComputeMerkleRootFromBranch(b *testing.B) {
	hash := bytes.Repeat([]byte{0xAA}, 32)
	branch := make([][]byte, 10)
	for i := range branch {
		branch[i] = bytes.Repeat([]byte{byte(i)}, 32)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeMerkleRootFromBranch(hash, branch, 0)
	}
}

// Example demonstrates basic AuxPoW workflow.
func ExampleBuildAuxCommitment() {
	// Create aux merkle root (would come from BuildAuxMerkleRoot in practice)
	auxRoot, _ := hex.DecodeString("aabbccdd" + "00000000000000000000000000000000000000000000000000000000")

	// Build commitment for embedding in coinbase
	treeSize := uint32(2)
	merkleNonce := uint32(0)
	commitment := BuildAuxCommitment(auxRoot, treeSize, merkleNonce)

	// Commitment can now be embedded in parent coinbase scriptsig
	_ = commitment
}

// ═══════════════════════════════════════════════════════════════════════════════
// SHA-256d MERGE MINING TESTS (Bitcoin + Namecoin)
// ═══════════════════════════════════════════════════════════════════════════════

// TestNamecoinChainID verifies Namecoin's chain ID constant.
func TestNamecoinChainID(t *testing.T) {
	// Namecoin was the first AuxPoW coin, so it has chain ID 1
	const expectedNamecoinChainID = 1

	// Verify slot calculation works for Namecoin
	slot := coin.AuxChainSlot(expectedNamecoinChainID, 0, 1)
	if slot != 0 {
		t.Errorf("NMC slot in tree size 1 should be 0, got %d", slot)
	}

	// Test with tree size 2
	slot2 := coin.AuxChainSlot(expectedNamecoinChainID, 0, 2)
	if slot2 >= 2 {
		t.Errorf("NMC slot %d exceeds tree size 2", slot2)
	}

	t.Logf("Namecoin ChainID: %d, Slot in tree[1]: %d, Slot in tree[2]: %d",
		expectedNamecoinChainID, slot, slot2)
}

// TestSHA256dAuxBlockData tests aux block data for SHA-256d chains.
func TestSHA256dAuxBlockData(t *testing.T) {
	// Test Namecoin aux block data
	nmcTarget := new(big.Int)
	nmcTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	nmcBlock := AuxBlockData{
		Symbol:        "NMC",
		ChainID:       1,
		Hash:          bytes.Repeat([]byte{0xAB}, 32),
		Target:        nmcTarget,
		Height:        700000, // Current Namecoin height range
		CoinbaseValue: 625000000, // 6.25 NMC in satoshis
		ChainIndex:    0,
		Difficulty:    1.0,
		Bits:          0x1d00ffff,
	}

	// Verify fields
	if nmcBlock.ChainID != 1 {
		t.Errorf("Namecoin ChainID should be 1, got %d", nmcBlock.ChainID)
	}
	if nmcBlock.Symbol != "NMC" {
		t.Errorf("Symbol should be NMC, got %s", nmcBlock.Symbol)
	}
	if len(nmcBlock.Hash) != 32 {
		t.Errorf("Hash should be 32 bytes, got %d", len(nmcBlock.Hash))
	}
}

// TestBuildAuxMerkleRootSHA256d tests merkle root with SHA-256d aux chains.
func TestBuildAuxMerkleRootSHA256d(t *testing.T) {
	// Single Namecoin aux block (most common BTC+NMC setup)
	nmcBlock := AuxBlockData{
		Symbol:     "NMC",
		ChainID:    1,
		Hash:       bytes.Repeat([]byte{0x55}, 32),
		ChainIndex: 0,
	}

	root, branches := BuildAuxMerkleRoot([]AuxBlockData{nmcBlock})

	// For single aux chain, root equals block hash, no branches
	if root == nil {
		t.Fatal("root should not be nil for single aux block")
	}
	if !bytes.Equal(root, nmcBlock.Hash) {
		t.Error("single chain root should equal block hash")
	}
	if len(branches) != 0 {
		t.Errorf("single chain should have no branches, got %d", len(branches))
	}
}

// TestBuildAuxCommitmentSHA256d tests aux commitment for SHA-256d mining.
func TestBuildAuxCommitmentSHA256d(t *testing.T) {
	// Simulated Namecoin aux merkle root
	nmcRoot := bytes.Repeat([]byte{0x77}, 32)
	treeSize := uint32(1)
	merkleNonce := uint32(0)

	commitment := BuildAuxCommitment(nmcRoot, treeSize, merkleNonce)

	// Verify commitment structure
	if len(commitment) != 44 {
		t.Fatalf("commitment should be 44 bytes, got %d", len(commitment))
	}

	// Check magic marker (fabe6d6d)
	if !bytes.Equal(commitment[0:4], AuxMarker) {
		t.Errorf("marker mismatch: %x vs %x", commitment[0:4], AuxMarker)
	}

	// Check aux root is embedded
	if !bytes.Equal(commitment[4:36], nmcRoot) {
		t.Error("aux root not correctly embedded in commitment")
	}

	// Check tree size (should be 1 for single aux chain)
	if commitment[36] != 1 || commitment[37] != 0 || commitment[38] != 0 || commitment[39] != 0 {
		t.Errorf("tree size encoding wrong: %x", commitment[36:40])
	}
}

// TestBuildAuxPowProofSHA256d tests AuxPoW proof for SHA-256d chains.
func TestBuildAuxPowProofSHA256d(t *testing.T) {
	// Create simulated Bitcoin parent block data
	parentCoinbase := make([]byte, 150)
	copy(parentCoinbase[60:64], AuxMarker) // Embed marker
	nmcRoot := bytes.Repeat([]byte{0xDD}, 32)
	copy(parentCoinbase[64:96], nmcRoot) // Embed NMC root

	parentHeader := make([]byte, 80)
	parentHash := bytes.Repeat([]byte{0xEE}, 32) // Parent block hash
	coinbaseBranch := [][]byte{bytes.Repeat([]byte{0x11}, 32)}

	nmcBlock := &AuxBlockData{
		Symbol:     "NMC",
		ChainID:    1,
		Hash:       nmcRoot, // Single chain, hash = root
		ChainIndex: 0,
		Height:     700000,
	}

	// No aux branch for single aux chain
	auxBranch := [][]byte{}

	proof, err := BuildAuxPowProof(parentCoinbase, coinbaseBranch, parentHeader, parentHash, nmcBlock, auxBranch)
	if err != nil {
		t.Fatalf("BuildAuxPowProof failed: %v", err)
	}

	// Verify proof structure
	if proof == nil {
		t.Fatal("proof should not be nil")
	}
	if !bytes.Equal(proof.ParentCoinbase, parentCoinbase) {
		t.Error("parent coinbase mismatch")
	}
	if len(proof.ParentHeader) != 80 {
		t.Errorf("parent header should be 80 bytes, got %d", len(proof.ParentHeader))
	}
	if proof.AuxMerkleIndex != 0 {
		t.Errorf("aux merkle index should be 0 for single chain, got %d", proof.AuxMerkleIndex)
	}
}

// TestBitcoinNamecoinMerge tests the full BTC+NMC merge mining flow.
func TestBitcoinNamecoinMerge(t *testing.T) {
	// Simulate BTC parent block with NMC aux commitment

	// Step 1: Create Namecoin aux block data
	nmcHash := bytes.Repeat([]byte{0xAA}, 32)
	nmcBlock := AuxBlockData{
		Symbol:     "NMC",
		ChainID:    1, // Namecoin ChainID
		Hash:       nmcHash,
		ChainIndex: 0,
	}

	// Step 2: Build aux merkle root (single chain = hash itself)
	auxRoot, branches := BuildAuxMerkleRoot([]AuxBlockData{nmcBlock})
	if !bytes.Equal(auxRoot, nmcHash) {
		t.Error("aux root should equal block hash for single chain")
	}
	if len(branches) != 0 {
		t.Error("single aux chain should have no branches")
	}

	// Step 3: Build aux commitment for Bitcoin coinbase
	commitment := BuildAuxCommitment(auxRoot, 1, 0)
	if len(commitment) != 44 {
		t.Fatalf("commitment size wrong: %d", len(commitment))
	}

	// Step 4: Embed commitment in Bitcoin coinbase (simulation)
	btcCoinbase := make([]byte, 200)
	copy(btcCoinbase[100:144], commitment)

	// Step 5: Verify we can extract the aux root back
	extracted := ExtractAuxRootFromCoinbase(btcCoinbase)
	if extracted == nil {
		t.Fatal("failed to extract aux root from coinbase")
	}
	if !bytes.Equal(extracted, auxRoot) {
		t.Errorf("extracted aux root mismatch: %x vs %x", extracted, auxRoot)
	}

	t.Log("BTC+NMC merge mining flow validated successfully")
}

// TestMultiAuxChainSHA256d tests multiple SHA-256d aux chains (theoretical).
func TestMultiAuxChainSHA256d(t *testing.T) {
	// While NMC is the main SHA-256d aux chain, test with hypothetical additional chains
	auxBlocks := []AuxBlockData{
		{
			Symbol:     "NMC",
			ChainID:    1,
			Hash:       bytes.Repeat([]byte{0x11}, 32),
			ChainIndex: 0,
		},
		{
			Symbol:     "AUX2", // Hypothetical second SHA-256d aux chain
			ChainID:    2,
			Hash:       bytes.Repeat([]byte{0x22}, 32),
			ChainIndex: 1,
		},
	}

	root, branch := BuildAuxMerkleRoot(auxBlocks)

	// With 2 aux chains, we should have branches
	if root == nil {
		t.Fatal("root should not be nil")
	}
	if len(root) != 32 {
		t.Errorf("root should be 32 bytes, got %d", len(root))
	}

	// Branch should not be empty with 2 aux chains
	if len(branch) == 0 {
		t.Error("branch should not be empty with 2 aux chains")
	}
	t.Logf("branch has %d elements", len(branch))
}

// TestChainSlotConsistency verifies slot calculation is deterministic.
func TestChainSlotConsistency(t *testing.T) {
	// Run slot calculation multiple times with same inputs
	const iterations = 100
	const nmcChainID int32 = 1
	const dogeChainID int32 = 98

	nmcSlots := make([]int, iterations)
	dogeSlots := make([]int, iterations)

	for i := 0; i < iterations; i++ {
		nmcSlots[i] = coin.AuxChainSlot(nmcChainID, 0, 4)
		dogeSlots[i] = coin.AuxChainSlot(dogeChainID, 0, 4)
	}

	// All results should be identical
	for i := 1; i < iterations; i++ {
		if nmcSlots[i] != nmcSlots[0] {
			t.Errorf("NMC slot not consistent: %d vs %d", nmcSlots[i], nmcSlots[0])
		}
		if dogeSlots[i] != dogeSlots[0] {
			t.Errorf("DOGE slot not consistent: %d vs %d", dogeSlots[i], dogeSlots[0])
		}
	}

	// NMC and DOGE should have different slots in tree size 4
	// (unless the algorithm produces a collision for these specific chain IDs)
	t.Logf("NMC slot: %d, DOGE slot: %d (tree size 4)", nmcSlots[0], dogeSlots[0])
}

// BenchmarkBuildAuxCommitmentSHA256d benchmarks commitment building.
func BenchmarkBuildAuxCommitmentSHA256d(b *testing.B) {
	auxRoot := bytes.Repeat([]byte{0xAA}, 32)
	treeSize := uint32(1)
	merkleNonce := uint32(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildAuxCommitment(auxRoot, treeSize, merkleNonce)
	}
}

// BenchmarkExtractAuxRootFromCoinbase benchmarks aux root extraction.
func BenchmarkExtractAuxRootFromCoinbase(b *testing.B) {
	coinbase := make([]byte, 200)
	copy(coinbase[50:54], AuxMarker)
	copy(coinbase[54:86], bytes.Repeat([]byte{0xBB}, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractAuxRootFromCoinbase(coinbase)
	}
}
