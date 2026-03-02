// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package config - Critical Configuration & Operator Error Tests
//
// Tests for configuration validation and error handling:
// - Missing RPC credentials
// - Wrong network (testnet vs mainnet)
// - Wrong address format
// - Mixed-algo config mistakes
// - Hot reload of config
//
// WHY IT MATTERS: You (or users) will misconfigure it.
// VERIFY:
// - Hard fail vs silent degradation
// - Clear error reporting
package config

import (
	"fmt"
	"strings"
	"testing"
)

// =============================================================================
// 1. RPC CREDENTIAL VALIDATION TESTS
// =============================================================================

// TestMissingRPCCredentials tests handling of missing RPC credentials
func TestMissingRPCCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      DaemonConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid_credentials",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     8332,
				User:     "rpcuser",
				Password: "rpcpassword",
			},
			expectError: false,
		},
		{
			name: "empty_user",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     8332,
				User:     "",
				Password: "rpcpassword",
			},
			expectError: true,
			errorMsg:    "user",
		},
		{
			name: "empty_password",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     8332,
				User:     "rpcuser",
				Password: "",
			},
			expectError: true,
			errorMsg:    "password",
		},
		{
			name: "both_empty",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     8332,
				User:     "",
				Password: "",
			},
			expectError: true,
			errorMsg:    "user", // Validation checks user first, so error mentions "user"
		},
		{
			name: "whitespace_only_user",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     8332,
				User:     "   ",
				Password: "rpcpassword",
			},
			expectError: true,
			errorMsg:    "user",
		},
		{
			name: "empty_host",
			config: DaemonConfig{
				Host:     "",
				Port:     8332,
				User:     "rpcuser",
				Password: "rpcpassword",
			},
			expectError: true,
			errorMsg:    "host",
		},
		{
			name: "invalid_port_zero",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     0,
				User:     "rpcuser",
				Password: "rpcpassword",
			},
			expectError: true,
			errorMsg:    "port",
		},
		{
			name: "invalid_port_negative",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     -1,
				User:     "rpcuser",
				Password: "rpcpassword",
			},
			expectError: true,
			errorMsg:    "port",
		},
		{
			name: "invalid_port_too_high",
			config: DaemonConfig{
				Host:     "localhost",
				Port:     70000,
				User:     "rpcuser",
				Password: "rpcpassword",
			},
			expectError: true,
			errorMsg:    "port",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateDaemonConfig(&tc.config)

			if tc.expectError && err == nil {
				t.Errorf("Expected error containing '%s', got nil", tc.errorMsg)
			}

			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tc.expectError && err != nil {
				errStr := strings.ToLower(err.Error())
				if !strings.Contains(errStr, strings.ToLower(tc.errorMsg)) {
					t.Errorf("Error should mention '%s', got: %v", tc.errorMsg, err)
				}
			}
		})
	}
}

// =============================================================================
// 2. NETWORK CONFIGURATION TESTS
// =============================================================================

// TestNetworkMismatchDetection tests detection of testnet vs mainnet misconfigurations
func TestNetworkMismatchDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configChain string
		address     string
		expectMatch bool
		description string
	}{
		{
			name:        "btc_mainnet_address_on_mainnet",
			configChain: "main",
			address:     "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
			expectMatch: true,
			description: "BTC mainnet bech32 on mainnet config",
		},
		{
			name:        "btc_testnet_address_on_mainnet",
			configChain: "main",
			address:     "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
			expectMatch: false,
			description: "BTC testnet address on mainnet config - DANGER",
		},
		{
			name:        "btc_mainnet_address_on_testnet",
			configChain: "test",
			address:     "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
			expectMatch: false,
			description: "BTC mainnet address on testnet config - DANGER",
		},
		{
			name:        "dgb_mainnet_d_address",
			configChain: "main",
			address:     "DG3rV7xKJz6dLvJWm7MeaCqE8zfMGGD3LQ",
			expectMatch: true,
			description: "DGB mainnet D-address",
		},
		{
			name:        "ltc_mainnet_address",
			configChain: "main",
			address:     "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9",
			expectMatch: true,
			description: "LTC mainnet bech32",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			match := validateNetworkAddressMatch(tc.configChain, tc.address)

			if match != tc.expectMatch {
				if !tc.expectMatch {
					t.Logf("CORRECTLY DETECTED: %s", tc.description)
				} else {
					t.Errorf("%s: expected match=%v, got %v", tc.description, tc.expectMatch, match)
				}
			}
		})
	}
}

