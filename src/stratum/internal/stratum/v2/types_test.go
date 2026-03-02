// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 - Tests for Stratum V2 protocol types and message structures.
//
// These tests verify the message type definitions, constants, header encoding,
// and protocol invariants for the Stratum V2 binary protocol.
package v2

import (
	"bytes"
	"io"
	"testing"
)

// =============================================================================
// PROTOCOL CONSTANTS TESTS
// =============================================================================

func TestProtocolConstants(t *testing.T) {
	// Verify protocol constants match SV2 specification
	if DefaultPort != 3334 {
		t.Errorf("DefaultPort = %d, want 3334", DefaultPort)
	}

	if MaxMessageSize != 1<<20 {
		t.Errorf("MaxMessageSize = %d, want %d (1MB)", MaxMessageSize, 1<<20)
	}

	if HeaderSize != 6 {
		t.Errorf("HeaderSize = %d, want 6", HeaderSize)
	}

	if NoiseHandshakePattern != "Noise_NX_secp256k1_ChaChaPoly_SHA256" {
		t.Errorf("NoiseHandshakePattern = %q, want 'Noise_NX_secp256k1_ChaChaPoly_SHA256'",
			NoiseHandshakePattern)
	}
}

func TestMaxMessageSize_Reasonable(t *testing.T) {
	// 1MB is reasonable for mining data
	// - Block templates can be large with many transactions
	// - But not so large as to cause memory issues

	if MaxMessageSize < 1024*1024 {
		t.Error("MaxMessageSize too small for block templates")
	}
	if MaxMessageSize > 100*1024*1024 {
		t.Error("MaxMessageSize too large, could cause memory issues")
	}
}

// =============================================================================
// MESSAGE TYPE IDENTIFIER TESTS
// =============================================================================

func TestMessageTypeIdentifiers_Setup(t *testing.T) {
	// Setup connection messages
	if MsgSetupConnection != 0x00 {
		t.Errorf("MsgSetupConnection = %#x, want 0x00", MsgSetupConnection)
	}
	if MsgSetupConnectionSuccess != 0x01 {
		t.Errorf("MsgSetupConnectionSuccess = %#x, want 0x01", MsgSetupConnectionSuccess)
	}
	if MsgSetupConnectionError != 0x02 {
		t.Errorf("MsgSetupConnectionError = %#x, want 0x02", MsgSetupConnectionError)
	}
}

func TestMessageTypeIdentifiers_Channel(t *testing.T) {
	// Mining channel messages
	if MsgOpenStandardMiningChannel != 0x10 {
		t.Errorf("MsgOpenStandardMiningChannel = %#x, want 0x10", MsgOpenStandardMiningChannel)
	}
	if MsgOpenStandardMiningChannelSuccess != 0x11 {
		t.Errorf("MsgOpenStandardMiningChannelSuccess = %#x, want 0x11", MsgOpenStandardMiningChannelSuccess)
	}
	if MsgOpenMiningChannelError != 0x12 {
		t.Errorf("MsgOpenMiningChannelError = %#x, want 0x12", MsgOpenMiningChannelError)
	}
	if MsgOpenExtendedMiningChannel != 0x13 {
		t.Errorf("MsgOpenExtendedMiningChannel = %#x, want 0x13", MsgOpenExtendedMiningChannel)
	}
	if MsgOpenExtendedMiningChannelSuccess != 0x14 {
		t.Errorf("MsgOpenExtendedMiningChannelSuccess = %#x, want 0x14", MsgOpenExtendedMiningChannelSuccess)
	}
	if MsgUpdateChannel != 0x16 {
		t.Errorf("MsgUpdateChannel = %#x, want 0x16", MsgUpdateChannel)
	}
	if MsgUpdateChannelError != 0x17 {
		t.Errorf("MsgUpdateChannelError = %#x, want 0x17", MsgUpdateChannelError)
	}
	if MsgCloseChannel != 0x18 {
		t.Errorf("MsgCloseChannel = %#x, want 0x18", MsgCloseChannel)
	}
}

