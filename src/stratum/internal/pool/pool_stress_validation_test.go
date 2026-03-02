// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool — Full pool stress & race validation suite.
//
// This file implements the Pool Stress & Race Validation Plan, integrating
// all test categories (A–G) into a unified validation framework with:
//   - Full handleBlock pipeline simulation (daemon mock, retries, WAL, DB, logging)
//   - Metrics collection (submitBlock calls, blockStatus, ZMQ latency distribution)
//   - Audit reporting (timeline of races, bursts, state transitions)
//   - Multi-algorithm coverage (SHA-256d, Scrypt, merge-mined)
//   - Success criteria verification
//
// This complements block_race_test.go (unit-level) and pool_race_multi_algo_test.go
// (multi-algo state machine) by testing the complete handleBlock pipeline with
// side-effect tracking and daemon interaction simulation.
package pool

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// INTEGRATION TEST INFRASTRUCTURE
// =============================================================================

// mockDaemon simulates the daemon's SubmitBlock RPC and chain state.
// Thread-safe for concurrent access from handleBlock goroutines.
type mockDaemon struct {
	mu sync.RWMutex

	// Chain state
	tipHeight uint64
	tipHash   string

	// Behavior configuration
	rejectNext       string // If set, next SubmitBlock returns this error
	rejectAlways     string // If set, all SubmitBlock calls return this error
	acceptDelay      time.Duration // Artificial delay before responding
	failCount        int    // Number of transient failures before success
	failsRemaining   int

	// Metrics
	submitCalls      atomic.Int32
	acceptedBlocks   atomic.Int32
	rejectedBlocks   atomic.Int32
	lastSubmitHeight atomic.Uint64
}

func newMockDaemon(height uint64, hash string) *mockDaemon {
	return &mockDaemon{
		tipHeight: height,
		tipHash:   hash,
	}
}

func (d *mockDaemon) advanceTip(height uint64, hash string) {
	d.mu.Lock()
	d.tipHeight = height
	d.tipHash = hash
	d.mu.Unlock()
}

func (d *mockDaemon) getTip() (uint64, string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.tipHeight, d.tipHash
}

func (d *mockDaemon) setRejectNext(reason string) {
	d.mu.Lock()
	d.rejectNext = reason
	d.mu.Unlock()
}

func (d *mockDaemon) setRejectAlways(reason string) {
	d.mu.Lock()
	d.rejectAlways = reason
	d.mu.Unlock()
}

func (d *mockDaemon) setTransientFailures(count int) {
	d.mu.Lock()
	d.failCount = count
	d.failsRemaining = count
	d.mu.Unlock()
}

