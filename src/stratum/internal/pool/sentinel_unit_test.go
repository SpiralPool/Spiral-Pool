// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// =============================================================================
// truncateHash — Pure helper function
// =============================================================================

func TestTruncateHash_ShortHash(t *testing.T) {
	t.Parallel()
	result := truncateHash("abcdef")
	if result != "abcdef" {
		t.Errorf("expected short hash unchanged, got %q", result)
	}
}

func TestTruncateHash_LongHash(t *testing.T) {
	t.Parallel()
	hash := "0000000000000000000abcdef1234567890abcdef"
	result := truncateHash(hash)

	if len(result) > 19 { // 16 chars + "..."
		t.Errorf("expected truncated hash, got %q (len %d)", result, len(result))
	}
	if result != "0000000000000000..." {
		t.Errorf("expected '0000000000000000...', got %q", result)
	}
}

func TestTruncateHash_Exactly16Chars(t *testing.T) {
	t.Parallel()
	hash := "0123456789abcdef"
	result := truncateHash(hash)
	if result != "0123456789abcdef" {
		t.Errorf("expected 16-char hash unchanged, got %q", result)
	}
}

func TestTruncateHash_17Chars(t *testing.T) {
	t.Parallel()
	hash := "0123456789abcdefg"
	result := truncateHash(hash)
	if result != "0123456789abcdef..." {
		t.Errorf("expected truncated, got %q", result)
	}
}

func TestTruncateHash_EmptyString(t *testing.T) {
	t.Parallel()
	result := truncateHash("")
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

// =============================================================================
// readMetricValue — Prometheus metric reading
// =============================================================================

func TestReadMetricValue_NilReturnsZero(t *testing.T) {
	t.Parallel()
	result := readMetricValue(nil)
	if result != 0 {
		t.Errorf("expected 0 for nil metric, got %f", result)
	}
}

// =============================================================================
// fireAlert — Cooldown deduplication logic
// =============================================================================

func TestFireAlert_CooldownPreventsRepeat(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	s := &Sentinel{
		logger:    logger.Sugar(),
		cfg: &config.SentinelConfig{
			AlertCooldown: 1 * time.Hour,
		},
		cooldowns: make(map[string]time.Time),
	}

	// First alert should go through
	s.fireAlert(nil, "test_alert", severityWarning, "DGB", "pool1", "test message", nil)

	// Verify cooldown was recorded
	s.cooldownMu.Lock()
	_, exists := s.cooldowns["test_alert:DGB:pool1"]
	s.cooldownMu.Unlock()

	if !exists {
		t.Error("expected cooldown to be recorded after first alert")
	}
}

func TestFireAlert_DifferentAlertTypesBypassCooldown(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	s := &Sentinel{
		logger:    logger.Sugar(),
		cfg: &config.SentinelConfig{
			AlertCooldown: 1 * time.Hour,
		},
		cooldowns: make(map[string]time.Time),
	}

	s.fireAlert(nil, "alert_type_1", severityWarning, "DGB", "pool1", "msg1", nil)
	s.fireAlert(nil, "alert_type_2", severityWarning, "DGB", "pool1", "msg2", nil)

	s.cooldownMu.Lock()
	count := len(s.cooldowns)
	s.cooldownMu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 cooldown entries for different alert types, got %d", count)
	}
}

func TestFireAlert_DifferentCoinsBypassCooldown(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	s := &Sentinel{
		logger:    logger.Sugar(),
		cfg: &config.SentinelConfig{
			AlertCooldown: 1 * time.Hour,
		},
		cooldowns: make(map[string]time.Time),
	}

	s.fireAlert(nil, "test_alert", severityWarning, "DGB", "pool1", "msg1", nil)
	s.fireAlert(nil, "test_alert", severityWarning, "BTC", "pool2", "msg2", nil)

	s.cooldownMu.Lock()
	count := len(s.cooldowns)
	s.cooldownMu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 cooldown entries for different coins, got %d", count)
	}
}

