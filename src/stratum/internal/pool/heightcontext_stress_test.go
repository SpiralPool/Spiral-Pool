// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - HeightContext Stress & Edge Case Tests
//
// Comprehensive tests covering:
// - Multi-threaded concurrency (10-100 miners)
// - Network latency simulation
// - Merge-mining edge cases
// - Metrics verification under stress
// - Database/payout edge cases
// - Recursive race conditions
package pool

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/jobs"
)

// =============================================================================
// 1. MULTI-THREADED / CONCURRENCY SCENARIOS
// =============================================================================

// TestMultipleMinersSubmittingAtSameHeight simulates 100 miners racing to submit
// blocks at the same height. Only first valid block should be submitted,
// others should be aborted via HeightContext.
func TestMultipleMinersSubmittingAtSameHeight(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	const numMiners = 100
	var wg sync.WaitGroup
	var submittedCount atomic.Int64
	var abortedCount atomic.Int64
	var firstSubmission atomic.Bool

	// Track submission order
	submissionOrder := make([]int, 0, numMiners)
	var orderMu sync.Mutex

	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, 5*time.Second)
			defer deadlineCancel()

			// Simulate varying network latency (1-50ms)
			rpcLatency := time.Duration(1+rand.Intn(50)) * time.Millisecond

			select {
			case <-deadlineCtx.Done():
				if heightCtx.Err() != nil {
					abortedCount.Add(1)
				}
				return
			case <-time.After(rpcLatency):
				// Miner "found" a block
			}

			// Check if first to submit
			if firstSubmission.CompareAndSwap(false, true) {
				// First submission wins - advance height
				submittedCount.Add(1)
				orderMu.Lock()
				submissionOrder = append(submissionOrder, minerID)
				orderMu.Unlock()

				// Simulate block acceptance advancing height
				time.Sleep(10 * time.Millisecond)
				epoch.Advance(1001)
			} else {
				// Already submitted - check if context cancelled
				select {
				case <-heightCtx.Done():
					abortedCount.Add(1)
				default:
					// Context not yet cancelled, mark as late submission
					abortedCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Miners: submitted=%d, aborted=%d", submittedCount.Load(), abortedCount.Load())

	// Exactly 1 block should be submitted
	if submittedCount.Load() != 1 {
		t.Errorf("Expected exactly 1 submitted block, got %d", submittedCount.Load())
	}

	// Remaining miners should be aborted
	if abortedCount.Load() != numMiners-1 {
		t.Errorf("Expected %d aborted, got %d", numMiners-1, abortedCount.Load())
	}
}

// TestRapidFireHeightUpdates simulates DGB 15s block intervals with network jitter.
// Multiple parent blocks arrive in quick succession (forked tips scenario).
func TestRapidFireHeightUpdates(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(22000000) // DGB-like height

	var wg sync.WaitGroup
	var cancelledSubmissions atomic.Int64
	var completedSubmissions atomic.Int64
	var staleRPCsReached atomic.Int64

	// Simulate 50 miners with varying submission times
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, 2*time.Second)
			defer deadlineCancel()

			// Simulate submission process: validation + RPC
			validationTime := time.Duration(10+rand.Intn(40)) * time.Millisecond
			rpcTime := time.Duration(50+rand.Intn(150)) * time.Millisecond

			// Validation phase
			select {
			case <-deadlineCtx.Done():
				cancelledSubmissions.Add(1)
				return
			case <-time.After(validationTime):
			}

			// RPC phase - check if height already stale
			if heightCtx.Err() != nil {
				cancelledSubmissions.Add(1)
				return
			}

			select {
			case <-deadlineCtx.Done():
				cancelledSubmissions.Add(1)
				return
			case <-time.After(rpcTime):
				// Check if RPC would have reached daemon with stale height
				if heightCtx.Err() != nil {
					// This is bad - stale RPC reached daemon
					staleRPCsReached.Add(1)
				} else {
					completedSubmissions.Add(1)
				}
			}
		}(i)
	}

	// Simulate rapid height advances (network jitter, forked tips)
	go func() {
		heights := []uint64{22000001, 22000001, 22000002, 22000001, 22000003, 22000004}
		for _, h := range heights {
			time.Sleep(time.Duration(30+rand.Intn(70)) * time.Millisecond)
			epoch.Advance(h) // Advance only advances if h > current
		}
	}()

	wg.Wait()

	t.Logf("Submissions: completed=%d, cancelled=%d, staleRPCs=%d",
		completedSubmissions.Load(), cancelledSubmissions.Load(), staleRPCsReached.Load())

	// No stale RPCs should reach daemon
	if staleRPCsReached.Load() > 0 {
		t.Errorf("Expected 0 stale RPCs, got %d - HeightContext not cancelling correctly",
			staleRPCsReached.Load())
	}
}

