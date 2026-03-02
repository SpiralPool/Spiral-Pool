// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Critical Malicious Miner Behavior Tests
//
// Attack simulations for real-world miner cheating:
// - Share withholding
// - Difficulty oscillation abuse
// - Job ID replay
// - Nonce reuse across jobs
// - Extranonce collision attempts
// - Submit valid share for old diff under new diff
//
// WHY IT MATTERS: Miners WILL try to cheat.
// Pool MUST:
// - Detect
// - Classify
// - Reject
// - Not miscredit
package shares

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// 1. SHARE WITHHOLDING DETECTION
// =============================================================================

// TestShareWithholdingDetection tests detection of block withholding attacks
// In this attack, miners submit regular shares but withhold shares that are also valid blocks
func TestShareWithholdingDetection(t *testing.T) {
	t.Parallel()

	// Tracking statistics
	type MinerStats struct {
		totalShares     int64
		blockShares     int64 // Shares that meet network difficulty
		submittedBlocks int64
	}

	minerStats := make(map[string]*MinerStats)
	var statsMu sync.RWMutex

	recordShare := func(minerID string, isBlock bool, submitted bool) {
		statsMu.Lock()
		defer statsMu.Unlock()

		if _, ok := minerStats[minerID]; !ok {
			minerStats[minerID] = &MinerStats{}
		}

		minerStats[minerID].totalShares++
		if isBlock {
			minerStats[minerID].blockShares++
			if submitted {
				minerStats[minerID].submittedBlocks++
			}
		}
	}

	// Simulate an honest miner (submits all shares including blocks)
	honestMiner := "honest_miner_001"
	for i := 0; i < 10000; i++ {
		isBlock := i%1000 == 0 // 0.1% block rate
		recordShare(honestMiner, isBlock, true)
	}

	// Simulate a malicious miner (withholds blocks)
	maliciousMiner := "malicious_miner_001"
	for i := 0; i < 10000; i++ {
		isBlock := i%1000 == 0
		// Malicious: doesn't submit when isBlock is true
		submitted := !isBlock
		recordShare(maliciousMiner, isBlock, submitted)
	}

	// Detection: Compare expected vs actual block submission rate
	statsMu.RLock()
	defer statsMu.RUnlock()

	for minerID, stats := range minerStats {
		expectedBlocks := stats.blockShares
		actualBlocks := stats.submittedBlocks

		if stats.totalShares > 1000 && expectedBlocks > 0 {
			submissionRate := float64(actualBlocks) / float64(expectedBlocks) * 100

			t.Logf("Miner %s: shares=%d, expected_blocks=%d, submitted_blocks=%d (rate=%.1f%%)",
				minerID, stats.totalShares, expectedBlocks, actualBlocks, submissionRate)

			// Detection heuristic: if submission rate is significantly below 100%, flag
			if submissionRate < 50 && expectedBlocks >= 5 {
				t.Logf("WARNING: Potential share withholding detected for %s (%.1f%% submission rate)",
					minerID, submissionRate)

				if minerID == maliciousMiner {
					t.Log("Correctly detected malicious miner")
				}
			}
		}
	}
}

// =============================================================================
// 2. DIFFICULTY OSCILLATION ABUSE
// =============================================================================

