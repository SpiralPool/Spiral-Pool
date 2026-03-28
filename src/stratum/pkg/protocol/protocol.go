// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package protocol defines the abstract Stratum protocol interface.
// This allows support for both Stratum V1 (JSON-RPC) and V2 (binary)
// behind a unified interface.
package protocol

import (
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Protocol defines the interface for Stratum protocol implementations.
// Both V1 and V2 implement this interface, allowing the pool to be
// protocol-agnostic in its core logic.
type Protocol interface {
	// Version returns the protocol version string (e.g., "stratum/1.0", "stratum/2.0")
	Version() string

	// Handshake performs the initial connection handshake.
	Handshake(conn net.Conn) (*Session, error)

	// Close gracefully closes a session.
	Close(session *Session) error

	// ReadMessage reads the next message from the session.
	ReadMessage(session *Session) (Message, error)

	// WriteMessage writes a message to the session.
	WriteMessage(session *Session, msg Message) error

	// Features
	SupportsVersionRolling() bool
	SupportsExtranonce() bool
}

// Message represents a protocol message (abstract)
type Message interface {
	// ID returns the message ID (for request/response matching)
	ID() interface{}

	// Method returns the method name (e.g., "mining.submit")
	Method() string

	// Params returns the message parameters
	Params() []interface{}

	// IsRequest returns true if this is a request message
	IsRequest() bool

	// IsResponse returns true if this is a response message
	IsResponse() bool
}

// Session represents an active miner connection.
// SECURITY: Fields accessed concurrently use atomic operations.
type Session struct {
	ID           uint64
	Conn         net.Conn
	Protocol     Protocol
	RemoteAddr   string
	ConnectedAt  time.Time
	lastActivity int64 // Unix nano timestamp - atomic access via methods

	// Miner identity
	WorkerName   string
	MinerAddress string
	UserAgent    string

	// Mining state
	ExtraNonce1        string
	ExtraNonce2Size    int
	VersionRollingMask uint32

	// Difficulty (atomic access via methods)
	// Uses job-based difficulty tracking:
	// - difficultyBits: current difficulty
	// - prevDifficultyBits: previous difficulty before last change
	// - minDiffBits: minimum difficulty in current grace window (for rapid vardiff ramp-up)
	// - diffChangeJobID: job ID when difficulty changed (shares for older jobs use old diff)
	difficultyBits     uint64
	prevDifficultyBits uint64 // Previous difficulty for grace period
	minDiffBits        uint64 // Minimum difficulty in grace window (tracks rapid ramp-up)
	graceWindowStart   int64  // Start of current grace window (Unix nano)
	diffChangeJobID    uint64 // Job ID when difficulty changed (atomic)
	diffChangeNano     int64  // Unix nano when difficulty changed (fallback)
	blockTimeSec       int32  // Blockchain block time in seconds (for dynamic grace period)

	// VARDIFF state (atomic access via methods)
	shareCount        uint64 // Atomic: use IncrementShareCount/GetShareCount
	lastShareTimeNano int64  // Unix nano timestamp - atomic access via SetLastShareTime/GetLastShareTime
	lastRetargetNano  int64  // Unix nano timestamp - atomic access via SetLastRetarget/GetLastRetarget

	// Statistics (atomic access via methods)
	validShares   uint64 // Atomic: use IncrementValidShares/GetValidShares
	invalidShares uint64 // Atomic: use IncrementInvalidShares/GetInvalidShares
	staleShares   uint64 // Atomic: use IncrementStaleShares/GetStaleShares

	// State flags (atomic access via methods)
	// SECURITY: These must be atomic to prevent race conditions where:
	// - Thread A checks Authorized, finds false
	// - Thread B sets Authorized to true
	// - Thread A rejects share due to stale read
	// Or worse: share accepted before authorization completes
	authorized uint32 // Atomic: 1 if authorized, 0 otherwise - use SetAuthorized/IsAuthorized
	subscribed uint32 // Atomic: 1 if subscribed, 0 otherwise - use SetSubscribed/IsSubscribed
	diffSent   uint32 // Atomic: 1 if initial difficulty has been sent, 0 otherwise
	jobSent    uint32 // Atomic: 1 if initial job has been sent, 0 otherwise

	// RED-TEAM: Pre-auth message counter to prevent subscribe spam attacks
	preAuthMessages uint32 // Atomic: messages received before authorization

}

// SetDiffSent atomically marks that initial difficulty has been sent.
// Returns true if this call set the flag (first caller wins).
func (s *Session) SetDiffSent() bool {
	return atomic.CompareAndSwapUint32(&s.diffSent, 0, 1)
}

// IsDiffSent atomically checks if initial difficulty has been sent.
func (s *Session) IsDiffSent() bool {
	return atomic.LoadUint32(&s.diffSent) == 1
}

// SetJobSent atomically marks that initial job has been sent.
// Returns true if this call set the flag (first caller wins).
func (s *Session) SetJobSent() bool {
	return atomic.CompareAndSwapUint32(&s.jobSent, 0, 1)
}

// IsJobSent atomically checks if initial job has been sent.
func (s *Session) IsJobSent() bool {
	return atomic.LoadUint32(&s.jobSent) == 1
}

// SetAuthorized atomically marks the session as authorized.
// SECURITY: Must use atomic operation to prevent race conditions in share validation.
func (s *Session) SetAuthorized(authorized bool) {
	var val uint32
	if authorized {
		val = 1
	}
	atomic.StoreUint32(&s.authorized, val)
}

// IsAuthorized atomically checks if the session is authorized.
// SECURITY: Must use atomic operation to prevent race conditions in share validation.
func (s *Session) IsAuthorized() bool {
	return atomic.LoadUint32(&s.authorized) == 1
}

// SetSubscribed atomically marks the session as subscribed.
// SECURITY: Must use atomic operation to prevent race conditions in message handling.
func (s *Session) SetSubscribed(subscribed bool) {
	var val uint32
	if subscribed {
		val = 1
	}
	atomic.StoreUint32(&s.subscribed, val)
}

// IsSubscribed atomically checks if the session is subscribed.
// SECURITY: Must use atomic operation to prevent race conditions in message handling.
func (s *Session) IsSubscribed() bool {
	return atomic.LoadUint32(&s.subscribed) == 1
}

// IncrementPreAuthMessages atomically increments the pre-auth message counter.
// RED-TEAM: Used to limit messages before authorization to prevent subscribe spam.
// Returns the new count after increment.
func (s *Session) IncrementPreAuthMessages() uint32 {
	return atomic.AddUint32(&s.preAuthMessages, 1)
}

// GetPreAuthMessages atomically returns the pre-auth message count.
func (s *Session) GetPreAuthMessages() uint32 {
	return atomic.LoadUint32(&s.preAuthMessages)
}

// SetBlockTime sets the blockchain's block time for this session.
// This is used to calculate dynamic grace periods for difficulty changes.
// Should be called when the session is created or when joining a coin pool.
func (s *Session) SetBlockTime(seconds int) {
	atomic.StoreInt32(&s.blockTimeSec, int32(seconds))
}

// GetBlockTime returns the blockchain's block time in seconds.
func (s *Session) GetBlockTime() int {
	return int(atomic.LoadInt32(&s.blockTimeSec))
}

// getGracePeriod calculates the appropriate grace period based on block time.
// Faster chains need shorter grace periods, slower chains need longer ones.
// Returns grace period in nanoseconds for use with time.Now().UnixNano().
//
// Grace period logic:
//   - Minimum: 30 seconds (even for 15s block chains)
//   - Maximum: 2 minutes (even for 600s block chains)
//   - Default: 2x block time, clamped to min/max
//
// This ensures:
//   - DGB (15s blocks): 30s grace (2 blocks worth)
//   - DOGE (60s blocks): 2 min grace (2 blocks worth, capped)
//   - LTC (150s blocks): 2 min grace (capped)
//   - BTC/BC2 (600s blocks): 2 min grace (capped)
func (s *Session) getGracePeriod() int64 {
	blockTime := atomic.LoadInt32(&s.blockTimeSec)
	if blockTime <= 0 {
		blockTime = 600 // Default to Bitcoin's 10-minute blocks
	}

	// Grace period = 2x block time, gives miner 2 blocks worth of time
	graceSec := int64(blockTime) * 2

	// Minimum 30 seconds - even fast chains need some buffer
	if graceSec < 30 {
		graceSec = 30
	}
	// Maximum 2 minutes - don't accept ancient shares
	if graceSec > 120 {
		graceSec = 120
	}

	return graceSec * int64(time.Second)
}

// SetDifficulty atomically sets the session's current difficulty.
// Stores the previous difficulty for grace period handling.
// Tracks minimum difficulty in the grace window for rapid vardiff ramp-up.
// Note: Call SetDiffChangeJobID separately after sending the new job.
func (s *Session) SetDifficulty(diff float64) {
	now := time.Now().UnixNano()
	newBits := math.Float64bits(diff)
	// Dynamic grace period based on blockchain's block time.
	// Faster chains (DGB 15s) get shorter periods, slower chains (BTC 600s) get longer.
	gracePeriod := s.getGracePeriod()

	// Store current as previous before updating
	currentBits := atomic.LoadUint64(&s.difficultyBits)
	if currentBits != 0 {
		atomic.StoreUint64(&s.prevDifficultyBits, currentBits)
		atomic.StoreInt64(&s.diffChangeNano, now)
	}

	// Track minimum difficulty in the grace window
	// This handles rapid vardiff ramp-up where multiple increases happen quickly
	// The grace window ensures shares computed at initial diff are valid even after
	// vardiff ramps up (500→2000→8000→20000)
	windowStart := atomic.LoadInt64(&s.graceWindowStart)
	minBits := atomic.LoadUint64(&s.minDiffBits)

	if windowStart == 0 {
		// First difficulty set - initialize grace window
		atomic.StoreInt64(&s.graceWindowStart, now)
		atomic.StoreUint64(&s.minDiffBits, newBits)
	} else if (now - windowStart) > gracePeriod {
		// Grace window expired - start new window
		// CRITICAL FIX: When starting a new window after expiry, the miner may still
		// be working on shares at the OLD (current) difficulty. The new window's minimum
		// should be the LOWER of the old difficulty and the new difficulty.
		// This ensures shares in-flight during the transition are accepted.
		atomic.StoreInt64(&s.graceWindowStart, now)
		if currentBits != 0 && math.Float64frombits(currentBits) < diff {
			// Difficulty is increasing - keep old difficulty as minimum
			atomic.StoreUint64(&s.minDiffBits, currentBits)
		} else {
			// Difficulty is decreasing or first time - use new difficulty
			atomic.StoreUint64(&s.minDiffBits, newBits)
		}
	} else {
		// Within existing grace window - keep tracking minimum
		// This is the KEY fix: we keep the lowest difficulty seen in the window
		// so that shares from the initial difficulty remain valid
		if minBits == 0 || diff < math.Float64frombits(minBits) {
			atomic.StoreUint64(&s.minDiffBits, newBits)
		}
		// Note: we do NOT update minDiffBits when difficulty increases
		// This preserves the lowest difficulty for validation
	}

	atomic.StoreUint64(&s.difficultyBits, newBits)
}

// SetDiffChangeJobID records the job ID when difficulty changed.
// Job-based tracking: shares for jobs older than this use the previous difficulty.
func (s *Session) SetDiffChangeJobID(jobID uint64) {
	atomic.StoreUint64(&s.diffChangeJobID, jobID)
}

// GetDiffChangeJobID returns the job ID when difficulty last changed.
func (s *Session) GetDiffChangeJobID() uint64 {
	return atomic.LoadUint64(&s.diffChangeJobID)
}

// GetDifficulty atomically gets the session's current difficulty.
func (s *Session) GetDifficulty() float64 {
	return math.Float64frombits(atomic.LoadUint64(&s.difficultyBits))
}

// GetPrevDifficulty returns the previous difficulty before the last change.
func (s *Session) GetPrevDifficulty() float64 {
	return math.Float64frombits(atomic.LoadUint64(&s.prevDifficultyBits))
}

// GetDifficultyForJob returns the appropriate difficulty for validating a share.
// CRITICAL: Always checks grace window minimum FIRST to handle rapid vardiff ramp-up.
// cgminer/Avalon miners don't apply new difficulty to work-in-progress, so shares
// may come in at the INITIAL difficulty even after multiple vardiff increases
// (e.g., 500→2000→8000→20000). The grace window tracks the MINIMUM difficulty seen,
// ensuring shares at the initial difficulty remain valid.
func (s *Session) GetDifficultyForJob(jobID uint64) float64 {
	currentDiff := math.Float64frombits(atomic.LoadUint64(&s.difficultyBits))

	// FIRST: Check grace window minimum - this is the CRITICAL path for cgminer
	// This MUST be checked before job-based tracking because:
	// - prevDiff only stores ONE previous difficulty
	// - Rapid vardiff: 500→2000→8000→20000 loses 500 after second change
	// - minDiffBits tracks the TRUE minimum across all changes in the window
	// Dynamic grace period based on blockchain's block time
	gracePeriod := s.getGracePeriod()
	windowStart := atomic.LoadInt64(&s.graceWindowStart)

	if windowStart > 0 && (time.Now().UnixNano()-windowStart) < gracePeriod {
		minBits := atomic.LoadUint64(&s.minDiffBits)
		if minBits != 0 {
			minDiff := math.Float64frombits(minBits)
			if minDiff < currentDiff {
				return minDiff
			}
		}
	}

	// SECOND: Job-based tracking for single difficulty changes
	// This handles the case where miner submits for a job issued before diff change
	prevBits := atomic.LoadUint64(&s.prevDifficultyBits)
	diffChangeJob := atomic.LoadUint64(&s.diffChangeJobID)

	if prevBits != 0 && diffChangeJob > 0 && jobID < diffChangeJob {
		prevDiff := math.Float64frombits(prevBits)
		if prevDiff < currentDiff {
			return prevDiff
		}
	}

	return currentDiff
}

// GetDifficultyWithGrace returns the minimum difficulty during grace period.
// DEPRECATED: Use GetDifficultyForJob(jobID) for proper job-based tracking.
// Kept for backward compatibility.
func (s *Session) GetDifficultyWithGrace() float64 {
	currentDiff := math.Float64frombits(atomic.LoadUint64(&s.difficultyBits))
	prevBits := atomic.LoadUint64(&s.prevDifficultyBits)
	changeTime := atomic.LoadInt64(&s.diffChangeNano)

	// No previous difficulty or no change recorded
	if prevBits == 0 || changeTime == 0 {
		return currentDiff
	}

	// Dynamic grace period based on blockchain's block time
	gracePeriod := s.getGracePeriod()
	if time.Now().UnixNano()-changeTime < gracePeriod {
		prevDiff := math.Float64frombits(prevBits)
		// Return the lower of the two difficulties during grace period
		if prevDiff < currentDiff {
			return prevDiff
		}
	}

	return currentDiff
}

// GetMinDifficultyInWindow returns the minimum difficulty in the current grace window.
// This is used by the validator to accept shares at the lowest difficulty seen
// during rapid vardiff ramp-up (e.g., 500→2000→8000→20000).
func (s *Session) GetMinDifficultyInWindow() float64 {
	// Dynamic grace period based on blockchain's block time
	gracePeriod := s.getGracePeriod()
	windowStart := atomic.LoadInt64(&s.graceWindowStart)

	if windowStart > 0 && (time.Now().UnixNano()-windowStart) < gracePeriod {
		minBits := atomic.LoadUint64(&s.minDiffBits)
		if minBits != 0 {
			return math.Float64frombits(minBits)
		}
	}

	// Outside grace window or no minimum tracked - return current difficulty
	return math.Float64frombits(atomic.LoadUint64(&s.difficultyBits))
}

// SetLastActivity atomically sets the last activity timestamp.
func (s *Session) SetLastActivity(t time.Time) {
	atomic.StoreInt64(&s.lastActivity, t.UnixNano())
}

// GetLastActivity atomically gets the last activity timestamp.
func (s *Session) GetLastActivity() time.Time {
	nano := atomic.LoadInt64(&s.lastActivity)
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// IncrementShareCount atomically increments and returns the new share count.
func (s *Session) IncrementShareCount() uint64 {
	return atomic.AddUint64(&s.shareCount, 1)
}

// GetShareCount atomically gets the current share count.
func (s *Session) GetShareCount() uint64 {
	return atomic.LoadUint64(&s.shareCount)
}

// SetLastShareTime atomically sets the last share submission time.
// SECURITY: Uses atomic int64 (UnixNano) to prevent data races.
func (s *Session) SetLastShareTime(t time.Time) {
	atomic.StoreInt64(&s.lastShareTimeNano, t.UnixNano())
}

// GetLastShareTime atomically gets the last share submission time.
func (s *Session) GetLastShareTime() time.Time {
	nano := atomic.LoadInt64(&s.lastShareTimeNano)
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// SetLastRetarget atomically sets the last retarget time.
// SECURITY: Uses atomic int64 (UnixNano) to prevent data races.
func (s *Session) SetLastRetarget(t time.Time) {
	atomic.StoreInt64(&s.lastRetargetNano, t.UnixNano())
}

// GetLastRetarget atomically gets the last retarget time.
func (s *Session) GetLastRetarget() time.Time {
	nano := atomic.LoadInt64(&s.lastRetargetNano)
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// IncrementValidShares atomically increments the valid shares count.
func (s *Session) IncrementValidShares() uint64 {
	return atomic.AddUint64(&s.validShares, 1)
}

// GetValidShares atomically gets the valid shares count.
func (s *Session) GetValidShares() uint64 {
	return atomic.LoadUint64(&s.validShares)
}

// IncrementInvalidShares atomically increments the invalid shares count.
func (s *Session) IncrementInvalidShares() uint64 {
	return atomic.AddUint64(&s.invalidShares, 1)
}

// GetInvalidShares atomically gets the invalid shares count.
func (s *Session) GetInvalidShares() uint64 {
	return atomic.LoadUint64(&s.invalidShares)
}

// IncrementStaleShares atomically increments the stale shares count.
func (s *Session) IncrementStaleShares() uint64 {
	return atomic.AddUint64(&s.staleShares, 1)
}

// GetStaleShares atomically gets the stale shares count.
func (s *Session) GetStaleShares() uint64 {
	return atomic.LoadUint64(&s.staleShares)
}

// JobState represents the lifecycle state of a mining job.
// This provides explicit state tracking for audit trails and formal verification.
type JobState int

const (
	// JobStateCreated - Job has been generated from block template
	JobStateCreated JobState = iota
	// JobStateIssued - Job has been assigned to the broadcast queue
	JobStateIssued
	// JobStateActive - Job is currently being worked on by miners
	JobStateActive
	// JobStateInvalidated - Job is no longer valid (new block found, timeout)
	JobStateInvalidated
	// JobStateSolved - Job resulted in a valid block
	JobStateSolved
)

// String returns the string representation of JobState.
func (s JobState) String() string {
	switch s {
	case JobStateCreated:
		return "created"
	case JobStateIssued:
		return "issued"
	case JobStateActive:
		return "active"
	case JobStateInvalidated:
		return "invalidated"
	case JobStateSolved:
		return "solved"
	default:
		return "unknown"
	}
}

// Job represents a mining job to be sent to miners.
// THREAD SAFETY: The State field is protected by stateMu mutex. Use SetState/GetState methods.
type Job struct {
	ID             string
	PrevBlockHash  string
	CoinBase1      string
	CoinBase2      string
	MerkleBranches []string
	Version        string
	NBits          string
	NTime          string
	CleanJobs      bool

	// Internal
	Height     uint64
	Target     []byte
	Difficulty float64
	CreatedAt  time.Time

	// State tracking for formal verification and audit trails
	// Lifecycle: Created → Issued → Active → {Invalidated | Solved}
	// THREAD SAFETY: Protected by stateMu - use SetState/GetState methods
	stateMu        sync.RWMutex // Protects State, StateChangedAt, InvalidReason
	State          JobState
	StateChangedAt time.Time
	InvalidReason  string // Set when State == JobStateInvalidated

	// Version rolling
	VersionRollingAllowed bool
	VersionRollingMask    uint32

	// Block submission data (stored for constructing full block when found)
	TransactionData []string // Raw transaction hex data from block template

	// RawPrevBlockHash is the daemon-format (unformatted) previous block hash.
	// Used for stale detection by comparing against the job manager's lastBlockHash.
	RawPrevBlockHash string

	// NetworkTarget is the exact 256-bit target from getblocktemplate (hex string).
	// Used for block detection instead of converting float64 difficulty or compact bits.
	// This matches the daemon's exact target computation with zero precision loss.
	NetworkTarget string

	// Block reward (satoshis) - used for recording confirmed blocks
	CoinbaseValue int64

	// ═══════════════════════════════════════════════════════════════════════════
	// MERGE MINING (AUXPOW) FIELDS
	// ═══════════════════════════════════════════════════════════════════════════
	// These fields are populated when merge mining is enabled.
	// For non-merge-mining jobs, IsMergeJob is false and these are empty.

	// IsMergeJob indicates this job supports merge mining with auxiliary chains.
	IsMergeJob bool

	// AuxBlocks contains data for each auxiliary chain being merge mined.
	// For single aux chain (e.g., Dogecoin), this has one entry.
	// For multiple aux chains, this contains all configured chains.
	AuxBlocks []AuxBlockData

	// AuxMerkleRoot is the root of the aux merkle tree (32 bytes, hex).
	// This is embedded in the parent coinbase with magic marker 0xfabe6d6d.
	AuxMerkleRoot string

	// AuxMerkleBranch is the merkle path from first aux block to aux root.
	// For single aux chain, this is empty (root = block hash).
	AuxMerkleBranch []string

	// AuxTreeSize is the number of leaves in the aux merkle tree.
	// For single aux chain, this is 1.
	AuxTreeSize uint32

	// AuxMerkleNonce is the nonce used in chain slot calculation.
	// This affects which slot each aux chain occupies in the tree.
	AuxMerkleNonce uint32
}

// AuxBlockData contains aux block information for merge mining.
// This is stored in the Job structure for share validation.
type AuxBlockData struct {
	// Symbol is the aux chain ticker (e.g., "DOGE").
	Symbol string

	// ChainID is the unique chain identifier (e.g., 98 for Dogecoin).
	ChainID int32

	// Hash is the aux block header hash (internal byte order, little-endian).
	// This is what gets embedded in the aux merkle tree.
	Hash []byte

	// Target is the difficulty target for this aux block.
	Target []byte

	// Height is the aux block height.
	Height uint64

	// CoinbaseValue is the block reward in satoshis.
	CoinbaseValue int64

	// ChainIndex is the slot in the aux merkle tree.
	ChainIndex int

	// Difficulty is human-readable difficulty (for logging/metrics).
	Difficulty float64

	// Bits is the compact target representation.
	Bits uint32
}

// SetState transitions the job to a new state with timestamp.
// INVARIANT: Jobs cannot transition from Invalidated → Active or Solved → any state.
// THREAD SAFETY: This method is safe for concurrent use.
func (j *Job) SetState(newState JobState, reason string) bool {
	j.stateMu.Lock()
	defer j.stateMu.Unlock()

	// Enforce state machine rules
	switch j.State {
	case JobStateInvalidated:
		// Terminal state - cannot transition out
		return false
	case JobStateSolved:
		// Terminal state - cannot transition out
		return false
	}

	j.State = newState
	j.StateChangedAt = time.Now()
	if newState == JobStateInvalidated {
		j.InvalidReason = reason
	}
	return true
}

// GetState returns the current job state.
// THREAD SAFETY: This method is safe for concurrent use.
func (j *Job) GetState() JobState {
	j.stateMu.RLock()
	defer j.stateMu.RUnlock()
	return j.State
}

// IsValid returns true if the job is in a state that accepts shares.
// THREAD SAFETY: This method is safe for concurrent use.
func (j *Job) IsValid() bool {
	j.stateMu.RLock()
	defer j.stateMu.RUnlock()
	return j.State == JobStateActive || j.State == JobStateIssued || j.State == JobStateCreated
}

// Clone returns a deep copy of the Job without copying the mutex.
// State fields are read under lock; the returned Job has a fresh zero-valued stateMu.
func (j *Job) Clone() *Job {
	j.stateMu.RLock()
	state := j.State
	stateChangedAt := j.StateChangedAt
	invalidReason := j.InvalidReason
	j.stateMu.RUnlock()

	// Deep-copy slices to prevent shared backing arrays
	var merkleBranches []string
	if j.MerkleBranches != nil {
		merkleBranches = make([]string, len(j.MerkleBranches))
		copy(merkleBranches, j.MerkleBranches)
	}
	var target []byte
	if j.Target != nil {
		target = append([]byte(nil), j.Target...)
	}
	var txData []string
	if j.TransactionData != nil {
		txData = make([]string, len(j.TransactionData))
		copy(txData, j.TransactionData)
	}
	var auxBlocks []AuxBlockData
	if j.AuxBlocks != nil {
		auxBlocks = make([]AuxBlockData, len(j.AuxBlocks))
		for i, ab := range j.AuxBlocks {
			auxBlocks[i] = AuxBlockData{
				Symbol:        ab.Symbol,
				ChainID:       ab.ChainID,
				Hash:          append([]byte(nil), ab.Hash...),
				Target:        append([]byte(nil), ab.Target...),
				Height:        ab.Height,
				CoinbaseValue: ab.CoinbaseValue,
				ChainIndex:    ab.ChainIndex,
				Difficulty:    ab.Difficulty,
				Bits:          ab.Bits,
			}
		}
	}
	var auxMerkleBranch []string
	if j.AuxMerkleBranch != nil {
		auxMerkleBranch = make([]string, len(j.AuxMerkleBranch))
		copy(auxMerkleBranch, j.AuxMerkleBranch)
	}
	return &Job{
		ID:                    j.ID,
		PrevBlockHash:         j.PrevBlockHash,
		CoinBase1:             j.CoinBase1,
		CoinBase2:             j.CoinBase2,
		MerkleBranches:        merkleBranches,
		Version:               j.Version,
		NBits:                 j.NBits,
		NTime:                 j.NTime,
		CleanJobs:             j.CleanJobs,
		Height:                j.Height,
		Target:                target,
		Difficulty:            j.Difficulty,
		CreatedAt:             j.CreatedAt,
		State:                 state,
		StateChangedAt:        stateChangedAt,
		InvalidReason:         invalidReason,
		VersionRollingAllowed: j.VersionRollingAllowed,
		VersionRollingMask:    j.VersionRollingMask,
		TransactionData:       txData,
		RawPrevBlockHash:      j.RawPrevBlockHash,
		NetworkTarget:         j.NetworkTarget,
		CoinbaseValue:         j.CoinbaseValue,
		IsMergeJob:            j.IsMergeJob,
		AuxBlocks:             auxBlocks,
		AuxMerkleRoot:         j.AuxMerkleRoot,
		AuxMerkleBranch:       auxMerkleBranch,
		AuxTreeSize:           j.AuxTreeSize,
		AuxMerkleNonce:        j.AuxMerkleNonce,
	}
}

// Share represents a submitted share from a miner.
type Share struct {
	// Identity
	SessionID    uint64
	JobID        string
	MinerAddress string
	WorkerName   string

	// Share data - CRITICAL: Both ExtraNonce1 AND ExtraNonce2 are required for valid block construction
	// ExtraNonce1 is assigned per-session by the pool
	// ExtraNonce2 is chosen by the miner for each share
	// Coinbase = CoinBase1 + ExtraNonce1 + ExtraNonce2 + CoinBase2
	ExtraNonce1 string // Session-assigned extranonce (from pool)
	ExtraNonce2 string // Miner-chosen extranonce (from share submission)
	NTime       string
	Nonce       string
	VersionBits uint32

	// Validation results
	Difficulty    float64
	MinDifficulty float64 // Minimum acceptable difficulty (from session profile)
	NetworkDiff   float64
	BlockHeight   uint64
	IsBlock       bool
	BlockHash     string

	// Metadata
	IPAddress   string
	UserAgent   string
	SubmittedAt time.Time
}

// ShareResult represents the result of share validation.
type ShareResult struct {
	Accepted      bool
	IsBlock       bool
	BlockHash     string
	BlockHex       string // Full serialized block for submission to daemon (only set when IsBlock=true)
	BlockBuildError string // Error from buildFullBlock if block serialization failed (IsBlock=true but BlockHex="")
	RejectReason   string
	CoinbaseValue int64 // Block reward in satoshis (only set when IsBlock=true)

	// ActualDifficulty is the actual difficulty of the submitted hash.
	// This is calculated from the hash value itself (maxTarget / hashInt) and represents
	// how "hard" the miner actually worked, which may be much higher than the assigned
	// share difficulty. Used for best-share tracking and lucky share statistics.
	ActualDifficulty float64

	// Block diagnostic data (only set when IsBlock=true, for rejection analysis)
	PrevBlockHash string        // Previous block hash the block was built on
	JobAge        time.Duration // How old the job was when block was found

	// SilentDuplicate indicates this share was a duplicate but we told the miner it was accepted.
	// Used to prevent cgminer retry floods while not double-crediting the share.
	// When true: Accepted=true (for miner) but share should NOT be persisted or credited.
	SilentDuplicate bool

	// TargetSource records which target source was used for block detection.
	// One of: "gbt", "compact_bits", "float64_diff", "none".
	// Used for observability — if "float64_diff" appears frequently, GBT target population is broken.
	TargetSource string

	// AuxResults contains results for auxiliary chains (merge mining).
	// When a share meets an aux chain's difficulty target, the corresponding
	// result will have IsBlock=true with the AuxPoW proof ready for submission.
	AuxResults []AuxBlockResult
}

// AuxBlockResult contains the result of checking a share against an aux chain target.
type AuxBlockResult struct {
	// Symbol is the aux chain ticker (e.g., "DOGE").
	Symbol string

	// ChainID is the aux chain's unique identifier.
	ChainID int32

	// Height is the aux block height.
	Height uint64

	// IsBlock is true if the share meets this aux chain's difficulty target.
	IsBlock bool

	// BlockHash is the aux block hash (display order, big-endian hex).
	// Only set when IsBlock is true.
	BlockHash string

	// AuxPowHex is the serialized AuxPoW proof for submission via submitauxblock RPC.
	// Only set when IsBlock is true.
	AuxPowHex string

	// CoinbaseValue is the aux block reward in satoshis.
	CoinbaseValue int64

	// Error contains any error message during validation/proof construction.
	Error string
}

// Common reject reasons
const (
	RejectReasonNone                  = ""
	RejectReasonDuplicate             = "duplicate"
	RejectReasonLowDifficulty         = "low-difficulty"
	RejectReasonStale                 = "stale"
	RejectReasonInvalidJob            = "job-not-found"
	RejectReasonInvalidNonce          = "invalid-nonce"
	RejectReasonInvalidTime           = "invalid-time"
	RejectReasonInvalidSolution       = "invalid-solution"
	RejectReasonInvalidVersionRolling = "invalid-version-rolling" // BIP320 violation
)

// Methods defines standard Stratum method names
var Methods = struct {
	Subscribe           string
	Authorize           string
	Submit              string
	Notify              string
	SetDifficulty       string
	SetExtranonce       string // Hashrate rental: dynamic extranonce assignment for multiplexed miners
	Reconnect           string
	GetVersion          string
	Configure           string
	SuggestDifficulty   string
	ExtranonceSubscribe string
	Ping                string
	ShowMessage         string
}{
	Subscribe:           "mining.subscribe",
	Authorize:           "mining.authorize",
	Submit:              "mining.submit",
	Notify:              "mining.notify",
	SetDifficulty:       "mining.set_difficulty",
	SetExtranonce:       "mining.set_extranonce",
	Reconnect:           "client.reconnect",
	GetVersion:          "client.get_version",
	Configure:           "mining.configure",
	SuggestDifficulty:   "mining.suggest_difficulty",
	ExtranonceSubscribe: "mining.extranonce.subscribe",
	Ping:                "mining.ping",
	ShowMessage:         "client.show_message",
}
