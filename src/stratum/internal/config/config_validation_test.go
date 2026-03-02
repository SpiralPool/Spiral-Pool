// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Tests for config validation: MaskCredentials, ResolveCredentials,
// validatePortConflicts, address validation, and GetConfigWarnings.
package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// Part 1: MaskCredentials()
// =============================================================================

// TestMaskCredentials_RPC_Passwords verifies that daemon RPC passwords are
// masked in the output while the original config is unmodified.
func TestMaskCredentials_RPC_Passwords(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon: DaemonConfig{
			Host:     "localhost",
			User:     "myrpcuser",
			Password: "verysecretrpcpassword",
		},
		Database: DatabaseConfig{
			Host: "localhost",
		},
	}

	masked := cfg.MaskCredentials()

	// Daemon credentials should be masked
	if masked.Daemon.User != "***" {
		t.Errorf("Daemon user should be masked to '***', got: %q", masked.Daemon.User)
	}
	if masked.Daemon.Password != "***" {
		t.Errorf("Daemon password should be masked to '***', got: %q", masked.Daemon.Password)
	}

	// Original should be unchanged
	if cfg.Daemon.User != "myrpcuser" {
		t.Error("Original daemon user was modified by MaskCredentials()")
	}
	if cfg.Daemon.Password != "verysecretrpcpassword" {
		t.Error("Original daemon password was modified by MaskCredentials()")
	}
}

// TestMaskCredentials_Database_Passwords verifies that database credentials
// (user and password) are masked in the output.
func TestMaskCredentials_Database_Passwords(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:  DaemonConfig{Host: "localhost"},
		Database: DatabaseConfig{
			Host:     "db.example.com",
			User:     "pooldbuser",
			Password: "supersecretdbpass",
		},
	}

	masked := cfg.MaskCredentials()

	if masked.Database.User != "***" {
		t.Errorf("Database user should be masked, got: %q", masked.Database.User)
	}
	if masked.Database.Password != "***" {
		t.Errorf("Database password should be masked, got: %q", masked.Database.Password)
	}

	// Original unchanged
	if cfg.Database.User != "pooldbuser" {
		t.Error("Original database user was modified")
	}
	if cfg.Database.Password != "supersecretdbpass" {
		t.Error("Original database password was modified")
	}
}

// TestMaskCredentials_PerCoinDaemonCredentials verifies that per-coin daemon
// credentials are also masked in the output.
func TestMaskCredentials_PerCoinDaemonCredentials(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost"},
		Database: DatabaseConfig{Host: "localhost"},
		Coins: []CoinConfig{
			{
				Name:    "Bitcoin",
				Symbol:  "BTC",
				Enabled: true,
				Address: "bc1qTestAddress",
				Daemon: DaemonConfig{
					Host:     "btc-node.local",
					User:     "btcrpcuser",
					Password: "btcsecretpass",
				},
			},
			{
				Name:    "Litecoin",
				Symbol:  "LTC",
				Enabled: true,
				Address: "LTestAddress",
				Daemon: DaemonConfig{
					Host:     "ltc-node.local",
					User:     "ltcrpcuser",
					Password: "ltcsecretpass",
				},
			},
		},
	}

	masked := cfg.MaskCredentials()

	for i, coin := range masked.Coins {
		if coin.Daemon.User != "***" {
			t.Errorf("coins[%d] (%s) daemon user should be masked, got: %q", i, coin.Symbol, coin.Daemon.User)
		}
		if coin.Daemon.Password != "***" {
			t.Errorf("coins[%d] (%s) daemon password should be masked, got: %q", i, coin.Symbol, coin.Daemon.Password)
		}
	}

	// Verify originals are intact
	if cfg.Coins[0].Daemon.User != "btcrpcuser" {
		t.Error("Original BTC daemon user was modified")
	}
	if cfg.Coins[1].Daemon.Password != "ltcsecretpass" {
		t.Error("Original LTC daemon password was modified")
	}
}

// TestMaskCredentials_EmptyCredentials_NotMasked verifies that empty credentials
// are NOT replaced with "***" (they remain empty).
func TestMaskCredentials_EmptyCredentials_NotMasked(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost", User: "", Password: ""},
		Database: DatabaseConfig{Host: "localhost", User: "", Password: ""},
	}

	masked := cfg.MaskCredentials()

	if masked.Daemon.User == "***" {
		t.Error("Empty daemon user should NOT be masked to '***'")
	}
	if masked.Daemon.Password == "***" {
		t.Error("Empty daemon password should NOT be masked to '***'")
	}
	if masked.Database.User == "***" {
		t.Error("Empty database user should NOT be masked to '***'")
	}
	if masked.Database.Password == "***" {
		t.Error("Empty database password should NOT be masked to '***'")
	}
}

// TestMaskCredentials_NonCredentialFieldsPreserved verifies that non-credential
// fields (host, port, etc.) are preserved in the masked copy.
func TestMaskCredentials_NonCredentialFieldsPreserved(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Pool: PoolConfig{ID: "test-pool", Coin: "digibyte", Address: "DAddr"},
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
		},
		Daemon: DaemonConfig{
			Host:     "daemon.example.com",
			Port:     14022,
			User:     "rpcuser",
			Password: "secret",
		},
		Database: DatabaseConfig{
			Host:     "db.example.com",
			Port:     5432,
			Database: "pooldb",
			User:     "dbuser",
			Password: "dbsecret",
		},
	}

	masked := cfg.MaskCredentials()

	// Non-credential fields should be preserved
	if masked.Pool.ID != "test-pool" {
		t.Errorf("Pool ID not preserved: %q", masked.Pool.ID)
	}
	if masked.Daemon.Host != "daemon.example.com" {
		t.Errorf("Daemon host not preserved: %q", masked.Daemon.Host)
	}
	if masked.Daemon.Port != 14022 {
		t.Errorf("Daemon port not preserved: %d", masked.Daemon.Port)
	}
	if masked.Database.Host != "db.example.com" {
		t.Errorf("Database host not preserved: %q", masked.Database.Host)
	}
	if masked.Database.Port != 5432 {
		t.Errorf("Database port not preserved: %d", masked.Database.Port)
	}
	if masked.Database.Database != "pooldb" {
		t.Errorf("Database name not preserved: %q", masked.Database.Database)
	}
}

