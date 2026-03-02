// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares implements the share processing pipeline.
// It handles validation, buffering, and batch database writes.
// CRITICAL: Uses Write-Ahead Log (WAL) designed to minimize share loss on crash.
package shares

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/metrics"
	"github.com/spiralpool/stratum/pkg/protocol"
	"github.com/spiralpool/stratum/pkg/ringbuffer"
	"go.uber.org/zap"
)

// Database health thresholds
const (
	// MaxDBWriteFailures before marking DB as unhealthy
	MaxDBWriteFailures = 5
	// MaxDBWriteFailuresCritical before alerting operator
	MaxDBWriteFailuresCritical = 20
	// DBWriteRetryDelay between retry attempts
	DBWriteRetryDelay = 5 * time.Second
	// MaxRetryAttempts per batch before dropping
	MaxRetryAttempts = 3
)

// Backpressure thresholds
// These control when the pipeline signals upstream to slow down share submission.
// The goal is to prevent buffer overflow WITHOUT dropping valid shares.
const (
	// BackpressureWarnThreshold triggers warning when buffer is 70% full
	BackpressureWarnThreshold = 0.70
	// BackpressureCriticalThreshold triggers critical backpressure at 90% full
	BackpressureCriticalThreshold = 0.90
	// BackpressureEmergencyThreshold triggers emergency at 98% (near overflow)
	BackpressureEmergencyThreshold = 0.98
)

// BackpressureLevel indicates current pipeline pressure
type BackpressureLevel int

const (
	// BackpressureNone - pipeline operating normally
	BackpressureNone BackpressureLevel = iota
	// BackpressureWarn - buffer filling up, consider increasing difficulty
	BackpressureWarn
	// BackpressureCritical - buffer nearly full, should increase difficulty
	BackpressureCritical
	// BackpressureEmergency - buffer overflow imminent, must reduce share rate
	BackpressureEmergency
)

func (b BackpressureLevel) String() string {
	switch b {
	case BackpressureNone:
		return "none"
	case BackpressureWarn:
		return "warn"
	case BackpressureCritical:
		return "critical"
	case BackpressureEmergency:
		return "emergency"
	default:
		return "unknown"
	}
}

// SuggestedDifficultyMultiplier returns how much to multiply difficulty
// to reduce share rate and relieve backpressure.
// Returns 1.0 for no change, >1.0 to increase difficulty.
func (b BackpressureLevel) SuggestedDifficultyMultiplier() float64 {
	switch b {
	case BackpressureNone:
		return 1.0 // No change
	case BackpressureWarn:
		return 1.5 // Increase difficulty by 50%
	case BackpressureCritical:
		return 2.0 // Double difficulty
	case BackpressureEmergency:
		return 4.0 // Quadruple difficulty
	default:
		return 1.0
	}
}

// Pipeline processes shares from validation through database persistence.
// CRITICAL: Uses Write-Ahead Log (WAL) designed to minimize share loss on crash.
type Pipeline struct {
	cfg    *config.DatabaseConfig
	logger *zap.SugaredLogger

	// Ring buffer for share queuing (lock-free MPSC)
	buffer *ringbuffer.RingBuffer[*protocol.Share]

	// Batch writer
	batchSize     int
	flushInterval time.Duration
	batchChan     chan []*protocol.Share

	// Database writer (interface for testability)
	writer ShareWriter

	// Write-Ahead Log for crash recovery (designed to minimize share loss)
	wal     *WAL
	walPath string // Data directory for WAL storage
	poolID  string // Pool identifier for WAL namespacing

	// State
	running  atomic.Bool
	wg       sync.WaitGroup
	stopOnce sync.Once

	// Shutdown deadline — when set, batchWriter skips retries if deadline is near.
	// This enables cooperative deadline enforcement so the pipeline doesn't block
	// beyond systemd's SIGKILL timeout. Shares in WAL are recoverable on next startup.
	shutdownDeadline atomic.Value // stores time.Time (zero value = no deadline)

	// Metrics
	processed atomic.Uint64
	written   atomic.Uint64
	dropped   atomic.Uint64
	retried   atomic.Uint64 // Batches that required retry

	// Database health tracking
	dbWriteFailures atomic.Int32 // Consecutive DB write failures
	isDBDegraded    atomic.Bool  // True if DB writes are failing
	isDBCritical    atomic.Bool  // True if DB is critically unhealthy

	// Circuit breaker for database write retries
	circuitBreaker *database.CircuitBreaker

	// Backpressure tracking
	lastBackpressureLevel atomic.Int32  // Last logged backpressure level (to avoid log spam)
	backpressureCallback  func(BackpressureLevel) // Optional callback for backpressure changes

	// Prometheus metrics (optional, nil = disabled)
	metrics *metrics.Metrics
}

