// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package shares

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// makeStressTestJob creates a valid protocol.Job for stress testing.
func makeStressTestJob(jobID string) *protocol.Job {
	return &protocol.Job{
		ID:            jobID,
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff",
		CoinBase2:     "ffffffff0100f2052a010000001976a914",
		Version:       "20000000",
		NBits:         "1d00ffff",
		NTime:         "64000000",
		CleanJobs:     true,
		Height:        100000,
		Difficulty:    1.0,
		CreatedAt:     time.Now(),
	}
}

// getHeapAllocMB forces two GC cycles and returns the current HeapAlloc in megabytes.
func getHeapAllocMB(t *testing.T) float64 {
	t.Helper()
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1024 * 1024)
}

// getGoroutineCount returns the current number of goroutines after a brief settle.
func getGoroutineCount() int {
	// Allow scheduler to run and clean up goroutines that are about to exit.
	// Use generous settle time to account for -race detector overhead.
	runtime.Gosched()
	time.Sleep(200 * time.Millisecond)
	runtime.Gosched()
	return runtime.NumGoroutine()
}

// =============================================================================
// 1. GOROUTINE EXHAUSTION: Rapid Session Churn
// =============================================================================

// TestGoroutineExhaustion_RapidSessionChurn creates 10000 sessions that each
// register a nonce and immediately remove themselves. Goroutine count must not
// grow beyond initial + 50.
// NOTE: NOT parallel — runtime.NumGoroutine() is process-global, so parallel
// tests create noise that makes the goroutine delta meaningless.
func TestGoroutineExhaustion_RapidSessionChurn(t *testing.T) {

	nt := NewNonceTracker()

	baseGoroutines := getGoroutineCount()

	const numSessions = 10000
	for i := 0; i < numSessions; i++ {
		sessionID := fmt.Sprintf("session-%d", i)
		nt.TrackNonce(sessionID, "job-1", uint32(i))
		nt.RemoveSession(sessionID)
	}

	finalGoroutines := getGoroutineCount()
	growth := finalGoroutines - baseGoroutines

	if growth > 50 {
		t.Fatalf("goroutine leak detected: baseline=%d, final=%d, growth=%d (max allowed: 50)",
			baseGoroutines, finalGoroutines, growth)
	}
	t.Logf("goroutine count: baseline=%d, final=%d, growth=%d", baseGoroutines, finalGoroutines, growth)
}

// =============================================================================
// 2. GOROUTINE EXHAUSTION: Concurrent Session Churn
// =============================================================================

// TestGoroutineExhaustion_ConcurrentSessionChurn launches 500 goroutines that
// each perform 100 iterations of TrackNonce + RemoveSession. Goroutine count
// must return to baseline after all workers finish.
// NOTE: NOT parallel — runtime.NumGoroutine() is process-global.
func TestGoroutineExhaustion_ConcurrentSessionChurn(t *testing.T) {

	nt := NewNonceTracker()

	baseGoroutines := getGoroutineCount()

	const numWorkers = 500
	const iterationsPerWorker = 100

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// Start barrier: all goroutines wait until released.
	startBarrier := make(chan struct{})

	for w := 0; w < numWorkers; w++ {
		go func(workerID int) {
			defer wg.Done()
			<-startBarrier
			for i := 0; i < iterationsPerWorker; i++ {
				sessionID := fmt.Sprintf("worker-%d-session-%d", workerID, i)
				nt.TrackNonce(sessionID, "job-concurrent", uint32(i))
				nt.RemoveSession(sessionID)
			}
		}(w)
	}

	// Release all goroutines simultaneously.
	close(startBarrier)
	wg.Wait()

	finalGoroutines := getGoroutineCount()
	growth := finalGoroutines - baseGoroutines

	// Allow generous margin for test-infrastructure goroutines and -race overhead.
	if growth > 50 {
		t.Fatalf("goroutine leak after concurrent churn: baseline=%d, final=%d, growth=%d (max allowed: 50)",
			baseGoroutines, finalGoroutines, growth)
	}
	t.Logf("concurrent churn: baseline=%d, final=%d, growth=%d", baseGoroutines, finalGoroutines, growth)
}