func TestFireAlert_ExpiredCooldownAllowsRepeat(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	s := &Sentinel{
		logger:    logger.Sugar(),
		cfg: &config.SentinelConfig{
			AlertCooldown: 1 * time.Millisecond, // Very short cooldown
		},
		cooldowns: make(map[string]time.Time),
	}

	s.fireAlert(nil, "test_alert", severityWarning, "DGB", "pool1", "msg1", nil)

	// Wait for cooldown to expire
	time.Sleep(5 * time.Millisecond)

	// Should update the cooldown timestamp
	s.cooldownMu.Lock()
	firstTime := s.cooldowns["test_alert:DGB:pool1"]
	s.cooldownMu.Unlock()

	s.fireAlert(nil, "test_alert", severityWarning, "DGB", "pool1", "msg2", nil)

	s.cooldownMu.Lock()
	secondTime := s.cooldowns["test_alert:DGB:pool1"]
	s.cooldownMu.Unlock()

	if !secondTime.After(firstTime) {
		t.Error("expected cooldown timestamp to be updated after expiry")
	}
}

// =============================================================================
// trackHARoleChange — HA flap detection
// =============================================================================

func TestTrackHARoleChange_RecordsTransitions(t *testing.T) {
	t.Parallel()

	s := &Sentinel{
		cfg: &config.SentinelConfig{
			HAFlapWindow:    10 * time.Minute,
			HAFlapThreshold: 3,
		},
		haRoleHistory: make([]time.Time, 0, 16),
		mu:            sync.Mutex{},
	}

	// Initial role — no history entry (no previous role)
	s.trackHARoleChange("MASTER")

	s.mu.Lock()
	histLen := len(s.haRoleHistory)
	s.mu.Unlock()
	if histLen != 0 {
		t.Errorf("expected 0 history entries for first role, got %d", histLen)
	}

	// Transition MASTER -> BACKUP
	s.trackHARoleChange("BACKUP")

	s.mu.Lock()
	histLen = len(s.haRoleHistory)
	s.mu.Unlock()
	if histLen != 1 {
		t.Errorf("expected 1 transition recorded, got %d", histLen)
	}

	// Same role — no new entry
	s.trackHARoleChange("BACKUP")

	s.mu.Lock()
	histLen = len(s.haRoleHistory)
	s.mu.Unlock()
	if histLen != 1 {
		t.Errorf("expected still 1 transition (no change), got %d", histLen)
	}

	// Transition BACKUP -> MASTER
	s.trackHARoleChange("MASTER")

	s.mu.Lock()
	histLen = len(s.haRoleHistory)
	s.mu.Unlock()
	if histLen != 2 {
		t.Errorf("expected 2 transitions, got %d", histLen)
	}
}

func TestTrackHARoleChange_TrimsOldEntries(t *testing.T) {
	t.Parallel()

	s := &Sentinel{
		cfg: &config.SentinelConfig{
			HAFlapWindow:    100 * time.Millisecond, // Very short window
			HAFlapThreshold: 3,
		},
		haRoleHistory: make([]time.Time, 0, 16),
		mu:            sync.Mutex{},
	}

	// Record some transitions
	s.trackHARoleChange("MASTER")
	s.trackHARoleChange("BACKUP")
	s.trackHARoleChange("MASTER")
	s.trackHARoleChange("BACKUP")

	s.mu.Lock()
	histLen := len(s.haRoleHistory)
	s.mu.Unlock()
	if histLen != 3 {
		t.Errorf("expected 3 transitions, got %d", histLen)
	}

	// Wait for window to expire
	time.Sleep(150 * time.Millisecond)

	// New transition should trigger trimming of old entries
	s.trackHARoleChange("MASTER")

	s.mu.Lock()
	histLen = len(s.haRoleHistory)
	s.mu.Unlock()

	// Old entries should be trimmed, only the new one remains
	if histLen > 1 {
		t.Errorf("expected old entries trimmed, got %d remaining", histLen)
	}
}

// =============================================================================
// NewSentinel — Initialization wiring
// =============================================================================

