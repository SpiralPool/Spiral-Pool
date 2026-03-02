// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestValidatePortConflicts tests the port conflict validation logic.
func TestValidatePortConflicts(t *testing.T) {
	// Test 1: No conflicts - different ports for each service
	t.Run("no conflicts", func(t *testing.T) {
		cfg := &Config{
			Pool: PoolConfig{
				ID:      "test",
				Coin:    "digibyte",
				Address: "DTestAddress",
			},
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
			Daemon: DaemonConfig{
				Host: "localhost",
			},
			Database: DatabaseConfig{
				Host: "localhost",
			},
			API: APIConfig{
				Enabled: true,
				Listen:  "0.0.0.0:4000",
			},
		}

		if err := cfg.validatePortConflicts(); err != nil {
			t.Errorf("expected no conflict, got: %v", err)
		}
	})

	// Test 2: Stratum V1 and V2 on same port
	t.Run("stratum v1 v2 conflict", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen:   "0.0.0.0:3333",
				ListenV2: "0.0.0.0:3333", // Same port!
			},
		}

		err := cfg.validatePortConflicts()
		if err == nil {
			t.Error("expected conflict error, got nil")
		} else if !strings.Contains(err.Error(), "port 3333") {
			t.Errorf("expected port 3333 in error, got: %v", err)
		}
	})

	// Test 3: Stratum and API on same port
	t.Run("stratum api conflict", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:3333",
			},
			API: APIConfig{
				Enabled: true,
				Listen:  "0.0.0.0:3333", // Same as stratum!
			},
		}

		err := cfg.validatePortConflicts()
		if err == nil {
			t.Error("expected conflict error, got nil")
		} else if !strings.Contains(err.Error(), "stratum.listen") || !strings.Contains(err.Error(), "api.listen") {
			t.Errorf("expected both services in error, got: %v", err)
		}
	})

	// Test 4: Multi-coin port conflict
	t.Run("multi-coin conflict", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:3333",
			},
			Coins: []CoinConfig{
				{Enabled: true, Symbol: "BTC", StratumPort: 3333}, // Same as primary!
				{Enabled: true, Symbol: "DGB", StratumPort: 3334},
			},
		}

		err := cfg.validatePortConflicts()
		if err == nil {
			t.Error("expected conflict error, got nil")
		} else if !strings.Contains(err.Error(), "BTC") {
			t.Errorf("expected BTC in error, got: %v", err)
		}
	})

	// Test 5: Multi-coin conflict between coins
	t.Run("multi-coin inter-conflict", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:3333",
			},
			Coins: []CoinConfig{
				{Enabled: true, Symbol: "BTC", StratumPort: 3334},
				{Enabled: true, Symbol: "DGB", StratumPort: 3334}, // Same as BTC!
			},
		}

		err := cfg.validatePortConflicts()
		if err == nil {
			t.Error("expected conflict error, got nil")
		} else if !strings.Contains(err.Error(), "3334") {
			t.Errorf("expected port 3334 in error, got: %v", err)
		}
	})

	// Test 6: Disabled coin should not cause conflict
	t.Run("disabled coin no conflict", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:3333",
			},
			Coins: []CoinConfig{
				{Enabled: false, Symbol: "BTC", StratumPort: 3333}, // Same but disabled
			},
		}

		if err := cfg.validatePortConflicts(); err != nil {
			t.Errorf("expected no conflict for disabled coin, got: %v", err)
		}
	})

	// Test 7: VIP ports conflict with stratum
	t.Run("vip stratum conflict", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:5363",
			},
			VIP: VIPConfig{
				Enabled:       true,
				DiscoveryPort: 5363, // Same as stratum!
				StatusPort:    5354,
			},
		}

		err := cfg.validatePortConflicts()
		if err == nil {
			t.Error("expected conflict error, got nil")
		} else if !strings.Contains(err.Error(), "5363") {
			t.Errorf("expected port 5363 in error, got: %v", err)
		}
	})

	// Test 8: Zero port should not be checked
	t.Run("zero port ignored", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen:   "0.0.0.0:3333",
				ListenV2: "", // Empty = 0 port
			},
		}

		if err := cfg.validatePortConflicts(); err != nil {
			t.Errorf("expected no error for zero port, got: %v", err)
		}
	})
}

// =============================================================================
// CORE VALIDATION TESTS
// =============================================================================

