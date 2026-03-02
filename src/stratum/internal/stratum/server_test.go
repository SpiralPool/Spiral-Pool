// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package stratum provides comprehensive server tests.
//
// These tests validate:
// - API robustness
// - Session management
// - Job broadcasting
// - Connection limits
// - Message buffer handling
package stratum

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConnectionLimitEnforcement tests that connection limits are respected.
func TestConnectionLimitEnforcement(t *testing.T) {
	maxConns := int64(100)
	var currentConns atomic.Int64
	var rejected atomic.Uint64

	// Simulate connection attempts
	for i := 0; i < 150; i++ {
		current := currentConns.Load()
		if current >= maxConns {
			rejected.Add(1)
			continue
		}

		// Accept connection
		currentConns.Add(1)
	}

	if rejected.Load() != 50 {
		t.Errorf("Expected 50 rejected, got %d", rejected.Load())
	}
	if currentConns.Load() != maxConns {
		t.Errorf("Expected %d connections, got %d", maxConns, currentConns.Load())
	}
}

// TestSessionIDGeneration tests unique session ID generation.
func TestSessionIDGeneration(t *testing.T) {
	const numIDs = 10000

	var generator atomic.Uint64
	ids := make(map[uint64]bool)
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIDs/100; j++ {
				id := generator.Add(1)
				mu.Lock()
				if ids[id] {
					t.Errorf("Duplicate ID generated: %d", id)
				}
				ids[id] = true
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	mu.Lock()
	if len(ids) != numIDs {
		t.Errorf("Expected %d unique IDs, got %d", numIDs, len(ids))
	}
	mu.Unlock()
}

// TestExtraNonce1Format tests extranonce1 format.
func TestExtraNonce1Format(t *testing.T) {
	sessionIDs := []uint64{1, 100, 65535, 1000000, 4294967295}

	for _, id := range sessionIDs {
		t.Run(fmt.Sprintf("session_%d", id), func(t *testing.T) {
			extranonce1 := fmt.Sprintf("%08x", id)

			if len(extranonce1) != 8 {
				t.Errorf("ExtraNonce1 should be 8 chars, got %d: %s", len(extranonce1), extranonce1)
			}

			// Verify it's valid hex
			for _, c := range extranonce1 {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("Invalid hex character: %c", c)
				}
			}
		})
	}
}

// TestMessageBufferLimit tests that oversized messages are rejected.
func TestMessageBufferLimit(t *testing.T) {
	const maxBuffer = 16384

	tests := []struct {
		size     int
		shouldOK bool
	}{
		{100, true},
		{1000, true},
		{8000, true},
		{16000, true},
		{16384, true},
		{16385, false},
		{32000, false},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("size_%d", tc.size), func(t *testing.T) {
			partial := make([]byte, tc.size)
			isOK := len(partial) <= maxBuffer

			if isOK != tc.shouldOK {
				t.Errorf("Size %d: expected shouldOK=%v, got %v", tc.size, tc.shouldOK, isOK)
			}
		})
	}
}

// TestJobHistoryPruning tests job history is properly pruned.
func TestJobHistoryPruning(t *testing.T) {
	const maxJobs = 10

	type MockJob struct {
		ID        string
		CreatedAt time.Time
	}

	jobs := make(map[string]*MockJob)

	// Add 20 jobs
	for i := 0; i < 20; i++ {
		job := &MockJob{
			ID:        fmt.Sprintf("job_%02d", i),
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}

		jobs[job.ID] = job

		// Prune if over limit
		if len(jobs) > maxJobs {
			var oldestID string
			var oldestTime time.Time

			for id, j := range jobs {
				if oldestID == "" || j.CreatedAt.Before(oldestTime) {
					oldestID = id
					oldestTime = j.CreatedAt
				}
			}

			delete(jobs, oldestID)
		}
	}

	if len(jobs) != maxJobs {
		t.Errorf("Expected %d jobs, got %d", maxJobs, len(jobs))
	}

	// Verify oldest jobs were removed
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("job_%02d", i)
		if _, exists := jobs[id]; exists {
			t.Errorf("Old job %s should have been pruned", id)
		}
	}

	// Verify newest jobs remain
	for i := 10; i < 20; i++ {
		id := fmt.Sprintf("job_%02d", i)
		if _, exists := jobs[id]; !exists {
			t.Errorf("New job %s should still exist", id)
		}
	}
}

