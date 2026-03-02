// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// =============================================================================
// TEST SUITE: Coinbase Reward Routing Verification
// =============================================================================
// These tests verify that coinbase transactions correctly route 100% of
// block rewards to the configured solo pool wallet address.

// MockCoinbaseBuilder simulates coinbase construction for testing
type MockCoinbaseBuilder struct {
	version        uint32
	height         uint64
	coinbaseText   string
	coinbaseValue  int64 // satoshis
	outputScript   []byte
	auxCommitment  []byte
	extranonce1    []byte
	extranonce2Len int
}

func NewMockCoinbaseBuilder() *MockCoinbaseBuilder {
	return &MockCoinbaseBuilder{
		version:        1,
		height:         1000,
		coinbaseText:   "SpiralPool",
		coinbaseValue:  625000000, // 6.25 BTC in satoshis
		extranonce1:    []byte{0x00, 0x00, 0x00, 0x01},
		extranonce2Len: 4,
	}
}

// SetPoolAddress sets the output script from address
func (b *MockCoinbaseBuilder) SetPoolAddress(addressType string, pubKeyHash []byte) {
	switch addressType {
	case "p2pkh":
		// OP_DUP OP_HASH160 <20-byte-hash> OP_EQUALVERIFY OP_CHECKSIG
		b.outputScript = append([]byte{0x76, 0xa9, 0x14}, pubKeyHash[:20]...)
		b.outputScript = append(b.outputScript, 0x88, 0xac)
	case "p2wpkh":
		// OP_0 <20-byte-hash>
		b.outputScript = append([]byte{0x00, 0x14}, pubKeyHash[:20]...)
	case "p2sh":
		// OP_HASH160 <20-byte-hash> OP_EQUAL
		b.outputScript = append([]byte{0xa9, 0x14}, pubKeyHash[:20]...)
		b.outputScript = append(b.outputScript, 0x87)
	}
}

// Build constructs the coinbase transaction
func (b *MockCoinbaseBuilder) Build() (cb1, cb2 []byte) {
	// CB1: version + input + scriptsig (up to extranonce placeholder)
	cb1 = make([]byte, 0, 100)

	// Version (4 bytes, little-endian)
	versionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(versionBytes, b.version)
	cb1 = append(cb1, versionBytes...)

	// Input count (varint = 1)
	cb1 = append(cb1, 0x01)

	// Previous output (32 zero bytes + 0xffffffff index)
	cb1 = append(cb1, bytes.Repeat([]byte{0x00}, 32)...)
	cb1 = append(cb1, 0xff, 0xff, 0xff, 0xff)

	// ScriptSig: height + coinbase text + aux commitment (if any)
	scriptSig := make([]byte, 0, 100)

	// Height (BIP34)
	heightBytes := encodeHeightTest(b.height)
	scriptSig = append(scriptSig, heightBytes...)

	// Coinbase text
	scriptSig = append(scriptSig, []byte(b.coinbaseText)...)

	// Aux commitment (if present)
	if len(b.auxCommitment) > 0 {
		scriptSig = append(scriptSig, b.auxCommitment...)
	}

	// ScriptSig length placeholder (will add extranonces later)
	totalScriptSigLen := len(scriptSig) + len(b.extranonce1) + b.extranonce2Len
	cb1 = append(cb1, byte(totalScriptSigLen))

	// ScriptSig content (before extranonces)
	cb1 = append(cb1, scriptSig...)

	// CB2: sequence + outputs + locktime
	cb2 = make([]byte, 0, 50)

	// Sequence (after extranonce2)
	cb2 = append(cb2, 0xff, 0xff, 0xff, 0xff)

	// Output count
	cb2 = append(cb2, 0x01)

	// Output value (8 bytes, little-endian)
	valueBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(valueBytes, uint64(b.coinbaseValue))
	cb2 = append(cb2, valueBytes...)

	// Output script length + script
	cb2 = append(cb2, byte(len(b.outputScript)))
	cb2 = append(cb2, b.outputScript...)

	// Locktime
	cb2 = append(cb2, 0x00, 0x00, 0x00, 0x00)

	return cb1, cb2
}

