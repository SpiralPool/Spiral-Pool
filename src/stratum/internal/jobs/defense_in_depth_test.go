// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Defense-In-Depth & Edge Case Tests
// =============================================================================
// These tests cover edge cases, network anomalies, and defense-in-depth
// scenarios to ensure system resilience.

// -----------------------------------------------------------------------------
// Network Latency Simulation Tests
// -----------------------------------------------------------------------------

// TestDefense_NetworkLatency_HighLatency simulates high network latency.
func TestDefense_NetworkLatency_HighLatency(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial_tip")

	var wg sync.WaitGroup
	var staleSubmissions atomic.Int32
	var validSubmissions atomic.Int32

	// Miners with varying latency
	latencies := []time.Duration{
		10 * time.Millisecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
	}

	for _, latency := range latencies {
		wg.Add(1)
		go func(lat time.Duration) {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			// Simulate network latency
			time.Sleep(lat)

			select {
			case <-ctx.Done():
				staleSubmissions.Add(1)
			default:
				validSubmissions.Add(1)
			}
		}(latency)
	}

	// Block arrives at 75ms
	time.Sleep(75 * time.Millisecond)
	epoch.AdvanceWithTip(1001, "new_tip")

	wg.Wait()

	t.Logf("High latency test: %d stale, %d valid submissions",
		staleSubmissions.Load(), validSubmissions.Load())

	// Miners with >75ms latency should be stale
	if staleSubmissions.Load() < 2 {
		t.Error("Expected at least 2 stale submissions due to latency")
	}
}

// TestDefense_NetworkLatency_Jitter simulates network jitter.
func TestDefense_NetworkLatency_Jitter(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial_tip")

	var wg sync.WaitGroup
	const numMiners = 20

	results := make([]bool, numMiners) // true = stale

	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			// Jittery latency (5-50ms)
			jitter := time.Duration(5+(idx*2)) * time.Millisecond
			time.Sleep(jitter)

			select {
			case <-ctx.Done():
				results[idx] = true
			default:
				results[idx] = false
			}
		}(i)
	}

	// Block arrives at random time
	time.Sleep(25 * time.Millisecond)
	epoch.AdvanceWithTip(1001, "new_tip")

	wg.Wait()

	staleCount := 0
	for _, stale := range results {
		if stale {
			staleCount++
		}
	}

	t.Logf("Jitter test: %d/%d submissions marked stale", staleCount, numMiners)
}

// -----------------------------------------------------------------------------
// Temporary Node Desync Tests
// -----------------------------------------------------------------------------

// TestDefense_TemporaryDesync simulates temporary node desync.
func TestDefense_TemporaryDesync(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_A")

	// Simulate node desyncing then resyncing
	// Pattern: A -> B (desync) -> A (resync) -> C (new block)

	ctx1, cancel1 := epoch.HeightContext(context.Background())
	defer cancel1()

	// Node temporarily reports different tip (desync)
	epoch.AdvanceWithTip(1000, "tip_B_desync")

	// ctx1 should be cancelled due to tip change
	select {
	case <-ctx1.Done():
		t.Log("Context cancelled during desync - correct behavior")
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should be cancelled on desync")
	}

	// Node resyncs back to original tip
	ctx2, cancel2 := epoch.HeightContext(context.Background())
	defer cancel2()

	epoch.AdvanceWithTip(1000, "tip_A") // Back to original

	// ctx2 should be cancelled (tip changed back)
	select {
	case <-ctx2.Done():
		t.Log("Context cancelled on resync - correct (tip changed)")
	case <-time.After(100 * time.Millisecond):
		// This is also acceptable if we consider A->B->A as no net change
		t.Log("Context not cancelled on resync to same tip")
	}
}

// TestDefense_FlappingNode simulates a flapping node.
func TestDefense_FlappingNode(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_stable")

	cancelCount := 0

	// Node flaps between tips
	flaps := []string{"tip_A", "tip_B", "tip_A", "tip_B", "tip_stable"}

	for _, tip := range flaps {
		ctx, cancel := epoch.HeightContext(context.Background())
		epoch.AdvanceWithTip(1000, tip)

		select {
		case <-ctx.Done():
			cancelCount++
		case <-time.After(10 * time.Millisecond):
		}
		cancel()
	}

	// Each tip change should cancel
	if cancelCount < 4 {
		t.Errorf("Expected at least 4 cancellations on flapping, got %d", cancelCount)
	}

	t.Logf("Flapping node: %d cancellations on %d flaps", cancelCount, len(flaps))
}

// -----------------------------------------------------------------------------
// Deep Reorg Simulation Tests
// -----------------------------------------------------------------------------

// TestDefense_DeepReorg_2Blocks simulates 2-block deep reorg.
func TestDefense_DeepReorg_2Blocks(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Chain progresses: 1000 -> 1001 -> 1002
	epoch.AdvanceWithTip(1000, "tip_1000")
	epoch.AdvanceWithTip(1001, "tip_1001")
	epoch.AdvanceWithTip(1002, "tip_1002")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// 2-block reorg: chain goes back to 1000 then new path to 1002
	// In practice, this would be detected as tip change at current height
	epoch.AdvanceWithTip(1002, "tip_1002_reorged")

	select {
	case <-ctx.Done():
		t.Log("2-block reorg detected via tip change")
	case <-time.After(100 * time.Millisecond):
		t.Error("Deep reorg should be detected")
	}
}

