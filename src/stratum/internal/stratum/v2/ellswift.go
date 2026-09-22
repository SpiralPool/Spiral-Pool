// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// ElligatorSwift, as specified in BIP324, encodes a secp256k1 x coordinate as 64
// bytes u || t that are indistinguishable from random. The Stratum V2 Noise
// handshake ("Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256") sends every public
// key in this form and derives DH results with BIP324's x-only ECDH.

// EllSwiftPubKeySize is the size of an ElligatorSwift-encoded public key.
const EllSwiftPubKeySize = 64

var (
	feOne   = feInt(1)
	feTwo   = feInt(2)
	feThree = feInt(3)
	feFour  = feInt(4)
	feSeven = feInt(7)
	feHalf  = feInv(feTwo)

	// ellswiftC is the square root of -3 mod p that is itself a square, the
	// constant BIP324 fixes.
	ellswiftC = func() *secp256k1.FieldVal {
		c, ok := feSqrt(feNeg(feThree))
		if !ok {
			panic("secp256k1: -3 has no square root")
		}
		if _, square := feSqrt(c); !square {
			c = feNeg(c)
		}
		return c
	}()
	ellswiftCMinus = feMul(feSub(feOne, ellswiftC), feHalf) // (1 - c) / 2
	ellswiftCPlus  = feMul(feAdd(feOne, ellswiftC), feHalf) // (1 + c) / 2
)

// Field helpers. Every result is normalized, so values can be compared and fed
// into further operations without tracking magnitudes.

func feInt(v uint16) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.SetInt(v)
	return &r
}

func feAdd(a, b *secp256k1.FieldVal) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.Add2(a, b).Normalize()
	return &r
}

func feSub(a, b *secp256k1.FieldVal) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.NegateVal(b, 1).Add(a).Normalize()
	return &r
}

func feMul(a, b *secp256k1.FieldVal) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.Mul2(a, b).Normalize()
	return &r
}

func feSqr(a *secp256k1.FieldVal) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.SquareVal(a).Normalize()
	return &r
}

func feNeg(a *secp256k1.FieldVal) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.NegateVal(a, 1).Normalize()
	return &r
}

// feInv returns 1/a, and 0 for a = 0, as BIP324's reference field arithmetic does.
func feInv(a *secp256k1.FieldVal) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.Set(a).Inverse().Normalize()
	return &r
}

// feSqrt returns a^((p+1)/4) and whether it is a square root of a.
func feSqrt(a *secp256k1.FieldVal) (*secp256k1.FieldVal, bool) {
	var r secp256k1.FieldVal
	ok := r.SquareRootVal(a)
	r.Normalize()
	return &r, ok
}

// feFromBytes interprets 32 big-endian bytes as an integer reduced mod p.
func feFromBytes(b []byte) *secp256k1.FieldVal {
	var r secp256k1.FieldVal
	r.SetByteSlice(b)
	r.Normalize()
	return &r
}

// validX reports whether x is the x coordinate of a curve point.
func validX(x *secp256k1.FieldVal) bool {
	_, ok := feSqrt(feAdd(feMul(feSqr(x), x), feSeven))
	return ok
}

// xswiftec maps any field elements (u, t) to the x coordinate of a curve point.
func xswiftec(u, t *secp256k1.FieldVal) *secp256k1.FieldVal {
	if u.IsZero() {
		u = feOne
	}
	if t.IsZero() {
		t = feOne
	}
	u3 := feMul(feSqr(u), u)
	if feAdd(feAdd(u3, feSqr(t)), feSeven).IsZero() {
		t = feAdd(t, t)
	}
	X := feMul(feSub(feAdd(u3, feSeven), feSqr(t)), feInv(feAdd(t, t)))
	Y := feMul(feAdd(X, t), feInv(feMul(ellswiftC, u)))

	if x := feAdd(u, feMul(feFour, feSqr(Y))); validX(x) {
		return x
	}
	XoverY := feMul(X, feInv(Y))
	if x := feMul(feSub(feNeg(XoverY), u), feHalf); validX(x) {
		return x
	}
	// BIP324 shows one of the three candidates is always valid.
	return feMul(feSub(XoverY, u), feHalf)
}

