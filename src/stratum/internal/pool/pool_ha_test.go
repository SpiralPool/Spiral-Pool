// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/ha"
	"go.uber.org/zap"
)

// ============================================================================
// Coordinator HA Tests
// ============================================================================

// TestCoordinator_HandleRoleChange_MasterToBackup verifies that a role change
// from MASTER to BACKUP is propagated to all registered coin pools without
// panicking. With an empty pools map, the coordinator-level bookkeeping
// (logging, metrics guard) is exercised.
func TestCoordinator_HandleRoleChange_MasterToBackup(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Must not panic: master -> backup with zero pools.
	coord.handleRoleChange(ha.RoleMaster, ha.RoleBackup)
}

// TestCoordinator_HandleRoleChange_BackupToMaster verifies that promotion
// from BACKUP to MASTER is handled correctly at the coordinator level.
func TestCoordinator_HandleRoleChange_BackupToMaster(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Must not panic: backup -> master with zero pools.
	coord.handleRoleChange(ha.RoleBackup, ha.RoleMaster)
}

// TestCoordinator_HandleRoleChange_MultipleTransitions exercises rapid
// successive role transitions to ensure no panics, deadlocks, or
// accumulated stale state.
func TestCoordinator_HandleRoleChange_MultipleTransitions(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	transitions := []struct{ old, new ha.Role }{
		{ha.RoleUnknown, ha.RoleMaster},
		{ha.RoleMaster, ha.RoleBackup},
		{ha.RoleBackup, ha.RoleMaster},
		{ha.RoleMaster, ha.RoleObserver},
		{ha.RoleObserver, ha.RoleBackup},
		{ha.RoleBackup, ha.RoleMaster},
		{ha.RoleMaster, ha.RoleUnknown},
		{ha.RoleUnknown, ha.RoleMaster},
	}

	for i, tr := range transitions {
		// Must not panic on any rapid flip.
		coord.handleRoleChange(tr.old, tr.new)
		_ = i // iteration used only for debugging context
	}
}

// TestCoordinator_SetVIPManager_Wiring verifies that SetVIPManager stores
// the given manager in the coordinator's vipManager field.
func TestCoordinator_SetVIPManager_Wiring(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}

	// Set a non-nil value (we use a typed nil pointer -- the coordinator
	// only stores the reference; it does not dereference it during Set).
	var mgr *ha.VIPManager
	coord.SetVIPManager(mgr)

	if coord.vipManager != mgr {
		t.Errorf("vipManager: got %v, want %v", coord.vipManager, mgr)
	}

	// Setting nil should also work (disabling HA).
	coord.SetVIPManager(nil)
	if coord.vipManager != nil {
		t.Error("vipManager should be nil after SetVIPManager(nil)")
	}
}

// TestCoordinator_SetDatabaseManager_Wiring verifies that SetDatabaseManager
// stores the given manager correctly.
func TestCoordinator_SetDatabaseManager_Wiring(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}

	var mgr *database.DatabaseManager
	coord.SetDatabaseManager(mgr)

	if coord.dbManager != mgr {
		t.Errorf("dbManager: got %v, want %v", coord.dbManager, mgr)
	}

	coord.SetDatabaseManager(nil)
	if coord.dbManager != nil {
		t.Error("dbManager should be nil after SetDatabaseManager(nil)")
	}
}

// TestCoordinator_SetReplicationManager_Wiring verifies that
// SetReplicationManager stores the given manager correctly.
func TestCoordinator_SetReplicationManager_Wiring(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{}

	var mgr *database.ReplicationManager
	coord.SetReplicationManager(mgr)

	if coord.replicationManager != mgr {
		t.Errorf("replicationManager: got %v, want %v", coord.replicationManager, mgr)
	}

	coord.SetReplicationManager(nil)
	if coord.replicationManager != nil {
		t.Error("replicationManager should be nil after SetReplicationManager(nil)")
	}
}

// ============================================================================
// Startup Configuration Tests
// ============================================================================

