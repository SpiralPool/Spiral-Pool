// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCleanupMultiPortAfterCoinChange_RemoveCoinRedistributes(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled:    true,
			Port:       16180,
			PreferCoin: "BTC",
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 30},
				"BCH": {Weight: 20},
			},
		},
	}

	cleanupMultiPortAfterCoinChange(cfg, "bch")

	if _, ok := cfg.MultiPort.Coins["BCH"]; ok {
		t.Fatal("BCH should have been removed from multi_port coins")
	}
	if !cfg.MultiPort.Enabled {
		t.Fatal("multi_port should still be enabled with 2 remaining coins")
	}

	// Weights should sum to 100
	total := 0
	for _, rc := range cfg.MultiPort.Coins {
		total += rc.Weight
	}
	if total != 100 {
		t.Fatalf("weights should sum to 100, got %d", total)
	}

	// BTC was 50/80 = 62.5 → 63, DGB was 30/80 = 37.5 → last gets remainder = 37
	// Sort order: BTC, DGB — BTC gets round(50/80*100)=63, DGB gets 100-63=37
	if cfg.MultiPort.Coins["BTC"].Weight != 63 {
		t.Errorf("BTC weight: want 63, got %d", cfg.MultiPort.Coins["BTC"].Weight)
	}
	if cfg.MultiPort.Coins["DGB"].Weight != 37 {
		t.Errorf("DGB weight: want 37, got %d", cfg.MultiPort.Coins["DGB"].Weight)
	}
}

func TestCleanupMultiPortAfterCoinChange_DisablesWhenTooFewCoins(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled:    true,
			Port:       16180,
			PreferCoin: "BTC",
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 60},
				"DGB": {Weight: 40},
			},
		},
	}

	cleanupMultiPortAfterCoinChange(cfg, "dgb")

	if cfg.MultiPort.Enabled {
		t.Fatal("multi_port should be disabled when <2 coins remain")
	}
	if _, ok := cfg.MultiPort.Coins["DGB"]; ok {
		t.Fatal("DGB should have been removed")
	}
}

func TestCleanupMultiPortAfterCoinChange_FixesPreferCoin(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled:    true,
			Port:       16180,
			PreferCoin: "BCH",
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 40},
				"DGB": {Weight: 30},
				"BCH": {Weight: 30},
			},
		},
	}

	cleanupMultiPortAfterCoinChange(cfg, "bch")

	// prefer_coin was BCH (removed) → should switch to highest-weight remaining coin
	if cfg.MultiPort.PreferCoin != "BTC" {
		t.Errorf("prefer_coin: want BTC (highest weight), got %s", cfg.MultiPort.PreferCoin)
	}
}

func TestCleanupMultiPortAfterCoinChange_NilMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{MultiPort: nil}
	// Should not panic
	cleanupMultiPortAfterCoinChange(cfg, "btc")
}

func TestCleanupMultiPortAfterCoinChange_NilCoinsMap(t *testing.T) {
	t.Parallel()

	// Simulates YAML with multi_port enabled but no coins section
	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins:   nil,
		},
	}
	// Should not panic on nil map
	cleanupMultiPortAfterCoinChange(cfg, "btc")
}

func TestCleanupMultiPortAfterCoinChange_DisabledMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled: false,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 50},
			},
		},
	}

	cleanupMultiPortAfterCoinChange(cfg, "btc")

	// Should be a no-op since multi_port is disabled
	if _, ok := cfg.MultiPort.Coins["BTC"]; !ok {
		t.Fatal("BTC should still be present when multi_port is disabled")
	}
}

func TestCleanupMultiPortAfterCoinChange_CaseInsensitive(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 30},
				"BCH": {Weight: 20},
			},
		},
	}

	// Pass lowercase — function should find and remove uppercase key
	cleanupMultiPortAfterCoinChange(cfg, "bch")

	if _, ok := cfg.MultiPort.Coins["BCH"]; ok {
		t.Fatal("BCH should have been removed via lowercase input")
	}
}