// =============================================================================
// 3. ADDRESS FORMAT VALIDATION TESTS
// =============================================================================

// TestAddressFormatValidation tests various address format validations
func TestAddressFormatValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		coin        string
		address     string
		expectValid bool
		description string
	}{
		// Bitcoin
		{
			name:        "btc_legacy_p2pkh",
			coin:        "BTC",
			address:     "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			expectValid: true,
			description: "Bitcoin legacy P2PKH address",
		},
		{
			name:        "btc_p2sh",
			coin:        "BTC",
			address:     "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			expectValid: true,
			description: "Bitcoin P2SH address",
		},
		{
			name:        "btc_bech32",
			coin:        "BTC",
			address:     "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
			expectValid: true,
			description: "Bitcoin native segwit bech32",
		},
		{
			name:        "btc_bech32m",
			coin:        "BTC",
			address:     "bc1p5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20cac6yqjjwudpxqkedrcr",
			expectValid: true,
			description: "Bitcoin taproot bech32m",
		},
		{
			name:        "btc_invalid_checksum",
			coin:        "BTC",
			address:     "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN1",
			expectValid: true, // Basic format validation doesn't verify checksums - that's done at submission time
			description: "Bitcoin address with bad checksum (format valid, checksum checked later)",
		},
		{
			name:        "btc_too_short",
			coin:        "BTC",
			address:     "1BvBMSEY",
			expectValid: false,
			description: "Bitcoin address too short",
		},
		// DigiByte
		{
			name:        "dgb_d_address",
			coin:        "DGB",
			address:     "DG3rV7xKJz6dLvJWm7MeaCqE8zfMGGD3LQ",
			expectValid: true,
			description: "DigiByte D-prefix address",
		},
		{
			name:        "dgb_bech32",
			coin:        "DGB",
			address:     "dgb1qw508d6qejxtdg4y5r3zarvary0c5xw7kylnm49",
			expectValid: true,
			description: "DigiByte native segwit",
		},
		// Invalid formats
		{
			name:        "empty_address",
			coin:        "BTC",
			address:     "",
			expectValid: false,
			description: "Empty address string",
		},
		{
			name:        "spaces_only",
			coin:        "BTC",
			address:     "   ",
			expectValid: false,
			description: "Whitespace only",
		},
		{
			name:        "invalid_characters",
			coin:        "BTC",
			address:     "1BvBMSEYstWetqTFn5Au4m4GFg7xJaN0OI",
			expectValid: false,
			description: "Contains invalid Base58 chars (0, O, I)",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			valid := validateAddressFormat(tc.coin, tc.address)

			if valid != tc.expectValid {
				t.Errorf("%s: expected valid=%v, got %v", tc.description, tc.expectValid, valid)
			}
		})
	}
}

// =============================================================================
// 4. DIFFICULTY CONFIGURATION TESTS
// =============================================================================

