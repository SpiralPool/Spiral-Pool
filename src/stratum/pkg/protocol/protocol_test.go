// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package protocol provides tests for the Stratum protocol abstraction layer.
package protocol

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// SESSION TESTS
// =============================================================================

func TestSession_SetAuthorized(t *testing.T) {
	s := &Session{}

	// Initially not authorized
	if s.IsAuthorized() {
		t.Error("Session should not be authorized initially")
	}

	// Set authorized
	s.SetAuthorized(true)
	if !s.IsAuthorized() {
		t.Error("Session should be authorized after SetAuthorized(true)")
	}

	// Set unauthorized
	s.SetAuthorized(false)
	if s.IsAuthorized() {
		t.Error("Session should not be authorized after SetAuthorized(false)")
	}
}

func TestSession_SetSubscribed(t *testing.T) {
	s := &Session{}

	// Initially not subscribed
	if s.IsSubscribed() {
		t.Error("Session should not be subscribed initially")
	}

	// Set subscribed
	s.SetSubscribed(true)
	if !s.IsSubscribed() {
		t.Error("Session should be subscribed after SetSubscribed(true)")
	}

	// Set unsubscribed
	s.SetSubscribed(false)
	if s.IsSubscribed() {
		t.Error("Session should not be subscribed after SetSubscribed(false)")
	}
}

func TestSession_SetDiffSent(t *testing.T) {
	s := &Session{}

	// Initially not sent
	if s.IsDiffSent() {
		t.Error("DiffSent should be false initially")
	}

	// First call should succeed
	if !s.SetDiffSent() {
		t.Error("First SetDiffSent should return true")
	}

	if !s.IsDiffSent() {
		t.Error("IsDiffSent should be true after SetDiffSent")
	}

	// Second call should fail (already set)
	if s.SetDiffSent() {
		t.Error("Second SetDiffSent should return false")
	}
}

func TestSession_Difficulty(t *testing.T) {
	s := &Session{}

	// Initial difficulty is 0
	if s.GetDifficulty() != 0 {
		t.Error("Initial difficulty should be 0")
	}

	// Set difficulty
	s.SetDifficulty(100.5)
	if s.GetDifficulty() != 100.5 {
		t.Errorf("Expected difficulty 100.5, got %f", s.GetDifficulty())
	}

	// Set very small difficulty
	s.SetDifficulty(0.000001)
	if s.GetDifficulty() != 0.000001 {
		t.Errorf("Expected difficulty 0.000001, got %f", s.GetDifficulty())
	}

	// Set very large difficulty
	s.SetDifficulty(1e15)
	if s.GetDifficulty() != 1e15 {
		t.Errorf("Expected difficulty 1e15, got %f", s.GetDifficulty())
	}
}

func TestSession_LastActivity(t *testing.T) {
	s := &Session{}

	// Initial value is zero time
	if !s.GetLastActivity().IsZero() {
		t.Error("Initial LastActivity should be zero")
	}

	// Set activity
	now := time.Now()
	s.SetLastActivity(now)

	got := s.GetLastActivity()
	if got.UnixNano() != now.UnixNano() {
		t.Errorf("LastActivity mismatch: got %v, want %v", got, now)
	}
}

func TestSession_ShareCount(t *testing.T) {
	s := &Session{}

	// Initial count is 0
	if s.GetShareCount() != 0 {
		t.Error("Initial share count should be 0")
	}

	// Increment
	for i := uint64(1); i <= 100; i++ {
		count := s.IncrementShareCount()
		if count != i {
			t.Errorf("IncrementShareCount returned %d, expected %d", count, i)
		}
	}

	if s.GetShareCount() != 100 {
		t.Errorf("Final share count should be 100, got %d", s.GetShareCount())
	}
}

func TestSession_ValidShares(t *testing.T) {
	s := &Session{}

	if s.GetValidShares() != 0 {
		t.Error("Initial valid shares should be 0")
	}

	s.IncrementValidShares()
	s.IncrementValidShares()
	s.IncrementValidShares()

	if s.GetValidShares() != 3 {
		t.Errorf("Expected 3 valid shares, got %d", s.GetValidShares())
	}
}

func TestSession_InvalidShares(t *testing.T) {
	s := &Session{}

	if s.GetInvalidShares() != 0 {
		t.Error("Initial invalid shares should be 0")
	}

	s.IncrementInvalidShares()
	s.IncrementInvalidShares()

	if s.GetInvalidShares() != 2 {
		t.Errorf("Expected 2 invalid shares, got %d", s.GetInvalidShares())
	}
}

