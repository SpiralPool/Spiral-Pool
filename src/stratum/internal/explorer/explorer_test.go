// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package explorer - Tests for block explorer URL generation.
//
// These tests verify:
// - Input validation (block hashes, transaction hashes, addresses)
// - URL generation correctness
// - Security boundary tests (injection prevention)
// - Multi-coin support
// - Fallback explorer behavior
package explorer

import (
	"strings"
	"testing"
)

// TestValidBlockHash verifies block hash validation.
func TestValidBlockHash(t *testing.T) {
	tests := []struct {
		name      string
		hash      string
		wantValid bool
	}{
		// Valid hashes
		{
			name:      "valid lowercase hash",
			hash:      "0000000000000000000000000000000000000000000000000000000000000000",
			wantValid: true,
		},
		{
			name:      "valid uppercase hash",
			hash:      "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
			wantValid: true,
		},
		{
			name:      "valid mixed case hash",
			hash:      "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
			wantValid: true,
		},
		{
			name:      "genesis block hash",
			hash:      "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad2a5a6d4a8b4c3b1c2d",
			wantValid: true,
		},

		// Invalid hashes - security tests
		{
			name:      "too short",
			hash:      "000000000000000000000000000000000000000000000000000000000000000",
			wantValid: false,
		},
		{
			name:      "too long",
			hash:      "00000000000000000000000000000000000000000000000000000000000000000",
			wantValid: false,
		},
		{
			name:      "empty",
			hash:      "",
			wantValid: false,
		},
		{
			name:      "non-hex characters",
			hash:      "000000000000000000000000000000000000000000000000000000000000000g",
			wantValid: false,
		},
		{
			name:      "SQL injection attempt",
			hash:      "0000000000000000000000000000000000000000000000000000'; DROP TABLE--",
			wantValid: false,
		},
		{
			name:      "path traversal",
			hash:      "../../../etc/passwd0000000000000000000000000000000000000000000000",
			wantValid: false,
		},
		{
			name:      "XSS attempt",
			hash:      "<script>alert(1)</script>00000000000000000000000000000000000000",
			wantValid: false,
		},
		{
			name:      "URL injection",
			hash:      "https://evil.com/0000000000000000000000000000000000000000000",
			wantValid: false,
		},
		{
			name:      "null byte",
			hash:      "0000000000000000000000000000000000000000000000000000000000000\x000",
			wantValid: false,
		},
		{
			name:      "newline",
			hash:      "0000000000000000000000000000000000000000000000000000000000000\n00",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validBlockHash.MatchString(tt.hash)
			if got != tt.wantValid {
				t.Errorf("validBlockHash.MatchString(%q) = %v, want %v", tt.hash, got, tt.wantValid)
			}
		})
	}
}

// TestValidTxHash verifies transaction hash validation.
func TestValidTxHash(t *testing.T) {
	tests := []struct {
		name      string
		hash      string
		wantValid bool
	}{
		{
			name:      "valid tx hash",
			hash:      "a1075db55d416d3ca199f55b6084e2115b9345e16c5cf302fc80e9d5fbf5d48d",
			wantValid: true,
		},
		{
			name:      "coinbase tx hash",
			hash:      "4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b",
			wantValid: true,
		},
		{
			name:      "too short",
			hash:      "a1075db55d416d3ca199f55b6084e2115b9345e16c5cf302fc80e9d5fbf5d48",
			wantValid: false,
		},
		{
			name:      "special chars",
			hash:      "a1075db55d416d3ca199f55b6084e2115b9345e16c5cf302fc80e9d5fbf5d4$d",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validTxHash.MatchString(tt.hash)
			if got != tt.wantValid {
				t.Errorf("validTxHash.MatchString(%q) = %v, want %v", tt.hash, got, tt.wantValid)
			}
		})
	}
}

