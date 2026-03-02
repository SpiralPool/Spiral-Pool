// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Payment cycle integration test — requires real PostgreSQL.
//
// Uses a real PostgreSQL block store for persistence and a mock DaemonRPC
// for chain data. Verifies the complete pending→confirmed pipeline through
// the processor's processCycle method.
//
// To run:
//
//	SPIRAL_TEST_DB_URL="postgres://user:pass@localhost:5432/spiral_test?sslmode=disable" \
//	  go test -v -run TestIntegration_PaymentCycle ./internal/payments/
package payments

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

const integrationTestPoolID = "payment_integ_test"

// integrationDBPool returns a connected pgxpool.Pool for integration testing.
// Skips the test if SPIRAL_TEST_DB_URL is not set.
func integrationDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("SPIRAL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("Set SPIRAL_TEST_DB_URL to run payment integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("Failed to ping test database: %v", err)
	}

	return pool
}

// createIntegrationBlocksTable creates the blocks table for payment integration tests.
func createIntegrationBlocksTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)

	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			poolid TEXT NOT NULL,
			blockheight BIGINT NOT NULL,
			networkdifficulty DOUBLE PRECISION DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			type TEXT DEFAULT 'block',
			confirmationprogress DOUBLE PRECISION DEFAULT 0,
			effort DOUBLE PRECISION DEFAULT 0,
			transactionconfirmationdata TEXT DEFAULT '',
			miner TEXT NOT NULL DEFAULT '',
			reward DOUBLE PRECISION DEFAULT 0,
			source TEXT DEFAULT 'stratum',
			hash TEXT NOT NULL,
			created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
			orphan_mismatch_count INT DEFAULT 0,
			stability_check_count INT DEFAULT 0,
			last_verified_tip TEXT DEFAULT ''
		)
	`, tableName)

	if _, err := pool.Exec(ctx, query); err != nil {
		t.Fatalf("Failed to create integration blocks table: %v", err)
	}

	indexQuery := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_hash ON %s (hash)`, tableName, tableName)
	if _, err := pool.Exec(ctx, indexQuery); err != nil {
		t.Fatalf("Failed to create hash index: %v", err)
	}
}

// dropIntegrationBlocksTable drops the integration test blocks table.
func dropIntegrationBlocksTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", tableName))
}

// realBlockStore implements BlockStore by querying the real PostgreSQL database
// directly. This avoids needing to construct a database.PostgresDB with
// unexported fields from outside the package.
type realBlockStore struct {
	pool *pgxpool.Pool
}

func (s *realBlockStore) GetPendingBlocks(ctx context.Context) ([]*database.Block, error) {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`SELECT id, blockheight, networkdifficulty, status, type,
		confirmationprogress, effort, miner, reward, hash, created,
		orphan_mismatch_count, stability_check_count, last_verified_tip
		FROM %s WHERE status = 'pending' ORDER BY blockheight ASC`, tableName)

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*database.Block
	for rows.Next() {
		b := &database.Block{}
		if err := rows.Scan(&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status,
			&b.Type, &b.ConfirmationProgress, &b.Effort, &b.Miner, &b.Reward,
			&b.Hash, &b.Created, &b.OrphanMismatchCount, &b.StabilityCheckCount,
			&b.LastVerifiedTip); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, rows.Err()
}

func (s *realBlockStore) GetConfirmedBlocks(ctx context.Context) ([]*database.Block, error) {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`SELECT id, blockheight, networkdifficulty, status, type,
		confirmationprogress, effort, miner, reward, hash, created,
		orphan_mismatch_count, stability_check_count, last_verified_tip
		FROM %s WHERE status = 'confirmed' ORDER BY blockheight ASC`, tableName)

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*database.Block
	for rows.Next() {
		b := &database.Block{}
		if err := rows.Scan(&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status,
			&b.Type, &b.ConfirmationProgress, &b.Effort, &b.Miner, &b.Reward,
			&b.Hash, &b.Created, &b.OrphanMismatchCount, &b.StabilityCheckCount,
			&b.LastVerifiedTip); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, rows.Err()
}

func (s *realBlockStore) GetBlocksByStatus(ctx context.Context, status string) ([]*database.Block, error) {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`SELECT id, blockheight, networkdifficulty, status, type,
		confirmationprogress, effort, miner, reward, hash, created,
		orphan_mismatch_count, stability_check_count, last_verified_tip
		FROM %s WHERE status = $1 ORDER BY blockheight ASC`, tableName)

	rows, err := s.pool.Query(ctx, query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []*database.Block
	for rows.Next() {
		b := &database.Block{}
		if err := rows.Scan(&b.ID, &b.Height, &b.NetworkDifficulty, &b.Status,
			&b.Type, &b.ConfirmationProgress, &b.Effort, &b.Miner, &b.Reward,
			&b.Hash, &b.Created, &b.OrphanMismatchCount, &b.StabilityCheckCount,
			&b.LastVerifiedTip); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, rows.Err()
}

func (s *realBlockStore) UpdateBlockStatus(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64) error {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`UPDATE %s SET status = $1, confirmationprogress = $2
		WHERE blockheight = $3 AND hash = $4`, tableName)
	_, err := s.pool.Exec(ctx, query, status, confirmationProgress, height, hash)
	return err
}

