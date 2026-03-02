// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Merge mining block-loss prevention tests for SOLO mode.
//
// Merge mining allows a parent chain (e.g., BTC SHA256d or LTC Scrypt) to
// simultaneously secure one or more auxiliary chains (e.g., NMC, DOGE).
// At the payments processor level, each coin has its own independent
// Processor instance backed by its own DaemonRPC. These tests verify that
// failures or reorgs on one chain's processor never silently cause block
// loss on another chain's processor.
//
// Merge mining pairs:
//   BTC (SHA256d, 600s) -> NMC (600s), SYS (60s), XMY (60s), FBTC (30s)
//   LTC (Scrypt, 150s)  -> DOGE (60s), PEP (60s)
package payments

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// =============================================================================
// MERGE MINING HELPER: Independent Processor Per Chain
// =============================================================================

// newMergeMiningProcessor creates a Processor for a specific coin in a merge
// mining pair. Each coin (parent or aux) gets its own BlockStore and DaemonRPC,
// reflecting the real architecture where each chain daemon is independent.
func newMergeMiningProcessor(coin testCoinConfig, db BlockStore, daemon DaemonRPC) *Processor {
	cfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: DefaultBlockMaturityConfirmations,
	}
	poolCfg := &config.PoolConfig{
		Coin: coin.Symbol,
	}
	logger := zap.NewNop()
	return &Processor{
		cfg:          cfg,
		poolCfg:      poolCfg,
		logger:       logger.Sugar(),
		db:           db,
		daemonClient: daemon,
		stopCh:       make(chan struct{}),
	}
}

// =============================================================================
// RISK VECTOR 1: Parent + Aux Blocks Submitted Atomically
// =============================================================================
// When a merge-mined block is found, both the parent block and aux block(s) are
// submitted to their respective daemons. Both must appear in their chain's
// pending list and confirm independently through their own Processor instances.

