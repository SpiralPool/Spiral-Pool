// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Bitcoin II (BC2) implementation.
//
// Bitcoin II is a "nearly 1:1 re-launch of the Bitcoin protocol" with a new
// genesis block. It uses SHA256d for proof of work and supports SegWit.
//
// CRITICAL ADDRESS WARNING: Bitcoin II uses identical address formats to Bitcoin:
// - Same P2PKH version byte (0x00) - addresses start with "1"
// - Same P2SH version byte (0x05) - addresses start with "3"
// - Same Bech32 HRP ("bc") - addresses start with "bc1"
//
// This means Bitcoin and Bitcoin II addresses are INDISTINGUISHABLE.
// Users must ensure they configure the correct address for the correct chain.
//
// References:
// - https://github.com/Bitcoin-II/BitcoinII-Core
// - https://bitcoin-ii.org/
package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Bitcoin II address version bytes (identical to Bitcoin)
const (
	BitcoinIIP2PKHVersion        byte = 0x00 // Mainnet P2PKH starts with '1'
	BitcoinIIP2SHVersion         byte = 0x05 // Mainnet P2SH starts with '3'
	BitcoinIIRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	BitcoinIIRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
	BitcoinIIBech32HRP                = "bc" // Mainnet bech32 prefix (same as Bitcoin)
)

// Bitcoin II network parameters
const (
	BitcoinIIDefaultP2PPort   = 8338  // P2P network port
	BitcoinIIDefaultRPCPort   = 8339  // RPC port (standard Bitcoin offset from P2P)
	BitcoinIIBlockTime        = 600   // Target block time: 10 minutes
	BitcoinIICoinbaseMaturity = 100   // Blocks before coinbase is spendable
	// Genesis block hash for chain verification
	// Hash: 0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb
	// Date: December 12, 2024
	BitcoinIIGenesisBlockHash = "0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb"
)

// BitcoinIICoin implements the Coin interface for Bitcoin II.
type BitcoinIICoin struct{}

// NewBitcoinIICoin creates a new Bitcoin II coin instance.
func NewBitcoinIICoin() *BitcoinIICoin {
	return &BitcoinIICoin{}
}

// Symbol returns the ticker symbol.
func (c *BitcoinIICoin) Symbol() string {
	return "BC2"
}

// Name returns the full coin name.
func (c *BitcoinIICoin) Name() string {
	return "Bitcoin II"
}

// ValidateAddress validates a Bitcoin II address.
// WARNING: Bitcoin II addresses are identical to Bitcoin addresses.
// This function cannot distinguish between BTC and BC2 addresses.
func (c *BitcoinIICoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Bitcoin II address to its hash and type.
// Supports P2PKH, P2SH, P2WPKH, P2WSH, and P2TR address formats.
func (c *BitcoinIICoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32/Bech32m native SegWit (bc1q... or bc1p... or bcrt1q... for regtest)
	addrLower := strings.ToLower(address)
	hrp := BitcoinIIBech32HRP
	if strings.HasPrefix(addrLower, "bcrt1") {
		hrp = "bcrt" // Regtest bech32 prefix
	}
	if strings.HasPrefix(addrLower, hrp+"1") {
		decoded, err := decodeBech32Address(address, hrp)
		if err != nil {
			return nil, AddressTypeUnknown, err
		}
		// First byte is witness version, rest is the program
		if len(decoded) < 2 {
			return nil, AddressTypeUnknown, fmt.Errorf("decoded data too short")
		}
		witnessVersion := decoded[0]
		hash := decoded[1:]

		// Determine type based on witness version and program length
		switch witnessVersion {
		case 0: // SegWit v0: P2WPKH (20 bytes) or P2WSH (32 bytes)
			if len(hash) == 20 {
				return hash, AddressTypeP2WPKH, nil
			} else if len(hash) == 32 {
				return hash, AddressTypeP2WSH, nil
			}
			return nil, AddressTypeUnknown, fmt.Errorf("invalid v0 witness program length: %d", len(hash))
		case 1: // SegWit v1 (Taproot): P2TR (32 bytes)
			if len(hash) == 32 {
				return hash, AddressTypeP2TR, nil
			}
			return nil, AddressTypeUnknown, fmt.Errorf("invalid v1 witness program length: %d (expected 32)", len(hash))
		default:
			return nil, AddressTypeUnknown, fmt.Errorf("unsupported witness version: %d", witnessVersion)
		}
	}

	// Legacy Base58Check (1... or 3...)
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
	case BitcoinIIP2PKHVersion, BitcoinIIRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case BitcoinIIP2SHVersion, BitcoinIIRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *BitcoinIICoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
		script[0] = 0x00 // OP_0 (witness version 0)
		script[1] = 0x14 // PUSH 20 bytes
		copy(script[2:22], hash)
		return script, nil

	case AddressTypeP2WSH:
		// OP_0 <32 bytes>
		script := make([]byte, 34)
		script[0] = 0x00 // OP_0 (witness version 0)
		script[1] = 0x20 // PUSH 32 bytes
		copy(script[2:34], hash)
		return script, nil

	case AddressTypeP2TR:
		// OP_1 <32 bytes> (Taproot/SegWit v1)
		// P2TR uses witness version 1 (OP_1 = 0x51) and 32-byte x-only pubkey
		script := make([]byte, 34)
		script[0] = 0x51 // OP_1 (witness version 1)
		script[1] = 0x20 // PUSH 32 bytes
		copy(script[2:34], hash)
		return script, nil

	default:
		return nil, fmt.Errorf("unsupported address type: %v", addrType)
	}
}

