// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package coin provides cryptocurrency-specific abstractions for multi-coin support.
//
// Each cryptocurrency has unique characteristics for address encoding, coinbase
// construction, and mining algorithms. This package defines a common interface
// and registry for coin implementations.
package coin

import (
	"fmt"
	"math/big"
	"strings"
)

// AddressType identifies the type of cryptocurrency address.
type AddressType int

const (
	AddressTypeUnknown AddressType = iota
	AddressTypeP2PKH               // Pay-to-Public-Key-Hash (legacy)
	AddressTypeP2SH                // Pay-to-Script-Hash
	AddressTypeP2WPKH              // Pay-to-Witness-Public-Key-Hash (native segwit)
	AddressTypeP2WSH               // Pay-to-Witness-Script-Hash
	AddressTypeP2TR                // Pay-to-Taproot
)

func (t AddressType) String() string {
	switch t {
	case AddressTypeP2PKH:
		return "P2PKH"
	case AddressTypeP2SH:
		return "P2SH"
	case AddressTypeP2WPKH:
		return "P2WPKH"
	case AddressTypeP2WSH:
		return "P2WSH"
	case AddressTypeP2TR:
		return "P2TR"
	default:
		return "Unknown"
	}
}

// BlockHeader contains the fields needed to construct a block header.
type BlockHeader struct {
	Version           uint32
	PreviousBlockHash []byte // 32 bytes, little-endian
	MerkleRoot        []byte // 32 bytes, little-endian
	Timestamp         uint32
	Bits              uint32
	Nonce             uint32
}

// Coin defines the interface for cryptocurrency-specific behavior.
// Each supported cryptocurrency implements this interface.
type Coin interface {
	// Identity returns basic coin information.
	Symbol() string // Ticker symbol: "DGB", "BTC", "BCH", "BC2"
	Name() string   // Full name: "DigiByte", "Bitcoin", "Bitcoin Cash"

	// Address validation and decoding.
	// ValidateAddress checks if an address is valid for this coin.
	ValidateAddress(address string) error
	// DecodeAddress extracts the pubkey hash and address type.
	DecodeAddress(address string) (pubKeyHash []byte, addrType AddressType, err error)

	// Coinbase construction.
	// BuildCoinbaseScript creates the output script for the coinbase transaction.
	BuildCoinbaseScript(params CoinbaseParams) ([]byte, error)

	// Block header operations.
	// SerializeBlockHeader serializes a block header to bytes.
	SerializeBlockHeader(header *BlockHeader) []byte
	// HashBlockHeader computes the block hash from serialized header.
	// Most coins use SHA256d, but some use different algorithms.
	HashBlockHeader(serialized []byte) []byte

	// Difficulty calculations.
	// TargetFromBits converts compact target (bits) to full target.
	TargetFromBits(bits uint32) *big.Int
	// DifficultyFromTarget calculates human-readable difficulty from target.
	DifficultyFromTarget(target *big.Int) float64
	// ShareDifficultyMultiplier returns the coin-specific difficulty multiplier.
	// This adjusts share difficulty for coins with non-standard diff1 targets.
	ShareDifficultyMultiplier() float64

	// Network parameters.
	DefaultRPCPort() int
	DefaultP2PPort() int
	P2PKHVersionByte() byte
	P2SHVersionByte() byte
	Bech32HRP() string // Human-readable part: "dgb", "bc", "ltc"

	// Mining characteristics.
	Algorithm() string    // "sha256d", "scrypt", "odocrypt"
	SupportsSegWit() bool // Does the coin support segregated witness?
	BlockTime() int       // Target block time in seconds

	// Coinbase requirements.
	// MinCoinbaseScriptLen returns minimum coinbase scriptSig length (BIP34).
	MinCoinbaseScriptLen() int
	// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
	CoinbaseMaturity() int

	// Chain verification.
	// GenesisBlockHash returns the expected genesis block hash for chain verification.
	// This should be compared against the node's getblockhash(0) to ensure correct chain.
	GenesisBlockHash() string
	// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
	// Returns an error if mismatched, indicating possible misconfiguration.
	VerifyGenesisBlock(nodeGenesisHash string) error
}

