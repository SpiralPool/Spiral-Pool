// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database provides PostgreSQL persistence with multi-node failover.
//
// DatabaseManager supports multiple PostgreSQL nodes with automatic failover:
// - Primary node for writes
// - Automatic failover to replicas when primary fails
// - Health monitoring with configurable thresholds
// - Automatic recovery when primary comes back online
//
// # Solo Mining Note
//
// For solo mining pools, database replication between nodes is typically NOT required.
// This is because:
//   - Block rewards go directly to the miner's coinbase address (no payouts table)
//   - Miner sessions are transient and held in memory
//   - Share data is primarily for statistics, not payment calculations
//   - If the primary fails, the backup can start fresh with a clean state
//
// The HA failover ensures miners always have a pool to connect to, even if the
// backup doesn't have historical share data. When a block is found, the reward
// goes directly to the configured coinbase address regardless of which node is active.
//
// For production deployments that require share history preservation, configure
// PostgreSQL streaming replication separately (outside of this package).
package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// generateNodeID creates a unique identifier for a database node.
// Format: db-<priority>-<host>:<port>-<random4bytes>
// The random suffix prevents ID conflicts when the same host:port is configured multiple times.
func generateNodeID(priority int, host string, port int) string {
	randomBytes := make([]byte, 4)
	_, _ = rand.Read(randomBytes) // #nosec G104
	return fmt.Sprintf("db-%d-%s:%d-%s", priority, host, port, hex.EncodeToString(randomBytes))
}

// Database failover thresholds
const (
	// MaxDBNodeFailures before marking a node as unhealthy
	MaxDBNodeFailures = 3
	// DBHealthCheckInterval between health checks
	DBHealthCheckInterval = 10 * time.Second
	// DBReconnectBackoff initial backoff for reconnection attempts
	DBReconnectBackoff = 5 * time.Second
	// DBMaxReconnectBackoff maximum backoff duration
	DBMaxReconnectBackoff = 2 * time.Minute
)

// DBNodeState represents the health state of a database node
type DBNodeState int

const (
	DBNodeHealthy DBNodeState = iota
	DBNodeDegraded
	DBNodeUnhealthy
	DBNodeOffline
)

func (s DBNodeState) String() string {
	switch s {
	case DBNodeHealthy:
		return "healthy"
	case DBNodeDegraded:
		return "degraded"
	case DBNodeUnhealthy:
		return "unhealthy"
	case DBNodeOffline:
		return "offline"
	default:
		return "unknown"
	}
}

// DBNodeConfig is an alias for config.DatabaseNodeConfig
type DBNodeConfig = config.DatabaseNodeConfig

// nodeConnectionString generates a PostgreSQL connection string for a node.
// SECURITY: User and Password are URL-encoded to prevent connection string injection.
// Special characters like @, :, /, %, # in credentials could corrupt the connection URL
// and potentially redirect connections to attacker-controlled servers.
func nodeConnectionString(c *DBNodeConfig) string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?pool_max_conns=%d",
		url.QueryEscape(c.User), url.QueryEscape(c.Password), c.Host, c.Port, url.PathEscape(c.Database), c.MaxConnections,
	)
}

// SECURITY: safeConnectionString returns a connection string with password masked for logging (DB-03 fix)
// This prevents credential exposure in log files and error messages.
func safeConnectionString(c *DBNodeConfig) string {
	return fmt.Sprintf(
		"postgres://%s:***@%s:%d/%s?pool_max_conns=%d",
		url.QueryEscape(c.User), c.Host, c.Port, url.PathEscape(c.Database), c.MaxConnections,
	)
}

// ManagedDBNode represents a database node with health tracking
type ManagedDBNode struct {
	Config   DBNodeConfig
	Pool     *pgxpool.Pool
	ID       string
	Priority int
	ReadOnly bool

	// Health tracking
	mu               sync.RWMutex
	State            DBNodeState
	ConsecutiveFails int
	LastSuccess      time.Time
	LastError        error
	LastHealthCheck  time.Time
	ResponseTimeAvg  time.Duration
}

// DatabaseManager manages multiple PostgreSQL nodes with failover
type DatabaseManager struct {
	nodes     []*ManagedDBNode
	poolID    string
	algorithm string // Mining algorithm for hashrate calculations
	logger    *zap.SugaredLogger

	// Current active node for writes
	activeNodeIdx atomic.Int32

	// State
	running atomic.Bool
	wg      sync.WaitGroup

	// Metrics
	failovers     atomic.Uint64
	writeFailures atomic.Uint64
	readFailures  atomic.Uint64

	// Prometheus metrics (optional, nil = disabled)
	promMetrics dbMetrics

	// Advisory lock pinning — TryAdvisoryLock and ReleaseAdvisoryLock must use the
	// same PostgresDB instance (same pool session) to prevent lock/unlock hitting
	// different nodes after failover. Cleared after release.
	advisoryDB *PostgresDB
	advisoryMu sync.Mutex

	// VIP failover debounce — shared across all VIPFailoverCallback invocations
	// to ensure the debounce timer persists (not reset per call).
	vipFailoverDebounce atomic.Int64

	// Context cancel for graceful shutdown of health check goroutine
	cancelFunc context.CancelFunc
}

