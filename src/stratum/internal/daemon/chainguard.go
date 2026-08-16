// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package daemon

import (
	"context"
	"fmt"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// BITCOIN CHAIN IDENTITY GUARD
// ═══════════════════════════════════════════════════════════════════════════════
//
// On 2026-08-08 at block height 961,632 Bitcoin split. BIP-110 ("RDTS", the
// Reduced Data Temporary Softfork) entered a mandatory signalling window, and
// nodes enforcing it began rejecting any block that did not signal version bit
// 4. Roughly 99.85% of hashpower stayed on the majority chain. The enforcing
// minority chain has produced a handful of blocks and its coins are not traded.
//
// This matters because the failure is completely silent. A pool whose daemon
// follows the minority chain keeps working perfectly in every visible respect:
// stratum connects, shares validate, vardiff converges, the dashboard shows
// healthy hashrate. The only symptom is that blocks never arrive, which for a
// solo miner is indistinguishable from ordinary variance. Expected value is
// exactly zero and the operator has no way to find out.
//
// Hence a startup gate rather than a documentation note.

const (
	// BitcoinSplitHeight is the height at which the BIP-110 chain split occurred.
	BitcoinSplitHeight uint64 = 961632

	// BitcoinMajorityBlock961632 is the hash of block 961,632 on the majority
	// chain — the chain every exchange, wallet and miner treats as Bitcoin.
	//
	// Provenance: fetched 2026-08-14 from two independent block explorers,
	// blockstream.info and mempool.space, which returned byte-identical values.
	// Mined by Antpool; it did not signal bit 4, which is precisely why
	// RDTS-enforcing nodes rejected it and forked away.
	BitcoinMajorityBlock961632 = "00000000000000000000d1e01392faa65ceeaed307f0a3159144b84146ff24ba"

	// BitcoinRDTSBlock961632 is the competing block at the same height on the
	// BIP-110 minority chain, mined by Roughnecks. Used only to name the chain
	// in diagnostics: seeing this hash means the node is definitively on the
	// RDTS fork rather than merely lost or on some third branch.
	//
	// Provenance: published in the BIP-110 project's own chain-switching guide
	// at https://bip110.org/change-chains/ and corroborated by contemporaneous
	// reporting of the Antpool/Roughnecks split at 961,632.
	BitcoinRDTSBlock961632 = "0000000000000000000169eb6f811ddbd0daf343af7b62180cdb13e7c78dbc16"

	// rdtsDeploymentKey is the getdeploymentinfo key whose presence indicates
	// the daemon enforces BIP-110. This is the authoritative enforcement test.
	// Never infer enforcement from a version string: the enforcing and
	// non-enforcing Bitcoin Knots builds differ by one digit in a datestamp.
	rdtsDeploymentKey = "reduced_data"

	// DefaultMaxTipAge is how stale a Bitcoin tip may be before it is treated as
	// anomalous. Blocks arrive roughly every 10 minutes on the majority chain,
	// so three hours is far outside normal variance. On the minority chain,
	// which produces a block every day or two, a stale tip is the normal state —
	// which is exactly why this is a useful secondary signal.
	DefaultMaxTipAge = 3 * time.Hour
)

// ChainVerdict is the outcome of a chain identity check.
type ChainVerdict string

const (
	// VerdictMajority means the daemon is verifiably on the majority chain.
	VerdictMajority ChainVerdict = "majority"

	// VerdictMinority means the daemon is verifiably NOT on the majority chain.
	VerdictMinority ChainVerdict = "minority"

	// VerdictUnknown means the check could not reach a conclusion — the daemon
	// was unreachable, or it has not yet synced past the split height.
	//
	// This is deliberately distinct from VerdictMinority. A temporarily
	// unreachable daemon must never be reported as a wrong-chain daemon: that
	// would train operators to ignore the alarm that actually matters.
	VerdictUnknown ChainVerdict = "unknown"

	// VerdictNotApplicable means the daemon is not on Bitcoin mainnet, so the
	// question does not arise. The BIP-110 split was a mainnet event and block
	// 961,632 is a mainnet constant — regtest, testnet and signet have no such
	// height and must not be gated against it.
	VerdictNotApplicable ChainVerdict = "n/a"
)

// ChainCheck is the result of inspecting a Bitcoin daemon's chain identity.
type ChainCheck struct {
	Verdict ChainVerdict

	// RDTSEnforcing reports whether the daemon enforces BIP-110, determined from
	// getdeploymentinfo. False when it does not, or when it could not be
	// determined (see DeploymentKnown).
	RDTSEnforcing bool

	// DeploymentKnown is false when getdeploymentinfo could not be read, so
	// RDTSEnforcing=false must not be mistaken for a positive "not enforcing".
	DeploymentKnown bool

	// SplitBlockHash is the hash the daemon reports at BitcoinSplitHeight, empty
	// if it could not be read.
	SplitBlockHash string

	TipHeight uint64
	TipAge    time.Duration
	TipStale  bool

	// Unreachable distinguishes an RPC/transport failure from a real verdict.
	Unreachable bool

	// Reason is a plain-language explanation suitable for logging to operators.
	Reason string
}

// ChainInspector is the subset of the daemon client the guard needs. Narrowed to
// an interface so the decision logic can be tested without a live daemon.
type ChainInspector interface {
	GetDeploymentInfo(ctx context.Context) (*DeploymentInfo, error)
	GetBlockHash(ctx context.Context, height uint64) (string, error)
	GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error)
}

