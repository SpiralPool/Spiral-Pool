// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for Node Manager concurrent failover during SelectNode.
//
// TEST 9: Node Manager Concurrent Failover During SelectNode
// SelectNode reads primary.Health.Score WITHOUT holding node.mu at line 186,
// while performFailover modifies Health.Score under node.mu. This is a data
// race detectable with -race.
package nodemanager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestChaos_NodeManager_ConcurrentFailoverSelectNode runs SelectNode from
// many goroutines while simultaneously triggering failovers that change
// the primary node and health scores.
//
// TARGET: manager.go:180-212 (SelectNode), manager.go:352-466 (maybeFailover/performFailover)
// INVARIANT: No nil pointer dereferences, no panics, no data races (with -race).
// KNOWN ISSUE: SelectNode reads m.primary.Health.Score at line 186 without
// holding node.mu, while performFailover writes under node.mu.
// RUN WITH: go test -race -run TestChaos_NodeManager_ConcurrentFailoverSelectNode
func TestChaos_NodeManager_ConcurrentFailoverSelectNode(t *testing.T) {
	// Create managed nodes without daemon clients (not needed for this test)
	nodes := make([]*ManagedNode, 3)
	for i := 0; i < 3; i++ {
		nodes[i] = &ManagedNode{
			ID:       fmt.Sprintf("node-%d", i),
			Priority: i,
			Weight:   1,
			Health: HealthScore{
				Score:       float64(3-i) * 0.3, // node-0=0.9, node-1=0.6, node-2=0.3
				State:       NodeStateHealthy,
				SuccessRate: 0.9,
				LastSuccess: time.Now(),
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := &Manager{
		coin:    "CHAOS",
		nodes:   nodes,
		logger:  zap.NewNop().Sugar(),
		primary: nodes[0],
		monitor: DefaultHealthMonitor(),
		ctx:     ctx,
		cancel:  cancel,
	}

	const selectGoroutines = 50
	const failoverCycles = 200
	const selectOpsPerGoroutine = 2000

	var nilResults atomic.Int32
	var selectErrors atomic.Int32
	var selectSuccesses atomic.Int64

	var wg sync.WaitGroup

	// Concurrent SelectNode readers
	for g := 0; g < selectGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < selectOpsPerGoroutine; i++ {
				node, err := m.SelectNode(OpSubmitBlock)
				if err != nil {
					selectErrors.Add(1)
					continue
				}
				if node == nil {
					nilResults.Add(1)
					continue
				}
				selectSuccesses.Add(1)
			}
		}()
	}

	// Concurrent failover writer: cycles primary node and modifies health scores
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < failoverCycles; i++ {
			target := nodes[i%3]

			m.mu.Lock()

			// Degrade current primary
			if m.primary != nil {
				m.primary.mu.Lock()
				m.primary.Health.Score = 0.1
				m.primary.Health.State = NodeStateUnhealthy
				m.primary.mu.Unlock()
			}

			// Promote new primary with good health
			target.mu.Lock()
			target.Health.Score = 0.9
			target.Health.State = NodeStateHealthy
			target.mu.Unlock()

			m.primary = target
			m.mu.Unlock()

			time.Sleep(50 * time.Microsecond) // Small gap between failovers
		}
	}()

	// FIXED: SelectNode at manager.go:186 now properly acquires node.mu.RLock()
	// before reading Health.Score, matching all other read sites in the file.
	// The race between SelectNode (m.mu.RLock only) and checkAllNodes
	// (node.mu.Lock) has been resolved. This test is now safe to run with -race
	// even with concurrent Health.Score writers.

	wg.Wait()

	totalOps := selectSuccesses.Load() + int64(selectErrors.Load()) + int64(nilResults.Load())
	expectedOps := int64(selectGoroutines * selectOpsPerGoroutine)

	t.Logf("RESULTS: total=%d successes=%d errors=%d nilResults=%d",
		totalOps, selectSuccesses.Load(), selectErrors.Load(), nilResults.Load())
	t.Logf("FAILOVERS: %d cycles across 3 nodes", failoverCycles)

	if totalOps != expectedOps {
		t.Errorf("Operations lost! got %d, want %d", totalOps, expectedOps)
	}

	if nilResults.Load() > 0 {
		t.Errorf("SelectNode returned nil node %d times (nil pointer deref risk in caller)",
			nilResults.Load())
	}

	t.Logf("FIXED: SelectNode now acquires node.mu.RLock before reading Health.Score (manager.go:186)")
	t.Logf("Safe to run with -race and concurrent Health.Score writers")
}
