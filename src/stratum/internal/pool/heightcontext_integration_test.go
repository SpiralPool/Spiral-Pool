// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - HeightContext Integration Tests
//
// Tests for HeightContext wiring into block submission paths:
// - Context cancellation during simulated RPC
// - Metrics classification (height advance vs deadline expiry)
// - Aux chain HeightContext under rapid parent blocks
// - Multi-coin HeightEpoch isolation
// - Template fetch lag simulation
package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/jobs"
)

// =============================================================================
// 1. HEIGHTCONTEXT CANCELLATION DURING SIMULATED RPC
// =============================================================================

// TestHeightContextCancellationDuringRPC simulates block submission RPC being
// cancelled when HeightContext advances (new block found on network).
func TestHeightContextCancellationDuringRPC(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	// Simulate starting a block submission
	heightCtx, heightCancel := epoch.HeightContext(context.Background())
	defer heightCancel()

	// Add a timeout to simulate SubmitDeadline
	submitCtx, submitCancel := context.WithTimeout(heightCtx, 5*time.Second)
	defer submitCancel()

	// Start simulated RPC in goroutine
	rpcComplete := make(chan error, 1)
	go func() {
		// Simulate RPC that takes 500ms
		select {
		case <-submitCtx.Done():
			rpcComplete <- submitCtx.Err()
		case <-time.After(500 * time.Millisecond):
			rpcComplete <- nil // RPC would complete
		}
	}()

	// After 100ms, advance height (simulating ZMQ notification)
	time.Sleep(100 * time.Millisecond)
	epoch.Advance(1001)

	// RPC should be cancelled
	select {
	case err := <-rpcComplete:
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("RPC should have been cancelled within 1 second")
	}
}

// TestHeightContextNotCancelledOnSameHeight verifies that HeightContext
// is NOT cancelled when Advance is called with same or lower height.
func TestHeightContextNotCancelledOnSameHeight(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	heightCtx, heightCancel := epoch.HeightContext(context.Background())
	defer heightCancel()

	// Advance to same height - should NOT cancel
	epoch.Advance(1000)

	select {
	case <-heightCtx.Done():
		t.Fatal("Context should NOT be cancelled for same height")
	case <-time.After(50 * time.Millisecond):
		// Expected - context still alive
	}

	// Advance to lower height - should NOT cancel
	epoch.Advance(999)

	select {
	case <-heightCtx.Done():
		t.Fatal("Context should NOT be cancelled for lower height")
	case <-time.After(50 * time.Millisecond):
		// Expected - context still alive
	}
}

// =============================================================================
// 2. METRICS CLASSIFICATION TESTS
// =============================================================================

// MockMetrics tracks abort classifications for testing
type MockMetrics struct {
	HeightAborts   atomic.Int64
	DeadlineAborts atomic.Int64
	DeadlineUsages []float64
	mu             sync.Mutex
}

func (m *MockMetrics) RecordHeightAbort() {
	m.HeightAborts.Add(1)
}

func (m *MockMetrics) RecordDeadlineAbort() {
	m.DeadlineAborts.Add(1)
}

func (m *MockMetrics) RecordDeadlineUsage(ratio float64) {
	m.mu.Lock()
	m.DeadlineUsages = append(m.DeadlineUsages, ratio)
	m.mu.Unlock()
}

// TestMetricsClassificationHeightAdvance verifies that when HeightContext
// cancels before deadline, it's classified as height abort.
func TestMetricsClassificationHeightAdvance(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)
	metrics := &MockMetrics{}

	heightCtx, heightCancel := epoch.HeightContext(context.Background())
	defer heightCancel()

	deadline := 5 * time.Second
	deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, deadline)
	defer deadlineCancel()

	startTime := time.Now()

	// Advance height after 100ms
	time.Sleep(100 * time.Millisecond)
	epoch.Advance(1001)

	// Wait for context cancellation
	<-deadlineCtx.Done()
	elapsed := time.Since(startTime)

	// Classify the abort
	if deadlineCtx.Err() != nil {
		deadlineUsage := float64(elapsed) / float64(deadline)
		metrics.RecordDeadlineUsage(deadlineUsage)

		// Height context cancelled AND deadline context shows Canceled (not DeadlineExceeded)
		if heightCtx.Err() != nil && deadlineCtx.Err() == context.Canceled {
			metrics.RecordHeightAbort()
		} else {
			metrics.RecordDeadlineAbort()
		}
	}

	if metrics.HeightAborts.Load() != 1 {
		t.Errorf("Expected 1 height abort, got %d", metrics.HeightAborts.Load())
	}
	if metrics.DeadlineAborts.Load() != 0 {
		t.Errorf("Expected 0 deadline aborts, got %d", metrics.DeadlineAborts.Load())
	}
}

