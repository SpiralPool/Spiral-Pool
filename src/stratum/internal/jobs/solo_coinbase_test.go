// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
	"go.uber.org/zap"
)

// testLogger returns a no-op logger for tests to avoid nil pointer panics
func testLogger() *zap.SugaredLogger {
	logger, _ := zap.NewDevelopment()
	return logger.Sugar()
}

// TestSoloMinerAddress_ValidAddress tests that valid miner addresses are accepted.
// Note: Uses real valid addresses with proper checksums.
func TestSoloMinerAddress_ValidAddress(t *testing.T) {
	tests := []struct {
		name         string
		coinSymbol   string
		minerAddress string
		expectValid  bool
	}{
		// BTC addresses (real valid addresses)
		{"BTC P2PKH", "BTC", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", true},
		{"BTC Bech32", "BTC", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", true},

		// LTC addresses (real valid addresses)
		{"LTC P2PKH", "LTC", "LaMT348PWRnrqeeWArpwQPbuanpXDZGEUz", true},
		{"LTC Bech32", "LTC", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9", true},

		// Invalid addresses
		{"Invalid - empty", "BTC", "", false},
		{"Invalid - too short", "BTC", "1ABC", false},
		{"Invalid - wrong prefix", "LTC", "1WrongPrefix", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create coin implementation
			coinImpl, err := coin.Create(tt.coinSymbol)
			if err != nil {
				t.Fatalf("Failed to create coin %s: %v", tt.coinSymbol, err)
			}

			// Try to build coinbase script (this is what SetSoloMinerAddress does internally)
			if tt.minerAddress != "" {
				_, err = coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
					PoolAddress: tt.minerAddress,
				})
			} else {
				err = nil // Empty address should be handled as "clear SOLO miner"
			}

			isValid := err == nil
			if tt.minerAddress == "" {
				isValid = false // Empty address is a special case (clear)
			}

			if isValid != tt.expectValid {
				if tt.expectValid {
					t.Errorf("Expected valid address but got error: %v", err)
				} else {
					t.Errorf("Expected invalid address but it was accepted")
				}
			}
		})
	}
}

// TestSoloMinerAddress_OutputScriptPriority tests that the miner's output script
// takes priority over the config fallback when set.
func TestSoloMinerAddress_OutputScriptPriority(t *testing.T) {
	// This test verifies the buildOutputScript() logic:
	// 1. If soloMinerOutputScript is set, return it
	// 2. Otherwise, return the config outputScript

	// Create a manager with config script only
	configScript := []byte{0x76, 0xa9, 0x14, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x88, 0xac}

	m := &Manager{
		outputScript: configScript,
	}

	// Without SOLO miner set, should return config script
	result := m.buildOutputScript()
	if len(result) != len(configScript) {
		t.Errorf("Expected config script length %d, got %d", len(configScript), len(result))
	}

	// Manually set the SOLO miner script via internal fields (testing internals)
	minerScript := []byte{0x00, 0x14, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
	m.soloMinerMu.Lock()
	m.soloMinerAddress = "test_miner_address"
	m.soloMinerOutputScript = minerScript
	m.soloMinerMu.Unlock()

	// Now buildOutputScript should return miner script
	result = m.buildOutputScript()
	if len(result) != len(minerScript) {
		t.Errorf("Expected miner script length %d, got %d", len(minerScript), len(result))
	}

	// Clear miner script - should fall back to config
	m.soloMinerMu.Lock()
	m.soloMinerAddress = ""
	m.soloMinerOutputScript = nil
	m.soloMinerMu.Unlock()

	result = m.buildOutputScript()
	if len(result) != len(configScript) {
		t.Errorf("Expected config script length %d, got %d", len(configScript), len(result))
	}
}

// TestSoloMinerAddress_StratumUsernameParsing tests that stratum usernames
// are correctly parsed into wallet addresses.
func TestSoloMinerAddress_StratumUsernameParsing(t *testing.T) {
	tests := []struct {
		username       string
		expectedAddr   string
		expectedWorker string
	}{
		// Standard format: address.worker
		{"DWallet123.worker1", "DWallet123", "worker1"},
		{"DWallet123.rig1.gpu0", "DWallet123.rig1", "gpu0"},

		// Just address (no worker)
		{"DWallet123", "DWallet123", "default"},

		// Edge cases
		{"address.", "address", ""},
		{".worker", "", "worker"},
		{"", "", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			// Use the same parsing logic as the stratum handler
			addr, worker := parseWorkerNameForTest(tt.username)

			if addr != tt.expectedAddr {
				t.Errorf("Address: got %q, want %q", addr, tt.expectedAddr)
			}
			if worker != tt.expectedWorker {
				t.Errorf("Worker: got %q, want %q", worker, tt.expectedWorker)
			}
		})
	}
}

