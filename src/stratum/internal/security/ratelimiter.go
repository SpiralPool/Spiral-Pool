// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package security provides security features for the stratum server
// Rate limiting, DDoS protection, and connection management
package security

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// H-2: StateBackend interface for distributed rate limiting in HA mode
// Implementations can use Redis, etcd, or other distributed stores
type StateBackend interface {
	// IncrementConnections atomically increments connection count for an IP
	// Returns the new count and whether the IP is banned
	IncrementConnections(ctx context.Context, ip string, window time.Duration) (count int, banned bool, err error)

	// DecrementConnections atomically decrements connection count for an IP
	DecrementConnections(ctx context.Context, ip string) error

	// IncrementShares atomically increments share count for an IP within a time window
	// Returns the new count within the current window
	IncrementShares(ctx context.Context, ip string, window time.Duration) (count int, err error)

	// BanIP bans an IP across all cluster nodes
	BanIP(ctx context.Context, ip string, duration time.Duration, reason string) error

	// UnbanIP removes a ban from an IP
	UnbanIP(ctx context.Context, ip string) error

	// IsIPBanned checks if an IP is banned on any cluster node
	IsIPBanned(ctx context.Context, ip string) (bool, error)

	// RecordViolation records a rate limit violation and returns current count
	RecordViolation(ctx context.Context, ip string) (violations int, err error)

	// Close closes the backend connection
	Close() error
}

// LocalBackend implements StateBackend using local memory (non-distributed)
// This is the default backend for single-node deployments
type LocalBackend struct {
	rl *RateLimiter
}

// NewLocalBackend creates a local (non-distributed) state backend
func NewLocalBackend(rl *RateLimiter) *LocalBackend {
	return &LocalBackend{rl: rl}
}

func (b *LocalBackend) IncrementConnections(_ context.Context, ip string, _ time.Duration) (int, bool, error) {
	b.rl.mu.Lock()
	defer b.rl.mu.Unlock()

	// Check ban first
	if banExpiry, banned := b.rl.bannedIPs[ip]; banned {
		if time.Now().Before(banExpiry) {
			return 0, true, nil
		}
		delete(b.rl.bannedIPs, ip)
	}

	state, exists := b.rl.connectionsByIP[ip]
	if !exists {
		state = &ipState{
			minuteStart: time.Now(),
			secondStart: time.Now(),
		}
		b.rl.connectionsByIP[ip] = state
	}

	state.connections++
	state.lastConnection = time.Now()
	return state.connections, false, nil
}

func (b *LocalBackend) DecrementConnections(_ context.Context, ip string) error {
	b.rl.mu.Lock()
	defer b.rl.mu.Unlock()

	if state, exists := b.rl.connectionsByIP[ip]; exists {
		state.connections--
		if state.connections < 0 {
			state.connections = 0
		}
	}
	return nil
}

func (b *LocalBackend) IncrementShares(_ context.Context, ip string, _ time.Duration) (int, error) {
	b.rl.mu.Lock()
	defer b.rl.mu.Unlock()

	state, exists := b.rl.connectionsByIP[ip]
	if !exists {
		return 1, nil
	}

	now := time.Now()
	if now.Sub(state.secondStart) > time.Second {
		state.shares = 0
		state.secondStart = now
	}

	state.shares++
	return state.shares, nil
}

func (b *LocalBackend) BanIP(_ context.Context, ip string, duration time.Duration, _ string) error {
	b.rl.mu.Lock()
	defer b.rl.mu.Unlock()
	b.rl.bannedIPs[ip] = time.Now().Add(duration)
	return nil
}

func (b *LocalBackend) UnbanIP(_ context.Context, ip string) error {
	b.rl.mu.Lock()
	defer b.rl.mu.Unlock()
	delete(b.rl.bannedIPs, ip)
	return nil
}

func (b *LocalBackend) IsIPBanned(_ context.Context, ip string) (bool, error) {
	b.rl.mu.RLock()
	defer b.rl.mu.RUnlock()
	if banExpiry, banned := b.rl.bannedIPs[ip]; banned {
		return time.Now().Before(banExpiry), nil
	}
	return false, nil
}

