// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package auxpow provides auxiliary proof-of-work (merge mining) support for Spiral Pool.
//
// Merge mining allows a parent blockchain to provide proof-of-work for auxiliary
// blockchains that use the same mining algorithm. This is achieved by embedding
// commitments to auxiliary blocks in the parent coinbase transaction.
//
// Example: Litecoin (parent, Scrypt) + Dogecoin (auxiliary, Scrypt)
//
// Key concepts:
//   - Parent chain: The primary chain being mined (e.g., Litecoin)
//   - Auxiliary chain: Secondary chain(s) using parent's PoW (e.g., Dogecoin)
//   - Aux merkle root: Commitment to aux blocks embedded in parent coinbase
//   - AuxPoW proof: Data proving parent PoW is valid for aux block
//
// Reference: https://en.bitcoin.it/wiki/Merged_mining_specification
package auxpow

import (
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/daemon"
	"go.uber.org/zap"
)

// AuxChainConfig configures an auxiliary chain for merge mining.
type AuxChainConfig struct {
	// Symbol is the coin ticker (e.g., "DOGE").
	Symbol string

	// Coin is the coin implementation (must implement AuxPowCoin interface).
	Coin coin.AuxPowCoin

	// DaemonClient is the RPC client for the aux chain node.
	DaemonClient *daemon.Client

	// Address is the payout address for aux block rewards.
	Address string

	// Enabled indicates if this aux chain is active.
	Enabled bool
}

// AuxBlockData contains aux block information for job construction.
// This is what gets stored in the Job structure for share validation.
type AuxBlockData struct {
	// Symbol is the aux chain ticker (e.g., "DOGE").
	Symbol string

	// ChainID is the unique chain identifier (e.g., 98 for Dogecoin).
	ChainID int32

	// Hash is the aux block header hash (internal byte order, little-endian).
	// This is what gets embedded in the aux merkle tree.
	Hash []byte

	// Target is the difficulty target for this aux block.
	Target *big.Int

	// Height is the aux block height.
	Height uint64

	// CoinbaseValue is the block reward in satoshis.
	CoinbaseValue int64

	// ChainIndex is the slot in the aux merkle tree.
	ChainIndex int

	// Difficulty is human-readable difficulty (for logging).
	Difficulty float64

	// PreviousBlockHash is the previous aux block hash.
	PreviousBlockHash []byte

	// Bits is the compact target representation.
	Bits uint32

	// FetchedAt is when this aux block was fetched.
	FetchedAt time.Time
}

// AuxBlockResult contains the result of aux block validation and submission.
type AuxBlockResult struct {
	// Symbol is the aux chain ticker.
	Symbol string

	// ChainID is the aux chain's unique identifier.
	ChainID int32

	// Height is the aux block height.
	Height uint64

	// IsBlock is true if the share meets this aux chain's target.
	IsBlock bool

	// BlockHash is the aux block hash (display order, big-endian hex).
	BlockHash string

	// AuxPowHex is the serialized AuxPoW proof for submission.
	AuxPowHex string

	// CoinbaseValue is the block reward in satoshis.
	CoinbaseValue int64

	// Error contains any error message during validation.
	Error string

	// Submitted indicates if the aux block was submitted to the node.
	Submitted bool

	// Accepted indicates if the aux block was accepted by the node.
	Accepted bool

	// RejectReason is the reason for rejection (if not accepted).
	RejectReason string
}

// prevAuxState tracks the last-seen block state for an aux chain.
// Used for reorg detection: if the hash changes at the same or lower height,
// a reorg has occurred on the aux chain.
type prevAuxState struct {
	Hash   []byte
	Height uint64
}

// Manager coordinates auxiliary chain block templates for merge mining.
type Manager struct {
	// Configuration
	parentCoin coin.Coin
	auxConfigs []AuxChainConfig

	// Current aux blocks (atomic pointer for lock-free reads)
	currentAux atomic.Pointer[[]AuxBlockData]

	// Mutex for aux template updates
	auxMu sync.RWMutex

	// Refresh tracking
	lastRefresh   time.Time
	refreshErrors int

	// Previous aux block state for reorg detection (keyed by symbol)
	prevAuxHashes map[string]prevAuxState

	// Logger
	logger *zap.SugaredLogger
}

// MerkleTreeSize returns the smallest power of 2 >= n.
// This is used to calculate the aux merkle tree size for multiple aux chains.
func MerkleTreeSize(n int) int {
	if n <= 1 {
		return 1
	}
	size := 1
	for size < n {
		size *= 2
	}
	return size
}

// AuxDataSize is the size of aux commitment data in coinbase.
// Format: marker(4) + aux_root(32) + tree_size(4) + merkle_nonce(4) = 44 bytes
const AuxDataSize = 44

// AuxMarker is the magic bytes marking aux data in coinbase.
// This is the standard "fabe6d6d" marker used by all AuxPoW implementations.
var AuxMarker = []byte{0xfa, 0xbe, 0x6d, 0x6d}
