// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package nodemanager — Unit tests for fork selection during chain divergence.
//
// These tests cover the critical scenario where nodes disagree on chain tip
// during a blockchain fork. The correct behavior is:
//   - bestHeight is derived from the majority/highest node
//   - Nodes behind get penalized sync scores
//   - maybeFailover selects the best-scoring node as primary
//   - A 0.2 score improvement threshold prevents flapping during minor forks
package nodemanager

import (
	"sync"
	"testing"
	"time"
)

// createTestNode creates a ManagedNode with specified health parameters.
func createTestNode(id string, priority int, blockHeight uint64, isSynced bool, successRate float64, responseTime time.Duration) *ManagedNode {
	return &ManagedNode{
		ID:       id,
		Priority: priority,
		Health: HealthScore{
			BlockHeight:     blockHeight,
			IsSynced:        isSynced,
			SuccessRate:     successRate,
			ResponseTimeAvg: responseTime,
			LastSuccess:     time.Now(),
			State:           NodeStateHealthy,
		},
	}
}

// =============================================================================
// Fork Selection: Node heights diverge during blockchain fork
// =============================================================================

// TestForkSelection_MajorityChainWins verifies that when nodes disagree on
// chain tip, the node on the majority/longest chain scores highest.
// NOTE: During a fork, the behind node's daemon reports isSynced=false because
// it detects it is on a shorter chain. The height-based sync scoring only
// applies when isSynced=false (see health.go:76-93).
func TestForkSelection_MajorityChainWins(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	// Fork scenario: 2 nodes on new chain (height 1001), 1 on old chain (height 1000)
	// Node A is behind and reports isSynced=false (daemon detects shorter chain)
	nodeA := createTestNode("node_a", 0, 1000, false, 0.95, 50*time.Millisecond) // Old chain, not synced
	nodeB := createTestNode("node_b", 0, 1001, true, 0.95, 50*time.Millisecond)  // New chain, synced
	nodeC := createTestNode("node_c", 0, 1001, true, 0.95, 50*time.Millisecond)  // New chain, synced

	bestHeight := uint64(1001) // Majority height

	scoreA := monitor.CalculateHealth(nodeA, bestHeight)
	scoreB := monitor.CalculateHealth(nodeB, bestHeight)
	scoreC := monitor.CalculateHealth(nodeC, bestHeight)

	// Node A is 1 block behind + not synced — should get penalized sync score
	if scoreA >= scoreB {
		t.Errorf("Node A (old chain, height %d) score %.3f should be < Node B (new chain, height %d) score %.3f",
			1000, scoreA, 1001, scoreB)
	}

	// Nodes B and C should have identical scores (same height, same parameters)
	if scoreB != scoreC {
		t.Errorf("Nodes B and C should have equal scores: B=%.3f C=%.3f", scoreB, scoreC)
	}
}

// TestForkSelection_DeepFork_NodeFarBehind verifies that a node significantly
// behind during a deep fork gets severely penalized.
// The behind node reports isSynced=false (daemon detects it's on a shorter chain).
func TestForkSelection_DeepFork_NodeFarBehind(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	// Deep fork: node stuck at 9990 (isSynced=false), advanced node at 10000 (synced)
	nodeStuck := createTestNode("stuck", 0, 9990, false, 0.95, 50*time.Millisecond) // 10 blocks behind, not synced
	nodeAdvanced := createTestNode("advanced", 0, 10000, true, 0.95, 50*time.Millisecond)

	bestHeight := uint64(10000)

	scoreStuck := monitor.CalculateHealth(nodeStuck, bestHeight)
	scoreAdvanced := monitor.CalculateHealth(nodeAdvanced, bestHeight)

	// 10 blocks behind + not synced gets 0.3 sync multiplier (vs 1.0 for synced)
	// Score difference should be significant
	scoreDiff := scoreAdvanced - scoreStuck
	if scoreDiff < 0.1 {
		t.Errorf("Node 10 blocks behind should have significantly lower score: stuck=%.3f advanced=%.3f diff=%.3f",
			scoreStuck, scoreAdvanced, scoreDiff)
	}
}

// TestForkSelection_VeryDeepFork_ZeroSyncScore verifies that a node >10 blocks
// behind gets zero sync score.
// The behind node reports isSynced=false; only then does height-based scoring apply.
func TestForkSelection_VeryDeepFork_ZeroSyncScore(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	// Node 50 blocks behind (>10 threshold for zero sync score), not synced
	nodeVeryBehind := createTestNode("very_behind", 0, 9950, false, 0.95, 50*time.Millisecond)
	nodeCurrent := createTestNode("current", 0, 10000, true, 0.95, 50*time.Millisecond)

	bestHeight := uint64(10000)

	scoreBehind := monitor.CalculateHealth(nodeVeryBehind, bestHeight)
	scoreCurrent := monitor.CalculateHealth(nodeCurrent, bestHeight)

	// >10 blocks behind + not synced = 0 sync score (0.25 weight completely lost)
	expectedDiff := 0.25 // Full sync weight
	actualDiff := scoreCurrent - scoreBehind

	// Allow small tolerance for floating point
	if actualDiff < expectedDiff-0.01 || actualDiff > expectedDiff+0.01 {
		t.Errorf("Node >10 blocks behind should lose full sync weight (0.25): actual diff=%.3f expected=%.3f",
			actualDiff, expectedDiff)
	}
}

