// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package discovery provides subnet scanning to find existing Spiral Pool instances.
// This enables automatic failover pool detection during installation and runtime.
//
// NOTE: IPv4 ONLY - This package only supports IPv4 addresses. IPv6 is disabled
// at the OS level during installation for simplicity and security.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DiscoveredPool represents a pool found during subnet scanning.
type DiscoveredPool struct {
	Host         string    `json:"host"`
	Port         int       `json:"port"`
	Coin         string    `json:"coin,omitempty"`   // Detected coin if available
	PoolID       string    `json:"poolId,omitempty"` // Pool ID from subscribe response
	ResponseTime Duration  `json:"responseTime"`
	DiscoveredAt time.Time `json:"discoveredAt"`
	IsHealthy    bool      `json:"isHealthy"`
}

// Duration wraps time.Duration for JSON marshaling
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Scanner discovers Spiral Pool instances on the network.
type Scanner struct {
	subnets       []string
	ports         []int
	timeout       time.Duration
	maxConcurrent int
	logger        *zap.SugaredLogger

	mu        sync.RWMutex
	pools     map[string]*DiscoveredPool // key: "host:port"
	lastScan  time.Time
	isRunning bool
}

// Config holds scanner configuration.
type Config struct {
	Subnets       []string      // CIDR subnets to scan
	Ports         []int         // Stratum ports to check
	Timeout       time.Duration // Per-host timeout
	MaxConcurrent int           // Max concurrent scans
}

// DefaultStratumPorts returns the default stratum ports for all supported coins.
// These are the ports used by config.example.yaml for each coin.
var DefaultStratumPorts = []int{
	3333, 3334, // DGB (SHA256d) + DGB V2
	3336,       // DGB-SCRYPT
	4333,       // BTC
	5333,       // BCH
	6333,       // BC2 (Bitcoin II)
	7333,       // LTC
	8335,       // DOGE
	10335,      // PEP (PepeCoin)
	12335,      // CAT (Catcoin)
	// SHA-256d AuxPoW merge-mined coins
	14335, // NMC (Namecoin)
	15335, // SYS (Syscoin)
	17335, // XMY (Myriad)
	18335, // FBTC (Fractal Bitcoin)
	20335, // QBX (Q-BitX)
}

// NewScanner creates a new pool discovery scanner.
func NewScanner(cfg Config, logger *zap.Logger) *Scanner {
	if len(cfg.Ports) == 0 {
		cfg.Ports = DefaultStratumPorts
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 500 * time.Millisecond // Fast timeout for LAN scanning
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 200 // High concurrency for fast subnet scans
	}

	return &Scanner{
		subnets:       cfg.Subnets,
		ports:         cfg.Ports,
		timeout:       cfg.Timeout,
		maxConcurrent: cfg.MaxConcurrent,
		logger:        logger.Sugar().Named("discovery"),
		pools:         make(map[string]*DiscoveredPool),
	}
}

// NewAutoScanner creates a scanner that auto-detects local subnets (IPv4 only).
// This is the recommended way to create a scanner for automatic pool discovery.
func NewAutoScanner(logger *zap.Logger) (*Scanner, error) {
	subnets, err := DetectLocalSubnets()
	if err != nil {
		return nil, fmt.Errorf("failed to detect local subnets: %w", err)
	}

	if len(subnets) == 0 {
		return nil, fmt.Errorf("no local subnets detected")
	}

	cfg := Config{
		Subnets:       subnets,
		Ports:         DefaultStratumPorts,
		Timeout:       500 * time.Millisecond, // Fast timeout for LAN scanning
		MaxConcurrent: 200,                    // High concurrency for fast subnet scans
	}

	log := logger.Sugar().Named("discovery")
	log.Infow("Auto-detected local subnets (IPv4 only)",
		"subnets", subnets,
	)

	return NewScanner(cfg, logger), nil
}

// DetectLocalSubnets auto-detects the local network subnets.
func DetectLocalSubnets() ([]string, error) {
	var subnets []string

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces: %w", err)
	}

	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Skip IPv6 and loopback
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}

			// Skip link-local addresses (169.254.x.x)
			if ip4[0] == 169 && ip4[1] == 254 {
				continue
			}

			// Get the network address
			network := &net.IPNet{
				IP:   ip4.Mask(ipNet.Mask),
				Mask: ipNet.Mask,
			}
			subnets = append(subnets, network.String())
		}
	}

	return subnets, nil
}

