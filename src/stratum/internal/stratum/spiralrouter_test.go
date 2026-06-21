// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package stratum

import (
	"testing"
)

func TestSpiralRouterDetection(t *testing.T) {
	router := NewSpiralRouter()

	tests := []struct {
		userAgent     string
		expectedClass MinerClass
		expectedName  string
	}{
		// ========================================================================
		// TIER 1: CONFIRMED — verified in manufacturer source code
		// ========================================================================

		// NerdMiner V2 — sends "NerdMinerV2/{version}" [CONFIRMED: BitMaker-hub/NerdMiner_v2]
		{"NerdMinerV2/2.6.0", MinerClassLottery, "NerdMiner V2"},
		{"nerdminerv2/1.0", MinerClassLottery, "NerdMiner V2"},

		// HAN_SOLOminer — sends "HAN_SOLOminer/{version}" [CONFIRMED]
		{"HAN_SOLOminer/1.0", MinerClassLottery, "HAN_SOLOminer"},
		{"han solo miner", MinerClassLottery, "HAN_SOLOminer"},

		// NerdQAxe++ — sends "NerdQAxe++/BM1370/{ver}" [CONFIRMED: WantClue/ESP-Miner-NerdQAxePlus]
		{"NerdQAxe++/BM1370/v1.0.36", MinerClassMid, "NerdQAxe++"},
		{"nerdqaxe++/BM1370/v2.0", MinerClassMid, "NerdQAxe++"},

		// NerdOctaxe — sends "NerdOCTAXE/BM1370/{ver}" [CONFIRMED]
		{"NerdOCTAXE/BM1370/v1.0", MinerClassMid, "NerdOctaxe"},
		{"nerdoctaxe/2.0", MinerClassMid, "NerdOctaxe"},
		{"octaxe/1.0", MinerClassMid, "NerdOctaxe"},

		// Generic NerdQAxe catch-all
		{"nerdqaxe/1.0", MinerClassMid, "NerdQAxe"},
		{"nerdaxe/1.0", MinerClassMid, "NerdQAxe"},

		// JingleMiner — sends "JingleMiner" (static) [CONFIRMED]
		{"JingleMiner", MinerClassMid, "JingleMiner"},
		{"jingleminer/1.0", MinerClassMid, "JingleMiner"},
		{"jingle miner/3.0", MinerClassMid, "Jingle Miner"},

		// Zyber — sends "Zyber8S/{ver}" or "Zyber8G/{ver}" [CONFIRMED: TinyChipHub/ESP-Miner-Zyber]
		{"Zyber8S/v1.2.0", MinerClassMid, "Zyber"},
		{"Zyber8G/v2.0.0", MinerClassMid, "Zyber"},
		{"zyber/3.0", MinerClassMid, "Zyber"},

		// BitAxe / ESP-Miner — sends "bitaxe/{ASIC}/{ver}" [CONFIRMED: bitaxeorg/ESP-Miner]
		{"bitaxe/BM1370/v2.4.5", MinerClassLow, "BitAxe (BM1370)"},
		{"bitaxe/BM1368/v2.3.1", MinerClassLow, "BitAxe (BM1368)"},
		{"bitaxe/BM1366/v2.9.31", MinerClassLow, "BitAxe (BM1366)"},  // NMAxe real UA
		{"bitaxe/BM1366/v2.0.5", MinerClassLow, "BitAxe (BM1366)"},   // BitAxe Ultra real UA
		{"bitaxe/BM1397/v1.0.0", MinerClassLow, "BitAxe (BM1397)"},
		{"bitaxe/unknown/v1.0", MinerClassLow, "BitAxe"},              // Generic bitaxe

		// Bitmain stock — sends "bmminer/{version}" [CONFIRMED: bitmaintech/bmminer-mix]
		{"bmminer/2.0.0", MinerClassPro, "Bitmain (bmminer)"},
		{"bmminer/1.0.0", MinerClassPro, "Bitmain (bmminer)"},

		// MicroBT stock — sends "btminer/{version}" [HIGH: pyasic]
		{"btminer/3.4.0", MinerClassPro, "MicroBT (btminer)"},
		{"btminer/3.1.0", MinerClassPro, "MicroBT (btminer)"},

		// Braiins Farm Proxy [CONFIRMED]
		{"braiins-farm-proxy/1.0.0", MinerClassFarmProxy, "Braiins Farm Proxy"},
		{"farm-proxy/1.0", MinerClassFarmProxy, "Farm Proxy"},
		{"farmproxy/2.0", MinerClassFarmProxy, "Farm Proxy"},

		// Braiins OS+ — sends "Braiins OS {version}" [CONFIRMED]
		{"Braiins OS 22.08", MinerClassPro, "Braiins OS"},
		{"braiins os+", MinerClassPro, "Braiins OS"},

		// ========================================================================
		// TIER 2: HIGH/MEDIUM confidence
		// ========================================================================
		{"vnish/1.0", MinerClassPro, "Vnish"},
		{"Vnish 2024.6", MinerClassPro, "Vnish"},
		{"luxos/2.0", MinerClassPro, "LuxOS"},
		{"LuxOS/2024.1", MinerClassPro, "LuxOS"},
		{"luxminer/1.0.0", MinerClassPro, "LuxOS"},

		// NiceHash [HIGH]
		{"nicehash/1.0", MinerClassHashMarketplace, "NiceHash"},
		{"excavator/1.4.4a_nvidia", MinerClassHashMarketplace, "NiceHash"},

		// ========================================================================
		// TIER 3: GENERIC mining software
		// ========================================================================
		// cgminer → MinerClassUnknown (covers Avalon, GekkoScience, Goldshell, etc.)
		{"cgminer/4.11.1", MinerClassUnknown, "cgminer"},   // Avalon Nano 3S real UA
		{"cgminer/4.13.1", MinerClassUnknown, "cgminer"},   // GekkoScience real UA
		{"cgminer/4.12.0", MinerClassUnknown, "cgminer"},

		// bfgminer → MinerClassUnknown (covers FutureBit Apollo, etc.)
		{"bfgminer/5.4.0", MinerClassUnknown, "bfgminer"},  // FutureBit Apollo real UA
		{"bfgminer/5.5.0", MinerClassUnknown, "bfgminer"},

		// General-purpose mining software
		{"sgminer/5.6.0", MinerClassLow, "sgminer"},
		{"cpuminer/2.5.2", MinerClassLow, "cpuminer"},
		{"ccminer/2.3.1", MinerClassLow, "ccminer"},

		// ========================================================================
		// TIER 4: LOTTERY miners
		// ========================================================================
		{"nminer/1.0", MinerClassLottery, "NMiner"},
		{"NMiner", MinerClassLottery, "NMiner"},
		{"bitmaker", MinerClassLottery, "BitMaker"},
		{"bitmaker/2.1", MinerClassLottery, "BitMaker"},
		{"ESP32 Miner V2", MinerClassLottery, "ESP32"},
		{"esp32-miner", MinerClassLottery, "ESP32"},
		{"my-esp32-rig", MinerClassLottery, "ESP32"},
		{"arduino miner/0.1", MinerClassLottery, "Arduino"},
		{"Arduino BTC Miner", MinerClassLottery, "Arduino"},
		{"sparkminer/1.0", MinerClassLottery, "SparkMiner"},
		{"lottery miner/1.0", MinerClassLottery, "LotteryMiner"},

		// ========================================================================
		// TIER 5: HASHRATE MARKETPLACES
		// ========================================================================
		{"miningrigrentals/2.0", MinerClassHashMarketplace, "MiningRigRentals"},
		{"mrr/1.0", MinerClassHashMarketplace, "MiningRigRentals"},
		{"cudo/3.0", MinerClassHashMarketplace, "Cudo Miner"},
		{"zergpool/1.0", MinerClassHashMarketplace, "Zergpool"},
		{"prohashing/1.0", MinerClassHashMarketplace, "Prohashing"},
		{"miningdutch/1.0", MinerClassHashMarketplace, "Mining Dutch"},
		{"mining.dutch/2.0", MinerClassHashMarketplace, "Mining Dutch"},
		{"zpool/1.0", MinerClassHashMarketplace, "Zpool"},
		{"woolypooly/1.0", MinerClassHashMarketplace, "WoolyPooly"},
		{"wooly/2.0", MinerClassHashMarketplace, "WoolyPooly"},
		{"herominers/1.0", MinerClassHashMarketplace, "HeroMiners"},
		{"hero/2.0", MinerClassHashMarketplace, "HeroMiners"},
		{"unmineable/1.0", MinerClassHashMarketplace, "unMineable"},

		// ========================================================================
		// UNKNOWN — should return Unknown class
		// ========================================================================
		{"random-miner/1.0", MinerClassUnknown, "Unknown"},
		{"totally-unknown-device", MinerClassUnknown, "Unknown"},
		{"", MinerClassUnknown, "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			class, name := router.DetectMiner(tt.userAgent)
			if class != tt.expectedClass {
				t.Errorf("DetectMiner(%q) class = %v, want %v", tt.userAgent, class, tt.expectedClass)
			}
			if name != tt.expectedName {
				t.Errorf("DetectMiner(%q) name = %q, want %q", tt.userAgent, name, tt.expectedName)
			}
		})
	}
}

