// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fakeInspector lets each test drive the three RPCs independently, including
// making one fail while the others succeed.
type fakeInspector struct {
	deployments    map[string]json.RawMessage
	deploymentErr  error
	blockHash      string
	blockHashErr   error
	chainInfo      *BlockchainInfo
	chainInfoErr   error
	blockHashCalls int
}

func (f *fakeInspector) GetDeploymentInfo(ctx context.Context) (*DeploymentInfo, error) {
	if f.deploymentErr != nil {
		return nil, f.deploymentErr
	}
	return &DeploymentInfo{Deployments: f.deployments}, nil
}

func (f *fakeInspector) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	f.blockHashCalls++
	if f.blockHashErr != nil {
		return "", f.blockHashErr
	}
	return f.blockHash, nil
}

func (f *fakeInspector) GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error) {
	if f.chainInfoErr != nil {
		return nil, f.chainInfoErr
	}
	return f.chainInfo, nil
}

// now is a fixed clock; tip times are expressed relative to it.
var now = time.Unix(1_755_000_000, 0)

func freshTip(height uint64) *BlockchainInfo {
	return &BlockchainInfo{Chain: "main", Blocks: height, MedianTime: now.Add(-10 * time.Minute).Unix()}
}

// The gate must not fire on any chain other than Bitcoin mainnet. Block 961,632
// is a mainnet constant; a regtest chain sits at height ~100 and would otherwise
// be permanently "below the split height", refusing to start a correct node.
func TestNonMainnetChainsAreNotGated(t *testing.T) {
	for _, chain := range []string{"regtest", "test", "signet"} {
		t.Run(chain, func(t *testing.T) {
			f := &fakeInspector{
				chainInfo: &BlockchainInfo{
					Chain:      chain,
					Blocks:     101, // far below the mainnet split height
					MedianTime: now.Add(-10 * time.Minute).Unix(),
				},
			}

			cc := CheckBitcoinChain(context.Background(), f, now)
			if cc.Verdict != VerdictNotApplicable {
				t.Fatalf("chain %q: expected VerdictNotApplicable, got %q (%s)", chain, cc.Verdict, cc.Reason)
			}
			if f.blockHashCalls != 0 {
				t.Fatalf("chain %q: must not query block %d off mainnet", chain, BitcoinSplitHeight)
			}
			if allowed, reason := cc.AllowMining(false); !allowed {
				t.Fatalf("chain %q: mining must be allowed without an override: %s", chain, reason)
			}
		})
	}
}

// A daemon enforcing RDTS must be refused, because it follows the minority chain
// by construction.
func TestDeploymentInfoWithReducedDataTriggersRefusal(t *testing.T) {
	f := &fakeInspector{
		deployments: map[string]json.RawMessage{
			"segwit":       json.RawMessage(`{}`),
			"reduced_data": json.RawMessage(`{"type":"bip9"}`),
		},
		blockHash: BitcoinMajorityBlock961632, // even holding the majority block
		chainInfo: freshTip(BitcoinSplitHeight + 900),
	}

	cc := CheckBitcoinChain(context.Background(), f, now)
	if !cc.RDTSEnforcing {
		t.Fatal("expected RDTSEnforcing=true when reduced_data deployment is present")
	}
	if cc.Verdict != VerdictMinority {
		t.Fatalf("expected VerdictMinority, got %q (%s)", cc.Verdict, cc.Reason)
	}

	allowed, reason := cc.AllowMining(false)
	if allowed {
		t.Fatalf("expected mining refused for an RDTS-enforcing daemon, got allowed: %s", reason)
	}
}

// The normal healthy case: no reduced_data, majority block, fresh tip.
func TestDeploymentInfoWithoutReducedDataPasses(t *testing.T) {
	f := &fakeInspector{
		deployments: map[string]json.RawMessage{
			"segwit":  json.RawMessage(`{}`),
			"taproot": json.RawMessage(`{}`),
		},
		blockHash: BitcoinMajorityBlock961632,
		chainInfo: freshTip(BitcoinSplitHeight + 900),
	}

	cc := CheckBitcoinChain(context.Background(), f, now)
	if cc.RDTSEnforcing {
		t.Fatal("expected RDTSEnforcing=false when reduced_data is absent")
	}
	if cc.Verdict != VerdictMajority {
		t.Fatalf("expected VerdictMajority, got %q (%s)", cc.Verdict, cc.Reason)
	}

	if allowed, reason := cc.AllowMining(false); !allowed {
		t.Fatalf("expected mining allowed on a healthy majority-chain node: %s", reason)
	}
}

// A hash mismatch at the split height is the authoritative wrong-chain signal.
func TestBlockHashMismatchTriggersRefusal(t *testing.T) {
	cases := []struct {
		name string
		hash string
	}{
		{"known RDTS chain", BitcoinRDTSBlock961632},
		{"unrecognised branch", "00000000000000000000deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdead"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInspector{
				deployments: map[string]json.RawMessage{"segwit": json.RawMessage(`{}`)},
				blockHash:   tc.hash,
				chainInfo:   freshTip(BitcoinSplitHeight + 4),
			}

			cc := CheckBitcoinChain(context.Background(), f, now)
			if cc.Verdict != VerdictMinority {
				t.Fatalf("expected VerdictMinority for hash %s, got %q", tc.hash, cc.Verdict)
			}
			if cc.Unreachable {
				t.Fatal("a hash mismatch is a real verdict, not an unreachable daemon")
			}
			if allowed, _ := cc.AllowMining(false); allowed {
				t.Fatal("expected mining refused on a hash mismatch")
			}
		})
	}
}

