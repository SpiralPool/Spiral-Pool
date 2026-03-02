// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - DigiByte Scrypt tests
package coin

import (
	"testing"
)

func TestDigiByteScryptBasicProperties(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()

	t.Run("Symbol", func(t *testing.T) {
		if dgbScrypt.Symbol() != "DGB-SCRYPT" {
			t.Errorf("expected DGB-SCRYPT, got %s", dgbScrypt.Symbol())
		}
	})

	t.Run("Name", func(t *testing.T) {
		if dgbScrypt.Name() != "DigiByte (Scrypt)" {
			t.Errorf("expected 'DigiByte (Scrypt)', got %s", dgbScrypt.Name())
		}
	})

	t.Run("Algorithm", func(t *testing.T) {
		if dgbScrypt.Algorithm() != "scrypt" {
			t.Errorf("expected scrypt, got %s", dgbScrypt.Algorithm())
		}
	})

	t.Run("SupportsSegWit", func(t *testing.T) {
		if !dgbScrypt.SupportsSegWit() {
			t.Error("DGB-Scrypt should support SegWit")
		}
	})

	t.Run("BlockTime", func(t *testing.T) {
		if dgbScrypt.BlockTime() != 15 {
			t.Errorf("expected 15 seconds, got %d", dgbScrypt.BlockTime())
		}
	})

	t.Run("SupportsMultiAlgo", func(t *testing.T) {
		dgbs, ok := dgbScrypt.(*DigiByteScryptCoin)
		if !ok {
			t.Fatal("type assertion failed")
		}
		if !dgbs.SupportsMultiAlgo() {
			t.Error("DGB-Scrypt should report multi-algo support")
		}
	})

	t.Run("MultiAlgoSwitchBlock", func(t *testing.T) {
		dgbs, ok := dgbScrypt.(*DigiByteScryptCoin)
		if !ok {
			t.Fatal("type assertion failed")
		}
		if dgbs.MultiAlgoSwitchBlock() != 145000 {
			t.Errorf("expected 145000, got %d", dgbs.MultiAlgoSwitchBlock())
		}
	})
}

func TestDigiByteScryptNetworkConfig(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()

	// Network ports should be same as regular DGB (same blockchain)
	t.Run("DefaultPorts", func(t *testing.T) {
		if dgbScrypt.DefaultP2PPort() != 12024 {
			t.Errorf("expected P2P port 12024, got %d", dgbScrypt.DefaultP2PPort())
		}
		if dgbScrypt.DefaultRPCPort() != 14022 {
			t.Errorf("expected RPC port 14022, got %d", dgbScrypt.DefaultRPCPort())
		}
	})

	// Address format should be same as regular DGB
	t.Run("VersionBytes", func(t *testing.T) {
		if dgbScrypt.P2PKHVersionByte() != 0x1e {
			t.Errorf("expected P2PKH version 0x1e, got 0x%02x", dgbScrypt.P2PKHVersionByte())
		}
		if dgbScrypt.P2SHVersionByte() != 0x3f {
			t.Errorf("expected P2SH version 0x3f, got 0x%02x", dgbScrypt.P2SHVersionByte())
		}
	})

	t.Run("Bech32HRP", func(t *testing.T) {
		if dgbScrypt.Bech32HRP() != "dgb" {
			t.Errorf("expected dgb, got %s", dgbScrypt.Bech32HRP())
		}
	})
}

func TestDigiByteScryptAddressValidation(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()
	dgbSHA := NewDigiByteCoin()

	// DGB-Scrypt should accept the same addresses as DGB-SHA256d
	// (same blockchain, different PoW algorithm)

	testAddr := "dgb1qw508d6qejxtdg4y5r3zarvary0c5xw7kgth7t5"

	_, typeScrypt, errScrypt := dgbScrypt.DecodeAddress(testAddr)
	_, typeSHA, errSHA := dgbSHA.DecodeAddress(testAddr)

	if errScrypt != nil && errSHA == nil {
		t.Errorf("DGB-Scrypt rejected address that DGB-SHA256d accepted: %v", errScrypt)
	}

	if errScrypt == nil && errSHA != nil {
		t.Errorf("DGB-SHA256d rejected address that DGB-Scrypt accepted: %v", errSHA)
	}

	if errScrypt == nil && errSHA == nil {
		if typeScrypt != typeSHA {
			t.Errorf("address types differ: Scrypt=%v, SHA=%v", typeScrypt, typeSHA)
		}
	}
}

func TestDigiByteScryptGenesisBlock(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()
	dgbSHA := NewDigiByteCoin()

	// Same genesis block as regular DGB (same blockchain)
	if dgbScrypt.GenesisBlockHash() != dgbSHA.GenesisBlockHash() {
		t.Error("DGB-Scrypt should have same genesis as DGB-SHA256d")
	}

	t.Run("VerifyGenesisBlock_Valid", func(t *testing.T) {
		err := dgbScrypt.VerifyGenesisBlock("7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496")
		if err != nil {
			t.Errorf("expected valid genesis, got error: %v", err)
		}
	})
}

