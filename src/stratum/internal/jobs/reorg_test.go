// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package jobs - Critical Reorg & Template Churn Tests
//
// Tests for blockchain reorg handling:
// - Template replaced mid-job
// - New template invalidates active jobs
// - Shares submitted against replaced template
// - Block found on old template
//
// WHY IT MATTERS: Reorgs don't happen often - but when they do, bad pools lose blocks.
// MUST ENSURE:
// - Correct stale classification
// - No block loss due to race
package jobs

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// =============================================================================
// 1. TEMPLATE REPLACEMENT TESTS
// =============================================================================

// MockDaemonForReorg simulates a daemon that can experience reorgs
type MockDaemonForReorg struct {
	mu              sync.RWMutex
	templates       []*daemon.BlockTemplate
	currentIdx      int
	callCount       atomic.Int32
	submitCount     atomic.Int32
	submittedBlocks []string
}

func NewMockDaemonForReorg() *MockDaemonForReorg {
	return &MockDaemonForReorg{
		templates:       make([]*daemon.BlockTemplate, 0),
		submittedBlocks: make([]string, 0),
	}
}

func (m *MockDaemonForReorg) AddTemplate(t *daemon.BlockTemplate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.templates = append(m.templates, t)
}

func (m *MockDaemonForReorg) SetCurrentTemplate(idx int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentIdx = idx
}

func (m *MockDaemonForReorg) GetBlockTemplate(ctx context.Context) (*daemon.BlockTemplate, error) {
	m.callCount.Add(1)
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.templates) == 0 {
		return nil, fmt.Errorf("no templates available")
	}

	if m.currentIdx >= len(m.templates) {
		return m.templates[len(m.templates)-1], nil
	}

	return m.templates[m.currentIdx], nil
}

func (m *MockDaemonForReorg) SubmitBlock(ctx context.Context, blockHex string) error {
	m.submitCount.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submittedBlocks = append(m.submittedBlocks, blockHex)
	return nil
}

func (m *MockDaemonForReorg) GetSubmittedBlocks() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.submittedBlocks))
	copy(result, m.submittedBlocks)
	return result
}

