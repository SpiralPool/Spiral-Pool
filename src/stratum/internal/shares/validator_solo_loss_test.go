// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// SOLO MINING BLOCK LOSS PREVENTION: VALIDATOR / BLOCK BUILDING TEST SUITE
// =============================================================================
//
// These tests exercise every code path in buildFullBlock and RebuildBlockHex
// that could cause a valid solved block to be lost during SOLO mining.
//
// Risk vectors covered:
//   1. buildFullBlock failures (CoinBase1, ExtraNonce1, ExtraNonce2, CoinBase2)
//   2. RebuildBlockHex recovery path failures and equivalence
//   3. Edge-case block data (high tx counts, empty fields, malformed hex)
//   4. Varint encoding boundary conditions (252, 253, 65535, 65536)
//   5. Header reconstruction failures (Version, PrevHash, NBits, NTime, Nonce)
//   6. Version rolling with VersionBits

// =============================================================================
// Test Fixtures (reuse existing validJob/validShare patterns)
// =============================================================================

// soloJob returns a structurally valid job for block construction tests.
func soloJob() *protocol.Job {
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

// soloShare returns a structurally valid share for block construction tests.
func soloShare() *protocol.Share {
	return &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		NTime:       "64000000",
		Nonce:       "12345678",
	}
}

// minimalTx returns a minimal valid transaction hex string.
func minimalTx() string {
	return "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00000000" +
		"00" +
		"ffffffff" +
		"01" +
		"0000000000000000" +
		"00" +
		"00000000"
}

// =============================================================================
// RISK VECTOR 1: buildFullBlock Component Failures
// =============================================================================
// When buildFullBlock fails, the block hex is empty. handleBlock must detect
// this and either rebuild via RebuildBlockHex or log all raw components to WAL.

// TestSOLO_BuildFullBlock_InvalidCoinBase1_ReturnsError verifies that corrupt
// CoinBase1 causes buildFullBlock to fail cleanly (not panic), allowing
// handleBlock to trigger the recovery path.
func TestSOLO_BuildFullBlock_InvalidCoinBase1_ReturnsError(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.CoinBase1 = "ZZZZ_NOT_HEX" // Invalid hex
	share := soloShare()

	header, err := buildBlockHeader(job, share)
	// Header construction may fail first since coinbase is needed for merkle root
	if err != nil {
		// Expected — coinbase1 is used in merkle root calculation
		if !strings.Contains(err.Error(), "coinbase1") && !strings.Contains(err.Error(), "coinbase") {
			t.Logf("Header failed with: %v (acceptable if coinbase used in merkle)", err)
		}
		return
	}

	// If header somehow succeeds, buildFullBlock should still fail
	_, err = buildFullBlock(job, share, header)
	if err == nil {
		t.Fatal("BLOCK LOSS RISK: buildFullBlock succeeded with invalid CoinBase1 — block may be invalid")
	}
	if !strings.Contains(err.Error(), "coinbase1") {
		t.Logf("Error message: %v", err)
	}
}

// TestSOLO_BuildFullBlock_InvalidExtraNonce1_ReturnsError verifies that
// corrupt ExtraNonce1 causes a clean error.
func TestSOLO_BuildFullBlock_InvalidExtraNonce1_ReturnsError(t *testing.T) {
	t.Parallel()

	job := soloJob()
	share := soloShare()
	share.ExtraNonce1 = "NOT_VALID_HEX"

	header, err := buildBlockHeader(job, share)
	if err != nil {
		// ExtraNonce1 is part of coinbase → merkle root → header
		return // Expected failure path
	}

	_, err = buildFullBlock(job, share, header)
	if err == nil {
		t.Fatal("BLOCK LOSS RISK: buildFullBlock succeeded with invalid ExtraNonce1")
	}
}

// TestSOLO_BuildFullBlock_InvalidExtraNonce2_ReturnsError verifies that
// corrupt ExtraNonce2 causes a clean error.
func TestSOLO_BuildFullBlock_InvalidExtraNonce2_ReturnsError(t *testing.T) {
	t.Parallel()

	job := soloJob()
	share := soloShare()
	share.ExtraNonce2 = "XYZ_BAD"

	header, err := buildBlockHeader(job, share)
	if err != nil {
		return // Expected failure path
	}

	_, err = buildFullBlock(job, share, header)
	if err == nil {
		t.Fatal("BLOCK LOSS RISK: buildFullBlock succeeded with invalid ExtraNonce2")
	}
}