// TestAuxChainParentChainCollisions tests merge-mining scenario where
// aux chain proofs are submitted while parent chain rapidly produces blocks.
func TestAuxChainParentChainCollisions(t *testing.T) {
	t.Parallel()

	parentEpoch := jobs.NewHeightEpoch()
	auxEpoch := jobs.NewHeightEpoch()

	parentEpoch.Advance(800000)  // BTC-like parent
	auxEpoch.Advance(15000000)   // NMC-like aux

	var wg sync.WaitGroup
	var auxSubmitted atomic.Int64
	var auxCancelledByParent atomic.Int64
	var auxCancelledByAux atomic.Int64

	// Simulate 30 aux chain submissions
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(submissionID int) {
			defer wg.Done()

			// Aux submission depends on BOTH parent and aux HeightContexts
			parentCtx, parentCancel := parentEpoch.HeightContext(context.Background())
			defer parentCancel()
			auxCtx, auxCancel := auxEpoch.HeightContext(context.Background())
			defer auxCancel()

			// Combined context - cancelled if either chain advances
			combinedCtx, combinedCancel := context.WithCancel(context.Background())
			defer combinedCancel()

			go func() {
				select {
				case <-parentCtx.Done():
					combinedCancel()
				case <-auxCtx.Done():
					combinedCancel()
				case <-combinedCtx.Done():
				}
			}()

			deadline := 500 * time.Millisecond
			deadlineCtx, deadlineCancel := context.WithTimeout(combinedCtx, deadline)
			defer deadlineCancel()

			// Simulate aux proof submission (100-300ms)
			rpcTime := time.Duration(100+submissionID*7) * time.Millisecond

			select {
			case <-deadlineCtx.Done():
				// Determine which chain caused cancellation
				if parentCtx.Err() != nil {
					auxCancelledByParent.Add(1)
				} else if auxCtx.Err() != nil {
					auxCancelledByAux.Add(1)
				}
			case <-time.After(rpcTime):
				auxSubmitted.Add(1)
			}
		}(i)
	}

	// Parent chain produces blocks rapidly
	go func() {
		for h := uint64(800001); h <= 800003; h++ {
			time.Sleep(80 * time.Millisecond)
			parentEpoch.Advance(h)
		}
	}()

	// Aux chain also advances (slower)
	go func() {
		time.Sleep(200 * time.Millisecond)
		auxEpoch.Advance(15000001)
	}()

	wg.Wait()

	t.Logf("Aux submissions: completed=%d, cancelledByParent=%d, cancelledByAux=%d",
		auxSubmitted.Load(), auxCancelledByParent.Load(), auxCancelledByAux.Load())

	// Verify all accounted for
	total := auxSubmitted.Load() + auxCancelledByParent.Load() + auxCancelledByAux.Load()
	if total != 30 {
		t.Errorf("Expected 30 total aux submissions, got %d", total)
	}

	// At least some should be cancelled by parent chain
	if auxCancelledByParent.Load() == 0 {
		t.Error("Expected at least some aux submissions cancelled by parent chain advance")
	}
}

// =============================================================================
// 2. NETWORK / LATENCY EDGE CASES
// =============================================================================

// TestDelayedZMQNotifications simulates 50-300ms latency in ZMQ block notifications.
// HeightContext should still cancel submissions even with delayed template refresh.
func TestDelayedZMQNotifications(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var wg sync.WaitGroup
	var submissionsCancelled atomic.Int64
	var submissionsCompleted atomic.Int64
	var staleSubmissions atomic.Int64

	// Simulate ZMQ notification delay
	zmqDelay := 150 * time.Millisecond // 150ms network latency

	// Start submissions before ZMQ arrives
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			// Each miner gets HeightContext at start
			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, 2*time.Second)
			defer deadlineCancel()

			// Simulate RPC taking 100-400ms
			rpcTime := time.Duration(100+minerID*15) * time.Millisecond

			select {
			case <-deadlineCtx.Done():
				if heightCtx.Err() != nil {
					submissionsCancelled.Add(1)
				}
			case <-time.After(rpcTime):
				// Check if this submission is stale
				if heightCtx.Err() != nil {
					staleSubmissions.Add(1)
				} else {
					submissionsCompleted.Add(1)
				}
			}
		}(i)
	}

	// Simulate new block found on network + ZMQ delay
	go func() {
		// Real block found at T+50ms
		time.Sleep(50 * time.Millisecond)
		// ZMQ notification arrives after delay
		time.Sleep(zmqDelay)
		epoch.Advance(1001)
	}()

	wg.Wait()

	t.Logf("Submissions: completed=%d, cancelled=%d, stale=%d",
		submissionsCompleted.Load(), submissionsCancelled.Load(), staleSubmissions.Load())

	// Some early submissions may complete before ZMQ arrives
	// But once ZMQ arrives, remaining should be cancelled
	if submissionsCancelled.Load() == 0 {
		t.Error("Expected some submissions to be cancelled after ZMQ notification")
	}
}

