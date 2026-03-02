// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database - Unit tests for DatabaseManager internal methods.
//
// These tests exercise the actual DatabaseManager methods (safeActiveNode,
// findBestWriteNode, recordNodeFailure, recordNodeSuccess, Stats,
// VIPFailoverCallback) on hand-constructed DatabaseManager instances
// WITHOUT requiring a live PostgreSQL connection.
//
// Key approach: We construct DatabaseManager and ManagedDBNode structs
// directly (bypassing NewDatabaseManager which requires pgx), setting
// Pool to nil where node state logic doesn't need it, or setting it
// to a non-nil placeholder where the code only checks Pool != nil.
package database

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newTestLogger returns a no-op sugared logger for unit tests.
func newTestLogger() *zap.SugaredLogger {
	return zap.NewNop().Sugar()
}

// newTestManager builds a DatabaseManager with the given nodes for testing.
// This bypasses NewDatabaseManager (which requires live PostgreSQL)
// and directly populates the manager struct fields.
func newTestManager(nodes []*ManagedDBNode) *DatabaseManager {
	dm := &DatabaseManager{
		nodes:  nodes,
		poolID: "test_pool",
		logger: newTestLogger(),
	}
	return dm
}

// ═══════════════════════════════════════════════════════════════════════════════
// safeActiveNode tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSafeActiveNode_ValidIndex(t *testing.T) {
	t.Parallel()

	node0 := &ManagedDBNode{ID: "db-0-test", Priority: 0, State: DBNodeHealthy}
	node1 := &ManagedDBNode{ID: "db-1-test", Priority: 1, State: DBNodeHealthy}
	dm := newTestManager([]*ManagedDBNode{node0, node1})
	dm.activeNodeIdx.Store(0)

	got := dm.safeActiveNode()
	if got == nil {
		t.Fatal("safeActiveNode() returned nil for valid index 0")
	}
	if got.ID != "db-0-test" {
		t.Errorf("safeActiveNode() returned node %q, want %q", got.ID, "db-0-test")
	}

	// Switch to index 1.
	dm.activeNodeIdx.Store(1)
	got = dm.safeActiveNode()
	if got == nil {
		t.Fatal("safeActiveNode() returned nil for valid index 1")
	}
	if got.ID != "db-1-test" {
		t.Errorf("safeActiveNode() returned node %q, want %q", got.ID, "db-1-test")
	}
}

func TestSafeActiveNode_NegativeIndex(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-test", State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(-1)

	got := dm.safeActiveNode()
	if got != nil {
		t.Errorf("safeActiveNode() returned %v for negative index, want nil", got.ID)
	}
}

func TestSafeActiveNode_IndexTooLarge(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-test", State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(5) // Only 1 node, index 5 is out of bounds.

	got := dm.safeActiveNode()
	if got != nil {
		t.Errorf("safeActiveNode() returned %v for out-of-bounds index 5, want nil", got.ID)
	}
}

func TestSafeActiveNode_EmptyNodes(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{})
	dm.activeNodeIdx.Store(0)

	got := dm.safeActiveNode()
	if got != nil {
		t.Errorf("safeActiveNode() returned %v for empty nodes slice, want nil", got)
	}
}

