// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Syscoin (SYS) implementation.
//
// Syscoin is a blockchain platform combining Bitcoin's security with advanced features.
// It implements AuxPoW (merged mining) with Bitcoin, enabling miners to mine both
// chains simultaneously with the same work.
//
// Key characteristics:
//   - SHA-256d algorithm (same as Bitcoin)
//   - 2.5 minute block time (150s, since block 1,317,500 / Syscoin 4.3.0)
//   - AuxPoW (merged mining with Bitcoin) enabled from genesis
//   - UTXO-based with Z-DAG for fast confirmations
//
// References:
//   - https://github.com/syscoin/syscoin
//   - https://syscoin.org/
package coin

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Syscoin mainnet address version bytes
const (
	SyscoinP2PKHVersion byte = 0x3F // Mainnet P2PKH starts with 'S'
	SyscoinP2SHVersion  byte = 0x05 // Mainnet P2SH starts with '3'
	SyscoinBech32HRP         = "sys" // Mainnet bech32 prefix

	// Regtest address version bytes (Bitcoin-compatible for regtest mining)
	SyscoinRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	SyscoinRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
)

// Syscoin network parameters
const (
	SyscoinDefaultP2PPort   = 8369  // P2P network port
	SyscoinDefaultRPCPort   = 8370  // RPC port
	SyscoinBlockTime        = 150   // Target block time: 2.5 minutes (changed from 60s at block 1,317,500, Syscoin 4.3.0)
	SyscoinCoinbaseMaturity = 100   // Blocks before coinbase is spendable
	// Genesis block hash for chain verification
	SyscoinGenesisBlockHash = "0000022642db0346b6e01c2a397471f4f12e65d4f4251ec96c1f85367a61a7ab"
	// Syscoin uses AuxPoW from genesis
	SyscoinAuxPowStartHeight uint64 = 0
)

// Syscoin AuxPoW constants
// CONSENSUS-CRITICAL: These values must exactly match Syscoin Core
const (
	// SyscoinChainID is the unique chain ID for Syscoin in the aux merkle tree.
	// CONSENSUS-CRITICAL: Using wrong chain ID will cause all aux blocks to be rejected.
	// Note: Syscoin changed from old chain ID 4096 (0x1000) to 16 (0x10).
	// Source: syscoin/src/kernel/chainparams.cpp nAuxpowChainId=16, nAuxpowOldChainId=4096
	SyscoinChainID int32 = 0x0010 // 16 decimal (current); old was 0x1000 (4096)

	// SyscoinAuxPowVersionBit is the version bit indicating AuxPoW block.
	SyscoinAuxPowVersionBit uint32 = 0x00000100 // Bit 8 = 256

	// SyscoinAuxPowVersionChainID is the chain ID embedded in version.
	// Chain ID 16 (0x10) shifted left 16 bits = 0x00100000
	SyscoinAuxPowVersionChainID uint32 = 0x00100000
)

// SyscoinCoin implements the Coin interface for Syscoin.
type SyscoinCoin struct{}

// NewSyscoinCoin creates a new Syscoin coin instance.
func NewSyscoinCoin() *SyscoinCoin {
	return &SyscoinCoin{}
}

// Symbol returns the ticker symbol.
func (c *SyscoinCoin) Symbol() string {
	return "SYS"
}

// Name returns the full coin name.
func (c *SyscoinCoin) Name() string {
	return "Syscoin"
}

