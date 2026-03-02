// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Stratum Boundary Audit — V1 Handler layer tests
// Vectors: S9-S11 (Malformed JSON), S12-S14 (Protocol FSM), Parameter validation
package v1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// S9/S10 — Malformed JSON-RPC (validateJSONStructure)
// =============================================================================

// TestS9_JSONStructure_DeepNesting verifies that 33 levels of JSON nesting
// are rejected by validateJSONStructure to prevent CPU exhaustion.
func TestS9_JSONStructure_DeepNesting(t *testing.T) {
	t.Parallel()

	// Build JSON with 33 levels of nesting: {"a":{"a":...}}
	var b strings.Builder
	for i := 0; i < 33; i++ {
		b.WriteString(`{"a":`)
	}
	b.WriteString(`1`)
	for i := 0; i < 33; i++ {
		b.WriteString(`}`)
	}
	data := []byte(b.String())

	err := validateJSONStructure(data)
	if err == nil {
		t.Fatal("Expected non-nil error for 33 levels of nesting")
	}
	if !strings.Contains(err.Error(), "nesting too deep") {
		t.Errorf("Error = %q, want it to contain %q", err.Error(), "nesting too deep")
	}
}

// TestS9_JSONStructure_MaxDepthAllowed verifies that exactly 32 levels of
// nesting (the maximum) are accepted.
func TestS9_JSONStructure_MaxDepthAllowed(t *testing.T) {
	t.Parallel()

	// Build JSON with exactly 32 levels of nesting
	var b strings.Builder
	for i := 0; i < 32; i++ {
		b.WriteString(`{"a":`)
	}
	b.WriteString(`1`)
	for i := 0; i < 32; i++ {
		b.WriteString(`}`)
	}
	data := []byte(b.String())

	err := validateJSONStructure(data)
	if err != nil {
		t.Errorf("Expected nil error for exactly 32 levels of nesting, got: %v", err)
	}
}

// TestS10_JSONStructure_LargeArray verifies that an array with 102 elements
// (101 commas) is rejected by validateJSONStructure.
// The validator counts commas (not elements): arrayCount resets on '[' and
// increments on each ','. maxArrayLen=100 means 101 commas triggers the error.
func TestS10_JSONStructure_LargeArray(t *testing.T) {
	t.Parallel()

	// Build JSON array with 102 elements → 101 commas → arrayCount reaches 101 → > 100
	var b strings.Builder
	b.WriteString(`[`)
	for i := 0; i < 102; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		b.WriteString(`1`)
	}
	b.WriteString(`]`)
	data := []byte(b.String())

	err := validateJSONStructure(data)
	if err == nil {
		t.Fatal("Expected non-nil error for array with 102 elements (101 commas exceeds maxArrayLen=100)")
	}
	if !strings.Contains(err.Error(), "array too large") {
		t.Errorf("Error = %q, want it to contain %q", err.Error(), "array too large")
	}
}

// TestS10_JSONStructure_LargeObject verifies that an object with 52 keys
// (51 commas) is rejected by validateJSONStructure.
// The validator counts commas: objectKeyCount resets on '{' and increments
// on each ','. maxObjectKeys=50 means 51 commas triggers the error.
func TestS10_JSONStructure_LargeObject(t *testing.T) {
	t.Parallel()

	// Build JSON object with 52 keys → 51 commas → objectKeyCount reaches 51 → > 50
	var b strings.Builder
	b.WriteString(`{`)
	for i := 0; i < 52; i++ {
		if i > 0 {
			b.WriteString(`,`)
		}
		b.WriteString(`"k`)
		b.WriteString(strings.Repeat("x", i)) // unique keys
		b.WriteString(`":1`)
	}
	b.WriteString(`}`)
	data := []byte(b.String())

	err := validateJSONStructure(data)
	if err == nil {
		t.Fatal("Expected non-nil error for object with 52 keys (51 commas exceeds maxObjectKeys=50)")
	}
	if !strings.Contains(err.Error(), "object too large") {
		t.Errorf("Error = %q, want it to contain %q", err.Error(), "object too large")
	}
}

