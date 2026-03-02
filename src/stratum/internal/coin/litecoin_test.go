// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Litecoin tests
package coin

import (
	"strings"
	"testing"
)

func TestLitecoinBasicProperties(t *testing.T) {
	ltc := NewLitecoinCoin()

	t.Run("Symbol", func(t *testing.T) {
		if ltc.Symbol() != "LTC" {
			t.Errorf("expected LTC, got %s", ltc.Symbol())
		}
	})

	t.Run("Name", func(t *testing.T) {
		if ltc.Name() != "Litecoin" {
			t.Errorf("expected Litecoin, got %s", ltc.Name())
		}
	})

	t.Run("Algorithm", func(t *testing.T) {
		if ltc.Algorithm() != "scrypt" {
			t.Errorf("expected scrypt, got %s", ltc.Algorithm())
		}
	})

	t.Run("SupportsSegWit", func(t *testing.T) {
		if !ltc.SupportsSegWit() {
			t.Error("Litecoin should support SegWit")
		}
	})

	t.Run("BlockTime", func(t *testing.T) {
		if ltc.BlockTime() != 150 {
			t.Errorf("expected 150 seconds, got %d", ltc.BlockTime())
		}
	})

	t.Run("CoinbaseMaturity", func(t *testing.T) {
		if ltc.CoinbaseMaturity() != 100 {
			t.Errorf("expected 100, got %d", ltc.CoinbaseMaturity())
		}
	})
}

func TestLitecoinNetworkConfig(t *testing.T) {
	ltc := NewLitecoinCoin()

	t.Run("DefaultPorts", func(t *testing.T) {
		if ltc.DefaultP2PPort() != 9333 {
			t.Errorf("expected P2P port 9333, got %d", ltc.DefaultP2PPort())
		}
		if ltc.DefaultRPCPort() != 9332 {
			t.Errorf("expected RPC port 9332, got %d", ltc.DefaultRPCPort())
		}
	})

	t.Run("VersionBytes", func(t *testing.T) {
		if ltc.P2PKHVersionByte() != 0x30 {
			t.Errorf("expected P2PKH version 0x30, got 0x%02x", ltc.P2PKHVersionByte())
		}
		if ltc.P2SHVersionByte() != 0x32 {
			t.Errorf("expected P2SH version 0x32, got 0x%02x", ltc.P2SHVersionByte())
		}
	})

	t.Run("Bech32HRP", func(t *testing.T) {
		if ltc.Bech32HRP() != "ltc" {
			t.Errorf("expected ltc, got %s", ltc.Bech32HRP())
		}
	})
}

func TestLitecoinAddressValidation(t *testing.T) {
	ltc := NewLitecoinCoin()

	tests := []struct {
		name        string
		address     string
		wantType    AddressType
		shouldError bool
	}{
		// P2PKH addresses (start with L)
		{
			name:     "valid P2PKH L-address",
			address:  "LVg2kJoFNg45Nbpy53h7Fe1wKyeXVRhMH9",
			wantType: AddressTypeP2PKH,
		},
		// P2SH addresses - test legacy '3' prefix (version byte 0x05)
		// which is still accepted by Litecoin for backwards compatibility
		{
			name:     "valid legacy P2SH 3-address",
			address:  "3CWFddi6m4ndiGyKqzYvsFYagqDLPVMTzC",
			wantType: AddressTypeP2SH,
		},
		// Bech32 SegWit addresses (start with ltc1)
		{
			name:     "valid bech32 P2WPKH",
			address:  "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9",
			wantType: AddressTypeP2WPKH,
		},
		// Invalid addresses
		{
			name:        "empty address",
			address:     "",
			shouldError: true,
		},
		{
			name:        "invalid checksum",
			address:     "LVg2kJoFNg45Nbpy53h7Fe1wKyeXVRhMH0", // Changed last char
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, addrType, err := ltc.DecodeAddress(tt.address)

			if tt.shouldError {
				if err == nil {
					t.Errorf("expected error, got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if addrType != tt.wantType {
				t.Errorf("expected address type %v, got %v", tt.wantType, addrType)
			}

			if len(hash) == 0 {
				t.Error("expected non-empty hash")
			}
		})
	}
}

func TestLitecoinGenesisBlock(t *testing.T) {
	ltc := NewLitecoinCoin()

	t.Run("GenesisBlockHash", func(t *testing.T) {
		expected := "12a765e31ffd4059bada1e25190f6e98c99d9714d334efa41a195a7e7e04bfe2"
		if ltc.GenesisBlockHash() != expected {
			t.Errorf("expected %s, got %s", expected, ltc.GenesisBlockHash())
		}
	})

	t.Run("VerifyGenesisBlock_Valid", func(t *testing.T) {
		err := ltc.VerifyGenesisBlock("12a765e31ffd4059bada1e25190f6e98c99d9714d334efa41a195a7e7e04bfe2")
		if err != nil {
			t.Errorf("expected valid genesis, got error: %v", err)
		}
	})

	t.Run("VerifyGenesisBlock_Invalid", func(t *testing.T) {
		err := ltc.VerifyGenesisBlock("000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f")
		if err == nil {
			t.Error("expected error for Bitcoin genesis hash")
		}
	})
}

func TestLitecoinHashBlockHeader(t *testing.T) {
	ltc := NewLitecoinCoin()

	// Create a mock 80-byte block header
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i)
	}

	hash := ltc.HashBlockHeader(header)

	// Verify output is 32 bytes (256-bit)
	if len(hash) != 32 {
		t.Errorf("expected 32-byte hash, got %d bytes", len(hash))
	}

	// Verify deterministic output
	hash2 := ltc.HashBlockHeader(header)
	for i := range hash {
		if hash[i] != hash2[i] {
			t.Error("HashBlockHeader should be deterministic")
			break
		}
	}
}

