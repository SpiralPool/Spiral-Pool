// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package api provides worker statistics API endpoints.
//
// These endpoints expose per-worker hashrate data, share statistics,
// and historical performance data for dashboard visualization.
//
// Security: Worker names are validated to prevent injection.
// Miner addresses are validated against known address patterns.
package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// validWorkerPattern matches safe worker names
var validWorkerPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)

// WorkerStatsProvider interface for worker statistics (injected by pool).
type WorkerStatsProvider interface {
	GetWorkerStats(miner, worker string) (*WorkerStatsResponse, error)
	// GetMinerWorkers returns workers for a miner within the specified time window.
	// windowMinutes controls how far back to look for active workers:
	//   - 15 (default): Shows workers active in last 15 minutes (efficient, real-time view)
	//   - 60: Shows workers active in last hour
	//   - 1440: Shows workers active in last 24 hours (comprehensive but includes stale workers)
	GetMinerWorkers(miner string, windowMinutes int) ([]*WorkerSummaryResponse, error)
	GetWorkerHistory(miner, worker string, hours int) ([]*HashratePointResponse, error)
	GetAllWorkers(limit int) ([]*WorkerSummaryResponse, error)
	GetPoolHashrateHistory(hours int) ([]*HashratePointResponse, error)
}

// WorkerStatsResponse represents detailed worker statistics.
type WorkerStatsResponse struct {
	Miner           string             `json:"miner"`
	Worker          string             `json:"worker"`
	Hashrates       map[string]float64 `json:"hashrates"`
	CurrentHashrate float64            `json:"currentHashrate"`
	AverageHashrate float64            `json:"averageHashrate"`
	SharesSubmitted int64              `json:"sharesSubmitted"`
	SharesAccepted  int64              `json:"sharesAccepted"`
	SharesRejected  int64              `json:"sharesRejected"`
	AcceptanceRate  float64            `json:"acceptanceRate"`
	LastShare       string             `json:"lastShare"`
	Connected       bool               `json:"connected"`
	Difficulty      float64            `json:"difficulty"`
}

// WorkerSummaryResponse is a lightweight worker summary.
type WorkerSummaryResponse struct {
	Miner          string  `json:"miner"`
	Worker         string  `json:"worker"`
	Hashrate       float64 `json:"hashrate"`
	SharesPerSec   float64 `json:"sharesPerSec"`
	AcceptanceRate float64 `json:"acceptanceRate"`
	LastShare      string  `json:"lastShare"`
	Connected      bool    `json:"connected"`
}

// HashratePointResponse represents a single hashrate measurement.
type HashratePointResponse struct {
	Timestamp string  `json:"timestamp"`
	Hashrate  float64 `json:"hashrate"`
	Window    string  `json:"window,omitempty"`
}

// SetWorkerStatsProvider sets the worker stats provider.
func (s *Server) SetWorkerStatsProvider(provider WorkerStatsProvider) {
	s.workerStats = provider
}

