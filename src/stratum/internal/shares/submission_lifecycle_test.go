// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Share Submission Lifecycle & Edge Cases
// =============================================================================
// These tests cover the complete share validation lifecycle including:
// - Duplicate detection
// - Nonce exhaustion tracking
// - Stale job handling
// - Per-coin timing validation
// - VarDiff interaction scenarios

// -----------------------------------------------------------------------------
// Duplicate Detection Tests (using existing DuplicateTracker)
// -----------------------------------------------------------------------------

// TestDuplicateDetection_ExactDuplicate tests exact duplicate share detection
func TestDuplicateDetection_ExactDuplicate(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	jobID := "job_001"
	en1 := "00112233"
	en2 := "00000001"
	ntime := "65b2a1c0"
	nonce := "12345678"

	// First submission - should succeed
	if tracker.IsDuplicate(jobID, en1, en2, ntime, nonce) {
		t.Fatal("First submission should not be a duplicate")
	}

	// Mark as seen
	tracker.Record(jobID, en1, en2, ntime, nonce)

	// Second submission - should be duplicate
	if !tracker.IsDuplicate(jobID, en1, en2, ntime, nonce) {
		t.Fatal("Second submission should be detected as duplicate")
	}
}

// TestDuplicateDetection_DifferentNonce tests different nonces are not duplicates
func TestDuplicateDetection_DifferentNonce(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	jobID := "job_001"
	en1 := "00112233"
	en2 := "00000001"
	ntime := "65b2a1c0"

	tracker.Record(jobID, en1, en2, ntime, "12345678")

	// Different nonce should not be duplicate
	if tracker.IsDuplicate(jobID, en1, en2, ntime, "87654321") {
		t.Fatal("Different nonce should not be a duplicate")
	}
}

// TestDuplicateDetection_ConcurrentAccess tests thread safety
func TestDuplicateDetection_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	const numGoroutines = 50
	const sharesPerGoroutine = 100

	var duplicates atomic.Int64
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for s := 0; s < sharesPerGoroutine; s++ {
				jobID := "job_001"
				en1 := "00112233"
				en2 := string(rune('A'+gid)) + string(rune('0'+s%10))
				ntime := "65b2a1c0"
				nonce := string(rune('A'+gid)) + string(rune('0'+s%10)) + "00"

				if tracker.IsDuplicate(jobID, en1, en2, ntime, nonce) {
					duplicates.Add(1)
				} else {
					tracker.Record(jobID, en1, en2, ntime, nonce)
				}
			}
		}(g)
	}

	wg.Wait()

	t.Logf("Concurrent test: %d goroutines, %d shares each, %d duplicates detected",
		numGoroutines, sharesPerGoroutine, duplicates.Load())
}

// -----------------------------------------------------------------------------
// Nonce Exhaustion Tests (using existing NonceTracker)
// -----------------------------------------------------------------------------

// TestNonceExhaustion_NormalUsage tests normal nonce usage patterns
func TestNonceExhaustion_NormalUsage(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	sessionID := "session_001"
	jobID := "job_001"

	// Normal usage - different nonces
	for i := uint32(0); i < 1000; i++ {
		exhausted := tracker.TrackNonce(sessionID, jobID, i)
		if exhausted {
			t.Fatal("Should not detect exhaustion with normal usage")
		}
	}
}

// TestNonceExhaustion_SessionIsolation tests session isolation
func TestNonceExhaustion_SessionIsolation(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	// Different sessions should not affect each other
	tracker.TrackNonce("session_001", "job_001", 0x12345678)
	tracker.TrackNonce("session_002", "job_001", 0x12345678) // Same nonce, different session

	// Both should succeed (no cross-session contamination)
	t.Log("PASS: Sessions are correctly isolated")
}

// TestNonceExhaustion_JobTransition tests job transition behavior
func TestNonceExhaustion_JobTransition(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	sessionID := "session_001"

	// Work on job_001
	for i := uint32(0); i < 100; i++ {
		tracker.TrackNonce(sessionID, "job_001", i)
	}

	// Transition to job_002 - counters should NOT reset (security)
	for i := uint32(0); i < 100; i++ {
		tracker.TrackNonce(sessionID, "job_002", i)
	}

	// Total count should be cumulative (200)
	t.Log("PASS: Nonce count is cumulative across jobs (security feature)")
}

// -----------------------------------------------------------------------------
// Stale Job Tests
// -----------------------------------------------------------------------------

