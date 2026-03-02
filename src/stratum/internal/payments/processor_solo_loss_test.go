// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

// =============================================================================
// SOLO MINING BLOCK LOSS PREVENTION: PROCESSOR TEST SUITE
// =============================================================================
//
// These tests exercise the payment processor's block confirmation logic to
// verify that no valid block is silently lost due to:
//   1. RPC/daemon errors mid-cycle (GetBlockHash, GetBlockchainInfo failures)
//   2. Chain reorgs immediately after block submission
//   3. TOCTOU (Time-of-Check-Time-of-Use) race conditions
//   4. Edge-case block data (uint64 overflow, high heights, exact maturity)
//   5. Deep reorg detection for previously confirmed blocks
//   6. Stability window edge cases (tip flapping, partial stability)
//   7. Delayed orphaning threshold behavior

// =============================================================================
// Mock Types (extending existing patterns from processor_functional_test.go)
// =============================================================================

// soloBlockStore is an in-memory block store that supports error injection.
type soloBlockStore struct {
	mu              sync.Mutex
	pendingBlocks   []*database.Block
	confirmedBlocks []*database.Block

	// Call tracking
	updateStatusCalls    []soloStatusCall
	updateOrphanCalls    []soloOrphanCall
	updateStabilityCalls []soloStabilityCall
	getConfirmedCalled   bool

	// Error injection
	getPendingErr   error
	getConfirmedErr error
	updateStatusErr error
}

type soloStatusCall struct {
	Height               uint64
	Status               string
	ConfirmationProgress float64
}

type soloOrphanCall struct {
	Height        uint64
	MismatchCount int
}

type soloStabilityCall struct {
	Height         uint64
	StabilityCount int
	LastTip        string
}

func (m *soloBlockStore) GetPendingBlocks(_ context.Context) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getPendingErr != nil {
		return nil, m.getPendingErr
	}
	var result []*database.Block
	for _, b := range m.pendingBlocks {
		if b.Status == StatusPending {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *soloBlockStore) GetConfirmedBlocks(_ context.Context) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getConfirmedCalled = true
	if m.getConfirmedErr != nil {
		return nil, m.getConfirmedErr
	}
	var result []*database.Block
	for _, b := range m.confirmedBlocks {
		if b.Status == StatusConfirmed {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *soloBlockStore) UpdateBlockStatus(_ context.Context, height uint64, hash string, status string, confirmationProgress float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatusCalls = append(m.updateStatusCalls, soloStatusCall{height, status, confirmationProgress})
	if m.updateStatusErr != nil {
		return m.updateStatusErr
	}
	// V1 FIX: Only update the block matching BOTH height AND hash.
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

func (m *soloBlockStore) UpdateBlockOrphanCount(_ context.Context, height uint64, hash string, mismatchCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateOrphanCalls = append(m.updateOrphanCalls, soloOrphanCall{height, mismatchCount})
	// V1 FIX: Only update the block matching BOTH height AND hash.
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.OrphanMismatchCount = mismatchCount
		}
	}
	return nil
}

func (m *soloBlockStore) UpdateBlockStabilityCount(_ context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStabilityCalls = append(m.updateStabilityCalls, soloStabilityCall{height, stabilityCount, lastTip})
	// V1 FIX: Only update the block matching BOTH height AND hash.
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.StabilityCheckCount = stabilityCount
			b.LastVerifiedTip = lastTip
		}
	}
	return nil
}

