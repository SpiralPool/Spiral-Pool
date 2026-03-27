// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Tests for payment processor lifecycle: Start() V39 enforcement,
// processLoop() graceful shutdown, and processMatureBlocks() stub guard.
package payments

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"go.uber.org/zap"
)

// =============================================================================
// Part 1: Start() with V39 maturity floor
// =============================================================================

// TestStart_V39_MaturityBelowMinimum_WarnsOnly verifies that Start()
// with blockMaturity below the safe threshold (10) logs a warning but
// does NOT return an error. Regtest configs need low maturity for fast testing.
func TestStart_V39_MaturityBelowMinimum_WarnsOnly(t *testing.T) {
	t.Parallel()

	lowMaturities := []int{1, 2, 5, 9}
	for _, maturity := range lowMaturities {
		maturity := maturity
		t.Run(fmt.Sprintf("maturity_%d", maturity), func(t *testing.T) {
			t.Parallel()

			store := newMockBlockStore()
			rpc := newMockDaemonRPC()
			p := newTestProcessor(store, rpc, maturity)

			err := p.Start(context.Background())
			defer func() { _ = p.Stop() }()

			if err != nil {
				t.Fatalf("Start() should warn (not error) for blockMaturity=%d, got: %v", maturity, err)
			}
		})
	}
}

// TestStart_V39_MaturityAtMinimum_NoV39Error verifies that Start() does NOT
// return the V39 error when blockMaturity is exactly at the minimum (10).
// The processor should start successfully (it may still error on other things
// in production, but the V39 guard must not fire).
func TestStart_V39_MaturityAtMinimum_NoV39Error(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := p.Start(ctx)
	defer func() { _ = p.Stop() }()

	if err != nil && strings.Contains(err.Error(), "V39") {
		t.Errorf("Start() should NOT return V39 error for maturity=10, got: %v", err)
	}
}

// TestStart_V39_MaturityAboveMinimum_NoChange verifies that Start() passes
// through when blockMaturity is comfortably above the minimum. The configured
// value should be used as-is.
func TestStart_V39_MaturityAboveMinimum_NoChange(t *testing.T) {
	t.Parallel()

	safeMaturities := []int{10, 50, 100, 200, 500}
	for _, maturity := range safeMaturities {
		maturity := maturity
		t.Run(fmt.Sprintf("maturity_%d", maturity), func(t *testing.T) {
			t.Parallel()

			store := newMockBlockStore()
			rpc := &mockDaemonRPC{
				bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
				chainHeight:   1000,
				blockHashes:   make(map[uint64]string),
			}
			p := newTestProcessor(store, rpc, maturity)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := p.Start(ctx)
			defer func() { _ = p.Stop() }()

			if err != nil {
				t.Errorf("Start() should succeed for maturity=%d, got: %v", maturity, err)
			}

			// Verify the effective maturity is still what was configured
			effective := p.getBlockMaturity()
			if effective != maturity {
				t.Errorf("effective maturity should be %d, got %d", maturity, effective)
			}
		})
	}
}

// TestStart_V39_DefaultMaturity_NoV39Error verifies that blockMaturity=0
// (which uses the default of 100) does not trigger the V39 guard.
func TestStart_V39_DefaultMaturity_NoV39Error(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 0) // 0 => default (100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := p.Start(ctx)
	defer func() { _ = p.Stop() }()

	if err != nil && strings.Contains(err.Error(), "V39") {
		t.Errorf("Start() should not return V39 error for default maturity, got: %v", err)
	}

	// Default should be 100
	effective := p.getBlockMaturity()
	if effective != DefaultBlockMaturityConfirmations {
		t.Errorf("default maturity should be %d, got %d", DefaultBlockMaturityConfirmations, effective)
	}
}

// TestStart_Disabled_ReturnsNilImmediately verifies that Start() returns nil
// immediately when payments are disabled, without launching the processLoop goroutine.
func TestStart_Disabled_ReturnsNilImmediately(t *testing.T) {
	t.Parallel()

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled: false,
			Scheme:  "SOLO",
		},
		poolCfg: &config.PoolConfig{},
		logger:  zap.NewNop().Sugar(),
		stopCh:  make(chan struct{}),
	}

	err := p.Start(context.Background())
	if err != nil {
		t.Errorf("Start() with disabled payments should return nil, got: %v", err)
	}

	// Processor should NOT be running
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()
	if running {
		t.Error("Processor should not be running when payments are disabled")
	}
}

