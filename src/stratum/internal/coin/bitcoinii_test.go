// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// =============================================================================
// BITCOIN II COIN TESTS
// =============================================================================

func TestBitcoinIISymbol(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.Symbol() != "BC2" {
		t.Errorf("Symbol() = %s, want BC2", coin.Symbol())
	}
}

func TestBitcoinIIName(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.Name() != "Bitcoin II" {
		t.Errorf("Name() = %s, want Bitcoin II", coin.Name())
	}
}

func TestBitcoinIIAlgorithm(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.Algorithm() != "sha256d" {
		t.Errorf("Algorithm() = %s, want sha256d", coin.Algorithm())
	}
}

func TestBitcoinIISupportsSegWit(t *testing.T) {
	coin := NewBitcoinIICoin()
	if !coin.SupportsSegWit() {
		t.Error("SupportsSegWit() = false, want true")
	}
}

func TestBitcoinIIBlockTime(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.BlockTime() != 600 {
		t.Errorf("BlockTime() = %d, want 600", coin.BlockTime())
	}
}

func TestBitcoinIIDefaultPorts(t *testing.T) {
	coin := NewBitcoinIICoin()

	if coin.DefaultRPCPort() != 8339 {
		t.Errorf("DefaultRPCPort() = %d, want 8339", coin.DefaultRPCPort())
	}

	if coin.DefaultP2PPort() != 8338 {
		t.Errorf("DefaultP2PPort() = %d, want 8338", coin.DefaultP2PPort())
	}
}

func TestBitcoinIIVersionBytes(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Bitcoin II uses same version bytes as Bitcoin
	if coin.P2PKHVersionByte() != 0x00 {
		t.Errorf("P2PKHVersionByte() = 0x%02x, want 0x00", coin.P2PKHVersionByte())
	}

	if coin.P2SHVersionByte() != 0x05 {
		t.Errorf("P2SHVersionByte() = 0x%02x, want 0x05", coin.P2SHVersionByte())
	}
}

func TestBitcoinIIBech32HRP(t *testing.T) {
	coin := NewBitcoinIICoin()
	// Bitcoin II uses same HRP as Bitcoin
	if coin.Bech32HRP() != "bc" {
		t.Errorf("Bech32HRP() = %s, want bc", coin.Bech32HRP())
	}
}

func TestBitcoinIICoinbaseMaturity(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.CoinbaseMaturity() != 100 {
		t.Errorf("CoinbaseMaturity() = %d, want 100", coin.CoinbaseMaturity())
	}
}

func TestBitcoinIIMinCoinbaseScriptLen(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.MinCoinbaseScriptLen() != 2 {
		t.Errorf("MinCoinbaseScriptLen() = %d, want 2", coin.MinCoinbaseScriptLen())
	}
}

func TestBitcoinIIShareDifficultyMultiplier(t *testing.T) {
	coin := NewBitcoinIICoin()
	if coin.ShareDifficultyMultiplier() != 1.0 {
		t.Errorf("ShareDifficultyMultiplier() = %f, want 1.0", coin.ShareDifficultyMultiplier())
	}
}

// =============================================================================
// ADDRESS VALIDATION TESTS
// =============================================================================

