// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Shared types and helpers for the paranoid SOLO block-loss test suite.
//
// IMPORTANT: The base mock types (mockBlockStore, mockDaemonRPC, newTestProcessor)
// are defined in processor_functional_test.go. This file adds:
//   - Coin configuration for dynamic multi-coin testing
//   - Error-injecting mock variants for failure scenario testing
//   - Helper functions for block/hash generation
package payments

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// COIN CONFIGURATIONS FOR DYNAMIC TESTING
// ═══════════════════════════════════════════════════════════════════════════════

// testCoinConfig holds static coin parameters for parameterized tests.
type testCoinConfig struct {
	Symbol       string
	Algorithm    string
	BlockTimeSec int
	IsParent     bool   // Can serve as merge mining parent
	IsAux        bool   // Supports auxiliary proof-of-work
	ParentChain  string // Parent chain symbol (if aux)
}

// allTestCoins defines all 13 supported coins with their characteristics.
// Source of truth: config/coins.manifest.yaml + internal/coin/*.go
var allTestCoins = []testCoinConfig{
	// SHA-256d coins
	{Symbol: "BTC", Algorithm: "sha256d", BlockTimeSec: 600, IsParent: true},
	{Symbol: "BCH", Algorithm: "sha256d", BlockTimeSec: 600},
	{Symbol: "DGB", Algorithm: "sha256d", BlockTimeSec: 15},
	{Symbol: "BC2", Algorithm: "sha256d", BlockTimeSec: 600},
	{Symbol: "NMC", Algorithm: "sha256d", BlockTimeSec: 600, IsAux: true, ParentChain: "BTC"},
	{Symbol: "SYS", Algorithm: "sha256d", BlockTimeSec: 60, IsAux: true, ParentChain: "BTC"},
	{Symbol: "XMY", Algorithm: "sha256d", BlockTimeSec: 60, IsAux: true, ParentChain: "BTC"},
	{Symbol: "FBTC", Algorithm: "sha256d", BlockTimeSec: 30, IsAux: true, ParentChain: "BTC"},

	// Scrypt coins
	{Symbol: "LTC", Algorithm: "scrypt", BlockTimeSec: 150, IsParent: true},
	{Symbol: "DOGE", Algorithm: "scrypt", BlockTimeSec: 60, IsAux: true, ParentChain: "LTC"},
	{Symbol: "DGB-SCRYPT", Algorithm: "scrypt", BlockTimeSec: 15},
	{Symbol: "PEP", Algorithm: "scrypt", BlockTimeSec: 60, IsAux: true, ParentChain: "LTC"},
	{Symbol: "CAT", Algorithm: "scrypt", BlockTimeSec: 600},
}

// sha256dCoins returns only SHA-256d coins.
func sha256dCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.Algorithm == "sha256d" {
			coins = append(coins, c)
		}
	}
	return coins
}

// scryptCoins returns only Scrypt coins.
func scryptCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.Algorithm == "scrypt" {
			coins = append(coins, c)
		}
	}
	return coins
}

// auxCoins returns only merge-mineable auxiliary coins.
func auxCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.IsAux {
			coins = append(coins, c)
		}
	}
	return coins
}

// parentCoins returns only parent chain coins.
func parentCoins() []testCoinConfig {
	var coins []testCoinConfig
	for _, c := range allTestCoins {
		if c.IsParent {
			coins = append(coins, c)
		}
	}
	return coins
}

// mergePair defines a parent + auxiliary coin merge mining combination.
type mergePair struct {
	Parent testCoinConfig
	Aux    testCoinConfig
}

// mergeMiningPairs returns all parent+aux combinations.
func mergeMiningPairs() []mergePair {
	var pairs []mergePair
	parents := make(map[string]testCoinConfig)
	for _, c := range allTestCoins {
		if c.IsParent {
			parents[c.Symbol] = c
		}
	}
	for _, c := range allTestCoins {
		if c.IsAux {
			if p, ok := parents[c.ParentChain]; ok {
				pairs = append(pairs, mergePair{Parent: p, Aux: c})
			}
		}
	}
	return pairs
}

