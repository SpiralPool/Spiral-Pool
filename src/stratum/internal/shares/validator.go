// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package shares provides share validation for mining pools.
//
// Share validation logic follows standard Bitcoin protocol specifications,
// including block header construction, merkle root calculation, and
// difficulty target comparison as defined in the Bitcoin whitepaper and
// subsequent protocol documentation.
package shares

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// DefaultMaxJobAge is the default maximum age for jobs before they're considered stale.
// This can be overridden per-validator based on coin block time.
//
// This is a SAFETY NET - primary staleness is handled by job invalidation (CleanJobs).
// Formula: blockTime × 4, min 1 minute, max 10 minutes
// Default of 1 minute is reasonable for fast coins (DGB 15s = 4 blocks).
const DefaultMaxJobAge = 1 * time.Minute

// Validator validates submitted mining shares.
type Validator struct {
	// Job lookup
	getJob func(jobID string) (*protocol.Job, bool)

	// Duplicate detection (per job)
	duplicates *DuplicateTracker

	// Nonce exhaustion tracking (per session)
	nonceTracker *NonceTracker

	// Network difficulty (atomic for lock-free reads)
	networkDiffBits atomic.Uint64

	// Maximum job age before shares are rejected as stale.
	// Default: 5 minutes. Should be scaled based on coin block time.
	// Formula: max(blockTime * 2, 5 minutes) ensures at least 2 blocks of validity.
	maxJobAge time.Duration

	// Metrics
	validated atomic.Uint64
	accepted  atomic.Uint64
	rejected  atomic.Uint64

	// Security metrics
	staleShares        atomic.Uint64 // Shares for old jobs
	nonceExhaustion    atomic.Uint64 // Potential nonce exhaustion events
	versionRollRejects atomic.Uint64 // Rejected due to invalid version rolling

	// Logger for debugging share rejections
	logger *zap.SugaredLogger
}

// NewValidator creates a new share validator.
func NewValidator(getJob func(string) (*protocol.Job, bool)) *Validator {
	logger, _ := zap.NewProduction()
	return &Validator{
		getJob:       getJob,
		duplicates:   NewDuplicateTracker(),
		nonceTracker: NewNonceTracker(),
		maxJobAge:    DefaultMaxJobAge,
		logger:       logger.Sugar(),
	}
}

// NewValidatorWithLogger creates a new share validator with a custom logger.
func NewValidatorWithLogger(getJob func(string) (*protocol.Job, bool), logger *zap.SugaredLogger) *Validator {
	return &Validator{
		getJob:       getJob,
		duplicates:   NewDuplicateTracker(),
		nonceTracker: NewNonceTracker(),
		maxJobAge:    DefaultMaxJobAge,
		logger:       logger,
	}
}

// SetMaxJobAge sets the maximum age for jobs before they're considered stale.
// This should be called based on coin block time to ensure proper scaling.
// Formula recommendation: max(blockTime * 2, 5 minutes)
func (v *Validator) SetMaxJobAge(d time.Duration) {
	v.maxJobAge = d
}

// MaxJobAge returns the current maximum job age setting.
func (v *Validator) MaxJobAge() time.Duration {
	return v.maxJobAge
}

// NonceTracker monitors nonce usage per session to detect exhaustion.
// Nonce exhaustion can indicate:
// 1. Extremely high hashrate miner needing more extranonce2 space
// 2. Potential share flooding attack
// 3. Misconfigured miner software
type NonceTracker struct {
	mu       sync.RWMutex
	sessions map[string]*sessionNonces
}

type sessionNonces struct {
	jobID        string    // Current job being worked on
	nonceCount   uint64    // Number of unique nonces seen for current job
	lastNonce    uint32    // Last nonce value seen
	wrapCount    uint32    // Number of times nonce wrapped around
	lastActivity time.Time // Last share submission time
}

// NonceExhaustionThreshold is the number of unique nonces before warning.
// With 4 billion nonces (2^32), this threshold indicates heavy usage.
// At 1 TH/s, 4B nonces = 4000 seconds of work. At 100 TH/s = 40 seconds.
const NonceExhaustionThreshold = 3_000_000_000 // ~75% of nonce space

// NonceWrapThreshold triggers alert when nonce wraps this many times per job.
const NonceWrapThreshold = 3

func NewNonceTracker() *NonceTracker {
	return &NonceTracker{
		sessions: make(map[string]*sessionNonces),
	}
}

// Maximum tracked sessions to prevent unbounded memory growth
const maxTrackedSessions = 100000

// TrackNonce records nonce usage for a session.
// Returns true if potential exhaustion is detected.
func (nt *NonceTracker) TrackNonce(sessionID, jobID string, nonce uint32) bool {
	nt.mu.Lock()
	defer nt.mu.Unlock()

	sess, ok := nt.sessions[sessionID]
	if !ok {
		// SECURITY: Prevent unbounded map growth from rapid connect/disconnect attacks
		if len(nt.sessions) >= maxTrackedSessions {
			// Evict oldest session (LRU-style) to make room
			var oldestID string
			var oldestTime time.Time
			first := true
			for id, s := range nt.sessions {
				if first || s.lastActivity.Before(oldestTime) {
					oldestID = id
					oldestTime = s.lastActivity
					first = false
				}
			}
			if oldestID != "" {
				delete(nt.sessions, oldestID)
			}
		}

		nt.sessions[sessionID] = &sessionNonces{
			jobID:        jobID,
			nonceCount:   1,
			lastNonce:    nonce,
			lastActivity: time.Now(),
		}
		return false
	}

	// Update job ID if changed, but DON'T reset counters
	// SECURITY: Resetting counters on job change allows attackers to bypass
	// nonce exhaustion detection by simply requesting new jobs frequently.
	// The cumulative nonce count across all jobs is what matters for detecting
	// exhaustion attacks.
	if sess.jobID != jobID {
		sess.jobID = jobID
		sess.lastNonce = nonce
		sess.lastActivity = time.Now()
		// Note: nonceCount and wrapCount are NOT reset - they accumulate across jobs
		sess.nonceCount++
		return sess.nonceCount > NonceExhaustionThreshold || sess.wrapCount >= NonceWrapThreshold
	}

	// Detect nonce wrap-around
	if nonce < sess.lastNonce && sess.lastNonce-nonce > 0x80000000 {
		sess.wrapCount++
	}

	sess.nonceCount++
	sess.lastNonce = nonce
	sess.lastActivity = time.Now()

	// Check for exhaustion indicators
	return sess.nonceCount > NonceExhaustionThreshold || sess.wrapCount >= NonceWrapThreshold
}

