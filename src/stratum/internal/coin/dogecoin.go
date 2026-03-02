// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package coin - Dogecoin (DOGE) implementation.
//
// Dogecoin is a peer-to-peer cryptocurrency created in 2013. It uses the Scrypt
// algorithm for proof of work, with 1 minute block times.
//
// Key characteristics:
//   - Scrypt algorithm (same parameters as Litecoin: N=1024, r=1, p=1)
//   - 1 minute block time
//   - Fixed 10,000 DOGE block reward (no halving)
//   - AuxPoW (merged mining with Litecoin) enabled from block 371,337
//
// References:
//   - https://github.com/dogecoin/dogecoin
//   - https://dogecoin.com/
package coin

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/spiralpool/stratum/internal/crypto"
)

// Dogecoin mainnet address version bytes
const (
	DogecoinP2PKHVersion byte = 0x1e // Mainnet P2PKH starts with 'D'
	DogecoinP2SHVersion  byte = 0x16 // Mainnet P2SH starts with '9' or 'A'
	DogecoinBech32HRP         = "doge" // Dogecoin doesn't use bech32 widely, but define for future
)

// Dogecoin testnet/regtest address version bytes
// Note: Dogecoin regtest uses the same version bytes as Bitcoin testnet
const (
	DogecoinRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n' (111 decimal)
	DogecoinRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2' (196 decimal)
)

// Dogecoin network parameters
const (
	DogecoinDefaultP2PPort   = 22556 // P2P network port
	DogecoinDefaultRPCPort   = 22555 // RPC port
	DogecoinBlockTime        = 60    // Target block time: 1 minute
	DogecoinCoinbaseMaturity = 30    // Blocks before coinbase is spendable (was 100, reduced to 30)
	// Genesis block hash for chain verification
	// Hash: 1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691
	// Date: December 6, 2013
	DogecoinGenesisBlockHash = "1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691"
	// Block height at which AuxPoW (merged mining) was enabled
	DogecoinAuxPowStartHeight uint64 = 371337
)

// Dogecoin AuxPoW constants
// CONSENSUS-CRITICAL: These values must exactly match Dogecoin Core
const (
	// DogecoinChainID is the unique chain ID for Dogecoin in the aux merkle tree.
	// This value is specified in Dogecoin's chainparams.cpp as nAuxpowChainId.
	// CONSENSUS-CRITICAL: Using wrong chain ID will cause all aux blocks to be rejected.
	DogecoinChainID int32 = 0x0062 // 98 decimal

	// DogecoinAuxPowVersionBit is the version bit indicating AuxPoW block.
	// When this bit is set in the block version, the block uses auxiliary proof of work.
	// Defined in Dogecoin as BLOCK_VERSION_AUXPOW = (1 << 8)
	// CONSENSUS-CRITICAL: Must be set for all blocks after AuxPowStartHeight when using AuxPoW.
	DogecoinAuxPowVersionBit uint32 = 0x00000100 // Bit 8 = 256

	// DogecoinAuxPowVersionChainID is the chain ID embedded in version.
	// Version format: (chainID << 16) | AuxPowBit | baseVersion
	// For Dogecoin: 0x00620100 = (98 << 16) | 0x100 | version
	DogecoinAuxPowVersionChainID uint32 = 0x00620000
)

// DogecoinCoin implements the Coin interface for Dogecoin.
type DogecoinCoin struct{}

// NewDogecoinCoin creates a new Dogecoin coin instance.
func NewDogecoinCoin() *DogecoinCoin {
	return &DogecoinCoin{}
}

// Symbol returns the ticker symbol.
func (c *DogecoinCoin) Symbol() string {
	return "DOGE"
}

// Name returns the full coin name.
func (c *DogecoinCoin) Name() string {
	return "Dogecoin"
}

