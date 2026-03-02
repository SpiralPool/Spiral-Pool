// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database - V2 migrations for multi-coin and node failover support
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// MigratorV2 handles V2 database schema creation and migrations.
type MigratorV2 struct {
	pool   *pgxpool.Pool
	logger *zap.SugaredLogger
}

// NewMigratorV2 creates a new V2 database migrator.
func NewMigratorV2(pool *pgxpool.Pool, logger *zap.Logger) *MigratorV2 {
	return &MigratorV2{
		pool:   pool,
		logger: logger.Sugar(),
	}
}

// RunV2Migrations applies V2-specific migrations.
func (m *MigratorV2) RunV2Migrations(ctx context.Context) error {
	m.logger.Info("Running V2 database migrations...")

	migrations := []struct {
		version int
		name    string
		sql     string
	}{
		{
			version: 100,
			name:    "v2_coins_registry",
			sql: `
				-- Global coin registry
				CREATE TABLE IF NOT EXISTS coins (
					id              SERIAL PRIMARY KEY,
					symbol          VARCHAR(10) UNIQUE NOT NULL,
					name            VARCHAR(50) NOT NULL,
					algorithm       VARCHAR(20) NOT NULL,
					enabled         BOOLEAN DEFAULT true,
					created_at      TIMESTAMP DEFAULT NOW(),
					updated_at      TIMESTAMP DEFAULT NOW()
				);

				-- Insert default supported coins (SHA-256d and Scrypt)
				-- FIX: Removed XVG/JKC/LKY (deleted coins), added NMC/SYS/XMY/FBTC (aux chains)
				INSERT INTO coins (symbol, name, algorithm) VALUES
					('BTC', 'Bitcoin', 'sha256d'),
					('BCH', 'Bitcoin Cash', 'sha256d'),
					('BC2', 'Bitcoin II', 'sha256d'),
					('DGB', 'DigiByte', 'sha256d'),
					('NMC', 'Namecoin', 'sha256d'),
					('SYS', 'Syscoin', 'sha256d'),
					('XMY', 'Myriadcoin', 'sha256d'),
					('FBTC', 'Fractal Bitcoin', 'sha256d'),
					('LTC', 'Litecoin', 'scrypt'),
					('DOGE', 'Dogecoin', 'scrypt'),
					('DGB-SCRYPT', 'DigiByte (Scrypt)', 'scrypt'),
					('PEP', 'PepeCoin', 'scrypt'),
					('CAT', 'Catcoin', 'scrypt')
				ON CONFLICT (symbol) DO NOTHING;

				CREATE INDEX IF NOT EXISTS idx_coins_symbol ON coins(symbol);
			`,
		},
		{
			version: 101,
			name:    "v2_node_health_tracking",
			sql: `
				-- Node health history (per pool)
				CREATE TABLE IF NOT EXISTS node_health (
					id                  BIGSERIAL PRIMARY KEY,
					pool_id             VARCHAR(64) NOT NULL,
					node_id             VARCHAR(50) NOT NULL,
					health_score        FLOAT NOT NULL,
					response_time_ms    INTEGER NOT NULL,
					block_height        BIGINT NOT NULL,
					is_synced           BOOLEAN NOT NULL,
					connections         INTEGER DEFAULT 0,
					error_message       TEXT,
					recorded_at         TIMESTAMP DEFAULT NOW()
				);

				CREATE INDEX IF NOT EXISTS idx_node_health_pool ON node_health(pool_id);
				CREATE INDEX IF NOT EXISTS idx_node_health_node ON node_health(pool_id, node_id);
				CREATE INDEX IF NOT EXISTS idx_node_health_time ON node_health(recorded_at);

				-- Partition hint: For high-volume deployments, consider partitioning by time
				-- CREATE TABLE node_health_y2024m01 PARTITION OF node_health FOR VALUES FROM ('2024-01-01') TO ('2024-02-01');
			`,
		},
		{
			version: 102,
			name:    "v2_failover_events",
			sql: `
				-- Failover event log
				CREATE TABLE IF NOT EXISTS failover_events (
					id              BIGSERIAL PRIMARY KEY,
					pool_id         VARCHAR(64) NOT NULL,
					from_node_id    VARCHAR(50),
					to_node_id      VARCHAR(50) NOT NULL,
					reason          VARCHAR(100) NOT NULL,
					old_score       FLOAT,
					new_score       FLOAT NOT NULL,
					occurred_at     TIMESTAMP DEFAULT NOW()
				);

				CREATE INDEX IF NOT EXISTS idx_failover_pool ON failover_events(pool_id);
				CREATE INDEX IF NOT EXISTS idx_failover_time ON failover_events(occurred_at);
			`,
		},
		{
			version: 103,
			name:    "v2_multi_coin_shares",
			sql: `
				-- Add coin column to shares tables (for future multi-coin on same pool)
				-- This is a template that should be applied per-pool
				-- Actual migration happens in CreatePoolTablesV2

				-- Global shares view (optional, for cross-pool reporting)
				-- CREATE VIEW all_shares AS
				-- SELECT * FROM shares_dgb_mainnet UNION ALL
				-- SELECT * FROM shares_btc_mainnet;

				-- Placeholder migration - actual work done in CreatePoolTablesV2
				SELECT 1;
			`,
		},
		{
			version: 104,
			name:    "v2_block_submission_log",
			sql: `
				-- Block submission attempts log (for debugging and auditing)
				CREATE TABLE IF NOT EXISTS block_submissions (
					id                  BIGSERIAL PRIMARY KEY,
					pool_id             VARCHAR(64) NOT NULL,
					block_height        BIGINT NOT NULL,
					block_hash          VARCHAR(128) NOT NULL,
					node_id             VARCHAR(50) NOT NULL,
					attempt_number      INTEGER NOT NULL,
					success             BOOLEAN NOT NULL,
					error_message       TEXT,
					response_time_ms    INTEGER,
					submitted_at        TIMESTAMP DEFAULT NOW()
				);

				CREATE INDEX IF NOT EXISTS idx_block_sub_pool ON block_submissions(pool_id);
				CREATE INDEX IF NOT EXISTS idx_block_sub_height ON block_submissions(block_height);
				CREATE INDEX IF NOT EXISTS idx_block_sub_hash ON block_submissions(block_hash);
				CREATE INDEX IF NOT EXISTS idx_block_sub_time ON block_submissions(submitted_at);
			`,
		},
		{
			version: 105,
			name:    "v2_pool_configuration",
			sql: `
				-- Store pool configuration in database for API access
				CREATE TABLE IF NOT EXISTS pool_config (
					id              SERIAL PRIMARY KEY,
					pool_id         VARCHAR(64) UNIQUE NOT NULL,
					coin_symbol     VARCHAR(10) NOT NULL,
					stratum_port    INTEGER NOT NULL,
					enabled         BOOLEAN DEFAULT true,
					config_json     JSONB,
					created_at      TIMESTAMP DEFAULT NOW(),
					updated_at      TIMESTAMP DEFAULT NOW()
				);

				CREATE INDEX IF NOT EXISTS idx_pool_config_coin ON pool_config(coin_symbol);
			`,
		},
		{
			version: 106,
			name:    "v2_node_configuration",
			sql: `
				-- Store node configuration for each pool
				CREATE TABLE IF NOT EXISTS pool_nodes (
					id              SERIAL PRIMARY KEY,
					pool_id         VARCHAR(64) NOT NULL,
					node_id         VARCHAR(50) NOT NULL,
					host            VARCHAR(255) NOT NULL,
					port            INTEGER NOT NULL,
					priority        INTEGER DEFAULT 0,
					weight          INTEGER DEFAULT 1,
					zmq_enabled     BOOLEAN DEFAULT false,
					zmq_endpoint    VARCHAR(255),
					enabled         BOOLEAN DEFAULT true,
					created_at      TIMESTAMP DEFAULT NOW(),
					updated_at      TIMESTAMP DEFAULT NOW(),

					UNIQUE(pool_id, node_id)
				);

				CREATE INDEX IF NOT EXISTS idx_pool_nodes_pool ON pool_nodes(pool_id);
			`,
		},
	}

	// Get applied migrations
	applied := make(map[int]bool)
	rows, err := m.pool.Query(ctx, "SELECT version FROM schema_migrations WHERE version >= 100")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var version int
			if err := rows.Scan(&version); err == nil {
				applied[version] = true
			}
		}
	}

	// Apply pending migrations
	for _, migration := range migrations {
		if applied[migration.version] {
			m.logger.Debugw("Migration already applied", "version", migration.version)
			continue
		}

		m.logger.Infow("Applying V2 migration",
			"version", migration.version,
			"name", migration.name,
		)

		if _, err := m.pool.Exec(ctx, migration.sql); err != nil {
			return fmt.Errorf("failed to apply V2 migration %d (%s): %w",
				migration.version, migration.name, err)
		}

		// Record migration
		_, err := m.pool.Exec(ctx,
			"INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)",
			migration.version, migration.name, time.Now(),
		)
		if err != nil {
			return fmt.Errorf("failed to record V2 migration %d: %w", migration.version, err)
		}

		m.logger.Infow("V2 migration applied successfully",
			"version", migration.version,
			"name", migration.name,
		)
	}

	m.logger.Info("V2 database migrations complete")
	return nil
}

