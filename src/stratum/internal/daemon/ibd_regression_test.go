// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Regression tests for Initial Block Download (IBD) state handling.
//
// Background: ecashd (XEC) restarted mid-sync after OOM — MemoryMax=4G with
// dbcache=2048 was too tight for a 948k-block chain. The node returned
// initialblockdownload=true at 451977/948279 blocks (~47%) for hours. These
// tests pin the two behaviours that must hold regardless of sync state:
//
//  1. GetBlockchainInfo correctly parses and surfaces the IBD flag and all
//     associated progress fields (blocks, headers, verificationprogress, pruned).
//
//  2. SubmitBlockWithVerification succeeds during IBD — the submit pipeline
//     (submitblock → preciousblock → getblockhash) must not be gated on sync
//     completion. A found block must be credited even when the node is mid-sync.

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// newClientForServer builds a Client aimed at srv using the same pattern as
// newMockDaemon, for tests that need a custom RPC handler.
func newClientForServer(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	addr := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := strings.Cut(addr, ":")
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	cfg := &config.DaemonConfig{Host: host, Port: port, User: "test", Password: "test"}
	logger, _ := zap.NewDevelopment()
	return NewClient(cfg, logger)
}

// staticRPCServer returns a HandlerFunc that serves fixed JSON results keyed by
// RPC method name. Methods absent from the map return a null result.
func staticRPCServer(methods map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := RPCResponse{ID: req.ID, JSONRPC: "2.0"}
		if raw, ok := methods[req.Method]; ok {
			resp.Result = json.RawMessage(raw)
		} else {
			resp.Result = json.RawMessage(`null`)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// TestGetBlockchainInfo_IBD verifies that GetBlockchainInfo correctly parses
// initialblockdownload=true and all sync progress fields.
//
// The values mirror the real XEC node state captured during the OOM incident:
// 451977 blocks synced out of 948279 headers at ~47.66% verification progress.
func TestGetBlockchainInfo_IBD(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(staticRPCServer(map[string]string{
		"getblockchaininfo": `{
			"chain":                "main",
			"blocks":               451977,
			"headers":              948279,
			"bestblockhash":        "000000000000000000001abc451977def",
			"difficulty":           1.0,
			"mediantime":           1746000000,
			"verificationprogress": 0.4766,
			"initialblockdownload": true,
			"pruned":               true
		}`,
	}))
	defer srv.Close()

	info, err := newClientForServer(t, srv).GetBlockchainInfo(context.Background())
	if err != nil {
		t.Fatalf("GetBlockchainInfo: %v", err)
	}

	if !info.InitialBlockDownload {
		t.Error("InitialBlockDownload: got false, want true")
	}
	if info.Blocks != 451977 {
		t.Errorf("Blocks: got %d, want 451977", info.Blocks)
	}
	if info.Headers != 948279 {
		t.Errorf("Headers: got %d, want 948279", info.Headers)
	}
	if info.Headers-info.Blocks != 496302 {
		t.Errorf("remaining blocks: got %d, want 496302", info.Headers-info.Blocks)
	}
	if info.VerificationProgress < 0.47 || info.VerificationProgress > 0.48 {
		t.Errorf("VerificationProgress: got %f, want ~0.4766", info.VerificationProgress)
	}
	if !info.Pruned {
		t.Error("Pruned: got false, want true")
	}
}

// TestGetBlockchainInfo_FullySynced verifies that GetBlockchainInfo correctly
// reports InitialBlockDownload=false once the node reaches tip.
func TestGetBlockchainInfo_FullySynced(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(staticRPCServer(map[string]string{
		"getblockchaininfo": `{
			"chain":                "main",
			"blocks":               948279,
			"headers":              948279,
			"bestblockhash":        "000000000000000000009abc948279def",
			"difficulty":           2.5e13,
			"mediantime":           1746100000,
			"verificationprogress": 0.9999,
			"initialblockdownload": false,
			"pruned":               true
		}`,
	}))
	defer srv.Close()

	info, err := newClientForServer(t, srv).GetBlockchainInfo(context.Background())
	if err != nil {
		t.Fatalf("GetBlockchainInfo: %v", err)
	}

	if info.InitialBlockDownload {
		t.Error("InitialBlockDownload: got true, want false")
	}
	if info.Blocks != info.Headers {
		t.Errorf("fully synced node must have blocks==headers, got blocks=%d headers=%d",
			info.Blocks, info.Headers)
	}
}

// TestSubmitBlockWithVerification_NodeInIBD verifies that block submission
// succeeds regardless of the node's IBD state.
//
// The submit pipeline (submitblock → preciousblock → getblockhash) must not be
// gated on initialblockdownload. During XEC mid-sync recovery, a found block
// must still be submitted and credited even when the node reports IBD=true.
func TestSubmitBlockWithVerification_NodeInIBD(t *testing.T) {
	t.Parallel()

	const (
		blockHex  = "0000deadbeef"
		blockHash = "000000000000000000001abc451977def"
		height    = uint64(451977)
	)

	srv := httptest.NewServer(staticRPCServer(map[string]string{
		"getblockchaininfo": `{
			"chain":                "main",
			"blocks":               451977,
			"headers":              948279,
			"bestblockhash":        "000000000000000000001abc451977def",
			"difficulty":           1.0,
			"mediantime":           1746000000,
			"verificationprogress": 0.4766,
			"initialblockdownload": true,
			"pruned":               true
		}`,
		"submitblock":   `null`,
		"preciousblock": `null`,
		"getblockhash":  `"000000000000000000001abc451977def"`,
	}))
	defer srv.Close()

	client := newClientForServer(t, srv)

	// Precondition: confirm the mock presents IBD state.
	info, err := client.GetBlockchainInfo(context.Background())
	if err != nil {
		t.Fatalf("GetBlockchainInfo: %v", err)
	}
	if !info.InitialBlockDownload {
		t.Fatal("test precondition failed: node is not reporting IBD state")
	}

	// XEC block time matches Bitcoin: 600s.
	result := client.SubmitBlockWithVerification(
		context.Background(), blockHex, blockHash, height, NewSubmitTimeouts(600),
	)

	if result.SubmitErr != nil {
		t.Errorf("SubmitBlock failed during IBD: %v", result.SubmitErr)
	}
	if !result.Submitted {
		t.Error("Submitted: got false, want true")
	}
	if result.VerifyErr != nil {
		t.Errorf("GetBlockHash failed during IBD: %v", result.VerifyErr)
	}
	if !result.Verified {
		t.Errorf("Verified: got false, want true (chain hash: %q)", result.ChainHash)
	}
	if result.ChainHash != blockHash {
		t.Errorf("ChainHash: got %q, want %q", result.ChainHash, blockHash)
	}
}