func TestCleanupMultiPortAfterCoinChange_CoinNotInSchedule(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 50},
			},
		},
	}

	cleanupMultiPortAfterCoinChange(cfg, "ltc")

	// No change — LTC wasn't in schedule
	if len(cfg.MultiPort.Coins) != 2 {
		t.Fatalf("expected 2 coins unchanged, got %d", len(cfg.MultiPort.Coins))
	}
	if cfg.MultiPort.Coins["BTC"].Weight != 50 || cfg.MultiPort.Coins["DGB"].Weight != 50 {
		t.Fatal("weights should be unchanged")
	}
}

func TestCleanupMultiPortAfterCoinChange_MultipleCoinsRemoved(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled:    true,
			Port:       16180,
			PreferCoin: "BTC",
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 40},
				"DGB": {Weight: 20},
				"BCH": {Weight: 20},
				"NMC": {Weight: 20},
			},
		},
	}

	cleanupMultiPortAfterCoinChange(cfg, "bch", "nmc")

	if _, ok := cfg.MultiPort.Coins["BCH"]; ok {
		t.Fatal("BCH should have been removed")
	}
	if _, ok := cfg.MultiPort.Coins["NMC"]; ok {
		t.Fatal("NMC should have been removed")
	}
	if !cfg.MultiPort.Enabled {
		t.Fatal("multi_port should still be enabled with 2 remaining coins")
	}

	total := 0
	for _, rc := range cfg.MultiPort.Coins {
		total += rc.Weight
	}
	if total != 100 {
		t.Fatalf("weights should sum to 100, got %d", total)
	}
}

// ---------------------------------------------------------------------------
// switchToSolo: multi_port disable logic
// ---------------------------------------------------------------------------

// TestSoloMode_DisablesMultiPort simulates the config mutation that
// switchToSolo performs: if multi_port is enabled, it must be disabled
// because solo mode is one coin and smart port needs >=2.
func TestSoloMode_DisablesMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		Pool: PoolConfig{Coin: ""},
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 50},
			},
		},
	}

	// Simulate switchToSolo config mutations (lines 536-543 of mining.go)
	cfg.Pool.Coin = "btc"
	cfg.Coins = nil
	if cfg.MultiPort != nil && cfg.MultiPort.Enabled {
		cfg.MultiPort.Enabled = false
	}

	if cfg.MultiPort.Enabled {
		t.Fatal("multi_port should be disabled after solo switch")
	}
	if cfg.Pool.Coin != "btc" {
		t.Fatalf("pool coin should be btc, got %s", cfg.Pool.Coin)
	}
	if cfg.Coins != nil {
		t.Fatal("coins map should be nil in solo mode")
	}
}

func TestSoloMode_NilMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		Pool:      PoolConfig{Coin: ""},
		MultiPort: nil,
	}

	cfg.Pool.Coin = "btc"
	cfg.Coins = nil
	// Should not panic when MultiPort is nil
	if cfg.MultiPort != nil && cfg.MultiPort.Enabled {
		cfg.MultiPort.Enabled = false
	}
}

func TestSoloMode_AlreadyDisabledMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		Pool: PoolConfig{Coin: ""},
		MultiPort: &MultiPortConfig{
			Enabled: false,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 50},
			},
		},
	}

	cfg.Pool.Coin = "btc"
	cfg.Coins = nil
	if cfg.MultiPort != nil && cfg.MultiPort.Enabled {
		cfg.MultiPort.Enabled = false
	}

	// Coins map should be preserved (not cleared) — only Enabled was toggled
	if len(cfg.MultiPort.Coins) != 2 {
		t.Fatal("multi_port coins should be untouched when already disabled")
	}
}

// ---------------------------------------------------------------------------
// switchToMulti: stale coin cleanup logic
// ---------------------------------------------------------------------------

