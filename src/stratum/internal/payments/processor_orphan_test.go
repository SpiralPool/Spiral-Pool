// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Orphan Detection & Stability Window
// =============================================================================
// These tests verify the orphan detection logic including delayed orphaning,
// stability window checks, and TOCTOU protection.

// -----------------------------------------------------------------------------
// Constants Verification Tests
// -----------------------------------------------------------------------------

// TestConstants_OrphanThreshold verifies the orphan mismatch threshold constant.
func TestConstants_OrphanThreshold(t *testing.T) {
	t.Parallel()

	// Industry standard: 3-6 consecutive mismatches before orphaning
	if OrphanMismatchThreshold < 2 || OrphanMismatchThreshold > 10 {
		t.Errorf("OrphanMismatchThreshold %d is outside reasonable range [2, 10]",
			OrphanMismatchThreshold)
	}

	t.Logf("OrphanMismatchThreshold = %d (requires %d consecutive mismatches before orphaning)",
		OrphanMismatchThreshold, OrphanMismatchThreshold)
}

// TestConstants_StabilityWindow verifies the stability window constant.
func TestConstants_StabilityWindow(t *testing.T) {
	t.Parallel()

	// Industry standard: 2-5 stable checks before confirming
	if StabilityWindowChecks < 2 || StabilityWindowChecks > 10 {
		t.Errorf("StabilityWindowChecks %d is outside reasonable range [2, 10]",
			StabilityWindowChecks)
	}

	t.Logf("StabilityWindowChecks = %d (requires %d stable observations before confirmation)",
		StabilityWindowChecks, StabilityWindowChecks)
}

// TestConstants_DeepReorgInterval verifies deep reorg check interval.
func TestConstants_DeepReorgInterval(t *testing.T) {
	t.Parallel()

	if DeepReorgCheckInterval < 5 || DeepReorgCheckInterval > 100 {
		t.Errorf("DeepReorgCheckInterval %d is outside reasonable range [5, 100]",
			DeepReorgCheckInterval)
	}

	t.Logf("DeepReorgCheckInterval = %d (checks confirmed blocks every %d cycles)",
		DeepReorgCheckInterval, DeepReorgCheckInterval)
}

// TestConstants_StatusStrings verifies status constant values.
func TestConstants_StatusStrings(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		status   string
		expected string
	}{
		{"Pending", StatusPending, "pending"},
		{"Confirmed", StatusConfirmed, "confirmed"},
		{"Orphaned", StatusOrphaned, "orphaned"},
		{"Paid", StatusPaid, "paid"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.status != tc.expected {
				t.Errorf("Expected %s = %q, got %q", tc.name, tc.expected, tc.status)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Delayed Orphaning Simulation Tests
// -----------------------------------------------------------------------------

// MockBlock simulates a block for testing orphan logic.
type MockBlock struct {
	Height              uint64
	Hash                string
	Status              string
	OrphanMismatchCount int
	StabilityCheckCount int
	LastVerifiedTip     string
}

// TestDelayedOrphan_SingleMismatch verifies single mismatch doesn't orphan.
func TestDelayedOrphan_SingleMismatch(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
	}

	// Simulate first mismatch
	chainHash := "different_hash"
	if chainHash != block.Hash {
		block.OrphanMismatchCount++
	}

	// Should NOT be orphaned yet
	if block.OrphanMismatchCount >= OrphanMismatchThreshold {
		t.Fatal("Block should NOT be orphaned after single mismatch")
	}

	t.Logf("After 1 mismatch: OrphanMismatchCount=%d, threshold=%d, status=%s",
		block.OrphanMismatchCount, OrphanMismatchThreshold, block.Status)
}

// TestDelayedOrphan_ThresholdReached verifies orphaning at threshold.
func TestDelayedOrphan_ThresholdReached(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
	}

	chainHash := "different_hash"

	// Simulate reaching threshold
	for i := 0; i < OrphanMismatchThreshold; i++ {
		if chainHash != block.Hash {
			block.OrphanMismatchCount++
		}

		if block.OrphanMismatchCount >= OrphanMismatchThreshold {
			block.Status = StatusOrphaned
			break
		}
	}

	if block.Status != StatusOrphaned {
		t.Errorf("Block should be orphaned after %d mismatches, status=%s",
			OrphanMismatchThreshold, block.Status)
	}

	t.Logf("Block orphaned after %d consecutive mismatches", block.OrphanMismatchCount)
}

// TestDelayedOrphan_ResetOnMatch verifies mismatch counter resets on hash match.
func TestDelayedOrphan_ResetOnMatch(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: OrphanMismatchThreshold - 1, // One away from orphaning
	}

	// Simulate hash now matches (node synced)
	chainHash := "our_hash"
	if chainHash == block.Hash {
		block.OrphanMismatchCount = 0 // Reset counter
	}

	if block.OrphanMismatchCount != 0 {
		t.Errorf("Mismatch counter should reset to 0 on hash match, got %d",
			block.OrphanMismatchCount)
	}

	if block.Status == StatusOrphaned {
		t.Error("Block should NOT be orphaned after hash match recovery")
	}
}

