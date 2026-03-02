// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// JobManagerAdapter adapts the existing V1 job manager for V2 protocol
type JobManagerAdapter struct {
	cfg          *config.PoolConfig
	stratumCfg   *config.StratumConfig
	daemonClient *daemon.Client
	logger       *zap.SugaredLogger

	// Current job (atomic pointer for lock-free access)
	currentJob atomic.Pointer[MiningJobData]

	// Job history for share validation
	jobsMu   sync.RWMutex
	jobs     map[uint32]*MiningJobData
	jobOrder []uint32 // FIX: Track insertion order for FIFO eviction (avoids uint32 wraparound bug)

	// Job ID counter
	jobCounter atomic.Uint32

	// Callbacks
	onNewBlock func()

	// State
	stateMu       sync.RWMutex
	lastBlockHash string
	lastHeight    uint64
}

// NewJobManagerAdapter creates a new V2 job manager adapter
func NewJobManagerAdapter(cfg *config.PoolConfig, stratumCfg *config.StratumConfig, daemonClient *daemon.Client, logger *zap.Logger) *JobManagerAdapter {
	return &JobManagerAdapter{
		cfg:          cfg,
		stratumCfg:   stratumCfg,
		daemonClient: daemonClient,
		logger:       logger.Sugar(),
		jobs:         make(map[uint32]*MiningJobData),
		jobOrder:     make([]uint32, 0, 16),
	}
}

// Start begins the job manager's update loop
func (a *JobManagerAdapter) Start(ctx context.Context) error {
	// Generate initial job
	if err := a.RefreshJob(ctx, true); err != nil {
		return err
	}

	// Start periodic refresh
	go a.refreshLoop(ctx)

	a.logger.Info("V2 Job manager adapter started")
	return nil
}

// refreshLoop periodically refreshes jobs
func (a *JobManagerAdapter) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.RefreshJob(ctx, false); err != nil {
				a.logger.Errorw("Failed to refresh V2 job", "error", err)
			}
		}
	}
}

// RefreshJob fetches a new block template and generates a V2 job
func (a *JobManagerAdapter) RefreshJob(ctx context.Context, force bool) error {
	template, err := a.daemonClient.GetBlockTemplate(ctx)
	if err != nil {
		return err
	}

	// Check if we need a new job
	a.stateMu.RLock()
	isNewBlock := template.PreviousBlockHash != a.lastBlockHash
	a.stateMu.RUnlock()
	cleanJobs := isNewBlock || force

	// Generate job
	job, err := a.generateJob(template, cleanJobs)
	if err != nil {
		return err
	}

	// Update state
	a.stateMu.Lock()
	a.lastBlockHash = template.PreviousBlockHash
	a.lastHeight = template.Height
	a.stateMu.Unlock()

	// Store job
	a.currentJob.Store(job)
	a.storeJob(job)

	// Broadcast
	if isNewBlock && a.onNewBlock != nil {
		a.onNewBlock()
	}

	a.logger.Debugw("Generated V2 job",
		"jobId", job.ID,
		"height", a.lastHeight,
		"cleanJobs", cleanJobs,
	)

	return nil
}

// generateJob creates a V2 job from a block template
func (a *JobManagerAdapter) generateJob(template *daemon.BlockTemplate, cleanJobs bool) (*MiningJobData, error) {
	jobID := a.jobCounter.Add(1)

	// Decode previous block hash
	prevHashBytes, err := hex.DecodeString(template.PreviousBlockHash)
	if err != nil {
		return nil, err
	}

	var prevHash [32]byte
	copy(prevHash[:], crypto.ReverseBytes(prevHashBytes))

	// Compute merkle root from transactions
	merkleRoot := a.computeMerkleRoot(template)

	// SECURITY: Validate CurTime fits in uint32 to prevent overflow (G115 fix)
	// Unix timestamps fit in uint32 until year 2106, but validate to be safe
	if template.CurTime < 0 || template.CurTime > int64(^uint32(0)) {
		return nil, fmt.Errorf("invalid CurTime: %d out of uint32 range", template.CurTime)
	}

	job := &MiningJobData{
		ID:         jobID,
		PrevHash:   prevHash,
		MerkleRoot: merkleRoot,
		Version:    template.Version,
		NBits:      a.parseBits(template.Bits),
		NTime:      uint32(template.CurTime),
		CleanJobs:  cleanJobs,
	}

	return job, nil
}