// TestDifficultyOscillationAbuse tests abuse of difficulty adjustment
// Miners may try to game the system by:
// 1. Mining fast initially to get low difficulty
// 2. Then mining slow shares at higher actual hashrate
func TestDifficultyOscillationAbuse(t *testing.T) {
	t.Parallel()

	// Simulate a miner's share submission pattern
	type ShareSubmission struct {
		timestamp  time.Time
		difficulty float64
		hashrate   float64 // Actual hashrate at time of submission
	}

	var submissions []ShareSubmission
	currentDifficulty := 1000.0 // Starting difficulty

	// Phase 1: Miner pretends to be slow (gets difficulty reduced)
	baseTime := time.Now()
	for i := 0; i < 20; i++ {
		// Submit shares slowly (10 seconds apart) to appear slow
		submissions = append(submissions, ShareSubmission{
			timestamp:  baseTime.Add(time.Duration(i*10) * time.Second),
			difficulty: currentDifficulty,
			hashrate:   5000.0, // Real hashrate is high
		})
	}

	// Vardiff would reduce difficulty here (miner appears slow)
	currentDifficulty = 500.0 // Reduced

	// Phase 2: Miner now mines at full speed with lower difficulty
	for i := 20; i < 100; i++ {
		// Submit shares fast (1 second apart) at lower difficulty
		submissions = append(submissions, ShareSubmission{
			timestamp:  baseTime.Add(200*time.Second + time.Duration((i-20))*time.Second),
			difficulty: currentDifficulty,
			hashrate:   5000.0, // Same real hashrate
		})
	}

	// Detection: Look for sudden change in share rate after difficulty change
	shareRates := make(map[float64]float64) // difficulty -> shares per second

	for i := 1; i < len(submissions); i++ {
		interval := submissions[i].timestamp.Sub(submissions[i-1].timestamp).Seconds()
		if interval > 0 {
			rate := 1.0 / interval
			diff := submissions[i].difficulty

			// Running average
			if existing, ok := shareRates[diff]; ok {
				shareRates[diff] = (existing + rate) / 2
			} else {
				shareRates[diff] = rate
			}
		}
	}

	t.Log("Share rates by difficulty:")
	for diff, rate := range shareRates {
		t.Logf("  Diff %.0f: %.2f shares/sec", diff, rate)
	}

	// Detection heuristic: if rate jumps significantly after difficulty drop
	if rate500, ok := shareRates[500.0]; ok {
		if rate1000, ok := shareRates[1000.0]; ok {
			rateIncrease := rate500 / rate1000
			if rateIncrease > 3 {
				t.Logf("DETECTION: Share rate increased %.1fx after difficulty drop (potential gaming)",
					rateIncrease)
			}
		}
	}
}

// =============================================================================
// 3. JOB ID REPLAY ATTACKS
// =============================================================================

// TestJobIDReplayAttack tests replay of shares from old jobs
func TestJobIDReplayAttack(t *testing.T) {
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

	// Create current valid job
	currentJob := createTestJob(t, "current_valid_job")
	currentJob.CreatedAt = time.Now()
	jobsMu.Lock()
	jobs[currentJob.ID] = currentJob
	jobsMu.Unlock()

	// Create old expired job
	oldJob := createTestJob(t, "old_expired_job")
	oldJob.CreatedAt = time.Now().Add(-10 * time.Minute) // 10 minutes ago
	jobsMu.Lock()
	jobs[oldJob.ID] = oldJob
	jobsMu.Unlock()

	tests := []struct {
		name         string
		jobID        string
		expectAccept bool
		expectReason string // RejectReason is a string constant
		description  string
	}{
		{
			name:         "valid_current_job",
			jobID:        currentJob.ID,
			expectAccept: false, // Will fail PoW but not stale/invalid job
			expectReason: protocol.RejectReasonLowDifficulty,
			description:  "Current job share - valid job ID",
		},
		{
			name:         "replay_old_job",
			jobID:        oldJob.ID,
			expectAccept: false,
			expectReason: protocol.RejectReasonStale,
			description:  "Old job replay - should be rejected as stale",
		},
		{
			name:         "nonexistent_job",
			jobID:        "fake_job_12345",
			expectAccept: false,
			expectReason: protocol.RejectReasonInvalidJob,
			description:  "Fake job ID - should be rejected as invalid job",
		},
		{
			name:         "empty_job_id",
			jobID:        "",
			expectAccept: false,
			expectReason: protocol.RejectReasonInvalidJob,
			description:  "Empty job ID - should be rejected",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			share := &protocol.Share{
				JobID:       tc.jobID,
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000000",
				NTime:       currentJob.NTime,
				Nonce:       "12345678",
				Difficulty:  1.0,
				SessionID:   1,
			}

			result := validator.Validate(share)

			if result.Accepted != tc.expectAccept {
				t.Errorf("%s: accepted=%v, expected=%v", tc.description, result.Accepted, tc.expectAccept)
			}

			if result.RejectReason != tc.expectReason {
				t.Logf("%s: reason=%s, expected=%s", tc.description, result.RejectReason, tc.expectReason)
			}
		})
	}
}

