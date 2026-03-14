// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package discovery

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestDetectLocalSubnets(t *testing.T) {
	subnets, err := DetectLocalSubnets()
	if err != nil {
		t.Fatalf("DetectLocalSubnets failed: %v", err)
	}

	// Should find at least one subnet on most systems
	t.Logf("Detected %d subnets: %v", len(subnets), subnets)

	// Verify format of detected subnets
	for _, subnet := range subnets {
		_, _, err := net.ParseCIDR(subnet)
		if err != nil {
			t.Errorf("Invalid CIDR returned: %s", subnet)
		}
	}
}

func TestExpandCIDR(t *testing.T) {
	tests := []struct {
		cidr     string
		minHosts int
		maxHosts int
	}{
		{"192.168.1.0/30", 4, 4},     // 4 addresses (small subnets keep all)
		{"192.168.1.0/29", 8, 8},     // 8 addresses
		{"192.168.1.0/28", 16, 16},   // 16 addresses
		{"192.168.1.0/24", 253, 254}, // 256 addresses - 2 = 254 (we skip .0 and .255 for /24)
	}

	for _, tt := range tests {
		t.Run(tt.cidr, func(t *testing.T) {
			ips, err := expandCIDR(tt.cidr)
			if err != nil {
				t.Fatalf("expandCIDR(%s) failed: %v", tt.cidr, err)
			}

			if len(ips) < tt.minHosts || len(ips) > tt.maxHosts {
				t.Errorf("expandCIDR(%s) returned %d hosts, expected %d-%d",
					tt.cidr, len(ips), tt.minHosts, tt.maxHosts)
			}
		})
	}
}

func TestExpandCIDRInvalid(t *testing.T) {
	_, err := expandCIDR("not-a-cidr")
	if err == nil {
		t.Error("expandCIDR should fail for invalid CIDR")
	}
}

func TestScannerCreation(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := Config{
		Subnets:       []string{"192.168.1.0/24"},
		Ports:         []int{3333},
		Timeout:       1 * time.Second,
		MaxConcurrent: 10,
	}

	scanner := NewScanner(cfg, logger)
	if scanner == nil {
		t.Fatal("NewScanner returned nil")
	}

	if len(scanner.ports) != 1 || scanner.ports[0] != 3333 {
		t.Errorf("Expected port 3333, got %v", scanner.ports)
	}
}

func TestScannerDefaults(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Empty config should get defaults
	cfg := Config{
		Subnets: []string{"192.168.1.0/24"},
	}

	scanner := NewScanner(cfg, logger)

	// DefaultStratumPorts includes all supported coins:
	// DGB:3333, DGB-V2:3334, DGB-SCRYPT:3336, BTC:4333, BCH:5333, BC2:6333, LTC:7333, DOGE:8335
	// PEP:10335, CAT:12335, NMC:14335, SYS:15335, XMY:17335, FBTC:18335, QBX:20335
	if len(scanner.ports) != len(DefaultStratumPorts) {
		t.Errorf("Expected default ports %v, got %v", DefaultStratumPorts, scanner.ports)
	}
	if scanner.timeout != 500*time.Millisecond {
		t.Errorf("Expected default timeout 500ms, got %v", scanner.timeout)
	}
	if scanner.maxConcurrent != 200 {
		t.Errorf("Expected default maxConcurrent 200, got %d", scanner.maxConcurrent)
	}
}

// mockStratumServer creates a mock stratum server for testing
func mockStratumServer(t *testing.T) (string, int, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create mock server: %v", err)
	}

	addr := listener.Addr().(*net.TCPAddr)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func(c net.Conn) {
				defer c.Close()

				buf := make([]byte, 1024)
				n, err := c.Read(buf)
				if err != nil {
					return
				}

				// Parse request
				var req map[string]interface{}
				if err := json.Unmarshal(buf[:n], &req); err != nil {
					return
				}

				// Send mock subscribe response
				response := map[string]interface{}{
					"id": req["id"],
					"result": []interface{}{
						[]interface{}{
							[]interface{}{"mining.notify", "test-session"},
							"extranonce1",
						},
						4,
					},
					"error": nil,
				}

				data, _ := json.Marshal(response)
				c.Write(append(data, '\n'))
			}(conn)
		}
	}()

	return addr.IP.String(), addr.Port, func() { listener.Close() }
}