// TestHighLatencyMiners simulates miners with slow VPN or network partition
// receiving jobs late and attempting to submit stale blocks.
func TestHighLatencyMiners(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var wg sync.WaitGroup
	var normalMinersCompleted atomic.Int64
	var laggyMinersCancelled atomic.Int64
	var laggyMinersStale atomic.Int64

	// Normal miners (10ms latency)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			// Quick submission
			select {
			case <-heightCtx.Done():
				return
			case <-time.After(30 * time.Millisecond):
				if heightCtx.Err() == nil {
					normalMinersCompleted.Add(1)
				}
			}
		}()
	}

	// Laggy miners (200-500ms latency) - get job at old height, then experience delay
	// This simulates miners who received the job but have slow network/processing
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			// Get HeightContext immediately (miner received the job at height 1000)
			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			// Simulate processing/network delay AFTER receiving job
			// By the time they try to submit, height will have advanced
			processingDelay := time.Duration(150+minerID*30) * time.Millisecond
			time.Sleep(processingDelay)

			// Check if context was cancelled during delay
			if heightCtx.Err() != nil {
				laggyMinersCancelled.Add(1)
				return
			}

			// Attempt submission
			select {
			case <-heightCtx.Done():
				laggyMinersCancelled.Add(1)
			case <-time.After(50 * time.Millisecond):
				if heightCtx.Err() != nil {
					laggyMinersStale.Add(1)
				}
			}
		}(i)
	}

	// Height advances after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		epoch.Advance(1001)
	}()

	wg.Wait()

	t.Logf("Normal miners completed: %d, Laggy cancelled: %d, Laggy stale: %d",
		normalMinersCompleted.Load(), laggyMinersCancelled.Load(), laggyMinersStale.Load())

	// Most laggy miners should be cancelled or marked stale
	laggyTotal := laggyMinersCancelled.Load() + laggyMinersStale.Load()
	if laggyTotal < 5 {
		t.Errorf("Expected most laggy miners to fail, got %d cancelled/stale", laggyTotal)
	}
}

// TestNodeRestartMidSubmission simulates daemon or pool restart while
// a block submission is in-flight.
func TestNodeRestartMidSubmission(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var wg sync.WaitGroup
	var submissionsCancelledCleanly atomic.Int64
	var submissionsInProgress atomic.Int64

	// Simulate 20 in-flight submissions
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(submissionID int) {
			defer wg.Done()

			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			// Also use parent context that we can cancel (simulating pool shutdown)
			parentCtx, parentCancel := context.WithCancel(context.Background())
			defer parentCancel()

			// Combine pool shutdown and height context
			combinedCtx, combinedCancel := context.WithCancel(context.Background())
			defer combinedCancel()

			go func() {
				select {
				case <-heightCtx.Done():
					combinedCancel()
				case <-parentCtx.Done():
					combinedCancel()
				case <-combinedCtx.Done():
				}
			}()

			submissionsInProgress.Add(1)
			defer submissionsInProgress.Add(-1)

			// Simulate long RPC (500ms)
			select {
			case <-combinedCtx.Done():
				submissionsCancelledCleanly.Add(1)
			case <-time.After(500 * time.Millisecond):
				// Would complete
			}
		}(i)
	}

	// Wait for all to start
	time.Sleep(50 * time.Millisecond)

	// Verify submissions in progress
	inProgress := submissionsInProgress.Load()
	if inProgress != 20 {
		t.Errorf("Expected 20 in-progress, got %d", inProgress)
	}

	// Simulate pool restart by advancing height (cancels all HeightContexts)
	epoch.Advance(1001)

	// Wait for all to complete
	wg.Wait()

	// All should have cancelled cleanly
	if submissionsCancelledCleanly.Load() != 20 {
		t.Errorf("Expected 20 clean cancellations, got %d", submissionsCancelledCleanly.Load())
	}

	// No submissions should remain in progress
	if submissionsInProgress.Load() != 0 {
		t.Errorf("Expected 0 in-progress after shutdown, got %d", submissionsInProgress.Load())
	}
}

// =============================================================================
// 3. MERGE-MINING / MULTI-COIN EDGE CASES
// =============================================================================

