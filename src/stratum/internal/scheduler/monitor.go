// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package scheduler provides multi-coin scheduling, routing, and difficulty
// monitoring for the single-port multi-coin stratum server (v2.1).
//
// The Monitor watches network difficulty on all enabled SHA-256d coin pools
// and publishes change events to subscribers. The Selector assigns miners to
// coins based on time-weighted schedules. The MultiServer routes shares to
// the correct coin pool and hot-swaps job templates on coin switches.
package scheduler

import (
	"context"
	"math"
	"sync"
	"time"

	"go.uber.org/zap"
)

// CoinDifficultySource abstracts the difficulty/block-time data source for a coin.
// Satisfied by *pool.CoinPool.
type CoinDifficultySource interface {
	Symbol() string
	PoolID() string
	GetNetworkDifficulty() float64
	IsRunning() bool
}

// CoinDiffState holds the current network difficulty state for a single coin.
type CoinDiffState struct {
	Symbol      string
	PoolID      string
	NetworkDiff float64
	BlockTime   float64 // target block time in seconds
	LastUpdated time.Time
	Available   bool // false if node is down or coin pool not running
}

// DifficultyEvent is published when a coin's network difficulty changes significantly.
type DifficultyEvent struct {
	Symbol        string
	OldDiff       float64
	NewDiff       float64
	ChangePercent float64 // positive = harder, negative = easier
	Timestamp     time.Time
}

// Monitor watches network difficulty across all enabled SHA-256d coins.
type Monitor struct {
	mu          sync.RWMutex
	coins       map[string]*coinEntry // symbol -> entry
	subscribers []chan DifficultyEvent
	logger      *zap.SugaredLogger

	// Polling config
	pollInterval time.Duration

	// Lifecycle
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// coinEntry bundles the source and last-known state for a single coin.
type coinEntry struct {
	source    CoinDifficultySource
	state     CoinDiffState
	blockTime float64 // configured target block time (seconds)
}

// MonitorConfig holds configuration for creating a Monitor.
type MonitorConfig struct {
	PollInterval time.Duration // fallback polling interval (default 30s)
	Logger       *zap.Logger
}

// NewMonitor creates a new difficulty monitor.
func NewMonitor(cfg MonitorConfig) *Monitor {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Monitor{
		coins:        make(map[string]*coinEntry),
		pollInterval: cfg.PollInterval,
		logger:       logger.Sugar().Named("difficulty-monitor"),
	}
}

// RegisterCoin adds a coin to be monitored.
// blockTimeSec is the coin's target block time in seconds (e.g., 15 for DGB SHA-256d, 600 for BTC).
func (m *Monitor) RegisterCoin(source CoinDifficultySource, blockTimeSec float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	symbol := source.Symbol()
	m.coins[symbol] = &coinEntry{
		source:    source,
		blockTime: blockTimeSec,
		state: CoinDiffState{
			Symbol:    symbol,
			PoolID:    source.PoolID(),
			BlockTime: blockTimeSec,
		},
	}
	m.logger.Infow("Registered coin for difficulty monitoring",
		"symbol", symbol,
		"blockTimeSec", blockTimeSec,
	)
}

// Subscribe returns a channel that receives difficulty change events.
// The channel has a buffer of 64 to prevent blocking the monitor.
func (m *Monitor) Subscribe() chan DifficultyEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan DifficultyEvent, 64)
	m.subscribers = append(m.subscribers, ch)
	return ch
}

// Unsubscribe removes a subscriber channel.
// Safe to call after Stop() — if the channel was already closed by Stop(), this is a no-op.
func (m *Monitor) Unsubscribe(ch chan DifficultyEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, sub := range m.subscribers {
		if sub == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
	// Channel not found in subscribers list — already removed by Stop(). No-op.
}

// GetState returns the current difficulty state for a coin.
func (m *Monitor) GetState(symbol string) (CoinDiffState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.coins[symbol]
	if !ok {
		return CoinDiffState{}, false
	}
	return entry.state, true
}

// GetAllStates returns the current difficulty state for all monitored coins.
func (m *Monitor) GetAllStates() map[string]CoinDiffState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make(map[string]CoinDiffState, len(m.coins))
	for symbol, entry := range m.coins {
		states[symbol] = entry.state
	}
	return states
}