// =============================================================================
// Part 2: ResolveCredentials()
// =============================================================================

// TestResolveCredentials_DaemonFromEnv verifies that daemon RPC credentials
// can be resolved from SPIRAL_DAEMON_USER and SPIRAL_DAEMON_PASSWORD env vars.
func TestResolveCredentials_DaemonFromEnv(t *testing.T) {
	// Not parallel: modifies environment variables
	const envUser = "SPIRAL_DAEMON_USER"
	const envPass = "SPIRAL_DAEMON_PASSWORD"

	// Save and restore
	origUser := os.Getenv(envUser)
	origPass := os.Getenv(envPass)
	defer func() {
		os.Setenv(envUser, origUser)
		os.Setenv(envPass, origPass)
	}()

	os.Setenv(envUser, "env_rpc_user")
	os.Setenv(envPass, "env_rpc_pass")

	cfg := &Config{
		Daemon: DaemonConfig{
			Host:     "localhost",
			User:     "config_user",
			Password: "config_pass",
		},
	}

	cfg.ResolveCredentials()

	if cfg.Daemon.User != "env_rpc_user" {
		t.Errorf("Expected daemon user from env, got: %q", cfg.Daemon.User)
	}
	if cfg.Daemon.Password != "env_rpc_pass" {
		t.Errorf("Expected daemon password from env, got: %q", cfg.Daemon.Password)
	}
}

// TestResolveCredentials_DatabaseFromEnv verifies that database credentials
// can be resolved from SPIRAL_DATABASE_USER and SPIRAL_DATABASE_PASSWORD.
func TestResolveCredentials_DatabaseFromEnv(t *testing.T) {
	const envUser = "SPIRAL_DATABASE_USER"
	const envPass = "SPIRAL_DATABASE_PASSWORD"

	origUser := os.Getenv(envUser)
	origPass := os.Getenv(envPass)
	defer func() {
		os.Setenv(envUser, origUser)
		os.Setenv(envPass, origPass)
	}()

	os.Setenv(envUser, "env_db_user")
	os.Setenv(envPass, "env_db_pass")

	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "localhost",
			User:     "config_db_user",
			Password: "config_db_pass",
		},
	}

	cfg.ResolveCredentials()

	if cfg.Database.User != "env_db_user" {
		t.Errorf("Expected database user from env, got: %q", cfg.Database.User)
	}
	if cfg.Database.Password != "env_db_pass" {
		t.Errorf("Expected database password from env, got: %q", cfg.Database.Password)
	}
}

// TestResolveCredentials_PerCoinDaemonFromEnv verifies that per-coin daemon
// credentials can be resolved from SPIRAL_<COIN>_DAEMON_USER/PASSWORD.
func TestResolveCredentials_PerCoinDaemonFromEnv(t *testing.T) {
	const envUser = "SPIRAL_BTC_DAEMON_USER"
	const envPass = "SPIRAL_BTC_DAEMON_PASSWORD"

	origUser := os.Getenv(envUser)
	origPass := os.Getenv(envPass)
	defer func() {
		os.Setenv(envUser, origUser)
		os.Setenv(envPass, origPass)
	}()

	os.Setenv(envUser, "env_btc_user")
	os.Setenv(envPass, "env_btc_pass")

	cfg := &Config{
		Coins: []CoinConfig{
			{
				Symbol: "BTC",
				Daemon: DaemonConfig{
					Host:     "btc-node",
					User:     "config_btc_user",
					Password: "config_btc_pass",
				},
			},
		},
	}

	cfg.ResolveCredentials()

	if cfg.Coins[0].Daemon.User != "env_btc_user" {
		t.Errorf("Expected BTC daemon user from env, got: %q", cfg.Coins[0].Daemon.User)
	}
	if cfg.Coins[0].Daemon.Password != "env_btc_pass" {
		t.Errorf("Expected BTC daemon password from env, got: %q", cfg.Coins[0].Daemon.Password)
	}
}

// TestResolveCredentials_UnsetEnvKeepsConfigValue verifies that when environment
// variables are NOT set, the config file values are retained.
func TestResolveCredentials_UnsetEnvKeepsConfigValue(t *testing.T) {
	// Ensure env vars are unset
	const envUser = "SPIRAL_DAEMON_USER"
	const envPass = "SPIRAL_DAEMON_PASSWORD"

	origUser := os.Getenv(envUser)
	origPass := os.Getenv(envPass)
	defer func() {
		os.Setenv(envUser, origUser)
		os.Setenv(envPass, origPass)
	}()

	os.Unsetenv(envUser)
	os.Unsetenv(envPass)

	cfg := &Config{
		Daemon: DaemonConfig{
			Host:     "localhost",
			User:     "config_user",
			Password: "config_pass",
		},
	}

	cfg.ResolveCredentials()

	if cfg.Daemon.User != "config_user" {
		t.Errorf("Config user should be retained when env is unset, got: %q", cfg.Daemon.User)
	}
	if cfg.Daemon.Password != "config_pass" {
		t.Errorf("Config password should be retained when env is unset, got: %q", cfg.Daemon.Password)
	}
}

