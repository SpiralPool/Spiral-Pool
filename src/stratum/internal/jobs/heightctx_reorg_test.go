// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"testing"
	"time"
)

// =============================================================================
// CRITICAL FIX TESTS: Same-Height Reorg Detection
// =============================================================================
// These tests validate the CRITICAL FIX for same-height competing tips.
// Previously, HeightContext only cancelled on height advance, missing the
// most common orphan scenario: two miners finding blocks at the same height.

// TestAdvanceWithTip_SameHeightDifferentTip_CancelsContext
// CRITICAL: This is the main fix for same-height reorgs.
// When two miners find blocks at the same height, one wins and one orphans.
// HeightContext must cancel when the tip changes even at the same height.
func TestAdvanceWithTip_SameHeightDifferentTip_CancelsContext(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()

	// Initial state: height 1000, tip hash A
	epoch.AdvanceWithTip(1000, "hash_A")

	// Get context at this height/tip
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Verify context is valid
	select {
	case <-ctx.Done():
		t.Fatal("Context should not be cancelled yet")
	default:
		// Good - context is still valid
	}

	// CRITICAL TEST: Same height, DIFFERENT tip (competing block found)
	// This is a same-height reorg - must cancel context!
	epoch.AdvanceWithTip(1000, "hash_B")

	// Context MUST be cancelled
	select {
	case <-ctx.Done():
		// CORRECT! Context cancelled on same-height tip change
		t.Log("PASS: Context correctly cancelled on same-height tip change")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("CRITICAL BUG: Context should have been cancelled on same-height tip change")
	}
}

// TestAdvanceWithTip_SameHeightSameTip_NoCancel
// When height and tip are both the same, nothing should happen.
func TestAdvanceWithTip_SameHeightSameTip_NoCancel(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Call with same height AND same tip - should be no-op
	epoch.AdvanceWithTip(1000, "hash_A")

	// Context should NOT be cancelled
	select {
	case <-ctx.Done():
		t.Fatal("Context should NOT be cancelled when height and tip are unchanged")
	case <-time.After(50 * time.Millisecond):
		// Good - context still valid
	}
}

// TestAdvanceWithTip_HigherHeight_CancelsContext
// Standard case: height increases, context cancels.
func TestAdvanceWithTip_HigherHeight_CancelsContext(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Higher height, any tip
	epoch.AdvanceWithTip(1001, "hash_B")

	select {
	case <-ctx.Done():
		// Correct - cancelled on height advance
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Context should have been cancelled on height advance")
	}
}

// TestAdvanceWithTip_LowerHeight_NoCancel
// Chain can't go backwards, ignore lower heights.
func TestAdvanceWithTip_LowerHeight_NoCancel(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Lower height - should be ignored
	epoch.AdvanceWithTip(999, "hash_B")

	select {
	case <-ctx.Done():
		t.Fatal("Context should NOT be cancelled on lower height")
	case <-time.After(50 * time.Millisecond):
		// Good - ignored
	}
}

// TestAdvanceWithTip_MultipleContexts_AllCancelled
// Multiple in-flight submissions should all cancel on tip change.
func TestAdvanceWithTip_MultipleContexts_AllCancelled(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	// Multiple contexts (simulates multiple concurrent submissions)
	ctx1, cancel1 := epoch.HeightContext(context.Background())
	defer cancel1()
	ctx2, cancel2 := epoch.HeightContext(context.Background())
	defer cancel2()
	ctx3, cancel3 := epoch.HeightContext(context.Background())
	defer cancel3()

	// Same-height tip change
	epoch.AdvanceWithTip(1000, "hash_B")

	// ALL contexts must be cancelled
	for i, ctx := range []context.Context{ctx1, ctx2, ctx3} {
		select {
		case <-ctx.Done():
			// Correct
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Context %d should have been cancelled", i+1)
		}
	}
}

// TestAdvanceWithTip_RapidTipChanges
// Simulates rapid competing tips (race condition stress)
func TestAdvanceWithTip_RapidTipChanges(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_initial")

	cancelledCount := 0

	for i := 0; i < 10; i++ {
		ctx, cancel := epoch.HeightContext(context.Background())

		// Rapid tip change at same height
		epoch.AdvanceWithTip(1000, "hash_"+string(rune('A'+i)))

		select {
		case <-ctx.Done():
			cancelledCount++
		case <-time.After(50 * time.Millisecond):
			// Not cancelled
		}
		cancel()
	}

	// All should have been cancelled
	if cancelledCount != 10 {
		t.Errorf("Expected 10 cancellations, got %d", cancelledCount)
	}
}