// ValidateAddress validates a Syscoin address.
func (c *SyscoinCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Syscoin address to its hash and type.
func (c *SyscoinCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32 native SegWit (sys1q... or bcrt1q... for regtest)
	addrLower := strings.ToLower(address)
	hrp := SyscoinBech32HRP
	if strings.HasPrefix(addrLower, "bcrt1") {
		hrp = "bcrt" // Regtest bech32 prefix (same as Bitcoin)
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
		case 1:
			if len(hash) == 32 {
				return hash, AddressTypeP2TR, nil
			}
			return nil, AddressTypeUnknown, fmt.Errorf("invalid v1 witness program length: %d (expected 32)", len(hash))
		default:
			return nil, AddressTypeUnknown, fmt.Errorf("unsupported witness version: %d", witnessVersion)
		}
	}

	// Legacy Base58Check
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
	expectedChecksum := syscoinDoubleSHA256(payload)[:4]

	if subtle.ConstantTimeCompare(checksum, expectedChecksum) != 1 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid checksum")
	}

	version := decoded[0]
	hash := decoded[1:21]

	switch version {
	case SyscoinP2PKHVersion, SyscoinRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case SyscoinP2SHVersion, SyscoinRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x (expected 0x3F/0x6f for P2PKH or 0x05/0xc4 for P2SH)", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *SyscoinCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
	hash, addrType, err := c.DecodeAddress(params.PoolAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid pool address: %w", err)
	}

	switch addrType {
	case AddressTypeP2PKH:
		script := make([]byte, 25)
		script[0] = 0x76 // OP_DUP
		script[1] = 0xa9 // OP_HASH160
		script[2] = 0x14 // PUSH 20 bytes
		copy(script[3:23], hash)
		script[23] = 0x88 // OP_EQUALVERIFY
		script[24] = 0xac // OP_CHECKSIG
		return script, nil

	case AddressTypeP2SH:
		script := make([]byte, 23)
		script[0] = 0xa9 // OP_HASH160
		script[1] = 0x14 // PUSH 20 bytes
		copy(script[2:22], hash)
		script[22] = 0x87 // OP_EQUAL
		return script, nil

	case AddressTypeP2WPKH:
		script := make([]byte, 22)
		script[0] = 0x00
		script[1] = 0x14
		copy(script[2:22], hash)
		return script, nil

	case AddressTypeP2WSH:
		script := make([]byte, 34)
		script[0] = 0x00
		script[1] = 0x20
		copy(script[2:34], hash)
		return script, nil

	case AddressTypeP2TR:
		script := make([]byte, 34)
		script[0] = 0x51 // OP_1
		script[1] = 0x20 // PUSH 32 bytes
		copy(script[2:34], hash)
		return script, nil

	default:
		return nil, fmt.Errorf("unsupported address type: %v", addrType)
	}
}

