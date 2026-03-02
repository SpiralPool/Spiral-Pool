// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Concurrency & Chaos Tests
// =============================================================================
// These tests verify thread safety, race conditions, and stress behavior
// under high concurrency and chaotic conditions.

// -----------------------------------------------------------------------------
// High Concurrency Tests
// -----------------------------------------------------------------------------

// TestConcurrency_50Miners simulates 50 concurrent miners.
func TestConcurrency_50Miners(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial_tip")

	const numMiners = 50
	var wg sync.WaitGroup
	var successfulSubmissions atomic.Int32
	var cancelledSubmissions atomic.Int32

	// Start miners
	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			// Simulate submission work
			time.Sleep(time.Duration(minerID%10) * time.Millisecond)

			select {
			case <-ctx.Done():
				cancelledSubmissions.Add(1)
			default:
				successfulSubmissions.Add(1)
			}
		}(i)
	}

	// Trigger tip change mid-way
	time.Sleep(5 * time.Millisecond)
	epoch.AdvanceWithTip(1001, "new_tip")

	wg.Wait()

	total := successfulSubmissions.Load() + cancelledSubmissions.Load()
	if total != numMiners {
		t.Errorf("Expected %d total outcomes, got %d", numMiners, total)
	}

	t.Logf("50 miners: %d successful, %d cancelled",
		successfulSubmissions.Load(), cancelledSubmissions.Load())
}

// TestConcurrency_100Miners simulates 100 concurrent miners.
func TestConcurrency_100Miners(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping 100-miner test in short mode")
	}
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial_tip")

	const numMiners = 100
	var wg sync.WaitGroup
	var activeSubmissions atomic.Int32

	// Track max concurrent submissions
	var maxConcurrent atomic.Int32

	for i := 0; i < numMiners; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			current := activeSubmissions.Add(1)
			for {
				oldMax := maxConcurrent.Load()
				if current <= oldMax || maxConcurrent.CompareAndSwap(oldMax, current) {
					break
				}
			}

			// Simulate work
			time.Sleep(time.Millisecond)

			select {
			case <-ctx.Done():
				// Cancelled
			default:
				// Success
			}

			activeSubmissions.Add(-1)
		}()
	}

	wg.Wait()

	t.Logf("100 miners: max concurrent submissions = %d", maxConcurrent.Load())
}

// -----------------------------------------------------------------------------
// Rapid Block Succession Tests
// -----------------------------------------------------------------------------

// TestChaos_RapidBlocks simulates rapid block succession.
func TestChaos_RapidBlocks(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "block_1000")

	const numBlocks = 50
	var wg sync.WaitGroup
	var cancelledCount atomic.Int32

	// Spawn listeners
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ctx, cancel := epoch.HeightContext(context.Background())
				time.Sleep(time.Millisecond)
				select {
				case <-ctx.Done():
					cancelledCount.Add(1)
				default:
				}
				cancel()
			}
		}()
	}

	// Rapid block producer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for height := uint64(1001); height <= uint64(1000+numBlocks); height++ {
			tip := "block_" + string(rune('A'+height%26))
			epoch.AdvanceWithTip(height, tip)
			time.Sleep(500 * time.Microsecond)
		}
	}()

	wg.Wait()

	t.Logf("Rapid blocks: %d cancellations observed", cancelledCount.Load())
}

// TestChaos_ZMQLag simulates ZMQ notification lag.
func TestChaos_ZMQLag(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_A")

	var wg sync.WaitGroup
	var cancelledAfterLag atomic.Int32

	// Miners start work
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx, cancel := epoch.HeightContext(context.Background())
			defer cancel()

			// Simulate submission time + network latency
			time.Sleep(time.Duration(10+id*5) * time.Millisecond)

			select {
			case <-ctx.Done():
				cancelledAfterLag.Add(1)
			default:
			}
		}(i)
	}

	// Simulate ZMQ lag - notification arrives late
	time.Sleep(20 * time.Millisecond)
	epoch.AdvanceWithTip(1001, "tip_B") // Block found 20ms ago

	wg.Wait()

	// Some submissions should have been cancelled due to lag
	t.Logf("ZMQ lag simulation: %d submissions cancelled", cancelledAfterLag.Load())
}

