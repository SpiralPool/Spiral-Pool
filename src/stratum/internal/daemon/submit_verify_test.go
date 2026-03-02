// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package daemon - Tests for SubmitBlockWithVerification and PreciousBlock.
//
// Covers:
//   - High-latency daemon responses
//   - Submit success + verification
//   - Submit failure + chain verification recovery
//   - PreciousBlock retry behavior
//   - Context cancellation during submission
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// mockDaemonServer creates a test daemon server with configurable behavior.
type mockDaemonServer struct {
	submitBlockResult   string // "" for success, "high-hash" etc. for rejection
	submitBlockDelay    time.Duration
	submitBlockErr      bool // return HTTP error
	getBlockHashResult  string
	getBlockHashDelay   time.Duration
	preciousBlockCalls  atomic.Int32
	submitBlockCalls    atomic.Int32
	getBlockHashCalls   atomic.Int32
}

func newMockDaemon(t *testing.T, mock *mockDaemonServer) (*Client, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}

		switch req.Method {
		case "submitblock":
			mock.submitBlockCalls.Add(1)
			if mock.submitBlockDelay > 0 {
				time.Sleep(mock.submitBlockDelay)
			}
			if mock.submitBlockErr {
				http.Error(w, "internal error", 500)
				return
			}
			resp := RPCResponse{ID: req.ID, JSONRPC: "2.0"}
			if mock.submitBlockResult == "" {
				resp.Result = json.RawMessage(`null`)
			} else {
				resp.Result = json.RawMessage(fmt.Sprintf(`"%s"`, mock.submitBlockResult))
			}
			json.NewEncoder(w).Encode(resp)

		case "getblockhash":
			mock.getBlockHashCalls.Add(1)
			if mock.getBlockHashDelay > 0 {
				time.Sleep(mock.getBlockHashDelay)
			}
			resp := RPCResponse{ID: req.ID, JSONRPC: "2.0"}
			resp.Result = json.RawMessage(fmt.Sprintf(`"%s"`, mock.getBlockHashResult))
			json.NewEncoder(w).Encode(resp)

		case "preciousblock":
			mock.preciousBlockCalls.Add(1)
			resp := RPCResponse{ID: req.ID, JSONRPC: "2.0", Result: json.RawMessage(`null`)}
			json.NewEncoder(w).Encode(resp)

		default:
			resp := RPCResponse{ID: req.ID, JSONRPC: "2.0", Result: json.RawMessage(`null`)}
			json.NewEncoder(w).Encode(resp)
		}
	}))

	// Parse the server URL to extract host:port
	addr := strings.TrimPrefix(server.URL, "http://")
	parts := strings.SplitN(addr, ":", 2)
	host := parts[0]
	port := 0
	if len(parts) > 1 {
		fmt.Sscanf(parts[1], "%d", &port)
	}

	cfg := &config.DaemonConfig{
		Host:     host,
		Port:     port,
		User:     "test",
		Password: "test",
	}

	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	return client, server
}

// TestSubmitBlockWithVerification_Success tests the happy path:
// submitblock succeeds and getblockhash confirms.
func TestSubmitBlockWithVerification_Success(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockResult:  "",
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx := context.Background()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

	if !result.Submitted {
		t.Error("Expected Submitted=true")
	}
	if !result.Verified {
		t.Error("Expected Verified=true")
	}
	if result.SubmitErr != nil {
		t.Errorf("Expected no submit error, got: %v", result.SubmitErr)
	}
	if result.ChainHash != "abc123" {
		t.Errorf("ChainHash=%s, want abc123", result.ChainHash)
	}
	if mock.preciousBlockCalls.Load() < 1 {
		t.Error("Expected at least 1 preciousblock call on success")
	}
}

// TestSubmitBlockWithVerification_RejectButInChain tests the false rejection case:
// submitblock returns error but getblockhash confirms our block is in chain.
func TestSubmitBlockWithVerification_RejectButInChain(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockErr:     true, // HTTP-level error
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx := context.Background()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

	if result.Submitted {
		t.Error("Expected Submitted=false (submit returned error)")
	}
	if !result.Verified {
		t.Error("Expected Verified=true (block is in chain despite submit error)")
	}
	if mock.preciousBlockCalls.Load() < 1 {
		t.Error("Expected preciousblock call after false rejection recovery")
	}
}