// dbMetrics is a minimal interface for DB metric reporting to avoid import cycles.
type dbMetrics interface {
	SetDBActiveNode(nodeID string)
	IncDBFailover()
}

// NewDatabaseManager creates a new database manager with failover support
func NewDatabaseManager(configs []DBNodeConfig, poolID string, algorithm string, logger *zap.Logger) (*DatabaseManager, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("database HA requires at least one node: check database.ha.nodes in config")
	}

	// SECURITY: Validate poolID to prevent SQL injection in table name interpolation.
	// poolID is used in fmt.Sprintf("shares_%s", poolID) for dynamic table names which
	// cannot be parameterized. validIdentifierRe is defined in replication.go (same package).
	if !validIdentifierRe.MatchString(poolID) {
		return nil, fmt.Errorf("invalid pool ID %q: must be a valid SQL identifier (alphanumeric/underscore, 1-63 chars)", poolID)
	}

	log := logger.Sugar()
	if algorithm == "" {
		algorithm = "sha256d"
	}
	dm := &DatabaseManager{
		nodes:     make([]*ManagedDBNode, 0, len(configs)),
		poolID:    poolID,
		algorithm: algorithm,
		logger:    log,
	}

	// Validate and create nodes
	for _, cfg := range configs {
		if cfg.MaxConnections <= 0 || cfg.MaxConnections > 1000 {
			cfg.MaxConnections = 10 // Safe default
		}
		nodeID := generateNodeID(cfg.Priority, cfg.Host, cfg.Port)

		poolConfig, err := pgxpool.ParseConfig(nodeConnectionString(&cfg))
		if err != nil {
			// SECURITY: Use safe connection string in error message to prevent credential exposure (DB-03 fix)
			return nil, fmt.Errorf("failed to parse connection config for %s (%s): %w", nodeID, safeConnectionString(&cfg), err)
		}

		poolConfig.MaxConns = int32(cfg.MaxConnections)
		poolConfig.MinConns = 1
		poolConfig.MaxConnLifetime = 1 * time.Hour
		poolConfig.MaxConnIdleTime = 30 * time.Minute

		pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
		if err != nil {
			log.Warnw("Failed to create connection pool for node, will retry later",
				"nodeId", nodeID,
				"error", err,
			)
			// Don't fail - node might come online later
			dm.nodes = append(dm.nodes, &ManagedDBNode{
				Config:   cfg,
				Pool:     nil, // Will be created on reconnect
				ID:       nodeID,
				Priority: cfg.Priority,
				ReadOnly: cfg.ReadOnly,
				State:    DBNodeOffline,
			})
			continue
		}

		// Test connection
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = pool.Ping(ctx)
		cancel()

		state := DBNodeHealthy
		if err != nil {
			log.Warnw("Database node ping failed, marking as unhealthy",
				"nodeId", nodeID,
				"error", err,
			)
			state = DBNodeUnhealthy
		} else {
			log.Infow("✅ Connected to database node",
				"nodeId", nodeID,
				"host", cfg.Host,
				"priority", cfg.Priority,
				"readOnly", cfg.ReadOnly,
			)
		}

		dm.nodes = append(dm.nodes, &ManagedDBNode{
			Config:      cfg,
			Pool:        pool,
			ID:          nodeID,
			Priority:    cfg.Priority,
			ReadOnly:    cfg.ReadOnly,
			State:       state,
			LastSuccess: time.Now(),
		})
	}

	// Find initial active node (lowest priority healthy write node)
	activeIdx := dm.findBestWriteNode()
	if activeIdx < 0 {
		return nil, fmt.Errorf("no healthy write-capable database nodes available: shares cannot be persisted. Check PostgreSQL connectivity and credentials for all configured nodes")
	}
	dm.activeNodeIdx.Store(int32(activeIdx))

	log.Infow("Database manager initialized",
		"totalNodes", len(dm.nodes),
		"activeNode", dm.nodes[activeIdx].ID,
	)

	return dm, nil
}

// SetDBMetrics sets the Prometheus metrics interface for database failover observability.
func (dm *DatabaseManager) SetDBMetrics(m dbMetrics) {
	dm.promMetrics = m
}

// Start begins health monitoring and failover management
func (dm *DatabaseManager) Start(ctx context.Context) {
	ctx, dm.cancelFunc = context.WithCancel(ctx)
	dm.running.Store(true)

	dm.wg.Add(1)
	go dm.healthCheckLoop(ctx)

	dm.logger.Info("Database manager started")
}