// CleanupInactiveSessions removes sessions inactive for more than 10 minutes.
func (nt *NonceTracker) CleanupInactiveSessions() {
	nt.mu.Lock()
	defer nt.mu.Unlock()

	cutoff := time.Now().Add(-10 * time.Minute)
	for sessionID, sess := range nt.sessions {
		if sess.lastActivity.Before(cutoff) {
			delete(nt.sessions, sessionID)
		}
	}
}

// RemoveSession removes tracking for a disconnected session.
func (nt *NonceTracker) RemoveSession(sessionID string) {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	delete(nt.sessions, sessionID)

	// MEMORY OPTIMIZATION: If map is empty, recreate it to release memory
	// Go maps don't shrink after deletions, so we recreate when empty
	if len(nt.sessions) == 0 {
		nt.sessions = make(map[string]*sessionNonces)
	}
}

// SetNetworkDifficulty updates the current network difficulty.
func (v *Validator) SetNetworkDifficulty(diff float64) {
	// Store as raw float64 bits for atomic access with full precision
	v.networkDiffBits.Store(math.Float64bits(diff))
}

// GetNetworkDifficulty returns the current network difficulty.
func (v *Validator) GetNetworkDifficulty() float64 {
	return math.Float64frombits(v.networkDiffBits.Load())
}