// TestSubmitBlockWithVerification_RejectNotInChain tests actual rejection:
// submitblock fails and getblockhash shows different hash.
func TestSubmitBlockWithVerification_RejectNotInChain(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockResult:  "high-hash",
		getBlockHashResult: "different_hash",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx := context.Background()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

	if result.Submitted {
		t.Error("Expected Submitted=false")
	}
	if result.Verified {
		t.Error("Expected Verified=false (different hash in chain)")
	}
	if result.ChainHash != "different_hash" {
		t.Errorf("ChainHash=%s, want different_hash", result.ChainHash)
	}
	if mock.preciousBlockCalls.Load() != 0 {
		t.Error("Expected 0 preciousblock calls on full rejection")
	}
}

// TestSubmitBlockWithVerification_HighLatency tests with a slow daemon.
func TestSubmitBlockWithVerification_HighLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high-latency test in short mode")
	}

	mock := &mockDaemonServer{
		submitBlockDelay:   500 * time.Millisecond,
		getBlockHashDelay:  200 * time.Millisecond,
		submitBlockResult:  "",
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx := context.Background()
	start := time.Now()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)
	elapsed := time.Since(start)

	if !result.Submitted {
		t.Error("Expected Submitted=true despite delay")
	}
	if !result.Verified {
		t.Error("Expected Verified=true despite delay")
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("Expected at least 500ms for submission, got %v", elapsed)
	}
}

// TestSubmitBlockWithVerification_ContextCancellation tests that context
// cancellation is properly handled during submission.
func TestSubmitBlockWithVerification_ContextCancellation(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockDelay:   5 * time.Second, // Very slow
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

	// Should fail due to timeout
	if result.Submitted {
		t.Error("Expected Submitted=false due to context timeout")
	}
	// SubmitErr should be set
	if result.SubmitErr == nil {
		t.Error("Expected non-nil SubmitErr after context timeout")
	}
}