// ShareWriter defines the interface for persisting shares.
type ShareWriter interface {
	WriteBatch(ctx context.Context, shares []*protocol.Share) error
	Close() error
}

// NewPipeline creates a new share processing pipeline.
func NewPipeline(cfg *config.DatabaseConfig, writer ShareWriter, logger *zap.Logger) *Pipeline {
	return &Pipeline{
		cfg:            cfg,
		logger:         logger.Sugar(),
		buffer:         ringbuffer.New[*protocol.Share](1 << 20), // 1M capacity
		batchSize:      cfg.Batching.Size,
		flushInterval:  cfg.Batching.Interval,
		batchChan:      make(chan []*protocol.Share, 200), // Sized for burst handling
		writer:         writer,
		circuitBreaker: database.NewCircuitBreaker(database.DefaultCircuitBreakerConfig()),
	}
}

// Start begins the share processing pipeline.
// CRITICAL: Initializes WAL and replays any uncommitted shares from previous crash.
func (p *Pipeline) Start(ctx context.Context) error {
	// Initialize WAL for crash recovery
	if p.walPath != "" && p.poolID != "" {
		wal, err := NewWAL(p.walPath, p.poolID, p.logger.Desugar())
		if err != nil {
			return fmt.Errorf("failed to initialize share WAL: %w", err)
		}
		p.wal = wal

		// Replay any uncommitted shares from previous run
		uncommitted, err := p.wal.Replay()
		if err != nil {
			p.logger.Warnw("WAL replay failed - some shares may be lost", "error", err)
		} else if len(uncommitted) > 0 {
			p.logger.Infow("Replaying uncommitted shares from WAL",
				"count", len(uncommitted),
			)
			// Re-enqueue uncommitted shares
			for _, share := range uncommitted {
				if p.buffer.TryEnqueue(share) {
					p.processed.Add(1)
				} else {
					p.logger.Errorw("Buffer full during WAL replay - share lost",
						"miner", share.MinerAddress,
						"worker", share.WorkerName,
					)
					p.dropped.Add(1)
				}
			}
		}
	}

	p.running.Store(true)

	// Start batch collector
	p.wg.Add(1)
	go p.batchCollector(ctx)

	// Start batch writer
	p.wg.Add(1)
	go p.batchWriter(ctx)

	// Start periodic share loss rate updater
	if p.metrics != nil {
		p.wg.Add(1)
		go p.shareLossRateUpdater(ctx)
	}

	walStatus := "disabled"
	if p.wal != nil {
		walStatus = "enabled"
	}

	p.logger.Infow("Share pipeline started",
		"batchSize", p.batchSize,
		"flushInterval", p.flushInterval,
		"bufferCapacity", p.buffer.Cap(),
		"walStatus", walStatus,
	)

	return nil
}

// Stop gracefully shuts down the pipeline, flushing remaining shares.
func (p *Pipeline) Stop() error {
	var stopErr error
	p.stopOnce.Do(func() {
		p.running.Store(false)

		// Wait for goroutines to finish
		p.wg.Wait()

		// Close writer
		if p.writer != nil {
			_ = p.writer.Close() // #nosec G104
		}

		// Close WAL
		if p.wal != nil {
			if err := p.wal.Close(); err != nil {
				p.logger.Warnw("WAL close failed", "error", err)
				stopErr = err
			}
		}

		stats := p.buffer.Stats()
		p.logger.Infow("Share pipeline stopped",
			"processed", p.processed.Load(),
			"written", p.written.Load(),
			"dropped", p.dropped.Load(),
			"bufferRemaining", stats.Current,
		)
	})
	return stopErr
}