// Validate checks a submitted share for validity.
// Enhanced with security checks for stale shares, nonce exhaustion, and BIP320 compliance.
func (v *Validator) Validate(share *protocol.Share) *protocol.ShareResult {
	v.validated.Add(1)

	// SECURITY: Snapshot network difficulty once at the start to prevent race conditions
	// If difficulty is read multiple times during validation, it could change between reads,
	// causing inconsistent block detection and payment calculations
	currentNetworkDiff := v.GetNetworkDifficulty()

	// 1. Get the job
	job, ok := v.getJob(share.JobID)
	if !ok {
		v.rejected.Add(1)
		v.staleShares.Add(1) // Track stale/invalid job shares
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
	// This helps detect miners sending very old shares
	if isJobStale(job, v.maxJobAge) {
		v.rejected.Add(1)
		v.staleShares.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonStale,
		}
	}

	// 2. Check for duplicate using atomic check-and-record
	// SECURITY: Use RecordIfNew to atomically check and record in one operation
	// This prevents TOCTOU race where two goroutines submit same share simultaneously
	// The share is recorded early (before full validation) but this is safe because:
	// - Invalid shares are still rejected (just with duplicate flag set for future)
	// - The tradeoff is: invalid shares pollute duplicate tracker slightly
	// - But this is preferred over the security risk of double-acceptance
	//
	// CGMINER FIX: Silently accept duplicates instead of rejecting them.
	// cgminer (used by Avalon) re-submits shares when it doesn't receive an ACK fast enough,
	// causing high reject rates. By accepting duplicates (but not double-crediting), we keep
	// cgminer happy while maintaining accounting integrity.
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
		// Validate that version bits only modify allowed mask bits
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
				// Don't reject - just track for monitoring
				// High hashrate miners legitimately exhaust nonce space
			}
		}
	}

	// 4. Build the block header and compute hash
	header, err := buildBlockHeader(job, share)
	if err != nil {
		v.rejected.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonInvalidSolution,
		}
	}

	// 5. Compute SHA256d hash
	hash := crypto.SHA256d(header)

	// 6. Convert hash to big.Int for comparison (little-endian)
	hashInt := new(big.Int).SetBytes(reverseBytes(hash))

	// 7. Calculate share target from share difficulty
	shareTarget := difficultyToTarget(share.Difficulty)
	// SECURITY: Zero target indicates invalid difficulty (<=0, NaN, Inf) and rejects all shares
	if shareTarget.Sign() == 0 {
		v.rejected.Add(1)
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: protocol.RejectReasonInvalidSolution,
		}
	}

	// 8. Check if hash meets share difficulty
	// If it doesn't meet the expected difficulty, check against MinDifficulty as fallback.
	// This handles vardiff transitions where miners (especially cgminer/Avalon) continue
	// submitting shares at the old difficulty until they receive a new job.
	if hashInt.Cmp(shareTarget) > 0 {
		// Share doesn't meet expected difficulty - try MinDifficulty fallback
		// Use <= to handle case where MinDifficulty equals Difficulty (e.g., during vardiff grace period)
		if share.MinDifficulty > 0 && share.MinDifficulty <= share.Difficulty {
			minTarget := difficultyToTarget(share.MinDifficulty)
			// Zero target means invalid MinDifficulty - reject share
			if minTarget.Sign() != 0 && hashInt.Cmp(minTarget) <= 0 {
				// Share meets minimum difficulty - accept it
				// Update the share's difficulty to reflect what it actually achieved
				share.Difficulty = share.MinDifficulty
				// Continue to block check below
			} else {
				// Doesn't meet even minimum difficulty
				v.rejected.Add(1)
				return &protocol.ShareResult{
					Accepted:     false,
					RejectReason: protocol.RejectReasonLowDifficulty,
				}
			}
		} else {
			v.rejected.Add(1)
			return &protocol.ShareResult{
				Accepted:     false,
				RejectReason: protocol.RejectReasonLowDifficulty,
			}
		}
	}

	// Share is valid - check if it's also a block
	// Calculate the actual difficulty achieved by this hash for best-share tracking
	actualDiff := hashToDifficulty(hashInt)

	// Trace logging for share difficulty analysis
	// The hashInt determines ActualDifficulty - identical ActualDifficulty requires
	// nearly identical hash values (statistically improbable)
	hashHex := hex.EncodeToString(hash[:8]) // First 8 bytes of hash for identification
	v.logger.Debugw("share_diff_trace",
		"hashPrefix", hashHex,
		"actualDiff", actualDiff,
		"assignedDiff", share.Difficulty,
		"nonce", share.Nonce,
		"sessionId", share.SessionID,
		"jobId", share.JobID,
	)

	result := &protocol.ShareResult{
		Accepted:         true,
		ActualDifficulty: actualDiff,
	}

	// 9. Check if hash meets network difficulty (is a block)
	// CRITICAL: Use compact bits from the job for exact target calculation.
	// Converting float64 difficulty → big.Int target loses precision due to
	// the float64 round-trip (bits → float64 → target). The daemon validates
	// using the compact bits directly, so we must match its exact computation.
	// This prevents "high-hash" rejections where the pool's slightly-permissive
	// target accepts a hash that the daemon's exact target rejects.
	networkTarget := compactBitsToTarget(job.NBits)
	// Fallback: if NBits is missing or invalid, use float64 difficulty conversion
	if networkTarget.Sign() == 0 && currentNetworkDiff > 0 {
		networkTarget = difficultyToTarget(currentNetworkDiff)
	}
	// CRITICAL FIX: Check for zero network target to prevent panic
	// This can happen if network difficulty is 0, NaN, or uninitialized
	// Zero target means invalid difficulty - cannot determine if this is a block
	if networkTarget.Sign() == 0 {
		// Cannot check for block without valid network difficulty
		// Share is still valid, just can't determine if it's a block
		v.accepted.Add(1)
		share.NetworkDiff = currentNetworkDiff
		share.BlockHeight = job.Height
		return result
	}
	if hashInt.Cmp(networkTarget) <= 0 {
		result.IsBlock = true
		result.BlockHash = hex.EncodeToString(reverseBytes(hash))
		result.CoinbaseValue = job.CoinbaseValue // Capture block reward from job

		// Store diagnostic data for rejection analysis
		// This helps diagnose prev-blk-not-found and other timing-related rejections
		result.PrevBlockHash = job.PrevBlockHash
		result.JobAge = time.Since(job.CreatedAt)

		// Build the full block hex for submission to daemon
		blockHex, err := buildFullBlock(job, share, header)
		if err != nil {
			// CRITICAL: Block was solved but serialization failed.
			// Log all raw inputs at ERROR so manual reconstruction is possible.
			// Do NOT silently discard — propagate the error to handleBlock.
			v.logger.Errorw("🚨 BLOCK SERIALIZATION FAILED - solved block cannot be assembled!",
				"height", job.Height,
				"blockHash", result.BlockHash,
				"error", err,
				"jobId", job.ID,
				"coinbase1Len", len(job.CoinBase1),
				"coinbase2Len", len(job.CoinBase2),
				"extranonce1", share.ExtraNonce1,
				"extranonce2", share.ExtraNonce2,
				"txCount", len(job.TransactionData),
				"headerHex", hex.EncodeToString(header),
			)
			result.BlockHex = ""
			result.BlockBuildError = err.Error()
		} else {
			result.BlockHex = blockHex
		}
	}

	// 10. Share already recorded atomically in step 2 via RecordIfNew
	// No need to record again - this prevents any remaining TOCTOU window

	v.accepted.Add(1)
	// SECURITY: Use snapshotted difficulty for consistency
	share.NetworkDiff = currentNetworkDiff
	share.BlockHeight = job.Height

	return result
}