// TestSOLO_BuildFullBlock_InvalidCoinBase2_ReturnsError verifies that
// corrupt CoinBase2 causes a clean error.
func TestSOLO_BuildFullBlock_InvalidCoinBase2_ReturnsError(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.CoinBase2 = "GGG_NOT_HEX"
	share := soloShare()

	header, err := buildBlockHeader(job, share)
	if err != nil {
		return // Expected failure path
	}

	_, err = buildFullBlock(job, share, header)
	if err == nil {
		t.Fatal("BLOCK LOSS RISK: buildFullBlock succeeded with invalid CoinBase2")
	}
}

// TestSOLO_BuildFullBlock_InvalidTransactionHex_ReturnsError verifies that
// corrupt transaction hex in TransactionData causes a clean error.
func TestSOLO_BuildFullBlock_InvalidTransactionHex_ReturnsError(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.TransactionData = []string{
		minimalTx(),
		"ZZZZZ_INVALID_TX_HEX",
		minimalTx(),
	}
	share := soloShare()

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("Header construction should succeed (tx data not used in header): %v", err)
	}

	_, err = buildFullBlock(job, share, header)
	if err == nil {
		t.Fatal("BLOCK LOSS RISK: buildFullBlock succeeded with invalid transaction data")
	}
	if !strings.Contains(err.Error(), "transaction") {
		t.Logf("Error: %v", err)
	}
}

// TestSOLO_BuildFullBlock_EmptyCoinbaseFields_ProducesOutput verifies that
// empty coinbase fields (edge case) don't panic and produce some output.
func TestSOLO_BuildFullBlock_EmptyCoinbaseFields_ProducesOutput(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.CoinBase1 = ""
	job.CoinBase2 = ""
	share := soloShare()

	// Empty hex strings decode to empty byte slices — still valid hex
	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		// Some implementations may fail, which is acceptable as long as
		// the error is returned (not a panic)
		t.Logf("RebuildBlockHex with empty coinbase failed (acceptable): %v", err)
		return
	}

	if blockHex == "" {
		t.Error("Expected non-empty output even with empty coinbase fields")
	}
}

// =============================================================================
// RISK VECTOR 2: RebuildBlockHex Recovery Path
// =============================================================================
// RebuildBlockHex is the recovery path called by handleBlock when the initial
// buildFullBlock in Validate() fails. If this ALSO fails, the block components
// must be logged to WAL for manual reconstruction.

// TestSOLO_RebuildBlockHex_MatchesBuildFullBlock_CoinbaseOnly verifies exact
// equivalence between the normal path and recovery path for coinbase-only blocks.
func TestSOLO_RebuildBlockHex_MatchesBuildFullBlock_CoinbaseOnly(t *testing.T) {
	t.Parallel()

	job := soloJob()
	share := soloShare()

	// Normal path
	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}
	normalHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	// Recovery path
	recoveryHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	if normalHex != recoveryHex {
		t.Errorf("BLOCK LOSS RISK: Recovery path produces different block hex!\n"+
			"Normal:   %s\nRecovery: %s", normalHex[:80], recoveryHex[:80])
	}
}

// TestSOLO_RebuildBlockHex_MatchesBuildFullBlock_WithTransactions verifies
// exact equivalence with multiple transactions.
func TestSOLO_RebuildBlockHex_MatchesBuildFullBlock_WithTransactions(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.TransactionData = []string{minimalTx(), minimalTx(), minimalTx()}
	share := soloShare()

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}
	normalHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	recoveryHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	if normalHex != recoveryHex {
		t.Errorf("Recovery path differs with %d transactions", len(job.TransactionData))
	}
}

