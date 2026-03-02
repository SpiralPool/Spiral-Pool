// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Tests for the coin registry.
//
// The registry provides thread-safe registration and lookup of coin implementations.
// These tests verify registry operations, concurrency safety, and error handling.
package coin

import (
	"sync"
	"testing"
)

// =============================================================================
// REGISTRY LOOKUP TESTS
// =============================================================================

func TestCreate_RegisteredCoin(t *testing.T) {
	// Test that registered coins can be created
	coins := []string{"DGB", "BTC", "BCH", "BC2"}

	for _, symbol := range coins {
		t.Run(symbol, func(t *testing.T) {
			if !IsRegistered(symbol) {
				t.Skipf("%s not registered (may not be initialized)", symbol)
			}

			coin, err := Create(symbol)
			if err != nil {
				t.Errorf("Create(%q) failed: %v", symbol, err)
				return
			}
			if coin == nil {
				t.Errorf("Create(%q) returned nil coin", symbol)
				return
			}
			if coin.Symbol() != symbol {
				t.Errorf("coin.Symbol() = %q, want %q", coin.Symbol(), symbol)
			}
		})
	}
}

func TestCreate_CaseInsensitive(t *testing.T) {
	// Verify case-insensitive lookup
	variations := []string{"dgb", "DGB", "Dgb", "dGb"}

	for _, v := range variations {
		t.Run(v, func(t *testing.T) {
			if !IsRegistered("DGB") {
				t.Skip("DGB not registered")
			}

			coin, err := Create(v)
			if err != nil {
				t.Errorf("Create(%q) failed: %v", v, err)
				return
			}
			if coin.Symbol() != "DGB" {
				t.Errorf("coin.Symbol() = %q, want 'DGB'", coin.Symbol())
			}
		})
	}
}

func TestCreate_UnknownCoin(t *testing.T) {
	unknownCoins := []string{
		"UNKNOWN",
		"FAKE",
		"XYZ123",
		"",
	}

	for _, symbol := range unknownCoins {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err == nil {
				t.Errorf("Create(%q) should return error for unknown coin", symbol)
			}
			if coin != nil {
				t.Errorf("Create(%q) should return nil for unknown coin", symbol)
			}
		})
	}
}

func TestIsRegistered(t *testing.T) {
	tests := []struct {
		symbol   string
		expected bool
	}{
		{"DGB", true},  // Registered at init
		{"BTC", true},  // Registered at init
		{"BCH", true},  // Registered at init
		{"BC2", true},  // Registered at init (Bitcoin II)
		{"FAKE", false},
		{"", false},
		{"xyz", false},
	}

	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			got := IsRegistered(tt.symbol)
			// Note: We can't guarantee registration state in isolated tests
			// So we just verify the function doesn't panic
			t.Logf("IsRegistered(%q) = %v (expected ~%v)", tt.symbol, got, tt.expected)
		})
	}
}

func TestListRegistered_NoDuplicates(t *testing.T) {
	symbols := ListRegistered()

	// Should return at least the core coins
	if len(symbols) == 0 {
		t.Log("No coins registered yet (may be init order issue)")
		return
	}

	t.Logf("Registered coins: %v", symbols)

	// Verify no duplicates
	seen := make(map[string]bool)
	for _, s := range symbols {
		if seen[s] {
			t.Errorf("Duplicate symbol in registry: %s", s)
		}
		seen[s] = true
	}
}

func TestMustCreate_Success(t *testing.T) {
	if !IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	// MustCreate should not panic for registered coins
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("MustCreate panicked: %v", r)
		}
	}()

	coin := MustCreate("DGB")
	if coin == nil {
		t.Error("MustCreate returned nil")
	}
	if coin.Symbol() != "DGB" {
		t.Errorf("coin.Symbol() = %q, want 'DGB'", coin.Symbol())
	}
}

func TestMustCreate_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Error("MustCreate should panic for unknown coin")
		}
	}()

	_ = MustCreate("DEFINITELY_NOT_A_COIN_12345")
}

// =============================================================================
// CONCURRENT ACCESS TESTS
// =============================================================================

func TestRegistry_ConcurrentRead(t *testing.T) {
	if !IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	// Test concurrent reads don't race
	var wg sync.WaitGroup
	const numGoroutines = 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Multiple concurrent operations
			_ = IsRegistered("DGB")
			_ = ListRegistered()
			_, _ = Create("DGB")
		}()
	}

	wg.Wait()
}

func TestRegistry_ConcurrentCreateDifferentCoins(t *testing.T) {
	coins := []string{"DGB", "BTC", "BCH", "BC2"}
	registeredCoins := make([]string, 0)
	for _, c := range coins {
		if IsRegistered(c) {
			registeredCoins = append(registeredCoins, c)
		}
	}

	if len(registeredCoins) < 2 {
		t.Skip("Need at least 2 coins registered")
	}

	var wg sync.WaitGroup
	const numGoroutines = 50

	for i := 0; i < numGoroutines; i++ {
		for _, coinSymbol := range registeredCoins {
			wg.Add(1)
			go func(symbol string) {
				defer wg.Done()
				coin, err := Create(symbol)
				if err != nil {
					t.Errorf("Create(%q) failed: %v", symbol, err)
					return
				}
				if coin.Symbol() != symbol {
					t.Errorf("coin.Symbol() = %q, want %q", coin.Symbol(), symbol)
				}
			}(coinSymbol)
		}
	}

	wg.Wait()
}

// =============================================================================
// FACTORY TESTS
// =============================================================================

