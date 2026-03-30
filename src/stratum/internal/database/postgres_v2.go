// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database - V2 extensions for multi-pool support.
//
// These methods provide pool-specific database operations for
// the V2 multi-coin architecture.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// Pool returns the underlying connection pool for V2 migrations.
func (db *PostgresDB) Pool() *pgxpool.Pool {
	return db.pool
}

// WriteBatchForPool writes shares to a specific pool's table.
func (db *PostgresDB) WriteBatchForPool(ctx context.Context, poolID string, shares []*protocol.Share) error {
	if len(shares) == 0 {
		return nil
	}

	// SECURITY: Validate poolID to prevent SQL injection via table name
	if !validPoolID.MatchString(poolID) {
		return fmt.Errorf("invalid pool ID: %q", poolID)
	}

	tableName := fmt.Sprintf("shares_%s", poolID)

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
				poolID,
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
		return fmt.Errorf("failed to copy shares to %s: %w", tableName, err)
	}

	db.logger.Debugw("Wrote share batch",
		"poolId", poolID,
		"count", len(shares),
	)

	return nil
}

// InsertBlockForPool records a block for a specific pool.
// Uses PostgreSQL advisory locks to prevent duplicate insertion in HA scenarios
// where multiple stratum servers might try to record the same block.
func (db *PostgresDB) InsertBlockForPool(ctx context.Context, poolID string, block *Block) error {
	// SECURITY: Validate poolID to prevent SQL injection via table name
	if !validPoolID.MatchString(poolID) {
		return fmt.Errorf("invalid pool ID: %q", poolID)
	}

	tableName := fmt.Sprintf("blocks_%s", poolID)

	// Begin a transaction to hold the advisory lock
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Acquire advisory lock based on block height to prevent concurrent insertion
	// Lock key is a hash of pool_id + block_height to ensure uniqueness per pool
	lockKey := int64(block.Height) ^ int64(hash(poolID))
	var lockAcquired bool
	err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", lockKey).Scan(&lockAcquired)
	if err != nil {
		return fmt.Errorf("failed to acquire advisory lock: %w", err)
	}

	if !lockAcquired {
		// Another transaction is inserting this block - skip to avoid duplicate
		db.logger.Warnw("Block insertion skipped - another process is inserting",
			"poolId", poolID,
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
			"poolId", poolID,
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
		poolID,
		block.Height,
		block.NetworkDifficulty,
		block.Status,
		block.Type,
		block.ConfirmationProgress,
		block.Effort,
		block.TransactionConfirmationData,
		block.Miner,
		block.Reward,
		block.Source,
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
		"poolId", poolID,
		"height", block.Height,
		"hash", block.Hash,
		"miner", block.Miner,
	)

	return nil
}

// statusPriority defines the progression of block statuses.
// Higher values = more final. We never downgrade to a lower priority status.
var statusPriority = map[string]int{
	"submitting": 0, // Initial crash-safe marker
	"pending":    1, // Awaiting confirmations
	"confirmed":  2, // Fully confirmed
	"orphaned":   2, // Final (orphaned) - same priority as confirmed
	"paid":       3, // Payment processed - most final state
}

// UpdateBlockStatusForPool updates the status of a block for a specific pool.
// CRASH SAFETY: Used after block submission to update from "submitting" to final status.
// This enables crash-safe block submission: we record "submitting" before daemon submit,
// then update to "pending" or "orphaned" after the submit attempt completes.
//
// RACE SAFETY: Uses atomic check-then-update to prevent status downgrades. A block that
// has already progressed to "pending" or "confirmed" will not be downgraded by a late
// reconciliation or timeout handler trying to mark it "orphaned".
func (db *PostgresDB) UpdateBlockStatusForPool(ctx context.Context, poolID string, height uint64, hash string, status string, confirmationProgress float64) error {
	// SECURITY: Validate poolID to prevent SQL injection via table name
	if !validPoolID.MatchString(poolID) {
		return fmt.Errorf("invalid pool ID: %q", poolID)
	}

	tableName := fmt.Sprintf("blocks_%s", poolID)

	// Get priority of the new status
	newPriority, ok := statusPriority[status]
	if !ok {
		return fmt.Errorf("invalid block status %q: not in statusPriority map", status)
	}

	// Atomic check-then-update: only update if new status has equal or higher priority
	// This prevents race conditions where a late timeout handler tries to orphan
	// a block that has already been confirmed as pending by the confirmation cycle.
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

	result, err := db.pool.Exec(ctx, query, status, confirmationProgress, height, hash)
	if err != nil {
		return fmt.Errorf("failed to update block status: %w", err)
	}

	if result.RowsAffected() == 0 {
		// Check if block exists with a higher-priority status (expected race condition)
		var currentStatus string
		checkQuery := fmt.Sprintf("SELECT status FROM %s WHERE blockheight = $1 AND hash = $2", tableName)
		err := db.pool.QueryRow(ctx, checkQuery, height, hash).Scan(&currentStatus)
		if err != nil {
			db.logger.Warnw("Block status update affected no rows - block may not exist",
				"poolId", poolID,
				"height", height,
				"hash", hash,
				"attemptedStatus", status,
			)
		} else {
			// FIX: Check map key exists — an unexpected status from the DB
			// (e.g. manual SQL edit) would return 0, making comparisons wrong.
			currentPriority, known := statusPriority[currentStatus]
			if !known {
				db.logger.Errorw("Block has unknown status in database",
					"poolId", poolID,
					"height", height,
					"hash", hash,
					"currentStatus", currentStatus,
				)
			}
			if currentPriority >= newPriority {
				db.logger.Debugw("Block status update skipped - already at higher priority status",
					"poolId", poolID,
					"height", height,
					"hash", hash,
					"currentStatus", currentStatus,
					"attemptedStatus", status,
				)
			} else {
				db.logger.Warnw("Block status update unexpectedly affected no rows",
					"poolId", poolID,
					"height", height,
					"hash", hash,
					"currentStatus", currentStatus,
					"attemptedStatus", status,
				)
			}
		}
		return ErrStatusGuardBlocked
	} else {
		db.logger.Infow("Updated block status",
			"poolId", poolID,
			"height", height,
			"hash", hash,
			"status", status,
		)
	}

	return nil
}

// GetPoolHashrateForPool calculates hashrate for a specific pool.
// The algorithm parameter determines which hashrate constant to use:
//   - SHA-256d: difficulty * 2^32 / seconds
//   - Scrypt:   difficulty * 65536 / seconds
func (db *PostgresDB) GetPoolHashrateForPool(ctx context.Context, poolID string, windowMinutes int, algorithm string) (float64, error) {
	// SECURITY: Validate poolID to prevent SQL injection via table name
	if !validPoolID.MatchString(poolID) {
		return 0, fmt.Errorf("invalid pool ID: %q", poolID)
	}

	tableName := fmt.Sprintf("shares_%s", poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
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
	hashrate := coin.CalculateHashrateForAlgorithm(totalDiff, float64(windowMinutes*60), algorithm)
	return hashrate, nil
}

// UpdatePoolStatsForPool updates stats for a specific pool.
func (db *PostgresDB) UpdatePoolStatsForPool(ctx context.Context, poolID string, stats *PoolStats) error {
	// SECURITY: Validate poolID to prevent SQL injection
	if !validPoolID.MatchString(poolID) {
		return fmt.Errorf("invalid pool ID: %q", poolID)
	}

	query := `
		INSERT INTO poolstats (
			poolid, connectedminers, poolhashrate, sharespersecond,
			networkhashrate, networkdifficulty, lastnetworkblocktime,
			blockheight, connectedpeers, created
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`

	_, err := db.pool.Exec(ctx, query,
		poolID,
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

// NewPoolShareWriter creates a new share writer for a specific pool.
// This allows the shares package to write to pool-specific tables.
func NewPoolShareWriter(poolID string, db *PostgresDB) *PoolShareWriterDB {
	return &PoolShareWriterDB{
		poolID: poolID,
		db:     db,
	}
}

// PoolShareWriterDB implements ShareWriter for pool-specific tables.
type PoolShareWriterDB struct {
	poolID string
	db     *PostgresDB
}

// WriteBatch writes a batch of shares to the pool-specific table.
func (w *PoolShareWriterDB) WriteBatch(ctx context.Context, shares []*protocol.Share) error {
	return w.db.WriteBatchForPool(ctx, w.poolID, shares)
}

// Close closes the writer.
func (w *PoolShareWriterDB) Close() error {
	return nil
}