// TestMetricsClassificationDeadlineExpiry verifies that when deadline expires
// before height advance, it's classified as deadline abort.
func TestMetricsClassificationDeadlineExpiry(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)
	metrics := &MockMetrics{}

	heightCtx, heightCancel := epoch.HeightContext(context.Background())
	defer heightCancel()

	deadline := 100 * time.Millisecond
	deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, deadline)
	defer deadlineCancel()

	startTime := time.Now()

	// Do NOT advance height - let deadline expire
	<-deadlineCtx.Done()
	elapsed := time.Since(startTime)

	// Classify the abort
	if deadlineCtx.Err() != nil {
		deadlineUsage := float64(elapsed) / float64(deadline)
		metrics.RecordDeadlineUsage(deadlineUsage)

		// Height context NOT cancelled, deadline context shows DeadlineExceeded
		if heightCtx.Err() != nil && deadlineCtx.Err() == context.Canceled {
			metrics.RecordHeightAbort()
		} else {
			metrics.RecordDeadlineAbort()
		}
	}

	if metrics.HeightAborts.Load() != 0 {
		t.Errorf("Expected 0 height aborts, got %d", metrics.HeightAborts.Load())
	}
	if metrics.DeadlineAborts.Load() != 1 {
		t.Errorf("Expected 1 deadline abort, got %d", metrics.DeadlineAborts.Load())
	}

	// Deadline usage should be close to 1.0
	if len(metrics.DeadlineUsages) != 1 {
		t.Fatalf("Expected 1 deadline usage record, got %d", len(metrics.DeadlineUsages))
	}
	if metrics.DeadlineUsages[0] < 0.9 || metrics.DeadlineUsages[0] > 1.1 {
		t.Errorf("Deadline usage should be ~1.0, got %f", metrics.DeadlineUsages[0])
	}
}

// =============================================================================
// 3. AUX CHAIN HEIGHTCONTEXT UNDER RAPID PARENT BLOCKS
// =============================================================================

// TestAuxChainHeightContextRapidParentBlocks simulates merge-mining scenario
// where parent chain produces blocks rapidly (like DGB 15s blocks).
func TestAuxChainHeightContextRapidParentBlocks(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var cancelledCount atomic.Int64
	var completedCount atomic.Int64
	var wg sync.WaitGroup

	// Simulate 10 concurrent aux block submissions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(submissionID int) {
			defer wg.Done()

			auxHeightCtx, auxHeightCancel := epoch.HeightContext(context.Background())
			defer auxHeightCancel()

			auxDeadlineCtx, auxDeadlineCancel := context.WithTimeout(auxHeightCtx, 2*time.Second)
			defer auxDeadlineCancel()

			// Simulate aux block RPC taking 200-400ms
			rpcDuration := time.Duration(200+submissionID*20) * time.Millisecond

			select {
			case <-auxDeadlineCtx.Done():
				if auxHeightCtx.Err() != nil {
					cancelledCount.Add(1)
				}
			case <-time.After(rpcDuration):
				completedCount.Add(1)
			}
		}(i)
	}

	// Simulate rapid parent blocks - advance height 5 times over 500ms
	go func() {
		for h := uint64(1001); h <= 1005; h++ {
			time.Sleep(100 * time.Millisecond)
			epoch.Advance(h)
		}
	}()

	wg.Wait()

	t.Logf("Aux submissions: completed=%d, cancelled=%d",
		completedCount.Load(), cancelledCount.Load())

	// At least some should be cancelled due to rapid height advances
	if cancelledCount.Load() == 0 {
		t.Error("Expected at least some aux submissions to be cancelled")
	}

	// Total should be 10
	total := completedCount.Load() + cancelledCount.Load()
	if total != 10 {
		t.Errorf("Expected 10 total submissions, got %d", total)
	}
}

