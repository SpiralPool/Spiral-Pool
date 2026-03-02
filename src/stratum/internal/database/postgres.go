// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package database provides PostgreSQL persistence for mining pool data.
//
// The database schema stores mining shares, blocks, workers, and pool statistics
// using standard relational patterns for high-throughput write workloads.
package database

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// validPoolID matches valid pool identifiers (alphanumeric and underscores, 1-63 chars)
// SECURITY: Used to prevent SQL injection in table names like shares_<poolID>
var validPoolID = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// ErrStatusGuardBlocked is returned when UpdateBlockStatus affected 0 rows because
// the V12 status guard WHERE clause rejected the transition, or the block does not exist.
// Callers can check errors.Is(err, ErrStatusGuardBlocked) to distinguish this from real
// database errors. In HA mode, this is expected when a stale backup process attempts to
// update a block that the master already advanced.
var ErrStatusGuardBlocked = errors.New("block status update blocked: 0 rows affected (status guard or missing block)")

// V26 FIX: OnStatusGuardRejection is called when a block status update is
// blocked by the V12 status guard (RowsAffected == 0 due to WHERE clause guard).
// This callback enables Prometheus metric tracking without creating an import
// cycle between database and metrics packages.
// Wire this in pool startup: database.OnStatusGuardRejection = func() { metrics.Inc() }
var onStatusGuardRejectionMu sync.Mutex
var onStatusGuardRejection func()

func SetOnStatusGuardRejection(fn func()) {
	onStatusGuardRejectionMu.Lock()
	onStatusGuardRejection = fn
	onStatusGuardRejectionMu.Unlock()
}

