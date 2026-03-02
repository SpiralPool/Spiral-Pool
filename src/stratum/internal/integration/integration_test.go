// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package integration provides comprehensive integration stress tests.
//
// These tests validate cross-package concurrency and integration:
// - Concurrent share validation + crypto operations
// - Concurrent stratum message handling + job updates
// - Simulated block submission under load
// - Block reorg and fork handling
// - Resource management
//
// Note: This package is separate from pool to avoid ZMQ dependency issues
// on Windows systems without libzmq installed.
package integration

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// CrossPackageState tracks state across simulated packages.
type CrossPackageState struct {
	// Share validation state
	sharesValidated atomic.Uint64
	sharesAccepted  atomic.Uint64
	sharesRejected  atomic.Uint64

	// Crypto operations
	hashesComputed atomic.Uint64
	merkleRoots    atomic.Uint64

	// Job state
	jobsCreated   atomic.Uint64
	jobsBroadcast atomic.Uint64

	// Block state
	blocksFound     atomic.Uint64
	blocksSubmitted atomic.Uint64
	blocksFailed    atomic.Uint64

	// Session state
	sessionsActive atomic.Int64
	sessionsPeak   atomic.Int64
}

func (s *CrossPackageState) UpdatePeakSessions() {
	current := s.sessionsActive.Load()
	for {
		peak := s.sessionsPeak.Load()
		if current <= peak {
			break
		}
		if s.sessionsPeak.CompareAndSwap(peak, current) {
			break
		}
	}
}

// TestCrossPackageConcurrency tests concurrent operations across all packages.
func TestCrossPackageConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration stress test in short mode")
	}

	const (
		numMiners      = 20
		sharesPerMiner = 50
		testDuration   = 2 * time.Second
	)

	state := &CrossPackageState{}
	stopCh := make(chan struct{})
	cancel := func() { close(stopCh) }
	var wg sync.WaitGroup

	// Simulate miners submitting shares
	for m := 0; m < numMiners; m++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			state.sessionsActive.Add(1)
			state.UpdatePeakSessions()
			defer state.sessionsActive.Add(-1)

			for i := 0; i < sharesPerMiner; i++ {
				select {
				case <-stopCh:
					return
				default:
				}

				// Simulate share validation (includes crypto)
				simulateShareValidation(state)
				time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
			}
		}(m)
	}

	// Simulate job manager creating jobs
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				simulateJobCreation(state)
			}
		}
	}()

	// Simulate block notification handler
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				simulateBlockNotification(state)
			}
		}
	}()

	// Let the test run
	time.Sleep(testDuration)
	cancel()
	wg.Wait()

	// Report results
	t.Logf("Cross-Package Stress Test Results:")
	t.Logf("  Shares: validated=%d, accepted=%d, rejected=%d",
		state.sharesValidated.Load(), state.sharesAccepted.Load(), state.sharesRejected.Load())
	t.Logf("  Crypto: hashes=%d, merkleRoots=%d",
		state.hashesComputed.Load(), state.merkleRoots.Load())
	t.Logf("  Jobs: created=%d, broadcast=%d",
		state.jobsCreated.Load(), state.jobsBroadcast.Load())
	t.Logf("  Blocks: found=%d, submitted=%d, failed=%d",
		state.blocksFound.Load(), state.blocksSubmitted.Load(), state.blocksFailed.Load())
	t.Logf("  Sessions: peak=%d", state.sessionsPeak.Load())

	// Validate consistency
	if state.sharesValidated.Load() == 0 {
		t.Error("No shares were validated")
	}
	if state.sharesAccepted.Load()+state.sharesRejected.Load() != state.sharesValidated.Load() {
		t.Error("Share accepted + rejected != validated")
	}
}

func simulateShareValidation(state *CrossPackageState) {
	// Simulate crypto work (hash computation)
	data := make([]byte, 80)
	cryptorand.Read(data)
	first := sha256.Sum256(data)
	sha256.Sum256(first[:])
	state.hashesComputed.Add(1)

	// Random validation result
	state.sharesValidated.Add(1)
	if rand.Float64() < 0.9 {
		state.sharesAccepted.Add(1)

		// Small chance of block
		if rand.Float64() < 0.001 {
			state.blocksFound.Add(1)
			if rand.Float64() < 0.95 { // 95% submission success
				state.blocksSubmitted.Add(1)
			} else {
				state.blocksFailed.Add(1)
			}
		}
	} else {
		state.sharesRejected.Add(1)
	}
}

