// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Dogecoin tests
package coin

import (
	"strings"
	"testing"
)

func TestDogecoinBasicProperties(t *testing.T) {
	doge := NewDogecoinCoin()

	t.Run("Symbol", func(t *testing.T) {
		if doge.Symbol() != "DOGE" {
			t.Errorf("expected DOGE, got %s", doge.Symbol())
		}
	})

	t.Run("Name", func(t *testing.T) {
		if doge.Name() != "Dogecoin" {
			t.Errorf("expected Dogecoin, got %s", doge.Name())
		}
	})

	t.Run("Algorithm", func(t *testing.T) {
		if doge.Algorithm() != "scrypt" {
			t.Errorf("expected scrypt, got %s", doge.Algorithm())
		}
	})

	t.Run("SupportsSegWit", func(t *testing.T) {
		if doge.SupportsSegWit() {
			t.Error("Dogecoin should not support SegWit (as of 2024)")
		}
	})

	t.Run("BlockTime", func(t *testing.T) {
		if doge.BlockTime() != 60 {
			t.Errorf("expected 60 seconds, got %d", doge.BlockTime())
		}
	})

	t.Run("CoinbaseMaturity", func(t *testing.T) {
		if doge.CoinbaseMaturity() != 30 {
			t.Errorf("expected 30, got %d", doge.CoinbaseMaturity())
		}
	})
}

func TestDogecoinNetworkConfig(t *testing.T) {
	doge := NewDogecoinCoin()

	t.Run("DefaultPorts", func(t *testing.T) {
		if doge.DefaultP2PPort() != 22556 {
			t.Errorf("expected P2P port 22556, got %d", doge.DefaultP2PPort())
		}
		if doge.DefaultRPCPort() != 22555 {
			t.Errorf("expected RPC port 22555, got %d", doge.DefaultRPCPort())
		}
	})

	t.Run("VersionBytes", func(t *testing.T) {
		if doge.P2PKHVersionByte() != 0x1e {
			t.Errorf("expected P2PKH version 0x1e, got 0x%02x", doge.P2PKHVersionByte())
		}
		if doge.P2SHVersionByte() != 0x16 {
			t.Errorf("expected P2SH version 0x16, got 0x%02x", doge.P2SHVersionByte())
		}
	})

	t.Run("Bech32HRP", func(t *testing.T) {
		if doge.Bech32HRP() != "doge" {
			t.Errorf("expected doge, got %s", doge.Bech32HRP())
		}
	})
}

