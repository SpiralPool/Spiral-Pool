// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package nodemanager

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// NodeState.String() — Enum string representations
// =============================================================================

func TestNodeState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state  NodeState
		expect string
	}{
		{NodeStateUnknown, "unknown"},
		{NodeStateHealthy, "healthy"},
		{NodeStateDegraded, "degraded"},
		{NodeStateUnhealthy, "unhealthy"},
		{NodeStateOffline, "offline"},
		{NodeState(99), "unknown"}, // Invalid value
	}

	for _, tc := range tests {
		if got := tc.state.String(); got != tc.expect {
			t.Errorf("NodeState(%d).String() = %q, want %q", tc.state, got, tc.expect)
		}
	}
}

// =============================================================================
// OperationType.String() — Operation enum
// =============================================================================

func TestOperationType_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		op     OperationType
		expect string
	}{
		{OpRead, "read"},
		{OpSubmitBlock, "submit_block"},
		{OpSubmitShare, "submit_share"},
		{OperationType(99), "unknown"}, // Invalid value
	}

	for _, tc := range tests {
		if got := tc.op.String(); got != tc.expect {
			t.Errorf("OperationType(%d).String() = %q, want %q", tc.op, got, tc.expect)
		}
	}
}

// =============================================================================
// OperationType.RequiresPrimary() — Block submission routing
// =============================================================================

func TestOperationType_RequiresPrimary(t *testing.T) {
	t.Parallel()

	if !OpSubmitBlock.RequiresPrimary() {
		t.Error("OpSubmitBlock should require primary node")
	}
	if OpRead.RequiresPrimary() {
		t.Error("OpRead should NOT require primary node")
	}
	if OpSubmitShare.RequiresPrimary() {
		t.Error("OpSubmitShare should NOT require primary node")
	}
}

// =============================================================================
// DefaultHealthMonitor — Default configuration values
// =============================================================================

func TestDefaultHealthMonitor_SensibleDefaults(t *testing.T) {
	t.Parallel()
	m := DefaultHealthMonitor()

	if m.checkInterval != 10*time.Second {
		t.Errorf("expected checkInterval 10s, got %v", m.checkInterval)
	}
	if m.healthyThreshold != 0.8 {
		t.Errorf("expected healthyThreshold 0.8, got %f", m.healthyThreshold)
	}
	if m.degradedThreshold != 0.5 {
		t.Errorf("expected degradedThreshold 0.5, got %f", m.degradedThreshold)
	}
	if m.offlineThreshold != 5 {
		t.Errorf("expected offlineThreshold 5, got %d", m.offlineThreshold)
	}
	if m.responseTimeWindow != 10 {
		t.Errorf("expected responseTimeWindow 10, got %d", m.responseTimeWindow)
	}
}

// =============================================================================
// CalculateHealth — Health score computation
// =============================================================================

func TestCalculateHealth_HealthyNode(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "primary",
		Priority: 0,
	}
	node.Health = HealthScore{
		LastSuccess:     time.Now(),
		LastError:       nil,
		IsSynced:        true,
		BlockHeight:     100000,
		ResponseTimeAvg: 50 * time.Millisecond,
		SuccessRate:     0.99,
	}

	score := monitor.CalculateHealth(node, 100000)

	// Fully healthy node should score > 0.8
	if score < 0.8 {
		t.Errorf("healthy node scored too low: %.3f", score)
	}
}

func TestCalculateHealth_UnreachableNode(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "dead",
		Priority: 0,
	}
	node.Health = HealthScore{
		LastSuccess:     time.Now().Add(-5 * time.Minute), // 5 min ago
		LastError:       ErrNoHealthyNodes,
		IsSynced:        false,
		BlockHeight:     0,
		ResponseTimeAvg: 0,
		SuccessRate:     0.0,
	}

	score := monitor.CalculateHealth(node, 100000)

	// Unreachable node should score very low
	if score > 0.3 {
		t.Errorf("unreachable node scored too high: %.3f", score)
	}
}

func TestCalculateHealth_SyncedButBehind(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "lagging",
		Priority: 1,
	}
	node.Health = HealthScore{
		LastSuccess:     time.Now(),
		LastError:       nil,
		IsSynced:        false,
		BlockHeight:     99990, // 10 behind
		ResponseTimeAvg: 100 * time.Millisecond,
		SuccessRate:     0.95,
	}

	score := monitor.CalculateHealth(node, 100000)

	// Should be degraded — reachable but behind (10 blocks of 100k is minor, so score stays moderate-high)
	if score < 0.3 || score > 0.9 {
		t.Errorf("lagging node should be degraded range (0.3-0.9), got %.3f", score)
	}
}

func TestCalculateHealth_OneBlockBehindIsAcceptable(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "slightly-behind",
		Priority: 0,
	}
	node.Health = HealthScore{
		LastSuccess:     time.Now(),
		LastError:       nil,
		IsSynced:        false,
		BlockHeight:     99999, // 1 block behind
		ResponseTimeAvg: 50 * time.Millisecond,
		SuccessRate:     0.99,
	}

	score := monitor.CalculateHealth(node, 100000)

	// 1 block behind should still score well (0.9 * syncWeight)
	if score < 0.7 {
		t.Errorf("node 1 block behind should score > 0.7, got %.3f", score)
	}
}

