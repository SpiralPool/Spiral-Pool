// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Tests for pure job-building functions that do not require a daemon connection:
//   - formatPrevHash: 4-byte group order reversal for Stratum protocol
//   - bitsToTarget: compact difficulty bits → difficulty float64
//   - buildCoinbase: raw coinbase transaction construction
//   - buildCoinbaseWithAux: merge mining coinbase with aux commitment
//   - buildMerkleBranches: merkle branch sibling path for coinbase
package jobs

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test helpers
// ═══════════════════════════════════════════════════════════════════════════════

// newTestJobManager creates a minimal Manager for testing pure functions.
// No daemon client, no ZMQ, no database — only the fields these functions access.
func newTestJobManager() *Manager {
	return &Manager{
		coinbaseText: "/SpiralPool/",
		outputScript: []byte{
			0x76, 0xa9, 0x14, // OP_DUP OP_HASH160 PUSH20
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
			0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14,
			0x88, 0xac, // OP_EQUALVERIFY OP_CHECKSIG
		},
		logger: zap.NewNop().Sugar(),
	}
}

// reverseHexBytes reverses the byte order of a hex-encoded string.
// Used to match crypto.ReverseBytes behavior for merkle branch verification.
func reverseHexBytes(hexStr string) string {
	b, _ := hex.DecodeString(hexStr)
	for i := 0; i < len(b)/2; i++ {
		j := len(b) - 1 - i
		b[i], b[j] = b[j], b[i]
	}
	return hex.EncodeToString(b)
}

// ═══════════════════════════════════════════════════════════════════════════════
// formatPrevHash tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestFormatPrevHash_GroupOrderReversal(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// formatPrevHash reverses the ORDER of 4-byte (8 hex char) groups.
	// This is the standard stratum encoding: miners bswap32 each group to recover
	// the internal (LE) byte order for the block header.
	// [G0][G1][G2][G3][G4][G5][G6][G7] → [G7][G6][G5][G4][G3][G2][G1][G0]
	input := "00112233" + "44556677" + "8899aabb" + "ccddeeff" +
		"00112233" + "44556677" + "8899aabb" + "ccddeeff"
	expected := "ccddeeff" + "8899aabb" + "44556677" + "00112233" +
		"ccddeeff" + "8899aabb" + "44556677" + "00112233"

	got := m.formatPrevHash(input)
	if got != expected {
		t.Errorf("formatPrevHash mismatch:\n  got:  %s\n  want: %s", got, expected)
	}
}

func TestFormatPrevHash_RealBlockHash(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Bitcoin block 100000 hash (display format / big-endian)
	input := "000000000003ba27aa200b1cecaad478d2b00432346c3f1f3986da1afd33e506"

	got := m.formatPrevHash(input)

	// Group order reversed (8-char groups):
	// Display:  [00000000][0003ba27][aa200b1c][ecaad478][d2b00432][346c3f1f][3986da1a][fd33e506]
	// Stratum:  [fd33e506][3986da1a][346c3f1f][d2b00432][ecaad478][aa200b1c][0003ba27][00000000]
	//
	// After miner bswap32 each group → full byte reversal of display = internal (LE):
	// [06e533fd][1ada8639][1f3f6c34][3204b0d2][78d4aaec][1c0b20aa][27ba0300][00000000]
	expected := "fd33e506" + "3986da1a" + "346c3f1f" + "d2b00432" +
		"ecaad478" + "aa200b1c" + "0003ba27" + "00000000"

	if got != expected {
		t.Errorf("formatPrevHash mismatch:\n  got:  %s\n  want: %s", got, expected)
	}
}

func TestFormatPrevHash_InvalidLength_ReturnsUnchanged(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Too short
	if got := m.formatPrevHash("abcd"); got != "abcd" {
		t.Errorf("short input should be unchanged, got %q", got)
	}
	// Too long
	long := strings.Repeat("aa", 33) // 66 hex chars
	if got := m.formatPrevHash(long); got != long {
		t.Errorf("long input should be unchanged")
	}
	// Empty
	if got := m.formatPrevHash(""); got != "" {
		t.Errorf("empty should be unchanged, got %q", got)
	}
}

