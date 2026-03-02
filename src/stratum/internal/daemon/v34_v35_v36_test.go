// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build !nozmq

// Package daemon - V-audit fix tests for V34, V35, and V36.
//
// V34: BestBlockHash/Chain validation in GetBlockchainInfo (client.go:516-525)
// V35: Height regression detection via atomic.Uint64 tracking on Client
// V36: ZMQ deduplication ring buffer in ZMQListener.isDuplicateBlock
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// V34 — BestBlockHash/Chain validation in GetBlockchainInfo
// =============================================================================

// TestV34_EmptyBestBlockHash verifies that GetBlockchainInfo rejects responses
// with a missing or empty bestblockhash field.
func TestV34_EmptyBestBlockHash(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks":100000,"chain":"main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	_, err := client.GetBlockchainInfo(context.Background())
	if err == nil {
		t.Fatal("expected error for empty bestblockhash, got nil")
	}
	if !strings.Contains(err.Error(), "empty bestblockhash") {
		t.Errorf("error should contain 'empty bestblockhash', got: %v", err)
	}
}

// TestV34_EmptyChain verifies that GetBlockchainInfo rejects responses
// with a missing or empty chain field.
func TestV34_EmptyChain(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks":100000,"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	_, err := client.GetBlockchainInfo(context.Background())
	if err == nil {
		t.Fatal("expected error for empty chain, got nil")
	}
	if !strings.Contains(err.Error(), "empty chain") {
		t.Errorf("error should contain 'empty chain', got: %v", err)
	}
}

// TestV34_ValidResponse verifies that GetBlockchainInfo succeeds when all
// critical fields are present and correctly populated.
func TestV34_ValidResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks":100000,"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001","chain":"main"}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	info, err := client.GetBlockchainInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error for valid response: %v", err)
	}
	if info.Blocks != 100000 {
		t.Errorf("expected Blocks=100000, got %d", info.Blocks)
	}
	if info.BestBlockHash != "0000000000000000000000000000000000000000000000000000000000000001" {
		t.Errorf("unexpected BestBlockHash: %s", info.BestBlockHash)
	}
	if info.Chain != "main" {
		t.Errorf("expected Chain='main', got %q", info.Chain)
	}
}

