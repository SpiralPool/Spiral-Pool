// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
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

// Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256, as the Stratum V2 specification
// (04-Protocol-Security) defines it:
//
//   - Public keys travel as 64-byte ElligatorSwift encodings, and DH results are
//     BIP324's x-only ECDH, hashed with both parties' encodings.
//   - The server proves its static key with a certificate
//     (SIGNATURE_NOISE_MESSAGE): a BIP340 signature by the pool's authority key
//     over the static key and a validity window.
//   - After the handshake each SV2 frame is sent as its encrypted 6-byte header,
//     then its payload encrypted in blocks of at most 65,519 bytes.
const (
	CipherKeySize = 32 // ChaCha20-Poly1305 key size
	HashSize      = 32 // SHA-256 hash size
	TagSize       = 16 // Poly1305 tag size

	// MaxNoiseMessageSize is the largest plaintext one Noise message carries.
	MaxNoiseMessageSize = 65535 - TagSize

	NoiseProtocolName = "Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256"

	// CertificateSize is SIGNATURE_NOISE_MESSAGE: version (U16), valid_from (U32),
	// not_valid_after (U32) and a 64-byte signature.
	CertificateSize = 2 + 4 + 4 + 64

	// Handshake message sizes: -> e, then <- e, ee, s, es, SIGNATURE_NOISE_MESSAGE.
	act1Size = EllSwiftPubKeySize
	act2Size = EllSwiftPubKeySize + (EllSwiftPubKeySize + TagSize) + (CertificateSize + TagSize)

	encryptedHeaderSize = HeaderSize + TagSize

	// The server signs a new certificate for every handshake, valid this long on
	// either side of its clock, so no certificate expires while the pool runs.
	certificateSkew = time.Hour
)

// NoiseError represents a Noise protocol error
type NoiseError struct {
	msg string
}

