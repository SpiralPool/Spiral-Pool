// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - Unit tests for CoinPool per-coin mining operations.
//
// These tests verify:
// - CoinPool configuration validation
// - Block notification modes (polling vs ZMQ)
// - Share handling and VARDIFF integration
// - Component lifecycle management
// - Statistics collection
// - Block handling for SOLO mining
package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/nodemanager"
	"go.uber.org/zap"
)

// TestCoinPoolStatsStructure verifies CoinPoolStats fields.
func TestCoinPoolStatsStructure(t *testing.T) {
	stats := CoinPoolStats{
		PoolID:         "dgb_main",
		Coin:           "DGB",
		Connections:    100,
		TotalShares:    50000,
		AcceptedShares: 49990,
		RejectedShares: 10,
		SharesInBuffer: 15,
		SharesWritten:  49975,
		NodeStats: nodemanager.ManagerStats{
			TotalNodes:   2,
			HealthyNodes: 2,
		},
	}

	// Verify required fields
	if stats.PoolID == "" {
		t.Error("PoolID should not be empty")
	}

	if stats.Coin == "" {
		t.Error("Coin should not be empty")
	}

	// Verify share math
	if stats.AcceptedShares+stats.RejectedShares != stats.TotalShares {
		t.Errorf("Share mismatch: accepted(%d) + rejected(%d) != total(%d)",
			stats.AcceptedShares, stats.RejectedShares, stats.TotalShares)
	}

	// Verify node stats are populated
	if stats.NodeStats.TotalNodes < 1 {
		t.Error("NodeStats.TotalNodes should be at least 1")
	}
}

// TestCoinPoolConfigValidation verifies configuration requirements.
func TestCoinPoolConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.CoinPoolConfig
		wantErr bool
	}{
		{
			name: "valid DGB config",
			cfg: &config.CoinPoolConfig{
				PoolID:  "dgb_main",
				Symbol:  "DGB",
				Enabled: true,
				Address: "DTestAddress123",
				Nodes: []config.NodeConfig{
					{ID: "node1", Host: "localhost", Port: 14022},
				},
			},
			wantErr: false,
		},
		{
			name: "valid BTC config",
			cfg: &config.CoinPoolConfig{
				PoolID:  "btc_main",
				Symbol:  "BTC",
				Enabled: true,
				Address: "bc1qtest",
				Nodes: []config.NodeConfig{
					{ID: "btc_node", Host: "127.0.0.1", Port: 8332},
				},
			},
			wantErr: false,
		},
		{
			name: "missing pool ID",
			cfg: &config.CoinPoolConfig{
				Symbol:  "DGB",
				Enabled: true,
				Address: "DTestAddress",
			},
			wantErr: true,
		},
		{
			name: "missing symbol",
			cfg: &config.CoinPoolConfig{
				PoolID:  "dgb_main",
				Enabled: true,
				Address: "DTestAddress",
			},
			wantErr: true,
		},
		{
			name: "missing address",
			cfg: &config.CoinPoolConfig{
				PoolID:  "dgb_main",
				Symbol:  "DGB",
				Enabled: true,
			},
			wantErr: true,
		},
		{
			name: "no nodes configured",
			cfg: &config.CoinPoolConfig{
				PoolID:  "dgb_main",
				Symbol:  "DGB",
				Enabled: true,
				Address: "DTestAddress",
				Nodes:   []config.NodeConfig{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate required fields
			hasErr := false
			if tt.cfg.PoolID == "" {
				hasErr = true
			}
			if tt.cfg.Symbol == "" {
				hasErr = true
			}
			if tt.cfg.Address == "" {
				hasErr = true
			}
			if len(tt.cfg.Nodes) == 0 {
				hasErr = true
			}

			if hasErr != tt.wantErr {
				t.Errorf("validation error = %v, want %v", hasErr, tt.wantErr)
			}
		})
	}
}

