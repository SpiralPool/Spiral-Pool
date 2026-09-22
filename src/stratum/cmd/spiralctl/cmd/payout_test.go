// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// prodShapedConfig mirrors the config found on a real deployed pool: "version: 1"
// with a coins array, and a root stratum section left over from the single-coin
// layout. The pool loads this with LoadV2 (which accepts version 1 when a coins
// array is present), so it reads the per-coin key — and the CLI must write the
// per-coin key, not the root one, or the toggle would silently do nothing.
const prodShapedConfig = `global:
  api_port: 4000
pool:
  address: DTE2HHLNh1TNJzPaZb5uf6fkxxqYiLuGLB
stratum:
  listen: 0.0.0.0:3333
  versionRolling:
    enabled: true
version: 1
coins:
- symbol: DGB
  enabled: true
  address: DTE2HHLNh1TNJzPaZb5uf6fkxxqYiLuGLB
  stratum:
    port: 3333
`

// twoCoinV2Config is the shape install.sh writes for a multi-coin pool.
const twoCoinV2Config = `version: 2
coins:
- symbol: BTC
  stratum:
    port: 3333
- symbol: LTC
  stratum:
    port: 3334
`

// singleCoinV1Config has no coins array at all, so the pool falls back to
// config.Load and reads the root stratum section, which spells its keys in
// camelCase.
const singleCoinV1Config = `pool:
  address: DTE2HHLNh1TNJzPaZb5uf6fkxxqYiLuGLB
stratum:
  listen: 0.0.0.0:3333
  versionRolling:
    enabled: true
`

func parseConfig(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return &doc
}

func TestStratumSections_PicksWhereThePoolReads(t *testing.T) {
	cases := map[string]struct {
		src      string
		wantKey  string
		wantQty  int
		wantPort string // a port from the first section, to prove it is the right one
	}{
		"production shape (version 1 + coins)": {prodShapedConfig, payoutKeyV2, 1, "3333"},
		"multi-coin v2":                        {twoCoinV2Config, payoutKeyV2, 2, "3333"},
		"single-coin v1, no coins array":       {singleCoinV1Config, payoutKeyV1, 1, ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sections, key, err := stratumSections(parseConfig(t, tc.src))
			if err != nil {
				t.Fatalf("stratumSections: %v", err)
			}
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
			if len(sections) != tc.wantQty {
				t.Fatalf("got %d stratum section(s), want %d", len(sections), tc.wantQty)
			}
			if tc.wantPort == "" {
				return
			}
			var port string
			for j := 0; j < len(sections[0].Content)-1; j += 2 {
				if sections[0].Content[j].Value == "port" {
					port = sections[0].Content[j+1].Value
				}
			}
			if port != tc.wantPort {
				t.Errorf("first section has port %q, want %q — wrong stratum section", port, tc.wantPort)
			}
		})
	}
}

// TestApplyPayoutSetting_ProductionConfig is the one that matters for a live
// pool: the key has to land under the coin, in snake_case, and the root stratum
// section must be left alone.
func TestApplyPayoutSetting_ProductionConfig(t *testing.T) {
	doc := parseConfig(t, prodShapedConfig)
	n, err := applyPayoutSetting(doc, true)
	if err != nil {
		t.Fatalf("applyPayoutSetting: %v", err)
	}
	if n != 1 {
		t.Errorf("wrote %d sections, want 1", n)
	}

	out := encodeDoc(t, doc)
	if !strings.Contains(out, payoutKeyV2+": true") {
		t.Errorf("per-coin key missing from output:\n%s", out)
	}
	if strings.Contains(out, payoutKeyV1) {
		t.Errorf("wrote the v1 camelCase key into a config the pool reads as v2:\n%s", out)
	}
	// The root stratum section keeps exactly the keys it started with.
	root := docRoot(doc)
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value != "stratum" {
			continue
		}
		for j := 0; j < len(root.Content[i+1].Content)-1; j += 2 {
			if strings.HasPrefix(root.Content[i+1].Content[j].Value, "payout") {
				t.Error("root stratum section was modified; the pool would not read it")
			}
		}
	}
}

func TestApplyPayoutSetting_RoundTripAndIdempotent(t *testing.T) {
	for name, src := range map[string]string{
		"production shape": prodShapedConfig,
		"multi-coin v2":    twoCoinV2Config,
		"single-coin v1":   singleCoinV1Config,
	} {
		t.Run(name, func(t *testing.T) {
			doc := parseConfig(t, src)

			// Off is the default: an untouched config reads as off everywhere.
			if on := countEnabled(t, doc); on != 0 {
				t.Fatalf("a fresh config already reads %d section(s) as on", on)
			}

			if _, err := applyPayoutSetting(doc, true); err != nil {
				t.Fatalf("enable: %v", err)
			}
			sections, _, _ := stratumSections(doc)
			if on := countEnabled(t, doc); on != len(sections) {
				t.Errorf("after enable, %d of %d sections are on", on, len(sections))
			}

			// Enabling twice must not duplicate the key.
			if _, err := applyPayoutSetting(doc, true); err != nil {
				t.Fatalf("enable again: %v", err)
			}
			if got := strings.Count(encodeDoc(t, doc), "payout_from_worker_name"); got > len(sections) {
				t.Errorf("key appears %d times for %d sections — duplicated on re-apply", got, len(sections))
			}

			// And it switches back off.
			if _, err := applyPayoutSetting(doc, false); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if on := countEnabled(t, doc); on != 0 {
				t.Errorf("after disable, %d section(s) still on", on)
			}
		})
	}
}

func countEnabled(t *testing.T, doc *yaml.Node) int {
	t.Helper()
	sections, key, err := stratumSections(doc)
	if err != nil {
		t.Fatalf("stratumSections: %v", err)
	}
	on := 0
	for _, s := range sections {
		for j := 0; j < len(s.Content)-1; j += 2 {
			if s.Content[j].Value == key && s.Content[j+1].Value == "true" {
				on++
				break
			}
		}
	}
	return on
}

func encodeDoc(t *testing.T, doc *yaml.Node) string {
	t.Helper()
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close encoder: %v", err)
	}
	return buf.String()
}
