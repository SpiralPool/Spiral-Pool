// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package nodemanager provides multi-node management with automatic failover.
//
// NodeManager maintains connections to multiple daemon nodes for a single coin,
// monitors their health, and automatically fails over when the primary becomes
// unhealthy.
package nodemanager

import (
	"errors"
	"time"
)

// Common errors
var (
	ErrNoHealthyNodes = errors.New("no healthy nodes available")
	ErrNodeNotFound   = errors.New("node not found")
	ErrAllNodesFailed = errors.New("all nodes failed to respond")
)

// NodeState represents the current state of a node.
type NodeState int

const (
	NodeStateUnknown NodeState = iota
	NodeStateHealthy
	NodeStateDegraded
	NodeStateUnhealthy
	NodeStateOffline
)

func (s NodeState) String() string {
	switch s {
	case NodeStateHealthy:
		return "healthy"
	case NodeStateDegraded:
		return "degraded"
	case NodeStateUnhealthy:
		return "unhealthy"
	case NodeStateOffline:
		return "offline"
	default:
		return "unknown"
	}
}

// OperationType identifies the type of operation being performed.
// Some operations require the primary node, others can use any healthy node.
type OperationType int

const (
	OpRead        OperationType = iota // Read operations (getblocktemplate, getinfo)
	OpSubmitBlock                      // Block submission (requires primary)
	OpSubmitShare                      // Share validation (any healthy node)
)

func (o OperationType) String() string {
	switch o {
	case OpRead:
		return "read"
	case OpSubmitBlock:
		return "submit_block"
	case OpSubmitShare:
		return "submit_share"
	default:
		return "unknown"
	}
}

// RequiresPrimary returns true if this operation requires the primary node.
func (o OperationType) RequiresPrimary() bool {
	return o == OpSubmitBlock
}

// HealthScore tracks the health metrics for a node.
type HealthScore struct {
	Score            float64       // 0.0 (dead) to 1.0 (perfect)
	State            NodeState     // Current state
	ResponseTimeAvg  time.Duration // Average response time (last 10 requests)
	SuccessRate      float64       // Success rate (last 100 requests)
	BlockHeight      uint64        // Current block height
	IsSynced         bool          // True if caught up with network
	Connections      int           // Number of peer connections
	LastHealthCheck  time.Time     // When health was last checked
	LastSuccess      time.Time     // Last successful request
	LastError        error         // Last error (nil if healthy)
	ConsecutiveFails int           // Consecutive failed health checks
}

// NodeConfig contains configuration for a single daemon node.
type NodeConfig struct {
	ID       string `yaml:"id"`       // Unique identifier (e.g., "primary", "backup-1")
	Host     string `yaml:"host"`     // Hostname or IP
	Port     int    `yaml:"port"`     // RPC port
	User     string `yaml:"user"`     // RPC username
	Password string `yaml:"password"` // RPC password
	Priority int    `yaml:"priority"` // Lower = preferred (0 = highest priority)
	Weight   int    `yaml:"weight"`   // For load balancing (higher = more traffic)

	// ZMQ configuration (optional)
	ZMQ *ZMQConfig `yaml:"zmq,omitempty"`
}

// ZMQConfig contains ZMQ configuration for a node.
type ZMQConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"` // e.g., "tcp://127.0.0.1:28332"

	// Timing configuration - tuned per-coin based on block time
	// Fast coins (DGB 15s) need aggressive timing, slow coins (BTC 600s) use relaxed timing
	ReconnectInitial    time.Duration `yaml:"reconnect_initial,omitempty"`     // Initial reconnect delay
	ReconnectMax        time.Duration `yaml:"reconnect_max,omitempty"`         // Maximum reconnect delay
	ReconnectFactor     float64       `yaml:"reconnect_factor,omitempty"`      // Exponential backoff factor
	FailureThreshold    time.Duration `yaml:"failure_threshold,omitempty"`     // How long to fail before polling fallback
	StabilityPeriod     time.Duration `yaml:"stability_period,omitempty"`      // Time before marking stable
	HealthCheckInterval time.Duration `yaml:"health_check_interval,omitempty"` // How often to check health
}

// FailoverEvent records a failover occurrence.
type FailoverEvent struct {
	FromNodeID string    `json:"from_node_id"`
	ToNodeID   string    `json:"to_node_id"`
	Reason     string    `json:"reason"`
	OldScore   float64   `json:"old_score"`
	NewScore   float64   `json:"new_score"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ManagerStats contains statistics about the node manager.
type ManagerStats struct {
	Coin          string             // Coin symbol
	TotalNodes    int                // Total configured nodes
	HealthyNodes  int                // Currently healthy nodes
	PrimaryNodeID string             // Current primary node
	FailoverCount int                // Total failovers since start
	LastFailover  *FailoverEvent     // Most recent failover
	NodeHealths   map[string]float64 // Health scores by node ID
	BlockHeight   uint64             // Best known block height across all nodes
	PeerCount     int                // Peer count from primary node (-1 if unavailable)
}

// HealthCheckResult contains the result of a single health check.
type HealthCheckResult struct {
	NodeID       string
	Success      bool
	ResponseTime time.Duration
	BlockHeight  uint64
	IsSynced     bool
	Connections  int
	Error        error
	CheckedAt    time.Time
}