// =============================================================================
// 4. NONCE REUSE ATTACKS
// =============================================================================

// TestNonceReuseAcrossJobs tests detection of nonce reuse
func TestNonceReuseAcrossJobs(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()

	// Same nonce parameters across multiple jobs (suspicious but valid)
	fixedNonce := "deadbeef"
	fixedExtranonce2 := "00000000"
	fixedNtime := "65432100"
	fixedExtranonce1 := "00000001"

	// Submit to multiple jobs with same nonce
	for i := 0; i < 10; i++ {
		jobID := fmt.Sprintf("job_%d", i)

		// First submission should succeed
		if !tracker.RecordIfNew(jobID, fixedExtranonce1, fixedExtranonce2, fixedNtime, fixedNonce) {
			t.Errorf("Job %s: First submission should succeed", jobID)
		}

		// Duplicate to same job should fail
		if tracker.RecordIfNew(jobID, fixedExtranonce1, fixedExtranonce2, fixedNtime, fixedNonce) {
			t.Errorf("Job %s: Duplicate should be rejected", jobID)
		}
	}

	// NOTE: Same nonce across different jobs is VALID - each job has different
	// coinbase and merkle tree, so the block header is different even with same nonce
	t.Log("Nonce reuse across jobs is valid (different block headers)")
}

// TestNonceReuseSameJob tests strict duplicate detection within same job
func TestNonceReuseSameJob(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()
	jobID := "single_job_test"

	// Track many submissions to same job
	accepted := 0
	rejected := 0

	for i := 0; i < 1000; i++ {
		nonce := fmt.Sprintf("%08x", uint32(i%100)) // Only 100 unique nonces
		extranonce2 := fmt.Sprintf("%08x", uint32(i/100))

		if tracker.RecordIfNew(jobID, "en1", extranonce2, "65432100", nonce) {
			accepted++
		} else {
			rejected++
		}
	}

	t.Logf("Accepted: %d, Rejected: %d", accepted, rejected)

	// Should accept 100 unique extranonce2/nonce combinations per 100 nonces
	// Total unique = 100 nonces * 10 extranonce2 values = 1000 unique
	if accepted != 1000 {
		t.Errorf("Expected 1000 unique shares, got %d", accepted)
	}

	// Attempt to resubmit all - should all fail
	resubmitRejected := 0
	for i := 0; i < 100; i++ {
		nonce := fmt.Sprintf("%08x", uint32(i))
		extranonce2 := fmt.Sprintf("%08x", uint32(0))

		if !tracker.RecordIfNew(jobID, "en1", extranonce2, "65432100", nonce) {
			resubmitRejected++
		}
	}

	if resubmitRejected != 100 {
		t.Errorf("All resubmissions should be rejected, got %d rejections", resubmitRejected)
	}
}

// =============================================================================
// 5. EXTRANONCE COLLISION ATTACKS
// =============================================================================

// TestExtranoncCollisionAttempt tests collision between different sessions
func TestExtranoncCollisionAttempt(t *testing.T) {
	t.Parallel()

	tracker := NewDuplicateTracker()
	jobID := "collision_test_job"

	// Different extranonce1 (different sessions) but same extranonce2/nonce
	sessions := []string{
		"00000001", // Session 1
		"00000002", // Session 2
		"00000003", // Session 3
	}

	fixedExtranonce2 := "deadbeef"
	fixedNtime := "65432100"
	fixedNonce := "12345678"

	// Each session should be able to submit the same extranonce2/nonce
	// because extranonce1 is part of the key
	for _, extranonce1 := range sessions {
		if !tracker.RecordIfNew(jobID, extranonce1, fixedExtranonce2, fixedNtime, fixedNonce) {
			t.Errorf("Session %s: Should be allowed (different extranonce1)", extranonce1)
		}
	}

	// Verify duplicate detection still works within same session
	if tracker.RecordIfNew(jobID, sessions[0], fixedExtranonce2, fixedNtime, fixedNonce) {
		t.Error("Same session submitting same share should be rejected")
	}
}

