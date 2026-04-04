// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package ha provides High Availability features including Virtual IP (VIP)
// management, cluster coordination, and automatic failover for miners.
//
// # Virtual IP Architecture
//
// The VIP system allows multiple Spiral Pool nodes to share a single IP address.
// Miners connect to this VIP, and when the master node fails, the VIP automatically
// moves to a healthy backup node - transparent to miners.
//
// # Cluster Roles
//
//   - MASTER: Holds the VIP, accepts all miner connections
//   - BACKUP: Monitors master health, ready to take over VIP
//   - OBSERVER: Read-only node, cannot become master
//
// # Discovery Protocol
//
// Nodes discover each other via UDP broadcast on port 5363 (configurable).
// The first node to start becomes master and generates a cluster token.
// Subsequent nodes detect the master and join as backups.
//
// # Failover Process
//
//  1. Backup nodes continuously ping the master
//  2. If master fails 3 consecutive checks, election begins
//  3. Highest priority backup acquires the VIP
//  4. New master sends gratuitous ARP to update network switches
//  5. Miners' connections are maintained (same IP, new MAC)
//
// # Integration with Sentinel
//
// Sentinel can query the HA status endpoint (:5354/status) to:
//   - Detect if HA mode is active
//   - Get the current VIP address for miner configuration
//   - Monitor cluster health
package ha

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/hkdf"
)

// Role represents the node's role in the HA cluster.
type Role int

const (
	RoleUnknown  Role = iota
	RoleMaster        // Holds the VIP, accepts miner connections
	RoleBackup        // Ready to take over if master fails
	RoleObserver      // Read-only, cannot become master
)

func (r Role) String() string {
	switch r {
	case RoleMaster:
		return "MASTER"
	case RoleBackup:
		return "BACKUP"
	case RoleObserver:
		return "OBSERVER"
	default:
		return "UNKNOWN"
	}
}

// State represents the cluster state.
type State int

const (
	StateInitializing State = iota
	StateElection
	StateRunning
	StateFailover
	StateDegraded
)

func (s State) String() string {
	switch s {
	case StateInitializing:
		return "initializing"
	case StateElection:
		return "election"
	case StateRunning:
		return "running"
	case StateFailover:
		return "failover"
	case StateDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// SyncThresholdPercent is the minimum sync percentage to consider a blockchain "synced".
// Using 99.9% instead of 100% because:
//   - Floating point: 99.9999... effectively equals 100 for practical purposes
//   - Network lag: A node might be 1-2 blocks behind and still be considered operational
//   - Rapid sync: During final sync, percentage might briefly dip below 100% as new blocks arrive
//
// This threshold means a blockchain with 10,000,000 blocks is "synced" if within 10,000 blocks.
const SyncThresholdPercent = 99.9

// Coin awareness validation limits (anti-abuse/DoS protection)
const (
	MaxCoinTickerLength = 10 // Maximum length of coin ticker (e.g., "DGB", "BTC")
	MaxSupportedCoins   = 50 // Maximum coins a node can support (prevents memory exhaustion)
)

// ============================================================================
// VIP Flap Detection and Exponential Backoff
// ============================================================================
//
// Flap detection prevents rapid VIP transitions that destabilize the cluster.
// When failovers occur too frequently within a time window, the failover timeout
// is increased exponentially to allow the cluster to stabilize.
//
// Configuration:
//   - FlapWindow: Time window to count failovers (default: 5 minutes)
//   - FlapThreshold: Max failovers in window before triggering backoff (default: 3)
//   - BaseTimeout: Initial failover timeout (default: 90 seconds)
//   - MaxTimeout: Maximum backoff timeout (default: 15 minutes)
//   - BackoffMultiplier: Exponential backoff factor (default: 2.0)

// FlapDetectorConfig configures flap detection behavior.
type FlapDetectorConfig struct {
	// FlapWindow is the time window for counting failover events
	FlapWindow time.Duration `yaml:"flapWindow" json:"flapWindow"`

	// FlapThreshold is the number of failovers in FlapWindow that triggers backoff
	FlapThreshold int `yaml:"flapThreshold" json:"flapThreshold"`

	// BaseTimeout is the initial failover timeout before any backoff
	BaseTimeout time.Duration `yaml:"baseTimeout" json:"baseTimeout"`

	// MaxTimeout is the maximum failover timeout after exponential backoff
	MaxTimeout time.Duration `yaml:"maxTimeout" json:"maxTimeout"`

	// BackoffMultiplier is the exponential factor for timeout increase
	BackoffMultiplier float64 `yaml:"backoffMultiplier" json:"backoffMultiplier"`
}

// DefaultFlapDetectorConfig returns sensible defaults for flap detection.
func DefaultFlapDetectorConfig() FlapDetectorConfig {
	return FlapDetectorConfig{
		FlapWindow:        5 * time.Minute,
		FlapThreshold:     3,
		BaseTimeout:       90 * time.Second,
		MaxTimeout:        15 * time.Minute,
		BackoffMultiplier: 2.0,
	}
}

// flapDetector tracks failover frequency and applies exponential backoff.
type flapDetector struct {
	mu     sync.RWMutex
	config FlapDetectorConfig
	logger *zap.SugaredLogger

	// Failover history - timestamps of recent failovers
	failoverHistory []time.Time

	// Current backoff state
	currentBackoffLevel int           // Number of times backoff has been applied
	currentTimeout      time.Duration // Current effective timeout
	lastBackoffReset    time.Time     // When backoff was last reset to baseline

	// Metrics
	totalFlapsDetected int64
	totalBackoffs      int64
}

// newFlapDetector creates a new flap detector with the given config.
func newFlapDetector(config FlapDetectorConfig, logger *zap.SugaredLogger) *flapDetector {
	if config.FlapWindow == 0 {
		config = DefaultFlapDetectorConfig()
	}
	if config.BaseTimeout == 0 {
		config.BaseTimeout = 90 * time.Second
	}
	if config.MaxTimeout == 0 {
		config.MaxTimeout = 15 * time.Minute
	}
	if config.BackoffMultiplier == 0 {
		config.BackoffMultiplier = 2.0
	}
	if config.FlapThreshold == 0 {
		config.FlapThreshold = 3
	}

	return &flapDetector{
		config:           config,
		logger:           logger,
		failoverHistory:  make([]time.Time, 0, 10),
		currentTimeout:   config.BaseTimeout,
		lastBackoffReset: time.Now(),
	}
}

// RecordFailover records a failover event and checks for flapping.
// Returns true if flapping is detected and backoff should be applied.
func (fd *flapDetector) RecordFailover() bool {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	now := time.Now()
	fd.failoverHistory = append(fd.failoverHistory, now)

	// Clean up old entries outside the flap window
	fd.pruneHistoryLocked(now)

	// Check if we're flapping
	if len(fd.failoverHistory) >= fd.config.FlapThreshold {
		fd.totalFlapsDetected++
		fd.applyBackoffLocked()
		return true
	}

	return false
}

// pruneHistoryLocked removes failover entries outside the flap window.
// Must be called with mutex held.
func (fd *flapDetector) pruneHistoryLocked(now time.Time) {
	cutoff := now.Add(-fd.config.FlapWindow)
	validIdx := 0
	for _, t := range fd.failoverHistory {
		if t.After(cutoff) {
			fd.failoverHistory[validIdx] = t
			validIdx++
		}
	}
	fd.failoverHistory = fd.failoverHistory[:validIdx]
}

// applyBackoffLocked increases the failover timeout exponentially.
// Must be called with mutex held.
func (fd *flapDetector) applyBackoffLocked() {
	fd.currentBackoffLevel++
	fd.totalBackoffs++

	// Calculate new timeout: baseTimeout * (multiplier ^ backoffLevel)
	multiplier := 1.0
	for i := 0; i < fd.currentBackoffLevel; i++ {
		multiplier *= fd.config.BackoffMultiplier
	}
	newTimeout := time.Duration(float64(fd.config.BaseTimeout) * multiplier)

	// Cap at maximum
	if newTimeout > fd.config.MaxTimeout {
		newTimeout = fd.config.MaxTimeout
	}

	oldTimeout := fd.currentTimeout
	fd.currentTimeout = newTimeout

	fd.logger.Warnw("VIP flapping detected - applying exponential backoff",
		"failoversInWindow", len(fd.failoverHistory),
		"flapWindow", fd.config.FlapWindow,
		"backoffLevel", fd.currentBackoffLevel,
		"oldTimeout", oldTimeout,
		"newTimeout", newTimeout,
		"maxTimeout", fd.config.MaxTimeout,
	)
}

// GetCurrentTimeout returns the current effective failover timeout.
func (fd *flapDetector) GetCurrentTimeout() time.Duration {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	return fd.currentTimeout
}

// GetBackoffLevel returns the current backoff level (0 = no backoff).
func (fd *flapDetector) GetBackoffLevel() int {
	fd.mu.RLock()
	defer fd.mu.RUnlock()
	return fd.currentBackoffLevel
}

// IsFlapping returns true if the cluster is currently in a flapping state.
func (fd *flapDetector) IsFlapping() bool {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-fd.config.FlapWindow)
	count := 0
	for _, t := range fd.failoverHistory {
		if t.After(cutoff) {
			count++
		}
	}
	return count >= fd.config.FlapThreshold
}

// ResetBackoff resets the backoff to baseline if cluster has been stable.
// Should be called periodically (e.g., every FlapWindow) when no flapping detected.
func (fd *flapDetector) ResetBackoff() {
	fd.mu.Lock()
	defer fd.mu.Unlock()

	// Only reset if we've been stable for at least one flap window
	now := time.Now()
	if now.Sub(fd.lastBackoffReset) < fd.config.FlapWindow {
		return
	}

	// Check if we're still flapping
	fd.pruneHistoryLocked(now)
	if len(fd.failoverHistory) >= fd.config.FlapThreshold {
		return // Still flapping, don't reset
	}

	// Gradually reduce backoff level instead of immediate reset
	// FIX VIP-H1: Decay by 2 levels per stable window for faster recovery after partition heals
	if fd.currentBackoffLevel > 0 {
		fd.currentBackoffLevel -= 2
		if fd.currentBackoffLevel < 0 {
			fd.currentBackoffLevel = 0
		}

		// Recalculate timeout
		if fd.currentBackoffLevel == 0 {
			fd.currentTimeout = fd.config.BaseTimeout
		} else {
			multiplier := 1.0
			for i := 0; i < fd.currentBackoffLevel; i++ {
				multiplier *= fd.config.BackoffMultiplier
			}
			fd.currentTimeout = time.Duration(float64(fd.config.BaseTimeout) * multiplier)
		}

		fd.lastBackoffReset = now
		fd.logger.Infow("VIP flap backoff reduced - cluster stabilizing",
			"backoffLevel", fd.currentBackoffLevel,
			"currentTimeout", fd.currentTimeout,
		)
	}
}

// GetStats returns flap detection statistics.
func (fd *flapDetector) GetStats() FlapDetectorStats {
	fd.mu.RLock()
	defer fd.mu.RUnlock()

	// Count recent failovers without modifying state (read-only)
	now := time.Now()
	cutoff := now.Add(-fd.config.FlapWindow)
	count := 0
	for _, t := range fd.failoverHistory {
		if t.After(cutoff) {
			count++
		}
	}

	return FlapDetectorStats{
		CurrentTimeout:       fd.currentTimeout,
		BackoffLevel:         fd.currentBackoffLevel,
		FailoversInWindow:    count,
		FlapWindow:           fd.config.FlapWindow,
		FlapThreshold:        fd.config.FlapThreshold,
		IsFlapping:           count >= fd.config.FlapThreshold,
		TotalFlapsDetected:   fd.totalFlapsDetected,
		TotalBackoffsApplied: fd.totalBackoffs,
	}
}

// FlapDetectorStats contains flap detection metrics for monitoring.
type FlapDetectorStats struct {
	CurrentTimeout       time.Duration `json:"currentTimeout"`
	BackoffLevel         int           `json:"backoffLevel"`
	FailoversInWindow    int           `json:"failoversInWindow"`
	FlapWindow           time.Duration `json:"flapWindow"`
	FlapThreshold        int           `json:"flapThreshold"`
	IsFlapping           bool          `json:"isFlapping"`
	TotalFlapsDetected   int64         `json:"totalFlapsDetected"`
	TotalBackoffsApplied int64         `json:"totalBackoffsApplied"`
}

// ============================================================================
// Network Partition Detection (Multi-Path Heartbeat)
// ============================================================================
//
// Network partition detection uses multiple paths to verify connectivity:
// 1. UDP heartbeat (primary) - fast, low overhead
// 2. HTTP heartbeat (secondary) - more reliable, detects asymmetric partitions
// 3. Witness nodes (optional) - external verification of cluster state
//
// A node is considered partitioned only when multiple paths fail, reducing
// false positives from transient network issues.

// HeartbeatPath represents a communication path for heartbeat verification.
type HeartbeatPath int

const (
	HeartbeatPathUDP HeartbeatPath = iota
	HeartbeatPathHTTP
	HeartbeatPathWitness
)

func (p HeartbeatPath) String() string {
	switch p {
	case HeartbeatPathUDP:
		return "UDP"
	case HeartbeatPathHTTP:
		return "HTTP"
	case HeartbeatPathWitness:
		return "WITNESS"
	default:
		return "UNKNOWN"
	}
}

// PartitionStatus represents the current network partition detection state.
type PartitionStatus struct {
	// IsPartitioned indicates if this node believes it's partitioned
	IsPartitioned bool `json:"isPartitioned"`

	// PartitionDetectedAt is when partition was first detected
	PartitionDetectedAt time.Time `json:"partitionDetectedAt,omitempty"`

	// FailedPaths lists which heartbeat paths are currently failing
	FailedPaths []string `json:"failedPaths"`

	// HealthyPaths lists which heartbeat paths are healthy
	HealthyPaths []string `json:"healthyPaths"`

	// WitnessResponses contains responses from witness nodes
	WitnessResponses map[string]*WitnessResponse `json:"witnessResponses,omitempty"`

	// LastCheck is when partition status was last evaluated
	LastCheck time.Time `json:"lastCheck"`
}

// WitnessResponse contains the response from a witness node query.
type WitnessResponse struct {
	Address     string    `json:"address"`
	Reachable   bool      `json:"reachable"`
	MasterID    string    `json:"masterId,omitempty"`
	MasterHost  string    `json:"masterHost,omitempty"`
	ClusterSize int       `json:"clusterSize,omitempty"`
	QueryTime   time.Time `json:"queryTime"`
	Latency     int64     `json:"latencyMs"`
	Error       string    `json:"error,omitempty"`
}

// partitionDetector tracks multi-path heartbeat status for partition detection.
type partitionDetector struct {
	mu     sync.RWMutex
	logger *zap.SugaredLogger
	config Config

	// Per-node path status tracking
	nodePathStatus map[string]*nodeHeartbeatStatus

	// Witness node responses (cached)
	witnessResponses map[string]*WitnessResponse

	// Overall partition state
	isPartitioned       bool
	partitionDetectedAt time.Time

	// HTTP client for HTTP heartbeats and witness queries
	httpClient *http.Client

	// Prometheus metrics (optional)
	metrics haMetrics
}

// nodeHeartbeatStatus tracks heartbeat status per node across multiple paths.
type nodeHeartbeatStatus struct {
	nodeID       string
	udpLastSeen  time.Time
	httpLastSeen time.Time
	udpHealthy   bool
	httpHealthy  bool
}

// newPartitionDetector creates a new partition detector.
func newPartitionDetector(config Config, logger *zap.SugaredLogger, httpClient *http.Client) *partitionDetector {
	return &partitionDetector{
		logger:           logger,
		config:           config,
		nodePathStatus:   make(map[string]*nodeHeartbeatStatus),
		witnessResponses: make(map[string]*WitnessResponse),
		httpClient:       httpClient,
	}
}

// RecordUDPHeartbeat records a successful UDP heartbeat from a node.
func (pd *partitionDetector) RecordUDPHeartbeat(nodeID string) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	status, ok := pd.nodePathStatus[nodeID]
	if !ok {
		status = &nodeHeartbeatStatus{nodeID: nodeID}
		pd.nodePathStatus[nodeID] = status
	}
	status.udpLastSeen = time.Now()
	status.udpHealthy = true
}

// RecordHTTPHeartbeat records a successful HTTP heartbeat from a node.
func (pd *partitionDetector) RecordHTTPHeartbeat(nodeID string) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	status, ok := pd.nodePathStatus[nodeID]
	if !ok {
		status = &nodeHeartbeatStatus{nodeID: nodeID}
		pd.nodePathStatus[nodeID] = status
	}
	status.httpLastSeen = time.Now()
	status.httpHealthy = true
}

// CheckNodeHealth checks if a node is reachable via multiple paths.
// Returns (isHealthy, failedPathCount).
func (pd *partitionDetector) CheckNodeHealth(nodeID string, timeout time.Duration) (bool, int) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	status, ok := pd.nodePathStatus[nodeID]
	if !ok {
		return false, 2 // Both paths failed (no data)
	}

	now := time.Now()
	failedPaths := 0

	// Check UDP path
	if now.Sub(status.udpLastSeen) > timeout {
		status.udpHealthy = false
		failedPaths++
	}

	// Check HTTP path
	if now.Sub(status.httpLastSeen) > timeout {
		status.httpHealthy = false
		failedPaths++
	}

	isHealthy := failedPaths < pd.config.PartitionDetectionThreshold
	return isHealthy, failedPaths
}

// QueryWitness queries a witness node for cluster state verification.
func (pd *partitionDetector) QueryWitness(witnessAddr string) *WitnessResponse {
	start := time.Now()
	response := &WitnessResponse{
		Address:   witnessAddr,
		QueryTime: start,
	}

	url := fmt.Sprintf("http://%s/status", witnessAddr)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		response.Error = err.Error()
		response.Latency = time.Since(start).Milliseconds()
		return response
	}

	req.Header.Set("User-Agent", SpiralClusterUserAgent)

	resp, err := pd.httpClient.Do(req)
	if err != nil {
		response.Error = err.Error()
		response.Latency = time.Since(start).Milliseconds()
		return response
	}
	defer resp.Body.Close()

	response.Latency = time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		response.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return response
	}

	// Parse response
	var status struct {
		MasterId   string `json:"masterId"`
		MasterHost string `json:"masterHost"`
		Nodes      []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64KB limit
	if err != nil {
		response.Error = err.Error()
		return response
	}

	if err := json.Unmarshal(body, &status); err != nil {
		response.Error = err.Error()
		return response
	}

	response.Reachable = true
	response.MasterID = status.MasterId
	response.MasterHost = status.MasterHost
	response.ClusterSize = len(status.Nodes)

	// Cache the response
	pd.mu.Lock()
	pd.witnessResponses[witnessAddr] = response
	pd.mu.Unlock()

	return response
}

// QueryAllWitnesses queries all configured witness nodes.
func (pd *partitionDetector) QueryAllWitnesses() map[string]*WitnessResponse {
	results := make(map[string]*WitnessResponse)

	for _, addr := range pd.config.WitnessNodes {
		results[addr] = pd.QueryWitness(addr)
	}

	return results
}

// GetPartitionStatus returns the current partition detection status.
func (pd *partitionDetector) GetPartitionStatus(failoverTimeout time.Duration) PartitionStatus {
	pd.mu.RLock()
	defer pd.mu.RUnlock()

	// Deep copy witnessResponses to avoid returning internal map reference
	wrCopy := make(map[string]*WitnessResponse, len(pd.witnessResponses))
	for k, v := range pd.witnessResponses {
		vCopy := *v
		wrCopy[k] = &vCopy
	}

	status := PartitionStatus{
		IsPartitioned:    pd.isPartitioned,
		FailedPaths:      make([]string, 0),
		HealthyPaths:     make([]string, 0),
		WitnessResponses: wrCopy,
		LastCheck:        time.Now(),
	}

	if pd.isPartitioned {
		status.PartitionDetectedAt = pd.partitionDetectedAt
	}

	// Aggregate path health across all nodes
	now := time.Now()
	udpHealthy := false
	httpHealthy := false

	for _, nodeStatus := range pd.nodePathStatus {
		if now.Sub(nodeStatus.udpLastSeen) <= failoverTimeout {
			udpHealthy = true
		}
		if now.Sub(nodeStatus.httpLastSeen) <= failoverTimeout {
			httpHealthy = true
		}
	}

	if udpHealthy {
		status.HealthyPaths = append(status.HealthyPaths, HeartbeatPathUDP.String())
	} else {
		status.FailedPaths = append(status.FailedPaths, HeartbeatPathUDP.String())
	}

	if httpHealthy {
		status.HealthyPaths = append(status.HealthyPaths, HeartbeatPathHTTP.String())
	} else {
		status.FailedPaths = append(status.FailedPaths, HeartbeatPathHTTP.String())
	}

	return status
}