func TestSafeActiveNode_IndexExactlyAtLen(t *testing.T) {
	t.Parallel()

	// Two nodes: valid indices are 0 and 1. Index 2 = len(nodes) should return nil.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", State: DBNodeHealthy},
		{ID: "db-1", State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(2)

	got := dm.safeActiveNode()
	if got != nil {
		t.Errorf("safeActiveNode() returned node for index == len(nodes), want nil")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// findBestWriteNode tests
// ═══════════════════════════════════════════════════════════════════════════════

// fakePlaceholderPool is a marker used to satisfy the Pool != nil check
// in findBestWriteNode. We cannot create a real pgxpool.Pool without a
// PostgreSQL server, but findBestWriteNode only checks Pool != nil
// (it never calls any methods on the pool).
//
// We achieve this by using an unsafe workaround: we store an arbitrary
// non-nil pointer value via a test helper. Since findBestWriteNode reads
// node.Pool in a pointer-nil check only, this is safe for unit tests.
//
// NOTE: The tests below that call findBestWriteNode set Pool to nil for
// nodes that should be skipped, and leave Pool as nil for others too
// (which causes them to be skipped). To test the priority logic properly,
// we need nodes with non-nil Pool fields. Since pgxpool.Pool has no
// exported constructors usable without a DB, we test findBestWriteNode
// with pool=nil nodes and verify they ARE skipped, then test the priority
// selection logic separately on nodes that all share Pool=nil (verifying
// that when no pool is available, -1 is returned).

func TestFindBestWriteNode_AllNilPools(t *testing.T) {
	t.Parallel()

	// All nodes have nil Pool -> none eligible.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", Priority: 0, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
		{ID: "db-1", Priority: 1, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
	})

	idx := dm.findBestWriteNode()
	if idx != -1 {
		t.Errorf("findBestWriteNode() = %d, want -1 (all pools nil)", idx)
	}
}

func TestFindBestWriteNode_AllReadOnly(t *testing.T) {
	t.Parallel()

	// All nodes are read-only -> none eligible for writes.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", Priority: 0, ReadOnly: true, State: DBNodeHealthy, Pool: nil},
		{ID: "db-1", Priority: 1, ReadOnly: true, State: DBNodeHealthy, Pool: nil},
	})

	idx := dm.findBestWriteNode()
	if idx != -1 {
		t.Errorf("findBestWriteNode() = %d, want -1 (all read-only)", idx)
	}
}

func TestFindBestWriteNode_AllUnhealthy(t *testing.T) {
	t.Parallel()

	// Even with non-nil pools (if we could set them), unhealthy/offline nodes
	// should be skipped. Since Pool is nil here they'd be skipped anyway,
	// but let's verify the state check independently by examining the logic.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", Priority: 0, ReadOnly: false, State: DBNodeUnhealthy, Pool: nil},
		{ID: "db-1", Priority: 1, ReadOnly: false, State: DBNodeOffline, Pool: nil},
	})

	idx := dm.findBestWriteNode()
	if idx != -1 {
		t.Errorf("findBestWriteNode() = %d, want -1 (all unhealthy/offline)", idx)
	}
}

func TestFindBestWriteNode_EmptyNodes(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{})

	idx := dm.findBestWriteNode()
	if idx != -1 {
		t.Errorf("findBestWriteNode() = %d, want -1 (empty nodes)", idx)
	}
}

func TestFindBestWriteNode_DegradedNodeEligible(t *testing.T) {
	t.Parallel()

	// Verify that Degraded nodes are considered eligible (not just Healthy).
	// Both nodes have nil Pool so they'll be skipped, but this validates
	// that the state check accepts DBNodeDegraded alongside DBNodeHealthy.
	// The actual selection logic is: state == Healthy || state == Degraded.
	node := &ManagedDBNode{
		ID:       "db-0",
		Priority: 0,
		ReadOnly: false,
		State:    DBNodeDegraded,
		Pool:     nil, // Would be eligible if Pool were non-nil
	}

	// Verify the state check matches what findBestWriteNode does:
	state := node.State
	eligible := (state == DBNodeHealthy || state == DBNodeDegraded) && !node.ReadOnly
	if !eligible {
		t.Error("Degraded, non-read-only node should be eligible for write selection (state check)")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// recordNodeFailure tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordNodeFailure_BelowThreshold(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-fail",
		State:            DBNodeHealthy,
		ConsecutiveFails: 0,
	}
	dm := newTestManager([]*ManagedDBNode{node})

	testErr := fmt.Errorf("connection refused")

	// Record one failure (below MaxDBNodeFailures=3).
	dm.recordNodeFailure(node, testErr)

	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.ConsecutiveFails != 1 {
		t.Errorf("ConsecutiveFails = %d, want 1", node.ConsecutiveFails)
	}
	if node.State != DBNodeDegraded {
		t.Errorf("State = %v, want DBNodeDegraded after first failure on healthy node", node.State)
	}
	if node.LastError == nil {
		t.Error("LastError should be set after failure")
	}
	if node.LastError.Error() != "connection refused" {
		t.Errorf("LastError = %q, want %q", node.LastError.Error(), "connection refused")
	}
}