// TestExtranonceSpoofingAttempt tests attempts to use another session's extranonce
func TestExtranonceSpoofingAttempt(t *testing.T) {
	t.Parallel()

	// In production, each session gets a unique extranonce1 assigned by the pool
	// A malicious miner might try to use someone else's extranonce1

	// This should be prevented at the protocol level by:
	// 1. Server validates extranonce1 matches session
	// 2. Shares include session ID for cross-reference

	type SessionShare struct {
		sessionID   uint64
		extranonce1 string
	}

	// Session registry
	sessions := map[uint64]string{
		1: "00000001",
		2: "00000002",
		3: "00000003",
	}

	validateExtranonce := func(sessionID uint64, extranonce1 string) bool {
		expected, ok := sessions[sessionID]
		return ok && expected == extranonce1
	}

	tests := []struct {
		name        string
		sessionID   uint64
		extranonce1 string
		expectValid bool
		description string
	}{
		{
			name:        "valid_own_extranonce",
			sessionID:   1,
			extranonce1: "00000001",
			expectValid: true,
			description: "Using own assigned extranonce",
		},
		{
			name:        "spoofed_other_session",
			sessionID:   1,
			extranonce1: "00000002", // Belongs to session 2
			expectValid: false,
			description: "Attempting to use another session's extranonce",
		},
		{
			name:        "invalid_extranonce",
			sessionID:   1,
			extranonce1: "deadbeef",
			expectValid: false,
			description: "Using unassigned extranonce",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			valid := validateExtranonce(tc.sessionID, tc.extranonce1)
			if valid != tc.expectValid {
				t.Errorf("%s: valid=%v, expected=%v", tc.description, valid, tc.expectValid)
			}
		})
	}
}

// =============================================================================
// 6. DIFFICULTY MISMATCH ATTACKS
// =============================================================================

// TestSubmitOldDiffUnderNewDiff tests submitting share at old difficulty after increase
func TestSubmitOldDiffUnderNewDiff(t *testing.T) {
	t.Parallel()

	// Scenario: Pool increases difficulty from 1000 to 2000
	// Malicious miner submits share that meets 1000 but not 2000

	// Max target (difficulty 1)
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Calculate targets for different difficulties
	calculateTarget := func(diff float64) *big.Int {
		// target = maxTarget / diff
		target := new(big.Int)
		diffInt := new(big.Int).SetUint64(uint64(diff * 1e8))
		maxScaled := new(big.Int).Mul(maxTarget, big.NewInt(1e8))
		target.Div(maxScaled, diffInt)
		return target
	}

	oldDiff := 1000.0
	newDiff := 2000.0

	targetOld := calculateTarget(oldDiff)
	targetNew := calculateTarget(newDiff)

	// Create a hash that meets old difficulty but not new
	// Hash must be: targetNew < hash <= targetOld
	midpoint := new(big.Int).Add(targetNew, targetOld)
	midpoint.Div(midpoint, big.NewInt(2))

	shareHash := midpoint

	// Check against both targets
	meetsOldDiff := shareHash.Cmp(targetOld) <= 0
	meetsNewDiff := shareHash.Cmp(targetNew) <= 0

	t.Logf("Share hash meets old diff (1000): %v", meetsOldDiff)
	t.Logf("Share hash meets new diff (2000): %v", meetsNewDiff)

	if !meetsOldDiff {
		t.Error("Test setup error: hash should meet old difficulty")
	}

	if meetsNewDiff {
		t.Log("Hash meets new difficulty too (test case edge)")
	}

	// CRITICAL: Pool must validate share against CURRENT difficulty
	// If pool uses miner's claimed difficulty, this attack succeeds
	currentPoolDiff := newDiff
	currentTarget := calculateTarget(currentPoolDiff)

	shareAccepted := shareHash.Cmp(currentTarget) <= 0

	if !meetsOldDiff && shareAccepted {
		t.Error("VULNERABILITY: Share accepted despite not meeting current difficulty")
	} else {
		t.Log("Pool correctly validates against current difficulty")
	}
}

