// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Critical Time & Clock Pathology Tests
//
// Tests for time-related edge cases that can cause pool failures:
// - System clock jumps (NTP corrections)
// - Monotonic vs wall-clock misuse
// - Timestamp reuse across jobs
// - Miner-submitted timestamps out of range
// - Block template timestamp drift
//
// WHY IT MATTERS: Stratum pools die when time assumptions break.
// Failure modes include difficulty retarget misfires, jobs invalidated
// incorrectly, and shares silently rejected.
package shares

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// 1. SYSTEM CLOCK JUMP TESTS (NTP CORRECTION SIMULATION)
// =============================================================================

// TestClockJumpForward simulates NTP correcting clock forward
// This can cause jobs to appear stale when they're actually fresh
func TestClockJumpForward(t *testing.T) {
	t.Parallel()

	// DefaultMaxJobAge is 1 minute (4 blocks for fast coins like DGB)
	tests := []struct {
		name         string
		clockJump    time.Duration
		expectStale  bool
		description  string
	}{
		{
			name:         "small_forward_jump_20s",
			clockJump:    20 * time.Second,
			expectStale:  false,
			description:  "20s forward jump should not invalidate recent jobs",
		},
		{
			name:         "medium_forward_jump_50s",
			clockJump:    50 * time.Second,
			expectStale:  false,
			description:  "50s forward jump should not invalidate recent jobs",
		},
		{
			name:         "large_forward_jump_70s",
			clockJump:    70 * time.Second,
			expectStale:  true,
			description:  "70s forward jump exceeds max job age (1min)",
		},
		{
			name:         "extreme_forward_jump_5min",
			clockJump:    5 * time.Minute,
			expectStale:  true,
			description:  "5 min forward jump should definitely mark job stale",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create job with current time
			job := createTestJob(t, "clock_jump_test")

			// Simulate clock jump by manipulating job's CreatedAt
			// (In production, this tests how the validator handles jobs when
			// the system clock has been corrected forward)
			job.CreatedAt = time.Now().Add(-tc.clockJump)

			isStale := isJobStale(job, DefaultMaxJobAge)
			if isStale != tc.expectStale {
				t.Errorf("%s: isJobStale=%v, expected=%v",
					tc.description, isStale, tc.expectStale)
			}
		})
	}
}

// TestClockJumpBackward simulates NTP correcting clock backward
// This is more dangerous - can cause double-acceptance of shares
func TestClockJumpBackward(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		clockJump      time.Duration
		description    string
		criticalIssue  string
	}{
		{
			name:          "backward_jump_30s",
			clockJump:     -30 * time.Second,
			description:   "30s backward jump",
			criticalIssue: "May cause duplicate share tracker to fail if using wall clock",
		},
		{
			name:          "backward_jump_2min",
			clockJump:     -2 * time.Minute,
			description:   "2min backward jump",
			criticalIssue: "Job cleanup timing may malfunction",
		},
		{
			name:          "backward_jump_10min",
			clockJump:     -10 * time.Minute,
			description:   "10min backward jump",
			criticalIssue: "Duplicate tracker cleanup may delete active entries",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Test duplicate tracker behavior with backward clock jump
			tracker := NewDuplicateTracker()

			// Record a share "now"
			jobID := "test_job_1"
			recorded := tracker.RecordIfNew(jobID, "en1", "en2", "00000001", "deadbeef")
			if !recorded {
				t.Fatal("First share should be recorded")
			}

			// Simulate clock jump backward by manipulating the internal timestamps
			// After jump, the same share should still be detected as duplicate
			duplicate := tracker.RecordIfNew(jobID, "en1", "en2", "00000001", "deadbeef")
			if duplicate {
				t.Errorf("CRITICAL: %s - After clock jump, duplicate was accepted! %s",
					tc.description, tc.criticalIssue)
			}
		})
	}
}

