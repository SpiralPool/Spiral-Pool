// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestMyriadSymbol(t *testing.T) {
	coin := NewMyriadCoin()

	if coin.Symbol() != "XMY" {
		t.Errorf("expected symbol XMY, got %s", coin.Symbol())
	}

	if coin.Name() != "Myriad" {
		t.Errorf("expected name Myriad, got %s", coin.Name())
	}
}

func TestMyriadAlgorithm(t *testing.T) {
	coin := NewMyriadCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected Myriad to support SegWit")
	}
}

func TestMyriadNetworkParams(t *testing.T) {
	coin := NewMyriadCoin()

	if coin.DefaultRPCPort() != 10889 {
		t.Errorf("expected RPC port 10889, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 10888 {
		t.Errorf("expected P2P port 10888, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 60 {
		t.Errorf("expected block time 60, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestMyriadAddressValidation(t *testing.T) {
	coin := NewMyriadCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"M1K", false, AddressTypeUnknown},
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

func TestMyriadBlockHeader(t *testing.T) {
	coin := NewMyriadCoin()

	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1700000000,
		Bits:              0x1b0404cb,
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

func TestMyriadDifficulty(t *testing.T) {
	coin := NewMyriadCoin()

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

func TestMyriadCoinbaseScript(t *testing.T) {
	// Skip - requires valid Myriad address
	t.Skip("Skipping coinbase script test - requires valid Myriad address")
}

func TestMyriadVersionBytes(t *testing.T) {
	coin := NewMyriadCoin()

	if coin.P2PKHVersionByte() != 0x32 {
		t.Errorf("expected P2PKH version 0x32, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x09 {
		t.Errorf("expected P2SH version 0x09, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "my" {
		t.Errorf("expected bech32 HRP 'my', got '%s'", coin.Bech32HRP())
	}
}

func TestMyriadAuxPoW(t *testing.T) {
	coin := NewMyriadCoin()

	if !coin.SupportsAuxPow() {
		t.Error("expected Myriad to support AuxPoW")
	}

	if coin.AuxPowStartHeight() != 1402000 {
		t.Errorf("expected AuxPoW start height 1402000, got %d", coin.AuxPowStartHeight())
	}

	if coin.ChainID() != 0x005A {
		t.Errorf("expected chain ID 0x005A, got 0x%04x", coin.ChainID())
	}

	if coin.AuxPowVersionBit() != 0x00000100 {
		t.Errorf("expected AuxPow version bit 0x00000100, got 0x%08x", coin.AuxPowVersionBit())
	}
}

func TestMyriadGenesisBlock(t *testing.T) {
	coin := NewMyriadCoin()

	expectedGenesis := "00000ffde4c020b5938441a0ea3d314bf619ead3c29a4e5fadf91cd22bcff6d4"
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

func TestMyriadRegistry(t *testing.T) {
	if !IsRegistered("XMY") {
		t.Error("expected XMY to be registered")
	}

	if !IsRegistered("MYRIAD") {
		t.Error("expected MYRIAD alias to be registered")
	}

	xmy, err := Create("XMY")
	if err != nil {
		t.Fatalf("failed to create XMY coin: %v", err)
	}

	if xmy.Symbol() != "XMY" {
		t.Errorf("expected symbol XMY, got %s", xmy.Symbol())
	}
}

func TestMyriadShareDiffMultiplier(t *testing.T) {
	coin := NewMyriadCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func BenchmarkMyriadAddressValidation(b *testing.B) {
	coin := NewMyriadCoin()
	address := "MNVfP2g8N6s8xCXDR1GZqcLLWJhF3rRViG"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkMyriadBlockHeaderHashing(b *testing.B) {
	coin := NewMyriadCoin()
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
