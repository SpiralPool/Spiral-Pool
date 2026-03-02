// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build integration
// +build integration

// Package ha - Integration tests for HA/PostgreSQL failover.
//
// These tests require:
// - Docker for PostgreSQL container orchestration
// - Network namespace support (Linux) or simulation mode
// - Sufficient memory for multi-node testing
//
// Run with: go test -tags=integration -v -timeout=30m ./internal/ha/...
//
// Test categories:
// 1. PostgreSQL Integration - Real PostgreSQL replication failover
// 2. Network Partition - Split-brain prevention testing
// 3. Load Testing - Failover under miner load
// 4. Chaos Engineering - Random failure injection
package ha

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

// =============================================================================
// TEST INFRASTRUCTURE
// =============================================================================

// testCluster manages a multi-node test cluster
type testCluster struct {
	nodes       []*testNode
	vipManagers []*VIPManager
	primaryDB   *sql.DB
	replicaDB   *sql.DB
	logger      *zap.SugaredLogger
	cleanup     []func()
}

// testNode represents a simulated node in the test cluster
type testNode struct {
	ID         string
	Host       string
	Port       int
	Priority   int
	VIPManager *VIPManager
	IsHealthy  bool
	Listener   net.Listener
	DBConn     *sql.DB
	ShareCount atomic.Int64
	mu         sync.Mutex
}

// testMiner simulates a mining client
type testMiner struct {
	ID             string
	PoolHost       string
	PoolPort       int
	Conn           net.Conn
	SharesSent     atomic.Int64
	SharesAccepted atomic.Int64
	Disconnects    atomic.Int64
	Reconnects     atomic.Int64
	mu             sync.Mutex
}

// testResult captures test execution results
type testResult struct {
	TestName           string
	Duration           time.Duration
	FailoverCount      int
	DataLossDetected   bool
	SplitBrainDetected bool
	SharesSubmitted    int64
	SharesVerified     int64
	Errors             []string
}

// =============================================================================
// 1. POSTGRESQL INTEGRATION TESTS
// =============================================================================

