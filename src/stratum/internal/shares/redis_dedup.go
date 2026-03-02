// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares provides share validation for mining pools.
//
// This file implements an optional Redis-backed duplicate tracker for HA deployments.
// The Redis dedup cache provides cross-node share deduplication during brief failover windows.
//
// USAGE:
//   - Enable via REDIS_DEDUP_ENABLED=true environment variable
//   - Requires Redis server (can be standalone or Sentinel)
//   - Falls back to in-memory tracking if Redis unavailable
//
// DESIGN NOTES:
//   - Uses Redis SET with automatic TTL expiration
//   - Key format: "spiralpool:share:{jobID}:{extranonce1+extranonce2+ntime+nonce}"
//   - Default TTL: 10 minutes (matches in-memory tracker)
//   - Non-blocking: failures fall back to local tracking
package shares

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/spiralpool/stratum/internal/metrics"
	"go.uber.org/zap"
)

// RedisDedupConfig holds configuration for Redis-based duplicate tracking.
type RedisDedupConfig struct {
	// Redis connection
	Addr     string // Redis address (host:port)
	Password string // Redis password (optional)
	DB       int    // Redis database number

	// Sentinel configuration (optional)
	SentinelAddrs  []string // Sentinel addresses for HA Redis
	SentinelMaster string   // Sentinel master name

	// TTL for share keys
	TTL time.Duration

	// Fallback behavior
	FallbackToLocal bool // Fall back to in-memory on Redis failure
}

// RedisDedupTracker provides Redis-backed duplicate share tracking for HA deployments.
// It maintains cross-node share deduplication during failover windows.
type RedisDedupTracker struct {
	client  redis.UniversalClient
	config  RedisDedupConfig
	enabled atomic.Bool // SECURITY: Use atomic.Bool to prevent data race on concurrent read/write
	logger  *zap.SugaredLogger

	// Local fallback tracker
	localTracker *DuplicateTracker
	mu           sync.RWMutex

	// SECURITY: Grace period after Redis reconnection. During this window, BOTH
	// local and Redis trackers are checked to catch shares that were recorded only
	// in the local tracker while Redis was down. Without this, shares submitted
	// during an outage would not be detected as duplicates once Redis recovers.
	recoveryGracePeriod time.Time

	// Metrics
	redisHits   uint64
	redisMisses uint64
	redisErrors uint64
	localHits   uint64

	// Prometheus metrics (optional, nil = disabled)
	promMetrics *metrics.Metrics
}

// DefaultRedisDedupConfig returns default configuration for Redis dedup.
func DefaultRedisDedupConfig() RedisDedupConfig {
	return RedisDedupConfig{
		Addr:            "localhost:6379",
		Password:        "",
		DB:              0,
		TTL:             10 * time.Minute,
		FallbackToLocal: true,
	}
}

// NewRedisDedupTracker creates a new Redis-backed duplicate tracker.
// If Redis is unavailable and FallbackToLocal is true, uses in-memory tracking.
func NewRedisDedupTracker(config RedisDedupConfig, logger *zap.SugaredLogger) *RedisDedupTracker {
	tracker := &RedisDedupTracker{
		config:       config,
		logger:       logger,
		localTracker: NewDuplicateTracker(),
		// enabled is atomic.Bool, zero value is false
	}

	// Check if Redis dedup is enabled via environment
	if enabled := os.Getenv("REDIS_DEDUP_ENABLED"); enabled != "true" {
		logger.Info("Redis share dedup disabled (set REDIS_DEDUP_ENABLED=true to enable)")
		return tracker
	}

	// Override config from environment
	if addr := os.Getenv("REDIS_DEDUP_ADDR"); addr != "" {
		config.Addr = addr
	}
	if password := os.Getenv("REDIS_DEDUP_PASSWORD"); password != "" {
		config.Password = password
	}
	if dbStr := os.Getenv("REDIS_DEDUP_DB"); dbStr != "" {
		if db, err := strconv.Atoi(dbStr); err == nil {
			config.DB = db
		}
	}
	if ttlStr := os.Getenv("REDIS_DEDUP_TTL"); ttlStr != "" {
		if ttl, err := time.ParseDuration(ttlStr); err == nil {
			config.TTL = ttl
		}
	}

	// R-1: Update tracker.config AFTER all environment overrides are applied,
	// so that runtime fields (e.g., TTL used in SETNX) reflect the final values.
	tracker.config = config

	// Create Redis client with HA-tuned timeouts and pool settings.
	// These defaults prevent slow Redis operations from blocking share validation
	// and ensure rapid recovery during Redis Sentinel failover.
	var client redis.UniversalClient
	if len(config.SentinelAddrs) > 0 {
		// Sentinel mode for HA Redis
		client = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:      config.SentinelMaster,
			SentinelAddrs:   config.SentinelAddrs,
			Password:        config.Password,
			DB:              config.DB,
			DialTimeout:     5 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			PoolSize:        10,
			MinIdleConns:    2,
			MaxRetries:      3,
			MinRetryBackoff: 100 * time.Millisecond,
			MaxRetryBackoff: 2 * time.Second,
		})
	} else {
		// Standalone Redis
		client = redis.NewClient(&redis.Options{
			Addr:            config.Addr,
			Password:        config.Password,
			DB:              config.DB,
			DialTimeout:     5 * time.Second,
			ReadTimeout:     3 * time.Second,
			WriteTimeout:    3 * time.Second,
			PoolSize:        10,
			MinIdleConns:    2,
			MaxRetries:      3,
			MinRetryBackoff: 100 * time.Millisecond,
			MaxRetryBackoff: 2 * time.Second,
		})
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		logger.Warnf("Redis share dedup connection failed: %v (using local fallback)", err)
		if !config.FallbackToLocal {
			return nil
		}
		return tracker
	}

	tracker.client = client
	tracker.enabled.Store(true)
	logger.Info("Redis share dedup enabled",
		"addr", config.Addr,
		"ttl", config.TTL,
	)

	return tracker
}

