// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"encoding/hex"
	"math"
	"math/big"
	"testing"

	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// AUDIT TEST SUITE: Validator Gaps
// =============================================================================
// Tests covering gaps T9.5 and T2.6 from PARANOID_TEST_PLAN.md.
//
// T9.5: Zero/NaN/Inf network difficulty handling in difficultyToTarget.
// T2.6: VarInt encoding boundaries at 253+ transactions in buildFullBlock.

// -----------------------------------------------------------------------------
// T9.5: Zero/NaN/Inf Network Difficulty → Zero Target (Reject All)
// -----------------------------------------------------------------------------
// difficultyToTarget (validator.go:830) must return zero target for invalid
// difficulty values. A zero target means no hash can meet the threshold,
// effectively rejecting all shares. This is a security measure to prevent
// miners from exploiting invalid difficulty values.

// TestDifficultyToTarget_Zero verifies that difficulty=0 returns zero target.
// Zero difficulty would mean "any hash is valid" which is a critical
// security vulnerability — it must return zero target (reject all).
func TestDifficultyToTarget_Zero(t *testing.T) {
	t.Parallel()

	target := difficultyToTarget(0)
	if target == nil {
		t.Fatal("difficultyToTarget(0) returned nil — must return non-nil zero target")
	}
	if target.Sign() != 0 {
		t.Errorf("difficultyToTarget(0) = %v, want zero target (security: reject all shares)", target)
	}
}

// TestDifficultyToTarget_NaN verifies that NaN difficulty returns zero target.
// NaN can occur from 0/0 division or corrupt data. It must not produce
// an exploitable target.
func TestDifficultyToTarget_NaN(t *testing.T) {
	t.Parallel()

	nan := math.NaN()
	target := difficultyToTarget(nan)
	if target == nil {
		t.Fatal("difficultyToTarget(NaN) returned nil — must return non-nil zero target")
	}
	if target.Sign() != 0 {
		t.Errorf("difficultyToTarget(NaN) = %v, want zero target (security: reject all shares)", target)
	}
}

// TestDifficultyToTarget_PositiveInf verifies that +Inf difficulty returns zero target.
// +Inf would mean "impossible difficulty" but the division maxTarget/+Inf
// could produce zero or undefined behavior depending on big.Float handling.
func TestDifficultyToTarget_PositiveInf(t *testing.T) {
	t.Parallel()

	posInf := math.Inf(1)
	target := difficultyToTarget(posInf)
	if target == nil {
		t.Fatal("difficultyToTarget(+Inf) returned nil — must return non-nil zero target")
	}
	if target.Sign() != 0 {
		t.Errorf("difficultyToTarget(+Inf) = %v, want zero target (security: reject all shares)", target)
	}
}

// TestDifficultyToTarget_NegativeInf verifies that -Inf difficulty returns zero target.
func TestDifficultyToTarget_NegativeInf(t *testing.T) {
	t.Parallel()

	negInf := math.Inf(-1)
	target := difficultyToTarget(negInf)
	if target == nil {
		t.Fatal("difficultyToTarget(-Inf) returned nil — must return non-nil zero target")
	}
	if target.Sign() != 0 {
		t.Errorf("difficultyToTarget(-Inf) = %v, want zero target (security: reject all shares)", target)
	}
}

// TestDifficultyToTarget_Negative verifies that negative difficulty returns zero target.
func TestDifficultyToTarget_Negative(t *testing.T) {
	t.Parallel()

	for _, diff := range []float64{-1, -0.001, -1e18, -math.SmallestNonzeroFloat64} {
		target := difficultyToTarget(diff)
		if target == nil {
			t.Fatalf("difficultyToTarget(%v) returned nil", diff)
		}
		if target.Sign() != 0 {
			t.Errorf("difficultyToTarget(%v) = %v, want zero target", diff, target)
		}
	}
}

