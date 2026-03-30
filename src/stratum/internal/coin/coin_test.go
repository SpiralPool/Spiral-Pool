// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Tests for the coin interface and abstractions.
//
// This tests the core Coin interface, address types, block header structures,
// and coinbase parameter handling that all coin implementations must support.
package coin

import (
	"math/big"
	"testing"
)

// =============================================================================
// ADDRESS TYPE TESTS
// =============================================================================

func TestAddressType_String(t *testing.T) {
	tests := []struct {
		addrType AddressType
		want     string
	}{
		{AddressTypeUnknown, "Unknown"},
		{AddressTypeP2PKH, "P2PKH"},
		{AddressTypeP2SH, "P2SH"},
		{AddressTypeP2WPKH, "P2WPKH"},
		{AddressTypeP2WSH, "P2WSH"},
		{AddressTypeP2TR, "P2TR"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.addrType.String()
			if got != tt.want {
				t.Errorf("AddressType(%d).String() = %q, want %q", tt.addrType, got, tt.want)
			}
		})
	}
}

func TestAddressType_InvalidValue(t *testing.T) {
	// Test that invalid address types return "Unknown"
	invalidTypes := []AddressType{100, 255, -1}
	for _, at := range invalidTypes {
		got := at.String()
		if got != "Unknown" {
			t.Errorf("AddressType(%d).String() = %q, want 'Unknown'", at, got)
		}
	}
}

func TestAddressType_Ordering(t *testing.T) {
	// Verify the enum ordering for deterministic behavior
	if AddressTypeUnknown != 0 {
		t.Error("AddressTypeUnknown should be 0")
	}
	if AddressTypeP2PKH != 1 {
		t.Error("AddressTypeP2PKH should be 1")
	}
	if AddressTypeP2SH != 2 {
		t.Error("AddressTypeP2SH should be 2")
	}
	if AddressTypeP2WPKH != 3 {
		t.Error("AddressTypeP2WPKH should be 3")
	}
	if AddressTypeP2WSH != 4 {
		t.Error("AddressTypeP2WSH should be 4")
	}
	if AddressTypeP2TR != 5 {
		t.Error("AddressTypeP2TR should be 5")
	}
}

// =============================================================================
// BLOCK HEADER TESTS
// =============================================================================

func TestBlockHeader_Fields(t *testing.T) {
	header := BlockHeader{
		Version:           0x20000000,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1609459200, // 2021-01-01 00:00:00 UTC
		Bits:              0x1d00ffff,
		Nonce:             12345678,
	}

	if header.Version != 0x20000000 {
		t.Errorf("Version = %x, want 0x20000000", header.Version)
	}
	if len(header.PreviousBlockHash) != 32 {
		t.Errorf("PreviousBlockHash len = %d, want 32", len(header.PreviousBlockHash))
	}
	if len(header.MerkleRoot) != 32 {
		t.Errorf("MerkleRoot len = %d, want 32", len(header.MerkleRoot))
	}
	if header.Timestamp != 1609459200 {
		t.Errorf("Timestamp = %d, want 1609459200", header.Timestamp)
	}
	if header.Bits != 0x1d00ffff {
		t.Errorf("Bits = %x, want 0x1d00ffff", header.Bits)
	}
	if header.Nonce != 12345678 {
		t.Errorf("Nonce = %d, want 12345678", header.Nonce)
	}
}

func TestBlockHeader_ZeroValues(t *testing.T) {
	var header BlockHeader

	if header.Version != 0 {
		t.Error("Zero BlockHeader.Version should be 0")
	}
	if header.PreviousBlockHash != nil {
		t.Error("Zero BlockHeader.PreviousBlockHash should be nil")
	}
	if header.MerkleRoot != nil {
		t.Error("Zero BlockHeader.MerkleRoot should be nil")
	}
	if header.Timestamp != 0 {
		t.Error("Zero BlockHeader.Timestamp should be 0")
	}
	if header.Bits != 0 {
		t.Error("Zero BlockHeader.Bits should be 0")
	}
	if header.Nonce != 0 {
		t.Error("Zero BlockHeader.Nonce should be 0")
	}
}

func TestBlockHeader_VersionRolling(t *testing.T) {
	// Test version rolling masks (ASICBoost compatibility)
	baseVersion := uint32(0x20000000) // BIP9 version bit

	// Typical version rolling mask: lower 16 bits
	versionMask := uint32(0x1FFFE000)

	// Test that version rolling preserves BIP9 bits
	rolledVersions := []uint32{
		baseVersion | 0x00001234, // Low bits set
		baseVersion | 0x0000FFFF, // All low bits set
		baseVersion | 0x00000001, // Minimum
	}

	for _, v := range rolledVersions {
		// The BIP9 version bit should be preserved
		if v&0x20000000 == 0 {
			t.Errorf("Version %x lost BIP9 bit", v)
		}
		// The version mask should only affect allowed bits
		maskedChange := (v ^ baseVersion) & ^versionMask
		if maskedChange != 0 && v != baseVersion {
			// Some bits outside the mask were changed (which may be intentional)
			t.Logf("Version %x changes bits outside mask", v)
		}
	}
}