func TestValidate_RequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		wantErr     bool
		errContains string
	}{
		{
			name: "missing pool.id",
			config: Config{
				Pool:     PoolConfig{Coin: "digibyte", Address: "DAddr"},
				Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:   DaemonConfig{Host: "localhost"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "pool.id is required",
		},
		{
			name: "missing pool.coin",
			config: Config{
				Pool:     PoolConfig{ID: "test", Address: "DAddr"},
				Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:   DaemonConfig{Host: "localhost"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "pool.coin is required",
		},
		{
			name: "unsupported coin",
			config: Config{
				Pool:     PoolConfig{ID: "test", Coin: "invalidcoin", Address: "DAddr"},
				Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:   DaemonConfig{Host: "localhost"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "unsupported coin",
		},
		{
			name: "missing pool.address",
			config: Config{
				Pool:     PoolConfig{ID: "test", Coin: "digibyte"},
				Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:   DaemonConfig{Host: "localhost"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "pool.address is required",
		},
		{
			name: "missing stratum.listen",
			config: Config{
				Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
				Daemon:   DaemonConfig{Host: "localhost"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "stratum.listen is required",
		},
		{
			name: "missing daemon.host",
			config: Config{
				Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
				Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "daemon.host is required",
		},
		{
			name: "missing database.host",
			config: Config{
				Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
				Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:  DaemonConfig{Host: "localhost"},
			},
			wantErr:     true,
			errContains: "database.host is required",
		},
		{
			name: "valid minimal config",
			config: Config{
				Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
				Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
				Daemon:   DaemonConfig{Host: "localhost"},
				Database: DatabaseConfig{Host: "localhost"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidate_CoinbaseTextLength(t *testing.T) {
	baseConfig := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost"},
		Database: DatabaseConfig{Host: "localhost"},
	}

	tests := []struct {
		name         string
		coinbaseText string
		wantErr      bool
	}{
		{"empty", "", false},
		{"short", "Pool", false},
		{"40 bytes exactly", "1234567890123456789012345678901234567890", false},
		{"41 bytes", "12345678901234567890123456789012345678901", true},
		{"very long", strings.Repeat("x", 100), true},
		{"with emoji (4 bytes)", "🚀Pool", false},          // Emoji is 4 bytes
		{"emoji overflow", strings.Repeat("🚀", 11), true}, // 44 bytes
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig
			cfg.Pool.CoinbaseText = tt.coinbaseText
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "coinbaseText") {
					t.Errorf("expected coinbaseText error, got: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidate_TLSConfig(t *testing.T) {
	baseConfig := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost"},
		Database: DatabaseConfig{Host: "localhost"},
	}

	tests := []struct {
		name        string
		tls         TLSConfig
		wantErr     bool
		errContains string
	}{
		{
			name:    "TLS disabled",
			tls:     TLSConfig{Enabled: false},
			wantErr: false,
		},
		{
			name:        "TLS enabled without cert",
			tls:         TLSConfig{Enabled: true, KeyFile: "key.pem"},
			wantErr:     true,
			errContains: "certFile is required",
		},
		{
			name:        "TLS enabled without key",
			tls:         TLSConfig{Enabled: true, CertFile: "cert.pem"},
			wantErr:     true,
			errContains: "keyFile is required",
		},
		{
			name:    "TLS enabled with both",
			tls:     TLSConfig{Enabled: true, CertFile: "cert.pem", KeyFile: "key.pem"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig
			cfg.Stratum.TLS = tt.tls
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidate_PayoutScheme(t *testing.T) {
	baseConfig := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost"},
		Database: DatabaseConfig{Host: "localhost"},
	}

	tests := []struct {
		name        string
		payments    PaymentsConfig
		wantErr     bool
		errContains string
	}{
		{
			name:     "payments disabled",
			payments: PaymentsConfig{Enabled: false},
			wantErr:  false,
		},
		{
			name:     "SOLO scheme",
			payments: PaymentsConfig{Enabled: true, Scheme: "SOLO"},
			wantErr:  false,
		},
		{
			name:     "solo lowercase",
			payments: PaymentsConfig{Enabled: true, Scheme: "solo"},
			wantErr:  false,
		},
		{
			name:     "empty scheme (defaults to SOLO)",
			payments: PaymentsConfig{Enabled: true, Scheme: ""},
			wantErr:  false,
		},
		{
			name:        "PPLNS rejected (solo only)",
			payments:    PaymentsConfig{Enabled: true, Scheme: "PPLNS"},
			wantErr:     true,
			errContains: "unsupported payout scheme",
		},
		{
			name:        "PPS rejected (solo only)",
			payments:    PaymentsConfig{Enabled: true, Scheme: "PPS"},
			wantErr:     true,
			errContains: "unsupported payout scheme",
		},
		{
			name:        "PROP rejected (solo only)",
			payments:    PaymentsConfig{Enabled: true, Scheme: "PROP"},
			wantErr:     true,
			errContains: "unsupported payout scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig
			cfg.Payments = tt.payments
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidate_MultiCoinConfig(t *testing.T) {
	baseConfig := Config{
		Pool:     PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum:  StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon:   DaemonConfig{Host: "localhost"},
		Database: DatabaseConfig{Host: "localhost"},
	}

	tests := []struct {
		name        string
		coins       []CoinConfig
		wantErr     bool
		errContains string
	}{
		{
			name:    "no additional coins",
			coins:   nil,
			wantErr: false,
		},
		{
			name: "valid coin config",
			coins: []CoinConfig{
				{Name: "Bitcoin", Symbol: "BTC", Enabled: true, Address: "bc1q..."},
			},
			wantErr: false,
		},
		{
			name: "coin with symbol only (name auto-populated)",
			coins: []CoinConfig{
				{Symbol: "DGB", Enabled: true, Address: "DAddr"},
			},
			wantErr: false,
		},
		{
			name: "enabled coin without address",
			coins: []CoinConfig{
				{Name: "Bitcoin", Enabled: true},
			},
			wantErr:     true,
			errContains: "address is required",
		},
		{
			name: "disabled coin without address is OK",
			coins: []CoinConfig{
				{Name: "Bitcoin", Enabled: false},
			},
			wantErr: false,
		},
		{
			name: "coin without name or symbol",
			coins: []CoinConfig{
				{Enabled: true, Address: "addr"},
			},
			wantErr:     true,
			errContains: "name or symbol is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig
			cfg.Coins = tt.coins
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// =============================================================================
// CREDENTIAL MASKING TESTS
// =============================================================================

func TestMaskCredentials(t *testing.T) {
	cfg := &Config{
		Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon: DaemonConfig{
			Host:     "localhost",
			User:     "rpcuser",
			Password: "supersecretpassword",
		},
		Database: DatabaseConfig{
			Host:     "localhost",
			User:     "dbuser",
			Password: "dbpassword123",
		},
		Coins: []CoinConfig{
			{
				Name:    "Bitcoin",
				Enabled: true,
				Address: "bc1q...",
				Daemon: DaemonConfig{
					Host:     "btc-node",
					User:     "btcuser",
					Password: "btcsecret",
				},
			},
		},
	}

	masked := cfg.MaskCredentials()

	// Verify main daemon credentials are masked
	if masked.Daemon.User != "***" {
		t.Errorf("Daemon user should be masked, got: %s", masked.Daemon.User)
	}
	if masked.Daemon.Password != "***" {
		t.Errorf("Daemon password should be masked, got: %s", masked.Daemon.Password)
	}

	// Verify database credentials are masked
	if masked.Database.User != "***" {
		t.Errorf("Database user should be masked, got: %s", masked.Database.User)
	}
	if masked.Database.Password != "***" {
		t.Errorf("Database password should be masked, got: %s", masked.Database.Password)
	}

	// Verify coin daemon credentials are masked
	if len(masked.Coins) > 0 {
		if masked.Coins[0].Daemon.User != "***" {
			t.Errorf("Coin daemon user should be masked, got: %s", masked.Coins[0].Daemon.User)
		}
		if masked.Coins[0].Daemon.Password != "***" {
			t.Errorf("Coin daemon password should be masked, got: %s", masked.Coins[0].Daemon.Password)
		}
	}

	// Verify original config is unchanged
	if cfg.Daemon.User != "rpcuser" {
		t.Error("Original daemon user was modified")
	}
	if cfg.Daemon.Password != "supersecretpassword" {
		t.Error("Original daemon password was modified")
	}
}

func TestMaskCredentials_EmptyCredentials(t *testing.T) {
	cfg := &Config{
		Pool:    PoolConfig{ID: "test", Coin: "digibyte", Address: "DAddr"},
		Stratum: StratumConfig{Listen: "0.0.0.0:3333"},
		Daemon: DaemonConfig{
			Host:     "localhost",
			User:     "", // Empty
			Password: "", // Empty
		},
		Database: DatabaseConfig{
			Host: "localhost",
		},
	}

	masked := cfg.MaskCredentials()

	// Empty credentials should not become "***"
	if masked.Daemon.User == "***" {
		t.Error("Empty daemon user should not be masked to ***")
	}
	if masked.Daemon.Password == "***" {
		t.Error("Empty daemon password should not be masked to ***")
	}
}

// =============================================================================
// PORT CONFLICT TESTS
// =============================================================================

// TestGetConfigWarnings tests the privileged port warning logic.
func TestGetConfigWarnings(t *testing.T) {
	// Test 1: No warnings for normal ports
	t.Run("no warnings for normal ports", func(t *testing.T) {
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
			t.Errorf("expected no warnings, got: %v", warnings)
		}
	})

	// Test 2: Warning for privileged stratum port
	t.Run("privileged stratum port warning", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:443", // Privileged port
			},
		}

		warnings := cfg.GetConfigWarnings()
		if len(warnings) == 0 {
			t.Error("expected warning for privileged port")
		} else if !strings.Contains(warnings[0], "443") || !strings.Contains(warnings[0], "SECURITY") {
			t.Errorf("expected privileged port warning, got: %v", warnings)
		}
	})

	// Test 3: Warning for privileged API port
	t.Run("privileged api port warning", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:3333",
			},
			API: APIConfig{
				Enabled: true,
				Listen:  "0.0.0.0:80", // Privileged port
			},
		}

		warnings := cfg.GetConfigWarnings()
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "80") && strings.Contains(w, "api.listen") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning for privileged API port, got: %v", warnings)
		}
	})

	// Test 4: Multiple privileged port warnings
	t.Run("multiple privileged ports", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen:   "0.0.0.0:22",  // SSH port
				ListenV2: "0.0.0.0:443", // HTTPS port
			},
		}

		warnings := cfg.GetConfigWarnings()
		if len(warnings) < 2 {
			t.Errorf("expected at least 2 warnings, got %d: %v", len(warnings), warnings)
		}
	})

	// Test 5: Port 1024 should not trigger warning (boundary)
	t.Run("port 1024 no warning", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:1024", // First non-privileged port
			},
		}

		warnings := cfg.GetConfigWarnings()
		for _, w := range warnings {
			if strings.Contains(w, "1024") {
				t.Errorf("port 1024 should not trigger warning, got: %v", w)
			}
		}
	})

	// Test 6: Port 1023 should trigger warning (boundary)
	t.Run("port 1023 warning", func(t *testing.T) {
		cfg := &Config{
			Stratum: StratumConfig{
				Listen: "0.0.0.0:1023", // Last privileged port
			},
		}

		warnings := cfg.GetConfigWarnings()
		found := false
		for _, w := range warnings {
			if strings.Contains(w, "1023") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected warning for port 1023")
		}
	})
}

// =============================================================================
// V2 CONFIG VALIDATION TESTS
// =============================================================================

// TestConfigV2_PoolIDValidation tests the pool_id format validation in V2 configs.
// This is critical because invalid pool IDs cause database table creation failures.
func TestConfigV2_PoolIDValidation(t *testing.T) {
	baseConfig := func() ConfigV2 {
		return ConfigV2{
			Version: 2,
			Database: DatabaseConfig{
				Host: "localhost",
			},
			Coins: []CoinPoolConfig{
				{
					Symbol:  "DGB",
					PoolID:  "dgb_mainnet", // Valid default
					Enabled: true,
					Address: "DTestAddress",
					Stratum: CoinStratumConfig{Port: 3333},
					Nodes: []NodeConfig{
						{ID: "primary", Host: "localhost", Port: 14022},
					},
				},
			},
		}
	}

	tests := []struct {
		name        string
		poolID      string
		wantErr     bool
		errContains string
	}{
		// Valid pool IDs
		{"valid underscore", "dgb_mainnet", false, ""},
		{"valid alphanumeric", "dgb123", false, ""},
		{"valid with numbers", "pool1", false, ""},
		{"valid starts with underscore", "_test_pool", false, ""},
		{"valid single char", "a", false, ""},
		{"valid 63 chars", "a23456789012345678901234567890123456789012345678901234567890123", false, ""},

		// Invalid pool IDs - the common mistake
		{"hyphen not allowed", "dgb-sha256", true, "Hint: use underscores instead of hyphens"},
		{"hyphen in middle", "dgb-mainnet", true, "Hint: use underscores instead of hyphens"},

		// Other invalid pool IDs
		{"starts with number", "1pool", true, "invalid"},
		{"contains space", "dgb mainnet", true, "invalid"},
		{"contains dot", "dgb.mainnet", true, "invalid"},
		{"empty string", "", true, "pool_id is required"},
		{"64 chars too long", "a234567890123456789012345678901234567890123456789012345678901234", true, "invalid"},
		{"special chars", "dgb@pool", true, "invalid"},
		{"unicode", "dgb_池", true, "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Coins[0].PoolID = tt.poolID

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for pool_id %q, got nil", tt.poolID)
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error for pool_id %q: %v", tt.poolID, err)
				}
			}
		})
	}
}

// TestConfigV2_ValidateRequiredFields tests required field validation in V2 configs.
func TestConfigV2_ValidateRequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		config      ConfigV2
		wantErr     bool
		errContains string
	}{
		{
			name: "valid minimal config",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{
						Symbol:  "DGB",
						PoolID:  "dgb_mainnet",
						Enabled: true,
						Address: "DTestAddress",
						Stratum: CoinStratumConfig{Port: 3333},
						Nodes:   []NodeConfig{{ID: "primary", Host: "localhost"}},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "version 1 also valid for multi-coin",
			config: ConfigV2{
				Version:  1,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{
						Symbol:  "DGB",
						PoolID:  "dgb_mainnet",
						Enabled: true,
						Address: "DTestAddress",
						Stratum: CoinStratumConfig{Port: 3333},
						Nodes:   []NodeConfig{{ID: "primary", Host: "localhost"}},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid version",
			config: ConfigV2{
				Version:  3,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{Symbol: "DGB", PoolID: "dgb_mainnet", Enabled: true, Address: "addr", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "p", Host: "h"}}},
				},
			},
			wantErr:     true,
			errContains: "expected version 1 or 2",
		},
		{
			name: "no coins",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins:    []CoinPoolConfig{},
			},
			wantErr:     true,
			errContains: "at least one coin must be configured",
		},
		{
			name: "missing database host",
			config: ConfigV2{
				Version: 2,
				Coins: []CoinPoolConfig{
					{Symbol: "DGB", PoolID: "dgb_mainnet", Enabled: true, Address: "addr", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "p", Host: "h"}}},
				},
			},
			wantErr:     true,
			errContains: "database.host is required",
		},
		{
			name: "missing symbol",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{PoolID: "test", Enabled: true, Address: "addr", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "p", Host: "h"}}},
				},
			},
			wantErr:     true,
			errContains: "symbol is required",
		},
		{
			name: "missing address when enabled",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{Symbol: "DGB", PoolID: "dgb_mainnet", Enabled: true, Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "p", Host: "h"}}},
				},
			},
			wantErr:     true,
			errContains: "address is required when enabled",
		},
		{
			name: "missing nodes when enabled",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{Symbol: "DGB", PoolID: "dgb_mainnet", Enabled: true, Address: "addr", Stratum: CoinStratumConfig{Port: 3333}},
				},
			},
			wantErr:     true,
			errContains: "at least one node required",
		},
		{
			name: "duplicate node IDs",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{
						Symbol: "DGB", PoolID: "dgb_mainnet", Enabled: true, Address: "addr",
						Stratum: CoinStratumConfig{Port: 3333},
						Nodes: []NodeConfig{
							{ID: "primary", Host: "h1"},
							{ID: "primary", Host: "h2"}, // Duplicate!
						},
					},
				},
			},
			wantErr:     true,
			errContains: "duplicate node id",
		},
		{
			name: "port conflict between coins",
			config: ConfigV2{
				Version:  2,
				Database: DatabaseConfig{Host: "localhost"},
				Coins: []CoinPoolConfig{
					{Symbol: "DGB", PoolID: "dgb_mainnet", Enabled: true, Address: "addr1", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "p", Host: "h"}}},
					{Symbol: "BTC", PoolID: "btc_mainnet", Enabled: true, Address: "addr2", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "p", Host: "h"}}}, // Same port!
				},
			},
			wantErr:     true,
			errContains: "port 3333 conflict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// =============================================================================
