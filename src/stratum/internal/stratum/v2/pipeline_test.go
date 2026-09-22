// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/internal/shares"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// These tests run a Stratum V2 client over TCP against a server wired to the pool's
// real share validator. A standard channel's miner hashes only a header, so a share
// proves the pipeline works only if the header the validator rebuilds — from the
// pool job, the channel's extranonce prefix and its payout address — is the header
// the miner hashed. Each block test compares the two byte for byte.

var (
	sv2PayoutA       = bytes.Repeat([]byte{0xaa}, 22)
	sv2PayoutB       = bytes.Repeat([]byte{0xbb}, 22)
	sv2PoolScript, _ = hex.DecodeString("76a914000000000000000000000000000000000000000088ac")
)

const (
	sv2PrevHash1 = "00000000000000000000000000000000" + "00000000000000000000000000000001"
	sv2PrevHash2 = "00000000000000000000000000000000" + "00000000000000000000000000000002"
)

// sv2TestJob is a pool job with its coinbase2 split around the pool's reward script,
// as the job manager builds it for per-miner payouts.
func sv2TestJob(id, prevHash string) *protocol.Job {
	// CoinBase1's scriptSig length (0x10) counts the 4-byte height push and the 12
	// extranonce bytes a share inserts, as the job manager's does, so the assembled
	// coinbase is a valid transaction; the Stratum Reference Implementation's
	// translator parses it.
	return &protocol.Job{
		ID:              id,
		Version:         "20000000",
		PrevBlockHash:   prevHash,
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff1003000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		CoinBase2Prefix: "ffffffff0100f2052a01000000",
		CoinBase2Suffix: "00000000",
		PayoutScript: func(address string) []byte {
			switch address {
			case "minerA":
				return sv2PayoutA
			case "minerB":
				return sv2PayoutB
			}
			return nil
		},
		NBits:                 "207fffff", // regtest target: most shares are also blocks
		NTime:                 fmt.Sprintf("%08x", time.Now().Unix()),
		Height:                1,
		CreatedAt:             time.Now(),
		State:                 protocol.JobStateActive,
		VersionRollingAllowed: true,
		VersionRollingMask:    defaultVersionRollingMask,
		TransactionData:       []string{},
	}
}

// sv2TestPool stands in for the coin pool: a job store, the real validator, and a
// record of every share the server submits.
type sv2TestPool struct {
	mu        sync.Mutex
	jobs      map[string]*protocol.Job
	current   *protocol.Job
	shares    []*protocol.Share
	results   []*protocol.ShareResult
	validator *shares.ValidatorV2
}

func newSV2TestPool() *sv2TestPool {
	p := &sv2TestPool{jobs: map[string]*protocol.Job{}}
	p.validator = shares.NewValidatorWithCoin(p.getJob, coin.MustCreate("BTC"))
	p.setJob(sv2TestJob("1", sv2PrevHash1))
	return p
}

func (p *sv2TestPool) getJob(id string) (*protocol.Job, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	job, ok := p.jobs[id]
	return job, ok
}

func (p *sv2TestPool) setJob(job *protocol.Job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs[job.ID] = job
	p.current = job
}

func (p *sv2TestPool) currentJob() *protocol.Job {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

func (p *sv2TestPool) submit(share *protocol.Share) *protocol.ShareResult {
	result := p.validator.ValidateWithCoin(share)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shares = append(p.shares, share)
	p.results = append(p.results, result)
	return result
}

func (p *sv2TestPool) recorded() ([]*protocol.Share, []*protocol.ShareResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*protocol.Share(nil), p.shares...), append([]*protocol.ShareResult(nil), p.results...)
}