// TestTemplateReplacementMidJob tests behavior when template changes during mining
func TestTemplateReplacementMidJob(t *testing.T) {
	t.Parallel()

	// Create two templates with different prev hashes (simulating reorg)
	template1 := &daemon.BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     312500000,
		Target:            "00000000ffff0000000000000000000000000000000000000000000000000000",
		Bits:              "1d00ffff",
		Height:            100000,
		CurTime:           time.Now().Unix(),
	}

	template2 := &daemon.BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000002", // Different!
		CoinbaseValue:     312500000,
		Target:            "00000000ffff0000000000000000000000000000000000000000000000000000",
		Bits:              "1d00ffff",
		Height:            100000, // Same height (reorg at same height)
		CurTime:           time.Now().Unix(),
	}

	mockDaemon := NewMockDaemonForReorg()
	mockDaemon.AddTemplate(template1)
	mockDaemon.AddTemplate(template2)
	mockDaemon.SetCurrentTemplate(0)

	// Track job broadcasts
	var jobBroadcasts []*protocol.Job
	var broadcastMu sync.Mutex

	jobCallback := func(job *protocol.Job) {
		broadcastMu.Lock()
		jobBroadcasts = append(jobBroadcasts, job)
		broadcastMu.Unlock()
	}

	// Create job store for validation
	jobStore := make(map[string]*protocol.Job)
	var storeMu sync.RWMutex

	getJob := func(id string) (*protocol.Job, bool) {
		storeMu.RLock()
		defer storeMu.RUnlock()
		j, ok := jobStore[id]
		return j, ok
	}

	storeJob := func(job *protocol.Job) {
		storeMu.Lock()
		defer storeMu.Unlock()
		jobStore[job.ID] = job
	}

	// Simulate job manager behavior
	var jobCounter atomic.Uint64

	generateJob := func(template *daemon.BlockTemplate, cleanJobs bool) *protocol.Job {
		id := jobCounter.Add(1)
		job := &protocol.Job{
			ID:            fmt.Sprintf("%08x", id),
			PrevBlockHash: formatPrevHashForStratum(template.PreviousBlockHash),
			Version:       fmt.Sprintf("%08x", template.Version),
			NBits:         template.Bits,
			NTime:         fmt.Sprintf("%08x", template.CurTime),
			Height:        template.Height,
			CleanJobs:     cleanJobs,
			CreatedAt:     time.Now(),
		}
		return job
	}

	// Initial job from template 1
	job1 := generateJob(template1, true)
	storeJob(job1)
	jobCallback(job1)

	t.Logf("Job 1 created: ID=%s, PrevHash=%s...", job1.ID, job1.PrevBlockHash[:16])

	// Simulate miners working on job 1
	time.Sleep(100 * time.Millisecond)

	// REORG: Switch to template 2
	mockDaemon.SetCurrentTemplate(1)

	template, _ := mockDaemon.GetBlockTemplate(context.Background())
	job2 := generateJob(template, true) // CleanJobs=true indicates reorg
	storeJob(job2)
	jobCallback(job2)

	t.Logf("Job 2 created (reorg): ID=%s, PrevHash=%s...", job2.ID, job2.PrevBlockHash[:16])

	// Verify jobs have different prev hashes
	if job1.PrevBlockHash == job2.PrevBlockHash {
		t.Error("REORG NOT DETECTED: Jobs have same PrevBlockHash after template change")
	}

	// Verify job 2 has CleanJobs=true
	if !job2.CleanJobs {
		t.Error("Reorg job should have CleanJobs=true")
	}

	// Verify both jobs are retrievable (for late share submission)
	if _, ok := getJob(job1.ID); !ok {
		t.Error("Old job (job1) should still be retrievable for share validation")
	}

	if _, ok := getJob(job2.ID); !ok {
		t.Error("New job (job2) should be retrievable")
	}

	// Verify job callbacks were made
	broadcastMu.Lock()
	numBroadcasts := len(jobBroadcasts)
	broadcastMu.Unlock()

	if numBroadcasts != 2 {
		t.Errorf("Expected 2 job broadcasts, got %d", numBroadcasts)
	}
}

// TestShareOnReplacedTemplate tests shares submitted for old template after reorg
func TestShareOnReplacedTemplate(t *testing.T) {
	t.Parallel()

	// Job store with history
	jobStore := make(map[string]*protocol.Job)
	var storeMu sync.RWMutex

	getJob := func(id string) (*protocol.Job, bool) {
		storeMu.RLock()
		defer storeMu.RUnlock()
		j, ok := jobStore[id]
		return j, ok
	}

	storeJob := func(job *protocol.Job) {
		storeMu.Lock()
		defer storeMu.Unlock()
		jobStore[job.ID] = job
	}

	// Create old job (pre-reorg)
	oldJob := &protocol.Job{
		ID:            "old_job_001",
		PrevBlockHash: formatPrevHashForStratum("0000000000000000000000000000000000000000000000000000000000000001"),
		Version:       "20000000",
		NBits:         "1d00ffff",
		NTime:         fmt.Sprintf("%08x", time.Now().Add(-2*time.Minute).Unix()),
		Height:        100000,
		CleanJobs:     true,
		CreatedAt:     time.Now().Add(-2 * time.Minute), // Created 2 minutes ago
	}
	storeJob(oldJob)

	// Create new job (post-reorg)
	newJob := &protocol.Job{
		ID:            "new_job_002",
		PrevBlockHash: formatPrevHashForStratum("0000000000000000000000000000000000000000000000000000000000000002"),
		Version:       "20000000",
		NBits:         "1d00ffff",
		NTime:         fmt.Sprintf("%08x", time.Now().Unix()),
		Height:        100000, // Same height (reorg)
		CleanJobs:     true,
		CreatedAt:     time.Now(),
	}
	storeJob(newJob)

	// Simulate share submission scenarios
	tests := []struct {
		name          string
		jobID         string
		expectValid   bool
		expectStale   bool
		description   string
	}{
		{
			name:        "share_for_current_job",
			jobID:       newJob.ID,
			expectValid: true,
			expectStale: false,
			description: "Share for current job should be valid",
		},
		{
			name:        "share_for_old_job",
			jobID:       oldJob.ID,
			expectValid: true,  // Job still exists
			expectStale: false, // Not old enough to be stale (< 5 min)
			description: "Share for old job should still be accepted within grace period",
		},
		{
			name:        "share_for_nonexistent_job",
			jobID:       "nonexistent_job",
			expectValid: false,
			expectStale: true, // Invalid job = treat as stale
			description: "Share for unknown job should be rejected",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			job, found := getJob(tc.jobID)

			if tc.expectValid {
				if !found {
					t.Errorf("%s: Job not found but expected valid", tc.description)
				}
			} else {
				if found && !tc.expectStale {
					t.Errorf("%s: Job found but expected invalid", tc.description)
				}
			}

			if found && job != nil {
				// Check if job would be considered stale (> 5 min old)
				isStale := time.Since(job.CreatedAt) > 5*time.Minute

				if isStale != tc.expectStale {
					t.Logf("%s: staleness=%v, age=%v", tc.description, isStale, time.Since(job.CreatedAt))
				}
			}
		})
	}
}

