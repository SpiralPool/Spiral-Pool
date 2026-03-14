// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Q-BitX (QBX) implementation.
//
// Q-BitX is a SHA-256d Bitcoin fork with post-quantum cryptography features
// (Dilithium signatures). It uses Dilithium-derived address version bytes,
// NOT Bitcoin's 0x00/0x05 — addresses start with 'M' (P2PKH) or 'P' (P2SH).
//
// Key characteristics:
//   - SHA-256d algorithm (same as Bitcoin)
//   - 150 second block time (2.5 minutes)
//   - 12.5 QBX initial block reward, halving every 840,000 blocks
//   - No SegWit support
//   - No AuxPoW (standalone mining only)
//   - Post-quantum "pq" address type (Dilithium signatures)
//   - P2PKH addresses start with 'M' (version byte 0x32)
//   - P2SH addresses start with 'P' (version byte 0x37)
//
// References:
//   - https://qbitx.org/
//   - https://github.com/q-bitx/Source-
package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Q-BitX mainnet address version bytes
// NOTE: Q-BitX uses Dilithium-derived version bytes, NOT Bitcoin's 0x00/0x05.
const (
	QBXP2PKHVersion byte = 0x32 // Mainnet P2PKH starts with 'M' (0x32 = 50)
	QBXP2SHVersion  byte = 0x37 // Mainnet P2SH starts with 'P' (0x37 = 55)
	QBXBech32HRP         = "bc" // Mainnet bech32 prefix (SegWit not supported by QBX)

	// Regtest address version bytes
	QBXRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	QBXRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
)

// Q-BitX network parameters
// NOTE: Q-BitX defaults to P2P 8334 and RPC 8332, which conflict with Bitcoin and Namecoin.
// When running alongside other coins, we use remapped ports:
//   - 8344: QBX RPC (new allocation)
//   - 8345: QBX P2P (new allocation)
const (
	QBXDefaultP2PPort   = 8345  // P2P network port (unique, no conflict)
	QBXDefaultRPCPort   = 8344  // RPC port (unique, no conflict)
	QBXBlockTime        = 150   // Target block time: 150 seconds (2.5 minutes)
	QBXCoinbaseMaturity = 100   // Blocks before coinbase is spendable
	// Genesis block hash for chain verification
	QBXGenesisBlockHash = "407cdbc2ca102bd9e69069f25cebc2ef363a427166edba7580b41031b68549d9"
)

// QBXCoin implements the Coin interface for Q-BitX.
type QBXCoin struct{}

// NewQBXCoin creates a new Q-BitX coin instance.
func NewQBXCoin() *QBXCoin {
	return &QBXCoin{}
}

// Symbol returns the ticker symbol.
func (c *QBXCoin) Symbol() string {
	return "QBX"
}

// Name returns the full coin name.
func (c *QBXCoin) Name() string {
	return "Q-BitX"
}

