// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package assertions provides runtime invariant checking for economic integrity.
//
// This package implements fail-fast assertions for critical mining pool invariants.
// When enabled, violations trigger immediate termination with detailed diagnostics.
//
// Usage:
//
//	assertions.Enable()  // Enable assertion checking (call once at startup)
//	assertions.Assert(condition, "invariant description")
//	assertions.AssertNotNil(ptr, "value name")
//	assertions.AssertPositive(amount, "reward amount")
//
// In production, assertions can be disabled for performance, but this is NOT recommended
// for economic-critical code paths. Silent failures are worse than loud failures.
package assertions

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// enabled controls whether assertions are active.
// Default is disabled; call Enable() to activate.
var enabled atomic.Bool

// logger for assertion violations (optional)
var logger atomic.Pointer[zap.SugaredLogger]

// stats tracks assertion metrics
var (
	totalChecks    atomic.Uint64
	totalViolations atomic.Uint64
)

// Mode defines how assertion violations are handled.
type Mode int

const (
	// ModeDisabled - assertions are not checked (not recommended for economic code)
	ModeDisabled Mode = iota
	// ModeLog - violations are logged but execution continues (dangerous)
	ModeLog
	// ModePanic - violations trigger panic (can be recovered)
	ModePanic
	// ModeFatal - violations terminate the process immediately (recommended)
	ModeFatal
)

var mode atomic.Int32

// Enable activates assertion checking in fatal mode (recommended).
func Enable() {
	enabled.Store(true)
	mode.Store(int32(ModeFatal))
}

// EnableWithMode activates assertion checking with a specific mode.
func EnableWithMode(m Mode) {
	enabled.Store(true)
	mode.Store(int32(m))
}

// Disable deactivates assertion checking.
// WARNING: This should NOT be used in production for economic code paths.
func Disable() {
	enabled.Store(false)
}

// IsEnabled returns true if assertions are active.
func IsEnabled() bool {
	return enabled.Load()
}

// SetLogger sets a logger for assertion violations.
func SetLogger(l *zap.SugaredLogger) {
	logger.Store(l)
}

// Stats returns assertion statistics.
func Stats() (checks, violations uint64) {
	return totalChecks.Load(), totalViolations.Load()
}

// Assert checks that a condition is true.
// If the condition is false and assertions are enabled, this triggers a violation.
//
// Example:
//
//	assertions.Assert(share.Difficulty > 0, "share difficulty must be positive")
func Assert(condition bool, msg string, args ...interface{}) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if !condition {
		handleViolation(fmt.Sprintf(msg, args...))
	}
}

// AssertNotNil checks that a pointer is not nil.
//
// Example:
//
//	assertions.AssertNotNil(job, "job")
func AssertNotNil(ptr interface{}, name string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if ptr == nil {
		handleViolation(fmt.Sprintf("%s must not be nil", name))
	}
}

// AssertNotEmpty checks that a string is not empty.
//
// Example:
//
//	assertions.AssertNotEmpty(minerAddress, "miner address")
func AssertNotEmpty(s string, name string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if s == "" {
		handleViolation(fmt.Sprintf("%s must not be empty", name))
	}
}

// AssertPositive checks that a number is positive (> 0).
//
// Example:
//
//	assertions.AssertPositive(blockReward, "block reward")
func AssertPositive[T int | int64 | uint64 | float64](n T, name string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if n <= 0 {
		handleViolation(fmt.Sprintf("%s must be positive, got %v", name, n))
	}
}

// AssertNonNegative checks that a number is non-negative (>= 0).
//
// Example:
//
//	assertions.AssertNonNegative(confirmations, "confirmations")
func AssertNonNegative[T int | int64 | uint64 | float64](n T, name string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if n < 0 {
		handleViolation(fmt.Sprintf("%s must be non-negative, got %v", name, n))
	}
}

// AssertEqual checks that two values are equal.
//
// Example:
//
//	assertions.AssertEqual(expectedHash, actualHash, "block hash")
func AssertEqual[T comparable](expected, actual T, name string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if expected != actual {
		handleViolation(fmt.Sprintf("%s mismatch: expected %v, got %v", name, expected, actual))
	}
}