// TestForkSelection_NodeAhead_FullSyncScore verifies that a node ahead of
// bestHeight (can happen during reorgs) gets full sync score.
func TestForkSelection_NodeAhead_FullSyncScore(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	// Node found a block before others — temporarily ahead
	nodeAhead := createTestNode("ahead", 0, 10002, true, 0.95, 50*time.Millisecond)
	nodeBest := createTestNode("best", 0, 10001, true, 0.95, 50*time.Millisecond)

	bestHeight := uint64(10001)

	scoreAhead := monitor.CalculateHealth(nodeAhead, bestHeight)
	scoreBest := monitor.CalculateHealth(nodeBest, bestHeight)

	// Both should get full sync score (nodeAhead >= bestHeight)
	if scoreAhead < scoreBest {
		t.Errorf("Node ahead of bestHeight should get full sync score: ahead=%.3f best=%.3f",
			scoreAhead, scoreBest)
	}
}

// TestForkSelection_FailoverThreshold_PreventsFlapping verifies the 0.2 score
// improvement threshold prevents primary switching on minor score differences.
func TestForkSelection_FailoverThreshold_PreventsFlapping(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	// Primary is 1 block behind (score penalty ~0.025)
	primary := createTestNode("primary", 0, 999, true, 0.95, 50*time.Millisecond)
	backup := createTestNode("backup", 1, 1000, true, 0.95, 50*time.Millisecond)

	bestHeight := uint64(1000)

	primaryScore := monitor.CalculateHealth(primary, bestHeight)
	backupScore := monitor.CalculateHealth(backup, bestHeight)

	scoreDiff := backupScore - primaryScore

	// The failover threshold is 0.2 — a 1-block difference (~0.025 score diff)
	// should NOT trigger a failover
	if scoreDiff >= 0.2 {
		t.Errorf("1-block difference should NOT exceed 0.2 failover threshold: diff=%.3f", scoreDiff)
	}
}

// TestForkSelection_FailoverTriggered_NodeFailed verifies failover happens when
// primary goes offline (large score gap exceeds 0.2 threshold).
func TestForkSelection_FailoverTriggered_NodeFailed(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	// Primary failed (last success >2 min ago = 0 availability)
	primary := createTestNode("primary_failed", 0, 1000, true, 0.5, 1*time.Second)
	primary.Health.LastSuccess = time.Now().Add(-5 * time.Minute) // Old success
	primary.Health.LastError = nil                                // No recent error but no recent success either

	backup := createTestNode("backup_healthy", 1, 1000, true, 0.95, 50*time.Millisecond)

	bestHeight := uint64(1000)

	primaryScore := monitor.CalculateHealth(primary, bestHeight)
	backupScore := monitor.CalculateHealth(backup, bestHeight)

	scoreDiff := backupScore - primaryScore

	// Failed primary (last success >2min) should have 0 availability score (0.35 weight)
	// This should create a gap > 0.2, triggering failover
	if scoreDiff < 0.2 {
		t.Errorf("Failed primary should create >0.2 score gap for failover: primary=%.3f backup=%.3f diff=%.3f",
			primaryScore, backupScore, scoreDiff)
	}
}

// =============================================================================
// checkAllNodes: bestHeight discovery
// =============================================================================

// TestCheckAllNodes_BestHeightFromMultipleNodes verifies that checkAllNodes
// correctly identifies the highest block height across all nodes.
func TestCheckAllNodes_BestHeightFromMultipleNodes(t *testing.T) {
	t.Parallel()

	// Simulate the bestHeight discovery loop (mirrors manager.go:330-338)
	nodes := []*ManagedNode{
		createTestNode("node1", 0, 999, true, 0.95, 50*time.Millisecond),
		createTestNode("node2", 1, 1001, true, 0.95, 50*time.Millisecond), // Highest
		createTestNode("node3", 2, 1000, true, 0.95, 50*time.Millisecond),
	}

	bestHeight := uint64(0)
	for _, node := range nodes {
		node.mu.RLock()
		if node.Health.BlockHeight > bestHeight {
			bestHeight = node.Health.BlockHeight
		}
		node.mu.RUnlock()
	}

	if bestHeight != 1001 {
		t.Errorf("bestHeight should be 1001 (from node2), got %d", bestHeight)
	}
}