func CallOnStatusGuardRejection() {
	onStatusGuardRejectionMu.Lock()
	fn := onStatusGuardRejection
	onStatusGuardRejectionMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Database defines the interface for database operations.
// Both PostgresDB (single node) and DatabaseManager (HA failover) implement this.
type Database interface {
	// WriteBatch writes a batch of shares to the database
	WriteBatch(ctx context.Context, shares []*protocol.Share) error
	// InsertBlock records a newly found block
	InsertBlock(ctx context.Context, block *Block) error
	// GetPoolHashrate calculates the pool hashrate from recent shares
	GetPoolHashrate(ctx context.Context, windowMinutes int) (float64, error)
	// UpdatePoolStats updates pool statistics
	UpdatePoolStats(ctx context.Context, stats *PoolStats) error
	// Close closes the database connection(s)
	Close() error
}

// PostgresDB handles all database operations.
type PostgresDB struct {
	pool      *pgxpool.Pool
	cfg       *config.DatabaseConfig
	logger    *zap.SugaredLogger
	poolID    string
	algorithm string // Mining algorithm ("sha256d" or "scrypt") for hashrate calculations

	// advisoryConn is a dedicated connection for session-level advisory locks.
	// pg_try_advisory_lock is bound to the specific PostgreSQL session (connection)
	// that acquired it. Using pool.QueryRow() would grab a random connection from
	// the pool, causing lock and unlock to hit different sessions — the unlock
	// becomes a no-op and the lock is permanently stuck. We acquire a dedicated
	// connection on lock and release it on unlock to guarantee same-session semantics.
	advisoryMu   sync.Mutex
	advisoryConn *pgxpool.Conn
}

// NewPostgresDB creates a new PostgreSQL database connection.
func NewPostgresDB(cfg *config.DatabaseConfig, poolID string, algorithm string, logger *zap.Logger) (*PostgresDB, error) {
	// SECURITY: Validate poolID to prevent SQL injection in table names
	if !validPoolID.MatchString(poolID) {
		return nil, fmt.Errorf("invalid pool ID: %q (must be alphanumeric/underscore, 1-63 chars, start with letter/underscore)", poolID)
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	poolConfig.MaxConns = int32(cfg.MaxConnections)
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 1 * time.Hour
	poolConfig.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test connection
	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Default to SHA-256d if algorithm not specified
	if algorithm == "" {
		algorithm = "sha256d"
	}

	db := &PostgresDB{
		pool:      pool,
		cfg:       cfg,
		logger:    logger.Sugar(),
		poolID:    poolID,
		algorithm: algorithm,
	}

	return db, nil
}

// WithPoolID returns a new PostgresDB that shares the same underlying connection pool
// but scopes all table-name-based queries (blocks, shares, workers) to a different poolID.
// The returned instance must NOT call Close() — only the original owner closes the pool.
// This is used by the V2 coordinator to create per-coin payment processors that each
// query their own pool-specific block tables.
func (db *PostgresDB) WithPoolID(poolID string) *PostgresDB {
	if !validPoolID.MatchString(poolID) {
		// SECURITY: Fail-safe — return a "poisoned" PostgresDB with an empty poolID.
		// Any table name interpolation (e.g., "shares_" + "") will produce invalid SQL
		// like "shares_" which will fail loudly at query time rather than silently
		// operating on the original pool's tables. This prevents data cross-contamination
		// between pools when an invalid poolID is provided.
		// We do NOT return the original db because that would silently use the wrong pool.
		db.logger.Errorw("WithPoolID: invalid pool ID, returning poisoned instance that will fail on queries",
			"poolID", poolID,
		)
		return &PostgresDB{
			pool:   db.pool,
			cfg:    db.cfg,
			logger: db.logger,
			poolID: "", // Empty poolID — queries will produce invalid table names and fail
		}
	}
	return &PostgresDB{
		pool:   db.pool,
		cfg:    db.cfg,
		logger: db.logger,
		poolID: poolID,
	}
}

// Ping checks database connectivity. Used by health checks to detect
// DB failures that the circuit breaker alone may not surface.
func (db *PostgresDB) Ping(ctx context.Context) error {
	return db.pool.Ping(ctx)
}

// Close closes the database connection pool.
func (db *PostgresDB) Close() error {
	db.pool.Close()
	return nil
}

// WriteBatch writes a batch of shares to the database using COPY for efficiency.
// This implements the ShareWriter interface from the shares package.
func (db *PostgresDB) WriteBatch(ctx context.Context, shares []*protocol.Share) error {
	if len(shares) == 0 {
		return nil
	}

	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// Use COPY for maximum throughput
	_, err := db.pool.CopyFrom(
		ctx,
		pgx.Identifier{tableName},
		[]string{
			"poolid", "blockheight", "difficulty", "networkdifficulty",
			"miner", "worker", "useragent", "ipaddress", "source", "created",
		},
		pgx.CopyFromSlice(len(shares), func(i int) ([]interface{}, error) {
			s := shares[i]
			return []interface{}{
				db.poolID,
				s.BlockHeight,
				s.Difficulty,
				s.NetworkDiff,
				s.MinerAddress,
				s.WorkerName,
				s.UserAgent,
				s.IPAddress,
				"stratum", // source
				s.SubmittedAt,
			}, nil
		}),
	)

	if err != nil {
		return fmt.Errorf("failed to copy shares: %w", err)
	}

	db.logger.Debugw("Wrote share batch",
		"count", len(shares),
	)

	return nil
}

// InsertBlock records a newly found block.
// Uses PostgreSQL advisory locks to prevent duplicate insertion in HA scenarios
// where multiple stratum servers might try to record the same block.
func (db *PostgresDB) InsertBlock(ctx context.Context, block *Block) error {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// Begin a transaction to hold the advisory lock
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Acquire advisory lock based on block height to prevent concurrent insertion
	// Lock key is a hash of pool_id + block_height to ensure uniqueness per pool
	lockKey := int64(block.Height) ^ int64(hash(db.poolID))
	var lockAcquired bool
	err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lockKey).Scan(&lockAcquired)
	if err != nil {
		return fmt.Errorf("failed to acquire advisory lock: %w", err)
	}

	if !lockAcquired {
		// Another transaction is inserting this block - skip to avoid duplicate
		db.logger.Warnw("Block insertion skipped - another process is inserting",
			"height", block.Height,
			"hash", block.Hash,
		)
		return nil
	}

	// Check if block already exists (idempotency check)
	var exists bool
	checkQuery := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE hash = $1)", tableName)
	err = tx.QueryRow(ctx, checkQuery, block.Hash).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check block existence: %w", err)
	}

	if exists {
		db.logger.Infow("Block already recorded, skipping duplicate",
			"height", block.Height,
			"hash", block.Hash,
		)
		return nil
	}

	// Insert the block
	query := fmt.Sprintf(`
		INSERT INTO %s (
			poolid, blockheight, networkdifficulty, status, type,
			confirmationprogress, effort, transactionconfirmationdata,
			miner, reward, source, hash, created
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, tableName)

	_, err = tx.Exec(ctx, query,
		db.poolID,
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

	// Commit the transaction (releases advisory lock automatically)
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit block insertion: %w", err)
	}

	db.logger.Infow("Recorded block",
		"height", block.Height,
		"hash", block.Hash,
		"miner", block.Miner,
	)

	return nil
}

// hash generates a simple hash for pool ID to create unique lock keys.
func hash(s string) uint32 {
	var h uint32
	for i := 0; i < len(s); i++ {
		h = 31*h + uint32(s[i])
	}
	return h
}

// UpdateBlockStatus updates the status of a block.
// V1 FIX: Uses both blockheight AND hash in the WHERE clause to prevent
// cross-contamination when multiple blocks exist at the same height (fork).
func (db *PostgresDB) UpdateBlockStatus(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64) error {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// V12 FIX: Status guard prevents multi-instance race where a stale process
	// demotes a confirmed block back to pending. Valid transitions:
	//   submitting → pending/confirmed/orphaned (initial submission)
	//   pending → pending/confirmed/orphaned (normal lifecycle)
	//   confirmed → orphaned/paid (deep reorg or payment)
	// Blocked: orphaned → anything, paid → anything (terminal states)
	// FIX: Added 'submitting' — blocks start in this state after WAL commit
	// and must transition to pending. Without this, the confirmation cycle
	// could never advance a block past submitting status.
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = $1, confirmationprogress = $2
		WHERE blockheight = $3 AND hash = $4
		AND (
			status = 'submitting'
			OR status = 'pending'
			OR (status = 'confirmed' AND $1 IN ('orphaned', 'paid'))
		)
	`, tableName)

	// V6 FIX: Check RowsAffected to detect silent no-ops if block row disappears.
	result, err := db.pool.Exec(ctx, query, status, confirmationProgress, height, hash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		db.logger.Warnw("UpdateBlockStatus affected 0 rows - block may not exist or status guard prevented demotion",
			"height", height,
			"hash", hash,
			"attemptedStatus", status,
		)
		// V26 FIX: Notify metrics of status guard rejection
		CallOnStatusGuardRejection()
		return ErrStatusGuardBlocked
	}
	return nil
}