// A stale tip on the majority chain means the node is stalled or desynced.
func TestStaleTipBlocksMining(t *testing.T) {
	f := &fakeInspector{
		deployments: map[string]json.RawMessage{"segwit": json.RawMessage(`{}`)},
		blockHash:   BitcoinMajorityBlock961632,
		chainInfo: &BlockchainInfo{
			Blocks:     BitcoinSplitHeight + 900,
			MedianTime: now.Add(-6 * time.Hour).Unix(),
		},
	}

	cc := CheckBitcoinChain(context.Background(), f, now)
	if !cc.TipStale {
		t.Fatalf("expected TipStale=true for a 6h-old tip, got age %s", cc.TipAge)
	}
	// Chain identity is still correct — the verdict should say so.
	if cc.Verdict != VerdictMajority {
		t.Fatalf("expected VerdictMajority (identity is fine, freshness is not), got %q", cc.Verdict)
	}
	if allowed, _ := cc.AllowMining(false); allowed {
		t.Fatal("expected mining refused while the tip is stale")
	}
}

// The override lets a determined operator proceed, and says so loudly.
func TestOverridePermitsStartupWithWarning(t *testing.T) {
	f := &fakeInspector{
		deployments: map[string]json.RawMessage{"reduced_data": json.RawMessage(`{}`)},
		blockHash:   BitcoinRDTSBlock961632,
		chainInfo:   freshTip(BitcoinSplitHeight + 4),
	}

	cc := CheckBitcoinChain(context.Background(), f, now)
	allowed, reason := cc.AllowMining(true)
	if !allowed {
		t.Fatalf("expected override to permit mining, got refusal: %s", reason)
	}
	if !contains(reason, "allow_nonmajority_chain") {
		t.Fatalf("override reason must name the setting responsible, got: %s", reason)
	}
}

// The case that matters most for operator trust: an unreachable daemon must NOT
// be reported as a wrong-chain daemon. Conflating the two teaches operators to
// ignore the alarm that actually means something.
func TestRPCFailureIsDistinctFromMinorityVerdict(t *testing.T) {
	t.Run("getblockchaininfo fails", func(t *testing.T) {
		f := &fakeInspector{chainInfoErr: errors.New("connection refused")}

		cc := CheckBitcoinChain(context.Background(), f, now)
		if cc.Verdict != VerdictUnknown {
			t.Fatalf("expected VerdictUnknown on RPC failure, got %q", cc.Verdict)
		}
		if cc.Verdict == VerdictMinority {
			t.Fatal("an unreachable daemon must never be reported as wrong-chain")
		}
		if !cc.Unreachable {
			t.Fatal("expected Unreachable=true on RPC failure")
		}
		if f.blockHashCalls != 0 {
			t.Fatal("should not attempt getblockhash once the daemon is known unreachable")
		}
		// Still fails closed.
		if allowed, _ := cc.AllowMining(false); allowed {
			t.Fatal("expected mining refused when chain identity is unknown")
		}
	})

	t.Run("getblockhash fails", func(t *testing.T) {
		f := &fakeInspector{
			deployments:  map[string]json.RawMessage{"segwit": json.RawMessage(`{}`)},
			blockHashErr: errors.New("timeout"),
			chainInfo:    freshTip(BitcoinSplitHeight + 900),
		}

		cc := CheckBitcoinChain(context.Background(), f, now)
		if cc.Verdict != VerdictUnknown || !cc.Unreachable {
			t.Fatalf("expected unknown+unreachable, got %q unreachable=%v", cc.Verdict, cc.Unreachable)
		}
	})

	// getdeploymentinfo failing alone must NOT prevent a verdict: chain identity
	// is decidable from the block hash, which is the stronger signal.
	t.Run("getdeploymentinfo fails but chain is still decidable", func(t *testing.T) {
		f := &fakeInspector{
			deploymentErr: errors.New("method not found"),
			blockHash:     BitcoinMajorityBlock961632,
			chainInfo:     freshTip(BitcoinSplitHeight + 900),
		}

		cc := CheckBitcoinChain(context.Background(), f, now)
		if cc.DeploymentKnown {
			t.Fatal("expected DeploymentKnown=false when getdeploymentinfo errors")
		}
		if cc.Verdict != VerdictMajority {
			t.Fatalf("expected the block hash to still decide the verdict, got %q", cc.Verdict)
		}
		if allowed, _ := cc.AllowMining(false); !allowed {
			t.Fatal("expected mining allowed: identity verified via block hash")
		}
	})
}

// A node still syncing is unknown, never a pass.
func TestNodeBelowSplitHeightIsUnknownNotPass(t *testing.T) {
	f := &fakeInspector{
		deployments: map[string]json.RawMessage{"segwit": json.RawMessage(`{}`)},
		chainInfo:   freshTip(BitcoinSplitHeight - 1000),
	}

	cc := CheckBitcoinChain(context.Background(), f, now)
	if cc.Verdict != VerdictUnknown {
		t.Fatalf("expected VerdictUnknown below the split height, got %q", cc.Verdict)
	}
	if f.blockHashCalls != 0 {
		t.Fatal("should not query a block the node does not have yet")
	}
	if allowed, _ := cc.AllowMining(false); allowed {
		t.Fatal("a syncing node must not be reported as verified")
	}
}

// Enforcement is conclusive even before the node reaches the split height.
func TestEnforcingDaemonBelowSplitHeightIsStillMinority(t *testing.T) {
	f := &fakeInspector{
		deployments: map[string]json.RawMessage{"reduced_data": json.RawMessage(`{}`)},
		chainInfo:   freshTip(BitcoinSplitHeight - 1000),
	}

	cc := CheckBitcoinChain(context.Background(), f, now)
	if cc.Verdict != VerdictMinority {
		t.Fatalf("expected VerdictMinority for an enforcing daemon, got %q", cc.Verdict)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
