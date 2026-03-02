// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPoolStateString(t *testing.T) {
	tests := []struct {
		state    PoolState
		expected string
	}{
		{PoolStateUnknown, "unknown"},
		{PoolStateHealthy, "healthy"},
		{PoolStateDegraded, "degraded"},
		{PoolStateUnhealthy, "unhealthy"},
		{PoolStateOffline, "offline"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("PoolState.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFailoverManagerCreation(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "192.168.1.1",
		PrimaryPort: 3333,
		BackupPools: []BackupPoolConfig{
			{ID: "backup-1", Host: "192.168.1.2", Port: 3333, Priority: 1},
			{ID: "backup-2", Host: "192.168.1.3", Port: 3333, Priority: 2},
		},
	}

	fm := NewFailoverManager(cfg, logger)
	if fm == nil {
		t.Fatal("NewFailoverManager returned nil")
	}

	if len(fm.pools) != 2 {
		t.Errorf("Expected 2 backup pools, got %d", len(fm.pools))
	}

	// Verify priority ordering
	if fm.pools[0].ID != "backup-1" {
		t.Error("Pools should be sorted by priority")
	}
}

func TestFailoverManagerDefaults(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "localhost",
		PrimaryPort: 3333,
	}

	fm := NewFailoverManager(cfg, logger)

	// Default health check interval is 15 minutes for stable server environments
	// (servers rarely go down, frequent checks are wasteful)
	if fm.healthCheckInterval != 15*time.Minute {
		t.Errorf("Expected default healthCheckInterval 15m, got %v", fm.healthCheckInterval)
	}
	if fm.failoverThreshold != 3 {
		t.Errorf("Expected default failoverThreshold 3, got %d", fm.failoverThreshold)
	}
	if fm.recoveryThreshold != 5 {
		t.Errorf("Expected default recoveryThreshold 5, got %d", fm.recoveryThreshold)
	}
	if fm.probeTimeout != 5*time.Second {
		t.Errorf("Expected default probeTimeout 5s, got %v", fm.probeTimeout)
	}
}

func TestFailoverManagerGetActivePool(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "primary.local",
		PrimaryPort: 3333,
		BackupPools: []BackupPoolConfig{
			{ID: "backup-1", Host: "backup.local", Port: 3333, Priority: 1},
		},
	}

	fm := NewFailoverManager(cfg, logger)

	// Should return primary initially
	host, port := fm.GetActivePool()
	if host != "primary.local" || port != 3333 {
		t.Errorf("Expected primary.local:3333, got %s:%d", host, port)
	}

	// Simulate failover
	fm.mu.Lock()
	fm.isPrimaryActive = false
	fm.activePool = fm.pools[0]
	fm.mu.Unlock()

	host, port = fm.GetActivePool()
	if host != "backup.local" || port != 3333 {
		t.Errorf("Expected backup.local:3333, got %s:%d", host, port)
	}
}

// mockStratumServerWithControl creates a mock stratum server that can be controlled
type mockServer struct {
	listener net.Listener
	healthy  bool
	mu       sync.Mutex
}

func newMockServer(t *testing.T) *mockServer {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create mock server: %v", err)
	}

	ms := &mockServer{
		listener: listener,
		healthy:  true,
	}

	go ms.serve()
	return ms
}

func (ms *mockServer) serve() {
	for {
		conn, err := ms.listener.Accept()
		if err != nil {
			return
		}

		go func(c net.Conn) {
			defer c.Close()

			ms.mu.Lock()
			healthy := ms.healthy
			ms.mu.Unlock()

			if !healthy {
				return // Close connection without response
			}

			buf := make([]byte, 1024)
			n, err := c.Read(buf)
			if err != nil {
				return
			}

			var req map[string]interface{}
			if err := json.Unmarshal(buf[:n], &req); err != nil {
				return
			}

			response := map[string]interface{}{
				"id":     req["id"],
				"result": []interface{}{[]interface{}{}, ""},
				"error":  nil,
			}

			data, _ := json.Marshal(response)
			c.Write(append(data, '\n'))
		}(conn)
	}
}

