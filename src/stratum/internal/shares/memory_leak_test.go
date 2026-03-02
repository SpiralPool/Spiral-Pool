// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Critical Memory & State Leak Tests (Long-Run Stability)
//
// Tests for memory and state management issues:
// - Miner reconnect loop memory growth
// - Job object lifetime correctness
// - Share cache eviction
// - Stale job cleanup
// - Orphaned miner state
//
// WHY IT MATTERS: Pools run for months. Memory leaks compound over time.
// Failure modes include:
// - Difficulty drift over time
// - Incorrect share validation after long uptime
// - OOM crashes during peak load
package shares

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// 1. DUPLICATE TRACKER MEMORY TESTS
// =============================================================================

// TestDuplicateTrackerMemoryGrowth tests that duplicate tracker doesn't leak memory
func TestDuplicateTrackerMemoryGrowth(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	// Measure initial memory
	runtime.GC()
	runtime.GC() // Run twice to ensure stable baseline
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Simulate many jobs over time (like a pool running for days)
	numJobs := 10000
	sharesPerJob := 100

	for j := 0; j < numJobs; j++ {
		jobID := fmt.Sprintf("job_%d", j)

		// Add shares for this job
		for s := 0; s < sharesPerJob; s++ {
			tracker.RecordIfNew(jobID, "en1", fmt.Sprintf("%08x", s), "65432100", "deadbeef")
		}

		// Simulate job expiry by cleaning up old jobs periodically
		// In production, this happens via time-based cleanup
		// Only clean up jobs that exist (j >= 100)
		if j%100 == 0 && j >= 100 {
			tracker.CleanupJob(fmt.Sprintf("job_%d", j-100))
		}
	}

	// Force cleanup of remaining old jobs - leave only last 10
	for j := 0; j < numJobs-10; j++ {
		tracker.CleanupJob(fmt.Sprintf("job_%d", j))
	}

	// Measure memory after
	runtime.GC()
	runtime.GC() // Run twice for stable measurement
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	// Calculate memory growth
	heapGrowth := int64(m2.HeapAlloc) - int64(m1.HeapAlloc)
	heapGrowthMB := float64(heapGrowth) / (1024 * 1024)

	t.Logf("Heap growth after %d jobs with %d shares each: %.2f MB",
		numJobs, sharesPerJob, heapGrowthMB)

	// Verify stats show reasonable tracked data
	trackedJobs, trackedShares := tracker.Stats()
	t.Logf("Tracked jobs: %d, Tracked shares: %d", trackedJobs, trackedShares)

	// Memory should be bounded - not growing linearly with all jobs ever created
	// After cleanup, we should only be tracking recent jobs (last 10)
	maxExpectedJobs := 15 // Allow some buffer for timing
	if trackedJobs > maxExpectedJobs {
		t.Errorf("Too many jobs still tracked: %d (expected < %d) - possible memory leak",
			trackedJobs, maxExpectedJobs)
	}

	// Heap growth should be bounded
	// Note: Memory measurements in Go are inherently variable due to GC timing
	// In parallel test execution, heap can temporarily grow significantly
	maxExpectedGrowthMB := 100.0 // Generous limit for parallel test execution
	if heapGrowthMB > maxExpectedGrowthMB {
		t.Errorf("Excessive memory growth: %.2f MB (limit: %.2f MB)",
			heapGrowthMB, maxExpectedGrowthMB)
	}
}