// StopWithDeadline gracefully shuts down the pipeline with a hard deadline.
// P-1 FIX: If goroutines don't finish by the deadline, we log and return
// instead of blocking indefinitely. WAL ensures unwritten shares are recoverable.
func (p *Pipeline) StopWithDeadline(deadline time.Time) error {
	var stopErr error
	p.stopOnce.Do(func() {
		// Store deadline so batchWriter can cooperatively check it during retries
		p.shutdownDeadline.Store(deadline)

		p.running.Store(false)

		// Wait for goroutines with deadline
		remaining := time.Until(deadline)
		if remaining <= 0 {
			remaining = 1 * time.Second // Minimum grace period
		}

		done := make(chan struct{})
		go func() {
			p.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			p.logger.Info("Share pipeline goroutines stopped cleanly")
		case <-time.After(remaining):
			stats := p.buffer.Stats()
			p.logger.Warnw("Share pipeline deadline reached - goroutines still running (WAL preserves unwritten shares)",
				"deadline", deadline.Format(time.RFC3339),
				"bufferRemaining", stats.Current,
				"batchChanLen", len(p.batchChan),
			)
		}

		// Close writer (short timeout — best effort)
		if p.writer != nil {
			_ = p.writer.Close() // #nosec G104
		}

		// Close WAL
		if p.wal != nil {
			if err := p.wal.Close(); err != nil {
				p.logger.Warnw("WAL close failed", "error", err)
				stopErr = err
			}
		}

		stats := p.buffer.Stats()
		p.logger.Infow("Share pipeline stopped",
			"processed", p.processed.Load(),
			"written", p.written.Load(),
			"dropped", p.dropped.Load(),
			"bufferRemaining", stats.Current,
		)
	})
	return stopErr
}

// Submit adds a share to the pipeline.
// CRITICAL: Writes to WAL before enqueuing to minimize share loss on crash.
// This is safe for concurrent calls from multiple goroutines.
// Returns false if the buffer is full (backpressure) or WAL write fails.
//
// BACKPRESSURE: This method checks buffer fill and notifies via callback
// if backpressure level changes. Upstream (pool) should monitor backpressure
// and increase difficulty to reduce share rate when buffer is filling up.
func (p *Pipeline) Submit(share *protocol.Share) bool {
	if !p.running.Load() {
		return false
	}

	// Write to WAL first (if enabled) - share is only safe after WAL write
	if p.wal != nil {
		if err := p.wal.Write(share); err != nil {
			p.logger.Errorw("WAL write failed - share at risk",
				"error", err,
				"miner", share.MinerAddress,
				"worker", share.WorkerName,
			)
			// Continue anyway - better to have share in memory than nowhere
		}
	}

	if p.buffer.TryEnqueue(share) {
		p.processed.Add(1)
		// Check backpressure after successful enqueue (not on every call to reduce overhead)
		// Only check periodically based on processed count to minimize overhead
		if p.processed.Load()%100 == 0 {
			p.checkAndNotifyBackpressure()
		}
		return true
	}

	// Buffer full - this is the situation backpressure should prevent
	// Log as error since this means backpressure signaling wasn't acted upon
	p.logger.Errorw("🚨 SHARE BUFFER OVERFLOW - backpressure not acted upon!",
		"miner", share.MinerAddress,
		"worker", share.WorkerName,
		"bufferFill", p.bufferFillPercent(),
		"action", "Increase difficulty immediately to reduce share rate",
	)
	p.dropped.Add(1)
	return false
}

// batchCollector reads from the ring buffer and creates batches.
func (p *Pipeline) batchCollector(ctx context.Context) {
	defer p.wg.Done()

	batch := make([]*protocol.Share, p.batchSize)
	flushTicker := time.NewTicker(p.flushInterval)
	defer flushTicker.Stop()

	// Poll ticker checks buffer fullness at 10ms intervals without busy-waiting.
	// Previous implementation used default+Sleep which consumed CPU even when idle.
	pollTicker := time.NewTicker(10 * time.Millisecond)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush
			p.flushRemaining(ctx, batch)
			return

		case <-flushTicker.C:
			// Time-based flush
			n := p.buffer.DequeueBatch(batch)
			if n > 0 {
				p.sendBatch(batch[:n])
			}

		case <-pollTicker.C:
			// Check if we have a full batch ready
			if p.buffer.Len() >= p.batchSize {
				n := p.buffer.DequeueBatch(batch)
				if n > 0 {
					p.sendBatch(batch[:n])
				}
			}
		}

		if !p.running.Load() {
			p.flushRemaining(ctx, batch)
			return
		}
	}
}

// sendBatch sends a batch to the writer channel.
func (p *Pipeline) sendBatch(shares []*protocol.Share) {
	// Copy the batch to avoid race conditions
	batch := make([]*protocol.Share, len(shares))
	copy(batch, shares)

	select {
	case p.batchChan <- batch:
	default:
		// Channel full, drop batch (shouldn't happen with proper sizing)
		p.dropped.Add(uint64(len(shares)))
		if p.metrics != nil {
			p.metrics.RecordBatchDrop(len(shares))
		}
		p.logger.Warnw("Batch channel full, dropping shares",
			"count", len(shares),
		)
	}
}