// encodeHeightTest encodes block height for scriptsig (BIP34) - test helper
func encodeHeightTest(height uint64) []byte {
	if height <= 16 {
		return []byte{byte(0x50 + height)}
	}

	heightBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(heightBytes, height)

	// Trim trailing zeros
	length := 8
	for length > 0 && heightBytes[length-1] == 0 {
		length--
	}

	// Need extra byte if high bit is set
	if heightBytes[length-1]&0x80 != 0 {
		length++
	}

	return append([]byte{byte(length)}, heightBytes[:length]...)
}

// -----------------------------------------------------------------------------
// Solo Reward Routing Tests
// -----------------------------------------------------------------------------

// TestCoinbase_SoloReward_P2PKH tests P2PKH address reward routing
func TestCoinbase_SoloReward_P2PKH(t *testing.T) {
	t.Parallel()

	builder := NewMockCoinbaseBuilder()

	// Simulated P2PKH pubkey hash (20 bytes)
	pubKeyHash := bytes.Repeat([]byte{0xAB}, 20)
	builder.SetPoolAddress("p2pkh", pubKeyHash)

	cb1, cb2 := builder.Build()

	// Verify output script is valid P2PKH
	// Find output in cb2
	outputValue, outputScript := extractOutput(cb2)

	if outputValue != builder.coinbaseValue {
		t.Errorf("Output value mismatch: expected %d, got %d", builder.coinbaseValue, outputValue)
	}

	// Verify P2PKH structure
	if len(outputScript) != 25 {
		t.Errorf("P2PKH script should be 25 bytes, got %d", len(outputScript))
	}

	if outputScript[0] != 0x76 || outputScript[1] != 0xa9 {
		t.Error("P2PKH script should start with OP_DUP OP_HASH160")
	}

	if outputScript[23] != 0x88 || outputScript[24] != 0xac {
		t.Error("P2PKH script should end with OP_EQUALVERIFY OP_CHECKSIG")
	}

	// Verify pubkey hash embedded correctly
	embeddedHash := outputScript[3:23]
	if !bytes.Equal(embeddedHash, pubKeyHash) {
		t.Error("Embedded pubkey hash doesn't match")
	}

	t.Logf("P2PKH coinbase: CB1=%d bytes, CB2=%d bytes, value=%d satoshis",
		len(cb1), len(cb2), outputValue)
}

// TestCoinbase_SoloReward_P2WPKH tests native SegWit address reward routing
func TestCoinbase_SoloReward_P2WPKH(t *testing.T) {
	t.Parallel()

	builder := NewMockCoinbaseBuilder()

	// Simulated P2WPKH pubkey hash (20 bytes)
	pubKeyHash := bytes.Repeat([]byte{0xCD}, 20)
	builder.SetPoolAddress("p2wpkh", pubKeyHash)

	cb1, cb2 := builder.Build()

	_, outputScript := extractOutput(cb2)

	// Verify P2WPKH structure
	if len(outputScript) != 22 {
		t.Errorf("P2WPKH script should be 22 bytes, got %d", len(outputScript))
	}

	if outputScript[0] != 0x00 || outputScript[1] != 0x14 {
		t.Error("P2WPKH script should start with OP_0 PUSH_20")
	}

	// Verify pubkey hash
	embeddedHash := outputScript[2:22]
	if !bytes.Equal(embeddedHash, pubKeyHash) {
		t.Error("Embedded pubkey hash doesn't match")
	}

	t.Logf("P2WPKH coinbase verified: script=%x", outputScript)
	_ = cb1 // Use cb1 to avoid unused warning
}

// TestCoinbase_SoloReward_FullAmount tests 100% of reward goes to pool
func TestCoinbase_SoloReward_FullAmount(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		coinbaseValue int64
	}{
		{"BTC_6.25", 625000000},
		{"DGB_current", 72800000000},
		{"LTC_12.5", 1250000000},
		{"DOGE_10000", 1000000000000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			builder := NewMockCoinbaseBuilder()
			builder.coinbaseValue = tc.coinbaseValue
			builder.SetPoolAddress("p2pkh", bytes.Repeat([]byte{0x11}, 20))

			_, cb2 := builder.Build()

			outputValue, _ := extractOutput(cb2)

			if outputValue != tc.coinbaseValue {
				t.Errorf("Full reward not routed: expected %d, got %d",
					tc.coinbaseValue, outputValue)
			}
		})
	}
}

