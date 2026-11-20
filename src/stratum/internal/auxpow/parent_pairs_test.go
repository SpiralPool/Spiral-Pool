// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
	"go.uber.org/zap"
)

// Every merge-mining pair the pool advertises, built from the REAL coin
// implementations. The older tests used only mockParentCoin, which is how
// "DGB + NMC" shipped while auxpow.NewManager rejected DigiByte outright
// (it did not implement ParentChainCoin) and the DGB pool retried forever.
func TestNewManager_RealParentAuxPairs(t *testing.T) {
	cases := []struct {
		name   string
		parent coin.Coin
		aux    coin.AuxPowCoin
		ok     bool
	}{
		{"BTC+NMC", coin.NewBitcoinCoin(), coin.NewNamecoinCoin(), true},
		{"LTC+DOGE", coin.NewLitecoinCoin(), coin.NewDogecoinCoin(), true},
		{"LTC+PEP", coin.NewLitecoinCoin(), coin.NewPepeCoinCoin(), true},
		{"DGB+NMC", coin.NewDigiByteCoin(), coin.NewNamecoinCoin(), true},
		{"DGB-SCRYPT+DOGE", coin.NewDigiByteScryptCoin(), coin.NewDogecoinCoin(), true},
		{"DGB-SCRYPT+PEP", coin.NewDigiByteScryptCoin(), coin.NewPepeCoinCoin(), true},
		// Algorithm mismatches must be refused at startup, not mined.
		{"DGB-SCRYPT+NMC", coin.NewDigiByteScryptCoin(), coin.NewNamecoinCoin(), false},
		{"DGB+DOGE", coin.NewDigiByteCoin(), coin.NewDogecoinCoin(), false},
		{"LTC+NMC", coin.NewLitecoinCoin(), coin.NewNamecoinCoin(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AuxChainConfig{Symbol: tc.aux.Symbol(), Coin: tc.aux, Address: "x", Enabled: true}
			_, err := NewManager(tc.parent, []AuxChainConfig{cfg}, zap.NewNop())
			if tc.ok && err != nil {
				t.Fatalf("%s should be a valid pair: %v", tc.name, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("%s must be rejected (algorithm mismatch)", tc.name)
			}
		})
	}
}

// DGB-SCRYPT embeds DigiByteCoin; its CanBeParentFor must not fall through to
// the SHA-256d answer.
func TestDigiByteScryptParentOverride(t *testing.T) {
	p, ok := coin.NewDigiByteScryptCoin().(coin.ParentChainCoin)
	if !ok {
		t.Fatal("DGB-SCRYPT must implement ParentChainCoin")
	}
	if !p.CanBeParentFor("scrypt") || p.CanBeParentFor("sha256d") {
		t.Fatal("DGB-SCRYPT parents scrypt chains only")
	}
	if string(p.CoinbaseAuxMarker()) != "\xfa\xbe\x6d\x6d" || p.MaxCoinbaseAuxSize() != 44 {
		t.Fatal("standard fabe6d6d marker and 44-byte commitment expected")
	}
	d, ok := coin.NewDigiByteCoin().(coin.ParentChainCoin)
	if !ok || !d.CanBeParentFor("sha256d") || d.CanBeParentFor("scrypt") {
		t.Fatal("DGB (SHA-256d) parents sha256d chains only")
	}
}
