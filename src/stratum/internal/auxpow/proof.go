// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/crypto"
)

// auditDebugEnabled controls verbose audit logging output.
// Disabled by default - enable via SPIRAL_AUDIT_DEBUG=1 for debugging.
var auditDebugEnabled = os.Getenv("SPIRAL_AUDIT_DEBUG") == "1"

// debugLogAuxPowProof logs AuxPoW proof construction details for forensic analysis.
func debugLogAuxPowProof(auxSymbol string, parentCoinbase, coinbaseHash, parentHeader []byte, auxBlockHash []byte, auxMerkleIndex int, coinbaseBranchLen, auxBranchLen int) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_AUXPOW_PROOF aux_chain=%s parent_coinbase_len=%d parent_coinbase_hash=%s parent_header=%s aux_block_hash=%s aux_merkle_index=%d coinbase_branch_len=%d aux_branch_len=%d\n",
		auxSymbol,
		len(parentCoinbase),
		hex.EncodeToString(coinbaseHash),
		hex.EncodeToString(parentHeader),
		hex.EncodeToString(auxBlockHash),
		auxMerkleIndex,
		coinbaseBranchLen,
		auxBranchLen,
	)
}

// debugLogAuxCommitment logs AuxPoW commitment construction for forensic analysis.
func debugLogAuxCommitment(auxRoot []byte, treeSize, merkleNonce uint32, commitment []byte) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_AUXPOW_COMMITMENT aux_root=%s tree_size=%d merkle_nonce=%d commitment_hex=%s commitment_len=%d\n",
		hex.EncodeToString(auxRoot),
		treeSize,
		merkleNonce,
		hex.EncodeToString(commitment),
		len(commitment),
	)
}

// BuildAuxPowProof constructs the complete AuxPoW proof for aux block submission.
//
// This is called when a share meets an auxiliary chain's difficulty target.
// The proof demonstrates that the parent block's proof-of-work is valid for
// the auxiliary block.
//
// CONSENSUS-CRITICAL: The proof must exactly match what the aux chain node expects.
// Any deviation will cause the aux block to be rejected.
//
// Parameters:
//   - parentCoinbase: Full parent coinbase transaction
//   - coinbaseMerkleBranch: Merkle path from coinbase to parent merkle root
//   - parentHeader: 80-byte parent block header
//   - parentHash: Hash of the parent header (algorithm-specific: SHA256d or Scrypt)
//   - auxBlock: The aux block being proven
//   - auxMerkleBranch: Merkle path from aux block to aux merkle root
//
// Returns the AuxPoW proof structure ready for serialization.
func BuildAuxPowProof(
	parentCoinbase []byte,
	coinbaseMerkleBranch [][]byte,
	parentHeader []byte,
	parentHash []byte,
	auxBlock *AuxBlockData,
	auxMerkleBranch [][]byte,
) (*coin.AuxPowProof, error) {
	// Validate inputs
	if len(parentCoinbase) == 0 {
		return nil, fmt.Errorf("empty parent coinbase")
	}
	if len(parentHeader) != 80 {
		return nil, fmt.Errorf("invalid parent header length: %d (expected 80)", len(parentHeader))
	}
	if len(parentHash) != 32 {
		return nil, fmt.Errorf("invalid parent hash length: %d (expected 32)", len(parentHash))
	}
	if auxBlock == nil {
		return nil, fmt.Errorf("nil aux block")
	}

	// Compute coinbase hash (SHA256d of the full coinbase transaction)
	coinbaseHash := crypto.SHA256d(parentCoinbase)

	// Build proof structure
	proof := &coin.AuxPowProof{
		ParentCoinbase:       parentCoinbase,
		ParentCoinbaseHash:   coinbaseHash,
		CoinbaseMerkleBranch: coinbaseMerkleBranch,
		CoinbaseMerkleIndex:  0, // Coinbase is always at index 0
		AuxMerkleBranch:      auxMerkleBranch,
		AuxMerkleIndex:       auxBlock.ChainIndex,
		ParentHeader:         parentHeader,
		ParentHash:           parentHash, // Required for AuxPoW serialization
	}

	// AUDIT: Log AuxPoW proof construction (single-line, structured, deterministic)
	debugLogAuxPowProof(
		auxBlock.Symbol,
		parentCoinbase,
		coinbaseHash,
		parentHeader,
		auxBlock.Hash,
		auxBlock.ChainIndex,
		len(coinbaseMerkleBranch),
		len(auxMerkleBranch),
	)

	return proof, nil
}

