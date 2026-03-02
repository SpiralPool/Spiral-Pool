// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v1 provides stress tests for Stratum protocol handling.
//
// These tests validate:
// - Malformed message handling
// - Rapid subscribe/authorize sequences
// - Out-of-order message handling
// - Session stress testing
package v1

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// TestMalformedMessageRecovery tests that malformed messages don't crash the handler.
func TestMalformedMessageRecovery(t *testing.T) {
	malformedMessages := []struct {
		name string
		msg  string
	}{
		{"empty", ""},
		{"just_brace", "{"},
		{"unclosed_brace", `{"id":1`},
		{"invalid_json", `{id:1,method:"mining.subscribe"}`},
		{"null_bytes", "\x00\x00\x00"},
		{"unicode_garbage", "\xc0\xc1\xf5\xf6"},
		{"extremely_long", strings.Repeat("a", 100000)},
		{"nested_deep", strings.Repeat(`{"a":`, 100) + "1" + strings.Repeat("}", 100)},
		{"array_not_object", `[1,2,3]`},
		{"number_only", `42`},
		{"string_only", `"hello"`},
		{"null_only", `null`},
		{"boolean_only", `true`},
		{"missing_method", `{"id":1,"params":[]}`},
		{"null_method", `{"id":1,"method":null,"params":[]}`},
		{"number_method", `{"id":1,"method":42,"params":[]}`},
		{"missing_params", `{"id":1,"method":"mining.subscribe"}`},
		{"null_params", `{"id":1,"method":"mining.subscribe","params":null}`},
		{"string_params", `{"id":1,"method":"mining.subscribe","params":"invalid"}`},
		{"negative_id", `{"id":-1,"method":"mining.subscribe","params":[]}`},
		{"float_id", `{"id":1.5,"method":"mining.subscribe","params":[]}`},
		{"huge_id", `{"id":999999999999999999999999999,"method":"mining.subscribe","params":[]}`},
	}

	handler := NewHandler(1.0, true, 0x1FFFE000)
	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	for _, tc := range malformedMessages {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			_, err := handler.HandleMessage(session, []byte(tc.msg))

			// Error is expected for malformed messages
			if err == nil && tc.msg != "" {
				t.Logf("No error for message: %s (may be valid edge case)", tc.name)
			}
		})
	}
}

// FuzzStratumMessageParsing fuzz tests Stratum message parsing.
func FuzzStratumMessageParsing(f *testing.F) {
	// Seed corpus with various message types
	f.Add(`{"id":1,"method":"mining.subscribe","params":[]}`)
	f.Add(`{"id":2,"method":"mining.authorize","params":["addr.worker","password"]}`)
	f.Add(`{"id":3,"method":"mining.submit","params":["worker","job","en2","ntime","nonce"]}`)
	f.Add(`{"id":null,"method":"mining.notify","params":[]}`)
	f.Add(`{}`)
	f.Add(`{"id":1}`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`""`)

	handler := NewHandler(1.0, true, 0x1FFFE000)

	f.Fuzz(func(t *testing.T, msg string) {
		session := &protocol.Session{
			ID:              1,
			ExtraNonce1:     "00000001",
			ExtraNonce2Size: 4,
		}

		// Must not panic
		handler.HandleMessage(session, []byte(msg))
	})
}

// TestRapidSubscribeAuthorize tests rapid subscribe/authorize sequences.
func TestRapidSubscribeAuthorize(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numSessions = 100
	const cyclesPerSession = 10

	handler := NewHandler(1.0, true, 0x1FFFE000)

	var wg sync.WaitGroup
	var subscribeOK, authorizeOK atomic.Uint64

	for s := 0; s < numSessions; s++ {
		wg.Add(1)
		go func(sessionID int) {
			defer wg.Done()

			for cycle := 0; cycle < cyclesPerSession; cycle++ {
				session := &protocol.Session{
					ID:              uint64(sessionID*1000 + cycle),
					ExtraNonce1:     fmt.Sprintf("%08x", sessionID),
					ExtraNonce2Size: 4,
				}

				// Subscribe
				subscribeMsg := `{"id":1,"method":"mining.subscribe","params":["TestMiner/1.0"]}`
				resp, err := handler.HandleMessage(session, []byte(subscribeMsg))
				if err == nil && len(resp) > 0 {
					subscribeOK.Add(1)
				}

				// Authorize
				authorizeMsg := fmt.Sprintf(`{"id":2,"method":"mining.authorize","params":["DAddr%d.worker%d","x"]}`,
					sessionID, cycle)
				resp, err = handler.HandleMessage(session, []byte(authorizeMsg))
				if err == nil && len(resp) > 0 {
					authorizeOK.Add(1)
				}
			}
		}(s)
	}

	wg.Wait()

	expectedTotal := uint64(numSessions * cyclesPerSession)
	t.Logf("Subscribe OK: %d/%d, Authorize OK: %d/%d",
		subscribeOK.Load(), expectedTotal, authorizeOK.Load(), expectedTotal)

	if subscribeOK.Load() != expectedTotal {
		t.Errorf("Expected %d successful subscribes, got %d", expectedTotal, subscribeOK.Load())
	}
	if authorizeOK.Load() != expectedTotal {
		t.Errorf("Expected %d successful authorizes, got %d", expectedTotal, authorizeOK.Load())
	}
}

