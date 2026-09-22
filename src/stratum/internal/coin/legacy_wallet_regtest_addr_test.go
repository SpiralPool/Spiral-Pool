// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import "testing"

// Addresses a real pepecoind and dogecoind regtest daemon handed back from
// getnewaddress on their legacy (pre-createwallet) wallets.
//
// Worker-name payout feeds exactly these strings back through DecodeAddress, and
// a version byte the pool disagrees with is silent: the address simply fails to
// decode and the coinbase falls back to the configured wallet. PEP has shipped
// that bug once already — it carried Peercoin's Base58 versions (0x37/0x55)
// instead of its own (0x38/0x16) — and the regtest constants below were never
// covered, because the harness check for these two coins compared the payout
// address against itself.
func TestLegacyWalletRegtestAddressesDecode(t *testing.T) {
	cases := []struct {
		name string
		coin interface {
			DecodeAddress(string) ([]byte, AddressType, error)
		}
		addrs []string
	}{
		{"pepecoin", NewPepeCoinCoin(), []string{
			"mpvN4YMyJeNKrubmRCYjadrzUNkAp2nSoZ",
			"mpM1LKvHasxewHDECzqYDsUjAjeZGkKyqT",
		}},
		{"dogecoin", NewDogecoinCoin(), []string{
			"n3xLe1aQVYkjzfCD2aZmRhuXBpwLbTxkov",
			"mvudEG5Zy6nB6FaVkttE6VxmyD2GZeuFAC",
		}},
	}
	for _, tc := range cases {
		for _, a := range tc.addrs {
			hash, typ, err := tc.coin.DecodeAddress(a)
			if err != nil {
				t.Errorf("%s DecodeAddress(%q): %v", tc.name, a, err)
				continue
			}
			if typ != AddressTypeP2PKH {
				t.Errorf("%s %q: type = %v, want P2PKH", tc.name, a, typ)
			}
			if len(hash) != 20 {
				t.Errorf("%s %q: hash len = %d, want 20", tc.name, a, len(hash))
			}
		}
	}
}

// A mainnet PEP address must not be mistaken for Peercoin's, which is the
// collision that made the original defect hard to see: versions 55 and 56 both
// render a leading 'P'.
func TestPepeCoinMainnetVersionIsNotPeercoin(t *testing.T) {
	if PepeCoinP2PKHVersion != 0x38 {
		t.Errorf("PepeCoinP2PKHVersion = %#x, want 0x38 (56) — 0x37 is Peercoin", PepeCoinP2PKHVersion)
	}
	if PepeCoinP2SHVersion != 0x16 {
		t.Errorf("PepeCoinP2SHVersion = %#x, want 0x16 (22)", PepeCoinP2SHVersion)
	}
}
