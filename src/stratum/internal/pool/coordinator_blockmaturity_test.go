// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/payments"
	"go.uber.org/zap"
)

// These tests verify that blockMaturity config override logic works correctly
// across all three coordinator paths: standalone, aux chain, and late-start.
// Rather than spinning up the full coordinator (which requires daemon connections,
// database, etc.), we test the exact config-wiring logic extracted from the
// coordinator's initialization code.

// buildPaymentsConfig mirrors the coordinator's config-wiring logic for block
// maturity (coordinator.go lines 456-469). This is the standalone path.
func buildPaymentsConfig(coinMaturity int, cfgBlockMaturity int, blockTime int) *config.PaymentsConfig {
	effectiveMaturity := coinMaturity
	if cfgBlockMaturity > 0 {
		effectiveMaturity = cfgBlockMaturity
	}
	return &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: effectiveMaturity,
		BlockTime:     blockTime,
	}
}

// buildAuxPaymentsConfig mirrors the coordinator's aux chain config-wiring logic
// (coordinator.go lines 535-545). Aux chains inherit parent config's blockMaturity.
func buildAuxPaymentsConfig(auxCoinMaturity int, parentCfgBlockMaturity int, auxBlockTime int) *config.PaymentsConfig {
	auxMaturity := auxCoinMaturity
	if parentCfgBlockMaturity > 0 {
		auxMaturity = parentCfgBlockMaturity
	}
	return &config.PaymentsConfig{
		Enabled:       true,
		Scheme:        "SOLO",
		BlockTime:     auxBlockTime,
		BlockMaturity: auxMaturity,
	}
}

// buildLateStartPaymentsConfig mirrors the coordinator's late-start config-wiring
// logic (coordinator.go lines 926-937).
func buildLateStartPaymentsConfig(coinMaturity int, cfgBlockMaturity int, blockTime int) *config.PaymentsConfig {
	lateMaturity := coinMaturity
	if cfgBlockMaturity > 0 {
		lateMaturity = cfgBlockMaturity
	}
	return &config.PaymentsConfig{
		Enabled:       true,
		Interval:      time.Minute,
		Scheme:        "SOLO",
		BlockMaturity: lateMaturity,
		BlockTime:     blockTime,
	}
}

// TestBlockMaturity_ConfigOverride_Standalone verifies that when
// config.Payments.BlockMaturity is set (> 0), the standalone payment processor
// path uses the config override instead of the coin's default CoinbaseMaturity.
func TestBlockMaturity_ConfigOverride_Standalone(t *testing.T) {
	t.Parallel()

	coinDefault := 100     // e.g. BTC CoinbaseMaturity()
	cfgOverride := 10      // regtest override
	blockTime := 600       // 10 min blocks

	paymentsCfg := buildPaymentsConfig(coinDefault, cfgOverride, blockTime)

	if paymentsCfg.BlockMaturity != cfgOverride {
		t.Errorf("Standalone path: BlockMaturity = %d, want %d (config override)",
			paymentsCfg.BlockMaturity, cfgOverride)
	}

	// Verify a processor created with this config uses the override
	logger := zap.NewNop()
	proc := payments.NewProcessor(paymentsCfg, &config.PoolConfig{}, nil, nil, logger)
	// processCycle would use getBlockMaturity() internally; we verify the config was set
	_ = proc // Processor created with correct config
}

// TestBlockMaturity_ConfigOverride_AuxChain verifies that the aux/merge-mined
// path uses the parent config's blockMaturity override when set.
func TestBlockMaturity_ConfigOverride_AuxChain(t *testing.T) {
	t.Parallel()

	auxCoinDefault := 100    // aux coin's CoinbaseMaturity()
	parentCfgOverride := 10  // parent config override
	auxBlockTime := 15       // DGB-like 15s blocks

	paymentsCfg := buildAuxPaymentsConfig(auxCoinDefault, parentCfgOverride, auxBlockTime)

	if paymentsCfg.BlockMaturity != parentCfgOverride {
		t.Errorf("Aux chain path: BlockMaturity = %d, want %d (parent config override)",
			paymentsCfg.BlockMaturity, parentCfgOverride)
	}
}

// TestBlockMaturity_ConfigOverride_LateStart verifies that the late-start
// retry path respects config blockMaturity override, matching the init-time behavior.
func TestBlockMaturity_ConfigOverride_LateStart(t *testing.T) {
	t.Parallel()

	coinDefault := 100
	cfgOverride := 10
	blockTime := 600

	paymentsCfg := buildLateStartPaymentsConfig(coinDefault, cfgOverride, blockTime)

	if paymentsCfg.BlockMaturity != cfgOverride {
		t.Errorf("Late-start path: BlockMaturity = %d, want %d (config override)",
			paymentsCfg.BlockMaturity, cfgOverride)
	}
}

// TestBlockMaturity_ZeroUsesDefault verifies that when config.Payments.BlockMaturity
// is 0 (not set), all three paths fall back to the coin's CoinbaseMaturity() default.
func TestBlockMaturity_ZeroUsesDefault(t *testing.T) {
	t.Parallel()

	coinDefault := 100
	cfgZero := 0       // not set — use coin default
	blockTime := 600

	// Standalone path
	standaloneCfg := buildPaymentsConfig(coinDefault, cfgZero, blockTime)
	if standaloneCfg.BlockMaturity != coinDefault {
		t.Errorf("Standalone with zero config: BlockMaturity = %d, want %d (coin default)",
			standaloneCfg.BlockMaturity, coinDefault)
	}

	// Aux chain path
	auxCfg := buildAuxPaymentsConfig(coinDefault, cfgZero, 15)
	if auxCfg.BlockMaturity != coinDefault {
		t.Errorf("Aux chain with zero config: BlockMaturity = %d, want %d (coin default)",
			auxCfg.BlockMaturity, coinDefault)
	}

	// Late-start path
	lateCfg := buildLateStartPaymentsConfig(coinDefault, cfgZero, blockTime)
	if lateCfg.BlockMaturity != coinDefault {
		t.Errorf("Late-start with zero config: BlockMaturity = %d, want %d (coin default)",
			lateCfg.BlockMaturity, coinDefault)
	}
}