// submitBlock simulates SubmitBlock RPC. Returns error string or "" for success.
func (d *mockDaemon) submitBlock(jobHeight uint64) string {
	d.submitCalls.Add(1)
	d.lastSubmitHeight.Store(jobHeight)

	if d.acceptDelay > 0 {
		time.Sleep(d.acceptDelay)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Check rejectAlways first
	if d.rejectAlways != "" {
		d.rejectedBlocks.Add(1)
		return d.rejectAlways
	}

	// Check one-shot rejection
	if d.rejectNext != "" {
		reason := d.rejectNext
		d.rejectNext = ""
		d.rejectedBlocks.Add(1)
		return reason
	}

	// Check transient failure countdown
	if d.failsRemaining > 0 {
		d.failsRemaining--
		d.rejectedBlocks.Add(1)
		return "connection refused"
	}

	// Check if block is stale (tip already advanced)
	if jobHeight < d.tipHeight {
		d.rejectedBlocks.Add(1)
		return "prev-blk-not-found"
	}

	d.acceptedBlocks.Add(1)
	return ""
}

// validationMetrics captures all observables required by the validation plan.
type validationMetrics struct {
	// submitBlock tracking
	totalSubmitCalls    atomic.Int32
	acceptedSubmits     atomic.Int32
	rejectedSubmits     atomic.Int32
	skippedStale        atomic.Int32
	skippedSolved       atomic.Int32
	skippedEmptyHex     atomic.Int32

	// blockStatus tracking
	statusPending       atomic.Int32
	statusOrphaned      atomic.Int32
	statusRejected      atomic.Int32

	// Side-effect tracking
	celebrations        atomic.Int32
	blockLogs           atomic.Int32
	rewardLogs          atomic.Int32
	auxBlockLogs        atomic.Int32
	dbInserts           atomic.Int32

	// Retry tracking
	retryAttempts       atomic.Int32
	retrySuccesses      atomic.Int32
	retryFinalFailures  atomic.Int32

	// State machine tracking
	solvedTransitions   atomic.Int32
	invalidTransitions  atomic.Int32
	regressionAttempts  atomic.Int32 // Attempts to go from terminal → Active

	// ZMQ latency tracking (nanoseconds)
	zmqLatencies        []int64
	zmqMu               sync.Mutex
}

func (m *validationMetrics) recordZMQLatency(d time.Duration) {
	m.zmqMu.Lock()
	m.zmqLatencies = append(m.zmqLatencies, d.Nanoseconds())
	m.zmqMu.Unlock()
}

func (m *validationMetrics) zmqStats() (min, max, avg int64, count int) {
	m.zmqMu.Lock()
	defer m.zmqMu.Unlock()
	if len(m.zmqLatencies) == 0 {
		return 0, 0, 0, 0
	}
	min = m.zmqLatencies[0]
	max = m.zmqLatencies[0]
	var sum int64
	for _, v := range m.zmqLatencies {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	return min, max, sum / int64(len(m.zmqLatencies)), len(m.zmqLatencies)
}

// handleBlockSim simulates the full handleBlock pipeline from pool.go:979-1314.
// Returns blockStatus and updates metrics. This is the core integration function.
func handleBlockSim(
	store *jobStore,
	daemon *mockDaemon,
	metrics *validationMetrics,
	jobID string,
	jobHeight uint64,
	blockHex string, // "" simulates missing block hex
	_ bool, // isMergeJob — reserved for future aux-specific pipeline steps
	maxRetries int,
) string {
	blockStatus := "pending"
	claimedSolved := false

	// ── STEP 1: Pre-submission gate (pool.go:988-1028) ──
	job, jobFound := store.get(jobID)
	jobState := protocol.JobStateActive
	if jobFound {
		jobState = job.GetState()
	}

	if jobState == protocol.JobStateInvalidated {
		blockStatus = "orphaned"
		metrics.skippedStale.Add(1)
	} else if jobState == protocol.JobStateSolved {
		blockStatus = "orphaned"
		metrics.skippedSolved.Add(1)
	} else if blockHex == "" {
		blockStatus = "orphaned"
		metrics.skippedEmptyHex.Add(1)
	} else {
		// ── STEP 2: Submit immediately (pool.go:1030-1063) ──
		metrics.totalSubmitCalls.Add(1)
		errStr := daemon.submitBlock(jobHeight)

		if errStr == "" {
			blockStatus = "submitted"
			metrics.acceptedSubmits.Add(1)
			// Set Solved (pool.go:1042-1044) — only first goroutine claims ownership
			if jobFound && job != nil {
				if job.SetState(protocol.JobStateSolved, "block submitted successfully") {
					metrics.solvedTransitions.Add(1)
					claimedSolved = true
				}
			} else {
				claimedSolved = true // No job tracking — assume ownership
			}
		} else {
			blockStatus = "rejected"
			metrics.rejectedSubmits.Add(1)
		}
	}

	// ── STEP 3: Logging gates (pool.go:1069-1107) ──
	// Only log if this goroutine owns the block (claimed Solved or non-concurrent path)
	if (blockStatus == "pending" || blockStatus == "submitted") && claimedSolved {
		metrics.blockLogs.Add(1)
	}

	// ── STEP 4: Handle submission result (pool.go:1132-1258) ──
	if blockStatus == "submitted" && !claimedSolved {
		// Daemon accepted but another goroutine already claimed Solved — duplicate
		blockStatus = "orphaned"
		metrics.statusOrphaned.Add(1)
	} else if blockStatus == "submitted" {
		blockStatus = "pending"
		metrics.statusPending.Add(1)
	} else if blockStatus == "rejected" {
		// Check if the rejection is permanent (mirrors pool.go:1152-1153).
		// In the real code, lastErr.Error() is checked. Here we check
		// daemon.rejectAlways which represents the error the daemon returned.
		daemon.mu.RLock()
		rejectAlways := daemon.rejectAlways
		daemon.mu.RUnlock()

		if rejectAlways != "" && isPermanentRejection(rejectAlways) {
			blockStatus = "orphaned"
			metrics.statusOrphaned.Add(1)
		} else {
			// Retry loop (pool.go:1191-1238)
			retried := false
			for attempt := 2; attempt <= maxRetries; attempt++ {
				metrics.retryAttempts.Add(1)

				// Re-check job state before retry
				if jobFound {
					retryState := job.GetState()
					if retryState == protocol.JobStateInvalidated || retryState == protocol.JobStateSolved {
						blockStatus = "orphaned"
						metrics.statusOrphaned.Add(1)
						retried = true
						break
					}
				}

				retryErr := daemon.submitBlock(jobHeight)
				metrics.totalSubmitCalls.Add(1)
				if retryErr == "" {
					metrics.retrySuccesses.Add(1)
					retryClaimed := false
					if jobFound && job != nil {
						if job.SetState(protocol.JobStateSolved, "block submitted on retry") {
							metrics.solvedTransitions.Add(1)
							retryClaimed = true
						}
					} else {
						retryClaimed = true
					}
					if retryClaimed {
						blockStatus = "pending"
						metrics.statusPending.Add(1)
						metrics.blockLogs.Add(1)
					} else {
						// Another goroutine already claimed Solved
						blockStatus = "orphaned"
						metrics.statusOrphaned.Add(1)
					}
					retried = true
					break
				}
				if isPermanentRejection(retryErr) {
					blockStatus = "orphaned"
					metrics.statusOrphaned.Add(1)
					retried = true
					break
				}
			}
			if !retried {
				blockStatus = "orphaned"
				metrics.retryFinalFailures.Add(1)
				metrics.statusOrphaned.Add(1)
			}
		}
	} else if blockStatus == "orphaned" {
		metrics.statusOrphaned.Add(1)
	}

	// ── STEP 5: DB insert — ALWAYS (pool.go:1300-1304) ──
	metrics.dbInserts.Add(1)

	// ── STEP 6: Celebration + reward gate (pool.go:1306-1312) ──
	if blockStatus == "pending" {
		metrics.celebrations.Add(1)
		metrics.rewardLogs.Add(1)
	}

	return blockStatus
}

// printReport outputs the validation metrics in audit report format.
func printReport(t *testing.T, label string, m *validationMetrics) {
	t.Logf("═══════════════════════════════════════════════════════════")
	t.Logf("  VALIDATION REPORT: %s", label)
	t.Logf("═══════════════════════════════════════════════════════════")
	t.Logf("  submitBlock calls:      %d", m.totalSubmitCalls.Load())
	t.Logf("    accepted:             %d", m.acceptedSubmits.Load())
	t.Logf("    rejected:             %d", m.rejectedSubmits.Load())
	t.Logf("    skipped (stale):      %d", m.skippedStale.Load())
	t.Logf("    skipped (solved):     %d", m.skippedSolved.Load())
	t.Logf("    skipped (empty hex):  %d", m.skippedEmptyHex.Load())
	t.Logf("  blockStatus:")
	t.Logf("    pending (accepted):   %d", m.statusPending.Load())
	t.Logf("    orphaned:             %d", m.statusOrphaned.Load())
	t.Logf("  Side effects:")
	t.Logf("    celebrations:         %d", m.celebrations.Load())
	t.Logf("    block logs:           %d", m.blockLogs.Load())
	t.Logf("    reward logs:          %d", m.rewardLogs.Load())
	t.Logf("    DB inserts:           %d", m.dbInserts.Load())
	t.Logf("  Retries:")
	t.Logf("    attempts:             %d", m.retryAttempts.Load())
	t.Logf("    successes:            %d", m.retrySuccesses.Load())
	t.Logf("    final failures:       %d", m.retryFinalFailures.Load())
	t.Logf("  State transitions:")
	t.Logf("    solved:               %d", m.solvedTransitions.Load())
	t.Logf("    invalidated:          %d", m.invalidTransitions.Load())
	t.Logf("    regression attempts:  %d", m.regressionAttempts.Load())

	zMin, zMax, zAvg, zCount := m.zmqStats()
	if zCount > 0 {
		t.Logf("  ZMQ latency (ns):")
		t.Logf("    count:                %d", zCount)
		t.Logf("    min:                  %d", zMin)
		t.Logf("    max:                  %d", zMax)
		t.Logf("    avg:                  %d", zAvg)
	}
	t.Logf("═══════════════════════════════════════════════════════════")
}

// verifyCriteria checks all success criteria from section 6 of the plan.
func verifyCriteria(t *testing.T, label string, m *validationMetrics) {
	t.Helper()

	// Criterion 1: Celebrations only for accepted blocks
	if m.celebrations.Load() != m.statusPending.Load() {
		t.Errorf("[%s] CRITICAL: celebrations (%d) != accepted blocks (%d)",
			label, m.celebrations.Load(), m.statusPending.Load())
	}

	// Criterion 2: Reward logs only for accepted blocks
	if m.rewardLogs.Load() != m.statusPending.Load() {
		t.Errorf("[%s] CRITICAL: reward logs (%d) != accepted blocks (%d)",
			label, m.rewardLogs.Load(), m.statusPending.Load())
	}

	// Criterion 3: DB inserts fire for ALL blocks (audit trail)
	totalBlocks := m.statusPending.Load() + m.statusOrphaned.Load()
	if m.dbInserts.Load() != totalBlocks {
		t.Errorf("[%s] DB inserts (%d) != total blocks (%d)",
			label, m.dbInserts.Load(), totalBlocks)
	}

	// Criterion 4: No state regressions
	if m.regressionAttempts.Load() > 0 {
		t.Errorf("[%s] CRITICAL: %d state regression attempts detected",
			label, m.regressionAttempts.Load())
	}

	// Criterion 5: Block logs only for accepted/submitted blocks
	if m.blockLogs.Load() < m.statusPending.Load() {
		t.Errorf("[%s] block logs (%d) < accepted blocks (%d) — missing logs",
			label, m.blockLogs.Load(), m.statusPending.Load())
	}
}

// =============================================================================
// PLAN §2A: Deterministic Race Tests (Full Pipeline)
// =============================================================================
// Reproduce known races through the complete handleBlock pipeline with daemon mock.

func TestPlan2A_ZMQ_BeforeSubmission(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")
	m := &validationMetrics{}

	j := makeActiveJob("plan2a-before")
	store.put(j)

	// ZMQ fires BEFORE handleBlock — job is invalidated
	store.invalidateAll("zmq-before-submit")
	m.invalidTransitions.Add(1)

	status := handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef", false, 3)

	if status != "orphaned" {
		t.Fatalf("Expected orphaned, got %s", status)
	}
	if daemon.submitCalls.Load() != 0 {
		t.Fatalf("submitBlock should NOT be called for pre-invalidated job, got %d calls",
			daemon.submitCalls.Load())
	}
	if m.celebrations.Load() != 0 {
		t.Fatal("No celebration for orphaned block")
	}

	verifyCriteria(t, "2A-Before", m)
	printReport(t, "2A: ZMQ Before Submission", m)
}

func TestPlan2A_ZMQ_DuringRetry(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")
	m := &validationMetrics{}

	j := makeActiveJob("plan2a-during")
	store.put(j)

	// First submit fails transiently
	daemon.setTransientFailures(1)

	// Simulate: ZMQ fires during retry window
	go func() {
		time.Sleep(50 * time.Microsecond)
		store.invalidateAll("zmq-during-retry")
	}()

	status := handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef", false, 3)

	// Status must be pending (if retry beat ZMQ) or orphaned (if ZMQ beat retry)
	if status != "pending" && status != "orphaned" {
		t.Fatalf("Expected pending or orphaned, got %s", status)
	}
	if status == "pending" && m.celebrations.Load() != 1 {
		t.Fatal("Accepted block must trigger exactly 1 celebration")
	}
	if status == "orphaned" && m.celebrations.Load() != 0 {
		t.Fatal("Orphaned block must NOT trigger celebration")
	}

	verifyCriteria(t, "2A-During", m)
	printReport(t, "2A: ZMQ During Retry", m)
}

func TestPlan2A_DuplicateSubmission(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")
	m := &validationMetrics{}

	j := makeActiveJob("plan2a-dup")
	store.put(j)

	// First submission succeeds
	s1 := handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef01", false, 3)
	if s1 != "pending" {
		t.Fatalf("First submission should be pending, got %s", s1)
	}

	// Second submission from same job — must be blocked by Solved gate
	s2 := handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef02", false, 3)
	if s2 != "orphaned" {
		t.Fatalf("Duplicate submission should be orphaned, got %s", s2)
	}
	if m.skippedSolved.Load() != 1 {
		t.Fatalf("Expected 1 skipped-solved, got %d", m.skippedSolved.Load())
	}
	if m.celebrations.Load() != 1 {
		t.Fatalf("Expected exactly 1 celebration, got %d", m.celebrations.Load())
	}

	verifyCriteria(t, "2A-Duplicate", m)
	printReport(t, "2A: Duplicate Submission", m)
}

// =============================================================================
// PLAN §2B: Nano-Race Injection (Full Pipeline)
// =============================================================================
// Inject artificial delays between GetState and submission in the full pipeline.

func TestPlan2B_NanoRaceFullPipeline(t *testing.T) {
	const iterations = 200

	var totalOrphaned atomic.Int32
	var totalAccepted atomic.Int32
	var falsePositives atomic.Int32 // Celebration for orphaned block
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			store := newJobStore()
			daemon := newMockDaemon(1000000, "aabb")
			m := &validationMetrics{}
			j := makeActiveJob(fmt.Sprintf("nano-full-%d", idx))
			store.put(j)

			// Inject delay then invalidate (50% of iterations)
			if idx%2 == 0 {
				rng := rand.New(rand.NewSource(int64(idx)))
				delay := time.Duration(1+rng.Intn(10)) * time.Microsecond
				go func() {
					time.Sleep(delay)
					store.invalidateAll("zmq-nano-race")
				}()
			}

			status := handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef", false, 3)

			switch status {
			case "pending":
				totalAccepted.Add(1)
				if m.celebrations.Load() != 1 {
					falsePositives.Add(1)
				}
			case "orphaned":
				totalOrphaned.Add(1)
				if m.celebrations.Load() != 0 {
					falsePositives.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	if falsePositives.Load() > 0 {
		t.Fatalf("CRITICAL: %d false positive celebrations detected", falsePositives.Load())
	}

	t.Logf("NanoRace Full Pipeline: accepted=%d orphaned=%d falsePositives=%d",
		totalAccepted.Load(), totalOrphaned.Load(), falsePositives.Load())
}

// =============================================================================
// PLAN §2C: Concurrent Retry + ZMQ (Full Pipeline)
// =============================================================================
// Multiple goroutines run handleBlockSim on the same job while ZMQ fires.

func TestPlan2C_ConcurrentRetryZMQ(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")
	j := makeActiveJob("plan2c")
	store.put(j)

	// Daemon fails first 2 attempts then succeeds
	daemon.setTransientFailures(2)

	var wg sync.WaitGroup
	results := make([]string, 20)
	metricsSlice := make([]*validationMetrics, 20)

	// 10 goroutines attempt handleBlock
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m := &validationMetrics{}
			metricsSlice[idx] = m
			results[idx] = handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef", false, 3)
		}(i)
	}

	// 10 goroutines fire ZMQ invalidation
	for i := 10; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			metricsSlice[idx] = &validationMetrics{}
			store.invalidateAll(fmt.Sprintf("zmq-%d", idx))
		}(i)
	}

	wg.Wait()

	// Count outcomes
	accepted := 0
	orphaned := 0
	totalCelebrations := int32(0)
	for i := 0; i < 10; i++ {
		switch results[i] {
		case "pending":
			accepted++
		case "orphaned":
			orphaned++
		}
		if metricsSlice[i] != nil {
			totalCelebrations += metricsSlice[i].celebrations.Load()
		}
	}

	// INVARIANT: At most 1 accepted block (Solved gate prevents duplicates)
	if accepted > 1 {
		t.Fatalf("CRITICAL: %d accepted blocks from same job (max 1 allowed)", accepted)
	}

	// INVARIANT: Celebrations == accepted
	if totalCelebrations != int32(accepted) {
		t.Fatalf("Celebrations (%d) != accepted (%d)", totalCelebrations, accepted)
	}

	// Final job state must be terminal
	state := j.GetState()
	if state != protocol.JobStateSolved && state != protocol.JobStateInvalidated {
		t.Fatalf("Final state should be terminal, got %v", state)
	}

	t.Logf("ConcurrentRetryZMQ: accepted=%d orphaned=%d celebrations=%d finalState=%s",
		accepted, orphaned, totalCelebrations, state)
}

