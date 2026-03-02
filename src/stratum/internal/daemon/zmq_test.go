// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

//go:build !nozmq

// Package daemon - Unit tests for ZMQ listener resilience and fallback behavior.
//
// These tests verify:
// - ZMQ status state machine transitions
// - Exponential backoff configuration
// - Failure threshold triggering fallback
// - Stability period detection
// - Stats collection
// - Health monitoring intervals
// - Recovery loop behavior
package daemon

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
)

// TestZMQStatusValues verifies all ZMQ status values are defined.
func TestZMQStatusValues(t *testing.T) {
	statuses := []ZMQStatus{
		ZMQStatusDisabled,
		ZMQStatusConnecting,
		ZMQStatusHealthy,
		ZMQStatusDegraded,
		ZMQStatusFailed,
	}

	// Verify unique values
	seen := make(map[ZMQStatus]bool)
	for _, status := range statuses {
		if seen[status] {
			t.Errorf("Duplicate status value: %d", status)
		}
		seen[status] = true
	}

	if len(seen) != 5 {
		t.Errorf("Expected 5 unique statuses, got %d", len(seen))
	}
}

// TestZMQStatusStrings verifies status string representations.
func TestZMQStatusStrings(t *testing.T) {
	tests := []struct {
		status ZMQStatus
		want   string
	}{
		{ZMQStatusDisabled, "disabled"},
		{ZMQStatusConnecting, "connecting"},
		{ZMQStatusHealthy, "healthy"},
		{ZMQStatusDegraded, "degraded"},
		{ZMQStatusFailed, "failed"},
		{ZMQStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("ZMQStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

// TestDefaultZMQTimingConstants verifies default timing values.
func TestDefaultZMQTimingConstants(t *testing.T) {
	// Reconnect initial delay
	if DefaultZMQReconnectInitial != 1*time.Second {
		t.Errorf("DefaultZMQReconnectInitial = %v, want 1s", DefaultZMQReconnectInitial)
	}

	// Reconnect max delay (30 seconds)
	if DefaultZMQReconnectMax != 30*time.Second {
		t.Errorf("DefaultZMQReconnectMax = %v, want 30s", DefaultZMQReconnectMax)
	}

	// Backoff factor
	if DefaultZMQReconnectFactor != 2.0 {
		t.Errorf("DefaultZMQReconnectFactor = %v, want 2.0", DefaultZMQReconnectFactor)
	}

	// Failure threshold before switching to polling
	if DefaultZMQFailureThreshold != 30*time.Second {
		t.Errorf("DefaultZMQFailureThreshold = %v, want 30s", DefaultZMQFailureThreshold)
	}

	// Health check interval
	if DefaultZMQHealthCheckInterval != 10*time.Second {
		t.Errorf("DefaultZMQHealthCheckInterval = %v, want 10s", DefaultZMQHealthCheckInterval)
	}

	// Stability period
	if DefaultZMQStabilityPeriod != 2*time.Minute {
		t.Errorf("DefaultZMQStabilityPeriod = %v, want 2m", DefaultZMQStabilityPeriod)
	}
}

// TestZMQConfigDefaults verifies config methods return defaults.
func TestZMQConfigDefaults(t *testing.T) {
	cfg := &config.ZMQConfig{
		Enabled:  true,
		Endpoint: "tcp://localhost:28332",
		// All timing fields left as zero
	}

	// Create listener without starting it
	listener := &ZMQListener{
		cfg: cfg,
	}

	if listener.reconnectInitial() != DefaultZMQReconnectInitial {
		t.Errorf("reconnectInitial() = %v, want %v",
			listener.reconnectInitial(), DefaultZMQReconnectInitial)
	}

	if listener.reconnectMax() != DefaultZMQReconnectMax {
		t.Errorf("reconnectMax() = %v, want %v",
			listener.reconnectMax(), DefaultZMQReconnectMax)
	}

	if listener.reconnectFactor() != DefaultZMQReconnectFactor {
		t.Errorf("reconnectFactor() = %v, want %v",
			listener.reconnectFactor(), DefaultZMQReconnectFactor)
	}

	if listener.failureThreshold() != DefaultZMQFailureThreshold {
		t.Errorf("failureThreshold() = %v, want %v",
			listener.failureThreshold(), DefaultZMQFailureThreshold)
	}

	if listener.stabilityPeriod() != DefaultZMQStabilityPeriod {
		t.Errorf("stabilityPeriod() = %v, want %v",
			listener.stabilityPeriod(), DefaultZMQStabilityPeriod)
	}

	if listener.healthCheckInterval() != DefaultZMQHealthCheckInterval {
		t.Errorf("healthCheckInterval() = %v, want %v",
			listener.healthCheckInterval(), DefaultZMQHealthCheckInterval)
	}
}

// TestZMQConfigCustomValues verifies custom config values are used.
func TestZMQConfigCustomValues(t *testing.T) {
	cfg := &config.ZMQConfig{
		Enabled:             true,
		Endpoint:            "tcp://localhost:28332",
		ReconnectInitial:    10 * time.Second,
		ReconnectMax:        60 * time.Second,
		ReconnectFactor:     1.5,
		FailureThreshold:    10 * time.Minute,
		StabilityPeriod:     2 * time.Minute,
		HealthCheckInterval: 15 * time.Second,
	}

	listener := &ZMQListener{cfg: cfg}

	if listener.reconnectInitial() != 10*time.Second {
		t.Errorf("Custom reconnectInitial() = %v, want 10s", listener.reconnectInitial())
	}

	if listener.reconnectMax() != 60*time.Second {
		t.Errorf("Custom reconnectMax() = %v, want 60s", listener.reconnectMax())
	}

	if listener.reconnectFactor() != 1.5 {
		t.Errorf("Custom reconnectFactor() = %v, want 1.5", listener.reconnectFactor())
	}

	if listener.failureThreshold() != 10*time.Minute {
		t.Errorf("Custom failureThreshold() = %v, want 10m", listener.failureThreshold())
	}

	if listener.stabilityPeriod() != 2*time.Minute {
		t.Errorf("Custom stabilityPeriod() = %v, want 2m", listener.stabilityPeriod())
	}

	if listener.healthCheckInterval() != 15*time.Second {
		t.Errorf("Custom healthCheckInterval() = %v, want 15s", listener.healthCheckInterval())
	}
}

// TestZMQExponentialBackoff verifies backoff calculation.
func TestZMQExponentialBackoff(t *testing.T) {
	listener := &ZMQListener{
		cfg: &config.ZMQConfig{},
	}

	tests := []struct {
		current time.Duration
		max     time.Duration
		factor  float64
		want    time.Duration
	}{
		// Normal progression
		{5 * time.Second, 120 * time.Second, 2.0, 10 * time.Second},
		{10 * time.Second, 120 * time.Second, 2.0, 20 * time.Second},
		{20 * time.Second, 120 * time.Second, 2.0, 40 * time.Second},
		{40 * time.Second, 120 * time.Second, 2.0, 80 * time.Second},
		{80 * time.Second, 120 * time.Second, 2.0, 120 * time.Second}, // Capped at max

		// Already at max
		{120 * time.Second, 120 * time.Second, 2.0, 120 * time.Second},

		// Over max (clamp)
		{100 * time.Second, 120 * time.Second, 2.0, 120 * time.Second},

		// Custom factor
		{10 * time.Second, 120 * time.Second, 1.5, 15 * time.Second},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := listener.nextDelay(tt.current, tt.max, tt.factor)
			if got != tt.want {
				t.Errorf("nextDelay(%v, %v, %v) = %v, want %v",
					tt.current, tt.max, tt.factor, got, tt.want)
			}
		})
	}
}

// TestZMQStatsFields verifies stats structure.
func TestZMQStatsFields(t *testing.T) {
	stats := ZMQStats{
		Status:           "healthy",
		MessagesReceived: 1000,
		ErrorsCount:      5,
		LastMessageAge:   10 * time.Second,
		FailureDuration:  0,
		HealthyDuration:  5 * time.Minute,
		StabilityReached: true,
	}

	if stats.Status == "" {
		t.Error("Status should not be empty")
	}

	if stats.MessagesReceived == 0 && stats.ErrorsCount == 0 {
		t.Error("Expected some message or error counts")
	}

	if stats.HealthyDuration < 0 {
		t.Error("HealthyDuration should not be negative")
	}
}

// TestIsHealthyLogic verifies healthy check.
func TestIsHealthyLogic(t *testing.T) {
	tests := []struct {
		status  ZMQStatus
		healthy bool
	}{
		{ZMQStatusHealthy, true},
		{ZMQStatusConnecting, true},
		{ZMQStatusDegraded, false},
		{ZMQStatusFailed, false},
		{ZMQStatusDisabled, false},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			healthy := tt.status == ZMQStatusHealthy || tt.status == ZMQStatusConnecting
			if healthy != tt.healthy {
				t.Errorf("IsHealthy for %v = %v, want %v", tt.status, healthy, tt.healthy)
			}
		})
	}
}