// Stop gracefully shuts down the database manager
func (dm *DatabaseManager) Stop() {
	dm.running.Store(false)
	if dm.cancelFunc != nil {
		dm.cancelFunc() // Cancel context so health check loop exits immediately
	}
	dm.wg.Wait()

	// Close all pools
	for _, node := range dm.nodes {
		if node.Pool != nil {
			node.Pool.Close()
		}
	}

	dm.logger.Info("Database manager stopped")
}

// WriteBatch writes shares to the active database node with failover
func (dm *DatabaseManager) WriteBatch(ctx context.Context, shares []*protocol.Share) error {
	if len(shares) == 0 {
		return nil
	}

	// Try active node first
	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		return fmt.Errorf("active node index %d out of bounds (have %d nodes)", activeIdx, len(dm.nodes))
	}
	node := dm.nodes[activeIdx]

	err := dm.writeBatchToNode(ctx, node, shares)
	if err == nil {
		return nil
	}

	// Active node failed - try failover
	dm.logger.Warnw("Active database node failed, attempting failover",
		"activeNode", node.ID,
		"error", err,
	)

	// Record failure and try other nodes
	dm.recordNodeFailure(node, err)
	dm.writeFailures.Add(1)

	// Find next best write node — use fresh context for failover attempts
	// to avoid inheriting a potentially short/expired deadline from the caller
	failoverCtx, failoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer failoverCancel()

	for i, n := range dm.nodes {
		if i == activeIdx || n.ReadOnly || n.State == DBNodeOffline {
			continue
		}

		err = dm.writeBatchToNode(failoverCtx, n, shares)
		if err == nil {
			// Failover successful
			dm.activeNodeIdx.Store(int32(i))
			dm.failovers.Add(1)
			if dm.promMetrics != nil {
				dm.promMetrics.IncDBFailover()
				dm.promMetrics.SetDBActiveNode(n.ID)
			}
			dm.logger.Infow("✅ Database failover successful",
				"fromNode", node.ID,
				"toNode", n.ID,
			)
			return nil
		}

		dm.recordNodeFailure(n, err)
	}

	return fmt.Errorf("all database nodes failed, shares lost: %w. Check database connectivity immediately", err)
}

// writeBatchToNode writes shares to a specific node
// CRITICAL: Enforces single-writer invariant via pg_is_in_recovery() check (GATE Ω1 fix)
func (dm *DatabaseManager) writeBatchToNode(ctx context.Context, node *ManagedDBNode, shares []*protocol.Share) error {
	if node.Pool == nil {
		return fmt.Errorf("node %s has no connection pool", node.ID)
	}

	// GATE Ω1: Single-writer enforcement - verify this node is actually a PRIMARY
	// before attempting writes. This prevents split-brain scenarios where both
	// nodes believe they are masters and write to their own databases.
	var isInRecovery bool
	err := node.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery)
	if err != nil {
		return fmt.Errorf("failed to check pg_is_in_recovery on %s: %w (single-writer check failed)", node.ID, err)
	}
	if isInRecovery {
		return fmt.Errorf("CRITICAL: node %s is in recovery mode (standby) - refusing write to enforce single-writer invariant", node.ID)
	}

	tableName := fmt.Sprintf("shares_%s", dm.poolID)

	start := time.Now()
	_, err = node.Pool.CopyFrom(
		ctx,
		pgx.Identifier{tableName},
		[]string{
			"poolid", "blockheight", "difficulty", "networkdifficulty",
			"miner", "worker", "useragent", "ipaddress", "source", "created",
		},
		pgx.CopyFromSlice(len(shares), func(i int) ([]interface{}, error) {
			s := shares[i]
			return []interface{}{
				dm.poolID,
				s.BlockHeight,
				s.Difficulty,
				s.NetworkDiff,
				s.MinerAddress,
				s.WorkerName,
				s.UserAgent,
				s.IPAddress,
				"stratum",
				s.SubmittedAt,
			}, nil
		}),
	)

	if err != nil {
		return fmt.Errorf("failed to copy shares to %s: %w", node.ID, err)
	}

	// Record success
	dm.recordNodeSuccess(node, time.Since(start))

	dm.logger.Debugw("Wrote share batch",
		"node", node.ID,
		"count", len(shares),
		"duration", time.Since(start).Round(time.Millisecond),
	)

	return nil
}

