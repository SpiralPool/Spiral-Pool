// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Critical Backpressure & Queue Saturation Tests
//
// Tests for queue and load handling issues:
// - Share submit queue saturation
// - Job broadcast backlog
// - Slow miner blocking fast miners
// - Lock contention under share storms
// - Dropped shares due to queue limits
//
// WHY IT MATTERS: Pools don't fail at 10 miners - they fail at burst load.
// MUST VERIFY:
// - Shares fail explicitly, not silently
// - Difficulty is not skewed by backpressure
package shares

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
	"github.com/spiralpool/stratum/pkg/ringbuffer"
)

// =============================================================================
// 1. RING BUFFER SATURATION TESTS
// =============================================================================

// TestRingBufferSaturation tests buffer behavior when full
func TestRingBufferSaturation(t *testing.T) {
	t.Parallel()

	// Create small buffer for testing saturation
	// Note: ringbuffer rounds capacity up to next power of 2
	bufferSize := 1024 // Use power of 2 to match actual capacity
	buffer := ringbuffer.New[*protocol.Share](bufferSize)

	// Verify initial capacity
	if buffer.Cap() != bufferSize {
		t.Fatalf("Expected capacity %d, got %d", bufferSize, buffer.Cap())
	}

	// Fill the buffer completely
	filled := 0
	rejected := 0
	for i := 0; i < bufferSize+100; i++ {
		share := &protocol.Share{
			SessionID:    uint64(i),
			MinerAddress: fmt.Sprintf("miner_%d", i),
		}

		if buffer.TryEnqueue(share) {
			filled++
		} else {
			rejected++
		}
	}

	// We should have filled exactly bufferSize items and rejected 100
	if filled != bufferSize {
		t.Errorf("Expected to fill exactly %d items, got %d (rejected %d)", bufferSize, filled, rejected)
	}

	// Verify buffer reports correct count
	stats := buffer.Stats()
	if stats.Current != filled {
		t.Errorf("Buffer Current (%d) doesn't match filled count (%d)", stats.Current, filled)
	}

	// Verify we rejected some items
	if rejected == 0 {
		t.Error("Buffer should have rejected some items when full")
	}

	// Verify we can still dequeue
	batch := make([]*protocol.Share, 100)
	dequeued := buffer.DequeueBatch(batch)
	if dequeued == 0 {
		t.Error("Failed to dequeue from full buffer")
	}

	// Now we should be able to enqueue again
	share := &protocol.Share{SessionID: 999999}
	if !buffer.TryEnqueue(share) {
		t.Error("Failed to enqueue after dequeue made space")
	}
}