// buildBlockHeader constructs the 80-byte block header from job and share data.
func buildBlockHeader(job *protocol.Job, share *protocol.Share) ([]byte, error) {
	header := make([]byte, 80)

	// Version (4 bytes, little-endian)
	version, err := hex.DecodeString(job.Version)
	if err != nil || len(version) != 4 {
		return nil, fmt.Errorf("invalid version")
	}
	// Apply version rolling if present
	// Use the job's configured version rolling mask for consistency
	if share.VersionBits != 0 && job.VersionRollingAllowed {
		mask := job.VersionRollingMask
		if mask == 0 {
			mask = 0x1FFFE000 // Default BIP320 mask as fallback
		}
		v := binary.BigEndian.Uint32(version)
		v = (v &^ mask) | (share.VersionBits & mask)
		binary.LittleEndian.PutUint32(header[0:4], v)
	} else {
		copy(header[0:4], reverseBytes(version))
	}

	// Previous block hash (32 bytes)
	// Conversion from Stratum format to block header format:
	//
	// 1. Daemon (big-endian/display): [G0][G1][G2][G3][G4][G5][G6][G7]  (8 groups of 4 bytes)
	// 2. Stratum (formatPrevHash reverses group ORDER):
	//    [G7][G6][G5][G4][G3][G2][G1][G0]
	// 3. Miner/pool bswap32 each group:
	//    [rev(G7)][rev(G6)]...[rev(G0)] = full byte reversal of display = internal (LE)
	//
	// Single step: bswap32 each group of the group-reversed stratum format
	// produces the correct internal byte order for the block header.
	if len(job.PrevBlockHash) != 64 {
		return nil, fmt.Errorf("invalid prev block hash length: expected 64 hex chars, got %d", len(job.PrevBlockHash))
	}
	prevHash, err := hex.DecodeString(job.PrevBlockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid prev block hash hex: %w", err)
	}
	if len(prevHash) != 32 {
		return nil, fmt.Errorf("invalid prev block hash: decoded to %d bytes, expected 32", len(prevHash))
	}
	// SECURITY: Reject all-zeros prevhash before conversion
	// An all-zeros hash indicates malformed data or attack attempt
	allZeros := true
	for _, b := range prevHash {
		if b != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		return nil, fmt.Errorf("invalid prev block hash: all zeros")
	}

	// STRATUM PROTOCOL: formatPrevHash sends groups in reversed order so that
	// after the miner's per-word byte-swap (bswap32 / reverse_endianness_per_word),
	// the result is the correct internal (little-endian) byte order for the header.
	//
	// Daemon display format: [G0][G1][G2]...[G7]           (big-endian)
	// Stratum format:        [G7][G6][G5]...[G0]           (group-order reversed)
	// After bswap32 each:    [rev(G7)][rev(G6)]...[rev(G0)] = internal LE
	//
	// This is equivalent to full byte-reversal of the daemon display format,
	// which produces the raw SHA256d hash bytes the daemon uses internally.
	// Verified: un-swap each group of group-reversed = full reversal of original.
	var headerPrevHash [32]byte
	for i := 0; i < 8; i++ {
		// Reverse bytes within each 4-byte group (undo stratum's swap)
		headerPrevHash[i*4+0] = prevHash[i*4+3]
		headerPrevHash[i*4+1] = prevHash[i*4+2]
		headerPrevHash[i*4+2] = prevHash[i*4+1]
		headerPrevHash[i*4+3] = prevHash[i*4+0]
	}
	copy(header[4:36], headerPrevHash[:])

	// Merkle root (32 bytes) - computed from coinbase + merkle branches
	merkleRoot, err := computeMerkleRoot(job, share)
	if err != nil {
		return nil, err
	}
	copy(header[36:68], merkleRoot)

	// Time (4 bytes, little-endian)
	ntime, err := hex.DecodeString(share.NTime)
	if err != nil || len(ntime) != 4 {
		return nil, fmt.Errorf("invalid ntime")
	}
	copy(header[68:72], reverseBytes(ntime))

	// Bits (4 bytes, little-endian)
	nbits, err := hex.DecodeString(job.NBits)
	if err != nil || len(nbits) != 4 {
		return nil, fmt.Errorf("invalid nbits")
	}
	copy(header[72:76], reverseBytes(nbits))

	// Nonce (4 bytes, little-endian)
	// ESP-Miner/NerdQAxe uses %08lx to format nonce VALUE as big-endian hex.
	// Example: nonce VALUE 0xff9c0c5e -> sends "ff9c0c5e"
	// Header stores in little-endian: VALUE 0xff9c0c5e -> bytes [5e, 0c, 9c, ff]
	// So we must REVERSE, same as ntime/nbits handling.
	// FIX [BUILD-20260118H]: Nonce is a VALUE (like ntime), needs byte reversal.
	nonce, err := hex.DecodeString(share.Nonce)
	if err != nil || len(nonce) != 4 {
		return nil, fmt.Errorf("invalid nonce")
	}
	copy(header[76:80], reverseBytes(nonce))

	return header, nil
}

// computeMerkleRoot computes the merkle root from coinbase and merkle branches.
func computeMerkleRoot(job *protocol.Job, share *protocol.Share) ([]byte, error) {
	// Build coinbase transaction
	// CRITICAL: Coinbase = CoinBase1 + ExtraNonce1 + ExtraNonce2 + CoinBase2
	// Both ExtraNonce1 (session) and ExtraNonce2 (share) are required for valid block
	coinbase1, err := hex.DecodeString(job.CoinBase1)
	if err != nil {
		return nil, fmt.Errorf("invalid coinbase1: %w", err)
	}
	extranonce1, err := hex.DecodeString(share.ExtraNonce1)
	if err != nil {
		return nil, fmt.Errorf("invalid extranonce1: %w", err)
	}
	extranonce2, err := hex.DecodeString(share.ExtraNonce2)
	if err != nil {
		return nil, fmt.Errorf("invalid extranonce2: %w", err)
	}
	coinbase2, err := hex.DecodeString(job.CoinBase2)
	if err != nil {
		return nil, fmt.Errorf("invalid coinbase2: %w", err)
	}

	coinbase := make([]byte, 0, len(coinbase1)+len(extranonce1)+len(extranonce2)+len(coinbase2))
	coinbase = append(coinbase, coinbase1...)
	coinbase = append(coinbase, extranonce1...)
	coinbase = append(coinbase, extranonce2...)
	coinbase = append(coinbase, coinbase2...)

	// Hash coinbase to get txid
	txidLE := crypto.SHA256d(coinbase) // Little-endian (internal)
	txidBE := reverseBytes(txidLE)     // Big-endian (display)

	// AUDIT: Log coinbase construction (single-line, structured, deterministic)
	if auditDebugEnabled {
		fmt.Printf("AUDIT_COINBASE jobID=%s cb1_len=%d en1_len=%d en2_len=%d cb2_len=%d full_len=%d cb1_hex=%s en1_hex=%s en2_hex=%s cb2_hex=%s full_hex=%s txid_le=%s txid_be=%s\n",
			share.JobID,
			len(coinbase1), len(extranonce1), len(extranonce2), len(coinbase2), len(coinbase),
			hex.EncodeToString(coinbase1),
			hex.EncodeToString(extranonce1),
			hex.EncodeToString(extranonce2),
			hex.EncodeToString(coinbase2),
			hex.EncodeToString(coinbase),
			hex.EncodeToString(txidLE),
			hex.EncodeToString(txidBE),
		)
	}

	// Start merkle computation with coinbase txid
	merkle := make([]byte, len(txidLE))
	copy(merkle, txidLE)

	// Collect txid list for audit (coinbase is always first)
	txidList := []string{hex.EncodeToString(txidBE)}

	// Apply merkle branches
	for i, branchHex := range job.MerkleBranches {
		branch, err := hex.DecodeString(branchHex)
		if err != nil {
			return nil, fmt.Errorf("invalid merkle branch at index %d", i)
		}
		// SECURITY: Validate merkle branch length
		// All merkle branches must be exactly 32 bytes (256-bit SHA256 hashes)
		// This prevents malformed branches from causing invalid merkle roots
		if len(branch) != 32 {
			return nil, fmt.Errorf("merkle branch %d wrong length: got %d, expected 32", i, len(branch))
		}

		inputHash := make([]byte, len(merkle))
		copy(inputHash, merkle)

		combined := append(merkle, branch...)
		merkle = crypto.SHA256d(combined)

		// AUDIT: Log each merkle step (single-line, structured, deterministic)
		if auditDebugEnabled {
			fmt.Printf("AUDIT_MERKLE_STEP jobID=%s step=%d input=%s branch=%s output=%s\n",
				share.JobID, i,
				hex.EncodeToString(inputHash),
				branchHex,
				hex.EncodeToString(merkle),
			)
		}

		txidList = append(txidList, branchHex)
	}

	// AUDIT: Log final merkle root (single-line, structured, deterministic)
	if auditDebugEnabled {
		fmt.Printf("AUDIT_MERKLE_ROOT jobID=%s txid_count=%d final_root=%s branch_hashes=%v\n",
			share.JobID, len(txidList), hex.EncodeToString(merkle), txidList,
		)
	}

	return merkle, nil
}

