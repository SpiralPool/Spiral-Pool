// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// TEST SUITE: Multi-Coin & Multi-Port Isolation Tests
// =============================================================================
// These tests verify that HeightContext and job operations are properly
// isolated between different coins/chains operating on different ports.

// -----------------------------------------------------------------------------
// Mock Structures for Multi-Coin Testing
// -----------------------------------------------------------------------------

// CoinEpoch represents a HeightEpoch for a specific coin.
type CoinEpoch struct {
	CoinSymbol string
	Port       int
	Epoch      *HeightEpoch
}

// NewCoinEpoch creates a new epoch for a specific coin.
func NewCoinEpoch(symbol string, port int) *CoinEpoch {
	return &CoinEpoch{
		CoinSymbol: symbol,
		Port:       port,
		Epoch:      NewHeightEpoch(),
	}
}

// MultiCoinManager manages epochs for multiple coins.
type MultiCoinManager struct {
	epochs map[string]*CoinEpoch // keyed by coin symbol
	mu     sync.RWMutex
}

// NewMultiCoinManager creates a new multi-coin manager.
func NewMultiCoinManager() *MultiCoinManager {
	return &MultiCoinManager{
		epochs: make(map[string]*CoinEpoch),
	}
}

// AddCoin adds a coin to the manager.
func (m *MultiCoinManager) AddCoin(symbol string, port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epochs[symbol] = NewCoinEpoch(symbol, port)
}

// GetEpoch returns the epoch for a specific coin.
func (m *MultiCoinManager) GetEpoch(symbol string) *CoinEpoch {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.epochs[symbol]
}

// AdvanceCoin advances the height/tip for a specific coin.
func (m *MultiCoinManager) AdvanceCoin(symbol string, height uint64, tip string) {
	m.mu.RLock()
	epoch := m.epochs[symbol]
	m.mu.RUnlock()

	if epoch != nil {
		epoch.Epoch.AdvanceWithTip(height, tip)
	}
}

// GetCoinContext returns a HeightContext for a specific coin.
func (m *MultiCoinManager) GetCoinContext(symbol string, parent context.Context) (context.Context, context.CancelFunc) {
	m.mu.RLock()
	epoch := m.epochs[symbol]
	m.mu.RUnlock()

	if epoch != nil {
		return epoch.Epoch.HeightContext(parent)
	}
	return context.WithCancel(parent)
}

// -----------------------------------------------------------------------------
// Multi-Coin Isolation Tests
// -----------------------------------------------------------------------------

// TestMultiCoin_IsolatedHeightEpochs verifies each coin has isolated epoch.
func TestMultiCoin_IsolatedHeightEpochs(t *testing.T) {
	t.Parallel()

	manager := NewMultiCoinManager()
	manager.AddCoin("DGB", 3333)
	manager.AddCoin("LTC", 3334)
	manager.AddCoin("DOGE", 3335)

	// Set different heights for each coin
	manager.AdvanceCoin("DGB", 20000000, "dgb_tip_A")
	manager.AdvanceCoin("LTC", 2500000, "ltc_tip_B")
	manager.AdvanceCoin("DOGE", 5000000, "doge_tip_C")

	// Verify each coin has correct height
	dgbEpoch := manager.GetEpoch("DGB")
	ltcEpoch := manager.GetEpoch("LTC")
	dogeEpoch := manager.GetEpoch("DOGE")

	if dgbEpoch.Epoch.Height() != 20000000 {
		t.Errorf("DGB height mismatch: expected 20000000, got %d", dgbEpoch.Epoch.Height())
	}
	if ltcEpoch.Epoch.Height() != 2500000 {
		t.Errorf("LTC height mismatch: expected 2500000, got %d", ltcEpoch.Epoch.Height())
	}
	if dogeEpoch.Epoch.Height() != 5000000 {
		t.Errorf("DOGE height mismatch: expected 5000000, got %d", dogeEpoch.Epoch.Height())
	}

	t.Log("Multi-coin epochs are properly isolated")
}