func TestFormatPrevHash_AllZeros(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	input := strings.Repeat("0", 64)
	if got := m.formatPrevHash(input); got != input {
		t.Errorf("all zeros should be unchanged, got %q", got)
	}
}

func TestFormatPrevHash_AllFF(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	input := strings.Repeat("f", 64)
	if got := m.formatPrevHash(input); got != input {
		t.Errorf("all ff should be unchanged, got %q", got)
	}
}

func TestFormatPrevHash_Roundtrip(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Applying formatPrevHash twice should return the original
	original := "000000000003ba27aa200b1cecaad478d2b00432346c3f1f3986da1afd33e506"
	roundtrip := m.formatPrevHash(m.formatPrevHash(original))
	if roundtrip != original {
		t.Errorf("double formatPrevHash should be identity, got %s", roundtrip)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// bitsToTarget tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBitsToTarget_GenesisBlock_Difficulty1(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// 0x1d00ffff is the "difficulty 1" compact target for Bitcoin/DigiByte
	diff, err := m.bitsToTarget("1d00ffff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff != 1.0 {
		t.Errorf("bits 1d00ffff difficulty = %f, want 1.0", diff)
	}
}

func TestBitsToTarget_HighDifficulty(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Bitcoin block 600000 bits: 0x17148edf (difficulty ≈ 13.7 trillion)
	diff, err := m.bitsToTarget("17148edf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff < 1e13 {
		t.Errorf("bits 17148edf should give difficulty > 1e13, got %e", diff)
	}
}

func TestBitsToTarget_DifficultyOrdering(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Lower exponent = smaller target = higher difficulty
	cases := []string{
		"1d00ffff", // difficulty 1
		"1c00ffff", // difficulty 256
		"1b00ffff", // difficulty 65536
		"1a00ffff", // difficulty ~16M
	}

	var lastDiff float64
	for _, bits := range cases {
		diff, err := m.bitsToTarget(bits)
		if err != nil {
			t.Fatalf("unexpected error for bits %s: %v", bits, err)
		}
		if lastDiff > 0 && diff <= lastDiff {
			t.Errorf("difficulty should increase as exponent decreases: bits=%s diff=%f <= prev=%f",
				bits, diff, lastDiff)
		}
		lastDiff = diff
	}
}

func TestBitsToTarget_InvalidLength(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	_, err := m.bitsToTarget("1d00ff") // 6 chars
	if err == nil {
		t.Error("expected error for invalid length bits")
	}
}

func TestBitsToTarget_InvalidHex(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	_, err := m.bitsToTarget("zzzzzzzz")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestBitsToTarget_ZeroMantissa(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// 0x1d000000 → mantissa = 0 → zero target
	_, err := m.bitsToTarget("1d000000")
	if err == nil {
		t.Error("expected error for zero mantissa (zero target)")
	}
}

func TestBitsToTarget_NegativeFlag(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// 0x1d800000 → bit 23 of mantissa is set (negative flag)
	_, err := m.bitsToTarget("1d800000")
	if err == nil {
		t.Error("expected error for negative flag in bits")
	}
}

func TestBitsToTarget_SmallExponent(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Exponent <= 3: target = mantissa >> (8*(3-exponent))
	// 0x0300ffff → exponent=3, mantissa=0xffff → target = 0xffff
	diff, err := m.bitsToTarget("0300ffff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff <= 0 {
		t.Errorf("difficulty should be positive, got %f", diff)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// buildCoinbase tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBuildCoinbase_VersionAndInputStructure(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 1250000000, // 12.5 BTC
		Bits:          "1d00ffff",
	}

	cb1, _ := m.buildCoinbase(template)

	if len(cb1) < 42 {
		t.Fatalf("cb1 too short: %d bytes", len(cb1))
	}

	// Version: 01000000 (little-endian)
	if cb1[0] != 0x01 || cb1[1] != 0x00 || cb1[2] != 0x00 || cb1[3] != 0x00 {
		t.Errorf("version = %x, want 01000000", cb1[0:4])
	}

	// Input count: 1
	if cb1[4] != 0x01 {
		t.Errorf("input count = %d, want 1", cb1[4])
	}

	// Previous output: 32 zero bytes (null outpoint for coinbase)
	for i := 5; i < 37; i++ {
		if cb1[i] != 0x00 {
			t.Errorf("prev_out hash byte %d = 0x%02x, want 0x00", i-5, cb1[i])
			break
		}
	}

	// Previous output index: 0xffffffff
	if cb1[37] != 0xff || cb1[38] != 0xff || cb1[39] != 0xff || cb1[40] != 0xff {
		t.Errorf("prev_out index = %x, want ffffffff", cb1[37:41])
	}
}

func TestBuildCoinbase_ScriptsigLength(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 1250000000,
		Bits:          "1d00ffff",
	}

	cb1, _ := m.buildCoinbase(template)

	// Scriptsig length byte is at position 41
	scriptsigLen := int(cb1[41])

	// Expected: len(encodeHeight(100000)) + len("/SpiralPool/") + 8 (extranonce)
	heightBytes := encodeHeight(100000)
	expectedLen := len(heightBytes) + len("/SpiralPool/") + 8
	if scriptsigLen != expectedLen {
		t.Errorf("scriptsig length = %d, want %d (height=%d + text=%d + extranonce=8)",
			scriptsigLen, expectedLen, len(heightBytes), len("/SpiralPool/"))
	}
}

func TestBuildCoinbase_OutputValue(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 1250000000,
		Bits:          "1d00ffff",
	}

	_, cb2 := m.buildCoinbase(template)

	// cb2 structure: [sequence:4][output_count:1][value:8][script_len:1]...
	// Sequence
	if cb2[0] != 0xff || cb2[1] != 0xff || cb2[2] != 0xff || cb2[3] != 0xff {
		t.Errorf("sequence = %x, want ffffffff", cb2[0:4])
	}

	// Output count = 1 (no witness commitment)
	if cb2[4] != 0x01 {
		t.Errorf("output count = %d, want 1", cb2[4])
	}

	// Output value (little-endian uint64)
	value := binary.LittleEndian.Uint64(cb2[5:13])
	if value != 1250000000 {
		t.Errorf("output value = %d, want 1250000000", value)
	}
}