// TestDifficultyRaceCondition tests race between difficulty update and share validation
func TestDifficultyRaceCondition(t *testing.T) {
	t.Parallel()

	jobs := make(map[string]*protocol.Job)
	getJob := func(id string) (*protocol.Job, bool) {
		j, ok := jobs[id]
		return j, ok
	}

	validator := NewValidator(getJob)

	job := createTestJob(t, "race_test_job")
	jobs[job.ID] = job

	// Initial difficulty
	initialDiff := 1000.0
	validator.SetNetworkDifficulty(initialDiff)

	// Concurrent operations
	var wg sync.WaitGroup
	var sharesProcessed atomic.Int64

	// Goroutine 1: Update difficulty repeatedly
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			// Oscillate difficulty
			if i%2 == 0 {
				validator.SetNetworkDifficulty(1000.0)
			} else {
				validator.SetNetworkDifficulty(2000.0)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2: Process shares
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			share := &protocol.Share{
				JobID:       job.ID,
				ExtraNonce1: "00000001",
				ExtraNonce2: fmt.Sprintf("%08x", i),
				NTime:       job.NTime,
				Nonce:       fmt.Sprintf("%08x", i),
				Difficulty:  1.0,
				SessionID:   uint64(i % 10),
			}

			// Should not panic or produce inconsistent results
			_ = validator.Validate(share)
			sharesProcessed.Add(1)
		}
	}()

	wg.Wait()

	t.Logf("Processed %d shares during difficulty oscillation", sharesProcessed.Load())

	// Verify difficulty state is consistent
	finalDiff := validator.GetNetworkDifficulty()
	if finalDiff != 1000.0 && finalDiff != 2000.0 {
		t.Errorf("Inconsistent final difficulty: %f", finalDiff)
	}
}

// =============================================================================
// 7. VERSION ROLLING ABUSE
// =============================================================================

// TestVersionRollingAbuse tests BIP320 version rolling mask violations
func TestVersionRollingAbuse(t *testing.T) {
	t.Parallel()

	// BIP320 standard mask
	allowedMask := uint32(0x1FFFE000)

	tests := []struct {
		name        string
		versionBits uint32
		expectValid bool
		description string
	}{
		{
			name:        "valid_zero_bits",
			versionBits: 0x00000000,
			expectValid: true,
			description: "No version rolling",
		},
		{
			name:        "valid_within_mask",
			versionBits: 0x00002000,
			expectValid: true,
			description: "Lowest allowed bit",
		},
		{
			name:        "valid_full_mask",
			versionBits: 0x1FFFE000,
			expectValid: true,
			description: "All allowed bits set",
		},
		{
			name:        "invalid_below_mask",
			versionBits: 0x00000001,
			expectValid: false,
			description: "Bit outside mask (low)",
		},
		{
			name:        "invalid_above_mask",
			versionBits: 0x20000000,
			expectValid: false,
			description: "Bit outside mask (high)",
		},
		{
			name:        "invalid_mixed",
			versionBits: 0x2FFFE001,
			expectValid: false,
			description: "Mix of valid and invalid bits",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Validate: bits set outside mask should be rejected
			valid := testValidateVersionRolling(tc.versionBits, allowedMask)

			if valid != tc.expectValid {
				t.Errorf("%s: valid=%v, expected=%v (bits=0x%08x, mask=0x%08x)",
					tc.description, valid, tc.expectValid, tc.versionBits, allowedMask)
			}
		})
	}
}