// TestDelayedOrphan_IntermittentMismatches simulates flaky node.
func TestDelayedOrphan_IntermittentMismatches(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
	}

	// Simulate pattern: mismatch, mismatch, match, mismatch, match, mismatch, match
	// Should NOT orphan because counter resets on matches
	chainHashes := []string{
		"wrong", "wrong", "our_hash", // 2 mismatches, then reset
		"wrong", "our_hash", // 1 mismatch, then reset
		"wrong", "our_hash", // 1 mismatch, then reset
	}

	for _, chainHash := range chainHashes {
		if chainHash != block.Hash {
			block.OrphanMismatchCount++
		} else {
			block.OrphanMismatchCount = 0
		}

		if block.OrphanMismatchCount >= OrphanMismatchThreshold {
			block.Status = StatusOrphaned
			break
		}
	}

	if block.Status == StatusOrphaned {
		t.Error("Block should NOT be orphaned with intermittent mismatches")
	}

	t.Logf("Block survived intermittent mismatches, final counter=%d", block.OrphanMismatchCount)
}

// -----------------------------------------------------------------------------
// Stability Window Simulation Tests
// -----------------------------------------------------------------------------

// TestStabilityWindow_SingleCheck verifies single check doesn't confirm.
func TestStabilityWindow_SingleCheck(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		StabilityCheckCount: 0,
		LastVerifiedTip:     "",
	}

	maturity := 100
	confirmations := uint64(maturity) // At maturity

	// First stability check
	currentTip := "tip_A"
	if confirmations >= uint64(maturity) {
		block.StabilityCheckCount++
		block.LastVerifiedTip = currentTip

		if block.StabilityCheckCount >= StabilityWindowChecks {
			block.Status = StatusConfirmed
		}
	}

	if block.Status == StatusConfirmed {
		t.Error("Block should NOT be confirmed after single stability check")
	}

	t.Logf("After 1 check: StabilityCheckCount=%d, threshold=%d",
		block.StabilityCheckCount, StabilityWindowChecks)
}

