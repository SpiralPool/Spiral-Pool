// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package stratum - Spiral Router: intelligent miner routing based on user-agent detection
//
// Spiral Router is Spiral Pool's custom implementation for automatic difficulty
// assignment based on miner type detection. It eliminates the need for multiple
// ports by intelligently routing miners to appropriate difficulty levels.
//
// Multi-algorithm support for SHA-256d and Scrypt coins. Scrypt has different
// hashrate scales (~1000x slower per hash) so separate difficulty profiles are
// maintained for each algorithm.
//
// LEGAL NOTICE: Miner classifications (Lottery, Low, Mid, High, Pro, etc.) exist
// solely to deliver accurate pool statistics to miners more quickly than vardiff
// convergence would otherwise allow. These classifications:
//   - DO NOT constitute performance guarantees or warranties of any kind
//   - DO NOT promise any particular hashrate, earnings, or mining success
//   - ARE purely internal categorizations for statistics and difficulty assignment
//   - MAY be inaccurate for any given miner; vardiff adjusts to actual performance
//
// The hashrate ranges shown in comments are approximate reference values based on
// manufacturer specifications at time of writing. Actual performance varies based
// on firmware, configuration, cooling, power supply, and other factors outside
// the control of this software.
package stratum

import (
	"regexp"
	"strings"
)

// Algorithm represents the mining algorithm type
type Algorithm string

const (
	AlgorithmSHA256d Algorithm = "sha256d"
	AlgorithmScrypt  Algorithm = "scrypt"
)

// MinerClass represents the hashrate class of a miner.
type MinerClass int

const (
	MinerClassUnknown MinerClass = iota
	MinerClassLottery            // ESP32 Miner, NMiner, Arduino (~50-500 KH/s)
	MinerClassLow                // BitAxe Ultra, NMAxe (~400-600 GH/s)
	MinerClassMid                // NerdQAxe, BitAxe Hex/Gamma (~1-10 TH/s)
	MinerClassHigh               // Antminer S9, S15, older gen (~10-20 TH/s)
	MinerClassPro                // Antminer S19+, S21, Whatsminer M50+ (~100-200+ TH/s)

	// Avalon-specific classes for granular difficulty assignment
	// These provide explicit support for Canaan/Avalon hardware across all generations.
	// IMPORTANT: These classes ensure no Avalon device falls through to generic handling.
	MinerClassAvalonNano       // Avalon Nano 2/3/3S, consumer home miners (~3-7 TH/s)
	MinerClassAvalonLegacyLow  // Avalon 3, 3S, 6 series (~1-4 TH/s, legacy devices)
	MinerClassAvalonLegacyMid  // Avalon 7, 8 series (~7-15 TH/s)
	MinerClassAvalonMid        // Avalon 9, 10, 11 series (~18-81 TH/s)
	MinerClassAvalonHigh       // Avalon A12, A13 series (~85-130 TH/s)
	MinerClassAvalonPro        // Avalon A14, A15, Q series (~170-215 TH/s)
	MinerClassAvalonHome       // Avalon Mini 3, Q home miners (~37-90 TH/s)
)

func (c MinerClass) String() string {
	switch c {
	case MinerClassLottery:
		return "lottery"
	case MinerClassLow:
		return "low"
	case MinerClassMid:
		return "mid"
	case MinerClassHigh:
		return "high"
	case MinerClassPro:
		return "pro"
	// Avalon-specific classes
	case MinerClassAvalonNano:
		return "avalon_nano"
	case MinerClassAvalonLegacyLow:
		return "avalon_legacy_low"
	case MinerClassAvalonLegacyMid:
		return "avalon_legacy_mid"
	case MinerClassAvalonMid:
		return "avalon_mid"
	case MinerClassAvalonHigh:
		return "avalon_high"
	case MinerClassAvalonPro:
		return "avalon_pro"
	case MinerClassAvalonHome:
		return "avalon_home"
	default:
		return "unknown"
	}
}

// Vendor returns the miner vendor based on class.
// Used for observability metrics (miner_vendor label).
func (c MinerClass) Vendor() string {
	switch c {
	case MinerClassAvalonNano, MinerClassAvalonLegacyLow, MinerClassAvalonLegacyMid,
		MinerClassAvalonMid, MinerClassAvalonHigh, MinerClassAvalonPro, MinerClassAvalonHome:
		return "avalon"
	default:
		return "generic"
	}
}

// IsAvalon returns true if this class represents an Avalon/Canaan device.
func (c MinerClass) IsAvalon() bool {
	return c.Vendor() == "avalon"
}

// MinerProfile contains settings for a miner class.
type MinerProfile struct {
	Class           MinerClass
	InitialDiff     float64
	MinDiff         float64
	MaxDiff         float64
	TargetShareTime int // seconds between shares

	// Observability fields (set when miner is detected)
	// These provide granular information for Prometheus metrics and logging.
	DetectedModel string // e.g., "Avalon Nano 3S", "AvalonMiner 1246"
}

// Vendor returns the miner vendor string for observability metrics.
// Returns "avalon" for all Avalon-specific classes, "generic" otherwise.
func (p MinerProfile) Vendor() string {
	return p.Class.Vendor()
}

// Model returns the detected model name for observability.
// Falls back to class name if no specific model was detected.
func (p MinerProfile) Model() string {
	if p.DetectedModel != "" {
		return p.DetectedModel
	}
	return p.Class.String()
}

// DefaultProfiles returns recommended settings for each miner class (SHA-256d).
// Difficulty formula: Diff = Hashrate × TargetShareTime / 2^32
// Example: 500 GH/s × 1s / 2^32 = 116.4 difficulty
//
// NOTE: InitialDiff/MinDiff/MaxDiff are scaled by block time (see scaleProfilesForBlockTime).
// When target time increases (timeScaleFactor > 1), MaxDiff is also scaled to maintain
// the same hashrate ceiling. When target time decreases, MaxDiff is preserved for headroom.
//
// MaxDiff provides headroom for:
//   - Miners at top of their class range
//   - Overclocked/boosted miners
//   - Vardiff convergence without hitting ceiling
var DefaultProfiles = map[MinerClass]MinerProfile{
	MinerClassUnknown: {
		Class:           MinerClassUnknown,
		InitialDiff:     500,   // Mid-range default, vardiff adjusts quickly
		MinDiff:         500,   // Same as InitialDiff - prevents vardiff dropping below optimal (was 100)
		MaxDiff:         50000, // High ceiling for unknown - vardiff will find optimal
		TargetShareTime: 1,     // 1 share per second target
	},
	MinerClassLottery: {
		Class:           MinerClassLottery,
		InitialDiff:     0.001,  // ~500 KH/s × 60s / 2^32 = 0.007, start lower
		MinDiff:         0.0001, // Can go even lower for tiny miners
		MaxDiff:         100,    // Cap for lottery miners (up to ~4 MH/s)
		TargetShareTime: 60,     // 1 share per minute is fine
	},
	MinerClassLow: {
		Class:           MinerClassLow,
		InitialDiff:     580,   // ~500 GH/s × 5s / 2^32 = 582, optimal for 5s target
		MinDiff:         580,   // Same as InitialDiff - prevents vardiff from dropping below optimal (was 500, caused 12% hashrate loss)
		MaxDiff:         150000, // Cap for ~645 GH/s (covers NMAxe at 500 GH/s with headroom)
		TargetShareTime: 5,     // 1 share per 5 seconds (was 1s - caused ~36% hashrate loss from overhead)
	},
	MinerClassMid: {
		Class:           MinerClassMid,
		InitialDiff:     1165,  // 5 TH/s × 1s / 2^32 = 1164.15, optimal for NerdQAxe baseline
		MinDiff:         1165,  // Same as InitialDiff - prevents vardiff from dropping below optimal (was 750)
		MaxDiff:         50000, // Cap for ~214 TH/s (covers NerdQAxe to NerdOctaxe with headroom)
		TargetShareTime: 1,     // 1 share per second target
	},
	MinerClassHigh: {
		Class:           MinerClassHigh,
		InitialDiff:     3260,   // 14 TH/s × 1s / 2^32 = 3261, optimal for S9 class
		MinDiff:         3260,   // Same as InitialDiff - prevents vardiff dropping below optimal (was 2000)
		MaxDiff:         100000, // Cap for ~430 TH/s (covers S9 to S17 class with headroom)
		TargetShareTime: 1,      // 1 share per second target
	},
	MinerClassPro: {
		Class:           MinerClassPro,
		InitialDiff:     25600,  // 110 TH/s × 1s / 2^32 = 25,641, optimal for S19 class
		MinDiff:         25600,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 15000)
		MaxDiff:         500000, // Cap for ~2.1 PH/s (covers S21 Pro, large farms)
		TargetShareTime: 1,      // 1 share per second target
	},

	// ========================================================================
	// AVALON-SPECIFIC PROFILES
	// ========================================================================
	// These profiles are tailored to Canaan/Avalon hardware specifications.
	// Each tier is based on actual device hashrates from Canaan documentation.
	// CRITICAL: Avalon devices use cgminer firmware which is slow to apply new
	// difficulty. MinDiff floors are set to prevent oscillation.

	// Avalon Nano Series: Consumer home miners with USB/WiFi connectivity
	// Models: Nano 2 (~3 TH/s), Nano 3 (~4 TH/s), Nano 3S (6.6 TH/s, 4 TH/s eco)
	// Power: 100-140W, Quiet operation (33-40 dB)
	// User-agents: "cgminer/4.x", "Avalon Nano", "Canaan Nano"
	MinerClassAvalonNano: {
		Class:           MinerClassAvalonNano,
		InitialDiff:     1538,  // 6.6 TH/s × 1s / 2^32 = 1537, optimal for Nano 3S full power
		MinDiff:         1538,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 800)
		MaxDiff:         2500,  // Cap: ~10.7 TH/s (overclocked Nano 3S ceiling)
		TargetShareTime: 1,     // 1 share per second for accurate credit
	},

	// Avalon Legacy Low: First-generation datacenter ASICs (mostly obsolete)
	// Models: Avalon 3 (~0.8 TH/s), Avalon 3S (~1 TH/s), Avalon 6/621/641 (~3.5 TH/s)
	// Power: 500-1100W, 18nm chips (A3218)
	// User-agents: "cgminer/4.x", "Avalon3", "Avalon6", "AvalonMiner 6xx"
	// NOTE: These devices are rare but must be supported for legacy users
	MinerClassAvalonLegacyLow: {
		Class:           MinerClassAvalonLegacyLow,
		InitialDiff:     815,   // 3.5 TH/s × 1s / 2^32 = 815, optimal for Avalon 6
		MinDiff:         815,   // Same as InitialDiff - prevents vardiff dropping below optimal (was 400)
		MaxDiff:         1500,  // Cap: ~6.4 TH/s (headroom for Avalon 6)
		TargetShareTime: 1,     // 1 share per second
	},

	// Avalon Legacy Mid: Second-generation datacenter ASICs
	// Models: Avalon 7 (721/741/761: 6-8 TH/s), Avalon 8 (821/841/851: 11-15 TH/s)
	// Power: 850-1450W, 16nm chips (A3210)
	// User-agents: "cgminer/4.x", "Avalon7", "Avalon8", "AvalonMiner 7xx/8xx"
	MinerClassAvalonLegacyMid: {
		Class:           MinerClassAvalonLegacyMid,
		InitialDiff:     2560,  // 11 TH/s × 1s / 2^32 = 2562, optimal for Avalon 8
		MinDiff:         2560,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 2000)
		MaxDiff:         5000,  // Cap: ~21 TH/s (overclocked 851)
		TargetShareTime: 1,     // 1 share per second
	},

	// Avalon Mid: Third-generation datacenter ASICs
	// Models: Avalon 9 (911/921: 18-20 TH/s), Avalon 10 (1026/1047/1066: 30-50 TH/s),
	//         Avalon 11 (1126/1146/1166: 64-81 TH/s)
	// Power: 1400-3400W, 7nm chips (A3207, A3206)
	// User-agents: "cgminer/4.x", "Avalon9", "AvalonMiner 9xx/10xx/11xx"
	MinerClassAvalonMid: {
		Class:           MinerClassAvalonMid,
		InitialDiff:     11650,  // 50 TH/s × 1s / 2^32 = 11,642, optimal for Avalon 1047
		MinDiff:         11650,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 8000)
		MaxDiff:         25000,  // Cap: ~107 TH/s (overclocked Avalon 1166)
		TargetShareTime: 1,      // 1 share per second
	},

	// Avalon High: Modern A-series datacenter ASICs (A12, A13)
	// Models: A1246 (85-96 TH/s), A1346 (104-126 TH/s), A1366 (130 TH/s)
	// Power: 3300-3500W, 5nm chips
	// User-agents: "cgminer/4.x", "AvalonMiner 12xx/13xx", "Avalon A12/A13"
	MinerClassAvalonHigh: {
		Class:           MinerClassAvalonHigh,
		InitialDiff:     25000,  // ~107 TH/s × 1s / 2^32 = 24913, typical A13
		MinDiff:         25000,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 15000)
		MaxDiff:         40000,  // Cap: ~172 TH/s (overclocked A1366)
		TargetShareTime: 1,      // 1 share per second
	},

	// Avalon Pro: Latest generation A-series datacenter ASICs (A14, A15, A16)
	// Models: A1466 (150-170 TH/s), A1566 (185 TH/s), A15Pro (215 TH/s),
	//         A15XP (200-212 TH/s), A16 (282 TH/s), A16XP (300 TH/s)
	// Power: 3200-3900W, 5nm/3nm chips
	// User-agents: "cgminer/4.x", "AvalonMiner 14xx/15xx/16xx", "Avalon A14/A15/A16"
	MinerClassAvalonPro: {
		Class:           MinerClassAvalonPro,
		InitialDiff:     45000,  // ~193 TH/s × 1s / 2^32 = 44943, typical A15
		MinDiff:         45000,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 30000)
		MaxDiff:         80000,  // Cap: ~344 TH/s (covers A16XP at 300 TH/s with OC headroom)
		TargetShareTime: 1,      // 1 share per second
	},

	// Avalon Home: Consumer home mining products
	// Models: Avalon Mini 3 (~37.5 TH/s), Avalon Q (~90 TH/s)
	// Power: 800-1674W, Quiet operation (45-65 dB)
	// Features: WiFi, Ethernet, Avalon Family App integration
	// User-agents: "cgminer/4.x", "Avalon Mini", "Avalon Q", "Canaan Home"
	MinerClassAvalonHome: {
		Class:           MinerClassAvalonHome,
		InitialDiff:     14900,  // 64 TH/s × 1s / 2^32 = 14,901, mid-range for Avalon Q
		MinDiff:         14900,  // Same as InitialDiff - prevents vardiff dropping below optimal (was 12000)
		MaxDiff:         30000,  // Cap: ~129 TH/s (overclocked Avalon Q)
		TargetShareTime: 1,      // 1 share per second
	},
}