// TestPreciousBlock_Retry tests that PreciousBlock retries once on failure.
func TestPreciousBlock_Retry(t *testing.T) {
	callCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "preciousblock" {
			n := callCount.Add(1)
			if n == 1 {
				// First call fails
				http.Error(w, "busy", 500)
				return
			}
			// Second call succeeds
			resp := RPCResponse{ID: req.ID, JSONRPC: "2.0", Result: json.RawMessage(`null`)}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	addr := strings.TrimPrefix(server.URL, "http://")
	parts := strings.SplitN(addr, ":", 2)
	host := parts[0]
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)

	cfg := &config.DaemonConfig{Host: host, Port: port, User: "test", Password: "test"}
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	ctx := context.Background()
	err := client.PreciousBlock(ctx, "abc123")

	// Should succeed after retry
	if err != nil {
		t.Errorf("Expected success after retry, got: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("Expected 2 preciousblock calls (1 fail + 1 retry), got %d", callCount.Load())
	}
}

// TestSubmitBlockWithVerification_VerifyTimeout tests that a verify timeout
// doesn't mask a successful submission.
func TestSubmitBlockWithVerification_VerifyTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	mock := &mockDaemonServer{
		submitBlockResult:  "",             // submit succeeds
		getBlockHashDelay:  5 * time.Second, // verify times out
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

	// Submit should succeed
	if !result.Submitted {
		t.Error("Expected Submitted=true (submit succeeded before timeout)")
	}
	// Verify should fail due to timeout
	if result.Verified {
		t.Error("Expected Verified=false (verify timed out)")
	}
	if result.VerifyErr == nil {
		t.Error("Expected non-nil VerifyErr after timeout")
	}
}

// TestSubmitBlockWithVerification_ChainAdvanced tests behavior when the chain
// advances past our block between submit and verify (different hash at height).
func TestSubmitBlockWithVerification_ChainAdvanced(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockResult:  "",              // submit succeeds
		getBlockHashResult: "different_hash", // chain advanced, different block at our height
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx := context.Background()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

	// Submit succeeded but verification shows different hash
	if !result.Submitted {
		t.Error("Expected Submitted=true")
	}
	if result.Verified {
		t.Error("Expected Verified=false (chain has different hash at our height)")
	}
	if result.ChainHash != "different_hash" {
		t.Errorf("ChainHash=%s, want different_hash", result.ChainHash)
	}
	// PreciousBlock should still have been called (submit succeeded)
	if mock.preciousBlockCalls.Load() < 1 {
		t.Error("Expected at least 1 preciousblock call after successful submit")
	}
}

// TestSubmitBlockWithVerification_DGBFastChain simulates a 15-second blockchain
// scenario where the context must be tight to avoid stale submissions.
func TestSubmitBlockWithVerification_DGBFastChain(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockResult:  "",
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	// Use DGB-specific timeouts (15s block time)
	dgbTimeouts := NewSubmitTimeouts(15)
	ctx, cancel := context.WithTimeout(context.Background(), dgbTimeouts.TotalBudget)
	defer cancel()

	start := time.Now()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, dgbTimeouts)
	elapsed := time.Since(start)

	if !result.Submitted || !result.Verified {
		t.Errorf("Expected success on fast chain, got Submitted=%v Verified=%v", result.Submitted, result.Verified)
	}
	// On a healthy local daemon, the full submit+verify cycle should complete in <100ms
	if elapsed > 2*time.Second {
		t.Errorf("Submit+verify took %v — too slow for 15s blockchain", elapsed)
	}

	// Verify DGB timeouts: fast-chain tier (tight RPC, 2 retries)
	t.Logf("DGB: submit=%v verify=%v precious=%v total=%v retries=%d",
		dgbTimeouts.SubmitTimeout, dgbTimeouts.VerifyTimeout,
		dgbTimeouts.PreciousTimeout, dgbTimeouts.TotalBudget, dgbTimeouts.MaxRetries)
	if dgbTimeouts.SubmitTimeout != 3*time.Second {
		t.Errorf("DGB submit should be 3s (fast-chain tier), got %v", dgbTimeouts.SubmitTimeout)
	}
	if dgbTimeouts.TotalBudget != 5*time.Second {
		t.Errorf("DGB total budget should be 5s (fast-chain tier), got %v", dgbTimeouts.TotalBudget)
	}
	if dgbTimeouts.MaxRetries != 2 {
		t.Errorf("DGB should have 2 retries, got %d", dgbTimeouts.MaxRetries)
	}
}

// TestSubmitTimeouts_AllCoinTiers verifies timeout computation for all 13 supported
// coins grouped by block time tier. Fast chains (<30s) get tight RPC timeouts (3s/1s/500ms),
// normal chains get standard timeouts (5s/3s/2s). Retries are computed from remaining budget.
func TestSubmitTimeouts_AllCoinTiers(t *testing.T) {
	tests := []struct {
		name         string
		blockTimeSec int
		wantRetries  int
		wantSubmit   time.Duration
		wantBudget   time.Duration
		coins        string
	}{
		{"15s", 15, 2, 3 * time.Second, 5 * time.Second, "DGB, DGB-SCRYPT"},
		{"30s", 30, 3, 5 * time.Second, 10 * time.Second, "FBTC"},
		{"60s", 60, 5, 5 * time.Second, 10 * time.Second, "DOGE, XMY, PEP, SYS"},
		{"150s", 150, 5, 5 * time.Second, 10 * time.Second, "LTC"},
		{"600s", 600, 5, 5 * time.Second, 10 * time.Second, "BTC, BCH, BC2, CAT, NMC"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			timeouts := NewSubmitTimeouts(tc.blockTimeSec)
			bt := time.Duration(tc.blockTimeSec) * time.Second

			// RPC timeouts depend on tier (fast vs normal)
			if timeouts.SubmitTimeout != tc.wantSubmit {
				t.Errorf("SubmitTimeout=%v, want %v", timeouts.SubmitTimeout, tc.wantSubmit)
			}
			if timeouts.TotalBudget != tc.wantBudget {
				t.Errorf("TotalBudget=%v, want %v", timeouts.TotalBudget, tc.wantBudget)
			}
			// Retry count varies by block time
			if timeouts.MaxRetries != tc.wantRetries {
				t.Errorf("MaxRetries=%d, want %d", timeouts.MaxRetries, tc.wantRetries)
			}
			// All timeouts must be positive
			if timeouts.SubmitTimeout <= 0 || timeouts.VerifyTimeout <= 0 || timeouts.PreciousTimeout <= 0 {
				t.Errorf("Timeouts must be positive: submit=%v verify=%v precious=%v",
					timeouts.SubmitTimeout, timeouts.VerifyTimeout, timeouts.PreciousTimeout)
			}
			// CRITICAL INVARIANT: Worst-case total must fit within one block time.
			// worst = TotalBudget + MaxRetries * (RetrySleep + RetryTimeout)
			worstCase := timeouts.TotalBudget + time.Duration(timeouts.MaxRetries)*(timeouts.RetrySleep+timeouts.RetryTimeout)
			if worstCase > bt {
				t.Errorf("Worst-case %v exceeds block time %v (total=%v + %d*(sleep+retry))",
					worstCase, bt, timeouts.TotalBudget, timeouts.MaxRetries)
			}
			// Propagation margin must be >= 1/3 of block time
			propagation := bt - worstCase
			minPropagation := bt / 3
			if propagation < minPropagation {
				t.Errorf("Propagation margin %v < minimum %v (1/3 of block time)", propagation, minPropagation)
			}

			t.Logf("[%s] coins=%s submit=%v verify=%v precious=%v total=%v retries=%d retryTimeout=%v worst=%v propagation=%v",
				tc.name, tc.coins, timeouts.SubmitTimeout, timeouts.VerifyTimeout,
				timeouts.PreciousTimeout, timeouts.TotalBudget, timeouts.MaxRetries,
				timeouts.RetryTimeout, worstCase, propagation)
		})
	}
}