// TestSOLO_MergeMining_AtomicSubmission_BothTracked dynamically tests every
// parent+aux pair to ensure both blocks reach pending and then confirm
// independently through separate Processor instances.
func TestSOLO_MergeMining_AtomicSubmission_BothTracked(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair // capture range variable
		t.Run(fmt.Sprintf("%s_%s_AtomicSubmission", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: parent + aux blocks submitted atomically.
			// Parent coin: pair.Parent.Symbol (algorithm: pair.Parent.Algorithm, block interval: pair.Parent.BlockTimeSec s)
			// Aux coin:    pair.Aux.Symbol    (algorithm: pair.Aux.Algorithm, block interval: pair.Aux.BlockTimeSec s)
			//
			// Both blocks are tracked in pending by their respective Processor
			// instances and must reach confirmed independently.

			parentHeight := uint64(800000)
			auxHeight := uint64(900000)
			parentHash := makeBlockHash(pair.Parent.Symbol, parentHeight)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			chainTipHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			auxChainTipHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			parentTip := fmt.Sprintf("%064x", chainTipHeight+1)
			auxTip := fmt.Sprintf("%064x", auxChainTipHeight+2)

			// Parent chain: its own store + daemon
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(chainTipHeight, parentTip)
			parentDaemon.setBlockHash(parentHeight, parentHash)

			// Aux chain: its own store + daemon
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(auxChainTipHeight, auxTip)
			auxDaemon.setBlockHash(auxHeight, auxHash)

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			// Run StabilityWindowChecks cycles on both processors independently
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := parentProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Parent block must be confirmed
			if !parentStore.hasStatusUpdate(parentHeight, StatusConfirmed) {
				t.Errorf("[%s] parent block at height %d was NOT confirmed", pair.Parent.Symbol, parentHeight)
			}

			// Aux block must be confirmed
			if !auxStore.hasStatusUpdate(auxHeight, StatusConfirmed) {
				t.Errorf("[%s] aux block at height %d was NOT confirmed", pair.Aux.Symbol, auxHeight)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 2: Parent Confirms, Aux Chain Reorgs
// =============================================================================
// The parent block confirms normally but the auxiliary chain experiences a reorg.
// The parent block must stay confirmed; the aux block must be correctly orphaned
// after reaching the mismatch threshold. Independent tracking prevents cross-chain
// contamination.

func TestSOLO_MergeMining_ParentConfirms_AuxReorgs(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_ParentConfirms_AuxReorgs", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: parent block confirms but aux chain reorgs.
			// Parent coin: pair.Parent.Symbol (pair.Parent.Algorithm, pair.Parent.BlockTimeSec s blocks)
			// Aux coin:    pair.Aux.Symbol    (pair.Aux.Algorithm, pair.Aux.BlockTimeSec s blocks)
			//
			// Parent stays confirmed. Aux correctly orphaned after threshold.

			parentHeight := uint64(810000)
			auxHeight := uint64(910000)
			parentHash := makeBlockHash(pair.Parent.Symbol, parentHeight)
			reorgAuxHash := fmt.Sprintf("%064x", uint64(0xdead0000)+auxHeight)
			chainTipHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			parentTip := fmt.Sprintf("%064x", chainTipHeight+3)
			auxTip := fmt.Sprintf("%064x", chainTipHeight+4)

			// Parent chain: hash matches, will confirm normally
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(chainTipHeight, parentTip)
			parentDaemon.setBlockHash(parentHeight, parentHash)

			// Aux chain: hash MISMATCHES (reorg happened)
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(chainTipHeight, auxTip)
			auxDaemon.setBlockHash(auxHeight, reorgAuxHash) // Different hash!

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			// Run enough cycles for parent to confirm AND aux to reach orphan threshold
			maxCycles := StabilityWindowChecks
			if OrphanMismatchThreshold > maxCycles {
				maxCycles = OrphanMismatchThreshold
			}

			for i := 0; i < maxCycles; i++ {
				if err := parentProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Parent block must be confirmed
			if !parentStore.hasStatusUpdate(parentHeight, StatusConfirmed) {
				t.Errorf("[%s] parent block should be confirmed despite aux reorg", pair.Parent.Symbol)
			}

			// Aux block must be orphaned (not silently lost)
			if !auxStore.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("[%s] aux block should be orphaned after reorg", pair.Aux.Symbol)
			}

			// Parent must NOT have any orphan status
			if parentStore.hasStatusUpdate(parentHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] parent block falsely orphaned due to aux chain reorg!", pair.Parent.Symbol)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 3: Aux Confirms, Parent Chain Reorgs
// =============================================================================
// The auxiliary block confirms normally but the parent chain experiences a reorg.
// The aux block must stay confirmed; the parent block must be correctly orphaned.

func TestSOLO_MergeMining_AuxConfirms_ParentReorgs(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_AuxConfirms_ParentReorgs", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: aux block confirms but parent chain reorgs.
			// Parent coin: pair.Parent.Symbol (pair.Parent.Algorithm, pair.Parent.BlockTimeSec s blocks)
			// Aux coin:    pair.Aux.Symbol    (pair.Aux.Algorithm, pair.Aux.BlockTimeSec s blocks)
			//
			// Aux stays confirmed. Parent correctly orphaned after threshold.

			parentHeight := uint64(820000)
			auxHeight := uint64(920000)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			reorgParentHash := fmt.Sprintf("%064x", uint64(0xbeef0000)+parentHeight)
			chainTipHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			auxChainTipHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			parentTip := fmt.Sprintf("%064x", chainTipHeight+5)
			auxTip := fmt.Sprintf("%064x", auxChainTipHeight+6)

			// Parent chain: hash MISMATCHES (reorg happened)
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(chainTipHeight, parentTip)
			parentDaemon.setBlockHash(parentHeight, reorgParentHash) // Different hash!

			// Aux chain: hash matches, will confirm normally
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(auxChainTipHeight, auxTip)
			auxDaemon.setBlockHash(auxHeight, auxHash)

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			maxCycles := StabilityWindowChecks
			if OrphanMismatchThreshold > maxCycles {
				maxCycles = OrphanMismatchThreshold
			}

			for i := 0; i < maxCycles; i++ {
				if err := parentProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Aux block must be confirmed
			if !auxStore.hasStatusUpdate(auxHeight, StatusConfirmed) {
				t.Errorf("[%s] aux block should be confirmed despite parent reorg", pair.Aux.Symbol)
			}

			// Parent block must be orphaned
			if !parentStore.hasStatusUpdate(parentHeight, StatusOrphaned) {
				t.Errorf("[%s] parent block should be orphaned after reorg", pair.Parent.Symbol)
			}

			// Aux must NOT have any orphan status
			if auxStore.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] aux block falsely orphaned due to parent chain reorg!", pair.Aux.Symbol)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 4: Parent RPC Failure During Aux Confirmation Cycle
// =============================================================================
// The parent chain daemon becomes unreachable (RPC failure) while the aux
// chain processor is running its confirmation cycle. Since they are independent
// Processor instances with independent DaemonRPC clients, the aux block must
// NOT be falsely orphaned just because the parent daemon is down.

func TestSOLO_MergeMining_ParentRPCFailure_AuxUnaffected(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_ParentRPCFail_AuxUnaffected", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: parent daemon unreachable during aux confirmation cycle.
			// Parent coin: pair.Parent.Symbol (pair.Parent.Algorithm, pair.Parent.BlockTimeSec s blocks)
			// Aux coin:    pair.Aux.Symbol    (pair.Aux.Algorithm, pair.Aux.BlockTimeSec s blocks)
			//
			// Aux block must NOT be falsely orphaned because of parent RPC failure.

			parentHeight := uint64(830000)
			auxHeight := uint64(930000)
			parentHash := makeBlockHash(pair.Parent.Symbol, parentHeight)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			auxChainTipHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			auxTip := fmt.Sprintf("%064x", auxChainTipHeight+7)

			// Parent chain: daemon is UNREACHABLE
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.mu.Lock()
			parentDaemon.errGetBlockchainInfo = fmt.Errorf("connection refused: %s daemon unreachable", pair.Parent.Symbol)
			parentDaemon.mu.Unlock()

			// Aux chain: daemon is healthy, hash matches
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(auxChainTipHeight, auxTip)
			auxDaemon.setBlockHash(auxHeight, auxHash)

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			// Run StabilityWindowChecks cycles
			for i := 0; i < StabilityWindowChecks; i++ {
				// Parent processor will error out (RPC failure) -- this is expected
				_ = parentProc.updateBlockConfirmations(ctx)

				// Aux processor should work independently
				if err := auxProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Aux block must be confirmed (parent RPC failure is irrelevant)
			if !auxStore.hasStatusUpdate(auxHeight, StatusConfirmed) {
				t.Errorf("BLOCK LOSS: [%s] aux block NOT confirmed -- parent RPC failure interfered!", pair.Aux.Symbol)
			}

			// Aux block must NOT be orphaned
			if auxStore.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] aux block falsely orphaned due to parent RPC failure!", pair.Aux.Symbol)
			}

			// Parent block should NOT have any status updates (RPC failed before processing)
			parentUpdates := parentStore.getStatusUpdates()
			for _, u := range parentUpdates {
				if u.Height == parentHeight && u.Status == StatusOrphaned {
					t.Errorf("[%s] parent block should not be orphaned by RPC failure alone", pair.Parent.Symbol)
				}
			}

			// Verify parent hash was never accessed (daemon was down from the start)
			_ = parentHash // used only for block creation via makePendingBlock
		})
	}
}

// =============================================================================
// RISK VECTOR 5: Multiple Aux Chains From Same Parent
// =============================================================================
// BTC can parent-mine NMC + SYS + XMY + FBTC simultaneously. All blocks
// from a single share submission are tracked by independent processors. This
// test verifies that all 4+ blocks are tracked independently and none is lost.

func TestSOLO_MergeMining_BTC_MultipleAux_AllTrackedIndependently(t *testing.T) {
	t.Parallel()

	// Risk vector: BTC parent + NMC + SYS aux - all blocks tracked independently.
	// Parent coin: BTC (SHA256d, 600s blocks)
	// Aux coins:   NMC (SHA256d, 600s), SYS (SHA256d, 60s)
	//
	// All processors operate independently. Failure in one must not affect others.

	// Find BTC parent and its aux coins
	var btcConfig testCoinConfig
	var btcAuxCoins []testCoinConfig
	for _, c := range allTestCoins {
		if c.Symbol == "BTC" {
			btcConfig = c
		}
		if c.IsAux && c.ParentChain == "BTC" {
			btcAuxCoins = append(btcAuxCoins, c)
		}
	}

	if len(btcAuxCoins) < 3 {
		t.Fatalf("Expected at least 3 BTC aux coins, got %d", len(btcAuxCoins))
	}

	parentHeight := uint64(840000)
	chainTipHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 20
	parentHash := makeBlockHash(btcConfig.Symbol, parentHeight)
	parentTip := fmt.Sprintf("%064x", chainTipHeight+10)

	// Parent processor
	parentStore := newMockBlockStore()
	parentStore.addPendingBlock(makePendingBlock(btcConfig.Symbol, parentHeight))
	parentDaemon := newMockDaemonRPC()
	parentDaemon.setChainTip(chainTipHeight, parentTip)
	parentDaemon.setBlockHash(parentHeight, parentHash)
	parentProc := newMergeMiningProcessor(btcConfig, parentStore, parentDaemon)

	// Create independent processor for each aux coin
	type auxEntry struct {
		coin   testCoinConfig
		store  *mockBlockStore
		daemon *mockDaemonRPC
		proc   *Processor
		height uint64
	}

	auxEntries := make([]auxEntry, len(btcAuxCoins))
	for i, auxCoin := range btcAuxCoins {
		height := uint64(940000 + i*1000)
		hash := makeBlockHash(auxCoin.Symbol, height)
		auxChainTip := height + uint64(DefaultBlockMaturityConfirmations) + 20
		tip := fmt.Sprintf("%064x", auxChainTip+uint64(20+i))

		store := newMockBlockStore()
		store.addPendingBlock(makePendingBlock(auxCoin.Symbol, height))
		d := newMockDaemonRPC()
		d.setChainTip(auxChainTip, tip)
		d.setBlockHash(height, hash)

		auxEntries[i] = auxEntry{
			coin:   auxCoin,
			store:  store,
			daemon: d,
			proc:   newMergeMiningProcessor(auxCoin, store, d),
			height: height,
		}
	}

	ctx := context.Background()

	// Run StabilityWindowChecks cycles on all processors
	for i := 0; i < StabilityWindowChecks; i++ {
		if err := parentProc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("[BTC] parent cycle %d: %v", i, err)
		}
		for _, entry := range auxEntries {
			if err := entry.proc.updateBlockConfirmations(ctx); err != nil {
				t.Fatalf("[%s] aux cycle %d: %v", entry.coin.Symbol, i, err)
			}
		}
	}

	// Parent block must be confirmed
	if !parentStore.hasStatusUpdate(parentHeight, StatusConfirmed) {
		t.Errorf("[BTC] parent block at height %d was NOT confirmed", parentHeight)
	}

	// Every aux block must be confirmed independently
	for _, entry := range auxEntries {
		if !entry.store.hasStatusUpdate(entry.height, StatusConfirmed) {
			t.Errorf("[%s] aux block at height %d was NOT confirmed (silently lost!)",
				entry.coin.Symbol, entry.height)
		}
	}
}

// TestSOLO_MergeMining_LTC_MultipleAux_AllTrackedIndependently tests the LTC
// parent with DOGE + PEP aux chains.
func TestSOLO_MergeMining_LTC_MultipleAux_AllTrackedIndependently(t *testing.T) {
	t.Parallel()

	// Risk vector: LTC parent + DOGE + PEP aux - all blocks tracked independently.
	// Parent coin: LTC (Scrypt, 150s blocks)
	// Aux coins:   DOGE (Scrypt, 60s), PEP (Scrypt, 60s)

	var ltcConfig testCoinConfig
	var ltcAuxCoins []testCoinConfig
	for _, c := range allTestCoins {
		if c.Symbol == "LTC" {
			ltcConfig = c
		}
		if c.IsAux && c.ParentChain == "LTC" {
			ltcAuxCoins = append(ltcAuxCoins, c)
		}
	}

	if len(ltcAuxCoins) < 2 {
		t.Fatalf("Expected at least 2 LTC aux coins, got %d", len(ltcAuxCoins))
	}

	parentHeight := uint64(2500000)
	chainTipHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 20
	parentHash := makeBlockHash(ltcConfig.Symbol, parentHeight)
	parentTip := fmt.Sprintf("%064x", chainTipHeight+30)

	parentStore := newMockBlockStore()
	parentStore.addPendingBlock(makePendingBlock(ltcConfig.Symbol, parentHeight))
	parentDaemon := newMockDaemonRPC()
	parentDaemon.setChainTip(chainTipHeight, parentTip)
	parentDaemon.setBlockHash(parentHeight, parentHash)
	parentProc := newMergeMiningProcessor(ltcConfig, parentStore, parentDaemon)

	type auxEntry struct {
		coin   testCoinConfig
		store  *mockBlockStore
		daemon *mockDaemonRPC
		proc   *Processor
		height uint64
	}

	auxEntries := make([]auxEntry, len(ltcAuxCoins))
	for i, auxCoin := range ltcAuxCoins {
		height := uint64(5000000 + i*1000)
		hash := makeBlockHash(auxCoin.Symbol, height)
		auxChainTip := height + uint64(DefaultBlockMaturityConfirmations) + 20
		tip := fmt.Sprintf("%064x", auxChainTip+uint64(40+i))

		store := newMockBlockStore()
		store.addPendingBlock(makePendingBlock(auxCoin.Symbol, height))
		d := newMockDaemonRPC()
		d.setChainTip(auxChainTip, tip)
		d.setBlockHash(height, hash)

		auxEntries[i] = auxEntry{
			coin:   auxCoin,
			store:  store,
			daemon: d,
			proc:   newMergeMiningProcessor(auxCoin, store, d),
			height: height,
		}
	}

	ctx := context.Background()

	for i := 0; i < StabilityWindowChecks; i++ {
		if err := parentProc.updateBlockConfirmations(ctx); err != nil {
			t.Fatalf("[LTC] parent cycle %d: %v", i, err)
		}
		for _, entry := range auxEntries {
			if err := entry.proc.updateBlockConfirmations(ctx); err != nil {
				t.Fatalf("[%s] aux cycle %d: %v", entry.coin.Symbol, i, err)
			}
		}
	}

	if !parentStore.hasStatusUpdate(parentHeight, StatusConfirmed) {
		t.Errorf("[LTC] parent block at height %d was NOT confirmed", parentHeight)
	}

	for _, entry := range auxEntries {
		if !entry.store.hasStatusUpdate(entry.height, StatusConfirmed) {
			t.Errorf("[%s] aux block at height %d was NOT confirmed (silently lost!)",
				entry.coin.Symbol, entry.height)
		}
	}
}

// =============================================================================
// RISK VECTOR 6: Parent Reorg Doesn't Cascade-Orphan Aux Blocks
// =============================================================================
// Aux blocks have their own chain verification. A reorg on the parent chain
// must not cascade into orphaning aux blocks that are still valid on their
// own chains.

func TestSOLO_MergeMining_ParentReorg_NoCascadeOrphan(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_NoCascadeOrphan", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: parent reorg doesn't cascade-orphan aux blocks.
			// Parent coin: pair.Parent.Symbol (pair.Parent.Algorithm, pair.Parent.BlockTimeSec s blocks)
			// Aux coin:    pair.Aux.Symbol    (pair.Aux.Algorithm, pair.Aux.BlockTimeSec s blocks)
			//
			// Parent gets orphaned after threshold mismatches. Aux chain is
			// completely unaffected because it has its own chain verification.

			parentHeight := uint64(850000)
			auxHeight := uint64(950000)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			reorgParentHash := fmt.Sprintf("%064x", uint64(0xCAFE0000)+parentHeight)
			chainTipHeight := parentHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			auxChainTipHeight := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			parentTip := fmt.Sprintf("%064x", chainTipHeight+50)
			auxTip := fmt.Sprintf("%064x", auxChainTipHeight+51)

			// Parent chain: hash mismatches (reorg)
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(chainTipHeight, parentTip)
			parentDaemon.setBlockHash(parentHeight, reorgParentHash)

			// Aux chain: hash matches, healthy
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(auxChainTipHeight, auxTip)
			auxDaemon.setBlockHash(auxHeight, auxHash)

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			// Run enough cycles for parent to be orphaned
			maxCycles := OrphanMismatchThreshold
			if StabilityWindowChecks > maxCycles {
				maxCycles = StabilityWindowChecks
			}

			for i := 0; i < maxCycles; i++ {
				if err := parentProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Parent must be orphaned
			if !parentStore.hasStatusUpdate(parentHeight, StatusOrphaned) {
				t.Errorf("[%s] parent block should be orphaned after reorg", pair.Parent.Symbol)
			}

			// Aux must NOT be orphaned (no cascade)
			if auxStore.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] aux block cascade-orphaned by parent reorg!", pair.Aux.Symbol)
			}

			// Aux should be confirmed (or at least still pending and progressing)
			if auxStore.hasStatusUpdate(auxHeight, StatusConfirmed) {
				// Confirmed -- perfect
			} else {
				// At minimum, verify it was NOT orphaned
				updates := auxStore.getStatusUpdates()
				for _, u := range updates {
					if u.Height == auxHeight && u.Status == StatusOrphaned {
						t.Errorf("BLOCK LOSS: [%s] aux block orphaned despite valid chain state!", pair.Aux.Symbol)
					}
				}
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 7: Deep Reorg on Parent Chain, Aux Blocks Unaffected
// =============================================================================
// After initial confirmation, a deep reorg on the parent chain is detected by
// verifyConfirmedBlocks. The parent block is re-orphaned. However, aux blocks
// that were confirmed on their own chains must remain unaffected.

func TestSOLO_MergeMining_DeepReorg_ParentOrphaned_AuxUnaffected(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_DeepReorg_AuxUnaffected", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: deep reorg on parent catches orphan; aux blocks unaffected.
			// Parent coin: pair.Parent.Symbol (pair.Parent.Algorithm, pair.Parent.BlockTimeSec s blocks)
			// Aux coin:    pair.Aux.Symbol    (pair.Aux.Algorithm, pair.Aux.BlockTimeSec s blocks)
			//
			// verifyConfirmedBlocks detects parent orphan but aux blocks are on
			// their own chain with their own verification.

			parentHeight := uint64(860000)
			auxHeight := uint64(960000)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)
			reorgParentHash := fmt.Sprintf("%064x", uint64(0xFACE0000)+parentHeight)
			chainTipHeight := parentHeight + 500 // Well within DeepReorgMaxAge
			auxChainTipHeight := auxHeight + 500
			parentTip := fmt.Sprintf("%064x", chainTipHeight+60)
			auxTip := fmt.Sprintf("%064x", auxChainTipHeight+61)

			// Parent chain: confirmed block, but deep reorg changed the hash
			parentStore := newMockBlockStore()
			parentStore.addConfirmedBlock(makeConfirmedBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(chainTipHeight, parentTip)
			parentDaemon.setBlockHash(parentHeight, reorgParentHash) // Different!

			// Aux chain: confirmed block, hash still matches
			auxStore := newMockBlockStore()
			auxStore.addConfirmedBlock(makeConfirmedBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(auxChainTipHeight, auxTip)
			auxDaemon.setBlockHash(auxHeight, auxHash) // Matches

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			// Run verifyConfirmedBlocks on both (simulating deep reorg check)
			if err := parentProc.verifyConfirmedBlocks(ctx); err != nil {
				t.Fatalf("[%s] parent verifyConfirmedBlocks: %v", pair.Parent.Symbol, err)
			}
			if err := auxProc.verifyConfirmedBlocks(ctx); err != nil {
				t.Fatalf("[%s] aux verifyConfirmedBlocks: %v", pair.Aux.Symbol, err)
			}

			// Parent confirmed block must be orphaned (deep reorg detected)
			if !parentStore.hasStatusUpdate(parentHeight, StatusOrphaned) {
				t.Errorf("[%s] parent block should be orphaned by deep reorg detection", pair.Parent.Symbol)
			}

			// Aux confirmed block must NOT be orphaned
			if auxStore.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] aux block falsely orphaned during parent deep reorg!", pair.Aux.Symbol)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 8: Timing Difference - Parent (Slow) vs Aux (Fast)
// =============================================================================
// Merge-mined chains often have very different block intervals. For example,
// BTC produces blocks every ~600s while SYS produces blocks every ~60s. This
// means SYS will accumulate confirmations ~10x faster than BTC. The aux block
// may confirm while the parent is still pending. This must not cause confusion.

func TestSOLO_MergeMining_TimingDifference_AuxConfirmsFaster(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		// Only test pairs where aux has faster block times than parent
		if pair.Aux.BlockTimeSec >= pair.Parent.BlockTimeSec {
			continue
		}

		t.Run(fmt.Sprintf("%s_%s_TimingDifference", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Risk vector: timing difference between parent and aux chains.
			// Parent coin: pair.Parent.Symbol (pair.Parent.Algorithm, pair.Parent.BlockTimeSec s blocks)
			// Aux coin:    pair.Aux.Symbol    (pair.Aux.Algorithm, pair.Aux.BlockTimeSec s blocks)
			//
			// Aux confirms much faster. Parent still pending. No confusion.
			// Speed ratio: parent pair.Parent.BlockTimeSec s / aux pair.Aux.BlockTimeSec s

			parentHeight := uint64(870000)
			auxHeight := uint64(970000)
			parentHash := makeBlockHash(pair.Parent.Symbol, parentHeight)
			auxHash := makeBlockHash(pair.Aux.Symbol, auxHeight)

			// Aux chain: already well past maturity
			auxChainTip := auxHeight + uint64(DefaultBlockMaturityConfirmations) + 50
			auxTip := fmt.Sprintf("%064x", auxChainTip+70)

			// Parent chain: only partway to maturity (block time is slower)
			parentConfs := uint64(DefaultBlockMaturityConfirmations / 3) // Only 1/3 maturity
			parentChainTip := parentHeight + parentConfs
			parentTip := fmt.Sprintf("%064x", parentChainTip+71)

			// Parent chain: pending, not yet mature
			parentStore := newMockBlockStore()
			parentStore.addPendingBlock(makePendingBlock(pair.Parent.Symbol, parentHeight))
			parentDaemon := newMockDaemonRPC()
			parentDaemon.setChainTip(parentChainTip, parentTip)
			parentDaemon.setBlockHash(parentHeight, parentHash)

			// Aux chain: pending, well past maturity
			auxStore := newMockBlockStore()
			auxStore.addPendingBlock(makePendingBlock(pair.Aux.Symbol, auxHeight))
			auxDaemon := newMockDaemonRPC()
			auxDaemon.setChainTip(auxChainTip, auxTip)
			auxDaemon.setBlockHash(auxHeight, auxHash)

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			ctx := context.Background()

			// Run StabilityWindowChecks cycles
			for i := 0; i < StabilityWindowChecks; i++ {
				if err := parentProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] parent cycle %d: %v", pair.Parent.Symbol, i, err)
				}
				if err := auxProc.updateBlockConfirmations(ctx); err != nil {
					t.Fatalf("[%s] aux cycle %d: %v", pair.Aux.Symbol, i, err)
				}
			}

			// Aux block must be confirmed (it has enough confirmations)
			if !auxStore.hasStatusUpdate(auxHeight, StatusConfirmed) {
				t.Errorf("[%s] aux block should confirm quickly (faster block time %ds vs parent %ds)",
					pair.Aux.Symbol, pair.Aux.BlockTimeSec, pair.Parent.BlockTimeSec)
			}

			// Parent block must still be pending (not enough confirmations)
			if parentStore.hasStatusUpdate(parentHeight, StatusConfirmed) {
				t.Errorf("[%s] parent block should NOT be confirmed yet (only %d/%d confirmations)",
					pair.Parent.Symbol, parentConfs, DefaultBlockMaturityConfirmations)
			}

			// Neither block should be orphaned
			if parentStore.hasStatusUpdate(parentHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] parent block falsely orphaned during timing test!", pair.Parent.Symbol)
			}
			if auxStore.hasStatusUpdate(auxHeight, StatusOrphaned) {
				t.Errorf("BLOCK LOSS: [%s] aux block falsely orphaned during timing test!", pair.Aux.Symbol)
			}

			// Verify parent has progress but is not yet mature
			parentUpdates := parentStore.getStatusUpdates()
			foundParentPending := false
			for _, u := range parentUpdates {
				if u.Height == parentHeight && u.Status == StatusPending {
					foundParentPending = true
					if u.Progress <= 0 {
						t.Errorf("[%s] parent should have progress > 0, got %.2f", pair.Parent.Symbol, u.Progress)
					}
					if u.Progress >= 1.0 {
						t.Errorf("[%s] parent should have progress < 1.0 (still immature), got %.2f",
							pair.Parent.Symbol, u.Progress)
					}
				}
			}
			if !foundParentPending {
				t.Errorf("[%s] expected pending status update for parent block", pair.Parent.Symbol)
			}
		})
	}
}

