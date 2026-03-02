// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"golang.org/x/crypto/chacha20poly1305"
)

// Noise Protocol constants per SV2 specification:
//   - DH: secp256k1 ECDH (compressed 33-byte public keys)
//   - Cipher: ChaCha20-Poly1305 IETF (12-byte nonce)
//   - Hash: SHA-256
//   - KDF: HKDF with HMAC-SHA256
const (
	// Key sizes
	DHPrivKeySize = 32 // secp256k1 private key (scalar)
	DHPubKeySize  = 33 // secp256k1 compressed public key
	CipherKeySize = 32 // ChaCha20-Poly1305 key size
	HashSize      = 32 // SHA-256 hash size
	TagSize       = 16 // Poly1305 tag size

	// Maximum message sizes
	MaxNoiseMessageSize = 65535 - TagSize

	// Protocol name for hashing (Noise NX pattern with secp256k1)
	NoiseProtocolName = "Noise_NX_secp256k1_ChaChaPoly_SHA256"
)

// NoiseError represents a Noise protocol error
type NoiseError struct {
	msg string
}

func (e *NoiseError) Error() string {
	return "noise: " + e.msg
}

// keypair represents a secp256k1 key pair
type keypair struct {
	private [DHPrivKeySize]byte
	public  [DHPubKeySize]byte
}

// generateKeypair generates a new secp256k1 key pair
func generateKeypair() (*keypair, error) {
	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	kp := &keypair{}
	copy(kp.private[:], privKey.Serialize())
	pubBytes := privKey.PubKey().SerializeCompressed()
	copy(kp.public[:], pubBytes)
	return kp, nil
}

// dhSecp256k1 performs secp256k1 ECDH: shared_secret = x-coordinate(privKey * pubKey)
func dhSecp256k1(privateKey [DHPrivKeySize]byte, publicKey [DHPubKeySize]byte) ([HashSize]byte, error) {
	privKey := secp256k1.PrivKeyFromBytes(privateKey[:])
	pubKey, err := secp256k1.ParsePubKey(publicKey[:])
	if err != nil {
		return [HashSize]byte{}, fmt.Errorf("invalid secp256k1 public key: %w", err)
	}

	// ECDH: scalar multiply pubKey by privKey, take x-coordinate
	var pubJacobian secp256k1.JacobianPoint
	pubKey.AsJacobian(&pubJacobian)

	var result secp256k1.JacobianPoint
	secp256k1.ScalarMultNonConst(&privKey.Key, &pubJacobian, &result)
	result.ToAffine()

	// Check for point at infinity (invalid DH output)
	var shared [HashSize]byte
	if (result.X.IsZero() && result.Y.IsZero()) || result.Z.IsZero() {
		return shared, errors.New("ECDH produced point at infinity")
	}

	result.X.PutBytesUnchecked(shared[:])
	return shared, nil
}

// CipherState holds the symmetric encryption state
// SECURITY: All methods are protected by mutex to prevent nonce reuse race conditions
// Nonce reuse in ChaCha20-Poly1305 leads to complete cipher break
type CipherState struct {
	mu    sync.Mutex // Protects nonce counter from concurrent access
	key   [CipherKeySize]byte
	nonce uint64
	aead  cipher.AEAD
}

// NewCipherState creates a new CipherState with the given key
// Uses standard ChaCha20-Poly1305 (IETF, 12-byte nonce) per SV2 spec
func NewCipherState(key [CipherKeySize]byte) (*CipherState, error) {
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	return &CipherState{
		key:   key,
		nonce: 0,
		aead:  aead,
	}, nil
}

// Encrypt encrypts plaintext with optional additional data
// SECURITY: Mutex protects against nonce reuse from concurrent calls
// Nonce format per SV2 spec: 4 zero bytes || 8-byte LE counter
// #nosec G407 -- Nonce is derived from counter (cs.nonce), not hardcoded per Noise Protocol spec
func (cs *CipherState) Encrypt(ad, plaintext []byte) ([]byte, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.nonce == math.MaxUint64 {
		return nil, fmt.Errorf("nonce overflow: maximum message count exceeded")
	}

	nonce := make([]byte, chacha20poly1305.NonceSize) // 12 bytes
	// First 4 bytes are zero (per Noise/SV2 spec), last 8 bytes are LE counter
	binary.LittleEndian.PutUint64(nonce[4:], cs.nonce)
	cs.nonce++
	return cs.aead.Seal(nil, nonce, plaintext, ad), nil
}