// TestDifficultyToTarget_SmallestPositive verifies that the smallest positive
// float64 produces a valid (very large) target, not overflow or panic.
func TestDifficultyToTarget_SmallestPositive(t *testing.T) {
	t.Parallel()

	target := difficultyToTarget(math.SmallestNonzeroFloat64)
	if target == nil {
		t.Fatal("difficultyToTarget(SmallestNonzeroFloat64) returned nil")
	}
	// Very small difficulty = very large target (easy to mine)
	if target.Sign() <= 0 {
		t.Error("Very small positive difficulty should produce positive target")
	}
}

// TestDifficultyToTarget_LargestFinite verifies that MaxFloat64 produces a
// valid target (should be very close to zero but non-negative).
func TestDifficultyToTarget_LargestFinite(t *testing.T) {
	t.Parallel()

	target := difficultyToTarget(math.MaxFloat64)
	if target == nil {
		t.Fatal("difficultyToTarget(MaxFloat64) returned nil")
	}
	// Very high difficulty = very small target (hard to mine)
	// Should be zero or very small positive — not negative
	if target.Sign() < 0 {
		t.Error("MaxFloat64 difficulty should not produce negative target")
	}
}

// TestDifficultyToTarget_MonotonicallyDecreasing verifies that higher
// difficulty always produces lower (or equal) target.
func TestDifficultyToTarget_MonotonicallyDecreasing(t *testing.T) {
	t.Parallel()

	diffs := []float64{0.001, 0.01, 0.1, 1, 10, 100, 1000, 1e6, 1e9, 1e12}

	var prevTarget *big.Int
	for _, diff := range diffs {
		target := difficultyToTarget(diff)
		if target == nil {
			t.Fatalf("difficultyToTarget(%v) returned nil", diff)
		}
		if prevTarget != nil && target.Cmp(prevTarget) > 0 {
			t.Errorf("Target increased when difficulty increased: diff=%v target=%v > prev=%v",
				diff, target, prevTarget)
		}
		prevTarget = target
	}
}

// TestDifficultyToTarget_Diff1EqualsMaxTarget verifies that difficulty=1
// produces a target equal to the Bitcoin max target (0x00000000FFFF0...0).
func TestDifficultyToTarget_Diff1EqualsMaxTarget(t *testing.T) {
	t.Parallel()

	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	target := difficultyToTarget(1.0)
	if target == nil {
		t.Fatal("difficultyToTarget(1.0) returned nil")
	}

	if target.Cmp(maxTarget) != 0 {
		// Allow small precision difference
		diff := new(big.Int).Sub(maxTarget, target)
		diff.Abs(diff)
		ratio := new(big.Float).Quo(
			new(big.Float).SetInt(diff),
			new(big.Float).SetInt(maxTarget),
		)
		ratioF, _ := ratio.Float64()
		if ratioF > 0.001 {
			t.Errorf("difficultyToTarget(1.0) differs from maxTarget by %.4f%%", ratioF*100)
		}
	}
}

// -----------------------------------------------------------------------------
// T2.6: VarInt Encoding Boundaries (253+ Transactions)
// -----------------------------------------------------------------------------
// encodeVarInt (validator.go:777) uses Bitcoin CompactSize encoding.
// The critical boundaries are at 253 (switches from 1-byte to 3-byte) and
// 65536 (switches from 3-byte to 5-byte). Block construction via buildFullBlock
// uses crypto.EncodeVarInt(1 + len(txData)) for the transaction count.

// TestEncodeVarInt_BoundaryAt253 verifies the critical boundary where encoding
// switches from 1-byte (0x00-0xFC) to 3-byte (0xFD prefix).
func TestEncodeVarInt_BoundaryAt253(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		value    uint64
		expected []byte
	}{
		// Last 1-byte value
		{"252 (last 1-byte)", 252, []byte{0xFC}},
		// First 3-byte value
		{"253 (first 3-byte)", 253, []byte{0xFD, 0xFD, 0x00}},
		// 254
		{"254", 254, []byte{0xFD, 0xFE, 0x00}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := crypto.EncodeVarInt(tc.value)
			if !bytesEqual(result, tc.expected) {
				t.Errorf("crypto.EncodeVarInt(%d) = %x, want %x", tc.value, result, tc.expected)
			}
		})
	}
}