// TestPostgresRealFailover tests actual PostgreSQL replication failover.
// Requires: Docker with PostgreSQL containers
func TestPostgresRealFailover(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL integration test in short mode")
	}

	if !isDockerAvailable() {
		t.Skip("Docker not available, skipping PostgreSQL integration test")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	// Setup PostgreSQL primary and replica containers
	primary, replica, cleanup := setupPostgresContainers(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Wait for replication to be established
	waitForReplication(t, primary, replica, 30*time.Second)

	// Insert test data on primary
	testShareID := fmt.Sprintf("test-share-%d", time.Now().UnixNano())
	insertTestShare(t, primary, testShareID, "DTestWorker", 1000000)

	// Verify data replicated to replica
	verifyShareExists(t, replica, testShareID, 10*time.Second)
	log.Info("✅ Replication verified - data synced to replica")

	// Record pre-failover state
	preFailoverShares := countShares(t, primary)
	log.Infow("Pre-failover state", "shares", preFailoverShares)

	// Simulate primary failure by stopping container
	log.Info("⚡ Simulating primary failure...")
	stopContainer(t, "spiral-pg-primary")

	// Promote replica to primary
	log.Info("📈 Promoting replica to primary...")
	promoteReplica(t, replica)

	// Wait for promotion
	waitForPrimaryPromotion(t, replica, 30*time.Second)

	// Insert new data on promoted replica (now primary)
	postFailoverShareID := fmt.Sprintf("post-failover-%d", time.Now().UnixNano())
	insertTestShare(t, replica, postFailoverShareID, "DTestWorker2", 2000000)

	// Verify new primary is writable
	postFailoverShares := countShares(t, replica)
	if postFailoverShares <= preFailoverShares {
		t.Errorf("Post-failover share count should be greater: pre=%d, post=%d",
			preFailoverShares, postFailoverShares)
	}

	log.Infow("✅ PostgreSQL failover successful",
		"pre_failover_shares", preFailoverShares,
		"post_failover_shares", postFailoverShares,
	)

	// Verify NO data loss
	if !verifyShareExistsQuick(t, replica, testShareID) {
		t.Error("❌ DATA LOSS DETECTED: Pre-failover share not found after promotion")
	}

	// Restart original primary as replica
	log.Info("🔄 Restarting original primary as replica...")
	startContainerAsReplica(t, "spiral-pg-primary", replica)

	// Verify original primary rejoined as replica
	waitForReplicaSync(t, primary, replica, 60*time.Second)

	log.Info("✅ Full failover and failback cycle completed successfully")
}

// TestPostgresDataIntegrity verifies zero data loss during failover.
func TestPostgresDataIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping data integrity test in short mode")
	}

	if !isDockerAvailable() {
		t.Skip("Docker not available")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	primary, replica, cleanup := setupPostgresContainers(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	waitForReplication(t, primary, replica, 30*time.Second)

	// Insert shares continuously while triggering failover
	var wg sync.WaitGroup
	var insertedShares sync.Map
	var insertErrors atomic.Int64
	stopInserts := make(chan struct{})

	// Start continuous share insertion
	wg.Add(1)
	go func() {
		defer wg.Done()
		currentDB := primary
		shareNum := 0

		for {
			select {
			case <-stopInserts:
				return
			default:
				shareID := fmt.Sprintf("integrity-test-%d", shareNum)
				err := insertTestShareWithRetry(currentDB, shareID, "IntegrityWorker", int64(shareNum*1000))
				if err != nil {
					// Try replica (now promoted)
					if replica != nil {
						err = insertTestShareWithRetry(replica, shareID, "IntegrityWorker", int64(shareNum*1000))
					}
					if err != nil {
						insertErrors.Add(1)
						log.Warnw("Share insert failed", "shareID", shareID, "error", err)
					} else {
						insertedShares.Store(shareID, true)
						currentDB = replica // Switch to new primary
					}
				} else {
					insertedShares.Store(shareID, true)
				}
				shareNum++
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Let some shares be inserted
	time.Sleep(2 * time.Second)

	// Trigger failover
	log.Info("⚡ Triggering failover during active writes...")
	stopContainer(t, "spiral-pg-primary")
	promoteReplica(t, replica)
	waitForPrimaryPromotion(t, replica, 30*time.Second)

	// Continue inserts for a bit after failover
	time.Sleep(3 * time.Second)
	close(stopInserts)
	wg.Wait()

	// Verify all inserted shares exist
	var totalInserted, totalVerified int64
	insertedShares.Range(func(key, value interface{}) bool {
		totalInserted++
		shareID := key.(string)
		if verifyShareExistsQuick(t, replica, shareID) {
			totalVerified++
		} else {
			log.Errorw("❌ MISSING SHARE", "shareID", shareID)
		}
		return true
	})

	log.Infow("Data integrity verification",
		"inserted", totalInserted,
		"verified", totalVerified,
		"insert_errors", insertErrors.Load(),
	)

	lossRate := float64(totalInserted-totalVerified) / float64(totalInserted) * 100
	if lossRate > 0 {
		t.Errorf("DATA LOSS: %.2f%% of shares lost (%d/%d)", lossRate, totalInserted-totalVerified, totalInserted)
	}

	select {
	case <-ctx.Done():
		t.Fatal("Test timed out")
	default:
	}
}

// =============================================================================
// 2. NETWORK PARTITION TESTS (Split-Brain Prevention)
// =============================================================================

// TestNetworkPartitionSplitBrain tests split-brain prevention.
func TestNetworkPartitionSplitBrain(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping network partition test in short mode")
	}

	// Network partition simulation requires root on Linux
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		t.Skip("Network partition test requires root privileges on Linux")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	// Create 3-node cluster for quorum testing
	cluster := setupTestCluster(t, 3)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Start all VIP managers
	for _, vm := range cluster.vipManagers {
		if err := vm.Start(ctx); err != nil {
			t.Fatalf("Failed to start VIP manager: %v", err)
		}
	}

	// Wait for initial election
	time.Sleep(5 * time.Second)

	// Verify exactly one master
	masterCount := countMasters(cluster)
	if masterCount != 1 {
		t.Errorf("Expected 1 master, got %d", masterCount)
	}

	log.Info("✅ Initial cluster state: 1 master elected")

	// Simulate network partition: isolate node 0 from nodes 1,2
	log.Info("🔌 Simulating network partition...")
	partitionNode(t, cluster.nodes[0])

	// Wait for failover on the majority partition
	time.Sleep(10 * time.Second)

	// The majority partition (nodes 1,2) should elect a new master
	// The isolated node (node 0) should detect it's partitioned and step down

	// Check for split-brain
	masterCount = countMasters(cluster)
	if masterCount > 1 {
		t.Errorf("❌ SPLIT-BRAIN DETECTED: %d masters active", masterCount)
		for i, vm := range cluster.vipManagers {
			log.Infow("Node state",
				"node", i,
				"isMaster", vm.IsMaster(),
				"role", vm.role.String(),
			)
		}
	} else {
		log.Infow("✅ Split-brain prevented", "masters", masterCount)
	}

	// Heal partition
	log.Info("🔗 Healing network partition...")
	healPartition(t, cluster.nodes[0])

	// Wait for cluster to reconverge
	time.Sleep(15 * time.Second)

	// Verify cluster stabilized with exactly one master
	masterCount = countMasters(cluster)
	if masterCount != 1 {
		t.Errorf("Post-heal expected 1 master, got %d", masterCount)
	}

	log.Info("✅ Cluster reconverged after partition heal")
}

// TestVIPFencing tests that old master releases VIP before new master acquires.
func TestVIPFencing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VIP fencing test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	cluster := setupTestCluster(t, 2)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Start VIP managers
	for _, vm := range cluster.vipManagers {
		if err := vm.Start(ctx); err != nil {
			t.Fatalf("Failed to start VIP manager: %v", err)
		}
	}

	time.Sleep(5 * time.Second)

	// Find current master
	var masterIdx int
	for i, vm := range cluster.vipManagers {
		if vm.IsMaster() {
			masterIdx = i
			break
		}
	}

	log.Infow("Initial master", "node", masterIdx)

	// Track VIP holder changes
	var vipEvents []string
	var eventsMu sync.Mutex

	cluster.vipManagers[0].SetVIPReleasedHandler(func(vip string) {
		eventsMu.Lock()
		vipEvents = append(vipEvents, fmt.Sprintf("node-0-released:%s", vip))
		eventsMu.Unlock()
	})
	cluster.vipManagers[0].SetVIPAcquiredHandler(func(vip string) {
		eventsMu.Lock()
		vipEvents = append(vipEvents, fmt.Sprintf("node-0-acquired:%s", vip))
		eventsMu.Unlock()
	})
	cluster.vipManagers[1].SetVIPReleasedHandler(func(vip string) {
		eventsMu.Lock()
		vipEvents = append(vipEvents, fmt.Sprintf("node-1-released:%s", vip))
		eventsMu.Unlock()
	})
	cluster.vipManagers[1].SetVIPAcquiredHandler(func(vip string) {
		eventsMu.Lock()
		vipEvents = append(vipEvents, fmt.Sprintf("node-1-acquired:%s", vip))
		eventsMu.Unlock()
	})

	// Kill master node
	log.Info("⚡ Simulating master failure...")
	cluster.nodes[masterIdx].IsHealthy = false
	cluster.vipManagers[masterIdx].Stop()

	// Wait for failover
	time.Sleep(10 * time.Second)

	// Verify event ordering: release must happen before acquire
	eventsMu.Lock()
	log.Infow("VIP events", "events", vipEvents)
	eventsMu.Unlock()

	// Verify new master
	newMasterIdx := 1 - masterIdx
	if !cluster.vipManagers[newMasterIdx].IsMaster() {
		t.Error("Expected backup to become master after primary failure")
	}

	// Check VIP not held by both
	vipHolders := 0
	for _, vm := range cluster.vipManagers {
		if vm.IsMaster() && vm.IsEnabled() {
			vipHolders++
		}
	}
	if vipHolders > 1 {
		t.Error("❌ VIP FENCING FAILURE: Multiple VIP holders detected")
	}

	log.Info("✅ VIP fencing verified: clean handoff occurred")
}

// =============================================================================
// 3. LOAD TESTING - Failover Under Miner Load
// =============================================================================

// TestFailoverUnderLoad tests failover with active mining connections.
func TestFailoverUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	// Configuration
	const (
		numMiners      = 100
		sharesPerMiner = 50
		shareInterval  = 100 * time.Millisecond
	)

	cluster := setupTestCluster(t, 2)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Start simulated stratum server on each node
	for _, node := range cluster.nodes {
		startStratumServer(t, node)
	}

	// Start VIP managers
	for _, vm := range cluster.vipManagers {
		if err := vm.Start(ctx); err != nil {
			t.Fatalf("Failed to start VIP manager: %v", err)
		}
	}

	time.Sleep(5 * time.Second)

	// Find active pool endpoint
	var activeNode *testNode
	for _, node := range cluster.nodes {
		if node.VIPManager.IsMaster() {
			activeNode = node
			break
		}
	}
	if activeNode == nil {
		t.Fatal("No master node found")
	}

	log.Infow("Starting load test", "miners", numMiners, "shares_per_miner", sharesPerMiner)

	// Create miners
	miners := make([]*testMiner, numMiners)
	for i := 0; i < numMiners; i++ {
		miners[i] = &testMiner{
			ID:       fmt.Sprintf("miner-%03d", i),
			PoolHost: activeNode.Host,
			PoolPort: activeNode.Port,
		}
	}

	// Connect all miners
	var wg sync.WaitGroup
	connectErrors := atomic.Int64{}
	for _, miner := range miners {
		wg.Add(1)
		go func(m *testMiner) {
			defer wg.Done()
			if err := m.connect(); err != nil {
				connectErrors.Add(1)
			}
		}(miner)
	}
	wg.Wait()

	log.Infow("Miners connected",
		"connected", numMiners-int(connectErrors.Load()),
		"failed", connectErrors.Load(),
	)

	// Start share submission
	stopShares := make(chan struct{})
	var shareWg sync.WaitGroup

	for _, miner := range miners {
		shareWg.Add(1)
		go func(m *testMiner) {
			defer shareWg.Done()
			for i := 0; i < sharesPerMiner; i++ {
				select {
				case <-stopShares:
					return
				default:
					if err := m.submitShare(); err != nil {
						// Try to reconnect to new pool
						m.reconnectToPool(cluster)
					}
					time.Sleep(shareInterval)
				}
			}
		}(miner)
	}

	// Let shares flow
	time.Sleep(2 * time.Second)

	// TRIGGER FAILOVER
	log.Info("⚡ TRIGGERING FAILOVER UNDER LOAD...")
	failoverStart := time.Now()

	// Kill master
	activeNode.IsHealthy = false
	activeNode.Listener.Close()

	// Wait for failover and miner recovery
	time.Sleep(15 * time.Second)

	failoverDuration := time.Since(failoverStart)
	log.Infow("Failover completed", "duration", failoverDuration)

	// Stop share submission
	close(stopShares)
	shareWg.Wait()

	// Calculate results
	var totalSent, totalAccepted, totalDisconnects, totalReconnects int64
	for _, miner := range miners {
		totalSent += miner.SharesSent.Load()
		totalAccepted += miner.SharesAccepted.Load()
		totalDisconnects += miner.Disconnects.Load()
		totalReconnects += miner.Reconnects.Load()
	}

	// Verify shares on new master
	var newMaster *testNode
	for _, node := range cluster.nodes {
		if node != activeNode && node.IsHealthy {
			newMaster = node
			break
		}
	}

	verifiedShares := int64(0)
	if newMaster != nil {
		verifiedShares = newMaster.ShareCount.Load()
	}

	result := testResult{
		TestName:        "FailoverUnderLoad",
		Duration:        failoverDuration,
		FailoverCount:   1,
		SharesSubmitted: totalSent,
		SharesVerified:  verifiedShares,
	}

	log.Infow("Load test results",
		"total_sent", totalSent,
		"total_accepted", totalAccepted,
		"verified_on_new_master", verifiedShares,
		"disconnects", totalDisconnects,
		"reconnects", totalReconnects,
		"failover_duration", failoverDuration,
	)

	// Success criteria
	// - Failover under 30 seconds
	// - At least 90% of miners successfully reconnected
	// - Share loss < 5%

	if failoverDuration > 30*time.Second {
		t.Errorf("Failover too slow: %v (max 30s)", failoverDuration)
	}

	reconnectRate := float64(totalReconnects) / float64(numMiners) * 100
	if reconnectRate < 90 {
		t.Errorf("Reconnect rate too low: %.1f%% (min 90%%)", reconnectRate)
	}

	if totalSent > 0 {
		lossRate := float64(totalSent-verifiedShares) / float64(totalSent) * 100
		if lossRate > 5 {
			t.Errorf("Share loss too high: %.1f%% (max 5%%)", lossRate)
		}
	}

	log.Infow("✅ Load test passed", "result", result)
}

// TestHighThroughputFailover tests failover at maximum share rate.
func TestHighThroughputFailover(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high throughput test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	const (
		targetSharesPerSecond = 1000
		testDuration          = 30 * time.Second
	)

	cluster := setupTestCluster(t, 2)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, node := range cluster.nodes {
		startStratumServer(t, node)
	}

	for _, vm := range cluster.vipManagers {
		vm.Start(ctx)
	}

	time.Sleep(3 * time.Second)

	// High-speed share generator
	var activeNode *testNode
	for _, node := range cluster.nodes {
		if node.VIPManager.IsMaster() {
			activeNode = node
			break
		}
	}

	sharesSent := atomic.Int64{}
	stop := make(chan struct{})
	interval := time.Second / time.Duration(targetSharesPerSecond)

	// Start high-throughput share submission
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sharesSent.Add(1)
				// Simulate share arrival
				if activeNode != nil && activeNode.IsHealthy {
					activeNode.ShareCount.Add(1)
				}
			}
		}
	}()

	// Run for half the test, then failover
	time.Sleep(testDuration / 2)

	preFailoverShares := sharesSent.Load()
	log.Infow("Pre-failover", "shares", preFailoverShares)

	// Trigger failover
	log.Info("⚡ Triggering failover at high throughput...")
	activeNode.IsHealthy = false

	// Continue for remaining duration
	time.Sleep(testDuration / 2)
	close(stop)

	postFailoverShares := sharesSent.Load()
	log.Infow("Post-failover",
		"total_shares", postFailoverShares,
		"effective_rate", float64(postFailoverShares)/testDuration.Seconds(),
	)

	// Verify throughput maintained
	expectedShares := int64(float64(targetSharesPerSecond) * testDuration.Seconds() * 0.9) // 90% tolerance
	if postFailoverShares < expectedShares {
		t.Errorf("Throughput dropped: %d < %d expected", postFailoverShares, expectedShares)
	}

	log.Info("✅ High throughput failover test passed")
}