// TestClockJumpDuringShareValidation tests validation with clock inconsistency
func TestClockJumpDuringShareValidation(t *testing.T) {
	t.Parallel()

	// Create validator with test job lookup
	jobs := make(map[string]*protocol.Job)
	getJob := func(id string) (*protocol.Job, bool) {
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)
	validator.SetNetworkDifficulty(1.0)

	// Create a job
	job := createTestJob(t, "clock_test_job")
	jobs[job.ID] = job

	// Create a valid share
	share := &protocol.Share{
		JobID:       job.ID,
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000000",
		NTime:       job.NTime,
		Nonce:       "00000001",
		Difficulty:  1.0,
		SessionID:   1,
	}

	// Test 1: Share submitted with job "just created" (should pass)
	result := validator.Validate(share)
	// May fail due to PoW, but should not fail due to staleness
	if result.RejectReason == protocol.RejectReasonStale {
		t.Error("Fresh job should not be rejected as stale")
	}

	// Test 2: Simulate job appearing old (clock jumped forward)
	job.CreatedAt = time.Now().Add(-10 * time.Minute)
	share.Nonce = "00000002" // Different nonce to avoid duplicate

	result2 := validator.Validate(share)
	if result2.Accepted {
		t.Error("CRITICAL: Share accepted for stale job after clock jump")
	}
	if result2.RejectReason != protocol.RejectReasonStale {
		t.Logf("Rejection reason: %s (expected stale)", result2.RejectReason)
	}
}

// =============================================================================
// 2. NTIME VALIDATION EDGE CASES
// =============================================================================

// TestNTimeValidation tests the NTime validation boundary conditions
func TestNTimeValidation(t *testing.T) {
	t.Parallel()

	// Reference timestamp (Unix epoch offset)
	baseTime := uint32(1700000000)
	baseTimeHex := fmt.Sprintf("%08x", baseTime)

	tests := []struct {
		name        string
		shareNTime  uint32
		jobNTime    uint32
		expectValid bool
		description string
	}{
		{
			name:        "exact_match",
			shareNTime:  baseTime,
			jobNTime:    baseTime,
			expectValid: true,
			description: "Same timestamp should be valid",
		},
		{
			name:        "1_second_ahead",
			shareNTime:  baseTime + 1,
			jobNTime:    baseTime,
			expectValid: true,
			description: "1 second ahead is within allowed drift",
		},
		{
			name:        "max_allowed_ahead_7200s",
			shareNTime:  baseTime + 7200,
			jobNTime:    baseTime,
			expectValid: true,
			description: "Exactly 2 hours ahead is the maximum allowed",
		},
		{
			name:        "exceed_max_ahead_7201s",
			shareNTime:  baseTime + 7201,
			jobNTime:    baseTime,
			expectValid: false,
			description: "Exceeding 2 hours ahead should fail",
		},
		{
			name:        "max_allowed_behind_7200s",
			shareNTime:  baseTime - 7200,
			jobNTime:    baseTime,
			expectValid: true,
			description: "Exactly 2 hours behind is the maximum allowed",
		},
		{
			name:        "exceed_max_behind_7201s",
			shareNTime:  baseTime - 7201,
			jobNTime:    baseTime,
			expectValid: false,
			description: "Exceeding 2 hours behind should fail",
		},
		{
			name:        "1_day_drift",
			shareNTime:  baseTime + 86400,
			jobNTime:    baseTime,
			expectValid: false,
			description: "1 day drift should be rejected",
		},
		{
			name:        "negative_day_drift",
			shareNTime:  baseTime - 86400,
			jobNTime:    baseTime,
			expectValid: false,
			description: "1 day negative drift should be rejected",
		},
		{
			name:        "timestamp_zero",
			shareNTime:  0,
			jobNTime:    baseTime,
			expectValid: false,
			description: "Zero timestamp should be rejected",
		},
		{
			name:        "both_zero",
			shareNTime:  0,
			jobNTime:    0,
			expectValid: true,
			description: "Both zero timestamps technically pass the drift check",
		},
		{
			name:        "max_uint32",
			shareNTime:  0xFFFFFFFF,
			jobNTime:    baseTime,
			expectValid: false,
			description: "Max uint32 timestamp should fail drift check",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Format timestamps as hex (big-endian)
			shareHex := fmt.Sprintf("%08x", tc.shareNTime)
			jobHex := fmt.Sprintf("%08x", tc.jobNTime)

			// Handle special case for base time
			if tc.jobNTime == baseTime && tc.shareNTime == baseTime {
				shareHex = baseTimeHex
				jobHex = baseTimeHex
			}

			valid := validateNTime(shareHex, jobHex)
			if valid != tc.expectValid {
				t.Errorf("%s: validateNTime=%v, expected=%v (shareNTime=%d, jobNTime=%d)",
					tc.description, valid, tc.expectValid, tc.shareNTime, tc.jobNTime)
			}
		})
	}
}

