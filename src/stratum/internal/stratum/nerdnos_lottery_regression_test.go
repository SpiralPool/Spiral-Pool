// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package stratum

import (
	"math"
	"testing"
)

// =============================================================================
// TEST SUITE: NerdNOS class + lottery recalibration
// =============================================================================
// Covers the two routing changes made in response to the DigiByte reject-storm
// report: the NerdNOS class (previously unclassified, landing on MinerClassUnknown
// at difficulty 10000) and the lottery InitialDiff recalibration.
//
// The important tests here are the block-time-scaled ones. The profile constants
// are written for a 600s chain and every bug in this area appeared only after
// scaleProfilesForBlockTime multiplied them down for a 15s chain, so asserting the
// unscaled constants alone would not have caught the original defect.

const (
	// pow2_32 is the SHA-256d hashes-per-difficulty-unit constant.
	pow2_32 = 4294967296.0

	// dgbBlockTime is DigiByte's target block time - the chain the failure was
	// reported on, and the fastest one the pool supports.
	dgbBlockTime = 15
	btcBlockTime = 600
)

// shareIntervalSec returns the mean seconds between shares for a miner of the given
// hashrate at the given difficulty.
func shareIntervalSec(diff, hashrateHs float64) float64 {
	return diff * pow2_32 / hashrateHs
}

// -----------------------------------------------------------------------------
// The reported bug
// -----------------------------------------------------------------------------

// TestLotteryDGBScaling_RegressionForRejectStorm is the direct regression guard for
// the reported failure.
//
// On DigiByte the lottery profile is scaled by 15/60 = 0.25. At the old InitialDiff
// of 0.001 that produced 0.00025, at which a measured 430 KH/s board finds a share
// every ~2.5s - fast enough to sustain a reject storm and trip the ban threshold,
// with no way out because vardiff only retargets on accepted shares.
//
// This test asserts the scaled difficulty keeps real hardware at a sane cadence.
func TestLotteryDGBScaling_RegressionForRejectStorm(t *testing.T) {
	router := NewSpiralRouterWithBlockTime(dgbBlockTime)
	profile := router.GetProfile(MinerClassLottery)

	if profile.TargetShareTime != dgbBlockTime {
		t.Fatalf("expected 15s target on DGB, got %ds", profile.TargetShareTime)
	}

	// Measured hardware from the field report.
	boards := []struct {
		name       string
		hashrateHs float64
	}{
		{"ESP32-D0WD CYD NerdMiner", 430e3},
		{"NMMiner ESP32-S3", 390e3},
		{"LilyGo T-Display-S3", 250e3},
		{"LilyGo T-Dongle-S3", 250e3},
	}

	target := float64(profile.TargetShareTime)

	for _, b := range boards {
		t.Run(b.name, func(t *testing.T) {
			interval := shareIntervalSec(profile.InitialDiff, b.hashrateHs)

			// The failure was submissions far faster than the pool could keep up with.
			// Anything under a quarter of the target share time is back in that regime.
			if interval < target/4 {
				t.Errorf("share interval %.2fs is far below the %.0fs target - reject-storm regime (diff %.6f)",
					interval, target, profile.InitialDiff)
			}

			// Guard the opposite direction too: too high and the board waits so long
			// for a first share that it looks dead.
			if interval > target*4 {
				t.Errorf("share interval %.2fs is far above the %.0fs target - board would look dead (diff %.6f)",
					interval, target, profile.InitialDiff)
			}
		})
	}
}

