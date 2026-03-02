// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Fuzz tests for Noise Protocol implementation.
//
// These fuzz tests validate robustness of:
// - CipherState encrypt/decrypt round-trip with arbitrary data
// - CipherState decrypt resilience against malformed ciphertext
// - ServerHandshake resilience against garbage input
// - NoiseConn.Read resilience against malformed framed data

package v2

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// FuzzNoiseCipherRoundTrip verifies that for any arbitrary plaintext and
// additional data, encrypting then decrypting yields the original plaintext.
// Two CipherState instances are created from the same key (one for encrypt,
// one for decrypt) so nonces stay synchronised at position 0.
func FuzzNoiseCipherRoundTrip(f *testing.F) {
	// Seed corpus: a few representative inputs.
	f.Add([]byte("Hello, Stratum V2!"), []byte("additional data"))
	f.Add([]byte{}, []byte{})                  // empty plaintext + empty AD
	f.Add([]byte{0x00}, []byte{0xff})          // single-byte values
	f.Add(make([]byte, 1024), make([]byte, 0)) // 1 KiB plaintext, no AD
	f.Add(make([]byte, 0), make([]byte, 256))  // no plaintext, 256 B AD

	f.Fuzz(func(t *testing.T, plaintext, ad []byte) {
		// Deterministic key derived from a fixed value so the test is
		// reproducible. Security of the key is irrelevant for fuzzing.
		var key [CipherKeySize]byte
		for i := range key {
			key[i] = byte(i)
		}

		encryptor, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (encrypt) failed: %v", err)
		}

		decryptor, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (decrypt) failed: %v", err)
		}

		ciphertext, err := encryptor.Encrypt(ad, plaintext)
		if err != nil {
			t.Fatalf("Encrypt failed: %v", err)
		}

		// Ciphertext must be strictly longer due to the Poly1305 tag.
		if len(ciphertext) != len(plaintext)+TagSize {
			t.Fatalf("ciphertext length = %d, want %d (plaintext %d + tag %d)",
				len(ciphertext), len(plaintext)+TagSize, len(plaintext), TagSize)
		}

		got, err := decryptor.Decrypt(ad, ciphertext)
		if err != nil {
			t.Fatalf("Decrypt failed: %v", err)
		}

		if !bytes.Equal(got, plaintext) {
			t.Fatalf("round-trip mismatch: got %d bytes, want %d bytes", len(got), len(plaintext))
		}
	})
}

// FuzzNoiseCipherDecryptMalformed feeds arbitrary bytes to Decrypt. The
// function must never panic regardless of input; it should return an error
// for any input that is not a valid ciphertext.
func FuzzNoiseCipherDecryptMalformed(f *testing.F) {
	// Seed corpus: various garbage lengths.
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0x42}, []byte{0x00})
	f.Add(make([]byte, TagSize-1), []byte("ad"))     // one byte short of minimum
	f.Add(make([]byte, TagSize), []byte("ad"))        // exactly tag-sized
	f.Add(make([]byte, TagSize+1), []byte("ad"))      // one byte over tag size
	f.Add(make([]byte, 1024), []byte{})               // large garbage, no AD
	f.Add(make([]byte, MaxNoiseMessageSize), []byte{}) // near-maximum size

	f.Fuzz(func(t *testing.T, ciphertext, ad []byte) {
		var key [CipherKeySize]byte
		for i := range key {
			key[i] = byte(i * 3)
		}

		cs, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState failed: %v", err)
		}

		// Must not panic. An error is the expected outcome for random data.
		_, _ = cs.Decrypt(ad, ciphertext)
	})
}

