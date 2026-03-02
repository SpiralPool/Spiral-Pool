// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

// =============================================================================
// Mock Types
// =============================================================================

// mockBlockStore is an in-memory block store that tracks all update calls
// and mutates blocks so subsequent GetPendingBlocks/GetConfirmedBlocks reads
// reflect the latest state.
type mockBlockStore struct {
	mu              sync.Mutex
	pendingBlocks   []*database.Block
	confirmedBlocks []*database.Block

	// Call tracking
	updateStatusCalls    []updateStatusCall
	updateOrphanCalls    []updateOrphanCall
	updateStabilityCalls []updateStabilityCall
	getConfirmedCalled   bool
}

type updateStatusCall struct {
	Height   uint64
	Status   string
	Progress float64
}

type updateOrphanCall struct {
	Height        uint64
	MismatchCount int
}

type updateStabilityCall struct {
	Height         uint64
	StabilityCount int
	LastTip        string
}

func (m *mockBlockStore) GetPendingBlocks(_ context.Context) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return only blocks still in pending status.
	var result []*database.Block
	for _, b := range m.pendingBlocks {
		if b.Status == StatusPending {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockBlockStore) GetConfirmedBlocks(_ context.Context) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getConfirmedCalled = true
	var result []*database.Block
	for _, b := range m.confirmedBlocks {
		if b.Status == StatusConfirmed {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockBlockStore) UpdateBlockStatus(_ context.Context, height uint64, hash string, status string, confirmationProgress float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatusCalls = append(m.updateStatusCalls, updateStatusCall{height, status, confirmationProgress})
	// V1 FIX: Mutate only the block matching BOTH height AND hash.
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.Status = status
			b.ConfirmationProgress = confirmationProgress
		}
	}
	for _, b := range m.confirmedBlocks {
		if b.Height == height && b.Hash == hash {
			b.Status = status
			b.ConfirmationProgress = confirmationProgress
		}
	}
	return nil
}

func (m *mockBlockStore) UpdateBlockOrphanCount(_ context.Context, height uint64, hash string, mismatchCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateOrphanCalls = append(m.updateOrphanCalls, updateOrphanCall{height, mismatchCount})
	// V1 FIX: Only update the block matching BOTH height AND hash.
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.OrphanMismatchCount = mismatchCount
		}
	}
	return nil
}

func (m *mockBlockStore) UpdateBlockStabilityCount(_ context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStabilityCalls = append(m.updateStabilityCalls, updateStabilityCall{height, stabilityCount, lastTip})
	// V1 FIX: Only update the block matching BOTH height AND hash.
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.StabilityCheckCount = stabilityCount
			b.LastVerifiedTip = lastTip
		}
	}
	return nil
}

func (m *mockBlockStore) GetBlocksByStatus(_ context.Context, status string) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*database.Block
	for _, b := range m.pendingBlocks {
		if b.Status == status {
			result = append(result, b)
		}
	}
	for _, b := range m.confirmedBlocks {
		if b.Status == status {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockBlockStore) GetBlockStats(_ context.Context) (*database.BlockStats, error) {
	return &database.BlockStats{}, nil
}

func (m *mockBlockStore) UpdateBlockConfirmationState(_ context.Context, height uint64, hash string, status string, confirmationProgress float64, orphanMismatchCount int, stabilityCheckCount int, lastVerifiedTip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatusCalls = append(m.updateStatusCalls, updateStatusCall{height, status, confirmationProgress})
	// Also record stability updates so tests checking getStabilityUpdates() work
	// with the atomic UpdateBlockConfirmationState code path.
	if stabilityCheckCount > 0 {
		m.updateStabilityCalls = append(m.updateStabilityCalls, updateStabilityCall{height, stabilityCheckCount, lastVerifiedTip})
	}
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.Status = status
			b.ConfirmationProgress = confirmationProgress
			b.OrphanMismatchCount = orphanMismatchCount
			b.StabilityCheckCount = stabilityCheckCount
			b.LastVerifiedTip = lastVerifiedTip
		}
	}
	for _, b := range m.confirmedBlocks {
		if b.Height == height && b.Hash == hash {
			b.Status = status
			b.ConfirmationProgress = confirmationProgress
			b.OrphanMismatchCount = orphanMismatchCount
			b.StabilityCheckCount = stabilityCheckCount
			b.LastVerifiedTip = lastVerifiedTip
		}
	}
	return nil
}

