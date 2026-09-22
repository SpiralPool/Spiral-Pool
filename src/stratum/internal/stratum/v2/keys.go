// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// A pool keeps two long-lived keys, each in a file holding the private key as 64
// hex characters:
//
//   - authority.key signs the server's certificates. Miners and proxies configure
//     its public half to authenticate the pool, so it must not change.
//   - static.key is the server's Noise static key.
const (
	AuthorityKeyFile = "authority.key"
	StaticKeyFile    = "static.key"
)

// ReadKeyFile reads a private key file.
func ReadKeyFile(path string) (*secp256k1.PrivateKey, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator-configured key path
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("%s: not a 32-byte private key in hex", path)
	}
	var scalar secp256k1.ModNScalar
	if overflow := scalar.SetByteSlice(raw); overflow || scalar.IsZero() {
		return nil, fmt.Errorf("%s: private key out of range", path)
	}
	return secp256k1.NewPrivateKey(&scalar), nil
}

// WriteKeyFile creates a key file readable only by its owner. It never replaces an
// existing file.
func WriteKeyFile(path string, key *secp256k1.PrivateKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%x\n", key.Serialize())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// LoadOrCreateKey reads a key file, creating it with a new random key when it does
// not exist. created reports whether this call made it.
func LoadOrCreateKey(path string) (key *secp256k1.PrivateKey, created bool, err error) {
	key, err = ReadKeyFile(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return key, false, err
	}
	key, err = secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, false, err
	}
	if err := WriteKeyFile(path, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another coin pool created it first; use theirs.
			key, err = ReadKeyFile(path)
			return key, false, err
		}
		return nil, false, err
	}
	return key, true, nil
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// EncodeAuthorityKey formats an authority public key as the Stratum Reference
// Implementation's configuration files expect it: base58check over a little-endian
// U16 key version of 1 followed by the 32-byte x-only key.
func EncodeAuthorityKey(pub [32]byte) string {
	payload := append([]byte{1, 0}, pub[:]...)
	first := sha256.Sum256(payload)
	check := sha256.Sum256(first[:])
	payload = append(payload, check[:4]...)

	n := new(big.Int).SetBytes(payload)
	base, mod := big.NewInt(58), new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range payload {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// LoadServerKeys loads the authority and static keys from dir, creating any that
// are missing. created lists the files this call made.
func LoadServerKeys(dir string) (keys *ServerKeys, created []string, err error) {
	keys = &ServerKeys{}
	for _, item := range []struct {
		name string
		dst  **secp256k1.PrivateKey
	}{{AuthorityKeyFile, &keys.Authority}, {StaticKeyFile, &keys.Static}} {
		path := filepath.Join(dir, item.name)
		key, made, err := LoadOrCreateKey(path)
		if err != nil {
			return nil, nil, err
		}
		*item.dst = key
		if made {
			created = append(created, path)
		}
	}
	return keys, created, nil
}
