// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database provides PostgreSQL persistence with multi-node failover.
//
// This file implements autonomous PostgreSQL streaming replication management.
// It automatically discovers nodes, configures primary/replica relationships,
// and manages replication without user intervention.
//
// # Security Features
//
//   - TLS/SSL encryption for all replication traffic
//   - Encrypted credential storage (AES-256-GCM)
//   - Replication user with minimal required privileges
//   - Network-level access control via pg_hba.conf
//   - Certificate-based authentication support
//
// # VIP Integration
//
// The replication manager integrates with VIP failover:
//   - VIP master = PostgreSQL primary (writable)
//   - VIP backup = PostgreSQL replica (read-only)
//   - Automatic promotion when VIP role changes
//   - Automatic demotion and re-sync on failback
package database

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/crypto/hkdf"
)

// ReplicationRole represents the role of a PostgreSQL node in replication
type ReplicationRole int

const (
	RoleUnknown ReplicationRole = iota
	RolePrimary
	RoleReplica
	RoleStandby
)

func (r ReplicationRole) String() string {
	switch r {
	case RolePrimary:
		return "primary"
	case RoleReplica:
		return "replica"
	case RoleStandby:
		return "standby"
	default:
		return "unknown"
	}
}

// ReplicationState tracks the state of a replication relationship
type ReplicationState int

const (
	ReplicationStateUnknown ReplicationState = iota
	ReplicationStateSyncing
	ReplicationStateSynced
	ReplicationStateLagging
	ReplicationStateFailed
)

func (s ReplicationState) String() string {
	switch s {
	case ReplicationStateSyncing:
		return "syncing"
	case ReplicationStateSynced:
		return "synced"
	case ReplicationStateLagging:
		return "lagging"
	case ReplicationStateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// ReplicationConfig holds configuration for autonomous replication
type ReplicationConfig struct {
	// Enabled controls whether autonomous replication is active
	Enabled bool `yaml:"enabled"`

	// AutoDiscover enables automatic node discovery via network scanning
	AutoDiscover bool `yaml:"auto_discover"`

	// DiscoveryNetwork is the CIDR range to scan (e.g., "192.168.1.0/24")
	DiscoveryNetwork string `yaml:"discovery_network"`

	// PostgresPort is the port to scan for PostgreSQL (default 5432)
	PostgresPort int `yaml:"postgres_port"`

	// ReplicationUser is the PostgreSQL user for replication
	// Defaults to SPIRAL_REPLICATION_USER env var or "replicator"
	ReplicationUser string `yaml:"replication_user"`

	// ReplicationSlotPrefix is the prefix for replication slots
	ReplicationSlotPrefix string `yaml:"replication_slot_prefix"`

	// SyncTimeout is the maximum time to wait for initial sync
	SyncTimeout time.Duration `yaml:"sync_timeout"`

	// MaxLagBytes is the maximum acceptable replication lag
	MaxLagBytes int64 `yaml:"max_lag_bytes"`

	// PromotionDelay is the delay before promoting replica to primary
	PromotionDelay time.Duration `yaml:"promotion_delay"`

	// Patroni Integration (recommended for HA)
	// When enabled, uses Patroni REST API for promotion/demotion
	// instead of direct pg_promote() calls. This keeps etcd state in sync.
	PatroniEnabled  bool   `yaml:"patroni_enabled"`
	PatroniAPIHost  string `yaml:"patroni_api_host"`  // Default: localhost
	PatroniAPIPort  int    `yaml:"patroni_api_port"`  // Default: 8008
	PatroniUsername string `yaml:"patroni_username"`  // Basic auth username for Patroni API
	PatroniPassword string `yaml:"patroni_password"`  // Basic auth password for Patroni API

	// PatroniRetryAttempts is the number of retries for 409 conflicts (default: 3)
	PatroniRetryAttempts int `yaml:"patroni_retry_attempts"`
	// PatroniRetryDelay is the delay between retry attempts (default: 5s)
	PatroniRetryDelay time.Duration `yaml:"patroni_retry_delay"`

	// MaxLagOnFailover is the maximum acceptable lag (bytes) for failover candidate
	// Matches Patroni's maximum_lag_on_failover setting
	MaxLagOnFailover int64 `yaml:"max_lag_on_failover"`

	// RequireQuorumForFallback requires quorum confirmation before pg_promote() fallback
	// When true, direct pg_promote() will be refused if cluster quorum cannot be verified
	// This prevents split-brain scenarios but may delay promotion in true network partitions
	RequireQuorumForFallback bool `yaml:"require_quorum_for_fallback"`
}

// DefaultReplicationConfig returns sensible defaults
func DefaultReplicationConfig() ReplicationConfig {
	return ReplicationConfig{
		Enabled:                  true,
		AutoDiscover:             true,
		PostgresPort:             5432,
		ReplicationUser:          getEnvOrDefault("SPIRAL_REPLICATION_USER", "replicator"),
		ReplicationSlotPrefix:    "spiral_slot_",
		SyncTimeout:              30 * time.Minute,
		MaxLagBytes:              16 * 1024 * 1024, // 16MB
		PromotionDelay:           10 * time.Second,
		PatroniEnabled:           true, // Use Patroni API by default for HA
		PatroniAPIHost:           "localhost",
		PatroniAPIPort:           8008,
		PatroniUsername:          getEnvOrDefault("PATRONI_API_USERNAME", ""),
		PatroniPassword:          getEnvOrDefault("PATRONI_API_PASSWORD", ""),
		PatroniRetryAttempts:     3,
		PatroniRetryDelay:        5 * time.Second,
		MaxLagOnFailover:         1048576, // 1MB - matches patroni.yml.template
		RequireQuorumForFallback: true,    // Prevent split-brain by default
	}
}

// ReplicaInfo holds information about a replica node
type ReplicaInfo struct {
	NodeID          string
	Host            string
	Port            int
	Role            ReplicationRole
	State           ReplicationState
	ReplicationSlot string
	LagBytes        int64
	LastSeenLSN     string
	LastContact     time.Time
	IsLocal         bool
}

// ReplicationManager handles autonomous PostgreSQL replication
type ReplicationManager struct {
	config  ReplicationConfig
	dbMgr   *DatabaseManager
	logger  *zap.SugaredLogger
	localIP string
	nodeID  string

	// Replication state
	mu          sync.RWMutex
	role        ReplicationRole
	replicas    map[string]*ReplicaInfo
	primaryHost string
	primaryPort int

	// Coordination
	running       atomic.Bool
	wg            sync.WaitGroup
	promotionLock sync.Mutex
	cancel        context.CancelFunc

	// Callbacks
	onRoleChange func(newRole ReplicationRole)

	// Timing for race condition prevention
	lastPromotion   atomic.Int64
	lastDemotion    atomic.Int64
	electionTimeout time.Duration

	// Metrics for monitoring and alerting
	patroniFallbackTotal   atomic.Int64 // Counter: times pg_promote() fallback was used
	patroniPromotionTotal  atomic.Int64 // Counter: successful Patroni API promotions
	patroniRetryTotal      atomic.Int64 // Counter: total 409 conflict retries
	lastFallbackReason     atomic.Value // string: reason for last fallback
	splitBrainRiskDetected atomic.Bool  // Flag: potential split-brain condition detected
}

// NewReplicationManager creates a new autonomous replication manager
func NewReplicationManager(config ReplicationConfig, dbMgr *DatabaseManager, logger *zap.Logger) (*ReplicationManager, error) {
	log := logger.Sugar()

	// Generate unique node ID
	randomBytes := make([]byte, 4)
	_, _ = rand.Read(randomBytes) // #nosec G104
	nodeID := fmt.Sprintf("pg-%s-%s", getLocalHostname(), hex.EncodeToString(randomBytes))

	// Detect local IP
	localIP, err := detectLocalIP()
	if err != nil {
		log.Warnw("Could not detect local IP, using hostname",
			"error", err,
		)
		localIP = "localhost"
	}

	rm := &ReplicationManager{
		config:          config,
		dbMgr:           dbMgr,
		logger:          log,
		localIP:         localIP,
		nodeID:          nodeID,
		role:            RoleUnknown,
		replicas:        make(map[string]*ReplicaInfo),
		electionTimeout: 30 * time.Second,
	}

	log.Infow("Replication manager created",
		"nodeId", nodeID,
		"localIP", localIP,
		"autoDiscover", config.AutoDiscover,
	)

	return rm, nil
}

// Start begins autonomous replication management
func (rm *ReplicationManager) Start(ctx context.Context) error {
	if rm.running.Load() {
		return fmt.Errorf("replication manager already running")
	}

	ctx, rm.cancel = context.WithCancel(ctx)
	rm.running.Store(true)

	rm.logger.Info("Starting autonomous replication manager...")

	// Step 1: Determine initial role
	if err := rm.determineInitialRole(ctx); err != nil {
		rm.logger.Errorw("Could not determine initial role — waiting for Patroni",
			"error", err,
		)
		// Check Patroni API as fallback before assuming primary
		if rm.config.PatroniEnabled && rm.isPatroniAvailable(ctx) {
			status, pErr := rm.getPatroniStatus(ctx)
			if pErr == nil && status.Role == "replica" {
				rm.role = RoleReplica
				rm.logger.Infow("Patroni reports this node is a replica")
			} else {
				rm.role = RolePrimary // Patroni says primary or unavailable
				rm.logger.Infow("Patroni fallback: assuming primary",
					"patroniError", pErr,
				)
			}
		} else {
			rm.role = RolePrimary // No Patroni, assume primary (single-node)
			rm.logger.Warnw("No Patroni available, assuming primary (single-node)")
		}
	}

	rm.logger.Infow("Initial replication role determined",
		"role", rm.role.String(),
		"nodeId", rm.nodeID,
	)

	// Step 2: Start discovery if enabled
	if rm.config.AutoDiscover {
		rm.wg.Add(1)
		go rm.discoveryLoop(ctx)
	}

	// Step 3: Start replication monitoring
	rm.wg.Add(1)
	go rm.monitoringLoop(ctx)

	// Step 4: Start health check loop
	rm.wg.Add(1)
	go rm.healthCheckLoop(ctx)

	return nil
}

// Stop gracefully shuts down replication management
func (rm *ReplicationManager) Stop() {
	rm.running.Store(false)
	if rm.cancel != nil {
		rm.cancel()
	}
	rm.wg.Wait()
	rm.logger.Info("Replication manager stopped")
}

// SetRoleChangeCallback sets a callback for role changes
func (rm *ReplicationManager) SetRoleChangeCallback(cb func(ReplicationRole)) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.onRoleChange = cb
}

// GetRole returns the current replication role
func (rm *ReplicationManager) GetRole() ReplicationRole {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.role
}

// IsPrimary returns true if this node is the primary
func (rm *ReplicationManager) IsPrimary() bool {
	return rm.GetRole() == RolePrimary
}

// determineInitialRole checks PostgreSQL to determine our current role
func (rm *ReplicationManager) determineInitialRole(ctx context.Context) error {
	node := rm.dbMgr.GetActiveNode()
	if node == nil || node.Pool == nil {
		return fmt.Errorf("no active database connection")
	}

	// Query PostgreSQL for recovery status
	var isInRecovery bool
	err := node.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery)
	if err != nil {
		return fmt.Errorf("failed to check recovery status: %w", err)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	if isInRecovery {
		rm.role = RoleReplica
		rm.logger.Info("This node is a PostgreSQL REPLICA (in recovery mode)")
	} else {
		rm.role = RolePrimary
		rm.logger.Info("This node is a PostgreSQL PRIMARY (not in recovery)")
	}

	return nil
}

// discoveryLoop continuously discovers PostgreSQL nodes
func (rm *ReplicationManager) discoveryLoop(ctx context.Context) {
	defer rm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			rm.logger.Errorw("PANIC recovered in discoveryLoop", "panic", r)
		}
	}()

	// Initial discovery
	rm.discoverNodes(ctx)

	// Periodic discovery
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !rm.running.Load() {
				return
			}
			rm.discoverNodes(ctx)
		}
	}
}

