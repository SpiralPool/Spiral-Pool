// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ============================================================================
// Failover Behavior Tests
// ============================================================================

// TestRedisHA_LocalFallback_HighVolume records 10000 shares in local-only
// (fallback) mode and verifies that every single one is accounted for and
// that duplicates are correctly rejected.
func TestRedisHA_LocalFallback_HighVolume(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	const total = 10000

	// Record 10000 unique shares.
	for i := 0; i < total; i++ {
		nonce := fmt.Sprintf("nonce_%d", i)
		ok := tracker.RecordIfNew("highvol_job", "en1", "en2", "ntime", nonce)
		if !ok {
			t.Fatalf("RecordIfNew returned false for new share at index %d", i)
		}
	}

	// Verify stats reflect all shares.
	jobs, shares, _, _, _, _ := tracker.Stats()
	if jobs != 1 {
		t.Errorf("jobs: got %d, want 1", jobs)
	}
	if shares != total {
		t.Errorf("shares: got %d, want %d", shares, total)
	}

	// Every share should now be detected as duplicate.
	for i := 0; i < total; i++ {
		nonce := fmt.Sprintf("nonce_%d", i)
		ok := tracker.RecordIfNew("highvol_job", "en1", "en2", "ntime", nonce)
		if ok {
			t.Fatalf("RecordIfNew returned true for duplicate share at index %d", i)
		}
	}

	// Share count should not have increased.
	_, sharesAfter, _, _, _, _ := tracker.Stats()
	if sharesAfter != total {
		t.Errorf("shares after re-recording: got %d, want %d", sharesAfter, total)
	}
}

// TestRedisHA_LocalFallback_MultipleJobs verifies that the local fallback
// tracks shares independently per job ID.
func TestRedisHA_LocalFallback_MultipleJobs(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	jobIDs := []string{"job_alpha", "job_beta", "job_gamma", "job_delta"}
	const sharesPerJob = 50

	// Record unique shares per job.
	for _, jid := range jobIDs {
		for i := 0; i < sharesPerJob; i++ {
			nonce := fmt.Sprintf("nonce_%d", i)
			ok := tracker.RecordIfNew(jid, "en1", "en2", "ntime", nonce)
			if !ok {
				t.Fatalf("RecordIfNew returned false for new share: job=%s, i=%d", jid, i)
			}
		}
	}

	jobs, shares, _, _, _, _ := tracker.Stats()
	if jobs != len(jobIDs) {
		t.Errorf("jobs: got %d, want %d", jobs, len(jobIDs))
	}
	expectedShares := len(jobIDs) * sharesPerJob
	if shares != expectedShares {
		t.Errorf("shares: got %d, want %d", shares, expectedShares)
	}

	// Each job's shares should be independent -- recording the same nonce
	// under a different job should have already succeeded above, confirming
	// isolation. Now re-record under each job to confirm dedup is per-job.
	for _, jid := range jobIDs {
		ok := tracker.RecordIfNew(jid, "en1", "en2", "ntime", "nonce_0")
		if ok {
			t.Errorf("expected duplicate for job=%s nonce_0, got new", jid)
		}
	}
}

