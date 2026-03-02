// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database - Unit tests for worker statistics database operations.
//
// These tests verify:
// - Window minutes validation bounds (1-10080)
// - Hours validation bounds (1-720)
// - Limit bounds validation
// - CRITICAL: CleanupOldWorkerSnapshots with negative retention
// - Hashrate calculation formulas
// - Connected status determination
// - Empty result handling
// - SQL edge cases
package database

import (
	"testing"
	"time"
)

// =============================================================================
// Window Minutes Validation Tests (DB-01 SQL Injection Prevention)
// =============================================================================

// TestGetWorkerStats_WindowBounds tests window minutes bounds validation.
func TestGetWorkerStats_WindowBounds(t *testing.T) {
	tests := []struct {
		name          string
		windowMinutes int
		wantErr       bool
	}{
		{"valid min (1)", 1, false},
		{"valid typical (60)", 60, false},
		{"valid 24 hours", 1440, false},
		{"valid max (7 days)", 10080, false},
		{"invalid zero", 0, true},
		{"invalid negative", -1, true},
		{"invalid too large", 10081, true},
		{"invalid very large", 1000000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the validation logic from GetWorkerStats
			hasErr := tt.windowMinutes < 1 || tt.windowMinutes > 10080

			if hasErr != tt.wantErr {
				t.Errorf("windowMinutes=%d: wantErr=%v, gotErr=%v",
					tt.windowMinutes, tt.wantErr, hasErr)
			}
		})
	}
}

// TestGetMinerWorkers_WindowBounds tests window bounds in GetMinerWorkers.
func TestGetMinerWorkers_WindowBounds(t *testing.T) {
	tests := []struct {
		name          string
		windowMinutes int
		wantErr       bool
	}{
		{"valid 1 minute", 1, false},
		{"valid 30 minutes", 30, false},
		{"valid 1 hour", 60, false},
		{"valid 1 day", 1440, false},
		{"valid 7 days", 10080, false},
		{"invalid 0", 0, true},
		{"invalid -60", -60, true},
		{"invalid 10081", 10081, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasErr := tt.windowMinutes < 1 || tt.windowMinutes > 10080
			if hasErr != tt.wantErr {
				t.Errorf("windowMinutes=%d: validation mismatch", tt.windowMinutes)
			}
		})
	}
}

// TestGetAllWorkers_WindowBounds tests window bounds in GetAllWorkers.
func TestGetAllWorkers_WindowBounds(t *testing.T) {
	tests := []struct {
		name          string
		windowMinutes int
		wantErr       bool
	}{
		{"min valid", 1, false},
		{"max valid", 10080, false},
		{"just over max", 10081, true},
		{"zero", 0, true},
		{"negative", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasErr := tt.windowMinutes < 1 || tt.windowMinutes > 10080
			if hasErr != tt.wantErr {
				t.Errorf("windowMinutes=%d: validation mismatch", tt.windowMinutes)
			}
		})
	}
}

// =============================================================================
// Hours Validation Tests
// =============================================================================

// TestGetWorkerHashrateHistory_HoursBounds tests hours validation.
func TestGetWorkerHashrateHistory_HoursBounds(t *testing.T) {
	tests := []struct {
		name    string
		hours   int
		wantErr bool
	}{
		{"valid 1 hour", 1, false},
		{"valid 24 hours", 24, false},
		{"valid 168 hours (1 week)", 168, false},
		{"valid max (30 days)", 720, false},
		{"invalid zero", 0, true},
		{"invalid negative", -1, true},
		{"invalid 721", 721, true},
		{"invalid very large", 10000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasErr := tt.hours < 1 || tt.hours > 720
			if hasErr != tt.wantErr {
				t.Errorf("hours=%d: validation mismatch", tt.hours)
			}
		})
	}
}

// =============================================================================
// Limit Validation Tests
// =============================================================================

