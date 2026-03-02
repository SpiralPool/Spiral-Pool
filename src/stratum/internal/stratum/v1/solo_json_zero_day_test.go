// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Test 1: Depth boundary (maxJSONDepth = 32)
// ---------------------------------------------------------------------------

func TestJSONStructure_DepthBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		depth   int
		wantErr string // empty means nil expected
	}{
		{"depth_31_valid", 31, ""},
		{"depth_32_at_limit", 32, ""},
		{"depth_33_exceeds", 33, "nesting too deep"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Pattern: {"a":{"a":...{"a":1}...}}
			input := strings.Repeat(`{"a":`, tt.depth) + "1" + strings.Repeat("}", tt.depth)
			err := validateJSONStructure([]byte(input))

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("depth %d: expected nil error, got: %v", tt.depth, err)
				}
			} else {
				if err == nil {
					t.Errorf("depth %d: expected error containing %q, got nil", tt.depth, tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("depth %d: error %q does not contain %q", tt.depth, err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 2: Array length boundary (maxArrayLen = 100)
// ---------------------------------------------------------------------------
//
// Implementation detail: commas increment BOTH arrayCount and objectKeyCount.
// Since maxObjectKeys=50 < maxArrayLen=100, a flat array hits the object-key
// limit first. To isolate the array-length check we insert a dummy object
// every 49 commas to reset objectKeyCount while keeping arrayCount intact.
//
// Helper: buildArrayWithCommas produces a JSON array string whose total
// comma count (and thus arrayCount) equals exactly N, while never letting
// objectKeyCount exceed 49 between resets.

func buildArrayWithCommas(n int) string {
	// We will build: [1,1,...,{"k":1},1,1,...,{"k":1},...]
	// Every 49 commas we insert {"k":1} as an element (costs 1 comma for
	// the separator, but the '{' resets objectKeyCount to 0).
	//
	// Strategy: emit elements separated by commas. After every 49 commas
	// from the last reset, emit an object literal which resets objectKeyCount.
	// The object itself is an array element, so its separating comma still
	// increments arrayCount normally.

	const resetInterval = 49 // insert reset object after this many commas

	var b strings.Builder
	b.WriteByte('[')

	commasSoFar := 0
	sinceReset := 0

	// First element (no comma before it)
	b.WriteByte('1')

	for commasSoFar < n {
		b.WriteByte(',')
		commasSoFar++
		sinceReset++

		if sinceReset >= resetInterval {
			// Insert an object to reset objectKeyCount
			b.WriteString(`{"k":1}`)
			sinceReset = 0
		} else {
			b.WriteByte('1')
		}
	}

	b.WriteByte(']')
	return b.String()
}

func TestJSONStructure_ArrayLengthBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		commas  int
		wantErr string
	}{
		{"99_commas_valid", 99, ""},
		{"100_commas_at_limit", 100, ""},
		{"101_commas_exceeds", 101, "array too large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := buildArrayWithCommas(tt.commas)
			err := validateJSONStructure([]byte(input))

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("%d commas: expected nil, got: %v", tt.commas, err)
				}
			} else {
				if err == nil {
					t.Errorf("%d commas: expected error containing %q, got nil", tt.commas, tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("%d commas: error %q does not contain %q", tt.commas, err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 3: Object keys boundary (maxObjectKeys = 50)
// ---------------------------------------------------------------------------
//
// Each '{' resets objectKeyCount to 0. Commas inside the object increment it.
// We build a single flat object with N key-value pairs separated by N-1 commas.
// But commas also increment arrayCount (reset by '['). Since there is no '[',
// arrayCount accumulates from 0 — however maxArrayLen=100 > 50 so it won't
// interfere at the 50-comma boundary.

func buildObjectWithCommas(n int) string {
	parts := make([]string, n+1)
	for i := 0; i <= n; i++ {
		parts[i] = fmt.Sprintf(`"k_%d":1`, i)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestJSONStructure_ObjectKeysBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		commas  int
		wantErr string
	}{
		{"49_commas_valid", 49, ""},
		{"50_commas_at_limit", 50, ""},
		{"51_commas_exceeds", 51, "object too large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := buildObjectWithCommas(tt.commas)
			err := validateJSONStructure([]byte(input))

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("%d commas: expected nil, got: %v", tt.commas, err)
				}
			} else {
				if err == nil {
					t.Errorf("%d commas: expected error containing %q, got nil", tt.commas, tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("%d commas: error %q does not contain %q", tt.commas, err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 4: Braces inside JSON strings must not affect depth counting
// ---------------------------------------------------------------------------

func TestJSONStructure_StringsContainingBraces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"open_braces_in_string", `{"a":"{{{{[[[["}`},
		{"close_braces_in_string", `{"a":"}}}}]]]]"}`},
		{"mixed_braces_in_string", `{"a":"{[}]{[}]"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateJSONStructure([]byte(tt.input)); err != nil {
				t.Errorf("expected nil for braces inside strings, got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 5: Escaped quotes must not break string tracking
// ---------------------------------------------------------------------------

func TestJSONStructure_EscapedQuotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"escaped_quote_in_value", `{"a":"hello\"world"}`},
		{"escaped_backslash_then_quote", `{"a":"\\\""}` },
		{"double_escaped_backslash", `{"a":"\\\\"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateJSONStructure([]byte(tt.input)); err != nil {
				t.Errorf("expected nil for escaped quotes, got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 6: Empty / minimal inputs
// ---------------------------------------------------------------------------

func TestJSONStructure_EmptyInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty_string", ""},
		{"empty_object", "{}"},
		{"empty_array", "[]"},
		{"empty_json_string", `""`},
		{"null_literal", "null"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateJSONStructure([]byte(tt.input)); err != nil {
				t.Errorf("expected nil for %q, got: %v", tt.name, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 7: Nested arrays in objects — mixed nesting depth
// ---------------------------------------------------------------------------

func TestJSONStructure_NestedArraysInObjects(t *testing.T) {
	t.Parallel()

	// Each {"a":[ contributes 2 to depth (one for '{', one for '[').
	// Closing is ]} for each pair.
	buildAlternating := func(pairs int) string {
		return strings.Repeat(`{"a":[`, pairs) + "1" +
			strings.Repeat(`]}`, pairs)
	}

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			"depth_5_simple",
			`{"a":[{"b":[{"c":1}]}]}`,
			"",
		},
		{
			"depth_32_alternating_at_limit",
			buildAlternating(16), // 16 pairs × 2 = depth 32
			"",
		},
		{
			"depth_34_alternating_exceeds",
			buildAlternating(17), // 17 pairs × 2 = depth 34 > 32
			"nesting too deep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateJSONStructure([]byte(tt.input))

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected nil, got: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 8: Comma counter resets on new scope
// ---------------------------------------------------------------------------
//
// '[' resets arrayCount, '{' resets objectKeyCount. Verify that inner scopes
// do not accumulate into the outer scope's count.

func TestJSONStructure_CommaCounterResetOnNewScope(t *testing.T) {
	t.Parallel()

	// Build an outer array of 2 inner arrays, each with 49 commas (50
	// elements). The outer array has 1 comma separating the two inner
	// arrays. Each '[' resets arrayCount, so the inner arrays never
	// exceed 49. The outer comma count is 1.
	// Total depth = 2 (outer '[' + inner '[').
	//
	// Note: objectKeyCount also increments on commas but resets on '{'.
	// With no '{' in this payload, objectKeyCount accumulates across all
	// scopes. 2×49 + 1 = 99 commas total → objectKeyCount = 99 > 50.
	// This would trigger "object too large". To avoid this, wrap the
	// whole thing in an object so objectKeyCount resets at the top, but
	// even then inner commas still accumulate objectKeyCount.
	//
	// The real behaviour: '[' resets arrayCount only. So the inner arrays
	// each see arrayCount go up to 49, then the next '[' resets it.
	// BUT objectKeyCount is not reset by '[', so after the first inner
	// array's 49 commas + 1 outer comma, objectKeyCount = 50.
	// The second inner array adds 49 more → objectKeyCount = 99 > 50.
	// That fires "object too large" at the 51st total comma.
	//
	// To properly demonstrate array-scope reset while keeping
	// objectKeyCount safe, insert '{}' objects periodically inside
	// each inner array, following the same strategy as buildArrayWithCommas.

	// Inner array: 49 commas with object resets to keep objectKeyCount < 50
	inner := buildArrayWithCommas(49)
	// Outer: [ inner, inner ] → 1 outer comma
	input := "[" + inner + "," + inner + "]"

	err := validateJSONStructure([]byte(input))
	if err != nil {
		t.Errorf("expected nil for nested arrays with per-scope resets, got: %v", err)
	}

	// Also verify a much larger case: 3 inner arrays each with 99 commas.
	// Each '[' resets arrayCount so inner arrays stay at 99 ≤ 100.
	// Object resets inside buildArrayWithCommas keep objectKeyCount safe.
	inner99 := buildArrayWithCommas(99)
	bigInput := "[" + inner99 + "," + inner99 + "," + inner99 + "]"
	err = validateJSONStructure([]byte(bigInput))
	if err != nil {
		t.Errorf("expected nil for 3×99-comma inner arrays, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 9: HandleMessage rejects malicious JSON before parsing
// ---------------------------------------------------------------------------

func TestHandleMessage_JSONStructureRejection(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, false, 0)
	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "deadbeef",
		ExtraNonce2Size: 4,
	}

	t.Run("depth_33_rejected", func(t *testing.T) {
		t.Parallel()
		input := strings.Repeat(`{"a":`, 33) + "1" + strings.Repeat("}", 33)
		_, err := h.HandleMessage(session, []byte(input))
		if err == nil {
			t.Fatal("expected error for depth-33 nesting, got nil")
		}
		if !strings.Contains(err.Error(), "malformed JSON") {
			t.Errorf("error %q does not contain %q", err.Error(), "malformed JSON")
		}
	})

	t.Run("101_element_array_rejected", func(t *testing.T) {
		t.Parallel()
		input := buildArrayWithCommas(101)
		_, err := h.HandleMessage(session, []byte(input))
		if err == nil {
			t.Fatal("expected error for 101-element array, got nil")
		}
		if !strings.Contains(err.Error(), "malformed JSON") {
			t.Errorf("error %q does not contain %q", err.Error(), "malformed JSON")
		}
	})
}

// ---------------------------------------------------------------------------
// Test 10: Valid protocol messages pass structure validation
// ---------------------------------------------------------------------------

func TestHandleMessage_ValidProtocolMessages(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, false, 0)

	t.Run("valid_subscribe", func(t *testing.T) {
		t.Parallel()
		session := &protocol.Session{
			ID:              10,
			ExtraNonce1:     "aabbccdd",
			ExtraNonce2Size: 4,
		}
		msg := `{"id":1,"method":"mining.subscribe","params":["TestMiner/1.0"]}`
		resp, err := h.HandleMessage(session, []byte(msg))
		if err != nil {
			t.Fatalf("subscribe: unexpected error: %v", err)
		}
		var r Response
		if err := json.Unmarshal(resp[:len(resp)-1], &r); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if r.Error != nil {
			t.Errorf("subscribe returned error: %v", r.Error)
		}
	})

	t.Run("valid_authorize", func(t *testing.T) {
		t.Parallel()
		session := &protocol.Session{
			ID:              11,
			ExtraNonce1:     "aabbccdd",
			ExtraNonce2Size: 4,
		}
		// Must subscribe first
		session.SetSubscribed(true)
		msg := `{"id":2,"method":"mining.authorize","params":["addr.worker","x"]}`
		resp, err := h.HandleMessage(session, []byte(msg))
		if err != nil {
			t.Fatalf("authorize: unexpected error: %v", err)
		}
		var r Response
		if err := json.Unmarshal(resp[:len(resp)-1], &r); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if r.Error != nil {
			t.Errorf("authorize returned error: %v", r.Error)
		}
	})

	t.Run("unknown_method_returns_error_response", func(t *testing.T) {
		t.Parallel()
		session := &protocol.Session{
			ID:              12,
			ExtraNonce1:     "aabbccdd",
			ExtraNonce2Size: 4,
		}
		msg := `{"id":3,"method":"unknown","params":[]}`
		resp, err := h.HandleMessage(session, []byte(msg))
		if err != nil {
			t.Fatalf("unknown method: unexpected transport error: %v", err)
		}
		var r Response
		if err := json.Unmarshal(resp[:len(resp)-1], &r); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}
		if r.Error == nil {
			t.Error("unknown method should return error in response body")
		}
	})
}

// ---------------------------------------------------------------------------
// Test 11: Worker name injection attempts
// ---------------------------------------------------------------------------

func TestWorkerName_InjectionAttempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// Valid names
		{"addr_dot_worker", "addr.worker", true},
		{"upper_underscore_dash", "ADDR_worker-1", true},
		{"colon_separator", "a:b", true},
		{"plus_equals", "a+b=c", true},
		{"space_in_name", "a b", true},
		{"at_sign", "user@pool", true},

		// Invalid / injection attempts
		{"sql_injection", "addr;DROP TABLE", false},
		{"newline_injection", "addr\nworker", false},
		{"null_byte", "addr\x00worker", false},
		{"xss_angle_brackets", "<script>", false},
		{"empty_string", "", false},
		{"backtick", "addr`worker", false},
		{"single_quote", "addr'worker", false},
		{"double_quote", `addr"worker`, false},
		{"backslash", `addr\worker`, false},
		{"pipe", "addr|worker", false},
		{"ampersand", "addr&worker", false},
		{"dollar_sign", "addr$worker", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := validWorkerName.MatchString(tt.input)
			if got != tt.want {
				t.Errorf("validWorkerName.MatchString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 12: Billion-laughs style deep nesting — linear scanner caps at depth
// ---------------------------------------------------------------------------

func TestJSONStructure_BillionLaughs(t *testing.T) {
	t.Parallel()

	// Construct 100-deep nested objects. The scanner aborts at depth 33.
	input := strings.Repeat(`{"a":`, 100) + "1" + strings.Repeat("}", 100)
	err := validateJSONStructure([]byte(input))
	if err == nil {
		t.Fatal("expected depth error for 100-deep nesting, got nil")
	}
	if !strings.Contains(err.Error(), "nesting too deep") {
		t.Errorf("error %q does not contain %q", err.Error(), "nesting too deep")
	}
}

// ---------------------------------------------------------------------------
// Test 13: Large but valid payload — all limits respected
// ---------------------------------------------------------------------------

func TestJSONStructure_LargeButValidPayload(t *testing.T) {
	t.Parallel()

	// Build an object with 50 keys (49 commas), where each value is an
	// array of 50 elements (49 commas). Both are within limits.
	// Each key's array resets arrayCount via '[', and the commas inside
	// each array also increment objectKeyCount. But each value's '['
	// does NOT reset objectKeyCount, so the object-level commas (49)
	// plus inner commas would exceed objectKeyCount.
	//
	// To stay safe: build an object with 50 keys where each value is a
	// short array. The '{' at the start resets objectKeyCount. As we
	// iterate through the key-value pairs, commas between them increment
	// objectKeyCount. With 49 inter-key commas and inner-array commas,
	// objectKeyCount accumulates. To keep it under 50, use values with
	// no internal commas: each value is a single-element array [1].
	//
	// Then separately verify a single array with 99 commas (using the
	// helper that inserts object resets).

	// Part A: object with 50 keys, each with simple value → 49 commas
	parts := make([]string, 50)
	for i := 0; i < 50; i++ {
		parts[i] = fmt.Sprintf(`"key_%d":[1]`, i)
	}
	objPayload := "{" + strings.Join(parts, ",") + "}"

	if err := validateJSONStructure([]byte(objPayload)); err != nil {
		t.Errorf("50-key object: expected nil, got: %v", err)
	}

	// Part B: array with 99 commas (using reset-safe builder)
	arrPayload := buildArrayWithCommas(99)
	if err := validateJSONStructure([]byte(arrPayload)); err != nil {
		t.Errorf("99-comma array: expected nil, got: %v", err)
	}

	// Part C: combined — wrap the 99-comma array inside a 1-key object
	combined := `{"data":` + arrPayload + `}`
	if err := validateJSONStructure([]byte(combined)); err != nil {
		t.Errorf("combined payload: expected nil, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 14: No panic on pathological input
// ---------------------------------------------------------------------------

func TestHandleMessage_NoPanicOnAnything(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, false, 0)

	tests := []struct {
		name  string
		input []byte
	}{
		{"nil_input", nil},
		{"empty", []byte{}},
		{"single_zero", []byte{0}},
		{"all_zeros_16", make([]byte, 16)},
		{"all_0xff_16", bytes0xFF(16)},
		{"single_open_brace", []byte("{")},
		{"single_close_brace", []byte("}")},
		{"single_open_bracket", []byte("[")},
		{"single_close_bracket", []byte("]")},
		{"single_quote", []byte(`"`)},
		{"single_backslash", []byte(`\`)},
		{"null_bytes_1000", make([]byte, 1000)},
		{"alternating_open_braces", []byte(strings.Repeat("{[", 500))},
		{"only_commas", []byte(strings.Repeat(",", 200))},
		{"only_colons", []byte(strings.Repeat(":", 200))},
		{"utf8_bom_prefix", append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"id":1}`)...)},
		{"negative_json_number", []byte(`{"id":-1}`)},
		{"huge_number", []byte(`{"id":99999999999999999999999999}`)},
		{"truncated_string", []byte(`{"a":"hello`)},
		{"truncated_escape", []byte(`{"a":"\`)},
		{"nested_nulls", []byte(`[null,null,null,null,null]`)},
		{"mixed_whitespace", []byte(" \t\n\r{} \t\n\r")},
		{"just_true", []byte("true")},
		{"just_false", []byte("false")},
		{"binary_garbage", []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE}},
		{"control_chars", []byte("\x01\x02\x03\x04\x05\x06\x07")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic — errors are expected and acceptable.
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("HandleMessage panicked on %q: %v", tt.name, r)
					}
				}()
				_, _ = h.HandleMessage(&protocol.Session{
					ID:              99,
					ExtraNonce1:     "00000001",
					ExtraNonce2Size: 4,
				}, tt.input)
			}()
		})
	}
}

// bytes0xFF returns a byte slice of length n filled with 0xFF.
func bytes0xFF(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xFF
	}
	return b
}
