// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package shares - V2 validator with multi-coin and multi-algorithm support.
//
// This provides a share validator that uses the coin interface for coin-specific
// validation logic, including algorithm-specific block hashing.
//
// Supported Algorithms:
//   - SHA256d: Bitcoin, DigiByte, Bitcoin Cash, Bitcoin II
//   - Scrypt:  Litecoin, Dogecoin
package shares

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/spiralpool/stratum/internal/auxpow"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// ══════════════════════════════════════════════════════════════════════════════
// DEBUG AUDIT LOGGING - Disabled by default for production
// ══════════════════════════════════════════════════════════════════════════════
// Enable via SPIRAL_AUDIT_DEBUG=1 environment variable for forensic analysis.
// Format: Single-line, structured, deterministic, side-effect free
// ══════════════════════════════════════════════════════════════════════════════

// auditDebugEnabled controls verbose audit logging output.
// Disabled by default - enable via SPIRAL_AUDIT_DEBUG=1 for debugging.
var auditDebugEnabled = os.Getenv("SPIRAL_AUDIT_DEBUG") == "1"

// debugLogCoinbase logs all coinbase construction details for forensic analysis.
func debugLogCoinbase(jobID string, coinbase1, extranonce1, extranonce2, coinbase2, fullCoinbase []byte, txidLE, txidBE []byte) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_COINBASE jobID=%s cb1_len=%d en1_len=%d en2_len=%d cb2_len=%d full_len=%d cb1_hex=%s en1_hex=%s en2_hex=%s cb2_hex=%s full_hex=%s txid_le=%s txid_be=%s\n",
		jobID,
		len(coinbase1), len(extranonce1), len(extranonce2), len(coinbase2), len(fullCoinbase),
		hex.EncodeToString(coinbase1),
		hex.EncodeToString(extranonce1),
		hex.EncodeToString(extranonce2),
		hex.EncodeToString(coinbase2),
		hex.EncodeToString(fullCoinbase),
		hex.EncodeToString(txidLE),
		hex.EncodeToString(txidBE),
	)
}

// debugLogMerkleStep logs each step of merkle root computation.
func debugLogMerkleStep(jobID string, step int, inputHash, branchHash, outputHash []byte) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_MERKLE_STEP jobID=%s step=%d input=%s branch=%s output=%s\n",
		jobID, step,
		hex.EncodeToString(inputHash),
		hex.EncodeToString(branchHash),
		hex.EncodeToString(outputHash),
	)
}

// debugLogMerkleRoot logs the final merkle root computation.
func debugLogMerkleRoot(jobID string, txidList []string, finalRoot []byte) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_MERKLE_ROOT jobID=%s txid_count=%d final_root=%s txids=%v\n",
		jobID, len(txidList), hex.EncodeToString(finalRoot), txidList,
	)
}

// debugLogHeader logs the complete header construction.
func debugLogHeader(jobID string, header []byte, version, prevHash, merkleRoot, ntime, nbits, nonce []byte) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_HEADER jobID=%s header_hex=%s version=%s prevhash=%s merkle=%s ntime=%s nbits=%s nonce=%s\n",
		jobID,
		hex.EncodeToString(header),
		hex.EncodeToString(version),
		hex.EncodeToString(prevHash),
		hex.EncodeToString(merkleRoot),
		hex.EncodeToString(ntime),
		hex.EncodeToString(nbits),
		hex.EncodeToString(nonce),
	)
}

// debugLogHash logs the hash computation result.
func debugLogHash(jobID string, algorithm string, headerHex string, hashResult []byte, shareTarget, networkTarget, hashInt *big.Int, meetsShare, meetsNetwork bool) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_HASH jobID=%s algo=%s header=%s hash=%s hash_int=%064x share_target=%064x network_target=%064x meets_share=%v meets_network=%v\n",
		jobID, algorithm, headerHex,
		hex.EncodeToString(hashResult),
		hashInt,
		shareTarget,
		networkTarget,
		meetsShare, meetsNetwork,
	)
}

// debugLogDifficultyContext logs all difficulty-related values.
func debugLogDifficultyContext(jobID string, algorithm string, shareDiff, minDiff, networkDiff, actualDiff float64) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_DIFFICULTY jobID=%s algo=%s share_diff=%.8f min_diff=%.8f network_diff=%.8f actual_diff=%.8f\n",
		jobID, algorithm, shareDiff, minDiff, networkDiff, actualDiff,
	)
}

