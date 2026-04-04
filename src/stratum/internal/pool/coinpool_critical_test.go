// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool — Critical function tests for CoinPool.
//
// Covers audit items #3 and #8: untested money-critical and lifecycle functions.
//
// Functions under test:
//   - verifyBlockAcceptance() — post-timeout block chain verification
//   - waitForSync()           — daemon sync gate before mining starts
//   - cleanupStaleShares()    — startup share cleanup
//   - Background loop lifecycle (pollingLoop, zmqPromotionLoop, celebrationLoop,
//     reconciliationLoop, statsLoop, difficultyLoop, sessionCleanupLoop)
//
// Uses the mock pattern established in handleblock_production_test.go.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/nodemanager"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Mock extensions for critical function tests
// ═══════════════════════════════════════════════════════════════════════════════

// criticalMockNodeMgr extends mockNodeMgr with configurable behavior for
// functions not exercised in the handleBlock tests: GetDifficulty, IsZMQFailed,
// IsZMQStable, HasZMQ, Stats, Start, Stop, and GetPrimary.
type criticalMockNodeMgr struct {
	mockNodeMgr

	// GetDifficulty behavior
	difficulty    float64
	difficultyErr error

	// ZMQ state
	zmqFailed bool
	zmqStable bool
	hasZMQ    bool

	// Start/Stop
	startErr error
	stopErr  error
}

func newCriticalMockNodeMgr() *criticalMockNodeMgr {
	return &criticalMockNodeMgr{
		mockNodeMgr: mockNodeMgr{
			blockHashByHeight: make(map[uint64]string),
			blockInfoByHash:   make(map[string]map[string]interface{}),
		},
	}
}

func (m *criticalMockNodeMgr) GetDifficulty(ctx context.Context) (float64, error) {
	return m.difficulty, m.difficultyErr
}

func (m *criticalMockNodeMgr) IsZMQFailed() bool  { return m.zmqFailed }
func (m *criticalMockNodeMgr) IsZMQStable() bool   { return m.zmqStable }
func (m *criticalMockNodeMgr) HasZMQ() bool         { return m.hasZMQ }
func (m *criticalMockNodeMgr) Start(ctx context.Context) error { return m.startErr }
func (m *criticalMockNodeMgr) Stop() error          { return m.stopErr }
func (m *criticalMockNodeMgr) Stats() nodemanager.ManagerStats { return nodemanager.ManagerStats{} }
func (m *criticalMockNodeMgr) GetPrimary() *nodemanager.ManagedNode { return nil }

// criticalMockDB extends mockDB with configurable behavior for functions not
// exercised in the handleBlock tests: CleanupStaleShares, GetPoolHashrateForPool,
// UpdatePoolStatsForPool, GetPoolHashrate.
type criticalMockDB struct {
	mockDB

	// CleanupStaleShares behavior
	cleanupDeletedCount int64
	cleanupErr          error
	cleanupCalled       bool
	cleanupRetention    int

	// GetPoolHashrateForPool behavior
	poolHashrate    float64
	poolHashrateErr error

	// UpdatePoolStatsForPool behavior
	updateStatsErr     error
	updateStatsCalled  bool
	lastPoolStats      *database.PoolStats

	// GetPoolHashrate behavior
	globalHashrate    float64
	globalHashrateErr error
}

func newCriticalMockDB() *criticalMockDB {
	return &criticalMockDB{}
}

func (m *criticalMockDB) CleanupStaleSharesForPool(ctx context.Context, poolID string, retentionMinutes int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupCalled = true
	m.cleanupRetention = retentionMinutes
	return m.cleanupDeletedCount, m.cleanupErr
}

func (m *criticalMockDB) GetPoolHashrateForPool(ctx context.Context, poolID string, windowMinutes int, algorithm string) (float64, error) {
	return m.poolHashrate, m.poolHashrateErr
}

func (m *criticalMockDB) UpdatePoolStatsForPool(ctx context.Context, poolID string, stats *database.PoolStats) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatsCalled = true
	m.lastPoolStats = stats
	return m.updateStatsErr
}

func (m *criticalMockDB) GetBlocksByStatusForPool(ctx context.Context, poolID string, status string) ([]*database.Block, error) {
	return m.blocksByStatus[status], nil
}

