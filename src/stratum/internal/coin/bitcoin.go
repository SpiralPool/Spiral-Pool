// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package coin - Bitcoin (BTC) implementation.
//
// Bitcoin uses SHA256d for proof of work.
// This implementation supports mainnet addresses and SegWit.
package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Bitcoin mainnet address version bytes
const (
	BitcoinP2PKHVersion byte = 0x00 // Mainnet P2PKH starts with '1'
	BitcoinP2SHVersion  byte = 0x05 // Mainnet P2SH starts with '3'
	BitcoinBech32HRP         = "bc" // Mainnet bech32 prefix
)

// BitcoinGenesisBlockHash is the genesis block hash for chain verification.
// Hash: 000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f
// Date: January 3, 2009
const BitcoinGenesisBlockHash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"

// BitcoinCoin implements the Coin interface for Bitcoin.
type BitcoinCoin struct{}

// NewBitcoinCoin creates a new Bitcoin coin instance.
func NewBitcoinCoin() *BitcoinCoin {
	return &BitcoinCoin{}
}

// Symbol returns the ticker symbol.
func (c *BitcoinCoin) Symbol() string {
	return "BTC"
}

// Name returns the full coin name.
func (c *BitcoinCoin) Name() string {
	return "Bitcoin"
}

// ValidateAddress validates a Bitcoin address.
func (c *BitcoinCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Bitcoin address to its hash and type.
func (c *BitcoinCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32/Bech32m native SegWit (bc1q... or bc1p... or bcrt1q... for regtest)
	addrLower := strings.ToLower(address)
	hrp := BitcoinBech32HRP
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
	case BitcoinP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case BitcoinP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *BitcoinCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
func (c *BitcoinCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
func (c *BitcoinCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
//
// The compact format (nBits) is a 32-bit encoding where:
//   - Bits 24-31: Exponent (number of bytes in the full target)
//   - Bits 0-22:  Mantissa (significand)
//   - Bit 23:     Sign bit (negative targets are invalid in Bitcoin)
//
// Per Bitcoin Core behavior: if the sign bit is set, the target is treated as zero
// to prevent invalid (negative) difficulty targets from being used.
func (c *BitcoinCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in Bitcoin consensus rules.
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
// Note: This is for display purposes only; share validation uses exact big.Int comparison.
func (c *BitcoinCoin) DifficultyFromTarget(target *big.Int) float64 {
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

	// For extremely high difficulties that exceed float64 range, return best approximation
	// This only affects display; actual validation uses big.Int target comparison
	if accuracy == big.Below {
		return difficulty
	}

	return difficulty
}

// ShareDifficultyMultiplier returns the multiplier for share difficulty.
func (c *BitcoinCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// DefaultRPCPort returns the default RPC port.
func (c *BitcoinCoin) DefaultRPCPort() int {
	return 8332
}

// DefaultP2PPort returns the default P2P port.
func (c *BitcoinCoin) DefaultP2PPort() int {
	return 8333
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *BitcoinCoin) P2PKHVersionByte() byte {
	return BitcoinP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *BitcoinCoin) P2SHVersionByte() byte {
	return BitcoinP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *BitcoinCoin) Bech32HRP() string {
	return BitcoinBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *BitcoinCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
func (c *BitcoinCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *BitcoinCoin) BlockTime() int {
	return 600 // 10 minutes
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *BitcoinCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *BitcoinCoin) CoinbaseMaturity() int {
	return 100
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *BitcoinCoin) GenesisBlockHash() string {
	return BitcoinGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *BitcoinCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(BitcoinGenesisBlockHash) {
		return fmt.Errorf("BTC genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Bitcoin Core",
			nodeGenesisHash, BitcoinGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// MERGE MINING PARENT CHAIN SUPPORT
// ═══════════════════════════════════════════════════════════════════════════════
//
// Bitcoin serves as the primary merge mining parent for SHA-256d-based coins.
// The most notable auxiliary chain is Namecoin, which was the first coin to
// implement AuxPoW in 2011.
//
// As a parent chain, Bitcoin:
// - Provides proof-of-work for auxiliary chains
// - Embeds aux merkle root commitment in coinbase
// - Uses magic marker 0xfabe6d6d to identify aux data
//
// Bitcoin itself does NOT use AuxPoW - it provides PoW to aux chains.
// ═══════════════════════════════════════════════════════════════════════════════

// Bitcoin merge mining constants
const (
	// BitcoinAuxMarker is the magic bytes marking aux data in coinbase.
	// "fabe6d6d" = "fabe mm" (fake block merge mining) - standard across all AuxPoW implementations.
	// This marker is searched for by aux chains to locate the aux merkle root in the coinbase.
	BitcoinAuxMarker = "\xfa\xbe\x6d\x6d"

	// BitcoinMaxAuxDataSize is the maximum size for aux data in coinbase scriptSig.
	// Coinbase scriptSig max is 100 bytes. After height (1-9 bytes), pool tag, and extranonces,
	// we need to fit: marker (4) + aux_root (32) + tree_size (4) + merkle_nonce (4) = 44 bytes
	BitcoinMaxAuxDataSize = 44
)

// CanBeParentFor returns true if Bitcoin can merge mine the given algorithm.
// Bitcoin uses SHA-256d, so it can only merge mine other SHA-256d-based coins.
// This ensures the PoW is valid for both parent and aux chains.
func (c *BitcoinCoin) CanBeParentFor(auxAlgorithm string) bool {
	return auxAlgorithm == "sha256d"
}

// CoinbaseAuxMarker returns the magic bytes for aux data in coinbase.
// This is the standard merge mining marker: 0xfabe6d6d
// Aux chains search for this marker in the coinbase to locate the aux merkle root.
func (c *BitcoinCoin) CoinbaseAuxMarker() []byte {
	return []byte(BitcoinAuxMarker)
}

// MaxCoinbaseAuxSize returns the maximum size for aux data in coinbase.
// This ensures aux data fits within the 100-byte coinbase scriptSig limit.
func (c *BitcoinCoin) MaxCoinbaseAuxSize() int {
	return BitcoinMaxAuxDataSize
}

// SupportsAuxPow returns false - Bitcoin is a parent chain, not an aux chain.
// Bitcoin provides proof-of-work to auxiliary chains but does not use AuxPoW itself.
func (c *BitcoinCoin) SupportsAuxPow() bool {
	return false
}

// AuxPowStartHeight returns 0 - not applicable for parent chains.
// This method exists to satisfy interface checks but is not meaningful for Bitcoin.
func (c *BitcoinCoin) AuxPowStartHeight() uint64 {
	return 0
}

// init registers Bitcoin in the coin registry.
func init() {
	Register("BTC", func() Coin { return NewBitcoinCoin() })
	Register("BITCOIN", func() Coin { return NewBitcoinCoin() })
}
