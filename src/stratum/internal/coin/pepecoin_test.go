// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestPepeCoinSymbol(t *testing.T) {
	coin := NewPepeCoinCoin()

	if coin.Symbol() != "PEP" {
		t.Errorf("expected symbol PEP, got %s", coin.Symbol())
	}

	if coin.Name() != "PepeCoin" {
		t.Errorf("expected name PepeCoin, got %s", coin.Name())
	}
}

func TestPepeCoinAlgorithm(t *testing.T) {
	coin := NewPepeCoinCoin()

	if coin.Algorithm() != "scrypt" {
		t.Errorf("expected algorithm scrypt, got %s", coin.Algorithm())
	}

	// PepeCoin does not support SegWit
	if coin.SupportsSegWit() {
		t.Error("expected PepeCoin to NOT support SegWit")
	}
}

func TestPepeCoinNetworkParams(t *testing.T) {
	coin := NewPepeCoinCoin()

	if coin.DefaultRPCPort() != 33873 {
		t.Errorf("expected RPC port 33873, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 33874 {
		t.Errorf("expected P2P port 33874, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 60 {
		t.Errorf("expected block time 60, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 30 {
		t.Errorf("expected coinbase maturity 30, got %d", coin.CoinbaseMaturity())
	}
}

func TestPepeCoinAddressValidation(t *testing.T) {
	coin := NewPepeCoinCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"P1K", false, AddressTypeUnknown},
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

func TestPepeCoinBlockHeader(t *testing.T) {
	coin := NewPepeCoinCoin()

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

func TestPepeCoinDifficulty(t *testing.T) {
	coin := NewPepeCoinCoin()

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

func TestPepeCoinCoinbaseScript(t *testing.T) {
	// Skip - requires valid PepeCoin address
	t.Skip("Skipping coinbase script test - requires valid PepeCoin address")
}

func TestPepeCoinVersionBytes(t *testing.T) {
	coin := NewPepeCoinCoin()

	if coin.P2PKHVersionByte() != 0x37 {
		t.Errorf("expected P2PKH version 0x37, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x55 {
		t.Errorf("expected P2SH version 0x55, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "pep" {
		t.Errorf("expected bech32 HRP 'pep', got '%s'", coin.Bech32HRP())
	}
}

func TestPepeCoinAuxPoW(t *testing.T) {
	coin := NewPepeCoinCoin()

	if !coin.SupportsAuxPow() {
		t.Error("expected PepeCoin to support AuxPoW")
	}

	// AuxPoW enabled from genesis in Scrypt fork
	if coin.AuxPowStartHeight() != 0 {
		t.Errorf("expected AuxPoW start height 0, got %d", coin.AuxPowStartHeight())
	}

	if coin.ChainID() != 0x003F {
		t.Errorf("expected chain ID 0x003F, got 0x%04x", coin.ChainID())
	}

	if coin.AuxPowVersionBit() != 0x00000100 {
		t.Errorf("expected AuxPow version bit 0x00000100, got 0x%08x", coin.AuxPowVersionBit())
	}
}

func TestPepeCoinGenesisBlock(t *testing.T) {
	coin := NewPepeCoinCoin()

	expectedGenesis := "00008cae6a01358d774087e2daf3b2108252b0b5a440195ffec4fd38f9892272"
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

func TestPepeCoinRegistry(t *testing.T) {
	if !IsRegistered("PEP") {
		t.Error("expected PEP to be registered")
	}

	if !IsRegistered("PEPECOIN") {
		t.Error("expected PEPECOIN alias to be registered")
	}

	if !IsRegistered("MEME") {
		t.Error("expected MEME alias to be registered")
	}

	pep, err := Create("PEP")
	if err != nil {
		t.Fatalf("failed to create PEP coin: %v", err)
	}

	if pep.Symbol() != "PEP" {
		t.Errorf("expected symbol PEP, got %s", pep.Symbol())
	}
}

func TestPepeCoinShareDiffMultiplier(t *testing.T) {
	coin := NewPepeCoinCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 65536.0 {
		t.Errorf("expected multiplier 65536.0 (Scrypt 2^16 ratio), got %.2f", mult)
	}
}

func BenchmarkPepeCoinAddressValidation(b *testing.B) {
	coin := NewPepeCoinCoin()
	address := "PVjubSqRy3hy6fZHHGgzoatRdXQDwCszzq"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkPepeCoinBlockHeaderHashing(b *testing.B) {
	coin := NewPepeCoinCoin()
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