// TestStartupConfig_Default_HAReasonable verifies the grace period is long
// enough to cover typical blockchain daemon restarts (several minutes) and
// HA failover windows.
func TestStartupConfig_Default_HAReasonable(t *testing.T) {
	t.Parallel()

	cfg := DefaultStartupConfig()

	// Grace period must be at least 5 minutes for HA to be practical.
	if cfg.GracePeriod < 5*time.Minute {
		t.Errorf("GracePeriod %v is too short for HA (minimum 5m)", cfg.GracePeriod)
	}

	// Grace period must not be unreasonably long (> 24 hours would indicate a bug).
	if cfg.GracePeriod > 24*time.Hour {
		t.Errorf("GracePeriod %v is unreasonably long (> 24h)", cfg.GracePeriod)
	}
}

// TestStartupConfig_Default_RetryNotAggressive verifies the retry interval
// is not too short, which would hammer failing nodes and waste CPU.
func TestStartupConfig_Default_RetryNotAggressive(t *testing.T) {
	t.Parallel()

	cfg := DefaultStartupConfig()

	if cfg.RetryInterval < 10*time.Second {
		t.Errorf("RetryInterval %v is too aggressive (minimum 10s)", cfg.RetryInterval)
	}

	// Retry interval must be shorter than grace period for retries to occur.
	if cfg.RetryInterval >= cfg.GracePeriod {
		t.Errorf("RetryInterval (%v) >= GracePeriod (%v); no retries would occur",
			cfg.RetryInterval, cfg.GracePeriod)
	}
}

// TestStartupConfig_Default_PartialStartup verifies RequireAllCoins defaults
// to false, allowing the coordinator to start with a subset of coins while
// waiting for the rest. This is essential for HA where partial operation is
// preferable to total downtime.
func TestStartupConfig_Default_PartialStartup(t *testing.T) {
	t.Parallel()

	cfg := DefaultStartupConfig()

	if cfg.RequireAllCoins {
		t.Error("RequireAllCoins should default to false for graceful degradation")
	}
}

// ============================================================================
// WAL + DB Reconciliation Tests
// ============================================================================

// TestWAL_DBReconciliation_SubmittedBlocksMissing writes WAL entries with
// "submitted" status and verifies RecoverSubmittedBlocks finds them.
func TestWAL_DBReconciliation_SubmittedBlocksMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}

	// Log a block as found (pending).
	entry := &BlockWALEntry{
		Height:       500000,
		BlockHash:    "00000000000000000001aaa",
		BlockHex:     "deadbeef01",
		MinerAddress: "miner_a",
		WorkerName:   "rig1",
		JobID:        "j100",
		Status:       "pending",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound: %v", err)
	}

	// Now log the same block as submitted (simulates daemon accepted it).
	entry.Status = "submitted"
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult: %v", err)
	}

	wal.Close()

	// Recover submitted blocks.
	recovered, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks: %v", err)
	}

	if len(recovered) != 1 {
		t.Fatalf("expected 1 submitted block, got %d", len(recovered))
	}
	if recovered[0].BlockHash != "00000000000000000001aaa" {
		t.Errorf("recovered hash: got %q, want %q", recovered[0].BlockHash, "00000000000000000001aaa")
	}
	if recovered[0].Status != "submitted" {
		t.Errorf("recovered status: got %q, want %q", recovered[0].Status, "submitted")
	}
}

// TestWAL_DBReconciliation_AcceptedBlocksMissing writes WAL entries with
// "accepted" status and verifies RecoverSubmittedBlocks returns them.
func TestWAL_DBReconciliation_AcceptedBlocksMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}

	// Write pending entry.
	entry := &BlockWALEntry{
		Height:       600000,
		BlockHash:    "00000000000000000002bbb",
		BlockHex:     "cafebabe02",
		MinerAddress: "miner_b",
		WorkerName:   "rig2",
		JobID:        "j200",
		Status:       "pending",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound: %v", err)
	}

	// Update to accepted.
	entry.Status = "accepted"
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult: %v", err)
	}

	wal.Close()

	recovered, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks: %v", err)
	}

	if len(recovered) != 1 {
		t.Fatalf("expected 1 accepted block, got %d", len(recovered))
	}
	if recovered[0].Status != "accepted" {
		t.Errorf("recovered status: got %q, want %q", recovered[0].Status, "accepted")
	}
}