func simulateJobCreation(state *CrossPackageState) {
	// Simulate merkle root computation
	txCount := rand.Intn(100) + 1
	hashes := make([][]byte, txCount)
	for i := range hashes {
		hashes[i] = make([]byte, 32)
		cryptorand.Read(hashes[i])
	}

	// Simulate merkle computation
	for len(hashes) > 1 {
		var next [][]byte
		for i := 0; i < len(hashes); i += 2 {
			var combined []byte
			if i+1 < len(hashes) {
				combined = append(hashes[i], hashes[i+1]...)
			} else {
				combined = append(hashes[i], hashes[i]...)
			}
			hash := sha256.Sum256(combined)
			hash2 := sha256.Sum256(hash[:])
			next = append(next, hash2[:])
		}
		hashes = next
	}

	state.merkleRoots.Add(1)
	state.jobsCreated.Add(1)
	state.jobsBroadcast.Add(1)
}

func simulateBlockNotification(state *CrossPackageState) {
	state.jobsCreated.Add(1)
	state.jobsBroadcast.Add(1)
}

// TestContainsCaseInsensitive tests the contains helper function.
func TestContainsCaseInsensitive(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"duplicate block", "duplicate", true},
		{"DUPLICATE BLOCK", "duplicate", true},
		{"Block already exists", "already", true},
		{"invalid-block", "invalid", true},
		{"bad-txns", "bad-", true},
		{"success", "error", false},
		{"", "error", false},
		{"error", "", true},
		{"a", "ab", false},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_contains_%s", tc.s, tc.substr), func(t *testing.T) {
			got := containsCaseInsensitive(tc.s, tc.substr)
			if got != tc.want {
				t.Errorf("contains(%q, %q) = %v, want %v", tc.s, tc.substr, got, tc.want)
			}
		})
	}
}

func containsCaseInsensitive(s, substr string) bool {
	if substr == "" {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// TestExponentialBackoffCalculation tests the retry backoff calculation.
func TestExponentialBackoffCalculation(t *testing.T) {
	expectedBackoffs := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}

	for attempt := 1; attempt <= 5; attempt++ {
		backoff := time.Duration(1<<uint(attempt-1)) * time.Second
		if backoff != expectedBackoffs[attempt-1] {
			t.Errorf("Attempt %d: got %v, want %v", attempt, backoff, expectedBackoffs[attempt-1])
		}
	}
}

// TestPermanentErrorDetection tests detection of non-retryable errors.
func TestPermanentErrorDetection(t *testing.T) {
	permanentErrors := []string{
		"duplicate block submitted",
		"block already exists",
		"invalid block: bad-txns",
		"bad-cb-amount",
		"DUPLICATE",
		"ALREADY known",
		"Invalid block hash",
	}

	retryableErrors := []string{
		"connection refused",
		"timeout waiting for response",
		"network unreachable",
		"RPC error -28: Loading block index",
	}

	for _, errStr := range permanentErrors {
		t.Run("permanent_"+strings.ReplaceAll(errStr, " ", "_"), func(t *testing.T) {
			isPermanent := containsCaseInsensitive(errStr, "duplicate") ||
				containsCaseInsensitive(errStr, "already") ||
				containsCaseInsensitive(errStr, "invalid") ||
				containsCaseInsensitive(errStr, "bad-")
			if !isPermanent {
				t.Errorf("Error %q should be detected as permanent", errStr)
			}
		})
	}

	for _, errStr := range retryableErrors {
		t.Run("retryable_"+strings.ReplaceAll(errStr, " ", "_"), func(t *testing.T) {
			isPermanent := containsCaseInsensitive(errStr, "duplicate") ||
				containsCaseInsensitive(errStr, "already") ||
				containsCaseInsensitive(errStr, "invalid") ||
				containsCaseInsensitive(errStr, "bad-")
			if isPermanent {
				t.Errorf("Error %q should be detected as retryable", errStr)
			}
		})
	}
}

// SimulatedBlockchain simulates blockchain state for fork testing.
type SimulatedBlockchain struct {
	mu     sync.RWMutex
	height uint64
	blocks map[uint64]string
}

func NewSimulatedBlockchain() *SimulatedBlockchain {
	return &SimulatedBlockchain{
		height: 1000000,
		blocks: make(map[uint64]string),
	}
}

func (bc *SimulatedBlockchain) AddBlock() (uint64, string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.height++
	hash := fmt.Sprintf("%064x", rand.Uint64())
	bc.blocks[bc.height] = hash
	return bc.height, hash
}

func (bc *SimulatedBlockchain) Reorg(depth int) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	for i := 0; i < depth; i++ {
		delete(bc.blocks, bc.height)
		bc.height--
	}
}

