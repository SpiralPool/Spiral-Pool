// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/crypto"
)

// Message-level conformance (channel bit, SetupConnection, UpdateChannel, SetTarget
// from vardiff) and extended channels, over TCP against the server wired to the real
// share validator; the harness is in pipeline_test.go.

func TestSV2Encoding_ChannelMessageBit(t *testing.T) {
	channel := []uint8{MsgChannelEndpointChanged, MsgNewMiningJob, MsgUpdateChannel, MsgUpdateChannelError,
		MsgCloseChannel, MsgSetExtranoncePrefix, MsgSubmitSharesStandard, MsgSubmitSharesExtended,
		MsgSubmitSharesSuccess, MsgSubmitSharesError, MsgNewExtendedMiningJob, MsgSetNewPrevHash, MsgSetTarget,
		MsgSetCustomMiningJob, MsgSetCustomMiningJobSuccess, MsgSetCustomMiningJobError}
	plain := []uint8{MsgSetupConnection, MsgSetupConnectionSuccess, MsgSetupConnectionError, MsgReconnect,
		MsgOpenStandardMiningChannel, MsgOpenStandardMiningChannelSuccess, MsgOpenMiningChannelError,
		MsgOpenExtendedMiningChannel, MsgOpenExtendedMiningChannelSuccess, MsgSetGroupChannel}
	for _, m := range channel {
		if ext := binary.LittleEndian.Uint16(EncodeMessage(m, nil)[:2]); ext != 0x8000 {
			t.Errorf("message 0x%02x extension_type = 0x%04x, want 0x8000", m, ext)
		}
	}
	for _, m := range plain {
		if ext := binary.LittleEndian.Uint16(EncodeMessage(m, nil)[:2]); ext != 0 {
			t.Errorf("message 0x%02x extension_type = 0x%04x, want 0", m, ext)
		}
	}
}