// SetPartitioned sets the partition state.
func (pd *partitionDetector) SetPartitioned(partitioned bool) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	if partitioned && !pd.isPartitioned {
		pd.partitionDetectedAt = time.Now()
		if pd.metrics != nil {
			pd.metrics.IncHAPartitionDetected()
		}
		pd.logger.Warnw("Network partition detected",
			"time", pd.partitionDetectedAt,
		)
	} else if !partitioned && pd.isPartitioned {
		pd.logger.Infow("Network partition resolved",
			"duration", time.Since(pd.partitionDetectedAt),
		)
	}

	pd.isPartitioned = partitioned
}

// CoinSyncStatus represents the blockchain sync status for a specific coin.
type CoinSyncStatus struct {
	Coin        string  `json:"coin"`        // Coin ticker (e.g., "DGB", "RVN", "PEPE")
	SyncPct     float64 `json:"syncPct"`     // Sync percentage (0-100)
	BlockHeight int64   `json:"blockHeight"` // Current block height
	IsSynced    bool    `json:"isSynced"`    // True if fully synced (>= SyncThresholdPercent)
}

// CoinPortInfo contains stratum port information for a coin.
type CoinPortInfo struct {
	StratumV1  int `json:"stratumV1"`            // V1 stratum port
	StratumV2  int `json:"stratumV2,omitempty"`  // V2 stratum port (if enabled)
	StratumTLS int `json:"stratumTLS,omitempty"` // TLS stratum port (if enabled)
}

// ClusterNode represents a node in the HA cluster.
type ClusterNode struct {
	ID          string    `json:"id"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Role        Role      `json:"role"`
	Priority    int       `json:"priority"` // Lower = higher priority for master election
	JoinedAt    time.Time `json:"joinedAt"`
	LastSeen    time.Time `json:"lastSeen"`
	StratumPort int       `json:"stratumPort"` // The primary stratum port (for backwards compat)
	IsHealthy   bool      `json:"isHealthy"`

	// Multi-coin port support - per-coin stratum ports for V2 multi-coin setups
	CoinPorts map[string]*CoinPortInfo `json:"coinPorts,omitempty"` // Per-coin stratum ports (key: coin symbol like "DGB", "BTC")

	// Coin awareness - which coins this node supports and their sync status
	SupportedCoins []string                   `json:"supportedCoins,omitempty"` // List of coin tickers this node supports
	CoinSyncStatus map[string]*CoinSyncStatus `json:"coinSyncStatus,omitempty"` // Sync status per coin

	// Pool payout addresses - per-coin addresses for failover validation
	// Key: coin ticker (e.g., "BTC", "DGB"), Value: payout address
	// WARNING: Different addresses across HA nodes will cause split payouts!
	PoolAddresses map[string]string `json:"poolAddresses,omitempty"`
}

// ClusterStatus represents the current cluster state for API responses.
type ClusterStatus struct {
	Enabled       bool           `json:"enabled"`
	State         string         `json:"state"`
	VIP           string         `json:"vip"`          // Virtual IP address
	VIPInterface  string         `json:"vipInterface"` // Network interface holding VIP
	MasterID      string         `json:"masterId"`
	MasterHost    string         `json:"masterHost"`
	LocalRole     string         `json:"localRole"`
	LocalID       string         `json:"localId"`
	Nodes         []*ClusterNode `json:"nodes"`
	ClusterToken  string         `json:"clusterToken"` // Unique cluster identifier
	LastFailover  time.Time      `json:"lastFailover,omitempty"`
	FailoverCount int            `json:"failoverCount"`

	// Multi-coin port support - local node's configured ports for API convenience
	CoinPorts map[string]*CoinPortInfo `json:"coinPorts,omitempty"`

	// Flap detection status
	FlapStats *FlapDetectorStats `json:"flapStats,omitempty"`

	// Network partition detection status
	PartitionStatus *PartitionStatus `json:"partitionStatus,omitempty"`
}

// Config holds VIP manager configuration.
type Config struct {
	// Enabled controls whether HA/VIP mode is active
	Enabled bool `yaml:"enabled" json:"enabled"`

	// NodeID is this node's unique identifier (generated if empty)
	NodeID string `yaml:"nodeId" json:"nodeId"`

	// Priority for master election (lower = higher priority)
	// Valid range: 100-999 (values outside this range are clamped for security)
	// When AutoPriority is enabled: 100 for master, 101 for first backup, 102 for second, etc.
	// Priority < 100 is rejected from network messages to prevent priority hijacking attacks.
	Priority int `yaml:"priority" json:"priority"`

	// AutoPriority enables automatic priority assignment based on join order
	AutoPriority bool `yaml:"autoPriority" json:"autoPriority"`

	// VIPAddress is the virtual IP that floats between masters
	// Leave empty to auto-generate from the local subnet
	VIPAddress string `yaml:"vipAddress" json:"vipAddress"`

	// VIPInterface is the network interface to bind the VIP to
	// Examples: eth0, ens192, enp0s3
	VIPInterface string `yaml:"vipInterface" json:"vipInterface"`

	// VIPNetmask is the CIDR prefix for the VIP address (default /32 = single-IP, no subnet route)
	VIPNetmask int `yaml:"vipNetmask" json:"vipNetmask"`

	// DiscoveryPort is the UDP port for cluster discovery (default: 5363)
	// Note: 5363 is used instead of 5353 to avoid conflict with mDNS
	DiscoveryPort int `yaml:"discoveryPort" json:"discoveryPort"`

	// StatusPort is the HTTP port for status API (default: 5354)
	StatusPort int `yaml:"statusPort" json:"statusPort"`

	// StratumPort is the port miners connect to (default: 3333)
	StratumPort int `yaml:"stratumPort" json:"stratumPort"`

	// HeartbeatInterval is how often to send heartbeats (default: 30s)
	HeartbeatInterval time.Duration `yaml:"heartbeatInterval" json:"heartbeatInterval"`

	// FailoverTimeout is how long before declaring master dead (default: 90s)
	FailoverTimeout time.Duration `yaml:"failoverTimeout" json:"failoverTimeout"`

	// ClusterToken is the shared secret for the cluster
	// First master generates this; backups must match to join
	ClusterToken string `yaml:"clusterToken" json:"clusterToken"`

	// CanBecomeMaster controls whether this node can become master
	CanBecomeMaster bool `yaml:"canBecomeMaster" json:"canBecomeMaster"`

	// GratuitousARPCount is how many GARP packets to send on VIP acquisition
	GratuitousARPCount int `yaml:"gratuitousArpCount" json:"gratuitousArpCount"`

	// VIPMACAddress is the virtual MAC address used for the VIP.
	// This is critical for DHCP reservation - all nodes use the same MAC
	// so the router sees a consistent MAC regardless of which node holds the VIP.
	// Leave empty to auto-generate a MAC based on the VIP address.
	// Format: "02:xx:xx:xx:xx:xx" (locally administered unicast)
	VIPMACAddress string `yaml:"vipMacAddress" json:"vipMacAddress"`

	// UseMacVlan enables creating a macvlan interface for the VIP.
	// This creates a virtual interface with the VIP's dedicated MAC address.
	// Required for proper DHCP reservation on most routers.
	// Default: true (recommended for production)
	UseMacVlan bool `yaml:"useMacVlan" json:"useMacVlan"`

	// ============================================================================
	// Network Partition Detection (Multi-Path Heartbeat)
	// ============================================================================

	// EnableMultiPathHeartbeat enables HTTP-based heartbeat as a secondary path
	// alongside UDP discovery. This helps detect asymmetric network partitions.
	// Default: true (recommended for production)
	EnableMultiPathHeartbeat bool `yaml:"enableMultiPathHeartbeat" json:"enableMultiPathHeartbeat"`

	// WitnessNodes is a list of external witness node addresses for partition detection.
	// Format: ["192.168.1.50:5354", "192.168.1.51:5354"]
	// Witness nodes are queried to verify cluster state during suspected partitions.
	// Using at least one witness on a different network segment is recommended.
	WitnessNodes []string `yaml:"witnessNodes" json:"witnessNodes"`

	// PartitionDetectionThreshold is the number of failed heartbeat paths
	// required before considering the node potentially partitioned.
	// With multi-path (UDP + HTTP), a value of 2 means both must fail.
	// Default: 2 (both paths must fail to trigger partition detection)
	PartitionDetectionThreshold int `yaml:"partitionDetectionThreshold" json:"partitionDetectionThreshold"`
}

// DefaultConfig returns the default VIP configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:                     false,
		Priority:                    0, // 0 = auto-assign
		AutoPriority:                true,
		VIPNetmask:                  32,
		DiscoveryPort:               5363,
		StatusPort:                  5354,
		StratumPort:                 3333,
		HeartbeatInterval:           30 * time.Second,
		FailoverTimeout:             90 * time.Second,
		CanBecomeMaster:             true,
		GratuitousARPCount:          3,
		UseMacVlan:                  true, // Use macvlan for proper DHCP reservation
		EnableMultiPathHeartbeat:    true, // Enable HTTP heartbeat alongside UDP
		PartitionDetectionThreshold: 2,    // Both paths must fail
	}
}

// generateVIPMAC generates a deterministic MAC address from the VIP.
// Uses locally administered unicast format (02:xx:xx:xx:xx:xx).
// The last 4 bytes are derived from the VIP so all nodes generate the same MAC.
// Only IPv4 addresses are supported - IPv6 returns empty string.
func generateVIPMAC(vipAddress string) string {
	ip := net.ParseIP(vipAddress)
	if ip == nil {
		return ""
	}

	// Only IPv4 is supported for VIP MAC generation
	// IPv6 addresses are rejected as VIP is designed for local network failover
	ip4 := ip.To4()
	if ip4 == nil {
		// Not an IPv4 address (could be IPv6)
		return ""
	}

	// Locally administered unicast: bit 1 of first byte = 1, bit 0 = 0
	// Format: 02:53:AA:BB:CC:DD where AA.BB.CC.DD is the VIP
	// "53" = 0x53 (S for Spiral) - Spiral Pool identifier
	return fmt.Sprintf("02:53:%02x:%02x:%02x:%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}

// Message types for cluster communication.
const (
	MsgTypeHeartbeat   = "heartbeat"
	MsgTypeAnnounce    = "announce"
	MsgTypeElection    = "election"
	MsgTypeVIPAcquired = "vip_acquired"
	MsgTypeVIPReleased = "vip_released"
	MsgTypeJoinRequest = "join_request"
	MsgTypeJoinAccept  = "join_accept"
	MsgTypeJoinReject  = "join_reject"
)

// SpiralClusterUserAgentPrefix is the required prefix for Spiral Pool HA nodes.
// All cluster messages must have a user-agent starting with this prefix.
// The version number after the prefix can vary (e.g., "SpiralPool-HA-1.0.0", "SpiralPool-HA-1.0.1").
// Using "-" instead of "/" to avoid potential escape character issues in URLs and parsers.
const SpiralClusterUserAgentPrefix = "SpiralPool-HA-"

// SpiralPoolVersion is the current version of Spiral Pool (semver format).
// This is a var (not const) so it can be overridden via -ldflags at build time:
//   -X github.com/spiralpool/stratum/internal/ha.SpiralPoolVersion=X.Y.Z
var SpiralPoolVersion = "2.2.1"

// SpiralClusterUserAgent is the current version's user-agent string.
// This is used when sending messages; validation checks for the prefix only.
// Reinitialized in init() to pick up ldflags-injected SpiralPoolVersion.
var SpiralClusterUserAgent = SpiralClusterUserAgentPrefix + SpiralPoolVersion

func init() {
	// Reinitialize after ldflags injection of SpiralPoolVersion
	SpiralClusterUserAgent = SpiralClusterUserAgentPrefix + SpiralPoolVersion
}

// ClusterMessage is the UDP message format for cluster communication.
type ClusterMessage struct {
	Type             string    `json:"type"`
	NodeID           string    `json:"nodeId"`
	ClusterToken     string    `json:"clusterToken"`
	UserAgent        string    `json:"userAgent"` // Must start with SpiralClusterUserAgentPrefix
	Timestamp        time.Time `json:"timestamp"`
	Role             Role      `json:"role"`
	Priority         int       `json:"priority"`
	AssignedPriority int       `json:"assignedPriority,omitempty"` // Priority assigned by master to joining node
	VIPAddress       string    `json:"vipAddress,omitempty"`
	StratumPort      int       `json:"stratumPort"`           // Primary stratum port (for backwards compat)
	ClusterSize      int       `json:"clusterSize,omitempty"` // Current number of nodes in cluster
	Data             string    `json:"data,omitempty"`        // Additional JSON data

	// Multi-coin port support (V2)
	CoinPorts map[string]*CoinPortInfo `json:"coinPorts,omitempty"` // Per-coin stratum ports (key: coin symbol)

	// Coin awareness fields - included in heartbeats for failover decisions
	SupportedCoins []string                   `json:"supportedCoins,omitempty"` // List of coin tickers this node supports
	CoinSyncStatus map[string]*CoinSyncStatus `json:"coinSyncStatus,omitempty"` // Sync status per coin

	// Pool payout addresses - validated across cluster for failover safety
	PoolAddresses map[string]string `json:"poolAddresses,omitempty"` // Key: coin ticker, Value: payout address
}

// VIPManager manages the Virtual IP and cluster coordination.
type VIPManager struct {
	config Config
	logger *zap.SugaredLogger

	mu           sync.RWMutex
	role         Role
	state        State
	nodes        map[string]*ClusterNode
	masterID     string
	clusterToken string
	localNode    *ClusterNode

	// Atomic flags for thread-safe access without mutex
	hasVIP atomic.Bool

	// Network
	discoveryConn *net.UDPConn
	statusServer  net.Listener

	// Rate limiting for security
	udpRateLimiter      *rateLimiter
	udpPerIPLimiters    sync.Map // map[string]*rateLimiter — per-IP UDP rate limiters
	httpRateLimiter     *rateLimiter
	announceRateLimiter *ipRateLimiter // Per-IP rate limiting for announce responses (UDP amplification prevention)
	electionCooldown    atomic.Int64   // Unix timestamp of last election

	// Anti-brute-force: track failed auth attempts per IP
	failedAuthMu    sync.RWMutex
	failedAuthCount map[string]*authFailure // IP -> failure count
	blacklistedIPs  map[string]time.Time    // IP -> blacklist expiry

	// SECURITY: Anti-replay protection - track seen message hashes
	seenMessagesMu sync.Mutex
	seenMessages   map[string]time.Time // message hash -> first seen time

	// Lifecycle
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	started       atomic.Bool
	failoverCount atomic.Int64

	// VIP Flap Detection & Exponential Backoff
	flapDetector *flapDetector

	// Multi-path heartbeat for network partition detection
	httpHeartbeatClient *http.Client
	partitionDetector   *partitionDetector
	witnessNodes        []string // External witness node addresses for partition detection

	// Network topology (detected at startup, used for broadcast computation)
	interfaceNetmask int // Actual interface CIDR (e.g., 24 for /24 LAN), distinct from VIPNetmask

	// Multi-coin support
	coinPorts map[string]*CoinPortInfo // Per-coin stratum ports (key: coin symbol)

	// Callbacks
	onRoleChange       func(oldRole, newRole Role)
	onVIPAcquired      func(vip string)
	onVIPReleased      func(vip string)
	onNodeJoined       func(node *ClusterNode)
	onNodeLeft         func(node *ClusterNode)
	onDatabaseFailover func(isMaster bool) // Called when database should failover based on VIP role

	// Pool address validation for HA failover safety
	localPoolAddresses map[string]string                                      // coin ticker -> payout address
	onAddressMismatch  func(coin, localAddr, remoteAddr, remoteNodeID string) // Called when addresses don't match
	addressMismatchLog map[string]time.Time                                   // Tracks logged mismatches to avoid spam

	// Resolved binary paths for VIP management (found at startup via LookPath)
	ipBin     string // Absolute path to 'ip' command (e.g., /usr/sbin/ip)
	arpingBin string // Absolute path to 'arping' command (e.g., /usr/sbin/arping)

	// Prometheus metrics (optional, nil = disabled)
	metrics haMetrics
}

// haMetrics is a minimal interface for HA metric reporting to avoid import cycles.
type haMetrics interface {
	IncHAPartitionDetected()
}

// SetHAMetrics sets the Prometheus metrics interface for HA observability.
func (vm *VIPManager) SetHAMetrics(m haMetrics) {
	vm.metrics = m
	if vm.partitionDetector != nil {
		vm.partitionDetector.metrics = m
	}
}

// SetCoinPorts configures the per-coin stratum ports for multi-coin VIP routing.
// This should be called before Start() to ensure cluster messages include port info.
// Example: vm.SetCoinPorts(map[string]*CoinPortInfo{
//
//	"DGB": {StratumV1: 3333, StratumV2: 3334, StratumTLS: 3335},
//	"BTC": {StratumV1: 4333, StratumV2: 4334, StratumTLS: 4335},
//
// })
func (vm *VIPManager) SetCoinPorts(ports map[string]*CoinPortInfo) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.coinPorts = ports

	// Also update local node if it exists
	if vm.localNode != nil {
		vm.localNode.CoinPorts = ports
	}

	vm.logger.Infow("Multi-coin ports configured",
		"coins", len(ports),
	)
}

// GetCoinPorts returns a deep copy of the configured per-coin stratum ports.
func (vm *VIPManager) GetCoinPorts() map[string]*CoinPortInfo {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	cp := make(map[string]*CoinPortInfo, len(vm.coinPorts))
	for k, v := range vm.coinPorts {
		vCopy := *v
		cp[k] = &vCopy
	}
	return cp
}

// SetPoolAddresses configures the per-coin payout addresses for HA validation.
// This MUST be called before Start() to ensure addresses are validated across the cluster.
// CRITICAL: All HA nodes MUST have identical pool addresses or payouts will be split!
//
//	Example: vm.SetPoolAddresses(map[string]string{
//	    "DGB": "DPb5WkZ...",
//	    "BTC": "bc1q...",
//	})
func (vm *VIPManager) SetPoolAddresses(addresses map[string]string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.localPoolAddresses = addresses

	// Initialize mismatch log if needed
	if vm.addressMismatchLog == nil {
		vm.addressMismatchLog = make(map[string]time.Time)
	}

	// Also update local node if it exists
	if vm.localNode != nil {
		vm.localNode.PoolAddresses = addresses
	}

	vm.logger.Infow("Pool payout addresses configured for HA validation",
		"coins", len(addresses),
	)
}

// SetAddressMismatchHandler sets the callback for pool address mismatches.
// This is called when another node in the cluster has a different payout address
// for the same coin, which would cause split payouts during failover.
func (vm *VIPManager) SetAddressMismatchHandler(handler func(coin, localAddr, remoteAddr, remoteNodeID string)) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.onAddressMismatch = handler
}

// validatePoolAddresses checks if the remote node's pool addresses match local config.
// Logs warnings and triggers callback for any mismatches.
func (vm *VIPManager) validatePoolAddresses(remoteNodeID string, remoteAddresses map[string]string) {
	vm.mu.RLock()
	localAddrs := vm.localPoolAddresses
	handler := vm.onAddressMismatch
	vm.mu.RUnlock()

	if localAddrs == nil || remoteAddresses == nil {
		return
	}

	now := time.Now()
	mismatchCooldown := 5 * time.Minute // Only log each mismatch once per 5 minutes

	for coin, remoteAddr := range remoteAddresses {
		localAddr, exists := localAddrs[coin]
		if !exists {
			continue // Remote has a coin we don't support - not a mismatch
		}

		if localAddr != remoteAddr {
			// Check cooldown to avoid log spam
			key := coin + ":" + remoteNodeID
			vm.mu.Lock()
			if vm.addressMismatchLog == nil {
				vm.addressMismatchLog = make(map[string]time.Time)
			}
			lastLogged := vm.addressMismatchLog[key]
			if now.Sub(lastLogged) > mismatchCooldown {
				vm.addressMismatchLog[key] = now
				vm.mu.Unlock()

				// Log the mismatch with clear warning
				vm.logger.Errorw("CRITICAL: Pool address mismatch detected!",
					"coin", coin,
					"localAddress", localAddr,
					"remoteAddress", remoteAddr,
					"remoteNodeID", remoteNodeID,
					"warning", "Failover will cause SPLIT PAYOUTS - blocks mined by different nodes will pay to different addresses!",
				)

				// Trigger callback if registered
				if handler != nil {
					go handler(coin, localAddr, remoteAddr, remoteNodeID)
				}
			} else {
				vm.mu.Unlock()
			}
		}
	}
}

// GetPortForCoin returns the stratum V1 port for a specific coin.
// Returns 0 if the coin is not configured.
func (vm *VIPManager) GetPortForCoin(coin string) int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	if ports, ok := vm.coinPorts[coin]; ok {
		return ports.StratumV1
	}
	return 0
}

// authFailure tracks authentication failures for an IP.
type authFailure struct {
	count     int
	firstFail time.Time
	lastFail  time.Time
}

// Security constants for anti-brute-force protection
const (
	maxAuthFailures      = 5               // Max failures before blacklist
	authFailureWindow    = 5 * time.Minute // Window for counting failures
	blacklistDuration    = 30 * time.Minute
	blacklistCleanupFreq = 5 * time.Minute
	maxClusterSize       = 10 // Maximum nodes in a cluster (prevents resource exhaustion)
)

// rateLimiter provides simple rate limiting using a token bucket algorithm.
type rateLimiter struct {
	mu         sync.Mutex
	tokens     int
	maxTokens  int
	refillRate int // tokens per second
	lastRefill time.Time
}

// newRateLimiter creates a new rate limiter.
func newRateLimiter(maxTokens, refillRate int) *rateLimiter {
	return &rateLimiter{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// allowUDPFromIP applies per-IP rate limiting for UDP messages.
// Each IP gets its own rate limiter (20 burst, 10/sec) to prevent one noisy IP
// from exhausting the global rate limit and dropping legitimate cluster messages.
func (vm *VIPManager) allowUDPFromIP(ip string) bool {
	val, _ := vm.udpPerIPLimiters.LoadOrStore(ip, newRateLimiter(20, 10))
	return val.(*rateLimiter).allow()
}

// cleanupStaleUDPLimiters evicts per-IP rate limiters that haven't been used
// in over 5 minutes. This prevents unbounded sync.Map growth from spoofed IPs.
func (vm *VIPManager) cleanupStaleUDPLimiters() {
	cutoff := time.Now().Add(-5 * time.Minute)
	vm.udpPerIPLimiters.Range(func(key, value any) bool {
		rl := value.(*rateLimiter)
		rl.mu.Lock()
		lastUsed := rl.lastRefill
		rl.mu.Unlock()
		if lastUsed.Before(cutoff) {
			vm.udpPerIPLimiters.Delete(key)
		}
		return true
	})
}

// allow checks if an operation is allowed under the rate limit.
func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(r.lastRefill)
	tokensToAdd := int(elapsed.Seconds() * float64(r.refillRate))
	if tokensToAdd > 0 {
		r.tokens = min(r.maxTokens, r.tokens+tokensToAdd)
		r.lastRefill = now
	}

	// Check if we have tokens available
	if r.tokens > 0 {
		r.tokens--
		return true
	}
	return false
}

// ipRateLimiter provides per-IP rate limiting to prevent UDP amplification attacks.
// Each IP has its own token bucket that resets after the window expires.
// SECURITY: Implements LRU-style eviction to prevent unbounded memory growth.
type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
}

// IP rate limiter constants for memory management
const (
	maxIPBuckets      = 10000 // Maximum IPs to track before forced eviction
	ipBucketSoftLimit = 5000  // Soft limit that triggers cleanup
	ipCleanupInterval = 100   // Check cleanup every N new IPs
)

// ipBucket tracks rate limit state for a single IP.
type ipBucket struct {
	count       int
	windowStart time.Time
	lastAccess  time.Time // For LRU eviction
}

// newIPRateLimiter creates a new per-IP rate limiter.
func newIPRateLimiter() *ipRateLimiter {
	return &ipRateLimiter{
		buckets: make(map[string]*ipBucket),
	}
}

// allowIP checks if an operation from the given IP is allowed.
// maxCount is the maximum number of operations allowed within the window duration.
// SECURITY: Includes LRU eviction to prevent memory exhaustion attacks.
func (r *ipRateLimiter) allowIP(ip string, maxCount int, window time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	bucket, exists := r.buckets[ip]

	if !exists || now.Sub(bucket.windowStart) > window {
		// New IP or window expired - create fresh bucket
		r.buckets[ip] = &ipBucket{
			count:       1,
			windowStart: now,
			lastAccess:  now,
		}

		// SECURITY: LRU-style cleanup to prevent memory exhaustion
		// Cleanup triggers at soft limit to avoid O(n) cleanup on every request
		if len(r.buckets) > ipBucketSoftLimit && len(r.buckets)%ipCleanupInterval == 0 {
			r.cleanupLocked(now, window)
		}

		// Hard limit: force aggressive cleanup if too many entries
		if len(r.buckets) > maxIPBuckets {
			r.forceEvictionLocked(now)
		}

		return true
	}

	// Update last access time for LRU
	bucket.lastAccess = now

	if bucket.count >= maxCount {
		return false
	}

	bucket.count++
	return true
}

// cleanupLocked removes expired entries. Must be called with mutex held.
func (r *ipRateLimiter) cleanupLocked(now time.Time, window time.Duration) {
	// Remove entries older than 2x window (expired entries)
	for ip, b := range r.buckets {
		if now.Sub(b.windowStart) > window*2 {
			delete(r.buckets, ip)
		}
	}
}

// forceEvictionLocked aggressively removes oldest entries to stay under limit.
// Must be called with mutex held. Uses LRU-style eviction.
func (r *ipRateLimiter) forceEvictionLocked(now time.Time) {
	// Target: reduce to 80% of soft limit
	targetSize := ipBucketSoftLimit * 80 / 100
	toRemove := len(r.buckets) - targetSize

	if toRemove <= 0 {
		return
	}

	// Find and remove oldest entries by lastAccess time
	// For efficiency, we use a simple approach: remove entries older than median
	var oldestTime time.Time
	for _, b := range r.buckets {
		if oldestTime.IsZero() || b.lastAccess.Before(oldestTime) {
			oldestTime = b.lastAccess
		}
	}

	// Remove entries in the oldest 50% by time
	cutoff := now.Sub(oldestTime) / 2
	cutoffTime := now.Add(-cutoff)

	removed := 0
	for ip, b := range r.buckets {
		if b.lastAccess.Before(cutoffTime) {
			delete(r.buckets, ip)
			removed++
			if removed >= toRemove {
				break
			}
		}
	}
}

// generateSecureToken generates a cryptographically secure cluster token.
func generateSecureToken() (string, error) {
	bytes := make([]byte, 32) // 256-bit token for AES-256
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate secure token: %w", err)
	}
	return "spiral-" + hex.EncodeToString(bytes), nil
}

// constantTimeCompare performs a constant-time string comparison to prevent timing attacks.
func constantTimeCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// isValidNodeID validates that a node ID has a valid format.
// Valid formats: hostname-timestamp (e.g., "server1-1234567890") or UUID-like strings.
// Must be 3-64 characters, alphanumeric with hyphens allowed.
func isValidNodeID(nodeID string) bool {
	if len(nodeID) < 3 || len(nodeID) > 64 {
		return false
	}
	// Allow alphanumeric characters, hyphens, and underscores
	for _, c := range nodeID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	// Must not start or end with hyphen/underscore
	if nodeID[0] == '-' || nodeID[0] == '_' ||
		nodeID[len(nodeID)-1] == '-' || nodeID[len(nodeID)-1] == '_' {
		return false
	}
	return true
}

// isValidInterfaceName validates a Linux network interface name.
// Valid names: 1-15 characters, alphanumeric with dots/hyphens/underscores.
// Examples: eth0, ens192, enp0s3, wlan0, br-abc123
func isValidInterfaceName(ifaceName string) bool {
	if len(ifaceName) < 1 || len(ifaceName) > 15 { // Linux IFNAMSIZ = 16 (includes null)
		return false
	}
	// Allow alphanumeric characters, hyphens, underscores, and dots
	for _, c := range ifaceName {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	// Cannot start with hyphen or dot
	if ifaceName[0] == '-' || ifaceName[0] == '.' {
		return false
	}
	return true
}

// isValidMACAddress validates a MAC address string for command injection safety.
// Accepts format: "XX:XX:XX:XX:XX:XX" where X is [0-9A-Fa-f]
func isValidMACAddress(mac string) bool {
	if len(mac) != 17 {
		return false
	}
	for i, c := range mac {
		if (i+1)%3 == 0 {
			// Should be colon at positions 2, 5, 8, 11, 14
			if c != ':' {
				return false
			}
		} else {
			// Should be hex digit
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// isVIPInUse checks if the VIP is currently responding on the stratum port.
// This provides split-brain prevention by detecting if another node already holds the VIP.
// Returns true if the VIP appears to be in use by another node.
func (vm *VIPManager) isVIPInUse() bool {
	if vm.config.VIPAddress == "" {
		return false
	}

	// Layer-2 check via arping (if available on Linux) — catches VIP assigned
	// to another host even if stratum isn't listening yet.
	// arping -D (DAD mode) exit codes (iputils): 0 = no conflict, 1 = duplicate detected.
	if runtime.GOOS == "linux" {
		arpOut, err := exec.Command(vm.arpingBin, "-D", "-c", "2", "-w", "2",
			"-I", vm.config.VIPInterface, vm.config.VIPAddress).CombinedOutput()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				vm.logger.Warnw("VIP in use detected via ARP probe",
					"vip", vm.config.VIPAddress,
					"output", strings.TrimSpace(string(arpOut)))
				return true
			}
			// Exit code 2 or command error (not found, bad interface) — fall through to TCP
			vm.logger.Warnw("arping probe failed — falling back to TCP-only split-brain detection",
				"error", err, "vip", vm.config.VIPAddress)
		}
		// Exit 0 = no duplicate detected — fall through to TCP check
	}

	// Check if the VIP is assigned to the local interface (via keepalived).
	// If so, any TCP connection to VIP:port would connect to our own stratum,
	// not another node. Skip the TCP check to avoid false positive.
	if iface, err := net.InterfaceByName(vm.config.VIPInterface); err == nil {
		if addrs, err := iface.Addrs(); err == nil {
			vipIP := net.ParseIP(vm.config.VIPAddress)
			for _, a := range addrs {
				if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP.Equal(vipIP) {
					// VIP is on our interface — not another node
					return false
				}
			}
		}
	}

	// Layer-4 check: Try to connect to the VIP on the stratum port
	addr := net.JoinHostPort(vm.config.VIPAddress, fmt.Sprintf("%d", vm.config.StratumPort))
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		// Connection failed - VIP not in use or not responding
		return false
	}
	_ = conn.Close() // #nosec G104

	vm.logger.Warnw("VIP appears to be in use by another node",
		"vip", vm.config.VIPAddress,
		"port", vm.config.StratumPort,
	)
	return true
}

// checkRemoteMasterExists queries all known nodes via HTTP to see if any report being master.
// FIX H-1: This is the second verification layer in the split-brain prevention.
// Called with vm.mu held (locked) — we only read vm.nodes snapshot then release for HTTP.
func (vm *VIPManager) checkRemoteMasterExists() bool {
	// Snapshot nodes while holding lock (caller holds vm.mu)
	type nodeInfo struct {
		id   string
		host string
	}
	nodes := make([]nodeInfo, 0, len(vm.nodes))
	for id, node := range vm.nodes {
		if id != vm.config.NodeID {
			nodes = append(nodes, nodeInfo{id: id, host: node.Host})
		}
	}

	if len(nodes) == 0 {
		return false
	}

	// Use a short timeout — we're in the election path
	client := &http.Client{Timeout: 2 * time.Second}

	// Temporarily release the lock for HTTP calls (caller expects re-lock)
	vm.mu.Unlock()
	defer vm.mu.Lock()

	for _, node := range nodes {
		url := fmt.Sprintf("http://%s:%d/status", node.host, vm.config.StatusPort)
		resp, err := client.Get(url)
		if err != nil {
			continue // Node unreachable — skip, don't block on partition
		}

		var statusResp struct {
			LocalRole string `json:"localRole"`
			MasterID  string `json:"masterId"`
			State     string `json:"state"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&statusResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		// Direct check: remote node IS the master
		if statusResp.LocalRole == "MASTER" {
			vm.logger.Warnw("Remote node reports MASTER status during election",
				"remoteNode", node.id,
				"remoteHost", node.host,
			)
			return true
		}

		// Indirect check: remote node is in stable "running" state and believes
		// a DIFFERENT node is master. This indicates an asymmetric partition —
		// the remote node can still see a master that we cannot reach.
		// Only trigger when state is "running" (not "election"/"failover") to
		// avoid blocking elections when all nodes agree the master is gone.
		if statusResp.MasterID != "" &&
			statusResp.MasterID != vm.config.NodeID &&
			statusResp.State == "running" {
			vm.logger.Warnw("ASYMMETRIC PARTITION DETECTED: reachable node believes a different master is active",
				"remoteNode", node.id,
				"remoteHost", node.host,
				"remoteBelievedMaster", statusResp.MasterID,
				"remoteState", statusResp.State,
			)
			return true
		}
	}

	return false
}

