// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 provides tests for the Noise transport.
//
// These tests validate:
// - ChaCha20-Poly1305 IETF encryption/decryption and the symmetric state
// - the SV2 NX handshake: message sizes, the authority certificate, tampering
// - frame encryption: a separately encrypted header and 65,519-byte payload blocks
package v2

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCipherState validates symmetric encryption.
func TestCipherState(t *testing.T) {
	var key [CipherKeySize]byte
	rand.Read(key[:])

	cs, err := NewCipherState(key)
	if err != nil {
		t.Fatalf("NewCipherState failed: %v", err)
	}

	// Test encryption/decryption
	plaintext := []byte("Hello, Stratum V2!")
	ad := []byte("additional data")

	ciphertext, err := cs.Encrypt(ad, plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Ciphertext should be longer (includes tag)
	if len(ciphertext) <= len(plaintext) {
		t.Error("ciphertext should be longer than plaintext")
	}

	// Create new cipher state for decryption (reset nonce)
	cs2, err := NewCipherState(key)
	if err != nil {
		t.Fatalf("NewCipherState failed: %v", err)
	}

	decrypted, err := cs2.Decrypt(ad, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

// TestCipherStateNonceIncrement validates nonce increments.
func TestCipherStateNonceIncrement(t *testing.T) {
	var key [CipherKeySize]byte
	rand.Read(key[:])

	cs, _ := NewCipherState(key)

	plaintext := []byte("test")
	ct1, _ := cs.Encrypt(nil, plaintext)
	ct2, _ := cs.Encrypt(nil, plaintext)

	// Same plaintext should produce different ciphertext due to nonce increment
	if bytes.Equal(ct1, ct2) {
		t.Error("same plaintext should produce different ciphertext")
	}
}

// TestCipherStateWrongAD validates AD authentication.
func TestCipherStateWrongAD(t *testing.T) {
	var key [CipherKeySize]byte
	rand.Read(key[:])

	cs1, _ := NewCipherState(key)
	cs2, _ := NewCipherState(key)

	plaintext := []byte("secret data")
	ad1 := []byte("correct AD")
	ad2 := []byte("wrong AD")

	ciphertext, _ := cs1.Encrypt(ad1, plaintext)

	// Decryption with wrong AD should fail
	_, err := cs2.Decrypt(ad2, ciphertext)
	if err == nil {
		t.Error("decryption with wrong AD should fail")
	}
}

// TestSymmetricState validates handshake state.
func TestSymmetricState(t *testing.T) {
	ss := NewSymmetricState(NoiseProtocolName)

	// Initial hash should not be all zeros
	allZero := true
	for _, b := range ss.h {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("initial hash is all zeros")
	}

	// MixHash should change the hash
	oldH := ss.h
	ss.MixHash([]byte("test data"))
	if bytes.Equal(oldH[:], ss.h[:]) {
		t.Error("MixHash should change the hash")
	}
}

// TestSymmetricStateMixKey validates key mixing.
func TestSymmetricStateMixKey(t *testing.T) {
	ss := NewSymmetricState(NoiseProtocolName)

	var dhResult [HashSize]byte
	rand.Read(dhResult[:])

	oldCK := ss.ck
	err := ss.MixKey(dhResult)
	if err != nil {
		t.Fatalf("MixKey failed: %v", err)
	}

	// Chaining key should change
	if bytes.Equal(oldCK[:], ss.ck[:]) {
		t.Error("MixKey should change chaining key")
	}

	// Cipher state should be created
	if ss.cs == nil {
		t.Error("MixKey should create cipher state")
	}
}

// TestSymmetricStateSplit validates key derivation.
func TestSymmetricStateSplit(t *testing.T) {
	ss := NewSymmetricState(NoiseProtocolName)

	// Need to mix a key first to initialize state
	var dhResult [HashSize]byte
	rand.Read(dhResult[:])
	ss.MixKey(dhResult)

	cs1, cs2, err := ss.Split()
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	if cs1 == nil || cs2 == nil {
		t.Error("Split should return two cipher states")
	}

	// The two cipher states should have different keys
	if bytes.Equal(cs1.key[:], cs2.key[:]) {
		t.Error("Split should produce different keys")
	}
}

// countingConn records what a connection carries.
type countingConn struct {
	net.Conn
	mu     sync.Mutex
	read   int
	writes []int
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.mu.Lock()
	c.read += n
	c.mu.Unlock()
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.mu.Lock()
	c.writes = append(c.writes, n)
	c.mu.Unlock()
	return n, err
}

func (c *countingConn) counts() (int, []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read, append([]int(nil), c.writes...)
}

type noisePair struct {
	server, client *NoiseConn
	cert           *Certificate
	clientWire     *countingConn
	serverRaw      net.Conn
}

// handshakeOver runs the server handshake on one end of serverRaw/clientRaw and the
// client handshake on the other.
func handshakeOver(t *testing.T, serverRaw, clientRaw net.Conn, keys *ServerKeys, authority *[32]byte) (*noisePair, error) {
	t.Helper()
	wire := &countingConn{Conn: clientRaw}
	type result struct {
		nc  *NoiseConn
		err error
	}
	serverCh := make(chan result, 1)
	go func() {
		nc, err := ServerHandshake(serverRaw, keys)
		serverCh <- result{nc, err}
	}()

	client, cert, err := ClientHandshake(wire, authority)
	if err != nil {
		_ = serverRaw.Close()
		<-serverCh
		return nil, err
	}
	select {
	case res := <-serverCh:
		if res.err != nil {
			return nil, res.err
		}
		return &noisePair{server: res.nc, client: client, cert: cert, clientWire: wire, serverRaw: serverRaw}, nil
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake timed out")
		return nil, nil
	}
}

func pipeHandshake(t *testing.T, keys *ServerKeys, authority *[32]byte) (*noisePair, error) {
	t.Helper()
	serverRaw, clientRaw := net.Pipe()
	t.Cleanup(func() {
		_ = serverRaw.Close()
		_ = clientRaw.Close()
	})
	return handshakeOver(t, serverRaw, clientRaw, keys, authority)
}

// readFrameFrom reads one SV2 frame (header and payload) from a decrypting reader.
func readFrameFrom(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var header MessageHeader
	headerBytes := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		t.Fatalf("read header: %v", err)
	}
	if err := header.Decode(bytes.NewReader(headerBytes)); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, header.Length)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return append(headerBytes, payload...)
}

func TestNoiseHandshake_AuthenticatesServerAndEncryptsBothWays(t *testing.T) {
	keys, err := GenerateServerKeys()
	if err != nil {
		t.Fatal(err)
	}
	authority := keys.AuthorityPublicKey()
	p, err := pipeHandshake(t, keys, &authority)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	read, writes := p.clientWire.counts()
	if len(writes) != 1 || writes[0] != 64 {
		t.Errorf("client handshake writes = %v, want one 64-byte act 1", writes)
	}
	if read != 234 {
		t.Errorf("client read %d handshake bytes, want 234 (act 2)", read)
	}
	if now := time.Now().Unix(); p.cert.Version != 0 || now < int64(p.cert.ValidFrom) || now > int64(p.cert.NotValidAfter) {
		t.Errorf("certificate = %+v, want version 0 valid now", p.cert)
	}

	toServer := EncodeMessage(MsgSetupConnection, []byte("client to server"))
	go func() { _, _ = p.client.Write(toServer) }()
	if got := readFrameFrom(t, p.server); !bytes.Equal(got, toServer) {
		t.Errorf("server received %x, want %x", got, toServer)
	}

	toClient := EncodeMessage(MsgSetupConnectionSuccess, []byte("server to client"))
	go func() { _, _ = p.server.Write(toClient) }()
	if got := readFrameFrom(t, p.client); !bytes.Equal(got, toClient) {
		t.Errorf("client received %x, want %x", got, toClient)
	}
}

func TestNoiseHandshake_ClientRejectsServerNotSignedByAuthority(t *testing.T) {
	keys, _ := GenerateServerKeys()
	impostor, _ := GenerateServerKeys()
	authority := impostor.AuthorityPublicKey()
	if _, err := pipeHandshake(t, keys, &authority); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("handshake error = %v, want a rejected certificate", err)
	}
}