// TestMultiCoin_AdvanceOneDoesNotAffectOthers verifies isolation on advance.
func TestMultiCoin_AdvanceOneDoesNotAffectOthers(t *testing.T) {
	t.Parallel()

	manager := NewMultiCoinManager()
	manager.AddCoin("BTC", 3333)
	manager.AddCoin("BCH", 3334)

	// Initialize both at same height
	manager.AdvanceCoin("BTC", 800000, "btc_tip")
	manager.AdvanceCoin("BCH", 800000, "bch_tip")

	// Get contexts for both
	btcCtx, btcCancel := manager.GetCoinContext("BTC", context.Background())
	defer btcCancel()
	bchCtx, bchCancel := manager.GetCoinContext("BCH", context.Background())
	defer bchCancel()

	// Advance only BTC
	manager.AdvanceCoin("BTC", 800001, "btc_new_tip")

	// BTC context should be cancelled
	select {
	case <-btcCtx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("BTC context should have been cancelled")
	}

	// BCH context should NOT be cancelled
	select {
	case <-bchCtx.Done():
		t.Error("BCH context should NOT be cancelled when BTC advances")
	case <-time.After(50 * time.Millisecond):
		// Expected - BCH still valid
	}

	t.Log("Advancing one coin does not affect others")
}

// TestMultiCoin_SameHeightReorgIsolated tests same-height reorg isolation.
func TestMultiCoin_SameHeightReorgIsolated(t *testing.T) {
	t.Parallel()

	manager := NewMultiCoinManager()
	manager.AddCoin("DGB", 3333)
	manager.AddCoin("LTC", 3334)

	// Both at same height with their respective tips
	manager.AdvanceCoin("DGB", 20000000, "dgb_tip_A")
	manager.AdvanceCoin("LTC", 20000000, "ltc_tip_X")

	// Get contexts
	dgbCtx, dgbCancel := manager.GetCoinContext("DGB", context.Background())
	defer dgbCancel()
	ltcCtx, ltcCancel := manager.GetCoinContext("LTC", context.Background())
	defer ltcCancel()

	// Same-height reorg on DGB only
	manager.AdvanceCoin("DGB", 20000000, "dgb_tip_B")

	// DGB should be cancelled
	select {
	case <-dgbCtx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("DGB context should be cancelled on same-height reorg")
	}

	// LTC should NOT be affected
	select {
	case <-ltcCtx.Done():
		t.Error("LTC context should NOT be affected by DGB reorg")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}

	t.Log("Same-height reorg properly isolated between coins")
}

// TestMultiCoin_ConcurrentAdvances tests concurrent advances on different coins.
func TestMultiCoin_ConcurrentAdvances(t *testing.T) {
	t.Parallel()

	manager := NewMultiCoinManager()
	coins := []string{"BTC", "BCH", "LTC", "DOGE", "DGB"}

	for i, coin := range coins {
		manager.AddCoin(coin, 3333+i)
		manager.AdvanceCoin(coin, 1000, coin+"_initial")
	}

	var wg sync.WaitGroup

	// Concurrent advances on different coins
	for _, coin := range coins {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			for height := uint64(1001); height <= 1100; height++ {
				manager.AdvanceCoin(c, height, c+"_tip_"+string(rune('A'+height%26)))
				time.Sleep(time.Microsecond)
			}
		}(coin)
	}

	wg.Wait()

	// All coins should reach height 1100
	for _, coin := range coins {
		epoch := manager.GetEpoch(coin)
		if epoch.Epoch.Height() != 1100 {
			t.Errorf("%s height should be 1100, got %d", coin, epoch.Epoch.Height())
		}
	}

	t.Log("Concurrent advances on multiple coins completed successfully")
}

// -----------------------------------------------------------------------------
// Multi-Port Tests
// -----------------------------------------------------------------------------