// deriveEncryptionKey derives a 256-bit AES key from the cluster token using HKDF.
func deriveEncryptionKey(clusterToken string) ([]byte, error) {
	if clusterToken == "" {
		return nil, errors.New("cluster token is empty")
	}

	// Use SHA-256 for HKDF
	hash := sha256.New
	// Salt with a fixed value specific to Spiral Pool VIP
	salt := []byte("spiral-pool-vip-cluster-v1")
	// Info context for key derivation
	info := []byte("aes-256-gcm-encryption")

	// Create HKDF reader
	hkdfReader := hkdf.New(hash, []byte(clusterToken), salt, info)

	// Read 32 bytes for AES-256
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("failed to derive encryption key: %w", err)
	}

	return key, nil
}

// encryptMessage encrypts a cluster message using AES-256-GCM.
// Returns base64-encoded ciphertext with nonce prepended.
func encryptMessage(plaintext []byte, clusterToken string) (string, error) {
	key, err := deriveEncryptionKey(clusterToken)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and prepend nonce
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	// Return base64-encoded result
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptMessage decrypts a cluster message using AES-256-GCM.
// Expects base64-encoded ciphertext with nonce prepended.
func decryptMessage(encrypted string, clusterToken string) ([]byte, error) {
	key, err := deriveEncryptionKey(clusterToken)
	if err != nil {
		return nil, err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}

// EncryptedMessage wraps an encrypted cluster message for transport.
type EncryptedMessage struct {
	Version   int    `json:"v"` // Protocol version for future compatibility
	Encrypted string `json:"e"` // Base64-encoded encrypted payload
	NodeID    string `json:"n"` // Sender node ID (unencrypted for routing)
	Timestamp int64  `json:"t"` // Unix timestamp (for replay protection)
}

// NewVIPManager creates a new VIP manager.
func NewVIPManager(cfg Config, logger *zap.Logger) (*VIPManager, error) {
	if cfg.DiscoveryPort == 0 {
		cfg.DiscoveryPort = 5363
	}
	if cfg.StatusPort == 0 {
		cfg.StatusPort = 5354
	}
	if cfg.StratumPort == 0 {
		cfg.StratumPort = 3333
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.FailoverTimeout == 0 {
		cfg.FailoverTimeout = 90 * time.Second
	}
	if cfg.VIPNetmask == 0 {
		cfg.VIPNetmask = 32
	}
	if cfg.GratuitousARPCount == 0 {
		cfg.GratuitousARPCount = 3
	}
	if cfg.NodeID == "" {
		hostname, _ := os.Hostname()
		cfg.NodeID = fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano()%10000)
	}

	// Validate heartbeat/failover ratio: FailoverTimeout must be >= 3x HeartbeatInterval
	// to avoid false failovers caused by transient network delays or GC pauses.
	minFailover := 3 * cfg.HeartbeatInterval
	if cfg.FailoverTimeout < minFailover {
		logger.Sugar().Warnw("FailoverTimeout is too close to HeartbeatInterval, adjusting to safe minimum",
			"configured_failover", cfg.FailoverTimeout,
			"configured_heartbeat", cfg.HeartbeatInterval,
			"adjusted_failover", minFailover,
		)
		cfg.FailoverTimeout = minFailover
	}

	sugaredLogger := logger.Sugar().Named("ha-vip")

	vm := &VIPManager{
		config:              cfg,
		logger:              sugaredLogger,
		nodes:               make(map[string]*ClusterNode),
		role:                RoleUnknown,
		state:               StateInitializing,
		clusterToken:        cfg.ClusterToken,        // Pre-shared token from config (required for backup nodes)
		udpRateLimiter:      newRateLimiter(100, 50), // 100 burst, 50/sec refill
		httpRateLimiter:     newRateLimiter(20, 10),  // 20 burst, 10/sec refill
		announceRateLimiter: newIPRateLimiter(),      // Per-IP rate limiting for announce responses
		failedAuthCount:     make(map[string]*authFailure),
		blacklistedIPs:      make(map[string]time.Time),
		seenMessages:        make(map[string]time.Time), // SECURITY: Anti-replay protection
		// Initialize flap detector with default config (uses FailoverTimeout as base)
		flapDetector: newFlapDetector(FlapDetectorConfig{
			FlapWindow:        5 * time.Minute,
			FlapThreshold:     3,
			BaseTimeout:       cfg.FailoverTimeout,
			MaxTimeout:        15 * time.Minute,
			BackoffMultiplier: 2.0,
		}, sugaredLogger),
		// HTTP client for multi-path heartbeat
		httpHeartbeatClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		// Store witness nodes from config
		witnessNodes: cfg.WitnessNodes,
	}

	// SECURITY: Validate witness node addresses to prevent SSRF via config.
	// Reject loopback, link-local (cloud metadata 169.254.x.x), and malformed addresses.
	validatedWitness := make([]string, 0, len(cfg.WitnessNodes))
	for _, addr := range cfg.WitnessNodes {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			sugaredLogger.Warnw("Ignoring invalid witness address (not host:port)", "address", addr)
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			sugaredLogger.Warnw("Ignoring witness address with non-IP host", "address", addr)
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			sugaredLogger.Warnw("Ignoring witness address pointing to loopback/link-local/unspecified",
				"address", addr, "ip", ip.String())
			continue
		}
		validatedWitness = append(validatedWitness, addr)
	}
	vm.witnessNodes = validatedWitness
	cfg.WitnessNodes = validatedWitness

	// Initialize partition detector for multi-path heartbeat
	vm.partitionDetector = newPartitionDetector(cfg, sugaredLogger, vm.httpHeartbeatClient)

	// Auto-assign priority if enabled and priority is 0
	if cfg.AutoPriority && cfg.Priority == 0 {
		cfg.Priority = 100 // Default priority for master
	}

	// Validate priority bounds (anti-abuse)
	if cfg.Priority < 100 {
		logger.Sugar().Warnw("Priority below minimum 100, adjusting to 100", "requested", cfg.Priority)
		cfg.Priority = 100
	}
	if cfg.Priority > 999 {
		logger.Sugar().Warnw("Priority above maximum 999, adjusting to 999", "requested", cfg.Priority)
		cfg.Priority = 999
	}

	// Sync clamped priority back to vm.config (which was set before clamping)
	vm.config.Priority = cfg.Priority

	// Create local node representation
	vm.localNode = &ClusterNode{
		ID:          cfg.NodeID,
		Priority:    cfg.Priority,
		StratumPort: cfg.StratumPort,
		IsHealthy:   true,
		JoinedAt:    time.Now(),
		LastSeen:    time.Now(),
	}

	// Initialize ctx so callers that bypass Start() (e.g. tests) don't hit
	// nil-pointer panics in code paths that reference vm.ctx.
	// Start() replaces this with the caller-provided context.
	vm.ctx, vm.cancel = context.WithCancel(context.Background())

	// Resolve binary paths at startup (fail-fast if not found).
	// systemd's default PATH includes /usr/sbin, but we also check common
	// locations as a fallback in case PATH is restricted or non-standard.
	var err error
	vm.ipBin, err = exec.LookPath("ip")
	if err != nil {
		for _, p := range []string{"/usr/sbin/ip", "/sbin/ip", "/usr/bin/ip"} {
			if _, serr := os.Stat(p); serr == nil {
				vm.ipBin = p
				break
			}
		}
		if vm.ipBin == "" {
			return nil, fmt.Errorf("'ip' command not found in PATH or /usr/sbin, /sbin, /usr/bin — install iproute2")
		}
	}
	vm.arpingBin, err = exec.LookPath("arping")
	if err != nil {
		for _, p := range []string{"/usr/sbin/arping", "/sbin/arping", "/usr/bin/arping"} {
			if _, serr := os.Stat(p); serr == nil {
				vm.arpingBin = p
				break
			}
		}
		if vm.arpingBin == "" {
			// Non-fatal: split-brain detection degrades but VIP management still works
			sugaredLogger.Warnw("'arping' not found — split-brain detection and gratuitous ARP disabled. Install iputils-arping.",
				"searched", []string{"PATH", "/usr/sbin/arping", "/sbin/arping", "/usr/bin/arping"},
			)
			vm.arpingBin = "arping" // Will fail at runtime with clear exec error
		}
	}
	sugaredLogger.Infow("VIP binary paths resolved", "ip", vm.ipBin, "arping", vm.arpingBin)

	return vm, nil
}

// Start begins the VIP manager.
func (vm *VIPManager) Start(ctx context.Context) error {
	if !vm.config.Enabled {
		vm.logger.Info("HA/VIP mode disabled")
		return nil
	}

	if vm.started.Load() {
		return errors.New("VIP manager already started")
	}

	vm.ctx, vm.cancel = context.WithCancel(ctx)

	// Get local IP and interface info
	if err := vm.detectNetworkConfig(); err != nil {
		return fmt.Errorf("failed to detect network config: %w", err)
	}

	// FIX VIP-H3: Clean up orphaned VIP interfaces from a previous crash.
	// If the process died while holding the VIP, the macvlan interface and/or
	// IP assignment may still be present, causing duplicate VIP on the network.
	vm.cleanupOrphanedVIP()

	// Start discovery listener
	if err := vm.startDiscoveryListener(); err != nil {
		return fmt.Errorf("failed to start discovery listener: %w", err)
	}

	// Start status HTTP server
	if err := vm.startStatusServer(); err != nil {
		_ = vm.discoveryConn.Close() // #nosec G104
		return fmt.Errorf("failed to start status server: %w", err)
	}

	vm.started.Store(true)

	// Start background goroutines
	goroutineCount := 5
	if vm.config.EnableMultiPathHeartbeat {
		goroutineCount++ // Add HTTP heartbeat loop
	}
	vm.wg.Add(goroutineCount)
	go vm.discoveryLoop()
	go vm.heartbeatLoop()
	go vm.healthCheckLoop()
	go vm.blacklistCleanupLoop()
	go vm.flapBackoffResetLoop()

	// Start multi-path heartbeat if enabled
	if vm.config.EnableMultiPathHeartbeat {
		go vm.httpHeartbeatLoop()
	}

	vm.logger.Infow("VIP manager started",
		"nodeId", vm.config.NodeID,
		"vip", vm.config.VIPAddress,
		"interface", vm.config.VIPInterface,
		"discoveryPort", vm.config.DiscoveryPort,
		"statusPort", vm.config.StatusPort,
		"multiPathHeartbeat", vm.config.EnableMultiPathHeartbeat,
		"witnessNodes", len(vm.witnessNodes),
	)

	// Try to become master or find existing master
	vm.wg.Add(1)
	go vm.initializeCluster()

	return nil
}

// Stop gracefully shuts down the VIP manager.
func (vm *VIPManager) Stop() error {
	if !vm.started.Load() {
		return nil
	}

	vm.logger.Info("Stopping VIP manager...")

	// Release VIP if we hold it
	if vm.hasVIP.Load() {
		if err := vm.releaseVIP(); err != nil {
			vm.logger.Warnw("Failed to release VIP during shutdown", "error", err)
		}
	}

	// Announce departure to the cluster. Skip broadcast if releaseVIP() above
	// already broadcast VIPReleased (on Linux when VIP was held on a real interface).
	// On non-Linux or for non-master roles, releaseVIP doesn't broadcast, so we do it here.
	if vm.role != RoleMaster || runtime.GOOS != "linux" {
		vm.broadcastMessage(ClusterMessage{
			Type:         MsgTypeVIPReleased,
			NodeID:       vm.config.NodeID,
			ClusterToken: vm.clusterToken,
			UserAgent:    SpiralClusterUserAgent,
			Timestamp:    time.Now(),
			Role:         vm.role,
		})
	}

	vm.cancel()

	// Close network connections
	if vm.discoveryConn != nil {
		_ = vm.discoveryConn.Close() // #nosec G104
	}
	if vm.statusServer != nil {
		_ = vm.statusServer.Close() // #nosec G104
	}

	vm.wg.Wait()
	vm.started.Store(false)

	vm.logger.Info("VIP manager stopped")
	return nil
}

// detectNetworkConfig auto-detects network settings if not specified.
func (vm *VIPManager) detectNetworkConfig() error {
	// Get all network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("failed to get interfaces: %w", err)
	}

	var selectedIface *net.Interface
	var selectedIP net.IP
	var selectedMask net.IPMask

	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		// If interface is specified, match it
		if vm.config.VIPInterface != "" && iface.Name != vm.config.VIPInterface {
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

			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}

			// Skip link-local
			if ip4[0] == 169 && ip4[1] == 254 {
				continue
			}

			selectedIface = &iface
			selectedIP = ip4
			selectedMask = ipNet.Mask
			break
		}

		if selectedIface != nil {
			break
		}
	}

	if selectedIface == nil {
		return errors.New("no suitable network interface found")
	}

	// Update local node
	vm.localNode.Host = selectedIP.String()
	vm.localNode.Port = vm.config.DiscoveryPort

	// Set interface if not specified
	if vm.config.VIPInterface == "" {
		vm.config.VIPInterface = selectedIface.Name
	}

	// Store actual interface netmask for broadcast computation (distinct from VIP netmask)
	ones, _ := selectedMask.Size()
	vm.interfaceNetmask = ones

	// Generate VIP if not specified (use .200 in the same subnet)
	if vm.config.VIPAddress == "" {
		// Generate VIP as .200 in the subnet
		vip := make(net.IP, 4)
		copy(vip, selectedIP.Mask(selectedMask))
		vip[3] = 200 // .200 is a common choice for VIPs
		vm.config.VIPAddress = vip.String()
		// VIPNetmask stays at configured value (default /32) — do NOT override with
		// interface netmask. Using /24 here would create a duplicate subnet route
		// on the same interface, breaking outbound connectivity.
	}

	vm.logger.Infow("Network configuration detected",
		"interface", vm.config.VIPInterface,
		"localIP", selectedIP.String(),
		"vip", vm.config.VIPAddress,
		"netmask", vm.config.VIPNetmask,
		"interfaceNetmask", vm.interfaceNetmask,
	)

	return nil
}

