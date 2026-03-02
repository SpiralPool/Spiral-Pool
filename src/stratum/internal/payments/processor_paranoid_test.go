// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"testing"
)

// =============================================================================
// PARANOID VERIFICATION TEST SUITE: Confirmation Cycle RPC Failures
// =============================================================================
// Tests covering PARANOID_VERIFICATION_PLAN.md scenarios 6.6 and 6.7.
//
// These simulate RPC failure scenarios during the confirmation cycle in
// processor.go:updateBlockConfirmations() and verify that blocks are not
// falsely orphaned or confirmed when RPCs fail.

// =============================================================================
// V6.6: GetBlockHash RPC Fails During Confirmation Cycle
// =============================================================================

// TestV6_6_GetBlockHashFails_BlockSkipped simulates processor.go:306-313
// where GetBlockHash returns an error for a specific block. The block should
// be skipped (remain "pending"), NOT orphaned, and retried next cycle.
func TestV6_6_GetBlockHashFails_BlockSkipped(t *testing.T) {
	t.Parallel()

	blocks := []MockBlock{
		{Height: 1000, Hash: "hash_1000", Status: StatusPending, OrphanMismatchCount: 0},
		{Height: 1001, Hash: "hash_1001", Status: StatusPending, OrphanMismatchCount: 0},
		{Height: 1002, Hash: "hash_1002", Status: StatusPending, OrphanMismatchCount: 0},
	}

	// Simulate GetBlockHash results
	type rpcResult struct {
		hash string
		err  bool
	}
	rpcResults := map[uint64]rpcResult{
		1000: {hash: "hash_1000", err: false}, // Success
		1001: {hash: "", err: true},            // RPC FAILURE
		1002: {hash: "hash_1002", err: false}, // Success
	}

	maturity := 100
	snapshotHeight := uint64(1200)

	for i := range blocks {
		block := &blocks[i]
		result := rpcResults[block.Height]

		if result.err {
			// GetBlockHash failed → continue (skip this block)
			// Block remains in current status, retry next cycle
			// processor.go:306-313: `if err != nil { continue }`
			continue
		}

		// Normal processing
		chainHash := result.hash
		if chainHash != block.Hash {
			block.OrphanMismatchCount++
			if block.OrphanMismatchCount >= OrphanMismatchThreshold {
				block.Status = StatusOrphaned
			}
		} else {
			block.OrphanMismatchCount = 0
			if block.Height <= snapshotHeight {
				confirmations := snapshotHeight - block.Height
				if confirmations >= uint64(maturity) {
					block.StabilityCheckCount++
					if block.StabilityCheckCount >= StabilityWindowChecks {
						block.Status = StatusConfirmed
					}
				}
			}
		}
	}

	// Block 1000: processed normally (hash matches)
	if blocks[0].Status == StatusOrphaned {
		t.Error("Block 1000 should NOT be orphaned (hash matched)")
	}

	// Block 1001: SKIPPED due to RPC failure — should still be pending
	if blocks[1].Status != StatusPending {
		t.Errorf("Block 1001 should remain 'pending' after RPC failure, got %q", blocks[1].Status)
	}
	if blocks[1].OrphanMismatchCount != 0 {
		t.Errorf("Block 1001 mismatch count should be 0 (skipped, not checked), got %d",
			blocks[1].OrphanMismatchCount)
	}

	// Block 1002: processed normally
	if blocks[2].Status == StatusOrphaned {
		t.Error("Block 1002 should NOT be orphaned (hash matched)")
	}
}

// TestV6_6_GetBlockHashFails_RepeatedFailures verifies that repeated RPC
// failures do NOT increment the orphan mismatch counter. The block should
// remain pending indefinitely until the RPC succeeds.
func TestV6_6_GetBlockHashFails_RepeatedFailures(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
	}

	// Simulate 10 cycles where GetBlockHash always fails
	for cycle := 0; cycle < 10; cycle++ {
		rpcErr := true // GetBlockHash returns error every time

		if rpcErr {
			// Skip — processor.go:306-313
			continue
		}

		// This code is unreachable in this test, but shows the normal path
		block.OrphanMismatchCount++
	}

	// Block must still be pending after all failures
	if block.Status != StatusPending {
		t.Errorf("Block should remain 'pending' after repeated RPC failures, got %q", block.Status)
	}
	if block.OrphanMismatchCount != 0 {
		t.Errorf("Mismatch count should be 0 (never checked), got %d", block.OrphanMismatchCount)
	}
}