// SerializeBlockHeader serializes an 80-byte block header.
func (c *SyscoinCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
func (c *SyscoinCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
func (c *SyscoinCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

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
func (c *SyscoinCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	diff1Float := new(big.Float).SetInt(diff1Target)
	targetFloat := new(big.Float).SetInt(target)

	result := new(big.Float).Quo(diff1Float, targetFloat)
	difficulty, _ := result.Float64()

	return difficulty
}

// ShareDifficultyMultiplier returns the multiplier for share difficulty.
func (c *SyscoinCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// DefaultRPCPort returns the default RPC port.
func (c *SyscoinCoin) DefaultRPCPort() int {
	return SyscoinDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *SyscoinCoin) DefaultP2PPort() int {
	return SyscoinDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *SyscoinCoin) P2PKHVersionByte() byte {
	return SyscoinP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *SyscoinCoin) P2SHVersionByte() byte {
	return SyscoinP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *SyscoinCoin) Bech32HRP() string {
	return SyscoinBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *SyscoinCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
func (c *SyscoinCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *SyscoinCoin) BlockTime() int {
	return SyscoinBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *SyscoinCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *SyscoinCoin) CoinbaseMaturity() int {
	return SyscoinCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *SyscoinCoin) GenesisBlockHash() string {
	return SyscoinGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *SyscoinCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(SyscoinGenesisBlockHash) {
		return fmt.Errorf("SYS genesis block mismatch: got %s, expected %s",
			nodeGenesisHash, SyscoinGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXPOW (MERGE MINING) IMPLEMENTATION
// ═══════════════════════════════════════════════════════════════════════════════

// SupportsAuxPow returns whether the coin supports auxiliary proof of work.
func (c *SyscoinCoin) SupportsAuxPow() bool {
	return true
}

// AuxPowStartHeight returns the block height at which AuxPoW was enabled.
func (c *SyscoinCoin) AuxPowStartHeight() uint64 {
	return SyscoinAuxPowStartHeight
}

// ChainID returns Syscoin's unique chain identifier for the aux merkle tree.
func (c *SyscoinCoin) ChainID() int32 {
	return SyscoinChainID
}

// AuxPowVersionBit returns the version bit that indicates an AuxPoW block.
func (c *SyscoinCoin) AuxPowVersionBit() uint32 {
	return SyscoinAuxPowVersionBit
}

// ParseAuxBlockResponse parses the getauxblock RPC response from Syscoin Core.
func (c *SyscoinCoin) ParseAuxBlockResponse(response map[string]interface{}) (*AuxBlock, error) {
	auxBlock := &AuxBlock{
		ChainID: SyscoinChainID,
	}

	hashHex, ok := response["hash"].(string)
	if !ok || len(hashHex) != 64 {
		return nil, fmt.Errorf("invalid or missing aux block hash")
	}
	hash, err := hex.DecodeString(hashHex)
	if err != nil {
		return nil, fmt.Errorf("invalid aux block hash hex: %w", err)
	}
	auxBlock.Hash = syscoinReverseBytes(hash)

	if chainID, ok := response["chainid"].(float64); ok {
		if int32(chainID) != SyscoinChainID {
			return nil, fmt.Errorf("chain ID mismatch: got %d, expected %d", int32(chainID), SyscoinChainID)
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

	if height, ok := response["height"].(float64); ok {
		auxBlock.Height = uint64(height)
	}

	if value, ok := response["coinbasevalue"].(float64); ok {
		auxBlock.CoinbaseValue = int64(value)
	}

	if prevHashHex, ok := response["previousblockhash"].(string); ok && len(prevHashHex) == 64 {
		prevHash, err := hex.DecodeString(prevHashHex)
		if err == nil {
			auxBlock.PreviousBlockHash = syscoinReverseBytes(prevHash)
		}
	}

	auxBlock.ChainIndex = 0

	return auxBlock, nil
}

// SerializeAuxPowProof serializes the complete AuxPoW proof for block submission.
func (c *SyscoinCoin) SerializeAuxPowProof(proof *AuxPowProof) ([]byte, error) {
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
	buf.Write(syscoinEncodeVarInt(uint64(len(proof.CoinbaseMerkleBranch))))
	for i, hash := range proof.CoinbaseMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid coinbase branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	binary.Write(buf, binary.LittleEndian, uint32(proof.CoinbaseMerkleIndex))

	buf.Write(syscoinEncodeVarInt(uint64(len(proof.AuxMerkleBranch))))
	for i, hash := range proof.AuxMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid aux branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	binary.Write(buf, binary.LittleEndian, uint32(proof.AuxMerkleIndex))

	if len(proof.ParentHeader) != 80 {
		return nil, fmt.Errorf("invalid parent header length: got %d, expected 80", len(proof.ParentHeader))
	}
	buf.Write(proof.ParentHeader)

	return buf.Bytes(), nil
}

// syscoinDoubleSHA256 computes double SHA256 hash.
func syscoinDoubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// syscoinReverseBytes returns a new slice with bytes in reverse order.
func syscoinReverseBytes(b []byte) []byte {
	result := make([]byte, len(b))
	for i, j := 0, len(b)-1; i <= j; i, j = i+1, j-1 {
		result[i], result[j] = b[j], b[i]
	}
	return result
}

// syscoinEncodeVarInt encodes an integer as a Bitcoin-style variable length integer.
func syscoinEncodeVarInt(n uint64) []byte {
	if n < 0xfd {
		return []byte{byte(n)}
	} else if n <= 0xffff {
		return []byte{0xfd, byte(n), byte(n >> 8)}
	} else if n <= 0xffffffff {
		return []byte{0xfe, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	}
	return []byte{0xff, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24),
		byte(n >> 32), byte(n >> 40), byte(n >> 48), byte(n >> 56)}
}

// init registers Syscoin in the coin registry.
func init() {
	Register("SYS", func() Coin { return NewSyscoinCoin() })
	Register("SYSCOIN", func() Coin { return NewSyscoinCoin() })
}