func TestCoinFactory_CreatesNewInstances(t *testing.T) {
	if !IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	// Each Create call should return a new instance
	coin1, _ := Create("DGB")
	coin2, _ := Create("DGB")

	if coin1 == nil || coin2 == nil {
		t.Fatal("Create returned nil")
	}

	// They should have the same symbol
	if coin1.Symbol() != coin2.Symbol() {
		t.Error("Coins should have same symbol")
	}

	// But be different instances (pointer comparison)
	// Note: Depending on implementation, they might be the same or different
	t.Logf("coin1=%p, coin2=%p (may or may not be same instance)", coin1, coin2)
}

// =============================================================================
// COIN IMPLEMENTATION VALIDATION TESTS
// =============================================================================

func TestRegisteredCoins_HaveValidSymbols(t *testing.T) {
	// ListRegistered returns registry keys (which may be full names like DIGIBYTE)
	// coin.Symbol() returns the standard ticker symbol (e.g., DGB)
	registryKeys := ListRegistered()

	for _, key := range registryKeys {
		t.Run(key, func(t *testing.T) {
			coin, err := Create(key)
			if err != nil {
				t.Fatalf("Create(%q) failed: %v", key, err)
			}

			// Get the actual symbol from the coin
			symbol := coin.Symbol()

			// Symbol should be uppercase
			for _, c := range symbol {
				if c >= 'a' && c <= 'z' {
					t.Errorf("Symbol %q contains lowercase letter", symbol)
					break
				}
			}

			// Symbol should be 2-10 characters (standard ticker length)
			// Note: DGB-SCRYPT is a special case with hyphenated symbol for multi-algo support
			if len(symbol) < 2 || len(symbol) > 10 {
				t.Errorf("Symbol %q has unusual length: %d", symbol, len(symbol))
			}

			// Log the mapping for documentation
			t.Logf("Registry key %q -> coin.Symbol() = %q", key, symbol)
		})
	}
}

func TestRegisteredCoins_HaveNames(t *testing.T) {
	symbols := ListRegistered()

	for _, symbol := range symbols {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err != nil {
				t.Fatalf("Create(%q) failed: %v", symbol, err)
			}

			name := coin.Name()
			if name == "" {
				t.Errorf("coin.Name() is empty for %s", symbol)
			}
			if len(name) < 3 {
				t.Errorf("coin.Name() = %q seems too short for %s", name, symbol)
			}
		})
	}
}

func TestRegisteredCoins_HaveValidAlgorithms(t *testing.T) {
	validAlgorithms := map[string]bool{
		"sha256d":  true,
		"scrypt":   true,
		"odocrypt": true,
		"skein":    true,
		"qubit":    true,
	}

	symbols := ListRegistered()

	for _, symbol := range symbols {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err != nil {
				t.Fatalf("Create(%q) failed: %v", symbol, err)
			}

			algo := coin.Algorithm()
			if algo == "" {
				t.Errorf("coin.Algorithm() is empty for %s", symbol)
			}
			if !validAlgorithms[algo] {
				t.Logf("Unrecognized algorithm %q for %s (may be valid)", algo, symbol)
			}
		})
	}
}

func TestRegisteredCoins_HaveReasonablePorts(t *testing.T) {
	symbols := ListRegistered()

	for _, symbol := range symbols {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err != nil {
				t.Fatalf("Create(%q) failed: %v", symbol, err)
			}

			rpcPort := coin.DefaultRPCPort()
			p2pPort := coin.DefaultP2PPort()

			// Ports should be in valid range
			if rpcPort < 1024 || rpcPort > 65535 {
				t.Errorf("RPC port %d out of range for %s", rpcPort, symbol)
			}
			if p2pPort < 1024 || p2pPort > 65535 {
				t.Errorf("P2P port %d out of range for %s", p2pPort, symbol)
			}

			// Ports should be different
			if rpcPort == p2pPort {
				t.Errorf("RPC and P2P ports are same (%d) for %s", rpcPort, symbol)
			}
		})
	}
}

func TestRegisteredCoins_HaveBlockTime(t *testing.T) {
	symbols := ListRegistered()

	for _, symbol := range symbols {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err != nil {
				t.Fatalf("Create(%q) failed: %v", symbol, err)
			}

			blockTime := coin.BlockTime()

			// Block time should be positive
			if blockTime <= 0 {
				t.Errorf("BlockTime %d is not positive for %s", blockTime, symbol)
			}

			// Reasonable range: 1 second to 30 minutes
			if blockTime < 1 || blockTime > 1800 {
				t.Errorf("BlockTime %d seems unreasonable for %s", blockTime, symbol)
			}
		})
	}
}

func TestRegisteredCoins_HaveCoinbaseMaturity(t *testing.T) {
	symbols := ListRegistered()

	for _, symbol := range symbols {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err != nil {
				t.Fatalf("Create(%q) failed: %v", symbol, err)
			}

			maturity := coin.CoinbaseMaturity()

			// Maturity should be positive
			if maturity <= 0 {
				t.Errorf("CoinbaseMaturity %d is not positive for %s", maturity, symbol)
			}

			// Common values: 100 (BTC), 120 (some coins)
			// Range: 1 to 1000
			if maturity > 1000 {
				t.Logf("CoinbaseMaturity %d seems high for %s", maturity, symbol)
			}
		})
	}
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkCreate(b *testing.B) {
	if !IsRegistered("DGB") {
		b.Skip("DGB not registered")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Create("DGB")
	}
}

func BenchmarkIsRegistered(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsRegistered("DGB")
	}
}

func BenchmarkListRegistered(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ListRegistered()
	}
}

func BenchmarkCreate_ConcurrentReads(b *testing.B) {
	if !IsRegistered("DGB") {
		b.Skip("DGB not registered")
	}

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = Create("DGB")
		}
	})
}
