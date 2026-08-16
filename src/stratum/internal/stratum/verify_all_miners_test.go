// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package stratum

import (
	"testing"
)

// TestVerifyAllMinerProfiles verifies that ALL miner profiles have correct
// InitialDiff, MinDiff, MaxDiff values to prevent the 30x divergence issue.
func TestVerifyAllMinerProfiles(t *testing.T) {
	// Test with DGB block time (15 seconds) - fast chain
	router := NewSpiralRouterWithBlockTime(15)

	// All user agents to test — using REAL firmware UA strings
	// (hardware model names are never sent in stratum UA strings)
	testCases := []struct {
		userAgent     string
		expectedClass string
	}{
		// Lottery class (ESP32 miners)
		{"NerdMinerV2/1.5.3", "lottery"},
		{"ESP32Miner/1.0", "lottery"},
		{"NMiner/1.0", "lottery"},
		{"NMMiner/v0.6.30", "lottery"},
		{"LeafMiner/1.0", "lottery"},

		// Low class (BitAxe BM1366/BM1368)
		{"bitaxe/BM1366/v2.9.31", "low"},
		{"bitaxe/BM1368/v2.5.0", "low"},

		// Mid class (NerdQAxe, NerdOctaxe — BM1370+)
		{"NerdQAxe++/BM1370/v1.0.36", "mid"},
		{"nerdqaxe/1.0", "mid"},

		// Pro class (Bitmain/MicroBT/Braiins firmware UAs)
		{"bmminer/2.0.0", "pro"},
		{"btminer/3.0.1", "pro"},
		{"Braiins OS 24.04", "pro"},

		// Generic mining clients (reclassified to unknown in v1.1.3)
		{"cgminer/4.11.1", "unknown"},
		{"bfgminer/5.5.0", "unknown"},

		// Unknown
		{"SomeRandomMiner/1.0", "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.userAgent, func(t *testing.T) {
			class, name := router.DetectMiner(tc.userAgent)
			profile := router.GetProfileForAlgorithm(class, AlgorithmSHA256d)

			// 1. Verify class detection
			if class.String() != tc.expectedClass {
				t.Errorf("Class mismatch: got %s, expected %s", class.String(), tc.expectedClass)
			}

			// 2. Verify MinDiff = InitialDiff (except lottery and unknown which intentionally differ)
			// Unknown: MinDiff=100 (low floor), InitialDiff=500 (mid start) — vardiff ramps up
			if tc.expectedClass != "lottery" && tc.expectedClass != "unknown" && profile.MinDiff != profile.InitialDiff {
				t.Errorf("MinDiff (%.6f) != InitialDiff (%.6f) - will cause vardiff oscillation",
					profile.MinDiff, profile.InitialDiff)
			}

			// 3. Verify MaxDiff is NOT 1 trillion (the engine default bug)
			// Unknown class intentionally uses 1M ceiling so vardiff can ramp up for any ASIC
			if tc.expectedClass != "unknown" && profile.MaxDiff >= 1000000 {
				t.Errorf("MaxDiff = %.0f is TOO HIGH (>=1 million) for class %s", profile.MaxDiff, tc.expectedClass)
			}

			// 4. Verify MaxDiff is reasonable for the class
			if profile.MaxDiff <= 0 {
				t.Errorf("MaxDiff = %.0f is invalid (must be > 0)", profile.MaxDiff)
			}

			// Log the values for verification
			t.Logf("%-25s class=%-18s init=%-10.2f min=%-10.2f max=%-10.0f target=%ds [%s]",
				tc.userAgent, class.String(), profile.InitialDiff, profile.MinDiff,
				profile.MaxDiff, profile.TargetShareTime, name)
		})
	}
}

