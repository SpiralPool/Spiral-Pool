// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestFractalBTCSymbol(t *testing.T) {
	coin := NewFractalBTCCoin()

	if coin.Symbol() != "FBTC" {
		t.Errorf("expected symbol FBTC, got %s", coin.Symbol())
	}

	if coin.Name() != "Fractal Bitcoin" {
		t.Errorf("expected name Fractal Bitcoin, got %s", coin.Name())
	}
}

func TestFractalBTCAlgorithm(t *testing.T) {
	coin := NewFractalBTCCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	if !coin.SupportsSegWit() {
		t.Error("expected Fractal Bitcoin to support SegWit")
	}
}

func TestFractalBTCNetworkParams(t *testing.T) {
	coin := NewFractalBTCCoin()

	if coin.DefaultRPCPort() != 8340 {
		t.Errorf("expected RPC port 8340, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8341 {
		t.Errorf("expected P2P port 8341, got %d", coin.DefaultP2PPort())
	}

	// Fractal Bitcoin has 30 second block time
	if coin.BlockTime() != 30 {
		t.Errorf("expected block time 30, got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestFractalBTCAddressValidation(t *testing.T) {
	coin := NewFractalBTCCoin()

	tests := []struct {
		address  string
		valid    bool
		addrType AddressType
	}{
		// Valid bech32 P2WPKH address (bc1q...) - same as Bitcoin
		{"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq", true, AddressTypeP2WPKH},
		// Valid legacy P2PKH (1...)
		{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", true, AddressTypeP2PKH},
		// Invalid - empty
		{"", false, AddressTypeUnknown},
		// Too short
		{"bc1", false, AddressTypeUnknown},
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

func TestFractalBTCBlockHeader(t *testing.T) {
	coin := NewFractalBTCCoin()

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

func TestFractalBTCDifficulty(t *testing.T) {
	coin := NewFractalBTCCoin()

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

func TestFractalBTCCoinbaseScript(t *testing.T) {
	coin := NewFractalBTCCoin()

	params := CoinbaseParams{
		Height:            1000000,
		ExtraNonce:        []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:       "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		BlockReward:       2500000000, // 25 FB
		WitnessCommitment: nil,
		CoinbaseMessage:   "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script: %v", err)
	}

	// P2WPKH script should be 22 bytes: OP_0 (1) + PUSH 20 (1) + hash (20)
	if len(script) != 22 {
		t.Errorf("expected 22 byte P2WPKH script, got %d", len(script))
	}

	if script[0] != 0x00 {
		t.Errorf("expected OP_0 (0x00), got 0x%02x", script[0])
	}
	if script[1] != 0x14 {
		t.Errorf("expected PUSH 20 (0x14), got 0x%02x", script[1])
	}
}

func TestFractalBTCVersionBytes(t *testing.T) {
	coin := NewFractalBTCCoin()

	// Fractal Bitcoin uses same address format as Bitcoin
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

func TestFractalBTCAuxPoW(t *testing.T) {
	coin := NewFractalBTCCoin()

	if !coin.SupportsAuxPow() {
		t.Error("expected Fractal Bitcoin to support AuxPoW")
	}

	// AuxPoW enabled from genesis
	if coin.AuxPowStartHeight() != 0 {
		t.Errorf("expected AuxPoW start height 0, got %d", coin.AuxPowStartHeight())
	}

	if coin.ChainID() != 0x2024 {
		t.Errorf("expected chain ID 0x2024, got 0x%04x", coin.ChainID())
	}

	if coin.AuxPowVersionBit() != 0x00000100 {
		t.Errorf("expected AuxPow version bit 0x00000100, got 0x%08x", coin.AuxPowVersionBit())
	}
}

func TestFractalBTCGenesisBlock(t *testing.T) {
	coin := NewFractalBTCCoin()

	// Fractal Bitcoin uses the same genesis block as Bitcoin
	expectedGenesis := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
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

func TestFractalBTCRegistry(t *testing.T) {
	if !IsRegistered("FBTC") {
		t.Error("expected FBTC to be registered")
	}

	if !IsRegistered("FB") {
		t.Error("expected FB alias to be registered")
	}

	if !IsRegistered("FRACTALBTC") {
		t.Error("expected FRACTALBTC alias to be registered")
	}

	fbtc, err := Create("FBTC")
	if err != nil {
		t.Fatalf("failed to create FBTC coin: %v", err)
	}

	if fbtc.Symbol() != "FBTC" {
		t.Errorf("expected symbol FBTC, got %s", fbtc.Symbol())
	}
}

func TestFractalBTCShareDiffMultiplier(t *testing.T) {
	coin := NewFractalBTCCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func BenchmarkFractalBTCAddressValidation(b *testing.B) {
	coin := NewFractalBTCCoin()
	address := "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.ValidateAddress(address)
	}
}

func BenchmarkFractalBTCBlockHeaderHashing(b *testing.B) {
	coin := NewFractalBTCCoin()
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
