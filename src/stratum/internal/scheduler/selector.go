// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package scheduler

import (
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RoutingMode controls how the Smart Port decides which coin to mine.
type RoutingMode string

const (
	// RoutingModeTime distributes mining time by configured coin weights on a 24-hour schedule.
	// This is the default and backward-compatible mode.
	RoutingModeTime RoutingMode = "TIME"

	// RoutingModeDifficulty always selects the coin with the lowest current network difficulty.
	// Coin weights are ignored; the decision is made dynamically on each evaluation.
	RoutingModeDifficulty RoutingMode = "DIFFICULTY"
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
	Symbol    string
	Weight    int      // percentage of daily mining time (0-100, all weights must sum to 100)
	StartHour *float64 // optional custom start hour (0-23.99) in configured timezone
}

// timeSlot represents a contiguous time window in the 24-hour cycle assigned to a coin.
type timeSlot struct {
	symbol    string
	startFrac float64 // 0.0–1.0, fraction of 24 hours
	endFrac   float64 // 0.0–1.0, fraction of 24 hours
}

// Selector decides which coin a miner should be mining. It supports two routing
// modes: TIME (24-hour weight-based schedule) and DIFFICULTY (always pick the
// coin with the lowest current network difficulty).
type Selector struct {
	mu           sync.RWMutex
	monitor      *Monitor
	allowedCoins []string
	excludeCoins map[string]bool // coins never selected in DIFFICULTY mode
	preferCoin   string
	minTimeOnCoin time.Duration
	mode         RoutingMode // routing strategy (default: RoutingModeTime)

	// Time-based scheduling
	coinWeights []CoinWeight
	timeSlots   []timeSlot
	anchorFrac  float64          // Schedule anchor as fraction of day (0.0–1.0); slots are relative to this
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
	ExcludeCoins  []string         // coins never selected in DIFFICULTY mode
	CoinWeights   []CoinWeight
	PreferCoin    string
	MinTimeOnCoin time.Duration
	Location      *time.Location   // Timezone for 24h schedule (default: UTC)
	Logger        *zap.Logger
	NowFunc       func() time.Time // injectable clock for testing (default: time.Now)
	Mode          RoutingMode      // routing strategy (default: RoutingModeTime)
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

	mode := cfg.Mode
	if mode != RoutingModeTime && mode != RoutingModeDifficulty {
		mode = RoutingModeTime // default / unknown values fall back to TIME
	}

	excludeCoins := make(map[string]bool, len(cfg.ExcludeCoins))
	for _, sym := range cfg.ExcludeCoins {
		excludeCoins[strings.ToUpper(sym)] = true
	}

	s := &Selector{
		monitor:          cfg.Monitor,
		allowedCoins:     cfg.AllowedCoins,
		excludeCoins:     excludeCoins,
		coinWeights:      cfg.CoinWeights,
		preferCoin:       cfg.PreferCoin,
		minTimeOnCoin:    cfg.MinTimeOnCoin,
		mode:             mode,
		location:         loc,
		sessionCoins:     make(map[uint64]*sessionCoinState),
		maxSwitchHistory: 1000,
		nowFunc:          nowFunc,
		logger:           logger.Sugar().Named("coin-selector"),
	}

	s.timeSlots, s.anchorFrac = buildTimeSlots(cfg.CoinWeights)

	return s
}

// buildTimeSlots converts coin weights into contiguous 24-hour time slots.
// Coins are sorted by StartHour (if set), then alphabetically — matching the
// dashboard's schedule computation. The first coin's StartHour (default 0.0 =
// midnight) anchors the schedule; subsequent coins are stacked contiguously.
//
// Returns the slots (in anchor-relative space, 0.0–1.0) and the anchor
// fraction (absolute position in the 24h day, 0.0–1.0). SelectCoin adjusts
// the wall-clock day fraction by the anchor before looking up a slot.
//
// Example without start_hour: DGB:80, BCH:15, BTC:5 (total 100) →
//
//	DGB: 0.00–0.80 (00:00–19:12 local)
//	BCH: 0.80–0.95 (19:12–22:48 local)
//	BTC: 0.95–1.00 (22:48–24:00 local)
//	anchorFrac = 0.0
//
// Example with start_hour: (start_hour=22, weight=8), DGB(weight=92) →
//
//	: 0.00–0.08 (22:00–23:55 local)
//	DGB: 0.08–1.00 (23:55–22:00 next day)
//	anchorFrac = 22/24 ≈ 0.9167
func buildTimeSlots(weights []CoinWeight) ([]timeSlot, float64) {
	// Filter to positive weights
	active := make([]CoinWeight, 0, len(weights))
	totalWeight := 0
	for _, cw := range weights {
		if cw.Weight > 0 {
			active = append(active, cw)
			totalWeight += cw.Weight
		}
	}
	if totalWeight == 0 {
		return nil, 0
	}

	// Sort by StartHour (coins with start_hour first, ordered by hour),
	// then alphabetically — matches dashboard.py schedule logic.
	sort.SliceStable(active, func(i, j int) bool {
		iHas := active[i].StartHour != nil
		jHas := active[j].StartHour != nil
		if iHas != jHas {
			return iHas // coins with start_hour come first
		}
		if iHas && jHas {
			return *active[i].StartHour < *active[j].StartHour
		}
		return active[i].Symbol < active[j].Symbol
	})

	// Anchor: first coin's StartHour (default 0.0 = midnight)
	// Clamp to [0, 24) — out-of-range values would produce nonsensical schedules.
	anchorHour := 0.0
	if active[0].StartHour != nil {
		anchorHour = *active[0].StartHour
		if anchorHour < 0 {
			anchorHour = 0
		} else if anchorHour >= 24 {
			anchorHour = 0
		}
	}
	anchorFrac := anchorHour / 24.0

	// Build slots in anchor-relative space (0.0–1.0)
	var slots []timeSlot
	cursor := 0.0
	for _, cw := range active {
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
	return slots, anchorFrac
}

// SelectCoin determines which coin a session should be mining right now.
// In TIME mode it uses the configured weight schedule; in DIFFICULTY mode it
// always picks the coin with the lowest current network difficulty.
func (s *Selector) SelectCoin(sessionID uint64) CoinSelection {
	if s.mode == RoutingModeDifficulty {
		return s.selectByDifficulty(sessionID)
	}

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

	// Shift into anchor-relative space: if the schedule starts at 22:00
	// (anchorFrac=0.917), then 22:00 maps to 0.0 and 21:59 maps to ~1.0.
	dayFraction = dayFraction - s.anchorFrac
	if dayFraction < 0 {
		dayFraction += 1.0
	}
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
		st, ok := s.monitor.GetState(chosen)
		if !ok || !st.Available || st.NetworkDiff <= 0 {
			// Chosen coin is down or not yet registered — pick the next available coin in the slot list
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

// Mode returns the routing mode this selector was created with.
func (s *Selector) Mode() RoutingMode {
	return s.mode
}

// selectByDifficulty picks the coin with the lowest current network difficulty
// among all allowed coins that are currently available. It applies the same
// min_time_on_coin guard as the time-based path to prevent thrashing.
func (s *Selector) selectByDifficulty(sessionID uint64) CoinSelection {
	s.mu.RLock()
	state, hasState := s.sessionCoins[sessionID]
	s.mu.RUnlock()

	currentCoin := ""
	if hasState {
		currentCoin = state.currentCoin

		// Enforce minimum time on coin — identical guard to time-based path.
		// Bypass the guard if the current coin is excluded or unavailable so the
		// miner is moved to an eligible coin as soon as possible.
		currentAvailable := true
		currentExcluded := s.excludeCoins[currentCoin]
		if s.monitor != nil {
			if st, ok := s.monitor.GetState(currentCoin); !ok || !st.Available || st.NetworkDiff <= 0 {
				currentAvailable = false
			}
		}
		if currentAvailable && !currentExcluded && s.minTimeOnCoin > 0 && time.Since(state.assignedAt) < s.minTimeOnCoin {
			return CoinSelection{
				Symbol:  currentCoin,
				Reason:  "min_time_not_elapsed",
				Changed: false,
			}
		}
	}

	// Gather difficulty states for all allowed coins from the monitor,
	// skipping any coins in the exclude list.
	var candidates []CoinDiffState
	var excludedSymbols []string
	if s.monitor != nil {
		allStates := s.monitor.GetAllStates()
		for _, sym := range s.allowedCoins {
			if s.excludeCoins[sym] {
				excludedSymbols = append(excludedSymbols, sym)
				continue
			}
			if st, ok := allStates[sym]; ok && st.Available && st.NetworkDiff > 0 {
				candidates = append(candidates, st)
			}
		}
	}

	// Build a log-friendly slice of all evaluated coins with their difficulties.
	type coinDiffLog struct {
		Symbol string  `json:"symbol"`
		Diff   float64 `json:"diff"`
	}
	evaluated := make([]coinDiffLog, 0, len(candidates))
	for _, c := range candidates {
		evaluated = append(evaluated, coinDiffLog{Symbol: c.Symbol, Diff: c.NetworkDiff})
	}

	if len(candidates) == 0 {
		fallback := s.preferCoin
		if currentCoin != "" {
			fallback = currentCoin
		}
		s.logger.Warnw("No coins available for difficulty-based selection, using fallback",
			"mode", "DIFFICULTY",
			"fallback", fallback,
			"evaluated", evaluated,
			"excluded", excludedSymbols,
		)
		return CoinSelection{
			Symbol:  fallback,
			Reason:  "no_coins_available",
			Changed: false,
		}
	}

	// Sort ascending by NetworkDiff — lowest difficulty = easiest = highest priority.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].NetworkDiff < candidates[j].NetworkDiff
	})
	chosen := candidates[0]

	s.logger.Infow("Difficulty-based coin selection",
		"mode", "DIFFICULTY",
		"selected_coin", chosen.Symbol,
		"selected_diff", chosen.NetworkDiff,
		"reason", "lowest_difficulty",
		"candidates", evaluated,
		"excluded", excludedSymbols,
	)

	if currentCoin == "" {
		return CoinSelection{
			Symbol:  chosen.Symbol,
			Reason:  "initial_assignment_difficulty",
			Changed: true,
		}
	}
	if chosen.Symbol == currentCoin {
		return CoinSelection{
			Symbol:  currentCoin,
			Reason:  "difficulty_same",
			Changed: false,
		}
	}
	return CoinSelection{
		Symbol:  chosen.Symbol,
		Reason:  "difficulty_rotation",
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