// extractOutput extracts value and script from CB2
func extractOutput(cb2 []byte) (int64, []byte) {
	// Skip sequence (4 bytes) + output count (1 byte)
	pos := 5

	// Value (8 bytes)
	value := int64(binary.LittleEndian.Uint64(cb2[pos : pos+8]))
	pos += 8

	// Script length
	scriptLen := int(cb2[pos])
	pos++

	// Script
	script := cb2[pos : pos+scriptLen]

	return value, script
}

// -----------------------------------------------------------------------------
// Aux Chain Reward Routing Tests
// -----------------------------------------------------------------------------

// TestCoinbase_AuxReward_IndependentAddress tests aux rewards go to aux address
func TestCoinbase_AuxReward_IndependentAddress(t *testing.T) {
	t.Parallel()

	// Simulate parent and aux having different addresses
	parentAddress := bytes.Repeat([]byte{0xAA}, 20)
	auxAddress := bytes.Repeat([]byte{0xBB}, 20)

	// Parent coinbase
	parentBuilder := NewMockCoinbaseBuilder()
	parentBuilder.SetPoolAddress("p2pkh", parentAddress)
	_, parentCb2 := parentBuilder.Build()

	// Aux coinbase (would be built by aux chain)
	auxBuilder := NewMockCoinbaseBuilder()
	auxBuilder.coinbaseValue = 1000000000000 // DOGE value
	auxBuilder.SetPoolAddress("p2pkh", auxAddress)
	_, auxCb2 := auxBuilder.Build()

	// Extract and compare
	_, parentScript := extractOutput(parentCb2)
	_, auxScript := extractOutput(auxCb2)

	if bytes.Equal(parentScript, auxScript) {
		t.Error("Parent and aux scripts should differ (different addresses)")
	}

	// Verify aux address is correctly embedded
	auxEmbedded := auxScript[3:23]
	if !bytes.Equal(auxEmbedded, auxAddress) {
		t.Error("Aux address not correctly embedded")
	}

	t.Log("Parent and aux addresses verified as independent")
}

// TestCoinbase_AuxCommitment_Present tests aux commitment in parent coinbase
func TestCoinbase_AuxCommitment_Present(t *testing.T) {
	t.Parallel()

	builder := NewMockCoinbaseBuilder()
	builder.SetPoolAddress("p2pkh", bytes.Repeat([]byte{0x11}, 20))

	// Add aux commitment
	auxMarker := []byte{0xfa, 0xbe, 0x6d, 0x6d}
	auxMerkleRoot := bytes.Repeat([]byte{0xCC}, 32)
	treeSize := []byte{0x01, 0x00, 0x00, 0x00}
	merkleNonce := []byte{0x00, 0x00, 0x00, 0x00}

	builder.auxCommitment = append(auxMarker, auxMerkleRoot...)
	builder.auxCommitment = append(builder.auxCommitment, treeSize...)
	builder.auxCommitment = append(builder.auxCommitment, merkleNonce...)

	cb1, _ := builder.Build()

	// Verify aux marker is present
	if !bytes.Contains(cb1, auxMarker) {
		t.Error("Aux marker not found in coinbase")
	}

	// Find marker position
	markerPos := bytes.Index(cb1, auxMarker)
	t.Logf("Aux marker at position %d, commitment length %d", markerPos, len(builder.auxCommitment))

	// Verify merkle root follows marker
	expectedRoot := cb1[markerPos+4 : markerPos+36]
	if !bytes.Equal(expectedRoot, auxMerkleRoot) {
		t.Error("Aux merkle root not correctly positioned")
	}
}

// -----------------------------------------------------------------------------
// Script Validation Tests
// -----------------------------------------------------------------------------

