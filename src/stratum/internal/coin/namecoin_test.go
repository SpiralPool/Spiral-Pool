// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestNamecoinSymbol(t *testing.T) {
	coin := NewNamecoinCoin()

	if coin.Symbol() != "NMC" {
		t.Errorf("expected symbol NMC, got %s", coin.Symbol())
	}

	if coin.Name() != "Namecoin" {
		t.Errorf("expected name Namecoin, got %s", coin.Name())
	}
}

func TestNamecoinAlgorithm(t *testing.T) {
	coin := NewNamecoinCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected Namecoin to support SegWit")
	}
}

func TestNamecoinNetworkParams(t *testing.T) {
	coin := NewNamecoinCoin()

	if coin.DefaultRPCPort() != 8336 {
		t.Errorf("expected RPC port 8336, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8334 {
		t.Errorf("expected P2P port 8334, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 600 {
		t.Errorf("expected block time 600, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestNamecoinAddressValidation(t *testing.T) {
	coin := NewNamecoinCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Valid P2PKH address (N/M prefix) - version byte 0x34 (52)
		{"N1KHAL5C1CRzy58NdJwp1tbLze3XrkFxx9", true, AddressTypeP2PKH},
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"N1K", false, AddressTypeUnknown},
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

func TestNamecoinBlockHeader(t *testing.T) {
	coin := NewNamecoinCoin()

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

func TestNamecoinDifficulty(t *testing.T) {
	coin := NewNamecoinCoin()

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

func TestNamecoinCoinbaseScript(t *testing.T) {
	coin := NewNamecoinCoin()

	params := CoinbaseParams{
		Height:            700000,
		ExtraNonce:        []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:       "N1KHAL5C1CRzy58NdJwp1tbLze3XrkFxx9",
		BlockReward:       625000000, // 6.25 NMC
		WitnessCommitment: nil,
		CoinbaseMessage:   "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script: %v", err)
	}

	if len(script) != 25 {
		t.Errorf("expected 25 byte P2PKH script, got %d", len(script))
	}

	if script[0] != 0x76 {
		t.Errorf("expected OP_DUP (0x76), got 0x%02x", script[0])
	}
	if script[1] != 0xa9 {
		t.Errorf("expected OP_HASH160 (0xa9), got 0x%02x", script[1])
	}
	if script[24] != 0xac {
		t.Errorf("expected OP_CHECKSIG (0xac), got 0x%02x", script[24])
	}
}

func TestNamecoinVersionBytes(t *testing.T) {
	coin := NewNamecoinCoin()

	if coin.P2PKHVersionByte() != 0x34 {
		t.Errorf("expected P2PKH version 0x34, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x0D {
		t.Errorf("expected P2SH version 0x0D, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "nc" {
		t.Errorf("expected bech32 HRP 'nc', got '%s'", coin.Bech32HRP())
	}
}

func TestNamecoinAuxPoW(t *testing.T) {
	coin := NewNamecoinCoin()

	if !coin.SupportsAuxPow() {
		t.Error("expected Namecoin to support AuxPoW")
	}

	if coin.AuxPowStartHeight() != 19200 {
		t.Errorf("expected AuxPoW start height 19200, got %d", coin.AuxPowStartHeight())
	}

	if coin.ChainID() != 0x0001 {
		t.Errorf("expected chain ID 0x0001, got 0x%04x", coin.ChainID())
	}

	if coin.AuxPowVersionBit() != 0x00000100 {
		t.Errorf("expected AuxPow version bit 0x00000100, got 0x%08x", coin.AuxPowVersionBit())
	}
}

func TestNamecoinGenesisBlock(t *testing.T) {
	coin := NewNamecoinCoin()

	expectedGenesis := "000000000062b72c5e2ceb45fbc8587e807c155b0da735e6483dfba2f0a9c770"
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

func TestNamecoinRegistry(t *testing.T) {
	if !IsRegistered("NMC") {
		t.Error("expected NMC to be registered")
	}

	if !IsRegistered("NAMECOIN") {
		t.Error("expected NAMECOIN alias to be registered")
	}

	nmc, err := Create("NMC")
	if err != nil {
		t.Fatalf("failed to create NMC coin: %v", err)
	}

	if nmc.Symbol() != "NMC" {
		t.Errorf("expected symbol NMC, got %s", nmc.Symbol())
	}
}

func TestNamecoinShareDiffMultiplier(t *testing.T) {
	coin := NewNamecoinCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func BenchmarkNamecoinAddressValidation(b *testing.B) {
	coin := NewNamecoinCoin()
	address := "N1KHAL5C1CRzy58NdJwp1tbLze3XrkFxx9"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkNamecoinBlockHeaderHashing(b *testing.B) {
	coin := NewNamecoinCoin()
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