func TestBitcoinIIAddressValidation(t *testing.T) {
	coin := NewBitcoinIICoin()

	tests := []struct {
		name     string
		address  string
		valid    bool
		addrType AddressType
	}{
		// Valid P2PKH addresses (start with '1')
		{
			name:     "valid P2PKH",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		{
			name:     "valid P2PKH genesis address",
			address:  "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},

		// Valid P2SH addresses (start with '3')
		{
			name:     "valid P2SH",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			valid:    true,
			addrType: AddressTypeP2SH,
		},

		// Valid P2WPKH addresses (bc1q...)
		{
			name:     "valid P2WPKH",
			address:  "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			valid:    true,
			addrType: AddressTypeP2WPKH,
		},
		{
			name:     "valid P2WPKH uppercase",
			address:  "BC1QW508D6QEJXTDG4Y5R3ZARVARY0C5XW7KV8F3T4",
			valid:    true,
			addrType: AddressTypeP2WPKH,
		},

		// Valid P2WSH addresses (bc1q... with 32-byte program)
		{
			name:     "valid P2WSH",
			address:  "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3",
			valid:    true,
			addrType: AddressTypeP2WSH,
		},

		// Valid P2TR addresses (bc1p...)
		{
			name:     "valid P2TR",
			address:  "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			valid:    true,
			addrType: AddressTypeP2TR,
		},

		// Invalid addresses
		{
			name:     "empty address",
			address:  "",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "invalid checksum",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN3",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "too short",
			address:  "1Bv",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "invalid character",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVNO",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "invalid bech32 checksum",
			address:  "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t5",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
		{
			name:     "wrong HRP",
			address:  "tb1qw508d6qejxtdg4y5r3zarvary0c5xw7kxpjzsx",
			valid:    false,
			addrType: AddressTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := coin.ValidateAddress(tt.address)
			if tt.valid && err != nil {
				t.Errorf("ValidateAddress(%s) error = %v, want nil", tt.address, err)
			}
			if !tt.valid && err == nil {
				t.Errorf("ValidateAddress(%s) error = nil, want error", tt.address)
			}

			if tt.valid {
				_, addrType, err := coin.DecodeAddress(tt.address)
				if err != nil {
					t.Errorf("DecodeAddress(%s) error = %v", tt.address, err)
				}
				if addrType != tt.addrType {
					t.Errorf("DecodeAddress(%s) type = %v, want %v", tt.address, addrType, tt.addrType)
				}
			}
		})
	}
}

// =============================================================================
// COINBASE SCRIPT TESTS
// =============================================================================

func TestBitcoinIIBuildCoinbaseScript(t *testing.T) {
	coin := NewBitcoinIICoin()

	tests := []struct {
		name           string
		address        string
		expectedPrefix []byte
		expectedLen    int
	}{
		{
			name:           "P2PKH script",
			address:        "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			expectedPrefix: []byte{0x76, 0xa9, 0x14}, // OP_DUP OP_HASH160 PUSH20
			expectedLen:    25,
		},
		{
			name:           "P2SH script",
			address:        "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			expectedPrefix: []byte{0xa9, 0x14}, // OP_HASH160 PUSH20
			expectedLen:    23,
		},
		{
			name:           "P2WPKH script",
			address:        "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			expectedPrefix: []byte{0x00, 0x14}, // OP_0 PUSH20
			expectedLen:    22,
		},
		{
			name:           "P2TR script",
			address:        "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0",
			expectedPrefix: []byte{0x51, 0x20}, // OP_1 PUSH32
			expectedLen:    34,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := CoinbaseParams{
				PoolAddress: tt.address,
			}

			script, err := coin.BuildCoinbaseScript(params)
			if err != nil {
				t.Fatalf("BuildCoinbaseScript() error = %v", err)
			}

			if len(script) != tt.expectedLen {
				t.Errorf("script length = %d, want %d", len(script), tt.expectedLen)
			}

			for i, b := range tt.expectedPrefix {
				if script[i] != b {
					t.Errorf("script[%d] = 0x%02x, want 0x%02x", i, script[i], b)
				}
			}
		})
	}
}

func TestBitcoinIIBuildCoinbaseScriptInvalid(t *testing.T) {
	coin := NewBitcoinIICoin()

	params := CoinbaseParams{
		PoolAddress: "invalid_address",
	}

	_, err := coin.BuildCoinbaseScript(params)
	if err == nil {
		t.Error("BuildCoinbaseScript() with invalid address should return error")
	}
}

// =============================================================================
// BLOCK HEADER TESTS
// =============================================================================

func TestBitcoinIISerializeBlockHeader(t *testing.T) {
	coin := NewBitcoinIICoin()

	header := &BlockHeader{
		Version:           0x20000000,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1734019071,
		Bits:              0x1d00ffff,
		Nonce:             1597163478,
	}

	// Fill with test data
	for i := range header.PreviousBlockHash {
		header.PreviousBlockHash[i] = byte(i)
	}
	for i := range header.MerkleRoot {
		header.MerkleRoot[i] = byte(i + 32)
	}

	serialized := coin.SerializeBlockHeader(header)

	// Should be exactly 80 bytes
	if len(serialized) != 80 {
		t.Errorf("SerializeBlockHeader() length = %d, want 80", len(serialized))
	}

	// Verify version (little-endian)
	if serialized[0] != 0x00 || serialized[1] != 0x00 || serialized[2] != 0x00 || serialized[3] != 0x20 {
		t.Errorf("Version bytes incorrect: %x", serialized[0:4])
	}

	// Verify timestamp position and value
	// Timestamp at bytes 68-71 (little-endian)
	timestamp := uint32(serialized[68]) | uint32(serialized[69])<<8 |
		uint32(serialized[70])<<16 | uint32(serialized[71])<<24
	if timestamp != 1734019071 {
		t.Errorf("Timestamp = %d, want 1734019071", timestamp)
	}
}

func TestBitcoinIIHashBlockHeader(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Test with all-zero header
	header := make([]byte, 80)
	hash := coin.HashBlockHeader(header)

	// Should be 32 bytes
	if len(hash) != 32 {
		t.Errorf("HashBlockHeader() length = %d, want 32", len(hash))
	}

	// Hash of all-zero 80 bytes (double SHA256)
	// This is a known value we can verify
	expectedHex := "4be7570e8f70eb093640c8468274ba759745a7aa2b7d25ab1e0421b259845014"
	actualHex := hex.EncodeToString(hash)

	if actualHex != expectedHex {
		t.Errorf("HashBlockHeader() = %s, want %s", actualHex, expectedHex)
	}
}

// =============================================================================
// DIFFICULTY CALCULATION TESTS
// =============================================================================

func TestBitcoinIITargetFromBits(t *testing.T) {
	coin := NewBitcoinIICoin()

	tests := []struct {
		name   string
		bits   uint32
		expect string // Expected target as hex string
	}{
		{
			name:   "difficulty 1 (genesis)",
			bits:   0x1d00ffff,
			expect: "ffff0000000000000000000000000000000000000000000000000000",
		},
		{
			name:   "high difficulty",
			bits:   0x17034267,
			expect: "342670000000000000000000000000000000000000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := coin.TargetFromBits(tt.bits)

			// Convert to hex for comparison
			targetHex := target.Text(16)

			// Compare (allowing for leading zeros to be stripped)
			expected := new(big.Int)
			expected.SetString(tt.expect, 16)

			if target.Cmp(expected) != 0 {
				t.Errorf("TargetFromBits(0x%08x) = %s, want %s", tt.bits, targetHex, tt.expect)
			}
		})
	}
}