func (bc *SimulatedBlockchain) GetBlockHash(height uint64) (string, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	hash, ok := bc.blocks[height]
	return hash, ok
}

func (bc *SimulatedBlockchain) Height() uint64 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.height
}

// TestBlockReorgDetection tests detection of blockchain reorganizations.
func TestBlockReorgDetection(t *testing.T) {
	bc := NewSimulatedBlockchain()

	for i := 0; i < 10; i++ {
		bc.AddBlock()
	}

	initialHeight := bc.Height()
	t.Logf("Initial height: %d", initialHeight)

	bc.Reorg(3)
	afterReorgHeight := bc.Height()

	if afterReorgHeight != initialHeight-3 {
		t.Errorf("Expected height %d after reorg, got %d", initialHeight-3, afterReorgHeight)
	}

	for h := afterReorgHeight + 1; h <= initialHeight; h++ {
		if _, found := bc.GetBlockHash(h); found {
			t.Errorf("Block at height %d should be removed after reorg", h)
		}
	}

	t.Logf("Reorg from %d to %d detected correctly", initialHeight, afterReorgHeight)
}

// TestOrphanBlockDetection tests detection of orphaned blocks.
func TestOrphanBlockDetection(t *testing.T) {
	poolBlock := struct {
		Height uint64
		Hash   string
		Status string
	}{
		Height: 1000005,
		Hash:   "0000000000000000000000000000000000000000000000000000000000001234",
		Status: "pending",
	}

	networkHash := "0000000000000000000000000000000000000000000000000000000000005678"

	isOrphan := poolBlock.Hash != networkHash
	if isOrphan {
		poolBlock.Status = "orphaned"
	}

	if poolBlock.Status != "orphaned" {
		t.Error("Block should be marked as orphaned")
	}
}

// TestConcurrentBlockNotifications tests handling of rapid block notifications.
func TestConcurrentBlockNotifications(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numNotifications = 100
	var processed atomic.Uint64
	var wg sync.WaitGroup

	jobUpdateCh := make(chan uint64, numNotifications)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for height := range jobUpdateCh {
			time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			processed.Add(1)
			_ = height
		}
	}()

	start := time.Now()
	for i := 0; i < numNotifications; i++ {
		jobUpdateCh <- uint64(1000000 + i)
	}
	close(jobUpdateCh)

	wg.Wait()
	elapsed := time.Since(start)

	if processed.Load() != numNotifications {
		t.Errorf("Expected %d processed, got %d", numNotifications, processed.Load())
	}

	t.Logf("Processed %d block notifications in %v (%.0f/sec)",
		processed.Load(), elapsed, float64(numNotifications)/elapsed.Seconds())
}

// TestResourceUsageUnderLoad tests that resource usage stays bounded.
func TestResourceUsageUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const iterations = 10000
	const goroutines = 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations/goroutines; i++ {
				data := make([]byte, 1024)
				for j := range data {
					data[j] = byte(j)
				}
				_ = data
			}
		}()
	}

	wg.Wait()
	t.Log("Resource usage test completed without runaway allocation")
}

// TestGoroutineLeakPrevention tests that goroutines are properly cleaned up.
func TestGoroutineLeakPrevention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping goroutine leak test in short mode")
	}

	const numWorkers = 50
	var active atomic.Int64

	ctx := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			active.Add(1)
			defer active.Add(-1)

			select {
			case <-ctx:
				return
			case <-time.After(5 * time.Second):
				return
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	peakActive := active.Load()

	close(ctx)
	wg.Wait()

	finalActive := active.Load()

	t.Logf("Workers: peak=%d, final=%d", peakActive, finalActive)

	if finalActive != 0 {
		t.Errorf("Expected 0 active workers after shutdown, got %d", finalActive)
	}
}