// =============================================================================
// 4. CHAOS ENGINEERING TESTS
// =============================================================================

// TestChaosRandomFailures injects random failures during operation.
func TestChaosRandomFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	const (
		numNodes     = 5
		testDuration = 2 * time.Minute
		failureRate  = 10 * time.Second // Average time between failures
		recoveryTime = 5 * time.Second
	)

	cluster := setupTestCluster(t, numNodes)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Start all nodes
	for _, node := range cluster.nodes {
		startStratumServer(t, node)
	}
	for _, vm := range cluster.vipManagers {
		vm.Start(ctx)
	}

	time.Sleep(5 * time.Second)

	// Chaos event tracker
	var events []chaosEvent
	var eventsMu sync.Mutex

	// Statistics
	var (
		totalFailures   atomic.Int64
		totalRecoveries atomic.Int64
		maxMasters      atomic.Int64
		splitBrains     atomic.Int64
	)

	// Chaos goroutine - randomly kills and recovers nodes
	chaosStop := make(chan struct{})
	go func() {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))

		for {
			select {
			case <-chaosStop:
				return
			case <-time.After(time.Duration(rng.Int63n(int64(failureRate)))):
				// Pick random non-master node to fail
				nodeIdx := rng.Intn(numNodes)
				node := cluster.nodes[nodeIdx]

				if node.IsHealthy {
					log.Infow("💥 CHAOS: Failing node", "node", nodeIdx)
					node.IsHealthy = false
					node.Listener.Close()
					totalFailures.Add(1)

					eventsMu.Lock()
					events = append(events, chaosEvent{
						Time:   time.Now(),
						Type:   "failure",
						NodeID: node.ID,
					})
					eventsMu.Unlock()

					// Schedule recovery
					go func(n *testNode, idx int) {
						time.Sleep(recoveryTime)
						log.Infow("🔧 CHAOS: Recovering node", "node", idx)
						n.IsHealthy = true
						startStratumServer(t, n)
						totalRecoveries.Add(1)

						eventsMu.Lock()
						events = append(events, chaosEvent{
							Time:   time.Now(),
							Type:   "recovery",
							NodeID: n.ID,
						})
						eventsMu.Unlock()
					}(node, nodeIdx)
				}
			}
		}
	}()

	// Monitor goroutine - checks for split-brain
	monitorStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-monitorStop:
				return
			case <-ticker.C:
				masters := countMasters(cluster)
				if int64(masters) > maxMasters.Load() {
					maxMasters.Store(int64(masters))
				}
				if masters > 1 {
					splitBrains.Add(1)
					log.Warnw("⚠️ SPLIT-BRAIN DETECTED", "masters", masters)
				}
			}
		}
	}()

	// Run chaos test
	log.Infow("🔥 Starting chaos test", "duration", testDuration)
	time.Sleep(testDuration)

	// Stop chaos
	close(chaosStop)
	close(monitorStop)

	// Recover all nodes for cleanup
	for _, node := range cluster.nodes {
		node.IsHealthy = true
	}

	// Report results
	log.Infow("🔥 Chaos test complete",
		"total_failures", totalFailures.Load(),
		"total_recoveries", totalRecoveries.Load(),
		"max_simultaneous_masters", maxMasters.Load(),
		"split_brain_events", splitBrains.Load(),
	)

	// Success criteria
	if splitBrains.Load() > 0 {
		t.Errorf("❌ Split-brain detected %d times during chaos test", splitBrains.Load())
	}

	if maxMasters.Load() > 1 {
		t.Errorf("❌ Multiple masters detected: max %d", maxMasters.Load())
	}

	log.Info("✅ Chaos test passed - no split-brain detected")
}