// ScryptProfiles returns recommended settings for Scrypt coins (LTC, DOGE).
// Scrypt hashrates are measured in MH/s to GH/s (much lower than SHA-256d).
// Difficulty values are scaled accordingly.
//
// Scrypt ASIC examples:
//   - Goldshell Mini DOGE:  ~185 MH/s
//   - Bitmain Antminer L3+: ~504 MH/s
//   - Goldshell LT5 Pro:    ~2450 MH/s
//   - Bitmain Antminer L7:  ~9500 MH/s (9.5 GH/s)
//   - Bitmain Antminer L9:  ~16 GH/s
//   - Elphapex DG1:         ~14 GH/s
// ScryptProfiles defines difficulty profiles for Scrypt miners.
//
// CRITICAL: Scrypt stratum difficulty uses Litecoin's diff-1 target (0x0000FFFF...),
// which is 65536x larger than Bitcoin's (0x00000000FFFF...). This means:
//   - Expected hashes per share = D × 65536 (not D × 2^32 like SHA-256d)
//   - Correct formula: D = hashrate × targetTime / 65536
//
// Example: L7 at 9.5 GH/s, 2s target → D = 9.5e9 × 2 / 65536 ≈ 290,000
var ScryptProfiles = map[MinerClass]MinerProfile{
	MinerClassUnknown: {
		Class:           MinerClassUnknown,
		InitialDiff:     8000,    // Conservative default (~52 MH/s at 10s)
		MinDiff:         128,     // Floor for GPU miners (~838 KH/s at 10s)
		MaxDiff:         2048000, // Ceiling for ASICs (~13.4 GH/s at 10s)
		TargetShareTime: 10,
	},
	MinerClassLottery: {
		Class:           MinerClassLottery,
		InitialDiff:     0.1,    // CPU mining at ~109 H/s
		MinDiff:         0.001,  // ESP32 at ~1 H/s
		MaxDiff:         16,     // GPU at ~17.5 KH/s
		TargetShareTime: 60,
	},
	MinerClassLow: {
		Class:           MinerClassLow,
		InitialDiff:     28000,  // Mini DOGE class (~183 MH/s)
		MinDiff:         8000,   // Low-end ASIC (~52 MH/s)
		MaxDiff:         128000, // Upper range (~838 MH/s)
		TargetShareTime: 10,
	},
	MinerClassMid: {
		Class:           MinerClassMid,
		InitialDiff:     38000,  // Antminer L3+ (~498 MH/s)
		MinDiff:         16000,  // Lower-end L3 (~209 MH/s)
		MaxDiff:         256000, // L3++ and similar (~3.35 GH/s)
		TargetShareTime: 5,
	},
	MinerClassHigh: {
		Class:           MinerClassHigh,
		InitialDiff:     180000, // LT5 Pro (~2.95 GH/s)
		MinDiff:         64000,  // Low-power mode (~1.05 GH/s)
		MaxDiff:         512000, // High performance (~8.4 GH/s)
		TargetShareTime: 4,
	},
	MinerClassPro: {
		Class:           MinerClassPro,
		InitialDiff:     290000,  // Antminer L7 (~9.5 GH/s)
		MinDiff:         128000,  // L7 low-power mode (~4.2 GH/s)
		MaxDiff:         2048000, // Future ASICs / multi-rig (~67 GH/s)
		TargetShareTime: 2,
	},
}

// Spiral Router detects miner types and returns appropriate settings.
// This is Spiral Pool's custom implementation for intelligent difficulty routing.
// Supports multiple algorithms (SHA-256d, Scrypt) with separate difficulty profiles.
// Block-time aware: adjusts TargetShareTime based on blockchain's block interval.
type SpiralRouter struct {
	patterns          []minerPattern
	profiles          map[MinerClass]MinerProfile               // Active algorithm profiles (used by GetProfile)
	algorithmProfiles map[Algorithm]map[MinerClass]MinerProfile // Per-algorithm profiles
	algorithm         Algorithm                                 // Active algorithm (determines which profiles are in r.profiles)
	deviceHints       *DeviceHintsRegistry                      // IP-based device classification
	blockTime         int                                       // Block time in seconds (for scaling share targets)
	slowDiffPatterns  []string                                  // User-agent patterns for slow-diff-applying miners (e.g., cgminer)
}

type minerPattern struct {
	regex *regexp.Regexp
	class MinerClass
	name  string
}

