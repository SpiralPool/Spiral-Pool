// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package api provides REST API endpoints for the mining pool.
//
// The API provides standard mining pool endpoints for pool statistics,
// miner statistics, and administrative operations.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/discovery"
	"github.com/spiralpool/stratum/internal/stratum"
	"go.uber.org/zap"
)

// SECURITY: Request body size limits to prevent DoS attacks
const (
	maxRequestBodySize = 1024 * 1024 // 1MB max request body
)

// Input validation patterns
var (
	// Multi-coin address patterns:
	// - DigiByte: D (mainnet P2PKH), S (P2SH), dgb1 (bech32)
	// Address validation supports all 13 coins:
	// - BTC/BC2/FBTC/QBX: 1 (P2PKH), 3 (P2SH), bc1q (bech32)
	// - BCH: q/p (CashAddr), bitcoincash: prefix
	// - DGB: D (P2PKH), S (P2SH), 3 (P2SH), dgb1q (bech32)
	// - LTC: L/M (P2PKH/P2SH), ltc1q (bech32)
	// - DOGE: D (P2PKH)
	// - NMC: N/M (P2PKH/P2SH), nc1q (bech32)
	// - SYS: S (P2PKH), sys1q (bech32)
	// - XMY: M (P2PKH), my1q (bech32)
	// - PEP: P (P2PKH), pep1q (bech32)
	// - CAT: 9 (P2PKH, version byte 21) — SegWit disabled on mainnet (SegwitHeight=INT_MAX)
	validAddressPattern = regexp.MustCompile(`^(?:` +
		`[13DSLMNP9][a-km-zA-HJ-NP-Z1-9]{25,34}|` + // Legacy P2PKH/P2SH (all coins; 9=CAT)
		`bc1q[a-z0-9]{38,58}|` + // Bitcoin/BC2/FBTC/QBX bech32
		`bcrt1q[a-z0-9]{38,58}|` + // Bitcoin regtest bech32
		`dgb1q[a-z0-9]{38,58}|` + // DigiByte mainnet bech32
		`dgbt1q[a-z0-9]{38,58}|` + // DigiByte testnet bech32
		`dgbrt1q[a-z0-9]{38,58}|` + // DigiByte regtest bech32
		`ltc1q[a-z0-9]{38,58}|` + // Litecoin bech32
		`tltc1q[a-z0-9]{38,58}|` + // Litecoin testnet bech32
		`sys1q[a-z0-9]{38,58}|` + // Syscoin bech32
		`tsys1q[a-z0-9]{38,58}|` + // Syscoin testnet bech32
		`nc1q[a-z0-9]{38,58}|` + // Namecoin bech32
		`my1q[a-z0-9]{38,58}|` + // Myriadcoin bech32
		`pep1q[a-z0-9]{38,58}|` + // PepeCoin bech32
		`[qp][a-z0-9]{40,42}|` + // Bitcoin Cash CashAddr (short)
		`bitcoincash:[qp][a-z0-9]{40,42}` + // Bitcoin Cash CashAddr (full)
		`)$`)
	// Pool IDs: must be valid PostgreSQL identifiers (alphanumeric with underscores, 1-63 chars)
	// Must start with letter or underscore. Hyphens are NOT allowed as they are invalid in table names.
	validPoolIDPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
)

// Server provides the REST API for the pool.
type Server struct {
	cfg        *config.APIConfig
	poolCfg    *config.PoolConfig
	stratumCfg      *config.StratumConfig      // Stratum config for port info
	mergeMiningCfg  *config.MergeMiningConfig  // Merge mining config for aux chains
	logger     *zap.SugaredLogger
	server     *http.Server
	db         *database.PostgresDB // Concrete type for full API access (single-node mode)

	// Rate limiting
	rateLimiter *RateLimiter

	// Stats provider (set by pool)
	statsProvider StatsProvider

	// Worker stats provider (set by pool)
	workerStats WorkerStatsProvider

	// Router profiles provider (set by pool)
	routerProvider RouterProfilesProvider

	// Pipeline stats provider (set by pool)
	pipelineProvider PipelineStatsProvider

	// Payment stats provider (set by pool)
	paymentProvider PaymentStatsProvider

	// Connection stats provider (set by pool)
	connectionProvider ConnectionStatsProvider

	// HA components (optional - set when HA mode is enabled)
	dbManager       *database.DatabaseManager
	failoverManager *discovery.FailoverManager

	// Cached responses
	cacheMu     sync.RWMutex
	poolsCache  []byte
	cacheExpiry time.Time
}

// getDB returns the current active database connection.
// In HA mode, this dynamically fetches from the manager to handle failovers.
// In single-node mode, this returns the static db reference.
func (s *Server) getDB() *database.PostgresDB {
	if s.dbManager != nil {
		// HA mode: always get the current active node's connection
		return s.dbManager.GetActiveDB()
	}
	// Single-node mode: use the static reference
	return s.db
}

// StatsProvider interface for getting pool statistics.
type StatsProvider interface {
	GetConnections() int64
	GetHashrate() float64
	GetSharesPerSecond() float64
	GetBlockHeight() uint64
	GetNetworkDifficulty() float64
	GetNetworkHashrate() float64
	GetBlocksFound() int64
	GetBlockReward() float64
	GetPoolEffort() float64
}

// RouterProfile represents difficulty settings for a miner class.
type RouterProfile struct {
	Class           string  `json:"class"`
	InitialDiff     float64 `json:"initialDiff"`
	MinDiff         float64 `json:"minDiff"`
	MaxDiff         float64 `json:"maxDiff"`
	TargetShareTime int     `json:"targetShareTime"`
}

// RouterProfilesProvider provides access to Spiral Router profiles.
type RouterProfilesProvider interface {
	GetProfiles() []RouterProfile
	GetWorkersByClass() map[string]int
}

// PipelineStats represents share pipeline statistics.
type PipelineStats struct {
	Processed      uint64 `json:"processed"`
	Written        uint64 `json:"written"`
	Dropped        uint64 `json:"dropped"`
	BufferCurrent  int    `json:"bufferCurrent"`
	BufferCapacity int    `json:"bufferCapacity"`
}

// PipelineStatsProvider provides access to share pipeline statistics.
type PipelineStatsProvider interface {
	GetPipelineStats() PipelineStats
}

// PaymentStats represents payment processor statistics.
type PaymentStats struct {
	PendingBlocks   int     `json:"pendingBlocks"`
	ConfirmedBlocks int     `json:"confirmedBlocks"`
	OrphanedBlocks  int     `json:"orphanedBlocks"`
	PaidBlocks      int     `json:"paidBlocks"`
	BlockMaturity   int     `json:"blockMaturity"`
	TotalPaid       float64 `json:"totalPaid"`
}

// PaymentStatsProvider provides access to payment processor statistics.
type PaymentStatsProvider interface {
	GetPaymentStats() (*PaymentStats, error)
}

// WorkerConnection represents real-time connection status for a worker.
type WorkerConnection struct {
	SessionID    uint64    `json:"sessionId"`
	WorkerName   string    `json:"workerName"`
	MinerAddress string    `json:"minerAddress"`
	UserAgent    string    `json:"userAgent"`
	RemoteAddr   string    `json:"remoteAddr"`
	ConnectedAt  time.Time `json:"connectedAt"`
	LastActivity time.Time `json:"lastActivity"`
	Difficulty   float64   `json:"difficulty"`
	ShareCount   uint64    `json:"shareCount"`
}

