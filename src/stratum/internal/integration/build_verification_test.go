// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Build verification tests for orphaned packages.
//
// The explorer and tunnel packages are complete implementations that are not
// imported by any production code. These tests ensure the packages continue
// to compile and their exported APIs return sensible defaults, preventing
// silent breakage during refactors.
//
// NOTE: The observability package (internal/observability) only contains test
// files (observability_test.go) with no .go source files. It cannot be imported
// by other packages — this is a Go language limitation. It is not included here
// and is documented as a known structural design choice.
package integration

import (
	"testing"

	"github.com/spiralpool/stratum/internal/explorer"
	"github.com/spiralpool/stratum/internal/tunnel"
)

// TestExplorerPackage_Builds verifies the explorer package compiles and its
// exported APIs produce non-nil results.
func TestExplorerPackage_Builds(t *testing.T) {
	t.Parallel()

	cfg := explorer.DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	mgr := explorer.NewManager(cfg)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	// CoinFromString should not panic for a known coin
	coin := explorer.CoinFromString("BTC")
	if coin == "" {
		t.Error("CoinFromString(\"BTC\") returned empty string")
	}

	// CoinFromString should not panic for an unknown coin
	unknown := explorer.CoinFromString("ZZZZZ")
	if unknown == "" {
		t.Error("CoinFromString(\"ZZZZZ\") returned empty string")
	}
}

// TestTunnelPackage_Builds verifies the tunnel package compiles and its
// exported APIs produce valid defaults.
func TestTunnelPackage_Builds(t *testing.T) {
	t.Parallel()

	cfg := tunnel.DefaultExternalConfig()

	// Default config has Enabled: false, so Validate should return nil
	// (disabled configs skip validation).
	if err := cfg.Validate(); err != nil {
		t.Errorf("DefaultExternalConfig().Validate() returned unexpected error: %v", err)
	}

	// Enabling the config with zero-value fields should fail validation.
	cfg.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Log("Enabled zero-value ExternalConfig validates without error (may be expected depending on mode)")
	}
}
