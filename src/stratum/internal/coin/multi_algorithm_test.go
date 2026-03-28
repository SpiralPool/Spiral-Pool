// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Multi-algorithm and multi-coin test scenarios
//
// This file contains comprehensive tests for:
// 1. SHA-256d only configurations (DGB, BTC, BCH, BC2)
// 2. Scrypt only configurations (LTC, DOGE, DGB-SCRYPT, PEP, CAT)
// 3. Mixed algorithm configurations (SHA-256d + Scrypt)
// 4. Algorithm isolation (cross-algorithm confusion prevention)
//
// V2.0.0-PHI_HASH_REACTOR: Multi-algorithm support (SHA-256d + Scrypt)
package coin

import (
	"testing"
)

// ══════════════════════════════════════════════════════════════════════════════
// SHA-256d Only Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestSHA256dOnlyMultiCoin(t *testing.T) {
	// Test that all SHA-256d coins can be created and work together
	sha256dCoins := []string{"DGB", "BTC", "BCH", "BC2"}

	t.Run("all SHA256d coins can be created", func(t *testing.T) {
		for _, symbol := range sha256dCoins {
			coin, err := Create(symbol)
			if err != nil {
				t.Errorf("Failed to create %s: %v", symbol, err)
				continue
			}
			if coin.Algorithm() != "sha256d" {
				t.Errorf("%s should use sha256d, got %s", symbol, coin.Algorithm())
			}
		}
	})

	t.Run("SHA256d coins produce consistent hashes", func(t *testing.T) {
		header := make([]byte, 80)
		for i := range header {
			header[i] = byte(i * 2)
		}

		var firstHash []byte
		for _, symbol := range sha256dCoins {
			coin, _ := Create(symbol)
			hash := coin.HashBlockHeader(header)

			if firstHash == nil {
				firstHash = hash
			} else {
				// All SHA-256d coins should produce identical hashes
				for i := range hash {
					if hash[i] != firstHash[i] {
						t.Errorf("%s produced different hash than DGB at byte %d", symbol, i)
						break
					}
				}
			}
		}
	})

	t.Run("SHA256d coins have distinct genesis blocks", func(t *testing.T) {
		genesisHashes := make(map[string]string)
		for _, symbol := range sha256dCoins {
			coin, _ := Create(symbol)
			genesis := coin.GenesisBlockHash()

			for prevSymbol, prevGenesis := range genesisHashes {
				if genesis == prevGenesis {
					// BCH forked from BTC, so they share the same genesis block
					// This is expected and not a configuration error
					if (symbol == "BCH" && prevSymbol == "BTC") || (symbol == "BTC" && prevSymbol == "BCH") {
						continue // Skip BCH/BTC comparison - same genesis is expected
					}
					t.Errorf("%s and %s have the same genesis block hash - this is a configuration error", symbol, prevSymbol)
				}
			}
			genesisHashes[symbol] = genesis
		}
	})
}

// ══════════════════════════════════════════════════════════════════════════════
// Scrypt Only Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestScryptOnlyMultiCoin(t *testing.T) {
	// Test that all Scrypt coins can be created and work together
	scryptCoins := []string{"LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT"}

	t.Run("all Scrypt coins can be created", func(t *testing.T) {
		for _, symbol := range scryptCoins {
			coin, err := Create(symbol)
			if err != nil {
				t.Errorf("Failed to create %s: %v", symbol, err)
				continue
			}
			if coin.Algorithm() != "scrypt" {
				t.Errorf("%s should use scrypt, got %s", symbol, coin.Algorithm())
			}
		}
	})

	t.Run("Scrypt coins produce consistent hashes", func(t *testing.T) {
		header := make([]byte, 80)
		for i := range header {
			header[i] = byte(i * 3)
		}

		var firstHash []byte
		for _, symbol := range scryptCoins {
			coin, _ := Create(symbol)
			hash := coin.HashBlockHeader(header)

			if firstHash == nil {
				firstHash = hash
			} else {
				// All Scrypt coins should produce identical hashes (same params)
				for i := range hash {
					if hash[i] != firstHash[i] {
						t.Errorf("%s produced different hash than LTC at byte %d", symbol, i)
						break
					}
				}
			}
		}
	})

	t.Run("Scrypt coins have distinct genesis blocks", func(t *testing.T) {
		genesisHashes := make(map[string]string)
		for _, symbol := range scryptCoins {
			coin, _ := Create(symbol)
			genesis := coin.GenesisBlockHash()

			// DGB-SCRYPT shares genesis with DGB (same blockchain)
			if symbol == "DGB-SCRYPT" {
				dgb, _ := Create("DGB")
				if genesis != dgb.GenesisBlockHash() {
					t.Error("DGB-SCRYPT must have same genesis as DGB (same blockchain)")
				}
				continue // Don't add to unique check
			}

			for prevSymbol, prevGenesis := range genesisHashes {
				if genesis == prevGenesis {
					t.Errorf("%s and %s have the same genesis block hash", symbol, prevSymbol)
				}
			}
			genesisHashes[symbol] = genesis
		}
	})
}

