// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"testing"
)

// FuzzDecoderPrimitives fuzzes all primitive decoder methods with random bytes
// to ensure none of them panic on arbitrary input.
func FuzzDecoderPrimitives(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xff})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		dec := NewDecoderFromBytes(data)
		dec.ReadU8()

		dec = NewDecoderFromBytes(data)
		dec.ReadU16()

		dec = NewDecoderFromBytes(data)
		dec.ReadU24()

		dec = NewDecoderFromBytes(data)
		dec.ReadU32()

		dec = NewDecoderFromBytes(data)
		dec.ReadU64()

		dec = NewDecoderFromBytes(data)
		dec.ReadF32()

		dec = NewDecoderFromBytes(data)
		dec.ReadBool()
	})
}

// FuzzDecoderStrings fuzzes the length-prefixed string readers to ensure they
// handle malformed length prefixes and truncated payloads without panicking.
func FuzzDecoderStrings(f *testing.F) {
	f.Add([]byte{5, 'h', 'e', 'l', 'l', 'o'})
	f.Add([]byte{0xff})
	f.Add([]byte{0xff, 0xff})
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, data []byte) {
		dec := NewDecoderFromBytes(data)
		dec.ReadB0_255()

		dec = NewDecoderFromBytes(data)
		dec.ReadB0_64K()
	})
}

// FuzzDecoderSequences fuzzes the sequence readers with random data and item
// sizes to ensure they handle oversized counts and truncated data safely.
func FuzzDecoderSequences(f *testing.F) {
	f.Add([]byte{0}, 32)
	f.Add([]byte{1, 0x01, 0x02, 0x03}, 1)
	f.Add([]byte{0xff}, 1)
	f.Add([]byte{0xff, 0xff}, 32)

	f.Fuzz(func(t *testing.T, data []byte, itemSize int) {
		if itemSize <= 0 || itemSize > 1024 {
			return
		}

		dec := NewDecoderFromBytes(data)
		dec.ReadSeq0_255(itemSize)

		dec = NewDecoderFromBytes(data)
		dec.ReadSeq0_64K(itemSize)
	})
}

// FuzzDecodeSetupConnection fuzzes the SetupConnection decoder to verify it
// handles arbitrary byte sequences without panicking.
func FuzzDecodeSetupConnection(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x00, 0x02, 0x00, 0x02, 0x00, 0x00, 0x00})
	f.Add(make([]byte, 256))

	f.Fuzz(func(t *testing.T, data []byte) {
		DecodeSetupConnection(data)
	})
}

// FuzzDecodeSubmitSharesStandard fuzzes the SubmitSharesStandard decoder to
// verify it handles arbitrary byte sequences without panicking.
func FuzzDecodeSubmitSharesStandard(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 52))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		DecodeSubmitSharesStandard(data)
	})
}

// FuzzDecodeOpenStandardMiningChannel fuzzes the OpenStandardMiningChannel
// decoder to verify it handles arbitrary byte sequences without panicking.
func FuzzDecodeOpenStandardMiningChannel(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		DecodeOpenStandardMiningChannel(data)
	})
}

// FuzzEncoderDecoderRoundTrip verifies that encoding followed by decoding
// produces the original values for arbitrary inputs.
func FuzzEncoderDecoderRoundTrip(f *testing.F) {
	f.Add(uint8(42), uint16(1000), uint32(70000), "testminer")
	f.Add(uint8(0), uint16(0), uint32(0), "")
	f.Add(uint8(255), uint16(65535), uint32(4294967295), "x")

	f.Fuzz(func(t *testing.T, u8 uint8, u16 uint16, u32 uint32, str string) {
		if len(str) > 255 {
			str = str[:255]
		}

		enc := NewEncoder()
		enc.WriteU8(u8)
		enc.WriteU16(u16)
		enc.WriteU32(u32)
		enc.WriteB0_255(str)

		dec := NewDecoderFromBytes(enc.Bytes())
		gotU8, err := dec.ReadU8()
		if err != nil {
			t.Fatal(err)
		}
		if gotU8 != u8 {
			t.Errorf("U8 mismatch: got %d want %d", gotU8, u8)
		}

		gotU16, err := dec.ReadU16()
		if err != nil {
			t.Fatal(err)
		}
		if gotU16 != u16 {
			t.Errorf("U16 mismatch: got %d want %d", gotU16, u16)
		}

		gotU32, err := dec.ReadU32()
		if err != nil {
			t.Fatal(err)
		}
		if gotU32 != u32 {
			t.Errorf("U32 mismatch: got %d want %d", gotU32, u32)
		}

		gotStr, err := dec.ReadB0_255()
		if err != nil {
			t.Fatal(err)
		}
		if gotStr != str {
			t.Errorf("String mismatch")
		}
	})
}