// lastStatusFor returns the most recent UpdateBlockStatus call for a given height,
// or (false, updateStatusCall{}) if none found.
func (m *mockBlockStore) lastStatusFor(height uint64) (updateStatusCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.updateStatusCalls) - 1; i >= 0; i-- {
		if m.updateStatusCalls[i].Height == height {
			return m.updateStatusCalls[i], true
		}
	}
	return updateStatusCall{}, false
}

// newMockBlockStore creates a new mockBlockStore ready for use.
func newMockBlockStore() *mockBlockStore {
	return &mockBlockStore{}
}

// addPendingBlock adds a pending block (thread-safe).
func (m *mockBlockStore) addPendingBlock(block *database.Block) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingBlocks = append(m.pendingBlocks, block)
}

// addConfirmedBlock adds a confirmed block (thread-safe).
func (m *mockBlockStore) addConfirmedBlock(block *database.Block) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirmedBlocks = append(m.confirmedBlocks, block)
}

// getStatusUpdates returns a snapshot of all status update calls.
func (m *mockBlockStore) getStatusUpdates() []updateStatusCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]updateStatusCall, len(m.updateStatusCalls))
	copy(result, m.updateStatusCalls)
	return result
}

// getOrphanUpdates returns a snapshot of all orphan update calls.
func (m *mockBlockStore) getOrphanUpdates() []updateOrphanCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]updateOrphanCall, len(m.updateOrphanCalls))
	copy(result, m.updateOrphanCalls)
	return result
}

// getStabilityUpdates returns a snapshot of all stability update calls.
func (m *mockBlockStore) getStabilityUpdates() []updateStabilityCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]updateStabilityCall, len(m.updateStabilityCalls))
	copy(result, m.updateStabilityCalls)
	return result
}

// hasStatusUpdate checks if a specific height+status combination was recorded.
func (m *mockBlockStore) hasStatusUpdate(height uint64, status string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.updateStatusCalls {
		if u.Height == height && u.Status == status {
			return true
		}
	}
	return false
}

// mockDaemonRPC provides a configurable chain state with call counting.
// It supports tip-change-after-N-calls for TOCTOU testing, error injection,
// and per-call custom behavior for advanced failure scenario tests.
type mockDaemonRPC struct {
	mu sync.Mutex

	bestBlockHash string
	chainHeight   uint64

	// Per-height hash overrides. If a height is absent, returns a non-matching default.
	blockHashes map[uint64]string

	// TOCTOU simulation: after this many GetBlockchainInfo calls, switch tip.
	changeTipAfterCalls int    // 0 = never change
	newTip              string // tip to switch to
	newHeight           uint64 // height to switch to (0 = keep same)

	getBlockchainInfoCalls int
	getBlockHashCalls      int

	// Error injection — static errors returned on every call.
	errGetBlockchainInfo error
	errGetBlockHash      error

	// Per-call custom behavior (overrides static responses when set).
	blockchainInfoFunc func(ctx context.Context) (*daemon.BlockchainInfo, error)
	blockHashFunc      func(ctx context.Context, height uint64) (string, error)

	// Tip mutation for TOCTOU testing — called with call index, returns info.
	// When set, overrides all other GetBlockchainInfo behavior.
	tipMutator func(callIndex int64) *daemon.BlockchainInfo
}

func (m *mockDaemonRPC) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getBlockchainInfoCalls++

	// tipMutator overrides everything when set.
	if m.tipMutator != nil {
		info := m.tipMutator(int64(m.getBlockchainInfoCalls))
		if info != nil {
			return info, nil
		}
	}

	// Custom function overrides static responses.
	if m.blockchainInfoFunc != nil {
		return m.blockchainInfoFunc(ctx)
	}

	// Static error injection.
	if m.errGetBlockchainInfo != nil {
		return nil, m.errGetBlockchainInfo
	}

	tip := m.bestBlockHash
	height := m.chainHeight

	if m.changeTipAfterCalls > 0 && m.getBlockchainInfoCalls > m.changeTipAfterCalls {
		tip = m.newTip
		if m.newHeight > 0 {
			height = m.newHeight
		}
	}

	return &daemon.BlockchainInfo{
		BestBlockHash: tip,
		Blocks:        height,
	}, nil
}

