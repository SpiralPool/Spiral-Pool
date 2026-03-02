// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package workers - Tests for worker statistics collection.
//
// These tests verify:
// - Worker name validation (security)
// - Hashrate calculations
// - Session tracking
// - Share recording
// - Rolling window behavior
package workers

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestValidateWorkerName verifies worker name validation security.
func TestValidateWorkerName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantValid bool
	}{
		// Valid names
		{name: "simple name", input: "worker1", wantValid: true},
		{name: "with underscore", input: "my_worker", wantValid: true},
		{name: "with dash", input: "my-worker", wantValid: true},
		{name: "with dot", input: "worker.1", wantValid: true},
		{name: "mixed case", input: "WorkerName", wantValid: true},
		{name: "max length 64", input: "worker_123456789012345678901234567890123456789012345678901234567", wantValid: true},
		{name: "empty string", input: "", wantValid: true}, // Empty is allowed (becomes "default")

		// Invalid names - security boundary tests
		{name: "too long 65 chars", input: "a1234567890123456789012345678901234567890123456789012345678901234", wantValid: false},
		{name: "SQL injection attempt", input: "worker'; DROP TABLE--", wantValid: false},
		{name: "path traversal", input: "../../../etc/passwd", wantValid: false},
		{name: "null byte", input: "worker\x00name", wantValid: false},
		{name: "newline injection", input: "worker\nname", wantValid: false},
		{name: "tab injection", input: "worker\tname", wantValid: false},
		{name: "carriage return", input: "worker\rname", wantValid: false},
		{name: "unicode attack", input: "worker\u202Ename", wantValid: false},
		{name: "space in name", input: "worker name", wantValid: false},
		{name: "special chars", input: "worker@#$%", wantValid: false},
		{name: "angle brackets XSS", input: "<script>alert(1)</script>", wantValid: false},
		{name: "curly braces", input: "worker{name}", wantValid: false},
		{name: "backtick", input: "worker`cmd`", wantValid: false},
		{name: "dollar sign", input: "worker$var", wantValid: false},
		{name: "semicolon", input: "worker;cmd", wantValid: false},
		{name: "pipe", input: "worker|cmd", wantValid: false},
		{name: "ampersand", input: "worker&cmd", wantValid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateWorkerName(tt.input)
			if got != tt.wantValid {
				t.Errorf("ValidateWorkerName(%q) = %v, want %v", tt.input, got, tt.wantValid)
			}
		})
	}
}

// TestNormalizeWorkerName verifies safe normalization.
func TestNormalizeWorkerName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty becomes default", input: "", want: "default"},
		{name: "valid unchanged", input: "worker1", want: "worker1"},
		{name: "invalid becomes invalid", input: "worker'; DROP TABLE--", want: "invalid"},
		{name: "path traversal sanitized", input: "../etc/passwd", want: "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeWorkerName(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeWorkerName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestTimeWindowString verifies window string conversion.
func TestTimeWindowString(t *testing.T) {
	tests := []struct {
		window TimeWindow
		want   string
	}{
		{Window1m, "1m"},
		{Window5m, "5m"},
		{Window15m, "15m"},
		{Window1h, "1h"},
		{Window24h, "24h"},
		{TimeWindow(30), "30m"}, // Custom window
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.window.String()
			if got != tt.want {
				t.Errorf("TimeWindow(%d).String() = %q, want %q", tt.window, got, tt.want)
			}
		})
	}
}

// TestAllWindows verifies all windows are returned.
func TestAllWindows(t *testing.T) {
	windows := AllWindows()

	if len(windows) != 5 {
		t.Errorf("AllWindows() returned %d windows, want 5", len(windows))
	}

	expected := []TimeWindow{Window1m, Window5m, Window15m, Window1h, Window24h}
	for i, w := range expected {
		if windows[i] != w {
			t.Errorf("AllWindows()[%d] = %v, want %v", i, windows[i], w)
		}
	}
}

// mockStatsDB implements StatsDatabase for testing.
type mockStatsDB struct {
	mu        sync.Mutex
	snapshots []*WorkerStats
	cleanups  int
}

func (m *mockStatsDB) GetWorkerStats(ctx context.Context, miner, worker string, windowMinutes int) (*WorkerStats, error) {
	return &WorkerStats{
		Miner:  miner,
		Worker: worker,
	}, nil
}

func (m *mockStatsDB) GetMinerWorkers(ctx context.Context, miner string, windowMinutes int) ([]*WorkerSummary, error) {
	return []*WorkerSummary{}, nil
}

func (m *mockStatsDB) GetAllWorkers(ctx context.Context, windowMinutes int, limit int) ([]*WorkerSummary, error) {
	return []*WorkerSummary{}, nil
}

func (m *mockStatsDB) GetWorkerHashrateHistory(ctx context.Context, miner, worker string, hours int) ([]*WorkerHashratePoint, error) {
	return []*WorkerHashratePoint{}, nil
}

func (m *mockStatsDB) RecordWorkerSnapshot(ctx context.Context, stats *WorkerStats) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots = append(m.snapshots, stats)
	return nil
}