// TestLotteryScaledValues pins the exact scaled profile on both a fast and a slow
// chain, so any future change to either the constants or the scaling maths is visible.
func TestLotteryScaledValues(t *testing.T) {
	tests := []struct {
		name            string
		blockTime       int
		wantInitial     float64
		wantMin         float64
		wantMax         float64
		wantTargetShare int
	}{
		// DGB: scale factor 15/60 = 0.25
		{"DGB_15s", dgbBlockTime, 0.001, 0.000025, 100, 15},
		// BTC: target time is already at the 60s profile value, no scaling
		{"BTC_600s", btcBlockTime, 0.004, 0.0001, 100, 60},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := NewSpiralRouterWithBlockTime(tt.blockTime).GetProfile(MinerClassLottery)

			assertClose(t, "InitialDiff", profile.InitialDiff, tt.wantInitial)
			assertClose(t, "MinDiff", profile.MinDiff, tt.wantMin)
			assertClose(t, "MaxDiff", profile.MaxDiff, tt.wantMax)
			if profile.TargetShareTime != tt.wantTargetShare {
				t.Errorf("TargetShareTime: got %d, want %d", profile.TargetShareTime, tt.wantTargetShare)
			}
		})
	}
}

// TestLotteryMinDiffUnchanged guards the deliberate decision NOT to raise the floor.
//
// The field report proposed MinDiff 0.002, which lifts the slowest supported miner
// from ~7 KH/s to ~143 KH/s. That is fine for a 430 KH/s board and strands the
// Arduino and LeafMiner class that shares this tier, so the floor stayed put and the
// slow boards are handled by vardiff idle descent instead.
func TestLotteryMinDiffUnchanged(t *testing.T) {
	profile := NewSpiralRouter().GetProfile(MinerClassLottery)

	assertClose(t, "MinDiff", profile.MinDiff, 0.0001)

	// The floor must still let genuinely tiny boards reach a share at all.
	slowBoards := map[string]float64{
		"Arduino ~1 KH/s":   1e3,
		"LeafMiner ~3 KH/s": 3e3,
	}
	for name, hashrate := range slowBoards {
		interval := shareIntervalSec(profile.MinDiff, hashrate)
		if interval > 30*60 {
			t.Errorf("%s: %.0fs per share at the floor - effectively unmineable", name, interval)
		}
	}
}

// -----------------------------------------------------------------------------
// NerdNOS user-agent
// -----------------------------------------------------------------------------

// TestNerdNOSUserAgentDetection covers the new pattern across the casings and
// separators the firmware and its forks might emit.
func TestNerdNOSUserAgentDetection(t *testing.T) {
	router := NewSpiralRouter()

	userAgents := []string{
		"NerdNOS/1.0",
		"NerdNOS/0.9.2",
		"nerdnos/1.2.3",
		"NERDNOS/2.0",
		"NerdNos/2.1.0",
		"nerd nos/1.0",
		"nerd-nos/1.0",
		"nerd_nos/1.0",
	}

	for _, ua := range userAgents {
		t.Run(ua, func(t *testing.T) {
			class, name := router.DetectMiner(ua)
			if class != MinerClassNerdNOS {
				t.Errorf("expected MinerClassNerdNOS, got %s", class.String())
			}
			if name != "NerdNOS" {
				t.Errorf("expected name %q, got %q", "NerdNOS", name)
			}
		})
	}
}

// TestNerdNOSNoLongerFallsToUnknown is the regression guard for the reported
// misclassification: before the pattern existed, "NerdNOS/x" matched nothing and
// landed on MinerClassUnknown at difficulty 10000 - roughly 86x too high.
func TestNerdNOSNoLongerFallsToUnknown(t *testing.T) {
	router := NewSpiralRouter()

	class, _ := router.DetectMiner("NerdNOS/1.0")
	if class == MinerClassUnknown {
		t.Fatal("NerdNOS is unclassified again - it would start at the Unknown difficulty")
	}

	profile := router.GetProfile(class)
	unknown := router.GetProfile(MinerClassUnknown)
	if profile.InitialDiff >= unknown.InitialDiff {
		t.Errorf("NerdNOS InitialDiff %.0f is no better than Unknown's %.0f",
			profile.InitialDiff, unknown.InitialDiff)
	}

	// ~175 GH/s should land near the profile's own target share time.
	const nerdNOSHashrate = 175e9
	interval := shareIntervalSec(profile.InitialDiff, nerdNOSHashrate)
	target := float64(profile.TargetShareTime)
	if interval < target/2 || interval > target*2 {
		t.Errorf("175 GH/s gets a %.2fs share interval against a %.0fs target", interval, target)
	}
}

