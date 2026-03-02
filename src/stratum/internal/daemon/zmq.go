//go:build !nozmq

// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package daemon provides ZMQ block notification handling with automatic fallback.
//
// ZMQ integration allows for instant block notifications from the daemon,
// providing sub-second latency for job updates compared to polling. This uses
// the ZMQ PUB/SUB pattern as documented in Bitcoin Core for block notifications.
//
// ZMQ Library: github.com/go-zeromq/zmq4 (pure Go implementation)
// ZMQ4 License: BSD-3-Clause
// See: https://github.com/go-zeromq/zmq4/blob/main/LICENSE
//
// ZeroMQ Protocol: https://zeromq.org/
// Original libzmq Authors: iMatix Corporation and Contributors
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-zeromq/zmq4"
	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// Default ZMQ timing constants (can be overridden via config)
//
// KEY UNDERSTANDING:
// - ZMQ timing reflects socket/node health, NOT blockchain consensus timing
// - ZMQ tells you: "This node observed an event and emitted it over this socket"
// - Different chains change event volume and load, not ZMQ semantics
// - These defaults have the same MEANING across all coins; observed values
//   may differ due to node load and event frequency
const (
	// DefaultZMQReconnectInitial is the initial retry delay after disconnect
	DefaultZMQReconnectInitial = 1 * time.Second

	// DefaultZMQReconnectMax caps exponential backoff (1s → 2s → 4s → 8s → 16s → 30s)
	DefaultZMQReconnectMax = 30 * time.Second

	// DefaultZMQReconnectFactor is the backoff multiplier
	DefaultZMQReconnectFactor = 2.0

	// DefaultZMQFailureThreshold is how long SOCKET ERRORS must persist before
	// switching to RPC polling. This triggers on connection failures, NOT on
	// "no block messages received" (silence between blocks is normal).
	DefaultZMQFailureThreshold = 30 * time.Second

	// DefaultZMQHealthCheckInterval is how often we check socket connection health
	DefaultZMQHealthCheckInterval = 10 * time.Second

	// DefaultZMQStabilityPeriod is how long ZMQ must be error-free before
	// we trust it and disable the polling fallback
	DefaultZMQStabilityPeriod = 2 * time.Minute
)

// ZMQStatus represents the current state of ZMQ
type ZMQStatus int

const (
	ZMQStatusDisabled ZMQStatus = iota
	ZMQStatusConnecting
	ZMQStatusHealthy
	ZMQStatusDegraded
	ZMQStatusFailed
)