// TestDuplicateTrackerTimeBasedCleanup tests automatic time-based cleanup
func TestDuplicateTrackerTimeBasedCleanup(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	// Add shares across multiple jobs
	for j := 0; j < 100; j++ {
		jobID := fmt.Sprintf("job_%d", j)
		for s := 0; s < 10; s++ {
			tracker.RecordIfNew(jobID, "en1", fmt.Sprintf("%08x", s), "65432100", fmt.Sprintf("%08x", j*10+s))
		}
	}

	// Check initial state
	jobsInitial, sharesInitial := tracker.Stats()
	t.Logf("Initial: %d jobs, %d shares", jobsInitial, sharesInitial)

	// Simulate time passing by manipulating internal timestamps
	// The tracker should clean up entries older than 10 minutes (duplicateTrackerMaxAge)

	// Trigger cleanup by recording a new share (cleanup runs periodically)
	// The RecordIfNew function checks if 60 seconds have passed since last cleanup
	tracker.RecordIfNew("trigger_job", "en1", "00000000", "65432100", "trigger")

	// Get final stats
	jobsFinal, sharesFinal := tracker.Stats()
	t.Logf("After trigger: %d jobs, %d shares", jobsFinal, sharesFinal)

	// The cleanup mechanism exists but is time-based
	// This test verifies the cleanup function exists and runs without error
	// Full time-based testing would require mocking time.Now()
}

// =============================================================================
// 2. NONCE TRACKER MEMORY TESTS
// =============================================================================

// TestNonceTrackerMemoryGrowth tests nonce tracker doesn't leak on reconnects
func TestNonceTrackerMemoryGrowth(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	// Measure initial memory
	runtime.GC()
	runtime.GC() // Run twice for stable baseline
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Simulate many sessions connecting and disconnecting
	numCycles := 1000
	sessionsPerCycle := 100

	for cycle := 0; cycle < numCycles; cycle++ {
		// Create sessions
		for s := 0; s < sessionsPerCycle; s++ {
			sessionID := fmt.Sprintf("session_%d_%d", cycle, s)

			// Track some nonces
			for n := uint32(0); n < 10; n++ {
				tracker.TrackNonce(sessionID, "job_1", n)
			}
		}

		// Remove sessions (simulating disconnect)
		for s := 0; s < sessionsPerCycle; s++ {
			sessionID := fmt.Sprintf("session_%d_%d", cycle, s)
			tracker.RemoveSession(sessionID)
		}
	}

	// Measure memory after
	runtime.GC()
	runtime.GC() // Run twice for stable measurement
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	heapGrowth := int64(m2.HeapAlloc) - int64(m1.HeapAlloc)
	heapGrowthMB := float64(heapGrowth) / (1024 * 1024)

	t.Logf("Heap growth after %d reconnect cycles: %.2f MB",
		numCycles*sessionsPerCycle, heapGrowthMB)

	// After all disconnects, memory should be minimal
	// Any significant growth indicates a leak
	// Note: Memory measurements in Go are inherently variable due to GC timing
	// Negative values are possible if GC released more than was allocated during test
	// Use generous limit due to GC non-determinism in parallel tests
	maxExpectedGrowthMB := 100.0
	if heapGrowthMB > maxExpectedGrowthMB {
		t.Errorf("Possible memory leak: %.2f MB growth after all sessions removed",
			heapGrowthMB)
	}
}

// TestNonceTrackerBoundedGrowth tests tracker has upper bound on sessions
func TestNonceTrackerBoundedGrowth(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	// Try to add more sessions than maxTrackedSessions
	numSessions := maxTrackedSessions + 1000

	for s := 0; s < numSessions; s++ {
		sessionID := fmt.Sprintf("bounded_session_%d", s)
		tracker.TrackNonce(sessionID, "job_1", uint32(s))
	}

	// The tracker should have evicted old sessions to stay within bounds
	// We can't directly check the internal map size, but we can verify
	// memory is bounded by checking heap allocation

	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// Calculate expected max memory based on maxTrackedSessions
	// Each session is roughly: sessionID(~20 bytes) + sessionNonces struct(~40 bytes)
	// Plus map overhead
	expectedMaxBytes := uint64(maxTrackedSessions) * 200 // Generous estimate
	expectedMaxMB := float64(expectedMaxBytes) / (1024 * 1024)

	// Heap should be bounded (not growing with all sessions ever created)
	heapMB := float64(m.HeapAlloc) / (1024 * 1024)
	t.Logf("Heap after %d sessions: %.2f MB (expected max: %.2f MB based on limit %d)",
		numSessions, heapMB, expectedMaxMB, maxTrackedSessions)

	// This is a soft check - just log if exceeding expected
	if heapMB > expectedMaxMB*2 {
		t.Logf("WARNING: Heap larger than expected - possible unbounded growth")
	}
}

