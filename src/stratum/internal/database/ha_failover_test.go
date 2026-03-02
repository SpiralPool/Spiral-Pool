// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package database

import (
	"testing"
	"time"
)

// TestPaymentAdvisoryLockID_Stable verifies the advisory lock ID
// is a stable, well-known value that won't accidentally change.
func TestPaymentAdvisoryLockID_Stable(t *testing.T) {
	t.Parallel()

	lockID := PaymentAdvisoryLockID()

	// The value is 0x5350504159 ("SPPAY" in ASCII-ish hex)
	expected := int64(0x5350504159)
	if lockID != expected {
		t.Errorf("PaymentAdvisoryLockID() = %d (0x%X), want %d (0x%X)",
			lockID, lockID, expected, expected)
	}
}

// TestPaymentAdvisoryLockID_NonZero verifies the lock ID is non-zero
// to avoid conflicting with PostgreSQL's default advisory lock behavior.
func TestPaymentAdvisoryLockID_NonZero(t *testing.T) {
	t.Parallel()

	if PaymentAdvisoryLockID() == 0 {
		t.Error("PaymentAdvisoryLockID() should not be 0 (conflicts with default locks)")
	}
}

// TestPaymentAdvisoryLockID_FitsInt64 verifies the lock ID fits in int64
// which is required by PostgreSQL's pg_try_advisory_lock function.
func TestPaymentAdvisoryLockID_FitsInt64(t *testing.T) {
	t.Parallel()

	lockID := PaymentAdvisoryLockID()

	// PostgreSQL advisory locks use bigint (int64)
	// Verify the value is positive (negative values have different semantics)
	if lockID <= 0 {
		t.Errorf("PaymentAdvisoryLockID() = %d, want positive value", lockID)
	}
}

// TestDBNodeState_TransitionPaths verifies valid state transitions
// that occur during HA database failover.
func TestDBNodeState_TransitionPaths(t *testing.T) {
	t.Parallel()

	// Valid transitions during failover
	transitions := []struct {
		name string
		from DBNodeState
		to   DBNodeState
	}{
		{"healthy_to_degraded", DBNodeHealthy, DBNodeDegraded},
		{"degraded_to_unhealthy", DBNodeDegraded, DBNodeUnhealthy},
		{"unhealthy_to_offline", DBNodeUnhealthy, DBNodeOffline},
		{"offline_to_healthy_recovery", DBNodeOffline, DBNodeHealthy},
		{"degraded_to_healthy_recovery", DBNodeDegraded, DBNodeHealthy},
		{"healthy_to_offline_crash", DBNodeHealthy, DBNodeOffline},
	}

	for _, tt := range transitions {
		t.Run(tt.name, func(t *testing.T) {
			// Verify both states have valid string representations
			fromStr := tt.from.String()
			toStr := tt.to.String()

			if fromStr == "unknown" {
				t.Errorf("from state has unknown string: %d", tt.from)
			}
			if toStr == "unknown" {
				t.Errorf("to state has unknown string: %d", tt.to)
			}

			// Verify states are different (no self-transitions in this table)
			if tt.from == tt.to {
				t.Errorf("self-transition detected: %s -> %s", fromStr, toStr)
			}
		})
	}
}

// TestDBFailoverThresholds_NonZero verifies failover thresholds are sensible.
func TestDBFailoverThresholds_NonZero(t *testing.T) {
	t.Parallel()

	if MaxDBNodeFailures == 0 {
		t.Error("MaxDBNodeFailures must be > 0")
	}
	if DBHealthCheckInterval == 0 {
		t.Error("DBHealthCheckInterval must be > 0")
	}
	if DBReconnectBackoff == 0 {
		t.Error("DBReconnectBackoff must be > 0")
	}
}

// TestDBFailoverThresholds_ReasonableValues verifies thresholds are within
// reasonable bounds for production use.
func TestDBFailoverThresholds_ReasonableValues(t *testing.T) {
	t.Parallel()

	// MaxDBNodeFailures should be 1-10 (too low = flapping, too high = slow failover)
	if MaxDBNodeFailures < 1 || MaxDBNodeFailures > 10 {
		t.Errorf("MaxDBNodeFailures = %d, want 1-10", MaxDBNodeFailures)
	}

	// Health check interval should be 1s - 60s
	if DBHealthCheckInterval < 1*time.Second || DBHealthCheckInterval > 60*time.Second {
		t.Errorf("DBHealthCheckInterval = %v, want 1s-60s", DBHealthCheckInterval)
	}

	// Reconnect backoff should be 1s - 30s
	if DBReconnectBackoff < 1*time.Second || DBReconnectBackoff > 30*time.Second {
		t.Errorf("DBReconnectBackoff = %v, want 1s-30s", DBReconnectBackoff)
	}
}