// TestValidAddress verifies address validation.
func TestValidAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		wantValid bool
	}{
		// Valid addresses
		{
			name:      "Bitcoin P2PKH",
			address:   "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			wantValid: true,
		},
		{
			name:      "Bitcoin P2SH",
			address:   "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			wantValid: true,
		},
		{
			name:      "Bitcoin Bech32",
			address:   "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
			wantValid: true,
		},
		{
			name:      "DigiByte D address",
			address:   "DQCPj42jzVq1WbCUvGt6mBSY8PvkBPLXKD",
			wantValid: true,
		},
		{
			name:      "DigiByte S address",
			address:   "SWdQ4LTeFrQ6pP7rR5mYDfGvP7xQJd3Hoy",
			wantValid: true,
		},
		{
			name:      "DigiByte Bech32",
			address:   "dgb1q7up0h29d2swnl3k7q3k5v5qkz4l5n3g4qf4qq8",
			wantValid: true,
		},

		// Invalid addresses
		{
			name:      "too short",
			address:   "1BvBMSEYstWetqTFn5Au4m",
			wantValid: false,
		},
		{
			name:      "too long",
			address:   "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN21BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			wantValid: false,
		},
		{
			name:      "empty",
			address:   "",
			wantValid: false,
		},
		{
			name:      "special chars",
			address:   "1BvBMSEY$tWetqTFn5Au4m4GFg7xJaNVN2",
			wantValid: false,
		},
		{
			name:      "URL injection",
			address:   "https://evil.com/steal",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validAddress.MatchString(tt.address)
			if got != tt.wantValid {
				t.Errorf("validAddress.MatchString(%q) = %v, want %v", tt.address, got, tt.wantValid)
			}
		})
	}
}

// TestNewManager verifies manager creation.
func TestNewManager(t *testing.T) {
	t.Run("with nil config uses defaults", func(t *testing.T) {
		m := NewManager(nil)
		if m == nil {
			t.Fatal("NewManager(nil) returned nil")
		}
		if m.config == nil {
			t.Error("Manager config is nil")
		}

		// Should have default coins
		coins := m.GetSupportedCoins()
		if len(coins) == 0 {
			t.Error("No coins configured")
		}
	})

	t.Run("with custom config", func(t *testing.T) {
		cfg := &ExplorerConfig{
			DefaultExplorer: ExplorerBlockchair,
			Coins: map[CoinType][]Explorer{
				CoinBitcoin: {
					{
						Type:      ExplorerBlockchair,
						Name:      "Test Explorer",
						BaseURL:   "https://test.example.com",
						BlockPath: "/block/{hash}",
						TxPath:    "/tx/{hash}",
						Enabled:   true,
						Priority:  0,
					},
				},
			},
		}
		m := NewManager(cfg)

		coins := m.GetSupportedCoins()
		if len(coins) != 1 {
			t.Errorf("Expected 1 coin, got %d", len(coins))
		}
	})

	t.Run("disabled explorers filtered", func(t *testing.T) {
		cfg := &ExplorerConfig{
			Coins: map[CoinType][]Explorer{
				CoinBitcoin: {
					{Type: "enabled", Enabled: true, Priority: 0},
					{Type: "disabled", Enabled: false, Priority: 1},
				},
			},
		}
		m := NewManager(cfg)

		explorers := m.GetExplorersForCoin(CoinBitcoin)
		if len(explorers) != 1 {
			t.Errorf("Expected 1 enabled explorer, got %d", len(explorers))
		}
		if explorers[0].Type != "enabled" {
			t.Errorf("Wrong explorer: %s", explorers[0].Type)
		}
	})
}

// TestGetBlockURL verifies block URL generation.
func TestGetBlockURL(t *testing.T) {
	m := NewManager(nil)

	t.Run("valid hash returns URL", func(t *testing.T) {
		hash := "0000000000000000000000000000000000000000000000000000000000000000"
		url := m.GetBlockURL(CoinDigiByte, hash)

		if url == "" {
			t.Error("Expected URL, got empty string")
		}
		if !strings.Contains(url, hash) {
			t.Errorf("URL should contain hash: %s", url)
		}
	})

	t.Run("invalid hash returns empty", func(t *testing.T) {
		url := m.GetBlockURL(CoinDigiByte, "invalid")
		if url != "" {
			t.Errorf("Expected empty URL for invalid hash, got: %s", url)
		}
	})

	t.Run("SQL injection returns empty", func(t *testing.T) {
		url := m.GetBlockURL(CoinDigiByte, "'; DROP TABLE--")
		if url != "" {
			t.Errorf("Expected empty URL for SQL injection, got: %s", url)
		}
	})

	t.Run("unsupported coin returns empty", func(t *testing.T) {
		hash := "0000000000000000000000000000000000000000000000000000000000000000"
		url := m.GetBlockURL(CoinType("unsupported"), hash)
		if url != "" {
			t.Errorf("Expected empty URL for unsupported coin, got: %s", url)
		}
	})
}