func (s ZMQStatus) String() string {
	switch s {
	case ZMQStatusDisabled:
		return "disabled"
	case ZMQStatusConnecting:
		return "connecting"
	case ZMQStatusHealthy:
		return "healthy"
	case ZMQStatusDegraded:
		return "degraded"
	case ZMQStatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// ZMQListener listens for block notifications via ZMQ with automatic fallback to RPC polling.
// Uses ZeroMQ's SUB socket to receive instant notifications when
// DigiByte Core mines or receives a new block.
type ZMQListener struct {
	cfg    *config.ZMQConfig
	logger *zap.SugaredLogger

	// ZMQ socket
	subscriber zmq4.Socket
	socketMu   sync.Mutex
	socketCtx  context.Context
	socketStop context.CancelFunc

	// Callbacks
	onBlock        func(blockHash []byte)
	onFallback     func(usePoll bool) // Called when switching between ZMQ and polling
	onStatusChange func(status ZMQStatus)

	// Goroutine tracking for clean shutdown
	wg sync.WaitGroup

	// Health tracking
	status           atomic.Int32 // ZMQStatus
	lastMessageTime  atomic.Int64 // Unix timestamp of last successful message
	failureStartTime atomic.Int64 // Unix timestamp when failures started
	healthyStartTime atomic.Int64 // Unix timestamp when healthy period started
	stabilityReached atomic.Bool  // true once ZMQ has proven stable
	messagesReceived atomic.Uint64
	errorsCount      atomic.Uint64

	// V36 FIX: Recent block hash ring buffer for ZMQ deduplication.
	// ZMQ PUB/SUB can deliver duplicates on reconnection replay or
	// out-of-order messages. We track the last N block hashes and
	// skip notifications for hashes already processed.
	recentBlocksMu    sync.Mutex
	recentBlockHashes [8]string // Ring buffer of last 8 block hashes (hex)
	recentBlockIdx    int       // Current write position in ring buffer

	// State
	running            atomic.Bool
	recoveryLoopActive atomic.Bool // BUG FIX: Prevents multiple recovery loops from spawning
	reconnecting       atomic.Bool // BUG FIX: Suppresses recordFailure during reconnection
	stopCh             chan struct{}
	mu                 sync.Mutex
}

// NewZMQListener creates a new ZMQ block notification listener with fallback support.
func NewZMQListener(cfg *config.ZMQConfig, logger *zap.Logger) *ZMQListener {
	z := &ZMQListener{
		cfg:    cfg,
		logger: logger.Sugar(),
		stopCh: make(chan struct{}),
	}
	z.status.Store(int32(ZMQStatusDisabled))
	return z
}

// Config helper methods with defaults

func (z *ZMQListener) reconnectInitial() time.Duration {
	if z.cfg.ReconnectInitial > 0 {
		return z.cfg.ReconnectInitial
	}
	return DefaultZMQReconnectInitial
}

func (z *ZMQListener) reconnectMax() time.Duration {
	if z.cfg.ReconnectMax > 0 {
		return z.cfg.ReconnectMax
	}
	return DefaultZMQReconnectMax
}

func (z *ZMQListener) reconnectFactor() float64 {
	if z.cfg.ReconnectFactor > 0 {
		return z.cfg.ReconnectFactor
	}
	return DefaultZMQReconnectFactor
}

func (z *ZMQListener) failureThreshold() time.Duration {
	if z.cfg.FailureThreshold > 0 {
		return z.cfg.FailureThreshold
	}
	return DefaultZMQFailureThreshold
}

func (z *ZMQListener) stabilityPeriod() time.Duration {
	if z.cfg.StabilityPeriod > 0 {
		return z.cfg.StabilityPeriod
	}
	return DefaultZMQStabilityPeriod
}

func (z *ZMQListener) healthCheckInterval() time.Duration {
	if z.cfg.HealthCheckInterval > 0 {
		return z.cfg.HealthCheckInterval
	}
	return DefaultZMQHealthCheckInterval
}

// SetBlockHandler sets the callback for block notifications.
func (z *ZMQListener) SetBlockHandler(handler func(blockHash []byte)) {
	z.onBlock = handler
}

// SetFallbackHandler sets the callback for when switching between ZMQ and polling.
// The callback receives true when falling back to polling, false when ZMQ recovers.
func (z *ZMQListener) SetFallbackHandler(handler func(usePoll bool)) {
	z.onFallback = handler
}

// SetStatusChangeHandler sets the callback for status changes.
func (z *ZMQListener) SetStatusChangeHandler(handler func(status ZMQStatus)) {
	z.onStatusChange = handler
}

// isDuplicateBlock checks if a block hash was recently seen via ZMQ.
// V36 FIX: Prevents duplicate processing from ZMQ reconnection replay.
// Returns true if the hash was already in the ring buffer.
// If not a duplicate, adds it to the buffer.
func (z *ZMQListener) isDuplicateBlock(hashHex string) bool {
	z.recentBlocksMu.Lock()
	defer z.recentBlocksMu.Unlock()

	// Check if hash is already in ring buffer
	for _, h := range z.recentBlockHashes {
		if h != "" && h == hashHex {
			return true
		}
	}

	// Not a duplicate — add to ring buffer
	z.recentBlockHashes[z.recentBlockIdx] = hashHex
	z.recentBlockIdx = (z.recentBlockIdx + 1) % len(z.recentBlockHashes)
	return false
}

// Start begins listening for ZMQ notifications.
func (z *ZMQListener) Start(ctx context.Context) error {
	z.mu.Lock()
	defer z.mu.Unlock()

	if z.running.Load() {
		return nil
	}

	z.setStatus(ZMQStatusConnecting)

	if err := z.connect(); err != nil {
		z.setStatus(ZMQStatusFailed)
		return err
	}

	z.running.Store(true)
	z.stopCh = make(chan struct{})
	z.lastMessageTime.Store(time.Now().Unix())
	z.failureStartTime.Store(0)

	z.logger.Infow("ZMQ listener started",
		"endpoint", z.cfg.Endpoint,
		"subscription", "hashblock",
	)

	// Start receive loop (tracked by WaitGroup for clean shutdown)
	z.wg.Add(1)
	go func() {
		defer z.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				z.logger.Errorw("PANIC recovered in receiveLoop goroutine", "panic", r)
			}
		}()
		z.receiveLoop(ctx)
	}()

	// Start health monitor (tracked by WaitGroup for clean shutdown)
	z.wg.Add(1)
	go func() {
		defer z.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				z.logger.Errorw("PANIC recovered in healthMonitor goroutine", "panic", r)
			}
		}()
		z.healthMonitor(ctx)
	}()

	return nil
}

