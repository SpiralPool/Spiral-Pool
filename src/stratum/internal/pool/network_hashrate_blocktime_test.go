package pool

import (
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
)

// The network-hashrate calculation divides by getAlgoBlockTime, which keys off a
// ticker. Feeding it anything else is a silent failure rather than an error: the
// switch falls through to a 600s default and the result is simply wrong.
//
// That trap is easy to walk into because the coin registry accepts config *names*
// as aliases ("digibyte" -> DGB), so passing cfg.Pool.Coin compiles, runs, and
// produces a plausible number. It shipped exactly that way: DGB reported
// difficulty * 2^32 / 600 for a chain that produces a SHA256d block every 75s —
// an 8x overstatement served from /api/pools to the dashboard, spiralctl and
// Sentinel, with no error anywhere. The call site now passes coinImpl.Symbol().
func TestAlgoBlockTimeMatchesCoinSymbol(t *testing.T) {
	t.Parallel()

	cases := []struct {
		configName    string // what cfg.Pool.Coin holds
		wantSymbol    string
		wantBlockTime float64
	}{
		{"digibyte", "DGB", 75},
		{"digibyte-scrypt", "DGB-SCRYPT", 75},
		{"bitcoin", "BTC", 600},
		{"litecoin", "LTC", 150},
		{"dogecoin", "DOGE", 60},
	}

	for _, tc := range cases {
		t.Run(tc.configName, func(t *testing.T) {
			t.Parallel()
			impl, err := coin.Create(tc.configName)
			if err != nil {
				t.Fatalf("coin.Create(%q): %v", tc.configName, err)
			}
			if got := impl.Symbol(); got != tc.wantSymbol {
				t.Fatalf("Symbol() = %q, want %q", got, tc.wantSymbol)
			}
			if got := getAlgoBlockTime(impl.Symbol()); got != tc.wantBlockTime {
				t.Errorf("getAlgoBlockTime(Symbol()=%q) = %v, want %v",
					impl.Symbol(), got, tc.wantBlockTime)
			}
		})
	}
}

// Pins the trap itself: a config coin name resolves to the 600s default rather
// than DigiByte's real 75s. If getAlgoBlockTime is ever taught to accept names,
// delete this test rather than "fixing" it — at that point the call site no
// longer needs Symbol() and the hazard is gone.
func TestAlgoBlockTimeSilentlyDefaultsOnCoinName(t *testing.T) {
	t.Parallel()

	if got := getAlgoBlockTime("digibyte"); got != 600 {
		t.Errorf("getAlgoBlockTime(%q) = %v; expected the 600s default", "digibyte", got)
	}
	if got := getAlgoBlockTime("DGB"); got != 75 {
		t.Errorf("getAlgoBlockTime(%q) = %v, want 75", "DGB", got)
	}
}