// ValidateAddress validates a Q-BitX address.
// Supports legacy P2PKH (M...), P2SH (P...), and post-quantum (pq...) addresses.
func (c *QBXCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Q-BitX address to its hash and type.
// Q-BitX supports:
//   - P2PKH (M...) - legacy pay-to-pubkey-hash (version byte 0x32)
//   - P2SH (P...) - legacy pay-to-script-hash (version byte 0x37)
//   - PQ (pq...) - post-quantum Dilithium address
//
// NOTE: SegWit (bc1...) addresses are NOT supported by Q-BitX.
func (c *QBXCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Post-quantum address (pq...)
	// PQ addresses are a separate address format for Dilithium wallets.
	// For pool mining, use a standard M... (P2PKH) address — the stratum
	// automatically builds the required Dilithium P2PKH coinbase script
	// (OP_CHECKSIGDILITHIUM) from the M... address hash.
	if strings.HasPrefix(address, "pq") {
		if len(address) < 22 || len(address) > 82 {
			return nil, AddressTypeUnknown, fmt.Errorf("invalid pq address length: %d (expected 22-82)", len(address))
		}
		// pq address encoding is not Base58Check — we cannot decode the hash.
		// Use a standard M... (P2PKH) address for the pool mining address.
		return nil, AddressTypeUnknown, fmt.Errorf("pq addresses cannot be decoded by stratum — use a standard M... (P2PKH) address for pool mining")
	}

	// Regtest bech32 (bcrt1...) — only for testing
	addrLower := strings.ToLower(address)
	if strings.HasPrefix(addrLower, "bcrt1") {
		decoded, err := decodeBech32Address(address, "bcrt")
		if err != nil {
			return nil, AddressTypeUnknown, err
		}
		if len(decoded) < 2 {
			return nil, AddressTypeUnknown, fmt.Errorf("decoded data too short")
		}
		witnessVersion := decoded[0]
		hash := decoded[1:]
		if witnessVersion == 0 && len(hash) == 20 {
			return hash, AddressTypeP2WPKH, nil
		}
		return nil, AddressTypeUnknown, fmt.Errorf("Q-BitX does not support SegWit addresses on mainnet (regtest only)")
	}

	// Reject mainnet bech32 addresses (Q-BitX does not support SegWit)
	if strings.HasPrefix(addrLower, "bc1") {
		return nil, AddressTypeUnknown, fmt.Errorf("Q-BitX does not support SegWit (bc1) addresses — use a legacy (M...) or P2SH (P...) address")
	}

	// Legacy Base58Check (1... for P2PKH, 3... for P2SH)
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
	expectedChecksum := qbxDoubleSHA256(payload)[:4]

	// SECURITY: Use constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare(checksum, expectedChecksum) != 1 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid checksum")
	}

	version := decoded[0]
	hash := decoded[1:21]

	switch version {
	case QBXP2PKHVersion, QBXRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case QBXP2SHVersion, QBXRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x (expected 0x32/0x6f for P2PKH or 0x37/0xc4 for P2SH)", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
//
// CRITICAL: QBX consensus rules (validation.cpp CheckBlock) require that every
// coinbase vout[0] uses IsPQPayToPubKeyHash() — a Dilithium P2PKH script ending
// with OP_CHECKSIGDILITHIUM (0xC4), NOT standard OP_CHECKSIG (0xAC).
// Submitting a block with a standard P2PKH coinbase returns "bad-cb-pq".
//
// For P2PKH (M...) addresses we extract the 20-byte hash and build the
// Dilithium variant:
//
//	OP_DUP OP_HASH160 <20 bytes> OP_EQUALVERIFY OP_CHECKSIGDILITHIUM
func (c *QBXCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
	hash, addrType, err := c.DecodeAddress(params.PoolAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid pool address: %w", err)
	}

	switch addrType {
	case AddressTypeP2PKH:
		// QBX REQUIRES OP_CHECKSIGDILITHIUM (0xC4) for coinbase outputs.
		// Using standard OP_CHECKSIG (0xAC) causes "bad-cb-pq" rejection.
		script := make([]byte, 25)
		script[0] = 0x76 // OP_DUP
		script[1] = 0xa9 // OP_HASH160
		script[2] = 0x14 // PUSH 20 bytes
		copy(script[3:23], hash)
		script[23] = 0x88 // OP_EQUALVERIFY
		script[24] = 0xc4 // OP_CHECKSIGDILITHIUM (QBX-specific, NOT 0xac OP_CHECKSIG)
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
		return nil, fmt.Errorf("unsupported address type for QBX coinbase: %v (use P2PKH M... or P2SH P... address)", addrType)
	}
}

// SerializeBlockHeader serializes an 80-byte block header.
// Q-BitX uses the same block header format as Bitcoin.
func (c *QBXCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
func (c *QBXCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
func (c *QBXCoin) TargetFromBits(bits uint32) *big.Int {
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
func (c *QBXCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Difficulty 1 target (same as Bitcoin: 0x1d00ffff)
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
func (c *QBXCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// DefaultRPCPort returns the default RPC port.
func (c *QBXCoin) DefaultRPCPort() int {
	return QBXDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *QBXCoin) DefaultP2PPort() int {
	return QBXDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *QBXCoin) P2PKHVersionByte() byte {
	return QBXP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *QBXCoin) P2SHVersionByte() byte {
	return QBXP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
// NOTE: Q-BitX does not support SegWit, but shares the "bc" HRP with Bitcoin
// for compatibility. The pool rejects bc1 addresses for QBX mining.
func (c *QBXCoin) Bech32HRP() string {
	return QBXBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *QBXCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
// Q-BitX does NOT support SegWit.
func (c *QBXCoin) SupportsSegWit() bool {
	return false
}

// BlockTime returns the target block time in seconds.
func (c *QBXCoin) BlockTime() int {
	return QBXBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *QBXCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *QBXCoin) CoinbaseMaturity() int {
	return QBXCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *QBXCoin) GenesisBlockHash() string {
	return QBXGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *QBXCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(QBXGenesisBlockHash) {
		return fmt.Errorf("QBX genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Q-BitX Core",
			nodeGenesisHash, QBXGenesisBlockHash)
	}
	return nil
}

// qbxDoubleSHA256 computes double SHA256 hash.
func qbxDoubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// init registers Q-BitX in the coin registry.
func init() {
	Register("QBX", func() Coin { return NewQBXCoin() })
	Register("QBITX", func() Coin { return NewQBXCoin() })
}