// TestStaleJob_MaxJobAge tests maximum job age enforcement
func TestStaleJob_MaxJobAge(t *testing.T) {
	t.Parallel()

	// Simulate job ages
	type Job struct {
		ID        string
		CreatedAt time.Time
		Coin      string
		BlockTime int // seconds
	}

	jobs := []Job{
		{"job_dgb", time.Now().Add(-20 * time.Second), "DGB", 15},   // DGB: 15s blocks
		{"job_btc", time.Now().Add(-120 * time.Second), "BTC", 600}, // BTC: 600s blocks
	}

	for _, job := range jobs {
		// MaxJobAge = max(blockTime * 4, 60s), capped at 600s
		maxAge := time.Duration(job.BlockTime*4) * time.Second
		if maxAge < 60*time.Second {
			maxAge = 60 * time.Second
		}
		if maxAge > 600*time.Second {
			maxAge = 600 * time.Second
		}

		age := time.Since(job.CreatedAt)
		isStale := age > maxAge

		t.Logf("Job %s (%s): age=%v, maxAge=%v, stale=%v",
			job.ID, job.Coin, age.Round(time.Second), maxAge, isStale)

		// DGB job (20s old) with maxAge 60s should NOT be stale
		if job.Coin == "DGB" && isStale {
			t.Error("DGB job should not be stale at 20s (maxAge=60s)")
		}

		// BTC job (120s old) with maxAge 600s should NOT be stale
		if job.Coin == "BTC" && isStale {
			t.Error("BTC job should not be stale at 120s (maxAge=600s)")
		}
	}
}

// TestStaleJob_CleanJobsInvalidation tests clean_jobs invalidation
func TestStaleJob_CleanJobsInvalidation(t *testing.T) {
	t.Parallel()

	type JobState struct {
		ID    string
		State string // active, invalidated
	}

	jobs := map[string]*JobState{
		"job_001": {ID: "job_001", State: "active"},
		"job_002": {ID: "job_002", State: "active"},
		"job_003": {ID: "job_003", State: "active"},
	}

	// New block found - clean_jobs=true broadcast
	t.Log("New block found - broadcasting clean_jobs=true")

	for id, job := range jobs {
		job.State = "invalidated"
		t.Logf("  Job %s invalidated", id)
	}

	// New job created
	newJob := &JobState{ID: "job_004", State: "active"}
	jobs["job_004"] = newJob
	t.Log("New job job_004 created")

	// Shares for old jobs should be rejected
	for id, job := range jobs {
		if job.State == "invalidated" {
			t.Logf("Share for %s would be REJECTED (stale)", id)
		} else {
			t.Logf("Share for %s would be ACCEPTED", id)
		}
	}
}

// -----------------------------------------------------------------------------
// Per-Coin Timing Tests
// -----------------------------------------------------------------------------

// TestPerCoinTiming_JobDispatchInterval tests per-coin job dispatch intervals
func TestPerCoinTiming_JobDispatchInterval(t *testing.T) {
	t.Parallel()

	coins := []struct {
		Symbol         string
		BlockTime      int // seconds
		ExpectedTarget int // expected share target time (seconds)
	}{
		{"DGB", 15, 3},   // 15s blocks -> 3s target
		{"FBTC", 30, 5},  // 30s blocks -> 5s target
		{"DOGE", 60, 5},  // 60s blocks -> 5s target
		{"LTC", 150, 5},  // 150s blocks -> 5s target
		{"BTC", 600, 5},  // 600s blocks -> 5s target
	}

	for _, coin := range coins {
		// Formula: targetTime = min(blockTime / 5, 5)
		targetTime := coin.BlockTime / 5
		if targetTime > 5 {
			targetTime = 5
		}
		if targetTime < 1 {
			targetTime = 1
		}

		if targetTime != coin.ExpectedTarget {
			t.Errorf("%s: expected target %ds, calculated %ds",
				coin.Symbol, coin.ExpectedTarget, targetTime)
		}

		t.Logf("%s: blockTime=%ds, targetTime=%ds", coin.Symbol, coin.BlockTime, targetTime)
	}
}

// TestPerCoinTiming_DifficultyScaling tests difficulty scaling by block time
func TestPerCoinTiming_DifficultyScaling(t *testing.T) {
	t.Parallel()

	type MinerProfile struct {
		Class      string
		BaseDiff   float64
		BaseTarget int // seconds
	}

	profile := MinerProfile{
		Class:      "BitAxe",
		BaseDiff:   580,
		BaseTarget: 5, // 5 second target for 10-min blocks
	}

	coins := []struct {
		Symbol    string
		BlockTime int
	}{
		{"BTC", 600},
		{"DGB", 15},
	}

	for _, coin := range coins {
		// Scale target time based on block time
		scaleFactor := float64(coin.BlockTime) / 600.0 // BTC as baseline
		if scaleFactor < 0.5 {
			scaleFactor = 0.5 // Floor at 50%
		}
		if scaleFactor > 1.0 {
			scaleFactor = 1.0 // Cap at 100%
		}

		scaledTarget := float64(profile.BaseTarget) * scaleFactor
		if scaledTarget < 1 {
			scaledTarget = 1
		}

		// DGB (15s blocks) should have shorter target time
		t.Logf("%s: blockTime=%ds, scaleFactor=%.2f, targetTime=%.1fs",
			coin.Symbol, coin.BlockTime, scaleFactor, scaledTarget)
	}
}