func (s *realBlockStore) UpdateBlockOrphanCount(ctx context.Context, height uint64, hash string, mismatchCount int) error {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`UPDATE %s SET orphan_mismatch_count = $1
		WHERE blockheight = $2 AND hash = $3`, tableName)
	_, err := s.pool.Exec(ctx, query, mismatchCount, height, hash)
	return err
}

func (s *realBlockStore) UpdateBlockStabilityCount(ctx context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`UPDATE %s SET stability_check_count = $1, last_verified_tip = $2
		WHERE blockheight = $3 AND hash = $4`, tableName)
	_, err := s.pool.Exec(ctx, query, stabilityCount, lastTip, height, hash)
	return err
}

func (s *realBlockStore) GetBlockStats(ctx context.Context) (*database.BlockStats, error) {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`SELECT status, COUNT(*) as count FROM %s GROUP BY status`, tableName)
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &database.BlockStats{}
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
		}
	}
	return stats, rows.Err()
}

func (s *realBlockStore) UpdateBlockConfirmationState(ctx context.Context, height uint64, hash string, status string, confirmationProgress float64, orphanMismatchCount int, stabilityCheckCount int, lastVerifiedTip string) error {
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	query := fmt.Sprintf(`UPDATE %s SET status=$1, confirmationprogress=$2, orphan_mismatch_count=$3, stability_check_count=$4, last_verified_tip=$5 WHERE blockheight=$6 AND hash=$7`, tableName)
	_, err := s.pool.Exec(ctx, query, status, confirmationProgress, orphanMismatchCount, stabilityCheckCount, lastVerifiedTip, height, hash)
	return err
}

// TestIntegration_PaymentCycle_PendingToConfirmed verifies the complete
// payment pipeline: insert a pending block in real PostgreSQL, run 3 process
// cycles with a mock daemon returning sufficient confirmations, then verify
// the block transitions to "confirmed" with stability_check_count = 3.
func TestIntegration_PaymentCycle_PendingToConfirmed(t *testing.T) {
	pool := integrationDBPool(t)
	defer pool.Close()

	dropIntegrationBlocksTable(t, pool)
	createIntegrationBlocksTable(t, pool)
	defer dropIntegrationBlocksTable(t, pool)

	ctx := context.Background()

	blockHeight := uint64(50000)
	blockHash := "aaaa111122223333bbbb444455556666"
	chainTipHash := "cccc777788889999dddd000011112222"
	chainTipHeight := blockHeight + 200 // Well beyond maturity

	// Insert a pending block directly into the real database
	tableName := fmt.Sprintf("blocks_%s", integrationTestPoolID)
	insertQuery := fmt.Sprintf(`INSERT INTO %s (poolid, blockheight, status, type, miner, reward, hash, created)
		VALUES ($1, $2, 'pending', 'block', $3, $4, $5, NOW())`, tableName)
	_, err := pool.Exec(ctx, insertQuery,
		integrationTestPoolID, blockHeight, "DTestMinerIntegration", 7812.5, blockHash)
	if err != nil {
		t.Fatalf("Failed to insert test block: %v", err)
	}

	// Create a block store backed by real PostgreSQL
	store := &realBlockStore{pool: pool}

	// Create mock daemon that returns consistent chain state
	rpc := newMockDaemonRPC()
	rpc.setChainTip(chainTipHeight, chainTipHash)
	rpc.setBlockHash(blockHeight, blockHash) // Hash matches — block is valid

	// Create processor with low maturity (10) so our chain tip is well past it
	paymentsCfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: 10,
	}
	logger := zap.NewNop()
	proc := NewProcessor(paymentsCfg, &config.PoolConfig{ID: integrationTestPoolID, Coin: "BTC"}, store, rpc, logger)

	// Run 3 process cycles — the stability window requires 3 consecutive stable checks
	// before confirming a block (StabilityWindowChecks = 3).
	for i := 0; i < StabilityWindowChecks; i++ {
		proc.processCycle(ctx)
	}

	// Query the database to verify the block reached "confirmed"
	var status string
	var confirmProgress float64
	var stabilityCount int
	selectQuery := fmt.Sprintf(`SELECT status, confirmationprogress, stability_check_count
		FROM %s WHERE hash = $1`, tableName)
	err = pool.QueryRow(ctx, selectQuery, blockHash).Scan(&status, &confirmProgress, &stabilityCount)
	if err != nil {
		t.Fatalf("Failed to query block after processing: %v", err)
	}

	if status != StatusConfirmed {
		t.Errorf("Block status = %q, want %q after %d cycles", status, StatusConfirmed, StabilityWindowChecks)
	}

	if stabilityCount != StabilityWindowChecks {
		t.Errorf("stability_check_count = %d, want %d", stabilityCount, StabilityWindowChecks)
	}

	if confirmProgress != 1.0 {
		t.Errorf("confirmationprogress = %f, want 1.0", confirmProgress)
	}

	// Verify via GetBlockStats
	stats, err := store.GetBlockStats(ctx)
	if err != nil {
		t.Fatalf("GetBlockStats() error: %v", err)
	}
	if stats.Confirmed != 1 {
		t.Errorf("BlockStats.Confirmed = %d, want 1", stats.Confirmed)
	}
	if stats.Pending != 0 {
		t.Errorf("BlockStats.Pending = %d, want 0", stats.Pending)
	}
}
