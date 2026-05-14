// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// encodeCashAddrForTest generates a valid CashAddr string using the package's
// internal polymod and bit-conversion functions. This lets tests produce
// known-valid ecash: addresses without guessing external checksums.
//
// isP2SH=false → P2PKH (version byte 0x00, q-prefix)
// isP2SH=true  → P2SH  (version byte 0x08, p-prefix)
func encodeCashAddrForTest(tb testing.TB, prefix string, hash []byte, isP2SH bool) string {
	tb.Helper()

	versionByte := byte(0x00) // P2PKH: type bits 0x00, hash size 0x00 (20 bytes)
	if isP2SH {
		versionByte = 0x08 // P2SH: type bits 0x01<<3, hash size 0x00 (20 bytes)
	}

	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], hash)

	data5, err := bchConvertBits(payload, 8, 5, true)
	if err != nil {
		tb.Fatalf("encodeCashAddrForTest: bchConvertBits 8→5: %v", err)
	}

	// Build checksum preimage: prefix_expand + data5 + 8 zero placeholder values
	prefixExpanded := cashAddrPrefixExpand(prefix)
	preimage := make([]uint64, len(prefixExpanded)+len(data5)+8)
	copy(preimage, prefixExpanded)
	for i, b := range data5 {
		preimage[len(prefixExpanded)+i] = uint64(b)
	}
	// Positions [len(prefixExpanded)+len(data5) : +8] remain zero — checksum placeholder

	mod := cashAddrPolymod(preimage)

	// Encode data5 + checksum (8 5-bit groups, MSB first) as charset characters
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	buf := make([]byte, 0, len(data5)+8)
	for _, b := range data5 {
		buf = append(buf, charset[b])
	}
	for i := 7; i >= 0; i-- {
		buf = append(buf, charset[(mod>>(uint(i)*5))&0x1f])
	}
	return prefix + ":" + string(buf)
}

// testECashHash is a 20-byte hash used as a fixture across address tests.
var testECashHash = []byte{
	0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
	0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00,
	0x12, 0x34, 0x56, 0x78,
}

// ───────────────────────────────────────────────────────────────────────────────
// Basic identity tests
// ───────────────────────────────────────────────────────────────────────────────

func TestECashSymbol(t *testing.T) {
	c := NewECashCoin()
	if c.Symbol() != "XEC" {
		t.Errorf("Symbol: got %q, want %q", c.Symbol(), "XEC")
	}
	if c.Name() != "eCash" {
		t.Errorf("Name: got %q, want %q", c.Name(), "eCash")
	}
}

