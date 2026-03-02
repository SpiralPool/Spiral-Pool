// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares - Tests for ValidatorV2 with multi-coin support.
//
// ValidatorV2 extends the base Validator with coin-specific logic,
// enabling multi-coin pool operation with per-coin validation rules.
package shares

import (
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// VALIDATOR V2 CREATION TESTS
// =============================================================================

func TestNewValidatorWithCoin(t *testing.T) {
	// Mock job getter
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	// Create with registered coin
	if coin.IsRegistered("DGB") {
		dgb := coin.MustCreate("DGB")
		v := NewValidatorWithCoin(getJob, dgb)

		if v == nil {
			t.Fatal("NewValidatorWithCoin returned nil")
		}
		if v.Validator == nil {
			t.Error("Embedded Validator is nil")
		}
		if v.coin == nil {
			t.Error("Coin implementation is nil")
		}
		if v.Coin().Symbol() != "DGB" {
			t.Errorf("Coin symbol = %q, want 'DGB'", v.Coin().Symbol())
		}
	} else {
		t.Skip("DGB not registered")
	}
}

func TestValidatorV2_Coin(t *testing.T) {
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	coins := []string{"DGB", "BTC", "BCH", "BC2"}
	for _, symbol := range coins {
		t.Run(symbol, func(t *testing.T) {
			if !coin.IsRegistered(symbol) {
				t.Skipf("%s not registered", symbol)
			}

			coinImpl := coin.MustCreate(symbol)
			v := NewValidatorWithCoin(getJob, coinImpl)

			// Coin() should return the same implementation
			if v.Coin() != coinImpl {
				t.Error("Coin() did not return the expected implementation")
			}
			if v.Coin().Symbol() != symbol {
				t.Errorf("Coin().Symbol() = %q, want %q", v.Coin().Symbol(), symbol)
			}
		})
	}
}

// =============================================================================
// SHARE DIFFICULTY MULTIPLIER TESTS
// =============================================================================

func TestValidatorV2_ShareDifficultyMultiplier(t *testing.T) {
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	coins := []string{"DGB", "BTC", "BCH", "BC2"}
	for _, symbol := range coins {
		t.Run(symbol, func(t *testing.T) {
			if !coin.IsRegistered(symbol) {
				t.Skipf("%s not registered", symbol)
			}

			coinImpl := coin.MustCreate(symbol)
			v := NewValidatorWithCoin(getJob, coinImpl)

			multiplier := v.ShareDifficultyMultiplier()

			// Multiplier must be positive
			if multiplier <= 0 {
				t.Errorf("ShareDifficultyMultiplier = %f, want > 0", multiplier)
			}

			// For SHA256d coins, multiplier should be 1.0
			if coinImpl.Algorithm() == "sha256d" {
				if multiplier != 1.0 {
					t.Errorf("SHA256d coin %s: multiplier = %f, want 1.0", symbol, multiplier)
				}
			}
		})
	}
}

func TestValidatorV2_MultiplierMatchesCoin(t *testing.T) {
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	if !coin.IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	// ValidatorV2 multiplier should match coin's multiplier
	validatorMult := v.ShareDifficultyMultiplier()
	coinMult := coinImpl.ShareDifficultyMultiplier()

	if validatorMult != coinMult {
		t.Errorf("Multiplier mismatch: validator=%f, coin=%f", validatorMult, coinMult)
	}
}

// =============================================================================
// VALIDATE WITH COIN TESTS
// =============================================================================

func TestValidatorV2_ValidateWithCoin_NoJob(t *testing.T) {
	// Job not found should reject share
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	if !coin.IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	share := &protocol.Share{
		JobID:      "nonexistent",
		Difficulty: 1000,
	}

	result := v.ValidateWithCoin(share)

	if result.Accepted {
		t.Error("Share should be rejected when job not found")
	}
	if result.RejectReason != "job-not-found" {
		t.Errorf("RejectReason = %q, want 'job-not-found'", result.RejectReason)
	}
}

func TestValidatorV2_ValidateWithCoin_UsesSHA256d(t *testing.T) {
	// For SHA256d coins, ValidateWithCoin uses the standard validation
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	if !coin.IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	// Verify algorithm is SHA256d
	if coinImpl.Algorithm() != "sha256d" {
		t.Errorf("DGB algorithm = %q, expected 'sha256d'", coinImpl.Algorithm())
	}

	// For SHA256d, base validator is used
	// This is documented behavior - extension point for future algorithms
	share := &protocol.Share{
		JobID: "test",
	}
	result := v.ValidateWithCoin(share)

	// Should fail with job-not-found (not algorithm error)
	if result.RejectReason != "job-not-found" {
		t.Errorf("RejectReason = %q, want 'job-not-found'", result.RejectReason)
	}
}

// =============================================================================
// EMBEDDED VALIDATOR TESTS
// =============================================================================

func TestValidatorV2_EmbeddedValidator(t *testing.T) {
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	if !coin.IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	// Can access embedded Validator methods
	stats := v.Stats()
	if stats.Validated != 0 {
		t.Error("Fresh validator should have 0 validated")
	}

	// Can use Validate from embedded type
	share := &protocol.Share{JobID: "test"}
	result := v.Validate(share)
	if result.Accepted {
		t.Error("Share without valid job should be rejected")
	}

	// Stats should update
	stats = v.Stats()
	if stats.Validated != 1 {
		t.Errorf("Validated = %d, want 1", stats.Validated)
	}
}

func TestValidatorV2_SetNetworkDifficulty(t *testing.T) {
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	if !coin.IsRegistered("DGB") {
		t.Skip("DGB not registered")
	}

	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	// Can set network difficulty through embedded Validator
	v.SetNetworkDifficulty(1000000.0)

	// This affects block detection threshold
	// The share must meet network difficulty to be a block
	t.Log("SetNetworkDifficulty allows block detection for SOLO mining")
}

// =============================================================================
// MULTI-COIN VALIDATION TESTS
// =============================================================================

func TestValidatorV2_MultipleCoinInstances(t *testing.T) {
	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	coins := []string{"DGB", "BTC", "BCH", "BC2"}
	validators := make(map[string]*ValidatorV2)

	for _, symbol := range coins {
		if !coin.IsRegistered(symbol) {
			continue
		}

		coinImpl := coin.MustCreate(symbol)
		validators[symbol] = NewValidatorWithCoin(getJob, coinImpl)
	}

	if len(validators) < 2 {
		t.Skip("Need at least 2 coins registered")
	}

	// Each validator should be independent
	for symbol, v := range validators {
		if v.Coin().Symbol() != symbol {
			t.Errorf("Validator %s has wrong coin: %s", symbol, v.Coin().Symbol())
		}
	}

	// Operations on one shouldn't affect others
	if v, ok := validators["DGB"]; ok {
		share := &protocol.Share{JobID: "test"}
		v.Validate(share)

		// Other validators should still have 0 validated
		for symbol, other := range validators {
			if symbol == "DGB" {
				continue
			}
			if other.Stats().Validated != 0 {
				t.Errorf("%s validator affected by DGB validation", symbol)
			}
		}
	}
}

// =============================================================================
// COIN ALGORITHM EXTENSION POINT TESTS
// =============================================================================

func TestValidatorV2_AlgorithmExtensionPoint(t *testing.T) {
	// Document that ValidateWithCoin is the extension point for future algorithms
	// Currently all supported coins use SHA256d

	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}

	algorithms := make(map[string][]string)

	for _, symbol := range coin.ListRegistered() {
		coinImpl := coin.MustCreate(symbol)
		algo := coinImpl.Algorithm()
		algorithms[algo] = append(algorithms[algo], symbol)
	}

	for algo, coins := range algorithms {
		t.Run(algo, func(t *testing.T) {
			t.Logf("Algorithm %s is used by: %v", algo, coins)

			// Test that validator can be created for each
			for _, symbol := range coins {
				coinImpl := coin.MustCreate(symbol)
				v := NewValidatorWithCoin(getJob, coinImpl)

				if v == nil {
					t.Errorf("Failed to create validator for %s (%s)", symbol, algo)
				}
			}
		})
	}
}