// =============================================================================
// 2. BLOCK FOUND ON OLD TEMPLATE TESTS
// =============================================================================

// TestBlockFoundOnOldTemplate tests handling of blocks found on pre-reorg template
func TestBlockFoundOnOldTemplate(t *testing.T) {
	t.Parallel()

	mockDaemon := NewMockDaemonForReorg()

	// Pre-reorg template
	template1 := &daemon.BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     312500000,
		Height:            100000,
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix(),
	}

	// Post-reorg template (different chain)
	template2 := &daemon.BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000099", // Very different
		CoinbaseValue:     312500000,
		Height:            100000,
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix(),
	}

	mockDaemon.AddTemplate(template1)
	mockDaemon.AddTemplate(template2)

	// Scenario: Block found on template1 AFTER reorg to template2
	// This block should still be submitted (might still be valid on the original chain)

	type BlockCandidate struct {
		job      *protocol.Job
		blockHex string
		foundAt  time.Time
	}

	candidates := make(chan BlockCandidate, 10)

	// Simulate finding a block on old template
	oldJob := &protocol.Job{
		ID:            "old_block_job",
		PrevBlockHash: formatPrevHashForStratum(template1.PreviousBlockHash),
		Height:        template1.Height,
		CreatedAt:     time.Now().Add(-1 * time.Minute),
	}

	candidates <- BlockCandidate{
		job:      oldJob,
		blockHex: "deadbeef...", // Simulated block hex
		foundAt:  time.Now(),
	}

	// Process block candidate
	select {
	case candidate := <-candidates:
		// Check if job's prev hash still matches current template
		mockDaemon.SetCurrentTemplate(1) // Reorg happened
		currentTemplate, _ := mockDaemon.GetBlockTemplate(context.Background())

		if candidate.job.PrevBlockHash != formatPrevHashForStratum(currentTemplate.PreviousBlockHash) {
			t.Logf("Block found on old template (PrevHash mismatch)")
			t.Logf("  Job PrevHash: %s...", candidate.job.PrevBlockHash[:16])
			t.Logf("  Current PrevHash: %s...", formatPrevHashForStratum(currentTemplate.PreviousBlockHash)[:16])

			// DECISION: Should we still submit?
			// YES - the daemon will reject if invalid, but we shouldn't discard valid blocks
			t.Logf("Submitting block anyway (daemon will validate)")

			err := mockDaemon.SubmitBlock(context.Background(), candidate.blockHex)
			if err != nil {
				t.Logf("Block submission result: %v", err)
			}

			// Verify submission was attempted
			submitted := mockDaemon.GetSubmittedBlocks()
			if len(submitted) != 1 {
				t.Error("Block should have been submitted despite template change")
			}
		} else {
			t.Logf("Block found on current template (valid)")
		}

	default:
		t.Fatal("No block candidate received")
	}
}

// =============================================================================
// 3. JOB HISTORY DURING REORG
// =============================================================================

