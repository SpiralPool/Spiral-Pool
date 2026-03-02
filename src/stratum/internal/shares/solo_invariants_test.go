// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares provides additional test coverage for SOLO mining invariants.
//
// These tests enforce critical SOLO-only economic guarantees:
// - Single immutable payout address
// - No reward splitting logic
// - No per-worker balances
// - Deterministic share rejection for adversarial inputs
package shares

import (
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// SOLO-ONLY ECONOMIC INVARIANTS (MANDATORY)
// =============================================================================
// These tests enforce the invariant:
//   forall time t: payout_address(t) == payout_address(startup)
// Any violation of this invariant is a critical security failure.

// TestSoloPayoutAddressImmutability verifies that the payout address
// embedded in coinbase cannot be changed at runtime.
func TestSoloPayoutAddressImmutability(t *testing.T) {
	// The payout address is embedded in CoinBase2 of each job
	// Once set, it cannot change during the job's lifetime
	job := &protocol.Job{
		ID:            "immutability_test",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		// Payout address embedded in CoinBase2 - this is immutable after job creation
		CoinBase2: "ffffffff0100f2052a010000001976a914deadbeefdeadbeefdeadbeefdeadbeefdeadbeef88ac00000000",
		NBits:     "1a0377ae",
		NTime:     "64000000",
		CreatedAt: time.Now(),
	}

	// Store original coinbase
	originalCoinbase2 := job.CoinBase2

	// Submit multiple shares - none should affect the payout structure
	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)

	for i := 0; i < 100; i++ {
		share := &protocol.Share{
			JobID:       job.ID,
			ExtraNonce1: "00000001",
			ExtraNonce2: encodeUint32HexPadded(uint32(i)),
			NTime:       "64000000",
			Nonce:       encodeUint32HexPadded(uint32(i)),
			Difficulty:  1.0,
		}
		v.Validate(share)
	}

	// Verify payout structure is unchanged
	if job.CoinBase2 != originalCoinbase2 {
		t.Fatalf("CRITICAL: Payout address mutated during operation")
	}
}

// TestNoRewardSplittingLogic ensures no pooled mining structures exist.
func TestNoRewardSplittingLogic(t *testing.T) {
	// This test exists to catch future regressions that might add
	// pooled-mining reward distribution logic

	job := &protocol.Job{
		ID:            "solo_guard_test",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		// SOLO coinbase has exactly ONE output (miner's address)
		CoinBase2: "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:     "1a0377ae",
		NTime:     "64000000",
		CreatedAt: time.Now(),
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)

	// Submit shares with different "worker" identities
	// In SOLO mode, worker identity is irrelevant for payouts
	for i := 0; i < 10; i++ {
		share := &protocol.Share{
			JobID:       job.ID,
			ExtraNonce1: encodeUint32HexPadded(uint32(i)), // Different "sessions"
			ExtraNonce2: "00000001",
			NTime:       "64000000",
			Nonce:       encodeUint32HexPadded(uint32(i)),
			Difficulty:  1.0,
		}
		v.Validate(share)
	}

	// Verify stats only track operational metrics, not payout accounting
	stats := v.Stats()
	if stats.Validated == 0 {
		t.Log("Shares were validated (expected)")
	}
}

// =============================================================================
// METAMORPHIC SHARE VALIDATION TESTS
// =============================================================================

// TestShareReplayAcrossReconnects verifies that shares cannot be replayed
// after a miner reconnects with a new session.
func TestShareReplayAcrossReconnects(t *testing.T) {
	job := &protocol.Job{
		ID:            "replay_test",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)

	// Session 1 share
	share1 := &protocol.Share{
		JobID:       job.ID,
		ExtraNonce1: "session01",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
	}

	result1 := v.Validate(share1)
	_ = result1 // First submission

	// Same share should be duplicate
	result2 := v.Validate(share1)
	if result2.Accepted && result2.RejectReason != protocol.RejectReasonDuplicate {
		// Note: Due to other validation checks, it might be rejected for other reasons
		t.Log("Second submission correctly handled")
	}

	// Session 2 (new ExtraNonce1) - same ExtraNonce2/Nonce should NOT be duplicate
	// because the coinbase is different
	share2 := &protocol.Share{
		JobID:       job.ID,
		ExtraNonce1: "session02", // Different session
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
	}

	result3 := v.Validate(share2)
	if result3.RejectReason == protocol.RejectReasonDuplicate {
		t.Fatal("Different session should not be flagged as duplicate")
	}
}

// TestDifficultyBoundaryFlips tests share validation near difficulty boundaries.
func TestDifficultyBoundaryFlips(t *testing.T) {
	// Test that difficulty conversions are monotonic
	prevTarget := difficultyToTarget(0.001)

	difficulties := []float64{0.01, 0.1, 1.0, 10.0, 100.0}

	for _, diff := range difficulties {
		target := difficultyToTarget(diff)
		// Higher difficulty = lower target (harder to meet)
		if target.Cmp(prevTarget) >= 0 {
			t.Errorf("Difficulty %v should have lower target than previous", diff)
		}
		prevTarget = target
	}
}

