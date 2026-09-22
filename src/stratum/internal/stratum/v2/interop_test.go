// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/shares"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// TestInteropExternalClient runs a V2 server, wired to the pool's share validator,
// for a client from another implementation — such as the Stratum Reference
// Implementation's mining_device or translator — and checks the shares it submits.
// It is skipped unless SPIRAL_SV2_INTEROP_ADDR names the address to listen on:
//
//	SPIRAL_SV2_INTEROP_ADDR=127.0.0.1:34254 SPIRAL_SV2_INTEROP_KEYDIR=/tmp/sv2keys \
//	    go test -v -run TestInteropExternalClient ./internal/stratum/v2/
//
// It logs the authority key in hex and base58; runs sharing SPIRAL_SV2_INTEROP_KEYDIR
// keep the same key. SPIRAL_SV2_INTEROP_SECONDS sets how long it serves (default 60)
// and SPIRAL_SV2_INTEROP_DIFFICULTY the starting share difficulty (default 0.001).
// Vardiff is on, and a job on a new previous block hash is sent every 15 seconds.
// Clients should open their channel with user identity "minerA".
func TestInteropExternalClient(t *testing.T) {
	listen := os.Getenv("SPIRAL_SV2_INTEROP_ADDR")
	if listen == "" {
		t.Skip("set SPIRAL_SV2_INTEROP_ADDR to serve an external Stratum V2 client")
	}
	host, portText, err := net.SplitHostPort(listen)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	seconds := interopEnvFloat(t, "SPIRAL_SV2_INTEROP_SECONDS", 60)
	difficulty := interopEnvFloat(t, "SPIRAL_SV2_INTEROP_DIFFICULTY", 0.001)
	keyDir := os.Getenv("SPIRAL_SV2_INTEROP_KEYDIR")
	if keyDir == "" {
		keyDir = t.TempDir()
	}
	keys, _, err := LoadServerKeys(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	authority := keys.AuthorityPublicKey()
	t.Logf("authority key hex %x", authority[:])
	t.Logf("authority key base58 %s", EncodeAuthorityKey(authority))

	// Jobs use network difficulty 1, above every share difficulty here. With the
	// regtest bits of sv2TestJob every hash is a block: a proxy then forwards every
	// share as a block regardless of the channel target, and the pool, which checks
	// the share target before the block target, rejects those above the target.
	interopJob := func(id, prevHash string) *protocol.Job {
		job := sv2TestJob(id, prevHash)
		job.NBits = "1d00ffff"
		return job
	}
	pool := newSV2TestPool()
	pool.setJob(interopJob("1", sv2PrevHash1))
	cfg := DefaultServerConfig()
	cfg.ListenAddr = host
	cfg.Port = port
	cfg.Keys = keys
	cfg.InitialDifficulty = difficulty
	cfg.VarDiff = config.VarDiffConfig{
		Enabled: true, MinDiff: difficulty / 1000, MaxDiff: 1e6,
		TargetTime: 5, RetargetTime: 20, VariancePercent: 50,
	}
	logger, _ := zap.NewDevelopment()
	srv, err := NewServer(cfg, logger.Sugar())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetPipeline(&Pipeline{
		CurrentJob:  pool.currentJob,
		MerkleRoot:  shares.ShareMerkleRoot,
		ShareTarget: pool.validator.ShareTarget,
		SubmitShare: pool.submit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	t.Logf("serving on %s for %.0fs", listen, seconds)

	deadline := time.After(time.Duration(seconds * float64(time.Second)))
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for block := 2; ; block++ {
		select {
		case <-ticker.C:
			job := interopJob(strconv.Itoa(block), fmt.Sprintf("%064x", block))
			pool.setJob(job)
			srv.BroadcastJob(job)
			accepted, rejected := interopCounts(pool)
			t.Logf("new prevhash %d; shares so far: %d accepted, %d rejected", block, accepted, rejected)
			continue
		case <-deadline:
		}
		break
	}

	recorded, results := pool.recorded()
	accepted, rejected := interopCounts(pool)
	t.Logf("%d shares: %d accepted, %d rejected", len(recorded), accepted, rejected)
	if accepted == 0 {
		t.Error("no accepted shares")
	}
	for i, r := range results {
		if !r.Accepted {
			t.Errorf("share %d (job %s, miner %q) rejected: %s (share difficulty %.6g, hash difficulty %.6g)",
				i, recorded[i].JobID, recorded[i].MinerAddress, r.RejectReason, recorded[i].Difficulty, r.ActualDifficulty)
		}
	}
}

func interopEnvFloat(t *testing.T, name string, def float64) float64 {
	t.Helper()
	text := os.Getenv(name)
	if text == "" {
		return def
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || v <= 0 {
		t.Fatalf("%s=%q: want a positive number", name, text)
	}
	return v
}

func interopCounts(pool *sv2TestPool) (accepted, rejected int) {
	_, results := pool.recorded()
	for _, r := range results {
		if r.Accepted {
			accepted++
		} else {
			rejected++
		}
	}
	return accepted, rejected
}