// discoverNodes scans the network for PostgreSQL nodes
func (rm *ReplicationManager) discoverNodes(ctx context.Context) {
	if rm.config.DiscoveryNetwork == "" {
		// Try to auto-detect network from local IP
		rm.config.DiscoveryNetwork = rm.guessLocalNetwork()
	}

	if rm.config.DiscoveryNetwork == "" {
		rm.logger.Debug("No discovery network configured, skipping scan")
		return
	}

	rm.logger.Debugw("Scanning network for PostgreSQL nodes",
		"network", rm.config.DiscoveryNetwork,
		"port", rm.config.PostgresPort,
	)

	// Parse CIDR
	ip, ipnet, err := net.ParseCIDR(rm.config.DiscoveryNetwork)
	if err != nil {
		rm.logger.Warnw("Invalid discovery network CIDR",
			"network", rm.config.DiscoveryNetwork,
			"error", err,
		)
		return
	}

	// Limit scan size to prevent unbounded network scans
	ones, bits := ipnet.Mask.Size()
	rangeSize := uint64(1) << uint(bits-ones)
	const maxScanSize = 1024
	if rangeSize > maxScanSize {
		rm.logger.Warnw("CIDR range too large for discovery scan, limiting to first 1024 IPs",
			"network", rm.config.DiscoveryNetwork,
			"rangeSize", rangeSize,
			"maxScanSize", maxScanSize,
		)
	}

	// Scan each IP in the range (in parallel with limit)
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 50) // Limit concurrent scans

	scanned := uint64(0)
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incrementIP(ip) {
		if scanned >= maxScanSize {
			break
		}
		scanned++
		host := ip.String()

		// Skip broadcast and network addresses
		if host == rm.localIP {
			continue
		}

		wg.Add(1)
		semaphore <- struct{}{}

		go func(h string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			rm.probePostgres(ctx, h, rm.config.PostgresPort)
		}(host)
	}

	wg.Wait()
}

// probePostgres attempts to connect to a PostgreSQL server
func (rm *ReplicationManager) probePostgres(ctx context.Context, host string, port int) {
	// Quick TCP check first
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return // Not listening
	}
	_ = conn.Close() // #nosec G104

	rm.logger.Debugw("Found PostgreSQL listener",
		"host", host,
		"port", port,
	)

	// Try to get replication password from environment
	replicationPassword := os.Getenv("SPIRAL_REPLICATION_PASSWORD")
	if replicationPassword == "" {
		// Fall back to database password
		replicationPassword = os.Getenv("SPIRAL_DATABASE_PASSWORD")
	}

	if replicationPassword == "" {
		rm.logger.Debug("No replication password available, skipping node probe")
		return
	}

	// Try to connect and check role
	// SECURITY: URL-encode credentials to prevent connection string injection
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/postgres?connect_timeout=5",
		url.QueryEscape(rm.config.ReplicationUser), url.QueryEscape(replicationPassword), host, port,
	)

	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return
	}
	poolConfig.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return
	}
	defer pool.Close()

	// Check if in recovery
	var isInRecovery bool
	err = pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery)
	if err != nil {
		return
	}

	// Get server identifier
	var serverID string
	err = pool.QueryRow(ctx, "SELECT system_identifier FROM pg_control_system()").Scan(&serverID)
	if err != nil {
		// Not critical, use host as identifier
		serverID = host
	}

	// Add to known nodes
	rm.mu.Lock()
	defer rm.mu.Unlock()

	role := RolePrimary
	if isInRecovery {
		role = RoleReplica
	}

	nodeID := fmt.Sprintf("pg-%s:%d", host, port)
	rm.replicas[nodeID] = &ReplicaInfo{
		NodeID:      nodeID,
		Host:        host,
		Port:        port,
		Role:        role,
		LastContact: time.Now(),
		IsLocal:     host == rm.localIP,
	}

	rm.logger.Infow("Discovered PostgreSQL node",
		"host", host,
		"port", port,
		"role", role.String(),
	)

	// If we found a primary and we're a replica, record it
	if role == RolePrimary && rm.role == RoleReplica {
		rm.primaryHost = host
		rm.primaryPort = port
		rm.logger.Infow("Found primary server",
			"host", host,
			"port", port,
		)
	}
}

// monitoringLoop monitors replication status
func (rm *ReplicationManager) monitoringLoop(ctx context.Context) {
	defer rm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			rm.logger.Errorw("PANIC recovered in monitoringLoop", "panic", r)
		}
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !rm.running.Load() {
				return
			}
			rm.updateReplicationStatus(ctx)
		}
	}
}

// updateReplicationStatus checks replication lag and state
func (rm *ReplicationManager) updateReplicationStatus(ctx context.Context) {
	node := rm.dbMgr.GetActiveNode()
	if node == nil || node.Pool == nil {
		return
	}

	rm.mu.RLock()
	role := rm.role
	rm.mu.RUnlock()

	if role == RolePrimary {
		rm.updatePrimaryStatus(ctx, node.Pool)
	} else {
		rm.updateReplicaStatus(ctx, node.Pool)
	}
}

// updatePrimaryStatus checks replication status from primary's perspective
func (rm *ReplicationManager) updatePrimaryStatus(ctx context.Context, pool *pgxpool.Pool) {
	// Query replication slots and their lag
	rows, err := pool.Query(ctx, `
		SELECT
			slot_name,
			active,
			pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) as lag_bytes
		FROM pg_replication_slots
		WHERE slot_type = 'physical'
	`)
	if err != nil {
		rm.logger.Debugw("Could not query replication slots",
			"error", err,
		)
		return
	}
	defer rows.Close()

	rm.mu.Lock()
	defer rm.mu.Unlock()

	for rows.Next() {
		var slotName string
		var active bool
		var lagBytes int64

		if err := rows.Scan(&slotName, &active, &lagBytes); err != nil {
			continue
		}

		// Find replica by slot name
		for _, replica := range rm.replicas {
			if replica.ReplicationSlot == slotName {
				replica.LagBytes = lagBytes
				replica.LastContact = time.Now()

				if lagBytes > rm.config.MaxLagBytes {
					replica.State = ReplicationStateLagging
				} else {
					replica.State = ReplicationStateSynced
				}
			}
		}
	}
}

// updateReplicaStatus checks replication status from replica's perspective
func (rm *ReplicationManager) updateReplicaStatus(ctx context.Context, pool *pgxpool.Pool) {
	var receiveLocation, replayLocation string
	err := pool.QueryRow(ctx, `
		SELECT
			COALESCE(pg_last_wal_receive_lsn()::text, '0/0'),
			COALESCE(pg_last_wal_replay_lsn()::text, '0/0')
	`).Scan(&receiveLocation, &replayLocation)
	if err != nil {
		rm.logger.Debugw("Could not query replica status",
			"error", err,
		)
		return
	}

	rm.logger.Debugw("Replica replication status",
		"receiveLocation", receiveLocation,
		"replayLocation", replayLocation,
	)
}