// ValidatorV2 extends Validator with coin-specific logic.
// This version supports multi-algorithm validation (SHA256d, Scrypt).
type ValidatorV2 struct {
	*Validator
	coin       coin.Coin
	auxManager *auxpow.Manager // Optional: for merge mining aux block validation
}

// NewValidatorWithCoin creates a new share validator with coin-specific support.
//
// The validator's maxJobAge is automatically scaled based on the coin's block time.
// This is a SAFETY NET - primary staleness is handled by job invalidation (CleanJobs).
//
// Formula: blockTime × 4, min 1 minute, max 10 minutes
// - 4 blocks gives reasonable buffer for notification failures
// - 1 minute minimum prevents spurious rejections from network jitter
// - 10 minute maximum is reasonable upper bound
//
// Calculated values:
// - DGB  (15s blocks):  1 min  = 4 blocks
// - FBTC (30s blocks):  2 min  = 4 blocks
// - DOGE (60s blocks):  4 min  = 4 blocks
// - LTC  (150s blocks): 10 min = 4 blocks
// - BTC  (600s blocks): 10 min = 1 block (capped)
func NewValidatorWithCoin(getJob func(string) (*protocol.Job, bool), coinImpl coin.Coin) *ValidatorV2 {
	v := &ValidatorV2{
		Validator: NewValidator(getJob),
		coin:      coinImpl,
	}

	// Scale maxJobAge based on coin block time
	// Formula: blockTime × 4, min 1 minute, max 10 minutes
	//
	// Rationale:
	// - Primary staleness: CleanJobs invalidates jobs immediately on new block
	// - This timeout is a SAFETY NET for when ZMQ + polling both fail
	// - 4 blocks = reasonable time to detect notification failure
	// - For DGB (15s): 1 min = 4 blocks before rejecting stale shares
	// - For BTC (600s): 10 min = ~1 block (capped, notifications should work)
	blockTime := time.Duration(coinImpl.BlockTime()) * time.Second
	maxAge := blockTime * 4
	if maxAge < 1*time.Minute {
		maxAge = 1 * time.Minute // Minimum to avoid spurious rejections
	}
	if maxAge > 10*time.Minute {
		maxAge = 10 * time.Minute // Reasonable upper bound
	}
	v.SetMaxJobAge(maxAge)

	return v
}

// Coin returns the coin implementation.
func (v *ValidatorV2) Coin() coin.Coin {
	return v.coin
}

// SetAuxManager sets the AuxPoW manager for merge mining validation.
// When set, the validator will check shares against aux chain targets
// and populate AuxResults when shares meet aux difficulty.
func (v *ValidatorV2) SetAuxManager(mgr *auxpow.Manager) {
	v.auxManager = mgr
}

// GetAuxManager returns the AuxPoW manager (nil if merge mining is disabled).
func (v *ValidatorV2) GetAuxManager() *auxpow.Manager {
	return v.auxManager
}