// TestEncodeVarInt_BoundaryAt65536 verifies the boundary where encoding
// switches from 3-byte (0xFD prefix) to 5-byte (0xFE prefix).
func TestEncodeVarInt_BoundaryAt65536(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		value    uint64
		expected []byte
	}{
		// Last 3-byte value
		{"65535 (last 3-byte)", 65535, []byte{0xFD, 0xFF, 0xFF}},
		// First 5-byte value
		{"65536 (first 5-byte)", 65536, []byte{0xFE, 0x00, 0x00, 0x01, 0x00}},
		// One more
		{"65537", 65537, []byte{0xFE, 0x01, 0x00, 0x01, 0x00}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := crypto.EncodeVarInt(tc.value)
			if !bytesEqual(result, tc.expected) {
				t.Errorf("crypto.EncodeVarInt(%d) = %x, want %x", tc.value, result, tc.expected)
			}
		})
	}
}

// TestEncodeVarInt_BoundaryAt4294967296 verifies the boundary where encoding
// switches from 5-byte (0xFE prefix) to 9-byte (0xFF prefix).
func TestEncodeVarInt_BoundaryAt4294967296(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		value    uint64
		expected []byte
	}{
		// Last 5-byte value
		{"4294967295 (last 5-byte)", 4294967295, []byte{0xFE, 0xFF, 0xFF, 0xFF, 0xFF}},
		// First 9-byte value
		{"4294967296 (first 9-byte)", 4294967296, []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := crypto.EncodeVarInt(tc.value)
			if !bytesEqual(result, tc.expected) {
				t.Errorf("crypto.EncodeVarInt(%d) = %x, want %x", tc.value, result, tc.expected)
			}
		})
	}
}

// TestBuildFullBlock_253Transactions verifies that buildFullBlock correctly
// encodes the transaction count at the critical 253 boundary.
// With 252 extra transactions + 1 coinbase = 253 total transactions,
// the varint must use the 3-byte encoding (0xFD prefix).
func TestBuildFullBlock_253Transactions(t *testing.T) {
	t.Parallel()

	// Minimal valid transaction hex
	txHex := "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00000000" +
		"00" +
		"ffffffff" +
		"01" +
		"0000000000000000" +
		"00" +
		"00000000"

	// Create 252 extra transactions so total = 252 + 1 coinbase = 253
	txData := make([]string, 252)
	for i := range txData {
		txData[i] = txHex
	}

	job := &protocol.Job{
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		TransactionData: txData,
	}

	header := make([]byte, 80)
	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock with 253 txs failed: %v", err)
	}

	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("Invalid block hex: %v", err)
	}

	// At byte offset 80, the varint for 253 transactions should be:
	// 0xFD, 0xFD, 0x00 (3-byte encoding)
	if len(block) < 83 {
		t.Fatalf("Block too short: %d bytes, need at least 83", len(block))
	}

	if block[80] != 0xFD {
		t.Errorf("VarInt prefix byte = 0x%02x, want 0xFD for 253 transactions", block[80])
	}
	if block[81] != 0xFD {
		t.Errorf("VarInt low byte = 0x%02x, want 0xFD (253 & 0xFF)", block[81])
	}
	if block[82] != 0x00 {
		t.Errorf("VarInt high byte = 0x%02x, want 0x00 (253 >> 8)", block[82])
	}
}

