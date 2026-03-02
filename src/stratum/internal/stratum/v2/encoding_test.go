// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 provides tests for Stratum V2 binary encoding/decoding.
//
// These tests validate:
// - Binary encoder/decoder correctness
// - Message type encoding and decoding
// - Field ordering and byte alignment
// - Edge cases and boundary conditions
package v2

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestEncoderBasicTypes validates basic type encoding.
func TestEncoderBasicTypes(t *testing.T) {
	tests := []struct {
		name     string
		encode   func(*Encoder)
		expected []byte
	}{
		{
			name:     "U8",
			encode:   func(e *Encoder) { e.WriteU8(0x42) },
			expected: []byte{0x42},
		},
		{
			name:     "U16",
			encode:   func(e *Encoder) { e.WriteU16(0x1234) },
			expected: []byte{0x34, 0x12}, // Little-endian
		},
		{
			name:     "U24",
			encode:   func(e *Encoder) { e.WriteU24(0x123456) },
			expected: []byte{0x56, 0x34, 0x12},
		},
		{
			name:     "U32",
			encode:   func(e *Encoder) { e.WriteU32(0x12345678) },
			expected: []byte{0x78, 0x56, 0x34, 0x12},
		},
		{
			name:     "U64",
			encode:   func(e *Encoder) { e.WriteU64(0x123456789ABCDEF0) },
			expected: []byte{0xF0, 0xDE, 0xBC, 0x9A, 0x78, 0x56, 0x34, 0x12},
		},
		{
			name:     "Bool true",
			encode:   func(e *Encoder) { e.WriteBool(true) },
			expected: []byte{0x01},
		},
		{
			name:     "Bool false",
			encode:   func(e *Encoder) { e.WriteBool(false) },
			expected: []byte{0x00},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := NewEncoder()
			tt.encode(enc)
			result := enc.Bytes()

			if !bytes.Equal(result, tt.expected) {
				t.Errorf("got %x, want %x", result, tt.expected)
			}
		})
	}
}

// TestEncoderStrings validates string encoding (B0_255 type).
func TestEncoderStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []byte
	}{
		{
			name:     "empty string",
			input:    "",
			expected: []byte{0x00},
		},
		{
			name:     "short string",
			input:    "hello",
			expected: append([]byte{0x05}, []byte("hello")...),
		},
		{
			name:     "max length string prefix",
			input:    "a",
			expected: []byte{0x01, 'a'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := NewEncoder()
			enc.WriteB0_255(tt.input)
			result := enc.Bytes()

			if !bytes.Equal(result, tt.expected) {
				t.Errorf("got %x, want %x", result, tt.expected)
			}
		})
	}
}