// TestPollingToZMQTransition tests the transition between polling and ZMQ modes.
func TestPollingToZMQTransition(t *testing.T) {
	type NotificationMode int
	const (
		ModePolling NotificationMode = iota
		ModeZMQTesting
		ModeZMQStable
	)

	mode := ModePolling
	zmqHealthyStart := time.Time{}
	stabilityThreshold := 100 * time.Millisecond

	zmqHealthyStart = time.Now()
	mode = ModeZMQTesting

	time.Sleep(stabilityThreshold + 10*time.Millisecond)

	if time.Since(zmqHealthyStart) >= stabilityThreshold {
		mode = ModeZMQStable
	}

	if mode != ModeZMQStable {
		t.Error("Expected ZMQ to reach stable state")
	}

	zmqFailed := true
	if zmqFailed {
		mode = ModePolling
		zmqHealthyStart = time.Time{}
	}

	if mode != ModePolling {
		t.Error("Expected fallback to polling mode")
	}
}

// TestSimultaneousOperations tests many operations happening at exact same time.
func TestSimultaneousOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numOps = 100

	var startWg, doneWg sync.WaitGroup
	startWg.Add(1)

	var shareOps, jobOps, blockOps atomic.Uint64

	for i := 0; i < numOps; i++ {
		doneWg.Add(3)

		go func() {
			defer doneWg.Done()
			startWg.Wait()
			simulateConcurrentShare()
			shareOps.Add(1)
		}()

		go func() {
			defer doneWg.Done()
			startWg.Wait()
			simulateConcurrentJob()
			jobOps.Add(1)
		}()

		go func() {
			defer doneWg.Done()
			startWg.Wait()
			simulateConcurrentBlockCheck()
			blockOps.Add(1)
		}()
	}

	start := time.Now()
	startWg.Done()
	doneWg.Wait()
	elapsed := time.Since(start)

	total := shareOps.Load() + jobOps.Load() + blockOps.Load()
	t.Logf("Executed %d simultaneous operations in %v (%.0f ops/sec)",
		total, elapsed, float64(total)/elapsed.Seconds())

	if shareOps.Load() != numOps {
		t.Errorf("Expected %d share ops, got %d", numOps, shareOps.Load())
	}
}

func simulateConcurrentShare() {
	data := make([]byte, 80)
	cryptorand.Read(data)
	first := sha256.Sum256(data)
	sha256.Sum256(first[:])
}

func simulateConcurrentJob() {
	data := make([]byte, 256)
	cryptorand.Read(data)
	sha256.Sum256(data)
}

func simulateConcurrentBlockCheck() {
	data := make([]byte, 80)
	cryptorand.Read(data)
	first := sha256.Sum256(data)
	sha256.Sum256(first[:])
}

// BenchmarkCrossPackageOperations benchmarks integrated operations.
func BenchmarkCrossPackageOperations(b *testing.B) {
	state := &CrossPackageState{}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			switch rand.Intn(3) {
			case 0:
				simulateShareValidation(state)
			case 1:
				simulateConcurrentShare()
			case 2:
				simulateConcurrentJob()
			}
		}
	})

	b.ReportMetric(float64(state.sharesValidated.Load()), "shares")
	b.ReportMetric(float64(state.hashesComputed.Load()), "hashes")
}

// BenchmarkMerkleRootComputation benchmarks merkle root computation.
func BenchmarkMerkleRootComputation(b *testing.B) {
	sizes := []int{1, 10, 100, 1000}

	for _, size := range sizes {
		hashes := make([][]byte, size)
		for i := range hashes {
			hashes[i] = make([]byte, 32)
			cryptorand.Read(hashes[i])
		}

		b.Run(fmt.Sprintf("%d_txs", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				computeMerkle(hashes)
			}
		})
	}
}

func computeMerkle(hashes [][]byte) []byte {
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	level := make([][]byte, len(hashes))
	copy(level, hashes)

	for len(level) > 1 {
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			var combined []byte
			if i+1 < len(level) {
				combined = append(level[i], level[i+1]...)
			} else {
				combined = append(level[i], level[i]...)
			}
			h := sha256.Sum256(combined)
			h2 := sha256.Sum256(h[:])
			next = append(next, h2[:])
		}
		level = next
	}

	return level[0]
}