func (m *soloBlockStore) GetBlocksByStatus(_ context.Context, status string) ([]*database.Block, error) {
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

func (m *soloBlockStore) GetBlockStats(_ context.Context) (*database.BlockStats, error) {
	return &database.BlockStats{}, nil
}

func (m *soloBlockStore) UpdateBlockConfirmationState(_ context.Context, height uint64, hash string, status string, confirmationProgress float64, orphanMismatchCount int, stabilityCheckCount int, lastVerifiedTip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateStatusCalls = append(m.updateStatusCalls, soloStatusCall{height, status, confirmationProgress})
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

func (m *soloBlockStore) lastStatusFor(height uint64) (soloStatusCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.updateStatusCalls) - 1; i >= 0; i-- {
		if m.updateStatusCalls[i].Height == height {
			return m.updateStatusCalls[i], true
		}
	}
	return soloStatusCall{}, false
}

// soloDaemonRPC provides a configurable daemon with error injection and
// call counting for testing RPC failure modes.
type soloDaemonRPC struct {
	mu sync.Mutex

	bestBlockHash string
	chainHeight   uint64
	blockHashes   map[uint64]string

	// TOCTOU simulation
	changeTipAfterCalls int
	newTip              string
	newHeight           uint64

	// Error injection
	blockchainInfoErr error
	blockHashErr      error
	blockHashErrAfterN int // Fail GetBlockHash after N calls (0=never)

	// Call counting
	getBlockchainInfoCalls int
	getBlockHashCalls      int
}

func (m *soloDaemonRPC) GetBlockchainInfo(_ context.Context) (*daemon.BlockchainInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getBlockchainInfoCalls++

	if m.blockchainInfoErr != nil {
		return nil, m.blockchainInfoErr
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

func (m *soloDaemonRPC) GetBlockHash(_ context.Context, height uint64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getBlockHashCalls++

	if m.blockHashErrAfterN > 0 && m.getBlockHashCalls >= m.blockHashErrAfterN {
		return "", m.blockHashErr
	}
	if m.blockHashErr != nil && m.blockHashErrAfterN == 0 {
		return "", m.blockHashErr
	}

	if h, ok := m.blockHashes[height]; ok {
		return h, nil
	}
	return "0000000000000000_default_no_match", nil
}

func newSOLOTestProcessor(store *soloBlockStore, rpc *soloDaemonRPC, maturity int) *Processor {
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
// RISK VECTOR 1: RPC/Daemon Errors Mid-Cycle
// =============================================================================

// TestSOLO_Processor_GetBlockchainInfo_Fails_NoPanicNoOrphan verifies that
// when GetBlockchainInfo fails during a confirmation cycle, no block is
// falsely orphaned. The cycle should abort gracefully and retry next time.
func TestSOLO_Processor_GetBlockchainInfo_Fails_NoPanicNoOrphan(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(100000)
	blockHash := "aaaa111111111111bbbb222222222222"

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo_miner"},
		},
	}

	rpc := &soloDaemonRPC{
		blockchainInfoErr: fmt.Errorf("connection refused"),
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err == nil {
		t.Fatal("Expected error from failed GetBlockchainInfo")
	}

	// CRITICAL: Block must NOT be orphaned due to RPC error
	store.mu.Lock()
	statusCalls := len(store.updateStatusCalls)
	store.mu.Unlock()

	if statusCalls > 0 {
		call, _ := store.lastStatusFor(blockHeight)
		if call.Status == StatusOrphaned {
			t.Fatal("BLOCK LOSS: Block falsely orphaned due to RPC error!")
		}
	}
}

// TestSOLO_Processor_GetBlockHash_Fails_SkipsBlock verifies that when
// GetBlockHash fails for a specific block, that block is skipped (not
// orphaned) and the cycle continues processing other blocks.
func TestSOLO_Processor_GetBlockHash_Fails_SkipsBlock(t *testing.T) {
	t.Parallel()

	blockHeight1 := uint64(200000)
	blockHeight2 := uint64(200001)
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight2 + 50

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight1, Hash: blockHash, Status: StatusPending, Miner: "m1"},
			{Height: blockHeight2, Hash: blockHash, Status: StatusPending, Miner: "m2"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash:      tip,
		chainHeight:        chainHeight,
		blockHashes:        map[uint64]string{blockHeight2: blockHash},
		blockHashErr:       fmt.Errorf("timeout"),
		blockHashErrAfterN: 1, // First GetBlockHash call fails
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Block 1 should NOT be orphaned (RPC error → skip)
	store.mu.Lock()
	for _, call := range store.updateStatusCalls {
		if call.Height == blockHeight1 && call.Status == StatusOrphaned {
			t.Error("BLOCK LOSS: Block 1 orphaned due to GetBlockHash timeout!")
		}
	}
	store.mu.Unlock()
}

// TestSOLO_Processor_GetBlockchainInfo_Fails_VerifyConfirmedBlocks verifies
// that when GetBlockchainInfo fails during deep reorg verification, confirmed
// blocks are not falsely orphaned.
func TestSOLO_Processor_GetBlockchainInfo_Fails_VerifyConfirmedBlocks(t *testing.T) {
	t.Parallel()

	store := &soloBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: 300000, Hash: "confirmed_hash_1", Status: StatusConfirmed, Miner: "m1"},
		},
	}

	rpc := &soloDaemonRPC{
		blockchainInfoErr: fmt.Errorf("daemon unreachable"),
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.verifyConfirmedBlocks(context.Background())
	if err == nil {
		t.Fatal("Expected error from failed GetBlockchainInfo")
	}

	// Confirmed block must NOT be marked orphaned
	if call, ok := store.lastStatusFor(300000); ok {
		if call.Status == StatusOrphaned {
			t.Fatal("BLOCK LOSS: Confirmed block orphaned due to RPC error!")
		}
	}
}

// =============================================================================
// RISK VECTOR 2: Chain Reorgs After Block Submission
// =============================================================================

// TestSOLO_Processor_ImmediateReorg_DelayedOrphaning verifies that a reorg
// immediately after submission doesn't instantly orphan the block. The
// delayed orphaning threshold prevents false positive orphaning from
// temporary node desync.
func TestSOLO_Processor_ImmediateReorg_DelayedOrphaning(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(400000)
	blockHash := "aaaa111111111111bbbb222222222222"
	reorgHash := "ffff999999999999eeee888888888888"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + 50

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: reorgHash}, // Different hash!
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run ONE cycle — block should NOT be orphaned (threshold not reached)
	if err := proc.updateBlockConfirmations(context.Background()); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if call, ok := store.lastStatusFor(blockHeight); ok {
		if call.Status == StatusOrphaned {
			t.Fatal("BLOCK LOSS: Block orphaned after single mismatch (threshold not respected!)")
		}
	}

	// Verify mismatch counter was incremented
	store.mu.Lock()
	orphanCalls := store.updateOrphanCalls
	store.mu.Unlock()

	if len(orphanCalls) != 1 || orphanCalls[0].MismatchCount != 1 {
		t.Errorf("Expected mismatch count 1, got %v", orphanCalls)
	}
}

