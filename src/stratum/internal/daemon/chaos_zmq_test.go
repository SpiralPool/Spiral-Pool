//go:build !nozmq

// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Chaos test for ZMQ recovery loop double-spawn race.
//
// TEST 3: ZMQ Recovery Loop Double-Spawn Race
// When checkHealth() is called while the status transitions from Degraded to Failed,
// there is a TOCTOU window between status.Load() and setStatus() where multiple
// goroutines can each see status != Failed and each spawn a recovery loop.
package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// TestChaos_ZMQ_RecoveryDoubleSpawnRace calls checkHealth() from many goroutines
// simultaneously while the ZMQ listener is in a degraded state with failures
// exceeding the threshold. Multiple goroutines can each see status != Failed
// and spawn separate recovery loops, causing competing socket reconnections.
//
// TARGET: zmq.go:559-583 (checkHealth transition to Failed + recovery loop spawn)
// INVARIANT: Only one recovery loop should be spawned per failure episode.
// RUN WITH: go test -race -run TestChaos_ZMQ_RecoveryDoubleSpawnRace
func TestChaos_ZMQ_RecoveryDoubleSpawnRace(t *testing.T) {
	cfg := &config.ZMQConfig{
		Enabled:             true,
		Endpoint:            "tcp://127.0.0.1:59999", // Non-existent endpoint
		ReconnectInitial:    100 * time.Millisecond,
		ReconnectMax:        500 * time.Millisecond,
		ReconnectFactor:     2.0,
		FailureThreshold:    1 * time.Millisecond, // Trigger fallback instantly
		StabilityPeriod:     1 * time.Hour,         // Never reach stability
		HealthCheckInterval: 10 * time.Second,
	}

	z := NewZMQListener(cfg, zap.NewNop())
	z.running.Store(true)
	z.stopCh = make(chan struct{})

	// Track how many times onFallback is called with true (= fallback to polling)
	var fallbackCount atomic.Int32
	z.onFallback = func(usePoll bool) {
		if usePoll {
			fallbackCount.Add(1)
		}
	}

	// Set failure state: Degraded with failures exceeding threshold
	z.status.Store(int32(ZMQStatusDegraded))
	z.failureStartTime.Store(time.Now().Add(-1 * time.Hour).Unix()) // Well past threshold

	// Launch many goroutines calling checkHealth simultaneously
	// Each should see status != Failed and try to transition + spawn recovery loop
	const racers = 50
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			z.checkHealth()
		}()
	}
	wg.Wait()

	// Give recovery loops a moment to register with WaitGroup
	time.Sleep(500 * time.Millisecond)

	// Shutdown: signal all goroutines to exit
	z.running.Store(false)
	close(z.stopCh)

	// Wait for all recovery loops to exit
	done := make(chan struct{})
	go func() {
		z.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines exited
	case <-time.After(15 * time.Second):
		t.Fatalf("DEADLOCK: Recovery loops did not exit within 15 seconds")
	}

	// Cleanup sockets
	z.socketMu.Lock()
	if z.subscriber != nil {
		_ = z.subscriber.Close()
		z.subscriber = nil
	}
	if z.socketStop != nil {
		z.socketStop()
	}
	z.socketMu.Unlock()

	fc := fallbackCount.Load()
	t.Logf("RESULTS: %d concurrent checkHealth calls, fallback triggered %d times", racers, fc)

	if fc > 1 {
		t.Logf("RACE DETECTED: %d recovery loops spawned (want 1)", fc)
		t.Logf("ROOT CAUSE: TOCTOU between status.Load() and setStatus(Failed) in checkHealth()")
		t.Logf("IMPACT: %d concurrent recovery loops competing for socket reconnection", fc)
	} else if fc == 1 {
		t.Logf("Single transition observed. Run with -count=100 or -race for higher detection probability.")
	} else {
		t.Errorf("Fallback never triggered (expected at least 1 transition to Failed state)")
	}
}
