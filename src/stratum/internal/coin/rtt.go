// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package coin — Real Time Target (RTT) difficulty validation for eCash (XEC).
//
// eCash introduced the RTT algorithm in the November 2025 upgrade as a
// complementary difficulty adjustment that prevents mining bursts. A found
// block must satisfy BOTH the standard PoW target (nBits) AND the RTT target
// computed from recent block timing data.
//
// The RTT filter uses exponentially weighted moving averages over five windows
// (1, 2, 5, 11, and 17 blocks). The minimum of all applicable windows becomes
// the effective RTT target. Post-November 2025, the 1-block window (index 0)
// is used in addition to the longer windows.
//
// References:
//   - https://github.com/Bitcoin-ABC/bitcoin-abc (src/pow/rtarget.h)
package coin

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
)

// rttFilterCoefficients are the per-window EMASG filter coefficients.
// Index maps to window size: [1-block, 2-block, 5-block, 11-block, 17-block].
// These values are consensus-critical and must match Bitcoin ABC exactly.
var rttFilterCoefficients = []float64{
	5.0372626864e-11, // index 0 — 1-block window  (post-Nov 2025 upgrade)
	4.9192018423e-14, // index 1 — 2-block window
	4.8039080491e-17, // index 2 — 5-block window
	4.9192018423e-19, // index 3 — 11-block window
	4.6913164542e-20, // index 4 — 17-block window
}

// RTTRawData holds the RTT fields from a job, decoupled from the daemon package
// to avoid circular imports between coin ↔ daemon.
type RTTRawData struct {
	PrevHeaderTime []int64 // Timestamps of N most recent block headers, newest-first (index 0 = most recent)
	PrevBits       string  // Compact target (hex) of the previous block
	NextTarget     string  // Pre-computed RTT target from node (hex, may be empty)
	Bits           string  // Current block's compact target (hex) for PoW fallback
}

// IsRTTDataValidRaw returns true if rtt contains enough data for RTT computation.
func IsRTTDataValidRaw(rtt *RTTRawData) bool {
	if rtt == nil {
		return false
	}
	if len(rtt.PrevHeaderTime) < 2 {
		return false
	}
	// All identical timestamps indicate stale or synthetic data
	first := rtt.PrevHeaderTime[0]
	for _, t := range rtt.PrevHeaderTime[1:] {
		if t != first {
			return true
		}
	}
	return false
}

// rttCompactToBig parses an 8-char hex compact target into a *big.Int.
// Returns nil on any parse error.
func rttCompactToBig(compact string) *big.Int {
	if len(compact) != 8 {
		return nil
	}
	b, err := hex.DecodeString(compact)
	if err != nil || len(b) != 4 {
		return nil
	}
	bits := (uint32(b[0]) << 24) | (uint32(b[1]) << 16) | (uint32(b[2]) << 8) | uint32(b[3])

	exponent := bits >> 24
	mantissa := bits & 0x007fffff
	if bits&0x00800000 != 0 {
		return new(big.Int) // negative target is invalid
	}

	target := new(big.Int).SetUint64(uint64(mantissa))
	if exponent <= 3 {
		target.Rsh(target, uint(8*(3-exponent)))
	} else {
		target.Lsh(target, uint(8*(exponent-3)))
	}
	return target
}