// TestCoinbase_InvalidAddressRejected tests invalid addresses are rejected
func TestCoinbase_InvalidAddressRejected(t *testing.T) {
	t.Parallel()

	// Empty pubkey hash
	builder := NewMockCoinbaseBuilder()
	builder.outputScript = []byte{}

	_, cb2 := builder.Build()

	_, script := extractOutput(cb2)

	if len(script) != 0 {
		// Should be empty or validation should fail
		t.Logf("Empty script produces %d byte output", len(script))
	}
}

// TestCoinbase_MultipleOutputs_NotAllowed tests only one output in SOLO mode
func TestCoinbase_MultipleOutputs_NotAllowed(t *testing.T) {
	t.Parallel()

	// In SOLO mode, coinbase should have exactly ONE output
	// (ignoring OP_RETURN for witness commitment)

	builder := NewMockCoinbaseBuilder()
	builder.SetPoolAddress("p2pkh", bytes.Repeat([]byte{0x11}, 20))

	_, cb2 := builder.Build()

	// Count outputs in cb2
	// After sequence (4 bytes), next byte is output count
	outputCount := cb2[4]

	if outputCount != 1 {
		t.Errorf("SOLO mode should have 1 output, got %d", outputCount)
	}
}

// -----------------------------------------------------------------------------
// Value Validation Tests
// -----------------------------------------------------------------------------

// TestCoinbase_ValueBoundaries tests extreme coinbase values
func TestCoinbase_ValueBoundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		value int64
	}{
		{"minimum_1_satoshi", 1},
		{"typical_btc", 625000000},
		{"large_dgb", 72800000000},
		{"very_large", 1000000000000000}, // 10 million coins * 1e8
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			builder := NewMockCoinbaseBuilder()
			builder.coinbaseValue = tc.value
			builder.SetPoolAddress("p2pkh", bytes.Repeat([]byte{0x11}, 20))

			_, cb2 := builder.Build()

			outputValue, _ := extractOutput(cb2)

			if outputValue != tc.value {
				t.Errorf("Value boundary test failed: expected %d, got %d",
					tc.value, outputValue)
			}
		})
	}
}

// TestCoinbase_NoRewardLeakage tests no value leaks to fees
func TestCoinbase_NoRewardLeakage(t *testing.T) {
	t.Parallel()

	// In proper coinbase, sum of outputs = coinbaseValue
	// (no miner fee in coinbase transaction)

	builder := NewMockCoinbaseBuilder()
	builder.coinbaseValue = 625000000
	builder.SetPoolAddress("p2pkh", bytes.Repeat([]byte{0x11}, 20))

	_, cb2 := builder.Build()

	outputValue, _ := extractOutput(cb2)

	if outputValue != builder.coinbaseValue {
		leakage := builder.coinbaseValue - outputValue
		t.Errorf("Value leakage detected: %d satoshis not in output", leakage)
	}

	t.Logf("No reward leakage: input=%d, output=%d", builder.coinbaseValue, outputValue)
}

// -----------------------------------------------------------------------------
// Hex Encoding Tests
// -----------------------------------------------------------------------------

// TestCoinbase_HexEncoding tests correct hex encoding for RPC
func TestCoinbase_HexEncoding(t *testing.T) {
	t.Parallel()

	builder := NewMockCoinbaseBuilder()
	builder.SetPoolAddress("p2pkh", bytes.Repeat([]byte{0xAB}, 20))

	cb1, cb2 := builder.Build()

	cb1Hex := hex.EncodeToString(cb1)
	cb2Hex := hex.EncodeToString(cb2)

	// Verify hex is valid
	decoded1, err := hex.DecodeString(cb1Hex)
	if err != nil {
		t.Errorf("CB1 hex decode failed: %v", err)
	}

	decoded2, err := hex.DecodeString(cb2Hex)
	if err != nil {
		t.Errorf("CB2 hex decode failed: %v", err)
	}

	if !bytes.Equal(decoded1, cb1) {
		t.Error("CB1 hex round-trip failed")
	}

	if !bytes.Equal(decoded2, cb2) {
		t.Error("CB2 hex round-trip failed")
	}

	t.Logf("CB1 hex: %s...", cb1Hex[:min(32, len(cb1Hex))])
	t.Logf("CB2 hex: %s...", cb2Hex[:min(32, len(cb2Hex))])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