// ValidateAddress validates a Dogecoin address.
func (c *DogecoinCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Dogecoin address to its hash and type.
// Dogecoin primarily uses legacy P2PKH and P2SH addresses.
// SegWit/Bech32 is not widely adopted for Dogecoin.
func (c *DogecoinCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Dogecoin rarely uses bech32, but check for completeness (dogert1... for regtest)
	addrLower := strings.ToLower(address)
	hrp := DogecoinBech32HRP
	if strings.HasPrefix(addrLower, "dogert1") {
		hrp = "dogert" // Regtest bech32 prefix
	}
	if strings.HasPrefix(addrLower, hrp+"1") {
		decoded, err := decodeBech32Address(address, hrp)
		if err != nil {
			return nil, AddressTypeUnknown, err
		}
		if len(decoded) < 2 {
			return nil, AddressTypeUnknown, fmt.Errorf("decoded data too short")
		}
		witnessVersion := decoded[0]
		hash := decoded[1:]

		switch witnessVersion {
		case 0:
			if len(hash) == 20 {
				return hash, AddressTypeP2WPKH, nil
			} else if len(hash) == 32 {
				return hash, AddressTypeP2WSH, nil
			}
			return nil, AddressTypeUnknown, fmt.Errorf("invalid v0 witness program length: %d", len(hash))
		default:
			return nil, AddressTypeUnknown, fmt.Errorf("unsupported witness version: %d", witnessVersion)
		}
	}

	// Legacy Base58Check (D... for P2PKH, 9/A... for P2SH)
	if len(address) < 25 || len(address) > 35 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid address length: %d", len(address))
	}

	decoded, err := base58Decode(address)
	if err != nil {
		return nil, AddressTypeUnknown, fmt.Errorf("base58 decode failed: %w", err)
	}

	if len(decoded) != 25 {
		return nil, AddressTypeUnknown, fmt.Errorf("decoded length %d, expected 25", len(decoded))
	}

	// Verify checksum
	payload := decoded[:21]
	checksum := decoded[21:]
	expectedChecksum := doubleSHA256(payload)[:4]

	// SECURITY: Use constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare(checksum, expectedChecksum) != 1 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid checksum")
	}

	version := decoded[0]
	hash := decoded[1:21]

	switch version {
	case DogecoinP2PKHVersion, DogecoinRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case DogecoinP2SHVersion, DogecoinRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x (expected 0x1e/0x6f for P2PKH or 0x16/0xc4 for P2SH)", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *DogecoinCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
	hash, addrType, err := c.DecodeAddress(params.PoolAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid pool address: %w", err)
	}

	switch addrType {
	case AddressTypeP2PKH:
		// OP_DUP OP_HASH160 <20 bytes> OP_EQUALVERIFY OP_CHECKSIG
		script := make([]byte, 25)
		script[0] = 0x76 // OP_DUP
		script[1] = 0xa9 // OP_HASH160
		script[2] = 0x14 // PUSH 20 bytes
		copy(script[3:23], hash)
		script[23] = 0x88 // OP_EQUALVERIFY
		script[24] = 0xac // OP_CHECKSIG
		return script, nil

	case AddressTypeP2SH:
		// OP_HASH160 <20 bytes> OP_EQUAL
		script := make([]byte, 23)
		script[0] = 0xa9 // OP_HASH160
		script[1] = 0x14 // PUSH 20 bytes
		copy(script[2:22], hash)
		script[22] = 0x87 // OP_EQUAL
		return script, nil

	case AddressTypeP2WPKH:
		// OP_0 <20 bytes>
		script := make([]byte, 22)
		script[0] = 0x00
		script[1] = 0x14
		copy(script[2:22], hash)
		return script, nil

	case AddressTypeP2WSH:
		// OP_0 <32 bytes>
		script := make([]byte, 34)
		script[0] = 0x00
		script[1] = 0x20
		copy(script[2:34], hash)
		return script, nil

	default:
		return nil, fmt.Errorf("unsupported address type: %v", addrType)
	}
}

// SerializeBlockHeader serializes an 80-byte block header.
// Dogecoin uses the same block header format as Bitcoin/Litecoin.
func (c *DogecoinCoin) SerializeBlockHeader(header *BlockHeader) []byte {
	buf := make([]byte, 80)

	binary.LittleEndian.PutUint32(buf[0:4], header.Version)
	copy(buf[4:36], header.PreviousBlockHash)
	copy(buf[36:68], header.MerkleRoot)
	binary.LittleEndian.PutUint32(buf[68:72], header.Timestamp)
	binary.LittleEndian.PutUint32(buf[72:76], header.Bits)
	binary.LittleEndian.PutUint32(buf[76:80], header.Nonce)

	return buf
}

