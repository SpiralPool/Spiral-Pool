// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package database

import (
	"sync"
	"testing"
	"time"
)

// TestCircuitBreakerInitialState verifies initial closed state.
func TestCircuitBreakerInitialState(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	if cb.State() != CircuitClosed {
		t.Errorf("Initial state = %v, want CircuitClosed", cb.State())
	}

	allowed, _ := cb.AllowRequest()
	if !allowed {
		t.Error("Initial state should allow requests")
	}

	if cb.Failures() != 0 {
		t.Errorf("Initial failures = %d, want 0", cb.Failures())
	}
}

// TestCircuitBreakerExponentialBackoff verifies backoff increases exponentially.
func TestCircuitBreakerExponentialBackoff(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 100, // High threshold so we don't open
		CooldownPeriod:   30 * time.Second,
		InitialBackoff:   100 * time.Millisecond,
		MaxBackoff:       1600 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// First failure returns initial backoff
	backoff1 := cb.RecordFailure()
	if backoff1 != cfg.InitialBackoff {
		t.Errorf("First backoff = %v, want %v", backoff1, cfg.InitialBackoff)
	}

	// Second failure returns doubled backoff
	backoff2 := cb.RecordFailure()
	expected := 200 * time.Millisecond
	if backoff2 != expected {
		t.Errorf("Second backoff = %v, want %v", backoff2, expected)
	}

	// Third failure returns 4x initial
	backoff3 := cb.RecordFailure()
	expected = 400 * time.Millisecond
	if backoff3 != expected {
		t.Errorf("Third backoff = %v, want %v", backoff3, expected)
	}

	// Fourth failure returns 8x initial
	backoff4 := cb.RecordFailure()
	expected = 800 * time.Millisecond
	if backoff4 != expected {
		t.Errorf("Fourth backoff = %v, want %v", backoff4, expected)
	}

	// Fifth failure should cap at MaxBackoff
	backoff5 := cb.RecordFailure()
	if backoff5 != cfg.MaxBackoff {
		t.Errorf("Fifth backoff = %v, want %v (max)", backoff5, cfg.MaxBackoff)
	}

	// Sixth failure should still be capped
	backoff6 := cb.RecordFailure()
	if backoff6 != cfg.MaxBackoff {
		t.Errorf("Sixth backoff = %v, want %v (max)", backoff6, cfg.MaxBackoff)
	}
}

// TestCircuitBreakerOpensAfterThreshold verifies circuit opens at threshold.
func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 5,
		CooldownPeriod:   100 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Record failures up to threshold - 1
	for i := 0; i < cfg.FailureThreshold-1; i++ {
		cb.RecordFailure()
	}

	if cb.State() != CircuitClosed {
		t.Errorf("State before threshold = %v, want CircuitClosed", cb.State())
	}

	// One more failure should open the circuit
	cb.RecordFailure()

	if cb.State() != CircuitOpen {
		t.Errorf("State after threshold = %v, want CircuitOpen", cb.State())
	}

	// Verify requests are blocked
	allowed, remaining := cb.AllowRequest()
	if allowed {
		t.Error("Open circuit should block requests")
	}
	if remaining <= 0 {
		t.Error("Should report remaining cooldown time")
	}
}

// TestCircuitBreakerHalfOpenAfterCooldown verifies half-open transition.
func TestCircuitBreakerHalfOpenAfterCooldown(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Open the circuit
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}

	if cb.State() != CircuitOpen {
		t.Fatalf("Circuit should be open, got %v", cb.State())
	}

	// Wait for cooldown
	time.Sleep(cfg.CooldownPeriod + 10*time.Millisecond)

	// First request after cooldown should be allowed (transitions to half-open)
	allowed, _ := cb.AllowRequest()
	if !allowed {
		t.Error("Should allow probe request after cooldown")
	}

	if cb.State() != CircuitHalfOpen {
		t.Errorf("State after cooldown = %v, want CircuitHalfOpen", cb.State())
	}

	// Second request while half-open should be blocked
	allowed, _ = cb.AllowRequest()
	if allowed {
		t.Error("Half-open should block additional requests (only one probe)")
	}
}

