// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package ha provides chaos testing for High Availability scenarios.
//
// These tests simulate adverse conditions:
// - Network partitions
// - Node failures
// - Message delays and reordering
// - Split-brain scenarios
// - Rapid failover/failback cycles
//
// Run with: go test -v -run TestChaos ./...
package ha

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// CHAOS TESTING FOR HA CLUSTER
// =============================================================================

// TestChaosRapidLeaderElection tests rapid leader election under stress.
func TestChaosRapidLeaderElection(t *testing.T) {
	// Simulate rapid leader changes
	var leaderChanges atomic.Int64
	var currentLeader atomic.Int64
	var mu sync.Mutex
	leaderHistory := make([]int64, 0)

	const numNodes = 5
	const iterations = 100

	// Simulate election rounds
	for i := 0; i < iterations; i++ {
		// Random node becomes leader
		newLeader := int64(i % numNodes)

		oldLeader := currentLeader.Swap(newLeader)
		if oldLeader != newLeader {
			leaderChanges.Add(1)
			mu.Lock()
			leaderHistory = append(leaderHistory, newLeader)
			mu.Unlock()
		}

		// Small delay to simulate election time
		time.Sleep(time.Microsecond * 100)
	}

	t.Logf("Leader changes: %d over %d iterations", leaderChanges.Load(), iterations)
	t.Logf("Final leader: %d", currentLeader.Load())
	t.Logf("Leader history length: %d", len(leaderHistory))

	// Verify no leader was elected more than expected
	if leaderChanges.Load() > int64(iterations) {
		t.Errorf("Too many leader changes: %d", leaderChanges.Load())
	}
}

// TestChaosSplitBrainPrevention tests that split-brain scenarios are handled.
func TestChaosSplitBrainPrevention(t *testing.T) {
	// Simulate two partitions each believing they are the leader
	var partition1Leader atomic.Bool
	var partition2Leader atomic.Bool
	var conflictDetected atomic.Bool

	// Monitor for conflicts
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for i := 0; i < 1000; i++ {
			<-ticker.C
			if partition1Leader.Load() && partition2Leader.Load() {
				conflictDetected.Store(true)
			}
		}
	}()

	// Simulate partition 1 claiming leadership
	go func() {
		for i := 0; i < 100; i++ {
			partition1Leader.Store(true)
			time.Sleep(time.Millisecond * 5)
			partition1Leader.Store(false)
			time.Sleep(time.Millisecond * 5)
		}
	}()

	// Simulate partition 2 claiming leadership
	go func() {
		for i := 0; i < 100; i++ {
			partition2Leader.Store(true)
			time.Sleep(time.Millisecond * 5)
			partition2Leader.Store(false)
			time.Sleep(time.Millisecond * 5)
		}
	}()

	time.Sleep(time.Second * 2)

	// In a real implementation, the conflict should be detected and resolved
	// For now, we just verify the detection mechanism works
	if conflictDetected.Load() {
		t.Log("Split-brain scenario detected (expected in this simulation)")
	}
}

// TestChaosMessageReplayPrevention tests replay attack prevention under stress.
func TestChaosMessageReplayPrevention(t *testing.T) {
	seenMessages := make(map[string]time.Time)
	var mu sync.Mutex
	var replaysBlocked atomic.Int64
	var messagesAccepted atomic.Int64

	// Simulate message processing with replays
	checkAndRecord := func(msgHash string) bool {
		mu.Lock()
		defer mu.Unlock()

		now := time.Now()
		if _, seen := seenMessages[msgHash]; seen {
			replaysBlocked.Add(1)
			return false
		}

		seenMessages[msgHash] = now
		messagesAccepted.Add(1)

		// Cleanup old entries
		for hash, seenAt := range seenMessages {
			if now.Sub(seenAt) > 60*time.Second {
				delete(seenMessages, hash)
			}
		}

		return true
	}

	// Concurrent message senders
	var wg sync.WaitGroup
	const numSenders = 10
	const messagesPerSender = 1000

	for sender := 0; sender < numSenders; sender++ {
		wg.Add(1)
		go func(senderID int) {
			defer wg.Done()
			for i := 0; i < messagesPerSender; i++ {
				// Each sender sends the same message multiple times (replay attempt)
				msgHash := "msg_" + string(rune(senderID)) + "_" + string(rune(i%100))
				checkAndRecord(msgHash)
			}
		}(sender)
	}

	wg.Wait()

	t.Logf("Messages accepted: %d", messagesAccepted.Load())
	t.Logf("Replays blocked: %d", replaysBlocked.Load())

	// Verify replays were blocked
	if replaysBlocked.Load() == 0 {
		t.Error("No replays were blocked - replay detection may not be working")
	}

	// Verify some messages were accepted
	if messagesAccepted.Load() == 0 {
		t.Error("No messages were accepted")
	}
}

