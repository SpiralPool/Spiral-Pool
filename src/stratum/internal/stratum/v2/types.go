// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package v2 implements the Stratum V2 mining protocol.
//
// Stratum V2 is a binary protocol with authenticated encryption using the
// Noise Protocol Framework. This package handles:
// - Binary message encoding/decoding
// - Noise Protocol handshake and encryption
// - Mining channel management
// - Job distribution and share submission
//
// Reference: https://github.com/stratum-mining/sv2-spec
package v2

import (
	"encoding/binary"
	"errors"
	"io"
)

// Protocol constants
const (
	// DefaultPort is the standard Stratum V2 port
	DefaultPort = 3334

	// MaxMessageSize is the maximum allowed message size (1MB)
	MaxMessageSize = 1 << 20

	// HeaderSize is the size of the message header
	// 2 bytes extension_type + 1 byte msg_type + 3 bytes msg_length = 6
	HeaderSize = 6

	// NoiseHandshakePattern is the Noise protocol pattern used.
	// NX: No static key for initiator, static key for responder.
	// NOTE: Our implementation uses standard compressed secp256k1 pubkeys (33 bytes).
	// The official SV2 spec uses "Noise_NX_Secp256k1+EllSwift_ChaChaPoly_SHA256"
	// with EllSwift 64-byte pubkey encoding (BIP-324). For interoperability with SRI
	// (Stratum Reference Implementation), implement EllSwift and update this string.
	NoiseHandshakePattern = "Noise_NX_secp256k1_ChaChaPoly_SHA256"
)

// Message type identifiers per SV2 spec.
// Reference: https://github.com/stratum-mining/stratum (const_sv2 crate)
const (
	// Common Protocol Messages
	MsgSetupConnection        uint8 = 0x00
	MsgSetupConnectionSuccess uint8 = 0x01
	MsgSetupConnectionError   uint8 = 0x02
	MsgChannelEndpointChanged uint8 = 0x03
	MsgReconnect              uint8 = 0x04

	// Mining Channel
	MsgOpenStandardMiningChannel        uint8 = 0x10
	MsgOpenStandardMiningChannelSuccess uint8 = 0x11
	MsgOpenMiningChannelError           uint8 = 0x12
	MsgOpenExtendedMiningChannel        uint8 = 0x13
	MsgOpenExtendedMiningChannelSuccess uint8 = 0x14
	MsgNewMiningJob                     uint8 = 0x15
	MsgUpdateChannel                    uint8 = 0x16
	MsgUpdateChannelError               uint8 = 0x17
	MsgCloseChannel                     uint8 = 0x18
	MsgSetExtranoncePrefix              uint8 = 0x19

	// Share Submission
	MsgSubmitSharesStandard uint8 = 0x1a
	MsgSubmitSharesExtended uint8 = 0x1b
	MsgSubmitSharesSuccess  uint8 = 0x1c
	MsgSubmitSharesError    uint8 = 0x1d

	// Mining Jobs (continued)
	MsgNewExtendedMiningJob uint8 = 0x1f
	MsgSetNewPrevHash       uint8 = 0x20

	// Difficulty and Target
	MsgSetTarget          uint8 = 0x21
	MsgSetCustomMiningJob uint8 = 0x22

	// Custom Mining Job Responses
	MsgSetCustomMiningJobSuccess uint8 = 0x23
	MsgSetCustomMiningJobError   uint8 = 0x24

	// Group Management
	MsgSetGroupChannel uint8 = 0x25
)

// Protocol flags for SetupConnection
const (
	// Mining Protocol features
	ProtocolFlagRequiresStandardJobs   uint32 = 1 << 0
	ProtocolFlagRequiresWorkSelection  uint32 = 1 << 1
	ProtocolFlagRequiresVersionRolling uint32 = 1 << 2
)

// Error code strings per SV2 spec (STR0_255 wire format).
// Each message type has its own set of valid error codes.
const (
	// SetupConnection.Error codes
	ErrCodeUnsupportedFeatureFlags = "unsupported-feature-flags"
	ErrCodeUnsupportedProtocol     = "unsupported-protocol"
	ErrCodeProtocolVersionMismatch = "protocol-version-mismatch"

	// OpenMiningChannelError codes
	ErrCodeUnknownUser         = "unknown-user"
	ErrCodeMaxTargetOutOfRange = "max-target-out-of-range"

	// SubmitSharesError codes
	ErrCodeInvalidChannelID = "invalid-channel-id"
	ErrCodeStaleShare       = "stale-share"
	ErrCodeDifficultyNotMet = "difficulty-target-not-met"
	ErrCodeRateLimited      = "rate-limited"
)