// scaleProfilesForBlockTime adjusts TargetShareTime AND difficulty in profiles based on block time.
// The goal is to ensure miners submit enough shares per block for accurate credit,
// while not overwhelming the pool with too many shares.
//
// Scaling logic:
//   - Base profiles define optimal difficulty for their TargetShareTime
//   - Formula: Diff = Hashrate × TargetShareTime / 2^32
//   - When TargetShareTime changes, InitialDiff/MinDiff MUST scale proportionally
//   - NewDiff = OriginalDiff × (NewTargetTime / OriginalTargetTime)
//
// Algorithm-aware scaling:
//   - SHA-256d profiles have TargetShareTime=1 (except Lottery=60)
//   - Scrypt profiles have varying TargetShareTime (10, 5, 4, 2) due to lower hashrates
//   - This function respects algorithm-specific target times unless block time requires faster shares
//
// Block time constraints:
//   - We need minimum shares per block for accurate payout calculation
//   - maxTargetTime = blockTime / minSharesPerBlock
//   - newTargetTime = min(originalTargetTime, maxTargetTime)
//
// This ensures optimal difficulty regardless of blockchain or algorithm.
func scaleProfilesForBlockTime(profiles map[MinerClass]MinerProfile, blockTimeSec int) map[MinerClass]MinerProfile {
	if blockTimeSec <= 0 {
		blockTimeSec = 600 // Default to Bitcoin
	}

	scaled := make(map[MinerClass]MinerProfile, len(profiles))

	for class, profile := range profiles {
		newProfile := profile // Copy

		// Store original target time for proportional scaling
		originalTargetTime := float64(profile.TargetShareTime)
		if originalTargetTime <= 0 {
			originalTargetTime = 1 // Safety fallback
		}

		// Calculate maximum allowed TargetShareTime based on block time
		// Goal: ensure minimum shares per block for accurate payout calculation
		// Standard miners: at least 5 shares per block (maxTargetTime = blockTime/5)
		// Lottery miners: at least 1 share per block (accuracy less critical)
		var maxTargetTime float64
		var minTargetTime float64 = 1 // Minimum 1 second between shares

		switch class {
		case MinerClassLottery:
			// Lottery miners: very low hashrate, longer share times acceptable
			// At least 1 share per block, cap at 60s maximum
			maxTargetTime = float64(blockTimeSec) // 1 share per block minimum
			if maxTargetTime > 60 {
				maxTargetTime = 60 // Never more than 60s between shares
			}
			if maxTargetTime < 10 {
				maxTargetTime = 10 // At least 10s for lottery (prevents spam)
			}
			minTargetTime = 10 // Lottery miners: minimum 10s target

		case MinerClassAvalonLegacyLow:
			// Very old Avalon hardware (A3/A6) - cgminer is slow to apply difficulty
			// Need longer minimum target time to prevent oscillation
			maxTargetTime = float64(blockTimeSec) / 3.0 // At least 3 shares per block
			minTargetTime = 2                           // Minimum 2s for legacy Avalon

		default:
			// Standard miners: at least 5 shares per block for accuracy
			// This ensures good payout precision even on fast chains
			maxTargetTime = float64(blockTimeSec) / 5.0
		}

		// Ensure maxTargetTime is at least minTargetTime
		if maxTargetTime < minTargetTime {
			maxTargetTime = minTargetTime
		}

		// New target time is the MINIMUM of original and max allowed
		// This ensures we get enough shares per block while respecting algorithm-specific optimums
		// - SHA-256d with 1s target: stays at 1s (1 < maxTargetTime for most chains)
		// - Scrypt with 10s target: may be reduced on fast chains if needed
		// - Never increases target time beyond the original profile value
		newTargetTime := originalTargetTime
		if newTargetTime > maxTargetTime {
			newTargetTime = maxTargetTime
		}
		if newTargetTime < minTargetTime {
			newTargetTime = minTargetTime
		}

		// CRITICAL: Scale difficulty proportionally when TargetShareTime changes
		// Formula: NewDiff = OriginalDiff × (NewTargetTime / OriginalTargetTime)
		// This maintains the invariant: Diff = Hashrate × TargetTime / 2^32
		timeScaleFactor := newTargetTime / originalTargetTime

		newProfile.InitialDiff = profile.InitialDiff * timeScaleFactor
		newProfile.MinDiff = profile.MinDiff * timeScaleFactor
		newProfile.TargetShareTime = int(newTargetTime)

		// Scale MaxDiff when timeScaleFactor > 1 to maintain hashrate ceiling.
		// Example: avalon_legacy_low at 2s target needs 2x difficulty vs 1s target
		// to support the same 6.4 TH/s hashrate ceiling.
		// When timeScaleFactor < 1, keep original MaxDiff to preserve headroom.
		if timeScaleFactor > 1.0 {
			newProfile.MaxDiff = profile.MaxDiff * timeScaleFactor
		} else {
			newProfile.MaxDiff = profile.MaxDiff
		}

		scaled[class] = newProfile
	}

	return scaled
}

// NewSpiralRouter creates a new Spiral Router with default patterns.
// Uses default block time of 600 seconds (Bitcoin). For other chains,
// use NewSpiralRouterWithBlockTime to get properly scaled share targets.
func NewSpiralRouter() *SpiralRouter {
	return NewSpiralRouterWithBlockTime(600) // Default to Bitcoin's 10-minute blocks
}