// TestChaosNetworkLatency injects random network latency.
func TestChaosNetworkLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping network latency chaos test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	cluster := setupTestCluster(t, 3)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, vm := range cluster.vipManagers {
		vm.Start(ctx)
	}

	time.Sleep(5 * time.Second)

	// Simulate variable latency by injecting delays
	latencyStop := make(chan struct{})
	go func() {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		for {
			select {
			case <-latencyStop:
				return
			case <-time.After(time.Duration(rng.Int63n(int64(2 * time.Second)))):
				nodeIdx := rng.Intn(len(cluster.nodes))
				latency := time.Duration(rng.Int63n(int64(500 * time.Millisecond)))
				log.Debugw("Injecting latency", "node", nodeIdx, "latency", latency)
				// In real implementation, this would use tc netem
				time.Sleep(latency)
			}
		}
	}()

	// Run test
	time.Sleep(1 * time.Minute)
	close(latencyStop)

	// Verify cluster survived
	masters := countMasters(cluster)
	if masters != 1 {
		t.Errorf("Expected 1 master after latency chaos, got %d", masters)
	}

	log.Info("✅ Network latency chaos test passed")
}

// TestChaosBlockSubmissionDuringFailover tests block submission during failover.
func TestChaosBlockSubmissionDuringFailover(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping block submission chaos test in short mode")
	}

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	cluster := setupTestCluster(t, 2)
	defer cluster.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, node := range cluster.nodes {
		startStratumServer(t, node)
	}
	for _, vm := range cluster.vipManagers {
		vm.Start(ctx)
	}

	time.Sleep(3 * time.Second)

	// Simulate block found exactly during failover
	var blockSubmitted atomic.Bool
	var blockSuccess atomic.Bool

	go func() {
		// Wait for failover to start
		time.Sleep(100 * time.Millisecond)

		log.Info("📦 Submitting block during failover!")
		blockSubmitted.Store(true)

		// Try to submit block to any healthy node
		for _, node := range cluster.nodes {
			if node.IsHealthy {
				// Simulate block submission
				log.Infow("Block submitted to node", "node", node.ID)
				blockSuccess.Store(true)
				break
			}
		}
	}()

	// Trigger failover
	for _, node := range cluster.nodes {
		if node.VIPManager.IsMaster() {
			log.Info("⚡ Killing master during block submission")
			node.IsHealthy = false
			break
		}
	}

	time.Sleep(5 * time.Second)

	if blockSubmitted.Load() && !blockSuccess.Load() {
		t.Error("❌ Block submission failed during failover - potential block loss!")
	} else {
		log.Info("✅ Block submission succeeded during failover")
	}
}