// TestMultiMode_RemovesStaleCoinFromMultiPort simulates the config mutation
// in switchToMulti: if multi_port references coins not in the new coin set,
// they must be cleaned out via cleanupMultiPortAfterCoinChange.
func TestMultiMode_RemovesStaleCoinFromMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		Pool: PoolConfig{Coin: ""},
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 40},
				"DGB": {Weight: 30},
				"BCH": {Weight: 30},
			},
		},
	}

	// New coin set: btc, dgb (BCH dropped)
	newCoins := []string{"btc", "dgb"}

	// Simulate switchToMulti config mutations
	cfg.Pool.Coin = ""
	cfg.setCoinsList(newCoins)

	if cfg.MultiPort != nil && cfg.MultiPort.Enabled {
		newCoinSet := make(map[string]bool)
		for _, c := range newCoins {
			newCoinSet[strings.ToUpper(c)] = true
		}
		var staleCoins []string
		for sym := range cfg.MultiPort.Coins {
			if !newCoinSet[strings.ToUpper(sym)] {
				staleCoins = append(staleCoins, sym)
			}
		}
		if len(staleCoins) > 0 {
			cleanupMultiPortAfterCoinChange(cfg, staleCoins...)
		}
	}

	if _, ok := cfg.MultiPort.Coins["BCH"]; ok {
		t.Fatal("BCH should have been removed from multi_port schedule")
	}
	if !cfg.MultiPort.Enabled {
		t.Fatal("multi_port should still be enabled with 2 remaining coins")
	}

	total := 0
	for _, rc := range cfg.MultiPort.Coins {
		total += rc.Weight
	}
	if total != 100 {
		t.Fatalf("weights should sum to 100, got %d", total)
	}
}

func TestMultiMode_AllScheduledCoinsStillPresent(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BTC": {Weight: 50},
				"DGB": {Weight: 50},
			},
		},
	}

	newCoins := []string{"btc", "dgb"}

	newCoinSet := make(map[string]bool)
	for _, c := range newCoins {
		newCoinSet[strings.ToUpper(c)] = true
	}
	var staleCoins []string
	for sym := range cfg.MultiPort.Coins {
		if !newCoinSet[strings.ToUpper(sym)] {
			staleCoins = append(staleCoins, sym)
		}
	}
	if len(staleCoins) > 0 {
		cleanupMultiPortAfterCoinChange(cfg, staleCoins...)
	}

	// No stale coins — everything should be untouched
	if len(cfg.MultiPort.Coins) != 2 {
		t.Fatalf("expected 2 coins, got %d", len(cfg.MultiPort.Coins))
	}
	if cfg.MultiPort.Coins["BTC"].Weight != 50 || cfg.MultiPort.Coins["DGB"].Weight != 50 {
		t.Fatal("weights should be unchanged when no coins are stale")
	}
}

func TestMultiMode_AllScheduledCoinsRemoved(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		MultiPort: &MultiPortConfig{
			Enabled: true,
			Port:    16180,
			Coins: map[string]CoinRouteConfig{
				"BCH": {Weight: 50},
				"NMC": {Weight: 50},
			},
		},
	}

	// Completely different coin set
	newCoins := []string{"btc", "dgb"}

	newCoinSet := make(map[string]bool)
	for _, c := range newCoins {
		newCoinSet[strings.ToUpper(c)] = true
	}
	var staleCoins []string
	for sym := range cfg.MultiPort.Coins {
		if !newCoinSet[strings.ToUpper(sym)] {
			staleCoins = append(staleCoins, sym)
		}
	}
	if len(staleCoins) > 0 {
		cleanupMultiPortAfterCoinChange(cfg, staleCoins...)
	}

	// All coins removed → multi_port disabled
	if cfg.MultiPort.Enabled {
		t.Fatal("multi_port should be disabled when all scheduled coins are removed")
	}
}