func TestSpiralRouterDifficulties(t *testing.T) {
	router := NewSpiralRouter()

	tests := []struct {
		userAgent    string
		expectedDiff float64
	}{
		// ========================================================================
		// LOTTERY — InitialDiff 0.001, TargetShareTime 60s
		// ========================================================================
		{"NerdMinerV2/2.6.0", 0.001},
		{"HAN_SOLOminer/1.0", 0.001},
		{"nminer/1.0", 0.001},
		{"NMiner", 0.001},
		{"bitmaker", 0.001},
		{"bitmaker/2.1", 0.001},
		{"ESP32 Miner V2", 0.001},
		{"esp32-miner", 0.001},
		{"arduino miner/0.1", 0.001},
		{"sparkminer/1.0", 0.001},
		{"lottery miner/1.0", 0.001},

		// ========================================================================
		// LOW — InitialDiff 580, TargetShareTime 5s
		// ========================================================================
		{"bitaxe/BM1370/v2.4.5", 580},
		{"bitaxe/BM1368/v2.3.1", 580},
		{"bitaxe/BM1366/v2.9.31", 580},  // NMAxe real UA
		{"bitaxe/BM1397/v1.0.0", 580},
		{"bitaxe/unknown/v1.0", 580},
		{"sgminer/5.6.0", 580},
		{"cpuminer/2.5.2", 580},
		{"ccminer/2.3.1", 580},

		// ========================================================================
		// MID — InitialDiff 1165, TargetShareTime 1s
		// ========================================================================
		{"NerdQAxe++/BM1370/v1.0.36", 1165},
		{"nerdoctaxe/2.0", 1165},
		{"octaxe/1.0", 1165},
		{"nerdqaxe/1.0", 1165},
		{"nerdaxe/1.0", 1165},
		{"JingleMiner", 1165},
		{"jingle miner/3.0", 1165},
		{"Zyber8S/v1.2.0", 1165},
		{"Zyber8G/v2.0.0", 1165},

		// ========================================================================
		// PRO — InitialDiff 25600, TargetShareTime 1s
		// ========================================================================
		{"bmminer/2.0.0", 25600},
		{"btminer/3.4.0", 25600},
		{"Braiins OS 22.08", 25600},
		{"braiins os+", 25600},
		{"vnish/1.0", 25600},
		{"luxos/2.0", 25600},
		{"luxminer/1.0.0", 25600},

		// ========================================================================
		// HASH MARKETPLACE — InitialDiff 25600
		// ========================================================================
		{"nicehash/1.0", 25600},
		{"excavator/1.4.4a_nvidia", 25600},
		{"miningrigrentals/2.0", 25600},
		{"mrr/1.0", 25600},
		{"cudo/3.0", 25600},
		{"zergpool/1.0", 25600},
		{"prohashing/1.0", 25600},
		{"miningdutch/1.0", 25600},
		{"zpool/1.0", 25600},
		{"woolypooly/1.0", 25600},
		{"herominers/1.0", 25600},
		{"unmineable/1.0", 25600},

		// ========================================================================
		// UNKNOWN — InitialDiff 500
		// ========================================================================
		// cgminer/bfgminer now map to Unknown (was Mid)
		{"cgminer/4.11.1", 500},   // Avalon real UA → Unknown, vardiff adjusts
		{"cgminer/4.13.1", 500},   // GekkoScience real UA → Unknown
		{"bfgminer/5.4.0", 500},   // FutureBit real UA → Unknown

		{"random-miner/1.0", 500},
		{"totally-unknown-device", 500},
		{"", 500},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			diff := router.GetInitialDifficulty(tt.userAgent)
			if diff != tt.expectedDiff {
				t.Errorf("GetInitialDifficulty(%q) = %v, want %v", tt.userAgent, diff, tt.expectedDiff)
			}
		})
	}
}