// parseWorkerNameForTest replicates the stratum handler's parseWorkerName
// to verify the parsing logic.
func parseWorkerNameForTest(name string) (address, worker string) {
	if name == "" {
		return "", "default"
	}
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i], name[i+1:]
		}
	}
	return name, "default"
}

// TestSoloMinerAddress_MultiCoinIndependence verifies that in multi-coin mode,
// each coin's JobManager has completely independent SOLO mining state.
func TestSoloMinerAddress_MultiCoinIndependence(t *testing.T) {
	// Use real valid addresses for BTC and LTC
	coins := []struct {
		symbol       string
		poolAddress  string
		minerAddress string
	}{
		{"BTC", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"},
		{"LTC", "LaMT348PWRnrqeeWArpwQPbuanpXDZGEUz", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9"},
	}

	// Create managers for each coin
	managers := make(map[string]*Manager)

	for _, c := range coins {
		coinImpl, err := coin.Create(c.symbol)
		if err != nil {
			t.Fatalf("Failed to create coin %s: %v", c.symbol, err)
		}

		// Build the pool's fallback output script
		poolScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
			PoolAddress: c.poolAddress,
		})
		if err != nil {
			t.Fatalf("Failed to build pool script for %s: %v", c.symbol, err)
		}

		// Create a manager for this coin with logger
		m := &Manager{
			coinImpl:     coinImpl,
			outputScript: poolScript,
			logger:       testLogger(),
		}
		managers[c.symbol] = m
	}

	// Step 1: Verify each manager starts with no SOLO miner set
	for symbol, m := range managers {
		addr := m.GetSoloMinerAddress()
		if addr != "" {
			t.Errorf("%s: Expected no SOLO miner initially, got %q", symbol, addr)
		}
	}

	// Step 2: Set SOLO miner for BTC only - verify LTC unaffected
	btcMinerAddr := coins[0].minerAddress
	if err := managers["BTC"].SetSoloMinerAddress(btcMinerAddr); err != nil {
		t.Fatalf("BTC SetSoloMinerAddress failed: %v", err)
	}

	// Verify BTC has miner set
	if got := managers["BTC"].GetSoloMinerAddress(); got != btcMinerAddr {
		t.Errorf("BTC: Expected miner %q, got %q", btcMinerAddr, got)
	}

	// Verify LTC is NOT affected
	if got := managers["LTC"].GetSoloMinerAddress(); got != "" {
		t.Errorf("LTC: Expected no miner (independence), got %q", got)
	}

	// Step 3: Set different SOLO miner for LTC
	ltcMinerAddr := coins[1].minerAddress
	if err := managers["LTC"].SetSoloMinerAddress(ltcMinerAddr); err != nil {
		t.Fatalf("LTC SetSoloMinerAddress failed: %v", err)
	}

	// Verify each coin has its own miner
	if got := managers["BTC"].GetSoloMinerAddress(); got != btcMinerAddr {
		t.Errorf("BTC: Expected %q, got %q", btcMinerAddr, got)
	}
	if got := managers["LTC"].GetSoloMinerAddress(); got != ltcMinerAddr {
		t.Errorf("LTC: Expected %q, got %q", ltcMinerAddr, got)
	}

	// Step 4: Clear BTC miner, verify LTC still has its miner
	if err := managers["BTC"].SetSoloMinerAddress(""); err != nil {
		t.Fatalf("BTC clear miner failed: %v", err)
	}

	if got := managers["BTC"].GetSoloMinerAddress(); got != "" {
		t.Errorf("BTC: Expected cleared, got %q", got)
	}
	if got := managers["LTC"].GetSoloMinerAddress(); got != ltcMinerAddr {
		t.Errorf("LTC: Miner unexpectedly changed to %q after BTC clear", got)
	}

	t.Log("Multi-coin SOLO mining independence verified successfully")
}