// startDiscoveryListener starts the UDP listener for cluster discovery.
func (vm *VIPManager) startDiscoveryListener() error {
	addr := &net.UDPAddr{
		Port: vm.config.DiscoveryPort,
		IP:   net.IPv4zero,
	}

	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP port %d: %w", vm.config.DiscoveryPort, err)
	}

	vm.discoveryConn = conn
	return nil
}

// startStatusServer starts the HTTP server for status queries.
func (vm *VIPManager) startStatusServer() error {
	addr := fmt.Sprintf(":%d", vm.config.StatusPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on TCP port %d: %w", vm.config.StatusPort, err)
	}

	vm.statusServer = listener

	// Start HTTP handler in background
	vm.wg.Add(1)
	go vm.serveStatus()

	return nil
}

// serveStatus handles HTTP requests for cluster status.
// Limits concurrent connections to prevent resource exhaustion.
func (vm *VIPManager) serveStatus() {
	defer vm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			vm.logger.Errorw("PANIC recovered in serveStatus", "panic", r)
		}
	}()

	// Semaphore to bound concurrent status handler goroutines
	const maxConcurrentStatusConns = 64
	sem := make(chan struct{}, maxConcurrentStatusConns)

	for {
		conn, err := vm.statusServer.Accept()
		if err != nil {
			select {
			case <-vm.ctx.Done():
				return
			default:
				continue
			}
		}

		// Try to acquire semaphore slot; reject if at capacity
		select {
		case sem <- struct{}{}:
		default:
			vm.logger.Warnw("Status connection limit reached, rejecting",
				"maxConcurrent", maxConcurrentStatusConns)
			_ = conn.Close() // #nosec G104
			continue
		}

		vm.wg.Add(1)
		go func() {
			defer vm.wg.Done()
			defer func() { <-sem }()
			vm.handleStatusRequest(conn)
		}()
	}
}

// handleStatusRequest processes HTTP requests for status and failover.
// NOTE: This intentionally uses raw HTTP parsing on a net.Conn (not net/http.Server)
// for the internal cluster status API. This keeps the status endpoint lightweight,
// avoids pulling in the full HTTP server machinery for a simple JSON response,
// and gives us direct control over connection deadlines and rate limiting.
func (vm *VIPManager) handleStatusRequest(conn net.Conn) {
	defer func() { _ = conn.Close() }() // #nosec G104
	defer func() {
		if r := recover(); r != nil {
			vm.logger.Errorw("PANIC recovered in handleStatusRequest", "panic", r)
			// conn.Close() is handled by the first defer above — no need to double-close
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second)) // #nosec G104

	// Rate limit HTTP requests to prevent DoS
	if !vm.httpRateLimiter.allow() {
		response := "HTTP/1.1 429 Too Many Requests\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 36\r\n" +
			"Connection: close\r\n" +
			"\r\n{\"error\":\"rate limit exceeded\"}"
		_, _ = conn.Write([]byte(response)) // #nosec G104
		return
	}

	// Read HTTP request
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return
	}

	request := string(buf[:n])

	// Parse request line to determine method and endpoint
	lines := strings.Split(request, "\r\n")
	if len(lines) == 0 {
		return
	}
	requestLine := strings.Fields(lines[0])
	if len(requestLine) < 2 {
		return
	}
	method := requestLine[0]
	path := requestLine[1]

	// Check for authentication header if cluster token is set
	// Format: Authorization: Bearer <cluster-token>
	authenticated := false
	if vm.clusterToken == "" {
		// No token configured, allow public access to basic status
		authenticated = false
	} else {
		for _, line := range lines {
			if providedToken, found := strings.CutPrefix(line, "Authorization: Bearer "); found {
				providedToken = strings.TrimSpace(providedToken)
				if constantTimeCompare(providedToken, vm.clusterToken) {
					authenticated = true
				}
				break
			}
		}
	}

	// Route based on path
	switch {
	case path == "/failover" && method == "POST":
		vm.handleFailoverRequest(conn, authenticated)
	default:
		vm.handleGetStatus(conn, authenticated)
	}
}

// handleFailoverRequest handles POST /failover to trigger an election.
func (vm *VIPManager) handleFailoverRequest(conn net.Conn, authenticated bool) {
	// Failover requires authentication
	if !authenticated {
		response := "HTTP/1.1 401 Unauthorized\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 41\r\n" +
			"Connection: close\r\n" +
			"\r\n{\"error\":\"authentication required\"}"
		_, _ = conn.Write([]byte(response)) // #nosec G104
		return
	}

	// Check if VIP manager is actually running
	if !vm.started.Load() {
		response := "HTTP/1.1 503 Service Unavailable\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 44\r\n" +
			"Connection: close\r\n" +
			"\r\n{\"error\":\"VIP manager is not running\"}"
		_, _ = conn.Write([]byte(response)) // #nosec G104
		return
	}

	// Check if this node can participate
	vm.mu.Lock()
	if !vm.config.CanBecomeMaster {
		vm.mu.Unlock()
		response := "HTTP/1.1 400 Bad Request\r\n" +
			"Content-Type: application/json\r\n" +
			"Content-Length: 49\r\n" +
			"Connection: close\r\n" +
			"\r\n{\"error\":\"this node cannot become master\"}"
		_, _ = conn.Write([]byte(response)) // #nosec G104
		return
	}

	// FIX Issue 27: Reject if election already in progress. Without this guard,
	// setting state=StateElection while a running election is in StateFailover
	// causes the running election's re-validation (vm.state != StateFailover)
	// to abort, demoting the node to BACKUP even if it was winning.
	if vm.state == StateElection || vm.state == StateFailover {
		vm.mu.Unlock()
		responseBody := `{"error":"election already in progress"}`
		response := fmt.Sprintf("HTTP/1.1 409 Conflict\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: %d\r\n"+
			"Connection: close\r\n"+
			"\r\n%s", len(responseBody), responseBody)
		_, _ = conn.Write([]byte(response)) // #nosec G104
		return
	}

	// Trigger election by setting state
	vm.state = StateElection
	vm.mu.Unlock()

	// Run election in background
	vm.wg.Add(1)
	go func() {
		defer vm.wg.Done()
		vm.runElection()
	}()

	vm.logger.Infow("Failover triggered via API", "source", "local")

	responseBody := `{"status":"failover initiated","message":"election started"}`
	response := fmt.Sprintf("HTTP/1.1 200 OK\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n"+
		"\r\n%s", len(responseBody), responseBody)
	_, _ = conn.Write([]byte(response)) // #nosec G104
}

// handleGetStatus handles GET requests for cluster status.
func (vm *VIPManager) handleGetStatus(conn net.Conn, authenticated bool) {
	// Get cluster status
	status := vm.GetStatus()

	// Determine if caller is on localhost (Sentinel, CLI)
	isLocalhost := false
	if remoteAddr := conn.RemoteAddr(); remoteAddr != nil {
		if host, _, err := net.SplitHostPort(remoteAddr.String()); err == nil {
			ip := net.ParseIP(host)
			isLocalhost = ip != nil && ip.IsLoopback()
		}
	}

	if authenticated {
		status.ClusterToken = "[authenticated]" // Indicate auth succeeded without exposing token
	} else if !isLocalhost {
		// SECURITY: Unauthenticated REMOTE callers only see basic health — no topology details.
		// Localhost callers (Sentinel, CLI) get full data without auth.
		// Strip node IPs, VIP interface, coin ports, pool addresses, and partition info.
		status.VIPInterface = ""
		status.MasterHost = ""
		status.CoinPorts = nil
		status.PartitionStatus = nil
		// Strip per-node sensitive details but keep basic node count
		for _, node := range status.Nodes {
			node.Host = ""
			node.Port = 0
			node.StratumPort = 0
			node.CoinPorts = nil
			node.PoolAddresses = nil
			node.CoinSyncStatus = nil
		}
	}

	statusJSON, err := json.Marshal(status)
	if err != nil {
		statusJSON = []byte(`{"error":"failed to marshal status"}`)
	}

	// Send HTTP response with security-hardened CORS headers
	// SECURITY (M-02): No CORS headers - VIP status API is internal only
	// If cross-origin access is needed, configure via reverse proxy with explicit allowed origins
	response := fmt.Sprintf("HTTP/1.1 200 OK\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"Cache-Control: no-cache, no-store, must-revalidate\r\n"+
		"X-Content-Type-Options: nosniff\r\n"+
		"Connection: close\r\n"+
		"\r\n%s", len(statusJSON), statusJSON)

	_, _ = conn.Write([]byte(response)) // #nosec G104
}

// GetStatus returns the current cluster status.
func (vm *VIPManager) GetStatus() ClusterStatus {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	nodes := make([]*ClusterNode, 0, len(vm.nodes))
	for _, node := range vm.nodes {
		nodes = append(nodes, node)
	}

	// Sort by priority
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Priority < nodes[j].Priority
	})

	var masterHost string
	if master, ok := vm.nodes[vm.masterID]; ok {
		masterHost = master.Host
	}

	// Get flap detection stats
	var flapStats *FlapDetectorStats
	if vm.flapDetector != nil {
		stats := vm.flapDetector.GetStats()
		flapStats = &stats
	}

	// Get partition detection status
	var partitionStatus *PartitionStatus
	if vm.partitionDetector != nil {
		status := vm.partitionDetector.GetPartitionStatus(vm.config.FailoverTimeout)
		partitionStatus = &status
	}

	return ClusterStatus{
		Enabled:         vm.config.Enabled,
		State:           vm.state.String(),
		VIP:             vm.config.VIPAddress,
		VIPInterface:    vm.config.VIPInterface,
		MasterID:        vm.masterID,
		MasterHost:      masterHost,
		LocalRole:       vm.role.String(),
		LocalID:         vm.config.NodeID,
		Nodes:           nodes,
		ClusterToken:    "", // Security: Never expose cluster token via API
		FailoverCount:   int(vm.failoverCount.Load()),
		CoinPorts:       vm.coinPorts,
		FlapStats:       flapStats,
		PartitionStatus: partitionStatus,
	}
}

// IsEnabled returns whether HA mode is enabled.
func (vm *VIPManager) IsEnabled() bool {
	return vm.config.Enabled && vm.started.Load()
}

// IsMaster returns whether this node is the current master.
func (vm *VIPManager) IsMaster() bool {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.role == RoleMaster
}

// GetVIP returns the current VIP address.
func (vm *VIPManager) GetVIP() string {
	return vm.config.VIPAddress
}

// GetClusterToken returns the cluster token.
func (vm *VIPManager) GetClusterToken() string {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.clusterToken
}