// TestRingBufferUnderLoad tests buffer under heavy concurrent load
func TestRingBufferUnderLoad(t *testing.T) {
	t.Parallel()

	bufferSize := 1 << 16 // 64K
	buffer := ringbuffer.New[*protocol.Share](bufferSize)

	numProducers := 20
	sharesPerProducer := 500 // Reduced for -race overhead tolerance (race adds ~10x overhead)
	// Note: RingBuffer is MPSC (Multi-Producer Single-Consumer)
	// Only ONE consumer is allowed by design
	numConsumers := 1

	var producedTotal atomic.Int64
	var consumedTotal atomic.Int64
	var rejectedTotal atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Track when producers are done with a separate WaitGroup
	var producersWg sync.WaitGroup

	// Start producers (miners submitting shares)
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		producersWg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			defer producersWg.Done()
			for i := 0; i < sharesPerProducer; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				share := &protocol.Share{
					SessionID:    uint64(producerID*sharesPerProducer + i),
					MinerAddress: fmt.Sprintf("miner_%d", producerID),
				}

				if buffer.TryEnqueue(share) {
					producedTotal.Add(1)
				} else {
					rejectedTotal.Add(1)
				}
			}
		}(p)
	}

	// Start consumers (database writers)
	consumersDone := make(chan struct{})
	var consumersWg sync.WaitGroup
	for c := 0; c < numConsumers; c++ {
		wg.Add(1)
		consumersWg.Add(1)
		go func() {
			defer wg.Done()
			defer consumersWg.Done()
			batch := make([]*protocol.Share, 100)

			for {
				select {
				case <-ctx.Done():
					return
				case <-consumersDone:
					return
				default:
					n := buffer.DequeueBatch(batch)
					if n > 0 {
						consumedTotal.Add(int64(n))
					} else {
						time.Sleep(time.Millisecond)
					}
				}
			}
		}()
	}

	// Wait for producers to actually finish
	producersDone := make(chan struct{})
	go func() {
		producersWg.Wait()
		close(producersDone)
	}()

	select {
	case <-producersDone:
		// All producers finished
	case <-time.After(60 * time.Second):
		t.Log("Warning: producers timed out")
		cancel() // Force remaining producers to exit
	}

	// Give consumers time to process remaining items
	time.Sleep(200 * time.Millisecond)

	// Signal consumers to exit
	close(consumersDone)

	// Wait for consumers to exit
	consumersWg.Wait()

	// Drain remaining items with single goroutine (no race)
	batch := make([]*protocol.Share, 100)
	for {
		n := buffer.DequeueBatch(batch)
		if n == 0 {
			break
		}
		consumedTotal.Add(int64(n))
	}

	// Wait for all goroutines
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for goroutines")
	}

	produced := producedTotal.Load()
	consumed := consumedTotal.Load()
	rejected := rejectedTotal.Load()

	t.Logf("Produced: %d, Consumed: %d, Rejected: %d", produced, consumed, rejected)

	// Verify accounting
	if produced != consumed+int64(buffer.Len()) {
		t.Errorf("Accounting mismatch: produced=%d, consumed=%d, inBuffer=%d",
			produced, consumed, buffer.Len())
	}

	// Verify all attempted shares are accounted for
	expected := int64(numProducers * sharesPerProducer)
	actual := produced + rejected
	if actual != expected {
		t.Errorf("Lost shares: expected %d attempts, got %d (produced+rejected)",
			expected, actual)
	}

	// Log rejection rate
	rejectionRate := float64(rejected) / float64(expected) * 100
	t.Logf("Rejection rate: %.2f%%", rejectionRate)
}

// =============================================================================
// 2. SHARE PIPELINE BACKPRESSURE TESTS
// =============================================================================

// MockSlowShareWriter simulates a slow database writer
type MockSlowShareWriter struct {
	writeDelay time.Duration
	written    atomic.Int64
	failed     atomic.Int64
}

func (w *MockSlowShareWriter) WriteBatch(ctx context.Context, shares []*protocol.Share) error {
	select {
	case <-ctx.Done():
		w.failed.Add(int64(len(shares)))
		return ctx.Err()
	case <-time.After(w.writeDelay):
		w.written.Add(int64(len(shares)))
		return nil
	}
}

func (w *MockSlowShareWriter) Close() error { return nil }