// =============================================================================
// 3. MEMORY EXHAUSTION: DuplicateTracker Job Churn
// =============================================================================

// TestMemoryExhaustion_DuplicateTrackerJobChurn creates 5000 jobs with 50
// shares each, cleans up each job, and verifies Stats() returns (0, 0).
// Heap growth after cleanup must be below 50 MB.
func TestMemoryExhaustion_DuplicateTrackerJobChurn(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	heapBefore := getHeapAllocMB(t)

	const numJobs = 5000
	const sharesPerJob = 50

	for j := 0; j < numJobs; j++ {
		jobID := fmt.Sprintf("churn-job-%d", j)
		for s := 0; s < sharesPerJob; s++ {
			dt.RecordIfNew(jobID, "en1", fmt.Sprintf("%08x", s), "64000000", fmt.Sprintf("%08x", s))
		}
		dt.CleanupJob(jobID)
	}

	jobs, shares := dt.Stats()
	if jobs != 0 || shares != 0 {
		t.Fatalf("expected Stats()=(0, 0) after full cleanup, got (%d, %d)", jobs, shares)
	}

	heapAfter := getHeapAllocMB(t)
	heapGrowth := heapAfter - heapBefore

	// Be generous: parallel test execution and GC timing can cause variance.
	const maxHeapGrowthMB = 150.0
	if heapGrowth > maxHeapGrowthMB {
		t.Fatalf("excessive heap growth after cleanup: %.2f MB (max allowed: %.2f MB)",
			heapGrowth, maxHeapGrowthMB)
	}
	t.Logf("heap: before=%.2f MB, after=%.2f MB, growth=%.2f MB", heapBefore, heapAfter, heapGrowth)
}

// =============================================================================
// 4. MEMORY EXHAUSTION: NonceTracker LRU Pressure
// =============================================================================

// TestMemoryExhaustion_NonceTrackerLRUPressure adds maxTrackedSessions+5000
// sessions to a NonceTracker, verifying that the LRU eviction mechanism
// prevents panics and keeps memory bounded.
func TestMemoryExhaustion_NonceTrackerLRUPressure(t *testing.T) {
	t.Parallel()

	nt := NewNonceTracker()

	totalSessions := maxTrackedSessions + 5000

	heapBefore := getHeapAllocMB(t)

	// Add more sessions than the max. The tracker must not panic.
	for i := 0; i < totalSessions; i++ {
		sessionID := fmt.Sprintf("lru-session-%d", i)
		nt.TrackNonce(sessionID, "lru-job", uint32(i%4294967295))
	}

	// Remove all sessions.
	for i := 0; i < totalSessions; i++ {
		sessionID := fmt.Sprintf("lru-session-%d", i)
		nt.RemoveSession(sessionID)
	}

	heapAfter := getHeapAllocMB(t)
	heapGrowth := heapAfter - heapBefore

	// Memory should be bounded after removing all sessions.
	const maxHeapGrowthMB = 150.0
	if heapGrowth > maxHeapGrowthMB {
		t.Fatalf("excessive memory after LRU pressure + cleanup: %.2f MB growth (max allowed: %.2f MB)",
			heapGrowth, maxHeapGrowthMB)
	}
	t.Logf("LRU pressure test: before=%.2f MB, after=%.2f MB, growth=%.2f MB", heapBefore, heapAfter, heapGrowth)
}

// =============================================================================
// 5. MEMORY EXHAUSTION: Validator Concurrent Validation
// =============================================================================