// flushRemaining drains the buffer during shutdown.
func (p *Pipeline) flushRemaining(_ context.Context, batch []*protocol.Share) {
	for p.buffer.Len() > 0 {
		n := p.buffer.DequeueBatch(batch)
		if n > 0 {
			p.sendBatch(batch[:n])
		}
	}
	close(p.batchChan)
}

// batchWriter writes batches to the database with circuit breaker protection.
// Uses exponential backoff on retries and stops attempting when circuit is open.
// WAL ensures shares are recoverable even when circuit blocks writes.
func (p *Pipeline) batchWriter(ctx context.Context) {
	defer p.wg.Done()

	for batch := range p.batchChan {
		if len(batch) == 0 {
			continue
		}

		// Check circuit breaker before attempting write
		allowed, waitDuration := p.circuitBreaker.AllowRequest()
		if !allowed {
			// Circuit is open - skip write, WAL will recover on restart
			p.logger.Warnw("Circuit breaker OPEN - skipping batch write (WAL will recover)",
				"batchSize", len(batch),
				"cooldownRemaining", waitDuration.Round(time.Second),
				"circuitState", p.circuitBreaker.State().String(),
			)
			p.dropped.Add(uint64(len(batch)))
			if p.metrics != nil {
				p.metrics.RecordBatchDrop(len(batch))
			}
			continue
		}

		// Attempt write with retries and exponential backoff
		var lastErr error
		for attempt := 1; attempt <= MaxRetryAttempts; attempt++ {
			writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := p.writer.WriteBatch(writeCtx, batch)
			cancel()

			if err == nil {
				// Success - commit batch to WAL (marks shares as persisted)
				// FIX: Use CommitBatchVerified for atomic commit+sync to minimize replay window
				// This ensures the commit marker is durably persisted before we proceed
				if p.wal != nil {
					committed, walErr := p.wal.CommitBatchVerified(batch)
					if walErr != nil {
						// CRITICAL: WAL commit failed but shares ARE in DB
						// On crash before next successful commit, these may replay (duplicate, not loss)
						p.logger.Errorw("WAL commit verification failed - shares in DB but may replay on crash",
							"error", walErr,
							"batchSize", len(batch),
							"impact", "potential duplicate shares on crash (no data loss)",
						)
					} else if !committed {
						// Should not happen with CommitBatchVerified, but log just in case
						p.logger.Warnw("WAL commit returned false - marker may not be durable",
							"batchSize", len(batch),
						)
					}
				}

				// Reset circuit breaker and failure tracking
				p.circuitBreaker.RecordSuccess()
				p.written.Add(uint64(len(batch)))
				if p.dbWriteFailures.Load() > 0 {
					p.dbWriteFailures.Store(0)
					if p.isDBDegraded.Load() {
						p.isDBDegraded.Store(false)
						p.logger.Infow("Database health recovered from degraded state",
							"circuitState", "closed",
						)
					}
					if p.isDBCritical.Load() {
						p.isDBCritical.Store(false)
						p.logger.Infow("Database health recovered from critical state",
							"circuitState", "closed",
						)
					}
				}
				lastErr = nil
				break
			}

			// Record failure and get exponential backoff duration
			lastErr = err
			backoff := p.circuitBreaker.RecordFailure()

			if attempt < MaxRetryAttempts {
				// P-1 FIX: Check shutdown deadline before starting another retry.
				// If we're within 15s of the deadline, skip remaining retries —
				// shares are already in WAL and will replay on next startup.
				if dl, ok := p.shutdownDeadline.Load().(time.Time); ok && !dl.IsZero() {
					if time.Until(dl) < 15*time.Second {
						p.logger.Warnw("Shutdown deadline approaching - skipping retry (shares in WAL)",
							"batchSize", len(batch),
							"deadline", dl.Format(time.RFC3339),
							"remaining", time.Until(dl).Round(time.Millisecond),
						)
						p.dropped.Add(uint64(len(batch)))
						if p.metrics != nil {
							p.metrics.RecordBatchDrop(len(batch))
						}
						lastErr = nil // Don't double-count in failure tracking below
						break
					}
				}

				p.retried.Add(1)
				p.logger.Warnw("Database write failed, retrying with backoff...",
					"error", err,
					"attempt", attempt,
					"maxAttempts", MaxRetryAttempts,
					"backoff", backoff.Round(time.Millisecond),
					"batchSize", len(batch),
					"circuitState", p.circuitBreaker.State().String(),
				)

				select {
				case <-ctx.Done():
					p.logger.Warnw("Context cancelled during retry, batch in WAL for recovery",
						"batchSize", len(batch),
					)
					p.dropped.Add(uint64(len(batch)))
					if p.metrics != nil {
						p.metrics.RecordBatchDrop(len(batch))
					}
					return
				case <-time.After(backoff):
					// Continue to next retry attempt
				}

				// Re-check circuit breaker after backoff (may have opened)
				if allowed, _ := p.circuitBreaker.AllowRequest(); !allowed {
					p.logger.Warnw("Circuit breaker opened during retry - aborting batch",
						"batchSize", len(batch),
						"circuitState", p.circuitBreaker.State().String(),
					)
					p.dropped.Add(uint64(len(batch)))
					if p.metrics != nil {
						p.metrics.RecordBatchDrop(len(batch))
					}
					lastErr = nil // Don't double-count in failure tracking below
					break
				}
			}
		}

		if lastErr != nil {
			// All retries exhausted - track failure
			failures := p.dbWriteFailures.Add(1)
			p.dropped.Add(uint64(len(batch)))
			if p.metrics != nil {
				p.metrics.RecordBatchDrop(len(batch))
			}

			// Check degradation thresholds (kept for backward compatibility with monitoring)
			if failures >= MaxDBWriteFailuresCritical {
				if !p.isDBCritical.Load() {
					p.isDBCritical.Store(true)
					p.logger.Errorw("🚨 CRITICAL: Database write failures exceeded critical threshold - circuit breaker OPEN",
						"consecutiveFailures", failures,
						"threshold", MaxDBWriteFailuresCritical,
						"error", lastErr,
						"shareCount", len(batch),
						"totalDropped", p.dropped.Load(),
						"circuitState", p.circuitBreaker.State().String(),
					)
				}
			} else if failures >= MaxDBWriteFailures {
				if !p.isDBDegraded.Load() {
					p.isDBDegraded.Store(true)
					p.logger.Warnw("⚠️ WARNING: Database health degraded - consecutive write failures",
						"consecutiveFailures", failures,
						"threshold", MaxDBWriteFailures,
						"error", lastErr,
						"circuitState", p.circuitBreaker.State().String(),
					)
				}
			}

			p.logger.Errorw("Failed to write share batch after all retries - SHARES IN WAL FOR RECOVERY",
				"error", lastErr,
				"attempts", MaxRetryAttempts,
				"shareCount", len(batch),
				"consecutiveFailures", failures,
			)
		}
	}
}