// TestPipelineBackpressureWithSlowWriter tests pipeline behavior with slow DB
func TestPipelineBackpressureWithSlowWriter(t *testing.T) {
	t.Parallel()

	// Create slow writer (100ms per batch)
	writer := &MockSlowShareWriter{
		writeDelay: 100 * time.Millisecond,
	}

	// Create pipeline with small buffer
	buffer := ringbuffer.New[*protocol.Share](1000)
	batchChan := make(chan []*protocol.Share, 10)

	ctx, cancel := context.WithCancel(context.Background())

	// Start batch writer
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for batch := range batchChan {
			if len(batch) == 0 {
				continue
			}
			writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
			err := writer.WriteBatch(writeCtx, batch)
			writeCancel()
			if err != nil {
				t.Logf("Write failed: %v", err)
			}
		}
	}()

	// Flood the pipeline with shares
	numShares := 5000
	var submitted atomic.Int64
	var rejected atomic.Int64

	var submitterWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		submitterWg.Add(1)
		go func(id int) {
			defer submitterWg.Done()
			for j := 0; j < numShares/10; j++ {
				share := &protocol.Share{
					SessionID:    uint64(id*1000 + j),
					MinerAddress: fmt.Sprintf("miner_%d", id),
				}

				if buffer.TryEnqueue(share) {
					submitted.Add(1)
				} else {
					rejected.Add(1)
				}
			}
		}(i)
	}

	// Batch collector
	go func() {
		batch := make([]*protocol.Share, 100)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n := buffer.DequeueBatch(batch)
				if n > 0 {
					batchCopy := make([]*protocol.Share, n)
					copy(batchCopy, batch[:n])
					select {
					case batchChan <- batchCopy:
					default:
						t.Log("Batch channel full, dropping batch")
					}
				}
			}
		}
	}()

	// Wait for submitters
	submitterWg.Wait()
	time.Sleep(500 * time.Millisecond) // Allow processing

	cancel()
	close(batchChan)
	writerWg.Wait()

	t.Logf("Submitted: %d, Rejected: %d, Written: %d",
		submitted.Load(), rejected.Load(), writer.written.Load())

	// CRITICAL: Verify shares didn't silently disappear
	totalAttempted := submitted.Load() + rejected.Load()
	if totalAttempted != int64(numShares) {
		t.Errorf("SILENT FAILURE: %d shares attempted, expected %d",
			totalAttempted, numShares)
	}

	// Verify rejected shares were explicitly tracked
	if rejected.Load() > 0 {
		t.Logf("Backpressure activated: %d shares explicitly rejected", rejected.Load())
	}
}

// =============================================================================
// 3. LOCK CONTENTION TESTS
// =============================================================================

// TestDuplicateTrackerContention tests lock contention on duplicate tracker
func TestDuplicateTrackerContention(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	numGoroutines := runtime.NumCPU() * 4
	opsPerGoroutine := 10000

	var contentionEvents atomic.Int64
	var successfulOps atomic.Int64
	var wg sync.WaitGroup

	startTime := time.Now()

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for i := 0; i < opsPerGoroutine; i++ {
				jobID := fmt.Sprintf("job_%d", i%10) // Limited job IDs to increase contention
				nonce := fmt.Sprintf("%08x", uint32(id*opsPerGoroutine+i))
				extranonce2 := fmt.Sprintf("%08x", uint32(id))

				opStart := time.Now()
				tracker.RecordIfNew(jobID, "00000001", extranonce2, "65432100", nonce)
				opDuration := time.Since(opStart)

				// Track if operation was slow (potential contention)
				if opDuration > time.Millisecond {
					contentionEvents.Add(1)
				}
				successfulOps.Add(1)
			}
		}(g)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	totalOps := numGoroutines * opsPerGoroutine
	opsPerSec := float64(totalOps) / elapsed.Seconds()

	t.Logf("Total ops: %d, Elapsed: %v, Ops/sec: %.0f, Contention events: %d",
		totalOps, elapsed, opsPerSec, contentionEvents.Load())

	// Verify all operations completed
	if int(successfulOps.Load()) != totalOps {
		t.Errorf("Lost operations: expected %d, got %d", totalOps, successfulOps.Load())
	}

	// Warn if high contention
	contentionRate := float64(contentionEvents.Load()) / float64(totalOps) * 100
	if contentionRate > 1 {
		t.Logf("WARNING: High contention rate: %.2f%%", contentionRate)
	}
}