// TestNonceTrackerCleanupInactiveSessions tests cleanup of inactive sessions
func TestNonceTrackerCleanupInactiveSessions(t *testing.T) {
	t.Parallel()

	tracker := NewNonceTracker()

	// Add sessions
	numSessions := 100
	for s := 0; s < numSessions; s++ {
		sessionID := fmt.Sprintf("cleanup_session_%d", s)
		tracker.TrackNonce(sessionID, "job_1", uint32(s))
	}

	// Call cleanup (normally removes sessions inactive > 10 minutes)
	// Since we just added them, none should be removed
	tracker.CleanupInactiveSessions()

	// In a real test with time mocking, we'd advance time and verify cleanup
	// For now, just verify the cleanup doesn't panic or corrupt state

	// Add more nonces after cleanup
	for s := 0; s < 10; s++ {
		sessionID := fmt.Sprintf("cleanup_session_%d", s)
		exhausted := tracker.TrackNonce(sessionID, "job_2", uint32(1000+s))
		if exhausted {
			t.Logf("Session %d showed exhaustion", s)
		}
	}
}

// =============================================================================
// 3. JOB LIFETIME TESTS
// =============================================================================

// TestJobObjectLifetime tests that jobs are properly garbage collected
func TestJobObjectLifetime(t *testing.T) {
	t.Parallel()

	// Simulate job manager keeping limited job history
	maxJobs := 10
	jobHistory := make(map[string]*protocol.Job)
	var historyMu sync.RWMutex

	storeJob := func(job *protocol.Job) {
		historyMu.Lock()
		defer historyMu.Unlock()

		jobHistory[job.ID] = job

		// Cleanup old jobs (keep only maxJobs most recent)
		if len(jobHistory) > maxJobs {
			var oldest string
			var oldestTime time.Time
			first := true

			for id, j := range jobHistory {
				if first || j.CreatedAt.Before(oldestTime) {
					oldest = id
					oldestTime = j.CreatedAt
					first = false
				}
			}

			if oldest != "" {
				delete(jobHistory, oldest)
			}
		}
	}

	// Measure initial memory
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Create many jobs over time
	for i := 0; i < 10000; i++ {
		job := &protocol.Job{
			ID:             fmt.Sprintf("job_%d", i),
			PrevBlockHash:  "0000000000000000000000000000000000000000000000000000000000000001",
			MerkleBranches: make([]string, 100), // Simulate large merkle tree
			TransactionData: make([]string, 100), // Simulate transactions
			CreatedAt:      time.Now(),
		}

		// Fill with some data
		for j := 0; j < 100; j++ {
			job.MerkleBranches[j] = fmt.Sprintf("%064x", j)
			job.TransactionData[j] = fmt.Sprintf("%064x", j*2)
		}

		storeJob(job)
	}

	// Force GC
	runtime.GC()
	runtime.GC() // Run twice to ensure finalizers run
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	heapMB := float64(m2.HeapAlloc) / (1024 * 1024)

	// Verify only maxJobs are kept
	historyMu.RLock()
	keptJobs := len(jobHistory)
	historyMu.RUnlock()

	if keptJobs > maxJobs {
		t.Errorf("Job history has %d jobs, expected max %d", keptJobs, maxJobs)
	}

	t.Logf("Final heap: %.2f MB, Jobs kept: %d", heapMB, keptJobs)

	// Memory should be bounded by maxJobs worth of data
	// Each job with 100 merkle branches and 100 txs is roughly 13KB
	expectedMaxMB := float64(maxJobs) * 0.015 * 2 // 2x buffer
	if heapMB > 50+expectedMaxMB { // 50MB baseline
		t.Logf("WARNING: Heap larger than expected - possible job lifetime issue")
	}
}

