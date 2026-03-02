// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package coin - Litecoin (LTC) implementation.
//
// Litecoin is a peer-to-peer cryptocurrency created by Charlie Lee in 2011.
// It uses the Scrypt algorithm for proof of work, with 2.5 minute block times
// (4x faster than Bitcoin).
//
// Key differences from Bitcoin:
//   - Scrypt algorithm (memory-hard, N=1024, r=1, p=1)
//   - 2.5 minute block time (vs 10 minutes)
//   - 84 million total supply (4x Bitcoin)
//   - Different address prefixes (L for P2PKH, M for P2SH, ltc1 for bech32)
//
// References:
//   - https://github.com/litecoin-project/litecoin
//   - https://litecoin.org/
package coin

import (
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"

	"github.com/spiralpool/stratum/internal/crypto"
)

// Litecoin mainnet address version bytes
const (
	LitecoinP2PKHVersion byte = 0x30 // Mainnet P2PKH starts with 'L'
	LitecoinP2SHVersion  byte = 0x32 // Mainnet P2SH starts with 'M' (was 0x05/'3', updated)
	LitecoinBech32HRP         = "ltc" // Mainnet bech32 prefix
)

// Litecoin network parameters
const (
	LitecoinDefaultP2PPort   = 9333  // P2P network port
	LitecoinDefaultRPCPort   = 9332  // RPC port
	LitecoinBlockTime        = 150   // Target block time: 2.5 minutes
	LitecoinCoinbaseMaturity = 100   // Blocks before coinbase is spendable
	// Genesis block hash for chain verification
	// Hash: 12a765e31ffd4059bada1e25190f6e98c99d9714d334efa41a195a7e7e04bfe2
	// Date: October 7, 2011
	LitecoinGenesisBlockHash = "12a765e31ffd4059bada1e25190f6e98c99d9714d334efa41a195a7e7e04bfe2"
)

// LitecoinCoin implements the Coin interface for Litecoin.
type LitecoinCoin struct{}

// NewLitecoinCoin creates a new Litecoin coin instance.
func NewLitecoinCoin() *LitecoinCoin {
	return &LitecoinCoin{}
}

// Symbol returns the ticker symbol.
func (c *LitecoinCoin) Symbol() string {
	return "LTC"
}

// Name returns the full coin name.
func (c *LitecoinCoin) Name() string {
	return "Litecoin"
}

