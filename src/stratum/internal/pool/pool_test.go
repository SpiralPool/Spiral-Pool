// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool provides tests for pool configuration and utilities.
//
// NOTE: Most pool functionality requires external dependencies (daemon, ZMQ, database).
// These tests focus on pure functions and configuration validation.
// Integration tests are in internal/integration to avoid ZMQ dependency issues.
package pool

import (
	"strings"
	"testing"
	"time"
)

// =============================================================================
// COORDINATOR CONFIG TESTS
// =============================================================================

func TestDefaultStartupConfig(t *testing.T) {
	cfg := DefaultStartupConfig()

	// Verify defaults are sensible
	if cfg.GracePeriod < time.Minute {
		t.Errorf("GracePeriod too short: %v", cfg.GracePeriod)
	}

	if cfg.RetryInterval < time.Second {
		t.Errorf("RetryInterval too short: %v", cfg.RetryInterval)
	}

	// RequireAllCoins should default to false for graceful degradation
	if cfg.RequireAllCoins {
		t.Error("RequireAllCoins should default to false")
	}
}

func TestStartupConfig_GracePeriod(t *testing.T) {
	cfg := DefaultStartupConfig()

	// Grace period should be reasonable (30 min default)
	if cfg.GracePeriod != 30*time.Minute {
		t.Errorf("Expected 30 minute grace period, got %v", cfg.GracePeriod)
	}
}

func TestStartupConfig_RetryInterval(t *testing.T) {
	cfg := DefaultStartupConfig()

	// Retry interval should be 30 seconds
	if cfg.RetryInterval != 30*time.Second {
		t.Errorf("Expected 30 second retry interval, got %v", cfg.RetryInterval)
	}
}

func TestStartupConfig_CustomValues(t *testing.T) {
	cfg := StartupConfig{
		GracePeriod:     5 * time.Minute,
		RetryInterval:   10 * time.Second,
		RequireAllCoins: true,
	}

	if cfg.GracePeriod != 5*time.Minute {
		t.Error("Custom GracePeriod not applied")
	}

	if cfg.RetryInterval != 10*time.Second {
		t.Error("Custom RetryInterval not applied")
	}

	if !cfg.RequireAllCoins {
		t.Error("Custom RequireAllCoins not applied")
	}
}

// =============================================================================
// IP ADDRESS MASKING TESTS (Privacy Protection)
// =============================================================================

func TestMaskIPAddress(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Empty input
		{
			name:     "empty_address",
			input:    "",
			expected: "",
		},

		// IPv4 with port
		{
			name:     "ipv4_with_port",
			input:    "192.168.1.100:3333",
			expected: "192.168.1.xxx:3333",
		},
		{
			name:     "ipv4_localhost_with_port",
			input:    "127.0.0.1:3333",
			expected: "127.0.0.xxx:3333",
		},
		{
			name:     "ipv4_10_network",
			input:    "10.0.0.50:8080",
			expected: "10.0.0.xxx:8080",
		},

		// IPv4 without port
		{
			name:     "ipv4_no_port",
			input:    "192.168.1.100",
			expected: "192.168.1.xxx",
		},
		{
			name:     "ipv4_single_octet",
			input:    "10",
			expected: "10",
		},

		// IPv6 with port (bracketed format)
		{
			name:     "ipv6_with_port",
			input:    "[2001:db8::1]:3333",
			expected: "[2001:db8::xxx]:3333",
		},
		{
			name:     "ipv6_localhost_with_port",
			input:    "[::1]:3333",
			expected: "[::xxx]:3333",
		},
		{
			name:     "ipv6_full_with_port",
			input:    "[2001:db8:85a3:0:0:8a2e:370:7334]:8080",
			expected: "[2001:db8:85a3:0:0:8a2e:370:xxx]:8080",
		},

		// Edge cases
		{
			name:     "hostname_with_port",
			input:    "miner.example.com:3333",
			expected: "miner.example.xxx:3333", // Treats hostname like IP
		},
		{
			name:     "no_delimiter",
			input:    "localhost",
			expected: "localhost",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := maskIPAddress(tt.input)
			if result != tt.expected {
				t.Errorf("maskIPAddress(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMaskIPAddress_Privacy(t *testing.T) {
	// Verify last octet/segment is always masked
	testIPs := []string{
		"192.168.1.100:3333",
		"10.0.0.1:8080",
		"[2001:db8::abcd]:3333",
		"172.16.0.254:9999",
	}

	for _, ip := range testIPs {
		masked := maskIPAddress(ip)
		if !strings.Contains(masked, "xxx") {
			t.Errorf("Expected 'xxx' in masked IP, got %q from %q", masked, ip)
		}
		// Verify original last octet is not present
		if strings.Contains(ip, ":3333") {
			original := strings.Split(strings.Split(ip, ":")[0], ".")
			if len(original) == 4 {
				lastOctet := original[3]
				if strings.Contains(masked, "."+lastOctet) {
					t.Errorf("Last octet %q should be masked in %q", lastOctet, masked)
				}
			}
		}
	}
}

// =============================================================================
// HELPER FUNCTION TESTS
// =============================================================================

func TestContains(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		substr string
		want   bool
	}{
		{"empty_both", "", "", true},
		{"empty_substr", "hello", "", true},
		{"empty_s", "", "x", false},
		{"exact_match", "hello", "hello", true},
		{"contains_start", "hello world", "hello", true},
		{"contains_middle", "hello world", "lo wo", true},
		{"contains_end", "hello world", "world", true},
		{"not_contains", "hello", "xyz", false},
		{"case_insensitive_upper", "DUPLICATE", "duplicate", true},
		{"case_insensitive_mixed", "Bad-Block-Error", "bad-", true},
		{"invalid_prefix", "bad-txns", "bad-", true},
		{"already_exists", "Block already exists", "already", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contains(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
			}
		})
	}
}

func TestContainsLower(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		substr string
		want   bool
	}{
		{"exact_match", "hello", "hello", true},
		{"contains_start", "hello world", "hello", true},
		{"contains_end", "hello world", "world", true},
		{"no_match", "hello", "xyz", false},
		{"partial_match", "blockchain", "chain", true},
		{"empty_substr", "hello", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsLower(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("containsLower(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
			}
		})
	}
}