// UpdateBlockOrphanCount updates the orphan mismatch counter for delayed orphaning.
// CRITICAL FIX: Prevents false orphaning by requiring multiple consecutive mismatches.
// V1 FIX: Uses both blockheight AND hash to prevent cross-contamination at same height.
func (db *PostgresDB) UpdateBlockOrphanCount(ctx context.Context, height uint64, hash string, mismatchCount int) error {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// Note: If orphan_mismatch_count column doesn't exist, this will fail.
	// Run migration: ALTER TABLE blocks_xxx ADD COLUMN orphan_mismatch_count INTEGER DEFAULT 0;
	// V12 FIX: Only update orphan count for pending blocks. If a block is already
	// confirmed/orphaned/paid, orphan counting is irrelevant and a stale process
	// should not modify it.
	query := fmt.Sprintf(`
		UPDATE %s
		SET orphan_mismatch_count = $1
		WHERE blockheight = $2 AND hash = $3
		AND status = 'pending'
	`, tableName)

	// V6 FIX: Check RowsAffected to detect silent no-ops if block row disappears.
	result, err := db.pool.Exec(ctx, query, mismatchCount, height, hash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		db.logger.Warnw("UpdateBlockOrphanCount affected 0 rows - block may not exist or is no longer pending",
			"height", height,
			"hash", hash,
			"mismatchCount", mismatchCount,
		)
		// V26 FIX: Notify metrics of status guard rejection
		CallOnStatusGuardRejection()
	}
	return nil
}

// UpdateBlockStabilityCount updates the stability check counter and last verified tip.
// CRITICAL FIX: Prevents premature confirmation by requiring multiple stable observations.
// V1 FIX: Uses both blockheight AND hash to prevent cross-contamination at same height.
func (db *PostgresDB) UpdateBlockStabilityCount(ctx context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// Note: If columns don't exist, this will fail.
	// Run migration:
	//   ALTER TABLE blocks_xxx ADD COLUMN stability_check_count INTEGER DEFAULT 0;
	//   ALTER TABLE blocks_xxx ADD COLUMN last_verified_tip VARCHAR(64) DEFAULT '';
	// V12 FIX: Only update stability count for pending blocks. If a block is already
	// confirmed/orphaned/paid, stability counting is irrelevant and a stale process
	// should not modify it.
	query := fmt.Sprintf(`
		UPDATE %s
		SET stability_check_count = $1, last_verified_tip = $2
		WHERE blockheight = $3 AND hash = $4
		AND status = 'pending'
	`, tableName)

	// V6 FIX: Check RowsAffected to detect silent no-ops if block row disappears.
	result, err := db.pool.Exec(ctx, query, stabilityCount, lastTip, height, hash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		db.logger.Warnw("UpdateBlockStabilityCount affected 0 rows - block may not exist or is no longer pending",
			"height", height,
			"hash", hash,
			"stabilityCount", stabilityCount,
		)
		// V26 FIX: Notify metrics of status guard rejection
		CallOnStatusGuardRejection()
	}
	return nil
}