// TestVerifyFallbackMaxDiff verifies that fallback code paths do NOT use
// the buggy 1 million or 1 trillion MaxDiff values.
func TestVerifyFallbackMaxDiff(t *testing.T) {
	router := NewSpiralRouterWithBlockTime(15)

	// Test GetProfile fallback with invalid class — falls through to MinerClassUnknown
	// which has MaxDiff=1M (wide range so vardiff ramps for any ASIC)
	t.Run("GetProfile_InvalidClass", func(t *testing.T) {
		fallback := router.GetProfile(MinerClass(999))
		if fallback.MaxDiff >= 1000000000000 {
			t.Errorf("GetProfile fallback MaxDiff = %.0f (>=1 trillion) - REGRESSION", fallback.MaxDiff)
		}
		if fallback.MaxDiff != 1000000 {
			t.Errorf("GetProfile fallback MaxDiff = %.0f, expected 1000000", fallback.MaxDiff)
		}
		t.Logf("GetProfile fallback: MaxDiff=%.0f ✓", fallback.MaxDiff)
	})

	// Test GetProfileForAlgorithm fallback with invalid class
	t.Run("GetProfileForAlgorithm_InvalidClass", func(t *testing.T) {
		fallback := router.GetProfileForAlgorithm(MinerClass(999), AlgorithmSHA256d)
		if fallback.MaxDiff >= 1000000000000 {
			t.Errorf("GetProfileForAlgorithm fallback MaxDiff = %.0f (>=1 trillion) - REGRESSION", fallback.MaxDiff)
		}
		if fallback.MaxDiff != 1000000 {
			t.Errorf("GetProfileForAlgorithm fallback MaxDiff = %.0f, expected 1000000", fallback.MaxDiff)
		}
		t.Logf("GetProfileForAlgorithm fallback: MaxDiff=%.0f ✓", fallback.MaxDiff)
	})
}

// TestVerifyDefaultProfilesValid verifies that DefaultProfiles map
// contains expected MaxDiff values within valid ranges.
func TestVerifyDefaultProfilesValid(t *testing.T) {
	for class, profile := range DefaultProfiles {
		t.Run(class.String(), func(t *testing.T) {
			// MaxDiff should never be 1 trillion (invalid engine default)
			if profile.MaxDiff >= 1000000000000 {
				t.Errorf("Class %s has MaxDiff>=1 trillion - REGRESSION: unexpected default value", class.String())
			}

			// MinDiff should equal InitialDiff (except lottery, unknown, farm_proxy, s19).
			// Lottery: MinDiff lower to support tiny ESP32 miners.
			// Unknown: MinDiff=100 (low floor), InitialDiff=500 — wide range for vardiff.
			// FarmProxy: InitialDiff is the optimistic starting point (500K), while MinDiff
			// is the floor that prevents vardiff dropping below a single-miner equivalent.
			// S19: the series spans ~70 TH/s (eco-mode / underclocked) to ~255 TH/s
			// (S19 XP Hyd) — a 3.6x spread. InitialDiff 24000 targets the ~100 TH/s
			// baseline, so a 70 TH/s unit needs 1.47s per share against a 1s target and
			// requires headroom below the start. MinDiff 16384 is that floor.
			if class != MinerClassLottery && class != MinerClassUnknown && class != MinerClassFarmProxy &&
				class != MinerClassS19 && profile.MinDiff != profile.InitialDiff {
				t.Errorf("Class %s: MinDiff (%.6f) != InitialDiff (%.6f)",
					class.String(), profile.MinDiff, profile.InitialDiff)
			}

			t.Logf("Class %-18s: init=%-10.2f min=%-10.2f max=%-10.0f ✓",
				class.String(), profile.InitialDiff, profile.MinDiff, profile.MaxDiff)
		})
	}
}

