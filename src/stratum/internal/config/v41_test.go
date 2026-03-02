// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package config

import (
	"strings"
	"testing"
)

// TestV41_SafeConnectionString_MasksPassword verifies that SafeConnectionString
// replaces the actual password with "***" while ConnectionString retains it.
func TestV41_SafeConnectionString_MasksPassword(t *testing.T) {
	t.Parallel()

	cfg := &DatabaseConfig{
		Host:           "db.example.com",
		Port:           5432,
		User:           "pooladmin",
		Password:       "supersecret",
		Database:       "stratum",
		MaxConnections: 20,
		SSLMode:        "require",
	}

	safe := cfg.SafeConnectionString()
	full := cfg.ConnectionString()

	// SafeConnectionString must contain the mask
	if !strings.Contains(safe, "***") {
		t.Errorf("SafeConnectionString should contain '***', got: %s", safe)
	}

	// SafeConnectionString must NOT leak the actual password
	if strings.Contains(safe, "supersecret") {
		t.Errorf("SafeConnectionString must not contain the actual password, got: %s", safe)
	}

	// ConnectionString must contain the actual password
	if !strings.Contains(full, "supersecret") {
		t.Errorf("ConnectionString should contain the actual password, got: %s", full)
	}
}

// TestV41_SafeConnectionString_SpecialChars verifies masking when the password
// contains special characters that would normally be URL-encoded.
func TestV41_SafeConnectionString_SpecialChars(t *testing.T) {
	t.Parallel()

	cfg := &DatabaseConfig{
		Host:           "db.example.com",
		Port:           5432,
		User:           "pooladmin",
		Password:       "p@ss:word/123",
		Database:       "stratum",
		MaxConnections: 20,
		SSLMode:        "require",
	}

	safe := cfg.SafeConnectionString()

	// Must contain the mask
	if !strings.Contains(safe, "***") {
		t.Errorf("SafeConnectionString should contain '***', got: %s", safe)
	}

	// Must not contain the raw password
	if strings.Contains(safe, "p@ss:word/123") {
		t.Errorf("SafeConnectionString must not contain the raw password, got: %s", safe)
	}

	// Must not contain the URL-encoded password either
	// url.QueryEscape("p@ss:word/123") produces "p%40ss%3Aword%2F123"
	if strings.Contains(safe, "p%40ss") {
		t.Errorf("SafeConnectionString must not contain the URL-encoded password, got: %s", safe)
	}
}

// TestV41_SafeConnectionString_EmptyPassword verifies that SafeConnectionString
// still produces a valid masked string even when the password is empty.
func TestV41_SafeConnectionString_EmptyPassword(t *testing.T) {
	t.Parallel()

	cfg := &DatabaseConfig{
		Host:           "db.example.com",
		Port:           5432,
		User:           "pooladmin",
		Password:       "",
		Database:       "stratum",
		MaxConnections: 20,
		SSLMode:        "require",
	}

	safe := cfg.SafeConnectionString()

	// Must still show the mask even for an empty password
	if !strings.Contains(safe, "***") {
		t.Errorf("SafeConnectionString should contain '***' even for empty password, got: %s", safe)
	}

	// Must still be a well-formed connection string
	if !strings.HasPrefix(safe, "postgres://") {
		t.Errorf("SafeConnectionString should start with 'postgres://', got: %s", safe)
	}
}

// TestV41_SafeConnectionString_PreservesOtherFields verifies that host, port,
// database, user, and other config fields appear correctly in the safe string.
func TestV41_SafeConnectionString_PreservesOtherFields(t *testing.T) {
	t.Parallel()

	cfg := &DatabaseConfig{
		Host:           "prod-db.internal",
		Port:           5433,
		User:           "miner",
		Password:       "hunter2",
		Database:       "spiralpool",
		MaxConnections: 50,
		SSLMode:        "verify-full",
	}

	safe := cfg.SafeConnectionString()

	checks := []struct {
		label string
		want  string
	}{
		{"host", "prod-db.internal"},
		{"port", "5433"},
		{"database", "spiralpool"},
		{"user", "miner"},
		{"pool_max_conns", "pool_max_conns=50"},
		{"sslmode", "sslmode=verify-full"},
		{"password mask", ":***@"},
	}

	for _, c := range checks {
		if !strings.Contains(safe, c.want) {
			t.Errorf("SafeConnectionString missing %s (%q), got: %s", c.label, c.want, safe)
		}
	}

	// Password must be masked
	if strings.Contains(safe, "hunter2") {
		t.Errorf("SafeConnectionString must not contain the actual password, got: %s", safe)
	}
}