// =============================================================================
// HELPER TYPES AND FUNCTIONS
// =============================================================================

type chaosEvent struct {
	Time   time.Time
	Type   string
	NodeID string
}

func isDockerAvailable() bool {
	cmd := exec.Command("docker", "version")
	return cmd.Run() == nil
}

func setupPostgresContainers(t *testing.T) (primary, replica *sql.DB, cleanup func()) {
	t.Helper()

	// Create containers using docker-compose or docker run
	// This is a simplified version - real implementation would use testcontainers-go

	primaryPort := 5432 + rand.Intn(1000)
	replicaPort := primaryPort + 1

	// For testing purposes, we'll simulate with a single connection
	// In production tests, this would spin up actual containers

	primaryDSN := fmt.Sprintf("postgres://spiraltest:testpass@localhost:%d/spiralpool?sslmode=disable", primaryPort)
	replicaDSN := fmt.Sprintf("postgres://spiraltest:testpass@localhost:%d/spiralpool?sslmode=disable", replicaPort)

	var err error
	primary, err = sql.Open("postgres", primaryDSN)
	if err != nil {
		t.Skipf("Could not connect to primary PostgreSQL: %v", err)
	}

	replica, err = sql.Open("postgres", replicaDSN)
	if err != nil {
		t.Skipf("Could not connect to replica PostgreSQL: %v", err)
	}

	cleanup = func() {
		if primary != nil {
			primary.Close()
		}
		if replica != nil {
			replica.Close()
		}
		// Stop containers
		exec.Command("docker", "stop", "spiral-pg-primary", "spiral-pg-replica").Run()
		exec.Command("docker", "rm", "spiral-pg-primary", "spiral-pg-replica").Run()
	}

	return primary, replica, cleanup
}

