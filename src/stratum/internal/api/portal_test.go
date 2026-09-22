// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

const portalTestAddress = "DPPuRbdG3XNRi5P5r4R8N4T9AnhWKoKGHy"

// fakePortalStore answers portal queries from fixed data for one address.
type fakePortalStore struct {
	address string
	stats   *database.MinerStats
	workers []*database.WorkerSummary
	blocks  []*database.Block
	err     error
	calls   int
}

func (f *fakePortalStore) GetMinerStats(ctx context.Context, address string) (*database.MinerStats, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if address != f.address || f.stats == nil {
		return &database.MinerStats{Address: address}, nil
	}
	return f.stats, nil
}

func (f *fakePortalStore) GetMinerWorkers(ctx context.Context, miner string, windowMinutes int) ([]*database.WorkerSummary, error) {
	f.calls++
	if miner != f.address {
		return []*database.WorkerSummary{}, nil
	}
	return f.workers, nil
}

func (f *fakePortalStore) GetBlocksByMiner(ctx context.Context, miner string, limit int) ([]*database.Block, error) {
	f.calls++
	if miner != f.address {
		return nil, nil
	}
	return f.blocks, nil
}

func newTestPortal(t *testing.T, pools []portalPool) *portal {
	t.Helper()
	p := newPortal(func() []portalPool { return pools }, zap.NewNop().Sugar())
	t.Cleanup(p.stop)
	return p
}

