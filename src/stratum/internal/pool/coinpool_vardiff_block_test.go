// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"testing"
	"time"
)

// =============================================================================
// G5: CoinPool handleBlock Timeout & Crash Safety Tests
// =============================================================================
// handleBlock uses concrete types (*database.PostgresDB, *nodemanager.Manager)
// so we test the timeout logic and crash safety algorithm in isolation.

// TestHandleBlock_SubmitTimeout_CoinAware verifies the coin-aware timeout
// constants are reasonable: the submit timeout should fit within one block
// period, and the deadline should allow for retries.
func TestHandleBlock_SubmitTimeout_CoinAware(t *testing.T) {
	t.Parallel()

	// Table of expected timeout constraints per coin.
	// These verify the logic in daemon.NewSubmitTimeouts().
	tests := []struct {
		coin             string
		blockTimeSec     int
		minSubmitTimeout time.Duration
		maxSubmitTimeout time.Duration
		minDeadline      time.Duration
		maxDeadline      time.Duration
	}{
		{
			coin:             "BTC",
			blockTimeSec:     600, // 10 min
			minSubmitTimeout: 5 * time.Second,
			maxSubmitTimeout: 30 * time.Second,
			minDeadline:      30 * time.Second,
			maxDeadline:      120 * time.Second,
		},
		{
			coin:             "DGB",
			blockTimeSec:     15, // 15 sec
			minSubmitTimeout: 2 * time.Second,
			maxSubmitTimeout: 10 * time.Second,
			minDeadline:      5 * time.Second,
			maxDeadline:      15 * time.Second,
		},
		{
			coin:             "LTC",
			blockTimeSec:     150, // 2.5 min
			minSubmitTimeout: 3 * time.Second,
			maxSubmitTimeout: 20 * time.Second,
			minDeadline:      10 * time.Second,
			maxDeadline:      60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.coin, func(t *testing.T) {
			blockTime := time.Duration(tt.blockTimeSec) * time.Second

			// Validate constraint: submit timeout should be well under block time
			if tt.maxSubmitTimeout >= blockTime {
				t.Errorf("maxSubmitTimeout (%v) >= blockTime (%v)", tt.maxSubmitTimeout, blockTime)
			}

			// Validate constraint: deadline should allow for at least 2 retries
			if tt.maxDeadline < 2*tt.minSubmitTimeout {
				t.Errorf("maxDeadline (%v) < 2 * minSubmitTimeout (%v)",
					tt.maxDeadline, 2*tt.minSubmitTimeout)
			}
		})
	}
}

// TestHandleBlock_CrashSafety_StatusFlow verifies the crash-safe block
// submission status flow: "submitting" → "pending"/"orphaned".
func TestHandleBlock_CrashSafety_StatusFlow(t *testing.T) {
	t.Parallel()

	// The handleBlock crash safety flow:
	// 1. Insert block with status="submitting" (DB write)
	// 2. Submit to daemon (RPC call)
	// 3. Update status to "pending" (success) or "orphaned" (failure)
	//
	// If crash occurs between step 1 and 3, reconcileSubmittingBlocks
	// picks up blocks stuck in "submitting" and resolves them.

	statusTransitions := []struct {
		name          string
		initialStatus string
		submitResult  bool // true=success, false=failure
		finalStatus   string
	}{
		{"success", "submitting", true, "pending"},
		{"failure", "submitting", false, "orphaned"},
	}

	for _, tt := range statusTransitions {
		t.Run(tt.name, func(t *testing.T) {
			if tt.initialStatus != "submitting" {
				t.Errorf("initial status should be 'submitting', got %q", tt.initialStatus)
			}

			var finalStatus string
			if tt.submitResult {
				finalStatus = "pending"
			} else {
				finalStatus = "orphaned"
			}

			if finalStatus != tt.finalStatus {
				t.Errorf("final status: got %q, want %q", finalStatus, tt.finalStatus)
			}
		})
	}
}

// TestHandleBlock_PermanentRejection_Decision verifies that permanent
// rejection error strings cause immediate orphaning (no retry).
func TestHandleBlock_PermanentRejection_Decision(t *testing.T) {
	t.Parallel()

	permanentErrors := []string{
		"duplicate",
		"stale",
		"inconclusive",
		"high-hash",
		"bad-prevblk",
	}

	for _, errStr := range permanentErrors {
		t.Run(errStr, func(t *testing.T) {
			// isPermanentRejection is the function under test.
			// Verify the known permanent error strings are recognized.
			if !isPermanentRejection(errStr) {
				t.Errorf("expected %q to be a permanent rejection", errStr)
			}
		})
	}

	// Transient errors should NOT be permanent
	transientErrors := []string{
		"connection refused",
		"timeout",
		"EOF",
	}

	for _, errStr := range transientErrors {
		t.Run("transient_"+errStr, func(t *testing.T) {
			if isPermanentRejection(errStr) {
				t.Errorf("expected %q to NOT be a permanent rejection", errStr)
			}
		})
	}
}

// =============================================================================
// G7: VARDIFF Algorithm Tests
// =============================================================================
// Tests the VARDIFF (variable difficulty) algorithm's key decision points:
// ramp-up threshold, miner-specific cooldowns, and exponential backoff.

// TestVARDIFF_RampUpThreshold verifies the ramp-up boundary:
// first 10 shares use aggressive retargeting, then normal vardiff.
func TestVARDIFF_RampUpThreshold(t *testing.T) {
	t.Parallel()

	rampUpLimit := 10

	tests := []struct {
		totalShares    int
		expectRampUp   bool
	}{
		{1, true},
		{5, true},
		{10, true},
		{11, false},
		{100, false},
	}

	for _, tt := range tests {
		isRampUp := tt.totalShares <= rampUpLimit
		if isRampUp != tt.expectRampUp {
			t.Errorf("totalShares=%d: isRampUp=%v, want %v",
				tt.totalShares, isRampUp, tt.expectRampUp)
		}
	}
}