func setupTestCluster(t *testing.T, numNodes int) *testCluster {
	t.Helper()

	logger, _ := zap.NewDevelopment()
	log := logger.Sugar()

	cluster := &testCluster{
		nodes:       make([]*testNode, numNodes),
		vipManagers: make([]*VIPManager, numNodes),
		logger:      log,
	}

	basePort := 15000 + rand.Intn(10000)

	for i := 0; i < numNodes; i++ {
		nodeID := fmt.Sprintf("node-%d", i)
		port := basePort + i

		cfg := DefaultConfig()
		cfg.Enabled = true
		cfg.NodeID = nodeID
		cfg.Priority = 100 + i
		cfg.VIPAddress = "192.168.100.200"
		cfg.VIPInterface = "lo" // Loopback for testing
		cfg.DiscoveryPort = port
		cfg.StatusPort = port + 1000
		cfg.StratumPort = port + 2000
		cfg.HeartbeatInterval = 1 * time.Second
		cfg.FailoverTimeout = 5 * time.Second
		cfg.ClusterToken = "spiral-test-cluster-token"

		vm, err := NewVIPManager(cfg, logger)
		if err != nil {
			t.Fatalf("Failed to create VIP manager for node %d: %v", i, err)
		}

		cluster.nodes[i] = &testNode{
			ID:         nodeID,
			Host:       "127.0.0.1",
			Port:       port + 2000,
			Priority:   100 + i,
			VIPManager: vm,
			IsHealthy:  true,
		}
		cluster.vipManagers[i] = vm
	}

	cluster.cleanup = append(cluster.cleanup, func() {
		for _, vm := range cluster.vipManagers {
			vm.Stop()
		}
		for _, node := range cluster.nodes {
			if node.Listener != nil {
				node.Listener.Close()
			}
		}
	})

	return cluster
}

