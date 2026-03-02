// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares — Validator constructor tests.
//
// These tests verify that NewValidator and NewValidatorWithLogger properly
// initialize all Validator fields: getJob function, duplicate tracker,
// nonce tracker, max job age, and logger.
package shares

import (
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// testGetJob is a simple job lookup function for constructor testing.
func testGetJob(id string) (*protocol.Job, bool) {
	if id == "test_job_1" {
		return &protocol.Job{ID: "test_job_1"}, true
	}
	return nil, false
}

// =============================================================================
// NewValidator: default constructor
// =============================================================================

// TestNewValidator_InitializesAllFields verifies that NewValidator properly
// initializes the getJob function, duplicate tracker, nonce tracker,
// max job age, and logger.
func TestNewValidator_InitializesAllFields(t *testing.T) {
	t.Parallel()

	v := NewValidator(testGetJob)

	if v == nil {
		t.Fatal("NewValidator should not return nil")
	}

	// getJob should be wired correctly
	if v.getJob == nil {
		t.Fatal("getJob function should not be nil")
	}
	job, ok := v.getJob("test_job_1")
	if !ok || job == nil {
		t.Error("getJob should return test_job_1")
	}
	_, ok = v.getJob("nonexistent")
	if ok {
		t.Error("getJob should return false for nonexistent job")
	}

	// Duplicate tracker should be initialized
	if v.duplicates == nil {
		t.Error("duplicates tracker should not be nil")
	}

	// Nonce tracker should be initialized
	if v.nonceTracker == nil {
		t.Error("nonceTracker should not be nil")
	}

	// Max job age should be the default
	if v.maxJobAge != DefaultMaxJobAge {
		t.Errorf("maxJobAge = %v, want %v", v.maxJobAge, DefaultMaxJobAge)
	}

	// Logger should be initialized (production logger)
	if v.logger == nil {
		t.Error("logger should not be nil")
	}
}

// TestNewValidator_MaxJobAgeDefault verifies the default max job age
// is consistent with the constant.
func TestNewValidator_MaxJobAgeDefault(t *testing.T) {
	t.Parallel()

	v := NewValidator(testGetJob)
	if v.MaxJobAge() != DefaultMaxJobAge {
		t.Errorf("MaxJobAge() = %v, want %v", v.MaxJobAge(), DefaultMaxJobAge)
	}
}

// =============================================================================
// NewValidatorWithLogger: custom logger constructor
// =============================================================================

// TestNewValidatorWithLogger_UsesProvidedLogger verifies that
// NewValidatorWithLogger uses the provided logger instead of creating one.
func TestNewValidatorWithLogger_UsesProvidedLogger(t *testing.T) {
	t.Parallel()

	logger, _ := zap.NewDevelopment()
	sugar := logger.Sugar()

	v := NewValidatorWithLogger(testGetJob, sugar)

	if v == nil {
		t.Fatal("NewValidatorWithLogger should not return nil")
	}

	// Logger should be the one we provided
	if v.logger != sugar {
		t.Error("logger should be the custom logger we provided")
	}
}

// TestNewValidatorWithLogger_InitializesAllFields verifies all fields
// are initialized correctly with the custom logger constructor.
func TestNewValidatorWithLogger_InitializesAllFields(t *testing.T) {
	t.Parallel()

	logger := zap.NewNop().Sugar()
	v := NewValidatorWithLogger(testGetJob, logger)

	if v.getJob == nil {
		t.Error("getJob should not be nil")
	}
	if v.duplicates == nil {
		t.Error("duplicates should not be nil")
	}
	if v.nonceTracker == nil {
		t.Error("nonceTracker should not be nil")
	}
	if v.maxJobAge != DefaultMaxJobAge {
		t.Errorf("maxJobAge = %v, want %v", v.maxJobAge, DefaultMaxJobAge)
	}
	if v.logger == nil {
		t.Error("logger should not be nil")
	}
}

// =============================================================================
// SetMaxJobAge: runtime reconfiguration
// =============================================================================

// TestSetMaxJobAge_OverridesDefault verifies that SetMaxJobAge changes
// the max job age from the default.
func TestSetMaxJobAge_OverridesDefault(t *testing.T) {
	t.Parallel()

	v := NewValidator(testGetJob)

	customAge := 5 * time.Minute
	v.SetMaxJobAge(customAge)

	if v.MaxJobAge() != customAge {
		t.Errorf("MaxJobAge() = %v, want %v after SetMaxJobAge", v.MaxJobAge(), customAge)
	}
}

// TestSetMaxJobAge_CoinSpecificScaling verifies the coin-specific scaling
// pattern: max(blockTime * 2, 5 minutes).
func TestSetMaxJobAge_CoinSpecificScaling(t *testing.T) {
	t.Parallel()

	v := NewValidator(testGetJob)

	// DGB: blockTime=15s → 15*2=30s → max(30s, 5min) = 5min
	dgbAge := 5 * time.Minute
	v.SetMaxJobAge(dgbAge)
	if v.MaxJobAge() != dgbAge {
		t.Errorf("DGB max job age = %v, want %v", v.MaxJobAge(), dgbAge)
	}

	// BTC: blockTime=600s → 600*2=1200s=20min → max(20min, 5min) = 20min
	btcAge := 20 * time.Minute
	v.SetMaxJobAge(btcAge)
	if v.MaxJobAge() != btcAge {
		t.Errorf("BTC max job age = %v, want %v", v.MaxJobAge(), btcAge)
	}
}

// =============================================================================
// Initial metrics: zero state
// =============================================================================

// TestNewValidator_MetricsStartAtZero verifies all counters start at 0.
func TestNewValidator_MetricsStartAtZero(t *testing.T) {
	t.Parallel()

	v := NewValidator(testGetJob)

	if v.validated.Load() != 0 {
		t.Errorf("validated = %d, want 0", v.validated.Load())
	}
	if v.accepted.Load() != 0 {
		t.Errorf("accepted = %d, want 0", v.accepted.Load())
	}
	if v.rejected.Load() != 0 {
		t.Errorf("rejected = %d, want 0", v.rejected.Load())
	}
	if v.staleShares.Load() != 0 {
		t.Errorf("staleShares = %d, want 0", v.staleShares.Load())
	}
	if v.nonceExhaustion.Load() != 0 {
		t.Errorf("nonceExhaustion = %d, want 0", v.nonceExhaustion.Load())
	}
	if v.versionRollRejects.Load() != 0 {
		t.Errorf("versionRollRejects = %d, want 0", v.versionRollRejects.Load())
	}
}