func (b *LocalBackend) RecordViolation(_ context.Context, ip string) (int, error) {
	b.rl.mu.Lock()
	defer b.rl.mu.Unlock()
	if state, exists := b.rl.connectionsByIP[ip]; exists {
		state.violations++
		return state.violations, nil
	}
	return 1, nil
}

func (b *LocalBackend) Close() error {
	return nil
}

// RateLimiter provides connection and share rate limiting per IP
type RateLimiter struct {
	mu sync.RWMutex

	// Configuration
	maxConnectionsPerIP  int
	maxConnectionsPerMin int
	maxSharesPerSecond   int
	banThreshold         int
	banDuration          time.Duration
	whitelistIPs         map[string]bool

	// RED-TEAM: Additional security configuration
	maxWorkersPerIP    int    // Max unique workers per IP
	banPersistencePath string // Path to persist bans

	// Audit #17: Serialized ban persistence channel
	// Prevents concurrent goroutines from racing on file writes
	persistCh chan struct{}

	// Graceful shutdown channel — prevents goroutine leaks from cleanupLoop/persistLoop
	stopCh   chan struct{}
	stopOnce sync.Once

	// State tracking
	connectionsByIP   map[string]*ipState
	bannedIPs         map[string]time.Time
	globalConnections int64

	// H-2: Distributed state backend for HA mode
	// If nil, local memory is used (default for single-node deployments)
	backend StateBackend

	// Metrics
	totalBlocked     int64
	totalBanned      int64
	totalRateLimited int64

	logger *zap.SugaredLogger
}

// ipState tracks connection state for a single IP
type ipState struct {
	connections    int
	connectionsMin int       // Connections in current minute
	minuteStart    time.Time // Start of current minute window
	shares         int       // Shares in current second
	secondStart    time.Time // Start of current second window
	violations     int       // Rate limit violations
	lastConnection time.Time

	// RED-TEAM: Worker identity tracking (prevents identity churn attacks)
	workers      map[string]bool // Set of unique worker names registered from this IP
	workersCount int             // Count of unique workers (for quick limit checks)
}