// SetMetrics sets the Prometheus metrics collector for share pipeline observability.
func (p *Pipeline) SetMetrics(m *metrics.Metrics) {
	p.metrics = m
}

// shareLossRateUpdater periodically updates the share loss rate metric.
func (p *Pipeline) shareLossRateUpdater(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.metrics != nil {
				p.metrics.UpdateShareLossRate(p.processed.Load(), p.dropped.Load())
			}
		}
	}
}

// DBHealthStatus returns the current database health status for monitoring.
func (p *Pipeline) DBHealthStatus() (failures int32, degraded, critical bool, dropped uint64, circuitState string) {
	failures = p.dbWriteFailures.Load()
	degraded = p.isDBDegraded.Load()
	critical = p.isDBCritical.Load()
	dropped = p.dropped.Load()
	if p.circuitBreaker != nil {
		circuitState = p.circuitBreaker.State().String()
	} else {
		circuitState = "disabled"
	}
	return
}

// CircuitBreakerStats returns circuit breaker statistics for monitoring.
func (p *Pipeline) CircuitBreakerStats() database.CircuitBreakerStats {
	if p.circuitBreaker == nil {
		return database.CircuitBreakerStats{}
	}
	return p.circuitBreaker.Stats()
}

// Stats returns pipeline statistics.
type Stats struct {
	Processed      uint64
	Written        uint64
	Dropped        uint64
	BufferCurrent  int
	BufferCapacity int
}