// ConnectionStatsProvider provides access to real-time worker connection status.
type ConnectionStatsProvider interface {
	GetActiveConnections() []WorkerConnection
}

// NewServer creates a new API server.
func NewServer(cfg *config.APIConfig, poolCfg *config.PoolConfig, db *database.PostgresDB, logger *zap.Logger) *Server {
	return &Server{
		cfg:         cfg,
		poolCfg:     poolCfg,
		logger:      logger.Sugar(),
		db:          db,
		rateLimiter: NewRateLimiter(cfg.RateLimiting),
	}
}

// SetStatsProvider sets the stats provider.
func (s *Server) SetStatsProvider(provider StatsProvider) {
	s.statsProvider = provider
}

// SetDatabaseManager sets the HA database manager for status reporting.
func (s *Server) SetDatabaseManager(dm *database.DatabaseManager) {
	s.dbManager = dm
}

// SetStratumConfig sets the stratum configuration for port info in API responses.
func (s *Server) SetStratumConfig(cfg *config.StratumConfig) {
	s.stratumCfg = cfg
}

// SetMergeMiningConfig sets the merge mining configuration for API responses.
func (s *Server) SetMergeMiningConfig(cfg *config.MergeMiningConfig) {
	s.mergeMiningCfg = cfg
}

// SetFailoverManager sets the pool failover manager for status reporting.
func (s *Server) SetFailoverManager(fm *discovery.FailoverManager) {
	s.failoverManager = fm
}

// SetRouterProvider sets the router profiles provider.
func (s *Server) SetRouterProvider(rp RouterProfilesProvider) {
	s.routerProvider = rp
}

// SetPipelineProvider sets the pipeline stats provider.
func (s *Server) SetPipelineProvider(pp PipelineStatsProvider) {
	s.pipelineProvider = pp
}

// SetPaymentProvider sets the payment stats provider.
func (s *Server) SetPaymentProvider(pp PaymentStatsProvider) {
	s.paymentProvider = pp
}

// SetConnectionProvider sets the connection stats provider.
func (s *Server) SetConnectionProvider(cp ConnectionStatsProvider) {
	s.connectionProvider = cp
}

// Start begins serving the API.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Pool endpoints (public - no authentication required)
	mux.HandleFunc("/api/pools", s.handlePools)
	mux.HandleFunc("/api/pools/", s.handlePoolRoutes)

	// Admin endpoints (SECURITY: Protected by API key authentication)
	mux.HandleFunc("/api/admin/stats", s.adminAuthMiddleware(s.handleAdminStats))
	mux.HandleFunc("/api/admin/device-hints", s.adminAuthMiddleware(s.handleDeviceHints))

	// HA/Failover status endpoints (SECURITY: Protected by API key authentication)
	// These expose sensitive cluster information and must be protected
	mux.HandleFunc("/api/ha/status", s.adminAuthMiddleware(s.handleHAStatus))
	mux.HandleFunc("/api/ha/database", s.adminAuthMiddleware(s.handleDatabaseHAStatus))
	mux.HandleFunc("/api/ha/failover", s.adminAuthMiddleware(s.handleFailoverStatus))
	mux.HandleFunc("/api/ha/alerts", s.adminAuthMiddleware(s.handleHAAlerts))

	// Sentinel alerts endpoint (public — consumed by Python Spiral Sentinel on same machine)
	// In V1 mode the Go sentinel doesn't run, so this always returns an empty array.
	// This prevents 404 errors when the Python Sentinel polls for alerts.
	mux.HandleFunc("/api/sentinel/alerts", s.handleSentinelAlerts)

	// Health check (public - needed for load balancers/k8s probes)
	mux.HandleFunc("/health", s.handleHealth)

	// Coin registry endpoint (public - for Sentinel/Dashboard validation)
	mux.HandleFunc("/api/coins", s.handleCoins)

	// Apply middleware
	handler := s.rateLimitMiddleware(mux)
	handler = s.loggingMiddleware(handler)
	handler = s.corsMiddleware(handler)
	handler = s.securityHeadersMiddleware(handler)

	s.server = &http.Server{
		Addr:         s.cfg.Listen,
		Handler:      handler,
		ReadTimeout:  15 * time.Second, // Allow time for dashboard queries under load
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Infow("API server starting", "address", s.cfg.Listen)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Errorw("PANIC recovered in ListenAndServe goroutine", "panic", r)
			}
		}()
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Errorw("API server error", "error", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the API server.
func (s *Server) Stop() error {
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

// handlePools returns pool information.
func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check cache
	s.cacheMu.RLock()
	if time.Now().Before(s.cacheExpiry) && len(s.poolsCache) > 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.poolsCache) // #nosec G104
		s.cacheMu.RUnlock()
		return
	}
	s.cacheMu.RUnlock()

	// Build response
	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	hashrate, err := db.GetPoolHashrate(ctx, 10)
	if err != nil {
		s.logger.Warnw("Failed to get pool hashrate for /api/pools", "error", err)
	}

	var connections int64
	var sharesPerSec float64
	var blockHeight uint64
	var networkDiff float64
	var networkHashrate float64
	var blocksFound int64
	var blockReward float64
	var poolEffort float64

	if s.statsProvider != nil {
		connections = s.statsProvider.GetConnections()
		sharesPerSec = s.statsProvider.GetSharesPerSecond()
		blockHeight = s.statsProvider.GetBlockHeight()
		networkDiff = s.statsProvider.GetNetworkDifficulty()
		networkHashrate = s.statsProvider.GetNetworkHashrate()
		blocksFound = s.statsProvider.GetBlocksFound()
		blockReward = s.statsProvider.GetBlockReward()
		poolEffort = s.statsProvider.GetPoolEffort()
	}

	// Build ports info from stratum config if available
	ports := PortsInfo{}
	if s.stratumCfg != nil {
		ports.Stratum = s.stratumCfg.GetListenPort()
		ports.StratumV2 = s.stratumCfg.GetV2ListenPort()
		ports.StratumTLS = s.stratumCfg.GetTLSListenPort()
	}

	// Build merge mining info if configured
	var mergeMiningInfo *PoolMergeMiningInfo
	if s.mergeMiningCfg != nil && s.mergeMiningCfg.Enabled {
		auxChains := make([]string, 0)
		for _, aux := range s.mergeMiningCfg.AuxChains {
			if aux.Enabled {
				auxChains = append(auxChains, aux.Symbol)
			}
		}
		if len(auxChains) > 0 {
			mergeMiningInfo = &PoolMergeMiningInfo{
				Enabled:   true,
				AuxChains: auxChains,
			}
		}
	}

	response := PoolsResponse{
		Software: "spiral-stratum",
		Version:  "1.0-BLACKICE",
		Pools: []PoolInfo{
			{
				ID: s.poolCfg.ID,
				Coin: CoinInfo{
					Type:      s.poolCfg.Coin,
					Algorithm: coin.AlgorithmFromCoinSymbol(s.poolCfg.Coin),
				},
				Address:     s.poolCfg.Address,
				Ports:       ports,
				MergeMining: mergeMiningInfo,
				PoolStats: PoolStatsInfo{
					ConnectedMiners:   int(connections),
					PoolHashrate:      hashrate,
					SharesPerSecond:   sharesPerSec,
					NetworkDifficulty: networkDiff,
					NetworkHashrate:   networkHashrate,
					BlockHeight:       blockHeight,
					BlocksFound:       blocksFound,
					BlockReward:       blockReward,
					PoolEffort:        poolEffort,
				},
				PaymentProcessing: PaymentInfo{
					Enabled:        true,
					PayoutScheme:   "SOLO",
					MinimumPayment: 1.0,
				},
			},
		},
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
	_, _ = w.Write(data) // #nosec G104
}

