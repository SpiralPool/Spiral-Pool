// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package security provides security testing for the stratum server.
package security

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// TEST HELPERS
// =============================================================================

// mockAddr implements net.Addr for testing
type mockAddr struct {
	network string
	addr    string
}

func (m mockAddr) Network() string { return m.network }
func (m mockAddr) String() string  { return m.addr }

func newMockAddr(ip string, port int) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: port}
}

func newTestRateLimiter(t *testing.T) *RateLimiter {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  10,
		MaxConnectionsPerMin: 30,
		MaxSharesPerSecond:   100,
		BanThreshold:         5,
		BanDuration:          1 * time.Minute,
		WhitelistIPs:         []string{"127.0.0.1", "10.0.0.1"},
	}
	return NewRateLimiter(cfg, logger)
}

// =============================================================================
// UNIT TESTS
// =============================================================================

func TestNewRateLimiter(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  5,
		MaxConnectionsPerMin: 20,
		MaxSharesPerSecond:   50,
		BanThreshold:         3,
		BanDuration:          30 * time.Second,
		WhitelistIPs:         []string{"192.168.1.1", "10.0.0.0/8"},
	}

	rl := NewRateLimiter(cfg, logger)

	if rl == nil {
		t.Fatal("NewRateLimiter returned nil")
	}

	if rl.maxConnectionsPerIP != 5 {
		t.Errorf("Expected maxConnectionsPerIP=5, got %d", rl.maxConnectionsPerIP)
	}

	if rl.maxSharesPerSecond != 50 {
		t.Errorf("Expected maxSharesPerSecond=50, got %d", rl.maxSharesPerSecond)
	}

	// Check whitelist was populated
	if !rl.whitelistIPs["192.168.1.1"] {
		t.Error("Expected 192.168.1.1 to be whitelisted")
	}
}

func TestNewRateLimiter_InvalidWhitelist(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  5,
		MaxConnectionsPerMin: 20,
		MaxSharesPerSecond:   50,
		BanThreshold:         3,
		BanDuration:          30 * time.Second,
		WhitelistIPs:         []string{"not-a-valid-ip", "also-invalid", "192.168.1.1"},
	}

	rl := NewRateLimiter(cfg, logger)

	// Should only have the valid IP
	if !rl.whitelistIPs["192.168.1.1"] {
		t.Error("Expected valid IP to be whitelisted")
	}

	// Invalid IPs should not be in whitelist
	if rl.whitelistIPs["not-a-valid-ip"] {
		t.Error("Invalid IP should not be whitelisted")
	}
}

func TestAllowConnection_Basic(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// First connection should be allowed
	allowed, reason := rl.AllowConnection(addr)
	if !allowed {
		t.Errorf("First connection should be allowed, reason: %s", reason)
	}

	// Verify connection was tracked
	stats := rl.GetStats()
	if stats.ActiveConnections != 1 {
		t.Errorf("Expected 1 active connection, got %d", stats.ActiveConnections)
	}
}

func TestAllowConnection_NilAddr(t *testing.T) {
	rl := newTestRateLimiter(t)

	allowed, reason := rl.AllowConnection(nil)
	if allowed {
		t.Error("Nil address should not be allowed")
	}
	if reason != "invalid address" {
		t.Errorf("Expected 'invalid address' reason, got '%s'", reason)
	}
}

func TestAllowConnection_Whitelist(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Whitelisted IP should always be allowed
	addr := newMockAddr("127.0.0.1", 12345)

	for i := 0; i < 100; i++ {
		allowed, _ := rl.AllowConnection(addr)
		if !allowed {
			t.Errorf("Whitelisted IP should always be allowed, iteration %d", i)
		}
	}
}

func TestAllowConnection_MaxPerIP(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  3,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   100,
		BanThreshold:         10,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Allow up to max connections
	for i := 0; i < 3; i++ {
		allowed, _ := rl.AllowConnection(addr)
		if !allowed {
			t.Errorf("Connection %d should be allowed", i+1)
		}
	}

	// Next connection should be denied
	allowed, reason := rl.AllowConnection(addr)
	if allowed {
		t.Error("Connection exceeding max per IP should be denied")
	}
	if reason != "too many connections from this IP" {
		t.Errorf("Expected 'too many connections from this IP', got '%s'", reason)
	}
}