// CreatePoolTablesV2 creates V2 tables for a specific pool.
// It handles both fresh creation and upgrading existing V1 tables by adding missing columns.
func (m *MigratorV2) CreatePoolTablesV2(ctx context.Context, poolID string, coinSymbol string) error {
	// SECURITY: Validate poolID to prevent SQL injection (DB-02 fix)
	if !isValidIdentifier(poolID) {
		return fmt.Errorf("invalid pool ID: must be alphanumeric with underscores, 1-63 chars")
	}
	// SECURITY: Validate coinSymbol to prevent SQL injection (DB-05 fix)
	if !isValidIdentifier(coinSymbol) {
		return fmt.Errorf("invalid coin symbol: must be alphanumeric with underscores, 1-63 chars")
	}

	m.logger.Infow("Creating V2 pool tables", "poolId", poolID, "coin", coinSymbol)

	// Create shares table (without coin column - added separately for V1 compatibility)
	sharesTable := fmt.Sprintf("shares_%s", poolID)
	_, err := m.pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                  BIGSERIAL PRIMARY KEY,
			poolid              VARCHAR(64) NOT NULL,
			blockheight         BIGINT NOT NULL,
			difficulty          DOUBLE PRECISION NOT NULL,
			networkdifficulty   DOUBLE PRECISION NOT NULL,
			miner               VARCHAR(256) NOT NULL,
			worker              VARCHAR(256),
			useragent           VARCHAR(256),
			ipaddress           VARCHAR(64),
			source              VARCHAR(64),
			created             TIMESTAMP NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_%s_miner ON %s(miner);
		CREATE INDEX IF NOT EXISTS idx_%s_created ON %s(created);
		CREATE INDEX IF NOT EXISTS idx_%s_blockheight ON %s(blockheight);
	`, sharesTable,
		poolID, sharesTable,
		poolID, sharesTable,
		poolID, sharesTable))
	if err != nil {
		return fmt.Errorf("failed to create V2 shares table: %w", err)
	}

	// Add 'coin' column - works for both fresh installs and V1->V2 upgrades
	// Using ADD COLUMN IF NOT EXISTS (PostgreSQL 9.6+) for idempotency
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS coin VARCHAR(10) NOT NULL DEFAULT '%s';
	`, sharesTable, coinSymbol))
	if err != nil {
		return fmt.Errorf("failed to add coin column to shares table: %w", err)
	}

	// Now create the coin index (safe to run even if column was just added)
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS idx_%s_coin ON %s(coin);
	`, poolID, sharesTable))
	if err != nil {
		return fmt.Errorf("failed to create coin index on shares table: %w", err)
	}

	// Create blocks table (without V2-specific columns - added separately for V1 compatibility)
	blocksTable := fmt.Sprintf("blocks_%s", poolID)
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                          BIGSERIAL PRIMARY KEY,
			poolid                      VARCHAR(64) NOT NULL,
			blockheight                 BIGINT NOT NULL,
			networkdifficulty           DOUBLE PRECISION NOT NULL,
			status                      VARCHAR(32) NOT NULL DEFAULT 'pending',
			type                        VARCHAR(32),
			confirmationprogress        DOUBLE PRECISION DEFAULT 0,
			effort                      DOUBLE PRECISION,
			transactionconfirmationdata TEXT,
			miner                       VARCHAR(256) NOT NULL,
			reward                      DECIMAL(28,12),
			source                      VARCHAR(64),
			hash                        VARCHAR(128),
			created                     TIMESTAMP NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_%s_status ON %s(status);
		CREATE INDEX IF NOT EXISTS idx_%s_miner ON %s(miner);
		CREATE INDEX IF NOT EXISTS idx_%s_created ON %s(created);
		CREATE INDEX IF NOT EXISTS idx_%s_height ON %s(blockheight);
	`, blocksTable,
		poolID, blocksTable,
		poolID, blocksTable,
		poolID, blocksTable,
		poolID, blocksTable))
	if err != nil {
		return fmt.Errorf("failed to create V2 blocks table: %w", err)
	}

	// Add V2-specific columns - works for both fresh installs and V1->V2 upgrades
	// Using ADD COLUMN IF NOT EXISTS (PostgreSQL 9.6+) for idempotency
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS coin VARCHAR(10) NOT NULL DEFAULT '%s';
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS submitted_node VARCHAR(50);
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS submission_attempts INTEGER DEFAULT 1;
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS orphan_mismatch_count INTEGER DEFAULT 0;
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS stability_check_count INTEGER DEFAULT 0;
		ALTER TABLE %s ADD COLUMN IF NOT EXISTS last_verified_tip TEXT DEFAULT '';
	`, blocksTable, coinSymbol, blocksTable, blocksTable, blocksTable, blocksTable, blocksTable))
	if err != nil {
		return fmt.Errorf("failed to add V2 columns to blocks table: %w", err)
	}

	// Create the coin index on blocks table
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		CREATE INDEX IF NOT EXISTS idx_%s_coin ON %s(coin);
	`, poolID, blocksTable))
	if err != nil {
		return fmt.Errorf("failed to create coin index on blocks table: %w", err)
	}

	// Create per-pool node health table
	nodeHealthTable := fmt.Sprintf("node_health_%s", poolID)
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id                  BIGSERIAL PRIMARY KEY,
			node_id             VARCHAR(50) NOT NULL,
			health_score        FLOAT NOT NULL,
			response_time_ms    INTEGER NOT NULL,
			block_height        BIGINT NOT NULL,
			is_synced           BOOLEAN NOT NULL,
			connections         INTEGER DEFAULT 0,
			error_message       TEXT,
			recorded_at         TIMESTAMP DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_%s_node ON %s(node_id);
		CREATE INDEX IF NOT EXISTS idx_%s_time ON %s(recorded_at);
	`, nodeHealthTable, poolID, nodeHealthTable, poolID, nodeHealthTable))
	if err != nil {
		return fmt.Errorf("failed to create node health table: %w", err)
	}

	// Create per-pool failover events table
	failoverTable := fmt.Sprintf("failover_events_%s", poolID)
	_, err = m.pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id              BIGSERIAL PRIMARY KEY,
			from_node_id    VARCHAR(50),
			to_node_id      VARCHAR(50) NOT NULL,
			reason          VARCHAR(100) NOT NULL,
			old_score       FLOAT,
			new_score       FLOAT NOT NULL,
			occurred_at     TIMESTAMP DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_%s_time ON %s(occurred_at);
	`, failoverTable, poolID, failoverTable))
	if err != nil {
		return fmt.Errorf("failed to create failover events table: %w", err)
	}

	m.logger.Infow("V2 pool tables created successfully",
		"poolId", poolID,
		"coin", coinSymbol,
		"tables", []string{sharesTable, blocksTable, nodeHealthTable, failoverTable},
	)

	return nil
}