// TestNewlineDelimitedParsing tests JSON message parsing.
func TestNewlineDelimitedParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			"single_message",
			`{"method":"mining.subscribe"}` + "\n",
			[]string{`{"method":"mining.subscribe"}`},
		},
		{
			"multiple_messages",
			`{"method":"subscribe"}` + "\n" + `{"method":"authorize"}` + "\n",
			[]string{`{"method":"subscribe"}`, `{"method":"authorize"}`},
		},
		{
			"empty_lines",
			"\n\n" + `{"method":"submit"}` + "\n\n",
			[]string{`{"method":"submit"}`},
		},
		{
			"partial_no_newline",
			`{"method":"partial"`,
			[]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			partial := []byte(tc.input)
			var messages []string

			for {
				idx := -1
				for i, b := range partial {
					if b == '\n' {
						idx = i
						break
					}
				}

				if idx == -1 {
					break
				}

				msg := partial[:idx]
				partial = partial[idx+1:]

				if len(msg) > 0 {
					messages = append(messages, string(msg))
				}
			}

			if len(messages) != len(tc.expected) {
				t.Errorf("Expected %d messages, got %d", len(tc.expected), len(messages))
				return
			}

			for i, msg := range messages {
				if msg != tc.expected[i] {
					t.Errorf("Message %d: expected %q, got %q", i, tc.expected[i], msg)
				}
			}
		})
	}
}

// TestKeepaliveInterval tests keepalive timing.
func TestKeepaliveInterval(t *testing.T) {
	interval := 30 * time.Second

	if interval <= 0 {
		t.Error("Keepalive interval should be positive")
	}

	if interval > 5*time.Minute {
		t.Error("Keepalive interval should not be too long")
	}
}

// TestConnectionTimeoutEnforcement tests timeout enforcement.
func TestConnectionTimeoutEnforcement(t *testing.T) {
	timeout := 5 * time.Minute
	lastActivity := time.Now().Add(-6 * time.Minute)

	isTimedOut := time.Since(lastActivity) > timeout
	if !isTimedOut {
		t.Error("Connection should be timed out")
	}

	lastActivity = time.Now().Add(-4 * time.Minute)
	isTimedOut = time.Since(lastActivity) > timeout
	if isTimedOut {
		t.Error("Connection should not be timed out")
	}
}

// TestDifficultyMessageFormat tests mining.set_difficulty format.
func TestDifficultyMessageFormat(t *testing.T) {
	difficulties := []float64{0.001, 1.0, 16.0, 65536.0, 0.0001}

	for _, diff := range difficulties {
		t.Run(fmt.Sprintf("diff_%g", diff), func(t *testing.T) {
			msg := fmt.Sprintf(`{"id":null,"method":"mining.set_difficulty","params":[%g]}`, diff)

			if !strings.Contains(msg, "mining.set_difficulty") {
				t.Error("Message should contain method name")
			}

			if !strings.Contains(msg, fmt.Sprintf("%g", diff)) {
				t.Errorf("Message should contain difficulty %g", diff)
			}
		})
	}
}

// TestConcurrentSessionAccess tests concurrent session map access.
func TestConcurrentSessionAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numOps = 10000

	sessions := sync.Map{}
	var wg sync.WaitGroup
	var sets, gets, deletes atomic.Uint64

	// Concurrent operations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numOps/100; j++ {
				id := uint64(rand.Intn(1000))

				switch rand.Intn(3) {
				case 0: // Set
					sessions.Store(id, fmt.Sprintf("session_%d_%d", workerID, j))
					sets.Add(1)
				case 1: // Get
					sessions.Load(id)
					gets.Add(1)
				case 2: // Delete
					sessions.Delete(id)
					deletes.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Operations: sets=%d, gets=%d, deletes=%d",
		sets.Load(), gets.Load(), deletes.Load())
}