// TestSoloMinerAddress_ConfigFallback verifies that when a miner's
// address is invalid, the coin correctly falls back to its pool address.
func TestSoloMinerAddress_ConfigFallback(t *testing.T) {
	// Use real valid pool addresses
	testCases := []struct {
		symbol   string
		poolAddr string
	}{
		{"BTC", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"},
		{"LTC", "LaMT348PWRnrqeeWArpwQPbuanpXDZGEUz"},
	}

	for _, tc := range testCases {
		t.Run(tc.symbol+"_fallback", func(t *testing.T) {
			coinImpl, err := coin.Create(tc.symbol)
			if err != nil {
				t.Skipf("Coin %s not available: %v", tc.symbol, err)
			}

			// Build pool's config script
			poolScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
				PoolAddress: tc.poolAddr,
			})
			if err != nil {
				t.Fatalf("Failed to build pool script: %v", err)
			}

			m := &Manager{
				coinImpl:     coinImpl,
				outputScript: poolScript,
				logger:       testLogger(),
			}

			// Try to set an invalid miner address
			invalidAddr := "INVALID_ADDRESS_123"
			err = m.SetSoloMinerAddress(invalidAddr)
			if err == nil {
				t.Fatalf("Expected error for invalid address, got nil")
			}

			// Verify the manager still uses the pool's config script
			result := m.buildOutputScript()
			if len(result) != len(poolScript) {
				t.Errorf("Expected fallback to pool script (len %d), got len %d",
					len(poolScript), len(result))
			}

			// Verify the miner address is NOT set after failed attempt
			if got := m.GetSoloMinerAddress(); got != "" {
				t.Errorf("Expected no miner after invalid attempt, got %q", got)
			}
		})
	}
}

// TestSoloMinerAddress_CrossCoinRejection verifies that a coin rejects
// addresses from other coins (e.g., BTC rejects LTC addresses).
func TestSoloMinerAddress_CrossCoinRejection(t *testing.T) {
	crossCoinTests := []struct {
		coin        string
		wrongAddr   string
		description string
	}{
		{"BTC", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9", "LTC bech32 on BTC"},
		{"LTC", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "BTC bech32 on LTC"},
	}

	for _, tc := range crossCoinTests {
		t.Run(tc.description, func(t *testing.T) {
			coinImpl, err := coin.Create(tc.coin)
			if err != nil {
				t.Skipf("Coin %s not available: %v", tc.coin, err)
			}

			// Try to build script with wrong coin's address
			_, err = coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
				PoolAddress: tc.wrongAddr,
			})

			if err == nil {
				t.Errorf("%s should reject %s, but it was accepted",
					tc.coin, tc.description)
			}
		})
	}
}

// TestSoloMinerAddress_CoinbaseConstruction verifies the full coinbase
// construction path with SOLO mining enabled.
func TestSoloMinerAddress_CoinbaseConstruction(t *testing.T) {
	// Test with BTC (well-tested addresses)
	coinImpl, err := coin.Create("BTC")
	if err != nil {
		t.Fatalf("Failed to create BTC coin: %v", err)
	}

	poolAddress := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
	minerAddress := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"

	// Build pool's fallback output script
	poolScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
		PoolAddress: poolAddress,
	})
	if err != nil {
		t.Fatalf("Failed to build pool script: %v", err)
	}

	// Build miner's output script
	minerScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
		PoolAddress: minerAddress,
	})
	if err != nil {
		t.Fatalf("Failed to build miner script: %v", err)
	}

	// Verify scripts are different
	if len(poolScript) == len(minerScript) {
		same := true
		for i := range poolScript {
			if poolScript[i] != minerScript[i] {
				same = false
				break
			}
		}
		if same {
			t.Fatalf("Pool and miner scripts are identical — test is invalid")
		}
	}

	// Create manager with pool script as default and logger
	m := &Manager{
		coinImpl:     coinImpl,
		outputScript: poolScript,
		logger:       testLogger(),
	}

	// Step 1: Verify buildOutputScript returns pool script initially
	initialScript := m.buildOutputScript()
	if len(initialScript) != len(poolScript) {
		t.Errorf("Initial script length mismatch: got %d, want %d",
			len(initialScript), len(poolScript))
	}

	// Step 2: Set SOLO miner address
	if err := m.SetSoloMinerAddress(minerAddress); err != nil {
		t.Fatalf("SetSoloMinerAddress failed: %v", err)
	}

	// Step 3: Verify buildOutputScript now returns miner script
	soloScript := m.buildOutputScript()
	if len(soloScript) != len(minerScript) {
		t.Errorf("SOLO script length mismatch: got %d, want %d",
			len(soloScript), len(minerScript))
	}

	// Verify the scripts match byte-for-byte
	for i := range soloScript {
		if soloScript[i] != minerScript[i] {
			t.Errorf("SOLO script byte mismatch at position %d: got 0x%02x, want 0x%02x",
				i, soloScript[i], minerScript[i])
			break
		}
	}

	// Step 4: Verify expected prefix (P2WPKH for bech32)
	expectedPrefix := []byte{0x00, 0x14} // OP_0 PUSH20
	if len(soloScript) >= 2 {
		for i, b := range expectedPrefix {
			if soloScript[i] != b {
				t.Errorf("SOLO script prefix mismatch at position %d: got 0x%02x, want 0x%02x",
					i, soloScript[i], b)
			}
		}
	}

	// Step 5: Verify GetSoloMinerAddress returns the set address
	gotAddr := m.GetSoloMinerAddress()
	if gotAddr != minerAddress {
		t.Errorf("GetSoloMinerAddress mismatch: got %q, want %q",
			gotAddr, minerAddress)
	}

	// Step 6: Clear SOLO miner and verify fallback
	if err := m.SetSoloMinerAddress(""); err != nil {
		t.Fatalf("Clear SOLO miner failed: %v", err)
	}

	clearedScript := m.buildOutputScript()
	if len(clearedScript) != len(poolScript) {
		t.Errorf("Cleared script length mismatch: got %d, want %d",
			len(clearedScript), len(poolScript))
	}

	t.Logf("SOLO coinbase construction verified for BTC:")
	t.Logf("  Pool script length: %d bytes", len(poolScript))
	t.Logf("  Miner script length: %d bytes", len(minerScript))
	t.Logf("  Miner address: %s", minerAddress)
}

