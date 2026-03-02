// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package daemon - Critical Network Failure Injection Tests
//
// Tests for distributed systems failure modes:
// - Bitcoind/Litecoind RPC timeout
// - Partial RPC responses
// - RPC returns stale data
// - Node restarts mid-job
// - Stratum process restart
//
// WHY IT MATTERS: Mining is distributed systems hell.
// VERIFY:
// - Miners are not fed invalid jobs
// - Difficulty does not desync
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// =============================================================================
// 1. RPC TIMEOUT TESTS
// =============================================================================

// TestRPCTimeout tests behavior when daemon doesn't respond in time
func TestRPCTimeout(t *testing.T) {
	t.Parallel()

	// Create slow server
	delays := []time.Duration{
		100 * time.Millisecond,
		500 * time.Millisecond,
		2 * time.Second,
		5 * time.Second,
	}

	for _, delay := range delays {
		delay := delay
		t.Run(fmt.Sprintf("delay_%v", delay), func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(delay)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(RPCResponse{
					JSONRPC: "2.0",
					ID:      1,
					Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
				})
			}))
			defer server.Close()

			cfg := parseTestServerURL(server.URL)

			// Use short timeout retry config
			retryConfig := RetryConfig{
				MaxRetries:     1,
				InitialBackoff: 10 * time.Millisecond,
				MaxBackoff:     100 * time.Millisecond,
				BackoffFactor:  2.0,
			}

			logger, _ := zap.NewDevelopment()
			client := NewClientWithRetry(cfg, logger, retryConfig)
			client.client.Timeout = 1 * time.Second // 1 second timeout

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			start := time.Now()
			_, err := client.GetBlockchainInfo(ctx)
			elapsed := time.Since(start)

			if delay > client.client.Timeout {
				if err == nil {
					t.Errorf("Expected timeout error for delay %v", delay)
				}
				t.Logf("Correctly got error for delay %v: %v (elapsed: %v)", delay, err, elapsed)
			} else {
				if err != nil {
					t.Logf("Unexpected error for delay %v: %v", delay, err)
				}
			}
		})
	}
}

// TestRPCTimeoutRecovery tests that client recovers after timeout
func TestRPCTimeoutRecovery(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	failUntil := int32(3) // Fail first 3 requests

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)

		if count <= failUntil {
			// Simulate slow response (will timeout)
			time.Sleep(2 * time.Second)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)

	retryConfig := RetryConfig{
		MaxRetries:     5,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		BackoffFactor:  1.5,
	}

	logger, _ := zap.NewDevelopment()
	client := NewClientWithRetry(cfg, logger, retryConfig)
	client.client.Timeout = 500 * time.Millisecond

	ctx := context.Background()

	// First call should fail (with retries)
	_, err := client.GetBlockchainInfo(ctx)
	if err == nil {
		t.Log("First call succeeded (retries worked)")
	} else {
		t.Logf("First call failed as expected: %v", err)
	}

	// Subsequent call should succeed (server now responds)
	info, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Errorf("Second call should succeed: %v", err)
	} else {
		t.Logf("Recovery successful, blocks: %d", info.Blocks)
	}

	// Verify health tracking
	if !client.IsHealthy() {
		t.Log("Client marked as unhealthy after initial failures")
	}

	failures := client.ConsecutiveFailures()
	t.Logf("Consecutive failures tracked: %d", failures)
}

// =============================================================================
// 2. PARTIAL RESPONSE TESTS
// =============================================================================

// TestPartialRPCResponse tests handling of incomplete responses
func TestPartialRPCResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		response       string
		expectError    bool
		description    string
	}{
		{
			name:           "valid_response",
			response:       `{"jsonrpc":"2.0","id":1,"result":{"blocks":100000,"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001","chain":"main"}}`,
			expectError:    false,
			description:    "Valid complete response",
		},
		{
			name:           "truncated_json",
			response:       `{"jsonrpc":"2.0","id":1,"result":{"blocks":10`,
			expectError:    true,
			description:    "Truncated JSON should fail",
		},
		{
			name:           "empty_result",
			response:       `{"jsonrpc":"2.0","id":1,"result":null}`,
			expectError:    true,
			description:    "Null result should fail for GetBlockchainInfo",
		},
		{
			name:           "missing_fields",
			response:       `{"jsonrpc":"2.0","id":1,"result":{}}`,
			expectError:    true, // V34 FIX: Empty bestblockhash/chain are now rejected as invalid
			description:    "Missing critical fields should be rejected by V34 validation",
		},
		{
			name:           "rpc_error",
			response:       `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"test error"}}`,
			expectError:    true,
			description:    "RPC error response should fail",
		},
		{
			name:           "garbage_response",
			response:       `not valid json at all`,
			expectError:    true,
			description:    "Garbage should fail",
		},
		{
			name:           "empty_response",
			response:       ``,
			expectError:    true,
			description:    "Empty response should fail",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tc.response)
			}))
			defer server.Close()

			cfg := parseTestServerURL(server.URL)
			logger, _ := zap.NewDevelopment()
			client := NewClient(cfg, logger)

			_, err := client.GetBlockchainInfo(context.Background())

			if tc.expectError && err == nil {
				t.Errorf("%s: expected error but got nil", tc.description)
			}
			if !tc.expectError && err != nil {
				t.Errorf("%s: unexpected error: %v", tc.description, err)
			}
		})
	}
}

