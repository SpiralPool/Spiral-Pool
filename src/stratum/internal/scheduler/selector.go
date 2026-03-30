// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

// CoinSelection is the result of selecting the optimal coin for a miner.
type CoinSelection struct {
	Symbol  string
	Reason  string
	Changed bool // true if this is different from the miner's current coin
}

// SwitchEvent records a coin switch for audit/dashboard purposes.
type SwitchEvent struct {
	SessionID  uint64
	WorkerName string
	MinerClass string
	FromCoin   string
	ToCoin     string
	Reason     string
	Timestamp  time.Time
}

// CoinWeight holds the weight for a single coin.
type CoinWeight struct {
	Symbol string
	Weight int // percentage of daily mining time (0-100, all weights must sum to 100)
}

// timeSlot represents a contiguous time window in the 24-hour cycle assigned to a coin.
type timeSlot struct {
	symbol    string
	startFrac float64 // 0.0–1.0, fraction of 24 hours
	endFrac   float64 // 0.0–1.0, fraction of 24 hours
}

// Selector decides which coin a miner should be mining based on the current
// position in a 24-hour schedule (in the configured timezone). Weights map
// directly to time: 80% DGB = 19.2 hours/day on DGB.
type Selector struct {
	mu           sync.RWMutex
	monitor      *Monitor
	allowedCoins []string
	preferCoin   string
	minTimeOnCoin time.Duration

	// Time-based scheduling
	coinWeights []CoinWeight
	timeSlots   []timeSlot
	location    *time.Location   // Timezone for schedule (default: UTC)
	nowFunc     func() time.Time // injectable clock for testing

	// Per-session state
	sessionCoins map[uint64]*sessionCoinState

	// Switch history for dashboard
	switchHistory    []SwitchEvent
	maxSwitchHistory int

	logger *zap.SugaredLogger
}

// sessionCoinState tracks a session's current coin assignment.
type sessionCoinState struct {
	currentCoin string
	assignedAt  time.Time
	minerClass  string
}

// SelectorConfig holds configuration for creating a Selector.
type SelectorConfig struct {
	Monitor       *Monitor
	AllowedCoins  []string
	CoinWeights   []CoinWeight
	PreferCoin    string
	MinTimeOnCoin time.Duration
	Location      *time.Location   // Timezone for 24h schedule (default: UTC)
	Logger        *zap.Logger
	NowFunc       func() time.Time // injectable clock for testing (default: time.Now)
}

// NewSelector creates a new coin selector.
func NewSelector(cfg SelectorConfig) *Selector {
	if cfg.MinTimeOnCoin < 0 {
		cfg.MinTimeOnCoin = 0 // explicitly disabled
	} else if cfg.MinTimeOnCoin == 0 {
		cfg.MinTimeOnCoin = 60 * time.Second // default
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	nowFunc := cfg.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}

	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}

	s := &Selector{
		monitor:          cfg.Monitor,
		allowedCoins:     cfg.AllowedCoins,
		coinWeights:      cfg.CoinWeights,
		preferCoin:       cfg.PreferCoin,
		minTimeOnCoin:    cfg.MinTimeOnCoin,
		location:         loc,
		sessionCoins:     make(map[uint64]*sessionCoinState),
		maxSwitchHistory: 1000,
		nowFunc:          nowFunc,
		logger:           logger.Sugar().Named("coin-selector"),
	}

	s.timeSlots = buildTimeSlots(cfg.CoinWeights)

	return s
}

// buildTimeSlots converts coin weights into contiguous 24-hour time slots.
// Example: DGB:80, BCH:15, BTC:5 (total 100) →
//
//	DGB: 0.00–0.80 (00:00–19:12 local)
//	BCH: 0.80–0.95 (19:12–22:48 local)
//	BTC: 0.95–1.00 (22:48–24:00 local)
func buildTimeSlots(weights []CoinWeight) []timeSlot {
	totalWeight := 0
	for _, cw := range weights {
		if cw.Weight > 0 {
			totalWeight += cw.Weight
		}
	}
	if totalWeight == 0 {
		return nil
	}

	var slots []timeSlot
	cursor := 0.0
	for _, cw := range weights {
		if cw.Weight <= 0 {
			continue
		}
		frac := float64(cw.Weight) / float64(totalWeight)
		slots = append(slots, timeSlot{
			symbol:    cw.Symbol,
			startFrac: cursor,
			endFrac:   cursor + frac,
		})
		cursor += frac
	}
	// Fix floating-point: ensure last slot ends at exactly 1.0
	if len(slots) > 0 {
		slots[len(slots)-1].endFrac = 1.0
	}
	return slots
}