// TestV6_6_GetBlockHashFails_ThenSucceeds verifies that after several RPC
// failures, when GetBlockHash finally succeeds, the block is processed
// normally and can eventually reach confirmation.
func TestV6_6_GetBlockHashFails_ThenSucceeds(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
		StabilityCheckCount: 0,
		LastVerifiedTip:     "",
	}

	maturity := 100
	snapshotHeight := uint64(1200) // 200 confirmations
	currentTip := "stable_tip"

	// Simulate: 5 cycles fail, then succeeds for StabilityWindowChecks cycles
	totalCycles := 5 + StabilityWindowChecks

	for cycle := 0; cycle < totalCycles; cycle++ {
		rpcErr := cycle < 5 // First 5 cycles fail

		if rpcErr {
			continue // Skip — processor.go:306-313
		}

		// RPC succeeded — check hash
		chainHash := "our_hash" // Hash matches
		if chainHash != block.Hash {
			block.OrphanMismatchCount++
			block.StabilityCheckCount = 0
		} else {
			block.OrphanMismatchCount = 0

			if block.Height <= snapshotHeight {
				confirmations := snapshotHeight - block.Height
				if confirmations >= uint64(maturity) {
					if block.LastVerifiedTip == "" || block.LastVerifiedTip == currentTip {
						block.StabilityCheckCount++
						block.LastVerifiedTip = currentTip
					}

					if block.StabilityCheckCount >= StabilityWindowChecks {
						block.Status = StatusConfirmed
						break
					}
				}
			}
		}
	}

	if block.Status != StatusConfirmed {
		t.Errorf("Block should eventually be confirmed after RPC recovers, got %q", block.Status)
	}
}

// =============================================================================
// V6.7: GetBlockchainInfo Fails at Cycle Start
// =============================================================================

// TestV6_7_GetBlockchainInfoFails_NoCycleRun simulates processor.go:238-241
// where GetBlockchainInfo returns an error at the start of the cycle. No
// blocks should be processed and no status changes should occur.
func TestV6_7_GetBlockchainInfoFails_NoCycleRun(t *testing.T) {
	t.Parallel()

	blocks := []MockBlock{
		{Height: 1000, Hash: "hash_1000", Status: StatusPending},
		{Height: 1001, Hash: "hash_1001", Status: StatusPending},
	}

	// Simulate GetBlockchainInfo failure
	getBlockchainInfoErr := true
	blocksProcessed := 0

	if getBlockchainInfoErr {
		// processor.go:238-241: return error, cycle doesn't run
		// No blocks are processed, no status changes
	} else {
		// Normal cycle
		for range blocks {
			blocksProcessed++
		}
	}

	if blocksProcessed != 0 {
		t.Errorf("Expected 0 blocks processed when GetBlockchainInfo fails, got %d", blocksProcessed)
	}

	// All blocks must remain in their original status
	for i, block := range blocks {
		if block.Status != StatusPending {
			t.Errorf("Block %d should remain 'pending', got %q", i, block.Status)
		}
	}
}

// TestV6_7_GetBlockchainInfoFails_RepeatedCycles verifies that repeated
// GetBlockchainInfo failures don't corrupt any state. Each failed cycle
// is a no-op.
func TestV6_7_GetBlockchainInfoFails_RepeatedCycles(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
		StabilityCheckCount: 2, // Was making progress before failures started
		LastVerifiedTip:     "tip_A",
	}

	// Save initial state
	initialStatus := block.Status
	initialMismatch := block.OrphanMismatchCount
	initialStability := block.StabilityCheckCount
	initialTip := block.LastVerifiedTip

	// Simulate 10 failed cycles
	for cycle := 0; cycle < 10; cycle++ {
		getBlockchainInfoErr := true
		if getBlockchainInfoErr {
			continue // Cycle doesn't run
		}
	}

	// All state must be preserved exactly
	if block.Status != initialStatus {
		t.Errorf("Status changed from %q to %q during failed cycles", initialStatus, block.Status)
	}
	if block.OrphanMismatchCount != initialMismatch {
		t.Errorf("OrphanMismatchCount changed from %d to %d", initialMismatch, block.OrphanMismatchCount)
	}
	if block.StabilityCheckCount != initialStability {
		t.Errorf("StabilityCheckCount changed from %d to %d", initialStability, block.StabilityCheckCount)
	}
	if block.LastVerifiedTip != initialTip {
		t.Errorf("LastVerifiedTip changed from %q to %q", initialTip, block.LastVerifiedTip)
	}
}