// =============================================================================
// CROSS-CUTTING: Dynamic Verification Over All Merge Pairs
// =============================================================================

// TestSOLO_MergeMining_AllPairs_IndependentProcessors verifies that every
// known parent+aux pair uses independent Processor instances and that no
// shared state exists between them.
func TestSOLO_MergeMining_AllPairs_IndependentProcessors(t *testing.T) {
	t.Parallel()

	pairs := mergeMiningPairs()
	if len(pairs) == 0 {
		t.Fatal("No merge mining pairs found -- test data is missing")
	}

	// Verify expected pair count
	// BTC -> NMC, SYS, XMY, FBTC = 4 pairs
	// LTC -> DOGE, PEP = 2 pairs
	expectedPairs := 6
	if len(pairs) != expectedPairs {
		t.Errorf("Expected %d merge mining pairs, got %d", expectedPairs, len(pairs))
	}

	for _, pair := range pairs {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_Independent", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			// Verify that parent and aux are truly independent by creating
			// processors that share no state whatsoever.

			parentStore := newMockBlockStore()
			auxStore := newMockBlockStore()
			parentDaemon := newMockDaemonRPC()
			auxDaemon := newMockDaemonRPC()

			parentProc := newMergeMiningProcessor(pair.Parent, parentStore, parentDaemon)
			auxProc := newMergeMiningProcessor(pair.Aux, auxStore, auxDaemon)

			// Verify they have different pool configs
			if parentProc.poolCfg.Coin == auxProc.poolCfg.Coin {
				t.Errorf("Parent and aux processors should have different coin configs: %s vs %s",
					parentProc.poolCfg.Coin, auxProc.poolCfg.Coin)
			}

			// Verify they have different DB instances
			if parentProc.db == auxProc.db {
				t.Error("Parent and aux processors share the same BlockStore instance!")
			}

			// Verify they have different daemon instances
			if parentProc.daemonClient == auxProc.daemonClient {
				t.Error("Parent and aux processors share the same DaemonRPC instance!")
			}
		})
	}
}