// TestResolveCredentials_AdminAPIKeyFromEnv verifies that the admin API key
// can be resolved from SPIRAL_ADMIN_API_KEY environment variable.
func TestResolveCredentials_AdminAPIKeyFromEnv(t *testing.T) {
	const envKey = "SPIRAL_ADMIN_API_KEY"
	origKey := os.Getenv(envKey)
	defer os.Setenv(envKey, origKey)

	os.Setenv(envKey, "env_admin_key_32chars_abcdefghijk")

	cfg := &Config{
		API: APIConfig{
			AdminAPIKey: "config_key",
		},
	}

	cfg.ResolveCredentials()

	if cfg.API.AdminAPIKey != "env_admin_key_32chars_abcdefghijk" {
		t.Errorf("Expected admin API key from env, got: %q", cfg.API.AdminAPIKey)
	}
}

// TestResolveCredentials_MetricsTokenFromEnv verifies that the metrics auth
// token can be resolved from SPIRAL_METRICS_TOKEN environment variable.
func TestResolveCredentials_MetricsTokenFromEnv(t *testing.T) {
	const envToken = "SPIRAL_METRICS_TOKEN"
	origToken := os.Getenv(envToken)
	defer os.Setenv(envToken, origToken)

	os.Setenv(envToken, "env_metrics_token_value")

	cfg := &Config{
		Metrics: MetricsConfig{
			AuthToken: "config_token",
		},
	}

	cfg.ResolveCredentials()

	if cfg.Metrics.AuthToken != "env_metrics_token_value" {
		t.Errorf("Expected metrics token from env, got: %q", cfg.Metrics.AuthToken)
	}
}

// =============================================================================
// Part 3: validatePortConflicts() — additional edge cases
// =============================================================================

// TestValidatePortConflicts_TLSPortConflict verifies that TLS port conflicts
// are detected.
func TestValidatePortConflicts_TLSPortConflict(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
			TLS: TLSConfig{
				Enabled:   true,
				ListenTLS: "0.0.0.0:3333", // Same as stratum!
				CertFile:  "cert.pem",
				KeyFile:   "key.pem",
			},
		},
	}

	err := cfg.validatePortConflicts()
	if err == nil {
		t.Error("Expected port conflict between stratum and TLS on same port")
	} else if !strings.Contains(err.Error(), "3333") {
		t.Errorf("Error should mention port 3333, got: %v", err)
	}
}

// TestValidatePortConflicts_NoConflict_DifferentPorts verifies that distinct
// ports across all services pass validation.
func TestValidatePortConflicts_NoConflict_DifferentPorts(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen:   "0.0.0.0:3333",
			ListenV2: "0.0.0.0:3334",
			TLS: TLSConfig{
				Enabled:   true,
				ListenTLS: "0.0.0.0:3335",
				CertFile:  "cert.pem",
				KeyFile:   "key.pem",
			},
		},
		API: APIConfig{
			Enabled: true,
			Listen:  "0.0.0.0:4000",
		},
		VIP: VIPConfig{
			Enabled:       true,
			DiscoveryPort: 5363,
			StatusPort:    5354,
		},
	}

	err := cfg.validatePortConflicts()
	if err != nil {
		t.Errorf("Expected no conflict with all different ports, got: %v", err)
	}
}

// TestValidatePortConflicts_MultiCoinPortConflict verifies that port conflicts
// between multi-coin stratum ports are detected.
func TestValidatePortConflicts_MultiCoinPortConflict(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
		},
		Coins: []CoinConfig{
			{Enabled: true, Symbol: "BTC", StratumPort: 3334},
			{Enabled: true, Symbol: "DGB", StratumPort: 3334}, // Conflict!
		},
	}

	err := cfg.validatePortConflicts()
	if err == nil {
		t.Error("Expected port conflict between BTC and DGB on port 3334")
	} else if !strings.Contains(err.Error(), "3334") {
		t.Errorf("Error should mention port 3334, got: %v", err)
	}
}

// TestValidatePortConflicts_DisabledCoinIgnored verifies that disabled coins
// do not participate in port conflict detection.
func TestValidatePortConflicts_DisabledCoinIgnored(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
		},
		Coins: []CoinConfig{
			{Enabled: false, Symbol: "BTC", StratumPort: 3333}, // Same as stratum but disabled
		},
	}

	err := cfg.validatePortConflicts()
	if err != nil {
		t.Errorf("Disabled coin should not cause conflict, got: %v", err)
	}
}

// =============================================================================
// Part 4: Address validation
// =============================================================================

// TestValidateCoinAddress_ValidBase58_BTC verifies that a valid Bitcoin
// base58check address passes validation.
func TestValidateCoinAddress_ValidBase58_BTC(t *testing.T) {
	t.Parallel()

	// Real Bitcoin mainnet P2PKH address (1-prefix)
	// This is a well-known burn address that passes checksum validation.
	addr := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa" // Satoshi's address
	err := ValidateCoinAddress(addr, "BTC")
	if err != nil {
		t.Errorf("Valid BTC address should pass validation, got: %v", err)
	}
}

// TestValidateCoinAddress_ValidBech32_BTC verifies that a valid Bitcoin
// bech32 segwit address passes validation.
func TestValidateCoinAddress_ValidBech32_BTC(t *testing.T) {
	t.Parallel()

	// Valid bech32 address format (bc1q...)
	addr := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	err := ValidateCoinAddress(addr, "BTC")
	if err != nil {
		t.Errorf("Valid BTC bech32 address should pass validation, got: %v", err)
	}
}

