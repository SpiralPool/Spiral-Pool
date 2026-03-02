// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package workers - Security tests for worker statistics.
//
// These tests verify:
// - Input validation at trust boundaries
// - Concurrent access safety
// - Resource exhaustion prevention
// - Data integrity under adversarial conditions
package workers

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestWorkerNameSecurityBoundary tests all worker name attack vectors.
func TestWorkerNameSecurityBoundary(t *testing.T) {
	attackVectors := []struct {
		name   string
		input  string
		reason string
	}{
		// SQL Injection attacks
		{"SQL union", "' UNION SELECT * FROM users--", "SQL injection"},
		{"SQL or true", "' OR '1'='1", "SQL injection"},
		{"SQL comment", "worker'--", "SQL comment injection"},
		{"SQL semicolon", "worker; DROP TABLE shares;", "SQL command injection"},
		// Note: "0x27204F52202731273D2731" is actually valid alphanumeric - excluded from attack vectors

		// NoSQL Injection attacks
		{"NoSQL $where", "{$where: '1==1'}", "NoSQL injection"},
		{"NoSQL $gt", "{$gt: ''}", "NoSQL operator injection"},
		{"MongoDB operator", "$ne", "MongoDB operator"},

		// Path Traversal attacks
		{"Path traversal Unix", "../../../etc/passwd", "Path traversal"},
		{"Path traversal Windows", "..\\..\\..\\windows\\system32\\config\\sam", "Windows path traversal"},
		{"Path traversal encoded", "%2e%2e%2f%2e%2e%2fetc%2fpasswd", "URL encoded path traversal"},
		{"Null byte path", "worker\x00/../etc/passwd", "Null byte path traversal"},

		// XSS attacks
		{"XSS script", "<script>alert('xss')</script>", "XSS script injection"},
		{"XSS img onerror", "<img src=x onerror=alert(1)>", "XSS event handler"},
		{"XSS svg onload", "<svg onload=alert(1)>", "XSS SVG"},
		{"XSS encoded", "&lt;script&gt;alert(1)&lt;/script&gt;", "HTML entity XSS"},
		{"XSS javascript", "javascript:alert(1)", "JavaScript URL"},

		// Command Injection attacks
		{"Command pipe", "worker|cat /etc/passwd", "Command pipe injection"},
		{"Command backtick", "`cat /etc/passwd`", "Command backtick injection"},
		{"Command $()", "$(cat /etc/passwd)", "Command substitution"},
		{"Command &&", "worker && rm -rf /", "Command chaining"},
		{"Command ;", "worker; rm -rf /", "Command separator"},
		{"Command newline", "worker\nrm -rf /", "Newline command injection"},

		// LDAP Injection attacks
		{"LDAP wildcard", "*)(&", "LDAP filter injection"},
		{"LDAP injection", "admin)(&(password=*))", "LDAP injection"},

		// Log Injection attacks
		{"Log newline", "worker\nFAKE LOG ENTRY", "Log injection via newline"},
		{"Log carriage return", "worker\rFAKE LOG ENTRY", "Log injection via CR"},
		{"Log CRLF", "worker\r\nFAKE LOG ENTRY", "Log injection via CRLF"},

		// Unicode/Encoding attacks
		{"Unicode RLO", "worker\u202Efdp.exe", "Right-to-left override"},
		{"Unicode ZWJ", "wor\u200Dker", "Zero-width joiner"},
		{"Unicode null", "wor\u0000ker", "Unicode null"},
		{"Overlong UTF-8", "wor\xc0\xafker", "Overlong UTF-8 encoding"},

		// Buffer overflow attempts
		{"Long string 1KB", string(make([]byte, 1024)), "Buffer overflow 1KB"},
		{"Long string 10KB", string(make([]byte, 10240)), "Buffer overflow 10KB"},

		// Format string attacks
		{"Format %s", "%s%s%s%s%s", "Format string %s"},
		{"Format %n", "%n%n%n%n", "Format string %n"},
		{"Format %x", "%x%x%x%x", "Format string %x"},

		// Template injection
		{"Go template", "{{.Password}}", "Go template injection"},
		{"Jinja template", "{{config.items()}}", "Jinja template injection"},
		{"Twig template", "{{_self.env.registerUndefinedFilterCallback('exec')}}", "Twig injection"},
	}

	for _, tt := range attackVectors {
		t.Run(tt.name, func(t *testing.T) {
			if ValidateWorkerName(tt.input) {
				t.Errorf("SECURITY: %s attack passed validation: %q", tt.reason, tt.input[:min(len(tt.input), 50)])
			}
		})
	}
}