func (m *mockDaemonRPC) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getBlockHashCalls++

	// Custom function overrides static responses.
	if m.blockHashFunc != nil {
		return m.blockHashFunc(ctx, height)
	}

	// Static error injection.
	if m.errGetBlockHash != nil {
		return "", m.errGetBlockHash
	}

	if h, ok := m.blockHashes[height]; ok {
		return h, nil
	}
	// Default: return a hash that does NOT match any block
	return "0000000000000000_default_no_match", nil
}

// newMockDaemonRPC creates a new mockDaemonRPC with default state.
func newMockDaemonRPC() *mockDaemonRPC {
	return &mockDaemonRPC{
		blockHashes:   make(map[uint64]string),
		bestBlockHash: fmt.Sprintf("%064d", 1000),
		chainHeight:   1000,
	}
}

// setBlockHash sets the hash for a specific height (thread-safe).
func (m *mockDaemonRPC) setBlockHash(height uint64, hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blockHashes == nil {
		m.blockHashes = make(map[uint64]string)
	}
	m.blockHashes[height] = hash
}

// setChainTip updates the chain tip and height (thread-safe).
func (m *mockDaemonRPC) setChainTip(height uint64, bestHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chainHeight = height
	m.bestBlockHash = bestHash
}

// =============================================================================
// Helper: build a test Processor
// =============================================================================

func newTestProcessor(store *mockBlockStore, rpc *mockDaemonRPC, maturity int) *Processor {
	cfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: maturity,
	}
	logger := zap.NewNop()
	return &Processor{
		cfg:          cfg,
		poolCfg:      &config.PoolConfig{},
		logger:       logger.Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}
}

// =============================================================================
// updateBlockConfirmations tests (5)
// =============================================================================

// TestUpdateBlockConfirmations_BlockReachesConfirmed verifies that a pending
// block at maturity with a matching hash is confirmed after
// StabilityWindowChecks consecutive stable cycles.
func TestUpdateBlockConfirmations_BlockReachesConfirmed(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000)
	blockHash := "abcdef1234567890abcdef1234567890" // ≥16 chars
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := "1111111111111111222222222222222233333333" // ≥16 chars

	store := &mockBlockStore{
		pendingBlocks: []*database.Block{
			{
				Height: blockHeight,
				Hash:   blockHash,
				Status: StatusPending,
				Miner:  "test-miner",
			},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run StabilityWindowChecks cycles — each cycle the block is at maturity,
	// hash matches, and tip is stable ⇒ stability counter increments.
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("expected UpdateBlockStatus to be called")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("expected status %q, got %q", StatusConfirmed, call.Status)
	}
}

// TestUpdateBlockConfirmations_HashMismatch_DelayedOrphan verifies that a
// hash mismatch must occur OrphanMismatchThreshold consecutive times before
// the block is marked orphaned.
func TestUpdateBlockConfirmations_HashMismatch_DelayedOrphan(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(2000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + 50
	tip := "cccc333333333333dddd444444444444"
	chainHashAtHeight := "eeee555555555555ffff666666666666" // different from blockHash

	store := &mockBlockStore{
		pendingBlocks: []*database.Block{
			{
				Height: blockHeight,
				Hash:   blockHash,
				Status: StatusPending,
				Miner:  "test-miner",
			},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: chainHashAtHeight},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run OrphanMismatchThreshold cycles with continuous hash mismatch.
	for i := 0; i < OrphanMismatchThreshold; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("expected UpdateBlockStatus to be called for orphaning")
	}
	if call.Status != StatusOrphaned {
		t.Errorf("expected status %q after %d mismatches, got %q",
			StatusOrphaned, OrphanMismatchThreshold, call.Status)
	}
}

// TestUpdateBlockConfirmations_MismatchThenRecovery verifies that when a hash
// mismatch occurs but later the hash matches again, the mismatch counter
// resets and the block can eventually confirm.
func TestUpdateBlockConfirmations_MismatchThenRecovery(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(3000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)
	tip := "cccc333333333333dddd444444444444"
	wrongHash := "eeee555555555555ffff666666666666"

	store := &mockBlockStore{
		pendingBlocks: []*database.Block{
			{
				Height: blockHeight,
				Hash:   blockHash,
				Status: StatusPending,
				Miner:  "test-miner",
			},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: wrongHash},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run 2 mismatch cycles (below threshold).
	for i := 0; i < OrphanMismatchThreshold-1; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("mismatch cycle %d: unexpected error: %v", i, err)
		}
	}

	// Verify not orphaned yet.
	if call, ok := store.lastStatusFor(blockHeight); ok && call.Status == StatusOrphaned {
		t.Fatal("block should NOT be orphaned yet")
	}

	// Now fix the hash — recovery.
	rpc.mu.Lock()
	rpc.blockHashes[blockHeight] = blockHash
	rpc.mu.Unlock()

	// Run StabilityWindowChecks more cycles — should reach confirmed.
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("recovery cycle %d: unexpected error: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("expected UpdateBlockStatus to be called")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("expected block to confirm after recovery, got status %q", call.Status)
	}
}