// TestGetBlockURLByHeight verifies block URL by height generation.
func TestGetBlockURLByHeight(t *testing.T) {
	m := NewManager(nil)

	t.Run("valid height returns URL", func(t *testing.T) {
		url := m.GetBlockURLByHeight(CoinBitcoin, 100000)

		if url == "" {
			t.Error("Expected URL, got empty string")
		}
		if !strings.Contains(url, "100000") {
			t.Errorf("URL should contain height: %s", url)
		}
	})

	t.Run("zero height returns URL", func(t *testing.T) {
		url := m.GetBlockURLByHeight(CoinBitcoin, 0)
		if url == "" {
			t.Error("Expected URL for genesis block")
		}
	})
}

// TestGetTransactionURL verifies transaction URL generation.
func TestGetTransactionURL(t *testing.T) {
	m := NewManager(nil)

	t.Run("valid tx hash returns URL", func(t *testing.T) {
		hash := "a1075db55d416d3ca199f55b6084e2115b9345e16c5cf302fc80e9d5fbf5d48d"
		url := m.GetTransactionURL(CoinBitcoin, hash)

		if url == "" {
			t.Error("Expected URL, got empty string")
		}
		if !strings.Contains(url, hash) {
			t.Errorf("URL should contain tx hash: %s", url)
		}
	})

	t.Run("invalid tx hash returns empty", func(t *testing.T) {
		url := m.GetTransactionURL(CoinBitcoin, "invalid-tx-hash")
		if url != "" {
			t.Errorf("Expected empty URL for invalid tx hash, got: %s", url)
		}
	})
}

// TestGetAddressURL verifies address URL generation.
func TestGetAddressURL(t *testing.T) {
	m := NewManager(nil)

	t.Run("valid address returns URL", func(t *testing.T) {
		address := "DQCPj42jzVq1WbCUvGt6mBSY8PvkBPLXKD"
		url := m.GetAddressURL(CoinDigiByte, address)

		if url == "" {
			t.Error("Expected URL, got empty string")
		}
		if !strings.Contains(url, address) {
			t.Errorf("URL should contain address: %s", url)
		}
	})

	t.Run("invalid address returns empty", func(t *testing.T) {
		url := m.GetAddressURL(CoinDigiByte, "xyz") // Too short
		if url != "" {
			t.Errorf("Expected empty URL for invalid address, got: %s", url)
		}
	})
}

// TestGetBlockLinks verifies multi-explorer link generation.
func TestGetBlockLinks(t *testing.T) {
	m := NewManager(nil)

	t.Run("returns links for all explorers", func(t *testing.T) {
		hash := "0000000000000000000000000000000000000000000000000000000000000000"
		links := m.GetBlockLinks(CoinBitcoin, hash, 0)

		if links == nil {
			t.Fatal("GetBlockLinks returned nil")
		}
		if links.Primary == "" {
			t.Error("Primary link is empty")
		}
		if len(links.All) == 0 {
			t.Error("All links map is empty")
		}
		if links.BlockHash != hash {
			t.Errorf("BlockHash = %s, want %s", links.BlockHash, hash)
		}
	})

	t.Run("by height when no hash", func(t *testing.T) {
		links := m.GetBlockLinks(CoinBitcoin, "", 500000)

		if links == nil {
			t.Fatal("GetBlockLinks returned nil")
		}
		if links.Height != 500000 {
			t.Errorf("Height = %d, want 500000", links.Height)
		}
		if links.Primary == "" {
			t.Error("Primary link is empty")
		}
	})

	t.Run("invalid hash returns nil", func(t *testing.T) {
		links := m.GetBlockLinks(CoinBitcoin, "invalid", 0)
		if links != nil {
			t.Error("Expected nil for invalid hash")
		}
	})
}

// TestGetTransactionLinks verifies multi-explorer transaction links.
func TestGetTransactionLinks(t *testing.T) {
	m := NewManager(nil)

	t.Run("returns links for valid tx", func(t *testing.T) {
		hash := "a1075db55d416d3ca199f55b6084e2115b9345e16c5cf302fc80e9d5fbf5d48d"
		links := m.GetTransactionLinks(CoinBitcoin, hash)

		if links == nil {
			t.Fatal("GetTransactionLinks returned nil")
		}
		if links.TxHash != hash {
			t.Errorf("TxHash = %s, want %s", links.TxHash, hash)
		}
		if links.Primary == "" {
			t.Error("Primary link is empty")
		}
	})

	t.Run("invalid tx hash returns nil", func(t *testing.T) {
		links := m.GetTransactionLinks(CoinBitcoin, "invalid")
		if links != nil {
			t.Error("Expected nil for invalid tx hash")
		}
	})
}

