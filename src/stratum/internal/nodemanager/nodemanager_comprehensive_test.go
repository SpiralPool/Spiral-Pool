// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Tests for SelectNode routing, ExecuteOnAll ordering/fallback,
// maybeFailover anti-flapping, and Stats completeness.
//
// These tests construct Manager and ManagedNode structs directly
// (no daemon.Client needed) to test the decision-making logic
// without network dependencies.
package nodemanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test helpers
// ═══════════════════════════════════════════════════════════════════════════════

// newTestManager creates a minimal Manager with the given nodes.
// The first node is set as primary.
func newTestManager(nodes []*ManagedNode) *Manager {
	m := &Manager{
		coin:    "TEST",
		nodes:   nodes,
		logger:  zap.NewNop().Sugar(),
		monitor: DefaultHealthMonitor(),
	}
	if len(nodes) > 0 {
		m.primary = nodes[0]
	}
	return m
}

// newTestNode creates a ManagedNode with the given ID, health score, state,
// and priority. ZMQ and Client are nil (not needed for logic tests).
func newTestNode(id string, score float64, state NodeState, priority int) *ManagedNode {
	return &ManagedNode{
		ID:       id,
		Priority: priority,
		Health: HealthScore{
			Score:       score,
			State:       state,
			SuccessRate: score,
			LastSuccess: time.Now(),
			IsSynced:    state == NodeStateHealthy || state == NodeStateDegraded,
			BlockHeight: 100000,
		},
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// SelectNode tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSelectNode_OpSubmitBlock_ReturnsPrimary(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.9, NodeStateHealthy, 0)
	backup := newTestNode("backup", 0.95, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	got, err := m.SelectNode(OpSubmitBlock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "primary" {
		t.Errorf("SelectNode(OpSubmitBlock) = %q, want primary", got.ID)
	}
}

func TestSelectNode_OpSubmitBlock_FallsThrough_WhenPrimaryLowScore(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.2, NodeStateUnhealthy, 0) // Below 0.3 threshold
	backup := newTestNode("backup", 0.9, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	got, err := m.SelectNode(OpSubmitBlock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "backup" {
		t.Errorf("SelectNode(OpSubmitBlock) with degraded primary = %q, want backup", got.ID)
	}
}

func TestSelectNode_OpRead_PicksBestHealth(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.6, NodeStateDegraded, 0)
	backup := newTestNode("backup", 0.95, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	got, err := m.SelectNode(OpRead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "backup" {
		t.Errorf("SelectNode(OpRead) = %q, want backup (highest score)", got.ID)
	}
}

func TestSelectNode_OpSubmitShare_PicksBestHealth(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.6, NodeStateDegraded, 0)
	backup := newTestNode("backup", 0.95, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	got, err := m.SelectNode(OpSubmitShare)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "backup" {
		t.Errorf("SelectNode(OpSubmitShare) = %q, want backup (highest score)", got.ID)
	}
}

func TestSelectNode_AllUnhealthy_ReturnsBestAvailable(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("node1", 0.05, NodeStateOffline, 0) // Below 0.1 threshold
	n2 := newTestNode("node2", 0.08, NodeStateOffline, 1)
	m := newTestManager([]*ManagedNode{n1, n2})

	// BUG FIX: SelectNode now always returns the best available node
	// rather than returning ErrNoHealthyNodes. Low scores are used for
	// SELECTION (pick the best), not REJECTION (refuse to try).
	got, err := m.SelectNode(OpRead)
	if err != nil {
		t.Errorf("SelectNode with all unhealthy should return best, got error: %v", err)
	}
	if got != nil && got.ID != "node2" {
		t.Errorf("expected best node 'node2' (highest score), got %q", got.ID)
	}
}

func TestSelectNode_NilPrimary_OpSubmitBlock_FindsBest(t *testing.T) {
	t.Parallel()
	backup := newTestNode("backup", 0.9, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{backup})
	m.primary = nil

	got, err := m.SelectNode(OpSubmitBlock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "backup" {
		t.Errorf("SelectNode(OpSubmitBlock) with nil primary = %q, want backup", got.ID)
	}
}

func TestSelectNode_PrimaryExactlyAtThreshold(t *testing.T) {
	t.Parallel()
	// Primary score exactly 0.3 — condition is > 0.3, so 0.3 does NOT qualify
	primary := newTestNode("primary", 0.3, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.9, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	got, err := m.SelectNode(OpSubmitBlock)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 0.3 is NOT > 0.3, so primary is rejected; falls through to best node
	if got.ID != "backup" {
		t.Errorf("primary at exactly 0.3 should be rejected, got %q", got.ID)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ExecuteOnAll tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestExecuteOnAll_SortsByHealth_BestFirst(t *testing.T) {
	t.Parallel()
	lowScore := newTestNode("low", 0.3, NodeStateDegraded, 0)
	midScore := newTestNode("mid", 0.6, NodeStateDegraded, 1)
	highScore := newTestNode("high", 0.95, NodeStateHealthy, 2)
	m := newTestManager([]*ManagedNode{lowScore, midScore, highScore})

	var order []string
	err := m.ExecuteOnAll(context.Background(), func(node *ManagedNode) error {
		order = append(order, node.ID)
		return errors.New("intentional failure")
	})

	if err == nil {
		t.Fatal("expected error when all nodes fail")
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(order))
	}
	if order[0] != "high" {
		t.Errorf("first attempt should be highest score node 'high', got %q", order[0])
	}
	if order[1] != "mid" {
		t.Errorf("second attempt should be 'mid', got %q", order[1])
	}
	if order[2] != "low" {
		t.Errorf("third attempt should be 'low', got %q", order[2])
	}
}

func TestExecuteOnAll_StopsOnFirstSuccess(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("fail", 0.9, NodeStateHealthy, 0)
	n2 := newTestNode("succeed", 0.8, NodeStateHealthy, 1)
	n3 := newTestNode("never", 0.7, NodeStateHealthy, 2)
	m := newTestManager([]*ManagedNode{n1, n2, n3})

	callCount := 0
	err := m.ExecuteOnAll(context.Background(), func(node *ManagedNode) error {
		callCount++
		if node.ID == "fail" {
			return errors.New("node down")
		}
		return nil
	})

	if err != nil {
		t.Errorf("expected nil error on success, got: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (fail + succeed), got %d", callCount)
	}
}

func TestExecuteOnAll_AllFail_ReturnsLastError(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("n1", 0.5, NodeStateDegraded, 0)
	n2 := newTestNode("n2", 0.4, NodeStateDegraded, 1)
	m := newTestManager([]*ManagedNode{n1, n2})

	finalErr := errors.New("final failure")
	callCount := 0
	err := m.ExecuteOnAll(context.Background(), func(node *ManagedNode) error {
		callCount++
		if callCount == 2 {
			return finalErr
		}
		return errors.New("first failure")
	})

	if err == nil {
		t.Fatal("expected error when all fail")
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestExecuteOnAll_SingleNode_Success(t *testing.T) {
	t.Parallel()
	n := newTestNode("only", 0.9, NodeStateHealthy, 0)
	m := newTestManager([]*ManagedNode{n})

	called := false
	err := m.ExecuteOnAll(context.Background(), func(node *ManagedNode) error {
		called = true
		return nil
	})

	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if !called {
		t.Error("function was never called")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// maybeFailover tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestMaybeFailover_NoSwitch_WhenPrimaryHealthy(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.9, NodeStateHealthy, 0)
	backup := newTestNode("backup", 0.95, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	if m.primary.ID != "primary" {
		t.Errorf("primary should remain when healthy, got %q", m.primary.ID)
	}
	if m.failoverCount != 0 {
		t.Errorf("failoverCount should be 0, got %d", m.failoverCount)
	}
}

func TestMaybeFailover_NoSwitch_WhenDegradedAboveThreshold(t *testing.T) {
	t.Parallel()
	// Degraded with score >= 0.5 is considered acceptable
	primary := newTestNode("primary", 0.6, NodeStateDegraded, 0)
	backup := newTestNode("backup", 0.95, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	if m.primary.ID != "primary" {
		t.Errorf("primary should remain when degraded but score >= 0.5, got %q", m.primary.ID)
	}
}

func TestMaybeFailover_AntiFlapping_NoSwitch_BelowThreshold(t *testing.T) {
	t.Parallel()
	// Primary is unhealthy (0.3), backup is only 0.15 better (0.45)
	// Anti-flapping requires >= 0.2 improvement
	primary := newTestNode("primary", 0.3, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.45, NodeStateDegraded, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	if m.primary.ID != "primary" {
		t.Errorf("should NOT failover when improvement < 0.2 (anti-flapping), got %q", m.primary.ID)
	}
	if m.failoverCount != 0 {
		t.Errorf("failoverCount should be 0, got %d", m.failoverCount)
	}
}

func TestMaybeFailover_Switches_WhenImprovementAboveThreshold(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.3, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.8, NodeStateHealthy, 1) // 0.5 improvement > 0.2
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	if m.primary.ID != "backup" {
		t.Errorf("should failover when improvement >= 0.2, primary is now %q", m.primary.ID)
	}
	if m.failoverCount != 1 {
		t.Errorf("failoverCount should be 1, got %d", m.failoverCount)
	}
}

func TestMaybeFailover_ExactThreshold_ShouldSwitch(t *testing.T) {
	t.Parallel()
	// Primary 0.3, backup 0.5 → improvement exactly 0.2
	// Code: if bestScore < primaryScore+0.2 { return } → 0.5 < 0.5 → false → proceed
	primary := newTestNode("primary", 0.3, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.5, NodeStateDegraded, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	if m.primary.ID != "backup" {
		t.Errorf("at exactly 0.2 improvement, failover should occur, primary is %q", m.primary.ID)
	}
}

func TestMaybeFailover_EventRecorded(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.2, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.9, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	if len(m.failoverHistory) != 1 {
		t.Fatalf("expected 1 failover event, got %d", len(m.failoverHistory))
	}
	event := m.failoverHistory[0]
	if event.FromNodeID != "primary" {
		t.Errorf("event.FromNodeID = %q, want primary", event.FromNodeID)
	}
	if event.ToNodeID != "backup" {
		t.Errorf("event.ToNodeID = %q, want backup", event.ToNodeID)
	}
	if event.NewScore <= event.OldScore {
		t.Errorf("event.NewScore (%f) should be > OldScore (%f)", event.NewScore, event.OldScore)
	}
	if event.Reason != "health_score" {
		t.Errorf("event.Reason = %q, want health_score", event.Reason)
	}
	if event.OccurredAt.IsZero() {
		t.Error("event.OccurredAt should be set")
	}
}

func TestMaybeFailover_NilPrimary_SwitchesToBest(t *testing.T) {
	t.Parallel()
	n := newTestNode("only", 0.9, NodeStateHealthy, 0)
	m := newTestManager([]*ManagedNode{n})
	m.primary = nil

	m.maybeFailover()

	if m.primary == nil || m.primary.ID != "only" {
		t.Error("with nil primary, should switch to best available node")
	}
}

func TestMaybeFailover_CallbackInvoked(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.2, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.9, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	// performFailover invokes the callback in a goroutine (go m.onFailover(event)),
	// so we use a channel to synchronize.
	done := make(chan FailoverEvent, 1)
	m.onFailover = func(e FailoverEvent) {
		done <- e
	}

	m.maybeFailover()

	select {
	case event := <-done:
		if event.ToNodeID != "backup" {
			t.Errorf("onFailover callback ToNodeID=%q, want backup", event.ToNodeID)
		}
	case <-time.After(time.Second):
		t.Fatal("onFailover callback was not invoked within 1 second")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Stats tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestStats_CompleteCounts(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("n1", 0.9, NodeStateHealthy, 0)
	n2 := newTestNode("n2", 0.6, NodeStateDegraded, 1)
	n3 := newTestNode("n3", 0.1, NodeStateOffline, 2)
	m := newTestManager([]*ManagedNode{n1, n2, n3})
	m.bestHeight = 500000

	stats := m.Stats()

	if stats.Coin != "TEST" {
		t.Errorf("Coin = %q, want TEST", stats.Coin)
	}
	if stats.TotalNodes != 3 {
		t.Errorf("TotalNodes = %d, want 3", stats.TotalNodes)
	}
	// Healthy + Degraded count as healthy
	if stats.HealthyNodes != 2 {
		t.Errorf("HealthyNodes = %d, want 2 (healthy + degraded)", stats.HealthyNodes)
	}
	if stats.PrimaryNodeID != "n1" {
		t.Errorf("PrimaryNodeID = %q, want n1", stats.PrimaryNodeID)
	}
	if stats.BlockHeight != 500000 {
		t.Errorf("BlockHeight = %d, want 500000", stats.BlockHeight)
	}
	if stats.FailoverCount != 0 {
		t.Errorf("FailoverCount = %d, want 0", stats.FailoverCount)
	}
	if stats.LastFailover != nil {
		t.Error("LastFailover should be nil with no failovers")
	}
}

func TestStats_NodeHealthsMap(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("n1", 0.9, NodeStateHealthy, 0)
	n2 := newTestNode("n2", 0.6, NodeStateDegraded, 1)
	m := newTestManager([]*ManagedNode{n1, n2})

	stats := m.Stats()

	if len(stats.NodeHealths) != 2 {
		t.Fatalf("expected 2 entries in NodeHealths, got %d", len(stats.NodeHealths))
	}
	if stats.NodeHealths["n1"] != 0.9 {
		t.Errorf("NodeHealths[n1] = %f, want 0.9", stats.NodeHealths["n1"])
	}
	if stats.NodeHealths["n2"] != 0.6 {
		t.Errorf("NodeHealths[n2] = %f, want 0.6", stats.NodeHealths["n2"])
	}
}

func TestStats_FailoverCountAndHistory(t *testing.T) {
	t.Parallel()
	primary := newTestNode("primary", 0.2, NodeStateUnhealthy, 0)
	backup := newTestNode("backup", 0.9, NodeStateHealthy, 1)
	m := newTestManager([]*ManagedNode{primary, backup})

	m.maybeFailover()

	stats := m.Stats()
	if stats.FailoverCount != 1 {
		t.Errorf("FailoverCount = %d, want 1", stats.FailoverCount)
	}
	if stats.LastFailover == nil {
		t.Fatal("LastFailover should not be nil after failover")
	}
	if stats.LastFailover.ToNodeID != "backup" {
		t.Errorf("LastFailover.ToNodeID = %q, want backup", stats.LastFailover.ToNodeID)
	}
}

func TestStats_PeerCount_FromPrimary(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("n1", 0.9, NodeStateHealthy, 0)
	n1.Health.Connections = 42
	m := newTestManager([]*ManagedNode{n1})

	stats := m.Stats()
	if stats.PeerCount != 42 {
		t.Errorf("PeerCount = %d, want 42", stats.PeerCount)
	}
}

func TestStats_NoPrimary_PeerCountUnavailable(t *testing.T) {
	t.Parallel()
	n1 := newTestNode("n1", 0.9, NodeStateHealthy, 0)
	m := newTestManager([]*ManagedNode{n1})
	m.primary = nil

	stats := m.Stats()
	if stats.PeerCount != -1 {
		t.Errorf("PeerCount without primary = %d, want -1", stats.PeerCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CalculateHealth supplemental tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestCalculateHealth_ResponseTimeWeight(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	// Fast node (< 100ms): gets full response time weight
	fast := newTestNode("fast", 0, NodeStateHealthy, 0)
	fast.Health.ResponseTimeAvg = 50 * time.Millisecond
	fast.Health.LastSuccess = time.Now()
	fast.Health.SuccessRate = 1.0
	fast.Health.IsSynced = true
	fast.Health.BlockHeight = 100000

	// Slow node (> 2000ms): gets zero response time weight
	slow := newTestNode("slow", 0, NodeStateHealthy, 0)
	slow.Health.ResponseTimeAvg = 3000 * time.Millisecond
	slow.Health.LastSuccess = time.Now()
	slow.Health.SuccessRate = 1.0
	slow.Health.IsSynced = true
	slow.Health.BlockHeight = 100000

	scoreF := monitor.CalculateHealth(fast, 100000)
	scoreS := monitor.CalculateHealth(slow, 100000)

	if scoreF <= scoreS {
		t.Errorf("fast node score (%f) should be > slow node score (%f)", scoreF, scoreS)
	}
	// Difference should be approximately 0.20 (wResponseTime weight)
	diff := scoreF - scoreS
	if diff < 0.15 || diff > 0.25 {
		t.Errorf("score difference = %f, expected ~0.20 (response time weight)", diff)
	}
}

func TestCalculateHealth_PriorityWeight(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	high := newTestNode("high", 0, NodeStateHealthy, 0)
	high.Health.ResponseTimeAvg = 50 * time.Millisecond
	high.Health.LastSuccess = time.Now()
	high.Health.SuccessRate = 1.0
	high.Health.IsSynced = true
	high.Health.BlockHeight = 100000

	low := newTestNode("low", 0, NodeStateHealthy, 10)
	low.Health.ResponseTimeAvg = 50 * time.Millisecond
	low.Health.LastSuccess = time.Now()
	low.Health.SuccessRate = 1.0
	low.Health.IsSynced = true
	low.Health.BlockHeight = 100000

	scoreH := monitor.CalculateHealth(high, 100000)
	scoreL := monitor.CalculateHealth(low, 100000)

	if scoreH <= scoreL {
		t.Errorf("high priority score (%f) should be > low priority score (%f)", scoreH, scoreL)
	}
	// Difference should be approximately 0.05 (wPriority weight)
	diff := scoreH - scoreL
	if diff < 0.03 || diff > 0.07 {
		t.Errorf("score difference = %f, expected ~0.05 (priority weight)", diff)
	}
}
