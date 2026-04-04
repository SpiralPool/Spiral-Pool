// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool — Production tests for CoinPool.handleBlock().
//
// handleBlock() is the most money-critical code path in the pool.
// A bug here means silent block loss: the block is on the blockchain
// but the pool marks it orphaned, and the miner never gets paid.
//
// These tests exercise the REAL handleBlock() method with mock dependencies,
// covering every major branch: stale races, permanent rejections, transient
// retries, HA backup gating, post-timeout verification, and DB failures.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/ha"
	"github.com/spiralpool/stratum/internal/nodemanager"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Mock implementations
// ═══════════════════════════════════════════════════════════════════════════════

// mockJobMgr implements coinPoolJobManager for handleBlock tests.
type mockJobMgr struct {
	jobs          map[string]*protocol.Job
	lastBlockHash string
}

func newMockJobMgr() *mockJobMgr {
	return &mockJobMgr{
		jobs: make(map[string]*protocol.Job),
	}
}

func (m *mockJobMgr) GetJob(id string) (*protocol.Job, bool) {
	j, ok := m.jobs[id]
	return j, ok
}

func (m *mockJobMgr) GetLastBlockHash() string {
	return m.lastBlockHash
}

func (m *mockJobMgr) HeightContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func (m *mockJobMgr) SetJobCallback(callback func(*protocol.Job)) {
	panic("SetJobCallback not called in handleBlock tests")
}

func (m *mockJobMgr) OnBlockNotificationWithHash(ctx context.Context, newTipHash string) {
	panic("OnBlockNotificationWithHash not called in handleBlock tests")
}

func (m *mockJobMgr) Start(ctx context.Context) error {
	panic("Start not called in handleBlock tests")
}

func (m *mockJobMgr) GetCurrentJob() *protocol.Job {
	panic("GetCurrentJob not called in handleBlock tests")
}

func (m *mockJobMgr) SetSoloMinerAddress(address string) error {
	// No-op for tests - SOLO miner address not relevant to handleBlock tests
	return nil
}

func (m *mockJobMgr) RefreshJob(_ context.Context, _ bool) error {
	return nil
}

// mockNodeMgr implements coinPoolNodeManager for handleBlock tests.
type mockNodeMgr struct {
	mu sync.Mutex

	// SubmitBlockWithVerification behavior
	submitResults []*daemon.BlockSubmitResult // returns results in order; last one repeats
	submitCalls   int

	// GetBlockHash behavior
	blockHashByHeight map[uint64]string
	blockHashErr      error

	// GetBlock behavior
	blockInfoByHash map[string]map[string]interface{}
	getBlockErr     error

	// GetBlockchainInfo behavior
	blockchainInfo    *daemon.BlockchainInfo
	blockchainInfoErr error

	// SubmitBlock behavior (simple resubmission, not SubmitBlockWithVerification)
	submitBlockErr   error
	submitBlockCalls int
}

func newMockNodeMgr() *mockNodeMgr {
	return &mockNodeMgr{
		blockHashByHeight: make(map[uint64]string),
		blockInfoByHash:   make(map[string]map[string]interface{}),
	}
}

func (m *mockNodeMgr) SubmitBlockWithVerification(ctx context.Context, blockHex string, blockHash string, height uint64, timeouts *daemon.SubmitTimeouts) *daemon.BlockSubmitResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.submitCalls
	m.submitCalls++
	if idx >= len(m.submitResults) {
		idx = len(m.submitResults) - 1
	}
	if idx < 0 {
		return &daemon.BlockSubmitResult{}
	}
	return m.submitResults[idx]
}

func (m *mockNodeMgr) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	if m.blockHashErr != nil {
		return "", m.blockHashErr
	}
	h, ok := m.blockHashByHeight[height]
	if !ok {
		return "", fmt.Errorf("block not found at height %d", height)
	}
	return h, nil
}