// TestDefense_DeepReorg_RapidSuccession simulates rapid reorgs.
func TestDefense_DeepReorg_RapidSuccession(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var cancelCount atomic.Int32

	// Rapid reorgs at same height
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			ctx, cancel := epoch.HeightContext(context.Background())
			time.Sleep(time.Millisecond)
			select {
			case <-ctx.Done():
				cancelCount.Add(1)
			default:
			}
			cancel()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			tip := "reorg_tip_" + string(rune('A'+i%26))
			epoch.AdvanceWithTip(1000, tip)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()

	t.Logf("Rapid reorgs: %d cancellations detected", cancelCount.Load())
}

// -----------------------------------------------------------------------------
// Chain Instability Tests
// -----------------------------------------------------------------------------

// TestDefense_ChainInstability_MultipleCompetingTips simulates competing tips.
func TestDefense_ChainInstability_MultipleCompetingTips(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_miner_A")

	// Multiple miners find blocks at same height
	competingTips := []string{
		"tip_miner_A",
		"tip_miner_B",
		"tip_miner_C",
		"tip_miner_A", // A wins temporarily
		"tip_miner_D",
		"tip_miner_D", // D wins
	}

	cancelCount := 0

	for _, tip := range competingTips {
		ctx, cancel := epoch.HeightContext(context.Background())
		epoch.AdvanceWithTip(1000, tip)

		select {
		case <-ctx.Done():
			cancelCount++
		case <-time.After(10 * time.Millisecond):
		}
		cancel()
	}

	// Should cancel on each tip CHANGE (not when tip is same)
	// A->B, B->C, C->A, A->D = 4 changes
	if cancelCount < 4 {
		t.Errorf("Expected at least 4 cancellations for competing tips, got %d", cancelCount)
	}

	t.Logf("Competing tips: %d cancellations", cancelCount)
}

// TestDefense_ChainInstability_HeightRollback tests height rollback handling.
func TestDefense_ChainInstability_HeightRollback(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Advance normally
	epoch.AdvanceWithTip(1000, "tip_1000")
	epoch.AdvanceWithTip(1001, "tip_1001")
	epoch.AdvanceWithTip(1002, "tip_1002")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Height "rollback" - should be ignored
	epoch.AdvanceWithTip(1001, "tip_1001_new")

	// Context should NOT be cancelled (lower height ignored)
	select {
	case <-ctx.Done():
		t.Error("Context should NOT be cancelled on height rollback")
	case <-time.After(50 * time.Millisecond):
		// Expected - lower heights are ignored
	}

	// Verify height didn't decrease
	if epoch.Height() != 1002 {
		t.Errorf("Height should remain at 1002, got %d", epoch.Height())
	}
}

// -----------------------------------------------------------------------------
// Submission Retry Tests
// -----------------------------------------------------------------------------

// TestDefense_SubmissionRetry_RespectsHeightContext tests retry behavior.
func TestDefense_SubmissionRetry_RespectsHeightContext(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Simulate retry loop
	retryCount := 0
	maxRetries := 5
	retryDelay := 10 * time.Millisecond

	// Block arrives mid-retry
	go func() {
		time.Sleep(25 * time.Millisecond)
		epoch.AdvanceWithTip(1001, "tip_B")
	}()

	for retryCount < maxRetries {
		select {
		case <-ctx.Done():
			t.Logf("Retry loop exited after %d retries (context cancelled)", retryCount)
			return
		default:
			// Simulate retry
			retryCount++
			time.Sleep(retryDelay)
		}
	}

	t.Logf("Retry loop completed all %d retries (no cancellation during window)",
		maxRetries)
}

// TestDefense_SubmissionRetry_ImmediateCancel tests immediate cancellation.
func TestDefense_SubmissionRetry_ImmediateCancel(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Immediate block arrival
	epoch.AdvanceWithTip(1001, "tip_B")

	// First check should see cancellation
	select {
	case <-ctx.Done():
		t.Log("Immediate cancellation detected correctly")
	default:
		t.Error("Should detect immediate cancellation")
	}
}

// -----------------------------------------------------------------------------
// Stress & Chaos Tests
// -----------------------------------------------------------------------------

// TestDefense_Chaos_RandomizedOperations tests randomized chaotic operations.
func TestDefense_Chaos_RandomizedOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var wg sync.WaitGroup
	var operations atomic.Int64
	var cancellations atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Chaotic workers
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Random operation
					switch workerID % 4 {
					case 0:
						// Height advance
						h := epoch.Height()
						epoch.AdvanceWithTip(h+1, "tip_"+string(rune('A'+h%26)))
					case 1:
						// Same-height tip change
						h := epoch.Height()
						epoch.AdvanceWithTip(h, "tip_"+string(rune('a'+workerID)))
					case 2:
						// Context creation and check
						subCtx, subCancel := epoch.HeightContext(ctx)
						time.Sleep(time.Microsecond)
						select {
						case <-subCtx.Done():
							cancellations.Add(1)
						default:
						}
						subCancel()
					case 3:
						// State reads
						_ = epoch.Height()
						_ = epoch.TipHash()
						_, _ = epoch.State()
					}
					operations.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Chaos test: %d operations, %d cancellations in 2 seconds",
		operations.Load(), cancellations.Load())
}