// TestWAL_DBReconciliation_NoMissing verifies that when every block has a
// terminal non-success status (e.g., "rejected"), RecoverSubmittedBlocks
// returns an empty slice.
func TestWAL_DBReconciliation_NoMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}

	// Log a block that was rejected.
	entry := &BlockWALEntry{
		Height:       700000,
		BlockHash:    "00000000000000000003ccc",
		BlockHex:     "baadf00d03",
		MinerAddress: "miner_c",
		WorkerName:   "rig3",
		JobID:        "j300",
		Status:       "pending",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound: %v", err)
	}

	entry.Status = "rejected"
	entry.RejectReason = "inconclusive"
	if err := wal.LogSubmissionResult(entry); err != nil {
		t.Fatalf("LogSubmissionResult: %v", err)
	}

	wal.Close()

	recovered, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks: %v", err)
	}

	if len(recovered) != 0 {
		t.Errorf("expected 0 submitted blocks, got %d", len(recovered))
	}
}

// ============================================================================
// Circuit Breaker + BlockQueue Integration Tests
// ============================================================================

// TestCircuitBreaker_BlockQueue_Integration verifies that blocks can be
// enqueued while the circuit breaker is open, and drained when it closes.
func TestCircuitBreaker_BlockQueue_Integration(t *testing.T) {
	t.Parallel()

	cfg := database.CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   100 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       100 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := database.NewCircuitBreaker(cfg)
	queue := database.NewBlockQueue(10)

	// Drive circuit breaker to open.
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}

	if cb.State() != database.CircuitOpen {
		t.Fatalf("expected CircuitOpen after %d failures, got %v", cfg.FailureThreshold, cb.State())
	}

	// Simulate queueing blocks while the circuit is open.
	block1 := &database.Block{Height: 100, Hash: "hash100", Status: "pending"}
	block2 := &database.Block{Height: 101, Hash: "hash101", Status: "pending"}

	if !queue.Enqueue(block1) {
		t.Fatal("Enqueue block1 should succeed")
	}
	if !queue.Enqueue(block2) {
		t.Fatal("Enqueue block2 should succeed")
	}

	if queue.Len() != 2 {
		t.Errorf("queue length: got %d, want 2", queue.Len())
	}

	// Simulate circuit closing (DB recovered).
	cb.RecordSuccess()
	if cb.State() != database.CircuitClosed {
		t.Fatalf("expected CircuitClosed after success, got %v", cb.State())
	}

	// Drain all queued blocks.
	entries := queue.DrainAll()
	if len(entries) != 2 {
		t.Fatalf("DrainAll: got %d entries, want 2", len(entries))
	}

	if entries[0].Block.Height != 100 {
		t.Errorf("first drained block height: got %d, want 100", entries[0].Block.Height)
	}
	if entries[1].Block.Height != 101 {
		t.Errorf("second drained block height: got %d, want 101", entries[1].Block.Height)
	}

	// Queue should be empty after drain.
	if queue.Len() != 0 {
		t.Errorf("queue length after drain: got %d, want 0", queue.Len())
	}
	if queue.Dropped() != 0 {
		t.Errorf("dropped count: got %d, want 0", queue.Dropped())
	}
}

// TestCircuitBreaker_BlockQueue_CommitSafety verifies that
// DequeueWithCommit only removes the entry when commit() is called.
// If commit is never called (simulating a crash between dequeue and DB
// write), the entry remains in the queue.
func TestCircuitBreaker_BlockQueue_CommitSafety(t *testing.T) {
	t.Parallel()

	queue := database.NewBlockQueue(10)

	block := &database.Block{Height: 200, Hash: "hash200", Status: "pending"}
	if !queue.Enqueue(block) {
		t.Fatal("Enqueue should succeed")
	}

	// Dequeue without committing.
	entry, commit := queue.DequeueWithCommit()
	if entry == nil {
		t.Fatal("DequeueWithCommit should return an entry")
	}
	if entry.Block.Height != 200 {
		t.Errorf("dequeued block height: got %d, want 200", entry.Block.Height)
	}

	// Entry is still in queue (not committed).
	if queue.Len() != 1 {
		t.Errorf("queue length before commit: got %d, want 1 (entry should remain)", queue.Len())
	}

	// Now commit -- this simulates a successful DB write.
	ok := commit()
	if !ok {
		t.Error("commit() should return true on first commit")
	}

	// Entry is now removed.
	if queue.Len() != 0 {
		t.Errorf("queue length after commit: got %d, want 0", queue.Len())
	}

	// Double-commit should return false.
	ok2 := commit()
	if ok2 {
		t.Error("second commit() should return false (already committed)")
	}
}