// HashBlockHeader hashes a serialized block header using Scrypt.
// Dogecoin uses the same Scrypt parameters as Litecoin.
func (c *DogecoinCoin) HashBlockHeader(serialized []byte) []byte {
	return crypto.ScryptHash(serialized)
}

// TargetFromBits converts compact bits representation to target.
func (c *DogecoinCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid
	if bits&0x00800000 != 0 {
		return new(big.Int)
	}

	target := new(big.Int).SetUint64(uint64(mantissa))

	if exponent <= 3 {
		target.Rsh(target, uint(8*(3-exponent)))
	} else {
		target.Lsh(target, uint(8*(exponent-3)))
	}

	return target
}

// DifficultyFromTarget calculates difficulty from target.
func (c *DogecoinCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Dogecoin uses the same difficulty 1 target as Bitcoin/Litecoin
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	diff1Float := new(big.Float).SetInt(diff1Target)
	targetFloat := new(big.Float).SetInt(target)

	result := new(big.Float).Quo(diff1Float, targetFloat)
	difficulty, accuracy := result.Float64()

	if accuracy == big.Below {
		return difficulty
	}

	return difficulty
}

// ShareDifficultyMultiplier returns the multiplier for share difficulty.
// Scrypt diff-1 target is 0x0000FFFF... (2^240) vs Bitcoin's 0x00000000FFFF... (2^224).
// Ratio is 2^16 = 65536. Pool must account for this when validating share targets.
func (c *DogecoinCoin) ShareDifficultyMultiplier() float64 {
	return 65536.0
}

// GBTRules returns the rules for getblocktemplate.
// Dogecoin has SegWit code disabled (fMineWitnessTx = false), no rules required.
func (c *DogecoinCoin) GBTRules() []string {
	return []string{}
}

// DefaultRPCPort returns the default RPC port.
func (c *DogecoinCoin) DefaultRPCPort() int {
	return DogecoinDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *DogecoinCoin) DefaultP2PPort() int {
	return DogecoinDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *DogecoinCoin) P2PKHVersionByte() byte {
	return DogecoinP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *DogecoinCoin) P2SHVersionByte() byte {
	return DogecoinP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *DogecoinCoin) Bech32HRP() string {
	return DogecoinBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *DogecoinCoin) Algorithm() string {
	return "scrypt"
}

// SupportsSegWit returns whether the coin supports SegWit.
// Dogecoin does not have SegWit activated on mainnet as of 2024.
func (c *DogecoinCoin) SupportsSegWit() bool {
	return false
}

