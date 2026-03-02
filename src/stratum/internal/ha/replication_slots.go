// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package ha provides High Availability (HA) functionality for Spiral Pool.
//
// This file implements replication slot monitoring and auto-cleanup for PostgreSQL HA.
// Orphaned replication slots can cause disk exhaustion by preventing WAL cleanup.
package ha

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ============================================================================
// Replication Slot Monitoring
// ============================================================================
//
// PostgreSQL replication slots hold WAL segments for streaming replication.
// If a replica disconnects and doesn't reconnect, the slot continues to hold
// WAL, eventually causing disk exhaustion.
//
// This monitor:
// 1. Tracks replication slot status and WAL retention
// 2. Alerts when WAL retention exceeds thresholds
// 3. Optionally auto-drops orphaned slots after a grace period
//
// Safety measures:
// - Grace period before auto-drop (default: 24 hours)
// - WAL retention threshold alerts before auto-drop
// - Logging of all actions for post-mortem analysis

// ReplicationSlotMonitorConfig configures the replication slot monitor.
type ReplicationSlotMonitorConfig struct {
	// Enabled controls whether slot monitoring is active
	Enabled bool `yaml:"enabled" json:"enabled"`

	// CheckInterval is how often to check slot status (default: 5 minutes)
	CheckInterval time.Duration `yaml:"checkInterval" json:"checkInterval"`

	// WALRetentionWarningBytes triggers a warning when a slot holds this much WAL
	// Default: 1GB (1073741824 bytes)
	WALRetentionWarningBytes int64 `yaml:"walRetentionWarningBytes" json:"walRetentionWarningBytes"`

	// WALRetentionCriticalBytes triggers a critical alert
	// Default: 10GB (10737418240 bytes)
	WALRetentionCriticalBytes int64 `yaml:"walRetentionCriticalBytes" json:"walRetentionCriticalBytes"`

	// AutoDropOrphanedSlots enables automatic dropping of orphaned slots
	// Default: false (requires explicit enablement for safety)
	AutoDropOrphanedSlots bool `yaml:"autoDropOrphanedSlots" json:"autoDropOrphanedSlots"`

	// OrphanGracePeriod is how long a slot must be inactive before auto-drop
	// Default: 24 hours
	OrphanGracePeriod time.Duration `yaml:"orphanGracePeriod" json:"orphanGracePeriod"`

	// ProtectedSlotNames is a list of slot names that should never be auto-dropped
	// Default: ["spiral_backup_slot"]
	ProtectedSlotNames []string `yaml:"protectedSlotNames" json:"protectedSlotNames"`
}

// DefaultReplicationSlotMonitorConfig returns sensible defaults.
func DefaultReplicationSlotMonitorConfig() ReplicationSlotMonitorConfig {
	return ReplicationSlotMonitorConfig{
		Enabled:                   true,
		CheckInterval:             5 * time.Minute,
		WALRetentionWarningBytes:  1 * 1024 * 1024 * 1024,  // 1GB
		WALRetentionCriticalBytes: 10 * 1024 * 1024 * 1024, // 10GB
		AutoDropOrphanedSlots:     false,                   // Disabled by default for safety
		OrphanGracePeriod:         24 * time.Hour,
		ProtectedSlotNames:        []string{"spiral_backup_slot"},
	}
}

// ReplicationSlot represents a PostgreSQL replication slot.
type ReplicationSlot struct {
	SlotName          string    `json:"slotName"`
	Plugin            string    `json:"plugin,omitempty"`   // For logical slots
	SlotType          string    `json:"slotType"`           // "physical" or "logical"
	Database          string    `json:"database,omitempty"` // For logical slots
	Active            bool      `json:"active"`
	ActivePID         int       `json:"activePid,omitempty"`
	RestartLSN        string    `json:"restartLsn,omitempty"`
	ConfirmedFlushLSN string    `json:"confirmedFlushLsn,omitempty"`
	WALRetentionBytes int64     `json:"walRetentionBytes"`
	LastActiveAt      time.Time `json:"lastActiveAt,omitempty"`
	CreatedAt         time.Time `json:"createdAt,omitempty"`
}

