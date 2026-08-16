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

// The sync gate decides whether miners are allowed to connect at all, and two
// of its failure modes are the kind an operator only discovers from missing
// revenue:
//
//   - Passing too early. `blocks >= headers` with no floor meant a daemon
//     reporting blocks=5, headers=5, IBD=false was declared "fully synced" and
//     mining began at height 5.
//
//   - Never giving up. Retrying an unreachable daemon forever held cp.runMu for
//     the whole of Start(), and the coordinator's 90s timeout calls Stop(),
//     which needs that same mutex — so Stop() blocked, startWg.Wait() never
//     returned, and NO coin mined. The IBD case already fast-failed for exactly
//     this reason; the unreachable case did not.

// syncGateNodeMgr scripts a daemon for waitForSync.
type syncGateNodeMgr struct {
	mockNodeMgr

	calls      int
	failFirstN int // transport error for this many calls
	failAll    bool
	info       *daemon.BlockchainInfo
}

func (m *syncGateNodeMgr) GetBlockchainInfo(ctx context.Context) (*daemon.BlockchainInfo, error) {
	m.calls++
	if m.failAll || m.calls <= m.failFirstN {
		return nil, fmt.Errorf("connection refused")
	}
	return m.info, nil
}

func newSyncGatePool(mgr *syncGateNodeMgr) *CoinPool {
	return &CoinPool{
		coinSymbol:  "BTC",
		logger:      zap.NewNop().Sugar(),
		nodeManager: mgr,
		cfg:         &config.CoinPoolConfig{},
	}
}

// The gate polls on a 10s ticker, so these tests need a deadline rather than
// patience. A context timeout distinguishes "returned a verdict" from "still
// looping", which is the property under test.
func runSyncGate(t *testing.T, cp *CoinPool, within time.Duration) (error, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- cp.waitForSync(ctx) }()

	select {
	case err := <-done:
		return err, true
	case <-time.After(within + 2*time.Second):
		return nil, false
	}
}

func TestSyncGateRejectsImplausiblyShortChain(t *testing.T) {
	// blocks == headers and IBD false, but at height 5. Before the floor this
	// passed and mining started.
	mgr := &syncGateNodeMgr{info: &daemon.BlockchainInfo{
		Chain: "main", Blocks: 5, Headers: 5,
		InitialBlockDownload: false, VerificationProgress: 0.0,
	}}
	err, returned := runSyncGate(t, newSyncGatePool(mgr), 25*time.Second)
	if returned && err == nil {
		t.Fatal("a daemon at height 5 must NOT pass the sync gate")
	}
	// Either it keeps waiting or it returns ctx.Err() — both are acceptable;
	// what must never happen is a nil (passed) verdict.
}

func TestSyncGateAcceptsAFullySyncedNode(t *testing.T) {
	mgr := &syncGateNodeMgr{info: &daemon.BlockchainInfo{
		Chain: "main", Blocks: 961632, Headers: 961632,
		InitialBlockDownload: false, VerificationProgress: 0.9999,
	}}
	err, returned := runSyncGate(t, newSyncGatePool(mgr), 25*time.Second)
	if !returned {
		t.Fatal("the gate should have returned for a synced node")
	}
	if err != nil {
		t.Fatalf("a fully synced mainnet node must pass: %v", err)
	}
}

// The blocks>=headers fallback exists for daemons that report a near-zero
// verificationprogress even when synced. It must still work above the floor.
func TestSyncGateFallbackStillWorksAtRealHeights(t *testing.T) {
	mgr := &syncGateNodeMgr{info: &daemon.BlockchainInfo{
		Chain: "main", Blocks: 900000, Headers: 900000,
		InitialBlockDownload: false, VerificationProgress: 0.0,
	}}
	err, returned := runSyncGate(t, newSyncGatePool(mgr), 25*time.Second)
	if !returned || err != nil {
		t.Fatalf("blocks>=headers at a real height must pass: returned=%v err=%v", returned, err)
	}
}

func TestSyncGateGivesUpOnAnUnreachableDaemon(t *testing.T) {
	// Must return an ERROR rather than looping. Looping holds cp.runMu, and the
	// coordinator's Stop() needs it — that deadlock stops every coin, not just
	// this one.
	mgr := &syncGateNodeMgr{failAll: true}
	err, returned := runSyncGate(t, newSyncGatePool(mgr), 90*time.Second)
	if !returned {
		t.Fatal("an unreachable daemon must produce a verdict, not loop forever " +
			"(looping deadlocks the coordinator via runMu)")
	}
	if err == nil {
		t.Fatal("an unreachable daemon must NOT pass the sync gate")
	}
}

// A daemon that blips and recovers must not be given up on — that is the whole
// reason the failure count is a threshold rather than one strike.
func TestSyncGateToleratesTransientRPCFailures(t *testing.T) {
	mgr := &syncGateNodeMgr{
		failFirstN: 2,
		info: &daemon.BlockchainInfo{
			Chain: "main", Blocks: 961632, Headers: 961632,
			InitialBlockDownload: false, VerificationProgress: 0.9999,
		},
	}
	err, returned := runSyncGate(t, newSyncGatePool(mgr), 60*time.Second)
	if !returned || err != nil {
		t.Fatalf("a daemon that recovers after 2 blips must pass: returned=%v err=%v", returned, err)
	}
	if mgr.calls < 3 {
		t.Errorf("expected retries before success, got %d calls", mgr.calls)
	}
}

func TestSyncGateFastFailsEarlyIBD(t *testing.T) {
	// Pre-existing behaviour, pinned here because the unreachable fast-fail was
	// modelled on it and they share the release path.
	mgr := &syncGateNodeMgr{info: &daemon.BlockchainInfo{
		Chain: "main", Blocks: 100, Headers: 961632,
		InitialBlockDownload: true, VerificationProgress: 0.10,
	}}
	err, returned := runSyncGate(t, newSyncGatePool(mgr), 25*time.Second)
	if !returned {
		t.Fatal("early IBD must fast-fail so the coordinator can retry the coin")
	}
	if err == nil {
		t.Fatal("a node at 10% IBD must not pass the sync gate")
	}
}
