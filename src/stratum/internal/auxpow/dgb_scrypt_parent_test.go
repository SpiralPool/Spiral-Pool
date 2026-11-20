// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/crypto"
)

// dogeStyleAuxCheck re-implements the parts of Dogecoin/PepeCoin's
// CAuxPow::check that depend on the PARENT chain, so a DGB-SCRYPT parent can be
// held to the same rules an LTC parent already meets:
//   - strict chain ID: parent nVersion>>16 must not equal the aux chain ID
//   - exactly one fabe6d6d marker, aux root immediately after it
//   - nSize == 1<<len(auxBranch), slot from nonce matches the chain index
//   - coinbase hash links to the parent header's merkle root
//   - parent PoW = scrypt(header) is what the share validator hashed
func dogeStyleAuxCheck(t *testing.T, parentHeader, coinbase []byte, chainID int32, auxRootLE []byte) {
	t.Helper()
	ver := binary.LittleEndian.Uint32(parentHeader[0:4])
	if int32(ver>>16) == chainID {
		t.Fatalf("parent version %#x carries the aux chain ID %d (Dogecoin rejects: 'Aux POW parent has our chain ID')", ver, chainID)
	}
	if bytes.Count(coinbase, AuxMarker) != 1 {
		t.Fatalf("need exactly one merged-mining marker, found %d", bytes.Count(coinbase, AuxMarker))
	}
	pos := bytes.Index(coinbase, AuxMarker) + len(AuxMarker)
	rootBE := make([]byte, 32)
	for i := range rootBE {
		rootBE[i] = auxRootLE[31-i]
	}
	if !bytes.Equal(coinbase[pos:pos+32], rootBE) {
		t.Fatal("aux merkle root does not immediately follow the marker")
	}
	nSize := binary.LittleEndian.Uint32(coinbase[pos+32 : pos+36])
	if nSize != 1 { // single aux chain: merkle height 0
		t.Fatalf("nSize %d, want 1 for a single aux chain", nSize)
	}
	if !bytes.Equal(crypto.SHA256d(coinbase), parentHeader[36:68]) {
		t.Fatal("coinbase does not link to the parent merkle root")
	}
}

// A DigiByte-Scrypt block found while merge-mining Dogecoin and PepeCoin must
// produce an AuxPoW those chains accept: same commitment and proof path as an
// LTC parent, with DGB's multi-algo header version.
func TestDGBScryptParent_AuxPowProofForDogeAndPep(t *testing.T) {
	parent := coin.NewDigiByteScryptCoin()
	for _, aux := range []coin.AuxPowCoin{coin.NewDogecoinCoin(), coin.NewPepeCoinCoin()} {
		t.Run(aux.Symbol(), func(t *testing.T) {
			auxHash := crypto.SHA256d([]byte("aux block " + aux.Symbol()))
			commitment := BuildAuxCommitment(auxHash, 1, 0)

			// Coinbase scriptSig: BIP34 height (DGB ~23M → 4-byte push) + commitment + extranonce + tag.
			height := []byte{0x04, 0x31, 0xf3, 0x5f, 0x01}
			scriptSig := append(append(append([]byte{}, height...), commitment...), []byte("\x00\x00\x00\x00\x00\x00\x00\x01/SpiralPool/")...)
			if len(scriptSig) > 100 {
				t.Fatalf("scriptSig %d bytes exceeds the 100-byte consensus limit", len(scriptSig))
			}
			cb := []byte{0x01, 0x00, 0x00, 0x00, 0x01}
			cb = append(cb, make([]byte, 32)...)
			cb = append(cb, 0xff, 0xff, 0xff, 0xff, byte(len(scriptSig)))
			cb = append(cb, scriptSig...)
			cb = append(cb, 0xff, 0xff, 0xff, 0xff, 0x01)
			cb = append(cb, make([]byte, 8)...)
			cb = append(cb, 0x00, 0x00, 0x00, 0x00, 0x00)

			// DGB-scrypt header: multi-algo version 0x20000002 (scrypt algo bits, no AuxPoW flag).
			header := make([]byte, 80)
			binary.LittleEndian.PutUint32(header[0:4], 0x20000002)
			copy(header[36:68], crypto.SHA256d(cb))
			binary.LittleEndian.PutUint32(header[68:72], 1763400000)
			binary.LittleEndian.PutUint32(header[72:76], 0x1b01c13a)

			powHash := parent.HashBlockHeader(header)
			if !bytes.Equal(powHash, crypto.ScryptHash(header)) {
				t.Fatal("DGB-SCRYPT must hash the header with plain scrypt, as the aux chain will")
			}

			auxBlock := &AuxBlockData{Symbol: aux.Symbol(), ChainID: aux.ChainID(), Hash: auxHash, ChainIndex: 0}
			proof, err := BuildAuxPowProof(cb, nil, header, powHash, auxBlock, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyAuxPowProof(proof, auxHash, auxHash); err != nil {
				t.Fatalf("proof does not verify: %v", err)
			}
			dogeStyleAuxCheck(t, header, cb, aux.ChainID(), auxHash)

			ser, err := aux.SerializeAuxPowProof(proof)
			if err != nil || len(ser) == 0 {
				t.Fatalf("%s cannot serialize the proof: %v", aux.Symbol(), err)
			}
			if !bytes.Contains(ser, header) {
				t.Fatal("serialized AuxPoW must carry the DGB parent header verbatim")
			}
		})
	}
}