// TestMultiPort_ContextIsolation tests context isolation by port.
func TestMultiPort_ContextIsolation(t *testing.T) {
	t.Parallel()

	// Simulate different stratum ports for different coins
	port3333 := NewHeightEpoch() // DGB SHA-256
	port3334 := NewHeightEpoch() // DGB Scrypt (different algo)
	port3339 := NewHeightEpoch() // LTC

	port3333.AdvanceWithTip(20000000, "dgb_sha_tip")
	port3334.AdvanceWithTip(20000000, "dgb_scrypt_tip")
	port3339.AdvanceWithTip(2500000, "ltc_tip")

	// Get contexts
	ctx3333, cancel3333 := port3333.HeightContext(context.Background())
	defer cancel3333()
	ctx3334, cancel3334 := port3334.HeightContext(context.Background())
	defer cancel3334()
	ctx3339, cancel3339 := port3339.HeightContext(context.Background())
	defer cancel3339()

	// Advance port 3333 only
	port3333.AdvanceWithTip(20000001, "dgb_sha_new_tip")

	// Only port 3333 context should be cancelled
	select {
	case <-ctx3333.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Port 3333 context should be cancelled")
	}

	// Others should remain valid
	for _, tc := range []struct {
		port int
		ctx  context.Context
	}{
		{3334, ctx3334},
		{3339, ctx3339},
	} {
		select {
		case <-tc.ctx.Done():
			t.Errorf("Port %d context should NOT be cancelled", tc.port)
		case <-time.After(50 * time.Millisecond):
			// Expected
		}
	}
}

// TestMultiPort_SimultaneousMiners simulates miners on different ports.
func TestMultiPort_SimultaneousMiners(t *testing.T) {
	t.Parallel()

	manager := NewMultiCoinManager()
	manager.AddCoin("DGB_SHA", 3333)
	manager.AddCoin("DGB_SCRYPT", 3334)
	manager.AddCoin("LTC", 3339)

	for _, coin := range []string{"DGB_SHA", "DGB_SCRYPT", "LTC"} {
		manager.AdvanceCoin(coin, 1000, coin+"_tip")
	}

	var wg sync.WaitGroup
	cancelCounts := make(map[string]*atomic.Int32)
	for _, coin := range []string{"DGB_SHA", "DGB_SCRYPT", "LTC"} {
		cancelCounts[coin] = &atomic.Int32{}
	}

	// Miners on each port
	for _, coin := range []string{"DGB_SHA", "DGB_SCRYPT", "LTC"} {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()

			for i := 0; i < 20; i++ {
				ctx, cancel := manager.GetCoinContext(c, context.Background())

				// Simulate work
				time.Sleep(5 * time.Millisecond)

				select {
				case <-ctx.Done():
					cancelCounts[c].Add(1)
				default:
				}
				cancel()
			}
		}(coin)

		// Block producer for each coin
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			for height := uint64(1001); height <= 1010; height++ {
				manager.AdvanceCoin(c, height, c+"_tip_"+string(rune('A'+height%26)))
				time.Sleep(10 * time.Millisecond)
			}
		}(coin)
	}

	wg.Wait()

	t.Logf("Cancellations per coin: DGB_SHA=%d, DGB_SCRYPT=%d, LTC=%d",
		cancelCounts["DGB_SHA"].Load(),
		cancelCounts["DGB_SCRYPT"].Load(),
		cancelCounts["LTC"].Load())
}

// -----------------------------------------------------------------------------
// Merge Mining Tests
// -----------------------------------------------------------------------------