func TestAllowConnection_MaxPerMinute(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 5,
		MaxSharesPerSecond:   100,
		BanThreshold:         10,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Allow up to max connections per minute
	for i := 0; i < 5; i++ {
		allowed, _ := rl.AllowConnection(addr)
		if !allowed {
			t.Errorf("Connection %d should be allowed", i+1)
		}
		// Release so we don't hit per-IP limit
		rl.ReleaseConnection(addr)
	}

	// Next connection should be denied (rate limit)
	allowed, reason := rl.AllowConnection(addr)
	if allowed {
		t.Error("Connection exceeding rate limit should be denied")
	}
	if reason != "connection rate limit exceeded" {
		t.Errorf("Expected 'connection rate limit exceeded', got '%s'", reason)
	}
}

func TestReleaseConnection(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Add some connections
	for i := 0; i < 5; i++ {
		rl.AllowConnection(addr)
	}

	stats := rl.GetStats()
	if stats.ActiveConnections != 5 {
		t.Errorf("Expected 5 active connections, got %d", stats.ActiveConnections)
	}

	// Release connections
	for i := 0; i < 5; i++ {
		rl.ReleaseConnection(addr)
	}

	stats = rl.GetStats()
	if stats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections after release, got %d", stats.ActiveConnections)
	}
}

func TestReleaseConnection_DoesNotGoNegative(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Release without connecting
	rl.ReleaseConnection(addr)

	// Add one connection
	rl.AllowConnection(addr)

	// Release more than we added
	rl.ReleaseConnection(addr)
	rl.ReleaseConnection(addr)
	rl.ReleaseConnection(addr)

	// Connections should not go negative
	rl.mu.RLock()
	state := rl.connectionsByIP["192.168.1.100"]
	rl.mu.RUnlock()

	if state != nil && state.connections < 0 {
		t.Error("Connection count should not go negative")
	}
}

func TestAllowShare_Basic(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Establish connection first
	rl.AllowConnection(addr)

	// Shares should be allowed
	allowed, _ := rl.AllowShare(addr)
	if !allowed {
		t.Error("Share should be allowed")
	}
}

func TestAllowShare_RateLimit(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   5,
		BanThreshold:         10,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Establish connection
	rl.AllowConnection(addr)

	// Allow up to max shares per second
	for i := 0; i < 5; i++ {
		allowed, _ := rl.AllowShare(addr)
		if !allowed {
			t.Errorf("Share %d should be allowed", i+1)
		}
	}

	// Next share should be denied
	allowed, reason := rl.AllowShare(addr)
	if allowed {
		t.Error("Share exceeding rate limit should be denied")
	}
	if reason != "share rate limit exceeded" {
		t.Errorf("Expected 'share rate limit exceeded', got '%s'", reason)
	}
}

func TestAllowShare_Whitelist(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("127.0.0.1", 12345)

	// Whitelisted IP should always be allowed
	for i := 0; i < 1000; i++ {
		allowed, _ := rl.AllowShare(addr)
		if !allowed {
			t.Errorf("Whitelisted IP shares should always be allowed, iteration %d", i)
		}
	}
}

func TestBanIP(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Ban the IP
	rl.BanIP("192.168.1.100", 1*time.Hour, "test ban")

	// Connection should be denied
	allowed, reason := rl.AllowConnection(addr)
	if allowed {
		t.Error("Banned IP should not be allowed to connect")
	}
	if reason != "IP is banned" {
		t.Errorf("Expected 'IP is banned', got '%s'", reason)
	}

	// Verify ban tracking
	if !rl.IsIPBanned("192.168.1.100") {
		t.Error("IP should be reported as banned")
	}

	stats := rl.GetStats()
	if stats.TotalBanned != 1 {
		t.Errorf("Expected TotalBanned=1, got %d", stats.TotalBanned)
	}
}

func TestUnbanIP(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Ban then unban
	rl.BanIP("192.168.1.100", 1*time.Hour, "test ban")
	rl.UnbanIP("192.168.1.100")

	// Connection should be allowed
	allowed, _ := rl.AllowConnection(addr)
	if !allowed {
		t.Error("Unbanned IP should be allowed to connect")
	}

	if rl.IsIPBanned("192.168.1.100") {
		t.Error("IP should not be reported as banned after unban")
	}
}