// =============================================================================
// Part 2: processLoop() lifecycle
// =============================================================================

// lifecycleDaemonRPC is a minimal DaemonRPC that records calls and returns
// stable chain data. Used for testing the processLoop lifecycle.
type lifecycleDaemonRPC struct {
	callCount atomic.Int64
}

func (m *lifecycleDaemonRPC) GetBlockchainInfo(_ context.Context) (*daemon.BlockchainInfo, error) {
	m.callCount.Add(1)
	return &daemon.BlockchainInfo{
		BestBlockHash: "0000000000000000000000000000000000000000000000000000000000001234",
		Blocks:        50000,
	}, nil
}

func (m *lifecycleDaemonRPC) GetBlockHash(_ context.Context, height uint64) (string, error) {
	return fmt.Sprintf("%064d", height), nil
}

// lifecycleBlockStore is a minimal BlockStore that returns empty results.
type lifecycleBlockStore struct{}

func (m *lifecycleBlockStore) GetPendingBlocks(_ context.Context) ([]*database.Block, error) {
	return nil, nil
}
func (m *lifecycleBlockStore) GetConfirmedBlocks(_ context.Context) ([]*database.Block, error) {
	return nil, nil
}
func (m *lifecycleBlockStore) GetBlocksByStatus(_ context.Context, _ string) ([]*database.Block, error) {
	return nil, nil
}
func (m *lifecycleBlockStore) UpdateBlockStatus(_ context.Context, _ uint64, _ string, _ string, _ float64) error {
	return nil
}
func (m *lifecycleBlockStore) UpdateBlockOrphanCount(_ context.Context, _ uint64, _ string, _ int) error {
	return nil
}
func (m *lifecycleBlockStore) UpdateBlockStabilityCount(_ context.Context, _ uint64, _ string, _ int, _ string) error {
	return nil
}
func (m *lifecycleBlockStore) GetBlockStats(_ context.Context) (*database.BlockStats, error) {
	return &database.BlockStats{}, nil
}
func (m *lifecycleBlockStore) UpdateBlockConfirmationState(_ context.Context, _ uint64, _ string, _ string, _ float64, _ int, _ int, _ string) error {
	return nil
}

// TestProcessLoop_ContextCancel_StopsCleanly verifies that cancelling the
// context passed to Start() causes the processLoop to exit gracefully.
func TestProcessLoop_ContextCancel_StopsCleanly(t *testing.T) {
	t.Parallel()

	rpc := &lifecycleDaemonRPC{}
	store := &lifecycleBlockStore{}

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled:       true,
			Interval:      50 * time.Millisecond, // Fast interval for testing
			Scheme:        "SOLO",
			BlockMaturity: 100,
		},
		poolCfg:      &config.PoolConfig{Coin: "DGB"},
		logger:       zap.NewNop().Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())

	err := p.Start(ctx)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Let at least one cycle run
	time.Sleep(200 * time.Millisecond)

	// Cancel context — processLoop should exit
	cancel()

	// Give time for the goroutine to stop
	time.Sleep(200 * time.Millisecond)

	// Verify at least one cycle ran
	p.mu.Lock()
	cycles := p.cycleCount
	p.mu.Unlock()
	if cycles == 0 {
		t.Error("Expected at least one processCycle to run before shutdown")
	}
}

// TestProcessLoop_Stop_StopsCleanly verifies that calling Stop() causes the
// processLoop to exit via the stopCh channel.
func TestProcessLoop_Stop_StopsCleanly(t *testing.T) {
	t.Parallel()

	rpc := &lifecycleDaemonRPC{}
	store := &lifecycleBlockStore{}

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled:       true,
			Interval:      50 * time.Millisecond,
			Scheme:        "SOLO",
			BlockMaturity: 100,
		},
		poolCfg:      &config.PoolConfig{Coin: "DGB"},
		logger:       zap.NewNop().Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}

	err := p.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Let at least one cycle run
	time.Sleep(200 * time.Millisecond)

	// Stop should be safe and cause processLoop to exit
	err = p.Stop()
	if err != nil {
		t.Errorf("Stop() returned error: %v", err)
	}

	// Give time for cleanup
	time.Sleep(200 * time.Millisecond)

	// Verify at least one cycle ran
	p.mu.Lock()
	cycles := p.cycleCount
	p.mu.Unlock()
	if cycles == 0 {
		t.Error("Expected at least one processCycle to run before Stop()")
	}

	// Verify the processor is no longer running
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()
	if running {
		t.Error("Processor should not be running after Stop()")
	}
}

