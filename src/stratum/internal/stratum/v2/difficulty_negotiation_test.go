// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"math"
	"testing"
)

// Where a V2 channel's difficulty comes from.
//
// The V1 path reads the `mining.subscribe` user agent and hands it to the Spiral
// Router, so a lottery miner and an S21 do not start on the same number. V2 has
// no user agent, and every channel opened at the single figure in config — the
// exact failure the router exists to prevent, reintroduced on the newer protocol.
// A 4.9 TH/s NerdQAxe++ met that in production.
//
// V2 does carry something better than a user agent: OpenMiningChannel states the
// device's own nominal hashrate. These pin that it is used, that the vendor
// string is still offered as a fallback for a miner that declares no hashrate,
// and — the part that is easy to break — that none of it overrides the miner's
// own max_target floor, which the SV2 specification requires the pool to respect.

func openChannelForDifficulty(t *testing.T, srv *Server, open OpenStandardMiningChannel) *OpenStandardMiningChannelSuccess {
	t.Helper()
	client := dialSV2(t, srv)
	client.setup()
	msg, err := EncodeOpenStandardMiningChannel(&open)
	if err != nil {
		t.Fatal(err)
	}
	client.send(msg)
	success, err := DecodeOpenStandardMiningChannelSuccess(client.expect(MsgOpenStandardMiningChannelSuccess))
	if err != nil {
		t.Fatal(err)
	}
	return success
}

func TestSV2Difficulty_UsesWhatTheMinerDeclares(t *testing.T) {
	pool := newSV2TestPool()
	var gotVendor string
	var gotHashrate float64
	srv, _ := startSV2TestServer(t, pool, func(c *ServerConfig) {
		c.InitialDifficulty = 50000 // the one-size-fits-all number, deliberately wrong here
		c.InitialDifficultyFor = func(vendor string, hashrate float64) float64 {
			gotVendor, gotHashrate = vendor, hashrate
			return 7
		}
	})

	success := openChannelForDifficulty(t, srv,
		OpenStandardMiningChannel{RequestID: 1, UserIdentity: "miner.rig1", NominalHashRate: 5e12})

	if gotVendor != "sv2-test" {
		t.Errorf("vendor = %q, want the SetupConnection vendor %q", gotVendor, "sv2-test")
	}
	// nominal_hash_rate crosses the wire as an f32, so compare within its precision.
	if math.Abs(gotHashrate-5e12)/5e12 > 1e-6 {
		t.Errorf("nominal hashrate = %v, want ~5e12", gotHashrate)
	}
	if want := targetToU256(pool.validator.ShareTarget(7)); success.Target != want {
		t.Errorf("channel opened at the config difficulty, not the declared one\n got %x\nwant %x",
			success.Target, want)
	}
}

func TestSV2Difficulty_MinerMaxTargetStillWins(t *testing.T) {
	// The pool offering something easier than the device will accept must not
	// override it: SV2 says the pool MUST respect max_target. This is the
	// assertion that stops difficulty selection being "improved" into a
	// specification violation.
	pool := newSV2TestPool()
	maxTarget := NBitsToU256(0x1d00ffff)
	srv, _ := startSV2TestServer(t, pool, func(c *ServerConfig) {
		c.InitialDifficultyFor = func(string, float64) float64 { return 1e-9 }
	})

	success := openChannelForDifficulty(t, srv, OpenStandardMiningChannel{
		RequestID: 2, UserIdentity: "miner.rig1", NominalHashRate: 1e6, MaxTarget: maxTarget,
	})

	if success.Target != maxTarget {
		t.Errorf("the miner's max_target floor was overridden\n got %x\nwant %x",
			success.Target, maxTarget)
	}
}

func TestSV2Difficulty_NoOpinionKeepsTheConfiguredValue(t *testing.T) {
	// A miner that declares no hashrate and matches no vendor profile: the hook
	// says 0, meaning "no opinion", and config must still decide.
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool, func(c *ServerConfig) {
		c.InitialDifficulty = 512
		c.InitialDifficultyFor = func(string, float64) float64 { return 0 }
	})

	success := openChannelForDifficulty(t, srv,
		OpenStandardMiningChannel{RequestID: 3, UserIdentity: "miner.rig1", NominalHashRate: 1e6})

	if want := targetToU256(pool.validator.ShareTarget(512)); success.Target != want {
		t.Errorf("a hook with no opinion changed the difficulty\n got %x\nwant %x",
			success.Target, want)
	}
}

func TestSV2Difficulty_UnsetHookIsUnchangedBehaviour(t *testing.T) {
	// A pool that never wires the hook — every deployment before this change, and
	// the V1-only path — must behave exactly as it did.
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool, func(c *ServerConfig) {
		c.InitialDifficulty = 256
	})

	success := openChannelForDifficulty(t, srv,
		OpenStandardMiningChannel{RequestID: 4, UserIdentity: "miner.rig1", NominalHashRate: 1e6})

	if want := targetToU256(pool.validator.ShareTarget(256)); success.Target != want {
		t.Errorf("behaviour changed for a pool that wires no hook\n got %x\nwant %x",
			success.Target, want)
	}
}