func (m *mockStatsDB) CleanupOldSnapshots(ctx context.Context, retentionDays int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanups++
	return nil
}

// getSnapshots returns recorded snapshots for test assertions.
//
//lint:ignore U1000 Reserved for future test assertions
func (m *mockStatsDB) getSnapshots() []*WorkerStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshots
}

// TestStatsCollectorSessionTracking tests session registration and unregistration.
func TestStatsCollectorSessionTracking(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := &StatsConfig{
		Enabled:            true,
		CollectionInterval: 1 * time.Minute,
		RetentionDays:      30,
		MaxWorkersPerMiner: 1000,
		MaxTotalWorkers:    100000,
	}

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register a session
	sc.RegisterSession(1, "miner1", "worker1", "test-agent", "127.0.0.1", 8.0)

	count := sc.GetActiveWorkerCount()
	if count != 1 {
		t.Errorf("GetActiveWorkerCount() = %d, want 1", count)
	}

	// Register another session for same worker
	sc.RegisterSession(2, "miner1", "worker1", "test-agent", "127.0.0.1", 8.0)

	count = sc.GetActiveWorkerCount()
	if count != 2 {
		t.Errorf("GetActiveWorkerCount() = %d, want 2", count)
	}

	// Unregister one session - worker should still be connected
	sc.UnregisterSession(1)

	count = sc.GetActiveWorkerCount()
	if count != 1 {
		t.Errorf("GetActiveWorkerCount() = %d after unregister, want 1", count)
	}

	// Unregister last session
	sc.UnregisterSession(2)

	count = sc.GetActiveWorkerCount()
	if count != 0 {
		t.Errorf("GetActiveWorkerCount() = %d after full unregister, want 0", count)
	}
}

// TestStatsCollectorShareRecording tests share recording.
func TestStatsCollectorShareRecording(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register a session
	sc.RegisterSession(1, "miner1", "worker1", "test-agent", "127.0.0.1", 8.0)

	// Record some shares
	sc.RecordShare(1, true, false, 8.0)  // Accepted
	sc.RecordShare(1, true, false, 8.0)  // Accepted
	sc.RecordShare(1, false, false, 8.0) // Rejected
	sc.RecordShare(1, false, true, 8.0)  // Stale

	// Get stats from cache
	ctx := context.Background()
	stats, err := sc.GetWorkerStats(ctx, "miner1", "worker1")
	if err != nil {
		t.Fatalf("GetWorkerStats() error: %v", err)
	}

	if stats.SharesSubmitted != 4 {
		t.Errorf("SharesSubmitted = %d, want 4", stats.SharesSubmitted)
	}
	if stats.SharesAccepted != 2 {
		t.Errorf("SharesAccepted = %d, want 2", stats.SharesAccepted)
	}
	if stats.SharesRejected != 2 {
		t.Errorf("SharesRejected = %d, want 2", stats.SharesRejected)
	}
	if stats.SharesStale != 1 {
		t.Errorf("SharesStale = %d, want 1", stats.SharesStale)
	}

	// Acceptance rate should be 50%
	if stats.AcceptanceRate != 50.0 {
		t.Errorf("AcceptanceRate = %f, want 50.0", stats.AcceptanceRate)
	}
}

// TestStatsCollectorDifficultyUpdate tests difficulty update.
func TestStatsCollectorDifficultyUpdate(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register a session with initial difficulty
	sc.RegisterSession(1, "miner1", "worker1", "test-agent", "127.0.0.1", 8.0)

	// Update difficulty
	sc.UpdateDifficulty(1, 16.0)

	// Verify difficulty is updated (internal check via session)
	val, ok := sc.sessions.Load(uint64(1))
	if !ok {
		t.Fatal("Session not found")
	}
	session := val.(*workerSession)
	if session.Difficulty != 16.0 {
		t.Errorf("Difficulty = %f, want 16.0", session.Difficulty)
	}
}

// TestStatsCollectorWorkerNameNormalization tests that invalid worker names are normalized.
func TestStatsCollectorWorkerNameNormalization(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register session with malicious worker name
	sc.RegisterSession(1, "miner1", "'; DROP TABLE--", "test-agent", "127.0.0.1", 8.0)

	// Worker name should be normalized to "invalid"
	val, ok := sc.sessions.Load(uint64(1))
	if !ok {
		t.Fatal("Session not found")
	}
	session := val.(*workerSession)
	if session.Worker != "invalid" {
		t.Errorf("Worker name = %q, want %q", session.Worker, "invalid")
	}
}

// TestStatsCollectorEmptyWorkerName tests empty worker name handling.
func TestStatsCollectorEmptyWorkerName(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register session with empty worker name
	sc.RegisterSession(1, "miner1", "", "test-agent", "127.0.0.1", 8.0)

	// Worker name should be normalized to "default"
	val, ok := sc.sessions.Load(uint64(1))
	if !ok {
		t.Fatal("Session not found")
	}
	session := val.(*workerSession)
	if session.Worker != "default" {
		t.Errorf("Worker name = %q, want %q", session.Worker, "default")
	}
}