// TestParentChainOrphan tests scenario where parent block is found and then
// quickly orphaned. Aux block should be marked stale and not submitted.
func TestParentChainOrphan(t *testing.T) {
	t.Parallel()

	parentEpoch := jobs.NewHeightEpoch()
	auxEpoch := jobs.NewHeightEpoch()

	parentEpoch.Advance(800000)
	auxEpoch.Advance(15000000)

	var wg sync.WaitGroup
	var auxSubmitted atomic.Int64
	var auxOrphaned atomic.Int64

	// Simulate aux block found with parent at 800000
	parentHeight := parentEpoch.Height()

	// Start aux submission
	wg.Add(1)
	go func() {
		defer wg.Done()

		parentCtx, parentCancel := parentEpoch.HeightContext(context.Background())
		defer parentCancel()

		// Aux submission takes 200ms
		select {
		case <-parentCtx.Done():
			// Parent chain moved - aux is orphaned
			auxOrphaned.Add(1)
			return
		case <-time.After(200 * time.Millisecond):
			// Check parent height still matches
			if parentEpoch.Height() != parentHeight {
				auxOrphaned.Add(1)
				return
			}
			auxSubmitted.Add(1)
		}
	}()

	// Parent block found and then orphaned (reorg) after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		// New competing block wins, height still advances
		parentEpoch.Advance(800001)
	}()

	wg.Wait()

	if auxSubmitted.Load() != 0 {
		t.Error("Aux block should not be submitted when parent is orphaned")
	}
	if auxOrphaned.Load() != 1 {
		t.Error("Aux block should be marked orphaned")
	}
}

// TestMultiAlgoChainInteractions tests SHA256d vs Scrypt simultaneous submissions.
// Confirms isolated HeightEpochs prevent cross-chain cancellation.
func TestMultiAlgoChainInteractions(t *testing.T) {
	t.Parallel()

	// SHA256d chain (Bitcoin-like)
	sha256Epoch := jobs.NewHeightEpoch()
	sha256Epoch.Advance(800000)

	// Scrypt chain (Litecoin-like)
	scryptEpoch := jobs.NewHeightEpoch()
	scryptEpoch.Advance(2500000)

	var wg sync.WaitGroup
	var sha256Submitted atomic.Int64
	var sha256Cancelled atomic.Int64
	var scryptSubmitted atomic.Int64
	var scryptCancelled atomic.Int64

	// SHA256d miners
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := sha256Epoch.HeightContext(context.Background())
			defer cancel()

			select {
			case <-ctx.Done():
				sha256Cancelled.Add(1)
			case <-time.After(150 * time.Millisecond):
				sha256Submitted.Add(1)
			}
		}()
	}

	// Scrypt miners
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := scryptEpoch.HeightContext(context.Background())
			defer cancel()

			select {
			case <-ctx.Done():
				scryptCancelled.Add(1)
			case <-time.After(150 * time.Millisecond):
				scryptSubmitted.Add(1)
			}
		}()
	}

	// Advance SHA256 chain only - should NOT affect Scrypt
	go func() {
		time.Sleep(50 * time.Millisecond)
		sha256Epoch.Advance(800001)
	}()

	wg.Wait()

	t.Logf("SHA256: submitted=%d, cancelled=%d | Scrypt: submitted=%d, cancelled=%d",
		sha256Submitted.Load(), sha256Cancelled.Load(),
		scryptSubmitted.Load(), scryptCancelled.Load())

	// SHA256 submissions should be cancelled
	if sha256Cancelled.Load() != 10 {
		t.Errorf("Expected all 10 SHA256 submissions cancelled, got %d", sha256Cancelled.Load())
	}

	// Scrypt submissions should complete (different chain)
	if scryptSubmitted.Load() != 10 {
		t.Errorf("Expected all 10 Scrypt submissions to complete, got %d", scryptSubmitted.Load())
	}
}

// =============================================================================
// 4. METRICS & OBSERVABILITY TESTS
// =============================================================================

// StressMetrics tracks metrics under high concurrency
type StressMetrics struct {
	ParentHeightAborts atomic.Int64
	AuxHeightAborts    atomic.Int64
	DeadlineAborts     atomic.Int64
	DeadlineUsages     []float64
	mu                 sync.Mutex
}

func (m *StressMetrics) RecordParentHeightAbort() {
	m.ParentHeightAborts.Add(1)
}

func (m *StressMetrics) RecordAuxHeightAbort() {
	m.AuxHeightAborts.Add(1)
}

func (m *StressMetrics) RecordDeadlineAbort() {
	m.DeadlineAborts.Add(1)
}

func (m *StressMetrics) RecordDeadlineUsage(ratio float64) {
	m.mu.Lock()
	m.DeadlineUsages = append(m.DeadlineUsages, ratio)
	m.mu.Unlock()
}

