// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestBitcoinCashSymbol(t *testing.T) {
	coin := NewBitcoinCashCoin()

	if coin.Symbol() != "BCH" {
		t.Errorf("expected symbol BCH, got %s", coin.Symbol())
	}

	if coin.Name() != "Bitcoin Cash" {
		t.Errorf("expected name 'Bitcoin Cash', got %s", coin.Name())
	}
}

func TestBitcoinCashAlgorithm(t *testing.T) {
	coin := NewBitcoinCashCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	// BCH doesn't support SegWit
	if coin.SupportsSegWit() {
		t.Error("BCH should not support SegWit")
	}
}

func TestBitcoinCashNetworkParams(t *testing.T) {
	coin := NewBitcoinCashCoin()

	if coin.DefaultRPCPort() != 8432 {
		t.Errorf("expected RPC port 8432, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8433 {
		t.Errorf("expected P2P port 8433, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 600 {
		t.Errorf("expected block time 600, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestBitcoinCashVersionBytes(t *testing.T) {
	coin := NewBitcoinCashCoin()

	if coin.P2PKHVersionByte() != 0x00 {
		t.Errorf("expected P2PKH version 0x00, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x05 {
		t.Errorf("expected P2SH version 0x05, got 0x%02x", coin.P2SHVersionByte())
	}

	// BCH doesn't use bech32 (uses CashAddr instead)
	if coin.Bech32HRP() != "" {
		t.Errorf("expected empty bech32 HRP for BCH, got '%s'", coin.Bech32HRP())
	}
}

func TestBitcoinCashAddressValidation(t *testing.T) {
	coin := NewBitcoinCashCoin()

	tests := []struct {
		name     string
		address  string
		valid    bool
		addrType AddressType
	}{
		// CashAddr P2PKH (q...)
		{
			name:     "valid CashAddr P2PKH with prefix",
			address:  "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		{
			name:     "valid CashAddr P2PKH without prefix",
			address:  "qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		// CashAddr P2SH (p...)
		{
			name:     "valid CashAddr P2SH with prefix",
			address:  "bitcoincash:ppm2qsznhks23z7629mms6s4cwef74vcwvn0h829pq",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		// Legacy P2PKH (1...)
		{
			name:     "valid legacy P2PKH",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		// Legacy P2SH (3...)
		{
			name:     "valid legacy P2SH",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		// Invalid
		{
			name:     "empty address",
			address:  "",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "too short",
			address:  "qpm",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "invalid CashAddr checksum",
			address:  "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6b",
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

func TestBitcoinCashBlockHeader(t *testing.T) {
	coin := NewBitcoinCashCoin()

	// Create a test block header
	header := &BlockHeader{
		Version:           536870912, // 0x20000000
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1d00ffff,
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

func TestBitcoinCashDifficulty(t *testing.T) {
	coin := NewBitcoinCashCoin()

	// Test difficulty 1
	bits := uint32(0x1d00ffff)
	target := coin.TargetFromBits(bits)

	if target.Sign() == 0 {
		t.Error("expected non-zero target")
	}

	diff := coin.DifficultyFromTarget(target)
	if diff < 0.99 || diff > 1.01 {
		t.Errorf("expected difficulty ~1.0, got %.4f", diff)
	}
}

func TestBitcoinCashCoinbaseScript(t *testing.T) {
	coin := NewBitcoinCashCoin()

	// Test CashAddr address
	params := CoinbaseParams{
		Height:          800000,
		ExtraNonce:      []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:     "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
		BlockReward:     625000000, // 6.25 BCH
		CoinbaseMessage: "Spiral Pool",
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
	if script[1] != 0xa9 { // OP_HASH160
		t.Errorf("expected OP_HASH160 (0xa9), got 0x%02x", script[1])
	}
}

func TestBitcoinCashCoinbaseScriptLegacy(t *testing.T) {
	coin := NewBitcoinCashCoin()

	// Test legacy address
	params := CoinbaseParams{
		Height:          800000,
		PoolAddress:     "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
		BlockReward:     625000000,
		CoinbaseMessage: "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script for legacy: %v", err)
	}

	// P2PKH script should be 25 bytes
	if len(script) != 25 {
		t.Errorf("expected 25 byte P2PKH script, got %d", len(script))
	}
}

func TestBitcoinCashShareDiffMultiplier(t *testing.T) {
	coin := NewBitcoinCashCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func TestBitcoinCashRegistry(t *testing.T) {
	// BCH should be registered via init()
	if !IsRegistered("BCH") {
		t.Error("expected BCH to be registered")
	}

	if !IsRegistered("BITCOINCASH") {
		t.Error("expected BITCOINCASH alias to be registered")
	}

	// Create BCH coin
	bch, err := Create("BCH")
	if err != nil {
		t.Fatalf("failed to create BCH coin: %v", err)
	}

	if bch.Symbol() != "BCH" {
		t.Errorf("expected symbol BCH, got %s", bch.Symbol())
	}

	// Test case insensitivity
	bch2, err := Create("bch")
	if err != nil {
		t.Fatalf("failed to create bch coin (lowercase): %v", err)
	}

	if bch2.Symbol() != "BCH" {
		t.Errorf("expected symbol BCH from lowercase, got %s", bch2.Symbol())
	}
}

func TestCashAddrPolymod(t *testing.T) {
	// Test the polymod function with known values
	// This validates the CashAddr checksum algorithm
	coin := NewBitcoinCashCoin()

	// A valid CashAddr should validate
	err := coin.ValidateAddress("bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a")
	if err != nil {
		t.Errorf("valid CashAddr failed validation: %v", err)
	}
}

func TestCashAddrTypes(t *testing.T) {
	coin := NewBitcoinCashCoin()

	// Test that q prefix gives P2PKH
	_, addrType, err := coin.DecodeAddress("bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a")
	if err != nil {
		t.Fatalf("failed to decode CashAddr: %v", err)
	}
	if addrType != AddressTypeP2PKH {
		t.Errorf("expected P2PKH for q-prefix, got %v", addrType)
	}

	// Test that p prefix gives P2SH
	_, addrType, err = coin.DecodeAddress("bitcoincash:ppm2qsznhks23z7629mms6s4cwef74vcwvn0h829pq")
	if err != nil {
		t.Fatalf("failed to decode CashAddr P2SH: %v", err)
	}
	if addrType != AddressTypeP2SH {
		t.Errorf("expected P2SH for p-prefix, got %v", addrType)
	}
}

// Benchmark tests

func BenchmarkBitcoinCashCashAddrValidation(b *testing.B) {
	coin := NewBitcoinCashCoin()
	address := "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkBitcoinCashLegacyValidation(b *testing.B) {
	coin := NewBitcoinCashCoin()
	address := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkBitcoinCashBlockHeaderHashing(b *testing.B) {
	coin := NewBitcoinCashCoin()
	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1d00ffff,
		Nonce:             12345,
	}
	serialized := coin.SerializeBlockHeader(header)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.HashBlockHeader(serialized)
	}
}
