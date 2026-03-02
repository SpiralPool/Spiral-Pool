// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Tests that no valid solved block is silently lost due to RPC failures
// (GetBlockHash, GetBlockchainInfo timeouts, truncated responses) in SOLO mode.
//
// These tests use the shared mocks from solo_mocks_test.go (mockBlockStore,
// mockDaemonRPC, testCoinConfig, allTestCoins, etc.) and exercise
// updateBlockConfirmations directly since we are in the same package.
package payments

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// newRPCFailureProcessor creates a Processor wired to the shared mock types
// from solo_mocks_test.go, suitable for RPC failure testing.
func newRPCFailureProcessor(db *mockBlockStore, rpc *mockDaemonRPC) *Processor {
	cfg := &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: DefaultBlockMaturityConfirmations,
	}
	logger := zap.NewNop()
	return &Processor{
		cfg:          cfg,
		poolCfg:      &config.PoolConfig{},
		logger:       logger.Sugar(),
		db:           db,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 1: GetBlockchainInfo fails -> cycle aborted, blocks stay pending
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_GetBlockchainInfoFails tests that when
// GetBlockchainInfo returns an error (connection refused, timeout, etc.),
// the entire confirmation cycle aborts and no pending block is orphaned.
//
// Risk vector:  GetBlockchainInfo fails -> cycle aborted, blocks stay pending (NOT orphaned)
// Coins:        All 13 supported coins (SHA-256d + Scrypt)
// Block interval: Varies per coin (15s - 600s)
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_GetBlockchainInfoFails(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin // capture range variable
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()
			blockHeight := uint64(100000)
			db.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("connection refused: daemon %s unreachable", coin.Symbol)

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s] Expected error from failed GetBlockchainInfo, got nil", coin.Symbol)
			}

			// CRITICAL: No block status should have been updated at all.
			// The cycle must abort before processing any blocks.
			updates := db.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status == StatusOrphaned {
					t.Fatalf("[%s] BLOCK LOSS: pending block at height %d falsely orphaned due to GetBlockchainInfo failure",
						coin.Symbol, blockHeight)
				}
			}

			// Orphan counter must not have been touched either.
			orphanUpdates := db.getOrphanUpdates()
			if len(orphanUpdates) > 0 {
				t.Errorf("[%s] Expected zero orphan counter updates when GetBlockchainInfo fails, got %d",
					coin.Symbol, len(orphanUpdates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 2: GetBlockHash timeout for specific block -> block skipped
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_GetBlockHashTimeout tests that when
// GetBlockHash times out for a specific pending block, that block is
// skipped (stays pending) and is NOT orphaned. Other blocks continue
// processing normally.
//
// Risk vector:  GetBlockHash timeout for specific block -> block skipped, stays pending
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_GetBlockHashTimeout(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			blockHeight := uint64(200000)
			block := makePendingBlock(coin.Symbol, blockHeight)
			db.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			chainHeight := blockHeight + 50
			tip := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, tip)

			// GetBlockHash will fail for all calls (simulates timeout).
			rpc.errGetBlockHash = fmt.Errorf("timeout: GetBlockHash for %s at height %d", coin.Symbol, blockHeight)

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			// The method should NOT return an error; it logs a warning and continues/skips.
			if err != nil {
				t.Fatalf("[%s] Unexpected error: %v", coin.Symbol, err)
			}

			// Block must NOT be orphaned.
			if db.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: pending block at height %d orphaned due to GetBlockHash timeout",
					coin.Symbol, blockHeight)
			}

			// Block should still be pending -- if any status update was recorded,
			// it must be "pending" (progress update), not orphaned or confirmed.
			updates := db.getStatusUpdates()
			for _, u := range updates {
				if u.Height == blockHeight && u.Status != StatusPending {
					t.Errorf("[%s] Expected block to remain pending after GetBlockHash timeout, got status %q",
						coin.Symbol, u.Status)
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 3: GetBlockchainInfo returns truncated/nil -> graceful handling
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_GetBlockchainInfoNil tests that when the
// daemon returns a nil BlockchainInfo (truncated response, parse error),
// the cycle aborts gracefully and no pending blocks are falsely orphaned.
//
// Risk vector:  GetBlockchainInfo returns truncated/nil -> graceful handling, no false orphans
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_GetBlockchainInfoNil(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			blockHeight := uint64(300000)
			db.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			// Override the blockchainInfoFunc to return nil with an error,
			// simulating a truncated/corrupt RPC response.
			rpc.blockchainInfoFunc = func(ctx context.Context) (*daemon.BlockchainInfo, error) {
				return nil, fmt.Errorf("truncated response from %s daemon: unexpected EOF", coin.Symbol)
			}

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s] Expected error from truncated GetBlockchainInfo, got nil", coin.Symbol)
			}

			// No blocks should have been processed or orphaned.
			updates := db.getStatusUpdates()
			for _, u := range updates {
				if u.Status == StatusOrphaned {
					t.Fatalf("[%s] BLOCK LOSS: block at height %d falsely orphaned after truncated RPC response",
						coin.Symbol, u.Height)
				}
			}

			orphanUpdates := db.getOrphanUpdates()
			if len(orphanUpdates) > 0 {
				t.Errorf("[%s] No orphan counter updates expected after truncated response, got %d",
					coin.Symbol, len(orphanUpdates))
			}

			stabilityUpdates := db.getStabilityUpdates()
			if len(stabilityUpdates) > 0 {
				t.Errorf("[%s] No stability updates expected after truncated response, got %d",
					coin.Symbol, len(stabilityUpdates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 4: All RPC calls fail -> no status changes, blocks remain safe
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_AllRPCFail tests total RPC outage: both
// GetBlockchainInfo and GetBlockHash are broken. Pending blocks must
// remain in their current state with zero status mutations.
//
// Risk vector:  All RPC calls fail -> no status changes, blocks remain safe in pending
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_AllRPCFail(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			// Add multiple pending blocks at different heights.
			heights := []uint64{400000, 400010, 400020}
			for _, h := range heights {
				db.addPendingBlock(makePendingBlock(coin.Symbol, h))
			}

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("all RPCs down: %s daemon offline", coin.Symbol)
			rpc.errGetBlockHash = fmt.Errorf("all RPCs down: %s daemon offline", coin.Symbol)

			proc := newRPCFailureProcessor(db, rpc)

			// Run multiple cycles -- none should change any block status.
			for cycle := 0; cycle < 5; cycle++ {
				_ = proc.updateBlockConfirmations(context.Background())
			}

			// CRITICAL: zero status updates, zero orphan updates, zero stability updates.
			updates := db.getStatusUpdates()
			if len(updates) > 0 {
				t.Errorf("[%s] Expected zero status updates during total RPC outage, got %d: %+v",
					coin.Symbol, len(updates), updates)
			}

			orphanUpdates := db.getOrphanUpdates()
			if len(orphanUpdates) > 0 {
				t.Errorf("[%s] Expected zero orphan updates during total RPC outage, got %d",
					coin.Symbol, len(orphanUpdates))
			}

			stabilityUpdates := db.getStabilityUpdates()
			if len(stabilityUpdates) > 0 {
				t.Errorf("[%s] Expected zero stability updates during total RPC outage, got %d",
					coin.Symbol, len(stabilityUpdates))
			}

			// Specifically assert no orphaned blocks.
			for _, h := range heights {
				if db.hasStatusUpdate(h, StatusOrphaned) {
					t.Fatalf("[%s] BLOCK LOSS: block at height %d orphaned during total RPC outage",
						coin.Symbol, h)
				}
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 5: Intermittent RPC -- first GetBlockchainInfo succeeds,
//                TOCTOU check fails -> cycle aborted
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_TOCTOUCheckFails tests that when the initial
// GetBlockchainInfo (snapshot) succeeds but the per-block TOCTOU re-check
// of GetBlockchainInfo fails, the cycle aborts immediately and no block is
// orphaned or confirmed.
//
// Risk vector:  Intermittent RPC: first GetBlockchainInfo succeeds, TOCTOU check fails -> cycle aborted
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_TOCTOUCheckFails(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			blockHeight := uint64(500000)
			db.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			chainHeight := blockHeight + 50
			tip := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, tip)

			// First call succeeds (snapshot), second call fails (TOCTOU check).
			// failBlockchainInfoCount = 0 means the static error is not used.
			// We use blockchainInfoFunc to control per-call behavior.
			callCount := 0
			rpc.blockchainInfoFunc = func(ctx context.Context) (*daemon.BlockchainInfo, error) {
				callCount++
				if callCount == 1 {
					// First call: snapshot -- succeeds
					return &daemon.BlockchainInfo{
						Chain:         "regtest",
						Blocks:        chainHeight,
						BestBlockHash: tip,
					}, nil
				}
				// Second call (TOCTOU check): fails
				return nil, fmt.Errorf("intermittent RPC failure on TOCTOU check for %s", coin.Symbol)
			}

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			// updateBlockConfirmations returns nil on TOCTOU abort (logs warning, returns nil).
			if err != nil {
				t.Fatalf("[%s] Expected nil error on TOCTOU abort, got: %v", coin.Symbol, err)
			}

			// No blocks should have been processed.
			updates := db.getStatusUpdates()
			for _, u := range updates {
				if u.Status == StatusOrphaned {
					t.Fatalf("[%s] BLOCK LOSS: block at height %d orphaned when TOCTOU check failed",
						coin.Symbol, u.Height)
				}
				if u.Status == StatusConfirmed {
					t.Fatalf("[%s] Block at height %d falsely confirmed when TOCTOU check failed",
						coin.Symbol, u.Height)
				}
			}

			orphanUpdates := db.getOrphanUpdates()
			if len(orphanUpdates) > 0 {
				t.Errorf("[%s] Expected zero orphan updates on TOCTOU abort, got %d",
					coin.Symbol, len(orphanUpdates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RISK VECTOR 6: GetBlockHash returns wrong hash but under
//                OrphanMismatchThreshold -> delayed orphaning, not immediate
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_WrongHashUnderThreshold tests that when
// GetBlockHash returns a hash that does not match our block (possible
// temporary node desync or minority fork), the block is NOT immediately
// orphaned. The delayed orphaning mechanism requires OrphanMismatchThreshold
// consecutive mismatches before marking orphaned.
//
// Risk vector:  GetBlockHash returns wrong hash but under OrphanMismatchThreshold -> delayed orphaning
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_WrongHashUnderThreshold(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			blockHeight := uint64(600000)
			block := makePendingBlock(coin.Symbol, blockHeight)
			db.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			chainHeight := blockHeight + 50
			tip := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, tip)

			// Return a WRONG hash -- simulates node returning stale/forked data.
			wrongHash := fmt.Sprintf("wrong_%s_%064x", coin.Symbol, blockHeight)
			if len(wrongHash) > 64 {
				wrongHash = wrongHash[:64]
			}
			rpc.setBlockHash(blockHeight, wrongHash)

			proc := newRPCFailureProcessor(db, rpc)

			// Run (OrphanMismatchThreshold - 1) cycles.
			// Block must NOT be orphaned -- threshold not yet reached.
			for cycle := 0; cycle < OrphanMismatchThreshold-1; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Verify the block was NOT orphaned.
			if db.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block at height %d orphaned after only %d mismatches (threshold=%d)",
					coin.Symbol, blockHeight, OrphanMismatchThreshold-1, OrphanMismatchThreshold)
			}

			// Verify orphan mismatch counter was incremented but not at threshold.
			orphanUpdates := db.getOrphanUpdates()
			if len(orphanUpdates) == 0 {
				t.Fatalf("[%s] Expected orphan mismatch counter to be incremented", coin.Symbol)
			}

			// The latest orphan update should have count < OrphanMismatchThreshold.
			lastOrphan := orphanUpdates[len(orphanUpdates)-1]
			if lastOrphan.Height != blockHeight {
				t.Errorf("[%s] Orphan update for wrong height: got %d, want %d",
					coin.Symbol, lastOrphan.Height, blockHeight)
			}
			if lastOrphan.MismatchCount >= OrphanMismatchThreshold {
				t.Errorf("[%s] Mismatch count %d should be below threshold %d after %d cycles",
					coin.Symbol, lastOrphan.MismatchCount, OrphanMismatchThreshold, OrphanMismatchThreshold-1)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ALGORITHM-GROUPED: SHA-256d coins under RPC failure
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_SHA256d_GetBlockchainInfoFails verifies RPC failure
// safety specifically for SHA-256d algorithm coins: BTC, BCH, DGB, BC2,
// NMC, SYS, XMY, FBTC.
//
// Risk vector:  GetBlockchainInfo fails -> cycle aborted
// Coins:        SHA-256d only (BTC, BCH, DGB, BC2, NMC, SYS, XMY, FBTC)
// Block interval: 15s (DGB) to 600s (BTC/BCH)
// Algorithm:    SHA-256d
func TestSOLO_RPCFailure_SHA256d_GetBlockchainInfoFails(t *testing.T) {
	t.Parallel()

	for _, coin := range sha256dCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()
			db.addPendingBlock(makePendingBlock(coin.Symbol, 750000))

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("timeout: %s daemon (sha256d, %ds blocks)", coin.Symbol, coin.BlockTimeSec)

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s/sha256d] Expected error, got nil", coin.Symbol)
			}

			if db.hasStatusUpdate(750000, StatusOrphaned) {
				t.Fatalf("[%s/sha256d] BLOCK LOSS: falsely orphaned during RPC failure", coin.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ALGORITHM-GROUPED: Scrypt coins under RPC failure
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_Scrypt_GetBlockchainInfoFails verifies RPC failure
// safety specifically for Scrypt algorithm coins: LTC, DOGE,
// DGB-SCRYPT, PEP, CAT.
//
// Risk vector:  GetBlockchainInfo fails -> cycle aborted
// Coins:        Scrypt only (LTC, DOGE, DGB-SCRYPT, PEP, CAT)
// Block interval: 15s (DGB-SCRYPT) to 600s (CAT)
// Algorithm:    Scrypt
func TestSOLO_RPCFailure_Scrypt_GetBlockchainInfoFails(t *testing.T) {
	t.Parallel()

	for _, coin := range scryptCoins() {
		coin := coin
		t.Run(coin.Symbol, func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()
			db.addPendingBlock(makePendingBlock(coin.Symbol, 850000))

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("timeout: %s daemon (scrypt, %ds blocks)", coin.Symbol, coin.BlockTimeSec)

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s/scrypt] Expected error, got nil", coin.Symbol)
			}

			if db.hasStatusUpdate(850000, StatusOrphaned) {
				t.Fatalf("[%s/scrypt] BLOCK LOSS: falsely orphaned during RPC failure", coin.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MULTI-BLOCK: GetBlockHash fails for one block, others process normally
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_PartialGetBlockHashFailure tests that when
// GetBlockHash fails for one specific block but succeeds for others, only
// the affected block is skipped. The other blocks continue normal processing
// (progress updates etc.) and are not disrupted.
//
// Risk vector:  GetBlockHash timeout for specific block -> block skipped, stays pending
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_PartialGetBlockHashFailure(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			// Two blocks: one will have GetBlockHash fail, one will succeed.
			heightFail := uint64(700000)
			heightOK := uint64(700010)
			blockFail := makePendingBlock(coin.Symbol, heightFail)
			blockOK := makePendingBlock(coin.Symbol, heightOK)
			db.addPendingBlock(blockFail)
			db.addPendingBlock(blockOK)

			rpc := newMockDaemonRPC()
			chainHeight := heightOK + 50
			tip := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, tip)

			// Set hash for the OK block so it matches and processes normally.
			rpc.setBlockHash(heightOK, blockOK.Hash)

			// Use custom blockHashFunc: fail for heightFail, succeed for heightOK.
			rpc.blockHashFunc = func(ctx context.Context, height uint64) (string, error) {
				if height == heightFail {
					return "", fmt.Errorf("timeout: GetBlockHash for %s at height %d", coin.Symbol, height)
				}
				return blockOK.Hash, nil
			}

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] Unexpected error: %v", coin.Symbol, err)
			}

			// The failed block must NOT be orphaned.
			if db.hasStatusUpdate(heightFail, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block at height %d orphaned when GetBlockHash failed",
					coin.Symbol, heightFail)
			}

			// The OK block should have received a status update (pending with progress).
			if !db.hasStatusUpdate(heightOK, StatusPending) {
				t.Errorf("[%s] Expected status update for successfully processed block at height %d",
					coin.Symbol, heightOK)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MULTI-CYCLE RESILIENCE: Repeated RPC failures across multiple cycles
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_RepeatedFailuresNeverOrphan tests that even
// after many consecutive cycles of complete RPC failure, blocks remain
// safely in pending status and are never orphaned.
//
// Risk vector:  All RPC calls fail -> no status changes, blocks remain safe in pending
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_RepeatedFailuresNeverOrphan(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()
			blockHeight := uint64(800000)
			db.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("daemon %s completely unreachable", coin.Symbol)

			proc := newRPCFailureProcessor(db, rpc)

			// Run far more cycles than the OrphanMismatchThreshold to prove
			// RPC errors never contribute to orphan counting.
			cycleCount := OrphanMismatchThreshold * 10
			for cycle := 0; cycle < cycleCount; cycle++ {
				_ = proc.updateBlockConfirmations(context.Background())
			}

			// Absolutely no status changes allowed.
			updates := db.getStatusUpdates()
			if len(updates) > 0 {
				t.Errorf("[%s] Expected zero status updates after %d failed cycles, got %d",
					coin.Symbol, cycleCount, len(updates))
			}

			if db.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block at height %d orphaned after %d cycles of RPC failure",
					coin.Symbol, blockHeight, cycleCount)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// TOCTOU + RPC FAILURE: Snapshot succeeds, per-block tip differs
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_TOCTOUTipChangeAbortsCycle tests the TOCTOU
// protection path where the initial snapshot GetBlockchainInfo succeeds but
// the per-block TOCTOU re-verification returns a DIFFERENT tip hash.
// The cycle must abort without processing any block.
//
// Risk vector:  Intermittent RPC: first GetBlockchainInfo succeeds, TOCTOU check fails -> cycle aborted
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_TOCTOUTipChangeAbortsCycle(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			blockHeight := uint64(900000)
			db.addPendingBlock(makePendingBlock(coin.Symbol, blockHeight))

			rpc := newMockDaemonRPC()
			chainHeight := blockHeight + 200
			originalTip := makeChainTip(chainHeight)
			newTip := makeChainTip(chainHeight + 1) // Different tip on second call

			// tipMutator returns original tip on first call, different tip on second.
			// Use SAME height with different tip to trigger TOCTOU abort.
			// Height increase is treated as chain advancement, not a reorg.
			rpc.tipMutator = func(callIndex int64) *daemon.BlockchainInfo {
				if callIndex == 1 {
					return &daemon.BlockchainInfo{
						Chain:         "regtest",
						Blocks:        chainHeight,
						BestBlockHash: originalTip,
					}
				}
				return &daemon.BlockchainInfo{
					Chain:         "regtest",
					Blocks:        chainHeight,
					BestBlockHash: newTip,
				}
			}

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] Expected nil on TOCTOU abort, got: %v", coin.Symbol, err)
			}

			// Zero DB mutations expected.
			updates := db.getStatusUpdates()
			if len(updates) > 0 {
				t.Errorf("[%s] Expected zero status updates on TOCTOU abort, got %d",
					coin.Symbol, len(updates))
			}

			if db.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: block orphaned during TOCTOU tip change", coin.Symbol)
			}

			orphanUpdates := db.getOrphanUpdates()
			if len(orphanUpdates) > 0 {
				t.Errorf("[%s] Expected zero orphan updates on TOCTOU abort, got %d",
					coin.Symbol, len(orphanUpdates))
			}

			stabilityUpdates := db.getStabilityUpdates()
			if len(stabilityUpdates) > 0 {
				t.Errorf("[%s] Expected zero stability updates on TOCTOU abort, got %d",
					coin.Symbol, len(stabilityUpdates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// WRONG HASH RECOVERY: Hash mismatch under threshold, then recovery
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AllCoins_WrongHashRecovery tests that a block with
// hash mismatches below OrphanMismatchThreshold can recover when the
// chain hash starts matching again. The mismatch counter must reset to zero
// upon a successful match, preventing false orphaning from temporary forks.
//
// Risk vector:  GetBlockHash returns wrong hash but under OrphanMismatchThreshold -> delayed orphaning
// Coins:        All 13 supported coins
// Block interval: Varies per coin
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_AllCoins_WrongHashRecovery(t *testing.T) {
	t.Parallel()

	for _, coin := range allTestCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			blockHeight := uint64(950000)
			block := makePendingBlock(coin.Symbol, blockHeight)
			db.addPendingBlock(block)

			rpc := newMockDaemonRPC()
			chainHeight := blockHeight + 50
			tip := makeChainTip(chainHeight)
			rpc.setChainTip(chainHeight, tip)

			// Phase 1: wrong hash for (OrphanMismatchThreshold - 1) cycles.
			wrongHash := fmt.Sprintf("wrong_%s_%060d", coin.Symbol, blockHeight)
			if len(wrongHash) > 64 {
				wrongHash = wrongHash[:64]
			}
			rpc.setBlockHash(blockHeight, wrongHash)

			proc := newRPCFailureProcessor(db, rpc)

			for cycle := 0; cycle < OrphanMismatchThreshold-1; cycle++ {
				err := proc.updateBlockConfirmations(context.Background())
				if err != nil {
					t.Fatalf("[%s] Phase 1 cycle %d: unexpected error: %v", coin.Symbol, cycle, err)
				}
			}

			// Block must NOT be orphaned yet.
			if db.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: orphaned before threshold", coin.Symbol)
			}

			// Phase 2: fix the hash -- chain recovered.
			rpc.setBlockHash(blockHeight, block.Hash)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] Recovery cycle: unexpected error: %v", coin.Symbol, err)
			}

			// The orphan counter should have been reset to 0.
			orphanUpdates := db.getOrphanUpdates()
			foundReset := false
			for _, u := range orphanUpdates {
				if u.Height == blockHeight && u.MismatchCount == 0 {
					foundReset = true
				}
			}
			if !foundReset {
				t.Errorf("[%s] Expected orphan mismatch counter to be reset to 0 after hash match recovery",
					coin.Symbol)
			}

			// Block still must not be orphaned.
			if db.hasStatusUpdate(blockHeight, StatusOrphaned) {
				t.Fatalf("[%s] BLOCK LOSS: orphaned after hash recovery", coin.Symbol)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUX COINS: Merge-mined auxiliary coins under RPC failure
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_AuxCoins_GetBlockchainInfoFails verifies that
// auxiliary (merge-mined) coins are equally protected against RPC failures.
// AuxPoW blocks are particularly valuable because they share work with
// the parent chain and would be expensive to re-find.
//
// Risk vector:  GetBlockchainInfo fails -> cycle aborted, blocks stay pending
// Coins:        Auxiliary only (NMC, SYS, XMY, FBTC, DOGE, PEP)
// Block interval: 30s (FBTC) to 600s (NMC)
// Algorithm:    SHA-256d (NMC, SYS, XMY, FBTC) and Scrypt (DOGE, PEP)
func TestSOLO_RPCFailure_AuxCoins_GetBlockchainInfoFails(t *testing.T) {
	t.Parallel()

	for _, coin := range auxCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_parent_%s_%s_%ds", coin.Symbol, coin.ParentChain, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()
			db.addPendingBlock(makePendingBlock(coin.Symbol, 1000000))

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("aux daemon %s (parent: %s) unreachable",
				coin.Symbol, coin.ParentChain)

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err == nil {
				t.Fatalf("[%s/aux] Expected error, got nil", coin.Symbol)
			}

			if db.hasStatusUpdate(1000000, StatusOrphaned) {
				t.Fatalf("[%s/aux] BLOCK LOSS: merge-mined block orphaned during RPC failure (parent: %s)",
					coin.Symbol, coin.ParentChain)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// PARENT COINS: Parent chain coins under RPC failure
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_ParentCoins_AllRPCFail verifies that parent chain
// coins (BTC, LTC) used for merge mining are protected during total RPC outage.
// Loss of a parent chain block is catastrophic since it also implies lost
// work on all auxiliary chains.
//
// Risk vector:  All RPC calls fail -> no status changes, blocks remain safe in pending
// Coins:        Parent chain only (BTC, LTC)
// Block interval: 150s (LTC) to 600s (BTC)
// Algorithm:    SHA-256d (BTC) and Scrypt (LTC)
func TestSOLO_RPCFailure_ParentCoins_AllRPCFail(t *testing.T) {
	t.Parallel()

	for _, coin := range parentCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			heights := []uint64{1100000, 1100001, 1100002}
			for _, h := range heights {
				db.addPendingBlock(makePendingBlock(coin.Symbol, h))
			}

			rpc := newMockDaemonRPC()
			rpc.errGetBlockchainInfo = fmt.Errorf("total outage: parent chain %s daemon", coin.Symbol)
			rpc.errGetBlockHash = fmt.Errorf("total outage: parent chain %s daemon", coin.Symbol)

			proc := newRPCFailureProcessor(db, rpc)

			for cycle := 0; cycle < OrphanMismatchThreshold*5; cycle++ {
				_ = proc.updateBlockConfirmations(context.Background())
			}

			for _, h := range heights {
				if db.hasStatusUpdate(h, StatusOrphaned) {
					t.Fatalf("[%s/parent] BLOCK LOSS: parent chain block at height %d orphaned during total outage",
						coin.Symbol, h)
				}
			}

			updates := db.getStatusUpdates()
			if len(updates) > 0 {
				t.Errorf("[%s/parent] Expected zero status updates during total outage, got %d",
					coin.Symbol, len(updates))
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// RAPID BLOCK COINS: Fast block interval coins are more susceptible to
//                    RPC timing issues
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_RPCFailure_FastBlockCoins_TOCTOUResilience tests coins with
// very fast block intervals (15-60s) which are most susceptible to
// chain tip changes during processing. Verifies that TOCTOU protection
// correctly aborts the cycle for these rapid-block chains.
//
// Risk vector:  Intermittent RPC: first GetBlockchainInfo succeeds, TOCTOU check fails
// Coins:        Fast-block coins (DGB 15s, DGB-SCRYPT 15s, FBTC 30s)
// Block interval: 15-30s
// Algorithm:    SHA-256d and Scrypt
func TestSOLO_RPCFailure_FastBlockCoins_TOCTOUResilience(t *testing.T) {
	t.Parallel()

	// Filter for coins with block time <= 30 seconds.
	var fastCoins []testCoinConfig
	for _, c := range allTestCoins {
		if c.BlockTimeSec <= 30 {
			fastCoins = append(fastCoins, c)
		}
	}

	if len(fastCoins) == 0 {
		t.Fatal("Expected at least one fast-block coin in allTestCoins")
	}

	for _, coin := range fastCoins {
		coin := coin
		t.Run(fmt.Sprintf("%s_%s_%ds", coin.Symbol, coin.Algorithm, coin.BlockTimeSec), func(t *testing.T) {
			t.Parallel()

			db := newMockBlockStore()

			// Multiple blocks for fast-block chains.
			for h := uint64(1200000); h < 1200005; h++ {
				db.addPendingBlock(makePendingBlock(coin.Symbol, h))
			}

			rpc := newMockDaemonRPC()
			chainHeight := uint64(1200100)
			originalTip := makeChainTip(chainHeight)
			newTip := makeChainTip(chainHeight + 1)

			// Tip changes after the snapshot call.
			rpc.tipMutator = func(callIndex int64) *daemon.BlockchainInfo {
				if callIndex == 1 {
					return &daemon.BlockchainInfo{
						Chain:         "regtest",
						Blocks:        chainHeight,
						BestBlockHash: originalTip,
					}
				}
				// Subsequent calls see same height but different tip (reorg).
				return &daemon.BlockchainInfo{
					Chain:         "regtest",
					Blocks:        chainHeight,
					BestBlockHash: newTip,
				}
			}

			proc := newRPCFailureProcessor(db, rpc)

			err := proc.updateBlockConfirmations(context.Background())
			if err != nil {
				t.Fatalf("[%s] Expected nil on TOCTOU abort, got: %v", coin.Symbol, err)
			}

			// No block should be orphaned.
			for h := uint64(1200000); h < 1200005; h++ {
				if db.hasStatusUpdate(h, StatusOrphaned) {
					t.Fatalf("[%s] BLOCK LOSS: fast-block chain block at height %d orphaned during TOCTOU",
						coin.Symbol, h)
				}
			}

			// Zero DB mutations.
			updates := db.getStatusUpdates()
			if len(updates) > 0 {
				t.Errorf("[%s] Expected zero status updates on TOCTOU abort for fast-block chain, got %d",
					coin.Symbol, len(updates))
			}
		})
	}
}
