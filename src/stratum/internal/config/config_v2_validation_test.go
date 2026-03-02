// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package config

import (
	"os"
	"strings"
	"testing"
)

// =============================================================================
// Placeholder Password Detection (M14)
// =============================================================================

func TestV2Validate_RejectsPlaceholderDatabasePasswords(t *testing.T) {
	t.Parallel()
	placeholders := []string{
		"your-database-password",
		"your-password",
		"your_password",
		"rpcpassword",
		"changeme",
		"password123",
		"YOUR_PASSWORD_HERE",
		"CHANGE_ME",
		"CHANGE_THIS_TO_A_STRONG_PASSWORD",
	}

	for _, placeholder := range placeholders {
		t.Run("db_"+placeholder, func(t *testing.T) {
			t.Parallel()
			cfg := minimalValidV2Config()
			cfg.Database.Password = placeholder

			err := cfg.Validate()
			if err == nil {
				t.Errorf("expected validation error for placeholder password %q, got nil", placeholder)
			} else if !strings.Contains(err.Error(), "SECURITY") {
				t.Errorf("expected SECURITY error for %q, got: %v", placeholder, err)
			}
		})
	}
}

func TestV2Validate_RejectsPlaceholderPasswordsCaseInsensitive(t *testing.T) {
	t.Parallel()
	// Test that "CHANGEME", "Changeme", "changeme" are all rejected
	variants := []string{"CHANGEME", "Changeme", "changeme", "ChangEMe"}

	for _, v := range variants {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			cfg := minimalValidV2Config()
			cfg.Database.Password = v

			err := cfg.Validate()
			if err == nil {
				t.Errorf("expected validation error for %q, got nil", v)
			}
		})
	}
}

func TestV2Validate_RejectsPlaceholderNodePasswords(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Coins[0].Nodes[0].Password = "changeme"

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for placeholder node password")
	} else if !strings.Contains(err.Error(), "SECURITY") {
		t.Errorf("expected SECURITY error, got: %v", err)
	}
}

func TestV2Validate_AcceptsStrongDatabasePassword(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Database.Password = "a7f3b2c9e1d8f4a6b5c3d7e2f9a1b4c8"

	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error for strong password, got: %v", err)
	}
}

func TestV2Validate_AcceptsEmptyDatabasePassword(t *testing.T) {
	t.Parallel()
	// Empty password should not trigger placeholder check
	// (it will likely fail at the DB connection level, but config validation should pass)
	cfg := minimalValidV2Config()
	cfg.Database.Password = ""

	err := cfg.Validate()
	if err != nil && strings.Contains(err.Error(), "placeholder") {
		t.Errorf("empty password should not trigger placeholder check, got: %v", err)
	}
}

// =============================================================================
// Admin API Key Minimum Length (M3)
// =============================================================================

func TestV2Validate_RejectsShortAdminAPIKey(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.AdminAPIKey = "short-key" // Only 9 chars

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for short API key")
	} else if !strings.Contains(err.Error(), "too short") {
		t.Errorf("expected 'too short' error, got: %v", err)
	}
}

func TestV2Validate_RejectsAPIKeyAt31Chars(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.AdminAPIKey = strings.Repeat("a", 31) // 31 chars, below 32 minimum

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for 31-char API key")
	}
}

func TestV2Validate_AcceptsAPIKeyAt32Chars(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.AdminAPIKey = strings.Repeat("a", 32) // Exactly 32 chars

	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error for 32-char API key, got: %v", err)
	}
}

func TestV2Validate_AcceptsAPIKeyAt64Chars(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.AdminAPIKey = strings.Repeat("a", 64) // 64 chars (openssl rand -hex 32 output)

	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error for 64-char API key, got: %v", err)
	}
}

func TestV2Validate_AcceptsEmptyAdminAPIKey(t *testing.T) {
	t.Parallel()
	// Empty key means API auth is disabled — should not error
	cfg := minimalValidV2Config()
	cfg.Global.AdminAPIKey = ""

	err := cfg.Validate()
	if err != nil && strings.Contains(err.Error(), "admin_api_key") {
		t.Errorf("empty API key should not trigger length check, got: %v", err)
	}
}

// =============================================================================
// Webhook URL HTTPS Validation (M10) — Non-fatal warning
// =============================================================================

func TestV2Validate_AcceptsHTTPSWebhook(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "https://discord.com/api/webhooks/123/token"},
	}

	// HTTPS webhook should not cause any validation error
	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error for HTTPS webhook, got: %v", err)
	}
}

func TestV2Validate_DoesNotErrorOnHTTPWebhook(t *testing.T) {
	t.Parallel()
	// M10 is a WARNING, not an error — Validate() should still pass
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "http://hooks.internal.example.com/alert"},
	}

	err := cfg.Validate()
	if err != nil {
		t.Errorf("HTTP webhook should produce a warning, not a validation error, got: %v", err)
	}
}

// =============================================================================
// ResolveWebhookCredentials — Environment variable override (M4)
// =============================================================================