// FuzzNoiseHandshakeGarbage creates a net.Pipe pair, writes fuzzed garbage
// to the client end, and calls ServerHandshake on the server end. The
// handshake must fail gracefully (return an error) without panicking.
func FuzzNoiseHandshakeGarbage(f *testing.F) {
	// Seed corpus: various lengths of garbage.
	f.Add([]byte{})                                    // empty
	f.Add(make([]byte, 1))                             // 1 byte
	f.Add(make([]byte, DHPubKeySize))                        // exactly one DH key
	f.Add(make([]byte, DHPubKeySize+DHPubKeySize+TagSize)) // Act1 + Act2 sized
	f.Add(make([]byte, 4096))                          // large blob
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) // short non-zero garbage

	f.Fuzz(func(t *testing.T, garbage []byte) {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()

		serverKeys, err := GenerateServerKeys()
		if err != nil {
			t.Fatalf("GenerateServerKeys failed: %v", err)
		}

		handshakeErr := make(chan error, 1)
		go func() {
			_, err := ServerHandshake(serverConn, serverKeys)
			handshakeErr <- err
		}()

		// Set a tight deadline to avoid deadlocks. net.Pipe is synchronous:
		// if the garbage is larger than Act 1 (32 bytes), the server will
		// try to write Act 2 while we're still blocked writing garbage,
		// causing a deadlock. The deadline breaks the deadlock.
		_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))

		// Write garbage, drain any server response (Act 2), then close.
		go func() {
			defer clientConn.Close()
			_, _ = clientConn.Write(garbage)
			// Drain anything the server writes back (Act 2 response)
			// so the server's write doesn't block.
			buf := make([]byte, 4096)
			for {
				_, err := clientConn.Read(buf)
				if err != nil {
					return
				}
			}
		}()

		select {
		case err := <-handshakeErr:
			_ = err
		case <-time.After(10 * time.Second):
			t.Fatal("ServerHandshake did not return within timeout")
		}
	})
}

// FuzzNoiseConnReadMalformed constructs a NoiseConn with valid cipher states
// and feeds it malformed framed data. NoiseConn.Read uses a 2-byte LE length
// prefix followed by that many bytes of ciphertext. The Read must not panic
// regardless of what bytes are supplied.
//
// Since NoiseConn fields are unexported but this test file is in the same
// package (v2), we can construct the struct directly.
func FuzzNoiseConnReadMalformed(f *testing.F) {
	// Seed corpus: various frame shapes.
	f.Add([]byte{})                     // empty - no length prefix at all
	f.Add([]byte{0x00, 0x00})           // length = 0
	f.Add([]byte{0x01, 0x00})           // length = 1, but no payload
	f.Add([]byte{0x01, 0x00, 0xAB})     // length = 1, one byte payload (too short for valid ciphertext)
	f.Add([]byte{0x05, 0x00, 1, 2, 3, 4, 5})                    // length = 5, matching payload
	f.Add([]byte{0xff, 0xff, 0x00, 0x00})                        // length = 65535, only 2 payload bytes
	f.Add(append([]byte{0x20, 0x00}, make([]byte, 32)...))       // length = 32, 32 bytes of zeroes
	f.Add(append([]byte{0x30, 0x00}, make([]byte, 48)...))       // length = 48, 48 bytes of zeroes

	f.Fuzz(func(t *testing.T, data []byte) {
		// Build a deterministic key for the recv CipherState.
		var key [CipherKeySize]byte
		for i := range key {
			key[i] = byte(i * 7)
		}

		recvCS, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState failed: %v", err)
		}

		// We also need a send CipherState (NoiseConn has both), but
		// this test only exercises Read, so send can use the same key.
		sendCS, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (send) failed: %v", err)
		}

		// Create a pipe. Write fuzzed data into the write end, then
		// close it so Read sees EOF after the data.
		serverSide, clientSide := net.Pipe()
		defer serverSide.Close()

		go func() {
			_, _ = clientSide.Write(data)
			clientSide.Close()
		}()

		nc := &NoiseConn{
			conn:     serverSide,
			send:     sendCS,
			recv:     recvCS,
			isServer: true,
		}

		// Attempt to read. We do NOT care about the result; we only
		// care that Read does not panic.
		buf := make([]byte, 65536)
		_, _ = nc.Read(buf)
	})
}

