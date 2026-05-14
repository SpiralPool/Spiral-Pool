// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Bitcoin Cash II (BCH2) implementation.
//
// Bitcoin Cash II forked from Bitcoin II (BC2) at block 53,200 and adopted
// Bitcoin Cash consensus rules: CashAddr addressing (prefix 'bitcoincashii:'),
// SIGHASH_FORKID (0x40) for transaction signing, and ASERT DAA difficulty
// adjustment. BCH2 does NOT support SegWit.
//
// References:
// - https://bch2.org
// - https://github.com/BitcoincashII/bitcoincashII-core
package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Bitcoin Cash II address constants
const (
	BCH2P2PKHVersion        byte = 0x00 // Same as BCH/BTC legacy
	BCH2P2SHVersion         byte = 0x05 // Same as BCH/BTC legacy
	BCH2RegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	BCH2RegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
	BCH2CashAddrPrefix           = "bitcoincashii:"
)

// Bitcoin Cash II network parameters
const (
	BCH2DefaultP2PPort = 8534 // P2P network port
	BCH2DefaultRPCPort = 8533 // RPC port
)

// BCH2GenesisBlockHash is the genesis block hash for chain verification.
// BCH2 forked from BC2 at block 53,200 and shares BC2's genesis block.
// Hash: 0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb
// Date: December 12, 2024
const BCH2GenesisBlockHash = "0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb"

// BitcoinCashIICoin implements the Coin interface for Bitcoin Cash II.
type BitcoinCashIICoin struct{}

// NewBitcoinCashIICoin creates a new Bitcoin Cash II coin instance.
func NewBitcoinCashIICoin() *BitcoinCashIICoin {
	return &BitcoinCashIICoin{}
}

// Symbol returns the ticker symbol.
func (c *BitcoinCashIICoin) Symbol() string {
	return "BCH2"
}

// Name returns the full coin name.
func (c *BitcoinCashIICoin) Name() string {
	return "Bitcoin Cash II"
}

// ValidateAddress validates a Bitcoin Cash II address.
func (c *BitcoinCashIICoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Bitcoin Cash II address to its hash and type.
// Supports CashAddr (bitcoincashii:q...) and legacy Base58Check formats.
func (c *BitcoinCashIICoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// CashAddr format (bitcoincashii: for mainnet)
	addrLower := strings.ToLower(address)
	if strings.HasPrefix(addrLower, BCH2CashAddrPrefix) ||
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
	case BCH2P2PKHVersion, BCH2RegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case BCH2P2SHVersion, BCH2RegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unknown address version: 0x%02x", version)
	}
}

// decodeCashAddr decodes a CashAddr format address using the bitcoincashii: prefix.
// Full-form addresses (bitcoincashii:q...) get full checksum verification.
// Short-form addresses (q... or p... without prefix) are decoded without
// prefix-specific checksum verification, since the checksum cannot be verified
// without knowing which prefix was used to generate the address.
func (c *BitcoinCashIICoin) decodeCashAddr(address string) ([]byte, AddressType, error) {
	addrLower := strings.ToLower(address)
	prefix := "bitcoincashii"
	hasPrefix := strings.Contains(addrLower, ":")

	if !hasPrefix {
		addrLower = prefix + ":" + addrLower
	}

	parts := strings.Split(addrLower, ":")
	if len(parts) != 2 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid CashAddr format")
	}

	var data []byte
	var err error
	if hasPrefix {
		// Full form: verify checksum against the declared prefix
		data, err = decodeCashAddrDataWithPrefix(parts[0], parts[1])
	} else {
		// Short form: prefix is unknown, skip prefix-specific checksum verification
		data, err = bch2DecodeCashAddrShortForm(parts[1])
	}
	if err != nil {
		return nil, AddressTypeUnknown, err
	}

	if len(data) < 21 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid CashAddr data length")
	}

	versionByte := data[0]
	hash := data[1:21]

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

// bch2DecodeCashAddrShortForm decodes a CashAddr payload without checksum verification.
// Used when the address has no prefix (e.g. "qpm2qsznhks23z...") and the prefix
// used to compute the checksum is therefore unknown.
func bch2DecodeCashAddrShortForm(data string) ([]byte, error) {
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

	values := make([]byte, len(data))
	for i, char := range data {
		idx := strings.IndexRune(charset, char)
		if idx < 0 {
			return nil, fmt.Errorf("invalid CashAddr character: %c", char)
		}
		values[i] = byte(idx)
	}

	// Minimum: 8 checksum chars + at least 1 data char
	if len(values) < 9 {
		return nil, fmt.Errorf("CashAddr too short")
	}

	// Strip the 8-character checksum without verifying it
	values = values[:len(values)-8]

	return bchConvertBits(values, 5, 8, false)
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *BitcoinCashIICoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
func (c *BitcoinCashIICoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
func (c *BitcoinCashIICoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
func (c *BitcoinCashIICoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in BCH2 consensus rules.
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
func (c *BitcoinCashIICoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

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
func (c *BitcoinCashIICoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// GBTRules returns the rules for getblocktemplate.
// BCH2 uses BCH consensus rules — no SegWit rules required.
func (c *BitcoinCashIICoin) GBTRules() []string {
	return []string{}
}

// DefaultRPCPort returns the default RPC port.
func (c *BitcoinCashIICoin) DefaultRPCPort() int {
	return BCH2DefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *BitcoinCashIICoin) DefaultP2PPort() int {
	return BCH2DefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *BitcoinCashIICoin) P2PKHVersionByte() byte {
	return BCH2P2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *BitcoinCashIICoin) P2SHVersionByte() byte {
	return BCH2P2SHVersion
}

// Bech32HRP returns the bech32 human-readable part (empty — BCH2 uses CashAddr).
func (c *BitcoinCashIICoin) Bech32HRP() string {
	return "" // BCH2 uses CashAddr (bitcoincashii:), not bech32
}

// Algorithm returns the mining algorithm.
func (c *BitcoinCashIICoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
// BCH2 uses BCH consensus rules — SegWit is not supported.
func (c *BitcoinCashIICoin) SupportsSegWit() bool {
	return false
}

// BlockTime returns the target block time in seconds.
func (c *BitcoinCashIICoin) BlockTime() int {
	return 600 // 10 minutes (inherited from BC2)
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *BitcoinCashIICoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
// BCH2 inherits BCH's 100-block maturity.
func (c *BitcoinCashIICoin) CoinbaseMaturity() int {
	return 100
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
// BCH2 shares BC2's genesis block (forked at block 53,200, not at genesis).
func (c *BitcoinCashIICoin) GenesisBlockHash() string {
	return BCH2GenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *BitcoinCashIICoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(BCH2GenesisBlockHash) {
		return fmt.Errorf("BCH2 genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Bitcoin Cash II (bitcoincashIId) "+
			"and RPC port is %d",
			nodeGenesisHash, BCH2GenesisBlockHash, BCH2DefaultRPCPort)
	}
	return nil
}

// init registers Bitcoin Cash II in the coin registry.
func init() {
	Register("BCH2", func() Coin { return NewBitcoinCashIICoin() })
	Register("BITCOINCASHII", func() Coin { return NewBitcoinCashIICoin() })
}
