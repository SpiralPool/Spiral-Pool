// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Security tests for share validation.
//
// These tests verify the security hardening features added to the validator:
// - Stale share detection
// - BIP320 version rolling validation
// - Nonce exhaustion tracking
// - Duplicate share prevention
package shares

import (
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// TestStaleShareDetection verifies that old jobs are properly rejected.
// Default maxJobAge is 1 minute (4 blocks for DGB 15s).
// Formula: blockTime × 4, min 1 min, max 10 min.
func TestStaleShareDetection(t *testing.T) {
	tests := []struct {
		name      string
		jobAge    time.Duration
		maxAge    time.Duration
		wantStale bool
	}{
		{
			name:      "fresh job (30 seconds old) with default max",
			jobAge:    30 * time.Second,
			maxAge:    DefaultMaxJobAge, // 1 minute
			wantStale: false,
		},
		{
			name:      "recent job (50 seconds old) with default max",
			jobAge:    50 * time.Second,
			maxAge:    DefaultMaxJobAge,
			wantStale: false,
		},
		{
			name:      "stale job (70 seconds old) with default max",
			jobAge:    70 * time.Second,
			maxAge:    DefaultMaxJobAge, // 1 minute
			wantStale: true,
		},
		{
			name:      "very stale job (5 minutes old) with default max",
			jobAge:    5 * time.Minute,
			maxAge:    DefaultMaxJobAge,
			wantStale: true,
		},
		// Test coin-specific scaling
		// DGB (15s): maxAge = 1 min (4 blocks)
		{
			name:      "DGB: 45s job with 1 min max",
			jobAge:    45 * time.Second,
			maxAge:    1 * time.Minute,
			wantStale: false,
		},
		{
			name:      "DGB: 75s job with 1 min max",
			jobAge:    75 * time.Second,
			maxAge:    1 * time.Minute,
			wantStale: true,
		},
		// DOGE (60s): maxAge = 4 min (4 blocks)
		{
			name:      "DOGE: 3 min job with 4 min max",
			jobAge:    3 * time.Minute,
			maxAge:    4 * time.Minute,
			wantStale: false,
		},
		{
			name:      "DOGE: 5 min job with 4 min max",
			jobAge:    5 * time.Minute,
			maxAge:    4 * time.Minute,
			wantStale: true,
		},
		// BTC (600s): maxAge = 10 min (capped, ~1 block)
		{
			name:      "BTC: 8 min job with 10 min max",
			jobAge:    8 * time.Minute,
			maxAge:    10 * time.Minute,
			wantStale: false,
		},
		{
			name:      "BTC: 12 min job with 10 min max",
			jobAge:    12 * time.Minute,
			maxAge:    10 * time.Minute,
			wantStale: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &protocol.Job{
				ID:        "test-job",
				CreatedAt: time.Now().Add(-tt.jobAge),
			}

			got := isJobStale(job, tt.maxAge)
			if got != tt.wantStale {
				t.Errorf("isJobStale() = %v, want %v", got, tt.wantStale)
			}
		})
	}
}