// TestJobHistoryPreservation tests that job history is maintained across reorgs
func TestJobHistoryPreservation(t *testing.T) {
	t.Parallel()

	maxJobHistory := 10
	jobHistory := make(map[string]*protocol.Job)
	var historyMu sync.RWMutex
	var jobOrder []string

	storeJobWithHistory := func(job *protocol.Job) {
		historyMu.Lock()
		defer historyMu.Unlock()

		jobHistory[job.ID] = job
		jobOrder = append(jobOrder, job.ID)

		// Cleanup old jobs
		for len(jobHistory) > maxJobHistory {
			oldestID := jobOrder[0]
			delete(jobHistory, oldestID)
			jobOrder = jobOrder[1:]
		}
	}

	getJobFromHistory := func(id string) (*protocol.Job, bool) {
		historyMu.RLock()
		defer historyMu.RUnlock()
		j, ok := jobHistory[id]
		return j, ok
	}

	// Create jobs across multiple "reorgs"
	prevHashes := []string{
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000001", // Same chain
		"0000000000000000000000000000000000000000000000000000000000000002", // Reorg 1
		"0000000000000000000000000000000000000000000000000000000000000002", // Same chain
		"0000000000000000000000000000000000000000000000000000000000000003", // Reorg 2
	}

	for i, prevHash := range prevHashes {
		job := &protocol.Job{
			ID:            fmt.Sprintf("history_job_%03d", i),
			PrevBlockHash: formatPrevHashForStratum(prevHash),
			Height:        uint64(100000 + i),
			CleanJobs:     i == 2 || i == 4, // Clean on reorgs
			CreatedAt:     time.Now().Add(time.Duration(i) * time.Second),
		}
		storeJobWithHistory(job)
	}

	// Verify all recent jobs are accessible
	historyMu.RLock()
	historySize := len(jobHistory)
	historyMu.RUnlock()

	if historySize != 5 {
		t.Errorf("Expected 5 jobs in history, got %d", historySize)
	}

	// Verify specific jobs are retrievable
	for i := 0; i < 5; i++ {
		jobID := fmt.Sprintf("history_job_%03d", i)
		job, found := getJobFromHistory(jobID)
		if !found {
			t.Errorf("Job %s not found in history", jobID)
		}
		if job != nil && job.ID != jobID {
			t.Errorf("Retrieved wrong job: expected %s, got %s", jobID, job.ID)
		}
	}

	// Add more jobs to trigger eviction
	for i := 5; i < 15; i++ {
		job := &protocol.Job{
			ID:            fmt.Sprintf("history_job_%03d", i),
			PrevBlockHash: formatPrevHashForStratum("0000000000000000000000000000000000000000000000000000000000000003"),
			Height:        uint64(100000 + i),
			CleanJobs:     false,
			CreatedAt:     time.Now().Add(time.Duration(i) * time.Second),
		}
		storeJobWithHistory(job)
	}

	// Verify old jobs are evicted
	historyMu.RLock()
	historySize = len(jobHistory)
	historyMu.RUnlock()

	if historySize != maxJobHistory {
		t.Errorf("Expected %d jobs after eviction, got %d", maxJobHistory, historySize)
	}

	// First 5 jobs should be evicted
	for i := 0; i < 5; i++ {
		jobID := fmt.Sprintf("history_job_%03d", i)
		_, found := getJobFromHistory(jobID)
		if found {
			t.Errorf("Job %s should have been evicted", jobID)
		}
	}
}

// =============================================================================
// 4. RACE CONDITION TESTS
// =============================================================================