func TestNoiseHandshake_WithoutAuthorityAcceptsAnyServer(t *testing.T) {
	keys, _ := GenerateServerKeys()
	if _, err := pipeHandshake(t, keys, nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}
}

// Flipping any bit of act 2 must break the handshake: the static key and the
// certificate are authenticated against the transcript.
func TestNoiseHandshake_TamperedAct2Fails(t *testing.T) {
	for _, pos := range []int{10, 64 + 5, 64 + 80 + 20, 233} {
		keys, _ := GenerateServerKeys()
		serverRaw, relayToServer := net.Pipe()
		relayToClient, clientRaw := net.Pipe()
		t.Cleanup(func() {
			for _, c := range []net.Conn{serverRaw, relayToServer, relayToClient, clientRaw} {
				_ = c.Close()
			}
		})
		go func() {
			act1 := make([]byte, 64)
			if _, err := io.ReadFull(relayToClient, act1); err != nil {
				return
			}
			_, _ = relayToServer.Write(act1)
			act2 := make([]byte, 234)
			if _, err := io.ReadFull(relayToServer, act2); err != nil {
				return
			}
			act2[pos] ^= 0x01
			_, _ = relayToClient.Write(act2)
		}()

		authority := keys.AuthorityPublicKey()
		if _, err := handshakeOver(t, serverRaw, clientRaw, keys, &authority); err == nil {
			t.Errorf("handshake with act 2 byte %d flipped succeeded", pos)
		}
	}
}

