// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// BIP340 Schnorr signatures over secp256k1, with 32-byte x-only public keys. The
// Stratum V2 handshake uses them for the pool authority's signature over the
// server's static key.

// SchnorrPubKey returns the BIP340 x-only public key for a private key.
func SchnorrPubKey(priv *secp256k1.PrivateKey) [32]byte {
	var out [32]byte
	copy(out[:], priv.PubKey().SerializeCompressed()[1:])
	return out
}

// SchnorrSign signs msg with the BIP340 default signing algorithm, using aux as
// the auxiliary randomness.
func SchnorrSign(priv *secp256k1.PrivateKey, msg []byte, aux [32]byte) ([64]byte, error) {
	var sig [64]byte

	var d secp256k1.ModNScalar
	d.Set(&priv.Key)
	if d.IsZero() {
		return sig, errors.New("schnorr: zero private key")
	}
	var P secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(&d, &P)
	P.ToAffine()
	P.X.Normalize()
	P.Y.Normalize()
	if P.Y.IsOdd() {
		d.Negate()
	}
	px := P.X.Bytes()

	auxHash := taggedHash("BIP0340/aux", aux[:])
	t := d.Bytes()
	for i := range t {
		t[i] ^= auxHash[i]
	}
	nonce := taggedHash("BIP0340/nonce", t[:], px[:], msg)

	var k secp256k1.ModNScalar
	k.SetBytes(&nonce)
	if k.IsZero() {
		return sig, errors.New("schnorr: zero nonce")
	}
	var R secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(&k, &R)
	R.ToAffine()
	R.X.Normalize()
	R.Y.Normalize()
	if R.Y.IsOdd() {
		k.Negate()
	}
	rx := R.X.Bytes()

	challenge := taggedHash("BIP0340/challenge", rx[:], px[:], msg)
	var e secp256k1.ModNScalar
	e.SetBytes(&challenge)
	var s secp256k1.ModNScalar
	s.Mul2(&e, &d).Add(&k)

	copy(sig[:32], rx[:])
	sBytes := s.Bytes()
	copy(sig[32:], sBytes[:])

	if !SchnorrVerify(*px, msg, sig) {
		return [64]byte{}, errors.New("schnorr: produced signature does not verify")
	}
	return sig, nil
}

// SchnorrVerify reports whether sig is a valid BIP340 signature of msg by pub.
func SchnorrVerify(pub [32]byte, msg []byte, sig [64]byte) bool {
	var px, py secp256k1.FieldVal
	if px.SetBytes(&pub) != 0 {
		return false
	}
	if !secp256k1.DecompressY(&px, false, &py) {
		return false
	}
	var r secp256k1.FieldVal
	if r.SetBytes((*[32]byte)(sig[:32])) != 0 {
		return false
	}
	var s secp256k1.ModNScalar
	if s.SetBytes((*[32]byte)(sig[32:])) != 0 {
		return false
	}

	challenge := taggedHash("BIP0340/challenge", sig[:32], pub[:], msg)
	var e secp256k1.ModNScalar
	e.SetBytes(&challenge)
	e.Negate()

	var P, sG, eP, R secp256k1.JacobianPoint
	P.X.Set(&px)
	P.Y.Set(&py)
	P.Z.SetInt(1)
	secp256k1.ScalarBaseMultNonConst(&s, &sG)
	secp256k1.ScalarMultNonConst(&e, &P, &eP)
	secp256k1.AddNonConst(&sG, &eP, &R)

	R.Z.Normalize()
	if R.Z.IsZero() {
		return false
	}
	R.ToAffine()
	R.X.Normalize()
	R.Y.Normalize()
	if R.Y.IsOdd() {
		return false
	}
	return R.X.Equals(&r)
}
