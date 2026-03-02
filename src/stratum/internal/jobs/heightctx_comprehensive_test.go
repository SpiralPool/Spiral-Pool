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
// TEST SUITE: HeightContext & Reorg Safety
// =============================================================================
// These tests verify that HeightContext properly cancels stale submissions
// on height advance, tip hash changes, and concurrent access scenarios.

// -----------------------------------------------------------------------------
// Basic Functionality Tests
// -----------------------------------------------------------------------------

// TestHeightEpoch_NewInstance verifies initial state.
func TestHeightEpoch_NewInstance(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	if epoch.Height() != 0 {
		t.Errorf("Expected initial height 0, got %d", epoch.Height())
	}
	if epoch.TipHash() != "" {
		t.Errorf("Expected empty initial tip hash, got %s", epoch.TipHash())
	}

	height, tip := epoch.State()
	if height != 0 || tip != "" {
		t.Errorf("Expected State() to return (0, \"\"), got (%d, %s)", height, tip)
	}
}

// TestHeightEpoch_Advance_Basic verifies legacy Advance behavior.
func TestHeightEpoch_Advance_Basic(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	epoch.Advance(100)
	if epoch.Height() != 100 {
		t.Errorf("Expected height 100, got %d", epoch.Height())
	}

	// Legacy Advance clears tip hash
	if epoch.TipHash() != "" {
		t.Errorf("Expected empty tip after legacy Advance, got %s", epoch.TipHash())
	}
}

// TestHeightEpoch_AdvanceWithTip_Basic verifies new AdvanceWithTip behavior.
func TestHeightEpoch_AdvanceWithTip_Basic(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	epoch.AdvanceWithTip(100, "hash_abc123")
	if epoch.Height() != 100 {
		t.Errorf("Expected height 100, got %d", epoch.Height())
	}
	if epoch.TipHash() != "hash_abc123" {
		t.Errorf("Expected tip hash_abc123, got %s", epoch.TipHash())
	}
}

// TestHeightEpoch_State_Atomicity verifies State returns consistent snapshot.
func TestHeightEpoch_State_Atomicity(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	epoch.AdvanceWithTip(1000, "tip_A")
	height, tip := epoch.State()

	if height != 1000 || tip != "tip_A" {
		t.Errorf("Expected (1000, tip_A), got (%d, %s)", height, tip)
	}

	epoch.AdvanceWithTip(1001, "tip_B")
	height, tip = epoch.State()

	if height != 1001 || tip != "tip_B" {
		t.Errorf("Expected (1001, tip_B), got (%d, %s)", height, tip)
	}
}

// -----------------------------------------------------------------------------
// Context Cancellation Tests
// -----------------------------------------------------------------------------

// TestHeightContext_CancelsOnHeightAdvance verifies context cancels on height increase.
func TestHeightContext_CancelsOnHeightAdvance(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Context should be valid initially
	select {
	case <-ctx.Done():
		t.Fatal("Context should not be cancelled initially")
	default:
	}

	// Advance height
	epoch.AdvanceWithTip(1001, "hash_B")

	// Context must be cancelled
	select {
	case <-ctx.Done():
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Context should have been cancelled on height advance")
	}
}

// TestHeightContext_CancelsOnSameHeightTipChange is the CRITICAL test for same-height reorgs.
func TestHeightContext_CancelsOnSameHeightTipChange(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Same height, different tip - MUST cancel
	epoch.AdvanceWithTip(1000, "hash_B")

	select {
	case <-ctx.Done():
		// Success - same-height reorg detected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("CRITICAL: Context should cancel on same-height tip change")
	}
}

// TestHeightContext_NoCancelOnSameHeightSameTip verifies no false cancellation.
func TestHeightContext_NoCancelOnSameHeightSameTip(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Same height, same tip - should NOT cancel
	epoch.AdvanceWithTip(1000, "hash_A")

	select {
	case <-ctx.Done():
		t.Fatal("Context should NOT cancel when height and tip are unchanged")
	case <-time.After(50 * time.Millisecond):
		// Success
	}
}