// TestS9_JSONStructure_ValidJSON verifies that normal valid JSON passes
// structure validation without error.
func TestS9_JSONStructure_ValidJSON(t *testing.T) {
	t.Parallel()

	data := []byte(`{"id":1,"method":"mining.subscribe","params":["test"]}`)

	err := validateJSONStructure(data)
	if err != nil {
		t.Errorf("Expected nil error for valid JSON, got: %v", err)
	}
}

// TestS9_JSONStructure_StringBypass verifies that deeply nested content
// inside a JSON string value does NOT trigger the depth limit. The parser
// must skip content within quoted strings.
func TestS9_JSONStructure_StringBypass(t *testing.T) {
	t.Parallel()

	// Build a string value containing what looks like 50 levels of nesting
	var nested strings.Builder
	for i := 0; i < 50; i++ {
		nested.WriteString(`{\"a\":`)
	}
	nested.WriteString(`1`)
	for i := 0; i < 50; i++ {
		nested.WriteString(`}`)
	}

	// Wrap in a valid single-level JSON object with the nested content as a string value
	data := []byte(`{"payload":"` + nested.String() + `"}`)

	err := validateJSONStructure(data)
	if err != nil {
		t.Errorf("Deeply nested content inside a string should not trigger depth limit, got: %v", err)
	}
}

// =============================================================================
// S11 — Unknown Methods
// =============================================================================

// TestS11_UnknownMethod verifies that an unknown method name produces an
// error response with code 20.
func TestS11_UnknownMethod(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}

	req := `{"id":1,"method":"mining.bogus","params":[]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected non-nil error for unknown method")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	code, ok := errArray[0].(float64)
	if !ok || int(code) != 20 {
		t.Errorf("Error code = %v, want 20", errArray[0])
	}
}

// =============================================================================
// S12 — Submit Before Subscribe
// =============================================================================

// TestS12_SubmitBeforeSubscribe verifies that mining.submit is rejected
// when the session has not yet subscribed.
func TestS12_SubmitBeforeSubscribe(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// Session is NOT subscribed (default)

	req := `{"id":1,"method":"mining.submit","params":["addr.worker","job","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected error response for submit before subscribe")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	errMsg, ok := errArray[1].(string)
	if !ok || !strings.Contains(errMsg, "Not subscribed") {
		t.Errorf("Error message = %q, want it to contain %q", errMsg, "Not subscribed")
	}
}

// =============================================================================
// S13 — Authorize Before Subscribe
// =============================================================================