// Decrypt decrypts ciphertext with optional additional data
// SECURITY: Mutex protects against nonce reuse from concurrent calls
func (cs *CipherState) Decrypt(ad, ciphertext []byte) ([]byte, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.nonce == math.MaxUint64 {
		return nil, fmt.Errorf("nonce overflow: maximum message count exceeded")
	}

	nonce := make([]byte, chacha20poly1305.NonceSize) // 12 bytes
	binary.LittleEndian.PutUint64(nonce[4:], cs.nonce)
	cs.nonce++
	return cs.aead.Open(nil, nonce, ciphertext, ad)
}

// SymmetricState holds the handshake hash state
type SymmetricState struct {
	h  [HashSize]byte // Chaining hash
	ck [HashSize]byte // Chaining key
	cs *CipherState
}

// NewSymmetricState initializes the symmetric state per Noise spec:
// If len(protocol_name) <= HASHLEN, set h = protocol_name zero-padded to HASHLEN
// Otherwise set h = HASH(protocol_name)
// Set ck = h
func NewSymmetricState(protocolName string) *SymmetricState {
	ss := &SymmetricState{}

	if len(protocolName) <= HashSize {
		copy(ss.h[:], protocolName)
		copy(ss.ck[:], protocolName)
	} else {
		hash := sha256.Sum256([]byte(protocolName))
		ss.h = hash
		ss.ck = hash
	}

	return ss
}

// MixHash mixes data into the hash: h = SHA-256(h || data)
func (ss *SymmetricState) MixHash(data []byte) {
	hasher := sha256.New()
	hasher.Write(ss.h[:])
	hasher.Write(data)
	copy(ss.h[:], hasher.Sum(nil))
}

// hkdf2 implements HKDF with 2 outputs per the Noise spec:
//
//	temp_key = HMAC-SHA256(chaining_key, input_key_material)
//	output1  = HMAC-SHA256(temp_key, 0x01)
//	output2  = HMAC-SHA256(temp_key, output1 || 0x02)
func hkdf2(chainingKey [HashSize]byte, inputKeyMaterial []byte) (ck [HashSize]byte, k [CipherKeySize]byte) {
	// HKDF-Extract
	mac := hmac.New(sha256.New, chainingKey[:])
	mac.Write(inputKeyMaterial)
	tempKey := mac.Sum(nil)

	// HKDF-Expand: output1
	mac = hmac.New(sha256.New, tempKey)
	mac.Write([]byte{0x01})
	output1 := mac.Sum(nil)
	copy(ck[:], output1)

	// HKDF-Expand: output2
	mac = hmac.New(sha256.New, tempKey)
	mac.Write(output1)
	mac.Write([]byte{0x02})
	output2 := mac.Sum(nil)
	copy(k[:], output2)

	return ck, k
}

// MixKey mixes a DH result into the key using HKDF per Noise spec
func (ss *SymmetricState) MixKey(dhResult [HashSize]byte) error {
	newCK, cipherKey := hkdf2(ss.ck, dhResult[:])
	ss.ck = newCK

	var err error
	ss.cs, err = NewCipherState(cipherKey)
	return err
}

// EncryptAndHash encrypts and mixes into hash
func (ss *SymmetricState) EncryptAndHash(plaintext []byte) ([]byte, error) {
	if ss.cs == nil {
		// No cipher yet, just mix hash
		ss.MixHash(plaintext)
		return plaintext, nil
	}
	ciphertext, err := ss.cs.Encrypt(ss.h[:], plaintext)
	if err != nil {
		return nil, err
	}
	ss.MixHash(ciphertext)
	return ciphertext, nil
}

// DecryptAndHash decrypts and mixes into hash
func (ss *SymmetricState) DecryptAndHash(ciphertext []byte) ([]byte, error) {
	if ss.cs == nil {
		// No cipher yet, just mix hash
		ss.MixHash(ciphertext)
		return ciphertext, nil
	}
	plaintext, err := ss.cs.Decrypt(ss.h[:], ciphertext)
	if err != nil {
		return nil, err
	}
	ss.MixHash(ciphertext)
	return plaintext, nil
}

// Split finalizes the handshake and returns two cipher states using HKDF
func (ss *SymmetricState) Split() (*CipherState, *CipherState, error) {
	key1, key2 := hkdf2(ss.ck, nil)

	cs1, err := NewCipherState(key1)
	if err != nil {
		return nil, nil, err
	}
	cs2, err := NewCipherState(key2)
	if err != nil {
		return nil, nil, err
	}

	return cs1, cs2, nil
}