// buildFullBlock constructs the complete serialized block for submission to the daemon.
// Block format: header (80 bytes) + varint tx count + coinbase tx + other transactions
func buildFullBlock(job *protocol.Job, share *protocol.Share, header []byte) (string, error) {
	// Start with the 80-byte header
	block := make([]byte, 0, 1024*1024) // Pre-allocate 1MB
	block = append(block, header...)

	// Build coinbase transaction
	// CRITICAL: Coinbase = CoinBase1 + ExtraNonce1 + ExtraNonce2 + CoinBase2
	// Both ExtraNonce1 (session) and ExtraNonce2 (share) are required for valid block
	coinbase1, err := hex.DecodeString(job.CoinBase1)
	if err != nil {
		return "", fmt.Errorf("invalid coinbase1: %w", err)
	}
	extranonce1, err := hex.DecodeString(share.ExtraNonce1)
	if err != nil {
		return "", fmt.Errorf("invalid extranonce1: %w", err)
	}
	extranonce2, err := hex.DecodeString(share.ExtraNonce2)
	if err != nil {
		return "", fmt.Errorf("invalid extranonce2: %w", err)
	}
	coinbase2, err := hex.DecodeString(job.CoinBase2)
	if err != nil {
		return "", fmt.Errorf("invalid coinbase2: %w", err)
	}

	coinbaseTx := make([]byte, 0, len(coinbase1)+len(extranonce1)+len(extranonce2)+len(coinbase2))
	coinbaseTx = append(coinbaseTx, coinbase1...)
	coinbaseTx = append(coinbaseTx, extranonce1...)
	coinbaseTx = append(coinbaseTx, extranonce2...)
	coinbaseTx = append(coinbaseTx, coinbase2...)

	// BIP141: If the coinbase contains a witness commitment (OP_RETURN + aa21a9ed),
	// it must include a witness nonce (32 zero bytes) and be serialized in SegWit format.
	// The coinbase1/coinbase2 split uses legacy format for correct txid computation
	// (in computeMerkleRoot), but the block submission requires SegWit serialization.
	if coinbaseHasWitnessCommitment(coinbaseTx) {
		coinbaseTx = coinbaseWithWitnessNonce(coinbaseTx)
	}

	// Transaction count (coinbase + template transactions)
	txCount := 1 + len(job.TransactionData)
	block = append(block, crypto.EncodeVarInt(uint64(txCount))...)

	// Add coinbase transaction
	block = append(block, coinbaseTx...)

	// Add all other transactions from the template
	for _, txHex := range job.TransactionData {
		txData, err := hex.DecodeString(txHex)
		if err != nil {
			return "", fmt.Errorf("invalid transaction data: %w", err)
		}
		block = append(block, txData...)
	}

	return hex.EncodeToString(block), nil
}

// witnessCommitmentMagic is the 6-byte prefix identifying a BIP141 witness commitment
// in a coinbase output: OP_RETURN (0x6a) + PUSH36 (0x24) + magic (0xaa21a9ed).
var witnessCommitmentMagic = []byte{0x6a, 0x24, 0xaa, 0x21, 0xa9, 0xed}

// coinbaseHasWitnessCommitment checks if the coinbase transaction contains a
// BIP141 witness commitment output by scanning for the magic byte sequence.
func coinbaseHasWitnessCommitment(tx []byte) bool {
	return bytes.Contains(tx, witnessCommitmentMagic)
}