func (e *NoiseError) Error() string {
	return "noise: " + e.msg
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

// Split finalizes the handshake and returns two cipher states using HKDF: the
// first encrypts initiator-to-responder traffic, the second the other direction.
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

// newHandshakeState is InitializeSymmetric with the SV2 protocol name, followed by
// the empty prologue.
func newHandshakeState() *SymmetricState {
	ss := NewSymmetricState(NoiseProtocolName)
	ss.MixHash(nil)
	return ss
}

// Certificate is the SIGNATURE_NOISE_MESSAGE the server sends in the handshake.
type Certificate struct {
	Version       uint16
	ValidFrom     uint32 // unix time
	NotValidAfter uint32 // unix time
	Signature     [64]byte
}

func (c *Certificate) encode() []byte {
	out := make([]byte, CertificateSize)
	binary.LittleEndian.PutUint16(out[0:2], c.Version)
	binary.LittleEndian.PutUint32(out[2:6], c.ValidFrom)
	binary.LittleEndian.PutUint32(out[6:10], c.NotValidAfter)
	copy(out[10:], c.Signature[:])
	return out
}

func decodeCertificate(b []byte) (*Certificate, error) {
	if len(b) != CertificateSize {
		return nil, fmt.Errorf("certificate is %d bytes, want %d", len(b), CertificateSize)
	}
	c := &Certificate{
		Version:       binary.LittleEndian.Uint16(b[0:2]),
		ValidFrom:     binary.LittleEndian.Uint32(b[2:6]),
		NotValidAfter: binary.LittleEndian.Uint32(b[6:10]),
	}
	copy(c.Signature[:], b[10:])
	return c, nil
}

// certificateDigest is what the authority signs: SHA-256 over the version, the
// validity window and the server's x-only static public key.
func certificateDigest(c *Certificate, staticKey [32]byte) [32]byte {
	msg := make([]byte, 0, 10+32)
	msg = binary.LittleEndian.AppendUint16(msg, c.Version)
	msg = binary.LittleEndian.AppendUint32(msg, c.ValidFrom)
	msg = binary.LittleEndian.AppendUint32(msg, c.NotValidAfter)
	msg = append(msg, staticKey[:]...)
	return sha256.Sum256(msg)
}

// VerifyCertificate checks a server certificate for its x-only static key against
// the pool's authority public key at the given time.
func VerifyCertificate(c *Certificate, staticKey, authority [32]byte, now time.Time) error {
	if c.Version != 0 {
		return fmt.Errorf("unsupported certificate version %d", c.Version)
	}
	if ts := now.Unix(); ts < int64(c.ValidFrom) || ts > int64(c.NotValidAfter) {
		return fmt.Errorf("certificate valid %d-%d, not at %d", c.ValidFrom, c.NotValidAfter, ts)
	}
	digest := certificateDigest(c, staticKey)
	if !SchnorrVerify(authority, digest[:], c.Signature) {
		return errors.New("certificate is not signed by the pool authority key")
	}
	return nil
}

// ServerKeys are the server's static Noise key and the authority key that signs
// its certificates.
type ServerKeys struct {
	Static    *secp256k1.PrivateKey
	Authority *secp256k1.PrivateKey
}

// GenerateServerKeys makes new random static and authority keys. Miners cannot pin
// an authority key that changes on every start, so a pool loads persistent keys
// with LoadServerKeys.
func GenerateServerKeys() (*ServerKeys, error) {
	static, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	authority, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	return &ServerKeys{Static: static, Authority: authority}, nil
}

// AuthorityPublicKey is the x-only key miners and proxies configure to
// authenticate the pool.
func (k *ServerKeys) AuthorityPublicKey() [32]byte {
	return SchnorrPubKey(k.Authority)
}

func (k *ServerKeys) certificate(now time.Time) (*Certificate, error) {
	c := &Certificate{
		ValidFrom:     uint32(now.Add(-certificateSkew).Unix()),
		NotValidAfter: uint32(now.Add(certificateSkew).Unix()),
	}
	digest := certificateDigest(c, SchnorrPubKey(k.Static))
	var aux [32]byte
	if _, err := rand.Read(aux[:]); err != nil {
		return nil, err
	}
	sig, err := SchnorrSign(k.Authority, digest[:], aux)
	if err != nil {
		return nil, err
	}
	c.Signature = sig
	return c, nil
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

// ServerHandshake performs the responder side of the SV2 Noise NX handshake.
func ServerHandshake(conn net.Conn, keys *ServerKeys) (*NoiseConn, error) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	ss := newHandshakeState()

	// Act 1: -> e
	var re [EllSwiftPubKeySize]byte
	if _, err := io.ReadFull(conn, re[:]); err != nil {
		return nil, &NoiseError{"failed to read act 1: " + err.Error()}
	}
	ss.MixHash(re[:])
	if _, err := ss.DecryptAndHash(nil); err != nil {
		return nil, err
	}

	// Act 2: <- e, ee, s, es, SIGNATURE_NOISE_MESSAGE
	e, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, &NoiseError{"failed to generate ephemeral key: " + err.Error()}
	}
	eEnc, err := EllSwiftEncode(e.PubKey())
	if err != nil {
		return nil, &NoiseError{err.Error()}
	}
	ss.MixHash(eEnc[:])
	ee, err := EllSwiftECDH(e, eEnc, re, false)
	if err != nil {
		return nil, &NoiseError{"ee: " + err.Error()}
	}
	if err := ss.MixKey(ee); err != nil {
		return nil, err
	}

	sEnc, err := EllSwiftEncode(keys.Static.PubKey())
	if err != nil {
		return nil, &NoiseError{err.Error()}
	}
	encStatic, err := ss.EncryptAndHash(sEnc[:])
	if err != nil {
		return nil, &NoiseError{"failed to encrypt static key: " + err.Error()}
	}
	es, err := EllSwiftECDH(keys.Static, sEnc, re, false)
	if err != nil {
		return nil, &NoiseError{"es: " + err.Error()}
	}
	if err := ss.MixKey(es); err != nil {
		return nil, err
	}

	cert, err := keys.certificate(time.Now())
	if err != nil {
		return nil, &NoiseError{"failed to sign certificate: " + err.Error()}
	}
	encCert, err := ss.EncryptAndHash(cert.encode())
	if err != nil {
		return nil, &NoiseError{"failed to encrypt certificate: " + err.Error()}
	}

	act2 := make([]byte, 0, act2Size)
	act2 = append(act2, eEnc[:]...)
	act2 = append(act2, encStatic...)
	act2 = append(act2, encCert...)
	if _, err := conn.Write(act2); err != nil {
		return nil, &NoiseError{"failed to write act 2: " + err.Error()}
	}

	toServer, toClient, err := ss.Split()
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &NoiseConn{conn: conn, send: toClient, recv: toServer, isServer: true}, nil
}