// TestOutOfOrderNotifyHandling tests handling of out-of-order messages.
func TestOutOfOrderNotifyHandling(t *testing.T) {
	handler := NewHandler(1.0, true, 0x1FFFE000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	// Try to submit before authorize (should fail)
	submitMsg := `{"id":1,"method":"mining.submit","params":["worker","job1","00000001","12345678","deadbeef"]}`
	resp, err := handler.HandleMessage(session, []byte(submitMsg))
	if err != nil {
		t.Fatalf("HandleMessage returned error: %v", err)
	}

	// Parse response
	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Should be an error response (unauthorized)
	if response.Error == nil {
		t.Error("Expected error for submit before authorize")
	}

	// Now subscribe
	subscribeMsg := `{"id":2,"method":"mining.subscribe","params":[]}`
	_, _ = handler.HandleMessage(session, []byte(subscribeMsg))

	// Submit should still fail (not authorized yet)
	resp, _ = handler.HandleMessage(session, []byte(submitMsg))
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if response.Error == nil {
		t.Error("Expected error for submit before authorize (after subscribe)")
	}

	// Now authorize
	authorizeMsg := `{"id":3,"method":"mining.authorize","params":["DAddress.worker","x"]}`
	_, _ = handler.HandleMessage(session, []byte(authorizeMsg))

	// Submit is accepted after authorization (may fail validation - testing auth flow only)
	resp, _ = handler.HandleMessage(session, []byte(submitMsg))
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	// Error is OK (invalid job), but shouldn't be "unauthorized"
	if response.Error != nil {
		errArr, ok := response.Error.([]interface{})
		if ok && len(errArr) >= 2 {
			if errArr[1] == "Unauthorized" {
				t.Error("Still getting unauthorized after authorize")
			}
		}
	}
}

// TestInvalidExtraNonces tests handling of invalid extranonce values.
func TestInvalidExtraNonces(t *testing.T) {
	handler := NewHandler(1.0, true, 0x1FFFE000)

	// Set up a handler that accepts shares
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)
	session.SetDifficulty(1.0)

	invalidSubmits := []struct {
		name   string
		en2    string
		errMsg string
	}{
		{"too_short", "00", "Invalid extranonce2 length"},
		{"too_long", "0000000001", "Invalid extranonce2 length"},
		{"invalid_hex", "0000000g", "Invalid extranonce2 hex"},
		{"uppercase", "0000000A", ""},  // This should be valid
		{"mixed_case", "0000000a", ""}, // This should be valid
	}

	for _, tc := range invalidSubmits {
		t.Run(tc.name, func(t *testing.T) {
			submitMsg := fmt.Sprintf(`{"id":1,"method":"mining.submit","params":["worker","job1","%s","12345678","deadbeef"]}`, tc.en2)
			resp, err := handler.HandleMessage(session, []byte(submitMsg))
			if err != nil {
				t.Fatalf("HandleMessage error: %v", err)
			}

			var response Response
			if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
				t.Fatalf("Parse error: %v", err)
			}

			if tc.errMsg != "" {
				if response.Error == nil {
					t.Errorf("Expected error containing '%s'", tc.errMsg)
				}
			}
		})
	}
}

