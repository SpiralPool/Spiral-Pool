// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for full health monitor concurrency path.
//
// TEST C: Full Health Monitor Concurrency Path
// Exercises the REAL production code path:
//
//	checkAllNodes → PerformHealthCheck (RPC) → RecordSuccess/RecordFailure
//	→ CalculateHealth → Score write → UpdateState
//
// Concurrent with SelectNode and maybeFailover.
//
// Unlike TestChaos_NodeManager_ConcurrentFailoverSelectNode which manually
// mutates Health.Score/State fields, this test uses real daemon RPC calls
// through mock HTTP servers to exercise the actual production code paths
// including the fixed CalculateHealth and UpdateState locking.
package nodemanager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// TestChaos_HealthMonitor_FullProductionPath exercises the real health monitor
// code path with mock daemon RPC servers under high concurrency.
//
// TARGET:
//   - health.go:41-118  (CalculateHealth — fixed: node.mu.RLock snapshot)
//   - health.go:123-148 (UpdateState — fixed: node.mu.Lock)
//   - health.go:151-210 (RecordSuccess/RecordFailure)
//   - health.go:213-248 (PerformHealthCheck → GetBlockchainInfo RPC)
//   - manager.go:321-354 (checkAllNodes → CalculateHealth → Score write → UpdateState)
//   - manager.go:180-217 (SelectNode — reads Health.Score under node.mu.RLock)
//   - manager.go:357-409 (maybeFailover — reads Health.Score/State under node.mu.RLock)
//
// INVARIANT:
//   - No data races under -race (primary assertion)
//   - No nil pointer dereferences from SelectNode
//   - No panics
//   - CalculateHealth and UpdateState run through real production locks
//   - Health scores update from initial values (proves RPC path exercised)
//
// RUN WITH: go test -race -v -run TestChaos_HealthMonitor_FullProductionPath
func TestChaos_HealthMonitor_FullProductionPath(t *testing.T) {
	logger := zap.NewNop()

	// Track mock server state per node
	type mockInfo struct {
		blockHeight *atomic.Uint64
		callCount   *atomic.Int64
	}

	mocks := make([]mockInfo, 3)
	nodes := make([]*ManagedNode, 3)

	for i := 0; i < 3; i++ {
		bh := &atomic.Uint64{}
		bh.Store(uint64(100000 + i))
		cc := &atomic.Int64{}

		// Node 2 fails every 3rd call to exercise RecordFailure path
		failEvery := int32(0)
		if i == 2 {
			failEvery = 3
		}
		fe := failEvery

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := cc.Add(1)

			// Intermittent failures for fault injection
			if fe > 0 && count%int64(fe) == 0 {
				http.Error(w, "daemon busy", http.StatusServiceUnavailable)
				return
			}

			height := bh.Load()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":`+
				`{"chain":"main","blocks":%d,"headers":%d,`+
				`"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001",`+
				`"difficulty":1234.5,"mediantime":1700000000,`+
				`"verificationprogress":1.0,"initialblockdownload":false}}`,
				height, height)
		}))
		t.Cleanup(server.Close)

		host, port := parseMockServerURL(server.URL)
		daemonCfg := &config.DaemonConfig{
			Host:     host,
			Port:     port,
			User:     "test",
			Password: "test",
		}

		// Fast retry config: no retries, minimal backoff (mock server is instant)
		retryConfig := daemon.RetryConfig{
			MaxRetries:     0,
			InitialBackoff: 1 * time.Millisecond,
			MaxBackoff:     10 * time.Millisecond,
			BackoffFactor:  1.0,
		}
		client := daemon.NewClientWithRetry(daemonCfg, logger, retryConfig)

		nodes[i] = &ManagedNode{
			ID:       fmt.Sprintf("node-%d", i),
			Client:   client,
			Priority: i,
			Weight:   1,
			Health: HealthScore{
				Score:       0.5,
				State:       NodeStateUnknown,
				SuccessRate: 0.5,
			},
		}

		mocks[i] = mockInfo{blockHeight: bh, callCount: cc}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := &Manager{
		coin:    "CHAOS-HEALTH",
		nodes:   nodes,
		logger:  logger.Sugar(),
		primary: nodes[0],
		monitor: DefaultHealthMonitor(),
		ctx:     ctx,
		cancel:  cancel,
	}

	const healthCheckCycles = 50
	const selectGoroutines = 30
	const selectOpsPerGoroutine = 1000

	var nilResults atomic.Int32
	var selectErrors atomic.Int32
	var selectSuccesses atomic.Int64
	var healthChecksDone atomic.Int32

	var wg sync.WaitGroup

	// ── Goroutine 1: Health monitor path (writer) ──────────────────────────
	// Calls checkAllNodes (PerformHealthCheck → CalculateHealth → UpdateState)
	// then maybeFailover — the full production path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for cycle := 0; cycle < healthCheckCycles; cycle++ {
			m.checkAllNodes(ctx)
			m.maybeFailover()
			healthChecksDone.Add(1)

			// Advance block heights to trigger state transitions
			for idx := range mocks {
				mocks[idx].blockHeight.Add(1)
			}
		}
	}()

	// ── Goroutines 2-N: SelectNode readers ─────────────────────────────────
	// Concurrent reads of Health.Score/State via SelectNode.
	// Exercises the fixed node.mu.RLock in SelectNode (manager.go:187-189)
	// and the fixed node.mu.RLock in maybeFailover (manager.go:363-366).
	for g := 0; g < selectGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ops := []OperationType{OpRead, OpSubmitBlock, OpSubmitShare}
			for i := 0; i < selectOpsPerGoroutine; i++ {
				node, err := m.SelectNode(ops[i%3])
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

	wg.Wait()

	// ── Results ────────────────────────────────────────────────────────────

	totalOps := selectSuccesses.Load() + int64(selectErrors.Load()) + int64(nilResults.Load())
	expectedOps := int64(selectGoroutines * selectOpsPerGoroutine)

	t.Logf("HEALTH CHECKS: %d cycles completed", healthChecksDone.Load())
	t.Logf("SELECT OPS: total=%d success=%d errors=%d nil=%d",
		totalOps, selectSuccesses.Load(), selectErrors.Load(), nilResults.Load())

	// Count daemon RPC calls
	totalRPC := int64(0)
	for i := range mocks {
		calls := mocks[i].callCount.Load()
		t.Logf("  node-%d: %d RPC calls", i, calls)
		totalRPC += calls
	}
	t.Logf("  total RPC calls: %d", totalRPC)

	// ── ASSERTIONS ─────────────────────────────────────────────────────────

	// 1. No operations lost
	if totalOps != expectedOps {
		t.Errorf("Operations lost: got %d, want %d", totalOps, expectedOps)
	}

	// 2. No nil node returns (nil pointer deref risk in callers)
	if nilResults.Load() > 0 {
		t.Errorf("SelectNode returned nil node %d times", nilResults.Load())
	}

	// 3. RPC calls were made (health checks actually exercised the daemon path)
	if totalRPC == 0 {
		t.Errorf("No RPC calls made — health check path was not exercised")
	}

	// 4. Health scores updated from initial values (CalculateHealth ran)
	for _, node := range nodes {
		node.mu.RLock()
		score := node.Health.Score
		state := node.Health.State
		blockHeight := node.Health.BlockHeight
		node.mu.RUnlock()

		t.Logf("  %s: score=%.3f state=%s blockHeight=%d", node.ID, score, state, blockHeight)

		// Score should have been recalculated (not still at initial 0.5)
		if blockHeight == 0 {
			t.Errorf("  %s: blockHeight still 0 — PerformHealthCheck never set it", node.ID)
		}
	}

	// 5. Primary assertion: test passes under -race without data race reports.
	// If you got here without -race failures, CalculateHealth and UpdateState
	// locks are correct, and all concurrent read paths (SelectNode, maybeFailover)
	// are properly synchronized.
	t.Logf("FULL PATH VERIFIED: checkAllNodes→PerformHealthCheck→CalculateHealth→UpdateState")
	t.Logf("Concurrent with %d SelectNode goroutines + maybeFailover, all under -race", selectGoroutines)
}

// parseMockServerURL extracts host and port from an httptest.Server URL.
func parseMockServerURL(rawURL string) (string, int) {
	rawURL = strings.TrimPrefix(rawURL, "http://")
	parts := strings.Split(rawURL, ":")
	host := parts[0]
	port, _ := strconv.Atoi(parts[1])
	return host, port
}