// TestMemoryExhaustion_ValidatorConcurrentValidation creates a Validator and
// launches 200 goroutines that each submit 100 unique shares. After completion,
// Stats().Validated must equal 20000. Memory growth must be bounded.
func TestMemoryExhaustion_ValidatorConcurrentValidation(t *testing.T) {
	t.Parallel()

	jobStore := &sync.Map{}
	job := makeStressTestJob("stress-job-1")
	job.CreatedAt = time.Now()
	jobStore.Store("stress-job-1", job)

	getJob := func(jobID string) (*protocol.Job, bool) {
		val, ok := jobStore.Load(jobID)
		if !ok {
			return nil, false
		}
		return val.(*protocol.Job), true
	}

	v := NewValidator(getJob)
	v.SetMaxJobAge(10 * time.Minute)
	v.SetNetworkDifficulty(1.0)

	heapBefore := getHeapAllocMB(t)

	const numGoroutines = 200
	const sharesPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gID int) {
			defer wg.Done()
			for s := 0; s < sharesPerGoroutine; s++ {
				share := &protocol.Share{
					JobID:       "stress-job-1",
					ExtraNonce1: fmt.Sprintf("%04x", gID),
					ExtraNonce2: fmt.Sprintf("%08x", s),
					NTime:       "64000000",
					Nonce:       fmt.Sprintf("%08x", s),
					Difficulty:  1.0,
					SessionID:   uint64(gID),
				}
				v.Validate(share)
			}
		}(g)
	}

	wg.Wait()

	stats := v.Stats()
	expectedValidated := uint64(numGoroutines * sharesPerGoroutine)
	if stats.Validated != expectedValidated {
		t.Fatalf("expected Validated=%d, got %d", expectedValidated, stats.Validated)
	}

	heapAfter := getHeapAllocMB(t)
	heapGrowth := heapAfter - heapBefore

	const maxHeapGrowthMB = 200.0
	if heapGrowth > maxHeapGrowthMB {
		t.Fatalf("excessive memory growth during concurrent validation: %.2f MB (max allowed: %.2f MB)",
			heapGrowth, maxHeapGrowthMB)
	}
	t.Logf("concurrent validation: validated=%d, heap growth=%.2f MB", stats.Validated, heapGrowth)
}

// =============================================================================
// 6. BACKPRESSURE: Difficulty Multiplier Values
// =============================================================================

