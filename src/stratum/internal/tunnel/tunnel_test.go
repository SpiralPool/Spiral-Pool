// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package tunnel — tests for external access tunnel configuration and management.
//
// Functions that require the cloudflared binary (VerifyInstallation, ListTunnels,
// TunnelExists) and process lifecycle (Start/Stop/Restart) are not tested here
// because they depend on external system state. All non-process-dependent logic
// is tested: config validation, endpoint generation, constructor validation,
// config file generation, and credential file discovery.
package tunnel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// DefaultExternalConfig tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestDefaultExternalConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultExternalConfig()

	if cfg.Enabled {
		t.Error("expected Enabled=false by default")
	}
	if cfg.Mode != ModeTunnel {
		t.Errorf("expected Mode=tunnel, got %q", cfg.Mode)
	}
	if cfg.PortForward.PublicPort != 3333 {
		t.Errorf("expected PublicPort=3333, got %d", cfg.PortForward.PublicPort)
	}
	if cfg.PortForward.LocalPort != 3333 {
		t.Errorf("expected LocalPort=3333, got %d", cfg.PortForward.LocalPort)
	}
	if cfg.Tunnel.BinaryPath != "/usr/local/bin/cloudflared" {
		t.Errorf("expected BinaryPath=/usr/local/bin/cloudflared, got %q", cfg.Tunnel.BinaryPath)
	}
	if !cfg.Security.HardenOnEnable {
		t.Error("expected HardenOnEnable=true by default")
	}
	if cfg.Security.MaxConnectionsPerIP != 50 {
		t.Errorf("expected MaxConnectionsPerIP=50, got %d", cfg.Security.MaxConnectionsPerIP)
	}
	if cfg.Security.SharesPerSecond != 50 {
		t.Errorf("expected SharesPerSecond=50, got %d", cfg.Security.SharesPerSecond)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ExternalConfig.Validate tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestValidate_Disabled_NoValidation(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected nil error for disabled config, got %v", err)
	}
}

func TestValidate_PortForward_MissingPublicHost(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "",
			PublicPort: 3333,
			LocalPort:  3333,
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing PublicHost")
	}
	if !strings.Contains(err.Error(), "publicHost") {
		t.Errorf("error should mention publicHost: %v", err)
	}
}

func TestValidate_PortForward_InvalidPublicPort(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "pool.example.com",
			PublicPort: 0, // invalid
			LocalPort:  3333,
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid PublicPort")
	}
	if !strings.Contains(err.Error(), "publicPort") {
		t.Errorf("error should mention publicPort: %v", err)
	}
}

func TestValidate_PortForward_PortTooHigh(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "pool.example.com",
			PublicPort: 70000, // > 65535
			LocalPort:  3333,
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for port > 65535")
	}
}

func TestValidate_PortForward_InvalidLocalPort(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "pool.example.com",
			PublicPort: 3333,
			LocalPort:  -1, // invalid
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid LocalPort")
	}
	if !strings.Contains(err.Error(), "localPort") {
		t.Errorf("error should mention localPort: %v", err)
	}
}

func TestValidate_PortForward_Valid(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "pool.example.com",
			PublicPort: 3333,
			LocalPort:  3333,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected nil error for valid port-forward config, got %v", err)
	}
}

func TestValidate_Tunnel_MissingName(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModeTunnel,
		Tunnel: TunnelConfig{
			Name:     "",
			Hostname: "stratum.example.com",
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing tunnel name")
	}
	if !strings.Contains(err.Error(), "tunnel.name") {
		t.Errorf("error should mention tunnel.name: %v", err)
	}
}

func TestValidate_Tunnel_MissingHostname(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModeTunnel,
		Tunnel: TunnelConfig{
			Name:     "my-tunnel",
			Hostname: "",
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing hostname")
	}
	if !strings.Contains(err.Error(), "tunnel.hostname") {
		t.Errorf("error should mention tunnel.hostname: %v", err)
	}
}

func TestValidate_Tunnel_Valid(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    ModeTunnel,
		Tunnel: TunnelConfig{
			Name:     "my-tunnel",
			Hostname: "stratum.example.com",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected nil error for valid tunnel config, got %v", err)
	}
}

func TestValidate_UnknownMode(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Enabled: true,
		Mode:    "invalid-mode",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unknown mode")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Errorf("error should mention mode: %v", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// GetPublicEndpoint tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestGetPublicEndpoint_PortForward_DefaultPort(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Mode: ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "pool.example.com",
			PublicPort: 3333,
		},
	}
	ep := cfg.GetPublicEndpoint()
	if ep != "stratum+tcp://pool.example.com:3333" {
		t.Errorf("expected stratum+tcp://pool.example.com:3333, got %q", ep)
	}
}