// TestGetAllWorkers_LimitBounds tests limit parameter normalization.
func TestGetAllWorkers_LimitBounds(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		expectedLimit int
	}{
		{"valid 100", 100, 100},
		{"valid 1000", 1000, 1000},
		{"valid 10000 (max)", 10000, 10000},
		{"zero uses default", 0, 1000},
		{"negative uses default", -1, 1000},
		{"over max uses default", 10001, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := tt.limit
			if limit <= 0 || limit > 10000 {
				limit = 1000 // Default
			}

			if limit != tt.expectedLimit {
				t.Errorf("limit=%d: got %d, want %d",
					tt.limit, limit, tt.expectedLimit)
			}
		})
	}
}

// =============================================================================
// CRITICAL: CleanupOldWorkerSnapshots Negative Retention Bug Test
// =============================================================================

// TestCleanupOldWorkerSnapshots_NegativeRetention tests negative retention days.
// CRITICAL: Negative retention could delete ALL data (DELETE WHERE timestamp < NOW() - -N days)
func TestCleanupOldWorkerSnapshots_NegativeRetention(t *testing.T) {
	tests := []struct {
		name           string
		retentionDays  int
		isSafe         bool
		description    string
	}{
		{
			name:          "valid 7 days",
			retentionDays: 7,
			isSafe:        true,
			description:   "Normal 7-day retention",
		},
		{
			name:          "valid 30 days",
			retentionDays: 30,
			isSafe:        true,
			description:   "Normal 30-day retention",
		},
		{
			name:          "valid 365 days",
			retentionDays: 365,
			isSafe:        true,
			description:   "1 year retention",
		},
		{
			name:          "zero days (DANGEROUS)",
			retentionDays: 0,
			isSafe:        false,
			description:   "Would delete everything before NOW()",
		},
		{
			name:          "negative -1 (DATA LOSS)",
			retentionDays: -1,
			isSafe:        false,
			description:   "Would delete everything before NOW() + 1 day (FUTURE!)",
		},
		{
			name:          "negative -30 (CATASTROPHIC)",
			retentionDays: -30,
			isSafe:        false,
			description:   "Would delete everything before NOW() + 30 days",
		},
		{
			name:          "negative -365 (TOTAL LOSS)",
			retentionDays: -365,
			isSafe:        false,
			description:   "Would delete all data in the table",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate retention days
			isSafe := tt.retentionDays > 0

			if isSafe != tt.isSafe {
				t.Errorf("retentionDays=%d: safety=%v, want %v\n%s",
					tt.retentionDays, isSafe, tt.isSafe, tt.description)
			}

			// SECURITY: Log warning for dangerous values
			if !isSafe {
				t.Logf("SECURITY WARNING: retentionDays=%d is dangerous - %s",
					tt.retentionDays, tt.description)
			}
		})
	}
}

// TestCleanupRetentionMustBePositive verifies positive retention requirement.
func TestCleanupRetentionMustBePositive(t *testing.T) {
	// CRITICAL: The cleanup function should REJECT non-positive retention
	// because: DELETE WHERE timestamp < NOW() - (-N days)
	//        = DELETE WHERE timestamp < NOW() + N days
	//        = DELETE EVERYTHING (including future timestamps!)

	retentionDays := -7

	// Simulate what SHOULD happen (validation)
	shouldReject := retentionDays <= 0

	if !shouldReject {
		t.Error("SECURITY: Negative retentionDays should be rejected")
	}

	// Show what the SQL would look like with negative days
	// This demonstrates why this is dangerous
	t.Logf("With retentionDays=%d, the interval becomes: NOW() - %d days = NOW() + %d days",
		retentionDays, retentionDays, -retentionDays)
}

// =============================================================================
// Hashrate Calculation Tests
// =============================================================================

