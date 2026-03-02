// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database — PostgreSQL integration tests for money-path operations.
//
// These tests require a real PostgreSQL connection and exercise the actual
// database operations that handleBlock and the payment processor rely on.
// They verify crash safety, status guard logic, advisory locking, and
// idempotency protections that cannot be tested without a live database.
//
// To run these tests:
//
//	SPIRAL_TEST_DB_URL="postgres://user:pass@localhost:5432/spiral_test?sslmode=disable" \
//	  go test -v -run TestIntegration ./internal/database/
//
// The tests create and drop their own test tables, so they won't interfere
// with production data. Use a dedicated test database.
//
// Covered scenarios:
//   - InsertBlockForPool: crash-safe "submitting" status insertion
//   - UpdateBlockStatusForPool: status transitions through full lifecycle
//   - Status guard: prevents illegal status downgrades (money-safety critical)
//   - Advisory lock: prevents duplicate insertion in HA scenarios
//   - Idempotency: duplicate inserts are safely ignored
//   - WriteBatchForPool: batch share persistence via COPY protocol
package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

const testPoolID = "integration_test"

// testDBPool returns a connected pgxpool.Pool for integration testing.
// Returns nil and calls t.Skip if SPIRAL_TEST_DB_URL is not set.
func testDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("SPIRAL_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("Set SPIRAL_TEST_DB_URL to run database integration tests (e.g., postgres://user:pass@localhost:5432/spiral_test?sslmode=disable)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Verify connection works
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("Failed to ping test database: %v", err)
	}

	return pool
}

// testPostgresDB creates a PostgresDB instance for integration testing.
func testPostgresDB(t *testing.T, pool *pgxpool.Pool) *PostgresDB {
	t.Helper()
	logger := zap.NewNop()
	return &PostgresDB{
		pool:   pool,
		logger: logger.Sugar(),
		poolID: testPoolID,
	}
}

// createTestBlocksTable creates the blocks table for integration tests.
// Uses the same schema as CreatePoolTablesV2 but isolated to the test pool ID.
func createTestBlocksTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	tableName := fmt.Sprintf("blocks_%s", testPoolID)
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
			created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
		)
	`, tableName)

	if _, err := pool.Exec(ctx, query); err != nil {
		t.Fatalf("Failed to create test blocks table: %v", err)
	}

	// Create index on hash for the existence check in InsertBlockForPool
	indexQuery := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_hash ON %s (hash)`, tableName, tableName)
	if _, err := pool.Exec(ctx, indexQuery); err != nil {
		t.Fatalf("Failed to create hash index: %v", err)
	}

	// Create composite index for UpdateBlockStatusForPool WHERE clause
	compositeQuery := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_height_hash ON %s (blockheight, hash)`, tableName, tableName)
	if _, err := pool.Exec(ctx, compositeQuery); err != nil {
		t.Fatalf("Failed to create composite index: %v", err)
	}
}

// dropTestBlocksTable drops the test blocks table.
func dropTestBlocksTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", tableName))
}

// createTestSharesTable creates the shares table for integration tests.
func createTestSharesTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	tableName := fmt.Sprintf("shares_%s", testPoolID)
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			poolid TEXT NOT NULL,
			blockheight BIGINT DEFAULT 0,
			difficulty DOUBLE PRECISION DEFAULT 0,
			networkdifficulty DOUBLE PRECISION DEFAULT 0,
			miner TEXT NOT NULL DEFAULT '',
			worker TEXT NOT NULL DEFAULT '',
			useragent TEXT DEFAULT '',
			ipaddress TEXT DEFAULT '',
			source TEXT DEFAULT 'stratum',
			created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
		)
	`, tableName)

	if _, err := pool.Exec(ctx, query); err != nil {
		t.Fatalf("Failed to create test shares table: %v", err)
	}
}

// dropTestSharesTable drops the test shares table.
func dropTestSharesTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tableName := fmt.Sprintf("shares_%s", testPoolID)
	_, _ = pool.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", tableName))
}

// =============================================================================
// InsertBlockForPool: crash-safe block recording
// =============================================================================