// FuzzNoiseCipherRoundTripMultiMessage verifies that multiple sequential
// encrypt/decrypt operations with the same CipherState pair remain
// consistent. The nonce auto-increments on each call so both sides must
// stay in lock-step.
func FuzzNoiseCipherRoundTripMultiMessage(f *testing.F) {
	f.Add([]byte("msg1"), []byte("msg2"), []byte("msg3"))
	f.Add([]byte{}, []byte{0x00}, []byte{0xff, 0xfe})
	f.Add(make([]byte, 500), make([]byte, 1000), make([]byte, 1))

	f.Fuzz(func(t *testing.T, m1, m2, m3 []byte) {
		var key [CipherKeySize]byte
		for i := range key {
			key[i] = byte(i + 0x55)
		}

		enc, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (enc) failed: %v", err)
		}
		dec, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (dec) failed: %v", err)
		}

		messages := [][]byte{m1, m2, m3}
		for i, msg := range messages {
			ct, err := enc.Encrypt(nil, msg)
			if err != nil {
				t.Fatalf("Encrypt #%d failed: %v", i, err)
			}

			pt, err := dec.Decrypt(nil, ct)
			if err != nil {
				t.Fatalf("Decrypt #%d failed: %v", i, err)
			}

			if !bytes.Equal(pt, msg) {
				t.Fatalf("round-trip mismatch on message #%d: got %d bytes, want %d bytes",
					i, len(pt), len(msg))
			}
		}
	})
}

// FuzzNoiseConnReadTamperedFrame performs a full handshake, then tampers
// with a legitimately framed message before the server reads it. This
// validates that NoiseConn.Read properly returns an error on authentication
// failure and does not panic or return corrupted plaintext.
func FuzzNoiseConnReadTamperedFrame(f *testing.F) {
	// The fuzz input is a byte used to XOR-tamper a position in the
	// ciphertext, plus an index offset to choose which byte to corrupt.
	f.Add(byte(0xff), uint8(0))
	f.Add(byte(0x01), uint8(5))
	f.Add(byte(0x80), uint8(15))

	f.Fuzz(func(t *testing.T, xorByte byte, posOffset uint8) {
		if xorByte == 0 {
			// XOR with 0 changes nothing; skip trivial case.
			t.Skip("xorByte=0 is a no-op")
		}

		// We need a valid encrypted frame to tamper with. Build one by
		// using a deterministic key and encrypting a known plaintext.
		var key [CipherKeySize]byte
		for i := range key {
			key[i] = byte(i + 0xAA)
		}

		sendCS, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (send) failed: %v", err)
		}
		recvCS, err := NewCipherState(key)
		if err != nil {
			t.Fatalf("NewCipherState (recv) failed: %v", err)
		}

		plaintext := []byte("authenticated plaintext for tamper test")
		ciphertext, err := sendCS.Encrypt(nil, plaintext)
		if err != nil {
			t.Fatalf("Encrypt failed: %v", err)
		}

		// Tamper with the ciphertext at a position derived from the
		// fuzz input. The position wraps around the ciphertext length.
		if len(ciphertext) == 0 {
			t.Skip("empty ciphertext")
		}
		pos := int(posOffset) % len(ciphertext)
		ciphertext[pos] ^= xorByte

		// Frame the tampered ciphertext with its length prefix.
		var frame []byte
		var lengthBuf [2]byte
		binary.LittleEndian.PutUint16(lengthBuf[:], uint16(len(ciphertext)))
		frame = append(frame, lengthBuf[:]...)
		frame = append(frame, ciphertext...)

		// Feed it to a NoiseConn via net.Pipe.
		serverSide, clientSide := net.Pipe()
		defer serverSide.Close()

		go func() {
			_, _ = clientSide.Write(frame)
			clientSide.Close()
		}()

		nc := &NoiseConn{
			conn:     serverSide,
			send:     sendCS, // not used by Read
			recv:     recvCS,
			isServer: true,
		}

		buf := make([]byte, 1024)
		_, readErr := nc.Read(buf)

		// Tampered ciphertext must fail authentication.
		if readErr == nil {
			t.Fatal("Read should have returned an error for tampered ciphertext")
		}
	})
}