// connect establishes the ZMQ connection
func (z *ZMQListener) connect() error {
	z.socketMu.Lock()
	defer z.socketMu.Unlock()

	// Close existing socket and cancel its context if any
	if z.subscriber != nil {
		_ = z.subscriber.Close() // #nosec G104
		z.subscriber = nil
	}
	if z.socketStop != nil {
		z.socketStop()
	}

	// Create a new context for this socket with timeout
	z.socketCtx, z.socketStop = context.WithCancel(context.Background())

	// Create ZMQ subscriber socket with timeout option
	subscriber := zmq4.NewSub(z.socketCtx, zmq4.WithTimeout(1*time.Second))

	// Connect to the daemon's ZMQ endpoint
	if err := subscriber.Dial(z.cfg.Endpoint); err != nil {
		_ = subscriber.Close() // #nosec G104
		z.socketStop()
		return fmt.Errorf("failed to connect to ZMQ endpoint %s: %w", z.cfg.Endpoint, err)
	}

	// Subscribe to hashblock notifications
	if err := subscriber.SetOption(zmq4.OptionSubscribe, "hashblock"); err != nil {
		_ = subscriber.Close() // #nosec G104
		z.socketStop()
		return fmt.Errorf("failed to subscribe to hashblock: %w", err)
	}

	z.subscriber = subscriber
	return nil
}

// Stop stops the ZMQ listener.
// It signals goroutines to stop, waits for them to exit cleanly, then closes the socket.
// This prevents the libzmq assertion failure that occurs when closing a socket
// while a receive operation is in progress.
func (z *ZMQListener) Stop() error {
	z.mu.Lock()

	if !z.running.Load() {
		z.mu.Unlock()
		return nil
	}

	// Signal all goroutines to stop
	z.running.Store(false)
	close(z.stopCh)

	// Release the lock before waiting, to avoid deadlock with goroutines
	// that might need to acquire socketMu (which we hold during z.mu lock)
	z.mu.Unlock()

	// Wait for receive loop and health monitor to exit cleanly
	// The receive loop has a 1-second timeout, so this should complete quickly
	// Use a timeout to prevent hanging if something goes wrong
	done := make(chan struct{})
	go func() {
		z.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Goroutines exited cleanly
	case <-time.After(3 * time.Second):
		// Timeout - goroutines didn't exit in time, proceed anyway
		z.logger.Warn("ZMQ goroutines did not exit within timeout, proceeding with socket close")
	}

	// Now it's safe to close the socket - no goroutines are using it
	z.socketMu.Lock()
	if z.subscriber != nil {
		_ = z.subscriber.Close() // #nosec G104
		z.subscriber = nil
	}
	if z.socketStop != nil {
		z.socketStop()
		z.socketStop = nil
	}
	z.socketMu.Unlock()

	z.setStatus(ZMQStatusDisabled)
	z.logger.Info("ZMQ listener stopped")
	return nil
}