func TestDigiByteScryptHashDiffers(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()
	dgbSHA := NewDigiByteCoin()

	// Create a test block header
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i * 7)
	}

	scryptHash := dgbScrypt.HashBlockHeader(header)
	shaHash := dgbSHA.HashBlockHeader(header)

	// Hashes MUST be different (Scrypt vs SHA256d)
	if len(scryptHash) != 32 {
		t.Errorf("expected 32-byte hash, got %d", len(scryptHash))
	}

	same := true
	for i := range scryptHash {
		if scryptHash[i] != shaHash[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("DGB-Scrypt and DGB-SHA256d should produce different hashes")
	}
}

func TestDigiByteScryptMatchesLitecoin(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()
	ltc := NewLitecoinCoin()

	// Both use Scrypt, so should produce identical hashes
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i * 11)
	}

	dgbHash := dgbScrypt.HashBlockHeader(header)
	ltcHash := ltc.HashBlockHeader(header)

	for i := range dgbHash {
		if dgbHash[i] != ltcHash[i] {
			t.Errorf("DGB-Scrypt and LTC should produce identical Scrypt hashes at byte %d", i)
			break
		}
	}
}

func TestDigiByteScryptRegistry(t *testing.T) {
	// Test that DGB-Scrypt is registered with multiple aliases
	tests := []string{"DGB-SCRYPT", "DIGIBYTE-SCRYPT", "DGB_SCRYPT"}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			coin, err := Create(name)
			if err != nil {
				t.Fatalf("failed to create %s: %v", name, err)
			}

			if coin.Symbol() != "DGB-SCRYPT" {
				t.Errorf("expected DGB-SCRYPT, got %s", coin.Symbol())
			}

			if coin.Algorithm() != "scrypt" {
				t.Errorf("expected scrypt algorithm, got %s", coin.Algorithm())
			}
		})
	}
}

func TestDigiByteScryptCoinbaseScript(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()
	dgbSHA := NewDigiByteCoin()

	// Should produce same coinbase scripts (same address format)
	addr := "DGmAhpDtGzEjqDjWtHD5EBNzqoKHpQfr6M"

	scryptScript, errScrypt := dgbScrypt.BuildCoinbaseScript(CoinbaseParams{PoolAddress: addr})
	shaScript, errSHA := dgbSHA.BuildCoinbaseScript(CoinbaseParams{PoolAddress: addr})

	if errScrypt != nil && errSHA == nil {
		t.Errorf("DGB-Scrypt rejected address: %v", errScrypt)
	}

	if errScrypt == nil && errSHA == nil {
		if len(scryptScript) != len(shaScript) {
			t.Error("coinbase scripts should have same length")
		}
		for i := range scryptScript {
			if scryptScript[i] != shaScript[i] {
				t.Error("coinbase scripts should be identical")
				break
			}
		}
	}
}

func TestDigiByteScryptTargetFromBits(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()

	tests := []struct {
		name string
		bits uint32
	}{
		{"low difficulty", 0x1d00ffff},
		{"medium difficulty", 0x1b0404cb},
		{"high difficulty", 0x1800c4eb},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := dgbScrypt.TargetFromBits(tt.bits)
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
		target := dgbScrypt.TargetFromBits(0x1d80ffff)
		if target.Sign() != 0 {
			t.Error("negative target should return zero")
		}
	})
}

func TestDigiByteScryptDifficultyMultiplier(t *testing.T) {
	dgbScrypt := NewDigiByteScryptCoin()

	if dgbScrypt.ShareDifficultyMultiplier() != 65536.0 {
		t.Errorf("expected multiplier 65536.0 (Scrypt), got %f", dgbScrypt.ShareDifficultyMultiplier())
	}
}

// TestDigiByteMultiAlgoAwareness verifies both DGB variants are distinguishable
func TestDigiByteMultiAlgoAwareness(t *testing.T) {
	dgbSHA := NewDigiByteCoin()
	dgbScrypt := NewDigiByteScryptCoin()

	t.Run("different algorithms", func(t *testing.T) {
		if dgbSHA.Algorithm() == dgbScrypt.Algorithm() {
			t.Error("DGB-SHA256d and DGB-Scrypt should have different algorithms")
		}
	})

	t.Run("different symbols", func(t *testing.T) {
		if dgbSHA.Symbol() == dgbScrypt.Symbol() {
			t.Error("DGB and DGB-Scrypt should have different symbols")
		}
	})

	t.Run("same genesis", func(t *testing.T) {
		if dgbSHA.GenesisBlockHash() != dgbScrypt.GenesisBlockHash() {
			t.Error("both DGB variants should have same genesis block")
		}
	})

	t.Run("same address format", func(t *testing.T) {
		if dgbSHA.Bech32HRP() != dgbScrypt.Bech32HRP() {
			t.Error("both DGB variants should use same bech32 HRP")
		}
		if dgbSHA.P2PKHVersionByte() != dgbScrypt.P2PKHVersionByte() {
			t.Error("both DGB variants should use same P2PKH version")
		}
	})
}
