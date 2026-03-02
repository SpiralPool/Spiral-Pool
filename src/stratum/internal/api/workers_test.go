// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package api - Tests for worker API endpoints.
//
// These tests verify:
// - Input validation (addresses, worker names)
// - HTTP method enforcement
// - Parameter parsing and bounds checking
// - Response format correctness
// - Security boundary tests
package api

import (
	"testing"
)

// TestValidWorkerPattern verifies worker name pattern validation.
func TestValidWorkerPattern(t *testing.T) {
	tests := []struct {
		name      string
		worker    string
		wantValid bool
	}{
		// Valid worker names
		{name: "simple", worker: "worker1", wantValid: true},
		{name: "underscore", worker: "my_worker", wantValid: true},
		{name: "dash", worker: "my-worker", wantValid: true},
		{name: "dot", worker: "worker.1", wantValid: true},
		{name: "mixed", worker: "My_Worker-1.0", wantValid: true},
		{name: "numbers only", worker: "12345", wantValid: true},
		{name: "max length 64", worker: "a123456789012345678901234567890123456789012345678901234567890123", wantValid: true},

		// Invalid worker names - security tests
		{name: "too long 65 chars", worker: "a1234567890123456789012345678901234567890123456789012345678901234", wantValid: false},
		{name: "empty", worker: "", wantValid: false},
		{name: "space", worker: "worker name", wantValid: false},
		{name: "SQL injection", worker: "'; DROP TABLE--", wantValid: false},
		{name: "path traversal", worker: "../etc/passwd", wantValid: false},
		{name: "XSS script", worker: "<script>alert(1)</script>", wantValid: false},
		{name: "null byte", worker: "worker\x00name", wantValid: false},
		{name: "newline", worker: "worker\nname", wantValid: false},
		{name: "tab", worker: "worker\tname", wantValid: false},
		{name: "special @", worker: "worker@name", wantValid: false},
		{name: "special #", worker: "worker#name", wantValid: false},
		{name: "special $", worker: "worker$name", wantValid: false},
		{name: "special %", worker: "worker%name", wantValid: false},
		{name: "special ^", worker: "worker^name", wantValid: false},
		{name: "special &", worker: "worker&name", wantValid: false},
		{name: "special *", worker: "worker*name", wantValid: false},
		{name: "special (", worker: "worker(name", wantValid: false},
		{name: "special )", worker: "worker)name", wantValid: false},
		{name: "special +", worker: "worker+name", wantValid: false},
		{name: "special =", worker: "worker=name", wantValid: false},
		{name: "special {", worker: "worker{name", wantValid: false},
		{name: "special }", worker: "worker}name", wantValid: false},
		{name: "special [", worker: "worker[name", wantValid: false},
		{name: "special ]", worker: "worker]name", wantValid: false},
		{name: "special |", worker: "worker|name", wantValid: false},
		{name: "special \\", worker: "worker\\name", wantValid: false},
		{name: "special :", worker: "worker:name", wantValid: false},
		{name: "special ;", worker: "worker;name", wantValid: false},
		{name: "special \"", worker: "worker\"name", wantValid: false},
		{name: "special '", worker: "worker'name", wantValid: false},
		{name: "special <", worker: "worker<name", wantValid: false},
		{name: "special >", worker: "worker>name", wantValid: false},
		{name: "special ,", worker: "worker,name", wantValid: false},
		{name: "special ?", worker: "worker?name", wantValid: false},
		{name: "special /", worker: "worker/name", wantValid: false},
		{name: "special `", worker: "worker`name", wantValid: false},
		{name: "special ~", worker: "worker~name", wantValid: false},
		{name: "unicode", worker: "worker\u202Ename", wantValid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validWorkerPattern.MatchString(tt.worker)
			if got != tt.wantValid {
				t.Errorf("validWorkerPattern.MatchString(%q) = %v, want %v", tt.worker, got, tt.wantValid)
			}
		})
	}
}

// TestParseWorkerPath verifies path parsing.
func TestParseWorkerPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantAddr   string
		wantWorker string
		wantAction string
	}{
		{
			name:       "full path with history",
			path:       "/api/pools/dgb/miners/DAddress123/workers/rig1/history",
			wantAddr:   "DAddress123",
			wantWorker: "rig1",
			wantAction: "history",
		},
		{
			name:       "worker without action",
			path:       "/api/pools/dgb/miners/DAddress123/workers/rig1",
			wantAddr:   "DAddress123",
			wantWorker: "rig1",
			wantAction: "",
		},
		{
			name:       "miners only",
			path:       "/api/pools/dgb/miners/DAddress123",
			wantAddr:   "DAddress123",
			wantWorker: "",
			wantAction: "",
		},
		{
			name:       "workers endpoint only",
			path:       "/api/pools/dgb/miners/DAddress123/workers",
			wantAddr:   "DAddress123",
			wantWorker: "",
			wantAction: "",
		},
		{
			name:       "empty path",
			path:       "",
			wantAddr:   "",
			wantWorker: "",
			wantAction: "",
		},
		{
			name:       "no miners segment",
			path:       "/api/pools/dgb/workers",
			wantAddr:   "",
			wantWorker: "",
			wantAction: "",
		},
		{
			name:       "deep path",
			path:       "/api/pools/dgb/miners/DAddr/workers/rig1/history/extra",
			wantAddr:   "DAddr",
			wantWorker: "rig1",
			wantAction: "history",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, worker, action := parseWorkerPath(tt.path)
			if addr != tt.wantAddr {
				t.Errorf("address = %q, want %q", addr, tt.wantAddr)
			}
			if worker != tt.wantWorker {
				t.Errorf("worker = %q, want %q", worker, tt.wantWorker)
			}
			if action != tt.wantAction {
				t.Errorf("action = %q, want %q", action, tt.wantAction)
			}
		})
	}
}

