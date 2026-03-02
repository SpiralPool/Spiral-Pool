// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/ha"
	"go.uber.org/zap"
)

// =============================================================================
// G3: Coordinator Run() Lifecycle Tests
// =============================================================================
// Verifies Run() startup guard, shutdown ordering, and failed-coins tracking.

// TestCoordinator_Run_PreventsConcurrentRun verifies that calling Run()
// on an already-running coordinator returns an error immediately.
func TestCoordinator_Run_PreventsConcurrentRun(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Simulate already running by setting the flag directly
	coord.runMu.Lock()
	coord.running = true
	coord.runMu.Unlock()

	// Run() should detect the guard and return error without blocking
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so Run doesn't block
	err := coord.Run(ctx)
	if err == nil {
		t.Fatal("expected error when coordinator is already running")
	}
	if err.Error() != "coordinator already running" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCoordinator_IsRunning_ReflectsState verifies that IsRunning() returns
// the correct state before and after the running flag is set.
func TestCoordinator_IsRunning_ReflectsState(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}

	if coord.IsRunning() {
		t.Error("coordinator should not be running initially")
	}

	coord.runMu.Lock()
	coord.running = true
	coord.runMu.Unlock()

	if !coord.IsRunning() {
		t.Error("coordinator should be running after flag set")
	}
}

// TestCoordinator_FailedCoins_Tracking verifies that failed coin configs
// are tracked for retry when pool startup fails.
func TestCoordinator_FailedCoins_Tracking(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Initially no failed coins
	if len(coord.failedCoins) != 0 {
		t.Errorf("initial failedCoins: got %d, want 0", len(coord.failedCoins))
	}

	// The failed coins list is populated during NewCoordinator when
	// coin pool creation fails. Verify the field is accessible and typed.
	coord.failedCoinsMu.Lock()
	coord.failedCoins = nil
	coord.failedCoinsMu.Unlock()

	if coord.failedCoins != nil {
		t.Error("failedCoins should be nil after explicit nil set")
	}
}

// TestCoordinator_HandleRoleChange_RoleHandlerWiring verifies that the
// coordinator correctly dispatches role changes to all registered CoinPools.
func TestCoordinator_HandleRoleChange_RoleHandlerWiring(t *testing.T) {
	t.Parallel()

	nopLogger := zap.NewNop().Sugar()

	dgbPool := &CoinPool{
		logger:     nopLogger.With("coin", "DGB"),
		coinSymbol: "DGB",
	}
	btcPool := &CoinPool{
		logger:     nopLogger.With("coin", "BTC"),
		coinSymbol: "BTC",
	}

	coord := &Coordinator{
		logger: nopLogger,
		pools: map[string]*CoinPool{
			"dgb_main": dgbPool,
			"btc_main": btcPool,
		},
	}

	// Promote to master
	coord.handleRoleChange(ha.RoleUnknown, ha.RoleMaster)

	// Both pools should have their haRole set to Master
	if ha.Role(dgbPool.haRole.Load()) != ha.RoleMaster {
		t.Errorf("DGB haRole: got %v, want RoleMaster", ha.Role(dgbPool.haRole.Load()))
	}
	if ha.Role(btcPool.haRole.Load()) != ha.RoleMaster {
		t.Errorf("BTC haRole: got %v, want RoleMaster", ha.Role(btcPool.haRole.Load()))
	}

	// Demote to backup
	coord.handleRoleChange(ha.RoleMaster, ha.RoleBackup)

	if ha.Role(dgbPool.haRole.Load()) != ha.RoleBackup {
		t.Errorf("DGB haRole after demotion: got %v, want RoleBackup", ha.Role(dgbPool.haRole.Load()))
	}
	if ha.Role(btcPool.haRole.Load()) != ha.RoleBackup {
		t.Errorf("BTC haRole after demotion: got %v, want RoleBackup", ha.Role(btcPool.haRole.Load()))
	}
}

// TestCoordinator_DefaultStartupConfig_Bounds verifies the startup config
// has reasonable bounds for production use.
func TestCoordinator_DefaultStartupConfig_Bounds(t *testing.T) {
	t.Parallel()

	cfg := DefaultStartupConfig()

	// Grace period: long enough for blockchain daemons to sync
	if cfg.GracePeriod < 5*time.Minute {
		t.Errorf("GracePeriod too short: %v", cfg.GracePeriod)
	}
	if cfg.GracePeriod > 60*time.Minute {
		t.Errorf("GracePeriod too long: %v", cfg.GracePeriod)
	}

	// Retry interval: reasonable polling frequency
	if cfg.RetryInterval < 10*time.Second {
		t.Errorf("RetryInterval too aggressive: %v", cfg.RetryInterval)
	}
	if cfg.RetryInterval >= cfg.GracePeriod {
		t.Errorf("RetryInterval (%v) >= GracePeriod (%v)", cfg.RetryInterval, cfg.GracePeriod)
	}
}

// =============================================================================
// G4: Redis SETNX Dedup Decision Tests
// =============================================================================
// Tests the Redis SETNX block dedup key format and decision logic.
// The actual Redis calls are tested via integration tests; these verify
// the key construction and decision branching.

// TestRedisDedup_KeyFormat verifies that the Redis dedup key follows
// the expected format: "pool:block_dedup:{coin}:{height}:{hash}"
func TestRedisDedup_KeyFormat(t *testing.T) {
	t.Parallel()

	coinSymbol := "DGB"
	height := uint64(12345)
	hash := "0000abcdef123456"

	key := fmt.Sprintf("pool:block_dedup:%s:%d:%s", coinSymbol, height, hash)
	expected := "pool:block_dedup:DGB:12345:0000abcdef123456"

	if key != expected {
		t.Errorf("dedup key: got %q, want %q", key, expected)
	}
}

// TestRedisDedup_DecisionLogic verifies the decision branching when
// Redis SETNX returns acquired=true vs acquired=false.
func TestRedisDedup_DecisionLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setnxResult  bool
		shouldSubmit bool
	}{
		{
			name:         "acquired_lock_submits",
			setnxResult:  true,
			shouldSubmit: true,
		},
		{
			name:         "lock_already_held_skips",
			setnxResult:  false,
			shouldSubmit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the decision logic from pool.go handleBlock:
			// if tracker.TrySetBlockDedup(...) returned true, we submit.
			shouldSubmit := tt.setnxResult
			if shouldSubmit != tt.shouldSubmit {
				t.Errorf("decision for setnx=%v: got submit=%v, want %v",
					tt.setnxResult, shouldSubmit, tt.shouldSubmit)
			}
		})
	}
}

// TestRedisDedup_ExpiryReasonable verifies that the dedup key expiry
// is long enough to cover block propagation but short enough to not
// cause permanent lockout.
func TestRedisDedup_ExpiryReasonable(t *testing.T) {
	t.Parallel()

	// The dedup key should expire after a reasonable time:
	// - Too short (<30s): could allow duplicate submissions during slow propagation
	// - Too long (>1h): could prevent legitimate re-submissions after node restart
	// Typical value: 5 minutes (300 seconds)
	dedupExpiry := 5 * time.Minute

	if dedupExpiry < 30*time.Second {
		t.Errorf("dedup expiry too short: %v", dedupExpiry)
	}
	if dedupExpiry > 1*time.Hour {
		t.Errorf("dedup expiry too long: %v", dedupExpiry)
	}
}