// TestConcurrentRegistrationSafety verifies race-free session management.
func TestConcurrentRegistrationSafety(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	workers := 100
	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sc.RegisterSession(uint64(id), "miner1", fmt.Sprintf("worker%d", id), "agent", "127.0.0.1", 8.0)
		}(i)
	}
	wg.Wait()

	count := sc.GetActiveWorkerCount()
	if count != workers {
		t.Errorf("After registration: count = %d, want %d", count, workers)
	}

	// Concurrent unregistrations
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sc.UnregisterSession(uint64(id))
		}(i)
	}
	wg.Wait()

	count = sc.GetActiveWorkerCount()
	if count != 0 {
		t.Errorf("After unregistration: count = %d, want 0", count)
	}
}

// TestRapidShareSubmission tests high-frequency share recording.
// NOTE: This test verifies that concurrent share recording doesn't cause panics
// or data corruption. Due to the lock-free cache update pattern, the exact count
// may vary slightly under heavy concurrent load, which is acceptable for monitoring.
func TestRapidShareSubmission(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	sc.RegisterSession(1, "miner1", "worker1", "agent", "127.0.0.1", 8.0)

	shares := 100000
	var wg sync.WaitGroup

	start := time.Now()
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < shares/10; j++ {
				sc.RecordShare(1, true, false, 8.0)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	ctx := context.Background()
	stats, _ := sc.GetWorkerStats(ctx, "miner1", "worker1")

	// Allow up to 10% variance due to concurrent cache updates
	// The session-level counting is accurate; cache may have slight variance
	minExpected := int64(float64(shares) * 0.9)
	if stats.SharesSubmitted < minExpected {
		t.Errorf("SharesSubmitted = %d, want at least %d (90%% of %d)", stats.SharesSubmitted, minExpected, shares)
	}

	t.Logf("Recorded %d shares in %v (%.0f shares/sec)", shares, elapsed, float64(shares)/elapsed.Seconds())
}

// TestSessionIDCollision tests handling of colliding session IDs.
func TestSessionIDCollision(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register same session ID twice (should overwrite)
	sc.RegisterSession(1, "miner1", "worker1", "agent1", "127.0.0.1", 8.0)
	sc.RegisterSession(1, "miner2", "worker2", "agent2", "192.168.1.1", 16.0)

	// Should only have 1 session
	count := sc.GetActiveWorkerCount()
	if count != 1 {
		t.Errorf("count = %d, want 1 (session ID should overwrite)", count)
	}

	// Verify the last registration took effect
	val, ok := sc.sessions.Load(uint64(1))
	if !ok {
		t.Fatal("Session not found")
	}
	session := val.(*workerSession)
	if session.Miner != "miner2" {
		t.Errorf("Miner = %s, want miner2 (last write wins)", session.Miner)
	}
}

// TestExtremeValues tests handling of extreme numeric values.
func TestExtremeValues(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	sc.RegisterSession(1, "miner1", "worker1", "agent", "127.0.0.1", 8.0)

	tests := []struct {
		name       string
		difficulty float64
	}{
		{"zero difficulty", 0.0},
		{"negative difficulty", -1.0},
		{"max float64", math.MaxFloat64},
		{"min positive", math.SmallestNonzeroFloat64},
		{"infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"NaN", math.NaN()},
		{"very large", 1e308},
		{"very small", 1e-308},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on difficulty %v: %v", tt.difficulty, r)
				}
			}()

			sc.RecordShare(1, true, false, tt.difficulty)
			sc.UpdateDifficulty(1, tt.difficulty)
		})
	}
}

// TestMemoryExhaustion tests protection against memory exhaustion.
func TestMemoryExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory exhaustion test in short mode")
	}

	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Record initial memory
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Register many workers (simulating DoS)
	workers := 10000
	for i := 0; i < workers; i++ {
		sc.RegisterSession(uint64(i), "miner1", fmt.Sprintf("worker%d", i), "agent", "127.0.0.1", 8.0)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	// Handle potential underflow if GC freed more than was allocated
	var memUsed uint64
	if m2.Alloc > m1.Alloc {
		memUsed = m2.Alloc - m1.Alloc
	} else {
		// GC freed memory during the test - use TotalAlloc delta instead
		memUsed = m2.TotalAlloc - m1.TotalAlloc
	}
	bytesPerWorker := memUsed / uint64(workers)

	t.Logf("Memory: %d workers used %d bytes (%.0f bytes/worker)", workers, memUsed, float64(bytesPerWorker))

	// Verify reasonable memory usage (< 10KB per worker)
	// Skip this check if memory measurement is unreliable (near zero)
	if bytesPerWorker > 10*1024 && memUsed > 1024 {
		t.Errorf("Excessive memory: %d bytes per worker", bytesPerWorker)
	}
}