func TestECashAlgorithm(t *testing.T) {
	c := NewECashCoin()
	if c.Algorithm() != "sha256d" {
		t.Errorf("Algorithm: got %q, want sha256d", c.Algorithm())
	}
	if c.SupportsSegWit() {
		t.Error("SupportsSegWit: got true, want false (XEC has no SegWit)")
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Network parameter tests
// ───────────────────────────────────────────────────────────────────────────────

func TestECashNetworkParams(t *testing.T) {
	c := NewECashCoin()

	if c.DefaultRPCPort() != XECDefaultRPCPort {
		t.Errorf("DefaultRPCPort: got %d, want %d", c.DefaultRPCPort(), XECDefaultRPCPort)
	}
	if c.DefaultP2PPort() != XECDefaultP2PPort {
		t.Errorf("DefaultP2PPort: got %d, want %d", c.DefaultP2PPort(), XECDefaultP2PPort)
	}
	if c.BlockTime() != XECBlockTime {
		t.Errorf("BlockTime: got %d, want %d", c.BlockTime(), XECBlockTime)
	}
	if c.CoinbaseMaturity() != XECCoinbaseMaturity {
		t.Errorf("CoinbaseMaturity: got %d, want %d", c.CoinbaseMaturity(), XECCoinbaseMaturity)
	}
	// XEC uses CashAddr, not bech32 — HRP must be empty
	if c.Bech32HRP() != "" {
		t.Errorf("Bech32HRP: got %q, want empty (XEC uses CashAddr)", c.Bech32HRP())
	}
}

func TestECashVersionBytes(t *testing.T) {
	c := NewECashCoin()
	if c.P2PKHVersionByte() != XECP2PKHVersion {
		t.Errorf("P2PKHVersionByte: got 0x%02x, want 0x%02x", c.P2PKHVersionByte(), XECP2PKHVersion)
	}
	if c.P2SHVersionByte() != XECP2SHVersion {
		t.Errorf("P2SHVersionByte: got 0x%02x, want 0x%02x", c.P2SHVersionByte(), XECP2SHVersion)
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Address validation — CashAddr and legacy Base58Check
// ───────────────────────────────────────────────────────────────────────────────

func TestECashAddressValidation(t *testing.T) {
	c := NewECashCoin()

	// Generate CashAddr test addresses using the package's own polymod functions.
	p2pkhAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, false)
	p2shAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, true)

	tests := []struct {
		name     string
		address  string
		valid    bool
		addrType AddressType
	}{
		{
			name:     "valid CashAddr P2PKH with ecash: prefix",
			address:  p2pkhAddr,
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		{
			name:     "valid CashAddr P2SH with ecash: prefix",
			address:  p2shAddr,
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		{
			name:     "valid legacy P2PKH (1...)",
			address:  "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			valid:    true,
			addrType: AddressTypeP2PKH,
		},
		{
			name:     "valid legacy P2SH (3...)",
			address:  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
			valid:    true,
			addrType: AddressTypeP2SH,
		},
		{
			name:    "empty address",
			address: "",
			valid:   false,
		},
		{
			name:    "too short to be any address type",
			address: "qpm",
			valid:   false,
		},
		{
			name:    "BCH prefix rejected — wrong network checksum",
			address: "bitcoincash:qpm2qsznhks23z7629mms6s4cwef74vcwvy22gdx6a",
			valid:   false,
		},
		{
			name:    "BTC bech32 rejected — XEC has no SegWit",
			address: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
			valid:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.ValidateAddress(tt.address)
			if tt.valid && err != nil {
				t.Errorf("expected valid address, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid address, got no error")
			}
			if tt.valid {
				_, addrType, err := c.DecodeAddress(tt.address)
				if err != nil {
					t.Errorf("DecodeAddress failed: %v", err)
				}
				if addrType != tt.addrType {
					t.Errorf("expected type %v, got %v", tt.addrType, addrType)
				}
			}
		})
	}
}

// TestECashCashAddrRoundTrip verifies encode → decode produces the original hash.
func TestECashCashAddrRoundTrip(t *testing.T) {
	c := NewECashCoin()

	// P2PKH round-trip
	p2pkhAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, false)
	hash, addrType, err := c.DecodeAddress(p2pkhAddr)
	if err != nil {
		t.Fatalf("P2PKH DecodeAddress: %v", err)
	}
	if addrType != AddressTypeP2PKH {
		t.Errorf("P2PKH type: got %v, want P2PKH", addrType)
	}
	if !bytes.Equal(hash, testECashHash) {
		t.Errorf("P2PKH hash mismatch:\n  got:  %x\n  want: %x", hash, testECashHash)
	}

	// P2SH round-trip
	p2shAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, true)
	hash, addrType, err = c.DecodeAddress(p2shAddr)
	if err != nil {
		t.Fatalf("P2SH DecodeAddress: %v", err)
	}
	if addrType != AddressTypeP2SH {
		t.Errorf("P2SH type: got %v, want P2SH", addrType)
	}
	if !bytes.Equal(hash, testECashHash) {
		t.Errorf("P2SH hash mismatch:\n  got:  %x\n  want: %x", hash, testECashHash)
	}
}

// TestECashCashAddrTypes verifies the q-prefix → P2PKH and p-prefix → P2SH mapping.
func TestECashCashAddrTypes(t *testing.T) {
	c := NewECashCoin()

	p2pkh := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, false)
	_, addrType, err := c.DecodeAddress(p2pkh)
	if err != nil {
		t.Fatalf("decode P2PKH: %v", err)
	}
	if addrType != AddressTypeP2PKH {
		t.Errorf("q-prefix address: expected P2PKH, got %v", addrType)
	}

	p2sh := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, true)
	_, addrType, err = c.DecodeAddress(p2sh)
	if err != nil {
		t.Fatalf("decode P2SH: %v", err)
	}
	if addrType != AddressTypeP2SH {
		t.Errorf("p-prefix address: expected P2SH, got %v", addrType)
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Block header
// ───────────────────────────────────────────────────────────────────────────────

func TestECashBlockHeader(t *testing.T) {
	c := NewECashCoin()

	header := &BlockHeader{
		Version:           536870912, // 0x20000000
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1746000000, // May 2025 — within live XEC sync window
		Bits:              0x18034287,
		Nonce:             99999,
	}
	for i := 0; i < 32; i++ {
		header.PreviousBlockHash[i] = byte(i)
		header.MerkleRoot[i] = byte(31 - i)
	}

	serialized := c.SerializeBlockHeader(header)
	if len(serialized) != 80 {
		t.Errorf("SerializeBlockHeader: got %d bytes, want 80", len(serialized))
	}

	// SHA-256d hash must be 32 bytes and deterministic
	hash1 := c.HashBlockHeader(serialized)
	hash2 := c.HashBlockHeader(serialized)
	if len(hash1) != 32 {
		t.Errorf("HashBlockHeader: got %d bytes, want 32", len(hash1))
	}
	if hex.EncodeToString(hash1) != hex.EncodeToString(hash2) {
		t.Error("HashBlockHeader: not deterministic")
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Difficulty
// ───────────────────────────────────────────────────────────────────────────────

func TestECashDifficulty(t *testing.T) {
	c := NewECashCoin()

	// Difficulty-1 target (0x1d00ffff) used in genesis
	bits := uint32(0x1d00ffff)
	target := c.TargetFromBits(bits)
	if target.Sign() == 0 {
		t.Error("TargetFromBits: expected non-zero target")
	}

	diff := c.DifficultyFromTarget(target)
	if diff < 0.99 || diff > 1.01 {
		t.Errorf("DifficultyFromTarget(diff-1 target): got %.4f, want ~1.0", diff)
	}

	// Negative-mantissa bits must yield zero target (SECURITY: rejects invalid targets)
	negBits := uint32(0x1d800000) // negative bit set
	negTarget := c.TargetFromBits(negBits)
	if negTarget.Sign() != 0 {
		t.Error("TargetFromBits with negative mantissa: expected zero target")
	}

	if c.ShareDifficultyMultiplier() != 1.0 {
		t.Errorf("ShareDifficultyMultiplier: got %.2f, want 1.0", c.ShareDifficultyMultiplier())
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Coinbase script building — mining output
// ───────────────────────────────────────────────────────────────────────────────

func TestECashCoinbaseScript_P2PKH_CashAddr(t *testing.T) {
	c := NewECashCoin()
	poolAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, false)

	params := CoinbaseParams{
		Height:          951001,
		ExtraNonce:      []byte{0x01, 0x02, 0x03, 0x04},
		PoolAddress:     poolAddr,
		BlockReward:     312500000,
		CoinbaseMessage: "Spiral Pool",
	}

	script, err := c.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("BuildCoinbaseScript: %v", err)
	}
	// P2PKH: OP_DUP OP_HASH160 <20 bytes> OP_EQUALVERIFY OP_CHECKSIG = 25 bytes
	if len(script) != 25 {
		t.Errorf("P2PKH script length: got %d, want 25", len(script))
	}
	if script[0] != 0x76 {
		t.Errorf("P2PKH script[0]: got 0x%02x, want 0x76 (OP_DUP)", script[0])
	}
	if script[1] != 0xa9 {
		t.Errorf("P2PKH script[1]: got 0x%02x, want 0xa9 (OP_HASH160)", script[1])
	}
	if script[2] != 0x14 {
		t.Errorf("P2PKH script[2]: got 0x%02x, want 0x14 (PUSH 20 bytes)", script[2])
	}
	if script[23] != 0x88 {
		t.Errorf("P2PKH script[23]: got 0x%02x, want 0x88 (OP_EQUALVERIFY)", script[23])
	}
	if script[24] != 0xac {
		t.Errorf("P2PKH script[24]: got 0x%02x, want 0xac (OP_CHECKSIG)", script[24])
	}
	// Hash embedded in script must match the original
	if !bytes.Equal(script[3:23], testECashHash) {
		t.Errorf("P2PKH hash in script:\n  got:  %x\n  want: %x", script[3:23], testECashHash)
	}
}

func TestECashCoinbaseScript_P2SH_CashAddr(t *testing.T) {
	c := NewECashCoin()
	poolAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, true)

	params := CoinbaseParams{
		Height:      951001,
		PoolAddress: poolAddr,
		BlockReward: 312500000,
	}

	script, err := c.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("BuildCoinbaseScript P2SH: %v", err)
	}
	// P2SH: OP_HASH160 <20 bytes> OP_EQUAL = 23 bytes
	if len(script) != 23 {
		t.Errorf("P2SH script length: got %d, want 23", len(script))
	}
	if script[0] != 0xa9 {
		t.Errorf("P2SH script[0]: got 0x%02x, want 0xa9 (OP_HASH160)", script[0])
	}
	if script[22] != 0x87 {
		t.Errorf("P2SH script[22]: got 0x%02x, want 0x87 (OP_EQUAL)", script[22])
	}
}

func TestECashCoinbaseScript_Legacy(t *testing.T) {
	c := NewECashCoin()

	// Legacy P2PKH (same version bytes as Bitcoin, commonly used by older wallets)
	params := CoinbaseParams{
		Height:      951001,
		PoolAddress: "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
		BlockReward: 312500000,
	}
	script, err := c.BuildCoinbaseScript(params)
	if err != nil {
		t.Fatalf("BuildCoinbaseScript legacy P2PKH: %v", err)
	}
	if len(script) != 25 {
		t.Errorf("legacy P2PKH script length: got %d, want 25", len(script))
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// XEC-specific: MinerFund and StakingRewards coinbase outputs
// ───────────────────────────────────────────────────────────────────────────────

// TestDecodeMinerFundScript verifies that the mandatory IFP (Infrastructure Funding
// Plan) coinbase output script is built correctly for both P2PKH and P2SH addresses.
func TestDecodeMinerFundScript(t *testing.T) {
	// P2PKH via CashAddr — uses the test hash fixture
	mfAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, false)
	script, err := DecodeMinerFundScript(mfAddr)
	if err != nil {
		t.Fatalf("DecodeMinerFundScript P2PKH (CashAddr): %v", err)
	}
	if len(script) != 25 {
		t.Errorf("MinerFund P2PKH script length: got %d, want 25", len(script))
	}
	if script[0] != 0x76 || script[1] != 0xa9 {
		t.Errorf("MinerFund P2PKH: expected OP_DUP OP_HASH160, got 0x%02x 0x%02x", script[0], script[1])
	}
	if !bytes.Equal(script[3:23], testECashHash) {
		t.Errorf("MinerFund P2PKH hash mismatch:\n  got:  %x\n  want: %x", script[3:23], testECashHash)
	}

	// P2SH via legacy address — mirrors the IFP contract address format
	script, err = DecodeMinerFundScript("3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy")
	if err != nil {
		t.Fatalf("DecodeMinerFundScript P2SH (legacy): %v", err)
	}
	if len(script) != 23 {
		t.Errorf("MinerFund P2SH script length: got %d, want 23", len(script))
	}
	if script[0] != 0xa9 || script[1] != 0x14 {
		t.Errorf("MinerFund P2SH: expected OP_HASH160 PUSH20, got 0x%02x 0x%02x", script[0], script[1])
	}
	if script[22] != 0x87 {
		t.Errorf("MinerFund P2SH: expected OP_EQUAL at [22], got 0x%02x", script[22])
	}

	// Error: empty address
	if _, err = DecodeMinerFundScript(""); err == nil {
		t.Error("DecodeMinerFundScript: expected error for empty address")
	}

	// Error: not a valid address
	if _, err = DecodeMinerFundScript("notanaddress"); err == nil {
		t.Error("DecodeMinerFundScript: expected error for garbage input")
	}
}

// TestDecodeStakingScript verifies the staking rewards script decoder.
// The XEC node provides this script verbatim in getblocktemplate; the pool
// must include it in the coinbase transaction unchanged.
func TestDecodeStakingScript(t *testing.T) {
	// Standard P2SH script as would appear in the node's GBT response:
	// OP_HASH160 <20 bytes> OP_EQUAL
	stakingHex := "a914" + hex.EncodeToString(testECashHash) + "87"
	script, err := DecodeStakingScript(stakingHex)
	if err != nil {
		t.Fatalf("DecodeStakingScript valid hex: %v", err)
	}
	if len(script) != 23 {
		t.Errorf("staking script length: got %d, want 23", len(script))
	}
	// Script contents must be byte-identical to the decoded hex
	want, _ := hex.DecodeString(stakingHex)
	if !bytes.Equal(script, want) {
		t.Errorf("staking script bytes:\n  got:  %x\n  want: %x", script, want)
	}

	// A longer P2PKH staking script is also accepted — decoder is format-agnostic
	p2pkhHex := "76a914" + hex.EncodeToString(testECashHash) + "88ac"
	script, err = DecodeStakingScript(p2pkhHex)
	if err != nil {
		t.Fatalf("DecodeStakingScript P2PKH hex: %v", err)
	}
	if len(script) != 25 {
		t.Errorf("P2PKH staking script length: got %d, want 25", len(script))
	}

	// Error: empty hex
	if _, err = DecodeStakingScript(""); err == nil {
		t.Error("DecodeStakingScript: expected error for empty hex")
	}

	// Error: invalid hex (odd length)
	if _, err = DecodeStakingScript("a9ff0"); err == nil {
		t.Error("DecodeStakingScript: expected error for odd-length hex")
	}

	// Error: non-hex characters
	if _, err = DecodeStakingScript("gg1122"); err == nil {
		t.Error("DecodeStakingScript: expected error for non-hex input")
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Full coinbase reward pipeline
//
// Tests the complete mining → submission → reward → maturation path at the coin
// package level.  The block reward accounting for XEC (from live height 951,001):
//
//   Gross reward      = CoinbaseValue returned by node before job manager deductions
//   MinerFund (IFP)   = mandatory P2SH output, deducted by job manager (manager.go:835)
//   StakingRewards    = mandatory P2SH output, deducted by job manager (manager.go:841)
//   Net pool reward   = 312,500,000 satoshis = 3,125,000 XEC
//   Maturity          = 100 confirmations × 600s = 60,000 s ≈ 16.67 hours
// ───────────────────────────────────────────────────────────────────────────────

func TestECashCoinbaseRewardPipeline(t *testing.T) {
	c := NewECashCoin()
	poolAddr := encodeCashAddrForTest(t, XECCashAddrPrefix, testECashHash, false)

	// 1. Pool reward output (mining → submission)
	rewardScript, err := c.BuildCoinbaseScript(CoinbaseParams{
		Height:          951001,
		PoolAddress:     poolAddr,
		BlockReward:     312500000, // net pool satoshis (post-IFP/staking deductions)
		CoinbaseMessage: "Spiral Pool",
	})
	if err != nil {
		t.Fatalf("BuildCoinbaseScript (pool reward): %v", err)
	}
	if len(rewardScript) != 25 {
		t.Errorf("pool reward script: got %d bytes, want 25 (P2PKH)", len(rewardScript))
	}
	if rewardScript[0] != 0x76 || rewardScript[1] != 0xa9 {
		t.Errorf("pool reward: expected OP_DUP OP_HASH160, got 0x%02x 0x%02x",
			rewardScript[0], rewardScript[1])
	}

	// 2. MinerFund output (mandatory IFP — deducted before pool receives net reward)
	mfScript, err := DecodeMinerFundScript("3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy")
	if err != nil {
		t.Fatalf("DecodeMinerFundScript: %v", err)
	}
	if len(mfScript) != 23 {
		t.Errorf("MinerFund script: got %d bytes, want 23 (P2SH)", len(mfScript))
	}

	// 3. StakingRewards output (mandatory — provided by node in GBT)
	srHex := "a914" + hex.EncodeToString(testECashHash) + "87"
	srScript, err := DecodeStakingScript(srHex)
	if err != nil {
		t.Fatalf("DecodeStakingScript: %v", err)
	}
	if len(srScript) != 23 {
		t.Errorf("StakingRewards script: got %d bytes, want 23 (P2SH)", len(srScript))
	}

	// 4. Maturation — a found block must reach 100 confirmations before spendable.
	//    At 600 s/block that is 60,000 s ≈ 16.67 hours.
	if c.CoinbaseMaturity() != XECCoinbaseMaturity {
		t.Errorf("CoinbaseMaturity: got %d, want %d", c.CoinbaseMaturity(), XECCoinbaseMaturity)
	}
	maturitySeconds := c.CoinbaseMaturity() * c.BlockTime()
	if maturitySeconds != 60000 {
		t.Errorf("maturity window: got %d s, want 60000 s (16.67 h)", maturitySeconds)
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Genesis block
// ───────────────────────────────────────────────────────────────────────────────

func TestECashGenesisHash(t *testing.T) {
	// XEC shares Bitcoin's genesis block (forked from BCH which forked from BTC)
	expected := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	if XECGenesisBlockHash != expected {
		t.Errorf("XECGenesisBlockHash:\n  got:  %s\n  want: %s", XECGenesisBlockHash, expected)
	}
}

func TestECashVerifyGenesisBlock(t *testing.T) {
	c := NewECashCoin()

	// Exact match (case-insensitive)
	if err := c.VerifyGenesisBlock(XECGenesisBlockHash); err != nil {
		t.Errorf("VerifyGenesisBlock correct hash: %v", err)
	}
	if err := c.VerifyGenesisBlock(strings.ToUpper(XECGenesisBlockHash)); err != nil {
		t.Errorf("VerifyGenesisBlock uppercase hash: %v", err)
	}

	// Wrong hash must return an error — catches connecting to wrong node
	if err := c.VerifyGenesisBlock("0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Error("VerifyGenesisBlock wrong hash: expected error, got nil")
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Registry
// ───────────────────────────────────────────────────────────────────────────────

func TestECashRegistry(t *testing.T) {
	if !IsRegistered("XEC") {
		t.Error("IsRegistered(XEC): expected true")
	}
	if !IsRegistered("ECASH") {
		t.Error("IsRegistered(ECASH): expected true (alias)")
	}

	xec, err := Create("XEC")
	if err != nil {
		t.Fatalf("Create(XEC): %v", err)
	}
	if xec.Symbol() != "XEC" {
		t.Errorf("Create(XEC).Symbol: got %q, want XEC", xec.Symbol())
	}

	// Case-insensitive lookup
	xecLower, err := Create("xec")
	if err != nil {
		t.Fatalf("Create(xec): %v", err)
	}
	if xecLower.Symbol() != "XEC" {
		t.Errorf("Create(xec).Symbol: got %q, want XEC", xecLower.Symbol())
	}

	// Alias
	ecash, err := Create("ECASH")
	if err != nil {
		t.Fatalf("Create(ECASH): %v", err)
	}
	if ecash.Symbol() != "XEC" {
		t.Errorf("Create(ECASH).Symbol: got %q, want XEC", ecash.Symbol())
	}
}

// ───────────────────────────────────────────────────────────────────────────────
// Benchmarks
// ───────────────────────────────────────────────────────────────────────────────

func BenchmarkECashCashAddrValidation(b *testing.B) {
	c := NewECashCoin()
	addr := encodeCashAddrForTest(b, XECCashAddrPrefix, testECashHash, false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.ValidateAddress(addr)
	}
}

func BenchmarkECashLegacyValidation(b *testing.B) {
	c := NewECashCoin()
	address := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.ValidateAddress(address)
	}
}

func BenchmarkECashBlockHeaderHashing(b *testing.B) {
	c := NewECashCoin()
	header := &BlockHeader{
		Version:           536870912,
		PreviousBlockHash: make([]byte, 32),
		MerkleRoot:        make([]byte, 32),
		Timestamp:         1746000000,
		Bits:              0x18034287,
		Nonce:             12345,
	}
	serialized := c.SerializeBlockHeader(header)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.HashBlockHeader(serialized)
	}
}