// RecordNodeHealth records a node health check result.
func (m *MigratorV2) RecordNodeHealth(ctx context.Context, poolID, nodeID string, score float64, responseTimeMs int, blockHeight uint64, isSynced bool, connections int, errorMsg string) error {
	// SECURITY: Validate poolID to prevent SQL injection (DB-02 fix)
	if !isValidIdentifier(poolID) {
		return fmt.Errorf("invalid pool ID: must be alphanumeric with underscores, 1-63 chars")
	}
	table := fmt.Sprintf("node_health_%s", poolID)
	_, err := m.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (node_id, health_score, response_time_ms, block_height, is_synced, connections, error_message, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`, table), nodeID, score, responseTimeMs, blockHeight, isSynced, connections, errorMsg)
	return err
}

// RecordFailoverEvent records a failover event.
func (m *MigratorV2) RecordFailoverEvent(ctx context.Context, poolID, fromNodeID, toNodeID, reason string, oldScore, newScore float64) error {
	// SECURITY: Validate poolID to prevent SQL injection (DB-02 fix)
	if !isValidIdentifier(poolID) {
		return fmt.Errorf("invalid pool ID: must be alphanumeric with underscores, 1-63 chars")
	}
	table := fmt.Sprintf("failover_events_%s", poolID)
	_, err := m.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (from_node_id, to_node_id, reason, old_score, new_score, occurred_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
	`, table), fromNodeID, toNodeID, reason, oldScore, newScore)
	return err
}

// CleanupOldHealthRecords removes health records older than retention period.
func (m *MigratorV2) CleanupOldHealthRecords(ctx context.Context, poolID string, retentionDays int) error {
	// SECURITY: Validate poolID to prevent SQL injection (DB-02 fix)
	if !isValidIdentifier(poolID) {
		return fmt.Errorf("invalid pool ID: must be alphanumeric with underscores, 1-63 chars")
	}
	// SECURITY: Validate retentionDays bounds (DB-01 fix - integer validation)
	if retentionDays < 1 || retentionDays > 365 {
		return fmt.Errorf("invalid retention days: must be 1-365")
	}
	table := fmt.Sprintf("node_health_%s", poolID)
	// SECURITY: Use parameterized INTERVAL to prevent SQL injection (DB-01 fix)
	_, err := m.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %s WHERE recorded_at < NOW() - ($1 * INTERVAL '1 day')
	`, table), retentionDays)
	return err
}