// shareKey generates the Redis key for a share.
// SECURITY: Use colon delimiter between fields to prevent key collision attacks
// where different field combinations produce the same concatenated key.
// SECURITY: Normalize hex case to prevent duplicate share exploit.
func (rt *RedisDedupTracker) shareKey(jobID, extranonce1, extranonce2, ntime, nonce string) string {
	return fmt.Sprintf("spiralpool:share:%s:%s:%s:%s:%s", jobID, strings.ToLower(extranonce1), strings.ToLower(extranonce2), strings.ToLower(ntime), strings.ToLower(nonce))
}

// RecordIfNew atomically checks if a share is a duplicate and records it if not.
// Returns true if the share was new (recorded), false if it was a duplicate (rejected).
func (rt *RedisDedupTracker) RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce string) bool {
	// Always check local tracker first for fast path
	rt.mu.RLock()
	localEnabled := rt.localTracker != nil
	recoveryDeadline := rt.recoveryGracePeriod
	rt.mu.RUnlock()

	// If Redis not enabled, use local tracker only
	if !rt.enabled.Load() || rt.client == nil {
		if localEnabled {
			return rt.localTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
		}
		return true // No tracking available, accept share
	}

	// SECURITY: During recovery grace period after Redis reconnects, check BOTH
	// local tracker and Redis. Shares recorded only in the local tracker while
	// Redis was down would otherwise be accepted as new by Redis alone.
	inRecoveryGrace := time.Now().Before(recoveryDeadline)
	if inRecoveryGrace && localEnabled {
		if !rt.localTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce) {
			atomic.AddUint64(&rt.localHits, 1)
			return false // Duplicate found in local tracker during grace period
		}
	}

	key := rt.shareKey(jobID, extranonce1, extranonce2, ntime, nonce)

	// Use Redis SETNX (set if not exists) for atomic check-and-record
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// SETNX with TTL - returns true if key was set (new share), false if exists (duplicate)
	set, err := rt.client.SetNX(ctx, key, "1", rt.config.TTL).Result()
	if err != nil {
		// Redis error - fall back to local tracking
		atomic.AddUint64(&rt.redisErrors, 1)
		if rt.config.FallbackToLocal && localEnabled {
			atomic.AddUint64(&rt.localHits, 1)
			// During grace period we already called RecordIfNew above, use IsDuplicate
			if inRecoveryGrace {
				return true // Already checked local above, and it was new
			}
			return rt.localTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
		}
		// If no fallback, accept share (safe default - may allow duplicate)
		return true
	}

	if set {
		atomic.AddUint64(&rt.redisMisses, 1) // Cache miss = new share
		// Also record locally for consistency (if not already recorded during grace period)
		if localEnabled && !inRecoveryGrace {
			rt.localTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
		}
		return true // New share - accepted
	}

	atomic.AddUint64(&rt.redisHits, 1) // Cache hit = duplicate
	return false   // Duplicate - rejected
}