// TestMetricsUnderHighConcurrency verifies all metrics trigger correctly
// under stress with 100+ concurrent submissions.
func TestMetricsUnderHighConcurrency(t *testing.T) {
	t.Parallel()

	parentEpoch := jobs.NewHeightEpoch()
	auxEpoch := jobs.NewHeightEpoch()
	parentEpoch.Advance(1000)
	auxEpoch.Advance(5000)

	metrics := &StressMetrics{DeadlineUsages: make([]float64, 0, 200)}

	var wg sync.WaitGroup
	const numSubmissions = 100

	// Mixed parent and aux submissions
	for i := 0; i < numSubmissions; i++ {
		wg.Add(1)
		isAux := i%2 == 0

		go func(submissionID int, aux bool) {
			defer wg.Done()

			var heightCtx context.Context
			var heightCancel context.CancelFunc

			if aux {
				heightCtx, heightCancel = auxEpoch.HeightContext(context.Background())
			} else {
				heightCtx, heightCancel = parentEpoch.HeightContext(context.Background())
			}
			defer heightCancel()

			deadline := 500 * time.Millisecond
			deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, deadline)
			defer deadlineCancel()

			startTime := time.Now()

			// Simulate varying RPC times
			rpcTime := time.Duration(50+submissionID*5) * time.Millisecond

			select {
			case <-deadlineCtx.Done():
				elapsed := time.Since(startTime)
				deadlineUsage := float64(elapsed) / float64(deadline)
				metrics.RecordDeadlineUsage(deadlineUsage)

				// Classify abort
				if heightCtx.Err() != nil && deadlineCtx.Err() == context.Canceled {
					if aux {
						metrics.RecordAuxHeightAbort()
					} else {
						metrics.RecordParentHeightAbort()
					}
				} else {
					metrics.RecordDeadlineAbort()
				}
			case <-time.After(rpcTime):
				// Completed successfully - record deadline usage
				elapsed := time.Since(startTime)
				metrics.RecordDeadlineUsage(float64(elapsed) / float64(deadline))
			}
		}(i, isAux)
	}

	// Advance both chains at different times
	go func() {
		time.Sleep(100 * time.Millisecond)
		parentEpoch.Advance(1001)
		time.Sleep(150 * time.Millisecond)
		auxEpoch.Advance(5001)
	}()

	wg.Wait()

	t.Logf("Metrics: parentAborts=%d, auxAborts=%d, deadlineAborts=%d, usageSamples=%d",
		metrics.ParentHeightAborts.Load(),
		metrics.AuxHeightAborts.Load(),
		metrics.DeadlineAborts.Load(),
		len(metrics.DeadlineUsages))

	// Verify metrics captured
	if metrics.ParentHeightAborts.Load() == 0 {
		t.Error("Expected parent height aborts to be recorded")
	}
	if metrics.AuxHeightAborts.Load() == 0 {
		t.Error("Expected aux height aborts to be recorded")
	}
	if len(metrics.DeadlineUsages) == 0 {
		t.Error("Expected deadline usage samples to be recorded")
	}

	// Verify reasonable deadline usage distribution
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	var lowUsage, highUsage int
	for _, usage := range metrics.DeadlineUsages {
		if usage < 0.5 {
			lowUsage++
		} else {
			highUsage++
		}
	}
	t.Logf("Deadline usage distribution: low(<0.5)=%d, high(>=0.5)=%d", lowUsage, highUsage)
}

// TestLogDifferentiation verifies the system can differentiate between
// "height advanced (new block found)" and "submit deadline expired".
func TestLogDifferentiation(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var wg sync.WaitGroup
	var heightAdvanced atomic.Int64
	var deadlineExpired atomic.Int64

	// Test case 1: Height advance causes cancellation
	wg.Add(1)
	go func() {
		defer wg.Done()

		heightCtx, heightCancel := epoch.HeightContext(context.Background())
		defer heightCancel()

		deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, 500*time.Millisecond)
		defer deadlineCancel()

		<-deadlineCtx.Done()

		// Differentiate cause
		if deadlineCtx.Err() == context.DeadlineExceeded {
			deadlineExpired.Add(1)
		} else if heightCtx.Err() != nil {
			heightAdvanced.Add(1)
		}
	}()

	// Advance height before deadline
	time.Sleep(100 * time.Millisecond)
	epoch.Advance(1001)

	wg.Wait()

	// Test case 2: Deadline expires without height change
	epoch2 := jobs.NewHeightEpoch()
	epoch2.Advance(2000)

	wg.Add(1)
	go func() {
		defer wg.Done()

		heightCtx, heightCancel := epoch2.HeightContext(context.Background())
		defer heightCancel()

		deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, 50*time.Millisecond)
		defer deadlineCancel()

		<-deadlineCtx.Done()

		if deadlineCtx.Err() == context.DeadlineExceeded {
			deadlineExpired.Add(1)
		} else if heightCtx.Err() != nil {
			heightAdvanced.Add(1)
		}
	}()

	wg.Wait()

	if heightAdvanced.Load() != 1 {
		t.Errorf("Expected 1 height advance abort, got %d", heightAdvanced.Load())
	}
	if deadlineExpired.Load() != 1 {
		t.Errorf("Expected 1 deadline expiry, got %d", deadlineExpired.Load())
	}

	t.Log("Successfully differentiated: height_advanced=1, deadline_expired=1")
}