// TestVerifyScryptProfilesValid verifies Scrypt profiles contain expected values.
// NOTE: Unlike SHA-256d profiles, Scrypt profiles intentionally have MinDiff < InitialDiff.
// Scrypt miner classes span wider hashrate ranges (e.g., Low covers 185 MH/s Mini DOGE
// to 810 MH/s Mini DOGE III+), so vardiff needs room to converge downward.
func TestVerifyScryptProfilesValid(t *testing.T) {
	for class, profile := range ScryptProfiles {
		t.Run(class.String(), func(t *testing.T) {
			// MaxDiff should never be 1 million (invalid fallback value)
			if profile.MaxDiff == 1000000 {
				t.Errorf("Scrypt class %s has MaxDiff=1000000 - REGRESSION: unexpected fallback value", class.String())
			}

			// MinDiff must be <= InitialDiff (vardiff can go down but not below MinDiff)
			if profile.MinDiff > profile.InitialDiff {
				t.Errorf("Scrypt class %s: MinDiff (%.6f) > InitialDiff (%.6f) - invalid",
					class.String(), profile.MinDiff, profile.InitialDiff)
			}

			// InitialDiff must be <= MaxDiff
			if profile.InitialDiff > profile.MaxDiff {
				t.Errorf("Scrypt class %s: InitialDiff (%.6f) > MaxDiff (%.6f) - invalid",
					class.String(), profile.InitialDiff, profile.MaxDiff)
			}

			t.Logf("Scrypt %-18s: init=%-10.2f min=%-10.2f max=%-10.0f ✓",
				class.String(), profile.InitialDiff, profile.MinDiff, profile.MaxDiff)
		})
	}
}

