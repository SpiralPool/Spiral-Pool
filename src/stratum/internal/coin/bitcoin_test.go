// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestBitcoinSymbol(t *testing.T) {
	coin := NewBitcoinCoin()

	if coin.Symbol() != "BTC" {
		t.Errorf("expected symbol BTC, got %s", coin.Symbol())
	}

	if coin.Name() != "Bitcoin" {
		t.Errorf("expected name Bitcoin, got %s", coin.Name())
	}
}

func TestBitcoinAlgorithm(t *testing.T) {
	coin := NewBitcoinCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected Bitcoin to support SegWit")
	}
}

func TestBitcoinNetworkParams(t *testing.T) {
	coin := NewBitcoinCoin()

	if coin.DefaultRPCPort() != 8332 {
		t.Errorf("expected RPC port 8332, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8333 {
		t.Errorf("expected P2P port 8333, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 600 {
		t.Errorf("expected block time 600, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestBitcoinVersionBytes(t *testing.T) {
	coin := NewBitcoinCoin()

	if coin.P2PKHVersionByte() != 0x00 {
		t.Errorf("expected P2PKH version 0x00, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x05 {
		t.Errorf("expected P2SH version 0x05, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "bc" {
		t.Errorf("expected bech32 HRP 'bc', got '%s'", coin.Bech32HRP())
	}
}

func TestBitcoinAddressValidation(t *testing.T) {
	coin := NewBitcoinCoin()

	tests := []struct {
		name     string
		address  string
		valid    bool
		addrType AddressType
	}{
		// Legacy P2PKH (1...)
		{
			name:     "valid P2PKH",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		// Legacy P2SH (3...)
		{
			name:     "valid P2SH",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		// Native SegWit P2WPKH (bc1q...)
		{
			name:     "valid bech32 P2WPKH",
			address:  "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
			valid:    true,
			addrType: AddressTypeP2WPKH,
		},
		// Invalid - wrong checksum
		{
			name:     "invalid checksum",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN3",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		// Empty
		{
			name:     "empty address",
			address:  "",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		// Too short
		{
			name:     "too short",
			address:  "1Bv",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

func TestBitcoinBlockHeader(t *testing.T) {
	coin := NewBitcoinCoin()

	// Create a test block header
	header := &BlockHeader{
		Version:           536870912, // 0x20000000
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1d00ffff, // Difficulty 1
		Nonce:             2083236893,
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

func TestBitcoinDifficulty(t *testing.T) {
	coin := NewBitcoinCoin()

	// Test difficulty 1 (genesis block target)
	bits := uint32(0x1d00ffff)
	target := coin.TargetFromBits(bits)

	if target.Sign() == 0 {
		t.Error("expected non-zero target")
	}

	diff := coin.DifficultyFromTarget(target)
	// Difficulty should be very close to 1.0
	if diff < 0.99 || diff > 1.01 {
		t.Errorf("expected difficulty ~1.0, got %.4f", diff)
	}

	// Test higher difficulty
	hardBits := uint32(0x170b8c8b) // A real Bitcoin bits value
	hardTarget := coin.TargetFromBits(hardBits)
	hardDiff := coin.DifficultyFromTarget(hardTarget)

	if hardDiff <= 1.0 {
		t.Errorf("expected difficulty > 1.0 for bits 0x%08x, got %.4f", hardBits, hardDiff)
	}
}

func TestBitcoinCoinbaseScript(t *testing.T) {
	coin := NewBitcoinCoin()

	// Test P2PKH address
	params := CoinbaseParams{
		Height:            800000,
		ExtraNonce:        []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:       "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
		BlockReward:       625000000, // 6.25 BTC
		WitnessCommitment: nil,
		CoinbaseMessage:   "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script: %v", err)
	}

	// P2PKH script should be 25 bytes
	if len(script) != 25 {
		t.Errorf("expected 25 byte P2PKH script, got %d", len(script))
	}

	// Check script format
	if script[0] != 0x76 { // OP_DUP
		t.Errorf("expected OP_DUP (0x76), got 0x%02x", script[0])
	}
}

func TestBitcoinCoinbaseScriptBech32(t *testing.T) {
	coin := NewBitcoinCoin()

	// Test bech32 address (P2WPKH)
	params := CoinbaseParams{
		Height:          800000,
		PoolAddress:     "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		BlockReward:     625000000,
		CoinbaseMessage: "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script for bech32: %v", err)
	}

	// P2WPKH script should be 22 bytes: OP_0 (1) + PUSH_20 (1) + hash (20)
	if len(script) != 22 {
		t.Errorf("expected 22 byte P2WPKH script, got %d", len(script))
	}

	// Check script format
	if script[0] != 0x00 { // OP_0 (witness version)
		t.Errorf("expected OP_0 (0x00), got 0x%02x", script[0])
	}
	if script[1] != 0x14 { // PUSH 20 bytes
		t.Errorf("expected PUSH 20 (0x14), got 0x%02x", script[1])
	}
}

func TestBitcoinShareDiffMultiplier(t *testing.T) {
	coin := NewBitcoinCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func TestBitcoinRegistry(t *testing.T) {
	// BTC should be registered via init()
	if !IsRegistered("BTC") {
		t.Error("expected BTC to be registered")
	}

	if !IsRegistered("BITCOIN") {
		t.Error("expected BITCOIN alias to be registered")
	}

	// Create BTC coin
	btc, err := Create("BTC")
	if err != nil {
		t.Fatalf("failed to create BTC coin: %v", err)
	}

	if btc.Symbol() != "BTC" {
		t.Errorf("expected symbol BTC, got %s", btc.Symbol())
	}

	// Test case insensitivity
	btc2, err := Create("btc")
	if err != nil {
		t.Fatalf("failed to create btc coin (lowercase): %v", err)
	}

	if btc2.Symbol() != "BTC" {
		t.Errorf("expected symbol BTC from lowercase, got %s", btc2.Symbol())
	}
}

// Benchmark tests

func BenchmarkBitcoinAddressValidation(b *testing.B) {
	coin := NewBitcoinCoin()
	address := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkBitcoinBech32Validation(b *testing.B) {
	coin := NewBitcoinCoin()
	address := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkBitcoinBlockHeaderHashing(b *testing.B) {
	coin := NewBitcoinCoin()
	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1d00ffff,
		Nonce:             2083236893,
	}
	serialized := coin.SerializeBlockHeader(header)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.HashBlockHeader(serialized)
	}
}