// TestNerdNOSDoesNotStealOtherMiners guards the pattern's placement ahead of
// nerdminerv2. It must claim NerdNOS strings and nothing else.
func TestNerdNOSDoesNotStealOtherMiners(t *testing.T) {
	router := NewSpiralRouter()

	unaffected := []struct {
		userAgent string
		want      MinerClass
	}{
		{"NerdMinerV2/2.6.0", MinerClassLottery},
		{"nerdminerv2/1.0", MinerClassLottery},
		{"NerdQAxe++/BM1370/v1.0.36", MinerClassMid},
		{"NerdOCTAXE/BM1370/v1.0", MinerClassMid},
		{"nerdqaxe/BM1370/v1.0", MinerClassMid},
		{"bitaxe/BM1397/v1.0.0", MinerClassLow},
		{"bitaxe/BM1366/v2.9.31", MinerClassLow},
		{"esp32-miner/2.0", MinerClassLottery},
		{"NMMiner/v0.6.30", MinerClassLottery},
		{"bmminer/2.0.0", MinerClassPro},
		{"cgminer/4.12", MinerClassUnknown},
	}

	for _, tt := range unaffected {
		t.Run(tt.userAgent, func(t *testing.T) {
			if class, _ := router.DetectMiner(tt.userAgent); class != tt.want {
				t.Errorf("classification changed: got %s, want %s", class.String(), tt.want.String())
			}
		})
	}
}

