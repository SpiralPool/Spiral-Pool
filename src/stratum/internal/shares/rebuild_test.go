// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// validJob returns a minimal but structurally valid job for block construction.
// Uses the same fixture pattern as TestBuildFullBlock in validator_test.go.
func validJob() *protocol.Job {
	return &protocol.Job{
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		TransactionData: []string{},
	}
}

// validShare returns a minimal but structurally valid share.
func validShare() *protocol.Share {
	return &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		NTime:       "64000000",
		Nonce:       "12345678",
	}
}

// TestRebuildBlockHex_Success verifies that RebuildBlockHex produces a valid
// block hex string from a valid job and share, matching what buildFullBlock
// would produce via the normal path.
func TestRebuildBlockHex_Success(t *testing.T) {
	job := validJob()
	share := validShare()

	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed with valid inputs: %v", err)
	}

	if blockHex == "" {
		t.Fatal("RebuildBlockHex returned empty string with valid inputs")
	}

	// Verify it decodes to valid hex
	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("RebuildBlockHex produced invalid hex: %v", err)
	}

	// Must have at least 80-byte header + 1-byte varint + coinbase
	if len(block) <= 81 {
		t.Errorf("Block too short: %d bytes", len(block))
	}

	// First 80 bytes are the header
	// Transaction count varint should be 0x01 (1 tx = coinbase only)
	if block[80] != 0x01 {
		t.Errorf("Transaction count = 0x%02x, want 0x01", block[80])
	}
}

// TestRebuildBlockHex_MatchesBuildFullBlock verifies that the recovery path
// produces identical output to the normal build path.
func TestRebuildBlockHex_MatchesBuildFullBlock(t *testing.T) {
	job := validJob()
	share := validShare()

	// Normal path: buildBlockHeader + buildFullBlock
	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}
	normalHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	// Recovery path: RebuildBlockHex
	recoveryHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	if normalHex != recoveryHex {
		t.Errorf("Recovery path produced different hex.\nNormal:   %s...\nRecovery: %s...",
			normalHex[:40], recoveryHex[:40])
	}
}

// TestRebuildBlockHex_WithTransactions verifies recovery with non-empty tx data.
func TestRebuildBlockHex_WithTransactions(t *testing.T) {
	txHex := "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00000000" +
		"00" +
		"ffffffff" +
		"01" +
		"0000000000000000" +
		"00" +
		"00000000"

	job := validJob()
	job.TransactionData = []string{txHex, txHex}
	share := validShare()

	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex with txs failed: %v", err)
	}

	block, _ := hex.DecodeString(blockHex)

	// Transaction count should be 3 (coinbase + 2 txs)
	if block[80] != 0x03 {
		t.Errorf("Transaction count = 0x%02x, want 0x03", block[80])
	}
}

// TestRebuildBlockHex_CorruptCoinbase1 verifies failure with invalid coinbase1 hex.
// CoinBase1 is used during merkle root computation in buildBlockHeader,
// so corrupt coinbase1 fails at header reconstruction, not block assembly.
func TestRebuildBlockHex_CorruptCoinbase1(t *testing.T) {
	job := validJob()
	job.CoinBase1 = "ZZZZ_NOT_HEX"
	share := validShare()

	_, err := RebuildBlockHex(job, share)
	if err == nil {
		t.Fatal("Expected error for corrupt coinbase1, got nil")
	}
	if !strings.Contains(err.Error(), "header reconstruction failed") {
		t.Errorf("Expected 'header reconstruction failed' in error, got: %v", err)
	}
}

// TestRebuildBlockHex_CorruptExtraNonce1 verifies failure with invalid extranonce1.
func TestRebuildBlockHex_CorruptExtraNonce1(t *testing.T) {
	job := validJob()
	share := validShare()
	share.ExtraNonce1 = "NOT_VALID_HEX"

	_, err := RebuildBlockHex(job, share)
	if err == nil {
		t.Fatal("Expected error for corrupt extranonce1, got nil")
	}
}

// TestRebuildBlockHex_CorruptVersion verifies failure with invalid version string.
// This fails in buildBlockHeader (header reconstruction), not buildFullBlock.
func TestRebuildBlockHex_CorruptVersion(t *testing.T) {
	job := validJob()
	job.Version = "ZZZZ" // Not valid hex, and wrong length
	share := validShare()

	_, err := RebuildBlockHex(job, share)
	if err == nil {
		t.Fatal("Expected error for corrupt version, got nil")
	}
	if !strings.Contains(err.Error(), "header reconstruction failed") {
		t.Errorf("Expected 'header reconstruction failed' in error, got: %v", err)
	}
}

// TestRebuildBlockHex_CorruptPrevHash verifies failure with short prevhash.
func TestRebuildBlockHex_CorruptPrevHash(t *testing.T) {
	job := validJob()
	job.PrevBlockHash = "abcd" // Too short (need 64 hex chars = 32 bytes)
	share := validShare()

	_, err := RebuildBlockHex(job, share)
	if err == nil {
		t.Fatal("Expected error for short prevhash, got nil")
	}
	if !strings.Contains(err.Error(), "header reconstruction failed") {
		t.Errorf("Expected 'header reconstruction failed' in error, got: %v", err)
	}
}

// TestRebuildBlockHex_CorruptTransactionData verifies failure with invalid tx hex.
func TestRebuildBlockHex_CorruptTransactionData(t *testing.T) {
	job := validJob()
	job.TransactionData = []string{"ZZZZ_NOT_HEX"}
	share := validShare()

	_, err := RebuildBlockHex(job, share)
	if err == nil {
		t.Fatal("Expected error for corrupt transaction data, got nil")
	}
	if !strings.Contains(err.Error(), "block assembly failed") {
		t.Errorf("Expected 'block assembly failed' in error, got: %v", err)
	}
}

// TestRebuildBlockHex_EmptyCoinbase verifies behavior with empty coinbase fields.
func TestRebuildBlockHex_EmptyCoinbase(t *testing.T) {
	job := validJob()
	job.CoinBase1 = ""
	job.CoinBase2 = ""
	share := validShare()

	// Empty hex strings decode to empty byte slices — this should still produce
	// a block (header + varint + extranonce1 + extranonce2 as the "coinbase").
	// The block is technically malformed but RebuildBlockHex should not error on it.
	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed with empty coinbase: %v", err)
	}
	if blockHex == "" {
		t.Fatal("Expected non-empty hex even with empty coinbase")
	}
}