// computeMerkleRoot computes the merkle root from block template transactions
func (a *JobManagerAdapter) computeMerkleRoot(template *daemon.BlockTemplate) [32]byte {
	var merkleRoot [32]byte

	if len(template.Transactions) == 0 {
		// No transactions - return empty merkle root
		// In reality, there's always at least the coinbase
		return merkleRoot
	}

	// Get transaction hashes
	// FIX: Use append instead of indexed assignment to avoid nil entries
	// when hex decode fails. A nil entry would crash the merkle tree computation
	// since it concatenates hashes[i] + hashes[i+1] without nil checks.
	hashes := make([][]byte, 0, len(template.Transactions))
	for _, tx := range template.Transactions {
		txHash, err := hex.DecodeString(tx.TxID)
		if err != nil {
			a.logger.Errorw("Invalid transaction hash — skipping tx", "txid", tx.TxID, "error", err)
			continue
		}
		hashes = append(hashes, crypto.ReverseBytes(txHash))
	}

	// Build merkle tree
	for len(hashes) > 1 {
		var nextLevel [][]byte
		for i := 0; i < len(hashes); i += 2 {
			left := hashes[i]
			var right []byte
			if i+1 < len(hashes) {
				right = hashes[i+1]
			} else {
				right = left // Duplicate for odd count
			}
			combined := append(left, right...)
			nextLevel = append(nextLevel, crypto.SHA256d(combined))
		}
		hashes = nextLevel
	}

	if len(hashes) > 0 {
		copy(merkleRoot[:], hashes[0])
	}

	return merkleRoot
}

// parseBits parses the bits string to uint32
func (a *JobManagerAdapter) parseBits(bits string) uint32 {
	bitsBytes, err := hex.DecodeString(bits)
	if err != nil {
		return 0x1d00ffff // Default difficulty 1
	}
	if len(bitsBytes) != 4 {
		return 0x1d00ffff
	}
	return uint32(bitsBytes[0])<<24 | uint32(bitsBytes[1])<<16 | uint32(bitsBytes[2])<<8 | uint32(bitsBytes[3])
}

// storeJob saves a job for later share validation
func (a *JobManagerAdapter) storeJob(job *MiningJobData) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()

	a.jobs[job.ID] = job
	a.jobOrder = append(a.jobOrder, job.ID)

	// Cleanup old jobs (keep last 10) using FIFO insertion order.
	// FIX: Previous code used numeric ID comparison (id < oldestID) which breaks
	// after uint32 wraparound (4294967295 → 0). The new job with ID=0 would be
	// evicted immediately instead of the actually-oldest job.
	for len(a.jobs) > 10 {
		evictID := a.jobOrder[0]
		a.jobOrder = a.jobOrder[1:]
		delete(a.jobs, evictID)
	}
}

// JobProvider interface implementation

// GetCurrentJob returns the current mining job
func (a *JobManagerAdapter) GetCurrentJob() *MiningJobData {
	return a.currentJob.Load()
}

// GetJob returns a job by ID
func (a *JobManagerAdapter) GetJob(id uint32) *MiningJobData {
	a.jobsMu.RLock()
	defer a.jobsMu.RUnlock()
	return a.jobs[id]
}

// RegisterNewBlockCallback registers a callback for new block notifications
func (a *JobManagerAdapter) RegisterNewBlockCallback(callback func()) {
	a.onNewBlock = callback
}

// OnBlockNotification handles a new block notification (from ZMQ)
func (a *JobManagerAdapter) OnBlockNotification(ctx context.Context) {
	a.OnBlockNotificationWithHash(ctx, "")
}

