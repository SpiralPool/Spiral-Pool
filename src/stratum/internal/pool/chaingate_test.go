package pool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// The chain gate is the last thing between a miner and a chain whose blocks are
// worth nothing. internal/daemon covers the verdict matrix; what is covered here
// is the orchestration around it in CoinPool, which nothing else reaches --
// no test constructs a CoinPool far enough to call Start().
//
// The distinction these tests protect: an UNREACHABLE daemon is retried, because
// the sync gate immediately above has already proved the daemon answers RPC, so
// a failure here means a transient blip. A minority or stale verdict is
// conclusive and must fail on the first attempt -- retrying it would delay a
// refusal that is already correct.

// chainGateNodeMgr drives verifyChainIdentity. It counts calls so a test can
// assert how many attempts were made.
type chainGateNodeMgr struct {
	mockNodeMgr

	calls          int
	failFirstN     int // return a transport error for this many calls
	chainName      string
	blocks         uint64
	mediantimeAgo  time.Duration
	splitBlockHash string
	hangFor        time.Duration
}

func (m *chainGateNodeMgr) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	m.calls++
	if m.hangFor > 0 {
		select {
		case <-time.After(m.hangFor):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.calls <= m.failFirstN {
		return nil, fmt.Errorf("connection refused")
	}
	chain := m.chainName
	if chain == "" {
		chain = "main"
	}
	return &daemon.BlockchainInfo{
		Chain:      chain,
		Blocks:     m.blocks,
		MedianTime: time.Now().Add(-m.mediantimeAgo).Unix(),
	}, nil
}

func (m *chainGateNodeMgr) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	if m.splitBlockHash == "" {
		return "", fmt.Errorf("no block at %d", height)
	}
	return m.splitBlockHash, nil
}

func (m *chainGateNodeMgr) GetDeploymentInfo(ctx context.Context) (*daemon.DeploymentInfo, error) {
	return &daemon.DeploymentInfo{Deployments: nil}, nil
}

func newChainGatePool(t *testing.T, mgr *chainGateNodeMgr, allowNonMajority bool) *CoinPool {
	t.Helper()
	return &CoinPool{
		coinSymbol:  "BTC",
		logger:      zap.NewNop().Sugar(),
		nodeManager: mgr,
		cfg:         &config.CoinPoolConfig{AllowNonMajorityChain: allowNonMajority},
	}
}

// shortenChainCheckRetries makes the retry loop finish in milliseconds instead
// of its production budget, and restores the real values afterwards.
func shortenChainCheckRetries(t *testing.T) {
	t.Helper()
	attempts, timeout, backoff := chainCheckAttempts, chainCheckTimeout, chainCheckBackoff
	chainCheckTimeout = 200 * time.Millisecond
	chainCheckBackoff = 1 * time.Millisecond
	t.Cleanup(func() {
		chainCheckAttempts, chainCheckTimeout, chainCheckBackoff = attempts, timeout, backoff
	})
}

func TestChainGateRetriesOnlyWhenDaemonUnreachable(t *testing.T) {
	shortenChainCheckRetries(t)

	t.Run("transient unreachability recovers and is allowed", func(t *testing.T) {
		mgr := &chainGateNodeMgr{
			failFirstN:     2, // blips, then answers
			blocks:         daemon.BitcoinSplitHeight + 500,
			splitBlockHash: daemon.BitcoinMajorityBlock961632,
			mediantimeAgo:  time.Minute,
		}
		if err := newChainGatePool(t, mgr, false).verifyChainIdentity(context.Background()); err != nil {
			t.Fatalf("a daemon that answers on the third attempt must be allowed to mine: %v", err)
		}
		if mgr.calls != 3 {
			t.Errorf("expected 3 attempts, got %d", mgr.calls)
		}
	})

	t.Run("a minority verdict is conclusive and is NOT retried", func(t *testing.T) {
		mgr := &chainGateNodeMgr{
			blocks:         daemon.BitcoinSplitHeight + 500,
			splitBlockHash: daemon.BitcoinRDTSBlock961632,
			mediantimeAgo:  time.Minute,
		}
		err := newChainGatePool(t, mgr, false).verifyChainIdentity(context.Background())
		if err == nil {
			t.Fatal("mining the BIP-110 minority chain must be refused")
		}
		if mgr.calls != 1 {
			t.Errorf("a conclusive verdict must not be retried; got %d attempts", mgr.calls)
		}
	})

	t.Run("a persistently unreachable daemon gives up and blocks", func(t *testing.T) {
		mgr := &chainGateNodeMgr{failFirstN: 1000}
		err := newChainGatePool(t, mgr, false).verifyChainIdentity(context.Background())
		if err == nil {
			t.Fatal("an unverifiable chain must block mining, not default to allowed")
		}
		if mgr.calls != chainCheckAttempts {
			t.Errorf("expected %d attempts, got %d", chainCheckAttempts, mgr.calls)
		}
	})
}

