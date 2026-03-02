// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// PARANOID VERIFICATION TEST SUITE: HeightEpoch ZMQ Edge Cases
// =============================================================================
// Tests covering PARANOID_VERIFICATION_PLAN.md scenarios 9.1, 9.3, 9.4, 9.5.
//
// These test HeightEpoch behavior under ZMQ notification edge cases that are
// not covered by the comprehensive or reorg test suites.

// =============================================================================
// V9.1: ZMQ Drops a Block Notification
// =============================================================================

// TestV9_1_ZMQDrop_NextNotificationUpdates verifies that if one ZMQ block
// notification is dropped (height 101), the next notification (height 102)
// still correctly advances the epoch and cancels stale contexts.
// ZMQ dropping a message means the pool never calls AdvanceWithTip for that
// height. The next notification should still work correctly.
func TestV9_1_ZMQDrop_NextNotificationUpdates(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(100, "hash_100")

	// Create context at height 100
	ctx100, cancel100 := epoch.HeightContext(context.Background())
	defer cancel100()

	// ZMQ notification for height 101 is DROPPED (never arrives)
	// Pool continues mining at height 100's template

	// Next ZMQ notification arrives for height 102
	epoch.AdvanceWithTip(102, "hash_102")

	// Context from height 100 MUST be cancelled
	select {
	case <-ctx100.Done():
		// Correct — even though 101 was skipped, 102 > 100 triggers cancel
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Context should be cancelled when height jumps from 100 to 102 (skipping 101)")
	}

	// Height should be 102, not 101
	if epoch.Height() != 102 {
		t.Errorf("Expected height 102 after skipped notification, got %d", epoch.Height())
	}
	if epoch.TipHash() != "hash_102" {
		t.Errorf("Expected tip hash_102, got %s", epoch.TipHash())
	}
}

// TestV9_1_ZMQDrop_StaleSubmissionCaught verifies that after a ZMQ drop,
// any submission attempt using a context from the pre-drop height is
// correctly cancelled when the next notification arrives.
func TestV9_1_ZMQDrop_StaleSubmissionCaught(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	// Miner starts work at height 1000
	submissionCtx, submissionCancel := epoch.HeightContext(context.Background())
	defer submissionCancel()

	// Heights 1001 and 1002 are dropped by ZMQ
	// Height 1003 arrives
	epoch.AdvanceWithTip(1003, "hash_D")

	// Submission context must be cancelled
	select {
	case <-submissionCtx.Done():
		// Good — stale submission detected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stale submission should be cancelled after ZMQ drop + later notification")
	}
}

// =============================================================================
// V9.3: ZMQ Messages Arrive Out of Order
// =============================================================================

// TestV9_3_OutOfOrder_LowerHeightIgnored verifies that if a ZMQ notification
// arrives with a lower height (e.g., 101 arrives after 102 due to network
// redelivery), it is completely ignored by AdvanceWithTip.
// This tests heightctx.go:82-84: `if newHeight < h.height → return`.
func TestV9_3_OutOfOrder_LowerHeightIgnored(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()

	// Normal: height 101 arrives
	epoch.AdvanceWithTip(101, "hash_101")

	// Normal: height 102 arrives
	epoch.AdvanceWithTip(102, "hash_102")

	// Create context at height 102
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// OUT OF ORDER: height 101 arrives LATE (ZMQ redelivery)
	epoch.AdvanceWithTip(101, "hash_101_late")

	// State should NOT change — lower height is ignored
	if epoch.Height() != 102 {
		t.Errorf("Height should remain 102 after out-of-order 101, got %d", epoch.Height())
	}
	if epoch.TipHash() != "hash_102" {
		t.Errorf("Tip should remain hash_102 after out-of-order 101, got %s", epoch.TipHash())
	}

	// Context should NOT be cancelled
	select {
	case <-ctx.Done():
		t.Fatal("Context should NOT be cancelled by out-of-order lower height")
	case <-time.After(50 * time.Millisecond):
		// Good — ignored
	}
}

// TestV9_3_OutOfOrder_StateUnchanged verifies that out-of-order messages
// don't corrupt the internal state in any way.
func TestV9_3_OutOfOrder_StateUnchanged(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()

	// Advance through several heights
	epoch.AdvanceWithTip(100, "hash_100")
	epoch.AdvanceWithTip(101, "hash_101")
	epoch.AdvanceWithTip(102, "hash_102")

	// Record state
	h, tip := epoch.State()

	// Fire several out-of-order messages
	epoch.AdvanceWithTip(99, "hash_99")
	epoch.AdvanceWithTip(100, "hash_100_again")
	epoch.AdvanceWithTip(101, "hash_101_again")

	// State must be unchanged
	h2, tip2 := epoch.State()
	if h != h2 || tip != tip2 {
		t.Errorf("State changed after out-of-order messages: (%d,%s) → (%d,%s)", h, tip, h2, tip2)
	}
}

// TestV9_3_OutOfOrder_ConcurrentOutOfOrder stress-tests out-of-order
// messages from multiple goroutines to verify no data races or state corruption.
func TestV9_3_OutOfOrder_ConcurrentOutOfOrder(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_1000")

	var wg sync.WaitGroup

	// Goroutines sending out-of-order messages (heights 990-999)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(height uint64) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				epoch.AdvanceWithTip(height, "old_hash")
			}
		}(uint64(990 + i))
	}

	// One goroutine advancing forward
	wg.Add(1)
	go func() {
		defer wg.Done()
		for h := uint64(1001); h <= 1010; h++ {
			epoch.AdvanceWithTip(h, "forward_hash")
		}
	}()

	wg.Wait()

	// Height should be at least 1010 (the forward goroutine's max)
	finalHeight := epoch.Height()
	if finalHeight < 1010 {
		t.Errorf("Expected height >= 1010, got %d", finalHeight)
	}
}