// TestValidateCoinAddress_ValidBech32_LTC verifies that a valid Litecoin
// bech32 address passes validation.
func TestValidateCoinAddress_ValidBech32_LTC(t *testing.T) {
	t.Parallel()

	// Valid Litecoin bech32 address (ltc1q...)
	addr := "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9"
	err := ValidateCoinAddress(addr, "LTC")
	if err != nil {
		t.Errorf("Valid LTC bech32 address should pass validation, got: %v", err)
	}
}

// TestValidateCoinAddress_ValidBech32_DGB verifies that a valid DigiByte
// bech32 address passes validation.
func TestValidateCoinAddress_ValidBech32_DGB(t *testing.T) {
	t.Parallel()

	// Valid DigiByte bech32 address (dgb1q...)
	addr := "dgb1qw508d6qejxtdg4y5r3zarvary0c5xw7klfenxs"
	err := ValidateCoinAddress(addr, "DGB")
	if err != nil {
		t.Errorf("Valid DGB bech32 address should pass validation, got: %v", err)
	}
}

// TestValidateCoinAddress_InvalidEmpty verifies that an empty address is rejected.
func TestValidateCoinAddress_InvalidEmpty(t *testing.T) {
	t.Parallel()

	err := ValidateCoinAddress("", "BTC")
	if err == nil {
		t.Error("Empty address should be rejected")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("Error should mention 'empty', got: %v", err)
	}
}

// TestValidateCoinAddress_PlaceholderRejected verifies that placeholder
// addresses are rejected.
func TestValidateCoinAddress_PlaceholderRejected(t *testing.T) {
	t.Parallel()

	placeholders := []string{
		"YOUR_ADDRESS_HERE",
		"PENDING_GENERATION",
		"CHANGE_ME_TO_YOUR_ADDRESS",
		"PLACEHOLDER_ADDRESS",
	}

	for _, addr := range placeholders {
		addr := addr
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			err := ValidateCoinAddress(addr, "BTC")
			if err == nil {
				t.Errorf("Placeholder address %q should be rejected", addr)
			}
			if !strings.Contains(err.Error(), "placeholder") {
				t.Errorf("Error should mention 'placeholder', got: %v", err)
			}
		})
	}
}

// TestValidateCoinAddress_InvalidBase58Characters verifies that addresses
// with invalid base58 characters are rejected.
func TestValidateCoinAddress_InvalidBase58Characters(t *testing.T) {
	t.Parallel()

	// Base58 does not include 0, O, I, l
	invalidAddresses := []string{
		"10000000000000000000000000000000", // Contains '0' which is not in base58
		"1OOOOOOOOOOOOOOOOOOOOOOOOOOOOOOO", // Contains 'O' which is not in base58
	}

	for _, addr := range invalidAddresses {
		addr := addr
		t.Run(addr[:8], func(t *testing.T) {
			t.Parallel()
			err := ValidateCoinAddress(addr, "BTC")
			if err == nil {
				t.Errorf("Address with invalid base58 chars should be rejected: %q", addr)
			}
		})
	}
}

// TestValidateCoinAddress_InvalidBech32_MixedCase verifies that mixed-case
// bech32 addresses are rejected (bech32 requires uniform case).
func TestValidateCoinAddress_InvalidBech32_MixedCase(t *testing.T) {
	t.Parallel()

	// Mixed case is invalid in bech32
	addr := "bc1qW508D6QEJXTDG4y5r3zarvary0c5xw7kv8f3t4"
	err := ValidateCoinAddress(addr, "BTC")
	if err == nil {
		t.Error("Mixed-case bech32 address should be rejected")
	}
	if !strings.Contains(err.Error(), "mixed case") {
		t.Errorf("Error should mention mixed case, got: %v", err)
	}
}

// TestValidateCoinAddress_InvalidBech32_TooShort verifies that a truncated
// bech32 address is rejected.
func TestValidateCoinAddress_InvalidBech32_TooShort(t *testing.T) {
	t.Parallel()

	addr := "bc1qw5" // Too short
	err := ValidateCoinAddress(addr, "BTC")
	if err == nil {
		t.Error("Too-short bech32 address should be rejected")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("Error should mention 'too short', got: %v", err)
	}
}

// TestValidateCoinAddress_InvalidBech32_BadChars verifies that bech32 addresses
// with characters outside the bech32 charset are rejected.
func TestValidateCoinAddress_InvalidBech32_BadChars(t *testing.T) {
	t.Parallel()

	// 'b', 'i', 'o' are not in bech32 charset
	addr := "bc1qbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := ValidateCoinAddress(addr, "BTC")
	if err == nil {
		t.Error("Bech32 address with invalid chars should be rejected")
	}
}

// TestValidateCoinAddress_CashAddr_ValidFormat verifies that valid-looking
// Bitcoin Cash CashAddr addresses pass basic validation.
func TestValidateCoinAddress_CashAddr_ValidFormat(t *testing.T) {
	t.Parallel()

	// CashAddr format with prefix
	addr := "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"
	err := ValidateCoinAddress(addr, "BCH")
	if err != nil {
		t.Errorf("Valid CashAddr should pass validation, got: %v", err)
	}
}

// TestValidateCoinAddress_CashAddr_TooShort verifies that truncated CashAddr
// addresses are rejected.
func TestValidateCoinAddress_CashAddr_TooShort(t *testing.T) {
	t.Parallel()

	addr := "q1234" // Too short for CashAddr (needs at least 34 chars)
	err := ValidateCoinAddress(addr, "BCH")
	if err == nil {
		t.Error("Too-short CashAddr should be rejected")
	}
}