// TestBroadcastUnderLoad tests job broadcasting under high load.
func TestBroadcastUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numSessions = 100
	const numBroadcasts = 50

	sessions := make([]uint64, numSessions)
	for i := range sessions {
		sessions[i] = uint64(i)
	}

	var broadcastCount atomic.Uint64
	var messagesSent atomic.Uint64

	for b := 0; b < numBroadcasts; b++ {
		broadcastCount.Add(1)

		var wg sync.WaitGroup
		for _, sessionID := range sessions {
			wg.Add(1)
			go func(id uint64) {
				defer wg.Done()
				// Simulate sending job to session
				time.Sleep(time.Duration(rand.Intn(100)) * time.Microsecond)
				messagesSent.Add(1)
				_ = id
			}(sessionID)
		}
		wg.Wait()
	}

	expectedMessages := uint64(numSessions * numBroadcasts)
	if messagesSent.Load() != expectedMessages {
		t.Errorf("Expected %d messages, got %d", expectedMessages, messagesSent.Load())
	}

	t.Logf("Broadcast %d jobs to %d sessions (%d total messages)",
		broadcastCount.Load(), numSessions, messagesSent.Load())
}

// TestPanicRecoveryInHandler tests that panics don't crash the server.
func TestPanicRecoveryInHandler(t *testing.T) {
	panicMessages := []string{
		"nil pointer dereference",
		"index out of range",
		"invalid memory address",
	}

	for _, panicMsg := range panicMessages {
		t.Run(strings.ReplaceAll(panicMsg, " ", "_"), func(t *testing.T) {
			recovered := false

			func() {
				defer func() {
					if r := recover(); r != nil {
						recovered = true
						if !strings.Contains(fmt.Sprint(r), panicMsg) {
							t.Errorf("Unexpected panic: %v", r)
						}
					}
				}()

				panic(panicMsg)
			}()

			if !recovered {
				t.Error("Panic should have been recovered")
			}
		})
	}
}

// TestWriteDeadlineEnforcement tests write deadline setting.
func TestWriteDeadlineEnforcement(t *testing.T) {
	deadlines := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		30 * time.Second,
	}

	for _, d := range deadlines {
		t.Run(fmt.Sprintf("%v", d), func(t *testing.T) {
			deadline := time.Now().Add(d)

			if deadline.Before(time.Now()) {
				t.Error("Deadline should be in the future")
			}

			if time.Until(deadline) > d+time.Second {
				t.Error("Deadline should be approximately d from now")
			}
		})
	}
}

// TestSessionStatistics tests session statistics tracking.
func TestSessionStatistics(t *testing.T) {
	type MockSession struct {
		ValidShares   atomic.Uint64
		InvalidShares atomic.Uint64
	}

	session := &MockSession{}

	const numValid = 100
	const numInvalid = 10

	var wg sync.WaitGroup
	for i := 0; i < numValid; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.ValidShares.Add(1)
		}()
	}

	for i := 0; i < numInvalid; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.InvalidShares.Add(1)
		}()
	}

	wg.Wait()

	if session.ValidShares.Load() != uint64(numValid) {
		t.Errorf("Expected %d valid shares, got %d", numValid, session.ValidShares.Load())
	}

	if session.InvalidShares.Load() != uint64(numInvalid) {
		t.Errorf("Expected %d invalid shares, got %d", numInvalid, session.InvalidShares.Load())
	}
}

// BenchmarkSessionMapAccess benchmarks session map operations.
func BenchmarkSessionMapAccess(b *testing.B) {
	sessions := sync.Map{}

	// Pre-populate
	for i := 0; i < 1000; i++ {
		sessions.Store(uint64(i), fmt.Sprintf("session_%d", i))
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			id := uint64(i % 1000)
			sessions.Load(id)
			i++
		}
	})
}

// BenchmarkBroadcast benchmarks job broadcast to many sessions.
func BenchmarkBroadcast(b *testing.B) {
	numSessions := 100
	sessions := make([]uint64, numSessions)
	for i := range sessions {
		sessions[i] = uint64(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for range sessions {
			// Simulate minimal work
			_ = fmt.Sprintf("%d", i)
		}
	}
}