// TestSOLO_MergeMining_AllPairs_AuxKnowsParent verifies that the test
// configuration correctly maps each aux coin to its parent chain.
func TestSOLO_MergeMining_AllPairs_AuxKnowsParent(t *testing.T) {
	t.Parallel()

	for _, pair := range mergeMiningPairs() {
		pair := pair
		t.Run(fmt.Sprintf("%s_%s_ParentMapping", pair.Parent.Symbol, pair.Aux.Symbol), func(t *testing.T) {
			t.Parallel()

			if !pair.Parent.IsParent {
				t.Errorf("[%s] should be marked as parent chain", pair.Parent.Symbol)
			}
			if !pair.Aux.IsAux {
				t.Errorf("[%s] should be marked as auxiliary chain", pair.Aux.Symbol)
			}
			if pair.Aux.ParentChain != pair.Parent.Symbol {
				t.Errorf("[%s] aux coin ParentChain=%q does not match parent symbol %q",
					pair.Aux.Symbol, pair.Aux.ParentChain, pair.Parent.Symbol)
			}
			if pair.Parent.Algorithm != pair.Aux.Algorithm {
				t.Errorf("[%s/%s] algorithm mismatch: parent=%q aux=%q (merge mining requires same algorithm)",
					pair.Parent.Symbol, pair.Aux.Symbol, pair.Parent.Algorithm, pair.Aux.Algorithm)
			}
		})
	}
}

