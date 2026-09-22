// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package config

import (
	"strings"
	"testing"
)

// multiPortTestConfig builds a minimal V2 config with a multi-coin port over the
// given coin symbols, each weighted evenly so the weights sum to 100.
func multiPortTestConfig(t *testing.T, symbols ...string) *ConfigV2 {
	t.Helper()

	addrs := map[string]string{
		"DGB":        "DAjLRZ4ZsbUcLFFtf3GGbEKWmakNTLh6aq",
		"DGB-SCRYPT": "DAjLRZ4ZsbUcLFFtf3GGbEKWmakNTLh6aq",
		"BTC":        "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
		"LTC":        "LhyLNfBkoKshT7R8Pce6vkB9T2cP2o84hx",
		"CAT":        "9rqKQNAkGKGXWWzvJHLqPJCFmDZAvkqF2v",
	}
	ports := map[string]int{"DGB": 3333, "DGB-SCRYPT": 3336, "BTC": 4333, "LTC": 7333, "CAT": 12335}

	coins := make([]CoinPoolConfig, 0, len(symbols))
	routes := make(map[string]CoinRouteConfig, len(symbols))
	weight := 100 / len(symbols)
	for i, s := range symbols {
		w := weight
		if i == len(symbols)-1 {
			w = 100 - weight*(len(symbols)-1) // absorb rounding
		}
		coins = append(coins, CoinPoolConfig{
			Symbol:  s,
			PoolID:  strings.ToLower(strings.ReplaceAll(s, "-", "_")) + "_pool",
			Enabled: true,
			Address: addrs[s],
			Stratum: CoinStratumConfig{Port: ports[s]},
			Nodes:   []NodeConfig{{ID: "primary", Host: "127.0.0.1"}},
		})
		routes[s] = CoinRouteConfig{Weight: w}
	}

	return &ConfigV2{
		Version:  2,
		Global:   GlobalConfig{APIPort: 4000, MetricsPort: 9100},
		Database: DatabaseConfig{Host: "localhost"},
		Coins:    coins,
		MultiPort: MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins:   routes,
		},
	}
}

// TestMultiPortRejectsMixedAlgorithms is the regression test for the gap this
// check closes: a scrypt coin listed alongside SHA-256d coins was accepted, and
// SHA-256d miners were then rotated onto work they cannot mine.
func TestMultiPortRejectsMixedAlgorithms(t *testing.T) {
	t.Parallel()

	cfg := multiPortTestConfig(t, "DGB", "DGB-SCRYPT")
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected mixed sha256d/scrypt multi_port.coins to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "mixes mining algorithms") {
		t.Fatalf("expected an algorithm-mixing error, got: %v", err)
	}
	for _, want := range []string{"sha256d", "scrypt", "DGB", "DGB-SCRYPT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so the operator knows which coin is wrong; got: %v", want, err)
		}
	}
}

// TestMultiPortAcceptsSingleAlgorithm guards against the check being too strict:
// a set of coins that all share one algorithm must still validate.
func TestMultiPortAcceptsSingleAlgorithm(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		symbols []string
	}{
		{"all sha256d", []string{"DGB", "BTC"}},
		{"all scrypt", []string{"LTC", "CAT"}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := multiPortTestConfig(t, tc.symbols...)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("expected %v to validate, got: %v", tc.symbols, err)
			}
		})
	}
}
