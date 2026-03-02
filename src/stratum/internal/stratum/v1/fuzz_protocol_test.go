// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v1 provides comprehensive fuzz tests for Stratum V1 protocol.
//
// These fuzz tests target security-critical parsing and protocol handling:
// - JSON message parsing edge cases
// - Worker authorization flow
// - Share submission validation
// - Message size limits and memory safety
//
// Run with: go test -fuzz=FuzzXxx -fuzztime=5m ./...
package v1

import (
	"encoding/json"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// JSON MESSAGE PARSING FUZZING
// =============================================================================

// FuzzJSONMessageParsing tests JSON message parsing with malicious inputs.
func FuzzJSONMessageParsing(f *testing.F) {
	// Seed corpus with valid and edge case messages
	f.Add(`{"id":1,"method":"mining.subscribe","params":[]}`)
	f.Add(`{"id":1,"method":"mining.authorize","params":["worker","password"]}`)
	f.Add(`{"id":1,"method":"mining.submit","params":["worker","job","00000001","12345678","deadbeef"]}`)
	f.Add(`{}`)
	f.Add(`{"id":null}`)
	f.Add(`{"id":-1}`)
	f.Add(`{"id":9999999999999999999}`)
	f.Add(`{"method":null}`)
	f.Add(`{"method":""}`)
	f.Add(`{"method":"unknown.method"}`)
	f.Add(`{"params":null}`)
	f.Add(`{"params":"not_an_array"}`)
	f.Add(`{"params":{}}`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`true`)
	f.Add(`false`)
	f.Add(`123`)
	f.Add(`"string"`)
	f.Add(`{"nested":{"deeply":{"nested":{"value":true}}}}`)
	// Injection attempts
	f.Add(`{"id":1,"method":"mining.subscribe","params":["<script>alert(1)</script>"]}`)
	f.Add(`{"id":1,"method":"mining.authorize","params":["'; DROP TABLE shares;--","x"]}`)
	f.Add(`{"id":1,"method":"mining.submit","params":["\x00\x01\x02","job","00000001","12345678","deadbeef"]}`)
	// Unicode edge cases
	f.Add(`{"id":1,"method":"mining.authorize","params":["\u0000hidden","x"]}`)
	f.Add(`{"id":1,"method":"mining.authorize","params":["\u202Ereversed","x"]}`)
	// Large values
	f.Add(`{"id":1,"method":"mining.subscribe","params":["` + string(make([]byte, 1000)) + `"]}`)

	handler := NewHandler(1.0, true, 0x1FFFE000)

	f.Fuzz(func(t *testing.T, jsonMsg string) {
		session := &protocol.Session{
			ID:              1,
			ExtraNonce1:     "12345678",
			ExtraNonce2Size: 4,
		}
		session.SetSubscribed(true)
		session.SetAuthorized(true)
		session.SetDifficulty(1.0)

		// Should not panic on any input
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on message %q: %v", truncate(jsonMsg, 100), r)
			}
		}()

		resp, _ := handler.HandleMessage(session, []byte(jsonMsg))

		// If we got a response, verify it's valid JSON
		if len(resp) > 0 {
			// Remove trailing newline if present
			if resp[len(resp)-1] == '\n' {
				resp = resp[:len(resp)-1]
			}
			var v interface{}
			if err := json.Unmarshal(resp, &v); err != nil {
				t.Errorf("Response is not valid JSON: %v, response: %s", err, truncate(string(resp), 100))
			}
		}
	})
}

// =============================================================================
// WORKER NAME FUZZING
// =============================================================================

// FuzzWorkerName tests worker name parsing and validation.
func FuzzWorkerName(f *testing.F) {
	// Seed corpus with various worker name formats
	f.Add("DGBaddress.worker1")
	f.Add("DGBaddress")
	f.Add("address.worker.with.dots")
	f.Add("")
	f.Add(".")
	f.Add("..")
	f.Add("worker.")
	f.Add(".worker")
	f.Add("a")
	f.Add(string(make([]byte, 256)))
	// Injection attempts
	f.Add("worker'; DROP TABLE--")
	f.Add("worker<script>")
	f.Add("worker\x00hidden")
	f.Add("worker\r\nHTTP/1.1")
	// Unicode
	f.Add("worker\u0000null")
	f.Add("工人")
	f.Add("worker🔥emoji")

	f.Fuzz(func(t *testing.T, workerName string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on worker name %q: %v", truncate(workerName, 50), r)
			}
		}()

		address, worker := parseWorkerName(workerName)

		// Basic sanity checks
		// Note: address can never be longer than input
		// Note: worker can be "default" (7 chars) even for short inputs without dots
		if len(address) > len(workerName) {
			t.Errorf("Address longer than input: %q from %q", address, workerName)
		}
		// Worker is "default" if no dot separator - this is valid
		if worker != "default" && len(worker) > len(workerName) {
			t.Errorf("Worker longer than input: %q from %q", worker, workerName)
		}
	})
}