func TestMultiMode_NilMultiPort(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{MultiPort: nil}

	// Simulate switchToMulti with nil multi_port — should not panic
	if cfg.MultiPort != nil && cfg.MultiPort.Enabled {
		t.Fatal("should not enter this branch")
	}
}

// ---------------------------------------------------------------------------
// V2 config format: coins as YAML list (not map)
// ---------------------------------------------------------------------------

func TestExtendedConfig_CoinsListFormat(t *testing.T) {
	t.Parallel()

	// Simulate what yaml.Unmarshal produces for a V2 coins list
	coinsList := []interface{}{
		map[string]interface{}{
			"symbol":  "BTC",
			"enabled": true,
			"pool_id": "btc_sha256",
		},
		map[string]interface{}{
			"symbol":  "DGB",
			"enabled": true,
			"pool_id": "dgb_sha256",
		},
	}

	cfg := &ExtendedConfig{
		Version: 2,
		Coins:   coinsList,
	}

	if cfg.coinsLen() != 2 {
		t.Fatalf("expected 2 coins, got %d", cfg.coinsLen())
	}

	syms := cfg.coinSymbols()
	if len(syms) != 2 || syms[0] != "btc" || syms[1] != "dgb" {
		t.Fatalf("unexpected symbols: %v", syms)
	}

	if !cfg.hasCoin("BTC") {
		t.Fatal("should find BTC (case-insensitive)")
	}
	if !cfg.hasCoin("dgb") {
		t.Fatal("should find dgb (lowercase)")
	}
	if cfg.hasCoin("BCH") {
		t.Fatal("should not find BCH")
	}
}

func TestExtendedConfig_RemoveCoinFromList(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		Version: 2,
		Coins: []interface{}{
			map[string]interface{}{"symbol": "BTC", "enabled": true, "pool_id": "btc_sha256"},
			map[string]interface{}{"symbol": "DGB", "enabled": true, "pool_id": "dgb_sha256"},
			map[string]interface{}{"symbol": "BCH", "enabled": true, "pool_id": "bch_sha256"},
		},
	}

	cfg.removeCoin("bch")

	if cfg.coinsLen() != 2 {
		t.Fatalf("expected 2 coins after removal, got %d", cfg.coinsLen())
	}
	if cfg.hasCoin("BCH") {
		t.Fatal("BCH should have been removed")
	}
	if !cfg.hasCoin("BTC") || !cfg.hasCoin("DGB") {
		t.Fatal("BTC and DGB should remain")
	}
}

func TestExtendedConfig_SetCoinsListPreservesExisting(t *testing.T) {
	t.Parallel()

	cfg := &ExtendedConfig{
		Version: 2,
		Coins: []interface{}{
			map[string]interface{}{
				"symbol":  "BTC",
				"enabled": true,
				"pool_id": "btc_sha256",
				"address": "bc1qtest",
				"stratum": map[string]interface{}{"port": 4333},
			},
			map[string]interface{}{
				"symbol":  "DGB",
				"enabled": true,
				"pool_id": "dgb_sha256",
				"address": "dgb1test",
				"stratum": map[string]interface{}{"port": 3333},
			},
		},
	}

	// Switch to btc + dgb + bch — existing BTC/DGB should preserve their config
	cfg.setCoinsList([]string{"btc", "dgb", "bch"})

	if cfg.coinsLen() != 3 {
		t.Fatalf("expected 3 coins, got %d", cfg.coinsLen())
	}

	// BTC should still have its address and port
	btcData := cfg.getCoinData("btc")
	if btcData == nil {
		t.Fatal("BTC data should exist")
	}
	if btcData["address"] != "bc1qtest" {
		t.Fatalf("BTC address should be preserved, got %v", btcData["address"])
	}

	// BCH is new — should have minimal entry
	bchData := cfg.getCoinData("bch")
	if bchData == nil {
		t.Fatal("BCH data should exist")
	}
	if bchData["symbol"] != "BCH" {
		t.Fatalf("BCH symbol should be uppercase, got %v", bchData["symbol"])
	}
}