// NewSpiralRouterWithBlockTime creates a Spiral Router with block-time-aware share targets.
// Block time affects how quickly shares should be submitted:
//   - 15s blocks (DGB): shares every 3-5 seconds
//   - 60s blocks (DOGE): shares every 10-15 seconds
//   - 600s blocks (BTC): shares every 30+ seconds
//
// This ensures miners submit enough shares per block for accurate credit.
func NewSpiralRouterWithBlockTime(blockTimeSec int) *SpiralRouter {
	// Scale profiles based on block time
	scaledSHA256 := scaleProfilesForBlockTime(DefaultProfiles, blockTimeSec)
	scaledScrypt := scaleProfilesForBlockTime(ScryptProfiles, blockTimeSec)

	r := &SpiralRouter{
		profiles: scaledSHA256,
		algorithmProfiles: map[Algorithm]map[MinerClass]MinerProfile{
			AlgorithmSHA256d: scaledSHA256,
			AlgorithmScrypt:  scaledScrypt,
		},
		algorithm:        AlgorithmSHA256d, // Default to SHA-256d; call SetAlgorithm for Scrypt coins
		deviceHints:      GetGlobalDeviceHints(), // Use global registry by default
		blockTime:        blockTimeSec,
		slowDiffPatterns: []string{"cgminer"}, // Default: cgminer-based miners are slow to apply new diff
	}

	// Define miner patterns (order matters - first match wins)
	patterns := []struct {
		pattern string
		class   MinerClass
		name    string
	}{
		// ========================================================================
		// AVALON/CANAAN MINERS - EXHAUSTIVE DETECTION
		// ========================================================================
		// CRITICAL: All Avalon patterns must be checked BEFORE lottery patterns
		// because "AvalonMiner" contains "miner" which could match lottery patterns.
		//
		// Pattern ordering is critical:
		//   1. Most specific model numbers first (e.g., A1566, 1246)
		//   2. Series patterns next (e.g., A15, 12xx)
		//   3. Generic fallbacks last (e.g., "avalon", "canaan")
		//
		// Each pattern maps to a specific Avalon class for accurate difficulty.
		// NO Avalon device should fall through to generic handling.

		// ----------------------------------------------------------------
		// AVALON HOME SERIES (Consumer products with WiFi/App support)
		// Avalon Mini 3: 37.5 TH/s @ 800W - Silent home miner
		// Avalon Q: 90 TH/s @ 1674W - High-performance home miner
		// ----------------------------------------------------------------
		{`(?i)avalon.*mini.*3`, MinerClassAvalonHome, "Avalon Mini 3"},
		{`(?i)canaan.*mini.*3`, MinerClassAvalonHome, "Canaan Mini 3"},
		{`(?i)mini.*3.*avalon`, MinerClassAvalonHome, "Mini 3 (Avalon)"},
		{`(?i)avalon[^a-z]*q\b`, MinerClassAvalonHome, "Avalon Q"},
		{`(?i)canaan[^a-z]*q\b`, MinerClassAvalonHome, "Canaan Q"},
		{`(?i)avalon.*home`, MinerClassAvalonHome, "Avalon Home"},
		{`(?i)canaan.*home`, MinerClassAvalonHome, "Canaan Home"},

		// ----------------------------------------------------------------
		// AVALON NANO SERIES (Consumer USB/WiFi miners)
		// Nano 2: ~3 TH/s, Nano 3: ~4 TH/s, Nano 3S: ~6 TH/s
		// These use cgminer firmware and may report minimal user-agent.
		// ----------------------------------------------------------------
		{`(?i)avalon.*nano.*3s`, MinerClassAvalonNano, "Avalon Nano 3S"},
		{`(?i)canaan.*nano.*3s`, MinerClassAvalonNano, "Canaan Nano 3S"}, // MUST be before nano.*3s
		{`(?i)nano.*3s`, MinerClassAvalonNano, "Nano 3S"},
		{`(?i)avalon.*nano.*3\b`, MinerClassAvalonNano, "Avalon Nano 3"},
		{`(?i)canaan.*nano.*3\b`, MinerClassAvalonNano, "Canaan Nano 3"}, // MUST be before canaan.*nano
		{`(?i)avalon.*nano.*2`, MinerClassAvalonNano, "Avalon Nano 2"},
		{`(?i)avalon.*nano`, MinerClassAvalonNano, "Avalon Nano"},
		{`(?i)canaan.*nano`, MinerClassAvalonNano, "Canaan Nano"},
		{`(?i)nano.*avalon`, MinerClassAvalonNano, "Nano (Avalon)"},
		// Nano 3 may report simply as "Avalon 3" - must distinguish from Avalon 3 (legacy)
		// Check for context clues: "nano" anywhere, or small power indicators
		{`(?i)nano[^a-z]*3`, MinerClassAvalonNano, "Nano 3"},

		// ----------------------------------------------------------------
		// AVALON PRO (A14, A15, A16 Series - Latest generation)
		// A1466: 150-170 TH/s, A1566: 185 TH/s, A16: 282 TH/s, A16XP: 300 TH/s
		// A15Pro: 215 TH/s, A15XP: 200-212 TH/s, A15: 188-203 TH/s, A15SE: 170-185 TH/s
		// ----------------------------------------------------------------
		// A16 series (newest gen, check BEFORE A15)
		{`(?i)a16\s*xp`, MinerClassAvalonPro, "Avalon A16XP"},             // 300 TH/s
		{`(?i)avalon.*a16`, MinerClassAvalonPro, "Avalon A16"},             // 282 TH/s
		// A15 variants (specific models first)
		{`(?i)a15\s*pro`, MinerClassAvalonPro, "Avalon A15Pro"},
		{`(?i)a15\s*xp`, MinerClassAvalonPro, "Avalon A15XP"},
		{`(?i)a15\s*se`, MinerClassAvalonPro, "Avalon A15SE"},
		{`(?i)avalonminer.*1566`, MinerClassAvalonPro, "AvalonMiner A1566"},
		{`(?i)avalon.*a?1566`, MinerClassAvalonPro, "Avalon A1566"},
		{`(?i)avalon.*a15`, MinerClassAvalonPro, "Avalon A15"},
		// A14 series
		{`(?i)avalonminer.*1466`, MinerClassAvalonPro, "AvalonMiner A1466"},
		{`(?i)avalon.*a?1466`, MinerClassAvalonPro, "Avalon A1466"},
		{`(?i)avalon.*a14`, MinerClassAvalonPro, "Avalon A14"},
		// Generic 14xx/15xx/16xx series patterns
		{`(?i)avalonminer.*16[0-9]{2}`, MinerClassAvalonPro, "AvalonMiner 16xx"},
		{`(?i)avalonminer.*15[0-9]{2}`, MinerClassAvalonPro, "AvalonMiner 15xx"},
		{`(?i)avalonminer.*14[0-9]{2}`, MinerClassAvalonPro, "AvalonMiner 14xx"},
		{`(?i)avalon.*16[0-9]{2}`, MinerClassAvalonPro, "Avalon 16xx"},
		{`(?i)avalon.*15[0-9]{2}`, MinerClassAvalonPro, "Avalon 15xx"},
		{`(?i)avalon.*14[0-9]{2}`, MinerClassAvalonPro, "Avalon 14xx"},

		// ----------------------------------------------------------------
		// AVALON HIGH (A12, A13 Series - Modern datacenter ASICs)
		// A1246: 85-96 TH/s, A1346: 104-126 TH/s, A1366: 130 TH/s
		// ----------------------------------------------------------------
		// A13 series (specific models)
		{`(?i)avalonminer.*1366`, MinerClassAvalonHigh, "AvalonMiner A1366"},
		{`(?i)avalonminer.*1346`, MinerClassAvalonHigh, "AvalonMiner A1346"},
		{`(?i)avalon.*a?1366`, MinerClassAvalonHigh, "Avalon A1366"},
		{`(?i)avalon.*a?1346`, MinerClassAvalonHigh, "Avalon A1346"},
		{`(?i)avalon.*a13`, MinerClassAvalonHigh, "Avalon A13"},
		// A12 series (specific models)
		{`(?i)avalonminer.*1246`, MinerClassAvalonHigh, "AvalonMiner A1246"},
		{`(?i)avalon.*a?1246`, MinerClassAvalonHigh, "Avalon A1246"},
		{`(?i)avalon.*a12`, MinerClassAvalonHigh, "Avalon A12"},
		// Generic 12xx/13xx series patterns
		{`(?i)avalonminer.*13[0-9]{2}`, MinerClassAvalonHigh, "AvalonMiner 13xx"},
		{`(?i)avalonminer.*12[0-9]{2}`, MinerClassAvalonHigh, "AvalonMiner 12xx"},
		{`(?i)avalon.*13[0-9]{2}`, MinerClassAvalonHigh, "Avalon 13xx"},
		{`(?i)avalon.*12[0-9]{2}`, MinerClassAvalonHigh, "Avalon 12xx"},

		// ----------------------------------------------------------------
		// AVALON MID (A9, A10, A11 Series - Third-generation datacenter)
		// A911/921: 18-20 TH/s, A1026/1047/1066: 30-50 TH/s
		// A1126/1146/1166: 64-81 TH/s
		// ----------------------------------------------------------------
		// A11 series (specific models)
		{`(?i)avalonminer.*1166`, MinerClassAvalonMid, "AvalonMiner 1166"},
		{`(?i)avalonminer.*1146`, MinerClassAvalonMid, "AvalonMiner 1146"},
		{`(?i)avalonminer.*1126`, MinerClassAvalonMid, "AvalonMiner 1126"},
		{`(?i)avalon.*1166`, MinerClassAvalonMid, "Avalon 1166"},
		{`(?i)avalon.*1146`, MinerClassAvalonMid, "Avalon 1146"},
		{`(?i)avalon.*1126`, MinerClassAvalonMid, "Avalon 1126"},
		// A10 series (specific models)
		{`(?i)avalonminer.*1066`, MinerClassAvalonMid, "AvalonMiner 1066"},
		{`(?i)avalonminer.*1047`, MinerClassAvalonMid, "AvalonMiner 1047"},
		{`(?i)avalonminer.*1026`, MinerClassAvalonMid, "AvalonMiner 1026"},
		{`(?i)avalon.*1066`, MinerClassAvalonMid, "Avalon 1066"},
		{`(?i)avalon.*1047`, MinerClassAvalonMid, "Avalon 1047"},
		{`(?i)avalon.*1026`, MinerClassAvalonMid, "Avalon 1026"},
		// A9 series (specific models)
		{`(?i)avalonminer.*921`, MinerClassAvalonMid, "AvalonMiner 921"},
		{`(?i)avalonminer.*911`, MinerClassAvalonMid, "AvalonMiner 911"},
		{`(?i)avalon.*921`, MinerClassAvalonMid, "Avalon 921"},
		{`(?i)avalon.*911`, MinerClassAvalonMid, "Avalon 911"},
		// Generic 9xx/10xx/11xx series patterns
		{`(?i)avalonminer.*11[0-9]{2}`, MinerClassAvalonMid, "AvalonMiner 11xx"},
		{`(?i)avalonminer.*10[0-9]{2}`, MinerClassAvalonMid, "AvalonMiner 10xx"},
		{`(?i)avalonminer.*9[0-9]{2}`, MinerClassAvalonMid, "AvalonMiner 9xx"},
		{`(?i)avalon.*11[0-9]{2}`, MinerClassAvalonMid, "Avalon 11xx"},
		{`(?i)avalon.*10[0-9]{2}`, MinerClassAvalonMid, "Avalon 10xx"},
		{`(?i)avalon.*9[0-9]{2}`, MinerClassAvalonMid, "Avalon 9xx"},

		// ----------------------------------------------------------------
		// AVALON LEGACY MID (A7, A8 Series - Second-generation)
		// A721/741/761: 6-8 TH/s, A821/841/851: 11-15 TH/s
		// ----------------------------------------------------------------
		// A8 series (specific models)
		{`(?i)avalonminer.*851`, MinerClassAvalonLegacyMid, "AvalonMiner 851"},
		{`(?i)avalonminer.*841`, MinerClassAvalonLegacyMid, "AvalonMiner 841"},
		{`(?i)avalonminer.*821`, MinerClassAvalonLegacyMid, "AvalonMiner 821"},
		{`(?i)avalon.*851`, MinerClassAvalonLegacyMid, "Avalon 851"},
		{`(?i)avalon.*841`, MinerClassAvalonLegacyMid, "Avalon 841"},
		{`(?i)avalon.*821`, MinerClassAvalonLegacyMid, "Avalon 821"},
		// A7 series (specific models)
		{`(?i)avalonminer.*761`, MinerClassAvalonLegacyMid, "AvalonMiner 761"},
		{`(?i)avalonminer.*741`, MinerClassAvalonLegacyMid, "AvalonMiner 741"},
		{`(?i)avalonminer.*721`, MinerClassAvalonLegacyMid, "AvalonMiner 721"},
		{`(?i)avalon.*761`, MinerClassAvalonLegacyMid, "Avalon 761"},
		{`(?i)avalon.*741`, MinerClassAvalonLegacyMid, "Avalon 741"},
		{`(?i)avalon.*721`, MinerClassAvalonLegacyMid, "Avalon 721"},
		// Generic 7xx/8xx series patterns
		{`(?i)avalonminer.*8[0-9]{2}`, MinerClassAvalonLegacyMid, "AvalonMiner 8xx"},
		{`(?i)avalonminer.*7[0-9]{2}`, MinerClassAvalonLegacyMid, "AvalonMiner 7xx"},
		{`(?i)avalon.*8[0-9]{2}`, MinerClassAvalonLegacyMid, "Avalon 8xx"},
		{`(?i)avalon.*7[0-9]{2}`, MinerClassAvalonLegacyMid, "Avalon 7xx"},

		// ----------------------------------------------------------------
		// AVALON LEGACY LOW (A3, A6 Series - First-generation)
		// A3/3S: 0.8-1 TH/s, A6/621/641: 3.5 TH/s
		// NOTE: "Avalon 3" without "Nano" context = legacy Avalon 3, NOT Nano 3
		// ----------------------------------------------------------------
		// A6 series (specific models)
		{`(?i)avalonminer.*641`, MinerClassAvalonLegacyLow, "AvalonMiner 641"},
		{`(?i)avalonminer.*621`, MinerClassAvalonLegacyLow, "AvalonMiner 621"},
		{`(?i)avalon.*641`, MinerClassAvalonLegacyLow, "Avalon 641"},
		{`(?i)avalon.*621`, MinerClassAvalonLegacyLow, "Avalon 621"},
		{`(?i)avalon[^a-z]*6\b`, MinerClassAvalonLegacyLow, "Avalon 6"},
		// A3 series - CAREFUL: Must not match "Nano 3" or "Mini 3"
		// Only match if NOT preceded by "nano" or "mini"
		{`(?i)avalonminer.*3s?\b`, MinerClassAvalonLegacyLow, "AvalonMiner 3/3S"},
		{`(?i)avalon[^a-z]*3s\b`, MinerClassAvalonLegacyLow, "Avalon 3S"},
		// Generic 6xx series pattern
		{`(?i)avalonminer.*6[0-9]{2}`, MinerClassAvalonLegacyLow, "AvalonMiner 6xx"},
		{`(?i)avalon.*6[0-9]{2}`, MinerClassAvalonLegacyLow, "Avalon 6xx"},

		// ----------------------------------------------------------------
		// AVALON GENERIC FALLBACKS
		// These catch any Avalon/Canaan device not matched by specific patterns.
		// Uses MinerClassAvalonMid as safe default (MaxDiff 25000 provides headroom).
		// ----------------------------------------------------------------
		{`(?i)avalonminer`, MinerClassAvalonMid, "AvalonMiner (generic)"},
		{`(?i)avalon\s*miner`, MinerClassAvalonMid, "Avalon Miner (generic)"},
		{`(?i)avalon`, MinerClassAvalonMid, "Avalon (generic)"},
		{`(?i)canaan`, MinerClassAvalonMid, "Canaan (generic)"},

		// ========================================================================
		// NON-AVALON MINERS
		// ========================================================================

		// Lottery miners (ESP32-based, very low hashrate)
		// NerdMinerV2 specific — must come BEFORE generic nerdminer catch-all
		{`(?i)nerdminerv2`, MinerClassLottery, "NerdMiner"},
		{`(?i)nerdminer`, MinerClassLottery, "ESP32 Miner"},
		{`(?i)han.?solo`, MinerClassLottery, "ESP32 Miner"},  // HAN_SOLOminer firmware variant
		{`(?i)^nminer`, MinerClassLottery, "NMiner"},  // ^ anchor to avoid matching AvalonMiner
		{`(?i)\bnminer`, MinerClassLottery, "NMiner"}, // Word boundary version
		{`(?i)bitmaker`, MinerClassLottery, "BitMaker"},
		{`(?i)nerd.*esp`, MinerClassLottery, "NerdESP"},
		{`(?i)lottery`, MinerClassLottery, "LotteryMiner"},
		{`(?i)esp32`, MinerClassLottery, "ESP32"},
		{`(?i)arduino`, MinerClassLottery, "Arduino"},
		{`(?i)sparkminer`, MinerClassLottery, "ESP32 Miner"},  // SparkMiner ESP32 firmware

		// Mid-range (multi-ASIC boards, NerdQAxe) - check specific models FIRST
		// NerdOctaxe Gamma: 8x BM1370, 9.6 TH/s, 160W - check BEFORE generic nerdaxe
		{`(?i)nerdoctaxe`, MinerClassMid, "NerdOctaxe Gamma"},
		{`(?i)octaxe`, MinerClassMid, "NerdOctaxe"},
		{`(?i)nerdq?axe`, MinerClassMid, "NerdQAxe"},
		// BitAxe Hex variants (6-chip boards) - check BEFORE single-chip models
		// Supra Hex 701/702: 6x BM1368, ~3.5-4.2 TH/s, 90W
		{`(?i)bitaxe.*supra.*hex`, MinerClassMid, "BitAxe Supra Hex"},
		{`(?i)supra.*hex`, MinerClassMid, "Supra Hex"},
		// Ultra Hex: 6x BM1366, ~3+ TH/s
		{`(?i)bitaxe.*ultra.*hex`, MinerClassMid, "BitAxe Ultra Hex"},
		{`(?i)ultra.*hex`, MinerClassMid, "Ultra Hex"},
		// Generic Hex (catches remaining hex models)
		{`(?i)bitaxe.*hex`, MinerClassMid, "BitAxe Hex"},
		// BitAxe GT 801 (Gamma Turbo): 2x BM1370, ~2.15 TH/s, 43W - check BEFORE generic gamma
		{`(?i)bitaxe.*gt`, MinerClassMid, "BitAxe GT"},
		{`(?i)bitaxe.*801`, MinerClassMid, "BitAxe GT 801"},
		{`(?i)bitaxe.*turbo`, MinerClassMid, "BitAxe GT"},
		{`(?i)gamma.*turbo`, MinerClassMid, "BitAxe GT"},
		{`(?i)bitaxe.*gamma`, MinerClassMid, "BitAxe Gamma"},
		{`(?i)gekkoscience`, MinerClassMid, "GekkoScience"},

		// Low-end (single BitAxe class, small USB miners) - generic patterns AFTER specific models
		{`(?i)bitaxe.*ultra`, MinerClassLow, "BitAxe Ultra"},
		{`(?i)bitaxe.*supra`, MinerClassLow, "BitAxe Supra"},
		{`(?i)nmaxe`, MinerClassLow, "NMAxe"},   // NMAxe (~500 GH/s, Gamma fork)
		{`(?i)bitaxe`, MinerClassLow, "BitAxe"}, // Generic bitaxe (catches all remaining)
		// ESP-Miner firmware: Used by BitAxe (~500 GH/s), NerdQAxe++ (~5 TH/s), and others.
		// Classify as MinerClassLow (MaxDiff 10K) - vardiff will quickly ramp up for
		// higher-hashrate devices like NerdQAxe++. This prevents massive overshoot.
		// Note: CYD/lottery ESP32 devices use ESP32 miner firmware, not ESP-Miner.
		{`(?i)esp-miner`, MinerClassLow, "ESP-Miner"},

		// ----------------------------------------------------------------
		// LUCKY MINER (SHA-256d + Scrypt)
		// Chinese BitAxe clones with BM1366 chip, ESP-Miner/AxeOS firmware
		// SHA-256d: LV06: 500 GH/s @ 13W, LV07: 1 TH/s @ 30W, LV08: 4.5 TH/s @ 120W
		// Scrypt:   LG07: 11 MH/s @ 25W (Lottery-class for Scrypt)
		// ----------------------------------------------------------------
		{`(?i)lucky.*miner.*lv08`, MinerClassMid, "Lucky Miner LV08"},    // 4.5 TH/s SHA-256d
		{`(?i)lucky.*miner.*lv07`, MinerClassMid, "Lucky Miner LV07"},    // 1 TH/s SHA-256d
		{`(?i)lucky.*lv08`, MinerClassMid, "Lucky Miner LV08"},
		{`(?i)lucky.*lv07`, MinerClassMid, "Lucky Miner LV07"},
		{`(?i)lv08`, MinerClassMid, "LV08"},
		{`(?i)lv07`, MinerClassMid, "LV07"},
		{`(?i)lucky.*miner.*lv06`, MinerClassLow, "Lucky Miner LV06"},    // 500 GH/s SHA-256d
		{`(?i)lucky.*lv06`, MinerClassLow, "Lucky Miner LV06"},
		{`(?i)lv06`, MinerClassLow, "LV06"},
		// LG07 (Scrypt): 11 MH/s @ 25W - tiny Scrypt ASIC
		// Low class: diff 8 gives ~90s shares (slow but functional, no pool flooding)
		// Lottery MaxDiff=1 would cause ~2 shares/sec = pool flooding
		{`(?i)lucky.*miner.*lg07`, MinerClassLow, "Lucky Miner LG07"},    // 11 MH/s Scrypt
		{`(?i)lucky.*lg07`, MinerClassLow, "Lucky Miner LG07"},
		{`(?i)lg07`, MinerClassLow, "LG07"},
		{`(?i)lucky.*miner`, MinerClassLow, "Lucky Miner"},              // Generic fallback

		{`(?i)compac.*f`, MinerClassLow, "Compac F"},
		{`(?i)futurebit.*moonlander`, MinerClassLow, "Moonlander"}, // FutureBit Moonlander 2

		// More mid-range
		{`(?i)compac`, MinerClassMid, "Compac"},
		{`(?i)newpac`, MinerClassMid, "NewPac"},
		{`(?i)r606`, MinerClassMid, "R606"},

		// ----------------------------------------------------------------
		// JINGLE MINER (SHA-256d)
		// BM1370 chip, ESP-Miner based firmware (AxeOS-style API)
		// BTC Solo Lite: 1.2 TH/s @ 23W, BTC Solo Pro: 4.8 TH/s @ 96W
		// ----------------------------------------------------------------
		{`(?i)jingle.*miner.*pro`, MinerClassMid, "Jingle Miner BTC Solo Pro"},  // 4.8 TH/s
		{`(?i)jingle.*pro`, MinerClassMid, "Jingle Miner Pro"},
		{`(?i)btc.*solo.*pro`, MinerClassMid, "BTC Solo Pro"},
		{`(?i)jingle.*miner.*lite`, MinerClassMid, "Jingle Miner BTC Solo Lite"}, // 1.2 TH/s
		{`(?i)jingle.*lite`, MinerClassMid, "Jingle Miner Lite"},
		{`(?i)btc.*solo.*lite`, MinerClassMid, "BTC Solo Lite"},
		{`(?i)jingleminer`, MinerClassMid, "JingleMiner"},            // MUST be before jingle.*miner
		{`(?i)jingle.*miner`, MinerClassMid, "Jingle Miner"},

		// ----------------------------------------------------------------
		// FUTUREBIT APOLLO (SHA-256d)
		// Desktop home miner with built-in full node
		// Gen1: 2-3.8 TH/s @ 125-200W, Gen2 (Apollo II): 6-9 TH/s @ 175-375W
		// Uses CGMiner API or proprietary web interface
		// ----------------------------------------------------------------
		{`(?i)futurebit.*apollo.*ii`, MinerClassMid, "FutureBit Apollo II"},  // 6-9 TH/s
		{`(?i)apollo.*ii`, MinerClassMid, "Apollo II"},
		{`(?i)futurebit.*apollo`, MinerClassMid, "FutureBit Apollo"},         // 2-3.8 TH/s
		{`(?i)apollo.*btc`, MinerClassMid, "Apollo BTC"},
		{`(?i)futurebit`, MinerClassMid, "FutureBit"},

		// ----------------------------------------------------------------
		// ZYBER MINERS (SHA-256d) - TinyChipHub
		// Premium home miners with AxeOS firmware (ESP-Miner fork)
		// Zyber 8S: 6.4 TH/s @ 140W, 8x BM1368 chips
		// Zyber 8G: 10+ TH/s @ 180W, 8x BM1370 chips (up to 12 TH/s OC)
		// ----------------------------------------------------------------
		{`(?i)zyber.*8gp`, MinerClassMid, "Zyber 8GP"},       // 10+ TH/s (home variant) — MUST be before 8g
		{`(?i)zyber.*8g`, MinerClassMid, "Zyber 8G"},         // 10+ TH/s
		{`(?i)zyber.*8s`, MinerClassMid, "Zyber 8S"},         // 6.4 TH/s
		{`(?i)zyber`, MinerClassMid, "Zyber"},                // Generic Zyber
		{`(?i)tinychip`, MinerClassMid, "TinyChipHub"},       // TinyChipHub devices

		// High-end (older ASICs, small farms) - 10-80 TH/s range
		{`(?i)antminer.*s9`, MinerClassHigh, "Antminer S9"},
		{`(?i)antminer.*s11`, MinerClassHigh, "Antminer S11"},
		{`(?i)antminer.*s15`, MinerClassHigh, "Antminer S15"},
		{`(?i)antminer.*s17`, MinerClassHigh, "Antminer S17"},
		{`(?i)antminer.*t9`, MinerClassHigh, "Antminer T9"},
		{`(?i)antminer.*t15`, MinerClassHigh, "Antminer T15"},
		{`(?i)antminer.*t17`, MinerClassHigh, "Antminer T17"},
		{`(?i)antminer.*t19`, MinerClassPro, "Antminer T19"},  // 84 TH/s - Pro class
		{`(?i)whatsminer.*m30s\+\+`, MinerClassPro, "Whatsminer M30S++"},  // 112 TH/s - Pro class
		{`(?i)whatsminer.*m30s\+`, MinerClassPro, "Whatsminer M30S+"},    // 100 TH/s - Pro class
		{`(?i)whatsminer.*m30s`, MinerClassPro, "Whatsminer M30S"},       // 86-88 TH/s - Pro class
		{`(?i)whatsminer.*m[12]0s?\b`, MinerClassHigh, "Whatsminer M10-M20"},  // 55-68 TH/s
		{`(?i)innosilicon.*t[12]\b`, MinerClassHigh, "Innosilicon T1/T2"},
		{`(?i)innosilicon.*t3`, MinerClassHigh, "Innosilicon T3"},
		// Ebang/Ebit miners (37-44 TH/s)
		{`(?i)ebang`, MinerClassHigh, "Ebang"},
		{`(?i)ebit.*e1[12]`, MinerClassHigh, "Ebit E11/E12"},
		{`(?i)ebit`, MinerClassHigh, "Ebit"},

		// Modern ASICs (high hashrate) - 100+ TH/s range
		{`(?i)antminer.*s19`, MinerClassPro, "Antminer S19"},
		{`(?i)antminer.*s21`, MinerClassPro, "Antminer S21"},
		{`(?i)antminer.*t21`, MinerClassPro, "Antminer T21"},
		// Whatsminer M60 series (150-226 TH/s air-cooled)
		{`(?i)whatsminer.*m60`, MinerClassPro, "Whatsminer M60"},
		// Whatsminer M63 series (334-390 TH/s hydro-cooled)
		{`(?i)whatsminer.*m63`, MinerClassPro, "Whatsminer M63"},
		// Whatsminer M66 series (240-356 TH/s immersion)
		{`(?i)whatsminer.*m66`, MinerClassPro, "Whatsminer M66"},
		// Whatsminer M70 series (Dec 2025 — air, hydro, immersion, rack)
		// M79/M79S: 870-1040 TH/s hydro rack, M78/M78S: 440-522 TH/s immersion
		// M76/M76S/M76S+: 336-440 TH/s immersion, M73/M73S+: 470-600 TH/s hydro
		// M72/M72S: 246-300 TH/s air OC, M70/M70S: 220-258 TH/s air
		{`(?i)whatsminer.*m79`, MinerClassPro, "Whatsminer M79"},
		{`(?i)whatsminer.*m78`, MinerClassPro, "Whatsminer M78"},
		{`(?i)whatsminer.*m76`, MinerClassPro, "Whatsminer M76"},
		{`(?i)whatsminer.*m73`, MinerClassPro, "Whatsminer M73"},
		{`(?i)whatsminer.*m72`, MinerClassPro, "Whatsminer M72"},
		{`(?i)whatsminer.*m70`, MinerClassPro, "Whatsminer M70"},
		{`(?i)whatsminer.*m[5-9]0`, MinerClassPro, "Whatsminer M50+"},
		{`(?i)whatsminer.*m[3-4]0s`, MinerClassPro, "Whatsminer M30S+"},
		{`(?i)braiins`, MinerClassPro, "Braiins"},
		{`(?i)vnish`, MinerClassPro, "Vnish"},
		{`(?i)luxos|luxminer`, MinerClassPro, "LuxOS"},

		// ----------------------------------------------------------------
		// BITDEER / SEALMINER (SHA-256d)
		// SEAL chip architecture (4nm/5nm), proprietary firmware
		// A3 (Sep 2025): A3 260 TH/s, A3 Pro 310 TH/s, A3 Hydro 500 TH/s, A3 Pro Hydro 660 TH/s
		// A2: 226 TH/s, A2 Pro: 255 TH/s, A2 Hydro: 446 TH/s, A2 Pro Hydro: 500+ TH/s
		// ----------------------------------------------------------------
		// A3 series (newest gen, check BEFORE A2)
		{`(?i)sealminer.*a3.*pro.*hydro`, MinerClassPro, "Sealminer A3 Pro Hydro"}, // 660 TH/s
		{`(?i)sealminer.*a3.*hydro`, MinerClassPro, "Sealminer A3 Hydro"},          // 500 TH/s
		{`(?i)sealminer.*a3.*pro`, MinerClassPro, "Sealminer A3 Pro"},              // 310 TH/s
		{`(?i)sealminer.*a3`, MinerClassPro, "Sealminer A3"},                       // 260 TH/s
		// A2 series
		{`(?i)sealminer.*a2.*pro.*hydro`, MinerClassPro, "Sealminer A2 Pro Hydro"}, // 500 TH/s
		{`(?i)sealminer.*a2.*hydro`, MinerClassPro, "Sealminer A2 Hydro"},          // 446 TH/s
		{`(?i)sealminer.*a2.*pro`, MinerClassPro, "Sealminer A2 Pro"},              // 255 TH/s
		{`(?i)sealminer.*a2`, MinerClassPro, "Sealminer A2"},                       // 226 TH/s
		{`(?i)sealminer`, MinerClassPro, "Sealminer"},
		{`(?i)bitdeer`, MinerClassPro, "Bitdeer"},

		// ----------------------------------------------------------------
		// AURADINE / TERAFLUX (SHA-256d)
		// US-based, proprietary firmware (FluxVision), EnergyTune/AutoTune
		// AH3880: 600 TH/s hydro, AI3680: 365-375 TH/s, AT2880: 180-260 TH/s
		// ----------------------------------------------------------------
		{`(?i)teraflux.*ah3880`, MinerClassPro, "Auradine Teraflux AH3880"},  // 600 TH/s hydro
		{`(?i)teraflux.*ai3680`, MinerClassPro, "Auradine Teraflux AI3680"},  // 375 TH/s
		{`(?i)teraflux.*at2880`, MinerClassPro, "Auradine Teraflux AT2880"},  // 260 TH/s
		{`(?i)teraflux`, MinerClassPro, "Auradine Teraflux"},
		{`(?i)auradine`, MinerClassPro, "Auradine"},

		// ----------------------------------------------------------------
		// GOLDSHELL MINERS (Scrypt - LTC/DOGE)
		// Full Scrypt ASIC lineup: Mini DOGE series, LT series, DG Max, BYTE
		// Power: 65W (BYTE DG Card) to 3400W (DG Max)
		// User-agents: "goldshell", model names, or "cgminer" (internal)
		// ----------------------------------------------------------------
		// DG Max: 6.5 GH/s @ 3400W, industrial Scrypt (Nov 2024)
		{`(?i)goldshell.*dg.*max`, MinerClassPro, "Goldshell DG Max"},
		// LT6 series (high-end Scrypt): 3.35 GH/s — MinerClassHigh, NOT Pro.
		// At Pro Scrypt (MinDiff=128000, target=2s), D_optimal=102234 falls BELOW MinDiff floor.
		// At High Scrypt (MinDiff=64000, target=4s), D_optimal=204468 — vardiff has full range.
		{`(?i)goldshell.*lt6`, MinerClassHigh, "Goldshell LT6"},
		{`(?i)lt6.*goldshell`, MinerClassHigh, "Goldshell LT6"},
		// LT5 series (mid-high Scrypt): ~2-2.5 GH/s
		{`(?i)goldshell.*lt5`, MinerClassHigh, "Goldshell LT5"},
		{`(?i)lt5.*goldshell`, MinerClassHigh, "Goldshell LT5"},
		// LT Lite: 1.62 GH/s @ 1450W, mid-range rack miner
		{`(?i)goldshell.*lt.*lite`, MinerClassMid, "Goldshell LT Lite"},
		// Mini DOGE series (specific models BEFORE generic — "III" before "II")
		{`(?i)goldshell.*mini.*doge.*iii.*(\+|plus)`, MinerClassLow, "Goldshell Mini DOGE III+"}, // 810 MH/s
		{`(?i)goldshell.*mini.*doge.*iii`, MinerClassLow, "Goldshell Mini DOGE III"},        // 700 MH/s
		{`(?i)goldshell.*mini.*doge.*ii`, MinerClassLow, "Goldshell Mini DOGE II"},           // 420 MH/s
		{`(?i)goldshell.*mini.*doge.*pro`, MinerClassLow, "Goldshell Mini DOGE Pro"},         // 205 MH/s
		{`(?i)goldshell.*mini.*doge`, MinerClassLow, "Goldshell Mini DOGE"},                  // 185 MH/s
		{`(?i)mini.*doge`, MinerClassLow, "Mini DOGE"},
		// BYTE modular platform (80 MH/s per DG Card, swappable)
		{`(?i)goldshell.*byte`, MinerClassLow, "Goldshell BYTE"},
		// Generic Goldshell (fallback to mid-range)
		{`(?i)goldshell`, MinerClassMid, "Goldshell"},

		// ----------------------------------------------------------------
		// ANTMINER L SERIES (Scrypt - LTC/DOGE)
		// Bitmain's Scrypt ASIC lineup for Litecoin/Dogecoin mining
		// ----------------------------------------------------------------
		// L11 (newest gen): 20-35 GH/s, 3612-5775W
		{`(?i)antminer.*l11.*hydro`, MinerClassPro, "Antminer L11 Hydro"}, // 33-35 GH/s
		{`(?i)antminer.*l11.*pro`, MinerClassPro, "Antminer L11 Pro"},     // 21 GH/s
		{`(?i)antminer.*l11`, MinerClassPro, "Antminer L11"},              // 20 GH/s
		// L9: 16-17 GH/s, 3360-3570W
		{`(?i)antminer.*l9`, MinerClassPro, "Antminer L9"},
		// L7: 9.5 GH/s, 3425W
		{`(?i)antminer.*l7`, MinerClassPro, "Antminer L7"},
		// L3+ (older): 504 MH/s, 800W - still widely used
		{`(?i)antminer.*l3`, MinerClassLow, "Antminer L3+"},

		// ----------------------------------------------------------------
		// ELPHAPEX MINERS (Scrypt - LTC/DOGE)
		// DG2 series (2025): DG2+/DG2 20.5 GH/s, DG2 Mini 2.4 GH/s
		// DG Hydro 1: 20 GH/s water-cooled
		// DG1 series: DG1+ 14 GH/s, DG1/DG1 Lite 11 GH/s, DG Home 2.1 GH/s
		// ----------------------------------------------------------------
		// DG2 series (newest gen — check BEFORE DG1)
		{`(?i)elphapex.*dg2\+`, MinerClassPro, "Elphapex DG2+"},             // 20.5 GH/s, best efficiency
		{`(?i)dg2\+`, MinerClassPro, "DG2+"},
		{`(?i)elphapex.*dg2.*mini`, MinerClassHigh, "Elphapex DG2 Mini"},    // 2.4 GH/s home
		{`(?i)elphapex.*dg2`, MinerClassPro, "Elphapex DG2"},                // 20.5 GH/s
		// DG Hydro 1: 20 GH/s, water-cooled industrial
		{`(?i)elphapex.*dg.*hydro`, MinerClassPro, "Elphapex DG Hydro"},     // 20 GH/s
		{`(?i)dg.*hydro`, MinerClassPro, "DG Hydro"},
		// DG1+ (high-end): ~14 GH/s
		{`(?i)elphapex.*dg1\+`, MinerClassPro, "Elphapex DG1+"},
		{`(?i)dg1\+`, MinerClassPro, "DG1+"},
		// DG1/DG1 Lite (standard): ~11 GH/s
		{`(?i)elphapex.*dg1.*lite`, MinerClassPro, "Elphapex DG1 Lite"},     // 11 GH/s budget
		{`(?i)elphapex.*dg1`, MinerClassPro, "Elphapex DG1"},
		// DG Home 1: ~2.1 GH/s, 630W - home miner
		{`(?i)elphapex.*dg.*home`, MinerClassHigh, "Elphapex DG Home"},
		{`(?i)dg.*home`, MinerClassHigh, "DG Home"},
		// Generic Elphapex
		{`(?i)elphapex`, MinerClassHigh, "Elphapex"},

		// ----------------------------------------------------------------
		// VOLCMINER (Scrypt - LTC/DOGE)
		// Chinese manufacturer, proprietary firmware with web GUI
		// D1 Hydro: 30.4 GH/s, D3: 20 GH/s, D1 Pro: 18 GH/s
		// D1: 15-18.5 GH/s, D1 Lite: 14 GH/s, D1 Mini: 2.2 GH/s
		// ----------------------------------------------------------------
		{`(?i)volcminer.*d1.*hydro`, MinerClassPro, "VolcMiner D1 Hydro"},  // 30.4 GH/s
		{`(?i)volcminer.*d1.*pro`, MinerClassPro, "VolcMiner D1 Pro"},      // 18 GH/s
		{`(?i)volcminer.*d3`, MinerClassPro, "VolcMiner D3"},               // 20 GH/s
		{`(?i)volcminer.*d1.*lite`, MinerClassPro, "VolcMiner D1 Lite"},    // 14 GH/s
		{`(?i)volcminer.*d1.*mini`, MinerClassHigh, "VolcMiner D1 Mini"},   // 2.2 GH/s
		{`(?i)volcminer.*d1`, MinerClassPro, "VolcMiner D1"},               // 15-18.5 GH/s
		{`(?i)volcminer`, MinerClassPro, "VolcMiner"},

		// ----------------------------------------------------------------
		// FLUMINER (SHA-256d + Scrypt)
		// Chinese manufacturer: Flu Electronic Technology (Hong Kong) Co., Ltd.
		// Proprietary firmware with web UI, stratum protocol support
		// L-series: Scrypt (LTC/DOGE), T-series: SHA-256d (BTC)
		// ----------------------------------------------------------------
		// T3 (SHA-256d): 115 TH/s @ 1700W, silent home miner
		{`(?i)fluminer.*t3`, MinerClassPro, "FluMiner T3"},
		{`(?i)flu.*miner.*t3`, MinerClassPro, "FluMiner T3"},
		// L3 (Scrypt): 9.5 GH/s @ 1700W - check BEFORE L1 to prevent L1 matching L13/L3
		{`(?i)fluminer.*l3`, MinerClassPro, "FluMiner L3"},
		{`(?i)flu.*miner.*l3`, MinerClassPro, "FluMiner L3"},
		// L2 (Scrypt): 1-1.2 GH/s @ 230-280W, speaker box form factor
		{`(?i)fluminer.*l2`, MinerClassMid, "FluMiner L2"},
		{`(?i)flu.*miner.*l2`, MinerClassMid, "FluMiner L2"},
		// L1 Pro (Scrypt): 6 GH/s @ 1400W - check BEFORE L1
		{`(?i)fluminer.*l1.*pro`, MinerClassPro, "FluMiner L1 Pro"},
		{`(?i)flu.*miner.*l1.*pro`, MinerClassPro, "FluMiner L1 Pro"},
		// L1 (Scrypt): 5.3 GH/s @ 1200W
		{`(?i)fluminer.*l1`, MinerClassPro, "FluMiner L1"},
		{`(?i)flu.*miner.*l1`, MinerClassPro, "FluMiner L1"},
		// Generic FluMiner fallback
		{`(?i)fluminer`, MinerClassPro, "FluMiner"},
		{`(?i)flu.*miner`, MinerClassMid, "FluMiner"},

		// ----------------------------------------------------------------
		// IBELINK MINERS (Scrypt - LTC/DOGE)
		// BM-L3: 3.2 GH/s @ 3000W, actively shipping
		// ----------------------------------------------------------------
		{`(?i)ibelink.*bm.*l3`, MinerClassHigh, "iBeLink BM-L3"},  // 3.2 GH/s Scrypt
		{`(?i)ibelink`, MinerClassHigh, "iBeLink"},

		// ----------------------------------------------------------------
		// HAMMER MINER / PLEBSOURCE (Scrypt - LTC/DOGE)
		// Entry-level Scrypt ASIC: 105 MH/s, 25W, WiFi connected
		// Made by PlebSource, single-chip design
		// ----------------------------------------------------------------
		{`(?i)hammer.*miner`, MinerClassLow, "Hammer Miner"},
		{`(?i)hammerminer`, MinerClassLow, "Hammer Miner"},
		{`(?i)plebsource.*hammer`, MinerClassLow, "PlebSource Hammer"},
		{`(?i)plebsource`, MinerClassLow, "PlebSource"},
		{`(?i)doge.*digger`, MinerClassLow, "Doge Digger"},

		// Stock manufacturer firmware (identifies brand, not specific model)
		// Bitmain sends "bmminer/X.X.X" on stock firmware (S9 through S21 XP Hyd)
		// MicroBT sends "btminer/X.X.X" on stock firmware (M30S through M79S)
		// Pro class: modern ASICs dominate usage; MaxDiff=500K covers up to ~2.1 PH/s.
		// Older models (S9 etc.) get vardiff-adjusted down within seconds.
		{`(?i)bmminer`, MinerClassPro, "Bitmain Stock Firmware"},
		{`(?i)btminer`, MinerClassPro, "MicroBT Stock Firmware"},

		// Common mining software
		// cgminer is used by many devices including Avalon Nano 3s (~6.6 TH/s)
		// Use MinerClassMid to support Avalon-class devices (MaxDiff=50000 needed for 6.6 TH/s)
		// Lower hashrate devices using cgminer will simply stabilize at lower difficulty
		{`(?i)cgminer`, MinerClassMid, "cgminer"},
		{`(?i)bfgminer`, MinerClassMid, "bfgminer"},
		{`(?i)sgminer`, MinerClassLow, "sgminer"},
		{`(?i)cpuminer`, MinerClassLow, "cpuminer"},
		{`(?i)ccminer`, MinerClassLow, "ccminer"},

		// ----------------------------------------------------------------
		// HASHRATE RENTAL SERVICES (User-Agent Detection)
		// ----------------------------------------------------------------
		// USER-AGENT DETECTION: Vendor names below are required for miner identification.
		// These are technical identifiers from the miner's user-agent string, not endorsements,
		// recommendations, or affiliations. Accurate detection enables proper initial difficulty
		// assignment for miners connecting via these services. The authors have no business
		// relationship with any of these services. All trademarks are property of their
		// respective owners. Users are solely responsible for any fees, costs, or agreements
		// with third-party hashrate rental services.
		//
		// MinerClassPro is used because rented hashrate typically represents aggregated
		// high-performance mining capacity requiring higher initial difficulty.
		{`(?i)nicehash|excavator`, MinerClassPro, "NiceHash"},
		{`(?i)miningrigrentals|mrr`, MinerClassPro, "MiningRigRentals"},
		{`(?i)cudo`, MinerClassPro, "Cudo Miner"},
		{`(?i)zergpool`, MinerClassPro, "Zergpool"},
		{`(?i)prohashing`, MinerClassPro, "Prohashing"},
		{`(?i)miningdutch|mining.dutch`, MinerClassPro, "Mining Dutch"},
		{`(?i)zpool`, MinerClassPro, "Zpool"},
		{`(?i)woolypooly|wooly`, MinerClassPro, "WoolyPooly"},
		{`(?i)herominers|hero`, MinerClassPro, "HeroMiners"},
		{`(?i)unmineable`, MinerClassPro, "unMineable"},
	}

	for _, p := range patterns {
		compiled, err := regexp.Compile(p.pattern)
		if err != nil {
			continue // Skip invalid patterns
		}
		r.patterns = append(r.patterns, minerPattern{
			regex: compiled,
			class: p.class,
			name:  p.name,
		})
	}

	return r
}