// TestV34_BothEmpty verifies that when both bestblockhash and chain are missing,
// the bestblockhash check fires first (it comes before chain in the code path).
func TestV34_BothEmpty(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"blocks":100000}`),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	_, err := client.GetBlockchainInfo(context.Background())
	if err == nil {
		t.Fatal("expected error when both fields are empty, got nil")
	}
	// bestblockhash check (line 520) fires before chain check (line 523)
	if !strings.Contains(err.Error(), "empty bestblockhash") {
		t.Errorf("expected bestblockhash error to fire first, got: %v", err)
	}
}

// =============================================================================
// V35 — Height regression detection
// =============================================================================

// TestV35_HeightRegression verifies that a large height regression (>10 blocks)
// is detected but does not cause an error return. The fix only logs; it does
// not reject the response.
func TestV35_HeightRegression(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)

		var height int
		if count == 1 {
			height = 100000
		} else {
			height = 99980 // regression of 20, > 10 threshold
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result: json.RawMessage(fmt.Sprintf(
				`{"blocks":%d,"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001","chain":"main"}`,
				height,
			)),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	ctx := context.Background()

	// First call: establishes height 100000
	info1, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if info1.Blocks != 100000 {
		t.Fatalf("expected first height=100000, got %d", info1.Blocks)
	}

	// Second call: returns height 99980 (regression of 20)
	// V35 logs the regression but does NOT return an error
	info2, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("second call should succeed (V35 logs but does not error): %v", err)
	}
	if info2.Blocks != 99980 {
		t.Fatalf("expected second height=99980, got %d", info2.Blocks)
	}
}

// TestV35_NormalProgression verifies that normal height increases succeed
// without any issues.
func TestV35_NormalProgression(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)

		var height int
		if count == 1 {
			height = 100000
		} else {
			height = 100001
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result: json.RawMessage(fmt.Sprintf(
				`{"blocks":%d,"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001","chain":"main"}`,
				height,
			)),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	ctx := context.Background()

	info1, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if info1.Blocks != 100000 {
		t.Fatalf("expected first height=100000, got %d", info1.Blocks)
	}

	info2, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if info2.Blocks != 100001 {
		t.Fatalf("expected second height=100001, got %d", info2.Blocks)
	}
}

// TestV35_SmallReorg verifies that a small height regression (<=10 blocks,
// typical reorg) succeeds without error.
func TestV35_SmallReorg(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)

		var height int
		if count == 1 {
			height = 100000
		} else {
			height = 99995 // regression of 5, within threshold
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result: json.RawMessage(fmt.Sprintf(
				`{"blocks":%d,"bestblockhash":"0000000000000000000000000000000000000000000000000000000000000001","chain":"main"}`,
				height,
			)),
		})
	}))
	defer server.Close()

	cfg := parseTestServerURL(server.URL)
	logger, _ := zap.NewDevelopment()
	client := NewClient(cfg, logger)

	ctx := context.Background()

	info1, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if info1.Blocks != 100000 {
		t.Fatalf("expected first height=100000, got %d", info1.Blocks)
	}

	info2, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("second call should succeed (small reorg within threshold): %v", err)
	}
	if info2.Blocks != 99995 {
		t.Fatalf("expected second height=99995, got %d", info2.Blocks)
	}
}

// =============================================================================
// V36 — ZMQ deduplication ring buffer
// =============================================================================

// TestV36_DuplicateDetection verifies that calling isDuplicateBlock with the
// same hash twice returns false on first call and true on second call.
func TestV36_DuplicateDetection(t *testing.T) {
	t.Parallel()

	z := &ZMQListener{}

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// First call: not a duplicate
	if z.isDuplicateBlock(hash) {
		t.Error("first call should return false (not a duplicate)")
	}

	// Second call: duplicate
	if !z.isDuplicateBlock(hash) {
		t.Error("second call should return true (duplicate)")
	}
}

// TestV36_RingBufferEviction verifies that the ring buffer evicts the oldest
// entry when more than 8 unique hashes are added.
func TestV36_RingBufferEviction(t *testing.T) {
	t.Parallel()

	z := &ZMQListener{}

	// Add 9 unique hashes (ring buffer holds 8)
	hashes := make([]string, 9)
	for i := 0; i < 9; i++ {
		hashes[i] = fmt.Sprintf("%064x", i+1)
	}

	for _, h := range hashes {
		if z.isDuplicateBlock(h) {
			t.Fatalf("first insertion of %s should not be a duplicate", h[:8])
		}
	}

	// The first hash should have been evicted (ring buffer is size 8,
	// we wrote 9 entries, so index 0 was overwritten by hash #9)
	if z.isDuplicateBlock(hashes[0]) {
		t.Error("first hash should have been evicted and not detected as duplicate")
	}

	// After the 9th insert, idx=1. Re-adding hash[0] above wrote at idx=1
	// (overwriting hash[1]) and advanced idx to 2. So hash[1] is gone but
	// hash[2] at index 2 is untouched and should still be detected as duplicate.
	if !z.isDuplicateBlock(hashes[2]) {
		t.Error("hash at index 2 should still be in the buffer (duplicate)")
	}
}

// TestV36_EmptyHash verifies that empty strings are handled correctly.
// An empty hash should not match the zero-value empty slots in the ring buffer
// because the isDuplicateBlock check skips entries where h == "" (line 217).
// However, once stored, a subsequent empty-string call should find it.
func TestV36_EmptyHash(t *testing.T) {
	t.Parallel()

	z := &ZMQListener{}

	// First call with empty string: the ring buffer slots are all "",
	// but the check at line 217 is `if h != "" && h == hashHex`,
	// so empty slots do NOT match an empty hashHex.
	// The empty string is then stored in the buffer.
	first := z.isDuplicateBlock("")
	if first {
		t.Error("first call with empty string should return false (empty slots are skipped)")
	}

	// Second call: the stored "" entry has h == "", but the check
	// requires h != "" to match, so it will NOT match.
	// The empty string gets stored again at the next position.
	second := z.isDuplicateBlock("")
	// Per the code: `if h != "" && h == hashHex` -- empty h is always skipped.
	// So empty string will NEVER be detected as duplicate by the current implementation.
	if second {
		t.Error("empty hash should not match because isDuplicateBlock skips h == \"\" entries")
	}
}

// TestV36_ConcurrentAccess launches 100 goroutines calling isDuplicateBlock
// concurrently to verify mutex safety (no panics or data races).
func TestV36_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	z := &ZMQListener{}

	var wg sync.WaitGroup
	const numGoroutines = 100

	// Use a start signal to maximize contention
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start // Wait for all goroutines to be ready

			hash := fmt.Sprintf("%064x", id%20) // 20 unique hashes, some overlap
			for j := 0; j < 50; j++ {
				_ = z.isDuplicateBlock(hash)
			}
		}(i)
	}

	// Release all goroutines at once
	close(start)

	// Wait with a timeout to detect deadlocks
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: no panics, no deadlocks
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent access test timed out (possible deadlock)")
	}
}
