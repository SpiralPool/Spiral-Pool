// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Advisory lock integration tests — require a real PostgreSQL connection.
//
// These tests verify the dedicated-connection advisory lock fix that prevents
// the connection pool bug where lock and unlock hit different PG sessions.
//
// To run:
//
//	SPIRAL_TEST_DB_URL="postgres://user:pass@localhost:5432/spiral_test?sslmode=disable" \
//	  go test -v -run TestIntegration_AdvisoryLock ./internal/database/
package database

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIntegration_AdvisoryLock_SameSession verifies that TryAdvisoryLock and
// ReleaseAdvisoryLock use the same PostgreSQL backend PID (same session).
// pg_locks must show the lock while held, and not show it after release.
func TestIntegration_AdvisoryLock_SameSession(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	ctx := context.Background()
	lockID := int64(999001) // unique test lock ID

	// Acquire lock
	acquired, err := db.TryAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("TryAdvisoryLock() error: %v", err)
	}
	if !acquired {
		t.Fatal("TryAdvisoryLock() returned false — expected to acquire lock")
	}

	// Verify lock is visible in pg_locks
	var lockCount int
	err = pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted = true",
		lockID,
	).Scan(&lockCount)
	if err != nil {
		t.Fatalf("pg_locks query failed: %v", err)
	}
	if lockCount == 0 {
		t.Error("Advisory lock not visible in pg_locks after acquisition")
	}

	// Record the backend PID of the dedicated connection
	db.advisoryMu.Lock()
	conn := db.advisoryConn
	db.advisoryMu.Unlock()
	if conn == nil {
		t.Fatal("advisoryConn is nil after successful lock acquisition")
	}
	var lockPID uint32
	err = conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&lockPID)
	if err != nil {
		t.Fatalf("Failed to get backend PID: %v", err)
	}
	if lockPID == 0 {
		t.Error("Backend PID is 0 — expected a valid PID")
	}

	// Release lock
	err = db.ReleaseAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("ReleaseAdvisoryLock() error: %v", err)
	}

	// Verify lock is no longer in pg_locks
	err = pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted = true",
		lockID,
	).Scan(&lockCount)
	if err != nil {
		t.Fatalf("pg_locks query after release failed: %v", err)
	}
	if lockCount != 0 {
		t.Errorf("Advisory lock still visible in pg_locks after release (count=%d)", lockCount)
	}

	// advisoryConn should be nil after release
	db.advisoryMu.Lock()
	connAfter := db.advisoryConn
	db.advisoryMu.Unlock()
	if connAfter != nil {
		t.Error("advisoryConn should be nil after ReleaseAdvisoryLock")
	}
}

// TestIntegration_AdvisoryLock_ExclusiveAcrossInstances verifies that two
// PostgresDB instances compete for the same advisory lock — only one can hold
// it at a time. After the first releases, the second can acquire it.
func TestIntegration_AdvisoryLock_ExclusiveAcrossInstances(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()

	dbA := testPostgresDB(t, pool)
	dbB := testPostgresDB(t, pool)

	ctx := context.Background()
	lockID := int64(999002)

	// A acquires lock
	acquiredA, err := dbA.TryAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("A.TryAdvisoryLock() error: %v", err)
	}
	if !acquiredA {
		t.Fatal("A should acquire the lock")
	}

	// B tries to acquire — should fail (lock held by A)
	acquiredB, err := dbB.TryAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("B.TryAdvisoryLock() error: %v", err)
	}
	if acquiredB {
		t.Error("B should NOT acquire the lock while A holds it")
		// Clean up B's lock if acquired
		_ = dbB.ReleaseAdvisoryLock(ctx, lockID)
	}

	// A releases
	if err := dbA.ReleaseAdvisoryLock(ctx, lockID); err != nil {
		t.Fatalf("A.ReleaseAdvisoryLock() error: %v", err)
	}

	// B retries — should now succeed
	acquiredB2, err := dbB.TryAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("B.TryAdvisoryLock() retry error: %v", err)
	}
	if !acquiredB2 {
		t.Error("B should acquire the lock after A releases it")
	}

	// Clean up
	_ = dbB.ReleaseAdvisoryLock(ctx, lockID)
}