// DetectMiner identifies the miner class from user-agent string.
func (r *SpiralRouter) DetectMiner(userAgent string) (class MinerClass, name string) {
	if userAgent == "" {
		return MinerClassUnknown, "Unknown"
	}

	// Clean up user agent
	userAgent = strings.TrimSpace(userAgent)

	// Try to match against known patterns
	for _, pattern := range r.patterns {
		if pattern.regex.MatchString(userAgent) {
			return pattern.class, pattern.name
		}
	}

	// Default: unknown miners get mid-range difficulty
	// This is safer than assuming they're ASICs
	return MinerClassUnknown, "Unknown"
}

// GetProfile returns the difficulty profile for a miner class.
func (r *SpiralRouter) GetProfile(class MinerClass) MinerProfile {
	if profile, ok := r.profiles[class]; ok {
		return profile
	}
	// Fallback: try to get MinerClassUnknown from profiles (which has proper scaled values)
	if profile, ok := r.profiles[MinerClassUnknown]; ok {
		return profile
	}
	// Ultimate fallback - should never reach here if profiles are initialized correctly
	// Using conservative MaxDiff=50000 to match DefaultProfiles[MinerClassUnknown]
	return MinerProfile{
		Class:           MinerClassUnknown,
		InitialDiff:     500,
		MinDiff:         500,
		MaxDiff:         50000, // Was 1000000 - caused runaway difficulty!
		TargetShareTime: 1,
	}
}