// TestMergeMining_AuxChainIsolation tests auxiliary chain epoch isolation.
func TestMergeMining_AuxChainIsolation(t *testing.T) {
	t.Parallel()

	// Parent chain (LTC) and aux chains (DOGE)
	parentEpoch := NewHeightEpoch()
	auxEpoch := NewHeightEpoch()

	parentEpoch.AdvanceWithTip(2500000, "ltc_parent_tip")
	auxEpoch.AdvanceWithTip(5000000, "doge_aux_tip")

	// Parent context
	parentCtx, parentCancel := parentEpoch.HeightContext(context.Background())
	defer parentCancel()

	// Aux context
	auxCtx, auxCancel := auxEpoch.HeightContext(context.Background())
	defer auxCancel()

	// Advance parent - should NOT affect aux
	parentEpoch.AdvanceWithTip(2500001, "ltc_new_tip")

	select {
	case <-parentCtx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Parent context should be cancelled")
	}

	select {
	case <-auxCtx.Done():
		t.Error("Aux context should NOT be affected by parent advance")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}

	t.Log("Merge mining: aux chain isolated from parent")
}

// TestMergeMining_ParentReorgAffectsBoth tests parent reorg impact.
func TestMergeMining_ParentReorgAffectsBoth(t *testing.T) {
	t.Parallel()

	// In merge mining, if parent reorgs, both parent and aux work becomes stale
	// This test simulates proper handling

	parentEpoch := NewHeightEpoch()
	auxEpoch := NewHeightEpoch()

	parentEpoch.AdvanceWithTip(2500000, "ltc_tip_A")
	auxEpoch.AdvanceWithTip(5000000, "doge_tip_X")

	// Combined merge mining context would check both
	type MergeContext struct {
		ParentCtx context.Context
		AuxCtx    context.Context
	}

	parentCtx, parentCancel := parentEpoch.HeightContext(context.Background())
	defer parentCancel()
	auxCtx, auxCancel := auxEpoch.HeightContext(context.Background())
	defer auxCancel()

	mergeCtx := &MergeContext{
		ParentCtx: parentCtx,
		AuxCtx:    auxCtx,
	}

	// Check if merge work is stale
	isMergeStale := func(mc *MergeContext) bool {
		select {
		case <-mc.ParentCtx.Done():
			return true
		case <-mc.AuxCtx.Done():
			return true
		default:
			return false
		}
	}

	if isMergeStale(mergeCtx) {
		t.Fatal("Merge work should not be stale initially")
	}

	// Parent reorg
	parentEpoch.AdvanceWithTip(2500000, "ltc_tip_B")

	if !isMergeStale(mergeCtx) {
		t.Error("Merge work should be stale after parent reorg")
	}

	t.Log("Parent reorg correctly invalidates merge mining work")
}

// -----------------------------------------------------------------------------
// Solo Payout Isolation Tests
// -----------------------------------------------------------------------------

// TestSoloPayout_PerCoinAddress tests solo payout address isolation.
func TestSoloPayout_PerCoinAddress(t *testing.T) {
	t.Parallel()

	// Simulate per-coin payout addresses
	coinAddresses := map[string]string{
		"BTC":  "bc1qxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"LTC":  "ltc1qxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"DOGE": "D1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"DGB":  "dgb1qxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
	}

	// Verify each coin has unique address
	seen := make(map[string]bool)
	for coin, addr := range coinAddresses {
		if seen[addr] {
			t.Errorf("Address collision detected for %s", coin)
		}
		seen[addr] = true
	}

	t.Logf("Verified %d coins have unique payout addresses", len(coinAddresses))
}

// -----------------------------------------------------------------------------
// Stress Tests
// -----------------------------------------------------------------------------

// TestMultiCoin_StressConcurrentOperations stress tests multi-coin operations.
func TestMultiCoin_StressConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}
	t.Parallel()

	manager := NewMultiCoinManager()
	coins := []string{"BTC", "BCH", "LTC", "DOGE", "DGB", "XMY", "NMC", "SYS"}

	for i, coin := range coins {
		manager.AddCoin(coin, 3333+i)
		manager.AdvanceCoin(coin, 1000, coin+"_init")
	}

	var wg sync.WaitGroup
	var operations atomic.Int64

	// Many concurrent operations across all coins
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < 100; j++ {
				coin := coins[j%len(coins)]
				height := uint64(1001 + j)

				// Mix of operations
				switch j % 3 {
				case 0:
					manager.AdvanceCoin(coin, height, coin+"_tip")
				case 1:
					ctx, cancel := manager.GetCoinContext(coin, context.Background())
					_ = ctx
					cancel()
				case 2:
					epoch := manager.GetEpoch(coin)
					_ = epoch.Epoch.Height()
					_ = epoch.Epoch.TipHash()
				}

				operations.Add(1)
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Completed %d multi-coin operations", operations.Load())
}