func (p *Pipeline) Stats() Stats {
	bufStats := p.buffer.Stats()
	return Stats{
		Processed:      p.processed.Load(),
		Written:        p.written.Load(),
		Dropped:        p.dropped.Load(),
		BufferCurrent:  bufStats.Current,
		BufferCapacity: bufStats.Capacity,
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// BACKPRESSURE API
// ═══════════════════════════════════════════════════════════════════════════════
//
// The backpressure system provides upstream signaling to prevent share loss.
// When the buffer fills up, instead of dropping shares, we signal that
// difficulty should be increased to reduce share submission rate.
//
// DESIGN PRINCIPLES:
// 1. NEVER reject valid shares - backpressure is advisory, not blocking
// 2. Use difficulty as the throttling mechanism (miner-transparent)
// 3. Provide clear levels for upstream decision-making
// 4. Support both polling (GetBackpressureLevel) and push (callback) models
// ═══════════════════════════════════════════════════════════════════════════════

// GetBackpressureLevel returns the current backpressure level based on buffer fill.
// This should be polled periodically by the pool to adjust difficulty as needed.
func (p *Pipeline) GetBackpressureLevel() BackpressureLevel {
	stats := p.buffer.Stats()
	if stats.Capacity == 0 {
		return BackpressureNone
	}

	fillRatio := float64(stats.Current) / float64(stats.Capacity)

	switch {
	case fillRatio >= BackpressureEmergencyThreshold:
		return BackpressureEmergency
	case fillRatio >= BackpressureCriticalThreshold:
		return BackpressureCritical
	case fillRatio >= BackpressureWarnThreshold:
		return BackpressureWarn
	default:
		return BackpressureNone
	}
}

// SetBackpressureCallback sets a callback that will be invoked when
// backpressure level changes. This enables push-based backpressure notification.
// The callback receives the new backpressure level.
// MUST be called before Start(). The callback itself should be thread-safe.
func (p *Pipeline) SetBackpressureCallback(callback func(BackpressureLevel)) {
	p.backpressureCallback = callback
}

// checkAndNotifyBackpressure checks the current backpressure level and
// notifies via callback if it has changed. Called during submit operations.
func (p *Pipeline) checkAndNotifyBackpressure() {
	level := p.GetBackpressureLevel()
	lastLevel := BackpressureLevel(p.lastBackpressureLevel.Load())

	if level != lastLevel {
		p.lastBackpressureLevel.Store(int32(level))

		// Log level changes (but not every share)
		if level > lastLevel {
			p.logger.Warnw("⚠️ Backpressure increased",
				"level", level.String(),
				"previousLevel", lastLevel.String(),
				"suggestedDiffMultiplier", level.SuggestedDifficultyMultiplier(),
				"bufferFillPercent", p.bufferFillPercent(),
			)
		} else {
			p.logger.Infow("✅ Backpressure decreased",
				"level", level.String(),
				"previousLevel", lastLevel.String(),
				"bufferFillPercent", p.bufferFillPercent(),
			)
		}

		// Notify via callback if registered
		// AUDIT FIX: Panic recovery prevents a misbehaving callback from crashing
		// the Submit goroutine, which would silently stop all share processing.
		if p.backpressureCallback != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						p.logger.Errorw("Backpressure callback panicked (recovered)",
							"panic", r,
							"level", level.String(),
						)
					}
				}()
				p.backpressureCallback(level)
			}()
		}
	}
}

// bufferFillPercent returns current buffer fill as a percentage string.
func (p *Pipeline) bufferFillPercent() string {
	stats := p.buffer.Stats()
	if stats.Capacity == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(stats.Current)/float64(stats.Capacity)*100)
}

// BackpressureStats provides detailed backpressure information for monitoring.
type BackpressureStats struct {
	Level                        BackpressureLevel
	BufferCurrent                int
	BufferCapacity               int
	FillPercent                  float64
	SuggestedDifficultyMultiplier float64
}

// GetBackpressureStats returns detailed backpressure statistics.
func (p *Pipeline) GetBackpressureStats() BackpressureStats {
	stats := p.buffer.Stats()
	level := p.GetBackpressureLevel()

	fillPercent := 0.0
	if stats.Capacity > 0 {
		fillPercent = float64(stats.Current) / float64(stats.Capacity) * 100
	}

	return BackpressureStats{
		Level:                        level,
		BufferCurrent:                stats.Current,
		BufferCapacity:               stats.Capacity,
		FillPercent:                  fillPercent,
		SuggestedDifficultyMultiplier: level.SuggestedDifficultyMultiplier(),
	}
}