// ZMQ TIMING TESTS
// =============================================================================

// TestGetBlockTimeForCoin tests the block time lookup for all supported coins.
// CRITICAL: These values determine ZMQ timing behavior. Wrong values cause orphans.
func TestGetBlockTimeForCoin(t *testing.T) {
	tests := []struct {
		symbol        string
		expectedTime  int
		caseSensitive bool // Test both upper and lower case
	}{
		// Fast coins (15-30 seconds) - need aggressive ZMQ timing
		{"DGB", 15, true},           // DigiByte - 15 second blocks
		{"DIGIBYTE", 15, true},      // DigiByte alias
		{"DGB-SCRYPT", 15, true},    // DigiByte Scrypt variant
		{"DIGIBYTE-SCRYPT", 15, true},
		{"FBTC", 30, true},          // Fractal BTC - 30 second blocks
		{"FRACTALBTC", 30, true},

		// Medium coins (60 seconds) - balanced ZMQ timing
		{"DOGE", 60, true},          // Dogecoin - 1 minute blocks
		{"DOGECOIN", 60, true},
		{"PEP", 60, true},           // Pepecoin - 1 minute blocks
		{"PEPECOIN", 60, true},
		{"XMY", 60, true},           // Myriad - 1 minute blocks
		{"MYRIAD", 60, true},

		// Slow-medium coins (150 seconds)
		{"LTC", 150, true},          // Litecoin - 2.5 minute blocks
		{"LITECOIN", 150, true},
		{"SYS", 150, true},          // Syscoin - 2.5 minute blocks
		{"SYSCOIN", 150, true},

		// Slow coins (600 seconds) - relaxed ZMQ timing
		{"BTC", 600, true},          // Bitcoin - 10 minute blocks
		{"BITCOIN", 600, true},
		{"BCH", 600, true},          // Bitcoin Cash - 10 minute blocks
		{"BITCOINCASH", 600, true},
		{"BITCOIN-CASH", 600, true},
		{"BC2", 600, true},          // Bitcoin II - 10 minute blocks
		{"BCII", 600, true},
		{"BITCOINII", 600, true},
		{"BITCOIN-II", 600, true},
		{"BITCOIN2", 600, true},
		{"CAT", 600, true},          // Catcoin - 10 minute blocks
		{"CATCOIN", 600, true},
		{"NMC", 600, true},          // Namecoin - 10 minute blocks
		{"NAMECOIN", 600, true},

		// Unknown coins default to Bitcoin-like 10 minute blocks
		{"UNKNOWNCOIN", 600, false},
		{"XYZ", 600, false},
	}

	for _, tt := range tests {
		t.Run(tt.symbol, func(t *testing.T) {
			blockTime := getBlockTimeForCoin(tt.symbol)
			if blockTime != tt.expectedTime {
				t.Errorf("getBlockTimeForCoin(%q) = %d, want %d", tt.symbol, blockTime, tt.expectedTime)
			}

			// Test lowercase version
			if tt.caseSensitive {
				lowerTime := getBlockTimeForCoin(strings.ToLower(tt.symbol))
				if lowerTime != tt.expectedTime {
					t.Errorf("getBlockTimeForCoin(%q) = %d, want %d (case insensitive)", strings.ToLower(tt.symbol), lowerTime, tt.expectedTime)
				}
			}
		})
	}
}