func TestSV2Encoding_NewMessagesRoundTrip(t *testing.T) {
	ntime := uint32(1789500000)
	path := [][]byte{bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)}
	cases := []struct {
		name   string
		encode func() ([]byte, error)
		decode func([]byte) (interface{}, error)
		want   interface{}
	}{
		{"SetupConnection", func() ([]byte, error) {
			return EncodeSetupConnection(&SetupConnection{Protocol: 0, MinVersion: 2, MaxVersion: 2, Flags: 5,
				Endpoint: "pool.lan", EndpointPort: 3334, VendorID: "v", HardwareVersion: "h", FirmwareVersion: "f", DeviceID: "dev-7"})
		}, func(b []byte) (interface{}, error) { return DecodeSetupConnection(b) },
			&SetupConnection{Protocol: 0, MinVersion: 2, MaxVersion: 2, Flags: 5,
				Endpoint: "pool.lan", EndpointPort: 3334, VendorID: "v", HardwareVersion: "h", FirmwareVersion: "f", DeviceID: "dev-7"}},
		{"OpenExtendedMiningChannel", func() ([]byte, error) {
			return EncodeOpenExtendedMiningChannel(&OpenExtendedMiningChannel{
				OpenStandardMiningChannel: OpenStandardMiningChannel{RequestID: 3, UserIdentity: "addr.rig", NominalHashRate: 1e9, MaxTarget: NBitsToU256(0x1d00ffff)},
				MinExtranonceSize:         8})
		}, func(b []byte) (interface{}, error) { return DecodeOpenExtendedMiningChannel(b) },
			&OpenExtendedMiningChannel{
				OpenStandardMiningChannel: OpenStandardMiningChannel{RequestID: 3, UserIdentity: "addr.rig", NominalHashRate: 1e9, MaxTarget: NBitsToU256(0x1d00ffff)},
				MinExtranonceSize:         8}},
		{"OpenExtendedMiningChannelSuccess", func() ([]byte, error) {
			return EncodeOpenExtendedMiningChannelSuccess(&OpenExtendedMiningChannelSuccess{RequestID: 3, ChannelID: 9,
				Target: NBitsToU256(0x207fffff), ExtranonceSize: 8, ExtranoncePrefix: []byte{0xc0, 0, 0, 1}, GroupChannelID: 0})
		}, func(b []byte) (interface{}, error) { return DecodeOpenExtendedMiningChannelSuccess(b) },
			&OpenExtendedMiningChannelSuccess{RequestID: 3, ChannelID: 9,
				Target: NBitsToU256(0x207fffff), ExtranonceSize: 8, ExtranoncePrefix: []byte{0xc0, 0, 0, 1}, GroupChannelID: 0}},
		{"UpdateChannel", func() ([]byte, error) {
			return EncodeUpdateChannel(&UpdateChannel{ChannelID: 4, NominalHashRate: 2e6, MaximumTarget: NBitsToU256(0x1e00ffff)}), nil
		}, func(b []byte) (interface{}, error) { return DecodeUpdateChannel(b) },
			&UpdateChannel{ChannelID: 4, NominalHashRate: 2e6, MaximumTarget: NBitsToU256(0x1e00ffff)}},
		{"UpdateChannelError", func() ([]byte, error) {
			return EncodeUpdateChannelError(&UpdateChannelError{ChannelID: 4, ErrorCode: ErrCodeInvalidChannelID})
		}, func(b []byte) (interface{}, error) { return DecodeUpdateChannelError(b) },
			&UpdateChannelError{ChannelID: 4, ErrorCode: ErrCodeInvalidChannelID}},
		{"SubmitSharesExtended", func() ([]byte, error) {
			return EncodeSubmitSharesExtended(&SubmitSharesExtended{ChannelID: 1, SequenceNum: 2, JobID: 3, Nonce: 4, NTime: 5, Version: 6,
				Extranonce: []byte{1, 2, 3, 4, 5, 6, 7, 8}})
		}, func(b []byte) (interface{}, error) { return DecodeSubmitSharesExtended(b) },
			&SubmitSharesExtended{ChannelID: 1, SequenceNum: 2, JobID: 3, Nonce: 4, NTime: 5, Version: 6,
				Extranonce: []byte{1, 2, 3, 4, 5, 6, 7, 8}}},
		{"NewExtendedMiningJob", func() ([]byte, error) {
			return EncodeNewExtendedMiningJob(&NewExtendedMiningJob{ChannelID: 1, JobID: 2, MinNTime: &ntime, Version: 0x20000000,
				VersionRollingAllowed: true, MerklePath: path, CoinbaseTxPrefix: []byte{0xaa, 0xbb}, CoinbaseTxSuffix: []byte{0xcc}})
		}, func(b []byte) (interface{}, error) { return DecodeNewExtendedMiningJob(b) },
			&NewExtendedMiningJob{ChannelID: 1, JobID: 2, MinNTime: &ntime, Version: 0x20000000,
				VersionRollingAllowed: true, MerklePath: path, CoinbaseTxPrefix: []byte{0xaa, 0xbb}, CoinbaseTxSuffix: []byte{0xcc}}},
	}
	for _, tc := range cases {
		frame, err := tc.encode()
		if err != nil {
			t.Fatalf("%s: encode: %v", tc.name, err)
		}
		got, err := tc.decode(frame[HeaderSize:])
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: round trip = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	if _, err := EncodeNewExtendedMiningJob(&NewExtendedMiningJob{MerklePath: [][]byte{{1, 2, 3}}}); err == nil {
		t.Error("a merkle path entry that is not 32 bytes was encoded")
	}
}

func TestSV2SetupConnection_DeviceIDIsOptionalOnTheWire(t *testing.T) {
	// A SetupConnection as clients built it before device_id existed.
	enc := NewEncoder()
	enc.WriteU8(ProtocolMiningV2)
	enc.WriteU16(2)
	enc.WriteU16(2)
	enc.WriteU32(0)
	_ = enc.WriteB0_255("host")
	enc.WriteU16(3334)
	_ = enc.WriteB0_255("vendor")
	_ = enc.WriteB0_255("hw")
	_ = enc.WriteB0_255("fw")
	withoutDeviceID := enc.Bytes()

	msg, err := DecodeSetupConnection(withoutDeviceID)
	if err != nil || msg.DeviceID != "" || msg.FirmwareVersion != "fw" {
		t.Fatalf("decode without device_id = %+v, %v", msg, err)
	}
	if _, err := DecodeSetupConnection(append(withoutDeviceID, 5, 'a')); err == nil {
		t.Error("a truncated device_id was accepted")
	}
}