// =============================================================================
// SUBMIT PARAMETERS FUZZING
// =============================================================================

// FuzzSubmitParams tests mining.submit parameter parsing.
func FuzzSubmitParams(f *testing.F) {
	// Valid submit formats
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef")
	f.Add("address.worker", "1", "ffffffff", "00000000", "00000000")
	// Edge cases
	f.Add("", "", "", "", "")
	f.Add("worker", "job", "0", "0", "0")
	f.Add("worker", "job", "12345678", "12345678", "12345678") // Valid lengths
	// Invalid hex
	f.Add("worker", "job", "ghijklmn", "12345678", "deadbeef")
	f.Add("worker", "job", "00000001", "zzzzzzzz", "deadbeef")
	f.Add("worker", "job", "00000001", "12345678", "notahex!")
	// Wrong lengths
	f.Add("worker", "job", "000001", "12345678", "deadbeef")     // 6 chars
	f.Add("worker", "job", "0000000001", "12345678", "deadbeef") // 10 chars
	f.Add("worker", "job", "00000001", "123456", "deadbeef")     // 6 chars
	f.Add("worker", "job", "00000001", "12345678", "dead")       // 4 chars

	handler := NewHandler(1.0, true, 0x1FFFE000)
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	f.Fuzz(func(t *testing.T, worker, jobID, extranonce2, ntime, nonce string) {
		session := &protocol.Session{
			ID:              1,
			ExtraNonce1:     "12345678",
			ExtraNonce2Size: 4,
		}
		session.SetSubscribed(true)
		session.SetAuthorized(true)
		session.SetDifficulty(1.0)

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on submit params: worker=%q job=%q en2=%q ntime=%q nonce=%q: %v",
					truncate(worker, 20), truncate(jobID, 20), truncate(extranonce2, 20),
					truncate(ntime, 20), truncate(nonce, 20), r)
			}
		}()

		// Build submit message
		params := []interface{}{worker, jobID, extranonce2, ntime, nonce}
		msg := map[string]interface{}{
			"id":     1,
			"method": "mining.submit",
			"params": params,
		}
		jsonMsg, err := json.Marshal(msg)
		if err != nil {
			return // Invalid JSON, skip
		}

		_, _ = handler.HandleMessage(session, jsonMsg)
	})
}

// =============================================================================
// VERSION BITS FUZZING
// =============================================================================

// FuzzVersionBits tests version rolling parameter handling.
func FuzzVersionBits(f *testing.F) {
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef", "20000000")
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef", "00000000")
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef", "ffffffff")
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef", "1fffe000")
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef", "")
	f.Add("worker", "job123", "00000001", "12345678", "deadbeef", "invalid")

	handler := NewHandler(1.0, true, 0x1FFFE000)
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	f.Fuzz(func(t *testing.T, worker, jobID, extranonce2, ntime, nonce, versionBits string) {
		session := &protocol.Session{
			ID:              1,
			ExtraNonce1:     "12345678",
			ExtraNonce2Size: 4,
		}
		session.SetSubscribed(true)
		session.SetAuthorized(true)
		session.SetDifficulty(1.0)

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on version bits: %q: %v", truncate(versionBits, 20), r)
			}
		}()

		// Build submit message with version bits (6 params)
		params := []interface{}{worker, jobID, extranonce2, ntime, nonce, versionBits}
		msg := map[string]interface{}{
			"id":     1,
			"method": "mining.submit",
			"params": params,
		}
		jsonMsg, _ := json.Marshal(msg)

		_, _ = handler.HandleMessage(session, jsonMsg)
	})
}

// =============================================================================
// MESSAGE SIZE FUZZING
// =============================================================================

// FuzzMessageSize tests handling of various message sizes.
func FuzzMessageSize(f *testing.F) {
	// Various sizes
	f.Add(0)
	f.Add(1)
	f.Add(100)
	f.Add(1000)
	f.Add(10000)
	f.Add(100000)
	f.Add(1000000)

	handler := NewHandler(1.0, true, 0x1FFFE000)

	f.Fuzz(func(t *testing.T, size int) {
		// Limit size to prevent OOM
		if size < 0 || size > 10*1024*1024 {
			return
		}

		session := &protocol.Session{
			ID:              1,
			ExtraNonce1:     "12345678",
			ExtraNonce2Size: 4,
		}
		session.SetSubscribed(true)
		session.SetAuthorized(true)
		session.SetDifficulty(1.0)

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on message size %d: %v", size, r)
			}
		}()

		// Create a message of the specified size
		msg := make([]byte, size)
		for i := range msg {
			msg[i] = 'a'
		}

		_, _ = handler.HandleMessage(session, msg)
	})
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// truncate shortens a string for logging.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