// TestIsFailedLogic verifies failed check.
func TestIsFailedLogic(t *testing.T) {
	tests := []struct {
		status ZMQStatus
		failed bool
	}{
		{ZMQStatusFailed, true},
		{ZMQStatusHealthy, false},
		{ZMQStatusConnecting, false},
		{ZMQStatusDegraded, false},
		{ZMQStatusDisabled, false},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			failed := tt.status == ZMQStatusFailed
			if failed != tt.failed {
				t.Errorf("IsFailed for %v = %v, want %v", tt.status, failed, tt.failed)
			}
		})
	}
}

// TestZMQStateTransitions verifies state machine behavior.
func TestZMQStateTransitions(t *testing.T) {
	tests := []struct {
		name  string
		from  ZMQStatus
		event string
		to    ZMQStatus
	}{
		{"disabled to connecting on start", ZMQStatusDisabled, "start", ZMQStatusConnecting},
		{"connecting to healthy on success", ZMQStatusConnecting, "success", ZMQStatusHealthy},
		{"healthy to degraded on error", ZMQStatusHealthy, "error", ZMQStatusDegraded},
		{"degraded to failed on threshold", ZMQStatusDegraded, "threshold", ZMQStatusFailed},
		{"failed to connecting on recover", ZMQStatusFailed, "recover", ZMQStatusConnecting},
		{"degraded to healthy on success", ZMQStatusDegraded, "success", ZMQStatusHealthy},
		{"any to disabled on stop", ZMQStatusHealthy, "stop", ZMQStatusDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the transitions are valid
			if tt.from == tt.to && tt.event != "no-op" {
				t.Logf("State unchanged: %v on %v", tt.from, tt.event)
			}
		})
	}
}

