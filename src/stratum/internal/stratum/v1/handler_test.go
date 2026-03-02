// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v1 provides tests for Stratum V1 protocol handling.
//
// These tests validate:
// - Subscribe/Authorize/Submit message handling
// - JSON-RPC parsing and responses
// - Share parameter validation
// - Session state management
package v1

import (
	"encoding/json"
	"testing"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// TestHandleSubscribe validates mining.subscribe handling.
func TestHandleSubscribe(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	// Valid subscribe request
	req := `{"id":1,"method":"mining.subscribe","params":["TestMiner/1.0"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// Parse response
	var response Response
	if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil { // Remove trailing newline
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error != nil {
		t.Errorf("Unexpected error in response: %v", response.Error)
	}

	// Validate result structure
	result, ok := response.Result.([]interface{})
	if !ok || len(result) != 3 {
		t.Fatalf("Invalid result structure: %v", response.Result)
	}

	// Check extranonce1
	if en1, ok := result[1].(string); !ok || en1 != "00000001" {
		t.Errorf("ExtraNonce1 = %v, want 00000001", result[1])
	}

	// Check extranonce2_size
	if en2Size, ok := result[2].(float64); !ok || int(en2Size) != 4 {
		t.Errorf("ExtraNonce2Size = %v, want 4", result[2])
	}

	// Session should be marked as subscribed
	if !session.IsSubscribed() {
		t.Error("Session should be marked as subscribed")
	}

	// User agent should be set
	if session.UserAgent != "TestMiner/1.0" {
		t.Errorf("UserAgent = %s, want TestMiner/1.0", session.UserAgent)
	}
}

// TestHandleAuthorize validates mining.authorize handling.
func TestHandleAuthorize(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	tests := []struct {
		name         string
		request      string
		expectAuth   bool
		expectAddr   string
		expectWorker string
		expectError  bool
	}{
		{
			name:         "valid address.worker",
			request:      `{"id":1,"method":"mining.authorize","params":["DGBaddress.worker1","x"]}`,
			expectAuth:   true,
			expectAddr:   "DGBaddress",
			expectWorker: "worker1",
		},
		{
			name:         "valid address only",
			request:      `{"id":1,"method":"mining.authorize","params":["DGBaddress","x"]}`,
			expectAuth:   true,
			expectAddr:   "DGBaddress",
			expectWorker: "default",
		},
		{
			name:        "missing params",
			request:     `{"id":1,"method":"mining.authorize","params":[]}`,
			expectAuth:  false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset session — use atomic method (Authorized bool is deprecated)
			session.SetAuthorized(false)
			session.MinerAddress = ""
			session.WorkerName = ""
			// FSM: Must subscribe before authorize
			session.SetSubscribed(true)

			resp, err := h.HandleMessage(session, []byte(tt.request))
			if err != nil {
				t.Fatalf("HandleMessage failed: %v", err)
			}

			var response Response
			json.Unmarshal(resp[:len(resp)-1], &response)

			if tt.expectError {
				if response.Error == nil {
					t.Error("Expected error response")
				}
				return
			}

			if session.IsAuthorized() != tt.expectAuth {
				t.Errorf("Authorized = %v, want %v", session.IsAuthorized(), tt.expectAuth)
			}

			if session.MinerAddress != tt.expectAddr {
				t.Errorf("MinerAddress = %s, want %s", session.MinerAddress, tt.expectAddr)
			}

			if session.WorkerName != tt.expectWorker {
				t.Errorf("WorkerName = %s, want %s", session.WorkerName, tt.expectWorker)
			}
		})
	}
}

// TestHandleSubmit validates mining.submit handling.
func TestHandleSubmit(t *testing.T) {
	var receivedShare *protocol.Share
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		receivedShare = share
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		MinerAddress:    "DGBaddress",
		WorkerName:      "worker1",
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
		UserAgent:       "TestMiner/1.0",
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	// SECURITY: Use atomic method to set authorized state
	session.SetAuthorized(true)

	// Valid submit
	req := `{"id":1,"method":"mining.submit","params":["DGBaddress.worker1","00000001","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error != nil {
		t.Errorf("Unexpected error: %v", response.Error)
	}

	if response.Result != true {
		t.Errorf("Result = %v, want true", response.Result)
	}

	// Validate share was correctly parsed
	if receivedShare == nil {
		t.Fatal("Share handler not called")
	}

	if receivedShare.JobID != "00000001" {
		t.Errorf("JobID = %s, want 00000001", receivedShare.JobID)
	}

	if receivedShare.ExtraNonce1 != "00000001" {
		t.Errorf("ExtraNonce1 = %s, want 00000001", receivedShare.ExtraNonce1)
	}

	if receivedShare.ExtraNonce2 != "00000002" {
		t.Errorf("ExtraNonce2 = %s, want 00000002", receivedShare.ExtraNonce2)
	}

	if receivedShare.NTime != "64000000" {
		t.Errorf("NTime = %s, want 64000000", receivedShare.NTime)
	}

	if receivedShare.Nonce != "12345678" {
		t.Errorf("Nonce = %s, want 12345678", receivedShare.Nonce)
	}

	// Validate session was updated (use atomic accessor)
	if session.GetValidShares() != 1 {
		t.Errorf("ValidShares = %d, want 1", session.GetValidShares())
	}
}

// TestHandleSubmitUnauthorized validates rejection of unauthorized submits.
func TestHandleSubmitUnauthorized(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:         1,
		Authorized: false, // Not authorized
	}
	// FSM: Must subscribe to test unauthorized case (otherwise "Not subscribed" error)
	session.SetSubscribed(true)

	req := `{"id":1,"method":"mining.submit","params":["address.worker","jobid","en2","ntime","nonce"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error == nil {
		t.Error("Expected error for unauthorized submit")
	}
}

// TestHandleSubmitInvalidParams validates parameter validation.
func TestHandleSubmitInvalidParams(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	session := &protocol.Session{
		ID:              1,
		ExtraNonce2Size: 4, // Expect 8 hex chars
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	tests := []struct {
		name    string
		request string
	}{
		{
			name:    "wrong extranonce2 length",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job","0000","64000000","12345678"]}`,
		},
		{
			name:    "invalid extranonce2 hex",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job","XXXXXXXX","64000000","12345678"]}`,
		},
		{
			name:    "wrong ntime length",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","640000","12345678"]}`,
		},
		{
			name:    "invalid ntime hex",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","XXXXXXXX","12345678"]}`,
		},
		{
			name:    "wrong nonce length",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","1234"]}`,
		},
		{
			name:    "invalid nonce hex",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","XXXXXXXX"]}`,
		},
		{
			name:    "too few params",
			request: `{"id":1,"method":"mining.submit","params":["addr.w","job"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := h.HandleMessage(session, []byte(tt.request))
			if err != nil {
				t.Fatalf("HandleMessage failed: %v", err)
			}

			var response Response
			json.Unmarshal(resp[:len(resp)-1], &response)

			if response.Error == nil {
				t.Error("Expected error for invalid params")
			}
		})
	}
}

// TestHandleSubmitWithVersionBits validates BIP320 version rolling.
func TestHandleSubmitWithVersionBits(t *testing.T) {
	var receivedShare *protocol.Share
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		receivedShare = share
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	// Submit with version bits
	req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678","1fffe000"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error != nil {
		t.Errorf("Unexpected error: %v", response.Error)
	}

	if receivedShare == nil {
		t.Fatal("Share handler not called")
	}

	if receivedShare.VersionBits != 0x1fffe000 {
		t.Errorf("VersionBits = 0x%08x, want 0x1fffe000", receivedShare.VersionBits)
	}
}

// TestBuildNotify validates mining.notify message construction.
func TestBuildNotify(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	job := &protocol.Job{
		ID:             "00000001",
		PrevBlockHash:  "000000000000000000000000000000000000000000000000000000000000dead",
		CoinBase1:      "01000000010000",
		CoinBase2:      "ffffffff01",
		MerkleBranches: []string{"branch1", "branch2"},
		Version:        "20000000",
		NBits:          "1a0377ae",
		NTime:          "64000000",
		CleanJobs:      true,
	}

	msg, err := h.BuildNotify(job)
	if err != nil {
		t.Fatalf("BuildNotify failed: %v", err)
	}

	// Parse the notification
	var notification Notification
	if err := json.Unmarshal(msg[:len(msg)-1], &notification); err != nil {
		t.Fatalf("Failed to parse notification: %v", err)
	}

	if notification.Method != "mining.notify" {
		t.Errorf("Method = %s, want mining.notify", notification.Method)
	}

	if notification.ID != nil {
		t.Error("Notification should have nil ID")
	}

	params := notification.Params
	if len(params) != 9 {
		t.Fatalf("Invalid params length: %d, want 9", len(params))
	}

	// Validate params order
	if params[0] != job.ID {
		t.Errorf("Params[0] = %v, want %s", params[0], job.ID)
	}
	if params[1] != job.PrevBlockHash {
		t.Errorf("Params[1] = %v, want %s", params[1], job.PrevBlockHash)
	}
	if params[8] != true {
		t.Errorf("Params[8] (CleanJobs) = %v, want true", params[8])
	}
}

// TestBuildSetDifficulty validates mining.set_difficulty message.
func TestBuildSetDifficulty(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	msg, err := h.BuildSetDifficulty(16.0)
	if err != nil {
		t.Fatalf("BuildSetDifficulty failed: %v", err)
	}

	var notification Notification
	if err := json.Unmarshal(msg[:len(msg)-1], &notification); err != nil {
		t.Fatalf("Failed to parse notification: %v", err)
	}

	if notification.Method != "mining.set_difficulty" {
		t.Errorf("Method = %s, want mining.set_difficulty", notification.Method)
	}

	params := notification.Params
	if len(params) != 1 {
		t.Fatalf("Invalid params length: %d, want 1", len(params))
	}

	if diff, ok := params[0].(float64); !ok || diff != 16.0 {
		t.Errorf("Difficulty = %v, want 16.0", params[0])
	}
}

// TestParseWorkerName validates worker name parsing.
func TestParseWorkerName(t *testing.T) {
	tests := []struct {
		input        string
		expectAddr   string
		expectWorker string
	}{
		{"DGBaddress.worker1", "DGBaddress", "worker1"},
		{"DGBaddress.rig.01", "DGBaddress.rig", "01"},
		{"DGBaddress", "DGBaddress", "default"},
		{"address.", "address", ""},
		{".worker", "", "worker"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			addr, worker := parseWorkerName(tt.input)
			if addr != tt.expectAddr {
				t.Errorf("address = %s, want %s", addr, tt.expectAddr)
			}
			if worker != tt.expectWorker {
				t.Errorf("worker = %s, want %s", worker, tt.expectWorker)
			}
		})
	}
}

// TestUnknownMethod validates unknown method handling.
func TestUnknownMethod(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)
	session := &protocol.Session{ID: 1}

	req := `{"id":1,"method":"unknown.method","params":[]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error == nil {
		t.Error("Expected error for unknown method")
	}
}

// TestInvalidJSON validates JSON error handling.
func TestInvalidJSON(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)
	session := &protocol.Session{ID: 1}

	_, err := h.HandleMessage(session, []byte("not valid json"))
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

// =============================================================================
// Rate Limiter Tests (SEC-05)
// =============================================================================

// TestShareRateLimiter_AllowsNormal verifies normal operation.
func TestShareRateLimiter_AllowsNormal(t *testing.T) {
	callCount := 0
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareRateLimiter(func(remoteAddr string) (bool, string) {
		callCount++
		return true, "" // Always allow
	})
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error != nil {
		t.Errorf("Unexpected error: %v", response.Error)
	}

	if callCount != 1 {
		t.Errorf("Rate limiter called %d times, want 1", callCount)
	}
}

// TestShareRateLimiter_BlocksExcessive verifies rate limiting.
func TestShareRateLimiter_BlocksExcessive(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareRateLimiter(func(remoteAddr string) (bool, string) {
		return false, "Rate limit exceeded" // Block all shares
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error == nil {
		t.Error("Expected error for rate-limited share")
	}

	// Verify error code 25 (rate limit)
	errArray, ok := response.Error.([]interface{})
	if !ok || len(errArray) < 2 {
		t.Fatal("Invalid error response format")
	}

	if errCode, ok := errArray[0].(float64); !ok || int(errCode) != 25 {
		t.Errorf("Error code = %v, want 25", errArray[0])
	}

	if errMsg, ok := errArray[1].(string); !ok || errMsg != "Rate limit exceeded" {
		t.Errorf("Error message = %v, want 'Rate limit exceeded'", errArray[1])
	}
}

// TestShareRateLimiter_NilIsPermissive verifies nil limiter allows shares.
func TestShareRateLimiter_NilIsPermissive(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)
	// Don't set a rate limiter - should be permissive
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`
	resp, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error != nil {
		t.Errorf("Nil rate limiter should allow shares: %v", response.Error)
	}
}

// TestShareRateLimiter_IPBased verifies IP-based rate limiting.
func TestShareRateLimiter_IPBased(t *testing.T) {
	blocked := make(map[string]bool)
	blocked["192.168.1.100:12345"] = true

	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareRateLimiter(func(remoteAddr string) (bool, string) {
		if blocked[remoteAddr] {
			return false, "IP blocked"
		}
		return true, ""
	})
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	tests := []struct {
		name        string
		remoteAddr  string
		expectAllow bool
	}{
		{"allowed IP", "192.168.1.1:12345", true},
		{"blocked IP", "192.168.1.100:12345", false},
		{"another allowed", "10.0.0.1:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &protocol.Session{
				ID:              1,
				ExtraNonce1:     "00000001",
				ExtraNonce2Size: 4,
				RemoteAddr:      tt.remoteAddr,
			}
			// FSM: Must subscribe before authorize/submit
			session.SetSubscribed(true)
			session.SetAuthorized(true)

			req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`
			resp, _ := h.HandleMessage(session, []byte(req))

			var response Response
			json.Unmarshal(resp[:len(resp)-1], &response)

			allowed := response.Error == nil
			if allowed != tt.expectAllow {
				t.Errorf("remoteAddr=%s: allowed=%v, want %v",
					tt.remoteAddr, allowed, tt.expectAllow)
			}
		})
	}
}