func TestChainGateSkipsNonBitcoinCoins(t *testing.T) {
	shortenChainCheckRetries(t)
	mgr := &chainGateNodeMgr{failFirstN: 1000}
	cp := newChainGatePool(t, mgr, false)
	cp.coinSymbol = "LTC"
	if err := cp.verifyChainIdentity(context.Background()); err != nil {
		t.Fatalf("the gate is Bitcoin-only: %v", err)
	}
	if mgr.calls != 0 {
		t.Errorf("a non-Bitcoin coin must not query the daemon at all; got %d calls", mgr.calls)
	}
}

// The V1 pool gained the same gate, because cmd/spiralpool falls back to V1
// automatically (with only a WARN) when the V2 config fails to load — so a YAML
// typo would otherwise disable the chain check entirely.
//
// V1's daemonClient is a concrete *daemon.Client, so the verdict path cannot be
// faked here; internal/daemon covers that matrix directly. What IS testable, and
// what this change could plausibly break, is which coins the gate engages for.
//
// CRITICAL: these must be real cfg.Pool.Coin values, i.e. keys of
// config.SupportedCoins. That field holds a coin NAME ("bitcoin"), not a symbol
// ("BTC"), and Validate() rejects anything else. An earlier version of this test
// fed "BTC"/"btc" — values that can never appear in a validated config — and so
// passed against a gate that returned early for every real input, i.e. against a
// gate that did nothing at all. Test the values that actually occur.
func TestV1ChainGateSkipsNonBitcoinCoins(t *testing.T) {
	for _, coin := range []string{"litecoin", "dogecoin", "digibyte", "digibyte-scrypt", ""} {
		p := &Pool{logger: zap.NewNop().Sugar()}
		p.cfg = &config.Config{}
		p.cfg.Pool.Coin = coin

		// A nil daemonClient makes the point sharply: if the gate did not return
		// early for a non-Bitcoin coin, this would panic rather than pass.
		if err := p.verifyChainIdentity(context.Background()); err != nil {
			t.Errorf("coin %q must not be gated on Bitcoin's split: %v", coin, err)
		}
	}
}

func TestV1ChainGateEngagesForBitcoin(t *testing.T) {
	// The values a real config can hold for Bitcoin. install.sh writes
	// coin: "bitcoin" (install.sh:23557); GetCoinInfo maps that to Symbol "BTC".
	// Case variants are included because cfg.Pool.Coin is operator-written and
	// GetCoinInfo lower-cases before lookup, so all three are valid configs.
	for _, coin := range []string{"bitcoin", "Bitcoin", "BITCOIN"} {
		p := &Pool{logger: zap.NewNop().Sugar()}
		p.cfg = &config.Config{}
		p.cfg.Pool.Coin = coin

		// Reaching the daemon call with a nil client panics; recover and treat
		// that as proof the gate did NOT skip. Anything else means it returned
		// early, which for BTC is the bug.
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("coin %q must be gated, but the check returned early", coin)
				}
			}()
			_ = p.verifyChainIdentity(context.Background())
		}()
	}
}

// Pins the mapping the gate depends on. If "btc" ever became a valid config
// value, or SupportedCoins["bitcoin"].Symbol changed, the gate would silently
// stop engaging — which is precisely how it was broken before.
func TestV1CoinNameResolvesToSymbol(t *testing.T) {
	if _, ok := config.SupportedCoins["btc"]; ok {
		t.Error(`"btc" is now a valid config coin — the gate's name/symbol assumption needs revisiting`)
	}
	info, ok := config.SupportedCoins["bitcoin"]
	if !ok {
		t.Fatal(`SupportedCoins has no "bitcoin" key — the V1 gate cannot resolve Bitcoin`)
	}
	if info.Symbol != "BTC" {
		t.Errorf(`SupportedCoins["bitcoin"].Symbol = %q, want "BTC"`, info.Symbol)
	}
}