func TestBuildCoinbase_Locktime(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 1250000000,
		Bits:          "1d00ffff",
	}

	_, cb2 := m.buildCoinbase(template)

	// Last 4 bytes should be locktime = 0
	lt := cb2[len(cb2)-4:]
	if lt[0] != 0 || lt[1] != 0 || lt[2] != 0 || lt[3] != 0 {
		t.Errorf("locktime = %x, want 00000000", lt)
	}
}

func TestBuildCoinbase_WithWitnessCommitment(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Valid OP_RETURN witness commitment script
	witnessHex := "6a" + "24" + "aa21a9ed" + strings.Repeat("ab", 32)

	template := &daemon.BlockTemplate{
		Height:                   100001,
		CoinbaseValue:            625000000,
		Bits:                     "1d00ffff",
		DefaultWitnessCommitment: witnessHex,
	}

	_, cb2 := m.buildCoinbase(template)

	// Output count should be 2 (pool reward + witness commitment)
	if cb2[4] != 0x02 {
		t.Errorf("output count with witness = %d, want 2", cb2[4])
	}
}

func TestBuildCoinbase_NegativeCoinbaseValue_ClampedToZero(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: -1,
		Bits:          "1d00ffff",
	}

	_, cb2 := m.buildCoinbase(template)

	value := binary.LittleEndian.Uint64(cb2[5:13])
	if value != 0 {
		t.Errorf("negative coinbase value should be clamped to 0, got %d", value)
	}
}

func TestBuildCoinbase_ScriptsigTruncation(t *testing.T) {
	t.Parallel()
	m := &Manager{
		coinbaseText: strings.Repeat("X", 100), // Exceeds 100-byte limit
		outputScript: []byte{0x76, 0xa9, 0x14,
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
			0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14,
			0x88, 0xac,
		},
		logger: zap.NewNop().Sugar(),
	}

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 100000000,
		Bits:          "1d00ffff",
	}

	cb1, _ := m.buildCoinbase(template)

	scriptsigLen := int(cb1[41])
	if scriptsigLen > 100 {
		t.Errorf("scriptsig length %d exceeds Bitcoin max of 100", scriptsigLen)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// buildMerkleBranches tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBuildMerkleBranches_NoTransactions(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Transactions: []daemon.TxData{},
	}

	branches := m.buildMerkleBranches(template)
	if branches == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(branches) != 0 {
		t.Errorf("expected 0 branches for empty block, got %d", len(branches))
	}
}