// ComputeRTTRaw computes the RTT target from raw timing data and current time.
//
// The algorithm:
//  1. Determine which filter windows apply based on how many timestamps exist.
//  2. For each applicable window, compute: prevTarget * coefficient * (timeDiff^5).
//  3. Take the minimum target across all windows.
//
// Post-Nov 2025 upgrade: window index 0 (1-block) is active when numWindows >= 5.
// Pre-upgrade: window index 1 (2-block) is the smallest applicable window.
func ComputeRTTRaw(rtt *RTTRawData, currentTime int64) (*big.Int, error) {
	if !IsRTTDataValidRaw(rtt) {
		return nil, fmt.Errorf("invalid or insufficient RTT data")
	}

	prevTarget := rttCompactToBig(rtt.PrevBits)
	if prevTarget == nil || prevTarget.Sign() == 0 {
		return nil, fmt.Errorf("cannot parse prevBits %q as compact target", rtt.PrevBits)
	}

	numWindows := len(rtt.PrevHeaderTime)

	// Determine starting filter index:
	// - numWindows >= 5: post-upgrade, start at index 0 (1-block window)
	// - numWindows < 5 (< 2 already excluded above): start at index 1
	filterStart := 1
	if numWindows >= 5 {
		filterStart = 0
	}

	var minTarget *big.Int

	for idx := filterStart; idx < len(rttFilterCoefficients); idx++ {
		// timeDiff = currentTime - timestamp[idx]
		// timestamps are ordered newest-first, so idx=0 is most recent
		tsIdx := idx
		if tsIdx >= len(rtt.PrevHeaderTime) {
			break
		}
		timeDiff := currentTime - rtt.PrevHeaderTime[tsIdx]
		if timeDiff <= 0 {
			timeDiff = 1 // prevent zero/negative time differences
		}

		// RTT_i = prevTarget * coeff_i * timeDiff^5
		// Use big.Float for precision
		prevTargetFloat := new(big.Float).SetPrec(256).SetInt(prevTarget)
		coeffFloat := new(big.Float).SetPrec(256).SetFloat64(rttFilterCoefficients[idx])
		tdFloat := new(big.Float).SetPrec(256).SetInt64(timeDiff)

		// timeDiff^5
		td5 := new(big.Float).SetPrec(256).SetFloat64(1.0)
		for i := 0; i < 5; i++ {
			td5.Mul(td5, tdFloat)
		}

		rttFloat := new(big.Float).SetPrec(256)
		rttFloat.Mul(prevTargetFloat, coeffFloat)
		rttFloat.Mul(rttFloat, td5)

		rttInt, _ := rttFloat.Int(nil)
		if rttInt == nil || rttInt.Sign() <= 0 {
			continue
		}

		if minTarget == nil || rttInt.Cmp(minTarget) < 0 {
			minTarget = rttInt
		}
	}

	if minTarget == nil {
		return nil, fmt.Errorf("RTT computation produced no valid target")
	}
	return minTarget, nil
}

// GetRTTTargetRaw returns the effective RTT target for a job.
// Prefers the node's pre-computed NextTarget when available; falls back to
// ComputeRTTRaw using local timing data.
func GetRTTTargetRaw(rtt *RTTRawData, currentTime int64) (*big.Int, error) {
	// Node pre-computes NextTarget when it has enough data
	if rtt != nil && len(rtt.NextTarget) == 64 {
		target := new(big.Int)
		if _, ok := target.SetString(rtt.NextTarget, 16); ok && target.Sign() > 0 {
			return target, nil
		}
	}
	return ComputeRTTRaw(rtt, currentTime)
}

// CheckRTTTargetRaw returns true if blockHashBE (big-endian) meets the RTT target.
// A block is valid only when hash <= RTT target (in addition to meeting nBits).
func CheckRTTTargetRaw(blockHashBE []byte, rtt *RTTRawData, currentTime int64) (bool, error) {
	if len(blockHashBE) != 32 {
		return false, fmt.Errorf("block hash must be 32 bytes, got %d", len(blockHashBE))
	}

	rttTarget, err := GetRTTTargetRaw(rtt, currentTime)
	if err != nil {
		return false, fmt.Errorf("failed to compute RTT target: %w", err)
	}

	hashInt := new(big.Int).SetBytes(blockHashBE)
	return hashInt.Cmp(rttTarget) <= 0, nil
}

// RTTDataFromTemplate extracts RTT fields from a daemon BlockTemplate.
// Returns nil if the template has no RTT data (non-XEC coins, or pre-upgrade).
// This function signature uses raw primitives to stay import-free from daemon.
func RTTDataFromTemplate(
	prevHeaderTime []int64,
	prevBits string,
	nextTarget string,
	bits string,
) *RTTRawData {
	if len(prevHeaderTime) < 2 {
		return nil
	}
	return &RTTRawData{
		PrevHeaderTime: prevHeaderTime,
		PrevBits:       prevBits,
		NextTarget:     nextTarget,
		Bits:           bits,
	}
}

// XECCurrentTime returns the current Unix timestamp for RTT comparisons.
// Isolated here so callers don't need to import "time" directly.
func XECCurrentTime() int64 {
	return time.Now().Unix()
}
