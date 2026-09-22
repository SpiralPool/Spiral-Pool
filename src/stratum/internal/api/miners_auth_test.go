// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// GET /api/pools/{id}/miners lists every active wallet address. It requires the
// admin key whenever one is configured; per-address lookups stay public.

const minersAuthTestAddress = "DQkwDpRYUyNNnoEZDf5Cb3QVazh4FuPRs9"

// minersAuthProvider satisfies CoinPoolProvider for routing only; the miners
// handlers never call it.
type minersAuthProvider struct{ CoinPoolProvider }

func minersAuthV1(key string) http.HandlerFunc {
	s := &Server{
		cfg:     &config.APIConfig{Enabled: true, AdminAPIKey: key},
		poolCfg: &config.PoolConfig{ID: "pool1"},
		logger:  zap.NewNop().Sugar(),
	}
	return s.handlePoolRoutes
}

func minersAuthV2(key string) http.HandlerFunc {
	cfg := &config.ConfigV2{}
	cfg.Global.AdminAPIKey = key
	s := &ServerV2{
		cfg:           cfg,
		logger:        zap.NewNop().Sugar(),
		poolProviders: map[string]CoinPoolProvider{"pool1": minersAuthProvider{}},
	}
	return s.handlePoolRoutes
}

func minersAuthRequest(handler http.HandlerFunc, path, apiKey string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec.Code
}

func TestPoolMinersList_RequiresAdminKey(t *testing.T) {
	servers := map[string]func(string) http.HandlerFunc{"v1": minersAuthV1, "v2": minersAuthV2}
	for name, build := range servers {
		t.Run(name, func(t *testing.T) {
			handler := build("secret-key")

			if code := minersAuthRequest(handler, "/api/pools/pool1/miners", ""); code != http.StatusUnauthorized {
				t.Errorf("no key: expected 401, got %d", code)
			}
			if code := minersAuthRequest(handler, "/api/pools/pool1/miners", "wrong-key"); code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("wrong key: expected 401/403, got %d", code)
			}
			// The right key reaches the handler, which reports the missing test database.
			if code := minersAuthRequest(handler, "/api/pools/pool1/miners", "secret-key"); code != http.StatusServiceUnavailable {
				t.Errorf("right key: expected the handler's 503, got %d", code)
			}
		})
	}
}

func TestPoolMinersList_PublicWhenNoKeyConfigured(t *testing.T) {
	servers := map[string]func(string) http.HandlerFunc{"v1": minersAuthV1, "v2": minersAuthV2}
	for name, build := range servers {
		t.Run(name, func(t *testing.T) {
			if code := minersAuthRequest(build(""), "/api/pools/pool1/miners", ""); code != http.StatusServiceUnavailable {
				t.Errorf("expected the handler's 503 without a configured key, got %d", code)
			}
		})
	}
}

func TestMinerLookup_StaysPublic(t *testing.T) {
	servers := map[string]func(string) http.HandlerFunc{"v1": minersAuthV1, "v2": minersAuthV2}
	for name, build := range servers {
		t.Run(name, func(t *testing.T) {
			code := minersAuthRequest(build("secret-key"), "/api/pools/pool1/miners/"+minersAuthTestAddress, "")
			if code == http.StatusUnauthorized || code == http.StatusForbidden {
				t.Errorf("per-address lookup must not need the admin key, got %d", code)
			}
		})
	}
}