// handlePoolRoutes routes pool-specific requests.
func (s *Server) handlePoolRoutes(w http.ResponseWriter, r *http.Request) {
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

	if poolID != s.poolCfg.ID {
		http.Error(w, "Pool not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		// /api/pools/{id}
		s.handlePoolInfo(w, r, poolID)
		return
	}

	switch parts[1] {
	case "stats":
		s.handlePoolStats(w, r, poolID)
	case "blocks":
		s.handlePoolBlocks(w, r, poolID)
	case "hashrate":
		if len(parts) >= 3 && parts[2] == "history" {
			s.handlePoolHashrateHistory(w, r, poolID)
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
	case "workers":
		// GET /api/pools/{id}/workers - all workers (admin view)
		// SECURITY: Protected by API key - exposes worker details
		s.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
			s.handlePoolWorkers(w, r, poolID)
		}).ServeHTTP(w, r)
	case "miners":
		if len(parts) < 3 {
			s.handlePoolMiners(w, r, poolID)
			return
		}

		minerAddr := parts[2]

		// Check for worker-related paths
		if len(parts) >= 4 && parts[3] == "workers" {
			if len(parts) == 4 {
				// GET /api/pools/{id}/miners/{address}/workers
				s.handleMinerWorkers(w, r, poolID, minerAddr)
			} else if len(parts) == 5 {
				// GET /api/pools/{id}/miners/{address}/workers/{worker}
				s.handleWorkerStats(w, r, poolID, minerAddr, parts[4])
			} else if len(parts) == 6 && parts[5] == "history" {
				// GET /api/pools/{id}/miners/{address}/workers/{worker}/history
				s.handleWorkerHistory(w, r, poolID, minerAddr, parts[4])
			} else {
				http.Error(w, "Not found", http.StatusNotFound)
			}
		} else {
			// GET /api/pools/{id}/miners/{address}
			s.handleMinerStats(w, r, poolID, minerAddr)
		}
	case "router":
		if len(parts) >= 3 && parts[2] == "profiles" {
			// GET /api/pools/{id}/router/profiles
			s.handleRouterProfiles(w, r, poolID)
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
	case "pipeline":
		if len(parts) >= 3 && parts[2] == "stats" {
			// GET /api/pools/{id}/pipeline/stats
			s.handlePipelineStats(w, r, poolID)
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
	case "workers-by-class":
		// GET /api/pools/{id}/workers-by-class
		s.handleWorkersByClass(w, r, poolID)
	case "payments":
		if len(parts) >= 3 && parts[2] == "stats" {
			// GET /api/pools/{id}/payments/stats
			s.handlePaymentStats(w, r, poolID)
		} else {
			http.Error(w, "Not found", http.StatusNotFound)
		}
	case "connections":
		// GET /api/pools/{id}/connections - real-time worker connection status
		// SECURITY: Protected by API key - exposes session IDs and network info
		s.adminAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
			s.handleConnections(w, r, poolID)
		}).ServeHTTP(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// handlePoolInfo returns detailed pool information.
func (s *Server) handlePoolInfo(w http.ResponseWriter, r *http.Request, poolID string) {
	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	hashrate, err := db.GetPoolHashrate(ctx, 10)
	if err != nil {
		s.logger.Warnw("Failed to get pool hashrate for pool info", "pool", poolID, "error", err)
	}

	// Build ports info from stratum config if available
	ports := map[string]interface{}{}
	if s.stratumCfg != nil {
		ports["stratum"] = s.stratumCfg.GetListenPort()
		if v2Port := s.stratumCfg.GetV2ListenPort(); v2Port > 0 {
			ports["stratumV2"] = v2Port
		}
		if tlsPort := s.stratumCfg.GetTLSListenPort(); tlsPort > 0 {
			ports["stratumTLS"] = tlsPort
		}
	}

	response := map[string]interface{}{
		"id":    poolID,
		"coin":  s.poolCfg.Coin,
		"ports": ports,
		"poolStats": map[string]interface{}{
			"poolHashrate": hashrate,
		},
	}

	s.writeJSON(w, response)
}

// handlePoolStats returns pool statistics.
func (s *Server) handlePoolStats(w http.ResponseWriter, r *http.Request, poolID string) {
	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	hashrate, err := db.GetPoolHashrate(ctx, 10)
	if err != nil {
		s.logger.Warnw("Failed to get pool hashrate for pool stats", "pool", poolID, "error", err)
	}

	var connections int64
	var sharesPerSec float64
	var blockHeight uint64
	var networkDiff float64
	var networkHashrate float64
	var blocksFound int64
	var blockReward float64
	var poolEffort float64

	if s.statsProvider != nil {
		connections = s.statsProvider.GetConnections()
		sharesPerSec = s.statsProvider.GetSharesPerSecond()
		blockHeight = s.statsProvider.GetBlockHeight()
		networkDiff = s.statsProvider.GetNetworkDifficulty()
		networkHashrate = s.statsProvider.GetNetworkHashrate()
		blocksFound = s.statsProvider.GetBlocksFound()
		blockReward = s.statsProvider.GetBlockReward()
		poolEffort = s.statsProvider.GetPoolEffort()
	}

	response := map[string]interface{}{
		"poolId":            poolID,
		"connectedMiners":   connections,
		"poolHashrate":      hashrate,
		"sharesPerSecond":   sharesPerSec,
		"networkDifficulty": networkDiff,
		"networkHashrate":   networkHashrate,
		"blockHeight":       blockHeight,
		"blocksFound":       blocksFound,
		"blockReward":       blockReward,
		"poolEffort":        poolEffort,
	}

	s.writeJSON(w, response)
}

// handlePoolBlocks returns block history.
func (s *Server) handlePoolBlocks(w http.ResponseWriter, r *http.Request, poolID string) {
	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	// Scope DB queries to this specific pool's tables
	scopedDB := db.WithPoolID(poolID)
	blocks, err := scopedDB.GetBlocks(ctx)
	if err != nil {
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
		}
		if b.Source != "" {
			entry["source"] = b.Source
		}
		response = append(response, entry)
	}

	s.writeJSON(w, response)
}

// handlePoolMiners returns all active miners.
func (s *Server) handlePoolMiners(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}

	// Get miners who have submitted shares in the last 10 minutes
	// This matches the hashrate calculation window
	miners, err := db.GetActiveMiners(ctx, 10)
	if err != nil {
		s.logger.Errorw("Failed to get active miners", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Return empty array instead of null if no miners
	if miners == nil {
		miners = []*database.MinerSummary{}
	}

	s.writeJSON(w, miners)
}

// handleMinerStats returns statistics for a specific miner.
func (s *Server) handleMinerStats(w http.ResponseWriter, r *http.Request, poolID, address string) {
	// Validate miner address format
	if !validAddressPattern.MatchString(address) {
		http.Error(w, "Invalid miner address format", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	stats, err := db.GetMinerStats(ctx, address)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"address":         stats.Address,
		"hashrate":        stats.Hashrate,
		"sharesPerSecond": float64(stats.ShareCount) / (24 * 3600),
		"lastShare":       stats.LastShare,
	}

	s.writeJSON(w, response)
}

// handleAdminStats returns admin statistics.
func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	hashrate, err := db.GetPoolHashrate(ctx, 10)
	if err != nil {
		s.logger.Warnw("Failed to get pool hashrate for admin stats", "error", err)
	}

	var connections int64
	if s.statsProvider != nil {
		connections = s.statsProvider.GetConnections()
	}

	response := map[string]interface{}{
		"pools": []map[string]interface{}{
			{
				"poolId":          s.poolCfg.ID,
				"connectedMiners": connections,
				"poolHashrate":    hashrate,
			},
		},
	}

	s.writeJSON(w, response)
}

// handleDeviceHints handles device hint updates from Spiral Sentinel.
// POST /api/admin/device-hints - Add/update device hints (from Sentinel discovery)
// GET /api/admin/device-hints - List all current device hints
// DELETE /api/admin/device-hints?ip=X.X.X.X - Remove a device hint
func (s *Server) handleDeviceHints(w http.ResponseWriter, r *http.Request) {
	registry := s.getDeviceHintsRegistry()
	if registry == nil {
		http.Error(w, "Device hints registry not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Return all current device hints
		hints := registry.GetAll()
		response := map[string]interface{}{
			"hints": hints,
			"count": len(hints),
		}
		s.writeJSON(w, response)

	case http.MethodPost:
		// Add or update device hints (single or batch)
		// Limit request body size
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

		var request struct {
			Hints []DeviceHintRequest `json:"hints"`
			// Also support single hint for convenience
			IP          string  `json:"ip,omitempty"`
			DeviceModel string  `json:"deviceModel,omitempty"`
			ASICModel   string  `json:"asicModel,omitempty"`
			ASICCount   int     `json:"asicCount,omitempty"`
			HashrateGHs float64 `json:"hashrateGHs,omitempty"`
		}

		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		// Handle single hint (backwards compatibility)
		if request.IP != "" && len(request.Hints) == 0 {
			request.Hints = []DeviceHintRequest{{
				IP:          request.IP,
				DeviceModel: request.DeviceModel,
				ASICModel:   request.ASICModel,
				ASICCount:   request.ASICCount,
				HashrateGHs: request.HashrateGHs,
			}}
		}

		// Validate and add hints
		added := 0
		for _, h := range request.Hints {
			if h.IP == "" {
				continue // Skip invalid entries
			}
			registry.Set(h.ToDeviceHint())
			added++
		}

		s.logger.Infow("Device hints updated",
			"added", added,
			"total", len(registry.GetAll()),
		)

		response := map[string]interface{}{
			"success": true,
			"added":   added,
		}
		s.writeJSON(w, response)

	case http.MethodDelete:
		// Remove a device hint by IP
		ip := r.URL.Query().Get("ip")
		if ip == "" {
			http.Error(w, "IP parameter required", http.StatusBadRequest)
			return
		}

		registry.Delete(ip)
		s.logger.Infow("Device hint deleted", "ip", ip)

		response := map[string]interface{}{
			"success": true,
			"deleted": ip,
		}
		s.writeJSON(w, response)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// DeviceHintRequest represents a device hint from Sentinel API.
type DeviceHintRequest struct {
	IP          string  `json:"ip"`
	DeviceModel string  `json:"deviceModel"`
	ASICModel   string  `json:"asicModel"`
	ASICCount   int     `json:"asicCount"`
	HashrateGHs float64 `json:"hashrateGHs"`
}

// ToDeviceHint converts the API request to a stratum.DeviceHint.
func (r *DeviceHintRequest) ToDeviceHint() *stratum.DeviceHint {
	return &stratum.DeviceHint{
		IP:          r.IP,
		DeviceModel: r.DeviceModel,
		ASICModel:   r.ASICModel,
		ASICCount:   r.ASICCount,
		HashrateGHs: r.HashrateGHs,
		// Class is computed automatically by the registry's Set method
	}
}

// getDeviceHintsRegistry returns the global device hints registry.
func (s *Server) getDeviceHintsRegistry() *stratum.DeviceHintsRegistry {
	return stratum.GetGlobalDeviceHints()
}

// handleRouterProfiles returns the Spiral Router difficulty profiles.
// GET /api/pools/{id}/router/profiles
func (s *Server) handleRouterProfiles(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.routerProvider == nil {
		http.Error(w, "Router profiles not available", http.StatusServiceUnavailable)
		return
	}

	profiles := s.routerProvider.GetProfiles()
	response := map[string]interface{}{
		"poolId":   poolID,
		"profiles": profiles,
	}

	s.writeJSON(w, response)
}

// handlePipelineStats returns share pipeline statistics.
// GET /api/pools/{id}/pipeline/stats
func (s *Server) handlePipelineStats(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.pipelineProvider == nil {
		http.Error(w, "Pipeline stats not available", http.StatusServiceUnavailable)
		return
	}

	stats := s.pipelineProvider.GetPipelineStats()
	response := map[string]interface{}{
		"poolId":   poolID,
		"pipeline": stats,
	}

	s.writeJSON(w, response)
}

// handleWorkersByClass returns worker count breakdown by miner class.
// GET /api/pools/{id}/workers-by-class
func (s *Server) handleWorkersByClass(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.routerProvider == nil {
		http.Error(w, "Worker class data not available", http.StatusServiceUnavailable)
		return
	}

	workersByClass := s.routerProvider.GetWorkersByClass()

	// Calculate total
	total := 0
	for _, count := range workersByClass {
		total += count
	}

	response := map[string]interface{}{
		"poolId":         poolID,
		"workersByClass": workersByClass,
		"total":          total,
	}

	s.writeJSON(w, response)
}

// handlePaymentStats returns payment processor statistics.
// GET /api/pools/{id}/payments/stats
func (s *Server) handlePaymentStats(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.paymentProvider == nil {
		http.Error(w, "Payment stats not available", http.StatusServiceUnavailable)
		return
	}

	stats, err := s.paymentProvider.GetPaymentStats()
	if err != nil {
		s.logger.Errorw("Failed to get payment stats", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"poolId":   poolID,
		"payments": stats,
	}

	s.writeJSON(w, response)
}

// handleConnections returns real-time worker connection status.
// Supports pagination via query params: ?page=1&limit=100 (max 1000, default 100)
// SECURITY: Protected by adminAuthMiddleware - exposes sensitive connection data.
func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.connectionProvider == nil {
		http.Error(w, "Connection stats not available", http.StatusServiceUnavailable)
		return
	}

	// Parse pagination parameters with safe defaults
	// SECURITY: Limit max page size to prevent DoS via large responses
	const (
		defaultLimit = 100
		maxLimit     = 1000
	)

	page := 1
	limit := defaultLimit

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			if l > maxLimit {
				l = maxLimit
			}
			limit = l
		}
	}

	connections := s.connectionProvider.GetActiveConnections()
	total := len(connections)

	// Apply pagination
	start := (page - 1) * limit
	end := start + limit

	if start >= total {
		// Page out of range - return empty
		connections = []WorkerConnection{}
	} else {
		if end > total {
			end = total
		}
		connections = connections[start:end]
	}

	// Calculate pagination metadata
	totalPages := (total + limit - 1) / limit
	if totalPages == 0 {
		totalPages = 1
	}

	response := map[string]interface{}{
		"poolId":      poolID,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"totalPages":  totalPages,
		"connections": connections,
	}

	s.writeJSON(w, response)
}

// handleHealth returns health status for load balancers and uptime monitors.
// Supports three modes via query parameter:
//   - /health (default) - basic liveness check
//   - /health?check=ready - readiness check including dependencies
//   - /health?check=live - simple liveness check
//
// SECURITY: Does not expose sensitive internal state.
// Returns minimal information suitable for external monitoring.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	checkType := r.URL.Query().Get("check")

	switch checkType {
	case "ready":
		s.handleReadinessCheck(w, r)
	case "live":
		s.handleLivenessCheck(w, r)
	default:
		// Default: basic liveness check
		s.handleLivenessCheck(w, r)
	}
}

// handleLivenessCheck returns a simple liveness status.
// This should always return 200 if the process is running.
func (s *Server) handleLivenessCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","check":"live"}`)) // #nosec G104
}

// handleReadinessCheck performs dependency checks with timeouts.
// Returns 200 if all dependencies are healthy, 503 otherwise.
func (s *Server) handleReadinessCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	response := HealthResponse{
		Status:       "ok",
		Check:        "ready",
		Dependencies: map[string]DependencyStatus{},
	}

	allHealthy := true

	// Check database connection
	dbStatus := s.checkDatabaseHealth(ctx)
	response.Dependencies["database"] = dbStatus
	if dbStatus.Status != "ok" {
		allHealthy = false
	}

	// Check stratum server (via stats provider)
	stratumStatus := s.checkStratumHealth()
	response.Dependencies["stratum"] = stratumStatus
	if stratumStatus.Status != "ok" {
		allHealthy = false
	}

	// Set overall status
	// SECURITY FIX: Return 200 for degraded state with X-Health-Status header
	// Load balancers remove 503 nodes from rotation, but degraded nodes can still
	// serve traffic. Use the X-Health-Status header for fine-grained health checks.
	// Content-Type MUST be set before WriteHeader — Go ignores headers set after WriteHeader()
	w.Header().Set("Content-Type", "application/json")

	if !allHealthy {
		response.Status = "degraded"
		w.Header().Set("X-Health-Status", "degraded")
		// Return 200 so load balancers keep node in rotation
		// Monitoring systems should check X-Health-Status or response body for true status
		w.WriteHeader(http.StatusOK)
	} else {
		w.Header().Set("X-Health-Status", "healthy")
		w.WriteHeader(http.StatusOK)
	}

	_ = json.NewEncoder(w).Encode(response) // #nosec G104
}

// handleCoins returns all registered coins from the Go registry.
// This endpoint allows Sentinel and Dashboard to validate that coins in the
// manifest actually have Go implementations registered in the pool.
// GET /api/coins - Returns all registered coin symbols with their properties
func (s *Server) handleCoins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get all registered coin symbols
	symbols := coin.ListRegistered()

	// Build detailed coin info for each registered coin
	coins := make([]RegisteredCoinInfo, 0, len(symbols))
	for _, symbol := range symbols {
		c, err := coin.Create(symbol)
		if err != nil {
			continue // Skip if we can't create it (shouldn't happen)
		}

		info := RegisteredCoinInfo{
			Symbol:    c.Symbol(),
			Name:      c.Name(),
			Algorithm: c.Algorithm(),
			Network: CoinNetworkInfo{
				RPCPort:        c.DefaultRPCPort(),
				P2PPort:        c.DefaultP2PPort(),
				P2PKHVersion:   c.P2PKHVersionByte(),
				P2SHVersion:    c.P2SHVersionByte(),
				Bech32HRP:      c.Bech32HRP(),
				SupportsSegWit: c.SupportsSegWit(),
			},
			Chain: CoinChainInfo{
				GenesisHash:      c.GenesisBlockHash(),
				BlockTime:        c.BlockTime(),
				CoinbaseMaturity: c.CoinbaseMaturity(),
			},
		}

		// Check if coin supports AuxPoW
		if auxCoin, ok := c.(coin.AuxPowCoin); ok && auxCoin.SupportsAuxPow() {
			info.MergeMining = &CoinMergeMiningInfo{
				SupportsAuxPow:    true,
				AuxPowStartHeight: auxCoin.AuxPowStartHeight(),
				ChainID:           auxCoin.ChainID(),
				VersionBit:        auxCoin.AuxPowVersionBit(),
			}
		}

		coins = append(coins, info)
	}

	response := CoinsResponse{
		Count: len(coins),
		Coins: coins,
	}

	s.writeJSON(w, response)
}