// newCriticalTestCoinPool constructs a minimal CoinPool for critical function testing.
// Uses the same pattern as newTestCoinPool from handleblock_production_test.go but
// with the extended mocks that support more methods.
func newCriticalTestCoinPool(nm coinPoolNodeManager, db coinPoolDB) *CoinPool {
	ctx, cancel := context.WithCancel(context.Background())
	cp := &CoinPool{
		nodeManager: nm,
		db:          db,
		logger:      zap.NewNop().Sugar(),
		poolID:      "test-pool",
		coinSymbol:  "TEST",
		submitTimeouts: &daemon.SubmitTimeouts{
			SubmitTimeout:   100 * time.Millisecond,
			VerifyTimeout:   100 * time.Millisecond,
			PreciousTimeout: 50 * time.Millisecond,
			TotalBudget:     200 * time.Millisecond,
			RetryTimeout:    100 * time.Millisecond,
			MaxRetries:      2,
			RetrySleep:      1 * time.Millisecond,
			SubmitDeadline:  200 * time.Millisecond,
		},
		roleCtx:    ctx,
		roleCancel: cancel,
	}
	return cp
}

// ═══════════════════════════════════════════════════════════════════════════════
// verifyBlockAcceptance() tests — MONEY CRITICAL
// ═══════════════════════════════════════════════════════════════════════════════

// TestVerifyBlockAcceptance_BlockInActiveChain tests that verifyBlockAcceptance
// returns true when GetBlockHash returns a matching hash at the expected height.
// This is the happy path: the daemon accepted our block.
func TestVerifyBlockAcceptance_BlockInActiveChain(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	nm.blockHashByHeight[testHeight] = testBlockHash

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	result := cp.verifyBlockAcceptance(testBlockHash, testHeight)
	if !result {
		t.Error("verifyBlockAcceptance should return true when block hash matches at height")
	}
}

// TestVerifyBlockAcceptance_BlockNotInActiveChain tests that verifyBlockAcceptance
// returns false when the hash at the expected height does not match our block.
// This covers the case where another miner's block won the race.
func TestVerifyBlockAcceptance_BlockNotInActiveChain(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	differentHash := "000000000000000000000000000000000000000000000000000000000000ffff"
	nm.blockHashByHeight[testHeight] = differentHash
	// GetBlock also fails — block not found by hash either
	nm.getBlockErr = fmt.Errorf("block not found")

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	result := cp.verifyBlockAcceptance(testBlockHash, testHeight)
	if result {
		t.Error("verifyBlockAcceptance should return false when block hash doesn't match at height")
	}
}

// TestVerifyBlockAcceptance_DaemonRPCError tests that verifyBlockAcceptance
// returns false when all RPC calls fail across all retry attempts.
func TestVerifyBlockAcceptance_DaemonRPCError(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	nm.blockHashErr = errors.New("connection refused")

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	result := cp.verifyBlockAcceptance(testBlockHash, testHeight)
	if result {
		t.Error("verifyBlockAcceptance should return false when daemon RPC fails on all attempts")
	}
}

// TestVerifyBlockAcceptance_GetBlockFallback tests that when GetBlockHash returns
// a different hash at the target height but GetBlock finds our block by hash
// (e.g., at a different height due to reorg timing), it returns true.
func TestVerifyBlockAcceptance_GetBlockFallback(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	// Different hash at height — triggers GetBlock fallback
	differentHash := "000000000000000000000000000000000000000000000000000000000000ffff"
	nm.blockHashByHeight[testHeight] = differentHash
	// GetBlock finds our block with positive confirmations
	nm.blockInfoByHash[testBlockHash] = map[string]interface{}{
		"confirmations": float64(1),
		"height":        float64(testHeight),
	}

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	result := cp.verifyBlockAcceptance(testBlockHash, testHeight)
	if !result {
		t.Error("verifyBlockAcceptance should return true when GetBlock finds block in chain by hash")
	}
}