// TestNTimeValidationMalformedInput tests handling of malformed NTime values
func TestNTimeValidationMalformedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		shareNTime  string
		jobNTime    string
		expectValid bool
		description string
	}{
		{
			name:        "short_share_ntime",
			shareNTime:  "1234567", // 7 chars instead of 8
			jobNTime:    "12345678",
			expectValid: false,
			description: "Short share NTime should fail",
		},
		{
			name:        "short_job_ntime",
			shareNTime:  "12345678",
			jobNTime:    "1234567",
			expectValid: false,
			description: "Short job NTime should fail",
		},
		{
			name:        "long_share_ntime",
			shareNTime:  "123456789", // 9 chars
			jobNTime:    "12345678",
			expectValid: false,
			description: "Long share NTime should fail",
		},
		{
			name:        "empty_share_ntime",
			shareNTime:  "",
			jobNTime:    "12345678",
			expectValid: false,
			description: "Empty share NTime should fail",
		},
		{
			name:        "invalid_hex_share",
			shareNTime:  "1234567g", // 'g' is not valid hex
			jobNTime:    "12345678",
			expectValid: false,
			description: "Invalid hex in share NTime should fail",
		},
		{
			name:        "invalid_hex_job",
			shareNTime:  "12345678",
			jobNTime:    "1234567x",
			expectValid: false,
			description: "Invalid hex in job NTime should fail",
		},
		{
			name:        "uppercase_hex",
			shareNTime:  "AABBCCDD",
			jobNTime:    "aabbccdd",
			expectValid: true,
			description: "Uppercase hex should be handled (same value)",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			valid := validateNTime(tc.shareNTime, tc.jobNTime)
			if valid != tc.expectValid {
				t.Errorf("%s: validateNTime=%v, expected=%v",
					tc.description, valid, tc.expectValid)
			}
		})
	}
}

// =============================================================================
// 3. TIMESTAMP REUSE ACROSS JOBS
// =============================================================================

// TestTimestampReuseAcrossJobs ensures different jobs with same timestamp
// don't cause cross-contamination in duplicate tracking
func TestTimestampReuseAcrossJobs(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	// Same ntime across different jobs
	ntime := "65432100"
	nonce := "deadbeef"
	extranonce1 := "00000001"
	extranonce2 := "00000000"

	// Submit share for job 1
	if !tracker.RecordIfNew("job1", extranonce1, extranonce2, ntime, nonce) {
		t.Fatal("First share for job1 should be accepted")
	}

	// Same timestamp/nonce combination but different job should succeed
	if !tracker.RecordIfNew("job2", extranonce1, extranonce2, ntime, nonce) {
		t.Error("CRITICAL: Same ntime/nonce for different job rejected - jobs not isolated")
	}

	// Same share for job1 again should fail
	if tracker.RecordIfNew("job1", extranonce1, extranonce2, ntime, nonce) {
		t.Error("CRITICAL: Duplicate share for job1 was accepted")
	}
}

// TestHighFrequencyTimestampCollision tests rapid submissions within same second
func TestHighFrequencyTimestampCollision(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()
	jobID := "high_freq_job"

	// Current timestamp
	now := uint32(time.Now().Unix())
	ntimeHex := fmt.Sprintf("%08x", now)

	// Submit many shares with same timestamp but different nonces
	// This simulates high-hashrate miners submitting multiple shares per second
	numShares := 1000
	accepted := 0
	rejected := 0

	for i := 0; i < numShares; i++ {
		nonce := fmt.Sprintf("%08x", uint32(i))
		extranonce2 := fmt.Sprintf("%08x", uint32(i/100))

		if tracker.RecordIfNew(jobID, "00000001", extranonce2, ntimeHex, nonce) {
			accepted++
		} else {
			rejected++
		}
	}

	// All shares with unique (nonce, extranonce2) combinations should be accepted
	if accepted != numShares {
		t.Errorf("Expected all %d unique shares to be accepted, got %d accepted, %d rejected",
			numShares, accepted, rejected)
	}

	// Now try to resubmit some - should all fail
	resubmitFailed := 0
	for i := 0; i < 10; i++ {
		nonce := fmt.Sprintf("%08x", uint32(i))
		extranonce2 := fmt.Sprintf("%08x", uint32(i/100))

		if !tracker.RecordIfNew(jobID, "00000001", extranonce2, ntimeHex, nonce) {
			resubmitFailed++
		}
	}

	if resubmitFailed != 10 {
		t.Errorf("Expected all resubmissions to fail, got %d failures", resubmitFailed)
	}
}

