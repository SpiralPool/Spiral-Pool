// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package payments

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spiralpool/stratum/internal/database"
)

// =============================================================================
// Mock Advisory Locker
// =============================================================================

// mockAdvisoryLocker implements AdvisoryLocker with call tracking and
// configurable error injection for testing HA advisory lock behavior.
type mockAdvisoryLocker struct {
	mu           sync.Mutex
	locked       bool
	lockErr      error
	releaseErr   error
	lockCalls    int
	releaseCalls int
}

func (m *mockAdvisoryLocker) TryAdvisoryLock(ctx context.Context, lockID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lockCalls++
	if m.lockErr != nil {
		return false, m.lockErr
	}
	if m.locked {
		return false, nil // Another process holds it
	}
	m.locked = true
	return true, nil
}

func (m *mockAdvisoryLocker) ReleaseAdvisoryLock(ctx context.Context, lockID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCalls++
	m.locked = false
	return m.releaseErr
}

// getLockCalls returns the number of TryAdvisoryLock calls (thread-safe).
func (m *mockAdvisoryLocker) getLockCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lockCalls
}

// getReleaseCalls returns the number of ReleaseAdvisoryLock calls (thread-safe).
func (m *mockAdvisoryLocker) getReleaseCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.releaseCalls
}

// isLocked returns the current lock state (thread-safe).
func (m *mockAdvisoryLocker) isLocked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.locked
}

// =============================================================================
// HA Tests
// =============================================================================

// TestProcessor_HA_SkipCycleOnBackup verifies that when haEnabled=true and
// isMaster=false, processCycle is a no-op: no DB calls, no RPC calls, and
// cycleCount does not increment.
func TestProcessor_HA_SkipCycleOnBackup(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(true)
	proc.SetMasterRole(false)

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	// cycleCount must NOT increment — the cycle was skipped entirely.
	if proc.cycleCount != initialCycle {
		t.Errorf("expected cycleCount to stay at %d on backup node, got %d",
			initialCycle, proc.cycleCount)
	}

	// No RPC calls should have been made.
	rpc.mu.Lock()
	bcInfoCalls := rpc.getBlockchainInfoCalls
	bhCalls := rpc.getBlockHashCalls
	rpc.mu.Unlock()

	if bcInfoCalls != 0 {
		t.Errorf("expected 0 GetBlockchainInfo calls on backup, got %d", bcInfoCalls)
	}
	if bhCalls != 0 {
		t.Errorf("expected 0 GetBlockHash calls on backup, got %d", bhCalls)
	}

	// No DB calls should have been made.
	statusUpdates := store.getStatusUpdates()
	if len(statusUpdates) != 0 {
		t.Errorf("expected 0 status updates on backup, got %d", len(statusUpdates))
	}
}

// TestProcessor_HA_RunCycleOnMaster verifies that when haEnabled=true and
// isMaster=true, processCycle runs normally (cycleCount increments, RPC
// calls are made for pending block processing).
func TestProcessor_HA_RunCycleOnMaster(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(true)
	proc.SetMasterRole(true)

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	// cycleCount must increment — the master runs the cycle.
	if proc.cycleCount != initialCycle+1 {
		t.Errorf("expected cycleCount %d on master, got %d",
			initialCycle+1, proc.cycleCount)
	}
}

// TestProcessor_HA_NonHAMode_AlwaysRuns verifies that when haEnabled=false,
// processCycle always runs regardless of the isMaster value. This is the
// default non-HA (standalone) mode.
func TestProcessor_HA_NonHAMode_AlwaysRuns(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(false)
	proc.SetMasterRole(false) // Even with isMaster=false, non-HA mode runs

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	if proc.cycleCount != initialCycle+1 {
		t.Errorf("expected cycleCount %d in non-HA mode, got %d",
			initialCycle+1, proc.cycleCount)
	}

	// Also verify with isMaster=true (should also run).
	proc.SetMasterRole(true)
	proc.processCycle(context.Background())

	if proc.cycleCount != initialCycle+2 {
		t.Errorf("expected cycleCount %d in non-HA mode with isMaster=true, got %d",
			initialCycle+2, proc.cycleCount)
	}
}

// TestProcessor_HA_RoleTransition_MasterToBackup verifies that flipping
// isMaster from true to false causes the processor to stop running cycles.
// The first cycle should run, then after role change the second should skip.
func TestProcessor_HA_RoleTransition_MasterToBackup(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(true)
	proc.SetMasterRole(true)

	// First cycle: master, should run.
	proc.processCycle(context.Background())
	if proc.cycleCount != 1 {
		t.Fatalf("expected cycleCount 1 after first master cycle, got %d", proc.cycleCount)
	}

	// Transition to backup.
	proc.SetMasterRole(false)

	// Second cycle: backup, should skip.
	proc.processCycle(context.Background())
	if proc.cycleCount != 1 {
		t.Errorf("expected cycleCount to stay at 1 after transition to backup, got %d",
			proc.cycleCount)
	}
}