// =============================================================================
// COINBASE PARAMS TESTS
// =============================================================================

func TestCoinbaseParams_Fields(t *testing.T) {
	params := CoinbaseParams{
		Height:            123456,
		ExtraNonce:        []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:       "dgb1qtest...",
		BlockReward:       7200000000, // 72 DGB
		WitnessCommitment: make([]byte, 32),
		CoinbaseMessage:   "SpiralPool/v2.1.0/",
	}

	if params.Height != 123456 {
		t.Errorf("Height = %d, want 123456", params.Height)
	}
	if len(params.ExtraNonce) != 4 {
		t.Errorf("ExtraNonce len = %d, want 4", len(params.ExtraNonce))
	}
	if params.PoolAddress != "dgb1qtest..." {
		t.Errorf("PoolAddress = %q, want 'dgb1qtest...'", params.PoolAddress)
	}
	if params.BlockReward != 7200000000 {
		t.Errorf("BlockReward = %d, want 7200000000", params.BlockReward)
	}
	if len(params.WitnessCommitment) != 32 {
		t.Errorf("WitnessCommitment len = %d, want 32", len(params.WitnessCommitment))
	}
	if params.CoinbaseMessage != "SpiralPool/v2.1.0/" {
		t.Errorf("CoinbaseMessage = %q, want 'SpiralPool/v2.1.0/'", params.CoinbaseMessage)
	}
}

func TestCoinbaseParams_SOLOMining(t *testing.T) {
	// In SOLO mode, BlockReward is 100% to PoolAddress
	// No fee splitting, no payout addresses
	params := CoinbaseParams{
		Height:      100000,
		PoolAddress: "DGBMinerAddress123",
		BlockReward: 72160000000, // 721.6 DGB
	}

	// Document SOLO mining invariants
	t.Run("full_reward_to_miner", func(t *testing.T) {
		// The entire block reward goes to the pool address (miner in SOLO mode)
		if params.BlockReward <= 0 {
			t.Error("BlockReward must be positive")
		}
		if params.PoolAddress == "" {
			t.Error("PoolAddress must be set for SOLO mining")
		}
	})

	t.Run("no_fee_outputs", func(t *testing.T) {
		// In SOLO mode, there are no additional fee outputs
		// The coinbase has only one output to the miner
		t.Log("SOLO mode: Single coinbase output to miner address")
	})
}

func TestCoinbaseParams_BlockRewardConversion(t *testing.T) {
	tests := []struct {
		name     string
		satoshis int64
		coins    float64
	}{
		{"1_coin", 100000000, 1.0},
		{"half_coin", 50000000, 0.5},
		{"100_coins", 10000000000, 100.0},
		{"dgb_block_reward", 72160000000, 721.6},
		{"zero", 0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := CoinbaseParams{BlockReward: tt.satoshis}
			coins := float64(params.BlockReward) / 1e8
			if coins != tt.coins {
				t.Errorf("Conversion: %d satoshis = %f coins, want %f",
					tt.satoshis, coins, tt.coins)
			}
		})
	}
}

// =============================================================================
// COINBASE RESULT TESTS
// =============================================================================

func TestCoinbaseResult_Fields(t *testing.T) {
	result := CoinbaseResult{
		TxData:        make([]byte, 200),
		TxID:          make([]byte, 32),
		ScriptSig:     make([]byte, 50),
		ExtraNonceOff: 10,
		ExtraNonceLen: 8,
	}

	if len(result.TxData) != 200 {
		t.Errorf("TxData len = %d, want 200", len(result.TxData))
	}
	if len(result.TxID) != 32 {
		t.Errorf("TxID len = %d, want 32", len(result.TxID))
	}
	if len(result.ScriptSig) != 50 {
		t.Errorf("ScriptSig len = %d, want 50", len(result.ScriptSig))
	}
	if result.ExtraNonceOff != 10 {
		t.Errorf("ExtraNonceOff = %d, want 10", result.ExtraNonceOff)
	}
	if result.ExtraNonceLen != 8 {
		t.Errorf("ExtraNonceLen = %d, want 8", result.ExtraNonceLen)
	}
}

func TestCoinbaseResult_ExtraNoncePlacement(t *testing.T) {
	// Test that extranonce can be placed correctly in the coinbase
	scriptSig := make([]byte, 100)
	for i := range scriptSig {
		scriptSig[i] = 0xAA // Fill with marker bytes
	}

	result := CoinbaseResult{
		ScriptSig:     scriptSig,
		ExtraNonceOff: 20,
		ExtraNonceLen: 8,
	}

	// Verify extranonce bounds are within script
	if result.ExtraNonceOff < 0 {
		t.Error("ExtraNonceOff must be non-negative")
	}
	if result.ExtraNonceOff+result.ExtraNonceLen > len(result.ScriptSig) {
		t.Error("ExtraNonce placement exceeds ScriptSig bounds")
	}

	// Place an extranonce value
	extraNonce := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	copy(result.ScriptSig[result.ExtraNonceOff:], extraNonce)

	// Verify placement
	for i := 0; i < result.ExtraNonceLen; i++ {
		if result.ScriptSig[result.ExtraNonceOff+i] != extraNonce[i] {
			t.Errorf("ExtraNonce byte %d not placed correctly", i)
		}
	}
}