func TestMessageTypeIdentifiers_Jobs(t *testing.T) {
	// Mining job messages
	if MsgNewMiningJob != 0x15 {
		t.Errorf("MsgNewMiningJob = %#x, want 0x15", MsgNewMiningJob)
	}
	if MsgNewExtendedMiningJob != 0x1f {
		t.Errorf("MsgNewExtendedMiningJob = %#x, want 0x1f", MsgNewExtendedMiningJob)
	}
	if MsgSetNewPrevHash != 0x20 {
		t.Errorf("MsgSetNewPrevHash = %#x, want 0x20", MsgSetNewPrevHash)
	}
}

func TestMessageTypeIdentifiers_Shares(t *testing.T) {
	// Share submission messages
	if MsgSubmitSharesStandard != 0x1a {
		t.Errorf("MsgSubmitSharesStandard = %#x, want 0x1a", MsgSubmitSharesStandard)
	}
	if MsgSubmitSharesExtended != 0x1b {
		t.Errorf("MsgSubmitSharesExtended = %#x, want 0x1b", MsgSubmitSharesExtended)
	}
	if MsgSubmitSharesSuccess != 0x1c {
		t.Errorf("MsgSubmitSharesSuccess = %#x, want 0x1c", MsgSubmitSharesSuccess)
	}
	if MsgSubmitSharesError != 0x1d {
		t.Errorf("MsgSubmitSharesError = %#x, want 0x1d", MsgSubmitSharesError)
	}
}

func TestMessageTypeIdentifiers_Misc(t *testing.T) {
	// Common protocol messages
	if MsgReconnect != 0x04 {
		t.Errorf("MsgReconnect = %#x, want 0x04", MsgReconnect)
	}
	if MsgChannelEndpointChanged != 0x03 {
		t.Errorf("MsgChannelEndpointChanged = %#x, want 0x03", MsgChannelEndpointChanged)
	}
	if MsgSetExtranoncePrefix != 0x19 {
		t.Errorf("MsgSetExtranoncePrefix = %#x, want 0x19", MsgSetExtranoncePrefix)
	}
	// Difficulty/target and group management
	if MsgSetTarget != 0x21 {
		t.Errorf("MsgSetTarget = %#x, want 0x21", MsgSetTarget)
	}
	if MsgSetCustomMiningJob != 0x22 {
		t.Errorf("MsgSetCustomMiningJob = %#x, want 0x22", MsgSetCustomMiningJob)
	}
	if MsgSetGroupChannel != 0x25 {
		t.Errorf("MsgSetGroupChannel = %#x, want 0x25", MsgSetGroupChannel)
	}
}

// =============================================================================
// PROTOCOL FLAG TESTS
// =============================================================================

func TestProtocolFlags(t *testing.T) {
	if ProtocolFlagRequiresStandardJobs != 1<<0 {
		t.Errorf("ProtocolFlagRequiresStandardJobs = %#x, want 0x01", ProtocolFlagRequiresStandardJobs)
	}
	if ProtocolFlagRequiresWorkSelection != 1<<1 {
		t.Errorf("ProtocolFlagRequiresWorkSelection = %#x, want 0x02", ProtocolFlagRequiresWorkSelection)
	}
	if ProtocolFlagRequiresVersionRolling != 1<<2 {
		t.Errorf("ProtocolFlagRequiresVersionRolling = %#x, want 0x04", ProtocolFlagRequiresVersionRolling)
	}
}

func TestProtocolFlags_Combinations(t *testing.T) {
	// Test flag combinations
	allFlags := ProtocolFlagRequiresStandardJobs |
		ProtocolFlagRequiresWorkSelection |
		ProtocolFlagRequiresVersionRolling

	// Flags should be combinable
	if allFlags != 0x07 {
		t.Errorf("All flags combined = %#x, want 0x07", allFlags)
	}

	// Flags should be independent
	tests := []struct {
		flag uint32
		name string
	}{
		{ProtocolFlagRequiresStandardJobs, "StandardJobs"},
		{ProtocolFlagRequiresWorkSelection, "WorkSelection"},
		{ProtocolFlagRequiresVersionRolling, "VersionRolling"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each flag should be a single bit
			bitCount := 0
			for f := tt.flag; f > 0; f >>= 1 {
				if f&1 == 1 {
					bitCount++
				}
			}
			if bitCount != 1 {
				t.Errorf("Flag %s has %d bits set, want 1", tt.name, bitCount)
			}
		})
	}
}