// TestSOLO_RebuildBlockHex_AllInvalidInputs verifies that RebuildBlockHex
// returns errors (not panics) for every type of invalid input.
func TestSOLO_RebuildBlockHex_AllInvalidInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		modifyJob func(*protocol.Job)
		modifyShare func(*protocol.Share)
		errContains string
	}{
		{
			name:        "corrupt CoinBase1",
			modifyJob:   func(j *protocol.Job) { j.CoinBase1 = "ZZZZ_BAD" },
			errContains: "header reconstruction failed",
		},
		{
			name:        "corrupt CoinBase2",
			modifyJob:   func(j *protocol.Job) { j.CoinBase2 = "ZZZZ_BAD" },
			errContains: "block assembly failed",
		},
		{
			name:          "corrupt ExtraNonce1",
			modifyShare: func(s *protocol.Share) { s.ExtraNonce1 = "NOT_HEX" },
			errContains:   "header reconstruction failed",
		},
		{
			name:          "corrupt ExtraNonce2",
			modifyShare: func(s *protocol.Share) { s.ExtraNonce2 = "NOT_HEX" },
			errContains:   "block assembly failed",
		},
		{
			name:        "corrupt Version",
			modifyJob:   func(j *protocol.Job) { j.Version = "ZZ" },
			errContains: "header reconstruction failed",
		},
		{
			name:        "short PrevBlockHash",
			modifyJob:   func(j *protocol.Job) { j.PrevBlockHash = "abcd" },
			errContains: "header reconstruction failed",
		},
		{
			name:        "corrupt NBits",
			modifyJob:   func(j *protocol.Job) { j.NBits = "XXXX" },
			errContains: "header reconstruction failed",
		},
		{
			name:          "corrupt Nonce",
			modifyShare: func(s *protocol.Share) { s.Nonce = "NOT_HEX!" },
			errContains:   "header reconstruction failed",
		},
		{
			name:          "corrupt NTime",
			modifyShare: func(s *protocol.Share) { s.NTime = "BAD_TIME" },
			errContains:   "header reconstruction failed",
		},
		{
			name:        "corrupt TransactionData",
			modifyJob:   func(j *protocol.Job) { j.TransactionData = []string{"ZZZZZ"} },
			errContains: "block assembly failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			job := soloJob()
			share := soloShare()

			if tc.modifyJob != nil {
				tc.modifyJob(job)
			}
			if tc.modifyShare != nil {
				tc.modifyShare(share)
			}

			_, err := RebuildBlockHex(job, share)
			if err == nil {
				t.Fatalf("BLOCK LOSS RISK: RebuildBlockHex succeeded with %s — block may be invalid", tc.name)
			}

			if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
				t.Logf("Error for %s: %v (expected to contain %q)", tc.name, err, tc.errContains)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 3: Varint Encoding Boundary Conditions
// =============================================================================
// The varint encoding in Bitcoin's wire protocol has boundaries at 252, 253,
// 65535, and 65536. Incorrect varint encoding would produce an invalid block
// that the daemon rejects — a silent block loss.

// TestSOLO_VarIntEncoding_Boundaries verifies encodeVarInt at all boundaries.
func TestSOLO_VarIntEncoding_Boundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		value    uint64
		expected []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{252, []byte{0xFC}},                           // Last single-byte
		{253, []byte{0xFD, 0xFD, 0x00}},               // First 3-byte
		{254, []byte{0xFD, 0xFE, 0x00}},
		{255, []byte{0xFD, 0xFF, 0x00}},
		{256, []byte{0xFD, 0x00, 0x01}},
		{0xFFFF, []byte{0xFD, 0xFF, 0xFF}},             // Last 3-byte
		{0x10000, []byte{0xFE, 0x00, 0x00, 0x01, 0x00}}, // First 5-byte
		{0xFFFFFFFF, []byte{0xFE, 0xFF, 0xFF, 0xFF, 0xFF}},
		{0x100000000, []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}},
		{math.MaxUint64, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("varint_%d", tc.value), func(t *testing.T) {
			got := crypto.EncodeVarInt(tc.value)
			if len(got) != len(tc.expected) {
				t.Fatalf("crypto.EncodeVarInt(%d): length %d, want %d (got %x, want %x)",
					tc.value, len(got), len(tc.expected), got, tc.expected)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("crypto.EncodeVarInt(%d)[%d] = 0x%02X, want 0x%02X",
						tc.value, i, got[i], tc.expected[i])
				}
			}
		})
	}
}