// TestSetZMQTimingDefaults_DGB tests ZMQ timing defaults for DigiByte.
// HYBRID APPROACH: Socket params fixed, health/polling params scale with block time.
func TestSetZMQTimingDefaults_DGB(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 15) // 15s blocks

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}
	if zmq.ReconnectFactor != 2.0 {
		t.Errorf("ReconnectFactor = %v, want 2.0", zmq.ReconnectFactor)
	}

	// Health monitoring (scales with block time for fast coins):
	// DGB: 15s / 3 = 5s (at min)
	if zmq.HealthCheckInterval != 5*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 5s (fast coin detection)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// DGB: 15s × 2 = 30s (hits min 30s)
	if zmq.FailureThreshold != 30*time.Second {
		t.Errorf("FailureThreshold = %v, want 30s (min for fast coins)", zmq.FailureThreshold)
	}
	// DGB: 15s × 4 = 60s = 1 min (hits min 1 min)
	if zmq.StabilityPeriod != 1*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 1m (min for fast coins)", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_BTC tests ZMQ timing defaults for Bitcoin.
// HYBRID APPROACH: Socket params fixed, health/polling params scale with block time.
func TestSetZMQTimingDefaults_BTC(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 600) // 600s blocks

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}
	if zmq.ReconnectFactor != 2.0 {
		t.Errorf("ReconnectFactor = %v, want 2.0", zmq.ReconnectFactor)
	}

	// Health monitoring (scales with block time, capped at 10s):
	// BTC: 600s / 3 = 200s → capped at 10s
	if zmq.HealthCheckInterval != 10*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 10s (max cap)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// BTC: 600s × 2 = 1200s = 20 min → capped at 2 min
	if zmq.FailureThreshold != 2*time.Minute {
		t.Errorf("FailureThreshold = %v, want 2m (max cap for slow coins)", zmq.FailureThreshold)
	}
	// BTC: 600s × 4 = 2400s = 40 min → capped at 5 min
	if zmq.StabilityPeriod != 5*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 5m (max cap for slow coins)", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_LTC tests ZMQ timing defaults for Litecoin.