// TestValidateBech32Address_ValidAddress verifies validateBech32Address
// with various HRPs.
func TestValidateBech32Address_ValidAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		hrp     string
	}{
		{"BTC bech32", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "bc"},
		{"LTC bech32", "ltc1qw508d6qejxtdg4y5r3zarvary0c5xw7kgmn4n9", "ltc"},
		{"DGB bech32", "dgb1qw508d6qejxtdg4y5r3zarvary0c5xw7klfenxs", "dgb"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateBech32Address(tc.address, tc.hrp)
			if err != nil {
				t.Errorf("Valid bech32 address should pass: %v", err)
			}
		})
	}
}

// TestValidateBech32Address_WrongHRP verifies that bech32 addresses with
// the wrong human-readable prefix are rejected.
func TestValidateBech32Address_WrongHRP(t *testing.T) {
	t.Parallel()

	// Bitcoin address validated as Litecoin
	err := validateBech32Address("bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "ltc")
	if err == nil {
		t.Error("BTC bech32 address with LTC HRP should be rejected")
	}
	if !strings.Contains(err.Error(), "prefix") {
		t.Errorf("Error should mention prefix mismatch, got: %v", err)
	}
}

// TestValidateBech32Address_TooLong verifies that bech32 addresses exceeding
// 90 characters are rejected per BIP173.
func TestValidateBech32Address_TooLong(t *testing.T) {
	t.Parallel()

	// Create a bech32 address longer than 90 chars
	longAddr := "bc1q" + strings.Repeat("q", 90)
	err := validateBech32Address(longAddr, "bc")
	if err == nil {
		t.Error("Bech32 address > 90 chars should be rejected")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("Error should mention 'too long', got: %v", err)
	}
}

// TestBase58Decode_ValidInput verifies that base58 decoding produces correct output.
func TestBase58Decode_ValidInput(t *testing.T) {
	t.Parallel()

	// "1" decodes to 0x00
	decoded, err := base58Decode("1")
	if err != nil {
		t.Fatalf("Failed to decode '1': %v", err)
	}
	if len(decoded) != 1 || decoded[0] != 0 {
		t.Errorf("Expected [0x00], got %v", decoded)
	}
}

// TestBase58Decode_InvalidCharacter verifies that invalid base58 characters
// produce an error.
func TestBase58Decode_InvalidCharacter(t *testing.T) {
	t.Parallel()

	invalidChars := []string{"0", "O", "I", "l", "+", "/", " "}
	for _, c := range invalidChars {
		c := c
		t.Run(fmt.Sprintf("char_%s", c), func(t *testing.T) {
			t.Parallel()
			_, err := base58Decode("1" + c + "1")
			if err == nil {
				t.Errorf("base58Decode should reject character %q", c)
			}
			if !strings.Contains(err.Error(), "invalid base58 character") {
				t.Errorf("Error should mention 'invalid base58 character', got: %v", err)
			}
		})
	}
}

// TestValidateCashAddr_ValidFormats verifies various valid CashAddr formats.
func TestValidateCashAddr_ValidFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
	}{
		{"with prefix", "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"},
		{"q-prefix only", "qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"},
		{"p-prefix only", "ppm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateCashAddr(tc.address)
			if err != nil {
				t.Errorf("Valid CashAddr %q should pass: %v", tc.address, err)
			}
		})
	}
}

// TestValidateCashAddr_InvalidTooShort verifies that short CashAddr is rejected.
func TestValidateCashAddr_InvalidTooShort(t *testing.T) {
	t.Parallel()

	err := validateCashAddr("qshort")
	if err == nil {
		t.Error("Short CashAddr should be rejected")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("Error should mention 'too short', got: %v", err)
	}
}

// TestGetCoinAddressPrefixes_KnownCoins verifies that getCoinAddressPrefixes
// returns expected prefixes for all supported coins.
func TestGetCoinAddressPrefixes_KnownCoins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		coin           string
		expectedLen    int  // Minimum number of prefixes
		containsPrefix byte // At least one expected prefix
	}{
		{"BTC", 2, 0x00},  // P2PKH
		{"BCH", 2, 0x00},  // Legacy
		{"DGB", 2, 0x1E},  // D prefix
		{"LTC", 3, 0x30},  // L prefix
		{"DOGE", 2, 0x1E}, // D prefix
		{"BC2", 2, 0x00},  // Same as BTC
		{"PEP", 2, 0x37},  // P prefix
		{"CAT", 2, 0x15},  // 9 prefix
		{"NMC", 1, 0x34},  // N prefix
		{"SYS", 1, 0x3F},  // S prefix
		{"XMY", 1, 0x32},  // M prefix
		{"FBTC", 2, 0x00}, // Same as BTC
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.coin, func(t *testing.T) {
			t.Parallel()
			prefixes := getCoinAddressPrefixes(tc.coin)
			if len(prefixes) < tc.expectedLen {
				t.Errorf("%s: expected at least %d prefixes, got %d: %v", tc.coin, tc.expectedLen, len(prefixes), prefixes)
			}
			found := false
			for _, p := range prefixes {
				if p == tc.containsPrefix {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: expected prefix 0x%02X in %v", tc.coin, tc.containsPrefix, prefixes)
			}
		})
	}
}

// TestGetCoinAddressPrefixes_UnknownCoin verifies that an unknown coin
// returns nil (skip version check).
func TestGetCoinAddressPrefixes_UnknownCoin(t *testing.T) {
	t.Parallel()

	prefixes := getCoinAddressPrefixes("UNKNOWN_COIN_XYZ")
	if prefixes != nil {
		t.Errorf("Unknown coin should return nil prefixes, got: %v", prefixes)
	}
}

// =============================================================================
// Part 5: GetConfigWarnings() — V1 config
// =============================================================================