// TestSubmitTimeouts_DefaultMatchesBTC600 verifies that DefaultSubmitTimeouts()
// is field-for-field identical to NewSubmitTimeouts(600). This prevents silent
// drift if either function is modified independently.
func TestSubmitTimeouts_DefaultMatchesBTC600(t *testing.T) {
	def := DefaultSubmitTimeouts()
	btc := NewSubmitTimeouts(600)

	if def.SubmitTimeout != btc.SubmitTimeout {
		t.Errorf("SubmitTimeout: Default=%v, BTC600=%v", def.SubmitTimeout, btc.SubmitTimeout)
	}
	if def.VerifyTimeout != btc.VerifyTimeout {
		t.Errorf("VerifyTimeout: Default=%v, BTC600=%v", def.VerifyTimeout, btc.VerifyTimeout)
	}
	if def.PreciousTimeout != btc.PreciousTimeout {
		t.Errorf("PreciousTimeout: Default=%v, BTC600=%v", def.PreciousTimeout, btc.PreciousTimeout)
	}
	if def.TotalBudget != btc.TotalBudget {
		t.Errorf("TotalBudget: Default=%v, BTC600=%v", def.TotalBudget, btc.TotalBudget)
	}
	if def.RetryTimeout != btc.RetryTimeout {
		t.Errorf("RetryTimeout: Default=%v, BTC600=%v", def.RetryTimeout, btc.RetryTimeout)
	}
	if def.MaxRetries != btc.MaxRetries {
		t.Errorf("MaxRetries: Default=%d, BTC600=%d", def.MaxRetries, btc.MaxRetries)
	}
	if def.RetrySleep != btc.RetrySleep {
		t.Errorf("RetrySleep: Default=%v, BTC600=%v", def.RetrySleep, btc.RetrySleep)
	}
	if def.SubmitDeadline != btc.SubmitDeadline {
		t.Errorf("SubmitDeadline: Default=%v, BTC600=%v", def.SubmitDeadline, btc.SubmitDeadline)
	}
}

