// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package vardiff implements variable difficulty adjustment for mining sessions.
//
// This implementation uses lock-free atomic operations for high-performance
// share processing. Variable difficulty adjustment follows standard mining pool
// practices as documented in Stratum protocol specifications.
package vardiff

import (
	"errors"
	"math"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
)

// Configuration validation errors
var (
	// ErrZeroMinDiff is returned when MinDiff is configured as zero or negative.
	// A zero minimum difficulty would cause division by zero in hashrate calculations
	// and could allow miners to submit trivially easy shares.
	// Note: Fractional difficulty (e.g., 0.001) IS valid for ESP32/lottery miners.
	ErrZeroMinDiff = errors.New("vardiff: MinDiff must be > 0 (got 0 or negative)")

	// ErrZeroMaxDiff is returned when MaxDiff is configured as zero or negative.
	ErrZeroMaxDiff = errors.New("vardiff: MaxDiff must be > 0 (got 0 or negative)")

	// ErrMinExceedsMax is returned when MinDiff > MaxDiff.
	ErrMinExceedsMax = errors.New("vardiff: MinDiff cannot exceed MaxDiff")

	// ErrZeroTargetTime is returned when TargetTime is configured as zero or negative.
	ErrZeroTargetTime = errors.New("vardiff: TargetTime must be > 0 (got 0 or negative)")
)

// Engine manages per-session difficulty adjustment.
type Engine struct {
	cfg       config.VarDiffConfig
	algorithm string // Mining algorithm for hashrate estimation ("sha256d" or "scrypt")
}

// SetAlgorithm sets the mining algorithm for accurate hashrate estimation.
// Must be called after construction when the coin's algorithm is known.
func (e *Engine) SetAlgorithm(algo string) {
	e.algorithm = algo
}

// SessionState holds VARDIFF state for a single session.
// All fields are accessed atomically for lock-free operation.
type SessionState struct {
	// Current difficulty (stored as float64 bits)
	difficultyBits atomic.Uint64

	// Share tracking
	shareCount    atomic.Uint64
	lastShareNano atomic.Int64 // UnixNano timestamp

	// Retarget tracking
	lastRetargetNano    atomic.Int64
	sharesSinceRetarget atomic.Uint64

	// Backoff tracking for stability
	// Counts consecutive retarget attempts that resulted in no change.
	// Used to apply exponential backoff to cooldown intervals, preventing
	// rapid retarget attempts when difficulty is already optimal.
	consecutiveNoChange atomic.Uint64

	// Configuration (immutable after init)
	minDiff    float64
	maxDiff    float64
	targetTime float64 // Per-session target time (from Spiral Router profile, 0 = use engine default)
}

// ValidateConfig validates a VarDiffConfig for correctness.
// Returns an error if the configuration would cause runtime issues.
// SECURITY: Call this at startup to fail fast on invalid configuration.
//
// Valid difficulty ranges:
//   - ESP32/lottery miners (300 KH/s): MinDiff ~0.001
//   - GPU miners: MinDiff ~1-100
//   - ASICs: MinDiff ~100-10000+
//
// Fractional difficulty IS valid and required for very low hashrate devices.
func ValidateConfig(cfg config.VarDiffConfig) error {
	// Must be positive (but can be fractional for ESP32 miners)
	if cfg.MinDiff <= 0 {
		return ErrZeroMinDiff
	}
	if cfg.MaxDiff <= 0 {
		return ErrZeroMaxDiff
	}
	if cfg.MinDiff > cfg.MaxDiff {
		return ErrMinExceedsMax
	}
	if cfg.TargetTime <= 0 {
		return ErrZeroTargetTime
	}
	return nil
}

// NewEngine creates a new VARDIFF engine.
// IMPORTANT: Call ValidateConfig() before NewEngine() to catch configuration errors.
func NewEngine(cfg config.VarDiffConfig) *Engine {
	return &Engine{cfg: cfg}
}