// Protocol sub-types
const (
	ProtocolMiningV2 uint8 = 0 // Mining Protocol
	ProtocolJobDecl  uint8 = 1 // Job Declaration Protocol
	ProtocolTemplate uint8 = 2 // Template Distribution Protocol
)

// Errors
var (
	ErrMessageTooLarge    = errors.New("message exceeds maximum size")
	ErrInvalidHeader      = errors.New("invalid message header")
	ErrUnknownMessage     = errors.New("unknown message type")
	ErrHandshakeFailed    = errors.New("noise handshake failed")
	ErrNotEncrypted       = errors.New("connection not encrypted")
	ErrChannelNotFound    = errors.New("channel not found")
	ErrInvalidJobID       = errors.New("invalid job ID")
	ErrDuplicateShare     = errors.New("duplicate share")
	ErrInvalidShare       = errors.New("invalid share")
	ErrStaleShare         = errors.New("stale share")
	ErrJobNotFound        = errors.New("job not found")
	ErrLowDifficultyShare = errors.New("share does not meet difficulty target")
)

// MessageHeader represents the SV2 message header
type MessageHeader struct {
	ExtensionType uint16 // Extension type (0 for standard messages)
	MsgType       uint8  // Message type identifier
	Length        uint32 // Payload length (24-bit, max 16MB)
}

// Encode writes the header to a writer
func (h *MessageHeader) Encode(w io.Writer) error {
	buf := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint16(buf[0:2], h.ExtensionType)
	buf[2] = h.MsgType
	// Length is 3 bytes little-endian
	buf[3] = byte(h.Length)
	buf[4] = byte(h.Length >> 8)
	buf[5] = byte(h.Length >> 16)
	_, err := w.Write(buf)
	return err
}

// Decode reads the header from a reader
func (h *MessageHeader) Decode(r io.Reader) error {
	buf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	h.ExtensionType = binary.LittleEndian.Uint16(buf[0:2])
	h.MsgType = buf[2]
	h.Length = uint32(buf[3]) | uint32(buf[4])<<8 | uint32(buf[5])<<16
	return nil
}

// SetupConnection is sent by the client to initiate connection.
// SV2 spec fields: protocol(U8), min_version(U16), max_version(U16),
// flags(U32), endpoint_host(STR0_255), endpoint_port(U16),
// vendor(STR0_255), hardware_version(STR0_255), firmware(STR0_255)
type SetupConnection struct {
	Protocol        uint8  // Protocol identifier (0 = Mining Protocol)
	MinVersion      uint16 // Minimum supported protocol version
	MaxVersion      uint16 // Maximum supported protocol version
	Flags           uint32 // Protocol feature flags
	Endpoint        string // Mining endpoint hostname (STR0_255)
	EndpointPort    uint16 // Mining endpoint port (U16)
	VendorID        string // Vendor identifier (STR0_255)
	HardwareVersion string // Hardware version string (STR0_255)
	FirmwareVersion string // Firmware version string (STR0_255)
}

// SetupConnectionSuccess is sent by the server on successful setup
type SetupConnectionSuccess struct {
	UsedVersion uint16 // Negotiated protocol version
	Flags       uint32 // Accepted feature flags
}

// SetupConnectionError is sent when setup fails
type SetupConnectionError struct {
	Flags     uint32 // Flags that caused the error
	ErrorCode string // Error code (STR0_255 per SV2 spec)
}

// OpenStandardMiningChannel requests a standard mining channel.
// SV2 spec fields: request_id(U32), user_identity(STR0_255),
// nominal_hash_rate(f32), max_target(U256)
type OpenStandardMiningChannel struct {
	RequestID       uint32   // Client-assigned request ID
	UserIdentity    string   // Miner identity (wallet.worker)
	NominalHashRate float32  // Expected hashrate in H/s
	MaxTarget       [32]byte // Maximum difficulty target (U256, little-endian)
}