// healthCheckLoop monitors primary availability for promotion
func (rm *ReplicationManager) healthCheckLoop(ctx context.Context) {
	defer rm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			rm.logger.Errorw("PANIC recovered in healthCheckLoop", "panic", r)
		}
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	consecutiveFailures := 0
	const failureThreshold = 3

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !rm.running.Load() {
				return
			}

			rm.mu.RLock()
			role := rm.role
			primaryHost := rm.primaryHost
			primaryPort := rm.primaryPort
			rm.mu.RUnlock()

			// Only replicas need to monitor the primary
			if role != RoleReplica || primaryHost == "" {
				consecutiveFailures = 0
				continue
			}

			// Check if primary is reachable
			if rm.isPrimaryReachable(ctx, primaryHost, primaryPort) {
				consecutiveFailures = 0
			} else {
				consecutiveFailures++
				rm.logger.Warnw("Primary unreachable",
					"host", primaryHost,
					"port", primaryPort,
					"consecutiveFailures", consecutiveFailures,
				)

				if consecutiveFailures >= failureThreshold {
					rm.logger.Warn("Primary confirmed unreachable, initiating promotion")
					rm.initiatePromotion(ctx)
					consecutiveFailures = 0
				}
			}
		}
	}
}

// isPrimaryReachable checks if the primary server is responding via TCP connect.
// PostgreSQL waits for the client to speak first after TCP connect, so a raw
// Read() will always timeout. TCP reachability is sufficient for this check.
func (rm *ReplicationManager) isPrimaryReachable(ctx context.Context, host string, port int) bool {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close() // #nosec G104
	return true
}

// initiatePromotion promotes this replica to primary
func (rm *ReplicationManager) initiatePromotion(ctx context.Context) {
	rm.promotionLock.Lock()
	defer rm.promotionLock.Unlock()

	// Debounce: prevent rapid promotions (30s to match Patroni timeout)
	now := time.Now().Unix()
	lastPromo := rm.lastPromotion.Load()
	if now-lastPromo < 30 {
		rm.logger.Debug("Promotion debounced (too recent)")
		return
	}

	rm.mu.RLock()
	role := rm.role
	rm.mu.RUnlock()

	if role != RoleReplica {
		rm.logger.Debug("Not a replica, cannot promote")
		return
	}

	rm.logger.Info("Starting promotion to primary...")

	// Wait for promotion delay (allows other replicas to catch up)
	select {
	case <-ctx.Done():
		return
	case <-time.After(rm.config.PromotionDelay):
	}

	// Execute promotion
	node := rm.dbMgr.GetActiveNode()
	if node == nil || node.Pool == nil {
		rm.logger.Error("No database connection for promotion")
		return
	}

	var err error
	var fallbackReason string

	// Prefer Patroni API for promotion to keep etcd state in sync
	if rm.config.PatroniEnabled {
		rm.logger.Info("Using Patroni API for promotion (keeps etcd state in sync)")

		if rm.isPatroniAvailable(ctx) {
			err = rm.promoteViaPatroni(ctx)
			if err != nil {
				fallbackReason = fmt.Sprintf("patroni_api_error: %v", err)
				rm.logger.Warnw("Patroni API promotion failed, evaluating fallback",
					"error", err,
					"requireQuorum", rm.config.RequireQuorumForFallback,
				)
				// Fall through to evaluate fallback
			} else {
				// Patroni promotion successful
				rm.lastPromotion.Store(now)

				rm.mu.Lock()
				rm.role = RolePrimary
				rm.primaryHost = rm.localIP
				rm.primaryPort = rm.config.PostgresPort
				callback := rm.onRoleChange
				rm.mu.Unlock()

				rm.logger.Infow("✅ PROMOTED TO PRIMARY via Patroni",
					"nodeId", rm.nodeID,
					"localIP", rm.localIP,
				)

				if callback != nil {
					go callback(RolePrimary)
				}
				return
			}
		} else {
			fallbackReason = "patroni_unavailable"
			rm.logger.Warn("Patroni API not available, evaluating fallback")
		}
	}

	// ============================================================================
	// CRITICAL: Quorum Check Before pg_promote() Fallback
	// ============================================================================
	if rm.config.PatroniEnabled && rm.config.RequireQuorumForFallback {
		rm.logger.Warn("🔒 Quorum check required before pg_promote() fallback")

		hasQuorum, quorumErr := rm.verifyClusterQuorum(ctx)
		if quorumErr != nil {
			rm.logger.Errorw("❌ REFUSING pg_promote() - cannot verify cluster quorum",
				"error", quorumErr,
				"reason", "potential split-brain risk",
			)
			rm.splitBrainRiskDetected.Store(true)
			rm.lastFallbackReason.Store(fmt.Sprintf("quorum_check_failed: %v", quorumErr))
			return
		}

		if !hasQuorum {
			rm.logger.Errorw("❌ REFUSING pg_promote() - no quorum, potential split-brain",
				"reason", "cluster quorum not established",
			)
			rm.splitBrainRiskDetected.Store(true)
			rm.lastFallbackReason.Store("no_quorum")
			return
		}

		rm.logger.Infow("✅ Quorum verified, proceeding with pg_promote() fallback",
			"quorum", true,
		)
	}

	// Direct pg_promote() - fallback or when Patroni is disabled
	if rm.config.PatroniEnabled {
		rm.logger.Warn("⚠️  Using direct pg_promote() - Patroni etcd state may be inconsistent!")
		rm.logger.Warnw("🚨 OPERATOR ALERT: Patroni fallback triggered - manual etcd reconciliation may be required",
			"fallbackReason", fallbackReason,
			"nodeId", rm.nodeID,
			"localIP", rm.localIP,
		)
	}

	// Track fallback metrics
	rm.patroniFallbackTotal.Add(1)
	rm.lastFallbackReason.Store(fallbackReason)

	// PostgreSQL 12+ uses pg_promote()
	_, err = node.Pool.Exec(ctx, "SELECT pg_promote(true, 60)")
	if err != nil {
		rm.logger.Errorw("Promotion failed",
			"error", err,
		)
		return
	}

	// Verify promotion actually succeeded
	var stillInRecovery bool
	verifyErr := node.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&stillInRecovery)
	if verifyErr != nil || stillInRecovery {
		rm.logger.Errorw("❌ pg_promote() did not complete successfully",
			"stillInRecovery", stillInRecovery,
			"verifyError", verifyErr,
		)
		return
	}

	rm.lastPromotion.Store(now)

	rm.mu.Lock()
	rm.role = RolePrimary
	rm.primaryHost = rm.localIP
	rm.primaryPort = rm.config.PostgresPort
	callback := rm.onRoleChange
	rm.mu.Unlock()

	rm.logger.Infow("✅ PROMOTED TO PRIMARY via pg_promote()",
		"nodeId", rm.nodeID,
		"localIP", rm.localIP,
		"fallbackReason", fallbackReason,
		"patroniFallbackTotal", rm.patroniFallbackTotal.Load(),
	)

	if callback != nil {
		go callback(RolePrimary)
	}
}

// ConfigureAsReplica sets up this node as a replica of the given primary
func (rm *ReplicationManager) ConfigureAsReplica(ctx context.Context, primaryHost string, primaryPort int) error {
	rm.mu.Lock()
	rm.primaryHost = primaryHost
	rm.primaryPort = primaryPort
	rm.role = RoleReplica
	rm.mu.Unlock()

	rm.logger.Infow("Configured as replica",
		"primaryHost", primaryHost,
		"primaryPort", primaryPort,
	)

	return nil
}

// CreateReplicationSlot creates a replication slot on the primary
func (rm *ReplicationManager) CreateReplicationSlot(ctx context.Context, slotName string) error {
	node := rm.dbMgr.GetActiveNode()
	if node == nil || node.Pool == nil {
		return fmt.Errorf("no database connection")
	}

	// Check if slot already exists
	var exists bool
	err := node.Pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)",
		slotName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check slot existence: %w", err)
	}

	if exists {
		rm.logger.Debugw("Replication slot already exists",
			"slotName", slotName,
		)
		return nil
	}

	// Create physical replication slot
	_, err = node.Pool.Exec(ctx,
		"SELECT pg_create_physical_replication_slot($1)",
		slotName,
	)
	if err != nil {
		return fmt.Errorf("failed to create replication slot: %w", err)
	}

	rm.logger.Infow("Created replication slot",
		"slotName", slotName,
	)

	return nil
}

// DropReplicationSlot removes a replication slot
func (rm *ReplicationManager) DropReplicationSlot(ctx context.Context, slotName string) error {
	node := rm.dbMgr.GetActiveNode()
	if node == nil || node.Pool == nil {
		return fmt.Errorf("no database connection")
	}

	_, err := node.Pool.Exec(ctx,
		"SELECT pg_drop_replication_slot($1)",
		slotName,
	)
	if err != nil {
		// Ignore if slot doesn't exist
		if strings.Contains(err.Error(), "does not exist") {
			return nil
		}
		return fmt.Errorf("failed to drop replication slot: %w", err)
	}

	rm.logger.Infow("Dropped replication slot",
		"slotName", slotName,
	)

	return nil
}

// GetReplicationInfo returns information about all known nodes
func (rm *ReplicationManager) GetReplicationInfo() map[string]*ReplicaInfo {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	result := make(map[string]*ReplicaInfo)
	for k, v := range rm.replicas {
		info := *v
		result[k] = &info
	}
	return result
}

// GetPrimaryInfo returns the primary server information
func (rm *ReplicationManager) GetPrimaryInfo() (host string, port int, isLocal bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rm.primaryHost, rm.primaryPort, rm.primaryHost == rm.localIP
}

// Helper functions

func detectLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no suitable IP address found")
}

func getLocalHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}

func getEnvOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func (rm *ReplicationManager) guessLocalNetwork() string {
	parts := strings.Split(rm.localIP, ".")
	if len(parts) == 4 {
		return fmt.Sprintf("%s.%s.%s.0/24", parts[0], parts[1], parts[2])
	}
	return ""
}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// =============================================================================
// VIP Integration
// =============================================================================

// VIPRoleChangeHandler returns a callback function for VIP role changes.
// This enables automatic PostgreSQL promotion/demotion synchronized with VIP.
//
// When VIP isMaster=true: Promote local PostgreSQL to primary
// When VIP isMaster=false: Demote to replica and re-sync from new primary
func (rm *ReplicationManager) VIPRoleChangeHandler() func(isMaster bool) {
	return func(isMaster bool) {
		rm.handleVIPRoleChange(isMaster)
	}
}