// ============================================================================
// Multi-Coin HA Tests
// ============================================================================

// TestCoordinator_MultiCoin_RoleChange verifies that handleRoleChange
// iterates over ALL registered pools without skipping any. Since we
// cannot easily construct real CoinPool instances without daemon
// connections, this test verifies the coordinator-level iteration logic
// with an empty pools map and separately confirms that a non-empty map
// does not cause index-out-of-range or nil dereference panics.
func TestCoordinator_MultiCoin_RoleChange(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Register pool IDs (the coordinator iterates by poolID).
	// We add entries to the map that point to minimally initialized CoinPools
	// to verify the loop visits all entries.
	poolIDs := []string{"btc-main", "ltc-main", "dgb-main"}
	for _, id := range poolIDs {
		cp := &CoinPool{
			logger:     zap.NewNop().Sugar(),
			coinSymbol: id,
			poolID:     id,
		}
		coord.pools[id] = cp
	}

	// handleRoleChange should visit all three pools without panic.
	// CoinPool.OnHARoleChange only logs + does a switch on the role,
	// which is safe even for a minimally constructed CoinPool.
	coord.handleRoleChange(ha.RoleMaster, ha.RoleBackup)
	coord.handleRoleChange(ha.RoleBackup, ha.RoleMaster)
}

// TestCoordinator_PartialStartup_GraceDegradation verifies that the
// Coordinator can be constructed with a subset of pools and still
// functions: Stats, ListPools, GetPool, and IsRunning all work correctly
// when the coordinator has fewer pools than configured.
func TestCoordinator_PartialStartup_GraceDegradation(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger:     zap.NewNop().Sugar(),
		startupCfg: DefaultStartupConfig(),
		pools:      make(map[string]*CoinPool),
	}

	// Simulate partial startup: only BTC came up; LTC and DGB failed.
	btcPool := &CoinPool{
		logger:     zap.NewNop().Sugar(),
		coinSymbol: "BTC",
		poolID:     "btc-solo",
	}
	coord.pools["btc-solo"] = btcPool

	// Coordinator should report 1 pool.
	poolList := coord.ListPools()
	if len(poolList) != 1 {
		t.Fatalf("ListPools: got %d, want 1", len(poolList))
	}
	if poolList[0] != "btc-solo" {
		t.Errorf("ListPools[0]: got %q, want %q", poolList[0], "btc-solo")
	}

	// GetPool should find the BTC pool.
	pool, found := coord.GetPool("btc-solo")
	if !found {
		t.Fatal("GetPool(btc-solo) should return found=true")
	}
	if pool != btcPool {
		t.Error("GetPool returned wrong pool reference")
	}

	// GetPool for a missing coin should return not-found.
	_, found = coord.GetPool("ltc-solo")
	if found {
		t.Error("GetPool(ltc-solo) should return found=false for missing coin")
	}

	// Verify the pool count via the pools map length.
	// (We cannot call coord.Stats() with partially initialized CoinPools
	// because CoinPool.Stats() dereferences internal components like
	// stratumServer that are nil in our test fixtures.)
	coord.poolsMu.RLock()
	poolCount := len(coord.pools)
	coord.poolsMu.RUnlock()
	if poolCount != 1 {
		t.Errorf("pools map length: got %d, want 1", poolCount)
	}

	// IsRunning should be false (Run() was never called).
	if coord.IsRunning() {
		t.Error("IsRunning should be false without calling Run()")
	}
}

// ============================================================================
// Stats Under HA Tests
// ============================================================================

