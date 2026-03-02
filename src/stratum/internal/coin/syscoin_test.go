// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestSyscoinSymbol(t *testing.T) {
	coin := NewSyscoinCoin()

	if coin.Symbol() != "SYS" {
		t.Errorf("expected symbol SYS, got %s", coin.Symbol())
	}

	if coin.Name() != "Syscoin" {
		t.Errorf("expected name Syscoin, got %s", coin.Name())
	}
}

func TestSyscoinAlgorithm(t *testing.T) {
	coin := NewSyscoinCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected Syscoin to support SegWit")
	}
}

func TestSyscoinNetworkParams(t *testing.T) {
	coin := NewSyscoinCoin()

	if coin.DefaultRPCPort() != 8370 {
		t.Errorf("expected RPC port 8370, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8369 {
		t.Errorf("expected P2P port 8369, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 150 {
		t.Errorf("expected block time 150, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestSyscoinAddressValidation(t *testing.T) {
	coin := NewSyscoinCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"sys1", false, AddressTypeUnknown},
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

func TestSyscoinBlockHeader(t *testing.T) {
	coin := NewSyscoinCoin()

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

func TestSyscoinDifficulty(t *testing.T) {
	coin := NewSyscoinCoin()

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

func TestSyscoinCoinbaseScript(t *testing.T) {
	// Skip - requires valid Syscoin address
	t.Skip("Skipping coinbase script test - requires valid Syscoin address")
}

func TestSyscoinVersionBytes(t *testing.T) {
	coin := NewSyscoinCoin()

	if coin.P2PKHVersionByte() != 0x3F {
		t.Errorf("expected P2PKH version 0x3F, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x05 {
		t.Errorf("expected P2SH version 0x05, got 0x%02x", coin.P2SHVersionByte())
	}

	if coin.Bech32HRP() != "sys" {
		t.Errorf("expected bech32 HRP 'sys', got '%s'", coin.Bech32HRP())
	}
}

func TestSyscoinAuxPoW(t *testing.T) {
	coin := NewSyscoinCoin()

	if !coin.SupportsAuxPow() {
		t.Error("expected Syscoin to support AuxPoW")
	}

	if coin.AuxPowStartHeight() != 0 {
		t.Errorf("expected AuxPoW start height 0, got %d", coin.AuxPowStartHeight())
	}

	if coin.ChainID() != 0x0010 {
		t.Errorf("expected chain ID 0x0010, got 0x%04x", coin.ChainID())
	}

	if coin.AuxPowVersionBit() != 0x00000100 {
		t.Errorf("expected AuxPow version bit 0x00000100, got 0x%08x", coin.AuxPowVersionBit())
	}
}

func TestSyscoinGenesisBlock(t *testing.T) {
	coin := NewSyscoinCoin()

	expectedGenesis := "0000022642db0346b6e01c2a397471f4f12e65d4f4251ec96c1f85367a61a7ab"
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

func TestSyscoinRegistry(t *testing.T) {
	if !IsRegistered("SYS") {
		t.Error("expected SYS to be registered")
	}

	if !IsRegistered("SYSCOIN") {
		t.Error("expected SYSCOIN alias to be registered")
	}

	sys, err := Create("SYS")
	if err != nil {
		t.Fatalf("failed to create SYS coin: %v", err)
	}

	if sys.Symbol() != "SYS" {
		t.Errorf("expected symbol SYS, got %s", sys.Symbol())
	}
}

func TestSyscoinShareDiffMultiplier(t *testing.T) {
	coin := NewSyscoinCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func BenchmarkSyscoinAddressValidation(b *testing.B) {
	coin := NewSyscoinCoin()
	address := "sys1qxvay4an52gcghxq5jkelemdj55k63sunc3mqe9"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkSyscoinBlockHeaderHashing(b *testing.B) {
	coin := NewSyscoinCoin()
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