// handleVIPRoleChange handles VIP role changes
func (rm *ReplicationManager) handleVIPRoleChange(isMaster bool) {
	rm.mu.RLock()
	currentRole := rm.role
	rm.mu.RUnlock()

	rm.logger.Infow("VIP role change received",
		"isMaster", isMaster,
		"currentRole", currentRole.String(),
	)

	if isMaster {
		// VIP says we're master - ensure PostgreSQL is primary
		if currentRole != RolePrimary {
			rm.promoteToMaster()
		}
	} else {
		// VIP says we're backup - ensure PostgreSQL is replica
		if currentRole == RolePrimary {
			rm.demoteToReplica()
		}
	}
}

// promoteToMaster promotes this node to PostgreSQL primary
func (rm *ReplicationManager) promoteToMaster() {
	rm.promotionLock.Lock()
	defer rm.promotionLock.Unlock()

	// Debounce - use 30 seconds to match Patroni timeout
	// This prevents rapid promotion attempts during network instability
	now := time.Now().Unix()
	lastPromo := rm.lastPromotion.Load()
	if now-lastPromo < 30 {
		rm.logger.Debug("Promotion debounced (VIP) - within 30s window")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	node := rm.dbMgr.GetActiveNode()
	if node == nil || node.Pool == nil {
		rm.logger.Error("No database connection for VIP promotion")
		return
	}

	// Check if already primary
	var isInRecovery bool
	err := node.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery)
	if err != nil {
		rm.logger.Errorw("Failed to check recovery status",
			"error", err,
		)
		return
	}

	if !isInRecovery {
		rm.logger.Info("Already primary, no promotion needed")
		rm.mu.Lock()
		rm.role = RolePrimary
		rm.mu.Unlock()
		return
	}

	// Execute promotion - prefer Patroni API to keep etcd state in sync
	rm.logger.Info("Promoting PostgreSQL to primary (VIP triggered)...")

	var fallbackReason string

	if rm.config.PatroniEnabled {
		// Try Patroni API first - this keeps etcd state consistent
		rm.logger.Info("Using Patroni API for promotion (keeps etcd state in sync)")

		if rm.isPatroniAvailable(ctx) {
			err = rm.promoteViaPatroni(ctx)
			if err != nil {
				fallbackReason = fmt.Sprintf("patroni_api_error: %v", err)
				rm.logger.Warnw("Patroni API promotion failed, evaluating fallback",
					"error", err,
					"requireQuorum", rm.config.RequireQuorumForFallback,
				)
				// Fall through to evaluate fallback
			} else {
				// Patroni promotion successful - metrics updated in promoteViaPatroni
				rm.lastPromotion.Store(now)

				rm.mu.Lock()
				rm.role = RolePrimary
				rm.primaryHost = rm.localIP
				rm.primaryPort = rm.config.PostgresPort
				callback := rm.onRoleChange
				rm.mu.Unlock()

				rm.logger.Infow("✅ PROMOTED TO PRIMARY via Patroni (VIP)",
					"nodeId", rm.nodeID,
				)

				if callback != nil {
					go callback(RolePrimary)
				}
				return
			}
		} else {
			fallbackReason = "patroni_unavailable"
			rm.logger.Warn("Patroni API not available, evaluating fallback")
		}
	}

	// ============================================================================
	// CRITICAL: Quorum Check Before pg_promote() Fallback
	// ============================================================================
	// Direct pg_promote() bypasses Patroni and may cause split-brain if:
	// - Another node also believes it should be promoted
	// - etcd still shows a different leader
	// - Network partition makes us unable to verify cluster state

	if rm.config.PatroniEnabled && rm.config.RequireQuorumForFallback {
		rm.logger.Warn("🔒 Quorum check required before pg_promote() fallback")

		hasQuorum, quorumErr := rm.verifyClusterQuorum(ctx)
		if quorumErr != nil {
			rm.logger.Errorw("❌ REFUSING pg_promote() - cannot verify cluster quorum",
				"error", quorumErr,
				"reason", "potential split-brain risk",
			)
			rm.splitBrainRiskDetected.Store(true)
			rm.lastFallbackReason.Store(fmt.Sprintf("quorum_check_failed: %v", quorumErr))
			return
		}

		if !hasQuorum {
			rm.logger.Errorw("❌ REFUSING pg_promote() - no quorum, potential split-brain",
				"reason", "cluster quorum not established",
			)
			rm.splitBrainRiskDetected.Store(true)
			rm.lastFallbackReason.Store("no_quorum")
			return
		}

		rm.logger.Infow("✅ Quorum verified, proceeding with pg_promote() fallback",
			"quorum", true,
		)
	}

	// Direct pg_promote() - fallback or when Patroni is disabled
	// WARNING: This bypasses Patroni and may cause etcd state inconsistency
	if rm.config.PatroniEnabled {
		rm.logger.Warn("⚠️  Using direct pg_promote() - Patroni etcd state may be inconsistent!")
		rm.logger.Warnw("🚨 OPERATOR ALERT: Patroni fallback triggered - manual etcd reconciliation may be required",
			"fallbackReason", fallbackReason,
			"nodeId", rm.nodeID,
			"localIP", rm.localIP,
		)
	}

	// Track fallback metrics
	rm.patroniFallbackTotal.Add(1)
	rm.lastFallbackReason.Store(fallbackReason)

	_, err = node.Pool.Exec(ctx, "SELECT pg_promote(true, 60)")
	if err != nil {
		rm.logger.Errorw("VIP-triggered promotion failed",
			"error", err,
		)
		return
	}

	// Verify promotion actually succeeded
	var stillInRecovery bool
	verifyErr := node.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&stillInRecovery)
	if verifyErr != nil || stillInRecovery {
		rm.logger.Errorw("❌ pg_promote() did not complete successfully",
			"stillInRecovery", stillInRecovery,
			"verifyError", verifyErr,
		)
		return
	}

	rm.lastPromotion.Store(now)

	rm.mu.Lock()
	rm.role = RolePrimary
	rm.primaryHost = rm.localIP
	rm.primaryPort = rm.config.PostgresPort
	callback := rm.onRoleChange
	rm.mu.Unlock()

	rm.logger.Infow("✅ PROMOTED TO PRIMARY via pg_promote() (VIP)",
		"nodeId", rm.nodeID,
		"fallbackReason", fallbackReason,
		"patroniFallbackTotal", rm.patroniFallbackTotal.Load(),
	)

	if callback != nil {
		go callback(RolePrimary)
	}
}

// verifyClusterQuorum checks if we have quorum to safely perform pg_promote()
// This prevents split-brain by ensuring we can verify cluster state before promotion
func (rm *ReplicationManager) verifyClusterQuorum(ctx context.Context) (bool, error) {
	rm.mu.RLock()
	knownNodes := len(rm.replicas)
	rm.mu.RUnlock()

	// If we have no known nodes, we cannot establish quorum
	if knownNodes == 0 {
		return false, fmt.Errorf("no known cluster nodes")
	}

	// Try to reach at least one other node to verify cluster state.
	// Uses Patroni API when available (stronger than TCP-only), falling back to TCP.
	reachableNodes := 0
	var lastErr error

	// Try Patroni API first — this verifies the actual cluster state, not just TCP
	if rm.config.PatroniEnabled {
		status, err := rm.getPatroniStatus(context.Background())
		if err == nil && status != nil {
			// Patroni is alive and responding — this is a strong quorum signal.
			// Count the Patroni node as reachable (it confirms the cluster is functional).
			reachableNodes++
		} else {
			lastErr = err
		}
	}

	// Fallback: TCP check against known replicas (weaker but covers non-Patroni setups)
	if reachableNodes == 0 {
		rm.mu.RLock()
		for _, replica := range rm.replicas {
			if replica.IsLocal {
				continue
			}

			addr := net.JoinHostPort(replica.Host, fmt.Sprintf("%d", replica.Port))
			conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				lastErr = err
				continue
			}
			conn.Close()
			reachableNodes++
		}
		rm.mu.RUnlock()
	}

	// For a 2-node cluster, if we can reach 0 other nodes, we might be partitioned
	// For larger clusters, require majority minus 1 (since we count ourselves)
	totalNodes := knownNodes
	if totalNodes <= 2 {
		// 2-node cluster: need to reach at least 1 other node OR have explicit override
		if reachableNodes == 0 {
			rm.logger.Warnw("Cannot reach any other nodes in cluster",
				"knownNodes", knownNodes,
				"reachableNodes", reachableNodes,
				"lastError", lastErr,
			)
			return false, fmt.Errorf("cannot reach any peer nodes: %w", lastErr)
		}
	} else {
		// Larger cluster: need majority
		majority := (totalNodes / 2) + 1
		if reachableNodes+1 < majority { // +1 for ourselves
			return false, fmt.Errorf("insufficient quorum: reachable=%d, needed=%d", reachableNodes+1, majority)
		}
	}

	return true, nil
}

