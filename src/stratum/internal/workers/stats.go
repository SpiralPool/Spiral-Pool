// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package workers provides per-worker statistics tracking and aggregation.
//
// This package implements hashrate tracking, share counting, and performance
// metrics for individual mining workers. It supports configurable time windows
// and handles intermittent workers without skewing statistics.
//
// Security: All worker names are validated to prevent injection attacks.
// Worker data is scoped per-miner address to enforce trust boundaries.
package workers

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// validWorkerName matches safe worker names (alphanumeric, underscore, dash, dot)
// SECURITY: Used to prevent injection in database queries and log messages
var validWorkerName = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)

// pureHexString detects strings that are only hex characters (potential hash confusion)
var pureHexString = regexp.MustCompile(`^[a-fA-F0-9]{32,}$`)

// TimeWindow represents a hashrate calculation time window.
type TimeWindow int

const (
	Window1m  TimeWindow = 1
	Window5m  TimeWindow = 5
	Window15m TimeWindow = 15
	Window1h  TimeWindow = 60
	Window24h TimeWindow = 1440
)

// AllWindows returns all supported time windows.
func AllWindows() []TimeWindow {
	return []TimeWindow{Window1m, Window5m, Window15m, Window1h, Window24h}
}

// String returns a human-readable window name.
func (w TimeWindow) String() string {
	switch w {
	case Window1m:
		return "1m"
	case Window5m:
		return "5m"
	case Window15m:
		return "15m"
	case Window1h:
		return "1h"
	case Window24h:
		return "24h"
	default:
		return fmt.Sprintf("%dm", int(w))
	}
}

// WorkerStats represents statistics for a single worker.
type WorkerStats struct {
	mu              sync.RWMutex       `json:"-"` // Protects concurrent access
	Miner           string             `json:"miner"`
	Worker          string             `json:"worker"`
	Hashrates       map[string]float64 `json:"hashrates"`       // Hashrate per window (1m, 5m, 15m, 1h, 24h)
	CurrentHashrate float64            `json:"currentHashrate"` // Most recent (1m) hashrate
	AverageHashrate float64            `json:"averageHashrate"` // 24h average
	SharesSubmitted int64              `json:"sharesSubmitted"` // Total shares in window
	SharesAccepted  int64              `json:"sharesAccepted"`  // Accepted shares in window
	SharesRejected  int64              `json:"sharesRejected"`  // Rejected shares in window
	SharesStale     int64              `json:"sharesStale"`     // Stale shares in window
	AcceptanceRate  float64            `json:"acceptanceRate"`  // Percentage (0-100)
	LastShare       time.Time          `json:"lastShare"`       // Time of last share
	LastSeen        time.Time          `json:"lastSeen"`        // Time of last activity
	Connected       bool               `json:"connected"`       // Currently connected
	Difficulty      float64            `json:"difficulty"`      // Current difficulty
	UserAgent       string             `json:"userAgent"`       // Miner user-agent
	IPAddress       string             `json:"-"`               // Not exposed in API (security)
	SessionDuration time.Duration      `json:"sessionDuration"` // Current session length
	TotalDifficulty float64            `json:"totalDifficulty"` // Sum of share difficulties
	EffectiveShares float64            `json:"effectiveShares"` // Difficulty-weighted share count
}

// WorkerHashratePoint represents a single hashrate measurement for time-series.
type WorkerHashratePoint struct {
	Timestamp time.Time `json:"timestamp"`
	Hashrate  float64   `json:"hashrate"`
	Window    string    `json:"window"` // Which window this represents
}

// WorkerSummary is a lightweight summary for listing workers.
type WorkerSummary struct {
	Miner          string    `json:"miner"`
	Worker         string    `json:"worker"`
	Hashrate       float64   `json:"hashrate"`       // Current (1m) hashrate
	SharesPerSec   float64   `json:"sharesPerSec"`   // Shares per second
	AcceptanceRate float64   `json:"acceptanceRate"` // Acceptance percentage
	LastShare      time.Time `json:"lastShare"`
	Connected      bool      `json:"connected"`
}