// checkDatabaseHealth tests database connectivity.
func (s *Server) checkDatabaseHealth(ctx context.Context) DependencyStatus {
	db := s.getDB()
	if db == nil {
		return DependencyStatus{Status: "unknown", Message: "not configured"}
	}

	// Use a simple query to test connectivity
	_, err := db.GetPoolHashrate(ctx, 1)
	if err != nil {
		return DependencyStatus{Status: "error", Message: "connection failed"}
	}

	return DependencyStatus{Status: "ok"}
}

// checkStratumHealth checks if stratum server is running.
func (s *Server) checkStratumHealth() DependencyStatus {
	if s.statsProvider == nil {
		return DependencyStatus{Status: "unknown", Message: "not configured"}
	}

	// Check if stratum is accepting connections
	// A negative connection count would indicate an error
	conns := s.statsProvider.GetConnections()
	if conns < 0 {
		return DependencyStatus{Status: "error", Message: "stratum unavailable"}
	}

	return DependencyStatus{Status: "ok"}
}

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status       string                      `json:"status"` // ok, degraded, error
	Check        string                      `json:"check"`  // live, ready
	Dependencies map[string]DependencyStatus `json:"dependencies,omitempty"`
}

// DependencyStatus represents the health of a dependency.
type DependencyStatus struct {
	Status  string `json:"status"`            // ok, error, unknown
	Message string `json:"message,omitempty"` // Error message (never exposes internals)
}