// UpdateBlockConfirmationState atomically updates all confirmation-related fields.
// FIX D-5: Wraps status, orphan count, stability count, and tip in a single transaction
// to prevent inconsistent state if process crashes mid-update.
// V1 FIX: Uses both blockheight AND hash to prevent cross-contamination at same height.
func (db *PostgresDB) UpdateBlockConfirmationState(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64, orphanMismatchCount int, stabilityCheckCount int, lastVerifiedTip string) error {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// Begin transaction for atomic update
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// V12 FIX: Status guard prevents multi-instance race (same logic as UpdateBlockStatus).
	// FIX: Added 'submitting' to match UpdateBlockStatus — blocks start in this state
	// after WAL commit and must be able to transition via the confirmation cycle.
	query := fmt.Sprintf(`
		UPDATE %s
		SET status = $1,
		    confirmationprogress = $2,
		    orphan_mismatch_count = $3,
		    stability_check_count = $4,
		    last_verified_tip = $5
		WHERE blockheight = $6 AND hash = $7
		AND (
			status = 'submitting'
			OR status = 'pending'
			OR (status = 'confirmed' AND $1 IN ('orphaned', 'paid'))
		)
	`, tableName)

	// V6 FIX: Check RowsAffected to detect silent no-ops if block row disappears.
	result, err := tx.Exec(ctx, query, status, confirmationProgress, orphanMismatchCount, stabilityCheckCount, lastVerifiedTip, height, hash)
	if err != nil {
		return fmt.Errorf("failed to update block confirmation state: %w", err)
	}
	if result.RowsAffected() == 0 {
		db.logger.Warnw("UpdateBlockConfirmationState affected 0 rows - block may not exist or status guard prevented demotion",
			"height", height,
			"hash", hash,
			"attemptedStatus", status,
		)
		// V26 FIX: Notify metrics of status guard rejection
		CallOnStatusGuardRejection()
		// BUG FIX: Return error instead of falling through to tx.Commit().
		// Previously, an empty transaction was committed successfully, and the
		// caller believed the update happened. Now callers can detect the no-op.
		return ErrStatusGuardBlocked
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetBlocksByStatus returns blocks with a specific status.
// Used for reconciliation of "submitting" blocks on startup.
func (db *PostgresDB) GetBlocksByStatus(ctx context.Context, status string) ([]*Block, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// Include all columns for consistency
	query := fmt.Sprintf(`
		SELECT id, blockheight, networkdifficulty, status, type,
			   confirmationprogress, effort, transactionconfirmationdata,
			   miner, reward, hash, created,
			   COALESCE(orphan_mismatch_count, 0) as orphan_mismatch_count,
			   COALESCE(stability_check_count, 0) as stability_check_count,
			   COALESCE(last_verified_tip, '') as last_verified_tip
		FROM %s
		WHERE status = $1
		ORDER BY blockheight ASC
	`, tableName)

	rows, err := db.pool.Query(ctx, query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*Block
	for rows.Next() {
		b := &Block{}
		err := rows.Scan(
			&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status, &b.Type,
			&b.ConfirmationProgress, &b.Effort, &b.TransactionConfirmationData,
			&b.Miner, &b.Reward, &b.Hash, &b.Created,
			&b.OrphanMismatchCount, &b.StabilityCheckCount, &b.LastVerifiedTip,
		)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating blocks by status: %w", err)
	}

	return blocks, nil
}

// GetPendingBlocks returns blocks awaiting confirmation.
// CRITICAL FIX: Now includes delayed orphaning and stability window columns
// to persist state across restarts. Without these, counters reset to 0 on restart.
func (db *PostgresDB) GetPendingBlocks(ctx context.Context) ([]*Block, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// CRITICAL FIX: Include orphan_mismatch_count, stability_check_count, last_verified_tip
	// These columns are required for delayed orphaning and stability window to work across restarts.
	// If columns don't exist, run migration:
	//   ALTER TABLE blocks_xxx ADD COLUMN orphan_mismatch_count INTEGER DEFAULT 0;
	//   ALTER TABLE blocks_xxx ADD COLUMN stability_check_count INTEGER DEFAULT 0;
	//   ALTER TABLE blocks_xxx ADD COLUMN last_verified_tip VARCHAR(64) DEFAULT '';
	query := fmt.Sprintf(`
		SELECT id, blockheight, networkdifficulty, status, type,
			   confirmationprogress, effort, transactionconfirmationdata,
			   miner, reward, hash, created,
			   COALESCE(orphan_mismatch_count, 0) as orphan_mismatch_count,
			   COALESCE(stability_check_count, 0) as stability_check_count,
			   COALESCE(last_verified_tip, '') as last_verified_tip
		FROM %s
		WHERE status = 'pending'
		ORDER BY blockheight ASC
	`, tableName)

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*Block
	for rows.Next() {
		b := &Block{}
		err := rows.Scan(
			&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status, &b.Type,
			&b.ConfirmationProgress, &b.Effort, &b.TransactionConfirmationData,
			&b.Miner, &b.Reward, &b.Hash, &b.Created,
			&b.OrphanMismatchCount, &b.StabilityCheckCount, &b.LastVerifiedTip,
		)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	// Check for errors from iteration
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pending blocks: %w", err)
	}

	return blocks, nil
}

// GetBlocks returns all non-orphaned blocks (pending + confirmed), newest first.
// Used by the blocks API to provide complete block history to the dashboard.
// Without this, confirmed blocks disappear from the API and the dashboard shows
// "Last Block Found: Never" even though blocks were found.
func (db *PostgresDB) GetBlocks(ctx context.Context) ([]*Block, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	query := fmt.Sprintf(`
		SELECT id, blockheight, networkdifficulty, status, type,
			   confirmationprogress, effort, transactionconfirmationdata,
			   miner, reward, hash, created,
			   COALESCE(orphan_mismatch_count, 0) as orphan_mismatch_count,
			   COALESCE(stability_check_count, 0) as stability_check_count,
			   COALESCE(last_verified_tip, '') as last_verified_tip
		FROM %s
		WHERE status IN ('pending', 'confirmed')
		ORDER BY blockheight DESC
	`, tableName)

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*Block
	for rows.Next() {
		b := &Block{}
		err := rows.Scan(
			&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status, &b.Type,
			&b.ConfirmationProgress, &b.Effort, &b.TransactionConfirmationData,
			&b.Miner, &b.Reward, &b.Hash, &b.Created,
			&b.OrphanMismatchCount, &b.StabilityCheckCount, &b.LastVerifiedTip,
		)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating blocks: %w", err)
	}

	return blocks, nil
}

// GetBlocksWithOrphans returns all blocks including orphaned, newest first.
// Used by the blocks API to enable Sentinel orphan detection.
// Sentinel's check_for_orphans() tracks status transitions — it needs to see
// blocks that changed from "pending"/"confirmed" to "orphaned".
// Without orphaned blocks in the response, orphan detection is blind.
// NOTE: Sentinel's check_pool_for_new_blocks() already filters out orphaned
// blocks (only alerts on "pending"/"confirmed"), so this is safe for block detection.
func (db *PostgresDB) GetBlocksWithOrphans(ctx context.Context) ([]*Block, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	query := fmt.Sprintf(`
		SELECT id, blockheight, networkdifficulty, status, type,
			   confirmationprogress, effort, transactionconfirmationdata,
			   miner, reward, hash, created,
			   COALESCE(orphan_mismatch_count, 0) as orphan_mismatch_count,
			   COALESCE(stability_check_count, 0) as stability_check_count,
			   COALESCE(last_verified_tip, '') as last_verified_tip
		FROM %s
		ORDER BY blockheight DESC
		LIMIT 200
	`, tableName)

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*Block
	for rows.Next() {
		b := &Block{}
		err := rows.Scan(
			&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status, &b.Type,
			&b.ConfirmationProgress, &b.Effort, &b.TransactionConfirmationData,
			&b.Miner, &b.Reward, &b.Hash, &b.Created,
			&b.OrphanMismatchCount, &b.StabilityCheckCount, &b.LastVerifiedTip,
		)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating blocks with orphans: %w", err)
	}

	return blocks, nil
}

// GetConfirmedBlocks returns blocks that have reached maturity confirmation.
// Used by deep reorg detection to periodically re-verify confirmed blocks.
func (db *PostgresDB) GetConfirmedBlocks(ctx context.Context) ([]*Block, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	// Include all columns for consistency with GetPendingBlocks
	query := fmt.Sprintf(`
		SELECT id, blockheight, networkdifficulty, status, type,
			   confirmationprogress, effort, transactionconfirmationdata,
			   miner, reward, hash, created,
			   COALESCE(orphan_mismatch_count, 0) as orphan_mismatch_count,
			   COALESCE(stability_check_count, 0) as stability_check_count,
			   COALESCE(last_verified_tip, '') as last_verified_tip
		FROM %s
		WHERE status = 'confirmed'
		ORDER BY blockheight DESC
	`, tableName)

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*Block
	for rows.Next() {
		b := &Block{}
		err := rows.Scan(
			&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status, &b.Type,
			&b.ConfirmationProgress, &b.Effort, &b.TransactionConfirmationData,
			&b.Miner, &b.Reward, &b.Hash, &b.Created,
			&b.OrphanMismatchCount, &b.StabilityCheckCount, &b.LastVerifiedTip,
		)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}

	// Check for errors from iteration
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating confirmed blocks: %w", err)
	}

	return blocks, nil
}

// BlockStats contains aggregated block statistics by status.
type BlockStats struct {
	Pending   int
	Confirmed int
	Orphaned  int
	Paid      int
}

// GetBlockStats returns block counts grouped by status.
func (db *PostgresDB) GetBlockStats(ctx context.Context) (*BlockStats, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)

	query := fmt.Sprintf(`
		SELECT status, COUNT(*) as count
		FROM %s
		GROUP BY status
	`, tableName)

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &BlockStats{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		switch status {
		case "pending":
			stats.Pending = count
		case "confirmed":
			stats.Confirmed = count
		case "orphaned":
			stats.Orphaned = count
		case "paid":
			stats.Paid = count
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating block stats: %w", err)
	}

	return stats, nil
}

// GetLastBlockFoundTime returns the creation time of the most recently found block.
// Used to initialize pool effort calculation on startup.
func (db *PostgresDB) GetLastBlockFoundTime(ctx context.Context) (time.Time, error) {
	tableName := fmt.Sprintf("blocks_%s", db.poolID)
	query := fmt.Sprintf(`SELECT created FROM %s ORDER BY created DESC LIMIT 1`, tableName)

	var created time.Time
	err := db.pool.QueryRow(ctx, query).Scan(&created)
	if err != nil {
		return time.Time{}, err
	}
	return created, nil
}

// InsertPayment records a payment.
func (db *PostgresDB) InsertPayment(ctx context.Context, payment *Payment) error {
	query := `
		INSERT INTO payments (poolid, coin, address, amount, transactionconfirmationdata, created)
		VALUES ($1, $2, $3, $4, $5, $6)
	`

	_, err := db.pool.Exec(ctx, query,
		db.poolID,
		payment.Coin,
		payment.Address,
		payment.Amount,
		payment.TransactionConfirmationData,
		payment.Created,
	)

	return err
}

// UpdatePoolStats updates the poolstats table.
func (db *PostgresDB) UpdatePoolStats(ctx context.Context, stats *PoolStats) error {
	query := `
		INSERT INTO poolstats (
			poolid, connectedminers, poolhashrate, sharespersecond,
			networkhashrate, networkdifficulty, lastnetworkblocktime,
			blockheight, connectedpeers, created
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err := db.pool.Exec(ctx, query,
		db.poolID,
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

	return err
}

// GetMinerStats returns statistics for a specific miner.
func (db *PostgresDB) GetMinerStats(ctx context.Context, address string) (*MinerStats, error) {
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	query := fmt.Sprintf(`
		SELECT
			miner,
			COUNT(*) as shares,
			SUM(difficulty) as total_difficulty,
			MAX(created) as last_share
		FROM %s
		WHERE miner = $1 AND created > NOW() - INTERVAL '24 hours'
		GROUP BY miner
	`, tableName)

	var stats MinerStats
	err := db.pool.QueryRow(ctx, query, address).Scan(
		&stats.Address,
		&stats.ShareCount,
		&stats.TotalDifficulty,
		&stats.LastShare,
	)

	if err == pgx.ErrNoRows {
		return &MinerStats{Address: address}, nil
	}
	if err != nil {
		return nil, err
	}

	// Estimate hashrate from difficulty using algorithm-aware formula
	stats.Hashrate = coin.CalculateHashrateForAlgorithm(stats.TotalDifficulty, 24*3600, db.algorithm)

	return &stats, nil
}

// GetPoolHashrate calculates the current pool hashrate from recent shares.
func (db *PostgresDB) GetPoolHashrate(ctx context.Context, windowMinutes int) (float64, error) {
	// SECURITY: Validate windowMinutes bounds (DB-01 fix)
	if windowMinutes < 1 || windowMinutes > 10080 { // Max 7 days
		return 0, fmt.Errorf("invalid windowMinutes: must be 1-10080")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// SECURITY: Use parameterized INTERVAL with multiplication to prevent SQL injection (DB-04 fix)
	// Even though windowMinutes is validated, using parameterized queries is defense-in-depth
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(difficulty), 0) as total_difficulty
		FROM %s
		WHERE created > NOW() - ($1 * INTERVAL '1 minute')
	`, tableName)

	var totalDiff float64
	err := db.pool.QueryRow(ctx, query, windowMinutes).Scan(&totalDiff)
	if err != nil {
		return 0, err
	}

	// Convert to hashrate using algorithm-aware formula
	// SHA-256d: difficulty * 2^32 / seconds; Scrypt: difficulty * 65536 / seconds
	hashrate := coin.CalculateHashrateForAlgorithm(totalDiff, float64(windowMinutes*60), db.algorithm)
	return hashrate, nil
}

// GetPoolHashrateSince calculates pool hashrate from shares since a specific time.
// This is used to get accurate hashrate after server restart by only counting
// shares that were submitted during the current session.
func (db *PostgresDB) GetPoolHashrateSince(ctx context.Context, since time.Time, windowMinutes int) (float64, error) {
	// SECURITY: Validate windowMinutes bounds (DB-01 fix)
	if windowMinutes < 1 || windowMinutes > 10080 { // Max 7 days
		return 0, fmt.Errorf("invalid windowMinutes: must be 1-10080")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// Use the later of: (now - window) or since
	// This ensures we don't count shares from before the server started
	// SECURITY: Use parameterized INTERVAL with multiplication to prevent SQL injection (DB-04 fix)
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(difficulty), 0) as total_difficulty,
		       EXTRACT(EPOCH FROM (NOW() - LEAST($1, NOW() - ($2 * INTERVAL '1 minute')))) as elapsed_seconds
		FROM %s
		WHERE created > GREATEST($1, NOW() - ($2 * INTERVAL '1 minute'))
	`, tableName)

	var totalDiff float64
	var elapsedSeconds float64
	err := db.pool.QueryRow(ctx, query, since, windowMinutes).Scan(&totalDiff, &elapsedSeconds)
	if err != nil {
		return 0, err
	}

	// Avoid division by zero for very recent starts
	if elapsedSeconds < 1 {
		elapsedSeconds = 1
	}

	// Convert to hashrate using algorithm-aware formula
	hashrate := coin.CalculateHashrateForAlgorithm(totalDiff, elapsedSeconds, db.algorithm)
	return hashrate, nil
}

// CleanupStaleShares removes shares older than the specified retention period.
// This should be called on server startup to prevent stale shares from
// inflating hashrate calculations.
func (db *PostgresDB) CleanupStaleShares(ctx context.Context, retentionMinutes int) (int64, error) {
	// SECURITY: Validate retentionMinutes bounds (DB-01 fix)
	if retentionMinutes < 1 || retentionMinutes > 43200 { // Max 30 days
		return 0, fmt.Errorf("invalid retentionMinutes: must be 1-43200")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE created < NOW() - ($1 * INTERVAL '1 minute')
	`, tableName)

	result, err := db.pool.Exec(ctx, query, retentionMinutes)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected(), nil
}