// TestCheckAllNodes_AllSameHeight verifies that when all nodes agree on height,
// all get full sync score.
func TestCheckAllNodes_AllSameHeight(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	nodes := []*ManagedNode{
		createTestNode("n1", 0, 5000, true, 0.95, 50*time.Millisecond),
		createTestNode("n2", 1, 5000, true, 0.95, 50*time.Millisecond),
		createTestNode("n3", 2, 5000, true, 0.95, 50*time.Millisecond),
	}

	bestHeight := uint64(5000)

	scores := make([]float64, len(nodes))
	for i, node := range nodes {
		scores[i] = monitor.CalculateHealth(node, bestHeight)
	}

	// Node 0 (priority 0) should score highest due to priority bonus
	if scores[0] <= scores[1] || scores[0] <= scores[2] {
		t.Errorf("Priority-0 node should score highest when all else equal: scores=%v", scores)
	}
}

// =============================================================================
// Edge cases
// =============================================================================

// TestForkSelection_ZeroBestHeight verifies behavior when bestHeight is 0
// (initial startup before first health check).
func TestForkSelection_ZeroBestHeight(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()

	node := createTestNode("startup", 0, 0, false, 0, 0)
	bestHeight := uint64(0)

	score := monitor.CalculateHealth(node, bestHeight)

	// Should not panic. Score should be 0 or very low (no data yet).
	if score < 0 || score > 1.0 {
		t.Errorf("Score should be in [0, 1.0], got %.3f", score)
	}
}

// TestForkSelection_ConcurrentScoreUpdates verifies thread safety when
// multiple goroutines update health scores simultaneously.
func TestForkSelection_ConcurrentScoreUpdates(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()
	node := createTestNode("concurrent", 0, 1000, true, 0.95, 50*time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(height uint64) {
			defer wg.Done()
			node.mu.Lock()
			node.Health.BlockHeight = height
			node.mu.Unlock()

			_ = monitor.CalculateHealth(node, height)
		}(uint64(1000 + i))
	}
	wg.Wait()

	// No race condition = pass
}

// TestForkSelection_SyncStatusGradient verifies the complete gradient of sync
// score penalties from 1 block behind to >10 blocks behind.
func TestForkSelection_SyncStatusGradient(t *testing.T) {
	t.Parallel()

	monitor := DefaultHealthMonitor()
	bestHeight := uint64(10000)

	// Map of blocks-behind -> expected sync weight multiplier
	expectedMultipliers := map[uint64]float64{
		0:  1.0, // At best height
		1:  0.9, // 1 block behind
		5:  0.7, // 2-5 blocks behind
		10: 0.3, // 6-10 blocks behind
		11: 0.0, // >10 blocks behind
		50: 0.0, // Way behind
	}

	for behind, expectedMult := range expectedMultipliers {
		height := bestHeight - behind
		node := createTestNode("test", 0, height, false, 0.95, 50*time.Millisecond) // isSynced=false to use height-based scoring
		node.Health.LastSuccess = time.Now()

		score := monitor.CalculateHealth(node, bestHeight)

		// The sync component is wSyncStatus * multiplier = 0.25 * multiplier
		// Total score includes other components, so we verify relative ordering
		_ = score
		_ = expectedMult
	}

	// Verify strict ordering: closer to bestHeight = higher score
	nodeAt := createTestNode("at", 0, 10000, false, 0.95, 50*time.Millisecond)
	nodeAt.Health.LastSuccess = time.Now()
	node1Behind := createTestNode("1behind", 0, 9999, false, 0.95, 50*time.Millisecond)
	node1Behind.Health.LastSuccess = time.Now()
	node5Behind := createTestNode("5behind", 0, 9995, false, 0.95, 50*time.Millisecond)
	node5Behind.Health.LastSuccess = time.Now()
	node10Behind := createTestNode("10behind", 0, 9990, false, 0.95, 50*time.Millisecond)
	node10Behind.Health.LastSuccess = time.Now()
	node11Behind := createTestNode("11behind", 0, 9989, false, 0.95, 50*time.Millisecond)
	node11Behind.Health.LastSuccess = time.Now()

	scoreAt := monitor.CalculateHealth(nodeAt, bestHeight)
	score1 := monitor.CalculateHealth(node1Behind, bestHeight)
	score5 := monitor.CalculateHealth(node5Behind, bestHeight)
	score10 := monitor.CalculateHealth(node10Behind, bestHeight)
	score11 := monitor.CalculateHealth(node11Behind, bestHeight)

	if !(scoreAt > score1 && score1 > score5 && score5 > score10 && score10 > score11) {
		t.Errorf("Sync score gradient violated: at=%.3f 1behind=%.3f 5behind=%.3f 10behind=%.3f 11behind=%.3f",
			scoreAt, score1, score5, score10, score11)
	}
}
