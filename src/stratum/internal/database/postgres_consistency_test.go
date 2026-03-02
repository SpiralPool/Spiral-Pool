// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package database

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Database Consistency Tests
// =============================================================================
// These tests verify database consistency for block tracking, including
// idempotent insertion, concurrent updates, and orphan/stability counter management.

// -----------------------------------------------------------------------------
// Mock Block for Testing
// -----------------------------------------------------------------------------

// MockDBBlock simulates a block record for testing without a real database.
type MockDBBlock struct {
	ID                   int64
	Height               uint64
	Hash                 string
	Status               string
	ConfirmationProgress float64
	OrphanMismatchCount  int
	StabilityCheckCount  int
	LastVerifiedTip      string
	mu                   sync.Mutex
}

// MockBlockStore simulates the database for testing.
type MockBlockStore struct {
	blocks map[uint64]*MockDBBlock
	mu     sync.RWMutex
}

// NewMockBlockStore creates a new mock block store.
func NewMockBlockStore() *MockBlockStore {
	return &MockBlockStore{
		blocks: make(map[uint64]*MockDBBlock),
	}
}

// InsertBlock simulates idempotent block insertion.
func (s *MockBlockStore) InsertBlock(block *MockDBBlock) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if block already exists (by hash)
	for _, existing := range s.blocks {
		if existing.Hash == block.Hash {
			return false // Already exists - idempotent
		}
	}

	s.blocks[block.Height] = block
	return true
}

// GetBlock retrieves a block by height.
func (s *MockBlockStore) GetBlock(height uint64) *MockDBBlock {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.blocks[height]
}

// UpdateBlockStatus updates block status.
func (s *MockBlockStore) UpdateBlockStatus(height uint64, status string, progress float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	block, exists := s.blocks[height]
	if !exists {
		return false
	}

	block.mu.Lock()
	defer block.mu.Unlock()
	block.Status = status
	block.ConfirmationProgress = progress
	return true
}

// UpdateBlockOrphanCount updates orphan mismatch counter.
func (s *MockBlockStore) UpdateBlockOrphanCount(height uint64, count int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	block, exists := s.blocks[height]
	if !exists {
		return false
	}

	block.mu.Lock()
	defer block.mu.Unlock()
	block.OrphanMismatchCount = count
	return true
}

// UpdateBlockStabilityCount updates stability counter and tip.
func (s *MockBlockStore) UpdateBlockStabilityCount(height uint64, count int, tip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	block, exists := s.blocks[height]
	if !exists {
		return false
	}

	block.mu.Lock()
	defer block.mu.Unlock()
	block.StabilityCheckCount = count
	block.LastVerifiedTip = tip
	return true
}

// -----------------------------------------------------------------------------
// Idempotent Insertion Tests
// -----------------------------------------------------------------------------

// TestDB_IdempotentBlockInsertion verifies blocks can only be inserted once.
func TestDB_IdempotentBlockInsertion(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height: 1000,
		Hash:   "hash_abc123",
		Status: "pending",
	}

	// First insertion should succeed
	if !store.InsertBlock(block) {
		t.Fatal("First insertion should succeed")
	}

	// Duplicate insertion should be rejected
	duplicate := &MockDBBlock{
		Height: 1000,
		Hash:   "hash_abc123", // Same hash
		Status: "pending",
	}

	if store.InsertBlock(duplicate) {
		t.Fatal("Duplicate insertion should be rejected")
	}

	t.Log("Idempotent insertion working correctly")
}