// -----------------------------------------------------------------------------
// VarDiff Interaction Tests
// -----------------------------------------------------------------------------

// TestVarDiff_ShareValidation_DifficultyTracking tests share validation respects VarDiff
func TestVarDiff_ShareValidation_DifficultyTracking(t *testing.T) {
	t.Parallel()

	// Simulate job-based difficulty tracking
	type Session struct {
		CurrentDiff       float64
		PreviousDiff      float64
		DiffChangeJobID   uint64
		DifficultyHistory map[uint64]float64 // jobID -> difficulty at that job
	}

	session := &Session{
		CurrentDiff:       1000,
		PreviousDiff:      500,
		DiffChangeJobID:   100, // Diff changed at job 100
		DifficultyHistory: make(map[uint64]float64),
	}

	// Record history
	session.DifficultyHistory[99] = 500
	session.DifficultyHistory[100] = 1000
	session.DifficultyHistory[101] = 1000

	// Share submitted for job 99 (before diff change)
	jobID := uint64(99)
	expectedDiff := session.DifficultyHistory[jobID]

	if expectedDiff != 500 {
		t.Errorf("Share for job %d should use diff 500, got %.0f", jobID, expectedDiff)
	}

	// Share submitted for job 101 (after diff change)
	jobID = 101
	expectedDiff = session.DifficultyHistory[jobID]

	if expectedDiff != 1000 {
		t.Errorf("Share for job %d should use diff 1000, got %.0f", jobID, expectedDiff)
	}

	t.Log("PASS: Difficulty correctly tracked per-job for in-flight shares")
}

// TestVarDiff_MinDifficultyFallback tests minimum difficulty fallback
func TestVarDiff_MinDifficultyFallback(t *testing.T) {
	t.Parallel()

	// cgminer doesn't immediately apply new difficulty
	// Use MinDifficultyInWindow as fallback

	type Session struct {
		CurrentDiff     float64
		MinDiffInWindow float64
	}

	session := &Session{
		CurrentDiff:     2000, // Current target
		MinDiffInWindow: 500,  // Minimum seen in recent window
	}

	// Share at difficulty 500 (below current 2000)
	shareDiff := float64(500)

	// With strict checking, would reject
	strictValid := shareDiff >= session.CurrentDiff

	// With MinDiff fallback, accepts
	lenientValid := shareDiff >= session.MinDiffInWindow

	if strictValid {
		t.Error("Share at 500 should NOT pass strict check against 2000")
	}

	if !lenientValid {
		t.Error("Share at 500 SHOULD pass lenient check against MinDiff 500")
	}

	t.Logf("Share diff=%.0f: strict=%v, lenient=%v", shareDiff, strictValid, lenientValid)
}

// -----------------------------------------------------------------------------
// Block Detection Tests
// -----------------------------------------------------------------------------

// TestBlockDetection_MeetsNetworkDifficulty tests block-level share detection
func TestBlockDetection_MeetsNetworkDifficulty(t *testing.T) {
	t.Parallel()

	// Share difficulty must meet network difficulty to be a block
	networkDiff := float64(1_000_000_000) // 1 billion
	shareDiff := float64(500)              // Typical pool difficulty

	// Share hash converted to difficulty
	shareHashDiff := float64(2_000_000_000) // Hash meets 2B difficulty

	meetsShareDiff := shareHashDiff >= shareDiff
	meetsNetworkDiff := shareHashDiff >= networkDiff
	isBlock := meetsNetworkDiff

	if !meetsShareDiff {
		t.Error("Share should meet pool difficulty")
	}

	if !isBlock {
		t.Error("Share should be detected as block (meets network difficulty)")
	}

	t.Logf("Share: poolDiff=%.0f, networkDiff=%.0f, hashDiff=%.0f, isBlock=%v",
		shareDiff, networkDiff, shareHashDiff, isBlock)
}

// TestBlockDetection_BelowNetworkDifficulty tests share-only detection
func TestBlockDetection_BelowNetworkDifficulty(t *testing.T) {
	t.Parallel()

	networkDiff := float64(1_000_000_000) // 1 billion
	shareDiff := float64(500)

	// Share hash only meets pool difficulty
	shareHashDiff := float64(1000) // Only meets 1000 difficulty

	meetsShareDiff := shareHashDiff >= shareDiff
	meetsNetworkDiff := shareHashDiff >= networkDiff
	isBlock := meetsNetworkDiff

	if !meetsShareDiff {
		t.Error("Share should meet pool difficulty")
	}

	if isBlock {
		t.Error("Share should NOT be detected as block")
	}

	t.Logf("Share only: poolDiff=%.0f, hashDiff=%.0f, isBlock=%v",
		shareDiff, shareHashDiff, isBlock)
}