// TestStateMethod_Atomicity
// State() should return consistent height/tip pair.
func TestStateMethod_Atomicity(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	height, tip := epoch.State()
	if height != 1000 {
		t.Errorf("Expected height 1000, got %d", height)
	}
	if tip != "hash_A" {
		t.Errorf("Expected tip hash_A, got %s", tip)
	}

	epoch.AdvanceWithTip(1001, "hash_B")

	height, tip = epoch.State()
	if height != 1001 {
		t.Errorf("Expected height 1001, got %d", height)
	}
	if tip != "hash_B" {
		t.Errorf("Expected tip hash_B, got %s", tip)
	}
}

// TestTipHash_ReturnsCorrectValue
func TestTipHash_ReturnsCorrectValue(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()

	// Initially empty
	if epoch.TipHash() != "" {
		t.Errorf("Expected empty tip initially, got %s", epoch.TipHash())
	}

	epoch.AdvanceWithTip(1000, "hash_A")
	if epoch.TipHash() != "hash_A" {
		t.Errorf("Expected hash_A, got %s", epoch.TipHash())
	}
}

// TestReorg_LegacyAdvance_ClearsTipHash
// Legacy Advance() should clear tip hash for backward compatibility.
func TestReorg_LegacyAdvance_ClearsTipHash(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	if epoch.TipHash() != "hash_A" {
		t.Fatalf("Setup failed - tip should be hash_A")
	}

	// Legacy advance clears tip
	epoch.Advance(1001)

	if epoch.TipHash() != "" {
		t.Errorf("Legacy Advance should clear tip hash, got %s", epoch.TipHash())
	}
}

// TestBackwardCompatibility_LegacyAdvanceStillWorks
// Ensure legacy Advance() still works for existing code.
func TestBackwardCompatibility_LegacyAdvanceStillWorks(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.Advance(1000)

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Legacy advance to higher height should still cancel
	epoch.Advance(1001)

	select {
	case <-ctx.Done():
		// Correct - legacy still works
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Legacy Advance should still cancel contexts on height increase")
	}
}

// =============================================================================
// SCENARIO TESTS: Real-World Orphan Patterns
// =============================================================================

// TestScenario_CompetingMinersAtSameHeight
// Two miners find blocks within seconds - this is the #1 orphan cause
func TestScenario_CompetingMinersAtSameHeight(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()

	// Pool is mining at height 1001, tip is block A at height 1000
	epoch.AdvanceWithTip(1001, "hash_blockA_at_1000")

	// We submit a block for height 1001
	submissionCtx, submissionCancel := epoch.HeightContext(context.Background())
	defer submissionCancel()

	// COMPETITOR finds block at height 1001 too!
	// Network switches to their block as the tip at height 1000
	// Our daemon now reports a DIFFERENT tip at the same height 1000
	epoch.AdvanceWithTip(1001, "hash_competitorBlockA_at_1000")

	// Our submission MUST be cancelled - we're building on wrong tip
	select {
	case <-submissionCtx.Done():
		t.Log("PASS: Competing block detected, our submission cancelled")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ORPHAN VULNERABILITY: Submission should cancel when competitor tip detected")
	}
}

// TestReorg_NetworkFlapping
// Network can't decide which tip is winning (rapid flapping)
func TestReorg_NetworkFlapping(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	totalCancelled := 0

	// Network flaps between tips A and B
	tips := []string{"hash_A", "hash_B", "hash_A", "hash_B", "hash_A"}
	for _, tip := range tips {
		ctx, cancel := epoch.HeightContext(context.Background())
		epoch.AdvanceWithTip(1000, tip)
		select {
		case <-ctx.Done():
			totalCancelled++
		case <-time.After(10 * time.Millisecond):
			// Not cancelled (same tip as before)
		}
		cancel()
	}

	// Should cancel on every tip CHANGE (A→B, B→A, etc.)
	// Sequence: A→A (no), A→B (yes), B→A (yes), A→B (yes), B→A (yes) = 4
	if totalCancelled < 4 {
		t.Errorf("Expected at least 4 cancellations on tip flapping, got %d", totalCancelled)
	}
}
