// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestRedisDedup_DefaultConfig verifies DefaultRedisDedupConfig returns correct defaults.
func TestRedisDedup_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultRedisDedupConfig()

	if cfg.Addr != "localhost:6379" {
		t.Errorf("Addr: got %q, want %q", cfg.Addr, "localhost:6379")
	}
	if cfg.Password != "" {
		t.Errorf("Password: got %q, want empty string", cfg.Password)
	}
	if cfg.DB != 0 {
		t.Errorf("DB: got %d, want 0", cfg.DB)
	}
	if cfg.TTL != 10*time.Minute {
		t.Errorf("TTL: got %v, want %v", cfg.TTL, 10*time.Minute)
	}
	if !cfg.FallbackToLocal {
		t.Error("FallbackToLocal: got false, want true")
	}
	if cfg.SentinelAddrs != nil {
		t.Errorf("SentinelAddrs: got %v, want nil", cfg.SentinelAddrs)
	}
	if cfg.SentinelMaster != "" {
		t.Errorf("SentinelMaster: got %q, want empty string", cfg.SentinelMaster)
	}
}

// TestRedisDedup_DisabledByDefault verifies that the tracker is disabled when
// REDIS_DEDUP_ENABLED is not set in the environment.
func TestRedisDedup_DisabledByDefault(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	if tracker == nil {
		t.Fatal("NewRedisDedupTracker returned nil")
	}
	if tracker.IsEnabled() {
		t.Error("IsEnabled: got true, want false (REDIS_DEDUP_ENABLED not set)")
	}
}

// TestRedisDedup_DisabledRecordIfNew_DelegatesLocal verifies that a disabled tracker
// delegates RecordIfNew to the local DuplicateTracker, returning true for a new share
// and false for a duplicate.
func TestRedisDedup_DisabledRecordIfNew_DelegatesLocal(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// First submission should be new
	ok := tracker.RecordIfNew("job1", "en1a", "en2a", "ntime1", "nonce1")
	if !ok {
		t.Error("RecordIfNew (first call): got false, want true (new share)")
	}

	// Same parameters should be duplicate
	ok = tracker.RecordIfNew("job1", "en1a", "en2a", "ntime1", "nonce1")
	if ok {
		t.Error("RecordIfNew (second call, same params): got true, want false (duplicate)")
	}
}

// TestRedisDedup_DisabledIsDuplicate_DelegatesLocal verifies that a disabled tracker
// delegates IsDuplicate to the local DuplicateTracker.
func TestRedisDedup_DisabledIsDuplicate_DelegatesLocal(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Before recording, IsDuplicate should return false
	if tracker.IsDuplicate("job1", "en1a", "en2a", "ntime1", "nonce1") {
		t.Error("IsDuplicate before recording: got true, want false")
	}

	// Record the share
	tracker.RecordIfNew("job1", "en1a", "en2a", "ntime1", "nonce1")

	// After recording, IsDuplicate should return true for same params
	if !tracker.IsDuplicate("job1", "en1a", "en2a", "ntime1", "nonce1") {
		t.Error("IsDuplicate after recording (same params): got false, want true")
	}

	// Different nonce should not be a duplicate
	if tracker.IsDuplicate("job1", "en1a", "en2a", "ntime1", "nonce2") {
		t.Error("IsDuplicate (different nonce): got true, want false")
	}
}

// TestRedisDedup_ShareKeyFormat verifies the exact key format produced by shareKey.
func TestRedisDedup_ShareKeyFormat(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	key := tracker.shareKey("job1", "en1", "en2", "ntime", "nonce")
	expected := "spiralpool:share:job1:en1:en2:ntime:nonce"
	if key != expected {
		t.Errorf("shareKey: got %q, want %q", key, expected)
	}
}