// NewEngineWithValidation creates a new VARDIFF engine after validating the config.
// Returns an error if the configuration is invalid.
// This is the recommended constructor for production use.
func NewEngineWithValidation(cfg config.VarDiffConfig) (*Engine, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg}, nil
}

// NewSessionState creates a new session state with initial difficulty.
// Uses global engine config for min/max/target.
func (e *Engine) NewSessionState(initialDiff float64) *SessionState {
	// SECURITY: Sanitize initial difficulty — NaN/Inf/zero would poison all future calculations
	if math.IsNaN(initialDiff) || math.IsInf(initialDiff, 0) || initialDiff <= 0 {
		initialDiff = e.cfg.MinDiff
	}
	state := &SessionState{
		minDiff:    e.cfg.MinDiff,
		maxDiff:    e.cfg.MaxDiff,
		targetTime: 0, // Use engine default
	}
	state.difficultyBits.Store(math.Float64bits(initialDiff))
	state.lastRetargetNano.Store(time.Now().UnixNano())
	return state
}

// NewSessionStateWithProfile creates a session state with miner-specific settings.
// This allows per-session target times based on Spiral Router's miner classification.
// Parameters:
//   - initialDiff: Starting difficulty from Spiral Router profile
//   - minDiff: Minimum difficulty for this miner class (0 = use engine default)
//   - maxDiff: Maximum difficulty for this miner class (0 = use engine default)
//   - targetTime: Target seconds between shares for this miner (0 = use engine default)
func (e *Engine) NewSessionStateWithProfile(initialDiff, minDiff, maxDiff, targetTime float64) *SessionState {
	// Use profile values if provided, otherwise fall back to engine defaults
	if minDiff <= 0 || math.IsNaN(minDiff) || math.IsInf(minDiff, 0) {
		minDiff = e.cfg.MinDiff
	}
	if maxDiff <= 0 || math.IsNaN(maxDiff) || math.IsInf(maxDiff, 0) {
		maxDiff = e.cfg.MaxDiff
	}
	// SECURITY: Sanitize initial difficulty — NaN/Inf/zero would poison all future calculations
	if math.IsNaN(initialDiff) || math.IsInf(initialDiff, 0) || initialDiff <= 0 {
		initialDiff = minDiff
	}
	// targetTime of 0 means "use engine default" - handled in getTargetTime()

	state := &SessionState{
		minDiff:    minDiff,
		maxDiff:    maxDiff,
		targetTime: targetTime,
	}
	state.difficultyBits.Store(math.Float64bits(initialDiff))
	state.lastRetargetNano.Store(time.Now().UnixNano())
	return state
}

// getTargetTime returns the effective target time for a session.
// Uses per-session target if set, otherwise falls back to engine default.
func (e *Engine) getTargetTime(state *SessionState) float64 {
	if state.targetTime > 0 {
		return state.targetTime
	}
	return e.cfg.TargetTime
}

