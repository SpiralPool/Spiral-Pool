// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Vector V2: Mock fidelity tests for SOLO mode payment processor.
//
// These tests target the fidelity gap between test mocks and production
// PostgreSQL. The standard errInjectBlockStore returns the SAME pointers that
// it stores internally, so in-memory mutations by the processor (e.g.,
// block.OrphanMismatchCount++ or block.StabilityCheckCount++) persist across
// cycles even when the corresponding DB write fails. In production, PostgreSQL
// returns fresh structs from each query — if the DB write fails, the next
// cycle reads the old DB values and the counter effectively resets.
//
// The copyOnReadBlockStore defined here wraps errInjectBlockStore to return
// DEEP COPIES from GetPendingBlocks/GetConfirmedBlocks, matching the
// production PostgreSQL behavior.
package payments

import (
	"context"
	"fmt"
	"testing"

	"github.com/spiralpool/stratum/internal/database"
)

// ═══════════════════════════════════════════════════════════════════════════════
// copyOnReadBlockStore: production-fidelity mock
// ═══════════════════════════════════════════════════════════════════════════════

// copyOnReadBlockStore wraps errInjectBlockStore to match PostgreSQL behavior:
// each GetPendingBlocks/GetConfirmedBlocks call returns DEEP COPIES of blocks,
// not pointers to the stored originals. This means in-memory mutations by the
// processor do NOT persist across cycles unless the DB write succeeds.
type copyOnReadBlockStore struct {
	*errInjectBlockStore
}

func newCopyOnReadBlockStore() *copyOnReadBlockStore {
	return &copyOnReadBlockStore{errInjectBlockStore: newErrInjectBlockStore()}
}

