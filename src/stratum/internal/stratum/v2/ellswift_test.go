// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"bytes"
	"encoding/csv"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Test vectors come from BIP324 (bitcoin/bips, BSD-3-Clause); see
// testdata/PROVENANCE.txt.

func readVectors(t *testing.T, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, row := range rows[1:] {
		m := make(map[string]string, len(row))
		for i, col := range rows[0] {
			m[col] = row[i]
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no vectors", name)
	}
	return out
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestEllSwiftConstantC(t *testing.T) {
	want := mustHex(t, "0a2d2ba93507f1df233770c2a797962cc61f6d15da14ecd47d8d27ae1cd5f852")
	if got := ellswiftC.Bytes(); !bytes.Equal(got[:], want) {
		t.Fatalf("c = %x, want %x", got[:], want)
	}
}

func TestEllSwiftDecodeVectors(t *testing.T) {
	for i, v := range readVectors(t, "ellswift_decode_test_vectors.csv") {
		var enc [64]byte
		copy(enc[:], mustHex(t, v["ellswift"]))
		got := EllSwiftDecode(enc)
		if want := mustHex(t, v["x"]); !bytes.Equal(got[:], want) {
			t.Errorf("vector %d (%s): x = %x, want %x", i, v["comment"], got, want)
		}
	}
}

func TestXSwiftECInvVectors(t *testing.T) {
	for i, v := range readVectors(t, "xswiftec_inv_test_vectors.csv") {
		u := feFromBytes(mustHex(t, v["u"]))
		x := feFromBytes(mustHex(t, v["x"]))
		for branch := 0; branch < 8; branch++ {
			want := v["case"+string(rune('0'+branch))+"_t"]
			got := xswiftecInv(x, u, branch)
			if want == "" {
				if got != nil {
					t.Errorf("vector %d case %d: got t=%x, want none", i, branch, got.Bytes()[:])
				}
				continue
			}
			if got == nil {
				t.Errorf("vector %d case %d: got none, want %s", i, branch, want)
				continue
			}
			if !bytes.Equal(got.Bytes()[:], mustHex(t, want)) {
				t.Errorf("vector %d case %d: t = %x, want %s", i, branch, got.Bytes()[:], want)
				continue
			}
			if back := xswiftec(u, got); !back.Equals(x) {
				t.Errorf("vector %d case %d: xswiftec(u, t) does not return x", i, branch)
			}
		}
	}
}

func TestEllSwiftECDHVectors(t *testing.T) {
	for i, v := range readVectors(t, "bip324_ecdh_test_vectors.csv") {
		priv := secp256k1.PrivKeyFromBytes(mustHex(t, v["in_priv_ours"]))
		var ours, theirs [64]byte
		copy(ours[:], mustHex(t, v["in_ellswift_ours"]))
		copy(theirs[:], mustHex(t, v["in_ellswift_theirs"]))

		x, err := ellswiftECDHXOnly(theirs, priv)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		if want := mustHex(t, v["mid_x_shared"]); !bytes.Equal(x[:], want) {
			t.Errorf("vector %d: shared x = %x, want %x", i, x, want)
		}
		secret, err := EllSwiftECDH(priv, ours, theirs, v["in_initiating"] == "1")
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		if want := mustHex(t, v["mid_shared_secret"]); !bytes.Equal(secret[:], want) {
			t.Errorf("vector %d: secret = %x, want %x", i, secret, want)
		}
	}
}

func TestEllSwiftEncodeRoundTripAndAgreement(t *testing.T) {
	for i := 0; i < 16; i++ {
		a, _ := secp256k1.GeneratePrivateKey()
		b, _ := secp256k1.GeneratePrivateKey()
		encA, err := EllSwiftEncode(a.PubKey())
		if err != nil {
			t.Fatal(err)
		}
		encB, err := EllSwiftEncode(b.PubKey())
		if err != nil {
			t.Fatal(err)
		}
		if x := EllSwiftDecode(encA); !bytes.Equal(x[:], a.PubKey().SerializeCompressed()[1:]) {
			t.Fatalf("round %d: encoding decodes to %x, not the key's x", i, x)
		}
		initiator, err := EllSwiftECDH(a, encA, encB, true)
		if err != nil {
			t.Fatal(err)
		}
		responder, err := EllSwiftECDH(b, encB, encA, false)
		if err != nil {
			t.Fatal(err)
		}
		if initiator != responder {
			t.Fatalf("round %d: initiator and responder secrets differ", i)
		}
	}
}