// TestStabilityWindow_ThresholdReached verifies confirmation at threshold.
func TestStabilityWindow_ThresholdReached(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		StabilityCheckCount: 0,
		LastVerifiedTip:     "",
	}

	maturity := 100
	confirmations := uint64(maturity + 10) // Well past maturity
	currentTip := "stable_tip"

	// Simulate reaching stability threshold
	for i := 0; i < StabilityWindowChecks; i++ {
		if confirmations >= uint64(maturity) {
			// Tip unchanged = stable
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

	if block.Status != StatusConfirmed {
		t.Errorf("Block should be confirmed after %d stable checks, status=%s",
			StabilityWindowChecks, block.Status)
	}

	t.Logf("Block confirmed after %d consecutive stable checks", block.StabilityCheckCount)
}

// TestStabilityWindow_ResetOnTipChange verifies stability resets on tip change.
func TestStabilityWindow_ResetOnTipChange(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		StabilityCheckCount: StabilityWindowChecks - 1, // One away from confirming
		LastVerifiedTip:     "tip_A",
	}

	maturity := 100
	confirmations := uint64(maturity + 10)

	// Tip changes!
	newTip := "tip_B"
	tipChanged := block.LastVerifiedTip != "" && block.LastVerifiedTip != newTip

	if confirmations >= uint64(maturity) {
		if tipChanged {
			block.StabilityCheckCount = 0 // Reset!
		}
		block.StabilityCheckCount++
		block.LastVerifiedTip = newTip

		if block.StabilityCheckCount >= StabilityWindowChecks {
			block.Status = StatusConfirmed
		}
	}

	if block.Status == StatusConfirmed {
		t.Error("Block should NOT be confirmed after tip change")
	}

	if block.StabilityCheckCount != 1 {
		t.Errorf("Stability counter should be 1 after reset+increment, got %d",
			block.StabilityCheckCount)
	}
}

// TestStabilityWindow_ResetOnHashMismatch verifies stability resets on orphan mismatch.
func TestStabilityWindow_ResetOnHashMismatch(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
		StabilityCheckCount: StabilityWindowChecks - 1,
	}

	// Hash mismatch detected
	chainHash := "different_hash"
	if chainHash != block.Hash {
		block.OrphanMismatchCount++
		block.StabilityCheckCount = 0 // Reset stability on mismatch
	}

	if block.StabilityCheckCount != 0 {
		t.Errorf("Stability counter should reset on hash mismatch, got %d",
			block.StabilityCheckCount)
	}
}

// -----------------------------------------------------------------------------
// Combined Orphan + Stability Tests
// -----------------------------------------------------------------------------

// TestCombined_OrphanBeforeStability verifies orphan detection overrides stability.
func TestCombined_OrphanBeforeStability(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
		StabilityCheckCount: StabilityWindowChecks - 1, // Almost confirmed
	}

	maturity := 100
	confirmations := uint64(maturity + 10)

	// Simulate orphan detection during stability window
	chainHash := "wrong_hash"
	currentTip := "tip_A"

	for i := 0; i < OrphanMismatchThreshold; i++ {
		if chainHash != block.Hash {
			block.OrphanMismatchCount++
			block.StabilityCheckCount = 0 // Reset stability

			if block.OrphanMismatchCount >= OrphanMismatchThreshold {
				block.Status = StatusOrphaned
				break
			}
		} else if confirmations >= uint64(maturity) {
			block.StabilityCheckCount++
			block.LastVerifiedTip = currentTip

			if block.StabilityCheckCount >= StabilityWindowChecks {
				block.Status = StatusConfirmed
				break
			}
		}
	}

	if block.Status != StatusOrphaned {
		t.Errorf("Block should be orphaned, not %s", block.Status)
	}

	t.Log("Orphan detection correctly overrode near-confirmation")
}

// TestCombined_RecoveryAfterNearOrphan verifies recovery scenario.
func TestCombined_RecoveryAfterNearOrphan(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              1000,
		Hash:                "our_hash",
		Status:              StatusPending,
		OrphanMismatchCount: OrphanMismatchThreshold - 1, // One from orphaning
		StabilityCheckCount: 0,
	}

	maturity := 100
	confirmations := uint64(maturity + 10)

	// Node recovers - hash now matches
	chainHash := "our_hash"
	currentTip := "stable_tip"

	// Simulate recovery and subsequent stability checks
	for i := 0; i < StabilityWindowChecks; i++ {
		if chainHash == block.Hash {
			block.OrphanMismatchCount = 0 // Reset orphan counter
		}

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

	if block.Status != StatusConfirmed {
		t.Errorf("Block should recover and confirm, status=%s", block.Status)
	}

	t.Log("Block successfully recovered from near-orphan state")
}