// SelectCoin determines which coin a session should be mining right now
// based on the current time (in the configured timezone) and the weight schedule.
func (s *Selector) SelectCoin(sessionID uint64) CoinSelection {
	s.mu.RLock()
	state, hasState := s.sessionCoins[sessionID]
	slots := s.timeSlots
	s.mu.RUnlock()

	if len(slots) == 0 {
		fallback := s.preferCoin
		if hasState {
			fallback = state.currentCoin
		}
		return CoinSelection{
			Symbol: fallback,
			Reason: "no_weights_configured",
		}
	}

	currentCoin := ""
	if hasState {
		currentCoin = state.currentCoin

		// Enforce minimum time on coin — but only if the current coin is still available
		currentAvailable := true
		if s.monitor != nil {
			if st, ok := s.monitor.GetState(currentCoin); !ok || !st.Available || st.NetworkDiff <= 0 {
				currentAvailable = false
			}
		}

		if currentAvailable && s.minTimeOnCoin > 0 && time.Since(state.assignedAt) < s.minTimeOnCoin {
			return CoinSelection{
				Symbol:  currentCoin,
				Reason:  "min_time_not_elapsed",
				Changed: false,
			}
		}
	}

	// Compute position in the 24-hour cycle (fraction 0.0–1.0)
	// Uses the configured timezone so the schedule aligns with user's local day.
	// DST-safe: compute actual day length to handle 23h/25h days correctly.
	now := s.nowFunc().In(s.location)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.location)
	startOfNextDay := startOfDay.AddDate(0, 0, 1)
	dayLengthSec := startOfNextDay.Sub(startOfDay).Seconds()
	if dayLengthSec <= 0 {
		dayLengthSec = 86400 // fallback
	}
	secondsIntoDay := now.Sub(startOfDay).Seconds()
	dayFraction := secondsIntoDay / dayLengthSec
	if dayFraction >= 1.0 {
		dayFraction = 0.9999999 // clamp to last slot
	}

	// Find which slot the current time falls into
	chosen := slots[0].symbol
	for _, slot := range slots {
		if dayFraction >= slot.startFrac && dayFraction < slot.endFrac {
			chosen = slot.symbol
			break
		}
	}

	// Check if the chosen coin is available
	if s.monitor != nil {
		if st, ok := s.monitor.GetState(chosen); ok && (!st.Available || st.NetworkDiff <= 0) {
			// Chosen coin is down — pick the next available coin in the slot list
			found := false
			for _, slot := range slots {
				if slot.symbol == chosen {
					continue
				}
				if st2, ok2 := s.monitor.GetState(slot.symbol); ok2 && st2.Available && st2.NetworkDiff > 0 {
					chosen = slot.symbol
					found = true
					break
				}
			}
			if !found {
				fallback := s.preferCoin
				if currentCoin != "" {
					fallback = currentCoin
				}
				return CoinSelection{
					Symbol:  fallback,
					Reason:  "no_coins_available",
					Changed: false,
				}
			}
		}
	}

	if currentCoin == "" {
		return CoinSelection{
			Symbol:  chosen,
			Reason:  "initial_assignment",
			Changed: true,
		}
	}

	if chosen == currentCoin {
		return CoinSelection{
			Symbol:  currentCoin,
			Reason:  "scheduled_same",
			Changed: false,
		}
	}

	return CoinSelection{
		Symbol:  chosen,
		Reason:  "scheduled_rotation",
		Changed: true,
	}
}

// AssignCoin records that a session has been assigned to a coin.
func (s *Selector) AssignCoin(sessionID uint64, symbol, workerName, minerClass string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, hadOld := s.sessionCoins[sessionID]
	s.sessionCoins[sessionID] = &sessionCoinState{
		currentCoin: symbol,
		assignedAt:  time.Now(),
		minerClass:  minerClass,
	}

	if hadOld && old.currentCoin != symbol {
		event := SwitchEvent{
			SessionID:  sessionID,
			WorkerName: workerName,
			MinerClass: minerClass,
			FromCoin:   old.currentCoin,
			ToCoin:     symbol,
			Reason:     "scheduled_rotation",
			Timestamp:  time.Now(),
		}
		s.switchHistory = append(s.switchHistory, event)
		if len(s.switchHistory) > s.maxSwitchHistory {
			// Copy to a new slice to release the old backing array (prevents memory leak)
			trimmed := make([]SwitchEvent, len(s.switchHistory)-1)
			copy(trimmed, s.switchHistory[1:])
			s.switchHistory = trimmed
		}

		s.logger.Infow("Coin switch",
			"sessionId", sessionID,
			"worker", workerName,
			"from", old.currentCoin,
			"to", symbol,
			"minerClass", minerClass,
		)
	}
}

// RemoveSession cleans up state for a disconnected session.
func (s *Selector) RemoveSession(sessionID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessionCoins, sessionID)
}

// GetSessionCoin returns the current coin assignment for a session.
func (s *Selector) GetSessionCoin(sessionID uint64) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.sessionCoins[sessionID]
	if !ok {
		return "", false
	}
	return state.currentCoin, true
}

// GetSwitchHistory returns recent switch events for the dashboard.
func (s *Selector) GetSwitchHistory(limit int) []SwitchEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.switchHistory) {
		limit = len(s.switchHistory)
	}
	start := len(s.switchHistory) - limit
	result := make([]SwitchEvent, limit)
	copy(result, s.switchHistory[start:])
	return result
}

// GetCoinDistribution returns how many sessions are on each coin.
func (s *Selector) GetCoinDistribution() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dist := make(map[string]int)
	for _, state := range s.sessionCoins {
		dist[state.currentCoin]++
	}
	return dist
}

// GetCoinWeights returns the configured coin weights.
func (s *Selector) GetCoinWeights() []CoinWeight {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]CoinWeight, len(s.coinWeights))
	copy(result, s.coinWeights)
	return result
}

// GetTimeSlots returns the computed 24-hour time slots for the dashboard.
func (s *Selector) GetTimeSlots() []timeSlot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]timeSlot, len(s.timeSlots))
	copy(result, s.timeSlots)
	return result
}

// GetTimezone returns the IANA timezone name used for schedule computation.
func (s *Selector) GetTimezone() string {
	return s.location.String()
}