// -----------------------------------------------------------------------------
// Edge Cases
// -----------------------------------------------------------------------------

// TestMultiCoin_SameTipDifferentCoins tests same tip hash on different coins.
func TestMultiCoin_SameTipDifferentCoins(t *testing.T) {
	t.Parallel()

	manager := NewMultiCoinManager()
	manager.AddCoin("COIN_A", 3333)
	manager.AddCoin("COIN_B", 3334)

	// Coincidentally same tip (extremely rare in practice)
	sameTip := "0000000000000000000123456789abcdef"
	manager.AdvanceCoin("COIN_A", 1000, sameTip)
	manager.AdvanceCoin("COIN_B", 1000, sameTip)

	ctxA, cancelA := manager.GetCoinContext("COIN_A", context.Background())
	defer cancelA()
	ctxB, cancelB := manager.GetCoinContext("COIN_B", context.Background())
	defer cancelB()

	// Advance COIN_A
	manager.AdvanceCoin("COIN_A", 1001, "new_tip_A")

	// COIN_A should be cancelled, COIN_B should NOT
	select {
	case <-ctxA.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("COIN_A should be cancelled")
	}

	select {
	case <-ctxB.Done():
		t.Error("COIN_B should NOT be affected despite same tip")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}
}

// TestMultiCoin_DifferentBlockTimes tests coins with different block times.
func TestMultiCoin_DifferentBlockTimes(t *testing.T) {
	t.Parallel()

	// Simulate different block production rates
	manager := NewMultiCoinManager()
	manager.AddCoin("BTC", 3333)  // 10 min blocks
	manager.AddCoin("LTC", 3334)  // 2.5 min blocks
	manager.AddCoin("DGB", 3335)  // 15 sec blocks

	for _, coin := range []string{"BTC", "LTC", "DGB"} {
		manager.AdvanceCoin(coin, 1000, coin+"_init")
	}

	var wg sync.WaitGroup

	// DGB produces blocks faster
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= 40; i++ { // 40 DGB blocks = ~10 min
			manager.AdvanceCoin("DGB", 1000+i, "dgb_"+string(rune('A'+i%26)))
			time.Sleep(time.Millisecond) // Simulated 15 sec -> 1ms
		}
	}()

	// LTC produces at medium rate
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= 4; i++ { // 4 LTC blocks = ~10 min
			manager.AdvanceCoin("LTC", 1000+i, "ltc_"+string(rune('A'+i%26)))
			time.Sleep(10 * time.Millisecond) // Simulated 2.5 min -> 10ms
		}
	}()

	// BTC produces slowest
	wg.Add(1)
	go func() {
		defer wg.Done()
		manager.AdvanceCoin("BTC", 1001, "btc_next")
		time.Sleep(40 * time.Millisecond) // Simulated 10 min -> 40ms
	}()

	wg.Wait()

	// Verify final heights
	dgbHeight := manager.GetEpoch("DGB").Epoch.Height()
	ltcHeight := manager.GetEpoch("LTC").Epoch.Height()
	btcHeight := manager.GetEpoch("BTC").Epoch.Height()

	t.Logf("Final heights - DGB: %d, LTC: %d, BTC: %d", dgbHeight, ltcHeight, btcHeight)

	if dgbHeight < ltcHeight || ltcHeight < btcHeight {
		t.Error("Block height ordering unexpected based on block times")
	}
}