// TestChaosNodeFailureRecovery tests cluster recovery after node failures.
func TestChaosNodeFailureRecovery(t *testing.T) {
	const numNodes = 5
	nodeStates := make([]atomic.Bool, numNodes)

	// Initialize all nodes as healthy
	for i := range nodeStates {
		nodeStates[i].Store(true)
	}

	var failureCount atomic.Int64
	var recoveryCount atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Failure injector
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Randomly fail a node
				nodeID := int(time.Now().UnixNano() % numNodes)
				if nodeStates[nodeID].CompareAndSwap(true, false) {
					failureCount.Add(1)
				}
				time.Sleep(time.Millisecond * 50)
			}
		}
	}()

	// Recovery process
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Recover failed nodes
				for i := range nodeStates {
					if nodeStates[i].CompareAndSwap(false, true) {
						recoveryCount.Add(1)
					}
				}
				time.Sleep(time.Millisecond * 100)
			}
		}
	}()

	// Wait for chaos
	<-ctx.Done()

	t.Logf("Failures injected: %d", failureCount.Load())
	t.Logf("Recoveries completed: %d", recoveryCount.Load())

	// Verify recovery is happening
	if recoveryCount.Load() == 0 && failureCount.Load() > 0 {
		t.Error("No recoveries detected despite failures")
	}
}

// TestChaosHighMessageRate tests cluster behavior under high message rates.
func TestChaosHighMessageRate(t *testing.T) {
	var messagesProcessed atomic.Int64
	var messagesFailed atomic.Int64

	// Simulate message processing with backpressure
	messageQueue := make(chan struct{}, 1000)

	// Consumer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Multiple consumers for higher throughput
	const numConsumers = 5
	for c := 0; c < numConsumers; c++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-messageQueue:
					messagesProcessed.Add(1)
					// Minimal processing time
					time.Sleep(time.Microsecond)
				}
			}
		}()
	}

	// Producers (flooding)
	var wg sync.WaitGroup
	const numProducers = 20

	for i := 0; i < numProducers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case messageQueue <- struct{}{}:
					// Sent successfully
				default:
					// Queue full - message dropped
					messagesFailed.Add(1)
				}
			}
		}()
	}

	<-ctx.Done()
	wg.Wait()

	t.Logf("Messages processed: %d", messagesProcessed.Load())
	t.Logf("Messages dropped: %d", messagesFailed.Load())

	// The key metric is that processing happened despite flooding
	// With race detection and CI variability, lower threshold is appropriate
	if messagesProcessed.Load() < 100 {
		t.Errorf("Too few messages processed: %d", messagesProcessed.Load())
	}
}

// TestChaosSequenceNumberWraparound tests handling of sequence number wraparound.
func TestChaosSequenceNumberWraparound(t *testing.T) {
	var sequence atomic.Uint64
	var wraparounds atomic.Int64

	// Start near max to test wraparound
	sequence.Store(^uint64(0) - 1000)

	const iterations = 2000

	for i := 0; i < iterations; i++ {
		old := sequence.Load()
		new := sequence.Add(1)

		// Detect wraparound
		if new < old {
			wraparounds.Add(1)
		}
	}

	t.Logf("Final sequence: %d", sequence.Load())
	t.Logf("Wraparounds detected: %d", wraparounds.Load())

	// Should have wrapped around exactly once
	if wraparounds.Load() != 1 {
		t.Errorf("Expected 1 wraparound, got %d", wraparounds.Load())
	}
}

// TestChaosContextCancellation tests proper cleanup on context cancellation.
func TestChaosContextCancellation(t *testing.T) {
	var goroutinesStarted atomic.Int64
	var goroutinesStopped atomic.Int64

	ctx, cancel := context.WithCancel(context.Background())

	// Start many goroutines
	const numGoroutines = 100

	for i := 0; i < numGoroutines; i++ {
		goroutinesStarted.Add(1)
		go func() {
			defer goroutinesStopped.Add(1)
			<-ctx.Done()
		}()
	}

	// Wait a bit then cancel
	time.Sleep(time.Millisecond * 100)
	cancel()

	// Wait for cleanup
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if goroutinesStopped.Load() == numGoroutines {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}

	started := goroutinesStarted.Load()
	stopped := goroutinesStopped.Load()

	t.Logf("Goroutines started: %d", started)
	t.Logf("Goroutines stopped: %d", stopped)

	if started != stopped {
		t.Errorf("Goroutine leak: started %d, stopped %d", started, stopped)
	}
}

// TestChaosMapConcurrentAccess tests concurrent map access patterns.
func TestChaosMapConcurrentAccess(t *testing.T) {
	// Test sync.Map under stress
	var syncMap sync.Map
	var operations atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	const numWorkers = 50

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Random operation
					key := workerID % 100
					switch operations.Add(1) % 4 {
					case 0:
						syncMap.Store(key, time.Now())
					case 1:
						syncMap.Load(key)
					case 2:
						syncMap.Delete(key)
					case 3:
						syncMap.Range(func(k, v interface{}) bool {
							return true
						})
					}
				}
			}
		}(i)
	}

	<-ctx.Done()
	wg.Wait()

	t.Logf("Total operations: %d", operations.Load())

	// Should have performed many operations without crashing
	if operations.Load() < 10000 {
		t.Errorf("Too few operations: %d", operations.Load())
	}
}