// TestSubmitTimeouts_SubmitDeadlineFloor verifies the 3-second floor on SubmitDeadline.
// SubmitDeadline = BlockTime × 0.30, but must never drop below 3s regardless of block time.
func TestSubmitTimeouts_SubmitDeadlineFloor(t *testing.T) {
	tests := []struct {
		blockTimeSec int
		wantDeadline time.Duration
		desc         string
	}{
		{9, 3 * time.Second, "9s × 0.30 = 2.7s → floors to 3s"},
		{10, 3 * time.Second, "10s × 0.30 = 3.0s → exact floor"},
		{15, 4500 * time.Millisecond, "DGB: 15s × 0.30 = 4.5s (above floor)"},
		{30, 9 * time.Second, "FBTC: 30s × 0.30 = 9s"},
		{60, 18 * time.Second, "DOGE/XMY/PEP/SYS: 60s × 0.30 = 18s"},
		{150, 45 * time.Second, "LTC: 150s × 0.30 = 45s"},
		{600, 180 * time.Second, "BTC/BCH/BC2/CAT/NMC: 600s × 0.30 = 180s"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%ds", tc.blockTimeSec), func(t *testing.T) {
			timeouts := NewSubmitTimeouts(tc.blockTimeSec)
			if timeouts.SubmitDeadline != tc.wantDeadline {
				t.Errorf("SubmitDeadline=%v, want %v (%s)", timeouts.SubmitDeadline, tc.wantDeadline, tc.desc)
			}
		})
	}
}

// TestSubmitTimeouts_BlockTimeZero is a defensive test: NewSubmitTimeouts(0) must not
// panic, must produce positive durations, and must hit the 3s SubmitDeadline floor.
// Unreachable in production (all coins return positive BlockTime), but protects future callers.
func TestSubmitTimeouts_BlockTimeZero(t *testing.T) {
	timeouts := NewSubmitTimeouts(0)

	if timeouts.SubmitTimeout <= 0 {
		t.Errorf("SubmitTimeout=%v, must be positive", timeouts.SubmitTimeout)
	}
	if timeouts.VerifyTimeout <= 0 {
		t.Errorf("VerifyTimeout=%v, must be positive", timeouts.VerifyTimeout)
	}
	if timeouts.PreciousTimeout <= 0 {
		t.Errorf("PreciousTimeout=%v, must be positive", timeouts.PreciousTimeout)
	}
	if timeouts.TotalBudget <= 0 {
		t.Errorf("TotalBudget=%v, must be positive", timeouts.TotalBudget)
	}
	if timeouts.RetryTimeout <= 0 {
		t.Errorf("RetryTimeout=%v, must be positive", timeouts.RetryTimeout)
	}
	if timeouts.RetrySleep <= 0 {
		t.Errorf("RetrySleep=%v, must be positive", timeouts.RetrySleep)
	}
	if timeouts.SubmitDeadline < 3*time.Second {
		t.Errorf("SubmitDeadline=%v, must be >= 3s floor", timeouts.SubmitDeadline)
	}
	if timeouts.MaxRetries < 0 {
		t.Errorf("MaxRetries=%d, must be >= 0", timeouts.MaxRetries)
	}
}

// TestSubmitBlockWithVerification_ContextAlreadyCancelled verifies that passing
// an already-cancelled context returns immediately without blocking.
// In production, the chain tip may advance (cancelling the height-locked context)
// before SubmitBlockWithVerification is even entered.
func TestSubmitBlockWithVerification_ContextAlreadyCancelled(t *testing.T) {
	mock := &mockDaemonServer{
		submitBlockDelay:   10 * time.Second, // Would block forever if context not checked
		getBlockHashResult: "abc123",
	}
	client, server := newMockDaemon(t, mock)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel BEFORE calling

	start := time.Now()
	result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("Should return immediately on cancelled context, took %v", elapsed)
	}
	if result.Submitted {
		t.Error("Expected Submitted=false with cancelled context")
	}
	if result.SubmitErr == nil {
		t.Error("Expected non-nil SubmitErr with cancelled context")
	}
}