func TestSession_StaleShares(t *testing.T) {
	s := &Session{}

	if s.GetStaleShares() != 0 {
		t.Error("Initial stale shares should be 0")
	}

	s.IncrementStaleShares()

	if s.GetStaleShares() != 1 {
		t.Errorf("Expected 1 stale share, got %d", s.GetStaleShares())
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestSession_ConcurrentAuthorization(t *testing.T) {
	s := &Session{}

	var wg sync.WaitGroup
	const numGoroutines = 100

	// Concurrent setters
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(val bool) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.SetAuthorized(val)
			}
		}(i%2 == 0)
	}

	// Concurrent readers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = s.IsAuthorized()
			}
		}()
	}

	wg.Wait()
	// Test passes if no race conditions or panics occur
}

func TestSession_ConcurrentSubscription(t *testing.T) {
	s := &Session{}

	var wg sync.WaitGroup
	const numGoroutines = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(2)
		go func(val bool) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.SetSubscribed(val)
			}
		}(i%2 == 0)

		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = s.IsSubscribed()
			}
		}()
	}

	wg.Wait()
}

func TestSession_ConcurrentDifficulty(t *testing.T) {
	s := &Session{}

	var wg sync.WaitGroup
	const numGoroutines = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(2)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.SetDifficulty(float64(id*100 + j))
			}
		}(i)

		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				diff := s.GetDifficulty()
				if diff < 0 {
					t.Errorf("Negative difficulty: %f", diff)
				}
			}
		}()
	}

	wg.Wait()
}

func TestSession_ConcurrentShareCount(t *testing.T) {
	s := &Session{}

	var wg sync.WaitGroup
	const numGoroutines = 100
	const incrementsPerGoroutine = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < incrementsPerGoroutine; j++ {
				s.IncrementShareCount()
			}
		}()
	}

	wg.Wait()

	expected := uint64(numGoroutines * incrementsPerGoroutine)
	if s.GetShareCount() != expected {
		t.Errorf("Expected share count %d, got %d", expected, s.GetShareCount())
	}
}

func TestSession_ConcurrentDiffSent(t *testing.T) {
	s := &Session{}

	var wg sync.WaitGroup
	var winners atomic.Int64
	const numGoroutines = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.SetDiffSent() {
				winners.Add(1)
			}
		}()
	}

	wg.Wait()

	// Exactly one goroutine should win
	if winners.Load() != 1 {
		t.Errorf("Expected exactly 1 winner, got %d", winners.Load())
	}

	if !s.IsDiffSent() {
		t.Error("DiffSent should be true")
	}
}

func TestSession_ConcurrentMixedOperations(t *testing.T) {
	s := &Session{}

	var wg sync.WaitGroup
	const numGoroutines = 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				switch j % 10 {
				case 0:
					s.SetAuthorized(true)
				case 1:
					s.SetAuthorized(false)
				case 2:
					s.IsAuthorized()
				case 3:
					s.SetSubscribed(true)
				case 4:
					s.IsSubscribed()
				case 5:
					s.SetDifficulty(float64(id))
				case 6:
					s.GetDifficulty()
				case 7:
					s.IncrementShareCount()
				case 8:
					s.IncrementValidShares()
				case 9:
					s.SetLastActivity(time.Now())
				}
			}
		}(i)
	}

	wg.Wait()
	// Test passes if no race conditions
}

// =============================================================================
// JOB AND SHARE TESTS
// =============================================================================

func TestJob_Fields(t *testing.T) {
	job := &Job{
		ID:             "job123",
		PrevBlockHash:  "0000000000000000000abc",
		CoinBase1:      "01000000010000",
		CoinBase2:      "ffffffff",
		MerkleBranches: []string{"branch1", "branch2"},
		Version:        "20000000",
		NBits:          "1d00ffff",
		NTime:          "12345678",
		CleanJobs:      true,
		Height:         100000,
		Difficulty:     1000.0,
	}

	if job.ID != "job123" {
		t.Error("Job ID mismatch")
	}

	if len(job.MerkleBranches) != 2 {
		t.Error("Merkle branches count mismatch")
	}

	if !job.CleanJobs {
		t.Error("CleanJobs should be true")
	}
}