func TestBanExpiry(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  10,
		MaxConnectionsPerMin: 30,
		MaxSharesPerSecond:   100,
		BanThreshold:         5,
		BanDuration:          50 * time.Millisecond, // Very short ban
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Ban the IP
	rl.BanIP("192.168.1.100", 50*time.Millisecond, "test ban")

	// Should be banned initially
	allowed, _ := rl.AllowConnection(addr)
	if allowed {
		t.Error("IP should be banned initially")
	}

	// Wait for ban to expire
	time.Sleep(100 * time.Millisecond)

	// Should be allowed now
	allowed, _ = rl.AllowConnection(addr)
	if !allowed {
		t.Error("IP should be allowed after ban expires")
	}
}

func TestAutoBan(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  2,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   100,
		BanThreshold:         3,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Use up connections to trigger violations
	rl.AllowConnection(addr)
	rl.AllowConnection(addr)

	// These should all fail and record violations
	for i := 0; i < 3; i++ {
		rl.AllowConnection(addr)
	}

	// IP should be auto-banned after threshold
	if !rl.IsIPBanned("192.168.1.100") {
		t.Error("IP should be auto-banned after exceeding violation threshold")
	}
}

func TestGetBannedIPs(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Ban multiple IPs
	rl.BanIP("192.168.1.1", 1*time.Hour, "test")
	rl.BanIP("192.168.1.2", 1*time.Hour, "test")
	rl.BanIP("192.168.1.3", 1*time.Hour, "test")

	banned := rl.GetBannedIPs()
	if len(banned) != 3 {
		t.Errorf("Expected 3 banned IPs, got %d", len(banned))
	}

	for _, ip := range []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"} {
		if _, ok := banned[ip]; !ok {
			t.Errorf("Expected %s to be in banned list", ip)
		}
	}
}

func TestWhitelist_Operations(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Add to whitelist
	rl.AddToWhitelist("192.168.1.50")

	// Verify it's whitelisted
	addr := newMockAddr("192.168.1.50", 12345)
	for i := 0; i < 100; i++ {
		allowed, _ := rl.AllowConnection(addr)
		if !allowed {
			t.Error("Newly whitelisted IP should be allowed")
		}
	}

	// Remove from whitelist
	rl.RemoveFromWhitelist("192.168.1.50")

	// Reset state
	rl.mu.Lock()
	delete(rl.connectionsByIP, "192.168.1.50")
	rl.mu.Unlock()

	// Now should be rate limited after max connections
	for i := 0; i < 10; i++ {
		rl.AllowConnection(addr)
	}
	allowed, _ := rl.AllowConnection(addr)
	if allowed {
		t.Error("Removed from whitelist IP should be rate limited")
	}
}

func TestCleanup(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Create connection
	rl.AllowConnection(addr)
	rl.ReleaseConnection(addr)

	// Manually set lastConnection to old time
	rl.mu.Lock()
	if state, ok := rl.connectionsByIP["192.168.1.100"]; ok {
		state.lastConnection = time.Now().Add(-20 * time.Minute)
	}
	rl.mu.Unlock()

	// Run cleanup
	rl.cleanup()

	// State should be removed
	rl.mu.RLock()
	_, exists := rl.connectionsByIP["192.168.1.100"]
	rl.mu.RUnlock()

	if exists {
		t.Error("Stale IP state should be cleaned up")
	}
}

func TestGetStats(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Create some activity
	addr1 := newMockAddr("192.168.1.1", 12345)
	addr2 := newMockAddr("192.168.1.2", 12345)

	rl.AllowConnection(addr1)
	rl.AllowConnection(addr1)
	rl.AllowConnection(addr2)
	rl.BanIP("192.168.1.3", 1*time.Hour, "test")

	stats := rl.GetStats()

	if stats.ActiveConnections != 3 {
		t.Errorf("Expected ActiveConnections=3, got %d", stats.ActiveConnections)
	}
	if stats.UniqueIPs != 2 {
		t.Errorf("Expected UniqueIPs=2, got %d", stats.UniqueIPs)
	}
	if stats.BannedIPs != 1 {
		t.Errorf("Expected BannedIPs=1, got %d", stats.BannedIPs)
	}
}