// =============================================================================
// V9.4: ZMQ Disconnects During Block Submission
// =============================================================================

// TestV9_4_ZMQDisconnect_RPCIndependent verifies that block submission
// (SubmitBlockWithVerification) is NOT affected by ZMQ state. The RPC call
// uses HTTP/JSON-RPC which is completely independent of the ZMQ PUB/SUB
// channel. Even if ZMQ disconnects, the HeightContext only cancels when
// AdvanceWithTip is explicitly called — a ZMQ disconnect alone doesn't
// change the epoch state.
func TestV9_4_ZMQDisconnect_RPCIndependent(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_1000")

	// Create submission context
	submissionCtx, submissionCancel := epoch.HeightContext(context.Background())
	defer submissionCancel()

	// ZMQ disconnects — but no AdvanceWithTip is called
	// (The ZMQ disconnect handler would try to reconnect, but it doesn't
	// call AdvanceWithTip because it has no new block info)

	// Simulate: some time passes with ZMQ disconnected
	time.Sleep(10 * time.Millisecond)

	// Submission context should still be valid
	select {
	case <-submissionCtx.Done():
		t.Fatal("Submission context should NOT be cancelled by ZMQ disconnect alone")
	default:
		// Good — ZMQ disconnect doesn't affect HeightEpoch state
	}

	// Height and tip should be unchanged
	if epoch.Height() != 1000 {
		t.Errorf("Height should be unchanged during ZMQ disconnect, got %d", epoch.Height())
	}
}

// TestV9_4_ZMQReconnect_NotificationsResume verifies that after ZMQ
// reconnects, new notifications are processed normally and stale contexts
// are cancelled.
func TestV9_4_ZMQReconnect_NotificationsResume(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_1000")

	// Create context during "connected" phase
	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// ZMQ disconnects — context stays valid (no AdvanceWithTip called)

	// ZMQ reconnects — first notification after reconnect
	epoch.AdvanceWithTip(1003, "hash_1003") // Missed 1001, 1002

	// Context from height 1000 must now be cancelled
	select {
	case <-ctx.Done():
		// Correct — first notification after reconnect cancels stale context
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Context should cancel when notifications resume after ZMQ reconnect")
	}
}

// =============================================================================
// V9.5: Polling Fallback Delay → HeightContext Cancellation
// =============================================================================

// TestV9_5_PollingDetectsNewHeight_ContextCancels simulates the scenario
// where ZMQ is not available (or not yet promoted) and RPC polling detects
// a new chain height. The polling path calls AdvanceWithTip, which should
// cancel any existing HeightContext.
func TestV9_5_PollingDetectsNewHeight_ContextCancels(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()

	// Initial state from polling
	epoch.AdvanceWithTip(1000, "hash_1000")

	// Block submission starts
	submissionCtx, submissionCancel := epoch.HeightContext(context.Background())
	defer submissionCancel()

	// Polling interval fires — detects new block at height 1001
	// (In production, this happens via RefreshJob → AdvanceWithTip)
	epoch.AdvanceWithTip(1001, "hash_1001")

	// Submission context must be cancelled
	select {
	case <-submissionCtx.Done():
		// Correct — polling detected new height
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Submission should be cancelled when polling detects new height")
	}
}

// TestV9_5_PollingDetectsSameHeightReorg simulates polling detecting a
// same-height reorg (tip hash changed but height unchanged).
func TestV9_5_PollingDetectsSameHeightReorg(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	ctx, cancel := epoch.HeightContext(context.Background())
	defer cancel()

	// Polling detects same height but different tip (competing block won)
	epoch.AdvanceWithTip(1000, "hash_B")

	select {
	case <-ctx.Done():
		// Correct — same-height reorg detected via polling
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Context should cancel on same-height reorg detected by polling")
	}
}

// =============================================================================
// HeightContext Chaining: Multiple Submissions at Same Height
// =============================================================================

// TestHeightContextChaining_AllCancelOnAdvance verifies that multiple
// HeightContext calls at the same height all produce contexts that cancel
// when AdvanceWithTip is called. This is critical because handleBlock may
// be called multiple times at the same height (multiple shares meeting
// target), and ALL submission contexts must cancel on height advance.
func TestHeightContextChaining_AllCancelOnAdvance(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	// Create 5 contexts at the same height (5 concurrent submissions)
	const numCtx = 5
	contexts := make([]context.Context, numCtx)
	cancels := make([]context.CancelFunc, numCtx)

	for i := 0; i < numCtx; i++ {
		contexts[i], cancels[i] = epoch.HeightContext(context.Background())
		defer cancels[i]()
	}

	// All should be alive
	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			t.Fatalf("Context %d should not be cancelled initially", i)
		default:
		}
	}

	// Height advances — ALL must cancel
	epoch.AdvanceWithTip(1001, "hash_B")

	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			// Good
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Context %d should be cancelled on height advance", i)
		}
	}
}

// TestHeightContextChaining_AllCancelOnSameHeightReorg verifies chaining
// works for same-height tip changes too.
func TestHeightContextChaining_AllCancelOnSameHeightReorg(t *testing.T) {
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "hash_A")

	const numCtx = 5
	contexts := make([]context.Context, numCtx)
	cancels := make([]context.CancelFunc, numCtx)

	for i := 0; i < numCtx; i++ {
		contexts[i], cancels[i] = epoch.HeightContext(context.Background())
		defer cancels[i]()
	}

	// Same-height reorg — ALL must cancel
	epoch.AdvanceWithTip(1000, "hash_B")

	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			// Good
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("Context %d should be cancelled on same-height reorg", i)
		}
	}
}