func TestShare_Fields(t *testing.T) {
	share := &Share{
		SessionID:    12345,
		JobID:        "job123",
		MinerAddress: "DGB1234567890",
		WorkerName:   "worker1",
		ExtraNonce1:  "12345678",
		ExtraNonce2:  "00000001",
		NTime:        "12345678",
		Nonce:        "deadbeef",
		VersionBits:  0x20000000,
		Difficulty:   100.0,
		IsBlock:      false,
		SubmittedAt:  time.Now(),
	}

	if share.SessionID != 12345 {
		t.Error("Share SessionID mismatch")
	}

	if share.ExtraNonce1 != "12345678" {
		t.Error("Share ExtraNonce1 mismatch")
	}
}

func TestShareResult_Fields(t *testing.T) {
	result := &ShareResult{
		Accepted:     true,
		IsBlock:      false,
		RejectReason: RejectReasonNone,
	}

	if !result.Accepted {
		t.Error("ShareResult should be accepted")
	}

	if result.IsBlock {
		t.Error("ShareResult should not be a block")
	}

	// Test reject reasons
	result.Accepted = false
	result.RejectReason = RejectReasonDuplicate
	if result.RejectReason != "duplicate" {
		t.Error("RejectReason mismatch")
	}
}

func TestRejectReasons(t *testing.T) {
	reasons := map[string]string{
		RejectReasonNone:                  "",
		RejectReasonDuplicate:             "duplicate",
		RejectReasonLowDifficulty:         "low-difficulty",
		RejectReasonStale:                 "stale",
		RejectReasonInvalidJob:            "job-not-found",
		RejectReasonInvalidNonce:          "invalid-nonce",
		RejectReasonInvalidTime:           "invalid-time",
		RejectReasonInvalidSolution:       "invalid-solution",
		RejectReasonInvalidVersionRolling: "invalid-version-rolling",
	}

	for constant, expected := range reasons {
		if constant != expected {
			t.Errorf("Reject reason %s != %s", constant, expected)
		}
	}
}

func TestMethods(t *testing.T) {
	if Methods.Subscribe != "mining.subscribe" {
		t.Error("Methods.Subscribe mismatch")
	}
	if Methods.Authorize != "mining.authorize" {
		t.Error("Methods.Authorize mismatch")
	}
	if Methods.Submit != "mining.submit" {
		t.Error("Methods.Submit mismatch")
	}
	if Methods.Notify != "mining.notify" {
		t.Error("Methods.Notify mismatch")
	}
	if Methods.SetDifficulty != "mining.set_difficulty" {
		t.Error("Methods.SetDifficulty mismatch")
	}
	if Methods.Reconnect != "client.reconnect" {
		t.Error("Methods.Reconnect mismatch")
	}
	if Methods.GetVersion != "client.get_version" {
		t.Error("Methods.GetVersion mismatch")
	}
}

// =============================================================================
// EDGE CASES
// =============================================================================

func TestSession_ZeroValues(t *testing.T) {
	s := &Session{}

	// All atomic values should be readable even without initialization
	if s.GetDifficulty() != 0 {
		t.Error("Uninitialized difficulty should be 0")
	}

	if s.GetShareCount() != 0 {
		t.Error("Uninitialized share count should be 0")
	}

	if s.GetValidShares() != 0 {
		t.Error("Uninitialized valid shares should be 0")
	}

	if !s.GetLastActivity().IsZero() {
		t.Error("Uninitialized LastActivity should be zero time")
	}
}

func TestSession_LargeDifficulty(t *testing.T) {
	s := &Session{}

	// Test with very large difficulty (network diff)
	largeDiff := 1e18
	s.SetDifficulty(largeDiff)

	if s.GetDifficulty() != largeDiff {
		t.Errorf("Large difficulty mismatch: got %e", s.GetDifficulty())
	}
}

func TestSession_SmallDifficulty(t *testing.T) {
	s := &Session{}

	// Test with very small difficulty
	smallDiff := 1e-10
	s.SetDifficulty(smallDiff)

	if s.GetDifficulty() != smallDiff {
		t.Errorf("Small difficulty mismatch: got %e", s.GetDifficulty())
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkSession_IsAuthorized(b *testing.B) {
	s := &Session{}
	s.SetAuthorized(true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.IsAuthorized()
	}
}

func BenchmarkSession_SetDifficulty(b *testing.B) {
	s := &Session{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.SetDifficulty(float64(i))
	}
}

func BenchmarkSession_IncrementShareCount(b *testing.B) {
	s := &Session{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.IncrementShareCount()
	}
}

func BenchmarkSession_ConcurrentIncrementShareCount(b *testing.B) {
	s := &Session{}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.IncrementShareCount()
		}
	})
}

// =============================================================================
// JOB CLONE DEEP COPY TESTS
// =============================================================================