// receiveLoop receives messages from the ZMQ socket.
// Uses non-blocking receives with timeout to avoid the libzmq signaler assertion
// failure that can crash the process when the daemon disconnects unexpectedly.
func (z *ZMQListener) receiveLoop(ctx context.Context) {
	consecutiveErrors := 0
	const maxConsecutiveErrors = 10

	for z.running.Load() {
		select {
		case <-ctx.Done():
			return
		case <-z.stopCh:
			return
		default:
		}

		z.socketMu.Lock()
		if z.subscriber == nil {
			z.socketMu.Unlock()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		subscriber := z.subscriber
		z.socketMu.Unlock()

		// Receive message - uses the timeout set in connect() (1 second)
		// The timeout allows us to check for context cancellation regularly
		msg, err := subscriber.Recv()

		// Check if we should stop immediately after receive returns
		// This ensures we exit quickly during shutdown without processing stale data
		if !z.running.Load() {
			return
		}

		if err != nil {
			// Check if it's a context timeout/cancellation (normal during shutdown)
			if err == context.DeadlineExceeded || err == context.Canceled {
				// Timeout - this is normal, reset error count
				consecutiveErrors = 0
				continue
			}

			consecutiveErrors++
			z.errorsCount.Add(1)
			z.recordFailure()

			z.logger.Warnw("ZMQ receive error",
				"error", err,
				"consecutiveErrors", consecutiveErrors,
			)

			// If too many consecutive errors, try to reconnect the socket
			// This helps recover from daemon restarts or network issues
			if consecutiveErrors >= maxConsecutiveErrors {
				z.logger.Warnw("Too many ZMQ errors, reconnecting socket",
					"consecutiveErrors", consecutiveErrors,
				)

				// Close and reconnect
				z.socketMu.Lock()
				if z.subscriber != nil {
					_ = z.subscriber.Close() // #nosec G104
					z.subscriber = nil
				}
				if z.socketStop != nil {
					z.socketStop()
				}
				z.socketMu.Unlock()

				// Brief pause before reconnect
				time.Sleep(time.Second)

				if err := z.connect(); err != nil {
					z.logger.Errorw("Failed to reconnect ZMQ", "error", err)
					time.Sleep(5 * time.Second) // Longer pause on reconnect failure
				} else {
					z.logger.Info("ZMQ socket reconnected successfully")
				}
				consecutiveErrors = 0
				continue
			}

			// Brief pause before retry
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Success! Reset error tracking
		consecutiveErrors = 0
		z.recordSuccess()

		// ZMQ message format: [topic, body, sequence]
		// msg.Frames is [][]byte containing the message frames
		if len(msg.Frames) < 2 {
			continue
		}

		topic := string(msg.Frames[0])
		body := msg.Frames[1]

		switch topic {
		case "hashblock":
			z.messagesReceived.Add(1)
			// V36 FIX: Deduplicate ZMQ block notifications.
			// ZMQ PUB/SUB can deliver duplicates on reconnection replay.
			hashHex := hex.EncodeToString(body)
			if z.isDuplicateBlock(hashHex) {
				z.logger.Debugw("V36: Duplicate ZMQ hashblock notification — skipping",
					"hash", hashHex,
				)
				continue
			}
			z.logger.Infow("Received block notification via ZMQ",
				"hashLen", len(body),
			)
			if z.onBlock != nil {
				z.onBlock(body)
			}

		case "rawblock":
			z.messagesReceived.Add(1)
			// Extract block hash from header (first 80 bytes) for deduplication
			if len(body) >= 80 {
				first := sha256.Sum256(body[:80])
				headerHash := sha256.Sum256(first[:])
				hexHash := hex.EncodeToString(headerHash[:])
				if z.isDuplicateBlock(hexHash) {
					z.logger.Debugw("Duplicate rawblock notification, skipping",
						"hash", hexHash,
					)
					continue
				}
				z.logger.Infow("Received raw block notification via ZMQ",
					"blockSize", len(body),
					"hash", hexHash,
				)
			} else {
				z.logger.Warnw("Received undersized rawblock notification",
					"bodyLen", len(body),
				)
			}
			if z.onBlock != nil {
				z.onBlock(body)
			}

		default:
			z.logger.Debugw("Received unknown ZMQ topic", "topic", topic)
		}
	}
}

// healthMonitor monitors ZMQ health and triggers fallback if needed
func (z *ZMQListener) healthMonitor(ctx context.Context) {
	ticker := time.NewTicker(z.healthCheckInterval())
	defer ticker.Stop()

	for z.running.Load() {
		select {
		case <-ctx.Done():
			return
		case <-z.stopCh:
			return
		case <-ticker.C:
			z.checkHealth()
		}
	}
}

// recordSuccess records a successful ZMQ operation
// SECURITY: Uses atomic operations consistently to prevent race conditions in state transitions
func (z *ZMQListener) recordSuccess() {
	now := time.Now().Unix()
	z.lastMessageTime.Store(now)

	// Clear failure tracking atomically - use Swap to get and clear in one operation
	// This prevents TOCTOU race where failure could be set between Load and Store
	failureStart := z.failureStartTime.Swap(0)
	if failureStart > 0 {
		z.healthyStartTime.Store(now) // Reset healthy start time
		z.logger.Info("ZMQ connection recovered")

		// If we were in failed state, notify recovery
		// Use Swap to atomically check and clear the failed state
		oldStatus := ZMQStatus(z.status.Load())
		if oldStatus == ZMQStatusFailed {
			if z.onFallback != nil {
				z.onFallback(false) // false = use ZMQ, not polling
			}
		}
	}

	// Start healthy period if not already started
	if z.healthyStartTime.CompareAndSwap(0, now) {
		z.logger.Info("ZMQ healthy period started, monitoring for stability")
	}

	z.setStatus(ZMQStatusHealthy)
}

// recordFailure records a ZMQ failure
func (z *ZMQListener) recordFailure() {
	// BUG FIX: Skip failure recording during reconnection.
	// When recoveryLoop closes the old socket and creates a new one,
	// receiveLoop will get errors on the old socket. These errors are
	// expected and should not reset failureStartTime.
	if z.reconnecting.Load() {
		return
	}

	now := time.Now().Unix()

	// Reset healthy period tracking on any failure
	z.healthyStartTime.Store(0)

	// Start failure timer if not already started
	if z.failureStartTime.CompareAndSwap(0, now) {
		z.logger.Warn("ZMQ failures started, monitoring for fallback threshold")
		z.setStatus(ZMQStatusDegraded)
	}
}

// checkHealth evaluates ZMQ health, triggers fallback on failure, and
// detects when ZMQ has proven stable enough to disable polling.
// SECURITY: Uses atomic CAS operations to ensure only one goroutine triggers state transitions
func (z *ZMQListener) checkHealth() {
	stabilityThreshold := z.stabilityPeriod()
	failureThreshold := z.failureThreshold()

	// Check for stability - if healthy for long enough, disable polling
	// Use CAS to ensure only one goroutine wins the stability transition
	if !z.stabilityReached.Load() {
		healthyStart := z.healthyStartTime.Load()
		if healthyStart > 0 {
			healthyDuration := time.Since(time.Unix(healthyStart, 0))
			if healthyDuration >= stabilityThreshold {
				// CAS ensures only one caller wins and triggers the callback
				if z.stabilityReached.CompareAndSwap(false, true) {
					z.logger.Infow("ZMQ stability confirmed, disabling RPC polling fallback",
						"healthyDuration", healthyDuration.Round(time.Second),
						"threshold", stabilityThreshold,
					)
					// Notify to disable polling - ZMQ is now primary
					if z.onFallback != nil {
						z.onFallback(false) // false = use ZMQ, not polling
					}
				}
			}
		}
	}

	// Check for failures - use atomic load once and work with the value
	failureStart := z.failureStartTime.Load()
	if failureStart == 0 {
		// No active failures
		return
	}

	failureDuration := time.Since(time.Unix(failureStart, 0))

	if failureDuration >= failureThreshold {
		// BUG FIX: Check if recovery loop is already running to prevent spawning multiple.
		// The CAS below only prevents concurrent spawns at the same instant, but not
		// across 10-second health check intervals when status changes to "connecting".
		if z.recoveryLoopActive.Load() {
			return // Recovery loop already running, don't spawn another
		}

		// Use CAS on status to ensure only one goroutine triggers the fallback.
		// Without CAS, concurrent checkHealth() calls can each Load() a non-Failed
		// status, pass the check, and each spawn a separate recovery loop.
		currentStatus := ZMQStatus(z.status.Load())
		if currentStatus != ZMQStatusFailed &&
			z.status.CompareAndSwap(int32(currentStatus), int32(ZMQStatusFailed)) {
			z.logger.Errorw("ZMQ failed for extended period, switching to RPC polling",
				"failureDuration", failureDuration.Round(time.Second),
				"threshold", failureThreshold,
			)

			z.stabilityReached.Store(false) // Reset stability flag

			// Notify fallback handler
			if z.onFallback != nil {
				z.onFallback(true) // true = use polling
			}

			// Start recovery attempts in background
			// CRITICAL FIX: Track this goroutine in WaitGroup to prevent leak on shutdown
			z.recoveryLoopActive.Store(true) // Mark loop as active
			z.wg.Add(1)
			go z.recoveryLoop()
		}
	}
}

// recoveryLoop attempts to recover ZMQ after failure using exponential backoff.
// Backoff sequence: 5s → 10s → 20s → 40s → 60s → 120s (max)
// This goroutine exits when:
// 1. The listener is stopped (z.running becomes false or z.stopCh is closed)
// 2. ZMQ successfully recovers (status is no longer Failed)
func (z *ZMQListener) recoveryLoop() {
	defer z.wg.Done()                    // CRITICAL FIX: Signal completion on exit
	defer z.recoveryLoopActive.Store(false) // BUG FIX: Clear flag so new loop can spawn if needed
	defer func() {
		if r := recover(); r != nil {
			z.logger.Errorw("PANIC recovered in recoveryLoop goroutine", "panic", r)
		}
	}()

	// Get backoff configuration
	initialDelay := z.reconnectInitial()
	maxDelay := z.reconnectMax()
	factor := z.reconnectFactor()
	currentDelay := initialDelay

	z.logger.Infow("ZMQ recovery loop started with exponential backoff",
		"initialDelay", initialDelay,
		"maxDelay", maxDelay,
		"factor", factor,
	)
	defer z.logger.Info("ZMQ recovery loop exited")

	for {
		// Check if we should exit BEFORE waiting
		if !z.running.Load() {
			return
		}

		// Check if already recovered (status changed by receiveLoop success)
		currentStatus := ZMQStatus(z.status.Load())
		if currentStatus != ZMQStatusFailed && currentStatus != ZMQStatusConnecting {
			z.logger.Infow("ZMQ recovery loop: status changed, exiting",
				"currentStatus", currentStatus.String(),
			)
			return
		}

		z.logger.Infow("Waiting before ZMQ recovery attempt", "delay", currentDelay)

		// Wait with backoff delay
		select {
		case <-z.stopCh:
			return
		case <-time.After(currentDelay):
			// Re-check status after wait
			if ZMQStatus(z.status.Load()) != ZMQStatusFailed {
				z.logger.Info("ZMQ recovered, exiting recovery loop")
				return
			}

			z.logger.Infow("Attempting ZMQ recovery...", "attempt_delay", currentDelay)

			// BUG FIX: Set reconnecting flag to suppress recordFailure() calls
			// during reconnection. The old socket close will cause errors in
			// receiveLoop, which would otherwise reset failureStartTime.
			z.reconnecting.Store(true)

			// Try to reconnect
			if err := z.connect(); err != nil {
				z.reconnecting.Store(false)
				z.logger.Warnw("ZMQ recovery failed", "error", err, "next_delay", z.nextDelay(currentDelay, maxDelay, factor))
				// Increase delay for next attempt (exponential backoff)
				currentDelay = z.nextDelay(currentDelay, maxDelay, factor)
				continue
			}

			// BUG FIX: Reset failure tracking on successful reconnect.
			// This gives the new connection a clean slate. Without this reset,
			// the stability check below always fails in regtest (no blocks = no messages).
			z.failureStartTime.Store(0)
			z.healthyStartTime.Store(time.Now().Unix())

			// Test the connection by waiting for a message or timeout
			z.logger.Info("ZMQ reconnected, testing stability...")
			z.setStatus(ZMQStatusConnecting)

			// BUG FIX: Clear reconnecting flag now that new socket is established.
			// This allows receiveLoop to resume normal error tracking.
			z.reconnecting.Store(false)

			// Give it some time to stabilize, but check periodically
			for i := 0; i < 10; i++ {
				time.Sleep(1 * time.Second)
				if !z.running.Load() {
					return
				}
				// If failures cleared, we're recovered
				if z.failureStartTime.Load() == 0 {
					z.logger.Info("ZMQ recovered successfully")
					return
				}
			}

			// Check if we received any messages after stability period
			if z.failureStartTime.Load() == 0 {
				z.logger.Info("ZMQ recovered successfully")
				return
			}

			// Still failing, set back to failed and continue retry loop
			z.setStatus(ZMQStatusFailed)
			// Increase delay for next attempt (exponential backoff)
			currentDelay = z.nextDelay(currentDelay, maxDelay, factor)
		}
	}
}

// nextDelay calculates the next backoff delay
func (z *ZMQListener) nextDelay(current, max time.Duration, factor float64) time.Duration {
	next := time.Duration(float64(current) * factor)
	if next > max {
		return max
	}
	return next
}

// setStatus updates the status and notifies handler
func (z *ZMQListener) setStatus(status ZMQStatus) {
	old := ZMQStatus(z.status.Swap(int32(status)))
	if old != status {
		z.logger.Infow("ZMQ status changed",
			"from", old.String(),
			"to", status.String(),
		)
		if z.onStatusChange != nil {
			z.onStatusChange(status)
		}
	}
}

// Status returns the current ZMQ status
func (z *ZMQListener) Status() ZMQStatus {
	return ZMQStatus(z.status.Load())
}

// IsHealthy returns true if ZMQ is working properly
func (z *ZMQListener) IsHealthy() bool {
	status := z.Status()
	return status == ZMQStatusHealthy || status == ZMQStatusConnecting
}

// IsFailed returns true if ZMQ has failed and we're using polling
func (z *ZMQListener) IsFailed() bool {
	return z.Status() == ZMQStatusFailed
}

// IsRunning returns whether the listener is active.
func (z *ZMQListener) IsRunning() bool {
	return z.running.Load()
}

// Stats returns ZMQ listener statistics
type ZMQStats struct {
	Status           string
	MessagesReceived uint64
	ErrorsCount      uint64
	LastMessageAge   time.Duration
	FailureDuration  time.Duration
	HealthyDuration  time.Duration
	StabilityReached bool
}

func (z *ZMQListener) Stats() ZMQStats {
	stats := ZMQStats{
		Status:           z.Status().String(),
		MessagesReceived: z.messagesReceived.Load(),
		ErrorsCount:      z.errorsCount.Load(),
		StabilityReached: z.stabilityReached.Load(),
	}

	lastMsg := z.lastMessageTime.Load()
	if lastMsg > 0 {
		stats.LastMessageAge = time.Since(time.Unix(lastMsg, 0))
	}

	failureStart := z.failureStartTime.Load()
	if failureStart > 0 {
		stats.FailureDuration = time.Since(time.Unix(failureStart, 0))
	}

	healthyStart := z.healthyStartTime.Load()
	if healthyStart > 0 {
		stats.HealthyDuration = time.Since(time.Unix(healthyStart, 0))
	}

	return stats
}

// TestConnection tests the ZMQ connection by attempting to connect and receive
// Returns nil if successful, error otherwise
func (z *ZMQListener) TestConnection(timeout time.Duration) error {
	z.logger.Info("Testing ZMQ connection...")

	// Create a test socket with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	testSocket := zmq4.NewSub(ctx, zmq4.WithTimeout(timeout))
	defer testSocket.Close()

	// Connect
	if err := testSocket.Dial(z.cfg.Endpoint); err != nil {
		return fmt.Errorf("failed to connect to %s: %w", z.cfg.Endpoint, err)
	}

	// Subscribe
	if err := testSocket.SetOption(zmq4.OptionSubscribe, "hashblock"); err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	z.logger.Infow("ZMQ test connection successful",
		"endpoint", z.cfg.Endpoint,
	)

	return nil
}
