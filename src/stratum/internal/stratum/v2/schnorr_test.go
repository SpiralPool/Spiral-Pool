// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"bytes"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Vectors are BIP340's test-vectors.csv (bitcoin/bips); see testdata/PROVENANCE.txt.
func TestSchnorrBIP340Vectors(t *testing.T) {
	for _, v := range readVectors(t, "bip340_test_vectors.csv") {
		idx := v["index"]
		var pub [32]byte
		copy(pub[:], mustHex(t, v["public key"]))
		msg := mustHex(t, v["message"])
		var sig [64]byte
		copy(sig[:], mustHex(t, v["signature"]))
		wantValid := v["verification result"] == "TRUE"

		if got := SchnorrVerify(pub, msg, sig); got != wantValid {
			t.Errorf("vector %s (%s): verify = %v, want %v", idx, v["comment"], got, wantValid)
		}

		if v["secret key"] == "" {
			continue
		}
		priv := secp256k1.PrivKeyFromBytes(mustHex(t, v["secret key"]))
		if got := SchnorrPubKey(priv); got != pub {
			t.Errorf("vector %s: public key = %x, want %x", idx, got, pub)
		}
		var aux [32]byte
		copy(aux[:], mustHex(t, v["aux_rand"]))
		got, err := SchnorrSign(priv, msg, aux)
		if err != nil {
			t.Fatalf("vector %s: sign: %v", idx, err)
		}
		if !bytes.Equal(got[:], sig[:]) {
			t.Errorf("vector %s: signature = %x, want %x", idx, got, sig)
		}
	}
}

func TestSchnorrRejectsTamperedMessage(t *testing.T) {
	priv, _ := secp256k1.GeneratePrivateKey()
	msg := []byte("spiral pool static key certificate")
	sig, err := SchnorrSign(priv, msg, [32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	pub := SchnorrPubKey(priv)
	if !SchnorrVerify(pub, msg, sig) {
		t.Fatal("valid signature rejected")
	}
	msg[0] ^= 1
	if SchnorrVerify(pub, msg, sig) {
		t.Fatal("signature over a different message accepted")
	}
}