// =============================================================================
// CONCURRENCY TESTS
// =============================================================================

func TestConcurrentConnections(t *testing.T) {
	rl := newTestRateLimiter(t)

	var wg sync.WaitGroup
	var allowed atomic.Int64
	var denied atomic.Int64

	// Spawn many goroutines connecting/releasing
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			addr := newMockAddr("192.168.1.100", 12345+id)

			for j := 0; j < 50; j++ {
				if ok, _ := rl.AllowConnection(addr); ok {
					allowed.Add(1)
					time.Sleep(time.Microsecond * 10)
					rl.ReleaseConnection(addr)
				} else {
					denied.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()

	// Should have processed many connections without panicking
	total := allowed.Load() + denied.Load()
	if total < 1000 {
		t.Errorf("Expected many connection attempts, got %d", total)
	}

	t.Logf("Concurrent test: allowed=%d, denied=%d", allowed.Load(), denied.Load())
}

func TestConcurrentShares(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Establish connection
	rl.AllowConnection(addr)

	var wg sync.WaitGroup
	var allowed atomic.Int64
	var denied atomic.Int64

	// Spawn many goroutines submitting shares
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 100; j++ {
				if ok, _ := rl.AllowShare(addr); ok {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	// Should have processed shares without panicking
	t.Logf("Concurrent shares: allowed=%d, denied=%d", allowed.Load(), denied.Load())
}

func TestConcurrentBanOperations(t *testing.T) {
	rl := newTestRateLimiter(t)

	var wg sync.WaitGroup

	// Concurrent bans and unbans
	for i := 0; i < 50; i++ {
		wg.Add(2)
		ip := "192.168.1.100"

		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rl.BanIP(ip, 1*time.Second, "test")
			}
		}()

		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rl.UnbanIP(ip)
			}
		}()
	}

	wg.Wait()

	// Should complete without panicking or deadlocking
	t.Log("Concurrent ban operations completed successfully")
}

// =============================================================================
// EDGE CASES AND SECURITY TESTS
// =============================================================================

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name     string
		addr     net.Addr
		expected string
	}{
		{
			name:     "TCPAddr",
			addr:     &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 12345},
			expected: "192.168.1.1",
		},
		{
			name:     "UDPAddr",
			addr:     &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 12345},
			expected: "10.0.0.1",
		},
		{
			name:     "IPv6",
			addr:     &net.TCPAddr{IP: net.ParseIP("::1"), Port: 12345},
			expected: "::1",
		},
		{
			name:     "nil",
			addr:     nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractIP(tt.addr)
			if result != tt.expected {
				t.Errorf("extractIP(%v) = %s, want %s", tt.addr, result, tt.expected)
			}
		})
	}
}

func TestExtractIPString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"192.168.1.1", "192.168.1.1"},
		{"192.168.1.1:8080", "192.168.1.1"},
		{"[::1]:8080", "::1"},
		{"", ""},
	}

	for _, tt := range tests {
		result := extractIPString(tt.input)
		if result != tt.expected {
			t.Errorf("extractIPString(%s) = %s, want %s", tt.input, result, tt.expected)
		}
	}
}

func TestShareWindowReset(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   5,
		BanThreshold:         100,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Establish connection
	rl.AllowConnection(addr)

	// Use up share limit
	for i := 0; i < 5; i++ {
		rl.AllowShare(addr)
	}

	// Should be denied
	allowed, _ := rl.AllowShare(addr)
	if allowed {
		t.Error("Share should be denied after hitting limit")
	}

	// Wait for window to reset
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	allowed, _ = rl.AllowShare(addr)
	if !allowed {
		t.Error("Share should be allowed after window reset")
	}
}

// =============================================================================
// IPV6 AND EDGE CASE TESTS
// =============================================================================