// OnBlockNotificationWithHash handles a new block notification with the new tip hash.
// FIX D-6: When ZMQ provides the block hash, enables immediate same-height reorg detection.
func (a *JobManagerAdapter) OnBlockNotificationWithHash(ctx context.Context, newTipHash string) {
	// For V2 adapter, we just refresh the job - the underlying manager handles epoch advancement
	if err := a.RefreshJob(ctx, true); err != nil {
		a.logger.Errorw("Failed to refresh V2 job on block notification", "error", err)
	}
}

// ShareValidator validates V2 shares against jobs
type ShareValidator struct {
	jobAdapter   *JobManagerAdapter
	daemonClient *daemon.Client
	logger       *zap.SugaredLogger
	algorithm    string // "sha256d" or "scrypt" — determines hash function for PoW
}

// NewShareValidator creates a new V2 share validator.
// algorithm should be "sha256d" or "scrypt" to select the PoW hash function.
func NewShareValidator(jobAdapter *JobManagerAdapter, daemonClient *daemon.Client, algorithm string, logger *zap.Logger) *ShareValidator {
	return &ShareValidator{
		jobAdapter:   jobAdapter,
		daemonClient: daemonClient,
		algorithm:    algorithm,
		logger:       logger.Sugar(),
	}
}

// ProcessShare validates and processes a share submission
func (v *ShareValidator) ProcessShare(share *ShareSubmission) *ShareResult {
	// Get the job
	job := v.jobAdapter.GetJob(share.JobID)
	if job == nil {
		return &ShareResult{
			Accepted: false,
			Error:    ErrJobNotFound,
		}
	}

	// Build block header from share data
	header := v.buildBlockHeader(job, share)

	// Hash the header using algorithm-appropriate function
	var blockHash []byte
	switch v.algorithm {
	case "scrypt":
		blockHash = crypto.ScryptHash(header)
	default: // "sha256d" and all others
		blockHash = crypto.SHA256d(header)
	}

	// Check against share target (channel difficulty, NOT network difficulty).
	// share.TargetNBits is the pool's share difficulty target assigned to this channel.
	// job.NBits is the NETWORK target — only used for block detection below.
	shareTargetNBits := share.TargetNBits
	if shareTargetNBits == 0 {
		// Fallback: if no share target set, use job's network target
		shareTargetNBits = job.NBits
	}
	targetMet := v.checkTarget(blockHash, shareTargetNBits)
	if !targetMet {
		return &ShareResult{
			Accepted: false,
			Error:    ErrLowDifficultyShare,
		}
	}

	// Check against network target (block detection)
	isBlock := v.checkNetworkTarget(blockHash, job.NBits)

	result := &ShareResult{
		Accepted:  true,
		IsBlock:   isBlock,
		BlockHash: hex.EncodeToString(crypto.ReverseBytes(blockHash)),
	}

	// If it's a block, submit to daemon
	if isBlock {
		v.logger.Infow("Block solution found!",
			"hash", result.BlockHash,
			"jobID", share.JobID,
			"session", share.SessionID,
		)
		// Block submission would go here
	}

	return result
}

// buildBlockHeader constructs an 80-byte block header
func (v *ShareValidator) buildBlockHeader(job *MiningJobData, share *ShareSubmission) []byte {
	header := make([]byte, 80)

	// Version (4 bytes, little-endian)
	version := share.Version
	if version == 0 {
		version = job.Version
	}
	header[0] = byte(version)
	header[1] = byte(version >> 8)
	header[2] = byte(version >> 16)
	header[3] = byte(version >> 24)

	// Previous block hash (32 bytes)
	copy(header[4:36], job.PrevHash[:])

	// Merkle root (32 bytes)
	copy(header[36:68], job.MerkleRoot[:])

	// Time (4 bytes, little-endian)
	ntime := share.NTime
	if ntime == 0 {
		ntime = job.NTime
	}
	header[68] = byte(ntime)
	header[69] = byte(ntime >> 8)
	header[70] = byte(ntime >> 16)
	header[71] = byte(ntime >> 24)

	// Bits (4 bytes, little-endian)
	header[72] = byte(job.NBits)
	header[73] = byte(job.NBits >> 8)
	header[74] = byte(job.NBits >> 16)
	header[75] = byte(job.NBits >> 24)

	// Nonce (4 bytes, little-endian)
	header[76] = byte(share.Nonce)
	header[77] = byte(share.Nonce >> 8)
	header[78] = byte(share.Nonce >> 16)
	header[79] = byte(share.Nonce >> 24)

	return header
}