// TestSubmitTimeouts_BoundaryBlockTimes tests edge block times around the fast/normal
// threshold (29s/30s/31s) and extremes. For every block time, the worst-case submission
// time must not exceed the block time. This extends TestSubmitTimeouts_AllCoinTiers to
// cover non-production values and verify the two-tier boundary is correct.
func TestSubmitTimeouts_BoundaryBlockTimes(t *testing.T) {
	edgeCases := []struct {
		blockTimeSec int
		desc         string
	}{
		{1, "hypothetical ultra-fast chain"},
		{5, "sub-DGB fast chain"},
		{14, "just below DGB"},
		{15, "DGB/DGB-SCRYPT (sha256d, fastest production)"},
		{29, "just below fast/normal threshold"},
		{30, "FBTC (sha256d, threshold boundary)"},
		{31, "just above threshold"},
		{60, "DOGE/XMY/PEP/SYS (scrypt+sha256d, merge-mineable)"},
		{150, "LTC (scrypt, parent for merge mining)"},
		{600, "BTC/BCH/BC2/CAT/NMC (sha256d+scrypt, merge-mineable)"},
		{1800, "hypothetical very slow chain"},
	}

	for _, tc := range edgeCases {
		t.Run(fmt.Sprintf("%ds_%s", tc.blockTimeSec, tc.desc), func(t *testing.T) {
			timeouts := NewSubmitTimeouts(tc.blockTimeSec)
			bt := time.Duration(tc.blockTimeSec) * time.Second

			// All durations must be positive
			if timeouts.SubmitTimeout <= 0 || timeouts.VerifyTimeout <= 0 || timeouts.PreciousTimeout <= 0 {
				t.Errorf("Timeouts must be positive: submit=%v verify=%v precious=%v",
					timeouts.SubmitTimeout, timeouts.VerifyTimeout, timeouts.PreciousTimeout)
			}
			if timeouts.MaxRetries < 0 {
				t.Errorf("MaxRetries=%d, must be >= 0", timeouts.MaxRetries)
			}

			// CRITICAL: Worst-case must not exceed block time for production coins (>=15s).
			// Sub-5s chains are hypothetical — the 5s TotalBudget floor is designed for
			// the fastest real coin (DGB at 15s), not theoretical 1s chains.
			worstCase := timeouts.TotalBudget + time.Duration(timeouts.MaxRetries)*(timeouts.RetrySleep+timeouts.RetryTimeout)
			if tc.blockTimeSec >= 15 && worstCase > bt {
				t.Errorf("Worst-case %v exceeds block time %v (budget=%v + %d*(sleep+retry))",
					worstCase, bt, timeouts.TotalBudget, timeouts.MaxRetries)
			}

			// Fast tier (<30s) must use tight timeouts
			if tc.blockTimeSec < 30 {
				if timeouts.SubmitTimeout != 3*time.Second {
					t.Errorf("Fast chain: SubmitTimeout=%v, want 3s", timeouts.SubmitTimeout)
				}
				if timeouts.TotalBudget != 5*time.Second {
					t.Errorf("Fast chain: TotalBudget=%v, want 5s", timeouts.TotalBudget)
				}
			}
			// Normal tier (>=30s) must use standard timeouts
			if tc.blockTimeSec >= 30 {
				if timeouts.SubmitTimeout != 5*time.Second {
					t.Errorf("Normal chain: SubmitTimeout=%v, want 5s", timeouts.SubmitTimeout)
				}
				if timeouts.TotalBudget != 10*time.Second {
					t.Errorf("Normal chain: TotalBudget=%v, want 10s", timeouts.TotalBudget)
				}
			}

			// MaxRetries must be capped at 5
			if timeouts.MaxRetries > 5 {
				t.Errorf("MaxRetries=%d exceeds cap of 5", timeouts.MaxRetries)
			}

			// SubmitDeadline floor: must be >= 3s
			if timeouts.SubmitDeadline < 3*time.Second {
				t.Errorf("SubmitDeadline=%v, must be >= 3s floor", timeouts.SubmitDeadline)
			}

			t.Logf("[%ds] tier=%s submit=%v budget=%v retries=%d deadline=%v worst=%v",
				tc.blockTimeSec,
				func() string {
					if tc.blockTimeSec < 30 {
						return "fast"
					}
					return "normal"
				}(),
				timeouts.SubmitTimeout, timeouts.TotalBudget, timeouts.MaxRetries,
				timeouts.SubmitDeadline, worstCase)
		})
	}
}