func (m *mockNodeMgr) GetBlock(ctx context.Context, blockHash string) (map[string]interface{}, error) {
	if m.getBlockErr != nil {
		return nil, m.getBlockErr
	}
	info, ok := m.blockInfoByHash[blockHash]
	if !ok {
		return nil, fmt.Errorf("block %s not found", blockHash)
	}
	return info, nil
}

func (m *mockNodeMgr) SubmitCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.submitCalls
}

func (m *mockNodeMgr) SubmitBlockCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.submitBlockCalls
}

func (m *mockNodeMgr) SetBlockHandler(handler func(blockHash []byte)) {
	panic("SetBlockHandler not called in handleBlock tests")
}
func (m *mockNodeMgr) SetZMQStatusHandler(handler func(status daemon.ZMQStatus)) {
	panic("SetZMQStatusHandler not called in handleBlock tests")
}
func (m *mockNodeMgr) Start(ctx context.Context) error {
	panic("Start not called in handleBlock tests")
}
func (m *mockNodeMgr) Stop() error {
	panic("Stop not called in handleBlock tests")
}
func (m *mockNodeMgr) HasZMQ() bool {
	panic("HasZMQ not called in handleBlock tests")
}
func (m *mockNodeMgr) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	if m.blockchainInfoErr != nil {
		return nil, m.blockchainInfoErr
	}
	return m.blockchainInfo, nil
}
func (m *mockNodeMgr) IsZMQFailed() bool {
	panic("IsZMQFailed not called in handleBlock tests")
}
func (m *mockNodeMgr) IsZMQStable() bool {
	panic("IsZMQStable not called in handleBlock tests")
}
func (m *mockNodeMgr) GetDifficulty(ctx context.Context) (float64, error) {
	panic("GetDifficulty not called in handleBlock tests")
}
func (m *mockNodeMgr) Stats() nodemanager.ManagerStats {
	panic("Stats not called in handleBlock tests")
}
func (m *mockNodeMgr) SubmitBlock(ctx context.Context, blockHex string) error {
	m.mu.Lock()
	m.submitBlockCalls++
	m.mu.Unlock()
	return m.submitBlockErr
}
func (m *mockNodeMgr) GetPrimary() *nodemanager.ManagedNode {
	panic("GetPrimary not called in handleBlock tests")
}

// mockDB implements coinPoolDB for handleBlock tests.
type mockDB struct {
	mu sync.Mutex

	// InsertBlockForPool tracking
	insertedBlocks []*database.Block
	insertErr      error

	// UpdateBlockStatusForPool tracking
	statusUpdates []statusUpdate
	updateErr     error

	// GetBlocksByStatus behavior
	blocksByStatus       map[string][]*database.Block
	getBlocksByStatusErr error
}

type statusUpdate struct {
	height uint64
	hash   string
	status string
}

func newMockDB() *mockDB {
	return &mockDB{}
}

func (m *mockDB) InsertBlockForPool(ctx context.Context, poolID string, block *database.Block) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertedBlocks = append(m.insertedBlocks, block)
	return m.insertErr
}

func (m *mockDB) UpdateBlockStatusForPool(ctx context.Context, poolID string, height uint64, hash string, status string, confirmationProgress float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusUpdates = append(m.statusUpdates, statusUpdate{height: height, hash: hash, status: status})
	return m.updateErr
}

func (m *mockDB) lastStatus() (uint64, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.statusUpdates) == 0 {
		return 0, "", ""
	}
	last := m.statusUpdates[len(m.statusUpdates)-1]
	return last.height, last.hash, last.status
}

func (m *mockDB) insertCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.insertedBlocks)
}

func (m *mockDB) statusUpdateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.statusUpdates)
}