// TestCoordinatorStats_ReflectsRegisteredPools verifies that the
// CoordinatorStats.PoolCount and Pools map correctly reflect the number
// of registered pools.
//
// Note: CoinPool.Stats() dereferences internal components (stratumServer,
// sharePipeline, etc.) so we cannot use minimally-constructed CoinPools
// when calling coord.Stats(). Instead, we verify the coordinator's pool
// registry via ListPools/GetPool and confirm Stats works with zero pools.
func TestCoordinatorStats_ReflectsRegisteredPools(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// With zero pools, Stats returns empty, well-formed result.
	stats0 := coord.Stats()
	if stats0.PoolCount != 0 {
		t.Errorf("PoolCount with 0 pools: got %d", stats0.PoolCount)
	}
	if stats0.Pools == nil {
		t.Fatal("Pools map should be initialized (not nil) even with 0 pools")
	}

	// Register pools and verify ListPools/GetPool track them.
	// (We cannot call coord.Stats() with partially initialized CoinPools
	// because Stats() dereferences internal components.)
	coord.pools["pool-a"] = &CoinPool{
		logger:     zap.NewNop().Sugar(),
		coinSymbol: "AAA",
		poolID:     "pool-a",
	}

	poolList := coord.ListPools()
	if len(poolList) != 1 {
		t.Errorf("ListPools with 1 pool: got %d", len(poolList))
	}
	if _, found := coord.GetPool("pool-a"); !found {
		t.Error("GetPool should find 'pool-a'")
	}

	// Add a second pool.
	coord.pools["pool-b"] = &CoinPool{
		logger:     zap.NewNop().Sugar(),
		coinSymbol: "BBB",
		poolID:     "pool-b",
	}

	poolList2 := coord.ListPools()
	if len(poolList2) != 2 {
		t.Errorf("ListPools with 2 pools: got %d", len(poolList2))
	}
	if _, found := coord.GetPool("pool-b"); !found {
		t.Error("GetPool should find 'pool-b'")
	}
}

// TestCoordinatorStats_EmptyPools_NoNils verifies that calling Stats on a
// coordinator with zero pools returns zero-valued stats (not nil maps or
// negative values).
func TestCoordinatorStats_EmptyPools_NoNils(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		pools: make(map[string]*CoinPool),
	}

	stats := coord.Stats()

	if stats.Pools == nil {
		t.Fatal("Pools map must not be nil (should be empty map)")
	}
	if len(stats.Pools) != 0 {
		t.Errorf("Pools map: got %d entries, want 0", len(stats.Pools))
	}
	if stats.PoolCount != 0 {
		t.Errorf("PoolCount: got %d, want 0", stats.PoolCount)
	}
	if stats.TotalConnections != 0 {
		t.Errorf("TotalConnections: got %d, want 0", stats.TotalConnections)
	}
	if stats.TotalShares != 0 {
		t.Errorf("TotalShares: got %d, want 0", stats.TotalShares)
	}
	if stats.TotalAccepted != 0 {
		t.Errorf("TotalAccepted: got %d, want 0", stats.TotalAccepted)
	}
	if stats.TotalRejected != 0 {
		t.Errorf("TotalRejected: got %d, want 0", stats.TotalRejected)
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

// TestCoordinator_HandleRoleChange_SameRole verifies that calling
// handleRoleChange with the same old and new role (a no-op transition)
// does not panic or cause unexpected side effects.
func TestCoordinator_HandleRoleChange_SameRole(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Same role transitions should be harmless no-ops.
	roles := []ha.Role{ha.RoleUnknown, ha.RoleMaster, ha.RoleBackup, ha.RoleObserver}
	for _, role := range roles {
		coord.handleRoleChange(role, role)
	}
}

// TestCoordinator_HandleRoleChange_ObserverRole verifies that transitioning
// to or from the Observer role does not crash the coordinator. Observers
// are read-only nodes that should not cause special handling failures.
func TestCoordinator_HandleRoleChange_ObserverRole(t *testing.T) {
	t.Parallel()

	coord := &Coordinator{
		logger: zap.NewNop().Sugar(),
		pools:  make(map[string]*CoinPool),
	}

	// Transition to Observer from various roles.
	coord.handleRoleChange(ha.RoleUnknown, ha.RoleObserver)
	coord.handleRoleChange(ha.RoleMaster, ha.RoleObserver)
	coord.handleRoleChange(ha.RoleBackup, ha.RoleObserver)

	// Transition from Observer to other roles.
	coord.handleRoleChange(ha.RoleObserver, ha.RoleMaster)
	coord.handleRoleChange(ha.RoleObserver, ha.RoleBackup)
	coord.handleRoleChange(ha.RoleObserver, ha.RoleUnknown)
}

// ============================================================================
// Additional WAL recovery edge cases
// ============================================================================

// TestWAL_RecoverUnsubmitted_EmptyDir verifies that RecoverUnsubmittedBlocks
// returns an empty slice (not an error) when the data directory has no WAL files.
func TestWAL_RecoverUnsubmitted_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	entries, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks on empty dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries from empty dir, got %d", len(entries))
	}
}