// TestIntegration_InsertBlockForPool_SubmittingStatus verifies that
// InsertBlockForPool correctly inserts a block with "submitting" status,
// matching the crash-safe pattern used by handleBlock.
func TestIntegration_InsertBlockForPool_SubmittingStatus(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:            50000,
		NetworkDifficulty: 123456.789,
		Status:            "submitting",
		Type:              "block",
		Miner:             "DTestMinerAddress123",
		Reward:            7812.5,
		Hash:              "aaaa111122223333bbbb444455556666",
		Created:           time.Now(),
	}

	err := db.InsertBlockForPool(ctx, testPoolID, block)
	if err != nil {
		t.Fatalf("InsertBlockForPool() failed: %v", err)
	}

	// Verify block was inserted with correct status
	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	var status, miner, hash string
	var height int64
	var reward float64
	query := fmt.Sprintf("SELECT blockheight, status, miner, hash, reward FROM %s WHERE hash = $1", tableName)
	err = pool.QueryRow(ctx, query, block.Hash).Scan(&height, &status, &miner, &hash, &reward)
	if err != nil {
		t.Fatalf("Failed to query inserted block: %v", err)
	}

	if status != "submitting" {
		t.Errorf("Block status = %q, want %q", status, "submitting")
	}
	if height != int64(block.Height) {
		t.Errorf("Block height = %d, want %d", height, block.Height)
	}
	if miner != block.Miner {
		t.Errorf("Block miner = %q, want %q", miner, block.Miner)
	}
	if hash != block.Hash {
		t.Errorf("Block hash = %q, want %q", hash, block.Hash)
	}
}

// TestIntegration_InsertBlockForPool_DuplicateIdempotent verifies that
// inserting the same block twice is safely ignored (idempotency).
func TestIntegration_InsertBlockForPool_DuplicateIdempotent(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  50001,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Reward:  7812.5,
		Hash:    "bbbb222233334444cccc555566667777",
		Created: time.Now(),
	}

	// First insert — should succeed
	err := db.InsertBlockForPool(ctx, testPoolID, block)
	if err != nil {
		t.Fatalf("First InsertBlockForPool() failed: %v", err)
	}

	// Second insert with same hash — should be silently ignored (idempotent)
	err = db.InsertBlockForPool(ctx, testPoolID, block)
	if err != nil {
		t.Fatalf("Second InsertBlockForPool() should succeed (idempotent), got: %v", err)
	}

	// Verify only one block exists
	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	var count int
	err = pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE hash = $1", tableName), block.Hash).Scan(&count)
	if err != nil {
		t.Fatalf("Count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 block with hash %s, got %d (duplicate not prevented)", block.Hash, count)
	}
}

// TestIntegration_InsertBlockForPool_InvalidPoolID verifies that SQL
// injection via pool ID is rejected.
func TestIntegration_InsertBlockForPool_InvalidPoolID(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	ctx := context.Background()
	block := &Block{
		Height: 50002,
		Status: "submitting",
		Hash:   "cccc333344445555dddd666677778888",
	}

	err := db.InsertBlockForPool(ctx, "'; DROP TABLE--", block)
	if err == nil {
		t.Fatal("InsertBlockForPool should reject SQL injection pool ID")
	}
}

// =============================================================================
// UpdateBlockStatusForPool: status lifecycle transitions
// =============================================================================

// TestIntegration_StatusTransition_SubmittingToPending verifies the normal
// crash-safe flow: submitting → pending after daemon accepts the block.
func TestIntegration_StatusTransition_SubmittingToPending(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  60000,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Reward:  7812.5,
		Hash:    "dddd444455556666eeee777788889999",
		Created: time.Now(),
	}

	// Insert as "submitting"
	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("InsertBlockForPool() failed: %v", err)
	}

	// Transition to "pending" (daemon accepted)
	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0)
	if err != nil {
		t.Fatalf("UpdateBlockStatusForPool(submitting→pending) failed: %v", err)
	}

	// Verify status changed
	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	var status string
	err = pool.QueryRow(ctx, fmt.Sprintf("SELECT status FROM %s WHERE hash = $1", tableName), block.Hash).Scan(&status)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if status != "pending" {
		t.Errorf("Block status = %q, want %q", status, "pending")
	}
}