// RateLimiterConfig holds rate limiter configuration
type RateLimiterConfig struct {
	MaxConnectionsPerIP  int
	MaxConnectionsPerMin int
	MaxSharesPerSecond   int
	BanThreshold         int
	BanDuration          time.Duration
	WhitelistIPs         []string

	// H-2: Distributed backend configuration for HA mode
	// If Backend is non-nil, state is shared across cluster nodes
	Backend StateBackend

	// RED-TEAM: Additional security hardening options
	MaxWorkersPerIP    int    // Max unique workers per IP (identity churn protection)
	BanPersistencePath string // Path to persist bans across restarts
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(cfg RateLimiterConfig, logger *zap.SugaredLogger) *RateLimiter {
	whitelist := make(map[string]bool)
	for _, ip := range cfg.WhitelistIPs {
		// SECURITY: Validate whitelist IP format to prevent misconfiguration (SEC-11 fix)
		ip = extractIPString(ip) // Normalize - strip port if present
		if ip == "" {
			logger.Warnw("Skipping invalid whitelist IP entry (empty after normalization)",
				"original", ip)
			continue
		}
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil {
			// Try parsing as CIDR
			_, _, err := net.ParseCIDR(ip)
			if err != nil {
				logger.Warnw("Skipping invalid whitelist IP entry",
					"ip", ip,
					"error", "not a valid IP address or CIDR")
				continue
			}
		}
		// SECURITY: Normalize IPv4-mapped IPv6 to plain IPv4 so whitelist lookups
		// match regardless of whether the connection arrives as IPv4 or IPv6-mapped.
		if parsedIP != nil {
			if ip4 := parsedIP.To4(); ip4 != nil {
				parsedIP = ip4
			}
			ip = parsedIP.String()
		}
		whitelist[ip] = true
		logger.Debugw("Added IP to whitelist", "ip", ip)
	}

	rl := &RateLimiter{
		maxConnectionsPerIP:  cfg.MaxConnectionsPerIP,
		maxConnectionsPerMin: cfg.MaxConnectionsPerMin,
		maxSharesPerSecond:   cfg.MaxSharesPerSecond,
		banThreshold:         cfg.BanThreshold,
		banDuration:          cfg.BanDuration,
		whitelistIPs:         whitelist,
		maxWorkersPerIP:      cfg.MaxWorkersPerIP,    // RED-TEAM: Worker limit
		banPersistencePath:   cfg.BanPersistencePath, // RED-TEAM: Ban persistence
		connectionsByIP:      make(map[string]*ipState),
		bannedIPs:            make(map[string]time.Time),
		backend:              cfg.Backend, // H-2: Use provided backend for HA mode
		stopCh:               make(chan struct{}),
		logger:               logger,
	}

	// RED-TEAM: WorkersPerIP of 0 means disabled (default)
	// This avoids breaking hashrate rental scenarios where miners share dynamic IPs

	// RED-TEAM: Load persisted bans if configured
	if rl.banPersistencePath != "" {
		if err := rl.loadPersistedBans(); err != nil {
			logger.Warnw("Failed to load persisted bans (starting fresh)",
				"path", rl.banPersistencePath,
				"error", err)
		}

		// Audit #17: Start serialized ban persistence goroutine
		// This ensures concurrent ban events don't race on file writes
		rl.persistCh = make(chan struct{}, 1)
		go rl.persistLoop()
	}

	// Start cleanup goroutine
	go rl.cleanupLoop()

	// H-2: Log HA mode status
	if rl.backend != nil {
		logger.Infow("Rate limiter initialized with distributed backend (HA mode)")
	} else {
		logger.Debugw("Rate limiter initialized with local backend (single-node mode)")
	}

	return rl
}

// Stop gracefully shuts down the RateLimiter's background goroutines (cleanupLoop, persistLoop).
// Must be called during server shutdown to prevent goroutine leaks.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.stopCh)
		if rl.persistCh != nil {
			close(rl.persistCh)
		}
	})
}

// SetBackend sets the distributed state backend for HA mode
// This should be called before the rate limiter starts accepting connections
func (rl *RateLimiter) SetBackend(backend StateBackend) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.backend = backend
	if backend != nil {
		rl.logger.Infow("Distributed backend attached (HA mode enabled)")
	}
}

// HasDistributedBackend returns true if a distributed backend is configured
func (rl *RateLimiter) HasDistributedBackend() bool {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return rl.backend != nil
}