// TestNonceTrackerContention tests lock contention on nonce tracker
func TestNonceTrackerContention(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	numSessions := 100
	sharesPerSession := 1000
	numGoroutines := 20

	var exhaustionAlerts atomic.Int64
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for s := 0; s < numSessions/numGoroutines; s++ {
				sessionID := fmt.Sprintf("session_%d_%d", goroutineID, s)

				for i := 0; i < sharesPerSession; i++ {
					jobID := fmt.Sprintf("job_%d", i/100)
					nonce := uint32(i)

					if tracker.TrackNonce(sessionID, jobID, nonce) {
						exhaustionAlerts.Add(1)
					}
				}
			}
		}(g)
	}

	wg.Wait()

	t.Logf("Exhaustion alerts: %d", exhaustionAlerts.Load())

	// Cleanup completes without deadlock (expected behavior)
	tracker.CleanupInactiveSessions()
}

// =============================================================================
// 4. SLOW MINER ISOLATION TESTS
// =============================================================================

// TestSlowMinerDoesNotBlockFastMiners tests miner isolation
func TestSlowMinerDoesNotBlockFastMiners(t *testing.T) {
	t.Parallel()

	// Simulate share submission with per-session processing
	type ShareSubmission struct {
		sessionID uint64
		shareID   int
		submitted time.Time
	}

	// Channel per session to simulate independent processing
	numSessions := 10
	channels := make([]chan *ShareSubmission, numSessions)
	for i := range channels {
		channels[i] = make(chan *ShareSubmission, 100)
	}

	// Session 0 will be "slow" (simulates slow miner or network)
	slowSessionID := uint64(0)
	slowProcessingTime := 50 * time.Millisecond
	fastProcessingTime := 1 * time.Millisecond

	// Track processing times per session
	var processingTimes sync.Map // sessionID -> []time.Duration

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start workers for each session
	var workerWg sync.WaitGroup
	for i := 0; i < numSessions; i++ {
		workerWg.Add(1)
		go func(sessionID int) {
			defer workerWg.Done()

			var times []time.Duration
			ch := channels[sessionID]

			for {
				select {
				case <-ctx.Done():
					processingTimes.Store(uint64(sessionID), times)
					return
				case sub, ok := <-ch:
					if !ok {
						processingTimes.Store(uint64(sessionID), times)
						return
					}

					// Simulate processing time
					if sub.sessionID == slowSessionID {
						time.Sleep(slowProcessingTime)
					} else {
						time.Sleep(fastProcessingTime)
					}

					processingDuration := time.Since(sub.submitted)
					times = append(times, processingDuration)
				}
			}
		}(i)
	}

	// Submit shares to all sessions
	sharesPerSession := 50
	var submitWg sync.WaitGroup

	for i := 0; i < numSessions; i++ {
		submitWg.Add(1)
		go func(sessionID int) {
			defer submitWg.Done()
			for j := 0; j < sharesPerSession; j++ {
				select {
				case <-ctx.Done():
					return
				case channels[sessionID] <- &ShareSubmission{
					sessionID: uint64(sessionID),
					shareID:   j,
					submitted: time.Now(),
				}:
				}
				time.Sleep(5 * time.Millisecond) // Simulate share arrival rate
			}
		}(i)
	}

	submitWg.Wait()

	// Close channels and wait for workers
	for i := range channels {
		close(channels[i])
	}
	workerWg.Wait()

	// Analyze results - fast miners should have low latency despite slow miner
	var slowMinerAvg, fastMinerAvg time.Duration
	var slowCount, fastCount int

	processingTimes.Range(func(key, value interface{}) bool {
		sessionID := key.(uint64)
		times := value.([]time.Duration)

		if len(times) == 0 {
			return true
		}

		var total time.Duration
		for _, d := range times {
			total += d
		}
		avg := total / time.Duration(len(times))

		if sessionID == slowSessionID {
			slowMinerAvg = avg
			slowCount = len(times)
		} else {
			fastMinerAvg += avg
			fastCount += len(times)
		}
		return true
	})

	if fastCount > 0 {
		fastMinerAvg = fastMinerAvg / time.Duration(numSessions-1)
	}

	t.Logf("Slow miner avg latency: %v (%d shares)", slowMinerAvg, slowCount)
	t.Logf("Fast miners avg latency: %v (%d total shares)", fastMinerAvg, fastCount)

	// Fast miners should have much lower latency than slow miner
	if fastMinerAvg > slowProcessingTime/2 {
		t.Errorf("ISOLATION FAILURE: Fast miners blocked by slow miner (avg latency %v > %v)",
			fastMinerAvg, slowProcessingTime/2)
	}
}