// TestRedisDedup_ShareKeyDifferentFields_NeverCollide verifies that the colon
// delimiter prevents concatenation collisions between different field values.
// Without delimiters, shareKey("a","bc","","d","e") could collide with
// shareKey("a","b","c","d","e") because the concatenation would be identical.
func TestRedisDedup_ShareKeyDifferentFields_NeverCollide(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	key1 := tracker.shareKey("a", "b", "c", "d", "e")
	key2 := tracker.shareKey("a", "bc", "", "d", "e")

	if key1 == key2 {
		t.Errorf("shareKey collision detected: both produce %q", key1)
	}
}

// TestRedisDedup_CleanupJob_ClearsLocalTracker verifies that CleanupJob removes
// shares for a specific job from the local tracker, so that a previously recorded
// share is no longer considered a duplicate.
func TestRedisDedup_CleanupJob_ClearsLocalTracker(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Record a share for job1
	tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce")

	// Verify it is recorded
	if !tracker.IsDuplicate("job1", "en1", "en2", "ntime", "nonce") {
		t.Fatal("share should be duplicate before cleanup")
	}

	// Cleanup job1
	tracker.CleanupJob("job1")

	// After cleanup, the share should no longer be considered a duplicate
	if tracker.IsDuplicate("job1", "en1", "en2", "ntime", "nonce") {
		t.Error("IsDuplicate after CleanupJob: got true, want false")
	}

	// And RecordIfNew should accept it again as new
	ok := tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce")
	if !ok {
		t.Error("RecordIfNew after CleanupJob: got false, want true (should be new again)")
	}
}

// TestRedisDedup_Stats_ReportsLocalTracker verifies that Stats returns accurate
// job/share counts from the local tracker, and that all Redis-specific counters
// are zero when the tracker is disabled.
func TestRedisDedup_Stats_ReportsLocalTracker(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Record shares across two jobs
	tracker.RecordIfNew("job1", "en1", "en2a", "ntime1", "nonce1")
	tracker.RecordIfNew("job1", "en1", "en2b", "ntime1", "nonce2")
	tracker.RecordIfNew("job2", "en1", "en2a", "ntime1", "nonce1")

	jobs, shares, redisHits, redisMisses, redisErrors, localHits := tracker.Stats()

	if jobs != 2 {
		t.Errorf("Stats jobs: got %d, want 2", jobs)
	}
	if shares != 3 {
		t.Errorf("Stats shares: got %d, want 3", shares)
	}
	if redisHits != 0 {
		t.Errorf("Stats redisHits: got %d, want 0 (disabled)", redisHits)
	}
	if redisMisses != 0 {
		t.Errorf("Stats redisMisses: got %d, want 0 (disabled)", redisMisses)
	}
	if redisErrors != 0 {
		t.Errorf("Stats redisErrors: got %d, want 0 (disabled)", redisErrors)
	}
	if localHits != 0 {
		t.Errorf("Stats localHits: got %d, want 0 (disabled)", localHits)
	}
}

// TestRedisDedup_Close_NilClient verifies that closing a disabled tracker
// (whose Redis client is nil) returns no error.
func TestRedisDedup_Close_NilClient(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	err := tracker.Close()
	if err != nil {
		t.Errorf("Close on disabled tracker (nil client): got error %v, want nil", err)
	}
}

// TestRedisDedup_ConcurrentRecordIfNew verifies that concurrent calls to
// RecordIfNew do not cause panics, data races, or deadlocks.
func TestRedisDedup_ConcurrentRecordIfNew(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			jobID := fmt.Sprintf("job%d", id%5)
			en1 := fmt.Sprintf("en1_%d", id)
			en2 := fmt.Sprintf("en2_%d", id)
			ntime := fmt.Sprintf("%08x", id)
			nonce := fmt.Sprintf("%08x", id)

			// Each goroutine records a unique share, then attempts a duplicate
			tracker.RecordIfNew(jobID, en1, en2, ntime, nonce)
			tracker.RecordIfNew(jobID, en1, en2, ntime, nonce) // duplicate
			tracker.IsDuplicate(jobID, en1, en2, ntime, nonce)
		}(i)
	}

	// Use a channel to detect if WaitGroup blocks forever (deadlock)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - all goroutines completed without panic or deadlock
	case <-time.After(10 * time.Second):
		t.Fatal("ConcurrentRecordIfNew: timed out after 10s (possible deadlock)")
	}
}