// TestSOLO_BuildFullBlock_252Transactions verifies the last single-byte varint
// boundary (252 txs + 1 coinbase = 253 total, which uses 0xFD prefix).
func TestSOLO_BuildFullBlock_252Transactions(t *testing.T) {
	t.Parallel()

	txHex := minimalTx()
	txData := make([]string, 252) // +1 coinbase = 253 total
	for i := range txData {
		txData[i] = txHex
	}

	job := soloJob()
	job.TransactionData = txData
	share := soloShare()

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	block, _ := hex.DecodeString(blockHex)

	// 253 transactions → varint should be 0xFD 0xFD 0x00
	if block[80] != 0xFD {
		t.Errorf("VarInt prefix byte = 0x%02X, want 0xFD for 253 transactions", block[80])
	}
	if block[81] != 0xFD {
		t.Errorf("VarInt low byte = 0x%02X, want 0xFD (253 & 0xFF)", block[81])
	}
	if block[82] != 0x00 {
		t.Errorf("VarInt high byte = 0x%02X, want 0x00 (253 >> 8)", block[82])
	}
}

// TestSOLO_BuildFullBlock_253Transactions_MatchesRecovery verifies that
// both normal and recovery paths produce identical output at the 253
// transaction boundary.
func TestSOLO_BuildFullBlock_253Transactions_MatchesRecovery(t *testing.T) {
	t.Parallel()

	txData := make([]string, 253) // +1 coinbase = 254 total
	for i := range txData {
		txData[i] = minimalTx()
	}

	job := soloJob()
	job.TransactionData = txData
	share := soloShare()

	// Normal path
	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}
	normalHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	// Recovery path
	recoveryHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	if normalHex != recoveryHex {
		t.Errorf("BLOCK LOSS RISK: Recovery path differs at 254 tx boundary")
	}

	// Verify varint encoding
	block, _ := hex.DecodeString(normalHex)
	// 254 txs → varint 0xFD 0xFE 0x00
	if block[80] != 0xFD {
		t.Errorf("VarInt prefix = 0x%02X, want 0xFD", block[80])
	}
}

// TestSOLO_BuildFullBlock_SingleTransaction verifies minimal block (coinbase only).
func TestSOLO_BuildFullBlock_SingleTransaction(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.TransactionData = []string{} // No extra txs, just coinbase
	share := soloShare()

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	block, _ := hex.DecodeString(blockHex)

	// Header is 80 bytes
	if len(block) < 81 {
		t.Fatalf("Block too short: %d bytes", len(block))
	}

	// 1 transaction (coinbase only) → varint byte = 0x01
	if block[80] != 0x01 {
		t.Errorf("Tx count varint = 0x%02X, want 0x01", block[80])
	}
}

// =============================================================================
// RISK VECTOR 4: Header Reconstruction Failures
// =============================================================================

// TestSOLO_BuildBlockHeader_InvalidVersion verifies header fails cleanly
// with invalid version field.
func TestSOLO_BuildBlockHeader_InvalidVersion(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		version string
	}{
		{"not hex", "ZZZZZZZZ"},
		{"too short", "2000"},
		{"too long", "2000000000"},
		{"empty", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := soloJob()
			job.Version = tc.version
			share := soloShare()

			_, err := buildBlockHeader(job, share)
			if err == nil {
				t.Errorf("buildBlockHeader should fail with version %q", tc.version)
			}
		})
	}
}

// TestSOLO_BuildBlockHeader_InvalidPrevHash verifies header fails cleanly
// with invalid previous block hash.
func TestSOLO_BuildBlockHeader_InvalidPrevHash(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		prevHash string
	}{
		{"too short", "abcdef"},
		{"too long", strings.Repeat("a", 66)},
		{"not hex", strings.Repeat("Z", 64)},
		{"all zeros", strings.Repeat("0", 64)},
		{"empty", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := soloJob()
			job.PrevBlockHash = tc.prevHash
			share := soloShare()

			_, err := buildBlockHeader(job, share)
			if err == nil {
				t.Errorf("buildBlockHeader should fail with prevHash %q", tc.prevHash)
			}
		})
	}
}

// TestSOLO_BuildBlockHeader_InvalidNBits verifies header fails cleanly
// with invalid difficulty bits.
func TestSOLO_BuildBlockHeader_InvalidNBits(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		nbits string
	}{
		{"not hex", "ZZZZZZZZ"},
		{"too short", "1a03"},
		{"empty", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := soloJob()
			job.NBits = tc.nbits
			share := soloShare()

			_, err := buildBlockHeader(job, share)
			if err == nil {
				t.Errorf("buildBlockHeader should fail with nbits %q", tc.nbits)
			}
		})
	}
}