func setupMessage(flags uint32) *SetupConnection {
	return &SetupConnection{Protocol: ProtocolMiningV2, MinVersion: 2, MaxVersion: 2, Flags: flags, VendorID: "sv2-test", DeviceID: "unit-1"}
}

// SetupConnection.Success's flags are the server's requirements. Echoing the
// client's REQUIRES_STANDARD_JOBS (bit 0) back would tell it the server requires a
// fixed version.
func TestSV2SetupConnection_SuccessFlagsAreTheServersOwn(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	msg, _ := EncodeSetupConnection(setupMessage(ProtocolFlagRequiresStandardJobs | ProtocolFlagRequiresVersionRolling))
	client.send(msg)
	ok, err := DecodeSetupConnectionSuccess(client.expect(MsgSetupConnectionSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if ok.Flags != 0 || ok.UsedVersion != 2 {
		t.Errorf("SetupConnection.Success = %+v, want version 2 and flags 0", ok)
	}
}

func TestSV2SetupConnection_RefusalsNameTheProblemAndClose(t *testing.T) {
	cases := []struct {
		name     string
		msg      *SetupConnection
		code     string
		errFlags uint32
	}{
		{"job declaration protocol", &SetupConnection{Protocol: ProtocolJobDecl, MinVersion: 2, MaxVersion: 2}, ErrCodeUnsupportedProtocol, 0},
		{"work selection", setupMessage(ProtocolFlagRequiresWorkSelection | ProtocolFlagRequiresVersionRolling), ErrCodeUnsupportedFeatureFlags, ProtocolFlagRequiresWorkSelection},
		{"version 3 only", &SetupConnection{Protocol: ProtocolMiningV2, MinVersion: 3, MaxVersion: 3}, ErrCodeProtocolVersionMismatch, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := newSV2TestPool()
			srv, _ := startSV2TestServer(t, pool)
			client := dialSV2(t, srv)
			msg, _ := EncodeSetupConnection(tc.msg)
			client.send(msg)
			refused, err := DecodeSetupConnectionError(client.expect(MsgSetupConnectionError))
			if err != nil {
				t.Fatal(err)
			}
			if refused.ErrorCode != tc.code || refused.Flags != tc.errFlags {
				t.Errorf("SetupConnection.Error = %+v, want code %q flags %#x", refused, tc.code, tc.errFlags)
			}
			// The server must close the connection: a read ends with EOF or a reset,
			// not by running into the deadline.
			_ = client.raw.SetReadDeadline(time.Now().Add(3 * time.Second))
			var header MessageHeader
			err = header.Decode(client.conn)
			var netErr net.Error
			if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
				t.Errorf("connection stayed open after a refused setup (read: %v)", err)
			}
		})
	}
}

func TestSV2UpdateChannel_TightensTheTargetAndResendsTheJob(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openChannel(1, "minerA.rig1")

	maxTarget := NBitsToU256(0x2000ffff)
	client.send(EncodeUpdateChannel(&UpdateChannel{ChannelID: ch.open.ChannelID, NominalHashRate: 2e6, MaximumTarget: maxTarget}))

	setTarget, err := DecodeSetTarget(client.expect(MsgSetTarget))
	if err != nil {
		t.Fatal(err)
	}
	newTarget := U256ToTarget(setTarget.MaxTarget)
	if newTarget.Sign() <= 0 || newTarget.Cmp(U256ToTarget(maxTarget)) > 0 {
		t.Fatalf("SetTarget target %x is not within the requested maximum %x", newTarget, U256ToTarget(maxTarget))
	}
	job, err := DecodeNewMiningJob(client.expect(MsgNewMiningJob))
	if err != nil {
		t.Fatal(err)
	}
	if job.MinNTime == nil || job.JobID == ch.job.JobID {
		t.Fatalf("expected a new active job after SetTarget, got %+v", job)
	}

	retargeted := &sv2TestChannel{open: &OpenStandardMiningChannelSuccess{ChannelID: ch.open.ChannelID, Target: setTarget.MaxTarget}, job: job, prev: ch.prev}
	nonce, header := mineBlock(t, retargeted)
	client.submit(ch.open.ChannelID, 2, job.JobID, nonce, ch.prev.MinNTime, job.Version)
	client.expect(MsgSubmitSharesSuccess)
	submitted, results := pool.recorded()
	if len(submitted) != 1 || !results[0].Accepted {
		t.Fatalf("validator results %+v", results)
	}
	if got := pool.validator.ShareTarget(submitted[0].Difficulty); got.Cmp(newTarget) < 0 {
		t.Errorf("share validated at a target %x harder than the %x sent to the miner", got, newTarget)
	}
	checkBlock(t, results[0], header, sv2PayoutA)

	client.send(EncodeUpdateChannel(&UpdateChannel{ChannelID: 999, NominalHashRate: 1, MaximumTarget: maxTarget}))
	refused, err := DecodeUpdateChannelError(client.expect(MsgUpdateChannelError))
	if err != nil || refused.ErrorCode != ErrCodeInvalidChannelID || refused.ChannelID != 999 {
		t.Errorf("UpdateChannel.Error = %+v, %v", refused, err)
	}
}