// TestDB_ConcurrentBlockInsertion tests concurrent insertion of same block.
func TestDB_ConcurrentBlockInsertion(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	var wg sync.WaitGroup
	var successCount atomic.Int32
	const numGoroutines = 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			block := &MockDBBlock{
				Height: 1000,
				Hash:   "hash_same_block",
				Status: "pending",
			}

			if store.InsertBlock(block) {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if successCount.Load() != 1 {
		t.Errorf("Expected exactly 1 successful insertion, got %d", successCount.Load())
	}

	t.Logf("Concurrent insertion: %d attempts, %d successful", numGoroutines, successCount.Load())
}

// TestDB_DifferentHashesSameHeight tests different hashes at same height.
func TestDB_DifferentHashesSameHeight(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block1 := &MockDBBlock{
		Height: 1000,
		Hash:   "hash_A",
		Status: "pending",
	}

	block2 := &MockDBBlock{
		Height: 1000, // Same height
		Hash:   "hash_B", // Different hash
		Status: "pending",
	}

	// First should succeed
	if !store.InsertBlock(block1) {
		t.Fatal("First block insertion should succeed")
	}

	// Second has different hash - behavior depends on uniqueness constraint
	// In real DB, this might fail due to unique height constraint
	// Our mock allows it since hash is different
	inserted := store.InsertBlock(block2)
	t.Logf("Second block (different hash, same height) inserted: %v", inserted)
}

// -----------------------------------------------------------------------------
// Orphan Counter Tests
// -----------------------------------------------------------------------------

// TestDB_OrphanCounterIncrement verifies orphan counter incrementing.
func TestDB_OrphanCounterIncrement(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height:              1000,
		Hash:                "hash_test",
		Status:              "pending",
		OrphanMismatchCount: 0,
	}

	store.InsertBlock(block)

	// Increment counter
	for i := 1; i <= 5; i++ {
		if !store.UpdateBlockOrphanCount(1000, i) {
			t.Fatalf("Failed to update orphan count at iteration %d", i)
		}

		retrieved := store.GetBlock(1000)
		if retrieved.OrphanMismatchCount != i {
			t.Errorf("Expected orphan count %d, got %d", i, retrieved.OrphanMismatchCount)
		}
	}
}

// TestDB_OrphanCounterReset verifies orphan counter reset.
func TestDB_OrphanCounterReset(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height:              1000,
		Hash:                "hash_test",
		Status:              "pending",
		OrphanMismatchCount: 5,
	}

	store.InsertBlock(block)

	// Reset counter
	if !store.UpdateBlockOrphanCount(1000, 0) {
		t.Fatal("Failed to reset orphan count")
	}

	retrieved := store.GetBlock(1000)
	if retrieved.OrphanMismatchCount != 0 {
		t.Errorf("Expected orphan count 0 after reset, got %d", retrieved.OrphanMismatchCount)
	}
}

// TestDB_ConcurrentOrphanCounterUpdate tests concurrent counter updates.
func TestDB_ConcurrentOrphanCounterUpdate(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height:              1000,
		Hash:                "hash_test",
		Status:              "pending",
		OrphanMismatchCount: 0,
	}

	store.InsertBlock(block)

	var wg sync.WaitGroup
	const numUpdates = 100

	for i := 0; i < numUpdates; i++ {
		wg.Add(1)
		go func(count int) {
			defer wg.Done()
			store.UpdateBlockOrphanCount(1000, count)
		}(i)
	}

	wg.Wait()

	// Final value should be one of the values written
	retrieved := store.GetBlock(1000)
	if retrieved.OrphanMismatchCount < 0 || retrieved.OrphanMismatchCount >= numUpdates {
		t.Errorf("Orphan count %d is outside expected range [0, %d)",
			retrieved.OrphanMismatchCount, numUpdates)
	}

	t.Logf("After %d concurrent updates, orphan count = %d",
		numUpdates, retrieved.OrphanMismatchCount)
}

// -----------------------------------------------------------------------------
// Stability Counter Tests
// -----------------------------------------------------------------------------

// TestDB_StabilityCounterIncrement verifies stability counter incrementing.
func TestDB_StabilityCounterIncrement(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height:              1000,
		Hash:                "hash_test",
		Status:              "pending",
		StabilityCheckCount: 0,
	}

	store.InsertBlock(block)

	tips := []string{"tip_A", "tip_A", "tip_A"} // Same tip for stability

	for i, tip := range tips {
		if !store.UpdateBlockStabilityCount(1000, i+1, tip) {
			t.Fatalf("Failed to update stability count at iteration %d", i)
		}

		retrieved := store.GetBlock(1000)
		if retrieved.StabilityCheckCount != i+1 {
			t.Errorf("Expected stability count %d, got %d", i+1, retrieved.StabilityCheckCount)
		}
		if retrieved.LastVerifiedTip != tip {
			t.Errorf("Expected last tip %s, got %s", tip, retrieved.LastVerifiedTip)
		}
	}
}

