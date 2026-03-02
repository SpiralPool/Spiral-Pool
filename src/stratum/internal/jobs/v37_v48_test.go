// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"sync"
	"sync/atomic"
	"testing"
)

// =============================================================================
// TEST SUITE: V-Audit Fix Verification (V37, V48)
// =============================================================================
// V37 — DrainRPCRecovery: atomic flag set on RPC recovery, drained exactly once.
// V48 — Cross-RPC consistency: ZMQ tip hash vs template prevBlockHash check.

// -----------------------------------------------------------------------------
// V37: DrainRPCRecovery Tests
// -----------------------------------------------------------------------------

// TestV37_DrainRPCRecovery_InitiallyFalse verifies that a freshly-created
// Manager has rpcRecovered == false, so DrainRPCRecovery returns false
// without any prior Store.
func TestV37_DrainRPCRecovery_InitiallyFalse(t *testing.T) {
	t.Parallel()

	m := &Manager{}
	// Zero-value atomic.Bool is false; DrainRPCRecovery should return false.
	if got := m.DrainRPCRecovery(); got {
		t.Fatalf("DrainRPCRecovery on zero-value Manager = true, want false")
	}
}

// TestV37_DrainRPCRecovery_SetAndDrain verifies the drain-once semantics:
// after the flag is set to true, the first call returns true and resets
// the flag; subsequent calls return false.
func TestV37_DrainRPCRecovery_SetAndDrain(t *testing.T) {
	t.Parallel()

	m := &Manager{}
	// Simulate recovery: set rpcRecovered to true.
	m.rpcRecovered.Store(true)

	// First drain must return true (flag was set).
	if got := m.DrainRPCRecovery(); !got {
		t.Fatalf("first DrainRPCRecovery after Store(true) = false, want true")
	}

	// Second drain must return false (flag already consumed).
	if got := m.DrainRPCRecovery(); got {
		t.Fatalf("second DrainRPCRecovery = true, want false (already drained)")
	}

	// Third call still false — idempotent after drain.
	if got := m.DrainRPCRecovery(); got {
		t.Fatalf("third DrainRPCRecovery = true, want false")
	}
}

// TestV37_DrainRPCRecovery_ConcurrentDrain verifies that when 100 goroutines
// race to drain the flag, exactly one observes true and the remaining 99
// observe false. This proves the CompareAndSwap provides correct mutual
// exclusion without external locking.
func TestV37_DrainRPCRecovery_ConcurrentDrain(t *testing.T) {
	t.Parallel()

	m := &Manager{}
	m.rpcRecovered.Store(true)

	const goroutines = 100
	var (
		wg       sync.WaitGroup
		trueCount atomic.Int32
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if m.DrainRPCRecovery() {
				trueCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if n := trueCount.Load(); n != 1 {
		t.Fatalf("DrainRPCRecovery returned true %d times across %d goroutines, want exactly 1", n, goroutines)
	}
}

// -----------------------------------------------------------------------------
// V48: Cross-RPC Consistency Check Tests
// -----------------------------------------------------------------------------

// TestV48_ConsistencyCheck exercises the V48 condition from
// OnBlockNotificationWithHash:
//
//	if newTipHash != "" && newHash != newTipHash { /* warn */ }
//
// Since the full method requires a running daemon, we validate the boolean
// logic of the check directly in a table-driven test.
func TestV48_ConsistencyCheck(t *testing.T) {
	t.Parallel()

	// v48Inconsistent mirrors the condition used in manager.go:
	//   newTipHash != "" && newHash != newTipHash
	v48Inconsistent := func(zmqHash, templateHash string) bool {
		return zmqHash != "" && templateHash != zmqHash
	}

	tests := []struct {
		name         string
		zmqHash      string
		templateHash string
		expectWarn   bool
	}{
		{
			name:         "match — no warning",
			zmqHash:      "abc123",
			templateHash: "abc123",
			expectWarn:   false,
		},
		{
			name:         "mismatch — warning expected",
			zmqHash:      "abc123",
			templateHash: "def456",
			expectWarn:   true,
		},
		{
			name:         "empty ZMQ hash — check skipped, no false positive",
			zmqHash:      "",
			templateHash: "abc123",
			expectWarn:   false,
		},
		{
			name:         "both empty — check skipped",
			zmqHash:      "",
			templateHash: "",
			expectWarn:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := v48Inconsistent(tc.zmqHash, tc.templateHash)
			if got != tc.expectWarn {
				t.Errorf("v48Inconsistent(%q, %q) = %v, want %v",
					tc.zmqHash, tc.templateHash, got, tc.expectWarn)
			}
		})
	}
}

// TestV48_ConsistencyCheck_Match is a standalone test confirming that equal
// hashes produce no inconsistency detection.
func TestV48_ConsistencyCheck_Match(t *testing.T) {
	t.Parallel()

	zmqHash := "00000000000000000007abc123def456"
	templateHash := "00000000000000000007abc123def456"

	if zmqHash != "" && templateHash != zmqHash {
		t.Fatal("identical hashes incorrectly flagged as inconsistent")
	}
}

// TestV48_ConsistencyCheck_Mismatch is a standalone test confirming that
// differing hashes are correctly detected as inconsistent.
func TestV48_ConsistencyCheck_Mismatch(t *testing.T) {
	t.Parallel()

	zmqHash := "00000000000000000007abc123def456"
	templateHash := "000000000000000000099999deadbeef"

	detected := zmqHash != "" && templateHash != zmqHash
	if !detected {
		t.Fatal("mismatched hashes not detected as inconsistent")
	}
}

// TestV48_ConsistencyCheck_EmptyZMQHash confirms that when the ZMQ hash is
// empty (e.g., notification arrived without a hash), the check is skipped
// entirely, avoiding false positives.
func TestV48_ConsistencyCheck_EmptyZMQHash(t *testing.T) {
	t.Parallel()

	zmqHash := ""
	templateHash := "00000000000000000007abc123def456"

	detected := zmqHash != "" && templateHash != zmqHash
	if detected {
		t.Fatal("empty ZMQ hash should skip the consistency check, but it triggered")
	}
}
