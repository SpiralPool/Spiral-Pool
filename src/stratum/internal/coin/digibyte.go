// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// DigiByteP2PKHVersion is the version byte for mainnet P2PKH addresses (D prefix).
const DigiByteP2PKHVersion byte = 0x1e // 30 decimal

// DigiByteP2SHVersion is the version byte for mainnet P2SH addresses (S prefix).
const DigiByteP2SHVersion byte = 0x3f // 63 decimal

// DigiByteRegtestP2PKHVersion is the version byte for regtest P2PKH addresses (s prefix).
const DigiByteRegtestP2PKHVersion byte = 0x7e // 126 decimal

// DigiByteRegtestP2SHVersion is the version byte for regtest P2SH addresses.
const DigiByteRegtestP2SHVersion byte = 0xc4 // 196 decimal

// DigiByteTestnetP2SHVersion is the version byte for testnet P2SH addresses (9 prefix).
const DigiByteTestnetP2SHVersion byte = 0x8c // 140 decimal

// DigiByteBech32HRP is the human-readable part for DigiByte bech32 addresses.
const DigiByteBech32HRP = "dgb"

// DigiByteGenesisBlockHash is the genesis block hash for chain verification.
// Hash: 7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496
// Date: January 10, 2014
const DigiByteGenesisBlockHash = "7497ea1b465eb39f1c8f507bc877078fe016d6fcb6dfad3a64c98dcc6e1e8496"

// DigiByteTestnetGenesisBlockHash is the genesis block hash for testnet chain verification.
const DigiByteTestnetGenesisBlockHash = "308ea0711d5763be2995670dd9ca9872753561285a84da1d58be58acaa822252"

// DigiByteCoin implements the Coin interface for DigiByte.
type DigiByteCoin struct{}

// NewDigiByteCoin creates a new DigiByte coin instance.
func NewDigiByteCoin() Coin {
	return &DigiByteCoin{}
}

// Symbol returns the ticker symbol.
func (c *DigiByteCoin) Symbol() string {
	return "DGB"
}

// Name returns the full coin name.
func (c *DigiByteCoin) Name() string {
	return "DigiByte"
}

// ValidateAddress checks if an address is valid for DigiByte.
func (c *DigiByteCoin) ValidateAddress(address string) error {
	_, _, err := c.DecodeAddress(address)
	return err
}

// DecodeAddress extracts the pubkey hash and address type from a DigiByte address.
func (c *DigiByteCoin) DecodeAddress(address string) ([]byte, AddressType, error) {
	if len(address) == 0 {
		return nil, AddressTypeUnknown, fmt.Errorf("empty address")
	}

	// Check for bech32/bech32m address (dgb1... mainnet, dgbt1... testnet, dgbrt1... regtest)
	addrLower := strings.ToLower(address)
	hrp := DigiByteBech32HRP
	if strings.HasPrefix(addrLower, "dgbrt1") {
		hrp = "dgbrt" // Regtest bech32 prefix
	} else if strings.HasPrefix(addrLower, "dgbt1") {
		hrp = "dgbt" // Testnet bech32 prefix
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
		pubkeyHash := decoded[1:]

		// Determine type based on witness version and program length
		switch witnessVersion {
		case 0: // SegWit v0: P2WPKH (20 bytes) or P2WSH (32 bytes)
			if len(pubkeyHash) == 20 {
				return pubkeyHash, AddressTypeP2WPKH, nil
			} else if len(pubkeyHash) == 32 {
				return pubkeyHash, AddressTypeP2WSH, nil
			}
			return nil, AddressTypeUnknown, fmt.Errorf("invalid v0 witness program length: %d", len(pubkeyHash))
		case 1: // SegWit v1 (Taproot): P2TR (32 bytes) - future support for DigiByte
			if len(pubkeyHash) == 32 {
				return pubkeyHash, AddressTypeP2TR, nil
			}
			return nil, AddressTypeUnknown, fmt.Errorf("invalid v1 witness program length: %d (expected 32)", len(pubkeyHash))
		default:
			return nil, AddressTypeUnknown, fmt.Errorf("unsupported witness version: %d", witnessVersion)
		}
	}

	// Check for legacy address (D... or S...)
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
	pubkeyHash := decoded[1:21]

	switch version {
	case DigiByteP2PKHVersion, DigiByteRegtestP2PKHVersion:
		return pubkeyHash, AddressTypeP2PKH, nil
	case DigiByteP2SHVersion, DigiByteRegtestP2SHVersion, DigiByteTestnetP2SHVersion:
		return pubkeyHash, AddressTypeP2SH, nil
	default:
		return nil, AddressTypeUnknown, fmt.Errorf("unsupported version byte: 0x%02x", version)
	}
}