// Scan performs a one-time scan of configured subnets.
func (s *Scanner) Scan(ctx context.Context) ([]*DiscoveredPool, error) {
	s.mu.Lock()
	if s.isRunning {
		s.mu.Unlock()
		return nil, fmt.Errorf("scan already in progress")
	}
	s.isRunning = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.isRunning = false
		s.lastScan = time.Now()
		s.mu.Unlock()
	}()

	// Collect all IPs to scan
	var hosts []string
	for _, subnet := range s.subnets {
		ips, err := expandCIDR(subnet)
		if err != nil {
			s.logger.Warnw("Invalid subnet", "subnet", subnet, "error", err)
			continue
		}
		hosts = append(hosts, ips...)
	}

	if len(hosts) == 0 {
		return nil, nil
	}

	s.logger.Infow("Starting subnet scan",
		"hosts", len(hosts),
		"ports", s.ports,
		"maxConcurrent", s.maxConcurrent,
	)

	// Create work channel
	type scanTarget struct {
		host string
		port int
	}

	targets := make(chan scanTarget, len(hosts)*len(s.ports))
	results := make(chan *DiscoveredPool, len(hosts)*len(s.ports))

	// Queue all targets
	for _, host := range hosts {
		for _, port := range s.ports {
			targets <- scanTarget{host: host, port: port}
		}
	}
	close(targets)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < s.maxConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					s.logger.Errorw("PANIC recovered in probe worker", "panic", r)
				}
			}()
			for target := range targets {
				select {
				case <-ctx.Done():
					return
				default:
					if pool := s.probeHost(ctx, target.host, target.port); pool != nil {
						results <- pool
					}
				}
			}
		}()
	}

	// Wait for workers and close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var discovered []*DiscoveredPool
	for pool := range results {
		key := fmt.Sprintf("%s:%d", pool.Host, pool.Port)
		s.mu.Lock()
		s.pools[key] = pool
		s.mu.Unlock()
		discovered = append(discovered, pool)
	}

	// Sort by response time
	sort.Slice(discovered, func(i, j int) bool {
		return time.Duration(discovered[i].ResponseTime) < time.Duration(discovered[j].ResponseTime)
	})

	s.logger.Infow("Scan complete",
		"discovered", len(discovered),
		"scanned", len(hosts)*len(s.ports),
	)

	return discovered, nil
}

// probeHost checks if a stratum server is running at host:port.
func (s *Scanner) probeHost(ctx context.Context, host string, port int) *DiscoveredPool {
	addr := fmt.Sprintf("%s:%d", host, port)
	start := time.Now()

	// Create connection with fast dial timeout (200ms for LAN)
	dialTimeout := s.timeout
	if dialTimeout > 200*time.Millisecond {
		dialTimeout = 200 * time.Millisecond
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil // Not a stratum server or not reachable
	}
	defer conn.Close()

	// Set read/write deadline for stratum handshake
	deadline := time.Now().Add(s.timeout)
	_ = conn.SetDeadline(deadline) // #nosec G104

	// Send stratum subscribe to verify it's a real stratum server
	subscribe := `{"id":1,"method":"mining.subscribe","params":["SpiralDiscovery/1.0"]}` + "\n"
	if _, err := conn.Write([]byte(subscribe)); err != nil {
		return nil
	}

	// Read response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}

	responseTime := time.Since(start)

	// Parse response to verify it's stratum
	var response map[string]interface{}
	if err := json.Unmarshal(buf[:n], &response); err != nil {
		return nil // Not valid JSON, not a stratum server
	}

	// Check for valid stratum response (has "result" or "error" field)
	_, hasResult := response["result"]
	_, hasError := response["error"]
	if !hasResult && !hasError {
		return nil // Not a stratum response
	}

	pool := &DiscoveredPool{
		Host:         host,
		Port:         port,
		ResponseTime: Duration(responseTime),
		DiscoveredAt: time.Now(),
		IsHealthy:    hasResult,
	}

	// Try to extract pool ID from result
	if result, ok := response["result"].([]interface{}); ok && len(result) > 1 {
		if sessionData, ok := result[0].([]interface{}); ok && len(sessionData) > 1 {
			if poolID, ok := sessionData[1].(string); ok {
				pool.PoolID = poolID
			}
		}
	}

	s.logger.Debugw("Found stratum server",
		"host", host,
		"port", port,
		"responseTime", responseTime,
	)

	return pool
}

// GetDiscoveredPools returns all currently known pools.
func (s *Scanner) GetDiscoveredPools() []*DiscoveredPool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pools := make([]*DiscoveredPool, 0, len(s.pools))
	for _, pool := range s.pools {
		pools = append(pools, pool)
	}

	// Sort by response time
	sort.Slice(pools, func(i, j int) bool {
		return time.Duration(pools[i].ResponseTime) < time.Duration(pools[j].ResponseTime)
	})

	return pools
}

// GetHealthyPools returns only healthy discovered pools.
func (s *Scanner) GetHealthyPools() []*DiscoveredPool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pools []*DiscoveredPool
	for _, pool := range s.pools {
		if pool.IsHealthy {
			pools = append(pools, pool)
		}
	}

	sort.Slice(pools, func(i, j int) bool {
		return time.Duration(pools[i].ResponseTime) < time.Duration(pools[j].ResponseTime)
	})

	return pools
}

// LastScanTime returns when the last scan was performed.
func (s *Scanner) LastScanTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastScan
}

// expandCIDR expands a CIDR notation to a list of IP addresses.
func expandCIDR(cidr string) ([]string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	var ips []string
	for ip := ipNet.IP.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
		// Skip network and broadcast addresses for /24 and smaller
		ones, bits := ipNet.Mask.Size()
		if ones <= 24 {
			if ip[3] == 0 || ip[3] == 255 {
				continue
			}
		}
		// Limit to /16 to avoid excessive scanning
		if bits-ones > 16 {
			if len(ips) >= 65534 {
				break
			}
		}
		ips = append(ips, ip.String())
	}

	return ips, nil
}

// incrementIP increments an IP address by 1.
func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