// TestRedisDedup_MultipleJobs_Isolation verifies that shares with identical
// extranonce1/extranonce2/ntime/nonce but different jobIDs are treated as
// independent shares (no cross-job collision).
func TestRedisDedup_MultipleJobs_Isolation(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Record a share for job1
	ok1 := tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce")
	if !ok1 {
		t.Error("job1 RecordIfNew: got false, want true (new share)")
	}

	// Same params but different job should also be new
	ok2 := tracker.RecordIfNew("job2", "en1", "en2", "ntime", "nonce")
	if !ok2 {
		t.Error("job2 RecordIfNew: got false, want true (different job = new share)")
	}

	// Verify job1 share is still a duplicate within job1
	if !tracker.IsDuplicate("job1", "en1", "en2", "ntime", "nonce") {
		t.Error("job1 IsDuplicate: got false, want true")
	}

	// Verify job2 share is still a duplicate within job2
	if !tracker.IsDuplicate("job2", "en1", "en2", "ntime", "nonce") {
		t.Error("job2 IsDuplicate: got false, want true")
	}

	// Cleanup job1 should not affect job2
	tracker.CleanupJob("job1")
	if tracker.IsDuplicate("job1", "en1", "en2", "ntime", "nonce") {
		t.Error("job1 IsDuplicate after cleanup: got true, want false")
	}
	if !tracker.IsDuplicate("job2", "en1", "en2", "ntime", "nonce") {
		t.Error("job2 IsDuplicate after job1 cleanup: got false, want true (should be unaffected)")
	}
}

// TestRedisDedup_RecordIfNew_ExactDuplicate verifies that recording a share and
// then recording the exact same parameters again returns false on the second call.
func TestRedisDedup_RecordIfNew_ExactDuplicate(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// First record should succeed
	first := tracker.RecordIfNew("jobX", "aaa", "bbb", "cccccccc", "dddddddd")
	if !first {
		t.Error("RecordIfNew (first): got false, want true")
	}

	// Exact duplicate should fail
	second := tracker.RecordIfNew("jobX", "aaa", "bbb", "cccccccc", "dddddddd")
	if second {
		t.Error("RecordIfNew (exact duplicate): got true, want false")
	}

	// Third attempt should still fail
	third := tracker.RecordIfNew("jobX", "aaa", "bbb", "cccccccc", "dddddddd")
	if third {
		t.Error("RecordIfNew (third attempt): got true, want false")
	}
}

// TestRedisDedup_RecordIfNew_NonceDifference verifies that shares with the same
// job/en1/en2/ntime but different nonce values are treated as distinct new shares.
func TestRedisDedup_RecordIfNew_NonceDifference(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Record with nonce "00000001"
	ok1 := tracker.RecordIfNew("jobY", "en1", "en2", "aabbccdd", "00000001")
	if !ok1 {
		t.Error("RecordIfNew (nonce 00000001): got false, want true")
	}

	// Same everything except nonce "00000002" should also be new
	ok2 := tracker.RecordIfNew("jobY", "en1", "en2", "aabbccdd", "00000002")
	if !ok2 {
		t.Error("RecordIfNew (nonce 00000002): got false, want true (different nonce = new share)")
	}

	// Verify both are now duplicates
	if !tracker.IsDuplicate("jobY", "en1", "en2", "aabbccdd", "00000001") {
		t.Error("IsDuplicate (nonce 00000001): got false, want true")
	}
	if !tracker.IsDuplicate("jobY", "en1", "en2", "aabbccdd", "00000002") {
		t.Error("IsDuplicate (nonce 00000002): got false, want true")
	}

	// Original nonce is still a duplicate if re-submitted
	ok3 := tracker.RecordIfNew("jobY", "en1", "en2", "aabbccdd", "00000001")
	if ok3 {
		t.Error("RecordIfNew (nonce 00000001, re-submit): got true, want false (duplicate)")
	}
}