func startSV2TestServer(t *testing.T, pool *sv2TestPool, configure ...func(*ServerConfig)) (*Server, string) {
	t.Helper()
	cfg := DefaultServerConfig()
	cfg.ListenAddr = "127.0.0.1"
	cfg.Port = 0
	cfg.InitialDifficulty = 1e-9 // nearly every hash is a share
	for _, fn := range configure {
		fn(cfg)
	}

	srv, err := NewServer(cfg, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.SetPipeline(&Pipeline{
		CurrentJob:  pool.currentJob,
		MerkleRoot:  shares.ShareMerkleRoot,
		ShareTarget: pool.validator.ShareTarget,
		SubmitShare: pool.submit,
	})

	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Stop()
		cancel()
	})
	return srv, srv.listener.Addr().String()
}

type sv2TestClient struct {
	t    *testing.T
	raw  net.Conn
	conn *NoiseConn
}

// dialSV2 connects and completes the Noise handshake, verifying the server's
// certificate against its authority key as a miner configured with it would.
func dialSV2(t *testing.T, srv *Server) *sv2TestClient {
	t.Helper()
	raw, err := net.DialTimeout("tcp", srv.listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	authority := srv.AuthorityPublicKey()
	nc, _, err := ClientHandshake(raw, &authority)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return &sv2TestClient{t: t, raw: raw, conn: nc}
}

func (c *sv2TestClient) send(msg []byte) {
	c.t.Helper()
	if _, err := c.conn.Write(msg); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func (c *sv2TestClient) expect(msgType uint8) []byte {
	c.t.Helper()
	gotType, payload := c.next()
	if gotType != msgType {
		c.t.Fatalf("message type 0x%02x, want 0x%02x", gotType, msgType)
	}
	return payload
}

// next reads the next message, checking its extension_type: 0, or the channel_msg
// bit for exactly the messages addressed to a channel.
func (c *sv2TestClient) next() (uint8, []byte) {
	c.t.Helper()
	_ = c.raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	var header MessageHeader
	if err := header.Decode(c.conn); err != nil {
		c.t.Fatalf("read header: %v", err)
	}
	payload := make([]byte, header.Length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		c.t.Fatalf("read payload: %v", err)
	}
	want := uint16(0)
	if isChannelMessage(header.MsgType) {
		want = ChannelMsgBit
	}
	if header.ExtensionType != want {
		c.t.Fatalf("message 0x%02x has extension_type 0x%04x, want 0x%04x", header.MsgType, header.ExtensionType, want)
	}
	return header.MsgType, payload
}

func (c *sv2TestClient) setup() {
	c.t.Helper()
	msg, err := EncodeSetupConnection(&SetupConnection{
		Protocol:   ProtocolMiningV2,
		MinVersion: 2,
		MaxVersion: 2,
		Flags:      ProtocolFlagRequiresStandardJobs,
		VendorID:   "sv2-test",
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(msg)
	c.expect(MsgSetupConnectionSuccess)
}

// sv2TestChannel is what a standard channel has been sent: the open response and
// the job it is mining.
type sv2TestChannel struct {
	open *OpenStandardMiningChannelSuccess
	job  *NewMiningJob
	prev *SetNewPrevHash
}

func (c *sv2TestClient) openChannel(requestID uint32, identity string) *sv2TestChannel {
	c.t.Helper()
	msg, err := EncodeOpenStandardMiningChannel(&OpenStandardMiningChannel{
		RequestID:       requestID,
		UserIdentity:    identity,
		NominalHashRate: 1e6,
	})
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(msg)

	open, err := DecodeOpenStandardMiningChannelSuccess(c.expect(MsgOpenStandardMiningChannelSuccess))
	if err != nil {
		c.t.Fatal(err)
	}
	job, err := DecodeNewMiningJob(c.expect(MsgNewMiningJob))
	if err != nil {
		c.t.Fatal(err)
	}
	prev, err := DecodeSetNewPrevHash(c.expect(MsgSetNewPrevHash))
	if err != nil {
		c.t.Fatal(err)
	}
	return &sv2TestChannel{open: open, job: job, prev: prev}
}

func (c *sv2TestClient) submit(channelID, seq, jobID, nonce, ntime, version uint32) {
	c.t.Helper()
	c.send(EncodeSubmitSharesStandard(&SubmitSharesStandard{
		ChannelID:   channelID,
		SequenceNum: seq,
		JobID:       jobID,
		Nonce:       nonce,
		NTime:       ntime,
		Version:     version,
	}))
}

// sv2Header builds the 80-byte header a standard-channel miner hashes.
func sv2Header(job *NewMiningJob, prev *SetNewPrevHash, nonce uint32) []byte {
	header := make([]byte, 80)
	binary.LittleEndian.PutUint32(header[0:4], job.Version)
	copy(header[4:36], prev.PrevHash[:])
	copy(header[36:68], job.MerkleRoot[:])
	binary.LittleEndian.PutUint32(header[68:72], prev.MinNTime)
	binary.LittleEndian.PutUint32(header[72:76], prev.NBits)
	binary.LittleEndian.PutUint32(header[76:80], nonce)
	return header
}

// mineBlock finds a nonce whose header meets both the channel's share target and
// the network target.
func mineBlock(t *testing.T, ch *sv2TestChannel) (uint32, []byte) {
	t.Helper()
	target := U256ToTarget(ch.open.Target)
	if network := NBitsToTarget(ch.prev.NBits); network.Cmp(target) < 0 {
		target = network
	}
	for nonce := uint32(0); nonce < 1<<20; nonce++ {
		header := sv2Header(ch.job, ch.prev, nonce)
		hash := new(big.Int).SetBytes(crypto.ReverseBytes(crypto.SHA256d(header)))
		if hash.Cmp(target) <= 0 {
			return nonce, header
		}
	}
	t.Fatal("no nonce met the target")
	return 0, nil
}

// checkBlock asserts the block the validator built has the header the V2 miner
// hashed and pays only the given payout script.
func checkBlock(t *testing.T, result *protocol.ShareResult, header, payout []byte) {
	t.Helper()
	if !result.Accepted || !result.IsBlock {
		t.Fatalf("result = %+v, want an accepted block", result)
	}
	block, err := hex.DecodeString(result.BlockHex)
	if err != nil || len(block) < 80 {
		t.Fatalf("block hex: %v (%d bytes)", err, len(block))
	}
	if !bytes.Equal(block[:80], header) {
		t.Errorf("block header differs from the header the V2 miner hashed:\n block %x\n miner %x", block[:80], header)
	}
	if !bytes.Contains(block, payout) {
		t.Error("block does not pay the channel's miner")
	}
	for _, other := range [][]byte{sv2PayoutA, sv2PayoutB, sv2PoolScript} {
		if !bytes.Equal(other, payout) && bytes.Contains(block, other) {
			t.Error("block pays someone other than the channel's miner")
		}
	}
}

// A V2 block must be built from the channel's own coinbase, paying the address in
// its user identity, and reach the validator with that miner's identity.
func TestSV2StandardChannel_BlockPaysChannelMiner(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openChannel(1, "minerA.rig1")

	nonce, header := mineBlock(t, ch)
	client.submit(ch.open.ChannelID, 1, ch.job.JobID, nonce, ch.prev.MinNTime, ch.job.Version)
	ok, err := DecodeSubmitSharesSuccess(client.expect(MsgSubmitSharesSuccess))
	if err != nil {
		t.Fatal(err)
	}
	if ok.LastSequenceNum != 1 {
		t.Errorf("LastSequenceNum = %d, want 1", ok.LastSequenceNum)
	}

	submitted, results := pool.recorded()
	if len(submitted) != 1 {
		t.Fatalf("validator saw %d shares, want 1", len(submitted))
	}
	share := submitted[0]
	if share.MinerAddress != "minerA" || share.WorkerName != "rig1" {
		t.Errorf("share miner/worker = %q/%q, want minerA/rig1", share.MinerAddress, share.WorkerName)
	}
	if share.JobID != "1" {
		t.Errorf("share job = %q, want the pool job 1", share.JobID)
	}
	checkBlock(t, results[0], header, sv2PayoutA)
}

// Each channel must be sent the merkle root of its own coinbase: its own extranonce
// prefix and its own payout address.
func TestSV2StandardChannel_MerkleRootCommitsToChannelPayout(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	a := client.openChannel(1, "minerA.rig1")
	b := client.openChannel(2, "minerB.rig2")

	if len(a.open.ExtranoncePrefix) != extranoncePrefixSize {
		t.Fatalf("extranonce prefix = %d bytes, want %d", len(a.open.ExtranoncePrefix), extranoncePrefixSize)
	}
	if bytes.Equal(a.open.ExtranoncePrefix, b.open.ExtranoncePrefix) {
		t.Error("two channels were given the same extranonce prefix")
	}

	job := pool.currentJob()
	rootFor := func(prefix []byte, address string) []byte {
		t.Helper()
		root, err := shares.ShareMerkleRoot(job, &protocol.Share{
			ExtraNonce1:  hex.EncodeToString(prefix[:4]),
			ExtraNonce2:  hex.EncodeToString(prefix[4:]),
			MinerAddress: address,
		})
		if err != nil {
			t.Fatal(err)
		}
		return root
	}

	if !bytes.Equal(a.job.MerkleRoot[:], rootFor(a.open.ExtranoncePrefix, "minerA")) {
		t.Error("minerA's channel was not sent the root of its own coinbase")
	}
	if bytes.Equal(a.job.MerkleRoot[:], rootFor(a.open.ExtranoncePrefix, "minerB")) {
		t.Error("minerA's root does not depend on its payout address")
	}
	if !bytes.Equal(b.job.MerkleRoot[:], rootFor(b.open.ExtranoncePrefix, "minerB")) {
		t.Error("minerB's channel was not sent the root of its own coinbase")
	}
}

// A job on the current prevhash is sent active; a job on a new prevhash is sent as a
// future job activated by SetNewPrevHash, and shares on it validate.
func TestSV2BroadcastJob_RefreshThenNewBlock(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openChannel(1, "minerA.rig1")

	refresh := sv2TestJob("2", sv2PrevHash1)
	pool.setJob(refresh)
	srv.BroadcastJob(refresh)
	refreshed, err := DecodeNewMiningJob(client.expect(MsgNewMiningJob))
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.MinNTime == nil {
		t.Error("a job on the current prevhash must be sent active (min_ntime set)")
	}

	next := sv2TestJob("3", sv2PrevHash2)
	pool.setJob(next)
	srv.BroadcastJob(next)
	future, err := DecodeNewMiningJob(client.expect(MsgNewMiningJob))
	if err != nil {
		t.Fatal(err)
	}
	if future.MinNTime != nil {
		t.Error("a job on a new prevhash must be sent as a future job")
	}
	prev, err := DecodeSetNewPrevHash(client.expect(MsgSetNewPrevHash))
	if err != nil {
		t.Fatal(err)
	}
	if prev.JobID != future.JobID {
		t.Errorf("SetNewPrevHash activates job %d, want %d", prev.JobID, future.JobID)
	}
	if refreshed.JobID == ch.job.JobID || future.JobID == refreshed.JobID {
		t.Error("job IDs must be unique within a channel")
	}

	nonce, header := mineBlock(t, &sv2TestChannel{open: ch.open, job: future, prev: prev})
	client.submit(ch.open.ChannelID, 2, future.JobID, nonce, prev.MinNTime, future.Version)
	client.expect(MsgSubmitSharesSuccess)

	submitted, results := pool.recorded()
	if len(submitted) != 1 || submitted[0].JobID != "3" {
		t.Fatalf("validator saw %d shares (want 1, on pool job 3)", len(submitted))
	}
	checkBlock(t, results[0], header, sv2PayoutA)
}

// A share for a job the channel was never sent must be rejected before validation.
func TestSV2StandardChannel_UnknownJobRejected(t *testing.T) {
	pool := newSV2TestPool()
	srv, _ := startSV2TestServer(t, pool)
	client := dialSV2(t, srv)
	client.setup()
	ch := client.openChannel(1, "minerA.rig1")

	client.submit(ch.open.ChannelID, 7, ch.job.JobID+100, 0, ch.prev.MinNTime, ch.job.Version)
	rejected, err := DecodeSubmitSharesError(client.expect(MsgSubmitSharesError))
	if err != nil {
		t.Fatal(err)
	}
	if rejected.ErrorCode != ErrCodeInvalidJobID || rejected.SequenceNum != 7 {
		t.Errorf("error = %q for seq %d, want %q for seq 7", rejected.ErrorCode, rejected.SequenceNum, ErrCodeInvalidJobID)
	}
	if submitted, _ := pool.recorded(); len(submitted) != 0 {
		t.Error("a share for an unknown job reached the validator")
	}
}

// A client whose max_target is below the pool's starting target gets its channel,
// at max_target, as the Stratum Reference Implementation's pool does. Its translator
// opens every channel with a max_target derived from its hashrate, and was refused
// before this.
func TestSV2OpenChannel_MaxTargetBelowTargetIsClamped(t *testing.T) {
	maxTarget := NBitsToU256(0x1d00ffff)
	open := OpenStandardMiningChannel{RequestID: 9, UserIdentity: "minerA.rig1", NominalHashRate: 1e6, MaxTarget: maxTarget}

	for _, extended := range []bool{false, true} {
		pool := newSV2TestPool()
		srv, _ := startSV2TestServer(t, pool)
		client := dialSV2(t, srv)
		client.setup()

		var got [32]byte
		if extended {
			msg, err := EncodeOpenExtendedMiningChannel(&OpenExtendedMiningChannel{OpenStandardMiningChannel: open, MinExtranonceSize: 6})
			if err != nil {
				t.Fatal(err)
			}
			client.send(msg)
			success, err := DecodeOpenExtendedMiningChannelSuccess(client.expect(MsgOpenExtendedMiningChannelSuccess))
			if err != nil {
				t.Fatal(err)
			}
			got = success.Target
		} else {
			msg, err := EncodeOpenStandardMiningChannel(&open)
			if err != nil {
				t.Fatal(err)
			}
			client.send(msg)
			success, err := DecodeOpenStandardMiningChannelSuccess(client.expect(MsgOpenStandardMiningChannelSuccess))
			if err != nil {
				t.Fatal(err)
			}
			got = success.Target
		}
		if got != maxTarget {
			t.Errorf("extended=%v: channel target %x, want max_target %x", extended, U256ToTarget(got), U256ToTarget(maxTarget))
		}
		// The validator must not demand more than the target the miner was given.
		if enforced := pool.validator.ShareTarget(srv.difficultyForTarget(U256ToTarget(maxTarget))); enforced.Cmp(U256ToTarget(maxTarget)) < 0 {
			t.Errorf("extended=%v: validator target %x is harder than the channel target", extended, enforced)
		}
	}
}

func TestShareErrorCode(t *testing.T) {
	cases := []struct {
		result *protocol.ShareResult
		want   string
	}{
		{nil, "invalid-share"},
		{&protocol.ShareResult{RejectReason: protocol.RejectReasonInvalidJob}, ErrCodeInvalidJobID},
		{&protocol.ShareResult{RejectReason: protocol.RejectReasonStale}, ErrCodeStaleShare},
		{&protocol.ShareResult{RejectReason: protocol.RejectReasonLowDifficulty}, ErrCodeDifficultyTooLow},
		{&protocol.ShareResult{RejectReason: protocol.RejectReasonInvalidTime}, protocol.RejectReasonInvalidTime},
		{&protocol.ShareResult{}, "invalid-share"},
	}
	for _, tc := range cases {
		if got := shareErrorCode(tc.result); got != tc.want {
			t.Errorf("shareErrorCode(%+v) = %q, want %q", tc.result, got, tc.want)
		}
	}
}