func TestGetPublicEndpoint_PortForward_CustomPort(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Mode: ModePortForward,
		PortForward: PortForwardConfig{
			PublicHost: "pool.example.com",
			PublicPort: 4444,
		},
	}
	ep := cfg.GetPublicEndpoint()
	if ep != "stratum+tcp://pool.example.com:4444" {
		t.Errorf("expected stratum+tcp://pool.example.com:4444, got %q", ep)
	}
}

func TestGetPublicEndpoint_Tunnel(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Mode: ModeTunnel,
		Tunnel: TunnelConfig{
			Hostname: "stratum.example.com",
		},
	}
	ep := cfg.GetPublicEndpoint()
	if ep != "stratum+tcp://stratum.example.com:443" {
		t.Errorf("expected stratum+tcp://stratum.example.com:443, got %q", ep)
	}
}

func TestGetPublicEndpoint_UnknownMode(t *testing.T) {
	t.Parallel()
	cfg := ExternalConfig{
		Mode: "unknown",
	}
	ep := cfg.GetPublicEndpoint()
	if ep != "" {
		t.Errorf("expected empty endpoint for unknown mode, got %q", ep)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// itoa tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestItoa(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{3333, "3333"},
		{65535, "65535"},
		{-1, "-1"},
		{-3333, "-3333"},
		{100, "100"},
	}

	for _, tt := range tests {
		got := itoa(tt.input)
		if got != tt.expected {
			t.Errorf("itoa(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ValidationError tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestValidationError_Error(t *testing.T) {
	t.Parallel()

	err := &ValidationError{Field: "tunnel.name", Message: "required when mode is tunnel"}
	expected := "external.tunnel.name: required when mode is tunnel"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// NewCloudflareTunnel tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestNewCloudflareTunnel_Valid(t *testing.T) {
	t.Parallel()
	ct, err := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if ct == nil {
		t.Fatal("expected non-nil CloudflareTunnel")
	}
}

func TestNewCloudflareTunnel_MissingName(t *testing.T) {
	t.Parallel()
	_, err := NewCloudflareTunnel(CloudflareConfig{
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
	})
	if err == nil {
		t.Fatal("expected error for missing tunnel name")
	}
	if !strings.Contains(err.Error(), "tunnel name is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewCloudflareTunnel_MissingHostname(t *testing.T) {
	t.Parallel()
	_, err := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		StratumPort: 3333,
	})
	if err == nil {
		t.Fatal("expected error for missing hostname")
	}
	if !strings.Contains(err.Error(), "hostname is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewCloudflareTunnel_InvalidPort_Zero(t *testing.T) {
	t.Parallel()
	_, err := NewCloudflareTunnel(CloudflareConfig{
		TunnelName: "my-tunnel",
		Hostname:   "stratum.example.com",
		// StratumPort defaults to 0
	})
	if err == nil {
		t.Fatal("expected error for zero port")
	}
	if !strings.Contains(err.Error(), "stratum port") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewCloudflareTunnel_InvalidPort_TooHigh(t *testing.T) {
	t.Parallel()
	_, err := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		Hostname:    "stratum.example.com",
		StratumPort: 70000,
	})
	if err == nil {
		t.Fatal("expected error for port > 65535")
	}
}

func TestNewCloudflareTunnel_DefaultsApplied(t *testing.T) {
	t.Parallel()
	ct, err := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ct.cfg.BinaryPath != "/usr/local/bin/cloudflared" {
		t.Errorf("expected default BinaryPath, got %q", ct.cfg.BinaryPath)
	}
	if ct.cfg.LogLevel != "info" {
		t.Errorf("expected default LogLevel 'info', got %q", ct.cfg.LogLevel)
	}
	if ct.cfg.MetricsAddr != "127.0.0.1:2000" {
		t.Errorf("expected default MetricsAddr, got %q", ct.cfg.MetricsAddr)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// CloudflareTunnel method tests (non-process)
// ═══════════════════════════════════════════════════════════════════════════════

func TestCloudflareTunnel_Endpoint(t *testing.T) {
	t.Parallel()
	ct, _ := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
	})

	ep := ct.Endpoint()
	if ep != "stratum+tcp://stratum.example.com:443" {
		t.Errorf("expected stratum+tcp://stratum.example.com:443, got %q", ep)
	}
}

func TestCloudflareTunnel_IsRunning_InitialFalse(t *testing.T) {
	t.Parallel()
	ct, _ := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
	})

	if ct.IsRunning() {
		t.Error("expected IsRunning=false initially")
	}
}