// InsertBlock inserts a block record to the active node with failover
func (dm *DatabaseManager) InsertBlock(ctx context.Context, block *Block) error {
	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		return fmt.Errorf("active node index %d out of bounds (have %d nodes)", activeIdx, len(dm.nodes))
	}
	node := dm.nodes[activeIdx]

	err := dm.insertBlockToNode(ctx, node, block)
	if err == nil {
		return nil
	}

	// Try failover — use fresh context to avoid inheriting caller's expired deadline.
	// Block insertions are financially critical; we must give failover the best chance.
	dm.recordNodeFailure(node, err)

	failoverCtx, failoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer failoverCancel()

	for i, n := range dm.nodes {
		if i == activeIdx || n.ReadOnly || n.State == DBNodeOffline {
			continue
		}

		err = dm.insertBlockToNode(failoverCtx, n, block)
		if err == nil {
			dm.activeNodeIdx.Store(int32(i))
			dm.failovers.Add(1)
			if dm.promMetrics != nil {
				dm.promMetrics.IncDBFailover()
				dm.promMetrics.SetDBActiveNode(n.ID)
			}
			dm.logger.Infow("✅ Database failover successful during block insert",
				"fromNode", node.ID,
				"toNode", n.ID,
			)
			return nil
		}

		dm.recordNodeFailure(n, err)
	}

	return fmt.Errorf("CRITICAL: block insertion failed on all nodes, block may be lost: %w. Check database connectivity immediately", err)
}

// insertBlockToNode inserts a block to a specific node
// CRITICAL: Enforces single-writer invariant via pg_is_in_recovery() check (GATE Ω1 fix)
func (dm *DatabaseManager) insertBlockToNode(ctx context.Context, node *ManagedDBNode, block *Block) error {
	if node.Pool == nil {
		return fmt.Errorf("node %s has no connection pool", node.ID)
	}

	// GATE Ω1: Single-writer enforcement for blocks - even more critical than shares
	// as block records affect financial accounting
	var isInRecovery bool
	if err := node.Pool.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery); err != nil {
		return fmt.Errorf("failed to check pg_is_in_recovery on %s: %w (single-writer check failed)", node.ID, err)
	}
	if isInRecovery {
		return fmt.Errorf("CRITICAL: node %s is in recovery mode (standby) - refusing block write to enforce single-writer invariant", node.ID)
	}

	tableName := fmt.Sprintf("blocks_%s", dm.poolID)

	query := fmt.Sprintf(`
		INSERT INTO %s (
			poolid, blockheight, networkdifficulty, status, type,
			confirmationprogress, effort, transactionconfirmationdata,
			miner, reward, source, hash, created
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, tableName)

	start := time.Now()
	_, err := node.Pool.Exec(ctx, query,
		dm.poolID,
		block.Height,
		block.NetworkDifficulty,
		block.Status,
		block.Type,
		block.ConfirmationProgress,
		block.Effort,
		block.TransactionConfirmationData,
		block.Miner,
		block.Reward,
		"stratum",
		block.Hash,
		block.Created,
	)

	if err != nil {
		return fmt.Errorf("failed to insert block: %w", err)
	}

	dm.recordNodeSuccess(node, time.Since(start))

	dm.logger.Infow("Recorded block",
		"node", node.ID,
		"height", block.Height,
		"hash", block.Hash,
		"miner", block.Miner,
	)

	return nil
}

// Close closes all database connections
func (dm *DatabaseManager) Close() error {
	dm.Stop()
	return nil
}

// GetPoolHashrate calculates the pool hashrate from recent shares (uses active node)
func (dm *DatabaseManager) GetPoolHashrate(ctx context.Context, windowMinutes int) (float64, error) {
	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		return 0, fmt.Errorf("active node index %d out of bounds (have %d nodes)", activeIdx, len(dm.nodes))
	}
	node := dm.nodes[activeIdx]

	if node.Pool == nil {
		return 0, fmt.Errorf("no active database connection")
	}

	tableName := fmt.Sprintf("shares_%s", dm.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(difficulty), 0) as total_difficulty
		FROM %s
		WHERE created > NOW() - ($1 * INTERVAL '1 minute')
	`, tableName)

	var totalDiff float64
	err := node.Pool.QueryRow(ctx, query, windowMinutes).Scan(&totalDiff)
	if err != nil {
		dm.recordNodeFailure(node, err)
		return 0, err
	}

	dm.recordNodeSuccess(node, 0)

	// Convert to hashrate using algorithm-aware formula
	hashrate := coin.CalculateHashrateForAlgorithm(totalDiff, float64(windowMinutes*60), dm.algorithm)
	return hashrate, nil
}