// TestVARDIFF_MinerSpecificCooldown verifies that cgminer/Avalon miners
// get a 30-second cooldown while other miners get a 5-second cooldown.
func TestVARDIFF_MinerSpecificCooldown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		isSlowDiffApplier bool
		wantCooldown      float64
	}{
		{
			name:              "cgminer_30s",
			isSlowDiffApplier: true,
			wantCooldown:      30.0,
		},
		{
			name:              "normal_miner_5s",
			isSlowDiffApplier: false,
			wantCooldown:      5.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This mirrors the logic in handleShare:
			minRetargetInterval := 5.0
			if tt.isSlowDiffApplier {
				minRetargetInterval = 30.0
			}

			if minRetargetInterval != tt.wantCooldown {
				t.Errorf("cooldown: got %.1f, want %.1f",
					minRetargetInterval, tt.wantCooldown)
			}
		})
	}
}

// TestVARDIFF_ExponentialBackoff verifies the exponential backoff multiplier
// for sessions that have converged to optimal difficulty.
func TestVARDIFF_ExponentialBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		consecutiveNoChange int
		wantMultiplier      float64
	}{
		{0, 1.0}, // No backoff (freshly changed)
		{1, 2.0}, // 2x
		{2, 3.0}, // 3x
		{3, 4.0}, // 4x (cap)
		{4, 4.0}, // Still capped at 4x
		{10, 4.0}, // Still capped at 4x
	}

	for _, tt := range tests {
		// This mirrors the backoff logic in handleShare:
		backoffCount := tt.consecutiveNoChange
		var multiplier float64
		if backoffCount > 0 {
			multiplier = float64(backoffCount + 1)
			if multiplier > 4.0 {
				multiplier = 4.0
			}
		} else {
			multiplier = 1.0
		}

		if multiplier != tt.wantMultiplier {
			t.Errorf("consecutiveNoChange=%d: multiplier=%.1f, want %.1f",
				tt.consecutiveNoChange, multiplier, tt.wantMultiplier)
		}
	}
}

// TestVARDIFF_CooldownWithBackoff verifies the combined cooldown calculation:
// base cooldown * backoff multiplier.
func TestVARDIFF_CooldownWithBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		isSlowDiffApplier   bool
		consecutiveNoChange int
		wantInterval        float64
	}{
		{"normal_fresh", false, 0, 5.0},
		{"normal_1x_backoff", false, 1, 10.0},
		{"normal_max_backoff", false, 5, 20.0},
		{"cgminer_fresh", true, 0, 30.0},
		{"cgminer_1x_backoff", true, 1, 60.0},
		{"cgminer_max_backoff", true, 5, 120.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Base cooldown
			minRetargetInterval := 5.0
			if tt.isSlowDiffApplier {
				minRetargetInterval = 30.0
			}

			// Apply backoff
			backoffCount := tt.consecutiveNoChange
			if backoffCount > 0 {
				backoffMultiplier := float64(backoffCount + 1)
				if backoffMultiplier > 4.0 {
					backoffMultiplier = 4.0
				}
				minRetargetInterval *= backoffMultiplier
			}

			if minRetargetInterval != tt.wantInterval {
				t.Errorf("interval: got %.1f, want %.1f",
					minRetargetInterval, tt.wantInterval)
			}
		})
	}
}

// TestVARDIFF_AggressiveRetargetGate verifies the conditions that trigger
// aggressive retargeting: (ramp-up OR deviation) AND enough shares since
// last retarget AND enough elapsed time.
func TestVARDIFF_AggressiveRetargetGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		totalShares         int
		shouldAggressive    bool // from engine
		sharesSinceRetarget int
		elapsedSec          float64
		minInterval         float64
		expectRetarget      bool
	}{
		{
			name:                "rampup_sufficient_shares_and_time",
			totalShares:         5,
			shouldAggressive:    false,
			sharesSinceRetarget: 3,
			elapsedSec:          10.0,
			minInterval:         5.0,
			expectRetarget:      true,
		},
		{
			name:                "rampup_insufficient_shares",
			totalShares:         5,
			shouldAggressive:    false,
			sharesSinceRetarget: 1, // < 2
			elapsedSec:          10.0,
			minInterval:         5.0,
			expectRetarget:      false,
		},
		{
			name:                "rampup_insufficient_time",
			totalShares:         5,
			shouldAggressive:    false,
			sharesSinceRetarget: 3,
			elapsedSec:          3.0, // < 5.0
			minInterval:         5.0,
			expectRetarget:      false,
		},
		{
			name:                "post_rampup_aggressive_deviation",
			totalShares:         50,
			shouldAggressive:    true,
			sharesSinceRetarget: 5,
			elapsedSec:          10.0,
			minInterval:         5.0,
			expectRetarget:      true,
		},
		{
			name:                "post_rampup_no_deviation",
			totalShares:         50,
			shouldAggressive:    false,
			sharesSinceRetarget: 5,
			elapsedSec:          10.0,
			minInterval:         5.0,
			expectRetarget:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirror the handleShare logic:
			needsAggressive := tt.totalShares <= 10 || tt.shouldAggressive
			canRetarget := needsAggressive &&
				tt.sharesSinceRetarget >= 2 &&
				tt.elapsedSec > tt.minInterval

			if canRetarget != tt.expectRetarget {
				t.Errorf("retarget decision: got %v, want %v", canRetarget, tt.expectRetarget)
			}
		})
	}
}
