// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"testing"
)

func TestBitcoinCashIISymbol(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	if coin.Symbol() != "BCH2" {
		t.Errorf("expected symbol BCH2, got %s", coin.Symbol())
	}

	if coin.Name() != "Bitcoin Cash II" {
		t.Errorf("expected name 'Bitcoin Cash II', got %s", coin.Name())
	}
}

func TestBitcoinCashIIAlgorithm(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	if coin.Algorithm() != "sha256d" {
		t.Errorf("expected algorithm sha256d, got %s", coin.Algorithm())
	}

	// BCH2 uses BCH consensus rules — no SegWit
	if coin.SupportsSegWit() {
		t.Error("BCH2 should not support SegWit (BCH consensus rules)")
	}
}

func TestBitcoinCashIINetworkParams(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	if coin.DefaultRPCPort() != 8533 {
		t.Errorf("expected RPC port 8533, got %d", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8534 {
		t.Errorf("expected P2P port 8534, got %d", coin.DefaultP2PPort())
	}

	if coin.BlockTime() != 600 {
		t.Errorf("expected block time 600 (10 min), got %d", coin.BlockTime())
	}

	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("expected coinbase maturity 100, got %d", coin.CoinbaseMaturity())
	}
}

func TestBitcoinCashIIVersionBytes(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	if coin.P2PKHVersionByte() != 0x00 {
		t.Errorf("expected P2PKH version 0x00, got 0x%02x", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x05 {
		t.Errorf("expected P2SH version 0x05, got 0x%02x", coin.P2SHVersionByte())
	}

	// BCH2 uses CashAddr, not bech32 — Bech32HRP must be empty
	if coin.Bech32HRP() != "" {
		t.Errorf("expected empty Bech32HRP for BCH2 (uses CashAddr), got '%s'", coin.Bech32HRP())
	}
}

func TestBitcoinCashIIAddressValidation(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	tests := []struct {
		name     string
		address  string
		valid    bool
		addrType AddressType
	}{
		// CashAddr P2PKH short form (q...)
		{
			name:     "valid CashAddr P2PKH short form",
			address:  "qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		// CashAddr P2SH short form (p...)
		{
			name:     "valid CashAddr P2SH short form",
			address:  "ppm2qsznhks23z7629mms6s4cwef74vcwvn0h829pq",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		// Legacy P2PKH (1...)
		{
			name:     "valid legacy P2PKH",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		// Legacy P2SH (3...)
		{
			name:     "valid legacy P2SH",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		// Invalid cases
		{
			name:     "empty address",
			address:  "",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "too short",
			address:  "qpm",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "wrong network (BCH prefix rejected)",
			address:  "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "invalid bech32 (BCH2 does not support SegWit)",
			address:  "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
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

func TestBitcoinCashIIBlockHeader(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1733990400, // Dec 12, 2024 (BCH2 fork date)
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

func TestBitcoinCashIIDifficulty(t *testing.T) {
	coin := NewBitcoinCashIICoin()

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

func TestBitcoinCashIICoinbaseScript(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	// Test with CashAddr short form
	params := CoinbaseParams{
		Height:          53200,
		ExtraNonce:      []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:     "qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
		BlockReward:     5000000000,
		CoinbaseMessage: "Spiral Pool",
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
}

func TestBitcoinCashIICoinbaseScriptLegacy(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	params := CoinbaseParams{
		Height:          53200,
		PoolAddress:     "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
		BlockReward:     5000000000,
		CoinbaseMessage: "Spiral Pool",
	}

	script, err := coin.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("failed to build coinbase script for legacy: %v", err)
	}

	if len(script) != 25 {
		t.Errorf("expected 25 byte P2PKH script, got %d", len(script))
	}
}

func TestBitcoinCashIIShareDiffMultiplier(t *testing.T) {
	coin := NewBitcoinCashIICoin()

	mult := coin.ShareDifficultyMultiplier()
	if mult != 1.0 {
		t.Errorf("expected multiplier 1.0, got %.2f", mult)
	}
}

func TestBitcoinCashIIRegistry(t *testing.T) {
	if !IsRegistered("BCH2") {
		t.Error("expected BCH2 to be registered")
	}

	if !IsRegistered("BITCOINCASHII") {
		t.Error("expected BITCOINCASHII alias to be registered")
	}

	bch2, err := Create("BCH2")
	if err != nil {
		t.Fatalf("failed to create BCH2 coin: %v", err)
	}

	if bch2.Symbol() != "BCH2" {
		t.Errorf("expected symbol BCH2, got %s", bch2.Symbol())
	}

	// Case insensitive
	bch2lower, err := Create("bch2")
	if err != nil {
		t.Fatalf("failed to create bch2 (lowercase): %v", err)
	}
	if bch2lower.Symbol() != "BCH2" {
		t.Errorf("case-insensitive lookup: expected BCH2, got %s", bch2lower.Symbol())
	}

	// Alias
	bch2alias, err := Create("BITCOINCASHII")
	if err != nil {
		t.Fatalf("failed to create by BITCOINCASHII alias: %v", err)
	}
	if bch2alias.Symbol() != "BCH2" {
		t.Errorf("alias lookup: expected BCH2, got %s", bch2alias.Symbol())
	}
}

func TestBitcoinCashIIGenesisHash(t *testing.T) {
	// BCH2 forked from BC2 at block 53,200 — shares BC2's genesis
	expected := "0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb"
	if BCH2GenesisBlockHash != expected {
		t.Errorf("expected genesis hash %s, got %s", expected, BCH2GenesisBlockHash)
	}
}

func TestBitcoinCashIIAddressCollisionWarning(t *testing.T) {
	// BCH2 legacy addresses are byte-identical to BCH and BTC.
	// This test documents that known BTC/BCH legacy addresses pass BCH2 validation —
	// which is intentional (same version bytes), but operators must use CashAddr.
	coin := NewBitcoinCashIICoin()

	// A known BTC P2PKH address — BCH2 accepts it (same version byte 0x00)
	err := coin.ValidateAddress("1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2")
	if err != nil {
		t.Errorf("BCH2 should accept legacy P2PKH (0x00 version byte): %v", err)
	}

	// CashAddr form disambiguates the coin
	err = coin.ValidateAddress("qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a")
	if err != nil {
		t.Errorf("BCH2 should accept short CashAddr form: %v", err)
	}
}

func BenchmarkBitcoinCashIIAddressValidation(b *testing.B) {
	coin := NewBitcoinCashIICoin()
	addr := "qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = coin.ValidateAddress(addr)
	}
}

func BenchmarkBitcoinCashIIHashBlockHeader(b *testing.B) {
	coin := NewBitcoinCashIICoin()
	header := make([]byte, 80)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = coin.HashBlockHeader(header)
	}
}