// TestVerifyBlockAcceptance_GetBlockFallback_StaleFork tests that when GetBlock
// finds the block but with negative confirmations (stale fork), it does NOT return true.
func TestVerifyBlockAcceptance_GetBlockFallback_StaleFork(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	differentHash := "000000000000000000000000000000000000000000000000000000000000ffff"
	nm.blockHashByHeight[testHeight] = differentHash
	// GetBlock finds block but on a stale fork (negative confirmations)
	nm.blockInfoByHash[testBlockHash] = map[string]interface{}{
		"confirmations": float64(-1),
		"height":        float64(testHeight),
	}

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	result := cp.verifyBlockAcceptance(testBlockHash, testHeight)
	if result {
		t.Error("verifyBlockAcceptance should return false when block is on stale fork (negative confirmations)")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// waitForSync() tests — CRITICAL
// ═══════════════════════════════════════════════════════════════════════════════

// TestWaitForSync_AlreadySynced tests that waitForSync returns immediately
// when the daemon is already fully synced (IBD=false, progress >= threshold).
func TestWaitForSync_AlreadySynced(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	nm.blockchainInfo = &daemon.BlockchainInfo{
		Blocks:               500000,
		Headers:              500000,
		VerificationProgress: 0.999999,
		InitialBlockDownload: false,
	}

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// waitForSync uses a 10s ticker internally; the first tick should resolve.
	// We use a timeout to ensure the test doesn't hang if something breaks.
	done := make(chan error, 1)
	go func() {
		done <- cp.waitForSync(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waitForSync returned unexpected error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("waitForSync did not return within 20 seconds for already-synced daemon")
	}
}

// TestWaitForSync_ContextCancelled tests that waitForSync returns the context error
// when the context is cancelled while waiting for the daemon to sync.
func TestWaitForSync_ContextCancelled(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	// Daemon is still syncing
	nm.blockchainInfo = &daemon.BlockchainInfo{
		Blocks:               100,
		Headers:              500000,
		VerificationProgress: 0.0002,
		InitialBlockDownload: true,
	}

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- cp.waitForSync(ctx)
	}()

	// Cancel after a brief moment
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("waitForSync should return error when context is cancelled")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("waitForSync did not return after context cancellation")
	}
}

// TestWaitForSync_RPCErrorRetries tests that waitForSync continues retrying
// when GetBlockchainInfo fails, and succeeds once the daemon responds with synced status.
func TestWaitForSync_RPCErrorRetries(t *testing.T) {
	t.Parallel()

	// Create a node manager that fails initially, then succeeds.
	nm := &syncTestNodeMgr{
		failCount:   2, // fail first 2 calls
		syncedAfter: 3, // succeed on 3rd call
	}

	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- cp.waitForSync(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waitForSync should succeed after RPC retries, got: %v", err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("waitForSync did not complete within timeout")
	}
}

// syncTestNodeMgr is a mock that fails GetBlockchainInfo for `failCount` calls,
// then returns synced status. All other methods panic if called.
type syncTestNodeMgr struct {
	mu          sync.Mutex
	callCount   int
	failCount   int
	syncedAfter int
}

func (m *syncTestNodeMgr) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	m.mu.Lock()
	m.callCount++
	n := m.callCount
	m.mu.Unlock()

	if n <= m.failCount {
		return nil, fmt.Errorf("connection refused (call %d)", n)
	}

	if n >= m.syncedAfter {
		return &daemon.BlockchainInfo{
			Blocks:               500000,
			Headers:              500000,
			VerificationProgress: 0.999999,
			InitialBlockDownload: false,
		}, nil
	}

	// Still syncing
	return &daemon.BlockchainInfo{
		Blocks:               100000,
		Headers:              500000,
		VerificationProgress: 0.2,
		InitialBlockDownload: true,
	}, nil
}

// Stub implementations — panic if called since waitForSync only uses GetBlockchainInfo.
func (m *syncTestNodeMgr) SetBlockHandler(handler func(blockHash []byte))                 { panic("unused") }
func (m *syncTestNodeMgr) SetZMQStatusHandler(handler func(status daemon.ZMQStatus))      { panic("unused") }
func (m *syncTestNodeMgr) SubmitBlockWithVerification(ctx context.Context, blockHex string, blockHash string, height uint64, timeouts *daemon.SubmitTimeouts) *daemon.BlockSubmitResult { panic("unused") }
func (m *syncTestNodeMgr) GetBlockHash(ctx context.Context, height uint64) (string, error) { panic("unused") }
func (m *syncTestNodeMgr) Start(ctx context.Context) error                                { panic("unused") }
func (m *syncTestNodeMgr) Stop() error                                                    { panic("unused") }
func (m *syncTestNodeMgr) HasZMQ() bool                                                   { panic("unused") }
func (m *syncTestNodeMgr) IsZMQFailed() bool                                              { panic("unused") }
func (m *syncTestNodeMgr) IsZMQStable() bool                                              { panic("unused") }
func (m *syncTestNodeMgr) GetDifficulty(ctx context.Context) (float64, error)              { panic("unused") }
func (m *syncTestNodeMgr) Stats() nodemanager.ManagerStats                                 { panic("unused") }
func (m *syncTestNodeMgr) SubmitBlock(ctx context.Context, blockHex string) error          { panic("unused") }
func (m *syncTestNodeMgr) GetBlock(ctx context.Context, blockHash string) (map[string]interface{}, error) { panic("unused") }
func (m *syncTestNodeMgr) GetPrimary() *nodemanager.ManagedNode                           { panic("unused") }