// coinbaseWithWitnessNonce converts a legacy-serialized coinbase transaction to
// SegWit serialization format with a witness nonce (32 zero bytes) per BIP141.
//
// Legacy:  [version(4)][inputs...][outputs...][locktime(4)]
// SegWit:  [version(4)][0x00][0x01][inputs...][outputs...][witness...][locktime(4)]
//
// The witness nonce is a single 32-byte stack item of all zeros — the "witness
// reserved value" that, combined with the witness merkle root, produces the
// commitment stored in the coinbase OP_RETURN output. The default_witness_commitment
// from getblocktemplate is computed assuming this nonce is all zeros.
func coinbaseWithWitnessNonce(legacyTx []byte) []byte {
	// SegWit adds: 2 bytes (marker+flag) + 34 bytes (witness: 1 item header + 32 byte nonce)
	result := make([]byte, 0, len(legacyTx)+36)
	result = append(result, legacyTx[:4]...)                // version
	result = append(result, 0x00, 0x01)                     // SegWit marker + flag
	result = append(result, legacyTx[4:len(legacyTx)-4]...) // inputs + outputs
	result = append(result, 0x01)                            // 1 witness stack item for input 0
	result = append(result, 0x20)                            // item length: 32 bytes
	result = append(result, make([]byte, 32)...)             // witness nonce: 32 zero bytes
	result = append(result, legacyTx[len(legacyTx)-4:]...)   // locktime
	return result
}

// RebuildBlockHex reconstructs the full serialized block hex from raw job and
// share data. This is the recovery path used by handleBlock when the initial
// buildFullBlock call in Validate() failed. If this also fails, the caller
// must write all raw components to the WAL for manual reconstruction.
func RebuildBlockHex(job *protocol.Job, share *protocol.Share) (string, error) {
	header, err := buildBlockHeader(job, share)
	if err != nil {
		return "", fmt.Errorf("header reconstruction failed: %w", err)
	}
	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		return "", fmt.Errorf("block assembly failed: %w", err)
	}
	return blockHex, nil
}

// validateNTime checks if the submitted ntime is valid.
// NTime must be within +/- 7200 seconds (2 hours) of the job time,
// following Bitcoin protocol rules.
func validateNTime(shareNTime, jobNTime string) bool {
	if len(shareNTime) != 8 || len(jobNTime) != 8 {
		return false
	}

	shareBytes, err := hex.DecodeString(shareNTime)
	if err != nil {
		return false
	}
	jobBytes, err := hex.DecodeString(jobNTime)
	if err != nil {
		return false
	}

	// NTime is stored as big-endian in stratum, convert to uint32
	shareTime := binary.BigEndian.Uint32(shareBytes)
	jobTime := binary.BigEndian.Uint32(jobBytes)

	// SECURITY: Reject timestamps near uint32 max (year 2106+)
	// These are likely malicious or malformed - no legitimate miner will submit
	// timestamps this far in the future. The cutoff is ~year 2100.
	const maxReasonableTimestamp = uint32(0xF0000000) // ~year 2100
	if shareTime > maxReasonableTimestamp || jobTime > maxReasonableTimestamp {
		return false
	}

	// Allow +/- 7200 seconds (2 hours) from job time
	// This is the standard Bitcoin rolling time window
	const maxDrift = 7200
	diff := int64(shareTime) - int64(jobTime)
	if diff < -maxDrift || diff > maxDrift {
		return false
	}

	return true
}

// difficultyToTarget converts pool difficulty to a target value.
func difficultyToTarget(difficulty float64) *big.Int {
	// Bitcoin-style difficulty calculation
	// Target = MaxTarget / Difficulty
	// MaxTarget = 0x00000000FFFF0000000000000000000000000000000000000000000000000000

	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// SECURITY: Validate difficulty bounds to prevent overflow/underflow attacks
	// Return zero target for invalid difficulties (rejects all shares)
	if difficulty <= 0 || math.IsNaN(difficulty) || math.IsInf(difficulty, 0) {
		return big.NewInt(0) // Security: zero target rejects all shares
	}

	// PRECISION FIX: Use big.Float for the entire calculation to maintain precision
	// Previous approach using 1e8 scaling lost precision at high difficulties (>1e12)
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	diffFloat := new(big.Float).SetFloat64(difficulty)

	// target = maxTarget / difficulty
	targetFloat := new(big.Float).Quo(maxTargetFloat, diffFloat)

	// Convert to big.Int with proper rounding
	target, _ := targetFloat.Int(nil)

	// Ensure target is positive (very high difficulties may round to zero)
	// Return zero target for negative targets (rejects all shares)
	if target.Sign() < 0 {
		return big.NewInt(0) // Security: zero target rejects all shares
	}

	return target
}

// compactBitsToTarget converts the compact "bits" field from a block header
// directly to a big.Int target. This matches the exact computation used by
// Bitcoin/DigiByte Core daemons, avoiding float64 precision loss.
//
// Format: 0xNNHHHHHH where NN is the exponent and HHHHHH is the mantissa.
// Target = mantissa * 256^(exponent-3)
//
// Returns zero for invalid input (which causes block detection to be skipped).
func compactBitsToTarget(bits string) *big.Int {
	if len(bits) != 8 {
		return big.NewInt(0)
	}
	bitsBytes, err := hex.DecodeString(bits)
	if err != nil {
		return big.NewInt(0)
	}

	compact := binary.BigEndian.Uint32(bitsBytes)
	exponent := compact >> 24
	mantissa := compact & 0x007FFFFF

	// Invalid: zero mantissa or negative flag set (check compact, not masked mantissa)
	if mantissa == 0 || compact&0x00800000 != 0 {
		return big.NewInt(0)
	}

	var target big.Int
	target.SetUint64(uint64(mantissa))
	if exponent <= 3 {
		target.Rsh(&target, uint(8*(3-exponent)))
	} else {
		target.Lsh(&target, uint(8*(exponent-3)))
	}

	if target.Sign() <= 0 {
		return big.NewInt(0)
	}
	return &target
}