// TestDB_StabilityCounterResetOnTipChange verifies stability resets on tip change.
func TestDB_StabilityCounterResetOnTipChange(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height:              1000,
		Hash:                "hash_test",
		Status:              "pending",
		StabilityCheckCount: 2,
		LastVerifiedTip:     "tip_A",
	}

	store.InsertBlock(block)

	// Tip changes - simulate reset and re-increment
	newTip := "tip_B"
	if !store.UpdateBlockStabilityCount(1000, 1, newTip) { // Reset to 1
		t.Fatal("Failed to reset stability count")
	}

	retrieved := store.GetBlock(1000)
	if retrieved.StabilityCheckCount != 1 {
		t.Errorf("Expected stability count 1 after tip change, got %d", retrieved.StabilityCheckCount)
	}
	if retrieved.LastVerifiedTip != newTip {
		t.Errorf("Expected new tip %s, got %s", newTip, retrieved.LastVerifiedTip)
	}
}

// -----------------------------------------------------------------------------
// Status Transition Tests
// -----------------------------------------------------------------------------

// TestDB_StatusTransition_PendingToConfirmed tests valid status transition.
func TestDB_StatusTransition_PendingToConfirmed(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height:               1000,
		Hash:                 "hash_test",
		Status:               "pending",
		ConfirmationProgress: 0.5,
	}

	store.InsertBlock(block)

	// Transition to confirmed
	if !store.UpdateBlockStatus(1000, "confirmed", 1.0) {
		t.Fatal("Failed to update status")
	}

	retrieved := store.GetBlock(1000)
	if retrieved.Status != "confirmed" {
		t.Errorf("Expected status confirmed, got %s", retrieved.Status)
	}
	if retrieved.ConfirmationProgress != 1.0 {
		t.Errorf("Expected progress 1.0, got %f", retrieved.ConfirmationProgress)
	}
}

// TestDB_StatusTransition_PendingToOrphaned tests orphan transition.
func TestDB_StatusTransition_PendingToOrphaned(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height: 1000,
		Hash:   "hash_test",
		Status: "pending",
	}

	store.InsertBlock(block)

	// Transition to orphaned
	if !store.UpdateBlockStatus(1000, "orphaned", 0) {
		t.Fatal("Failed to update status")
	}

	retrieved := store.GetBlock(1000)
	if retrieved.Status != "orphaned" {
		t.Errorf("Expected status orphaned, got %s", retrieved.Status)
	}
}

// TestDB_StatusTransition_ConfirmedToOrphaned tests deep reorg transition.
func TestDB_StatusTransition_ConfirmedToOrphaned(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height: 1000,
		Hash:   "hash_test",
		Status: "confirmed",
	}

	store.InsertBlock(block)

	// Deep reorg - confirmed to orphaned
	if !store.UpdateBlockStatus(1000, "orphaned", 0) {
		t.Fatal("Failed to update status")
	}

	retrieved := store.GetBlock(1000)
	if retrieved.Status != "orphaned" {
		t.Errorf("Expected status orphaned after deep reorg, got %s", retrieved.Status)
	}

	t.Log("Deep reorg status transition working correctly")
}

// -----------------------------------------------------------------------------
// Concurrent Status Update Tests
// -----------------------------------------------------------------------------

// TestDB_ConcurrentStatusUpdates tests concurrent status updates.
func TestDB_ConcurrentStatusUpdates(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	block := &MockDBBlock{
		Height: 1000,
		Hash:   "hash_test",
		Status: "pending",
	}

	store.InsertBlock(block)

	var wg sync.WaitGroup
	statuses := []string{"pending", "confirmed", "orphaned"}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			status := statuses[idx%len(statuses)]
			store.UpdateBlockStatus(1000, status, float64(idx)/100)
		}(i)
	}

	wg.Wait()

	// Final status should be one of the valid statuses
	retrieved := store.GetBlock(1000)
	validStatus := false
	for _, s := range statuses {
		if retrieved.Status == s {
			validStatus = true
			break
		}
	}

	if !validStatus {
		t.Errorf("Final status %s is not a valid status", retrieved.Status)
	}

	t.Logf("After concurrent updates, status = %s", retrieved.Status)
}