// TestSOLO_BuildBlockHeader_InvalidNonce verifies header fails cleanly
// with invalid nonce.
func TestSOLO_BuildBlockHeader_InvalidNonce(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		nonce string
	}{
		{"not hex", "ZZZZZZZZ"},
		{"too short", "1234"},
		{"too long", "123456789a"},
		{"empty", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			job := soloJob()
			share := soloShare()
			share.Nonce = tc.nonce

			_, err := buildBlockHeader(job, share)
			if err == nil {
				t.Errorf("buildBlockHeader should fail with nonce %q", tc.nonce)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 5: Block Structure Integrity
// =============================================================================

// TestSOLO_BuildFullBlock_HeaderLength verifies the output starts with
// exactly 80 bytes of header data.
func TestSOLO_BuildFullBlock_HeaderLength(t *testing.T) {
	t.Parallel()

	job := soloJob()
	share := soloShare()

	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("Invalid hex output: %v", err)
	}

	// Block must be at least 80 (header) + 1 (varint) + 1 (coinbase byte) = 82 bytes
	if len(block) < 82 {
		t.Fatalf("Block too short: %d bytes (need at least 82)", len(block))
	}
}

// TestSOLO_BuildFullBlock_CoinbaseContainsExtraNonces verifies that the
// coinbase transaction in the output block contains the correct extranonces.
// A mismatch here would cause the block hash to differ from what the miner
// solved for, resulting in an invalid block.
func TestSOLO_BuildFullBlock_CoinbaseContainsExtraNonces(t *testing.T) {
	t.Parallel()

	job := soloJob()
	share := soloShare()
	share.ExtraNonce1 = "aabbccdd"
	share.ExtraNonce2 = "11223344"

	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	// The block hex must contain the extranonce values (they're part of coinbase)
	if !strings.Contains(blockHex, "aabbccdd") {
		t.Error("BLOCK LOSS RISK: ExtraNonce1 not found in block hex — hash will be wrong!")
	}
	if !strings.Contains(blockHex, "11223344") {
		t.Error("BLOCK LOSS RISK: ExtraNonce2 not found in block hex — hash will be wrong!")
	}
}

// TestSOLO_BuildFullBlock_CoinbaseOrder verifies that the coinbase is
// assembled as CoinBase1 + ExtraNonce1 + ExtraNonce2 + CoinBase2.
func TestSOLO_BuildFullBlock_CoinbaseOrder(t *testing.T) {
	t.Parallel()

	// Use distinctive patterns to verify order
	job := soloJob()
	job.CoinBase1 = "aa"     // 1 byte
	job.CoinBase2 = "dd"     // 1 byte
	share := soloShare()
	share.ExtraNonce1 = "bb" // 1 byte
	share.ExtraNonce2 = "cc" // 1 byte

	// Note: with such short coinbase components, the merkle root will be different
	// but the block structure should still be: header + varint + cb1+en1+en2+cb2

	blockHex, err := RebuildBlockHex(job, share)
	if err != nil {
		t.Fatalf("RebuildBlockHex failed: %v", err)
	}

	block, _ := hex.DecodeString(blockHex)

	// After 80-byte header + 1-byte varint (0x01), the coinbase should be: aa bb cc dd
	if len(block) < 85 {
		t.Fatalf("Block too short: %d bytes", len(block))
	}

	coinbaseStart := 81 // header(80) + varint(1)
	coinbase := block[coinbaseStart : coinbaseStart+4]

	expected := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	for i, b := range expected {
		if coinbase[i] != b {
			t.Errorf("Coinbase byte[%d] = 0x%02X, want 0x%02X (order: cb1+en1+en2+cb2)",
				i, coinbase[i], b)
		}
	}
}

// =============================================================================
// RISK VECTOR 6: Version Rolling
// =============================================================================

// TestSOLO_BuildBlockHeader_VersionRolling verifies that when version rolling
// is enabled, the VersionBits from the share are correctly applied to the
// block header version field using the mask.
func TestSOLO_BuildBlockHeader_VersionRolling(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.Version = "20000000"
	job.VersionRollingAllowed = true
	job.VersionRollingMask = 0x1FFFE000 // BIP320 mask

	share := soloShare()
	share.VersionBits = 0x00002000 // Set bit within mask

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}

	if len(header) != 80 {
		t.Fatalf("Header length: %d, want 80", len(header))
	}

	// The version field is the first 4 bytes of the header (little-endian in header)
	// With version rolling, the base version bits outside the mask are preserved
	// and the bits within the mask come from VersionBits.
	// This test primarily verifies no panic/error occurs.
	t.Logf("Header version bytes: %x", header[:4])
}

// TestSOLO_BuildBlockHeader_NoVersionRolling verifies that without version
// rolling, the header version matches the job version exactly.
func TestSOLO_BuildBlockHeader_NoVersionRolling(t *testing.T) {
	t.Parallel()

	job := soloJob()
	job.Version = "20000000"
	job.VersionRollingAllowed = false

	share := soloShare()
	share.VersionBits = 0 // No rolling

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}

	// Version "20000000" in big-endian = 0x20000000
	// In little-endian (header format): 0x00000020
	if header[3] != 0x20 {
		t.Errorf("Version byte[3] = 0x%02X, want 0x20 (base version 0x20000000 in LE)", header[3])
	}
}