func (m *mockDB) GetBlocksByStatusForPool(ctx context.Context, poolID string, status string) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getBlocksByStatusErr != nil {
		return nil, m.getBlocksByStatusErr
	}
	if m.blocksByStatus != nil {
		return m.blocksByStatus[status], nil
	}
	return nil, nil
}
func (m *mockDB) GetPoolHashrateForPool(ctx context.Context, poolID string, windowMinutes int, algorithm string) (float64, error) {
	panic("GetPoolHashrateForPool not called in handleBlock tests")
}
func (m *mockDB) UpdatePoolStatsForPool(ctx context.Context, poolID string, stats *database.PoolStats) error {
	panic("UpdatePoolStatsForPool not called in handleBlock tests")
}
func (m *mockDB) CleanupStaleSharesForPool(ctx context.Context, poolID string, retentionMinutes int) (int64, error) {
	panic("CleanupStaleSharesForPool not called in handleBlock tests")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Test helper
// ═══════════════════════════════════════════════════════════════════════════════

// newTestCoinPool constructs a minimal CoinPool wired to mocks for handleBlock testing.
func newTestCoinPool(jm *mockJobMgr, nm *mockNodeMgr, db *mockDB) *CoinPool {
	ctx, cancel := context.WithCancel(context.Background())
	cp := &CoinPool{
		jobManager:  jm,
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
	// Default to RoleMaster (standalone mode) — matches production NewCoinPool().
	// Tests that need HA role gating should explicitly set haRole to RoleBackup.
	cp.haRole.Store(int32(ha.RoleMaster))
	return cp
}

// makeActiveJobWithPrev creates a Job in Active state with the given prevBlockHash.
func makeActiveJobWithPrev(id, rawPrevBlockHash string) *protocol.Job {
	j := &protocol.Job{
		ID:               id,
		RawPrevBlockHash: rawPrevBlockHash,
		CoinbaseValue:    1000000000, // 10 coins
	}
	j.SetState(protocol.JobStateActive, "test")
	return j
}

// makeShare creates a minimal Share for handleBlock testing.
func makeShare(jobID string, height uint64, blockHash string) *protocol.Share {
	return &protocol.Share{
		JobID:        jobID,
		BlockHeight:  height,
		MinerAddress: "TTestMiner123",
		WorkerName:   "rig1",
		NetworkDiff:  1000.0,
		Difficulty:   1.0,
	}
}

// makeResult creates a minimal ShareResult for handleBlock testing.
func makeResult(blockHash, blockHex string) *protocol.ShareResult {
	return &protocol.ShareResult{
		IsBlock:       true,
		BlockHash:     blockHash,
		BlockHex:      blockHex,
		CoinbaseValue: 1000000000,
		PrevBlockHash: "0000prev",
		JobAge:        100 * time.Millisecond,
	}
}

const (
	testBlockHash = "0000000000000000000000000000000000000000000000000000000000001234"
	testBlockHex  = "01000000deadbeefcafebabe"
	testPrevHash  = "0000000000000000000000000000000000000000000000000000000000005678"
	testJobID     = "job_1"
	testHeight    = 100000
)

// ═══════════════════════════════════════════════════════════════════════════════
// Test cases
// ═══════════════════════════════════════════════════════════════════════════════

// Test 1: Happy path — submission succeeds on first attempt.
func TestHandleBlock_HappyPath_SubmissionSucceeds(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: true, Verified: true, ChainHash: testBlockHash},
	}

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	height, hash, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected status 'pending', got %q (height=%d, hash=%s)", status, height, hash)
	}
	if nm.SubmitCallCount() != 1 {
		t.Errorf("expected 1 submit call, got %d", nm.SubmitCallCount())
	}
	if db.insertCount() < 1 {
		t.Error("expected at least 1 InsertBlockForPool call for pre-submit record")
	}
}

// Test 2: Stale race — job invalidated before submission.
func TestHandleBlock_StaleRace_JobInvalidated(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	job := makeActiveJobWithPrev(testJobID, testPrevHash)
	job.SetState(protocol.JobStateInvalidated, "chain advanced")
	jm.jobs[testJobID] = job

	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned', got %q", status)
	}
	if nm.SubmitCallCount() != 0 {
		t.Errorf("expected 0 submit calls for invalidated job, got %d", nm.SubmitCallCount())
	}
}