// TestSOLO_Processor_Reorg_RecoverAfterNearOrphan verifies that a block
// can recover from being near the orphan threshold when the chain tip
// stabilizes and the hash matches again.
func TestSOLO_Processor_Reorg_RecoverAfterNearOrphan(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(500000)
	blockHash := "aaaa111111111111bbbb222222222222"
	wrongHash := "eeee555555555555ffff666666666666"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations)

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: wrongHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run OrphanMismatchThreshold-1 mismatch cycles
	for i := 0; i < OrphanMismatchThreshold-1; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	// Verify NOT orphaned yet
	if call, ok := store.lastStatusFor(blockHeight); ok && call.Status == StatusOrphaned {
		t.Fatal("Block orphaned too early!")
	}

	// Now fix the hash — chain recovered
	rpc.mu.Lock()
	rpc.blockHashes[blockHeight] = blockHash
	rpc.mu.Unlock()

	// Run StabilityWindowChecks cycles with correct hash → should confirm
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("recovery cycle %d: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus call")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("Block should confirm after recovery, got status %q", call.Status)
	}
}

// TestSOLO_Processor_Reorg_OrphanAfterThreshold verifies that a block IS
// orphaned after OrphanMismatchThreshold consecutive mismatches.
func TestSOLO_Processor_Reorg_OrphanAfterThreshold(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(600000)
	blockHash := "aaaa111111111111bbbb222222222222"
	wrongHash := "eeee555555555555ffff666666666666"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + 50

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: wrongHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run exactly OrphanMismatchThreshold cycles
	for i := 0; i < OrphanMismatchThreshold; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus call for orphaning")
	}
	if call.Status != StatusOrphaned {
		t.Errorf("Block should be orphaned after %d mismatches, got %q",
			OrphanMismatchThreshold, call.Status)
	}
}