// CheckBitcoinChain determines which chain a Bitcoin daemon is following.
//
// It never returns an error: every failure mode is folded into the ChainCheck so
// callers cannot accidentally treat "could not check" as "check passed". Callers
// decide what to do via AllowMining.
func CheckBitcoinChain(ctx context.Context, c ChainInspector, now time.Time) ChainCheck {
	var res ChainCheck

	info, err := c.GetBlockchainInfo(ctx)
	if err != nil {
		res.Verdict = VerdictUnknown
		res.Unreachable = true
		res.Reason = fmt.Sprintf("could not reach the daemon to verify chain identity: %v", err)
		return res
	}
	res.TipHeight = info.Blocks

	// Only Bitcoin mainnet has a block 961,632 to compare against. Gating
	// regtest, testnet or signet on a mainnet constant would refuse to start a
	// perfectly correct node — a regtest chain sits at height ~100 and would be
	// permanently "below the split height".
	if info.Chain != "" && info.Chain != "main" {
		res.Verdict = VerdictNotApplicable
		res.Reason = fmt.Sprintf("chain is %q, not Bitcoin mainnet — the BIP-110 split does not apply", info.Chain)
		return res
	}

	if info.MedianTime > 0 {
		res.TipAge = now.Sub(time.Unix(info.MedianTime, 0))
		res.TipStale = res.TipAge > DefaultMaxTipAge
	}

	// Check A — enforcement. A daemon that enforces RDTS will follow the
	// minority chain by construction, whatever its current tip happens to be.
	// A failure here is not fatal to the overall check: chain identity is still
	// decidable from the block hash below, which is the stronger signal.
	if dep, derr := c.GetDeploymentInfo(ctx); derr == nil {
		res.DeploymentKnown = true
		if _, found := dep.Deployments[rdtsDeploymentKey]; found {
			res.RDTSEnforcing = true
		}
	}

	// A node below the split height has no block to compare. That is unknown,
	// never a pass — a node still syncing must not be reported as verified.
	if info.Blocks < BitcoinSplitHeight {
		res.Verdict = VerdictUnknown
		res.Reason = fmt.Sprintf(
			"node is at height %d, below the split height %d; chain identity cannot be verified until it syncs past that point",
			info.Blocks, BitcoinSplitHeight)
		if res.RDTSEnforcing {
			// Enforcement is decidable even when the hash is not, and it is
			// conclusive on its own.
			res.Verdict = VerdictMinority
			res.Reason = "daemon enforces BIP-110 (RDTS) and will follow the minority chain once it reaches the split height"
		}
		return res
	}

	// Check B — chain identity. This is the authoritative test.
	hash, herr := c.GetBlockHash(ctx, BitcoinSplitHeight)
	if herr != nil {
		res.Verdict = VerdictUnknown
		res.Unreachable = true
		res.Reason = fmt.Sprintf("could not read block %d to verify chain identity: %v", BitcoinSplitHeight, herr)
		return res
	}
	res.SplitBlockHash = hash

	switch {
	case hash == BitcoinMajorityBlock961632:
		res.Verdict = VerdictMajority
		res.Reason = fmt.Sprintf("on the majority chain (block %d verified)", BitcoinSplitHeight)
		if res.RDTSEnforcing {
			// Contradictory: enforcing RDTS but currently holding the majority
			// block. It will reorg away as soon as it sees the minority branch.
			res.Verdict = VerdictMinority
			res.Reason = "daemon enforces BIP-110 (RDTS); it will reject the majority chain and follow the minority branch"
		}
	case hash == BitcoinRDTSBlock961632:
		res.Verdict = VerdictMinority
		res.Reason = "following the BIP-110 (RDTS) minority chain"
	default:
		res.Verdict = VerdictMinority
		res.Reason = fmt.Sprintf("block %d does not match the majority chain or the known RDTS chain; this node is on an unrecognised branch", BitcoinSplitHeight)
	}

	return res
}

// AllowMining reports whether the pool may serve work for this coin, and why.
//
// Fails closed: anything other than a verified majority-chain verdict blocks
// mining unless the operator has explicitly opted in via allowNonMajority.
func (cc ChainCheck) AllowMining(allowNonMajority bool) (bool, string) {
	if cc.Verdict == VerdictNotApplicable {
		return true, cc.Reason
	}

	if cc.Verdict == VerdictMajority && !cc.TipStale {
		return true, cc.Reason
	}

	if allowNonMajority {
		// cc.Reason is set before staleness is evaluated, so for a stale tip it
		// reads "on the majority chain (block 961632 verified)" — which makes the
		// override message say the check "did not pass (on the majority chain)".
		// Describe the actual reason mining was blocked instead.
		blocked := cc.Reason
		if cc.Verdict == VerdictMajority && cc.TipStale {
			blocked = fmt.Sprintf("on the majority chain but the tip is %s old",
				cc.TipAge.Round(time.Minute))
		}
		return true, fmt.Sprintf(
			"chain check did not pass (%s) but allow_nonmajority_chain is set — mining anyway at operator request",
			blocked)
	}

	switch cc.Verdict {
	case VerdictMinority:
		return false, fmt.Sprintf(
			"refusing to mine: %s. Blocks found on this chain are unlikely to have any value. "+
				"Set allow_nonmajority_chain: true for this coin only if you genuinely intend to mine it.",
			cc.Reason)
	case VerdictUnknown:
		return false, fmt.Sprintf("refusing to mine: %s", cc.Reason)
	default: // majority but stale tip
		return false, fmt.Sprintf(
			"refusing to mine: on the majority chain but the tip is %s old, which is far outside normal "+
				"variance for Bitcoin. The node is probably stalled or desynced.",
			cc.TipAge.Round(time.Minute))
	}
}