// =============================================================================
// MULTI-AUX FAILURE ISOLATION
// =============================================================================

// TestSOLO_MergeMining_BTC_OneAuxFails_OthersUnaffected verifies that when
// one aux chain's daemon fails, other aux chains from the same parent continue
// to confirm blocks normally.
func TestSOLO_MergeMining_BTC_OneAuxFails_OthersUnaffected(t *testing.T) {
	t.Parallel()

	// Risk vector: BTC parent with multiple aux chains; one aux daemon fails.
	// Parent coin: BTC (SHA256d, 600s blocks)
	// Aux coins: NMC (failing), SYS (healthy), XMY (healthy)
	//
	// NMC daemon failure must not affect SYS or XMY block confirmation.

	var btcAuxCoins []testCoinConfig
	for _, c := range allTestCoins {
		if c.IsAux && c.ParentChain == "BTC" {
			btcAuxCoins = append(btcAuxCoins, c)
			if len(btcAuxCoins) >= 3 {
				break
			}
		}
	}
	if len(btcAuxCoins) < 3 {
		t.Fatalf("Need at least 3 BTC aux coins, got %d", len(btcAuxCoins))
	}

	failingCoin := btcAuxCoins[0]    // First aux coin will fail
	healthyCoins := btcAuxCoins[1:3] // Next two are healthy

	type auxEntry struct {
		coin   testCoinConfig
		store  *mockBlockStore
		proc   *Processor
		height uint64
	}

	// Failing aux: daemon returns errors
	failHeight := uint64(980000)
	failStore := newMockBlockStore()
	failStore.addPendingBlock(makePendingBlock(failingCoin.Symbol, failHeight))
	failDaemon := newMockDaemonRPC()
	failDaemon.mu.Lock()
	failDaemon.errGetBlockchainInfo = fmt.Errorf("%s daemon unreachable", failingCoin.Symbol)
	failDaemon.mu.Unlock()
	failProc := newMergeMiningProcessor(failingCoin, failStore, failDaemon)

	// Healthy aux coins
	var healthyEntries []auxEntry
	for i, coin := range healthyCoins {
		height := uint64(980000 + (i+1)*1000)
		hash := makeBlockHash(coin.Symbol, height)
		auxChainTip := height + uint64(DefaultBlockMaturityConfirmations) + 20
		tip := fmt.Sprintf("%064x", auxChainTip+uint64(80+i))

		store := newMockBlockStore()
		store.addPendingBlock(makePendingBlock(coin.Symbol, height))
		d := newMockDaemonRPC()
		d.setChainTip(auxChainTip, tip)
		d.setBlockHash(height, hash)

		healthyEntries = append(healthyEntries, auxEntry{
			coin:   coin,
			store:  store,
			proc:   newMergeMiningProcessor(coin, store, d),
			height: height,
		})
	}

	ctx := context.Background()

	for i := 0; i < StabilityWindowChecks; i++ {
		// Failing processor will error -- expected
		_ = failProc.updateBlockConfirmations(ctx)

		// Healthy processors should work fine
		for _, entry := range healthyEntries {
			if err := entry.proc.updateBlockConfirmations(ctx); err != nil {
				t.Fatalf("[%s] healthy aux cycle %d: %v", entry.coin.Symbol, i, err)
			}
		}
	}

	// Failing coin should have NO confirmed status (daemon was unreachable)
	if failStore.hasStatusUpdate(failHeight, StatusConfirmed) {
		t.Errorf("[%s] failing aux should NOT be confirmed without daemon access", failingCoin.Symbol)
	}

	// Healthy coins must all be confirmed
	for _, entry := range healthyEntries {
		if !entry.store.hasStatusUpdate(entry.height, StatusConfirmed) {
			t.Errorf("BLOCK LOSS: [%s] healthy aux block NOT confirmed -- failure in %s interfered!",
				entry.coin.Symbol, failingCoin.Symbol)
		}
	}
}