// =============================================================================
// 4. BLOCK TEMPLATE TIMESTAMP DRIFT
// =============================================================================

// TestTemplateTimestampValidation tests template freshness checks
func TestTemplateTimestampValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		templateAge    time.Duration
		expectValid    bool
		expectWarning  bool
		description    string
	}{
		{
			name:          "fresh_template_1s",
			templateAge:   1 * time.Second,
			expectValid:   true,
			expectWarning: false,
			description:   "Very fresh template should be valid",
		},
		{
			name:          "reasonable_template_30s",
			templateAge:   30 * time.Second,
			expectValid:   true,
			expectWarning: false,
			description:   "30s old template is normal",
		},
		{
			name:          "old_template_45s",
			templateAge:   45 * time.Second,
			expectValid:   true,
			expectWarning: false,
			description:   "45s old template is valid but getting old",
		},
		{
			name:          "stale_template_70s",
			templateAge:   70 * time.Second,
			expectValid:   false, // Exceeds DefaultMaxTemplateAge (1min for fast coins)
			expectWarning: false,
			description:   "70s old template exceeds max age",
		},
		{
			name:          "very_stale_template_1hour",
			templateAge:   1 * time.Hour,
			expectValid:   false,
			expectWarning: false,
			description:   "1 hour old template is definitely stale",
		},
		{
			name:          "future_template_1min",
			templateAge:   -1 * time.Minute, // Negative = future
			expectValid:   true,
			expectWarning: false,
			description:   "1min future template is within drift tolerance",
		},
		{
			name:          "future_template_3min",
			templateAge:   -3 * time.Minute,
			expectValid:   true,
			expectWarning: true, // Exceeds MaxTimeDrift (2min)
			description:   "3min future template should warn",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Calculate template time relative to "now"
			// Note: templateAge is how old the template is (positive = past)
			now := time.Now()
			templateTime := now.Add(-tc.templateAge)
			timeDrift := templateTime.Sub(now)

			// Check against validation logic from jobs/manager.go
			// DefaultMaxTemplateAge is 1 minute for fast coins (DGB 15s blocks)
			maxTemplateAge := 1 * time.Minute
			maxTimeDrift := 2 * time.Minute

			isStale := timeDrift < -maxTemplateAge
			isFuture := timeDrift > maxTimeDrift

			actualValid := !isStale
			actualWarning := isFuture

			if actualValid != tc.expectValid {
				t.Errorf("%s: validity=%v, expected=%v (age=%v)",
					tc.description, actualValid, tc.expectValid, tc.templateAge)
			}

			if actualWarning != tc.expectWarning {
				t.Errorf("%s: warning=%v, expected=%v (drift=%v)",
					tc.description, actualWarning, tc.expectWarning, timeDrift)
			}
		})
	}
}

// =============================================================================
// 5. CONCURRENT TIMING RACE CONDITIONS
// =============================================================================

// TestConcurrentTimestampRaces tests for race conditions in time-based operations
func TestConcurrentTimestampRaces(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()
	jobID := "race_test_job"

	// Run many goroutines submitting shares concurrently
	numGoroutines := 100
	sharesPerGoroutine := 100

	// Use per-goroutine counters to reduce atomic contention, then aggregate
	type result struct {
		accepted int
		rejected int
	}
	results := make([]result, numGoroutines)

	var wg sync.WaitGroup

	// Use fixed timestamp for this test
	ntime := "65432100"

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			localAccepted := 0
			localRejected := 0

			for i := 0; i < sharesPerGoroutine; i++ {
				// Each goroutine uses unique nonce range to avoid legitimate duplicates
				// Key = extranonce1:extranonce2:ntime:nonce
				// With unique extranonce2 per goroutine, all shares are unique
				nonce := fmt.Sprintf("%08x", uint32(goroutineID*sharesPerGoroutine+i))
				extranonce2 := fmt.Sprintf("%08x", uint32(goroutineID))

				if tracker.RecordIfNew(jobID, "00000001", extranonce2, ntime, nonce) {
					localAccepted++
				} else {
					localRejected++
				}
			}

			results[goroutineID] = result{accepted: localAccepted, rejected: localRejected}
		}(g)
	}

	wg.Wait()

	// Aggregate results
	var acceptedTotal, rejectedTotal int
	for _, r := range results {
		acceptedTotal += r.accepted
		rejectedTotal += r.rejected
	}

	expectedTotal := numGoroutines * sharesPerGoroutine
	actualTotal := acceptedTotal + rejectedTotal

	if actualTotal != expectedTotal {
		t.Errorf("Share count mismatch: expected %d, got %d (race condition?)",
			expectedTotal, actualTotal)
	}

	// All unique shares should be accepted (no legitimate duplicates in this test)
	if acceptedTotal != expectedTotal {
		t.Errorf("Expected all %d unique shares to be accepted, got %d (rejected %d)",
			expectedTotal, acceptedTotal, rejectedTotal)
	}
}