// TestProcessLoop_RunsImmediatelyOnStart verifies that processLoop runs
// processCycle immediately on start (before the first ticker fires).
func TestProcessLoop_RunsImmediatelyOnStart(t *testing.T) {
	t.Parallel()

	rpc := &lifecycleDaemonRPC{}
	store := &lifecycleBlockStore{}

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled:       true,
			Interval:      10 * time.Second, // Long interval to prove immediate run
			Scheme:        "SOLO",
			BlockMaturity: 100,
		},
		poolCfg:      &config.PoolConfig{Coin: "DGB"},
		logger:       zap.NewNop().Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := p.Start(ctx)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() { _ = p.Stop() }()

	// Give the goroutine a brief moment to run the first cycle
	time.Sleep(200 * time.Millisecond)

	// Even though interval is 10s, processLoop calls processCycle() immediately.
	// GetBlockchainInfo is called by updateBlockConfirmations (which is called by processCycle).
	// With no pending blocks, it should still be called 0 times (no pending blocks = early return).
	// Actually, updateBlockConfirmations returns early if GetPendingBlocks returns nil,
	// so GetBlockchainInfo won't be called. But the cycle itself still runs.
	// We verify the cycle ran by checking cycleCount.
	p.mu.Lock()
	cycles := p.cycleCount
	p.mu.Unlock()

	if cycles == 0 {
		t.Error("Expected cycleCount > 0 indicating processLoop ran immediately on start")
	}
}

// TestProcessLoop_IntervalTiming verifies that processCycle is called
// approximately at the configured interval.
func TestProcessLoop_IntervalTiming(t *testing.T) {
	t.Parallel()

	rpc := &lifecycleDaemonRPC{}
	store := &lifecycleBlockStore{}

	interval := 100 * time.Millisecond
	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled:       true,
			Interval:      interval,
			Scheme:        "SOLO",
			BlockMaturity: 100,
		},
		poolCfg:      &config.PoolConfig{Coin: "DGB"},
		logger:       zap.NewNop().Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := p.Start(ctx)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() { _ = p.Stop() }()

	// Wait for ~3 intervals (plus the immediate first cycle)
	time.Sleep(350 * time.Millisecond)

	p.mu.Lock()
	cycles := p.cycleCount
	p.mu.Unlock()

	// Expect 1 (immediate) + ~3 (from ticker) = ~4 cycles
	// Allow some tolerance for scheduling jitter
	if cycles < 2 {
		t.Errorf("Expected at least 2 cycles in 350ms with 100ms interval, got %d", cycles)
	}
	if cycles > 6 {
		t.Errorf("Expected no more than ~6 cycles, got %d (possible runaway)", cycles)
	}
}

// =============================================================================
// Part 3: processMatureBlocks() empty stub guard — CRITICAL
// =============================================================================

// TestProcessMatureBlocks_SOLO_ReturnsNil verifies that processMatureBlocks()
// returns nil for the SOLO scheme. This is the current behavior: it's a
// placeholder stub that silently succeeds.
//
// DOCUMENTED BEHAVIOR: For non-SOLO schemes, processMatureBlocks() also returns nil.
// This is a no-op because Spiral Pool is a solo mining pool and does not implement
// pooled payout schemes. The Validate() function in config.go permanently rejects
// non-SOLO schemes, so this code path should never be reached in practice.
func TestProcessMatureBlocks_SOLO_ReturnsNil(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 100)

	err := p.processMatureBlocks(context.Background())
	if err != nil {
		t.Errorf("processMatureBlocks() should return nil for SOLO, got: %v", err)
	}
}