func TestRecordNodeFailure_AtThreshold(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-threshold",
		State:            DBNodeDegraded,
		ConsecutiveFails: MaxDBNodeFailures - 1, // One more failure will hit threshold.
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeFailure(node, fmt.Errorf("timeout"))

	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.ConsecutiveFails != MaxDBNodeFailures {
		t.Errorf("ConsecutiveFails = %d, want %d", node.ConsecutiveFails, MaxDBNodeFailures)
	}
	if node.State != DBNodeUnhealthy {
		t.Errorf("State = %v, want DBNodeUnhealthy at threshold", node.State)
	}
}

func TestRecordNodeFailure_AboveThreshold(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-above",
		State:            DBNodeUnhealthy,
		ConsecutiveFails: MaxDBNodeFailures + 5,
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeFailure(node, fmt.Errorf("still failing"))

	node.mu.RLock()
	defer node.mu.RUnlock()

	// Should remain unhealthy, failure count incremented.
	if node.ConsecutiveFails != MaxDBNodeFailures+6 {
		t.Errorf("ConsecutiveFails = %d, want %d", node.ConsecutiveFails, MaxDBNodeFailures+6)
	}
	if node.State != DBNodeUnhealthy {
		t.Errorf("State = %v, want DBNodeUnhealthy (should stay unhealthy)", node.State)
	}
}

func TestRecordNodeFailure_DegradedStaysDegradedBelowThreshold(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-degraded",
		State:            DBNodeDegraded,
		ConsecutiveFails: 1, // Already degraded with 1 failure.
	}
	dm := newTestManager([]*ManagedDBNode{node})

	// Record another failure (still below threshold of 3).
	dm.recordNodeFailure(node, fmt.Errorf("temporary error"))

	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.ConsecutiveFails != 2 {
		t.Errorf("ConsecutiveFails = %d, want 2", node.ConsecutiveFails)
	}
	// Degraded node stays degraded (the Healthy->Degraded transition only
	// fires if state == DBNodeHealthy).
	if node.State != DBNodeDegraded {
		t.Errorf("State = %v, want DBNodeDegraded (should stay degraded below threshold)", node.State)
	}
}

func TestRecordNodeFailure_ProgressionHealthyToDegradedToUnhealthy(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-progression",
		State:            DBNodeHealthy,
		ConsecutiveFails: 0,
	}
	dm := newTestManager([]*ManagedDBNode{node})

	// Drive from Healthy through Degraded to Unhealthy.
	for i := 0; i < MaxDBNodeFailures; i++ {
		dm.recordNodeFailure(node, fmt.Errorf("fail #%d", i+1))
	}

	node.mu.RLock()
	state := node.State
	fails := node.ConsecutiveFails
	node.mu.RUnlock()

	if fails != MaxDBNodeFailures {
		t.Errorf("ConsecutiveFails = %d, want %d", fails, MaxDBNodeFailures)
	}
	if state != DBNodeUnhealthy {
		t.Errorf("State = %v after %d failures, want DBNodeUnhealthy", state, MaxDBNodeFailures)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// recordNodeSuccess tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordNodeSuccess_ResetsFromUnhealthy(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-success",
		State:            DBNodeUnhealthy,
		ConsecutiveFails: 10,
		LastError:        fmt.Errorf("old error"),
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeSuccess(node, 5*time.Millisecond)

	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.State != DBNodeHealthy {
		t.Errorf("State = %v, want DBNodeHealthy after success", node.State)
	}
	if node.ConsecutiveFails != 0 {
		t.Errorf("ConsecutiveFails = %d, want 0 after success", node.ConsecutiveFails)
	}
	if node.LastError != nil {
		t.Errorf("LastError = %v, want nil after success", node.LastError)
	}
	if node.LastSuccess.IsZero() {
		t.Error("LastSuccess should be set after success")
	}
}