// NoiseConn wraps a net.Conn with Noise encryption
// Uses separate mutexes for read and write to allow bidirectional traffic
type NoiseConn struct {
	conn     net.Conn
	send     *CipherState
	recv     *CipherState
	readBuf  []byte
	readPos  int
	readMu   sync.Mutex // Protects recv cipher + read buffer
	writeMu  sync.Mutex // Protects send cipher
	isServer bool
}

// ServerKeys holds the server's static secp256k1 key pair
type ServerKeys struct {
	Private [DHPrivKeySize]byte
	Public  [DHPubKeySize]byte
}

// GenerateServerKeys generates a new server secp256k1 key pair
func GenerateServerKeys() (*ServerKeys, error) {
	kp, err := generateKeypair()
	if err != nil {
		return nil, err
	}
	return &ServerKeys{
		Private: kp.private,
		Public:  kp.public,
	}, nil
}

// ServerHandshake performs the server-side Noise NX handshake using secp256k1
func ServerHandshake(conn net.Conn, serverKeys *ServerKeys) (*NoiseConn, error) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	ss := NewSymmetricState(NoiseProtocolName)

	// --- Act 1: Receive initiator's ephemeral public key (33 bytes compressed) ---
	var ephemeralPub [DHPubKeySize]byte
	if _, err := io.ReadFull(conn, ephemeralPub[:]); err != nil {
		return nil, &NoiseError{"failed to read ephemeral key: " + err.Error()}
	}

	// Validate the received key is a valid secp256k1 point
	if _, err := secp256k1.ParsePubKey(ephemeralPub[:]); err != nil {
		return nil, &NoiseError{"invalid initiator ephemeral key: " + err.Error()}
	}

	ss.MixHash(ephemeralPub[:])

	// --- Act 2: Send responder's ephemeral + encrypted static ---
	responderEphemeral, err := generateKeypair()
	if err != nil {
		return nil, &NoiseError{"failed to generate ephemeral key: " + err.Error()}
	}

	// Mix ephemeral public key
	ss.MixHash(responderEphemeral.public[:])

	// DH(ephemeral, initiator_ephemeral)
	dh1, err := dhSecp256k1(responderEphemeral.private, ephemeralPub)
	if err != nil {
		return nil, &NoiseError{"DH1 failed: " + err.Error()}
	}
	if err := ss.MixKey(dh1); err != nil {
		return nil, err
	}

	// Encrypt and send responder's static public key
	encryptedStatic, err := ss.EncryptAndHash(serverKeys.Public[:])
	if err != nil {
		return nil, &NoiseError{"failed to encrypt static key: " + err.Error()}
	}

	// DH(static, initiator_ephemeral)
	dh2, err := dhSecp256k1(serverKeys.Private, ephemeralPub)
	if err != nil {
		return nil, &NoiseError{"DH2 failed: " + err.Error()}
	}
	if err := ss.MixKey(dh2); err != nil {
		return nil, err
	}

	// Send: ephemeral_pub || encrypted_static
	act2 := make([]byte, 0, DHPubKeySize+len(encryptedStatic))
	act2 = append(act2, responderEphemeral.public[:]...)
	act2 = append(act2, encryptedStatic...)

	if _, err := conn.Write(act2); err != nil {
		return nil, &NoiseError{"failed to write act 2: " + err.Error()}
	}

	// Split into send/receive cipher states
	// Client sends on key1, receives on key2
	// Server sends on key2, receives on key1
	cs1, cs2, err := ss.Split()
	if err != nil {
		return nil, err
	}

	_ = conn.SetDeadline(time.Time{})

	return &NoiseConn{
		conn:     conn,
		send:     cs2, // Server sends on key2
		recv:     cs1, // Server receives on key1
		isServer: true,
	}, nil
}