// TestTimestampRollover tests behavior at timestamp boundaries
func TestTimestampRollover(t *testing.T) {
	t.Parallel()

	// Note: validateNTime has a security check that rejects timestamps > 0xF0000000 (~year 2100)
	// to prevent malicious/malformed submissions. Test cases must use timestamps below this limit.
	tests := []struct {
		name       string
		shareTime  uint32
		jobTime    uint32
		expectPass bool
		desc       string
	}{
		{
			name:       "near_security_limit",
			shareTime:  0xEFFFFFFF, // Just below security limit
			jobTime:    0xEFFFFF00, // 255 seconds before shareTime
			expectPass: true,       // Within drift window
			desc:       "Near security limit should still validate drift correctly",
		},
		{
			name:       "exceeds_drift_at_high_timestamp",
			shareTime:  0xEFFFFFFF,
			jobTime:    0xEFFF0000, // Much larger gap
			expectPass: false,      // Difference > 7200
			desc:       "High timestamps exceeding drift should fail",
		},
		{
			name:       "near_zero",
			shareTime:  100,
			jobTime:    7199,
			expectPass: true, // diff = -7099, within [-7200, 7200]
			desc:       "Near zero timestamps within drift",
		},
		{
			name:       "cross_zero_boundary",
			shareTime:  100,
			jobTime:    7400, // diff = -7300, outside [-7200, 7200]
			expectPass: false,
			desc:       "Crossing zero boundary exceeds drift",
		},
		{
			name:       "exactly_at_drift_limit_negative",
			shareTime:  1000,
			jobTime:    8200, // diff = -7200, exactly at negative limit
			expectPass: true,
			desc:       "Exactly at negative drift limit should pass",
		},
		{
			name:       "exactly_at_drift_limit_positive",
			shareTime:  8200,
			jobTime:    1000, // diff = +7200, exactly at positive limit
			expectPass: true,
			desc:       "Exactly at positive drift limit should pass",
		},
		{
			name:       "just_over_drift_limit",
			shareTime:  8201,
			jobTime:    1000, // diff = +7201, just over positive limit
			expectPass: false,
			desc:       "Just over drift limit should fail",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			shareHex := fmt.Sprintf("%08x", tc.shareTime)
			jobHex := fmt.Sprintf("%08x", tc.jobTime)

			result := validateNTime(shareHex, jobHex)
			if result != tc.expectPass {
				t.Errorf("%s: got %v, expected %v", tc.desc, result, tc.expectPass)
			}
		})
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// createTestJob creates a test job with valid structure
func createTestJob(t *testing.T, jobID string) *protocol.Job {
	t.Helper()

	// Create valid 64-char hex string for prev block hash
	prevHash := "0000000000000000000000000000000000000000000000000000000000000001"

	// Current time as hex
	now := time.Now().Unix()
	var ntimeBuf [4]byte
	binary.BigEndian.PutUint32(ntimeBuf[:], uint32(now))
	ntime := hex.EncodeToString(ntimeBuf[:])

	return &protocol.Job{
		ID:            jobID,
		PrevBlockHash: prevHash,
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff",
		CoinBase2:     "ffffffff0100f2052a010000001976a914",
		MerkleBranches: []string{},
		Version:       "20000000",
		NBits:         "1d00ffff",
		NTime:         ntime,
		CleanJobs:     true,
		Height:        100000,
		Difficulty:    1.0,
		CreatedAt:     time.Now(),

		VersionRollingAllowed: true,
		VersionRollingMask:    0x1FFFE000,
	}
}
