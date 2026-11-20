// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// New-block notification burst (v3.1.0)
// =============================================================================
// Field report (DGB, single coin): after a new block, a miner received ~45
// mining.notify messages in 3.5 s, every one with the same prevhash, and two
// valid shares were rejected as stale. Cause: each poll of the post-notification
// wait loop broadcast a job even when the template had not changed, two loops
// (ZMQ + RPC poller) ran at once, and each ended with a "forced" clean job on
// the unchanged template. These tests pin the fixed behaviour.

// fakeDaemon stands in for getblocktemplate: the template moves to `tip` once
// `catchUpAfter` polls have been made.
type fakeDaemon struct {
	mu           sync.Mutex
	m            *Manager
	tip          string
	catchUpAfter int32
	polls        atomic.Int32
	broadcasts   atomic.Int32
	pollDelay    time.Duration
}

func (d *fakeDaemon) refreshIfNewBlock(ctx context.Context) (bool, error) {
	n := d.polls.Add(1)
	if d.pollDelay > 0 {
		time.Sleep(d.pollDelay)
	}
	if n <= d.catchUpAfter {
		return false, nil // daemon still serves the old template
	}
	d.mu.Lock()
	tip := d.tip
	d.mu.Unlock()
	d.m.stateMu.Lock()
	defer d.m.stateMu.Unlock()
	if d.m.lastBlockHash == tip {
		return false, nil // unchanged template: must not broadcast
	}
	d.m.lastBlockHash = tip
	d.m.lastHeight++
	d.broadcasts.Add(1)
	return true, nil
}

func newNotifyTestManager(lastHash string, height uint64) *Manager {
	return &Manager{
		logger:        zap.NewNop().Sugar(),
		heightEpoch:   NewHeightEpoch(),
		lastBlockHash: lastHash,
		lastHeight:    height,
	}
}

const (
	hashA = "00000000000000000001aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "00000000000000000002bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "00000000000000000003cccccccccccccccccccccccccccccccccccccccccccc"
)