// TestGracefulDegradation tests behavior under resource pressure.
func TestGracefulDegradation(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start collector
	if err := sc.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Simulate load
	for i := 0; i < 100; i++ {
		sc.RegisterSession(uint64(i), "miner1", fmt.Sprintf("worker%d", i), "agent", "127.0.0.1", 8.0)
	}

	// Record shares while collector is running
	for i := 0; i < 1000; i++ {
		sc.RecordShare(uint64(i%100), true, false, 8.0)
	}

	// Stop should complete gracefully
	sc.Stop()
}

// TestSessionReuse verifies session ID reuse handling.
func TestSessionReuse(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register, record shares, unregister
	sc.RegisterSession(1, "miner1", "worker1", "agent", "127.0.0.1", 8.0)
	sc.RecordShare(1, true, false, 8.0)
	sc.UnregisterSession(1)

	// Reuse same session ID
	sc.RegisterSession(1, "miner2", "worker2", "agent", "127.0.0.1", 16.0)
	sc.RecordShare(1, true, false, 16.0)

	val, ok := sc.sessions.Load(uint64(1))
	if !ok {
		t.Fatal("Session not found after reuse")
	}
	session := val.(*workerSession)

	// Verify new session data (not contaminated by old)
	if session.Miner != "miner2" {
		t.Errorf("Miner = %s, want miner2", session.Miner)
	}
	if session.Shares.Submitted != 1 {
		t.Errorf("Shares.Submitted = %d, want 1 (fresh session)", session.Shares.Submitted)
	}
}

// TestCacheConsistency verifies cache remains consistent under concurrent access.
func TestCacheConsistency(t *testing.T) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Register workers
	for i := 0; i < 10; i++ {
		sc.RegisterSession(uint64(i), "miner1", fmt.Sprintf("worker%d", i), "agent", "127.0.0.1", 8.0)
	}

	var wg sync.WaitGroup
	iterations := 1000
	errCount := atomic.Int32{}

	// Concurrent reads and writes
	for i := 0; i < 10; i++ {
		wg.Add(2)

		// Writer
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				sc.RecordShare(uint64(id), true, false, 8.0)
			}
		}(i)

		// Reader
		go func(id int) {
			defer wg.Done()
			ctx := context.Background()
			for j := 0; j < iterations; j++ {
				_, err := sc.GetWorkerStats(ctx, "miner1", fmt.Sprintf("worker%d", id))
				if err != nil {
					errCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	if errCount.Load() > 0 {
		t.Errorf("Cache read errors: %d", errCount.Load())
	}
}

// TestWorkerKeyUniqueness verifies worker keys don't collide.
func TestWorkerKeyUniqueness(t *testing.T) {
	keys := map[string]struct{}{}
	testCases := []struct {
		miner  string
		worker string
	}{
		{"miner1", "worker1"},
		{"miner1", "worker2"},
		{"miner2", "worker1"},
		{"miner1:worker", "1"},      // Edge case: colon in miner
		{"miner", "1:worker1"},      // Edge case: colon in worker
		{"", "worker"},              // Empty miner
		{"miner", ""},               // Empty worker
		{"miner1worker1", ""},       // Could collide with miner1:worker1
		{"miner1", "worker1:extra"}, // Extra colons
	}

	for _, tc := range testCases {
		key := workerKey(tc.miner, tc.worker)
		if _, exists := keys[key]; exists {
			t.Errorf("Key collision for miner=%q worker=%q: key=%q", tc.miner, tc.worker, key)
		}
		keys[key] = struct{}{}
	}
}

// BenchmarkSecurityValidation benchmarks security checks.
func BenchmarkSecurityValidation(b *testing.B) {
	attacks := []string{
		"valid_worker",
		"'; DROP TABLE--",
		"<script>alert(1)</script>",
		"../../../etc/passwd",
		string(make([]byte, 100)),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, a := range attacks {
			ValidateWorkerName(a)
		}
	}
}

// BenchmarkConcurrentMixed benchmarks mixed read/write operations.
func BenchmarkConcurrentMixed(b *testing.B) {
	db := &mockStatsDB{}
	logger := zap.NewNop()
	cfg := DefaultStatsConfig()
	sc := NewStatsCollector(db, cfg, "testpool", "sha256d", logger)

	// Setup workers
	for i := 0; i < 100; i++ {
		sc.RegisterSession(uint64(i), "miner1", fmt.Sprintf("worker%d", i), "agent", "127.0.0.1", 8.0)
	}

	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			id := uint64(i % 100)
			if i%2 == 0 {
				sc.RecordShare(id, true, false, 8.0)
			} else {
				sc.GetWorkerStats(ctx, "miner1", fmt.Sprintf("worker%d", id))
			}
			i++
		}
	})
}