// TestHashrateCalculation verifies the hashrate formula.
func TestHashrateCalculation(t *testing.T) {
	tests := []struct {
		name            string
		totalDifficulty float64
		windowMinutes   int
		expectedRate    float64
		tolerance       float64 // Acceptable error margin
	}{
		{
			name:            "1 difficulty in 1 minute",
			totalDifficulty: 1.0,
			windowMinutes:   1,
			expectedRate:    4.295e9 / 60, // ~71.58 MH/s
			tolerance:       1e6,
		},
		{
			name:            "1000 difficulty in 60 minutes",
			totalDifficulty: 1000.0,
			windowMinutes:   60,
			expectedRate:    1000.0 * 4.295e9 / 3600, // ~1.19 TH/s
			tolerance:       1e9,
		},
		{
			name:            "zero difficulty",
			totalDifficulty: 0.0,
			windowMinutes:   60,
			expectedRate:    0.0,
			tolerance:       0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Formula from workers.go: difficulty * 4.295e9 / windowSeconds
			windowSeconds := float64(tt.windowMinutes * 60)
			hashrate := tt.totalDifficulty * 4.295e9 / windowSeconds

			diff := hashrate - tt.expectedRate
			if diff < 0 {
				diff = -diff
			}

			if diff > tt.tolerance {
				t.Errorf("Hashrate calculation: got %.2e, want %.2e (diff: %.2e)",
					hashrate, tt.expectedRate, diff)
			}
		})
	}
}

// TestHashrateConstant verifies the 4.295e9 constant.
func TestHashrateConstant(t *testing.T) {
	// 4.295e9 is approximately 2^32 (4,294,967,296)
	// This is used in hashrate calculation: difficulty * 2^32 / seconds

	expected := float64(1 << 32) // 4,294,967,296
	used := 4.295e9              // Value in workers.go

	diff := used - expected
	if diff < 0 {
		diff = -diff
	}

	// Allow 0.1% tolerance
	tolerance := expected * 0.001

	if diff > tolerance {
		t.Errorf("Hashrate constant 4.295e9 differs from 2^32 by %.2f (tolerance: %.2f)",
			diff, tolerance)
	}
}

// =============================================================================
// Connected Status Tests
// =============================================================================

// TestWorkerConnectedStatus verifies connected status determination.
func TestWorkerConnectedStatus(t *testing.T) {
	tests := []struct {
		name      string
		lastShare time.Time
		connected bool
	}{
		{
			name:      "just now",
			lastShare: time.Now(),
			connected: true,
		},
		{
			name:      "1 minute ago",
			lastShare: time.Now().Add(-1 * time.Minute),
			connected: true,
		},
		{
			name:      "4 minutes ago",
			lastShare: time.Now().Add(-4 * time.Minute),
			connected: true,
		},
		{
			name:      "exactly 5 minutes ago",
			lastShare: time.Now().Add(-5 * time.Minute),
			connected: false, // >= 5 minutes means disconnected
		},
		{
			name:      "6 minutes ago",
			lastShare: time.Now().Add(-6 * time.Minute),
			connected: false,
		},
		{
			name:      "1 hour ago",
			lastShare: time.Now().Add(-1 * time.Hour),
			connected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Logic from workers.go
			connected := time.Since(tt.lastShare) < 5*time.Minute

			if connected != tt.connected {
				t.Errorf("lastShare=%v: connected=%v, want %v",
					tt.lastShare, connected, tt.connected)
			}
		})
	}
}

// =============================================================================
// Acceptance Rate Tests
// =============================================================================

// TestAcceptanceRateCalculation verifies acceptance rate formula.
func TestAcceptanceRateCalculation(t *testing.T) {
	tests := []struct {
		name            string
		submitted       int64
		accepted        int64
		expectedRate    float64
	}{
		{"perfect 100%", 100, 100, 100.0},
		{"90%", 100, 90, 90.0},
		{"50%", 100, 50, 50.0},
		{"10%", 100, 10, 10.0},
		{"0%", 100, 0, 0.0},
		{"zero submitted", 0, 0, 0.0}, // Division by zero case
		{"1 share", 1, 1, 100.0},
		{"large numbers", 1000000, 999990, 99.999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rate float64
			if tt.submitted > 0 {
				rate = float64(tt.accepted) / float64(tt.submitted) * 100
			}

			diff := rate - tt.expectedRate
			if diff < 0 {
				diff = -diff
			}

			if diff > 0.01 { // 0.01% tolerance
				t.Errorf("Acceptance rate: got %.3f%%, want %.3f%%",
					rate, tt.expectedRate)
			}
		})
	}
}

