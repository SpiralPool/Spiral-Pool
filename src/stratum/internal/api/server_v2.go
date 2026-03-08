// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package api - V2 multi-coin API server
//
// ServerV2 provides REST API endpoints for multi-coin pool configurations.
// It aggregates statistics from multiple CoinPools managed by the Coordinator.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/stratum"
	"go.uber.org/zap"
)

// ServerV2 provides the REST API for V2 multi-coin pools.
type ServerV2 struct {
	cfg    *config.ConfigV2
	logger *zap.SugaredLogger
	server *http.Server
	db     *database.PostgresDB

	// Rate limiting
	rateLimiter *RateLimiter

	// Pool stats providers (one per coin pool)
	poolProviders   map[string]CoinPoolProvider
	poolProvidersMu sync.RWMutex

	// Sentinel alert provider (wired from Coordinator)
	sentinelProvider SentinelAlertProvider

	// Cached responses
	cacheMu     sync.RWMutex
	poolsCache  []byte
	cacheExpiry time.Time
}

// SentinelAlertProvider provides access to API Sentinel alerts.
// Implemented by the Coordinator to avoid circular imports.
type SentinelAlertProvider interface {
	GetSentinelAlerts(since time.Time) []SentinelAlert
}

// SentinelAlert mirrors pool.SentinelAlert for JSON serialization at the API layer.
type SentinelAlert struct {
	AlertType string    `json:"alert_type"`
	Severity  string    `json:"severity"`
	Coin      string    `json:"coin,omitempty"`
	PoolID    string    `json:"pool_id,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// CoinPoolProvider provides stats for a single coin pool.
type CoinPoolProvider interface {
	Symbol() string
	PoolID() string
	GetConnections() int64
	GetHashrate() float64
	GetSharesPerSecond() float64
	GetBlockHeight() uint64
	GetNetworkDifficulty() float64
	GetNetworkHashrate() float64
	GetBlocksFound() int64
	GetBlockReward() float64
	GetPoolEffort() float64
	GetStratumPort() int
}

// NewServerV2 creates a new V2 multi-coin API server.
func NewServerV2(cfg *config.ConfigV2, db *database.PostgresDB, logger *zap.Logger) *ServerV2 {
	// Build rate limiting config from global settings
	rateCfg := config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 10,
		Whitelist:         []string{"127.0.0.1"}, // IPv4-only (IPv6 disabled at OS level)
	}

	return &ServerV2{
		cfg:           cfg,
		logger:        logger.Sugar(),
		db:            db,
		rateLimiter:   NewRateLimiter(rateCfg),
		poolProviders: make(map[string]CoinPoolProvider),
	}
}

// RegisterPool registers a coin pool provider for API stats.
func (s *ServerV2) RegisterPool(poolID string, provider CoinPoolProvider) {
	s.poolProvidersMu.Lock()
	defer s.poolProvidersMu.Unlock()
	s.poolProviders[poolID] = provider
	s.logger.Infow("Registered pool for API", "poolId", poolID, "coin", provider.Symbol())
}

// SetSentinelProvider sets the sentinel alert provider for the /api/sentinel/alerts endpoint.
func (s *ServerV2) SetSentinelProvider(provider SentinelAlertProvider) {
	s.sentinelProvider = provider
}

// UnregisterPool removes a coin pool provider.
func (s *ServerV2) UnregisterPool(poolID string) {
	s.poolProvidersMu.Lock()
	defer s.poolProvidersMu.Unlock()
	delete(s.poolProviders, poolID)
}

// Start begins serving the V2 API.
func (s *ServerV2) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Pool endpoints (public)
	mux.HandleFunc("/api/pools", s.handlePools)
	mux.HandleFunc("/api/pools/", s.handlePoolRoutes)

	// Sentinel alerts endpoint (public — consumed by Python Spiral Sentinel on same machine)
	mux.HandleFunc("/api/sentinel/alerts", s.handleSentinelAlerts)

	// Health check (public)
	mux.HandleFunc("/health", s.handleHealth)

	// Admin endpoints (protected by API key)
	mux.HandleFunc("/api/admin/device-hints", s.adminAuthMiddlewareV2(s.handleDeviceHintsV2))

	// Apply middleware
	handler := s.rateLimitMiddleware(mux)
	handler = s.loggingMiddleware(handler)
	handler = s.corsMiddleware(handler)

	listenAddr := fmt.Sprintf("0.0.0.0:%d", s.cfg.Global.APIPort)
	s.server = &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Infow("V2 API server starting", "address", listenAddr)

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Errorw("V2 API server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the V2 API server.
func (s *ServerV2) Stop() error {
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

// handlePools returns information about all pools.
func (s *ServerV2) handlePools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check cache
	s.cacheMu.RLock()
	if time.Now().Before(s.cacheExpiry) && len(s.poolsCache) > 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.poolsCache)
		s.cacheMu.RUnlock()
		return
	}
	s.cacheMu.RUnlock()

	// Build response from all registered pools
	s.poolProvidersMu.RLock()
	pools := make([]PoolInfo, 0, len(s.poolProviders))

	for _, provider := range s.poolProviders {
		algorithm := s.getAlgorithmForCoin(provider.Symbol())
		hashrate := provider.GetHashrate()

		poolInfo := PoolInfo{
			ID: provider.PoolID(),
			Coin: CoinInfo{
				Type:      provider.Symbol(),
				Algorithm: algorithm,
			},
			Ports: PortsInfo{
				Stratum: provider.GetStratumPort(),
			},
			PoolStats: PoolStatsInfo{
				ConnectedMiners:       int(provider.GetConnections()),
				PoolHashrate:          hashrate,
				PoolHashrateFormatted: coin.FormatHashrateString(hashrate, algorithm),
				SharesPerSecond:       provider.GetSharesPerSecond(),
				NetworkDifficulty:     provider.GetNetworkDifficulty(),
				NetworkHashrate:       provider.GetNetworkHashrate(),
				BlockHeight:           provider.GetBlockHeight(),
				BlocksFound:           provider.GetBlocksFound(),
				BlockReward:           provider.GetBlockReward(),
				PoolEffort:            provider.GetPoolEffort(),
			},
			PaymentProcessing: PaymentInfo{
				Enabled:        true,
				PayoutScheme:   "SOLO",
				MinimumPayment: 1.0,
			},
		}

		// Get address from config
		for _, coin := range s.cfg.Coins {
			if coin.PoolID == provider.PoolID() {
				poolInfo.Address = coin.Address
				break
			}
		}

		pools = append(pools, poolInfo)
	}
	s.poolProvidersMu.RUnlock()

	response := PoolsResponse{
		Software: "spiral-stratum",
		Version:  "1.0-BLACKICE-V2",
		Pools:    pools,
	}

	data, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Update cache
	s.cacheMu.Lock()
	s.poolsCache = data
	s.cacheExpiry = time.Now().Add(10 * time.Second)
	s.cacheMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handlePoolRoutes routes pool-specific requests.
func (s *ServerV2) handlePoolRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/pools/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Pool ID required", http.StatusBadRequest)
		return
	}

	poolID := parts[0]

	// Validate pool ID format to prevent injection
	if !validPoolIDPattern.MatchString(poolID) {
		http.Error(w, "Invalid pool ID format", http.StatusBadRequest)
		return
	}

	// Validate pool ID exists
	s.poolProvidersMu.RLock()
	provider, exists := s.poolProviders[poolID]
	s.poolProvidersMu.RUnlock()

	if !exists {
		http.Error(w, "Pool not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		// /api/pools/{id}
		s.handlePoolInfo(w, r, provider)
		return
	}

	switch parts[1] {
	case "stats":
		s.handlePoolStats(w, r, provider)
	case "blocks":
		s.handlePoolBlocks(w, r, poolID, provider.Symbol())
	case "network":
		s.handlePoolNetwork(w, r, provider)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// handlePoolInfo returns detailed pool information.
func (s *ServerV2) handlePoolInfo(w http.ResponseWriter, r *http.Request, provider CoinPoolProvider) {
	algorithm := s.getAlgorithmForCoin(provider.Symbol())
	hashrate := provider.GetHashrate()

	response := map[string]interface{}{
		"id":        provider.PoolID(),
		"coin":      provider.Symbol(),
		"algorithm": algorithm,
		"ports": map[string]interface{}{
			"stratum": provider.GetStratumPort(),
		},
		"poolStats": map[string]interface{}{
			"connectedMiners":   provider.GetConnections(),
			"poolHashrate":      hashrate,
			"poolHashrateFormatted": coin.FormatHashrateString(hashrate, algorithm),
			"sharesPerSecond":   provider.GetSharesPerSecond(),
			"networkDifficulty": provider.GetNetworkDifficulty(),
			"networkHashrate":   provider.GetNetworkHashrate(),
			"blockHeight":       provider.GetBlockHeight(),
			"blocksFound":       provider.GetBlocksFound(),
			"blockReward":       provider.GetBlockReward(),
			"poolEffort":        provider.GetPoolEffort(),
		},
	}

	s.writeJSON(w, response)
}

// handlePoolStats returns pool statistics.
func (s *ServerV2) handlePoolStats(w http.ResponseWriter, r *http.Request, provider CoinPoolProvider) {
	algorithm := s.getAlgorithmForCoin(provider.Symbol())
	hashrate := provider.GetHashrate()

	response := map[string]interface{}{
		"poolId":              provider.PoolID(),
		"coin":                provider.Symbol(),
		"algorithm":           algorithm,
		"connectedMiners":     provider.GetConnections(),
		"poolHashrate":        hashrate,
		"poolHashrateFormatted": coin.FormatHashrateString(hashrate, algorithm),
		"sharesPerSecond":     provider.GetSharesPerSecond(),
		"networkDifficulty":   provider.GetNetworkDifficulty(),
		"networkHashrate":     provider.GetNetworkHashrate(),
		"blockHeight":         provider.GetBlockHeight(),
		"blocksFound":         provider.GetBlocksFound(),
		"blockReward":         provider.GetBlockReward(),
		"poolEffort":          provider.GetPoolEffort(),
	}

	s.writeJSON(w, response)
}

// handlePoolNetwork returns network statistics for a pool's coin.
// Used by Spiral Dash to display blockchain difficulty, hashrate, and block height.
func (s *ServerV2) handlePoolNetwork(w http.ResponseWriter, r *http.Request, provider CoinPoolProvider) {
	response := map[string]interface{}{
		"coin":       provider.Symbol(),
		"difficulty": provider.GetNetworkDifficulty(),
		"height":     provider.GetBlockHeight(),
		"hashrate":   provider.GetNetworkHashrate(),
	}
	s.writeJSON(w, response)
}

// handlePoolBlocks returns block history for a pool.
func (s *ServerV2) handlePoolBlocks(w http.ResponseWriter, r *http.Request, poolID string, coinSymbol string) {
	ctx := r.Context()

	// Scope DB queries to this specific pool's tables
	// BUG FIX: Use GetBlocksWithOrphans instead of GetBlocks.
	// GetBlocks filters WHERE status IN ('pending', 'confirmed'), which hides
	// orphaned blocks from the API. Sentinel's check_for_orphans() needs to see
	// blocks that transitioned to "orphaned" status for orphan detection to work.
	// Sentinel's check_pool_for_new_blocks() already skips non-pending/non-confirmed
	// blocks (line 14394), so including orphaned blocks is safe for block detection.
	scopedDB := s.db.WithPoolID(poolID)
	blocks, err := scopedDB.GetBlocksWithOrphans(ctx)
	if err != nil {
		s.logger.Errorw("Failed to get blocks", "poolId", poolID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response := make([]map[string]interface{}, 0, len(blocks))
	for _, b := range blocks {
		entry := map[string]interface{}{
			"blockHeight":          b.Height,
			"status":               b.Status,
			"confirmationProgress": b.ConfirmationProgress,
			"networkDifficulty":    b.NetworkDifficulty,
			"effort":               b.Effort,
			"miner":                b.Miner,
			"reward":               b.Reward,
			"hash":                 b.Hash,
			"created":              b.Created,
			"coin":                 coinSymbol,
		}
		if b.Source != "" {
			entry["source"] = b.Source
		}
		response = append(response, entry)
	}

	s.writeJSON(w, response)
}

// handleHealth returns health status.
func (s *ServerV2) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.poolProvidersMu.RLock()
	poolCount := len(s.poolProviders)
	s.poolProvidersMu.RUnlock()

	response := map[string]interface{}{
		"status":     "ok",
		"version":    "V2",
		"poolsOnline": poolCount,
	}

	s.writeJSON(w, response)
}

// handleSentinelAlerts returns recent API Sentinel alerts.
// Accepts optional query param ?since=RFC3339_timestamp (defaults to last 5 minutes).
// Used by the Python Spiral Sentinel to poll and forward alerts to Discord/Telegram.
func (s *ServerV2) handleSentinelAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.sentinelProvider == nil {
		s.writeJSON(w, []SentinelAlert{})
		return
	}

	// Parse ?since= parameter, default to 5 minutes ago
	since := time.Now().Add(-5 * time.Minute)
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		parsed, err := time.Parse(time.RFC3339, sinceParam)
		if err != nil {
			http.Error(w, "Invalid 'since' parameter: must be RFC3339 format", http.StatusBadRequest)
			return
		}
		since = parsed
	}

	alerts := s.sentinelProvider.GetSentinelAlerts(since)
	if alerts == nil {
		alerts = []SentinelAlert{}
	}

	s.writeJSON(w, alerts)
}

// getAlgorithmForCoin returns the mining algorithm for a coin symbol.
// Uses the centralized coin.AlgorithmFromCoinSymbol for consistency.
func (s *ServerV2) getAlgorithmForCoin(symbol string) string {
	return coin.AlgorithmFromCoinSymbol(symbol)
}

// writeJSON writes a JSON response.
func (s *ServerV2) writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Errorw("Failed to encode JSON response", "error", err)
	}
}

// Middleware

func (s *ServerV2) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Debugw("API request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
			"ip", r.RemoteAddr,
		)
	})
}

func (s *ServerV2) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowedOrigin := ""

		if origin != "" {
			// Allow localhost origins for local dashboard access
			// SECURITY: Use URL parsing instead of prefix matching to prevent
			// bypasses like "http://localhost.evil.com"
			if parsed, err := url.Parse(origin); err == nil {
				hostname := parsed.Hostname()
				if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
					allowedOrigin = origin
				}
			}
		}

		if allowedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *ServerV2) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.rateLimiter.Allow(r.RemoteAddr) {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminAuthMiddlewareV2 protects admin endpoints with API key authentication.
func (s *ServerV2) adminAuthMiddlewareV2(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Global.AdminAPIKey == "" {
			s.logger.Warnw("Admin endpoint accessed without API key configured", "path", r.URL.Path)
			http.Error(w, "Admin API key not configured", http.StatusForbidden)
			return
		}

		// Check X-API-Key header first, then Authorization: Bearer
		providedKey := r.Header.Get("X-API-Key")
		if providedKey == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				providedKey = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if providedKey == "" {
			http.Error(w, "API key required", http.StatusUnauthorized)
			return
		}

		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(s.cfg.Global.AdminAPIKey)) != 1 {
			s.logger.Warnw("Invalid admin API key attempt", "path", r.URL.Path)
			http.Error(w, "Invalid API key", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

// handleDeviceHintsV2 handles device hints for V2 multi-coin pools.
// POST: Add/update device hints from Sentinel
// GET: List all device hints
// DELETE: Remove a device hint by IP
func (s *ServerV2) handleDeviceHintsV2(w http.ResponseWriter, r *http.Request) {
	registry := stratum.GetGlobalDeviceHints()

	switch r.Method {
	case http.MethodGet:
		// List all hints
		hints := registry.GetAll()
		s.writeJSON(w, map[string]interface{}{
			"hints": hints,
			"count": len(hints),
		})

	case http.MethodPost:
		// Add/update hint
		// SECURITY: Limit request body size to prevent DoS
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
		var req struct {
			IP          string  `json:"ip"`
			DeviceModel string  `json:"deviceModel"`
			ASICModel   string  `json:"asicModel"`
			ASICCount   int     `json:"asicCount"`
			HashrateGHs float64 `json:"hashrateGHs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if req.IP == "" {
			http.Error(w, "IP required", http.StatusBadRequest)
			return
		}

		hint := &stratum.DeviceHint{
			IP:          req.IP,
			DeviceModel: req.DeviceModel,
			ASICModel:   req.ASICModel,
			ASICCount:   req.ASICCount,
			HashrateGHs: req.HashrateGHs,
		}
		registry.Set(hint)

		s.logger.Infow("Device hint registered",
			"ip", req.IP,
			"model", req.DeviceModel,
			"asic", req.ASICModel,
			"chips", req.ASICCount,
			"class", hint.Class.String(),
		)

		s.writeJSON(w, map[string]interface{}{
			"status":  "ok",
			"ip":      req.IP,
			"class":   hint.Class.String(),
			"message": "Device hint registered",
		})

	case http.MethodDelete:
		ip := r.URL.Query().Get("ip")
		if ip == "" {
			http.Error(w, "IP query parameter required", http.StatusBadRequest)
			return
		}
		registry.Delete(ip)
		s.writeJSON(w, map[string]interface{}{
			"status":  "ok",
			"ip":      ip,
			"message": "Device hint removed",
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