// ═══════════════════════════════════════════════════════════════════════════════
// ERROR-INJECTING BLOCK STORE (extends base mockBlockStore capabilities)
// ═══════════════════════════════════════════════════════════════════════════════

// errInjectBlockStore implements BlockStore with configurable error injection.
// Unlike the base mockBlockStore (processor_functional_test.go), this variant
// supports per-method error injection and failure counting for resilience tests.
type errInjectBlockStore struct {
	mu sync.Mutex

	// Block data
	pendingBlocks   []*database.Block
	confirmedBlocks []*database.Block

	// Call recording
	statusUpdates    []statusUpdateRecord
	orphanUpdates    []orphanUpdateRecord
	stabilityUpdates []stabilityUpdateRecord
	getConfirmedCalled bool

	// Error injection — static errors
	errGetPending      error
	errGetConfirmed    error
	errUpdateStatus    error
	errUpdateOrphan    error
	errUpdateStability error
	errGetStats        error

	// Error injection — countdown (errors for next N calls, then succeeds)
	failGetPendingN   int
	failGetConfirmedN int
}

type statusUpdateRecord struct {
	Height   uint64
	Status   string
	Progress float64
}

type orphanUpdateRecord struct {
	Height        uint64
	MismatchCount int
}

type stabilityUpdateRecord struct {
	Height         uint64
	StabilityCount int
	LastTip        string
}

func newErrInjectBlockStore() *errInjectBlockStore {
	return &errInjectBlockStore{}
}

func (m *errInjectBlockStore) GetPendingBlocks(_ context.Context) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failGetPendingN > 0 {
		m.failGetPendingN--
		return nil, fmt.Errorf("injected GetPendingBlocks failure")
	}
	if m.errGetPending != nil {
		return nil, m.errGetPending
	}

	var result []*database.Block
	for _, b := range m.pendingBlocks {
		if b.Status == StatusPending {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *errInjectBlockStore) GetConfirmedBlocks(_ context.Context) ([]*database.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.getConfirmedCalled = true
	if m.failGetConfirmedN > 0 {
		m.failGetConfirmedN--
		return nil, fmt.Errorf("injected GetConfirmedBlocks failure")
	}
	if m.errGetConfirmed != nil {
		return nil, m.errGetConfirmed
	}

	var result []*database.Block
	for _, b := range m.confirmedBlocks {
		if b.Status == StatusConfirmed {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *errInjectBlockStore) UpdateBlockStatus(_ context.Context, height uint64, hash string, status string, progress float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.errUpdateStatus != nil {
		return m.errUpdateStatus
	}

	m.statusUpdates = append(m.statusUpdates, statusUpdateRecord{height, status, progress})
	// V1 FIX: Mutate only the block matching BOTH height AND hash
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.Status = status
			b.ConfirmationProgress = progress
		}
	}
	for _, b := range m.confirmedBlocks {
		if b.Height == height && b.Hash == hash {
			b.Status = status
			b.ConfirmationProgress = progress
		}
	}
	return nil
}

func (m *errInjectBlockStore) UpdateBlockOrphanCount(_ context.Context, height uint64, hash string, mismatchCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.errUpdateOrphan != nil {
		return m.errUpdateOrphan
	}

	m.orphanUpdates = append(m.orphanUpdates, orphanUpdateRecord{height, mismatchCount})
	// V1 FIX: Only update the block matching BOTH height AND hash
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.OrphanMismatchCount = mismatchCount
		}
	}
	return nil
}