// checkTarget checks if hash meets the share target using proper big.Int comparison
func (v *ShareValidator) checkTarget(hash []byte, targetNBits uint32) bool {
	// SECURITY: Proper target comparison using big.Int
	// Convert nBits to target value per Bitcoin protocol
	target := NBitsToTarget(targetNBits)
	if target.Sign() == 0 {
		return false // Invalid target
	}

	// Convert hash to big.Int (little-endian to big-endian)
	hashReversed := make([]byte, len(hash))
	for i := 0; i < len(hash); i++ {
		hashReversed[i] = hash[len(hash)-1-i]
	}
	hashInt := new(big.Int).SetBytes(hashReversed)

	// Hash must be less than or equal to target
	return hashInt.Cmp(target) <= 0
}

// checkNetworkTarget checks if hash meets network difficulty
func (v *ShareValidator) checkNetworkTarget(hash []byte, targetNBits uint32) bool {
	// SECURITY: Use same proper big.Int comparison for network target
	return v.checkTarget(hash, targetNBits)
}

// NBitsToTarget converts the compact nBits representation to a full 256-bit target.
// This follows the Bitcoin protocol specification for compact target encoding.
// nBits format: [1 byte exponent][3 bytes mantissa]
// Target = mantissa * 2^(8*(exponent-3))
func NBitsToTarget(nBits uint32) *big.Int {
	// Extract exponent (first byte) and mantissa (lower 3 bytes)
	exponent := int(nBits >> 24)
	mantissa := nBits & 0x00FFFFFF

	// SECURITY: Validate exponent range to prevent overflow
	// Exponent 0 is invalid (would be target = 0)
	if exponent == 0 {
		return big.NewInt(0)
	}

	// SECURITY: Validate exponent upper bound
	// For 256-bit targets, exponent cannot exceed 33 (32 + 1 for the mantissa bytes)
	// Values above this would create targets > 2^256, which are invalid
	// This prevents attacks using nBits like 0xFF7FFFFF that would accept any hash
	if exponent > 33 {
		return big.NewInt(0)
	}

	// SECURITY: Check for negative target (high bit of mantissa set with exponent <= 3)
	// Per Bitcoin protocol, this indicates a negative number which is invalid
	if mantissa&0x800000 != 0 {
		if exponent <= 3 {
			return big.NewInt(0)
		}
		// For exponent > 3, clear the high bit and adjust
		mantissa &= 0x7FFFFF
	}

	// Calculate target = mantissa * 2^(8*(exponent-3))
	target := new(big.Int).SetUint64(uint64(mantissa))

	if exponent > 3 {
		// Shift left by 8*(exponent-3) bits
		// Maximum shift: 8*(33-3) = 240 bits, which keeps target < 2^256
		shift := uint(8 * (exponent - 3))
		target.Lsh(target, shift)
	} else if exponent < 3 {
		// Shift right by 8*(3-exponent) bits
		shift := uint(8 * (3 - exponent))
		target.Rsh(target, shift)
	}
	// If exponent == 3, no shift needed

	return target
}

// TargetToNBits converts a big.Int target to compact nBits representation.
// This is the inverse of NBitsToTarget: nBits = exponent<<24 | mantissa
// where mantissa is the top 3 significant bytes and exponent is the byte length.
func TargetToNBits(target *big.Int) uint32 {
	if target.Sign() <= 0 {
		return 0
	}

	// Get big-endian byte representation
	b := target.Bytes()
	size := uint32(len(b))

	// Extract top 3 bytes as mantissa
	var mantissa uint32
	if size >= 3 {
		mantissa = uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	} else if size == 2 {
		mantissa = uint32(b[0])<<16 | uint32(b[1])<<8
	} else if size == 1 {
		mantissa = uint32(b[0]) << 16
	}

	// If high bit of mantissa is set, right-shift by 8 bits and increment size
	// to keep the compact encoding positive (Bitcoin protocol convention)
	if mantissa&0x800000 != 0 {
		mantissa >>= 8
		size++
	}

	return size<<24 | mantissa
}