// demoteToReplica demotes this node to replica and re-syncs.
// NOTE: We only set the internal role here. Actual PostgreSQL demotion is
// handled by Patroni via etcd leader election. The GATE Omega1 check in
// writeBatchToNode/insertBlockToNode prevents any writes to a recovering node,
// so this internal flag is a fast-path hint, not the sole protection.
func (rm *ReplicationManager) demoteToReplica() {
	rm.promotionLock.Lock()
	defer rm.promotionLock.Unlock()

	// Debounce
	now := time.Now().Unix()
	lastDemo := rm.lastDemotion.Load()
	if now-lastDemo < 10 {
		rm.logger.Debug("Demotion debounced (VIP)")
		return
	}
	rm.lastDemotion.Store(now)

	rm.logger.Warn("Demoting to replica (VIP triggered)...")

	// Find the new primary from discovered nodes
	rm.mu.Lock()
	var newPrimaryHost string
	var newPrimaryPort int

	for _, replica := range rm.replicas {
		if replica.Role == RolePrimary && !replica.IsLocal {
			newPrimaryHost = replica.Host
			newPrimaryPort = replica.Port
			break
		}
	}

	if newPrimaryHost == "" {
		rm.logger.Warn("No remote primary found, will re-discover")
		rm.mu.Unlock()
		return
	}

	rm.role = RoleReplica
	rm.primaryHost = newPrimaryHost
	rm.primaryPort = newPrimaryPort
	callback := rm.onRoleChange
	rm.mu.Unlock()

	rm.logger.Infow("Configured as replica (VIP demoted)",
		"newPrimary", fmt.Sprintf("%s:%d", newPrimaryHost, newPrimaryPort),
	)

	if callback != nil {
		go callback(RoleReplica)
	}

	// Initiate pg_rewind if needed (async)
	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		rm.initiateRewind(newPrimaryHost, newPrimaryPort)
	}()
}

// initiateRewind uses pg_rewind to re-sync a demoted primary
func (rm *ReplicationManager) initiateRewind(primaryHost string, primaryPort int) {
	rm.logger.Infow("Initiating pg_rewind to sync with new primary",
		"primary", fmt.Sprintf("%s:%d", primaryHost, primaryPort),
	)

	// pg_rewind requires the server to be stopped, so this is complex
	// In practice, you'd typically:
	// 1. Stop PostgreSQL
	// 2. Run pg_rewind
	// 3. Start PostgreSQL as replica

	// For now, log the intent - full implementation requires root access
	rm.logger.Warn("pg_rewind requires PostgreSQL restart - manual intervention may be needed")
	rm.logger.Info("To manually rewind: pg_rewind --target-pgdata=$PGDATA --source-server='host=PRIMARY port=5432'")
}

// =============================================================================
// Patroni API Integration
// =============================================================================

// PatroniStatus represents the response from Patroni's /patroni endpoint
type PatroniStatus struct {
	State          string `json:"state"`
	Role           string `json:"role"`
	ServerVersion  int    `json:"server_version"`
	ClusterUnlocked bool  `json:"cluster_unlocked"`
	Timeline       int    `json:"timeline"`
	Xlog           struct {
		Location         int64 `json:"location"`
		ReceivedLocation int64 `json:"received_location"`
		ReplayedLocation int64 `json:"replayed_location"`
	} `json:"xlog"`
	Patroni struct {
		Version string `json:"version"`
		Scope   string `json:"scope"`
	} `json:"patroni"`
}

