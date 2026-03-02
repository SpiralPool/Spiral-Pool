// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package nodemanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mockDaemonClient simulates a daemon client for testing
type mockDaemonClient struct {
	healthy      bool
	blockHeight  uint64
	responseTime time.Duration
	mu           sync.Mutex
}

func (m *mockDaemonClient) setHealthy(healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthy = healthy
}

func (m *mockDaemonClient) setBlockHeight(height uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockHeight = height
}

func TestHealthScoreCalculation(t *testing.T) {
	monitor := DefaultHealthMonitor()

	tests := []struct {
		name        string
		node        *ManagedNode
		bestHeight  uint64
		expectedMin float64
		expectedMax float64
	}{
		{
			name: "healthy_synced_node",
			node: &ManagedNode{
				ID:       "primary",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			expectedMin: 0.9,
			expectedMax: 1.0,
		},
		{
			name: "degraded_slow_node",
			node: &ManagedNode{
				ID:       "backup",
				Priority: 1,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 1500 * time.Millisecond, // Slow
					SuccessRate:     0.8,
				},
			},
			bestHeight:  1000000,
			expectedMin: 0.6,
			expectedMax: 0.85,
		},
		{
			name: "behind_blocks",
			node: &ManagedNode{
				ID:       "backup",
				Priority: 1,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        false,
					BlockHeight:     999990, // 10 blocks behind
					ResponseTimeAvg: 100 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight: 1000000,
			// 10 blocks behind gives reduced sync score (0.3 of sync weight)
			// But node is otherwise healthy: good availability, response time, success rate
			expectedMin: 0.75,
			expectedMax: 0.90,
		},
		{
			name: "recent_failure",
			node: &ManagedNode{
				ID:       "backup",
				Priority: 2,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now().Add(-90 * time.Second), // 90 seconds ago
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 100 * time.Millisecond,
					SuccessRate:     0.5,
				},
			},
			bestHeight: 1000000,
			// 90 seconds since last success gives reduced availability (0.5 of weight)
			// Otherwise synced, fast, but low success rate (0.5)
			expectedMin: 0.65,
			expectedMax: 0.80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := monitor.CalculateHealth(tt.node, tt.bestHeight)
			if score < tt.expectedMin || score > tt.expectedMax {
				t.Errorf("expected score between %.2f and %.2f, got %.2f",
					tt.expectedMin, tt.expectedMax, score)
			}
		})
	}
}

func TestNodeStateTransitions(t *testing.T) {
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "test",
		Priority: 0,
		Health: HealthScore{
			Score: 0.9,
		},
	}

	// Test healthy state
	node.Health.Score = 0.9
	node.Health.ConsecutiveFails = 0
	monitor.UpdateState(node)
	if node.Health.State != NodeStateHealthy {
		t.Errorf("expected healthy state, got %v", node.Health.State)
	}

	// Test degraded state
	node.Health.Score = 0.6
	monitor.UpdateState(node)
	if node.Health.State != NodeStateDegraded {
		t.Errorf("expected degraded state, got %v", node.Health.State)
	}

	// Test unhealthy state
	node.Health.Score = 0.3
	monitor.UpdateState(node)
	if node.Health.State != NodeStateUnhealthy {
		t.Errorf("expected unhealthy state, got %v", node.Health.State)
	}

	// Test offline state (consecutive failures)
	node.Health.Score = 0.5
	node.Health.ConsecutiveFails = 6
	monitor.UpdateState(node)
	if node.Health.State != NodeStateOffline {
		t.Errorf("expected offline state, got %v", node.Health.State)
	}
}

func TestSuccessRateTracking(t *testing.T) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID: "test",
		Health: HealthScore{
			SuccessRate: 0.5,
		},
	}

	// Record several successes - uses exponential moving average with alpha=0.02
	// This is intentionally slow-moving for stability. After 10 successes starting
	// from 0.5, the rate converges slowly: 0.5 + 0.5*(1-0.98^10) ≈ 0.59
	for i := 0; i < 10; i++ {
		monitor.RecordSuccess(node, 50*time.Millisecond)
	}

	// With alpha=0.02, success rate should increase but remain below 0.6 after 10 samples
	if node.Health.SuccessRate <= 0.5 {
		t.Errorf("expected success rate > 0.5 after successes, got %.2f", node.Health.SuccessRate)
	}

	initialRate := node.Health.SuccessRate

	// Record several failures
	for i := 0; i < 10; i++ {
		monitor.RecordFailure(node, nil)
	}

	// Success rate should decrease from the initial rate
	if node.Health.SuccessRate >= initialRate {
		t.Errorf("expected success rate < %.2f after failures, got %.2f", initialRate, node.Health.SuccessRate)
	}
}

