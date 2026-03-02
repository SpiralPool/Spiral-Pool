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
		// LOTTERY MINERS — ESP32-based, very low hashrate (~50-500 KH/s)
		// ========================================================================
		{"nerdminer/2.0.0", MinerClassLottery, "ESP32 Miner"},
		{"NerdMiner V2", MinerClassLottery, "ESP32 Miner"},
		{"HAN_SOLOminer/1.0", MinerClassLottery, "ESP32 Miner"},    // han.?solo pattern
		{"han solo miner", MinerClassLottery, "ESP32 Miner"},
		{"nminer/1.0", MinerClassLottery, "NMiner"},                 // ^nminer anchor
		{"NMiner", MinerClassLottery, "NMiner"},                     // \bnminer word boundary
		{"bitmaker", MinerClassLottery, "BitMaker"},
		{"bitmaker/2.1", MinerClassLottery, "BitMaker"},
		{"NerdESP32/1.0", MinerClassLottery, "NerdESP"},             // nerd.*esp pattern
		{"lottery miner/1.0", MinerClassLottery, "LotteryMiner"},    // lottery pattern
		{"ESP32 Miner V2", MinerClassLottery, "ESP32"},              // esp32 pattern
		{"esp32-miner", MinerClassLottery, "ESP32"},
		{"my-esp32-rig", MinerClassLottery, "ESP32"},
		{"arduino miner/0.1", MinerClassLottery, "Arduino"},         // arduino pattern
		{"Arduino BTC Miner", MinerClassLottery, "Arduino"},
		{"sparkminer/1.0", MinerClassLottery, "ESP32 Miner"},       // sparkminer pattern
		{"SparkMiner ESP32", MinerClassLottery, "ESP32"}, // esp32 pattern matches before sparkminer

		// ========================================================================
		// LOW-END — Single-chip BitAxe, USB miners (~400-600 GH/s)
		// ========================================================================
		{"bitaxe ultra/2.0", MinerClassLow, "BitAxe Ultra"},
		{"BitAxe Ultra 204", MinerClassLow, "BitAxe Ultra"},
		{"BitAxe Supra", MinerClassLow, "BitAxe Supra"},
		{"bitaxe supra/1.2", MinerClassLow, "BitAxe Supra"},
		{"nmaxe/1.0", MinerClassLow, "NMAxe"},
		{"NMAxe Pro", MinerClassLow, "NMAxe"},
		{"bitaxe/1.0", MinerClassLow, "BitAxe"},                    // Generic bitaxe fallback
		{"BitAxe 201", MinerClassLow, "BitAxe"},
		// ESP-Miner firmware (used by BitAxe, NerdQAxe++, etc.)
		{"ESP-Miner/2.9.21", MinerClassLow, "ESP-Miner"},
		{"esp-miner/1.0", MinerClassLow, "ESP-Miner"},
		// Common mining software — Low class
		{"cpuminer/2.5.1", MinerClassLow, "cpuminer"},
		{"sgminer/5.6.0", MinerClassLow, "sgminer"},                // sgminer pattern
		{"ccminer/2.3.1", MinerClassLow, "ccminer"},                // ccminer pattern
		// GekkoScience Compac F (single-chip USB miner)
		{"Compac F/1.0", MinerClassLow, "Compac F"},                // compac.*f pattern
		// FutureBit Moonlander 2 (Scrypt USB miner)
		{"FutureBit Moonlander 2", MinerClassLow, "Moonlander"},    // futurebit.*moonlander

		// ========================================================================
		// MID-RANGE — Multi-chip boards, NerdQAxe (~1-10 TH/s)
		// ========================================================================
		// NerdOctaxe / Octaxe (8-chip boards)
		{"NerdOctaxe Gamma/1.0", MinerClassMid, "NerdOctaxe Gamma"},  // nerdoctaxe pattern
		{"nerdoctaxe/2.0", MinerClassMid, "NerdOctaxe Gamma"},
		{"octaxe/1.0", MinerClassMid, "NerdOctaxe"},                  // octaxe pattern
		// NerdQAxe
		{"nerdqaxe/1.0", MinerClassMid, "NerdQAxe"},
		{"NerdQAxe++/3.0", MinerClassMid, "NerdQAxe"},               // nerdq?axe matches nerdqaxe
		{"nerdaxe/1.0", MinerClassMid, "NerdQAxe"},                   // nerdq?axe also matches nerdaxe
		// BitAxe Hex variants (6-chip boards)
		{"BitAxe Supra Hex 701", MinerClassMid, "BitAxe Supra Hex"}, // bitaxe.*supra.*hex
		{"supra hex/1.0", MinerClassMid, "Supra Hex"},               // supra.*hex
		{"BitAxe Ultra Hex/1.0", MinerClassMid, "BitAxe Ultra Hex"}, // bitaxe.*ultra.*hex
		{"ultra hex miner", MinerClassMid, "Ultra Hex"},              // ultra.*hex
		{"bitaxe hex/1.0", MinerClassMid, "BitAxe Hex"},             // Generic hex
		// BitAxe GT 801 (Gamma Turbo)
		{"BitAxe GT/1.0", MinerClassMid, "BitAxe GT"},               // bitaxe.*gt
		{"bitaxe 801", MinerClassMid, "BitAxe GT 801"},              // bitaxe.*801
		{"bitaxe turbo/1.0", MinerClassMid, "BitAxe GT"},            // bitaxe.*turbo
		{"Gamma Turbo/2.0", MinerClassMid, "BitAxe GT"},             // gamma.*turbo
		// BitAxe Gamma (generic)
		{"bitaxe gamma", MinerClassMid, "BitAxe Gamma"},
		// GekkoScience
		{"gekkoscience/compac", MinerClassMid, "GekkoScience"},
		{"GekkoScience R909", MinerClassMid, "GekkoScience"},
		// Compac / NewPac / R606 (GekkoScience line)
		{"compac/4.12", MinerClassMid, "Compac"},                    // compac (without "f")
		{"newpac/1.0", MinerClassMid, "NewPac"},                     // newpac
		{"R606/2.0", MinerClassMid, "R606"},                         // r606

		// ========================================================================
		// LUCKY MINER — Chinese BitAxe clones (SHA-256d + Scrypt)
		// ========================================================================
		// SHA-256d models
		{"Lucky Miner LV08/1.0", MinerClassMid, "Lucky Miner LV08"},   // 4.5 TH/s
		{"lucky lv08", MinerClassMid, "Lucky Miner LV08"},
		{"LV08/2.0", MinerClassMid, "LV08"},
		{"Lucky Miner LV07/1.0", MinerClassMid, "Lucky Miner LV07"},   // 1 TH/s
		{"lucky lv07", MinerClassMid, "Lucky Miner LV07"},
		{"LV07/1.0", MinerClassMid, "LV07"},
		{"Lucky Miner LV06/1.0", MinerClassLow, "Lucky Miner LV06"},   // 500 GH/s
		{"lucky lv06", MinerClassLow, "Lucky Miner LV06"},
		{"LV06/1.0", MinerClassLow, "LV06"},
		// Scrypt model
		{"Lucky Miner LG07/1.0", MinerClassLow, "Lucky Miner LG07"},   // 11 MH/s Scrypt
		{"lucky lg07", MinerClassLow, "Lucky Miner LG07"},
		{"LG07/1.0", MinerClassLow, "LG07"},
		// Generic Lucky Miner fallback
		{"lucky miner/2.0", MinerClassLow, "Lucky Miner"},

		// ========================================================================
		// JINGLE MINER — BM1370 chip, ESP-Miner based
		// ========================================================================
		{"Jingle Miner BTC Solo Pro/1.0", MinerClassMid, "Jingle Miner BTC Solo Pro"},  // 4.8 TH/s
		{"jingle pro/2.0", MinerClassMid, "Jingle Miner Pro"},
		{"BTC Solo Pro/1.0", MinerClassMid, "BTC Solo Pro"},
		{"Jingle Miner BTC Solo Lite/1.0", MinerClassMid, "Jingle Miner BTC Solo Lite"}, // 1.2 TH/s
		{"jingle lite/2.0", MinerClassMid, "Jingle Miner Lite"},
		{"BTC Solo Lite/1.0", MinerClassMid, "BTC Solo Lite"},
		{"jingle miner/3.0", MinerClassMid, "Jingle Miner"},          // Generic
		{"JingleMiner/1.0", MinerClassMid, "JingleMiner"},            // jingleminer now before jingle.*miner

		// ========================================================================
		// FUTUREBIT APOLLO — Desktop home miner with built-in full node
		// ========================================================================
		{"FutureBit Apollo II/2.0", MinerClassMid, "FutureBit Apollo II"},  // 6-9 TH/s
		{"Apollo II/1.5", MinerClassMid, "Apollo II"},
		{"FutureBit Apollo/1.0", MinerClassMid, "FutureBit Apollo"},        // 2-3.8 TH/s
		{"Apollo BTC/1.0", MinerClassMid, "Apollo BTC"},
		{"futurebit/3.0", MinerClassMid, "FutureBit"},                      // Generic fallback

		// ========================================================================
		// ZYBER MINERS / TINYCHIPHUB — Premium home miners
		// ========================================================================
		{"Zyber 8G/1.0", MinerClassMid, "Zyber 8G"},                  // 10+ TH/s
		{"zyber 8gp/2.0", MinerClassMid, "Zyber 8GP"},                // 8gp pattern now before 8g
		{"Zyber 8S/1.0", MinerClassMid, "Zyber 8S"},                  // 6.4 TH/s
		{"zyber/3.0", MinerClassMid, "Zyber"},                        // Generic
		{"TinyChipHub/1.0", MinerClassMid, "TinyChipHub"},            // tinychip pattern

		// ========================================================================
		// GENERIC MINING SOFTWARE — class based on typical usage
		// ========================================================================
		{"cgminer/4.12.0", MinerClassMid, "cgminer"},                 // Mid (Avalon support)
		{"bfgminer/5.5.0", MinerClassMid, "bfgminer"},                // Mid

		// ========================================================================
		// STOCK MANUFACTURER FIRMWARE
		// ========================================================================
		{"bmminer/1.0.0", MinerClassPro, "Bitmain Stock Firmware"},    // Bitmain (S9-S21)
		{"btminer/3.1.0", MinerClassPro, "MicroBT Stock Firmware"},    // MicroBT (M30S-M79)

		// ========================================================================
		// AVALON/CANAAN MINERS — EXHAUSTIVE TEST COVERAGE
		// ========================================================================
		// Tests cover ALL Avalon generations from legacy to modern.
		// Each model MUST map to its specific Avalon class.

		// ----------------------------------------------------------------
		// AVALON HOME SERIES (Mini 3, Q — consumer WiFi products)
		// ----------------------------------------------------------------
		{"Avalon Mini 3", MinerClassAvalonHome, "Avalon Mini 3"},
		{"canaan mini 3", MinerClassAvalonHome, "Canaan Mini 3"},
		{"Mini 3 Avalon", MinerClassAvalonHome, "Mini 3 (Avalon)"},    // Reversed order
		{"avalon q 90th", MinerClassAvalonHome, "Avalon Q"},
		{"canaan q miner", MinerClassAvalonHome, "Canaan Q"},
		{"avalon home miner", MinerClassAvalonHome, "Avalon Home"},
		{"canaan home miner", MinerClassAvalonHome, "Canaan Home"},    // canaan.*home

		// ----------------------------------------------------------------
		// AVALON NANO SERIES (3-7 TH/s consumer devices)
		// ----------------------------------------------------------------
		{"avalon nano 3s", MinerClassAvalonNano, "Avalon Nano 3S"},
		{"Avalon Nano 3S", MinerClassAvalonNano, "Avalon Nano 3S"},
		{"nano 3s", MinerClassAvalonNano, "Nano 3S"},
		{"Avalon Nano 3", MinerClassAvalonNano, "Avalon Nano 3"},
		{"avalon nano 2", MinerClassAvalonNano, "Avalon Nano 2"},
		{"avalon nano", MinerClassAvalonNano, "Avalon Nano"},
		{"Nano Avalon 3S", MinerClassAvalonNano, "Nano 3S"},           // nano.*3s matches before nano.*avalon
		{"Nano Avalon", MinerClassAvalonNano, "Nano (Avalon)"},        // nano.*avalon (no "3s" so this pattern fires)
		{"canaan nano 3s", MinerClassAvalonNano, "Canaan Nano 3S"},    // canaan.*nano.*3s now before nano.*3s
		{"canaan nano 3", MinerClassAvalonNano, "Canaan Nano 3"},      // canaan.*nano.*3\b
		{"canaan nano", MinerClassAvalonNano, "Canaan Nano"},
		{"Nano 3/1.0", MinerClassAvalonNano, "Nano 3"},               // nano[^a-z]*3

		// ----------------------------------------------------------------
		// AVALON PRO (A14, A15, A16 Series — 150-300 TH/s)
		// ----------------------------------------------------------------
		// A16 series (newest generation)
		{"A16 XP/1.0", MinerClassAvalonPro, "Avalon A16XP"},          // 300 TH/s
		{"avalon a16/2.0", MinerClassAvalonPro, "Avalon A16"},         // 282 TH/s
		// A15 variants
		{"A15 Pro", MinerClassAvalonPro, "Avalon A15Pro"},
		{"a15 xp", MinerClassAvalonPro, "Avalon A15XP"},
		{"A15 SE", MinerClassAvalonPro, "Avalon A15SE"},
		{"AvalonMiner 1566", MinerClassAvalonPro, "AvalonMiner A1566"},
		{"avalon a1566", MinerClassAvalonPro, "Avalon A1566"},
		{"avalon 1566", MinerClassAvalonPro, "Avalon A1566"},
		{"avalon a15", MinerClassAvalonPro, "Avalon A15"},
		// A14 series
		{"AvalonMiner 1466", MinerClassAvalonPro, "AvalonMiner A1466"},
		{"avalon a1466", MinerClassAvalonPro, "Avalon A1466"},
		{"avalon a14", MinerClassAvalonPro, "Avalon A14"},
		// Generic series patterns
		{"AvalonMiner 1600", MinerClassAvalonPro, "AvalonMiner 16xx"},  // avalonminer.*16[0-9]{2}
		{"AvalonMiner 1599", MinerClassAvalonPro, "AvalonMiner 15xx"},  // avalonminer.*15[0-9]{2}
		{"AvalonMiner 1499", MinerClassAvalonPro, "AvalonMiner 14xx"},  // avalonminer.*14[0-9]{2}
		{"avalon 1680", MinerClassAvalonPro, "Avalon 16xx"},            // avalon.*16[0-9]{2}
		{"avalon 1580", MinerClassAvalonPro, "Avalon 15xx"},            // avalon.*15[0-9]{2}
		{"avalon 1480", MinerClassAvalonPro, "Avalon 14xx"},            // avalon.*14[0-9]{2}

		// ----------------------------------------------------------------
		// AVALON HIGH (A12, A13 Series — 85-130 TH/s)
		// ----------------------------------------------------------------
		{"AvalonMiner 1366", MinerClassAvalonHigh, "AvalonMiner A1366"},
		{"AvalonMiner 1346", MinerClassAvalonHigh, "AvalonMiner A1346"},
		{"avalon a1366", MinerClassAvalonHigh, "Avalon A1366"},
		{"avalon 1346", MinerClassAvalonHigh, "Avalon A1346"},
		{"avalon a13", MinerClassAvalonHigh, "Avalon A13"},
		{"AvalonMiner 1246", MinerClassAvalonHigh, "AvalonMiner A1246"},
		{"avalon a1246", MinerClassAvalonHigh, "Avalon A1246"},
		{"avalon 1246", MinerClassAvalonHigh, "Avalon A1246"},
		{"avalon a12", MinerClassAvalonHigh, "Avalon A12"},
		// Generic series patterns
		{"AvalonMiner 1399", MinerClassAvalonHigh, "AvalonMiner 13xx"},  // avalonminer.*13[0-9]{2}
		{"AvalonMiner 1299", MinerClassAvalonHigh, "AvalonMiner 12xx"},  // avalonminer.*12[0-9]{2}
		{"avalon 1380", MinerClassAvalonHigh, "Avalon 13xx"},            // avalon.*13[0-9]{2}
		{"avalon 1280", MinerClassAvalonHigh, "Avalon 12xx"},            // avalon.*12[0-9]{2}

		// ----------------------------------------------------------------
		// AVALON MID (A9, A10, A11 Series — 18-81 TH/s)
		// ----------------------------------------------------------------
		{"AvalonMiner 1166", MinerClassAvalonMid, "AvalonMiner 1166"},
		{"AvalonMiner 1146", MinerClassAvalonMid, "AvalonMiner 1146"},
		{"AvalonMiner 1126", MinerClassAvalonMid, "AvalonMiner 1126"},
		{"avalon 1166 pro", MinerClassAvalonMid, "Avalon 1166"},
		{"avalon 1146", MinerClassAvalonMid, "Avalon 1146"},
		{"avalon 1126", MinerClassAvalonMid, "Avalon 1126"},
		{"AvalonMiner 1066", MinerClassAvalonMid, "AvalonMiner 1066"},
		{"AvalonMiner 1047", MinerClassAvalonMid, "AvalonMiner 1047"},
		{"AvalonMiner 1026", MinerClassAvalonMid, "AvalonMiner 1026"},
		{"avalon 1066", MinerClassAvalonMid, "Avalon 1066"},
		{"avalon 1047", MinerClassAvalonMid, "Avalon 1047"},
		{"avalon 1026", MinerClassAvalonMid, "Avalon 1026"},
		{"AvalonMiner 921", MinerClassAvalonMid, "AvalonMiner 921"},
		{"AvalonMiner 911", MinerClassAvalonMid, "AvalonMiner 911"},
		{"avalon 921", MinerClassAvalonMid, "Avalon 921"},
		{"avalon 911", MinerClassAvalonMid, "Avalon 911"},
		// Generic series patterns
		{"AvalonMiner 1199", MinerClassAvalonMid, "AvalonMiner 11xx"},   // avalonminer.*11[0-9]{2}
		{"AvalonMiner 1099", MinerClassAvalonMid, "AvalonMiner 10xx"},   // avalonminer.*10[0-9]{2}
		{"AvalonMiner 999", MinerClassAvalonMid, "AvalonMiner 9xx"},     // avalonminer.*9[0-9]{2}
		{"avalon 1180", MinerClassAvalonMid, "Avalon 11xx"},             // avalon.*11[0-9]{2}
		{"avalon 1090", MinerClassAvalonMid, "Avalon 10xx"},             // avalon.*10[0-9]{2}
		{"avalon 950", MinerClassAvalonMid, "Avalon 9xx"},               // avalon.*9[0-9]{2}

		// ----------------------------------------------------------------
		// AVALON LEGACY MID (A7, A8 Series — 7-15 TH/s)
		// ----------------------------------------------------------------
		{"AvalonMiner 851", MinerClassAvalonLegacyMid, "AvalonMiner 851"},
		{"AvalonMiner 841", MinerClassAvalonLegacyMid, "AvalonMiner 841"},
		{"AvalonMiner 821", MinerClassAvalonLegacyMid, "AvalonMiner 821"},
		{"avalon 851", MinerClassAvalonLegacyMid, "Avalon 851"},
		{"avalon 841", MinerClassAvalonLegacyMid, "Avalon 841"},
		{"avalon 821", MinerClassAvalonLegacyMid, "Avalon 821"},
		{"AvalonMiner 761", MinerClassAvalonLegacyMid, "AvalonMiner 761"},
		{"AvalonMiner 741", MinerClassAvalonLegacyMid, "AvalonMiner 741"},
		{"AvalonMiner 721", MinerClassAvalonLegacyMid, "AvalonMiner 721"},
		{"avalon 761", MinerClassAvalonLegacyMid, "Avalon 761"},
		{"avalon 741", MinerClassAvalonLegacyMid, "Avalon 741"},
		{"avalon 721", MinerClassAvalonLegacyMid, "Avalon 721"},
		// Generic series patterns
		{"AvalonMiner 899", MinerClassAvalonLegacyMid, "AvalonMiner 8xx"},  // avalonminer.*8[0-9]{2}
		{"AvalonMiner 799", MinerClassAvalonLegacyMid, "AvalonMiner 7xx"},  // avalonminer.*7[0-9]{2}
		{"avalon 870", MinerClassAvalonLegacyMid, "Avalon 8xx"},            // avalon.*8[0-9]{2}
		{"avalon 770", MinerClassAvalonLegacyMid, "Avalon 7xx"},            // avalon.*7[0-9]{2}

		// ----------------------------------------------------------------
		// AVALON LEGACY LOW (A3, A6 Series — 1-4 TH/s)
		// ----------------------------------------------------------------
		{"AvalonMiner 641", MinerClassAvalonLegacyLow, "AvalonMiner 641"},
		{"AvalonMiner 621", MinerClassAvalonLegacyLow, "AvalonMiner 621"},
		{"avalon 641", MinerClassAvalonLegacyLow, "Avalon 641"},
		{"avalon 621", MinerClassAvalonLegacyLow, "Avalon 621"},
		{"avalon 6", MinerClassAvalonLegacyLow, "Avalon 6"},
		{"AvalonMiner 3s", MinerClassAvalonLegacyLow, "AvalonMiner 3/3S"},
		{"avalon 3s", MinerClassAvalonLegacyLow, "Avalon 3S"},
		// Generic series patterns
		{"AvalonMiner 690", MinerClassAvalonLegacyLow, "AvalonMiner 6xx"},  // avalonminer.*6[0-9]{2}
		{"avalon 660", MinerClassAvalonLegacyLow, "Avalon 6xx"},            // avalon.*6[0-9]{2}

		// ----------------------------------------------------------------
		// AVALON GENERIC FALLBACKS (MinerClassAvalonMid)
		// ----------------------------------------------------------------
		{"avalonminer unknown", MinerClassAvalonMid, "AvalonMiner (generic)"},
		{"avalon miner xyz", MinerClassAvalonMid, "Avalon Miner (generic)"},  // avalon\s*miner (with space)
		{"avalon something", MinerClassAvalonMid, "Avalon (generic)"},
		{"canaan miner", MinerClassAvalonMid, "Canaan (generic)"},
		{"Canaan XYZ 2026", MinerClassAvalonMid, "Canaan (generic)"},

		// ========================================================================
		// HIGH-END — Older ASICs, small farms (10-80 TH/s)
		// ========================================================================
		// Bitmain Antminer S-series (SHA-256d)
		{"antminer s9/1.0", MinerClassHigh, "Antminer S9"},
		{"Antminer S9i", MinerClassHigh, "Antminer S9"},
		{"Antminer S9j", MinerClassHigh, "Antminer S9"},
		{"antminer s11/1.0", MinerClassHigh, "Antminer S11"},         // s11 pattern
		{"antminer s15/1.0", MinerClassHigh, "Antminer S15"},         // s15 pattern
		{"Antminer S17 Pro", MinerClassHigh, "Antminer S17"},         // s17 pattern
		// Bitmain Antminer T-series (SHA-256d)
		{"antminer t9+", MinerClassHigh, "Antminer T9"},              // t9 pattern
		{"Antminer T15/1.0", MinerClassHigh, "Antminer T15"},         // t15 pattern
		{"antminer t17/2.0", MinerClassHigh, "Antminer T17"},         // t17 pattern
		{"Antminer T19/1.0", MinerClassPro, "Antminer T19"},          // t19 → Pro (84 TH/s)
		// MicroBT Whatsminer (older models)
		{"whatsminer m20s", MinerClassHigh, "Whatsminer M10-M20"},
		{"Whatsminer M10/1.0", MinerClassHigh, "Whatsminer M10-M20"},
		// Innosilicon
		{"innosilicon t2", MinerClassHigh, "Innosilicon T1/T2"},
		{"Innosilicon T1/1.0", MinerClassHigh, "Innosilicon T1/T2"},
		{"innosilicon t3/1.0", MinerClassHigh, "Innosilicon T3"},      // t3 pattern
		// Ebang / Ebit
		{"ebang/1.0", MinerClassHigh, "Ebang"},
		{"Ebang E12+", MinerClassHigh, "Ebang"},
		{"Ebit E11++", MinerClassHigh, "Ebit E11/E12"},               // ebit.*e1[12]
		{"Ebit E12/1.0", MinerClassHigh, "Ebit E11/E12"},
		{"ebit/2.0", MinerClassHigh, "Ebit"},                         // Generic ebit

		// ========================================================================
		// PRO — Modern ASICs (100+ TH/s SHA-256d)
		// ========================================================================
		// Bitmain Antminer S-series (modern)
		{"antminer s19 pro", MinerClassPro, "Antminer S19"},
		{"Antminer S19 XP Hyd", MinerClassPro, "Antminer S19"},
		{"Antminer S21", MinerClassPro, "Antminer S21"},
		{"Antminer S21 XP", MinerClassPro, "Antminer S21"},
		{"Antminer T21/1.0", MinerClassPro, "Antminer T21"},          // t21 pattern
		// MicroBT Whatsminer M30S series (86-112 TH/s)
		{"Whatsminer M30S++/1.0", MinerClassPro, "Whatsminer M30S++"},  // 112 TH/s
		{"whatsminer m30s+/2.0", MinerClassPro, "Whatsminer M30S+"},    // 100 TH/s
		{"Whatsminer M30S/1.0", MinerClassPro, "Whatsminer M30S"},      // 86 TH/s
		// MicroBT Whatsminer M50+ series
		{"whatsminer m50s", MinerClassPro, "Whatsminer M50+"},
		{"Whatsminer M60S/1.0", MinerClassPro, "Whatsminer M60"},       // m60 pattern
		{"whatsminer m63s/1.0", MinerClassPro, "Whatsminer M63"},       // m63 pattern (hydro)
		{"whatsminer m66s/1.0", MinerClassPro, "Whatsminer M66"},       // m66 pattern (immersion)
		// MicroBT Whatsminer M70 series (Dec 2025 — newest gen)
		{"Whatsminer M79S/1.0", MinerClassPro, "Whatsminer M79"},       // 870-1040 TH/s hydro
		{"Whatsminer M78/1.0", MinerClassPro, "Whatsminer M78"},        // 440-522 TH/s immersion
		{"Whatsminer M76S+/1.0", MinerClassPro, "Whatsminer M76"},      // 336-440 TH/s
		{"Whatsminer M73S+/1.0", MinerClassPro, "Whatsminer M73"},      // 470-600 TH/s hydro
		{"Whatsminer M72S/1.0", MinerClassPro, "Whatsminer M72"},       // 246-300 TH/s air OC
		{"Whatsminer M70S/1.0", MinerClassPro, "Whatsminer M70"},       // 220-258 TH/s air
		// MicroBT generic fallbacks
		{"whatsminer m90s/1.0", MinerClassPro, "Whatsminer M50+"},      // m[5-9]0 pattern
		{"whatsminer m40s/1.0", MinerClassPro, "Whatsminer M30S+"},     // m[3-4]0s pattern
		// Third-party firmware
		{"braiins os+", MinerClassPro, "Braiins"},
		{"Braiins OS 22.08", MinerClassPro, "Braiins"},
		{"vnish/1.0", MinerClassPro, "Vnish"},
		{"Vnish 2024.6", MinerClassPro, "Vnish"},
		{"luxos/2.0", MinerClassPro, "LuxOS"},
		{"LuxOS/2024.1", MinerClassPro, "LuxOS"},
		{"luxminer/1.0.0", MinerClassPro, "LuxOS"},               // LuxOS API identifies as LUXminer

		// ========================================================================
		// SEALMINER / BITDEER — SEAL chip architecture (SHA-256d)
		// ========================================================================
		// A3 series (newest gen)
		{"Sealminer A3 Pro Hydro/1.0", MinerClassPro, "Sealminer A3 Pro Hydro"},  // 660 TH/s
		{"sealminer a3 hydro/1.0", MinerClassPro, "Sealminer A3 Hydro"},           // 500 TH/s
		{"Sealminer A3 Pro/1.0", MinerClassPro, "Sealminer A3 Pro"},               // 310 TH/s
		{"sealminer a3/1.0", MinerClassPro, "Sealminer A3"},                        // 260 TH/s
		// A2 series
		{"Sealminer A2 Pro Hydro/1.0", MinerClassPro, "Sealminer A2 Pro Hydro"},  // 500 TH/s
		{"sealminer a2 hydro/1.0", MinerClassPro, "Sealminer A2 Hydro"},           // 446 TH/s
		{"Sealminer A2 Pro/1.0", MinerClassPro, "Sealminer A2 Pro"},               // 255 TH/s
		{"sealminer a2/1.0", MinerClassPro, "Sealminer A2"},                        // 226 TH/s
		// Generic
		{"sealminer/3.0", MinerClassPro, "Sealminer"},
		{"bitdeer/1.0", MinerClassPro, "Bitdeer"},
		{"Bitdeer Miner/2.0", MinerClassPro, "Bitdeer"},

		// ========================================================================
		// AURADINE / TERAFLUX — US-based, FluxVision firmware (SHA-256d)
		// ========================================================================
		{"Teraflux AH3880/1.0", MinerClassPro, "Auradine Teraflux AH3880"},  // 600 TH/s hydro
		{"teraflux ai3680/1.0", MinerClassPro, "Auradine Teraflux AI3680"},   // 375 TH/s
		{"Teraflux AT2880/1.0", MinerClassPro, "Auradine Teraflux AT2880"},   // 260 TH/s
		{"teraflux/2.0", MinerClassPro, "Auradine Teraflux"},                 // Generic teraflux
		{"auradine/1.0", MinerClassPro, "Auradine"},                          // Generic auradine

		// ========================================================================
		// GOLDSHELL — Scrypt ASIC lineup (LTC/DOGE)
		// ========================================================================
		{"Goldshell DG Max/1.0", MinerClassPro, "Goldshell DG Max"},           // 6.5 GH/s
		{"Goldshell LT6/1.0", MinerClassHigh, "Goldshell LT6"},               // 3.35 GH/s — High, not Pro (Scrypt MinDiff issue)
		{"LT6 Goldshell/1.0", MinerClassHigh, "Goldshell LT6"},               // Reversed
		{"Goldshell LT5 Pro/1.0", MinerClassHigh, "Goldshell LT5"},           // 2-2.5 GH/s
		{"LT5 Goldshell/1.0", MinerClassHigh, "Goldshell LT5"},               // Reversed
		{"Goldshell LT Lite/1.0", MinerClassMid, "Goldshell LT Lite"},        // 1.62 GH/s
		// Mini DOGE series (specific models BEFORE generic)
		{"Goldshell Mini DOGE III+/1.0", MinerClassLow, "Goldshell Mini DOGE III+"}, // pattern now matches both "+" and "plus"
		{"Goldshell Mini DOGE III/1.0", MinerClassLow, "Goldshell Mini DOGE III"},     // 700 MH/s
		{"Goldshell Mini DOGE II/1.0", MinerClassLow, "Goldshell Mini DOGE II"},       // 420 MH/s
		{"Goldshell Mini DOGE Pro/1.0", MinerClassLow, "Goldshell Mini DOGE Pro"},     // 205 MH/s
		{"Goldshell Mini DOGE/1.0", MinerClassLow, "Goldshell Mini DOGE"},             // 185 MH/s
		{"Mini DOGE/1.0", MinerClassLow, "Mini DOGE"},                                 // Without "Goldshell"
		{"Goldshell BYTE DG Card", MinerClassLow, "Goldshell BYTE"},                   // 80 MH/s modular
		// Generic Goldshell
		{"goldshell/2.0", MinerClassMid, "Goldshell"},

		// ========================================================================
		// ANTMINER L SERIES — Scrypt (LTC/DOGE)
		// ========================================================================
		{"Antminer L11 Hydro/1.0", MinerClassPro, "Antminer L11 Hydro"},  // 33-35 GH/s
		{"antminer l11 pro/1.0", MinerClassPro, "Antminer L11 Pro"},      // 21 GH/s
		{"Antminer L11/1.0", MinerClassPro, "Antminer L11"},              // 20 GH/s
		{"antminer l9/1.0", MinerClassPro, "Antminer L9"},                // 16-17 GH/s
		{"Antminer L7/1.0", MinerClassPro, "Antminer L7"},                // 9.5 GH/s
		{"antminer l3+/1.0", MinerClassLow, "Antminer L3+"},              // 504 MH/s (older)

		// ========================================================================
		// ELPHAPEX — Scrypt (LTC/DOGE)
		// ========================================================================
		// DG2 series (newest gen)
		{"Elphapex DG2+/1.0", MinerClassPro, "Elphapex DG2+"},           // 20.5 GH/s
		{"DG2+/1.0", MinerClassPro, "DG2+"},                              // Short form
		{"Elphapex DG2 Mini/1.0", MinerClassHigh, "Elphapex DG2 Mini"},  // 2.4 GH/s home
		{"Elphapex DG2/1.0", MinerClassPro, "Elphapex DG2"},             // 20.5 GH/s
		// DG Hydro
		{"Elphapex DG Hydro 1/1.0", MinerClassPro, "Elphapex DG Hydro"}, // 20 GH/s water
		{"DG Hydro/1.0", MinerClassPro, "DG Hydro"},
		// DG1 series
		{"Elphapex DG1+/1.0", MinerClassPro, "Elphapex DG1+"},           // 14 GH/s
		{"DG1+/1.0", MinerClassPro, "DG1+"},
		{"Elphapex DG1 Lite/1.0", MinerClassPro, "Elphapex DG1 Lite"},   // 11 GH/s budget
		{"Elphapex DG1/1.0", MinerClassPro, "Elphapex DG1"},             // 11 GH/s
		// DG Home
		{"Elphapex DG Home 1/1.0", MinerClassHigh, "Elphapex DG Home"},  // 2.1 GH/s
		{"DG Home/1.0", MinerClassHigh, "DG Home"},
		// Generic
		{"elphapex/2.0", MinerClassHigh, "Elphapex"},

		// ========================================================================
		// VOLCMINER — Scrypt (LTC/DOGE)
		// ========================================================================
		{"VolcMiner D1 Hydro/1.0", MinerClassPro, "VolcMiner D1 Hydro"},  // 30.4 GH/s
		{"volcminer d1 pro/1.0", MinerClassPro, "VolcMiner D1 Pro"},      // 18 GH/s
		{"VolcMiner D3/1.0", MinerClassPro, "VolcMiner D3"},              // 20 GH/s
		{"volcminer d1 lite/1.0", MinerClassPro, "VolcMiner D1 Lite"},    // 14 GH/s
		{"VolcMiner D1 Mini/1.0", MinerClassHigh, "VolcMiner D1 Mini"},   // 2.2 GH/s
		{"volcminer d1/1.0", MinerClassPro, "VolcMiner D1"},              // 15-18.5 GH/s
		{"volcminer/2.0", MinerClassPro, "VolcMiner"},                    // Generic

		// ========================================================================
		// FLUMINER — SHA-256d + Scrypt
		// ========================================================================
		// T-series (SHA-256d)
		{"FluMiner T3/1.0", MinerClassPro, "FluMiner T3"},                // 115 TH/s
		{"flu miner t3/2.0", MinerClassPro, "FluMiner T3"},               // With space
		// L-series (Scrypt)
		{"FluMiner L3/1.0", MinerClassPro, "FluMiner L3"},                // 9.5 GH/s
		{"flu miner l3/2.0", MinerClassPro, "FluMiner L3"},
		{"FluMiner L2/1.0", MinerClassMid, "FluMiner L2"},                // 1-1.2 GH/s
		{"flu miner l2/2.0", MinerClassMid, "FluMiner L2"},
		{"FluMiner L1 Pro/1.0", MinerClassPro, "FluMiner L1 Pro"},        // 6 GH/s
		{"flu miner l1 pro/2.0", MinerClassPro, "FluMiner L1 Pro"},
		{"FluMiner L1/1.0", MinerClassPro, "FluMiner L1"},                // 5.3 GH/s
		{"flu miner l1/2.0", MinerClassPro, "FluMiner L1"},
		// Generic
		{"fluminer/3.0", MinerClassPro, "FluMiner"},
		{"flu miner/1.0", MinerClassMid, "FluMiner"},                     // Generic with space → Mid

		// ========================================================================
		// IBELINK — Scrypt (LTC/DOGE)
		// ========================================================================
		{"iBeLink BM-L3/1.0", MinerClassHigh, "iBeLink BM-L3"},           // 3.2 GH/s
		{"ibelink/2.0", MinerClassHigh, "iBeLink"},                        // Generic

		// ========================================================================
		// HAMMER MINER / PLEBSOURCE — Entry-level Scrypt ASIC
		// ========================================================================
		{"Hammer Miner/1.0", MinerClassLow, "Hammer Miner"},
		{"hammerminer/2.0", MinerClassLow, "Hammer Miner"},
		{"PlebSource Hammer/1.0", MinerClassLow, "PlebSource Hammer"},
		{"plebsource/2.0", MinerClassLow, "PlebSource"},
		{"Doge Digger/1.0", MinerClassLow, "Doge Digger"},

		// ========================================================================
		// HASHRATE RENTAL SERVICES
		// ========================================================================
		// USER-AGENT DETECTION TEST: Vendor names required for testing miner identification.
		// These are technical identifiers from the miner's user-agent string, not endorsements.
		// Users are solely responsible for any fees/costs with third-party services.
		{"nicehash/1.0", MinerClassPro, "NiceHash"},
		{"excavator/1.4.4a_nvidia", MinerClassPro, "NiceHash"},    // NiceHash GPU miner real UA
		{"miningrigrentals/2.0", MinerClassPro, "MiningRigRentals"},
		{"mrr/1.0", MinerClassPro, "MiningRigRentals"},
		{"cudo/3.0", MinerClassPro, "Cudo Miner"},
		{"zergpool/1.0", MinerClassPro, "Zergpool"},
		{"prohashing/1.0", MinerClassPro, "Prohashing"},
		{"miningdutch/1.0", MinerClassPro, "Mining Dutch"},
		{"mining.dutch/2.0", MinerClassPro, "Mining Dutch"},           // Alternate form
		{"zpool/1.0", MinerClassPro, "Zpool"},
		{"woolypooly/1.0", MinerClassPro, "WoolyPooly"},
		{"wooly/2.0", MinerClassPro, "WoolyPooly"},                   // Short form
		{"herominers/1.0", MinerClassPro, "HeroMiners"},
		{"hero/2.0", MinerClassPro, "HeroMiners"},                    // Short form
		{"unmineable/1.0", MinerClassPro, "unMineable"},

		// ========================================================================
		// CONFIRMED REAL-WORLD USER AGENTS (from firmware source code)
		// These are the ACTUAL strings sent by stock firmware.
		// ========================================================================
		// BitAxe/ESP-Miner: sends "bitaxe/{chip}/{version}" (confirmed from ESP-Miner source)
		{"bitaxe/BM1370/v2.4.5", MinerClassLow, "BitAxe"},          // BitAxe Gamma/Supra stock UA
		{"bitaxe/BM1366/v2.0.5", MinerClassLow, "BitAxe"},          // BitAxe Ultra / Lucky Miner stock UA
		{"bitaxe/BM1368/v2.3.0", MinerClassLow, "BitAxe"},          // BitAxe 201 stock UA
		// NerdMiner: sends "NerdMinerV2/{version}" (confirmed from NerdMiner_v2 source)
		{"NerdMinerV2/2.6.0", MinerClassLottery, "NerdMiner"},
		// Bitmain stock: sends "bmminer/{version}" (confirmed from bmminer source)
		{"bmminer/2.0.0", MinerClassPro, "Bitmain Stock Firmware"}, // S19/S21 stock
		// MicroBT stock: sends "btminer/{version}" (high confidence from pyasic)
		{"btminer/3.4.0", MinerClassPro, "MicroBT Stock Firmware"}, // M30S/M50/M60 stock
		// Avalon/Canaan: sends "cgminer/{version}" (confirmed from Canaan cgminer fork)
		{"cgminer/4.11.1", MinerClassMid, "cgminer"},               // Avalon Nano/A-series stock
		// GekkoScience: sends "cgminer/{version}" (confirmed from cgminer-gekko fork)
		{"cgminer/4.13.1", MinerClassMid, "cgminer"},               // GekkoScience Compac/NewPac stock

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
		// 1:1 MIRROR OF TestSpiralRouterDetection — EVERY detected UA verified
		// ========================================================================
		// If detection assigns a class, difficulty MUST match that class's profile.
		// Formula: Diff = Hashrate × TargetShareTime / 2^32

		// ========================================================================
		// LOTTERY — InitialDiff 0.001, TargetShareTime 60s
		// ========================================================================
		{"nerdminer/2.0.0", 0.001},
		{"NerdMiner V2", 0.001},
		{"HAN SOLOminer/1.0", 0.001},
		{"han solo miner", 0.001},
		{"nminer/1.0", 0.001},
		{"NMiner", 0.001},
		{"bitmaker", 0.001},
		{"bitmaker/2.1", 0.001},
		{"NerdESP32/1.0", 0.001},
		{"lottery miner/1.0", 0.001},
		{"ESP32 Miner V2", 0.001},
		{"esp32-miner", 0.001},
		{"my-esp32-rig", 0.001},
		{"arduino miner/0.1", 0.001},
		{"Arduino BTC Miner", 0.001},
		{"sparkminer/1.0", 0.001},
		{"SparkMiner ESP32", 0.001},

		// ========================================================================
		// LOW — InitialDiff 580, TargetShareTime 5s (500 GH/s × 5s / 2^32)
		// ========================================================================
		{"bitaxe ultra/2.0", 580},
		{"BitAxe Ultra 204", 580},
		{"BitAxe Supra", 580},
		{"bitaxe supra/1.2", 580},
		{"nmaxe/1.0", 580},
		{"NMAxe Pro", 580},
		{"bitaxe/1.0", 580},
		{"BitAxe 201", 580},
		{"ESP-Miner/2.9.21", 580},
		{"esp-miner/1.0", 580},
		{"cpuminer/2.5.1", 580},
		{"sgminer/5.6.0", 580},
		{"ccminer/2.3.1", 580},
		{"Compac F/1.0", 580},
		{"FutureBit Moonlander 2", 580},
		{"Lucky Miner LV06/1.0", 580},
		{"lucky lv06", 580},
		{"LV06/1.0", 580},
		{"Lucky Miner LG07/1.0", 580},
		{"lucky lg07", 580},
		{"LG07/1.0", 580},
		{"lucky miner/2.0", 580},
		{"antminer l3+/1.0", 580},
		{"Goldshell Mini DOGE III+/1.0", 580},
		{"Goldshell Mini DOGE III/1.0", 580},
		{"Goldshell Mini DOGE II/1.0", 580},
		{"Goldshell Mini DOGE Pro/1.0", 580},
		{"Goldshell Mini DOGE/1.0", 580},
		{"Mini DOGE/1.0", 580},
		{"Goldshell BYTE DG Card", 580},
		{"Hammer Miner/1.0", 580},
		{"hammerminer/2.0", 580},
		{"PlebSource Hammer/1.0", 580},
		{"plebsource/2.0", 580},
		{"Doge Digger/1.0", 580},

		// ========================================================================
		// MID — InitialDiff 1165, TargetShareTime 1s (5 TH/s × 1s / 2^32)
		// ========================================================================
		{"NerdOctaxe Gamma/1.0", 1165},
		{"nerdoctaxe/2.0", 1165},
		{"octaxe/1.0", 1165},
		{"nerdqaxe/1.0", 1165},
		{"NerdQAxe++/3.0", 1165},
		{"nerdaxe/1.0", 1165},
		{"BitAxe Supra Hex 701", 1165},
		{"supra hex/1.0", 1165},
		{"BitAxe Ultra Hex/1.0", 1165},
		{"ultra hex miner", 1165},
		{"bitaxe hex/1.0", 1165},
		{"BitAxe GT/1.0", 1165},
		{"bitaxe 801", 1165},
		{"bitaxe turbo/1.0", 1165},
		{"Gamma Turbo/2.0", 1165},
		{"bitaxe gamma", 1165},
		{"gekkoscience/compac", 1165},
		{"GekkoScience R909", 1165},
		{"compac/4.12", 1165},
		{"newpac/1.0", 1165},
		{"R606/2.0", 1165},
		{"Lucky Miner LV08/1.0", 1165},
		{"lucky lv08", 1165},
		{"LV08/2.0", 1165},
		{"Lucky Miner LV07/1.0", 1165},
		{"lucky lv07", 1165},
		{"LV07/1.0", 1165},
		{"Jingle Miner BTC Solo Pro/1.0", 1165},
		{"jingle pro/2.0", 1165},
		{"BTC Solo Pro/1.0", 1165},
		{"Jingle Miner BTC Solo Lite/1.0", 1165},
		{"jingle lite/2.0", 1165},
		{"BTC Solo Lite/1.0", 1165},
		{"jingle miner/3.0", 1165},
		{"JingleMiner/1.0", 1165},
		{"FutureBit Apollo II/2.0", 1165},
		{"Apollo II/1.5", 1165},
		{"FutureBit Apollo/1.0", 1165},
		{"Apollo BTC/1.0", 1165},
		{"futurebit/3.0", 1165},
		{"Zyber 8G/1.0", 1165},
		{"zyber 8gp/2.0", 1165},
		{"Zyber 8S/1.0", 1165},
		{"zyber/3.0", 1165},
		{"TinyChipHub/1.0", 1165},
		{"cgminer/4.12.0", 1165},
		{"bfgminer/5.5.0", 1165},
		{"Goldshell LT Lite/1.0", 1165},
		{"goldshell/2.0", 1165},
		{"FluMiner L2/1.0", 1165},
		{"flu miner l2/2.0", 1165},
		{"flu miner/1.0", 1165},

		// ========================================================================
		// HIGH — InitialDiff 3260, TargetShareTime 1s (14 TH/s × 1s / 2^32)
		// ========================================================================
		{"antminer s9/1.0", 3260},
		{"Antminer S9i", 3260},
		{"Antminer S9j", 3260},
		{"antminer s11/1.0", 3260},
		{"antminer s15/1.0", 3260},
		{"Antminer S17 Pro", 3260},
		{"antminer t9+", 3260},
		{"Antminer T15/1.0", 3260},
		{"antminer t17/2.0", 3260},
		{"whatsminer m20s", 3260},
		{"Whatsminer M10/1.0", 3260},
		{"innosilicon t2", 3260},
		{"Innosilicon T1/1.0", 3260},
		{"innosilicon t3/1.0", 3260},
		{"ebang/1.0", 3260},
		{"Ebang E12+", 3260},
		{"Ebit E11++", 3260},
		{"Ebit E12/1.0", 3260},
		{"ebit/2.0", 3260},
		{"Goldshell LT6/1.0", 3260},           // 3.35 GH/s Scrypt-only — High class (Pro MinDiff too high for Scrypt)
		{"LT6 Goldshell/1.0", 3260},
		{"Goldshell LT5 Pro/1.0", 3260},
		{"LT5 Goldshell/1.0", 3260},
		{"Elphapex DG2 Mini/1.0", 3260},
		{"Elphapex DG Home 1/1.0", 3260},
		{"DG Home/1.0", 3260},
		{"elphapex/2.0", 3260},
		{"VolcMiner D1 Mini/1.0", 3260},
		{"iBeLink BM-L3/1.0", 3260},
		{"ibelink/2.0", 3260},

		// ========================================================================
		// PRO — InitialDiff 25600, TargetShareTime 1s (110 TH/s × 1s / 2^32)
		// ========================================================================
		{"bmminer/1.0.0", 25600},
		{"btminer/3.1.0", 25600},
		{"antminer s19 pro", 25600},
		{"Antminer S19 XP Hyd", 25600},
		{"Antminer S21", 25600},
		{"Antminer S21 XP", 25600},
		{"Antminer T19/1.0", 25600},
		{"Antminer T21/1.0", 25600},
		{"Whatsminer M30S++/1.0", 25600},
		{"whatsminer m30s+/2.0", 25600},
		{"Whatsminer M30S/1.0", 25600},
		{"whatsminer m50s", 25600},
		{"Whatsminer M60S/1.0", 25600},
		{"whatsminer m63s/1.0", 25600},
		{"whatsminer m66s/1.0", 25600},
		{"Whatsminer M79S/1.0", 25600},
		{"Whatsminer M78/1.0", 25600},
		{"Whatsminer M76S+/1.0", 25600},
		{"Whatsminer M73S+/1.0", 25600},
		{"Whatsminer M72S/1.0", 25600},
		{"Whatsminer M70S/1.0", 25600},
		{"whatsminer m90s/1.0", 25600},
		{"whatsminer m40s/1.0", 25600},
		{"braiins os+", 25600},
		{"Braiins OS 22.08", 25600},
		{"vnish/1.0", 25600},
		{"Vnish 2024.6", 25600},
		{"luxos/2.0", 25600},
		{"LuxOS/2024.1", 25600},
		{"luxminer/1.0.0", 25600},
		{"Sealminer A3 Pro Hydro/1.0", 25600},
		{"sealminer a3 hydro/1.0", 25600},
		{"Sealminer A3 Pro/1.0", 25600},
		{"sealminer a3/1.0", 25600},
		{"Sealminer A2 Pro Hydro/1.0", 25600},
		{"sealminer a2 hydro/1.0", 25600},
		{"Sealminer A2 Pro/1.0", 25600},
		{"sealminer a2/1.0", 25600},
		{"sealminer/3.0", 25600},
		{"bitdeer/1.0", 25600},
		{"Bitdeer Miner/2.0", 25600},
		{"Teraflux AH3880/1.0", 25600},
		{"teraflux ai3680/1.0", 25600},
		{"Teraflux AT2880/1.0", 25600},
		{"teraflux/2.0", 25600},
		{"auradine/1.0", 25600},
		{"Goldshell DG Max/1.0", 25600},
		{"Antminer L11 Hydro/1.0", 25600},
		{"antminer l11 pro/1.0", 25600},
		{"Antminer L11/1.0", 25600},
		{"antminer l9/1.0", 25600},
		{"Antminer L7/1.0", 25600},
		{"Elphapex DG2+/1.0", 25600},
		{"DG2+/1.0", 25600},
		{"Elphapex DG2/1.0", 25600},
		{"Elphapex DG Hydro 1/1.0", 25600},
		{"DG Hydro/1.0", 25600},
		{"Elphapex DG1+/1.0", 25600},
		{"DG1+/1.0", 25600},
		{"Elphapex DG1 Lite/1.0", 25600},
		{"Elphapex DG1/1.0", 25600},
		{"VolcMiner D1 Hydro/1.0", 25600},
		{"volcminer d1 pro/1.0", 25600},
		{"VolcMiner D3/1.0", 25600},
		{"volcminer d1 lite/1.0", 25600},
		{"volcminer d1/1.0", 25600},
		{"volcminer/2.0", 25600},
		{"FluMiner T3/1.0", 25600},
		{"flu miner t3/2.0", 25600},
		{"FluMiner L3/1.0", 25600},
		{"flu miner l3/2.0", 25600},
		{"FluMiner L1 Pro/1.0", 25600},
		{"flu miner l1 pro/2.0", 25600},
		{"FluMiner L1/1.0", 25600},
		{"flu miner l1/2.0", 25600},
		{"fluminer/3.0", 25600},
		{"nicehash/1.0", 25600},
		{"excavator/1.4.4a_nvidia", 25600},
		{"miningrigrentals/2.0", 25600},
		{"mrr/1.0", 25600},
		{"cudo/3.0", 25600},
		{"zergpool/1.0", 25600},
		{"prohashing/1.0", 25600},
		{"miningdutch/1.0", 25600},
		{"mining.dutch/2.0", 25600},
		{"zpool/1.0", 25600},
		{"woolypooly/1.0", 25600},
		{"wooly/2.0", 25600},
		{"herominers/1.0", 25600},
		{"hero/2.0", 25600},
		{"unmineable/1.0", 25600},

		// ========================================================================
		// CONFIRMED REAL-WORLD USER AGENTS (from firmware source code)
		// ========================================================================
		{"bitaxe/BM1370/v2.4.5", 580},      // BitAxe stock UA → Low
		{"bitaxe/BM1366/v2.0.5", 580},      // Lucky Miner stock UA → Low
		{"bitaxe/BM1368/v2.3.0", 580},      // BitAxe 201 stock UA → Low
		{"NerdMinerV2/2.6.0", 0.001},        // NerdMiner stock UA → Lottery
		{"bmminer/2.0.0", 25600},            // Bitmain stock UA → Pro
		{"btminer/3.4.0", 25600},            // MicroBT stock UA → Pro
		{"cgminer/4.11.1", 1165},            // Avalon stock UA → Mid
		{"cgminer/4.13.1", 1165},            // GekkoScience stock UA → Mid

		// ========================================================================
		// AVALON HOME — InitialDiff 14900 (64 TH/s × 1s / 2^32)
		// ========================================================================
		{"Avalon Mini 3", 14900},
		{"canaan mini 3", 14900},
		{"Mini 3 Avalon", 14900},
		{"avalon q 90th", 14900},
		{"canaan q miner", 14900},
		{"avalon home miner", 14900},
		{"canaan home miner", 14900},

		// ========================================================================
		// AVALON NANO — InitialDiff 1538 (6.6 TH/s × 1s / 2^32)
		// ========================================================================
		{"avalon nano 3s", 1538},
		{"Avalon Nano 3S", 1538},
		{"nano 3s", 1538},
		{"Avalon Nano 3", 1538},
		{"avalon nano 2", 1538},
		{"avalon nano", 1538},
		{"Nano Avalon 3S", 1538},
		{"Nano Avalon", 1538},
		{"canaan nano 3s", 1538},
		{"canaan nano 3", 1538},
		{"canaan nano", 1538},
		{"Nano 3/1.0", 1538},

		// ========================================================================
		// AVALON PRO — InitialDiff 45000 (193 TH/s × 1s / 2^32)
		// ========================================================================
		{"A16 XP/1.0", 45000},
		{"avalon a16/2.0", 45000},
		{"A15 Pro", 45000},
		{"a15 xp", 45000},
		{"A15 SE", 45000},
		{"AvalonMiner 1566", 45000},
		{"avalon a1566", 45000},
		{"avalon 1566", 45000},
		{"avalon a15", 45000},
		{"AvalonMiner 1466", 45000},
		{"avalon a1466", 45000},
		{"avalon a14", 45000},
		{"AvalonMiner 1600", 45000},
		{"AvalonMiner 1599", 45000},
		{"AvalonMiner 1499", 45000},
		{"avalon 1680", 45000},
		{"avalon 1580", 45000},
		{"avalon 1480", 45000},

		// ========================================================================
		// AVALON HIGH — InitialDiff 25000 (107 TH/s × 1s / 2^32)
		// ========================================================================
		{"AvalonMiner 1366", 25000},
		{"AvalonMiner 1346", 25000},
		{"avalon a1366", 25000},
		{"avalon 1346", 25000},
		{"avalon a13", 25000},
		{"AvalonMiner 1246", 25000},
		{"avalon a1246", 25000},
		{"avalon 1246", 25000},
		{"avalon a12", 25000},
		{"AvalonMiner 1399", 25000},
		{"AvalonMiner 1299", 25000},
		{"avalon 1380", 25000},
		{"avalon 1280", 25000},

		// ========================================================================
		// AVALON MID — InitialDiff 11650 (50 TH/s × 1s / 2^32)
		// ========================================================================
		{"AvalonMiner 1166", 11650},
		{"AvalonMiner 1146", 11650},
		{"AvalonMiner 1126", 11650},
		{"avalon 1166 pro", 11650},
		{"avalon 1146", 11650},
		{"avalon 1126", 11650},
		{"AvalonMiner 1066", 11650},
		{"AvalonMiner 1047", 11650},
		{"AvalonMiner 1026", 11650},
		{"avalon 1066", 11650},
		{"avalon 1047", 11650},
		{"avalon 1026", 11650},
		{"AvalonMiner 921", 11650},
		{"AvalonMiner 911", 11650},
		{"avalon 921", 11650},
		{"avalon 911", 11650},
		{"AvalonMiner 1199", 11650},
		{"AvalonMiner 1099", 11650},
		{"AvalonMiner 999", 11650},
		{"avalon 1180", 11650},
		{"avalon 1090", 11650},
		{"avalon 950", 11650},
		// Generic Avalon/Canaan fallbacks
		{"avalonminer unknown", 11650},
		{"avalon miner xyz", 11650},
		{"avalon something", 11650},
		{"canaan miner", 11650},
		{"Canaan XYZ 2026", 11650},

		// ========================================================================
		// AVALON LEGACY MID — InitialDiff 2560 (11 TH/s × 1s / 2^32)
		// ========================================================================
		{"AvalonMiner 851", 2560},
		{"AvalonMiner 841", 2560},
		{"AvalonMiner 821", 2560},
		{"avalon 851", 2560},
		{"avalon 841", 2560},
		{"avalon 821", 2560},
		{"AvalonMiner 761", 2560},
		{"AvalonMiner 741", 2560},
		{"AvalonMiner 721", 2560},
		{"avalon 761", 2560},
		{"avalon 741", 2560},
		{"avalon 721", 2560},
		{"AvalonMiner 899", 2560},
		{"AvalonMiner 799", 2560},
		{"avalon 870", 2560},
		{"avalon 770", 2560},

		// ========================================================================
		// AVALON LEGACY LOW — InitialDiff 1630 (815 × 2s target)
		// ========================================================================
		{"AvalonMiner 641", 1630},
		{"AvalonMiner 621", 1630},
		{"avalon 641", 1630},
		{"avalon 621", 1630},
		{"avalon 6", 1630},
		{"AvalonMiner 3s", 1630},
		{"avalon 3s", 1630},
		{"AvalonMiner 690", 1630},
		{"avalon 660", 1630},

		// ========================================================================
		// UNKNOWN — InitialDiff 500
		// ========================================================================
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
		// Avalon-specific classes
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
	diff := router.GetInitialDifficulty("nerdminer/2.0")
	if diff != 0.0001 {
		t.Errorf("Expected 0.0001, got %v", diff)
	}
}