func TestNewSentinel_InitializesAllMaps(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	cfg := &config.SentinelConfig{
		Enabled:       true,
		CheckInterval: 60 * time.Second,
		AlertCooldown: 15 * time.Minute,
		HAFlapWindow:  10 * time.Minute,
	}

	// NewSentinel requires a Coordinator, but we can pass nil and just check
	// that maps are initialized (we won't call Run)
	s := NewSentinel(nil, cfg, nil, logger)

	if s.prevHashrates == nil {
		t.Error("prevHashrates map not initialized")
	}
	if s.prevConnections == nil {
		t.Error("prevConnections map not initialized")
	}
	if s.lastBlockCount == nil {
		t.Error("lastBlockCount map not initialized")
	}
	if s.lastBlockTime == nil {
		t.Error("lastBlockTime map not initialized")
	}
	if s.heightTracker == nil {
		t.Error("heightTracker map not initialized")
	}
	if s.heightChangedAt == nil {
		t.Error("heightChangedAt map not initialized")
	}
	if s.prevDropped == nil {
		t.Error("prevDropped map not initialized")
	}
	if s.paymentPending == nil {
		t.Error("paymentPending map not initialized")
	}
	if s.paymentStallCount == nil {
		t.Error("paymentStallCount map not initialized")
	}
	if s.prevOrphaned == nil {
		t.Error("prevOrphaned map not initialized")
	}
	if s.cooldowns == nil {
		t.Error("cooldowns map not initialized")
	}
	if s.haRoleHistory == nil {
		t.Error("haRoleHistory slice not initialized")
	}
}

func TestNewSentinel_RecentAlertsBufferInitialized(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	cfg := &config.SentinelConfig{
		Enabled:       true,
		AlertCooldown: 1 * time.Millisecond,
	}

	s := NewSentinel(nil, cfg, nil, logger)

	// Ring buffer should be nil initially (no alerts fired yet)
	alerts := s.GetRecentAlerts(time.Time{})
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts initially, got %d", len(alerts))
	}

	// Fire an alert and verify it lands in the buffer
	s.fireAlert(nil, "test_buffer", severityWarning, "DGB", "dgb_main", "buffer test", nil)

	alerts = s.GetRecentAlerts(time.Time{})
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert after firing, got %d", len(alerts))
	}
	if alerts[0].AlertType != "test_buffer" {
		t.Errorf("expected alert type 'test_buffer', got %q", alerts[0].AlertType)
	}
	if alerts[0].Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", alerts[0].Severity)
	}
}

func TestGetRecentAlerts_SinceFilter(t *testing.T) {
	t.Parallel()
	logger, _ := zap.NewDevelopment()

	cfg := &config.SentinelConfig{
		Enabled:       true,
		AlertCooldown: 1 * time.Millisecond,
	}

	s := NewSentinel(nil, cfg, nil, logger)

	// Fire alert, wait, record timestamp, fire another
	s.fireAlert(nil, "old_alert", severityInfo, "BTC", "", "old", nil)
	time.Sleep(5 * time.Millisecond)
	since := time.Now()
	time.Sleep(5 * time.Millisecond)
	s.fireAlert(nil, "new_alert", severityWarning, "DGB", "", "new", nil)

	// GetRecentAlerts(since) should only return the new alert
	alerts := s.GetRecentAlerts(since)
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert after since, got %d", len(alerts))
	}
	if len(alerts) > 0 && alerts[0].AlertType != "new_alert" {
		t.Errorf("expected 'new_alert', got %q", alerts[0].AlertType)
	}
}

// =============================================================================
// Severity constants — Verify string values
// =============================================================================

func TestSeverityConstants(t *testing.T) {
	t.Parallel()
	if string(severityCritical) != "critical" {
		t.Errorf("expected 'critical', got %q", severityCritical)
	}
	if string(severityWarning) != "warning" {
		t.Errorf("expected 'warning', got %q", severityWarning)
	}
	if string(severityInfo) != "info" {
		t.Errorf("expected 'info', got %q", severityInfo)
	}
}