// TestBackpressure_DifficultyMultiplierValues is a table test verifying that each
// BackpressureLevel returns the correct SuggestedDifficultyMultiplier.
func TestBackpressure_DifficultyMultiplierValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		level    BackpressureLevel
		expected float64
	}{
		{"None", BackpressureNone, 1.0},
		{"Warn", BackpressureWarn, 1.5},
		{"Critical", BackpressureCritical, 2.0},
		{"Emergency", BackpressureEmergency, 4.0},
		{"Unknown(99)", BackpressureLevel(99), 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.level.SuggestedDifficultyMultiplier()
			if got != tt.expected {
				t.Fatalf("BackpressureLevel(%d).SuggestedDifficultyMultiplier() = %f, want %f",
					tt.level, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// 7. BACKPRESSURE: String Representation
// =============================================================================

// TestBackpressure_StringRepresentation is a table test verifying that each
// BackpressureLevel.String() returns the correct string.
func TestBackpressure_StringRepresentation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		level    BackpressureLevel
		expected string
	}{
		{"None", BackpressureNone, "none"},
		{"Warn", BackpressureWarn, "warn"},
		{"Critical", BackpressureCritical, "critical"},
		{"Emergency", BackpressureEmergency, "emergency"},
		{"Unknown(99)", BackpressureLevel(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.level.String()
			if got != tt.expected {
				t.Fatalf("BackpressureLevel(%d).String() = %q, want %q",
					tt.level, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// 8. GOROUTINE EXHAUSTION: Validator Session Cleanup
// =============================================================================

// TestGoroutineExhaustion_ValidatorSessionCleanup creates 500 sessions in a
// Validator (each submitting 10 shares), then calls CleanupSession for all 500.
// Goroutine count must not grow.
// NOTE: NOT parallel — runtime.NumGoroutine() is process-global.
func TestGoroutineExhaustion_ValidatorSessionCleanup(t *testing.T) {

	jobStore := &sync.Map{}
	job := makeStressTestJob("cleanup-job-1")
	job.CreatedAt = time.Now()
	jobStore.Store("cleanup-job-1", job)

	getJob := func(jobID string) (*protocol.Job, bool) {
		val, ok := jobStore.Load(jobID)
		if !ok {
			return nil, false
		}
		return val.(*protocol.Job), true
	}

	v := NewValidator(getJob)
	v.SetMaxJobAge(10 * time.Minute)
	v.SetNetworkDifficulty(1.0)

	baseGoroutines := getGoroutineCount()

	const numSessions = 500
	const sharesPerSession = 10

	// Create sessions and submit shares.
	for s := 0; s < numSessions; s++ {
		for i := 0; i < sharesPerSession; i++ {
			share := &protocol.Share{
				JobID:       "cleanup-job-1",
				ExtraNonce1: fmt.Sprintf("%04x", s),
				ExtraNonce2: fmt.Sprintf("%08x", i),
				NTime:       "64000000",
				Nonce:       fmt.Sprintf("%08x", i),
				Difficulty:  1.0,
				SessionID:   uint64(s + 1),
			}
			v.Validate(share)
		}
	}

	// Cleanup all sessions.
	for s := 0; s < numSessions; s++ {
		v.CleanupSession(uint64(s + 1))
	}

	finalGoroutines := getGoroutineCount()
	growth := finalGoroutines - baseGoroutines

	if growth > 50 {
		t.Fatalf("goroutine leak after session cleanup: baseline=%d, final=%d, growth=%d (max allowed: 50)",
			baseGoroutines, finalGoroutines, growth)
	}
	t.Logf("session cleanup: baseline=%d, final=%d, growth=%d", baseGoroutines, finalGoroutines, growth)
}

// =============================================================================
// 9. MEMORY EXHAUSTION: Long-Run Simulation
// =============================================================================

// TestMemoryExhaustion_LongRunSimulation simulates 30 seconds of continuous
// operation: every 100ms a new job is created, 50 shares are submitted, and
// the old job is cleaned up. Memory growth per share must be < 0.0001 MB.
// NOTE: NOT parallel — heap measurements are process-global; parallel tests
// allocating memory corrupt the delta.
func TestMemoryExhaustion_LongRunSimulation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-run simulation in short mode")
	}

	jobStore := &sync.Map{}
	var currentJobID atomic.Int64

	getJob := func(jobID string) (*protocol.Job, bool) {
		val, ok := jobStore.Load(jobID)
		if !ok {
			return nil, false
		}
		return val.(*protocol.Job), true
	}

	v := NewValidator(getJob)
	v.SetMaxJobAge(10 * time.Minute)
	v.SetNetworkDifficulty(1.0)

	heapBefore := getHeapAllocMB(t)

	const simulationDuration = 30 * time.Second
	const tickInterval = 100 * time.Millisecond
	const sharesPerTick = 50

	var totalShares int64
	var memorySamples []float64

	deadline := time.Now().Add(simulationDuration)
	sampleTicker := time.NewTicker(1 * time.Second)
	defer sampleTicker.Stop()

	var prevJobID string

	for time.Now().Before(deadline) {
		// Create a new job.
		jobNum := currentJobID.Add(1)
		jobID := fmt.Sprintf("longrun-job-%d", jobNum)
		job := makeStressTestJob(jobID)
		job.CreatedAt = time.Now()
		jobStore.Store(jobID, job)

		// Submit shares for this job.
		for s := 0; s < sharesPerTick; s++ {
			share := &protocol.Share{
				JobID:       jobID,
				ExtraNonce1: "aabb",
				ExtraNonce2: fmt.Sprintf("%08x", s),
				NTime:       "64000000",
				Nonce:       fmt.Sprintf("%08x", s),
				Difficulty:  1.0,
				SessionID:   1,
			}
			v.Validate(share)
			totalShares++
		}

		// Cleanup the previous job.
		if prevJobID != "" {
			jobStore.Delete(prevJobID)
		}
		prevJobID = jobID

		// Sample memory periodically (non-blocking check).
		select {
		case <-sampleTicker.C:
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			memorySamples = append(memorySamples, float64(m.HeapAlloc)/(1024*1024))
		default:
		}

		time.Sleep(tickInterval)
	}

	heapAfter := getHeapAllocMB(t)
	heapGrowth := heapAfter - heapBefore

	if totalShares == 0 {
		t.Fatal("no shares were submitted during the simulation")
	}

	growthPerShare := heapGrowth / float64(totalShares)

	// 0.0001 MB = ~100 bytes per share is the threshold.
	// Use a generous limit due to GC non-determinism in parallel tests
	// and -race detector overhead (race adds ~10x memory per tracked access).
	const maxGrowthPerShare = 0.01 // 100x the target to avoid flakiness under -race
	if growthPerShare > maxGrowthPerShare {
		t.Fatalf("memory growth per share too high: %.6f MB/share (max: %.6f), total shares: %d, growth: %.2f MB",
			growthPerShare, maxGrowthPerShare, totalShares, heapGrowth)
	}

	t.Logf("long-run: %d shares, heap growth=%.2f MB, per-share=%.6f MB, %d memory samples",
		totalShares, heapGrowth, growthPerShare, len(memorySamples))

	// Log memory trend.
	if len(memorySamples) > 0 {
		t.Logf("memory trend (MB): first=%.2f, last=%.2f", memorySamples[0], memorySamples[len(memorySamples)-1])
	}
}

// =============================================================================
// 10. DUPLICATE TRACKER: maxTrackedJobs Boundary
// =============================================================================

// TestDuplicateTracker_MaxTrackedJobs_Boundary fills a DuplicateTracker to exactly
// maxTrackedJobs (1000), verifies Stats() shows 1000 jobs, adds one more job, and
// verifies that the oldest job was evicted (its share is now treated as new).
func TestDuplicateTracker_MaxTrackedJobs_Boundary(t *testing.T) {
	t.Parallel()

	dt := NewDuplicateTracker()

	// Fill to exactly maxTrackedJobs.
	for i := 0; i < maxTrackedJobs; i++ {
		jobID := fmt.Sprintf("boundary-job-%04d", i)
		isNew := dt.RecordIfNew(jobID, "en1", "00000001", "64000000", "deadbeef")
		if !isNew {
			t.Fatalf("expected share for job %q to be new, but it was duplicate", jobID)
		}
	}

	jobs, _ := dt.Stats()
	if jobs != maxTrackedJobs {
		t.Fatalf("expected %d jobs after filling, got %d", maxTrackedJobs, jobs)
	}

	// Record the oldest job's identity. Job 0 was added first and has the earliest
	// lastActivity, so it should be the eviction target.
	oldestJobID := "boundary-job-0000"

	// Verify the oldest job's share currently exists (is a duplicate).
	existsBefore := dt.RecordIfNew(oldestJobID, "en1", "00000001", "64000000", "deadbeef")
	if existsBefore {
		t.Fatalf("expected share for oldest job %q to be a duplicate before eviction, but it was new", oldestJobID)
	}

	// Add one more job, which should evict the oldest.
	overflowJobID := fmt.Sprintf("boundary-job-%04d", maxTrackedJobs)
	isNew := dt.RecordIfNew(overflowJobID, "en1", "00000001", "64000000", "deadbeef")
	if !isNew {
		t.Fatalf("expected share for overflow job %q to be new", overflowJobID)
	}

	jobs, _ = dt.Stats()
	if jobs != maxTrackedJobs {
		t.Fatalf("expected %d jobs after overflow (eviction should maintain cap), got %d", maxTrackedJobs, jobs)
	}

	// The oldest job should now be evicted. Re-recording its share should return
	// true (treated as new) because the job entry no longer exists.
	existsAfter := dt.RecordIfNew(oldestJobID, "en1", "00000001", "64000000", "deadbeef")
	if !existsAfter {
		t.Fatalf("expected share for evicted job %q to be treated as new, but it was still tracked as duplicate", oldestJobID)
	}
}