func TestBuildMerkleBranches_SingleTransaction(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// One transaction besides coinbase: need 1 branch (the sibling)
	tx1Hash := strings.Repeat("ab", 32)
	template := &daemon.BlockTemplate{
		Transactions: []daemon.TxData{
			{TxID: tx1Hash},
		},
	}

	branches := m.buildMerkleBranches(template)
	if len(branches) != 1 {
		t.Fatalf("expected 1 branch for 1 transaction, got %d", len(branches))
	}

	// Branch should be the reversed (little-endian) tx1 hash
	expected := reverseHexBytes(tx1Hash)
	if branches[0] != expected {
		t.Errorf("branch[0] = %s, want %s (LE-reversed tx1)", branches[0], expected)
	}
}

func TestBuildMerkleBranches_TwoTransactions(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Tree with [coinbase, tx1, tx2]:
	//   Level 0: [nil, tx1LE, tx2LE] → branch[0] = tx1LE
	//   Level 1: [nil, SHA256d(tx2LE||tx2LE)] → branch[1] = SHA256d(tx2LE||tx2LE)
	tx1Hash := strings.Repeat("01", 32)
	tx2Hash := strings.Repeat("02", 32)
	template := &daemon.BlockTemplate{
		Transactions: []daemon.TxData{
			{TxID: tx1Hash},
			{TxID: tx2Hash},
		},
	}

	branches := m.buildMerkleBranches(template)
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches for 2 transactions, got %d", len(branches))
	}

	// First branch: reversed tx1
	expectedFirst := reverseHexBytes(tx1Hash)
	if branches[0] != expectedFirst {
		t.Errorf("branch[0] = %s, want %s", branches[0], expectedFirst)
	}

	// Second branch: SHA256d(tx2LE || tx2LE) (odd leaf duplicated)
	tx2Bytes, _ := hex.DecodeString(tx2Hash)
	tx2LE := crypto.ReverseBytes(tx2Bytes)
	combined := append(tx2LE, tx2LE...)
	expectedSecond := hex.EncodeToString(crypto.SHA256d(combined))
	if branches[1] != expectedSecond {
		t.Errorf("branch[1] = %s, want %s", branches[1], expectedSecond)
	}
}

func TestBuildMerkleBranches_ThreeTransactions(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	// Tree with [coinbase, tx1, tx2, tx3]:
	//        root
	//       /    \
	//     H01     H23
	//    /  \    /  \
	//  cb   tx1 tx2 tx3
	//
	// branch[0] = tx1LE (sibling of cb at level 0)
	// branch[1] = SHA256d(tx2LE || tx3LE) = H23 (sibling at level 1)
	tx1Hash := strings.Repeat("01", 32)
	tx2Hash := strings.Repeat("02", 32)
	tx3Hash := strings.Repeat("03", 32)
	template := &daemon.BlockTemplate{
		Transactions: []daemon.TxData{
			{TxID: tx1Hash},
			{TxID: tx2Hash},
			{TxID: tx3Hash},
		},
	}

	branches := m.buildMerkleBranches(template)
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches for 3 transactions, got %d", len(branches))
	}

	// First branch: reversed tx1
	expectedFirst := reverseHexBytes(tx1Hash)
	if branches[0] != expectedFirst {
		t.Errorf("branch[0] = %s, want %s", branches[0], expectedFirst)
	}

	// Second branch: SHA256d(tx2LE || tx3LE)
	tx2Bytes, _ := hex.DecodeString(tx2Hash)
	tx3Bytes, _ := hex.DecodeString(tx3Hash)
	tx2LE := crypto.ReverseBytes(tx2Bytes)
	tx3LE := crypto.ReverseBytes(tx3Bytes)
	combined := append(tx2LE, tx3LE...)
	expectedSecond := hex.EncodeToString(crypto.SHA256d(combined))
	if branches[1] != expectedSecond {
		t.Errorf("branch[1] = %s, want %s", branches[1], expectedSecond)
	}
}