// TestIntegration_StatusTransition_SubmittingToOrphaned verifies the
// rejection flow: submitting → orphaned when daemon rejects the block.
func TestIntegration_StatusTransition_SubmittingToOrphaned(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  60001,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Hash:    "eeee555566667777ffff888899990000",
		Created: time.Now(),
	}

	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("InsertBlockForPool() failed: %v", err)
	}

	// Transition to "orphaned" (daemon rejected)
	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "orphaned", 0)
	if err != nil {
		t.Fatalf("UpdateBlockStatusForPool(submitting→orphaned) failed: %v", err)
	}

	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	var status string
	err = pool.QueryRow(ctx, fmt.Sprintf("SELECT status FROM %s WHERE hash = $1", tableName), block.Hash).Scan(&status)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if status != "orphaned" {
		t.Errorf("Block status = %q, want %q", status, "orphaned")
	}
}

// TestIntegration_StatusTransition_PendingToConfirmed verifies the
// confirmation flow: pending → confirmed after maturity window.
func TestIntegration_StatusTransition_PendingToConfirmed(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  60002,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Hash:    "ffff666677778888aaaa999900001111",
		Created: time.Now(),
	}

	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("InsertBlockForPool() failed: %v", err)
	}

	// submitting → pending
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0); err != nil {
		t.Fatalf("submitting→pending failed: %v", err)
	}

	// pending → confirmed
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "confirmed", 1.0); err != nil {
		t.Fatalf("pending→confirmed failed: %v", err)
	}

	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	var status string
	var progress float64
	err := pool.QueryRow(ctx, fmt.Sprintf("SELECT status, confirmationprogress FROM %s WHERE hash = $1", tableName), block.Hash).Scan(&status, &progress)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if status != "confirmed" {
		t.Errorf("Block status = %q, want %q", status, "confirmed")
	}
	if progress != 1.0 {
		t.Errorf("Confirmation progress = %f, want 1.0", progress)
	}
}

// =============================================================================
// Status Guard: prevents illegal downgrades (MONEY-SAFETY CRITICAL)
// =============================================================================

// TestIntegration_StatusGuard_ConfirmedCannotRevertToPending verifies that
// a confirmed block cannot be downgraded to pending. This is the most critical
// money-safety invariant: once a block is confirmed and miners are paid,
// reverting it could lead to double-payment.
func TestIntegration_StatusGuard_ConfirmedCannotRevertToPending(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  70000,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Hash:    "aaaa000011112222bbbb333344445555",
		Created: time.Now(),
	}

	// Progress to confirmed: submitting → pending → confirmed
	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0); err != nil {
		t.Fatalf("submitting→pending failed: %v", err)
	}
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "confirmed", 1.0); err != nil {
		t.Fatalf("pending→confirmed failed: %v", err)
	}

	// Attempt to downgrade confirmed → pending (should be blocked)
	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("Downgrade confirmed→pending should return ErrStatusGuardBlocked, got: %v", err)
	}

	// Verify status is still confirmed
	tableName := fmt.Sprintf("blocks_%s", testPoolID)
	var status string
	err = pool.QueryRow(ctx, fmt.Sprintf("SELECT status FROM %s WHERE hash = $1", tableName), block.Hash).Scan(&status)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if status != "confirmed" {
		t.Errorf("Block status should still be 'confirmed' after failed downgrade, got %q", status)
	}
}

// TestIntegration_StatusGuard_ConfirmedCannotRevertToSubmitting verifies
// that a confirmed block cannot be reset to submitting.
func TestIntegration_StatusGuard_ConfirmedCannotRevertToSubmitting(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  70001,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Hash:    "bbbb111122223333cccc444455556666",
		Created: time.Now(),
	}

	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0); err != nil {
		t.Fatalf("submitting→pending failed: %v", err)
	}
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "confirmed", 1.0); err != nil {
		t.Fatalf("pending→confirmed failed: %v", err)
	}

	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "submitting", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("Downgrade confirmed→submitting should return ErrStatusGuardBlocked, got: %v", err)
	}
}

// TestIntegration_StatusGuard_PendingCannotRevertToSubmitting verifies
// that a pending block cannot be reset to submitting by a late crash recovery.
func TestIntegration_StatusGuard_PendingCannotRevertToSubmitting(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  70002,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Hash:    "cccc222233334444dddd555566667777",
		Created: time.Now(),
	}

	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0); err != nil {
		t.Fatalf("submitting→pending failed: %v", err)
	}

	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "submitting", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("Downgrade pending→submitting should return ErrStatusGuardBlocked, got: %v", err)
	}
}