func TestMinerClassString(t *testing.T) {
	tests := []struct {
		class    MinerClass
		expected string
	}{
		{MinerClassLottery, "lottery"},
		{MinerClassLow, "low"},
		{MinerClassMid, "mid"},
		{MinerClassHigh, "high"},
		{MinerClassPro, "pro"},
		{MinerClassUnknown, "unknown"},
		// Avalon-specific classes (still used by DeviceHints)
		{MinerClassAvalonNano, "avalon_nano"},
		{MinerClassAvalonLegacyLow, "avalon_legacy_low"},
		{MinerClassAvalonLegacyMid, "avalon_legacy_mid"},
		{MinerClassAvalonMid, "avalon_mid"},
		{MinerClassAvalonHigh, "avalon_high"},
		{MinerClassAvalonPro, "avalon_pro"},
		{MinerClassAvalonHome, "avalon_home"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.class.String(); got != tt.expected {
				t.Errorf("MinerClass.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestMinerClassVendor(t *testing.T) {
	tests := []struct {
		class          MinerClass
		expectedVendor string
		isAvalon       bool
	}{
		{MinerClassLottery, "generic", false},
		{MinerClassLow, "generic", false},
		{MinerClassMid, "generic", false},
		{MinerClassHigh, "generic", false},
		{MinerClassPro, "generic", false},
		{MinerClassUnknown, "generic", false},
		// Avalon classes should return "avalon" vendor
		{MinerClassAvalonNano, "avalon", true},
		{MinerClassAvalonLegacyLow, "avalon", true},
		{MinerClassAvalonLegacyMid, "avalon", true},
		{MinerClassAvalonMid, "avalon", true},
		{MinerClassAvalonHigh, "avalon", true},
		{MinerClassAvalonPro, "avalon", true},
		{MinerClassAvalonHome, "avalon", true},
	}

	for _, tt := range tests {
		t.Run(tt.class.String(), func(t *testing.T) {
			if got := tt.class.Vendor(); got != tt.expectedVendor {
				t.Errorf("MinerClass.Vendor() = %q, want %q", got, tt.expectedVendor)
			}
			if got := tt.class.IsAvalon(); got != tt.isAvalon {
				t.Errorf("MinerClass.IsAvalon() = %v, want %v", got, tt.isAvalon)
			}
		})
	}
}

func TestSpiralRouterCustomPattern(t *testing.T) {
	router := NewSpiralRouter()

	// Add custom pattern for a fictional miner
	err := router.AddPattern(`(?i)spiral.*miner`, MinerClassPro, "SpiralMiner")
	if err != nil {
		t.Fatalf("AddPattern failed: %v", err)
	}

	// Test that custom pattern takes priority
	class, name := router.DetectMiner("SpiralMiner Pro/3.0")
	if class != MinerClassPro {
		t.Errorf("Expected MinerClassPro, got %v", class)
	}
	if name != "SpiralMiner" {
		t.Errorf("Expected 'SpiralMiner', got %q", name)
	}
}

func TestSpiralRouterCustomProfile(t *testing.T) {
	router := NewSpiralRouter()

	// Customize lottery profile
	router.SetProfile(MinerClassLottery, MinerProfile{
		Class:           MinerClassLottery,
		InitialDiff:     0.0001, // Even lower
		MinDiff:         0.00001,
		MaxDiff:         10,
		TargetShareTime: 120,
	})

	// Test that custom profile is used
	diff := router.GetInitialDifficulty("NerdMinerV2/2.0")
	if diff != 0.0001 {
		t.Errorf("Expected 0.0001, got %v", diff)
	}
}

// Benchmark Spiral Router miner detection
func BenchmarkSpiralRouterDetection(b *testing.B) {
	router := NewSpiralRouter()
	userAgents := []string{
		"NerdMinerV2/2.0",
		"nminer/1.0",
		"bitaxe/BM1366/v2.9.31",
		"bmminer/2.0.0",
		"cgminer/4.12.0",
		"unknown-miner/1.0",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		router.DetectMiner(userAgents[i%len(userAgents)])
	}
}

func TestSpiralRouterWithDeviceHints(t *testing.T) {
	router := NewSpiralRouter()

	// Create a test device hints registry
	registry := NewDeviceHintsRegistry(0) // Default TTL
	router.SetDeviceHints(registry)

	// Test 1: BitAxe with device hint for NMAxe
	registry.Set(&DeviceHint{
		IP:          "192.168.1.100",
		DeviceModel: "NMAxe",
		ASICModel:   "BM1366",
		ASICCount:   1,
		HashrateGHs: 470,
		Class:       MinerClassLow,
	})

	// Should use device hint even though user-agent is generic bitaxe
	class, name := router.DetectMinerWithIP("bitaxe/BM1366/v2.9.31", "192.168.1.100")
	if class != MinerClassLow {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassLow", class)
	}
	if name != "NMAxe" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"NMAxe\"", name)
	}

	// Test 2: BitAxe with device hint for NerdQAxe++
	registry.Set(&DeviceHint{
		IP:          "192.168.1.101",
		DeviceModel: "NerdQAxe++",
		ASICModel:   "BM1370",
		ASICCount:   4,
		HashrateGHs: 5000,
		Class:       MinerClassMid,
	})

	class, name = router.DetectMinerWithIP("NerdQAxe++/BM1370/v1.0.36", "192.168.1.101")
	if class != MinerClassMid {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassMid", class)
	}
	if name != "NerdQAxe++" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"NerdQAxe++\"", name)
	}

	// Test 3: No device hint - falls back to user-agent detection
	class, name = router.DetectMinerWithIP("bitaxe/BM1366/v2.9.31", "192.168.1.200")
	if class != MinerClassLow {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassLow", class)
	}
	if name != "BitAxe (BM1366)" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"BitAxe (BM1366)\"", name)
	}

	// Test 4: No device hint but identifiable user-agent
	class, name = router.DetectMinerWithIP("bitaxe/BM1370/v2.4.5", "192.168.1.200")
	if class != MinerClassLow {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassLow", class)
	}
	if name != "BitAxe (BM1370)" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"BitAxe (BM1370)\"", name)
	}

	// Test 5: Device hint overrides identifiable user-agent
	registry.Set(&DeviceHint{
		IP:          "192.168.1.102",
		DeviceModel: "Antminer S19",
		ASICModel:   "BM1398",
		ASICCount:   76,
		HashrateGHs: 95000,
		Class:       MinerClassPro,
	})

	// Even if user-agent says cgminer, device hint wins
	class, name = router.DetectMinerWithIP("cgminer/4.12.0", "192.168.1.102")
	if class != MinerClassPro {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassPro", class)
	}
	if name != "Antminer S19" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"Antminer S19\"", name)
	}
}