// ============================================================================
// HA: Advisory Lock Methods for Payment Fencing
// ============================================================================

// paymentAdvisoryLockID is a well-known lock ID for payment processing.
// Only one node in the cluster should hold this lock at a time.
// Value: "SPPAY" as a 64-bit integer (0x5350504159 = 357637718361)
const paymentAdvisoryLockID int64 = 0x5350504159

// TryAdvisoryLock attempts to acquire a PostgreSQL session-level advisory lock.
// Returns true if the lock was acquired, false if another session holds it.
// This provides a database-level single-writer guarantee for payment processing,
// preventing split-brain double-processing even if VIP fencing fails.
//
// IMPORTANT: Uses a dedicated connection (not the general pool) because
// pg_try_advisory_lock is bound to the specific PostgreSQL session. Using
// pool.QueryRow() would grab a random connection, and the subsequent
// ReleaseAdvisoryLock might hit a different connection — making the unlock
// a no-op and leaving the lock permanently stuck.
func (db *PostgresDB) TryAdvisoryLock(ctx context.Context, lockID int64) (bool, error) {
	db.advisoryMu.Lock()
	defer db.advisoryMu.Unlock()

	// Acquire a dedicated connection from the pool
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to acquire connection for advisory lock: %w", err)
	}

	var acquired bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
	if err != nil {
		conn.Release()
		return false, err
	}

	if acquired {
		// Hold this connection — ReleaseAdvisoryLock will use it and release it
		db.advisoryConn = conn
	} else {
		// Lock not acquired — return connection to pool immediately
		conn.Release()
	}

	return acquired, nil
}