// initializeCluster runs the initial cluster discovery and election.
func (vm *VIPManager) initializeCluster() {
	defer vm.wg.Done()

	vm.logger.Info("Initializing cluster...")

	// Get our coin info for the announce message
	vm.mu.RLock()
	supportedCoins := vm.localNode.SupportedCoins
	coinSyncStatus := vm.localNode.CoinSyncStatus
	coinPorts := vm.coinPorts
	poolAddresses := vm.localPoolAddresses
	vm.mu.RUnlock()

	// Announce presence and listen for existing master
	vm.broadcastMessage(ClusterMessage{
		Type:           MsgTypeAnnounce,
		NodeID:         vm.config.NodeID,
		ClusterToken:   vm.clusterToken,
		UserAgent:      SpiralClusterUserAgent,
		Timestamp:      time.Now(),
		Priority:       vm.config.Priority,
		StratumPort:    vm.config.StratumPort,
		CoinPorts:      coinPorts,
		SupportedCoins: supportedCoins,
		CoinSyncStatus: coinSyncStatus,
		PoolAddresses:  poolAddresses,
	})

	// Wait for responses (give existing master time to respond)
	discoveryTimeout := 3 * time.Second
	select {
	case <-time.After(discoveryTimeout):
	case <-vm.ctx.Done():
		return
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Check if we found an existing master
	var foundMaster *ClusterNode
	for _, node := range vm.nodes {
		if node.Role == RoleMaster {
			foundMaster = node
			break
		}
	}

	if foundMaster != nil {
		// Join existing cluster as backup
		// SECURITY: Token must be pre-configured via --token flag (not received over network)
		if vm.clusterToken == "" {
			vm.logger.Error("Cannot join cluster: no cluster token configured. Use --token flag with spiralctl vip enable")
			vm.state = StateDegraded
			return
		}
		vm.masterID = foundMaster.ID
		vm.role = RoleBackup
		vm.state = StateRunning
		vm.logger.Infow("Joined existing cluster as BACKUP",
			"master", foundMaster.Host,
			"masterId", foundMaster.ID,
		)
	} else if vm.config.CanBecomeMaster {
		// No master found - can we become master?
		// CRITICAL: Only fully synced nodes can become master
		if vm.isLocalNodeFullySyncedLocked() {
			// Don't directly become master — run election to prevent dual-master
			// on simultaneous startup of multiple nodes
			vm.state = StateElection
			vm.wg.Add(1)
			go func() {
				defer vm.wg.Done()
				vm.runElection()
			}()
		} else {
			// Not synced yet - start as backup and wait for sync to complete
			vm.role = RoleBackup
			vm.state = StateRunning
			vm.logger.Infow("No master found, but node is not fully synced - starting as BACKUP",
				"supportedCoins", vm.localNode.SupportedCoins,
				"coinSyncStatus", vm.localNode.CoinSyncStatus,
				"note", "Will attempt to become master once fully synced",
			)
		}
	} else {
		// Cannot become master, stay as observer
		vm.role = RoleObserver
		vm.state = StateDegraded
		vm.logger.Warn("No master found and this node cannot become master")
	}

	// Add ourselves to nodes list
	vm.localNode.Role = vm.role
	vm.nodes[vm.config.NodeID] = vm.localNode
}

// becomeMasterLocked promotes this node to master (must hold lock).
// IMPORTANT: Node must be fully synced on all supported coins before becoming master.
// This prevents unsynced nodes from providing stale block templates to miners.
func (vm *VIPManager) becomeMasterLocked() {
	// Check sync status before allowing master promotion
	if !vm.isLocalNodeFullySyncedLocked() {
		vm.logger.Warnw("Cannot become MASTER: node is not fully synced",
			"supportedCoins", vm.localNode.SupportedCoins,
			"coinSyncStatus", vm.localNode.CoinSyncStatus,
		)
		vm.role = RoleBackup
		vm.state = StateRunning
		return
	}

	vm.logger.Info("Becoming MASTER (all coins synced)...")

	oldRole := vm.role
	vm.role = RoleMaster
	vm.masterID = vm.config.NodeID
	vm.state = StateRunning

	// Generate cluster token if first master (use cryptographically secure token)
	// SECURITY: Fail hard if crypto/rand is unavailable - never use weak fallback tokens
	if vm.clusterToken == "" {
		if vm.config.ClusterToken != "" {
			// Use pre-configured token (expected to be securely generated)
			vm.clusterToken = vm.config.ClusterToken
		} else {
			token, err := generateSecureToken()
			if err != nil {
				// SECURITY: Do NOT fall back to timestamp-based tokens
				// Cluster authentication tokens must be cryptographically secure
				vm.logger.Errorw("CRITICAL: Failed to generate secure cluster token - crypto/rand unavailable",
					"error", err,
					"action", "VIP cluster will not start without secure token",
				)
				vm.role = oldRole
				vm.masterID = ""
				vm.state = StateDegraded
				return // Abort becoming master - security cannot be compromised
			}
			vm.clusterToken = token
		}
	}

	// Acquire VIP FIRST, then broadcast only on success.
	// If VIP acquisition fails, revert to BACKUP and trigger election.
	// This prevents phantom-master state where the cluster thinks we're master
	// but miners can't reach us on the VIP.
	// Capture role transition by value before launching the async VIP acquisition.
	newRole := vm.role

	// Fire callbacks from within the acquireVIP goroutine to guarantee ordering:
	// On success: fire forward callbacks (BACKUP→MASTER) THEN broadcast.
	// On failure: fire reverse callbacks (MASTER→BACKUP) to demote.
	// This prevents the race where reverse callbacks arrive before forward callbacks
	// when acquireVIP fails quickly, which would leave the coordinator stuck in MASTER.
	vm.wg.Add(1)
	go func() {
		defer vm.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				vm.logger.Errorw("PANIC recovered in acquireVIP goroutine", "panic", r)
			}
		}()
		if err := vm.acquireVIP(); err != nil {
			vm.logger.Errorw("Failed to acquire VIP — reverting to BACKUP", "error", err)
			vm.mu.Lock()
			vm.role = RoleBackup
			vm.masterID = ""
			vm.state = StateElection
			vm.mu.Unlock()
			// Fire reverse callbacks sequentially so coordinator demotes to BACKUP.
			// These run INSTEAD of forward callbacks (not after), preventing out-of-order delivery.
			if vm.onRoleChange != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							vm.logger.Errorw("PANIC recovered in onRoleChange callback", "panic", r)
						}
					}()
					vm.onRoleChange(RoleMaster, RoleBackup)
				}()
			}
			if vm.onDatabaseFailover != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							vm.logger.Errorw("PANIC recovered in onDatabaseFailover callback", "panic", r)
						}
					}()
					vm.onDatabaseFailover(false)
				}()
			}
			// Trigger new election so another node can try
			vm.wg.Add(1)
			go func() {
				defer vm.wg.Done()
				vm.runElection()
			}()
			return
		}

		// VIP acquired successfully — fire forward callbacks THEN broadcast.
		// Sequential execution guarantees callbacks complete before broadcast
		// announces this node as master to the cluster.
		if vm.onRoleChange != nil && oldRole != newRole {
			func() {
				defer func() {
					if r := recover(); r != nil {
						vm.logger.Errorw("PANIC recovered in onRoleChange callback", "panic", r)
					}
				}()
				vm.onRoleChange(oldRole, newRole)
			}()
		}
		if vm.onDatabaseFailover != nil && oldRole != newRole {
			isMaster := newRole == RoleMaster
			func() {
				defer func() {
					if r := recover(); r != nil {
						vm.logger.Errorw("PANIC recovered in onDatabaseFailover callback", "panic", r)
					}
				}()
				vm.onDatabaseFailover(isMaster)
			}()
		}

		// Now broadcast to cluster — callbacks have completed
		vm.broadcastMessage(ClusterMessage{
			Type:           MsgTypeVIPAcquired,
			NodeID:         vm.config.NodeID,
			ClusterToken:   vm.clusterToken,
			UserAgent:      SpiralClusterUserAgent,
			Timestamp:      time.Now(),
			Role:           RoleMaster,
			Priority:       vm.config.Priority,
			VIPAddress:     vm.config.VIPAddress,
			StratumPort:    vm.config.StratumPort,
			CoinPorts:      vm.coinPorts,
			SupportedCoins: vm.localNode.SupportedCoins,
			CoinSyncStatus: vm.localNode.CoinSyncStatus,
			PoolAddresses:  vm.localPoolAddresses,
		})
	}()
}

// macvlanInterfaceName returns the name of the macvlan interface for the VIP.
const macvlanPrefix = "spiralvip"

// acquireVIP adds the VIP to the network interface.
// If UseMacVlan is enabled, creates a macvlan interface with a dedicated MAC address.
// This allows proper DHCP reservation on routers since the MAC stays consistent.
func (vm *VIPManager) acquireVIP() error {
	if runtime.GOOS != "linux" {
		vm.logger.Warn("VIP acquisition only supported on Linux")
		vm.hasVIP.Store(true) // Pretend we have it for testing
		return nil
	}

	// Security: Validate interface name to prevent command injection
	if !isValidInterfaceName(vm.config.VIPInterface) {
		return fmt.Errorf("invalid interface name: %q", vm.config.VIPInterface)
	}

	// Security: Validate VIP address (net.ParseIP handles this)
	if net.ParseIP(vm.config.VIPAddress) == nil {
		return fmt.Errorf("invalid VIP address: %q", vm.config.VIPAddress)
	}

	// Security: Split-brain prevention - check if VIP is already in use.
	// ARP detection alone is not sufficient: keepalived VRRP can hold the VIP
	// on a peer's interface even when no stratum on that peer is MASTER (e.g.,
	// after failover recovery). Only abort if ARP detects the VIP AND a remote
	// node actually reports being stratum MASTER. The election already performed
	// this combined check before calling becomeMasterLocked → acquireVIP, but
	// we verify again here as a defense-in-depth measure.
	// NOTE: checkRemoteMasterExists expects vm.mu held (it unlocks/relocks internally).
	// acquireVIP runs in a goroutine without the lock, so we acquire it here.
	if vm.isVIPInUse() {
		vm.mu.Lock()
		remoteMaster := vm.checkRemoteMasterExists()
		vm.mu.Unlock()
		if remoteMaster {
			return fmt.Errorf("VIP %s in use AND remote master confirmed (split-brain prevention)", vm.config.VIPAddress)
		}
		vm.logger.Infow("VIP detected via ARP during acquisition but no remote master — proceeding (stale keepalived binding)",
			"vip", vm.config.VIPAddress)
	}

	vipCIDR := fmt.Sprintf("%s/%d", vm.config.VIPAddress, vm.config.VIPNetmask)

	// Determine MAC address for the VIP
	vipMAC := vm.config.VIPMACAddress
	if vipMAC == "" {
		vipMAC = generateVIPMAC(vm.config.VIPAddress)
	}

	// Security: Validate MAC address to prevent command injection
	if vipMAC != "" && !isValidMACAddress(vipMAC) {
		return fmt.Errorf("invalid MAC address format: %q", vipMAC)
	}

	if vm.config.UseMacVlan && vipMAC != "" {
		// Use macvlan interface for dedicated MAC address
		// This is required for proper DHCP reservation on most routers
		macvlanIface := macvlanPrefix + "0"

		// Delete existing macvlan interface if it exists (clean slate)
		_ = exec.Command(vm.ipBin, "link", "del", macvlanIface).Run() // #nosec G104

		// FIX VIP-H2: Atomic acquisition with defer-based rollback.
		// If any step fails mid-sequence, the defer cleans up the partial macvlan state.
		acquired := false
		defer func() {
			if !acquired {
				_ = exec.Command(vm.ipBin, "link", "del", macvlanIface).Run() // #nosec G104
				vm.logger.Warnw("VIP acquisition failed — cleaned up partial macvlan state",
					"interface", macvlanIface)
			}
		}()

		// Create macvlan interface
		// ip link add spiralvip0 link eth0 type macvlan mode bridge
		cmd := exec.Command(vm.ipBin, "link", "add", macvlanIface, "link", vm.config.VIPInterface, "type", "macvlan", "mode", "bridge")
		if output, err := cmd.CombinedOutput(); err != nil {
			vm.logger.Warnw("Failed to create macvlan interface, falling back to direct IP assignment",
				"error", err, "output", string(output))
			acquired = true // Prevent defer cleanup — no macvlan was created
			// Fall through to direct IP assignment
		} else {
			// Set the MAC address on the macvlan interface
			// ip link set spiralvip0 address 02:53:c0:a8:01:c8
			cmd = exec.Command(vm.ipBin, "link", "set", macvlanIface, "address", vipMAC)
			if output, err := cmd.CombinedOutput(); err != nil {
				vm.logger.Warnw("Failed to set MAC address on macvlan", "error", err, "output", string(output))
			}

			// Bring up the macvlan interface.
			// BUG FIX (M2): Return error if interface-up fails. Without this, the node
			// claims VIP but miners cannot connect (interface is down), and heartbeats
			// continue reporting MASTER, preventing failover to a working node.
			cmd = exec.Command(vm.ipBin, "link", "set", macvlanIface, "up")
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to bring up macvlan interface: %w: %s", err, output)
			}

			// Add IP to macvlan interface instead of physical interface
			cmd = exec.Command(vm.ipBin, "addr", "add", vipCIDR, "dev", macvlanIface)
			if output, err := cmd.CombinedOutput(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
					vm.logger.Debug("VIP already assigned to macvlan interface")
				} else {
					return fmt.Errorf("failed to add VIP to macvlan: %w: %s", err, output)
				}
			}

			// Send gratuitous ARP from the macvlan interface
			// FIX VIP-H2: GARP must succeed before we mark VIP as acquired.
			if err := vm.sendGratuitousARPFromInterface(macvlanIface); err != nil {
				vm.logger.Warnw("Failed to send gratuitous ARP", "error", err)
			}

			// FIX VIP-H2: hasVIP.Store(true) moved AFTER all operations including GARP.
			acquired = true
			vm.hasVIP.Store(true)
			vm.logger.Infow("VIP acquired with macvlan",
				"vip", vm.config.VIPAddress,
				"mac", vipMAC,
				"interface", macvlanIface,
				"parent", vm.config.VIPInterface,
			)

			if vm.onVIPAcquired != nil {
				go vm.onVIPAcquired(vm.config.VIPAddress)
			}

			return nil
		}
	}

	// Fallback: Add IP directly to interface (no dedicated MAC)
	cmd := exec.Command(vm.ipBin, "addr", "add", vipCIDR, "dev", vm.config.VIPInterface)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Check if already exists (not an error)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			vm.logger.Debug("VIP already assigned to interface")
		} else {
			return fmt.Errorf("failed to add VIP: %w: %s", err, output)
		}
	}

	vm.logger.Infow("VIP acquired (direct mode - no dedicated MAC)",
		"vip", vm.config.VIPAddress,
		"interface", vm.config.VIPInterface,
	)

	// Send gratuitous ARP to update network switches
	if err := vm.sendGratuitousARP(); err != nil {
		vm.logger.Warnw("Failed to send gratuitous ARP", "error", err)
	}

	// FIX VIP-H2: hasVIP.Store(true) moved AFTER GARP — matches macvlan path ordering.
	// Without this, other components see VIP as acquired before network switches are updated.
	vm.hasVIP.Store(true)

	if vm.onVIPAcquired != nil {
		go vm.onVIPAcquired(vm.config.VIPAddress)
	}

	return nil
}

// cleanupOrphanedVIP removes VIP interfaces left behind by a previous crash.
// FIX VIP-H3: Called during Start() before any cluster activity begins.
// Uses the same cleanup pattern as releaseVIP() but unconditionally — doesn't
// check hasVIP since we don't know our state after a crash.
func (vm *VIPManager) cleanupOrphanedVIP() {
	if runtime.GOOS != "linux" {
		return
	}

	macvlanIface := macvlanPrefix + "0"

	// Check if orphaned macvlan interface exists
	if _, err := net.InterfaceByName(macvlanIface); err == nil {
		vm.logger.Warnw("Found orphaned VIP interface from previous run — cleaning up",
			"interface", macvlanIface)
		_ = exec.Command(vm.ipBin, "link", "del", macvlanIface).Run() // #nosec G104
	}

	// Also remove any stale VIP IP from the physical interface — but ONLY if
	// it was placed there by stratum (direct mode), NOT by keepalived.
	// Keepalived adds VIP with label "spiralpool-vip". If we see that label on
	// the VIP address, keepalived owns it and will manage its lifecycle. Removing
	// it here would cause 90-180s of VIP downtime until stratum re-acquires.
	if vm.config.VIPAddress != "" && vm.config.VIPNetmask > 0 {
		// Check if keepalived owns this address (has spiralpool-vip label)
		out, _ := exec.Command(vm.ipBin, "-o", "addr", "show", "dev", vm.config.VIPInterface).CombinedOutput() // #nosec G104
		keepalivedOwns := false
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, vm.config.VIPAddress+"/") && strings.Contains(line, "spiralpool-vip") {
				keepalivedOwns = true
				break
			}
		}
		if keepalivedOwns {
			vm.logger.Debugw("Skipping VIP cleanup on physical interface — managed by keepalived",
				"vip", vm.config.VIPAddress, "interface", vm.config.VIPInterface)
		} else {
			vipCIDR := fmt.Sprintf("%s/%d", vm.config.VIPAddress, vm.config.VIPNetmask)
			_ = exec.Command(vm.ipBin, "addr", "del", vipCIDR, "dev", vm.config.VIPInterface).Run() // #nosec G104
		}
	}
}

// releaseVIP removes the VIP from the network interface.
func (vm *VIPManager) releaseVIP() error {
	if !vm.hasVIP.Load() {
		return nil
	}

	if runtime.GOOS != "linux" {
		vm.hasVIP.Store(false)
		return nil
	}

	vipCIDR := fmt.Sprintf("%s/%d", vm.config.VIPAddress, vm.config.VIPNetmask)
	macvlanIface := macvlanPrefix + "0"

	// Try to remove macvlan interface first (this also removes the IP)
	if vm.config.UseMacVlan {
		cmd := exec.Command(vm.ipBin, "link", "del", macvlanIface)
		if output, err := cmd.CombinedOutput(); err == nil {
			vm.hasVIP.Store(false)
			vm.logger.Infow("VIP released (macvlan removed)", "vip", vm.config.VIPAddress, "interface", macvlanIface)

			// Broadcast VIP released
			vm.broadcastMessage(ClusterMessage{
				Type:         MsgTypeVIPReleased,
				NodeID:       vm.config.NodeID,
				ClusterToken: vm.clusterToken,
				UserAgent:    SpiralClusterUserAgent,
				Timestamp:    time.Now(),
			})

			if vm.onVIPReleased != nil {
				go vm.onVIPReleased(vm.config.VIPAddress)
			}

			return nil
		} else {
			vm.logger.Debugw("No macvlan interface to remove", "output", string(output))
		}
	}

	// Fallback: Remove IP from physical interface — but ONLY if stratum owns it.
	// Same guard as cleanupOrphanedVIP: if keepalived added this VIP (label
	// "spiralpool-vip"), removing it here would cause 90-180s VIP outage because
	// keepalived wouldn't know its address was removed and would continue sending
	// VRRP advertisements while no node actually serves the VIP.
	out, _ := exec.Command(vm.ipBin, "-o", "addr", "show", "dev", vm.config.VIPInterface).CombinedOutput() // #nosec G104
	keepalivedOwns := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, vm.config.VIPAddress+"/") && strings.Contains(line, "spiralpool-vip") {
			keepalivedOwns = true
			break
		}
	}
	if keepalivedOwns {
		vm.logger.Debugw("Skipping VIP removal from physical interface — managed by keepalived",
			"vip", vm.config.VIPAddress, "interface", vm.config.VIPInterface)
	} else {
		cmd := exec.Command(vm.ipBin, "addr", "del", vipCIDR, "dev", vm.config.VIPInterface)
		if output, err := cmd.CombinedOutput(); err != nil {
			// Not a fatal error - VIP might already be gone
			vm.logger.Debugw("Could not remove VIP from interface", "error", err, "output", string(output))
		}
	}

	vm.hasVIP.Store(false)
	vm.logger.Infow("VIP released", "vip", vm.config.VIPAddress)

	// Broadcast VIP released
	vm.broadcastMessage(ClusterMessage{
		Type:         MsgTypeVIPReleased,
		NodeID:       vm.config.NodeID,
		ClusterToken: vm.clusterToken,
		UserAgent:    SpiralClusterUserAgent,
		Timestamp:    time.Now(),
	})

	if vm.onVIPReleased != nil {
		go vm.onVIPReleased(vm.config.VIPAddress)
	}

	return nil
}

// sendGratuitousARP sends GARP to update network switches using the configured VIP interface.
func (vm *VIPManager) sendGratuitousARP() error {
	return vm.sendGratuitousARPFromInterface(vm.config.VIPInterface)
}

