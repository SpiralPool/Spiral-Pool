// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Encoder handles binary encoding of SV2 messages
type Encoder struct {
	buf *bytes.Buffer
}

// NewEncoder creates a new encoder
func NewEncoder() *Encoder {
	return &Encoder{buf: new(bytes.Buffer)}
}

// Reset clears the encoder buffer
func (e *Encoder) Reset() {
	e.buf.Reset()
}

// Bytes returns the encoded bytes
func (e *Encoder) Bytes() []byte {
	return e.buf.Bytes()
}

// WriteU8 writes a uint8
func (e *Encoder) WriteU8(v uint8) {
	e.buf.WriteByte(v)
}

// WriteU16 writes a uint16 (little-endian)
func (e *Encoder) WriteU16(v uint16) {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	e.buf.Write(buf[:])
}

// WriteU24 writes a 24-bit unsigned integer (little-endian)
func (e *Encoder) WriteU24(v uint32) {
	e.buf.WriteByte(byte(v))
	e.buf.WriteByte(byte(v >> 8))
	e.buf.WriteByte(byte(v >> 16))
}

// WriteU32 writes a uint32 (little-endian)
func (e *Encoder) WriteU32(v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	e.buf.Write(buf[:])
}

// WriteU64 writes a uint64 (little-endian)
func (e *Encoder) WriteU64(v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	e.buf.Write(buf[:])
}

// WriteF32 writes a float32 (IEEE 754, little-endian)
func (e *Encoder) WriteF32(v float32) {
	e.WriteU32(math.Float32bits(v))
}

// WriteBool writes a boolean as a single byte
func (e *Encoder) WriteBool(v bool) {
	if v {
		e.buf.WriteByte(1)
	} else {
		e.buf.WriteByte(0)
	}
}

// WriteBytes writes raw bytes
func (e *Encoder) WriteBytes(v []byte) {
	e.buf.Write(v)
}

// WriteFixedBytes writes exactly n bytes, padding with zeros if needed
func (e *Encoder) WriteFixedBytes(v []byte, n int) {
	if len(v) >= n {
		e.buf.Write(v[:n])
	} else {
		e.buf.Write(v)
		// Pad with zeros
		for i := len(v); i < n; i++ {
			e.buf.WriteByte(0)
		}
	}
}

// WriteB0_255 writes a SV2 STR0_255 / B0_255 string (1-byte length prefix, max 255 bytes)
func (e *Encoder) WriteB0_255(s string) error {
	if len(s) > 255 {
		return errors.New("string too long for B0_255")
	}
	e.buf.WriteByte(byte(len(s)))
	e.buf.WriteString(s)
	return nil
}

// WriteB0_32 writes a SV2 B0_32 byte sequence (1-byte length prefix, max 32 bytes)
func (e *Encoder) WriteB0_32(v []byte) error {
	if len(v) > 32 {
		return errors.New("bytes too long for B0_32")
	}
	e.buf.WriteByte(byte(len(v)))
	e.buf.Write(v)
	return nil
}

// WriteB0_64K writes a SV2 B0_64K string (2-byte length prefix, max 64KB)
func (e *Encoder) WriteB0_64K(s string) error {
	if len(s) > 65535 {
		return errors.New("string too long for B0_64K")
	}
	e.WriteU16(uint16(len(s)))
	e.buf.WriteString(s)
	return nil
}

// WriteB0_64KBytes writes a SV2 B0_64K byte sequence (2-byte length prefix, max 64KB)
func (e *Encoder) WriteB0_64KBytes(v []byte) error {
	if len(v) > 65535 {
		return errors.New("bytes too long for B0_64K")
	}
	e.WriteU16(uint16(len(v)))
	e.buf.Write(v)
	return nil
}

// WriteOptionU32 writes a SV2 OPTION[U32] (1 byte presence flag + optional U32)
func (e *Encoder) WriteOptionU32(v *uint32) {
	if v == nil {
		e.buf.WriteByte(0) // None
	} else {
		e.buf.WriteByte(1) // Some
		e.WriteU32(*v)
	}
}

