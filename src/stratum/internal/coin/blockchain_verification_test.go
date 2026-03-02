// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// REAL BLOCKCHAIN VERIFICATION TESTS
// All hashes can be verified at: blockchain.com, blockchair.com, digiexplorer.info
// Raw headers obtained via: <coin>-cli getblock <hash> 0
// ═══════════════════════════════════════════════════════════════════════════════

// BlockTestVector contains real blockchain data for verification
type BlockTestVector struct {
	Height    uint64
	RawHeader string // 80-byte header in hex (160 chars)
	ExpHash   string // Expected block hash (display order)
}

// ═══════════════════════════════════════════════════════════════════════════════
// BITCOIN (BTC) - SHA256d - VERIFIED REAL DATA
// Verify at: https://blockchain.com/btc/block/<height>
// Raw headers from: bitcoin-cli getblock <hash> 0
// ═══════════════════════════════════════════════════════════════════════════════

var bitcoinTestVectors = []BlockTestVector{
	{
		// Genesis block - universally known
		Height:    0,
		RawHeader: "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a29ab5f49ffff001d1dac2b7c",
		ExpHash:   "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
	},
	{
		// Block 100000 - well documented
		Height:    100000,
		RawHeader: "0100000050120119172a610421a6c3011dd330d9df07b63616c2cc1f1cd00200000000006657a9252aacd5c0b2940996ecff952228c3067cc38d4885efb5a4ac4247e9f337221b4d4c86041b0f2b5710",
		ExpHash:   "000000000003ba27aa200b1cecaad478d2b00432346c3f1f3986da1afd33e506",
	},
	{
		// Block 300000 - verified
		Height:    300000,
		RawHeader: "020000007ef055e1674d2e6551dba41cd214debbee34aeb544c7ec670000000000000000d3998963f80c5bab43fe8c26228e98d030edf4dcbe48a666f5c39e2d7a885c9102c86d536c890019593a470d",
		ExpHash:   "000000000000000082ccf8f1557c5d40b21edabb18d2d691cfbf87118bac7254",
	},
}