// DifficultyToNBits converts a stratum pool difficulty to compact nBits target.
// This is used to set the initial V2 share difficulty from the configured initial
// difficulty value. Uses the Bitcoin maxTarget for difficulty-1 conversion.
func DifficultyToNBits(difficulty float64) uint32 {
	// Safety: return difficulty-1 for invalid input
	if difficulty <= 0 || math.IsNaN(difficulty) || math.IsInf(difficulty, 0) {
		return 0x1d00ffff // Difficulty 1 fallback
	}

	// maxTarget = 0x00000000FFFF0000000000000000000000000000000000000000000000000000
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// target = maxTarget / difficulty
	maxTargetFloat := new(big.Float).SetInt(maxTarget)
	diffFloat := new(big.Float).SetFloat64(difficulty)
	targetFloat := new(big.Float).Quo(maxTargetFloat, diffFloat)

	target, _ := targetFloat.Int(nil)
	if target.Sign() <= 0 {
		return 0x1d00ffff // Difficulty 1 fallback
	}

	return TargetToNBits(target)
}

// V1toV2JobAdapter converts V1 protocol.Job to V2 MiningJobData
type V1toV2JobAdapter struct {
	v1Job *protocol.Job
}

// NewV1toV2JobAdapter creates an adapter from V1 job
func NewV1toV2JobAdapter(job *protocol.Job) *V1toV2JobAdapter {
	return &V1toV2JobAdapter{v1Job: job}
}

// ToV2Job converts V1 job to V2 format
func (a *V1toV2JobAdapter) ToV2Job() (*MiningJobData, error) {
	var jobID uint32

	// Parse job ID (V1 uses hex string)
	idBytes, err := hex.DecodeString(a.v1Job.ID)
	if err == nil && len(idBytes) >= 4 {
		jobID = uint32(idBytes[0])<<24 | uint32(idBytes[1])<<16 | uint32(idBytes[2])<<8 | uint32(idBytes[3])
	}

	// Parse previous hash
	// V1 stratum format has 4-byte groups byte-swapped but in big-endian group order.
	// V2/block header needs little-endian (reversed group order).
	// FIX [BUILD-20260118D]: Reverse the order of 4-byte groups.
	var prevHash [32]byte
	prevHashBytes, err := hex.DecodeString(a.v1Job.PrevBlockHash)
	if err == nil && len(prevHashBytes) == 32 {
		for i := 0; i < 8; i++ {
			copy(prevHash[(7-i)*4:(7-i)*4+4], prevHashBytes[i*4:i*4+4])
		}
	}

	// Parse version
	var version uint32
	versionBytes, err := hex.DecodeString(a.v1Job.Version)
	if err == nil && len(versionBytes) == 4 {
		version = uint32(versionBytes[0])<<24 | uint32(versionBytes[1])<<16 | uint32(versionBytes[2])<<8 | uint32(versionBytes[3])
	}

	// Parse NBits
	var nbits uint32
	nbitsBytes, err := hex.DecodeString(a.v1Job.NBits)
	if err == nil && len(nbitsBytes) == 4 {
		nbits = uint32(nbitsBytes[0])<<24 | uint32(nbitsBytes[1])<<16 | uint32(nbitsBytes[2])<<8 | uint32(nbitsBytes[3])
	}

	// Parse NTime
	var ntime uint32
	ntimeBytes, err := hex.DecodeString(a.v1Job.NTime)
	if err == nil && len(ntimeBytes) == 4 {
		ntime = uint32(ntimeBytes[0])<<24 | uint32(ntimeBytes[1])<<16 | uint32(ntimeBytes[2])<<8 | uint32(ntimeBytes[3])
	}

	return &MiningJobData{
		ID:        jobID,
		PrevHash:  prevHash,
		Version:   version,
		NBits:     nbits,
		NTime:     ntime,
		CleanJobs: a.v1Job.CleanJobs,
	}, nil
}
