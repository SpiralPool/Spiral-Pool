// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestDigiByteSymbol(t *testing.T) {
	coin := NewDigiByteCoin()

	if coin.Symbol() != "DGB" {
		t.Errorf("expected symbol DGB, got %s", coin.Symbol())
	}

	if coin.Name() != "DigiByte" {
		t.Errorf("expected name DigiByte, got %s", coin.Name())
	}
}

func TestDigiByteAlgorithm(t *testing.T) {
	coin := NewDigiByteCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected DigiByte to support SegWit")
	}
}

func TestDigiByteNetworkParams(t *testing.T) {
	coin := NewDigiByteCoin()

	if coin.DefaultRPCPort() != 14022 {
		t.Errorf("expected RPC port 14022, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 12024 {
		t.Errorf("expected P2P port 12024, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 15 {
		t.Errorf("expected block time 15, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestDigiByteAddressValidation(t *testing.T) {
	coin := NewDigiByteCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Valid P2PKH address (D prefix) - known valid DigiByte address
		// DGB mainnet P2PKH: version byte 0x1e (30)
		{"DBXu2kgc3xtvCUWFcxFE3r9hEYgmuaaCyD", true, AddressTypeP2PKH},
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"DFu", false, AddressTypeUnknown},
		// Invalid characters
		{"D0OIl1", false, AddressTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			err := coin.ValidateAddress(tt.address)
			if tt.valid && err != nil {
				t.Errorf("expected valid address, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid address, got no error")
			}

			if tt.valid {
				_, addrType, err := coin.DecodeAddress(tt.address)
				if err != nil {
					t.Errorf("expected to decode address, got error: %v", err)
				}
				if addrType != tt.addrType {
					t.Errorf("expected address type %v, got %v", tt.addrType, addrType)
				}
			}
		})
	}
}

func TestDigiByteBlockHeader(t *testing.T) {
	coin := NewDigiByteCoin()

	// Create a test block header
	header := &BlockHeader{
		Version:           536870912, // 0x20000000
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1b0404cb,
		Nonce:             12345,
	}

	// Fill with test data
	for i := 0; i < 32; i++ {
		header.PreviousBlockHash[i] = byte(i)
		header.MerkleRoot[i] = byte(31 - i)
	}

	// Serialize
	serialized := coin.SerializeBlockHeader(header)

	if len(serialized) != 80 {
		t.Errorf("expected 80 bytes, got %d", len(serialized))
	}

	// Hash should be deterministic
	hash1 := coin.HashBlockHeader(serialized)
	hash2 := coin.HashBlockHeader(serialized)

	if hex.EncodeToString(hash1) != hex.EncodeToString(hash2) {
		t.Error("expected deterministic hash")
	}

	if len(hash1) != 32 {
		t.Errorf("expected 32 byte hash, got %d", len(hash1))
	}
}

func TestDigiByteDifficulty(t *testing.T) {
	coin := NewDigiByteCoin()

	// Test bits to target conversion
	// These are real-world DigiByte difficulty values
	tests := []struct {
		bits         uint32
		expectedDiff float64
		tolerance    float64
	}{
		{0x1b0404cb, 16307.42, 1.0}, // Low difficulty
		{0x1d00ffff, 1.0, 0.001},    // Difficulty 1
	}

	for _, tt := range tests {
		target := coin.TargetFromBits(tt.bits)
		if target.Sign() == 0 {
			t.Errorf("expected non-zero target for bits 0x%08x", tt.bits)
			continue
		}

		diff := coin.DifficultyFromTarget(target)
		if diff < tt.expectedDiff-tt.tolerance || diff > tt.expectedDiff+tt.tolerance {
			t.Errorf("bits 0x%08x: expected difficulty ~%.2f, got %.2f",
				tt.bits, tt.expectedDiff, diff)
		}
	}
}

func TestDigiByteCoinbaseScript(t *testing.T) {
	coin := NewDigiByteCoin()

	params := CoinbaseParams{
		Height:            1000000,
		ExtraNonce:        []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:       "DBXu2kgc3xtvCUWFcxFE3r9hEYgmuaaCyD",
		BlockReward:       72500000000, // 725 DGB
		WitnessCommitment: nil,
		CoinbaseMessage:   "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script: %v", err)
	}

	// P2PKH script should be 25 bytes
	// OP_DUP (1) + OP_HASH160 (1) + PUSH 20 (1) + hash (20) + OP_EQUALVERIFY (1) + OP_CHECKSIG (1)
	if len(script) != 25 {
		t.Errorf("expected 25 byte P2PKH script, got %d", len(script))
	}

	// Check script format
	if script[0] != 0x76 { // OP_DUP
		t.Errorf("expected OP_DUP (0x76), got 0x%02x", script[0])
	}
	if script[1] != 0xa9 { // OP_HASH160
		t.Errorf("expected OP_HASH160 (0xa9), got 0x%02x", script[1])
	}
	if script[2] != 0x14 { // PUSH 20 bytes
		t.Errorf("expected PUSH 20 (0x14), got 0x%02x", script[2])
	}
	if script[23] != 0x88 { // OP_EQUALVERIFY
		t.Errorf("expected OP_EQUALVERIFY (0x88), got 0x%02x", script[23])
	}
	if script[24] != 0xac { // OP_CHECKSIG
		t.Errorf("expected OP_CHECKSIG (0xac), got 0x%02x", script[24])
	}
}