// OpenStandardMiningChannelSuccess confirms channel creation.
// SV2 spec fields: request_id(U32), channel_id(U32), target(U256),
// extranonce_prefix(B0_32), group_channel_id(U32)
type OpenStandardMiningChannelSuccess struct {
	RequestID        uint32   // Echoed request ID
	ChannelID        uint32   // Server-assigned channel ID
	Target           [32]byte // Initial target (U256, little-endian)
	ExtranoncePrefix []byte   // Extranonce prefix (B0_32)
	GroupChannelID   uint32   // Group channel ID (0 for ungrouped)
}

// OpenMiningChannelError indicates channel open failure
type OpenMiningChannelError struct {
	RequestID uint32 // Echoed request ID
	ErrorCode string // Error code (STR0_255 per SV2 spec)
}

// NewMiningJob distributes a new mining job.
// SV2 spec fields: channel_id(U32), job_id(U32), min_ntime(OPTION[U32]),
// version(U32), merkle_root(U256)
type NewMiningJob struct {
	ChannelID  uint32   // Target channel
	JobID      uint32   // Server-assigned job ID
	MinNTime   *uint32  // OPTION[U32]: nil = future job (wait for SetNewPrevHash), non-nil = active with min ntime
	Version    uint32   // Block version
	MerkleRoot [32]byte // Merkle root of transactions
}

// IsFuture returns true if this is a future job (MinNTime is nil).
func (j *NewMiningJob) IsFuture() bool {
	return j.MinNTime == nil
}

// NewExtendedMiningJob is for extended channels with coinbase control.
// SV2 spec fields: channel_id(U32), job_id(U32), min_ntime(OPTION[U32]),
// version(U32), version_rolling_allowed(BOOL), merkle_path(SEQ0_255[U256]),
// coinbase_tx_prefix(B0_64K), coinbase_tx_suffix(B0_64K)
type NewExtendedMiningJob struct {
	ChannelID             uint32   // Target channel
	JobID                 uint32   // Server-assigned job ID
	MinNTime              *uint32  // OPTION[U32]: nil = future job, non-nil = active with min ntime
	Version               uint32   // Block version
	VersionRollingAllowed bool     // Whether version rolling is allowed
	MerklePath            [][]byte // Merkle branch for coinbase
	CoinbaseTxPrefix      []byte   // Coinbase before extranonce
	CoinbaseTxSuffix      []byte   // Coinbase after extranonce
}

// SetNewPrevHash updates the previous block hash
type SetNewPrevHash struct {
	ChannelID uint32   // Target channel (0xFFFFFFFF for all)
	JobID     uint32   // Job this applies to
	PrevHash  [32]byte // Previous block hash
	MinNTime  uint32   // Minimum ntime
	NBits     uint32   // Network difficulty target
}

// SubmitSharesStandard submits a share for standard channel
type SubmitSharesStandard struct {
	ChannelID   uint32 // Channel ID
	SequenceNum uint32 // Monotonic sequence number
	JobID       uint32 // Job this share is for
	Nonce       uint32 // Nonce value
	NTime       uint32 // nTime value
	Version     uint32 // Block version (with rolled bits)
}

// SubmitSharesExtended submits a share for extended channel
type SubmitSharesExtended struct {
	ChannelID   uint32 // Channel ID
	SequenceNum uint32 // Monotonic sequence number
	JobID       uint32 // Job this share is for
	Nonce       uint32 // Nonce value
	NTime       uint32 // nTime value
	Version     uint32 // Block version
	ExtraNonce2 []byte // Extranonce2 value
}

// SubmitSharesSuccess confirms share acceptance
type SubmitSharesSuccess struct {
	ChannelID           uint32 // Channel ID
	LastSequenceNum     uint32 // Last accepted sequence number
	NewSubmissionsCount uint32 // Number of new submissions accepted
	NewSharesSum        uint64 // Sum of difficulty of accepted shares
}

// SubmitSharesError indicates share rejection
type SubmitSharesError struct {
	ChannelID   uint32 // Channel ID
	SequenceNum uint32 // Rejected sequence number
	ErrorCode   string // Error code (STR0_255 per SV2 spec)
}

// SetTarget updates the target difficulty
type SetTarget struct {
	ChannelID uint32   // Target channel
	MaxTarget [32]byte // Maximum target (U256, little-endian)
}

// Reconnect instructs client to reconnect
type Reconnect struct {
	NewHost string // New pool hostname (empty = same host)
	NewPort uint16 // New port (0 = same port)
}