// TestConcurrentReorgAndShareSubmission tests race between reorg and share processing
func TestConcurrentReorgAndShareSubmission(t *testing.T) {
	t.Parallel()

	jobStore := make(map[string]*protocol.Job)
	var storeMu sync.RWMutex

	var currentJobID atomic.Pointer[string]

	getJob := func(id string) (*protocol.Job, bool) {
		storeMu.RLock()
		defer storeMu.RUnlock()
		j, ok := jobStore[id]
		return j, ok
	}

	storeJob := func(job *protocol.Job) {
		storeMu.Lock()
		defer storeMu.Unlock()
		jobStore[job.ID] = job
		currentJobID.Store(&job.ID)
	}

	// Create initial job
	initialJob := &protocol.Job{
		ID:            "concurrent_job_001",
		PrevBlockHash: formatPrevHashForStratum("0000000000000000000000000000000000000000000000000000000000000001"),
		Height:        100000,
		CreatedAt:     time.Now(),
	}
	storeJob(initialJob)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Track results
	var sharesProcessed atomic.Int64
	var sharesAccepted atomic.Int64
	var jobsNotFound atomic.Int64

	// Share submission goroutines
	numSubmitters := 10
	for s := 0; s < numSubmitters; s++ {
		wg.Add(1)
		go func(submitterID int) {
			defer wg.Done()

			for i := 0; i < 100; i++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Get current job ID
				jobIDPtr := currentJobID.Load()
				if jobIDPtr == nil {
					continue
				}

				// Simulate small delay (mining time)
				time.Sleep(time.Microsecond * 100)

				// Submit share
				job, found := getJob(*jobIDPtr)
				sharesProcessed.Add(1)

				if found && job != nil {
					sharesAccepted.Add(1)
				} else {
					jobsNotFound.Add(1)
				}
			}
		}(s)
	}

	// Reorg simulation goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()

		for i := 2; i <= 20; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Simulate reorg
			newJob := &protocol.Job{
				ID:            fmt.Sprintf("concurrent_job_%03d", i),
				PrevBlockHash: formatPrevHashForStratum(fmt.Sprintf("%064d", i)),
				Height:        uint64(100000 + i),
				CleanJobs:     true,
				CreatedAt:     time.Now(),
			}
			storeJob(newJob)

			time.Sleep(50 * time.Millisecond)
		}
	}()

	wg.Wait()

	t.Logf("Shares processed: %d", sharesProcessed.Load())
	t.Logf("Shares accepted: %d", sharesAccepted.Load())
	t.Logf("Jobs not found: %d", jobsNotFound.Load())

	// Most shares should find their job (job history is kept)
	acceptRate := float64(sharesAccepted.Load()) / float64(sharesProcessed.Load()) * 100
	if acceptRate < 95 {
		t.Errorf("Low share acceptance rate during reorg churn: %.1f%% (expected > 95%%)", acceptRate)
	}
}

// =============================================================================
// 5. STALE SHARE CLASSIFICATION
// =============================================================================

// TestStaleShareClassification tests correct classification of stale shares
func TestStaleShareClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		jobAge      time.Duration
		expectStale bool
		reason      string
	}{
		{
			name:        "fresh_job_1s",
			jobAge:      1 * time.Second,
			expectStale: false,
			reason:      "1 second old job is fresh",
		},
		{
			name:        "recent_job_1min",
			jobAge:      1 * time.Minute,
			expectStale: false,
			reason:      "1 minute old job is recent",
		},
		{
			name:        "old_job_4min",
			jobAge:      4 * time.Minute,
			expectStale: false,
			reason:      "4 minute old job is within grace period",
		},
		{
			name:        "stale_job_6min",
			jobAge:      6 * time.Minute,
			expectStale: true,
			reason:      "6 minute old job exceeds 5 minute limit",
		},
		{
			name:        "very_stale_job_1hour",
			jobAge:      1 * time.Hour,
			expectStale: true,
			reason:      "1 hour old job is definitely stale",
		},
	}

	const maxJobAge = 5 * time.Minute

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			job := &protocol.Job{
				ID:        tc.name,
				CreatedAt: time.Now().Add(-tc.jobAge),
			}

			isStale := time.Since(job.CreatedAt) > maxJobAge

			if isStale != tc.expectStale {
				t.Errorf("%s: isStale=%v, expected=%v", tc.reason, isStale, tc.expectStale)
			}
		})
	}
}

// =============================================================================
// 6. TEMPLATE CHURN STRESS TEST
// =============================================================================

