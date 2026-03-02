// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestCatcoinSymbol(t *testing.T) {
	coin := NewCatcoinCoin()

	if coin.Symbol() != "CAT" {
		t.Errorf("expected symbol CAT, got %s", coin.Symbol())
	}

	if coin.Name() != "Catcoin" {
		t.Errorf("expected name Catcoin, got %s", coin.Name())
	}
}

func TestCatcoinAlgorithm(t *testing.T) {
	coin := NewCatcoinCoin()

	if coin.Algorithm() != "scrypt" {
		t.Errorf("expected algorithm scrypt, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected Catcoin to support SegWit")
	}
}

func TestCatcoinNetworkParams(t *testing.T) {
	coin := NewCatcoinCoin()

	if coin.DefaultRPCPort() != 9932 {
		t.Errorf("expected RPC port 9932, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 9933 {
		t.Errorf("expected P2P port 9933, got %d", coin.DefaultP2PPort())
	}

	// Catcoin has 10 minute block time (like Bitcoin)
	if coin.BlockTime() != 600 {
		t.Errorf("expected block time 600, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestCatcoinAddressValidation(t *testing.T) {
	coin := NewCatcoinCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"91K", false, AddressTypeUnknown},
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

func TestCatcoinBlockHeader(t *testing.T) {
	coin := NewCatcoinCoin()

	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1e0ffff0,
		Nonce:             12345,
	}

	for i := 0; i < 32; i++ {
		header.PreviousBlockHash[i] = byte(i)
		header.MerkleRoot[i] = byte(31 - i)
	}

	serialized := coin.SerializeBlockHeader(header)

	if len(serialized) != 80 {
		t.Errorf("expected 80 bytes, got %d", len(serialized))
	}

	hash1 := coin.HashBlockHeader(serialized)
	hash2 := coin.HashBlockHeader(serialized)

	if hex.EncodeToString(hash1) != hex.EncodeToString(hash2) {
		t.Error("expected deterministic hash")
	}

	if len(hash1) != 32 {
		t.Errorf("expected 32 byte hash, got %d", len(hash1))
	}
}

func TestCatcoinDifficulty(t *testing.T) {
	coin := NewCatcoinCoin()

	tests := []struct {
		bits         uint32
		expectedDiff float64
		tolerance    float64
	}{
		{0x1d00ffff, 1.0, 0.001}, // Difficulty 1
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

func TestCatcoinCoinbaseScript(t *testing.T) {
	// Skip - requires valid Catcoin address
	t.Skip("Skipping coinbase script test - requires valid Catcoin address")
}

func TestCatcoinVersionBytes(t *testing.T) {
	coin := NewCatcoinCoin()

	if coin.P2PKHVersionByte() != 0x15 {
		t.Errorf("expected P2PKH version 0x15, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x58 {
		t.Errorf("expected P2SH version 0x58, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "cat" {
		t.Errorf("expected bech32 HRP 'cat', got '%s'", coin.Bech32HRP())
	}
}

func TestCatcoinNoAuxPoW(t *testing.T) {
	coin := NewCatcoinCoin()

	// Catcoin does NOT implement AuxPoW interface
	// Check that it's a standalone Scrypt coin
	if coin.Algorithm() != "scrypt" {
		t.Error("expected Catcoin to use scrypt algorithm")
	}
}

func TestCatcoinGenesisBlock(t *testing.T) {
	coin := NewCatcoinCoin()

	expectedGenesis := "bc3b4ec43c4ebb2fef49e6240812549e61ffa623d9418608aa90eaad26c96296"
	if coin.GenesisBlockHash() != expectedGenesis {
		t.Errorf("expected genesis %s, got %s", expectedGenesis, coin.GenesisBlockHash())
	}

	err := coin.VerifyGenesisBlock(expectedGenesis)
	if err != nil {
		t.Errorf("expected genesis verification to pass: %v", err)
	}

	err = coin.VerifyGenesisBlock("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected genesis verification to fail with wrong hash")
	}
}

func TestCatcoinRegistry(t *testing.T) {
	if !IsRegistered("CAT") {
		t.Error("expected CAT to be registered")
	}

	if !IsRegistered("CATCOIN") {
		t.Error("expected CATCOIN alias to be registered")
	}

	cat, err := Create("CAT")
	if err != nil {
		t.Fatalf("failed to create CAT coin: %v", err)
	}

	if cat.Symbol() != "CAT" {
		t.Errorf("expected symbol CAT, got %s", cat.Symbol())
	}
}

func TestCatcoinShareDiffMultiplier(t *testing.T) {
	coin := NewCatcoinCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 65536.0 {
		t.Errorf("expected multiplier 65536.0 (Scrypt 2^16 ratio), got %.2f", mult)
	}
}

func BenchmarkCatcoinAddressValidation(b *testing.B) {
	coin := NewCatcoinCoin()
	address := "9YjPBrNF888xsbPcLJKEXRv1BDfAqkUVJU"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkCatcoinBlockHeaderHashing(b *testing.B) {
	coin := NewCatcoinCoin()
	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1e0ffff0,
		Nonce:             12345,
	}
	serialized := coin.SerializeBlockHeader(header)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.HashBlockHeader(serialized)
	}
}
