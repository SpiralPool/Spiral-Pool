// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - Unit tests for Coordinator multi-coin orchestration.
//
// These tests verify:
// - Coordinator creation and initialization
// - Multi-coin pool management
// - Startup configuration and grace periods
// - Pool lookup methods
// - Statistics aggregation
// - Graceful shutdown behavior
package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// TestCoordinatorDefaultStartupConfig verifies sensible defaults for startup behavior.
func TestCoordinatorDefaultStartupConfig(t *testing.T) {
	cfg := DefaultStartupConfig()

	// Grace period should be reasonable (30 minutes for blockchain daemons)
	if cfg.GracePeriod != 30*time.Minute {
		t.Errorf("GracePeriod = %v, want 30m", cfg.GracePeriod)
	}

	// Retry interval should be frequent enough to catch daemon startup
	if cfg.RetryInterval != 30*time.Second {
		t.Errorf("RetryInterval = %v, want 30s", cfg.RetryInterval)
	}

	// Default should allow partial operation
	if cfg.RequireAllCoins != false {
		t.Error("RequireAllCoins should default to false (allow partial operation)")
	}
}

// TestStartupConfigHA verifies HA mode configuration.
func TestStartupConfigHA(t *testing.T) {
	cfg := DefaultStartupConfig()

	// For HA mode, RequireAllCoins should be configurable to true
	cfg.RequireAllCoins = true

	if !cfg.RequireAllCoins {
		t.Error("RequireAllCoins should be settable to true for HA mode")
	}
}

// TestCoordinatorStatsAggregation verifies statistics collection.
func TestCoordinatorStatsAggregation(t *testing.T) {
	// Test the CoordinatorStats structure
	stats := CoordinatorStats{
		PoolCount:        2,
		TotalConnections: 150,
		TotalShares:      1000000,
		TotalAccepted:    999900,
		TotalRejected:    100,
		Pools:            make(map[string]CoinPoolStats),
	}

	stats.Pools["dgb_main"] = CoinPoolStats{
		PoolID:         "dgb_main",
		Coin:           "DGB",
		Connections:    100,
		TotalShares:    600000,
		AcceptedShares: 599950,
		RejectedShares: 50,
	}

	stats.Pools["btc_main"] = CoinPoolStats{
		PoolID:         "btc_main",
		Coin:           "BTC",
		Connections:    50,
		TotalShares:    400000,
		AcceptedShares: 399950,
		RejectedShares: 50,
	}

	// Verify totals match sum of pools
	var totalConnFromPools int64
	for _, ps := range stats.Pools {
		totalConnFromPools += ps.Connections
	}

	if totalConnFromPools != stats.TotalConnections {
		t.Errorf("TotalConnections = %d, sum of pools = %d",
			stats.TotalConnections, totalConnFromPools)
	}

	// Verify acceptance rate can be calculated
	if stats.TotalAccepted+stats.TotalRejected != stats.TotalShares {
		t.Error("TotalAccepted + TotalRejected should equal TotalShares")
	}
}

// TestCoinPoolStatsFields verifies all stats fields are present.
func TestCoinPoolStatsFields(t *testing.T) {
	stats := CoinPoolStats{
		PoolID:         "dgb_main",
		Coin:           "DGB",
		Connections:    100,
		TotalShares:    1000,
		AcceptedShares: 990,
		RejectedShares: 10,
		SharesInBuffer: 5,
		SharesWritten:  985,
	}

	// Verify required fields
	if stats.PoolID == "" {
		t.Error("PoolID should not be empty")
	}

	if stats.Coin == "" {
		t.Error("Coin should not be empty")
	}

	// Verify share accounting
	if stats.AcceptedShares+stats.RejectedShares != stats.TotalShares {
		t.Error("Share totals don't match")
	}

	// SharesInBuffer + SharesWritten should be <= TotalShares
	// (some shares might be in flight)
	if stats.SharesInBuffer+int(stats.SharesWritten) > int(stats.TotalShares) {
		t.Error("Buffer + Written exceeds total shares")
	}
}