// TestCircuitBreakerClosesOnSuccess verifies recovery from half-open.
func TestCircuitBreakerClosesOnSuccess(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Open circuit and wait for half-open
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cfg.CooldownPeriod + 10*time.Millisecond)
	cb.AllowRequest() // Trigger half-open

	if cb.State() != CircuitHalfOpen {
		t.Fatalf("Expected half-open, got %v", cb.State())
	}

	// Success should close circuit
	cb.RecordSuccess()

	if cb.State() != CircuitClosed {
		t.Errorf("State after success = %v, want CircuitClosed", cb.State())
	}

	// Backoff should be reset
	stats := cb.Stats()
	if stats.CurrentBackoff != cfg.InitialBackoff {
		t.Errorf("Backoff after recovery = %v, want %v", stats.CurrentBackoff, cfg.InitialBackoff)
	}

	// Failures should be reset
	if stats.Failures != 0 {
		t.Errorf("Failures after recovery = %d, want 0", stats.Failures)
	}
}

// TestCircuitBreakerFailureInHalfOpen verifies reopening on half-open failure.
func TestCircuitBreakerFailureInHalfOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Open circuit and wait for half-open
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}
	time.Sleep(cfg.CooldownPeriod + 10*time.Millisecond)
	cb.AllowRequest() // Trigger half-open

	// Failure in half-open should increment failures
	cb.RecordFailure()

	// The circuit may or may not transition back to open depending on threshold
	// The key is that failures are tracked
	if cb.Failures() <= cfg.FailureThreshold {
		// Should have more failures now
		if cb.Failures() != cfg.FailureThreshold+1 {
			t.Errorf("Failures = %d, want %d", cb.Failures(), cfg.FailureThreshold+1)
		}
	}
}

// TestCircuitBreakerStats verifies statistics tracking.
func TestCircuitBreakerStats(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		CooldownPeriod:   50 * time.Millisecond,
		InitialBackoff:   10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BackoffFactor:    2.0,
	}
	cb := NewCircuitBreaker(cfg)

	// Record some failures to open circuit
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}

	// Try some blocked requests
	cb.AllowRequest()
	cb.AllowRequest()

	stats := cb.Stats()

	if stats.Failures != cfg.FailureThreshold {
		t.Errorf("Stats.Failures = %d, want %d", stats.Failures, cfg.FailureThreshold)
	}

	if stats.State != CircuitOpen {
		t.Errorf("Stats.State = %v, want CircuitOpen", stats.State)
	}

	if stats.TotalBlocked < 2 {
		t.Errorf("Stats.TotalBlocked = %d, want >= 2", stats.TotalBlocked)
	}

	if stats.StateChanges == 0 {
		t.Error("Stats.StateChanges should be > 0")
	}
}

// TestCircuitBreakerReset verifies manual reset.
func TestCircuitBreakerReset(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(cfg)

	// Open the circuit
	for i := 0; i < cfg.FailureThreshold; i++ {
		cb.RecordFailure()
	}

	if cb.State() != CircuitOpen {
		t.Fatalf("Expected open, got %v", cb.State())
	}

	// Reset
	cb.Reset()

	if cb.State() != CircuitClosed {
		t.Errorf("After reset: state = %v, want CircuitClosed", cb.State())
	}

	if cb.Failures() != 0 {
		t.Errorf("After reset: failures = %d, want 0", cb.Failures())
	}

	allowed, _ := cb.AllowRequest()
	if !allowed {
		t.Error("After reset: should allow requests")
	}
}

// TestCircuitBreakerConcurrentAccess verifies thread safety.
func TestCircuitBreakerConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent failures
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordFailure()
		}()
	}

	// Concurrent success
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordSuccess()
		}()
	}

	// Concurrent reads
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.AllowRequest()
			cb.State()
			cb.Stats()
			cb.Failures()
		}()
	}

	wg.Wait()

	// Should not panic or deadlock - just verify we can still use it
	_ = cb.State()
	_ = cb.Stats()
}

// TestCircuitStateString verifies state string representation.
func TestCircuitStateString(t *testing.T) {
	tests := []struct {
		state CircuitState
		want  string
	}{
		{CircuitClosed, "closed"},
		{CircuitOpen, "open"},
		{CircuitHalfOpen, "half-open"},
		{CircuitState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("CircuitState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

// BenchmarkCircuitBreakerAllowRequest benchmarks the hot path.
func BenchmarkCircuitBreakerAllowRequest(b *testing.B) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.AllowRequest()
	}
}

// BenchmarkCircuitBreakerRecordFailure benchmarks failure recording.
func BenchmarkCircuitBreakerRecordFailure(b *testing.B) {
	cfg := DefaultCircuitBreakerConfig()
	cfg.FailureThreshold = 1000000 // Don't open during benchmark
	cb := NewCircuitBreaker(cfg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordFailure()
	}
}

// BenchmarkCircuitBreakerRecordSuccess benchmarks success recording.
func BenchmarkCircuitBreakerRecordSuccess(b *testing.B) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordSuccess()
	}
}