// TestVerifyScryptMinerDetectionAndProfiles tests that Scrypt miners get correct
// Scrypt profiles when the router is in Scrypt mode. This validates the full path:
// user-agent → DetectMiner → class → Scrypt profile → correct difficulty.
//
// This test can be run WITHOUT physical miners to verify all Scrypt ASIC
// user-agents are properly detected and assigned correct Scrypt difficulties.
func TestVerifyScryptMinerDetectionAndProfiles(t *testing.T) {
	// Use LTC block time (150 seconds) — the primary Scrypt chain
	router := NewSpiralRouterWithBlockTime(150)
	router.SetAlgorithm(AlgorithmScrypt)

	// Scrypt miner firmware user-agents with expected class and difficulty ranges.
	//
	// VERIFIED firmware UA sources:
	//   - Antminer L-series (L3+, L7, L9): sends "cgminer/X.X.X" — NOT bmminer.
	//     Source: bitmaintech/cgminer-ltc, bitmaintech/ltc_frimware (builds cgminer)
	//   - Goldshell (Mini DOGE, LT, DG): sends "cgminer/X.X.X" (cgminer-based firmware)
	//     Source: goldshellminer/firmware, cgminer API compatibility
	//   - iBeLink (BM-L3): likely "cgminer/X.X.X" (cgminer compatible)
	//   - FutureBit Apollo LTC: sends "bfgminer/X.X.X"
	//     Source: jstefanop/bfgminer futurebit_driver branch
	//   - Vnish (L3+, L7): supports Scrypt, exact UA unverified (may send vnish or cgminer)
	//     Source: vnish.group/en/antminer-l3-l3-2, vnish-firmware.com L7 page
	//
	// NOT Scrypt-capable (DO NOT include):
	//   - bmminer: SHA-256d only (S9/T9/S19/S21). Source: bitmaintech/bmminer-mix
	//   - btminer: MicroBT makes NO Scrypt miners (all Whatsminer = SHA-256d)
	//   - Braiins OS: SHA-256d only (no L-series support). Source: braiins.com/os-firmware
	//   - ESP32/BitAxe/NerdQAxe: SHA-256d BM-series chips, cannot compute Scrypt
	//   - sgminer: GPU miner — pool does not support GPU mining
	testCases := []struct {
		userAgent     string
		expectedClass string
		minDiff       float64 // Minimum acceptable InitialDiff
		maxDiff       float64 // Maximum acceptable InitialDiff
		description   string  // Miner model and hashrate for logging
	}{
		// ================================================================
		// UNKNOWN CLASS — cgminer-based firmware (vardiff finds optimal)
		// Covers: Antminer L3+/L7/L9, Goldshell all models, iBeLink
		// All send "cgminer/X.X.X" — class=unknown, vardiff ramps to correct diff
		// ================================================================
		{"cgminer/4.10.1", "unknown", 5000, 15000, "cgminer (Antminer L3+/L7/L9, Goldshell, iBeLink)"},
		{"cgminer/4.12.0", "unknown", 5000, 15000, "cgminer (alternate version)"},

		// ================================================================
		// UNKNOWN CLASS — bfgminer (FutureBit Apollo LTC ~100-135 MH/s)
		// Source: jstefanop/bfgminer futurebit_driver branch [HIGH confidence]
		// ================================================================
		{"bfgminer/5.4.0", "unknown", 5000, 15000, "bfgminer (FutureBit Apollo LTC)"},

		// ================================================================
		// PRO CLASS — Vnish aftermarket firmware (L3+, L7 confirmed)
		// Exact UA unverified — may send "vnish" or modified "cgminer"
		// Source: vnish.group (Scrypt support confirmed, UA string LOW confidence)
		// ================================================================
		{"vnish/1.2.3", "pro", 200000, 400000, "Vnish (aftermarket firmware for L3+/L7)"},

		// ================================================================
		// UNKNOWN — completely unknown miner
		// ================================================================
		{"SomeRandomMiner/1.0", "unknown", 5000, 15000, "Unknown Scrypt miner"},
	}

	for _, tc := range testCases {
		t.Run(tc.userAgent, func(t *testing.T) {
			class, name := router.DetectMiner(tc.userAgent)
			profile := router.GetProfile(class)

			// 1. Verify class detection
			if class.String() != tc.expectedClass {
				t.Errorf("FAIL class: got %s, expected %s", class.String(), tc.expectedClass)
			}

			// 2. Verify we're getting SCRYPT profile (not SHA-256d)
			// Scrypt Pro InitialDiff should be ~290000, not ~25600 (SHA-256d)
			// This catches the bug where SHA-256d profiles were served to Scrypt miners
			if tc.expectedClass == "pro" && profile.InitialDiff < 100000 {
				t.Errorf("FAIL: got SHA-256d profile instead of Scrypt! InitialDiff=%.0f (expected >100000 for Scrypt Pro)",
					profile.InitialDiff)
			}

			// 3. Verify InitialDiff is in expected range (accounting for block-time scaling)
			if profile.InitialDiff < tc.minDiff || profile.InitialDiff > tc.maxDiff {
				t.Errorf("FAIL InitialDiff=%.2f outside range [%.0f, %.0f]",
					profile.InitialDiff, tc.minDiff, tc.maxDiff)
			}

			// 4. Verify MinDiff <= InitialDiff <= MaxDiff
			if profile.MinDiff > profile.InitialDiff {
				t.Errorf("FAIL MinDiff (%.2f) > InitialDiff (%.2f)", profile.MinDiff, profile.InitialDiff)
			}
			if profile.InitialDiff > profile.MaxDiff {
				t.Errorf("FAIL InitialDiff (%.2f) > MaxDiff (%.2f)", profile.InitialDiff, profile.MaxDiff)
			}

			// 5. Verify TargetShareTime is reasonable (1-60 seconds)
			if profile.TargetShareTime < 1 || profile.TargetShareTime > 60 {
				t.Errorf("FAIL TargetShareTime=%d outside [1, 60]", profile.TargetShareTime)
			}

			t.Logf("%-30s class=%-8s init=%-10.2f min=%-10.2f max=%-10.0f target=%ds [%s] (%s)",
				tc.userAgent, class.String(), profile.InitialDiff, profile.MinDiff,
				profile.MaxDiff, profile.TargetShareTime, name, tc.description)
		})
	}
}