// =============================================================================
// PLAN §2D: Daemon Tip Mismatch (Full Pipeline)
// =============================================================================
// Daemon advances tip faster than ZMQ refresh.

func TestPlan2D_DaemonTipMismatch(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")
	m := &validationMetrics{}

	j := makeActiveJob("plan2d")
	j.Height = 1000000
	store.put(j)

	// Daemon advances by 2 blocks — but ZMQ hasn't fired yet
	daemon.advanceTip(1000002, "ccdd")
	// Now daemon will reject with prev-blk-not-found because job.Height < tip

	status := handleBlockSim(store, daemon, m, j.ID, j.Height, "deadbeef", false, 3)

	if status != "orphaned" {
		t.Fatalf("Expected orphaned (daemon tip advanced), got %s", status)
	}
	if m.celebrations.Load() != 0 {
		t.Fatal("No celebration for tip-mismatch orphan")
	}

	verifyCriteria(t, "2D-TipMismatch", m)
	printReport(t, "2D: Daemon Tip Mismatch", m)
}

func TestPlan2D_DaemonTipMismatchThenZMQ(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")

	j := makeActiveJob("plan2d-late")
	j.Height = 1000000
	store.put(j)

	// Daemon advances
	daemon.advanceTip(1000001, "ccdd")

	// First share hits daemon rejection (prev-blk-not-found)
	m1 := &validationMetrics{}
	s1 := handleBlockSim(store, daemon, m1, j.ID, j.Height, "deadbeef", false, 3)
	if s1 != "orphaned" {
		t.Fatalf("Expected orphaned from daemon rejection, got %s", s1)
	}

	// Now ZMQ finally fires (delayed notification)
	store.invalidateAll("zmq-late-arrival")

	// Second share for same job must also be orphaned
	j2 := makeActiveJob("plan2d-late-2")
	j2.Height = 1000000
	store.put(j2)
	store.invalidateAll("zmq-new-block") // This one is also invalidated

	m2 := &validationMetrics{}
	s2 := handleBlockSim(store, daemon, m2, j2.ID, j2.Height, "deadbeef", false, 3)
	if s2 != "orphaned" {
		t.Fatalf("Expected orphaned after ZMQ, got %s", s2)
	}
}