// TestUpdateBlockConfirmations_TOCTOU_TipChangeAbortsCycle verifies that if
// the chain tip changes between the initial snapshot and the per-block TOCTOU
// check, the cycle aborts without processing further blocks.
func TestUpdateBlockConfirmations_TOCTOU_TipChangeAbortsCycle(t *testing.T) {
	t.Parallel()

	blockHeight1 := uint64(4000)
	blockHeight2 := uint64(4001)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight2 + uint64(DefaultBlockMaturityConfirmations)
	initialTip := "1111111111111111222222222222222233333333"
	newTip := "9999999999999999888888888888888877777777"

	store := &mockBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight1, Hash: blockHash, Status: StatusPending, Miner: "m1"},
			{Height: blockHeight2, Hash: blockHash, Status: StatusPending, Miner: "m2"},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: initialTip,
		chainHeight:   chainHeight,
		blockHashes: map[uint64]string{
			blockHeight1: blockHash,
			blockHeight2: blockHash,
		},
		// First GetBlockchainInfo call = snapshot (call 1).
		// Second call = TOCTOU check for block 1 (call 2) — tip changes.
		changeTipAfterCalls: 1,
		newTip:              newTip,
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("expected nil error on TOCTOU abort, got: %v", err)
	}

	// Neither block should have been processed (status updates for pending progress).
	// The cycle should have aborted before processing any block.
	store.mu.Lock()
	statusCalls := len(store.updateStatusCalls)
	stabilityCalls := len(store.updateStabilityCalls)
	orphanCalls := len(store.updateOrphanCalls)
	store.mu.Unlock()

	if statusCalls > 0 || stabilityCalls > 0 || orphanCalls > 0 {
		t.Errorf("expected no DB updates on TOCTOU abort, got status=%d stability=%d orphan=%d",
			statusCalls, stabilityCalls, orphanCalls)
	}
}

// TestUpdateBlockConfirmations_BlockHeightExceedsChain verifies that when a
// block's height exceeds the current chain height, the mismatch counter is
// incremented (possible reorg).
func TestUpdateBlockConfirmations_BlockHeightExceedsChain(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(5000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight - 1 // block is ahead of chain
	tip := "cccc333333333333dddd444444444444"

	store := &mockBlockStore{
		pendingBlocks: []*database.Block{
			{
				Height: blockHeight,
				Hash:   blockHash,
				Status: StatusPending,
				Miner:  "test-miner",
			},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.updateBlockConfirmations(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify orphan mismatch count was incremented.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.updateOrphanCalls) != 1 {
		t.Fatalf("expected 1 UpdateBlockOrphanCount call, got %d", len(store.updateOrphanCalls))
	}
	if store.updateOrphanCalls[0].MismatchCount != 1 {
		t.Errorf("expected mismatch count 1, got %d", store.updateOrphanCalls[0].MismatchCount)
	}
}

// =============================================================================
// verifyConfirmedBlocks tests (3)
// =============================================================================

// TestVerifyConfirmedBlocks_StillInChain verifies that when a confirmed block's
// hash still matches the chain, no status update occurs.
func TestVerifyConfirmedBlocks_StillInChain(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(6000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + 500
	tip := "cccc333333333333dddd444444444444"

	store := &mockBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusConfirmed, Miner: "m1"},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No status update should have occurred — block is still valid.
	if call, ok := store.lastStatusFor(blockHeight); ok {
		t.Errorf("expected no status update, got %+v", call)
	}
}

// TestVerifyConfirmedBlocks_DeepReorg_Orphaned verifies that when a confirmed
// block's hash no longer matches the chain, it is marked orphaned.
func TestVerifyConfirmedBlocks_DeepReorg_Orphaned(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(7000)
	blockHash := "aaaa111111111111bbbb222222222222"
	reorgedHash := "ffff999999999999eeee888888888888"
	chainHeight := blockHeight + 500
	tip := "cccc333333333333dddd444444444444"

	store := &mockBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusConfirmed, Miner: "m1"},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: reorgedHash},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("expected UpdateBlockStatus call for deep reorg orphaning")
	}
	if call.Status != StatusOrphaned {
		t.Errorf("expected status %q, got %q", StatusOrphaned, call.Status)
	}
}

