// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Bitcoin Cash (BCH) implementation.
//
// Bitcoin Cash uses SHA256d for proof of work, same as Bitcoin and DigiByte.
// BCH forked from Bitcoin and added CashAddr format (bitcoincash:q...).
package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Bitcoin Cash address constants
const (
	BCHP2PKHVersion        byte = 0x00 // Same as Bitcoin legacy
	BCHP2SHVersion         byte = 0x05 // Same as Bitcoin legacy
	BCHRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	BCHRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
	CashAddrPrefix              = "bitcoincash:"
)

// Bitcoin Cash network parameters
// Note: BCH uses different ports than BTC to allow both to run simultaneously
const (
	BCHDefaultP2PPort = 8433 // P2P network port (BTC uses 8333)
	BCHDefaultRPCPort = 8432 // RPC port (BTC uses 8332)
)

// BCHGenesisBlockHash is the genesis block hash for chain verification.
// BCH shares the same genesis block as Bitcoin (forked at block 478558).
// Hash: 000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f
// Date: January 3, 2009 (same as Bitcoin)
const BCHGenesisBlockHash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"

// BitcoinCashCoin implements the Coin interface for Bitcoin Cash.
type BitcoinCashCoin struct{}

// NewBitcoinCashCoin creates a new Bitcoin Cash coin instance.
func NewBitcoinCashCoin() *BitcoinCashCoin {
	return &BitcoinCashCoin{}
}

// Symbol returns the ticker symbol.
func (c *BitcoinCashCoin) Symbol() string {
	return "BCH"
}

// Name returns the full coin name.
func (c *BitcoinCashCoin) Name() string {
	return "Bitcoin Cash"
}

// ValidateAddress validates a Bitcoin Cash address.
func (c *BitcoinCashCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Bitcoin Cash address to its hash and type.
func (c *BitcoinCashCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// CashAddr format (bitcoincash: for mainnet, bchreg: for regtest)
	addrLower := strings.ToLower(address)
	if strings.HasPrefix(addrLower, CashAddrPrefix) ||
		strings.HasPrefix(addrLower, "bchreg:") ||
		strings.HasPrefix(addrLower, "q") ||
		strings.HasPrefix(addrLower, "p") {
		return c.decodeCashAddr(address)
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
	case BCHP2PKHVersion, BCHRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case BCHP2SHVersion, BCHRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unknown address version: 0x%02x", version)
	}
}

// decodeCashAddr decodes a CashAddr format address.
func (c *BitcoinCashCoin) decodeCashAddr(address string) ([]byte, AddressType, error) {
	// Normalize address
	addrLower := strings.ToLower(address)

	// Detect prefix (bitcoincash: for mainnet, bchreg: for regtest)
	prefix := "bitcoincash"
	if strings.HasPrefix(addrLower, "bchreg:") {
		prefix = "bchreg"
	}

	// Add prefix if missing (no colon = mainnet without prefix)
	if !strings.Contains(addrLower, ":") {
		addrLower = prefix + ":" + addrLower
	}

	// Split prefix and data
	parts := strings.Split(addrLower, ":")
	if len(parts) != 2 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid CashAddr format")
	}

	// Use the actual prefix from the address for checksum verification
	data, err := decodeCashAddrDataWithPrefix(parts[0], parts[1])
	if err != nil {
		return nil, AddressTypeUnknown, err
	}

	if len(data) < 21 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid CashAddr data length")
	}

	// First byte is version/type
	versionByte := data[0]
	hash := data[1:21]

	// Type is encoded in version byte
	// 0 = P2PKH, 8 = P2SH
	addrType := versionByte & 0x78
	switch addrType {
	case 0x00: // P2PKH (q...)
		return hash, AddressTypeP2PKH, nil
	case 0x08: // P2SH (p...)
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unknown CashAddr type: 0x%02x", versionByte)
	}
}

// cashAddrPolymod computes the CashAddr BCH polymod checksum.
// This implements the BCH code specified in the CashAddr specification.
func cashAddrPolymod(values []uint64) uint64 {
	// Generator polynomial coefficients for CashAddr
	generator := []uint64{
		0x98f2bc8e61,
		0x79b76d99e2,
		0xf33e5fb3c4,
		0xae2eabe2a8,
		0x1e4f43e470,
	}
	c := uint64(1)
	for _, d := range values {
		c0 := c >> 35
		c = ((c & 0x07ffffffff) << 5) ^ d
		for i := 0; i < 5; i++ {
			if (c0>>uint(i))&1 != 0 {
				c ^= generator[i]
			}
		}
	}
	return c ^ 1
}

// cashAddrPrefixExpand expands the CashAddr prefix for checksum computation.
func cashAddrPrefixExpand(prefix string) []uint64 {
	result := make([]uint64, len(prefix)+1)
	for i, c := range prefix {
		result[i] = uint64(c) & 0x1f
	}
	result[len(prefix)] = 0
	return result
}