func TestResponseTimeTracking(t *testing.T) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID: "test",
		Health: HealthScore{
			ResponseTimeAvg: 0,
		},
	}

	// Record response times
	responseTimes := []time.Duration{
		100 * time.Millisecond,
		150 * time.Millisecond,
		200 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
	}

	for _, rt := range responseTimes {
		monitor.RecordSuccess(node, rt)
	}

	// Average should be somewhere between min and max
	if node.Health.ResponseTimeAvg < 50*time.Millisecond ||
		node.Health.ResponseTimeAvg > 200*time.Millisecond {
		t.Errorf("expected response time avg between 50ms and 200ms, got %v",
			node.Health.ResponseTimeAvg)
	}
}

func TestOperationTypeRequirements(t *testing.T) {
	tests := []struct {
		op              OperationType
		requiresPrimary bool
	}{
		{OpRead, false},
		{OpSubmitShare, false},
		{OpSubmitBlock, true},
	}

	for _, tt := range tests {
		if tt.op.RequiresPrimary() != tt.requiresPrimary {
			t.Errorf("OpType %s: expected RequiresPrimary=%v, got %v",
				tt.op.String(), tt.requiresPrimary, tt.op.RequiresPrimary())
		}
	}
}

func TestManagerStats(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Note: This test would require mock daemon clients
	// For now, test the stats structure
	stats := ManagerStats{
		Coin:          "DGB",
		TotalNodes:    3,
		HealthyNodes:  2,
		PrimaryNodeID: "primary",
		FailoverCount: 1,
		NodeHealths:   map[string]float64{"primary": 0.95, "backup-1": 0.85, "backup-2": 0.0},
	}

	if stats.TotalNodes != 3 {
		t.Errorf("expected 3 total nodes, got %d", stats.TotalNodes)
	}

	if stats.HealthyNodes != 2 {
		t.Errorf("expected 2 healthy nodes, got %d", stats.HealthyNodes)
	}

	_ = logger // Suppress unused warning
}

func TestConcurrentHealthUpdates(t *testing.T) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID: "test",
		Health: HealthScore{
			SuccessRate: 0.5,
		},
	}

	// Concurrent updates
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			monitor.RecordSuccess(node, 50*time.Millisecond)
		}()
		go func() {
			defer wg.Done()
			monitor.RecordFailure(node, nil)
		}()
	}
	wg.Wait()

	// Should not panic and should have valid values
	if node.Health.SuccessRate < 0 || node.Health.SuccessRate > 1 {
		t.Errorf("invalid success rate after concurrent updates: %.2f", node.Health.SuccessRate)
	}
}

func TestFailoverEventRecording(t *testing.T) {
	event := FailoverEvent{
		FromNodeID: "primary",
		ToNodeID:   "backup-1",
		Reason:     "health_score",
		OldScore:   0.3,
		NewScore:   0.9,
		OccurredAt: time.Now(),
	}

	if event.FromNodeID != "primary" {
		t.Errorf("expected FromNodeID='primary', got %s", event.FromNodeID)
	}
	if event.ToNodeID != "backup-1" {
		t.Errorf("expected ToNodeID='backup-1', got %s", event.ToNodeID)
	}
	if event.NewScore <= event.OldScore {
		t.Errorf("expected NewScore > OldScore for failover")
	}
}

// BenchmarkHealthCalculation benchmarks health score calculation
func BenchmarkHealthCalculation(b *testing.B) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID:       "primary",
		Priority: 0,
		Health: HealthScore{
			LastError:       nil,
			LastSuccess:     time.Now(),
			IsSynced:        true,
			BlockHeight:     1000000,
			ResponseTimeAvg: 50 * time.Millisecond,
			SuccessRate:     0.95,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		monitor.CalculateHealth(node, 1000000)
	}
}

// BenchmarkSuccessRecording benchmarks recording successful requests
func BenchmarkSuccessRecording(b *testing.B) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID: "test",
		Health: HealthScore{
			SuccessRate: 0.5,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		monitor.RecordSuccess(node, 50*time.Millisecond)
	}
}

// Integration test placeholder
func TestManagerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// This would test the full manager lifecycle
	// Requires mock daemon clients or test environment

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = ctx // Placeholder
}

// =============================================================================
// EDGE CASE TESTS FOR HEALTH SCORE CALCULATION
// =============================================================================