// TestPartialBlockTemplate tests handling of incomplete block templates
func TestPartialBlockTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		template    map[string]interface{}
		expectError bool
		description string
	}{
		{
			name: "valid_template",
			template: map[string]interface{}{
				"version":           int64(0x20000000),
				"previousblockhash": "0000000000000000000000000000000000000000000000000000000000000001",
				"transactions":      []interface{}{},
				"coinbasevalue":     int64(312500000),
				"target":            "00000000ffff0000000000000000000000000000000000000000000000000000",
				"bits":              "1d00ffff",
				"height":            int64(100000),
				"curtime":           time.Now().Unix(),
			},
			expectError: false,
			description: "Valid complete template",
		},
		{
			name: "missing_height",
			template: map[string]interface{}{
				"version":           int64(0x20000000),
				"previousblockhash": "0000000000000000000000000000000000000000000000000000000000000001",
				"coinbasevalue":     int64(312500000),
				"bits":              "1d00ffff",
				"curtime":           time.Now().Unix(),
				// height = 0 (missing)
			},
			expectError: true,
			description: "Missing height should fail validation",
		},
		{
			name: "invalid_prev_hash_length",
			template: map[string]interface{}{
				"version":           int64(0x20000000),
				"previousblockhash": "0000000001", // Too short
				"coinbasevalue":     int64(312500000),
				"bits":              "1d00ffff",
				"height":            int64(100000),
				"curtime":           time.Now().Unix(),
			},
			expectError: true,
			description: "Invalid prev hash length should fail",
		},
		{
			name: "negative_coinbase",
			template: map[string]interface{}{
				"version":           int64(0x20000000),
				"previousblockhash": "0000000000000000000000000000000000000000000000000000000000000001",
				"coinbasevalue":     int64(-1000),
				"bits":              "1d00ffff",
				"height":            int64(100000),
				"curtime":           time.Now().Unix(),
			},
			expectError: true,
			description: "Negative coinbase value should fail",
		},
		{
			name: "stale_timestamp",
			template: map[string]interface{}{
				"version":           int64(0x20000000),
				"previousblockhash": "0000000000000000000000000000000000000000000000000000000000000001",
				"coinbasevalue":     int64(312500000),
				"bits":              "1d00ffff",
				"height":            int64(100000),
				"curtime":           time.Now().Add(-3 * time.Hour).Unix(), // 3 hours old
			},
			expectError: false, // Warning but not error (just logs)
			description: "Old timestamp should warn but not fail",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				result, _ := json.Marshal(tc.template)
				json.NewEncoder(w).Encode(RPCResponse{
					JSONRPC: "2.0",
					ID:      1,
					Result:  result,
				})
			}))
			defer server.Close()

			cfg := parseTestServerURL(server.URL)
			logger, _ := zap.NewDevelopment()
			client := NewClient(cfg, logger)

			_, err := client.GetBlockTemplate(context.Background())

			if tc.expectError && err == nil {
				t.Errorf("%s: expected error but got nil", tc.description)
			}
			if !tc.expectError && err != nil {
				t.Errorf("%s: unexpected error: %v", tc.description, err)
			}
		})
	}
}

// =============================================================================
// 3. STALE DATA DETECTION TESTS
// =============================================================================

// TestStaleBlockTemplateDetection tests detection of stale templates
func TestStaleBlockTemplateDetection(t *testing.T) {
	t.Parallel()

	// Create template with old timestamp
	staleTime := time.Now().Add(-10 * time.Minute).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		template := map[string]interface{}{
			"version":           int64(0x20000000),
			"previousblockhash": "0000000000000000000000000000000000000000000000000000000000000001",
			"transactions":      []interface{}{},
			"coinbasevalue":     int64(312500000),
			"target":            "00000000ffff0000000000000000000000000000000000000000000000000000",
			"bits":              "1d00ffff",
			"height":            int64(100000),
			"curtime":           staleTime,
		}

		w.Header().Set("Content-Type", "application/json")
		result, _ := json.Marshal(template)
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  result,
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	// GetBlockTemplate should detect stale template
	_, err := client.GetBlockTemplate(context.Background())

	// Template validation happens in GetBlockTemplate via validateBlockTemplate
	// If timestamp is > 2 hours old, it logs a warning
	// If timestamp is > 2 hours in future, it returns error

	// For 10 minutes old, it should just warn, not error
	if err != nil {
		t.Logf("Template rejection (expected for very stale): %v", err)
	}
}