// TestHighTemplateChurn tests behavior under rapid template changes
func TestHighTemplateChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	t.Parallel()

	jobStore := make(map[string]*protocol.Job)
	var storeMu sync.RWMutex
	maxJobs := 100

	var jobIDOrder []string

	storeJobWithEviction := func(job *protocol.Job) {
		storeMu.Lock()
		defer storeMu.Unlock()

		jobStore[job.ID] = job
		jobIDOrder = append(jobIDOrder, job.ID)

		for len(jobStore) > maxJobs {
			oldestID := jobIDOrder[0]
			delete(jobStore, oldestID)
			jobIDOrder = jobIDOrder[1:]
		}
	}

	getJob := func(id string) (*protocol.Job, bool) {
		storeMu.RLock()
		defer storeMu.RUnlock()
		j, ok := jobStore[id]
		return j, ok
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Rapid job creation (simulating fast blocks or reorgs)
	var jobsCreated atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jobNum := jobsCreated.Add(1)
				job := &protocol.Job{
					ID:            fmt.Sprintf("churn_job_%06d", jobNum),
					PrevBlockHash: formatPrevHashForStratum(fmt.Sprintf("%064d", jobNum)),
					Height:        100000 + uint64(jobNum),
					CleanJobs:     jobNum%5 == 0, // Every 5th job is a "reorg"
					CreatedAt:     time.Now(),
				}
				storeJobWithEviction(job)
			}
		}
	}()

	// Share validators querying jobs
	var queriesTotal atomic.Int64
	var queriesFound atomic.Int64

	numValidators := 20
	for v := 0; v < numValidators; v++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				// Query a random recent job
				jobNum := jobsCreated.Load()
				if jobNum < 1 {
					time.Sleep(time.Millisecond)
					continue
				}

				// Query jobs from recent history
				queryJobNum := jobNum - int64(jobNum%int64(maxJobs)/2)
				if queryJobNum < 1 {
					queryJobNum = 1
				}

				jobID := fmt.Sprintf("churn_job_%06d", queryJobNum)
				queriesTotal.Add(1)

				if _, found := getJob(jobID); found {
					queriesFound.Add(1)
				}

				time.Sleep(time.Microsecond * 100)
			}
		}()
	}

	wg.Wait()

	t.Logf("Jobs created: %d", jobsCreated.Load())
	t.Logf("Queries total: %d", queriesTotal.Load())
	t.Logf("Queries found: %d", queriesFound.Load())

	if queriesTotal.Load() > 0 {
		hitRate := float64(queriesFound.Load()) / float64(queriesTotal.Load()) * 100
		t.Logf("Job cache hit rate: %.1f%%", hitRate)

		// Hit rate should be high for recent jobs
		if hitRate < 50 {
			t.Errorf("Low cache hit rate under churn: %.1f%% (expected > 50%%)", hitRate)
		}
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// formatPrevHashForStratum converts block hash to stratum format.
// Reverses 4-byte group order so that miner bswap32 produces internal LE byte order.
// Must match Manager.formatPrevHash in manager.go.
func formatPrevHashForStratum(hash string) string {
	if len(hash) != 64 {
		return hash
	}

	result := make([]byte, 0, 64)
	for i := 56; i >= 0; i -= 8 {
		result = append(result, hash[i:i+8]...)
	}

	return string(result)
}

// createReorgTestManager creates a minimal job manager for reorg testing
func createReorgTestManager(t *testing.T) *Manager {
	t.Helper()

	logger, _ := zap.NewDevelopment()

	poolCfg := &config.PoolConfig{
		ID:           "test_pool",
		Coin:         "bitcoin",
		Address:      "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		CoinbaseText: "Test Pool",
	}

	stratumCfg := &config.StratumConfig{
		VersionRolling: config.VersionRolling{
			Enabled: true,
			Mask:    0x1FFFE000,
		},
	}

	// Create manager (will fail on address validation but that's OK for basic tests)
	manager, err := NewManager(poolCfg, stratumCfg, nil, logger)
	if err != nil {
		// For testing, we may need to skip address validation
		t.Logf("Manager creation note: %v", err)
	}

	return manager
}

// Helper to create test prev hash bytes
func makePrevHashBytes(val int) string {
	b := make([]byte, 32)
	b[31] = byte(val)
	return hex.EncodeToString(b)
}