// AssertLessOrEqual checks that actual <= limit.
//
// Example:
//
//	assertions.AssertLessOrEqual(totalPaid, totalRewards, "paid vs rewards")
func AssertLessOrEqual[T int | int64 | uint64 | float64](actual, limit T, name string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if actual > limit {
		handleViolation(fmt.Sprintf("%s exceeds limit: %v > %v", name, actual, limit))
	}
}

// AssertJobValid checks job invariants for share validation.
//
// Example:
//
//	assertions.AssertJobValid(job)
func AssertJobValid(job interface {
	IsValid() bool
}, jobID string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if job == nil {
		handleViolation(fmt.Sprintf("job %s is nil", jobID))
		return
	}
	if !job.IsValid() {
		handleViolation(fmt.Sprintf("job %s is in invalid state", jobID))
	}
}

// handleViolation processes an assertion violation according to the current mode.
func handleViolation(msg string) {
	totalViolations.Add(1)

	// Get stack trace
	pc := make([]uintptr, 10)
	n := runtime.Callers(3, pc) // Skip handleViolation, Assert*, and the caller
	frames := runtime.CallersFrames(pc[:n])

	var stackTrace string
	for {
		frame, more := frames.Next()
		stackTrace += fmt.Sprintf("\n  %s:%d %s", frame.File, frame.Line, frame.Function)
		if !more {
			break
		}
	}

	fullMsg := fmt.Sprintf("INVARIANT VIOLATION: %s\nStack trace:%s\nTime: %s",
		msg, stackTrace, time.Now().Format(time.RFC3339Nano))

	// Log if logger is available
	if l := logger.Load(); l != nil {
		l.Errorw("INVARIANT VIOLATION",
			"message", msg,
			"stack", stackTrace,
		)
	}

	// Handle based on mode
	currentMode := Mode(mode.Load())
	switch currentMode {
	case ModeDisabled:
		// Should not reach here, but just in case
		return
	case ModeLog:
		fmt.Fprintln(os.Stderr, fullMsg)
	case ModePanic:
		panic(fullMsg)
	case ModeFatal:
		fmt.Fprintln(os.Stderr, fullMsg)
		os.Exit(1)
	}
}

// =============================================================================
// ECONOMIC INVARIANT ASSERTIONS
// =============================================================================
// These are specialized assertions for mining pool economic integrity.

// AssertNoDoublePayout checks that a block hash has not been paid before.
// This should be called before executing any payout.
func AssertNoDoublePayout(blockHash string, isPaid func(hash string) bool) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if isPaid(blockHash) {
		handleViolation(fmt.Sprintf("double payout attempt for block %s", blockHash))
	}
}

// AssertOnChainConfirmation checks that a block is confirmed on-chain.
// This should be called before crediting any rewards.
func AssertOnChainConfirmation(blockHeight uint64, expectedHash string, getChainHash func(height uint64) (string, error)) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	chainHash, err := getChainHash(blockHeight)
	if err != nil {
		handleViolation(fmt.Sprintf("failed to verify block %d on chain: %v", blockHeight, err))
		return
	}
	if chainHash != expectedHash {
		handleViolation(fmt.Sprintf("block %d hash mismatch: chain=%s, expected=%s (possible reorg)", blockHeight, chainHash, expectedHash))
	}
}

// AssertMaturityReached checks that a block has reached the required confirmations.
func AssertMaturityReached(confirmations, required uint64, blockHeight uint64) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if confirmations < required {
		handleViolation(fmt.Sprintf("block %d not mature: %d/%d confirmations", blockHeight, confirmations, required))
	}
}

// AssertWalletBinding checks that a share's wallet matches the coinbase output.
func AssertWalletBinding(shareWallet, coinbaseWallet string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if shareWallet != coinbaseWallet {
		handleViolation(fmt.Sprintf("wallet binding mismatch: share=%s, coinbase=%s", shareWallet, coinbaseWallet))
	}
}

// AssertIdempotentOperation checks that an operation produces consistent results.
// Call with result of first execution, then verify subsequent executions match.
func AssertIdempotentOperation[T comparable](expected, actual T, operation string) {
	if !enabled.Load() {
		return
	}
	totalChecks.Add(1)

	if expected != actual {
		handleViolation(fmt.Sprintf("non-idempotent operation %s: first=%v, retry=%v", operation, expected, actual))
	}
}