// sendGratuitousARPFromInterface sends GARP from a specific interface.
func (vm *VIPManager) sendGratuitousARPFromInterface(iface string) error {
	if runtime.GOOS != "linux" {
		return nil
	}

	// Use arping to send gratuitous ARP (broadcasts new MAC→IP mapping to switches).
	// If arping fails, fall back to ip neigh flush (only clears local ARP cache —
	// switches won't learn the new MAC until their cache expires, ~30-300s).
	arpingFailed := false
	for i := 0; i < vm.config.GratuitousARPCount; i++ {
		cmd := exec.Command(vm.arpingBin, "-U", "-c", "1", "-I", iface, vm.config.VIPAddress)
		if err := cmd.Run(); err != nil {
			if !arpingFailed {
				vm.logger.Warnw("arping failed — falling back to ip neigh flush (switches will NOT be updated, miners may take 30-300s to reconnect)",
					"error", err, "interface", iface, "vip", vm.config.VIPAddress)
				arpingFailed = true
			}
			cmd = exec.Command(vm.ipBin, "neigh", "flush", vm.config.VIPAddress)
			if err2 := cmd.Run(); err2 != nil {
				vm.logger.Warnw("ip neigh flush also failed", "error", err2)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// discoveryLoop handles incoming cluster messages.
func (vm *VIPManager) discoveryLoop() {
	defer vm.wg.Done()

	buf := make([]byte, 8192) // Larger buffer for encrypted messages
	loopCount := 0

	for {
		select {
		case <-vm.ctx.Done():
			return
		default:
		}

		// Periodic cleanup of stale per-IP rate limiters (~every 5 minutes at 1s/iteration)
		loopCount++
		if loopCount%300 == 0 {
			vm.cleanupStaleUDPLimiters()
		}

		_ = vm.discoveryConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := vm.discoveryConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-vm.ctx.Done():
				return
			default:
				continue
			}
		}

		// Check if IP is blacklisted (anti-brute-force)
		ipStr := addr.IP.String()

		// Per-IP rate limit for UDP messages to prevent a single noisy IP
		// from exhausting the global rate limit and dropping legitimate cluster messages
		if !vm.allowUDPFromIP(ipStr) {
			vm.logger.Debugw("UDP rate limit exceeded for IP", "ip", ipStr)
			continue
		}

		// Global rate limit as a secondary safety net
		if !vm.udpRateLimiter.allow() {
			vm.logger.Debug("Global UDP rate limit exceeded, dropping message")
			continue
		}

		if vm.isIPBlacklisted(ipStr) {
			vm.logger.Debugw("Dropped message from blacklisted IP", "ip", ipStr)
			continue
		}

		// Try to decrypt the message (encrypted messages use EncryptedMessage wrapper)
		msg, err := vm.decryptAndUnwrapMessage(buf[:n])
		if err != nil {
			// Record auth failure for anti-brute-force (decryption failure = wrong token)
			vm.recordAuthFailure(ipStr)
			vm.logger.Debugw("Failed to decrypt cluster message",
				"from", addr.String(),
				"error", err,
			)
			continue
		}

		// Successful decryption - clear any auth failures for this IP
		vm.resetAuthFailures(ipStr)

		// Ignore our own messages
		if msg.NodeID == vm.config.NodeID {
			continue
		}

		vm.handleMessage(*msg, addr)
	}
}

// handleMessage processes a cluster message.
func (vm *VIPManager) handleMessage(msg ClusterMessage, addr *net.UDPAddr) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Validate user-agent - must be a Spiral Pool HA node (prefix match allows version variations)
	if !strings.HasPrefix(msg.UserAgent, SpiralClusterUserAgentPrefix) {
		vm.logger.Warnw("Rejected message with invalid user-agent",
			"from", addr.String(),
			"nodeId", msg.NodeID,
			"userAgent", msg.UserAgent,
			"expectedPrefix", SpiralClusterUserAgentPrefix,
		)
		// Record as auth failure - likely not a legitimate Spiral Pool node
		vm.recordAuthFailure(addr.IP.String())
		return
	}

	// Validate cluster token using constant-time comparison (prevents timing attacks)
	// SECURITY: When a token is configured, ALL messages must present a valid token.
	// Rejecting empty tokens prevents auth bypass where an attacker sends messages
	// without a token to skip this check entirely.
	if vm.clusterToken != "" && !constantTimeCompare(msg.ClusterToken, vm.clusterToken) {
		// Record auth failure for anti-brute-force (wrong token in message)
		vm.recordAuthFailure(addr.IP.String())
		vm.logger.Warnw("Rejected message with wrong cluster token",
			"from", addr.String(),
			"nodeId", msg.NodeID,
		)
		return
	}

	// Anti-abuse: Validate Node ID format
	if !isValidNodeID(msg.NodeID) {
		vm.logger.Warnw("Rejected message with invalid node ID format",
			"from", addr.String(),
			"nodeId", msg.NodeID,
		)
		vm.recordAuthFailure(addr.IP.String())
		return
	}

	// Anti-abuse: Validate priority is within acceptable range (100-999)
	// Priority < 100 is reserved and not allowed from network messages
	claimedPriority := msg.Priority
	if claimedPriority < 100 {
		vm.logger.Warnw("Rejected node with invalid priority (below minimum 100)",
			"from", addr.String(),
			"nodeId", msg.NodeID,
			"claimedPriority", claimedPriority,
		)
		// Record as auth failure - could be abuse attempt
		vm.recordAuthFailure(addr.IP.String())
		return
	}
	if claimedPriority > 999 {
		// Cap at 999 but don't reject - just log warning
		vm.logger.Warnw("Node priority capped at 999",
			"nodeId", msg.NodeID,
			"claimedPriority", claimedPriority,
		)
		claimedPriority = 999
	}

	// Update or create node
	node, exists := vm.nodes[msg.NodeID]
	if !exists {
		// Anti-abuse: Limit maximum cluster size to prevent resource exhaustion
		if len(vm.nodes) >= maxClusterSize {
			vm.logger.Warnw("Rejected node: cluster at maximum capacity",
				"from", addr.String(),
				"nodeId", msg.NodeID,
				"maxClusterSize", maxClusterSize,
			)
			return
		}

		node = &ClusterNode{
			ID:          msg.NodeID,
			Host:        addr.IP.String(),
			Port:        addr.Port,
			StratumPort: msg.StratumPort,
			CoinPorts:   msg.CoinPorts, // Multi-coin port support
			Priority:    claimedPriority,
			JoinedAt:    time.Now(),
		}
		vm.nodes[msg.NodeID] = node

		if vm.onNodeJoined != nil {
			go vm.onNodeJoined(node)
		}
	} else {
		// Update priority if node exists (but still validate)
		node.Priority = claimedPriority
	}

	node.LastSeen = time.Now()
	node.Role = msg.Role
	node.IsHealthy = true

	// Update coin awareness information from the message
	// Security: Validate and sanitize received coin data to prevent DoS
	if len(msg.SupportedCoins) > 0 {
		// Limit number of coins and validate each ticker
		validCoins := make([]string, 0, min(len(msg.SupportedCoins), MaxSupportedCoins))
		for _, coin := range msg.SupportedCoins {
			if len(coin) > 0 && len(coin) <= MaxCoinTickerLength {
				validCoins = append(validCoins, coin)
				if len(validCoins) >= MaxSupportedCoins {
					break
				}
			}
		}
		node.SupportedCoins = validCoins
	}
	if msg.CoinSyncStatus != nil {
		// Validate and copy coin sync status
		validStatus := make(map[string]*CoinSyncStatus)
		for coin, status := range msg.CoinSyncStatus {
			if len(coin) == 0 || len(coin) > MaxCoinTickerLength {
				continue // Skip invalid coin tickers
			}
			if status == nil {
				continue
			}
			// Clamp values to valid ranges
			syncPct := status.SyncPct
			if syncPct < 0 || syncPct != syncPct { // NaN check
				syncPct = 0
			} else if syncPct > 100 {
				syncPct = 100
			}
			blockHeight := status.BlockHeight
			if blockHeight < 0 {
				blockHeight = 0
			}
			validStatus[coin] = &CoinSyncStatus{
				Coin:        coin,
				SyncPct:     syncPct,
				BlockHeight: blockHeight,
				IsSynced:    syncPct >= SyncThresholdPercent,
			}
			if len(validStatus) >= MaxSupportedCoins {
				break
			}
		}
		node.CoinSyncStatus = validStatus
	}

	// Update multi-coin port information
	// Security: Validate port ranges to prevent resource exhaustion or confusion
	if msg.CoinPorts != nil {
		validPorts := make(map[string]*CoinPortInfo)
		for coin, portInfo := range msg.CoinPorts {
			if len(coin) == 0 || len(coin) > MaxCoinTickerLength {
				continue // Skip invalid coin tickers
			}
			if portInfo == nil {
				continue
			}
			// Validate port ranges (1-65535)
			if portInfo.StratumV1 < 1 || portInfo.StratumV1 > 65535 {
				continue
			}
			// Copy with validated ports
			validated := &CoinPortInfo{
				StratumV1: portInfo.StratumV1,
			}
			if portInfo.StratumV2 >= 1 && portInfo.StratumV2 <= 65535 {
				validated.StratumV2 = portInfo.StratumV2
			}
			if portInfo.StratumTLS >= 1 && portInfo.StratumTLS <= 65535 {
				validated.StratumTLS = portInfo.StratumTLS
			}
			validPorts[coin] = validated
			if len(validPorts) >= MaxSupportedCoins {
				break
			}
		}
		node.CoinPorts = validPorts
	}

	// Update pool payout addresses for failover validation
	// CRITICAL: Addresses must match across all HA nodes or payouts will split!
	if msg.PoolAddresses != nil {
		validAddrs := make(map[string]string)
		for coin, addr := range msg.PoolAddresses {
			// Validate coin ticker and address format
			if len(coin) == 0 || len(coin) > MaxCoinTickerLength {
				continue
			}
			if len(addr) == 0 || len(addr) > 128 { // Max reasonable address length
				continue
			}
			validAddrs[coin] = addr
			if len(validAddrs) >= MaxSupportedCoins {
				break
			}
		}
		node.PoolAddresses = validAddrs

		// Validate addresses match across cluster (async to avoid deadlock)
		// The validatePoolAddresses function acquires RLock, so we call it
		// via goroutine to avoid deadlock with our current Lock
		if len(validAddrs) > 0 {
			nodeID := msg.NodeID
			addrs := validAddrs
			go vm.validatePoolAddresses(nodeID, addrs)
		}
	}

	switch msg.Type {
	case MsgTypeHeartbeat:
		// Just update last seen (already done above)
		// FIX: If we see a heartbeat from a master but have no masterID recorded
		// (e.g., JoinAccept was lost due to packet loss or node joined late),
		// record the master now. Prevents spurious "masterless cluster" elections.
		if msg.Role == RoleMaster && vm.masterID == "" {
			vm.masterID = msg.NodeID
			vm.logger.Infow("Discovered master via heartbeat",
				"masterId", msg.NodeID,
				"from", addr.String(),
			)
		}
		// Record UDP heartbeat for multi-path partition detection
		if vm.partitionDetector != nil {
			vm.partitionDetector.RecordUDPHeartbeat(msg.NodeID)
		}

	case MsgTypeAnnounce:
		// New node announcing - if we're master, respond with cluster info
		// SECURITY: We do NOT send the cluster token here - it must be pre-shared
		// out-of-band (via spiralctl vip enable --token <token>)
		if vm.role == RoleMaster {
			// Only respond if the announcing node already has our token
			// (they proved it by sending an encrypted message we could decrypt)

			// SECURITY: Rate limit announce responses per-IP to prevent UDP amplification attacks.
			// An attacker could spoof source IPs and use announce messages to amplify traffic.
			// Limit to max 3 JoinAccept responses per IP per minute.
			ipStr := addr.IP.String()
			if !vm.announceRateLimiter.allowIP(ipStr, 3, time.Minute) {
				vm.logger.Debugw("Rate limiting announce response",
					"from", ipStr,
					"nodeId", msg.NodeID,
				)
				return
			}

			// Calculate assigned priority for the new node based on cluster size
			// Master is 100, first backup is 101, etc.
			assignedPriority := 100 + len(vm.nodes)

			vm.logger.Infow("Node announced to cluster, assigning priority",
				"nodeId", msg.NodeID,
				"from", addr.String(),
				"assignedPriority", assignedPriority,
				"clusterSize", len(vm.nodes),
			)

			// Update the node's priority in our records
			if existingNode, ok := vm.nodes[msg.NodeID]; ok {
				existingNode.Priority = assignedPriority
			}

			// Send JoinAccept with assigned priority and coin port info
			// FIX: Cannot use sendToNode() here — it calls encryptAndWrapMessage()
			// which takes vm.mu.RLock(), but we already hold vm.mu.Lock() (from
			// handleMessage). RLock-while-Lock on the same goroutine deadlocks in Go.
			// Instead, read the token directly (safe under write lock) and use
			// encryptAndWrapMessageWithToken which doesn't take any lock.
			joinAcceptMsg := ClusterMessage{
				Type:             MsgTypeJoinAccept,
				NodeID:           vm.config.NodeID,
				UserAgent:        SpiralClusterUserAgent,
				Timestamp:        time.Now(),
				Role:             RoleMaster,
				Priority:         vm.config.Priority,
				AssignedPriority: assignedPriority,
				ClusterSize:      len(vm.nodes),
				VIPAddress:       vm.config.VIPAddress,
				StratumPort:      vm.config.StratumPort,
				CoinPorts:        vm.coinPorts,
				SupportedCoins:   vm.localNode.SupportedCoins,
				CoinSyncStatus:   vm.localNode.CoinSyncStatus,
				// SECURITY: ClusterToken is NOT included - must be pre-shared
			}
			if data, err := vm.encryptAndWrapMessageWithToken(joinAcceptMsg, vm.clusterToken); err == nil {
				_, _ = vm.discoveryConn.WriteTo(data, addr) // #nosec G104
			} else {
				vm.logger.Warnw("Failed to encrypt JoinAccept", "error", err)
			}
		}

	case MsgTypeJoinAccept:
		// Master accepted us and assigned a priority
		// FIX: Record masterID so masterless detection (line 3661) doesn't trigger
		// a spurious election. Without this, a node that receives JoinAccept but
		// starts as BACKUP (not yet synced) would later fire "masterless cluster
		// detected" because vm.masterID was never set from the JoinAccept.
		if msg.Role == RoleMaster && vm.masterID == "" {
			vm.masterID = msg.NodeID
		}
		if msg.AssignedPriority > 0 && vm.config.AutoPriority {
			// Update our priority to the one assigned by master
			vm.config.Priority = msg.AssignedPriority
			vm.localNode.Priority = msg.AssignedPriority
			vm.logger.Infow("Joined cluster with assigned priority",
				"masterId", msg.NodeID,
				"vip", msg.VIPAddress,
				"assignedPriority", msg.AssignedPriority,
				"clusterSize", msg.ClusterSize,
			)
		} else {
			vm.logger.Infow("Joined cluster, master acknowledged",
				"masterId", msg.NodeID,
				"vip", msg.VIPAddress,
			)
		}

	case MsgTypeVIPAcquired:
		// Another node claims master. Only accept if they have higher priority
		// (or equal priority with lower NodeID). Do NOT set masterID unconditionally —
		// that would create dual-master state if we don't actually defer.
		// SECURITY: Token is NOT received over network - must be pre-shared

		if msg.NodeID == vm.config.NodeID {
			// Our own broadcast echoed back — ignore
			break
		}

		if vm.role == RoleMaster {
			// We are currently master — check if we should defer
			// Tiebreaker: if equal priority, lower NodeID wins (deterministic)
			if msg.Priority < vm.config.Priority || (msg.Priority == vm.config.Priority && msg.NodeID < vm.config.NodeID) {
				vm.logger.Warnw("Deferring to higher priority master",
					"newMaster", msg.NodeID,
					"theirPriority", msg.Priority,
					"ourPriority", vm.config.Priority,
				)
				vm.masterID = msg.NodeID
				oldRole := vm.role
				vm.role = RoleBackup
				vm.state = StateRunning
				vm.wg.Add(1)
				go func() {
					defer vm.wg.Done()
					if err := vm.releaseVIP(); err != nil {
						vm.logger.Warnw("Failed to release VIP after deferring to higher-priority master",
							"error", err, "newMaster", msg.NodeID)
					}
				}()
				// FIX Issue 28: Fire onRoleChange so coordinator demotes CoinPools
				// and payment processors. Without this, the coordinator stays in
				// MASTER mode (haRole=MASTER on CoinPools, isMaster=true on
				// payment processors) despite this node no longer being master.
				// Mitigated by no-VIP=no-miners, but state should be consistent.
				if vm.onRoleChange != nil {
					vm.wg.Add(1)
					go func() {
						defer vm.wg.Done()
						defer func() {
							if r := recover(); r != nil {
								vm.logger.Errorw("PANIC recovered in onRoleChange callback", "panic", r)
							}
						}()
						vm.onRoleChange(oldRole, RoleBackup)
					}()
				}
				// Trigger database failover - we're no longer master
				if vm.onDatabaseFailover != nil && oldRole != vm.role {
					vm.wg.Add(1)
					go func() {
						defer vm.wg.Done()
						defer func() {
							if r := recover(); r != nil {
								vm.logger.Errorw("PANIC recovered in onDatabaseFailover callback", "panic", r)
							}
						}()
						vm.onDatabaseFailover(false)
					}()
				}
			} else {
				// We have higher priority — re-broadcast our own VIPAcquired to reassert
				vm.logger.Warnw("Rejecting lower-priority master claim, reasserting our VIPAcquired",
					"claimant", msg.NodeID,
					"theirPriority", msg.Priority,
					"ourPriority", vm.config.Priority,
				)
				// BUG FIX (C1): Without this re-assertion broadcast, the lower-priority
				// claimant continues believing it is master (it already called acquireVIP
				// and broadcast VIPAcquired). This causes dual-master / split-brain.
				vm.broadcastMessage(ClusterMessage{
					Type:           MsgTypeVIPAcquired,
					NodeID:         vm.config.NodeID,
					ClusterToken:   vm.clusterToken,
					UserAgent:      SpiralClusterUserAgent,
					Timestamp:      time.Now(),
					Role:           RoleMaster,
					Priority:       vm.config.Priority,
					VIPAddress:     vm.config.VIPAddress,
					StratumPort:    vm.config.StratumPort,
					CoinPorts:      vm.coinPorts,
					SupportedCoins: vm.localNode.SupportedCoins,
					CoinSyncStatus: vm.localNode.CoinSyncStatus,
					PoolAddresses:  vm.localPoolAddresses,
				})
			}
		} else {
			// We are not master — accept the new master
			vm.masterID = msg.NodeID
			// Reset election state if running — the election is now moot
			// since a master has been established. Without this, the election
			// goroutine continues until its timeout completes.
			if vm.state == StateElection || vm.state == StateFailover {
				vm.state = StateRunning
			}
		}

	case MsgTypeVIPReleased:
		// Master released VIP - clear masterID and start election if eligible.
		// BUG FIX (H1): masterID MUST be cleared regardless of CanBecomeMaster.
		// Without this, "masterless cluster" detection (requires masterID == "")
		// never fires, causing election deadlock after graceful master shutdown.
		// BUG FIX (M1): Observer nodes also clear masterID. Without this, their
		// /status endpoint reports stale masterID, blocking elections on other
		// nodes via the asymmetric partition detection check.
		if msg.NodeID == vm.masterID {
			vm.masterID = ""
			if vm.config.CanBecomeMaster {
				vm.state = StateElection
				vm.wg.Add(1)
				go func() {
					defer vm.wg.Done()
					vm.runElection()
				}()
			}
		}

	case MsgTypeElection:
		// Election in progress - participate if we're higher priority (or win tiebreaker)
		// Must match the tiebreaker logic in runElection: lower priority wins, then lower NodeID
		if vm.config.CanBecomeMaster &&
			vm.state != StateElection && vm.state != StateFailover &&
			(vm.config.Priority < msg.Priority ||
				(vm.config.Priority == msg.Priority && vm.config.NodeID < msg.NodeID)) {
			// We have higher priority (or win tiebreaker), start our own election.
			// BUG FIX: Must set StateElection BEFORE launching runElection().
			// Without this, runElection() checks vm.state != StateElection at line 4033
			// and returns immediately — the higher-priority node never participates,
			// letting the lower-priority node win by default.
			vm.state = StateElection
			vm.wg.Add(1)
			go func() {
				defer vm.wg.Done()
				vm.runElection()
			}()
		}
	}
}

// heartbeatLoop sends periodic heartbeats.
func (vm *VIPManager) heartbeatLoop() {
	defer vm.wg.Done()

	ticker := time.NewTicker(vm.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-vm.ctx.Done():
			return
		case <-ticker.C:
			vm.mu.RLock()
			msg := ClusterMessage{
				Type:         MsgTypeHeartbeat,
				NodeID:       vm.config.NodeID,
				ClusterToken: vm.clusterToken,
				UserAgent:    SpiralClusterUserAgent,
				Timestamp:    time.Now(),
				Role:         vm.role,
				Priority:     vm.config.Priority,
				StratumPort:  vm.config.StratumPort,
				// Multi-coin port support
				CoinPorts: vm.coinPorts,
				// Include coin awareness information for failover decisions
				SupportedCoins: vm.localNode.SupportedCoins,
				CoinSyncStatus: vm.localNode.CoinSyncStatus,
				// Pool addresses for failover validation
				PoolAddresses: vm.localPoolAddresses,
			}
			if vm.role == RoleMaster {
				msg.VIPAddress = vm.config.VIPAddress
			}
			vm.mu.RUnlock()

			vm.broadcastMessage(msg)
		}
	}
}