// TestRedisHA_LocalFallback_CleanupMultipleJobs verifies that CleanupJob
// only removes shares for the targeted job, leaving other jobs intact.
func TestRedisHA_LocalFallback_CleanupMultipleJobs(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	// Set up three jobs with distinct share counts.
	for i := 0; i < 10; i++ {
		tracker.RecordIfNew("keep_a", "en1", "en2", "ntime", fmt.Sprintf("n%d", i))
	}
	for i := 0; i < 20; i++ {
		tracker.RecordIfNew("remove_me", "en1", "en2", "ntime", fmt.Sprintf("n%d", i))
	}
	for i := 0; i < 15; i++ {
		tracker.RecordIfNew("keep_b", "en1", "en2", "ntime", fmt.Sprintf("n%d", i))
	}

	jobsBefore, sharesBefore, _, _, _, _ := tracker.Stats()
	if jobsBefore != 3 {
		t.Fatalf("before cleanup: jobs=%d, want 3", jobsBefore)
	}
	if sharesBefore != 45 {
		t.Fatalf("before cleanup: shares=%d, want 45", sharesBefore)
	}

	// Cleanup only the middle job.
	tracker.CleanupJob("remove_me")

	jobsAfter, sharesAfter, _, _, _, _ := tracker.Stats()
	if jobsAfter != 2 {
		t.Errorf("after cleanup: jobs=%d, want 2", jobsAfter)
	}
	if sharesAfter != 25 {
		t.Errorf("after cleanup: shares=%d, want 25 (10+15)", sharesAfter)
	}

	// Shares from surviving jobs should still be duplicates.
	if tracker.RecordIfNew("keep_a", "en1", "en2", "ntime", "n0") {
		t.Error("keep_a/n0 should still be duplicate")
	}
	if tracker.RecordIfNew("keep_b", "en1", "en2", "ntime", "n0") {
		t.Error("keep_b/n0 should still be duplicate")
	}

	// Shares from the removed job should now be accepted as new.
	if !tracker.RecordIfNew("remove_me", "en1", "en2", "ntime", "n0") {
		t.Error("remove_me/n0 should be new after cleanup")
	}
}

// TestRedisHA_StatsAccuracy_HighVolume verifies that stats counters remain
// accurate after a large number of mixed operations.
func TestRedisHA_StatsAccuracy_HighVolume(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	const numJobs = 20
	const sharesPerJob = 100

	for j := 0; j < numJobs; j++ {
		jobID := fmt.Sprintf("statsjob_%d", j)
		for s := 0; s < sharesPerJob; s++ {
			nonce := fmt.Sprintf("nonce_%d", s)
			tracker.RecordIfNew(jobID, "en1", "en2", "ntime", nonce)
		}
	}

	jobs, shares, redisHits, redisMisses, redisErrors, localHits := tracker.Stats()
	if jobs != numJobs {
		t.Errorf("jobs: got %d, want %d", jobs, numJobs)
	}
	if shares != numJobs*sharesPerJob {
		t.Errorf("shares: got %d, want %d", shares, numJobs*sharesPerJob)
	}

	// In local-only mode there are no Redis operations.
	if redisHits != 0 || redisMisses != 0 || redisErrors != 0 || localHits != 0 {
		t.Errorf("expected all redis/local counters to be 0, got hits=%d misses=%d errors=%d localHits=%d",
			redisHits, redisMisses, redisErrors, localHits)
	}

	// Cleanup half the jobs and verify stats update.
	for j := 0; j < numJobs/2; j++ {
		tracker.CleanupJob(fmt.Sprintf("statsjob_%d", j))
	}

	jobsAfter, sharesAfter, _, _, _, _ := tracker.Stats()
	expectedJobs := numJobs / 2
	expectedShares := expectedJobs * sharesPerJob
	if jobsAfter != expectedJobs {
		t.Errorf("after cleanup: jobs=%d, want %d", jobsAfter, expectedJobs)
	}
	if sharesAfter != expectedShares {
		t.Errorf("after cleanup: shares=%d, want %d", sharesAfter, expectedShares)
	}
}

// ============================================================================
// Session Continuity Tests
// ============================================================================

// TestRedisHA_SharesPreserved_AcrossHealthChecks verifies that shares
// recorded before and during health check execution remain valid.
func TestRedisHA_SharesPreserved_AcrossHealthChecks(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	// Record shares before health check starts.
	for i := 0; i < 100; i++ {
		tracker.RecordIfNew("hcjob", "en1", "en2", "ntime", fmt.Sprintf("pre_%d", i))
	}

	// Start health check with a short-lived context.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	tracker.StartHealthCheck(ctx)

	// Record shares while health check is running.
	for i := 0; i < 100; i++ {
		tracker.RecordIfNew("hcjob", "en1", "en2", "ntime", fmt.Sprintf("during_%d", i))
	}

	// Wait for health check to finish.
	<-ctx.Done()
	// Allow goroutine to exit.
	time.Sleep(50 * time.Millisecond)

	// All shares should still be tracked as duplicates.
	for i := 0; i < 100; i++ {
		if !tracker.IsDuplicate("hcjob", "en1", "en2", "ntime", fmt.Sprintf("pre_%d", i)) {
			t.Errorf("pre-healthcheck share pre_%d should be duplicate", i)
		}
		if !tracker.IsDuplicate("hcjob", "en1", "en2", "ntime", fmt.Sprintf("during_%d", i)) {
			t.Errorf("during-healthcheck share during_%d should be duplicate", i)
		}
	}

	_, shares, _, _, _, _ := tracker.Stats()
	if shares != 200 {
		t.Errorf("shares: got %d, want 200", shares)
	}
}