// TestConcurrentShareSubmission tests concurrent share submissions.
func TestConcurrentShareSubmission(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numSessions = 20
	const sharesPerSession = 100

	handler := NewHandler(1.0, true, 0x1FFFE000)

	var shareCount atomic.Uint64
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		shareCount.Add(1)
		return &protocol.ShareResult{Accepted: true}
	})

	var wg sync.WaitGroup
	var accepted atomic.Uint64

	for s := 0; s < numSessions; s++ {
		wg.Add(1)
		go func(sessionID int) {
			defer wg.Done()

			session := &protocol.Session{
				ID:              uint64(sessionID),
				ExtraNonce1:     fmt.Sprintf("%08x", sessionID),
				ExtraNonce2Size: 4,
				MinerAddress:    "DTestAddress",
				WorkerName:      fmt.Sprintf("worker%d", sessionID),
			}
			session.SetSubscribed(true)
			session.SetAuthorized(true)
			session.SetDifficulty(1.0)

			for i := 0; i < sharesPerSession; i++ {
				submitMsg := fmt.Sprintf(`{"id":%d,"method":"mining.submit","params":["worker","job1","%08x","%08x","%08x"]}`,
					i+1, i, rand.Uint32(), rand.Uint32())

				resp, _ := handler.HandleMessage(session, []byte(submitMsg))

				var response Response
				if json.Unmarshal(resp[:len(resp)-1], &response) == nil {
					if response.Result == true {
						accepted.Add(1)
					}
				}
			}
		}(s)
	}

	wg.Wait()

	expectedShares := uint64(numSessions * sharesPerSession)
	t.Logf("Shares processed: %d, Accepted: %d", shareCount.Load(), accepted.Load())

	if shareCount.Load() != expectedShares {
		t.Errorf("Expected %d shares processed, got %d", expectedShares, shareCount.Load())
	}
}

// TestBuildNotifyStress tests notify message building under stress.
func TestBuildNotifyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 100

	handler := NewHandler(1.0, true, 0x1FFFE000)

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				job := &protocol.Job{
					ID:             fmt.Sprintf("job_%d_%d", gID, i),
					PrevBlockHash:  "0000000000000000000000000000000000000000000000000000000000000001",
					CoinBase1:      "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403",
					CoinBase2:      "ffffffff01",
					MerkleBranches: []string{"0000000000000000000000000000000000000000000000000000000000000002"},
					Version:        "20000000",
					NBits:          "1d00ffff",
					NTime:          fmt.Sprintf("%08x", i),
					CleanJobs:      i%10 == 0,
				}

				msg, err := handler.BuildNotify(job)
				if err != nil {
					errors.Add(1)
					continue
				}

				if len(msg) == 0 {
					errors.Add(1)
					continue
				}

				// Verify it's valid JSON
				var notification Notification
				if json.Unmarshal(msg[:len(msg)-1], &notification) != nil {
					errors.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("BuildNotify errors: %d", errors.Load())
	}
}

// TestBuildSetDifficultyStress tests difficulty message building under stress.
func TestBuildSetDifficultyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 100

	handler := NewHandler(1.0, true, 0x1FFFE000)

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				// Various difficulty values
				diffs := []float64{0.001, 0.1, 1.0, 10.0, 100.0, 1000.0}
				diff := diffs[i%len(diffs)]

				msg, err := handler.BuildSetDifficulty(diff)
				if err != nil {
					errors.Add(1)
					continue
				}

				if len(msg) == 0 {
					errors.Add(1)
					continue
				}

				// Verify it's valid JSON
				var notification Notification
				if json.Unmarshal(msg[:len(msg)-1], &notification) != nil {
					errors.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("BuildSetDifficulty errors: %d", errors.Load())
	}
}

// BenchmarkMessageParsing benchmarks message parsing performance.
func BenchmarkMessageParsing(b *testing.B) {
	handler := NewHandler(1.0, true, 0x1FFFE000)
	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	messages := []struct {
		name string
		msg  string
	}{
		{"subscribe", `{"id":1,"method":"mining.subscribe","params":["TestMiner/1.0"]}`},
		{"authorize", `{"id":2,"method":"mining.authorize","params":["addr.worker","x"]}`},
		{"submit", `{"id":3,"method":"mining.submit","params":["worker","job1","00000001","12345678","deadbeef"]}`},
	}

	for _, m := range messages {
		b.Run(m.name, func(b *testing.B) {
			msgBytes := []byte(m.msg)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				handler.HandleMessage(session, msgBytes)
			}
		})
	}
}

// BenchmarkConcurrentMessageParsing benchmarks parallel message parsing.
func BenchmarkConcurrentMessageParsing(b *testing.B) {
	handler := NewHandler(1.0, true, 0x1FFFE000)
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	b.RunParallel(func(pb *testing.PB) {
		session := &protocol.Session{
			ID:              uint64(rand.Int63()),
			ExtraNonce1:     fmt.Sprintf("%08x", rand.Uint32()),
			ExtraNonce2Size: 4,
		}
		session.SetSubscribed(true)
		session.SetAuthorized(true)
		session.SetDifficulty(1.0)

		i := 0
		for pb.Next() {
			msg := fmt.Sprintf(`{"id":%d,"method":"mining.submit","params":["worker","job1","%08x","12345678","deadbeef"]}`, i, i)
			handler.HandleMessage(session, []byte(msg))
			i++
		}
	})
}