// TestBIP320VersionRolling verifies BIP320 compliance.
func TestBIP320VersionRolling(t *testing.T) {
	// Standard BIP320 mask: bits 13-28 (0x1FFFE000)
	standardMask := uint32(0x1FFFE000)

	tests := []struct {
		name        string
		versionBits uint32
		mask        uint32
		wantValid   bool
	}{
		{
			name:        "no version rolling",
			versionBits: 0,
			mask:        standardMask,
			wantValid:   true,
		},
		{
			name:        "valid - single bit in mask",
			versionBits: 0x00002000, // Bit 13, which is in mask
			mask:        standardMask,
			wantValid:   true,
		},
		{
			name:        "valid - multiple bits in mask",
			versionBits: 0x0FFFE000, // All bits 13-27
			mask:        standardMask,
			wantValid:   true,
		},
		{
			name:        "valid - max allowed bits",
			versionBits: standardMask, // All mask bits set
			mask:        standardMask,
			wantValid:   true,
		},
		{
			name:        "invalid - bit below mask",
			versionBits: 0x00001000, // Bit 12, not in mask
			mask:        standardMask,
			wantValid:   false,
		},
		{
			name:        "invalid - bit above mask",
			versionBits: 0x20000000, // Bit 29, not in mask
			mask:        standardMask,
			wantValid:   false,
		},
		{
			name:        "invalid - bits outside mask",
			versionBits: 0x00000001, // Bit 0, definitely not in mask
			mask:        standardMask,
			wantValid:   false,
		},
		{
			name:        "invalid - mix of valid and invalid bits",
			versionBits: 0x1FFFE001, // Valid mask bits + bit 0
			mask:        standardMask,
			wantValid:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateVersionRolling(tt.versionBits, tt.mask)
			if got != tt.wantValid {
				t.Errorf("validateVersionRolling(0x%08X, 0x%08X) = %v, want %v",
					tt.versionBits, tt.mask, got, tt.wantValid)
			}
		})
	}
}

// TestNonceTracking verifies nonce exhaustion detection.
func TestNonceTracking(t *testing.T) {
	tracker := NewNonceTracker()

	sessionID := "session-1"
	jobID := "job-1"

	// Initial nonce should not trigger exhaustion
	exhausted := tracker.TrackNonce(sessionID, jobID, 0)
	if exhausted {
		t.Error("First nonce should not trigger exhaustion")
	}

	// Sequential nonces should not trigger exhaustion
	for i := uint32(1); i < 1000; i++ {
		exhausted = tracker.TrackNonce(sessionID, jobID, i)
		if exhausted {
			t.Errorf("Sequential nonce %d should not trigger exhaustion", i)
		}
	}

	// New job should reset counter
	exhausted = tracker.TrackNonce(sessionID, "job-2", 0)
	if exhausted {
		t.Error("New job should reset nonce counter")
	}
}

// TestNonceWrapDetection verifies nonce wrap-around detection.
func TestNonceWrapDetection(t *testing.T) {
	tracker := NewNonceTracker()

	sessionID := "session-wrap"
	jobID := "job-wrap"

	// Start near max uint32
	tracker.TrackNonce(sessionID, jobID, 0xFFFFFFFE)
	tracker.TrackNonce(sessionID, jobID, 0xFFFFFFFF)

	// Wrap to 0
	tracker.TrackNonce(sessionID, jobID, 0)
	tracker.TrackNonce(sessionID, jobID, 1)

	// Another wrap
	tracker.TrackNonce(sessionID, jobID, 0xFFFFFFFE)
	tracker.TrackNonce(sessionID, jobID, 0xFFFFFFFF)
	tracker.TrackNonce(sessionID, jobID, 0)

	// Third wrap should trigger threshold (NonceWrapThreshold = 3)
	tracker.TrackNonce(sessionID, jobID, 0xFFFFFFFE)
	tracker.TrackNonce(sessionID, jobID, 0xFFFFFFFF)
	exhausted := tracker.TrackNonce(sessionID, jobID, 0)

	if !exhausted {
		t.Error("Third wrap-around should trigger exhaustion warning")
	}
}