func TestVerifyCertificate(t *testing.T) {
	keys, _ := GenerateServerKeys()
	now := time.Now()
	cert, err := keys.certificate(now)
	if err != nil {
		t.Fatal(err)
	}
	static := SchnorrPubKey(keys.Static)
	authority := keys.AuthorityPublicKey()

	if err := VerifyCertificate(cert, static, authority, now); err != nil {
		t.Fatalf("valid certificate rejected: %v", err)
	}
	if VerifyCertificate(cert, static, authority, now.Add(2*certificateSkew)) == nil {
		t.Error("expired certificate accepted")
	}
	if VerifyCertificate(cert, static, authority, now.Add(-2*certificateSkew)) == nil {
		t.Error("not-yet-valid certificate accepted")
	}
	other, _ := GenerateServerKeys()
	if VerifyCertificate(cert, SchnorrPubKey(other.Static), authority, now) == nil {
		t.Error("certificate accepted for a different static key")
	}
	future := *cert
	future.Version = 1
	if VerifyCertificate(&future, static, authority, now) == nil {
		t.Error("unknown certificate version accepted")
	}
	stretched := *cert
	stretched.NotValidAfter += 3600
	if VerifyCertificate(&stretched, static, authority, now) == nil {
		t.Error("certificate with an altered validity window accepted")
	}
}

// A frame is sent as a 22-byte encrypted header and payload blocks of at most
// 65,519 bytes, each with its own tag.
func TestNoiseFrame_WireLayout(t *testing.T) {
	keys, _ := GenerateServerKeys()
	p, err := pipeHandshake(t, keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := p.clientWire.counts()

	payload := make([]byte, 70000)
	rand.Read(payload)
	frame := EncodeMessage(MsgNewExtendedMiningJob, payload)
	go func() { _, _ = p.server.Write(frame) }()

	if got := readFrameFrom(t, p.client); !bytes.Equal(got, frame) {
		t.Fatal("large frame corrupted in transit")
	}
	after, _ := p.clientWire.counts()
	want := 22 + (MaxNoiseMessageSize + TagSize) + (70000 - MaxNoiseMessageSize + TagSize)
	if after-before != want {
		t.Errorf("frame took %d bytes on the wire, want %d", after-before, want)
	}
}

func TestNoiseFrame_ManyFramesInOrder(t *testing.T) {
	keys, _ := GenerateServerKeys()
	p, err := pipeHandshake(t, keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	frames := [][]byte{
		EncodeMessage(MsgSubmitSharesStandard, []byte("first")),
		EncodeMessage(MsgUpdateChannel, nil),
		EncodeMessage(MsgCloseChannel, []byte("third frame with more data")),
	}
	go func() {
		for _, f := range frames {
			_, _ = p.client.Write(f)
		}
	}()
	for i, want := range frames {
		if got := readFrameFrom(t, p.server); !bytes.Equal(got, want) {
			t.Errorf("frame %d = %x, want %x", i, got, want)
		}
	}
}

func TestNoiseWrite_RejectsIncompleteFrames(t *testing.T) {
	keys, _ := GenerateServerKeys()
	p, err := pipeHandshake(t, keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.client.Write([]byte{1, 2, 3}); err == nil {
		t.Error("write of a partial header succeeded")
	}
	frame := EncodeMessage(MsgSetupConnection, []byte("payload"))
	if _, err := p.client.Write(frame[:len(frame)-1]); err == nil {
		t.Error("write of a frame shorter than its header's length succeeded")
	}
}

func TestNoiseRead_RejectsOversizeFrame(t *testing.T) {
	keys, _ := GenerateServerKeys()
	p, err := pipeHandshake(t, keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, HeaderSize)
	length := MaxMessageSize + 1
	header[2] = MsgSetupConnection
	header[3], header[4], header[5] = byte(length), byte(length>>8), byte(length>>16)
	encrypted, err := p.server.send.Encrypt(nil, header)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = p.serverRaw.Write(encrypted) }()

	// Without the size check the read would wait for a megabyte that never comes.
	_ = p.clientWire.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if _, err := p.client.Read(buf); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("read error = %v, want ErrMessageTooLarge", err)
	}
}

func BenchmarkEncrypt(b *testing.B) {
	var key [CipherKeySize]byte
	rand.Read(key[:])
	cs, _ := NewCipherState(key)

	plaintext := make([]byte, 1024)
	rand.Read(plaintext)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cs.Encrypt(nil, plaintext) //nolint:errcheck
	}
}

func BenchmarkDecrypt(b *testing.B) {
	var key [CipherKeySize]byte
	rand.Read(key[:])

	plaintext := make([]byte, 1024)
	rand.Read(plaintext)

	// Pre-encrypt
	cs1, _ := NewCipherState(key)
	ciphertexts := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		ciphertexts[i], _ = cs1.Encrypt(nil, plaintext)
	}

	cs2, _ := NewCipherState(key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cs2.Decrypt(nil, ciphertexts[i])
	}
}