// TestDecoderBasicTypes validates basic type decoding.
func TestDecoderBasicTypes(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		decode   func(*Decoder) (interface{}, error)
		expected interface{}
	}{
		{
			name:     "U8",
			input:    []byte{0x42},
			decode:   func(d *Decoder) (interface{}, error) { return d.ReadU8() },
			expected: uint8(0x42),
		},
		{
			name:     "U16",
			input:    []byte{0x34, 0x12},
			decode:   func(d *Decoder) (interface{}, error) { return d.ReadU16() },
			expected: uint16(0x1234),
		},
		{
			name:     "U32",
			input:    []byte{0x78, 0x56, 0x34, 0x12},
			decode:   func(d *Decoder) (interface{}, error) { return d.ReadU32() },
			expected: uint32(0x12345678),
		},
		{
			name:     "U64",
			input:    []byte{0xF0, 0xDE, 0xBC, 0x9A, 0x78, 0x56, 0x34, 0x12},
			decode:   func(d *Decoder) (interface{}, error) { return d.ReadU64() },
			expected: uint64(0x123456789ABCDEF0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := NewDecoderFromBytes(tt.input)
			result, err := tt.decode(dec)
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestDecoderStrings validates string decoding.
func TestDecoderStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty string",
			input:    []byte{0x00},
			expected: "",
		},
		{
			name:     "short string",
			input:    append([]byte{0x05}, []byte("hello")...),
			expected: "hello",
		},
		{
			name:     "unicode string",
			input:    append([]byte{0x06}, []byte("日本")...), // 2 chars = 6 bytes in UTF-8
			expected: "日本",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := NewDecoderFromBytes(tt.input)
			result, err := dec.ReadB0_255()
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestMessageHeaderEncode validates message header encoding.
func TestMessageHeaderEncode(t *testing.T) {
	header := &MessageHeader{
		ExtensionType: 0x00,
		MsgType:       MsgSetupConnection,
		Length:        0x001234,
	}

	var buf bytes.Buffer
	if err := header.Encode(&buf); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	result := buf.Bytes()
	if len(result) != HeaderSize {
		t.Errorf("header size = %d, want %d", len(result), HeaderSize)
	}

	// Header format: [2 bytes ext type (LE)][1 byte msg type][3 bytes length (LE)]
	// Check extension type (2 bytes little-endian)
	extType := binary.LittleEndian.Uint16(result[0:2])
	if extType != 0x00 {
		t.Errorf("extension_type = %x, want 00", extType)
	}
	// Check message type (byte at offset 2)
	if result[2] != MsgSetupConnection {
		t.Errorf("msg_type = %x, want %x", result[2], MsgSetupConnection)
	}
	// Length is 3 bytes little-endian (at offset 3-5)
	length := uint32(result[3]) | uint32(result[4])<<8 | uint32(result[5])<<16
	if length != 0x001234 {
		t.Errorf("length = %x, want 001234", length)
	}
}

// TestMessageHeaderDecode validates message header decoding.
func TestMessageHeaderDecode(t *testing.T) {
	// Header format: [2 bytes ext type (LE)][1 byte msg type][3 bytes length (LE)]
	input := []byte{
		0x00, 0x00, // Extension type (2 bytes LE)
		MsgSetupConnection, // Message type (1 byte)
		0x34, 0x12, 0x00,   // Length (3 bytes LE)
	}

	header := &MessageHeader{}
	if err := header.Decode(bytes.NewReader(input)); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if header.ExtensionType != 0x00 {
		t.Errorf("extension_type = %d, want 0", header.ExtensionType)
	}
	if header.MsgType != MsgSetupConnection {
		t.Errorf("msg_type = %d, want %d", header.MsgType, MsgSetupConnection)
	}
	if header.Length != 0x1234 {
		t.Errorf("length = %d, want %d", header.Length, 0x1234)
	}
}

// TestSetupConnectionEncode validates SetupConnection message encoding.
func TestSetupConnectionEncode(t *testing.T) {
	msg := &SetupConnection{
		Protocol:        ProtocolMiningV2,
		MinVersion:      2,
		MaxVersion:      2,
		Flags:           ProtocolFlagRequiresStandardJobs,
		Endpoint:        "pool.example.com",
		EndpointPort:    3334,
		VendorID:        "TestVendor",
		HardwareVersion: "1.0",
		FirmwareVersion: "2.0",
	}

	encoded, err := EncodeSetupConnection(msg)
	if err != nil {
		t.Fatalf("EncodeSetupConnection failed: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("EncodeSetupConnection returned empty")
	}

	// Verify header - format is [2 bytes ext][1 byte type][3 bytes len]
	if encoded[2] != MsgSetupConnection {
		t.Errorf("msg_type = %x, want %x", encoded[2], MsgSetupConnection)
	}
}

// TestSetupConnectionDecode validates SetupConnection message decoding.
func TestSetupConnectionDecode(t *testing.T) {
	// Build payload manually matching SV2 spec wire order
	enc := NewEncoder()
	enc.WriteU8(ProtocolMiningV2)
	enc.WriteU16(2) // MinVersion
	enc.WriteU16(2) // MaxVersion
	enc.WriteU32(ProtocolFlagRequiresStandardJobs)
	enc.WriteB0_255("pool.example.com") // endpoint_host
	enc.WriteU16(3334)                  // endpoint_port
	enc.WriteB0_255("TestVendor")       // vendor
	enc.WriteB0_255("1.0")              // hardware_version
	enc.WriteB0_255("2.0")              // firmware
	payload := enc.Bytes()

	msg, err := DecodeSetupConnection(payload)
	if err != nil {
		t.Fatalf("DecodeSetupConnection failed: %v", err)
	}

	if msg.Protocol != ProtocolMiningV2 {
		t.Errorf("Protocol = %d, want %d", msg.Protocol, ProtocolMiningV2)
	}
	if msg.MinVersion != 2 {
		t.Errorf("MinVersion = %d, want 2", msg.MinVersion)
	}
	if msg.MaxVersion != 2 {
		t.Errorf("MaxVersion = %d, want 2", msg.MaxVersion)
	}
	if msg.Flags != ProtocolFlagRequiresStandardJobs {
		t.Errorf("Flags = %x, want %x", msg.Flags, ProtocolFlagRequiresStandardJobs)
	}
	if msg.Endpoint != "pool.example.com" {
		t.Errorf("Endpoint = %s, want pool.example.com", msg.Endpoint)
	}
	if msg.EndpointPort != 3334 {
		t.Errorf("EndpointPort = %d, want 3334", msg.EndpointPort)
	}
	if msg.VendorID != "TestVendor" {
		t.Errorf("VendorID = %s, want TestVendor", msg.VendorID)
	}
}

// TestOpenStandardMiningChannelEncode validates channel open encoding.
func TestOpenStandardMiningChannelEncode(t *testing.T) {
	msg := &OpenStandardMiningChannel{
		RequestID:       1,
		UserIdentity:    "DGBaddress.worker1",
		NominalHashRate: 1000000.0,
		MaxTarget:       NBitsToU256(0x1d00ffff),
	}

	encoded, err := EncodeOpenStandardMiningChannel(msg)
	if err != nil {
		t.Fatalf("EncodeOpenStandardMiningChannel failed: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("EncodeOpenStandardMiningChannel returned empty")
	}

	// Verify message type - format is [2 bytes ext][1 byte type][3 bytes len]
	if encoded[2] != MsgOpenStandardMiningChannel {
		t.Errorf("msg_type = %x, want %x", encoded[2], MsgOpenStandardMiningChannel)
	}
}

// TestOpenStandardMiningChannelDecode validates channel open decoding.
func TestOpenStandardMiningChannelDecode(t *testing.T) {
	expectedTarget := NBitsToU256(0x1d00ffff)
	enc := NewEncoder()
	enc.WriteU32(1) // RequestID
	enc.WriteB0_255("DGBaddress.worker1")
	enc.WriteF32(1000000.0)
	enc.WriteBytes(expectedTarget[:]) // U256 (32 bytes)
	payload := enc.Bytes()

	msg, err := DecodeOpenStandardMiningChannel(payload)
	if err != nil {
		t.Fatalf("DecodeOpenStandardMiningChannel failed: %v", err)
	}

	if msg.RequestID != 1 {
		t.Errorf("RequestID = %d, want 1", msg.RequestID)
	}
	if msg.UserIdentity != "DGBaddress.worker1" {
		t.Errorf("UserIdentity = %s, want DGBaddress.worker1", msg.UserIdentity)
	}
	// Float comparison with tolerance
	if msg.NominalHashRate < 999999.0 || msg.NominalHashRate > 1000001.0 {
		t.Errorf("NominalHashRate = %f, want ~1000000.0", msg.NominalHashRate)
	}
	if msg.MaxTarget != expectedTarget {
		t.Errorf("MaxTarget mismatch")
	}
}

// TestSubmitSharesStandardEncode validates share submission encoding.
func TestSubmitSharesStandardEncode(t *testing.T) {
	msg := &SubmitSharesStandard{
		ChannelID:   1,
		SequenceNum: 100,
		JobID:       42,
		Nonce:       0xDEADBEEF,
		NTime:       1609459200,
		Version:     0x20000000,
	}

	encoded := EncodeSubmitSharesStandard(msg)
	if len(encoded) == 0 {
		t.Fatal("EncodeSubmitSharesStandard returned empty")
	}

	if encoded[2] != MsgSubmitSharesStandard {
		t.Errorf("msg_type = %x, want %x", encoded[2], MsgSubmitSharesStandard)
	}
}

// TestSubmitSharesStandardDecode validates share submission decoding.
func TestSubmitSharesStandardDecode(t *testing.T) {
	enc := NewEncoder()
	enc.WriteU32(1)          // ChannelID
	enc.WriteU32(100)        // SequenceNum
	enc.WriteU32(42)         // JobID
	enc.WriteU32(0xDEADBEEF) // Nonce
	enc.WriteU32(1609459200) // NTime
	enc.WriteU32(0x20000000) // Version
	payload := enc.Bytes()

	msg, err := DecodeSubmitSharesStandard(payload)
	if err != nil {
		t.Fatalf("DecodeSubmitSharesStandard failed: %v", err)
	}

	if msg.ChannelID != 1 {
		t.Errorf("ChannelID = %d, want 1", msg.ChannelID)
	}
	if msg.SequenceNum != 100 {
		t.Errorf("SequenceNum = %d, want 100", msg.SequenceNum)
	}
	if msg.JobID != 42 {
		t.Errorf("JobID = %d, want 42", msg.JobID)
	}
	if msg.Nonce != 0xDEADBEEF {
		t.Errorf("Nonce = %x, want DEADBEEF", msg.Nonce)
	}
	if msg.NTime != 1609459200 {
		t.Errorf("NTime = %d, want 1609459200", msg.NTime)
	}
	if msg.Version != 0x20000000 {
		t.Errorf("Version = %x, want 20000000", msg.Version)
	}
}

// TestNewMiningJobEncode validates job notification encoding.
func TestNewMiningJobEncode(t *testing.T) {
	var merkleRoot [32]byte
	for i := range merkleRoot {
		merkleRoot[i] = byte(i)
	}

	ntime := uint32(1609459200)
	msg := &NewMiningJob{
		ChannelID:  1,
		JobID:      42,
		MinNTime:   &ntime,
		Version:    0x20000000,
		MerkleRoot: merkleRoot,
	}

	encoded := EncodeNewMiningJob(msg)
	if len(encoded) == 0 {
		t.Fatal("EncodeNewMiningJob returned empty")
	}

	if encoded[2] != MsgNewMiningJob {
		t.Errorf("msg_type = %x, want %x", encoded[2], MsgNewMiningJob)
	}
}

// TestSetNewPrevHashEncode validates prev hash message encoding.
func TestSetNewPrevHashEncode(t *testing.T) {
	var prevHash [32]byte
	for i := range prevHash {
		prevHash[i] = byte(i)
	}

	msg := &SetNewPrevHash{
		ChannelID: 1,
		JobID:     42,
		PrevHash:  prevHash,
		MinNTime:  1609459200,
		NBits:     0x1d00ffff,
	}

	encoded := EncodeSetNewPrevHash(msg)
	if len(encoded) == 0 {
		t.Fatal("EncodeSetNewPrevHash returned empty")
	}

	if encoded[2] != MsgSetNewPrevHash {
		t.Errorf("msg_type = %x, want %x", encoded[2], MsgSetNewPrevHash)
	}
}

// TestEncoderDecoderRoundTrip validates encode/decode symmetry.
func TestEncoderDecoderRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		values []interface{}
	}{
		{
			name:   "mixed types",
			values: []interface{}{uint8(0x42), uint16(0x1234), uint32(0x12345678), "hello"},
		},
		{
			name:   "boundary values",
			values: []interface{}{uint8(0xFF), uint16(0xFFFF), uint32(0xFFFFFFFF)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := NewEncoder()
			for _, v := range tt.values {
				switch val := v.(type) {
				case uint8:
					enc.WriteU8(val)
				case uint16:
					enc.WriteU16(val)
				case uint32:
					enc.WriteU32(val)
				case string:
					enc.WriteB0_255(val)
				}
			}

			dec := NewDecoderFromBytes(enc.Bytes())
			for _, v := range tt.values {
				switch expected := v.(type) {
				case uint8:
					got, err := dec.ReadU8()
					if err != nil || got != expected {
						t.Errorf("U8 = %x, want %x", got, expected)
					}
				case uint16:
					got, err := dec.ReadU16()
					if err != nil || got != expected {
						t.Errorf("U16 = %x, want %x", got, expected)
					}
				case uint32:
					got, err := dec.ReadU32()
					if err != nil || got != expected {
						t.Errorf("U32 = %x, want %x", got, expected)
					}
				case string:
					got, err := dec.ReadB0_255()
					if err != nil || got != expected {
						t.Errorf("string = %q, want %q", got, expected)
					}
				}
			}
		})
	}
}

// TestDecoderErrors validates error handling for malformed input.
func TestDecoderErrors(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		read  func(*Decoder) error
	}{
		{
			name:  "U16 truncated",
			input: []byte{0x34}, // Only 1 byte for U16
			read:  func(d *Decoder) error { _, err := d.ReadU16(); return err },
		},
		{
			name:  "U32 truncated",
			input: []byte{0x34, 0x12}, // Only 2 bytes for U32
			read:  func(d *Decoder) error { _, err := d.ReadU32(); return err },
		},
		{
			name:  "string length exceeds data",
			input: []byte{0x10}, // Length 16 but no data
			read:  func(d *Decoder) error { _, err := d.ReadB0_255(); return err },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := NewDecoderFromBytes(tt.input)
			err := tt.read(dec)
			if err == nil {
				t.Error("expected error for malformed input")
			}
		})
	}
}

// Benchmark tests

func BenchmarkEncodeU32(b *testing.B) {
	enc := NewEncoder()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc.WriteU32(uint32(i))
	}
}

func BenchmarkDecodeU32(b *testing.B) {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 0x12345678)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec := NewDecoderFromBytes(data)
		dec.ReadU32()
	}
}

func BenchmarkEncodeSetupConnection(b *testing.B) {
	msg := &SetupConnection{
		Protocol:        ProtocolMiningV2,
		MinVersion:      2,
		MaxVersion:      2,
		Flags:           ProtocolFlagRequiresStandardJobs,
		Endpoint:        "pool.example.com",
		EndpointPort:    3334,
		VendorID:        "TestVendor",
		HardwareVersion: "1.0",
		FirmwareVersion: "2.0",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeSetupConnection(msg)
	}
}

func BenchmarkEncodeSubmitSharesStandard(b *testing.B) {
	msg := &SubmitSharesStandard{
		ChannelID:   1,
		SequenceNum: 100,
		JobID:       42,
		Nonce:       0xDEADBEEF,
		NTime:       1609459200,
		Version:     0x20000000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeSubmitSharesStandard(msg)
	}
}