// GetInitialDifficulty returns the recommended initial difficulty for a user-agent.
func (r *SpiralRouter) GetInitialDifficulty(userAgent string) float64 {
	class, _ := r.DetectMiner(userAgent)
	profile := r.GetProfile(class)
	return profile.InitialDiff
}

// SetProfile allows customizing a miner class profile.
func (r *SpiralRouter) SetProfile(class MinerClass, profile MinerProfile) {
	r.profiles[class] = profile
}

// AddPattern adds a custom miner detection pattern.
func (r *SpiralRouter) AddPattern(pattern string, class MinerClass, name string) error {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	// Prepend to give custom patterns priority
	r.patterns = append([]minerPattern{{
		regex: compiled,
		class: class,
		name:  name,
	}}, r.patterns...)
	return nil
}

// Global default Spiral Router instance
var defaultRouter = NewSpiralRouter()

// DetectMinerClass is a convenience function using the default router.
func DetectMinerClass(userAgent string) (MinerClass, string) {
	return defaultRouter.DetectMiner(userAgent)
}

// GetMinerInitialDifficulty is a convenience function using the default router.
func GetMinerInitialDifficulty(userAgent string) float64 {
	return defaultRouter.GetInitialDifficulty(userAgent)
}

// GetProfileForAlgorithm returns the difficulty profile for a miner class and algorithm.
// This is the primary method for multi-algorithm support.
func (r *SpiralRouter) GetProfileForAlgorithm(class MinerClass, algorithm Algorithm) MinerProfile {
	// Check algorithm-specific profiles
	if algProfiles, ok := r.algorithmProfiles[algorithm]; ok {
		if profile, ok := algProfiles[class]; ok {
			return profile
		}
		// Try MinerClassUnknown for this algorithm
		if profile, ok := algProfiles[MinerClassUnknown]; ok {
			return profile
		}
	}

	// Fall back to default profiles (SHA-256d)
	if profile, ok := r.profiles[class]; ok {
		return profile
	}
	// Try MinerClassUnknown from default profiles
	if profile, ok := r.profiles[MinerClassUnknown]; ok {
		return profile
	}

	// Ultimate fallback - should never reach here if profiles are initialized correctly
	// Using conservative MaxDiff=50000 to match DefaultProfiles[MinerClassUnknown]
	return MinerProfile{
		Class:           MinerClassUnknown,
		InitialDiff:     500,
		MinDiff:         500,
		MaxDiff:         50000, // Was 1000000 - caused runaway difficulty!
		TargetShareTime: 1,
	}
}