func (ms *mockServer) Addr() (string, int) {
	addr := ms.listener.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func (ms *mockServer) SetHealthy(healthy bool) {
	ms.mu.Lock()
	ms.healthy = healthy
	ms.mu.Unlock()
}

func (ms *mockServer) Close() {
	ms.listener.Close()
}

func TestFailoverManagerCheckPool(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Start mock server
	server := newMockServer(t)
	defer server.Close()

	host, port := server.Addr()

	cfg := FailoverConfig{
		PrimaryHost:  host,
		PrimaryPort:  port,
		ProbeTimeout: 2 * time.Second,
	}

	fm := NewFailoverManager(cfg, logger)

	// Test healthy server
	if !fm.checkPool(host, port) {
		t.Error("checkPool should return true for healthy server")
	}

	// Make server unhealthy
	server.SetHealthy(false)
	time.Sleep(100 * time.Millisecond)

	// Test unhealthy server
	if fm.checkPool(host, port) {
		t.Error("checkPool should return false for unhealthy server")
	}
}

func TestFailoverManagerGetPoolStatus(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "primary.local",
		PrimaryPort: 3333,
		BackupPools: []BackupPoolConfig{
			{ID: "backup-1", Host: "backup1.local", Port: 3333, Priority: 1},
			{ID: "backup-2", Host: "backup2.local", Port: 3333, Priority: 2},
		},
	}

	fm := NewFailoverManager(cfg, logger)

	status := fm.GetPoolStatus()

	if status.PrimaryHost != "primary.local" {
		t.Errorf("Expected primary host primary.local, got %s", status.PrimaryHost)
	}
	if !status.IsPrimaryActive {
		t.Error("Primary should be active initially")
	}
	if len(status.BackupPools) != 2 {
		t.Errorf("Expected 2 backup pools, got %d", len(status.BackupPools))
	}
	if status.FailoverCount != 0 {
		t.Errorf("Expected 0 failovers, got %d", status.FailoverCount)
	}
}

func TestFailoverManagerAddDiscoveredPool(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "primary.local",
		PrimaryPort: 3333,
		BackupPools: []BackupPoolConfig{
			{ID: "backup-1", Host: "backup1.local", Port: 3333, Priority: 1},
		},
	}

	fm := NewFailoverManager(cfg, logger)

	// Add discovered pool
	discovered := &DiscoveredPool{
		Host:      "discovered.local",
		Port:      3333,
		IsHealthy: true,
	}

	if err := fm.AddDiscoveredPool(discovered); err != nil {
		t.Fatalf("AddDiscoveredPool failed: %v", err)
	}

	if len(fm.pools) != 2 {
		t.Errorf("Expected 2 pools after adding discovered, got %d", len(fm.pools))
	}

	// Adding same pool again should not duplicate
	_ = fm.AddDiscoveredPool(discovered) // Ignore error, just checking for duplicates
	if len(fm.pools) != 2 {
		t.Errorf("Should not add duplicate pool, got %d pools", len(fm.pools))
	}
}

func TestFailoverManagerMaxPoolsLimit(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "primary.local",
		PrimaryPort: 3333,
	}

	fm := NewFailoverManager(cfg, logger)

	// Add pools up to the limit
	for i := 0; i < MaxBackupPools; i++ {
		pool := &DiscoveredPool{
			Host:      fmt.Sprintf("192.168.1.%d", i%256),
			Port:      3333 + i/256,
			IsHealthy: true,
		}
		err := fm.AddDiscoveredPool(pool)
		if err != nil {
			t.Fatalf("Failed to add pool %d: %v", i, err)
		}
	}

	if len(fm.pools) != MaxBackupPools {
		t.Errorf("Expected %d pools, got %d", MaxBackupPools, len(fm.pools))
	}

	// Try to add one more - should fail
	extraPool := &DiscoveredPool{
		Host:      "extra.local",
		Port:      9999,
		IsHealthy: true,
	}
	err := fm.AddDiscoveredPool(extraPool)
	if err != ErrMaxPoolsReached {
		t.Errorf("Expected ErrMaxPoolsReached, got %v", err)
	}

	// Pool count should still be at max
	if len(fm.pools) != MaxBackupPools {
		t.Errorf("Pool count changed after rejected add: %d", len(fm.pools))
	}
}