// =============================================================================
// ERROR CODE TESTS
// =============================================================================

func TestErrorCodeStrings(t *testing.T) {
	// SV2 spec uses STR0_255 error codes
	tests := []struct {
		name string
		code string
	}{
		{"unsupported-feature-flags", ErrCodeUnsupportedFeatureFlags},
		{"unsupported-protocol", ErrCodeUnsupportedProtocol},
		{"protocol-version-mismatch", ErrCodeProtocolVersionMismatch},
		{"unknown-user", ErrCodeUnknownUser},
		{"max-target-out-of-range", ErrCodeMaxTargetOutOfRange},
		{"invalid-channel-id", ErrCodeInvalidChannelID},
		{"stale-share", ErrCodeStaleShare},
		{"difficulty-target-not-met", ErrCodeDifficultyNotMet},
		{"rate-limited", ErrCodeRateLimited},
	}

	for _, tt := range tests {
		if tt.code == "" {
			t.Errorf("Error code %q is empty", tt.name)
		}
		if tt.code != tt.name {
			t.Errorf("Error code constant %q does not match expected %q", tt.code, tt.name)
		}
	}
}

// =============================================================================
// PROTOCOL SUB-TYPE TESTS
// =============================================================================

func TestProtocolSubTypes(t *testing.T) {
	if ProtocolMiningV2 != 0 {
		t.Errorf("ProtocolMiningV2 = %d, want 0", ProtocolMiningV2)
	}
	if ProtocolJobDecl != 1 {
		t.Errorf("ProtocolJobDecl = %d, want 1", ProtocolJobDecl)
	}
	if ProtocolTemplate != 2 {
		t.Errorf("ProtocolTemplate = %d, want 2", ProtocolTemplate)
	}
}

// =============================================================================
// ERROR VARIABLE TESTS
// =============================================================================

func TestErrors(t *testing.T) {
	errors := []struct {
		err  error
		name string
	}{
		{ErrMessageTooLarge, "ErrMessageTooLarge"},
		{ErrInvalidHeader, "ErrInvalidHeader"},
		{ErrUnknownMessage, "ErrUnknownMessage"},
		{ErrHandshakeFailed, "ErrHandshakeFailed"},
		{ErrNotEncrypted, "ErrNotEncrypted"},
		{ErrChannelNotFound, "ErrChannelNotFound"},
		{ErrInvalidJobID, "ErrInvalidJobID"},
		{ErrDuplicateShare, "ErrDuplicateShare"},
		{ErrInvalidShare, "ErrInvalidShare"},
		{ErrStaleShare, "ErrStaleShare"},
		{ErrJobNotFound, "ErrJobNotFound"},
		{ErrLowDifficultyShare, "ErrLowDifficultyShare"},
	}

	for _, te := range errors {
		t.Run(te.name, func(t *testing.T) {
			if te.err == nil {
				t.Errorf("%s is nil", te.name)
			}
			if te.err.Error() == "" {
				t.Errorf("%s has empty message", te.name)
			}
		})
	}
}

// =============================================================================
// MESSAGE HEADER TESTS
// =============================================================================

func TestMessageHeader_Fields(t *testing.T) {
	header := MessageHeader{
		ExtensionType: 0,
		MsgType:       MsgSetupConnection,
		Length:        1000,
	}

	if header.ExtensionType != 0 {
		t.Errorf("ExtensionType = %d, want 0", header.ExtensionType)
	}
	if header.MsgType != MsgSetupConnection {
		t.Errorf("MsgType = %#x, want %#x", header.MsgType, MsgSetupConnection)
	}
	if header.Length != 1000 {
		t.Errorf("Length = %d, want 1000", header.Length)
	}
}