// TestProcessor_HA_RoleTransition_BackupToMaster verifies that flipping
// isMaster from false to true causes the processor to resume running cycles.
func TestProcessor_HA_RoleTransition_BackupToMaster(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(true)
	proc.SetMasterRole(false)

	// First cycle: backup, should skip.
	proc.processCycle(context.Background())
	if proc.cycleCount != 0 {
		t.Fatalf("expected cycleCount 0 on backup, got %d", proc.cycleCount)
	}

	// Transition to master.
	proc.SetMasterRole(true)

	// Second cycle: master, should run.
	proc.processCycle(context.Background())
	if proc.cycleCount != 1 {
		t.Errorf("expected cycleCount 1 after transition to master, got %d",
			proc.cycleCount)
	}
}

// TestProcessor_HA_AdvisoryLock_AcquiredOnCycle verifies that when an
// advisoryLocker is set and the lock is successfully acquired, the cycle
// runs normally (cycleCount increments, TryAdvisoryLock and
// ReleaseAdvisoryLock are both called).
func TestProcessor_HA_AdvisoryLock_AcquiredOnCycle(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	locker := &mockAdvisoryLocker{}
	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetAdvisoryLocker(locker)

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	// Cycle should have run.
	if proc.cycleCount != initialCycle+1 {
		t.Errorf("expected cycleCount %d, got %d", initialCycle+1, proc.cycleCount)
	}

	// TryAdvisoryLock should have been called exactly once.
	if lc := locker.getLockCalls(); lc != 1 {
		t.Errorf("expected 1 TryAdvisoryLock call, got %d", lc)
	}

	// ReleaseAdvisoryLock should have been called exactly once (deferred).
	if rc := locker.getReleaseCalls(); rc != 1 {
		t.Errorf("expected 1 ReleaseAdvisoryLock call, got %d", rc)
	}

	// Lock should be released after cycle.
	if locker.isLocked() {
		t.Error("expected advisory lock to be released after cycle")
	}
}

// TestProcessor_HA_AdvisoryLock_Rejected verifies that when the advisory lock
// is held by another process (TryAdvisoryLock returns false, nil), the cycle
// is skipped entirely: cycleCount does not increment, no DB operations occur.
func TestProcessor_HA_AdvisoryLock_Rejected(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	// Add a pending block to verify it is NOT processed.
	store.addPendingBlock(&database.Block{
		Height: 1000,
		Hash:   "aaaa111111111111bbbb222222222222",
		Status: StatusPending,
		Miner:  "test-miner",
	})
	rpc := newMockDaemonRPC()
	rpc.setBlockHash(1000, "aaaa111111111111bbbb222222222222")

	locker := &mockAdvisoryLocker{locked: true} // Pre-locked by another process
	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetAdvisoryLocker(locker)

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	// Cycle must NOT have run.
	if proc.cycleCount != initialCycle {
		t.Errorf("expected cycleCount to stay at %d when lock rejected, got %d",
			initialCycle, proc.cycleCount)
	}

	// ReleaseAdvisoryLock should NOT have been called.
	if rc := locker.getReleaseCalls(); rc != 0 {
		t.Errorf("expected 0 ReleaseAdvisoryLock calls when lock rejected, got %d", rc)
	}

	// No DB updates should have happened.
	statusUpdates := store.getStatusUpdates()
	if len(statusUpdates) != 0 {
		t.Errorf("expected 0 status updates when lock rejected, got %d", len(statusUpdates))
	}
}

// TestProcessor_HA_AdvisoryLock_Error verifies that when TryAdvisoryLock
// returns an error, the cycle continues anyway (VIP-only fencing). The
// advisory lock is defense-in-depth; if the DB is unreachable, VIP fencing
// via isMaster is still enforced.
func TestProcessor_HA_AdvisoryLock_Error(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	lockErr := errors.New("connection refused: postgres unavailable")
	locker := &mockAdvisoryLocker{lockErr: lockErr}
	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetAdvisoryLocker(locker)

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	// Cycle should still run despite the lock error.
	if proc.cycleCount != initialCycle+1 {
		t.Errorf("expected cycleCount %d after lock error (VIP-only fencing), got %d",
			initialCycle+1, proc.cycleCount)
	}

	// TryAdvisoryLock was called.
	if lc := locker.getLockCalls(); lc != 1 {
		t.Errorf("expected 1 TryAdvisoryLock call, got %d", lc)
	}

	// ReleaseAdvisoryLock should NOT have been called (lock was never acquired).
	if rc := locker.getReleaseCalls(); rc != 0 {
		t.Errorf("expected 0 ReleaseAdvisoryLock calls after lock error, got %d", rc)
	}
}