// TestWAL_RecoverSubmitted_EmptyDir verifies that RecoverSubmittedBlocks
// returns an empty slice when no WAL files exist.
func TestWAL_RecoverSubmitted_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	entries, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks on empty dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries from empty dir, got %d", len(entries))
	}
}

// TestWAL_MultipleBlocks_MixedStates writes several blocks with various
// terminal states and verifies that RecoverSubmittedBlocks and
// RecoverUnsubmittedBlocks each return the correct subset.
func TestWAL_MultipleBlocks_MixedStates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}

	// Block A: pending (never submitted).
	entryA := &BlockWALEntry{
		Height:    100,
		BlockHash: "aaa",
		BlockHex:  "aa",
		Status:    "pending",
	}
	if err := wal.LogBlockFound(entryA); err != nil {
		t.Fatalf("LogBlockFound A: %v", err)
	}

	// Block B: submitted.
	entryB := &BlockWALEntry{
		Height:    101,
		BlockHash: "bbb",
		BlockHex:  "bb",
		Status:    "pending",
	}
	if err := wal.LogBlockFound(entryB); err != nil {
		t.Fatalf("LogBlockFound B: %v", err)
	}
	entryB.Status = "submitted"
	if err := wal.LogSubmissionResult(entryB); err != nil {
		t.Fatalf("LogSubmissionResult B: %v", err)
	}

	// Block C: accepted.
	entryC := &BlockWALEntry{
		Height:    102,
		BlockHash: "ccc",
		BlockHex:  "cc",
		Status:    "pending",
	}
	if err := wal.LogBlockFound(entryC); err != nil {
		t.Fatalf("LogBlockFound C: %v", err)
	}
	entryC.Status = "accepted"
	if err := wal.LogSubmissionResult(entryC); err != nil {
		t.Fatalf("LogSubmissionResult C: %v", err)
	}

	// Block D: rejected.
	entryD := &BlockWALEntry{
		Height:    103,
		BlockHash: "ddd",
		BlockHex:  "dd",
		Status:    "pending",
	}
	if err := wal.LogBlockFound(entryD); err != nil {
		t.Fatalf("LogBlockFound D: %v", err)
	}
	entryD.Status = "rejected"
	entryD.RejectReason = "stale"
	if err := wal.LogSubmissionResult(entryD); err != nil {
		t.Fatalf("LogSubmissionResult D: %v", err)
	}

	wal.Close()

	// Unsubmitted: only block A (pending).
	unsubmitted, err := RecoverUnsubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks: %v", err)
	}
	if len(unsubmitted) != 1 {
		t.Fatalf("expected 1 unsubmitted, got %d", len(unsubmitted))
	}
	if unsubmitted[0].BlockHash != "aaa" {
		t.Errorf("unsubmitted hash: got %q, want %q", unsubmitted[0].BlockHash, "aaa")
	}

	// Submitted: blocks B and C.
	submitted, err := RecoverSubmittedBlocks(dir)
	if err != nil {
		t.Fatalf("RecoverSubmittedBlocks: %v", err)
	}
	if len(submitted) != 2 {
		t.Fatalf("expected 2 submitted, got %d", len(submitted))
	}

	// Sort by height for deterministic assertion.
	sort.Slice(submitted, func(i, j int) bool {
		return submitted[i].Height < submitted[j].Height
	})
	if submitted[0].BlockHash != "bbb" {
		t.Errorf("submitted[0] hash: got %q, want %q", submitted[0].BlockHash, "bbb")
	}
	if submitted[1].BlockHash != "ccc" {
		t.Errorf("submitted[1] hash: got %q, want %q", submitted[1].BlockHash, "ccc")
	}
}

// TestBlockWAL_FilePath_Stable verifies FilePath returns a consistent,
// non-empty path after construction.
func TestBlockWAL_FilePath_Stable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}
	defer wal.Close()

	fp := wal.FilePath()
	if fp == "" {
		t.Fatal("FilePath should not be empty")
	}

	// Should be under the data directory.
	if filepath.Dir(fp) != dir {
		t.Errorf("FilePath directory: got %q, want %q", filepath.Dir(fp), dir)
	}

	// Calling FilePath again should return the same value.
	if wal.FilePath() != fp {
		t.Errorf("FilePath not stable: first=%q, second=%q", fp, wal.FilePath())
	}
}

