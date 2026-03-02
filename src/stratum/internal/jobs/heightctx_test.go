// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestHeightEpoch_Advance_CancelsOldContext(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(100)

	// Get a context for height 100
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Context should be alive
	select {
	case <-ctx.Done():
		t.Fatal("context should not be canceled yet")
	default:
	}

	// Advance to 101 — should cancel the height-100 context
	epoch.Advance(101)

	// Context should now be canceled
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("context should have been canceled after Advance")
	}
}

func TestHeightEpoch_Advance_SameHeight_NoCancellation(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(100)

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Advance to same height — should NOT cancel
	epoch.Advance(100)

	select {
	case <-ctx.Done():
		t.Fatal("context should not be canceled for same height")
	default:
		// expected
	}
}

func TestHeightEpoch_Advance_LowerHeight_NoCancellation(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(100)

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Advance to lower height (reorg to lower) — should NOT cancel
	epoch.Advance(99)

	select {
	case <-ctx.Done():
		t.Fatal("context should not be canceled for lower height")
	default:
		// expected
	}
}

func TestHeightEpoch_MultipleContexts_AllCanceled(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(100)

	// Create multiple contexts at the same height
	ctx1, cancel1 := epoch.HeightContext(context.Background())
	defer cancel1()
	ctx2, cancel2 := epoch.HeightContext(context.Background())
	defer cancel2()

	// Both should be alive
	select {
	case <-ctx1.Done():
		t.Fatal("ctx1 should not be canceled yet")
	default:
	}
	select {
	case <-ctx2.Done():
		t.Fatal("ctx2 should not be canceled yet")
	default:
	}

	// Advance — both should cancel
	epoch.Advance(101)

	select {
	case <-ctx1.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ctx1 should be canceled")
	}
	select {
	case <-ctx2.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ctx2 should be canceled")
	}
}

func TestHeightEpoch_HeightContext_InheritsParentCancellation(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(100)

	// Create a parent context we can cancel
	parent, parentCancel := context.WithCancel(context.Background())

	ctx, cancel := epoch.HeightContext(parent)
	defer cancel()

	// Cancel parent — child should also cancel
	parentCancel()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("child context should cancel when parent cancels")
	}
}

func TestHeightEpoch_Height_ReturnsCurrentHeight(t *testing.T) {
	epoch := NewHeightEpoch()

	if epoch.Height() != 0 {
		t.Fatalf("expected initial height 0, got %d", epoch.Height())
	}

	epoch.Advance(500)
	if epoch.Height() != 500 {
		t.Fatalf("expected height 500, got %d", epoch.Height())
	}

	// Lower height doesn't change it
	epoch.Advance(499)
	if epoch.Height() != 500 {
		t.Fatalf("expected height 500 after lower advance, got %d", epoch.Height())
	}
}

func TestHeightEpoch_ConcurrentAdvance(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(1)

	var wg sync.WaitGroup
	// Hammer Advance from many goroutines
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(h uint64) {
			defer wg.Done()
			epoch.Advance(h)
		}(uint64(i + 2))
	}
	wg.Wait()

	// Final height should be 101 (highest value written)
	if epoch.Height() != 101 {
		t.Fatalf("expected height 101 after concurrent advances, got %d", epoch.Height())
	}
}

func TestHeightEpoch_ConcurrentHeightContextAndAdvance(t *testing.T) {
	epoch := NewHeightEpoch()
	epoch.Advance(1)

	var wg sync.WaitGroup
	// Mix of HeightContext and Advance calls
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(h uint64) {
			defer wg.Done()
			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()
			// Just exercise the context
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}(uint64(i + 2))
		go func(h uint64) {
			defer wg.Done()
			epoch.Advance(h)
		}(uint64(i + 2))
	}
	wg.Wait()
	// No panics or deadlocks = pass
}

func TestHeightEpoch_AdvanceFromZero(t *testing.T) {
	epoch := NewHeightEpoch()

	// HeightContext before any Advance should still work
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Advance from 0 to 1
	epoch.Advance(1)

	select {
	case <-ctx.Done():
		// expected — height changed from 0 to 1
	case <-time.After(100 * time.Millisecond):
		t.Fatal("context should cancel when advancing from 0")
	}
}