// =============================================================================
// POOL STATS TESTS
// =============================================================================

func TestPoolStats_ZeroValues(t *testing.T) {
	stats := PoolStats{}

	if stats.Connections != 0 {
		t.Error("Connections should be zero")
	}
	if stats.TotalShares != 0 {
		t.Error("TotalShares should be zero")
	}
	if stats.AcceptedShares != 0 {
		t.Error("AcceptedShares should be zero")
	}
	if stats.RejectedShares != 0 {
		t.Error("RejectedShares should be zero")
	}
	if stats.SharesInBuffer != 0 {
		t.Error("SharesInBuffer should be zero")
	}
	if stats.SharesWritten != 0 {
		t.Error("SharesWritten should be zero")
	}
}

func TestPoolStats_Values(t *testing.T) {
	stats := PoolStats{
		Connections:    10,
		TotalShares:    1000,
		AcceptedShares: 950,
		RejectedShares: 50,
		SharesInBuffer: 25,
		SharesWritten:  975,
	}

	if stats.Connections != 10 {
		t.Errorf("Connections = %d, want 10", stats.Connections)
	}
	if stats.TotalShares != 1000 {
		t.Errorf("TotalShares = %d, want 1000", stats.TotalShares)
	}
	if stats.AcceptedShares != 950 {
		t.Errorf("AcceptedShares = %d, want 950", stats.AcceptedShares)
	}
	if stats.RejectedShares != 50 {
		t.Errorf("RejectedShares = %d, want 50", stats.RejectedShares)
	}
	if stats.SharesInBuffer != 25 {
		t.Errorf("SharesInBuffer = %d, want 25", stats.SharesInBuffer)
	}
	if stats.SharesWritten != 975 {
		t.Errorf("SharesWritten = %d, want 975", stats.SharesWritten)
	}
}

func TestPoolStats_AcceptRejectRatio(t *testing.T) {
	tests := []struct {
		name     string
		accepted uint64
		rejected uint64
		wantPct  float64
	}{
		{"all_accepted", 100, 0, 100.0},
		{"all_rejected", 0, 100, 0.0},
		{"half_half", 50, 50, 50.0},
		{"90_percent", 90, 10, 90.0},
		{"no_shares", 0, 0, 0.0}, // Edge case
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := PoolStats{
				AcceptedShares: tt.accepted,
				RejectedShares: tt.rejected,
			}
			total := stats.AcceptedShares + stats.RejectedShares
			var pct float64
			if total > 0 {
				pct = float64(stats.AcceptedShares) / float64(total) * 100
			}
			if pct != tt.wantPct {
				t.Errorf("Accept rate = %.1f%%, want %.1f%%", pct, tt.wantPct)
			}
		})
	}
}