// ValidateWithCoin validates a share using coin-specific hashing algorithm.
// This is the primary validation method for multi-algorithm support.
//
// Algorithm dispatch:
//   - SHA256d coins (BTC, DGB, BCH, BC2): Use crypto.SHA256d
//   - Scrypt coins (LTC, DOGE): Use crypto.ScryptHash
//
// The algorithm is determined by the coin's Algorithm() method.
func (v *ValidatorV2) ValidateWithCoin(share *protocol.Share) *protocol.ShareResult {
	v.validated.Add(1)

	// SECURITY: Snapshot network difficulty once at the start
	currentNetworkDiff := v.GetNetworkDifficulty()

	// 1. Get the job
	job, ok := v.getJob(share.JobID)
	if !ok {
		v.rejected.Add(1)
		v.staleShares.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonInvalidJob,
		}
	}

	// 1.25 Check if job was invalidated (new block found, reorg, etc.)
	// CRITICAL: This prevents accepting shares on old blockchain branches
	// THREAD SAFETY: Use GetState() for atomic read of job state
	if job.GetState() == protocol.JobStateInvalidated {
		v.rejected.Add(1)
		v.staleShares.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonStale,
		}
	}

	// 1.5 Check if job is stale (based on maxJobAge, scaled by coin block time)
	if isJobStale(job, v.maxJobAge) {
		v.rejected.Add(1)
		v.staleShares.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonStale,
		}
	}

	// 2. Atomic duplicate check and record
	// CGMINER FIX: Silently accept duplicates instead of rejecting them.
	// cgminer (used by Avalon) re-submits shares when it doesn't receive an ACK fast enough,
	// causing high reject rates. By accepting duplicates (but not double-crediting), we keep
	// cgminer happy while maintaining accounting integrity. The share is already recorded,
	// so it won't be credited twice.
	if !v.duplicates.RecordIfNew(share.JobID, share.ExtraNonce1, share.ExtraNonce2, share.NTime, share.Nonce) {
		// Track as duplicate for metrics, but tell miner it was accepted
		return &protocol.ShareResult{
			Accepted:        true,                           // Tell miner it's accepted (prevents retry flood)
			RejectReason:    protocol.RejectReasonDuplicate, // For metrics tracking
			SilentDuplicate: true,                           // Flag to prevent double-crediting
		}
	}

	// 2.5 Validate version rolling (BIP320 compliance)
	if share.VersionBits != 0 {
		if !job.VersionRollingAllowed {
			v.rejected.Add(1)
			v.versionRollRejects.Add(1)
			return &protocol.ShareResult{
				Accepted:     false,
				RejectReason: protocol.RejectReasonInvalidVersionRolling,
			}
		}
		if !validateVersionRolling(share.VersionBits, job.VersionRollingMask) {
			v.rejected.Add(1)
			v.versionRollRejects.Add(1)
			return &protocol.ShareResult{
				Accepted:     false,
				RejectReason: protocol.RejectReasonInvalidVersionRolling,
			}
		}
	}

	// 3. Validate ntime
	if !validateNTime(share.NTime, job.NTime) {
		v.rejected.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonInvalidTime,
		}
	}

	// 3.5 Track nonce usage for exhaustion detection
	if share.SessionID != 0 {
		nonce, err := parseNonce(share.Nonce)
		if err == nil {
			sessionKey := fmt.Sprintf("%d", share.SessionID)
			if exhausted := v.nonceTracker.TrackNonce(sessionKey, share.JobID, nonce); exhausted {
				v.nonceExhaustion.Add(1)
			}
		}
	}

	// 4. Build the block header (uses computeMerkleRoot internally which has audit logging)
	header, err := buildBlockHeader(job, share)
	if err != nil {
		if auditDebugEnabled {
			fmt.Printf("AUDIT_ERROR jobID=%s error=header_build_failed detail=%v\n", share.JobID, err)
		}
		v.rejected.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonInvalidSolution,
		}
	}

	// AUDIT: Log header construction
	debugLogHeader(share.JobID, header,
		header[0:4],   // version
		header[4:36],  // prevhash
		header[36:68], // merkle root
		header[68:72], // ntime
		header[72:76], // nbits
		header[76:80], // nonce
	)

	// 5. MULTI-ALGORITHM: Compute hash using coin-specific algorithm
	// This is the key difference from base Validator - we dispatch through coin interface
	algorithm := v.coin.Algorithm()
	hash := v.coin.HashBlockHeader(header)

	// 6. Convert hash to big.Int for comparison (little-endian)
	hashInt := new(big.Int).SetBytes(reverseBytes(hash))

	// 7. Calculate share target from share difficulty
	// Uses coin-specific multiplier (65536 for scrypt) so the target matches
	// the diff-1 convention used by the miner's stratum implementation.
	shareTarget := v.coinDifficultyToTarget(share.Difficulty)
	// SECURITY: Zero target indicates invalid difficulty (<=0, NaN, Inf) and rejects all shares
	if shareTarget.Sign() == 0 {
		v.rejected.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonInvalidSolution,
		}
	}

	// CRITICAL: Use the STRICTEST available target for block detection.
	//
	// Bitcoin Core's CheckProofOfWork() (src/pow.cpp) validates submitted blocks
	// against the compact bits (nBits) in the block header — NOT the GBT target.
	// For single-algo coins (BTC, LTC), the GBT target and compact bits expansion
	// are identical. But for multi-algo coins (DGB), the GBT target can be
	// MORE PERMISSIVE than the compact bits, causing "high-hash" rejections where
	// the pool accepts a hash the daemon rejects.
	//
	// INCIDENT: 2026-01-27 04:49 UTC, DGB height 22870718 — pool accepted hash
	// 00000000000000057f... against a permissive GBT target, but the daemon's
	// compact bits target was 0000000000000004e1e4... (stricter). Result: high-hash.
	//
	// FIX: Compute BOTH targets when available, use min(gbt, bits) — the stricter
	// one. This guarantees the pool never accepts a hash the daemon will reject.
	//
	// Fallback chain:
	//   1. If both GBT target AND compact bits are valid → use the SMALLER (stricter)
	//   2. If only one is valid → use that one
	//   3. difficultyToTarget(float64) — last resort, loses precision at high difficulty
	//
	// If ALL fail, networkTarget stays zero → block detection is skipped (safe).
	var networkTarget *big.Int
	targetSource := "none"

	// Parse GBT target (if available)
	var gbtTarget *big.Int
	if job.NetworkTarget != "" {
		gbtTarget = new(big.Int)
		if _, ok := gbtTarget.SetString(job.NetworkTarget, 16); !ok || gbtTarget.Sign() == 0 {
			gbtTarget = nil
			if auditDebugEnabled {
				fmt.Printf("AUDIT_TARGET_FALLBACK jobID=%s reason=invalid_gbt_target raw=%s\n", share.JobID, job.NetworkTarget)
			}
		}
	}

	// Parse compact bits target (if available)
	bitsTarget := compactBitsToTarget(job.NBits)
	if bitsTarget.Sign() == 0 {
		bitsTarget = nil
	}

	// Use the STRICTEST (smallest) available target
	switch {
	case gbtTarget != nil && bitsTarget != nil:
		// Both available — use the stricter (smaller) one.
		// The daemon validates against compact bits (CheckProofOfWork), so if
		// GBT is more permissive, we'd get high-hash rejections using GBT alone.
		if gbtTarget.Cmp(bitsTarget) <= 0 {
			networkTarget = gbtTarget
			targetSource = "gbt"
		} else {
			networkTarget = bitsTarget
			targetSource = "compact_bits_stricter"
			if auditDebugEnabled {
				fmt.Printf("AUDIT_TARGET_OVERRIDE jobID=%s reason=gbt_more_permissive gbt=%064x bits=%064x\n",
					share.JobID, gbtTarget, bitsTarget)
			}
		}
	case gbtTarget != nil:
		networkTarget = gbtTarget
		targetSource = "gbt"
	case bitsTarget != nil:
		networkTarget = bitsTarget
		targetSource = "compact_bits"
	default:
		// Neither GBT nor compact bits — fall back to float64 (last resort)
		if currentNetworkDiff > 0 {
			networkTarget = v.coinDifficultyToTarget(currentNetworkDiff)
			if networkTarget != nil && networkTarget.Sign() > 0 {
				targetSource = "float64_diff"
			}
		}
	}

	// Final nil guard — ensure networkTarget is never nil downstream
	if networkTarget == nil {
		networkTarget = big.NewInt(0)
	}
	if auditDebugEnabled {
		fmt.Printf("AUDIT_TARGET_SOURCE jobID=%s source=%s target=%064x\n", share.JobID, targetSource, networkTarget)
	}
	meetsShareTarget := hashInt.Cmp(shareTarget) <= 0
	meetsNetworkTarget := networkTarget.Sign() != 0 && hashInt.Cmp(networkTarget) <= 0

	// AUDIT: Log hash computation and comparison
	debugLogHash(share.JobID, algorithm, hex.EncodeToString(header), hash, shareTarget, networkTarget, hashInt, meetsShareTarget, meetsNetworkTarget)

	// ══════════════════════════════════════════════════════════════════════════════
	// MERGE MINING: Check aux chain targets FIRST - BEFORE parent difficulty check
	// ══════════════════════════════════════════════════════════════════════════════
	// CRITICAL: Aux chains typically have MUCH lower difficulty than parent chains.
	// A share that doesn't meet parent MinDifficulty could still meet an aux chain's
	// difficulty. We MUST check aux targets before rejecting for low difficulty,
	// otherwise we lose potential aux block rewards.
	// ══════════════════════════════════════════════════════════════════════════════
	var auxResults []protocol.AuxBlockResult
	if job.IsMergeJob && v.auxManager != nil && len(job.AuxBlocks) > 0 {
		auxResults = v.checkAuxTargets(job, share, header, hash, hashInt)
	}

	// 8. Check if hash meets share difficulty
	// If it doesn't meet the expected difficulty, check against MinDifficulty as fallback.
	// This handles vardiff transitions where miners (especially cgminer/Avalon) continue
	// submitting shares at the old difficulty until they receive a new job.
	if !meetsShareTarget {
		actualDiff := v.coinHashToDifficulty(hashInt)

		// AUDIT: Log difficulty context for rejected share
		debugLogDifficultyContext(share.JobID, algorithm, share.Difficulty, share.MinDifficulty, currentNetworkDiff, actualDiff)

		// Share doesn't meet expected difficulty - try MinDifficulty fallback
		// Use <= to handle case where MinDifficulty equals Difficulty (e.g., during vardiff grace period)
		if share.MinDifficulty > 0 && share.MinDifficulty <= share.Difficulty {
			minTarget := v.coinDifficultyToTarget(share.MinDifficulty)
			// Zero target means invalid MinDifficulty - reject share
			if minTarget.Sign() != 0 && hashInt.Cmp(minTarget) <= 0 {
				// Share meets minimum difficulty - accept it
				// Update the share's difficulty to reflect what it actually achieved
				share.Difficulty = share.MinDifficulty
				// Continue to block check below
			} else {
				// Doesn't meet even minimum difficulty for parent chain
				// BUT: Still return aux results so aux blocks can be submitted!
				v.rejected.Add(1)
				return &protocol.ShareResult{
					Accepted:     false,
					RejectReason: protocol.RejectReasonLowDifficulty,
					AuxResults:   auxResults, // CRITICAL: Include aux blocks even on rejected shares
				}
			}
		} else {
			// Share doesn't meet parent difficulty
			// BUT: Still return aux results so aux blocks can be submitted!
			v.rejected.Add(1)
			return &protocol.ShareResult{
				Accepted:     false,
				RejectReason: protocol.RejectReasonLowDifficulty,
				AuxResults:   auxResults, // CRITICAL: Include aux blocks even on rejected shares
			}
		}
	}

	// Share is valid - check if it's also a block
	// Calculate the actual difficulty achieved by this hash for best-share tracking
	actualDiff := v.coinHashToDifficulty(hashInt)

	// AUDIT: Log difficulty context for accepted share
	debugLogDifficultyContext(share.JobID, algorithm, share.Difficulty, share.MinDifficulty, currentNetworkDiff, actualDiff)

	result := &protocol.ShareResult{
		Accepted:         true,
		ActualDifficulty: actualDiff,
		TargetSource:     targetSource,
	}
	// CRITICAL FIX: Check for zero network target to prevent panic
	// This can happen if network difficulty is 0, NaN, or uninitialized
	// Zero target means invalid difficulty - cannot determine if this is a block
	if networkTarget.Sign() == 0 {
		// Cannot check for block without valid network difficulty
		// Share is still valid, just can't determine if it's a block
		result.AuxResults = auxResults // Include any aux blocks found
		v.accepted.Add(1)
		share.NetworkDiff = currentNetworkDiff
		share.BlockHeight = job.Height
		return result
	}
	if hashInt.Cmp(networkTarget) <= 0 {
		result.IsBlock = true

		// CANONICAL BLOCK HASH: In all Bitcoin-derived chains, the block identifier
		// (what getblockhash returns) is always SHA256d(header), regardless of the
		// PoW algorithm. For SHA256d coins the PoW hash IS the block hash. For scrypt
		// coins (LTC, DOGE, DGB-SCRYPT, PEP, CAT, etc.), the PoW hash differs from the
		// block identifier. Using the PoW hash would cause every non-SHA256d block
		// to be falsely marked as orphaned by the payment processor.
		if algorithm != "sha256d" {
			first := sha256.Sum256(header)
			second := sha256.Sum256(first[:])
			result.BlockHash = hex.EncodeToString(reverseBytes(second[:]))
		} else {
			result.BlockHash = hex.EncodeToString(reverseBytes(hash))
		}
		result.CoinbaseValue = job.CoinbaseValue
		result.PrevBlockHash = job.PrevBlockHash
		result.JobAge = time.Since(job.CreatedAt)

		blockHex, err := buildFullBlock(job, share, header)
		if err != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_ERROR jobID=%s error=block_build_failed height=%d hash=%s detail=%v\n",
					share.JobID, job.Height, result.BlockHash, err)
			}
			result.BlockHex = ""
			result.BlockBuildError = err.Error()
		} else {
			result.BlockHex = blockHex
		}
	}

	// Assign aux results (already computed before difficulty check)
	result.AuxResults = auxResults

	v.accepted.Add(1)
	share.NetworkDiff = currentNetworkDiff
	share.BlockHeight = job.Height

	return result
}