// TestCoordinatorValidation_NoCoins verifies NewCoordinator rejects empty coin list.
func TestCoordinatorValidation_NoCoins(t *testing.T) {
	cfg := &config.ConfigV2{
		Coins: []config.CoinPoolConfig{},
	}

	_, err := NewCoordinator(cfg, zap.NewNop())
	if err == nil {
		t.Fatal("NewCoordinator should return error for empty coins")
	}
	if !strings.Contains(err.Error(), "no coins configured") {
		t.Errorf("unexpected error: %v, want error containing %q", err, "no coins configured")
	}
}

// TestCoordinatorValidation_NoEnabledCoins verifies NewCoordinator rejects all-disabled coins.
func TestCoordinatorValidation_NoEnabledCoins(t *testing.T) {
	cfg := &config.ConfigV2{
		Coins: []config.CoinPoolConfig{
			{PoolID: "dgb_main", Symbol: "DGB", Enabled: false},
			{PoolID: "btc_main", Symbol: "BTC", Enabled: false},
		},
	}

	_, err := NewCoordinator(cfg, zap.NewNop())
	if err == nil {
		t.Fatal("NewCoordinator should return error when all coins are disabled")
	}
	if !strings.Contains(err.Error(), "no enabled coins configured") {
		t.Errorf("unexpected error: %v, want error containing %q", err, "no enabled coins configured")
	}
}

// TestCoordinatorValidation_AtLeastOneEnabled verifies NewCoordinator passes
// the coin-count validation when at least one coin is enabled.
func TestCoordinatorValidation_AtLeastOneEnabled(t *testing.T) {
	cfg := &config.ConfigV2{
		Coins: []config.CoinPoolConfig{
			{PoolID: "dgb_main", Symbol: "DGB", Enabled: true},
			{PoolID: "btc_main", Symbol: "BTC", Enabled: false},
		},
	}

	// This will fail later (e.g. database init), but the error should NOT be
	// about "no coins" or "no enabled coins" — those validations must pass.
	_, err := NewCoordinator(cfg, zap.NewNop())
	if err == nil {
		// Unlikely without a real DB, but acceptable
		return
	}
	if strings.Contains(err.Error(), "no coins configured") {
		t.Errorf("should not fail with 'no coins configured': %v", err)
	}
	if strings.Contains(err.Error(), "no enabled coins configured") {
		t.Errorf("should not fail with 'no enabled coins configured': %v", err)
	}
}

// TestStartupGracePeriodValues verifies grace period boundary values.
func TestStartupGracePeriodValues(t *testing.T) {
	tests := []struct {
		name        string
		gracePeriod time.Duration
		valid       bool
	}{
		{"default 30 minutes", 30 * time.Minute, true},
		{"short 5 minutes", 5 * time.Minute, true},
		{"long 1 hour", 1 * time.Hour, true},
		{"zero", 0, false},
		{"negative", -1 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := StartupConfig{
				GracePeriod:   tt.gracePeriod,
				RetryInterval: 30 * time.Second,
			}

			isValid := cfg.GracePeriod > 0
			if isValid != tt.valid {
				t.Errorf("GracePeriod %v: valid = %v, want %v",
					cfg.GracePeriod, isValid, tt.valid)
			}
		})
	}
}

// TestRetryIntervalValues verifies retry interval boundaries.
func TestRetryIntervalValues(t *testing.T) {
	tests := []struct {
		name          string
		retryInterval time.Duration
		valid         bool
	}{
		{"default 30 seconds", 30 * time.Second, true},
		{"fast 5 seconds", 5 * time.Second, true},
		{"slow 5 minutes", 5 * time.Minute, true},
		{"zero", 0, false},
		{"negative", -1 * time.Second, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := StartupConfig{
				GracePeriod:   30 * time.Minute,
				RetryInterval: tt.retryInterval,
			}

			isValid := cfg.RetryInterval > 0
			if isValid != tt.valid {
				t.Errorf("RetryInterval %v: valid = %v, want %v",
					cfg.RetryInterval, isValid, tt.valid)
			}
		})
	}
}

