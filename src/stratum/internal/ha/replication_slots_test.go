// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package ha — tests for replication slot monitoring and auto-cleanup.
//
// The checkSlots() method requires a live PostgreSQL connection and is not
// tested here. These tests cover all non-DB-dependent logic: config defaults,
// config normalization, protected slot detection, byte formatting, callback
// registration, lifecycle management, and public API surface.
package ha

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════════════════════════════════
// DefaultReplicationSlotMonitorConfig tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestDefaultReplicationSlotMonitorConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultReplicationSlotMonitorConfig()

	if !cfg.Enabled {
		t.Error("expected Enabled=true by default")
	}
	if cfg.CheckInterval != 5*time.Minute {
		t.Errorf("expected CheckInterval=5m, got %v", cfg.CheckInterval)
	}
	if cfg.WALRetentionWarningBytes != 1*1024*1024*1024 {
		t.Errorf("expected WALRetentionWarningBytes=1GB, got %d", cfg.WALRetentionWarningBytes)
	}
	if cfg.WALRetentionCriticalBytes != 10*1024*1024*1024 {
		t.Errorf("expected WALRetentionCriticalBytes=10GB, got %d", cfg.WALRetentionCriticalBytes)
	}
	if cfg.AutoDropOrphanedSlots {
		t.Error("expected AutoDropOrphanedSlots=false by default (safety)")
	}
	if cfg.OrphanGracePeriod != 24*time.Hour {
		t.Errorf("expected OrphanGracePeriod=24h, got %v", cfg.OrphanGracePeriod)
	}
	if len(cfg.ProtectedSlotNames) != 1 || cfg.ProtectedSlotNames[0] != "spiral_backup_slot" {
		t.Errorf("expected ProtectedSlotNames=[spiral_backup_slot], got %v", cfg.ProtectedSlotNames)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// NewReplicationSlotMonitor tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestNewReplicationSlotMonitor_DefaultsApplied(t *testing.T) {
	t.Parallel()
	// Zero-value config — NewReplicationSlotMonitor should fill in defaults
	cfg := ReplicationSlotMonitorConfig{}
	m := NewReplicationSlotMonitor(cfg, nil, zap.NewNop())

	if m.config.CheckInterval != 5*time.Minute {
		t.Errorf("expected CheckInterval default 5m, got %v", m.config.CheckInterval)
	}
	if m.config.WALRetentionWarningBytes != 1*1024*1024*1024 {
		t.Errorf("expected WALRetentionWarningBytes default 1GB, got %d", m.config.WALRetentionWarningBytes)
	}
	if m.config.WALRetentionCriticalBytes != 10*1024*1024*1024 {
		t.Errorf("expected WALRetentionCriticalBytes default 10GB, got %d", m.config.WALRetentionCriticalBytes)
	}
	if m.config.OrphanGracePeriod != 24*time.Hour {
		t.Errorf("expected OrphanGracePeriod default 24h, got %v", m.config.OrphanGracePeriod)
	}
	if len(m.config.ProtectedSlotNames) != 1 || m.config.ProtectedSlotNames[0] != "spiral_backup_slot" {
		t.Errorf("expected default ProtectedSlotNames, got %v", m.config.ProtectedSlotNames)
	}
}

func TestNewReplicationSlotMonitor_CustomConfig(t *testing.T) {
	t.Parallel()
	cfg := ReplicationSlotMonitorConfig{
		CheckInterval:             1 * time.Minute,
		WALRetentionWarningBytes:  500 * 1024 * 1024,
		WALRetentionCriticalBytes: 5 * 1024 * 1024 * 1024,
		OrphanGracePeriod:         12 * time.Hour,
		ProtectedSlotNames:        []string{"slot_a", "slot_b"},
	}
	m := NewReplicationSlotMonitor(cfg, nil, zap.NewNop())

	// Custom values should be preserved (not overwritten by defaults)
	if m.config.CheckInterval != 1*time.Minute {
		t.Errorf("expected custom CheckInterval 1m, got %v", m.config.CheckInterval)
	}
	if m.config.WALRetentionWarningBytes != 500*1024*1024 {
		t.Errorf("expected custom WALRetentionWarningBytes, got %d", m.config.WALRetentionWarningBytes)
	}
	if m.config.OrphanGracePeriod != 12*time.Hour {
		t.Errorf("expected custom OrphanGracePeriod, got %v", m.config.OrphanGracePeriod)
	}
	if len(m.config.ProtectedSlotNames) != 2 {
		t.Errorf("expected 2 protected slots, got %d", len(m.config.ProtectedSlotNames))
	}
}