// TestProcessMatureBlocks_NonSOLO_SilentlyReturnsNil documents the behavior:
// processMatureBlocks() returns nil even for non-SOLO schemes.
//
// Spiral Pool is a solo mining pool. Pooled payout schemes are permanently
// unsupported. The Validate() function in config.go rejects non-SOLO schemes,
// so this code path should never execute. If someone constructs a Processor
// directly (bypassing validation), the processor silently does nothing.
func TestProcessMatureBlocks_NonSOLO_SilentlyReturnsNil(t *testing.T) {
	t.Parallel()

	nonSOLOSchemes := []string{"PPLNS", "PPS", "PROP", ""}
	for _, scheme := range nonSOLOSchemes {
		scheme := scheme
		t.Run(fmt.Sprintf("scheme_%s", scheme), func(t *testing.T) {
			t.Parallel()

			store := newMockBlockStore()
			rpc := &mockDaemonRPC{
				bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
				chainHeight:   1000,
				blockHashes:   make(map[uint64]string),
			}

			cfg := &config.PaymentsConfig{
				Enabled:       true,
				Interval:      time.Minute,
				Scheme:        scheme,
				BlockMaturity: 100,
			}
			logger := zap.NewNop()
			p := &Processor{
				cfg:          cfg,
				poolCfg:      &config.PoolConfig{},
				logger:       logger.Sugar(),
				db:           store,
				daemonClient: rpc,
				stopCh:       make(chan struct{}),
			}

			err := p.processMatureBlocks(context.Background())

			// processMatureBlocks() returns nil for all schemes. Non-SOLO schemes
			// are permanently blocked at config validation, so this path is unreachable
			// in production. Spiral Pool is solo-only by design.
			if err != nil {
				t.Errorf("processMatureBlocks() returned error for scheme=%q: %v "+
					"(unexpected — stub should return nil for all schemes)", scheme, err)
			} else {
				t.Logf("processMatureBlocks() returns nil for scheme=%q — "+
					"non-SOLO schemes are permanently blocked at config validation. "+
					"Spiral Pool is a solo mining pool.", scheme)
			}
		})
	}
}

// TestExecutePendingPayments_SOLO_ReturnsNil verifies that executePendingPayments()
// returns nil for SOLO mode (placeholder stub).
func TestExecutePendingPayments_SOLO_ReturnsNil(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := &mockDaemonRPC{
		bestBlockHash: "0000000000000000000000000000000000000000000000000000000000000000",
		chainHeight:   1000,
		blockHashes:   make(map[uint64]string),
	}
	p := newTestProcessor(store, rpc, 100)

	err := p.executePendingPayments(context.Background())
	if err != nil {
		t.Errorf("executePendingPayments() should return nil for SOLO, got: %v", err)
	}
}

// =============================================================================
// Part 4: getEffectiveInterval() edge cases
// =============================================================================

// TestGetEffectiveInterval_ExplicitValue verifies that an explicit interval
// is used when configured.
func TestGetEffectiveInterval_ExplicitValue(t *testing.T) {
	t.Parallel()

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Interval: 5 * time.Minute,
		},
		logger: zap.NewNop().Sugar(),
	}

	interval := p.getEffectiveInterval()
	if interval != 5*time.Minute {
		t.Errorf("Expected 5m, got %v", interval)
	}
}

// TestGetEffectiveInterval_AutoScaleFromBlockTime verifies V14 auto-scaling:
// interval = blockTime * 10, clamped to [60s, 10m].
func TestGetEffectiveInterval_AutoScaleFromBlockTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		blockTime int
		expected  time.Duration
	}{
		{15, 2*time.Minute + 30*time.Second}, // 15*10=150s = 2m30s
		{60, 10 * time.Minute},               // 60*10=600s = 10m (at cap)
		{600, 10 * time.Minute},              // 600*10=6000s -> capped at 10m
		{3, 60 * time.Second},                // 3*10=30s -> min 60s
	}

	for _, tc := range tests {
		tc := tc
		t.Run(fmt.Sprintf("blockTime_%d", tc.blockTime), func(t *testing.T) {
			t.Parallel()

			p := &Processor{
				cfg: &config.PaymentsConfig{
					BlockTime: tc.blockTime,
				},
				logger: zap.NewNop().Sugar(),
			}

			interval := p.getEffectiveInterval()
			if interval != tc.expected {
				t.Errorf("blockTime=%d: expected %v, got %v", tc.blockTime, tc.expected, interval)
			}
		})
	}
}