func TestDogecoinAddressValidation(t *testing.T) {
	doge := NewDogecoinCoin()

	tests := []struct {
		name        string
		address     string
		wantType    AddressType
		shouldError bool
	}{
		// P2PKH addresses (start with D)
		{
			name:     "valid P2PKH D-address",
			address:  "DLAznsPDLDRgsVcTFWRMYMG5uH6GddDtv8",
			wantType: AddressTypeP2PKH,
		},
		// P2SH addresses (start with 9 or A)
		{
			name:     "valid P2SH 9-address",
			address:  "9sLa1AKzjWuNTe1CkLh5GDYyRP9enb1Spp",
			wantType: AddressTypeP2SH,
		},
		// Invalid addresses
		{
			name:        "empty address",
			address:     "",
			shouldError: true,
		},
		{
			name:        "Bitcoin address",
			address:     "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, addrType, err := doge.DecodeAddress(tt.address)

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

func TestDogecoinGenesisBlock(t *testing.T) {
	doge := NewDogecoinCoin()

	t.Run("GenesisBlockHash", func(t *testing.T) {
		expected := "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691"
		if doge.GenesisBlockHash() != expected {
			t.Errorf("expected %s, got %s", expected, doge.GenesisBlockHash())
		}
	})

	t.Run("VerifyGenesisBlock_Valid", func(t *testing.T) {
		err := doge.VerifyGenesisBlock("1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691")
		if err != nil {
			t.Errorf("expected valid genesis, got error: %v", err)
		}
	})

	t.Run("VerifyGenesisBlock_Invalid", func(t *testing.T) {
		err := doge.VerifyGenesisBlock("000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f")
		if err == nil {
			t.Error("expected error for Bitcoin genesis hash")
		}
	})
}

func TestDogecoinHashBlockHeader(t *testing.T) {
	doge := NewDogecoinCoin()

	// Create a mock 80-byte block header
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i)
	}

	hash := doge.HashBlockHeader(header)

	// Verify output is 32 bytes (256-bit)
	if len(hash) != 32 {
		t.Errorf("expected 32-byte hash, got %d bytes", len(hash))
	}

	// Verify deterministic output
	hash2 := doge.HashBlockHeader(header)
	for i := range hash {
		if hash[i] != hash2[i] {
			t.Error("HashBlockHeader should be deterministic")
			break
		}
	}
}

func TestDogecoinTargetFromBits(t *testing.T) {
	doge := NewDogecoinCoin()

	tests := []struct {
		name string
		bits uint32
	}{
		{"low difficulty", 0x1e0ffff0},
		{"medium difficulty", 0x1d00ffff},
		{"high difficulty", 0x1800c4eb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := doge.TargetFromBits(tt.bits)
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
		target := doge.TargetFromBits(0x1d80ffff)
		if target.Sign() != 0 {
			t.Error("negative target should return zero")
		}
	})
}

func TestDogecoinRegistry(t *testing.T) {
	// Test that Dogecoin is registered in the coin registry
	doge, err := Create("DOGE")
	if err != nil {
		t.Fatalf("failed to create DOGE: %v", err)
	}

	if doge.Symbol() != "DOGE" {
		t.Errorf("expected DOGE, got %s", doge.Symbol())
	}

	// Test alias
	doge2, err := Create("DOGECOIN")
	if err != nil {
		t.Fatalf("failed to create DOGECOIN: %v", err)
	}

	if doge2.Symbol() != "DOGE" {
		t.Errorf("expected DOGE from alias, got %s", doge2.Symbol())
	}
}

func TestDogecoinCoinbaseScript(t *testing.T) {
	doge := NewDogecoinCoin()

	tests := []struct {
		name    string
		address string
		wantLen int
	}{
		{
			name:    "P2PKH address",
			address: "DLAznsPDLDRgsVcTFWRMYMG5uH6GddDtv8",
			wantLen: 25, // OP_DUP OP_HASH160 <20> OP_EQUALVERIFY OP_CHECKSIG
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, err := doge.BuildCoinbaseScript(CoinbaseParams{
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

func TestDogecoinAuxPow(t *testing.T) {
	doge := NewDogecoinCoin()

	t.Run("SupportsAuxPow", func(t *testing.T) {
		if !doge.SupportsAuxPow() {
			t.Error("Dogecoin should support AuxPoW")
		}
	})

	t.Run("AuxPowStartHeight", func(t *testing.T) {
		if doge.AuxPowStartHeight() != 371337 {
			t.Errorf("expected AuxPoW start at 371337, got %d", doge.AuxPowStartHeight())
		}
	})
}

func TestDogecoinDifficultyMultiplier(t *testing.T) {
	doge := NewDogecoinCoin()

	if doge.ShareDifficultyMultiplier() != 65536.0 {
		t.Errorf("expected multiplier 65536.0 (Scrypt), got %f", doge.ShareDifficultyMultiplier())
	}
}

func TestDogecoinSameAlgorithmAsLitecoin(t *testing.T) {
	doge := NewDogecoinCoin()
	ltc := NewLitecoinCoin()

	if doge.Algorithm() != ltc.Algorithm() {
		t.Errorf("DOGE (%s) should use same algorithm as LTC (%s)", doge.Algorithm(), ltc.Algorithm())
	}

	// Same input should produce same hash (since both use scrypt)
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i)
	}

	dogeHash := doge.HashBlockHeader(header)
	ltcHash := ltc.HashBlockHeader(header)

	for i := range dogeHash {
		if dogeHash[i] != ltcHash[i] {
			t.Error("DOGE and LTC should produce identical hashes for same input")
			break
		}
	}
}

func TestDogecoinDifferentFromBitcoin(t *testing.T) {
	doge := NewDogecoinCoin()
	btc := NewBitcoinCoin()

	// Verify key differences
	if doge.Algorithm() == btc.Algorithm() {
		t.Error("DOGE should use different algorithm than BTC")
	}

	if doge.P2PKHVersionByte() == btc.P2PKHVersionByte() {
		t.Error("DOGE should have different P2PKH version than BTC")
	}

	if strings.ToLower(doge.GenesisBlockHash()) == strings.ToLower(btc.GenesisBlockHash()) {
		t.Error("DOGE should have different genesis block than BTC")
	}

	// Different algorithm means different hash for same input
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i)
	}

	dogeHash := doge.HashBlockHeader(header)
	btcHash := btc.HashBlockHeader(header)

	same := true
	for i := range dogeHash {
		if dogeHash[i] != btcHash[i] {
			same = false
			break
		}
	}

	if same {
		t.Error("DOGE (scrypt) and BTC (sha256d) should produce different hashes")
	}
}
