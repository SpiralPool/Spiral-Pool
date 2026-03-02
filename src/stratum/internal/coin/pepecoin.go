// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - PepeCoin/Memetic (MEME) implementation.
//
// PepeCoin (also known as Memetic) is a meme cryptocurrency launched in 2016.
// It was designed for decentralized image storage and the Kekdaq meme exchange.
// Originally PoW with X11, it transitioned to full PoS at block 1,700,000.
//
// NOTE: This implementation is for the pepecoinppc fork which uses Scrypt,
// enabling merged mining with Litecoin. This is distinct from the original
// X11-based Memetic/PepeCoin which is now PoS-only.
//
// Key characteristics:
//   - Scrypt algorithm (N=1024, r=1, p=1) - same as Litecoin
//   - 1 minute block time
//   - Merge-mined with Litecoin via AuxPoW
//   - Address prefix 'P' for P2PKH (version byte 55)
//
// References:
//   - https://github.com/pepecoinppc/pepecoin
//   - https://pepe.cx/
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

// PepeCoin mainnet address version bytes
const (
	PepeCoinP2PKHVersion byte = 0x37 // 55 decimal - addresses start with 'P'
	PepeCoinP2SHVersion  byte = 0x55 // 85 decimal - P2SH addresses
	PepeCoinBech32HRP         = "pep" // Bech32 prefix (if supported)

	// Regtest address version bytes (Bitcoin-compatible for regtest mining)
	PepeCoinRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	PepeCoinRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
)

// PepeCoin network parameters
const (
	PepeCoinDefaultP2PPort   = 33874 // P2P network port
	PepeCoinDefaultRPCPort   = 33873 // RPC port
	PepeCoinBlockTime        = 60    // Target block time: 1 minute
	PepeCoinCoinbaseMaturity = 30    // Blocks before coinbase is spendable (Dogecoin fork, inherits nCoinbaseMaturity=30)
	// Genesis block hash for chain verification
	// Hash from pepecoinppc Scrypt fork
	PepeCoinGenesisBlockHash = "00008cae6a01358d774087e2daf3b2108252b0b5a440195ffec4fd38f9892272"
	// Block height at which AuxPoW (merged mining) was enabled
	PepeCoinAuxPowStartHeight uint64 = 0 // AuxPoW enabled from genesis in Scrypt fork
)

// PepeCoin AuxPoW constants
// CONSENSUS-CRITICAL: These values must exactly match PepeCoin Core
const (
	// PepeCoinChainID is the unique chain ID for PepeCoin in the aux merkle tree.
	// CONSENSUS-CRITICAL: Using wrong chain ID will cause all aux blocks to be rejected.
	// Source: pepecoinppc/pepecoin/src/chainparams.cpp — nAuxpowChainId = 0x003f (all networks)
	PepeCoinChainID int32 = 0x003F // 63 decimal - PepeCoin's chain ID

	// PepeCoinAuxPowVersionBit is the version bit indicating AuxPoW block.
	// When this bit is set in the block version, the block uses auxiliary proof of work.
	PepeCoinAuxPowVersionBit uint32 = 0x00000100 // Bit 8 = 256

	// PepeCoinAuxPowVersionChainID is the chain ID embedded in version.
	PepeCoinAuxPowVersionChainID uint32 = 0x003F0000
)

// PepeCoinCoin implements the Coin interface for PepeCoin (Scrypt fork).
type PepeCoinCoin struct{}

// NewPepeCoinCoin creates a new PepeCoin coin instance.
func NewPepeCoinCoin() *PepeCoinCoin {
	return &PepeCoinCoin{}
}

// Symbol returns the ticker symbol.
func (c *PepeCoinCoin) Symbol() string {
	return "PEP"
}

// Name returns the full coin name.
func (c *PepeCoinCoin) Name() string {
	return "PepeCoin"
}