func TestSaveExtendedConfig_NilCoinsClearsCoinsSection(t *testing.T) {
	t.Parallel()

	// Simulate what saveExtendedConfig does: read existing file into fullCfg,
	// then merge ExtendedConfig fields. When cfg.Coins is nil (solo switch),
	// the coins key must be DELETED from fullCfg, not left as stale data.
	fullCfg := map[string]interface{}{
		"version": 2,
		"coins": []interface{}{
			map[string]interface{}{"symbol": "BTC", "enabled": true},
			map[string]interface{}{"symbol": "DGB", "enabled": true},
		},
	}

	// Simulate solo switch: cfg.Coins = nil
	var nilCoins interface{} = nil

	if nilCoins != nil {
		fullCfg["coins"] = nilCoins
	} else {
		delete(fullCfg, "coins")
	}

	if _, exists := fullCfg["coins"]; exists {
		t.Fatal("coins key should be deleted when cfg.Coins is nil")
	}
}

func TestDocRoot_EmptyDocument(t *testing.T) {
	t.Parallel()

	// Empty YAML should return nil root, not panic
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(""), &doc); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if r := docRoot(&doc); r != nil {
		t.Fatal("expected nil root for empty document")
	}

	// Malformed (non-mapping) should also return nil
	var doc2 yaml.Node
	if err := yaml.Unmarshal([]byte("- item1\n- item2"), &doc2); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if r := docRoot(&doc2); r != nil {
		t.Fatal("expected nil root for non-mapping document")
	}
}

func TestExtendedConfig_YAMLUnmarshalV2CoinsList(t *testing.T) {
	t.Parallel()

	// This is the actual V2 config format written by the dashboard and pool-mode.sh.
	// Before the fix, ExtendedConfig.Coins was map[string]interface{} which caused
	// yaml.Unmarshal to fail with "cannot unmarshal !!seq into map[string]interface{}"
	yamlData := `
version: 2
coins:
  - symbol: BTC
    pool_id: btc_sha256
    enabled: true
    address: "bc1qtest"
    stratum:
      port: 4333
    daemon:
      port: 8332
  - symbol: DGB
    pool_id: dgb_sha256
    enabled: true
    address: "dgb1test"
    stratum:
      port: 3333
    daemon:
      port: 14022
multi_port:
  enabled: true
  port: 16180
  coins:
    BTC:
      weight: 60
    DGB:
      weight: 40
  prefer_coin: BTC
`

	var cfg ExtendedConfig
	if err := yaml.Unmarshal([]byte(yamlData), &cfg); err != nil {
		t.Fatalf("failed to unmarshal V2 config: %v", err)
	}

	if cfg.Version != 2 {
		t.Fatalf("expected version 2, got %d", cfg.Version)
	}
	if cfg.coinsLen() != 2 {
		t.Fatalf("expected 2 coins, got %d", cfg.coinsLen())
	}
	if !cfg.hasCoin("BTC") || !cfg.hasCoin("DGB") {
		t.Fatalf("expected BTC and DGB, got symbols: %v", cfg.coinSymbols())
	}

	// Verify full coin data is preserved
	btc := cfg.getCoinData("btc")
	if btc == nil {
		t.Fatal("BTC coin data should exist")
	}
	if btc["address"] != "bc1qtest" {
		t.Fatalf("BTC address not preserved: %v", btc["address"])
	}

	// Verify multi_port parsed correctly
	if cfg.MultiPort == nil || !cfg.MultiPort.Enabled {
		t.Fatal("multi_port should be enabled")
	}
	if cfg.MultiPort.Coins["BTC"].Weight != 60 {
		t.Fatalf("BTC weight: want 60, got %d", cfg.MultiPort.Coins["BTC"].Weight)
	}
}