// ReleaseAdvisoryLock releases a previously acquired session-level advisory lock.
// Uses the same dedicated connection that TryAdvisoryLock acquired, ensuring
// the unlock targets the correct PostgreSQL session.
func (db *PostgresDB) ReleaseAdvisoryLock(ctx context.Context, lockID int64) error {
	db.advisoryMu.Lock()
	defer db.advisoryMu.Unlock()

	conn := db.advisoryConn
	if conn == nil {
		// No dedicated connection — lock was never acquired by us
		return nil
	}

	_, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
	conn.Release()
	db.advisoryConn = nil
	return err
}

// PaymentAdvisoryLockID returns the well-known lock ID for payment fencing.
// Deprecated: Use PaymentAdvisoryLockIDForPool for multi-coin deployments.
func PaymentAdvisoryLockID() int64 {
	return paymentAdvisoryLockID
}

// PaymentAdvisoryLockIDForPool returns a per-pool advisory lock ID derived from
// the pool ID. This allows V2 multi-coin deployments to process payments for
// different coins in parallel, while still preventing duplicate processing of
// the same coin across HA nodes.
// AUDIT FIX (PF-2): The global lock serialized ALL per-coin processors.
func PaymentAdvisoryLockIDForPool(poolID string) int64 {
	h := fnv.New64a()
	h.Write([]byte("SPPAY:" + poolID))
	return int64(h.Sum64())
}