// GetInitialDifficultyForAlgorithm returns the recommended initial difficulty
// for a user-agent and algorithm combination.
func (r *SpiralRouter) GetInitialDifficultyForAlgorithm(userAgent string, algorithm Algorithm) float64 {
	class, _ := r.DetectMiner(userAgent)
	profile := r.GetProfileForAlgorithm(class, algorithm)
	return profile.InitialDiff
}

// SetAlgorithmProfile allows customizing a miner class profile for a specific algorithm.
func (r *SpiralRouter) SetAlgorithmProfile(algorithm Algorithm, class MinerClass, profile MinerProfile) {
	if r.algorithmProfiles == nil {
		r.algorithmProfiles = make(map[Algorithm]map[MinerClass]MinerProfile)
	}
	if r.algorithmProfiles[algorithm] == nil {
		r.algorithmProfiles[algorithm] = make(map[MinerClass]MinerProfile)
	}
	r.algorithmProfiles[algorithm][class] = profile
}

// GetMinerInitialDifficultyForAlgorithm is a convenience function using the default router.
func GetMinerInitialDifficultyForAlgorithm(userAgent string, algorithm Algorithm) float64 {
	return defaultRouter.GetInitialDifficultyForAlgorithm(userAgent, algorithm)
}

// GetProfileForAlgorithmDefault is a convenience function using the default router.
func GetProfileForAlgorithmDefault(class MinerClass, algorithm Algorithm) MinerProfile {
	return defaultRouter.GetProfileForAlgorithm(class, algorithm)
}