func portalRequest(t *testing.T, p *portal, method, path, remote string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	p.register(mux)
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A lookup returns the address's activity on each pool it mined, and leaves out
// pools where it has none.
func TestPortalLookup_AggregatesActivePools(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	active := &fakePortalStore{
		address: portalTestAddress,
		stats:   &database.MinerStats{Address: portalTestAddress, ShareCount: 1200, Hashrate: 4.8e12, LastShare: now},
		workers: []*database.WorkerSummary{{Miner: portalTestAddress, Worker: "nerdqaxe", Hashrate: 4.8e12, LastShare: now, Connected: true}},
		blocks:  []*database.Block{{Height: 24000000, Hash: "00000000abc", Status: "confirmed", Reward: 277.4, Source: "nerdqaxe", Created: now}},
	}
	idle := &fakePortalStore{address: "someone-else"}

	p := newTestPortal(t, []portalPool{
		{PoolID: "dgb_sha256_1", Coin: "DGB", Algorithm: "sha256d", Store: active},
		{PoolID: "btc_sha256_1", Coin: "BTC", Algorithm: "sha256d", Store: idle},
	})
	rec := portalRequest(t, p, http.MethodGet, "/api/portal/"+portalTestAddress, "203.0.113.5:4242")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var got portalLookup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Address != portalTestAddress {
		t.Errorf("address = %q", got.Address)
	}
	if len(got.Coins) != 1 {
		t.Fatalf("coins = %d, want only the pool with activity", len(got.Coins))
	}
	c := got.Coins[0]
	if c.Coin != "DGB" || c.Shares != 1200 || c.HashrateFormatted == "" {
		t.Errorf("coin = %+v", c)
	}
	if len(c.Workers) != 1 || c.Workers[0].Worker != "nerdqaxe" || !c.Workers[0].Connected {
		t.Errorf("workers = %+v", c.Workers)
	}
	if len(c.Blocks) != 1 || c.Blocks[0].Height != 24000000 || c.Blocks[0].Worker != "nerdqaxe" {
		t.Errorf("blocks = %+v", c.Blocks)
	}
}

// An address with no activity anywhere gets an empty list, not an error.
func TestPortalLookup_NoActivity(t *testing.T) {
	p := newTestPortal(t, []portalPool{{PoolID: "dgb_sha256_1", Coin: "DGB", Store: &fakePortalStore{address: "other"}}})
	rec := portalRequest(t, p, http.MethodGet, "/api/portal/"+portalTestAddress, "203.0.113.5:4242")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"coins":[]`) {
		t.Errorf("body = %s, want an empty coins list", rec.Body.String())
	}
}

// Anything that is not a wallet address is refused before any query runs.
func TestPortalLookup_InvalidAddress(t *testing.T) {
	store := &fakePortalStore{address: portalTestAddress}
	p := newTestPortal(t, []portalPool{{PoolID: "dgb_sha256_1", Coin: "DGB", Store: store}})
	for i, path := range []string{"/api/portal/", "/api/portal/not-an-address", "/api/portal/DGB'%3B--", "/api/portal/" + portalTestAddress + "/extra"} {
		rec := portalRequest(t, p, http.MethodGet, path, "203.0.113."+string(rune('1'+i))+":4242")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, rec.Code)
		}
	}
	if store.calls != 0 {
		t.Errorf("store queried %d times for invalid addresses", store.calls)
	}
}

func TestPortalLookup_DatabaseUnavailable(t *testing.T) {
	for name, pools := range map[string][]portalPool{
		"no pools":       nil,
		"no db for pool": {{PoolID: "dgb_sha256_1", Coin: "DGB"}},
	} {
		p := newTestPortal(t, pools)
		rec := portalRequest(t, p, http.MethodGet, "/api/portal/"+portalTestAddress, "203.0.113.5:4242")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", name, rec.Code)
		}
	}
}

// A database error is reported as a server error without leaking its text.
func TestPortalLookup_StoreError(t *testing.T) {
	store := &fakePortalStore{address: portalTestAddress, err: errors.New("relation shares_x does not exist")}
	p := newTestPortal(t, []portalPool{{PoolID: "dgb_sha256_1", Coin: "DGB", Store: store}})
	rec := portalRequest(t, p, http.MethodGet, "/api/portal/"+portalTestAddress, "203.0.113.5:4242")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "relation") {
		t.Error("response leaks the database error")
	}
}

// Each lookup queries every pool, so one client is held to a stricter rate than
// the general API.
func TestPortalLookup_RateLimited(t *testing.T) {
	p := newTestPortal(t, []portalPool{{PoolID: "dgb_sha256_1", Coin: "DGB", Store: &fakePortalStore{address: portalTestAddress}}})
	limited := false
	for i := 0; i < portalRequestsPerSecond+3; i++ {
		if portalRequest(t, p, http.MethodGet, "/api/portal/"+portalTestAddress, "198.51.100.7:5000").Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("a burst of lookups from one IP was never rate limited")
	}
	if rec := portalRequest(t, p, http.MethodGet, "/api/portal/"+portalTestAddress, "198.51.100.8:5000"); rec.Code != http.StatusOK {
		t.Errorf("another IP was limited too: status %d", rec.Code)
	}
}

func TestPortalLookup_MethodNotAllowed(t *testing.T) {
	p := newTestPortal(t, []portalPool{{PoolID: "dgb_sha256_1", Coin: "DGB", Store: &fakePortalStore{}}})
	if rec := portalRequest(t, p, http.MethodPost, "/api/portal/"+portalTestAddress, "203.0.113.5:4242"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

// The page and its assets are served with a CSP that allows only same-origin
// script, and the page carries no inline script for that CSP to have to allow.
func TestPortalFiles_ServedWithCSP(t *testing.T) {
	p := newTestPortal(t, nil)

	cases := map[string]string{
		"/portal":            "text/html",
		"/portal/":           "text/html",
		"/portal/portal.js":  "text/javascript",
		"/portal/portal.css": "text/css",
	}
	for path, wantType := range cases {
		rec := portalRequest(t, p, http.MethodGet, path, "203.0.113.5:4242")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, wantType) {
			t.Errorf("%s: Content-Type = %q, want %s", path, ct, wantType)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
	}

	page := portalRequest(t, p, http.MethodGet, "/portal", "203.0.113.5:4242").Body.String()
	for _, inline := range []string{"<script>", "onclick=", "onsubmit=", "onload=", "javascript:"} {
		if strings.Contains(page, inline) {
			t.Errorf("portal page contains inline script (%s)", inline)
		}
	}

	for _, path := range []string{"/portal/../server.go", "/portal/missing.js"} {
		if rec := portalRequest(t, p, http.MethodGet, path, "203.0.113.5:4242"); rec.Code == http.StatusOK {
			t.Errorf("%s served with 200", path)
		}
	}
}

// The portal script must never parse API data as HTML.
func TestPortalScript_NoHTMLInjection(t *testing.T) {
	js, err := fs.ReadFile(portalFiles, "portal/portal.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(string(js), sink) {
			t.Errorf("portal.js uses %s", sink)
		}
	}
}