// handleHAStatus returns comprehensive HA status for Spiral Sentinel monitoring.
// This endpoint provides a unified view of both database and pool failover status.
func (s *Server) handleHAStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response := HAStatusResponse{
		Timestamp: time.Now(),
		PoolID:    s.poolCfg.ID,
	}

	// Database HA status
	if s.dbManager != nil {
		stats := s.dbManager.Stats()
		response.Database = &DatabaseHAStatus{
			Enabled:       true,
			ActiveNode:    stats.ActiveNode,
			TotalNodes:    stats.TotalNodes,
			HealthyNodes:  stats.HealthyNodes,
			Failovers:     stats.Failovers,
			WriteFailures: stats.WriteFailures,
			Status:        s.getDatabaseHAHealthStatus(stats),
		}
	} else {
		response.Database = &DatabaseHAStatus{
			Enabled: false,
			Status:  "disabled",
		}
	}

	// Pool failover status
	if s.failoverManager != nil {
		poolStatus := s.failoverManager.GetPoolStatus()
		response.Pool = &PoolFailoverStatus{
			Enabled:         true,
			PrimaryHost:     poolStatus.PrimaryHost,
			PrimaryPort:     poolStatus.PrimaryPort,
			IsPrimaryActive: poolStatus.IsPrimaryActive,
			ActivePoolID:    poolStatus.ActivePoolID,
			BackupPools:     len(poolStatus.BackupPools),
			FailoverCount:   poolStatus.FailoverCount,
			Status:          s.getPoolHAHealthStatus(poolStatus),
		}
		if poolStatus.LastFailover != nil {
			response.Pool.LastFailover = poolStatus.LastFailover.OccurredAt
			response.Pool.LastFailoverReason = poolStatus.LastFailover.Reason
		}
	} else {
		response.Pool = &PoolFailoverStatus{
			Enabled: false,
			Status:  "disabled",
		}
	}

	// Overall health
	response.OverallStatus = s.getOverallHAStatus(response.Database, response.Pool)

	s.writeJSON(w, response)
}

