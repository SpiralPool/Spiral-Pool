// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - DigiByte Scrypt algorithm variant.
//
// DigiByte is a multi-algorithm blockchain that supports 5 PoW algorithms:
//   - SHA256d
//   - Scrypt
//   - Groestl
//   - Skein
//   - Qubit
//
// This implementation provides Scrypt mining support for DigiByte.
// Each algorithm has its own port and difficulty, but shares the same
// blockchain and address format.
//
// Multi-algorithm was enabled at block 145,000 (July 2014).
//
// References:
//   - https://github.com/digibyte-core/digibyte
//   - https://www.digibyte.io/
package coin

import (
	"math/big"

	"github.com/spiralpool/stratum/internal/crypto"
)

// DigiByteScrypt network parameters
const (
	DigiByteScryptDefaultStratumPort = 3336 // Scrypt stratum port (different from SHA256d: 3333/3334/3335-TLS)
	// Multi-algo switch block
	DigiByteMultiAlgoSwitchBlock = 145000
)

// DigiByteScryptCoin implements the Coin interface for DigiByte using Scrypt.
// This is a variant of DigiByteCoin that uses Scrypt instead of SHA256d.
type DigiByteScryptCoin struct {
	*DigiByteCoin
}

// NewDigiByteScryptCoin creates a new DigiByte Scrypt coin instance.
func NewDigiByteScryptCoin() Coin {
	return &DigiByteScryptCoin{
		DigiByteCoin: &DigiByteCoin{},
	}
}

// Symbol returns the ticker symbol with algorithm suffix.
func (c *DigiByteScryptCoin) Symbol() string {
	return "DGB-SCRYPT"
}

// Name returns the full coin name with algorithm.
func (c *DigiByteScryptCoin) Name() string {
	return "DigiByte (Scrypt)"
}

// Algorithm returns the mining algorithm.
func (c *DigiByteScryptCoin) Algorithm() string {
	return "scrypt"
}

// HashBlockHeader computes the block hash using Scrypt.
// This overrides the SHA256d method from DigiByteCoin.
func (c *DigiByteScryptCoin) HashBlockHeader(serialized []byte) []byte {
	return crypto.ScryptHash(serialized)
}

// DifficultyFromTarget calculates difficulty from target for Scrypt.
// Uses the same diff1 target as other Scrypt coins.
func (c *DigiByteScryptCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Scrypt diff1 target
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	diff1Float := new(big.Float).SetInt(diff1Target)
	targetFloat := new(big.Float).SetInt(target)

	result := new(big.Float).Quo(diff1Float, targetFloat)
	difficulty, accuracy := result.Float64()

	if accuracy == big.Below {
		return difficulty
	}

	return difficulty
}

// SupportsMultiAlgo returns true since DGB supports multiple algorithms.
func (c *DigiByteScryptCoin) SupportsMultiAlgo() bool {
	return true
}

// MultiAlgoSwitchBlock returns the block height at which multi-algo was enabled.
func (c *DigiByteScryptCoin) MultiAlgoSwitchBlock() uint64 {
	return DigiByteMultiAlgoSwitchBlock
}

// ShareDifficultyMultiplier returns the stratum difficulty multiplier for Scrypt.
// Scrypt stratum pools use a different diff-1 target than Bitcoin:
//   - Bitcoin diff-1:  0x00000000FFFF0000... (2^224)
//   - Scrypt diff-1:   0x0000FFFF00000000... (2^240)
//
// The ratio is 2^16 = 65536. When a pool sends mining.set_difficulty(1) to a
// scrypt miner, the miner targets the scrypt diff-1. The pool must account for
// this 65536x difference when validating share targets.
func (c *DigiByteScryptCoin) ShareDifficultyMultiplier() float64 {
	return 65536.0
}

// MultiAlgoGBTParam returns the Scrypt algorithm parameter for getblocktemplate.
func (c *DigiByteScryptCoin) MultiAlgoGBTParam() string {
	return "scrypt"
}

// init registers DigiByte Scrypt in the coin registry.
func init() {
	Register("DGB-SCRYPT", NewDigiByteScryptCoin)
	Register("DIGIBYTE-SCRYPT", NewDigiByteScryptCoin)
	Register("DGB_SCRYPT", NewDigiByteScryptCoin) // Alternative format
}