func TestDigiByteVersionBytes(t *testing.T) {
	coin := NewDigiByteCoin()

	if coin.P2PKHVersionByte() != 0x1e {
		t.Errorf("expected P2PKH version 0x1e, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x3f {
		t.Errorf("expected P2SH version 0x3f, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "dgb" {
		t.Errorf("expected bech32 HRP 'dgb', got '%s'", coin.Bech32HRP())
	}
}

func TestDigiByteShareDiffMultiplier(t *testing.T) {
	coin := NewDigiByteCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func TestCoinRegistry(t *testing.T) {
	// DGB should be registered via init()
	if !IsRegistered("DGB") {
		t.Error("expected DGB to be registered")
	}

	if !IsRegistered("DIGIBYTE") {
		t.Error("expected DIGIBYTE alias to be registered")
	}

	// Create DGB coin
	dgb, err := Create("DGB")
	if err != nil {
		t.Fatalf("failed to create DGB coin: %v", err)
	}

	if dgb.Symbol() != "DGB" {
		t.Errorf("expected symbol DGB, got %s", dgb.Symbol())
	}

	// Test case insensitivity
	dgb2, err := Create("dgb")
	if err != nil {
		t.Fatalf("failed to create dgb coin (lowercase): %v", err)
	}

	if dgb2.Symbol() != "DGB" {
		t.Errorf("expected symbol DGB from lowercase, got %s", dgb2.Symbol())
	}

	// Unknown coin
	_, err = Create("UNKNOWN")
	if err == nil {
		t.Error("expected error for unknown coin")
	}
}

func TestListRegistered(t *testing.T) {
	coins := ListRegistered()

	// At minimum, DGB and DIGIBYTE should be registered
	found := false
	for _, c := range coins {
		if c == "DGB" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected DGB in registered coins, got %v", coins)
	}
}

func TestMustCreate(t *testing.T) {
	// Should not panic for valid coin
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("MustCreate panicked for valid coin: %v", r)
			}
		}()
		coin := MustCreate("DGB")
		if coin.Symbol() != "DGB" {
			t.Error("expected DGB coin")
		}
	}()

	// Should panic for invalid coin
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected MustCreate to panic for invalid coin")
			}
		}()
		MustCreate("INVALID")
	}()
}

// Benchmark tests

func BenchmarkAddressValidation(b *testing.B) {
	coin := NewDigiByteCoin()
	address := "DBXu2kgc3xtvCUWFcxFE3r9hEYgmuaaCyD"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkBlockHeaderSerialization(b *testing.B) {
	coin := NewDigiByteCoin()
	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1b0404cb,
		Nonce:             12345,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.SerializeBlockHeader(header)
	}
}

func BenchmarkBlockHeaderHashing(b *testing.B) {
	coin := NewDigiByteCoin()
	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1b0404cb,
		Nonce:             12345,
	}
	serialized := coin.SerializeBlockHeader(header)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.HashBlockHeader(serialized)
	}
}

func BenchmarkTargetFromBits(b *testing.B) {
	coin := NewDigiByteCoin()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.TargetFromBits(0x1b0404cb)
	}
}

func BenchmarkDifficultyFromTarget(b *testing.B) {
	coin := NewDigiByteCoin()
	target := coin.TargetFromBits(0x1b0404cb)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.DifficultyFromTarget(target)
	}
}

func BenchmarkCoinbaseScript(b *testing.B) {
	coin := NewDigiByteCoin()
	params := CoinbaseParams{
		Height:      1000000,
		PoolAddress: "DBXu2kgc3xtvCUWFcxFE3r9hEYgmuaaCyD",
		BlockReward: 72500000000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.BuildCoinbaseScript(params)
	}
}