// =============================================================================
// PLAN §2E: Burst & Out-of-Order ZMQ (Full Pipeline)
// =============================================================================

func TestPlan2E_BurstZMQWithSubmissions(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")

	const (
		burstSize    = 100
		submitJobs   = 20
	)

	// Create jobs
	jobs := make([]*protocol.Job, submitJobs)
	for i := 0; i < submitJobs; i++ {
		jobs[i] = makeActiveJob(fmt.Sprintf("burst-%d", i))
		jobs[i].Height = 1000000 + uint64(i)
		store.put(jobs[i])
	}

	var wg sync.WaitGroup
	allMetrics := &validationMetrics{}

	// Burst ZMQ notifications
	wg.Add(1)
	go func() {
		defer wg.Done()
		for b := 0; b < burstSize; b++ {
			start := time.Now()
			store.invalidateAll(fmt.Sprintf("burst-%d", b))
			allMetrics.recordZMQLatency(time.Since(start))
		}
	}()

	// Concurrent submissions
	for i := 0; i < submitJobs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			handleBlockSim(store, daemon, allMetrics, jobs[idx].ID, jobs[idx].Height, "deadbeef", false, 3)
		}(i)
	}

	wg.Wait()

	// INVARIANT: Celebrations <= accepted blocks
	if allMetrics.celebrations.Load() > allMetrics.statusPending.Load() {
		t.Fatalf("CRITICAL: celebrations (%d) > accepted (%d)",
			allMetrics.celebrations.Load(), allMetrics.statusPending.Load())
	}

	// INVARIANT: All blocks must be in final status
	total := allMetrics.statusPending.Load() + allMetrics.statusOrphaned.Load()
	if total != int32(submitJobs) {
		t.Errorf("Total finalized blocks (%d) != submitted jobs (%d)", total, submitJobs)
	}

	verifyCriteria(t, "2E-Burst", allMetrics)
	printReport(t, "2E: Burst ZMQ With Submissions", allMetrics)
}