// =============================================================================
// DIFFICULTY CALCULATION TESTS
// =============================================================================

func TestDifficultyFromBits(t *testing.T) {
	// Standard Bitcoin diff1 target
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// bits 0x1d00ffff = diff1 (difficulty 1.0)
	// The formula: difficulty = diff1_target / current_target

	tests := []struct {
		name       string
		bits       uint32
		difficulty float64
		desc       string
	}{
		{"diff1", 0x1d00ffff, 1.0, "Base difficulty"},
		// Higher difficulty = lower target = harder to find
		// Lower difficulty = higher target = easier to find
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("bits=%08x: %s (difficulty ~%.1f)", tt.bits, tt.desc, tt.difficulty)
		})
	}
}

func TestShareDifficultyMultiplier(t *testing.T) {
	// Document coin-specific share difficulty multipliers
	// Most coins use 1.0, but some have different diff1 definitions

	multipliers := []struct {
		coin       string
		multiplier float64
		reason     string
	}{
		{"DGB", 1.0, "Standard Bitcoin-like difficulty"},
		{"BTC", 1.0, "Standard difficulty"},
		{"BCH", 1.0, "Standard difficulty"},
		{"LTC", 256.0, "Scrypt uses different diff1 (historical)"},
	}

	for _, m := range multipliers {
		t.Run(m.coin, func(t *testing.T) {
			if m.multiplier <= 0 {
				t.Errorf("Invalid multiplier for %s: %f", m.coin, m.multiplier)
			}
			t.Logf("%s: multiplier=%.1f (%s)", m.coin, m.multiplier, m.reason)
		})
	}
}

// =============================================================================
// COIN INTERFACE CONTRACT TESTS
// =============================================================================

func TestCoinInterfaceContract(t *testing.T) {
	// Document the Coin interface contract that all implementations must follow

	t.Run("identity_methods", func(t *testing.T) {
		// Symbol() must return uppercase ticker (e.g., "DGB", "BTC")
		// Name() must return full name (e.g., "DigiByte", "Bitcoin")
		t.Log("Symbol() returns uppercase ticker symbol")
		t.Log("Name() returns full coin name")
	})

	t.Run("address_methods", func(t *testing.T) {
		// ValidateAddress must validate address format
		// DecodeAddress must extract pubKeyHash and type
		t.Log("ValidateAddress() checks address format for coin")
		t.Log("DecodeAddress() extracts pubKeyHash and AddressType")
	})

	t.Run("block_methods", func(t *testing.T) {
		// SerializeBlockHeader returns 80 bytes for standard Bitcoin-like coins
		// HashBlockHeader computes the hash (usually SHA256d)
		t.Log("SerializeBlockHeader() returns 80 bytes (standard)")
		t.Log("HashBlockHeader() computes block hash (usually SHA256d)")
	})

	t.Run("difficulty_methods", func(t *testing.T) {
		// TargetFromBits converts compact bits to full 256-bit target
		// DifficultyFromTarget calculates human-readable difficulty
		// ShareDifficultyMultiplier returns coin-specific adjustment
		t.Log("TargetFromBits() converts compact to 256-bit target")
		t.Log("DifficultyFromTarget() returns human-readable difficulty")
		t.Log("ShareDifficultyMultiplier() returns coin-specific multiplier")
	})

	t.Run("network_methods", func(t *testing.T) {
		// Returns standard ports and address version bytes
		t.Log("DefaultRPCPort() returns daemon RPC port")
		t.Log("DefaultP2PPort() returns P2P network port")
		t.Log("P2PKHVersionByte() returns legacy address version")
		t.Log("P2SHVersionByte() returns script hash version")
		t.Log("Bech32HRP() returns bech32 human-readable part")
	})

	t.Run("mining_methods", func(t *testing.T) {
		// Mining characteristics
		t.Log("Algorithm() returns mining algorithm name")
		t.Log("SupportsSegWit() returns SegWit support flag")
		t.Log("BlockTime() returns target block time in seconds")
	})

	t.Run("coinbase_methods", func(t *testing.T) {
		// Coinbase construction requirements
		t.Log("MinCoinbaseScriptLen() returns minimum BIP34 length")
		t.Log("CoinbaseMaturity() returns confirmation requirement")
	})
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkAddressTypeString(b *testing.B) {
	types := []AddressType{
		AddressTypeP2PKH,
		AddressTypeP2SH,
		AddressTypeP2WPKH,
		AddressTypeP2WSH,
		AddressTypeP2TR,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = types[i%len(types)].String()
	}
}

func BenchmarkBlockHeaderAllocation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = BlockHeader{
			Version:           0x20000000,
			PreviousBlockHash: make([]byte, 32),
			MerkleRoot:        make([]byte, 32),
			Timestamp:         1609459200,
			Bits:              0x1d00ffff,
			Nonce:             12345678,
		}
	}
}