// TestJob_Clone_DeepCopySlices verifies that Clone() produces a fully
// independent copy — mutating slices on the original must not affect the clone.
func TestJob_Clone_DeepCopySlices(t *testing.T) {
	original := &Job{
		ID:              "test-job",
		MerkleBranches:  []string{"aaa", "bbb", "ccc"},
		Target:          []byte{0x00, 0xff, 0x01},
		TransactionData: []string{"tx1", "tx2"},
		AuxMerkleBranch: []string{"aux1", "aux2"},
		AuxBlocks: []AuxBlockData{
			{
				Symbol:        "DOGE",
				ChainID:       98,
				Hash:          []byte{0x01, 0x02},
				Target:        []byte{0x03, 0x04},
				Height:        100,
				CoinbaseValue: 500000000,
				ChainIndex:    3,
				Difficulty:    1234.5,
				Bits:          0x1d00ffff,
			},
		},
		NetworkTarget: "00000000ffff",
	}

	clone := original.Clone()

	// Verify values match
	if clone.ID != original.ID {
		t.Errorf("Clone ID mismatch: got %q, want %q", clone.ID, original.ID)
	}
	if len(clone.MerkleBranches) != len(original.MerkleBranches) {
		t.Fatalf("MerkleBranches length mismatch: got %d, want %d", len(clone.MerkleBranches), len(original.MerkleBranches))
	}

	// Verify AuxBlockData value fields are copied
	if clone.AuxBlocks[0].CoinbaseValue != 500000000 {
		t.Errorf("Clone AuxBlocks[0].CoinbaseValue not copied: got %d, want 500000000", clone.AuxBlocks[0].CoinbaseValue)
	}
	if clone.AuxBlocks[0].ChainIndex != 3 {
		t.Errorf("Clone AuxBlocks[0].ChainIndex not copied: got %d, want 3", clone.AuxBlocks[0].ChainIndex)
	}
	if clone.AuxBlocks[0].Difficulty != 1234.5 {
		t.Errorf("Clone AuxBlocks[0].Difficulty not copied: got %f, want 1234.5", clone.AuxBlocks[0].Difficulty)
	}
	if clone.AuxBlocks[0].Bits != 0x1d00ffff {
		t.Errorf("Clone AuxBlocks[0].Bits not copied: got %d, want %d", clone.AuxBlocks[0].Bits, uint32(0x1d00ffff))
	}

	// Mutate original slices — clone must not be affected
	original.MerkleBranches[0] = "MUTATED"
	if clone.MerkleBranches[0] == "MUTATED" {
		t.Error("Clone MerkleBranches shares backing array with original — shallow copy bug")
	}

	original.Target[0] = 0xDE
	if clone.Target[0] == 0xDE {
		t.Error("Clone Target shares backing array with original — shallow copy bug")
	}

	original.TransactionData[0] = "MUTATED_TX"
	if clone.TransactionData[0] == "MUTATED_TX" {
		t.Error("Clone TransactionData shares backing array with original — shallow copy bug")
	}

	original.AuxMerkleBranch[0] = "MUTATED_AUX"
	if clone.AuxMerkleBranch[0] == "MUTATED_AUX" {
		t.Error("Clone AuxMerkleBranch shares backing array with original — shallow copy bug")
	}

	original.AuxBlocks[0].Hash[0] = 0xFF
	if clone.AuxBlocks[0].Hash[0] == 0xFF {
		t.Error("Clone AuxBlocks[0].Hash shares backing array with original — shallow copy bug")
	}

	original.AuxBlocks[0].Target[0] = 0xEE
	if clone.AuxBlocks[0].Target[0] == 0xEE {
		t.Error("Clone AuxBlocks[0].Target shares backing array with original — shallow copy bug")
	}
}

// TestJob_Clone_NilSlices verifies Clone() handles nil slices gracefully.
func TestJob_Clone_NilSlices(t *testing.T) {
	original := &Job{
		ID:   "nil-test",
		NBits: "1d00ffff",
	}

	clone := original.Clone()

	if clone.ID != "nil-test" {
		t.Errorf("Clone ID = %q, want %q", clone.ID, "nil-test")
	}
	if clone.MerkleBranches != nil {
		t.Error("Clone should have nil MerkleBranches when original is nil")
	}
	if clone.Target != nil {
		t.Error("Clone should have nil Target when original is nil")
	}
	if clone.AuxBlocks != nil {
		t.Error("Clone should have nil AuxBlocks when original is nil")
	}
}