// ValidateAddress validates a PepeCoin address.
func (c *PepeCoinCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a PepeCoin address to its hash and type.
// Supports P2PKH and P2SH address formats.
func (c *PepeCoinCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32 native SegWit (pep1... or peprt1... for regtest)
	addrLower := strings.ToLower(address)
	hrp := PepeCoinBech32HRP
	if strings.HasPrefix(addrLower, "peprt1") {
		hrp = "peprt" // Regtest bech32 prefix
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

	// Legacy Base58Check (P... for P2PKH)
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
	case PepeCoinP2PKHVersion, PepeCoinRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case PepeCoinP2SHVersion, PepeCoinRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x (expected 0x37/0x6f for P2PKH or 0x55/0xc4 for P2SH)", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *PepeCoinCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
// PepeCoin uses the same block header format as Bitcoin/Litecoin.
func (c *PepeCoinCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
// PepeCoin uses the same Scrypt parameters as Litecoin.
func (c *PepeCoinCoin) HashBlockHeader(serialized []byte) []byte {
	return crypto.ScryptHash(serialized)
}

// TargetFromBits converts compact bits representation to target.
func (c *PepeCoinCoin) TargetFromBits(bits uint32) *big.Int {
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
func (c *PepeCoinCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// PepeCoin uses the same difficulty 1 target as Bitcoin/Litecoin
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
func (c *PepeCoinCoin) ShareDifficultyMultiplier() float64 {
	return 65536.0
}

// GBTRules returns the rules for getblocktemplate.
// PepeCoin does not have SegWit activated, no rules required.
func (c *PepeCoinCoin) GBTRules() []string {
	return []string{}
}

// DefaultRPCPort returns the default RPC port.
func (c *PepeCoinCoin) DefaultRPCPort() int {
	return PepeCoinDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *PepeCoinCoin) DefaultP2PPort() int {
	return PepeCoinDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *PepeCoinCoin) P2PKHVersionByte() byte {
	return PepeCoinP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *PepeCoinCoin) P2SHVersionByte() byte {
	return PepeCoinP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *PepeCoinCoin) Bech32HRP() string {
	return PepeCoinBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *PepeCoinCoin) Algorithm() string {
	return "scrypt"
}

// SupportsSegWit returns whether the coin supports SegWit.
// PepeCoin (Scrypt fork) does not have SegWit activated.
func (c *PepeCoinCoin) SupportsSegWit() bool {
	return false
}

// BlockTime returns the target block time in seconds.
func (c *PepeCoinCoin) BlockTime() int {
	return PepeCoinBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *PepeCoinCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *PepeCoinCoin) CoinbaseMaturity() int {
	return PepeCoinCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *PepeCoinCoin) GenesisBlockHash() string {
	return PepeCoinGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *PepeCoinCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(PepeCoinGenesisBlockHash) {
		return fmt.Errorf("PEP genesis block mismatch: got %s, expected %s - "+
			"verify your node is running PepeCoin Core (Scrypt fork)",
			nodeGenesisHash, PepeCoinGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXPOW (MERGE MINING) IMPLEMENTATION
// ═══════════════════════════════════════════════════════════════════════════════
//
// PepeCoin (Scrypt fork) supports AuxPoW for merged mining with Litecoin.
// This allows Litecoin miners to simultaneously mine PepeCoin blocks.
// ═══════════════════════════════════════════════════════════════════════════════

// SupportsAuxPow returns whether the coin supports auxiliary proof of work (merged mining).
func (c *PepeCoinCoin) SupportsAuxPow() bool {
	return true
}

// AuxPowStartHeight returns the block height at which AuxPoW was enabled.
func (c *PepeCoinCoin) AuxPowStartHeight() uint64 {
	return PepeCoinAuxPowStartHeight
}

// ChainID returns PepeCoin's unique chain identifier for the aux merkle tree.
func (c *PepeCoinCoin) ChainID() int32 {
	return PepeCoinChainID
}

// AuxPowVersionBit returns the version bit that indicates an AuxPoW block.
func (c *PepeCoinCoin) AuxPowVersionBit() uint32 {
	return PepeCoinAuxPowVersionBit
}

// ParseAuxBlockResponse parses the getauxblock RPC response from PepeCoin Core.
func (c *PepeCoinCoin) ParseAuxBlockResponse(response map[string]interface{}) (*AuxBlock, error) {
	auxBlock := &AuxBlock{
		ChainID: PepeCoinChainID,
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
		if int32(chainID) != PepeCoinChainID {
			return nil, fmt.Errorf("chain ID mismatch: got %d, expected %d", int32(chainID), PepeCoinChainID)
		}
	}

	// Parse target from bits field
	// CRITICAL: Always use bits-derived target. The daemon validates against nBits
	// (compact target), and the explicit "target" field may be unreliable in some
	// network modes (observed in Dogecoin regtest - applying same fix for safety).
	if bitsHex, ok := response["bits"].(string); ok && len(bitsHex) == 8 {
		bitsBytes, err := hex.DecodeString(bitsHex)
		if err == nil && len(bitsBytes) == 4 {
			bits := binary.BigEndian.Uint32(bitsBytes)
			auxBlock.Bits = bits
			auxBlock.Target = c.TargetFromBits(bits)
		}
	}
	// NOTE: We intentionally ignore the explicit "target" field from getauxblock.
	// The daemon validates blocks against nBits, not the explicit target.
	if auxBlock.Target == nil {
		return nil, fmt.Errorf("missing bits field in aux block response")
	}

	// Parse height
	if height, ok := response["height"].(float64); ok {
		auxBlock.Height = uint64(height)
	}

	// Parse coinbase value
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

	auxBlock.ChainIndex = 0
	return auxBlock, nil
}

// SerializeAuxPowProof serializes the complete AuxPoW proof for block submission.
func (c *PepeCoinCoin) SerializeAuxPowProof(proof *AuxPowProof) ([]byte, error) {
	if proof == nil {
		return nil, fmt.Errorf("nil proof")
	}

	buf := new(bytes.Buffer)

	// 1. Parent coinbase transaction
	if len(proof.ParentCoinbase) == 0 {
		return nil, fmt.Errorf("empty parent coinbase")
	}
	buf.Write(proof.ParentCoinbase)

	// 2. Parent block hash (32 bytes, little-endian)
	if len(proof.ParentHash) != 32 {
		return nil, fmt.Errorf("invalid parent hash length: got %d, expected 32", len(proof.ParentHash))
	}
	buf.Write(proof.ParentHash)

	// 3. Coinbase merkle branch
	buf.Write(crypto.EncodeVarInt(uint64(len(proof.CoinbaseMerkleBranch))))
	for i, hash := range proof.CoinbaseMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid coinbase branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	binary.Write(buf, binary.LittleEndian, uint32(proof.CoinbaseMerkleIndex))

	// 3. Aux merkle branch
	buf.Write(crypto.EncodeVarInt(uint64(len(proof.AuxMerkleBranch))))
	for i, hash := range proof.AuxMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid aux branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	binary.Write(buf, binary.LittleEndian, uint32(proof.AuxMerkleIndex))

	// 4. Parent block header (80 bytes)
	if len(proof.ParentHeader) != 80 {
		return nil, fmt.Errorf("invalid parent header length: got %d, expected 80", len(proof.ParentHeader))
	}
	buf.Write(proof.ParentHeader)

	return buf.Bytes(), nil
}

// init registers PepeCoin in the coin registry.
func init() {
	Register("PEP", func() Coin { return NewPepeCoinCoin() })
	Register("PEPECOIN", func() Coin { return NewPepeCoinCoin() })
	Register("MEME", func() Coin { return NewPepeCoinCoin() }) // Alternative name
}