func (m *errInjectBlockStore) UpdateBlockStabilityCount(_ context.Context, height uint64, hash string, stabilityCount int, lastTip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.errUpdateStability != nil {
		return m.errUpdateStability
	}

	m.stabilityUpdates = append(m.stabilityUpdates, stabilityUpdateRecord{height, stabilityCount, lastTip})
	// V1 FIX: Only update the block matching BOTH height AND hash
	for _, b := range m.pendingBlocks {
		if b.Height == height && b.Hash == hash {
			b.StabilityCheckCount = stabilityCount
			b.LastVerifiedTip = lastTip
		}
	}
	return nil
}

func (m *errInjectBlockStore) GetBlocksByStatus(_ context.Context, status string) ([]*database.Block, error) {
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

func (m *errInjectBlockStore) GetBlockStats(_ context.Context) (*database.BlockStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errGetStats != nil {
		return nil, m.errGetStats
	}
	return &database.BlockStats{}, nil
}

func (m *errInjectBlockStore) UpdateBlockConfirmationState(_ context.Context, height uint64, hash string, status string, confirmationProgress float64, orphanMismatchCount int, stabilityCheckCount int, lastVerifiedTip string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Respect error injection — this is the atomic replacement for UpdateBlockStatus
	if m.errUpdateStatus != nil {
		return m.errUpdateStatus
	}
	m.statusUpdates = append(m.statusUpdates, statusUpdateRecord{height, status, confirmationProgress})
	// Also record stability updates so tests checking stabilityUpdates work
	// with the atomic UpdateBlockConfirmationState code path.
	if stabilityCheckCount > 0 {
		m.stabilityUpdates = append(m.stabilityUpdates, stabilityUpdateRecord{height, stabilityCheckCount, lastVerifiedTip})
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
	return nil
}

// addPendingBlock adds a pending block (thread-safe).
func (m *errInjectBlockStore) addPendingBlock(block *database.Block) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingBlocks = append(m.pendingBlocks, block)
}

// addConfirmedBlock adds a confirmed block (thread-safe).
func (m *errInjectBlockStore) addConfirmedBlock(block *database.Block) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirmedBlocks = append(m.confirmedBlocks, block)
}

// hasStatusUpdateFor checks if a specific height+status was recorded.
func (m *errInjectBlockStore) hasStatusUpdateFor(height uint64, status string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.statusUpdates {
		if u.Height == height && u.Status == status {
			return true
		}
	}
	return false
}

// statusUpdateCount returns the number of status updates recorded.
func (m *errInjectBlockStore) statusUpdateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.statusUpdates)
}

// orphanUpdateCount returns the number of orphan updates recorded.
func (m *errInjectBlockStore) orphanUpdateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.orphanUpdates)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ERROR-INJECTING DAEMON RPC
// ═══════════════════════════════════════════════════════════════════════════════

// errInjectDaemonRPC implements DaemonRPC with configurable error injection.
// Supports per-call custom behavior, countdown failures, tip mutation, and
// call counting for paranoid testing of RPC failure scenarios.
type errInjectDaemonRPC struct {
	mu sync.Mutex

	// Static responses
	bestBlockHash string
	chainHeight   uint64
	blockHashes   map[uint64]string

	// Call counting
	getBlockchainInfoCalls atomic.Int64
	getBlockHashCalls      atomic.Int64

	// Static error injection
	errGetBlockchainInfo error
	errGetBlockHash      error

	// Countdown error injection (errors for next N calls)
	failBlockchainInfoN int
	failBlockHashN      int

	// Per-call custom behavior (overrides static responses)
	blockchainInfoFunc func(ctx context.Context, callIndex int64) (*daemon.BlockchainInfo, error)
	blockHashFunc      func(ctx context.Context, height uint64, callIndex int64) (string, error)

	// TOCTOU simulation
	changeTipAfterCalls int    // 0 = never change
	newTip              string // tip to switch to
	newHeight           uint64 // height to switch to
}

func newErrInjectDaemonRPC() *errInjectDaemonRPC {
	return &errInjectDaemonRPC{
		blockHashes: make(map[uint64]string),
		bestBlockHash: fmt.Sprintf("%064d", 1000),
		chainHeight:   1000,
	}
}