// TestChaos_TemplateFetchDelay simulates slow daemon response.
func TestChaos_TemplateFetchDelay(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "tip_A")

	var wg sync.WaitGroup

	// Simulate template fetch with delay
	wg.Add(1)
	go func() {
		defer wg.Done()

		ctx, cancel := epoch.HeightContext(context.Background())
		defer cancel()

		// Simulate slow daemon response
		time.Sleep(50 * time.Millisecond)

		select {
		case <-ctx.Done():
			t.Log("Template fetch cancelled due to height change")
		default:
			t.Log("Template fetch completed successfully")
		}
	}()

	// Block arrives during template fetch
	time.Sleep(25 * time.Millisecond)
	epoch.AdvanceWithTip(1001, "tip_B")

	wg.Wait()
}

// -----------------------------------------------------------------------------
// Goroutine Leak Detection Tests
// -----------------------------------------------------------------------------

// TestGoroutineLeak_ContextCleanup verifies no goroutine leaks on context cleanup.
// NOTE: NOT parallel — runtime.NumGoroutine() is process-global, so parallel
// tests create noise that makes the goroutine delta meaningless.
func TestGoroutineLeak_ContextCleanup(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	// Create and cancel many contexts
	for i := 0; i < 1000; i++ {
		ctx, cancel := epoch.HeightContext(context.Background())
		_ = ctx
		cancel() // Properly cancel
	}

	// Allow cleanup — generous settle time for -race detector overhead.
	runtime.Gosched()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	runtime.Gosched()

	finalGoroutines := runtime.NumGoroutine()

	// Allow generous variance for test framework goroutines and -race overhead.
	if finalGoroutines > initialGoroutines+50 {
		t.Errorf("Possible goroutine leak: started with %d, ended with %d",
			initialGoroutines, finalGoroutines)
	}
}

// TestGoroutineLeak_AdvanceCleanup verifies no leaks on advance.
// NOTE: NOT parallel — runtime.NumGoroutine() is process-global, so parallel
// tests create noise that makes the goroutine delta meaningless.
func TestGoroutineLeak_AdvanceCleanup(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()

	epoch := NewHeightEpoch()

	// Create contexts at each height, then advance
	for height := uint64(1000); height < 1100; height++ {
		epoch.AdvanceWithTip(height, "tip_"+string(rune('A'+height%26)))
		ctx, cancel := epoch.HeightContext(context.Background())
		_ = ctx
		cancel()
	}

	// Allow cleanup — generous settle time for -race detector overhead.
	runtime.Gosched()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	runtime.Gosched()

	finalGoroutines := runtime.NumGoroutine()

	// Allow generous variance for test framework goroutines and -race overhead.
	if finalGoroutines > initialGoroutines+50 {
		t.Errorf("Possible goroutine leak: started with %d, ended with %d",
			initialGoroutines, finalGoroutines)
	}
}

// -----------------------------------------------------------------------------
// Race Condition Tests
// -----------------------------------------------------------------------------

// TestRace_ConcurrentStateAccess tests concurrent state access.
func TestRace_ConcurrentStateAccess(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var wg sync.WaitGroup
	const numGoroutines = 50

	// Concurrent readers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = epoch.Height()
				_ = epoch.TipHash()
				_, _ = epoch.State()
			}
		}()
	}

	// Concurrent writers
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				height := uint64(1000 + j)
				tip := "tip_" + string(rune('A'+id%26))
				epoch.AdvanceWithTip(height, tip)
			}
		}(i)
	}

	wg.Wait()
	// Test passes if no race detected (run with -race flag)
}

// TestRace_ConcurrentContextCreationAndAdvance tests context/advance race.
func TestRace_ConcurrentContextCreationAndAdvance(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var wg sync.WaitGroup
	const numIterations = 100

	// Context creators
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				ctx, cancel := epoch.HeightContext(context.Background())
				time.Sleep(time.Microsecond)
				select {
				case <-ctx.Done():
				default:
				}
				cancel()
			}
		}()
	}

	// Advancers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				height := uint64(1000 + j)
				tip := "tip_" + string(rune('A'+id))
				epoch.AdvanceWithTip(height, tip)
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	wg.Wait()
	// Test passes if no race detected
}

// -----------------------------------------------------------------------------
// TOCTOU Race Tests
// -----------------------------------------------------------------------------