func TestResolveWebhookCredentials_OverridesDiscordURL(t *testing.T) {
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "https://discord.com/api/webhooks/old-id/old-token"},
	}

	os.Setenv("SPIRAL_DISCORD_WEBHOOK_URL", "https://discord.com/api/webhooks/new-id/new-token")
	defer os.Unsetenv("SPIRAL_DISCORD_WEBHOOK_URL")

	cfg.ResolveWebhookCredentials()

	if cfg.Global.Sentinel.Webhooks[0].URL != "https://discord.com/api/webhooks/new-id/new-token" {
		t.Errorf("expected Discord URL to be overridden, got: %s", cfg.Global.Sentinel.Webhooks[0].URL)
	}
}

func TestResolveWebhookCredentials_OverridesTelegramToken(t *testing.T) {
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "https://api.telegram.org/botOLD_TOKEN/sendMessage", ChatID: "12345"},
	}

	os.Setenv("SPIRAL_TELEGRAM_BOT_TOKEN", "NEW_BOT_TOKEN")
	defer os.Unsetenv("SPIRAL_TELEGRAM_BOT_TOKEN")

	cfg.ResolveWebhookCredentials()

	expectedURL := "https://api.telegram.org/botNEW_BOT_TOKEN/sendMessage"
	if cfg.Global.Sentinel.Webhooks[0].URL != expectedURL {
		t.Errorf("expected Telegram URL with new token, got: %s", cfg.Global.Sentinel.Webhooks[0].URL)
	}
	if cfg.Global.Sentinel.Webhooks[0].Token != "NEW_BOT_TOKEN" {
		t.Errorf("expected Token field to be set, got: %s", cfg.Global.Sentinel.Webhooks[0].Token)
	}
}

func TestResolveWebhookCredentials_OverridesTelegramChatID(t *testing.T) {
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "https://api.telegram.org/botTOKEN/sendMessage", ChatID: "old-chat"},
	}

	os.Setenv("SPIRAL_TELEGRAM_CHAT_ID", "new-chat-id")
	defer os.Unsetenv("SPIRAL_TELEGRAM_CHAT_ID")

	cfg.ResolveWebhookCredentials()

	if cfg.Global.Sentinel.Webhooks[0].ChatID != "new-chat-id" {
		t.Errorf("expected chat_id override, got: %s", cfg.Global.Sentinel.Webhooks[0].ChatID)
	}
}

func TestResolveWebhookCredentials_SkipsWhenSentinelDisabled(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = false
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "https://discord.com/api/webhooks/id/token"},
	}

	os.Setenv("SPIRAL_DISCORD_WEBHOOK_URL", "https://discord.com/api/webhooks/new/new")
	defer os.Unsetenv("SPIRAL_DISCORD_WEBHOOK_URL")

	cfg.ResolveWebhookCredentials()

	// Should NOT be overridden since sentinel is disabled
	if cfg.Global.Sentinel.Webhooks[0].URL != "https://discord.com/api/webhooks/id/token" {
		t.Errorf("expected URL unchanged when sentinel disabled, got: %s", cfg.Global.Sentinel.Webhooks[0].URL)
	}
}

func TestResolveWebhookCredentials_DoesNotAffectNonMatchingEndpoints(t *testing.T) {
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.Global.Sentinel.Webhooks = []WebhookConfig{
		{URL: "https://hooks.example.com/custom-webhook"},
	}

	os.Setenv("SPIRAL_DISCORD_WEBHOOK_URL", "https://discord.com/api/webhooks/new/new")
	defer os.Unsetenv("SPIRAL_DISCORD_WEBHOOK_URL")

	cfg.ResolveWebhookCredentials()

	// Custom webhook should not be affected by Discord env var
	if cfg.Global.Sentinel.Webhooks[0].URL != "https://hooks.example.com/custom-webhook" {
		t.Errorf("expected custom webhook URL unchanged, got: %s", cfg.Global.Sentinel.Webhooks[0].URL)
	}
}

// =============================================================================
// isDiscordWebhookURL / isTelegramWebhookURL — URL detection helpers
// =============================================================================

func TestIsDiscordWebhookURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url    string
		expect bool
	}{
		{"https://discord.com/api/webhooks/123/abc", true},
		{"https://discordapp.com/api/webhooks/123/abc", true},
		{"https://api.telegram.org/bot123/sendMessage", false},
		{"https://example.com/webhook", false},
		{"", false},
	}

	for _, tc := range tests {
		if got := isDiscordWebhookURL(tc.url); got != tc.expect {
			t.Errorf("isDiscordWebhookURL(%q) = %v, want %v", tc.url, got, tc.expect)
		}
	}
}

func TestIsTelegramWebhookURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url    string
		expect bool
	}{
		{"https://api.telegram.org/bot123:TOKEN/sendMessage", true},
		{"https://api.telegram.org/botABC/sendMessage", true},
		{"https://discord.com/api/webhooks/123/abc", false},
		{"https://example.com/webhook", false},
		{"", false},
	}

	for _, tc := range tests {
		if got := isTelegramWebhookURL(tc.url); got != tc.expect {
			t.Errorf("isTelegramWebhookURL(%q) = %v, want %v", tc.url, got, tc.expect)
		}
	}
}

// =============================================================================
// V2 Config Validation — Structural integrity
// =============================================================================

func TestV2Validate_RejectsNoCoins(t *testing.T) {
	t.Parallel()
	cfg := &ConfigV2{
		Version:  2,
		Database: DatabaseConfig{Host: "localhost"},
		Coins:    []CoinPoolConfig{},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for config with no coins")
	}
}

func TestV2Validate_RejectsDuplicatePoolIDs(t *testing.T) {
	t.Parallel()
	cfg := &ConfigV2{
		Version:  2,
		Database: DatabaseConfig{Host: "localhost", Password: "a7f3b2c9e1d8f4a6b5c3d7e2f9a1b4c8"},
		Coins: []CoinPoolConfig{
			{Symbol: "DGB", PoolID: "dgb_sha256", Enabled: true, Address: "DAddr", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "n1", Host: "localhost", Password: "a7f3b2c9e1d8f4a6"}}},
			{Symbol: "BTC", PoolID: "dgb_sha256", Enabled: true, Address: "BAddr", Stratum: CoinStratumConfig{Port: 3334}, Nodes: []NodeConfig{{ID: "n1", Host: "localhost", Password: "a7f3b2c9e1d8f4a6"}}},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for duplicate pool IDs")
	} else if !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("expected pool_id conflict error, got: %v", err)
	}
}

func TestV2Validate_RejectsPortConflicts(t *testing.T) {
	t.Parallel()
	cfg := &ConfigV2{
		Version:  2,
		Database: DatabaseConfig{Host: "localhost", Password: "a7f3b2c9e1d8f4a6b5c3d7e2f9a1b4c8"},
		Coins: []CoinPoolConfig{
			{Symbol: "DGB", PoolID: "dgb_sha256", Enabled: true, Address: "DAddr", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "n1", Host: "localhost", Password: "a7f3b2c9e1d8f4a6"}}},
			{Symbol: "BTC", PoolID: "btc_sha256", Enabled: true, Address: "BAddr", Stratum: CoinStratumConfig{Port: 3333}, Nodes: []NodeConfig{{ID: "n1", Host: "localhost", Password: "a7f3b2c9e1d8f4a6"}}},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for port conflict")
	} else if !strings.Contains(err.Error(), "port 3333") {
		t.Errorf("expected port conflict error, got: %v", err)
	}
}

func TestV2Validate_RejectsInvalidPoolID(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Coins[0].PoolID = "dgb-sha256" // Hyphens not allowed

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for pool_id with hyphens")
	} else if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected 'invalid' error, got: %v", err)
	}
}

// =============================================================================
// SetDefaults — Default value application
// =============================================================================

func TestV2SetDefaults_SentinelDefaults(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.Sentinel.Enabled = true
	cfg.SetDefaults()

	if cfg.Global.Sentinel.CheckInterval == 0 {
		t.Error("expected CheckInterval default to be set")
	}
	if cfg.Global.Sentinel.WALStuckThreshold == 0 {
		t.Error("expected WALStuckThreshold default to be set")
	}
	if cfg.Global.Sentinel.MinPeerCount == 0 {
		t.Error("expected MinPeerCount default to be set")
	}
}

func TestV2SetDefaults_GlobalDefaults(t *testing.T) {
	t.Parallel()
	cfg := minimalValidV2Config()
	cfg.Global.LogLevel = ""
	cfg.Global.LogFormat = ""
	cfg.SetDefaults()

	if cfg.Global.LogLevel != "info" {
		t.Errorf("expected default log level 'info', got %q", cfg.Global.LogLevel)
	}
	if cfg.Global.LogFormat != "json" {
		t.Errorf("expected default log format 'json', got %q", cfg.Global.LogFormat)
	}
}

// =============================================================================
// Helper: minimal valid V2 config for testing
// =============================================================================

func minimalValidV2Config() *ConfigV2 {
	return &ConfigV2{
		Version: 2,
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "pooladmin",
			Password: "a7f3b2c9e1d8f4a6b5c3d7e2f9a1b4c8",
			Database: "stratum",
		},
		Coins: []CoinPoolConfig{
			{
				Symbol:  "DGB",
				PoolID:  "dgb_sha256",
				Enabled: true,
				Address: "DTestAddress123",
				Stratum: CoinStratumConfig{
					Port: 3333,
				},
				Nodes: []NodeConfig{
					{
						ID:       "primary",
						Host:     "localhost",
						Port:     14022,
						User:     "rpcuser",
						Password: "a7f3b2c9e1d8f4a6b5c3d7e2f9a1b4c8",
					},
				},
			},
		},
	}
}