// TestGetConfigWarnings_NoWarningsForNormalConfig verifies that a normal
// configuration produces no warnings.
func TestGetConfigWarnings_NoWarningsForNormalConfig(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
			RateLimiting: StratumRateLimitConfig{
				Enabled: true,
			},
		},
	}

	warnings := cfg.GetConfigWarnings()
	if len(warnings) != 0 {
		t.Errorf("Expected no warnings for normal config, got: %v", warnings)
	}
}

// TestGetConfigWarnings_PrivilegedPortWarning verifies that privileged ports
// (< 1024) produce security warnings.
func TestGetConfigWarnings_PrivilegedPortWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantIn  string // Expected substring in at least one warning
	}{
		{
			name: "stratum on port 443",
			cfg: Config{
				Stratum: StratumConfig{Listen: "0.0.0.0:443"},
			},
			wantIn: "443",
		},
		{
			name: "API on port 80",
			cfg: Config{
				Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
				API:     APIConfig{Enabled: true, Listen: "0.0.0.0:80"},
			},
			wantIn: "80",
		},
		{
			name: "stratum V2 on port 22",
			cfg: Config{
				Stratum: StratumConfig{
					Listen:   "0.0.0.0:3333",
					ListenV2: "0.0.0.0:22",
				},
			},
			wantIn: "22",
		},
		{
			name: "VIP discovery on port 53",
			cfg: Config{
				Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
				VIP: VIPConfig{
					Enabled:       true,
					DiscoveryPort: 53,
					StatusPort:    5354,
				},
			},
			wantIn: "53",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			warnings := tc.cfg.GetConfigWarnings()
			found := false
			for _, w := range warnings {
				if strings.Contains(w, tc.wantIn) && strings.Contains(w, "SECURITY") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected warning containing %q and 'SECURITY', got: %v", tc.wantIn, warnings)
			}
		})
	}
}

// TestGetConfigWarnings_Port1024Boundary verifies the boundary: port 1023
// triggers a warning, port 1024 does not.
func TestGetConfigWarnings_Port1024Boundary(t *testing.T) {
	t.Parallel()

	// Port 1023 = privileged
	cfg1023 := &Config{
		Stratum: StratumConfig{Listen: "0.0.0.0:1023"},
	}
	warnings := cfg1023.GetConfigWarnings()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "1023") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Port 1023 should trigger a privileged port warning")
	}

	// Port 1024 = not privileged
	cfg1024 := &Config{
		Stratum: StratumConfig{Listen: "0.0.0.0:1024"},
	}
	warnings = cfg1024.GetConfigWarnings()
	for _, w := range warnings {
		if strings.Contains(w, "1024") {
			t.Errorf("Port 1024 should NOT trigger a privileged port warning, got: %v", w)
		}
	}
}

// TestGetConfigWarnings_RateLimitingDisabled verifies that disabled stratum
// rate limiting produces a security warning.
func TestGetConfigWarnings_RateLimitingDisabled(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
			RateLimiting: StratumRateLimitConfig{
				Enabled: false, // Disabled
			},
		},
	}

	warnings := cfg.GetConfigWarnings()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "rate limiting") && strings.Contains(w, "SECURITY") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Disabled rate limiting should produce a security warning")
	}
}

// TestGetConfigWarnings_MultiCoinPrivilegedPort verifies that per-coin
// stratum ports on privileged ports also produce warnings.
func TestGetConfigWarnings_MultiCoinPrivilegedPort(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Stratum: StratumConfig{
			Listen: "0.0.0.0:3333",
			RateLimiting: StratumRateLimitConfig{
				Enabled: true,
			},
		},
		Coins: []CoinConfig{
			{Enabled: true, Symbol: "BTC", StratumPort: 443}, // Privileged
		},
	}

	warnings := cfg.GetConfigWarnings()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "443") && strings.Contains(w, "BTC") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Privileged per-coin stratum port should produce a warning")
	}
}

// =============================================================================
// Part 6: GetConfigWarnings() — V2 config
// =============================================================================

// TestConfigV2_GetConfigWarnings_PrivilegedPorts verifies V2 config warning
// generation for privileged ports.
func TestConfigV2_GetConfigWarnings_PrivilegedPorts(t *testing.T) {
	t.Parallel()

	cfg := &ConfigV2{
		Version: 2,
		Global: GlobalConfig{
			APIPort:     80,  // Privileged
			MetricsPort: 443, // Privileged
		},
		Database: DatabaseConfig{Host: "localhost"},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "DGB",
				PoolID:  "dgb_mainnet",
				Enabled: true,
				Address: "DTestAddress",
				Stratum: CoinStratumConfig{Port: 22}, // Privileged
				Nodes:   []NodeConfig{{ID: "p", Host: "h"}},
			},
		},
	}

	warnings := cfg.GetConfigWarnings()

	if len(warnings) == 0 {
		t.Error("Expected warnings for privileged ports in V2 config")
	}

	// Check for API port warning
	foundAPI := false
	for _, w := range warnings {
		if strings.Contains(w, "80") && strings.Contains(w, "api") {
			foundAPI = true
			break
		}
	}
	if !foundAPI {
		t.Errorf("Expected warning for privileged API port 80, got: %v", warnings)
	}

	// Check for metrics port warning
	foundMetrics := false
	for _, w := range warnings {
		if strings.Contains(w, "443") && strings.Contains(w, "metrics") {
			foundMetrics = true
			break
		}
	}
	if !foundMetrics {
		t.Errorf("Expected warning for privileged metrics port 443, got: %v", warnings)
	}
}