func TestFailoverManagerForceFailover(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "primary.local",
		PrimaryPort: 3333,
		BackupPools: []BackupPoolConfig{
			{ID: "backup-1", Host: "backup1.local", Port: 3333, Priority: 1},
		},
	}

	fm := NewFailoverManager(cfg, logger)

	// Force failover to backup
	err := fm.ForceFailover("backup-1")
	if err != nil {
		t.Fatalf("ForceFailover failed: %v", err)
	}

	host, _ := fm.GetActivePool()
	if host != "backup1.local" {
		t.Errorf("Expected backup1.local after failover, got %s", host)
	}

	// Force back to primary
	err = fm.ForceFailover("primary")
	if err != nil {
		t.Fatalf("ForceFailover to primary failed: %v", err)
	}

	host, _ = fm.GetActivePool()
	if host != "primary.local" {
		t.Errorf("Expected primary.local after recovery, got %s", host)
	}
}

func TestFailoverManagerForceFailoverNotFound(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost: "primary.local",
		PrimaryPort: 3333,
	}

	fm := NewFailoverManager(cfg, logger)

	err := fm.ForceFailover("nonexistent")
	if err == nil {
		t.Error("ForceFailover should fail for nonexistent pool")
	}
}

func TestFailoverManagerStartStop(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := FailoverConfig{
		PrimaryHost:         "127.0.0.1",
		PrimaryPort:         59999, // Unlikely to be in use
		HealthCheckInterval: 50 * time.Millisecond,
		ProbeTimeout:        50 * time.Millisecond,
	}

	fm := NewFailoverManager(cfg, logger)

	ctx := context.Background()
	err := fm.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Let it run for a bit
	time.Sleep(150 * time.Millisecond)

	err = fm.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestFailoverManagerCallback(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	server := newMockServer(t)
	defer server.Close()
	host, port := server.Addr()

	cfg := FailoverConfig{
		PrimaryHost: host,
		PrimaryPort: port,
		BackupPools: []BackupPoolConfig{
			{ID: "backup-1", Host: host, Port: port, Priority: 1},
		},
		HealthCheckInterval: 50 * time.Millisecond,
		FailoverThreshold:   1,
		ProbeTimeout:        100 * time.Millisecond,
	}

	fm := NewFailoverManager(cfg, logger)

	var mu sync.Mutex
	var events []FailoverEvent

	fm.SetFailoverHandler(func(event FailoverEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	// Force a failover
	fm.ForceFailover("backup-1")

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if len(events) != 1 {
		t.Errorf("Expected 1 failover event, got %d", len(events))
	}
	mu.Unlock()
}

func TestFailoverEventJSON(t *testing.T) {
	event := FailoverEvent{
		FromPool:   "primary",
		ToPool:     "backup-1",
		Reason:     "primary_down",
		OccurredAt: time.Now(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	t.Logf("JSON: %s", string(data))

	var decoded FailoverEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if decoded.FromPool != event.FromPool {
		t.Errorf("FromPool mismatch: %s != %s", decoded.FromPool, event.FromPool)
	}
	if decoded.ToPool != event.ToPool {
		t.Errorf("ToPool mismatch: %s != %s", decoded.ToPool, event.ToPool)
	}
}

func TestPoolStatusJSON(t *testing.T) {
	status := PoolStatus{
		PrimaryHost:     "primary.local",
		PrimaryPort:     3333,
		IsPrimaryActive: true,
		BackupPools: []BackupPoolStatus{
			{
				ID:           "backup-1",
				Host:         "backup.local",
				Port:         3333,
				State:        "healthy",
				ResponseTime: 50 * time.Millisecond,
				LastCheck:    time.Now(),
			},
		},
		FailoverCount: 0,
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	t.Logf("JSON: %s", string(data))
}

func BenchmarkCheckPool(b *testing.B) {
	logger, _ := zap.NewProduction()

	server := newMockServer(&testing.T{})
	defer server.Close()
	host, port := server.Addr()

	cfg := FailoverConfig{
		PrimaryHost:  host,
		PrimaryPort:  port,
		ProbeTimeout: 100 * time.Millisecond,
	}

	fm := NewFailoverManager(cfg, logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fm.checkPool(host, port)
	}
}