// ═══════════════════════════════════════════════════════════════════════════════
// cleanupStaleShares() tests — MEDIUM
// ═══════════════════════════════════════════════════════════════════════════════

// TestCleanupStaleShares_Success tests that cleanupStaleShares calls the DB
// with the correct retention window and returns nil on success.
func TestCleanupStaleShares_Success(t *testing.T) {
	t.Parallel()

	db := newCriticalMockDB()
	db.cleanupDeletedCount = 42

	cp := newCriticalTestCoinPool(newCriticalMockNodeMgr(), db)

	err := cp.cleanupStaleShares(context.Background())
	if err != nil {
		t.Errorf("cleanupStaleShares returned unexpected error: %v", err)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.cleanupCalled {
		t.Error("CleanupStaleShares should have been called on DB")
	}
	if db.cleanupRetention != 15 {
		t.Errorf("retention minutes = %d, want 15", db.cleanupRetention)
	}
}

// TestCleanupStaleShares_DBError tests that cleanupStaleShares returns the
// error when the database call fails.
func TestCleanupStaleShares_DBError(t *testing.T) {
	t.Parallel()

	db := newCriticalMockDB()
	db.cleanupErr = errors.New("connection refused")

	cp := newCriticalTestCoinPool(newCriticalMockNodeMgr(), db)

	err := cp.cleanupStaleShares(context.Background())
	if err == nil {
		t.Error("cleanupStaleShares should return error when DB fails")
	}
	if !errors.Is(err, db.cleanupErr) {
		t.Errorf("expected error %v, got: %v", db.cleanupErr, err)
	}
}

// TestCleanupStaleShares_NilDB tests that cleanupStaleShares returns nil
// when db is nil (e.g., standalone mode without a database).
func TestCleanupStaleShares_NilDB(t *testing.T) {
	t.Parallel()

	cp := newCriticalTestCoinPool(newCriticalMockNodeMgr(), nil)
	// Explicitly set db to nil (newCriticalTestCoinPool might set it)
	cp.db = nil

	err := cp.cleanupStaleShares(context.Background())
	if err != nil {
		t.Errorf("cleanupStaleShares should return nil when db is nil, got: %v", err)
	}
}

// TestCleanupStaleShares_ZeroDeleted tests the no-op path where cleanup
// succeeds but no stale shares were found.
func TestCleanupStaleShares_ZeroDeleted(t *testing.T) {
	t.Parallel()

	db := newCriticalMockDB()
	db.cleanupDeletedCount = 0

	cp := newCriticalTestCoinPool(newCriticalMockNodeMgr(), db)

	err := cp.cleanupStaleShares(context.Background())
	if err != nil {
		t.Errorf("cleanupStaleShares returned unexpected error: %v", err)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.cleanupCalled {
		t.Error("CleanupStaleShares should have been called even when 0 deleted")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Background loop lifecycle tests — HIGH
// Tests that each loop exits cleanly when context is cancelled (no goroutine leak).
// ═══════════════════════════════════════════════════════════════════════════════

// TestCelebrationLoop_ExitsOnCancel verifies celebrationLoop exits when context is cancelled.
func TestCelebrationLoop_ExitsOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithCancel(context.Background())

	cp.wg.Add(1)
	go cp.celebrationLoop(ctx)

	// Cancel and verify the goroutine exits
	cancel()
	waitForWg(t, &cp.wg, 5*time.Second, "celebrationLoop")
}

// TestReconciliationLoop_ExitsOnCancel verifies reconciliationLoop exits when context is cancelled.
func TestReconciliationLoop_ExitsOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	db := newCriticalMockDB()
	cp := newCriticalTestCoinPool(nm, db)

	ctx, cancel := context.WithCancel(context.Background())

	cp.wg.Add(1)
	go cp.reconciliationLoop(ctx)

	cancel()
	waitForWg(t, &cp.wg, 5*time.Second, "reconciliationLoop")
}

// TestZmqPromotionLoop_ExitsOnCancel verifies zmqPromotionLoop exits when context is cancelled.
func TestZmqPromotionLoop_ExitsOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	// zmqPromotionLoop calls IsZMQStable() and IsZMQFailed() on startup (checkZMQ)
	// so we need safe stubs. criticalMockNodeMgr provides these.
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithCancel(context.Background())

	cp.wg.Add(1)
	go cp.zmqPromotionLoop(ctx)

	cancel()
	waitForWg(t, &cp.wg, 5*time.Second, "zmqPromotionLoop")
}

// TestSessionCleanupLoop_ExitsOnCancel verifies sessionCleanupLoop exits when context is cancelled.
func TestSessionCleanupLoop_ExitsOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithCancel(context.Background())

	cp.wg.Add(1)
	go cp.sessionCleanupLoop(ctx)

	cancel()
	waitForWg(t, &cp.wg, 5*time.Second, "sessionCleanupLoop")
}