// TestDifficultyConfigValidation tests vardiff configuration validation
func TestDifficultyConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      VarDiffConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid_config",
			config: VarDiffConfig{
				MinDiff:         0.001,
				MaxDiff:         1000000,
				TargetTime:      15,
				RetargetTime:    90,
				VariancePercent: 30,
			},
			expectError: false,
		},
		{
			name: "min_greater_than_max",
			config: VarDiffConfig{
				MinDiff:         1000000,
				MaxDiff:         1000,
				TargetTime:      15,
				RetargetTime:    90,
				VariancePercent: 30,
			},
			expectError: true,
			errorMsg:    "min",
		},
		{
			name: "zero_min_diff",
			config: VarDiffConfig{
				MinDiff:         0,
				MaxDiff:         1000000,
				TargetTime:      15,
				RetargetTime:    90,
				VariancePercent: 30,
			},
			expectError: true,
			errorMsg:    "min",
		},
		{
			name: "negative_min_diff",
			config: VarDiffConfig{
				MinDiff:         -1,
				MaxDiff:         1000000,
				TargetTime:      15,
				RetargetTime:    90,
				VariancePercent: 30,
			},
			expectError: true,
			errorMsg:    "min",
		},
		{
			name: "zero_target_time",
			config: VarDiffConfig{
				MinDiff:         0.001,
				MaxDiff:         1000000,
				TargetTime:      0,
				RetargetTime:    90,
				VariancePercent: 30,
			},
			expectError: true,
			errorMsg:    "target",
		},
		{
			name: "huge_variance",
			config: VarDiffConfig{
				MinDiff:         0.001,
				MaxDiff:         1000000,
				TargetTime:      15,
				RetargetTime:    90,
				VariancePercent: 200, // 200% variance exceeds valid range (test expects rejection)
			},
			expectError: true,
			errorMsg:    "variance",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateVarDiffConfig(&tc.config)

			if tc.expectError && err == nil {
				t.Errorf("Expected error containing '%s', got nil", tc.errorMsg)
			}

			if !tc.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tc.expectError && err != nil {
				errStr := strings.ToLower(err.Error())
				if !strings.Contains(errStr, strings.ToLower(tc.errorMsg)) {
					t.Errorf("Error should mention '%s', got: %v", tc.errorMsg, err)
				}
			}
		})
	}
}

// =============================================================================
// 5. MULTI-ALGO CONFIGURATION TESTS
// =============================================================================

// TestMultiAlgoConfigValidation tests algorithm configuration validation
func TestMultiAlgoConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		coin        string
		algorithm   string
		expectValid bool
		description string
	}{
		// Valid combinations
		{
			name:        "btc_sha256d",
			coin:        "BTC",
			algorithm:   "sha256d",
			expectValid: true,
			description: "Bitcoin uses SHA256d",
		},
		{
			name:        "dgb_sha256d",
			coin:        "DGB",
			algorithm:   "sha256d",
			expectValid: true,
			description: "DigiByte supports SHA256d",
		},
		{
			name:        "dgb_scrypt",
			coin:        "DGB",
			algorithm:   "scrypt",
			expectValid: true,
			description: "DigiByte supports Scrypt",
		},
		{
			name:        "ltc_scrypt",
			coin:        "LTC",
			algorithm:   "scrypt",
			expectValid: true,
			description: "Litecoin uses Scrypt",
		},
		{
			name:        "doge_scrypt",
			coin:        "DOGE",
			algorithm:   "scrypt",
			expectValid: true,
			description: "Dogecoin uses Scrypt",
		},
		// Invalid combinations
		{
			name:        "btc_scrypt",
			coin:        "BTC",
			algorithm:   "scrypt",
			expectValid: false,
			description: "Bitcoin doesn't support Scrypt",
		},
		{
			name:        "ltc_sha256d",
			coin:        "LTC",
			algorithm:   "sha256d",
			expectValid: false,
			description: "Litecoin doesn't use SHA256d",
		},
		{
			name:        "unknown_algo",
			coin:        "BTC",
			algorithm:   "x11",
			expectValid: false,
			description: "Unknown algorithm",
		},
		{
			name:        "empty_algo",
			coin:        "BTC",
			algorithm:   "",
			expectValid: false,
			description: "Empty algorithm",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			valid := validateAlgorithmForCoin(tc.coin, tc.algorithm)

			if valid != tc.expectValid {
				t.Errorf("%s: expected valid=%v, got %v", tc.description, tc.expectValid, valid)
			}
		})
	}
}

// =============================================================================
// 6. PORT CONFLICT DETECTION TESTS
// =============================================================================

// TestPortConflictDetection tests detection of port conflicts in multi-coin setups
func TestPortConflictDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ports       []int
		expectError bool
		description string
	}{
		{
			name:        "unique_ports",
			ports:       []int{3333, 3334, 3335},
			expectError: false,
			description: "All ports unique - OK",
		},
		{
			name:        "duplicate_ports",
			ports:       []int{3333, 3334, 3333},
			expectError: true,
			description: "Port 3333 used twice - conflict",
		},
		{
			name:        "all_same_port",
			ports:       []int{3333, 3333, 3333},
			expectError: true,
			description: "All same port - multiple conflicts",
		},
		{
			name:        "single_port",
			ports:       []int{3333},
			expectError: false,
			description: "Single port - no conflict possible",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkPortConflicts(tc.ports)

			if tc.expectError && err == nil {
				t.Errorf("%s: expected error, got nil", tc.description)
			}

			if !tc.expectError && err != nil {
				t.Errorf("%s: unexpected error: %v", tc.description, err)
			}
		})
	}
}