func TestLitecoinTargetFromBits(t *testing.T) {
	ltc := NewLitecoinCoin()

	tests := []struct {
		name string
		bits uint32
	}{
		{"genesis target", 0x1e0ffff0},
		{"low difficulty", 0x1d00ffff},
		{"high difficulty", 0x1800c4eb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := ltc.TargetFromBits(tt.bits)
			if target == nil {
				t.Error("expected non-nil target")
				return
			}
			if target.Sign() < 0 {
				t.Error("target should not be negative")
			}
		})
	}

	t.Run("negative target rejected", func(t *testing.T) {
		// Bit 23 set = negative in compact encoding
		target := ltc.TargetFromBits(0x1d80ffff)
		if target.Sign() != 0 {
			t.Error("negative target should return zero")
		}
	})
}

func TestLitecoinRegistry(t *testing.T) {
	// Test that Litecoin is registered in the coin registry
	ltc, err := Create("LTC")
	if err != nil {
		t.Fatalf("failed to create LTC: %v", err)
	}

	if ltc.Symbol() != "LTC" {
		t.Errorf("expected LTC, got %s", ltc.Symbol())
	}

	// Test alias
	ltc2, err := Create("LITECOIN")
	if err != nil {
		t.Fatalf("failed to create LITECOIN: %v", err)
	}

	if ltc2.Symbol() != "LTC" {
		t.Errorf("expected LTC from alias, got %s", ltc2.Symbol())
	}
}

func TestLitecoinCoinbaseScript(t *testing.T) {
	ltc := NewLitecoinCoin()

	tests := []struct {
		name    string
		address string
		wantLen int
	}{
		{
			name:    "P2PKH address",
			address: "LVg2kJoFNg45Nbpy53h7Fe1wKyeXVRhMH9",
			wantLen: 25, // OP_DUP OP_HASH160 <20> OP_EQUALVERIFY OP_CHECKSIG
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, err := ltc.BuildCoinbaseScript(CoinbaseParams{
				PoolAddress: tt.address,
			})

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(script) != tt.wantLen {
				t.Errorf("expected script length %d, got %d", tt.wantLen, len(script))
			}
		})
	}
}

func TestLitecoinDifficultyMultiplier(t *testing.T) {
	ltc := NewLitecoinCoin()

	if ltc.ShareDifficultyMultiplier() != 65536.0 {
		t.Errorf("expected multiplier 65536.0 (Scrypt), got %f", ltc.ShareDifficultyMultiplier())
	}
}

func TestLitecoinDifferentFromBitcoin(t *testing.T) {
	ltc := NewLitecoinCoin()
	btc := NewBitcoinCoin()

	// Verify key differences
	if ltc.Algorithm() == btc.Algorithm() {
		t.Error("LTC should use different algorithm than BTC")
	}

	if ltc.BlockTime() == btc.BlockTime() {
		t.Error("LTC should have different block time than BTC")
	}

	if ltc.Bech32HRP() == btc.Bech32HRP() {
		t.Error("LTC should have different bech32 HRP than BTC")
	}

	if ltc.P2PKHVersionByte() == btc.P2PKHVersionByte() {
		t.Error("LTC should have different P2PKH version than BTC")
	}

	if strings.ToLower(ltc.GenesisBlockHash()) == strings.ToLower(btc.GenesisBlockHash()) {
		t.Error("LTC should have different genesis block than BTC")
	}
}