// AllowConnection checks if a new connection from the given IP should be allowed
func (rl *RateLimiter) AllowConnection(remoteAddr net.Addr) (allowed bool, reason string) {
	ip := extractIP(remoteAddr)
	if ip == "" {
		return false, "invalid address"
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Check whitelist first
	if rl.whitelistIPs[ip] {
		return true, ""
	}

	// Check if IP is banned
	if banExpiry, banned := rl.bannedIPs[ip]; banned {
		if time.Now().Before(banExpiry) {
			atomic.AddInt64(&rl.totalBlocked, 1)
			return false, "IP is banned"
		}
		// Ban expired, remove it
		delete(rl.bannedIPs, ip)
	}

	// Get or create IP state
	state, exists := rl.connectionsByIP[ip]
	if !exists {
		state = &ipState{
			minuteStart: time.Now(),
			secondStart: time.Now(),
		}
		rl.connectionsByIP[ip] = state
	}

	now := time.Now()

	// Reset minute counter if window passed
	if now.Sub(state.minuteStart) > time.Minute {
		state.connectionsMin = 0
		state.minuteStart = now
	}

	// Check connections per IP (0 = disabled for hashrate rental compatibility)
	if rl.maxConnectionsPerIP > 0 && state.connections >= rl.maxConnectionsPerIP {
		rl.recordViolation(ip, state, "max connections per IP exceeded")
		atomic.AddInt64(&rl.totalRateLimited, 1)
		return false, "too many connections from this IP"
	}

	// Check connections per minute (0 = disabled for hashrate rental compatibility)
	if rl.maxConnectionsPerMin > 0 && state.connectionsMin >= rl.maxConnectionsPerMin {
		rl.recordViolation(ip, state, "max connections per minute exceeded")
		atomic.AddInt64(&rl.totalRateLimited, 1)
		return false, "connection rate limit exceeded"
	}

	// Allow connection
	state.connections++
	state.connectionsMin++
	state.lastConnection = now
	atomic.AddInt64(&rl.globalConnections, 1)

	return true, ""
}

// ReleaseConnection decrements the connection count for an IP
func (rl *RateLimiter) ReleaseConnection(remoteAddr net.Addr) {
	ip := extractIP(remoteAddr)
	if ip == "" {
		return
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	if state, exists := rl.connectionsByIP[ip]; exists {
		state.connections--
		if state.connections < 0 {
			state.connections = 0
		}
	}
	atomic.AddInt64(&rl.globalConnections, -1)
}

// AllowShare checks if a share submission should be allowed
func (rl *RateLimiter) AllowShare(remoteAddr net.Addr) (allowed bool, reason string) {
	ip := extractIP(remoteAddr)
	if ip == "" {
		return false, "invalid address"
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Check whitelist first
	if rl.whitelistIPs[ip] {
		return true, ""
	}

	state, exists := rl.connectionsByIP[ip]
	if !exists {
		return true, "" // No state = no limit yet
	}

	now := time.Now()

	// Reset second counter if window passed
	if now.Sub(state.secondStart) > time.Second {
		state.shares = 0
		state.secondStart = now
	}

	// Check shares per second (0 = disabled for hashrate rental compatibility)
	if rl.maxSharesPerSecond > 0 && state.shares >= rl.maxSharesPerSecond {
		rl.recordViolation(ip, state, "max shares per second exceeded")
		atomic.AddInt64(&rl.totalRateLimited, 1)
		return false, "share rate limit exceeded"
	}

	state.shares++
	return true, ""
}

// BanIP manually bans an IP address
func (rl *RateLimiter) BanIP(ip string, duration time.Duration, reason string) {
	rl.mu.Lock()
	rl.bannedIPs[ip] = time.Now().Add(duration)
	atomic.AddInt64(&rl.totalBanned, 1)
	rl.mu.Unlock()

	rl.logger.Warnw("IP banned",
		"ip", ip,
		"duration", duration,
		"reason", reason,
	)

	// RED-TEAM: Persist ban to survive restarts (Audit #17: serialized via channel)
	if rl.persistCh != nil {
		select {
		case rl.persistCh <- struct{}{}:
		default:
			// Persist already queued, skip
		}
	}
}

// UnbanIP removes a ban for an IP address
func (rl *RateLimiter) UnbanIP(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	delete(rl.bannedIPs, ip)
	rl.logger.Infow("IP unbanned", "ip", ip)
}

// IsIPBanned checks if an IP is currently banned
func (rl *RateLimiter) IsIPBanned(ip string) bool {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	if banExpiry, banned := rl.bannedIPs[ip]; banned {
		return time.Now().Before(banExpiry)
	}
	return false
}

// GetStats returns rate limiter statistics
func (rl *RateLimiter) GetStats() RateLimiterStats {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	return RateLimiterStats{
		ActiveConnections: atomic.LoadInt64(&rl.globalConnections),
		UniqueIPs:         len(rl.connectionsByIP),
		BannedIPs:         len(rl.bannedIPs),
		TotalBlocked:      atomic.LoadInt64(&rl.totalBlocked),
		TotalBanned:       atomic.LoadInt64(&rl.totalBanned),
		TotalRateLimited:  atomic.LoadInt64(&rl.totalRateLimited),
	}
}

// RateLimiterStats contains rate limiter statistics
type RateLimiterStats struct {
	ActiveConnections int64
	UniqueIPs         int
	BannedIPs         int
	TotalBlocked      int64
	TotalBanned       int64
	TotalRateLimited  int64
}

// recordViolation records a rate limit violation and potentially bans the IP
// NOTE: Caller must hold rl.mu lock
func (rl *RateLimiter) recordViolation(ip string, state *ipState, reason string) {
	state.violations++

	rl.logger.Warnw("Rate limit violation",
		"ip", ip,
		"violations", state.violations,
		"threshold", rl.banThreshold,
		"reason", reason,
	)

	if state.violations >= rl.banThreshold {
		rl.bannedIPs[ip] = time.Now().Add(rl.banDuration)
		atomic.AddInt64(&rl.totalBanned, 1)
		rl.logger.Warnw("IP auto-banned due to violations",
			"ip", ip,
			"violations", state.violations,
			"duration", rl.banDuration,
		)

		// RED-TEAM: Persist auto-ban (Audit #17: serialized via channel, no goroutine spawn)
		if rl.persistCh != nil {
			select {
			case rl.persistCh <- struct{}{}:
			default:
				// Persist already queued, skip
			}
		}
	}
}

// cleanupLoop periodically cleans up stale entries.
// Exits when stopCh is closed (via Stop()) to prevent goroutine leaks.
func (rl *RateLimiter) cleanupLoop() {
	defer func() {
		if r := recover(); r != nil {
			rl.logger.Errorw("PANIC recovered in cleanupLoop", "panic", r)
		}
	}()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.cleanup()
		}
	}
}

// cleanup removes stale IP states and expired bans
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	staleThreshold := 10 * time.Minute

	// Clean up stale IP states
	for ip, state := range rl.connectionsByIP {
		if state.connections == 0 && now.Sub(state.lastConnection) > staleThreshold {
			delete(rl.connectionsByIP, ip)
		}
	}

	// Clean up expired bans
	for ip, expiry := range rl.bannedIPs {
		if now.After(expiry) {
			delete(rl.bannedIPs, ip)
		}
	}
}