// TestSOLO_Processor_DeepReorg_ConfirmedBlockOrphaned verifies that deep
// chain reorganizations are detected during periodic re-verification,
// catching orphans that occur after initial confirmation.
func TestSOLO_Processor_DeepReorg_ConfirmedBlockOrphaned(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(700000)
	blockHash := "aaaa111111111111bbbb222222222222"
	reorgHash := "ffff999999999999eeee888888888888"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + 500

	store := &soloBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusConfirmed, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: reorgHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus call for deep reorg")
	}
	if call.Status != StatusOrphaned {
		t.Errorf("Confirmed block should be orphaned after deep reorg, got %q", call.Status)
	}
}

// TestSOLO_Processor_DeepReorg_OldBlockSkipped verifies that blocks beyond
// DeepReorgMaxAge are not re-verified (performance optimization with
// diminishing returns).
func TestSOLO_Processor_DeepReorg_OldBlockSkipped(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000)
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + DeepReorgMaxAge + 100

	store := &soloBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusConfirmed, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: "different_but_should_not_matter"},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// GetBlockHash should NOT have been called (block is too old)
	rpc.mu.Lock()
	hashCalls := rpc.getBlockHashCalls
	rpc.mu.Unlock()

	if hashCalls != 0 {
		t.Errorf("Expected 0 GetBlockHash calls for old block, got %d", hashCalls)
	}
}

// =============================================================================
// RISK VECTOR 3: TOCTOU Race Conditions
// =============================================================================

// TestSOLO_Processor_TOCTOU_TipChangeDuringOrphanCheck verifies that if
// the chain tip changes between the initial snapshot and a per-block TOCTOU
// check, the entire cycle aborts without processing any blocks.
func TestSOLO_Processor_TOCTOU_TipChangeDuringOrphanCheck(t *testing.T) {
	t.Parallel()

	blockHash := "aaaa111111111111bbbb222222222222"
	initialTip := "1111111111111111222222222222222233333333"
	newTip := "9999999999999999888888888888888877777777"
	chainHeight := uint64(800200)

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: 800100, Hash: blockHash, Status: StatusPending, Miner: "m1"},
			{Height: 800101, Hash: blockHash, Status: StatusPending, Miner: "m2"},
			{Height: 800102, Hash: blockHash, Status: StatusPending, Miner: "m3"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: initialTip,
		chainHeight:   chainHeight,
		blockHashes: map[uint64]string{
			800100: blockHash,
			800101: blockHash,
			800102: blockHash,
		},
		// Tip changes after first GetBlockchainInfo call (snapshot)
		changeTipAfterCalls: 1,
		newTip:              newTip,
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("Expected nil error on TOCTOU abort, got: %v", err)
	}

	// NO blocks should have been processed (cycle aborted)
	store.mu.Lock()
	statusCalls := len(store.updateStatusCalls)
	stabilityCalls := len(store.updateStabilityCalls)
	orphanCalls := len(store.updateOrphanCalls)
	store.mu.Unlock()

	if statusCalls > 0 || stabilityCalls > 0 || orphanCalls > 0 {
		t.Errorf("TOCTOU: Expected no DB updates on abort, got status=%d stability=%d orphan=%d",
			statusCalls, stabilityCalls, orphanCalls)
	}
}