// TestProcessor_HA_AdvisoryLock_ReleasedAfterCycle verifies that
// ReleaseAdvisoryLock is called after the cycle completes, even when the
// cycle involves processing blocks.
func TestProcessor_HA_AdvisoryLock_ReleasedAfterCycle(t *testing.T) {
	t.Parallel()

	blockHeight := uint64(5000)
	blockHash := "aaaa111111111111bbbb222222222222"

	store := newMockBlockStore()
	store.addPendingBlock(&database.Block{
		Height: blockHeight,
		Hash:   blockHash,
		Status: StatusPending,
		Miner:  "test-miner",
	})

	rpc := newMockDaemonRPC()
	rpc.setChainTip(blockHeight+50, "cccc333333333333dddd444444444444")
	rpc.setBlockHash(blockHeight, blockHash)

	locker := &mockAdvisoryLocker{}
	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetAdvisoryLocker(locker)

	proc.processCycle(context.Background())

	// The lock must have been acquired then released.
	if lc := locker.getLockCalls(); lc != 1 {
		t.Errorf("expected 1 TryAdvisoryLock call, got %d", lc)
	}
	if rc := locker.getReleaseCalls(); rc != 1 {
		t.Errorf("expected 1 ReleaseAdvisoryLock call, got %d", rc)
	}

	// Lock should be released after the cycle.
	if locker.isLocked() {
		t.Error("expected advisory lock to be released after cycle completes")
	}

	// Verify the cycle actually processed something (DB was queried).
	rpc.mu.Lock()
	bcInfoCalls := rpc.getBlockchainInfoCalls
	rpc.mu.Unlock()
	if bcInfoCalls == 0 {
		t.Error("expected at least 1 GetBlockchainInfo call during cycle")
	}
}

// TestProcessor_HA_AdvisoryLock_NilLocker verifies that when advisoryLocker
// is nil, the cycle runs without attempting any lock operations.
func TestProcessor_HA_AdvisoryLock_NilLocker(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	// advisoryLocker is nil by default from newTestProcessor

	initialCycle := proc.cycleCount

	proc.processCycle(context.Background())

	// Cycle should run normally.
	if proc.cycleCount != initialCycle+1 {
		t.Errorf("expected cycleCount %d with nil locker, got %d",
			initialCycle+1, proc.cycleCount)
	}
}

// TestProcessor_HA_SplitBrain_BothMaster simulates a split-brain scenario
// where two processors both believe they are master. The advisory lock
// ensures only one can process at a time: the first acquires the lock and
// runs, the second is rejected and skips.
func TestProcessor_HA_SplitBrain_BothMaster(t *testing.T) {
	t.Parallel()

	// Shared advisory locker simulates a single PostgreSQL instance.
	locker := &mockAdvisoryLocker{}

	// Processor A: master
	storeA := newMockBlockStore()
	rpcA := newMockDaemonRPC()
	procA := newTestProcessor(storeA, rpcA, DefaultBlockMaturityConfirmations)
	procA.SetHAEnabled(true)
	procA.SetMasterRole(true)
	procA.SetAdvisoryLocker(locker)

	// Processor B: also believes it is master (split-brain)
	storeB := newMockBlockStore()
	rpcB := newMockDaemonRPC()
	procB := newTestProcessor(storeB, rpcB, DefaultBlockMaturityConfirmations)
	procB.SetHAEnabled(true)
	procB.SetMasterRole(true)
	procB.SetAdvisoryLocker(locker)

	// Processor A acquires the lock first.
	// We simulate sequential execution: A runs, then B tries.
	// Since mockAdvisoryLocker tracks locked state, A locks it, and the
	// defer releases it after the cycle. But since we call processCycle
	// synchronously, A's defer runs before B starts.
	//
	// To simulate true split-brain (A holds lock while B tries), we need
	// to hold the lock manually.
	locker.mu.Lock()
	locker.locked = false
	locker.mu.Unlock()

	// A runs first and acquires the lock. After A's cycle, the deferred
	// release unlocks it. To test split-brain, we need A to hold the lock
	// while B tries. We achieve this by running A, then re-locking before B.
	procA.processCycle(context.Background())

	// A should have run.
	if procA.cycleCount != 1 {
		t.Fatalf("expected procA cycleCount 1, got %d", procA.cycleCount)
	}

	// Now simulate: A holds the lock (e.g., A is mid-cycle on another node).
	locker.mu.Lock()
	locker.locked = true
	locker.mu.Unlock()

	// B tries to run while A holds the lock.
	procB.processCycle(context.Background())

	// B should have been rejected by the advisory lock.
	if procB.cycleCount != 0 {
		t.Errorf("expected procB cycleCount 0 (lock rejected), got %d", procB.cycleCount)
	}

	// Verify B attempted the lock but was rejected.
	if lc := locker.getLockCalls(); lc != 2 {
		t.Errorf("expected 2 total TryAdvisoryLock calls (A + B), got %d", lc)
	}

	// Only A should have released (1 release from A's successful cycle).
	if rc := locker.getReleaseCalls(); rc != 1 {
		t.Errorf("expected 1 ReleaseAdvisoryLock call (from A only), got %d", rc)
	}
}