// handleDatabaseHAStatus returns detailed database HA status.
func (s *Server) handleDatabaseHAStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.dbManager == nil {
		s.writeJSON(w, map[string]interface{}{
			"enabled": false,
			"message": "Database HA mode is not enabled",
		})
		return
	}

	stats := s.dbManager.Stats()
	activeNode := s.dbManager.GetActiveNode()

	// AUDIT FIX (API-1): GetActiveNode() can return nil when the active node
	// index is out of bounds (e.g., during HA startup race or total node failure).
	// Without this guard, lines below would panic on nil pointer dereference.
	var nodeDetails *DatabaseNodeDetails
	if activeNode != nil {
		nodeDetails = &DatabaseNodeDetails{
			ID:       activeNode.ID,
			Host:     activeNode.Config.Host,
			Port:     activeNode.Config.Port,
			Priority: activeNode.Priority,
			ReadOnly: activeNode.ReadOnly,
			State:    activeNode.State.String(),
		}
	}

	response := DatabaseHADetailedStatus{
		Enabled:           true,
		ActiveNode:        stats.ActiveNode,
		TotalNodes:        stats.TotalNodes,
		HealthyNodes:      stats.HealthyNodes,
		Failovers:         stats.Failovers,
		WriteFailures:     stats.WriteFailures,
		ReadFailures:      stats.ReadFailures,
		Status:            s.getDatabaseHAHealthStatus(stats),
		ActiveNodeDetails: nodeDetails,
	}

	s.writeJSON(w, response)
}

// handleFailoverStatus returns pool failover status.
func (s *Server) handleFailoverStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.failoverManager == nil {
		s.writeJSON(w, map[string]interface{}{
			"enabled": false,
			"message": "Pool failover is not enabled",
		})
		return
	}

	poolStatus := s.failoverManager.GetPoolStatus()

	response := PoolFailoverDetailedStatus{
		Enabled:         true,
		PrimaryHost:     poolStatus.PrimaryHost,
		PrimaryPort:     poolStatus.PrimaryPort,
		IsPrimaryActive: poolStatus.IsPrimaryActive,
		ActivePoolID:    poolStatus.ActivePoolID,
		FailoverCount:   poolStatus.FailoverCount,
		Status:          s.getPoolHAHealthStatus(poolStatus),
		BackupPools:     make([]BackupPoolInfo, 0, len(poolStatus.BackupPools)),
	}

	for _, bp := range poolStatus.BackupPools {
		response.BackupPools = append(response.BackupPools, BackupPoolInfo{
			ID:           bp.ID,
			Host:         bp.Host,
			Port:         bp.Port,
			State:        bp.State,
			ResponseTime: bp.ResponseTime.Milliseconds(),
			LastCheck:    bp.LastCheck,
		})
	}

	if poolStatus.LastFailover != nil {
		response.LastFailover = &FailoverEventInfo{
			FromPool:   poolStatus.LastFailover.FromPool,
			ToPool:     poolStatus.LastFailover.ToPool,
			Reason:     poolStatus.LastFailover.Reason,
			OccurredAt: poolStatus.LastFailover.OccurredAt,
		}
	}

	s.writeJSON(w, response)
}