// TestNerdNOSScaledValues pins the NerdNOS profile after block-time scaling.
// It scales differently from lottery - the default branch gives blockTime/5, so DGB
// takes it from a 5s to a 3s target rather than lottery's 60s to 15s.
func TestNerdNOSScaledValues(t *testing.T) {
	tests := []struct {
		name            string
		blockTime       int
		wantInitial     float64
		wantMin         float64
		wantTargetShare int
	}{
		{"DGB_15s", dgbBlockTime, 121.8, 84, 3}, // scale 3/5 = 0.6
		{"BTC_600s", btcBlockTime, 203, 140, 5}, // unscaled
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := NewSpiralRouterWithBlockTime(tt.blockTime).GetProfile(MinerClassNerdNOS)

			assertClose(t, "InitialDiff", profile.InitialDiff, tt.wantInitial)
			assertClose(t, "MinDiff", profile.MinDiff, tt.wantMin)
			if profile.TargetShareTime != tt.wantTargetShare {
				t.Errorf("TargetShareTime: got %d, want %d", profile.TargetShareTime, tt.wantTargetShare)
			}

			// A ~175 GH/s board should sit near its target at the scaled difficulty.
			interval := shareIntervalSec(profile.InitialDiff, 175e9)
			target := float64(profile.TargetShareTime)
			if interval < target/2 || interval > target*2 {
				t.Errorf("175 GH/s gets %.2fs per share against a %.0fs target", interval, target)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// NerdNOS via DeviceHints
// -----------------------------------------------------------------------------

// TestNerdNOSDeviceHintRouting covers the hint path, which takes priority over the
// user-agent in DetectMinerWithIP - so without it, Sentinel would silently override
// the user-agent classification.
func TestNerdNOSDeviceHintRouting(t *testing.T) {
	tests := []struct {
		name string
		hint *DeviceHint
		want MinerClass
	}{
		{
			name: "model names NerdNOS",
			hint: &DeviceHint{IP: "10.0.0.1", DeviceModel: "NerdNOS"},
			want: MinerClassNerdNOS,
		},
		{
			name: "model with space",
			hint: &DeviceHint{IP: "10.0.0.2", DeviceModel: "Nerd NOS v1"},
			want: MinerClassNerdNOS,
		},
		{
			name: "model names both, ASIC wins over lottery",
			hint: &DeviceHint{IP: "10.0.0.3", DeviceModel: "NerdMiner NerdNOS"},
			want: MinerClassNerdNOS,
		},
		{
			name: "bare BM1397 ASIC",
			hint: &DeviceHint{IP: "10.0.0.4", ASICModel: "BM1397"},
			want: MinerClassNerdNOS,
		},
		{
			name: "plain NerdMiner is still lottery",
			hint: &DeviceHint{IP: "10.0.0.5", DeviceModel: "NerdMiner V2"},
			want: MinerClassLottery,
		},
		{
			name: "recognised BitAxe still claimed by model switch",
			hint: &DeviceHint{IP: "10.0.0.6", DeviceModel: "BitAxe Ultra", ASICModel: "BM1366", ASICCount: 1},
			want: MinerClassLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDevice(tt.hint); got != tt.want {
				t.Errorf("got %s, want %s", got.String(), tt.want.String())
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Class plumbing
// -----------------------------------------------------------------------------

// TestNerdNOSClassPlumbing checks the new class is wired through everything a class
// is expected to support, rather than only existing in the profile map.
func TestNerdNOSClassPlumbing(t *testing.T) {
	if got := MinerClassNerdNOS.String(); got != "nerdnos" {
		t.Errorf("String(): got %q, want %q", got, "nerdnos")
	}

	// Vendor() must not claim this as an Avalon or Bitmain device.
	if got := MinerClassNerdNOS.Vendor(); got != "generic" {
		t.Errorf("Vendor(): got %q, want %q", got, "generic")
	}
	if MinerClassNerdNOS.IsAvalon() {
		t.Error("IsAvalon() must be false")
	}

	// The profile must be present rather than silently falling back to Unknown.
	router := NewSpiralRouter()
	profile := router.GetProfile(MinerClassNerdNOS)
	if profile.Class != MinerClassNerdNOS {
		t.Errorf("GetProfile fell back to %s", profile.Class.String())
	}

	// SHA-256d only: a BM1397 cannot mine Scrypt, so the Scrypt lookup is expected to
	// fall back rather than carry a bogus dedicated profile.
	scrypt := router.GetProfileForAlgorithm(MinerClassNerdNOS, AlgorithmScrypt)
	if scrypt.Class == MinerClassNerdNOS {
		t.Error("unexpected dedicated Scrypt profile for a SHA-256d-only ASIC")
	}
}

// TestNerdNOSSitsBetweenLotteryAndLow verifies the class occupies the gap it was
// created for, rather than duplicating a neighbour.
func TestNerdNOSSitsBetweenLotteryAndLow(t *testing.T) {
	router := NewSpiralRouter()

	lottery := router.GetProfile(MinerClassLottery)
	nerdnos := router.GetProfile(MinerClassNerdNOS)
	low := router.GetProfile(MinerClassLow)

	if !(lottery.InitialDiff < nerdnos.InitialDiff && nerdnos.InitialDiff < low.InitialDiff) {
		t.Errorf("InitialDiff not ordered lottery < nerdnos < low: %v, %v, %v",
			lottery.InitialDiff, nerdnos.InitialDiff, low.InitialDiff)
	}

	// The reason it is not simply MinerClassLow: Low pins MinDiff to InitialDiff for
	// ~500 GH/s hardware, so a 150-200 GH/s board would land on that floor and stay.
	if low.MinDiff != low.InitialDiff {
		t.Fatal("MinerClassLow no longer pins MinDiff to InitialDiff - revisit whether NerdNOS still needs its own class")
	}
	if nerdnos.MinDiff >= nerdnos.InitialDiff {
		t.Error("NerdNOS must keep room below InitialDiff so slower clocks can descend")
	}
}

func assertClose(t *testing.T, field string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > math.Abs(want)*1e-9 {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