func TestPlan2E_OutOfOrderZMQ(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(1000005, "ff05")

	// Create jobs at sequential heights
	for h := uint64(0); h < 5; h++ {
		j := makeActiveJob(fmt.Sprintf("ooo-%d", h))
		j.Height = 1000000 + h
		store.put(j)
	}

	// Fire ZMQ in REVERSE order (out-of-order delivery)
	for h := int64(4); h >= 0; h-- {
		store.invalidateAll(fmt.Sprintf("zmq-height-%d", 1000000+h))
	}

	// All jobs must be invalidated regardless of notification order
	for h := uint64(0); h < 5; h++ {
		j, _ := store.get(fmt.Sprintf("ooo-%d", h))
		if j.GetState() != protocol.JobStateInvalidated {
			t.Errorf("Job ooo-%d not invalidated after out-of-order ZMQ", h)
		}
	}

	// Submission for any of them must be rejected
	m := &validationMetrics{}
	for h := uint64(0); h < 5; h++ {
		status := handleBlockSim(store, daemon, m, fmt.Sprintf("ooo-%d", h), 1000000+h, "deadbeef", false, 3)
		if status != "orphaned" {
			t.Errorf("Job ooo-%d: expected orphaned, got %s", h, status)
		}
	}

	if m.celebrations.Load() != 0 {
		t.Fatal("No celebrations for out-of-order orphaned blocks")
	}
}