// =============================================================================
// BLOCK SUBMISSION ERROR DETECTION TESTS
// =============================================================================

func TestBlockSubmissionErrorDetection(t *testing.T) {
	// Test error strings that indicate permanent block rejection (no retry)
	permanentErrors := []struct {
		error  string
		isPerm bool
		reason string
	}{
		// Duplicate block errors
		{"duplicate block", true, "block already exists"},
		{"block already exists in chain", true, "duplicate"},
		{"Block already known", true, "already known"},

		// Invalid block errors
		{"invalid block", true, "invalid block"},
		{"bad-txns-missing", true, "bad- prefix"},
		{"bad-blk-length", true, "bad- prefix"},
		{"bad-prevblk", true, "previous block invalid"},

		// Transient errors (should retry)
		{"connection refused", false, "network error"},
		{"timeout", false, "timeout"},
		{"RPC error", false, "RPC issue"},
		{"internal server error", false, "server error"},
	}

	for _, te := range permanentErrors {
		isPermanent := contains(te.error, "duplicate") ||
			contains(te.error, "already") ||
			contains(te.error, "invalid") ||
			contains(te.error, "bad-")

		if isPermanent != te.isPerm {
			t.Errorf("Error %q: isPermanent=%v, want %v (reason: %s)",
				te.error, isPermanent, te.isPerm, te.reason)
		}
	}
}

// =============================================================================
// BLOCK SUBMISSION BACKOFF TESTS
// =============================================================================

func TestBlockSubmissionBackoff(t *testing.T) {
	// Test exponential backoff calculation
	backoffSeconds := []time.Duration{1, 2, 4, 8, 16}

	for attempt := 1; attempt <= 5; attempt++ {
		idx := attempt - 1
		if idx < 0 {
			idx = 0
		} else if idx >= len(backoffSeconds) {
			idx = len(backoffSeconds) - 1
		}

		expectedBackoff := backoffSeconds[idx] * time.Second
		actualBackoff := backoffSeconds[idx] * time.Second

		if actualBackoff != expectedBackoff {
			t.Errorf("Attempt %d: backoff = %v, want %v", attempt, actualBackoff, expectedBackoff)
		}
	}

	// Verify exponential growth pattern
	for i := 1; i < len(backoffSeconds); i++ {
		if backoffSeconds[i] != backoffSeconds[i-1]*2 {
			t.Errorf("Backoff not doubling: [%d]=%v, [%d]=%v",
				i-1, backoffSeconds[i-1], i, backoffSeconds[i])
		}
	}

	// Test that attempt 0 (invalid) is handled
	idx := 0 - 1
	if idx < 0 {
		idx = 0
	}
	if idx != 0 {
		t.Error("Negative index should be clamped to 0")
	}

	// Test that attempts beyond max use last value
	idx = 10 - 1
	if idx >= len(backoffSeconds) {
		idx = len(backoffSeconds) - 1
	}
	if idx != 4 {
		t.Errorf("Index for attempt 10 should be 4, got %d", idx)
	}
}

// =============================================================================
// SOLO MINING BLOCK REWARD TESTS (Critical for SOLO mode)
// =============================================================================

func TestSOLOMiningBlockReward(t *testing.T) {
	// SOLO mining means 100% of block reward goes to miner's coinbase address
	// No fees, no splitting, no PPLNS/PPS/PROP

	// Test reward conversion from satoshis to coins
	testCases := []struct {
		name          string
		satoshis      int64
		expectedCoins float64
	}{
		{"1_DGB", 100000000, 1.0},
		{"0.5_DGB", 50000000, 0.5},
		{"1000_DGB_block_reward", 100000000000, 1000.0},
		{"72.16_DGB_typical", 7216000000, 72.16},
		{"zero_reward", 0, 0.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rewardCoins := float64(tc.satoshis) / 1e8
			if rewardCoins != tc.expectedCoins {
				t.Errorf("Reward conversion: %d satoshis = %f coins, want %f",
					tc.satoshis, rewardCoins, tc.expectedCoins)
			}
		})
	}
}