// StatsCollector periodically collects and aggregates worker statistics.
type StatsCollector struct {
	db        StatsDatabase
	cfg       *StatsConfig
	logger    *zap.SugaredLogger
	poolID    string
	algorithm string

	// In-memory cache for real-time stats
	cache sync.Map // map[string]*WorkerStats (key: miner:worker)

	// Session tracking for connected workers
	sessions sync.Map // map[uint64]*workerSession (key: session ID)

	// Control
	done   chan struct{}
	ticker *time.Ticker
	wg     sync.WaitGroup
}

// workerSession tracks a currently connected worker session.
// SECURITY: All mutable fields are protected by mutex to prevent race conditions
type workerSession struct {
	mu          sync.Mutex // Protects all mutable fields below
	SessionID   uint64
	Miner       string
	Worker      string
	ConnectedAt time.Time
	LastShare   time.Time
	Difficulty  float64
	UserAgent   string
	IPAddress   string
	Shares      sessionShares
}

// sessionShares tracks share counts for a session.
type sessionShares struct {
	Submitted int64
	Accepted  int64
	Rejected  int64
	Stale     int64
	TotalDiff float64
}

// StatsDatabase defines the interface for worker stats persistence.
type StatsDatabase interface {
	// GetWorkerStats retrieves stats for a specific worker
	GetWorkerStats(ctx context.Context, miner, worker string, windowMinutes int) (*WorkerStats, error)
	// GetMinerWorkers retrieves all workers for a miner
	GetMinerWorkers(ctx context.Context, miner string, windowMinutes int) ([]*WorkerSummary, error)
	// GetAllWorkers retrieves all active workers (for admin)
	GetAllWorkers(ctx context.Context, windowMinutes int, limit int) ([]*WorkerSummary, error)
	// GetWorkerHashrateHistory retrieves historical hashrate data
	GetWorkerHashrateHistory(ctx context.Context, miner, worker string, hours int) ([]*WorkerHashratePoint, error)
	// RecordWorkerSnapshot stores a worker stats snapshot
	RecordWorkerSnapshot(ctx context.Context, stats *WorkerStats) error
	// CleanupOldSnapshots removes old historical data
	CleanupOldSnapshots(ctx context.Context, retentionDays int) error
}

// StatsConfig configures the stats collector.
type StatsConfig struct {
	Enabled            bool          // Enable worker stats tracking
	CollectionInterval time.Duration // How often to snapshot stats (default: 1min)
	RetentionDays      int           // How long to keep history (default: 30 days)
	MaxWorkersPerMiner int           // Limit workers per miner (DoS protection)
	MaxTotalWorkers    int           // Global limit (memory protection)
}

// DefaultStatsConfig returns sensible defaults.
func DefaultStatsConfig() *StatsConfig {
	return &StatsConfig{
		Enabled:            true,
		CollectionInterval: 1 * time.Minute,
		RetentionDays:      30,
		MaxWorkersPerMiner: 1000, // Reasonable for large farms
		MaxTotalWorkers:    100000,
	}
}

// NewStatsCollector creates a new worker statistics collector.
func NewStatsCollector(db StatsDatabase, cfg *StatsConfig, poolID string, algorithm string, logger *zap.Logger) *StatsCollector {
	if cfg == nil {
		cfg = DefaultStatsConfig()
	}
	if algorithm == "" {
		algorithm = "sha256d"
	}
	return &StatsCollector{
		db:        db,
		cfg:       cfg,
		logger:    logger.Sugar(),
		poolID:    poolID,
		algorithm: algorithm,
		done:      make(chan struct{}),
	}
}

