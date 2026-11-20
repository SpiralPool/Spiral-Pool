// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package config

import (
	"strings"
	"testing"
)

func auxEntry(sym string) AuxChainConfig {
	return AuxChainConfig{Symbol: sym, Enabled: true, Address: "AuxPayoutAddress",
		Daemon: DaemonConfig{Host: "127.0.0.1", Port: 22555}}
}

func warningsMention(cfg *ConfigV2, s string) bool {
	for _, w := range cfg.MergeMiningWarnings {
		if strings.Contains(w, s) {
			return true
		}
	}
	return false
}

// v3.1.0: a merge-mining pair the parent cannot mine is DISABLED with a logged
// warning — the config still loads and the parent keeps mining. (Failing the
// whole V2 config would make stratum fall back to V1 parsing and not start.)
func TestV2MergeMiningPairValidation(t *testing.T) {
	cases := []struct {
		parent, aux string
		valid       bool
		why         string
	}{
		{"DGB-SCRYPT", "DOGE", true, ""},
		{"DGB-SCRYPT", "PEP", true, ""},
		{"DGB", "NMC", true, ""},
		{"DGB-SCRYPT", "NMC", false, "algorithms differ"},
		{"DGB", "DOGE", false, "algorithms differ"},
		{"DGB", "LTC", false, "does not support AuxPoW"},
	}
	for _, tc := range cases {
		t.Run(tc.parent+"+"+tc.aux, func(t *testing.T) {
			cfg := minimalValidV2Config()
			cfg.Coins[0].Symbol = tc.parent
			cfg.Coins[0].MergeMining = MergeMiningConfig{Enabled: true, AuxChains: []AuxChainConfig{auxEntry(tc.aux)}}
			err := cfg.Validate()
			if err != nil && (strings.Contains(err.Error(), "merge") || strings.Contains(err.Error(), "algorithm")) {
				t.Fatalf("merge-mining problems must not fail the config: %v", err)
			}
			mm := cfg.Coins[0].MergeMining
			if tc.valid {
				if !mm.Enabled || !mm.AuxChains[0].Enabled || len(cfg.MergeMiningWarnings) != 0 {
					t.Fatalf("%s+%s should stay enabled; warnings=%v", tc.parent, tc.aux, cfg.MergeMiningWarnings)
				}
				return
			}
			if mm.AuxChains[0].Enabled {
				t.Fatalf("%s+%s must be disabled", tc.parent, tc.aux)
			}
			if mm.Enabled {
				t.Fatalf("%s has no valid aux chain left: merge mining should be off (it mines solo)", tc.parent)
			}
			if !cfg.Coins[0].Enabled {
				t.Fatalf("the parent coin itself must keep mining")
			}
			if !warningsMention(cfg, tc.why) || !warningsMention(cfg, "DISABLED") {
				t.Fatalf("want a warning mentioning %q, got %v", tc.why, cfg.MergeMiningWarnings)
			}
		})
	}
}

func TestV2MergeMiningAuxUnderTwoParentsKeepsFirst(t *testing.T) {
	cfg := minimalValidV2Config()
	ltc := cfg.Coins[0]
	ltc.Symbol, ltc.PoolID, ltc.Stratum.Port = "LTC", "ltc_1", 7333
	dgbs := cfg.Coins[0]
	dgbs.Symbol, dgbs.PoolID, dgbs.Stratum.Port = "DGB-SCRYPT", "dgb_scrypt_1", 3336
	ltc.MergeMining = MergeMiningConfig{Enabled: true, AuxChains: []AuxChainConfig{auxEntry("DOGE")}}
	dgbs.MergeMining = MergeMiningConfig{Enabled: true, AuxChains: []AuxChainConfig{auxEntry("DOGE"), auxEntry("PEP")}}
	cfg.Coins = []CoinPoolConfig{ltc, dgbs}

	if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "merge") {
		t.Fatalf("must not fail the config: %v", err)
	}
	if !cfg.Coins[0].MergeMining.AuxChains[0].Enabled {
		t.Fatal("the first parent in the file (LTC) keeps DOGE")
	}
	dg := cfg.Coins[1].MergeMining
	if dg.AuxChains[0].Enabled {
		t.Fatal("DOGE under DGB-SCRYPT must be disabled (already under LTC)")
	}
	if !dg.AuxChains[1].Enabled || !dg.Enabled {
		t.Fatal("DGB-SCRYPT keeps merge-mining PEP")
	}
	if !warningsMention(cfg, "already merge-mined under LTC") {
		t.Fatalf("want a two-parent warning, got %v", cfg.MergeMiningWarnings)
	}
}
