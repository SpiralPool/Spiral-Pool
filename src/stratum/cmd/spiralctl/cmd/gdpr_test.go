// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"encoding/hex"
	"strings"
	"testing"
)

// =============================================================================
// buildGDPRConnString — SSL mode handling (M2)
// =============================================================================

func TestBuildGDPRConnString_DefaultsToSSLRequire(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "db.example.com",
			Port:     5432,
			User:     "pooladmin",
			Password: "secret",
			Database: "stratum",
			SSLMode:  "", // Empty — should default to "require"
		},
	}

	connStr := buildGDPRConnString(cfg, 5432)

	if !strings.Contains(connStr, "sslmode=require") {
		t.Errorf("expected sslmode=require when SSLMode is empty, got: %s", connStr)
	}
}

func TestBuildGDPRConnString_RespectsExplicitSSLMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sslMode string
		expect  string
	}{
		{"verify-full", "verify-full", "sslmode=verify-full"},
		{"verify-ca", "verify-ca", "sslmode=verify-ca"},
		{"require", "require", "sslmode=require"},
		{"prefer", "prefer", "sslmode=prefer"},
		{"disable", "disable", "sslmode=disable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Database: DatabaseConfig{
					Host:     "db.example.com",
					Port:     5432,
					User:     "pooladmin",
					Password: "secret",
					Database: "stratum",
					SSLMode:  tc.sslMode,
				},
			}

			connStr := buildGDPRConnString(cfg, 5432)
			if !strings.Contains(connStr, tc.expect) {
				t.Errorf("expected %s, got: %s", tc.expect, connStr)
			}
		})
	}
}

func TestBuildGDPRConnString_EscapesSpecialCharsInCredentials(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "db.example.com",
			Port:     5432,
			User:     "user@domain",
			Password: "p@ss:word/123",
			Database: "my db",
			SSLMode:  "require",
		},
	}

	connStr := buildGDPRConnString(cfg, 5432)

	// Special characters should be URL-encoded
	if strings.Contains(connStr, "p@ss:word/123") {
		t.Errorf("password should be URL-encoded, got raw password in: %s", connStr)
	}
	if !strings.Contains(connStr, "db.example.com") {
		t.Errorf("expected host in connection string, got: %s", connStr)
	}
	if !strings.Contains(connStr, "postgres://") {
		t.Errorf("expected postgres:// scheme, got: %s", connStr)
	}
}

func TestBuildGDPRConnString_UsesProvidedPort(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "db.example.com",
			User:     "admin",
			Password: "pass",
			Database: "stratum",
		},
	}

	connStr := buildGDPRConnString(cfg, 15432)
	if !strings.Contains(connStr, ":15432/") {
		t.Errorf("expected port 15432 in connection string, got: %s", connStr)
	}
}

func TestBuildGDPRConnString_NeverHardcodesDisable(t *testing.T) {
	t.Parallel()
	// Regression test: M2 removed the hardcoded sslmode=disable
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "db.example.com",
			Port:     5432,
			User:     "admin",
			Password: "pass",
			Database: "stratum",
			// SSLMode intentionally empty
		},
	}

	connStr := buildGDPRConnString(cfg, 5432)
	if strings.Contains(connStr, "sslmode=disable") {
		t.Errorf("M2 regression: buildGDPRConnString should not default to sslmode=disable, got: %s", connStr)
	}
}

// =============================================================================
// hashForAudit — Salted SHA-256 for GDPR audit logs (M1)
// =============================================================================

func TestHashForAudit_EmptyInputReturnsNone(t *testing.T) {
	t.Parallel()
	result := hashForAudit("", "any-salt")
	if result != "none" {
		t.Errorf("expected 'none' for empty input, got: %s", result)
	}
}

func TestHashForAudit_ProducesValidHexSHA256(t *testing.T) {
	t.Parallel()
	result := hashForAudit("DTestWalletAddress123", "test-salt-abc")

	// SHA-256 produces 32 bytes = 64 hex characters
	if len(result) != 64 {
		t.Errorf("expected 64 hex chars, got %d: %s", len(result), result)
	}

	// Must be valid hex
	if _, err := hex.DecodeString(result); err != nil {
		t.Errorf("not valid hex: %v", err)
	}
}

func TestHashForAudit_DifferentSaltsProduceDifferentHashes(t *testing.T) {
	t.Parallel()
	input := "DTestWalletAddress123"
	hash1 := hashForAudit(input, "salt-one")
	hash2 := hashForAudit(input, "salt-two")

	if hash1 == hash2 {
		t.Error("same input with different salts should produce different hashes")
	}
}