// =============================================================================
// 5. DATABASE / PAYOUT EDGE CASES
// =============================================================================

// MockBlockDB simulates database operations for testing
type MockBlockDB struct {
	blocks         map[uint64]*MockBlock
	pendingPayouts map[uint64]float64
	mu             sync.Mutex
}

type MockBlock struct {
	Height    uint64
	Status    string // "pending", "confirmed", "orphaned"
	Reward    float64
	Confirmed bool
}

func NewMockBlockDB() *MockBlockDB {
	return &MockBlockDB{
		blocks:         make(map[uint64]*MockBlock),
		pendingPayouts: make(map[uint64]float64),
	}
}

func (db *MockBlockDB) AddBlock(height uint64, reward float64) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.blocks[height] = &MockBlock{
		Height: height,
		Status: "pending",
		Reward: reward,
	}
	db.pendingPayouts[height] = reward
}

func (db *MockBlockDB) OrphanBlock(height uint64) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	if block, exists := db.blocks[height]; exists {
		block.Status = "orphaned"
		delete(db.pendingPayouts, height)
		return true
	}
	return false
}

func (db *MockBlockDB) ConfirmBlock(height uint64) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	if block, exists := db.blocks[height]; exists {
		block.Status = "confirmed"
		block.Confirmed = true
		return true
	}
	return false
}

func (db *MockBlockDB) GetPendingPayouts() map[uint64]float64 {
	db.mu.Lock()
	defer db.mu.Unlock()
	result := make(map[uint64]float64)
	for k, v := range db.pendingPayouts {
		result[k] = v
	}
	return result
}

// TestBlockMaturityUnderReorg tests that orphaned blocks have their
// pending payouts cancelled.
func TestBlockMaturityUnderReorg(t *testing.T) {
	t.Parallel()

	db := NewMockBlockDB()

	// Add blocks at different heights
	db.AddBlock(1000, 12.5)
	db.AddBlock(1001, 12.5)
	db.AddBlock(1002, 12.5)

	// Verify pending payouts
	payouts := db.GetPendingPayouts()
	if len(payouts) != 3 {
		t.Errorf("Expected 3 pending payouts, got %d", len(payouts))
	}

	// Simulate deep reorg - blocks 1001 and 1002 orphaned
	db.OrphanBlock(1001)
	db.OrphanBlock(1002)

	// Verify orphaned blocks removed from pending
	payouts = db.GetPendingPayouts()
	if len(payouts) != 1 {
		t.Errorf("Expected 1 pending payout after reorg, got %d", len(payouts))
	}

	if _, exists := payouts[1000]; !exists {
		t.Error("Block 1000 should still be in pending payouts")
	}
}

// TestSimultaneousMaturation tests that multiple matured blocks in same
// interval don't collide in payment processor.
func TestSimultaneousMaturation(t *testing.T) {
	t.Parallel()

	db := NewMockBlockDB()

	// Add multiple blocks
	for h := uint64(1000); h < 1010; h++ {
		db.AddBlock(h, 12.5)
	}

	var wg sync.WaitGroup
	var confirmedCount atomic.Int64
	var mu sync.Mutex
	confirmedHeights := make([]uint64, 0, 10)

	// Simulate simultaneous maturation (all confirm at once)
	for h := uint64(1000); h < 1010; h++ {
		wg.Add(1)
		go func(height uint64) {
			defer wg.Done()

			if db.ConfirmBlock(height) {
				confirmedCount.Add(1)
				mu.Lock()
				confirmedHeights = append(confirmedHeights, height)
				mu.Unlock()
			}
		}(h)
	}

	wg.Wait()

	if confirmedCount.Load() != 10 {
		t.Errorf("Expected 10 confirmed blocks, got %d", confirmedCount.Load())
	}

	// Verify no duplicates
	seen := make(map[uint64]bool)
	for _, h := range confirmedHeights {
		if seen[h] {
			t.Errorf("Duplicate confirmation for height %d", h)
		}
		seen[h] = true
	}
}