// mineFrom is mineBlock starting at a nonce, so repeated shares differ.
func mineFrom(t *testing.T, ch *sv2TestChannel, start uint32) uint32 {
	t.Helper()
	target := U256ToTarget(ch.open.Target)
	for nonce := start; nonce < start+1<<20; nonce++ {
		hash := new(big.Int).SetBytes(crypto.ReverseBytes(crypto.SHA256d(sv2Header(ch.job, ch.prev, nonce))))
		if hash.Cmp(target) <= 0 {
			return nonce
		}
	}
	t.Fatal("no nonce met the target")
	return 0
}

func TestSV2Vardiff_FastSharesRaiseDifficultyWithSetTarget(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool, func(cfg *ServerConfig) {
		cfg.VarDiff = config.VarDiffConfig{Enabled: true, MinDiff: 1e-12, MaxDiff: 1e12,
			TargetTime: 1000, RetargetTime: 0.05, VariancePercent: 50}
	})
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openChannel(1, "minerA.rig1")

	first := mineFrom(t, ch, 0)
	client.submit(ch.open.ChannelID, 1, ch.job.JobID, first, ch.prev.MinNTime, ch.job.Version)
	client.expect(MsgSubmitSharesSuccess)
	time.Sleep(80 * time.Millisecond)
	second := mineFrom(t, ch, first+1)
	client.submit(ch.open.ChannelID, 2, ch.job.JobID, second, ch.prev.MinNTime, ch.job.Version)
	client.expect(MsgSubmitSharesSuccess)

	setTarget, err := DecodeSetTarget(client.expect(MsgSetTarget))
	if err != nil {
		t.Fatal(err)
	}
	want := targetToU256(pool.validator.ShareTarget(4e-9))
	if setTarget.MaxTarget != want {
		t.Errorf("SetTarget target %x, want difficulty 4e-9's %x", U256ToTarget(setTarget.MaxTarget), U256ToTarget(want))
	}
	job, err := DecodeNewMiningJob(client.expect(MsgNewMiningJob))
	if err != nil {
		t.Fatal(err)
	}

	retargeted := &sv2TestChannel{open: &OpenStandardMiningChannelSuccess{Target: setTarget.MaxTarget}, job: job, prev: ch.prev}
	third := mineFrom(t, retargeted, 0)
	client.submit(ch.open.ChannelID, 3, job.JobID, third, ch.prev.MinNTime, job.Version)
	client.expect(MsgSubmitSharesSuccess)
	submitted, _ := pool.recorded()
	if len(submitted) != 3 || submitted[0].Difficulty != 1e-9 || submitted[2].Difficulty != 4e-9 {
		t.Errorf("share difficulties %v, %v, want 1e-9 before the retarget and 4e-9 after", submitted[0].Difficulty, submitted[len(submitted)-1].Difficulty)
	}
}

// ── extended channels ──────────────────────────────────────────────────────

type sv2ExtendedChannel struct {
	open *OpenExtendedMiningChannelSuccess
	job  *NewExtendedMiningJob
	prev *SetNewPrevHash
}

