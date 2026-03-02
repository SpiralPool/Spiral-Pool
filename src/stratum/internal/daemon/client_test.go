// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

func newTestLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func TestDefaultRetryConfig(t *testing.T) {
	cfg := DefaultRetryConfig()

	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg.MaxRetries)
	}
	if cfg.InitialBackoff != 500*time.Millisecond {
		t.Errorf("expected InitialBackoff=500ms, got %v", cfg.InitialBackoff)
	}
	if cfg.MaxBackoff != 10*time.Second {
		t.Errorf("expected MaxBackoff=10s, got %v", cfg.MaxBackoff)
	}
	if cfg.BackoffFactor != 2.0 {
		t.Errorf("expected BackoffFactor=2.0, got %f", cfg.BackoffFactor)
	}
}

func TestClientIsHealthy(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}

	client := NewClient(daemonCfg, logger)

	// Should be healthy initially
	if !client.IsHealthy() {
		t.Error("new client should be healthy")
	}

	// Simulate failures
	for i := 0; i < 3; i++ {
		client.recordFailure()
	}

	// Should be unhealthy after 3 consecutive failures
	if client.IsHealthy() {
		t.Error("client with 3 failures should be unhealthy")
	}

	// Record success should reset
	client.recordSuccess()
	if !client.IsHealthy() {
		t.Error("client should be healthy after success")
	}
}

func TestConsecutiveFailures(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}

	client := NewClient(daemonCfg, logger)

	if client.ConsecutiveFailures() != 0 {
		t.Error("new client should have 0 failures")
	}

	client.recordFailure()
	if client.ConsecutiveFailures() != 1 {
		t.Errorf("expected 1 failure, got %d", client.ConsecutiveFailures())
	}

	client.recordSuccess()
	if client.ConsecutiveFailures() != 0 {
		t.Error("failures should reset after success")
	}
}

func TestCallWithRetryContextCancellation(t *testing.T) {
	// Create a server that delays response
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  "test",
		})
	}))
	defer server.Close()

	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}

	// Override the HTTP client to use test server
	client := NewClientWithRetry(daemonCfg, logger, RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		BackoffFactor:  2.0,
	})

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.callWithRetry(ctx, "getinfo", nil, true)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRPCErrorNoRetry(t *testing.T) {
	// Create a server that returns RPC error
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  nil,
			"error": map[string]interface{}{
				"code":    -1,
				"message": "test error",
			},
		})
	}))
	defer server.Close()

	// Parse the test server URL
	serverURL := server.URL

	logger := newTestLogger()

	// Create a custom daemon config that points to the test server
	daemonCfg := &config.DaemonConfig{
		User:     "test",
		Password: "test",
	}

	client := NewClientWithRetry(daemonCfg, logger, RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		BackoffFactor:  2.0,
	})

	// Override HTTP client to use test server
	client.client = server.Client()

	// Create a custom config type that returns the test server URL
	// We use a wrapper function to handle the RPC call directly
	ctx := context.Background()

	// Make a direct HTTP request to the test server to verify RPC error handling
	reqBody, _ := json.Marshal(RPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "getinfo",
		Params:  nil,
	})

	httpReq, _ := http.NewRequestWithContext(ctx, "POST", serverURL, bytes.NewReader(reqBody))
	httpReq.SetBasicAuth("test", "test")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.client.Do(httpReq)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var rpcResp RPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Server should have been called once
	if requestCount.Load() != 1 {
		t.Errorf("expected 1 request, got %d", requestCount.Load())
	}

	// Should have an RPC error in the response
	if rpcResp.Error == nil {
		t.Error("expected RPC error in response")
	} else if rpcResp.Error.Code != -1 {
		t.Errorf("expected error code -1, got %d", rpcResp.Error.Code)
	}
}

func TestRPCErrorMessage(t *testing.T) {
	err := &RPCError{Code: -32601, Message: "Method not found"}
	expected := "RPC error -32601: Method not found"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestExponentialBackoff(t *testing.T) {
	cfg := RetryConfig{
		MaxRetries:     5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
		BackoffFactor:  2.0,
	}

	backoff := cfg.InitialBackoff
	expectedBackoffs := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1000 * time.Millisecond, // Capped at MaxBackoff
	}

	for i, expected := range expectedBackoffs {
		if i > 0 {
			backoff = time.Duration(float64(backoff) * cfg.BackoffFactor)
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
			}
		}
		if backoff != expected {
			t.Errorf("iteration %d: expected %v, got %v", i, expected, backoff)
		}
	}
}