// TestS13_AuthorizeBeforeSubscribe verifies that mining.authorize is rejected
// when the session has not yet subscribed.
func TestS13_AuthorizeBeforeSubscribe(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// Session is NOT subscribed (default)

	req := `{"id":1,"method":"mining.authorize","params":["addr.worker","x"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected error response for authorize before subscribe")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	errMsg, ok := errArray[1].(string)
	if !ok || !strings.Contains(errMsg, "Not subscribed") {
		t.Errorf("Error message = %q, want it to contain %q", errMsg, "Not subscribed")
	}
}

// =============================================================================
// S14 — Submit Before Authorize
// =============================================================================

// TestS14_SubmitBeforeAuthorize verifies that mining.submit is rejected
// when the session is subscribed but not yet authorized.
func TestS14_SubmitBeforeAuthorize(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)
	// Session is NOT authorized (default)

	req := `{"id":1,"method":"mining.submit","params":["addr.worker","00000001","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected error response for submit before authorize")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	errMsg, ok := errArray[1].(string)
	if !ok || !strings.Contains(errMsg, "Unauthorized") {
		t.Errorf("Error message = %q, want it to contain %q", errMsg, "Unauthorized")
	}
}

// =============================================================================
// Parameter Validation
// =============================================================================

// TestS14_EmptyJobID verifies that a submit with an empty job_id is rejected.
func TestS14_EmptyJobID(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	req := `{"id":1,"method":"mining.submit","params":["addr.worker","","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Error("Expected error response for empty job_id")
	}
}

// TestS14_InvalidExtranonce2Hex verifies that a submit with non-hex extranonce2
// is rejected.
func TestS14_InvalidExtranonce2Hex(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	// "ZZZZZZZZ" is 8 chars (correct length for ExtraNonce2Size=4) but not valid hex
	req := `{"id":1,"method":"mining.submit","params":["addr.worker","00000001","ZZZZZZZZ","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Error("Expected error response for non-hex extranonce2")
	}
}

// TestS14_InvalidNTimeLength verifies that a submit with wrong-length ntime
// (6 chars instead of 8) is rejected.
func TestS14_InvalidNTimeLength(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	// ntime is 6 chars instead of the required 8
	req := `{"id":1,"method":"mining.submit","params":["addr.worker","00000001","00000002","640000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Error("Expected error response for wrong-length ntime")
	}
}

// TestS14_InvalidNonceHex verifies that a submit with non-hex nonce is rejected.
func TestS14_InvalidNonceHex(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	// "GGGGGGGG" is 8 chars (correct length) but not valid hex
	req := `{"id":1,"method":"mining.submit","params":["addr.worker","00000001","00000002","64000000","GGGGGGGG"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Error("Expected error response for non-hex nonce")
	}
}

// TestS14_TooFewParams verifies that a submit with only 3 params is rejected.
func TestS14_TooFewParams(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	// Only 3 params instead of the required 5
	req := `{"id":1,"method":"mining.submit","params":["addr.worker","00000001","00000002"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Error("Expected error response for too few params")
	}
}

// =============================================================================
// Worker Name Validation
// =============================================================================

// TestS14_WorkerName_Injection verifies that a worker name containing SQL
// injection characters is rejected with "Invalid characters".
func TestS14_WorkerName_Injection(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)

	// Worker name with SQL injection chars: semicolons, quotes
	reqObj := Request{
		ID:     1,
		Method: "mining.authorize",
		Params: []interface{}{`"; DROP TABLE`, "x"},
	}
	reqBytes, err := json.Marshal(reqObj)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	resp, err := h.HandleMessage(session, reqBytes)
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected error response for SQL injection worker name")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	errMsg, ok := errArray[1].(string)
	if !ok || !strings.Contains(errMsg, "Invalid characters") {
		t.Errorf("Error message = %q, want it to contain %q", errMsg, "Invalid characters")
	}
}

// TestS14_WorkerName_TooLong verifies that a worker name exceeding 256
// characters is rejected with "too long".
func TestS14_WorkerName_TooLong(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)

	// Build a 257-character worker name using valid characters
	longName := strings.Repeat("a", 257)

	reqObj := Request{
		ID:     1,
		Method: "mining.authorize",
		Params: []interface{}{longName, "x"},
	}
	reqBytes, err := json.Marshal(reqObj)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	resp, err := h.HandleMessage(session, reqBytes)
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected error response for 257-char worker name")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	errMsg, ok := errArray[1].(string)
	if !ok || !strings.Contains(errMsg, "too long") {
		t.Errorf("Error message = %q, want it to contain %q", errMsg, "too long")
	}
}

// TestS14_WorkerName_Empty verifies that an empty worker name is rejected
// with "Empty".
func TestS14_WorkerName_Empty(t *testing.T) {
	t.Parallel()

	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	session.SetSubscribed(true)

	reqObj := Request{
		ID:     1,
		Method: "mining.authorize",
		Params: []interface{}{"", "x"},
	}
	reqBytes, err := json.Marshal(reqObj)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	resp, err := h.HandleMessage(session, reqBytes)
	if err != nil {
		t.Fatalf("HandleMessage returned unexpected error: %v", err)
	}

	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected error response for empty worker name")
	}

	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatalf("Invalid error format: %v", response.Error)
	}

	errMsg, ok := errArray[1].(string)
	if !ok || !strings.Contains(errMsg, "Empty") {
		t.Errorf("Error message = %q, want it to contain %q", errMsg, "Empty")
	}
}