func (c *sv2TestClient) openExtended(requestID uint32, identity string, minExtranonce uint16) *sv2ExtendedChannel {
	c.t.Helper()
	msg, err := EncodeOpenExtendedMiningChannel(&OpenExtendedMiningChannel{
		OpenStandardMiningChannel: OpenStandardMiningChannel{RequestID: requestID, UserIdentity: identity, NominalHashRate: 1e6},
		MinExtranonceSize:         minExtranonce,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(msg)
	open, err := DecodeOpenExtendedMiningChannelSuccess(c.expect(MsgOpenExtendedMiningChannelSuccess))
	if err != nil {
		c.t.Fatal(err)
	}
	job, err := DecodeNewExtendedMiningJob(c.expect(MsgNewExtendedMiningJob))
	if err != nil {
		c.t.Fatal(err)
	}
	prev, err := DecodeSetNewPrevHash(c.expect(MsgSetNewPrevHash))
	if err != nil {
		c.t.Fatal(err)
	}
	return &sv2ExtendedChannel{open: open, job: job, prev: prev}
}

// extendedHeader builds the header an extended-channel miner hashes: the coinbase
// from the job's prefix, the channel's extranonce prefix, its own extranonce and the
// job's suffix, then the merkle root folded up the job's merkle path.
func extendedHeader(ch *sv2ExtendedChannel, extranonce []byte, nonce uint32) []byte {
	coinbase := append(append(append(append([]byte{}, ch.job.CoinbaseTxPrefix...), ch.open.ExtranoncePrefix...), extranonce...), ch.job.CoinbaseTxSuffix...)
	root := crypto.SHA256d(coinbase)
	for _, leaf := range ch.job.MerklePath {
		root = crypto.SHA256d(append(append([]byte{}, root...), leaf...))
	}
	header := make([]byte, 80)
	binary.LittleEndian.PutUint32(header[0:4], ch.job.Version)
	copy(header[4:36], ch.prev.PrevHash[:])
	copy(header[36:68], root)
	binary.LittleEndian.PutUint32(header[68:72], ch.prev.MinNTime)
	binary.LittleEndian.PutUint32(header[72:76], ch.prev.NBits)
	binary.LittleEndian.PutUint32(header[76:80], nonce)
	return header
}

func mineExtended(t *testing.T, ch *sv2ExtendedChannel, extranonce []byte) (uint32, []byte) {
	t.Helper()
	target := U256ToTarget(ch.open.Target)
	if network := NBitsToTarget(ch.prev.NBits); network.Cmp(target) < 0 {
		target = network
	}
	for nonce := uint32(0); nonce < 1<<20; nonce++ {
		header := extendedHeader(ch, extranonce, nonce)
		if new(big.Int).SetBytes(crypto.ReverseBytes(crypto.SHA256d(header))).Cmp(target) <= 0 {
			return nonce, header
		}
	}
	t.Fatal("no nonce met the target")
	return 0, nil
}

func (c *sv2TestClient) submitExtended(channelID, seq, jobID, nonce, ntime, version uint32, extranonce []byte) {
	c.t.Helper()
	msg, err := EncodeSubmitSharesExtended(&SubmitSharesExtended{ChannelID: channelID, SequenceNum: seq, JobID: jobID,
		Nonce: nonce, NTime: ntime, Version: version, Extranonce: extranonce})
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(msg)
}

// An extended channel builds its own coinbase and merkle root; the block the
// validator assembles from its share must have the header the miner hashed and pay
// the channel's own address.
func TestSV2ExtendedChannel_BlockFromTheMinersOwnCoinbasePaysTheChannel(t *testing.T) {
	pool := newSV2TestPool()
	job := sv2TestJob("7", sv2PrevHash1)
	job.MerkleBranches = []string{hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32)), hex.EncodeToString(bytes.Repeat([]byte{0x22}, 32))}
	pool.setJob(job)
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openExtended(5, "minerB.rig2", 8)

	if ch.open.ExtranonceSize != 8 || len(ch.open.ExtranoncePrefix) != 4 || ch.open.ExtranoncePrefix[0]&0xC0 != 0xC0 {
		t.Fatalf("extended channel = %+v, want an 8-byte extranonce after a 4-byte 0b11 prefix", ch.open)
	}
	if len(ch.job.MerklePath) != 2 || !bytes.Contains(ch.job.CoinbaseTxSuffix, sv2PayoutB) || bytes.Contains(ch.job.CoinbaseTxSuffix, sv2PayoutA) {
		t.Fatal("extended job does not carry the merkle path and a coinbase paying the channel's address")
	}

	extranonce := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	nonce, header := mineExtended(t, ch, extranonce)
	client.submitExtended(ch.open.ChannelID, 1, ch.job.JobID, nonce, ch.prev.MinNTime, ch.job.Version, extranonce)
	client.expect(MsgSubmitSharesSuccess)

	submitted, results := pool.recorded()
	if len(submitted) != 1 {
		t.Fatalf("validator saw %d shares, want 1", len(submitted))
	}
	share := submitted[0]
	if share.MinerAddress != "minerB" || share.ExtraNonce1 != hex.EncodeToString(ch.open.ExtranoncePrefix) || share.ExtraNonce2 != hex.EncodeToString(extranonce) {
		t.Errorf("share = %+v, want minerB with the channel prefix and the miner's extranonce", share)
	}
	checkBlock(t, results[0], header, sv2PayoutB)
}