// Test 3: Duplicate candidate — job already solved.
func TestHandleBlock_DuplicateCandidate_JobSolved(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	job := makeActiveJobWithPrev(testJobID, testPrevHash)
	job.SetState(protocol.JobStateSolved, "already submitted")
	jm.jobs[testJobID] = job

	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned', got %q", status)
	}
	if nm.SubmitCallCount() != 0 {
		t.Errorf("expected 0 submit calls for solved job, got %d", nm.SubmitCallCount())
	}
}

// Test 4: Chain tip moved — prevBlockHash mismatch.
func TestHandleBlock_ChainTipMoved_PrevHashMismatch(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = "000000000000000000000000000000000000000000000000000000000000aaaa" // different from job
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash) // job has testPrevHash

	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned', got %q", status)
	}
	if nm.SubmitCallCount() != 0 {
		t.Errorf("expected 0 submit calls for stale block, got %d", nm.SubmitCallCount())
	}
}

// Test 5: Empty BlockHex — no rebuild possible.
func TestHandleBlock_EmptyBlockHex_NoRebuild(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	// Empty BlockHex — rebuild will also fail since job has no TransactionData
	result := makeResult(testBlockHash, "")
	result.BlockBuildError = "failed to decode coinbase1"

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), result)

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned', got %q", status)
	}
	if nm.SubmitCallCount() != 0 {
		t.Errorf("expected 0 submit calls for empty BlockHex, got %d", nm.SubmitCallCount())
	}
}

// Test 6: Permanent rejection — bad-txnmrklroot.
func TestHandleBlock_PermanentRejection_BadTxn(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: false, Verified: false, SubmitErr: errors.New("bad-txnmrklroot")},
	}

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned', got %q", status)
	}
}

// Test 7: Permanent rejection "duplicate" but block IS in chain → pending.
func TestHandleBlock_PermanentRejection_DuplicateVerifiedInChain(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: false, Verified: false, SubmitErr: errors.New("duplicate")},
	}
	// verifyBlockAcceptance will call GetBlockHash — return matching hash
	nm.blockHashByHeight[testHeight] = testBlockHash

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected status 'pending' (duplicate verified in chain), got %q", status)
	}
}

// Test 8: Permanent rejection "duplicate" but block NOT in chain → orphaned.
func TestHandleBlock_PermanentRejection_DuplicateNotInChain(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: false, Verified: false, SubmitErr: errors.New("duplicate")},
	}
	// verifyBlockAcceptance: GetBlockHash returns a DIFFERENT hash
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"
	// GetBlock also won't find our hash
	nm.getBlockErr = fmt.Errorf("block not found")

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned' (duplicate not in chain), got %q", status)
	}
}

// Test 9: Transient failure — first attempt fails, retry succeeds.
func TestHandleBlock_TransientFailure_RetrySucceeds(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		// First attempt: transient failure (not permanent rejection)
		{Submitted: false, Verified: false, SubmitErr: errors.New("connection timeout")},
		// Second attempt (retry): success
		{Submitted: true, Verified: true, ChainHash: testBlockHash},
	}

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected status 'pending' after retry success, got %q", status)
	}
	if nm.SubmitCallCount() < 2 {
		t.Errorf("expected at least 2 submit calls (initial + retry), got %d", nm.SubmitCallCount())
	}
}

// Test 10: All retries exhausted, but post-timeout verification finds block in chain.
func TestHandleBlock_RetryExhausted_PostTimeoutVerifySucceeds(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	// All submit attempts fail with transient error
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: false, Verified: false, SubmitErr: errors.New("timeout")},
		{Submitted: false, Verified: false, SubmitErr: errors.New("timeout")},
		{Submitted: false, Verified: false, SubmitErr: errors.New("timeout")},
	}
	// But verifyBlockAcceptance finds the block in chain
	nm.blockHashByHeight[testHeight] = testBlockHash

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected status 'pending' (post-timeout verify succeeded), got %q", status)
	}
}