// ShareDifficultyMultiplier returns the coin's share difficulty multiplier.
func (v *ValidatorV2) ShareDifficultyMultiplier() float64 {
	return v.coin.ShareDifficultyMultiplier()
}

// coinDifficultyToTarget converts stratum difficulty to a hash target, accounting
// for the coin's share difficulty multiplier. Scrypt coins use a different diff-1
// target (0x0000FFFF00000000... vs Bitcoin's 0x00000000FFFF0000...), making the
// effective maxTarget 65536x larger. This adjusts the difficulty divisor so that
// the resulting target matches what the miner actually targets.
func (v *ValidatorV2) coinDifficultyToTarget(difficulty float64) *big.Int {
	mult := v.coin.ShareDifficultyMultiplier()
	if mult > 1 {
		return difficultyToTarget(difficulty / mult)
	}
	return difficultyToTarget(difficulty)
}

// coinHashToDifficulty converts a hash value to stratum difficulty, accounting
// for the coin's share difficulty multiplier. Returns difficulty in the same
// scale as stratum difficulty values sent to miners.
func (v *ValidatorV2) coinHashToDifficulty(hashInt *big.Int) float64 {
	mult := v.coin.ShareDifficultyMultiplier()
	return hashToDifficulty(hashInt) * mult
}