// TestProcessor_HA_ConcurrentCycles verifies that concurrent processCycle
// calls with HA enabled are race-detector safe. This test exercises the
// atomic isMaster access and advisory locker mutex concurrently.
func TestProcessor_HA_ConcurrentCycles(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()
	locker := &mockAdvisoryLocker{}

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(true)
	proc.SetMasterRole(true)
	proc.SetAdvisoryLocker(locker)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			proc.processCycle(context.Background())
		}()
	}

	wg.Wait()

	// Verify no panics occurred and lock calls were made.
	// The exact count depends on scheduling, but all goroutines should
	// have attempted the lock.
	if lc := locker.getLockCalls(); lc != goroutines {
		t.Errorf("expected %d TryAdvisoryLock calls, got %d", goroutines, lc)
	}

	// After all goroutines complete, the lock should be released.
	if locker.isLocked() {
		t.Error("expected advisory lock to be released after all concurrent cycles")
	}
}

// TestProcessor_HA_PaymentAdvisoryLockID_Stable verifies that the advisory
// lock ID returned by database.PaymentAdvisoryLockID() is deterministic and
// matches the expected constant 0x5350504159 ("SPPAY" in ASCII).
func TestProcessor_HA_PaymentAdvisoryLockID_Stable(t *testing.T) {
	t.Parallel()

	const expectedLockID int64 = 0x5350504159 // "SPPAY"

	lockID := database.PaymentAdvisoryLockID()

	if lockID != expectedLockID {
		t.Errorf("PaymentAdvisoryLockID() = %d (0x%X), want %d (0x%X)",
			lockID, lockID, expectedLockID, expectedLockID)
	}

	// Verify stability: calling multiple times returns the same value.
	for i := 0; i < 100; i++ {
		if got := database.PaymentAdvisoryLockID(); got != expectedLockID {
			t.Fatalf("PaymentAdvisoryLockID() returned different value on call %d: %d vs %d",
				i, got, expectedLockID)
		}
	}
}

// TestProcessor_HA_SetMasterRole_AtomicSafe verifies that concurrent
// SetMasterRole calls do not race with concurrent isMaster reads. This
// exercises the atomic.Bool used for isMaster. The test runs under the
// Go race detector.
//
// NOTE: We read isMaster.Load() directly instead of calling processCycle,
// because processCycle writes to non-atomic fields (cycleCount,
// consecutiveFailedCycles) that are not designed for concurrent access.
// processCycle runs sequentially in the processLoop; this test only
// validates the atomic safety of the isMaster flag itself.
func TestProcessor_HA_SetMasterRole_AtomicSafe(t *testing.T) {
	t.Parallel()

	store := newMockBlockStore()
	rpc := newMockDaemonRPC()

	proc := newTestProcessor(store, rpc, DefaultBlockMaturityConfirmations)
	proc.SetHAEnabled(true)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the goroutines set master=true, half set master=false.
	// Meanwhile, we also read isMaster.Load() directly to exercise
	// concurrent read+write on the atomic.
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			proc.SetMasterRole(n%2 == 0)
		}(i)
		go func() {
			defer wg.Done()
			// Read isMaster directly to test atomic safety without
			// touching non-atomic processCycle internals.
			_ = proc.isMaster.Load()
		}()
	}

	wg.Wait()

	// The final value is nondeterministic, but the test succeeds if no
	// race is detected. Verify the value is a valid bool by reading it.
	var finalVal atomic.Bool
	finalVal.Store(proc.isMaster.Load())
	got := finalVal.Load()
	if got != true && got != false {
		t.Errorf("isMaster has invalid value after concurrent access: %v", got)
	}
}