// Block represents a mined block record.
type Block struct {
	ID                          int64
	Height                      uint64
	NetworkDifficulty           float64
	Status                      string
	Type                        string
	ConfirmationProgress        float64
	Effort                      float64
	TransactionConfirmationData string
	Miner                       string
	Reward                      float64
	Hash                        string
	Created                     time.Time

	// CRITICAL FIX: Delayed orphaning fields
	// OrphanMismatchCount tracks consecutive hash mismatches.
	// A block is only marked orphaned after N consecutive mismatches to prevent
	// false orphaning due to temporary node desync or minority fork observation.
	OrphanMismatchCount int

	// CRITICAL FIX: Stability window fields
	// StabilityCheckCount tracks consecutive checks where the block was at/above
	// maturity AND hash matched. A block only transitions to "confirmed" after
	// StabilityWindowChecks consecutive stable observations.
	StabilityCheckCount int

	// LastVerifiedTip stores the chain tip hash at the time of last verification.
	// Used to detect if tip changed between checks (chain instability).
	LastVerifiedTip string
}

// Payment represents a payment record.
type Payment struct {
	ID                          int64
	Coin                        string
	Address                     string
	Amount                      float64
	TransactionConfirmationData string
	Created                     time.Time
}

// PoolStats represents pool statistics.
type PoolStats struct {
	ConnectedMiners      int
	PoolHashrate         float64
	SharesPerSecond      float64
	NetworkHashrate      float64
	NetworkDifficulty    float64
	LastNetworkBlockTime time.Time
	BlockHeight          uint64
	ConnectedPeers       int
}