func TestSpiralRouterWithDeviceHintsDifficulty(t *testing.T) {
	router := NewSpiralRouter()
	registry := NewDeviceHintsRegistry(0)
	router.SetDeviceHints(registry)

	// Add device hints for test IPs
	// HashrateGHs is used to calculate difficulty directly:
	// Diff = HashrateGHs * 1e9 * TargetShareTime / 2^32
	registry.Set(&DeviceHint{
		IP:          "10.0.0.1",
		DeviceModel: "NMAxe",
		ASICModel:   "BM1366",
		ASICCount:   1,
		HashrateGHs: 470,
		Class:       MinerClassLow,
	})

	registry.Set(&DeviceHint{
		IP:          "10.0.0.2",
		DeviceModel: "NerdQAxe++",
		ASICModel:   "BM1370",
		ASICCount:   4,
		HashrateGHs: 5000,
		Class:       MinerClassMid,
	})

	// Test hashrate-based difficulty calculation
	// Formula: Diff = HashrateGHs * 1e9 * TargetShareTime / 2^32
	// MinerClassLow: TargetShareTime = 5s, so 470 * 1e9 * 5 / 4294967296 ≈ 547.3
	// MinerClassMid: TargetShareTime = 1s, so 5000 * 1e9 * 1 / 4294967296 ≈ 1164.2

	t.Run("NMAxe with hashrate-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("bitaxe/BM1366/v2.9.31", "10.0.0.1")
		// Expected: 470 * 1e9 * 5 / 4294967296 ≈ 547.3
		if diff < 500 || diff > 600 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~547 (470 GH/s @ 5s target)", diff)
		}
	})

	t.Run("NerdQAxe++ with hashrate-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("NerdQAxe++/BM1370/v1.0.36", "10.0.0.2")
		// Expected: 5000 * 1e9 * 1 / 4294967296 ≈ 1164.2
		if diff < 1100 || diff > 1250 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~1164 (5000 GH/s @ 1s target)", diff)
		}
	})

	t.Run("Unknown IP falls back to user-agent detection", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("bitaxe/BM1366/v2.9.31", "10.0.0.99")
		// bitaxe/BM1366 matches MinerClassLow with InitialDiff = 580
		if diff != 580 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want 580 (class-based fallback)", diff)
		}
	})

	t.Run("With port number - should still work", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("bitaxe/BM1366/v2.9.31", "10.0.0.1:12345")
		// Same as 10.0.0.1 without port
		if diff < 500 || diff > 600 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~547 (470 GH/s @ 5s target)", diff)
		}
	})
}