// TestSOLO_Processor_TOCTOU_DeepReorgVerification verifies that tip change
// during deep reorg verification also aborts the cycle.
func TestSOLO_Processor_TOCTOU_DeepReorgVerification(t *testing.T) {
	t.Parallel()

	blockHash := "aaaa111111111111bbbb222222222222"
	initialTip := "1111111111111111222222222222222233333333"
	newTip := "9999999999999999888888888888888877777777"
	chainHeight := uint64(900500)

	store := &soloBlockStore{
		confirmedBlocks: []*database.Block{
			{Height: 900000, Hash: blockHash, Status: StatusConfirmed, Miner: "m1"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash:       initialTip,
		chainHeight:         chainHeight,
		blockHashes:         map[uint64]string{900000: "different_hash"},
		changeTipAfterCalls: 1, // Tip changes after snapshot
		newTip:              newTip,
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.verifyConfirmedBlocks(context.Background()); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Block should NOT be orphaned (cycle aborted due to TOCTOU)
	if call, ok := store.lastStatusFor(900000); ok {
		if call.Status == StatusOrphaned {
			t.Error("BLOCK LOSS: Block falsely orphaned during TOCTOU tip change")
		}
	}
}

// =============================================================================
// RISK VECTOR 4: Edge-Case Block Data
// =============================================================================

// TestSOLO_Processor_BlockHeightExceedsChainHeight verifies that when a
// block's height is ahead of the current chain (possible during reorg),
// the uint64 subtraction doesn't underflow and the block is handled safely.
func TestSOLO_Processor_BlockHeightExceedsChainHeight(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1000100) // Ahead of chain!
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := uint64(1000099) // Behind the block

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Should not panic (uint64 underflow protection)
	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Block should NOT be immediately orphaned — just mismatch counter increment
	if call, ok := store.lastStatusFor(blockHeight); ok {
		if call.Status == StatusOrphaned {
			t.Error("Block ahead of chain should not be immediately orphaned")
		}
	}
}

// TestSOLO_Processor_MaxUint64Height verifies no overflow at extreme heights.
func TestSOLO_Processor_MaxUint64Height(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(math.MaxUint64 - 200) // Very high but below max
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := uint64(math.MaxUint64 - 100)

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Should not panic due to overflow
	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error at extreme height: %v", err)
	}
}

// TestSOLO_Processor_ZeroConfirmations verifies correct handling at block
// height equal to chain height (0 confirmations).
func TestSOLO_Processor_ZeroConfirmations(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1100000)
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   blockHeight, // Same height = 0 confirmations
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	if err := proc.updateBlockConfirmations(context.Background()); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Block should remain pending (0 confirmations < maturity)
	store.mu.Lock()
	var confirmedCount int
	for _, call := range store.updateStatusCalls {
		if call.Height == blockHeight && call.Status == StatusConfirmed {
			confirmedCount++
		}
	}
	store.mu.Unlock()

	if confirmedCount > 0 {
		t.Error("Block with 0 confirmations should not be confirmed!")
	}
}

// TestSOLO_Processor_ExactMaturityBoundary verifies correct behavior when
// a block has exactly the maturity number of confirmations.
func TestSOLO_Processor_ExactMaturityBoundary(t *testing.T) {
	t.Parallel()

	maturity := 50
	blockHeight := uint64(1200000)
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + uint64(maturity) // Exactly at maturity

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newSOLOTestProcessor(store, rpc, maturity)

	// Run StabilityWindowChecks cycles — block should confirm
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus call")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("Block at exact maturity should confirm after stability window, got %q",
			call.Status)
	}
}

// =============================================================================
// RISK VECTOR 5: Stability Window Edge Cases
// =============================================================================