// Both spellings of the override key must be honoured: V1 config is camelCase,
// but every document names the V2 snake_case key, and a non-strict unmarshal
// discards an unrecognised spelling in silence.
func TestV1OverrideAcceptsBothKeySpellings(t *testing.T) {
	var camel, snake config.PoolConfig
	camel.AllowNonMajorityChain = true
	snake.AllowNonMajorityChainSnake = true

	if !camel.AllowsNonMajorityChain() {
		t.Error("allowNonMajorityChain (camelCase, V1 convention) was ignored")
	}
	if !snake.AllowsNonMajorityChain() {
		t.Error("allow_nonmajority_chain (snake_case, as documented) was ignored")
	}
	var none config.PoolConfig
	if none.AllowsNonMajorityChain() {
		t.Error("override defaulted to enabled — it must fail closed")
	}
}

func TestChainGateAllowsNonMainnet(t *testing.T) {
	shortenChainCheckRetries(t)
	// Block 961,632 is a mainnet constant. A regtest chain sitting at height
	// ~100 can never reach it, so gating on it would refuse to start a
	// perfectly correct node, permanently.
	mgr := &chainGateNodeMgr{chainName: "regtest", blocks: 100, mediantimeAgo: time.Minute}
	if err := newChainGatePool(t, mgr, false).verifyChainIdentity(context.Background()); err != nil {
		t.Fatalf("regtest must not be gated on a mainnet block: %v", err)
	}
}

func TestChainGateOverrideAllowsMiningAndAlsoStaleTip(t *testing.T) {
	shortenChainCheckRetries(t)

	t.Run("override permits the minority chain", func(t *testing.T) {
		mgr := &chainGateNodeMgr{
			blocks:         daemon.BitcoinSplitHeight + 500,
			splitBlockHash: daemon.BitcoinRDTSBlock961632,
			mediantimeAgo:  time.Minute,
		}
		if err := newChainGatePool(t, mgr, true).verifyChainIdentity(context.Background()); err != nil {
			t.Fatalf("allow_nonmajority_chain must permit mining: %v", err)
		}
	})

	// The second, easy-to-miss effect documented on the config field: the
	// override also disables the stale-tip refusal.
	t.Run("override also suppresses the stale-tip refusal", func(t *testing.T) {
		mgr := &chainGateNodeMgr{
			blocks:         daemon.BitcoinSplitHeight + 500,
			splitBlockHash: daemon.BitcoinMajorityBlock961632,
			mediantimeAgo:  48 * time.Hour,
		}
		if err := newChainGatePool(t, mgr, false).verifyChainIdentity(context.Background()); err == nil {
			t.Fatal("a tip 48h old must block mining without the override")
		}
		if err := newChainGatePool(t, mgr, true).verifyChainIdentity(context.Background()); err != nil {
			t.Fatalf("the override must also suppress the stale-tip refusal: %v", err)
		}
	})
}

func TestChainGateHonoursContextCancellation(t *testing.T) {
	shortenChainCheckRetries(t)
	chainCheckBackoff = 5 * time.Second // long enough that cancellation must win

	mgr := &chainGateNodeMgr{failFirstN: 1000}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := newChainGatePool(t, mgr, false).verifyChainIdentity(ctx); err == nil {
		t.Fatal("expected an error when the context is cancelled mid-retry")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation must interrupt the backoff; took %s", elapsed)
	}
}

// The retry budget is bounded by the coordinator, which allows each pool's
// Start() 90 seconds before calling Stop() -- and Stop() blocks on the same
// mutex Start() holds, so overrunning stalls every other coin's startup, not
// just Bitcoin's. A hanging (not refusing) daemon burns the full per-attempt
// timeout each time, which is the worst case.
func TestChainGateWorstCaseFitsCoordinatorStartBudget(t *testing.T) {
	// The gate is not the only consumer of that window: waitForSync runs first
	// and polls on a 10s interval, so budget the gate against what is actually
	// left rather than the full 90s.
	const (
		coordinatorStartBudget = 90 * time.Second
		syncGateHeadroom       = 10 * time.Second
	)
	budget := coordinatorStartBudget - syncGateHeadroom

	worst := time.Duration(chainCheckAttempts)*chainCheckTimeout +
		time.Duration(chainCheckAttempts-1)*chainCheckBackoff

	if worst >= budget {
		t.Fatalf("chain gate worst case %s exceeds the %s left of the coordinator's "+
			"%s Start() budget; a hanging daemon would stall startup for every other coin",
			worst, budget, coordinatorStartBudget)
	}
}