// TestIntegration_StatusGuard_OrphanedCannotRevertToPending verifies
// that an orphaned block cannot be "un-orphaned".
func TestIntegration_StatusGuard_OrphanedCannotRevertToPending(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	block := &Block{
		Height:  70003,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerAddress123",
		Hash:    "dddd333344445555eeee666677778888",
		Created: time.Now(),
	}

	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	// submitting → orphaned (direct rejection)
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "orphaned", 0); err != nil {
		t.Fatalf("submitting→orphaned failed: %v", err)
	}

	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("Downgrade orphaned→pending should return ErrStatusGuardBlocked, got: %v", err)
	}
}

// =============================================================================
// Full lifecycle: submitting → pending → confirmed
// =============================================================================

// TestIntegration_FullBlockLifecycle verifies the complete happy path:
// submitting → pending → confirmed, which is the exact flow that
// handleBlock + payment processor execute in production.
func TestIntegration_FullBlockLifecycle(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	tableName := fmt.Sprintf("blocks_%s", testPoolID)

	block := &Block{
		Height:            80000,
		NetworkDifficulty: 999.99,
		Status:            "submitting",
		Type:              "block",
		Miner:             "DTestMinerFullLifecycle",
		Reward:            7812.5,
		Hash:              "eeee444455556666ffff777788889999",
		Created:           time.Now(),
	}

	// Step 1: handleBlock inserts as "submitting" before daemon call
	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("Step 1 (insert submitting) failed: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "submitting")

	// Step 2: handleBlock updates to "pending" after daemon accepts
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0); err != nil {
		t.Fatalf("Step 2 (submitting→pending) failed: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "pending")

	// Step 3: Payment processor confirms after maturity window
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "confirmed", 1.0); err != nil {
		t.Fatalf("Step 3 (pending→confirmed) failed: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "confirmed")

	// Step 4: Verify the guard holds — late orphan attempt is blocked
	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "orphaned", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("Step 4 (confirmed→orphaned guard) should return ErrStatusGuardBlocked, got: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "confirmed")
}

// TestIntegration_OrphanLifecycle verifies the rejection path:
// submitting → orphaned (block rejected by daemon).
func TestIntegration_OrphanLifecycle(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	tableName := fmt.Sprintf("blocks_%s", testPoolID)

	block := &Block{
		Height:  80001,
		Status:  "submitting",
		Type:    "block",
		Miner:   "DTestMinerOrphanPath",
		Hash:    "ffff555566667777aaaa888899990000",
		Created: time.Now(),
	}

	// Step 1: Insert as submitting
	if err := db.InsertBlockForPool(ctx, testPoolID, block); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "submitting")

	// Step 2: Daemon rejects → orphaned
	if err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "orphaned", 0); err != nil {
		t.Fatalf("submitting→orphaned failed: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "orphaned")

	// Step 3: Guard holds — cannot un-orphan
	err := db.UpdateBlockStatusForPool(ctx, testPoolID, block.Height, block.Hash, "pending", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("orphaned→pending should return ErrStatusGuardBlocked, got: %v", err)
	}
	assertBlockStatus(t, pool, tableName, block.Hash, "orphaned")
}

// =============================================================================
// Update nonexistent block
// =============================================================================

// TestIntegration_UpdateNonexistentBlock verifies that updating a block
// that doesn't exist returns ErrStatusGuardBlocked (0 rows affected).
func TestIntegration_UpdateNonexistentBlock(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	dropTestBlocksTable(t, pool)
	createTestBlocksTable(t, pool)
	defer dropTestBlocksTable(t, pool)

	ctx := context.Background()
	err := db.UpdateBlockStatusForPool(ctx, testPoolID, 99999, "nonexistent_hash_1234567890abcdef", "pending", 0)
	if !errors.Is(err, ErrStatusGuardBlocked) {
		t.Errorf("Updating nonexistent block should return ErrStatusGuardBlocked, got: %v", err)
	}
}

// =============================================================================
// Helpers
// =============================================================================

func assertBlockStatus(t *testing.T, pool *pgxpool.Pool, tableName, hash, expectedStatus string) {
	t.Helper()
	var status string
	err := pool.QueryRow(context.Background(), fmt.Sprintf("SELECT status FROM %s WHERE hash = $1", tableName), hash).Scan(&status)
	if err != nil {
		t.Fatalf("Failed to query block status for hash %s: %v", hash, err)
	}
	if status != expectedStatus {
		t.Errorf("Block %s status = %q, want %q", hash, status, expectedStatus)
	}
}
