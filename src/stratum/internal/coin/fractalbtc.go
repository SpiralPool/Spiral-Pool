// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Fractal Bitcoin (FBTC) implementation.
//
// Fractal Bitcoin is a Bitcoin scaling solution that uses Bitcoin Core code
// to recursively scale unlimited layers. It implements merge mining (AuxPoW)
// with Bitcoin as the parent chain.
//
// Key characteristics:
//   - SHA-256d algorithm (same as Bitcoin)
//   - 30 second block time
//   - AuxPoW (merged mining with Bitcoin) enabled from genesis
//   - Uses "Cadence Mining": 2 permissionless + 1 merged-mined per 3 blocks
//   - Same address format as Bitcoin (bc1, 1..., 3...)
//
// References:
//   - https://github.com/fractal-bitcoin/fractal
//   - https://fractalbitcoin.io/
//   - https://docs.fractalbitcoin.io/node-operation/mining
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

// Fractal Bitcoin mainnet address version bytes
// NOTE: Fractal Bitcoin uses the SAME address format as Bitcoin
const (
	FractalBTCP2PKHVersion byte = 0x00 // Mainnet P2PKH starts with '1' (same as Bitcoin)
	FractalBTCP2SHVersion  byte = 0x05 // Mainnet P2SH starts with '3' (same as Bitcoin)
	FractalBTCBech32HRP         = "bc" // Mainnet bech32 prefix (same as Bitcoin)

	// Regtest address version bytes (Bitcoin-compatible for regtest mining)
	FractalBTCRegtestP2PKHVersion byte = 0x6f // Regtest P2PKH starts with 'm' or 'n'
	FractalBTCRegtestP2SHVersion  byte = 0xc4 // Regtest P2SH starts with '2'
)

// Fractal Bitcoin network parameters
// NOTE: Fractal uses Bitcoin Core code with same default ports (8332/8333).
// However, when running alongside Bitcoin for merge mining, we MUST use
// different ports to avoid conflicts. Port allocation:
//   - 8334: NMC P2P (taken)
//   - 8335: DOGE Stratum V1 (taken)
//   - 8336: NMC RPC (taken)
//   - 8337: DOGE Stratum V2 (taken)
//   - 8338: BC2 P2P (taken)
//   - 8339: BC2 RPC (taken)
//   - 8340: FBTC RPC (new allocation)
//   - 8341: FBTC P2P (new allocation)
const (
	FractalBTCDefaultP2PPort   = 8341  // P2P network port (unique, no conflict)
	FractalBTCDefaultRPCPort   = 8340  // RPC port (unique, no conflict)
	FractalBTCBlockTime        = 30    // Target block time: 30 seconds
	FractalBTCCoinbaseMaturity = 100   // Blocks before coinbase is spendable
	// Genesis block hash for chain verification
	// Fractal Bitcoin intentionally uses the SAME genesis block as Bitcoin
	// to honor Bitcoin's origins. The genesis block reward is unspendable.
	// Hash: 000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f
	FractalBTCGenesisBlockHash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	// AuxPoW is enabled from genesis (block 0)
	// Fractal uses "Cadence Mining": 2 permissionless + 1 merged-mined per 3 blocks
	FractalBTCAuxPowStartHeight uint64 = 0
)

// Fractal Bitcoin AuxPoW constants
// CONSENSUS-CRITICAL: These values must exactly match Fractal Bitcoin Core
const (
	// FractalBTCChainID is the unique chain ID for Fractal Bitcoin in the aux merkle tree.
	// This value is specified in Fractal's chainparams as nAuxpowChainId = 0x2024.
	// CONSENSUS-CRITICAL: Using wrong chain ID will cause all aux blocks to be rejected.
	FractalBTCChainID int32 = 0x2024 // 8228 decimal

	// FractalBTCAuxPowVersionBit is the version bit indicating AuxPoW block.
	// When this bit is set in the block version, the block uses auxiliary proof of work.
	// Standard AuxPoW version bit: (1 << 8) = 0x100
	// CONSENSUS-CRITICAL: Must be set for merged-mined blocks.
	FractalBTCAuxPowVersionBit uint32 = 0x00000100 // Bit 8 = 256

	// FractalBTCAuxPowVersionChainID is the chain ID embedded in version.
	// Version format: (chainID << 16) | AuxPowBit | baseVersion
	// For Fractal: 0x20240000 = (0x2024 << 16)
	FractalBTCAuxPowVersionChainID uint32 = 0x20240000
)

// FractalBTCCoin implements the Coin interface for Fractal Bitcoin.
type FractalBTCCoin struct{}