// TestIntegration_AdvisoryLock_ReleaseWithoutAcquire verifies that calling
// ReleaseAdvisoryLock when no lock is held is a safe no-op (no error, no panic).
func TestIntegration_AdvisoryLock_ReleaseWithoutAcquire(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()
	db := testPostgresDB(t, pool)

	ctx := context.Background()
	lockID := int64(999003)

	// Release without ever acquiring — should be a no-op
	err := db.ReleaseAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Errorf("ReleaseAdvisoryLock() without prior acquire should be a no-op, got error: %v", err)
	}
}

// TestIntegration_AdvisoryLock_ConcurrentContention runs 5 PostgresDB instances
// racing for the same lock across 3 rounds. Exactly 1 winner per round.
func TestIntegration_AdvisoryLock_ConcurrentContention(t *testing.T) {
	pool := testDBPool(t)
	defer pool.Close()

	const numInstances = 5
	const numRounds = 3
	lockID := int64(999004)

	instances := make([]*PostgresDB, numInstances)
	for i := range instances {
		instances[i] = testPostgresDB(t, pool)
	}

	for round := 0; round < numRounds; round++ {
		var winners int32
		var wg sync.WaitGroup
		wg.Add(numInstances)

		for i := 0; i < numInstances; i++ {
			go func(idx int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				acquired, err := instances[idx].TryAdvisoryLock(ctx, lockID)
				if err != nil {
					t.Errorf("Round %d, instance %d: TryAdvisoryLock error: %v", round, idx, err)
					return
				}
				if acquired {
					atomic.AddInt32(&winners, 1)
				}
			}(i)
		}
		wg.Wait()

		if winners != 1 {
			t.Errorf("Round %d: expected exactly 1 winner, got %d", round, winners)
		}

		// Release from the winner so next round can proceed
		ctx := context.Background()
		for _, inst := range instances {
			// Release is a no-op if this instance didn't hold the lock
			_ = inst.ReleaseAdvisoryLock(ctx, lockID)
		}
	}
}

// TestIntegration_AdvisoryLock_ConnectionDeath verifies that killing the
// lock-holding PostgreSQL backend (via pg_terminate_backend) auto-releases
// the advisory lock, allowing another instance to acquire it.
func TestIntegration_AdvisoryLock_ConnectionDeath(t *testing.T) {
	// Use separate pools for A and B so that killing A's backend doesn't
	// poison B's pool with a stale FATAL-state connection.
	poolA := testDBPool(t)
	defer poolA.Close()
	poolB := testDBPool(t)
	defer poolB.Close()

	dbA := testPostgresDB(t, poolA)
	dbB := testPostgresDB(t, poolB)

	ctx := context.Background()
	lockID := int64(999005)

	// A acquires lock
	acquiredA, err := dbA.TryAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("A.TryAdvisoryLock() error: %v", err)
	}
	if !acquiredA {
		t.Fatal("A should acquire the lock")
	}

	// Get A's backend PID
	dbA.advisoryMu.Lock()
	connA := dbA.advisoryConn
	dbA.advisoryMu.Unlock()
	if connA == nil {
		t.Fatal("A.advisoryConn is nil after lock acquisition")
	}
	var pidA int
	if err := connA.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pidA); err != nil {
		t.Fatalf("Failed to get A's backend PID: %v", err)
	}

	// Kill A's backend via pg_terminate_backend (uses B's pool to avoid poisoning)
	var terminated bool
	err = poolB.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pidA).Scan(&terminated)
	if err != nil {
		t.Fatalf("pg_terminate_backend() error: %v", err)
	}
	if !terminated {
		t.Fatal("pg_terminate_backend returned false — could not kill connection")
	}

	// Small delay for PostgreSQL to clean up the terminated session
	time.Sleep(500 * time.Millisecond)

	// Clean up A's stale state so we don't leak a dead connection reference.
	// In production, the connection error would surface on the next use.
	dbA.advisoryMu.Lock()
	if dbA.advisoryConn != nil {
		dbA.advisoryConn.Release()
		dbA.advisoryConn = nil
	}
	dbA.advisoryMu.Unlock()

	// B should now be able to acquire the lock (auto-released by session death)
	acquiredB, err := dbB.TryAdvisoryLock(ctx, lockID)
	if err != nil {
		t.Fatalf("B.TryAdvisoryLock() after A's death error: %v", err)
	}
	if !acquiredB {
		t.Error("B should acquire the lock after A's connection was terminated")
	}

	// Clean up
	_ = dbB.ReleaseAdvisoryLock(ctx, lockID)
}