// TestSOLO_Processor_StabilityWindow_TipFlapping verifies that when the
// chain tip keeps changing (flapping), the stability counter still increments
// as long as the block hash matches at its height. Tip changes no longer
// reset stability — fast-block chains (DGB 15s, regtest <5s) would never
// confirm otherwise. The hash check is the real stability signal.
func TestSOLO_Processor_StabilityWindow_TipFlapping(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1300000)
	blockHash := "aaaa111111111111bbbb222222222222"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	// Tip changes every call (flapping chain) but block hash still matches
	tips := []string{
		"1111111111111111222222222222222233333333",
		"4444444444444444555555555555555566666666",
		"7777777777777777888888888888888899999999",
		"aaaa111111111111bbbb222222222222cccccccc",
		"dddd111111111111eeee222222222222ffffffff",
	}

	for tipIdx := 0; tipIdx < len(tips); tipIdx++ {
		rpc := &soloDaemonRPC{
			bestBlockHash: tips[tipIdx],
			chainHeight:   chainHeight + uint64(tipIdx), // Chain advancing (not reorg)
			blockHashes:   map[uint64]string{blockHeight: blockHash},
		}

		proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", tipIdx, err)
		}
	}

	// Block SHOULD be confirmed after StabilityWindowChecks (3) cycles,
	// because production no longer resets stability on tip change.
	// The block hash matches at its height each cycle — that's sufficient.
	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("expected at least one status update for block")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("expected block to confirm (hash matches despite tip changes), got status %q", call.Status)
	}
}

// TestSOLO_Processor_StabilityWindow_ConfirmsAfterStable verifies that
// after tip stops changing, the stability window eventually passes and
// the block confirms.
func TestSOLO_Processor_StabilityWindow_ConfirmsAfterStable(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1400000)
	blockHash := "aaaa111111111111bbbb222222222222"
	stableTip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: stableTip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run exactly StabilityWindowChecks cycles with stable tip
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	call, ok := store.lastStatusFor(blockHeight)
	if !ok {
		t.Fatal("Expected UpdateBlockStatus call")
	}
	if call.Status != StatusConfirmed {
		t.Errorf("Block should confirm after %d stable cycles, got %q",
			StabilityWindowChecks, call.Status)
	}
}

// TestSOLO_Processor_StabilityWindow_HashMismatchResetsCounter verifies
// that a hash mismatch during the stability window resets the stability
// counter, preventing premature confirmation.
func TestSOLO_Processor_StabilityWindow_HashMismatchResetsCounter(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1500000)
	blockHash := "aaaa111111111111bbbb222222222222"
	wrongHash := "ffff999999999999eeee888888888888"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes:   map[uint64]string{blockHeight: blockHash},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run StabilityWindowChecks-1 stable cycles
	for i := 0; i < StabilityWindowChecks-1; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("stable cycle %d: %v", i, err)
		}
	}

	// Now introduce a hash mismatch — should reset stability
	rpc.mu.Lock()
	rpc.blockHashes[blockHeight] = wrongHash
	rpc.mu.Unlock()

	if err := proc.updateBlockConfirmations(context.Background()); err != nil {
		t.Fatalf("mismatch cycle: %v", err)
	}

	// Block should NOT be confirmed (stability was reset by mismatch)
	confirmed := false
	store.mu.Lock()
	for _, call := range store.updateStatusCalls {
		if call.Height == blockHeight && call.Status == StatusConfirmed {
			confirmed = true
		}
	}
	store.mu.Unlock()

	if confirmed {
		t.Error("Block should not confirm after hash mismatch during stability window")
	}
}

// =============================================================================
// RISK VECTOR 6: Multiple Pending Blocks
// =============================================================================