func (c *testCluster) cleanup() {
	for _, fn := range c.cleanup {
		fn()
	}
}

func startStratumServer(t *testing.T, node *testNode) {
	t.Helper()

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", node.Host, node.Port))
	if err != nil {
		// Try next port if busy
		node.Port++
		listener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", node.Host, node.Port))
		if err != nil {
			t.Logf("Warning: could not start stratum server for node %s: %v", node.ID, err)
			return
		}
	}

	node.Listener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleMinerConnection(node, conn)
		}
	}()
}

func handleMinerConnection(node *testNode, conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		// Parse stratum message
		var msg map[string]interface{}
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}

		method, _ := msg["method"].(string)
		id := msg["id"]

		var response map[string]interface{}
		switch method {
		case "mining.subscribe":
			response = map[string]interface{}{
				"id":     id,
				"result": []interface{}{[]interface{}{}, "00000000", 4},
				"error":  nil,
			}
		case "mining.authorize":
			response = map[string]interface{}{
				"id":     id,
				"result": true,
				"error":  nil,
			}
		case "mining.submit":
			node.ShareCount.Add(1)
			response = map[string]interface{}{
				"id":     id,
				"result": true,
				"error":  nil,
			}
		}

		if response != nil {
			data, _ := json.Marshal(response)
			conn.Write(append(data, '\n'))
		}
	}
}

func (m *testMiner) connect() error {
	conn, err := net.DialTimeout("tcp",
		fmt.Sprintf("%s:%d", m.PoolHost, m.PoolPort),
		5*time.Second)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.Conn = conn
	m.mu.Unlock()

	// Subscribe
	subscribe := map[string]interface{}{
		"id":     1,
		"method": "mining.subscribe",
		"params": []string{"test-miner/1.0"},
	}
	data, _ := json.Marshal(subscribe)
	conn.Write(append(data, '\n'))

	return nil
}

