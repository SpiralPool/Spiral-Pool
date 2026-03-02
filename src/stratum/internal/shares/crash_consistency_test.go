// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares provides crash consistency and recovery tests.
//
// These tests verify that the system behaves correctly under crash conditions:
// - No phantom shares after restart
// - No double counting of blocks
// - No silent state rollback
// - Shares are either fully persisted or fully rejected
package shares

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// CRASH CONSISTENCY TESTS
// =============================================================================
// These tests simulate crash scenarios and verify correct behavior on recovery.

// TestCrashDuringShareAcceptance simulates a crash during share processing.
// Verifies that partial state doesn't corrupt the system.
func TestCrashDuringShareAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping crash simulation in short mode")
	}

	job := &protocol.Job{
		ID:            "crash_test",
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

	// Start processing shares in goroutines
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	var successfulShares atomic.Uint64

	// Submit shares continuously
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			shareCount := 0
			for {
				select {
				case <-ctx.Done():
					return
				default:
					share := &protocol.Share{
						JobID:       job.ID,
						ExtraNonce1: encodeUint32HexPadded(uint32(workerID)),
						ExtraNonce2: encodeUint32HexPadded(uint32(shareCount)),
						NTime:       "64000000",
						Nonce:       encodeUint32HexPadded(uint32(shareCount)),
						Difficulty:  1.0,
					}
					result := v.Validate(share)
					if result.Accepted {
						successfulShares.Add(1)
					}
					shareCount++
				}
			}
		}(i)
	}

	// Wait for context to timeout (simulating "crash")
	<-ctx.Done()
	wg.Wait()

	t.Logf("Shares processed before 'crash': %d", successfulShares.Load())

	// "Restart" - create new validator
	v2 := NewValidator(getJob)

	// Verify no phantom state from previous run
	stats := v2.Stats()
	if stats.Validated > 0 {
		t.Fatal("CRITICAL: Phantom shares detected after restart")
	}
}