func TestSV2ExtendedChannel_RefusesBadExtranonceAndMismatchedChannelTypes(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ext := client.openExtended(1, "minerA.rig1", 4)
	std := client.openChannel(2, "minerA.rig2")

	client.submitExtended(ext.open.ChannelID, 1, ext.job.JobID, 0, ext.prev.MinNTime, ext.job.Version, []byte{1, 2, 3})
	if e, _ := DecodeSubmitSharesError(client.expect(MsgSubmitSharesError)); e.ErrorCode != ErrCodeInvalidExtranonceSize {
		t.Errorf("short extranonce: %q, want %q", e.ErrorCode, ErrCodeInvalidExtranonceSize)
	}
	client.submit(ext.open.ChannelID, 2, ext.job.JobID, 0, ext.prev.MinNTime, ext.job.Version)
	if e, _ := DecodeSubmitSharesError(client.expect(MsgSubmitSharesError)); e.ErrorCode != ErrCodeInvalidChannelID {
		t.Errorf("standard share on an extended channel: %q, want %q", e.ErrorCode, ErrCodeInvalidChannelID)
	}
	client.submitExtended(std.open.ChannelID, 3, std.job.JobID, 0, std.prev.MinNTime, std.job.Version, make([]byte, 8))
	if e, _ := DecodeSubmitSharesError(client.expect(MsgSubmitSharesError)); e.ErrorCode != ErrCodeInvalidChannelID {
		t.Errorf("extended share on a standard channel: %q, want %q", e.ErrorCode, ErrCodeInvalidChannelID)
	}
	if submitted, _ := pool.recorded(); len(submitted) != 0 {
		t.Error("a refused share reached the validator")
	}

	msg, _ := EncodeOpenExtendedMiningChannel(&OpenExtendedMiningChannel{
		OpenStandardMiningChannel: OpenStandardMiningChannel{RequestID: 9, UserIdentity: "minerA.rig3", NominalHashRate: 1e6},
		MinExtranonceSize:         9,
	})
	client.send(msg)
	if e, _ := DecodeOpenMiningChannelError(client.expect(MsgOpenMiningChannelError)); e.ErrorCode != ErrCodeUnsupportedMinExtranonceSize || e.RequestID != 9 {
		t.Errorf("min_extranonce_size 9: %+v, want %q", e, ErrCodeUnsupportedMinExtranonceSize)
	}
}

// A new block reaches an extended channel as a future job activated by SetNewPrevHash.
func TestSV2ExtendedChannel_NewBlockIsAFutureJob(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openExtended(1, "minerA.rig1", 8)

	next := sv2TestJob("8", sv2PrevHash2)
	pool.setJob(next)
	srv.BroadcastJob(next)
	future, err := DecodeNewExtendedMiningJob(client.expect(MsgNewExtendedMiningJob))
	if err != nil {
		t.Fatal(err)
	}
	prev, err := DecodeSetNewPrevHash(client.expect(MsgSetNewPrevHash))
	if err != nil {
		t.Fatal(err)
	}
	if future.MinNTime != nil || prev.JobID != future.JobID || future.JobID == ch.job.JobID {
		t.Fatalf("future job %+v, prevhash %+v", future, prev)
	}
}
