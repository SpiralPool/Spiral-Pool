// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestRedisHA_FallbackToLocal_OnDisabled verifies that when Redis is disabled,
// all operations fall back to the local in-memory tracker transparently.
func TestRedisHA_FallbackToLocal_OnDisabled(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	if tracker == nil {
		t.Fatal("tracker should not be nil")
	}
	if tracker.IsEnabled() {
		t.Error("tracker should be disabled (no REDIS_DEDUP_ENABLED env)")
	}

	// Operations should still work via local fallback
	ok := tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce1")
	if !ok {
		t.Error("RecordIfNew should return true for new share")
	}

	// Duplicate should be caught locally
	ok = tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce1")
	if ok {
		t.Error("RecordIfNew should return false for duplicate share")
	}
}

// TestRedisHA_ClientNilWhenDisabled verifies Client() returns nil when
// Redis is not connected, preventing accidental Redis operations.
func TestRedisHA_ClientNilWhenDisabled(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	if tracker.Client() != nil {
		t.Error("Client() should return nil when Redis is not connected")
	}
}

// TestRedisHA_CloseNilClient verifies closing a tracker with no Redis
// connection doesn't panic or return an error.
func TestRedisHA_CloseNilClient(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	if err := tracker.Close(); err != nil {
		t.Errorf("Close() with nil client should return nil, got: %v", err)
	}
}

// TestRedisHA_HealthCheckDoesNotPanic verifies that starting the health
// check loop on a disabled tracker doesn't panic.
func TestRedisHA_HealthCheckDoesNotPanic(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// This should not panic even with nil client
	tracker.StartHealthCheck(ctx)

	// Wait for context to expire
	<-ctx.Done()
}

// TestRedisHA_StatsAccurateForLocalFallback verifies that stats are tracked
// correctly when using local fallback.
func TestRedisHA_StatsAccurateForLocalFallback(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Record some shares
	tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce1")
	tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce2")
	tracker.RecordIfNew("job2", "en1", "en2", "ntime", "nonce1")

	jobs, shares, redisHits, redisMisses, redisErrors, localHits := tracker.Stats()

	// Should have 2 jobs and 3 shares tracked locally
	if jobs != 2 {
		t.Errorf("jobs: got %d, want 2", jobs)
	}
	if shares != 3 {
		t.Errorf("shares: got %d, want 3", shares)
	}

	// No Redis activity expected
	if redisHits != 0 {
		t.Errorf("redisHits: got %d, want 0", redisHits)
	}
	if redisMisses != 0 {
		t.Errorf("redisMisses: got %d, want 0", redisMisses)
	}
	if redisErrors != 0 {
		t.Errorf("redisErrors: got %d, want 0", redisErrors)
	}
	if localHits != 0 {
		t.Errorf("localHits: got %d, want 0 (no redis errors to trigger fallback)", localHits)
	}
}

// TestRedisHA_ConcurrentFallbackSafe verifies that concurrent operations
// on the local fallback tracker don't race.
func TestRedisHA_ConcurrentFallbackSafe(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	var wg sync.WaitGroup
	const goroutines = 10
	const sharesPerGoroutine = 100

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gID int) {
			defer wg.Done()
			for s := 0; s < sharesPerGoroutine; s++ {
				jobID := "job1"
				nonce := fmt.Sprintf("nonce_%d_%d", gID, s)
				tracker.RecordIfNew(jobID, "en1", "en2", "ntime", nonce)
			}
		}(g)
	}
	wg.Wait()

	jobs, shares, _, _, _, _ := tracker.Stats()
	if jobs != 1 {
		t.Errorf("jobs: got %d, want 1", jobs)
	}

	expectedShares := goroutines * sharesPerGoroutine
	if shares != expectedShares {
		t.Errorf("shares: got %d, want %d", shares, expectedShares)
	}
}

// TestRedisHA_CleanupJobWorksInFallback verifies CleanupJob removes
// tracked shares even in local-only mode.
func TestRedisHA_CleanupJobWorksInFallback(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Record shares for two jobs
	tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce1")
	tracker.RecordIfNew("job2", "en1", "en2", "ntime", "nonce1")

	jobs, shares, _, _, _, _ := tracker.Stats()
	if jobs != 2 || shares != 2 {
		t.Fatalf("before cleanup: jobs=%d shares=%d, want 2/2", jobs, shares)
	}

	// Cleanup job1
	tracker.CleanupJob("job1")

	jobs, shares, _, _, _, _ = tracker.Stats()
	if jobs != 1 {
		t.Errorf("after cleanup: jobs=%d, want 1", jobs)
	}
	if shares != 1 {
		t.Errorf("after cleanup: shares=%d, want 1", shares)
	}

	// Re-recording the cleaned-up share should succeed (it's "new" again)
	ok := tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce1")
	if !ok {
		t.Error("RecordIfNew after CleanupJob should return true")
	}
}

// TestRedisHA_IsDuplicate_FallbackConsistency verifies IsDuplicate
// and RecordIfNew are consistent when using local fallback.
func TestRedisHA_IsDuplicate_FallbackConsistency(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	cfg := DefaultRedisDedupConfig()
	tracker := NewRedisDedupTracker(cfg, logger)

	// Before recording, should not be duplicate
	if tracker.IsDuplicate("job1", "en1", "en2", "ntime", "nonce1") {
		t.Error("IsDuplicate should return false for unrecorded share")
	}

	// Record the share
	tracker.RecordIfNew("job1", "en1", "en2", "ntime", "nonce1")

	// Now should be duplicate
	if !tracker.IsDuplicate("job1", "en1", "en2", "ntime", "nonce1") {
		t.Error("IsDuplicate should return true for recorded share")
	}
}