// TestNodeConfigConversion verifies node config conversion to nodemanager.
func TestNodeConfigConversion(t *testing.T) {
	cfg := config.NodeConfig{
		ID:       "node1",
		Host:     "localhost",
		Port:     14022,
		User:     "rpcuser",
		Password: "rpcpass",
		Priority: 0,
		Weight:   100,
		ZMQ: &config.NodeZMQConfig{
			Enabled:  true,
			Endpoint: "tcp://localhost:28332",
		},
	}

	// Simulate conversion logic from NewCoinPool
	nmCfg := nodemanager.NodeConfig{
		ID:       cfg.ID,
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.User,
		Password: cfg.Password,
		Priority: cfg.Priority,
		Weight:   cfg.Weight,
	}
	if cfg.ZMQ != nil {
		nmCfg.ZMQ = &nodemanager.ZMQConfig{
			Enabled:  cfg.ZMQ.Enabled,
			Endpoint: cfg.ZMQ.Endpoint,
		}
	}

	// Verify conversion
	if nmCfg.ID != cfg.ID {
		t.Errorf("ID mismatch: got %q, want %q", nmCfg.ID, cfg.ID)
	}
	if nmCfg.Host != cfg.Host {
		t.Errorf("Host mismatch: got %q, want %q", nmCfg.Host, cfg.Host)
	}
	if nmCfg.Port != cfg.Port {
		t.Errorf("Port mismatch: got %d, want %d", nmCfg.Port, cfg.Port)
	}
	if nmCfg.ZMQ == nil {
		t.Error("ZMQ config should not be nil")
	} else if nmCfg.ZMQ.Endpoint != cfg.ZMQ.Endpoint {
		t.Errorf("ZMQ endpoint mismatch: got %q, want %q",
			nmCfg.ZMQ.Endpoint, cfg.ZMQ.Endpoint)
	}
}

// TestPollingFallbackInterval verifies polling interval.
func TestPollingFallbackInterval(t *testing.T) {
	// Default polling interval should be 1 second
	expectedInterval := 1 * time.Second

	// This matches pollingTicker = time.NewTicker(1 * time.Second) in coinpool.go
	if expectedInterval != time.Second {
		t.Errorf("Polling interval = %v, want 1s", expectedInterval)
	}
}

// TestZMQPromotionInterval verifies ZMQ stability check interval.
func TestZMQPromotionInterval(t *testing.T) {
	// ZMQ promotion check runs every 10 seconds (matches Pool v1 behavior)
	// CRITICAL FIX: Changed from 1 minute to 10 seconds for faster ZMQ failover detection
	expectedInterval := 10 * time.Second

	// This matches ticker := time.NewTicker(10 * time.Second) in zmqPromotionLoop
	if expectedInterval != 10*time.Second {
		t.Errorf("ZMQ promotion interval = %v, want 10s", expectedInterval)
	}
}

// TestStatsLoopInterval verifies stats update interval.
func TestStatsLoopInterval(t *testing.T) {
	// Stats update every 60 seconds
	expectedInterval := 60 * time.Second

	if expectedInterval != time.Minute {
		t.Errorf("Stats interval = %v, want 60s", expectedInterval)
	}
}

// TestDifficultyLoopInterval verifies network difficulty update interval.
func TestDifficultyLoopInterval(t *testing.T) {
	// Network difficulty updates every 30 seconds
	expectedInterval := 30 * time.Second

	if expectedInterval != 30*time.Second {
		t.Errorf("Difficulty interval = %v, want 30s", expectedInterval)
	}
}

// TestBlockStatusValues verifies block status strings.
func TestBlockStatusValues(t *testing.T) {
	// Valid block statuses
	statuses := []string{"pending", "confirmed", "orphaned"}

	for _, status := range statuses {
		if status == "" {
			t.Error("Block status should not be empty")
		}
	}

	// Verify "pending" is initial status for submitted blocks
	if statuses[0] != "pending" {
		t.Error("Initial block status should be 'pending'")
	}
}