func TestBitcoinIIDifficultyFromTarget(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Test difficulty 1 (bits = 0x1d00ffff)
	diff1Target := new(big.Int)
	diff1Target.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	difficulty := coin.DifficultyFromTarget(diff1Target)

	// Difficulty 1 target should give difficulty ~1.0
	if difficulty < 0.99 || difficulty > 1.01 {
		t.Errorf("DifficultyFromTarget(diff1) = %f, want ~1.0", difficulty)
	}

	// Test zero target
	zeroTarget := big.NewInt(0)
	zeroDiff := coin.DifficultyFromTarget(zeroTarget)
	if zeroDiff != 0 {
		t.Errorf("DifficultyFromTarget(0) = %f, want 0", zeroDiff)
	}
}

func TestBitcoinIIDifficultyRoundtrip(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Starting from bits, convert to target, then to difficulty
	bits := uint32(0x1d00ffff)
	target := coin.TargetFromBits(bits)
	difficulty := coin.DifficultyFromTarget(target)

	// Difficulty 1 bits should give difficulty ~1
	if difficulty < 0.99 || difficulty > 1.01 {
		t.Errorf("Roundtrip difficulty = %f, expected ~1.0", difficulty)
	}
}

// =============================================================================
// REGISTRY TESTS
// =============================================================================

func TestBitcoinIIRegistration(t *testing.T) {
	// Test primary symbol
	if !IsRegistered("BC2") {
		t.Error("BC2 should be registered")
	}

	// Test aliases
	if !IsRegistered("BITCOINII") {
		t.Error("BITCOINII should be registered")
	}
	if !IsRegistered("BITCOIN2") {
		t.Error("BITCOIN2 should be registered")
	}

	// Test case insensitivity
	if !IsRegistered("bc2") {
		t.Error("bc2 (lowercase) should be registered")
	}
}