// TestBuildFullBlock_252Transactions verifies that 252 total transactions
// (251 extra + 1 coinbase) still uses the 1-byte varint encoding.
func TestBuildFullBlock_252Transactions(t *testing.T) {
	t.Parallel()

	txHex := "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00000000" +
		"00" +
		"ffffffff" +
		"01" +
		"0000000000000000" +
		"00" +
		"00000000"

	// 251 extra + 1 coinbase = 252 total
	txData := make([]string, 251)
	for i := range txData {
		txData[i] = txHex
	}

	job := &protocol.Job{
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		TransactionData: txData,
	}

	header := make([]byte, 80)
	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock with 252 txs failed: %v", err)
	}

	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("Invalid block hex: %v", err)
	}

	// 252 in 1-byte encoding = 0xFC
	if len(block) < 81 {
		t.Fatalf("Block too short: %d bytes", len(block))
	}

	if block[80] != 0xFC {
		t.Errorf("VarInt byte = 0x%02x, want 0xFC for 252 transactions", block[80])
	}
}

// TestBuildFullBlock_254Transactions verifies that 254 total transactions
// also uses the 3-byte varint encoding correctly.
func TestBuildFullBlock_254Transactions(t *testing.T) {
	t.Parallel()

	txHex := "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00000000" +
		"00" +
		"ffffffff" +
		"01" +
		"0000000000000000" +
		"00" +
		"00000000"

	// 253 extra + 1 coinbase = 254 total
	txData := make([]string, 253)
	for i := range txData {
		txData[i] = txHex
	}

	job := &protocol.Job{
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		TransactionData: txData,
	}

	header := make([]byte, 80)
	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock with 254 txs failed: %v", err)
	}

	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("Invalid block hex: %v", err)
	}

	// 254 in 3-byte encoding = 0xFD, 0xFE, 0x00
	if len(block) < 83 {
		t.Fatalf("Block too short: %d bytes", len(block))
	}

	if block[80] != 0xFD {
		t.Errorf("VarInt prefix byte = 0x%02x, want 0xFD for 254 transactions", block[80])
	}
	if block[81] != 0xFE {
		t.Errorf("VarInt low byte = 0x%02x, want 0xFE (254 & 0xFF)", block[81])
	}
	if block[82] != 0x00 {
		t.Errorf("VarInt high byte = 0x%02x, want 0x00 (254 >> 8)", block[82])
	}
}

// TestRebuildBlockHex_253Transactions verifies the recovery path also handles
// the 253 varint boundary correctly, matching buildFullBlock output.
func TestRebuildBlockHex_253Transactions(t *testing.T) {
	t.Parallel()

	txHex := "0100000001" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"00000000" +
		"00" +
		"ffffffff" +
		"01" +
		"0000000000000000" +
		"00" +
		"00000000"

	// 252 extra + 1 coinbase = 253 total
	txData := make([]string, 252)
	for i := range txData {
		txData[i] = txHex
	}

	job := &protocol.Job{
		Version:         "20000000",
		PrevBlockHash:   "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:           "1a0377ae",
		NTime:           "64000000",
		TransactionData: txData,
	}

	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

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
		t.Errorf("Recovery path differs from normal path at 253 tx boundary\n"+
			"Normal:   %s...\nRecovery: %s...",
			normalHex[:80], recoveryHex[:80])
	}
}

// TestCompactBitsToTarget_InvalidInputs verifies compactBitsToTarget handles
// malformed inputs without panic (part of T9.5 scope).
func TestCompactBitsToTarget_InvalidInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		bits     string
		wantZero bool
	}{
		{"empty string", "", true},
		{"too short", "1a03", true},
		{"too long", "1a0377ae00", true},
		{"non-hex", "ZZZZZZZZ", true},
		{"zero mantissa", "1a000000", true},
		{"negative flag", "1a800001", true},
		{"valid bits", "1a0377ae", false},
		{"difficulty 1 bits", "1d00ffff", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			target := compactBitsToTarget(tc.bits)
			if target == nil {
				t.Fatal("compactBitsToTarget returned nil — must return non-nil")
			}
			if tc.wantZero && target.Sign() != 0 {
				t.Errorf("Expected zero target for invalid input %q, got %v", tc.bits, target)
			}
			if !tc.wantZero && target.Sign() <= 0 {
				t.Errorf("Expected positive target for valid input %q, got %v", tc.bits, target)
			}
		})
	}
}