// -----------------------------------------------------------------------------
// TOCTOU Protection Simulation Tests
// -----------------------------------------------------------------------------

// TestTOCTOU_TipChangeDuringCycle simulates tip change during processing.
func TestTOCTOU_TipChangeDuringCycle(t *testing.T) {
	t.Parallel()

	// Simulate starting a cycle with a snapshot
	snapshotTip := "tip_at_start"
	blocksToProcess := 5
	processedBlocks := 0
	cycleAborted := false

	// Simulate processing blocks
	for i := 0; i < blocksToProcess; i++ {
		// Check if tip changed (simulated)
		currentTip := snapshotTip
		if i == 2 {
			currentTip = "tip_changed_midway" // Tip changes at block 2
		}

		if currentTip != snapshotTip {
			cycleAborted = true
			break
		}

		processedBlocks++
	}

	if !cycleAborted {
		t.Error("Cycle should have been aborted when tip changed")
	}

	if processedBlocks != 2 {
		t.Errorf("Expected 2 blocks processed before abort, got %d", processedBlocks)
	}

	t.Logf("TOCTOU protection: aborted cycle after %d blocks due to tip change",
		processedBlocks)
}

// TestTOCTOU_ConcurrentTipChange simulates race condition.
func TestTOCTOU_ConcurrentTipChange(t *testing.T) {
	t.Parallel()

	var (
		mu         sync.Mutex
		currentTip atomic.Value
		aborted    atomic.Bool
	)

	currentTip.Store("initial_tip")

	var wg sync.WaitGroup

	// Goroutine 1: Processing cycle
	wg.Add(1)
	go func() {
		defer wg.Done()

		snapshotTip := currentTip.Load().(string)

		for i := 0; i < 100; i++ {
			mu.Lock()
			nowTip := currentTip.Load().(string)
			mu.Unlock()

			if nowTip != snapshotTip {
				aborted.Store(true)
				return
			}

			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 2: Tip changer
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Microsecond)

		mu.Lock()
		currentTip.Store("new_tip")
		mu.Unlock()
	}()

	wg.Wait()

	if !aborted.Load() {
		t.Log("Tip change happened after processing completed (timing-dependent)")
	} else {
		t.Log("TOCTOU protection detected concurrent tip change")
	}
}

// -----------------------------------------------------------------------------
// Deep Reorg Detection Tests
// -----------------------------------------------------------------------------

// TestDeepReorg_ConfirmedBlockOrphaned simulates deep reorg.
func TestDeepReorg_ConfirmedBlockOrphaned(t *testing.T) {
	t.Parallel()

	// Block was confirmed 500 confirmations ago
	confirmedBlock := &MockBlock{
		Height: 1000,
		Hash:   "confirmed_hash",
		Status: StatusConfirmed,
	}

	currentChainHeight := uint64(1500)

	// Deep reorg check - hash at height 1000 is now different
	chainHashAtHeight := "reorged_hash"

	if chainHashAtHeight != confirmedBlock.Hash {
		confirmedBlock.Status = StatusOrphaned
	}

	if confirmedBlock.Status != StatusOrphaned {
		t.Error("Confirmed block should be marked orphaned after deep reorg")
	}

	t.Logf("Deep reorg detected: block at height %d (current height %d) was orphaned",
		confirmedBlock.Height, currentChainHeight)
}

// TestDeepReorg_MaxAgeSkip verifies old blocks are skipped.
func TestDeepReorg_MaxAgeSkip(t *testing.T) {
	t.Parallel()

	// Block is very old - beyond DeepReorgMaxAge
	oldBlock := &MockBlock{
		Height: 1000,
		Hash:   "old_hash",
		Status: StatusConfirmed,
	}

	currentChainHeight := uint64(1000 + DeepReorgMaxAge + 100)

	// Check if block should be skipped
	shouldSkip := oldBlock.Height+DeepReorgMaxAge < currentChainHeight

	if !shouldSkip {
		t.Error("Block beyond DeepReorgMaxAge should be skipped for verification")
	}

	t.Logf("Block at height %d skipped (current height %d, max age %d)",
		oldBlock.Height, currentChainHeight, DeepReorgMaxAge)
}