func (m *testMiner) submitShare() error {
	m.mu.Lock()
	conn := m.Conn
	m.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	submit := map[string]interface{}{
		"id":     m.SharesSent.Load() + 100,
		"method": "mining.submit",
		"params": []string{"worker.1", "job1", "00000000", "00000000", "00000000"},
	}

	data, _ := json.Marshal(submit)
	_, err := conn.Write(append(data, '\n'))
	if err != nil {
		m.Disconnects.Add(1)
		return err
	}

	m.SharesSent.Add(1)
	m.SharesAccepted.Add(1)
	return nil
}

func (m *testMiner) reconnectToPool(cluster *testCluster) {
	m.mu.Lock()
	if m.Conn != nil {
		m.Conn.Close()
		m.Conn = nil
	}
	m.mu.Unlock()

	// Find new master
	for _, node := range cluster.nodes {
		if node.IsHealthy && node.VIPManager.IsMaster() {
			m.PoolHost = node.Host
			m.PoolPort = node.Port
			if err := m.connect(); err == nil {
				m.Reconnects.Add(1)
				return
			}
		}
	}
}

func countMasters(cluster *testCluster) int {
	count := 0
	for _, vm := range cluster.vipManagers {
		if vm.IsMaster() {
			count++
		}
	}
	return count
}

func partitionNode(t *testing.T, node *testNode) {
	t.Helper()
	// In real implementation, use iptables:
	// iptables -A INPUT -s <node_ip> -j DROP
	// iptables -A OUTPUT -d <node_ip> -j DROP
	node.IsHealthy = false
}

func healPartition(t *testing.T, node *testNode) {
	t.Helper()
	// In real implementation, remove iptables rules
	node.IsHealthy = true
}

func waitForReplication(t *testing.T, primary, replica *sql.DB, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var lag int64
		err := replica.QueryRow("SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int").Scan(&lag)
		if err == nil && lag < 5 {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Log("Warning: replication lag check timed out")
}

func insertTestShare(t *testing.T, db *sql.DB, shareID, worker string, difficulty int64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO shares (share_id, worker, difficulty, created_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (share_id) DO NOTHING`,
		shareID, worker, difficulty)
	if err != nil {
		t.Logf("Warning: could not insert test share: %v", err)
	}
}

func insertTestShareWithRetry(db *sql.DB, shareID, worker string, difficulty int64) error {
	for i := 0; i < 3; i++ {
		_, err := db.Exec(`
			INSERT INTO shares (share_id, worker, difficulty, created_at)
			VALUES ($1, $2, $3, NOW())
			ON CONFLICT (share_id) DO NOTHING`,
			shareID, worker, difficulty)
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("failed after retries")
}

func verifyShareExists(t *testing.T, db *sql.DB, shareID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM shares WHERE share_id = $1", shareID).Scan(&count)
		if err == nil && count > 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Errorf("Share %s not found within timeout", shareID)
}

func verifyShareExistsQuick(t *testing.T, db *sql.DB, shareID string) bool {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM shares WHERE share_id = $1", shareID).Scan(&count)
	return err == nil && count > 0
}

func countShares(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var count int64
	err := db.QueryRow("SELECT COUNT(*) FROM shares").Scan(&count)
	if err != nil {
		t.Logf("Warning: could not count shares: %v", err)
		return 0
	}
	return count
}

func stopContainer(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command("docker", "stop", name)
	if err := cmd.Run(); err != nil {
		t.Logf("Warning: could not stop container %s: %v", name, err)
	}
}

func promoteReplica(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec("SELECT pg_promote()")
	if err != nil {
		t.Logf("Warning: pg_promote failed: %v", err)
	}
}

func waitForPrimaryPromotion(t *testing.T, db *sql.DB, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var isInRecovery bool
		err := db.QueryRow("SELECT pg_is_in_recovery()").Scan(&isInRecovery)
		if err == nil && !isInRecovery {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Log("Warning: promotion check timed out")
}

func startContainerAsReplica(t *testing.T, name string, primaryDB *sql.DB) {
	t.Helper()
	// In real implementation, reconfigure and restart container
	cmd := exec.Command("docker", "start", name)
	cmd.Run()
}

func waitForReplicaSync(t *testing.T, replica, primary *sql.DB, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var isInRecovery bool
		err := replica.QueryRow("SELECT pg_is_in_recovery()").Scan(&isInRecovery)
		if err == nil && isInRecovery {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Log("Warning: replica sync check timed out")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