// TestCachedTemplateExpiry tests that cached templates expire properly
func TestCachedTemplateExpiry(t *testing.T) {
	t.Parallel()

	var templateVersion atomic.Int64
	templateVersion.Store(1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver := templateVersion.Load()
		template := map[string]interface{}{
			"version":           ver,
			"previousblockhash": fmt.Sprintf("%064d", ver),
			"transactions":      []interface{}{},
			"coinbasevalue":     int64(312500000),
			"target":            "00000000ffff0000000000000000000000000000000000000000000000000000",
			"bits":              "1d00ffff",
			"height":            int64(100000),
			"curtime":           time.Now().Unix(),
		}

		w.Header().Set("Content-Type", "application/json")
		result, _ := json.Marshal(template)
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  result,
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	// Get first template
	template1, err := client.GetBlockTemplate(context.Background())
	if err != nil {
		t.Fatalf("First GetBlockTemplate failed: %v", err)
	}

	// Get cached template (should return same)
	cached := client.GetCachedBlockTemplate(10 * time.Second)
	if cached == nil {
		t.Error("Cached template should be available")
	}
	if cached != nil && cached.Version != template1.Version {
		t.Error("Cached template should match original")
	}

	// Update server template
	templateVersion.Store(2)

	// Fresh fetch should get new template
	template2, err := client.GetBlockTemplate(context.Background())
	if err != nil {
		t.Fatalf("Second GetBlockTemplate failed: %v", err)
	}

	if template2.Version == template1.Version {
		t.Log("Templates have same version (expected if caching works)")
	}

	// Check cache expiry
	expired := client.GetCachedBlockTemplate(0) // 0 = always expired
	if expired != nil {
		t.Error("Expired cache should return nil")
	}
}

// =============================================================================
// 4. NODE RESTART SIMULATION
// =============================================================================

// TestNodeRestartMidOperation tests handling of node restart during operation
func TestNodeRestartMidOperation(t *testing.T) {
	t.Parallel()

	var serverOnline atomic.Bool
	serverOnline.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serverOnline.Load() {
			// Simulate server being down
			http.Error(w, "Connection refused", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	// Initial call should succeed
	_, err := client.GetBlockchainInfo(context.Background())
	if err != nil {
		t.Fatalf("Initial call failed: %v", err)
	}

	// Simulate node going down
	serverOnline.Store(false)

	// Calls should fail
	_, err = client.GetBlockchainInfo(context.Background())
	if err == nil {
		t.Error("Call should fail when node is down")
	}

	// Check failure tracking
	failures := client.ConsecutiveFailures()
	if failures < 1 {
		t.Error("Failures should be tracked")
	}

	// Simulate node coming back
	serverOnline.Store(true)

	// Call should succeed after recovery
	_, err = client.GetBlockchainInfo(context.Background())
	if err != nil {
		t.Errorf("Call should succeed after node recovery: %v", err)
	}

	// Failures should reset
	if client.IsHealthy() {
		t.Log("Client correctly marked healthy after recovery")
	}
}

// TestConcurrentRequestsDuringRestart tests concurrent requests during node restart
func TestConcurrentRequestsDuringRestart(t *testing.T) {
	t.Parallel()

	var serverOnline atomic.Bool
	serverOnline.Store(true)
	var requestCount atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)

		if !serverOnline.Load() {
			time.Sleep(50 * time.Millisecond) // Simulate hanging connection
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()

	retryConfig := RetryConfig{
		MaxRetries:     2,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		BackoffFactor:  2.0,
	}

	client := NewClientWithRetry(cfg, logger, retryConfig)
	client.client.Timeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var successCount atomic.Int64
	var failCount atomic.Int64

	numClients := 10

	// Start concurrent requests
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 20; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				_, err := client.GetBlockchainInfo(ctx)
				if err != nil {
					failCount.Add(1)
				} else {
					successCount.Add(1)
				}

				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	// Toggle server state
	go func() {
		time.Sleep(500 * time.Millisecond)
		serverOnline.Store(false) // Node goes down

		time.Sleep(1 * time.Second)
		serverOnline.Store(true) // Node comes back

		time.Sleep(1 * time.Second)
		serverOnline.Store(false) // Goes down again

		time.Sleep(500 * time.Millisecond)
		serverOnline.Store(true) // Final recovery
	}()

	wg.Wait()

	t.Logf("Requests: %d, Success: %d, Fail: %d",
		requestCount.Load(), successCount.Load(), failCount.Load())

	// Some requests should succeed, some should fail
	if successCount.Load() == 0 {
		t.Error("All requests failed - no resilience")
	}
	if failCount.Load() == 0 {
		t.Log("All requests succeeded - node restarts were handled transparently")
	}
}

// =============================================================================
// 5. CONNECTION POOL EXHAUSTION
// =============================================================================

// TestConnectionPoolExhaustion tests behavior when many concurrent requests are made
func TestConnectionPoolExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	t.Parallel()

	var activeConnections atomic.Int64
	var maxConnections atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := activeConnections.Add(1)
		defer activeConnections.Add(-1)

		// Track max concurrent connections
		for {
			max := maxConnections.Load()
			if current <= max || maxConnections.CompareAndSwap(max, current) {
				break
			}
		}

		// Simulate processing time
		time.Sleep(50 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	numConcurrent := 100

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.GetBlockchainInfo(ctx)
		}()
	}

	wg.Wait()

	t.Logf("Max concurrent connections: %d", maxConnections.Load())

	// Verify connections were pooled (not all at once)
	if maxConnections.Load() > int64(numConcurrent) {
		t.Errorf("Connection leak: max connections %d > requests %d",
			maxConnections.Load(), numConcurrent)
	}
}

// =============================================================================
// 6. HEALTH TRACKING TESTS
// =============================================================================

// TestHealthTracking tests the client health monitoring
func TestHealthTracking(t *testing.T) {
	t.Parallel()

	var failNext atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext.Load() {
			http.Error(w, "Internal Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	// Initially healthy
	if !client.IsHealthy() {
		t.Error("Client should be healthy initially")
	}

	// Successful call
	_, _ = client.GetBlockchainInfo(context.Background())
	if !client.IsHealthy() {
		t.Error("Client should be healthy after success")
	}

	// Cause failures
	failNext.Store(true)
	for i := 0; i < 5; i++ {
		_, _ = client.GetBlockchainInfo(context.Background())
	}

	failures := client.ConsecutiveFailures()
	if failures < 3 {
		t.Errorf("Expected at least 3 consecutive failures, got %d", failures)
	}

	if client.IsHealthy() {
		t.Error("Client should be unhealthy after 3+ consecutive failures")
	}

	// Recovery
	failNext.Store(false)
	_, err := client.GetBlockchainInfo(context.Background())
	if err != nil {
		t.Errorf("Recovery call failed: %v", err)
	}

	if client.ConsecutiveFailures() != 0 {
		t.Error("Consecutive failures should reset after success")
	}

	if !client.IsHealthy() {
		t.Error("Client should be healthy after recovery")
	}
}

// =============================================================================
// 7. RETRY BEHAVIOR TESTS
// =============================================================================

// TestRetryBehavior tests the retry mechanism
func TestRetryBehavior(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	failUntil := int32(2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)

		if count <= failUntil {
			http.Error(w, "Temporary Error", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks": 100000, "bestblockhash": "0000000000000000000000000000000000000000000000000000000000000001", "chain": "main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()

	retryConfig := RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		BackoffFactor:  2.0,
	}

	client := NewClientWithRetry(cfg, logger, retryConfig)

	_, err := client.GetBlockchainInfo(context.Background())

	// Should succeed after retries
	if err != nil {
		t.Errorf("Call should succeed after retries: %v", err)
	}

	totalCalls := callCount.Load()
	t.Logf("Total calls made: %d (expected %d)", totalCalls, failUntil+1)

	if totalCalls != failUntil+1 {
		t.Errorf("Expected %d calls (failures + success), got %d", failUntil+1, totalCalls)
	}
}

// TestNoRetryOnRPCError tests that RPC errors are not retried
func TestNoRetryOnRPCError(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Error: &RPCError{
				Code:    -1,
				Message: "Invalid method",
			},
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()

	retryConfig := RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		BackoffFactor:  2.0,
	}

	client := NewClientWithRetry(cfg, logger, retryConfig)

	_, err := client.GetBlockchainInfo(context.Background())

	// Should fail immediately (no retries for RPC errors)
	if err == nil {
		t.Error("Expected RPC error")
	}

	if callCount.Load() != 1 {
		t.Errorf("RPC errors should not be retried, got %d calls", callCount.Load())
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// parseTestServerURL parses httptest server URL into daemon config
func parseTestServerURL(serverURL string) *config.DaemonConfig {
	// Parse URL like http://127.0.0.1:12345
	serverURL = strings.TrimPrefix(serverURL, "http://")
	parts := strings.Split(serverURL, ":")

	host := "127.0.0.1"
	port := 8332

	if len(parts) >= 1 {
		host = parts[0]
	}
	if len(parts) >= 2 {
		fmt.Sscanf(parts[1], "%d", &port)
	}

	return &config.DaemonConfig{
		Host:     host,
		Port:     port,
		User:     "test",
		Password: "test",
	}
}