func TestSpiralRouterHashrateBasedDifficulty(t *testing.T) {
	router := NewSpiralRouter()
	registry := NewDeviceHintsRegistry(0)
	router.SetDeviceHints(registry)

	// Test Avalon Nano 3S at 6.66 TH/s (6660 GH/s)
	// cgminer UA → MinerClassUnknown, but DeviceHint overrides with proper class
	registry.Set(&DeviceHint{
		IP:          "192.168.1.14",
		DeviceModel: "Avalon Nano 3S",
		ASICModel:   "unknown",
		ASICCount:   1,
		HashrateGHs: 6660,
		Class:       MinerClassAvalonNano,
	})

	t.Run("Avalon Nano 3S gets correct hashrate-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("cgminer/4.11.1", "192.168.1.14")
		// Expected: 6660 * 1e9 * 1 / 4294967296 ≈ 1550.5
		// MinerClassAvalonNano has TargetShareTime = 1s
		if diff < 1400 || diff > 1700 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~1550 (6660 GH/s @ 1s target)", diff)
		}
	})

	// Test very high hashrate miner (Antminer S19 Pro)
	registry.Set(&DeviceHint{
		IP:          "192.168.1.20",
		DeviceModel: "Antminer S19 Pro",
		ASICModel:   "BM1398",
		ASICCount:   76,
		HashrateGHs: 110000, // 110 TH/s
		Class:       MinerClassPro,
	})

	t.Run("S19 Pro gets correct hashrate-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("bmminer/2.0.0", "192.168.1.20")
		// Expected: 110000 * 1e9 * 1 / 4294967296 ≈ 25611.2
		// MinerClassPro has TargetShareTime = 1s
		if diff < 24000 || diff > 27500 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~25611 (110 TH/s @ 1s target)", diff)
		}
	})

	// Test device hint with zero hashrate (should fall back to class-based)
	registry.Set(&DeviceHint{
		IP:          "192.168.1.30",
		DeviceModel: "Unknown Device",
		HashrateGHs: 0, // No hashrate info
		Class:       MinerClassMid,
	})

	t.Run("Zero hashrate falls back to class-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("unknown/1.0", "192.168.1.30")
		// Should use MinerClassMid.InitialDiff = 1165
		if diff != 1165 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want 1165 (class-based fallback)", diff)
		}
	})
}