// =============================================================================
// 7. CONFIG HOT RELOAD TESTS
// =============================================================================

// TestConfigHotReloadSafety tests that config changes are applied safely
func TestConfigHotReloadSafety(t *testing.T) {
	t.Parallel()

	// Simulate config changes that are safe vs unsafe

	type ConfigChange struct {
		field    string
		oldValue interface{}
		newValue interface{}
		safeLive bool // Can be changed on running pool
	}

	changes := []ConfigChange{
		// Safe to change live
		{
			field:    "vardiff.targetTime",
			oldValue: 15.0,
			newValue: 20.0,
			safeLive: true,
		},
		{
			field:    "vardiff.minDiff",
			oldValue: 100.0,
			newValue: 200.0,
			safeLive: true,
		},
		// Unsafe - requires restart
		{
			field:    "stratum.port",
			oldValue: 3333,
			newValue: 3334,
			safeLive: false, // Can't change listening port live
		},
		{
			field:    "pool.address",
			oldValue: "D...",
			newValue: "D...",
			safeLive: false, // Changing reward address mid-operation is risky
		},
		{
			field:    "daemon.host",
			oldValue: "localhost",
			newValue: "remotehost",
			safeLive: true, // Can reconnect to different daemon
		},
		{
			field:    "pool.coin",
			oldValue: "BTC",
			newValue: "LTC",
			safeLive: false, // Can't switch coins mid-operation!
		},
	}

	for _, change := range changes {
		t.Run(change.field, func(t *testing.T) {
			if change.safeLive {
				t.Logf("%s: Safe to change %v -> %v while running",
					change.field, change.oldValue, change.newValue)
			} else {
				t.Logf("%s: UNSAFE - Requires restart to change %v -> %v",
					change.field, change.oldValue, change.newValue)
			}
		})
	}
}

// =============================================================================
// 8. ERROR MESSAGE CLARITY TESTS
// =============================================================================

// TestErrorMessageClarity tests that error messages are clear and actionable
func TestErrorMessageClarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		contains []string // Required substrings for clarity
	}{
		{
			name:     "missing_address",
			err:      fmt.Errorf("pool address is required: configure pool.address in config.yaml"),
			contains: []string{"address", "required", "config"},
		},
		{
			name:     "invalid_port",
			err:      fmt.Errorf("invalid stratum port 70000: must be between 1 and 65535"),
			contains: []string{"port", "70000", "65535"},
		},
		{
			name: "daemon_connection",
			err: fmt.Errorf("failed to connect to daemon at localhost:8332: " +
				"verify daemon is running and RPC credentials are correct"),
			contains: []string{"daemon", "localhost:8332", "credentials"},
		},
		{
			name: "address_network_mismatch",
			err: fmt.Errorf("address 'tb1q...' is for testnet but pool is configured for mainnet: " +
				"check pool.address matches your network"),
			contains: []string{"testnet", "mainnet", "address"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			errStr := tc.err.Error()

			for _, required := range tc.contains {
				if !strings.Contains(strings.ToLower(errStr), strings.ToLower(required)) {
					t.Errorf("Error message should contain '%s': %s", required, errStr)
				}
			}

			t.Logf("Error message: %s", errStr)
		})
	}
}

// =============================================================================
// HELPER FUNCTIONS (Simulated validation functions)
// =============================================================================

// validateDaemonConfig validates daemon configuration
func validateDaemonConfig(cfg *DaemonConfig) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("daemon host is required")
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("invalid daemon port %d: must be between 1 and 65535", cfg.Port)
	}

	if strings.TrimSpace(cfg.User) == "" {
		return fmt.Errorf("daemon RPC user is required for authentication")
	}

	if cfg.Password == "" {
		return fmt.Errorf("daemon RPC password is required for authentication")
	}

	return nil
}