// TestSOLO_Processor_MultipleBlocks_IndependentTracking verifies that
// orphan/stability counters are tracked independently for each block.
// One block being orphaned should not affect other pending blocks.
func TestSOLO_Processor_MultipleBlocks_IndependentTracking(t *testing.T) {
	t.Parallel()

	tip := "cccc333333333333dddd444444444444"
	chainHeight := uint64(1600200)

	goodHash := "aaaa111111111111bbbb222222222222"
	badHash := "ffff999999999999eeee888888888888"

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: 1600000, Hash: goodHash, Status: StatusPending, Miner: "good_miner"},
			{Height: 1600100, Hash: badHash, Status: StatusPending, Miner: "bad_miner"},
		},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   chainHeight,
		blockHashes: map[uint64]string{
			1600000: goodHash,                             // Matches
			1600100: "eeee555555555555ffff666666666666", // Different!
		},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run StabilityWindowChecks cycles — good block should confirm,
	// bad block should just accumulate mismatches
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	// Good block should be confirmed
	goodCall, goodOk := store.lastStatusFor(1600000)
	if !goodOk {
		t.Fatal("Expected status update for good block")
	}
	if goodCall.Status != StatusConfirmed {
		t.Errorf("Good block should be confirmed, got %q", goodCall.Status)
	}

	// Bad block should NOT be orphaned yet (only StabilityWindowChecks mismatches,
	// which may be less than OrphanMismatchThreshold)
	// It should just have accumulated mismatches
	store.mu.Lock()
	badOrphanCount := 0
	for _, call := range store.updateOrphanCalls {
		if call.Height == 1600100 {
			badOrphanCount = call.MismatchCount // Use latest
		}
	}
	store.mu.Unlock()

	if badOrphanCount == 0 {
		t.Error("Bad block should have accumulated mismatch count")
	}
}

// =============================================================================
// RISK VECTOR 7: Process Cycle Orchestration
// =============================================================================

// TestSOLO_Processor_ProcessCycle_DeepReorgCheckInterval verifies that
// deep reorg verification runs on the correct cycle interval.
func TestSOLO_Processor_ProcessCycle_DeepReorgCheckInterval(t *testing.T) {
	t.Parallel()

	tip := "cccc333333333333dddd444444444444"
	store := &soloBlockStore{}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   10000,
		blockHashes:   map[uint64]string{},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run cycles 1 through DeepReorgCheckInterval
	for i := 0; i < DeepReorgCheckInterval; i++ {
		proc.processCycle(context.Background())
	}

	// GetConfirmedBlocks should have been called exactly once
	// (on the cycle where cycleCount % DeepReorgCheckInterval == 0)
	store.mu.Lock()
	called := store.getConfirmedCalled
	store.mu.Unlock()

	if !called {
		t.Error("Deep reorg check was not triggered on interval cycle")
	}
}

// TestSOLO_Processor_ProcessCycle_SkipsBeforeInterval verifies that deep
// reorg checks don't run on non-interval cycles.
func TestSOLO_Processor_ProcessCycle_SkipsBeforeInterval(t *testing.T) {
	t.Parallel()

	tip := "cccc333333333333dddd444444444444"
	store := &soloBlockStore{}

	rpc := &soloDaemonRPC{
		bestBlockHash: tip,
		chainHeight:   10000,
		blockHashes:   map[uint64]string{},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run only 1 cycle (not at interval)
	proc.processCycle(context.Background())

	store.mu.Lock()
	called := store.getConfirmedCalled
	store.mu.Unlock()

	if called {
		t.Error("Deep reorg check should NOT run on cycle 1")
	}
}

// TestSOLO_Processor_NoPendingBlocks_NoError verifies that an empty
// pending block list doesn't cause errors or panics.
func TestSOLO_Processor_NoPendingBlocks_NoError(t *testing.T) {
	t.Parallel()

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{},
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: "cccc333333333333dddd444444444444",
		chainHeight:   10000,
		blockHashes:   map[uint64]string{},
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Should complete without error or panic
	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("Unexpected error with no pending blocks: %v", err)
	}
}

