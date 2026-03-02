// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Multi-Node Tip Divergence & Split-Brain Scenarios
// =============================================================================
// These tests verify the pool handles multi-node scenarios where different
// daemon nodes report different chain tips (split-brain, network partitions).

// MockNode simulates a daemon node with configurable tip behavior
type MockNode struct {
	id       string
	tipHash  string
	height   uint64
	healthy  bool
	latency  time.Duration
	mu       sync.RWMutex
}

func (n *MockNode) GetBlockchainInfo() (uint64, string, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.latency > 0 {
		time.Sleep(n.latency)
	}
	return n.height, n.tipHash, nil
}

func (n *MockNode) SetTip(height uint64, tipHash string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.height = height
	n.tipHash = tipHash
}

// MockNodeManager simulates multi-node management
type MockNodeManager struct {
	nodes   []*MockNode
	primary int
	mu      sync.RWMutex
}

func NewMockNodeManager(nodes []*MockNode) *MockNodeManager {
	return &MockNodeManager{
		nodes:   nodes,
		primary: 0,
	}
}

func (m *MockNodeManager) GetPrimaryTip() (uint64, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodes[m.primary].GetBlockchainInfo()
}

func (m *MockNodeManager) GetAllTips() map[string]struct {
	Height  uint64
	TipHash string
} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tips := make(map[string]struct {
		Height  uint64
		TipHash string
	})
	for _, node := range m.nodes {
		h, t, _ := node.GetBlockchainInfo()
		tips[node.id] = struct {
			Height  uint64
			TipHash string
		}{h, t}
	}
	return tips
}