func TestHashForAudit_SameInputSameSaltIsDeterministic(t *testing.T) {
	t.Parallel()
	input := "192.168.1.100"
	salt := "fixed-salt-for-test"
	hash1 := hashForAudit(input, salt)
	hash2 := hashForAudit(input, salt)

	if hash1 != hash2 {
		t.Error("same input and salt should produce the same hash")
	}
}

func TestHashForAudit_DifferentInputsSameSaltDiffer(t *testing.T) {
	t.Parallel()
	salt := "shared-salt"
	hash1 := hashForAudit("wallet-A", salt)
	hash2 := hashForAudit("wallet-B", salt)

	if hash1 == hash2 {
		t.Error("different inputs with same salt should produce different hashes")
	}
}

func TestHashForAudit_SaltIsPrefix(t *testing.T) {
	t.Parallel()
	// Verify the hash includes the salt (salt:input format)
	// hashForAudit("x", "s1") should differ from hashForAudit("s1:x", "")
	h1 := hashForAudit("x", "s1")
	h2 := hashForAudit("s1:x", "")
	// h2 input is "s1:x" with empty salt, so sha256(":s1:x") vs sha256("s1:x")
	// These should be different
	if h1 == h2 {
		t.Error("hash construction should use salt:input format, preventing trivial collisions")
	}
}

// =============================================================================
// generateErasureSalt — Cryptographic salt generation (M1)
// =============================================================================

func TestGenerateErasureSalt_ReturnsValidHex(t *testing.T) {
	t.Parallel()
	salt := generateErasureSalt()

	// 16 bytes = 32 hex characters
	if len(salt) != 32 {
		t.Errorf("expected 32 hex chars (16 bytes), got %d: %s", len(salt), salt)
	}

	if _, err := hex.DecodeString(salt); err != nil {
		t.Errorf("salt is not valid hex: %v", err)
	}
}

func TestGenerateErasureSalt_UniquePerCall(t *testing.T) {
	t.Parallel()
	salts := make(map[string]bool)
	for i := 0; i < 100; i++ {
		salt := generateErasureSalt()
		if salts[salt] {
			t.Fatalf("duplicate salt generated after %d iterations: %s", i, salt)
		}
		salts[salt] = true
	}
}

func TestGenerateErasureSalt_SufficientEntropy(t *testing.T) {
	t.Parallel()
	salt := generateErasureSalt()

	// Decode to verify actual byte length
	bytes, err := hex.DecodeString(salt)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(bytes) != 16 {
		t.Errorf("expected 16 bytes of entropy, got %d", len(bytes))
	}

	// Verify it's not all zeros (would indicate crypto/rand failure fallback)
	allZero := true
	for _, b := range bytes {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("salt is all zeros — crypto/rand may have failed")
	}
}

// =============================================================================
// gdprDataFiles / gdprAdditionalStores — Completeness (M1)
// =============================================================================

func TestGDPRDataFiles_ContainsExpectedPaths(t *testing.T) {
	t.Parallel()
	expected := []string{
		"bans.json",
		"device_hints.json",
		"fleet.json",
	}

	for _, exp := range expected {
		found := false
		for _, f := range gdprDataFiles {
			if strings.Contains(f, exp) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("gdprDataFiles missing expected file: %s", exp)
		}
	}
}

func TestGDPRAdditionalStores_ContainsRedis(t *testing.T) {
	t.Parallel()
	found := false
	for _, store := range gdprAdditionalStores {
		if strings.Contains(store.Name, "Redis") {
			found = true
			break
		}
	}
	if !found {
		t.Error("gdprAdditionalStores missing Redis dedup keys")
	}
}

func TestGDPRAdditionalStores_ContainsWAL(t *testing.T) {
	t.Parallel()
	found := false
	for _, store := range gdprAdditionalStores {
		if strings.Contains(store.Name, "WAL") {
			found = true
			break
		}
	}
	if !found {
		t.Error("gdprAdditionalStores missing WAL files")
	}
}

func TestGDPRAdditionalStores_ContainsPrometheus(t *testing.T) {
	t.Parallel()
	found := false
	for _, store := range gdprAdditionalStores {
		if strings.Contains(store.Name, "Prometheus") {
			found = true
			break
		}
	}
	if !found {
		t.Error("gdprAdditionalStores missing Prometheus metrics")
	}
}