// hashToDifficulty calculates the actual difficulty achieved by a hash.
// This is the inverse of difficultyToTarget: difficulty = maxTarget / hashInt
// Returns the difficulty value representing how "hard" this hash was to find.
func hashToDifficulty(hashInt *big.Int) float64 {
	if hashInt == nil || hashInt.Sign() <= 0 {
		return 0
	}

	// MaxTarget = 0x00000000FFFF0000000000000000000000000000000000000000000000000000
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Use big.Float for precision: difficulty = maxTarget / hashInt
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	hashFloat := new(big.Float).SetInt(hashInt)

	diffFloat := new(big.Float).Quo(maxTargetFloat, hashFloat)

	// Convert to float64
	diff, _ := diffFloat.Float64()
	return diff
}

// reverseBytes reverses a byte slice in place and returns it.
func reverseBytes(b []byte) []byte {
	result := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		result[i] = b[len(b)-1-i]
	}
	return result
}

// Stats returns validator statistics.
type ValidatorStats struct {
	Validated uint64
	Accepted  uint64
	Rejected  uint64
}

func (v *Validator) Stats() ValidatorStats {
	return ValidatorStats{
		Validated: v.validated.Load(),
		Accepted:  v.accepted.Load(),
		Rejected:  v.rejected.Load(),
	}
}

// DuplicateTracker tracks submitted shares to prevent duplicates.
// Thread-safe for concurrent access from multiple goroutines.
// Includes automatic time-based cleanup to prevent memory leaks.
type DuplicateTracker struct {
	mu          sync.RWMutex
	jobs        map[string]*jobShares
	lastCleanup int64  // Unix timestamp of last cleanup
	seq         uint64 // Monotonic counter for deterministic LRU ordering
}

// jobShares holds shares for a single job with activity tracking
type jobShares struct {
	shares       map[string]struct{}
	createdAt    int64  // Unix timestamp when job was first seen
	lastActivity int64  // FIX D-8: Unix timestamp of last share activity (for true LRU)
	lastSeq      uint64 // Monotonic sequence for deterministic LRU tiebreaking
}

// Maximum age for job entries (10 minutes)
const duplicateTrackerMaxAge = 10 * 60

// Maximum number of jobs to track (prevents unbounded memory growth)
const maxTrackedJobs = 1000

func NewDuplicateTracker() *DuplicateTracker {
	return &DuplicateTracker{
		jobs:        make(map[string]*jobShares),
		lastCleanup: time.Now().Unix(),
	}
}

// IsDuplicate checks if a share with the given parameters has already been submitted.
// The key includes ExtraNonce1 to distinguish shares from different miners - without this,
// two miners with different ExtraNonce1 but same ExtraNonce2+ntime+nonce would be
// incorrectly flagged as duplicates.
// DEPRECATED: Use RecordIfNew for atomic check-and-record to prevent TOCTOU races
func (dt *DuplicateTracker) IsDuplicate(jobID, extranonce1, extranonce2, ntime, nonce string) bool {
	// Key must include extranonce1 to scope duplicates per-session
	// SECURITY: Use colon delimiter to prevent key collision attacks
	// SECURITY: Normalize hex case to prevent duplicate share exploit
	// where submitting "aAbB" and "AaBb" bypasses dedup despite identical PoW hash
	key := strings.ToLower(extranonce1) + ":" + strings.ToLower(extranonce2) + ":" + strings.ToLower(ntime) + ":" + strings.ToLower(nonce)
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	if js, ok := dt.jobs[jobID]; ok {
		_, exists := js.shares[key]
		return exists
	}
	return false
}

// RecordIfNew atomically checks if a share is a duplicate and records it if not.
// SECURITY: This prevents TOCTOU race conditions where two goroutines could both
// pass the duplicate check and both record the same share.
// Returns true if the share was new (recorded), false if it was a duplicate (rejected).
func (dt *DuplicateTracker) RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce string) bool {
	// Key must include extranonce1 to scope duplicates per-session
	// SECURITY: Use colon delimiter to prevent key collision attacks where
	// different field combinations produce the same concatenated key
	// (e.g., "AA"+"BB" vs "A"+"ABB" both produce "AABB" without delimiter)
	// SECURITY: Normalize hex case to prevent duplicate share exploit
	key := strings.ToLower(extranonce1) + ":" + strings.ToLower(extranonce2) + ":" + strings.ToLower(ntime) + ":" + strings.ToLower(nonce)
	now := time.Now().Unix()

	dt.mu.Lock()
	defer dt.mu.Unlock()

	// Check if job exists and if share already recorded
	if js, ok := dt.jobs[jobID]; ok {
		if _, exists := js.shares[key]; exists {
			return false // Duplicate - reject
		}
		// Not a duplicate - record it atomically
		js.shares[key] = struct{}{}
		// FIX D-8: Update lastActivity for true LRU tracking
		dt.seq++
		js.lastActivity = now
		js.lastSeq = dt.seq
	} else {
		// SECURITY: Enforce maximum job count to prevent unbounded memory growth
		if len(dt.jobs) >= maxTrackedJobs {
			// Evict oldest job to make room
			dt.evictOldestJob()
		}

		// New job - create entry and record share
		dt.seq++
		dt.jobs[jobID] = &jobShares{
			shares:       make(map[string]struct{}),
			createdAt:    now,
			lastActivity: now, // FIX D-8: Initialize lastActivity
			lastSeq:      dt.seq,
		}
		dt.jobs[jobID].shares[key] = struct{}{}
	}

	// Periodic cleanup: every 60 seconds, remove old entries
	if now-dt.lastCleanup > 60 {
		dt.cleanupOldEntries(now)
		dt.lastCleanup = now
	}

	return true // New share - accepted
}