// GetPoolHashrateSince calculates pool hashrate from shares since a specific time.
// This is used to get accurate hashrate after server restart.
func (dm *DatabaseManager) GetPoolHashrateSince(ctx context.Context, since time.Time, windowMinutes int) (float64, error) {
	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		return 0, fmt.Errorf("active node index %d out of bounds (have %d nodes)", activeIdx, len(dm.nodes))
	}
	node := dm.nodes[activeIdx]

	if node.Pool == nil {
		return 0, fmt.Errorf("no active database connection")
	}

	tableName := fmt.Sprintf("shares_%s", dm.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(difficulty), 0) as total_difficulty,
		       EXTRACT(EPOCH FROM (NOW() - LEAST($1, NOW() - ($2 * INTERVAL '1 minute')))) as elapsed_seconds
		FROM %s
		WHERE created > GREATEST($1, NOW() - ($2 * INTERVAL '1 minute'))
	`, tableName)

	var totalDiff float64
	var elapsedSeconds float64
	err := node.Pool.QueryRow(ctx, query, since, windowMinutes).Scan(&totalDiff, &elapsedSeconds)
	if err != nil {
		dm.recordNodeFailure(node, err)
		return 0, err
	}

	dm.recordNodeSuccess(node, 0)

	if elapsedSeconds < 1 {
		elapsedSeconds = 1
	}

	hashrate := coin.CalculateHashrateForAlgorithm(totalDiff, elapsedSeconds, dm.algorithm)
	return hashrate, nil
}

// CleanupStaleShares removes shares older than the specified retention period.
func (dm *DatabaseManager) CleanupStaleShares(ctx context.Context, retentionMinutes int) (int64, error) {
	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		return 0, fmt.Errorf("active node index %d out of bounds (have %d nodes)", activeIdx, len(dm.nodes))
	}
	node := dm.nodes[activeIdx]

	if node.Pool == nil {
		return 0, fmt.Errorf("no active database connection")
	}

	tableName := fmt.Sprintf("shares_%s", dm.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE created < NOW() - ($1 * INTERVAL '1 minute')
	`, tableName)

	result, err := node.Pool.Exec(ctx, query, retentionMinutes)
	if err != nil {
		dm.recordNodeFailure(node, err)
		return 0, err
	}

	dm.recordNodeSuccess(node, 0)
	return result.RowsAffected(), nil
}