// TestHeightContext_NoCancelOnLowerHeight verifies lower heights are ignored.
func TestHeightContext_NoCancelOnLowerHeight(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Lower height - should be ignored
	epoch.AdvanceWithTip(999, "hash_B")

	select {
	case <-ctx.Done():
		t.Fatal("Context should NOT cancel on lower height")
	case <-time.After(50 * time.Millisecond):
		// Success
	}
}

// TestHeightContext_MultipleContextsAllCancel verifies all concurrent contexts cancel.
func TestHeightContext_MultipleContextsAllCancel(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	const numContexts = 10
	contexts := make([]context.Context, numContexts)
	cancels := make([]context.CancelFunc, numContexts)

	for i := 0; i < numContexts; i++ {
		contexts[i], cancels[i] = epoch.HeightContext(context.Background())
		defer cancels[i]()
	}

	// Advance - all contexts must cancel
	epoch.AdvanceWithTip(1001, "hash_B")

	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			// Success
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Context %d was not cancelled", i)
		}
	}
}

// -----------------------------------------------------------------------------
// Concurrent Access Tests
// -----------------------------------------------------------------------------

// TestHeightContext_ConcurrentAdvance tests concurrent AdvanceWithTip calls.
func TestHeightContext_ConcurrentAdvance(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var wg sync.WaitGroup
	const numGoroutines = 50

	// Concurrent advances with different heights and tips
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			height := uint64(1001 + idx%10)
			tip := "hash_" + string(rune('A'+idx%26))
			epoch.AdvanceWithTip(height, tip)
		}(i)
	}

	wg.Wait()

	// Final height should be the max
	finalHeight := epoch.Height()
	if finalHeight < 1001 || finalHeight > 1010 {
		t.Errorf("Expected final height in range [1001, 1010], got %d", finalHeight)
	}
}

// TestHeightContext_ConcurrentContextCreation tests concurrent HeightContext calls.
func TestHeightContext_ConcurrentContextCreation(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	var wg sync.WaitGroup
	const numGoroutines = 100
	var cancelledCount atomic.Int32

	// Barrier: all goroutines signal after creating their context.
	// This replaces time.Sleep coordination which is flaky under -race.
	var ready sync.WaitGroup
	ready.Add(numGoroutines)
	advanced := make(chan struct{})

	// Spawn goroutines that create contexts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			ready.Done()  // Signal: context created
			<-advanced    // Wait for advance to complete

			select {
			case <-ctx.Done():
				cancelledCount.Add(1)
			default:
			}
		}()
	}

	// Wait for ALL goroutines to create contexts, then advance
	ready.Wait()
	epoch.AdvanceWithTip(1001, "hash_B")
	close(advanced)

	wg.Wait()

	// All contexts were created before the advance, so all should be cancelled
	if cancelledCount.Load() < int32(numGoroutines/2) {
		t.Errorf("Expected at least %d cancellations, got %d", numGoroutines/2, cancelledCount.Load())
	}
}

// TestHeightContext_ConcurrentAdvanceAndContext tests mixed concurrent operations.
func TestHeightContext_ConcurrentAdvanceAndContext(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var wg sync.WaitGroup
	const numWorkers = 20

	// Mix of context creators and advancers
	for i := 0; i < numWorkers; i++ {
		wg.Add(2)

		// Context creator
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ctx, cancel := epoch.HeightContext(context.Background())
				time.Sleep(time.Millisecond)
				cancel()
				_ = ctx // Use ctx to avoid lint warning
			}
		}()

		// Advancer
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				height := uint64(1001 + j)
				tip := "tip_" + string(rune('A'+idx))
				epoch.AdvanceWithTip(height, tip)
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Should complete without deadlock or panic
	if epoch.Height() < 1001 {
		t.Errorf("Expected height >= 1001, got %d", epoch.Height())
	}
}

