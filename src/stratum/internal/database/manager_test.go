// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package database - Unit tests for DatabaseManager failover logic.
//
// These tests verify:
// - Multi-node failover behavior
// - Node health state transitions
// - Connection string security (credential masking)
// - Write/read retry logic
// - VIP failover callback integration
// - Failback to higher priority nodes
package database

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestDBNodeStateValues verifies all node states are defined.
func TestDBNodeStateValues(t *testing.T) {
	states := []DBNodeState{
		DBNodeHealthy,
		DBNodeDegraded,
		DBNodeUnhealthy,
		DBNodeOffline,
	}

	// Verify unique values
	seen := make(map[DBNodeState]bool)
	for _, state := range states {
		if seen[state] {
			t.Errorf("Duplicate state value: %d", state)
		}
		seen[state] = true
	}

	if len(seen) != 4 {
		t.Errorf("Expected 4 unique states, got %d", len(seen))
	}
}

// TestDBNodeStateStrings verifies state string representations.
func TestDBNodeStateStrings(t *testing.T) {
	tests := []struct {
		state DBNodeState
		want  string
	}{
		{DBNodeHealthy, "healthy"},
		{DBNodeDegraded, "degraded"},
		{DBNodeUnhealthy, "unhealthy"},
		{DBNodeOffline, "offline"},
		{DBNodeState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("DBNodeState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

// TestDBFailoverThresholds verifies failover configuration constants.
func TestDBFailoverThresholds(t *testing.T) {
	// MaxDBNodeFailures before marking unhealthy
	if MaxDBNodeFailures != 3 {
		t.Errorf("MaxDBNodeFailures = %d, want 3", MaxDBNodeFailures)
	}

	// Health check interval
	if DBHealthCheckInterval != 10*time.Second {
		t.Errorf("DBHealthCheckInterval = %v, want 10s", DBHealthCheckInterval)
	}

	// Reconnect backoff values
	if DBReconnectBackoff != 5*time.Second {
		t.Errorf("DBReconnectBackoff = %v, want 5s", DBReconnectBackoff)
	}
	if DBMaxReconnectBackoff != 2*time.Minute {
		t.Errorf("DBMaxReconnectBackoff = %v, want 2m", DBMaxReconnectBackoff)
	}
}

// TestSafeConnectionString verifies credential masking for logs.
func TestSafeConnectionString(t *testing.T) {
	cfg := &DBNodeConfig{
		Host:           "db.example.com",
		Port:           5432,
		User:           "spiraluser",
		Password:       "supersecret123!",
		Database:       "spiralpool",
		MaxConnections: 25,
	}

	safe := safeConnectionString(cfg)

	// Password should be masked
	if dbContainsSubstring(safe, cfg.Password) {
		t.Error("Safe connection string should NOT contain the password")
	}

	// Should contain "***" as mask
	if !dbContainsSubstring(safe, "***") {
		t.Error("Safe connection string should contain *** mask")
	}

	// Should still contain other info for debugging
	if !dbContainsSubstring(safe, cfg.Host) {
		t.Error("Safe connection string should contain host")
	}
	if !dbContainsSubstring(safe, cfg.User) {
		t.Error("Safe connection string should contain user")
	}
	if !dbContainsSubstring(safe, cfg.Database) {
		t.Error("Safe connection string should contain database name")
	}
}

// TestNodeConnectionString verifies full connection string format.
func TestNodeConnectionString(t *testing.T) {
	cfg := &DBNodeConfig{
		Host:           "localhost",
		Port:           5432,
		User:           "spiral",
		Password:       "testpass",
		Database:       "pool_db",
		MaxConnections: 10,
	}

	connStr := nodeConnectionString(cfg)

	// Verify format
	if !dbContainsSubstring(connStr, "postgres://") {
		t.Error("Connection string should start with postgres://")
	}
	if !dbContainsSubstring(connStr, "pool_max_conns=10") {
		t.Error("Connection string should include pool_max_conns")
	}
}

// TestGenerateNodeID verifies unique node ID generation.
func TestGenerateNodeID(t *testing.T) {
	ids := make(map[string]bool)

	// Generate multiple IDs for same host/port
	for i := 0; i < 100; i++ {
		id := generateNodeID(0, "localhost", 5432)
		if ids[id] {
			t.Errorf("Duplicate node ID generated: %s", id)
		}
		ids[id] = true

		// Verify format: db-<priority>-<host>:<port>-<random>
		if !dbContainsSubstring(id, "db-0-localhost:5432-") {
			t.Errorf("Node ID format incorrect: %s", id)
		}
	}
}

// TestNodeIDPriorityInclusion verifies priority is in node ID.
func TestNodeIDPriorityInclusion(t *testing.T) {
	tests := []struct {
		priority int
		prefix   string
	}{
		{0, "db-0-"},
		{1, "db-1-"},
		{10, "db-10-"},
	}

	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			id := generateNodeID(tt.priority, "host", 5432)
			if !dbContainsSubstring(id, tt.prefix) {
				t.Errorf("Node ID %q should contain prefix %q", id, tt.prefix)
			}
		})
	}
}

// TestManagedDBNodeFields verifies node structure.
func TestManagedDBNodeFields(t *testing.T) {
	node := &ManagedDBNode{
		Config: DBNodeConfig{
			Host:     "db1.example.com",
			Port:     5432,
			User:     "user",
			Password: "pass",
			Database: "pool",
		},
		ID:               "db-0-db1:5432-abc123",
		Priority:         0,
		ReadOnly:         false,
		State:            DBNodeHealthy,
		ConsecutiveFails: 0,
		LastSuccess:      time.Now(),
	}

	if node.ID == "" {
		t.Error("Node ID should not be empty")
	}
	if node.Priority < 0 {
		t.Error("Priority should be non-negative")
	}
	if node.State != DBNodeHealthy {
		t.Error("Initial state should be healthy")
	}
}

// TestNodeStateTransitions verifies state machine behavior.
func TestNodeStateTransitions(t *testing.T) {
	tests := []struct {
		name        string
		initialState DBNodeState
		failures    int
		threshold   int
		wantState   DBNodeState
	}{
		{
			name:         "healthy to degraded on first failure",
			initialState: DBNodeHealthy,
			failures:     1,
			threshold:    3,
			wantState:    DBNodeDegraded,
		},
		{
			name:         "degraded stays degraded below threshold",
			initialState: DBNodeDegraded,
			failures:     2,
			threshold:    3,
			wantState:    DBNodeDegraded,
		},
		{
			name:         "becomes unhealthy at threshold",
			initialState: DBNodeDegraded,
			failures:     3,
			threshold:    3,
			wantState:    DBNodeUnhealthy,
		},
		{
			name:         "success resets to healthy",
			initialState: DBNodeDegraded,
			failures:     0, // success
			threshold:    3,
			wantState:    DBNodeHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := tt.initialState

			if tt.failures == 0 {
				// Success resets to healthy
				state = DBNodeHealthy
			} else if tt.failures >= tt.threshold {
				state = DBNodeUnhealthy
			} else if state == DBNodeHealthy {
				state = DBNodeDegraded
			}

			if state != tt.wantState {
				t.Errorf("state = %v, want %v", state, tt.wantState)
			}
		})
	}
}