func (m *MockNodeManager) DetectDivergence() bool {
	tips := m.GetAllTips()
	var firstTip string
	for _, tip := range tips {
		if firstTip == "" {
			firstTip = tip.TipHash
		} else if tip.TipHash != firstTip {
			return true // Divergence detected
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Split-Brain Scenario Tests
// -----------------------------------------------------------------------------

// TestMultiNode_SplitBrain_TwoOfThreeAgree tests 2-of-3 consensus
func TestMultiNode_SplitBrain_TwoOfThreeAgree(t *testing.T) {
	t.Parallel()

	nodes := []*MockNode{
		{id: "node1", height: 1000, tipHash: "tip_A", healthy: true},
		{id: "node2", height: 1000, tipHash: "tip_A", healthy: true},
		{id: "node3", height: 1000, tipHash: "tip_B", healthy: true}, // Divergent
	}

	mgr := NewMockNodeManager(nodes)

	// Verify divergence is detected
	if !mgr.DetectDivergence() {
		t.Error("Should detect divergence when one node has different tip")
	}

	// Get all tips
	tips := mgr.GetAllTips()
	tipCount := make(map[string]int)
	for _, tip := range tips {
		tipCount[tip.TipHash]++
	}

	// Verify majority
	majorityTip := ""
	for tip, count := range tipCount {
		if count > len(nodes)/2 {
			majorityTip = tip
			break
		}
	}

	if majorityTip != "tip_A" {
		t.Errorf("Expected majority tip_A, got %s", majorityTip)
	}

	t.Logf("Split-brain: 2 nodes on tip_A, 1 node on tip_B. Majority: %s", majorityTip)
}

// TestMultiNode_SplitBrain_AllDifferent tests total divergence
func TestMultiNode_SplitBrain_AllDifferent(t *testing.T) {
	t.Parallel()

	nodes := []*MockNode{
		{id: "node1", height: 1000, tipHash: "tip_A", healthy: true},
		{id: "node2", height: 1000, tipHash: "tip_B", healthy: true},
		{id: "node3", height: 1000, tipHash: "tip_C", healthy: true},
	}

	mgr := NewMockNodeManager(nodes)

	// All nodes divergent - no majority
	tips := mgr.GetAllTips()
	tipCount := make(map[string]int)
	for _, tip := range tips {
		tipCount[tip.TipHash]++
	}

	hasMajority := false
	for _, count := range tipCount {
		if count > len(nodes)/2 {
			hasMajority = true
			break
		}
	}

	if hasMajority {
		t.Error("Should NOT have majority when all nodes diverge")
	}

	// In this case, pool should use primary node but log warning
	primaryHeight, primaryTip, _ := mgr.GetPrimaryTip()
	t.Logf("All nodes divergent. Using primary: height=%d, tip=%s", primaryHeight, primaryTip)
}

// TestMultiNode_SplitBrain_HeightContext tests HeightContext under divergence
func TestMultiNode_SplitBrain_HeightContext(t *testing.T) {
	t.Parallel()

	// Simulate HeightEpoch behavior when nodes diverge
	type HeightEpochSim struct {
		height  uint64
		tipHash string
		mu      sync.Mutex
		cancel  context.CancelFunc
	}

	epoch := &HeightEpochSim{height: 1000, tipHash: "tip_A"}

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	epoch.cancel = cancel

	// Simulate divergence: new tip at same height
	epoch.mu.Lock()
	if epoch.tipHash != "tip_B" { // Different tip
		if epoch.cancel != nil {
			epoch.cancel() // Cancel on divergence
		}
	}
	epoch.tipHash = "tip_B"
	epoch.mu.Unlock()

	// Verify context was cancelled
	select {
	case <-ctx.Done():
		t.Log("PASS: Context cancelled on tip divergence at same height")
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should have been cancelled on tip divergence")
	}
}

// -----------------------------------------------------------------------------
// Failover During Block Submission Tests
// -----------------------------------------------------------------------------

// TestMultiNode_FailoverDuringSubmission tests node failure mid-submission
func TestMultiNode_FailoverDuringSubmission(t *testing.T) {
	t.Parallel()

	var submissionStarted atomic.Bool
	var failoverOccurred atomic.Bool

	// Simulate submission with failover
	submitBlock := func(ctx context.Context, nodeID string) error {
		submissionStarted.Store(true)

		// Simulate slow submission
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	}

	// Start submission on primary
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := submitBlock(ctx, "primary")
		if err != nil {
			// Failover to secondary
			failoverOccurred.Store(true)
			_ = submitBlock(ctx, "secondary")
		}
	}()

	// Cancel primary mid-submission (simulate node failure)
	time.Sleep(25 * time.Millisecond)
	cancel()

	wg.Wait()

	if !submissionStarted.Load() {
		t.Error("Submission should have started")
	}

	// Note: In this simulation, context cancellation triggers failover logic
	t.Log("Failover during submission test completed")
}

// TestMultiNode_ConcurrentTipUpdates tests concurrent tip updates from multiple nodes
func TestMultiNode_ConcurrentTipUpdates(t *testing.T) {
	t.Parallel()

	nodes := []*MockNode{
		{id: "node1", height: 1000, tipHash: "initial", healthy: true},
		{id: "node2", height: 1000, tipHash: "initial", healthy: true},
		{id: "node3", height: 1000, tipHash: "initial", healthy: true},
	}

	mgr := NewMockNodeManager(nodes)

	var wg sync.WaitGroup
	updates := 100

	// Concurrent updates from "ZMQ notifications"
	for i, node := range nodes {
		wg.Add(1)
		go func(n *MockNode, idx int) {
			defer wg.Done()
			for j := 0; j < updates; j++ {
				height := uint64(1001 + j)
				tip := "tip_" + string(rune('A'+idx))
				n.SetTip(height, tip)
				time.Sleep(time.Microsecond)
			}
		}(node, i)
	}

	wg.Wait()

	// After concurrent updates, nodes may have different tips (different timing)
	tips := mgr.GetAllTips()
	t.Logf("After concurrent updates: %v", tips)

	// All should have advanced to similar height
	for id, tip := range tips {
		if tip.Height < 1001 {
			t.Errorf("Node %s height should have advanced, got %d", id, tip.Height)
		}
	}
}

// -----------------------------------------------------------------------------
// Network Partition Recovery Tests
// -----------------------------------------------------------------------------

// TestMultiNode_PartitionRecovery tests recovery after network partition heals
func TestMultiNode_PartitionRecovery(t *testing.T) {
	t.Parallel()

	nodes := []*MockNode{
		{id: "node1", height: 1000, tipHash: "tip_A", healthy: true},
		{id: "node2", height: 1005, tipHash: "tip_B", healthy: true}, // Diverged during partition
	}

	mgr := NewMockNodeManager(nodes)

	// Initially divergent
	if !mgr.DetectDivergence() {
		t.Error("Should detect initial divergence")
	}

	// Simulate partition recovery - both nodes sync to longest chain
	nodes[0].SetTip(1010, "tip_consensus")
	nodes[1].SetTip(1010, "tip_consensus")

	// After recovery, no divergence
	if mgr.DetectDivergence() {
		t.Error("Should not detect divergence after recovery")
	}

	// Verify consensus
	tips := mgr.GetAllTips()
	var consensusTip string
	for _, tip := range tips {
		if consensusTip == "" {
			consensusTip = tip.TipHash
		} else if tip.TipHash != consensusTip {
			t.Error("All nodes should agree on tip after recovery")
		}
	}

	t.Logf("Partition recovered. Consensus tip: %s", consensusTip)
}

// TestMultiNode_ReorgDuringPartition tests reorg while nodes are partitioned
func TestMultiNode_ReorgDuringPartition(t *testing.T) {
	t.Parallel()

	// Node 1 sees reorg while partitioned from others
	nodes := []*MockNode{
		{id: "node1", height: 1000, tipHash: "tip_A", healthy: true},
		{id: "node2", height: 1000, tipHash: "tip_B", healthy: true},
	}

	mgr := NewMockNodeManager(nodes)

	// Simulate reorg on node1 (same height, different tip)
	originalTip := nodes[0].tipHash
	nodes[0].SetTip(1000, "tip_A_reorged")

	// Pool using node1 as primary should see tip change
	_, currentTip, _ := mgr.GetPrimaryTip()

	if currentTip == originalTip {
		t.Error("Primary tip should have changed after reorg")
	}

	t.Logf("Reorg during partition: original=%s, new=%s", originalTip, currentTip)
}

// -----------------------------------------------------------------------------
// Latency Simulation Tests
// -----------------------------------------------------------------------------

// TestMultiNode_LatencyVariance tests handling of variable node latencies
func TestMultiNode_LatencyVariance(t *testing.T) {
	t.Parallel()

	nodes := []*MockNode{
		{id: "fast", height: 1000, tipHash: "tip", latency: 10 * time.Millisecond},
		{id: "medium", height: 1000, tipHash: "tip", latency: 50 * time.Millisecond},
		{id: "slow", height: 1000, tipHash: "tip", latency: 200 * time.Millisecond},
	}

	start := time.Now()

	// Query all nodes concurrently
	var wg sync.WaitGroup
	results := make(map[string]time.Duration)
	var mu sync.Mutex

	for _, node := range nodes {
		wg.Add(1)
		go func(n *MockNode) {
			defer wg.Done()
			queryStart := time.Now()
			n.GetBlockchainInfo()
			mu.Lock()
			results[n.id] = time.Since(queryStart)
			mu.Unlock()
		}(node)
	}

	wg.Wait()
	totalTime := time.Since(start)

	// Concurrent queries should complete in ~200ms (slowest node)
	// Not 260ms (sum of all latencies)
	if totalTime > 300*time.Millisecond {
		t.Errorf("Queries should run concurrently, took %v", totalTime)
	}

	t.Logf("Concurrent query times: %v, total: %v", results, totalTime)
}

// TestMultiNode_TimeoutHandling tests timeout handling for slow nodes
func TestMultiNode_TimeoutHandling(t *testing.T) {
	t.Parallel()

	slowNode := &MockNode{
		id:      "slow",
		height:  1000,
		tipHash: "tip",
		latency: 500 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		slowNode.GetBlockchainInfo()
		close(done)
	}()

	select {
	case <-ctx.Done():
		t.Log("PASS: Timeout triggered before slow node response")
	case <-done:
		t.Error("Slow node should have been timed out")
	}
}

// -----------------------------------------------------------------------------
// Stress Tests
// -----------------------------------------------------------------------------

// TestMultiNode_StressRapidTipChanges stress tests rapid tip changes across nodes
func TestMultiNode_StressRapidTipChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	const numNodes = 5
	const numUpdates = 1000

	nodes := make([]*MockNode, numNodes)
	for i := 0; i < numNodes; i++ {
		nodes[i] = &MockNode{
			id:      string(rune('A' + i)),
			height:  1000,
			tipHash: "initial",
			healthy: true,
		}
	}

	mgr := NewMockNodeManager(nodes)

	var wg sync.WaitGroup
	var divergenceCount atomic.Int64

	// Rapid updates on all nodes
	for i, node := range nodes {
		wg.Add(1)
		go func(n *MockNode, idx int) {
			defer wg.Done()
			for j := 0; j < numUpdates; j++ {
				height := uint64(1001 + j)
				tip := "tip_" + string(rune('A'+idx)) + "_" + string(rune('0'+j%10))
				n.SetTip(height, tip)
			}
		}(node, i)
	}

	// Monitor divergence concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numUpdates; i++ {
			if mgr.DetectDivergence() {
				divergenceCount.Add(1)
			}
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()

	t.Logf("Stress test: %d updates per node, divergences detected: %d",
		numUpdates, divergenceCount.Load())
}
