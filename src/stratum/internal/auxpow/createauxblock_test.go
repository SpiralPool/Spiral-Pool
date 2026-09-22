// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// fakeAuxNode is an aux-chain RPC server that records every call. Like the real
// aux daemons it answers createauxblock; any other method is "not found".
type fakeAuxNode struct {
	mu    sync.Mutex
	calls []daemon.RPCRequest
}

func newFakeAuxNode(t *testing.T) (*fakeAuxNode, *daemon.Client) {
	t.Helper()
	node := &fakeAuxNode{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req daemon.RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		node.mu.Lock()
		node.calls = append(node.calls, req)
		node.mu.Unlock()

		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "createauxblock" {
			resp["result"] = map[string]interface{}{
				"hash":              strings.Repeat("ab", 32),
				"chainid":           98,
				"previousblockhash": strings.Repeat("cd", 32),
				"coinbasevalue":     1000000000000,
				"bits":              "207fffff",
				"height":            101,
			}
		} else {
			resp["error"] = map[string]interface{}{"code": -32601, "message": "Method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	client := daemon.NewClient(&config.DaemonConfig{Host: host, Port: port, User: "rpc", Password: "rpc"}, zap.NewNop())
	return node, client
}

// Aux blocks must pay the pool's configured aux wallet. getauxblock takes no address
// and pays a key from the aux node's own wallet, so the pool must request
// createauxblock with the configured address.
func TestRefreshAuxBlocks_PaysConfiguredAuxAddress(t *testing.T) {
	const configuredAddress = "DConfiguredPoolDogeWallet"

	dogeImpl, err := coin.Create("DOGE")
	if err != nil {
		t.Fatalf("coin.Create(DOGE): %v", err)
	}
	doge, ok := dogeImpl.(coin.AuxPowCoin)
	if !ok {
		t.Fatal("DOGE does not implement coin.AuxPowCoin")
	}

	node, client := newFakeAuxNode(t)
	m := &Manager{
		auxConfigs: []AuxChainConfig{{
			Symbol:       "DOGE",
			Coin:         doge,
			DaemonClient: client,
			Address:      configuredAddress,
			Enabled:      true,
		}},
		prevAuxHashes: make(map[string]prevAuxState),
		logger:        zap.NewNop().Sugar(),
	}

	blocks, err := m.RefreshAuxBlocks(context.Background())
	if err != nil {
		t.Fatalf("RefreshAuxBlocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d aux blocks, want 1", len(blocks))
	}

	node.mu.Lock()
	defer node.mu.Unlock()
	if len(node.calls) != 1 {
		t.Fatalf("aux node received %d RPC calls, want 1", len(node.calls))
	}
	call := node.calls[0]
	if call.Method != "createauxblock" {
		t.Errorf("aux block requested with %q, want createauxblock", call.Method)
	}
	if len(call.Params) != 1 || call.Params[0] != configuredAddress {
		t.Errorf("createauxblock params = %v, want [%s]", call.Params, configuredAddress)
	}
}
