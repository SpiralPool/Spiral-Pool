// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Bitcoin Silver (BTCS) implementation.
//
// Bitcoin Silver is a SHA-256d Bitcoin fork with 5-minute block times,
// 200-block coinbase maturity, and full SegWit/Taproot support from block 0.
// It uses standard Bitcoin (BTC-style) consensus — no BCH rules.
//
// Address formats:
//   - Legacy P2PKH:  'B' addresses  (prefix byte 0x1A)
//   - Legacy P2SH:   '3' addresses  (prefix byte 0x05)
//   - Bech32 SegWit: 'bs1q' addresses (bech32 HRP "bs")
//   - Bech32m Taproot: 'bs1p' addresses (bech32m HRP "bs")
//
// References:
// - https://github.com/bitcoin-silver/core
// - src/kernel/chainparams.cpp (verified 2026-04-12)
package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Bitcoin Silver mainnet address version bytes
// Verified from bitcoin-silver/core/src/kernel/chainparams.cpp:
//
//	base58Prefixes[PUBKEY_ADDRESS] = std::vector<unsigned char>(1, 0x1A); // 'B'
//	base58Prefixes[SCRIPT_ADDRESS] = std::vector<unsigned char>(1, 0x05); // '3'
const (
	BTCSMainP2PKHVersion    byte = 0x1A // Mainnet P2PKH starts with 'B' (decimal 26)
	BTCSMainP2SHVersion     byte = 0x05 // Mainnet P2SH starts with '3' (same as BTC)
	BTCSRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	BTCSRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
	BTCSBech32HRP                = "bs" // Mainnet bech32 prefix: "bs1q..." / "bs1p..."
)

// Bitcoin Silver network parameters
// Verified from bitcoin-silver/core/src/kernel/chainparams.cpp and chainparamsbase.cpp:
//
//	nDefaultPort = 10566 (P2P)
//	RPC port = 10567
const (
	BTCSDefaultP2PPort   = 10566 // P2P network port
	BTCSDefaultRPCPort   = 10567 // RPC port
	BTCSBlockTime        = 300   // Target block time: 5 minutes (300 seconds)
	BTCSCoinbaseMaturity = 200   // Blocks before coinbase is spendable (vs BTC's 100)
)

// BTCSGenesisBlockHash is the genesis block hash for chain verification.
// Verified from bitcoin-silver/core/src/kernel/chainparams.cpp:
//
//	assert(consensus.hashGenesisBlock == uint256S("0x00000ea8e97e04892a03df35947ff0c4df705723f5b18be7cc6456ed16e9788e"))
//
// Genesis timestamp: 1720806555 (July 12, 2024)
const BTCSGenesisBlockHash = "00000ea8e97e04892a03df35947ff0c4df705723f5b18be7cc6456ed16e9788e"

// BitcoinSilverCoin implements the Coin interface for Bitcoin Silver.
type BitcoinSilverCoin struct{}

// NewBitcoinSilverCoin creates a new Bitcoin Silver coin instance.
func NewBitcoinSilverCoin() *BitcoinSilverCoin {
	return &BitcoinSilverCoin{}
}

// Symbol returns the ticker symbol.
func (c *BitcoinSilverCoin) Symbol() string {
	return "BTCS"
}

// Name returns the full coin name.
func (c *BitcoinSilverCoin) Name() string {
	return "Bitcoin Silver"
}

// ValidateAddress validates a Bitcoin Silver address.
func (c *BitcoinSilverCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Bitcoin Silver address to its hash and type.
// Supports P2PKH ('B' addresses), P2SH ('3' addresses), bech32 SegWit (bs1q...),
// and bech32m Taproot (bs1p...).
func (c *BitcoinSilverCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32/Bech32m native SegWit (bs1q... or bs1p...)
	addrLower := strings.ToLower(address)
	hrp := BTCSBech32HRP
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

	// Legacy Base58Check ('B' P2PKH or '3' P2SH)
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
	case BTCSMainP2PKHVersion, BTCSRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case BTCSMainP2SHVersion, BTCSRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unknown address version: 0x%02x", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *BitcoinSilverCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
// Bitcoin Silver uses the standard Bitcoin block header format.
func (c *BitcoinSilverCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
func (c *BitcoinSilverCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
func (c *BitcoinSilverCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in BTCS consensus rules.
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
func (c *BitcoinSilverCoin) DifficultyFromTarget(target *big.Int) float64 {
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
func (c *BitcoinSilverCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// GBTRules returns the rules for getblocktemplate.
// BTCS has SegWit active from block 0.
func (c *BitcoinSilverCoin) GBTRules() []string {
	return []string{"segwit"}
}

// DefaultRPCPort returns the default RPC port.
func (c *BitcoinSilverCoin) DefaultRPCPort() int {
	return BTCSDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *BitcoinSilverCoin) DefaultP2PPort() int {
	return BTCSDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *BitcoinSilverCoin) P2PKHVersionByte() byte {
	return BTCSMainP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *BitcoinSilverCoin) P2SHVersionByte() byte {
	return BTCSMainP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
// BTCS uses "bs" → addresses appear as "bs1q..." (SegWit) or "bs1p..." (Taproot).
func (c *BitcoinSilverCoin) Bech32HRP() string {
	return BTCSBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *BitcoinSilverCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
// BTCS has SegWit and Taproot active from block 0.
func (c *BitcoinSilverCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
// BTCS uses 5-minute blocks (300s), half of Bitcoin's 10 minutes.
func (c *BitcoinSilverCoin) BlockTime() int {
	return BTCSBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *BitcoinSilverCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
// BTCS uses 200 blocks (vs Bitcoin's 100).
func (c *BitcoinSilverCoin) CoinbaseMaturity() int {
	return BTCSCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *BitcoinSilverCoin) GenesisBlockHash() string {
	return BTCSGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *BitcoinSilverCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(BTCSGenesisBlockHash) {
		return fmt.Errorf("BTCS genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Bitcoin Silver (bitcoinsilverd)",
			nodeGenesisHash, BTCSGenesisBlockHash)
	}
	return nil
}

// init registers Bitcoin Silver in the coin registry.
func init() {
	Register("BTCS", func() Coin { return NewBitcoinSilverCoin() })
	Register("BITCOINSILVER", func() Coin { return NewBitcoinSilverCoin() })
}