// RecordShare records a share and checks if retarget is needed.
// Returns (newDifficulty, changed) where changed is true if difficulty was adjusted.
// This method is lock-free and safe for concurrent calls.
func (e *Engine) RecordShare(state *SessionState) (float64, bool) {
	now := time.Now().UnixNano()

	// Update share tracking
	state.shareCount.Add(1)
	state.sharesSinceRetarget.Add(1)
	state.lastShareNano.Store(now)

	// Check if retarget interval has passed
	lastRetarget := state.lastRetargetNano.Load()
	retargetIntervalNano := int64(e.cfg.RetargetTime * float64(time.Second))

	if now-lastRetarget < retargetIntervalNano {
		// Not time to retarget yet
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	// Attempt to claim the retarget (only one goroutine should do this)
	if !state.lastRetargetNano.CompareAndSwap(lastRetarget, now) {
		// Another goroutine beat us to it
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	// Calculate new difficulty
	shares := state.sharesSinceRetarget.Swap(0)
	if shares == 0 {
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	elapsedSec := float64(now-lastRetarget) / float64(time.Second)

	// FIX T-2: Handle clock jump (NTP resync, VM time drift)
	// If clock jumped backward, elapsedSec is negative - skip this retarget
	// If clock jumped forward massively (>10min), also skip to prevent wild swings
	if elapsedSec <= 0 || elapsedSec > 600 {
		// Reset retarget timestamp but don't adjust difficulty
		state.sharesSinceRetarget.Store(shares) // Restore shares for next attempt
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	actualTime := elapsedSec / float64(shares)
	targetTime := e.getTargetTime(state) // Use per-session target if available

	// Calculate variance
	variance := (actualTime - targetTime) / targetTime * 100

	// Only adjust if variance exceeds threshold
	// STABILITY FIX: Use minimum 50% variance threshold to prevent oscillation
	// from cgminer work-in-progress delays. Config can set higher but not lower.
	varianceThreshold := e.cfg.VariancePercent
	if varianceThreshold < 50.0 {
		varianceThreshold = 50.0
	}
	if math.Abs(variance) <= varianceThreshold {
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	// Calculate adjustment factor
	// If shares are coming too fast (actualTime < targetTime), increase difficulty
	// If shares are coming too slow (actualTime > targetTime), decrease difficulty
	factor := targetTime / actualTime

	// Limit adjustment factor for controlled convergence
	// ASYMMETRIC LIMITS: Increases can be aggressive (4x), decreases must be gentle (0.75x)
	// This handles cgminer/Avalon work-in-progress delays:
	// - When diff increases, miner keeps working at old diff → few shares arrive
	// - Pool sees low share rate and wants to decrease diff
	// - But this is artificial - miner IS capable, just delayed
	// - Limiting decrease to 0.75x (25% reduction) prevents oscillation
	// - Allowing 4x increase is fine because shares at old diff are accepted via grace period
	if factor > 4.0 {
		factor = 4.0
	} else if factor < 0.75 {
		// OSCILLATION FIX: Limit decreases to 25% instead of 50%
		factor = 0.75
	}

	// Apply adjustment
	currentDiff := math.Float64frombits(state.difficultyBits.Load())
	newDiff := currentDiff * factor

	// Clamp to min/max
	if newDiff < state.minDiff {
		newDiff = state.minDiff
	} else if newDiff > state.maxDiff {
		newDiff = state.maxDiff
	}

	// Store new difficulty
	state.difficultyBits.Store(math.Float64bits(newDiff))

	return newDiff, true
}

// GetDifficulty returns the current difficulty for a session.
// Lock-free read.
func GetDifficulty(state *SessionState) float64 {
	return math.Float64frombits(state.difficultyBits.Load())
}

// SetDifficulty manually sets the difficulty for a session.
// Used for initial difficulty or admin overrides.
// SECURITY: NaN/Inf are rejected to prevent poisoning the difficulty state.
func SetDifficulty(state *SessionState, diff float64) {
	if math.IsNaN(diff) || math.IsInf(diff, 0) || diff <= 0 {
		diff = state.minDiff // Clamp to minimum (matches NewSessionState behavior)
	}
	if diff < state.minDiff {
		diff = state.minDiff
	} else if diff > state.maxDiff {
		diff = state.maxDiff
	}
	state.difficultyBits.Store(math.Float64bits(diff))
}

// LastShareNano returns the Unix nanosecond timestamp of the last share.
// H-4 fix: Exported for session cleanup to detect stale sessions.
func (s *SessionState) LastShareNano() int64 {
	return s.lastShareNano.Load()
}

// MaxDiff returns the maximum difficulty for this session.
// Used for debugging vardiff ceiling issues.
func (s *SessionState) MaxDiff() float64 {
	return s.maxDiff
}

// MinDiff returns the minimum difficulty for this session.
func (s *SessionState) MinDiff() float64 {
	return s.minDiff
}

// TargetTime returns the target time between shares for this session.
func (s *SessionState) TargetTime() float64 {
	return s.targetTime
}

// SharesSinceRetarget returns the number of shares since the last retarget.
// Used for aggressive retarget calculations - must use this instead of TotalShares
// to correctly calculate share rate during the retarget window.
func (s *SessionState) SharesSinceRetarget() uint64 {
	return s.sharesSinceRetarget.Load()
}

// LastRetargetNano returns the Unix nanosecond timestamp of the last retarget.
// Used for calculating elapsed time since last retarget.
func (s *SessionState) LastRetargetNano() int64 {
	return s.lastRetargetNano.Load()
}

// ConsecutiveNoChange returns the count of consecutive retarget attempts
// that resulted in no difficulty change. Used for exponential backoff
// to prevent rapid retarget attempts when difficulty is already optimal.
func (s *SessionState) ConsecutiveNoChange() uint64 {
	return s.consecutiveNoChange.Load()
}

// GetStats returns VARDIFF statistics for a session.
type Stats struct {
	CurrentDifficulty float64
	TotalShares       uint64
	LastShareTime     time.Time
	LastRetargetTime  time.Time
}

func (e *Engine) GetStats(state *SessionState) Stats {
	lastShare := state.lastShareNano.Load()
	lastRetarget := state.lastRetargetNano.Load()

	return Stats{
		CurrentDifficulty: math.Float64frombits(state.difficultyBits.Load()),
		TotalShares:       state.shareCount.Load(),
		LastShareTime:     time.Unix(0, lastShare),
		LastRetargetTime:  time.Unix(0, lastRetarget),
	}
}

// EstimateHashrate estimates hashrate based on difficulty and share rate.
// Returns hashrate in H/s.
// DEPRECATED: Use Engine.EstimateHashrate for accurate per-session estimates.
func EstimateHashrate(state *SessionState, windowSec float64) float64 {
	lastShare := state.lastShareNano.Load()

	if lastShare == 0 {
		return 0
	}

	// Simple estimate: difficulty * 2^32 / time_between_shares
	diff := math.Float64frombits(state.difficultyBits.Load())

	// Use per-session target time if available, otherwise default to 4 seconds
	targetTime := state.targetTime
	if targetTime <= 0 {
		targetTime = 4.0 // Default fallback
	}

	// hashrate = difficulty * constant / targetTime (constant depends on algorithm)
	return coin.CalculateHashrateForAlgorithm(diff, targetTime, "sha256d")
}

// EstimateSessionHashrate estimates hashrate using the session's configured target time.
// This provides more accurate estimates than the standalone EstimateHashrate function.
func (e *Engine) EstimateSessionHashrate(state *SessionState) float64 {
	lastShare := state.lastShareNano.Load()

	if lastShare == 0 {
		return 0
	}

	diff := math.Float64frombits(state.difficultyBits.Load())
	targetTime := e.getTargetTime(state)

	return coin.CalculateHashrateForAlgorithm(diff, targetTime, e.algorithm)
}

// AggressiveRetarget performs immediate retarget for sessions that are far from optimal.
// This is called during the ramp-up period to quickly find the right difficulty.
// Returns (newDifficulty, changed) where changed is true if difficulty was adjusted.
func (e *Engine) AggressiveRetarget(state *SessionState, shares uint64, elapsedSec float64) (float64, bool) {
	if shares < 2 {
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	// FIX T-2: Handle clock jump (NTP resync, VM time drift)
	// Skip retarget if elapsed time is invalid (negative or suspiciously large)
	if elapsedSec <= 0 || elapsedSec > 600 {
		return math.Float64frombits(state.difficultyBits.Load()), false
	}

	actualTime := elapsedSec / float64(shares)
	targetTime := e.getTargetTime(state) // Use per-session target if available

	// Calculate how far off we are from target
	ratio := actualTime / targetTime
	currentDiff := math.Float64frombits(state.difficultyBits.Load())

	// ASYMMETRIC THRESHOLDS for aggressive retarget trigger:
	// - Shares too FAST (ratio < 0.8): Trigger at 1.25x - ramp up quickly during convergence
	// - Shares too SLOW (ratio > 2.0): Trigger at 2x - conservative due to cgminer delays
	//
	// CRITICAL FIX: Previous threshold of 0.5 (2x faster) was too conservative.
	// A 5 TH/s miner at diff 1000 with targetTime=1s produces ~5 shares/sec,
	// giving ratio=0.2. But during ramp-up when diff oscillates between 500-1100,
	// the ratio hovers around 0.8-1.0 and never triggers aggressive increase.
	// By triggering at 0.8 (shares 25% faster than target), we catch the ramp-up case.
	if ratio >= 0.8 && ratio <= 2.0 {
		// Difficulty is within acceptable range - increment backoff counter
		// This enables exponential backoff in the pool handler's cooldown logic
		state.consecutiveNoChange.Add(1)
		return currentDiff, false
	}

	// Aggressive adjustment with ASYMMETRIC LIMITS
	// Increases can be aggressive (4x), decreases must be gentle (0.5x)
	// This prevents oscillation caused by cgminer/Avalon work-in-progress delays:
	// - After diff increase, miner submits few shares (still working at old diff)
	// - Pool interprets this as "miner is slow" and wants to drop diff
	// - But miner is actually capable - it's just a timing artifact
	// - Limiting decrease to 0.5x prevents wild swings like 1800 → 450 → 100
	factor := targetTime / actualTime
	if factor > 4.0 {
		factor = 4.0
	} else if factor < 0.75 {
		// OSCILLATION FIX: Limit decreases to 25% (0.75x) instead of 50% (0.5x)
		// This prevents wild swings when shares temporarily slow down after a diff increase.
		// With 0.5x, diff would swing: 2246 → 1123 → 2000 → 1000 (oscillating)
		// With 0.75x, diff converges: 2246 → 1684 → 1500 → stable
		factor = 0.75
	}

	newDiff := currentDiff * factor

	// Clamp to configured limits
	if newDiff < state.minDiff {
		newDiff = state.minDiff
	} else if newDiff > state.maxDiff {
		newDiff = state.maxDiff
	}

	// Only update if meaningfully different (>5% change)
	if math.Abs(newDiff-currentDiff)/currentDiff > 0.05 {
		state.difficultyBits.Store(math.Float64bits(newDiff))
		state.lastRetargetNano.Store(time.Now().UnixNano())
		// Reset sharesSinceRetarget to ensure clean state for next retarget window.
		// This prevents immediate re-evaluation with stale share counts and ensures
		// retargetTime intervals are properly respected.
		state.sharesSinceRetarget.Store(0)
		// Reset backoff counter on successful change
		state.consecutiveNoChange.Store(0)
		return newDiff, true
	}

	// Change was too small (<5%) - increment backoff counter
	state.consecutiveNoChange.Add(1)
	return currentDiff, false
}

// ShouldAggressiveRetarget checks if the session needs aggressive retargeting.
// Returns true if shares are coming in way too fast or too slow relative to target.
func (e *Engine) ShouldAggressiveRetarget(state *SessionState) bool {
	lastRetarget := state.lastRetargetNano.Load()
	if lastRetarget == 0 {
		return false
	}

	elapsedSec := float64(time.Now().UnixNano()-lastRetarget) / float64(time.Second)
	shares := state.sharesSinceRetarget.Load()

	// FIX T-2: Skip if clock jumped (negative elapsed or suspiciously large)
	if shares < 2 || elapsedSec < 0.5 || elapsedSec > 600 {
		return false
	}

	actualTime := elapsedSec / float64(shares)
	targetTime := e.getTargetTime(state) // Use per-session target if available
	ratio := actualTime / targetTime

	// ASYMMETRIC THRESHOLDS: Be aggressive on increases, conservative on decreases
	// - Shares too FAST (ratio < 0.8): Trigger at 1.25x faster - ramp up during convergence
	// - Shares too SLOW (ratio > 2.0): Trigger at 2x slower - conservative due to cgminer delays
	// CRITICAL FIX: Must match thresholds in AggressiveRetarget (0.8 and 2.0)
	return ratio < 0.8 || ratio > 2.0
}

// GetTargetTime returns the effective target time for a session (exported for testing/logging).
func (e *Engine) GetTargetTime(state *SessionState) float64 {
	return e.getTargetTime(state)
}