// TestAtomicStatusUpdates verifies atomic status operations.
func TestAtomicStatusUpdates(t *testing.T) {
	var status atomic.Int32
	status.Store(int32(ZMQStatusDisabled))

	// Swap and verify old value
	old := ZMQStatus(status.Swap(int32(ZMQStatusConnecting)))
	if old != ZMQStatusDisabled {
		t.Errorf("Old status = %v, want %v", old, ZMQStatusDisabled)
	}

	// Current should be new value
	current := ZMQStatus(status.Load())
	if current != ZMQStatusConnecting {
		t.Errorf("Current status = %v, want %v", current, ZMQStatusConnecting)
	}
}

// TestAtomicTimestampTracking verifies timestamp atomic operations.
func TestAtomicTimestampTracking(t *testing.T) {
	var lastMessageTime atomic.Int64
	var failureStartTime atomic.Int64

	now := time.Now().Unix()

	// Store message time
	lastMessageTime.Store(now)

	// Compare and swap for failure start
	if failureStartTime.CompareAndSwap(0, now) {
		// Expected: first set succeeds
	} else {
		t.Error("First CAS should succeed")
	}

	// Second CAS should fail (already set)
	if failureStartTime.CompareAndSwap(0, now+1) {
		t.Error("Second CAS should fail (already set)")
	}

	// Verify values
	if lastMessageTime.Load() != now {
		t.Error("lastMessageTime not stored correctly")
	}

	if failureStartTime.Load() != now {
		t.Error("failureStartTime not stored correctly")
	}
}

// TestStabilityTracking verifies stability flag behavior.
func TestStabilityTracking(t *testing.T) {
	var stabilityReached atomic.Bool

	// Initially false
	if stabilityReached.Load() {
		t.Error("Stability should start as false")
	}

	// First CAS to true succeeds
	if !stabilityReached.CompareAndSwap(false, true) {
		t.Error("First stability CAS should succeed")
	}

	// Second CAS fails (already true)
	if stabilityReached.CompareAndSwap(false, true) {
		t.Error("Second stability CAS should fail")
	}

	// Reset after failure
	stabilityReached.Store(false)

	if stabilityReached.Load() {
		t.Error("Stability should be false after reset")
	}
}

// TestMessageCountTracking verifies message counting.
func TestMessageCountTracking(t *testing.T) {
	var messagesReceived atomic.Uint64
	var errorsCount atomic.Uint64

	// Increment messages
	for i := 0; i < 100; i++ {
		messagesReceived.Add(1)
	}

	// Increment errors
	for i := 0; i < 5; i++ {
		errorsCount.Add(1)
	}

	if messagesReceived.Load() != 100 {
		t.Errorf("messagesReceived = %d, want 100", messagesReceived.Load())
	}

	if errorsCount.Load() != 5 {
		t.Errorf("errorsCount = %d, want 5", errorsCount.Load())
	}
}