// =============================================================================
// SOLO MINING INVARIANT TESTS
// =============================================================================

func TestValidatorV2_SOLOMiningInvariants(t *testing.T) {
	// Document SOLO mining invariants for ValidatorV2

	t.Run("no_fee_calculation", func(t *testing.T) {
		// ValidatorV2 does NOT calculate fees
		// In SOLO mode, 100% of block reward goes to miner
		t.Log("ValidatorV2 validates shares, does not calculate fees")
		t.Log("SOLO mode: Full reward to miner's coinbase address")
	})

	t.Run("block_detection_uses_network_diff", func(t *testing.T) {
		// A share is a block if it meets network difficulty
		// This is set via SetNetworkDifficulty
		t.Log("Block detection: share.difficulty >= network_difficulty")
	})

	t.Run("no_pplns_or_pps", func(t *testing.T) {
		// There is no share tracking for payment schemes
		// All shares are just for validating miner work
		t.Log("No PPLNS/PPS/PROP - SOLO mining only")
	})
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkNewValidatorWithCoin(b *testing.B) {
	if !coin.IsRegistered("DGB") {
		b.Skip("DGB not registered")
	}

	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}
	coinImpl := coin.MustCreate("DGB")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewValidatorWithCoin(getJob, coinImpl)
	}
}

func BenchmarkValidatorV2_ShareDifficultyMultiplier(b *testing.B) {
	if !coin.IsRegistered("DGB") {
		b.Skip("DGB not registered")
	}

	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}
	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = v.ShareDifficultyMultiplier()
	}
}

func BenchmarkValidatorV2_Coin(b *testing.B) {
	if !coin.IsRegistered("DGB") {
		b.Skip("DGB not registered")
	}

	getJob := func(jobID string) (*protocol.Job, bool) {
		return nil, false
	}
	coinImpl := coin.MustCreate("DGB")
	v := NewValidatorWithCoin(getJob, coinImpl)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = v.Coin()
	}
}