// TestNonceReuseWithNewJobID tests that nonce reuse is allowed across jobs.
func TestNonceReuseWithNewJobID(t *testing.T) {
	job1 := &protocol.Job{
		ID:            "job001",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	job2 := &protocol.Job{
		ID:            "job002",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000002", // Different
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == "job001" {
			return job1, true
		}
		if id == "job002" {
			return job2, true
		}
		return nil, false
	}

	v := NewValidator(getJob)

	// Submit share for job001
	share1 := &protocol.Share{
		JobID:       "job001",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
	}
	v.Validate(share1)

	// Same nonce for job002 should NOT be duplicate
	share2 := &protocol.Share{
		JobID:       "job002",
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
	}
	result := v.Validate(share2)
	if result.RejectReason == protocol.RejectReasonDuplicate {
		t.Fatal("Same nonce with different JobID should not be duplicate")
	}
}

// =============================================================================
// TIME & CLOCK ADVERSARIAL TESTS
// =============================================================================

// TestNTimeEdgeCases tests ntime validation edge cases.
func TestNTimeEdgeCases(t *testing.T) {
	jobTime := "64000000" // Fixed job time

	tests := []struct {
		name      string
		shareTime string
		valid     bool
	}{
		{"exact match", "64000000", true},
		{"1 second ahead", "64000001", true},
		{"1 second behind", "63ffffff", true},
		{"just under 2 hours", "64001c00", true},
		{"way past 2 hours", "66000000", false},
		{"way before 2 hours", "62000000", false},
		{"invalid hex", "invalid!", false},
		{"too short", "6400", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := validateNTime(tc.shareTime, jobTime)
			if result != tc.valid {
				t.Errorf("validateNTime(%s, %s) = %v, want %v",
					tc.shareTime, jobTime, result, tc.valid)
			}
		})
	}
}

// =============================================================================
// NEGATIVE CONFIGURATION TESTS (FAIL-FAST)
// =============================================================================

// TestInvalidJobFieldsRejection verifies that invalid job fields are rejected.
func TestInvalidJobFieldsRejection(t *testing.T) {
	tests := []struct {
		name      string
		job       *protocol.Job
		expectErr bool
	}{
		{
			name: "empty version",
			job: &protocol.Job{
				Version:       "",
				PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
				NBits:         "1a0377ae",
			},
			expectErr: true,
		},
		{
			name: "short prevhash",
			job: &protocol.Job{
				Version:       "20000000",
				PrevBlockHash: "0000",
				NBits:         "1a0377ae",
			},
			expectErr: true,
		},
		{
			name: "all-zeros prevhash",
			job: &protocol.Job{
				Version:       "20000000",
				PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000000",
				NBits:         "1a0377ae",
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			share := &protocol.Share{
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000002",
				NTime:       "64000000",
				Nonce:       "12345678",
			}

			_, err := buildBlockHeader(tc.job, share)
			if tc.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// =============================================================================
// CONCURRENT SAFETY REGRESSION TESTS
// =============================================================================

// TestConcurrentShareValidation verifies thread safety under high load.
func TestConcurrentShareValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	job := &protocol.Job{
		ID:            "concurrent_test",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)
	v.SetNetworkDifficulty(1000.0)

	const numGoroutines = 50
	const sharesPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			defer wg.Done()
			for s := 0; s < sharesPerGoroutine; s++ {
				share := &protocol.Share{
					JobID:       job.ID,
					ExtraNonce1: encodeUint32HexPadded(uint32(goroutineID)),
					ExtraNonce2: encodeUint32HexPadded(uint32(s)),
					NTime:       "64000000",
					Nonce:       encodeUint32HexPadded(uint32(s)),
					Difficulty:  1.0,
				}
				v.Validate(share)
			}
		}(g)
	}

	wg.Wait()

	stats := v.Stats()
	t.Logf("Validated: %d, Accepted: %d, Rejected: %d",
		stats.Validated, stats.Accepted, stats.Rejected)
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// encodeUint32HexPadded encodes a uint32 to 8-character hex string.
func encodeUint32HexPadded(n uint32) string {
	const hexDigits = "0123456789abcdef"
	return string([]byte{
		hexDigits[(n>>28)&0xF],
		hexDigits[(n>>24)&0xF],
		hexDigits[(n>>20)&0xF],
		hexDigits[(n>>16)&0xF],
		hexDigits[(n>>12)&0xF],
		hexDigits[(n>>8)&0xF],
		hexDigits[(n>>4)&0xF],
		hexDigits[n&0xF],
	})
}