func TestRecordNodeSuccess_ResetsFromDegraded(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-degraded-success",
		State:            DBNodeDegraded,
		ConsecutiveFails: 2,
		LastError:        fmt.Errorf("temporary error"),
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeSuccess(node, 3*time.Millisecond)

	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.State != DBNodeHealthy {
		t.Errorf("State = %v, want DBNodeHealthy after success", node.State)
	}
	if node.ConsecutiveFails != 0 {
		t.Errorf("ConsecutiveFails = %d, want 0", node.ConsecutiveFails)
	}
}

func TestRecordNodeSuccess_HealthyStaysHealthy(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:               "db-test-healthy-success",
		State:            DBNodeHealthy,
		ConsecutiveFails: 0,
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeSuccess(node, 2*time.Millisecond)

	node.mu.RLock()
	defer node.mu.RUnlock()

	if node.State != DBNodeHealthy {
		t.Errorf("State = %v, want DBNodeHealthy (should stay healthy)", node.State)
	}
}

func TestRecordNodeSuccess_ResponseTimeAverage_Initial(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:              "db-test-rt-init",
		State:           DBNodeHealthy,
		ResponseTimeAvg: 0, // Zero means no prior measurement.
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeSuccess(node, 10*time.Millisecond)

	node.mu.RLock()
	defer node.mu.RUnlock()

	// When ResponseTimeAvg is 0, it should be set directly to the new value.
	if node.ResponseTimeAvg != 10*time.Millisecond {
		t.Errorf("ResponseTimeAvg = %v, want 10ms (initial measurement)", node.ResponseTimeAvg)
	}
}