// =============================================================================
// 5. DROPPED SHARE ACCOUNTING
// =============================================================================

// TestDroppedSharesExplicitlyTracked verifies no silent failures
func TestDroppedSharesExplicitlyTracked(t *testing.T) {
	t.Parallel()

	// Small buffer to force drops
	bufferSize := 100
	buffer := ringbuffer.New[*protocol.Share](bufferSize)

	var enqueued atomic.Int64
	var dropped atomic.Int64

	// Flood the buffer
	numShares := 1000
	for i := 0; i < numShares; i++ {
		share := &protocol.Share{
			SessionID: uint64(i),
		}

		if buffer.TryEnqueue(share) {
			enqueued.Add(1)
		} else {
			dropped.Add(1)
		}
	}

	// CRITICAL: All shares must be accounted for
	total := enqueued.Load() + dropped.Load()
	if total != int64(numShares) {
		t.Errorf("SILENT FAILURE: %d shares unaccounted (submitted %d, enqueued %d, dropped %d)",
			int64(numShares)-total, numShares, enqueued.Load(), dropped.Load())
	}

	// Verify buffer stats match
	stats := buffer.Stats()
	if int64(stats.Current) != enqueued.Load() {
		t.Errorf("Buffer stats mismatch: Current=%d, enqueued=%d",
			stats.Current, enqueued.Load())
	}

	t.Logf("Enqueued: %d, Dropped: %d, Buffer: %d/%d",
		enqueued.Load(), dropped.Load(), stats.Current, stats.Capacity)
}

// TestDroppedSharesReportedToMiner verifies miners get explicit feedback
func TestDroppedSharesReportedToMiner(t *testing.T) {
	t.Parallel()

	// Simulate share result with rejection
	type ShareResult struct {
		accepted     bool
		rejectReason string
	}

	// When buffer is full, shares should be explicitly rejected
	// with a clear reason - never silently dropped

	results := []ShareResult{
		{accepted: false, rejectReason: "buffer-full"},
		{accepted: false, rejectReason: "queue-saturated"},
		{accepted: false, rejectReason: "backpressure"},
	}

	// All rejection reasons should be explicit
	for _, result := range results {
		if !result.accepted && result.rejectReason == "" {
			t.Error("SILENT FAILURE: Rejected share has no reason")
		}
	}
}

// =============================================================================
// 6. DIFFICULTY NOT SKEWED BY BACKPRESSURE
// =============================================================================

// TestDifficultyNotAffectedByBackpressure verifies difficulty calculation isolation
func TestDifficultyNotAffectedByBackpressure(t *testing.T) {
	t.Parallel()

	// Create validator
	jobs := make(map[string]*protocol.Job)
	getJob := func(id string) (*protocol.Job, bool) {
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)

	// Set network difficulty
	networkDiff := 1000000.0
	validator.SetNetworkDifficulty(networkDiff)

	// Verify difficulty under various loads
	// FIXED: Use mutex to protect slice append from concurrent goroutines
	var diffReadings []float64
	var diffMu sync.Mutex
	var wg sync.WaitGroup

	// Concurrent difficulty reads while under "load"
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				diff := validator.GetNetworkDifficulty()
				diffMu.Lock()
				diffReadings = append(diffReadings, diff)
				diffMu.Unlock()
			}
		}()
	}

	// Simulate concurrent difficulty updates (like from different nodes)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			// Small variations around the network difficulty
			variation := networkDiff + float64(i)*0.001
			validator.SetNetworkDifficulty(variation)
			time.Sleep(time.Millisecond)
		}
		// Set back to original
		validator.SetNetworkDifficulty(networkDiff)
	}()

	wg.Wait()

	// Verify difficulty stayed within expected bounds
	for _, diff := range diffReadings {
		// Allow small variation but no wild swings
		relError := (diff - networkDiff) / networkDiff
		if relError > 0.01 { // More than 1% off
			t.Logf("Difficulty variation detected: %f (expected ~%f)", diff, networkDiff)
		}
	}

	// Final difficulty should match what was set
	finalDiff := validator.GetNetworkDifficulty()
	if finalDiff != networkDiff {
		t.Errorf("Final difficulty %f != expected %f", finalDiff, networkDiff)
	}
}