// evictOldestJob removes the least recently used job from the tracker.
// FIX D-8: Uses lastSeq (monotonic counter) for deterministic LRU eviction.
// Falls back to lastActivity timestamp for legacy entries without a sequence number.
// This prevents recently-active jobs from being evicted before old inactive ones.
// Must be called with mutex held.
func (dt *DuplicateTracker) evictOldestJob() {
	var oldestID string
	var oldestSeq uint64 = math.MaxUint64
	var oldestTime int64 = math.MaxInt64

	for id, js := range dt.jobs {
		// Primary: use monotonic sequence for deterministic ordering
		if js.lastSeq > 0 {
			if js.lastSeq < oldestSeq {
				oldestID = id
				oldestSeq = js.lastSeq
				oldestTime = js.lastActivity
			}
		} else {
			// Fallback for legacy entries: use lastActivity timestamp
			activityTime := js.lastActivity
			if activityTime == 0 {
				activityTime = js.createdAt
			}
			if oldestSeq == math.MaxUint64 && activityTime < oldestTime {
				oldestID = id
				oldestTime = activityTime
			}
		}
	}

	if oldestID != "" {
		delete(dt.jobs, oldestID)
	}
}

// Record stores a share's parameters to prevent future duplicates.
// The key includes ExtraNonce1 to scope duplicates per-session.
// DEPRECATED: Use RecordIfNew for atomic check-and-record to prevent TOCTOU races
func (dt *DuplicateTracker) Record(jobID, extranonce1, extranonce2, ntime, nonce string) {
	// Key must include extranonce1 to scope duplicates per-session
	// SECURITY: Use colon delimiter to prevent key collision attacks
	// SECURITY: Normalize hex case to prevent duplicate share exploit
	key := strings.ToLower(extranonce1) + ":" + strings.ToLower(extranonce2) + ":" + strings.ToLower(ntime) + ":" + strings.ToLower(nonce)
	now := time.Now().Unix()

	dt.mu.Lock()
	defer dt.mu.Unlock()

	if _, ok := dt.jobs[jobID]; !ok {
		dt.jobs[jobID] = &jobShares{
			shares:    make(map[string]struct{}),
			createdAt: now,
		}
	}
	dt.jobs[jobID].shares[key] = struct{}{}

	// Periodic cleanup: every 60 seconds, remove old entries
	if now-dt.lastCleanup > 60 {
		dt.cleanupOldEntries(now)
		dt.lastCleanup = now
	}
}

// cleanupOldEntries removes job entries older than duplicateTrackerMaxAge
// Must be called with mutex held
func (dt *DuplicateTracker) cleanupOldEntries(now int64) {
	cutoff := now - duplicateTrackerMaxAge
	for jobID, js := range dt.jobs {
		if js.createdAt < cutoff {
			delete(dt.jobs, jobID)
		}
	}
}

// CleanupJob removes duplicate tracking for a specific job.
func (dt *DuplicateTracker) CleanupJob(jobID string) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	delete(dt.jobs, jobID)

	// MEMORY OPTIMIZATION: If map is empty, recreate it to release memory
	if len(dt.jobs) == 0 {
		dt.jobs = make(map[string]*jobShares)
	}
}

// Stats returns the number of jobs and total shares being tracked
func (dt *DuplicateTracker) Stats() (jobs int, shares int) {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	jobs = len(dt.jobs)
	for _, js := range dt.jobs {
		shares += len(js.shares)
	}
	return
}

// bytesEqual compares two byte slices. Test-only helper.
func bytesEqual(a, b []byte) bool {
	return bytes.Equal(a, b)
}

// isJobStale checks if a job is too old to accept shares for.
// The maxAge parameter allows scaling based on coin block time.
// This prevents miners from submitting work for very old blocks.
//
// Scaling recommendation: max(blockTime * 2, 5 minutes)
// - Fast coins (DGB 15s): 5 min = 20 blocks
// - Medium coins (DOGE 60s): 5 min = 5 blocks
// - Slow coins (BTC 600s): 20 min = 2 blocks
func isJobStale(job *protocol.Job, maxAge time.Duration) bool {
	return time.Since(job.CreatedAt) > maxAge
}

// validateVersionRolling checks if the version bits comply with BIP320.
// The miner can only modify bits that are set in the mask.
// Any bits set outside the mask indicate a protocol violation.
func validateVersionRolling(versionBits, mask uint32) bool {
	// Check that no bits are set outside the mask
	// Valid: (versionBits & ^mask) == 0
	// This means all set bits in versionBits must also be set in mask
	return (versionBits &^ mask) == 0
}

// parseNonce parses a hex nonce string into a uint32.
// Used for nonce tracking/exhaustion detection, not for block header construction.
func parseNonce(nonceHex string) (uint32, error) {
	if len(nonceHex) != 8 {
		return 0, fmt.Errorf("invalid nonce length: expected 8 hex chars, got %d", len(nonceHex))
	}
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return 0, fmt.Errorf("invalid nonce hex: %w", err)
	}
	// Stratum nonce is little-endian hex (native x86 byte order).
	// For tracking purposes, we interpret as little-endian to get the numeric value.
	return binary.LittleEndian.Uint32(nonceBytes), nil
}

// SecurityStats returns security-related validator statistics.
type SecurityStats struct {
	StaleShares        uint64 // Shares for old/invalid jobs
	NonceExhaustion    uint64 // Potential nonce exhaustion events
	VersionRollRejects uint64 // BIP320 violations
}

// SecurityStats returns security metrics.
func (v *Validator) SecurityStats() SecurityStats {
	return SecurityStats{
		StaleShares:        v.staleShares.Load(),
		NonceExhaustion:    v.nonceExhaustion.Load(),
		VersionRollRejects: v.versionRollRejects.Load(),
	}
}

// CleanupSession removes all tracking data for a disconnected session.
func (v *Validator) CleanupSession(sessionID uint64) {
	v.nonceTracker.RemoveSession(fmt.Sprintf("%d", sessionID))
}

// CleanupNonceTracker evicts stale nonce tracking entries for sessions
// that disconnected without a clean cleanup. Should be called periodically.
func (v *Validator) CleanupNonceTracker() {
	v.nonceTracker.CleanupInactiveSessions()
}