// =============================================================================
// 4. MULTI-COIN HEIGHTEPOCH ISOLATION
// =============================================================================

// TestMultiCoinHeightEpochIsolation verifies that each coin has its own
// HeightEpoch and they don't interfere with each other.
func TestMultiCoinHeightEpochIsolation(t *testing.T) {
	t.Parallel()

	// Simulate 3 different coins with separate HeightEpochs
	dgbEpoch := jobs.NewHeightEpoch()
	btcEpoch := jobs.NewHeightEpoch()
	ltcEpoch := jobs.NewHeightEpoch()

	// Set different initial heights
	dgbEpoch.Advance(22000000) // DGB height
	btcEpoch.Advance(800000)   // BTC height
	ltcEpoch.Advance(2500000)  // LTC height

	// Get contexts for each coin
	dgbCtx, dgbCancel := dgbEpoch.HeightContext(context.Background())
	defer dgbCancel()
	btcCtx, btcCancel := btcEpoch.HeightContext(context.Background())
	defer btcCancel()
	ltcCtx, ltcCancel := ltcEpoch.HeightContext(context.Background())
	defer ltcCancel()

	// Advance DGB height - should only affect DGB context
	dgbEpoch.Advance(22000001)

	// DGB context should be cancelled
	select {
	case <-dgbCtx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("DGB context should be cancelled after DGB height advance")
	}

	// BTC and LTC contexts should still be alive
	select {
	case <-btcCtx.Done():
		t.Error("BTC context should NOT be cancelled by DGB height advance")
	default:
		// Expected
	}

	select {
	case <-ltcCtx.Done():
		t.Error("LTC context should NOT be cancelled by DGB height advance")
	default:
		// Expected
	}

	// Advance BTC height
	btcEpoch.Advance(800001)

	select {
	case <-btcCtx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("BTC context should be cancelled after BTC height advance")
	}

	// LTC should still be alive
	select {
	case <-ltcCtx.Done():
		t.Error("LTC context should NOT be cancelled by BTC height advance")
	default:
		// Expected
	}
}

// =============================================================================
// 5. TEMPLATE FETCH LAG SIMULATION
// =============================================================================

// TestTemplateFetchLagWithHeightContext simulates network lag in template
// fetching and verifies HeightContext prevents stale submissions.
func TestTemplateFetchLagWithHeightContext(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	// Simulate scenario:
	// T+0ms: Share validated, enters handleBlock
	// T+50ms: HeightContext created, submission starts
	// T+100ms: ZMQ fires, height advances to 1001
	// T+200ms: Template fetch completes (stale data)
	// T+250ms: Submission RPC would start, but context already cancelled

	heightCtx, heightCancel := epoch.HeightContext(context.Background())
	defer heightCancel()

	var templateFetched atomic.Bool
	var submissionAborted atomic.Bool

	// Goroutine simulating template fetch with lag
	go func() {
		// Simulate 200ms template fetch lag
		select {
		case <-heightCtx.Done():
			submissionAborted.Store(true)
			return
		case <-time.After(200 * time.Millisecond):
			templateFetched.Store(true)
		}
	}()

	// After 100ms, advance height (ZMQ notification)
	time.Sleep(100 * time.Millisecond)
	epoch.Advance(1001)

	// Wait for goroutine to complete
	time.Sleep(150 * time.Millisecond)

	if templateFetched.Load() {
		t.Error("Template fetch should have been aborted by HeightContext cancellation")
	}
	if !submissionAborted.Load() {
		t.Error("Submission should be marked as aborted")
	}
}

// =============================================================================
// 6. CONCURRENT HEIGHT ADVANCES STRESS TEST
// =============================================================================