// handleHAAlerts returns HA alerts for Spiral Sentinel monitoring.
// Only returns alerts when failover is enabled (multi-pool mode).
// Supports GET (list alerts) and POST (acknowledge alerts).
func (s *Server) handleHAAlerts(w http.ResponseWriter, r *http.Request) {
	// Alerts only available when failover is enabled (multi-pool mode)
	if s.failoverManager == nil {
		s.writeJSON(w, HAAlertResponse{
			Enabled: false,
			Message: "HA alerts only available when pool failover is enabled (multi-pool mode)",
			Alerts:  []discovery.HAAlert{},
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetAlerts(w, r)
	case http.MethodPost:
		s.handleAcknowledgeAlerts(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetAlerts returns all unacknowledged alerts.
func (s *Server) handleGetAlerts(w http.ResponseWriter, r *http.Request) {
	// Check for query params
	includeAcknowledged := r.URL.Query().Get("all") == "true"

	var alerts []discovery.HAAlert
	if includeAcknowledged {
		alerts = s.failoverManager.GetAllAlerts()
	} else {
		alerts = s.failoverManager.GetAlerts()
	}

	if alerts == nil {
		alerts = []discovery.HAAlert{}
	}

	response := HAAlertResponse{
		Enabled:     true,
		Alerts:      alerts,
		TotalAlerts: len(alerts),
	}

	// Count by severity
	for _, alert := range alerts {
		switch alert.Severity {
		case discovery.AlertSeverityCritical:
			response.CriticalCount++
		case discovery.AlertSeverityWarning:
			response.WarningCount++
		case discovery.AlertSeverityInfo:
			response.InfoCount++
		}
	}

	s.writeJSON(w, response)
}

// handleAcknowledgeAlerts acknowledges one or all alerts.
func (s *Server) handleAcknowledgeAlerts(w http.ResponseWriter, r *http.Request) {
	// SECURITY: Limit request body size to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

	var req AcknowledgeAlertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// SECURITY: Log decode failures for monitoring
		s.logger.Debugw("JSON decode failed", "error", err, "endpoint", "/alerts/acknowledge")
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.AcknowledgeAll {
		count := s.failoverManager.AcknowledgeAllAlerts()
		s.writeJSON(w, map[string]interface{}{
			"success":      true,
			"acknowledged": count,
		})
		return
	}

	if req.AlertID == "" {
		http.Error(w, "alert_id required", http.StatusBadRequest)
		return
	}

	if err := s.failoverManager.AcknowledgeAlert(req.AlertID); err != nil {
		http.Error(w, "alert not found", http.StatusNotFound)
		return
	}

	s.writeJSON(w, map[string]interface{}{
		"success":  true,
		"alert_id": req.AlertID,
	})
}

// getDatabaseHAHealthStatus determines the health status string for database HA.
func (s *Server) getDatabaseHAHealthStatus(stats database.DBManagerStats) string {
	if stats.HealthyNodes == 0 {
		return "critical"
	}
	if stats.HealthyNodes < stats.TotalNodes {
		return "degraded"
	}
	return "healthy"
}

// getPoolHAHealthStatus determines the health status string for pool failover.
func (s *Server) getPoolHAHealthStatus(status discovery.PoolStatus) string {
	if !status.IsPrimaryActive && status.ActivePoolID == "" {
		return "critical"
	}
	if !status.IsPrimaryActive {
		return "failover"
	}
	return "healthy"
}

// getOverallHAStatus determines the overall HA health status.
func (s *Server) getOverallHAStatus(db *DatabaseHAStatus, pool *PoolFailoverStatus) string {
	// If both are disabled, return "standalone"
	if !db.Enabled && !pool.Enabled {
		return "standalone"
	}

	// Check for critical status
	if (db.Enabled && db.Status == "critical") || (pool.Enabled && pool.Status == "critical") {
		return "critical"
	}

	// Check for degraded/failover status
	if (db.Enabled && db.Status == "degraded") || (pool.Enabled && pool.Status == "failover") {
		return "degraded"
	}

	return "healthy"
}

// handleSentinelAlerts returns an empty array in V1 mode since the Go sentinel
// only runs in V2 (Coordinator) mode. This endpoint exists so the Python Spiral
// Sentinel doesn't get 404 errors when polling for internal pool health alerts.
func (s *Server) handleSentinelAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writeJSON(w, []struct{}{})
}

// writeJSON writes a JSON response.
func (s *Server) writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Errorw("Failed to encode JSON response", "error", err)
	}
}

// Middleware

// responseRecorder wraps http.ResponseWriter to capture the status code.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rr, r)

		// Audit #7: Log admin endpoint access at INFO level with enhanced details
		if strings.HasPrefix(r.URL.Path, "/api/admin/") || strings.HasPrefix(r.URL.Path, "/api/ha/") {
			s.logger.Infow("Admin API request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rr.statusCode,
				"duration", time.Since(start),
				"ip", r.RemoteAddr,
				"apiKeyPresent", r.Header.Get("X-API-Key") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "),
			)
		} else {
			s.logger.Debugw("API request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rr.statusCode,
				"duration", time.Since(start),
				"ip", r.RemoteAddr,
			)
		}
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS: Allow requests from configured origins or localhost for dashboard
		// SECURITY (M-01): No wildcard CORS - restrict to known origins
		origin := r.Header.Get("Origin")
		allowedOrigin := "" // Default: no CORS (same-origin only)

		// For production deployments, restrict to known origins
		if s.cfg.CORSAllowedOrigin != "" {
			// Explicit origin configured - use it
			allowedOrigin = s.cfg.CORSAllowedOrigin
		} else if origin != "" {
			// Allow localhost origins for local dashboard access
			// This covers typical solo mining setups where dashboard runs locally
			// SECURITY (H-5): Use URL parsing instead of prefix matching to prevent
			// bypasses like "http://localhost.evil.com"
			if parsed, err := url.Parse(origin); err == nil {
				hostname := parsed.Hostname()
				if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
					allowedOrigin = origin
				}
			}
		}

		// Only set CORS headers if we have an allowed origin
		if allowedOrigin == "" {
			// No CORS - same-origin only
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400") // Cache preflight for 24h

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.RateLimiting.Enabled && !s.rateLimiter.Allow(r.RemoteAddr) {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminAuthMiddleware protects admin and HA endpoints with API key authentication.
// SECURITY: Requires X-API-Key header matching the configured AdminAPIKey.
// If no API key is configured, admin endpoints are disabled for security.
func (s *Server) adminAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// SECURITY: If no API key is configured, deny all admin access
		if s.cfg.AdminAPIKey == "" {
			s.logger.Warnw("Admin endpoint accessed without API key configured",
				"path", r.URL.Path,
				"ip", r.RemoteAddr)
			http.Error(w, "Admin API key not configured - admin endpoints disabled", http.StatusForbidden)
			return
		}

		// Get API key from header
		providedKey := r.Header.Get("X-API-Key")
		if providedKey == "" {
			// Also check Authorization header as fallback
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				providedKey = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if providedKey == "" {
			http.Error(w, "API key required", http.StatusUnauthorized)
			return
		}

		// SECURITY: Use constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(s.cfg.AdminAPIKey)) != 1 {
			s.logger.Warnw("Invalid admin API key attempt",
				"path", r.URL.Path,
				"ip", r.RemoteAddr)
			http.Error(w, "Invalid API key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	}
}

// Response types

type PoolsResponse struct {
	Software string     `json:"software"`          // Pool software identifier
	Version  string     `json:"version,omitempty"` // Software version
	Pools    []PoolInfo `json:"pools"`
}

type PoolInfo struct {
	ID                string              `json:"id"`
	Coin              CoinInfo            `json:"coin"`
	Address           string              `json:"address"`
	Ports             PortsInfo           `json:"ports"`
	PoolStats         PoolStatsInfo       `json:"poolStats"`
	PaymentProcessing PaymentInfo         `json:"paymentProcessing"`
	MergeMining       *PoolMergeMiningInfo `json:"mergeMining,omitempty"`
}

// PoolMergeMiningInfo contains merge mining status for the /api/pools response.
type PoolMergeMiningInfo struct {
	Enabled   bool     `json:"enabled"`
	AuxChains []string `json:"auxChains"`
}

// PortsInfo contains stratum port information for the pool.
// This allows Sentinel and other clients to discover the actual configured ports.
type PortsInfo struct {
	Stratum    int `json:"stratum"`              // V1 stratum port (e.g., 3333)
	StratumV2  int `json:"stratumV2,omitempty"`  // V2 stratum port (if enabled)
	StratumTLS int `json:"stratumTLS,omitempty"` // TLS stratum port (if enabled)
}

type CoinInfo struct {
	Type      string `json:"type"`
	Algorithm string `json:"algorithm"`
}

type PoolStatsInfo struct {
	ConnectedMiners       int     `json:"connectedMiners"`
	PoolHashrate          float64 `json:"poolHashrate"`
	PoolHashrateFormatted string  `json:"poolHashrateFormatted,omitempty"`
	SharesPerSecond       float64 `json:"sharesPerSecond"`
	NetworkDifficulty     float64 `json:"networkDifficulty"`
	NetworkHashrate       float64 `json:"networkHashrate"`
	BlockHeight           uint64  `json:"blockHeight"`
	BlocksFound           int64   `json:"blocksFound"`
	BlockReward           float64 `json:"blockReward"`
	PoolEffort            float64 `json:"poolEffort"`
}

type PaymentInfo struct {
	Enabled        bool    `json:"enabled"`
	PayoutScheme   string  `json:"payoutScheme"`
	MinimumPayment float64 `json:"minimumPayment"`
}

// RateLimiter implements a simple token bucket rate limiter.
type RateLimiter struct {
	cfg     config.RateLimitConfig
	buckets sync.Map // map[string]*bucket
	stopCh  chan struct{}
}

// HA Status Response Types (for Spiral Sentinel monitoring)

// HAStatusResponse is the comprehensive HA status response.
type HAStatusResponse struct {
	Timestamp     time.Time           `json:"timestamp"`
	PoolID        string              `json:"poolId"`
	OverallStatus string              `json:"overallStatus"` // healthy, degraded, critical, standalone
	Database      *DatabaseHAStatus   `json:"database"`
	Pool          *PoolFailoverStatus `json:"pool"`
}

// DatabaseHAStatus represents database HA status.
type DatabaseHAStatus struct {
	Enabled       bool   `json:"enabled"`
	ActiveNode    string `json:"activeNode,omitempty"`
	TotalNodes    int    `json:"totalNodes,omitempty"`
	HealthyNodes  int    `json:"healthyNodes,omitempty"`
	Failovers     uint64 `json:"failovers,omitempty"`
	WriteFailures uint64 `json:"writeFailures,omitempty"`
	Status        string `json:"status"` // healthy, degraded, critical, disabled
}

// PoolFailoverStatus represents pool failover status.
type PoolFailoverStatus struct {
	Enabled            bool      `json:"enabled"`
	PrimaryHost        string    `json:"primaryHost,omitempty"`
	PrimaryPort        int       `json:"primaryPort,omitempty"`
	IsPrimaryActive    bool      `json:"isPrimaryActive,omitempty"`
	ActivePoolID       string    `json:"activePoolId,omitempty"`
	BackupPools        int       `json:"backupPools,omitempty"`
	FailoverCount      int       `json:"failoverCount,omitempty"`
	LastFailover       time.Time `json:"lastFailover,omitempty"`
	LastFailoverReason string    `json:"lastFailoverReason,omitempty"`
	Status             string    `json:"status"` // healthy, failover, critical, disabled
}

// DatabaseHADetailedStatus is the detailed database HA status response.
type DatabaseHADetailedStatus struct {
	Enabled           bool                 `json:"enabled"`
	ActiveNode        string               `json:"activeNode"`
	TotalNodes        int                  `json:"totalNodes"`
	HealthyNodes      int                  `json:"healthyNodes"`
	Failovers         uint64               `json:"failovers"`
	WriteFailures     uint64               `json:"writeFailures"`
	ReadFailures      uint64               `json:"readFailures"`
	Status            string               `json:"status"`
	ActiveNodeDetails *DatabaseNodeDetails `json:"activeNodeDetails"`
}

// DatabaseNodeDetails contains details about a database node.
type DatabaseNodeDetails struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Priority int    `json:"priority"`
	ReadOnly bool   `json:"readOnly"`
	State    string `json:"state"`
}

// PoolFailoverDetailedStatus is the detailed pool failover status response.
type PoolFailoverDetailedStatus struct {
	Enabled         bool               `json:"enabled"`
	PrimaryHost     string             `json:"primaryHost"`
	PrimaryPort     int                `json:"primaryPort"`
	IsPrimaryActive bool               `json:"isPrimaryActive"`
	ActivePoolID    string             `json:"activePoolId,omitempty"`
	FailoverCount   int                `json:"failoverCount"`
	Status          string             `json:"status"`
	BackupPools     []BackupPoolInfo   `json:"backupPools"`
	LastFailover    *FailoverEventInfo `json:"lastFailover,omitempty"`
}

// BackupPoolInfo contains information about a backup pool.
type BackupPoolInfo struct {
	ID           string    `json:"id"`
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	State        string    `json:"state"`
	ResponseTime int64     `json:"responseTimeMs"`
	LastCheck    time.Time `json:"lastCheck"`
}

// FailoverEventInfo contains information about a failover event.
type FailoverEventInfo struct {
	FromPool   string    `json:"fromPool"`
	ToPool     string    `json:"toPool"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurredAt"`
}

// HAAlertResponse is the response for the HA alerts endpoint.
// Alerts are only available when failover is enabled (multi-pool mode).
type HAAlertResponse struct {
	Enabled       bool                `json:"enabled"`
	Message       string              `json:"message,omitempty"`
	Alerts        []discovery.HAAlert `json:"alerts"`
	TotalAlerts   int                 `json:"totalAlerts"`
	CriticalCount int                 `json:"criticalCount"`
	WarningCount  int                 `json:"warningCount"`
	InfoCount     int                 `json:"infoCount"`
}

// AcknowledgeAlertRequest is the request body for acknowledging alerts.
type AcknowledgeAlertRequest struct {
	AlertID        string `json:"alert_id,omitempty"`
	AcknowledgeAll bool   `json:"acknowledge_all,omitempty"`
}

// ═══════════════════════════════════════════════════════════════════════════════
// COIN REGISTRY RESPONSE TYPES
// These types support the /api/coins endpoint for Sentinel/Dashboard validation.
// ═══════════════════════════════════════════════════════════════════════════════

// CoinsResponse is the response for the /api/coins endpoint.
type CoinsResponse struct {
	Count int                  `json:"count"`
	Coins []RegisteredCoinInfo `json:"coins"`
}

// RegisteredCoinInfo contains detailed information about a registered coin.
type RegisteredCoinInfo struct {
	Symbol      string               `json:"symbol"`
	Name        string               `json:"name"`
	Algorithm   string               `json:"algorithm"`
	Network     CoinNetworkInfo      `json:"network"`
	Chain       CoinChainInfo        `json:"chain"`
	MergeMining *CoinMergeMiningInfo `json:"mergeMining,omitempty"`
}

// CoinNetworkInfo contains network-related coin parameters.
type CoinNetworkInfo struct {
	RPCPort        int    `json:"rpcPort"`
	P2PPort        int    `json:"p2pPort"`
	P2PKHVersion   byte   `json:"p2pkhVersion"`
	P2SHVersion    byte   `json:"p2shVersion"`
	Bech32HRP      string `json:"bech32Hrp"`
	SupportsSegWit bool   `json:"supportsSegwit"`
}

// CoinChainInfo contains chain-related coin parameters.
type CoinChainInfo struct {
	GenesisHash      string `json:"genesisHash"`
	BlockTime        int    `json:"blockTime"`
	CoinbaseMaturity int    `json:"coinbaseMaturity"`
}

// CoinMergeMiningInfo contains merge mining (AuxPoW) parameters.
type CoinMergeMiningInfo struct {
	SupportsAuxPow    bool   `json:"supportsAuxpow"`
	AuxPowStartHeight uint64 `json:"auxpowStartHeight"`
	ChainID           int32  `json:"chainId"`
	VersionBit        uint32 `json:"versionBit"`
}

type bucket struct {
	tokens    float64
	lastCheck time.Time
	lastSeen  time.Time
	mu        sync.Mutex
}

func NewRateLimiter(cfg config.RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Stop stops the rate limiter cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// cleanupLoop periodically evicts stale buckets to prevent unbounded memory growth.
func (rl *RateLimiter) cleanupLoop() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC recovered in API RateLimiter cleanupLoop: %v\n", r)
		}
	}()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-10 * time.Minute)
			rl.buckets.Range(func(key, value interface{}) bool {
				b := value.(*bucket)
				b.mu.Lock()
				stale := b.lastSeen.Before(cutoff)
				b.mu.Unlock()
				if stale {
					rl.buckets.Delete(key)
				}
				return true
			})
		}
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	// Check whitelist
	// SECURITY: Use net.SplitHostPort for correct IPv6 handling (e.g., "[::1]:1234")
	clientIP, _, err := net.SplitHostPort(ip)
	if err != nil {
		// No port component - use the raw value (handles bare IPs)
		clientIP = ip
	}
	for _, whitelisted := range rl.cfg.Whitelist {
		if clientIP == whitelisted {
			return true
		}
	}

	// Get or create bucket
	now := time.Now()
	val, _ := rl.buckets.LoadOrStore(clientIP, &bucket{
		tokens:    float64(rl.cfg.RequestsPerSecond),
		lastCheck: now,
		lastSeen:  now,
	})
	b := val.(*bucket)

	b.mu.Lock()
	defer b.mu.Unlock()

	// Update last seen time for cleanup eviction
	b.lastSeen = now

	// Refill tokens
	elapsed := now.Sub(b.lastCheck).Seconds()
	b.tokens += elapsed * float64(rl.cfg.RequestsPerSecond)
	if b.tokens > float64(rl.cfg.RequestsPerSecond) {
		b.tokens = float64(rl.cfg.RequestsPerSecond)
	}
	b.lastCheck = now

	// Check if allowed
	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}