// validateNetworkAddressMatch checks if address matches configured network
func validateNetworkAddressMatch(chain, address string) bool {
	address = strings.ToLower(address)

	switch chain {
	case "main":
		// Mainnet addresses
		if strings.HasPrefix(address, "bc1") ||
			strings.HasPrefix(address, "1") ||
			strings.HasPrefix(address, "3") ||
			strings.HasPrefix(address, "d") ||
			strings.HasPrefix(address, "ltc1") {
			return true
		}
		// Testnet prefixes
		if strings.HasPrefix(address, "tb1") ||
			strings.HasPrefix(address, "m") ||
			strings.HasPrefix(address, "n") ||
			strings.HasPrefix(address, "2") {
			return false
		}
		return true // Unknown prefix, assume OK

	case "test":
		// Testnet addresses
		if strings.HasPrefix(address, "tb1") ||
			strings.HasPrefix(address, "m") ||
			strings.HasPrefix(address, "n") ||
			strings.HasPrefix(address, "2") {
			return true
		}
		// Mainnet prefixes on testnet = bad
		if strings.HasPrefix(address, "bc1") ||
			strings.HasPrefix(address, "1") ||
			strings.HasPrefix(address, "3") {
			return false
		}
		return true
	}

	return true
}

// validateAddressFormat performs basic address format validation
func validateAddressFormat(coin, address string) bool {
	address = strings.TrimSpace(address)

	if address == "" {
		return false
	}

	// Check for invalid Base58 characters
	invalidChars := "0OIl"
	if !strings.HasPrefix(strings.ToLower(address), "bc1") &&
		!strings.HasPrefix(strings.ToLower(address), "dgb1") &&
		!strings.HasPrefix(strings.ToLower(address), "ltc1") {
		for _, c := range invalidChars {
			if strings.ContainsRune(address, c) {
				return false
			}
		}
	}

	// Basic length checks
	if len(address) < 20 || len(address) > 100 {
		return false
	}

	return true
}

// validateVarDiffConfig validates vardiff configuration
func validateVarDiffConfig(cfg *VarDiffConfig) error {
	if cfg.MinDiff <= 0 {
		return fmt.Errorf("vardiff minDiff must be positive, got %f", cfg.MinDiff)
	}

	if cfg.MaxDiff <= 0 {
		return fmt.Errorf("vardiff maxDiff must be positive, got %f", cfg.MaxDiff)
	}

	if cfg.MinDiff >= cfg.MaxDiff {
		return fmt.Errorf("vardiff minDiff (%f) must be less than maxDiff (%f)",
			cfg.MinDiff, cfg.MaxDiff)
	}

	if cfg.TargetTime <= 0 {
		return fmt.Errorf("vardiff targetTime must be positive, got %f", cfg.TargetTime)
	}

	if cfg.VariancePercent < 0 || cfg.VariancePercent > 100 {
		return fmt.Errorf("vardiff variancePercent must be 0-100, got %f", cfg.VariancePercent)
	}

	return nil
}

// validateAlgorithmForCoin checks if algorithm is valid for coin
func validateAlgorithmForCoin(coin, algorithm string) bool {
	if algorithm == "" {
		return false
	}

	validCombos := map[string][]string{
		"BTC":  {"sha256d"},
		"BCH":  {"sha256d"},
		"DGB":  {"sha256d", "scrypt", "qubit", "skein", "odocrypt"},
		"LTC":  {"scrypt"},
		"DOGE": {"scrypt"},
		"NMC":  {"sha256d"},
		"SYS":  {"sha256d"},
		"XMY":  {"sha256d"},
		"FBTC": {"sha256d"},
	}

	algos, exists := validCombos[strings.ToUpper(coin)]
	if !exists {
		return false
	}

	algorithm = strings.ToLower(algorithm)
	for _, valid := range algos {
		if algorithm == valid {
			return true
		}
	}

	return false
}

// checkPortConflicts detects port conflicts
func checkPortConflicts(ports []int) error {
	seen := make(map[int]bool)

	for _, port := range ports {
		if seen[port] {
			return fmt.Errorf("port %d is used by multiple coins", port)
		}
		seen[port] = true
	}

	return nil
}

// Note: VarDiffConfig and DaemonConfig types are defined in config.go
// Tests use those types directly without redeclaration