// Start begins the periodic collection process.
func (sc *StatsCollector) Start(ctx context.Context) error {
	if !sc.cfg.Enabled {
		sc.logger.Info("Worker stats collection disabled")
		return nil
	}

	sc.ticker = time.NewTicker(sc.cfg.CollectionInterval)
	sc.wg.Add(1)

	go func() {
		defer sc.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				sc.logger.Errorw("PANIC recovered in stats collection goroutine", "panic", r)
			}
		}()
		sc.logger.Info("Worker stats collector started")

		for {
			select {
			case <-sc.ticker.C:
				if err := sc.collectAndStore(ctx); err != nil {
					sc.logger.Warnw("Failed to collect worker stats", "error", err)
				}
			case <-sc.done:
				sc.logger.Info("Worker stats collector stopping")
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start cleanup routine (runs daily)
	sc.wg.Add(1)
	go func() {
		defer sc.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				sc.logger.Errorw("PANIC recovered in stats cleanup goroutine", "panic", r)
			}
		}()
		cleanupTicker := time.NewTicker(24 * time.Hour)
		defer cleanupTicker.Stop()

		for {
			select {
			case <-cleanupTicker.C:
				if err := sc.db.CleanupOldSnapshots(ctx, sc.cfg.RetentionDays); err != nil {
					sc.logger.Warnw("Failed to cleanup old snapshots", "error", err)
				}
			case <-sc.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// Stop gracefully shuts down the collector.
func (sc *StatsCollector) Stop() {
	if sc.ticker != nil {
		sc.ticker.Stop()
	}
	close(sc.done)
	sc.wg.Wait()
}

// ValidateWorkerName checks if a worker name is safe.
// SECURITY: Always validate before use in queries or logs.
// Rejects pure hex strings (32+ chars) to prevent hash/address confusion.
func ValidateWorkerName(name string) bool {
	if name == "" {
		return true // Empty is allowed (becomes "default")
	}
	if !validWorkerName.MatchString(name) {
		return false
	}
	// Reject pure hex strings that could be confused with hashes/addresses
	if pureHexString.MatchString(name) {
		return false
	}
	return true
}

// NormalizeWorkerName returns a safe worker name.
func NormalizeWorkerName(name string) string {
	if name == "" {
		return "default"
	}
	if !ValidateWorkerName(name) {
		return "invalid"
	}
	return name
}

// RegisterSession registers a new worker session.
func (sc *StatsCollector) RegisterSession(sessionID uint64, miner, worker, userAgent, ipAddress string, difficulty float64) {
	worker = NormalizeWorkerName(worker)

	// Check for session ID reuse
	if existing, loaded := sc.sessions.Load(sessionID); loaded {
		oldSession := existing.(*workerSession)
		sc.logger.Warnw("Session ID reuse detected, overwriting previous session",
			"sessionID", sessionID,
			"oldMiner", oldSession.Miner,
			"oldWorker", oldSession.Worker,
			"newMiner", miner,
			"newWorker", worker)
	}

	session := &workerSession{
		SessionID:   sessionID,
		Miner:       miner,
		Worker:      worker,
		ConnectedAt: time.Now(),
		Difficulty:  difficulty,
		UserAgent:   userAgent,
		IPAddress:   ipAddress,
	}
	sc.sessions.Store(sessionID, session)

	// Update cache to mark worker as connected
	key := workerKey(miner, worker)
	if val, ok := sc.cache.Load(key); ok {
		stats := val.(*WorkerStats)
		stats.mu.Lock()
		stats.Connected = true
		stats.LastSeen = time.Now()
		stats.mu.Unlock()
	}
}

// UnregisterSession removes a worker session.
func (sc *StatsCollector) UnregisterSession(sessionID uint64) {
	if val, ok := sc.sessions.LoadAndDelete(sessionID); ok {
		session := val.(*workerSession)
		key := workerKey(session.Miner, session.Worker)

		// Check if any other sessions exist for this worker
		hasOtherSessions := false
		sc.sessions.Range(func(k, v interface{}) bool {
			s := v.(*workerSession)
			if s.Miner == session.Miner && s.Worker == session.Worker {
				hasOtherSessions = true
				return false
			}
			return true
		})

		if !hasOtherSessions {
			if val, ok := sc.cache.Load(key); ok {
				stats := val.(*WorkerStats)
				stats.mu.Lock()
				stats.Connected = false
				stats.mu.Unlock()
			}
		}
	}
}

// RecordShare records a share submission for a worker.
// SECURITY: Uses mutex to prevent race conditions on session state updates
func (sc *StatsCollector) RecordShare(sessionID uint64, accepted bool, stale bool, difficulty float64) {
	val, ok := sc.sessions.Load(sessionID)
	if !ok {
		return
	}
	session := val.(*workerSession)

	// Lock session for thread-safe updates
	session.mu.Lock()
	session.Shares.Submitted++
	session.Shares.TotalDiff += difficulty
	session.LastShare = time.Now()

	if accepted {
		session.Shares.Accepted++
	} else {
		session.Shares.Rejected++
		if stale {
			session.Shares.Stale++
		}
	}
	// Copy immutable values while holding lock
	miner := session.Miner
	worker := session.Worker
	session.mu.Unlock()

	// Update real-time cache
	key := workerKey(miner, worker)
	sc.updateCache(key, session, difficulty, accepted, stale)
}

// UpdateDifficulty updates the difficulty for a session.
// SECURITY: Uses mutex to prevent race conditions on session state updates
func (sc *StatsCollector) UpdateDifficulty(sessionID uint64, difficulty float64) {
	if val, ok := sc.sessions.Load(sessionID); ok {
		session := val.(*workerSession)
		session.mu.Lock()
		session.Difficulty = difficulty
		session.mu.Unlock()
	}
}

// GetWorkerStats returns stats for a specific worker from cache or database.
func (sc *StatsCollector) GetWorkerStats(ctx context.Context, miner, worker string) (*WorkerStats, error) {
	worker = NormalizeWorkerName(worker)
	key := workerKey(miner, worker)

	// Try cache first for real-time data
	if val, ok := sc.cache.Load(key); ok {
		return val.(*WorkerStats), nil
	}

	// Fall back to database
	return sc.db.GetWorkerStats(ctx, miner, worker, int(Window24h))
}

// GetMinerWorkers returns all workers for a miner within the specified time window.
// windowMinutes controls how far back to look for active workers:
//   - 15 (default): Shows workers active in last 15 minutes (efficient, real-time view)
//   - 60: Shows workers active in last hour
//   - 1440: Shows workers active in last 24 hours (comprehensive but includes stale workers)
func (sc *StatsCollector) GetMinerWorkers(ctx context.Context, miner string, windowMinutes int) ([]*WorkerSummary, error) {
	// Clamp to valid range
	if windowMinutes < 1 {
		windowMinutes = int(Window15m) // Default to efficient 15-minute window
	}
	if windowMinutes > 10080 { // Max 7 days
		windowMinutes = 10080
	}
	return sc.db.GetMinerWorkers(ctx, miner, windowMinutes)
}

// GetWorkerHistory returns hashrate history for graphs.
func (sc *StatsCollector) GetWorkerHistory(ctx context.Context, miner, worker string, hours int) ([]*WorkerHashratePoint, error) {
	worker = NormalizeWorkerName(worker)
	return sc.db.GetWorkerHashrateHistory(ctx, miner, worker, hours)
}

// GetActiveWorkerCount returns the count of currently connected workers.
func (sc *StatsCollector) GetActiveWorkerCount() int {
	count := 0
	sc.sessions.Range(func(k, v interface{}) bool {
		count++
		return true
	})
	return count
}

// collectAndStore collects current stats and stores to database.
// SECURITY: Uses mutex when reading session data to prevent race conditions
func (sc *StatsCollector) collectAndStore(ctx context.Context) error {
	// Aggregate stats from all sessions
	minerWorkers := make(map[string]*WorkerStats)

	// Track earliest connection time per worker for accurate hashrate calculation
	workerConnectedAt := make(map[string]time.Time)

	sc.sessions.Range(func(k, v interface{}) bool {
		session := v.(*workerSession)

		// Lock session to read all fields atomically
		session.mu.Lock()
		miner := session.Miner
		worker := session.Worker
		sharesSubmitted := session.Shares.Submitted
		sharesAccepted := session.Shares.Accepted
		sharesRejected := session.Shares.Rejected
		sharesStale := session.Shares.Stale
		totalDiff := session.Shares.TotalDiff
		lastShare := session.LastShare
		difficulty := session.Difficulty
		userAgent := session.UserAgent
		connectedAt := session.ConnectedAt
		session.mu.Unlock()

		key := workerKey(miner, worker)

		stats, exists := minerWorkers[key]
		if !exists {
			stats = &WorkerStats{
				Miner:     miner,
				Worker:    worker,
				Connected: true,
				Hashrates: make(map[string]float64),
			}
			minerWorkers[key] = stats
		}

		// Aggregate session data (now using local copies)
		stats.SharesSubmitted += sharesSubmitted
		stats.SharesAccepted += sharesAccepted
		stats.SharesRejected += sharesRejected
		stats.SharesStale += sharesStale
		stats.TotalDifficulty += totalDiff

		if lastShare.After(stats.LastShare) {
			stats.LastShare = lastShare
		}
		stats.LastSeen = time.Now()
		stats.Difficulty = difficulty
		stats.UserAgent = userAgent

		// Track earliest connection for this worker (for hashrate calculation)
		if existing, ok := workerConnectedAt[key]; !ok || connectedAt.Before(existing) {
			workerConnectedAt[key] = connectedAt
		}

		return true
	})

	// Calculate hashrates and store
	now := time.Now()
	for _, stats := range minerWorkers {
		key := workerKey(stats.Miner, stats.Worker)

		// Calculate actual elapsed time since earliest connection
		// This is critical for accurate hashrate calculation
		elapsedSecs := float64(0)
		if connectedAt, ok := workerConnectedAt[key]; ok {
			elapsedSecs = now.Sub(connectedAt).Seconds()
		}

		// Calculate hashrate from difficulty using actual elapsed time
		// hashrate = sum(difficulty) * 2^32 / actual_elapsed_seconds
		// For each window, use min(elapsed, window) to avoid inflated rates for new sessions
		for _, window := range AllWindows() {
			windowSecs := float64(int(window) * 60)
			// Use the smaller of elapsed time or window size
			// This prevents inflated hashrates when session is shorter than window
			effectiveSecs := windowSecs
			if elapsedSecs > 0 && elapsedSecs < windowSecs {
				effectiveSecs = elapsedSecs
			}
			// If no elapsed time (shouldn't happen), fall back to window size
			if effectiveSecs <= 0 {
				effectiveSecs = windowSecs
			}
			hashrate := coin.CalculateHashrateForAlgorithm(stats.TotalDifficulty, effectiveSecs, sc.algorithm)
			stats.Hashrates[window.String()] = hashrate
		}

		// Store session duration for debugging/display
		if elapsedSecs > 0 {
			stats.SessionDuration = time.Duration(elapsedSecs) * time.Second
		}

		stats.CurrentHashrate = stats.Hashrates[Window1m.String()]
		stats.AverageHashrate = stats.Hashrates[Window24h.String()]

		// Calculate acceptance rate
		if stats.SharesSubmitted > 0 {
			stats.AcceptanceRate = float64(stats.SharesAccepted) / float64(stats.SharesSubmitted) * 100
		}

		// Store snapshot
		if err := sc.db.RecordWorkerSnapshot(ctx, stats); err != nil {
			sc.logger.Warnw("Failed to store worker snapshot",
				"miner", stats.Miner,
				"worker", stats.Worker,
				"error", err)
		}

		// Update cache
		sc.cache.Store(workerKey(stats.Miner, stats.Worker), stats)
	}

	return nil
}

// updateCache updates the in-memory cache with real-time data.
func (sc *StatsCollector) updateCache(key string, session *workerSession, difficulty float64, accepted, stale bool) {
	val, _ := sc.cache.LoadOrStore(key, &WorkerStats{
		Miner:     session.Miner,
		Worker:    session.Worker,
		Connected: true,
		Hashrates: make(map[string]float64),
	})
	stats := val.(*WorkerStats)

	stats.mu.Lock()
	defer stats.mu.Unlock()

	stats.SharesSubmitted++
	stats.TotalDifficulty += difficulty
	stats.LastShare = time.Now()
	stats.LastSeen = time.Now()
	stats.Connected = true

	if accepted {
		stats.SharesAccepted++
	} else {
		stats.SharesRejected++
		if stale {
			stats.SharesStale++
		}
	}

	if stats.SharesSubmitted > 0 {
		stats.AcceptanceRate = float64(stats.SharesAccepted) / float64(stats.SharesSubmitted) * 100
	}
}

// workerKey creates a unique key for miner:worker.
func workerKey(miner, worker string) string {
	return miner + ":" + worker
}

// LoadFromConfig creates a StatsConfig from the main config.
func LoadFromConfig(cfg *config.Config) *StatsConfig {
	return &StatsConfig{
		Enabled:            true,
		CollectionInterval: 1 * time.Minute,
		RetentionDays:      cfg.Logging.MaxAgeDays, // Align with log retention
		MaxWorkersPerMiner: 1000,
		MaxTotalWorkers:    100000,
	}
}
