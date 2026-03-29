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

	// Farm proxy / hashrate aggregator class.
	// A single upstream connection from a proxy carries aggregated hashrate from
	// many downstream miners (default aggregation = 50 workers per connection).
	// At 50× S21 Pro (~200 TH/s each) = ~10 PH/s per connection — far above
	// MinerClassPro's MaxDiff ceiling of 500,000 (~2.1 PH/s).
	// Examples: Braiins Farm Proxy, any other stratum aggregation proxy.
	MinerClassFarmProxy // Stratum aggregation proxy (~500 GH/s – 100+ PH/s per connection)

	// Hashrate marketplace / rental service class.
	// Connections arrive from rental platforms (NiceHash, MiningRigRentals, etc.).
	// Each connection may be an individual rig OR routed through the marketplace's
	// own stratum proxy infrastructure, meaning per-connection hashrate is unknown.
	// MinerClassPro's MaxDiff=500K (~2.1 PH/s) is too low if the platform proxies
	// multiple machines through one upstream connection.
	// MaxDiff=50M gives a ~214 PH/s ceiling while starting at the same level as Pro.
	MinerClassHashMarketplace // Hashrate rental marketplace (~100 TH/s – 214+ PH/s per connection)
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
	case MinerClassFarmProxy:
		return "farm_proxy"
	case MinerClassHashMarketplace:
		return "hash_marketplace"
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
	case MinerClassFarmProxy:
		return "proxy"
	case MinerClassHashMarketplace:
		return "marketplace"
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
		InitialDiff:     500,     // Mid-range start, vardiff adjusts quickly
		MinDiff:         100,     // Low floor — allows small miners to settle
		MaxDiff:         1000000, // 1M ceiling — 2x S19 Pro class (500k), lets vardiff find optimal
		TargetShareTime: 1,       // 1 share per second target
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

	// Farm Proxy / Stratum Aggregation Proxy
	// A single upstream connection from a proxy carries aggregated hashrate from many
	// downstream miners. Default aggregation for Braiins Farm Proxy = 50 workers per
	// upstream connection. At 50× S21 Pro (~200 TH/s each) = ~10 PH/s per connection.
	//
	// InitialDiff: 500,000 — matches MinerClassPro ceiling; vardiff immediately ramps up
	// MinDiff:      25,600  — prevents vardiff from dropping below S19-class single miner
	// MaxDiff: 100,000,000  — ~429 PH/s ceiling (handles even the largest proxy aggregations)
	// TargetShareTime: 1    — 1 share per second for fast vardiff convergence
	MinerClassFarmProxy: {
		Class:           MinerClassFarmProxy,
		InitialDiff:     500000,      // Start at MinerClassPro ceiling; vardiff ramps instantly
		MinDiff:         25600,       // S19-class floor — prevents oscillation on small proxy loads
		MaxDiff:         100000000,   // ~429 PH/s ceiling — handles any realistic proxy aggregation
		TargetShareTime: 1,           // 1 share per second for fast vardiff convergence
	},

	// Hashrate Marketplace / Rental Service
	// Connections from NiceHash, MiningRigRentals, etc. May be individual rigs or routed
	// through the marketplace's own stratum proxy infrastructure. Per-connection hashrate
	// is unknown — could be a single S21 (~200 TH/s) or a platform proxy carrying multiple PH/s.
	//
	// InitialDiff = MinDiff: 25,600 — S19-class floor, same as MinerClassPro.
	//   Vardiff ramps up immediately for high-hashrate connections. For individual rigs,
	//   it stabilizes at the rig's optimal difficulty. The MinDiff=InitialDiff floor matches
	//   the test invariant and prevents excessive share spam on connect.
	// MaxDiff: 50,000,000 — ~214 PH/s ceiling; much higher than MinerClassPro's 2.1 PH/s.
	MinerClassHashMarketplace: {
		Class:           MinerClassHashMarketplace,
		InitialDiff:     25600,      // S19-class start — vardiff ramps up quickly for proxy loads
		MinDiff:         25600,      // Same as InitialDiff — test invariant satisfied
		MaxDiff:         50000000,   // ~214 PH/s ceiling — handles marketplace proxy aggregation
		TargetShareTime: 1,          // 1 share per second
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
		MinDiff:         128,     // Floor for low-power miners (~838 KH/s at 10s)
		MaxDiff:         2048000, // Ceiling for ASICs (~13.4 GH/s at 10s)
		TargetShareTime: 10,
	},
	MinerClassLottery: {
		Class:           MinerClassLottery,
		InitialDiff:     0.1,    // CPU mining at ~109 H/s
		MinDiff:         0.001,  // ESP32 at ~1 H/s
		MaxDiff:         16,     // Lottery ceiling at ~17.5 KH/s
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

	// Farm Proxy Scrypt: aggregated Scrypt hashrate from downstream proxy workers.
	// Most farm proxies target SHA-256d, but included for completeness.
	// D = hashrate × targetTime / 65536
	// InitialDiff: 2,048,000 — matches MinerClassPro Scrypt MaxDiff; vardiff ramps up
	// MaxDiff:   200,000,000  — ~6.7 TH/s Scrypt ceiling (handles any realistic proxy load)
	MinerClassFarmProxy: {
		Class:           MinerClassFarmProxy,
		InitialDiff:     2048000,    // Start at MinerClassPro Scrypt ceiling
		MinDiff:         128000,     // L7 low-power floor — prevents drop on small proxy loads
		MaxDiff:         200000000,  // ~6.7 TH/s Scrypt ceiling
		TargetShareTime: 2,
	},

	// Hashrate Marketplace Scrypt: rental platforms delivering Scrypt hashrate.
	// D = hashrate × targetTime / 65536
	// InitialDiff = MinDiff: 128,000 — L7 low-power equivalent floor.
	// MaxDiff: 100,000,000 — ~3.4 TH/s Scrypt ceiling.
	MinerClassHashMarketplace: {
		Class:           MinerClassHashMarketplace,
		InitialDiff:     128000,     // L7 low-power start — vardiff ramps up for heavy loads
		MinDiff:         128000,     // Same as InitialDiff
		MaxDiff:         100000000,  // ~3.4 TH/s Scrypt ceiling
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
		// MINER USER-AGENT PATTERNS
		// ========================================================================
		// Source verification key:
		//   CONFIRMED = verified in manufacturer's GitHub source code
		//   HIGH      = confirmed via pyasic, test fixtures, or multiple pool codebases
		//   MEDIUM    = confirmed in 1-2 secondary sources
		//
		// IMPORTANT: Many miners share generic firmware user-agents:
		//   - ALL Avalon/Canaan → "cgminer/4.x.x" (ckolivas/cgminer)
		//   - ALL Bitmain/Antminer → "bmminer/X.X.X" (bitmaintech/bmminer-mix)
		//   - ALL MicroBT/Whatsminer → "btminer/X.X.X" (pyasic)
		//   - ALL BitAxe/ESP-Miner → "bitaxe/{ASIC}/{ver}" (bitaxeorg/ESP-Miner)
		//   - GekkoScience → "cgminer/4.13.x" (GekkoScience/cgminer)
		//   - FutureBit Apollo → "bfgminer/5.4.0" (luke-jr/bfgminer)
		//
		// For specific model identification (NMAxe vs BitAxe Ultra, Antminer S9 vs S21,
		// Avalon Nano 3S vs A16XP), use DeviceHints (IP-based HTTP API discovery).
		// See devicehints.go and SpiralSentinel for model-level classification.

		// ----------------------------------------------------------------
		// TIER 1: CONFIRMED — verified in manufacturer source code
		// ----------------------------------------------------------------

		// NerdMiner V2: ESP32 lottery miner, ~78 KH/s
		// Source: BitMaker-hub/NerdMiner_v2 — sends "NerdMinerV2/{version}" [CONFIRMED]
		{`(?i)nerdminerv2`, MinerClassLottery, "NerdMiner V2"},

		// HAN_SOLOminer: ESP32 lottery miner firmware variant
		// Source: valerio-vaccaro/HAN_SOLOminer [CONFIRMED]
		{`(?i)han.?solo`, MinerClassLottery, "HAN_SOLOminer"},

		// NerdQAxe++: 4x BM1370, ~5 TH/s — sends "NerdQAxe++/BM1370/{ver}"
		// Source: WantClue/ESP-Miner-NerdQAxePlus [CONFIRMED]
		// MUST be before generic nerdq?axe pattern
		{`(?i)nerdqaxe\+\+`, MinerClassMid, "NerdQAxe++"},

		// NerdOctaxe: 8x BM1370, ~9.6 TH/s — sends "NerdOCTAXE/BM1370/{ver}"
		// Source: WantClue ESP-Miner fork [CONFIRMED]
		{`(?i)nerdoctaxe`, MinerClassMid, "NerdOctaxe"},
		{`(?i)octaxe`, MinerClassMid, "NerdOctaxe"},

		// Generic NerdAxe/NerdQAxe catch-all
		{`(?i)nerdq?axe`, MinerClassMid, "NerdQAxe"},

		// JingleMiner: BM1370, ESP-Miner based
		// Sends "JingleMiner" (static, no version) [CONFIRMED]
		{`(?i)jingleminer`, MinerClassMid, "JingleMiner"},
		{`(?i)jingle.*miner`, MinerClassMid, "Jingle Miner"},

		// Zyber miners: TinyChipHub, 8x BM1368/BM1370
		// Source: TinyChipHub/ESP-Miner-Zyber — sends "Zyber8S/{ver}" or "Zyber8G/{ver}" [CONFIRMED]
		{`(?i)zyber`, MinerClassMid, "Zyber"},

		// BitAxe / ESP-Miner devices — sends "bitaxe/{ASIC_CHIP}/{version}"
		// Source: bitaxeorg/ESP-Miner stratum_api.c [CONFIRMED]
		// Covers: BitAxe Ultra, Supra, Gamma, GT, Hex, NMAxe, Lucky Miner, all ESP-Miner forks
		{`(?i)bitaxe/bm1370`, MinerClassLow, "BitAxe (BM1370)"},
		{`(?i)bitaxe/bm1368`, MinerClassLow, "BitAxe (BM1368)"},
		{`(?i)bitaxe/bm1366`, MinerClassLow, "BitAxe (BM1366)"},
		{`(?i)bitaxe/bm1397`, MinerClassLow, "BitAxe (BM1397)"},
		{`(?i)bitaxe`, MinerClassLow, "BitAxe"},

		// Bitmain stock firmware — sends "bmminer/{version}"
		// Source: bitmaintech/bmminer-mix config.h [CONFIRMED]
		// Covers: ALL Antminer S/T series (S9 through S21 XP Hyd)
		{`(?i)bmminer`, MinerClassPro, "Bitmain (bmminer)"},

		// MicroBT stock firmware — sends "btminer/{version}"
		// Source: pyasic [HIGH]
		// Covers: ALL Whatsminer models (M30S through M79S)
		{`(?i)btminer`, MinerClassPro, "MicroBT (btminer)"},

		// Braiins Farm Proxy — MUST match before generic Braiins
		// Source: Braiins documentation [CONFIRMED]
		{`(?i)braiins.*farm[_-]?proxy`, MinerClassFarmProxy, "Braiins Farm Proxy"},
		{`(?i)farm[_-]?proxy`, MinerClassFarmProxy, "Farm Proxy"},
		{`(?i)farmproxy`, MinerClassFarmProxy, "Farm Proxy"},

		// Braiins OS+ — aftermarket firmware for Antminer/Whatsminer
		// Source: test fixtures — sends "Braiins OS {version}" [CONFIRMED]
		{`(?i)braiins`, MinerClassPro, "Braiins OS"},

		// ----------------------------------------------------------------
		// TIER 2: HIGH/MEDIUM confidence from secondary sources
		// ----------------------------------------------------------------

		// Vnish — aftermarket Antminer firmware [MEDIUM]
		{`(?i)vnish`, MinerClassPro, "Vnish"},

		// LuxOS — aftermarket Antminer firmware [MEDIUM]
		{`(?i)luxos|luxminer`, MinerClassPro, "LuxOS"},

		// NiceHash — hashrate marketplace
		// Source: btcpool/yaamp — sends "NiceHash/{version}" or "excavator" [HIGH]
		{`(?i)nicehash|excavator`, MinerClassHashMarketplace, "NiceHash"},

		// ----------------------------------------------------------------
		// TIER 3: GENERIC mining software
		// ----------------------------------------------------------------
		// cgminer covers: ALL Avalon (6.6-300 TH/s), GekkoScience (~2 TH/s),
		//   Goldshell (Scrypt), FutureBit Apollo, some Innosilicon — 45,000x range.
		// MinerClassUnknown (MinDiff=100, MaxDiff=1M) lets vardiff find optimal.
		// For Avalon model classification, use DeviceHints (IP-based discovery).
		{`(?i)cgminer`, MinerClassUnknown, "cgminer"},

		// bfgminer covers: FutureBit Apollo (~2-9 TH/s), various
		{`(?i)bfgminer`, MinerClassUnknown, "bfgminer"},

		// General-purpose mining software
		{`(?i)sgminer`, MinerClassLow, "sgminer"},
		{`(?i)cpuminer`, MinerClassLow, "cpuminer"},
		{`(?i)ccminer`, MinerClassLow, "ccminer"},

		// ----------------------------------------------------------------
		// TIER 4: LOTTERY miners (ESP32, no ASIC)
		// ----------------------------------------------------------------
		{`(?i)^nminer`, MinerClassLottery, "NMiner"},
		{`(?i)\bnminer`, MinerClassLottery, "NMiner"},
		{`(?i)bitmaker`, MinerClassLottery, "BitMaker"},
		{`(?i)esp32`, MinerClassLottery, "ESP32"},
		{`(?i)arduino`, MinerClassLottery, "Arduino"},
		{`(?i)sparkminer`, MinerClassLottery, "SparkMiner"},
		{`(?i)lottery`, MinerClassLottery, "LotteryMiner"},

		// ----------------------------------------------------------------
		// TIER 5: HASHRATE MARKETPLACES
		// ----------------------------------------------------------------
		// USER-AGENT DETECTION: Vendor names are technical identifiers from
		// the miner's user-agent string, not endorsements or affiliations.
		// All trademarks are property of their respective owners. Users are
		// solely responsible for any fees, costs, or agreements with third-party
		// hashrate rental services.
		{`(?i)miningrigrentals|mrr`, MinerClassHashMarketplace, "MiningRigRentals"},
		{`(?i)cudo`, MinerClassHashMarketplace, "Cudo Miner"},
		{`(?i)zergpool`, MinerClassHashMarketplace, "Zergpool"},
		{`(?i)prohashing`, MinerClassHashMarketplace, "Prohashing"},
		{`(?i)miningdutch|mining\.dutch`, MinerClassHashMarketplace, "Mining Dutch"},
		{`(?i)zpool`, MinerClassHashMarketplace, "Zpool"},
		{`(?i)woolypooly|wooly`, MinerClassHashMarketplace, "WoolyPooly"},
		{`(?i)herominers|hero`, MinerClassHashMarketplace, "HeroMiners"},
		{`(?i)unmineable`, MinerClassHashMarketplace, "unMineable"},
		{`(?i)2miners`, MinerClassHashMarketplace, "2Miners"},
		{`(?i)kryptex`, MinerClassHashMarketplace, "Kryptex"},
		{`(?i)hash.*to.*coin|hashtocoins?`, MinerClassHashMarketplace, "HashToCoins"},
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
	return MinerProfile{
		Class:           MinerClassUnknown,
		InitialDiff:     500,
		MinDiff:         100,
		MaxDiff:         1000000, // 1M — wide range lets vardiff find optimal for any miner
		TargetShareTime: 1,
	}
}

// GetInitialDifficulty returns the recommended initial difficulty for a user-agent.
func (r *SpiralRouter) GetInitialDifficulty(userAgent string) float64 {
	class, _ := r.DetectMiner(userAgent)
	profile := r.GetProfile(class)
	return profile.InitialDiff
}

// GetAllProfiles returns a copy of all active (scaled) profiles.
func (r *SpiralRouter) GetAllProfiles() map[MinerClass]MinerProfile {
	out := make(map[MinerClass]MinerProfile, len(r.profiles))
	for k, v := range r.profiles {
		out[k] = v
	}
	return out
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
	return MinerProfile{
		Class:           MinerClassUnknown,
		InitialDiff:     500,
		MinDiff:         100,
		MaxDiff:         1000000, // 1M — wide range lets vardiff find optimal for any miner
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