// Test 11: All retries exhausted, post-timeout verification also fails.
func TestHandleBlock_RetryExhausted_PostTimeoutVerifyFails(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	// All submit attempts fail
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: false, Verified: false, SubmitErr: errors.New("timeout")},
		{Submitted: false, Verified: false, SubmitErr: errors.New("timeout")},
		{Submitted: false, Verified: false, SubmitErr: errors.New("timeout")},
	}
	// verifyBlockAcceptance: different hash at this height
	nm.blockHashByHeight[testHeight] = "000000000000000000000000000000000000000000000000000000000000ffff"
	nm.getBlockErr = fmt.Errorf("block not found")

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "orphaned" {
		t.Errorf("expected status 'orphaned' (post-timeout verify failed), got %q", status)
	}
}

// Test 12: HA backup node — skips daemon submission, records "submitted".
func TestHandleBlock_HABackupSkip(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	// Set HA role to Backup
	cp.haRole.Store(int32(ha.RoleBackup))

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected status 'pending' for HA backup (trust master submission), got %q", status)
	}
	if nm.SubmitCallCount() != 0 {
		t.Errorf("expected 0 submit calls for backup node, got %d", nm.SubmitCallCount())
	}
}

// Test 13: Verified=true but Submitted=false — false rejection, should be "pending".
func TestHandleBlock_VerifiedNotSubmitted_FalseRejection(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: false, Verified: true, ChainHash: testBlockHash, SubmitErr: errors.New("connection reset")},
	}

	db := newMockDB()
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	_, _, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected status 'pending' (verified despite submit error), got %q", status)
	}
}

// Test 14: DB InsertBlockForPool fails — submission still proceeds.
func TestHandleBlock_DBInsertFailure_SubmissionContinues(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: true, Verified: true, ChainHash: testBlockHash},
	}

	db := newMockDB()
	db.insertErr = errors.New("connection refused")
	cp := newTestCoinPool(jm, nm, db)

	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	// Submission should still have been called despite DB insert failure
	if nm.SubmitCallCount() != 1 {
		t.Errorf("expected 1 submit call despite DB insert failure, got %d", nm.SubmitCallCount())
	}

	// Final status update should still be recorded
	_, _, status := db.lastStatus()
	if status != "pending" {
		t.Errorf("expected final status 'pending', got %q", status)
	}
}

// Test 15: DB UpdateBlockStatusForPool fails — handleBlock completes without panic.
func TestHandleBlock_DBUpdateFailure_Logged(t *testing.T) {
	t.Parallel()

	jm := newMockJobMgr()
	jm.lastBlockHash = testPrevHash
	jm.jobs[testJobID] = makeActiveJobWithPrev(testJobID, testPrevHash)

	nm := newMockNodeMgr()
	nm.submitResults = []*daemon.BlockSubmitResult{
		{Submitted: true, Verified: true, ChainHash: testBlockHash},
	}

	db := newMockDB()
	db.updateErr = errors.New("connection refused")
	cp := newTestCoinPool(jm, nm, db)

	// Should not panic
	cp.handleBlock(makeShare(testJobID, testHeight, testBlockHash), makeResult(testBlockHash, testBlockHex))

	// Verify the update was attempted (even though it failed)
	if db.statusUpdateCount() < 1 {
		t.Error("expected at least 1 status update attempt")
	}
}

// Compile-time interface satisfaction checks
var (
	_ coinPoolJobManager  = (*mockJobMgr)(nil)
	_ coinPoolNodeManager = (*mockNodeMgr)(nil)
	_ coinPoolDB          = (*mockDB)(nil)
)

// Compile-time check that atomic.Int32 field is usable with ha.Role.
// This validates the test's haRole.Store(int32(ha.RoleBackup)) pattern.
var _ = func() {
	var x atomic.Int32
	x.Store(int32(ha.RoleBackup))
	_ = ha.Role(x.Load())
}