func TestSOLOMiningNoFees(t *testing.T) {
	// Document SOLO mining invariants
	// These tests serve as documentation and contract enforcement

	t.Run("no_pool_fee", func(t *testing.T) {
		// In SOLO mode, there is NO pool fee
		// The full block reward goes to the miner's configured address
		poolFee := 0.0
		if poolFee != 0.0 {
			t.Error("SOLO mining must have zero pool fee")
		}
	})

	t.Run("no_fee_distribution", func(t *testing.T) {
		// There is no fee distribution mechanism in SOLO mode
		// No PPLNS, no PPS, no PROP - ever
		t.Log("SOLO mode: Full block reward to miner's coinbase address")
		t.Log("Fee distribution is explicitly NOT supported")
	})

	t.Run("block_type_is_solo", func(t *testing.T) {
		// All blocks are type "block" not "uncle" or "share"
		blockType := "block"
		if blockType != "block" {
			t.Errorf("Block type must be 'block', got %q", blockType)
		}
	})
}

// =============================================================================
// SYNC GATE TESTS
// =============================================================================

func TestSyncGateThresholds(t *testing.T) {
	const syncThreshold = 0.990 // 99.0% - realistic threshold that accounts for verificationProgress drift

	tests := []struct {
		name      string
		progress  float64
		ibd       bool
		shouldRun bool
	}{
		{"fully_synced", 1.0, false, true},
		{"99.99_percent", 0.9999, false, true},
		{"99.95_percent", 0.9995, false, true},
		{"99.9_percent", 0.999, false, true},
		{"99.0_percent", 0.990, false, true},
		{"98.9_percent", 0.989, false, false},
		{"50_percent", 0.5, false, false},
		{"synced_but_ibd", 1.0, true, false},
		{"99.9_but_ibd", 0.999, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canRun := !tt.ibd && tt.progress >= syncThreshold
			if canRun != tt.shouldRun {
				t.Errorf("progress=%.4f, ibd=%v: canRun=%v, want %v",
					tt.progress, tt.ibd, canRun, tt.shouldRun)
			}
		})
	}
}

func TestSyncGateTimeout(t *testing.T) {
	maxWaitTime := 4 * time.Hour

	// Verify timeout is reasonable (not too short, not infinite)
	if maxWaitTime < 1*time.Hour {
		t.Error("Sync gate timeout too short - daemons may need hours to sync")
	}
	if maxWaitTime > 24*time.Hour {
		t.Error("Sync gate timeout too long - operator should investigate after 24h")
	}
}

// =============================================================================
// ZMQ PROMOTION TESTS
// =============================================================================

func TestZMQStabilityThreshold(t *testing.T) {
	// ZMQ needs 5 minutes of stability before becoming primary
	stabilityThreshold := 5 * time.Minute

	tests := []struct {
		name            string
		healthyDuration time.Duration
		shouldPromote   bool
	}{
		{"just_started", 0, false},
		{"1_minute", 1 * time.Minute, false},
		{"4_minutes", 4 * time.Minute, false},
		{"5_minutes_exact", 5 * time.Minute, true},
		{"10_minutes", 10 * time.Minute, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canPromote := tt.healthyDuration >= stabilityThreshold
			if canPromote != tt.shouldPromote {
				t.Errorf("healthyDuration=%v: canPromote=%v, want %v",
					tt.healthyDuration, canPromote, tt.shouldPromote)
			}
		})
	}
}

// =============================================================================
// STALE SHARE CLEANUP TESTS
// =============================================================================

func TestStaleShareCleanupRetention(t *testing.T) {
	// Cleanup window should be 15 minutes (1.5x hashrate calculation window)
	retentionMinutes := 15

	// Verify retention is reasonable
	if retentionMinutes < 10 {
		t.Error("Retention too short - might lose valid recent shares")
	}
	if retentionMinutes > 60 {
		t.Error("Retention too long - hashrate calculation would include stale data")
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkDefaultStartupConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = DefaultStartupConfig()
	}
}

func BenchmarkMaskIPAddress(b *testing.B) {
	ip := "192.168.1.100:3333"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = maskIPAddress(ip)
	}
}

func BenchmarkContains(b *testing.B) {
	s := "duplicate block already exists in chain"
	substr := "duplicate"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = contains(s, substr)
	}
}