// WriteSeq0_255 writes a sequence of fixed-size elements with 1-byte count
func (e *Encoder) WriteSeq0_255(items [][]byte, itemSize int) error {
	if len(items) > 255 {
		return errors.New("too many items for Seq0_255")
	}
	e.buf.WriteByte(byte(len(items)))
	for _, item := range items {
		e.WriteFixedBytes(item, itemSize)
	}
	return nil
}

// WriteSeq0_64K writes a sequence of fixed-size elements with 2-byte count
func (e *Encoder) WriteSeq0_64K(items [][]byte, itemSize int) error {
	if len(items) > 65535 {
		return errors.New("too many items for Seq0_64K")
	}
	e.WriteU16(uint16(len(items)))
	for _, item := range items {
		e.WriteFixedBytes(item, itemSize)
	}
	return nil
}

// Decoder handles binary decoding of SV2 messages
type Decoder struct {
	r io.Reader
}

// NewDecoder creates a new decoder
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// NewDecoderFromBytes creates a decoder from a byte slice
func NewDecoderFromBytes(data []byte) *Decoder {
	return &Decoder{r: bytes.NewReader(data)}
}

// ReadU8 reads a uint8
func (d *Decoder) ReadU8() (uint8, error) {
	var buf [1]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// ReadU16 reads a uint16 (little-endian)
func (d *Decoder) ReadU16() (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

// ReadU24 reads a 24-bit unsigned integer (little-endian)
func (d *Decoder) ReadU24() (uint32, error) {
	var buf [3]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16, nil
}

// ReadU32 reads a uint32 (little-endian)
func (d *Decoder) ReadU32() (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

// ReadU64 reads a uint64 (little-endian)
func (d *Decoder) ReadU64() (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// ReadF32 reads a float32 (IEEE 754, little-endian)
func (d *Decoder) ReadF32() (float32, error) {
	bits, err := d.ReadU32()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(bits), nil
}

// ReadBool reads a boolean from a single byte
func (d *Decoder) ReadBool() (bool, error) {
	v, err := d.ReadU8()
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// ReadBytes reads exactly n bytes
func (d *Decoder) ReadBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ReadFixedBytes32 reads exactly 32 bytes into a fixed-size array
func (d *Decoder) ReadFixedBytes32() ([32]byte, error) {
	var buf [32]byte
	if _, err := io.ReadFull(d.r, buf[:]); err != nil {
		return buf, err
	}
	return buf, nil
}

// ReadB0_255 reads a SV2 STR0_255 / B0_255 string
func (d *Decoder) ReadB0_255() (string, error) {
	length, err := d.ReadU8()
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// ReadB0_32 reads a SV2 B0_32 byte sequence (1-byte length prefix, max 32 bytes)
func (d *Decoder) ReadB0_32() ([]byte, error) {
	length, err := d.ReadU8()
	if err != nil {
		return nil, err
	}
	if length > 32 {
		return nil, fmt.Errorf("B0_32 length %d exceeds max 32", length)
	}
	if length == 0 {
		return nil, nil
	}
	return d.ReadBytes(int(length))
}

// ReadB0_64K reads a SV2 B0_64K string
func (d *Decoder) ReadB0_64K() (string, error) {
	length, err := d.ReadU16()
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// ReadB0_64KBytes reads a SV2 B0_64K byte sequence
func (d *Decoder) ReadB0_64KBytes() ([]byte, error) {
	length, err := d.ReadU16()
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil
	}
	return d.ReadBytes(int(length))
}

// ReadOptionU32 reads a SV2 OPTION[U32] (1 byte presence flag + optional U32)
func (d *Decoder) ReadOptionU32() (*uint32, error) {
	present, err := d.ReadU8()
	if err != nil {
		return nil, err
	}
	if present == 0 {
		return nil, nil // None
	}
	v, err := d.ReadU32()
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ReadSeq0_255 reads a sequence of fixed-size elements with 1-byte count
func (d *Decoder) ReadSeq0_255(itemSize int) ([][]byte, error) {
	count, err := d.ReadU8()
	if err != nil {
		return nil, err
	}
	items := make([][]byte, count)
	for i := 0; i < int(count); i++ {
		items[i], err = d.ReadBytes(itemSize)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// ReadSeq0_64K reads a sequence of fixed-size elements with 2-byte count
func (d *Decoder) ReadSeq0_64K(itemSize int) ([][]byte, error) {
	count, err := d.ReadU16()
	if err != nil {
		return nil, err
	}
	if int(count)*itemSize > 4*1024*1024 {
		return nil, fmt.Errorf("sequence too large: %d items of %d bytes", count, itemSize)
	}
	items := make([][]byte, count)
	for i := 0; i < int(count); i++ {
		items[i], err = d.ReadBytes(itemSize)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// EncodeMessage encodes a complete SV2 message with header
func EncodeMessage(msgType uint8, payload []byte) []byte {
	header := MessageHeader{
		ExtensionType: 0, // Standard message
		MsgType:       msgType,
		Length:        uint32(len(payload)),
	}

	buf := new(bytes.Buffer)
	_ = header.Encode(buf) // #nosec G104 - bytes.Buffer.Write never fails
	_, _ = buf.Write(payload)
	return buf.Bytes()
}

// EncodeSetupConnection encodes a SetupConnection message
func EncodeSetupConnection(msg *SetupConnection) ([]byte, error) {
	enc := NewEncoder()
	enc.WriteU8(msg.Protocol)
	enc.WriteU16(msg.MinVersion)
	enc.WriteU16(msg.MaxVersion)
	enc.WriteU32(msg.Flags)
	if err := enc.WriteB0_255(msg.Endpoint); err != nil {
		return nil, err
	}
	enc.WriteU16(msg.EndpointPort)
	if err := enc.WriteB0_255(msg.VendorID); err != nil {
		return nil, err
	}
	if err := enc.WriteB0_255(msg.HardwareVersion); err != nil {
		return nil, err
	}
	if err := enc.WriteB0_255(msg.FirmwareVersion); err != nil {
		return nil, err
	}
	return EncodeMessage(MsgSetupConnection, enc.Bytes()), nil
}

// DecodeSetupConnection decodes a SetupConnection message
func DecodeSetupConnection(data []byte) (*SetupConnection, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SetupConnection{}
	var err error

	if msg.Protocol, err = dec.ReadU8(); err != nil {
		return nil, err
	}
	if msg.MinVersion, err = dec.ReadU16(); err != nil {
		return nil, err
	}
	if msg.MaxVersion, err = dec.ReadU16(); err != nil {
		return nil, err
	}
	if msg.Flags, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.Endpoint, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	if msg.EndpointPort, err = dec.ReadU16(); err != nil {
		return nil, err
	}
	if msg.VendorID, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	if msg.HardwareVersion, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	if msg.FirmwareVersion, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeSetupConnectionSuccess encodes a SetupConnectionSuccess message
func EncodeSetupConnectionSuccess(msg *SetupConnectionSuccess) []byte {
	enc := NewEncoder()
	enc.WriteU16(msg.UsedVersion)
	enc.WriteU32(msg.Flags)
	return EncodeMessage(MsgSetupConnectionSuccess, enc.Bytes())
}

// EncodeSetupConnectionError encodes a SetupConnectionError message
func EncodeSetupConnectionError(msg *SetupConnectionError) ([]byte, error) {
	enc := NewEncoder()
	enc.WriteU32(msg.Flags)
	if err := enc.WriteB0_255(msg.ErrorCode); err != nil {
		return nil, err
	}
	return EncodeMessage(MsgSetupConnectionError, enc.Bytes()), nil
}

// EncodeOpenStandardMiningChannel encodes an OpenStandardMiningChannel message
func EncodeOpenStandardMiningChannel(msg *OpenStandardMiningChannel) ([]byte, error) {
	enc := NewEncoder()
	enc.WriteU32(msg.RequestID)
	if err := enc.WriteB0_255(msg.UserIdentity); err != nil {
		return nil, err
	}
	enc.WriteF32(msg.NominalHashRate)
	enc.WriteBytes(msg.MaxTarget[:]) // U256 (32 bytes)
	return EncodeMessage(MsgOpenStandardMiningChannel, enc.Bytes()), nil
}

// DecodeOpenStandardMiningChannel decodes an OpenStandardMiningChannel message
func DecodeOpenStandardMiningChannel(data []byte) (*OpenStandardMiningChannel, error) {
	dec := NewDecoderFromBytes(data)
	msg := &OpenStandardMiningChannel{}
	var err error

	if msg.RequestID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.UserIdentity, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	if msg.NominalHashRate, err = dec.ReadF32(); err != nil {
		return nil, err
	}
	if msg.MaxTarget, err = dec.ReadFixedBytes32(); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeOpenStandardMiningChannelSuccess encodes the success response
func EncodeOpenStandardMiningChannelSuccess(msg *OpenStandardMiningChannelSuccess) ([]byte, error) {
	enc := NewEncoder()
	enc.WriteU32(msg.RequestID)
	enc.WriteU32(msg.ChannelID)
	enc.WriteBytes(msg.Target[:]) // U256 (32 bytes)
	if err := enc.WriteB0_32(msg.ExtranoncePrefix); err != nil {
		return nil, err
	}
	enc.WriteU32(msg.GroupChannelID)
	return EncodeMessage(MsgOpenStandardMiningChannelSuccess, enc.Bytes()), nil
}

// DecodeOpenStandardMiningChannelSuccess decodes the success response
func DecodeOpenStandardMiningChannelSuccess(data []byte) (*OpenStandardMiningChannelSuccess, error) {
	dec := NewDecoderFromBytes(data)
	msg := &OpenStandardMiningChannelSuccess{}
	var err error

	if msg.RequestID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.Target, err = dec.ReadFixedBytes32(); err != nil {
		return nil, err
	}
	if msg.ExtranoncePrefix, err = dec.ReadB0_32(); err != nil {
		return nil, err
	}
	if msg.GroupChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeOpenMiningChannelError encodes a channel open error
func EncodeOpenMiningChannelError(msg *OpenMiningChannelError) ([]byte, error) {
	enc := NewEncoder()
	enc.WriteU32(msg.RequestID)
	if err := enc.WriteB0_255(msg.ErrorCode); err != nil {
		return nil, err
	}
	return EncodeMessage(MsgOpenMiningChannelError, enc.Bytes()), nil
}

// DecodeOpenMiningChannelError decodes a channel open error
func DecodeOpenMiningChannelError(data []byte) (*OpenMiningChannelError, error) {
	dec := NewDecoderFromBytes(data)
	msg := &OpenMiningChannelError{}
	var err error
	if msg.RequestID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.ErrorCode, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeNewMiningJob encodes a NewMiningJob message
func EncodeNewMiningJob(msg *NewMiningJob) []byte {
	enc := NewEncoder()
	enc.WriteU32(msg.ChannelID)
	enc.WriteU32(msg.JobID)
	enc.WriteOptionU32(msg.MinNTime)
	enc.WriteU32(msg.Version)
	enc.WriteBytes(msg.MerkleRoot[:])
	return EncodeMessage(MsgNewMiningJob, enc.Bytes())
}

// DecodeNewMiningJob decodes a NewMiningJob message
func DecodeNewMiningJob(data []byte) (*NewMiningJob, error) {
	dec := NewDecoderFromBytes(data)
	msg := &NewMiningJob{}
	var err error
	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.JobID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.MinNTime, err = dec.ReadOptionU32(); err != nil {
		return nil, err
	}
	if msg.Version, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.MerkleRoot, err = dec.ReadFixedBytes32(); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeSetNewPrevHash encodes a SetNewPrevHash message
func EncodeSetNewPrevHash(msg *SetNewPrevHash) []byte {
	enc := NewEncoder()
	enc.WriteU32(msg.ChannelID)
	enc.WriteU32(msg.JobID)
	enc.WriteBytes(msg.PrevHash[:])
	enc.WriteU32(msg.MinNTime)
	enc.WriteU32(msg.NBits)
	return EncodeMessage(MsgSetNewPrevHash, enc.Bytes())
}

// DecodeSubmitSharesStandard decodes a SubmitSharesStandard message
func DecodeSubmitSharesStandard(data []byte) (*SubmitSharesStandard, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SubmitSharesStandard{}
	var err error

	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.SequenceNum, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.JobID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.Nonce, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.NTime, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.Version, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	return msg, nil
}

// EncodeSubmitSharesStandard encodes a SubmitSharesStandard message
func EncodeSubmitSharesStandard(msg *SubmitSharesStandard) []byte {
	enc := NewEncoder()
	enc.WriteU32(msg.ChannelID)
	enc.WriteU32(msg.SequenceNum)
	enc.WriteU32(msg.JobID)
	enc.WriteU32(msg.Nonce)
	enc.WriteU32(msg.NTime)
	enc.WriteU32(msg.Version)
	return EncodeMessage(MsgSubmitSharesStandard, enc.Bytes())
}

// EncodeSubmitSharesSuccess encodes a SubmitSharesSuccess message
func EncodeSubmitSharesSuccess(msg *SubmitSharesSuccess) []byte {
	enc := NewEncoder()
	enc.WriteU32(msg.ChannelID)
	enc.WriteU32(msg.LastSequenceNum)
	enc.WriteU32(msg.NewSubmissionsCount)
	enc.WriteU64(msg.NewSharesSum)
	return EncodeMessage(MsgSubmitSharesSuccess, enc.Bytes())
}

// EncodeSubmitSharesError encodes a SubmitSharesError message
func EncodeSubmitSharesError(msg *SubmitSharesError) ([]byte, error) {
	enc := NewEncoder()
	enc.WriteU32(msg.ChannelID)
	enc.WriteU32(msg.SequenceNum)
	if err := enc.WriteB0_255(msg.ErrorCode); err != nil {
		return nil, err
	}
	return EncodeMessage(MsgSubmitSharesError, enc.Bytes()), nil
}

// EncodeSetTarget encodes a SetTarget message
func EncodeSetTarget(msg *SetTarget) []byte {
	enc := NewEncoder()
	enc.WriteU32(msg.ChannelID)
	enc.WriteBytes(msg.MaxTarget[:])
	return EncodeMessage(MsgSetTarget, enc.Bytes())
}

// DecodeSetupConnectionSuccess decodes a SetupConnectionSuccess message
func DecodeSetupConnectionSuccess(data []byte) (*SetupConnectionSuccess, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SetupConnectionSuccess{}
	var err error
	if msg.UsedVersion, err = dec.ReadU16(); err != nil {
		return nil, err
	}
	if msg.Flags, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	return msg, nil
}

// DecodeSetupConnectionError decodes a SetupConnectionError message
func DecodeSetupConnectionError(data []byte) (*SetupConnectionError, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SetupConnectionError{}
	var err error
	if msg.Flags, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.ErrorCode, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	return msg, nil
}

// DecodeSetNewPrevHash decodes a SetNewPrevHash message
func DecodeSetNewPrevHash(data []byte) (*SetNewPrevHash, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SetNewPrevHash{}
	var err error
	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.JobID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.PrevHash, err = dec.ReadFixedBytes32(); err != nil {
		return nil, err
	}
	if msg.MinNTime, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.NBits, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	return msg, nil
}

// DecodeSubmitSharesSuccess decodes a SubmitSharesSuccess message
func DecodeSubmitSharesSuccess(data []byte) (*SubmitSharesSuccess, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SubmitSharesSuccess{}
	var err error
	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.LastSequenceNum, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.NewSubmissionsCount, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.NewSharesSum, err = dec.ReadU64(); err != nil {
		return nil, err
	}
	return msg, nil
}

// DecodeSubmitSharesError decodes a SubmitSharesError message
func DecodeSubmitSharesError(data []byte) (*SubmitSharesError, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SubmitSharesError{}
	var err error
	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.SequenceNum, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.ErrorCode, err = dec.ReadB0_255(); err != nil {
		return nil, err
	}
	return msg, nil
}

// DecodeSetTarget decodes a SetTarget message
func DecodeSetTarget(data []byte) (*SetTarget, error) {
	dec := NewDecoderFromBytes(data)
	msg := &SetTarget{}
	var err error
	if msg.ChannelID, err = dec.ReadU32(); err != nil {
		return nil, err
	}
	if msg.MaxTarget, err = dec.ReadFixedBytes32(); err != nil {
		return nil, err
	}
	return msg, nil
}