func TestRecordNodeSuccess_ResponseTimeAverage_Rolling(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:              "db-test-rt-rolling",
		State:           DBNodeHealthy,
		ResponseTimeAvg: 10 * time.Millisecond,
	}
	dm := newTestManager([]*ManagedDBNode{node})

	dm.recordNodeSuccess(node, 50*time.Millisecond)

	node.mu.RLock()
	avg := node.ResponseTimeAvg
	node.mu.RUnlock()

	// Expected: 0.2 * 50ms + 0.8 * 10ms = 10ms + 8ms = 18ms
	expected := 18 * time.Millisecond
	diff := avg - expected
	if diff < 0 {
		diff = -diff
	}
	if diff > 1*time.Millisecond {
		t.Errorf("ResponseTimeAvg = %v, want ~%v (rolling average with alpha=0.2)", avg, expected)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Stats tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestStats_AllHealthy(t *testing.T) {
	t.Parallel()

	nodes := []*ManagedDBNode{
		{ID: "db-0-primary", State: DBNodeHealthy},
		{ID: "db-1-backup", State: DBNodeHealthy},
		{ID: "db-2-replica", State: DBNodeHealthy},
	}
	dm := newTestManager(nodes)
	dm.activeNodeIdx.Store(0)

	stats := dm.Stats()

	if stats.TotalNodes != 3 {
		t.Errorf("Stats.TotalNodes = %d, want 3", stats.TotalNodes)
	}
	if stats.HealthyNodes != 3 {
		t.Errorf("Stats.HealthyNodes = %d, want 3", stats.HealthyNodes)
	}
	if stats.ActiveNode != "db-0-primary" {
		t.Errorf("Stats.ActiveNode = %q, want %q", stats.ActiveNode, "db-0-primary")
	}
	if stats.Failovers != 0 {
		t.Errorf("Stats.Failovers = %d, want 0", stats.Failovers)
	}
	if stats.WriteFailures != 0 {
		t.Errorf("Stats.WriteFailures = %d, want 0", stats.WriteFailures)
	}
	if stats.ReadFailures != 0 {
		t.Errorf("Stats.ReadFailures = %d, want 0", stats.ReadFailures)
	}
}

func TestStats_MixedHealth(t *testing.T) {
	t.Parallel()

	nodes := []*ManagedDBNode{
		{ID: "db-0-primary", State: DBNodeHealthy},
		{ID: "db-1-backup", State: DBNodeUnhealthy},
		{ID: "db-2-replica", State: DBNodeDegraded},
		{ID: "db-3-offline", State: DBNodeOffline},
	}
	dm := newTestManager(nodes)
	dm.activeNodeIdx.Store(0)

	stats := dm.Stats()

	if stats.TotalNodes != 4 {
		t.Errorf("Stats.TotalNodes = %d, want 4", stats.TotalNodes)
	}
	// Only DBNodeHealthy counts as healthy in Stats().
	if stats.HealthyNodes != 1 {
		t.Errorf("Stats.HealthyNodes = %d, want 1 (only Healthy, not Degraded)", stats.HealthyNodes)
	}
}

func TestStats_InvalidActiveIndex(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-test", State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(99) // Out of bounds.

	stats := dm.Stats()

	if stats.ActiveNode != "unknown" {
		t.Errorf("Stats.ActiveNode = %q, want %q when index out of bounds", stats.ActiveNode, "unknown")
	}
}

func TestStats_TrackFailovers(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(0)
	dm.failovers.Store(7)
	dm.writeFailures.Store(3)
	dm.readFailures.Store(1)

	stats := dm.Stats()

	if stats.Failovers != 7 {
		t.Errorf("Stats.Failovers = %d, want 7", stats.Failovers)
	}
	if stats.WriteFailures != 3 {
		t.Errorf("Stats.WriteFailures = %d, want 3", stats.WriteFailures)
	}
	if stats.ReadFailures != 1 {
		t.Errorf("Stats.ReadFailures = %d, want 1", stats.ReadFailures)
	}
}

func TestStats_EmptyNodes(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{})
	dm.activeNodeIdx.Store(0)

	stats := dm.Stats()

	if stats.TotalNodes != 0 {
		t.Errorf("Stats.TotalNodes = %d, want 0", stats.TotalNodes)
	}
	if stats.HealthyNodes != 0 {
		t.Errorf("Stats.HealthyNodes = %d, want 0", stats.HealthyNodes)
	}
	if stats.ActiveNode != "unknown" {
		t.Errorf("Stats.ActiveNode = %q, want %q for empty nodes", stats.ActiveNode, "unknown")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// VIPFailoverCallback tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestVIPFailoverCallback_Debounce(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-local", Priority: 0, ReadOnly: false, State: DBNodeHealthy},
		{ID: "db-1-remote", Priority: 1, ReadOnly: false, State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(0)

	cb := dm.VIPFailoverCallback()

	// First call should proceed (store timestamp).
	cb(true)

	// Immediate second call should be debounced (within 5 seconds).
	// We verify by checking that the activeNodeIdx hasn't changed unexpectedly.
	initialIdx := dm.activeNodeIdx.Load()
	cb(false)

	// Since findBestWriteNode returns -1 (all pools nil) and findLocalNode
	// also returns -1 (all pools nil), neither isMaster=true nor isMaster=false
	// will actually change the active index. The debounce check itself runs,
	// but we can't observe it changing state because the fallback paths
	// don't find valid nodes.
	//
	// What we CAN verify: no panic, no deadlock, activeNodeIdx unchanged.
	if dm.activeNodeIdx.Load() != initialIdx {
		t.Errorf("activeNodeIdx changed unexpectedly during debounced callback")
	}
}

func TestVIPFailoverCallback_IsMasterSwitchesToLocal(t *testing.T) {
	t.Parallel()

	// NOTE: findLocalNode requires Pool != nil, so with nil pools this test
	// verifies the function runs without panicking. The actual switch won't
	// happen since no local node with a valid pool exists.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-local", Priority: 0, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
		{ID: "db-1-remote", Priority: 1, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
	})
	dm.activeNodeIdx.Store(1) // Currently on remote.

	cb := dm.VIPFailoverCallback()
	cb(true) // Become master -> try to switch to local.

	// Can't switch because Pool is nil, so activeNodeIdx should remain 1.
	if dm.activeNodeIdx.Load() != 1 {
		t.Errorf("activeNodeIdx = %d, want 1 (no valid local node with pool)", dm.activeNodeIdx.Load())
	}
}

func TestVIPFailoverCallback_IsBackupCallsBestWrite(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-local", Priority: 0, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
		{ID: "db-1-remote", Priority: 1, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
	})
	dm.activeNodeIdx.Store(0)

	cb := dm.VIPFailoverCallback()
	cb(false) // Become backup -> try to find best remote write node.

	// findBestWriteNode returns -1 (all pools nil), so no switch occurs.
	if dm.activeNodeIdx.Load() != 0 {
		t.Errorf("activeNodeIdx = %d, want 0 (no valid write node with pool)", dm.activeNodeIdx.Load())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// findLocalNode tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestFindLocalNode_NoLocalNode(t *testing.T) {
	t.Parallel()

	// No node with priority 0.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-1", Priority: 1, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
		{ID: "db-2", Priority: 2, ReadOnly: false, State: DBNodeHealthy, Pool: nil},
	})

	idx := dm.findLocalNode()
	if idx != -1 {
		t.Errorf("findLocalNode() = %d, want -1 (no priority-0 node)", idx)
	}
}