// TestVerifyScryptVsSHA256dProfileSeparation verifies that the same miner class
// gets DIFFERENT difficulty profiles for SHA-256d vs Scrypt. This catches the
// original bug where Scrypt miners received SHA-256d profiles.
func TestVerifyScryptVsSHA256dProfileSeparation(t *testing.T) {
	router := NewSpiralRouterWithBlockTime(150) // LTC block time

	classes := []MinerClass{
		MinerClassUnknown, MinerClassLottery, MinerClassLow,
		MinerClassMid, MinerClassHigh, MinerClassPro,
	}

	for _, class := range classes {
		t.Run(class.String(), func(t *testing.T) {
			sha256Profile := router.GetProfileForAlgorithm(class, AlgorithmSHA256d)
			scryptProfile := router.GetProfileForAlgorithm(class, AlgorithmScrypt)

			// Scrypt difficulties should be MUCH higher than SHA-256d
			// because Scrypt hashes-per-diff-unit = 65536, SHA-256d = 2^32
			// Ratio: 2^32 / 65536 = 65536
			// So for the same hashrate, Scrypt diff should be ~65536x higher
			if class != MinerClassLottery {
				if scryptProfile.InitialDiff <= sha256Profile.InitialDiff {
					t.Errorf("Scrypt InitialDiff (%.2f) should be >> SHA-256d (%.2f) for class %s",
						scryptProfile.InitialDiff, sha256Profile.InitialDiff, class.String())
				}
			}

			t.Logf("%-10s SHA256d: init=%-10.2f  Scrypt: init=%-10.2f  ratio=%.1fx",
				class.String(), sha256Profile.InitialDiff, scryptProfile.InitialDiff,
				scryptProfile.InitialDiff/sha256Profile.InitialDiff)
		})
	}
}

// TestVerifyScryptAlgorithmSwitch verifies that SetAlgorithm correctly switches
// the active profiles from SHA-256d to Scrypt and back.
// Uses cgminer UA since that's what real Scrypt ASICs send (Antminer L-series,
// Goldshell, iBeLink all use cgminer-based firmware).
func TestVerifyScryptAlgorithmSwitch(t *testing.T) {
	router := NewSpiralRouterWithBlockTime(150)

	// cgminer is classified as Unknown — vardiff finds optimal for any ASIC
	// Default: SHA-256d
	sha256Diff := router.GetInitialDifficulty("cgminer/4.10.1")
	t.Logf("SHA-256d mode: cgminer InitialDiff = %.2f (Unknown class)", sha256Diff)

	// Switch to Scrypt
	router.SetAlgorithm(AlgorithmScrypt)
	scryptDiff := router.GetInitialDifficulty("cgminer/4.10.1")
	t.Logf("Scrypt mode:   cgminer InitialDiff = %.2f (Unknown class)", scryptDiff)

	// Scrypt diff should be higher than SHA-256d for the same class
	// SHA-256d Unknown: ~500, Scrypt Unknown: ~8000
	if scryptDiff <= sha256Diff {
		t.Errorf("After SetAlgorithm(Scrypt), diff (%.2f) should be > SHA-256d diff (%.2f)",
			scryptDiff, sha256Diff)
	}

	// Verify the ratio is in a reasonable range
	ratio := scryptDiff / sha256Diff
	if ratio < 5 || ratio > 100 {
		t.Errorf("Scrypt/SHA-256d ratio = %.1f, expected 5-100x (different diff scales)", ratio)
	}
	t.Logf("Ratio: %.1fx (Scrypt uses 65536 hashes/diff vs SHA-256d 2^32)", ratio)

	// Switch back to SHA-256d
	router.SetAlgorithm(AlgorithmSHA256d)
	backToSha := router.GetInitialDifficulty("cgminer/4.10.1")
	if backToSha != sha256Diff {
		t.Errorf("After switching back to SHA-256d, diff (%.2f) != original (%.2f)", backToSha, sha256Diff)
	}
	t.Logf("Back to SHA-256d: cgminer InitialDiff = %.2f ✓", backToSha)
}