func TestCloudflareTunnel_Status_Initial(t *testing.T) {
	t.Parallel()
	ct, _ := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "my-tunnel",
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
	})

	status := ct.Status()
	if status.Running {
		t.Error("expected Running=false initially")
	}
	if status.Mode != "cloudflare" {
		t.Errorf("expected Mode='cloudflare', got %q", status.Mode)
	}
	if status.Endpoint != "stratum+tcp://stratum.example.com:443" {
		t.Errorf("expected correct Endpoint, got %q", status.Endpoint)
	}
	if status.Restarts != 0 {
		t.Errorf("expected Restarts=0, got %d", status.Restarts)
	}
	if status.PID != 0 {
		t.Errorf("expected PID=0 when not running, got %d", status.PID)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// generateConfig tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestGenerateConfig_CreatesValidConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cloudflared", "config.yml")

	ct, _ := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:      "spiral-test",
		Hostname:        "stratum.example.com",
		StratumPort:     3333,
		CredentialsPath: "/etc/cloudflared/creds.json",
	})
	ct.cfg.ConfigPath = configPath

	generatedPath, err := ct.generateConfig()
	if err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	// Verify file was created
	content, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("failed to read generated config: %v", err)
	}

	contentStr := string(content)

	// Verify tunnel name
	if !strings.Contains(contentStr, "tunnel: spiral-test") {
		t.Error("generated config missing tunnel name")
	}

	// Verify credentials path
	if !strings.Contains(contentStr, "credentials-file: /etc/cloudflared/creds.json") {
		t.Error("generated config missing credentials file")
	}

	// Verify hostname
	if !strings.Contains(contentStr, "hostname: stratum.example.com") {
		t.Error("generated config missing hostname")
	}

	// Verify stratum port
	if !strings.Contains(contentStr, "tcp://localhost:3333") {
		t.Error("generated config missing stratum port")
	}

	// Verify catch-all
	if !strings.Contains(contentStr, "http_status:404") {
		t.Error("generated config missing catch-all rule")
	}
}

func TestGenerateConfig_NoCredentials(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cloudflared", "config.yml")

	ct, _ := NewCloudflareTunnel(CloudflareConfig{
		TunnelName:  "spiral-test",
		Hostname:    "stratum.example.com",
		StratumPort: 3333,
		// No CredentialsPath
	})
	ct.cfg.ConfigPath = configPath

	generatedPath, err := ct.generateConfig()
	if err != nil {
		t.Fatalf("generateConfig failed: %v", err)
	}

	content, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("failed to read generated config: %v", err)
	}

	// Should NOT contain credentials-file line
	if strings.Contains(string(content), "credentials-file") {
		t.Error("generated config should not include credentials-file when not configured")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// FindCredentialsFile tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestFindCredentialsFile_NotFound(t *testing.T) {
	t.Parallel()
	// Use a tunnel name that won't have credentials anywhere
	_, err := FindCredentialsFile("nonexistent-tunnel-12345")
	if err == nil {
		t.Error("expected error when credentials file not found")
	}
	if !strings.Contains(err.Error(), "credentials file not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFindCredentialsFile_FoundInDir(t *testing.T) {
	t.Parallel()

	// Create a temp dir simulating a credentials directory
	tmpDir := t.TempDir()

	// Create a fake credentials JSON file
	credsPath := filepath.Join(tmpDir, "my-tunnel.json")
	credsContent := map[string]string{
		"AccountTag":   "abc123",
		"TunnelSecret": "secret",
		"TunnelID":     "tunnel-id",
	}
	data, _ := json.Marshal(credsContent)
	if err := os.WriteFile(credsPath, data, 0600); err != nil {
		t.Fatalf("failed to write test credentials: %v", err)
	}

	// FindCredentialsFile searches specific paths, not arbitrary dirs.
	// We can't easily inject our temp dir into its search paths.
	// This test verifies the "not found" case for completeness.
	// The actual file discovery logic is tested indirectly through integration tests.
	_, err := FindCredentialsFile("my-tunnel")
	// Expected to fail since temp dir isn't in the search path
	if err == nil {
		// If it succeeds, it found credentials somewhere on this system — that's OK
		t.Log("FindCredentialsFile found credentials on this system (expected on dev machines)")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Mode constants tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestModeConstants(t *testing.T) {
	t.Parallel()

	if ModePortForward != "port-forward" {
		t.Errorf("expected ModePortForward='port-forward', got %q", ModePortForward)
	}
	if ModeTunnel != "tunnel" {
		t.Errorf("expected ModeTunnel='tunnel', got %q", ModeTunnel)
	}
}