func TestDeviceHintClassification(t *testing.T) {
	registry := NewDeviceHintsRegistry(0)

	// Test automatic classification
	tests := []struct {
		hint          *DeviceHint
		expectedClass MinerClass
	}{
		// NMAxe - single BM1366
		{&DeviceHint{IP: "1.1.1.1", DeviceModel: "NMAxe", ASICModel: "BM1366", ASICCount: 1}, MinerClassLow},

		// NerdQAxe++ - 4x BM1370
		{&DeviceHint{IP: "1.1.1.2", DeviceModel: "NerdQAxe++", ASICModel: "BM1370", ASICCount: 4}, MinerClassMid},

		// BitAxe Ultra
		{&DeviceHint{IP: "1.1.1.3", DeviceModel: "BitAxe Ultra", ASICModel: "BM1366", ASICCount: 1}, MinerClassLow},

		// BitAxe Hex (6 chips)
		{&DeviceHint{IP: "1.1.1.4", DeviceModel: "BitAxe Hex", ASICModel: "BM1366", ASICCount: 6}, MinerClassMid},

		// Antminer S19
		{&DeviceHint{IP: "1.1.1.5", DeviceModel: "Antminer S19 Pro", ASICModel: "BM1398", ASICCount: 76}, MinerClassPro},

		// ESP32 Miner (lottery)
		{&DeviceHint{IP: "1.1.1.6", DeviceModel: "ESP32 Miner", ASICModel: "", ASICCount: 0}, MinerClassLottery},

		// Unknown device with low hashrate
		{&DeviceHint{IP: "1.1.1.7", DeviceModel: "UnknownDevice", HashrateGHs: 0.5}, MinerClassLottery},

		// Unknown device with mid hashrate
		{&DeviceHint{IP: "1.1.1.8", DeviceModel: "UnknownDevice", HashrateGHs: 5000}, MinerClassMid},

		// ========================================================================
		// AVALON/CANAAN DEVICE HINT CLASSIFICATION
		// ========================================================================
		// All Avalon devices MUST be classified to Avalon-specific classes.

		// Avalon Home Series
		{&DeviceHint{IP: "2.1.1.1", DeviceModel: "Avalon Mini 3", HashrateGHs: 37500}, MinerClassAvalonHome},
		{&DeviceHint{IP: "2.1.1.2", DeviceModel: "Avalon Q", HashrateGHs: 90000}, MinerClassAvalonHome},
		{&DeviceHint{IP: "2.1.1.3", DeviceModel: "Canaan Q 90TH", HashrateGHs: 90000}, MinerClassAvalonHome},

		// Avalon Nano Series
		{&DeviceHint{IP: "2.2.1.1", DeviceModel: "Avalon Nano 3S", HashrateGHs: 6000}, MinerClassAvalonNano},
		{&DeviceHint{IP: "2.2.1.2", DeviceModel: "Avalon Nano 3", HashrateGHs: 4000}, MinerClassAvalonNano},
		{&DeviceHint{IP: "2.2.1.3", DeviceModel: "Canaan Nano", HashrateGHs: 5000}, MinerClassAvalonNano},

		// Avalon Pro (A14, A15)
		{&DeviceHint{IP: "2.3.1.1", DeviceModel: "Avalon A1566", HashrateGHs: 185000}, MinerClassAvalonPro},
		{&DeviceHint{IP: "2.3.1.2", DeviceModel: "Avalon A15Pro", HashrateGHs: 215000}, MinerClassAvalonPro},
		{&DeviceHint{IP: "2.3.1.3", DeviceModel: "Avalon A1466", HashrateGHs: 165000}, MinerClassAvalonPro},

		// Avalon High (A12, A13)
		{&DeviceHint{IP: "2.4.1.1", DeviceModel: "Avalon A1366", HashrateGHs: 130000}, MinerClassAvalonHigh},
		{&DeviceHint{IP: "2.4.1.2", DeviceModel: "Avalon A1346", HashrateGHs: 110000}, MinerClassAvalonHigh},
		{&DeviceHint{IP: "2.4.1.3", DeviceModel: "Avalon A1246", HashrateGHs: 90000}, MinerClassAvalonHigh},

		// Avalon Mid (A9, A10, A11)
		{&DeviceHint{IP: "2.5.2.1", DeviceModel: "Avalon 1166", HashrateGHs: 81000}, MinerClassAvalonMid},
		{&DeviceHint{IP: "2.5.2.2", DeviceModel: "Avalon 1066", HashrateGHs: 50000}, MinerClassAvalonMid},
		{&DeviceHint{IP: "2.5.2.3", DeviceModel: "Avalon 921", HashrateGHs: 20000}, MinerClassAvalonMid},

		// Avalon Legacy Mid (A7, A8)
		{&DeviceHint{IP: "2.6.1.1", DeviceModel: "Avalon 851", HashrateGHs: 15000}, MinerClassAvalonLegacyMid},
		{&DeviceHint{IP: "2.6.1.2", DeviceModel: "Avalon 841", HashrateGHs: 13600}, MinerClassAvalonLegacyMid},
		{&DeviceHint{IP: "2.6.1.3", DeviceModel: "Avalon 741", HashrateGHs: 7300}, MinerClassAvalonLegacyMid},

		// Avalon Legacy Low (A3, A6)
		{&DeviceHint{IP: "2.7.1.1", DeviceModel: "Avalon 641", HashrateGHs: 3500}, MinerClassAvalonLegacyLow},
		{&DeviceHint{IP: "2.7.1.2", DeviceModel: "Avalon 6", HashrateGHs: 3500}, MinerClassAvalonLegacyLow},
		{&DeviceHint{IP: "2.7.1.3", DeviceModel: "Avalon 3", HashrateGHs: 800}, MinerClassAvalonLegacyLow},

		// Generic Avalon fallback with hashrate
		{&DeviceHint{IP: "2.8.1.1", DeviceModel: "Avalon Unknown", HashrateGHs: 3000}, MinerClassAvalonNano},
		{&DeviceHint{IP: "2.8.1.2", DeviceModel: "Canaan Unknown", HashrateGHs: 50000}, MinerClassAvalonMid},
		{&DeviceHint{IP: "2.8.1.3", DeviceModel: "Avalon Generic", HashrateGHs: 100000}, MinerClassAvalonHigh},
		{&DeviceHint{IP: "2.8.1.4", DeviceModel: "Canaan Generic", HashrateGHs: 180000}, MinerClassAvalonPro},

		// Unknown device with high hashrate
		{&DeviceHint{IP: "1.1.1.9", DeviceModel: "UnknownDevice", HashrateGHs: 100000}, MinerClassPro},
	}

	for _, tt := range tests {
		t.Run(tt.hint.DeviceModel, func(t *testing.T) {
			// Set with Class=Unknown to trigger auto-classification
			tt.hint.Class = MinerClassUnknown
			registry.Set(tt.hint)

			got := registry.Get(tt.hint.IP)
			if got == nil {
				t.Fatalf("Get(%q) returned nil", tt.hint.IP)
			}
			if got.Class != tt.expectedClass {
				t.Errorf("classifyDevice() = %v, want %v", got.Class, tt.expectedClass)
			}
		})
	}
}