// TestWorkerKeyGeneration tests the worker key generation.
func TestWorkerKeyGeneration(t *testing.T) {
	tests := []struct {
		miner  string
		worker string
		want   string
	}{
		{"miner1", "worker1", "miner1:worker1"},
		{"D123abc", "rig1", "D123abc:rig1"},
		{"", "worker", ":worker"},
		{"miner", "", "miner:"},
	}

	for _, tt := range tests {
		t.Run(tt.miner+":"+tt.worker, func(t *testing.T) {
			got := workerKey(tt.miner, tt.worker)
			if got != tt.want {
				t.Errorf("workerKey(%q, %q) = %q, want %q", tt.miner, tt.worker, got, tt.want)
			}
		})
	}
}

// TestStatsCollectorConcurrency tests concurrent access to stats collector.
func TestStatsCollectorConcurrency(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Run concurrent operations
	var wg sync.WaitGroup
	workers := 10
	sharesPerWorker := 100

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			sessionID := uint64(workerID)
			workerName := fmt.Sprintf("worker%d", workerID)

			// Register session
			sc.RegisterSession(sessionID, "miner1", workerName, "test-agent", "127.0.0.1", 8.0)

			// Record shares concurrently
			for j := 0; j < sharesPerWorker; j++ {
				sc.RecordShare(sessionID, true, false, 8.0)
			}

			// Unregister session
			sc.UnregisterSession(sessionID)
		}(i)
	}

	wg.Wait()

	// All sessions should be unregistered
	count := sc.GetActiveWorkerCount()
	if count != 0 {
		t.Errorf("GetActiveWorkerCount() = %d after concurrent test, want 0", count)
	}
}

// TestStatsCollectorNilConfig tests that nil config uses defaults.
func TestStatsCollectorNilConfig(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()

	sc := NewStatsCollector(db, nil, "testpool", "sha256d", logger)

	if sc.cfg == nil {
		t.Fatal("Config should not be nil")
	}
	if sc.cfg.CollectionInterval != 1*time.Minute {
		t.Errorf("Default CollectionInterval = %v, want 1m", sc.cfg.CollectionInterval)
	}
	if sc.cfg.RetentionDays != 30 {
		t.Errorf("Default RetentionDays = %d, want 30", sc.cfg.RetentionDays)
	}
}

// TestDefaultStatsConfig verifies default config values.
func TestDefaultStatsConfig(t *testing.T) {
	cfg := DefaultStatsConfig()

	if !cfg.Enabled {
		t.Error("Default Enabled = false, want true")
	}
	if cfg.CollectionInterval != 1*time.Minute {
		t.Errorf("Default CollectionInterval = %v, want 1m", cfg.CollectionInterval)
	}
	if cfg.RetentionDays != 30 {
		t.Errorf("Default RetentionDays = %d, want 30", cfg.RetentionDays)
	}
	if cfg.MaxWorkersPerMiner != 1000 {
		t.Errorf("Default MaxWorkersPerMiner = %d, want 1000", cfg.MaxWorkersPerMiner)
	}
	if cfg.MaxTotalWorkers != 100000 {
		t.Errorf("Default MaxTotalWorkers = %d, want 100000", cfg.MaxTotalWorkers)
	}
}

// TestRecordShareNonExistentSession tests recording shares for non-existent session.
func TestRecordShareNonExistentSession(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// This should not panic - just be a no-op
	sc.RecordShare(999, true, false, 8.0)

	// No sessions should exist
	count := sc.GetActiveWorkerCount()
	if count != 0 {
		t.Errorf("GetActiveWorkerCount() = %d, want 0", count)
	}
}

// TestUpdateDifficultyNonExistentSession tests updating difficulty for non-existent session.
func TestUpdateDifficultyNonExistentSession(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// This should not panic - just be a no-op
	sc.UpdateDifficulty(999, 16.0)
}

// TestUnregisterNonExistentSession tests unregistering non-existent session.
func TestUnregisterNonExistentSession(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()

	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// This should not panic - just be a no-op
	sc.UnregisterSession(999)
}

// BenchmarkValidateWorkerName benchmarks worker name validation.
func BenchmarkValidateWorkerName(b *testing.B) {
	names := []string{
		"worker1",
		"my_worker_name",
		"worker-with-dashes",
		"worker.with.dots",
		"'; DROP TABLE--", // Invalid
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, name := range names {
			ValidateWorkerName(name)
		}
	}
}

// BenchmarkRecordShare benchmarks share recording.
func BenchmarkRecordShare(b *testing.B) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	sc.RegisterSession(1, "miner1", "worker1", "test-agent", "127.0.0.1", 8.0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc.RecordShare(1, true, false, 8.0)
	}
}

// BenchmarkConcurrentShareRecording benchmarks concurrent share recording.
func BenchmarkConcurrentShareRecording(b *testing.B) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register 10 workers
	for i := uint64(0); i < 10; i++ {
		sc.RegisterSession(i, "miner1", fmt.Sprintf("worker%d", i), "test-agent", "127.0.0.1", 8.0)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		sessionID := uint64(0)
		for pb.Next() {
			sc.RecordShare(sessionID%10, true, false, 8.0)
			sessionID++
		}
	})
}
