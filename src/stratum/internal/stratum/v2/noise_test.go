// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 provides tests for Noise Protocol implementation.
//
// These tests validate:
// - secp256k1 key generation and ECDH operations
// - ChaCha20-Poly1305 IETF encryption/decryption
// - Noise handshake state machine
// - Encrypted connection read/write
package v2

import (
	"bytes"
	"crypto/rand"
	"net"
	"testing"
	"time"
)

// TestGenerateKeypair validates secp256k1 key generation.
func TestGenerateKeypair(t *testing.T) {
	kp1, err := generateKeypair()
	if err != nil {
		t.Fatalf("generateKeypair failed: %v", err)
	}

	// Keys should not be all zeros
	allZero := true
	for _, b := range kp1.private {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("private key is all zeros")
	}

	allZero = true
	for _, b := range kp1.public {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("public key is all zeros")
	}

	// Private and public should be different (also different sizes: 32 vs 33)
	if bytes.Equal(kp1.private[:], kp1.public[:DHPrivKeySize]) {
		t.Error("private and public keys should differ")
	}

	// Generate another keypair - should be different
	kp2, err := generateKeypair()
	if err != nil {
		t.Fatalf("generateKeypair failed: %v", err)
	}

	if bytes.Equal(kp1.private[:], kp2.private[:]) {
		t.Error("two generated private keys should differ")
	}
	if bytes.Equal(kp1.public[:], kp2.public[:]) {
		t.Error("two generated public keys should differ")
	}
}

// TestDH validates secp256k1 ECDH key agreement.
func TestDH(t *testing.T) {
	// Generate two key pairs
	alice, err := generateKeypair()
	if err != nil {
		t.Fatalf("generateKeypair failed: %v", err)
	}

	bob, err := generateKeypair()
	if err != nil {
		t.Fatalf("generateKeypair failed: %v", err)
	}

	// Compute shared secrets
	aliceShared, err := dhSecp256k1(alice.private, bob.public)
	if err != nil {
		t.Fatalf("dhSecp256k1 (alice) failed: %v", err)
	}
	bobShared, err := dhSecp256k1(bob.private, alice.public)
	if err != nil {
		t.Fatalf("dhSecp256k1 (bob) failed: %v", err)
	}

	// Shared secrets should be equal
	if !bytes.Equal(aliceShared[:], bobShared[:]) {
		t.Error("DH shared secrets should be equal")
	}

	// Shared secret should not be all zeros
	allZero := true
	for _, b := range aliceShared {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("shared secret is all zeros")
	}
}

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

// TestGenerateServerKeys validates server key generation.
func TestGenerateServerKeys(t *testing.T) {
	keys, err := GenerateServerKeys()
	if err != nil {
		t.Fatalf("GenerateServerKeys failed: %v", err)
	}

	// Validate key properties
	allZero := true
	for _, b := range keys.Private {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("server private key is all zeros")
	}

	allZero = true
	for _, b := range keys.Public {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("server public key is all zeros")
	}
}

// TestNoiseHandshake validates the full Noise handshake.
func TestNoiseHandshake(t *testing.T) {
	// Create a pipe for testing
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Generate server keys
	serverKeys, err := GenerateServerKeys()
	if err != nil {
		t.Fatalf("GenerateServerKeys failed: %v", err)
	}

	// Run handshake concurrently
	errChan := make(chan error, 2)
	var serverNoise, clientNoise *NoiseConn
	var serverPubKey [DHPubKeySize]byte

	go func() {
		var err error
		serverNoise, err = ServerHandshake(serverConn, serverKeys)
		errChan <- err
	}()

	go func() {
		var err error
		clientNoise, serverPubKey, err = ClientHandshake(clientConn)
		errChan <- err
	}()

	// Wait for both handshakes
	for i := 0; i < 2; i++ {
		select {
		case err := <-errChan:
			if err != nil {
				t.Fatalf("handshake failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("handshake timeout")
		}
	}

	// Verify server public key was transmitted
	if !bytes.Equal(serverPubKey[:], serverKeys.Public[:]) {
		t.Error("client did not receive correct server public key")
	}

	// Test encrypted communication
	testData := []byte("Hello from client to server!")

	go func() {
		clientNoise.Write(testData)
	}()

	buf := make([]byte, 100)
	n, err := serverNoise.Read(buf)
	if err != nil {
		t.Fatalf("server read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], testData) {
		t.Errorf("received = %q, want %q", buf[:n], testData)
	}

	// Test in reverse direction
	reverseData := []byte("Hello from server to client!")

	go func() {
		serverNoise.Write(reverseData)
	}()

	n, err = clientNoise.Read(buf)
	if err != nil {
		t.Fatalf("client read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], reverseData) {
		t.Errorf("received = %q, want %q", buf[:n], reverseData)
	}
}

// TestNoiseConnLargeMessage validates handling of large messages.
func TestNoiseConnLargeMessage(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverKeys, _ := GenerateServerKeys()

	errChan := make(chan error, 2)
	var serverNoise, clientNoise *NoiseConn

	go func() {
		var err error
		serverNoise, err = ServerHandshake(serverConn, serverKeys)
		errChan <- err
	}()

	go func() {
		var err error
		clientNoise, _, err = ClientHandshake(clientConn)
		errChan <- err
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-errChan:
			if err != nil {
				t.Fatalf("handshake failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("handshake timeout")
		}
	}

	// Send a large message (but under max size)
	largeData := make([]byte, 10000)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	done := make(chan struct{})
	go func() {
		clientNoise.Write(largeData)
		close(done)
	}()

	received := make([]byte, len(largeData))
	totalRead := 0
	for totalRead < len(largeData) {
		n, err := serverNoise.Read(received[totalRead:])
		if err != nil {
			t.Fatalf("read failed at offset %d: %v", totalRead, err)
		}
		totalRead += n
	}

	<-done

	if !bytes.Equal(received, largeData) {
		t.Error("large message was corrupted in transit")
	}
}

// TestNoiseConnMultipleMessages validates multiple message handling.
func TestNoiseConnMultipleMessages(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverKeys, _ := GenerateServerKeys()

	errChan := make(chan error, 2)
	var serverNoise, clientNoise *NoiseConn

	go func() {
		var err error
		serverNoise, err = ServerHandshake(serverConn, serverKeys)
		errChan <- err
	}()

	go func() {
		var err error
		clientNoise, _, err = ClientHandshake(clientConn)
		errChan <- err
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-errChan:
			if err != nil {
				t.Fatalf("handshake failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("handshake timeout")
		}
	}

	// Send multiple messages
	messages := []string{
		"First message",
		"Second message",
		"Third message with some more data",
	}

	go func() {
		for _, msg := range messages {
			clientNoise.Write([]byte(msg))
		}
	}()

	for _, expected := range messages {
		buf := make([]byte, 100)
		n, err := serverNoise.Read(buf)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if string(buf[:n]) != expected {
			t.Errorf("received = %q, want %q", buf[:n], expected)
		}
	}
}

// Benchmark tests

func BenchmarkKeyGeneration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generateKeypair()
	}
}

func BenchmarkDH(b *testing.B) {
	kp1, _ := generateKeypair()
	kp2, _ := generateKeypair()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dhSecp256k1(kp1.private, kp2.public) //nolint:errcheck
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