// Benchmark Spiral Router miner detection
func BenchmarkSpiralRouterDetection(b *testing.B) {
	router := NewSpiralRouter()
	userAgents := []string{
		"nerdminer/2.0",
		"nminer/1.0",
		"bitaxe ultra/1.0",
		"antminer s19 pro",
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

	// Test 1: ESP-Miner with device hint for NMAxe
	registry.Set(&DeviceHint{
		IP:          "192.168.1.100",
		DeviceModel: "NMAxe",
		ASICModel:   "BM1366",
		ASICCount:   1,
		HashrateGHs: 470,
		Class:       MinerClassLow,
	})

	// Should use device hint even though user-agent is ESP-Miner
	class, name := router.DetectMinerWithIP("ESP-Miner/2.9.21", "192.168.1.100")
	if class != MinerClassLow {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassLow", class)
	}
	if name != "NMAxe" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"NMAxe\"", name)
	}

	// Test 2: ESP-Miner with device hint for NerdQAxe++
	registry.Set(&DeviceHint{
		IP:          "192.168.1.101",
		DeviceModel: "NerdQAxe++",
		ASICModel:   "BM1370",
		ASICCount:   4,
		HashrateGHs: 5000,
		Class:       MinerClassMid,
	})

	class, name = router.DetectMinerWithIP("ESP-Miner/2.9.21", "192.168.1.101")
	if class != MinerClassMid {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassMid", class)
	}
	if name != "NerdQAxe++" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"NerdQAxe++\"", name)
	}

	// Test 3: No device hint - falls back to user-agent detection (ESP-Miner is now detected as Low)
	class, name = router.DetectMinerWithIP("ESP-Miner/2.9.21", "192.168.1.200")
	if class != MinerClassLow {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassLow", class)
	}
	if name != "ESP-Miner" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"ESP-Miner\"", name)
	}

	// Test 4: No device hint but identifiable user-agent
	class, name = router.DetectMinerWithIP("bitaxe ultra/2.0", "192.168.1.200")
	if class != MinerClassLow {
		t.Errorf("DetectMinerWithIP() class = %v, want MinerClassLow", class)
	}
	if name != "BitAxe Ultra" {
		t.Errorf("DetectMinerWithIP() name = %q, want \"BitAxe Ultra\"", name)
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

	// Even if user-agent says something else, device hint wins
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
	// HashrateGHs is now used to calculate difficulty directly:
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
		diff := router.GetInitialDifficultyWithIP("ESP-Miner/2.9.21", "10.0.0.1")
		// Expected: 470 * 1e9 * 5 / 4294967296 ≈ 547.3
		if diff < 500 || diff > 600 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~547 (470 GH/s @ 5s target)", diff)
		}
	})

	t.Run("NerdQAxe++ with hashrate-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("ESP-Miner/2.9.21", "10.0.0.2")
		// Expected: 5000 * 1e9 * 1 / 4294967296 ≈ 1164.2
		if diff < 1100 || diff > 1250 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want ~1164 (5000 GH/s @ 1s target)", diff)
		}
	})

	t.Run("Unknown IP falls back to user-agent detection", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("ESP-Miner/2.9.21", "10.0.0.99")
		// ESP-Miner matches MinerClassLow with InitialDiff = 580
		if diff != 580 {
			t.Errorf("GetInitialDifficultyWithIP() = %v, want 580 (class-based fallback)", diff)
		}
	})

	t.Run("With port number - should still work", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("ESP-Miner/2.9.21", "10.0.0.1:12345")
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
	// This was the original issue: high hashrate miner getting too low difficulty
	registry.Set(&DeviceHint{
		IP:          "192.168.1.14",
		DeviceModel: "Avalon Nano 3S",
		ASICModel:   "unknown",
		ASICCount:   1,
		HashrateGHs: 6660,
		Class:       MinerClassMid,
	})

	t.Run("Avalon Nano 3S gets correct hashrate-based difficulty", func(t *testing.T) {
		diff := router.GetInitialDifficultyWithIP("cgminer/4.11.1", "192.168.1.14")
		// Expected: 6660 * 1e9 * 1 / 4294967296 ≈ 1550.5
		// MinerClassMid has TargetShareTime = 1s
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
		diff := router.GetInitialDifficultyWithIP("cgminer/4.11.1", "192.168.1.20")
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
		{&DeviceHint{IP: "2.5.1.1", DeviceModel: "Avalon 1166", HashrateGHs: 81000}, MinerClassAvalonMid},
		{&DeviceHint{IP: "2.5.1.2", DeviceModel: "Avalon 1066", HashrateGHs: 50000}, MinerClassAvalonMid},
		{&DeviceHint{IP: "2.5.1.3", DeviceModel: "Avalon 921", HashrateGHs: 20000}, MinerClassAvalonMid},

		// Avalon Legacy Mid (A7, A8)
		{&DeviceHint{IP: "2.6.1.1", DeviceModel: "Avalon 851", HashrateGHs: 15000}, MinerClassAvalonLegacyMid},
		{&DeviceHint{IP: "2.6.1.2", DeviceModel: "Avalon 841", HashrateGHs: 13600}, MinerClassAvalonLegacyMid},
		{&DeviceHint{IP: "2.6.1.3", DeviceModel: "Avalon 741", HashrateGHs: 7300}, MinerClassAvalonLegacyMid},

		// Avalon Legacy Low (A3, A6)
		{&DeviceHint{IP: "2.7.1.1", DeviceModel: "Avalon 641", HashrateGHs: 3500}, MinerClassAvalonLegacyLow},
		{&DeviceHint{IP: "2.7.1.2", DeviceModel: "Avalon 6", HashrateGHs: 3500}, MinerClassAvalonLegacyLow},
		{&DeviceHint{IP: "2.7.1.3", DeviceModel: "Avalon 3", HashrateGHs: 800}, MinerClassAvalonLegacyLow},

		// Generic Avalon fallback with hashrate
		{&DeviceHint{IP: "2.8.1.1", DeviceModel: "Avalon Unknown", HashrateGHs: 3000}, MinerClassAvalonNano},   // Low hashrate -> Nano class
		{&DeviceHint{IP: "2.8.1.2", DeviceModel: "Canaan Unknown", HashrateGHs: 50000}, MinerClassAvalonMid},   // Mid hashrate -> Mid class
		{&DeviceHint{IP: "2.8.1.3", DeviceModel: "Avalon Generic", HashrateGHs: 100000}, MinerClassAvalonHigh}, // High hashrate -> High class
		{&DeviceHint{IP: "2.8.1.4", DeviceModel: "Canaan Generic", HashrateGHs: 180000}, MinerClassAvalonPro},  // Very high -> Pro class

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
// Scrypt miners (LTC, DOGE, PEP, CAT, DGB-SCRYPT) use completely different
// difficulty scales. These tests verify the Scrypt profiles return correct values.

func TestScryptAlgorithmDifficulties(t *testing.T) {
	router := NewSpiralRouter()
	router.SetAlgorithm(AlgorithmScrypt)

	// Scrypt formula: D = hashrate × targetTime / 65536
	// (vs SHA-256d: D = hashrate × targetTime / 2^32)
	//
	// Profile verification:
	//   Unknown: 8000 @ 10s → 52.4 MH/s
	//   Lottery: 0.1 @ 60s → 109 H/s
	//   Low:    28000 @ 10s → 183 MH/s (Mini DOGE class)
	//   Mid:    38000 @ 5s  → 498 MH/s (L3+ class)
	//   High:  180000 @ 4s  → 2.95 GH/s (LT5 Pro class)
	//   Pro:   290000 @ 2s  → 9.5 GH/s (L7 class)

	tests := []struct {
		userAgent    string
		expectedDiff float64
	}{
		// ========================================================================
		// UNKNOWN — Scrypt InitialDiff 8000
		// ========================================================================
		{"unknown-miner", 8000},
		{"totally-random-thing", 8000},
		{"", 8000},

		// ========================================================================
		// LOTTERY — Scrypt InitialDiff 0.1 (D = 109 H/s × 60s / 65536)
		// ========================================================================
		{"nerdminer/2.0.0", 0.1},
		{"NerdMiner V2", 0.1},
		{"HAN SOLOminer/1.0", 0.1},
		{"han solo miner", 0.1},
		{"nminer/1.0", 0.1},
		{"NMiner", 0.1},
		{"bitmaker", 0.1},
		{"bitmaker/2.1", 0.1},
		{"NerdESP32/1.0", 0.1},
		{"lottery miner/1.0", 0.1},
		{"ESP32 Miner V2", 0.1},
		{"esp32-miner", 0.1},
		{"my-esp32-rig", 0.1},
		{"arduino miner/0.1", 0.1},
		{"Arduino BTC Miner", 0.1},
		{"sparkminer/1.0", 0.1},
		{"SparkMiner ESP32", 0.1},

		// ========================================================================
		// LOW — Scrypt InitialDiff 28000 (D = 183 MH/s × 10s / 65536)
		// ========================================================================
		// Goldshell Mini DOGE series
		{"Goldshell Mini DOGE III+/1.0", 28000},
		{"Goldshell Mini DOGE III/1.0", 28000},
		{"Goldshell Mini DOGE II/1.0", 28000},
		{"Goldshell Mini DOGE Pro/1.0", 28000},
		{"Goldshell Mini DOGE/1.0", 28000},
		{"Mini DOGE/1.0", 28000},
		{"Goldshell BYTE DG Card", 28000},
		// Antminer L3+ (504 MH/s, older Scrypt ASIC)
		{"antminer l3+/1.0", 28000},
		// Hammer Miner / PlebSource (Scrypt entry-level)
		{"Hammer Miner/1.0", 28000},
		{"hammerminer/2.0", 28000},
		{"PlebSource Hammer/1.0", 28000},
		{"plebsource/2.0", 28000},
		{"Doge Digger/1.0", 28000},
		// Lucky Miner Scrypt model
		{"Lucky Miner LG07/1.0", 28000},
		{"lucky lg07", 28000},
		{"LG07/1.0", 28000},
		{"lucky miner/2.0", 28000},
		// BitAxe/USB miners (Low class)
		{"bitaxe ultra/2.0", 28000},
		{"bitaxe/1.0", 28000},
		{"nmaxe/1.0", 28000},
		{"BitAxe Supra", 28000},
		{"ESP-Miner/2.9.21", 28000},
		{"cpuminer/2.5.1", 28000},
		{"sgminer/5.6.0", 28000},
		{"ccminer/2.3.1", 28000},
		{"Compac F/1.0", 28000},
		{"FutureBit Moonlander 2", 28000},
		{"Lucky Miner LV06/1.0", 28000},

		// ========================================================================
		// MID — Scrypt InitialDiff 38000 (D = 498 MH/s × 5s / 65536)
		// ========================================================================
		{"Goldshell LT Lite/1.0", 38000},
		{"goldshell/2.0", 38000},
		{"FluMiner L2/1.0", 38000},
		{"flu miner l2/2.0", 38000},
		{"flu miner/1.0", 38000},
		{"cgminer/4.12.0", 38000},
		{"bfgminer/5.5.0", 38000},
		// Mid-range multi-chip boards
		{"NerdOctaxe Gamma/1.0", 38000},
		{"nerdqaxe/1.0", 38000},
		{"bitaxe hex/1.0", 38000},
		{"BitAxe GT/1.0", 38000},
		{"bitaxe gamma", 38000},
		{"gekkoscience/compac", 38000},
		{"compac/4.12", 38000},
		{"newpac/1.0", 38000},
		{"R606/2.0", 38000},
		{"Lucky Miner LV08/1.0", 38000},
		{"Lucky Miner LV07/1.0", 38000},
		{"Jingle Miner BTC Solo Pro/1.0", 38000},
		{"jingle miner/3.0", 38000},
		{"FutureBit Apollo II/2.0", 38000},
		{"futurebit/3.0", 38000},
		{"Zyber 8G/1.0", 38000},
		{"zyber 8gp/2.0", 38000},
		{"Zyber 8S/1.0", 38000},
		{"TinyChipHub/1.0", 38000},

		// ========================================================================
		// HIGH — Scrypt InitialDiff 180000 (D = 2.95 GH/s × 4s / 65536)
		// ========================================================================
		// Goldshell LT6 (3.35 GH/s Scrypt) — High, not Pro (Pro MinDiff=128K > D_optimal=102K)
		{"Goldshell LT6/1.0", 180000},
		{"LT6 Goldshell/1.0", 180000},
		// Goldshell LT5 (2-2.5 GH/s Scrypt)
		{"Goldshell LT5 Pro/1.0", 180000},
		{"LT5 Goldshell/1.0", 180000},
		// Elphapex home/mini (2.1-2.4 GH/s Scrypt)
		{"Elphapex DG2 Mini/1.0", 180000},
		{"Elphapex DG Home 1/1.0", 180000},
		{"DG Home/1.0", 180000},
		{"elphapex/2.0", 180000},
		// VolcMiner mini (2.2 GH/s Scrypt)
		{"VolcMiner D1 Mini/1.0", 180000},
		// iBeLink (3.2 GH/s Scrypt)
		{"iBeLink BM-L3/1.0", 180000},
		{"ibelink/2.0", 180000},
		// SHA-256d miners that get High class (same class, Scrypt diff)
		{"antminer s9/1.0", 180000},
		{"Antminer S17 Pro", 180000},
		{"whatsminer m20s", 180000},
		{"innosilicon t2", 180000},
		{"ebang/1.0", 180000},
		{"Ebit E11++", 180000},

		// ========================================================================
		// PRO — Scrypt InitialDiff 290000 (D = 9.5 GH/s × 2s / 65536)
		// ========================================================================
		// Antminer L series (Scrypt ASICs)
		{"Antminer L11 Hydro/1.0", 290000},
		{"antminer l11 pro/1.0", 290000},
		{"Antminer L11/1.0", 290000},
		{"antminer l9/1.0", 290000},
		{"Antminer L7/1.0", 290000},
		// Goldshell pro-tier Scrypt
		{"Goldshell DG Max/1.0", 290000},
		// Elphapex DG series (Scrypt ASICs)
		{"Elphapex DG2+/1.0", 290000},
		{"DG2+/1.0", 290000},
		{"Elphapex DG2/1.0", 290000},
		{"Elphapex DG Hydro 1/1.0", 290000},
		{"DG Hydro/1.0", 290000},
		{"Elphapex DG1+/1.0", 290000},
		{"DG1+/1.0", 290000},
		{"Elphapex DG1 Lite/1.0", 290000},
		{"Elphapex DG1/1.0", 290000},
		// VolcMiner D series (Scrypt ASICs)
		{"VolcMiner D1 Hydro/1.0", 290000},
		{"volcminer d1 pro/1.0", 290000},
		{"VolcMiner D3/1.0", 290000},
		{"volcminer d1 lite/1.0", 290000},
		{"volcminer d1/1.0", 290000},
		{"volcminer/2.0", 290000},
		// FluMiner L series (Scrypt)
		{"FluMiner L3/1.0", 290000},
		{"flu miner l3/2.0", 290000},
		{"FluMiner L1 Pro/1.0", 290000},
		{"flu miner l1 pro/2.0", 290000},
		{"FluMiner L1/1.0", 290000},
		{"flu miner l1/2.0", 290000},
		{"fluminer/3.0", 290000},
		// FluMiner T series (SHA-256d, but Pro class gets Scrypt diff when on Scrypt pool)
		{"FluMiner T3/1.0", 290000},
		{"flu miner t3/2.0", 290000},
		// SHA-256d Pro miners (same class → same Scrypt diff when on Scrypt pool)
		{"antminer s19 pro", 290000},
		{"Antminer S21", 290000},
		{"Whatsminer M30S++/1.0", 290000},
		{"whatsminer m50s", 290000},
		{"bmminer/1.0.0", 290000},
		{"btminer/3.1.0", 290000},
		{"braiins os+", 290000},
		{"vnish/1.0", 290000},
		{"luxos/2.0", 290000},
		{"Sealminer A3 Pro Hydro/1.0", 290000},
		{"bitdeer/1.0", 290000},
		{"Teraflux AH3880/1.0", 290000},
		{"auradine/1.0", 290000},
		{"nicehash/1.0", 290000},
		{"miningrigrentals/2.0", 290000},
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
	// Router defaults to SHA-256d, but GetInitialDifficultyForAlgorithm
	// should return the correct value for any requested algorithm
	router := NewSpiralRouter() // Defaults to SHA-256d

	tests := []struct {
		userAgent    string
		algorithm    Algorithm
		expectedDiff float64
	}{
		// Same device, SHA-256d vs Scrypt should give DIFFERENT difficulties
		{"Antminer L7/1.0", AlgorithmSHA256d, 25600},  // Pro SHA-256d
		{"Antminer L7/1.0", AlgorithmScrypt, 290000},   // Pro Scrypt

		{"Goldshell Mini DOGE/1.0", AlgorithmSHA256d, 580},    // Low SHA-256d
		{"Goldshell Mini DOGE/1.0", AlgorithmScrypt, 28000},   // Low Scrypt

		{"cgminer/4.12", AlgorithmSHA256d, 1165},   // Mid SHA-256d
		{"cgminer/4.12", AlgorithmScrypt, 38000},    // Mid Scrypt

		{"antminer s9", AlgorithmSHA256d, 3260},     // High SHA-256d
		{"antminer s9", AlgorithmScrypt, 180000},    // High Scrypt (same class)

		{"nerdminer/2.0", AlgorithmSHA256d, 0.001},  // Lottery SHA-256d
		{"nerdminer/2.0", AlgorithmScrypt, 0.1},      // Lottery Scrypt

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
// Different blockchains have different block times. The router must scale
// TargetShareTime and difficulty proportionally to ensure enough shares per block.

func TestBlockTimeScaling(t *testing.T) {
	tests := []struct {
		name          string
		blockTime     int
		class         MinerClass
		expectedTarget int // Expected TargetShareTime after scaling
	}{
		// 600s blocks (BTC, etc.) — standard, no scaling needed for SHA-256d 1s targets
		{"600s_lottery", 600, MinerClassLottery, 60},
		{"600s_low", 600, MinerClassLow, 5},
		{"600s_mid", 600, MinerClassMid, 1},
		{"600s_high", 600, MinerClassHigh, 1},
		{"600s_pro", 600, MinerClassPro, 1},
		{"600s_avalon_legacy_low", 600, MinerClassAvalonLegacyLow, 2}, // minTargetTime=2

		// 15s blocks (DGB, etc.) — fast chain, shares must be faster
		// maxTargetTime = 15/5 = 3s for standard, 15/3 = 5s for avalon_legacy_low
		// Lottery: maxTargetTime = 15 (capped at 60, floor at 10) → 15
		{"15s_lottery", 15, MinerClassLottery, 15},    // min(60, max(10, 15)) = 15
		{"15s_low", 15, MinerClassLow, 3},             // min(5, 15/5=3) = 3
		{"15s_mid", 15, MinerClassMid, 1},              // min(1, 3) = 1 (already below max)
		{"15s_high", 15, MinerClassHigh, 1},
		{"15s_pro", 15, MinerClassPro, 1},
		{"15s_avalon_legacy_low", 15, MinerClassAvalonLegacyLow, 2}, // min(2, max(2, 15/3=5)) → 2

		// 60s blocks (DOGE, etc.) — medium chain
		// maxTargetTime = 60/5 = 12s for standard
		{"60s_lottery", 60, MinerClassLottery, 60},    // min(60, max(10, 60)) = 60
		{"60s_low", 60, MinerClassLow, 5},             // min(5, 12) = 5 (5 is already below)
		{"60s_mid", 60, MinerClassMid, 1},
		{"60s_pro", 60, MinerClassPro, 1},

		// 150s blocks (LTC, etc.) — standard targets unchanged
		{"150s_lottery", 150, MinerClassLottery, 60},  // min(60, max(10, 150)) = 60
		{"150s_low", 150, MinerClassLow, 5},
		{"150s_mid", 150, MinerClassMid, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewSpiralRouterWithBlockTime(tt.blockTime)
			profile := router.GetProfile(tt.class)
			if profile.TargetShareTime != tt.expectedTarget {
				t.Errorf("BlockTime=%d class=%v: TargetShareTime = %d, want %d",
					tt.blockTime, tt.class, profile.TargetShareTime, tt.expectedTarget)
			}
		})
	}
}

func TestBlockTimeScalingDifficulty(t *testing.T) {
	// When TargetShareTime changes, InitialDiff MUST scale proportionally.
	// Formula: NewDiff = OriginalDiff × (NewTargetTime / OriginalTargetTime)

	// DGB (15s blocks): Low class TargetShareTime goes from 5s → 3s
	// So InitialDiff = 580 × (3/5) = 348
	router := NewSpiralRouterWithBlockTime(15)
	lowProfile := router.GetProfile(MinerClassLow)
	expectedLowDiff := 580.0 * 3.0 / 5.0 // 348
	if lowProfile.InitialDiff != expectedLowDiff {
		t.Errorf("DGB Low InitialDiff = %v, want %v", lowProfile.InitialDiff, expectedLowDiff)
	}

	// Mid/High/Pro with 1s target: no scaling needed (1s is already below maxTargetTime)
	midProfile := router.GetProfile(MinerClassMid)
	if midProfile.InitialDiff != 1165 {
		t.Errorf("DGB Mid InitialDiff = %v, want 1165 (no scaling)", midProfile.InitialDiff)
	}

	// Avalon Legacy Low with minTargetTime=2: target goes from 1 → 2
	// InitialDiff = 815 × (2/1) = 1630
	avalonLegacyLow := router.GetProfile(MinerClassAvalonLegacyLow)
	if avalonLegacyLow.InitialDiff != 1630 {
		t.Errorf("DGB AvalonLegacyLow InitialDiff = %v, want 1630 (scaled 2x)", avalonLegacyLow.InitialDiff)
	}

	// MaxDiff should also scale UP when timeScaleFactor > 1
	// AvalonLegacyLow: timeScaleFactor = 2/1 = 2, so MaxDiff = 1500 * 2 = 3000
	if avalonLegacyLow.MaxDiff != 3000 {
		t.Errorf("DGB AvalonLegacyLow MaxDiff = %v, want 3000 (scaled 2x)", avalonLegacyLow.MaxDiff)
	}

	// MaxDiff should NOT scale down when timeScaleFactor < 1
	// Low class: timeScaleFactor = 3/5 = 0.6, MaxDiff stays at 150000
	if lowProfile.MaxDiff != 150000 {
		t.Errorf("DGB Low MaxDiff = %v, want 150000 (preserved, not scaled down)", lowProfile.MaxDiff)
	}
}

func TestBlockTimeScalingScrypt(t *testing.T) {
	// Scrypt profiles have different base TargetShareTimes (10, 5, 4, 2)
	// DGB-SCRYPT (15s blocks) should scale them appropriately

	router := NewSpiralRouterWithBlockTime(15)
	router.SetAlgorithm(AlgorithmScrypt)

	// Scrypt Low: base TargetShareTime=10, maxTargetTime=15/5=3 → scaled to 3
	lowProfile := router.GetProfile(MinerClassLow)
	if lowProfile.TargetShareTime != 3 {
		t.Errorf("DGB-Scrypt Low TargetShareTime = %d, want 3", lowProfile.TargetShareTime)
	}
	// InitialDiff = 28000 × (3/10) = 8400
	expectedDiff := 28000.0 * 3.0 / 10.0
	if lowProfile.InitialDiff != expectedDiff {
		t.Errorf("DGB-Scrypt Low InitialDiff = %v, want %v", lowProfile.InitialDiff, expectedDiff)
	}

	// Scrypt Mid: base TargetShareTime=5, maxTargetTime=3 → scaled to 3
	midProfile := router.GetProfile(MinerClassMid)
	if midProfile.TargetShareTime != 3 {
		t.Errorf("DGB-Scrypt Mid TargetShareTime = %d, want 3", midProfile.TargetShareTime)
	}

	// Scrypt Pro: base TargetShareTime=2, maxTargetTime=3 → stays at 2 (already below)
	proProfile := router.GetProfile(MinerClassPro)
	if proProfile.TargetShareTime != 2 {
		t.Errorf("DGB-Scrypt Pro TargetShareTime = %d, want 2 (unchanged)", proProfile.TargetShareTime)
	}
}

// ========================================================================
// ALGORITHM SWITCHING TESTS
// ========================================================================

func TestSetAlgorithmSwitchesProfiles(t *testing.T) {
	router := NewSpiralRouter()

	// Default is SHA-256d
	sha256Diff := router.GetInitialDifficulty("unknown-miner")
	if sha256Diff != 500 {
		t.Errorf("SHA-256d unknown diff = %v, want 500", sha256Diff)
	}

	// Switch to Scrypt
	router.SetAlgorithm(AlgorithmScrypt)
	scryptDiff := router.GetInitialDifficulty("unknown-miner")
	if scryptDiff != 8000 {
		t.Errorf("Scrypt unknown diff = %v, want 8000", scryptDiff)
	}

	// Switch back to SHA-256d
	router.SetAlgorithm(AlgorithmSHA256d)
	sha256DiffAgain := router.GetInitialDifficulty("unknown-miner")
	if sha256DiffAgain != 500 {
		t.Errorf("SHA-256d (restored) unknown diff = %v, want 500", sha256DiffAgain)
	}
}

func TestSetAlgorithmAffectsAllClasses(t *testing.T) {
	router := NewSpiralRouter()
	router.SetAlgorithm(AlgorithmScrypt)

	// Verify all Scrypt-capable classes return Scrypt values (not SHA-256d).
	// Avalon classes are SHA-256d ASICs only — they have no Scrypt profiles
	// and will never connect to a Scrypt pool.
	classes := map[MinerClass]float64{
		MinerClassUnknown: 8000,
		MinerClassLottery: 0.1,
		MinerClassLow:     28000,
		MinerClassMid:     38000,
		MinerClassHigh:    180000,
		MinerClassPro:     290000,
	}

	for class, expectedDiff := range classes {
		profile := router.GetProfile(class)
		if profile.InitialDiff != expectedDiff {
			t.Errorf("Scrypt class %v InitialDiff = %v, want %v",
				class, profile.InitialDiff, expectedDiff)
		}
	}
}

// ========================================================================
// SLOW-DIFF APPLIER TESTS
// ========================================================================
// cgminer-based firmware doesn't apply new difficulty to work-in-progress,
// so the pool needs to know which miners are slow to adjust.

func TestIsSlowDiffApplier(t *testing.T) {
	router := NewSpiralRouter()

	tests := []struct {
		userAgent string
		expected  bool
	}{
		// cgminer IS slow (default pattern)
		{"cgminer/4.12.0", true},
		{"CGMINER/4.11.1", true},          // Case insensitive
		{"my-cgminer-fork/1.0", true},     // Substring match

		// Non-cgminer is NOT slow
		{"ESP-Miner/2.9.21", false},
		{"nerdminer/2.0", false},
		{"braiins os/22.08", false},
		{"bitaxe ultra/2.0", false},
		{"bfgminer/5.5.0", false},         // bfgminer is different from cgminer
		{"antminer s19/1.0", false},
		{"unknown-miner", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			result := router.IsSlowDiffApplier(tt.userAgent)
			if result != tt.expected {
				t.Errorf("IsSlowDiffApplier(%q) = %v, want %v", tt.userAgent, result, tt.expected)
			}
		})
	}
}

func TestSetSlowDiffPatterns(t *testing.T) {
	router := NewSpiralRouter()

	// Add custom patterns
	router.SetSlowDiffPatterns([]string{"cgminer", "bfgminer", "avalonminer"})

	// Now bfgminer should also be slow
	if !router.IsSlowDiffApplier("bfgminer/5.5.0") {
		t.Error("bfgminer should be slow after SetSlowDiffPatterns")
	}
	if !router.IsSlowDiffApplier("AvalonMiner 1566") {
		t.Error("AvalonMiner should be slow after SetSlowDiffPatterns")
	}

	// Setting empty restores default (cgminer only)
	router.SetSlowDiffPatterns(nil)
	if router.IsSlowDiffApplier("bfgminer/5.5.0") {
		t.Error("bfgminer should NOT be slow after resetting to defaults")
	}
	if !router.IsSlowDiffApplier("cgminer/4.12.0") {
		t.Error("cgminer should still be slow after resetting to defaults")
	}
}

// ========================================================================
// DEFAULT TARGET TIME TESTS
// ========================================================================

func TestGetDefaultTargetTime(t *testing.T) {
	tests := []struct {
		name       string
		blockTime  int
		expected   float64
	}{
		{"600s_blocks", 600, 5},  // min(5, 600/5=120) = 5
		{"15s_blocks", 15, 3},    // min(5, 15/5=3) = 3
		{"60s_blocks", 60, 5},    // min(5, 60/5=12) = 5
		{"150s_blocks", 150, 5},  // min(5, 150/5=30) = 5
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewSpiralRouterWithBlockTime(tt.blockTime)
			targetTime := router.GetDefaultTargetTime()
			if targetTime != tt.expected {
				t.Errorf("GetDefaultTargetTime() blockTime=%d = %v, want %v",
					tt.blockTime, targetTime, tt.expected)
			}
		})
	}
}

// ========================================================================
// EDGE CASE / REGRESSION TESTS
// ========================================================================

func TestSetBlockTimeZeroIsNoOp(t *testing.T) {
	router := NewSpiralRouter()
	originalDiff := router.GetInitialDifficulty("antminer s19")

	// SetBlockTime(0) should be a no-op
	router.SetBlockTime(0)
	afterDiff := router.GetInitialDifficulty("antminer s19")
	if afterDiff != originalDiff {
		t.Errorf("SetBlockTime(0) changed difficulty: %v → %v", originalDiff, afterDiff)
	}

	// SetBlockTime(-1) should also be a no-op
	router.SetBlockTime(-1)
	afterDiff2 := router.GetInitialDifficulty("antminer s19")
	if afterDiff2 != originalDiff {
		t.Errorf("SetBlockTime(-1) changed difficulty: %v → %v", originalDiff, afterDiff2)
	}
}

func TestGetBlockTimeReturnsConfigured(t *testing.T) {
	router := NewSpiralRouterWithBlockTime(15)
	if router.GetBlockTime() != 15 {
		t.Errorf("GetBlockTime() = %d, want 15", router.GetBlockTime())
	}

	router.SetBlockTime(60)
	if router.GetBlockTime() != 60 {
		t.Errorf("GetBlockTime() after SetBlockTime(60) = %d, want 60", router.GetBlockTime())
	}
}

func TestWhitespaceUserAgentHandling(t *testing.T) {
	router := NewSpiralRouter()

	// Leading/trailing whitespace should be trimmed
	class, name := router.DetectMiner("  nerdminer/2.0  ")
	if class != MinerClassLottery {
		t.Errorf("Whitespace UA class = %v, want MinerClassLottery", class)
	}
	if name != "ESP32 Miner" {
		t.Errorf("Whitespace UA name = %q, want \"ESP32 Miner\"", name)
	}
}

func TestEmptyAndNilPatternSafety(t *testing.T) {
	router := NewSpiralRouter()

	// Empty string should return Unknown
	class, name := router.DetectMiner("")
	if class != MinerClassUnknown || name != "Unknown" {
		t.Errorf("Empty UA: class=%v name=%q, want Unknown", class, name)
	}

	// Very long user agent should still work (capped at 256 by handler, but router should handle any)
	longUA := ""
	for i := 0; i < 500; i++ {
		longUA += "x"
	}
	class, name = router.DetectMiner(longUA)
	if class != MinerClassUnknown || name != "Unknown" {
		t.Errorf("Long UA: class=%v name=%q, want Unknown", class, name)
	}
}

func TestPatternPriorityOrder(t *testing.T) {
	router := NewSpiralRouter()

	// "bitaxe supra hex" should match Supra Hex (Mid), NOT BitAxe Supra (Low)
	class, name := router.DetectMiner("bitaxe supra hex 701")
	if class != MinerClassMid {
		t.Errorf("Supra Hex priority: class = %v, want MinerClassMid (not Low)", class)
	}
	if name != "BitAxe Supra Hex" {
		t.Errorf("Supra Hex priority: name = %q, want \"BitAxe Supra Hex\"", name)
	}

	// "bitaxe ultra hex" should match Ultra Hex (Mid), NOT BitAxe Ultra (Low)
	class, name = router.DetectMiner("bitaxe ultra hex/1.0")
	if class != MinerClassMid {
		t.Errorf("Ultra Hex priority: class = %v, want MinerClassMid (not Low)", class)
	}

	// "Avalon Nano 3S" should match Nano 3S, NOT Avalon 3S (Legacy Low)
	class, _ = router.DetectMiner("Avalon Nano 3S/1.0")
	if class != MinerClassAvalonNano {
		t.Errorf("Nano 3S priority: class = %v, want MinerClassAvalonNano", class)
	}

	// "Avalon Mini 3" should match Home, NOT Nano (which also has "3")
	class, _ = router.DetectMiner("Avalon Mini 3/1.0")
	if class != MinerClassAvalonHome {
		t.Errorf("Mini 3 priority: class = %v, want MinerClassAvalonHome", class)
	}

	// "FluMiner L1 Pro" should match L1 Pro (Pro), NOT L1 (also Pro, but different name)
	_, name = router.DetectMiner("FluMiner L1 Pro/1.0")
	if name != "FluMiner L1 Pro" {
		t.Errorf("FluMiner L1 Pro priority: name = %q, want \"FluMiner L1 Pro\"", name)
	}

	// "Goldshell Mini DOGE III" should match III, NOT generic Mini DOGE
	_, name = router.DetectMiner("Goldshell Mini DOGE III/1.0")
	if name != "Goldshell Mini DOGE III" {
		t.Errorf("Mini DOGE III priority: name = %q, want \"Goldshell Mini DOGE III\"", name)
	}

	// "Sealminer A3 Pro Hydro" should match A3 Pro Hydro, NOT A3 Pro or A3
	_, name = router.DetectMiner("Sealminer A3 Pro Hydro/1.0")
	if name != "Sealminer A3 Pro Hydro" {
		t.Errorf("Sealminer A3 Pro Hydro priority: name = %q, want \"Sealminer A3 Pro Hydro\"", name)
	}

	// "Whatsminer M30S++" should match M30S++ (112 TH/s), NOT M30S+ or M30S
	_, name = router.DetectMiner("Whatsminer M30S++/1.0")
	if name != "Whatsminer M30S++" {
		t.Errorf("M30S++ priority: name = %q, want \"Whatsminer M30S++\"", name)
	}
}