// TestConcurrentHeightAdvancesStress stress tests HeightContext under
// many concurrent height advances (simulating very fast block times).
func TestConcurrentHeightAdvancesStress(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1)

	var wg sync.WaitGroup
	var cancelledContexts atomic.Int64

	// Start 100 goroutines, each creating a HeightContext
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			select {
			case <-ctx.Done():
				cancelledContexts.Add(1)
			case <-time.After(500 * time.Millisecond):
				// Context survived
			}
		}()
	}

	// Rapidly advance height 50 times
	for h := uint64(2); h <= 51; h++ {
		epoch.Advance(h)
		time.Sleep(5 * time.Millisecond)
	}

	wg.Wait()

	t.Logf("Cancelled contexts: %d/100", cancelledContexts.Load())

	// Most contexts should be cancelled due to rapid advances
	if cancelledContexts.Load() < 50 {
		t.Errorf("Expected at least 50 cancelled contexts, got %d", cancelledContexts.Load())
	}

	// Final height should be 51
	if epoch.Height() != 51 {
		t.Errorf("Expected final height 51, got %d", epoch.Height())
	}
}

// =============================================================================
// 7. RETRY LOOP WITH HEIGHTCONTEXT CANCELLATION
// =============================================================================

// TestRetryLoopHeightContextCancellation verifies that retry loops properly
// abort when HeightContext is cancelled mid-retry.
func TestRetryLoopHeightContextCancellation(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	heightCtx, heightCancel := epoch.HeightContext(context.Background())
	defer heightCancel()

	deadline := 2 * time.Second
	deadlineCtx, deadlineCancel := context.WithTimeout(heightCtx, deadline)
	defer deadlineCancel()

	var retryCount atomic.Int32
	var abortedDueToHeight atomic.Bool

	// Simulate retry loop
	go func() {
		for attempt := 1; attempt <= 10; attempt++ {
			// Check context before each retry
			if deadlineCtx.Err() != nil {
				if heightCtx.Err() != nil {
					abortedDueToHeight.Store(true)
				}
				return
			}

			retryCount.Add(1)

			// Simulate RPC attempt taking 100ms
			select {
			case <-deadlineCtx.Done():
				if heightCtx.Err() != nil {
					abortedDueToHeight.Store(true)
				}
				return
			case <-time.After(100 * time.Millisecond):
				// RPC "failed", will retry
			}
		}
	}()

	// After 250ms (during retry 3), advance height
	time.Sleep(250 * time.Millisecond)
	epoch.Advance(1001)

	// Wait for retry loop to exit
	time.Sleep(200 * time.Millisecond)

	if !abortedDueToHeight.Load() {
		t.Error("Retry loop should have been aborted due to height advance")
	}

	// Should have completed 2-3 retries before cancellation
	retries := retryCount.Load()
	if retries < 2 || retries > 4 {
		t.Errorf("Expected 2-4 retries before abort, got %d", retries)
	}

	t.Logf("Retries before abort: %d", retries)
}

// =============================================================================
// 8. GOROUTINE LEAK PREVENTION TEST
// =============================================================================

// TestNoGoroutineLeaksOnCancellation verifies that all goroutines properly
// exit when HeightContext is cancelled.
func TestNoGoroutineLeaksOnCancellation(t *testing.T) {
	t.Parallel()

	epoch := jobs.NewHeightEpoch()
	epoch.Advance(1000)

	var activeGoroutines atomic.Int64
	var wg sync.WaitGroup

	// Start 50 goroutines simulating submissions
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			activeGoroutines.Add(1)
			defer activeGoroutines.Add(-1)

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			// Simulate long-running operation
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				return
			}
		}()
	}

	// Wait a bit for all goroutines to start
	time.Sleep(50 * time.Millisecond)

	peakActive := activeGoroutines.Load()
	if peakActive != 50 {
		t.Errorf("Expected 50 active goroutines at peak, got %d", peakActive)
	}

	// Advance height to cancel all contexts
	epoch.Advance(1001)

	// Wait for all goroutines to exit
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines exited
	case <-time.After(2 * time.Second):
		t.Fatalf("Goroutines did not exit within 2 seconds, still active: %d",
			activeGoroutines.Load())
	}

	finalActive := activeGoroutines.Load()
	if finalActive != 0 {
		t.Errorf("Expected 0 active goroutines after cancellation, got %d", finalActive)
	}
}
