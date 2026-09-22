// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Tests that the REAL server sends each miner a coinbase paying its own address,
// and never sends work before the payout address is known (authorize).
package stratum

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

const (
	payoutPrefix = "ffffffff0100f2052a01000000" // sequence | 1 output | value
	payoutSuffix = "00000000"                   // locktime
)

// payoutTestJob returns a job split the way the job manager splits it: the pool
// coinbase pays script 0x00, minerA pays 0xaa, minerB pays 0xbb.
func payoutTestJob() *protocol.Job {
	return &protocol.Job{
		ID:              "00000001",
		PrevBlockHash:   "000000000000000000000000000000000000000000000000000000000000dead",
		CoinBase1:       "01000000010000",
		CoinBase2:       payoutPrefix + "0100" + payoutSuffix,
		CoinBase2Prefix: payoutPrefix,
		CoinBase2Suffix: payoutSuffix,
		PayoutScript: func(address string) []byte {
			switch address {
			case "minerA":
				return []byte{0xaa}
			case "minerB":
				return []byte{0xbb}
			}
			return nil
		},
		MerkleBranches: []string{},
		Version:        "20000000",
		NBits:          "1d00ffff",
		NTime:          "64000000",
		CreatedAt:      time.Now(),
	}
}

func newPayoutTestServer() *Server {
	return NewServer(&config.StratumConfig{
		Listen:       "0.0.0.0:0",
		Difficulty:   config.DifficultyConfig{Initial: 1},
		RateLimiting: config.StratumRateLimitConfig{PreAuthMessageLimit: 20},
	}, zap.NewNop())
}

// pipeSession returns a server-side session over net.Pipe and a channel that
// receives the coinbase2 of every mining.notify the miner is sent.
func pipeSession(t *testing.T, id uint64) (*protocol.Session, <-chan string) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	notifies := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(client)
		for scanner.Scan() {
			var msg struct {
				Method string        `json:"method"`
				Params []interface{} `json:"params"`
			}
			if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Method != "mining.notify" || len(msg.Params) < 4 {
				continue
			}
			if coinbase2, ok := msg.Params[3].(string); ok {
				notifies <- coinbase2
			}
		}
	}()

	return &protocol.Session{ID: id, Conn: server, ExtraNonce1: "00000001", ExtraNonce2Size: 4}, notifies
}

func expectNotify(t *testing.T, who string, notifies <-chan string, want string) {
	t.Helper()
	select {
	case got := <-notifies:
		if got != want {
			t.Errorf("%s was sent coinbase2 %s, want %s", who, got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was not sent a job", who)
	}
}

// A miner completing subscribe → authorize must receive, as its FIRST job, a
// coinbase paying the address in its worker name — no pool-address job first.
func TestHandshake_FirstJobPaysMinersOwnAddress(t *testing.T) {
	s := newPayoutTestServer()
	s.currentJob.Store(payoutTestJob())
	session, notifies := pipeSession(t, 1)

	s.handleMessage(session, []byte(`{"id":1,"method":"mining.subscribe","params":["cpuminer/2.5.2"]}`))
	s.handleMessage(session, []byte(`{"id":2,"method":"mining.authorize","params":["minerA.rig1","x"]}`))

	expectNotify(t, "minerA", notifies, payoutPrefix+"01aa"+payoutSuffix)
}

// BroadcastJob sends each authorized miner its own coinbase and skips sessions
// that have not authorized yet.
func TestBroadcastJob_PerMinerCoinbaseAuthorizedOnly(t *testing.T) {
	s := newPayoutTestServer()

	minerA, notifiesA := pipeSession(t, 1)
	minerA.MinerAddress = "minerA"
	minerB, notifiesB := pipeSession(t, 2)
	minerB.MinerAddress = "minerB"
	pending, notifiesPending := pipeSession(t, 3)

	for _, session := range []*protocol.Session{minerA, minerB, pending} {
		session.SetSubscribed(true)
		s.sessions.Set(session.ID, session)
	}
	minerA.SetAuthorized(true)
	minerB.SetAuthorized(true)

	s.BroadcastJob(payoutTestJob())

	expectNotify(t, "minerA", notifiesA, payoutPrefix+"01aa"+payoutSuffix)
	expectNotify(t, "minerB", notifiesB, payoutPrefix+"01bb"+payoutSuffix)
	select {
	case got := <-notifiesPending:
		t.Errorf("unauthorized session was sent a job (coinbase2 %s)", got)
	case <-time.After(200 * time.Millisecond):
	}
}