// Algorithm returns the mining algorithm name for this validator's coin.
func (v *ValidatorV2) Algorithm() string {
	return v.coin.Algorithm()
}

// checkAuxTargets checks if the share hash meets any aux chain targets.
// Returns AuxBlockResults for chains where the hash meets the difficulty target.
//
// CRITICAL: This enables merge mining to work correctly - aux blocks can be found
// and submitted even when the share doesn't meet the parent chain's difficulty.
// This is the fundamental economic benefit of merge mining.
func (v *ValidatorV2) checkAuxTargets(
	job *protocol.Job,
	share *protocol.Share,
	header []byte,
	hash []byte,
	hashInt *big.Int,
) []protocol.AuxBlockResult {
	var results []protocol.AuxBlockResult

	// Build the full coinbase transaction for AuxPoW proof
	// Coinbase = CB1 + ExtraNonce1 + ExtraNonce2 + CB2
	cb1, err := hex.DecodeString(job.CoinBase1)
	if err != nil {
		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_ERROR jobID=%s error=cb1_decode detail=%v\n", share.JobID, err)
		}
		return nil
	}
	en1, err := hex.DecodeString(share.ExtraNonce1)
	if err != nil {
		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_ERROR jobID=%s error=en1_decode detail=%v\n", share.JobID, err)
		}
		return nil
	}
	en2, err := hex.DecodeString(share.ExtraNonce2)
	if err != nil {
		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_ERROR jobID=%s error=en2_decode detail=%v\n", share.JobID, err)
		}
		return nil
	}
	cb2, err := hex.DecodeString(job.CoinBase2)
	if err != nil {
		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_ERROR jobID=%s error=cb2_decode detail=%v\n", share.JobID, err)
		}
		return nil
	}

	// Build full coinbase
	fullCoinbase := make([]byte, 0, len(cb1)+len(en1)+len(en2)+len(cb2))
	fullCoinbase = append(fullCoinbase, cb1...)
	fullCoinbase = append(fullCoinbase, en1...)
	fullCoinbase = append(fullCoinbase, en2...)
	fullCoinbase = append(fullCoinbase, cb2...)

	// Convert coinbase merkle branches from hex strings to [][]byte
	coinbaseMerkleBranch := make([][]byte, len(job.MerkleBranches))
	for i, branchHex := range job.MerkleBranches {
		branch, err := hex.DecodeString(branchHex)
		if err != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s error=merkle_branch_decode index=%d detail=%v\n", share.JobID, i, err)
			}
			return nil
		}
		coinbaseMerkleBranch[i] = branch
	}

	// Convert aux merkle branches from hex strings to [][]byte
	auxMerkleBranch := make([][]byte, len(job.AuxMerkleBranch))
	for i, branchHex := range job.AuxMerkleBranch {
		branch, err := hex.DecodeString(branchHex)
		if err != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s error=aux_merkle_branch_decode index=%d detail=%v\n", share.JobID, i, err)
			}
			return nil
		}
		auxMerkleBranch[i] = branch
	}

	if auditDebugEnabled {
		fmt.Printf("MERGE_MINING_JOB jobID=%s isMerge=%v auxChains=%d jobAuxMerkleRoot=%s\n",
			share.JobID, job.IsMergeJob, len(job.AuxBlocks), job.AuxMerkleRoot)
	}

	// Check each aux chain's target
	for _, auxBlock := range job.AuxBlocks {
		// Convert target bytes to big.Int (big-endian)
		if len(auxBlock.Target) == 0 {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s symbol=%s error=empty_target\n", share.JobID, auxBlock.Symbol)
			}
			continue
		}
		auxTarget := new(big.Int).SetBytes(auxBlock.Target)

		// Skip if target is zero (invalid)
		if auxTarget.Sign() == 0 {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s symbol=%s error=zero_target\n", share.JobID, auxBlock.Symbol)
			}
			continue
		}

		// Check if hash meets aux chain difficulty
		meetsAuxTarget := hashInt.Cmp(auxTarget) <= 0

		// AUDIT: Log aux target check
		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_TARGET jobID=%s symbol=%s chain_id=%d hash_int=%064x aux_target=%064x meets_target=%v\n",
				share.JobID, auxBlock.Symbol, auxBlock.ChainID, hashInt, auxTarget, meetsAuxTarget)
		}

		if !meetsAuxTarget {
			continue
		}

		// Found an aux block! Build the AuxPoW proof
		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_BLOCK_FOUND jobID=%s symbol=%s height=%d chain_id=%d\n",
				share.JobID, auxBlock.Symbol, auxBlock.Height, auxBlock.ChainID)
		}

		// Get the aux coin implementation for proof serialization
		auxCoin, err := v.auxManager.GetAuxCoin(auxBlock.Symbol)
		if err != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s symbol=%s error=get_aux_coin detail=%v\n", share.JobID, auxBlock.Symbol, err)
			}
			results = append(results, protocol.AuxBlockResult{
				Symbol:  auxBlock.Symbol,
				ChainID: auxBlock.ChainID,
				Height:  auxBlock.Height,
				IsBlock: false,
				Error:   fmt.Sprintf("failed to get aux coin: %v", err),
			})
			continue
		}

		// Convert protocol.AuxBlockData to auxpow.AuxBlockData for proof building
		auxBlockData := &auxpow.AuxBlockData{
			Symbol:        auxBlock.Symbol,
			ChainID:       auxBlock.ChainID,
			Hash:          auxBlock.Hash,
			Target:        auxTarget,
			Height:        auxBlock.Height,
			CoinbaseValue: auxBlock.CoinbaseValue,
			ChainIndex:    auxBlock.ChainIndex,
		}

		// Build the AuxPoW proof
		// NOTE: 'hash' is the parent block hash (computed using parent coin's algorithm)
		proof, err := auxpow.BuildAuxPowProof(
			fullCoinbase,
			coinbaseMerkleBranch,
			header,
			hash, // Parent block hash - required for AuxPoW serialization
			auxBlockData,
			auxMerkleBranch,
		)
		if err != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s symbol=%s error=build_proof detail=%v\n", share.JobID, auxBlock.Symbol, err)
			}
			results = append(results, protocol.AuxBlockResult{
				Symbol:  auxBlock.Symbol,
				ChainID: auxBlock.ChainID,
				Height:  auxBlock.Height,
				IsBlock: false,
				Error:   fmt.Sprintf("failed to build proof: %v", err),
			})
			continue
		}

		// Serialize the proof using the aux coin's format
		proofBytes, err := auxCoin.SerializeAuxPowProof(proof)
		if err != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s symbol=%s error=serialize_proof detail=%v\n", share.JobID, auxBlock.Symbol, err)
			}
			results = append(results, protocol.AuxBlockResult{
				Symbol:  auxBlock.Symbol,
				ChainID: auxBlock.ChainID,
				Height:  auxBlock.Height,
				IsBlock: false,
				Error:   fmt.Sprintf("failed to serialize proof: %v", err),
			})
			continue
		}

		// Verify the proof is valid before submission
		// This catches issues early rather than waiting for daemon rejection
		auxRoot := auxBlock.Hash // For single aux chain, root = hash
		if len(job.AuxBlocks) > 1 {
			// For multiple aux chains, compute from job.AuxMerkleRoot
			// AuxMerkleRoot is in big-endian (display order), must reverse to little-endian (internal)
			auxRootHex, err := hex.DecodeString(job.AuxMerkleRoot)
			if err != nil || len(auxRootHex) != 32 {
				v.logger.Errorw("Invalid aux merkle root in job",
					"jobID", share.JobID,
					"auxMerkleRoot", job.AuxMerkleRoot,
					"error", err,
				)
				continue
			}
			// Reverse from big-endian to little-endian for internal comparison
			auxRoot = make([]byte, 32)
			for i := 0; i < 32; i++ {
				auxRoot[i] = auxRootHex[31-i]
			}
		}
		verifyErr := auxpow.VerifyAuxPowProof(proof, auxBlock.Hash, auxRoot)
		if verifyErr != nil {
			if auditDebugEnabled {
				fmt.Printf("AUDIT_AUX_ERROR jobID=%s symbol=%s error=verify_proof detail=%v\n", share.JobID, auxBlock.Symbol, verifyErr)
			}
			results = append(results, protocol.AuxBlockResult{
				Symbol:  auxBlock.Symbol,
				ChainID: auxBlock.ChainID,
				Height:  auxBlock.Height,
				IsBlock: false,
				Error:   fmt.Sprintf("proof verification failed: %v", verifyErr),
			})
			continue
		}

		// Success! Add to results
		// CRITICAL: BlockHash must be in display order (big-endian hex) for submitauxblock RPC.
		// auxBlock.Hash is stored in internal order (little-endian) after parsing from getauxblock,
		// so we must reverse it back to display order for submission.
		// Reference: https://github.com/dogecoin/dogecoin/blob/master/qa/rpc-tests/getauxblock.py
		auxResult := protocol.AuxBlockResult{
			Symbol:        auxBlock.Symbol,
			ChainID:       auxBlock.ChainID,
			Height:        auxBlock.Height,
			IsBlock:       true,
			BlockHash:     hex.EncodeToString(reverseBytes(auxBlock.Hash)),
			AuxPowHex:     hex.EncodeToString(proofBytes),
			CoinbaseValue: auxBlock.CoinbaseValue,
		}
		results = append(results, auxResult)

		// CRITICAL DIAGNOSTIC: Always log merge mining proof details for debugging
		// This helps diagnose "Aux POW missing chain merkle root" errors from aux daemons.
		// Extract aux root from coinbase to verify it matches what we're submitting.
		// After byte order fix: coinbase stores big-endian, should match submit_hash.
		var coinbaseAuxRoot string
		if markerIdx := bytes.Index(proof.ParentCoinbase, []byte{0xfa, 0xbe, 0x6d, 0x6d}); markerIdx >= 0 {
			rootStart := markerIdx + 4
			if rootStart+32 <= len(proof.ParentCoinbase) {
				coinbaseAuxRoot = hex.EncodeToString(proof.ParentCoinbase[rootStart : rootStart+32])
			}
		}
		if auditDebugEnabled {
			fmt.Printf("MERGE_MINING_PROOF symbol=%s height=%d submit_hash=%s coinbase_aux_root=%s match=%v\n",
				auxBlock.Symbol, auxBlock.Height,
				auxResult.BlockHash,                              // big-endian (what we submit)
				coinbaseAuxRoot,                                  // big-endian (stored in coinbase)
				coinbaseAuxRoot == auxResult.BlockHash)           // should match now!
		}

		if auditDebugEnabled {
			fmt.Printf("AUDIT_AUX_BLOCK_PROOF_BUILT jobID=%s symbol=%s height=%d proof_len=%d block_hash=%s\n",
				share.JobID, auxBlock.Symbol, auxBlock.Height, len(proofBytes), auxResult.BlockHash)
			fmt.Printf("AUDIT_AUX_PROOF_DETAILS parent_cb_len=%d parent_hash=%s coinbase_branch_len=%d aux_branch_len=%d header_len=%d\n",
				len(proof.ParentCoinbase), hex.EncodeToString(proof.ParentHash),
				len(proof.CoinbaseMerkleBranch), len(proof.AuxMerkleBranch), len(proof.ParentHeader))
		}
	}

	return results
}