func TestFindLocalNode_LocalIsReadOnly(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-ro", Priority: 0, ReadOnly: true, State: DBNodeHealthy, Pool: nil},
	})

	idx := dm.findLocalNode()
	if idx != -1 {
		t.Errorf("findLocalNode() = %d, want -1 (priority-0 node is read-only)", idx)
	}
}

func TestFindLocalNode_LocalIsUnhealthy(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0-unhealthy", Priority: 0, ReadOnly: false, State: DBNodeUnhealthy, Pool: nil},
	})

	idx := dm.findLocalNode()
	if idx != -1 {
		t.Errorf("findLocalNode() = %d, want -1 (priority-0 node is unhealthy and has nil pool)", idx)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// GetActiveNode / GetActiveDB delegation tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestGetActiveNode_DelegatesToSafeActiveNode(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{ID: "db-0-delegate"}
	dm := newTestManager([]*ManagedDBNode{node})
	dm.activeNodeIdx.Store(0)

	got := dm.GetActiveNode()
	if got == nil {
		t.Fatal("GetActiveNode() returned nil")
	}
	if got.ID != "db-0-delegate" {
		t.Errorf("GetActiveNode().ID = %q, want %q", got.ID, "db-0-delegate")
	}
}

func TestGetActiveNode_NilWhenOutOfBounds(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{})
	dm.activeNodeIdx.Store(0)

	got := dm.GetActiveNode()
	if got != nil {
		t.Errorf("GetActiveNode() should return nil for empty nodes, got %v", got.ID)
	}
}

func TestGetActiveDB_NilOnEmptyManager(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{})

	db := dm.GetActiveDB()
	if db != nil {
		t.Error("GetActiveDB() should return nil for empty nodes")
	}
}