// healthCheckLoop monitors cluster health and triggers failover.
func (vm *VIPManager) healthCheckLoop() {
	defer vm.wg.Done()

	ticker := time.NewTicker(vm.config.HeartbeatInterval * 2)
	defer ticker.Stop()

	for {
		select {
		case <-vm.ctx.Done():
			return
		case <-ticker.C:
			vm.checkClusterHealth()
		}
	}
}

// checkClusterHealth checks node health and triggers failover if needed.
// checkNodeHTTP verifies a node is alive via HTTP status endpoint.
// Used as a fallback when UDP heartbeats fail (e.g., UDP blocked but HTTP works).
func (vm *VIPManager) checkNodeHTTP(id string, node *ClusterNode) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s:%d/status", node.Host, vm.config.StatusPort)
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	var statusResp struct {
		LocalRole string `json:"localRole"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&statusResp); err != nil {
		return false
	}

	return statusResp.LocalRole != "" // Any valid role response means the node is alive
}

func (vm *VIPManager) checkClusterHealth() {
	// Phase 1: Identify stale nodes under read lock and snapshot their hosts
	// so we can do HTTP checks outside the lock (avoids blocking during I/O).
	type staleCandidate struct {
		id   string
		host string
	}
	vm.mu.RLock()
	now := time.Now()
	var staleCandidates []staleCandidate
	for id, node := range vm.nodes {
		if id == vm.config.NodeID {
			continue
		}
		if now.Sub(node.LastSeen) > vm.config.FailoverTimeout {
			staleCandidates = append(staleCandidates, staleCandidate{id, node.Host})
		}
	}
	vm.mu.RUnlock()

	// Phase 2: HTTP fallback checks outside lock (up to 2s timeout each)
	httpAlive := make(map[string]bool, len(staleCandidates))
	for _, sc := range staleCandidates {
		client := &http.Client{Timeout: 2 * time.Second}
		url := fmt.Sprintf("http://%s:%d/status", sc.host, vm.config.StatusPort)
		resp, err := client.Get(url)
		if err != nil {
			httpAlive[sc.id] = false
			continue
		}
		var statusResp struct {
			LocalRole string `json:"localRole"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&statusResp)
		resp.Body.Close()
		httpAlive[sc.id] = statusResp.LocalRole != ""
	}

	// Phase 3: Apply results under write lock
	vm.mu.Lock()
	defer vm.mu.Unlock()

	now = time.Now() // refresh after HTTP checks
	var removedNodes []string

	for id, node := range vm.nodes {
		if id == vm.config.NodeID {
			continue // Skip self
		}

		// Check if node is stale
		if now.Sub(node.LastSeen) > vm.config.FailoverTimeout {
			// If HTTP probe (done outside lock) confirmed the node is alive, update it
			if httpAlive[id] {
				node.LastSeen = now
				node.IsHealthy = true
				continue
			}
			node.IsHealthy = false

			// If this was the master, start election (guard: don't launch duplicate
			// election goroutines if one is already in progress — matches the
			// masterless detection guard at the bottom of checkClusterHealth)
			if id == vm.masterID && vm.config.CanBecomeMaster && vm.state != StateElection && vm.state != StateFailover {
				vm.logger.Warnw("Master node unresponsive, starting election",
					"masterId", vm.masterID,
					"lastSeen", node.LastSeen,
				)
				vm.state = StateElection
				vm.wg.Add(1)
				go func() {
					defer vm.wg.Done()
					vm.runElection()
				}()
			}

			// Remove very stale nodes
			if now.Sub(node.LastSeen) > vm.config.FailoverTimeout*10 {
				removedNodes = append(removedNodes, id)
			}
		} else {
			node.IsHealthy = true
		}
	}

	// Clean up removed nodes
	for _, id := range removedNodes {
		node := vm.nodes[id]
		delete(vm.nodes, id)
		// FIX Issue 25: Clear stale masterID when GC'ing the master node.
		// After 10x FailoverTimeout, the master node is removed from vm.nodes.
		// Without clearing masterID, the health-check election trigger (line 3438)
		// can no longer match the GC'd node, and masterless detection (line 3474)
		// requires masterID == "". Result: election deadlock — no path to recovery.
		if id == vm.masterID {
			vm.logger.Warnw("Clearing stale masterID — master node was garbage-collected",
				"masterID", vm.masterID,
			)
			vm.masterID = ""
		}
		if vm.onNodeLeft != nil {
			go vm.onNodeLeft(node)
		}
	}

	// BUG FIX: Detect masterless cluster and trigger election once local node is synced.
	// This handles the case where all nodes start as BACKUP (not synced at startup),
	// then sync completes but no election was triggered because there was never a
	// master to "fail over from". The log at startup says "Will attempt to become
	// master once fully synced" but this code was missing.
	if vm.masterID == "" && vm.config.CanBecomeMaster && vm.state != StateElection && vm.state != StateFailover {
		if vm.isLocalNodeFullySyncedLocked() {
			vm.logger.Infow("Masterless cluster detected and local node is now synced - starting election",
				"localNode", vm.config.NodeID,
				"state", vm.state,
			)
			vm.state = StateElection
			vm.wg.Add(1)
			go func() {
				defer vm.wg.Done()
				vm.runElection()
			}()
		}
	}
}

// blacklistCleanupLoop periodically cleans up expired blacklist entries and auth failures.
func (vm *VIPManager) blacklistCleanupLoop() {
	defer vm.wg.Done()

	ticker := time.NewTicker(blacklistCleanupFreq)
	defer ticker.Stop()

	for {
		select {
		case <-vm.ctx.Done():
			return
		case <-ticker.C:
			vm.cleanupBlacklist()
		}
	}
}

// flapBackoffResetLoop periodically checks if the cluster has stabilized and reduces backoff.
func (vm *VIPManager) flapBackoffResetLoop() {
	defer vm.wg.Done()

	// Check every flap window interval
	ticker := time.NewTicker(vm.flapDetector.config.FlapWindow)
	defer ticker.Stop()

	for {
		select {
		case <-vm.ctx.Done():
			return
		case <-ticker.C:
			// Try to reduce backoff if cluster has stabilized
			vm.flapDetector.ResetBackoff()
		}
	}
}

// httpHeartbeatLoop sends HTTP-based heartbeats to other nodes as a secondary path.
// This provides redundant connectivity verification alongside UDP discovery.
func (vm *VIPManager) httpHeartbeatLoop() {
	defer vm.wg.Done()

	ticker := time.NewTicker(vm.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-vm.ctx.Done():
			return
		case <-ticker.C:
			vm.sendHTTPHeartbeats()

			// Also query witness nodes periodically
			if len(vm.witnessNodes) > 0 {
				vm.queryWitnessNodes()
			}
		}
	}
}

// sendHTTPHeartbeats sends HTTP heartbeat requests to all known nodes.
func (vm *VIPManager) sendHTTPHeartbeats() {
	vm.mu.RLock()
	nodes := make([]*ClusterNode, 0, len(vm.nodes))
	for _, node := range vm.nodes {
		if node.ID != vm.config.NodeID { // Skip self
			nodes = append(nodes, node)
		}
	}
	vm.mu.RUnlock()

	for _, node := range nodes {
		go vm.sendHTTPHeartbeatToNode(node)
	}
}

// sendHTTPHeartbeatToNode sends an HTTP heartbeat to a specific node.
func (vm *VIPManager) sendHTTPHeartbeatToNode(node *ClusterNode) {
	statusPort := vm.config.StatusPort
	url := fmt.Sprintf("http://%s:%d/status", node.Host, statusPort)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}

	req.Header.Set("User-Agent", SpiralClusterUserAgent)
	req.Header.Set("X-Spiral-NodeID", vm.config.NodeID)

	resp, err := vm.httpHeartbeatClient.Do(req)
	if err != nil {
		vm.logger.Debugw("HTTP heartbeat failed",
			"targetNode", node.ID,
			"targetHost", node.Host,
			"error", err,
		)
		return
	}
	defer resp.Body.Close()
	// Drain the response body so the underlying TCP connection can be reused
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024)) // #nosec G104

	if resp.StatusCode == http.StatusOK {
		// Record successful HTTP heartbeat
		if vm.partitionDetector != nil {
			vm.partitionDetector.RecordHTTPHeartbeat(node.ID)
		}

		vm.logger.Debugw("HTTP heartbeat successful",
			"targetNode", node.ID,
			"targetHost", node.Host,
		)
	}
}

// queryWitnessNodes queries external witness nodes for cluster state verification.
func (vm *VIPManager) queryWitnessNodes() {
	if vm.partitionDetector == nil {
		return
	}

	responses := vm.partitionDetector.QueryAllWitnesses()

	// Check for partition based on witness responses
	reachableWitnesses := 0
	agreeOnMaster := 0
	var expectedMaster string

	vm.mu.RLock()
	expectedMaster = vm.masterID
	vm.mu.RUnlock()

	for _, resp := range responses {
		if resp.Reachable {
			reachableWitnesses++
			if resp.MasterID == expectedMaster {
				agreeOnMaster++
			}
		}
	}

	// If we can reach witnesses but they disagree on master, we may be partitioned
	if reachableWitnesses > 0 && agreeOnMaster == 0 && expectedMaster != "" {
		vm.logger.Warnw("Witness nodes disagree on master - possible partition",
			"expectedMaster", expectedMaster,
			"reachableWitnesses", reachableWitnesses,
			"agreeOnMaster", agreeOnMaster,
		)
	}
}

// GetPartitionStatus returns the current network partition detection status.
func (vm *VIPManager) GetPartitionStatus() *PartitionStatus {
	if vm.partitionDetector == nil {
		return nil
	}
	status := vm.partitionDetector.GetPartitionStatus(vm.config.FailoverTimeout)
	return &status
}

// cleanupBlacklist removes expired blacklist entries and old auth failures.
func (vm *VIPManager) cleanupBlacklist() {
	vm.failedAuthMu.Lock()
	defer vm.failedAuthMu.Unlock()

	now := time.Now()

	// Remove expired blacklist entries
	for ip, expiry := range vm.blacklistedIPs {
		if now.After(expiry) {
			delete(vm.blacklistedIPs, ip)
			vm.logger.Infow("IP removed from blacklist (expired)", "ip", ip)
		}
	}

	// Remove old auth failures outside the window
	for ip, failure := range vm.failedAuthCount {
		if now.Sub(failure.lastFail) > authFailureWindow {
			delete(vm.failedAuthCount, ip)
		}
	}
}

// isIPBlacklisted checks if an IP is currently blacklisted.
func (vm *VIPManager) isIPBlacklisted(ip string) bool {
	vm.failedAuthMu.RLock()
	defer vm.failedAuthMu.RUnlock()

	expiry, exists := vm.blacklistedIPs[ip]
	if !exists {
		return false
	}
	return time.Now().Before(expiry)
}

// recordAuthFailure records an authentication failure for an IP and blacklists if threshold exceeded.
func (vm *VIPManager) recordAuthFailure(ip string) {
	vm.failedAuthMu.Lock()
	defer vm.failedAuthMu.Unlock()

	now := time.Now()

	// Check if already blacklisted
	if expiry, exists := vm.blacklistedIPs[ip]; exists && now.Before(expiry) {
		return // Already blacklisted
	}

	failure, exists := vm.failedAuthCount[ip]
	if !exists {
		failure = &authFailure{
			count:     0,
			firstFail: now,
		}
		vm.failedAuthCount[ip] = failure
	}

	// Reset if outside window
	if now.Sub(failure.firstFail) > authFailureWindow {
		failure.count = 0
		failure.firstFail = now
	}

	failure.count++
	failure.lastFail = now

	// Blacklist if threshold exceeded
	if failure.count >= maxAuthFailures {
		vm.blacklistedIPs[ip] = now.Add(blacklistDuration)
		delete(vm.failedAuthCount, ip) // Clear failure count
		vm.logger.Warnw("IP blacklisted due to too many auth failures",
			"ip", ip,
			"failures", failure.count,
			"duration", blacklistDuration,
		)
	}
}

// resetAuthFailures clears auth failures for an IP (called on successful auth).
func (vm *VIPManager) resetAuthFailures(ip string) {
	vm.failedAuthMu.Lock()
	defer vm.failedAuthMu.Unlock()
	delete(vm.failedAuthCount, ip)
}

// getNextAutoPriority calculates the next priority for a joining node.
// Reserved for dynamic cluster join scenarios with automatic priority assignment.
//
//lint:ignore U1000 Reserved for future auto-priority implementation
func (vm *VIPManager) getNextAutoPriority() int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	// Base priority is 100, each additional node gets +1
	// Master = 100, first backup = 101, second = 102, etc.
	return 100 + len(vm.nodes)
}

// electionCooldownDuration is the minimum time between elections to prevent spam.
// SECURITY: Set to 15 seconds to prevent election flooding attacks.
// An attacker flooding election messages can only trigger elections every 15s,
// which limits the damage from a sustained attack while still allowing
// prompt failover (15s + FailoverTimeout for legitimate failovers).
const electionCooldownDuration = 15 * time.Second