// HYBRID APPROACH: Socket params fixed, health/polling params scale with block time.
func TestSetZMQTimingDefaults_LTC(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 150) // 150s blocks

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}

	// Health monitoring: 150s / 3 = 50s → capped at 10s
	if zmq.HealthCheckInterval != 10*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 10s (max cap)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// LTC: 150s × 2 = 300s = 5 min → capped at 2 min
	if zmq.FailureThreshold != 2*time.Minute {
		t.Errorf("FailureThreshold = %v, want 2m (max cap)", zmq.FailureThreshold)
	}
	// LTC: 150s × 4 = 600s = 10 min → capped at 5 min
	if zmq.StabilityPeriod != 5*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 5m (max cap)", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_DOGE tests ZMQ timing defaults for Dogecoin.
// HYBRID APPROACH: Socket params fixed, health/polling params scale with block time.
func TestSetZMQTimingDefaults_DOGE(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 60) // 60s blocks

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}

	// Health monitoring: 60s / 3 = 20s → capped at 10s
	if zmq.HealthCheckInterval != 10*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 10s (max cap)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// DOGE: 60s × 2 = 120s = 2 min (exactly at cap)
	if zmq.FailureThreshold != 2*time.Minute {
		t.Errorf("FailureThreshold = %v, want 2m", zmq.FailureThreshold)
	}
	// DOGE: 60s × 4 = 240s = 4 min
	if zmq.StabilityPeriod != 4*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 4m", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_FBTC tests ZMQ timing defaults for Fractal Bitcoin.
// HYBRID APPROACH: Socket params fixed, health/polling params scale with block time.
func TestSetZMQTimingDefaults_FBTC(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 30) // 30s blocks

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}

	// Health monitoring: 30s / 3 = 10s (exactly at max)
	if zmq.HealthCheckInterval != 10*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 10s", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// FBTC: 30s * 2 = 60s = 1 min
	if zmq.FailureThreshold != 1*time.Minute {
		t.Errorf("FailureThreshold = %v, want 1m", zmq.FailureThreshold)
	}
	// FBTC: 30s * 4 = 120s = 2 min
	if zmq.StabilityPeriod != 2*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 2m", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_PreservesCustomValues tests that custom values are preserved.
func TestSetZMQTimingDefaults_PreservesCustomValues(t *testing.T) {
	// Set all custom values
	zmq := &NodeZMQConfig{
		Enabled:             true,
		Endpoint:            "tcp://127.0.0.1:28332",
		ReconnectInitial:    3 * time.Second,
		ReconnectMax:        45 * time.Second,
		ReconnectFactor:     1.8,
		FailureThreshold:    12 * time.Second,
		HealthCheckInterval: 8 * time.Second,
		StabilityPeriod:     3 * time.Minute,
	}

	// Call setZMQTimingDefaults - should NOT overwrite custom values
	setZMQTimingDefaults(zmq, 15) // Even with DGB block time

	// Verify all custom values are preserved
	if zmq.ReconnectInitial != 3*time.Second {
		t.Errorf("Custom ReconnectInitial was overwritten: got %v, want 3s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 45*time.Second {
		t.Errorf("Custom ReconnectMax was overwritten: got %v, want 45s", zmq.ReconnectMax)
	}
	if zmq.ReconnectFactor != 1.8 {
		t.Errorf("Custom ReconnectFactor was overwritten: got %v, want 1.8", zmq.ReconnectFactor)
	}
	if zmq.FailureThreshold != 12*time.Second {
		t.Errorf("Custom FailureThreshold was overwritten: got %v, want 12s", zmq.FailureThreshold)
	}
	if zmq.HealthCheckInterval != 8*time.Second {
		t.Errorf("Custom HealthCheckInterval was overwritten: got %v, want 8s", zmq.HealthCheckInterval)
	}
	if zmq.StabilityPeriod != 3*time.Minute {
		t.Errorf("Custom StabilityPeriod was overwritten: got %v, want 3m", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_VeryFastCoin tests that very fast coins get minimum values.
// HYBRID APPROACH: Socket params fixed, health/polling params hit minimums.
func TestSetZMQTimingDefaults_VeryFastCoin(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 5) // 5s blocks (hypothetical very fast coin)

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}

	// Health monitoring: 5s / 3 = 1.67s → min 5s
	if zmq.HealthCheckInterval != 5*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 5s (min for very fast coins)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time, but hits minimums):
	// 5s × 2 = 10s → min 30s
	if zmq.FailureThreshold != 30*time.Second {
		t.Errorf("FailureThreshold = %v, want 30s (min for very fast coins)", zmq.FailureThreshold)
	}
	// 5s × 4 = 20s → min 1 min
	if zmq.StabilityPeriod != 1*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 1m (min for very fast coins)", zmq.StabilityPeriod)
	}
}