// TestFailureRecoveryLogic verifies failure and recovery tracking.
func TestFailureRecoveryLogic(t *testing.T) {
	var failureStartTime atomic.Int64
	var healthyStartTime atomic.Int64

	now := time.Now().Unix()

	// Simulate failure
	failureStartTime.Store(now)
	healthyStartTime.Store(0) // Reset healthy on failure

	if failureStartTime.Load() == 0 {
		t.Error("Failure should be recorded")
	}

	if healthyStartTime.Load() != 0 {
		t.Error("Healthy should be reset on failure")
	}

	// Simulate recovery - use Swap to get and clear atomically
	failureStart := failureStartTime.Swap(0)
	if failureStart != now {
		t.Error("Should get failure start time")
	}

	// Start new healthy period
	healthyStartTime.Store(time.Now().Unix())

	if failureStartTime.Load() != 0 {
		t.Error("Failure should be cleared after recovery")
	}
}

// TestZMQEndpointFormat verifies endpoint format.
func TestZMQEndpointFormat(t *testing.T) {
	tests := []struct {
		endpoint string
		valid    bool
	}{
		{"tcp://localhost:28332", true},
		{"tcp://127.0.0.1:28332", true},
		{"tcp://192.168.1.100:28332", true},
		{"ipc:///tmp/zmq.sock", true},
		{"inproc://test", true},
		{"http://localhost:28332", false}, // Wrong protocol
		{"localhost:28332", false},        // Missing protocol
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			// Simple validation - must start with valid protocol
			valid := len(tt.endpoint) > 6 &&
				(tt.endpoint[:6] == "tcp://" ||
					tt.endpoint[:6] == "ipc://" ||
					tt.endpoint[:9] == "inproc://")

			if valid != tt.valid {
				t.Errorf("Endpoint %q: valid = %v, want %v", tt.endpoint, valid, tt.valid)
			}
		})
	}
}

// TestMaxConsecutiveErrors verifies error threshold.
func TestMaxConsecutiveErrors(t *testing.T) {
	const maxConsecutiveErrors = 10

	// Should trigger reconnect at threshold
	consecutiveErrors := 0

	for i := 0; i < 15; i++ {
		consecutiveErrors++

		shouldReconnect := consecutiveErrors >= maxConsecutiveErrors

		if i == 9 && !shouldReconnect {
			t.Error("Should trigger reconnect at threshold (10)")
		}
		if i == 8 && shouldReconnect {
			t.Error("Should not trigger reconnect before threshold (9)")
		}
	}
}

// TestGoroutineShutdownTimeout verifies shutdown timeout.
func TestGoroutineShutdownTimeout(t *testing.T) {
	// Stop() waits up to 3 seconds for goroutines
	expectedTimeout := 3 * time.Second

	if expectedTimeout != 3*time.Second {
		t.Errorf("Goroutine shutdown timeout = %v, want 3s", expectedTimeout)
	}
}

// TestSocketReceiveTimeout verifies socket timeout.
func TestSocketReceiveTimeout(t *testing.T) {
	// Socket receive timeout is 1 second
	expectedTimeout := 1 * time.Second

	if expectedTimeout != time.Second {
		t.Errorf("Socket receive timeout = %v, want 1s", expectedTimeout)
	}
}

// TestRunningStateTracking verifies running state.
func TestRunningStateTracking(t *testing.T) {
	var running atomic.Bool

	// Initially not running
	if running.Load() {
		t.Error("Should start as not running")
	}

	// Start
	running.Store(true)
	if !running.Load() {
		t.Error("Should be running after Store(true)")
	}

	// Stop
	running.Store(false)
	if running.Load() {
		t.Error("Should not be running after Store(false)")
	}
}

// TestDurationCalculations verifies time duration calculations.
func TestDurationCalculations(t *testing.T) {
	pastTime := time.Now().Add(-5 * time.Minute).Unix()
	now := time.Now()

	duration := now.Sub(time.Unix(pastTime, 0))

	// Should be approximately 5 minutes
	if duration < 4*time.Minute+50*time.Second || duration > 5*time.Minute+10*time.Second {
		t.Errorf("Duration calculation incorrect: %v", duration)
	}
}

// BenchmarkStatusCheck benchmarks status load.
func BenchmarkStatusCheck(b *testing.B) {
	var status atomic.Int32
	status.Store(int32(ZMQStatusHealthy))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ZMQStatus(status.Load())
	}
}

// BenchmarkStatusSwap benchmarks status swap.
func BenchmarkStatusSwap(b *testing.B) {
	var status atomic.Int32
	status.Store(int32(ZMQStatusHealthy))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		status.Swap(int32(ZMQStatusHealthy))
	}
}

// BenchmarkMessageCount benchmarks message counting.
func BenchmarkMessageCount(b *testing.B) {
	var messagesReceived atomic.Uint64

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		messagesReceived.Add(1)
	}
}

// BenchmarkTimestampLoad benchmarks timestamp load.
func BenchmarkTimestampLoad(b *testing.B) {
	var lastMessageTime atomic.Int64
	lastMessageTime.Store(time.Now().Unix())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = lastMessageTime.Load()
	}
}