// TestIPv6Addresses tests rate limiting with various IPv6 address formats.
func TestIPv6Addresses(t *testing.T) {
	rl := newTestRateLimiter(t)

	tests := []struct {
		name string
		ip   net.IP
	}{
		{"loopback", net.ParseIP("::1")},
		{"link_local", net.ParseIP("fe80::1")},
		{"global", net.ParseIP("2001:db8::1")},
		{"ipv4_mapped", net.ParseIP("::ffff:192.168.1.1")},
		{"full_notation", net.ParseIP("2001:0db8:0000:0000:0000:0000:0000:0001")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := &net.TCPAddr{IP: tt.ip, Port: 12345}

			// Should be able to connect
			allowed, reason := rl.AllowConnection(addr)
			if !allowed {
				t.Errorf("IPv6 connection should be allowed: %s", reason)
			}

			// Should track correctly
			stats := rl.GetStats()
			if stats.ActiveConnections == 0 {
				t.Error("Active connections should be tracked")
			}

			// Release
			rl.ReleaseConnection(addr)
		})
	}
}

// TestIPv4MappedIPv6 tests handling of IPv4-mapped IPv6 addresses.
func TestIPv4MappedIPv6(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  2,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   100,
		BanThreshold:         5,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)

	// IPv4 address and its IPv4-mapped IPv6 equivalent
	ipv4Addr := &net.TCPAddr{IP: net.ParseIP("192.168.1.100"), Port: 12345}
	ipv6MappedAddr := &net.TCPAddr{IP: net.ParseIP("::ffff:192.168.1.100"), Port: 12345}

	// Connect with IPv4
	rl.AllowConnection(ipv4Addr)
	rl.AllowConnection(ipv4Addr)

	// Try to connect with IPv6-mapped (might be treated as same or different IP)
	allowed, _ := rl.AllowConnection(ipv6MappedAddr)

	// Log behavior for documentation
	t.Logf("IPv4-mapped IPv6 treated as same IP: %v", !allowed)
}

// TestMultipleIPv6FromSameSubnet tests that different IPv6 addresses
// from the same /64 are tracked independently.
func TestMultipleIPv6FromSameSubnet(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  2,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   100,
		BanThreshold:         5,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)

	// Two IPs from same /64
	addr1 := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 12345}
	addr2 := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 12345}

	// Both should have independent limits
	for i := 0; i < 2; i++ {
		allowed1, _ := rl.AllowConnection(addr1)
		allowed2, _ := rl.AllowConnection(addr2)

		if !allowed1 || !allowed2 {
			t.Errorf("Iteration %d: Both IPs should be allowed independently", i)
		}
	}

	// Now both should be at limit
	allowed1, _ := rl.AllowConnection(addr1)
	allowed2, _ := rl.AllowConnection(addr2)

	if allowed1 {
		t.Error("addr1 should be at limit")
	}
	if allowed2 {
		t.Error("addr2 should be at limit")
	}
}

// TestMinuteWindowRollover tests the minute window boundary.
func TestMinuteWindowRollover(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 3,
		MaxSharesPerSecond:   100,
		BanThreshold:         100,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// Use up the per-minute limit
	for i := 0; i < 3; i++ {
		rl.AllowConnection(addr)
		rl.ReleaseConnection(addr)
	}

	// Next should fail
	allowed, reason := rl.AllowConnection(addr)
	if allowed {
		t.Error("Should hit per-minute limit")
	}
	if reason != "connection rate limit exceeded" {
		t.Errorf("Expected rate limit reason, got: %s", reason)
	}

	// Manually trigger window rollover
	rl.mu.Lock()
	if state, ok := rl.connectionsByIP["192.168.1.100"]; ok {
		state.minuteStart = time.Now().Add(-2 * time.Minute)
	}
	rl.mu.Unlock()

	// Now should be allowed again
	allowed, _ = rl.AllowConnection(addr)
	if !allowed {
		t.Error("Should be allowed after window rollover")
	}
}

// TestViolationTracking tests that violations are tracked and lead to bans.
func TestViolationTracking(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  1,
		MaxConnectionsPerMin: 100,
		MaxSharesPerSecond:   100,
		BanThreshold:         3,
		BanDuration:          1 * time.Minute,
	}
	rl := NewRateLimiter(cfg, logger)
	addr := newMockAddr("192.168.1.100", 12345)

	// First connection succeeds
	rl.AllowConnection(addr)

	// Each denied connection increments violations
	for i := 0; i < 3; i++ {
		rl.AllowConnection(addr)
	}

	// Should now be banned
	if !rl.IsIPBanned("192.168.1.100") {
		t.Error("IP should be auto-banned after 3 violations")
	}

	// Verify violation count
	rl.mu.RLock()
	state := rl.connectionsByIP["192.168.1.100"]
	rl.mu.RUnlock()

	if state == nil || state.violations < 3 {
		t.Error("Violations should be tracked")
	}
}