// TestPollingLoop_ExitsOnStop verifies pollingLoop exits when its stop channel is closed.
// pollingLoop does NOT take a context — it uses pollingStopCh.
func TestPollingLoop_ExitsOnStop(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	// Set up polling state as startPollingFallback() would, but without
	// the wg.Add(1)/go since we manage that ourselves.
	cp.pollingMu.Lock()
	cp.usePolling = true
	cp.pollingStopCh = make(chan struct{})
	cp.pollingTicker = time.NewTicker(1 * time.Second)
	cp.pollingMu.Unlock()

	cp.wg.Add(1)
	go cp.pollingLoop()

	// Close the stop channel to signal exit
	cp.pollingMu.Lock()
	close(cp.pollingStopCh)
	cp.pollingTicker.Stop()
	cp.pollingMu.Unlock()

	waitForWg(t, &cp.wg, 5*time.Second, "pollingLoop")
}

// TestPollingLoop_NilTickerExitsImmediately verifies pollingLoop exits immediately
// if pollingTicker is nil (defensive guard).
func TestPollingLoop_NilTickerExitsImmediately(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	// Leave pollingTicker and pollingStopCh as nil
	cp.wg.Add(1)
	go cp.pollingLoop()

	waitForWg(t, &cp.wg, 5*time.Second, "pollingLoop (nil ticker)")
}

// TestDifficultyLoop_ExitsOnCancel verifies difficultyLoop exits when context is cancelled.
// NOTE: difficultyLoop calls GetDifficulty + shareValidator.SetNetworkDifficulty on startup.
// We make GetDifficulty return an error so it skips the shareValidator call (which would
// panic with nil shareValidator). The loop then enters select and exits on ctx.Done().
func TestDifficultyLoop_ExitsOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	nm.difficultyErr = errors.New("not ready") // Avoids shareValidator access
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithCancel(context.Background())

	cp.wg.Add(1)
	go cp.difficultyLoop(ctx)

	// Brief sleep to let the startup GetDifficulty call complete and enter the loop
	time.Sleep(50 * time.Millisecond)
	cancel()
	waitForWg(t, &cp.wg, 5*time.Second, "difficultyLoop")
}

// TestStatsLoop_ExitsOnCancel verifies statsLoop exits when context is cancelled.
// statsLoop uses a 60s ticker so cancelling immediately means updateStats is never called.
func TestStatsLoop_ExitsOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	cp := newCriticalTestCoinPool(nm, newCriticalMockDB())

	ctx, cancel := context.WithCancel(context.Background())

	cp.wg.Add(1)
	go cp.statsLoop(ctx)

	cancel()
	waitForWg(t, &cp.wg, 5*time.Second, "statsLoop")
}