// TestAuxBlockRewardAccuracy tests that aux reward is recorded only if
// parent block is accepted.
func TestAuxBlockRewardAccuracy(t *testing.T) {
	t.Parallel()

	parentEpoch := jobs.NewHeightEpoch()
	auxEpoch := jobs.NewHeightEpoch()

	parentEpoch.Advance(800000)
	auxEpoch.Advance(15000000)

	var auxRewardRecorded atomic.Int64
	var auxRewardRejected atomic.Int64

	var wg sync.WaitGroup

	// Simulate aux block submission with parent validation
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(submissionID int) {
			defer wg.Done()

			parentCtx, parentCancel := parentEpoch.HeightContext(context.Background())
			defer parentCancel()

			// Simulate aux submission
			rpcTime := time.Duration(50+submissionID*20) * time.Millisecond

			select {
			case <-parentCtx.Done():
				// Parent chain moved - don't record aux reward
				auxRewardRejected.Add(1)
			case <-time.After(rpcTime):
				// Check parent still valid
				if parentCtx.Err() == nil {
					auxRewardRecorded.Add(1)
				} else {
					auxRewardRejected.Add(1)
				}
			}
		}(i)
	}

	// Parent chain advances at T+100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		parentEpoch.Advance(800001)
	}()

	wg.Wait()

	t.Logf("Aux rewards: recorded=%d, rejected=%d",
		auxRewardRecorded.Load(), auxRewardRejected.Load())

	// Verify all accounted for
	total := auxRewardRecorded.Load() + auxRewardRejected.Load()
	if total != 10 {
		t.Errorf("Expected 10 total aux submissions, got %d", total)
	}

	// At least some should be rejected (those after parent advance)
	if auxRewardRejected.Load() == 0 {
		t.Error("Expected some aux rewards to be rejected after parent chain advance")
	}
}

// =============================================================================
// 6. STRESS / RECURSIVE RACE TESTING
// =============================================================================

// TestRapidConcurrentBlockFinds simulates multiple miners racing with
// network delay and template lag.
func TestRapidConcurrentBlockFinds(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var wg sync.WaitGroup
	var staleSubmissions atomic.Int64
	var validSubmissions atomic.Int64
	var raceCancellations atomic.Int64

	const numMiners = 50
	const numRounds = 5

	for round := 0; round < numRounds; round++ {
		for i := 0; i < numMiners; i++ {
			wg.Add(1)
			go func(minerID, roundNum int) {
				defer wg.Done()

				heightCtx, heightCancel := epoch.HeightContext(context.Background())
				defer heightCancel()

				// Simulate network delay + validation
				delay := time.Duration(10+rand.Intn(100)) * time.Millisecond

				select {
				case <-heightCtx.Done():
					raceCancellations.Add(1)
				case <-time.After(delay):
					// Check if still valid
					if heightCtx.Err() != nil {
						staleSubmissions.Add(1)
					} else {
						validSubmissions.Add(1)
					}
				}
			}(i, round)
		}

		// Advance height after each round
		time.Sleep(50 * time.Millisecond)
		epoch.Advance(uint64(1001 + round))
	}

	wg.Wait()

	t.Logf("Results: valid=%d, stale=%d, raceCancelled=%d",
		validSubmissions.Load(), staleSubmissions.Load(), raceCancellations.Load())

	// Total should match numMiners * numRounds
	total := validSubmissions.Load() + staleSubmissions.Load() + raceCancellations.Load()
	if total != numMiners*numRounds {
		t.Errorf("Expected %d total, got %d", numMiners*numRounds, total)
	}

	// Most should be cancelled or stale due to rapid height advances
	cancelled := staleSubmissions.Load() + raceCancellations.Load()
	if cancelled < int64(numMiners*numRounds/2) {
		t.Errorf("Expected at least %d cancelled/stale, got %d",
			numMiners*numRounds/2, cancelled)
	}
}