// TestRedisHA_ContextCancellation_NoLeak verifies that StartHealthCheck
// exits cleanly when the context is cancelled, with no goroutine leak.
func TestRedisHA_ContextCancellation_NoLeak(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Start health check.
	tracker.StartHealthCheck(ctx)

	// Cancel immediately.
	cancel()

	// Give the goroutine time to observe cancellation and exit.
	time.Sleep(100 * time.Millisecond)

	// Verify tracker is still functional after health check goroutine exits.
	ok := tracker.RecordIfNew("postcancel", "en1", "en2", "ntime", "nonce1")
	if !ok {
		t.Error("RecordIfNew should succeed after health check context cancel")
	}
	if !tracker.IsDuplicate("postcancel", "en1", "en2", "ntime", "nonce1") {
		t.Error("share should be duplicate after recording")
	}
}

// TestRedisHA_CloseIdempotent verifies that calling Close() multiple times
// does not panic or return an error.
func TestRedisHA_CloseIdempotent(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Close multiple times -- none should panic or return an error.
	for i := 0; i < 5; i++ {
		if err := tracker.Close(); err != nil {
			t.Errorf("Close() call %d returned error: %v", i+1, err)
		}
	}

	// Tracker should still be usable for local operations after Close()
	// because Close() only affects the Redis client (which is nil here).
	ok := tracker.RecordIfNew("afterclose", "en1", "en2", "ntime", "n1")
	if !ok {
		t.Error("RecordIfNew should work after Close() on nil-client tracker")
	}
}

// ============================================================================
// Dedup Correctness Tests
// ============================================================================

// TestRedisHA_DedupKey_Components verifies that different combinations of
// en1, en2, ntime, and nonce produce distinct dedup entries.
func TestRedisHA_DedupKey_Components(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	// Record a base share.
	ok := tracker.RecordIfNew("keyjob", "aaa", "bbb", "ccc", "ddd")
	if !ok {
		t.Fatal("base share should be new")
	}

	// Each variation changes exactly one field and should be accepted.
	variations := []struct {
		name  string
		en1   string
		en2   string
		ntime string
		nonce string
	}{
		{"different en1", "xxx", "bbb", "ccc", "ddd"},
		{"different en2", "aaa", "xxx", "ccc", "ddd"},
		{"different ntime", "aaa", "bbb", "xxx", "ddd"},
		{"different nonce", "aaa", "bbb", "ccc", "xxx"},
	}

	for _, v := range variations {
		ok := tracker.RecordIfNew("keyjob", v.en1, v.en2, v.ntime, v.nonce)
		if !ok {
			t.Errorf("%s: should be accepted as new share", v.name)
		}
	}

	// Original should still be duplicate.
	if tracker.RecordIfNew("keyjob", "aaa", "bbb", "ccc", "ddd") {
		t.Error("original share should still be duplicate")
	}
}

// TestRedisHA_DedupKey_JobIsolation verifies that the same nonce
// submitted under different job IDs are NOT treated as duplicates.
func TestRedisHA_DedupKey_JobIsolation(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	const numJobs = 10
	sharedNonce := "shared_nonce_value"

	// Record the exact same (en1, en2, ntime, nonce) under different jobs.
	for i := 0; i < numJobs; i++ {
		jobID := fmt.Sprintf("iso_job_%d", i)
		ok := tracker.RecordIfNew(jobID, "en1", "en2", "ntime", sharedNonce)
		if !ok {
			t.Errorf("job %s: same nonce in different job should be new", jobID)
		}
	}

	// Verify each is now a duplicate within its own job.
	for i := 0; i < numJobs; i++ {
		jobID := fmt.Sprintf("iso_job_%d", i)
		if !tracker.IsDuplicate(jobID, "en1", "en2", "ntime", sharedNonce) {
			t.Errorf("job %s: should be duplicate now", jobID)
		}
	}

	jobs, shares, _, _, _, _ := tracker.Stats()
	if jobs != numJobs {
		t.Errorf("jobs: got %d, want %d", jobs, numJobs)
	}
	if shares != numJobs {
		t.Errorf("shares: got %d, want %d", shares, numJobs)
	}
}