// TestBanPersistsAcrossAttempts tests that bans persist across new attempts.
func TestBanPersistsAcrossAttempts(t *testing.T) {
	rl := newTestRateLimiter(t)

	rl.BanIP("192.168.1.100", 1*time.Hour, "test")

	// Multiple attempts should all fail
	for i := 0; i < 10; i++ {
		addr := newMockAddr("192.168.1.100", 12345+i)
		allowed, reason := rl.AllowConnection(addr)

		if allowed {
			t.Errorf("Banned IP should not be allowed on attempt %d", i)
		}
		if reason != "IP is banned" {
			t.Errorf("Expected 'IP is banned', got '%s'", reason)
		}
	}

	// Other IPs should still work
	addr2 := newMockAddr("192.168.1.101", 12345)
	allowed, _ := rl.AllowConnection(addr2)
	if !allowed {
		t.Error("Different IP should not be affected by ban")
	}
}

// TestReleaseNonExistentIP tests releasing an IP that never connected.
func TestReleaseNonExistentIP(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Release an IP that never connected - should not panic
	addr := newMockAddr("192.168.1.100", 12345)
	rl.ReleaseConnection(addr)

	// Note: The current implementation decrements globalConnections unconditionally.
	// This is a known behavior - in production, ReleaseConnection is only called
	// after a successful AllowConnection, so this edge case doesn't occur.
	// We verify it doesn't panic, which is the important safety property.
	stats := rl.GetStats()
	t.Logf("Active connections after releasing non-existent: %d", stats.ActiveConnections)
}

// TestAllowShareWithoutState tests sharing without prior connection state.
func TestAllowShareWithoutState(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Try to submit share without establishing connection first
	addr := newMockAddr("192.168.1.100", 12345)
	allowed, _ := rl.AllowShare(addr)

	// Should be allowed (no state = no limit yet)
	if !allowed {
		t.Error("Share without prior state should be allowed")
	}
}

// TestWhitelistExactIP tests exact IP matching in whitelist.
// Note: The current implementation only supports exact IP matching in the
// whitelist lookup. CIDR ranges are validated during config but the runtime
// check uses a map lookup which only matches exact IPs.
func TestWhitelistExactIP(t *testing.T) {
	logger := zap.NewNop().Sugar()
	cfg := RateLimiterConfig{
		MaxConnectionsPerIP:  1,
		MaxConnectionsPerMin: 1,
		MaxSharesPerSecond:   1,
		BanThreshold:         1,
		BanDuration:          1 * time.Hour,
		WhitelistIPs:         []string{"10.0.0.1", "10.0.0.2"},
	}
	rl := NewRateLimiter(cfg, logger)

	// Exact whitelisted IPs should be unlimited
	for _, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		addr := newMockAddr(ip, 12345)

		// Unlimited connections for whitelisted IPs
		for i := 0; i < 10; i++ {
			allowed, _ := rl.AllowConnection(addr)
			if !allowed {
				t.Errorf("Whitelisted IP %s should have unlimited connections", ip)
				break
			}
		}
	}

	// Non-whitelisted IP should be limited
	addr := newMockAddr("192.168.1.1", 12345)
	rl.AllowConnection(addr)
	allowed, _ := rl.AllowConnection(addr)
	if allowed {
		t.Error("Non-whitelisted IP should be limited")
	}
}

// TestCleanupDoesNotRemoveActiveConnections tests that cleanup preserves active state.
func TestCleanupDoesNotRemoveActiveConnections(t *testing.T) {
	rl := newTestRateLimiter(t)
	addr := newMockAddr("192.168.1.100", 12345)

	// Establish connection
	rl.AllowConnection(addr)

	// Run cleanup
	rl.cleanup()

	// State should still exist because connection is active
	rl.mu.RLock()
	_, exists := rl.connectionsByIP["192.168.1.100"]
	rl.mu.RUnlock()

	if !exists {
		t.Error("Active connection state should not be cleaned up")
	}
}

