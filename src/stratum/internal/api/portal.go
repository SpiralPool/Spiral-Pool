// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package api - read-only miner portal.
//
// The portal lets a miner look up their own stats by wallet address without a
// dashboard login: a static page at /portal and one JSON endpoint,
// GET /api/portal/{address}, which gathers the address's recent hashrate,
// workers and found blocks from every pool. It exposes nothing the public
// per-address pool endpoints do not, and reads only.
package api

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

//go:embed portal
var portalFiles embed.FS

const (
	// portalWorkerWindowMinutes is the window for per-worker hashrate.
	portalWorkerWindowMinutes = 15
	// portalBlockLimit caps blocks returned per pool.
	portalBlockLimit = 50
	// portalRequestsPerSecond is the per-IP lookup rate; each lookup queries
	// every pool, so it is stricter than the general API limit.
	portalRequestsPerSecond = 2
	// portalQueryTimeout bounds one lookup across all pools.
	portalQueryTimeout = 10 * time.Second

	// portalCSP allows only the portal's own script, stylesheet and API calls.
	portalCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
		"img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
)

// portalStore is the per-pool data the portal reads.
type portalStore interface {
	GetMinerStats(ctx context.Context, address string) (*database.MinerStats, error)
	GetMinerWorkers(ctx context.Context, miner string, windowMinutes int) ([]*database.WorkerSummary, error)
	GetBlocksByMiner(ctx context.Context, miner string, limit int) ([]*database.Block, error)
}

// portalPool is one pool the portal searches.
type portalPool struct {
	PoolID    string
	Coin      string
	Algorithm string
	Store     portalStore
}

// portal serves the miner portal page and lookups.
type portal struct {
	pools   func() []portalPool // nil pools, or a nil Store, means no database
	limiter *RateLimiter
	logger  *zap.SugaredLogger
}

func newPortal(pools func() []portalPool, logger *zap.SugaredLogger) *portal {
	return &portal{
		pools: pools,
		limiter: NewRateLimiter(config.RateLimitConfig{
			Enabled:           true,
			RequestsPerSecond: portalRequestsPerSecond,
		}),
		logger: logger,
	}
}

func (p *portal) stop() {
	p.limiter.Stop()
}

// register adds the portal routes to mux.
func (p *portal) register(mux *http.ServeMux) {
	mux.HandleFunc("/portal", p.handleFiles)
	mux.HandleFunc("/portal/", p.handleFiles)
	mux.HandleFunc("/api/portal/", p.handleLookup)
}

// handleFiles serves the embedded page, script and stylesheet.
func (p *portal) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/portal")
	name = strings.TrimPrefix(name, "/")
	var contentType string
	switch name {
	case "", "index.html":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "portal.js":
		contentType = "text/javascript; charset=utf-8"
	case "portal.css":
		contentType = "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(portalFiles, "portal/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", portalCSP)
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(data)
}

// portalWorker is one worker in a lookup response.
type portalWorker struct {
	Worker            string    `json:"worker"`
	Hashrate          float64   `json:"hashrate"`
	HashrateFormatted string    `json:"hashrateFormatted"`
	LastShare         time.Time `json:"lastShare"`
	Connected         bool      `json:"connected"`
}

// portalBlock is one found block in a lookup response.
type portalBlock struct {
	Height               uint64    `json:"height"`
	Hash                 string    `json:"hash"`
	Status               string    `json:"status"`
	ConfirmationProgress float64   `json:"confirmationProgress"`
	Reward               float64   `json:"reward"`
	Worker               string    `json:"worker,omitempty"`
	Created              time.Time `json:"created"`
}

// portalCoin is the address's activity on one pool.
type portalCoin struct {
	PoolID            string         `json:"poolId"`
	Coin              string         `json:"coin"`
	Algorithm         string         `json:"algorithm"`
	Hashrate          float64        `json:"hashrate24h"`
	HashrateFormatted string         `json:"hashrate24hFormatted"`
	Shares            int64          `json:"shares24h"`
	LastShare         *time.Time     `json:"lastShare,omitempty"`
	Workers           []portalWorker `json:"workers"`
	Blocks            []portalBlock  `json:"blocks"`
}

// portalLookup is the response for GET /api/portal/{address}.
type portalLookup struct {
	Address     string       `json:"address"`
	Coins       []portalCoin `json:"coins"`
	GeneratedAt time.Time    `json:"generatedAt"`
}

// handleLookup serves GET /api/portal/{address}.
func (p *portal) handleLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !p.limiter.Allow(r.RemoteAddr) {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	address := strings.TrimPrefix(r.URL.Path, "/api/portal/")
	if !validAddressPattern.MatchString(address) {
		http.Error(w, "Invalid wallet address format", http.StatusBadRequest)
		return
	}

	var pools []portalPool
	if p.pools != nil {
		pools = p.pools()
	}
	if len(pools) == 0 {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), portalQueryTimeout)
	defer cancel()

	result := portalLookup{Address: address, Coins: []portalCoin{}, GeneratedAt: time.Now().UTC()}
	for _, pool := range pools {
		if pool.Store == nil {
			http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
			return
		}
		entry, active, err := p.lookupPool(ctx, pool, address)
		if err != nil {
			p.logger.Errorw("Portal lookup failed", "poolId", pool.PoolID, "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if active {
			result.Coins = append(result.Coins, entry)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		p.logger.Errorw("Failed to encode portal response", "error", err)
	}
}

// lookupPool gathers an address's activity on one pool. active is false when the
// address has no recent shares and no found blocks there.
func (p *portal) lookupPool(ctx context.Context, pool portalPool, address string) (portalCoin, bool, error) {
	entry := portalCoin{
		PoolID:    pool.PoolID,
		Coin:      pool.Coin,
		Algorithm: pool.Algorithm,
		Workers:   []portalWorker{},
		Blocks:    []portalBlock{},
	}

	stats, err := pool.Store.GetMinerStats(ctx, address)
	if err != nil {
		return entry, false, err
	}
	if stats != nil {
		entry.Hashrate = stats.Hashrate
		entry.Shares = stats.ShareCount
		if !stats.LastShare.IsZero() {
			last := stats.LastShare
			entry.LastShare = &last
		}
	}
	entry.HashrateFormatted = coin.FormatHashrateString(entry.Hashrate, pool.Algorithm)

	workers, err := pool.Store.GetMinerWorkers(ctx, address, portalWorkerWindowMinutes)
	if err != nil {
		return entry, false, err
	}
	for _, wk := range workers {
		entry.Workers = append(entry.Workers, portalWorker{
			Worker:            wk.Worker,
			Hashrate:          wk.Hashrate,
			HashrateFormatted: coin.FormatHashrateString(wk.Hashrate, pool.Algorithm),
			LastShare:         wk.LastShare,
			Connected:         wk.Connected,
		})
	}

	blocks, err := pool.Store.GetBlocksByMiner(ctx, address, portalBlockLimit)
	if err != nil {
		return entry, false, err
	}
	for _, b := range blocks {
		entry.Blocks = append(entry.Blocks, portalBlock{
			Height:               b.Height,
			Hash:                 b.Hash,
			Status:               b.Status,
			ConfirmationProgress: b.ConfirmationProgress,
			Reward:               b.Reward,
			Worker:               b.Source,
			Created:              b.Created,
		})
	}

	active := entry.Shares > 0 || len(entry.Workers) > 0 || len(entry.Blocks) > 0
	return entry, active, nil
}