// BlockTime returns the target block time in seconds.
func (c *DogecoinCoin) BlockTime() int {
	return DogecoinBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *DogecoinCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *DogecoinCoin) CoinbaseMaturity() int {
	return DogecoinCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *DogecoinCoin) GenesisBlockHash() string {
	return DogecoinGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *DogecoinCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(DogecoinGenesisBlockHash) {
		return fmt.Errorf("DOGE genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Dogecoin Core",
			nodeGenesisHash, DogecoinGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXPOW (MERGE MINING) IMPLEMENTATION
// ═══════════════════════════════════════════════════════════════════════════════
//
// Dogecoin enabled AuxPoW at block 371,337 to allow merged mining with Litecoin.
// This means Litecoin miners can simultaneously mine Dogecoin blocks by embedding
// a commitment to the Dogecoin block in their Litecoin coinbase transaction.
//
// The AuxPoW proof contains:
// 1. The parent (Litecoin) coinbase transaction
// 2. The merkle branch from coinbase to parent merkle root
// 3. The merkle branch from aux block hash to aux merkle root
// 4. The parent block header (which contains the PoW)
//
// Reference: https://en.bitcoin.it/wiki/Merged_mining_specification
// ═══════════════════════════════════════════════════════════════════════════════

// SupportsAuxPow returns whether the coin supports auxiliary proof of work (merged mining).
// Dogecoin enabled AuxPoW at block 371,337 for merged mining with Litecoin.
func (c *DogecoinCoin) SupportsAuxPow() bool {
	return true
}

// AuxPowStartHeight returns the block height at which AuxPoW was enabled.
func (c *DogecoinCoin) AuxPowStartHeight() uint64 {
	return DogecoinAuxPowStartHeight
}

// ChainID returns Dogecoin's unique chain identifier for the aux merkle tree.
// CONSENSUS-CRITICAL: This must be exactly 98 (0x62) to match Dogecoin Core.
func (c *DogecoinCoin) ChainID() int32 {
	return DogecoinChainID
}

// AuxPowVersionBit returns the version bit that indicates an AuxPoW block.
// CONSENSUS-CRITICAL: Blocks using AuxPoW must have this bit set in their version.
func (c *DogecoinCoin) AuxPowVersionBit() uint32 {
	return DogecoinAuxPowVersionBit
}

// ParseAuxBlockResponse parses the getauxblock RPC response from Dogecoin Core.
//
// Dogecoin's getauxblock returns:
//
//	{
//	  "hash": "block header hash (hex, big-endian display order)",
//	  "chainid": 98,
//	  "previousblockhash": "previous block hash (hex)",
//	  "coinbasevalue": 10000000000000,
//	  "bits": "1b3cc366",
//	  "height": 12345678,
//	  "target": "000000000003c366..." (optional, full target hex)
//	}
//
// CONSENSUS-CRITICAL: Hash byte order conversion must be exact.
func (c *DogecoinCoin) ParseAuxBlockResponse(response map[string]interface{}) (*AuxBlock, error) {
	auxBlock := &AuxBlock{
		ChainID: DogecoinChainID,
	}

	// Parse hash (convert from display order to internal order)
	hashHex, ok := response["hash"].(string)
	if !ok || len(hashHex) != 64 {
		return nil, fmt.Errorf("invalid or missing aux block hash")
	}
	hash, err := hex.DecodeString(hashHex)
	if err != nil {
		return nil, fmt.Errorf("invalid aux block hash hex: %w", err)
	}
	// Reverse from display (big-endian) to internal (little-endian)
	auxBlock.Hash = reverseBytesCopy(hash)

	// Parse chain ID (verify it matches expected)
	if chainID, ok := response["chainid"].(float64); ok {
		if int32(chainID) != DogecoinChainID {
			return nil, fmt.Errorf("chain ID mismatch: got %d, expected %d", int32(chainID), DogecoinChainID)
		}
	}

	// Parse target from bits field (required for Dogecoin)
	// CRITICAL: Always use bits-derived target for Dogecoin. The daemon validates
	// against nBits (compact target), and the explicit "target" field in regtest
	// mode returns incorrect/garbage values that are impossibly strict.
	// Example: bits=207fffff (easy regtest target) but target=0xffff7f (impossible)
	if bitsHex, ok := response["bits"].(string); ok && len(bitsHex) == 8 {
		bitsBytes, err := hex.DecodeString(bitsHex)
		if err == nil && len(bitsBytes) == 4 {
			bits := binary.BigEndian.Uint32(bitsBytes)
			auxBlock.Bits = bits
			auxBlock.Target = c.TargetFromBits(bits)
		}
	}
	// NOTE: We intentionally ignore the explicit "target" field from getauxblock.
	// Dogecoin Core validates blocks against nBits, not the explicit target.
	// In regtest mode, the explicit target field is unreliable (returns garbage).
	if auxBlock.Target == nil {
		return nil, fmt.Errorf("missing bits field in aux block response")
	}

	// Parse height
	if height, ok := response["height"].(float64); ok {
		auxBlock.Height = uint64(height)
	}

	// Parse coinbase value (block reward in satoshis)
	if value, ok := response["coinbasevalue"].(float64); ok {
		auxBlock.CoinbaseValue = int64(value)
	}

	// Parse previous block hash
	if prevHashHex, ok := response["previousblockhash"].(string); ok && len(prevHashHex) == 64 {
		prevHash, err := hex.DecodeString(prevHashHex)
		if err == nil {
			auxBlock.PreviousBlockHash = reverseBytesCopy(prevHash)
		}
	}

	// For single aux chain, chain index is always 0
	// For multiple chains, this would be calculated from chain ID and tree size
	auxBlock.ChainIndex = 0

	return auxBlock, nil
}

// SerializeAuxPowProof serializes the complete AuxPoW proof for block submission.
//
// CONSENSUS-CRITICAL: The exact serialization format must match Dogecoin Core.
// Any deviation will cause the aux block to be rejected by the network.
//
// AuxPoW serialization format (from Dogecoin/Namecoin specification):
//  1. Parent coinbase transaction (variable length, full serialized tx)
//  2. Parent block hash (32 bytes, little-endian) - hash of parent block header
//  3. Coinbase merkle branch:
//     a. Number of hashes (varint)
//     b. Hash array (32 bytes each, little-endian)
//     c. Branch side mask (uint32 LE) - bit i indicates if hash i is on left (0) or right (1)
//  4. Aux merkle branch:
//     a. Number of hashes (varint)
//     b. Hash array (32 bytes each, little-endian)
//     c. Branch side mask (uint32 LE) - encodes path to aux block in tree
//  5. Parent block header (80 bytes)
func (c *DogecoinCoin) SerializeAuxPowProof(proof *AuxPowProof) ([]byte, error) {
	if proof == nil {
		return nil, fmt.Errorf("nil proof")
	}

	buf := new(bytes.Buffer)

	// 1. Parent coinbase transaction (full tx, not just hash)
	if len(proof.ParentCoinbase) == 0 {
		return nil, fmt.Errorf("empty parent coinbase")
	}
	buf.Write(proof.ParentCoinbase)

	// 2. Parent block hash (32 bytes, little-endian)
	// CRITICAL: This field is required by Dogecoin Core's CAuxPow deserializer.
	// The hash is already in internal (little-endian) byte order.
	if len(proof.ParentHash) != 32 {
		return nil, fmt.Errorf("invalid parent hash length: got %d, expected 32", len(proof.ParentHash))
	}
	buf.Write(proof.ParentHash)

	// 3. Coinbase merkle branch
	// Number of branch hashes (varint)
	buf.Write(crypto.EncodeVarInt(uint64(len(proof.CoinbaseMerkleBranch))))
	// Branch hashes (from coinbase to merkle root)
	for i, hash := range proof.CoinbaseMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid coinbase branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	// Branch side mask (uint32, little-endian)
	// For coinbase at index 0, this encodes the path through the tree
	// Bit i = 0 means hash i is on the right, bit i = 1 means hash i is on the left
	binary.Write(buf, binary.LittleEndian, uint32(proof.CoinbaseMerkleIndex))

	// 4. Aux merkle branch
	// Number of branch hashes (varint)
	buf.Write(crypto.EncodeVarInt(uint64(len(proof.AuxMerkleBranch))))
	// Branch hashes
	for i, hash := range proof.AuxMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid aux branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	// Branch side mask (encodes path through aux tree)
	binary.Write(buf, binary.LittleEndian, uint32(proof.AuxMerkleIndex))

	// 5. Parent block header (80 bytes)
	if len(proof.ParentHeader) != 80 {
		return nil, fmt.Errorf("invalid parent header length: got %d, expected 80", len(proof.ParentHeader))
	}
	buf.Write(proof.ParentHeader)

	return buf.Bytes(), nil
}

// reverseBytesCopy returns a new slice with bytes in reverse order.
// Does not modify the input slice.
func reverseBytesCopy(b []byte) []byte {
	result := make([]byte, len(b))
	for i, j := 0, len(b)-1; i <= j; i, j = i+1, j-1 {
		result[i], result[j] = b[j], b[i]
	}
	return result
}

// init registers Dogecoin in the coin registry.
func init() {
	Register("DOGE", func() Coin { return NewDogecoinCoin() })
	Register("DOGECOIN", func() Coin { return NewDogecoinCoin() })
}