// UpdatePoolStats updates pool statistics (uses active node)
func (dm *DatabaseManager) UpdatePoolStats(ctx context.Context, stats *PoolStats) error {
	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		return fmt.Errorf("active node index %d out of bounds (have %d nodes)", activeIdx, len(dm.nodes))
	}
	node := dm.nodes[activeIdx]

	if node.Pool == nil {
		return fmt.Errorf("no active database connection")
	}

	query := `
		INSERT INTO poolstats (
			poolid, connectedminers, poolhashrate, sharespersecond,
			networkhashrate, networkdifficulty, lastnetworkblocktime,
			blockheight, connectedpeers, created
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	start := time.Now()
	_, err := node.Pool.Exec(ctx, query,
		dm.poolID,
		stats.ConnectedMiners,
		stats.PoolHashrate,
		stats.SharesPerSecond,
		stats.NetworkHashrate,
		stats.NetworkDifficulty,
		stats.LastNetworkBlockTime,
		stats.BlockHeight,
		stats.ConnectedPeers,
		time.Now(),
	)

	if err != nil {
		dm.recordNodeFailure(node, err)
		return err
	}

	dm.recordNodeSuccess(node, time.Since(start))
	return nil
}

// recordNodeSuccess records a successful operation on a node
func (dm *DatabaseManager) recordNodeSuccess(node *ManagedDBNode, responseTime time.Duration) {
	node.mu.Lock()
	defer node.mu.Unlock()

	node.LastSuccess = time.Now()
	node.LastError = nil
	node.ConsecutiveFails = 0

	// Update rolling average response time
	if node.ResponseTimeAvg == 0 {
		node.ResponseTimeAvg = responseTime
	} else {
		const alpha = 0.2
		node.ResponseTimeAvg = time.Duration(alpha*float64(responseTime) + (1-alpha)*float64(node.ResponseTimeAvg))
	}

	if node.State != DBNodeHealthy {
		node.State = DBNodeHealthy
		dm.logger.Infow("✅ Database node recovered",
			"nodeId", node.ID,
		)
	}
}

// recordNodeFailure records a failed operation on a node
func (dm *DatabaseManager) recordNodeFailure(node *ManagedDBNode, err error) {
	node.mu.Lock()
	defer node.mu.Unlock()

	node.LastError = err
	node.ConsecutiveFails++

	if node.ConsecutiveFails >= MaxDBNodeFailures {
		if node.State != DBNodeUnhealthy {
			node.State = DBNodeUnhealthy
			dm.logger.Errorw("🚨 Database node marked unhealthy",
				"nodeId", node.ID,
				"consecutiveFailures", node.ConsecutiveFails,
				"lastError", err,
			)
		}
	} else if node.State == DBNodeHealthy {
		node.State = DBNodeDegraded
		dm.logger.Warnw("⚠️ Database node degraded",
			"nodeId", node.ID,
			"consecutiveFailures", node.ConsecutiveFails,
		)
	}
}

// findBestWriteNode finds the best healthy write-capable node
func (dm *DatabaseManager) findBestWriteNode() int {
	bestIdx := -1
	bestPriority := int(^uint(0) >> 1) // Max int

	for i, node := range dm.nodes {
		if node.ReadOnly || node.Pool == nil {
			continue
		}

		node.mu.RLock()
		state := node.State
		priority := node.Priority
		node.mu.RUnlock()

		if state == DBNodeHealthy || state == DBNodeDegraded {
			if priority < bestPriority {
				bestPriority = priority
				bestIdx = i
			}
		}
	}

	return bestIdx
}

// healthCheckLoop periodically checks node health
func (dm *DatabaseManager) healthCheckLoop(ctx context.Context) {
	defer dm.wg.Done()

	ticker := time.NewTicker(DBHealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !dm.running.Load() {
				return
			}
			dm.checkAllNodes(ctx)
		}
	}
}

// checkAllNodes checks health of all database nodes
func (dm *DatabaseManager) checkAllNodes(ctx context.Context) {
	for _, node := range dm.nodes {
		dm.checkNode(ctx, node)
	}

	// Check if we need to fail back to a higher priority node
	dm.checkFailback()
}

// checkNode performs a health check on a single node
func (dm *DatabaseManager) checkNode(ctx context.Context, node *ManagedDBNode) {
	// Try to reconnect offline nodes
	if node.Pool == nil {
		dm.attemptReconnect(ctx, node)
		return
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	start := time.Now()
	err := node.Pool.Ping(checkCtx)

	node.mu.Lock()
	node.LastHealthCheck = time.Now()
	node.mu.Unlock()

	if err != nil {
		dm.recordNodeFailure(node, err)
	} else {
		dm.recordNodeSuccess(node, time.Since(start))
	}
}

// attemptReconnect tries to reconnect an offline node
func (dm *DatabaseManager) attemptReconnect(ctx context.Context, node *ManagedDBNode) {
	poolConfig, err := pgxpool.ParseConfig(nodeConnectionString(&node.Config))
	if err != nil {
		dm.logger.Warnw("Failed to parse connection config for reconnect",
			"nodeId", node.ID, "error", err)
		return
	}

	poolConfig.MaxConns = int32(node.Config.MaxConnections)
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = 1 * time.Hour
	poolConfig.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		dm.logger.Warnw("Failed to create connection pool for reconnect",
			"nodeId", node.ID, "error", err)
		return
	}

	// Test connection
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = pool.Ping(checkCtx)
	cancel()

	if err != nil {
		pool.Close()
		return
	}

	// Reconnection successful — close any existing pool first to prevent leak
	node.mu.Lock()
	if node.Pool != nil {
		node.Pool.Close()
	}
	node.Pool = pool
	node.State = DBNodeHealthy
	node.ConsecutiveFails = 0
	node.LastSuccess = time.Now()
	node.LastError = nil
	node.mu.Unlock()

	dm.logger.Infow("✅ Database node reconnected",
		"nodeId", node.ID,
	)
}

// checkFailback checks if we should fail back to a higher priority node
func (dm *DatabaseManager) checkFailback() {
	currentIdx := int(dm.activeNodeIdx.Load())
	if currentIdx < 0 || currentIdx >= len(dm.nodes) {
		return
	}
	currentNode := dm.nodes[currentIdx]

	bestIdx := dm.findBestWriteNode()
	if bestIdx < 0 || bestIdx == currentIdx {
		return
	}

	bestNode := dm.nodes[bestIdx]

	// Only failback if best node has higher priority (lower number)
	if bestNode.Priority < currentNode.Priority {
		// Verify the target is actually a writable primary before switching
		if bestNode.Pool != nil {
			checkCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var isInRecovery bool
			if err := bestNode.Pool.QueryRow(checkCtx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery); err != nil || isInRecovery {
				dm.logger.Warnw("Failback target is not a writable primary, skipping",
					"targetNode", bestNode.ID,
					"isInRecovery", isInRecovery,
				)
				return // Target node is not a writable primary, skip failback
			}
		}

		dm.activeNodeIdx.Store(int32(bestIdx))
		dm.failovers.Add(1)
		if dm.promMetrics != nil {
			dm.promMetrics.IncDBFailover()
			dm.promMetrics.SetDBActiveNode(bestNode.ID)
		}
		dm.logger.Infow("Failing back to higher priority database node",
			"fromNode", currentNode.ID,
			"toNode", bestNode.ID,
			"fromPriority", currentNode.Priority,
			"toPriority", bestNode.Priority,
		)
	}
}

// Stats returns database manager statistics
type DBManagerStats struct {
	ActiveNode    string
	TotalNodes    int
	HealthyNodes  int
	Failovers     uint64
	WriteFailures uint64
	ReadFailures  uint64
}

func (dm *DatabaseManager) Stats() DBManagerStats {
	healthy := 0
	for _, node := range dm.nodes {
		node.mu.RLock()
		if node.State == DBNodeHealthy {
			healthy++
		}
		node.mu.RUnlock()
	}

	activeNodeID := "unknown"
	if node := dm.safeActiveNode(); node != nil {
		activeNodeID = node.ID
	}
	return DBManagerStats{
		ActiveNode:    activeNodeID,
		TotalNodes:    len(dm.nodes),
		HealthyNodes:  healthy,
		Failovers:     dm.failovers.Load(),
		WriteFailures: dm.writeFailures.Load(),
		ReadFailures:  dm.readFailures.Load(),
	}
}

// safeActiveNode returns the active node with bounds checking.
// Returns nil if the index is out of bounds (should never happen in practice).
func (dm *DatabaseManager) safeActiveNode() *ManagedDBNode {
	idx := int(dm.activeNodeIdx.Load())
	if idx < 0 || idx >= len(dm.nodes) {
		return nil
	}
	return dm.nodes[idx]
}

// GetActiveNode returns the current active node for monitoring
func (dm *DatabaseManager) GetActiveNode() *ManagedDBNode {
	return dm.safeActiveNode()
}

// VIPFailoverCallback returns a callback function for VIP role changes.
// This enables automatic database failover synchronized with VIP ownership.
//
// When isMaster=true: This node became VIP master, so prefer local database (priority 0)
// When isMaster=false: This node is backup, so connect to the master's database
//
// The callback includes debouncing to prevent rapid failover oscillation.
func (dm *DatabaseManager) VIPFailoverCallback() func(isMaster bool) {
	return func(isMaster bool) {
		// Debounce: Ignore rapid failover requests (within 5 seconds).
		// Uses dm.vipFailoverDebounce (shared field) so the timer persists across calls.
		now := time.Now().Unix()
		last := dm.vipFailoverDebounce.Load()
		if now-last < 5 {
			dm.logger.Debugw("Database failover debounced (too soon)",
				"isMaster", isMaster,
				"secondsSinceLast", now-last,
			)
			return
		}
		dm.vipFailoverDebounce.Store(now)

		dm.logger.Infow("VIP role change triggered database failover check",
			"isMaster", isMaster,
		)

		if isMaster {
			// We became VIP master - prefer local database (lowest priority node)
			localIdx := dm.findLocalNode()
			if localIdx >= 0 && localIdx < len(dm.nodes) {
				localNode := dm.nodes[localIdx]
				// Verify local DB is a writable primary before switching.
				// During failover, VIP may arrive before Patroni promotes this node.
				// Without this check, writes would hit a read-only replica and fail.
				if localNode.Pool != nil {
					checkCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					var isInRecovery bool
					err := localNode.Pool.QueryRow(checkCtx, "SELECT pg_is_in_recovery()").Scan(&isInRecovery)
					cancel()
					if err != nil || isInRecovery {
						dm.logger.Warnw("Local DB is not a writable primary yet — skipping switch (Patroni promotion may be in progress)",
							"localNode", localNode.ID,
							"isInRecovery", isInRecovery,
							"error", err,
						)
						return
					}
				}
				currentIdx := int(dm.activeNodeIdx.Load())
				if currentIdx != localIdx && currentIdx >= 0 && currentIdx < len(dm.nodes) {
					dm.activeNodeIdx.Store(int32(localIdx))
					dm.failovers.Add(1)
					if dm.promMetrics != nil {
						dm.promMetrics.IncDBFailover()
						dm.promMetrics.SetDBActiveNode(dm.nodes[localIdx].ID)
					}
					dm.logger.Infow("✅ Database switched to local node (VIP master)",
						"fromNode", dm.nodes[currentIdx].ID,
						"toNode", dm.nodes[localIdx].ID,
					)
				}
			}
		} else {
			// We became backup - find the best remote node (the new master's database)
			bestIdx := dm.findBestWriteNode()
			if bestIdx >= 0 && bestIdx < len(dm.nodes) {
				currentIdx := int(dm.activeNodeIdx.Load())
				if currentIdx != bestIdx && currentIdx >= 0 && currentIdx < len(dm.nodes) {
					dm.activeNodeIdx.Store(int32(bestIdx))
					dm.failovers.Add(1)
					if dm.promMetrics != nil {
						dm.promMetrics.IncDBFailover()
						dm.promMetrics.SetDBActiveNode(dm.nodes[bestIdx].ID)
					}
					dm.logger.Infow("✅ Database switched to remote primary (VIP backup)",
						"fromNode", dm.nodes[currentIdx].ID,
						"toNode", dm.nodes[bestIdx].ID,
					)
				}
			}
		}
	}
}

// findLocalNode finds the node with priority 0 (local primary)
func (dm *DatabaseManager) findLocalNode() int {
	for i, node := range dm.nodes {
		if node.Priority == 0 && !node.ReadOnly && node.Pool != nil {
			node.mu.RLock()
			state := node.State
			node.mu.RUnlock()
			if state == DBNodeHealthy || state == DBNodeDegraded {
				return i
			}
		}
	}
	return -1
}

// GetActiveDB returns a PostgresDB wrapper around the active node's connection pool.
// This is used by the API server to access database methods like GetPendingBlocks.
// The returned PostgresDB uses the active node's pool and automatically follows failover.
func (dm *DatabaseManager) GetActiveDB() *PostgresDB {
	if dm == nil || len(dm.nodes) == 0 {
		return nil
	}

	activeIdx := int(dm.activeNodeIdx.Load())
	if activeIdx < 0 || activeIdx >= len(dm.nodes) {
		activeIdx = 0 // Fall back to first node
	}

	node := dm.nodes[activeIdx]
	if node == nil || node.Pool == nil {
		return nil
	}

	// Create a PostgresDB wrapper that uses the active node's pool
	// Note: This wrapper will use whatever node is currently active
	return &PostgresDB{
		pool:      node.Pool,
		logger:    dm.logger,
		poolID:    dm.poolID,
		algorithm: dm.algorithm,
	}
}

// TryAdvisoryLock acquires a PostgreSQL advisory lock on the active database node.
// The lock is pinned to a specific PostgresDB instance so that ReleaseAdvisoryLock
// releases on the same pool/session. If failover occurs mid-cycle, the old pool's
// connection dies and both lock/unlock will return errors (correct behavior — the
// caller should retry). This allows DatabaseManager to satisfy the
// payments.AdvisoryLocker interface for defense-in-depth payment fencing.
func (dm *DatabaseManager) TryAdvisoryLock(ctx context.Context, lockID int64) (bool, error) {
	dm.advisoryMu.Lock()
	defer dm.advisoryMu.Unlock()

	// Get or refresh the pinned advisory DB instance
	if dm.advisoryDB == nil {
		dm.advisoryDB = dm.GetActiveDB()
	}
	if dm.advisoryDB == nil {
		return false, fmt.Errorf("no active database connection for advisory lock")
	}
	return dm.advisoryDB.TryAdvisoryLock(ctx, lockID)
}

// ReleaseAdvisoryLock releases a PostgreSQL advisory lock on the same node where
// TryAdvisoryLock acquired it. After release, the pinned instance is cleared so
// the next lock cycle gets a fresh snapshot of the active node.
func (dm *DatabaseManager) ReleaseAdvisoryLock(ctx context.Context, lockID int64) error {
	dm.advisoryMu.Lock()
	defer dm.advisoryMu.Unlock()

	if dm.advisoryDB == nil {
		return fmt.Errorf("no active database connection for advisory lock release")
	}
	err := dm.advisoryDB.ReleaseAdvisoryLock(ctx, lockID)
	dm.advisoryDB = nil // Clear pin — next lock cycle gets fresh instance
	return err
}

// ============================================================================
// BlockStore interface delegation (enables payment processor HA failover)
// ============================================================================
// These methods delegate to the active database node, so payment processing
// automatically follows database failover without reconnection logic.

// GetPendingBlocks delegates to the active database node.
func (dm *DatabaseManager) GetPendingBlocks(ctx context.Context) ([]*Block, error) {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return nil, fmt.Errorf("no active database connection for GetPendingBlocks")
	}
	return activeDB.GetPendingBlocks(ctx)
}

// GetConfirmedBlocks delegates to the active database node.
func (dm *DatabaseManager) GetConfirmedBlocks(ctx context.Context) ([]*Block, error) {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return nil, fmt.Errorf("no active database connection for GetConfirmedBlocks")
	}
	return activeDB.GetConfirmedBlocks(ctx)
}

// GetBlocksByStatus delegates to the active database node.
func (dm *DatabaseManager) GetBlocksByStatus(ctx context.Context, status string) ([]*Block, error) {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return nil, fmt.Errorf("no active database connection for GetBlocksByStatus")
	}
	return activeDB.GetBlocksByStatus(ctx, status)
}

// UpdateBlockStatus delegates to the active database node.
func (dm *DatabaseManager) UpdateBlockStatus(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64) error {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return fmt.Errorf("no active database connection for UpdateBlockStatus")
	}
	return activeDB.UpdateBlockStatus(ctx, height, hash, status, confirmationProgress)
}

// UpdateBlockOrphanCount delegates to the active database node.
func (dm *DatabaseManager) UpdateBlockOrphanCount(ctx context.Context, height uint64, hash string, mismatchCount int) error {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return fmt.Errorf("no active database connection for UpdateBlockOrphanCount")
	}
	return activeDB.UpdateBlockOrphanCount(ctx, height, hash, mismatchCount)
}

// UpdateBlockStabilityCount delegates to the active database node.
func (dm *DatabaseManager) UpdateBlockStabilityCount(ctx context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return fmt.Errorf("no active database connection for UpdateBlockStabilityCount")
	}
	return activeDB.UpdateBlockStabilityCount(ctx, height, hash, stabilityCount, lastTip)
}

// GetBlockStats delegates to the active database node.
func (dm *DatabaseManager) GetBlockStats(ctx context.Context) (*BlockStats, error) {
	activeDB := dm.GetActiveDB()
	if activeDB == nil {
		return nil, fmt.Errorf("no active database connection for GetBlockStats")
	}
	return activeDB.GetBlockStats(ctx)
}