// BuildCoinbaseScript creates the output script for the coinbase transaction.
func (c *DigiByteCoin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {
	pubkeyHash, addrType, err := c.DecodeAddress(params.PoolAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid pool address: %w", err)
	}

	switch addrType {
	case AddressTypeP2PKH:
		// OP_DUP OP_HASH160 <20-byte hash> OP_EQUALVERIFY OP_CHECKSIG
		script := []byte{0x76, 0xa9, 0x14}
		script = append(script, pubkeyHash...)
		script = append(script, 0x88, 0xac)
		return script, nil

	case AddressTypeP2SH:
		// OP_HASH160 <20-byte hash> OP_EQUAL
		script := []byte{0xa9, 0x14}
		script = append(script, pubkeyHash...)
		script = append(script, 0x87)
		return script, nil

	case AddressTypeP2WPKH:
		// OP_0 <20-byte hash>
		script := []byte{0x00, 0x14}
		script = append(script, pubkeyHash...)
		return script, nil

	case AddressTypeP2WSH:
		// OP_0 <32-byte hash>
		script := []byte{0x00, 0x20}
		script = append(script, pubkeyHash...)
		return script, nil

	case AddressTypeP2TR:
		// OP_1 <32-byte hash> (Taproot/SegWit v1)
		// P2TR uses witness version 1 (OP_1 = 0x51) and 32-byte x-only pubkey
		script := []byte{0x51, 0x20}
		script = append(script, pubkeyHash...)
		return script, nil

	default:
		return nil, fmt.Errorf("unsupported address type: %s", addrType)
	}
}

// SerializeBlockHeader serializes a block header to 80 bytes.
func (c *DigiByteCoin) SerializeBlockHeader(header *BlockHeader) []byte {
	buf := make([]byte, 80)

	// Version (4 bytes, little-endian)
	binary.LittleEndian.PutUint32(buf[0:4], header.Version)

	// Previous block hash (32 bytes, already little-endian)
	copy(buf[4:36], header.PreviousBlockHash)

	// Merkle root (32 bytes, already little-endian)
	copy(buf[36:68], header.MerkleRoot)

	// Timestamp (4 bytes, little-endian)
	binary.LittleEndian.PutUint32(buf[68:72], header.Timestamp)

	// Bits (4 bytes, little-endian)
	binary.LittleEndian.PutUint32(buf[72:76], header.Bits)

	// Nonce (4 bytes, little-endian)
	binary.LittleEndian.PutUint32(buf[76:80], header.Nonce)

	return buf
}

// HashBlockHeader computes the block hash using SHA256d.
func (c *DigiByteCoin) HashBlockHeader(serialized []byte) []byte {
	first := sha256.Sum256(serialized)
	second := sha256.Sum256(first[:])
	return second[:]
}

// TargetFromBits converts compact target (bits) to full target.
//
// The compact format (nBits) is a 32-bit encoding where:
//   - Bits 24-31: Exponent (number of bytes in the full target)
//   - Bits 0-22:  Mantissa (significand)
//   - Bit 23:     Sign bit (negative targets are invalid in DigiByte)
//
// Per Bitcoin Core behavior: if the sign bit is set, the target is treated as zero
// to prevent invalid (negative) difficulty targets from being used.
func (c *DigiByteCoin) TargetFromBits(bits uint32) *big.Int {
	exponent := bits >> 24
	mantissa := bits & 0x007FFFFF

	// SECURITY: Negative targets are invalid in DigiByte consensus rules.
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

// DifficultyFromTarget calculates human-readable difficulty from target.
// Note: This is for display purposes only; share validation uses exact big.Int comparison.
func (c *DigiByteCoin) DifficultyFromTarget(target *big.Int) float64 {
	if target.Sign() == 0 {
		return 0
	}

	// Diff1 target for SHA256d coins
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	// Difficulty = diff1 / target
	diff1Float := new(big.Float).SetInt(diff1Target)
	targetFloat := new(big.Float).SetInt(target)

	diffFloat := new(big.Float).Quo(diff1Float, targetFloat)
	result, accuracy := diffFloat.Float64()

	// For extremely high difficulties that exceed float64 range, return best approximation
	// This only affects display; actual validation uses big.Int target comparison
	if accuracy == big.Below {
		return result
	}

	return result
}

// ShareDifficultyMultiplier returns the difficulty multiplier for shares.
func (c *DigiByteCoin) ShareDifficultyMultiplier() float64 {
	// DigiByte uses the same difficulty calculation as Bitcoin
	return 1.0
}

// DefaultRPCPort returns the default RPC port.
func (c *DigiByteCoin) DefaultRPCPort() int {
	return 14022
}

// DefaultP2PPort returns the default P2P port.
func (c *DigiByteCoin) DefaultP2PPort() int {
	return 12024
}

// P2PKHVersionByte returns the P2PKH version byte.
func (c *DigiByteCoin) P2PKHVersionByte() byte {
	return DigiByteP2PKHVersion
}

// P2SHVersionByte returns the P2SH version byte.
func (c *DigiByteCoin) P2SHVersionByte() byte {
	return DigiByteP2SHVersion
}

// Bech32HRP returns the bech32 human-readable part.
func (c *DigiByteCoin) Bech32HRP() string {
	return DigiByteBech32HRP
}

// Algorithm returns the mining algorithm.
func (c *DigiByteCoin) Algorithm() string {
	return "sha256d"
}

// SupportsSegWit returns whether the coin supports SegWit.
func (c *DigiByteCoin) SupportsSegWit() bool {
	return true
}

// BlockTime returns the target block time in seconds.
func (c *DigiByteCoin) BlockTime() int {
	return 15 // DigiByte has 15-second blocks
}

// MinCoinbaseScriptLen returns the minimum coinbase script length.
func (c *DigiByteCoin) MinCoinbaseScriptLen() int {
	return 2 // BIP34 requires at least 2 bytes (height encoding)
}

// CoinbaseMaturity returns the number of confirmations before coinbase is spendable.
func (c *DigiByteCoin) CoinbaseMaturity() int {
	return 100 // DigiByte requires 100 confirmations
}

// GenesisBlockHash returns the expected genesis block hash for chain verification.
func (c *DigiByteCoin) GenesisBlockHash() string {
	return DigiByteGenesisBlockHash
}

// VerifyGenesisBlock checks if the provided hash matches the expected genesis block.
func (c *DigiByteCoin) VerifyGenesisBlock(nodeGenesisHash string) error {
	if strings.ToLower(nodeGenesisHash) != strings.ToLower(DigiByteGenesisBlockHash) {
		return fmt.Errorf("DGB genesis block mismatch: got %s, expected %s - "+
			"verify your node is running DigiByte Core",
			nodeGenesisHash, DigiByteGenesisBlockHash)
	}
	return nil
}

// MultiAlgoGBTParam returns the algorithm parameter for getblocktemplate.
// DigiByte requires this to get the correct SHA256d difficulty target.
func (c *DigiByteCoin) MultiAlgoGBTParam() string {
	return "sha256d"
}

// Register DigiByte on package init
func init() {
	Register("DGB", NewDigiByteCoin)
	Register("DIGIBYTE", NewDigiByteCoin) // Alias
}

// Helper functions

// doubleSHA256 performs SHA256(SHA256(data)).
func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// base58Alphabet is the Base58 character set.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Decode decodes a Base58 string.
func base58Decode(s string) ([]byte, error) {
	alphabetMap := make(map[rune]int64)
	for i, c := range base58Alphabet {
		alphabetMap[c] = int64(i)
	}

	result := big.NewInt(0)
	for _, c := range s {
		val, ok := alphabetMap[c]
		if !ok {
			return nil, fmt.Errorf("invalid character: %c", c)
		}
		result.Mul(result, big.NewInt(58))
		result.Add(result, big.NewInt(val))
	}

	decoded := result.Bytes()

	// Count leading zeros
	leadingZeros := 0
	for _, c := range s {
		if c != '1' {
			break
		}
		leadingZeros++
	}

	if leadingZeros > 0 {
		padding := make([]byte, leadingZeros)
		decoded = append(padding, decoded...)
	}

	return decoded, nil
}

// Bech32 checksum constants per BIP-173 and BIP-350
const (
	bech32Const  = 1          // Bech32 (BIP-173) for witness v0
	bech32mConst = 0x2bc830a3 // Bech32m (BIP-350) for witness v1+
)

// bech32Polymod computes the Bech32 checksum polynomial.
// This implements the BCH code specified in BIP-173.
func bech32Polymod(values []int) int {
	generator := []int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := 1
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ v
		for i := 0; i < 5; i++ {
			if (top>>uint(i))&1 == 1 {
				chk ^= generator[i]
			}
		}
	}
	return chk
}

// bech32HRPExpand expands the HRP for checksum computation per BIP-173.
func bech32HRPExpand(hrp string) []int {
	result := make([]int, len(hrp)*2+1)
	for i, c := range hrp {
		result[i] = int(c) >> 5
	}
	result[len(hrp)] = 0
	for i, c := range hrp {
		result[len(hrp)+1+i] = int(c) & 31
	}
	return result
}

// bech32VerifyChecksum verifies the Bech32/Bech32m checksum.
// Returns the expected constant (bech32Const or bech32mConst) if valid, 0 if invalid.
func bech32VerifyChecksum(hrp string, data []int) int {
	hrpExpanded := bech32HRPExpand(hrp)
	combined := append(hrpExpanded, data...)
	polymod := bech32Polymod(combined)

	// Check against both Bech32 and Bech32m constants
	if polymod == bech32Const {
		return bech32Const
	}
	if polymod == bech32mConst {
		return bech32mConst
	}
	return 0
}

// decodeBech32Address decodes a bech32/bech32m address with full checksum verification.
// Implements BIP-173 (Bech32 for witness v0) and BIP-350 (Bech32m for witness v1+).
func decodeBech32Address(address, expectedHRP string) ([]byte, error) {
	// BIP-173: Bech32 addresses can be all lowercase OR all uppercase, but not mixed
	// Convert to lowercase for processing (canonical form)
	address = strings.ToLower(address)

	// Find separator (last occurrence of '1')
	pos := -1
	for i := len(address) - 1; i >= 0; i-- {
		if address[i] == '1' {
			pos = i
			break
		}
	}
	if pos < 1 {
		return nil, fmt.Errorf("invalid bech32: no separator")
	}

	hrp := address[:pos]
	if hrp != strings.ToLower(expectedHRP) {
		return nil, fmt.Errorf("invalid HRP: got %s, expected %s", hrp, expectedHRP)
	}

	// Decode data part (after separator)
	data := address[pos+1:]
	if len(data) < 6 {
		return nil, fmt.Errorf("bech32 data too short")
	}

	// Bech32 character set per BIP-173 (lowercase)
	charset := "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	charsetMap := make(map[rune]int)
	for i, c := range charset {
		charsetMap[c] = i
	}

	// Decode to 5-bit values
	values := make([]int, len(data))
	for i, c := range data {
		val, ok := charsetMap[c]
		if !ok {
			return nil, fmt.Errorf("invalid bech32 character: %c", c)
		}
		values[i] = val
	}

	// SECURITY: Full polymod checksum verification per BIP-173/BIP-350
	checksumType := bech32VerifyChecksum(hrp, values)
	if checksumType == 0 {
		return nil, fmt.Errorf("invalid bech32 checksum")
	}

	if len(values) < 7 {
		return nil, fmt.Errorf("bech32 data too short for witness program")
	}

	// First value is witness version
	witnessVersion := values[0]
	if witnessVersion > 16 {
		return nil, fmt.Errorf("invalid witness version: %d", witnessVersion)
	}

	// SECURITY: Verify correct encoding variant per BIP-350
	// Witness version 0 must use Bech32 (BIP-173)
	// Witness version 1+ must use Bech32m (BIP-350)
	if witnessVersion == 0 && checksumType != bech32Const {
		return nil, fmt.Errorf("witness v0 must use bech32 encoding, not bech32m")
	}
	if witnessVersion > 0 && checksumType != bech32mConst {
		return nil, fmt.Errorf("witness v%d must use bech32m encoding, not bech32", witnessVersion)
	}

	// Convert 5-bit to 8-bit (exclude version and checksum)
	data5bit := values[1 : len(values)-6]

	// Convert from 5-bit to 8-bit
	result, err := convertBits(data5bit, 5, 8, false)
	if err != nil {
		return nil, fmt.Errorf("bit conversion failed: %w", err)
	}

	// Prepend witness version to the result so caller can identify the type
	// Result format: [witnessVersion, ...programBytes]
	withVersion := make([]byte, len(result)+1)
	withVersion[0] = byte(witnessVersion)
	copy(withVersion[1:], result)

	return withVersion, nil
}

// convertBits converts a byte array between bit sizes.
func convertBits(data []int, fromBits, toBits int, pad bool) ([]byte, error) {
	acc := 0
	bits := 0
	var result []byte
	maxv := (1 << toBits) - 1

	for _, value := range data {
		if value < 0 || value>>fromBits != 0 {
			return nil, fmt.Errorf("invalid value: %d", value)
		}
		acc = (acc << fromBits) | value
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
	} else if bits >= fromBits || (acc<<(toBits-bits))&maxv != 0 {
		return nil, fmt.Errorf("invalid padding")
	}

	return result, nil
}