// TestVerifyConfirmedBlocks_BeyondMaxAge_Skipped verifies that confirmed blocks
// older than DeepReorgMaxAge are not re-verified (no GetBlockHash call).
func TestVerifyConfirmedBlocks_BeyondMaxAge_Skipped(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000)
	blockHash := "aaaa111111111111bbbb222222222222"
	// Chain height is beyond block + DeepReorgMaxAge so the block should be skipped.
	chainHeight := blockHeight + DeepReorgMaxAge + 100
	tip := "cccc333333333333dddd444444444444"

	store := &mockBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusConfirmed, Miner: "m1"},
		},
	}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: "different_should_not_matter"},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// GetBlockHash should NOT have been called for this block.
	rpc.mu.Lock()
	hashCalls := rpc.getBlockHashCalls
	rpc.mu.Unlock()

	if hashCalls != 0 {
		t.Errorf("expected 0 GetBlockHash calls for block beyond max age, got %d", hashCalls)
	}

	// No status update either.
	if call, ok := store.lastStatusFor(blockHeight); ok {
		t.Errorf("expected no status update for skipped block, got %+v", call)
	}
}

// =============================================================================
// processCycle tests (3)
// =============================================================================

// TestProcessCycle_FullCycleNoError verifies that processCycle completes without
// error or panic when there are no pending/confirmed blocks.
func TestProcessCycle_FullCycleNoError(t *testing.T) {
	t.Parallel()

	tip := "cccc333333333333dddd444444444444"
	store := &mockBlockStore{}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   10000,
		blockHashes:   map[uint64]string{},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	initialCycle := proc.cycleCount

	// Should not panic.
	proc.processCycle(context.Background())

	if proc.cycleCount != initialCycle+1 {
		t.Errorf("expected cycleCount to increment from %d to %d, got %d",
			initialCycle, initialCycle+1, proc.cycleCount)
	}
}

// TestProcessCycle_DeepReorgTriggersOnCycle10 verifies that verifyConfirmedBlocks
// is called when cycleCount reaches a multiple of DeepReorgCheckInterval.
func TestProcessCycle_DeepReorgTriggersOnCycle10(t *testing.T) {
	t.Parallel()

	tip := "cccc333333333333dddd444444444444"
	store := &mockBlockStore{}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   10000,
		blockHashes:   map[uint64]string{},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	// Set cycleCount so that after increment it equals DeepReorgCheckInterval.
	proc.cycleCount = DeepReorgCheckInterval - 1

	proc.processCycle(context.Background())

	if proc.cycleCount != DeepReorgCheckInterval {
		t.Errorf("expected cycleCount %d, got %d", DeepReorgCheckInterval, proc.cycleCount)
	}

	store.mu.Lock()
	called := store.getConfirmedCalled
	store.mu.Unlock()

	if !called {
		t.Error("expected GetConfirmedBlocks to be called on deep reorg check cycle")
	}
}

// TestProcessCycle_DeepReorgSkipsCycles1Through9 verifies that verifyConfirmedBlocks
// is NOT called on non-interval cycles.
func TestProcessCycle_DeepReorgSkipsCycles1Through9(t *testing.T) {
	t.Parallel()

	tip := "cccc333333333333dddd444444444444"
	store := &mockBlockStore{}

	rpc := &mockDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   10000,
		blockHashes:   map[uint64]string{},
	}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.cycleCount = 0 // After increment → 1, which is not a multiple of 10.

	proc.processCycle(context.Background())

	if proc.cycleCount != 1 {
		t.Errorf("expected cycleCount 1, got %d", proc.cycleCount)
	}

	store.mu.Lock()
	called := store.getConfirmedCalled
	store.mu.Unlock()

	if called {
		t.Error("GetConfirmedBlocks should NOT be called on non-interval cycles")
	}
}