// TestCoinFromString verifies coin type parsing.
func TestCoinFromString(t *testing.T) {
	tests := []struct {
		input string
		want  CoinType
	}{
		{"digibyte", CoinDigiByte},
		{"DGB", CoinDigiByte},
		{"dgb", CoinDigiByte},
		{"DIGIBYTE", CoinDigiByte},
		{"bitcoin", CoinBitcoin},
		{"BTC", CoinBitcoin},
		{"btc", CoinBitcoin},
		{"bitcoincash", CoinBitcoinCash},
		{"BCH", CoinBitcoinCash},
		{"bch", CoinBitcoinCash},
		{"unknown", CoinType("unknown")},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := CoinFromString(tt.input)
			if got != tt.want {
				t.Errorf("CoinFromString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestDefaultConfig verifies default configuration.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	// Check DigiByte is configured
	dgbExplorers, ok := cfg.Coins[CoinDigiByte]
	if !ok {
		t.Error("DigiByte not in default config")
	}
	if len(dgbExplorers) < 2 {
		t.Errorf("Expected at least 2 DGB explorers, got %d", len(dgbExplorers))
	}

	// Check Bitcoin is configured
	btcExplorers, ok := cfg.Coins[CoinBitcoin]
	if !ok {
		t.Error("Bitcoin not in default config")
	}
	if len(btcExplorers) < 2 {
		t.Errorf("Expected at least 2 BTC explorers, got %d", len(btcExplorers))
	}
}

// TestURLConstruction verifies URL paths are correctly constructed.
func TestURLConstruction(t *testing.T) {
	cfg := &ExplorerConfig{
		Coins: map[CoinType][]Explorer{
			CoinBitcoin: {
				{
					Type:        ExplorerBlockchair,
					Name:        "TestExplorer",
					BaseURL:     "https://example.com",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
		},
	}
	m := NewManager(cfg)

	t.Run("block URL construction", func(t *testing.T) {
		hash := "0000000000000000000000000000000000000000000000000000000000000000"
		url := m.GetBlockURL(CoinBitcoin, hash)

		expected := "https://example.com/block/" + hash
		if url != expected {
			t.Errorf("URL = %s, want %s", url, expected)
		}
	})

	t.Run("tx URL construction", func(t *testing.T) {
		hash := "a1075db55d416d3ca199f55b6084e2115b9345e16c5cf302fc80e9d5fbf5d48d"
		url := m.GetTransactionURL(CoinBitcoin, hash)

		expected := "https://example.com/tx/" + hash
		if url != expected {
			t.Errorf("URL = %s, want %s", url, expected)
		}
	})

	t.Run("address URL construction", func(t *testing.T) {
		address := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
		url := m.GetAddressURL(CoinBitcoin, address)

		expected := "https://example.com/address/" + address
		if url != expected {
			t.Errorf("URL = %s, want %s", url, expected)
		}
	})

	t.Run("height URL construction", func(t *testing.T) {
		url := m.GetBlockURLByHeight(CoinBitcoin, 123456)

		expected := "https://example.com/block/123456"
		if url != expected {
			t.Errorf("URL = %s, want %s", url, expected)
		}
	})
}

// TestURLSecurityNoInjection verifies URLs don't allow injection.
func TestURLSecurityNoInjection(t *testing.T) {
	m := NewManager(nil)

	// All these should return empty strings, preventing injection
	injections := []string{
		"'; DROP TABLE blocks--",
		"<script>alert('xss')</script>",
		"../../../etc/passwd",
		"https://evil.com/steal?cookie=",
		"javascript:alert(1)",
		"\x00null_byte",
		"\r\nHeader-Injection: true",
	}

	for _, injection := range injections {
		t.Run("block_"+injection[:min(len(injection), 20)], func(t *testing.T) {
			url := m.GetBlockURL(CoinBitcoin, injection)
			if url != "" {
				t.Errorf("Injection %q should not produce URL, got: %s", injection, url)
			}
		})

		t.Run("tx_"+injection[:min(len(injection), 20)], func(t *testing.T) {
			url := m.GetTransactionURL(CoinBitcoin, injection)
			if url != "" {
				t.Errorf("Injection %q should not produce URL, got: %s", injection, url)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BenchmarkGetBlockURL benchmarks URL generation.
func BenchmarkGetBlockURL(b *testing.B) {
	m := NewManager(nil)
	hash := "0000000000000000000000000000000000000000000000000000000000000000"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.GetBlockURL(CoinBitcoin, hash)
	}
}

// BenchmarkValidation benchmarks input validation.
func BenchmarkValidation(b *testing.B) {
	hash := "0000000000000000000000000000000000000000000000000000000000000000"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		validBlockHash.MatchString(hash)
	}
}