func TestBitcoinIICreate(t *testing.T) {
	coin, err := Create("BC2")
	if err != nil {
		t.Fatalf("Create(BC2) error = %v", err)
	}

	if coin.Symbol() != "BC2" {
		t.Errorf("Created coin symbol = %s, want BC2", coin.Symbol())
	}
}

func TestBitcoinIIMustCreate(t *testing.T) {
	// Should not panic for registered coin
	coin := MustCreate("BC2")
	if coin.Symbol() != "BC2" {
		t.Errorf("MustCreate(BC2) symbol = %s, want BC2", coin.Symbol())
	}
}

// =============================================================================
// CROSS-COIN COMPATIBILITY TESTS
// =============================================================================

// TestBitcoinIIBitcoinAddressCompatibility verifies that Bitcoin II accepts
// Bitcoin-format addresses (this is by design, as BC2 uses same address formats)
func TestBitcoinIIBitcoinAddressCompatibility(t *testing.T) {
	bc2 := NewBitcoinIICoin()
	btc := NewBitcoinCoin()

	// These addresses are valid for BOTH Bitcoin and Bitcoin II
	// This is intentional but users should be warned about it
	testAddresses := []string{
		"1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",             // P2PKH
		"3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",             // P2SH
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",    // P2WPKH
		"bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0", // P2TR
	}

	for _, addr := range testAddresses {
		// Both should validate the same address
		btcErr := btc.ValidateAddress(addr)
		bc2Err := bc2.ValidateAddress(addr)

		if btcErr != nil {
			t.Errorf("BTC rejected address %s: %v", addr, btcErr)
		}
		if bc2Err != nil {
			t.Errorf("BC2 rejected address %s: %v", addr, bc2Err)
		}

		// Both should decode to same hash
		btcHash, btcType, _ := btc.DecodeAddress(addr)
		bc2Hash, bc2Type, _ := bc2.DecodeAddress(addr)

		if btcType != bc2Type {
			t.Errorf("Address %s: BTC type %v != BC2 type %v", addr, btcType, bc2Type)
		}

		if len(btcHash) != len(bc2Hash) {
			t.Errorf("Address %s: hash length mismatch", addr)
		}

		for i := range btcHash {
			if btcHash[i] != bc2Hash[i] {
				t.Errorf("Address %s: hash byte %d differs", addr, i)
			}
		}
	}

	t.Log("WARNING: Bitcoin and Bitcoin II use identical address formats!")
	t.Log("Users must verify they are using the correct address for the correct chain.")
}

// =============================================================================
// GENESIS BLOCK VERIFICATION TESTS
// =============================================================================

func TestBitcoinIIGenesisBlockHash(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Verify the constant is correctly set
	expectedHash := "0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb"
	if coin.GenesisBlockHash() != expectedHash {
		t.Errorf("GenesisBlockHash() = %s, want %s", coin.GenesisBlockHash(), expectedHash)
	}
}

func TestBitcoinIIVerifyGenesisBlock_Valid(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Valid BC2 genesis hash
	validHash := "0000000028f062b221c1a8a5cf0244b1627315f7aa5b775b931cfec46dc17ceb"
	err := coin.VerifyGenesisBlock(validHash)
	if err != nil {
		t.Errorf("VerifyGenesisBlock() with valid hash returned error: %v", err)
	}

	// Test case insensitivity
	upperHash := "0000000028F062B221C1A8A5CF0244B1627315F7AA5B775B931CFEC46DC17CEB"
	err = coin.VerifyGenesisBlock(upperHash)
	if err != nil {
		t.Errorf("VerifyGenesisBlock() with uppercase hash returned error: %v", err)
	}
}

func TestBitcoinIIVerifyGenesisBlock_Invalid(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Bitcoin mainnet genesis hash (should fail verification)
	bitcoinGenesis := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	err := coin.VerifyGenesisBlock(bitcoinGenesis)
	if err == nil {
		t.Error("VerifyGenesisBlock() should fail for Bitcoin genesis hash")
	}

	// Random invalid hash
	randomHash := "0000000000000000000000000000000000000000000000000000000000000000"
	err = coin.VerifyGenesisBlock(randomHash)
	if err == nil {
		t.Error("VerifyGenesisBlock() should fail for random hash")
	}
}