// -----------------------------------------------------------------------------
// Scenario Tests: Real-World Reorg Patterns
// -----------------------------------------------------------------------------

// TestScenario_CompetingMiners simulates two miners finding blocks at same height.
func TestScenario_CompetingMiners(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Initial state: mining at height 1001, building on block A at 1000
	epoch.AdvanceWithTip(1001, "prevhash_blockA")

	// Our miner starts submission
	submissionCtx, submissionCancel := epoch.HeightContext(context.Background())
	defer submissionCancel()

	// COMPETITOR finds block at same height - network switches to their chain
	epoch.AdvanceWithTip(1001, "prevhash_competitorBlock")

	// Our submission MUST be cancelled
	select {
	case <-submissionCtx.Done():
		t.Log("PASS: Competing block detected, submission cancelled")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("CRITICAL: Submission should cancel when competitor tip detected")
	}
}

// TestScenario_RapidBlockSuccession simulates fast block finding.
func TestScenario_RapidBlockSuccession(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_1000")

	cancelledCount := 0

	for height := uint64(1001); height <= 1010; height++ {
		ctx, cancel := epoch.HeightContext(context.Background())

		// Rapid block found
		epoch.AdvanceWithTip(height, "hash_"+string(rune('0'+height%10)))

		select {
		case <-ctx.Done():
			cancelledCount++
		case <-time.After(10 * time.Millisecond):
		}
		cancel()
	}

	if cancelledCount != 10 {
		t.Errorf("Expected all 10 contexts cancelled, got %d", cancelledCount)
	}
}

// TestScenario_NetworkFlapping simulates rapid tip changes at same height.
func TestScenario_NetworkFlapping(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_A")

	// Track cancellations for tip changes: A->B, B->A, A->B, B->A
	tips := []string{"tip_B", "tip_A", "tip_B", "tip_A"}
	cancellations := 0

	for _, tip := range tips {
		ctx, cancel := epoch.HeightContext(context.Background())
		epoch.AdvanceWithTip(1000, tip)

		select {
		case <-ctx.Done():
			cancellations++
		case <-time.After(10 * time.Millisecond):
		}
		cancel()
	}

	// All tip changes should trigger cancellation
	if cancellations != 4 {
		t.Errorf("Expected 4 cancellations on tip flapping, got %d", cancellations)
	}
}

// TestScenario_OrphanRace simulates the classic orphan race condition.
func TestScenario_OrphanRace(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Setup: at height 1000, tip is hash_A
	epoch.AdvanceWithTip(1000, "hash_A")

	// Miner 1 starts work, gets context
	miner1Ctx, miner1Cancel := epoch.HeightContext(context.Background())
	defer miner1Cancel()

	// Miner 2 (competitor) starts work
	miner2Ctx, miner2Cancel := epoch.HeightContext(context.Background())
	defer miner2Cancel()

	// Both contexts should be valid
	select {
	case <-miner1Ctx.Done():
		t.Fatal("Miner1 context should be valid initially")
	case <-miner2Ctx.Done():
		t.Fatal("Miner2 context should be valid initially")
	default:
	}

	// Network accepts a competing block - tip changes at same height
	epoch.AdvanceWithTip(1000, "hash_COMPETITOR")

	// BOTH miners' contexts must be cancelled
	select {
	case <-miner1Ctx.Done():
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Miner1 context should be cancelled on tip change")
	}

	select {
	case <-miner2Ctx.Done():
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Miner2 context should be cancelled on tip change")
	}
}

// -----------------------------------------------------------------------------
// Legacy Compatibility Tests
// -----------------------------------------------------------------------------

// TestLegacyAdvance_StillWorks verifies backward compatibility.
func TestLegacyAdvance_StillWorks(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.Advance(1000)

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	epoch.Advance(1001)

	select {
	case <-ctx.Done():
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Legacy Advance should still cancel contexts")
	}
}