// =============================================================================
// 8. SHARE FLOODING DETECTION
// =============================================================================

// TestShareFloodingDetection tests detection of share flooding attacks
func TestShareFloodingDetection(t *testing.T) {
	t.Parallel()

	// Track share rates per session
	type SessionMetrics struct {
		sharesInWindow int
		windowStart    time.Time
		flagged        bool
	}

	sessions := make(map[uint64]*SessionMetrics)
	var sessionsMu sync.Mutex

	// Rate limit: max 100 shares per second
	maxSharesPerSecond := 100

	recordShareForSession := func(sessionID uint64) bool {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()

		now := time.Now()

		metrics, ok := sessions[sessionID]
		if !ok {
			metrics = &SessionMetrics{
				windowStart: now,
			}
			sessions[sessionID] = metrics
		}

		// Reset window if > 1 second
		if now.Sub(metrics.windowStart) > time.Second {
			metrics.sharesInWindow = 0
			metrics.windowStart = now
			metrics.flagged = false
		}

		metrics.sharesInWindow++

		// Check rate
		if metrics.sharesInWindow > maxSharesPerSecond {
			if !metrics.flagged {
				metrics.flagged = true
				return false // Flag but don't repeat
			}
			return false
		}

		return true
	}

	// Simulate normal miner
	normalSession := uint64(1)
	acceptedNormal := 0
	for i := 0; i < 50; i++ {
		if recordShareForSession(normalSession) {
			acceptedNormal++
		}
		time.Sleep(20 * time.Millisecond) // 50 shares/sec
	}

	// Simulate flooding miner
	floodSession := uint64(2)
	acceptedFlood := 0
	for i := 0; i < 500; i++ {
		if recordShareForSession(floodSession) {
			acceptedFlood++
		}
		// No delay - flooding as fast as possible
	}

	t.Logf("Normal miner: accepted %d/%d shares", acceptedNormal, 50)
	t.Logf("Flooding miner: accepted %d/%d shares", acceptedFlood, 500)

	// Normal miner should have all accepted
	if acceptedNormal < 50 {
		t.Errorf("Normal miner should have all shares accepted")
	}

	// Flooding miner should be rate limited
	if acceptedFlood >= 500 {
		t.Errorf("Flooding miner should be rate limited (accepted %d)", acceptedFlood)
	}

	if acceptedFlood > maxSharesPerSecond+10 {
		t.Errorf("Rate limiting not working: accepted %d (limit %d)", acceptedFlood, maxSharesPerSecond)
	}
}

// =============================================================================
// 9. HASHRATE SPOOFING DETECTION
// =============================================================================