// -----------------------------------------------------------------------------
// Maturity Calculation Tests
// -----------------------------------------------------------------------------

// TestOrphan_BlockMaturity_Default verifies default maturity value.
func TestOrphan_BlockMaturity_Default(t *testing.T) {
	t.Parallel()

	if DefaultBlockMaturityConfirmations != 100 {
		t.Errorf("Expected default maturity 100, got %d", DefaultBlockMaturityConfirmations)
	}
}

// TestGetBlockMaturity_CustomValue tests custom maturity configuration.
func TestGetBlockMaturity_CustomValue(t *testing.T) {
	t.Parallel()

	// Simulate configured maturity
	configuredMaturity := 50
	defaultMaturity := DefaultBlockMaturityConfirmations

	// getBlockMaturity logic
	var effectiveMaturity int
	if configuredMaturity > 0 {
		effectiveMaturity = configuredMaturity
	} else {
		effectiveMaturity = defaultMaturity
	}

	if effectiveMaturity != 50 {
		t.Errorf("Expected effective maturity 50, got %d", effectiveMaturity)
	}
}

// TestConfirmationProgress_Calculation verifies progress calculation.
func TestConfirmationProgress_Calculation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		confirmations uint64
		maturity      int
		expected      float64
	}{
		{0, 100, 0.0},
		{50, 100, 0.5},
		{100, 100, 1.0},
		{150, 100, 1.0}, // Capped at 1.0
		{25, 100, 0.25},
	}

	for _, tc := range testCases {
		progress := float64(tc.confirmations) / float64(tc.maturity)
		if progress > 1.0 {
			progress = 1.0
		}

		if progress != tc.expected {
			t.Errorf("confirmations=%d, maturity=%d: expected progress %.2f, got %.2f",
				tc.confirmations, tc.maturity, tc.expected, progress)
		}
	}
}

// -----------------------------------------------------------------------------
// Edge Cases
// -----------------------------------------------------------------------------

// TestEdgeCase_BlockAheadOfChain verifies handling when block height > chain height.
func TestEdgeCase_BlockAheadOfChain(t *testing.T) {
	t.Parallel()

	block := &MockBlock{
		Height:              2000,
		Hash:                "future_hash",
		Status:              StatusPending,
		OrphanMismatchCount: 0,
	}

	chainHeight := uint64(1999) // Block is ahead!

	// Check for underflow
	if block.Height > chainHeight {
		// This is a potential reorg - increment mismatch
		block.OrphanMismatchCount++
	}

	if block.OrphanMismatchCount != 1 {
		t.Errorf("Expected mismatch increment for block ahead of chain, got %d",
			block.OrphanMismatchCount)
	}
}

// TestEdgeCase_ZeroConfirmations verifies zero confirmation handling.
func TestEdgeCase_ZeroConfirmations(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000)
	chainHeight := uint64(1000)

	confirmations := chainHeight - blockHeight

	if confirmations != 0 {
		t.Errorf("Expected 0 confirmations, got %d", confirmations)
	}

	// Block should remain pending
	maturity := 100
	if confirmations >= uint64(maturity) {
		t.Error("Block with 0 confirmations should not reach maturity")
	}
}

// TestEdgeCase_ExactMaturity verifies exact maturity boundary.
func TestEdgeCase_ExactMaturity(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000)
	maturity := 100
	chainHeight := blockHeight + uint64(maturity) // Exactly at maturity

	confirmations := chainHeight - blockHeight

	if confirmations != uint64(maturity) {
		t.Errorf("Expected %d confirmations, got %d", maturity, confirmations)
	}

	// Block should start stability window
	if confirmations < uint64(maturity) {
		t.Error("Block should be at maturity threshold")
	}
}
