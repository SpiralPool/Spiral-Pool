// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestBitcoinSilverSymbol(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	if coin.Symbol() != "BTCS" {
		t.Errorf("expected symbol BTCS, got %s", coin.Symbol())
	}

	if coin.Name() != "Bitcoin Silver" {
		t.Errorf("expected name 'Bitcoin Silver', got %s", coin.Name())
	}
}

func TestBitcoinSilverAlgorithm(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	// BTCS supports SegWit+Taproot from block 0
	if !coin.SupportsSegWit() {
		t.Error("BTCS should support SegWit")
	}
}

func TestBitcoinSilverNetworkParams(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	if coin.DefaultRPCPort() != 10567 {
		t.Errorf("expected RPC port 10567, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 10566 {
		t.Errorf("expected P2P port 10566, got %d", coin.DefaultP2PPort())
	}

	// BTCS has 5-minute block time — NOT 10 minutes
	if coin.BlockTime() != 300 {
		t.Errorf("expected block time 300 (5 min), got %d", coin.BlockTime())
	}

	// BTCS coinbase maturity is 200 — NOT Bitcoin's 100
	if coin.CoinbaseMaturity() != 200 {
		t.Errorf("expected coinbase maturity 200 (vs BTC's 100), got %d", coin.CoinbaseMaturity())
	}
}

func TestBitcoinSilverVersionBytes(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	// P2PKH version byte 0x1A produces 'B' addresses
	if coin.P2PKHVersionByte() != 0x1A {
		t.Errorf("expected P2PKH version 0x1A ('B' addresses), got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x05 {
		t.Errorf("expected P2SH version 0x05, got 0x%02x", coin.P2SHVersionByte())
	}

	// BTCS bech32 HRP is "bs" → produces bs1q... and bs1p... addresses
	if coin.Bech32HRP() != "bs" {
		t.Errorf("expected bech32 HRP 'bs', got '%s'", coin.Bech32HRP())
	}
}

func TestBitcoinSilverAddressValidation(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	tests := []struct {
		name     string
		address  string
		valid    bool
		addrType AddressType
	}{
		// P2SH (3...)
		{
			name:     "valid P2SH",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		// Invalid: wrong network bech32 (BTC bc1q should be rejected)
		{
			name:     "BTC bech32 rejected",
			address:  "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		// Invalid: empty
		{
			name:     "empty address",
			address:  "",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		// Invalid: too short
		{
			name:     "too short",
			address:  "Bvb",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		// Invalid: wrong checksum on P2SH
		{
			name:     "invalid P2SH checksum",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLz",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		// Invalid: BTC P2PKH (version 0x00, starts with '1') — BTCS P2PKH is 0x1A
		{
			name:     "BTC P2PKH rejected (wrong version byte)",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

func TestBitcoinSilverBech32Format(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	// BTCS bech32 uses "bs" HRP — verify HRP detection
	if coin.Bech32HRP() != "bs" {
		t.Fatalf("expected HRP 'bs', got '%s'", coin.Bech32HRP())
	}

	// BTC bech32 (bc1...) must be rejected
	btcAddrs := []string{
		"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
	}
	for _, addr := range btcAddrs {
		if err := coin.ValidateAddress(addr); err == nil {
			t.Errorf("BTCS must reject BTC bech32 address %s", addr)
		}
	}

	// LTC bech32 (ltc1...) must be rejected
	if err := coin.ValidateAddress("ltc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"); err == nil {
		t.Error("BTCS must reject LTC bech32 address")
	}
}

func TestBitcoinSilverBlockHeader(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1720806555, // July 12, 2024 (BTCS genesis)
		Bits:              0x1d00ffff,
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

func TestBitcoinSilverDifficulty(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	bits := uint32(0x1d00ffff)
	target := coin.TargetFromBits(bits)

	if target.Sign() == 0 {
		t.Error("expected non-zero target")
	}

	diff := coin.DifficultyFromTarget(target)
	if diff < 0.99 || diff > 1.01 {
		t.Errorf("expected difficulty ~1.0, got %.4f", diff)
	}
}

func TestBitcoinSilverCoinbaseScriptP2SH(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	params := CoinbaseParams{
		Height:          1,
		ExtraNonce:      []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:     "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
		BlockReward:     5000000000,
		CoinbaseMessage: "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build P2SH coinbase script: %v", err)
	}

	// P2SH script should be 23 bytes
	if len(script) != 23 {
		t.Errorf("expected 23 byte P2SH script, got %d", len(script))
	}

	if script[0] != 0xa9 { // OP_HASH160
		t.Errorf("expected OP_HASH160 (0xa9), got 0x%02x", script[0])
	}
}

func TestBitcoinSilverShareDiffMultiplier(t *testing.T) {
	coin := NewBitcoinSilverCoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func TestBitcoinSilverRegistry(t *testing.T) {
	if !IsRegistered("BTCS") {
		t.Error("expected BTCS to be registered")
	}

	if !IsRegistered("BITCOINSILVER") {
		t.Error("expected BITCOINSILVER alias to be registered")
	}

	btcs, err := Create("BTCS")
	if err != nil {
		t.Fatalf("failed to create BTCS coin: %v", err)
	}

	if btcs.Symbol() != "BTCS" {
		t.Errorf("expected symbol BTCS, got %s", btcs.Symbol())
	}

	// Case insensitive
	btcsLower, err := Create("btcs")
	if err != nil {
		t.Fatalf("failed to create btcs (lowercase): %v", err)
	}
	if btcsLower.Symbol() != "BTCS" {
		t.Errorf("case-insensitive: expected BTCS, got %s", btcsLower.Symbol())
	}

	// Alias
	btcsAlias, err := Create("BITCOINSILVER")
	if err != nil {
		t.Fatalf("failed to create by BITCOINSILVER alias: %v", err)
	}
	if btcsAlias.Symbol() != "BTCS" {
		t.Errorf("alias lookup: expected BTCS, got %s", btcsAlias.Symbol())
	}
}

func TestBitcoinSilverGenesisHash(t *testing.T) {
	expected := "00000ea8e97e04892a03df35947ff0c4df705723f5b18be7cc6456ed16e9788e"
	if BTCSGenesisBlockHash != expected {
		t.Errorf("expected genesis hash %s, got %s", expected, BTCSGenesisBlockHash)
	}
}

func TestBitcoinSilverBlockTimeIsNotTenMin(t *testing.T) {
	// BTCS has 5-minute blocks (300s), NOT 10-minute (600s) like BTC/BCH/BCH2/BC2.
	// This test exists to catch accidental copy-paste errors from those coins.
	coin := NewBitcoinSilverCoin()
	if coin.BlockTime() == 600 {
		t.Error("BTCS block time should be 300s (5 min), not 600s — likely a copy-paste error from BTC/BCH")
	}
}

func TestBitcoinSilverMaturityIsNotOneHundred(t *testing.T) {
	// BTCS coinbase maturity is 200, NOT 100 like BTC/BCH/BCH2/BC2.
	// This test exists to catch accidental copy-paste errors from those coins.
	coin := NewBitcoinSilverCoin()
	if coin.CoinbaseMaturity() == 100 {
		t.Error("BTCS coinbase maturity should be 200, not 100 — likely a copy-paste error from BTC/BCH")
	}
}

func BenchmarkBitcoinSilverAddressValidation(b *testing.B) {
	coin := NewBitcoinSilverCoin()
	addr := "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = coin.ValidateAddress(addr)
	}
}

func BenchmarkBitcoinSilverHashBlockHeader(b *testing.B) {
	coin := NewBitcoinSilverCoin()
	header := make([]byte, 80)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = coin.HashBlockHeader(header)
	}
}