// -----------------------------------------------------------------------------
// Progress Calculation Tests
// -----------------------------------------------------------------------------

// TestDB_ProgressCalculation tests confirmation progress values.
func TestDB_ProgressCalculation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		confirmations uint64
		maturity      uint64
		expected      float64
	}{
		{0, 100, 0.0},
		{50, 100, 0.5},
		{100, 100, 1.0},
		{150, 100, 1.0}, // Capped
		{33, 100, 0.33},
	}

	for _, tc := range testCases {
		progress := float64(tc.confirmations) / float64(tc.maturity)
		if progress > 1.0 {
			progress = 1.0
		}

		// Allow small floating point variance
		if progress < tc.expected-0.01 || progress > tc.expected+0.01 {
			t.Errorf("confirmations=%d, maturity=%d: expected ~%.2f, got %.2f",
				tc.confirmations, tc.maturity, tc.expected, progress)
		}
	}
}

// -----------------------------------------------------------------------------
// Edge Cases
// -----------------------------------------------------------------------------

// TestDB_UpdateNonexistentBlock tests updating a block that doesn't exist.
func TestDB_UpdateNonexistentBlock(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	// Try to update non-existent block
	if store.UpdateBlockStatus(9999, "confirmed", 1.0) {
		t.Error("Should not be able to update non-existent block")
	}

	if store.UpdateBlockOrphanCount(9999, 5) {
		t.Error("Should not be able to update orphan count for non-existent block")
	}

	if store.UpdateBlockStabilityCount(9999, 3, "tip") {
		t.Error("Should not be able to update stability count for non-existent block")
	}
}

// TestDB_LargeBlockHeight tests large block heights.
func TestDB_LargeBlockHeight(t *testing.T) {
	t.Parallel()
	store := NewMockBlockStore()

	// Very large block height
	largeHeight := uint64(1 << 30) // ~1 billion

	block := &MockDBBlock{
		Height: largeHeight,
		Hash:   "hash_large",
		Status: "pending",
	}

	if !store.InsertBlock(block) {
		t.Fatal("Failed to insert block with large height")
	}

	retrieved := store.GetBlock(largeHeight)
	if retrieved == nil || retrieved.Height != largeHeight {
		t.Error("Failed to retrieve block with large height")
	}
}

// -----------------------------------------------------------------------------
// Stress Tests
// -----------------------------------------------------------------------------

// TestDB_Stress_ManyBlocks tests inserting many blocks.
func TestDB_Stress_ManyBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	store := NewMockBlockStore()
	const numBlocks = 10000

	start := time.Now()

	for i := uint64(0); i < numBlocks; i++ {
		block := &MockDBBlock{
			Height: i,
			Hash:   "hash_" + string(rune('A'+i%26)),
			Status: "pending",
		}
		store.InsertBlock(block)
	}

	elapsed := time.Since(start)
	t.Logf("Inserted %d blocks in %v (%.0f blocks/sec)",
		numBlocks, elapsed, float64(numBlocks)/elapsed.Seconds())
}

// TestDB_Stress_ConcurrentOperations tests many concurrent operations.
func TestDB_Stress_ConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	store := NewMockBlockStore()

	// Pre-populate
	for i := uint64(0); i < 100; i++ {
		store.InsertBlock(&MockDBBlock{
			Height: i,
			Hash:   "hash_" + string(rune('A'+i%26)),
			Status: "pending",
		})
	}

	var wg sync.WaitGroup
	var operations atomic.Int64

	// Concurrent readers, writers, updaters
	for i := 0; i < 20; i++ {
		wg.Add(3)

		// Reader
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = store.GetBlock(uint64(j % 100))
				operations.Add(1)
			}
		}()

		// Status updater
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				store.UpdateBlockStatus(uint64(j%100), "confirmed", 1.0)
				operations.Add(1)
			}
		}()

		// Counter updater
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				store.UpdateBlockOrphanCount(uint64(j%100), j%10)
				store.UpdateBlockStabilityCount(uint64(j%100), j%5, "tip")
				operations.Add(2)
			}
		}()
	}

	wg.Wait()
	t.Logf("Completed %d concurrent operations", operations.Load())
}