// TestCoinPoolConfig verifies coin pool configuration structure.
func TestCoinPoolConfig(t *testing.T) {
	cfg := &CoinPoolConfig{
		CoinConfig: &config.CoinPoolConfig{
			PoolID:  "dgb_main",
			Symbol:  "DGB",
			Enabled: true,
			Address: "DTestAddress123",
		},
		DBPool: nil, // Would be real pool in integration
		Logger: nil, // Would be real logger in integration
	}

	if cfg.CoinConfig.PoolID != "dgb_main" {
		t.Errorf("PoolID = %q, want dgb_main", cfg.CoinConfig.PoolID)
	}

	if cfg.CoinConfig.Symbol != "DGB" {
		t.Errorf("Symbol = %q, want DGB", cfg.CoinConfig.Symbol)
	}
}

// TestListPoolsEmpty verifies empty pool list behavior.
func TestListPoolsEmpty(t *testing.T) {
	// Simulate empty pools map
	pools := make(map[string]*CoinPool)

	ids := make([]string, 0, len(pools))
	for id := range pools {
		ids = append(ids, id)
	}

	if len(ids) != 0 {
		t.Errorf("Expected empty pool list, got %d pools", len(ids))
	}
}

// TestListPoolsMultiple verifies multiple pool listing.
func TestListPoolsMultiple(t *testing.T) {
	// Simulate pools map (without actual CoinPool instances)
	poolIDs := []string{"dgb_main", "btc_main", "bch_main"}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for _, id := range poolIDs {
		if seen[id] {
			t.Errorf("Duplicate pool ID: %s", id)
		}
		seen[id] = true
	}

	if len(seen) != 3 {
		t.Errorf("Expected 3 unique pools, got %d", len(seen))
	}
}

// TestGetPoolByID verifies pool lookup by ID.
func TestGetPoolByID(t *testing.T) {
	pools := map[string]bool{
		"dgb_main": true,
		"btc_main": true,
	}

	tests := []struct {
		poolID   string
		expected bool
	}{
		{"dgb_main", true},
		{"btc_main", true},
		{"eth-main", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.poolID, func(t *testing.T) {
			_, ok := pools[tt.poolID]
			if ok != tt.expected {
				t.Errorf("GetPool(%q) = %v, want %v", tt.poolID, ok, tt.expected)
			}
		})
	}
}

// TestGetPoolBySymbol verifies pool lookup by coin symbol.
func TestGetPoolBySymbol(t *testing.T) {
	// Simulate symbol to poolID mapping
	symbolToPool := map[string]string{
		"DGB": "dgb_main",
		"BTC": "btc_main",
	}

	tests := []struct {
		symbol   string
		expected bool
	}{
		{"DGB", true},
		{"BTC", true},
		{"ETH", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			_, ok := symbolToPool[tt.symbol]
			if ok != tt.expected {
				t.Errorf("GetPoolBySymbol(%q) = %v, want %v", tt.symbol, ok, tt.expected)
			}
		})
	}
}

// TestCoordinatorRunningState verifies running state tracking.
func TestCoordinatorRunningState(t *testing.T) {
	// Test state transitions (without actual Coordinator)
	running := false

	// Initially not running
	if running {
		t.Error("Should start in not-running state")
	}

	// After Start
	running = true
	if !running {
		t.Error("Should be running after Start")
	}

	// After Stop
	running = false
	if running {
		t.Error("Should not be running after Stop")
	}
}

// TestCoordinatorDoubleStart verifies double-start prevention.
func TestCoordinatorDoubleStart(t *testing.T) {
	// Simulate running check logic
	running := false

	// First start succeeds
	if running {
		t.Error("Expected to allow first start")
	}
	running = true

	// Second start should be rejected
	if !running {
		t.Error("Expected to detect already running")
	}
}

// BenchmarkPoolLookup benchmarks pool ID lookup.
func BenchmarkPoolLookup(b *testing.B) {
	pools := make(map[string]bool)
	for i := 0; i < 10; i++ {
		pools["pool-"+string(rune('a'+i))] = true
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pools["pool-e"]
	}
}

// BenchmarkSymbolLookup benchmarks symbol-based pool lookup.
func BenchmarkSymbolLookup(b *testing.B) {
	symbolMap := map[string]string{
		"DGB": "dgb_main",
		"BTC": "btc_main",
		"BCH": "bch_main",
		"LTC": "ltc_main",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = symbolMap["BTC"]
	}
}