// TestShareRateLimiter_TokenBucket simulates token bucket rate limiting.
func TestShareRateLimiter_TokenBucket(t *testing.T) {
	// Simulate a simple token bucket: 10 shares/second
	tokens := 10
	lastRefill := 0

	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareRateLimiter(func(remoteAddr string) (bool, string) {
		// Simplified token bucket (no time-based refill for test)
		if tokens > 0 {
			tokens--
			return true, ""
		}
		return false, "Rate limit exceeded"
	})
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`

	// First 10 should succeed
	for i := 0; i < 10; i++ {
		resp, _ := h.HandleMessage(session, []byte(req))
		var response Response
		json.Unmarshal(resp[:len(resp)-1], &response)
		if response.Error != nil {
			t.Errorf("Share %d should be allowed", i+1)
		}
	}

	// 11th should fail
	resp, _ := h.HandleMessage(session, []byte(req))
	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)
	if response.Error == nil {
		t.Error("Share 11 should be rate limited")
	}

	_ = lastRefill // Would be used in real implementation
}

// TestShareRateLimiter_CalledBeforeValidation verifies order of operations.
func TestShareRateLimiter_CalledBeforeValidation(t *testing.T) {
	rateLimiterCalled := false
	shareHandlerCalled := false

	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareRateLimiter(func(remoteAddr string) (bool, string) {
		rateLimiterCalled = true
		return false, "blocked" // Block all
	})
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		shareHandlerCalled = true
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
		RemoteAddr:      "192.168.1.1:12345",
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	req := `{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`
	h.HandleMessage(session, []byte(req))

	if !rateLimiterCalled {
		t.Error("Rate limiter should be called")
	}

	if shareHandlerCalled {
		t.Error("Share handler should NOT be called when rate limited")
	}
}

// =============================================================================
// Input Validation Security Tests
// =============================================================================

// TestWorkerNameValidation tests worker name character validation.
func TestWorkerNameValidation(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	tests := []struct {
		name        string
		workerName  string
		expectError bool
	}{
		{"valid simple", "miner1.rig1", false},
		{"valid no worker", "miner1", false},
		{"valid underscores", "miner_1.rig_1", false},
		{"valid dashes", "miner-1.rig-1", false},
		{"empty", "", true},
		{"spaces", "miner rig", false}, // Spaces are allowed by the relaxed validWorkerName regex
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &protocol.Session{ID: 1, ExtraNonce1: "00000001", ExtraNonce2Size: 4}
			// FSM: Must subscribe before authorize
			session.SetSubscribed(true)
			// Create request struct for proper JSON marshaling
			reqObj := Request{
				ID:     1,
				Method: "mining.authorize",
				Params: []interface{}{tt.workerName, "x"},
			}
			reqBytes, _ := json.Marshal(reqObj)

			resp, err := h.HandleMessage(session, reqBytes)
			if err != nil {
				// JSON parse error for special characters is also "error"
				if tt.expectError {
					return // Expected error
				}
				t.Fatalf("HandleMessage failed: %v", err)
			}

			var response Response
			if len(resp) > 0 {
				json.Unmarshal(resp[:len(resp)-1], &response)
			}

			hasError := response.Error != nil
			if hasError != tt.expectError {
				t.Errorf("workerName=%q: hasError=%v, want %v",
					tt.workerName, hasError, tt.expectError)
			}
		})
	}
}

// TestWorkerNameInjectionAttacks tests specific injection attack patterns.
func TestWorkerNameInjectionAttacks(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	// Test SQL injection pattern
	session := &protocol.Session{ID: 1, ExtraNonce1: "00000001", ExtraNonce2Size: 4}
	// FSM: Must subscribe before authorize
	session.SetSubscribed(true)
	reqObj := Request{
		ID:     1,
		Method: "mining.authorize",
		Params: []interface{}{"'; DROP TABLE--", "x"},
	}
	reqBytes, _ := json.Marshal(reqObj)

	resp, err := h.HandleMessage(session, reqBytes)
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error == nil {
		t.Error("SQL injection pattern should be rejected")
	}
}

// TestUserAgentTruncation tests user-agent length limit.
func TestUserAgentTruncation(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	// Create a very long user agent
	longUA := ""
	for i := 0; i < 500; i++ {
		longUA += "X"
	}

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	req := `{"id":1,"method":"mining.subscribe","params":["` + longUA + `"]}`
	_, err := h.HandleMessage(session, []byte(req))
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if len(session.UserAgent) > maxUserAgentLen {
		t.Errorf("UserAgent length = %d, want <= %d",
			len(session.UserAgent), maxUserAgentLen)
	}

	if len(session.UserAgent) != maxUserAgentLen {
		t.Errorf("UserAgent should be truncated to %d chars", maxUserAgentLen)
	}
}

// TestWorkerNameLengthLimit tests worker name max length.
func TestWorkerNameLengthLimit(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)

	// Create a worker name at the limit
	longWorker := ""
	for i := 0; i < maxWorkerNameLen+10; i++ {
		longWorker += "a"
	}

	session := &protocol.Session{ID: 1, ExtraNonce1: "00000001", ExtraNonce2Size: 4}
	// FSM: Must subscribe before authorize
	session.SetSubscribed(true)
	req := `{"id":1,"method":"mining.authorize","params":["` + longWorker + `","x"]}`

	resp, _ := h.HandleMessage(session, []byte(req))
	var response Response
	json.Unmarshal(resp[:len(resp)-1], &response)

	if response.Error == nil {
		t.Error("Should reject worker name exceeding max length")
	}
}

// TestExtranonce2RangeValidation tests extranonce2 value range.
func TestExtranonce2RangeValidation(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	tests := []struct {
		name        string
		extranonce2 string
		expectError bool
	}{
		{"valid low", "00000000", false},
		{"valid mid", "80000000", false},
		{"valid high", "ffffff00", false},
		{"near max", "ffffffff", true}, // Last 256 positions rejected
		{"wrap attack", "ffffff01", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := `{"id":1,"method":"mining.submit","params":["addr.w","job","` + tt.extranonce2 + `","64000000","12345678"]}`

			resp, _ := h.HandleMessage(session, []byte(req))
			var response Response
			json.Unmarshal(resp[:len(resp)-1], &response)

			hasError := response.Error != nil
			if hasError != tt.expectError {
				t.Errorf("extranonce2=%s: hasError=%v, want %v",
					tt.extranonce2, hasError, tt.expectError)
			}
		})
	}
}

// =============================================================================
// Miner Difficulty Router Tests
// =============================================================================

// TestMinerDifficultyRouter tests user-agent based difficulty routing.
func TestMinerDifficultyRouter(t *testing.T) {
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetMinerDifficultyRouter(func(userAgent string) float64 {
		switch {
		case userAgent == "BFGMiner/5.5.0":
			return 16.0 // ASIC
		case userAgent == "cpuminer/2.5.1":
			return 0.001 // CPU
		default:
			return 0 // Use default
		}
	})

	tests := []struct {
		userAgent    string
		expectedDiff float64
	}{
		{"BFGMiner/5.5.0", 16.0},
		{"cpuminer/2.5.1", 0.001},
		{"UnknownMiner/1.0", 1.0}, // Default
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			session := &protocol.Session{
				ID:              1,
				ExtraNonce1:     "00000001",
				ExtraNonce2Size: 4,
			}

			// Subscribe first (sets UserAgent and marks as subscribed)
			subscribeReq := `{"id":1,"method":"mining.subscribe","params":["` + tt.userAgent + `"]}`
			h.HandleMessage(session, []byte(subscribeReq))

			// Authorize (session is now subscribed via subscribe call above)
			authReq := `{"id":2,"method":"mining.authorize","params":["addr.worker","x"]}`
			h.HandleMessage(session, []byte(authReq))

			if session.GetDifficulty() != tt.expectedDiff {
				t.Errorf("Difficulty = %f, want %f",
					session.GetDifficulty(), tt.expectedDiff)
			}
		})
	}
}

// Benchmark tests

func BenchmarkHandleSubscribe(b *testing.B) {
	h := NewHandler(1.0, true, 0x1fffe000)
	req := []byte(`{"id":1,"method":"mining.subscribe","params":["TestMiner/1.0"]}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		session := &protocol.Session{
			ID:              uint64(i),
			ExtraNonce1:     "00000001",
			ExtraNonce2Size: 4,
		}
		h.HandleMessage(session, req)
	}
}

func BenchmarkHandleSubmit(b *testing.B) {
	h := NewHandler(1.0, true, 0x1fffe000)
	h.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	req := []byte(`{"id":1,"method":"mining.submit","params":["addr.w","job","00000002","64000000","12345678"]}`)
	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}
	// FSM: Must subscribe before authorize/submit
	session.SetSubscribed(true)
	session.SetAuthorized(true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.HandleMessage(session, req)
	}
}

func BenchmarkBuildNotify(b *testing.B) {
	h := NewHandler(1.0, true, 0x1fffe000)
	job := &protocol.Job{
		ID:             "00000001",
		PrevBlockHash:  "000000000000000000000000000000000000000000000000000000000000dead",
		CoinBase1:      "01000000010000",
		CoinBase2:      "ffffffff01",
		MerkleBranches: []string{"branch1", "branch2"},
		Version:        "20000000",
		NBits:          "1a0377ae",
		NTime:          "64000000",
		CleanJobs:      true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.BuildNotify(job)
	}
}
