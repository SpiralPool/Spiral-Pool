// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"sync"
)

// HeightEpoch tracks the current chain height AND tip hash, providing contexts that cancel
// when either the height advances OR the tip hash changes at the same height.
//
// CRITICAL FIX: Same-height reorg detection.
// Previously, HeightEpoch only cancelled contexts when height advanced. This missed
// the most common orphan scenario: competing tips at the SAME height. When two miners
// find blocks within seconds, both are at the same height but different hashes.
// Without same-height detection, block submission continues to a potentially losing tip.
//
// Usage:
//
//	epoch := NewHeightEpoch()
//	ctx, cancel := epoch.HeightContext(context.Background())
//	defer cancel()
//	// Use ctx for block submission — it cancels if epoch.Advance() is called
//	// with a higher height OR different tip hash at the same height.
type HeightEpoch struct {
	mu      sync.Mutex
	height  uint64
	tipHash string                // CRITICAL: Track tip hash for same-height reorg detection
	cancel  context.CancelFunc   // cancels the current epoch's derived context
}

// NewHeightEpoch creates a HeightEpoch starting at height 0 with no active context.
func NewHeightEpoch() *HeightEpoch {
	return &HeightEpoch{}
}

// Advance updates the chain height. If newHeight > current height, the previous
// epoch's context is canceled (killing any in-flight submissions for the old height).
// Called from OnBlockNotification (ZMQ) and RefreshJob (RPC polling).
//
// DEPRECATED: Use AdvanceWithTip instead for same-height reorg protection.
// This method is kept for backward compatibility but provides no same-height protection.
//
// Safe to call concurrently from multiple goroutines.
func (h *HeightEpoch) Advance(newHeight uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if newHeight <= h.height {
		return // not an advance — ignore reorgs to same or lower height
	}

	// Cancel the old epoch's context before updating height.
	// Any in-flight submission using HeightContext() will see context.Canceled.
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}

	h.height = newHeight
	h.tipHash = "" // Clear tip hash when using legacy Advance
}

// AdvanceWithTip updates the chain height AND tip hash. Cancels the previous epoch's
// context if EITHER:
//   - newHeight > current height (chain advanced)
//   - newHeight == current height AND newTipHash != current tipHash (same-height reorg)
//
// This is the CRITICAL fix for same-height competing tips, which is the most common
// cause of block orphaning. When two miners find blocks at the same height, only one
// wins. Without tip hash tracking, we would continue submitting to a potentially
// losing chain.
//
// Safe to call concurrently from multiple goroutines.
func (h *HeightEpoch) AdvanceWithTip(newHeight uint64, newTipHash string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Case 1: Lower height - ignore (not an advance)
	if newHeight < h.height {
		return
	}

	// Case 2: Same height, same tip - no change needed
	if newHeight == h.height && newTipHash == h.tipHash {
		return
	}

	// Case 3: Same height, DIFFERENT tip - SAME-HEIGHT REORG DETECTED!
	// This is the critical case that was previously missed.
	// Cancel contexts because we're now on a different chain tip.
	if newHeight == h.height && newTipHash != h.tipHash && h.tipHash != "" {
		// Log would go here in production, but we don't have logger access
		// The cancellation itself is the important action
	}

	// Case 4: Higher height - standard advance

	// Cancel the old epoch's context before updating.
	// Any in-flight submission using HeightContext() will see context.Canceled.
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}

	h.height = newHeight
	h.tipHash = newTipHash
}

// HeightContext returns a context derived from parent that will be canceled when
// the chain height advances past the current height OR when the tip hash changes
// at the same height. The returned cancel function must be called by the caller
// (standard Go context discipline).
//
// If AdvanceWithTip() is called with a higher height OR different tip hash before
// the caller finishes, the returned context cancels — this is the core mechanism
// that kills stale block submissions.
func (h *HeightEpoch) HeightContext(parent context.Context) (context.Context, context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Create a new cancellable context for this epoch.
	// If there's already a cancel from a previous HeightContext call at the
	// same height, we chain: both the old and new contexts will cancel on Advance.
	// We use context.WithCancel to create a child that we control.
	epochCtx, epochCancel := context.WithCancel(parent)

	// Store the cancel so Advance()/AdvanceWithTip() can trigger it.
	// If there's an existing cancel (from a prior HeightContext at the same height),
	// we need to cancel both on advance. Chain them.
	prevCancel := h.cancel
	h.cancel = func() {
		epochCancel()
		if prevCancel != nil {
			prevCancel()
		}
	}

	return epochCtx, epochCancel
}

// Height returns the current chain height (thread-safe).
func (h *HeightEpoch) Height() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.height
}

// TipHash returns the current tip hash (thread-safe).
func (h *HeightEpoch) TipHash() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tipHash
}

// State returns both height and tip hash atomically (thread-safe).
// Use this when you need a consistent snapshot of both values.
func (h *HeightEpoch) State() (height uint64, tipHash string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.height, h.tipHash
}