// ========================================================================
// SCRYPT ALGORITHM TESTS
// ========================================================================

func TestScryptAlgorithmDifficulties(t *testing.T) {
	router := NewSpiralRouter()
	router.SetAlgorithm(AlgorithmScrypt)

	tests := []struct {
		userAgent    string
		expectedDiff float64
	}{
		// UNKNOWN — Scrypt InitialDiff 8000
		{"unknown-miner", 8000},
		{"", 8000},
		// cgminer/bfgminer → Unknown class → Scrypt Unknown diff
		{"cgminer/4.12.0", 8000},
		{"bfgminer/5.5.0", 8000},

		// LOTTERY — Scrypt InitialDiff 0.1
		{"NerdMinerV2/2.6.0", 0.1},
		{"HAN_SOLOminer/1.0", 0.1},
		{"nminer/1.0", 0.1},
		{"bitmaker", 0.1},
		{"ESP32 Miner V2", 0.1},
		{"arduino miner/0.1", 0.1},
		{"sparkminer/1.0", 0.1},

		// LOW — Scrypt InitialDiff 28000
		{"bitaxe/BM1366/v2.9.31", 28000},
		{"bitaxe/BM1370/v2.4.5", 28000},
		{"sgminer/5.6.0", 28000},
		{"cpuminer/2.5.2", 28000},
		{"ccminer/2.3.1", 28000},

		// MID — Scrypt InitialDiff 38000
		{"NerdQAxe++/BM1370/v1.0.36", 38000},
		{"nerdoctaxe/2.0", 38000},
		{"nerdqaxe/1.0", 38000},
		{"JingleMiner", 38000},
		{"Zyber8S/v1.2.0", 38000},

		// PRO — Scrypt InitialDiff 290000
		{"bmminer/2.0.0", 290000},
		{"btminer/3.1.0", 290000},
		{"braiins os+", 290000},
		{"vnish/1.0", 290000},
		{"luxos/2.0", 290000},

		// HASH MARKETPLACE — Scrypt InitialDiff 128000
		{"nicehash/1.0", 128000},
		{"miningrigrentals/2.0", 128000},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			diff := router.GetInitialDifficulty(tt.userAgent)
			if diff != tt.expectedDiff {
				t.Errorf("Scrypt GetInitialDifficulty(%q) = %v, want %v", tt.userAgent, diff, tt.expectedDiff)
			}
		})
	}
}

func TestGetInitialDifficultyForAlgorithm(t *testing.T) {
	router := NewSpiralRouter() // Defaults to SHA-256d

	tests := []struct {
		userAgent    string
		algorithm    Algorithm
		expectedDiff float64
	}{
		// Same device, SHA-256d vs Scrypt should give DIFFERENT difficulties
		{"bmminer/2.0.0", AlgorithmSHA256d, 25600},   // Pro SHA-256d
		{"bmminer/2.0.0", AlgorithmScrypt, 290000},    // Pro Scrypt

		{"bitaxe/BM1366/v2.9.31", AlgorithmSHA256d, 580},    // Low SHA-256d
		{"bitaxe/BM1366/v2.9.31", AlgorithmScrypt, 28000},   // Low Scrypt

		// cgminer → Unknown class
		{"cgminer/4.12", AlgorithmSHA256d, 500},    // Unknown SHA-256d
		{"cgminer/4.12", AlgorithmScrypt, 8000},     // Unknown Scrypt

		{"NerdMinerV2/2.0", AlgorithmSHA256d, 0.001},  // Lottery SHA-256d
		{"NerdMinerV2/2.0", AlgorithmScrypt, 0.1},      // Lottery Scrypt

		{"unknown-miner", AlgorithmSHA256d, 500},     // Unknown SHA-256d
		{"unknown-miner", AlgorithmScrypt, 8000},      // Unknown Scrypt
	}

	for _, tt := range tests {
		t.Run(tt.userAgent+"_"+string(tt.algorithm), func(t *testing.T) {
			diff := router.GetInitialDifficultyForAlgorithm(tt.userAgent, tt.algorithm)
			if diff != tt.expectedDiff {
				t.Errorf("GetInitialDifficultyForAlgorithm(%q, %q) = %v, want %v",
					tt.userAgent, tt.algorithm, diff, tt.expectedDiff)
			}
		})
	}
}