// ReplicationSlotStatus represents the overall slot monitoring status.
type ReplicationSlotStatus struct {
	// Slots is the list of all replication slots
	Slots []ReplicationSlot `json:"slots"`

	// TotalWALRetention is the sum of WAL held by all slots
	TotalWALRetention int64 `json:"totalWalRetention"`

	// OrphanedSlots is the list of slots that appear orphaned
	OrphanedSlots []string `json:"orphanedSlots,omitempty"`

	// SlotsAtWarning is the list of slots exceeding warning threshold
	SlotsAtWarning []string `json:"slotsAtWarning,omitempty"`

	// SlotsAtCritical is the list of slots exceeding critical threshold
	SlotsAtCritical []string `json:"slotsAtCritical,omitempty"`

	// AutoDroppedSlots is the list of slots that were auto-dropped
	AutoDroppedSlots []string `json:"autoDroppedSlots,omitempty"`

	// LastCheck is when the status was last updated
	LastCheck time.Time `json:"lastCheck"`

	// LastError is the most recent error, if any
	LastError string `json:"lastError,omitempty"`
}

// ReplicationSlotMonitor monitors PostgreSQL replication slots.
type ReplicationSlotMonitor struct {
	mu     sync.RWMutex
	config ReplicationSlotMonitorConfig
	logger *zap.SugaredLogger
	db     *sql.DB

	// Current status
	status ReplicationSlotStatus

	// Tracking for orphan detection (slot name -> last active time)
	slotLastActive map[string]time.Time

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Callbacks
	onWarning  func(slot ReplicationSlot)
	onCritical func(slot ReplicationSlot)
	onDropped  func(slotName string)
}

// NewReplicationSlotMonitor creates a new replication slot monitor.
func NewReplicationSlotMonitor(config ReplicationSlotMonitorConfig, db *sql.DB, logger *zap.Logger) *ReplicationSlotMonitor {
	if config.CheckInterval == 0 {
		config.CheckInterval = 5 * time.Minute
	}
	if config.WALRetentionWarningBytes == 0 {
		config.WALRetentionWarningBytes = 1 * 1024 * 1024 * 1024
	}
	if config.WALRetentionCriticalBytes == 0 {
		config.WALRetentionCriticalBytes = 10 * 1024 * 1024 * 1024
	}
	if config.OrphanGracePeriod == 0 {
		config.OrphanGracePeriod = 24 * time.Hour
	}
	if len(config.ProtectedSlotNames) == 0 {
		config.ProtectedSlotNames = []string{"spiral_backup_slot"}
	}

	return &ReplicationSlotMonitor{
		config:         config,
		logger:         logger.Sugar().Named("repl-slots"),
		db:             db,
		slotLastActive: make(map[string]time.Time),
	}
}

// Start begins monitoring replication slots.
func (m *ReplicationSlotMonitor) Start(ctx context.Context) error {
	if !m.config.Enabled {
		m.logger.Info("Replication slot monitoring disabled")
		return nil
	}

	m.ctx, m.cancel = context.WithCancel(ctx)

	m.wg.Add(1)
	go m.monitorLoop()

	m.logger.Infow("Replication slot monitor started",
		"checkInterval", m.config.CheckInterval,
		"warningThreshold", formatBytes(m.config.WALRetentionWarningBytes),
		"criticalThreshold", formatBytes(m.config.WALRetentionCriticalBytes),
		"autoDropOrphaned", m.config.AutoDropOrphanedSlots,
		"orphanGracePeriod", m.config.OrphanGracePeriod,
	)

	return nil
}

// Stop stops the monitor.
func (m *ReplicationSlotMonitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	m.logger.Info("Replication slot monitor stopped")
}

// monitorLoop runs the periodic slot check.
func (m *ReplicationSlotMonitor) monitorLoop() {
	defer m.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			m.logger.Errorw("PANIC recovered in monitorLoop", "panic", r)
		}
	}()

	// Initial check
	m.checkSlots()

	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.checkSlots()
		}
	}
}