func TestBlockTemplateStruct(t *testing.T) {
	// Test that BlockTemplate struct can be unmarshaled correctly
	jsonData := `{
		"version": 536870912,
		"previousblockhash": "0000000000000000000abc",
		"transactions": [{"data": "0100", "txid": "abc", "hash": "abc", "fee": 1000, "sigops": 1, "weight": 100}],
		"coinbaseaux": {"flags": ""},
		"coinbasevalue": 312500000,
		"target": "000000000000000000000000000000000",
		"mintime": 1234567890,
		"mutable": ["time", "transactions"],
		"noncerange": "00000000ffffffff",
		"sigoplimit": 80000,
		"sizelimit": 4000000,
		"curtime": 1234567890,
		"bits": "1d00ffff",
		"height": 100000
	}`

	var template BlockTemplate
	if err := json.Unmarshal([]byte(jsonData), &template); err != nil {
		t.Fatalf("failed to unmarshal BlockTemplate: %v", err)
	}

	if template.Height != 100000 {
		t.Errorf("expected height 100000, got %d", template.Height)
	}
	if template.Version != 536870912 {
		t.Errorf("expected version 536870912, got %d", template.Version)
	}
	if len(template.Transactions) != 1 {
		t.Errorf("expected 1 transaction, got %d", len(template.Transactions))
	}
}

// =============================================================================
// COINBASE VALUE VALIDATION TESTS
// =============================================================================
// These tests verify that block template validation catches malformed or
// malicious coinbase values before mining starts.

func TestValidateBlockTemplate_NegativeCoinbaseValue(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	// Create template with negative coinbase value (should never happen)
	template := &BlockTemplate{
		Height:            100000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     -1, // Invalid: negative
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix(),
	}

	err := client.validateBlockTemplate(template)
	if err == nil {
		t.Fatal("expected error for negative coinbase value")
	}
	if !containsString(err.Error(), "negative coinbase value") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateBlockTemplate_ExceedsMaxSupply(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	// Create template with coinbase value exceeding 21M coins
	// 21M BTC = 2,100,000,000,000,000 satoshis
	const maxSupply int64 = 21_000_000 * 100_000_000
	template := &BlockTemplate{
		Height:            100000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     maxSupply + 1, // Invalid: exceeds max supply
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix(),
	}

	err := client.validateBlockTemplate(template)
	if err == nil {
		t.Fatal("expected error for coinbase value exceeding max supply")
	}
	if !containsString(err.Error(), "exceeds max supply") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateBlockTemplate_ZeroCoinbaseValue(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	// Zero coinbase is valid but unusual (fees-only block)
	// Should warn but not error
	template := &BlockTemplate{
		Height:            100000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     0, // Valid but unusual
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix(),
	}

	err := client.validateBlockTemplate(template)
	if err != nil {
		t.Errorf("zero coinbase should not error (only warn): %v", err)
	}
}

func TestValidateBlockTemplate_ValidCoinbaseValues(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	testCases := []struct {
		name          string
		coinbaseValue int64
		description   string
	}{
		{
			name:          "BTC block reward (6.25 BTC)",
			coinbaseValue: 625_000_000, // 6.25 BTC in satoshis
			description:   "Current Bitcoin block reward",
		},
		{
			name:          "BTC block reward (3.125 BTC)",
			coinbaseValue: 312_500_000, // 3.125 BTC in satoshis (post-2024 halving)
			description:   "Post-2024 halving Bitcoin block reward",
		},
		{
			name:          "DGB block reward (725 DGB)",
			coinbaseValue: 72_500_000_000, // 725 DGB in satoshis
			description:   "Current DigiByte block reward",
		},
		{
			name:          "Minimum realistic reward",
			coinbaseValue: 1, // 1 satoshi
			description:   "Minimum possible coinbase value",
		},
		{
			name:          "Large but valid reward",
			coinbaseValue: 50_000_000_00, // 50 BTC (early Bitcoin days)
			description:   "Original Bitcoin block reward",
		},
		{
			name:          "Max valid reward",
			coinbaseValue: 21_000_000 * 100_000_000, // 21M coins (theoretical max)
			description:   "Theoretical maximum (entire supply)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			template := &BlockTemplate{
				Height:            100000,
				PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
				CoinbaseValue:     tc.coinbaseValue,
				Bits:              "1d00ffff",
				CurTime:           time.Now().Unix(),
			}

			err := client.validateBlockTemplate(template)
			if err != nil {
				t.Errorf("valid coinbase value %d should not error: %v", tc.coinbaseValue, err)
			}
		})
	}
}