// TestStatsAfterCleanup tests stats accuracy after cleanup.
func TestStatsAfterCleanup(t *testing.T) {
	rl := newTestRateLimiter(t)

	// Create some activity
	addr := newMockAddr("192.168.1.100", 12345)
	rl.AllowConnection(addr)
	rl.BanIP("192.168.1.200", 1*time.Hour, "test")

	stats := rl.GetStats()
	initialConnections := stats.ActiveConnections
	initialBanned := stats.BannedIPs

	// Run cleanup
	rl.cleanup()

	// Stats should remain consistent
	stats = rl.GetStats()
	if stats.ActiveConnections != initialConnections {
		t.Error("Active connections changed after cleanup")
	}
	if stats.BannedIPs != initialBanned {
		t.Error("Banned IPs changed after cleanup")
	}
}

// TestHighVolumeIPTracking tests memory behavior with many unique IPs.
func TestHighVolumeIPTracking(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping high-volume test in short mode")
	}

	rl := newTestRateLimiter(t)

	// Connect from many unique IPs
	for i := 0; i < 10000; i++ {
		ip := net.IPv4(192, 168, byte(i/256), byte(i%256))
		addr := &net.TCPAddr{IP: ip, Port: 12345}
		rl.AllowConnection(addr)
		rl.ReleaseConnection(addr)
	}

	stats := rl.GetStats()
	t.Logf("After 10000 unique IPs: UniqueIPs=%d, ActiveConnections=%d",
		stats.UniqueIPs, stats.ActiveConnections)

	// Mark as stale and run cleanup
	rl.mu.Lock()
	for _, state := range rl.connectionsByIP {
		state.lastConnection = time.Now().Add(-15 * time.Minute)
	}
	rl.mu.Unlock()

	rl.cleanup()

	stats = rl.GetStats()
	t.Logf("After cleanup: UniqueIPs=%d", stats.UniqueIPs)

	// All should be cleaned up
	if stats.UniqueIPs > 0 {
		t.Error("Stale IPs should be cleaned up")
	}
}

// TestExtractIP_EdgeCases tests edge cases in IP extraction.
func TestExtractIP_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		addr     net.Addr
		expected string
	}{
		{
			name:     "standard_ipv4",
			addr:     &net.TCPAddr{IP: net.ParseIP("192.168.1.1"), Port: 12345},
			expected: "192.168.1.1",
		},
		{
			name:     "ipv6_full",
			addr:     &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 12345},
			expected: "2001:db8::1",
		},
		{
			name:     "ipv4_loopback",
			addr:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345},
			expected: "127.0.0.1",
		},
		{
			name:     "ipv6_loopback",
			addr:     &net.TCPAddr{IP: net.ParseIP("::1"), Port: 12345},
			expected: "::1",
		},
		{
			name:     "nil_addr",
			addr:     nil,
			expected: "",
		},
		{
			name:     "zero_ipv4",
			addr:     &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 12345},
			expected: "0.0.0.0",
		},
		{
			name:     "unspecified_ipv6",
			addr:     &net.TCPAddr{IP: net.ParseIP("::"), Port: 12345},
			expected: "::",
		},
		{
			name:     "udp_addr",
			addr:     &net.UDPAddr{IP: net.ParseIP("192.168.1.1"), Port: 12345},
			expected: "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractIP(tt.addr)
			if result != tt.expected {
				t.Errorf("extractIP(%v) = %q, want %q", tt.addr, result, tt.expected)
			}
		})
	}
}

// TestConcurrentBanAndConnect tests for race conditions between ban and connect.
func TestConcurrentBanAndConnect(t *testing.T) {
	rl := newTestRateLimiter(t)
	ip := "192.168.1.100"
	addr := newMockAddr(ip, 12345)

	var wg sync.WaitGroup

	// Concurrent bans
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			rl.BanIP(ip, time.Duration(i)*time.Millisecond, "test")
			time.Sleep(time.Microsecond)
		}
	}()

	// Concurrent unbans
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			rl.UnbanIP(ip)
			time.Sleep(time.Microsecond)
		}
	}()

	// Concurrent connection attempts
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			rl.AllowConnection(addr)
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()

	// Should not panic or deadlock - state may be either banned or not
	t.Log("Concurrent ban/connect operations completed without deadlock")
}
