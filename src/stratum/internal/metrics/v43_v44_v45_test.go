// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Tests for V43, V44, and V45 audit fixes in the metrics package.
//
// V43: Aux block submission metrics (merge mining observability)
// V44: Metrics server failure tracking (serverFailed atomic flag)
// V45: Health check callback (wired by pool.go for real subsystem health)
package metrics

import (
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// sharedMetrics provides a single Metrics instance for all V-audit tests.
// Prometheus does not allow duplicate metric registration in the default
// registry, so New() must be called exactly once per process.
var (
	sharedMetrics     *Metrics
	sharedMetricsOnce sync.Once
)

func getSharedMetrics(t *testing.T) *Metrics {
	t.Helper()
	sharedMetricsOnce.Do(func() {
		cfg := &config.MetricsConfig{
			Enabled: true,
			Listen:  "127.0.0.1:0",
		}
		logger, err := zap.NewDevelopment()
		if err != nil {
			// Cannot use t.Fatal inside sync.Once across tests; panic instead.
			panic("failed to create zap logger: " + err.Error())
		}
		sharedMetrics = New(cfg, logger)
	})
	if sharedMetrics == nil {
		t.Fatal("sharedMetrics is nil — New() must have panicked")
	}
	return sharedMetrics
}

// =============================================================================
// V43 — Aux block submission metrics (merge mining observability)
// =============================================================================

// TestV43_AuxBlockMetrics_Submitted verifies that RecordAuxBlockSubmitted
// increments the AuxBlocksSubmitted counter for a given coin.
func TestV43_AuxBlockMetrics_Submitted(t *testing.T) {
	t.Parallel()
	m := getSharedMetrics(t)

	// Record several DOGE aux block submissions.
	m.RecordAuxBlockSubmitted("DOGE")
	m.RecordAuxBlockSubmitted("DOGE")
	m.RecordAuxBlockSubmitted("DOGE")

	val := testutil.ToFloat64(m.AuxBlocksSubmitted.WithLabelValues("DOGE"))
	if val < 3 {
		t.Errorf("AuxBlocksSubmitted(DOGE) = %v, want >= 3", val)
	}
}

// TestV43_AuxBlockMetrics_Failed verifies that RecordAuxBlockFailed tracks
// failures with independent label combinations (coin + reason).
func TestV43_AuxBlockMetrics_Failed(t *testing.T) {
	t.Parallel()
	m := getSharedMetrics(t)

	m.RecordAuxBlockFailed("DOGE", "timeout")
	m.RecordAuxBlockFailed("DOGE", "stale")
	m.RecordAuxBlockFailed("DOGE", "timeout")

	timeoutVal := testutil.ToFloat64(m.AuxBlocksFailed.WithLabelValues("DOGE", "timeout"))
	if timeoutVal < 2 {
		t.Errorf("AuxBlocksFailed(DOGE, timeout) = %v, want >= 2", timeoutVal)
	}

	staleVal := testutil.ToFloat64(m.AuxBlocksFailed.WithLabelValues("DOGE", "stale"))
	if staleVal < 1 {
		t.Errorf("AuxBlocksFailed(DOGE, stale) = %v, want >= 1", staleVal)
	}
}

// TestV43_AuxBlockMetrics_MultipleCoin verifies that AuxBlocksSubmitted tracks
// each coin independently so merge-mined chains do not interfere.
func TestV43_AuxBlockMetrics_MultipleCoin(t *testing.T) {
	t.Parallel()
	m := getSharedMetrics(t)

	m.RecordAuxBlockSubmitted("NMC")
	m.RecordAuxBlockSubmitted("NMC")
	m.RecordAuxBlockSubmitted("LTC")

	nmcVal := testutil.ToFloat64(m.AuxBlocksSubmitted.WithLabelValues("NMC"))
	if nmcVal < 2 {
		t.Errorf("AuxBlocksSubmitted(NMC) = %v, want >= 2", nmcVal)
	}

	ltcVal := testutil.ToFloat64(m.AuxBlocksSubmitted.WithLabelValues("LTC"))
	if ltcVal < 1 {
		t.Errorf("AuxBlocksSubmitted(LTC) = %v, want >= 1", ltcVal)
	}

	// NMC and LTC counters must be independent — LTC should not include NMC counts.
	if ltcVal >= nmcVal {
		t.Errorf("LTC counter (%v) should be less than NMC counter (%v); labels may be conflated", ltcVal, nmcVal)
	}
}

// =============================================================================
// V44 — Metrics server failure tracking (serverFailed atomic flag)
// =============================================================================

// TestV44_ServerFailed_InitiallyFalse verifies that a freshly created Metrics
// instance reports IsServerFailed() == false (the zero value of atomic.Bool).
func TestV44_ServerFailed_InitiallyFalse(t *testing.T) {
	t.Parallel()
	m := getSharedMetrics(t)

	if m.IsServerFailed() {
		t.Error("IsServerFailed() = true on fresh Metrics; want false")
	}
}

// TestV44_ServerFailed_SetAndCheck verifies that the serverFailed flag can be
// set and subsequently read through the public accessor.
func TestV44_ServerFailed_SetAndCheck(t *testing.T) {
	t.Parallel()

	// Use a locally constructed Metrics to avoid mutating shared state.
	// We only need the atomic.Bool field — no Prometheus registration required.
	var local Metrics

	if local.IsServerFailed() {
		t.Fatal("IsServerFailed() = true before Store; want false")
	}

	local.serverFailed.Store(true)

	if !local.IsServerFailed() {
		t.Error("IsServerFailed() = false after Store(true); want true")
	}
}

// =============================================================================
// V45 — Health check callback
// =============================================================================

// TestV45_HealthCheck_DefaultNil verifies that the healthCheck callback is nil
// on a freshly created Metrics instance (before pool.go wires it up).
func TestV45_HealthCheck_DefaultNil(t *testing.T) {
	t.Parallel()
	m := getSharedMetrics(t)

	if m.healthCheck != nil {
		t.Error("healthCheck should be nil on a fresh Metrics instance")
	}
}

// TestV45_HealthCheck_Set verifies that SetHealthCheck stores the callback and
// that invoking it returns the expected healthy result.
func TestV45_HealthCheck_Set(t *testing.T) {
	t.Parallel()

	// Use a local Metrics to avoid mutating the shared instance.
	var local Metrics

	local.SetHealthCheck(func() (bool, string) {
		return true, "ok"
	})

	if local.healthCheck == nil {
		t.Fatal("healthCheck is nil after SetHealthCheck")
	}

	healthy, details := local.healthCheck()
	if !healthy {
		t.Errorf("healthy = false, want true")
	}
	if details != "ok" {
		t.Errorf("details = %q, want %q", details, "ok")
	}
}

// TestV45_HealthCheck_Unhealthy verifies that SetHealthCheck correctly stores a
// callback reporting an unhealthy state with a descriptive reason.
func TestV45_HealthCheck_Unhealthy(t *testing.T) {
	t.Parallel()

	var local Metrics

	local.SetHealthCheck(func() (bool, string) {
		return false, "daemon down"
	})

	if local.healthCheck == nil {
		t.Fatal("healthCheck is nil after SetHealthCheck")
	}

	healthy, details := local.healthCheck()
	if healthy {
		t.Errorf("healthy = true, want false")
	}
	if details != "daemon down" {
		t.Errorf("details = %q, want %q", details, "daemon down")
	}
}

// =============================================================================
// Helpers
// =============================================================================

// init registers a custom no-op collector so that parallel tests importing
// prometheus/testutil do not hit stale-descriptor errors when the default
// registry already contains the metrics registered by New().
//
// This block intentionally left empty — kept as a documentation anchor
// explaining why the shared-instance pattern is used above instead of
// calling New() per test.
var _ prometheus.Collector = (*prometheus.CounterVec)(nil) // compile-time interface check