// runElection runs the master election process.
func (vm *VIPManager) runElection() {
	defer func() {
		if r := recover(); r != nil {
			vm.logger.Errorw("PANIC recovered in runElection", "panic", r)
		}
	}()

	// Check election cooldown to prevent election spam attacks.
	// Use CompareAndSwap to ensure only ONE goroutine wins the cooldown race.
	now := time.Now().Unix()
	for {
		lastElection := vm.electionCooldown.Load()
		if now-lastElection < int64(electionCooldownDuration.Seconds()) {
			vm.logger.Debug("Election cooldown active, skipping election")
			return
		}
		if vm.electionCooldown.CompareAndSwap(lastElection, now) {
			break // We won the CAS — proceed with election
		}
		// Another goroutine beat us — re-check cooldown
	}

	vm.mu.Lock()
	if vm.state != StateElection {
		vm.mu.Unlock()
		return
	}
	vm.state = StateFailover
	vm.mu.Unlock()

	// Check flap detector state (but don't record yet — only record on successful failover)
	isFlapping := vm.flapDetector.IsFlapping()

	// FIX VIP-H1: Partition-aware flap detection.
	// When flapping AND witnesses are configured, consult witnesses before proceeding.
	// If witnesses report the current master is reachable, this is likely a partition
	// healing event — abort the election to prevent unnecessary failover.
	if isFlapping && len(vm.witnessNodes) > 0 && vm.partitionDetector != nil {
		responses := vm.partitionDetector.QueryAllWitnesses()
		for addr, resp := range responses {
			if resp.Reachable && resp.MasterID != "" && resp.MasterID != vm.config.NodeID {
				vm.logger.Warnw("FLAP PREVENTION: Witness reports master reachable — aborting election",
					"witness", addr,
					"masterNode", resp.MasterID,
				)
				vm.mu.Lock()
				vm.state = StateRunning
				vm.mu.Unlock()
				return
			}
		}
	}

	// Use dynamic timeout from flap detector (may be increased if flapping)
	electionTimeout := vm.flapDetector.GetCurrentTimeout()

	vm.logger.Infow("Starting master election...",
		"timeout", electionTimeout,
		"isFlapping", isFlapping,
	)

	// Get our coin info for the election message
	vm.mu.RLock()
	supportedCoins := vm.localNode.SupportedCoins
	coinSyncStatus := vm.localNode.CoinSyncStatus
	coinPorts := vm.coinPorts
	vm.mu.RUnlock()

	// Announce our candidacy
	vm.broadcastMessage(ClusterMessage{
		Type:           MsgTypeElection,
		NodeID:         vm.config.NodeID,
		ClusterToken:   vm.clusterToken,
		UserAgent:      SpiralClusterUserAgent,
		Timestamp:      time.Now(),
		Priority:       vm.config.Priority,
		StratumPort:    vm.config.StratumPort,
		CoinPorts:      coinPorts,
		SupportedCoins: supportedCoins,
		CoinSyncStatus: coinSyncStatus,
	})

	// Wait for other candidates (using dynamic timeout), but allow cancellation
	select {
	case <-time.After(electionTimeout):
	case <-vm.ctx.Done():
		return
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	// CRITICAL: We can only win the election if we are fully synced
	// An unsynced node cannot become master - it would provide stale block templates
	localSynced := vm.isLocalNodeFullySyncedLocked()
	if !localSynced {
		vm.logger.Warnw("Cannot win election: local node not fully synced, staying as BACKUP",
			"supportedCoins", vm.localNode.SupportedCoins,
			"coinSyncStatus", vm.localNode.CoinSyncStatus,
		)
		vm.role = RoleBackup
		vm.state = StateRunning
		return
	}

	// Check if we're still the best candidate among synced nodes
	// Priority: lower = better, but only synced nodes can compete
	weWin := true
	for id, node := range vm.nodes {
		if id == vm.config.NodeID {
			continue
		}
		// Only consider nodes that are healthy AND fully synced
		// Tiebreaker: if equal priority, lower NodeID wins (deterministic)
		if node.IsHealthy && vm.isNodeFullySynced(node) &&
			(node.Priority < vm.config.Priority ||
				(node.Priority == vm.config.Priority && id < vm.config.NodeID)) {
			weWin = false
			break
		}
	}

	if weWin {
		// FIX H-1 (CRITICAL): SPLIT-BRAIN PREVENTION
		// Before becoming master, do final verification that no other node
		// is already serving on the VIP. This catches the case where UDP
		// election messages were lost and another node already won.
		//
		// IMPORTANT: ARP detection alone is NOT sufficient to confirm split-brain.
		// Keepalived VRRP can assign the VIP to a node's interface even when no
		// stratum VIP manager on that node has claimed MASTER. This happens after
		// failover recovery: keepalived holds the VIP but stratum restarts fresh
		// as BACKUP. If we abort on ARP alone, the election winner (which may be
		// on a DIFFERENT node than keepalived) can never claim MASTER — deadlock.
		// Solution: ARP + remote master confirmation. Only abort if the VIP is in
		// use AND a remote node actually reports being stratum MASTER.
		if vm.isVIPInUse() {
			if vm.checkRemoteMasterExists() {
				vm.logger.Warnw("SPLIT-BRAIN PREVENTED: VIP in use AND remote master confirmed — aborting election",
					"vip", vm.config.VIPAddress)
				vm.role = RoleBackup
				vm.state = StateRunning
				weWin = false
			} else {
				vm.logger.Infow("VIP detected via ARP but no remote stratum master — proceeding (stale keepalived binding)",
					"vip", vm.config.VIPAddress)
			}
		}

		// Also check via HTTP if any reachable node reports being master,
		// even if ARP didn't detect the VIP (covers different subnet, ARP failure).
		// NOTE: checkRemoteMasterExists temporarily releases vm.mu for HTTP I/O.
		// After it returns, the lock is re-acquired but state may have changed.
		if weWin && vm.checkRemoteMasterExists() {
			vm.logger.Warnw("SPLIT-BRAIN PREVENTED: Remote node reports master status — aborting election")
			vm.role = RoleBackup
			vm.state = StateRunning
			weWin = false
		}

		// Re-validate after checkRemoteMasterExists — state may have changed during unlock.
		// If state is no longer StateFailover, another election or event already resolved
		// the cluster state. Return immediately — do NOT fall through to the else branch
		// which would set role=BACKUP and clobber a successful MASTER promotion.
		if weWin && vm.state != StateFailover {
			vm.logger.Infow("Election aborted: state changed during remote master check",
				"state", vm.state)
			return
		}
		if weWin && vm.masterID != "" && vm.masterID != vm.config.NodeID {
			vm.logger.Infow("Election aborted: another node became master during remote check",
				"masterID", vm.masterID)
			vm.role = RoleBackup
			vm.state = StateRunning
			weWin = false
		}
		// Re-check node priorities — vm.nodes may have changed during the HTTP unlock
		// window (new higher-priority node joined, or existing node became synced).
		if weWin {
			for id, node := range vm.nodes {
				if id == vm.config.NodeID {
					continue
				}
				if node.IsHealthy && vm.isNodeFullySynced(node) &&
					(node.Priority < vm.config.Priority ||
						(node.Priority == vm.config.Priority && id < vm.config.NodeID)) {
					vm.logger.Infow("Election aborted: higher-priority node appeared during remote check",
						"higherPriorityNode", id,
						"theirPriority", node.Priority,
						"ourPriority", vm.config.Priority)
					vm.role = RoleBackup
					vm.state = StateRunning
					weWin = false
					break
				}
			}
		}
	}

	if weWin {
		// Record failover NOW — only successful elections count for flap detection.
		// Aborted elections (split-brain prevented, VIP in use) should not trigger
		// exponential backoff that delays legitimate failover.
		if vm.flapDetector.RecordFailover() {
			vm.logger.Warnw("VIP flapping detected after successful election",
				"currentTimeout", vm.flapDetector.GetCurrentTimeout(),
				"backoffLevel", vm.flapDetector.GetBackoffLevel(),
			)
		}
		vm.failoverCount.Add(1)
		vm.becomeMasterLocked()
	} else {
		oldRole := vm.role
		vm.role = RoleBackup
		vm.state = StateRunning
		// Trigger database failover if role actually changed
		if vm.onDatabaseFailover != nil && oldRole != vm.role {
			vm.wg.Add(1)
			go func() {
				defer vm.wg.Done()
				defer func() {
					if r := recover(); r != nil {
						vm.logger.Errorw("PANIC recovered in onDatabaseFailover callback", "panic", r)
					}
				}()
				vm.onDatabaseFailover(false)
			}()
		}
	}
}

// broadcastMessage sends an encrypted message to all nodes via broadcast.
func (vm *VIPManager) broadcastMessage(msg ClusterMessage) {
	// Guard against nil connection (can happen during tests or when not fully started)
	if vm.discoveryConn == nil {
		vm.logger.Debug("Cannot broadcast: discovery connection not initialized")
		return
	}

	data, err := vm.encryptAndWrapMessage(msg)
	if err != nil {
		vm.logger.Warnw("Failed to encrypt broadcast message", "error", err)
		return
	}

	// Broadcast to local subnet
	broadcastAddr := &net.UDPAddr{
		IP:   net.IPv4bcast,
		Port: vm.config.DiscoveryPort,
	}

	_, _ = vm.discoveryConn.WriteTo(data, broadcastAddr) // #nosec G104

	// Also try subnet broadcast using the interface's actual netmask (NOT VIPNetmask).
	// VIPNetmask is /32 (host-only for the VIP address), but the broadcast must use
	// the real interface netmask (e.g., /24) to reach all nodes on the subnet.
	if vm.localNode.Host != "" && vm.interfaceNetmask > 0 {
		ip := net.ParseIP(vm.localNode.Host)
		if ip != nil {
			ip4 := ip.To4()
			if ip4 != nil {
				mask := net.CIDRMask(vm.interfaceNetmask, 32)
				// Broadcast = network OR (NOT mask)
				bcast := make(net.IP, 4)
				for i := 0; i < 4; i++ {
					bcast[i] = ip4[i] | ^mask[i]
				}
				subnetAddr := &net.UDPAddr{
					IP:   bcast,
					Port: vm.config.DiscoveryPort,
				}
				_, _ = vm.discoveryConn.WriteTo(data, subnetAddr) // #nosec G104
			}
		}
	}
}

// sendToNode sends an encrypted message to a specific node.
func (vm *VIPManager) sendToNode(addr *net.UDPAddr, msg ClusterMessage) {
	data, err := vm.encryptAndWrapMessage(msg)
	if err != nil {
		vm.logger.Warnw("Failed to encrypt message", "error", err)
		return
	}
	_, _ = vm.discoveryConn.WriteTo(data, addr) // #nosec G104
}

// encryptAndWrapMessage encrypts a ClusterMessage and wraps it for transport.
func (vm *VIPManager) encryptAndWrapMessage(msg ClusterMessage) ([]byte, error) {
	// SECURITY: Read cluster token under lock to prevent race condition
	vm.mu.RLock()
	token := vm.clusterToken
	vm.mu.RUnlock()

	return vm.encryptAndWrapMessageWithToken(msg, token)
}

// encryptAndWrapMessageWithToken encrypts a ClusterMessage using the provided token.
// Use this variant when the caller already holds vm.mu (Lock or RLock) to avoid
// deadlock — encryptAndWrapMessage takes vm.mu.RLock() internally, which deadlocks
// if the caller already holds vm.mu.Lock().
func (vm *VIPManager) encryptAndWrapMessageWithToken(msg ClusterMessage, token string) ([]byte, error) {
	// If no cluster token yet (initial discovery), send unencrypted with special marker
	if token == "" {
		// For announce messages before we have a token, use a minimal unencrypted format
		if msg.Type == MsgTypeAnnounce {
			wrapper := EncryptedMessage{
				Version:   0, // Version 0 = unencrypted announce
				NodeID:    msg.NodeID,
				Timestamp: time.Now().Unix(),
				Encrypted: "", // Empty = unencrypted
			}
			return json.Marshal(wrapper)
		}
		return nil, errors.New("no cluster token available for encryption")
	}

	// SECURITY: Strip the cluster token from the message body before encrypting.
	// The token is redundant in the encrypted path because AES-GCM encryption
	// already proves knowledge of the shared secret (the token derives the key).
	// Removing it prevents the plaintext token from appearing in the ciphertext,
	// reducing exposure if the encrypted payload is ever compromised.
	msg.ClusterToken = ""

	// Serialize the message
	plaintext, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	// Encrypt the message
	encrypted, err := encryptMessage(plaintext, token)
	if err != nil {
		return nil, err
	}

	// Wrap in transport format
	wrapper := EncryptedMessage{
		Version:   1, // Version 1 = AES-256-GCM encrypted
		Encrypted: encrypted,
		NodeID:    msg.NodeID,
		Timestamp: time.Now().Unix(),
	}

	return json.Marshal(wrapper)
}

// decryptAndUnwrapMessage decrypts a received message.
func (vm *VIPManager) decryptAndUnwrapMessage(data []byte) (*ClusterMessage, error) {
	var wrapper EncryptedMessage
	if err := json.Unmarshal(data, &wrapper); err != nil {
		// Try legacy unencrypted format for backwards compatibility
		var msg ClusterMessage
		if err2 := json.Unmarshal(data, &msg); err2 != nil {
			return nil, fmt.Errorf("failed to parse message: %w", err)
		}
		// SECURITY: Legacy unencrypted message - validate cluster token.
		// Without this check, an attacker could bypass encryption entirely
		// by sending plain JSON messages that skip the AES-GCM path.
		if vm.clusterToken != "" && !constantTimeCompare(msg.ClusterToken, vm.clusterToken) {
			return nil, fmt.Errorf("legacy message: invalid cluster token")
		}
		return &msg, nil
	}

	// SECURITY: Check for replay attacks (reject messages older than 30 seconds)
	// Reduced from 5 minutes to limit replay attack window for heartbeat messages
	// 30 seconds is sufficient for network latency while minimizing attack surface
	msgTime := time.Unix(wrapper.Timestamp, 0)
	if time.Since(msgTime) > 30*time.Second {
		return nil, errors.New("message too old (possible replay attack)")
	}
	if msgTime.After(time.Now().Add(30 * time.Second)) {
		return nil, errors.New("message from future (clock skew or attack)")
	}

	// SECURITY: Deduplicate messages to prevent replay within time window
	// Use the encrypted payload as the message fingerprint (unique per message).
	// Using the first 64 chars (32 bytes of base64-encoded AES-GCM ciphertext) provides
	// sufficient uniqueness: each message has a fresh 12-byte nonce, so the probability of
	// a 64-char prefix collision is negligible (~2^-128). Full hash is unnecessary overhead
	// for a 30-second dedup window with low message rates.
	if wrapper.Encrypted != "" {
		msgHash := wrapper.Encrypted[:min(64, len(wrapper.Encrypted))]
		if !vm.checkAndRecordMessage(msgHash) {
			return nil, errors.New("duplicate message (replay attack)")
		}
	}

	// Version 0 = unencrypted announce (for initial discovery ONLY)
	// SECURITY: Once a cluster has a token, we still accept v0 announce messages
	// but they can ONLY trigger an announce response - they cannot join the cluster
	// without the token. The new node must have the token pre-configured to decrypt
	// our encrypted JoinAccept response.
	if wrapper.Version == 0 && wrapper.Encrypted == "" {
		// Only allow Announce messages via unencrypted channel
		// This lets nodes discover the cluster exists, but they can't join without token
		return &ClusterMessage{
			Type:      MsgTypeAnnounce,
			NodeID:    wrapper.NodeID,
			Timestamp: msgTime,
		}, nil
	}

	// Version 1 = AES-256-GCM encrypted
	if wrapper.Version != 1 {
		return nil, fmt.Errorf("unsupported message version: %d", wrapper.Version)
	}

	// SECURITY: Read cluster token under lock to prevent race condition
	vm.mu.RLock()
	token := vm.clusterToken
	vm.mu.RUnlock()

	if token == "" {
		return nil, errors.New("no cluster token for decryption")
	}

	// Decrypt the message
	plaintext, err := decryptMessage(wrapper.Encrypted, token)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	var msg ClusterMessage
	if err := json.Unmarshal(plaintext, &msg); err != nil {
		return nil, fmt.Errorf("failed to parse decrypted message: %w", err)
	}

	// BUG FIX: Restore ClusterToken after decryption.
	// The token was stripped before encryption (line 3510) to avoid exposing it in ciphertext.
	// Successful decryption proves the sender knows the token, so restore it here
	// to pass the authentication check in handleMessage().
	msg.ClusterToken = token

	return &msg, nil
}

// SetRoleChangeHandler sets the callback for role changes.
func (vm *VIPManager) SetRoleChangeHandler(handler func(oldRole, newRole Role)) {
	vm.mu.Lock()
	vm.onRoleChange = handler
	vm.mu.Unlock()
}

// SetVIPAcquiredHandler sets the callback for VIP acquisition.
func (vm *VIPManager) SetVIPAcquiredHandler(handler func(vip string)) {
	vm.mu.Lock()
	vm.onVIPAcquired = handler
	vm.mu.Unlock()
}

// SetVIPReleasedHandler sets the callback for VIP release.
func (vm *VIPManager) SetVIPReleasedHandler(handler func(vip string)) {
	vm.mu.Lock()
	vm.onVIPReleased = handler
	vm.mu.Unlock()
}

// checkAndRecordMessage checks if a message hash has been seen before and records it.
// SECURITY: This prevents replay attacks within the time window.
// Returns true if this is a new message, false if it's a replay.
//
// OPTIMIZATION: Uses size-based cleanup with periodic full cleanup to prevent
// unbounded memory growth under high message rates while minimizing lock contention.
func (vm *VIPManager) checkAndRecordMessage(msgHash string) bool {
	vm.seenMessagesMu.Lock()
	defer vm.seenMessagesMu.Unlock()

	now := time.Now()

	// Check if message was already seen
	if _, seen := vm.seenMessages[msgHash]; seen {
		return false // Replay detected
	}

	// Record this message
	vm.seenMessages[msgHash] = now

	// OPTIMIZATION: Only run cleanup when map size exceeds threshold
	// This prevents O(n) cleanup on every message under normal load
	const maxSeenMessages = 10000
	const cleanupThreshold = maxSeenMessages / 2

	if len(vm.seenMessages) > maxSeenMessages {
		// Emergency cleanup - remove oldest entries
		// Find entries older than 60 seconds first
		oldEntries := 0
		for hash, seenAt := range vm.seenMessages {
			if now.Sub(seenAt) > 60*time.Second {
				delete(vm.seenMessages, hash)
				oldEntries++
			}
		}

		// If still over threshold, remove entries older than 30 seconds
		if len(vm.seenMessages) > cleanupThreshold && oldEntries < 100 {
			for hash, seenAt := range vm.seenMessages {
				if now.Sub(seenAt) > 30*time.Second {
					delete(vm.seenMessages, hash)
				}
			}
		}

		if len(vm.seenMessages) > cleanupThreshold {
			vm.logger.Warnw("High message rate detected in HA cluster",
				"seenMessagesCount", len(vm.seenMessages),
				"threshold", maxSeenMessages,
			)
		}
	}

	return true // New message
}

// SetNodeJoinedHandler sets the callback for node joins.
func (vm *VIPManager) SetNodeJoinedHandler(handler func(node *ClusterNode)) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.onNodeJoined = handler
}

// SetNodeLeftHandler sets the callback for node departures.
func (vm *VIPManager) SetNodeLeftHandler(handler func(node *ClusterNode)) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.onNodeLeft = handler
}

// SetDatabaseFailoverHandler sets the callback for database failover coordination.
// When this node becomes MASTER (isMaster=true), the callback should promote local DB to primary.
// When this node becomes BACKUP (isMaster=false), the callback should failover to remote primary.
// This enables automatic database HA synchronized with VIP failover.
func (vm *VIPManager) SetDatabaseFailoverHandler(handler func(isMaster bool)) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.onDatabaseFailover = handler
}

// SetSupportedCoins sets the list of coins this node supports.
// This is used in HA failover decisions - a node will only failover to another
// node that supports the same coins.
func (vm *VIPManager) SetSupportedCoins(coins []string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	vm.localNode.SupportedCoins = coins
	if vm.localNode.CoinSyncStatus == nil {
		vm.localNode.CoinSyncStatus = make(map[string]*CoinSyncStatus)
	}

	// Initialize sync status for new coins
	for _, coin := range coins {
		if _, exists := vm.localNode.CoinSyncStatus[coin]; !exists {
			vm.localNode.CoinSyncStatus[coin] = &CoinSyncStatus{
				Coin:     coin,
				SyncPct:  0,
				IsSynced: false,
			}
		}
	}

	vm.logger.Infow("Updated supported coins",
		"coins", coins,
		"nodeId", vm.config.NodeID,
	)
}

// UpdateCoinSyncStatus updates the blockchain sync status for a specific coin.
// syncPct should be 0-100, blockHeight is the current block height.
// A coin is considered "synced" when syncPct >= SyncThresholdPercent (99.9%).
// Input validation: coin must be 1-10 chars, syncPct 0-100, blockHeight >= 0.
func (vm *VIPManager) UpdateCoinSyncStatus(coin string, syncPct float64, blockHeight int64) {
	// Input validation (security: prevent DoS via malformed data)
	if len(coin) == 0 || len(coin) > MaxCoinTickerLength {
		return // Invalid coin ticker
	}
	// Clamp syncPct to valid range (handles NaN, Inf, negative, >100)
	if syncPct < 0 || syncPct != syncPct { // syncPct != syncPct detects NaN
		syncPct = 0
	} else if syncPct > 100 {
		syncPct = 100
	}
	if blockHeight < 0 {
		blockHeight = 0
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Check if we've hit the coin limit
	if vm.localNode.CoinSyncStatus == nil {
		vm.localNode.CoinSyncStatus = make(map[string]*CoinSyncStatus)
	}
	if _, exists := vm.localNode.CoinSyncStatus[coin]; !exists {
		if len(vm.localNode.CoinSyncStatus) >= MaxSupportedCoins {
			return // Already at max coins, don't add more
		}
	}

	status := vm.localNode.CoinSyncStatus[coin]
	if status == nil {
		status = &CoinSyncStatus{Coin: coin}
		vm.localNode.CoinSyncStatus[coin] = status
	}

	status.SyncPct = syncPct
	status.BlockHeight = blockHeight
	status.IsSynced = syncPct >= SyncThresholdPercent

	// Also ensure coin is in supported coins list
	found := false
	for _, c := range vm.localNode.SupportedCoins {
		if c == coin {
			found = true
			break
		}
	}
	if !found && len(vm.localNode.SupportedCoins) < MaxSupportedCoins {
		vm.localNode.SupportedCoins = append(vm.localNode.SupportedCoins, coin)
	}
}

// GetCoinSyncStatus returns a copy of the sync status for a specific coin on this node.
// Returns nil if the coin is not tracked.
func (vm *VIPManager) GetCoinSyncStatus(coin string) *CoinSyncStatus {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	if vm.localNode.CoinSyncStatus == nil {
		return nil
	}
	status := vm.localNode.CoinSyncStatus[coin]
	if status == nil {
		return nil
	}
	// Return a copy to prevent external modification
	statusCopy := *status
	return &statusCopy
}

// GetSupportedCoins returns a copy of the list of coins this node supports.
func (vm *VIPManager) GetSupportedCoins() []string {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	// Return a copy to prevent external modification
	coins := make([]string, len(vm.localNode.SupportedCoins))
	copy(coins, vm.localNode.SupportedCoins)
	return coins
}

// IsCoinSynced returns true if the specified coin's blockchain is fully synced on this node.
func (vm *VIPManager) IsCoinSynced(coin string) bool {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	if vm.localNode.CoinSyncStatus == nil {
		return false
	}
	status := vm.localNode.CoinSyncStatus[coin]
	if status == nil {
		return false
	}
	return status.IsSynced
}

// isLocalNodeFullySynced returns true if ALL supported coins on this node are fully synced.
// A node must be 100% synced on all its coins before it can become master.
// This prevents an unsynced node from providing stale block templates to miners.
// Reserved for sync-aware master election feature.
//
//lint:ignore U1000 Reserved for sync-aware master election
func (vm *VIPManager) isLocalNodeFullySynced() bool {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.isLocalNodeFullySyncedLocked()
}

// isLocalNodeFullySyncedLocked is the lock-free version (caller must hold vm.mu).
func (vm *VIPManager) isLocalNodeFullySyncedLocked() bool {
	// If no coins are configured, consider it not synced (can't mine without coins)
	if vm.localNode == nil || len(vm.localNode.SupportedCoins) == 0 {
		return false
	}

	// Check that ALL supported coins are synced
	for _, coin := range vm.localNode.SupportedCoins {
		if vm.localNode.CoinSyncStatus == nil {
			return false
		}
		status := vm.localNode.CoinSyncStatus[coin]
		if status == nil || !status.IsSynced {
			return false
		}
	}
	return true
}

// isNodeFullySynced checks if a remote node is fully synced on all its supported coins.
func (vm *VIPManager) isNodeFullySynced(node *ClusterNode) bool {
	if node == nil || len(node.SupportedCoins) == 0 {
		return false
	}
	for _, coin := range node.SupportedCoins {
		if node.CoinSyncStatus == nil {
			return false
		}
		status := node.CoinSyncStatus[coin]
		if status == nil || !status.IsSynced {
			return false
		}
	}
	return true
}

// NodeSupportsCoin checks if a specific node supports a given coin and has it synced.
// Returns (supports, synced) where:
//   - supports: true if the node has this coin in its supported coins list
//   - synced: true if the node's blockchain for this coin is fully synced
func (vm *VIPManager) NodeSupportsCoin(nodeID string, coin string) (supports bool, synced bool) {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	node, exists := vm.nodes[nodeID]
	if !exists {
		return false, false
	}

	// Check if coin is in supported list
	for _, c := range node.SupportedCoins {
		if c == coin {
			supports = true
			break
		}
	}

	if !supports {
		return false, false
	}

	// Check sync status
	if node.CoinSyncStatus != nil {
		if status := node.CoinSyncStatus[coin]; status != nil {
			synced = status.IsSynced
		}
	}

	return supports, synced
}

// GetNodesForCoin returns all healthy nodes that support a specific coin and have it synced.
// This is used for coin-aware failover decisions.
func (vm *VIPManager) GetNodesForCoin(coin string) []*ClusterNode {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	var nodes []*ClusterNode
	for _, node := range vm.nodes {
		if !node.IsHealthy {
			continue
		}

		// Check if node supports this coin
		supportsCoin := false
		for _, c := range node.SupportedCoins {
			if c == coin {
				supportsCoin = true
				break
			}
		}
		if !supportsCoin {
			continue
		}

		// Check if coin is synced
		if node.CoinSyncStatus != nil {
			if status := node.CoinSyncStatus[coin]; status != nil && status.IsSynced {
				nodes = append(nodes, node)
			}
		}
	}

	// Sort by priority (lower = higher priority)
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Priority < nodes[j].Priority
	})

	return nodes
}

// GetBestNodeForCoin returns the highest priority healthy node that supports a given coin.
// Returns nil if no suitable node is found.
func (vm *VIPManager) GetBestNodeForCoin(coin string) *ClusterNode {
	nodes := vm.GetNodesForCoin(coin)
	if len(nodes) == 0 {
		return nil
	}
	return nodes[0]
}