// NewFractalBTCCoin creates a new Fractal Bitcoin coin instance.
func NewFractalBTCCoin() *FractalBTCCoin {
	return &FractalBTCCoin{}
}

// Symbol returns the ticker symbol.
func (c *FractalBTCCoin) Symbol() string {
	return "FBTC"
}

// Name returns the full coin name.
func (c *FractalBTCCoin) Name() string {
	return "Fractal Bitcoin"
}

// ValidateAddress validates a Fractal Bitcoin address.
// Fractal Bitcoin uses the same address format as Bitcoin.
func (c *FractalBTCCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress decodes a Fractal Bitcoin address to its hash and type.
// Fractal Bitcoin supports the same address types as Bitcoin:
// P2PKH (1...), P2SH (3...), and bech32 (bc1q.../bc1p...)
func (c *FractalBTCCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if address == "" {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Bech32 native SegWit (bc1q... or bc1p... or bcrt1q... for regtest)
	addrLower := strings.ToLower(address)
	hrp := FractalBTCBech32HRP
	if strings.HasPrefix(addrLower, "bcrt1") {
		hrp = "bcrt" // Regtest bech32 prefix
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
	expectedChecksum := fractalBTCDoubleSHA256(payload)[:4]

	// SECURITY: Use constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare(checksum, expectedChecksum) != 1 {
		return nil, AddressTypeUnknown, fmt.Errorf("invalid checksum")
	}

	version := decoded[0]
	hash := decoded[1:21]

	switch version {
	case FractalBTCP2PKHVersion, FractalBTCRegtestP2PKHVersion:
		return hash, AddressTypeP2PKH, nil
	case FractalBTCP2SHVersion, FractalBTCRegtestP2SHVersion:
		return hash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x (expected 0x00/0x6f for P2PKH or 0x05/0xc4 for P2SH)", version)
	}
}

// BuildCoinbaseScript builds the output script for the coinbase transaction.
func (c *FractalBTCCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
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
		script[0] = 0x00
		script[1] = 0x14
		copy(script[2:22], hash)
		return script, nil

	case AddressTypeP2WSH:
		// OP_0 <32 bytes>
		script := make([]byte, 34)
		script[0] = 0x00
		script[1] = 0x20
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
// Fractal Bitcoin uses the same block header format as Bitcoin.
func (c *FractalBTCCoin) SerializeBlockHeader(header *BlockHeader) []byte {
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
// Fractal Bitcoin uses the same PoW algorithm as Bitcoin.
func (c *FractalBTCCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact bits representation to target.
// Uses the same compact target encoding as Bitcoin.
func (c *FractalBTCCoin) TargetFromBits(bits uint32) *big.Int {
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
// Fractal Bitcoin uses the same difficulty formula as Bitcoin.
func (c *FractalBTCCoin) DifficultyFromTarget(target *big.Int) float64 {
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
// Fractal Bitcoin uses the same difficulty calculation as Bitcoin.
func (c *FractalBTCCoin) ShareDifficultyMultiplier() float64 {
	return 1.0
}

// DefaultRPCPort returns the default RPC port.
func (c *FractalBTCCoin) DefaultRPCPort() int {
	return FractalBTCDefaultRPCPort
}

// DefaultP2PPort returns the default P2P port.
func (c *FractalBTCCoin) DefaultP2PPort() int {
	return FractalBTCDefaultP2PPort
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *FractalBTCCoin) P2PKHVersionByte() byte {
	return FractalBTCP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *FractalBTCCoin) P2SHVersionByte() byte {
	return FractalBTCP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *FractalBTCCoin) Bech32HRP() string {
	return FractalBTCBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *FractalBTCCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
// Fractal Bitcoin is based on Bitcoin Core v29 which fully supports SegWit.
func (c *FractalBTCCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *FractalBTCCoin) BlockTime() int {
	return FractalBTCBlockTime
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *FractalBTCCoin) MinCoinbaseScriptLen() int {
	return 2
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *FractalBTCCoin) CoinbaseMaturity() int {
	return FractalBTCCoinbaseMaturity
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *FractalBTCCoin) GenesisBlockHash() string {
	return FractalBTCGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *FractalBTCCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(FractalBTCGenesisBlockHash) {
		return fmt.Errorf("FBTC genesis block mismatch: got %s, expected %s - "+
			"verify your node is running Fractal Bitcoin Core",
			nodeGenesisHash, FractalBTCGenesisBlockHash)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXPOW (MERGE MINING) IMPLEMENTATION
// ═══════════════════════════════════════════════════════════════════════════════
//
// Fractal Bitcoin implements AuxPoW for merge mining with Bitcoin as the parent.
// The integration mechanism is identical to Namecoin's AuxPoW implementation.
//
// Key features:
// - Chain ID: 0x2024 (8228 decimal)
// - AuxPoW enabled from genesis (block 0)
// - Uses "Cadence Mining": 2 permissionless + 1 merged-mined per 3 blocks
// - Parent chain: Bitcoin (SHA256d)
//
// The AuxPoW proof contains:
// 1. The parent (Bitcoin) coinbase transaction
// 2. The merkle branch from coinbase to parent merkle root
// 3. The merkle branch from aux block hash to aux merkle root
// 4. The parent block header (which contains the PoW)
//
// Reference: https://docs.fractalbitcoin.io/node-operation/mining
// ═══════════════════════════════════════════════════════════════════════════════

// SupportsAuxPow returns whether the coin supports auxiliary proof of work (merged mining).
// Fractal Bitcoin enabled AuxPoW from genesis for merged mining with Bitcoin.
func (c *FractalBTCCoin) SupportsAuxPow() bool {
	return true
}

// AuxPowStartHeight returns the block height at which AuxPoW was enabled.
// Fractal Bitcoin supports AuxPoW from genesis (block 0).
func (c *FractalBTCCoin) AuxPowStartHeight() uint64 {
	return FractalBTCAuxPowStartHeight
}

// ChainID returns Fractal Bitcoin's unique chain identifier for the aux merkle tree.
// CONSENSUS-CRITICAL: This must be exactly 0x2024 (8228) to match Fractal Bitcoin Core.
func (c *FractalBTCCoin) ChainID() int32 {
	return FractalBTCChainID
}

// AuxPowVersionBit returns the version bit that indicates an AuxPoW block.
// CONSENSUS-CRITICAL: Blocks using AuxPoW must have this bit set in their version.
func (c *FractalBTCCoin) AuxPowVersionBit() uint32 {
	return FractalBTCAuxPowVersionBit
}

// UseCreateAuxBlock returns true because Fractal Bitcoin uses createauxblock(address)
// instead of the older getauxblock RPC for fetching aux block templates.
// Reference: https://docs.fractalbitcoin.io/node-operation/mining/how-to-mine
func (c *FractalBTCCoin) UseCreateAuxBlock() bool {
	return true
}

// ParseAuxBlockResponse parses the createauxblock RPC response from Fractal Bitcoin Core.
//
// Fractal Bitcoin's createauxblock returns (same format as Namecoin):
//
//	{
//	  "hash": "block header hash (hex, big-endian display order)",
//	  "chainid": 8228,
//	  "previousblockhash": "previous block hash (hex)",
//	  "coinbasevalue": 2500000000,
//	  "bits": "1b3cc366",
//	  "height": 12345678,
//	  "target": "000000000003c366..." (optional, full target hex)
//	}
//
// CONSENSUS-CRITICAL: Hash byte order conversion must be exact.
func (c *FractalBTCCoin) ParseAuxBlockResponse(response map[string]interface{}) (*AuxBlock, error) {
	auxBlock := &AuxBlock{
		ChainID: FractalBTCChainID,
	}

	// Parse hash (convert from display order to internal order)
	hashHex, ok := response["hash"].(string)
	if !ok || len(hashHex) != 64 {
		return nil, fmt.Errorf("invalid or missing aux block hash")
	}
	hash, err := hex.DecodeString(hashHex)
	if err != nil {
		return nil, fmt.Errorf("invalid aux block hash hex: %w", err)
	}
	// Reverse from display (big-endian) to internal (little-endian)
	auxBlock.Hash = fractalBTCReverseBytes(hash)

	// Parse chain ID (verify it matches expected)
	if chainID, ok := response["chainid"].(float64); ok {
		if int32(chainID) != FractalBTCChainID {
			return nil, fmt.Errorf("chain ID mismatch: got %d, expected %d", int32(chainID), FractalBTCChainID)
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

	// Parse height
	if height, ok := response["height"].(float64); ok {
		auxBlock.Height = uint64(height)
	}

	// Parse coinbase value (block reward in satoshis)
	// Fractal Bitcoin: 25 FB per block = 2,500,000,000 satoshis
	if value, ok := response["coinbasevalue"].(float64); ok {
		auxBlock.CoinbaseValue = int64(value)
	}

	// Parse previous block hash
	if prevHashHex, ok := response["previousblockhash"].(string); ok && len(prevHashHex) == 64 {
		prevHash, err := hex.DecodeString(prevHashHex)
		if err == nil {
			auxBlock.PreviousBlockHash = fractalBTCReverseBytes(prevHash)
		}
	}

	// For single aux chain, chain index is always 0
	// For multiple chains, this would be calculated from chain ID and tree size
	auxBlock.ChainIndex = 0

	return auxBlock, nil
}

// SerializeAuxPowProof serializes the complete AuxPoW proof for block submission.
//
// CONSENSUS-CRITICAL: The exact serialization format must match Fractal Bitcoin Core.
// Any deviation will cause the aux block to be rejected by the network.
//
// AuxPoW serialization format (same as Namecoin):
//  1. Parent coinbase transaction (variable length, full serialized tx)
//  2. Parent block hash (32 bytes, little-endian) - hash of parent block header
//  3. Coinbase merkle branch:
//     a. Number of hashes (varint)
//     b. Hash array (32 bytes each, little-endian)
//     c. Branch side mask (uint32 LE) - bit i indicates if hash i is on left (0) or right (1)
//  4. Aux merkle branch:
//     a. Number of hashes (varint)
//     b. Hash array (32 bytes each, little-endian)
//     c. Branch side mask (uint32 LE) - encodes path to aux block in tree
//  5. Parent block header (80 bytes)
func (c *FractalBTCCoin) SerializeAuxPowProof(proof *AuxPowProof) ([]byte, error) {
	if proof == nil {
		return nil, fmt.Errorf("nil proof")
	}

	buf := new(bytes.Buffer)

	// 1. Parent coinbase transaction (full tx, not just hash)
	if len(proof.ParentCoinbase) == 0 {
		return nil, fmt.Errorf("empty parent coinbase")
	}
	buf.Write(proof.ParentCoinbase)

	// 2. Parent block hash (32 bytes, little-endian)
	// CRITICAL: This field is required by Fractal Bitcoin Core's CAuxPow deserializer.
	// The hash is already in internal (little-endian) byte order.
	if len(proof.ParentHash) != 32 {
		return nil, fmt.Errorf("invalid parent hash length: got %d, expected 32", len(proof.ParentHash))
	}
	buf.Write(proof.ParentHash)

	// 3. Coinbase merkle branch
	// Number of branch hashes (varint)
	buf.Write(fractalBTCEncodeVarInt(uint64(len(proof.CoinbaseMerkleBranch))))
	// Branch hashes (from coinbase to merkle root)
	for i, hash := range proof.CoinbaseMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid coinbase branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	// Branch side mask (uint32, little-endian)
	// For coinbase at index 0, this encodes the path through the tree
	// Bit i = 0 means hash i is on the right, bit i = 1 means hash i is on the left
	binary.Write(buf, binary.LittleEndian, uint32(proof.CoinbaseMerkleIndex))

	// 4. Aux merkle branch
	// Number of branch hashes (varint)
	buf.Write(fractalBTCEncodeVarInt(uint64(len(proof.AuxMerkleBranch))))
	// Branch hashes
	for i, hash := range proof.AuxMerkleBranch {
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid aux branch hash length at %d: got %d, expected 32", i, len(hash))
		}
		buf.Write(hash)
	}
	// Branch side mask (encodes path through aux tree)
	binary.Write(buf, binary.LittleEndian, uint32(proof.AuxMerkleIndex))

	// 5. Parent block header (80 bytes)
	if len(proof.ParentHeader) != 80 {
		return nil, fmt.Errorf("invalid parent header length: got %d, expected 80", len(proof.ParentHeader))
	}
	buf.Write(proof.ParentHeader)

	return buf.Bytes(), nil
}

// fractalBTCDoubleSHA256 computes double SHA256 hash.
func fractalBTCDoubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// fractalBTCReverseBytes returns a new slice with bytes in reverse order.
// Does not modify the input slice.
func fractalBTCReverseBytes(b []byte) []byte {
	result := make([]byte, len(b))
	for i, j := 0, len(b)-1; i <= j; i, j = i+1, j-1 {
		result[i], result[j] = b[j], b[i]
	}
	return result
}

// fractalBTCEncodeVarInt encodes an integer as a Bitcoin-style variable length integer.
func fractalBTCEncodeVarInt(n uint64) []byte {
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

// init registers Fractal Bitcoin in the coin registry.
func init() {
	Register("FBTC", func() Coin { return NewFractalBTCCoin() })
	Register("FB", func() Coin { return NewFractalBTCCoin() })
	Register("FRACTALBTC", func() Coin { return NewFractalBTCCoin() })
	Register("FRACTAL", func() Coin { return NewFractalBTCCoin() })
}