// checkSlots queries PostgreSQL for replication slot status.
func (m *ReplicationSlotMonitor) checkSlots() {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()

	// Query replication slots with WAL retention calculation
	query := `
		SELECT
			slot_name,
			COALESCE(plugin, '') as plugin,
			slot_type,
			COALESCE(database, '') as database,
			active,
			COALESCE(active_pid, 0) as active_pid,
			COALESCE(restart_lsn::text, '') as restart_lsn,
			COALESCE(confirmed_flush_lsn::text, '') as confirmed_flush_lsn,
			COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)::bigint as wal_retention_bytes
		FROM pg_replication_slots
		ORDER BY slot_name
	`

	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		m.logger.Warnw("Failed to query replication slots", "error", err)
		m.mu.Lock()
		m.status.LastError = err.Error()
		m.status.LastCheck = time.Now()
		m.mu.Unlock()
		return
	}
	defer rows.Close()

	now := time.Now()
	slots := make([]ReplicationSlot, 0)
	var totalWAL int64
	orphanedSlots := make([]string, 0)
	slotsAtWarning := make([]string, 0)
	slotsAtCritical := make([]string, 0)

	for rows.Next() {
		var slot ReplicationSlot
		var activePID int

		err := rows.Scan(
			&slot.SlotName,
			&slot.Plugin,
			&slot.SlotType,
			&slot.Database,
			&slot.Active,
			&activePID,
			&slot.RestartLSN,
			&slot.ConfirmedFlushLSN,
			&slot.WALRetentionBytes,
		)
		if err != nil {
			m.logger.Warnw("Failed to scan slot row", "error", err)
			continue
		}
		slot.ActivePID = activePID

		// Track activity for orphan detection
		m.mu.Lock()
		if slot.Active {
			m.slotLastActive[slot.SlotName] = now
			slot.LastActiveAt = now
		} else if lastActive, ok := m.slotLastActive[slot.SlotName]; ok {
			slot.LastActiveAt = lastActive
		}
		m.mu.Unlock()

		totalWAL += slot.WALRetentionBytes
		slots = append(slots, slot)

		// Check thresholds
		if slot.WALRetentionBytes >= m.config.WALRetentionCriticalBytes {
			slotsAtCritical = append(slotsAtCritical, slot.SlotName)
			m.logger.Errorw("CRITICAL: Replication slot WAL retention exceeded critical threshold",
				"slot", slot.SlotName,
				"walRetention", formatBytes(slot.WALRetentionBytes),
				"threshold", formatBytes(m.config.WALRetentionCriticalBytes),
				"active", slot.Active,
			)
			m.mu.RLock()
			cb := m.onCritical
			m.mu.RUnlock()
			if cb != nil {
				cb(slot)
			}
		} else if slot.WALRetentionBytes >= m.config.WALRetentionWarningBytes {
			slotsAtWarning = append(slotsAtWarning, slot.SlotName)
			m.logger.Warnw("WARNING: Replication slot WAL retention exceeded warning threshold",
				"slot", slot.SlotName,
				"walRetention", formatBytes(slot.WALRetentionBytes),
				"threshold", formatBytes(m.config.WALRetentionWarningBytes),
				"active", slot.Active,
			)
			m.mu.RLock()
			cb := m.onWarning
			m.mu.RUnlock()
			if cb != nil {
				cb(slot)
			}
		}

		// Check for orphaned slots
		if !slot.Active && !slot.LastActiveAt.IsZero() {
			inactiveFor := now.Sub(slot.LastActiveAt)
			if inactiveFor >= m.config.OrphanGracePeriod {
				orphanedSlots = append(orphanedSlots, slot.SlotName)
			}
		}
	}

	// Auto-drop orphaned slots if enabled
	droppedSlots := make([]string, 0)
	if m.config.AutoDropOrphanedSlots && len(orphanedSlots) > 0 {
		for _, slotName := range orphanedSlots {
			if m.isProtectedSlot(slotName) {
				m.logger.Infow("Skipping auto-drop for protected slot", "slot", slotName)
				continue
			}

			if err := m.dropSlot(ctx, slotName); err != nil {
				m.logger.Errorw("Failed to auto-drop orphaned slot",
					"slot", slotName,
					"error", err,
				)
			} else {
				droppedSlots = append(droppedSlots, slotName)
				m.logger.Warnw("Auto-dropped orphaned replication slot",
					"slot", slotName,
					"reason", "inactive beyond grace period",
					"gracePeriod", m.config.OrphanGracePeriod,
				)
				m.mu.RLock()
				cb := m.onDropped
				m.mu.RUnlock()
				if cb != nil {
					cb(slotName)
				}
			}
		}
	}

	// Update status
	m.mu.Lock()
	m.status = ReplicationSlotStatus{
		Slots:             slots,
		TotalWALRetention: totalWAL,
		OrphanedSlots:     orphanedSlots,
		SlotsAtWarning:    slotsAtWarning,
		SlotsAtCritical:   slotsAtCritical,
		AutoDroppedSlots:  droppedSlots,
		LastCheck:         now,
		LastError:         "",
	}
	m.mu.Unlock()

	m.logger.Debugw("Replication slot check completed",
		"slotCount", len(slots),
		"totalWalRetention", formatBytes(totalWAL),
		"orphanedCount", len(orphanedSlots),
		"warningCount", len(slotsAtWarning),
		"criticalCount", len(slotsAtCritical),
		"droppedCount", len(droppedSlots),
	)
}