func TestBuildMerkleBranches_InvalidTxHash(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Transactions: []daemon.TxData{
			{TxID: "invalid_hex_string"},
		},
	}

	branches := m.buildMerkleBranches(template)
	if branches != nil {
		t.Errorf("expected nil for invalid transaction hash, got %v", branches)
	}
}

func TestBuildMerkleBranches_WrongHashLength(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Transactions: []daemon.TxData{
			{TxID: "abcd"}, // Only 2 bytes instead of 32
		},
	}

	branches := m.buildMerkleBranches(template)
	if branches != nil {
		t.Errorf("expected nil for wrong-length hash, got %v", branches)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// buildCoinbaseWithAux tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestBuildCoinbaseWithAux_NilRoot_FallsBackToStandard(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 100000000,
		Bits:          "1d00ffff",
	}

	cb1Aux, cb2Aux := m.buildCoinbaseWithAux(template, nil, 0)
	cb1Std, cb2Std := m.buildCoinbase(template)

	if hex.EncodeToString(cb1Aux) != hex.EncodeToString(cb1Std) {
		t.Error("buildCoinbaseWithAux(nil root) should produce same cb1 as buildCoinbase")
	}
	if hex.EncodeToString(cb2Aux) != hex.EncodeToString(cb2Std) {
		t.Error("buildCoinbaseWithAux(nil root) should produce same cb2 as buildCoinbase")
	}
}

func TestBuildCoinbaseWithAux_ContainsAuxMagicMarker(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	auxRoot := make([]byte, 32)
	for i := range auxRoot {
		auxRoot[i] = 0xAA
	}

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 100000000,
		Bits:          "1d00ffff",
	}

	cb1, _ := m.buildCoinbaseWithAux(template, auxRoot, 4)

	// The 0xfabe6d6d magic marker must be present in the coinbase scriptsig
	cb1Hex := hex.EncodeToString(cb1)
	if !strings.Contains(cb1Hex, "fabe6d6d") {
		t.Errorf("cb1 should contain aux magic marker fabe6d6d, got %s", cb1Hex)
	}
}

func TestBuildCoinbaseWithAux_ContainsAuxRoot(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	auxRoot := make([]byte, 32)
	for i := range auxRoot {
		auxRoot[i] = byte(i)
	}

	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 100000000,
		Bits:          "1d00ffff",
	}

	cb1, _ := m.buildCoinbaseWithAux(template, auxRoot, 2)

	// BuildAuxCommitment reverses aux root from little-endian to big-endian,
	// so we must check for the reversed bytes in cb1.
	reversedRoot := make([]byte, 32)
	for i := 0; i < 32; i++ {
		reversedRoot[i] = auxRoot[31-i]
	}
	reversedHex := hex.EncodeToString(reversedRoot)
	cb1Hex := hex.EncodeToString(cb1)
	if !strings.Contains(cb1Hex, reversedHex) {
		t.Error("cb1 should contain the aux merkle root (big-endian)")
	}
}

func TestBuildCoinbaseWithAux_CB1LargerByAuxSize(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	auxRoot := make([]byte, 32)
	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 100000000,
		Bits:          "1d00ffff",
	}

	cb1Aux, _ := m.buildCoinbaseWithAux(template, auxRoot, 4)
	cb1Std, _ := m.buildCoinbase(template)

	// Aux commitment is 44 bytes (4 magic + 32 root + 4 tree size + 4 nonce)
	diff := len(cb1Aux) - len(cb1Std)
	if diff != 44 {
		t.Errorf("aux cb1 should be 44 bytes larger than standard, diff=%d", diff)
	}
}

func TestBuildCoinbaseWithAux_CB2MatchesStandard(t *testing.T) {
	t.Parallel()
	m := newTestJobManager()

	auxRoot := make([]byte, 32)
	template := &daemon.BlockTemplate{
		Height:        100000,
		CoinbaseValue: 100000000,
		Bits:          "1d00ffff",
	}

	_, cb2Aux := m.buildCoinbaseWithAux(template, auxRoot, 4)
	_, cb2Std := m.buildCoinbase(template)

	// cb2 (outputs + locktime) should be identical for aux and standard
	if hex.EncodeToString(cb2Aux) != hex.EncodeToString(cb2Std) {
		t.Error("cb2 from aux version should match standard version")
	}
}
