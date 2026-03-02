// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database provides worker statistics database operations.
//
// This file implements the StatsDatabase interface for worker statistics
// persistence, including hashrate history, snapshots, and aggregations.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spiralpool/stratum/internal/coin"
)

// WorkerStats represents per-worker statistics (mirrors workers.WorkerStats).
type WorkerStats struct {
	Miner           string
	Worker          string
	Hashrate        float64
	SharesSubmitted int64
	SharesAccepted  int64
	SharesRejected  int64
	SharesStale     int64
	AcceptanceRate  float64
	TotalDifficulty float64
	LastShare       time.Time
	Connected       bool
	Difficulty      float64
	UserAgent       string
}

// WorkerSummary is a lightweight summary for listing workers.
type WorkerSummary struct {
	Miner          string    `json:"miner"`
	Worker         string    `json:"worker"`
	Hashrate       float64   `json:"hashrate"`
	SharesPerSec   float64   `json:"sharesPerSec"`
	AcceptanceRate float64   `json:"acceptanceRate"`
	LastShare      time.Time `json:"lastShare"`
	Connected      bool      `json:"connected"`
}

// WorkerHashratePoint represents a single hashrate measurement.
type WorkerHashratePoint struct {
	Timestamp time.Time `json:"timestamp"`
	Hashrate  float64   `json:"hashrate"`
	Window    string    `json:"window"`
}

// GetWorkerStats retrieves statistics for a specific worker.
func (db *PostgresDB) GetWorkerStats(ctx context.Context, miner, worker string, windowMinutes int) (*WorkerStats, error) {
	// SECURITY: Validate windowMinutes bounds (DB-01 fix)
	if windowMinutes < 1 || windowMinutes > 10080 { // Max 7 days
		return nil, fmt.Errorf("invalid windowMinutes: must be 1-10080")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		SELECT
			miner,
			worker,
			COUNT(*) as shares_submitted,
			SUM(CASE WHEN difficulty > 0 THEN 1 ELSE 0 END) as shares_accepted,
			SUM(difficulty) as total_difficulty,
			MAX(created) as last_share
		FROM %s
		WHERE miner = $1
			AND COALESCE(worker, 'default') = $2
			AND created > NOW() - ($3 * INTERVAL '1 minute')
		GROUP BY miner, worker
	`, tableName)

	var stats WorkerStats
	var workerNull *string
	err := db.pool.QueryRow(ctx, query, miner, worker, windowMinutes).Scan(
		&stats.Miner,
		&workerNull,
		&stats.SharesSubmitted,
		&stats.SharesAccepted,
		&stats.TotalDifficulty,
		&stats.LastShare,
	)

	if err == pgx.ErrNoRows {
		return &WorkerStats{Miner: miner, Worker: worker}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get worker stats: %w", err)
	}

	// Set worker name
	if workerNull != nil {
		stats.Worker = *workerNull
	} else {
		stats.Worker = "default"
	}

	// Calculate hashrate using algorithm-aware formula
	windowSeconds := float64(windowMinutes * 60)
	stats.Hashrate = coin.CalculateHashrateForAlgorithm(stats.TotalDifficulty, windowSeconds, db.algorithm)

	// Calculate acceptance rate
	if stats.SharesSubmitted > 0 {
		stats.AcceptanceRate = float64(stats.SharesAccepted) / float64(stats.SharesSubmitted) * 100
	}

	return &stats, nil
}

// GetMinerWorkers retrieves all workers for a miner.
func (db *PostgresDB) GetMinerWorkers(ctx context.Context, miner string, windowMinutes int) ([]*WorkerSummary, error) {
	// SECURITY: Validate windowMinutes bounds (DB-01 fix)
	if windowMinutes < 1 || windowMinutes > 10080 { // Max 7 days
		return nil, fmt.Errorf("invalid windowMinutes: must be 1-10080")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		SELECT
			miner,
			COALESCE(worker, 'default') as worker,
			COUNT(*) as shares_submitted,
			SUM(difficulty) as total_difficulty,
			MAX(created) as last_share
		FROM %s
		WHERE miner = $1 AND created > NOW() - ($2 * INTERVAL '1 minute')
		GROUP BY miner, worker
		ORDER BY total_difficulty DESC
		LIMIT 1000
	`, tableName)

	rows, err := db.pool.Query(ctx, query, miner, windowMinutes)
	if err != nil {
		return nil, fmt.Errorf("failed to query miner workers: %w", err)
	}
	defer rows.Close()

	windowSeconds := float64(windowMinutes * 60)
	var workers []*WorkerSummary

	for rows.Next() {
		var ws WorkerSummary
		var totalDiff float64
		var shareCount int64

		if err := rows.Scan(&ws.Miner, &ws.Worker, &shareCount, &totalDiff, &ws.LastShare); err != nil {
			return nil, fmt.Errorf("failed to scan worker row: %w", err)
		}

		ws.Hashrate = coin.CalculateHashrateForAlgorithm(totalDiff, windowSeconds, db.algorithm)
		ws.SharesPerSec = float64(shareCount) / windowSeconds
		ws.AcceptanceRate = 100 // Database stores accepted shares only; rejected shares are not persisted
		ws.Connected = time.Since(ws.LastShare) < 5*time.Minute

		workers = append(workers, &ws)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating workers: %w", err)
	}

	if workers == nil {
		workers = []*WorkerSummary{}
	}

	return workers, nil
}