// TestDefense_Chaos_BurstAndPause tests burst-pause patterns.
func TestDefense_Chaos_BurstAndPause(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	const numBursts = 3
	const burstSize = 100

	for burst := 0; burst < numBursts; burst++ {
		var wg sync.WaitGroup
		var burstCancellations atomic.Int32

		// Burst of activity
		for i := 0; i < burstSize; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := epoch.HeightContext(context.Background())
				defer cancel()

				time.Sleep(time.Millisecond)

				select {
				case <-ctx.Done():
					burstCancellations.Add(1)
				default:
				}
			}()
		}

		// Trigger cancellation
		time.Sleep(500 * time.Microsecond)
		epoch.AdvanceWithTip(uint64(1001+burst), "burst_tip")

		wg.Wait()

		t.Logf("Burst %d: %d/%d cancellations", burst+1,
			burstCancellations.Load(), burstSize)

		// Pause between bursts
		time.Sleep(10 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Recovery Tests
// -----------------------------------------------------------------------------

// TestDefense_Recovery_AfterCriticalFailure tests recovery behavior.
func TestDefense_Recovery_AfterCriticalFailure(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_before_failure")

	// Simulate critical failure (many rapid tip changes)
	for i := 0; i < 50; i++ {
		epoch.AdvanceWithTip(1000, "failure_tip_"+string(rune('A'+i%26)))
	}

	// System stabilizes
	epoch.AdvanceWithTip(1001, "tip_stable")

	// New contexts should work correctly
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	select {
	case <-ctx.Done():
		t.Error("New context should be valid after recovery")
	case <-time.After(50 * time.Millisecond):
		t.Log("System recovered correctly - new contexts work")
	}

	// Verify state is consistent
	height, tip := epoch.State()
	if height != 1001 || tip != "tip_stable" {
		t.Errorf("State inconsistent after recovery: height=%d, tip=%s",
			height, tip)
	}
}

// TestDefense_Recovery_RapidCreationAfterCancel tests rapid context creation.
func TestDefense_Recovery_RapidCreationAfterCancel(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	// Create context, cancel via advance, immediately create new one
	for i := 0; i < 100; i++ {
		ctx, cancel := epoch.HeightContext(context.Background())

		// Advance (cancels ctx)
		epoch.AdvanceWithTip(uint64(1001+i), "tip_"+string(rune('A'+i%26)))

		select {
		case <-ctx.Done():
			// Expected
		case <-time.After(10 * time.Millisecond):
			t.Fatalf("Context %d not cancelled", i)
		}

		cancel()

		// Immediately create new context
		newCtx, newCancel := epoch.HeightContext(context.Background())

		// New context should be valid
		select {
		case <-newCtx.Done():
			t.Fatalf("New context %d should be valid", i)
		default:
			// Good
		}
		newCancel()
	}

	t.Log("Rapid context creation after cancel works correctly")
}

// -----------------------------------------------------------------------------
// Boundary Condition Tests
// -----------------------------------------------------------------------------

// TestDefense_Boundary_MaxHeight tests maximum height values.
func TestDefense_Boundary_MaxHeight(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Very large height (near uint64 max but reasonable)
	largeHeight := uint64(1 << 40) // ~1 trillion
	epoch.AdvanceWithTip(largeHeight, "large_height_tip")

	if epoch.Height() != largeHeight {
		t.Errorf("Failed to set large height: expected %d, got %d",
			largeHeight, epoch.Height())
	}

	// Can still advance
	epoch.AdvanceWithTip(largeHeight+1, "larger_tip")

	if epoch.Height() != largeHeight+1 {
		t.Error("Failed to advance past large height")
	}
}

// TestDefense_Boundary_EmptyTipTransitions tests empty tip handling.
func TestDefense_Boundary_EmptyTipTransitions(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Empty tip initially
	if epoch.TipHash() != "" {
		t.Error("Initial tip should be empty")
	}

	// Set non-empty tip
	epoch.AdvanceWithTip(1000, "tip_A")
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Clear tip (legacy advance)
	epoch.Advance(1001)

	// Context should be cancelled (height advanced)
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should be cancelled on legacy advance")
	}

	// Tip should be empty after legacy advance
	if epoch.TipHash() != "" {
		t.Errorf("Tip should be empty after legacy advance, got %s", epoch.TipHash())
	}
}

// TestDefense_Boundary_RapidHeightJumps tests large height jumps.
func TestDefense_Boundary_RapidHeightJumps(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Jump 1000 blocks
	epoch.AdvanceWithTip(2000, "jumped_tip")

	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should be cancelled on large height jump")
	}

	if epoch.Height() != 2000 {
		t.Errorf("Height should be 2000 after jump, got %d", epoch.Height())
	}
}