func TestNewReplicationSlotMonitor_SlotLastActiveInitialized(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{}, nil, zap.NewNop())

	if m.slotLastActive == nil {
		t.Error("expected slotLastActive map to be initialized")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// isProtectedSlot tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestIsProtectedSlot_Protected(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{
		ProtectedSlotNames: []string{"spiral_backup_slot", "custom_slot"},
	}, nil, zap.NewNop())

	if !m.isProtectedSlot("spiral_backup_slot") {
		t.Error("expected spiral_backup_slot to be protected")
	}
	if !m.isProtectedSlot("custom_slot") {
		t.Error("expected custom_slot to be protected")
	}
}

func TestIsProtectedSlot_NotProtected(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{
		ProtectedSlotNames: []string{"spiral_backup_slot"},
	}, nil, zap.NewNop())

	if m.isProtectedSlot("orphaned_slot") {
		t.Error("expected orphaned_slot to NOT be protected")
	}
	if m.isProtectedSlot("") {
		t.Error("expected empty string to NOT be protected")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// formatBytes tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{10737418240, "10.00 GB"},
		{1099511627776, "1.00 TB"},
		{2199023255552, "2.00 TB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.bytes)
		if got != tt.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.expected)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// GetStatus tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestGetStatus_InitialState(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{}, nil, zap.NewNop())

	status := m.GetStatus()

	if status.TotalWALRetention != 0 {
		t.Errorf("expected TotalWALRetention=0, got %d", status.TotalWALRetention)
	}
	if len(status.Slots) != 0 {
		t.Errorf("expected 0 slots, got %d", len(status.Slots))
	}
	if len(status.OrphanedSlots) != 0 {
		t.Errorf("expected 0 orphaned slots, got %d", len(status.OrphanedSlots))
	}
	if status.LastError != "" {
		t.Errorf("expected no LastError, got %q", status.LastError)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Callback setter tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestSetWarningCallback(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{}, nil, zap.NewNop())

	called := false
	m.SetWarningCallback(func(slot ReplicationSlot) {
		called = true
	})

	if m.onWarning == nil {
		t.Error("expected onWarning callback to be set")
	}

	// Invoke to verify it's the right function
	m.onWarning(ReplicationSlot{})
	if !called {
		t.Error("expected warning callback to be invoked")
	}
}

func TestSetCriticalCallback(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{}, nil, zap.NewNop())

	called := false
	m.SetCriticalCallback(func(slot ReplicationSlot) {
		called = true
	})

	if m.onCritical == nil {
		t.Error("expected onCritical callback to be set")
	}

	m.onCritical(ReplicationSlot{})
	if !called {
		t.Error("expected critical callback to be invoked")
	}
}

func TestSetDroppedCallback(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{}, nil, zap.NewNop())

	var droppedName string
	m.SetDroppedCallback(func(slotName string) {
		droppedName = slotName
	})

	if m.onDropped == nil {
		t.Error("expected onDropped callback to be set")
	}

	m.onDropped("test_slot")
	if droppedName != "test_slot" {
		t.Errorf("expected dropped slot name 'test_slot', got %q", droppedName)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Start/Stop lifecycle tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestStart_DisabledConfig_NoOp(t *testing.T) {
	t.Parallel()
	cfg := ReplicationSlotMonitorConfig{
		Enabled: false,
	}
	m := NewReplicationSlotMonitor(cfg, nil, zap.NewNop())

	err := m.Start(t.Context())
	if err != nil {
		t.Errorf("expected nil error for disabled monitor, got %v", err)
	}

	// Stop should not panic even if Start was a no-op
	m.Stop()
}

// ═══════════════════════════════════════════════════════════════════════════════
// DropOrphanedSlot tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestDropOrphanedSlot_ProtectedSlot_Rejected(t *testing.T) {
	t.Parallel()
	m := NewReplicationSlotMonitor(ReplicationSlotMonitorConfig{
		ProtectedSlotNames: []string{"spiral_backup_slot"},
	}, nil, zap.NewNop())

	err := m.DropOrphanedSlot("spiral_backup_slot")
	if err == nil {
		t.Fatal("expected error when dropping protected slot")
	}
	if err.Error() != "cannot drop protected slot: spiral_backup_slot" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// ReplicationSlot struct tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestReplicationSlot_Fields(t *testing.T) {
	t.Parallel()

	slot := ReplicationSlot{
		SlotName:          "test_slot",
		SlotType:          "physical",
		Active:            true,
		ActivePID:         12345,
		WALRetentionBytes: 2 * 1024 * 1024 * 1024, // 2GB
	}

	if slot.SlotName != "test_slot" {
		t.Errorf("expected SlotName 'test_slot', got %q", slot.SlotName)
	}
	if slot.SlotType != "physical" {
		t.Errorf("expected SlotType 'physical', got %q", slot.SlotType)
	}
	if !slot.Active {
		t.Error("expected Active=true")
	}
	if slot.WALRetentionBytes != 2*1024*1024*1024 {
		t.Errorf("expected WALRetentionBytes=2GB, got %d", slot.WALRetentionBytes)
	}
}