// GetAllWorkers retrieves all active workers (admin view).
func (db *PostgresDB) GetAllWorkers(ctx context.Context, windowMinutes int, limit int) ([]*WorkerSummary, error) {
	// SECURITY: Validate windowMinutes bounds (DB-01 fix)
	if windowMinutes < 1 || windowMinutes > 10080 { // Max 7 days
		return nil, fmt.Errorf("invalid windowMinutes: must be 1-10080")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	if limit <= 0 || limit > 10000 {
		limit = 1000 // Sensible default
	}

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		SELECT
			miner,
			COALESCE(worker, 'default') as worker,
			COUNT(*) as shares_submitted,
			SUM(difficulty) as total_difficulty,
			MAX(created) as last_share
		FROM %s
		WHERE created > NOW() - ($1 * INTERVAL '1 minute')
		GROUP BY miner, worker
		ORDER BY total_difficulty DESC
		LIMIT %d
	`, tableName, limit)

	rows, err := db.pool.Query(ctx, query, windowMinutes)
	if err != nil {
		return nil, fmt.Errorf("failed to query all workers: %w", err)
	}
	defer rows.Close()

	windowSeconds := float64(windowMinutes * 60)
	var workers []*WorkerSummary

	for rows.Next() {
		var ws WorkerSummary
		var totalDiff float64
		var shareCount int64

		if err := rows.Scan(&ws.Miner, &ws.Worker, &shareCount, &totalDiff, &ws.LastShare); err != nil {
			return nil, fmt.Errorf("failed to scan worker row: %w", err)
		}

		ws.Hashrate = coin.CalculateHashrateForAlgorithm(totalDiff, windowSeconds, db.algorithm)
		ws.SharesPerSec = float64(shareCount) / windowSeconds
		ws.AcceptanceRate = 100
		ws.Connected = time.Since(ws.LastShare) < 5*time.Minute

		workers = append(workers, &ws)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating workers: %w", err)
	}

	if workers == nil {
		workers = []*WorkerSummary{}
	}

	return workers, nil
}

// GetWorkerHashrateHistory retrieves historical hashrate data for graphs.
func (db *PostgresDB) GetWorkerHashrateHistory(ctx context.Context, miner, worker string, hours int) ([]*WorkerHashratePoint, error) {
	// SECURITY: Validate hours bounds (DB-01 fix)
	if hours < 1 || hours > 720 { // Max 30 days
		return nil, fmt.Errorf("invalid hours: must be 1-720")
	}
	// First, check if worker_hashrate_history table exists
	historyTable := fmt.Sprintf("worker_hashrate_history_%s", db.poolID)
	var tableExists bool
	err := db.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
		historyTable,
	).Scan(&tableExists)

	if err != nil || !tableExists {
		// Fall back to aggregating from shares table
		return db.getWorkerHashrateFromShares(ctx, miner, worker, hours)
	}

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	// Query from history table
	query := fmt.Sprintf(`
		SELECT timestamp, hashrate, time_window
		FROM %s
		WHERE miner = $1 AND worker = $2 AND timestamp > NOW() - ($3 * INTERVAL '1 hour')
		ORDER BY timestamp ASC
	`, historyTable)

	rows, err := db.pool.Query(ctx, query, miner, worker, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to query hashrate history: %w", err)
	}
	defer rows.Close()

	var points []*WorkerHashratePoint
	for rows.Next() {
		var p WorkerHashratePoint
		if err := rows.Scan(&p.Timestamp, &p.Hashrate, &p.Window); err != nil {
			return nil, fmt.Errorf("failed to scan history row: %w", err)
		}
		points = append(points, &p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating history: %w", err)
	}

	return points, nil
}

// getWorkerHashrateFromShares calculates historical hashrate from shares table.
// This is a fallback when the history table doesn't exist.
func (db *PostgresDB) getWorkerHashrateFromShares(ctx context.Context, miner, worker string, hours int) ([]*WorkerHashratePoint, error) {
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// Group by 5-minute buckets for reasonable granularity
	bucketMinutes := 5
	if hours > 24 {
		bucketMinutes = 15 // Coarser for longer periods
	}
	if hours > 168 { // 1 week
		bucketMinutes = 60 // Hourly for very long periods
	}

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	// Use coin.HashrateDifficultyConstant (2^32) for hashrate calculation
	query := fmt.Sprintf(`
		WITH buckets AS (
			SELECT
				date_trunc('hour', created) +
					(EXTRACT(MINUTE FROM created)::int / %d * %d || ' minutes')::interval as bucket,
				SUM(difficulty) as total_difficulty
			FROM %s
			WHERE miner = $1
				AND COALESCE(worker, 'default') = $2
				AND created > NOW() - ($3 * INTERVAL '1 hour')
			GROUP BY bucket
			ORDER BY bucket
		)
		SELECT
			bucket as timestamp,
			total_difficulty * %f / (%d * 60) as hashrate
		FROM buckets
	`, bucketMinutes, bucketMinutes, tableName, coin.HashrateConstantForAlgorithm(db.algorithm), bucketMinutes)

	rows, err := db.pool.Query(ctx, query, miner, worker, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate hashrate history: %w", err)
	}
	defer rows.Close()

	var points []*WorkerHashratePoint
	for rows.Next() {
		var p WorkerHashratePoint
		if err := rows.Scan(&p.Timestamp, &p.Hashrate); err != nil {
			return nil, fmt.Errorf("failed to scan history row: %w", err)
		}
		p.Window = fmt.Sprintf("%dm", bucketMinutes)
		points = append(points, &p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating history: %w", err)
	}

	return points, nil
}

// RecordWorkerSnapshot stores a worker statistics snapshot for historical tracking.
func (db *PostgresDB) RecordWorkerSnapshot(ctx context.Context, miner, worker string, hashrate float64, stats *WorkerStats) error {
	historyTable := fmt.Sprintf("worker_hashrate_history_%s", db.poolID)

	// Check if table exists first
	var tableExists bool
	err := db.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
		historyTable,
	).Scan(&tableExists)

	if err != nil || !tableExists {
		// Table doesn't exist yet - that's OK, it will be created by migration
		return nil
	}

	query := fmt.Sprintf(`
		INSERT INTO %s (miner, worker, hashrate, shares_submitted, shares_accepted,
			shares_rejected, total_difficulty, time_window, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '1m', NOW())
	`, historyTable)

	_, err = db.pool.Exec(ctx, query,
		miner, worker, hashrate,
		stats.SharesSubmitted, stats.SharesAccepted, stats.SharesRejected,
		stats.TotalDifficulty,
	)

	if err != nil {
		return fmt.Errorf("failed to record worker snapshot: %w", err)
	}

	return nil
}

// CleanupOldWorkerSnapshots removes old historical data beyond retention period.
func (db *PostgresDB) CleanupOldWorkerSnapshots(ctx context.Context, retentionDays int) error {
	historyTable := fmt.Sprintf("worker_hashrate_history_%s", db.poolID)

	// Check if table exists first
	var tableExists bool
	err := db.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
		historyTable,
	).Scan(&tableExists)

	if err != nil || !tableExists {
		return nil // Table doesn't exist, nothing to clean
	}

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	query := fmt.Sprintf(`
		DELETE FROM %s WHERE timestamp < NOW() - ($1 * INTERVAL '1 day')
	`, historyTable)

	result, err := db.pool.Exec(ctx, query, retentionDays)
	if err != nil {
		return fmt.Errorf("failed to cleanup old snapshots: %w", err)
	}

	rowsDeleted := result.RowsAffected()
	if rowsDeleted > 0 {
		db.logger.Infow("Cleaned up old worker snapshots",
			"deleted", rowsDeleted,
			"retentionDays", retentionDays)
	}

	return nil
}

// GetPoolHashrateHistory retrieves historical pool hashrate for graphs.
func (db *PostgresDB) GetPoolHashrateHistory(ctx context.Context, hours int) ([]*WorkerHashratePoint, error) {
	// SECURITY: Validate hours bounds (DB-01 fix)
	if hours < 1 || hours > 720 { // Max 30 days
		return nil, fmt.Errorf("invalid hours: must be 1-720")
	}
	tableName := fmt.Sprintf("shares_%s", db.poolID)

	// Bucket size based on time range
	bucketMinutes := 5
	if hours > 24 {
		bucketMinutes = 15
	}
	if hours > 168 {
		bucketMinutes = 60
	}

	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	// Use coin.HashrateDifficultyConstant (2^32) for hashrate calculation
	query := fmt.Sprintf(`
		WITH buckets AS (
			SELECT
				date_trunc('hour', created) +
					(EXTRACT(MINUTE FROM created)::int / %d * %d || ' minutes')::interval as bucket,
				SUM(difficulty) as total_difficulty
			FROM %s
			WHERE created > NOW() - ($1 * INTERVAL '1 hour')
			GROUP BY bucket
			ORDER BY bucket
		)
		SELECT
			bucket as timestamp,
			total_difficulty * %f / (%d * 60) as hashrate
		FROM buckets
	`, bucketMinutes, bucketMinutes, tableName, coin.HashrateConstantForAlgorithm(db.algorithm), bucketMinutes)

	rows, err := db.pool.Query(ctx, query, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to query pool hashrate history: %w", err)
	}
	defer rows.Close()

	var points []*WorkerHashratePoint
	for rows.Next() {
		var p WorkerHashratePoint
		if err := rows.Scan(&p.Timestamp, &p.Hashrate); err != nil {
			return nil, fmt.Errorf("failed to scan history row: %w", err)
		}
		p.Window = fmt.Sprintf("%dm", bucketMinutes)
		points = append(points, &p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pool history: %w", err)
	}

	return points, nil
}