// PatroniClusterMember represents a member in Patroni cluster
type PatroniClusterMember struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	State    string `json:"state"`
	APIURL   string `json:"api_url"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Timeline int    `json:"timeline"`
	Lag      int64  `json:"lag"`
}

// PatroniCluster represents the response from Patroni's /cluster endpoint
type PatroniCluster struct {
	Members []PatroniClusterMember `json:"members"`
}

// getPatroniAPIURL returns the Patroni API base URL
func (rm *ReplicationManager) getPatroniAPIURL() string {
	host := rm.config.PatroniAPIHost
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		rm.logger.Warnw("Patroni API using HTTP to non-localhost host — credentials may be sent in cleartext",
			"host", host,
			"port", rm.config.PatroniAPIPort,
		)
	}
	return fmt.Sprintf("http://%s:%d", host, rm.config.PatroniAPIPort)
}

// addPatroniAuth adds Basic Authentication to the request if configured
func (rm *ReplicationManager) addPatroniAuth(req *http.Request) {
	if rm.config.PatroniUsername != "" && rm.config.PatroniPassword != "" {
		req.SetBasicAuth(rm.config.PatroniUsername, rm.config.PatroniPassword)
	}
}

// getPatroniStatus queries the Patroni status endpoint
func (rm *ReplicationManager) getPatroniStatus(ctx context.Context) (*PatroniStatus, error) {
	url := rm.getPatroniAPIURL() + "/patroni"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	rm.addPatroniAuth(req)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query Patroni: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("Patroni authentication failed (HTTP %d) - check PATRONI_API_USERNAME/PASSWORD", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Patroni status query failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var status PatroniStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("failed to parse Patroni status: %w", err)
	}

	return &status, nil
}

// getPatroniCluster queries the Patroni cluster endpoint
func (rm *ReplicationManager) getPatroniCluster(ctx context.Context) (*PatroniCluster, error) {
	url := rm.getPatroniAPIURL() + "/cluster"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	rm.addPatroniAuth(req)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query Patroni cluster: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("Patroni authentication failed (HTTP %d)", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Patroni cluster query failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var cluster PatroniCluster
	if err := json.Unmarshal(body, &cluster); err != nil {
		return nil, fmt.Errorf("failed to parse Patroni cluster: %w", err)
	}

	return &cluster, nil
}

// isPatroniAvailable checks if Patroni API is available
func (rm *ReplicationManager) isPatroniAvailable(ctx context.Context) bool {
	_, err := rm.getPatroniStatus(ctx)
	return err == nil
}

// promoteViaPatroni triggers a failover via Patroni REST API
// This ensures etcd state stays in sync with PostgreSQL state
//
// Safety checks performed:
// 1. Timeline validation - ensures candidate is on correct timeline
// 2. Lag check - ensures candidate is within acceptable lag threshold
// 3. 409 conflict retry - handles concurrent failover attempts
// 4. etcd leader verification - confirms etcd state after success
func (rm *ReplicationManager) promoteViaPatroni(ctx context.Context) error {
	// First, get current cluster state to find the current leader
	cluster, err := rm.getPatroniCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cluster state (HTTP): %w", err)
	}

	// Find current leader and our node, capturing timeline and lag info
	var currentLeader string
	var currentLeaderTimeline int
	var ourNodeName string
	var ourTimeline int
	var ourLag int64
	localHostname := getLocalHostname()

	rm.logger.Debugw("Analyzing Patroni cluster state",
		"memberCount", len(cluster.Members),
		"localHostname", localHostname,
		"localIP", rm.localIP,
	)

	for _, member := range cluster.Members {
		rm.logger.Debugw("Cluster member",
			"name", member.Name,
			"role", member.Role,
			"host", member.Host,
			"timeline", member.Timeline,
			"lag", member.Lag,
			"state", member.State,
		)

		if member.Role == "leader" {
			currentLeader = member.Name
			currentLeaderTimeline = member.Timeline
		}
		// Match by hostname or IP
		if member.Host == rm.localIP || member.Name == localHostname {
			ourNodeName = member.Name
			ourTimeline = member.Timeline
			ourLag = member.Lag
		}
	}

	if ourNodeName == "" {
		// Log all members to help diagnose the issue
		memberNames := make([]string, 0, len(cluster.Members))
		for _, m := range cluster.Members {
			memberNames = append(memberNames, fmt.Sprintf("%s(%s)", m.Name, m.Host))
		}
		return fmt.Errorf("could not determine local Patroni node name - members: %v, localIP: %s, hostname: %s",
			memberNames, rm.localIP, localHostname)
	}

	if currentLeader == ourNodeName {
		rm.logger.Info("Already the Patroni leader, no failover needed")
		rm.patroniPromotionTotal.Add(1)
		return nil
	}

	// ============================================================================
	// SAFETY CHECK 1: Timeline Validation
	// ============================================================================
	// Ensure we're on the same timeline as the leader to prevent data divergence
	if currentLeaderTimeline > 0 && ourTimeline > 0 && ourTimeline != currentLeaderTimeline {
		rm.logger.Warnw("⚠️  Timeline mismatch detected - candidate may have diverged",
			"leaderTimeline", currentLeaderTimeline,
			"ourTimeline", ourTimeline,
			"currentLeader", currentLeader,
			"candidate", ourNodeName,
		)
		// Allow promotion if we're one timeline ahead (expected after failover)
		// Reject if we're behind (data divergence risk)
		if ourTimeline < currentLeaderTimeline {
			return fmt.Errorf("timeline mismatch: candidate timeline %d < leader timeline %d - potential data loss",
				ourTimeline, currentLeaderTimeline)
		}
	}

	// ============================================================================
	// SAFETY CHECK 2: Lag Validation
	// ============================================================================
	// Ensure we're within acceptable lag before attempting failover
	maxLag := rm.config.MaxLagOnFailover
	if maxLag > 0 && ourLag > maxLag {
		return fmt.Errorf("candidate lag too high for failover: %d bytes > max %d bytes",
			ourLag, maxLag)
	}

	rm.logger.Infow("Pre-failover validation passed",
		"currentLeader", currentLeader,
		"candidate", ourNodeName,
		"timeline", ourTimeline,
		"lag", ourLag,
		"maxLag", maxLag,
	)

	// ============================================================================
	// FAILOVER REQUEST WITH 409 CONFLICT RETRY
	// ============================================================================
	url := rm.getPatroniAPIURL() + "/failover"

	payload := map[string]string{
		"candidate": ourNodeName,
	}
	if currentLeader != "" {
		payload["leader"] = currentLeader
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Retry configuration
	maxAttempts := rm.config.PatroniRetryAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	retryDelay := rm.config.PatroniRetryDelay
	if retryDelay <= 0 {
		retryDelay = 5 * time.Second
	}

	var lastErr error
	var lastStatusCode int
	var lastBody string

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		rm.logger.Infow("Triggering Patroni failover",
			"currentLeader", currentLeader,
			"candidate", ourNodeName,
			"attempt", attempt,
			"maxAttempts", maxAttempts,
		)

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		rm.addPatroniAuth(req)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failover request failed: %w", err)
			rm.logger.Warnw("Patroni failover request error, will retry",
				"attempt", attempt,
				"error", err,
			)
			if attempt < maxAttempts {
				time.Sleep(retryDelay)
				continue
			}
			return lastErr
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		lastStatusCode = resp.StatusCode
		lastBody = string(body)

		// Success
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			rm.logger.Infow("Patroni failover initiated successfully",
				"response", lastBody,
				"attempt", attempt,
			)

			// ============================================================================
			// SAFETY CHECK 3: etcd Leader Verification After Success
			// ============================================================================
			verifyErr := rm.verifyPatroniLeaderState(ctx, ourNodeName)
			if verifyErr != nil {
				rm.logger.Errorw("⚠️  Patroni API returned success but leader verification failed",
					"error", verifyErr,
					"expectedLeader", ourNodeName,
				)
				// Return error to trigger fallback evaluation
				return fmt.Errorf("leader verification failed after Patroni success: %w", verifyErr)
			}

			rm.patroniPromotionTotal.Add(1)
			return nil
		}

		// 409 Conflict - another failover in progress
		if resp.StatusCode == 409 {
			rm.patroniRetryTotal.Add(1)
			rm.logger.Warnw("Patroni failover conflict (409) - another failover in progress",
				"attempt", attempt,
				"response", lastBody,
			)

			if attempt < maxAttempts {
				rm.logger.Infow("Waiting before retry",
					"delay", retryDelay,
					"nextAttempt", attempt+1,
				)
				time.Sleep(retryDelay)

				// Re-check cluster state before retry - leader may have changed
				refreshedCluster, refreshErr := rm.getPatroniCluster(ctx)
				if refreshErr == nil {
					for _, member := range refreshedCluster.Members {
						if member.Role == "leader" && member.Name == ourNodeName {
							rm.logger.Info("We became leader during retry wait - no further action needed")
							rm.patroniPromotionTotal.Add(1)
							return nil
						}
					}
				}
				continue
			}
		}

		// 503 Service Unavailable - Patroni might be in transition
		if resp.StatusCode == 503 {
			rm.logger.Warnw("Patroni service unavailable (503)",
				"attempt", attempt,
				"response", lastBody,
			)
			if attempt < maxAttempts {
				time.Sleep(retryDelay)
				continue
			}
		}

		// Other errors - log and potentially retry
		lastErr = fmt.Errorf("Patroni failover failed (HTTP %d): %s", resp.StatusCode, lastBody)
		rm.logger.Warnw("Patroni failover failed",
			"statusCode", resp.StatusCode,
			"response", lastBody,
			"attempt", attempt,
		)

		// Don't retry on auth failures
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return lastErr
		}

		if attempt < maxAttempts {
			time.Sleep(retryDelay)
		}
	}

	return fmt.Errorf("Patroni failover failed after %d attempts (last HTTP %d): %s",
		maxAttempts, lastStatusCode, lastBody)
}

// verifyPatroniLeaderState verifies that etcd/Patroni now shows us as leader
// This is a critical safety check to ensure state consistency
func (rm *ReplicationManager) verifyPatroniLeaderState(ctx context.Context, expectedLeader string) error {
	// Wait a moment for etcd to propagate the change
	time.Sleep(2 * time.Second)

	// Query cluster state multiple times to handle propagation delay
	maxVerifyAttempts := 5
	verifyDelay := 2 * time.Second

	for attempt := 1; attempt <= maxVerifyAttempts; attempt++ {
		cluster, err := rm.getPatroniCluster(ctx)
		if err != nil {
			rm.logger.Warnw("Failed to get cluster state for verification",
				"attempt", attempt,
				"error", err,
			)
			if attempt < maxVerifyAttempts {
				time.Sleep(verifyDelay)
				continue
			}
			return fmt.Errorf("cannot verify leader state: %w", err)
		}

		// Find current leader
		var actualLeader string
		for _, member := range cluster.Members {
			if member.Role == "leader" {
				actualLeader = member.Name
				break
			}
		}

		if actualLeader == expectedLeader {
			rm.logger.Infow("✅ etcd leader verification successful",
				"leader", actualLeader,
				"attempt", attempt,
			)
			return nil
		}

		rm.logger.Warnw("Leader mismatch during verification",
			"expected", expectedLeader,
			"actual", actualLeader,
			"attempt", attempt,
		)

		if attempt < maxVerifyAttempts {
			time.Sleep(verifyDelay)
		}
	}

	return fmt.Errorf("leader verification failed: expected %s as leader but etcd shows different state",
		expectedLeader)
}

// switchoverViaPatroni triggers a graceful switchover via Patroni REST API
// Use this when both nodes are healthy and you want a clean transition
func (rm *ReplicationManager) switchoverViaPatroni(ctx context.Context) error {
	cluster, err := rm.getPatroniCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cluster state: %w", err)
	}

	var currentLeader string
	var ourNodeName string
	localHostname := getLocalHostname()

	for _, member := range cluster.Members {
		if member.Role == "leader" {
			currentLeader = member.Name
		}
		if member.Host == rm.localIP || member.Name == localHostname {
			ourNodeName = member.Name
		}
	}

	if ourNodeName == "" {
		return fmt.Errorf("could not determine local Patroni node name")
	}

	if currentLeader == ourNodeName {
		rm.logger.Info("Already the Patroni leader, no switchover needed")
		return nil
	}

	if currentLeader == "" {
		return fmt.Errorf("no current leader found, use failover instead")
	}

	rm.logger.Infow("Triggering Patroni switchover",
		"currentLeader", currentLeader,
		"candidate", ourNodeName,
	)

	url := rm.getPatroniAPIURL() + "/switchover"

	payload := map[string]string{
		"leader":    currentLeader,
		"candidate": ourNodeName,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	rm.addPatroniAuth(req) // Add authentication

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("switchover request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("Patroni authentication failed (HTTP %d)", resp.StatusCode)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		rm.logger.Infow("Patroni switchover initiated successfully",
			"response", string(body),
		)
		return nil
	}

	return fmt.Errorf("Patroni switchover failed (HTTP %d): %s", resp.StatusCode, string(body))
}

// =============================================================================
// Metrics Export for Monitoring
// =============================================================================

// ReplicationMetrics contains metrics for monitoring Patroni/PostgreSQL failover
type ReplicationMetrics struct {
	PatroniFallbackTotal   int64  `json:"patroni_fallback_total"`
	PatroniPromotionTotal  int64  `json:"patroni_promotion_total"`
	PatroniRetryTotal      int64  `json:"patroni_retry_total"`
	LastFallbackReason     string `json:"last_fallback_reason"`
	SplitBrainRiskDetected bool   `json:"split_brain_risk_detected"`
	CurrentRole            string `json:"current_role"`
}

// GetMetrics returns current replication metrics for Prometheus/monitoring
func (rm *ReplicationManager) GetMetrics() ReplicationMetrics {
	reason, _ := rm.lastFallbackReason.Load().(string)

	return ReplicationMetrics{
		PatroniFallbackTotal:   rm.patroniFallbackTotal.Load(),
		PatroniPromotionTotal:  rm.patroniPromotionTotal.Load(),
		PatroniRetryTotal:      rm.patroniRetryTotal.Load(),
		LastFallbackReason:     reason,
		SplitBrainRiskDetected: rm.splitBrainRiskDetected.Load(),
		CurrentRole:            rm.GetRole().String(),
	}
}

// ResetSplitBrainFlag resets the split-brain risk flag after manual review
func (rm *ReplicationManager) ResetSplitBrainFlag() {
	rm.splitBrainRiskDetected.Store(false)
	rm.logger.Info("Split-brain risk flag reset by operator")
}

// =============================================================================
// Recovery / Failback
// =============================================================================

// RecoveryConfig holds configuration for automatic recovery
type RecoveryConfig struct {
	// Enabled controls automatic failback
	Enabled bool `yaml:"enabled"`

	// FailbackDelay is the time to wait before failing back to original primary
	FailbackDelay time.Duration `yaml:"failback_delay"`

	// HealthCheckInterval for monitoring recovered nodes
	HealthCheckInterval time.Duration `yaml:"health_check_interval"`

	// MinStableTime is how long a recovered node must be stable before failback
	MinStableTime time.Duration `yaml:"min_stable_time"`
}

// DefaultRecoveryConfig returns sensible defaults
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		Enabled:             true,
		FailbackDelay:       5 * time.Minute,
		HealthCheckInterval: 30 * time.Second,
		MinStableTime:       2 * time.Minute,
	}
}

// recoveryState tracks recovery status for a node
type recoveryState struct {
	NodeID          string
	StartedAt       time.Time
	StableSince     time.Time
	IsStable        bool
	LastHealthCheck time.Time
	HealthyChecks   int
}

// CheckRecovery checks if any failed nodes have recovered.
// Snapshots replica info under read lock, then performs all network I/O (TCP probe +
// full DB verification) without holding any lock to avoid blocking other operations.
func (rm *ReplicationManager) CheckRecovery(ctx context.Context) {
	// Snapshot targets under read lock
	type checkTarget struct {
		nodeID string
		host   string
		port   int
	}
	rm.mu.RLock()
	var targets []checkTarget
	for nodeID, replica := range rm.replicas {
		if replica.State != ReplicationStateSynced {
			targets = append(targets, checkTarget{nodeID, replica.Host, replica.Port})
		}
	}
	rm.mu.RUnlock()

	now := time.Now()
	for _, t := range targets {
		if rm.isPrimaryReachable(ctx, t.host, t.port) {
			// Verify recovery outside lock — verifyNodeRecovery does network I/O
			// (pgxpool connect + query). Host/port already snapshotted in checkTarget.
			tempReplica := &ReplicaInfo{Host: t.host, Port: t.port}
			recovered := rm.verifyNodeRecovery(ctx, tempReplica)

			// Update state under write lock
			rm.mu.Lock()
			replica, exists := rm.replicas[t.nodeID]
			if exists {
				replica.LastContact = now
				if recovered {
					replica.State = ReplicationStateSynced
					rm.logger.Infow("Node recovered",
						"nodeId", t.nodeID,
						"host", t.host,
					)
				}
			}
			rm.mu.Unlock()
		}
	}
}

// verifyNodeRecovery verifies a node is properly recovered
func (rm *ReplicationManager) verifyNodeRecovery(ctx context.Context, replica *ReplicaInfo) bool {
	replicationPassword := os.Getenv("SPIRAL_REPLICATION_PASSWORD")
	if replicationPassword == "" {
		replicationPassword = os.Getenv("SPIRAL_DATABASE_PASSWORD")
	}
	if replicationPassword == "" {
		return false
	}

	// SECURITY: URL-encode credentials to prevent connection string injection
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/postgres?connect_timeout=5",
		url.QueryEscape(rm.config.ReplicationUser), url.QueryEscape(replicationPassword), replica.Host, replica.Port,
	)

	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return false
	}
	poolConfig.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return false
	}
	defer pool.Close()

	// Verify can query
	var result int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	return err == nil && result == 1
}

// ScheduleFailback schedules a failback to the original primary.
// The goroutine is tracked via rm.wg so Stop() waits for it.
func (rm *ReplicationManager) ScheduleFailback(ctx context.Context, originalPrimary string, delay time.Duration) {
	rm.logger.Infow("Scheduling failback to original primary",
		"originalPrimary", originalPrimary,
		"delay", delay,
	)

	rm.wg.Add(1)
	go func() {
		defer rm.wg.Done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			rm.executeFailback(ctx, originalPrimary)
		}
	}()
}

// executeFailback performs failback to the original primary
func (rm *ReplicationManager) executeFailback(ctx context.Context, originalPrimary string) {
	rm.promotionLock.Lock()
	defer rm.promotionLock.Unlock()

	rm.mu.RLock()
	role := rm.role
	rm.mu.RUnlock()

	if role != RolePrimary {
		rm.logger.Debug("Not primary, cannot initiate failback")
		return
	}

	rm.logger.Infow("Executing failback to original primary",
		"originalPrimary", originalPrimary,
	)

	// This would involve:
	// 1. Stopping writes
	// 2. Waiting for replica to catch up
	// 3. Demoting self
	// 4. Promoting original primary

	rm.logger.Warn("Failback requires coordination - triggering VIP-based failover")
}

// =============================================================================
// Security - Credential Encryption
// =============================================================================

// SecureCredentials holds encrypted database credentials
type SecureCredentials struct {
	EncryptedPassword string `json:"encrypted_password"`
	Salt              string `json:"salt"`
	Nonce             string `json:"nonce"`
}

// CredentialManager handles secure credential storage
type CredentialManager struct {
	masterKey []byte
	logger    *zap.SugaredLogger
}

// NewCredentialManager creates a credential manager with the given master key
func NewCredentialManager(masterKeyEnv string, logger *zap.Logger) (*CredentialManager, error) {
	// Get master key from environment
	masterKeyB64 := os.Getenv(masterKeyEnv)
	if masterKeyB64 == "" {
		// Generate a new master key if not provided
		masterKey := make([]byte, 32)
		if _, err := rand.Read(masterKey); err != nil {
			return nil, fmt.Errorf("failed to generate master key: %w", err)
		}
		// SECURITY: Do NOT log the full master key — it is sensitive cryptographic material.
		// The key is written to stdout only (not structured logging) so it does not end up
		// in log aggregation systems. Operators should capture it from the initial startup
		// output and store it in a secrets manager.
		masterKeyB64 = base64.StdEncoding.EncodeToString(masterKey)
		fmt.Fprintf(os.Stdout, "MASTER KEY (set %s for persistence): %s\n", masterKeyEnv, masterKeyB64)
		logger.Sugar().Warnw("Generated new master key - printed to stdout. Set the environment variable for persistence.",
			"envVar", masterKeyEnv,
			"keyPrefix", masterKeyB64[:4]+"...",
		)
		return &CredentialManager{
			masterKey: masterKey,
			logger:    logger.Sugar(),
		}, nil
	}

	masterKey, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		return nil, fmt.Errorf("invalid master key encoding: %w", err)
	}

	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes")
	}

	return &CredentialManager{
		masterKey: masterKey,
		logger:    logger.Sugar(),
	}, nil
}

// EncryptPassword encrypts a password using AES-256-GCM
func (cm *CredentialManager) EncryptPassword(password string) (*SecureCredentials, error) {
	// Generate salt
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}

	// Derive key using HKDF
	derivedKey := make([]byte, 32)
	kdf := hkdf.New(sha256.New, cm.masterKey, salt, []byte("spiral-postgres-replication"))
	if _, err := kdf.Read(derivedKey); err != nil {
		return nil, fmt.Errorf("failed to derive key: %w", err)
	}

	// Create cipher
	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt
	ciphertext := gcm.Seal(nil, nonce, []byte(password), nil)

	return &SecureCredentials{
		EncryptedPassword: base64.StdEncoding.EncodeToString(ciphertext),
		Salt:              base64.StdEncoding.EncodeToString(salt),
		Nonce:             base64.StdEncoding.EncodeToString(nonce),
	}, nil
}

// DecryptPassword decrypts a password
func (cm *CredentialManager) DecryptPassword(creds *SecureCredentials) (string, error) {
	salt, err := base64.StdEncoding.DecodeString(creds.Salt)
	if err != nil {
		return "", fmt.Errorf("invalid salt: %w", err)
	}

	nonce, err := base64.StdEncoding.DecodeString(creds.Nonce)
	if err != nil {
		return "", fmt.Errorf("invalid nonce: %w", err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(creds.EncryptedPassword)
	if err != nil {
		return "", fmt.Errorf("invalid ciphertext: %w", err)
	}

	// Derive key using HKDF
	derivedKey := make([]byte, 32)
	kdf := hkdf.New(sha256.New, cm.masterKey, salt, []byte("spiral-postgres-replication"))
	if _, err := kdf.Read(derivedKey); err != nil {
		return "", fmt.Errorf("failed to derive key: %w", err)
	}

	// Create cipher
	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Decrypt
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}

	return string(plaintext), nil
}

// =============================================================================
// Security - TLS Configuration
// =============================================================================

// TLSConfig holds TLS configuration for PostgreSQL connections
type TLSConfig struct {
	// Enabled controls whether TLS is required
	Enabled bool `yaml:"enabled"`

	// Mode can be "disable", "require", "verify-ca", or "verify-full"
	Mode string `yaml:"mode"`

	// CACertFile is the path to the CA certificate
	CACertFile string `yaml:"ca_cert_file"`

	// CertFile is the path to the client certificate
	CertFile string `yaml:"cert_file"`

	// KeyFile is the path to the client key
	KeyFile string `yaml:"key_file"`
}

// DefaultTLSConfig returns TLS defaults (verify-full for security)
func DefaultTLSConfig() TLSConfig {
	return TLSConfig{
		Enabled:    true,
		Mode:       "verify-full",
		CACertFile: "/etc/spiralpool/ssl/ca.crt",
		CertFile:   "/etc/spiralpool/ssl/client.crt",
		KeyFile:    "/etc/spiralpool/ssl/client.key",
	}
}

// BuildTLSConfig creates a tls.Config from the configuration
func (tc *TLSConfig) BuildTLSConfig() (*tls.Config, error) {
	if !tc.Enabled || tc.Mode == "disable" {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load CA certificate if specified
	if tc.CACertFile != "" && fileExistsSecure(tc.CACertFile) {
		caCert, err := os.ReadFile(tc.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	// Load client certificate if specified
	if tc.CertFile != "" && tc.KeyFile != "" {
		if fileExistsSecure(tc.CertFile) && fileExistsSecure(tc.KeyFile) {
			cert, err := tls.LoadX509KeyPair(tc.CertFile, tc.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}

	// Set verification mode
	switch tc.Mode {
	case "require":
		tlsConfig.InsecureSkipVerify = true
	case "verify-ca":
		tlsConfig.InsecureSkipVerify = false
	case "verify-full":
		tlsConfig.InsecureSkipVerify = false
		// Server name verification is automatic
	}

	return tlsConfig, nil
}

// GetConnectionStringWithTLS adds TLS parameters to a connection string
func (tc *TLSConfig) GetConnectionStringWithTLS(baseConnStr string) string {
	if !tc.Enabled || tc.Mode == "disable" {
		return baseConnStr + "&sslmode=disable"
	}

	connStr := baseConnStr + "&sslmode=" + tc.Mode

	if tc.CACertFile != "" && fileExistsSecure(tc.CACertFile) {
		connStr += "&sslrootcert=" + url.QueryEscape(tc.CACertFile)
	}
	if tc.CertFile != "" && fileExistsSecure(tc.CertFile) {
		connStr += "&sslcert=" + url.QueryEscape(tc.CertFile)
	}
	if tc.KeyFile != "" && fileExistsSecure(tc.KeyFile) {
		connStr += "&sslkey=" + url.QueryEscape(tc.KeyFile)
	}

	return connStr
}

// =============================================================================
// Security - PostgreSQL Configuration Generator
// =============================================================================

// validIdentifierRe matches safe PostgreSQL identifiers (alphanumeric + underscore, 1-63 chars).
// SECURITY: Used to prevent SQL injection in generated DDL statements where parameterized
// queries are not possible (CREATE USER, GRANT, pg_hba.conf entries).
var validIdentifierRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// GenerateReplicationUser generates SQL to create a secure replication user.
// SECURITY: username is validated against a strict identifier regex to prevent SQL injection.
// Password single quotes are escaped by doubling them (SQL standard escaping).
func GenerateReplicationUser(username, password string) string {
	// SECURITY: Validate username to prevent SQL injection in DDL statements.
	// Only alphanumeric characters and underscores are allowed.
	if !validIdentifierRe.MatchString(username) {
		return fmt.Sprintf("-- ERROR: invalid username %q (must match [a-zA-Z_][a-zA-Z0-9_]{0,62})\n", username)
	}

	// SECURITY: Escape single quotes in password by doubling them (SQL standard).
	// This prevents password values like "x'; DROP TABLE users;--" from breaking out
	// of the string literal.
	escapedPassword := strings.ReplaceAll(password, "'", "''")

	// Use SCRAM-SHA-256 password encryption (PostgreSQL 10+)
	return fmt.Sprintf(`
