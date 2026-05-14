// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package coin - eCash (XEC) implementation.
//
// eCash is a peer-to-peer electronic cash system forked from Bitcoin Cash
// in November 2020. It uses SHA-256d proof of work and CashAddr addressing
// with the "ecash:" prefix. The network introduced Real Time Target (RTT)
// difficulty adjustment in the November 2025 upgrade.
//
// Key characteristics:
//   - SHA-256d algorithm (double SHA-256)
//   - 10-minute block time
//   - CashAddr addressing (ecash:q... prefix) — not bech32
//   - No SegWit
//   - Real Time Target (RTT) difficulty adjustment (post-Nov 2025 upgrade)
//   - MinerFund and StakingRewards mandatory coinbase outputs
//
// References:
//   - https://e.cash
//   - https://github.com/Bitcoin-ABC/bitcoin-abc
package coin

import (
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// eCash mainnet address constants
const (
	XECP2PKHVersion  byte   = 0x00
	XECP2SHVersion   byte   = 0x05
	XECCashAddrPrefix        = "ecash"
)

// eCash network parameters
const (
	XECDefaultP2PPort   = 8343 // Non-default to avoid conflict with BTC (8333)
	XECDefaultRPCPort   = 9004 // ecash-node default RPC port
	XECBlockTime        = 600  // 10-minute blocks
	XECCoinbaseMaturity = 100
	// Genesis block hash — same as Bitcoin (eCash shares Bitcoin's genesis block)
	XECGenesisBlockHash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
)

// ECashCoin implements the Coin interface for eCash (XEC).
type ECashCoin struct{}

// NewECashCoin creates a new eCash coin instance.
func NewECashCoin() *ECashCoin {
	return &ECashCoin{}
}

// Symbol returns the ticker symbol.
func (c *ECashCoin) Symbol() string {
	return "XEC"
}

// Name returns the full coin name.
func (c *ECashCoin) Name() string {
	return "eCash"
}

// ValidateAddress validates an eCash address (CashAddr or legacy Base58).
func (c *ECashCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes an eCash address to its hash and type.
// Accepts both CashAddr format (ecash:q...) and legacy Base58Check.
func (c *ECashCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	addrLower := strings.ToLower(address)

	// CashAddr format: "ecash:q..." or bare "q..."/"p..."
	if strings.HasPrefix(addrLower, XECCashAddrPrefix+":") ||
		strings.HasPrefix(addrLower, "q") ||
		strings.HasPrefix(addrLower, "p") {
		return c.decodeCashAddr(address)
	}

	// Legacy Base58Check (1... for P2PKH, 3... for P2SH)
	return c.decodeLegacyBase58(address)
}

// decodeCashAddr decodes a CashAddr-format eCash address.
// Handles both prefixed ("ecash:q...") and bare ("q..."/"p...") forms.
func (c *ECashCoin) decodeCashAddr(address string) ([]byte, AddressType, error) {
	addrLower := strings.ToLower(address)

	// Strip prefix if present
	bare := addrLower
	if strings.HasPrefix(addrLower, XECCashAddrPrefix+":") {
		bare = addrLower[len(XECCashAddrPrefix)+1:]
	}

	if len(bare) < 34 {
		return nil, AddressTypeUnknown, fmt.Errorf("CashAddr too short")
	}

	// Decode and verify BCH polymod checksum against the "ecash" network prefix.
	// Rejects addresses checksummed for other networks (e.g. BCH "bitcoincash:").
	decoded, err := decodeCashAddrDataWithPrefix(XECCashAddrPrefix, bare)
	if err != nil {
		return nil, AddressTypeUnknown, fmt.Errorf("CashAddr decode failed: %w", err)
	}

	if len(decoded) < 21 {
		return nil, AddressTypeUnknown, fmt.Errorf("decoded CashAddr payload too short: %d bytes", len(decoded))
	}

	versionByte := decoded[0]
	hash := decoded[1:21]

	// Version byte encoding: bits 7-3 = address type, bits 2-0 = hash size
	addrTypeBits := (versionByte >> 3) & 0x1f
	switch addrTypeBits {
	case 0x00: // P2PKH
		if len(hash) != 20 {
			return nil, AddressTypeUnknown, fmt.Errorf("P2PKH hash must be 20 bytes, got %d", len(hash))
		}
		return hash, AddressTypeP2PKH, nil
	case 0x01: // P2SH
		if len(hash) != 20 {
			return nil, AddressTypeUnknown, fmt.Errorf("P2SH hash must be 20 bytes, got %d", len(hash))
		}
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported CashAddr type bits: 0x%02x", addrTypeBits)
	}
}

// decodeLegacyBase58 decodes a legacy Base58Check eCash/Bitcoin address.
func (c *ECashCoin) decodeLegacyBase58(address string) ([]byte, AddressType, error) {
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

	payload := decoded[:21]
	checksum := decoded[21:]
	expectedChecksum := doubleSHA256(payload)[:4]

	// SECURITY: constant-time comparison prevents timing attacks
	if subtle.ConstantTimeCompare(checksum, expectedChecksum) != 1 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid checksum")
	}

	version := decoded[0]
	hash := decoded[1:21]

	switch version {
	case XECP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case XECP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x", version)
	}
}

// BuildCoinbaseScript builds the P2PKH or P2SH output script for a coinbase transaction.
func (c *ECashCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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

// SerializeBlockHeader serializes an 80-byte eCash block header.
// eCash uses the same block header format as Bitcoin.
func (c *ECashCoin) SerializeBlockHeader(header *BlockHeader) []byte {
	buf := make([]byte, 80)
	binary.LittleEndian.PutUint32(buf[0:4], header.Version)
	copy(buf[4:36], header.PreviousBlockHash)
	copy(buf[36:68], header.MerkleRoot)
	binary.LittleEndian.PutUint32(buf[68:72], header.Timestamp)
	binary.LittleEndian.PutUint32(buf[72:76], header.Bits)
	binary.LittleEndian.PutUint32(buf[76:80], header.Nonce)
	return buf
}

// HashBlockHeader hashes a serialized block header using double SHA-256.
func (c *ECashCoin) HashBlockHeader(serialized []byte) []byte {
	return doubleSHA256Header(serialized)
}

// TargetFromBits converts compact bits representation to a target integer.
func (c *ECashCoin) TargetFromBits(bits uint32) *big.Int {
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

// DifficultyFromTarget calculates difficulty from a target.
// Uses Bitcoin-standard diff-1 target (0x00000000ffff0000...).
func (c *ECashCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}
	diff1 := new(big.Int)
	diff1.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)
	diff1Float := new(big.Float).SetInt(diff1)
	targetFloat := new(big.Float).SetInt(target)
	result, _ := new(big.Float).Quo(diff1Float, targetFloat).Float64()
	return result
}

// ShareDifficultyMultiplier returns 1.0 — XEC uses SHA256d like Bitcoin.
func (c *ECashCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// GBTRules returns the rules for getblocktemplate.
// eCash does not support SegWit.
func (c *ECashCoin) GBTRules() []string {
	return []string{}
}

// DefaultRPCPort returns the default RPC port.
func (c *ECashCoin) DefaultRPCPort() int {
	return XECDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *ECashCoin) DefaultP2PPort() int {
	return XECDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *ECashCoin) P2PKHVersionByte() byte {
	return XECP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *ECashCoin) P2SHVersionByte() byte {
	return XECP2SHVersion
}

// Bech32HRP returns empty string — eCash uses CashAddr, not bech32.
func (c *ECashCoin) Bech32HRP() string {
	return ""
}

// Algorithm returns the mining algorithm.
func (c *ECashCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns false — eCash does not support SegWit.
func (c *ECashCoin) SupportsSegWit() bool {
	return false
}

// BlockTime returns the target block time in seconds.
func (c *ECashCoin) BlockTime() int {
	return XECBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *ECashCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *ECashCoin) CoinbaseMaturity() int {
	return XECCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash.
func (c *ECashCoin) GenesisBlockHash() string {
	return XECGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *ECashCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(XECGenesisBlockHash) {
		return fmt.Errorf("XEC genesis block mismatch: got %s, expected %s - "+
			"verify your node is running eCash (ecash-node / Bitcoin ABC)",
			nodeGenesisHash, XECGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// HELPER: double SHA-256 for block header hashing
// ═══════════════════════════════════════════════════════════════════════════════

// doubleSHA256Header performs SHA256(SHA256(data)) for block header hashing.
// Uses the shared doubleSHA256 helper from the coin package.
func doubleSHA256Header(data []byte) []byte {
	return doubleSHA256(data)
}

// DecodeMinerFundScript decodes an eCash MinerFund address string into an output script.
// Used by the job manager to build the MinerFund output when constructing coinbase txs.
func DecodeMinerFundScript(address string) ([]byte, error) {
	if address == "" {
		return nil, fmt.Errorf("empty MinerFund address")
	}
	c := NewECashCoin()
	hash, addrType, err := c.DecodeAddress(address)
	if err != nil {
		return nil, fmt.Errorf("invalid MinerFund address %q: %w", address, err)
	}
	switch addrType {
	case AddressTypeP2PKH:
		script := make([]byte, 25)
		script[0] = 0x76
		script[1] = 0xa9
		script[2] = 0x14
		copy(script[3:23], hash)
		script[23] = 0x88
		script[24] = 0xac
		return script, nil
	case AddressTypeP2SH:
		script := make([]byte, 23)
		script[0] = 0xa9
		script[1] = 0x14
		copy(script[2:22], hash)
		script[22] = 0x87
		return script, nil
	default:
		return nil, fmt.Errorf("unsupported address type for MinerFund: %v", addrType)
	}
}

// DecodeStakingScript decodes a hex-encoded staking rewards script.
// The eCash node provides this script directly in the getblocktemplate response.
func DecodeStakingScript(hexScript string) ([]byte, error) {
	if hexScript == "" {
		return nil, fmt.Errorf("empty staking rewards script")
	}
	script, err := hex.DecodeString(hexScript)
	if err != nil {
		return nil, fmt.Errorf("invalid staking script hex: %w", err)
	}
	return script, nil
}

// init registers eCash in the coin registry under both "XEC" and "ECASH".
func init() {
	Register("XEC", func() Coin { return NewECashCoin() })
	Register("ECASH", func() Coin { return NewECashCoin() })
}