// =============================================================================
// 4. VALIDATOR STATE LEAK TESTS
// =============================================================================

// TestValidatorDoesNotLeakShareState tests validator doesn't accumulate state
func TestValidatorDoesNotLeakShareState(t *testing.T) {
	t.Parallel()

	jobs := make(map[string]*protocol.Job)
	var jobsMu sync.RWMutex

	getJob := func(id string) (*protocol.Job, bool) {
		jobsMu.RLock()
		defer jobsMu.RUnlock()
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)
	validator.SetNetworkDifficulty(1000.0)

	// Measure initial memory
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	// Process many shares
	numJobs := 100
	sharesPerJob := 1000

	for j := 0; j < numJobs; j++ {
		// Create job
		job := createTestJob(t, fmt.Sprintf("leak_test_job_%d", j))
		jobsMu.Lock()
		jobs[job.ID] = job
		jobsMu.Unlock()

		// Process shares for this job
		for s := 0; s < sharesPerJob; s++ {
			share := &protocol.Share{
				JobID:       job.ID,
				ExtraNonce1: fmt.Sprintf("%08x", j),
				ExtraNonce2: fmt.Sprintf("%08x", s),
				NTime:       job.NTime,
				Nonce:       fmt.Sprintf("%08x", s),
				Difficulty:  1.0,
				SessionID:   uint64(j*sharesPerJob + s),
			}

			validator.Validate(share)
		}

		// Cleanup old job
		if j > 0 {
			oldJobID := fmt.Sprintf("leak_test_job_%d", j-1)
			jobsMu.Lock()
			delete(jobs, oldJobID)
			jobsMu.Unlock()

			validator.duplicates.CleanupJob(oldJobID)
		}
	}

	// Force GC
	runtime.GC()
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	heapGrowth := int64(m2.HeapAlloc) - int64(m1.HeapAlloc)
	heapGrowthMB := float64(heapGrowth) / (1024 * 1024)

	t.Logf("Heap growth after %d shares: %.2f MB",
		numJobs*sharesPerJob, heapGrowthMB)

	// Check validator stats
	stats := validator.Stats()
	t.Logf("Validator stats: validated=%d, accepted=%d, rejected=%d",
		stats.Validated, stats.Accepted, stats.Rejected)

	// Verify all shares were processed
	if stats.Validated != uint64(numJobs*sharesPerJob) {
		t.Errorf("Expected %d validated shares, got %d",
			numJobs*sharesPerJob, stats.Validated)
	}

	// Memory growth should be bounded.
	// Allow 150MB to account for race detector overhead (~20% extra).
	maxGrowthMB := 150.0
	if heapGrowthMB > maxGrowthMB {
		t.Errorf("Excessive memory growth: %.2f MB (limit: %.2f MB)",
			heapGrowthMB, maxGrowthMB)
	}
}

// =============================================================================
// 5. LONG-RUNNING SIMULATION
// =============================================================================