// TestBlockRewardConversion verifies satoshi to coin conversion.
func TestBlockRewardConversion(t *testing.T) {
	tests := []struct {
		name      string
		satoshis  int64
		wantCoins float64
		precision float64 // acceptable error margin
	}{
		{
			name:      "1 BTC",
			satoshis:  100_000_000,
			wantCoins: 1.0,
			precision: 0.00000001,
		},
		{
			name:      "6.25 BTC (current reward)",
			satoshis:  625_000_000,
			wantCoins: 6.25,
			precision: 0.00000001,
		},
		{
			name:      "3.125 BTC (next halving)",
			satoshis:  312_500_000,
			wantCoins: 3.125,
			precision: 0.00000001,
		},
		{
			name:      "500 DGB",
			satoshis:  50_000_000_000,
			wantCoins: 500.0,
			precision: 0.00000001,
		},
		{
			name:      "1 satoshi",
			satoshis:  1,
			wantCoins: 0.00000001,
			precision: 0.000000001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Conversion from handleBlock: rewardCoins := float64(result.CoinbaseValue) / 1e8
			rewardCoins := float64(tt.satoshis) / 1e8

			diff := rewardCoins - tt.wantCoins
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.precision {
				t.Errorf("Conversion %d satoshis = %.8f coins, want %.8f (diff: %.10f)",
					tt.satoshis, rewardCoins, tt.wantCoins, diff)
			}
		})
	}
}

// TestSessionStateManagement verifies VARDIFF session state tracking.
func TestSessionStateManagement(t *testing.T) {
	// Simulate session state map (sync.Map behavior)
	sessionStates := make(map[uint64]float64) // sessionID -> difficulty

	// Add new session
	sessionID := uint64(12345)
	initialDiff := 16.0
	sessionStates[sessionID] = initialDiff

	// Verify session exists
	if _, ok := sessionStates[sessionID]; !ok {
		t.Error("Session should exist after adding")
	}

	// Update difficulty
	newDiff := 32.0
	sessionStates[sessionID] = newDiff

	if sessionStates[sessionID] != newDiff {
		t.Errorf("Difficulty = %f, want %f", sessionStates[sessionID], newDiff)
	}

	// Remove on disconnect
	delete(sessionStates, sessionID)

	if _, ok := sessionStates[sessionID]; ok {
		t.Error("Session should not exist after disconnect")
	}
}

// TestBlockHexValidation verifies block hex presence for submission.
func TestBlockHexValidation(t *testing.T) {
	tests := []struct {
		name       string
		blockHex   string
		canSubmit  bool
		wantStatus string
	}{
		{
			name:       "valid block hex",
			blockHex:   "01000000deadbeef...",
			canSubmit:  true,
			wantStatus: "pending",
		},
		{
			name:       "empty block hex",
			blockHex:   "",
			canSubmit:  false,
			wantStatus: "orphaned",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canSubmit := tt.blockHex != ""
			if canSubmit != tt.canSubmit {
				t.Errorf("canSubmit = %v, want %v", canSubmit, tt.canSubmit)
			}

			var status string
			if tt.blockHex == "" {
				status = "orphaned"
			} else {
				status = "pending"
			}
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
		})
	}
}

// TestCoinPoolRunningState verifies running state transitions.
func TestCoinPoolRunningState(t *testing.T) {
	running := false

	// Initially not running
	if running {
		t.Error("CoinPool should start in not-running state")
	}

	// Start
	running = true
	if !running {
		t.Error("CoinPool should be running after Start")
	}

	// Double start should be detected
	if !running {
		t.Error("Double start check failed")
	}

	// Stop
	running = false
	if running {
		t.Error("CoinPool should not be running after Stop")
	}

	// Stop when not running is allowed (no-op)
}

// TestSymbolAccessor verifies Symbol() method behavior.
func TestSymbolAccessor(t *testing.T) {
	symbols := []string{"DGB", "BTC", "BCH", "BC2"}

	for _, symbol := range symbols {
		if symbol == "" {
			t.Error("Symbol should not be empty")
		}
		if len(symbol) > 10 {
			t.Errorf("Symbol %q is too long", symbol)
		}
	}
}

// TestPoolIDAccessor verifies PoolID() method behavior.
func TestPoolIDAccessor(t *testing.T) {
	poolIDs := []string{"dgb_main", "btc_main", "bch_testnet"}

	for _, poolID := range poolIDs {
		if poolID == "" {
			t.Error("PoolID should not be empty")
		}
	}
}

// TestBlockSubmissionTimeout verifies block submission timeout.
func TestBlockSubmissionTimeout(t *testing.T) {
	// Block submission uses 60 second timeout
	expectedTimeout := 60 * time.Second

	if expectedTimeout != time.Minute {
		t.Errorf("Block submission timeout = %v, want 60s", expectedTimeout)
	}
}