// extractIP extracts the IP address from a net.Addr
func extractIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}

	switch v := addr.(type) {
	case *net.TCPAddr:
		// SECURITY: Normalize IPv4-mapped IPv6 addresses (e.g., ::ffff:192.168.1.1)
		// to their IPv4 form so the same host is always identified by one key.
		if ip4 := v.IP.To4(); ip4 != nil {
			return ip4.String()
		}
		return v.IP.String()
	case *net.UDPAddr:
		if ip4 := v.IP.To4(); ip4 != nil {
			return ip4.String()
		}
		return v.IP.String()
	default:
		// Try to parse as host:port
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return addr.String()
		}
		return host
	}
}

// extractIPString extracts an IP address from a string, stripping port if present
func extractIPString(addr string) string {
	if addr == "" {
		return ""
	}
	// Try to parse as host:port
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// No port, return as-is (might be just an IP)
		return addr
	}
	return host
}

// AddToWhitelist adds an IP to the whitelist
func (rl *RateLimiter) AddToWhitelist(ip string) {
	// SECURITY: Normalize IPv4-mapped IPv6 to plain IPv4 for consistent matching.
	if parsed := net.ParseIP(ip); parsed != nil {
		if ip4 := parsed.To4(); ip4 != nil {
			ip = ip4.String()
		}
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.whitelistIPs[ip] = true
}

// RemoveFromWhitelist removes an IP from the whitelist
func (rl *RateLimiter) RemoveFromWhitelist(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.whitelistIPs, ip)
}

// GetBannedIPs returns a list of currently banned IPs
func (rl *RateLimiter) GetBannedIPs() map[string]time.Time {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	result := make(map[string]time.Time)
	now := time.Now()
	for ip, expiry := range rl.bannedIPs {
		if now.Before(expiry) {
			result[ip] = expiry
		}
	}
	return result
}

// =============================================================================
// RED-TEAM: Ban Persistence (survives restarts)
// =============================================================================

// persistedBanEntry represents a single ban entry for JSON persistence
type persistedBanEntry struct {
	IP     string    `json:"ip"`
	Expiry time.Time `json:"expiry"`
	Reason string    `json:"reason,omitempty"`
}

