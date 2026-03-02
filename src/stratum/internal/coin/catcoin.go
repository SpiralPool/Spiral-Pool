// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Catcoin (CAT) implementation.
//
// Catcoin is the first cat-themed meme coin, launched on December 23, 2013
// in response to Dogecoin. It uses Scrypt PoW with Bitcoin-like parameters.
//
// Key characteristics:
//   - Scrypt algorithm (N=1024, r=1, p=1) - same as Litecoin
//   - 10 minute block time (similar to Bitcoin)
//   - 21 million total supply
//   - Block reward halving every 210,000 blocks (same as Bitcoin)
//   - Bech32 addresses with "cat" prefix
//   - LWMA difficulty adjustment algorithm
//   - First PoW coin to implement PID controller for difficulty
//
// References:
//   - https://github.com/CatcoinCore/catcoincore
//   - https://github.com/catcoin-project/catcoin
//   - https://www.catcoin2013.org/
package coin

import (
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"

	"github.com/spiralpool/stratum/internal/crypto"
)

// Catcoin mainnet address version bytes
const (
	CatcoinP2PKHVersion byte = 0x15 // 21 decimal - addresses start with '9' (cats have 9 lives)
	CatcoinP2SHVersion  byte = 0x58 // 88 decimal - P2SH addresses
	CatcoinBech32HRP         = "cat" // Bech32 prefix — NOTE: SegWit is DISABLED on mainnet (SegwitHeight=INT_MAX)

	// Regtest/testnet address version bytes
	// Catcoin uses custom regtest version bytes (not Bitcoin-compatible)
	CatcoinRegtestP2PKHVersion  byte = 0x12 // 18 decimal - regtest P2PKH starts with '8'
	CatcoinTestnetP2PKHVersion  byte = 0x6f // 111 decimal - testnet P2PKH starts with 'm' or 'n'
	CatcoinRegtestP2SHVersion   byte = 0xc4 // 196 decimal - regtest P2SH starts with '2'
)

// Catcoin network parameters
const (
	CatcoinDefaultP2PPort   = 9933  // P2P network port
	CatcoinDefaultRPCPort   = 9932  // RPC port
	CatcoinBlockTime        = 600   // Target block time: 10 minutes (like Bitcoin)
	CatcoinCoinbaseMaturity = 100   // Blocks before coinbase is spendable
	// Genesis block hash for chain verification
	// Hash: bc3b4ec43c4ebb2fef49e6240812549e61ffa623d9418608aa90eaad26c96296
	// Date: December 23, 2013
	CatcoinGenesisBlockHash = "bc3b4ec43c4ebb2fef49e6240812549e61ffa623d9418608aa90eaad26c96296"
)

// CatcoinCoin implements the Coin interface for Catcoin.
// Note: Catcoin does NOT support AuxPoW/merge mining.
type CatcoinCoin struct{}

// NewCatcoinCoin creates a new Catcoin coin instance.
func NewCatcoinCoin() *CatcoinCoin {
	return &CatcoinCoin{}
}

// Symbol returns the ticker symbol.
func (c *CatcoinCoin) Symbol() string {
	return "CAT"
}

// Name returns the full coin name.
func (c *CatcoinCoin) Name() string {
	return "Catcoin"
}

// ValidateAddress validates a Catcoin address.
func (c *CatcoinCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Catcoin address to its hash and type.
// Supports P2PKH, P2SH, P2WPKH, P2WSH, and P2TR address formats.
func (c *CatcoinCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32/Bech32m native SegWit (cat1q... or cat1p... or rcat1q... for regtest)
	addrLower := strings.ToLower(address)
	hrp := CatcoinBech32HRP
	if strings.HasPrefix(addrLower, "rcat1") {
		hrp = "rcat" // Regtest bech32 prefix
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

	// Legacy Base58Check (9... for P2PKH, version byte 0x15=21)
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
	case CatcoinP2PKHVersion, CatcoinRegtestP2PKHVersion, CatcoinTestnetP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case CatcoinP2SHVersion, CatcoinRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x (expected 0x15/0x12/0x6f for P2PKH or 0x58/0xc4 for P2SH)", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *CatcoinCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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

	case AddressTypeP2WPKH, AddressTypeP2WSH, AddressTypeP2TR:
		// SegWit is DISABLED on Catcoin mainnet (SegwitHeight=INT_MAX).
		// Accepting bech32 addresses would create unspendable coinbase outputs.
		return nil, fmt.Errorf("SegWit addresses (cat1...) are not supported on Catcoin mainnet — use a P2PKH address starting with 9")

	default:
		return nil, fmt.Errorf("unsupported address type: %v", addrType)
	}
}

// SerializeBlockHeader serializes an 80-byte block header.
// Catcoin uses the same block header format as Bitcoin.
func (c *CatcoinCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
// Catcoin uses the same Scrypt parameters as Litecoin.
func (c *CatcoinCoin) HashBlockHeader(serialized []byte) []byte {
	return crypto.ScryptHash(serialized)
}

// TargetFromBits converts compact bits representation to target.
// Uses the same compact target encoding as Bitcoin.
func (c *CatcoinCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	// SECURITY: Negative targets are invalid in consensus rules.
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
// Note: Catcoin uses the same difficulty formula as Bitcoin.
func (c *CatcoinCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Catcoin difficulty 1 target (same as Bitcoin: 0x1d00ffff)
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
func (c *CatcoinCoin) ShareDifficultyMultiplier() float64 {
	return 65536.0
}

// GBTRules returns the rules for getblocktemplate.
// Catcoin Core is forked from Litecoin Core and requires MWEB+SegWit rules.
// The daemon rejects GBT calls without both rules present.
func (c *CatcoinCoin) GBTRules() []string {
	return []string{"mweb", "segwit"}
}

// DefaultRPCPort returns the default RPC port.
func (c *CatcoinCoin) DefaultRPCPort() int {
	return CatcoinDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *CatcoinCoin) DefaultP2PPort() int {
	return CatcoinDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *CatcoinCoin) P2PKHVersionByte() byte {
	return CatcoinP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *CatcoinCoin) P2SHVersionByte() byte {
	return CatcoinP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *CatcoinCoin) Bech32HRP() string {
	return CatcoinBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *CatcoinCoin) Algorithm() string {
	return "scrypt"
}

// SupportsSegWit returns whether the coin supports SegWit.
// NOTE: Catcoin Core code has bech32 infrastructure (cat1q) but SegWit is
// DISABLED on mainnet (SegwitHeight=INT_MAX). The daemon handles this —
// it won't produce SegWit block templates. We return true so the stratum
// requests the segwit-compatible template format (the daemon ignores it
// gracefully). However, cat1 addresses should NOT be used for pool payouts
// because SegWit outputs would be unspendable on mainnet.
func (c *CatcoinCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *CatcoinCoin) BlockTime() int {
	return CatcoinBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *CatcoinCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *CatcoinCoin) CoinbaseMaturity() int {
	return CatcoinCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *CatcoinCoin) GenesisBlockHash() string {
	return CatcoinGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *CatcoinCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(CatcoinGenesisBlockHash) {
		return fmt.Errorf("CAT genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Catcoin Core",
			nodeGenesisHash, CatcoinGenesisBlockHash)
	}
	return nil
}

// init registers Catcoin in the coin registry.
func init() {
	Register("CAT", func() Coin { return NewCatcoinCoin() })
	Register("CATCOIN", func() Coin { return NewCatcoinCoin() })
}