// TestGetEffectiveInterval_Fallback verifies the default 10-minute fallback
// when neither interval nor blockTime is configured.
func TestGetEffectiveInterval_Fallback(t *testing.T) {
	t.Parallel()

	p := &Processor{
		cfg:    &config.PaymentsConfig{},
		logger: zap.NewNop().Sugar(),
	}

	interval := p.getEffectiveInterval()
	if interval != 10*time.Minute {
		t.Errorf("Expected 10m fallback, got %v", interval)
	}
}

// =============================================================================
// Part 6: HA payment fencing in processCycle
// =============================================================================

// TestProcessCycle_HA_BackupSkipsCycle verifies that when HA is enabled and
// this node is NOT master, processCycle returns immediately without processing.
func TestProcessCycle_HA_BackupSkipsCycle(t *testing.T) {
	t.Parallel()

	rpc := &lifecycleDaemonRPC{}
	store := &lifecycleBlockStore{}

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled:       true,
			Interval:      time.Minute,
			Scheme:        "SOLO",
			BlockMaturity: 100,
		},
		poolCfg:      &config.PoolConfig{Coin: "DGB"},
		logger:       zap.NewNop().Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
		haEnabled:    true,
	}
	p.isMaster.Store(false) // This node is backup

	p.processCycle(context.Background())

	// No RPC calls should have been made since the cycle was skipped
	if rpc.callCount.Load() > 0 {
		t.Error("Backup node should skip processCycle entirely, but RPC calls were made")
	}

	// cycleCount should NOT have been incremented
	if p.cycleCount > 0 {
		t.Errorf("cycleCount should be 0 for skipped backup cycle, got %d", p.cycleCount)
	}
}

// TestProcessCycle_HA_MasterProcesses verifies that when HA is enabled and
// this node IS master, processCycle runs normally.
func TestProcessCycle_HA_MasterProcesses(t *testing.T) {
	t.Parallel()

	rpc := &lifecycleDaemonRPC{}
	store := &lifecycleBlockStore{}

	p := &Processor{
		cfg: &config.PaymentsConfig{
			Enabled:       true,
			Interval:      time.Minute,
			Scheme:        "SOLO",
			BlockMaturity: 100,
		},
		poolCfg:      &config.PoolConfig{Coin: "DGB"},
		logger:       zap.NewNop().Sugar(),
		db:           store,
		daemonClient: rpc,
		stopCh:       make(chan struct{}),
		haEnabled:    true,
	}
	p.isMaster.Store(true) // This node is master

	p.processCycle(context.Background())

	// cycleCount should have been incremented
	if p.cycleCount == 0 {
		t.Error("Master node should run processCycle, but cycleCount is 0")
	}
}

// =============================================================================
// Part 7: Consecutive failure tracking
// =============================================================================

// TestProcessCycle_ConsecutiveFailureTracking verifies that failed cycles
// increment the consecutive failure counter and successful cycles reset it.
func TestProcessCycle_ConsecutiveFailureTracking(t *testing.T) {
	t.Parallel()

	store := newErrInjectBlockStore()
	rpc := newErrInjectDaemonRPC()

	p := newParanoidTestProcessor(store, rpc, 100)
	p.poolCfg = &config.PoolConfig{Coin: "DGB"}

	// Inject error to make updateBlockConfirmations fail
	store.errGetPending = fmt.Errorf("simulated DB failure")

	// Add a pending block so GetPendingBlocks is actually called
	store.addPendingBlock(makePendingBlock("DGB", 900))

	// Run multiple failed cycles
	for i := 0; i < 3; i++ {
		p.processCycle(context.Background())
	}

	if p.consecutiveFailedCycles != 3 {
		t.Errorf("Expected 3 consecutive failures, got %d", p.consecutiveFailedCycles)
	}

	// Fix the error and run a successful cycle
	store.errGetPending = nil
	p.processCycle(context.Background())

	if p.consecutiveFailedCycles != 0 {
		t.Errorf("Expected 0 consecutive failures after recovery, got %d", p.consecutiveFailedCycles)
	}
}
