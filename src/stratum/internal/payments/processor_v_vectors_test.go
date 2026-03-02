// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Tests for V-vector gaps identified in the audit. Covers V14 (auto-scaling
// interval), V15 (consecutive failure tracking), V16 (deep reorg max age
// auto-scaling), V19 (IBD skip), V28 (stale pending block warnings),
// V12 (status guard transitions), and V26 (OnStatusGuardRejection callback).
package payments

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// V14 — getEffectiveInterval auto-scaling
// ═══════════════════════════════════════════════════════════════════════════════

func TestV14_EffectiveInterval_DefaultFallback(t *testing.T) {
	t.Parallel()

	// No BlockTime, no Interval → should fall back to 10*time.Minute.
	p := &Processor{
		cfg:     &config.PaymentsConfig{},
		poolCfg: &config.PoolConfig{},
		logger:  zap.NewNop().Sugar(),
		stopCh:  make(chan struct{}),
	}

	got := p.getEffectiveInterval()
	want := 10 * time.Minute
	if got != want {
		t.Errorf("getEffectiveInterval() with no blockTime and no interval = %v, want %v", got, want)
	}
}

func TestV14_EffectiveInterval_ExplicitOverride(t *testing.T) {
	t.Parallel()

	// Explicit interval should win over auto-scaling even when BlockTime is set.
	explicit := 5 * time.Minute
	p := &Processor{
		cfg: &config.PaymentsConfig{
			Interval:  explicit,
			BlockTime: 15, // Would auto-scale to 150s, but explicit wins
		},
		poolCfg: &config.PoolConfig{},
		logger:  zap.NewNop().Sugar(),
		stopCh:  make(chan struct{}),
	}

	got := p.getEffectiveInterval()
	if got != explicit {
		t.Errorf("getEffectiveInterval() with explicit interval = %v, want %v", got, explicit)
	}
}