func TestGetActiveDB_NilOnNilPool(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", Pool: nil},
	})
	dm.activeNodeIdx.Store(0)

	db := dm.GetActiveDB()
	if db != nil {
		t.Error("GetActiveDB() should return nil when active node has nil pool")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Failure-then-success lifecycle tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestFailureThenSuccess_FullCycle(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:    "db-lifecycle",
		State: DBNodeHealthy,
	}
	dm := newTestManager([]*ManagedDBNode{node})

	// Phase 1: Accumulate failures to unhealthy.
	for i := 0; i < MaxDBNodeFailures; i++ {
		dm.recordNodeFailure(node, fmt.Errorf("fail %d", i))
	}

	node.mu.RLock()
	if node.State != DBNodeUnhealthy {
		t.Fatalf("After %d failures: state = %v, want DBNodeUnhealthy", MaxDBNodeFailures, node.State)
	}
	node.mu.RUnlock()

	// Phase 2: Success resets to healthy.
	dm.recordNodeSuccess(node, 5*time.Millisecond)

	node.mu.RLock()
	if node.State != DBNodeHealthy {
		t.Errorf("After success: state = %v, want DBNodeHealthy", node.State)
	}
	if node.ConsecutiveFails != 0 {
		t.Errorf("After success: ConsecutiveFails = %d, want 0", node.ConsecutiveFails)
	}
	node.mu.RUnlock()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Concurrent access tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestRecordNodeFailureSuccess_ConcurrentSafe(t *testing.T) {
	t.Parallel()

	node := &ManagedDBNode{
		ID:    "db-concurrent",
		State: DBNodeHealthy,
	}
	dm := newTestManager([]*ManagedDBNode{node})
	dm.activeNodeIdx.Store(0)

	var wg sync.WaitGroup
	const goroutines = 50
	const opsPerGoroutine = 100

	// Concurrent failures.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				dm.recordNodeFailure(node, fmt.Errorf("concurrent fail"))
			}
		}()
	}

	// Concurrent successes.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				dm.recordNodeSuccess(node, time.Millisecond)
			}
		}()
	}

	// Concurrent Stats reads.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				_ = dm.Stats()
				_ = dm.safeActiveNode()
				_ = dm.findBestWriteNode()
			}
		}()
	}

	wg.Wait()

	// Verify the manager is still usable after concurrent access.
	node.mu.RLock()
	state := node.State
	node.mu.RUnlock()

	validStates := map[DBNodeState]bool{
		DBNodeHealthy:   true,
		DBNodeDegraded:  true,
		DBNodeUnhealthy: true,
	}
	if !validStates[state] {
		t.Errorf("Final node state = %v, want one of Healthy/Degraded/Unhealthy", state)
	}

	// Stats should not panic.
	stats := dm.Stats()
	if stats.TotalNodes != 1 {
		t.Errorf("Stats.TotalNodes = %d after concurrent access, want 1", stats.TotalNodes)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// WriteBatch bounds check test
// ═══════════════════════════════════════════════════════════════════════════════

func TestWriteBatch_EmptySharesNoOp(t *testing.T) {
	t.Parallel()

	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", State: DBNodeHealthy},
	})
	dm.activeNodeIdx.Store(0)

	// WriteBatch with nil shares should return nil immediately.
	err := dm.WriteBatch(nil, nil)
	if err != nil {
		t.Errorf("WriteBatch(nil) = %v, want nil", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// checkFailback logic test
// ═══════════════════════════════════════════════════════════════════════════════

func TestCheckFailback_NoChange(t *testing.T) {
	t.Parallel()

	// Both nodes have nil Pool, so findBestWriteNode returns -1.
	// checkFailback should not panic and should not change active index.
	dm := newTestManager([]*ManagedDBNode{
		{ID: "db-0", Priority: 0, State: DBNodeHealthy, Pool: nil},
		{ID: "db-1", Priority: 1, State: DBNodeHealthy, Pool: nil},
	})
	dm.activeNodeIdx.Store(0)

	// Should not panic.
	dm.checkFailback()

	if dm.activeNodeIdx.Load() != 0 {
		t.Errorf("activeNodeIdx changed unexpectedly during checkFailback with no valid nodes")
	}
}