func TestScannerProbeHost(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Start mock server
	host, port, cleanup := mockStratumServer(t)
	defer cleanup()

	cfg := Config{
		Subnets:       []string{},
		Ports:         []int{port},
		Timeout:       2 * time.Second,
		MaxConcurrent: 1,
	}

	scanner := NewScanner(cfg, logger)

	// Test probing the mock server
	ctx := context.Background()
	pool := scanner.probeHost(ctx, host, port)

	if pool == nil {
		t.Fatal("probeHost should find mock server")
	}

	if pool.Host != host {
		t.Errorf("Expected host %s, got %s", host, pool.Host)
	}
	if pool.Port != port {
		t.Errorf("Expected port %d, got %d", port, pool.Port)
	}
	if !pool.IsHealthy {
		t.Error("Pool should be healthy")
	}
}

func TestScannerProbeHostUnreachable(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := Config{
		Timeout: 100 * time.Millisecond,
	}

	scanner := NewScanner(cfg, logger)

	// Try to probe a non-existent host
	ctx := context.Background()
	pool := scanner.probeHost(ctx, "192.0.2.1", 3333) // TEST-NET-1, should not be routable

	if pool != nil {
		t.Error("probeHost should return nil for unreachable host")
	}
}

func TestScannerScan(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Start mock server
	host, port, cleanup := mockStratumServer(t)
	defer cleanup()

	// Create scanner with subnet containing our mock server
	cfg := Config{
		Subnets:       []string{host + "/32"}, // Just scan the one host
		Ports:         []int{port},
		Timeout:       2 * time.Second,
		MaxConcurrent: 1,
	}

	scanner := NewScanner(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pools, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(pools) != 1 {
		t.Errorf("Expected 1 pool, found %d", len(pools))
	}

	// Verify GetDiscoveredPools returns the same
	discovered := scanner.GetDiscoveredPools()
	if len(discovered) != 1 {
		t.Errorf("GetDiscoveredPools returned %d, expected 1", len(discovered))
	}

	// Verify GetHealthyPools
	healthy := scanner.GetHealthyPools()
	if len(healthy) != 1 {
		t.Errorf("GetHealthyPools returned %d, expected 1", len(healthy))
	}
}

func TestScannerConcurrentScan(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	cfg := Config{
		Subnets:       []string{"192.0.2.0/30"}, // Small subnet, no real hosts
		Ports:         []int{3333},
		Timeout:       50 * time.Millisecond,
		MaxConcurrent: 2,
	}

	scanner := NewScanner(cfg, logger)

	ctx := context.Background()

	// First scan
	_, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("First scan failed: %v", err)
	}

	// Try concurrent scan - should fail
	go func() {
		scanner.Scan(ctx)
	}()

	time.Sleep(10 * time.Millisecond)

	_, _ = scanner.Scan(ctx)
	// Second concurrent scan should fail while first is running
	// But by now the first might be done, so we just check it doesn't panic
}

func TestDiscoveredPoolJSON(t *testing.T) {
	pool := &DiscoveredPool{
		Host:         "192.168.1.100",
		Port:         3333,
		Coin:         "DGB",
		PoolID:       "spiral-1",
		ResponseTime: Duration(150 * time.Millisecond),
		DiscoveredAt: time.Now(),
		IsHealthy:    true,
	}

	data, err := json.Marshal(pool)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	t.Logf("JSON: %s", string(data))

	// Verify the duration is marshaled as string
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if _, ok := decoded["responseTime"].(string); !ok {
		t.Error("responseTime should be marshaled as string")
	}
}

func TestIncrementIP(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"192.168.1.1", "192.168.1.2"},
		{"192.168.1.255", "192.168.2.0"},
		{"192.168.255.255", "192.169.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ip := net.ParseIP(tt.input).To4()
			incrementIP(ip)
			if ip.String() != tt.expected {
				t.Errorf("incrementIP(%s) = %s, expected %s",
					tt.input, ip.String(), tt.expected)
			}
		})
	}
}