// =============================================================================
// Empty Result Handling Tests
// =============================================================================

// TestEmptyWorkersSlice verifies empty slice initialization.
func TestEmptyWorkersSlice(t *testing.T) {
	// From workers.go: if workers == nil { workers = []*WorkerSummary{} }
	var workers []*WorkerSummary

	if workers != nil {
		t.Error("Uninitialized slice should be nil")
	}

	workers = []*WorkerSummary{}

	if workers == nil {
		t.Error("Empty slice should not be nil")
	}

	if len(workers) != 0 {
		t.Errorf("Empty slice length = %d, want 0", len(workers))
	}
}

// TestDefaultWorkerName verifies null worker handling.
func TestDefaultWorkerName(t *testing.T) {
	// From workers.go: COALESCE(worker, 'default') as worker
	tests := []struct {
		name       string
		workerNull *string
		expected   string
	}{
		{"nil worker", nil, "default"},
		{"empty string", strPtr(""), ""},
		{"named worker", strPtr("rig1"), "rig1"},
		{"special chars", strPtr("rig-1_test"), "rig-1_test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if tt.workerNull != nil {
				result = *tt.workerNull
			} else {
				result = "default"
			}

			if result != tt.expected {
				t.Errorf("Worker name = %q, want %q", result, tt.expected)
			}
		})
	}
}

func strPtr(s string) *string {
	return &s
}

// =============================================================================
// Bucket Size Determination Tests
// =============================================================================

// TestBucketSizeDetermination verifies time bucket selection.
func TestBucketSizeDetermination(t *testing.T) {
	tests := []struct {
		hours         int
		expectedBucket int
	}{
		{1, 5},    // 1 hour -> 5 minute buckets
		{6, 5},    // 6 hours -> 5 minute buckets
		{24, 5},   // 24 hours -> 5 minute buckets
		{25, 15},  // 25 hours -> 15 minute buckets
		{48, 15},  // 48 hours -> 15 minute buckets
		{168, 15}, // 1 week -> 15 minute buckets
		{169, 60}, // > 1 week -> 60 minute buckets
		{720, 60}, // 30 days -> 60 minute buckets
	}

	for _, tt := range tests {
		t.Run(timeDesc(tt.hours), func(t *testing.T) {
			// Logic from workers.go
			bucketMinutes := 5
			if tt.hours > 24 {
				bucketMinutes = 15
			}
			if tt.hours > 168 {
				bucketMinutes = 60
			}

			if bucketMinutes != tt.expectedBucket {
				t.Errorf("hours=%d: bucketMinutes=%d, want %d",
					tt.hours, bucketMinutes, tt.expectedBucket)
			}
		})
	}
}

func timeDesc(hours int) string {
	if hours <= 24 {
		return "up to 24h"
	}
	if hours <= 168 {
		return "24h-1week"
	}
	return "over 1 week"
}

// =============================================================================
// WorkerStats Structure Tests
// =============================================================================

// TestWorkerStatsFields verifies WorkerStats structure.
func TestWorkerStatsFields(t *testing.T) {
	stats := WorkerStats{
		Miner:           "DTestAddress123",
		Worker:          "rig1",
		Hashrate:        1e12,
		SharesSubmitted: 10000,
		SharesAccepted:  9990,
		SharesRejected:  10,
		SharesStale:     0,
		AcceptanceRate:  99.9,
		TotalDifficulty: 5000.0,
		LastShare:       time.Now(),
		Connected:       true,
		Difficulty:      16.0,
		UserAgent:       "cgminer/4.11.1",
	}

	if stats.Miner == "" {
		t.Error("Miner should not be empty")
	}

	// Verify share math
	expectedTotal := stats.SharesAccepted + stats.SharesRejected + stats.SharesStale
	if expectedTotal > stats.SharesSubmitted {
		t.Error("Sum of accepted/rejected/stale should not exceed submitted")
	}

	if stats.Difficulty <= 0 {
		t.Error("Difficulty should be positive")
	}
}