// MinerStats represents per-miner statistics.
type MinerStats struct {
	Address         string
	ShareCount      int64
	TotalDifficulty float64
	Hashrate        float64
	LastShare       time.Time
}

// MinerSummary represents a miner's summary for listing.
type MinerSummary struct {
	Address         string    `json:"address"`
	Hashrate        float64   `json:"hashrate"`
	SharesPerSecond float64   `json:"sharesPerSecond"`
	LastShare       time.Time `json:"lastShare"`
}

// GetActiveMiners returns all miners who have submitted shares in the given time window.
func (db *PostgresDB) GetActiveMiners(ctx context.Context, windowMinutes int) ([]*MinerSummary, error) {
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		SELECT
			miner,
			SUM(difficulty) as total_difficulty,
			COUNT(*) as share_count,
			MAX(created) as last_share
		FROM %s
		WHERE created > NOW() - ($1 * INTERVAL '1 minute')
		GROUP BY miner
		ORDER BY total_difficulty DESC
	`, tableName)

	rows, err := db.pool.Query(ctx, query, windowMinutes)
	if err != nil {
		return nil, fmt.Errorf("failed to query active miners: %w", err)
	}
	defer rows.Close()

	var miners []*MinerSummary
	windowSeconds := float64(windowMinutes * 60)

	for rows.Next() {
		var address string
		var totalDiff float64
		var shareCount int64
		var lastShare time.Time

		if err := rows.Scan(&address, &totalDiff, &shareCount, &lastShare); err != nil {
			return nil, fmt.Errorf("failed to scan miner row: %w", err)
		}

		// Calculate hashrate using algorithm-aware formula
		hashrate := coin.CalculateHashrateForAlgorithm(totalDiff, windowSeconds, db.algorithm)
		sharesPerSec := float64(shareCount) / windowSeconds

		miners = append(miners, &MinerSummary{
			Address:         address,
			Hashrate:        hashrate,
			SharesPerSecond: sharesPerSec,
			LastShare:       lastShare,
		})
	}

	// Check for errors from iteration
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating miners: %w", err)
	}

	return miners, nil
}