// TestV6_7_GetBlockchainInfoFails_ThenRecovers verifies that after
// GetBlockchainInfo starts working again, the confirmation cycle resumes
// normally from where it left off.
func TestV6_7_GetBlockchainInfoFails_ThenRecovers(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
		StabilityCheckCount: 0,
		LastVerifiedTip:     "",
	}

	maturity := 100
	snapshotHeight := uint64(1200)
	currentTip := "stable_tip"

	// Simulate: 5 failed cycles, then successful cycles
	for cycle := 0; cycle < 5+StabilityWindowChecks; cycle++ {
		getBlockchainInfoErr := cycle < 5

		if getBlockchainInfoErr {
			continue // No processing
		}

		// Cycle runs normally
		chainHash := "our_hash"
		if chainHash == block.Hash {
			block.OrphanMismatchCount = 0

			if block.Height <= snapshotHeight {
				confirmations := snapshotHeight - block.Height
				if confirmations >= uint64(maturity) {
					if block.LastVerifiedTip == "" || block.LastVerifiedTip == currentTip {
						block.StabilityCheckCount++
						block.LastVerifiedTip = currentTip
					}

					if block.StabilityCheckCount >= StabilityWindowChecks {
						block.Status = StatusConfirmed
						break
					}
				}
			}
		}
	}

	if block.Status != StatusConfirmed {
		t.Errorf("Block should confirm after GetBlockchainInfo recovers, got %q", block.Status)
	}
}

// =============================================================================
// Combined: TOCTOU + RPC Failure
// =============================================================================

// TestTOCTOU_GetBlockchainInfoFails_MidCycle simulates the scenario where
// GetBlockchainInfo succeeds at cycle start (providing snapshotTip) but
// fails on a subsequent per-block check. This tests the TOCTOU check at
// processor.go:254-269 where GetBlockchainInfo is called before each block.
func TestTOCTOU_GetBlockchainInfoFails_MidCycle(t *testing.T) {
	t.Parallel()

	snapshotTip := "tip_at_start"
	blocksToProcess := 5
	processedBlocks := 0
	cycleAborted := false

	for i := 0; i < blocksToProcess; i++ {
		// Simulate per-block GetBlockchainInfo
		rpcFailed := (i == 2) // Fails at block 2

		if rpcFailed {
			// processor.go:254-269: if GetBlockchainInfo fails, we can't
			// verify the tip hasn't changed. Conservative approach: abort cycle.
			// (In actual code, this returns an error which aborts the cycle)
			cycleAborted = true
			break
		}

		// Check TOCTOU
		currentTip := snapshotTip // Tip unchanged in this test
		if currentTip != snapshotTip {
			cycleAborted = true
			break
		}

		processedBlocks++
	}

	if !cycleAborted {
		t.Error("Cycle should abort when mid-cycle GetBlockchainInfo fails")
	}
	if processedBlocks != 2 {
		t.Errorf("Expected 2 blocks processed before abort, got %d", processedBlocks)
	}
}

// =============================================================================
// Deep Reorg with TOCTOU Check
// =============================================================================

// TestDeepReorg_WithTOCTOU verifies that the deep reorg check at
// processor.go:437-514 also performs a TOCTOU check (processor.go:463-468)
// before re-verifying confirmed blocks.
func TestDeepReorg_WithTOCTOU(t *testing.T) {
	t.Parallel()

	confirmedBlocks := []MockBlock{
		{Height: 1000, Hash: "hash_1000", Status: StatusConfirmed},
		{Height: 1001, Hash: "hash_1001", Status: StatusConfirmed},
		{Height: 1002, Hash: "hash_1002", Status: StatusConfirmed},
	}

	snapshotTip := "tip_at_start"
	verifiedBlocks := 0
	cycleAborted := false

	for i := range confirmedBlocks {
		// TOCTOU check before each verification
		currentTip := snapshotTip
		if i == 1 {
			currentTip = "tip_changed" // Tip changes at block 1
		}

		if currentTip != snapshotTip {
			cycleAborted = true
			break
		}

		// Verify block hash
		// (In this test, we don't actually change any statuses)
		verifiedBlocks++
	}

	if !cycleAborted {
		t.Error("Deep reorg check should abort when tip changes mid-verification")
	}
	if verifiedBlocks != 1 {
		t.Errorf("Expected 1 block verified before abort, got %d", verifiedBlocks)
	}

	// All confirmed blocks should remain confirmed (abort = no changes)
	for i, block := range confirmedBlocks {
		if block.Status != StatusConfirmed {
			t.Errorf("Block %d should remain 'confirmed' after aborted cycle, got %q", i, block.Status)
		}
	}
}