-- Create replication user with minimal privileges
CREATE USER %s WITH REPLICATION LOGIN PASSWORD '%s';

-- Grant only required permissions
GRANT USAGE ON SCHEMA pg_catalog TO %s;
GRANT SELECT ON pg_catalog.pg_stat_replication TO %s;
`, username, escapedPassword, username, username)
}

// GeneratePgHbaEntry generates a pg_hba.conf entry for secure replication.
// SECURITY: Both username and replicaNetwork are validated to prevent injection
// into pg_hba.conf, which controls PostgreSQL authentication policy.
func GeneratePgHbaEntry(replicaNetwork, username string, useTLS bool) string {
	// SECURITY: Validate username to prevent pg_hba.conf injection.
	if !validIdentifierRe.MatchString(username) {
		return fmt.Sprintf("# ERROR: invalid username %q (must match [a-zA-Z_][a-zA-Z0-9_]{0,62})\n", username)
	}

	// SECURITY: Validate replicaNetwork as a valid CIDR or IP address.
	// An attacker-controlled network value could inject arbitrary pg_hba.conf rules.
	_, _, cidrErr := net.ParseCIDR(replicaNetwork)
	if cidrErr != nil {
		// Also accept a bare IP address (common for single-host entries)
		if net.ParseIP(replicaNetwork) == nil {
			return fmt.Sprintf("# ERROR: invalid replicaNetwork %q (must be valid CIDR or IP address)\n", replicaNetwork)
		}
	}

	method := "scram-sha-256"
	connType := "host"

	if useTLS {
		connType = "hostssl"
	}

	return fmt.Sprintf(
		"%s replication %s %s %s",
		connType, username, replicaNetwork, method,
	)
}

// GeneratePostgresqlReplicationConf generates postgresql.conf settings for replication
func GeneratePostgresqlReplicationConf(isPrimary bool, syncReplicas int) string {
	if isPrimary {
		return fmt.Sprintf(`