// TestConfigV2_GetConfigWarnings_NoWarnings verifies that a normal V2 config
// produces no warnings.
func TestConfigV2_GetConfigWarnings_NoWarnings(t *testing.T) {
	t.Parallel()

	cfg := &ConfigV2{
		Version: 2,
		Global: GlobalConfig{
			APIPort:     4000,
			MetricsPort: 9100,
		},
		Database: DatabaseConfig{Host: "localhost"},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "DGB",
				PoolID:  "dgb_mainnet",
				Enabled: true,
				Address: "DTestAddress",
				Stratum: CoinStratumConfig{Port: 3333},
				Nodes:   []NodeConfig{{ID: "p", Host: "h"}},
			},
		},
	}

	warnings := cfg.GetConfigWarnings()
	if len(warnings) != 0 {
		t.Errorf("Expected no warnings for normal V2 config, got: %v", warnings)
	}
}

// =============================================================================
// Part 7: Validate() — Placeholder password detection
// =============================================================================

// TestValidate_PlaceholderPasswordRejected verifies that common placeholder
// passwords from example configs are rejected during validation.
func TestValidate_PlaceholderPasswordRejected(t *testing.T) {
	t.Parallel()

	placeholders := []string{
		"your-database-password",
		"changeme",
		"password123",
		"YOUR_PASSWORD_HERE",
		"CHANGE_ME",
		"CHANGE_THIS_TO_A_STRONG_PASSWORD",
	}

	for _, placeholder := range placeholders {
		placeholder := placeholder
		t.Run(placeholder, func(t *testing.T) {
			t.Parallel()

			cfg := Config{
				Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
				Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:  DaemonConfig{Host: "localhost", User: "user", Password: "real_pass"},
				Database: DatabaseConfig{
					Host:     "localhost",
					User:     "user",
					Password: placeholder, // Placeholder!
				},
			}

			err := cfg.Validate()
			if err == nil {
				t.Errorf("Placeholder password %q should be rejected", placeholder)
			} else if !strings.Contains(err.Error(), "SECURITY") {
				t.Errorf("Error should mention 'SECURITY', got: %v", err)
			}
		})
	}
}

// TestValidate_PlaceholderDaemonPasswordRejected verifies that daemon RPC
// placeholder passwords are also rejected.
func TestValidate_PlaceholderDaemonPasswordRejected(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon: DaemonConfig{
			Host:     "localhost",
			User:     "user",
			Password: "rpcpassword", // Placeholder!
		},
		Database: DatabaseConfig{Host: "localhost", User: "user", Password: "real_pass"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("Placeholder daemon password should be rejected")
	} else if !strings.Contains(err.Error(), "SECURITY") {
		t.Errorf("Error should mention 'SECURITY', got: %v", err)
	}
}

// =============================================================================
// Part 8: V2 Validate() — Placeholder password detection
// =============================================================================