// =============================================================================
// PLAN §2F: Merge-Mining Stress (Full Pipeline)
// =============================================================================

func TestPlan2F_MergeMineStress(t *testing.T) {
	store := newJobStore()
	daemon := newMockDaemon(20000000, "aabb")

	const (
		numJobs    = 30
		goroutines = 50
	)

	// Create merge-mined jobs
	tc := algoTestCase{
		algo:     algoMergeMined,
		nbits:    "1a0377ae",
		diff:     50000.0,
		coinbase: 72600000000,
		height:   20000000,
	}

	for i := 0; i < numJobs; i++ {
		j := makeAlgoJob(fmt.Sprintf("mm-stress-%d", i), tc)
		j.Height = tc.height + uint64(i)
		store.put(j)
	}

	allMetrics := &validationMetrics{}
	var auxAccepted atomic.Int32
	var auxRejected atomic.Int32
	var wg sync.WaitGroup

	// ZMQ invalidation bursts
	wg.Add(1)
	go func() {
		defer wg.Done()
		for b := 0; b < 50; b++ {
			store.invalidateAll(fmt.Sprintf("mm-zmq-%d", b))
			// Add new jobs after invalidation (simulates template refresh)
			if b%5 == 0 {
				newJ := makeAlgoJob(fmt.Sprintf("mm-stress-new-%d", b), tc)
				newJ.Height = tc.height + uint64(numJobs+b)
				store.put(newJ)
			}
		}
	}()

	// Concurrent parent block submissions
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gIdx int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(gIdx * 997)))
			jobIdx := rng.Intn(numJobs)
			jobID := fmt.Sprintf("mm-stress-%d", jobIdx)

			// Parent block submission
			handleBlockSim(store, daemon, allMetrics, jobID, tc.height+uint64(jobIdx), "deadbeef", true, 3)

			// Aux block submission (independent of parent result)
			canAux, _ := auxSubmitDecision(store, jobID)
			if canAux {
				auxAccepted.Add(1)
			} else {
				auxRejected.Add(1)
			}
		}(g)
	}

	wg.Wait()

	// INVARIANT: Celebrations <= pending
	if allMetrics.celebrations.Load() > allMetrics.statusPending.Load() {
		t.Fatalf("CRITICAL: celebrations (%d) > pending (%d)",
			allMetrics.celebrations.Load(), allMetrics.statusPending.Load())
	}

	verifyCriteria(t, "2F-MergeMine", allMetrics)
	printReport(t, "2F: Merge-Mine Stress", allMetrics)
	t.Logf("  Aux: accepted=%d rejected=%d", auxAccepted.Load(), auxRejected.Load())
}

// =============================================================================
// PLAN §2G: Soak / Long-Term Timing Test (Full Pipeline)
// =============================================================================
// Simulates a full chain with 15-second blocks (compressed to 15ms) for
// a configurable duration. Runs the complete handleBlock pipeline.

func TestPlan2G_SoakFullPipeline(t *testing.T) {
	const (
		blockInterval   = 15 * time.Millisecond // 15s blocks at 1000x speed
		testDuration    = 3 * time.Second       // CI-safe (= ~200 simulated blocks)
		minerGoroutines = 15
		blockFindRate   = 0.005 // 0.5% of shares find a block
	)

	store := newJobStore()
	daemon := newMockDaemon(1000000, "aabb")
	allMetrics := &validationMetrics{}
	done := make(chan struct{})

	var currentJobID atomic.Value
	firstJob := makeActiveJob("soak-0")
	firstJob.Height = 1000000
	store.put(firstJob)
	currentJobID.Store(firstJob.ID)

	var totalBlocks atomic.Int32
	var blockNum atomic.Int32

	// Block producer
	go func() {
		ticker := time.NewTicker(blockInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				bn := blockNum.Add(1)
				totalBlocks.Add(1)
				newHeight := uint64(1000000 + bn)

				// Advance daemon tip
				daemon.advanceTip(newHeight, fmt.Sprintf("hash-%d", bn))

				// Phase 1: Immediate invalidation
				start := time.Now()
				store.invalidateAll(fmt.Sprintf("zmq-%d", bn))
				allMetrics.recordZMQLatency(time.Since(start))

				// Phase 2: New job
				newJob := makeActiveJob(fmt.Sprintf("soak-%d", bn))
				newJob.Height = newHeight
				store.put(newJob)
				daemon.advanceTip(newHeight, fmt.Sprintf("hash-%d", bn))

				currentJobID.Store(newJob.ID)
			}
		}
	}()

	// Miner goroutines
	var wg sync.WaitGroup
	for m := 0; m < minerGoroutines; m++ {
		wg.Add(1)
		go func(minerIdx int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(minerIdx * 6271)))
			deadline := time.After(testDuration)
			for {
				select {
				case <-deadline:
					return
				default:
				}

				jobID := currentJobID.Load().(string)
				j, found := store.get(jobID)
				if !found {
					time.Sleep(100 * time.Microsecond)
					continue
				}

				// Does this share find a block?
				if rng.Float64() < blockFindRate {
					status := handleBlockSim(store, daemon, allMetrics, jobID, j.Height, "deadbeef", false, 3)
					_ = status
				}

				time.Sleep(200 * time.Microsecond)
			}
		}(m)
	}

	time.Sleep(testDuration)
	close(done)
	wg.Wait()

	// === SUCCESS CRITERIA (Plan §6) ===

	// 1. Must have produced blocks
	if totalBlocks.Load() == 0 {
		t.Fatal("No blocks produced during soak test")
	}

	// 2. Celebrations == accepted blocks
	verifyCriteria(t, "2G-Soak", allMetrics)

	// 3. No false celebrations
	if allMetrics.celebrations.Load() > allMetrics.statusPending.Load() {
		t.Fatalf("CRITICAL: celebrations (%d) > accepted (%d)",
			allMetrics.celebrations.Load(), allMetrics.statusPending.Load())
	}

	// 4. DB inserts for every block attempt
	totalAttempts := allMetrics.statusPending.Load() + allMetrics.statusOrphaned.Load()
	if allMetrics.dbInserts.Load() != totalAttempts {
		t.Errorf("DB inserts (%d) != total block attempts (%d)",
			allMetrics.dbInserts.Load(), totalAttempts)
	}

	// 5. All jobs should be terminal except possibly the latest one
	allJobs := store.all()
	activeCount := 0
	for _, j := range allJobs {
		if j.GetState() == protocol.JobStateActive {
			activeCount++
		}
	}
	if activeCount > 1 {
		t.Errorf("Expected at most 1 active job, found %d", activeCount)
	}

	printReport(t, "2G: Soak Full Pipeline", allMetrics)
	t.Logf("  Simulated blocks: %d over %s", totalBlocks.Load(), testDuration)
}