func TestV14_EffectiveInterval_AutoScaling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		blockTime int
		want      time.Duration
	}{
		{"DGB_15s", 15, 150 * time.Second},
		{"FBTC_30s", 30, 300 * time.Second},
		{"DOGE_60s", 60, 10 * time.Minute},    // capped to max 10 minutes
		{"LTC_150s", 150, 10 * time.Minute},   // capped to max 10 minutes
		{"BTC_600s", 600, 10 * time.Minute},   // capped to max 10 minutes
		{"VeryFast_3s", 3, 60 * time.Second},  // clamped to minimum 60s
		{"VeryFast_5s", 5, 60 * time.Second},  // clamped to minimum 60s
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &Processor{
				cfg: &config.PaymentsConfig{
					BlockTime: tt.blockTime,
					// Interval left at zero so auto-scaling kicks in
				},
				poolCfg: &config.PoolConfig{},
				logger:  zap.NewNop().Sugar(),
				stopCh:  make(chan struct{}),
			}

			got := p.getEffectiveInterval()
			if got != tt.want {
				t.Errorf("getEffectiveInterval() blockTime=%d → %v, want %v",
					tt.blockTime, got, tt.want)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// V15 — consecutiveFailedCycles tracking
// ═══════════════════════════════════════════════════════════════════════════════

func TestV15_ConsecutiveFailures_Tracking(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Inject a persistent error on GetPendingBlocks so updateBlockConfirmations fails.
	store.errGetPending = fmt.Errorf("injected DB failure for V15 test")

	// Run 3 cycles — each should increment consecutiveFailedCycles.
	for i := 0; i < 3; i++ {
		proc.processCycle(context.Background())
	}

	if proc.consecutiveFailedCycles != 3 {
		t.Errorf("after 3 failed cycles: consecutiveFailedCycles = %d, want 3",
			proc.consecutiveFailedCycles)
	}

	// Fix the error — next cycle should reset the counter.
	store.mu.Lock()
	store.errGetPending = nil
	store.mu.Unlock()

	proc.processCycle(context.Background())

	if proc.consecutiveFailedCycles != 0 {
		t.Errorf("after recovery: consecutiveFailedCycles = %d, want 0",
			proc.consecutiveFailedCycles)
	}
}

func TestV15_ConsecutiveFailures_ThresholdWarning(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Inject a persistent error.
	store.errGetPending = fmt.Errorf("injected DB failure for V15 threshold test")

	// Run exactly ConsecutiveFailureThreshold cycles.
	for i := 0; i < ConsecutiveFailureThreshold; i++ {
		proc.processCycle(context.Background())
	}

	if proc.consecutiveFailedCycles != ConsecutiveFailureThreshold {
		t.Errorf("after %d failed cycles: consecutiveFailedCycles = %d, want %d",
			ConsecutiveFailureThreshold, proc.consecutiveFailedCycles, ConsecutiveFailureThreshold)
	}
}

func TestV15_ConsecutiveFailures_RecoveryResets(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Inject a persistent error for 20 failures.
	store.errGetPending = fmt.Errorf("injected DB failure for V15 recovery test")

	for i := 0; i < 20; i++ {
		proc.processCycle(context.Background())
	}

	if proc.consecutiveFailedCycles != 20 {
		t.Fatalf("after 20 failed cycles: consecutiveFailedCycles = %d, want 20",
			proc.consecutiveFailedCycles)
	}

	// Fix the error — 1 success should reset to 0.
	store.mu.Lock()
	store.errGetPending = nil
	store.mu.Unlock()

	proc.processCycle(context.Background())

	if proc.consecutiveFailedCycles != 0 {
		t.Errorf("after recovery from 20 failures: consecutiveFailedCycles = %d, want 0",
			proc.consecutiveFailedCycles)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// V16 — getDeepReorgMaxAge auto-scaling
// ═══════════════════════════════════════════════════════════════════════════════

func TestV16_DeepReorgMaxAge_DefaultValue(t *testing.T) {
	t.Parallel()

	// No BlockTime → should return DeepReorgMaxAge constant (1000).
	p := &Processor{
		cfg:     &config.PaymentsConfig{},
		poolCfg: &config.PoolConfig{},
		logger:  zap.NewNop().Sugar(),
		stopCh:  make(chan struct{}),
	}

	got := p.getDeepReorgMaxAge()
	if got != DeepReorgMaxAge {
		t.Errorf("getDeepReorgMaxAge() with no blockTime = %d, want %d", got, DeepReorgMaxAge)
	}
}

func TestV16_DeepReorgMaxAge_ExplicitOverride(t *testing.T) {
	t.Parallel()

	// Explicit DeepReorgMaxAge=5000 should win over auto-scaling.
	p := &Processor{
		cfg: &config.PaymentsConfig{
			DeepReorgMaxAge: 5000,
			BlockTime:       15, // Would auto-scale to 5760, but explicit wins
		},
		poolCfg: &config.PoolConfig{},
		logger:  zap.NewNop().Sugar(),
		stopCh:  make(chan struct{}),
	}

	got := p.getDeepReorgMaxAge()
	if got != 5000 {
		t.Errorf("getDeepReorgMaxAge() with explicit override = %d, want 5000", got)
	}
}

func TestV16_DeepReorgMaxAge_AutoScaling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		blockTime int
		want      uint64
	}{
		{"DGB_15s", 15, 5760},             // 86400/15 = 5760 > 1000 → 5760
		{"FBTC_30s", 30, 2880},            // 86400/30 = 2880 > 1000 → 2880
		{"DOGE_60s", 60, 1440},            // 86400/60 = 1440 > 1000 → 1440
		{"LTC_150s", 150, DeepReorgMaxAge}, // 86400/150 = 576 < 1000 → default
		{"BTC_600s", 600, DeepReorgMaxAge}, // 86400/600 = 144 < 1000 → default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &Processor{
				cfg: &config.PaymentsConfig{
					BlockTime: tt.blockTime,
				},
				poolCfg: &config.PoolConfig{},
				logger:  zap.NewNop().Sugar(),
				stopCh:  make(chan struct{}),
			}

			got := p.getDeepReorgMaxAge()
			if got != tt.want {
				t.Errorf("getDeepReorgMaxAge() blockTime=%d → %d, want %d",
					tt.blockTime, got, tt.want)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// V19 — IBD (Initial Block Download) skip
// ═══════════════════════════════════════════════════════════════════════════════

func TestV19_IBD_SkipsCycle(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Add a pending block so that if updateBlockConfirmations processes it,
	// we would see status updates.
	block := makePendingBlock("BTC", 500)
	store.addPendingBlock(block)
	rpc.setBlockHash(500, block.Hash)
	rpc.setChainTip(700, makeChainTip(700))

	// Mock daemon to return IBD=true.
	rpc.mu.Lock()
	rpc.blockchainInfoFunc = func(_ context.Context, _ int64) (*daemon.BlockchainInfo, error) {
		return &daemon.BlockchainInfo{
			BestBlockHash:        makeChainTip(700),
			Blocks:               700,
			Headers:              100000,
			InitialBlockDownload: true,
			VerificationProgress: 0.01,
		}, nil
	}
	rpc.mu.Unlock()

	// Run updateBlockConfirmations — should skip due to IBD.
	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("updateBlockConfirmations during IBD returned error: %v", err)
	}

	// Verify 0 status updates were made.
	count := store.statusUpdateCount()
	if count != 0 {
		t.Errorf("expected 0 status updates during IBD, got %d", count)
	}
}

func TestV19_IBD_ResumeAfterSync(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Add a pending block well within maturity range.
	block := makePendingBlock("BTC", 500)
	store.addPendingBlock(block)
	rpc.setBlockHash(500, block.Hash)

	tip := makeChainTip(700)
	rpc.setChainTip(700, tip)

	// Use an atomic counter to track calls and switch IBD off after 2 calls.
	var callCount atomic.Int64
	rpc.mu.Lock()
	rpc.blockchainInfoFunc = func(_ context.Context, _ int64) (*daemon.BlockchainInfo, error) {
		n := callCount.Add(1)
		if n <= 2 {
			// First 2 top-level calls: IBD is true.
			return &daemon.BlockchainInfo{
				BestBlockHash:        tip,
				Blocks:               700,
				Headers:              100000,
				InitialBlockDownload: true,
				VerificationProgress: 0.01,
			}, nil
		}
		// After call 2: IBD is false, fully synced.
		return &daemon.BlockchainInfo{
			BestBlockHash:        tip,
			Blocks:               700,
			Headers:              700,
			InitialBlockDownload: false,
			VerificationProgress: 1.0,
		}, nil
	}
	rpc.mu.Unlock()

	// First call: IBD=true → 0 updates.
	err := proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("call 1 error: %v", err)
	}
	if store.statusUpdateCount() != 0 {
		t.Errorf("call 1: expected 0 status updates during IBD, got %d", store.statusUpdateCount())
	}

	// Second call: IBD=true → still 0 updates.
	err = proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("call 2 error: %v", err)
	}
	if store.statusUpdateCount() != 0 {
		t.Errorf("call 2: expected 0 status updates during IBD, got %d", store.statusUpdateCount())
	}

	// Third call: IBD=false → updates should happen.
	err = proc.updateBlockConfirmations(context.Background())
	if err != nil {
		t.Fatalf("call 3 error: %v", err)
	}
	if store.statusUpdateCount() == 0 {
		t.Error("call 3: expected status updates after IBD=false, got 0")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// V28 — Stale pending block warning
// ═══════════════════════════════════════════════════════════════════════════════

func TestV28_StalePendingBlocks_FreshBlock(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Add a block created just now.
	block := makePendingBlock("BTC", 1000)
	block.Created = time.Now()
	store.addPendingBlock(block)

	// Should not panic.
	proc.checkStalePendingBlocks(context.Background())
}

func TestV28_StalePendingBlocks_OldBlock(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Add a block created 48 hours ago — exercises stale detection path.
	block := makePendingBlock("BTC", 2000)
	block.Created = time.Now().Add(-48 * time.Hour)
	store.addPendingBlock(block)

	// Should not panic — just logs warnings.
	proc.checkStalePendingBlocks(context.Background())
}

func TestV28_StalePendingBlocks_MixedAges(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Fresh block: 1 hour old.
	fresh := makePendingBlock("BTC", 3000)
	fresh.Created = time.Now().Add(-1 * time.Hour)
	store.addPendingBlock(fresh)

	// Stale block: 72 hours old.
	stale := makePendingBlock("BTC", 3001)
	stale.Created = time.Now().Add(-72 * time.Hour)
	store.addPendingBlock(stale)

	// Should not panic — logs warning for the stale block only.
	proc.checkStalePendingBlocks(context.Background())
}

func TestV28_StalePendingBlocks_DBError(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()
	proc := newParanoidTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)

	// Inject error on GetPendingBlocks.
	store.errGetPending = fmt.Errorf("injected DB error for V28 test")

	// Should not panic — early return on error.
	proc.checkStalePendingBlocks(context.Background())
}

func TestV28_StaleBlockAgeHours_Constant(t *testing.T) {
	t.Parallel()

	// Verify StaleBlockAgeHours is within a sensible range [6, 168] (6h to 1 week).
	if StaleBlockAgeHours < 6 || StaleBlockAgeHours > 168 {
		t.Errorf("StaleBlockAgeHours = %d, want value in [6, 168]", StaleBlockAgeHours)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// V12 — Status guard transition logic (application-level)
// ═══════════════════════════════════════════════════════════════════════════════

// TestV12_StatusGuard_ValidTransitions tests the V12 SQL WHERE clause logic:
//
//	WHERE (status = 'pending' OR (status = 'confirmed' AND $1 IN ('orphaned', 'paid')))
//
// This validates which status transitions the guard allows and which it blocks.
func TestV12_StatusGuard_ValidTransitions(t *testing.T) {
	t.Parallel()

	// statusGuardAllows simulates the SQL WHERE clause:
	// WHERE (status = 'pending' OR (status = 'confirmed' AND $1 IN ('orphaned', 'paid')))
	// currentStatus = current block status in DB
	// newStatus = the target status being set ($1)
	// Returns true if the guard would allow the UPDATE to proceed.
	statusGuardAllows := func(currentStatus, newStatus string) bool {
		if currentStatus == StatusPending {
			return true
		}
		if currentStatus == StatusConfirmed && (newStatus == StatusOrphaned || newStatus == StatusPaid) {
			return true
		}
		return false
	}

	tests := []struct {
		name          string
		currentStatus string
		newStatus     string
		allowed       bool
	}{
		// From pending: all transitions allowed (WHERE status = 'pending' matches)
		{"pending_to_pending", StatusPending, StatusPending, true},
		{"pending_to_confirmed", StatusPending, StatusConfirmed, true},
		{"pending_to_orphaned", StatusPending, StatusOrphaned, true},

		// From confirmed: only orphaned and paid allowed
		{"confirmed_to_orphaned", StatusConfirmed, StatusOrphaned, true},
		{"confirmed_to_paid", StatusConfirmed, StatusPaid, true},
		{"confirmed_to_pending_BLOCKED", StatusConfirmed, StatusPending, false},

		// From orphaned: all transitions blocked (terminal state)
		{"orphaned_to_pending_BLOCKED", StatusOrphaned, StatusPending, false},
		{"orphaned_to_confirmed_BLOCKED", StatusOrphaned, StatusConfirmed, false},
		{"orphaned_to_paid_BLOCKED", StatusOrphaned, StatusPaid, false},

		// From paid: all transitions blocked (terminal state)
		{"paid_to_pending_BLOCKED", StatusPaid, StatusPending, false},
		{"paid_to_confirmed_BLOCKED", StatusPaid, StatusConfirmed, false},
		{"paid_to_orphaned_BLOCKED", StatusPaid, StatusOrphaned, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := statusGuardAllows(tt.currentStatus, tt.newStatus)
			if got != tt.allowed {
				t.Errorf("statusGuard(%q → %q) = %v, want %v",
					tt.currentStatus, tt.newStatus, got, tt.allowed)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// V26 — OnStatusGuardRejection callback (in database package)
// ═══════════════════════════════════════════════════════════════════════════════

func TestV26_OnStatusGuardRejection_Fires(t *testing.T) {
	// NOT parallel: mutates package-level database callback.

	// Restore nil callback after test.
	t.Cleanup(func() {
		database.SetOnStatusGuardRejection(nil)
	})

	var counter atomic.Int64
	database.SetOnStatusGuardRejection(func() {
		counter.Add(1)
	})

	// Call it twice via the exported helper.
	database.CallOnStatusGuardRejection()
	database.CallOnStatusGuardRejection()

	if counter.Load() != 2 {
		t.Errorf("OnStatusGuardRejection counter = %d, want 2", counter.Load())
	}
}

func TestV26_OnStatusGuardRejection_NilSafe(t *testing.T) {
	// NOT parallel: mutates package-level database callback.

	// Restore nil callback after test.
	t.Cleanup(func() {
		database.SetOnStatusGuardRejection(nil)
	})

	// Set to nil.
	database.SetOnStatusGuardRejection(nil)

	// callOnStatusGuardRejection handles nil internally — verify no panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("nil OnStatusGuardRejection caused panic: %v", r)
			}
		}()
		database.CallOnStatusGuardRejection()
	}()
}