// ExpectedShareTime calculates the expected seconds between shares for a given
// hashrate (H/s) against a coin's current network difficulty.
// Formula: expectedTime = networkDiff * 2^32 / hashrate
func ExpectedShareTime(networkDiff float64, hashrate float64) float64 {
	if hashrate <= 0 || networkDiff <= 0 {
		return math.Inf(1)
	}
	return networkDiff * math.Pow(2, 32) / hashrate
}

// Start begins the polling loop. Call Stop() or cancel the context to shut down.
func (m *Monitor) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	go m.pollLoop(ctx)
	m.logger.Info("Difficulty monitor started")
}

// Stop gracefully shuts down the monitor.
func (m *Monitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()

	// Close all subscriber channels
	m.mu.Lock()
	for _, ch := range m.subscribers {
		close(ch)
	}
	m.subscribers = nil
	m.mu.Unlock()

	m.logger.Info("Difficulty monitor stopped")
}

// pollLoop periodically fetches network difficulty from all coin sources.
func (m *Monitor) pollLoop(ctx context.Context) {
	defer m.wg.Done()

	// Initial poll immediately
	m.poll()

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll()
		}
	}
}

// poll fetches current difficulty from all registered coins and publishes events.
func (m *Monitor) poll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for symbol, entry := range m.coins {
		if !entry.source.IsRunning() {
			if entry.state.Available {
				m.logger.Warnw("Coin pool no longer running, marking unavailable",
					"symbol", symbol,
				)
				entry.state.Available = false
			}
			continue
		}

		newDiff := entry.source.GetNetworkDifficulty()
		if newDiff <= 0 {
			// RPC returned zero/negative — coin daemon is likely syncing or unreachable.
			// Mark unavailable so the selector stops routing miners to it.
			if entry.state.Available {
				m.logger.Warnw("Coin returning zero difficulty, marking unavailable",
					"symbol", symbol,
				)
				entry.state.Available = false
			}
			continue
		}

		oldDiff := entry.state.NetworkDiff
		entry.state.NetworkDiff = newDiff
		entry.state.LastUpdated = time.Now()
		entry.state.Available = true

		// Publish event if difficulty changed by more than 0.1%
		if oldDiff > 0 {
			changePct := ((newDiff - oldDiff) / oldDiff) * 100
			if math.Abs(changePct) > 0.1 {
				event := DifficultyEvent{
					Symbol:        symbol,
					OldDiff:       oldDiff,
					NewDiff:       newDiff,
					ChangePercent: changePct,
					Timestamp:     time.Now(),
				}
				m.publishEvent(event)

				m.logger.Infow("Network difficulty changed",
					"symbol", symbol,
					"oldDiff", oldDiff,
					"newDiff", newDiff,
					"changePct", changePct,
				)
			}
		}
	}
}

// publishEvent sends an event to all subscribers (non-blocking).
// Must be called with m.mu held.
func (m *Monitor) publishEvent(event DifficultyEvent) {
	for _, ch := range m.subscribers {
		select {
		case ch <- event:
		default:
			// Subscriber is slow, drop event rather than block the monitor
		}
	}
}

// NotifyBlockFound can be called by the coordinator when a ZMQ block notification
// arrives for any coin, triggering an immediate re-poll of that coin's difficulty.
func (m *Monitor) NotifyBlockFound(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.coins[symbol]
	if !ok || !entry.source.IsRunning() {
		return
	}

	newDiff := entry.source.GetNetworkDifficulty()
	if newDiff <= 0 {
		return
	}

	oldDiff := entry.state.NetworkDiff
	entry.state.NetworkDiff = newDiff
	entry.state.LastUpdated = time.Now()
	entry.state.Available = true

	if oldDiff > 0 {
		changePct := ((newDiff - oldDiff) / oldDiff) * 100
		if math.Abs(changePct) > 0.1 {
			event := DifficultyEvent{
				Symbol:        symbol,
				OldDiff:       oldDiff,
				NewDiff:       newDiff,
				ChangePercent: changePct,
				Timestamp:     time.Now(),
			}
			m.publishEvent(event)

			m.logger.Infow("Difficulty updated on block notification",
				"symbol", symbol,
				"oldDiff", oldDiff,
				"newDiff", newDiff,
				"changePct", changePct,
			)
		}
	}
}