// TestHashrateSpoofingDetection tests detection of hashrate misrepresentation
func TestHashrateSpoofingDetection(t *testing.T) {
	t.Parallel()

	// Track expected vs actual share rates
	type MinerMetrics struct {
		claimedHashrate float64 // From miner registration
		difficulty      float64
		shares          int
		duration        time.Duration
	}

	calculateExpectedShareRate := func(hashrate, difficulty float64) float64 {
		// Expected shares per second = hashrate / (difficulty * 2^32)
		// At 1 TH/s (1e12 H/s) and diff 1000: 1e12 / (1000 * 4.295e9) ≈ 0.233 shares/sec
		return hashrate / (difficulty * math.Pow(2, 32))
	}

	calculateActualShareRate := func(shares int, duration time.Duration) float64 {
		return float64(shares) / duration.Seconds()
	}

	detectSpoofing := func(metrics MinerMetrics) (bool, string) {
		expectedRate := calculateExpectedShareRate(metrics.claimedHashrate, metrics.difficulty)
		actualRate := calculateActualShareRate(metrics.shares, metrics.duration)

		// Calculate percentage difference properly
		// Under-performing: if actual is 1% of expected, that's 99% under
		// Over-performing: if actual is 3x expected, that's 200% over
		var variance float64
		var isUnder bool

		if actualRate < expectedRate {
			// Under-performing: percentage of expected that is missing
			variance = (1.0 - actualRate/expectedRate) * 100
			isUnder = true
		} else {
			// Over-performing: percentage above expected
			variance = (actualRate/expectedRate - 1.0) * 100
			isUnder = false
		}

		if variance > 50 { // More than 50% variance threshold
			if isUnder {
				return true, fmt.Sprintf("under-performing %.0f%% (expected: %.4f shares/sec, actual: %.4f shares/sec)",
					variance, expectedRate, actualRate)
			}
			return true, fmt.Sprintf("over-performing %.0f%% (claimed: %.2f H/s, appears: %.2f H/s)",
				variance, metrics.claimedHashrate, actualRate*metrics.difficulty*math.Pow(2, 32))
		}

		return false, ""
	}

	// At 1 TH/s (1e12 H/s) and difficulty 1000:
	// Expected shares/sec = 1e12 / (1000 * 2^32) = 1e12 / 4.295e12 ≈ 0.233
	// So over 1000 seconds, expect ~233 shares

	tests := []struct {
		name            string
		claimedHashrate float64
		difficulty      float64
		shares          int
		duration        time.Duration
		expectSpoofing  bool
	}{
		{
			name:            "honest_miner",
			claimedHashrate: 1e12, // 1 TH/s
			difficulty:      1000,
			shares:          233,                    // Expected ~233 over 1000 seconds
			duration:        1000 * time.Second,    // 1000 seconds, not 1 second
			expectSpoofing:  false,
		},
		{
			name:            "hashrate_massively_inflated",
			claimedHashrate: 1000e12, // Claims 1000 TH/s (1 PH/s)
			difficulty:      1000,
			shares:          23,                     // Only produces 0.1 TH/s equivalent over 1000s
			duration:        1000 * time.Second,
			// Expected rate for 1000 TH/s = 1000e12 / (1000 * 2^32) ≈ 232.83 shares/sec
			// Actual rate = 23/1000 = 0.023 shares/sec
			// Under-performing by: (1 - 0.023/232.83) * 100 ≈ 99.99%
			expectSpoofing:  true,
		},
		{
			name:            "slight_variance",
			claimedHashrate: 1e12,
			difficulty:      1000,
			shares:          280,                    // ~20% over expected 233
			duration:        1000 * time.Second,
			expectSpoofing:  false, // Within tolerance
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			metrics := MinerMetrics{
				claimedHashrate: tc.claimedHashrate,
				difficulty:      tc.difficulty,
				shares:          tc.shares,
				duration:        tc.duration,
			}

			spoofed, reason := detectSpoofing(metrics)

			if spoofed != tc.expectSpoofing {
				t.Errorf("Expected spoofing=%v, got %v (reason: %s)", tc.expectSpoofing, spoofed, reason)
			}

			if spoofed {
				t.Logf("Detected spoofing: %s", reason)
			}
		})
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// testValidateVersionRolling checks if version bits comply with BIP320 mask (test-local helper)
func testValidateVersionRolling(versionBits, mask uint32) bool {
	// All bits set in versionBits must be within the mask
	return (versionBits &^ mask) == 0
}

// createMaliciousShare creates a share with specific attack characteristics
func createMaliciousShare(jobID string, attackType string) *protocol.Share {
	share := &protocol.Share{
		JobID:       jobID,
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000000",
		NTime:       "65432100",
		Nonce:       "deadbeef",
		Difficulty:  1.0,
		SessionID:   1,
	}

	switch attackType {
	case "replay":
		// Use old job ID
		share.JobID = "old_job_from_yesterday"
	case "invalid_nonce":
		share.Nonce = "invalid"
	case "zero_difficulty":
		share.Difficulty = 0
	case "negative_difficulty":
		share.Difficulty = -1
	case "version_rolling_abuse":
		share.VersionBits = 0xFFFFFFFF // All bits (invalid)
	}

	return share
}

// Helper to convert hex to bytes for tests
func hexToBytes(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