// persistedBanFile represents the ban persistence file format
type persistedBanFile struct {
	Version   int                 `json:"version"`
	UpdatedAt time.Time           `json:"updatedAt"`
	Bans      []persistedBanEntry `json:"bans"`
}

// loadPersistedBans loads bans from the persistence file
func (rl *RateLimiter) loadPersistedBans() error {
	if rl.banPersistencePath == "" {
		return nil
	}

	data, err := os.ReadFile(rl.banPersistencePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No file yet, that's OK
		}
		return err
	}

	var file persistedBanFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	loadedCount := 0
	for _, entry := range file.Bans {
		if entry.Expiry.After(now) {
			rl.bannedIPs[entry.IP] = entry.Expiry
			loadedCount++
		}
	}

	if loadedCount > 0 {
		rl.logger.Infow("Loaded persisted bans",
			"count", loadedCount,
			"path", rl.banPersistencePath)
	}

	return nil
}

// persistBans saves current bans to the persistence file
func (rl *RateLimiter) persistBans() error {
	if rl.banPersistencePath == "" {
		return nil
	}

	rl.mu.RLock()
	now := time.Now()
	var bans []persistedBanEntry
	for ip, expiry := range rl.bannedIPs {
		if expiry.After(now) {
			bans = append(bans, persistedBanEntry{
				IP:     ip,
				Expiry: expiry,
			})
		}
	}
	rl.mu.RUnlock()

	file := persistedBanFile{
		Version:   1,
		UpdatedAt: now,
		Bans:      bans,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically via temp file
	tmpPath := rl.banPersistencePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, rl.banPersistencePath)
}

// persistLoop reads from persistCh and serializes ban persistence writes.
// Audit #17: Prevents concurrent goroutines from racing on file I/O.
func (rl *RateLimiter) persistLoop() {
	for {
		select {
		case <-rl.stopCh:
			return
		case _, ok := <-rl.persistCh:
			if !ok {
				return
			}
			if err := rl.persistBans(); err != nil {
				rl.logger.Warnw("Failed to persist bans",
					"error", err)
			}
		}
	}
}

// =============================================================================
// RED-TEAM: Worker Identity Churn Protection
// =============================================================================

// AllowWorkerRegistration checks if a new worker can be registered from this IP
// Returns (allowed, reason) where reason explains denial if not allowed
func (rl *RateLimiter) AllowWorkerRegistration(remoteAddr net.Addr, workerName string) (bool, string) {
	ip := extractIP(remoteAddr)
	if ip == "" {
		return false, "invalid address"
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Check whitelist first
	if rl.whitelistIPs[ip] {
		return true, ""
	}

	// Worker limit of 0 means disabled
	if rl.maxWorkersPerIP == 0 {
		return true, ""
	}

	state, exists := rl.connectionsByIP[ip]
	if !exists {
		state = &ipState{
			minuteStart: time.Now(),
			secondStart: time.Now(),
			workers:     make(map[string]bool),
		}
		rl.connectionsByIP[ip] = state
	}

	// Initialize workers map if nil
	if state.workers == nil {
		state.workers = make(map[string]bool)
	}

	// Check if this worker already exists (OK to re-register)
	if state.workers[workerName] {
		return true, ""
	}

	// Check if we've hit the worker limit
	if state.workersCount >= rl.maxWorkersPerIP {
		rl.recordViolation(ip, state, "max workers per IP exceeded")
		return false, "too many workers from this IP"
	}

	// Register the new worker
	state.workers[workerName] = true
	state.workersCount++

	return true, ""
}

// GetWorkerCount returns the number of unique workers registered from an IP
func (rl *RateLimiter) GetWorkerCount(remoteAddr net.Addr) int {
	ip := extractIP(remoteAddr)
	if ip == "" {
		return 0
	}

	rl.mu.RLock()
	defer rl.mu.RUnlock()

	if state, exists := rl.connectionsByIP[ip]; exists {
		return state.workersCount
	}
	return 0
}