// TestSetZMQTimingDefaults_VerySlowCoin tests that very slow coins get capped values.
// HYBRID APPROACH: Socket params fixed, health/polling params hit caps.
func TestSetZMQTimingDefaults_VerySlowCoin(t *testing.T) {
	zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
	setZMQTimingDefaults(zmq, 1800) // 1800s = 30min blocks (hypothetical very slow coin)

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}

	// Health monitoring: 1800s / 3 = 600s → capped at 10s
	if zmq.HealthCheckInterval != 10*time.Second {
		t.Errorf("HealthCheckInterval = %v, want 10s (max cap)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time, but hits caps):
	// 1800s × 2 = 3600s = 60 min → capped at 2 min
	if zmq.FailureThreshold != 2*time.Minute {
		t.Errorf("FailureThreshold = %v, want 2m (max cap for very slow coins)", zmq.FailureThreshold)
	}
	// 1800s × 4 = 7200s = 120 min → capped at 5 min
	if zmq.StabilityPeriod != 5*time.Minute {
		t.Errorf("StabilityPeriod = %v, want 5m (max cap for very slow coins)", zmq.StabilityPeriod)
	}
}

// TestConfigV2_SetDefaults_ZMQ tests that ZMQ timing defaults are applied in ConfigV2.SetDefaults().
func TestConfigV2_SetDefaults_ZMQ(t *testing.T) {
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
					{
						ID:   "primary",
						Host: "localhost",
						ZMQ: &NodeZMQConfig{
							Enabled:  true,
							Endpoint: "tcp://127.0.0.1:28332",
							// No timing values set - should use defaults
						},
					},
				},
			},
		},
	}

	cfg.SetDefaults()

	// Verify ZMQ timing defaults for DGB (15s blocks)
	zmq := cfg.Coins[0].Nodes[0].ZMQ

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ZMQ ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ZMQ ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}
	if zmq.ReconnectFactor != 2.0 {
		t.Errorf("ZMQ ReconnectFactor = %v, want 2.0", zmq.ReconnectFactor)
	}

	// Health monitoring (scales with block time for fast coins):
	// DGB: 15s / 3 = 5s (at min)
	if zmq.HealthCheckInterval != 5*time.Second {
		t.Errorf("ZMQ HealthCheckInterval = %v, want 5s (DGB fast detection)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// DGB: 15s × 2 = 30s (min)
	if zmq.FailureThreshold != 30*time.Second {
		t.Errorf("ZMQ FailureThreshold = %v, want 30s (DGB min)", zmq.FailureThreshold)
	}
	// DGB: 15s × 4 = 60s = 1 min (min)
	if zmq.StabilityPeriod != 1*time.Minute {
		t.Errorf("ZMQ StabilityPeriod = %v, want 1m (DGB min)", zmq.StabilityPeriod)
	}
}

// TestConfigV2_SetDefaults_ZMQ_BTC tests ZMQ defaults for Bitcoin.
func TestConfigV2_SetDefaults_ZMQ_BTC(t *testing.T) {
	cfg := ConfigV2{
		Version:  2,
		Database: DatabaseConfig{Host: "localhost"},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "BTC",
				PoolID:  "btc_mainnet",
				Enabled: true,
				Address: "bc1qTestAddress",
				Stratum: CoinStratumConfig{Port: 3334},
				Nodes: []NodeConfig{
					{
						ID:   "primary",
						Host: "localhost",
						ZMQ: &NodeZMQConfig{
							Enabled:  true,
							Endpoint: "tcp://127.0.0.1:28332",
						},
					},
				},
			},
		},
	}

	cfg.SetDefaults()

	// Verify ZMQ timing defaults for BTC (600s blocks)
	zmq := cfg.Coins[0].Nodes[0].ZMQ

	// Socket behavior (same for ALL coins):
	if zmq.ReconnectInitial != 1*time.Second {
		t.Errorf("ZMQ ReconnectInitial = %v, want 1s", zmq.ReconnectInitial)
	}
	if zmq.ReconnectMax != 30*time.Second {
		t.Errorf("ZMQ ReconnectMax = %v, want 30s", zmq.ReconnectMax)
	}
	if zmq.ReconnectFactor != 2.0 {
		t.Errorf("ZMQ ReconnectFactor = %v, want 2.0", zmq.ReconnectFactor)
	}

	// Health monitoring: 600s / 3 = 200s → capped at 10s
	if zmq.HealthCheckInterval != 10*time.Second {
		t.Errorf("ZMQ HealthCheckInterval = %v, want 10s (BTC cap)", zmq.HealthCheckInterval)
	}

	// Polling/ZMQ tradeoff (scales with block time):
	// BTC: 600s × 2 = 1200s → capped at 2 min
	if zmq.FailureThreshold != 2*time.Minute {
		t.Errorf("ZMQ FailureThreshold = %v, want 2m (BTC cap)", zmq.FailureThreshold)
	}
	// BTC: 600s × 4 = 2400s → capped at 5 min
	if zmq.StabilityPeriod != 5*time.Minute {
		t.Errorf("ZMQ StabilityPeriod = %v, want 5m (BTC cap)", zmq.StabilityPeriod)
	}
}