func (m *copyOnReadBlockStore) GetPendingBlocks(ctx context.Context) ([]*database.Block, error) {
	m.errInjectBlockStore.mu.Lock()
	defer m.errInjectBlockStore.mu.Unlock()

	// Check countdown/error injection
	if m.errInjectBlockStore.failGetPendingN > 0 {
		m.errInjectBlockStore.failGetPendingN--
		return nil, fmt.Errorf("injected GetPendingBlocks failure")
	}
	if m.errInjectBlockStore.errGetPending != nil {
		return nil, m.errInjectBlockStore.errGetPending
	}

	// Return DEEP COPIES (matching PostgreSQL behavior)
	var result []*database.Block
	for _, b := range m.errInjectBlockStore.pendingBlocks {
		if b.Status == StatusPending {
			cp := *b // Value copy
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (m *copyOnReadBlockStore) GetConfirmedBlocks(ctx context.Context) ([]*database.Block, error) {
	m.errInjectBlockStore.mu.Lock()
	defer m.errInjectBlockStore.mu.Unlock()

	m.errInjectBlockStore.getConfirmedCalled = true
	if m.errInjectBlockStore.failGetConfirmedN > 0 {
		m.errInjectBlockStore.failGetConfirmedN--
		return nil, fmt.Errorf("injected GetConfirmedBlocks failure")
	}
	if m.errInjectBlockStore.errGetConfirmed != nil {
		return nil, m.errInjectBlockStore.errGetConfirmed
	}

	var result []*database.Block
	for _, b := range m.errInjectBlockStore.confirmedBlocks {
		if b.Status == StatusConfirmed {
			cp := *b
			result = append(result, &cp)
		}
	}
	return result, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// V2 TESTS: Mock fidelity vs production PostgreSQL behavior
// ═══════════════════════════════════════════════════════════════════════════════

// TestSOLO_Vector_V2_OrphanCounterResetOnDBFailure verifies that when the
// orphan count DB write fails, the counter does NOT accumulate across cycles
// in production (PostgreSQL returns fresh structs with the old DB value).
//
// With the pointer-sharing mock, the processor's in-memory increment survives
// across cycles, so after 3 cycles the block would be orphaned. With the
// copy-on-read mock (matching production), the counter resets to 0 each cycle
// because the DB write never succeeded and the next read returns a fresh copy.
func TestSOLO_Vector_V2_OrphanCounterResetOnDBFailure(t *testing.T) {
	t.Parallel()

	store := newCopyOnReadBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Block at height 800, WRONG hash (will mismatch every cycle).
	block := makePendingBlock("BTC", 800)
	block.Hash = fmt.Sprintf("aaaa%060d", 800)
	store.addPendingBlock(block)

	// Chain has a different hash at height 800.
	rpc.setBlockHash(800, makeBlockHash("BTC", 800))
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	// Inject error: ALL orphan count DB writes fail.
	store.errInjectBlockStore.errUpdateOrphan = fmt.Errorf("injected orphan update failure")

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run OrphanMismatchThreshold cycles.
	for i := 0; i < OrphanMismatchThreshold; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	// With copy-on-read, the block should NOT be orphaned because the
	// counter resets to the DB value (0) every cycle.
	store.errInjectBlockStore.mu.Lock()
	dbOrphanCount := store.errInjectBlockStore.pendingBlocks[0].OrphanMismatchCount
	dbStatus := store.errInjectBlockStore.pendingBlocks[0].Status
	store.errInjectBlockStore.mu.Unlock()

	if dbStatus == StatusOrphaned {
		t.Errorf("block should NOT be orphaned with copy-on-read mock when orphan DB writes fail "+
			"(got status %q)", dbStatus)
	}

	if dbOrphanCount != 0 {
		t.Errorf("stored orphan count should be 0 (DB write always fails), got %d", dbOrphanCount)
	}
}

// TestSOLO_Vector_V2_StabilityCounterResetOnDBFailure verifies that when the
// stability count DB write fails, the counter does NOT accumulate across cycles
// in production (PostgreSQL returns fresh structs with the old DB value).
//
// With the pointer-sharing mock, the processor's in-memory increment survives
// across cycles, so after StabilityWindowChecks cycles the block would be
// confirmed. With the copy-on-read mock, the counter resets each cycle.
func TestSOLO_Vector_V2_StabilityCounterResetOnDBFailure(t *testing.T) {
	t.Parallel()

	store := newCopyOnReadBlockStore()
	rpc := newErrInjectDaemonRPC()

	// Block at height 800, CORRECT hash, at maturity (chain at 1000, 200 confs).
	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)

	rpc.setBlockHash(800, block.Hash)
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	// Inject error: ALL DB writes fail (UpdateBlockConfirmationState checks errUpdateStatus).
	store.errInjectBlockStore.errUpdateStatus = fmt.Errorf("injected stability update failure")

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Run StabilityWindowChecks cycles.
	for i := 0; i < StabilityWindowChecks; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	// With copy-on-read, the block should NOT be confirmed because the
	// stability counter resets to 0 each cycle.
	store.errInjectBlockStore.mu.Lock()
	dbStabilityCount := store.errInjectBlockStore.pendingBlocks[0].StabilityCheckCount
	dbStatus := store.errInjectBlockStore.pendingBlocks[0].Status
	store.errInjectBlockStore.mu.Unlock()

	if dbStatus == StatusConfirmed {
		t.Errorf("block should NOT be confirmed with copy-on-read mock when stability DB writes fail "+
			"(got status %q)", dbStatus)
	}

	if dbStabilityCount != 0 {
		t.Errorf("stored stability count should be 0 (DB write always fails), got %d", dbStabilityCount)
	}
}

// TestSOLO_Vector_V2_CounterRecoveryAfterDBRecovers verifies that after the
// DB recovers from transient failures, the stability counter accumulates
// correctly and the block is eventually confirmed.
//
// Scenario: stability writes fail for the first 2 cycles (counter resets each
// time), then succeed for the next StabilityWindowChecks cycles (counter
// accumulates 1, 2, 3). The block should be confirmed after 5 total cycles.
func TestSOLO_Vector_V2_CounterRecoveryAfterDBRecovers(t *testing.T) {
	t.Parallel()

	store := newCopyOnReadBlockStore()
	rpc := newErrInjectDaemonRPC()

	block := makePendingBlock("BTC", 800)
	store.addPendingBlock(block)

	rpc.setBlockHash(800, block.Hash)
	tip := makeChainTip(1000)
	rpc.setChainTip(1000, tip)

	// All DB writes fail for first 2 cycles (UpdateBlockConfirmationState checks errUpdateStatus).
	store.errInjectBlockStore.errUpdateStatus = fmt.Errorf("injected stability update failure")

	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	ctx := context.Background()

	// Phase 1: 2 cycles with DB failures — counter resets each cycle.
	failCycles := 2
	for i := 0; i < failCycles; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	// Verify block is NOT confirmed yet.
	store.errInjectBlockStore.mu.Lock()
	midStatus := store.errInjectBlockStore.pendingBlocks[0].Status
	store.errInjectBlockStore.mu.Unlock()

	if midStatus == StatusConfirmed {
		t.Fatalf("block should not be confirmed during DB failure phase (got %q)", midStatus)
	}

	// Phase 2: Clear the error — DB writes succeed from now on.
	store.errInjectBlockStore.mu.Lock()
	store.errInjectBlockStore.errUpdateStatus = nil
	store.errInjectBlockStore.mu.Unlock()

	// Run StabilityWindowChecks more cycles — counter should now accumulate.
	for i := 0; i < StabilityWindowChecks; i++ {
		_ = proc.updateBlockConfirmations(ctx)
	}

	// Block should now be confirmed: stability counter reached the threshold.
	confirmed := store.hasStatusUpdateFor(800, StatusConfirmed)
	if !confirmed {
		store.errInjectBlockStore.mu.Lock()
		finalStatus := store.errInjectBlockStore.pendingBlocks[0].Status
		finalStability := store.errInjectBlockStore.pendingBlocks[0].StabilityCheckCount
		store.errInjectBlockStore.mu.Unlock()

		t.Errorf("block should be confirmed after DB recovery and %d stable cycles "+
			"(status=%q, stabilityCount=%d, want status=%q)",
			StabilityWindowChecks, finalStatus, finalStability, StatusConfirmed)
	}
}