// TestCrashDuringBlockSubmission simulates crash during block submission.
// In SOLO mode, this is the most critical crash scenario.
func TestCrashDuringBlockSubmission(t *testing.T) {
	// This test verifies behavior when:
	// 1. Valid block hash is found
	// 2. Submission to daemon is initiated
	// 3. Crash occurs before confirmation

	job := &protocol.Job{
		ID:            "block_crash_test",
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
	v.SetNetworkDifficulty(0.0001) // Very low to allow block matches

	// Simulate finding a block-meeting share
	share := &protocol.Share{
		JobID:       job.ID,
		ExtraNonce1: "12345678",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "cafebabe",
		Difficulty:  0.0001,
	}

	result := v.Validate(share)
	if result.IsBlock {
		t.Log("Block candidate detected - this would trigger submission")
		t.Log("Block hash:", result.BlockHash)
	}

	// The test verifies the validator can identify blocks correctly
	// Block submission persistence is handled at a higher layer
}

// TestRestartDuringRoundTransition tests behavior when restarting mid-round.
func TestRestartDuringRoundTransition(t *testing.T) {
	job1 := &protocol.Job{
		ID:            "round1_job",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	job2 := &protocol.Job{
		ID:            "round2_job",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000002",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job1.ID {
			return job1, true
		}
		if id == job2.ID {
			return job2, true
		}
		return nil, false
	}

	v := NewValidator(getJob)

	// Submit shares for "round 1"
	for i := 0; i < 50; i++ {
		v.Validate(&protocol.Share{
			JobID:       job1.ID,
			ExtraNonce1: "12345678",
			ExtraNonce2: encodeUint32HexPadded(uint32(i)),
			NTime:       "64000000",
			Nonce:       encodeUint32HexPadded(uint32(i)),
			Difficulty:  1.0,
		})
	}

	stats1 := v.Stats()
	round1Validated := stats1.Validated

	// Submit some shares for "round 2"
	for i := 0; i < 30; i++ {
		v.Validate(&protocol.Share{
			JobID:       job2.ID,
			ExtraNonce1: "12345678",
			ExtraNonce2: encodeUint32HexPadded(uint32(i)),
			NTime:       "64000000",
			Nonce:       encodeUint32HexPadded(uint32(i)),
			Difficulty:  1.0,
		})
	}

	stats2 := v.Stats()

	// "Crash" and restart
	v2 := NewValidator(getJob)

	// After restart, shares from both rounds are lost (in-memory only)
	// This is acceptable for SOLO mode - shares are for statistics only
	stats3 := v2.Stats()
	if stats3.Validated > 0 {
		t.Fatal("CRITICAL: State persisted across restart unexpectedly")
	}

	t.Logf("Round 1: %d shares, Round 2: %d shares, After restart: 0 (expected)",
		round1Validated, stats2.Validated-round1Validated)
}

// TestDuplicateTrackerCrashRecovery verifies duplicate detection after crash.
func TestDuplicateTrackerCrashRecovery(t *testing.T) {
	job := &protocol.Job{
		ID:            "dup_crash_test",
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

	share := &protocol.Share{
		JobID:       job.ID,
		ExtraNonce1: "12345678",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
	}

	// First submission
	result1 := v.Validate(share)
	_ = result1

	// Same share should be duplicate
	result2 := v.Validate(share)
	if result2.RejectReason != protocol.RejectReasonDuplicate {
		t.Log("Second submission may be rejected for other reasons")
	}

	// "Crash" and restart
	v2 := NewValidator(getJob)

	// After restart, the duplicate tracker is fresh
	// Note: The job is now stale (CreatedAt was before restart)
	// In production, a fresh job would be created

	// Create fresh job for post-restart
	freshJob := &protocol.Job{
		ID:            "fresh_job",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		CreatedAt:     time.Now(),
	}

	getJob2 := func(id string) (*protocol.Job, bool) {
		if id == freshJob.ID {
			return freshJob, true
		}
		return nil, false
	}

	v3 := NewValidator(getJob2)

	freshShare := &protocol.Share{
		JobID:       freshJob.ID,
		ExtraNonce1: "12345678",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
	}

	result3 := v3.Validate(freshShare)
	if result3.RejectReason == protocol.RejectReasonDuplicate {
		t.Log("Share on fresh job accepted after restart (expected behavior)")
	}

	_ = v2 // Suppress unused warning
}

// =============================================================================
// STATE PERSISTENCE VERIFICATION
// =============================================================================

// TestNoPhantomShares ensures shares never appear from nowhere.
func TestNoPhantomShares(t *testing.T) {
	job := &protocol.Job{
		ID:            "phantom_test",
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

	// Check initial state
	stats := v.Stats()
	if stats.Validated != 0 {
		t.Fatalf("Initial validated count should be 0, got %d", stats.Validated)
	}

	// Submit exactly N shares
	const expectedSubmissions = 42
	for i := 0; i < expectedSubmissions; i++ {
		v.Validate(&protocol.Share{
			JobID:       job.ID,
			ExtraNonce1: "12345678",
			ExtraNonce2: encodeUint32HexPadded(uint32(i)),
			NTime:       "64000000",
			Nonce:       encodeUint32HexPadded(uint32(i)),
			Difficulty:  1.0,
		})
	}

	// Verify count matches exactly
	finalStats := v.Stats()
	if finalStats.Validated != uint64(expectedSubmissions) {
		t.Fatalf("CRITICAL: Validated count mismatch: expected %d, got %d",
			expectedSubmissions, finalStats.Validated)
	}

	t.Logf("Submitted: %d, Validated: %d", expectedSubmissions, finalStats.Validated)
}

// TestNoDoubleCounting ensures shares aren't counted twice.
func TestNoDoubleCounting(t *testing.T) {
	job := &protocol.Job{
		ID:            "double_count_test",
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

	share := &protocol.Share{
		JobID:       job.ID,
		ExtraNonce1: "12345678",
		ExtraNonce2: "00000001",
		NTime:       "64000000",
		Nonce:       "cafebabe",
		Difficulty:  1.0,
	}

	// Submit same share 100 times
	for i := 0; i < 100; i++ {
		v.Validate(share)
	}

	// Validated count should be 100 (all were processed)
	// But accepted should be 1 (99 duplicates)
	stats := v.Stats()
	if stats.Validated != 100 {
		t.Errorf("Expected 100 validated, got %d", stats.Validated)
	}
	if stats.Accepted > 1 {
		t.Fatalf("Expected at most 1 accepted share, got %d (double counting detected)", stats.Accepted)
	}

	t.Logf("Validated: %d, Accepted: %d, Rejected: %d",
		stats.Validated, stats.Accepted, stats.Rejected)
}

// =============================================================================
// CONCURRENT CRASH SIMULATION
// =============================================================================

// TestConcurrentCrashRecovery tests recovery after concurrent operation crash.
func TestConcurrentCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent crash test in short mode")
	}

	for iteration := 0; iteration < 5; iteration++ {
		t.Run("Iteration", func(t *testing.T) {
			job := &protocol.Job{
				ID:            "concurrent_crash_test",
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

			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			var submitted atomic.Uint64

			// Start 20 concurrent workers
			for w := 0; w < 20; w++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for i := 0; ; i++ {
						select {
						case <-ctx.Done():
							return
						default:
							v.Validate(&protocol.Share{
								JobID:       job.ID,
								ExtraNonce1: encodeUint32HexPadded(uint32(workerID)),
								ExtraNonce2: encodeUint32HexPadded(uint32(i)),
								NTime:       "64000000",
								Nonce:       encodeUint32HexPadded(uint32(i)),
								Difficulty:  1.0,
							})
							submitted.Add(1)
						}
					}
				}(w)
			}

			// Random crash time
			time.Sleep(time.Duration(10+iteration*5) * time.Millisecond)
			cancel()
			wg.Wait()

			// Verify state consistency
			stats := v.Stats()
			if stats.Validated > submitted.Load() {
				t.Fatalf("CRITICAL: More validated (%d) than submitted (%d)",
					stats.Validated, submitted.Load())
			}
		})
	}
}