// TestConfigV2_Validate_PlaceholderDBPasswordRejected verifies V2 config
// rejects placeholder database passwords.
func TestConfigV2_Validate_PlaceholderDBPasswordRejected(t *testing.T) {
	t.Parallel()

	cfg := ConfigV2{
		Version: 2,
		Database: DatabaseConfig{
			Host:     "localhost",
			Password: "changeme", // Placeholder!
		},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "DGB",
				PoolID:  "dgb_mainnet",
				Enabled: true,
				Address: "DTestAddress",
				Stratum: CoinStratumConfig{Port: 3333},
				Nodes:   []NodeConfig{{ID: "p", Host: "h"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("Placeholder database password should be rejected in V2 config")
	} else if !strings.Contains(err.Error(), "SECURITY") {
		t.Errorf("Error should mention 'SECURITY', got: %v", err)
	}
}

// TestConfigV2_Validate_PlaceholderNodePasswordRejected verifies V2 config
// rejects placeholder node RPC passwords.
func TestConfigV2_Validate_PlaceholderNodePasswordRejected(t *testing.T) {
	t.Parallel()

	cfg := ConfigV2{
		Version:  2,
		Database: DatabaseConfig{Host: "localhost"},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "DGB",
				PoolID:  "dgb_mainnet",
				Enabled: true,
				Address: "DTestAddress",
				Stratum: CoinStratumConfig{Port: 3333},
				Nodes: []NodeConfig{
					{ID: "primary", Host: "localhost", Password: "password123"}, // Placeholder!
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("Placeholder node password should be rejected in V2 config")
	} else if !strings.Contains(err.Error(), "SECURITY") {
		t.Errorf("Error should mention 'SECURITY', got: %v", err)
	}
}

// =============================================================================
// Part 9: extractSymbolFromCoin()
// =============================================================================

// TestExtractSymbolFromCoin verifies the coin config name to symbol mapping.
func TestExtractSymbolFromCoin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"digibyte", "DGB"},
		{"bitcoin", "BTC"},
		{"bitcoincash", "BCH"},
		{"bitcoinii", "BC2"},
		{"litecoin", "LTC"},
		{"dogecoin", "DOGE"},
		{"fractalbitcoin", "FBTC"},
		{"btc", "BTC"},
		{"bch", "BCH"},
		{"bc2", "BC2"},
		{"dgb-scrypt", "DGB-SCRYPT"},
		// Unknown coins: uppercase the input
		{"unknowncoin", "UNKNOWNCOIN"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			result := extractSymbolFromCoin(tc.input)
			if result != tc.expected {
				t.Errorf("extractSymbolFromCoin(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// =============================================================================
// Part 10: Admin API key minimum length
// =============================================================================

// TestValidate_AdminAPIKey_TooShort verifies that admin API keys shorter than
// 32 characters are rejected (security requirement).
func TestValidate_AdminAPIKey_TooShort(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost", User: "u", Password: "p"},
		Database: DatabaseConfig{Host: "localhost", User: "u", Password: "p"},
		API: APIConfig{
			AdminAPIKey: "tooshort", // Only 8 chars, need 32
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("Short admin API key should be rejected")
	} else if !strings.Contains(err.Error(), "SECURITY") || !strings.Contains(err.Error(), "too short") {
		t.Errorf("Error should mention SECURITY and too short, got: %v", err)
	}
}

// TestValidate_AdminAPIKey_ValidLength verifies that admin API keys at or above
// 32 characters pass validation.
func TestValidate_AdminAPIKey_ValidLength(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost", User: "u", Password: "p"},
		Database: DatabaseConfig{Host: "localhost", User: "u", Password: "p"},
		API: APIConfig{
			AdminAPIKey: strings.Repeat("a", 32), // Exactly 32 chars
		},
	}

	err := cfg.Validate()
	if err != nil && strings.Contains(err.Error(), "admin") {
		t.Errorf("32-char admin API key should be valid, got: %v", err)
	}
}

// =============================================================================
// Part 11: PENDING_GENERATION — Validate() error messages include recovery info
// =============================================================================

// TestValidate_PendingGeneration_V1_SingleCoin verifies that a V1 config with
// PENDING_GENERATION as pool.address produces an error with recovery instructions.
func TestValidate_PendingGeneration_V1_SingleCoin(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "PENDING_GENERATION"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost", User: "u", Password: "p"},
		Database: DatabaseConfig{Host: "localhost", User: "u", Password: "p"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for PENDING_GENERATION address, got nil")
	}

	errStr := err.Error()

	// Must mention PENDING_GENERATION explicitly
	if !strings.Contains(errStr, "PENDING_GENERATION") {
		t.Errorf("Error should mention 'PENDING_GENERATION', got: %v", err)
	}

	// Must include recovery instructions (systemctl restart)
	if !strings.Contains(errStr, "systemctl restart") {
		t.Errorf("Error should mention 'systemctl restart' for recovery, got: %v", err)
	}

	// Must mention manual address setting as fallback
	if !strings.Contains(errStr, "config.yaml") {
		t.Errorf("Error should mention 'config.yaml' for manual address setting, got: %v", err)
	}
}

// TestValidate_PendingGeneration_V1_MultiCoin verifies that a V1 multi-coin config
// with PENDING_GENERATION on an enabled coin produces a clear error.
func TestValidate_PendingGeneration_V1_MultiCoin(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost", User: "u", Password: "p"},
		Database: DatabaseConfig{Host: "localhost", User: "u", Password: "p"},
		Coins: []CoinConfig{
			{
				Name:    "Bitcoin",
				Symbol:  "BTC",
				Enabled: true,
				Address: "PENDING_GENERATION",
				Daemon:  DaemonConfig{Host: "localhost", Port: 8332, User: "u", Password: "p"},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for multi-coin PENDING_GENERATION, got nil")
	}

	errStr := err.Error()

	// Must mention PENDING_GENERATION
	if !strings.Contains(errStr, "PENDING_GENERATION") {
		t.Errorf("Error should mention 'PENDING_GENERATION', got: %v", err)
	}

	// Must identify which coin has the issue
	if !strings.Contains(errStr, "Bitcoin") && !strings.Contains(errStr, "BTC") {
		t.Errorf("Error should identify the affected coin (Bitcoin/BTC), got: %v", err)
	}

	// Must include recovery instructions
	if !strings.Contains(errStr, "systemctl restart") {
		t.Errorf("Error should mention 'systemctl restart' for recovery, got: %v", err)
	}

	if !strings.Contains(errStr, "config.yaml") {
		t.Errorf("Error should mention 'config.yaml' for manual fix, got: %v", err)
	}
}

// TestValidate_PendingGeneration_V2 verifies that V2 config PENDING_GENERATION
// produces an error with recovery instructions.
func TestValidate_PendingGeneration_V2(t *testing.T) {
	t.Parallel()

	cfg := ConfigV2{
		Version:  2,
		Database: DatabaseConfig{Host: "localhost"},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "DGB",
				PoolID:  "dgb_mainnet",
				Enabled: true,
				Address: "PENDING_GENERATION",
				Stratum: CoinStratumConfig{Port: 3333},
				Nodes:   []NodeConfig{{ID: "p", Host: "h"}},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for V2 PENDING_GENERATION, got nil")
	}

	errStr := err.Error()

	if !strings.Contains(errStr, "PENDING_GENERATION") {
		t.Errorf("V2 error should mention 'PENDING_GENERATION', got: %v", err)
	}

	if !strings.Contains(errStr, "DGB") {
		t.Errorf("V2 error should identify coin symbol 'DGB', got: %v", err)
	}

	if !strings.Contains(errStr, "systemctl restart") {
		t.Errorf("V2 error should mention 'systemctl restart', got: %v", err)
	}

	if !strings.Contains(errStr, "config.yaml") {
		t.Errorf("V2 error should mention 'config.yaml', got: %v", err)
	}
}

// TestValidate_PendingGeneration_DisabledCoinIgnored verifies that
// PENDING_GENERATION on a DISABLED coin does NOT cause validation failure.
func TestValidate_PendingGeneration_DisabledCoinIgnored(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost", User: "u", Password: "p"},
		Database: DatabaseConfig{Host: "localhost", User: "u", Password: "p"},
		Coins: []CoinConfig{
			{
				Name:    "Bitcoin",
				Symbol:  "BTC",
				Enabled: false, // Disabled!
				Address: "PENDING_GENERATION",
				Daemon:  DaemonConfig{Host: "localhost", Port: 8332, User: "u", Password: "p"},
			},
		},
	}

	err := cfg.Validate()
	// Should NOT fail due to PENDING_GENERATION on a disabled coin
	if err != nil && strings.Contains(err.Error(), "PENDING_GENERATION") {
		t.Errorf("Disabled coin with PENDING_GENERATION should not cause validation error, got: %v", err)
	}
}