// cashAddrVerifyChecksum verifies the CashAddr checksum.
// Returns true if the checksum is valid.
func cashAddrVerifyChecksum(prefix string, payload []byte) bool {
	prefixExpanded := cashAddrPrefixExpand(prefix)
	values := make([]uint64, len(prefixExpanded)+len(payload))
	copy(values, prefixExpanded)
	for i, b := range payload {
		values[len(prefixExpanded)+i] = uint64(b)
	}
	return cashAddrPolymod(values) == 0
}

// decodeCashAddrDataWithPrefix decodes the data part of a CashAddr with full checksum verification.
// Implements the CashAddr specification with BCH polymod checksum.
// The prefix parameter is used for checksum verification (e.g., "bitcoincash" or "bchreg").
func decodeCashAddrDataWithPrefix(prefix, data string) ([]byte, error) {
	// CashAddr uses the same character set as bech32
	charset := "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

	// Convert characters to 5-bit values
	values := make([]byte, len(data))
	for i, char := range data {
		idx := strings.IndexRune(charset, char)
		if idx < 0 {
			return nil, fmt.Errorf("invalid CashAddr character: %c", char)
		}
		values[i] = byte(idx)
	}

	// Must have at least 8 characters for checksum + some data
	if len(values) < 8 {
		return nil, fmt.Errorf("CashAddr too short")
	}

	// SECURITY: Full BCH polymod checksum verification
	// The checksum is verified over the prefix + payload including checksum
	// For verification, we need the full payload (including checksum) to verify polymod == 0
	if !cashAddrVerifyChecksum(prefix, values) {
		return nil, fmt.Errorf("invalid CashAddr checksum")
	}

	// Remove checksum (last 8 characters = 40 bits) after verification
	values = values[:len(values)-8]

	// Convert 5-bit values to 8-bit bytes
	return bchConvertBits(values, 5, 8, false)
}

// bchConvertBits converts a byte slice between bit widths (BCH-specific to avoid collision).
func bchConvertBits(data []byte, fromBits, toBits int, pad bool) ([]byte, error) {
	acc := 0
	bits := 0
	result := make([]byte, 0, len(data)*fromBits/toBits+1)
	maxv := (1 << toBits) - 1

	for _, value := range data {
		acc = (acc << fromBits) | int(value)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			result = append(result, byte((acc>>bits)&maxv))
		}
	}

	if pad {
		if bits > 0 {
			result = append(result, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, fmt.Errorf("invalid bit conversion")
	}

	return result, nil
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *BitcoinCashCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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

	default:
		return nil, fmt.Errorf("unsupported address type: %v", addrType)
	}
}

// SerializeBlockHeader serializes an 80-byte block header.
func (c *BitcoinCashCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
func (c *BitcoinCashCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
//
// The compact format (nBits) is a 32-bit encoding where:
//   - Bits 24-31: Exponent (number of bytes in the full target)
//   - Bits 0-22:  Mantissa (significand)
//   - Bit 23:     Sign bit (negative targets are invalid in BCH)
//
// Per Bitcoin Core behavior: if the sign bit is set, the target is treated as zero
// to prevent invalid (negative) difficulty targets from being used.
func (c *BitcoinCashCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in BCH consensus rules.
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
func (c *BitcoinCashCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

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
func (c *BitcoinCashCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// GBTRules returns the rules for getblocktemplate.
// Bitcoin Cash rejected SegWit and does not require any rules.
func (c *BitcoinCashCoin) GBTRules() []string {
	return []string{}
}

// DefaultRPCPort returns the default RPC port.
// BCH uses 8432 to differentiate from Bitcoin's 8332.
func (c *BitcoinCashCoin) DefaultRPCPort() int {
	return BCHDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
// BCH uses 8433 to differentiate from Bitcoin's 8333.
func (c *BitcoinCashCoin) DefaultP2PPort() int {
	return BCHDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *BitcoinCashCoin) P2PKHVersionByte() byte {
	return BCHP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *BitcoinCashCoin) P2SHVersionByte() byte {
	return BCHP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part (empty for BCH - uses CashAddr).
func (c *BitcoinCashCoin) Bech32HRP() string {
	return "" // BCH uses CashAddr, not bech32
}

// Algorithm returns the mining algorithm.
func (c *BitcoinCashCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
func (c *BitcoinCashCoin) SupportsSegWit() bool {
	return false // BCH does not use SegWit
}

// BlockTime returns the target block time in seconds.
func (c *BitcoinCashCoin) BlockTime() int {
	return 600 // 10 minutes
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *BitcoinCashCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *BitcoinCashCoin) CoinbaseMaturity() int {
	return 100
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *BitcoinCashCoin) GenesisBlockHash() string {
	return BCHGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *BitcoinCashCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(BCHGenesisBlockHash) {
		return fmt.Errorf("BCH genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Bitcoin Cash Node (BCHN)",
			nodeGenesisHash, BCHGenesisBlockHash)
	}
	return nil
}

// init registers Bitcoin Cash in the coin registry.
func init() {
	Register("BCH", func() Coin { return NewBitcoinCashCoin() })
	Register("BITCOINCASH", func() Coin { return NewBitcoinCashCoin() })
}