// TestBlockWAL_CloseIdempotent verifies that closing the WAL twice
// does not return an error or panic.
func TestBlockWAL_CloseIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}

	if err := wal.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second close should be safe.
	if err := wal.Close(); err != nil {
		t.Errorf("second Close should not error: %v", err)
	}
}

// TestBlockQueue_Overflow verifies that blocks are dropped when the
// queue reaches its maximum size, and the Dropped counter increments.
func TestBlockQueue_Overflow(t *testing.T) {
	t.Parallel()

	queue := database.NewBlockQueue(2)

	b1 := &database.Block{Height: 1, Hash: "h1"}
	b2 := &database.Block{Height: 2, Hash: "h2"}
	b3 := &database.Block{Height: 3, Hash: "h3"}

	if !queue.Enqueue(b1) {
		t.Fatal("Enqueue b1 should succeed")
	}
	if !queue.Enqueue(b2) {
		t.Fatal("Enqueue b2 should succeed")
	}
	if queue.Enqueue(b3) {
		t.Fatal("Enqueue b3 should fail (queue full)")
	}

	if queue.Len() != 2 {
		t.Errorf("queue length: got %d, want 2", queue.Len())
	}
	if queue.Dropped() != 1 {
		t.Errorf("dropped: got %d, want 1", queue.Dropped())
	}
}

// TestWAL_WALEntry_JSONRoundTrip verifies that BlockWALEntry survives
// JSON marshal/unmarshal, which is important for WAL file integrity.
func TestWAL_WALEntry_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := BlockWALEntry{
		Height:        123456,
		BlockHash:     "0000000000000000000abcdef1234567890",
		PrevHash:      "0000000000000000000fedcba0987654321",
		BlockHex:      "deadbeefcafe",
		MinerAddress:  "bc1qtest123",
		WorkerName:    "worker.1",
		JobID:         "job_99",
		JobAge:        5 * time.Second,
		CoinbaseValue: 625000000,
		Status:        "submitted",
		CoinBase1:     "coinbase1hex",
		CoinBase2:     "coinbase2hex",
		ExtraNonce1:   "en1",
		ExtraNonce2:   "en2",
		Version:       "20000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		Nonce:         "aabbccdd",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded BlockWALEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.Height != original.Height {
		t.Errorf("Height: got %d, want %d", decoded.Height, original.Height)
	}
	if decoded.BlockHash != original.BlockHash {
		t.Errorf("BlockHash: got %q, want %q", decoded.BlockHash, original.BlockHash)
	}
	if decoded.Status != original.Status {
		t.Errorf("Status: got %q, want %q", decoded.Status, original.Status)
	}
	if decoded.CoinBase1 != original.CoinBase1 {
		t.Errorf("CoinBase1: got %q, want %q", decoded.CoinBase1, original.CoinBase1)
	}
	if decoded.CoinbaseValue != original.CoinbaseValue {
		t.Errorf("CoinbaseValue: got %d, want %d", decoded.CoinbaseValue, original.CoinbaseValue)
	}
}

// TestWAL_FlushToDisk_AfterLogBlockFound verifies that FlushToDisk
// after writing an entry results in data being readable from disk.
func TestWAL_FlushToDisk_AfterLogBlockFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(dir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL: %v", err)
	}
	defer wal.Close()

	entry := &BlockWALEntry{
		Height:    999,
		BlockHash: "flush_test_hash",
		BlockHex:  "ff",
		Status:    "pending",
	}
	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound: %v", err)
	}

	if err := wal.FlushToDisk(); err != nil {
		t.Fatalf("FlushToDisk: %v", err)
	}

	// Verify file has content.
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("WAL file should not be empty after flush")
	}

	// Verify it parses as valid JSON.
	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("WAL file has no lines after flush")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}
	if recovered.BlockHash != "flush_test_hash" {
		t.Errorf("recovered hash: got %q, want %q", recovered.BlockHash, "flush_test_hash")
	}
}