func (m *errInjectDaemonRPC) GetBlockchainInfo(_ context.Context) (*daemon.BlockchainInfo, error) {
	callIdx := m.getBlockchainInfoCalls.Add(1)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Countdown failure injection
	if m.failBlockchainInfoN > 0 {
		m.failBlockchainInfoN--
		return nil, fmt.Errorf("injected GetBlockchainInfo failure")
	}

	// Static error
	if m.errGetBlockchainInfo != nil {
		return nil, m.errGetBlockchainInfo
	}

	// Custom function
	if m.blockchainInfoFunc != nil {
		return m.blockchainInfoFunc(nil, callIdx)
	}

	tip := m.bestBlockHash
	height := m.chainHeight

	// TOCTOU simulation
	if m.changeTipAfterCalls > 0 && int(callIdx) > m.changeTipAfterCalls {
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

func (m *errInjectDaemonRPC) GetBlockHash(_ context.Context, height uint64) (string, error) {
	callIdx := m.getBlockHashCalls.Add(1)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Countdown failure injection
	if m.failBlockHashN > 0 {
		m.failBlockHashN--
		return "", fmt.Errorf("injected GetBlockHash failure")
	}

	// Static error
	if m.errGetBlockHash != nil {
		return "", m.errGetBlockHash
	}

	// Custom function
	if m.blockHashFunc != nil {
		return m.blockHashFunc(nil, height, callIdx)
	}

	hash, ok := m.blockHashes[height]
	if !ok {
		return "0000000000000000_default_no_match_0000000000000000000000000000", nil
	}
	return hash, nil
}

// setBlockHash sets the hash for a specific height.
func (m *errInjectDaemonRPC) setBlockHash(height uint64, hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockHashes[height] = hash
}

// setChainTip updates the chain tip and height.
func (m *errInjectDaemonRPC) setChainTip(height uint64, bestHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chainHeight = height
	m.bestBlockHash = bestHash
}

// ═══════════════════════════════════════════════════════════════════════════════
// HELPER: Build test processor with error-injecting mocks
// ═══════════════════════════════════════════════════════════════════════════════

// newParanoidTestProcessor creates a Processor with error-injecting mocks.
func newParanoidTestProcessor(store BlockStore, rpc DaemonRPC, maturity int) *Processor {
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

// ═══════════════════════════════════════════════════════════════════════════════
// TEST HELPERS — Block / Hash Generation
// ═══════════════════════════════════════════════════════════════════════════════

// makeBlockHash generates a deterministic 64-char hex hash from height and coin.
// The hash is guaranteed to be ≥16 chars for the [:16]+"..." log slicing.
func makeBlockHash(coinSymbol string, height uint64) string {
	return fmt.Sprintf("%032x%032x", height, uint64(len(coinSymbol))+height)
}

// makeChainTip generates a deterministic 64-char chain tip hash.
func makeChainTip(height uint64) string {
	return fmt.Sprintf("tip_%060d", height)
}

// makePendingBlock creates a database.Block in pending status for testing.
func makePendingBlock(coinSymbol string, height uint64) *database.Block {
	return &database.Block{
		Height:               height,
		Hash:                 makeBlockHash(coinSymbol, height),
		Status:               StatusPending,
		Miner:                "test_miner_" + coinSymbol,
		ConfirmationProgress: 0,
		Reward:               6.25,
	}
}

// makeConfirmedBlock creates a database.Block in confirmed status for testing.
func makeConfirmedBlock(coinSymbol string, height uint64) *database.Block {
	return &database.Block{
		Height:               height,
		Hash:                 makeBlockHash(coinSymbol, height),
		Status:               StatusConfirmed,
		Miner:                "test_miner_" + coinSymbol,
		ConfirmationProgress: 1.0,
		Reward:               6.25,
	}
}