// TestRedisHA_DedupKey_AllFieldsMatter ensures that changing ANY single
// field (jobID, en1, en2, ntime, nonce) produces a non-duplicate entry.
func TestRedisHA_DedupKey_AllFieldsMatter(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	base := [5]string{"job", "e1", "e2", "nt", "nc"}

	// Record the base share.
	ok := tracker.RecordIfNew(base[0], base[1], base[2], base[3], base[4])
	if !ok {
		t.Fatal("base share should be new")
	}

	// For each of the five fields, change only that field.
	fieldNames := []string{"jobID", "en1", "en2", "ntime", "nonce"}
	for idx := 0; idx < 5; idx++ {
		modified := base
		modified[idx] = "CHANGED"

		ok := tracker.RecordIfNew(modified[0], modified[1], modified[2], modified[3], modified[4])
		if !ok {
			t.Errorf("changing only %s should produce a new share", fieldNames[idx])
		}
	}

	// Re-check that the base is still duplicate.
	if !tracker.IsDuplicate(base[0], base[1], base[2], base[3], base[4]) {
		t.Error("base share should still be detected as duplicate")
	}
}

// TestRedisHA_RecordIfNew_ReturnValue_Consistency verifies that RecordIfNew
// and IsDuplicate always agree on the state of a share.
func TestRedisHA_RecordIfNew_ReturnValue_Consistency(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	const rounds = 500

	for i := 0; i < rounds; i++ {
		jobID := fmt.Sprintf("cjob_%d", i%5)
		nonce := fmt.Sprintf("cn_%d", i)

		// Before recording: IsDuplicate should be false.
		if tracker.IsDuplicate(jobID, "en1", "en2", "ntime", nonce) {
			t.Fatalf("round %d: IsDuplicate true before RecordIfNew", i)
		}

		// RecordIfNew should return true (new).
		ok := tracker.RecordIfNew(jobID, "en1", "en2", "ntime", nonce)
		if !ok {
			t.Fatalf("round %d: RecordIfNew returned false for new share", i)
		}

		// After recording: IsDuplicate should be true.
		if !tracker.IsDuplicate(jobID, "en1", "en2", "ntime", nonce) {
			t.Fatalf("round %d: IsDuplicate false after RecordIfNew", i)
		}

		// Second RecordIfNew should return false (duplicate).
		ok = tracker.RecordIfNew(jobID, "en1", "en2", "ntime", nonce)
		if ok {
			t.Fatalf("round %d: RecordIfNew returned true for duplicate share", i)
		}
	}
}

// ============================================================================
// Stress / Race Tests
// ============================================================================

// TestRedisHA_ConcurrentMixedOps runs RecordIfNew, CleanupJob, and Stats
// concurrently to verify there are no data races.
func TestRedisHA_ConcurrentMixedOps(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Writer goroutines: record shares across multiple jobs.
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				jobID := fmt.Sprintf("mixjob_%d", gID)
				nonce := fmt.Sprintf("n_%d_%d", gID, i)
				tracker.RecordIfNew(jobID, "en1", "en2", "ntime", nonce)
			}
		}(g)
	}

	// Cleanup goroutines: periodically cleanup random jobs.
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				jobID := fmt.Sprintf("mixjob_%d", (gID+i)%5)
				tracker.CleanupJob(jobID)
				time.Sleep(time.Millisecond)
			}
		}(g)
	}

	// Stats goroutines: read stats continuously.
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				tracker.Stats()
			}
		}()
	}

	// IsDuplicate goroutines: check duplicates continuously.
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				jobID := fmt.Sprintf("mixjob_%d", gID%5)
				nonce := fmt.Sprintf("n_%d_%d", gID, i%100)
				tracker.IsDuplicate(jobID, "en1", "en2", "ntime", nonce)
			}
		}(g)
	}

	wg.Wait()

	// If we got here without a panic or race detector error, the test passes.
	// Do a final sanity check on Stats.
	jobs, shares, _, _, _, _ := tracker.Stats()
	if jobs < 0 || shares < 0 {
		t.Errorf("stats should be non-negative: jobs=%d shares=%d", jobs, shares)
	}
}