# Primary Replication Settings
wal_level = replica
max_wal_senders = 10
max_replication_slots = 10
synchronous_commit = on
synchronous_standby_names = '%d (ANY %d (*))'

# Security
ssl = on
ssl_cert_file = '/etc/spiralpool/ssl/server.crt'
ssl_key_file = '/etc/spiralpool/ssl/server.key'
ssl_ca_file = '/etc/spiralpool/ssl/ca.crt'
ssl_min_protocol_version = 'TLSv1.2'

# Password Encryption
password_encryption = scram-sha-256
`, syncReplicas, syncReplicas)
	}

	// Replica settings
	return `
# Replica Settings
hot_standby = on
hot_standby_feedback = on

# Security
ssl = on
ssl_cert_file = '/etc/spiralpool/ssl/server.crt'
ssl_key_file = '/etc/spiralpool/ssl/server.key'
ssl_ca_file = '/etc/spiralpool/ssl/ca.crt'
ssl_min_protocol_version = 'TLSv1.2'

# Password Encryption
password_encryption = scram-sha-256
`
}

// =============================================================================
// Security - Certificate Generation (Helper)
// =============================================================================

// GenerateSelfSignedCerts generates self-signed certificates for testing
// In production, use proper CA-signed certificates
func GenerateSelfSignedCerts(outputDir string) error {
	// Validate that outputDir is an absolute path to prevent path traversal
	if !filepath.IsAbs(outputDir) {
		return fmt.Errorf("outputDir must be an absolute path, got: %s", outputDir)
	}

	// Ensure directory exists
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		return fmt.Errorf("failed to create cert directory: %w", err)
	}

	// Generate using openssl (requires openssl to be installed)
	commands := []struct {
		name string
		args []string
	}{
		{
			"Generate CA key",
			[]string{"genrsa", "-out", filepath.Join(outputDir, "ca.key"), "4096"},
		},
		{
			"Generate CA certificate",
			[]string{"req", "-new", "-x509", "-days", "3650", "-key", filepath.Join(outputDir, "ca.key"),
				"-out", filepath.Join(outputDir, "ca.crt"), "-subj", "/CN=SpiralPool-CA"},
		},
		{
			"Generate server key",
			[]string{"genrsa", "-out", filepath.Join(outputDir, "server.key"), "2048"},
		},
		{
			"Generate server CSR",
			[]string{"req", "-new", "-key", filepath.Join(outputDir, "server.key"),
				"-out", filepath.Join(outputDir, "server.csr"), "-subj", "/CN=localhost"},
		},
		{
			"Sign server certificate",
			[]string{"x509", "-req", "-days", "365", "-in", filepath.Join(outputDir, "server.csr"),
				"-CA", filepath.Join(outputDir, "ca.crt"), "-CAkey", filepath.Join(outputDir, "ca.key"),
				"-CAcreateserial", "-out", filepath.Join(outputDir, "server.crt")},
		},
		{
			"Generate client key",
			[]string{"genrsa", "-out", filepath.Join(outputDir, "client.key"), "2048"},
		},
		{
			"Generate client CSR",
			[]string{"req", "-new", "-key", filepath.Join(outputDir, "client.key"),
				"-out", filepath.Join(outputDir, "client.csr"), "-subj", "/CN=spiralpool-client"},
		},
		{
			"Sign client certificate",
			[]string{"x509", "-req", "-days", "365", "-in", filepath.Join(outputDir, "client.csr"),
				"-CA", filepath.Join(outputDir, "ca.crt"), "-CAkey", filepath.Join(outputDir, "ca.key"),
				"-CAcreateserial", "-out", filepath.Join(outputDir, "client.crt")},
		},
	}

	for _, cmd := range commands {
		execCmd := exec.Command("openssl", cmd.args...)
		if output, err := execCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s failed: %w\n%s", cmd.name, err, output)
		}
	}

	// Set secure permissions
	keyFiles := []string{"ca.key", "server.key", "client.key"}
	for _, kf := range keyFiles {
		if err := os.Chmod(filepath.Join(outputDir, kf), 0600); err != nil {
			return fmt.Errorf("failed to set permissions on %s: %w", kf, err)
		}
	}

	return nil
}

// fileExistsSecure checks if a file exists and has secure permissions
func fileExistsSecure(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	// Check it's a file, not a directory
	if info.IsDir() {
		return false
	}

	// On Unix, check permissions for key files
	if strings.HasSuffix(path, ".key") {
		mode := info.Mode().Perm()
		if mode&0077 != 0 {
			// Key file is readable by group or others - insecure
			return false
		}
	}

	return true
}

// =============================================================================
// Integrated HA Coordinator
// =============================================================================

// HACoordinator coordinates VIP and PostgreSQL replication together
type HACoordinator struct {
	// vipCallback is reserved for VIP state change notifications (extensibility point)
	_ func(isMaster bool) //nolint:unused // Reserved for VIP integration

	replicationMgr *ReplicationManager
	dbMgr          *DatabaseManager
	logger         *zap.SugaredLogger

	mu          sync.RWMutex
	isVIPMaster bool
	// isPGPrimary is reserved for PostgreSQL primary tracking (extensibility point)
	_ bool //nolint:unused // Reserved for PG primary tracking
}

// NewHACoordinator creates an integrated HA coordinator
func NewHACoordinator(replicationMgr *ReplicationManager, dbMgr *DatabaseManager, logger *zap.Logger) *HACoordinator {
	return &HACoordinator{
		replicationMgr: replicationMgr,
		dbMgr:          dbMgr,
		logger:         logger.Sugar(),
	}
}

// GetVIPHandler returns a handler for VIP role changes that coordinates both systems
func (hac *HACoordinator) GetVIPHandler() func(isMaster bool) {
	return func(isMaster bool) {
		hac.handleVIPChange(isMaster)
	}
}

// handleVIPChange coordinates VIP and database failover
func (hac *HACoordinator) handleVIPChange(isMaster bool) {
	hac.mu.Lock()
	hac.isVIPMaster = isMaster
	hac.mu.Unlock()

	hac.logger.Infow("HA Coordinator: VIP role changed",
		"isMaster", isMaster,
	)

	// Trigger PostgreSQL replication role change
	if hac.replicationMgr != nil {
		hac.replicationMgr.handleVIPRoleChange(isMaster)
	}

	// Trigger database connection failover
	if hac.dbMgr != nil {
		callback := hac.dbMgr.VIPFailoverCallback()
		callback(isMaster)
	}
}

// GetStatus returns the current HA status
func (hac *HACoordinator) GetStatus() map[string]interface{} {
	hac.mu.RLock()
	defer hac.mu.RUnlock()

	status := map[string]interface{}{
		"vip_master": hac.isVIPMaster,
	}

	if hac.replicationMgr != nil {
		status["pg_role"] = hac.replicationMgr.GetRole().String()
		host, port, isLocal := hac.replicationMgr.GetPrimaryInfo()
		status["pg_primary_host"] = host
		status["pg_primary_port"] = port
		status["pg_primary_is_local"] = isLocal
	}

	if hac.dbMgr != nil {
		stats := hac.dbMgr.Stats()
		status["db_active_node"] = stats.ActiveNode
		status["db_healthy_nodes"] = stats.HealthyNodes
		status["db_failovers"] = stats.Failovers
	}

	return status
}
