// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Long-lived multi-coin/multi-chain payment processor lifecycle simulation.
//
// These tests exercise independent, concurrent processor operation across
// multiple coin types, verifying isolation, timing auto-scaling, failure
// handling, recovery, deep reorg detection, stale block sweeps, stability
// windows, orphan vote accumulation, and extended sustained operation.
package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// HELPERS
// ═══════════════════════════════════════════════════════════════════════════════

// newMulticoinProcessor creates a Processor configured with BlockTime for
// auto-scaling tests. Interval is set to 0 so getEffectiveInterval and
// getDeepReorgMaxAge fall through to BlockTime-based calculation.
func newMulticoinProcessor(store BlockStore, rpc DaemonRPC, maturity int, blockTimeSec int) *Processor {
	cfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      0, // Force auto-scaling from BlockTime
		Scheme:        "SOLO",
		BlockMaturity: maturity,
		BlockTime:     blockTimeSec,
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

// getEffectiveIntervalForTest creates a throwaway Processor for a given coin
// and returns its auto-scaled effective interval. Because getEffectiveInterval
// is unexported, calling it from a _test.go file in the same package works.
func getEffectiveIntervalForTest(t *testing.T, coin testCoinConfig) time.Duration {
	t.Helper()
	bs := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	p := newMulticoinProcessor(bs, rpc, DefaultBlockMaturityConfirmations, coin.BlockTimeSec)
	return p.getEffectiveInterval()
}

// getDeepReorgMaxAgeForTest creates a throwaway Processor for a given coin
// and returns its auto-scaled deep reorg max age.
func getDeepReorgMaxAgeForTest(t *testing.T, coin testCoinConfig) uint64 {
	t.Helper()
	bs := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	p := newMulticoinProcessor(bs, rpc, DefaultBlockMaturityConfirmations, coin.BlockTimeSec)
	return p.getDeepReorgMaxAge()
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 1: Independent Processors — No Cross-Contamination
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_IndependentProcessors(t *testing.T) {
	t.Parallel()

	type coinTest struct {
		symbol  string
		heights [3]uint64
	}

	coins := []coinTest{
		{symbol: "DGB", heights: [3]uint64{100000, 100001, 100002}},
		{symbol: "RVN", heights: [3]uint64{200000, 200001, 200002}},
		{symbol: "BTC", heights: [3]uint64{300000, 300001, 300002}},
		{symbol: "LTC", heights: [3]uint64{400000, 400001, 400002}},
		{symbol: "DOGE", heights: [3]uint64{500000, 500001, 500002}},
	}

	type result struct {
		symbol        string
		statusUpdates []statusUpdateRecord
		err           error
	}

	results := make([]result, len(coins))
	var wg sync.WaitGroup

	for i, ct := range coins {
		i, ct := i, ct
		wg.Add(1)
		go func() {
			defer wg.Done()

			store := newErrInjectBlockStore()
			rpc := newErrInjectDaemonRPC()

			// Set chain tip well above all block heights.
			chainHeight := ct.heights[2] + uint64(DefaultBlockMaturityConfirmations) + 50
			tipHash := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, tipHash)

			for _, h := range ct.heights {
				block := makePendingBlock(ct.symbol, h)
				store.addPendingBlock(block)
				rpc.setBlockHash(h, makeBlockHash(ct.symbol, h))
			}

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
			err := proc.updateBlockConfirmations(context.Background())

			store.mu.Lock()
			updates := make([]statusUpdateRecord, len(store.statusUpdates))
			copy(updates, store.statusUpdates)
			store.mu.Unlock()

			results[i] = result{
				symbol:        ct.symbol,
				statusUpdates: updates,
				err:           err,
			}
		}()
	}

	wg.Wait()

	// Verify each processor only touched its own blocks.
	for _, r := range results {
		if r.err != nil {
			t.Errorf("[%s] unexpected error: %v", r.symbol, r.err)
			continue
		}
		for _, u := range r.statusUpdates {
			// Verify the updated height belongs to this coin.
			found := false
			for _, ct := range coins {
				if ct.symbol == r.symbol {
					for _, h := range ct.heights {
						if u.Height == h {
							found = true
							break
						}
					}
					break
				}
			}
			if !found {
				t.Errorf("[%s] status update for height %d does not belong to this coin", r.symbol, u.Height)
			}
		}
		// Each coin should have exactly 3 status updates (one per block).
		if len(r.statusUpdates) != 3 {
			t.Errorf("[%s] expected 3 status updates, got %d", r.symbol, len(r.statusUpdates))
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 2: All Coins — getEffectiveInterval Auto-Scaling
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_DifferentBlockTimes_EffectiveInterval(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			got := getEffectiveIntervalForTest(t, coin)

			// Expected: blockTime * 10, floored at 60s, capped at 10 minutes.
			expected := time.Duration(coin.BlockTimeSec*10) * time.Second
			if expected < 60*time.Second {
				expected = 60 * time.Second
			}
			if expected > 10*time.Minute {
				expected = 10 * time.Minute
			}

			if got != expected {
				t.Errorf("[%s] getEffectiveInterval = %v, want %v (blockTime=%ds)",
					coin.Symbol, got, expected, coin.BlockTimeSec)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 3: All Coins — getDeepReorgMaxAge Auto-Scaling
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_DifferentBlockTimes_DeepReorgMaxAge(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			got := getDeepReorgMaxAgeForTest(t, coin)

			// Expected: max(1000, 86400 / blockTime)
			computed := 86400 / coin.BlockTimeSec
			expected := uint64(DeepReorgMaxAge)
			if uint64(computed) > expected {
				expected = uint64(computed)
			}

			if got != expected {
				t.Errorf("[%s] getDeepReorgMaxAge = %d, want %d (blockTime=%ds)",
					coin.Symbol, got, expected, coin.BlockTimeSec)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 4: Concurrent Confirmation Updates Across 5 Processors
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_ConcurrentConfirmationUpdates(t *testing.T) {
	t.Parallel()

	type coinSetup struct {
		symbol      string
		baseHeight  uint64
		blockCount  int
		chainHeight uint64
	}

	setups := []coinSetup{
		{symbol: "DGB", baseHeight: 100000, blockCount: 10, chainHeight: 100000 + uint64(DefaultBlockMaturityConfirmations) + 5},
		{symbol: "BTC", baseHeight: 200000, blockCount: 10, chainHeight: 200000 + uint64(DefaultBlockMaturityConfirmations) + 5},
		{symbol: "LTC", baseHeight: 300000, blockCount: 10, chainHeight: 300000 + uint64(DefaultBlockMaturityConfirmations) + 5},
		{symbol: "DOGE", baseHeight: 400000, blockCount: 10, chainHeight: 400000 + uint64(DefaultBlockMaturityConfirmations) + 5},
		{symbol: "BCH", baseHeight: 500000, blockCount: 10, chainHeight: 500000 + uint64(DefaultBlockMaturityConfirmations) + 5},
	}

	var wg sync.WaitGroup
	errs := make([]error, len(setups))
	stores := make([]*errInjectBlockStore, len(setups))

	for i, s := range setups {
		i, s := i, s
		store := newErrInjectBlockStore()
		rpc := newErrInjectDaemonRPC()
		stores[i] = store

		tipHash := makeChainTip(s.chainHeight)
		rpc.setChainTip(s.chainHeight, tipHash)

		for j := 0; j < s.blockCount; j++ {
			h := s.baseHeight + uint64(j)
			block := makePendingBlock(s.symbol, h)
			store.addPendingBlock(block)
			rpc.setBlockHash(h, makeBlockHash(s.symbol, h))
		}

		proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = proc.updateBlockConfirmations(context.Background())
		}()
	}

	wg.Wait()

	for i, s := range setups {
		if errs[i] != nil {
			t.Errorf("[%s] unexpected error: %v", s.symbol, errs[i])
			continue
		}

		store := stores[i]
		store.mu.Lock()
		updateCount := len(store.statusUpdates)
		store.mu.Unlock()

		// Each block that has enough confirmations should get a status update.
		// All 10 blocks have confirmations ranging from 5 to 14+maturity, which
		// are above 0. The ones at the higher end are at/above maturity. At a
		// minimum, all 10 blocks should get a pending status update with progress.
		if updateCount != s.blockCount {
			t.Errorf("[%s] expected %d status updates, got %d", s.symbol, s.blockCount, updateCount)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 5: One Processor Fails, Others Succeed
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_OneFailsOthersSucceed(t *testing.T) {
	t.Parallel()

	type procResult struct {
		symbol string
		err    error
		store  *errInjectBlockStore
	}

	symbols := []string{"DGB", "BTC", "LTC"}
	results := make([]procResult, len(symbols))
	var wg sync.WaitGroup

	for i, sym := range symbols {
		i, sym := i, sym
		store := newErrInjectBlockStore()
		rpc := newErrInjectDaemonRPC()

		blockHeight := uint64(100000 + i*100000)
		chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
		tipHash := makeChainTip(chainHeight)
		rpc.setChainTip(chainHeight, tipHash)

		block := makePendingBlock(sym, blockHeight)
		store.addPendingBlock(block)
		rpc.setBlockHash(blockHeight, makeBlockHash(sym, blockHeight))

		// Inject error on the second processor (BTC).
		if i == 1 {
			rpc.errGetBlockchainInfo = errors.New("injected: daemon unreachable")
		}

		proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

		results[i] = procResult{symbol: sym, store: store}

		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].err = proc.updateBlockConfirmations(context.Background())
		}()
	}

	wg.Wait()

	// BTC (index 1) should fail.
	if results[1].err == nil {
		t.Error("[BTC] expected error from injected GetBlockchainInfo failure, got nil")
	}

	// DGB (index 0) and LTC (index 2) should succeed.
	for _, idx := range []int{0, 2} {
		r := results[idx]
		if r.err != nil {
			t.Errorf("[%s] unexpected error: %v", r.symbol, r.err)
			continue
		}
		if r.store.statusUpdateCount() == 0 {
			t.Errorf("[%s] expected status updates on successful processor, got 0", r.symbol)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 6: IBD Skips All Coins
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_IBDSkipsAllCoins(t *testing.T) {
	t.Parallel()

	symbols := []string{"DGB", "BTC", "LTC"}

	for _, sym := range symbols {
		sym := sym
		t.Run(sym, func(t *testing.T) {
			t.Parallel()

			store := newErrInjectBlockStore()
			rpc := newErrInjectDaemonRPC()

			blockHeight := uint64(500000)
			chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
			tipHash := makeChainTip(chainHeight)

			store.addPendingBlock(makePendingBlock(sym, blockHeight))
			rpc.setBlockHash(blockHeight, makeBlockHash(sym, blockHeight))

			// Force IBD = true via custom blockchainInfoFunc.
			rpc.blockchainInfoFunc = func(_ context.Context, _ int64) (*daemon.BlockchainInfo, error) {
				return &daemon.BlockchainInfo{
					BestBlockHash:        tipHash,
					Blocks:               chainHeight,
					Headers:              chainHeight + 1000,
					InitialBlockDownload: true,
				}, nil
			}

			proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", sym, err)
			}

			// No blocks should have been updated because IBD was active.
			if store.statusUpdateCount() != 0 {
				t.Errorf("[%s] expected 0 status updates during IBD, got %d",
					sym, store.statusUpdateCount())
			}
			if store.orphanUpdateCount() != 0 {
				t.Errorf("[%s] expected 0 orphan updates during IBD, got %d",
					sym, store.orphanUpdateCount())
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 7: Deep Reorg Detection on Confirmed Blocks
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_DeepReorgDetection(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// BTC: 600s block time.
	// Insert 3 confirmed blocks at heights within deep reorg verification range.
	chainHeight := uint64(700000)
	tipHash := makeChainTip(chainHeight)
	rpc.setChainTip(chainHeight, tipHash)

	confirmedHeights := []uint64{699900, 699950, 699990}
	for _, h := range confirmedHeights {
		block := makeConfirmedBlock("BTC", h)
		store.addConfirmedBlock(block)
		// Daemon reports a DIFFERENT hash at these heights (deep reorg occurred).
		rpc.setBlockHash(h, fmt.Sprintf("%064d", h+9999999))
	}

	proc := newMulticoinProcessor(store, rpc, DefaultBlockMaturityConfirmations, 600)
	err := proc.verifyConfirmedBlocks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All 3 blocks should be marked orphaned due to hash mismatch.
	for _, h := range confirmedHeights {
		if !store.hasStatusUpdateFor(h, StatusOrphaned) {
			t.Errorf("confirmed block at height %d should have been orphaned by deep reorg detection", h)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 8: Stale Pending Block Sweep
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_StalePendingBlockSweep(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	chainHeight := uint64(800000)
	tipHash := makeChainTip(chainHeight)
	rpc.setChainTip(chainHeight, tipHash)

	now := time.Now()

	// 3 fresh blocks (created recently).
	for i := 0; i < 3; i++ {
		h := uint64(799997 + i)
		block := makePendingBlock("DGB", h)
		block.Created = now.Add(-1 * time.Hour) // 1 hour ago — not stale
		store.addPendingBlock(block)
		rpc.setBlockHash(h, makeBlockHash("DGB", h))
	}

	// 2 stale blocks (created > 24 hours ago).
	for i := 0; i < 2; i++ {
		h := uint64(799000 + i)
		block := makePendingBlock("DGB", h)
		block.Created = now.Add(-25 * time.Hour) // 25 hours ago — stale
		store.addPendingBlock(block)
		rpc.setBlockHash(h, makeBlockHash("DGB", h))
	}

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// checkStalePendingBlocks only logs warnings — it does not orphan blocks.
	// The function emits STALE BLOCKS warnings but does not change block status.
	// Let's verify it doesn't panic and processes correctly.
	proc.checkStalePendingBlocks(context.Background())

	// The stale block check is a warning mechanism, not an orphaning mechanism.
	// It reads pending blocks and logs. Verify no status updates were made by
	// checkStalePendingBlocks itself (it only logs, doesn't mutate status).
	if store.statusUpdateCount() != 0 {
		t.Errorf("checkStalePendingBlocks should not make status updates, got %d",
			store.statusUpdateCount())
	}

	// Verify all 5 blocks are still pending (stale check is advisory only).
	store.mu.Lock()
	pendingCount := 0
	for _, b := range store.pendingBlocks {
		if b.Status == StatusPending {
			pendingCount++
		}
	}
	store.mu.Unlock()

	if pendingCount != 5 {
		t.Errorf("expected 5 pending blocks after stale check (advisory only), got %d", pendingCount)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 9: Consecutive Failure Tracking
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_ConsecutiveFailureTracking(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Insert a pending block so updateBlockConfirmations actually tries RPC.
	store.addPendingBlock(makePendingBlock("BTC", 600000))

	// Inject permanent GetBlockchainInfo failure.
	rpc.errGetBlockchainInfo = errors.New("injected: daemon permanently down")

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run 6 processCycles. updateBlockConfirmations will fail each time,
	// causing cycleFailed=true, which increments consecutiveFailedCycles.
	for i := 0; i < 6; i++ {
		proc.processCycle(context.Background())
	}

	if proc.consecutiveFailedCycles != 6 {
		t.Errorf("expected consecutiveFailedCycles=6 after 6 failures, got %d",
			proc.consecutiveFailedCycles)
	}

	// The threshold is 5. After 5+ failures the critical warning should have fired.
	// We verify the counter is at least at the threshold.
	if proc.consecutiveFailedCycles < ConsecutiveFailureThreshold {
		t.Errorf("consecutiveFailedCycles (%d) should be >= threshold (%d)",
			proc.consecutiveFailedCycles, ConsecutiveFailureThreshold)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 10: Failure Recovery Resets Counter
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_FailureRecoveryResets(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	chainHeight := uint64(700000)
	tipHash := makeChainTip(chainHeight)
	rpc.setChainTip(chainHeight, tipHash)

	// Insert a pending block so updateBlockConfirmations exercises the RPC path.
	block := makePendingBlock("BTC", 699900)
	store.addPendingBlock(block)
	rpc.setBlockHash(699900, makeBlockHash("BTC", 699900))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Inject 4 consecutive failures via countdown.
	rpc.failBlockchainInfoN = 4

	for i := 0; i < 4; i++ {
		proc.processCycle(context.Background())
	}

	if proc.consecutiveFailedCycles != 4 {
		t.Fatalf("expected 4 consecutive failures, got %d", proc.consecutiveFailedCycles)
	}

	// Next cycle should succeed (countdown exhausted, no static error).
	proc.processCycle(context.Background())

	if proc.consecutiveFailedCycles != 0 {
		t.Errorf("expected consecutiveFailedCycles to reset to 0 after recovery, got %d",
			proc.consecutiveFailedCycles)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 11: Stability Window Checks on Confirmed Blocks
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_StabilityWindowChecks(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(600000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 20
	tipHash := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tipHash)

	block := makePendingBlock("BTC", blockHeight)
	store.addPendingBlock(block)
	rpc.setBlockHash(blockHeight, makeBlockHash("BTC", blockHeight))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run StabilityWindowChecks cycles. Each cycle, the block is at maturity with
	// matching hash and stable tip, so the stability counter should increment.
	for i := 1; i <= StabilityWindowChecks; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}

		// Check stability updates.
		store.mu.Lock()
		var latestStability int
		for _, su := range store.stabilityUpdates {
			if su.Height == blockHeight {
				latestStability = su.StabilityCount
			}
		}
		store.mu.Unlock()

		if latestStability != i {
			t.Errorf("after cycle %d: expected stability count %d, got %d", i, i, latestStability)
		}
	}

	// After StabilityWindowChecks, the block should be confirmed.
	if !store.hasStatusUpdateFor(blockHeight, StatusConfirmed) {
		t.Error("block should be confirmed after stability window passed")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 12: Orphan Vote Accumulation
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_OrphanVoteAccumulation(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	blockHeight := uint64(650000)
	chainHeight := blockHeight + uint64(DefaultBlockMaturityConfirmations) + 10
	tipHash := makeChainTip(chainHeight)

	rpc.setChainTip(chainHeight, tipHash)

	block := makePendingBlock("DGB", blockHeight)
	store.addPendingBlock(block)

	// Daemon reports a DIFFERENT hash at the block's height.
	rpc.setBlockHash(blockHeight, fmt.Sprintf("%064d", 999999))

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Run OrphanMismatchThreshold cycles. Each cycle the hash mismatches,
	// incrementing the orphan vote counter.
	for i := 1; i <= OrphanMismatchThreshold; i++ {
		err := proc.updateBlockConfirmations(context.Background())
		if err != nil {
			t.Fatalf("cycle %d: unexpected error: %v", i, err)
		}

		if i < OrphanMismatchThreshold {
			// Not yet at threshold — block should still be pending.
			if store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
				t.Fatalf("block orphaned after only %d mismatches (threshold=%d)",
					i, OrphanMismatchThreshold)
			}

			// Verify orphan mismatch count was updated.
			store.mu.Lock()
			var latestMismatch int
			for _, ou := range store.orphanUpdates {
				if ou.Height == blockHeight {
					latestMismatch = ou.MismatchCount
				}
			}
			store.mu.Unlock()

			if latestMismatch != i {
				t.Errorf("after cycle %d: expected mismatch count %d, got %d", i, i, latestMismatch)
			}
		}
	}

	// After OrphanMismatchThreshold mismatches, the block should be orphaned.
	if !store.hasStatusUpdateFor(blockHeight, StatusOrphaned) {
		t.Error("block should be orphaned after reaching OrphanMismatchThreshold consecutive mismatches")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TEST 13: Simulated Extended Operation
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultiCoin_SimulatedExtendedOperation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping extended operation simulation in short mode")
	}
	t.Parallel()

	type procState struct {
		symbol     string
		proc       *Processor
		store      *errInjectBlockStore
		cycleCount atomic.Int64
	}

	coinSymbols := []string{"DGB", "BTC", "LTC"}
	states := make([]*procState, len(coinSymbols))

	for i, sym := range coinSymbols {
		store := newErrInjectBlockStore()
		rpc := newErrInjectDaemonRPC()

		baseHeight := uint64(100000*(i+1)) + 1000
		chainHeight := baseHeight + uint64(DefaultBlockMaturityConfirmations) + 50
		tipHash := makeChainTip(chainHeight)
		rpc.setChainTip(chainHeight, tipHash)

		// Add 5 pending blocks per coin.
		for j := 0; j < 5; j++ {
			h := baseHeight + uint64(j)
			block := makePendingBlock(sym, h)
			store.addPendingBlock(block)
			rpc.setBlockHash(h, makeBlockHash(sym, h))
		}

		// Every 5th GetBlockchainInfo call returns an error.
		callCount := &atomic.Int64{}
		rpc.blockchainInfoFunc = func(_ context.Context, callIdx int64) (*daemon.BlockchainInfo, error) {
			n := callCount.Add(1)
			if n%5 == 0 {
				return nil, fmt.Errorf("injected intermittent failure #%d", n)
			}
			return &daemon.BlockchainInfo{
				BestBlockHash: tipHash,
				Blocks:        chainHeight,
			}, nil
		}

		proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
		states[i] = &procState{
			symbol: sym,
			proc:   proc,
			store:  store,
		}
	}

	// Run all 3 processors concurrently for ~10 seconds, each cycling every 200ms.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range states {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.proc.processCycle(context.Background())
					s.cycleCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	// Verify each processor ran a reasonable number of cycles (at least 10).
	for _, s := range states {
		cycles := s.cycleCount.Load()
		if cycles < 10 {
			t.Errorf("[%s] expected at least 10 cycles in 10s at 200ms interval, got %d",
				s.symbol, cycles)
		}

		// Verify some blocks were processed (at least one status update).
		if s.store.statusUpdateCount() == 0 {
			t.Errorf("[%s] expected at least some status updates after %d cycles, got 0",
				s.symbol, cycles)
		}

		t.Logf("[%s] completed %d cycles, %d status updates, %d orphan updates",
			s.symbol, cycles, s.store.statusUpdateCount(), s.store.orphanUpdateCount())
	}
}