// TestParseNonce verifies nonce parsing.
func TestParseNonce(t *testing.T) {
	tests := []struct {
		name      string
		nonceHex  string
		wantNonce uint32
		wantErr   bool
	}{
		{
			name:      "valid nonce - zero",
			nonceHex:  "00000000",
			wantNonce: 0,
			wantErr:   false,
		},
		{
			name:      "valid nonce - max",
			nonceHex:  "ffffffff",
			wantNonce: 0xFFFFFFFF,
			wantErr:   false,
		},
		{
			name:      "valid nonce - typical value",
			nonceHex:  "12345678",
			wantNonce: 0x78563412, // Little-endian: bytes [0x12, 0x34, 0x56, 0x78] = 0x78563412
			wantErr:   false,
		},
		{
			name:     "invalid - too short",
			nonceHex: "1234",
			wantErr:  true,
		},
		{
			name:     "invalid - too long",
			nonceHex: "123456789a",
			wantErr:  true,
		},
		{
			name:     "invalid - non-hex characters",
			nonceHex: "1234567g",
			wantErr:  true,
		},
		{
			name:     "invalid - empty",
			nonceHex: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNonce(tt.nonceHex)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseNonce(%q) error = %v, wantErr %v", tt.nonceHex, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantNonce {
				t.Errorf("parseNonce(%q) = 0x%08X, want 0x%08X", tt.nonceHex, got, tt.wantNonce)
			}
		})
	}
}

// TestDuplicateTrackerConcurrencySecurity tests thread safety of duplicate tracking.
func TestDuplicateTrackerConcurrencySecurity(t *testing.T) {
	tracker := NewDuplicateTracker()

	// Run multiple goroutines accessing the tracker
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(workerID int) {
			for j := 0; j < 100; j++ {
				jobID := fmt.Sprintf("job-%d", j%10)
				en1 := fmt.Sprintf("en1-%d", workerID)
				en2 := fmt.Sprintf("en2-%d-%d", workerID, j)
				ntime := "12345678"
				nonce := fmt.Sprintf("%08x", j)

				// Check and record
				tracker.IsDuplicate(jobID, en1, en2, ntime, nonce)
				tracker.Record(jobID, en1, en2, ntime, nonce)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify stats are accessible
	jobs, shares := tracker.Stats()
	if jobs == 0 || shares == 0 {
		t.Error("Expected non-zero stats after concurrent operations")
	}
}

// TestDuplicateTrackerCleanupSecurity verifies old entries are cleaned up.
func TestDuplicateTrackerCleanupSecurity(t *testing.T) {
	tracker := NewDuplicateTracker()

	// Record a share
	tracker.Record("old-job", "en1", "en2", "12345678", "00000001")

	// Manually set the creation time to be old
	tracker.mu.Lock()
	if js, ok := tracker.jobs["old-job"]; ok {
		js.createdAt = time.Now().Unix() - (duplicateTrackerMaxAge + 100)
	}
	tracker.lastCleanup = time.Now().Unix() - 100 // Force cleanup on next record
	tracker.mu.Unlock()

	// Record another share to trigger cleanup
	tracker.Record("new-job", "en1", "en2", "12345678", "00000002")

	// Old job should be cleaned up
	tracker.mu.RLock()
	_, oldExists := tracker.jobs["old-job"]
	_, newExists := tracker.jobs["new-job"]
	tracker.mu.RUnlock()

	if oldExists {
		t.Error("Old job should have been cleaned up")
	}
	if !newExists {
		t.Error("New job should still exist")
	}
}

// TestSecurityStatsTracking verifies security metrics are tracked.
func TestSecurityStatsTracking(t *testing.T) {
	// Create a validator with a mock job lookup
	jobs := make(map[string]*protocol.Job)
	jobs["valid-job"] = &protocol.Job{
		ID:                    "valid-job",
		CreatedAt:             time.Now(),
		Version:               "20000000",
		PrevBlockHash:         "0000000000000000000000000000000000000000000000000000000000000000",
		NBits:                 "1d00ffff",
		NTime:                 "12345678",
		VersionRollingAllowed: true,
		VersionRollingMask:    0x1FFFE000,
	}

	getJob := func(id string) (*protocol.Job, bool) {
		job, ok := jobs[id]
		return job, ok
	}

	validator := NewValidator(getJob)

	// Get initial security stats
	initialStats := validator.SecurityStats()

	// The initial stats should be zero
	if initialStats.StaleShares != 0 || initialStats.NonceExhaustion != 0 || initialStats.VersionRollRejects != 0 {
		t.Error("Initial security stats should be zero")
	}
}