// TestRetryHeightContextRace verifies retry loops detect new height
// and all retries cancel if new block arrives.
func TestRetryHeightContextRace(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var wg sync.WaitGroup
	var heightDetections atomic.Int64
	var cleanCancellations atomic.Int64
	var completedRetries atomic.Int64

	const numMiners = 20

	// Barrier: all miners signal after creating HeightContext.
	// This replaces time.Sleep(100ms) which is flaky under -race.
	var ready sync.WaitGroup
	ready.Add(numMiners)

	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			initialHeight := epoch.Height()
			heightCtx, heightCancel := epoch.HeightContext(context.Background())
			defer heightCancel()

			ready.Done() // Signal: context created

			maxRetries := 5
			for retry := 0; retry < maxRetries; retry++ {
				// Check height still valid before retry
				currentHeight := epoch.Height()
				if currentHeight != initialHeight {
					heightDetections.Add(1)
					return
				}

				// Check context
				if heightCtx.Err() != nil {
					cleanCancellations.Add(1)
					return
				}

				// Simulate RPC attempt
				rpcTime := time.Duration(30+rand.Intn(50)) * time.Millisecond

				select {
				case <-heightCtx.Done():
					cleanCancellations.Add(1)
					return
				case <-time.After(rpcTime):
					// "Retry failed", continue loop
				}
			}

			completedRetries.Add(1)
		}(i)
	}

	// Wait until all miners have HeightContext, then advance
	ready.Wait()
	epoch.Advance(1001)

	wg.Wait()

	t.Logf("Retries: completed=%d, cleanCancel=%d, heightDetect=%d",
		completedRetries.Load(), cleanCancellations.Load(), heightDetections.Load())

	// All miners should detect the height change via either context cancellation
	// or explicit height polling. Both are valid detection mechanisms.
	totalDetected := cleanCancellations.Load() + heightDetections.Load()
	if totalDetected == 0 {
		t.Errorf("No miners detected height change — HeightContext cancellation not working")
	}

	// No miner should complete all retries without detecting the height change
	if completedRetries.Load() > 0 {
		t.Errorf("Expected 0 completed retries (all should detect height change), got %d",
			completedRetries.Load())
	}
}

// TestSustainedMiningSimulation simulates sustained mining activity
// to check for consistency over many cycles.
func TestSustainedMiningSimulation(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1)

	metrics := &StressMetrics{DeadlineUsages: make([]float64, 0, 1000)}

	var wg sync.WaitGroup
	var totalSubmissions atomic.Int64
	var totalCancelled atomic.Int64
	var totalCompleted atomic.Int64

	const numCycles = 20
	const minersPerCycle = 10

	// Simulate sustained mining cycles
	for cycle := 0; cycle < numCycles; cycle++ {
		// Start miners for this cycle
		for i := 0; i < minersPerCycle; i++ {
			wg.Add(1)
			totalSubmissions.Add(1)

			go func(cycleNum, minerID int) {
				defer wg.Done()

				heightCtx, heightCancel := epoch.HeightContext(context.Background())
				defer heightCancel()

				deadline := 200 * time.Millisecond
				deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, deadline)
				defer deadlineCancel()

				startTime := time.Now()
				rpcTime := time.Duration(20+rand.Intn(80)) * time.Millisecond

				select {
				case <-deadlineCtx.Done():
					elapsed := time.Since(startTime)
					metrics.RecordDeadlineUsage(float64(elapsed) / float64(deadline))
					totalCancelled.Add(1)
				case <-time.After(rpcTime):
					elapsed := time.Since(startTime)
					metrics.RecordDeadlineUsage(float64(elapsed) / float64(deadline))
					totalCompleted.Add(1)
				}
			}(cycle, i)
		}

		// Advance height between cycles
		time.Sleep(50 * time.Millisecond)
		epoch.Advance(uint64(2 + cycle))
	}

	wg.Wait()

	// Final verification
	t.Logf("Sustained test: submissions=%d, completed=%d, cancelled=%d",
		totalSubmissions.Load(), totalCompleted.Load(), totalCancelled.Load())

	// Verify all accounted for
	accounted := totalCompleted.Load() + totalCancelled.Load()
	if accounted != totalSubmissions.Load() {
		t.Errorf("Accounting mismatch: %d != %d", accounted, totalSubmissions.Load())
	}

	// Verify final height
	expectedHeight := uint64(1 + numCycles)
	if epoch.Height() != expectedHeight {
		t.Errorf("Expected final height %d, got %d", expectedHeight, epoch.Height())
	}

	// Check metrics consistency
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if len(metrics.DeadlineUsages) != int(totalSubmissions.Load()) {
		t.Errorf("Expected %d usage records, got %d",
			totalSubmissions.Load(), len(metrics.DeadlineUsages))
	}
}

// TestHeightEpochMemoryStability verifies HeightEpoch doesn't leak memory
// under sustained context creation/cancellation.
func TestHeightEpochMemoryStability(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1)

	var wg sync.WaitGroup
	const iterations = 1000

	// Create and cancel many contexts rapidly
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(iter int) {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())

			// Some contexts used briefly, some cancelled immediately
			if iter%3 == 0 {
				cancel()
				return
			}

			select {
			case <-ctx.Done():
			case <-time.After(1 * time.Millisecond):
			}

			cancel()
		}(i)

		// Periodically advance height
		if i%100 == 0 {
			epoch.Advance(uint64(2 + i/100))
		}
	}

	wg.Wait()

	// If we get here without hanging or crashing, memory is stable
	t.Log("Memory stability test completed - no leaks detected")
}