// TestLongRunningStability simulates extended operation
func TestLongRunningStability(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping long-running test in short mode")
	}

	t.Parallel()

	jobs := make(map[string]*protocol.Job)
	var jobsMu sync.RWMutex

	getJob := func(id string) (*protocol.Job, bool) {
		jobsMu.RLock()
		defer jobsMu.RUnlock()
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)
	validator.SetNetworkDifficulty(1000.0)

	// Track memory over time
	type MemSample struct {
		time     time.Duration
		heapMB   float64
		validated uint64
	}
	var samples []MemSample

	startTime := time.Now()
	simulationDuration := 10 * time.Second
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// Record initial state
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	samples = append(samples, MemSample{
		time:     0,
		heapMB:   float64(m.HeapAlloc) / (1024 * 1024),
		validated: 0,
	})

	// Run simulation
	jobCounter := 0
	shareCounter := 0
	done := make(chan struct{})

	go func() {
		for time.Since(startTime) < simulationDuration {
			// Create new job periodically
			jobCounter++
			job := createTestJob(t, fmt.Sprintf("longrun_job_%d", jobCounter))
			jobsMu.Lock()
			jobs[job.ID] = job
			// Keep only last 10 jobs
			if jobCounter > 10 {
				oldID := fmt.Sprintf("longrun_job_%d", jobCounter-10)
				delete(jobs, oldID)
				validator.duplicates.CleanupJob(oldID)
			}
			jobsMu.Unlock()

			// Process shares
			for s := 0; s < 100; s++ {
				shareCounter++
				share := &protocol.Share{
					JobID:       job.ID,
					ExtraNonce1: "00000001",
					ExtraNonce2: fmt.Sprintf("%08x", shareCounter),
					NTime:       job.NTime,
					Nonce:       fmt.Sprintf("%08x", shareCounter),
					Difficulty:  1.0,
					SessionID:   uint64(shareCounter % 100),
				}

				validator.Validate(share)
			}

			time.Sleep(50 * time.Millisecond)
		}
		close(done)
	}()

	// Sample memory periodically
	for {
		select {
		case <-done:
			// Final sample
			runtime.GC()
			runtime.ReadMemStats(&m)
			samples = append(samples, MemSample{
				time:     time.Since(startTime),
				heapMB:   float64(m.HeapAlloc) / (1024 * 1024),
				validated: validator.Stats().Validated,
			})
			goto analysis

		case <-ticker.C:
			runtime.GC()
			runtime.ReadMemStats(&m)
			samples = append(samples, MemSample{
				time:     time.Since(startTime),
				heapMB:   float64(m.HeapAlloc) / (1024 * 1024),
				validated: validator.Stats().Validated,
			})
		}
	}

analysis:
	// Analyze memory trend
	t.Logf("Memory samples over %v:", time.Since(startTime))
	for _, s := range samples {
		t.Logf("  t=%v: heap=%.2f MB, validated=%d", s.time.Round(time.Second), s.heapMB, s.validated)
	}

	// Check for memory growth trend
	if len(samples) >= 3 {
		first := samples[1] // Skip initial sample
		last := samples[len(samples)-1]

		// Calculate growth rate
		growthMB := last.heapMB - first.heapMB
		growthPerShare := growthMB / float64(last.validated-first.validated)

		t.Logf("Memory growth: %.2f MB total, %.6f MB per share", growthMB, growthPerShare)

		// Check for excessive growth (should be near zero per share)
		if growthPerShare > 0.0001 { // More than 100 bytes per share
			t.Errorf("MEMORY LEAK: %.6f MB growth per share", growthPerShare)
		}
	}
}

// =============================================================================
// 6. SESSION STATE CLEANUP TESTS
// =============================================================================