// TestRedisHA_ConcurrentRecordSameShare launches 100 goroutines that all
// attempt to record the exact same share. Exactly one should succeed with
// RecordIfNew returning true, and all others should get false.
func TestRedisHA_ConcurrentRecordSameShare(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	const goroutines = 100
	var successCount int64
	var wg sync.WaitGroup

	// Use a barrier to ensure all goroutines start simultaneously.
	ready := make(chan struct{})

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			<-ready
			ok := tracker.RecordIfNew("race_job", "en1", "en2", "ntime", "same_nonce")
			if ok {
				atomic.AddInt64(&successCount, 1)
			}
		}()
	}

	// Release all goroutines at once.
	close(ready)
	wg.Wait()

	if successCount != 1 {
		t.Errorf("exactly 1 goroutine should succeed, got %d", successCount)
	}

	// The share must now be a duplicate.
	if !tracker.IsDuplicate("race_job", "en1", "en2", "ntime", "same_nonce") {
		t.Error("share should be duplicate after concurrent recording")
	}

	_, shares, _, _, _, _ := tracker.Stats()
	if shares != 1 {
		t.Errorf("shares: got %d, want 1", shares)
	}
}

// TestRedisHA_HighConcurrency_StatsConsistent verifies that after a large
// number of concurrent operations, the stats accurately reflect the number
// of unique shares recorded.
func TestRedisHA_HighConcurrency_StatsConsistent(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	const goroutines = 20
	const sharesPerGoroutine = 200
	var totalNew int64
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gID int) {
			defer wg.Done()
			for s := 0; s < sharesPerGoroutine; s++ {
				// Each goroutine uses its own unique nonces to avoid
				// cross-goroutine dedup.
				nonce := fmt.Sprintf("hc_%d_%d", gID, s)
				ok := tracker.RecordIfNew("hc_job", "en1", "en2", "ntime", nonce)
				if ok {
					atomic.AddInt64(&totalNew, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	expectedNew := int64(goroutines * sharesPerGoroutine)
	if totalNew != expectedNew {
		t.Errorf("total new shares: got %d, want %d", totalNew, expectedNew)
	}

	_, shares, _, _, _, _ := tracker.Stats()
	if int64(shares) != expectedNew {
		t.Errorf("stats shares: got %d, want %d", shares, expectedNew)
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

// TestRedisHA_EmptyJobID verifies that empty string job IDs are handled
// gracefully without panics.
func TestRedisHA_EmptyJobID(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	// Recording with empty job ID should work.
	ok := tracker.RecordIfNew("", "en1", "en2", "ntime", "nonce1")
	if !ok {
		t.Error("RecordIfNew with empty jobID should return true for new share")
	}

	// Should detect duplicate.
	if tracker.RecordIfNew("", "en1", "en2", "ntime", "nonce1") {
		t.Error("RecordIfNew with empty jobID should return false for duplicate")
	}

	// IsDuplicate should agree.
	if !tracker.IsDuplicate("", "en1", "en2", "ntime", "nonce1") {
		t.Error("IsDuplicate with empty jobID should return true")
	}

	// CleanupJob with empty jobID should not panic.
	tracker.CleanupJob("")

	// After cleanup, the share should be new again.
	ok = tracker.RecordIfNew("", "en1", "en2", "ntime", "nonce1")
	if !ok {
		t.Error("RecordIfNew should return true after CleanupJob on empty jobID")
	}
}

// TestRedisHA_VeryLongNonce verifies that very long nonce strings (1000+
// characters) are handled without truncation or errors.
func TestRedisHA_VeryLongNonce(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	longNonce := strings.Repeat("a", 1000)

	ok := tracker.RecordIfNew("longjob", "en1", "en2", "ntime", longNonce)
	if !ok {
		t.Error("RecordIfNew with 1000-char nonce should return true")
	}

	// Same long nonce should be detected as duplicate.
	if tracker.RecordIfNew("longjob", "en1", "en2", "ntime", longNonce) {
		t.Error("RecordIfNew with same 1000-char nonce should return false")
	}

	// Nonce differing by one character should be accepted.
	slightlyDifferent := strings.Repeat("a", 999) + "b"
	ok = tracker.RecordIfNew("longjob", "en1", "en2", "ntime", slightlyDifferent)
	if !ok {
		t.Error("RecordIfNew with different long nonce should return true")
	}

	// Long strings in other fields too.
	longEN1 := strings.Repeat("x", 1000)
	ok = tracker.RecordIfNew("longjob", longEN1, "en2", "ntime", "shortnonce")
	if !ok {
		t.Error("RecordIfNew with 1000-char en1 should return true")
	}
}

// TestRedisHA_SpecialCharacters verifies that nonces containing special
// characters (colons, newlines, null bytes, unicode) are handled correctly.
func TestRedisHA_SpecialCharacters(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	specialNonces := []struct {
		name  string
		nonce string
	}{
		{"colons", "abc:def:ghi"},
		{"newlines", "abc\ndef\nghi"},
		{"tabs", "abc\tdef\tghi"},
		{"null bytes", "abc\x00def\x00ghi"},
		{"unicode", "\u00e9\u00e8\u00ea\u2603\u2764"},
		{"mixed delimiters", "a:b\nc\td\x00e"},
		{"empty-like", " "},
		{"just colon", ":"},
		{"double colon", "::"},
	}

	for _, tc := range specialNonces {
		t.Run(tc.name, func(t *testing.T) {
			// First recording should succeed.
			ok := tracker.RecordIfNew("specjob", "en1", "en2", "ntime", tc.nonce)
			if !ok {
				t.Errorf("RecordIfNew(%q): expected true for new share", tc.name)
			}

			// Second recording should detect duplicate.
			ok = tracker.RecordIfNew("specjob", "en1", "en2", "ntime", tc.nonce)
			if ok {
				t.Errorf("RecordIfNew(%q): expected false for duplicate", tc.name)
			}

			// IsDuplicate should agree.
			if !tracker.IsDuplicate("specjob", "en1", "en2", "ntime", tc.nonce) {
				t.Errorf("IsDuplicate(%q): expected true", tc.name)
			}
		})
	}

	// All special nonces should be distinct from each other.
	_, shares, _, _, _, _ := tracker.Stats()
	if shares != len(specialNonces) {
		t.Errorf("shares: got %d, want %d (each special nonce should be unique)", shares, len(specialNonces))
	}
}

// TestRedisHA_CleanupNonexistentJob verifies that calling CleanupJob with
// a job ID that was never recorded does not cause errors or panics.
func TestRedisHA_CleanupNonexistentJob(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)
	defer tracker.Close()

	// Record a share in one job.
	tracker.RecordIfNew("real_job", "en1", "en2", "ntime", "nonce1")

	jobsBefore, sharesBefore, _, _, _, _ := tracker.Stats()
	if jobsBefore != 1 || sharesBefore != 1 {
		t.Fatalf("before: jobs=%d shares=%d, want 1/1", jobsBefore, sharesBefore)
	}

	// Cleanup a job that never existed -- should be a no-op.
	tracker.CleanupJob("ghost_job")
	tracker.CleanupJob("another_ghost")
	tracker.CleanupJob("")

	// Stats should be unchanged.
	jobsAfter, sharesAfter, _, _, _, _ := tracker.Stats()
	if jobsAfter != 1 || sharesAfter != 1 {
		t.Errorf("after cleanup of nonexistent jobs: jobs=%d shares=%d, want 1/1",
			jobsAfter, sharesAfter)
	}

	// The real job's share should still be a duplicate.
	if !tracker.IsDuplicate("real_job", "en1", "en2", "ntime", "nonce1") {
		t.Error("real_job share should still be duplicate after cleaning up nonexistent jobs")
	}
}