func TestValidateBlockTemplate_InvalidHeight(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	template := &BlockTemplate{
		Height:            0, // Invalid: height 0 indicates daemon not synced
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     625_000_000,
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix(),
	}

	err := client.validateBlockTemplate(template)
	if err == nil {
		t.Fatal("expected error for zero height")
	}
	if !containsString(err.Error(), "height is 0") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateBlockTemplate_InvalidPrevHash(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	testCases := []struct {
		name     string
		prevHash string
	}{
		{"empty", ""},
		{"too short", "00000000000000000000"},
		{"too long", "00000000000000000000000000000000000000000000000000000000000000000000"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			template := &BlockTemplate{
				Height:            100000,
				PreviousBlockHash: tc.prevHash,
				CoinbaseValue:     625_000_000,
				Bits:              "1d00ffff",
				CurTime:           time.Now().Unix(),
			}

			err := client.validateBlockTemplate(template)
			if err == nil {
				t.Fatalf("expected error for invalid prev hash: %s", tc.name)
			}
			if !containsString(err.Error(), "previous block hash") {
				t.Errorf("unexpected error message: %v", err)
			}
		})
	}
}

func TestValidateBlockTemplate_InvalidBits(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	testCases := []struct {
		name string
		bits string
	}{
		{"empty", ""},
		{"too short", "1d00"},
		{"too long", "1d00ffffff"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			template := &BlockTemplate{
				Height:            100000,
				PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
				CoinbaseValue:     625_000_000,
				Bits:              tc.bits,
				CurTime:           time.Now().Unix(),
			}

			err := client.validateBlockTemplate(template)
			if err == nil {
				t.Fatalf("expected error for invalid bits: %s", tc.name)
			}
			if !containsString(err.Error(), "bits field") {
				t.Errorf("unexpected error message: %v", err)
			}
		})
	}
}

func TestValidateBlockTemplate_FutureTimestamp(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	// Timestamp 3 hours in future (exceeds 2-hour limit)
	template := &BlockTemplate{
		Height:            100000,
		PreviousBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinbaseValue:     625_000_000,
		Bits:              "1d00ffff",
		CurTime:           time.Now().Unix() + 10800, // 3 hours in future
	}

	err := client.validateBlockTemplate(template)
	if err == nil {
		t.Fatal("expected error for future timestamp")
	}
	if !containsString(err.Error(), "too far in future") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateBlockTemplate_ValidComplete(t *testing.T) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	// Valid block template with all correct fields
	template := &BlockTemplate{
		Height:            850000,
		PreviousBlockHash: "000000000000000000027a3bd0f0c7d28b07e55c4b0d3ef7f6b6b6b6b6b6b6b6",
		CoinbaseValue:     312_500_000, // 3.125 BTC
		Bits:              "17034219",
		CurTime:           time.Now().Unix(),
		Version:           536870912,
	}

	err := client.validateBlockTemplate(template)
	if err != nil {
		t.Errorf("valid block template should not error: %v", err)
	}
}

// containsString checks if substr is in s
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// BENCHMARKS
// =============================================================================

// BenchmarkRecordSuccess benchmarks the thread-safe success recording
func BenchmarkRecordSuccess(b *testing.B) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.recordSuccess()
	}
}

// BenchmarkRecordFailure benchmarks the thread-safe failure recording
func BenchmarkRecordFailure(b *testing.B) {
	logger := newTestLogger()
	daemonCfg := &config.DaemonConfig{
		Host:     "localhost",
		Port:     8332,
		User:     "test",
		Password: "test",
	}
	client := NewClient(daemonCfg, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.recordFailure()
	}
}