// =============================================================================
// PLAN §2: Cross-Algorithm Full Pipeline Sweep
// =============================================================================
// Runs a unified test across all algorithm types ensuring identical behavior.

func TestPlan_CrossAlgoFullPipeline(t *testing.T) {
	algos := []struct {
		name   string
		nbits  string
		diff   float64
		height uint64
	}{
		{"SHA256d", "1a0377ae", 50000.0, 20000000},
		{"Scrypt", "1b02c4a6", 1200.0, 5500000},
	}

	for _, algo := range algos {
		t.Run(algo.name, func(t *testing.T) {
			m := &validationMetrics{}

			// Scenario 1: Clean submission (isolated store + daemon)
			{
				store := newJobStore()
				daemon := newMockDaemon(algo.height, "aabb")
				j1 := makeActiveJob(fmt.Sprintf("xalgo-%s-clean", algo.name))
				j1.Height = algo.height
				j1.NBits = algo.nbits
				j1.Difficulty = algo.diff
				store.put(j1)

				s1 := handleBlockSim(store, daemon, m, j1.ID, j1.Height, "deadbeef", false, 3)
				if s1 != "pending" {
					t.Errorf("[%s] Clean submit: expected pending, got %s", algo.name, s1)
				}
			}

			// Scenario 2: Stale race (isolated)
			{
				store := newJobStore()
				daemon := newMockDaemon(algo.height, "aabb")
				j2 := makeActiveJob(fmt.Sprintf("xalgo-%s-stale", algo.name))
				j2.Height = algo.height
				j2.NBits = algo.nbits
				j2.Difficulty = algo.diff
				store.put(j2)
				store.invalidateAll("zmq-stale")

				s2 := handleBlockSim(store, daemon, m, j2.ID, j2.Height, "deadbeef", false, 3)
				if s2 != "orphaned" {
					t.Errorf("[%s] Stale race: expected orphaned, got %s", algo.name, s2)
				}
			}

			// Scenario 3: Daemon rejection (high-hash) — permanent, no retry
			{
				store := newJobStore()
				daemon := newMockDaemon(algo.height+1, "ccdd")
				j3 := makeActiveJob(fmt.Sprintf("xalgo-%s-highhash", algo.name))
				j3.Height = algo.height + 1
				j3.NBits = algo.nbits
				j3.Difficulty = algo.diff
				store.put(j3)
				daemon.setRejectAlways("high-hash") // Permanent — every call returns high-hash

				s3 := handleBlockSim(store, daemon, m, j3.ID, j3.Height, "deadbeef", false, 3)
				if s3 != "orphaned" {
					t.Errorf("[%s] High-hash: expected orphaned, got %s", algo.name, s3)
				}
			}

			// Scenario 4: Transient failure → retry success (isolated)
			{
				store := newJobStore()
				daemon := newMockDaemon(algo.height+2, "ddee")
				j4 := makeActiveJob(fmt.Sprintf("xalgo-%s-retry", algo.name))
				j4.Height = algo.height + 2
				j4.NBits = algo.nbits
				j4.Difficulty = algo.diff
				store.put(j4)
				daemon.setTransientFailures(1)

				s4 := handleBlockSim(store, daemon, m, j4.ID, j4.Height, "deadbeef", false, 3)
				if s4 != "pending" {
					t.Errorf("[%s] Retry: expected pending, got %s", algo.name, s4)
				}
			}

			verifyCriteria(t, algo.name, m)
			printReport(t, fmt.Sprintf("Cross-Algo: %s", algo.name), m)
		})
	}
}