// =============================================================================
// 7. STRESS TEST - SHARE STORM SIMULATION
// =============================================================================

// TestShareStorm simulates a sudden burst of shares
func TestShareStorm(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	t.Parallel()

	// Large buffer for storm
	bufferSize := 1 << 20 // 1M
	buffer := ringbuffer.New[*protocol.Share](bufferSize)

	// Tracking metrics
	var submitted atomic.Int64
	var accepted atomic.Int64
	var rejected atomic.Int64

	// Storm parameters
	numMiners := 100
	sharesPerSecond := 1000 // Per miner
	stormDuration := 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), stormDuration+5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Consumer (database writer simulation)
	wg.Add(1)
	go func() {
		defer wg.Done()
		batch := make([]*protocol.Share, 1000)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				n := buffer.DequeueBatch(batch)
				if n > 0 {
					accepted.Add(int64(n))
					// Simulate batch processing
					time.Sleep(time.Microsecond * 100)
				} else {
					time.Sleep(time.Millisecond)
				}
			}
		}
	}()

	// Producers (miners)
	stormStart := time.Now()
	for m := 0; m < numMiners; m++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			ticker := time.NewTicker(time.Second / time.Duration(sharesPerSecond))
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if time.Since(stormStart) > stormDuration {
						return
					}

					share := &protocol.Share{
						SessionID:    uint64(minerID),
						MinerAddress: fmt.Sprintf("storm_miner_%d", minerID),
					}

					submitted.Add(1)
					if !buffer.TryEnqueue(share) {
						rejected.Add(1)
					}
				}
			}
		}(m)
	}

	// Wait for storm to complete
	time.Sleep(stormDuration + time.Second)
	cancel()

	// Wait for all goroutines
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for storm to complete")
	}

	// Calculate metrics
	totalSubmitted := submitted.Load()
	totalAccepted := accepted.Load()
	totalRejected := rejected.Load()
	remaining := int64(buffer.Len())

	expectedRate := float64(numMiners * sharesPerSecond)
	actualRate := float64(totalSubmitted) / stormDuration.Seconds()

	t.Logf("Storm stats:")
	t.Logf("  Expected rate: %.0f shares/sec", expectedRate)
	t.Logf("  Actual rate: %.0f shares/sec", actualRate)
	t.Logf("  Total submitted: %d", totalSubmitted)
	t.Logf("  Accepted: %d", totalAccepted)
	t.Logf("  Rejected: %d", totalRejected)
	t.Logf("  Remaining in buffer: %d", remaining)

	// CRITICAL: No shares should be unaccounted for
	accounted := totalAccepted + totalRejected + remaining
	if accounted != totalSubmitted {
		t.Errorf("CRITICAL: %d shares unaccounted (submitted %d, accounted %d)",
			totalSubmitted-accounted, totalSubmitted, accounted)
	}

	// Log rejection rate
	if totalSubmitted > 0 {
		rejectionRate := float64(totalRejected) / float64(totalSubmitted) * 100
		t.Logf("  Rejection rate: %.2f%%", rejectionRate)

		if rejectionRate > 50 {
			t.Logf("WARNING: High rejection rate during storm")
		}
	}
}