// SetDeviceHints sets the device hints registry for IP-based classification.
func (r *SpiralRouter) SetDeviceHints(registry *DeviceHintsRegistry) {
	r.deviceHints = registry
}

// GetDeviceHints returns the device hints registry.
func (r *SpiralRouter) GetDeviceHints() *DeviceHintsRegistry {
	return r.deviceHints
}

// DetectMinerWithIP identifies the miner class using both IP hints and user-agent.
// IP-based device hints take priority over user-agent detection.
// This is the preferred method when the miner's IP address is known.
func (r *SpiralRouter) DetectMinerWithIP(userAgent, remoteAddr string) (class MinerClass, name string) {
	// First, check device hints registry by IP
	if r.deviceHints != nil && remoteAddr != "" {
		hint := r.deviceHints.Get(remoteAddr)
		if hint != nil {
			// Device hint found - use it
			if hint.Class != MinerClassUnknown {
				return hint.Class, hint.DeviceModel
			}
		}
	}

	// Fall back to user-agent detection
	return r.DetectMiner(userAgent)
}

// GetInitialDifficultyWithIP returns the recommended initial difficulty using IP hints.
// This is the preferred method when the miner's IP address is known.
// When a device hint with known hashrate exists, difficulty is calculated directly from
// the hashrate using the formula: Diff = Hashrate × TargetShareTime / 2^32
// This is more accurate than class-based profiles for devices with known performance.
func (r *SpiralRouter) GetInitialDifficultyWithIP(userAgent, remoteAddr string) float64 {
	// First, check device hints registry by IP for hashrate-based calculation
	if r.deviceHints != nil && remoteAddr != "" {
		hint := r.deviceHints.Get(remoteAddr)
		if hint != nil && hint.HashrateGHs > 0 {
			// Calculate difficulty directly from hashrate
			// Formula: Diff = Hashrate (H/s) × TargetShareTime / 2^32
			// HashrateGHs is in GH/s, so multiply by 1e9 to get H/s
			profile := r.GetProfile(hint.Class)
			targetTime := float64(profile.TargetShareTime)
			if targetTime <= 0 {
				targetTime = 5 // Default to 5 seconds
			}

			// Diff = (HashrateGHs * 1e9) * targetTime / 2^32
			// Simplified: Diff = HashrateGHs * targetTime * 1e9 / 4294967296
			// Which is approximately: Diff = HashrateGHs * targetTime * 0.2328
			calculatedDiff := hint.HashrateGHs * 1e9 * targetTime / 4294967296.0

			// Clamp to profile's min/max bounds
			if calculatedDiff < profile.MinDiff {
				calculatedDiff = profile.MinDiff
			}
			if calculatedDiff > profile.MaxDiff {
				calculatedDiff = profile.MaxDiff
			}

			return calculatedDiff
		}
	}

	// Fall back to class-based profile difficulty
	class, _ := r.DetectMinerWithIP(userAgent, remoteAddr)
	profile := r.GetProfile(class)
	return profile.InitialDiff
}

// GetProfileWithIP returns the full MinerProfile using IP hints.
// Use this when you need all profile settings (MinDiff, MaxDiff, TargetShareTime).
func (r *SpiralRouter) GetProfileWithIP(userAgent, remoteAddr string) MinerProfile {
	class, name := r.DetectMinerWithIP(userAgent, remoteAddr)
	profile := r.GetProfile(class)
	profile.DetectedModel = name // Set for observability

	return profile
}

// GetProfileWithIPForAlgorithm returns the full MinerProfile for a specific algorithm.
func (r *SpiralRouter) GetProfileWithIPForAlgorithm(userAgent, remoteAddr string, algorithm Algorithm) MinerProfile {
	class, name := r.DetectMinerWithIP(userAgent, remoteAddr)
	profile := r.GetProfileForAlgorithm(class, algorithm)
	profile.DetectedModel = name // Set for observability
	return profile
}

// GetInitialDifficultyWithIPForAlgorithm returns the recommended initial difficulty
// using IP hints for a specific algorithm.
// When a device hint with known hashrate exists, difficulty is calculated directly.
func (r *SpiralRouter) GetInitialDifficultyWithIPForAlgorithm(userAgent, remoteAddr string, algorithm Algorithm) float64 {
	// First, check device hints registry by IP for hashrate-based calculation
	if r.deviceHints != nil && remoteAddr != "" {
		hint := r.deviceHints.Get(remoteAddr)
		if hint != nil && hint.HashrateGHs > 0 {
			// Calculate difficulty directly from hashrate
			profile := r.GetProfileForAlgorithm(hint.Class, algorithm)
			targetTime := float64(profile.TargetShareTime)
			if targetTime <= 0 {
				targetTime = 5 // Default to 5 seconds
			}

			// Diff = (HashrateGHs * 1e9) * targetTime / 2^32
			calculatedDiff := hint.HashrateGHs * 1e9 * targetTime / 4294967296.0

			// Clamp to profile's min/max bounds
			if calculatedDiff < profile.MinDiff {
				calculatedDiff = profile.MinDiff
			}
			if calculatedDiff > profile.MaxDiff {
				calculatedDiff = profile.MaxDiff
			}

			return calculatedDiff
		}
	}

	// Fall back to class-based profile difficulty
	class, _ := r.DetectMinerWithIP(userAgent, remoteAddr)
	profile := r.GetProfileForAlgorithm(class, algorithm)
	return profile.InitialDiff
}

// AlgorithmFromString converts a string algorithm name to Algorithm type.
func AlgorithmFromString(s string) Algorithm {
	switch strings.ToLower(s) {
	case "sha256d", "sha256":
		return AlgorithmSHA256d
	case "scrypt":
		return AlgorithmScrypt
	default:
		return AlgorithmSHA256d // Default to SHA256d
	}
}

// SetBlockTime reconfigures the router with a new block time.
// This rescales all difficulty profiles to ensure appropriate share rates.
// Call this when the pool knows the actual blockchain being mined.
// Respects the stored algorithm: r.profiles points to the active algorithm's scaled profiles.
func (r *SpiralRouter) SetBlockTime(blockTimeSec int) {
	if blockTimeSec <= 0 {
		return // Invalid, keep current settings
	}

	r.blockTime = blockTimeSec
	scaledSHA256 := scaleProfilesForBlockTime(DefaultProfiles, blockTimeSec)
	scaledScrypt := scaleProfilesForBlockTime(ScryptProfiles, blockTimeSec)
	r.algorithmProfiles[AlgorithmSHA256d] = scaledSHA256
	r.algorithmProfiles[AlgorithmScrypt] = scaledScrypt

	// Set r.profiles to the active algorithm's scaled profiles
	if r.algorithm == AlgorithmScrypt {
		r.profiles = scaledScrypt
	} else {
		r.profiles = scaledSHA256
	}
}

// SetAlgorithm configures which algorithm's profiles are used as the default.
// For Scrypt pools (LTC, DOGE, PEP, CAT, DGB-SCRYPT), this switches from
// SHA-256d profiles to Scrypt profiles with appropriate difficulty scales.
// For SHA-256d pools, this is a no-op (SHA-256d is already the default).
// Call this after SetBlockTime so the correct scaled profiles are active.
func (r *SpiralRouter) SetAlgorithm(algo Algorithm) {
	r.algorithm = algo
	if algProfiles, ok := r.algorithmProfiles[algo]; ok {
		r.profiles = algProfiles
	}
	// If unknown algorithm, keep current profiles (SHA-256d default)
}

// GetBlockTime returns the configured block time in seconds.
func (r *SpiralRouter) GetBlockTime() int {
	return r.blockTime
}

// GetDefaultTargetTime returns the default scaled target share time for unclassified miners.
// This is used by pool handlers to set up initial vardiff state BEFORE miner classification.
// Uses MinerClassLow profile's scaled target time as the default (safe for most ASICs).
// Returns scaled value based on configured block time:
//   - 15s blocks (DGB):     3s target (15/5 = 3)
//   - 60s blocks (DOGE):    5s target (min of 60/5=12 and default 5)
//   - 150s blocks (LTC):    5s target (min of 150/5=30 and default 5)
//   - 600s blocks (BTC):    5s target (default)
// For merge mining, uses the PARENT chain's block time (aux chains piggyback on parent shares).
func (r *SpiralRouter) GetDefaultTargetTime() float64 {
	// Try to get the scaled target time from MinerClassLow (most common ASIC class)
	if profile, ok := r.profiles[MinerClassLow]; ok && profile.TargetShareTime > 0 {
		return float64(profile.TargetShareTime)
	}
	// Fallback: calculate from block time using same formula as scaleProfilesForBlockTime
	if r.blockTime > 0 {
		maxTargetTime := float64(r.blockTime) / 5.0 // Standard: at least 5 shares per block
		targetTime := 5.0                           // Default for Bitcoin (600s blocks)
		if maxTargetTime < targetTime {
			targetTime = maxTargetTime
		}
		if targetTime < 1.0 {
			targetTime = 1.0 // Minimum 1 second
		}
		return targetTime
	}
	return 5.0 // Ultimate fallback: Bitcoin default
}

// SetSlowDiffPatterns configures the user-agent patterns that identify miners
// which are slow to apply new difficulty (e.g., cgminer-based firmware).
// These miners need longer cooldown between retargets.
// If patterns is nil or empty, defaults to ["cgminer"].
func (r *SpiralRouter) SetSlowDiffPatterns(patterns []string) {
	if len(patterns) == 0 {
		r.slowDiffPatterns = []string{"cgminer"}
	} else {
		r.slowDiffPatterns = patterns
	}
}

// IsSlowDiffApplier checks if a miner (by user-agent) is slow to apply new difficulty.
// cgminer-based miners (Avalon, etc.) don't apply set_difficulty to work-in-progress,
// so they need longer cooldown between retargets (30s instead of 5s).
// Returns true if any configured pattern matches (case-insensitive substring match).
func (r *SpiralRouter) IsSlowDiffApplier(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	for _, pattern := range r.slowDiffPatterns {
		if strings.Contains(ua, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}
