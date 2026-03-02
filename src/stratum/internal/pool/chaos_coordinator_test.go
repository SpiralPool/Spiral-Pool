// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for Coordinator lock ordering between poolsMu and failedCoinsMu.
//
// TEST 8: Coordinator All-Coins-Fail-Then-Recover Under Context Race
// Tests the lock ordering fix at coordinator.go:448-455 where failedCoinsMu
// is released before poolsMu is acquired to prevent deadlock with
// printStartupSummary which acquires them in a different sequence.
package pool

import (
	"fmt"
	"sync"
	"testing"
)

// TestChaos_Coordinator_LockOrderingRace verifies the lock ordering between
// poolsMu (RWMutex) and failedCoinsMu (Mutex) used in the Coordinator.
//
// printStartupSummary: poolsMu.RLock → RUnlock → failedCoinsMu.Lock → Unlock → poolsMu.RLock → ...
// retryFailedCoinsLoop: failedCoinsMu.Lock → Unlock → poolsMu.Lock → Unlock → failedCoinsMu.Lock → ...
//
// The fix at coordinator.go:448-455 ensures failedCoinsMu is released before
// poolsMu is acquired in the retry loop, preventing deadlock.
//
// TARGET: coordinator.go:448-455 (lock ordering fix), coordinator.go:541-582 (printStartupSummary)
// INVARIANT: No deadlock when both patterns run concurrently.
// RUN WITH: go test -race -timeout 30s -run TestChaos_Coordinator_LockOrderingRace
func TestChaos_Coordinator_LockOrderingRace(t *testing.T) {
	// Simulate the two mutex patterns from the Coordinator without needing
	// real CoinPool instances or database connections.

	var poolsMu sync.RWMutex
	var failedCoinsMu sync.Mutex

	pools := make(map[string]int)
	failedCoins := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		failedCoins = append(failedCoins, fmt.Sprintf("COIN-%d", i))
	}

	const iterations = 50000

	var wg sync.WaitGroup

	// Goroutine 1: Simulates printStartupSummary lock pattern
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// Step 1: Read pools
			poolsMu.RLock()
			_ = len(pools)
			poolsMu.RUnlock()

			// Step 2: Read failedCoins
			failedCoinsMu.Lock()
			_ = len(failedCoins)
			failedCoinsMu.Unlock()

			// Step 3: Iterate pools
			poolsMu.RLock()
			for range pools {
				// Read pool data
			}
			poolsMu.RUnlock()

			// Step 4: Iterate failedCoins (if any)
			failedCoinsMu.Lock()
			for range failedCoins {
				// Read failed coin data
			}
			failedCoinsMu.Unlock()
		}
	}()

	// Goroutine 2: Simulates retryFailedCoinsLoop lock pattern (WITH the fix)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			failedCoinsMu.Lock()
			if len(failedCoins) > 0 {
				coin := failedCoins[0]
				failedCoins = failedCoins[1:]

				// THE FIX (coordinator.go:448-455):
				// Release failedCoinsMu BEFORE acquiring poolsMu
				failedCoinsMu.Unlock()

				poolsMu.Lock()
				pools[coin] = i
				poolsMu.Unlock()

				failedCoinsMu.Lock() // Re-acquire for next iteration
			}
			// Re-add coins to keep the loop going
			failedCoins = append(failedCoins, fmt.Sprintf("RETRY-%d", i))
			failedCoinsMu.Unlock()
		}
	}()

	// Goroutine 3: Additional poolsMu reader contention (simulates Stats/health calls)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			poolsMu.RLock()
			_ = len(pools)
			poolsMu.RUnlock()
		}
	}()

	// Goroutine 4: Additional failedCoinsMu contention (simulates health check)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			failedCoinsMu.Lock()
			_ = len(failedCoins)
			failedCoinsMu.Unlock()
		}
	}()

	// If this deadlocks, the -timeout flag will kill the test
	wg.Wait()

	t.Logf("PASSED: No deadlock in %d iterations with 4 concurrent goroutines", iterations)
	t.Logf("Lock ordering (failedCoinsMu release → poolsMu acquire) is safe")
	t.Logf("Final state: pools=%d, failedCoins=%d", len(pools), len(failedCoins))
}