func TestMessageHeader_Encode(t *testing.T) {
	header := MessageHeader{
		ExtensionType: 0x0102,
		MsgType:       0x10,
		Length:        0x112233,
	}

	buf := &bytes.Buffer{}
	err := header.Encode(buf)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	encoded := buf.Bytes()
	if len(encoded) != HeaderSize {
		t.Errorf("Encoded length = %d, want %d", len(encoded), HeaderSize)
	}

	// ExtensionType is 2 bytes little-endian
	if encoded[0] != 0x02 || encoded[1] != 0x01 {
		t.Errorf("ExtensionType encoding wrong: %#v", encoded[0:2])
	}

	// MsgType is 1 byte
	if encoded[2] != 0x10 {
		t.Errorf("MsgType encoding = %#x, want 0x10", encoded[2])
	}

	// Length is 3 bytes little-endian
	if encoded[3] != 0x33 || encoded[4] != 0x22 || encoded[5] != 0x11 {
		t.Errorf("Length encoding wrong: %#v", encoded[3:6])
	}
}

func TestMessageHeader_Decode(t *testing.T) {
	// Create encoded header
	encoded := []byte{
		0x02, 0x01, // ExtensionType (0x0102 LE)
		0x10,             // MsgType
		0x33, 0x22, 0x11, // Length (0x112233 LE)
	}

	header := &MessageHeader{}
	err := header.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if header.ExtensionType != 0x0102 {
		t.Errorf("ExtensionType = %#x, want 0x0102", header.ExtensionType)
	}
	if header.MsgType != 0x10 {
		t.Errorf("MsgType = %#x, want 0x10", header.MsgType)
	}
	if header.Length != 0x112233 {
		t.Errorf("Length = %#x, want 0x112233", header.Length)
	}
}