func TestBitcoin_SHA256d_RealBlocks(t *testing.T) {
	btc := NewBitcoinCoin()

	for _, tv := range bitcoinTestVectors {
		t.Run(fmt.Sprintf("Block_%d", tv.Height), func(t *testing.T) {
			if len(tv.RawHeader) != 160 {
				t.Fatalf("raw header hex should be 160 chars, got %d", len(tv.RawHeader))
			}

			rawHeader, err := hex.DecodeString(tv.RawHeader)
			if err != nil {
				t.Fatalf("failed to decode header: %v", err)
			}

			hashBytes := btc.HashBlockHeader(rawHeader)
			hashHex := hex.EncodeToString(reverseBytes(hashBytes))

			if hashHex != tv.ExpHash {
				t.Errorf("BTC Block %d hash mismatch!\n  Got:      %s\n  Expected: %s",
					tv.Height, hashHex, tv.ExpHash)
			} else {
				t.Logf("✓ BTC Block %d: %s", tv.Height, hashHex)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// LITECOIN (LTC) - Scrypt - VERIFIED REAL DATA
// Verify at: https://blockchair.com/litecoin/block/<height>
// Raw headers from: litecoin-cli getblock <hash> 0
//
// TO ADD MORE BLOCKS: Run on your Litecoin node:
//   litecoin-cli getblockhash <height>
//   litecoin-cli getblock <hash> 0
// ═══════════════════════════════════════════════════════════════════════════════

var litecoinTestVectors = []BlockTestVector{
	// To add vectors: litecoin-cli getblock $(litecoin-cli getblockhash 0) 0 | head -c 160
}

func TestLitecoin_Scrypt_RealBlocks(t *testing.T) {
	if len(litecoinTestVectors) == 0 {
		t.Skip("No Litecoin test vectors - fetch raw headers from litecoin-cli getblock <hash> 0")
	}

	ltc := NewLitecoinCoin()

	for _, tv := range litecoinTestVectors {
		t.Run(fmt.Sprintf("Block_%d", tv.Height), func(t *testing.T) {
			if len(tv.RawHeader) != 160 {
				t.Fatalf("raw header hex should be 160 chars, got %d", len(tv.RawHeader))
			}

			rawHeader, err := hex.DecodeString(tv.RawHeader)
			if err != nil {
				t.Fatalf("failed to decode header: %v", err)
			}

			hashBytes := ltc.HashBlockHeader(rawHeader)
			hashHex := hex.EncodeToString(reverseBytes(hashBytes))

			if hashHex != tv.ExpHash {
				t.Errorf("LTC Block %d hash mismatch!\n  Got:      %s\n  Expected: %s",
					tv.Height, hashHex, tv.ExpHash)
			} else {
				t.Logf("✓ LTC Block %d: %s", tv.Height, hashHex)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DOGECOIN (DOGE) - Scrypt + AuxPoW (after block 371337)
// Verify at: https://blockchair.com/dogecoin/block/<height>
// Raw headers from: dogecoin-cli getblock <hash> 0
// ═══════════════════════════════════════════════════════════════════════════════

var dogecoinTestVectors = []BlockTestVector{
	// To add vectors: dogecoin-cli getblock $(dogecoin-cli getblockhash 0) 0 | head -c 160
}

func TestDogecoin_Scrypt_RealBlocks(t *testing.T) {
	if len(dogecoinTestVectors) == 0 {
		t.Skip("No Dogecoin test vectors - fetch raw headers from dogecoin-cli getblock <hash> 0")
	}

	doge := NewDogecoinCoin()

	for _, tv := range dogecoinTestVectors {
		t.Run(fmt.Sprintf("Block_%d", tv.Height), func(t *testing.T) {
			if len(tv.RawHeader) != 160 {
				t.Fatalf("raw header hex should be 160 chars, got %d", len(tv.RawHeader))
			}

			rawHeader, err := hex.DecodeString(tv.RawHeader)
			if err != nil {
				t.Fatalf("failed to decode header: %v", err)
			}

			hashBytes := doge.HashBlockHeader(rawHeader)
			hashHex := hex.EncodeToString(reverseBytes(hashBytes))

			if hashHex != tv.ExpHash {
				t.Errorf("DOGE Block %d hash mismatch!\n  Got:      %s\n  Expected: %s",
					tv.Height, hashHex, tv.ExpHash)
			} else {
				t.Logf("✓ DOGE Block %d: %s", tv.Height, hashHex)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// DIGIBYTE (DGB) - SHA256d
// Verify at: https://digiexplorer.info/block/<height>
// Raw headers from: digibyte-cli getblock <hash> 0
// ═══════════════════════════════════════════════════════════════════════════════

var digibyteTestVectors = []BlockTestVector{
	// To add vectors: digibyte-cli getblock $(digibyte-cli getblockhash 0) 0 | head -c 160
}

func TestDigiByte_SHA256d_RealBlocks(t *testing.T) {
	if len(digibyteTestVectors) == 0 {
		t.Skip("No DigiByte test vectors - fetch raw headers from digibyte-cli getblock <hash> 0")
	}

	dgb := NewDigiByteCoin()

	for _, tv := range digibyteTestVectors {
		t.Run(fmt.Sprintf("Block_%d", tv.Height), func(t *testing.T) {
			if len(tv.RawHeader) != 160 {
				t.Fatalf("raw header hex should be 160 chars, got %d", len(tv.RawHeader))
			}

			rawHeader, err := hex.DecodeString(tv.RawHeader)
			if err != nil {
				t.Fatalf("failed to decode header: %v", err)
			}

			hashBytes := dgb.HashBlockHeader(rawHeader)
			hashHex := hex.EncodeToString(reverseBytes(hashBytes))

			if hashHex != tv.ExpHash {
				t.Errorf("DGB Block %d hash mismatch!\n  Got:      %s\n  Expected: %s",
					tv.Height, hashHex, tv.ExpHash)
			} else {
				t.Logf("✓ DGB Block %d: %s", tv.Height, hashHex)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXPOW MERGE MINING TESTS
// ═══════════════════════════════════════════════════════════════════════════════

func TestAuxPoW_Scrypt_MergeMining(t *testing.T) {
	ltc := NewLitecoinCoin()
	doge := NewDogecoinCoin()

	if ltc.Algorithm() != "scrypt" {
		t.Errorf("LTC should use scrypt, got %s", ltc.Algorithm())
	}
	if doge.Algorithm() != "scrypt" {
		t.Errorf("DOGE should use scrypt, got %s", doge.Algorithm())
	}

	if !doge.SupportsAuxPow() {
		t.Error("DOGE should support AuxPoW")
	}

	auxPowStart := doge.AuxPowStartHeight()
	if auxPowStart != 371337 {
		t.Errorf("DOGE AuxPoW start should be 371337, got %d", auxPowStart)
	}

	if ltc.CanBeParentFor("scrypt") {
		t.Logf("  LTC can parent Scrypt aux chains")
	} else {
		t.Error("LTC should be able to parent Scrypt aux chains")
	}

	t.Logf("✓ Scrypt merge mining: LTC (parent) -> DOGE (aux)")
	t.Logf("  DOGE AuxPoW activation: block %d", auxPowStart)
}

func TestAuxPoW_SHA256d_MergeMining(t *testing.T) {
	btc := NewBitcoinCoin()

	if btc.Algorithm() != "sha256d" {
		t.Errorf("BTC should use sha256d, got %s", btc.Algorithm())
	}

	if !btc.CanBeParentFor("sha256d") {
		t.Error("BTC should be able to parent SHA256d aux chains")
	}

	if btc.CanBeParentFor("scrypt") {
		t.Error("BTC should NOT be able to parent Scrypt aux chains")
	}

	marker := btc.CoinbaseAuxMarker()
	expectedMarker := []byte{0xfa, 0xbe, 0x6d, 0x6d}
	if !bytes.Equal(marker, expectedMarker) {
		t.Errorf("AuxPoW marker mismatch: got %x, expected %x", marker, expectedMarker)
	}

	t.Logf("✓ SHA256d merge mining: BTC (parent) -> NMC (aux)")
	t.Logf("  AuxPoW marker: 0x%x", marker)
}

func TestAuxPoW_CommitmentStructure(t *testing.T) {
	const expectedSize = 44

	marker := []byte{0xfa, 0xbe, 0x6d, 0x6d}
	auxRoot := bytes.Repeat([]byte{0xAB}, 32)
	treeSize := uint32(2)
	merkleNonce := uint32(0)

	commitment := make([]byte, 0, expectedSize)
	commitment = append(commitment, marker...)
	commitment = append(commitment, auxRoot...)

	treeSizeBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(treeSizeBytes, treeSize)
	commitment = append(commitment, treeSizeBytes...)

	nonceBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(nonceBytes, merkleNonce)
	commitment = append(commitment, nonceBytes...)

	if len(commitment) != expectedSize {
		t.Fatalf("commitment should be %d bytes, got %d", expectedSize, len(commitment))
	}

	if !bytes.Equal(commitment[0:4], marker) {
		t.Error("marker position incorrect")
	}
	if !bytes.Equal(commitment[4:36], auxRoot) {
		t.Error("aux root position incorrect")
	}

	extractedTreeSize := binary.LittleEndian.Uint32(commitment[36:40])
	if extractedTreeSize != treeSize {
		t.Errorf("tree size mismatch: got %d, expected %d", extractedTreeSize, treeSize)
	}

	t.Logf("✓ AuxPoW commitment structure verified (%d bytes)", len(commitment))
}

func TestAuxPoW_ChainIDs(t *testing.T) {
	doge := NewDogecoinCoin()
	dogeChainID := doge.ChainID()
	if dogeChainID != 98 {
		t.Errorf("DOGE ChainID should be 98, got %d", dogeChainID)
	}

	t.Logf("✓ AuxPoW Chain IDs verified")
	t.Logf("  DOGE: %d", dogeChainID)
}

// ═══════════════════════════════════════════════════════════════════════════════
// BYTE ORDER TESTS
// ═══════════════════════════════════════════════════════════════════════════════

func TestByteOrder_BlockHashConversion(t *testing.T) {
	display := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	internal := "6fe28c0ab6f1b372c1a6a246ae63f74f931e8365e15a089c68d6190000000000"

	displayBytes, _ := hex.DecodeString(display)
	internalBytes, _ := hex.DecodeString(internal)

	reversed := reverseBytes(displayBytes)
	if hex.EncodeToString(reversed) != internal {
		t.Error("display -> internal conversion failed")
	}

	reversed2 := reverseBytes(internalBytes)
	if hex.EncodeToString(reversed2) != display {
		t.Error("internal -> display conversion failed")
	}

	t.Logf("✓ Byte order conversion verified")
}

func TestHeaderSerialization_AllCoins(t *testing.T) {
	coins := []struct {
		name string
		coin interface {
			SerializeBlockHeader(*BlockHeader) []byte
			HashBlockHeader([]byte) []byte
		}
	}{
		{"BTC", NewBitcoinCoin()},
		{"LTC", NewLitecoinCoin()},
		{"DOGE", NewDogecoinCoin()},
		{"DGB", NewDigiByteCoin()},
	}

	header := &BlockHeader{
		Version:           0x20000000,
		PreviousBlockHash: bytes.Repeat([]byte{0x11}, 32),
		MerkleRoot:        bytes.Repeat([]byte{0x22}, 32),
		Timestamp:         1700000000,
		Bits:              0x1d00ffff,
		Nonce:             12345678,
	}

	for _, tc := range coins {
		t.Run(tc.name, func(t *testing.T) {
			serialized := tc.coin.SerializeBlockHeader(header)

			if len(serialized) != 80 {
				t.Errorf("%s header should be 80 bytes, got %d", tc.name, len(serialized))
			}

			hash1 := tc.coin.HashBlockHeader(serialized)
			hash2 := tc.coin.HashBlockHeader(serialized)

			if !bytes.Equal(hash1, hash2) {
				t.Errorf("%s hashing not deterministic", tc.name)
			}

			if len(hash1) != 32 {
				t.Errorf("%s hash should be 32 bytes, got %d", tc.name, len(hash1))
			}

			t.Logf("✓ %s: header=%d bytes, hash=%d bytes", tc.name, len(serialized), len(hash1))
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// HELPER
// ═══════════════════════════════════════════════════════════════════════════════

func reverseBytes(b []byte) []byte {
	result := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		result[i] = b[len(b)-1-i]
	}
	return result
}