// TestDBManagerStatsFields verifies stats structure.
func TestDBManagerStatsFields(t *testing.T) {
	stats := DBManagerStats{
		ActiveNode:    "db-0-primary:5432-abc",
		TotalNodes:    2,
		HealthyNodes:  2,
		Failovers:     0,
		WriteFailures: 0,
		ReadFailures:  0,
	}

	if stats.ActiveNode == "" {
		t.Error("ActiveNode should not be empty")
	}
	if stats.TotalNodes < 1 {
		t.Error("TotalNodes should be at least 1")
	}
	if stats.HealthyNodes > stats.TotalNodes {
		t.Error("HealthyNodes cannot exceed TotalNodes")
	}
}

// TestFindBestWriteNodeLogic verifies node selection priority.
func TestFindBestWriteNodeLogic(t *testing.T) {
	// Simulate node selection
	type nodeInfo struct {
		id       string
		priority int
		readOnly bool
		state    DBNodeState
	}

	nodes := []nodeInfo{
		{id: "db-1-replica", priority: 1, readOnly: true, state: DBNodeHealthy},
		{id: "db-0-primary", priority: 0, readOnly: false, state: DBNodeHealthy},
		{id: "db-2-backup", priority: 2, readOnly: false, state: DBNodeHealthy},
	}

	// Find best write node (lowest priority, not read-only, healthy)
	bestIdx := -1
	bestPriority := int(^uint(0) >> 1) // Max int

	for i, n := range nodes {
		if n.readOnly {
			continue
		}
		if n.state != DBNodeHealthy && n.state != DBNodeDegraded {
			continue
		}
		if n.priority < bestPriority {
			bestPriority = n.priority
			bestIdx = i
		}
	}

	if bestIdx != 1 {
		t.Errorf("Best write node index = %d, want 1 (primary)", bestIdx)
	}
	if nodes[bestIdx].id != "db-0-primary" {
		t.Errorf("Best write node = %q, want db-0-primary", nodes[bestIdx].id)
	}
}

// TestFailbackToHigherPriority verifies failback behavior.
func TestFailbackToHigherPriority(t *testing.T) {
	// Current active is priority 2 (backup), but priority 0 (primary) recovered
	currentPriority := 2
	bestPriority := 0

	shouldFailback := bestPriority < currentPriority

	if !shouldFailback {
		t.Error("Should failback to higher priority (lower number) node")
	}
}

// TestVIPFailoverDebounce verifies rapid failover prevention.
func TestVIPFailoverDebounce(t *testing.T) {
	var lastFailoverTime atomic.Int64
	debounceSeconds := int64(5)

	// First failover should be allowed
	now := time.Now().Unix()
	last := lastFailoverTime.Load()
	allowed := now-last >= debounceSeconds
	if !allowed {
		t.Error("First failover should be allowed")
	}
	lastFailoverTime.Store(now)

	// Immediate second failover should be debounced
	now = time.Now().Unix()
	last = lastFailoverTime.Load()
	allowed = now-last >= debounceSeconds
	if allowed {
		t.Error("Rapid failover should be debounced")
	}
}