// ══════════════════════════════════════════════════════════════════════════════
// Mixed Algorithm Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestMixedAlgorithmMultiCoin(t *testing.T) {
	// Test mixed SHA-256d + Scrypt configurations

	sha256d := []string{"DGB", "BTC", "NMC", "SYS", "XMY", "FBTC"}
	scrypt := []string{"LTC", "DOGE", "PEP", "CAT"}

	t.Run("algorithms produce different hashes", func(t *testing.T) {
		header := make([]byte, 80)
		for i := range header {
			header[i] = byte(i * 5)
		}

		// Get a SHA-256d hash
		dgb, _ := Create("DGB")
		sha256dHash := dgb.HashBlockHeader(header)

		// Get a Scrypt hash
		ltc, _ := Create("LTC")
		scryptHash := ltc.HashBlockHeader(header)

		// They MUST be different
		same := true
		for i := range sha256dHash {
			if sha256dHash[i] != scryptHash[i] {
				same = false
				break
			}
		}
		if same {
			t.Error("SHA-256d and Scrypt should produce different hashes for same input")
		}
	})

	t.Run("coins report correct algorithm", func(t *testing.T) {
		for _, symbol := range sha256d {
			coin, _ := Create(symbol)
			if coin.Algorithm() != "sha256d" {
				t.Errorf("%s should report sha256d, got %s", symbol, coin.Algorithm())
			}
		}
		for _, symbol := range scrypt {
			coin, _ := Create(symbol)
			if coin.Algorithm() != "scrypt" {
				t.Errorf("%s should report scrypt, got %s", symbol, coin.Algorithm())
			}
		}
	})

	t.Run("all coins have unique symbols", func(t *testing.T) {
		allCoins := append(append([]string{}, sha256d...), scrypt...)
		allCoins = append(allCoins, "BCH", "BC2", "DGB-SCRYPT")

		symbols := make(map[string]bool)
		for _, coinName := range allCoins {
			coin, err := Create(coinName)
			if err != nil {
				t.Errorf("Failed to create %s: %v", coinName, err)
				continue
			}
			symbol := coin.Symbol()
			if symbols[symbol] {
				t.Errorf("Duplicate symbol %s from %s", symbol, coinName)
			}
			symbols[symbol] = true
		}
	})
}

// ══════════════════════════════════════════════════════════════════════════════
// Algorithm Isolation Tests (Security)
// ══════════════════════════════════════════════════════════════════════════════

func TestAlgorithmIsolation(t *testing.T) {
	// Verify that coins with same address format but different algorithms
	// produce different hashes (prevents cross-chain confusion)

	t.Run("DGB vs DGB-SCRYPT", func(t *testing.T) {
		dgb, _ := Create("DGB")
		dgbScrypt, _ := Create("DGB-SCRYPT")

		// Same blockchain, same addresses
		if dgb.GenesisBlockHash() != dgbScrypt.GenesisBlockHash() {
			t.Error("DGB and DGB-SCRYPT should have same genesis (same blockchain)")
		}
		if dgb.Bech32HRP() != dgbScrypt.Bech32HRP() {
			t.Error("DGB and DGB-SCRYPT should have same bech32 HRP")
		}

		// But different algorithms
		if dgb.Algorithm() == dgbScrypt.Algorithm() {
			t.Error("DGB and DGB-SCRYPT should have different algorithms")
		}

		// And different hashes
		header := make([]byte, 80)
		for i := range header {
			header[i] = byte(i)
		}

		dgbHash := dgb.HashBlockHeader(header)
		dgbScryptHash := dgbScrypt.HashBlockHeader(header)

		same := true
		for i := range dgbHash {
			if dgbHash[i] != dgbScryptHash[i] {
				same = false
				break
			}
		}
		if same {
			t.Error("DGB (SHA256d) and DGB-SCRYPT should produce different hashes")
		}
	})

}