// ========================================================================
// BLOCK TIME SCALING TESTS
// ========================================================================

func TestBlockTimeScaling(t *testing.T) {
	tests := []struct {
		name          string
		blockTime     int
		class         MinerClass
		expectedTarget int
	}{
		// 600s blocks (BTC) — standard, no scaling needed for SHA-256d 1s targets
		{"600s_lottery", 600, MinerClassLottery, 60},
		{"600s_low", 600, MinerClassLow, 5},
		{"600s_mid", 600, MinerClassMid, 1},
		{"600s_high", 600, MinerClassHigh, 1},
		{"600s_pro", 600, MinerClassPro, 1},
		{"600s_avalon_legacy_low", 600, MinerClassAvalonLegacyLow, 2},

		// 15s blocks (DGB) — fast chain
		{"15s_lottery", 15, MinerClassLottery, 15},
		{"15s_low", 15, MinerClassLow, 3},
		{"15s_mid", 15, MinerClassMid, 1},
		{"15s_high", 15, MinerClassHigh, 1},
		{"15s_pro", 15, MinerClassPro, 1},
		{"15s_avalon_legacy_low", 15, MinerClassAvalonLegacyLow, 2},

		// 60s blocks (DOGE) — medium chain
		{"60s_lottery", 60, MinerClassLottery, 60},
		{"60s_low", 60, MinerClassLow, 5},
		{"60s_mid", 60, MinerClassMid, 1},
		{"60s_pro", 60, MinerClassPro, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewSpiralRouterWithBlockTime(tt.blockTime)
			profile := router.GetProfile(tt.class)
			if profile.TargetShareTime != tt.expectedTarget {
				t.Errorf("TargetShareTime for %v at %ds blocks = %d, want %d",
					tt.class, tt.blockTime, profile.TargetShareTime, tt.expectedTarget)
			}
		})
	}
}

func TestBlockTimeScalingDifficulty(t *testing.T) {
	// When TargetShareTime changes, difficulty should scale proportionally
	// Formula: NewDiff = OriginalDiff × (NewTargetTime / OriginalTargetTime)

	t.Run("15s blocks scale Low difficulty", func(t *testing.T) {
		router := NewSpiralRouterWithBlockTime(15)
		profile := router.GetProfile(MinerClassLow)
		// Original: 580 @ 5s, New target: 3s (15/5=3)
		// Expected: 580 × (3/5) = 348
		if profile.InitialDiff != 348 {
			t.Errorf("InitialDiff = %v, want 348", profile.InitialDiff)
		}
	})

	t.Run("600s blocks keep original difficulty", func(t *testing.T) {
		router := NewSpiralRouterWithBlockTime(600)
		profile := router.GetProfile(MinerClassMid)
		// Original: 1165 @ 1s, target stays at 1s
		if profile.InitialDiff != 1165 {
			t.Errorf("InitialDiff = %v, want 1165", profile.InitialDiff)
		}
	})
}

// TestProfileInvariants verifies that all profiles satisfy key invariants:
// 1. InitialDiff >= MinDiff (initial difficulty can't be below floor)
// 2. InitialDiff <= MaxDiff (initial difficulty can't exceed ceiling)
// 3. MinDiff > 0 for non-lottery classes (prevent zero difficulty)
// 4. TargetShareTime > 0 (must have positive share target)
func TestProfileInvariants(t *testing.T) {
	for _, profiles := range []map[MinerClass]MinerProfile{DefaultProfiles, ScryptProfiles} {
		for class, profile := range profiles {
			t.Run(class.String(), func(t *testing.T) {
				if profile.InitialDiff < profile.MinDiff {
					t.Errorf("InitialDiff (%v) < MinDiff (%v)", profile.InitialDiff, profile.MinDiff)
				}
				if profile.InitialDiff > profile.MaxDiff {
					t.Errorf("InitialDiff (%v) > MaxDiff (%v)", profile.InitialDiff, profile.MaxDiff)
				}
				if class != MinerClassLottery && profile.MinDiff <= 0 {
					t.Errorf("MinDiff (%v) must be > 0 for non-lottery class", profile.MinDiff)
				}
				if profile.TargetShareTime <= 0 {
					t.Errorf("TargetShareTime (%v) must be > 0", profile.TargetShareTime)
				}
			})
		}
	}
}

func TestSlowDiffApplier(t *testing.T) {
	router := NewSpiralRouter()

	// cgminer-based miners are slow to apply difficulty
	if !router.IsSlowDiffApplier("cgminer/4.11.1") {
		t.Error("cgminer should be detected as slow diff applier")
	}

	// Non-cgminer miners are not slow
	if router.IsSlowDiffApplier("bitaxe/BM1366/v2.9.31") {
		t.Error("bitaxe should not be detected as slow diff applier")
	}

	if router.IsSlowDiffApplier("bmminer/2.0.0") {
		t.Error("bmminer should not be detected as slow diff applier")
	}
}