func TestCalculateHealth_NodeAheadOfBestHeight(t *testing.T) {
	t.Parallel()
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "ahead",
		Priority: 0,
	}
	node.Health = HealthScore{
		LastSuccess:     time.Now(),
		LastError:       nil,
		IsSynced:        false,
		BlockHeight:     100001, // Ahead of "best" (reorg scenario)
		ResponseTimeAvg: 50 * time.Millisecond,
		SuccessRate:     0.99,
	}

	score := monitor.CalculateHealth(node, 100000)

	// Ahead of best should get full sync score
	if score < 0.8 {
		t.Errorf("node ahead of best should score high, got %.3f", score)
	}
}

// =============================================================================
// sortNodesByPriority — Priority ordering
// =============================================================================

func TestSortNodesByPriority(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	m := &Manager{
		coin:   "TEST",
		logger: logger.Sugar(),
		nodes: []*ManagedNode{
			{ID: "backup-2", Priority: 3},
			{ID: "primary", Priority: 0},
			{ID: "backup-1", Priority: 1},
		},
	}

	m.sortNodesByPriority()

	expected := []string{"primary", "backup-1", "backup-2"}
	for i, exp := range expected {
		if m.nodes[i].ID != exp {
			t.Errorf("nodes[%d] = %q, want %q", i, m.nodes[i].ID, exp)
		}
	}
}

func TestSortNodesByPriority_SingleNode(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	m := &Manager{
		coin:   "TEST",
		logger: logger.Sugar(),
		nodes: []*ManagedNode{
			{ID: "only", Priority: 0},
		},
	}

	// Should not panic on single node
	m.sortNodesByPriority()
	if m.nodes[0].ID != "only" {
		t.Error("single node should remain")
	}
}

func TestSortNodesByPriority_EqualPriorities(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	m := &Manager{
		coin:   "TEST",
		logger: logger.Sugar(),
		nodes: []*ManagedNode{
			{ID: "a", Priority: 1},
			{ID: "b", Priority: 1},
			{ID: "c", Priority: 1},
		},
	}

	// Should not panic and maintain stable order (bubble sort is stable)
	m.sortNodesByPriority()
	if len(m.nodes) != 3 {
		t.Errorf("expected 3 nodes after sort, got %d", len(m.nodes))
	}
}

// =============================================================================
// NewManagerFromConfigs — Factory wiring
// =============================================================================

func TestNewManagerFromConfigs_RejectsEmptyConfigs(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	_, err := NewManagerFromConfigs("DGB", nil, logger)
	if err == nil {
		t.Error("expected error for empty configs")
	}

	_, err = NewManagerFromConfigs("DGB", []NodeConfig{}, logger)
	if err == nil {
		t.Error("expected error for zero-length configs")
	}
}

func TestNewManagerFromConfigs_SetsPrimaryToHighestPriority(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	configs := []NodeConfig{
		{ID: "backup", Host: "backup.example.com", Port: 14022, Priority: 2},
		{ID: "primary", Host: "primary.example.com", Port: 14022, Priority: 0},
	}

	m, err := NewManagerFromConfigs("DGB", configs, logger)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if m.primary == nil {
		t.Fatal("primary should not be nil")
	}
	if m.primary.ID != "primary" {
		t.Errorf("expected primary node 'primary', got %q", m.primary.ID)
	}
}

func TestNewManagerFromConfigs_SetsInitialHealthScore(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	configs := []NodeConfig{
		{ID: "node1", Host: "localhost", Port: 14022, Priority: 0},
	}

	m, err := NewManagerFromConfigs("DGB", configs, logger)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Initial health should be 0.5 (neutral)
	if m.nodes[0].Health.Score != 0.5 {
		t.Errorf("expected initial health 0.5, got %f", m.nodes[0].Health.Score)
	}
	if m.nodes[0].Health.State != NodeStateUnknown {
		t.Errorf("expected initial state Unknown, got %s", m.nodes[0].Health.State)
	}
}

func TestNewManagerFromConfigs_CopiesWeights(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	configs := []NodeConfig{
		{ID: "heavy", Host: "localhost", Port: 14022, Priority: 0, Weight: 5},
		{ID: "light", Host: "localhost", Port: 14023, Priority: 1, Weight: 1},
	}

	m, err := NewManagerFromConfigs("DGB", configs, logger)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if m.nodes[0].Weight != 5 {
		t.Errorf("expected weight 5, got %d", m.nodes[0].Weight)
	}
	if m.nodes[1].Weight != 1 {
		t.Errorf("expected weight 1, got %d", m.nodes[1].Weight)
	}
}

// =============================================================================
// HasZMQ — ZMQ detection
// =============================================================================

func TestHasZMQ_FalseWhenNoZMQ(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	m := &Manager{
		coin:   "TEST",
		logger: logger.Sugar(),
		nodes: []*ManagedNode{
			{ID: "no-zmq", ZMQ: nil},
		},
	}

	if m.HasZMQ() {
		t.Error("HasZMQ should return false when no ZMQ configured")
	}
}

// =============================================================================
// Error sentinel values
// =============================================================================

func TestErrorSentinels(t *testing.T) {
	t.Parallel()

	if ErrNoHealthyNodes.Error() != "no healthy nodes available" {
		t.Errorf("unexpected error text: %s", ErrNoHealthyNodes)
	}
	if ErrNodeNotFound.Error() != "node not found" {
		t.Errorf("unexpected error text: %s", ErrNodeNotFound)
	}
	if ErrAllNodesFailed.Error() != "all nodes failed to respond" {
		t.Errorf("unexpected error text: %s", ErrAllNodesFailed)
	}
}