// TestConfigV2_SetDefaults_ZMQ_MultiCoin tests ZMQ defaults for multiple coins.
// HYBRID APPROACH: Socket params are same for all, polling/ZMQ tradeoff params scale with block time.
func TestConfigV2_SetDefaults_ZMQ_MultiCoin(t *testing.T) {
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
					{
						ID:   "primary",
						Host: "localhost",
						ZMQ: &NodeZMQConfig{
							Enabled:  true,
							Endpoint: "tcp://127.0.0.1:28332",
						},
					},
				},
			},
			{
				Symbol:  "BTC",
				PoolID:  "btc_mainnet",
				Enabled: true,
				Address: "bc1qTestAddress",
				Stratum: CoinStratumConfig{Port: 3334},
				Nodes: []NodeConfig{
					{
						ID:   "primary",
						Host: "localhost",
						ZMQ: &NodeZMQConfig{
							Enabled:  true,
							Endpoint: "tcp://127.0.0.1:38332",
						},
					},
				},
			},
			{
				Symbol:  "LTC",
				PoolID:  "ltc_mainnet",
				Enabled: true,
				Address: "LTestAddress",
				Stratum: CoinStratumConfig{Port: 3335},
				Nodes: []NodeConfig{
					{
						ID:   "primary",
						Host: "localhost",
						ZMQ: &NodeZMQConfig{
							Enabled:  true,
							Endpoint: "tcp://127.0.0.1:48332",
						},
					},
				},
			},
		},
	}

	cfg.SetDefaults()

	// Socket params should be IDENTICAL for all coins (except HealthCheckInterval which scales)
	for i, coin := range []string{"DGB", "BTC", "LTC"} {
		zmq := cfg.Coins[i].Nodes[0].ZMQ
		if zmq.ReconnectInitial != 1*time.Second {
			t.Errorf("%s ReconnectInitial = %v, want 1s", coin, zmq.ReconnectInitial)
		}
		if zmq.ReconnectMax != 30*time.Second {
			t.Errorf("%s ReconnectMax = %v, want 30s", coin, zmq.ReconnectMax)
		}
		if zmq.ReconnectFactor != 2.0 {
			t.Errorf("%s ReconnectFactor = %v, want 2.0", coin, zmq.ReconnectFactor)
		}
	}

	// HealthCheckInterval scales with block time: clamp(blockTime/3, 5s, 10s)
	// DGB (15s): 15/3 = 5s
	dgbHealthCheck := cfg.Coins[0].Nodes[0].ZMQ.HealthCheckInterval
	if dgbHealthCheck != 5*time.Second {
		t.Errorf("DGB HealthCheckInterval = %v, want 5s", dgbHealthCheck)
	}
	// BTC (600s): 600/3 = 200s → capped at 10s
	btcHealthCheck := cfg.Coins[1].Nodes[0].ZMQ.HealthCheckInterval
	if btcHealthCheck != 10*time.Second {
		t.Errorf("BTC HealthCheckInterval = %v, want 10s (cap)", btcHealthCheck)
	}
	// LTC (150s): 150/3 = 50s → capped at 10s
	ltcHealthCheck := cfg.Coins[2].Nodes[0].ZMQ.HealthCheckInterval
	if ltcHealthCheck != 10*time.Second {
		t.Errorf("LTC HealthCheckInterval = %v, want 10s (cap)", ltcHealthCheck)
	}

	// Polling/ZMQ tradeoff params scale with block time
	// DGB (15s): 15s × 2 = 30s (min), 15s × 4 = 60s = 1min (min)
	dgbZMQ := cfg.Coins[0].Nodes[0].ZMQ
	if dgbZMQ.FailureThreshold != 30*time.Second {
		t.Errorf("DGB FailureThreshold = %v, want 30s (min)", dgbZMQ.FailureThreshold)
	}
	if dgbZMQ.StabilityPeriod != 1*time.Minute {
		t.Errorf("DGB StabilityPeriod = %v, want 1m (min)", dgbZMQ.StabilityPeriod)
	}

	// BTC (600s): 600s × 2 = 1200s → cap 2min, 600s × 4 = 2400s → cap 5min
	btcZMQ := cfg.Coins[1].Nodes[0].ZMQ
	if btcZMQ.FailureThreshold != 2*time.Minute {
		t.Errorf("BTC FailureThreshold = %v, want 2m (cap)", btcZMQ.FailureThreshold)
	}
	if btcZMQ.StabilityPeriod != 5*time.Minute {
		t.Errorf("BTC StabilityPeriod = %v, want 5m (cap)", btcZMQ.StabilityPeriod)
	}

	// LTC (150s): 150s × 2 = 300s → cap 2min, 150s × 4 = 600s → cap 5min
	ltcZMQ := cfg.Coins[2].Nodes[0].ZMQ
	if ltcZMQ.FailureThreshold != 2*time.Minute {
		t.Errorf("LTC FailureThreshold = %v, want 2m (cap)", ltcZMQ.FailureThreshold)
	}
	if ltcZMQ.StabilityPeriod != 5*time.Minute {
		t.Errorf("LTC StabilityPeriod = %v, want 5m (cap)", ltcZMQ.StabilityPeriod)
	}
}

// TestZMQTimingConsistency verifies the HYBRID ZMQ timing approach.
//
// KEY UNDERSTANDING:
// - ZMQ timing reflects socket/node health, NOT blockchain consensus timing
// - ZMQ tells you: "This node observed an event and emitted it over this socket"
// - NOT: "When the blockchain globally finalized the event"
// - Different chains change event volume and load, not ZMQ semantics
//
// HYBRID APPROACH:
// - Socket behavior params (same for all coins): ReconnectInitial, ReconnectMax, ReconnectFactor
// - Health monitoring params (scale for fast coins): HealthCheckInterval = clamp(blockTime/3, 5s, 10s)
// - Polling/ZMQ tradeoff params (scale with block time): FailureThreshold, StabilityPeriod
//
// FORMULAS (from code):
//   HealthCheckInterval = clamp(blockTime / 3, 5s, 10s)
//   FailureThreshold = clamp(blockTime * 2, 30s, 2min)
//   StabilityPeriod  = clamp(blockTime * 4, 1min, 5min)
// All are computed INDEPENDENTLY.
//
// For fast-block coins, we need quick failover to polling to avoid missing blocks.
// For slow-block coins, we can be more patient before declaring ZMQ unhealthy.
func TestZMQTimingConsistency(t *testing.T) {
	// Test various block times with expected values
	// Values derived mechanically from formulas:
	//   HC = clamp(blockTime / 3, 5s, 10s)
	//   FT = clamp(blockTime * 2, 30s, 2min)
	//   SP = clamp(blockTime * 4, 1min, 5min)
	tests := []struct {
		blockTime              int
		expectedHealthCheck    time.Duration
		expectedFailThreshold  time.Duration
		expectedStability      time.Duration
	}{
		{15, 5 * time.Second, 30 * time.Second, 1 * time.Minute},   // DGB: 15/3=5s, 15*2=30s, 15*4=60s→1min
		{30, 10 * time.Second, 1 * time.Minute, 2 * time.Minute},   // FBTC: 30/3=10s, 30*2=60s, 30*4=120s
		{60, 10 * time.Second, 2 * time.Minute, 4 * time.Minute},   // DOGE: 60/3=20s→10s cap, 60*2=120s, 60*4=240s
		{150, 10 * time.Second, 2 * time.Minute, 5 * time.Minute},  // LTC: 150/3=50s→10s cap, 150*2=300s→2min cap, 150*4=600s→5min cap
		{600, 10 * time.Second, 2 * time.Minute, 5 * time.Minute},  // BTC: 600/3=200s→10s cap, 600*2=1200s→2min cap, 600*4=2400s→5min cap
		{1800, 10 * time.Second, 2 * time.Minute, 5 * time.Minute}, // Hypothetical: hits all caps
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("blockTime_%ds", tt.blockTime), func(t *testing.T) {
			zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
			setZMQTimingDefaults(zmq, tt.blockTime)

			// Socket behavior params should be IDENTICAL for all coins
			if zmq.ReconnectInitial != 1*time.Second {
				t.Errorf("blockTime=%d: ReconnectInitial = %v, want 1s", tt.blockTime, zmq.ReconnectInitial)
			}
			if zmq.ReconnectMax != 30*time.Second {
				t.Errorf("blockTime=%d: ReconnectMax = %v, want 30s", tt.blockTime, zmq.ReconnectMax)
			}
			if zmq.ReconnectFactor != 2.0 {
				t.Errorf("blockTime=%d: ReconnectFactor = %v, want 2.0", tt.blockTime, zmq.ReconnectFactor)
			}

			// HealthCheckInterval scales with block time (fast coins need faster detection)
			if zmq.HealthCheckInterval != tt.expectedHealthCheck {
				t.Errorf("blockTime=%d: HealthCheckInterval = %v, want %v", tt.blockTime, zmq.HealthCheckInterval, tt.expectedHealthCheck)
			}

			// Polling/ZMQ tradeoff params scale with block time
			if zmq.FailureThreshold != tt.expectedFailThreshold {
				t.Errorf("blockTime=%d: FailureThreshold = %v, want %v", tt.blockTime, zmq.FailureThreshold, tt.expectedFailThreshold)
			}
			if zmq.StabilityPeriod != tt.expectedStability {
				t.Errorf("blockTime=%d: StabilityPeriod = %v, want %v", tt.blockTime, zmq.StabilityPeriod, tt.expectedStability)
			}
		})
	}
}