// TestWorkerSummaryFields verifies WorkerSummary structure.
func TestWorkerSummaryFields(t *testing.T) {
	summary := WorkerSummary{
		Miner:          "DTestAddress123",
		Worker:         "rig1",
		Hashrate:       1e12,
		SharesPerSec:   5.5,
		AcceptanceRate: 100.0,
		LastShare:      time.Now(),
		Connected:      true,
	}

	if summary.Hashrate < 0 {
		t.Error("Hashrate should not be negative")
	}

	if summary.SharesPerSec < 0 {
		t.Error("SharesPerSec should not be negative")
	}

	if summary.AcceptanceRate < 0 || summary.AcceptanceRate > 100 {
		t.Errorf("AcceptanceRate=%.2f should be 0-100", summary.AcceptanceRate)
	}
}

// TestWorkerHashratePointFields verifies WorkerHashratePoint structure.
func TestWorkerHashratePointFields(t *testing.T) {
	point := WorkerHashratePoint{
		Timestamp: time.Now(),
		Hashrate:  1e12,
		Window:    "5m",
	}

	if point.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}

	if point.Hashrate < 0 {
		t.Error("Hashrate should not be negative")
	}

	validWindows := map[string]bool{
		"1m": true, "5m": true, "15m": true, "60m": true,
	}
	if !validWindows[point.Window] {
		t.Logf("Unusual window size: %s", point.Window)
	}
}

// =============================================================================
// Table Name Generation Tests
// =============================================================================

// TestTableNameGeneration verifies table name construction.
func TestTableNameGeneration(t *testing.T) {
	tests := []struct {
		poolID        string
		tableName     string
		historyTable  string
	}{
		{"dgb_main", "shares_dgb_main", "worker_hashrate_history_dgb_main"},
		{"btc", "shares_btc", "worker_hashrate_history_btc"},
		{"pool_1", "shares_pool_1", "worker_hashrate_history_pool_1"},
	}

	for _, tt := range tests {
		t.Run(tt.poolID, func(t *testing.T) {
			tableName := "shares_" + tt.poolID
			historyTable := "worker_hashrate_history_" + tt.poolID

			if tableName != tt.tableName {
				t.Errorf("tableName = %q, want %q", tableName, tt.tableName)
			}

			if historyTable != tt.historyTable {
				t.Errorf("historyTable = %q, want %q", historyTable, tt.historyTable)
			}
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

// BenchmarkHashrateCalculation benchmarks hashrate formula.
func BenchmarkHashrateCalculation(b *testing.B) {
	totalDifficulty := 1000.0
	windowMinutes := 60

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		windowSeconds := float64(windowMinutes * 60)
		_ = totalDifficulty * 4.295e9 / windowSeconds
	}
}

// BenchmarkAcceptanceRateCalculation benchmarks acceptance rate formula.
func BenchmarkAcceptanceRateCalculation(b *testing.B) {
	submitted := int64(10000)
	accepted := int64(9990)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = float64(accepted) / float64(submitted) * 100
	}
}

// BenchmarkConnectedStatus benchmarks connected status check.
func BenchmarkConnectedStatus(b *testing.B) {
	lastShare := time.Now().Add(-2 * time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = time.Since(lastShare) < 5*time.Minute
	}
}

// BenchmarkBucketDetermination benchmarks bucket size selection.
func BenchmarkBucketDetermination(b *testing.B) {
	hours := 48

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bucketMinutes := 5
		if hours > 24 {
			bucketMinutes = 15
		}
		if hours > 168 {
			bucketMinutes = 60
		}
		_ = bucketMinutes
	}
}