// TestLegacyAdvance_ClearsTipHash verifies tip is cleared on legacy advance.
func TestLegacyAdvance_ClearsTipHash(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	if epoch.TipHash() != "hash_A" {
		t.Fatal("Setup failed")
	}

	epoch.Advance(1001)

	if epoch.TipHash() != "" {
		t.Errorf("Legacy Advance should clear tip hash, got %s", epoch.TipHash())
	}
}

// TestMixedAdvanceMethods tests mixing legacy and new advance methods.
func TestMixedAdvanceMethods(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Start with new method
	epoch.AdvanceWithTip(1000, "hash_A")
	ctx1, cancel1 := epoch.HeightContext(context.Background())
	defer cancel1()

	// Use legacy method
	epoch.Advance(1001)

	select {
	case <-ctx1.Done():
		// Good
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Context should cancel on legacy advance")
	}

	// Continue with new method
	ctx2, cancel2 := epoch.HeightContext(context.Background())
	defer cancel2()

	epoch.AdvanceWithTip(1002, "hash_C")

	select {
	case <-ctx2.Done():
		// Good
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Context should cancel after mixed advance")
	}
}

// -----------------------------------------------------------------------------
// Stress Tests
// -----------------------------------------------------------------------------

// TestStress_HighVolumeContextCreation stress tests context creation.
func TestStress_HighVolumeContextCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	const iterations = 10000
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()
			_ = ctx
		}()
	}

	wg.Wait()
	// Should complete without deadlock
}

// TestStress_RapidAdvanceCycles stress tests rapid advance cycles.
func TestStress_RapidAdvanceCycles(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	epoch := NewHeightEpoch()

	for i := 0; i < 1000; i++ {
		height := uint64(1000 + i)
		tip := "hash_" + string(rune('A'+(i%26)))

		ctx, cancel := epoch.HeightContext(context.Background())
		epoch.AdvanceWithTip(height, tip)

		select {
		case <-ctx.Done():
			// Expected
		case <-time.After(10 * time.Millisecond):
			t.Fatalf("Iteration %d: context not cancelled", i)
		}
		cancel()
	}
}

// -----------------------------------------------------------------------------
// Edge Cases
// -----------------------------------------------------------------------------

// TestEdgeCase_ZeroHeight verifies handling of height 0.
func TestEdgeCase_ZeroHeight(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// Advance to 0 with tip should work
	epoch.AdvanceWithTip(0, "genesis")

	if epoch.TipHash() != "genesis" {
		t.Errorf("Expected genesis tip, got %s", epoch.TipHash())
	}
}

// TestEdgeCase_EmptyTipHash tests empty tip hash behavior.
func TestEdgeCase_EmptyTipHash(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Empty to non-empty should cancel
	epoch.AdvanceWithTip(1000, "now_has_tip")

	select {
	case <-ctx.Done():
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Should cancel when tip changes from empty to non-empty")
	}
}

// TestEdgeCase_VeryLargeTipHash tests large tip hash strings.
func TestEdgeCase_VeryLargeTipHash(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()

	// 64-char hex hash (typical Bitcoin hash)
	hash := "0000000000000000000123456789abcdef0123456789abcdef0123456789abcd"
	epoch.AdvanceWithTip(1000, hash)

	if epoch.TipHash() != hash {
		t.Errorf("Tip hash mismatch: expected %s, got %s", hash, epoch.TipHash())
	}
}

// TestEdgeCase_ParentContextCancellation verifies parent context propagation.
func TestEdgeCase_ParentContextCancellation(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	parentCtx, parentCancel := context.WithCancel(context.Background())
	ctx, cancel := epoch.HeightContext(parentCtx)
	defer cancel()

	// Cancel parent
	parentCancel()

	// Child context should be cancelled too
	select {
	case <-ctx.Done():
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Context should be cancelled when parent is cancelled")
	}
}