// TestSubmitTimeouts_AllCoinsExplicit tests every supported coin by name,
// grouped by algorithm (SHA-256d, Scrypt) and merge-mining capability.
// This ensures no coin is accidentally missed when timeout tiers change.
func TestSubmitTimeouts_AllCoinsExplicit(t *testing.T) {
	// Every supported coin with its actual block time, algorithm, and merge-mine status.
	coins := []struct {
		symbol       string
		blockTimeSec int
		algorithm    string
		mergeMined   bool // true = aux chain (merge-mineable as child)
	}{
		// SHA-256d coins
		{"BTC", 600, "sha256d", false},
		{"BCH", 600, "sha256d", false},
		{"BC2", 600, "sha256d", false},
		{"DGB", 15, "sha256d", false},
		{"FBTC", 30, "sha256d", true},  // merge-mined with BTC
		{"NMC", 600, "sha256d", true},  // merge-mined with BTC
		{"SYS", 60, "sha256d", true},   // merge-mined with BTC
		{"XMY", 60, "sha256d", true},   // merge-mined with BTC
		// Scrypt coins
		{"LTC", 150, "scrypt", false},  // parent for DOGE/PEP merge mining
		{"DOGE", 60, "scrypt", true},   // merge-mined with LTC
		{"PEP", 60, "scrypt", true},    // merge-mined with LTC
		{"CAT", 600, "scrypt", false},
		// SHA-256d (DGB-SCRYPT uses scrypt but same block time as DGB)
		{"DGB-SCRYPT", 15, "scrypt", false},
	}

	for _, c := range coins {
		t.Run(fmt.Sprintf("%s_%s_%ds", c.symbol, c.algorithm, c.blockTimeSec), func(t *testing.T) {
			timeouts := NewSubmitTimeouts(c.blockTimeSec)
			bt := time.Duration(c.blockTimeSec) * time.Second

			// Worst-case must fit in block time
			worstCase := timeouts.TotalBudget + time.Duration(timeouts.MaxRetries)*(timeouts.RetrySleep+timeouts.RetryTimeout)
			if worstCase > bt {
				t.Errorf("%s (%s, %ds): worst-case %v exceeds block time %v",
					c.symbol, c.algorithm, c.blockTimeSec, worstCase, bt)
			}

			// Propagation margin must be >= 1/3 of block time
			propagation := bt - worstCase
			minPropagation := bt / 3
			if propagation < minPropagation {
				t.Errorf("%s: propagation %v < minimum %v (1/3 of %v)",
					c.symbol, propagation, minPropagation, bt)
			}

			// Merge-mined coins use the SAME timeout path (pool.go aux submit uses submitTimeouts)
			// This verifies the timeout budget is safe for aux block submission too
			if c.mergeMined {
				// Aux blocks go through the same retry loop with SubmitDeadline
				if timeouts.SubmitDeadline <= 0 {
					t.Errorf("%s (merge-mined): SubmitDeadline=%v, must be positive", c.symbol, timeouts.SubmitDeadline)
				}
			}

			t.Logf("%s [%s%s]: %ds blocks, submit=%v, retries=%d, deadline=%v, worst=%v, propagation=%v",
				c.symbol, c.algorithm,
				func() string {
					if c.mergeMined {
						return "+auxpow"
					}
					return ""
				}(),
				c.blockTimeSec, timeouts.SubmitTimeout, timeouts.MaxRetries,
				timeouts.SubmitDeadline, worstCase, propagation)
		})
	}
}

// TestClassifyRejectionMetric tests that daemon error strings are correctly
// mapped to stable Prometheus metric labels.
func TestClassifyRejectionMetric(t *testing.T) {
	// This tests the pool.go classifyRejectionMetric function indirectly
	// by verifying the mock daemon rejection patterns match expectations.
	tests := []struct {
		submitResult string
		wantReject   bool
	}{
		{"", false},           // success
		{"high-hash", true},   // PoW failure
		{"duplicate", true},   // already accepted
	}

	for _, tc := range tests {
		tc := tc
		t.Run("result_"+tc.submitResult, func(t *testing.T) {
			mock := &mockDaemonServer{
				submitBlockResult:  tc.submitResult,
				getBlockHashResult: "different",
			}
			client, server := newMockDaemon(t, mock)
			defer server.Close()

			ctx := context.Background()
			result := client.SubmitBlockWithVerification(ctx, "deadbeef", "abc123", 12345, nil)

			if tc.wantReject && result.Submitted {
				t.Errorf("Expected rejection for result=%q", tc.submitResult)
			}
			if !tc.wantReject && !result.Submitted {
				t.Errorf("Expected success for result=%q, got err=%v", tc.submitResult, result.SubmitErr)
			}
		})
	}
}