// IsDuplicate checks if a share with the given parameters has already been submitted.
// DEPRECATED: Use RecordIfNew for atomic check-and-record.
func (rt *RedisDedupTracker) IsDuplicate(jobID, extranonce1, extranonce2, ntime, nonce string) bool {
	if !rt.enabled.Load() || rt.client == nil {
		if rt.localTracker != nil {
			return rt.localTracker.IsDuplicate(jobID, extranonce1, extranonce2, ntime, nonce)
		}
		return false
	}

	key := rt.shareKey(jobID, extranonce1, extranonce2, ntime, nonce)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	exists, err := rt.client.Exists(ctx, key).Result()
	if err != nil {
		// Redis error - check local
		if rt.localTracker != nil {
			return rt.localTracker.IsDuplicate(jobID, extranonce1, extranonce2, ntime, nonce)
		}
		return false
	}

	return exists > 0
}

// CleanupJob removes duplicate tracking for a specific job.
// For Redis, keys expire automatically via TTL.
func (rt *RedisDedupTracker) CleanupJob(jobID string) {
	// Clean local tracker
	if rt.localTracker != nil {
		rt.localTracker.CleanupJob(jobID)
	}

	// For Redis, we could scan and delete keys, but TTL handles this automatically.
	// Only implement if explicit cleanup is needed for memory management.
}

// Stats returns tracking statistics.
func (rt *RedisDedupTracker) Stats() (jobs int, shares int, redisHits, redisMisses, redisErrors, localHits uint64) {
	if rt.localTracker != nil {
		jobs, shares = rt.localTracker.Stats()
	}
	return jobs, shares, atomic.LoadUint64(&rt.redisHits), atomic.LoadUint64(&rt.redisMisses), atomic.LoadUint64(&rt.redisErrors), atomic.LoadUint64(&rt.localHits)
}

// Close closes the Redis connection.
func (rt *RedisDedupTracker) Close() error {
	if rt.client != nil {
		return rt.client.Close()
	}
	return nil
}

// IsEnabled returns whether Redis dedup is active.
func (rt *RedisDedupTracker) IsEnabled() bool {
	return rt.enabled.Load()
}

// Client returns the underlying Redis client for advanced operations.
// Returns nil if Redis is not connected.
func (rt *RedisDedupTracker) Client() redis.UniversalClient {
	return rt.client
}

// StartHealthCheck starts a background goroutine that periodically pings Redis
// and switches between Redis and local fallback based on connectivity.
// This ensures the pool degrades gracefully when Redis becomes unavailable
// and automatically re-enables Redis dedup when connectivity is restored.
func (rt *RedisDedupTracker) StartHealthCheck(ctx context.Context) {
	go rt.healthCheckLoop(ctx)
}

func (rt *RedisDedupTracker) healthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.checkAndReconnect(ctx)
		}
	}
}

// SetMetrics sets the Prometheus metrics collector for Redis dedup observability.
// R-2: Uses mutex to prevent data race with concurrent reads in checkAndReconnect.
func (rt *RedisDedupTracker) SetMetrics(m *metrics.Metrics) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.promMetrics = m
}

func (rt *RedisDedupTracker) checkAndReconnect(ctx context.Context) {
	if rt.client == nil {
		// Redis was never successfully connected — nothing to monitor.
		// go-redis Sentinel handles reconnection internally when client exists.
		return
	}

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := rt.client.Ping(pingCtx).Err(); err != nil {
		if rt.enabled.Load() {
			rt.logger.Warnw("Redis health check failed, switching to local fallback",
				"error", err,
			)
			rt.enabled.Store(false)
			rt.mu.RLock()
			m := rt.promMetrics
			rt.mu.RUnlock()
			if m != nil {
				m.SetRedisHealth(false)
			}
			atomic.AddUint64(&rt.redisErrors, 1)
		}
	} else if !rt.enabled.Load() {
		rt.logger.Info("Redis connection restored, re-enabling Redis dedup with recovery grace period")
		// SECURITY: Set a grace period so that BOTH local and Redis are checked.
		// Shares recorded only in the local tracker during the outage would otherwise
		// pass Redis SETNX (since Redis has no record of them), allowing duplicates.
		rt.mu.Lock()
		rt.recoveryGracePeriod = time.Now().Add(10 * time.Minute)
		m := rt.promMetrics
		rt.mu.Unlock()
		rt.enabled.Store(true)
		if m != nil {
			m.SetRedisHealth(true)
			m.IncRedisReconnects()
		}
	}
}