// SerializeBlockHeader serializes an 80-byte block header.
// Bitcoin II uses the standard Bitcoin block header format.
func (c *BitcoinIICoin) SerializeBlockHeader(header *BlockHeader) []byte {
	buf := make([]byte, 80)

	binary.LittleEndian.PutUint32(buf[0:4], header.Version)
	copy(buf[4:36], header.PreviousBlockHash)
	copy(buf[36:68], header.MerkleRoot)
	binary.LittleEndian.PutUint32(buf[68:72], header.Timestamp)
	binary.LittleEndian.PutUint32(buf[72:76], header.Bits)
	binary.LittleEndian.PutUint32(buf[76:80], header.Nonce)

	return buf
}

// HashBlockHeader hashes a serialized block header using SHA256d.
// This is the standard Bitcoin double-SHA256 hash.
func (c *BitcoinIICoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
// Uses the standard Bitcoin compact target encoding.
//
// The compact format (nBits) is a 32-bit encoding where:
//   - Bits 24-31: Exponent (number of bytes in the full target)
//   - Bits 0-22:  Mantissa (significand)
//   - Bit 23:     Sign bit (negative targets are invalid in Bitcoin/BC2)
//
// Per Bitcoin Core behavior: if the sign bit is set, the target is treated as zero
// to prevent invalid (negative) difficulty targets from being used.
func (c *BitcoinIICoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in Bitcoin/BC2 consensus rules.
	// If bit 23 (sign bit) is set, treat the target as zero per Bitcoin Core.
	// This is defensive coding - a properly functioning node will never send
	// a negative compact target, but we handle it safely if it occurs.
	if bits&0x00800000 != 0 {
		// Return zero target - any hash will be >= 0, so no blocks can be mined
		// This is the safest behavior for an invalid target encoding
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
// Uses the standard Bitcoin difficulty formula.
// Note: This is for display purposes only; share validation uses exact big.Int comparison.
func (c *BitcoinIICoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Bitcoin difficulty 1 target (0x1d00ffff)
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	// difficulty = diff1_target / current_target
	diff1Float := new(big.Float).SetInt(diff1Target)
	targetFloat := new(big.Float).SetInt(target)

	result := new(big.Float).Quo(diff1Float, targetFloat)
	difficulty, accuracy := result.Float64()

	// For extremely high difficulties that exceed float64 range, return max float64
	// This only affects display; actual validation uses big.Int target comparison
	if accuracy == big.Below {
		return difficulty // Best approximation available
	}

	return difficulty
}

// ShareDifficultyMultiplier returns the multiplier for share difficulty.
// Bitcoin II uses the same difficulty calculation as Bitcoin.
func (c *BitcoinIICoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// DefaultRPCPort returns the default RPC port.
func (c *BitcoinIICoin) DefaultRPCPort() int {
	return BitcoinIIDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *BitcoinIICoin) DefaultP2PPort() int {
	return BitcoinIIDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *BitcoinIICoin) P2PKHVersionByte() byte {
	return BitcoinIIP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *BitcoinIICoin) P2SHVersionByte() byte {
	return BitcoinIIP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *BitcoinIICoin) Bech32HRP() string {
	return BitcoinIIBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *BitcoinIICoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
// Bitcoin II has SegWit activated from block 290.
func (c *BitcoinIICoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *BitcoinIICoin) BlockTime() int {
	return BitcoinIIBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
// BIP34 requires at least 2 bytes for height encoding.
func (c *BitcoinIICoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *BitcoinIICoin) CoinbaseMaturity() int {
	return BitcoinIICoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
// This should be compared against the node's getblockhash(0) result to ensure
// the pool is connected to the correct BC2 network and not Bitcoin mainnet.
func (c *BitcoinIICoin) GenesisBlockHash() string {
	return BitcoinIIGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
// Returns an error if the hash doesn't match, indicating possible misconfiguration
// where the pool may be connected to Bitcoin instead of Bitcoin II.
func (c *BitcoinIICoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(BitcoinIIGenesisBlockHash) {
		return fmt.Errorf("BC2 genesis block mismatch: got %s, expected %s - "+
			"CRITICAL: You may be connected to Bitcoin mainnet instead of Bitcoin II. "+
			"Verify your node is running Bitcoin II Core and RPC port is 8339 (not 8332)",
			nodeGenesisHash, BitcoinIIGenesisBlockHash)
	}
	return nil
}

// init registers Bitcoin II in the coin registry.
func init() {
	Register("BC2", func() Coin { return NewBitcoinIICoin() })
	Register("BITCOINII", func() Coin { return NewBitcoinIICoin() })
	Register("BITCOIN2", func() Coin { return NewBitcoinIICoin() })
}