// handleMinerWorkers handles GET /api/pools/{id}/miners/{address}/workers
// Returns all workers for a specific miner.
//
// Query parameters:
//   - window: Time window in minutes for filtering active workers (default: 15, max: 1440)
//     Common values: 15 (real-time), 60 (last hour), 1440 (last 24 hours)
//
// The window parameter controls which workers are considered "active":
//   - Smaller windows (15m) show only recently active workers (efficient, real-time)
//   - Larger windows (1440m/24h) show all workers with recent activity (comprehensive)
//
// Note: Worker detailed stats (GetWorkerStats) always use a 24h window for complete data.
func (s *Server) handleMinerWorkers(w http.ResponseWriter, r *http.Request, poolID, address string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate miner address
	if !validAddressPattern.MatchString(address) {
		http.Error(w, "Invalid miner address format", http.StatusBadRequest)
		return
	}

	// Parse window parameter (default: 15 minutes for efficiency)
	windowMinutes := 15
	if wParam := r.URL.Query().Get("window"); wParam != "" {
		if parsed, err := strconv.Atoi(wParam); err == nil && parsed >= 1 && parsed <= 1440 {
			windowMinutes = parsed
		}
	}

	if s.workerStats == nil {
		// AUDIT FIX (API-2): Use s.getDB() for HA-aware DB routing.
		// s.db is static; s.getDB() dynamically fetches the active node after failover.
		db := s.getDB()
		if db == nil {
			http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
			return
		}
		// Fallback to database if no provider set
		workers, err := db.GetMinerWorkers(r.Context(), address, windowMinutes)
		if err != nil {
			s.logger.Errorw("Failed to get miner workers", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Convert to response format
		response := make([]*WorkerSummaryResponse, 0, len(workers))
		for _, w := range workers {
			response = append(response, &WorkerSummaryResponse{
				Miner:          w.Miner,
				Worker:         w.Worker,
				Hashrate:       w.Hashrate,
				SharesPerSec:   w.SharesPerSec,
				AcceptanceRate: w.AcceptanceRate,
				LastShare:      w.LastShare.Format("2006-01-02T15:04:05Z"),
				Connected:      w.Connected,
			})
		}
		s.writeJSON(w, response)
		return
	}

	workers, err := s.workerStats.GetMinerWorkers(address, windowMinutes)
	if err != nil {
		s.logger.Errorw("Failed to get miner workers", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if workers == nil {
		workers = []*WorkerSummaryResponse{}
	}

	s.writeJSON(w, workers)
}

// handleWorkerStats handles GET /api/pools/{id}/miners/{address}/workers/{worker}
// Returns detailed statistics for a specific worker.
func (s *Server) handleWorkerStats(w http.ResponseWriter, r *http.Request, poolID, address, worker string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate inputs
	if !validAddressPattern.MatchString(address) {
		http.Error(w, "Invalid miner address format", http.StatusBadRequest)
		return
	}
	if worker != "" && !validWorkerPattern.MatchString(worker) {
		http.Error(w, "Invalid worker name format", http.StatusBadRequest)
		return
	}

	if worker == "" {
		worker = "default"
	}

	if s.workerStats == nil {
		// AUDIT FIX (API-2): Use s.getDB() for HA-aware DB routing.
		db := s.getDB()
		if db == nil {
			http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
			return
		}
		// Fallback to database
		stats, err := db.GetWorkerStats(r.Context(), address, worker, 1440) // 24h window
		if err != nil {
			s.logger.Errorw("Failed to get worker stats", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		response := &WorkerStatsResponse{
			Miner:           stats.Miner,
			Worker:          stats.Worker,
			CurrentHashrate: stats.Hashrate,
			SharesSubmitted: stats.SharesSubmitted,
			SharesAccepted:  stats.SharesAccepted,
			SharesRejected:  stats.SharesRejected,
			AcceptanceRate:  stats.AcceptanceRate,
			LastShare:       stats.LastShare.Format("2006-01-02T15:04:05Z"),
		}
		s.writeJSON(w, response)
		return
	}

	stats, err := s.workerStats.GetWorkerStats(address, worker)
	if err != nil {
		s.logger.Errorw("Failed to get worker stats", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, stats)
}

// handleWorkerHistory handles GET /api/pools/{id}/miners/{address}/workers/{worker}/history
// Returns hashrate history for graphs.
func (s *Server) handleWorkerHistory(w http.ResponseWriter, r *http.Request, poolID, address, worker string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate inputs
	if !validAddressPattern.MatchString(address) {
		http.Error(w, "Invalid miner address format", http.StatusBadRequest)
		return
	}
	if worker != "" && !validWorkerPattern.MatchString(worker) {
		http.Error(w, "Invalid worker name format", http.StatusBadRequest)
		return
	}

	if worker == "" {
		worker = "default"
	}

	// Parse hours parameter (default: 24, max: 720 = 30 days)
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil && parsed > 0 && parsed <= 720 {
			hours = parsed
		}
	}

	if s.workerStats == nil {
		// AUDIT FIX (API-2): Use s.getDB() for HA-aware DB routing.
		db := s.getDB()
		if db == nil {
			http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
			return
		}
		// Fallback to database
		history, err := db.GetWorkerHashrateHistory(r.Context(), address, worker, hours)
		if err != nil {
			s.logger.Errorw("Failed to get worker history", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		response := make([]*HashratePointResponse, 0, len(history))
		for _, h := range history {
			response = append(response, &HashratePointResponse{
				Timestamp: h.Timestamp.Format("2006-01-02T15:04:05Z"),
				Hashrate:  h.Hashrate,
				Window:    h.Window,
			})
		}
		s.writeJSON(w, response)
		return
	}

	history, err := s.workerStats.GetWorkerHistory(address, worker, hours)
	if err != nil {
		s.logger.Errorw("Failed to get worker history", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if history == nil {
		history = []*HashratePointResponse{}
	}

	s.writeJSON(w, history)
}

// handlePoolHashrateHistory handles GET /api/pools/{id}/hashrate/history
// Returns pool-wide hashrate history for graphs.
func (s *Server) handlePoolHashrateHistory(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse hours parameter (default: 24, max: 720 = 30 days)
	hours := 24
	if h := r.URL.Query().Get("hours"); h != "" {
		if parsed, err := strconv.Atoi(h); err == nil && parsed > 0 && parsed <= 720 {
			hours = parsed
		}
	}

	if s.workerStats != nil {
		history, err := s.workerStats.GetPoolHashrateHistory(hours)
		if err != nil {
			s.logger.Errorw("Failed to get pool hashrate history", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if history == nil {
			history = []*HashratePointResponse{}
		}
		s.writeJSON(w, history)
		return
	}

	// AUDIT FIX (API-2): Use s.getDB() for HA-aware DB routing.
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	// Fallback to database
	history, err := db.GetPoolHashrateHistory(r.Context(), hours)
	if err != nil {
		s.logger.Errorw("Failed to get pool hashrate history", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response := make([]*HashratePointResponse, 0, len(history))
	for _, h := range history {
		response = append(response, &HashratePointResponse{
			Timestamp: h.Timestamp.Format("2006-01-02T15:04:05Z"),
			Hashrate:  h.Hashrate,
			Window:    h.Window,
		})
	}
	s.writeJSON(w, response)
}

// handlePoolWorkers handles GET /api/pools/{id}/workers (admin)
// Returns all workers across all miners (requires auth).
func (s *Server) handlePoolWorkers(w http.ResponseWriter, r *http.Request, poolID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse limit parameter (default: 100, max: 1000)
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	if s.workerStats != nil {
		workers, err := s.workerStats.GetAllWorkers(limit)
		if err != nil {
			s.logger.Errorw("Failed to get all workers", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if workers == nil {
			workers = []*WorkerSummaryResponse{}
		}
		s.writeJSON(w, workers)
		return
	}

	// AUDIT FIX (API-2): Use s.getDB() for HA-aware DB routing.
	db := s.getDB()
	if db == nil {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}
	// Fallback to database
	workers, err := db.GetAllWorkers(r.Context(), 15, limit)
	if err != nil {
		s.logger.Errorw("Failed to get all workers", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response := make([]*WorkerSummaryResponse, 0, len(workers))
	for _, w := range workers {
		response = append(response, &WorkerSummaryResponse{
			Miner:          w.Miner,
			Worker:         w.Worker,
			Hashrate:       w.Hashrate,
			SharesPerSec:   w.SharesPerSec,
			AcceptanceRate: w.AcceptanceRate,
			LastShare:      w.LastShare.Format("2006-01-02T15:04:05Z"),
			Connected:      w.Connected,
		})
	}
	s.writeJSON(w, response)
}

// parseWorkerPath extracts miner, worker, and action from path like:
// /miners/{address}/workers/{worker}/history
func parseWorkerPath(path string) (address, worker, action string) {
	parts := strings.Split(path, "/")

	// Find "miners" index
	minersIdx := -1
	for i, p := range parts {
		if p == "miners" {
			minersIdx = i
			break
		}
	}

	if minersIdx == -1 || minersIdx+1 >= len(parts) {
		return "", "", ""
	}

	address = parts[minersIdx+1]

	// Find "workers" index
	workersIdx := -1
	for i := minersIdx + 2; i < len(parts); i++ {
		if parts[i] == "workers" {
			workersIdx = i
			break
		}
	}

	if workersIdx == -1 {
		return address, "", ""
	}

	if workersIdx+1 < len(parts) {
		worker = parts[workersIdx+1]
	}

	if workersIdx+2 < len(parts) {
		action = parts[workersIdx+2]
	}

	return address, worker, action
}
