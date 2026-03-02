// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/ha"
	"go.uber.org/zap"
)

// TestCoordinator_DefaultStartupConfig verifies startup defaults are
// appropriate for HA deployments.
func TestCoordinator_DefaultStartupConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultStartupConfig()

	if cfg.GracePeriod != 30*time.Minute {
		t.Errorf("GracePeriod: got %v, want 30m", cfg.GracePeriod)
	}
	if cfg.RetryInterval != 30*time.Second {
		t.Errorf("RetryInterval: got %v, want 30s", cfg.RetryInterval)
	}
	if cfg.RequireAllCoins {
		t.Error("RequireAllCoins should be false by default (allow partial startup)")
	}
}

// TestCoordinator_DefaultStartupConfig_HAReasonable verifies the startup
// config values are reasonable for HA operation.
func TestCoordinator_DefaultStartupConfig_HAReasonable(t *testing.T) {
	t.Parallel()

	cfg := DefaultStartupConfig()

	// Grace period should be long enough for blockchain daemons to sync
	if cfg.GracePeriod < 5*time.Minute {
		t.Errorf("GracePeriod too short for HA: %v", cfg.GracePeriod)
	}

	// Retry interval should be at least 10 seconds to avoid hammering
	if cfg.RetryInterval < 10*time.Second {
		t.Errorf("RetryInterval too aggressive: %v", cfg.RetryInterval)
	}

	// Retry interval should be less than grace period
	if cfg.RetryInterval >= cfg.GracePeriod {
		t.Errorf("RetryInterval (%v) >= GracePeriod (%v)", cfg.RetryInterval, cfg.GracePeriod)
	}
}

// TestCoordinator_SetVIPManager_NilSafe verifies setting a nil VIP manager
// doesn't panic (allows disabling HA).
func TestCoordinator_SetVIPManager_NilSafe(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}
	// Should not panic
	coord.SetVIPManager(nil)
	if coord.vipManager != nil {
		t.Error("vipManager should be nil after SetVIPManager(nil)")
	}
}

// TestCoordinator_SetDatabaseManager_NilSafe verifies setting a nil DB manager
// doesn't panic.
func TestCoordinator_SetDatabaseManager_NilSafe(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}
	// Should not panic
	coord.SetDatabaseManager(nil)
	if coord.dbManager != nil {
		t.Error("dbManager should be nil after SetDatabaseManager(nil)")
	}
}

// TestCoordinator_SetReplicationManager_NilSafe verifies setting a nil
// replication manager doesn't panic.
func TestCoordinator_SetReplicationManager_NilSafe(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}
	// Should not panic
	coord.SetReplicationManager(nil)
	if coord.replicationManager != nil {
		t.Error("replicationManager should be nil after SetReplicationManager(nil)")
	}
}

// TestCoordinator_HandleRoleChange_NoPools verifies handleRoleChange
// works when no pools are registered (startup race).
func TestCoordinator_HandleRoleChange_NoPools(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Should not panic with empty pools map
	coord.handleRoleChange(ha.RoleUnknown, ha.RoleMaster)
	coord.handleRoleChange(ha.RoleMaster, ha.RoleBackup)
}

// TestCoordinator_HandleRoleChange_NilMetrics verifies handleRoleChange
// works when metrics server is nil.
func TestCoordinator_HandleRoleChange_NilMetrics(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger:        zap.NewNop().Sugar(),
		pools:         make(map[string]*CoinPool),
		metricsServer: nil,
	}

	// Should not panic with nil metricsServer
	coord.handleRoleChange(ha.RoleUnknown, ha.RoleMaster)
}

// TestCoordinatorStats_EmptyCoordinator verifies Stats returns zero values
// when no pools are registered.
func TestCoordinatorStats_EmptyCoordinator(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		pools: make(map[string]*CoinPool),
	}

	stats := coord.Stats()

	if stats.PoolCount != 0 {
		t.Errorf("PoolCount: got %d, want 0", stats.PoolCount)
	}
	if stats.TotalConnections != 0 {
		t.Errorf("TotalConnections: got %d, want 0", stats.TotalConnections)
	}
	if stats.TotalShares != 0 {
		t.Errorf("TotalShares: got %d, want 0", stats.TotalShares)
	}
	if len(stats.Pools) != 0 {
		t.Errorf("Pools map: got %d entries, want 0", len(stats.Pools))
	}
}

// TestCoordinator_ListPools_Empty verifies ListPools returns empty slice
// when no pools are registered.
func TestCoordinator_ListPools_Empty(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		pools: make(map[string]*CoinPool),
	}

	pools := coord.ListPools()
	if len(pools) != 0 {
		t.Errorf("ListPools: got %d pools, want 0", len(pools))
	}
}

// TestCoordinator_GetPool_NotFound verifies GetPool returns false
// for non-existent pool IDs.
func TestCoordinator_GetPool_NotFound(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		pools: make(map[string]*CoinPool),
	}

	_, found := coord.GetPool("nonexistent")
	if found {
		t.Error("GetPool should return false for non-existent pool")
	}
}

// TestCoordinator_IsRunning_InitialState verifies the coordinator
// starts in non-running state.
func TestCoordinator_IsRunning_InitialState(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}
	if coord.IsRunning() {
		t.Error("coordinator should not be running initially")
	}
}

// TestCoordinator_HandleRoleChange_WithPools verifies handleRoleChange
// propagates role changes to all populated CoinPool entries without panic.
func TestCoordinator_HandleRoleChange_WithPools(t *testing.T) {
	t.Parallel()

	nopLogger := zap.NewNop().Sugar()

	// Create minimally-initialized CoinPools (logger + coinSymbol are the
	// only fields accessed by OnHARoleChange and Symbol).
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

	// Should not panic: Unknown → Master with populated pools
	coord.handleRoleChange(ha.RoleUnknown, ha.RoleMaster)

	// Should not panic: Master → Backup with populated pools
	coord.handleRoleChange(ha.RoleMaster, ha.RoleBackup)

	// Should not panic: Backup → Master with populated pools
	coord.handleRoleChange(ha.RoleBackup, ha.RoleMaster)
}