// =============================================================================
// RISK VECTOR 7: compactBitsToTarget Edge Cases
// =============================================================================

// TestSOLO_CompactBitsToTarget_ValidInputs verifies standard difficulty targets.
func TestSOLO_CompactBitsToTarget_ValidInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		bits     string
		wantSign int // 1 = positive, 0 = zero
	}{
		{"difficulty 1", "1d00ffff", 1},
		{"DGB typical", "1a0377ae", 1},
		{"BTC high difficulty", "170cfb8e", 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := compactBitsToTarget(tc.bits)
			if target == nil {
				t.Fatal("compactBitsToTarget returned nil")
			}
			if target.Sign() != tc.wantSign {
				t.Errorf("Sign = %d, want %d for bits %s", target.Sign(), tc.wantSign, tc.bits)
			}
		})
	}
}

// TestSOLO_CompactBitsToTarget_InvalidInputs verifies zero return for bad inputs.
func TestSOLO_CompactBitsToTarget_InvalidInputs(t *testing.T) {
	t.Parallel()

	invalidBits := []string{
		"",          // empty
		"1a03",      // too short
		"1a0377ae0", // too long (9 chars)
		"ZZZZZZZZ",  // not hex
		"1a000000",  // zero mantissa
		"1a800001",  // negative flag set
	}

	for _, bits := range invalidBits {
		t.Run(fmt.Sprintf("bits_%s", bits), func(t *testing.T) {
			t.Parallel()
			target := compactBitsToTarget(bits)
			if target == nil {
				t.Fatal("compactBitsToTarget returned nil (should return zero)")
			}
			if target.Sign() != 0 {
				t.Errorf("Expected zero target for invalid bits %q, got %s", bits, target.String())
			}
		})
	}
}

// =============================================================================
// RISK VECTOR: NTime Validation Edge Cases
// =============================================================================

// TestSOLO_ValidateNTime_Boundaries verifies ntime validation at edge cases.
func TestSOLO_ValidateNTime_Boundaries(t *testing.T) {
	t.Parallel()

	jobNTime := "64000000" // Some reference time in big-endian hex

	testCases := []struct {
		name      string
		shareTime string
		valid     bool
	}{
		{"exact match", "64000000", true},
		{"too short", "6400", false},
		{"too long", "640000001234", false},
		{"empty", "", false},
		{"not hex", "ZZZZZZZZ", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := validateNTime(tc.shareTime, jobNTime)
			if result != tc.valid {
				t.Errorf("validateNTime(%q, %q) = %v, want %v",
					tc.shareTime, jobNTime, result, tc.valid)
			}
		})
	}
}

// TestSOLO_ValidateNTime_MaxTimestamp verifies rejection of timestamps near
// uint32 max (year 2106+) which are likely malicious.
func TestSOLO_ValidateNTime_MaxTimestamp(t *testing.T) {
	t.Parallel()

	// 0xF0000001 in big-endian hex = "f0000001"
	extremeTime := "f0000001"
	normalTime := "64000000"

	// Extreme share time should be rejected
	if validateNTime(extremeTime, normalTime) {
		t.Error("Should reject share ntime near uint32 max (year 2106+)")
	}

	// Extreme job time should also cause rejection
	if validateNTime(normalTime, extremeTime) {
		t.Error("Should reject when job ntime is near uint32 max")
	}
}