// TestSOLO_Processor_DatabaseError_NoPanicNoOrphan verifies that when
// the database returns an error for GetPendingBlocks, no blocks are
// affected and the error is propagated.
func TestSOLO_Processor_DatabaseError_NoPanicNoOrphan(t *testing.T) {
	t.Parallel()

	store := &soloBlockStore{
		getPendingErr: fmt.Errorf("database connection lost"),
	}

	rpc := &soloDaemonRPC{
		bestBlockHash: "cccc333333333333dddd444444444444",
		chainHeight:   10000,
	}

	proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	err := proc.updateBlockConfirmations(context.Background())
	if err == nil {
		t.Fatal("Expected error from database failure")
	}
}

// TestSOLO_Processor_ConfirmationProgress_Calculation verifies that
// confirmation progress is calculated correctly and capped at 1.0.
func TestSOLO_Processor_ConfirmationProgress_Calculation(t *testing.T) {
	t.Parallel()

	maturity := 100
	blockHeight := uint64(1700000)
	blockHash := "aaaa111111111111bbbb222222222222"
	tip := "cccc333333333333dddd444444444444"

	testCases := []struct {
		name             string
		chainHeight      uint64
		expectedProgress float64
	}{
		{"0%", blockHeight, 0.0},
		{"50%", blockHeight + 50, 0.5},
		{"99%", blockHeight + 99, 0.99},
		{"100%", blockHeight + 100, 1.0},
		{"150% capped", blockHeight + 150, 1.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &soloBlockStore{
				pendingBlocks: []*database.Block{
					{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
				},
			}

			rpc := &soloDaemonRPC{
				bestBlockHash: tip,
				chainHeight:   tc.chainHeight,
				blockHashes:   map[uint64]string{blockHeight: blockHash},
			}

			proc := newSOLOTestProcessor(store, rpc, maturity)

			if err := proc.updateBlockConfirmations(context.Background()); err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			// Find the progress update
			store.mu.Lock()
			var foundProgress float64
			for _, call := range store.updateStatusCalls {
				if call.Height == blockHeight {
					foundProgress = call.ConfirmationProgress
				}
			}
			store.mu.Unlock()

			// Allow small floating point tolerance
			if math.Abs(foundProgress-tc.expectedProgress) > 0.01 {
				t.Errorf("Expected progress %.2f, got %.2f", tc.expectedProgress, foundProgress)
			}
		})
	}
}

// TestSOLO_Processor_IntermittentMismatches_NoOrphan verifies that
// intermittent hash mismatches (flaky node) don't cause orphaning
// because the counter resets on each match.
func TestSOLO_Processor_IntermittentMismatches_NoOrphan(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(1800000)
	blockHash := "aaaa111111111111bbbb222222222222"
	wrongHash := "ffff999999999999eeee888888888888"
	tip := "cccc333333333333dddd444444444444"
	chainHeight := blockHeight + 50

	store := &soloBlockStore{
		pendingBlocks: []*database.Block{
			{Height: blockHeight, Hash: blockHash, Status: StatusPending, Miner: "solo"},
		},
	}

	// Pattern: mismatch, mismatch, match, mismatch, match, mismatch, match
	// Counter should reset on each match, never reaching threshold.
	hashSequence := []string{wrongHash, wrongHash, blockHash, wrongHash, blockHash, wrongHash, blockHash}

	for _, currentHash := range hashSequence {
		rpc := &soloDaemonRPC{
			bestBlockHash: tip,
			chainHeight:   chainHeight,
			blockHashes:   map[uint64]string{blockHeight: currentHash},
		}

		proc := newSOLOTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

		if err := proc.updateBlockConfirmations(context.Background()); err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	// Block should NOT be orphaned
	if call, ok := store.lastStatusFor(blockHeight); ok {
		if call.Status == StatusOrphaned {
			t.Fatal("BLOCK LOSS: Block orphaned due to intermittent mismatches (counter should reset)")
		}
	}
}