func TestBlockNotify_OneJobWhenDaemonCatchesUp(t *testing.T) {
	m := newNotifyTestManager(hashA, 100)
	d := &fakeDaemon{m: m, tip: hashB, catchUpAfter: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m.handleBlockNotification(ctx, hashB, d.refreshIfNewBlock)

	if got := d.broadcasts.Load(); got != 1 {
		t.Fatalf("broadcasts = %d, want exactly 1 (the new block's clean job)", got)
	}
	if got := d.polls.Load(); got != 4 {
		t.Fatalf("polls = %d, want 4 (3 unchanged + 1 new)", got)
	}
}

func TestBlockNotify_NoJobsWhenDaemonNeverCatchesUp(t *testing.T) {
	// Previously: 19 duplicate jobs + 2 clean jobs on the unchanged template.
	m := newNotifyTestManager(hashA, 100)
	d := &fakeDaemon{m: m, tip: hashB, catchUpAfter: 1 << 30}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	m.handleBlockNotification(ctx, hashB, d.refreshIfNewBlock)

	if got := d.broadcasts.Load(); got != 0 {
		t.Fatalf("broadcasts = %d, want 0 — an unchanged template must not go out again", got)
	}
	if got := d.polls.Load(); got != 20 {
		t.Fatalf("polls = %d, want 20", got)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("gave up after %v, want ~1.75s", el)
	}
}

func TestBlockNotify_AlreadyOnAnnouncedTip(t *testing.T) {
	// The RPC poller or a rebroadcast got the new template first. The
	// notification must not poll or broadcast at all.
	m := newNotifyTestManager(hashB, 101)
	d := &fakeDaemon{m: m, tip: hashB}
	m.handleBlockNotification(context.Background(), hashB, d.refreshIfNewBlock)

	if d.polls.Load() != 0 || d.broadcasts.Load() != 0 {
		t.Fatalf("polls=%d broadcasts=%d, want 0/0", d.polls.Load(), d.broadcasts.Load())
	}
}

func TestBlockNotify_AlreadyOnAnnouncedTip_ReversedByteOrder(t *testing.T) {
	reversed := make([]byte, len(hashB))
	for i := 0; i < len(hashB); i += 2 {
		copy(reversed[len(hashB)-i-2:], hashB[i:i+2])
	}
	m := newNotifyTestManager(hashB, 101)
	d := &fakeDaemon{m: m, tip: hashB}
	m.handleBlockNotification(context.Background(), string(reversed), d.refreshIfNewBlock)

	if d.polls.Load() != 0 {
		t.Fatalf("polls = %d, want 0 (same block, other byte order)", d.polls.Load())
	}
}

func TestBlockNotify_ConcurrentSameBlockSendsOneJob(t *testing.T) {
	// ZMQ and the RPC poller report the same block at the same moment while the
	// daemon lags. Exactly one job may go out.
	m := newNotifyTestManager(hashA, 100)
	d := &fakeDaemon{m: m, tip: hashB, catchUpAfter: 4, pollDelay: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, h := range []string{hashB, ""} { // ZMQ has the hash; legacy path may not
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			m.handleBlockNotification(ctx, h, d.refreshIfNewBlock)
		}(h)
	}
	wg.Wait()

	if got := d.broadcasts.Load(); got != 1 {
		t.Fatalf("broadcasts = %d, want exactly 1", got)
	}
}

func TestBlockNotify_QueuedNewerBlockIsHandled(t *testing.T) {
	// A second, newer block arrives while the first notification is still
	// waiting on the daemon. It must not be dropped.
	m := newNotifyTestManager(hashA, 100)
	d := &fakeDaemon{m: m, tip: hashB, catchUpAfter: 3, pollDelay: 2 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		m.handleBlockNotification(ctx, hashB, d.refreshIfNewBlock)
		close(done)
	}()
	// Let the first handler take the lock and start polling.
	for d.polls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	// Block C arrives; the first handler is still waiting for B.
	d.mu.Lock()
	d.tip = hashC
	d.mu.Unlock()
	m.handleBlockNotification(ctx, hashC, d.refreshIfNewBlock) // queued, returns at once
	<-done

	m.stateMu.RLock()
	last := m.lastBlockHash
	m.stateMu.RUnlock()
	if last != hashC {
		t.Fatalf("template ends on %s, want block C", last[:24])
	}
	if got := d.broadcasts.Load(); got != 1 {
		// The daemon jumped straight to C, so one clean job covers both.
		t.Fatalf("broadcasts = %d, want 1", got)
	}
}

func TestBlockNotify_StaleNotificationSupersededByNewer(t *testing.T) {
	// Field case (DGB): blocks B and C 4 ms apart. The RPC poller already moved
	// the template to C when ZMQ announced B, then C. Waiting for B (behind the
	// template) must stop as soon as C is announced, not poll 20 times.
	m := newNotifyTestManager(hashC, 102)
	d := &fakeDaemon{m: m, tip: hashC, pollDelay: 2 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		m.handleBlockNotification(ctx, hashB, d.refreshIfNewBlock) // B: older than the template
		close(done)
	}()
	for d.polls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	m.handleBlockNotification(ctx, hashC, d.refreshIfNewBlock) // C: queued
	<-done

	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("stale wait lasted %v, want it cut short by the newer notification", el)
	}
	if p := d.polls.Load(); p >= 20 {
		t.Fatalf("polls = %d, want the wait abandoned early", p)
	}
	if b := d.broadcasts.Load(); b != 0 {
		t.Fatalf("broadcasts = %d, want 0 (template already on C)", b)
	}
}

func TestSameBlockHash(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abcd", "abcd", true},
		{"ABCD", "abcd", true},
		{"abcd", "cdab", true}, // byte-reversed
		{"abcd", "dcba", false},
		{"abcd", "", false},
		{"", "", false},
		{"abcd", "abcdef", false},
	}
	for _, c := range cases {
		if got := sameBlockHash(c.a, c.b); got != c.want {
			t.Errorf("sameBlockHash(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