// TestSoloMinerAddress_EndToEndFlow tests the complete SOLO mining flow
// as it would occur in production.
func TestSoloMinerAddress_EndToEndFlow(t *testing.T) {
	// Simulate the stratum username format: "ADDRESS.WORKER"
	testCases := []struct {
		stratumUsername string
		expectedAddress string
		expectedWorker  string
		coinSymbol      string
		shouldSucceed   bool
	}{
		// Valid BTC bech32 address
		{
			stratumUsername: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4.rig1",
			expectedAddress: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedWorker:  "rig1",
			coinSymbol:      "BTC",
			shouldSucceed:   true,
		},
		// Invalid address (should fallback to pool)
		{
			stratumUsername: "TEST.worker1",
			expectedAddress: "TEST",
			expectedWorker:  "worker1",
			coinSymbol:      "BTC",
			shouldSucceed:   false,
		},
		// Just address, no worker
		{
			stratumUsername: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedAddress: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedWorker:  "default",
			coinSymbol:      "BTC",
			shouldSucceed:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.stratumUsername, func(t *testing.T) {
			// Step 1: Parse username (same logic as stratum handler)
			parsedAddr, parsedWorker := parseWorkerNameForTest(tc.stratumUsername)

			if parsedAddr != tc.expectedAddress {
				t.Errorf("Parsed address mismatch: got %q, want %q",
					parsedAddr, tc.expectedAddress)
			}
			if parsedWorker != tc.expectedWorker {
				t.Errorf("Parsed worker mismatch: got %q, want %q",
					parsedWorker, tc.expectedWorker)
			}

			// Step 2: Create coin and manager
			coinImpl, err := coin.Create(tc.coinSymbol)
			if err != nil {
				t.Skipf("Coin %s not available: %v", tc.coinSymbol, err)
			}

			poolAddress := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
			poolScript, err := coinImpl.BuildCoinbaseScript(coin.CoinbaseParams{
				PoolAddress: poolAddress,
			})
			if err != nil {
				t.Fatalf("Failed to build pool script: %v", err)
			}

			m := &Manager{
				coinImpl:     coinImpl,
				outputScript: poolScript,
				logger:       testLogger(),
			}

			// Step 3: Attempt to set SOLO miner address (as CoinPool would)
			setErr := m.SetSoloMinerAddress(parsedAddr)

			if tc.shouldSucceed {
				if setErr != nil {
					t.Errorf("SetSoloMinerAddress should succeed but failed: %v", setErr)
				}

				// Verify miner address is set
				if got := m.GetSoloMinerAddress(); got != parsedAddr {
					t.Errorf("Miner address not set: got %q, want %q", got, parsedAddr)
				}

				// Verify buildOutputScript returns miner's script
				script := m.buildOutputScript()
				if len(script) == len(poolScript) {
					same := true
					for i := range script {
						if script[i] != poolScript[i] {
							same = false
							break
						}
					}
					if same {
						t.Errorf("Output script should be miner's, not pool's")
					}
				}
			} else {
				if setErr == nil {
					t.Errorf("SetSoloMinerAddress should fail for invalid address %q", parsedAddr)
				}

				// Verify fallback to pool address
				if got := m.GetSoloMinerAddress(); got != "" {
					t.Errorf("Invalid address should not be stored: got %q", got)
				}

				// Verify buildOutputScript returns pool's script
				script := m.buildOutputScript()
				if len(script) != len(poolScript) {
					t.Errorf("Fallback script length mismatch: got %d, want %d",
						len(script), len(poolScript))
				}
			}
		})
	}
}