func TestBitcoinIIVerifyGenesisBlock_ErrorMessage(t *testing.T) {
	coin := NewBitcoinIICoin()

	// Use Bitcoin mainnet genesis to trigger error
	bitcoinGenesis := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	err := coin.VerifyGenesisBlock(bitcoinGenesis)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	// Error message should warn about Bitcoin/BC2 confusion
	errMsg := err.Error()
	if !strings.Contains(errMsg, "CRITICAL") {
		t.Error("Error message should contain 'CRITICAL'")
	}
	if !strings.Contains(errMsg, "Bitcoin mainnet") {
		t.Error("Error message should mention Bitcoin mainnet")
	}
	if !strings.Contains(errMsg, "8339") {
		t.Error("Error message should mention correct BC2 RPC port (8339)")
	}
}

// =============================================================================
// REGTEST bcrt1 ADDRESS SUPPORT (Bug Fix Regression Tests)
// =============================================================================
// These tests verify that bcrt1 (regtest bech32) addresses are correctly
// decoded by the Bitcoin II coin implementation. Without the fix, DecodeAddress
// would reject bcrt1 addresses because the HRP "bcrt" doesn't match "bc".

func TestBitcoinIIRegtestBcrt1Address(t *testing.T) {
	c := NewBitcoinIICoin()

	err := c.ValidateAddress("bcrt1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqdku202")
	if err != nil {
		t.Fatalf("ValidateAddress(bcrt1q...) should succeed for regtest address, got: %v", err)
	}
}

func TestBitcoinIIRegtestBcrt1Decode(t *testing.T) {
	c := NewBitcoinIICoin()

	hash, addrType, err := c.DecodeAddress("bcrt1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqdku202")
	if err != nil {
		t.Fatalf("DecodeAddress(bcrt1q...) error: %v", err)
	}

	if addrType != AddressTypeP2WPKH {
		t.Errorf("expected AddressTypeP2WPKH, got %v", addrType)
	}

	if len(hash) != 20 {
		t.Errorf("expected 20-byte hash for P2WPKH, got %d bytes", len(hash))
	}
}

func TestBitcoinIIRegtestBcrt1CoinbaseScript(t *testing.T) {
	c := NewBitcoinIICoin()

	params := CoinbaseParams{
		PoolAddress: "bcrt1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqdku202",
	}

	script, err := c.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("BuildCoinbaseScript(bcrt1q...) error: %v", err)
	}

	if len(script) != 22 {
		t.Errorf("expected 22-byte P2WPKH script, got %d", len(script))
	}

	if script[0] != 0x00 {
		t.Errorf("expected OP_0 (0x00), got 0x%02x", script[0])
	}
	if script[1] != 0x14 {
		t.Errorf("expected PUSH_20 (0x14), got 0x%02x", script[1])
	}
}

func TestBitcoinIIRegtestBcrt1BothFormats(t *testing.T) {
	c := NewBitcoinIICoin()

	// Mainnet bc1q should still work
	err := c.ValidateAddress("bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4")
	if err != nil {
		t.Errorf("mainnet bc1q address should still be valid: %v", err)
	}

	// Regtest bcrt1q should also work (the fix)
	err = c.ValidateAddress("bcrt1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqdku202")
	if err != nil {
		t.Errorf("regtest bcrt1q address should be valid: %v", err)
	}
}

// =============================================================================
// BENCHMARK TESTS
// =============================================================================

func BenchmarkBitcoinIIHashBlockHeader(b *testing.B) {
	coin := NewBitcoinIICoin()
	header := make([]byte, 80)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.HashBlockHeader(header)
	}
}

func BenchmarkBitcoinIIDecodeAddress(b *testing.B) {
	coin := NewBitcoinIICoin()
	address := "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.DecodeAddress(address)
	}
}

func BenchmarkBitcoinIITargetFromBits(b *testing.B) {
	coin := NewBitcoinIICoin()
	bits := uint32(0x1d00ffff)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coin.TargetFromBits(bits)
	}
}