// ValidateAddress validates a Litecoin address.
func (c *LitecoinCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Litecoin address to its hash and type.
// Supports P2PKH, P2SH, P2WPKH, P2WSH, and P2TR address formats.
func (c *LitecoinCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32/Bech32m native SegWit (ltc1q... or ltc1p... or rltc1q... for regtest)
	addrLower := strings.ToLower(address)
	hrp := LitecoinBech32HRP
	if strings.HasPrefix(addrLower, "rltc1") {
		hrp = "rltc" // Regtest bech32 prefix
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

	// Legacy Base58Check (L... or M...)
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
	case LitecoinP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case LitecoinP2SHVersion:
		return hash, AddressTypeP2SH, nil
	case 0x05: // Legacy P2SH (old '3' prefix, still valid)
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *LitecoinCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
// Litecoin uses the same block header format as Bitcoin.
func (c *LitecoinCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
// This is the key difference from Bitcoin - Litecoin uses Scrypt for PoW.
func (c *LitecoinCoin) HashBlockHeader(serialized []byte) []byte {
	return crypto.ScryptHash(serialized)
}

// TargetFromBits converts compact bits representation to target.
// Uses the same compact target encoding as Bitcoin.
func (c *LitecoinCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in Litecoin consensus rules.
	if bits&0x00800000 != 0 {
		return new(big.Int) // Return zero target
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
// Note: Litecoin uses the same difficulty formula as Bitcoin.
func (c *LitecoinCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Litecoin difficulty 1 target (same as Bitcoin: 0x1d00ffff)
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	// difficulty = diff1_target / current_target
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
func (c *LitecoinCoin) ShareDifficultyMultiplier() float64 {
	return 65536.0
}

// GBTRules returns the rules for getblocktemplate.
// Litecoin requires MWEB (MimbleWimble Extension Blocks) support since v0.21.
func (c *LitecoinCoin) GBTRules() []string {
	return []string{"mweb", "segwit"}
}

// DefaultRPCPort returns the default RPC port.
func (c *LitecoinCoin) DefaultRPCPort() int {
	return LitecoinDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *LitecoinCoin) DefaultP2PPort() int {
	return LitecoinDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *LitecoinCoin) P2PKHVersionByte() byte {
	return LitecoinP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *LitecoinCoin) P2SHVersionByte() byte {
	return LitecoinP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *LitecoinCoin) Bech32HRP() string {
	return LitecoinBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *LitecoinCoin) Algorithm() string {
	return "scrypt"
}

// SupportsSegWit returns whether the coin supports SegWit.
// Litecoin activated SegWit in May 2017, before Bitcoin.
func (c *LitecoinCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *LitecoinCoin) BlockTime() int {
	return LitecoinBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *LitecoinCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *LitecoinCoin) CoinbaseMaturity() int {
	return LitecoinCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *LitecoinCoin) GenesisBlockHash() string {
	return LitecoinGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *LitecoinCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(LitecoinGenesisBlockHash) {
		return fmt.Errorf("LTC genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Litecoin Core",
			nodeGenesisHash, LitecoinGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// MERGE MINING PARENT CHAIN SUPPORT
// ═══════════════════════════════════════════════════════════════════════════════
//
// Litecoin serves as the primary merge mining parent for Scrypt-based coins.
// The most notable auxiliary chain is Dogecoin, which enabled AuxPoW in 2014.
//
// As a parent chain, Litecoin:
// - Provides proof-of-work for auxiliary chains
// - Embeds aux merkle root commitment in coinbase
// - Uses magic marker 0xfabe6d6d to identify aux data
//
// Litecoin itself does NOT use AuxPoW - it provides PoW to aux chains.
// ═══════════════════════════════════════════════════════════════════════════════

// Litecoin merge mining constants
const (
	// LitecoinAuxMarker is the magic bytes marking aux data in coinbase.
	// "fabe6d6d" = "fabe mm" (fake block merge mining) - standard across all AuxPoW implementations.
	// This marker is searched for by aux chains to locate the aux merkle root in the coinbase.
	LitecoinAuxMarker = "\xfa\xbe\x6d\x6d"

	// LitecoinMaxAuxDataSize is the maximum size for aux data in coinbase scriptSig.
	// Coinbase scriptSig max is 100 bytes. After height (1-9 bytes), pool tag, and extranonces,
	// we need to fit: marker (4) + aux_root (32) + tree_size (4) + merkle_nonce (4) = 44 bytes
	LitecoinMaxAuxDataSize = 44
)

// CanBeParentFor returns true if Litecoin can merge mine the given algorithm.
// Litecoin uses Scrypt, so it can only merge mine other Scrypt-based coins.
// This ensures the PoW is valid for both parent and aux chains.
func (c *LitecoinCoin) CanBeParentFor(auxAlgorithm string) bool {
	return auxAlgorithm == "scrypt"
}

// CoinbaseAuxMarker returns the magic bytes for aux data in coinbase.
// This is the standard merge mining marker: 0xfabe6d6d
// Aux chains search for this marker in the coinbase to locate the aux merkle root.
func (c *LitecoinCoin) CoinbaseAuxMarker() []byte {
	return []byte(LitecoinAuxMarker)
}

// MaxCoinbaseAuxSize returns the maximum size for aux data in coinbase.
// This ensures aux data fits within the 100-byte coinbase scriptSig limit.
func (c *LitecoinCoin) MaxCoinbaseAuxSize() int {
	return LitecoinMaxAuxDataSize
}

// SupportsAuxPow returns false - Litecoin is a parent chain, not an aux chain.
// Litecoin provides proof-of-work to auxiliary chains but does not use AuxPoW itself.
func (c *LitecoinCoin) SupportsAuxPow() bool {
	return false
}

// AuxPowStartHeight returns 0 - not applicable for parent chains.
// This method exists to satisfy interface checks but is not meaningful for Litecoin.
func (c *LitecoinCoin) AuxPowStartHeight() uint64 {
	return 0
}

// init registers Litecoin in the coin registry.
func init() {
	Register("LTC", func() Coin { return NewLitecoinCoin() })
	Register("LITECOIN", func() Coin { return NewLitecoinCoin() })
}