// VerifyAuxPowProof verifies an AuxPoW proof is valid.
// This is used for testing and for validating incoming shares.
//
// Verification steps:
//  1. Verify aux merkle branch leads to aux merkle root
//  2. Verify aux merkle root is in parent coinbase (after magic marker)
//  3. Verify coinbase merkle branch leads to parent merkle root
//  4. Verify parent merkle root matches parent header
//
// CONSENSUS-CRITICAL: This must match the validation logic in aux chain nodes.
func VerifyAuxPowProof(
	proof *coin.AuxPowProof,
	auxBlockHash []byte,
	expectedAuxRoot []byte,
) error {
	if proof == nil {
		return fmt.Errorf("nil proof")
	}

	// Step 1: Verify aux merkle path (if there is a branch)
	if len(proof.AuxMerkleBranch) > 0 {
		computedAuxRoot := ComputeMerkleRootFromBranch(
			auxBlockHash,
			proof.AuxMerkleBranch,
			proof.AuxMerkleIndex,
		)
		if !bytes.Equal(computedAuxRoot, expectedAuxRoot) {
			return fmt.Errorf("aux merkle root mismatch: computed %x, expected %x",
				computedAuxRoot, expectedAuxRoot)
		}
	} else {
		// No branch means single aux chain - aux root should equal block hash
		if !bytes.Equal(auxBlockHash, expectedAuxRoot) {
			return fmt.Errorf("aux block hash doesn't match expected root (single chain)")
		}
	}

	// Step 2: Verify aux root is in coinbase after magic marker
	markerPos := bytes.Index(proof.ParentCoinbase, AuxMarker)
	if markerPos == -1 {
		return fmt.Errorf("aux marker (0xfabe6d6d) not found in coinbase")
	}

	// Aux root follows marker immediately (stored in big-endian in coinbase)
	rootStart := markerPos + len(AuxMarker)
	if rootStart+32 > len(proof.ParentCoinbase) {
		return fmt.Errorf("coinbase too short for aux root after marker")
	}
	coinbaseAuxRoot := proof.ParentCoinbase[rootStart : rootStart+32]

	// Reverse expectedAuxRoot to big-endian for comparison (coinbase stores big-endian)
	expectedAuxRootBE := make([]byte, 32)
	for i := 0; i < 32; i++ {
		expectedAuxRootBE[i] = expectedAuxRoot[31-i]
	}
	if !bytes.Equal(coinbaseAuxRoot, expectedAuxRootBE) {
		return fmt.Errorf("aux root in coinbase doesn't match: got %x, expected %x",
			coinbaseAuxRoot, expectedAuxRootBE)
	}

	// Step 3: Verify coinbase merkle path
	if len(proof.CoinbaseMerkleBranch) > 0 {
		computedMerkleRoot := ComputeMerkleRootFromBranch(
			proof.ParentCoinbaseHash,
			proof.CoinbaseMerkleBranch,
			proof.CoinbaseMerkleIndex,
		)

		// Step 4: Verify merkle root is in parent header
		// Merkle root is at bytes 36-68 of the 80-byte header
		headerMerkleRoot := proof.ParentHeader[36:68]
		if !bytes.Equal(computedMerkleRoot, headerMerkleRoot) {
			return fmt.Errorf("merkle root mismatch in parent header: computed %x, header has %x",
				computedMerkleRoot, headerMerkleRoot)
		}
	} else {
		// No branch means coinbase is the only transaction
		// Coinbase hash should equal merkle root in header
		headerMerkleRoot := proof.ParentHeader[36:68]
		if !bytes.Equal(proof.ParentCoinbaseHash, headerMerkleRoot) {
			return fmt.Errorf("coinbase hash doesn't match header merkle root (single tx)")
		}
	}

	return nil
}

// BuildAuxCommitment creates the aux commitment data for embedding in coinbase.
//
// The commitment format is:
//   - Magic marker: 4 bytes (0xfabe6d6d)
//   - Aux merkle root: 32 bytes (BIG-ENDIAN / display order)
//   - Tree size: 4 bytes (uint32 LE, number of leaves in aux tree)
//   - Merkle nonce: 4 bytes (uint32 LE, used for chain slot calculation)
//
// Total: 44 bytes
//
// CONSENSUS-CRITICAL: This format must match what aux chains expect.
// The aux merkle root must be in big-endian (display order) because aux chain
// daemons compare it against the block hash they receive in submitauxblock,
// which is also in big-endian (RPC format).
func BuildAuxCommitment(auxRoot []byte, treeSize uint32, merkleNonce uint32) []byte {
	if len(auxRoot) != 32 {
		return nil
	}

	commitment := make([]byte, AuxDataSize)

	// Magic marker
	copy(commitment[0:4], AuxMarker)

	// Aux merkle root - reverse from little-endian (internal) to big-endian (display)
	// This is required because submitauxblock takes the hash in big-endian,
	// and the daemon compares it against what's embedded in the coinbase.
	for i := 0; i < 32; i++ {
		commitment[4+i] = auxRoot[31-i]
	}

	// Tree size (little-endian)
	commitment[36] = byte(treeSize)
	commitment[37] = byte(treeSize >> 8)
	commitment[38] = byte(treeSize >> 16)
	commitment[39] = byte(treeSize >> 24)

	// Merkle nonce (little-endian)
	commitment[40] = byte(merkleNonce)
	commitment[41] = byte(merkleNonce >> 8)
	commitment[42] = byte(merkleNonce >> 16)
	commitment[43] = byte(merkleNonce >> 24)

	// AUDIT: Log AuxPoW commitment construction (single-line, structured, deterministic)
	debugLogAuxCommitment(auxRoot, treeSize, merkleNonce, commitment)

	return commitment
}

// ExtractAuxRootFromCoinbase extracts the aux merkle root from a coinbase.
//
// Searches for the magic marker and extracts the 32-byte root that follows.
// Returns the root in little-endian (internal format), reversing from
// the big-endian format stored in the coinbase.
// Returns nil if the marker is not found or coinbase is too short.
func ExtractAuxRootFromCoinbase(coinbase []byte) []byte {
	markerPos := bytes.Index(coinbase, AuxMarker)
	if markerPos == -1 {
		return nil
	}

	rootStart := markerPos + len(AuxMarker)
	if rootStart+32 > len(coinbase) {
		return nil
	}

	// Coinbase stores aux root in big-endian, reverse to little-endian (internal)
	root := make([]byte, 32)
	for i := 0; i < 32; i++ {
		root[i] = coinbase[rootStart+31-i]
	}
	return root
}