// CoinbaseParams contains parameters for building a coinbase script.
type CoinbaseParams struct {
	Height            uint64 // Block height (for BIP34)
	ExtraNonce        []byte // Extra nonce for uniqueness
	PoolAddress       string // Address to receive block reward
	BlockReward       int64  // Block reward in satoshis
	WitnessCommitment []byte // SegWit witness commitment (if applicable)
	CoinbaseMessage   string // Optional pool tag message
}

// CoinbaseResult contains the built coinbase transaction data.
type CoinbaseResult struct {
	TxData        []byte // Full coinbase transaction
	TxID          []byte // Transaction ID (hash)
	ScriptSig     []byte // Input script (for extranonce placement)
	ExtraNonceOff int    // Offset of extranonce in scriptSig
	ExtraNonceLen int    // Length of extranonce placeholder
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXILIARY PROOF OF WORK (MERGE MINING) SUPPORT
// ═══════════════════════════════════════════════════════════════════════════════
//
// Merge mining (AuxPoW) allows mining a parent chain and one or more auxiliary
// chains simultaneously by embedding auxiliary chain commitments into the parent
// chain coinbase and using the parent block's PoW as proof for auxiliary blocks.
//
// Example: Litecoin (parent) + Dogecoin (auxiliary)
// - Both use Scrypt algorithm
// - Dogecoin enabled AuxPoW at block 371,337
// - Parent coinbase contains aux merkle root with magic marker 0xfabe6d6d
// ═══════════════════════════════════════════════════════════════════════════════

// AuxPowCoin extends Coin for auxiliary proof-of-work capable coins.
// Coins that support being merge-mined as auxiliary chains implement this interface.
type AuxPowCoin interface {
	Coin

	// SupportsAuxPow returns true if this coin supports auxiliary proof of work.
	// CONSENSUS-CRITICAL: This determines whether blocks must include AuxPoW data.
	SupportsAuxPow() bool

	// AuxPowStartHeight returns the block height at which AuxPoW was enabled.
	// Blocks below this height use standard PoW; blocks at or above use AuxPoW.
	// CONSENSUS-CRITICAL: Submitting AuxPoW blocks below this height will be rejected.
	AuxPowStartHeight() uint64

	// ChainID returns the unique chain identifier for the aux merkle tree.
	// This is used to calculate the slot in the aux merkle tree.
	// CONSENSUS-CRITICAL: Wrong chain ID causes invalid aux blocks.
	// Dogecoin: 0x0062 (98 decimal)
	ChainID() int32

	// AuxPowVersionBit returns the version bit that indicates AuxPoW block.
	// Dogecoin uses bit 8 (0x100) in the version field.
	// CONSENSUS-CRITICAL: Blocks with AuxPoW must have this bit set.
	AuxPowVersionBit() uint32

	// ParseAuxBlockResponse parses the getauxblock RPC response.
	// Returns the aux block hash, target, and chain-specific data.
	ParseAuxBlockResponse(response map[string]interface{}) (*AuxBlock, error)

	// SerializeAuxPowProof serializes the complete AuxPoW proof for block submission.
	// This includes parent coinbase, merkle branches, and parent header.
	// CONSENSUS-CRITICAL: Serialization must exactly match node expectations.
	SerializeAuxPowProof(proof *AuxPowProof) ([]byte, error)
}

// CreateAuxBlockCoin is an optional interface for aux chains that use the newer
// createauxblock(address)/submitauxblock RPC pair instead of the older getauxblock RPC.
// Fractal Bitcoin uses this style. If an AuxPowCoin does NOT implement this interface,
// the pool defaults to getauxblock.
type CreateAuxBlockCoin interface {
	// UseCreateAuxBlock returns true if this coin uses createauxblock(address)
	// instead of getauxblock for fetching aux block templates.
	UseCreateAuxBlock() bool
}

// ParentChainCoin extends Coin for coins that can serve as merge mining parents.
// This is separate from AuxPowCoin because parent chains have different requirements.
type ParentChainCoin interface {
	Coin

	// CanBeParentFor returns true if this coin can serve as a merge mining parent
	// for the specified auxiliary chain algorithm.
	// Parent and aux must use the same PoW algorithm.
	CanBeParentFor(auxAlgorithm string) bool

	// CoinbaseAuxMarker returns the magic bytes that mark aux commitment in coinbase.
	// Standard: 0xfabe6d6d ("fabe mm" - "fake block merge mining")
	CoinbaseAuxMarker() []byte

	// MaxCoinbaseAuxSize returns the maximum size for aux data in coinbase.
	// This depends on the coin's coinbase script size limits.
	MaxCoinbaseAuxSize() int
}

// MultiAlgoCoin extends Coin for multi-algorithm blockchains like DigiByte.
// Multi-algo coins require the algorithm name to be passed to getblocktemplate
// so the daemon returns the correct difficulty target and version bits.
type MultiAlgoCoin interface {
	Coin
	// MultiAlgoGBTParam returns the algorithm name to pass as the second
	// parameter to getblocktemplate RPC (e.g., "sha256d", "scrypt").
	MultiAlgoGBTParam() string
}

// GBTRulesCoin extends Coin for coins that require custom getblocktemplate rules.
// Most coins use ["segwit"], but some require additional rules:
// - Litecoin requires ["mweb", "segwit"] for MimbleWimble Extension Blocks
type GBTRulesCoin interface {
	Coin
	// GBTRules returns the rules array for getblocktemplate RPC.
	// Example: ["segwit"] for most coins, ["mweb", "segwit"] for Litecoin.
	GBTRules() []string
}

// AuxBlock represents an auxiliary block template from getauxblock RPC.
type AuxBlock struct {
	// ChainID is the unique identifier for this aux chain.
	ChainID int32

	// Hash is the aux block header hash (what gets embedded in parent coinbase).
	// This is in internal byte order (little-endian).
	Hash []byte

	// Target is the difficulty target for this aux block.
	Target *big.Int

	// Height is the aux block height.
	Height uint64

	// CoinbaseValue is the block reward for finding this aux block (in satoshis).
	CoinbaseValue int64

	// PreviousBlockHash is the previous aux block hash.
	PreviousBlockHash []byte

	// ChainIndex is the slot in the aux merkle tree (derived from ChainID).
	// This is calculated as: chainID % (2^nonce_range)
	ChainIndex int

	// Bits is the compact target representation.
	Bits uint32
}

// AuxPowProof contains all data needed to prove aux block validity.
// This is what gets serialized and submitted with the aux block.
type AuxPowProof struct {
	// ParentCoinbase is the full parent coinbase transaction.
	ParentCoinbase []byte

	// ParentCoinbaseHash is SHA256d(ParentCoinbase).
	ParentCoinbaseHash []byte

	// CoinbaseMerkleBranch is the merkle path from coinbase to parent merkle root.
	CoinbaseMerkleBranch [][]byte

	// CoinbaseMerkleIndex is the index in the coinbase merkle branch (always 0).
	CoinbaseMerkleIndex int

	// AuxMerkleBranch is the merkle path from aux block hash to aux merkle root.
	AuxMerkleBranch [][]byte

	// AuxMerkleIndex is the slot of this aux chain in the aux merkle tree.
	AuxMerkleIndex int

	// ParentHeader is the 80-byte parent block header.
	ParentHeader []byte

	// ParentHash is the hash of ParentHeader (Scrypt for LTC/DOGE).
	ParentHash []byte
}

// AuxChainSlot calculates the merkle tree slot for a chain ID.
// CONSENSUS-CRITICAL: This must match the calculation in the aux chain node.
//
// The slot is calculated using a deterministic algorithm:
//
//	rand = merkleNonce
//	rand = rand * 1103515245 + 12345
//	rand = rand + chainID
//	rand = rand * 1103515245 + 12345
//	slot = rand % treeSize
//
// For single aux chain, treeSize=1 and slot=0.
func AuxChainSlot(chainID int32, merkleNonce uint32, treeSize uint32) int {
	if treeSize == 0 {
		treeSize = 1
	}
	rand := merkleNonce
	rand = rand*1103515245 + 12345
	rand = rand + uint32(chainID)
	rand = rand*1103515245 + 12345
	return int(rand % treeSize)
}

// ═══════════════════════════════════════════════════════════════════════════════
// ALGORITHM-AWARE HASHRATE HELPERS
// ═══════════════════════════════════════════════════════════════════════════════
//
// SHA-256d and Scrypt have vastly different hashrate scales:
// - SHA-256d: ASICs produce TH/s to PH/s (10^12 to 10^15 H/s)
// - Scrypt:   ASICs produce MH/s to GH/s (10^6 to 10^9 H/s)
//
// The hashrate formula is the same: hashrate = difficulty * 2^32 / time
// But display units must be algorithm-appropriate to be meaningful.
// ═══════════════════════════════════════════════════════════════════════════════

// HashrateDifficultyConstant is 2^32 used in hashrate calculations.
// hashrate = sum(difficulty) * 2^32 / time_seconds
const HashrateDifficultyConstant = 4.294967296e9 // 2^32 = 4,294,967,296

// HashrateUnit represents a hashrate magnitude with its suffix.
type HashrateUnit struct {
	Divisor float64
	Suffix  string
}

// Hashrate unit definitions for each algorithm.
var (
	// SHA256dHashrateUnits are appropriate for SHA-256d mining (ASIC scale).
	// Modern SHA-256d ASICs operate in TH/s to EH/s range.
	SHA256dHashrateUnits = []HashrateUnit{
		{1e18, "EH/s"}, // Exahash (network scale)
		{1e15, "PH/s"}, // Petahash (large pools)
		{1e12, "TH/s"}, // Terahash (single ASICs)
		{1e9, "GH/s"},  // Gigahash (older ASICs)
		{1e6, "MH/s"},  // Megahash (rarely used)
		{1e3, "KH/s"},  // Kilohash
		{1, "H/s"},     // Base unit
	}

	// ScryptHashrateUnits are appropriate for Scrypt mining (ASIC scale).
	// Scrypt ASICs are much slower than SHA-256d, typically MH/s to GH/s.
	ScryptHashrateUnits = []HashrateUnit{
		{1e15, "PH/s"}, // Petahash (network scale, rare)
		{1e12, "TH/s"}, // Terahash (large pools)
		{1e9, "GH/s"},  // Gigahash (modern ASICs like L7)
		{1e6, "MH/s"},  // Megahash (typical ASIC range)
		{1e3, "KH/s"},  // Kilohash (GPU mining)
		{1, "H/s"},     // Base unit
	}
)

// HashrateUnitsForAlgorithm returns the appropriate hashrate units for an algorithm.
func HashrateUnitsForAlgorithm(algorithm string) []HashrateUnit {
	switch algorithm {
	case "scrypt":
		return ScryptHashrateUnits
	case "sha256d", "sha256":
		return SHA256dHashrateUnits
	default:
		return SHA256dHashrateUnits // Default to SHA-256d
	}
}

// FormatHashrate formats a hashrate value with the appropriate unit for the algorithm.
// Returns both the formatted string and the unit suffix.
func FormatHashrate(hashrate float64, algorithm string) (formatted string, suffix string) {
	if hashrate <= 0 {
		return "0", "H/s"
	}

	units := HashrateUnitsForAlgorithm(algorithm)
	for _, unit := range units {
		if hashrate >= unit.Divisor {
			value := hashrate / unit.Divisor
			// Use appropriate precision based on magnitude
			if value >= 100 {
				return formatFloat(value, 1), unit.Suffix
			} else if value >= 10 {
				return formatFloat(value, 2), unit.Suffix
			}
			return formatFloat(value, 3), unit.Suffix
		}
	}
	return formatFloat(hashrate, 2), "H/s"
}

// FormatHashrateString returns a single formatted hashrate string with unit.
func FormatHashrateString(hashrate float64, algorithm string) string {
	formatted, suffix := FormatHashrate(hashrate, algorithm)
	return formatted + " " + suffix
}

// formatFloat formats a float with the specified decimal places, removing trailing zeros.
func formatFloat(value float64, decimals int) string {
	format := "%." + string(rune('0'+decimals)) + "f"
	s := fmt.Sprintf(format, value)
	// Trim trailing zeros after decimal point
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

// CalculateHashrate calculates hashrate from difficulty sum over a time window.
// This uses the SHA-256d formula: hashrate = difficulty * 2^32 / seconds.
// For algorithm-aware calculation (required for Scrypt coins), use CalculateHashrateForAlgorithm.
func CalculateHashrate(totalDifficulty float64, windowSeconds float64) float64 {
	if windowSeconds <= 0 {
		return 0
	}
	return totalDifficulty * HashrateDifficultyConstant / windowSeconds
}

// HashrateConstantForAlgorithm returns the correct hashrate-per-difficulty-unit constant.
//
// The stratum protocol uses different diff-1 targets per algorithm:
//   - SHA-256d: diff-1 target = 0x00000000FFFF0000... → expected hashes per diff unit ≈ 2^32
//   - Scrypt:   diff-1 target = 0x0000FFFF00000000... → expected hashes per diff unit ≈ 2^16 = 65536
//
// The ratio is the ShareDifficultyMultiplier (65536 for Scrypt).
// Using 2^32 for Scrypt would overstate hashrate by 65536x.
func HashrateConstantForAlgorithm(algorithm string) float64 {
	switch algorithm {
	case "scrypt":
		return 65536.0 // 2^16: Scrypt diff-1 = 65536 hashes
	default:
		return HashrateDifficultyConstant // 2^32: SHA-256d diff-1 = 4,294,967,296 hashes
	}
}

// CalculateHashrateForAlgorithm calculates hashrate from difficulty sum, accounting
// for the mining algorithm's diff-1 target convention.
//
//   - SHA-256d: hashrate = difficulty * 2^32 / seconds
//   - Scrypt:   hashrate = difficulty * 65536 / seconds
func CalculateHashrateForAlgorithm(totalDifficulty float64, windowSeconds float64, algorithm string) float64 {
	if windowSeconds <= 0 {
		return 0
	}
	return totalDifficulty * HashrateConstantForAlgorithm(algorithm) / windowSeconds
}

// AlgorithmFromCoinSymbol returns the algorithm for a coin symbol.
// This is a convenience function for code that only has the symbol.
func AlgorithmFromCoinSymbol(symbol string) string {
	switch strings.ToUpper(symbol) {
	case "LTC", "LITECOIN":
		return "scrypt"
	case "DOGE", "DOGECOIN":
		return "scrypt"
	case "DGB-SCRYPT", "DGB_SCRYPT":
		return "scrypt"
	case "PEP", "PEPECOIN", "MEME":
		return "scrypt"
	case "CAT", "CATCOIN":
		return "scrypt"
	case "DGB", "DIGIBYTE", "BTC", "BITCOIN", "BCH", "BITCOINCASH", "BC2", "BITCOINII", "NMC", "NAMECOIN", "SYS", "SYSCOIN", "XMY", "MYRIAD", "MYRIADCOIN", "FBTC", "FB", "FRACTALBTC", "FRACTAL":
		return "sha256d"
	default:
		return "sha256d" // Default to SHA-256d
	}
}

// IsScryptCoin returns true if the coin uses the Scrypt algorithm.
func IsScryptCoin(symbol string) bool {
	return AlgorithmFromCoinSymbol(symbol) == "scrypt"
}

// IsSHA256dCoin returns true if the coin uses the SHA-256d algorithm.
func IsSHA256dCoin(symbol string) bool {
	return AlgorithmFromCoinSymbol(symbol) == "sha256d"
}