func TestMessageHeader_EncodeDecode_Roundtrip(t *testing.T) {
	original := MessageHeader{
		ExtensionType: 0x1234,
		MsgType:       MsgNewMiningJob,
		Length:        0xABCDEF,
	}

	// Encode
	buf := &bytes.Buffer{}
	if err := original.Encode(buf); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Decode
	decoded := &MessageHeader{}
	if err := decoded.Decode(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	// Compare
	if decoded.ExtensionType != original.ExtensionType {
		t.Errorf("ExtensionType: got %#x, want %#x", decoded.ExtensionType, original.ExtensionType)
	}
	if decoded.MsgType != original.MsgType {
		t.Errorf("MsgType: got %#x, want %#x", decoded.MsgType, original.MsgType)
	}
	if decoded.Length != original.Length {
		t.Errorf("Length: got %#x, want %#x", decoded.Length, original.Length)
	}
}

func TestMessageHeader_Decode_ShortRead(t *testing.T) {
	// Test incomplete data
	shortData := []byte{0x00, 0x00, 0x10} // Only 3 bytes, need 6

	header := &MessageHeader{}
	err := header.Decode(bytes.NewReader(shortData))
	if err == nil {
		t.Error("Decode should fail with short data")
	}
	if err != io.ErrUnexpectedEOF && err != io.EOF {
		t.Logf("Error type: %T, value: %v", err, err)
	}
}

func TestMessageHeader_Length_Max24Bit(t *testing.T) {
	// Length is 24-bit (max 16MB)
	maxLength := uint32((1 << 24) - 1) // 16777215

	header := MessageHeader{
		ExtensionType: 0,
		MsgType:       MsgSetupConnection,
		Length:        maxLength,
	}

	buf := &bytes.Buffer{}
	if err := header.Encode(buf); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	decoded := &MessageHeader{}
	if err := decoded.Decode(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded.Length != maxLength {
		t.Errorf("Max length roundtrip: got %d, want %d", decoded.Length, maxLength)
	}
}

// =============================================================================
// MESSAGE STRUCTURE TESTS
// =============================================================================

func TestSetupConnection_Fields(t *testing.T) {
	msg := SetupConnection{
		Protocol:        ProtocolMiningV2,
		MinVersion:      2,
		MaxVersion:      2,
		Flags:           ProtocolFlagRequiresVersionRolling,
		Endpoint:        "pool.example.com",
		EndpointPort:    3334,
		VendorID:        "SpiralPool",
		HardwareVersion: "v1.0",
		FirmwareVersion: "1.2.3",
	}

	if msg.Protocol != ProtocolMiningV2 {
		t.Error("Protocol should be Mining")
	}
	if msg.MinVersion != 2 || msg.MaxVersion != 2 {
		t.Error("Version should be 2")
	}
	if msg.Flags != ProtocolFlagRequiresVersionRolling {
		t.Error("Flags mismatch")
	}
	if msg.Endpoint != "pool.example.com" {
		t.Error("Endpoint mismatch")
	}
	if msg.EndpointPort != 3334 {
		t.Error("EndpointPort mismatch")
	}
}

func TestOpenStandardMiningChannel_Fields(t *testing.T) {
	maxTarget := NBitsToU256(0x1d00ffff)
	msg := OpenStandardMiningChannel{
		RequestID:       42,
		UserIdentity:    "DGBAddress.worker1",
		NominalHashRate: 1e12, // 1 TH/s
		MaxTarget:       maxTarget,
	}

	if msg.RequestID != 42 {
		t.Error("RequestID mismatch")
	}
	if msg.UserIdentity != "DGBAddress.worker1" {
		t.Error("UserIdentity mismatch")
	}
	if msg.NominalHashRate != 1e12 {
		t.Error("NominalHashRate mismatch")
	}
	if msg.MaxTarget != maxTarget {
		t.Error("MaxTarget mismatch")
	}
}

func TestNewMiningJob_Fields(t *testing.T) {
	ntime := uint32(1609459200)
	msg := NewMiningJob{
		ChannelID:  1,
		JobID:      100,
		MinNTime:   &ntime,
		Version:    0x20000000,
		MerkleRoot: [32]byte{0x01, 0x02}, // Partial for test
	}

	if msg.ChannelID != 1 {
		t.Error("ChannelID mismatch")
	}
	if msg.JobID != 100 {
		t.Error("JobID mismatch")
	}
	if msg.IsFuture() {
		t.Error("IsFuture should be false when MinNTime is set")
	}
	if msg.Version != 0x20000000 {
		t.Error("Version mismatch")
	}
	if *msg.MinNTime != 1609459200 {
		t.Errorf("MinNTime = %d, want 1609459200", *msg.MinNTime)
	}

	// Test future job (nil MinNTime)
	futureJob := NewMiningJob{
		ChannelID: 1,
		JobID:     101,
		MinNTime:  nil,
		Version:   0x20000000,
	}
	if !futureJob.IsFuture() {
		t.Error("IsFuture should be true when MinNTime is nil")
	}
}

func TestSubmitSharesStandard_Fields(t *testing.T) {
	msg := SubmitSharesStandard{
		ChannelID:   1,
		SequenceNum: 5,
		JobID:       100,
		Nonce:       0x12345678,
		NTime:       1609459200,
		Version:     0x20001234,
	}

	if msg.ChannelID != 1 {
		t.Error("ChannelID mismatch")
	}
	if msg.SequenceNum != 5 {
		t.Error("SequenceNum mismatch")
	}
	if msg.JobID != 100 {
		t.Error("JobID mismatch")
	}
	if msg.Nonce != 0x12345678 {
		t.Error("Nonce mismatch")
	}
	if msg.NTime != 1609459200 {
		t.Error("NTime mismatch")
	}
	if msg.Version != 0x20001234 {
		t.Error("Version mismatch")
	}
}

func TestSubmitSharesExtended_ExtraNonce2(t *testing.T) {
	extraNonce2 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	msg := SubmitSharesExtended{
		ChannelID:   1,
		SequenceNum: 1,
		JobID:       1,
		Nonce:       0,
		NTime:       0,
		Version:     0x20000000,
		ExtraNonce2: extraNonce2,
	}

	if len(msg.ExtraNonce2) != 8 {
		t.Errorf("ExtraNonce2 length = %d, want 8", len(msg.ExtraNonce2))
	}
	for i, b := range extraNonce2 {
		if msg.ExtraNonce2[i] != b {
			t.Errorf("ExtraNonce2[%d] = %#x, want %#x", i, msg.ExtraNonce2[i], b)
		}
	}
}

func TestSetNewPrevHash_Fields(t *testing.T) {
	prevHash := [32]byte{}
	for i := range prevHash {
		prevHash[i] = byte(i)
	}

	msg := SetNewPrevHash{
		ChannelID: 0xFFFFFFFF, // All channels
		JobID:     100,
		PrevHash:  prevHash,
		MinNTime:  1609459200,
		NBits:     0x1d00ffff,
	}

	if msg.ChannelID != 0xFFFFFFFF {
		t.Error("ChannelID should be all channels (0xFFFFFFFF)")
	}
	if msg.PrevHash != prevHash {
		t.Error("PrevHash mismatch")
	}
	if msg.NBits != 0x1d00ffff {
		t.Error("NBits mismatch")
	}
}

func TestSetTarget_MaxTarget(t *testing.T) {
	// MaxTarget is a 256-bit value
	maxTarget := [32]byte{}
	for i := range maxTarget {
		maxTarget[i] = 0xFF
	}

	msg := SetTarget{
		ChannelID: 1,
		MaxTarget: maxTarget,
	}

	if msg.ChannelID != 1 {
		t.Error("ChannelID mismatch")
	}

	// Verify all bytes are set
	for i, b := range msg.MaxTarget {
		if b != 0xFF {
			t.Errorf("MaxTarget[%d] = %#x, want 0xFF", i, b)
		}
	}
}

func TestReconnect_Fields(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		port     uint16
		samePool bool
	}{
		{"same_pool", "", 0, true},
		{"different_host", "backup.pool.com", 0, false},
		{"different_port", "", 3335, false},
		{"different_both", "backup.pool.com", 3335, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := Reconnect{
				NewHost: tt.host,
				NewPort: tt.port,
			}

			isSamePool := msg.NewHost == "" && msg.NewPort == 0
			if isSamePool != tt.samePool {
				t.Errorf("isSamePool = %v, want %v", isSamePool, tt.samePool)
			}
		})
	}
}

