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

	// All user agents to test
	testCases := []struct {
		userAgent     string
		expectedClass string
	}{
		// Lottery class
		{"nerdminer/1.0", "lottery"},
		{"ESP32Miner/1.0", "lottery"},
		{"NMiner/1.0", "lottery"},

		// Low class (BitAxe, NMAxe)
		{"bitaxe/2.0", "low"},
		{"BitAxe Ultra", "low"},
		{"nmaxe/1.0", "low"},
		{"NMAxe Pro", "low"},

		// Mid class
		{"BitAxe Gamma", "mid"},
		{"nerdqaxe/1.0", "mid"},
		{"BitAxe Hex", "mid"},
		{"NerdOctaxe", "mid"},

		// High class
		{"Antminer S9", "high"},
		{"Antminer S15", "high"},

		// Pro class
		{"Antminer S19", "pro"},
		{"Antminer S21", "pro"},
		{"Whatsminer M50", "pro"},
		{"Whatsminer M60", "pro"},

		// Avalon classes
		{"Avalon Nano", "avalon_nano"},
		{"Avalon Nano 3S", "avalon_nano"},
		{"AvalonMiner 621", "avalon_legacy_low"},
		{"Avalon6", "avalon_legacy_low"},
		{"AvalonMiner 741", "avalon_legacy_mid"},
		{"AvalonMiner 841", "avalon_legacy_mid"},
		{"AvalonMiner 1047", "avalon_mid"},
		{"AvalonMiner 921", "avalon_mid"},
		{"AvalonMiner 1246", "avalon_high"},
		{"AvalonMiner 1346", "avalon_high"},
		{"AvalonMiner 1466", "avalon_pro"},
		{"AvalonMiner 1566", "avalon_pro"},
		{"Avalon Mini 3", "avalon_home"},
		{"Canaan Avalon Q", "avalon_home"},

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

			// 2. Verify MinDiff = InitialDiff (except lottery which can go lower)
			if tc.expectedClass != "lottery" && profile.MinDiff != profile.InitialDiff {
				t.Errorf("MinDiff (%.6f) != InitialDiff (%.6f) - will cause vardiff oscillation",
					profile.MinDiff, profile.InitialDiff)
			}

			// 3. Verify MaxDiff is NOT 1 million or 1 trillion (the bug)
			if profile.MaxDiff >= 1000000 {
				t.Errorf("MaxDiff = %.0f is TOO HIGH (>=1 million) - this was the bug!", profile.MaxDiff)
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

	// Test GetProfile fallback with invalid class
	t.Run("GetProfile_InvalidClass", func(t *testing.T) {
		fallback := router.GetProfile(MinerClass(999))
		if fallback.MaxDiff >= 1000000 {
			t.Errorf("GetProfile fallback MaxDiff = %.0f (>=1 million) - REGRESSION: expected < 1 million", fallback.MaxDiff)
		}
		if fallback.MaxDiff != 50000 {
			t.Errorf("GetProfile fallback MaxDiff = %.0f, expected 50000", fallback.MaxDiff)
		}
		t.Logf("GetProfile fallback: MaxDiff=%.0f ✓", fallback.MaxDiff)
	})

	// Test GetProfileForAlgorithm fallback with invalid class
	t.Run("GetProfileForAlgorithm_InvalidClass", func(t *testing.T) {
		fallback := router.GetProfileForAlgorithm(MinerClass(999), AlgorithmSHA256d)
		if fallback.MaxDiff >= 1000000 {
			t.Errorf("GetProfileForAlgorithm fallback MaxDiff = %.0f (>=1 million) - REGRESSION: expected < 1 million", fallback.MaxDiff)
		}
		if fallback.MaxDiff != 50000 {
			t.Errorf("GetProfileForAlgorithm fallback MaxDiff = %.0f, expected 50000", fallback.MaxDiff)
		}
		t.Logf("GetProfileForAlgorithm fallback: MaxDiff=%.0f ✓", fallback.MaxDiff)
	})
}

// TestVerifyDefaultProfilesValid verifies that DefaultProfiles map
// contains expected MaxDiff values within valid ranges.
func TestVerifyDefaultProfilesValid(t *testing.T) {
	for class, profile := range DefaultProfiles {
		t.Run(class.String(), func(t *testing.T) {
			// MaxDiff should never be 1 million (invalid fallback value)
			if profile.MaxDiff == 1000000 {
				t.Errorf("Class %s has MaxDiff=1000000 - REGRESSION: unexpected fallback value", class.String())
			}

			// MaxDiff should never be 1 trillion (invalid engine default)
			if profile.MaxDiff >= 1000000000000 {
				t.Errorf("Class %s has MaxDiff>=1 trillion - REGRESSION: unexpected default value", class.String())
			}

			// MinDiff should equal InitialDiff (except lottery)
			if class != MinerClassLottery && profile.MinDiff != profile.InitialDiff {
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

	// All known Scrypt miner user-agents with expected class and difficulty ranges.
	// Difficulty formula: D = hashrate × targetTime / 65536
	testCases := []struct {
		userAgent     string
		expectedClass string
		minDiff       float64 // Minimum acceptable InitialDiff
		maxDiff       float64 // Maximum acceptable InitialDiff
		description   string  // Miner model and hashrate for logging
	}{
		// ================================================================
		// ANTMINER L SERIES (Bitmain Scrypt ASICs)
		// ================================================================
		{"Antminer L9", "pro", 200000, 400000, "L9 ~16 GH/s"},
		{"antminer l9/1.0", "pro", 200000, 400000, "L9 ~16 GH/s"},
		{"Antminer L7", "pro", 200000, 400000, "L7 ~9.5 GH/s"},
		{"antminer l7/2.0", "pro", 200000, 400000, "L7 ~9.5 GH/s"},
		{"Antminer L3+", "low", 20000, 40000, "L3+ ~504 MH/s"},
		{"antminer l3/1.0", "low", 20000, 40000, "L3+ ~504 MH/s"},

		// ================================================================
		// GOLDSHELL (Scrypt ASICs — Mini DOGE, LT, DG series)
		// ================================================================
		{"Goldshell DG Max", "pro", 200000, 400000, "DG Max ~6.5 GH/s"},
		{"goldshell lt6", "high", 100000, 250000, "LT6 ~3.35 GH/s (High, not Pro — Scrypt Pro MinDiff > D_optimal)"},
		{"Goldshell LT5 Pro", "high", 100000, 250000, "LT5 Pro ~2.45 GH/s"},
		{"goldshell lt5", "high", 100000, 250000, "LT5 ~2 GH/s"},
		{"Goldshell LT Lite", "mid", 25000, 60000, "LT Lite ~1.6 GH/s"},
		{"Goldshell Mini DOGE III+", "low", 20000, 40000, "Mini DOGE III+ ~810 MH/s"},
		{"goldshell mini doge iii", "low", 20000, 40000, "Mini DOGE III ~700 MH/s"},
		{"Goldshell Mini DOGE II", "low", 20000, 40000, "Mini DOGE II ~420 MH/s"},
		{"goldshell mini doge pro", "low", 20000, 40000, "Mini DOGE Pro ~205 MH/s"},
		{"Goldshell Mini DOGE", "low", 20000, 40000, "Mini DOGE ~185 MH/s"},
		{"goldshell byte", "low", 20000, 40000, "BYTE ~230 MH/s"},
		{"goldshell", "mid", 25000, 60000, "Generic Goldshell"},

		// ================================================================
		// ELPHAPEX (Scrypt ASICs — DG series)
		// ================================================================
		{"Elphapex DG2+", "pro", 200000, 400000, "DG2+ ~20.5 GH/s"},
		{"elphapex dg2", "pro", 200000, 400000, "DG2 ~20.5 GH/s"},
		{"Elphapex DG2 Mini", "high", 100000, 250000, "DG2 Mini ~2.4 GH/s"},
		{"Elphapex DG Hydro", "pro", 200000, 400000, "DG Hydro ~20 GH/s"},
		{"Elphapex DG1+", "pro", 200000, 400000, "DG1+ ~14 GH/s"},
		{"elphapex dg1 lite", "pro", 200000, 400000, "DG1 Lite ~11 GH/s"},
		{"Elphapex DG1", "pro", 200000, 400000, "DG1 ~11 GH/s"},
		{"Elphapex DG Home", "high", 100000, 250000, "DG Home ~2.1 GH/s"},
		{"elphapex", "high", 100000, 250000, "Generic Elphapex"},

		// ================================================================
		// FLUMINER L SERIES (Scrypt)
		// ================================================================
		{"FluMiner L3", "pro", 200000, 400000, "L3 ~9.5 GH/s"},
		{"fluminer l1 pro", "pro", 200000, 400000, "L1 Pro ~6 GH/s"},
		{"FluMiner L1", "pro", 200000, 400000, "L1 ~5.3 GH/s"},

		// ================================================================
		// IBELINK (Scrypt)
		// ================================================================
		{"iBeLink BM-L3", "high", 100000, 250000, "BM-L3 ~3.2 GH/s"},

		// ================================================================
		// VOLCMINER, HAMMER MINER (Scrypt)
		// ================================================================
		{"volcminer", "pro", 200000, 400000, "Generic VolcMiner"},
		{"hammer miner", "low", 20000, 40000, "Hammer ~105 MH/s"},

		// ================================================================
		// GENERIC / UNKNOWN (should get Scrypt Unknown profile)
		// ================================================================
		{"cgminer/4.12.0", "mid", 25000, 60000, "cgminer (generic)"},
		{"bfgminer/5.5.0", "mid", 25000, 60000, "bfgminer (generic)"},
		{"SomeRandomMiner/1.0", "unknown", 5000, 15000, "Unknown miner"},

		// ================================================================
		// LOTTERY (ESP32 / tiny miners on Scrypt)
		// ================================================================
		{"nerdminer/1.0", "lottery", 0.05, 0.5, "ESP32 on Scrypt"},
		{"ESP32Miner/1.0", "lottery", 0.05, 0.5, "ESP32 on Scrypt"},
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
func TestVerifyScryptAlgorithmSwitch(t *testing.T) {
	router := NewSpiralRouterWithBlockTime(150)

	// Default: SHA-256d
	sha256Diff := router.GetInitialDifficulty("Antminer L7")
	t.Logf("SHA-256d mode: L7 InitialDiff = %.2f", sha256Diff)

	// Switch to Scrypt
	router.SetAlgorithm(AlgorithmScrypt)
	scryptDiff := router.GetInitialDifficulty("Antminer L7")
	t.Logf("Scrypt mode:   L7 InitialDiff = %.2f", scryptDiff)

	// Scrypt diff should be much higher (L7 is Pro class)
	// SHA-256d Pro: ~25600, Scrypt Pro: ~290000
	if scryptDiff <= sha256Diff {
		t.Errorf("After SetAlgorithm(Scrypt), L7 diff (%.2f) should be > SHA-256d diff (%.2f)",
			scryptDiff, sha256Diff)
	}

	// Verify the ratio is roughly in the right ballpark (not exactly 65536x due to
	// different TargetShareTimes between SHA-256d Pro (1s) and Scrypt Pro (2s))
	ratio := scryptDiff / sha256Diff
	if ratio < 5 || ratio > 100 {
		t.Errorf("Scrypt/SHA-256d ratio = %.1f, expected 5-100x (different target times)", ratio)
	}
	t.Logf("Ratio: %.1fx (accounts for different TargetShareTime)", ratio)

	// Switch back to SHA-256d
	router.SetAlgorithm(AlgorithmSHA256d)
	backToSha := router.GetInitialDifficulty("Antminer L7")
	if backToSha != sha256Diff {
		t.Errorf("After switching back to SHA-256d, L7 diff (%.2f) != original (%.2f)", backToSha, sha256Diff)
	}
	t.Logf("Back to SHA-256d: L7 InitialDiff = %.2f ✓", backToSha)
}