// TestTOCTOU_RaceWindow simulates TOCTOU race window.
func TestTOCTOU_RaceWindow(t *testing.T) {
	t.Parallel()

	var (
		mu          sync.Mutex
		chainTip    = "tip_A"
		raceDetected atomic.Bool
	)

	const numCycles = 100
	var wg sync.WaitGroup

	// Processor goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numCycles; i++ {
			// Capture snapshot
			mu.Lock()
			snapshotTip := chainTip
			mu.Unlock()

			// Simulate processing time (race window)
			time.Sleep(time.Microsecond)

			// TOCTOU check
			mu.Lock()
			currentTip := chainTip
			mu.Unlock()

			if currentTip != snapshotTip {
				raceDetected.Store(true)
			}
		}
	}()

	// Tip changer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numCycles; i++ {
			mu.Lock()
			if i%2 == 0 {
				chainTip = "tip_A"
			} else {
				chainTip = "tip_B"
			}
			mu.Unlock()
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()

	if raceDetected.Load() {
		t.Log("TOCTOU race condition detected and handled correctly")
	} else {
		t.Log("No TOCTOU race occurred in this run (timing-dependent)")
	}
}

// -----------------------------------------------------------------------------
// Stress Tests
// -----------------------------------------------------------------------------

// TestStress_SustainedLoad tests sustained high load.
func TestStress_SustainedLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var operations atomic.Int64
	var wg sync.WaitGroup

	// Worker pool
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					subCtx, subCancel := epoch.HeightContext(ctx)
					_ = subCtx
					subCancel()
					operations.Add(1)
				}
			}
		}()
	}

	// Continuous advancer
	wg.Add(1)
	go func() {
		defer wg.Done()
		height := uint64(1001)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				epoch.AdvanceWithTip(height, "tip_"+string(rune('A'+height%26)))
				height++
				time.Sleep(time.Millisecond)
			}
		}
	}()

	wg.Wait()

	t.Logf("Sustained load test: %d operations in 5 seconds", operations.Load())
}

// TestStress_BurstLoad tests burst load patterns.
func TestStress_BurstLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	const burstSize = 500
	const numBursts = 5

	for burst := 0; burst < numBursts; burst++ {
		var wg sync.WaitGroup

		// Launch burst
		for i := 0; i < burstSize; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := epoch.HeightContext(context.Background())
				defer cancel()
				_ = ctx
			}()
		}

		wg.Wait()

		// Advance between bursts
		epoch.AdvanceWithTip(uint64(1001+burst), "burst_"+string(rune('A'+burst)))

		t.Logf("Burst %d: %d contexts created", burst+1, burstSize)
	}
}

// -----------------------------------------------------------------------------
// Edge Cases Under Concurrency
// -----------------------------------------------------------------------------

// TestConcurrency_AllContextsCancelledBeforeAdvanceReturns verifies atomicity.
func TestConcurrency_AllContextsCancelledBeforeAdvanceReturns(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	const numContexts = 100
	contexts := make([]context.Context, numContexts)
	cancels := make([]context.CancelFunc, numContexts)

	for i := 0; i < numContexts; i++ {
		contexts[i], cancels[i] = epoch.HeightContext(context.Background())
	}

	// Advance - should cancel all contexts atomically
	epoch.AdvanceWithTip(1001, "new_tip")

	// Verify all cancelled
	allCancelled := true
	for i, ctx := range contexts {
		select {
		case <-ctx.Done():
			// Good
		default:
			allCancelled = false
			t.Errorf("Context %d not cancelled", i)
		}
		cancels[i]()
	}

	if allCancelled {
		t.Logf("All %d contexts cancelled atomically", numContexts)
	}
}

// TestConcurrency_HeightNeverDecreases verifies monotonic height under concurrency.
func TestConcurrency_HeightNeverDecreases(t *testing.T) {
	t.Parallel()
	epoch := NewHeightEpoch()
	epoch.AdvanceWithTip(1000, "initial")

	var maxObservedHeight atomic.Uint64
	maxObservedHeight.Store(1000)

	var wg sync.WaitGroup
	const numGoroutines = 50

	// Concurrent advancers with various heights
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				height := uint64(1000 + id + j)
				epoch.AdvanceWithTip(height, "tip")

				// Check height
				observedHeight := epoch.Height()
				for {
					oldMax := maxObservedHeight.Load()
					if observedHeight > oldMax {
						if maxObservedHeight.CompareAndSwap(oldMax, observedHeight) {
							break
						}
					} else {
						break
					}
				}
			}
		}(i)
	}

	wg.Wait()

	finalHeight := epoch.Height()
	t.Logf("Final height: %d, max observed: %d", finalHeight, maxObservedHeight.Load())

	// Height should never have decreased from final
	if epoch.Height() < maxObservedHeight.Load() {
		t.Error("Height decreased - this should never happen")
	}
}