// ClientHandshake performs the client-side Noise NX handshake using secp256k1
func ClientHandshake(conn net.Conn) (*NoiseConn, [DHPubKeySize]byte, error) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	ss := NewSymmetricState(NoiseProtocolName)

	// --- Act 1: Send initiator's ephemeral public key (33 bytes compressed) ---
	initiatorEphemeral, err := generateKeypair()
	if err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"failed to generate ephemeral key: " + err.Error()}
	}

	ss.MixHash(initiatorEphemeral.public[:])

	if _, err := conn.Write(initiatorEphemeral.public[:]); err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"failed to write act 1: " + err.Error()}
	}

	// --- Act 2: Receive responder's ephemeral + encrypted static ---
	// Act 2 size: 33 (ephemeral) + 33 (static) + 16 (tag) = 82 bytes
	act2 := make([]byte, DHPubKeySize+DHPubKeySize+TagSize)
	if _, err := io.ReadFull(conn, act2); err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"failed to read act 2: " + err.Error()}
	}

	var responderEphemeralPub [DHPubKeySize]byte
	copy(responderEphemeralPub[:], act2[:DHPubKeySize])
	encryptedStatic := act2[DHPubKeySize:]

	// Validate the received ephemeral key
	if _, err := secp256k1.ParsePubKey(responderEphemeralPub[:]); err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"invalid responder ephemeral key: " + err.Error()}
	}

	// Mix ephemeral
	ss.MixHash(responderEphemeralPub[:])

	// DH(initiator_ephemeral, responder_ephemeral)
	dh1, err := dhSecp256k1(initiatorEphemeral.private, responderEphemeralPub)
	if err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"DH1 failed: " + err.Error()}
	}
	if err := ss.MixKey(dh1); err != nil {
		return nil, [DHPubKeySize]byte{}, err
	}

	// Decrypt responder's static key
	staticBytes, err := ss.DecryptAndHash(encryptedStatic)
	if err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"failed to decrypt static key: " + err.Error()}
	}

	var responderStaticPub [DHPubKeySize]byte
	copy(responderStaticPub[:], staticBytes)

	// Validate the decrypted static key
	if _, err := secp256k1.ParsePubKey(responderStaticPub[:]); err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"invalid responder static key: " + err.Error()}
	}

	// DH(initiator_ephemeral, responder_static)
	dh2, err := dhSecp256k1(initiatorEphemeral.private, responderStaticPub)
	if err != nil {
		return nil, [DHPubKeySize]byte{}, &NoiseError{"DH2 failed: " + err.Error()}
	}
	if err := ss.MixKey(dh2); err != nil {
		return nil, [DHPubKeySize]byte{}, err
	}

	// Split into send/receive cipher states
	cs1, cs2, err := ss.Split()
	if err != nil {
		return nil, [DHPubKeySize]byte{}, err
	}

	_ = conn.SetDeadline(time.Time{})

	return &NoiseConn{
		conn:     conn,
		send:     cs1, // Client sends on key1
		recv:     cs2, // Client receives on key2
		isServer: false,
	}, responderStaticPub, nil
}

// Read reads and decrypts data (uses readMu — independent from Write)
func (nc *NoiseConn) Read(b []byte) (int, error) {
	nc.readMu.Lock()
	defer nc.readMu.Unlock()

	// If we have buffered data, return it first
	if len(nc.readBuf) > nc.readPos {
		n := copy(b, nc.readBuf[nc.readPos:])
		nc.readPos += n
		if nc.readPos >= len(nc.readBuf) {
			nc.readBuf = nil
			nc.readPos = 0
		}
		return n, nil
	}

	// Read length prefix (2 bytes)
	var lengthBuf [2]byte
	if _, err := io.ReadFull(nc.conn, lengthBuf[:]); err != nil {
		return 0, err
	}
	length := binary.LittleEndian.Uint16(lengthBuf[:])

	if length == 0 {
		return 0, nil
	}

	// Read ciphertext
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(nc.conn, ciphertext); err != nil {
		return 0, err
	}

	// Decrypt
	plaintext, err := nc.recv.Decrypt(nil, ciphertext)
	if err != nil {
		return 0, err
	}

	// Copy to output buffer
	n := copy(b, plaintext)
	if n < len(plaintext) {
		// Buffer the rest
		nc.readBuf = plaintext
		nc.readPos = n
	}

	return n, nil
}

// Write encrypts and writes data (uses writeMu — independent from Read)
func (nc *NoiseConn) Write(b []byte) (int, error) {
	nc.writeMu.Lock()
	defer nc.writeMu.Unlock()

	if len(b) > MaxNoiseMessageSize {
		return 0, errors.New("message too large")
	}

	// Encrypt
	ciphertext, err := nc.send.Encrypt(nil, b)
	if err != nil {
		return 0, err
	}

	// Combine length prefix + ciphertext into a single write to prevent
	// partial sends that could corrupt framing
	frame := make([]byte, 2+len(ciphertext))
	binary.LittleEndian.PutUint16(frame[:2], uint16(len(ciphertext)))
	copy(frame[2:], ciphertext)

	if _, err := nc.conn.Write(frame); err != nil {
		return 0, err
	}

	return len(b), nil
}

// Close closes the underlying connection
func (nc *NoiseConn) Close() error {
	return nc.conn.Close()
}

// LocalAddr returns the local network address
func (nc *NoiseConn) LocalAddr() net.Addr {
	return nc.conn.LocalAddr()
}

// RemoteAddr returns the remote network address
func (nc *NoiseConn) RemoteAddr() net.Addr {
	return nc.conn.RemoteAddr()
}