// TestZMQTimingInvariants verifies ZMQ timing invariants hold for ANY block time.
// These constraints must always be true regardless of implementation details.
// Future-proofs against refactors that might change formulas but should preserve bounds.
func TestZMQTimingInvariants(t *testing.T) {
	// Constants from the implementation
	const (
		minHealthCheckInterval = 5 * time.Second
		maxHealthCheckInterval = 10 * time.Second
		minFailureThreshold    = 30 * time.Second
		maxFailureThreshold    = 2 * time.Minute
		minStabilityPeriod     = 1 * time.Minute
		maxStabilityPeriod     = 5 * time.Minute
	)

	// Test a wide range of block times including edge cases
	blockTimes := []int{1, 5, 10, 15, 30, 45, 60, 90, 120, 150, 300, 600, 900, 1800, 3600}

	for _, blockTime := range blockTimes {
		t.Run(fmt.Sprintf("blockTime_%ds", blockTime), func(t *testing.T) {
			zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
			setZMQTimingDefaults(zmq, blockTime)

			// Invariant 1: HealthCheckInterval is within bounds
			if zmq.HealthCheckInterval < minHealthCheckInterval {
				t.Errorf("HealthCheckInterval %v < min %v", zmq.HealthCheckInterval, minHealthCheckInterval)
			}
			if zmq.HealthCheckInterval > maxHealthCheckInterval {
				t.Errorf("HealthCheckInterval %v > max %v", zmq.HealthCheckInterval, maxHealthCheckInterval)
			}

			// Invariant 2: FailureThreshold is within bounds
			if zmq.FailureThreshold < minFailureThreshold {
				t.Errorf("FailureThreshold %v < min %v", zmq.FailureThreshold, minFailureThreshold)
			}
			if zmq.FailureThreshold > maxFailureThreshold {
				t.Errorf("FailureThreshold %v > max %v", zmq.FailureThreshold, maxFailureThreshold)
			}

			// Invariant 3: StabilityPeriod is within bounds
			if zmq.StabilityPeriod < minStabilityPeriod {
				t.Errorf("StabilityPeriod %v < min %v", zmq.StabilityPeriod, minStabilityPeriod)
			}
			if zmq.StabilityPeriod > maxStabilityPeriod {
				t.Errorf("StabilityPeriod %v > max %v", zmq.StabilityPeriod, maxStabilityPeriod)
			}

			// Invariant 4: StabilityPeriod >= FailureThreshold
			// (We should trust ZMQ for at least as long as we'd wait before declaring failure)
			if zmq.StabilityPeriod < zmq.FailureThreshold {
				t.Errorf("StabilityPeriod %v < FailureThreshold %v (should be >=)", zmq.StabilityPeriod, zmq.FailureThreshold)
			}

			// Invariant 5: Socket params are always fixed (not affected by block time)
			if zmq.ReconnectInitial != 1*time.Second {
				t.Errorf("ReconnectInitial %v != 1s (should be fixed)", zmq.ReconnectInitial)
			}
			if zmq.ReconnectMax != 30*time.Second {
				t.Errorf("ReconnectMax %v != 30s (should be fixed)", zmq.ReconnectMax)
			}
			if zmq.ReconnectFactor != 2.0 {
				t.Errorf("ReconnectFactor %v != 2.0 (should be fixed)", zmq.ReconnectFactor)
			}
		})
	}
}

// TestZMQTimingScaling verifies that scaled params increase monotonically.
// Longer block times should never result in SHORTER intervals/thresholds.
func TestZMQTimingScaling(t *testing.T) {
	var prevHC, prevFT, prevSP time.Duration

	// Block times in increasing order
	blockTimes := []int{5, 15, 30, 60, 150, 600}

	for i, blockTime := range blockTimes {
		zmq := &NodeZMQConfig{Enabled: true, Endpoint: "tcp://127.0.0.1:28332"}
		setZMQTimingDefaults(zmq, blockTime)

		if i > 0 {
			// HealthCheckInterval should be monotonically non-decreasing
			if zmq.HealthCheckInterval < prevHC {
				t.Errorf("HealthCheckInterval decreased from %v to %v when blockTime increased from %d to %d",
					prevHC, zmq.HealthCheckInterval, blockTimes[i-1], blockTime)
			}
			// FailureThreshold should be monotonically non-decreasing
			if zmq.FailureThreshold < prevFT {
				t.Errorf("FailureThreshold decreased from %v to %v when blockTime increased from %d to %d",
					prevFT, zmq.FailureThreshold, blockTimes[i-1], blockTime)
			}
			// StabilityPeriod should be monotonically non-decreasing
			if zmq.StabilityPeriod < prevSP {
				t.Errorf("StabilityPeriod decreased from %v to %v when blockTime increased from %d to %d",
					prevSP, zmq.StabilityPeriod, blockTimes[i-1], blockTime)
			}
		}

		prevHC = zmq.HealthCheckInterval
		prevFT = zmq.FailureThreshold
		prevSP = zmq.StabilityPeriod
	}
}