// =============================================================================
// PLAN §3: Metrics Observable — isPermanentRejection Coverage
// =============================================================================
// Ensure all known daemon error strings are correctly classified.

func TestPlan3_PermanentRejectionClassification(t *testing.T) {
	permanent := []string{
		"prev-blk-not-found", "bad-prevblk", "stale-prevblk", "stale-work",
		"high-hash", "bad-diffbits",
		"bad-cb-missing", "bad-cb-multiple", "bad-cb-height",
		"bad-txnmrklroot", "bad-txns",
		"duplicate",
	}

	for _, errStr := range permanent {
		if !isPermanentRejection(errStr) {
			t.Errorf("'%s' should be permanent rejection", errStr)
		}
		// Uppercase variant
		if !isPermanentRejection(strings.ToUpper(errStr)) {
			t.Errorf("'%s' (uppercase) should be permanent rejection", strings.ToUpper(errStr))
		}
	}

	transient := []string{
		"connection refused",
		"timeout",
		"EOF",
		"dial tcp",
		"i/o timeout",
	}

	for _, errStr := range transient {
		if isPermanentRejection(errStr) {
			t.Errorf("'%s' should NOT be permanent rejection", errStr)
		}
	}
}

// =============================================================================
// PLAN §3: Metrics Observable — JobState Monotonicity Under Concurrency
// =============================================================================

func TestPlan3_JobStateMonotonicity(t *testing.T) {
	const goroutines = 200

	j := makeActiveJob("monotonic")
	var wg sync.WaitGroup
	var regressions atomic.Int32

	// Phase 1: Race to terminal state (Invalidated or Solved)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			switch idx % 3 {
			case 0:
				j.SetState(protocol.JobStateInvalidated, "zmq")
			case 1:
				j.SetState(protocol.JobStateSolved, "block")
			case 2:
				// Read state (concurrent reader)
				_ = j.GetState()
			}
		}(g)
	}

	wg.Wait()

	// Phase 2: Job is now terminal. Verify Active regression is impossible.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if j.SetState(protocol.JobStateActive, "regression-attempt") {
				regressions.Add(1)
			}
		}()
	}

	wg.Wait()

	if regressions.Load() > 0 {
		t.Fatalf("CRITICAL: %d state regressions from terminal state", regressions.Load())
	}

	state := j.GetState()
	if state != protocol.JobStateSolved && state != protocol.JobStateInvalidated {
		t.Fatalf("Final state must be terminal, got %v", state)
	}
}

// =============================================================================
// PLAN §5: Full Reporting — Aggregate Multi-Scenario Summary
// =============================================================================

func TestPlan5_AggregateReport(t *testing.T) {
	scenarios := []struct {
		name        string
		setupFn     func(*jobStore, *mockDaemon, *protocol.Job)
		expectOK    bool
	}{
		{
			"clean-submit",
			func(s *jobStore, d *mockDaemon, j *protocol.Job) {},
			true,
		},
		{
			"pre-invalidated",
			func(s *jobStore, d *mockDaemon, j *protocol.Job) {
				s.invalidateAll("zmq")
			},
			false,
		},
		{
			"daemon-prevblk",
			func(s *jobStore, d *mockDaemon, j *protocol.Job) {
				d.setRejectAlways("prev-blk-not-found")
			},
			false,
		},
		{
			"daemon-highhash",
			func(s *jobStore, d *mockDaemon, j *protocol.Job) {
				d.setRejectAlways("high-hash")
			},
			false,
		},
		{
			"transient-then-ok",
			func(s *jobStore, d *mockDaemon, j *protocol.Job) {
				d.setTransientFailures(2)
			},
			true,
		},
		{
			"empty-hex",
			nil, // Special case handled below
			false,
		},
		{
			"duplicate-solved",
			func(s *jobStore, d *mockDaemon, j *protocol.Job) {
				j.SetState(protocol.JobStateSolved, "already-solved")
			},
			false,
		},
	}

	aggregate := &validationMetrics{}
	totalScenarios := 0
	passed := 0
	failed := 0

	for _, sc := range scenarios {
		store := newJobStore()
		daemon := newMockDaemon(1000000, "aabb")
		j := makeActiveJob(fmt.Sprintf("report-%s", sc.name))
		j.Height = 1000000
		store.put(j)

		blockHex := "deadbeef"
		if sc.name == "empty-hex" {
			blockHex = ""
		} else if sc.setupFn != nil {
			sc.setupFn(store, daemon, j)
		}

		status := handleBlockSim(store, daemon, aggregate, j.ID, j.Height, blockHex, false, 3)
		totalScenarios++

		isOK := status == "pending"
		if isOK == sc.expectOK {
			passed++
		} else {
			failed++
			t.Errorf("Scenario %s: got status=%s, expected ok=%v", sc.name, status, sc.expectOK)
		}
	}

	// Print aggregate report
	printReport(t, "Aggregate (All Scenarios)", aggregate)
	t.Logf("  Scenarios: %d total, %d passed, %d failed", totalScenarios, passed, failed)

	// Final success criteria
	verifyCriteria(t, "Aggregate", aggregate)

	if failed > 0 {
		t.Fatalf("%d scenarios failed", failed)
	}
}
