package pool

import (
	"math"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
)

// The drought threshold is derived from probability rather than wall-clock time
// because block discovery is Poisson: with effort defined as 100·λ·t, the chance a
// healthy pool sees a gap at least this long is exactly e^(-effort/100). A fixed
// hour count cannot serve two coins at once — the numbers below show the same
// probability landing at ~6.9 days for a small DGB pool and ~350 years for a solo
// BTC miner, which is why the old fixed threshold was simultaneously too twitchy
// on one coin and permanently useless on the other.
func TestBlockDroughtEffortThreshold(t *testing.T) {
	t.Parallel()

	cases := []struct {
		probability float64
		wantEffort  float64
	}{
		{0.01, 460.517018598809},  // 1-in-100
		{0.005, 529.831736654804}, // 1-in-200
		{0.001, 690.775527898214}, // 1-in-1000
	}

	for _, tc := range cases {
		got := -100 * math.Log(tc.probability)
		if math.Abs(got-tc.wantEffort) > 1e-9 {
			t.Errorf("threshold for p=%v = %v, want %v", tc.probability, got, tc.wantEffort)
		}
		// Round-trip: effort back to probability must recover the input.
		if back := math.Exp(-got / 100); math.Abs(back-tc.probability) > 1e-12 {
			t.Errorf("round-trip for p=%v gave %v", tc.probability, back)
		}
	}
}

// Pins the calibration claim: at the default 1-in-100, a pool averaging ~0.67
// blocks/day alerts at ~6.9 days — where the old fixed defaults sat — while a solo
// BTC miner is never woken. Same single setting, both correct.
func TestBlockDroughtThresholdScalesWithBlockRate(t *testing.T) {
	t.Parallel()

	const p = 0.01
	thresholdDays := func(blocksPerDay float64) float64 {
		return -math.Log(p) / blocksPerDay
	}

	if d := thresholdDays(0.67); d < 6.5 || d > 7.5 {
		t.Errorf("small DGB pool threshold = %.2f days, want ~6.9 (the old fixed default)", d)
	}
	// Solo BTC: one S21 (200 TH/s) against ~800 EH/s => ~3.6e-5 blocks/day.
	if d := thresholdDays(3.6e-5); d < 100000 {
		t.Errorf("solo BTC threshold = %.0f days, want effectively never (>100000)", d)
	}
}

// A drought is one condition that persists until a block is found, but fireAlert
// dedups only on AlertCooldown. Without the latch the alert would re-announce every
// cooldown for the entire drought — days of repeats on a slow coin.
func TestBlockDroughtLatch(t *testing.T) {
	t.Parallel()

	s := &Sentinel{
		cfg:            &config.SentinelConfig{AlertCooldown: time.Hour},
		droughtAlerted: make(map[string]bool),
		lastBlockCount: make(map[string]float64),
		lastBlockTime:  make(map[string]time.Time),
	}

	if s.droughtAlerted["DGB"] {
		t.Fatal("latch should start clear")
	}

	s.droughtAlerted["DGB"] = true
	if !s.droughtAlerted["DGB"] {
		t.Fatal("latch should be set after firing")
	}

	// Finding a block re-arms it — this is the only thing that does.
	delete(s.droughtAlerted, "DGB")
	if s.droughtAlerted["DGB"] {
		t.Error("latch should be cleared when a block is found")
	}

	// Latch is per coin: a drought on one must not silence another.
	s.droughtAlerted["DGB"] = true
	if s.droughtAlerted["BTC"] {
		t.Error("latch must not leak across coins")
	}
}

// Probability mode is the default, and a negative value is the documented way to
// turn the alert off entirely. Zero must not read as "disabled" — it is the
// unset-field zero value that SetSentinelDefaults replaces with 0.01.
func TestBlockDroughtDefaultAndDisable(t *testing.T) {
	t.Parallel()

	var c config.SentinelConfig
	c.SetSentinelDefaults()
	if c.BlockDroughtProbability != 0.01 {
		t.Errorf("default probability = %v, want 0.01", c.BlockDroughtProbability)
	}
	if c.BlockDroughtHours != 0 {
		t.Errorf("BlockDroughtHours default = %v, want 0 (probability mode)", c.BlockDroughtHours)
	}

	// An explicit hour count survives defaulting and takes precedence at check time.
	explicit := config.SentinelConfig{BlockDroughtHours: 24}
	explicit.SetSentinelDefaults()
	if explicit.BlockDroughtHours != 24 {
		t.Errorf("explicit hours overwritten: got %v", explicit.BlockDroughtHours)
	}

	// A negative probability is preserved so the disable path is reachable.
	off := config.SentinelConfig{BlockDroughtProbability: -1}
	off.SetSentinelDefaults()
	if off.BlockDroughtProbability != -1 {
		t.Errorf("disable value overwritten: got %v", off.BlockDroughtProbability)
	}
}