// dropSlot drops a replication slot.
func (m *ReplicationSlotMonitor) dropSlot(ctx context.Context, slotName string) error {
	// Safety check: Never drop protected slots
	if m.isProtectedSlot(slotName) {
		return fmt.Errorf("cannot drop protected slot: %s", slotName)
	}

	// Use pg_drop_replication_slot function
	_, err := m.db.ExecContext(ctx, "SELECT pg_drop_replication_slot($1)", slotName)
	if err != nil {
		return fmt.Errorf("pg_drop_replication_slot failed: %w", err)
	}

	// Remove from tracking
	m.mu.Lock()
	delete(m.slotLastActive, slotName)
	m.mu.Unlock()

	return nil
}

// isProtectedSlot checks if a slot is in the protected list.
func (m *ReplicationSlotMonitor) isProtectedSlot(slotName string) bool {
	for _, protected := range m.config.ProtectedSlotNames {
		if slotName == protected {
			return true
		}
	}
	return false
}

// GetStatus returns the current replication slot status.
func (m *ReplicationSlotMonitor) GetStatus() ReplicationSlotStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// ForceCheck triggers an immediate slot check.
func (m *ReplicationSlotMonitor) ForceCheck() {
	go m.checkSlots()
}

// DropOrphanedSlot manually drops an orphaned slot by name.
// Returns an error if the slot is protected or doesn't exist.
func (m *ReplicationSlotMonitor) DropOrphanedSlot(slotName string) error {
	if m.isProtectedSlot(slotName) {
		return fmt.Errorf("cannot drop protected slot: %s", slotName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return m.dropSlot(ctx, slotName)
}

// SetWarningCallback sets the callback for warning-level events.
func (m *ReplicationSlotMonitor) SetWarningCallback(cb func(slot ReplicationSlot)) {
	m.mu.Lock()
	m.onWarning = cb
	m.mu.Unlock()
}

// SetCriticalCallback sets the callback for critical-level events.
func (m *ReplicationSlotMonitor) SetCriticalCallback(cb func(slot ReplicationSlot)) {
	m.mu.Lock()
	m.onCritical = cb
	m.mu.Unlock()
}

// SetDroppedCallback sets the callback for slot drop events.
func (m *ReplicationSlotMonitor) SetDroppedCallback(cb func(slotName string)) {
	m.mu.Lock()
	m.onDropped = cb
	m.mu.Unlock()
}

// formatBytes formats bytes as human-readable string.
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