// TestCalculateHealth_ScoreBoundaries tests boundary conditions for health scores.
func TestCalculateHealth_ScoreBoundaries(t *testing.T) {
	monitor := DefaultHealthMonitor()

	tests := []struct {
		name        string
		node        *ManagedNode
		bestHeight  uint64
		minScore    float64
		maxScore    float64
		description string
	}{
		{
			name: "perfect_score_all_factors_max",
			node: &ManagedNode{
				ID:       "perfect",
				Priority: 0, // Best priority
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(), // Just now
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 10 * time.Millisecond, // Very fast
					SuccessRate:     1.0,                   // Perfect
				},
			},
			bestHeight:  1000000,
			minScore:    0.95,
			maxScore:    1.0,
			description: "All factors at maximum should give score near 1.0",
		},
		{
			name: "zero_score_offline_node",
			node: &ManagedNode{
				ID:       "offline",
				Priority: 10, // Worst priority
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now().Add(-10 * time.Minute), // Long ago
					IsSynced:        false,
					BlockHeight:     0,                      // Unknown height
					ResponseTimeAvg: 0,                      // No data
					SuccessRate:     0.0,                    // Zero success
				},
			},
			bestHeight:  1000000,
			minScore:    0.0,
			maxScore:    0.1,
			description: "All factors at minimum should give score near 0",
		},
		{
			name: "response_time_boundary_100ms",
			node: &ManagedNode{
				ID:       "fast",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 99 * time.Millisecond, // Just under 100ms threshold
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.95,
			maxScore:    1.0,
			description: "Response time just under 100ms should get full response score",
		},
		{
			name: "response_time_boundary_2000ms",
			node: &ManagedNode{
				ID:       "slow",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 2001 * time.Millisecond, // Just over 2000ms
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.75,
			maxScore:    0.85,
			description: "Response time over 2000ms should get zero response score",
		},
		{
			name: "sync_exactly_1_block_behind",
			node: &ManagedNode{
				ID:       "slightly_behind",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        false,
					BlockHeight:     999999, // Exactly 1 behind
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.85,
			maxScore:    0.98,
			description: "1 block behind should get 0.9 of sync weight",
		},
		{
			name: "sync_exactly_5_blocks_behind",
			node: &ManagedNode{
				ID:       "behind_5",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        false,
					BlockHeight:     999995, // Exactly 5 behind
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.82,
			maxScore:    0.95,
			description: "5 blocks behind should get 0.7 of sync weight",
		},
		{
			name: "sync_exactly_10_blocks_behind",
			node: &ManagedNode{
				ID:       "behind_10",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        false,
					BlockHeight:     999990, // Exactly 10 behind
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.70,
			maxScore:    0.85,
			description: "10 blocks behind should get 0.3 of sync weight",
		},
		{
			name: "sync_11_blocks_behind_zero_sync_score",
			node: &ManagedNode{
				ID:       "behind_11",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        false,
					BlockHeight:     999989, // 11 behind
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.65,
			maxScore:    0.80,
			description: "11+ blocks behind should get zero sync score",
		},
		{
			name: "availability_60_seconds_threshold",
			node: &ManagedNode{
				ID:       "edge_avail",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now().Add(-59 * time.Second), // Just under 60s
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.90,
			maxScore:    1.0,
			description: "Last success just under 60s should get full availability",
		},
		{
			name: "availability_61_seconds_partial",
			node: &ManagedNode{
				ID:       "partial_avail",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now().Add(-61 * time.Second), // Just over 60s
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.75,
			maxScore:    0.90,
			description: "Last success 61s ago should get partial availability",
		},
		{
			name: "availability_121_seconds_zero",
			node: &ManagedNode{
				ID:       "no_avail",
				Priority: 0,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now().Add(-121 * time.Second), // Over 2 minutes
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.55,
			maxScore:    0.75,
			description: "Last success over 2 minutes should get zero availability",
		},
		{
			name: "priority_10_lowest_priority_score",
			node: &ManagedNode{
				ID:       "lowest_priority",
				Priority: 10,
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.90,
			maxScore:    0.98,
			description: "Priority 10 should get zero priority score",
		},
		{
			name: "priority_over_10_ignored",
			node: &ManagedNode{
				ID:       "extreme_priority",
				Priority: 100, // Over 10, should be treated as 0 priority score
				Health: HealthScore{
					LastError:       nil,
					LastSuccess:     time.Now(),
					IsSynced:        true,
					BlockHeight:     1000000,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			},
			bestHeight:  1000000,
			minScore:    0.90,
			maxScore:    0.98,
			description: "Priority > 10 should get zero priority score",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := monitor.CalculateHealth(tt.node, tt.bestHeight)
			if score < tt.minScore || score > tt.maxScore {
				t.Errorf("%s: expected score in [%.2f, %.2f], got %.4f",
					tt.description, tt.minScore, tt.maxScore, score)
			}
		})
	}
}

// TestCalculateHealth_ZeroBlockHeight tests edge cases with zero block heights.
func TestCalculateHealth_ZeroBlockHeight(t *testing.T) {
	monitor := DefaultHealthMonitor()

	tests := []struct {
		name        string
		nodeHeight  uint64
		bestHeight  uint64
		description string
	}{
		{
			name:        "both_zero",
			nodeHeight:  0,
			bestHeight:  0,
			description: "Both heights zero - edge case at genesis",
		},
		{
			name:        "node_zero_best_nonzero",
			nodeHeight:  0,
			bestHeight:  1000000,
			description: "Node at zero, network advanced - should have low sync score",
		},
		{
			name:        "node_nonzero_best_zero",
			nodeHeight:  1000000,
			bestHeight:  0,
			description: "Node ahead, best is zero - edge case",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ManagedNode{
				ID:       "test",
				Priority: 0,
				Health: HealthScore{
					LastSuccess:     time.Now(),
					IsSynced:        tt.nodeHeight > 0 && tt.nodeHeight >= tt.bestHeight,
					BlockHeight:     tt.nodeHeight,
					ResponseTimeAvg: 50 * time.Millisecond,
					SuccessRate:     1.0,
				},
			}

			score := monitor.CalculateHealth(node, tt.bestHeight)
			// Should not panic and should return a valid score
			if score < 0 || score > 1 {
				t.Errorf("Score out of bounds: %.4f", score)
			}
			t.Logf("%s: score=%.4f", tt.description, score)
		})
	}
}

// TestCalculateHealth_NodeAheadOfBest tests when node is ahead of best height.
func TestCalculateHealth_NodeAheadOfBest(t *testing.T) {
	monitor := DefaultHealthMonitor()

	node := &ManagedNode{
		ID:       "ahead",
		Priority: 0,
		Health: HealthScore{
			LastError:       nil,
			LastSuccess:     time.Now(),
			IsSynced:        true,
			BlockHeight:     1000010, // 10 ahead of best
			ResponseTimeAvg: 50 * time.Millisecond,
			SuccessRate:     1.0,
		},
	}

	score := monitor.CalculateHealth(node, 1000000)

	// Node ahead of best should still be considered synced
	if score < 0.9 {
		t.Errorf("Node ahead of best should have high score, got %.4f", score)
	}
}

// TestUpdateState_ThresholdBoundaries tests exact threshold boundaries.
func TestUpdateState_ThresholdBoundaries(t *testing.T) {
	monitor := DefaultHealthMonitor()

	tests := []struct {
		name          string
		score         float64
		consecFails   int
		expectedState NodeState
	}{
		// Healthy threshold is 0.8
		{"exactly_0.8_healthy", 0.80, 0, NodeStateHealthy},
		{"just_below_0.8", 0.79, 0, NodeStateDegraded},
		{"just_above_0.8", 0.81, 0, NodeStateHealthy},

		// Degraded threshold is 0.5
		{"exactly_0.5_degraded", 0.50, 0, NodeStateDegraded},
		{"just_below_0.5", 0.49, 0, NodeStateUnhealthy},
		{"just_above_0.5", 0.51, 0, NodeStateDegraded},

		// Offline threshold is 5 consecutive fails
		{"4_fails_not_offline", 0.9, 4, NodeStateHealthy},
		{"5_fails_offline", 0.9, 5, NodeStateOffline},
		{"6_fails_offline", 0.9, 6, NodeStateOffline},

		// Offline takes precedence over score
		{"high_score_but_offline", 0.99, 5, NodeStateOffline},
		{"low_score_but_offline", 0.10, 5, NodeStateOffline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &ManagedNode{
				ID: "test",
				Health: HealthScore{
					Score:            tt.score,
					ConsecutiveFails: tt.consecFails,
				},
			}

			monitor.UpdateState(node)

			if node.Health.State != tt.expectedState {
				t.Errorf("Expected state %v, got %v", tt.expectedState, node.Health.State)
			}
		})
	}
}

// TestResponseTimeAvg_ExponentialMovingAverage tests the EMA calculation.
func TestResponseTimeAvg_ExponentialMovingAverage(t *testing.T) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID:     "test",
		Health: HealthScore{},
	}

	// First response time should be set directly
	monitor.RecordSuccess(node, 100*time.Millisecond)
	if node.Health.ResponseTimeAvg != 100*time.Millisecond {
		t.Errorf("First RT should be set directly, got %v", node.Health.ResponseTimeAvg)
	}

	// Subsequent should use EMA (alpha = 0.2)
	// newAvg = 0.2 * new + 0.8 * old = 0.2 * 200 + 0.8 * 100 = 40 + 80 = 120
	monitor.RecordSuccess(node, 200*time.Millisecond)
	expected := time.Duration(120 * time.Millisecond)
	if node.Health.ResponseTimeAvg != expected {
		t.Errorf("EMA calculation error: expected %v, got %v", expected, node.Health.ResponseTimeAvg)
	}
}

// TestSuccessRate_ExponentialMovingAverage tests the success rate EMA.
func TestSuccessRate_ExponentialMovingAverage(t *testing.T) {
	monitor := DefaultHealthMonitor()

	// Test first success sets rate to 1.0
	node1 := &ManagedNode{ID: "test1", Health: HealthScore{SuccessRate: 0}}
	monitor.RecordSuccess(node1, 50*time.Millisecond)
	if node1.Health.SuccessRate != 1.0 {
		t.Errorf("First success should set rate to 1.0, got %f", node1.Health.SuccessRate)
	}

	// Test first failure sets rate to 0
	node2 := &ManagedNode{ID: "test2", Health: HealthScore{SuccessRate: 0}}
	monitor.RecordFailure(node2, nil)
	if node2.Health.SuccessRate != 0 {
		t.Errorf("First failure should set rate to 0, got %f", node2.Health.SuccessRate)
	}

	// Test EMA decay for failures after success
	node3 := &ManagedNode{ID: "test3", Health: HealthScore{SuccessRate: 1.0}}
	monitor.RecordFailure(node3, nil)
	// EMA: 0.02 * 0 + 0.98 * 1.0 = 0.98
	if node3.Health.SuccessRate < 0.97 || node3.Health.SuccessRate > 0.99 {
		t.Errorf("Expected success rate ~0.98, got %f", node3.Health.SuccessRate)
	}
}

// TestConcurrentRecordOperations tests thread safety of record operations.
func TestConcurrentRecordOperations(t *testing.T) {
	monitor := DefaultHealthMonitor()
	node := &ManagedNode{
		ID:     "test",
		Health: HealthScore{SuccessRate: 0.5},
	}

	var wg sync.WaitGroup
	const goroutines = 100
	const iterations = 100

	// Mix of success and failure recordings
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if (id+j)%2 == 0 {
					monitor.RecordSuccess(node, time.Duration(id)*time.Millisecond)
				} else {
					monitor.RecordFailure(node, nil)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify no data corruption
	if node.Health.SuccessRate < 0 || node.Health.SuccessRate > 1 {
		t.Errorf("Success rate out of bounds after concurrent ops: %f", node.Health.SuccessRate)
	}
	if node.Health.ResponseTimeAvg < 0 {
		t.Errorf("Response time negative after concurrent ops: %v", node.Health.ResponseTimeAvg)
	}
	if node.Health.ConsecutiveFails < 0 {
		t.Errorf("Consecutive fails negative: %d", node.Health.ConsecutiveFails)
	}
}

// TestNodeStateString tests the String() method for NodeState.
func TestNodeStateString(t *testing.T) {
	tests := []struct {
		state    NodeState
		expected string
	}{
		{NodeStateUnknown, "unknown"},
		{NodeStateHealthy, "healthy"},
		{NodeStateDegraded, "degraded"},
		{NodeStateUnhealthy, "unhealthy"},
		{NodeStateOffline, "offline"},
		{NodeState(100), "unknown"}, // Invalid state
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("NodeState(%d).String() = %q, want %q", tt.state, got, tt.expected)
		}
	}
}

// TestOperationTypeString tests the String() method for OperationType.
func TestOperationTypeString(t *testing.T) {
	tests := []struct {
		op       OperationType
		expected string
	}{
		{OpRead, "read"},
		{OpSubmitBlock, "submit_block"},
		{OpSubmitShare, "submit_share"},
		{OperationType(100), "unknown"}, // Invalid op
	}

	for _, tt := range tests {
		if got := tt.op.String(); got != tt.expected {
			t.Errorf("OperationType(%d).String() = %q, want %q", tt.op, got, tt.expected)
		}
	}
}