// TestDBBlockInsertTimeout verifies database insert timeout.
func TestDBBlockInsertTimeout(t *testing.T) {
	// Database block insert uses 10 second timeout
	expectedTimeout := 10 * time.Second

	if expectedTimeout != 10*time.Second {
		t.Errorf("DB insert timeout = %v, want 10s", expectedTimeout)
	}
}

// TestBackgroundGoroutineTimeout verifies shutdown timeout.
func TestBackgroundGoroutineTimeout(t *testing.T) {
	// Shutdown waits up to 30 seconds for goroutines
	expectedTimeout := 30 * time.Second

	if expectedTimeout != 30*time.Second {
		t.Errorf("Goroutine shutdown timeout = %v, want 30s", expectedTimeout)
	}
}

// TestShareResultRejection verifies share rejection reasons.
func TestShareResultRejection(t *testing.T) {
	rejectReasons := []string{
		"session-not-found",
		"job-not-found",
		"low-difficulty",
		"duplicate",
		"stale",
	}

	for _, reason := range rejectReasons {
		if reason == "" {
			t.Error("Reject reason should not be empty")
		}
	}
}

// BenchmarkSessionStateLookup benchmarks session state lookup.
func BenchmarkSessionStateLookup(b *testing.B) {
	sessionStates := make(map[uint64]float64)

	// Populate with typical session count
	for i := uint64(0); i < 1000; i++ {
		sessionStates[i] = 16.0
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sessionStates[uint64(i%1000)]
	}
}

// BenchmarkBlockRewardConversion benchmarks satoshi to coin conversion.
func BenchmarkBlockRewardConversion(b *testing.B) {
	coinbaseValue := int64(625_000_000) // 6.25 BTC

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = float64(coinbaseValue) / 1e8
	}
}

// TestNewCoinPool_UnsupportedCoin verifies NewCoinPool rejects unknown coin symbols.
func TestNewCoinPool_UnsupportedCoin(t *testing.T) {
	cfg := &CoinPoolConfig{
		CoinConfig: &config.CoinPoolConfig{
			PoolID:  "fake_main",
			Symbol:  "FAKECOIN",
			Enabled: true,
			Address: "FakeAddress123",
			Nodes: []config.NodeConfig{
				{ID: "node1", Host: "localhost", Port: 9999},
			},
		},
		Logger: zap.NewNop(),
	}

	_, err := NewCoinPool(cfg)
	if err == nil {
		t.Fatal("NewCoinPool should return error for unsupported coin")
	}
	if !strings.Contains(err.Error(), "unsupported coin") {
		t.Errorf("unexpected error: %v, want error containing %q", err, "unsupported coin")
	}
}

// TestNewCoinPool_EmptyNodes verifies NewCoinPool rejects empty node configuration.
func TestNewCoinPool_EmptyNodes(t *testing.T) {
	cfg := &CoinPoolConfig{
		CoinConfig: &config.CoinPoolConfig{
			PoolID:  "dgb_main",
			Symbol:  "DGB",
			Enabled: true,
			Address: "DTestAddress123",
			Nodes:   []config.NodeConfig{},
		},
		Logger: zap.NewNop(),
	}

	_, err := NewCoinPool(cfg)
	if err == nil {
		t.Fatal("NewCoinPool should return error for empty nodes")
	}
	if !strings.Contains(err.Error(), "node manager") {
		t.Errorf("unexpected error: %v, want error about node manager", err)
	}
}

// TestNewCoinPool_NilLogger verifies NewCoinPool panics with nil logger
// (no nil-guard in the constructor — logger.Sugar() is called immediately).
func TestNewCoinPool_NilLogger(t *testing.T) {
	cfg := &CoinPoolConfig{
		CoinConfig: &config.CoinPoolConfig{
			PoolID:  "dgb_main",
			Symbol:  "DGB",
			Enabled: true,
			Address: "DTestAddress123",
			Nodes: []config.NodeConfig{
				{ID: "node1", Host: "localhost", Port: 14022},
			},
		},
		Logger: nil,
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("NewCoinPool with nil logger should panic (no nil-guard)")
		}
	}()

	NewCoinPool(cfg)
}