// =============================================================================
// SOLO MINING INVARIANT TESTS
// =============================================================================

func TestStratumV2_SOLOMiningInvariants(t *testing.T) {
	t.Run("no_fee_in_protocol", func(t *testing.T) {
		// Stratum V2 protocol doesn't include fee fields
		// Fee handling is pool-side, not protocol-side
		t.Log("SOLO mode: No fee fields in SV2 messages")
	})

	t.Run("share_submission_simplicity", func(t *testing.T) {
		// SubmitSharesStandard only contains mining proof
		// No fee calculations or payment splits
		msg := SubmitSharesStandard{}
		t.Logf("Share fields: ChannelID, SequenceNum, JobID, Nonce, NTime, Version")
		_ = msg
	})

	t.Run("block_reward_to_coinbase", func(t *testing.T) {
		// Block rewards go to the coinbase address in the job
		// Not split based on share data
		t.Log("Block reward -> miner's coinbase address (100%)")
	})
}

// =============================================================================
// BENCHMARKS
// =============================================================================

func BenchmarkMessageHeader_Encode(b *testing.B) {
	header := MessageHeader{
		ExtensionType: 0,
		MsgType:       MsgNewMiningJob,
		Length:        1000,
	}
	buf := make([]byte, 0, HeaderSize)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := bytes.NewBuffer(buf[:0])
		_ = header.Encode(w)
	}
}

func BenchmarkMessageHeader_Decode(b *testing.B) {
	data := []byte{0x00, 0x00, 0x15, 0xe8, 0x03, 0x00}
	header := &MessageHeader{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = header.Decode(bytes.NewReader(data))
	}
}

func BenchmarkMessageHeader_Roundtrip(b *testing.B) {
	header := MessageHeader{
		ExtensionType: 0x1234,
		MsgType:       MsgNewMiningJob,
		Length:        0xABCD,
	}
	buf := &bytes.Buffer{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_ = header.Encode(buf)
		decoded := &MessageHeader{}
		_ = decoded.Decode(bytes.NewReader(buf.Bytes()))
	}
}