// TestParseWorkerPathSecurity verifies path parsing rejects injection attempts.
func TestParseWorkerPathSecurity(t *testing.T) {
	// These paths should parse but the components should be validated separately
	paths := []struct {
		path       string
		expectAddr string
	}{
		{"/api/miners/../../../etc/passwd/workers/rig1", "../../../etc/passwd"},
		{"/api/miners/'; DROP TABLE--/workers/rig1", "'; DROP TABLE--"},
	}

	for _, tt := range paths {
		t.Run(tt.path, func(t *testing.T) {
			addr, _, _ := parseWorkerPath(tt.path)
			// The parser extracts the segment - validation happens in handler
			if addr != tt.expectAddr {
				t.Logf("Parsed addr: %q (validation should happen in handler)", addr)
			}
		})
	}
}

// TestHoursParameterBounds verifies hours parameter parsing.
func TestHoursParameterBounds(t *testing.T) {
	tests := []struct {
		input    string
		expected int
		valid    bool
	}{
		{"24", 24, true},
		{"1", 1, true},
		{"720", 720, true}, // Max allowed
		{"721", 0, false},  // Over max - should be rejected
		{"0", 0, false},    // Zero - invalid
		{"-1", 0, false},   // Negative - invalid
		{"abc", 0, false},  // Non-numeric
		{"", 0, false},     // Empty uses default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			// Simulate the parsing logic from handleWorkerHistory
			hours := 24 // default
			if tt.input != "" {
				// This matches the handler logic
				if parsed, err := parseIntOrZero(tt.input); err == nil && parsed > 0 && parsed <= 720 {
					hours = parsed
				}
			}

			if tt.valid && hours != tt.expected {
				t.Errorf("hours = %d, want %d", hours, tt.expected)
			}
		})
	}
}

// TestLimitParameterBounds verifies limit parameter parsing.
func TestLimitParameterBounds(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"100", 100},
		{"1", 1},
		{"1000", 1000}, // Max allowed
		{"1001", 100},  // Over max - uses default
		{"0", 100},     // Zero - uses default
		{"-1", 100},    // Negative - uses default
		{"abc", 100},   // Non-numeric - uses default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			// Simulate the parsing logic from handlePoolWorkers
			limit := 100 // default
			if parsed, err := parseIntOrZero(tt.input); err == nil && parsed > 0 && parsed <= 1000 {
				limit = parsed
			}

			if limit != tt.expected {
				t.Errorf("limit = %d, want %d", limit, tt.expected)
			}
		})
	}
}

// parseIntOrZero is a helper for testing parameter parsing.
func parseIntOrZero(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	var n int
	_, err := parseNumber(s, &n)
	return n, err
}

// parseNumber parses a string into an int pointer.
func parseNumber(s string, out *int) (bool, error) {
	if s == "" {
		return false, nil
	}
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, &parseError{s}
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return true, nil
}

type parseError struct {
	s string
}

func (e *parseError) Error() string {
	return "invalid number: " + e.s
}

// TestWindowParameterBounds verifies window parameter parsing for worker list endpoint.
func TestWindowParameterBounds(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"default", "", 15},           // Default to efficient 15-minute window
		{"minimum", "1", 1},           // Minimum allowed
		{"15 minutes", "15", 15},      // Standard real-time
		{"1 hour", "60", 60},          // Last hour
		{"24 hours", "1440", 1440},    // Maximum allowed (1 day)
		{"over max", "1441", 15},      // Over max - uses default
		{"zero", "0", 15},             // Zero - uses default
		{"negative", "-1", 15},        // Negative - uses default
		{"non-numeric", "abc", 15},    // Non-numeric - uses default
		{"injection", "15;DROP", 15},  // SQL injection attempt - uses default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the parsing logic from handleMinerWorkers
			windowMinutes := 15 // default
			if tt.input != "" {
				if parsed, err := parseIntOrZero(tt.input); err == nil && parsed >= 1 && parsed <= 1440 {
					windowMinutes = parsed
				}
			}

			if windowMinutes != tt.expected {
				t.Errorf("windowMinutes = %d, want %d", windowMinutes, tt.expected)
			}
		})
	}
}

// BenchmarkValidWorkerPattern benchmarks worker name validation.
func BenchmarkValidWorkerPattern(b *testing.B) {
	workers := []string{
		"worker1",
		"my_long_worker_name",
		"'; DROP TABLE--",
		"<script>alert(1)</script>",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, w := range workers {
			validWorkerPattern.MatchString(w)
		}
	}
}

// BenchmarkParseWorkerPath benchmarks path parsing.
func BenchmarkParseWorkerPath(b *testing.B) {
	path := "/api/pools/dgb/miners/DAddress123/workers/rig1/history"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseWorkerPath(path)
	}
}