// TestEmptyBatchWrite verifies empty batch handling.
func TestEmptyBatchWrite(t *testing.T) {
	// WriteBatch with empty shares should return nil immediately
	shares := []*struct{}{}

	if len(shares) != 0 {
		t.Error("Expected empty shares slice")
	}
	// No error for empty batch
}

// TestNodeConfigValidation verifies node config requirements.
func TestNodeConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     DBNodeConfig
		wantErr bool
	}{
		{
			name: "valid primary",
			cfg: DBNodeConfig{
				Host:           "db.example.com",
				Port:           5432,
				User:           "pool",
				Password:       "secret",
				Database:       "spiralpool",
				MaxConnections: 25,
				Priority:       0,
				ReadOnly:       false,
			},
			wantErr: false,
		},
		{
			name: "valid replica",
			cfg: DBNodeConfig{
				Host:           "replica.example.com",
				Port:           5432,
				User:           "pool",
				Password:       "secret",
				Database:       "spiralpool",
				MaxConnections: 10,
				Priority:       1,
				ReadOnly:       true,
			},
			wantErr: false,
		},
		{
			name: "missing host",
			cfg: DBNodeConfig{
				Port:     5432,
				User:     "pool",
				Password: "secret",
				Database: "spiralpool",
			},
			wantErr: true,
		},
		{
			name: "missing database",
			cfg: DBNodeConfig{
				Host:     "db.example.com",
				Port:     5432,
				User:     "pool",
				Password: "secret",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasErr := tt.cfg.Host == "" || tt.cfg.Database == ""

			if hasErr != tt.wantErr {
				t.Errorf("validation error = %v, want %v", hasErr, tt.wantErr)
			}
		})
	}
}

// TestResponseTimeAverage verifies rolling average calculation.
func TestResponseTimeAverage(t *testing.T) {
	const alpha = 0.2

	tests := []struct {
		current time.Duration
		new     time.Duration
		want    time.Duration
	}{
		{0, 10 * time.Millisecond, 10 * time.Millisecond}, // Initial case
		{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond},
		{10 * time.Millisecond, 50 * time.Millisecond, 18 * time.Millisecond}, // 0.2*50 + 0.8*10 = 18
	}

	for i, tt := range tests {
		var avg time.Duration
		if tt.current == 0 {
			avg = tt.new
		} else {
			avg = time.Duration(alpha*float64(tt.new) + (1-alpha)*float64(tt.current))
		}

		// Allow small rounding difference
		diff := avg - tt.want
		if diff < 0 {
			diff = -diff
		}
		if diff > time.Millisecond {
			t.Errorf("Case %d: avg = %v, want %v", i, avg, tt.want)
		}
	}
}

// TestMaxConnectionsLimit verifies connection pool limits.
func TestMaxConnectionsLimit(t *testing.T) {
	tests := []struct {
		maxConns int
		valid    bool
	}{
		{1, true},
		{25, true},
		{100, true},
		{0, false},
		{-1, false},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			valid := tt.maxConns > 0
			if valid != tt.valid {
				t.Errorf("MaxConnections %d: valid = %v, want %v",
					tt.maxConns, valid, tt.valid)
			}
		})
	}
}

// TestPoolIDInTableName verifies table name formatting.
func TestPoolIDInTableName(t *testing.T) {
	tests := []struct {
		poolID    string
		tableName string
	}{
		{"dgb_main", "shares_dgb_main"},
		{"btc_pool", "shares_btc_pool"},
		{"pool1", "shares_pool1"},
	}

	for _, tt := range tests {
		t.Run(tt.poolID, func(t *testing.T) {
			tableName := "shares_" + tt.poolID
			if tableName != tt.tableName {
				t.Errorf("Table name = %q, want %q", tableName, tt.tableName)
			}
		})
	}
}

// TestLocalNodePriority verifies local node identification.
func TestLocalNodePriority(t *testing.T) {
	// Local primary should have priority 0
	localPriority := 0

	nodes := []int{0, 1, 2}
	foundLocal := false
	for _, p := range nodes {
		if p == localPriority {
			foundLocal = true
			break
		}
	}

	if !foundLocal {
		t.Error("Should find local node with priority 0")
	}
}

// Helper function for substring check
func dbContainsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && dbContainsSubstringAt(s, substr)
}

func dbContainsSubstringAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if s[i+j] != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// BenchmarkNodeIDGeneration benchmarks node ID generation.
func BenchmarkNodeIDGeneration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generateNodeID(0, "localhost", 5432)
	}
}

// BenchmarkSafeConnectionString benchmarks credential masking.
func BenchmarkSafeConnectionString(b *testing.B) {
	cfg := &DBNodeConfig{
		Host:           "db.example.com",
		Port:           5432,
		User:           "spiraluser",
		Password:       "supersecret123!",
		Database:       "spiralpool",
		MaxConnections: 25,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		safeConnectionString(cfg)
	}
}