// TestMultipleLoops_AllExitOnCancel is an integration-style test that starts
// several loops concurrently and verifies they ALL exit when the shared context
// is cancelled. This catches goroutine leaks from any individual loop.
func TestMultipleLoops_AllExitOnCancel(t *testing.T) {
	t.Parallel()

	nm := newCriticalMockNodeMgr()
	nm.difficultyErr = errors.New("not ready") // Prevents shareValidator nil panic in difficultyLoop
	db := newCriticalMockDB()
	cp := newCriticalTestCoinPool(nm, db)

	ctx, cancel := context.WithCancel(context.Background())

	// Start all context-based loops
	loopCount := 6 // celebrationLoop, reconciliationLoop, zmqPromotionLoop, sessionCleanupLoop, difficultyLoop, statsLoop
	cp.wg.Add(loopCount)
	go cp.celebrationLoop(ctx)
	go cp.reconciliationLoop(ctx)
	go cp.zmqPromotionLoop(ctx)
	go cp.sessionCleanupLoop(ctx)
	go cp.difficultyLoop(ctx)
	go cp.statsLoop(ctx)

	// Also start pollingLoop with its own stop mechanism
	cp.pollingMu.Lock()
	cp.usePolling = true
	cp.pollingStopCh = make(chan struct{})
	cp.pollingTicker = time.NewTicker(1 * time.Second)
	cp.pollingMu.Unlock()
	cp.wg.Add(1)
	go cp.pollingLoop()

	// Brief delay to let goroutines spin up
	time.Sleep(50 * time.Millisecond)

	// Cancel context (for context-based loops)
	cancel()
	// Close polling stop channel
	cp.pollingMu.Lock()
	close(cp.pollingStopCh)
	cp.pollingTicker.Stop()
	cp.pollingMu.Unlock()

	waitForWg(t, &cp.wg, 10*time.Second, "all loops")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════════════

// waitForWg waits for a WaitGroup to complete within the given timeout.
// Fails the test if the goroutines don't exit in time.
func waitForWg(t *testing.T, wg *sync.WaitGroup, timeout time.Duration, loopName string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Success: goroutine(s) exited cleanly
	case <-time.After(timeout):
		t.Fatalf("%s did not exit within %v — goroutine leak", loopName, timeout)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// GetHashrate() per-pool isolation test — Smart Multi-Port bug fix
// ═══════════════════════════════════════════════════════════════════════════════
// Proves that GetHashrate() queries the correct per-coin share table when
// multiple CoinPools share a single database connection (the coordinator pattern).
//
// BUG (pre-fix): GetHashrate() called db.GetPoolHashrate() which uses the DB's
// internal poolID (set to firstPoolID during coordinator init). All CoinPools
// returned the SAME hashrate regardless of coin — causing the dashboard to show
// N× the actual hashrate when N coins were enabled in smart multi-port mode.
//
// FIX: GetHashrate() now calls db.GetPoolHashrateForPool(ctx, cp.poolID, ...)
// which queries shares_<THIS_COIN's_poolID>, returning correct per-coin hashrate.

// multiCoinMockDB simulates a shared PostgresDB where each coin has its own
// share table with different hashrates. This is the real-world scenario: shares
// are partitioned into shares_btc_sha256_1, shares_bch_sha256_1, etc.
type multiCoinMockDB struct {
	criticalMockDB

	// Per-pool hashrates: poolID → hashrate (simulates different share tables)
	perPoolHashrates map[string]float64

	// Track which method was called and with what poolID
	mu            sync.Mutex
	forPoolCalled bool   // GetPoolHashrateForPool was called
	lastForPoolID string // last poolID passed to GetPoolHashrateForPool
}

func (m *multiCoinMockDB) GetPoolHashrateForPool(ctx context.Context, poolID string, windowMinutes int, algorithm string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forPoolCalled = true
	m.lastForPoolID = poolID
	if hr, ok := m.perPoolHashrates[poolID]; ok {
		return hr, nil
	}
	return 0, nil
}

func TestGetHashrate_PerPoolIsolation_SmartMultiPort(t *testing.T) {
	t.Parallel()

	// Simulate 3 coins sharing a single DB connection (coordinator pattern).
	// The DB's internal poolID would be "btc_sha256_1" (first coin registered).
	// Each coin's share table has a DIFFERENT hashrate because the smart port
	// scheduler distributes mining time across coins based on difficulty.
	sharedDB := &multiCoinMockDB{
		perPoolHashrates: map[string]float64{
			"btc_sha256_1": 50e12,  // BTC: 50 TH/s (mined 50% of the time)
			"bch_sha256_1": 30e12,  // BCH: 30 TH/s (mined 30% of the time)
			"qbx_sha256_1": 20e12,  // QBX: 20 TH/s (mined 20% of the time)
		},
	}
	// The old GetPoolHashrate() always returned this (first coin's hashrate)
	sharedDB.globalHashrate = 50e12

	// Create 3 CoinPools sharing the SAME DB (exactly like the coordinator does)
	coins := []struct {
		symbol string
		poolID string
	}{
		{"BTC", "btc_sha256_1"},
		{"BCH", "bch_sha256_1"},
		{"QBX", "qbx_sha256_1"},
	}

	for _, tc := range coins {
		coinImpl, err := coin.Create(tc.symbol)
		if err != nil {
			t.Fatalf("coin.Create(%s): %v", tc.symbol, err)
		}

		cp := &CoinPool{
			db:         sharedDB,
			poolID:     tc.poolID,
			coinSymbol: tc.symbol,
			coin:       coinImpl,
			logger:     zap.NewNop().Sugar(),
		}

		got := cp.GetHashrate()
		expected := sharedDB.perPoolHashrates[tc.poolID]

		if got != expected {
			t.Errorf("%s (poolID=%s): GetHashrate() = %.0f H/s, want %.0f H/s (BUG: returning shared firstPoolID hashrate instead of per-coin)",
				tc.symbol, tc.poolID, got, expected)
		}
	}

	// Verify GetPoolHashrateForPool was called (the per-coin method).
	// Note: GetPoolHashrate (shared firstPoolID) is no longer in the coinPoolDB
	// interface, so the compiler enforces it cannot be called.
	sharedDB.mu.Lock()
	defer sharedDB.mu.Unlock()

	if !sharedDB.forPoolCalled {
		t.Error("FIX NOT APPLIED: GetHashrate() did not call GetPoolHashrateForPool()")
	}
}

// TestGetHashrate_OldBehavior_WouldTripleCount demonstrates what the bug looked like:
// if GetPoolHashrate() were still used, all 3 coins would return 50 TH/s (the first
// coin's hashrate), and the dashboard would sum them to 150 TH/s — 3× the actual.
func TestGetHashrate_OldBehavior_WouldTripleCount(t *testing.T) {
	t.Parallel()

	// With the old code, all coins would query shares_btc_sha256_1 and get 50 TH/s.
	// Dashboard sums: 50 + 50 + 50 = 150 TH/s (actual pool hashrate is ~100 TH/s).
	sharedDB := &multiCoinMockDB{
		perPoolHashrates: map[string]float64{
			"btc_sha256_1": 50e12,
			"bch_sha256_1": 30e12,
			"qbx_sha256_1": 20e12,
		},
	}
	sharedDB.globalHashrate = 50e12 // What GetPoolHashrate() would return

	// With the fix, summing per-coin gives the correct total
	var totalHashrate float64
	coins := []struct {
		symbol string
		poolID string
	}{
		{"BTC", "btc_sha256_1"},
		{"BCH", "bch_sha256_1"},
		{"QBX", "qbx_sha256_1"},
	}

	for _, tc := range coins {
		coinImpl, err := coin.Create(tc.symbol)
		if err != nil {
			t.Fatalf("coin.Create(%s): %v", tc.symbol, err)
		}

		cp := &CoinPool{
			db:         sharedDB,
			poolID:     tc.poolID,
			coinSymbol: tc.symbol,
			coin:       coinImpl,
			logger:     zap.NewNop().Sugar(),
		}

		totalHashrate += cp.GetHashrate()
	}

	// Correct total: 50 + 30 + 20 = 100 TH/s
	expectedTotal := 100e12
	if totalHashrate != expectedTotal {
		t.Errorf("Total hashrate = %.0f H/s, want %.0f H/s", totalHashrate, expectedTotal)
	}

	// Old buggy total would have been: 50 + 50 + 50 = 150 TH/s
	buggyTotal := sharedDB.globalHashrate * 3
	if totalHashrate == buggyTotal {
		t.Errorf("Total hashrate matches buggy 3× value (%.0f H/s) — fix not working", buggyTotal)
	}

	t.Logf("PASS: Total = %.0f TH/s (correct), old bug would show %.0f TH/s (3×)",
		totalHashrate/1e12, buggyTotal/1e12)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Compile-time interface satisfaction checks
// ═══════════════════════════════════════════════════════════════════════════════

var (
	_ coinPoolNodeManager = (*criticalMockNodeMgr)(nil)
	_ coinPoolDB          = (*criticalMockDB)(nil)
	_ coinPoolNodeManager = (*syncTestNodeMgr)(nil)
	_ coinPoolDB          = (*multiCoinMockDB)(nil)
)