// ClientHandshake performs the initiator side of the SV2 Noise NX handshake. With a
// non-nil authority key it verifies the server's certificate and fails the
// handshake if the certificate does not check out; with nil it accepts any server.
func ClientHandshake(conn net.Conn, authority *[32]byte) (*NoiseConn, *Certificate, error) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	ss := newHandshakeState()

	// Act 1: -> e
	e, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, &NoiseError{"failed to generate ephemeral key: " + err.Error()}
	}
	eEnc, err := EllSwiftEncode(e.PubKey())
	if err != nil {
		return nil, nil, &NoiseError{err.Error()}
	}
	ss.MixHash(eEnc[:])
	if _, err := ss.EncryptAndHash(nil); err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write(eEnc[:]); err != nil {
		return nil, nil, &NoiseError{"failed to write act 1: " + err.Error()}
	}

	// Act 2: <- e, ee, s, es, SIGNATURE_NOISE_MESSAGE
	act2 := make([]byte, act2Size)
	if _, err := io.ReadFull(conn, act2); err != nil {
		return nil, nil, &NoiseError{"failed to read act 2: " + err.Error()}
	}
	var re [EllSwiftPubKeySize]byte
	copy(re[:], act2[:EllSwiftPubKeySize])
	ss.MixHash(re[:])
	ee, err := EllSwiftECDH(e, eEnc, re, true)
	if err != nil {
		return nil, nil, &NoiseError{"ee: " + err.Error()}
	}
	if err := ss.MixKey(ee); err != nil {
		return nil, nil, err
	}

	staticEnd := EllSwiftPubKeySize + EllSwiftPubKeySize + TagSize
	staticBytes, err := ss.DecryptAndHash(act2[EllSwiftPubKeySize:staticEnd])
	if err != nil {
		return nil, nil, &NoiseError{"failed to decrypt static key: " + err.Error()}
	}
	var rs [EllSwiftPubKeySize]byte
	copy(rs[:], staticBytes)
	es, err := EllSwiftECDH(e, eEnc, rs, true)
	if err != nil {
		return nil, nil, &NoiseError{"es: " + err.Error()}
	}
	if err := ss.MixKey(es); err != nil {
		return nil, nil, err
	}

	certBytes, err := ss.DecryptAndHash(act2[staticEnd:])
	if err != nil {
		return nil, nil, &NoiseError{"failed to decrypt certificate: " + err.Error()}
	}
	cert, err := decodeCertificate(certBytes)
	if err != nil {
		return nil, nil, &NoiseError{err.Error()}
	}
	if authority != nil {
		if err := VerifyCertificate(cert, EllSwiftDecode(rs), *authority, time.Now()); err != nil {
			return nil, nil, &NoiseError{err.Error()}
		}
	}

	toServer, toClient, err := ss.Split()
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &NoiseConn{conn: conn, send: toServer, recv: toClient}, cert, nil
}

// Read returns the decrypted SV2 frames as a byte stream.
func (nc *NoiseConn) Read(b []byte) (int, error) {
	nc.readMu.Lock()
	defer nc.readMu.Unlock()

	if nc.readPos >= len(nc.readBuf) {
		frame, err := nc.readFrame()
		if err != nil {
			return 0, err
		}
		nc.readBuf, nc.readPos = frame, 0
	}
	n := copy(b, nc.readBuf[nc.readPos:])
	nc.readPos += n
	return n, nil
}

// readFrame reads and decrypts one frame: the 22-byte encrypted header, then the
// payload blocks its length calls for.
func (nc *NoiseConn) readFrame() ([]byte, error) {
	var encHeader [encryptedHeaderSize]byte
	if _, err := io.ReadFull(nc.conn, encHeader[:]); err != nil {
		return nil, err
	}
	header, err := nc.recv.Decrypt(nil, encHeader[:])
	if err != nil {
		return nil, err
	}
	length := int(header[3]) | int(header[4])<<8 | int(header[5])<<16
	if length > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}

	frame := make([]byte, HeaderSize, HeaderSize+length)
	copy(frame, header)
	for remaining := length; remaining > 0; {
		block := remaining
		if block > MaxNoiseMessageSize {
			block = MaxNoiseMessageSize
		}
		ciphertext := make([]byte, block+TagSize)
		if _, err := io.ReadFull(nc.conn, ciphertext); err != nil {
			return nil, err
		}
		plaintext, err := nc.recv.Decrypt(nil, ciphertext)
		if err != nil {
			return nil, err
		}
		frame = append(frame, plaintext...)
		remaining -= block
	}
	return frame, nil
}

// Write encrypts and sends one complete SV2 frame: a 6-byte header and exactly the
// payload its length field gives.
func (nc *NoiseConn) Write(b []byte) (int, error) {
	nc.writeMu.Lock()
	defer nc.writeMu.Unlock()

	if len(b) < HeaderSize {
		return 0, errors.New("noise: frame shorter than its header")
	}
	length := int(b[3]) | int(b[4])<<8 | int(b[5])<<16
	if len(b) != HeaderSize+length {
		return 0, fmt.Errorf("noise: frame is %d bytes but its header gives %d", len(b), HeaderSize+length)
	}

	blocks := (length + MaxNoiseMessageSize - 1) / MaxNoiseMessageSize
	out := make([]byte, 0, encryptedHeaderSize+length+blocks*TagSize)
	encHeader, err := nc.send.Encrypt(nil, b[:HeaderSize])
	if err != nil {
		return 0, err
	}
	out = append(out, encHeader...)
	for payload := b[HeaderSize:]; len(payload) > 0; {
		block := payload
		if len(block) > MaxNoiseMessageSize {
			block = block[:MaxNoiseMessageSize]
		}
		ciphertext, err := nc.send.Encrypt(nil, block)
		if err != nil {
			return 0, err
		}
		out = append(out, ciphertext...)
		payload = payload[len(block):]
	}

	// One write per frame, so a partial send cannot interleave with another frame.
	if _, err := nc.conn.Write(out); err != nil {
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