// xswiftecInv returns a t with xswiftec(u, t) = x for the given case (0-7), or nil
// when that case has no preimage.
func xswiftecInv(x, u *secp256k1.FieldVal, branch int) *secp256k1.FieldVal {
	u2 := feSqr(u)
	u3plus7 := feAdd(feMul(u2, u), feSeven)

	var s, v *secp256k1.FieldVal
	if branch&2 == 0 {
		if validX(feSub(feNeg(x), u)) {
			return nil
		}
		v = x
		s = feNeg(feMul(u3plus7, feInv(feAdd(feAdd(u2, feMul(u, v)), feSqr(v)))))
	} else {
		s = feSub(x, u)
		if s.IsZero() {
			return nil
		}
		r, ok := feSqrt(feNeg(feMul(s, feAdd(feMul(feFour, u3plus7), feMul(feMul(feThree, u2), s)))))
		if !ok {
			return nil
		}
		if branch&1 == 1 && r.IsZero() {
			return nil
		}
		v = feMul(feSub(feMul(r, feInv(s)), u), feHalf)
	}

	w, ok := feSqrt(s)
	if !ok {
		return nil
	}
	switch branch & 5 {
	case 0:
		return feNeg(feMul(w, feAdd(feMul(u, ellswiftCMinus), v)))
	case 1:
		return feMul(w, feAdd(feMul(u, ellswiftCPlus), v))
	case 4:
		return feMul(w, feAdd(feMul(u, ellswiftCMinus), v))
	default:
		return feNeg(feMul(w, feAdd(feMul(u, ellswiftCPlus), v)))
	}
}

// EllSwiftDecode returns the x coordinate an ElligatorSwift encoding maps to.
// Every 64-byte string is a valid encoding.
func EllSwiftDecode(enc [EllSwiftPubKeySize]byte) [32]byte {
	return *xswiftec(feFromBytes(enc[:32]), feFromBytes(enc[32:])).Bytes()
}

// EllSwiftEncode returns a random ElligatorSwift encoding of a public key's x
// coordinate.
func EllSwiftEncode(pub *secp256k1.PublicKey) ([EllSwiftPubKeySize]byte, error) {
	var out [EllSwiftPubKeySize]byte
	x := feFromBytes(pub.SerializeCompressed()[1:])
	var buf [33]byte
	for attempt := 0; attempt < 1000; attempt++ {
		if _, err := rand.Read(buf[:]); err != nil {
			return out, err
		}
		var u secp256k1.FieldVal
		if u.SetBytes((*[32]byte)(buf[:32])) != 0 {
			continue
		}
		if u.IsZero() {
			continue
		}
		if t := xswiftecInv(x, &u, int(buf[32]&7)); t != nil {
			copy(out[:32], u.Bytes()[:])
			copy(out[32:], t.Bytes()[:])
			return out, nil
		}
	}
	// Each attempt succeeds with probability about 1/4.
	return out, errors.New("ellswift: no encoding found")
}

// ellswiftECDHXOnly returns x(priv * P), where P is either point with the x
// coordinate the peer's encoding maps to.
func ellswiftECDHXOnly(theirs [EllSwiftPubKeySize]byte, priv *secp256k1.PrivateKey) ([32]byte, error) {
	var out [32]byte
	x := xswiftec(feFromBytes(theirs[:32]), feFromBytes(theirs[32:]))
	var point, result secp256k1.JacobianPoint
	point.X.Set(x)
	if !secp256k1.DecompressY(x, false, &point.Y) {
		return out, errors.New("ellswift: decoded x is not on the curve")
	}
	point.Z.SetInt(1)
	secp256k1.ScalarMultNonConst(&priv.Key, &point, &result)
	result.Z.Normalize()
	if result.Z.IsZero() {
		return out, errors.New("ellswift: ECDH result is the point at infinity")
	}
	result.ToAffine()
	result.X.Normalize()
	result.X.PutBytes(&out)
	return out, nil
}

// EllSwiftECDH is BIP324's v2_ecdh: the tagged hash of both encodings and the
// shared x coordinate. The initiator's encoding always comes first, so both sides
// derive the same secret.
func EllSwiftECDH(priv *secp256k1.PrivateKey, ours, theirs [EllSwiftPubKeySize]byte, initiating bool) ([32]byte, error) {
	shared, err := ellswiftECDHXOnly(theirs, priv)
	if err != nil {
		return [32]byte{}, err
	}
	if initiating {
		return taggedHash("bip324_ellswift_xonly_ecdh", ours[:], theirs[:], shared[:]), nil
	}
	return taggedHash("bip324_ellswift_xonly_ecdh", theirs[:], ours[:], shared[:]), nil
}

// taggedHash is SHA256(SHA256(tag) || SHA256(tag) || parts...), from BIP340.
func taggedHash(tag string, parts ...[]byte) [32]byte {
	tagHash := sha256.Sum256([]byte(tag))
	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])
	for _, p := range parts {
		h.Write(p)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