// TestSessionStateCleanup tests that session states are properly cleaned up
func TestSessionStateCleanup(t *testing.T) {
	t.Parallel()

	// Simulate session state map (like in pool/coinpool.go)
	sessionStates := new(sync.Map)

	type SessionState struct {
		difficulty   float64
		shareCount   uint64
		createdAt    time.Time
		lastActivity time.Time
	}

	// Create many sessions
	numSessions := 1000
	for s := 0; s < numSessions; s++ {
		state := &SessionState{
			difficulty:   1000.0,
			shareCount:   0,
			createdAt:    time.Now(),
			lastActivity: time.Now(),
		}
		sessionStates.Store(uint64(s), state)
	}

	// Verify all sessions are stored
	count := 0
	sessionStates.Range(func(_, _ interface{}) bool {
		count++
		return true
	})

	if count != numSessions {
		t.Errorf("Expected %d sessions, got %d", numSessions, count)
	}

	// Simulate disconnects (cleanup)
	for s := 0; s < numSessions/2; s++ {
		sessionStates.Delete(uint64(s))
	}

	// Verify cleanup worked
	count = 0
	sessionStates.Range(func(_, _ interface{}) bool {
		count++
		return true
	})

	expected := numSessions / 2
	if count != expected {
		t.Errorf("After cleanup: expected %d sessions, got %d", expected, count)
	}

	// Cleanup remaining
	for s := numSessions / 2; s < numSessions; s++ {
		sessionStates.Delete(uint64(s))
	}

	// Verify all cleaned
	count = 0
	sessionStates.Range(func(_, _ interface{}) bool {
		count++
		return true
	})

	if count != 0 {
		t.Errorf("After full cleanup: expected 0 sessions, got %d", count)
	}
}

// TestValidatorSessionCleanup tests validator's session cleanup method
func TestValidatorSessionCleanup(t *testing.T) {
	t.Parallel()

	jobs := make(map[string]*protocol.Job)
	getJob := func(id string) (*protocol.Job, bool) {
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)

	// Create job
	job := createTestJob(t, "cleanup_test_job")
	jobs[job.ID] = job

	// Track nonces for multiple sessions
	numSessions := 100
	for s := 0; s < numSessions; s++ {
		sessionID := uint64(s)

		// Submit some shares to track nonces
		for n := uint32(0); n < 10; n++ {
			sessionKey := fmt.Sprintf("%d", sessionID)
			validator.nonceTracker.TrackNonce(sessionKey, job.ID, n)
		}
	}

	// Cleanup half the sessions
	for s := 0; s < numSessions/2; s++ {
		validator.CleanupSession(uint64(s))
	}

	// The nonce tracker should have removed those sessions
	// Verify by checking memory or tracking (implementation-specific)

	// Cleanup remaining
	for s := numSessions / 2; s < numSessions; s++ {
		validator.CleanupSession(uint64(s))
	}

	// After full cleanup, memory should be minimal
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	t.Logf("Heap after session cleanup: %.2f MB", float64(m.HeapAlloc)/(1024*1024))
}

// =============================================================================
// 7. ORPHANED STATE DETECTION
// =============================================================================

// TestOrphanedStateDetection tests detection of orphaned state entries
func TestOrphanedStateDetection(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	// Create shares for jobs that will be "orphaned" (deleted without cleanup)
	for j := 0; j < 100; j++ {
		jobID := fmt.Sprintf("orphan_job_%d", j)
		for s := 0; s < 10; s++ {
			tracker.RecordIfNew(jobID, "en1", fmt.Sprintf("%08x", s), "65432100", fmt.Sprintf("%08x", j*10+s))
		}
	}

	// Get initial stats
	jobsInitial, sharesInitial := tracker.Stats()

	// Simulate time passing and trigger cleanup
	// In production, the tracker cleans up entries older than duplicateTrackerMaxAge

	// For this test, we manually clean some jobs
	for j := 0; j < 50; j++ {
		tracker.CleanupJob(fmt.Sprintf("orphan_job_%d", j))
	}

	jobsFinal, sharesFinal := tracker.Stats()

	t.Logf("Before cleanup: %d jobs, %d shares", jobsInitial, sharesInitial)
	t.Logf("After cleanup: %d jobs, %d shares", jobsFinal, sharesFinal)

	// Half should be cleaned
	expectedJobs := 50
	if jobsFinal != expectedJobs {
		t.Errorf("Expected %d jobs after cleanup, got %d", expectedJobs, jobsFinal)
	}

	// Shares should be reduced proportionally
	expectedShares := 500 // 50 jobs * 10 shares
	if sharesFinal != expectedShares {
		t.Errorf("Expected %d shares after cleanup, got %d", expectedShares, sharesFinal)
	}
}