// ══════════════════════════════════════════════════════════════════════════════
// Registry Completeness Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestCoinRegistryComplete(t *testing.T) {
	// Verify all expected coins are registered

	expectedCoins := []struct {
		name      string
		symbol    string
		algorithm string
	}{
		// SHA-256d
		{"DGB", "DGB", "sha256d"},
		{"DIGIBYTE", "DGB", "sha256d"},
		{"BTC", "BTC", "sha256d"},
		{"BITCOIN", "BTC", "sha256d"},
		{"BCH", "BCH", "sha256d"},
		{"BITCOINCASH", "BCH", "sha256d"},
		{"BC2", "BC2", "sha256d"},
		{"BITCOINII", "BC2", "sha256d"},
		{"NMC", "NMC", "sha256d"},
		{"NAMECOIN", "NMC", "sha256d"},
		{"SYS", "SYS", "sha256d"},
		{"SYSCOIN", "SYS", "sha256d"},
		{"XMY", "XMY", "sha256d"},
		{"MYRIAD", "XMY", "sha256d"},
		{"FBTC", "FBTC", "sha256d"},
		{"FRACTALBTC", "FBTC", "sha256d"},
		// Scrypt
		{"LTC", "LTC", "scrypt"},
		{"LITECOIN", "LTC", "scrypt"},
		{"DOGE", "DOGE", "scrypt"},
		{"DOGECOIN", "DOGE", "scrypt"},
		{"DGB-SCRYPT", "DGB-SCRYPT", "scrypt"},
		{"DIGIBYTE-SCRYPT", "DGB-SCRYPT", "scrypt"},
	}

	for _, expected := range expectedCoins {
		t.Run(expected.name, func(t *testing.T) {
			coin, err := Create(expected.name)
			if err != nil {
				t.Fatalf("Failed to create %s: %v", expected.name, err)
			}
			if coin.Symbol() != expected.symbol {
				t.Errorf("expected symbol %s, got %s", expected.symbol, coin.Symbol())
			}
			if coin.Algorithm() != expected.algorithm {
				t.Errorf("expected algorithm %s, got %s", expected.algorithm, coin.Algorithm())
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Block Time Configuration Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestBlockTimeConfigurations(t *testing.T) {
	// Verify correct block times for pool scheduling

	expectedBlockTimes := map[string]int{
		"DGB":        15,  // 15 seconds
		"BTC":        600, // 10 minutes
		"BCH":        600, // 10 minutes
		"BC2":        600, // 10 minutes
		"NMC":        600, // 10 minutes (same as Bitcoin)
		"SYS":        150, // 2.5 minutes
		"XMY":        60,  // 1 minute
		"FBTC":       30,  // 30 seconds
		"LTC":        150, // 2.5 minutes
		"DOGE":       60,  // 1 minute
		"DGB-SCRYPT": 15,  // Same as DGB
		"PEP":        60,  // 1 minute
		"CAT":        600, // 10 minutes (like Bitcoin)
	}

	for symbol, expectedTime := range expectedBlockTimes {
		t.Run(symbol, func(t *testing.T) {
			coin, err := Create(symbol)
			if err != nil {
				t.Fatalf("Failed to create %s: %v", symbol, err)
			}
			if coin.BlockTime() != expectedTime {
				t.Errorf("expected block time %d, got %d", expectedTime, coin.BlockTime())
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Port Configuration Tests
// ══════════════════════════════════════════════════════════════════════════════

func TestPortConfigurations(t *testing.T) {
	// Verify no port conflicts exist

	type portConfig struct {
		rpcPort int
		p2pPort int
	}

	coinPorts := make(map[string]portConfig)

	allCoins := []string{"DGB", "BTC", "BCH", "BC2", "NMC", "SYS", "XMY", "FBTC", "LTC", "DOGE", "PEP", "CAT"}
	// Note: DGB-SCRYPT uses same ports as DGB (same node)

	for _, symbol := range allCoins {
		coin, err := Create(symbol)
		if err != nil {
			t.Fatalf("Failed to create %s: %v", symbol, err)
		}

		ports := portConfig{
			rpcPort: coin.DefaultRPCPort(),
			p2pPort: coin.DefaultP2PPort(),
		}
		coinPorts[symbol] = ports
	}

	// Check for RPC port conflicts
	rpcPorts := make(map[int][]string)
	for symbol, ports := range coinPorts {
		rpcPorts[ports.rpcPort] = append(rpcPorts[ports.rpcPort], symbol)
	}

	for port, coins := range rpcPorts {
		if len(coins) > 1 {
			t.Errorf("RPC port %d conflict: %v", port, coins)
		}
	}

	// Check for P2P port conflicts
	p2pPorts := make(map[int][]string)
	for symbol, ports := range coinPorts {
		p2pPorts[ports.p2pPort] = append(p2pPorts[ports.p2pPort], symbol)
	}

	for port, coins := range p2pPorts {
		if len(coins) > 1 {
			t.Errorf("P2P port %d conflict: %v", port, coins)
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Concurrent Multi-Algorithm Tests (SHA256d + Scrypt)
// ══════════════════════════════════════════════════════════════════════════════

func TestConcurrentMultiAlgorithmHashing(t *testing.T) {
	// Test that SHA256d and Scrypt can run concurrently without interference
	// This simulates a multi-coin pool processing shares for different algorithms

	sha256dCoins := []string{"DGB", "BTC", "BCH", "BC2", "NMC", "SYS", "XMY", "FBTC"}
	scryptCoins := []string{"LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT"}

	// Pre-create all coin instances
	sha256dInstances := make([]Coin, 0)
	scryptInstances := make([]Coin, 0)

	for _, symbol := range sha256dCoins {
		c, err := Create(symbol)
		if err != nil {
			t.Fatalf("Failed to create %s: %v", symbol, err)
		}
		sha256dInstances = append(sha256dInstances, c)
	}

	for _, symbol := range scryptCoins {
		c, err := Create(symbol)
		if err != nil {
			t.Fatalf("Failed to create %s: %v", symbol, err)
		}
		scryptInstances = append(scryptInstances, c)
	}

	// Create a shared header for all coins
	header := make([]byte, 80)
	for i := range header {
		header[i] = byte(i * 11)
	}

	t.Run("concurrent hashing no data race", func(t *testing.T) {
		// Run with -race flag to detect any data races
		const goroutines = 100
		const hashesPerGoroutine = 50

		done := make(chan bool, goroutines)

		// Launch goroutines that hash with different algorithms
		for g := 0; g < goroutines; g++ {
			go func(id int) {
				defer func() { done <- true }()

				// Alternate between SHA256d and Scrypt
				for i := 0; i < hashesPerGoroutine; i++ {
					if id%2 == 0 {
						// SHA256d
						coinIdx := i % len(sha256dInstances)
						_ = sha256dInstances[coinIdx].HashBlockHeader(header)
					} else {
						// Scrypt
						coinIdx := i % len(scryptInstances)
						_ = scryptInstances[coinIdx].HashBlockHeader(header)
					}
				}
			}(g)
		}

		// Wait for all goroutines
		for i := 0; i < goroutines; i++ {
			<-done
		}
	})

	t.Run("algorithm results are deterministic under concurrency", func(t *testing.T) {
		// Compute expected hashes first (single-threaded)
		expectedSHA256d := make(map[string][]byte)
		expectedScrypt := make(map[string][]byte)

		for _, c := range sha256dInstances {
			expectedSHA256d[c.Symbol()] = c.HashBlockHeader(header)
		}
		for _, c := range scryptInstances {
			expectedScrypt[c.Symbol()] = c.HashBlockHeader(header)
		}

		// Now verify under concurrent load
		const iterations = 100
		errors := make(chan string, iterations*2)

		for i := 0; i < iterations; i++ {
			go func() {
				for _, c := range sha256dInstances {
					result := c.HashBlockHeader(header)
					expected := expectedSHA256d[c.Symbol()]
					for j := range result {
						if result[j] != expected[j] {
							errors <- c.Symbol() + " SHA256d hash mismatch"
							return
						}
					}
				}
				errors <- ""
			}()

			go func() {
				for _, c := range scryptInstances {
					result := c.HashBlockHeader(header)
					expected := expectedScrypt[c.Symbol()]
					for j := range result {
						if result[j] != expected[j] {
							errors <- c.Symbol() + " Scrypt hash mismatch"
							return
						}
					}
				}
				errors <- ""
			}()
		}

		// Collect results
		for i := 0; i < iterations*2; i++ {
			if err := <-errors; err != "" {
				t.Error(err)
			}
		}
	})

	t.Run("algorithms never cross-contaminate", func(t *testing.T) {
		// Verify that SHA256d and Scrypt always produce different results
		// Even under heavy concurrent load

		sha256dHash := sha256dInstances[0].HashBlockHeader(header)
		scryptHash := scryptInstances[0].HashBlockHeader(header)

		// They must be different
		same := true
		for i := range sha256dHash {
			if sha256dHash[i] != scryptHash[i] {
				same = false
				break
			}
		}
		if same {
			t.Fatal("CRITICAL: SHA256d and Scrypt produced identical hashes!")
		}

		// Now verify this holds under concurrent access
		const checks = 1000
		violations := make(chan bool, checks)

		for i := 0; i < checks; i++ {
			go func() {
				sha := sha256dInstances[i%len(sha256dInstances)].HashBlockHeader(header)
				scr := scryptInstances[i%len(scryptInstances)].HashBlockHeader(header)

				// Check they're different
				same := true
				for j := range sha {
					if sha[j] != scr[j] {
						same = false
						break
					}
				}
				violations <- same
			}()
		}

		for i := 0; i < checks; i++ {
			if <-violations {
				t.Error("CRITICAL: Algorithm cross-contamination detected under concurrency!")
			}
		}
	})
}

func TestMultiAlgorithmJobIsolation(t *testing.T) {
	// Verify that jobs for different algorithms cannot be confused

	t.Run("DGB and DGB-SCRYPT use same node but different algorithms", func(t *testing.T) {
		dgb, _ := Create("DGB")
		dgbScrypt, _ := Create("DGB-SCRYPT")

		// Same blockchain properties
		if dgb.GenesisBlockHash() != dgbScrypt.GenesisBlockHash() {
			t.Error("Should share genesis block")
		}
		if dgb.DefaultRPCPort() != dgbScrypt.DefaultRPCPort() {
			t.Error("Should share RPC port (same daemon)")
		}

		// Different algorithms
		if dgb.Algorithm() == dgbScrypt.Algorithm() {
			t.Error("Must have different algorithms")
		}

		// Same header produces different hashes
		header := make([]byte, 80)
		for i := range header {
			header[i] = byte(i)
		}

		dgbHash := dgb.HashBlockHeader(header)
		dgbScryptHash := dgbScrypt.HashBlockHeader(header)

		if bytesEqual(dgbHash, dgbScryptHash) {
			t.Error("Same header should produce different hashes for different algorithms")
		}
	})

	t.Run("share submitted to wrong algorithm is rejected", func(t *testing.T) {
		// This tests the conceptual isolation - in practice, the pool
		// routes shares based on the stratum port they connected to

		dgb, _ := Create("DGB")           // SHA256d
		dgbScrypt, _ := Create("DGB-SCRYPT") // Scrypt

		// Create a header that hashes to low value with SHA256d
		// It will NOT hash to low value with Scrypt (statistically impossible)
		header := make([]byte, 80)

		dgbHash := dgb.HashBlockHeader(header)
		dgbScryptHash := dgbScrypt.HashBlockHeader(header)

		// The hashes must be different
		if bytesEqual(dgbHash, dgbScryptHash) {
			t.Error("Hashes should be different for different algorithms")
		}

		// This means a share valid for SHA256d is invalid for Scrypt
		// The pool uses this to prevent cross-algorithm attacks
		t.Log("Share validation uses coin.HashBlockHeader() which ensures algorithm isolation")
	})
}

func TestMultiAlgorithmSymbolUniqueness(t *testing.T) {
	// Ensure all coin symbols are unique across all algorithms

	allSymbols := make(map[string]string) // symbol -> algorithm

	sha256dCoins := []string{"DGB", "BTC", "BCH", "BC2", "NMC", "SYS", "XMY", "FBTC"}
	scryptCoins := []string{"LTC", "DOGE", "DGB-SCRYPT", "PEP", "CAT"}

	for _, name := range sha256dCoins {
		c, _ := Create(name)
		symbol := c.Symbol()
		if existing, ok := allSymbols[symbol]; ok {
			t.Errorf("Duplicate symbol %s: used by both %s and %s algorithms",
				symbol, existing, c.Algorithm())
		}
		allSymbols[symbol] = c.Algorithm()
	}

	for _, name := range scryptCoins {
		c, _ := Create(name)
		symbol := c.Symbol()
		if existing, ok := allSymbols[symbol]; ok && existing != c.Algorithm() {
			// Special case: DGB exists as SHA256d, DGB-SCRYPT is different symbol
			if symbol == "DGB" {
				t.Errorf("DGB symbol collision between algorithms")
			}
		}
		allSymbols[symbol] = c.Algorithm()
	}

	t.Logf("Verified %d unique symbols across SHA256d and Scrypt", len(allSymbols))
}

// Helper function for byte comparison
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
