// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package nodemanager

import (
	"context"
	"sync"
	"time"
)

// HealthMonitor performs health checks on nodes and updates their scores.
type HealthMonitor struct {
	// Configuration
	checkInterval     time.Duration // How often to check each node
	healthyThreshold  float64       // Score above this = healthy
	degradedThreshold float64       // Score above this = degraded, below = unhealthy
	offlineThreshold  int           // Consecutive fails before marking offline

	// Response time tracking
	responseTimeWindow int // Number of samples for rolling average

	mu sync.RWMutex
}

// DefaultHealthMonitor creates a health monitor with default settings.
func DefaultHealthMonitor() *HealthMonitor {
	return &HealthMonitor{
		checkInterval:      10 * time.Second,
		healthyThreshold:   0.8,
		degradedThreshold:  0.5,
		offlineThreshold:   5,
		responseTimeWindow: 10,
	}
}

// CalculateHealth computes the health score for a node based on check results.
// The score is a weighted combination of multiple factors.
// FIX: Acquires node.mu.RLock to snapshot health fields, preventing data races
// with concurrent RecordSuccess/RecordFailure/PerformHealthCheck writers.
func (m *HealthMonitor) CalculateHealth(node *ManagedNode, bestHeight uint64) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Snapshot health fields under node lock to prevent races with writers
	node.mu.RLock()
	lastError := node.Health.LastError
	lastSuccess := node.Health.LastSuccess
	isSynced := node.Health.IsSynced
	blockHeight := node.Health.BlockHeight
	responseTimeAvg := node.Health.ResponseTimeAvg
	successRate := node.Health.SuccessRate
	priority := node.Priority
	node.mu.RUnlock()

	// Weight factors (must sum to 1.0)
	const (
		wAvailability = 0.35 // Is node responding?
		wSyncStatus   = 0.25 // Is node synced with network?
		wResponseTime = 0.20 // How fast does it respond?
		wSuccessRate  = 0.15 // Recent request success rate
		wPriority     = 0.05 // Configured preference
	)

	score := 0.0

	// Availability: Can we reach the node?
	if lastError == nil && time.Since(lastSuccess) < 60*time.Second {
		score += wAvailability * 1.0
	} else if time.Since(lastSuccess) < 2*time.Minute {
		score += wAvailability * 0.5 // Recently worked
	}
	// If last success > 2 minutes ago, availability score = 0

	// Sync status: Is it caught up with the network?
	if isSynced && blockHeight > 0 {
		score += wSyncStatus * 1.0
	} else if blockHeight > 0 && bestHeight > 0 {
		// Partial score based on how close to best height
		// Handle case where node is ahead of "best" (can happen during reorgs)
		if blockHeight >= bestHeight {
			score += wSyncStatus * 1.0 // Node is ahead or at best - that's great
		} else {
			behind := bestHeight - blockHeight
			if behind <= 1 {
				score += wSyncStatus * 0.9 // 1 block behind is fine
			} else if behind <= 5 {
				score += wSyncStatus * 0.7
			} else if behind <= 10 {
				score += wSyncStatus * 0.3
			}
			// > 10 blocks behind = 0 sync score
		}
	}

	// Response time: < 100ms = 1.0, > 2000ms = 0.0
	if responseTimeAvg > 0 {
		rtMs := float64(responseTimeAvg.Milliseconds())
		if rtMs < 100 {
			score += wResponseTime * 1.0
		} else if rtMs < 2000 {
			rtScore := 1.0 - (rtMs-100)/1900.0 // Linear interpolation
			score += wResponseTime * rtScore
		}
		// > 2000ms = 0 response time score
	}

	// Success rate from recent requests
	score += wSuccessRate * successRate

	// Priority bonus for preferred nodes (priority 0-10, lower is better)
	if priority <= 10 {
		priorityScore := 1.0 - (float64(priority) / 10.0)
		score += wPriority * priorityScore
	}

	return score
}

// UpdateState updates the node's state based on its health score.
// FIX: Acquires node.mu.Lock to read ConsecutiveFails/Score and write State,
// preventing data races with concurrent SelectNode/maybeFailover readers.
func (m *HealthMonitor) UpdateState(node *ManagedNode) {
	m.mu.RLock()
	healthyThreshold := m.healthyThreshold
	degradedThreshold := m.degradedThreshold
	offlineThreshold := m.offlineThreshold
	m.mu.RUnlock()

	node.mu.Lock()
	defer node.mu.Unlock()

	// Check for offline first (consecutive failures)
	if node.Health.ConsecutiveFails >= offlineThreshold {
		node.Health.State = NodeStateOffline
		return
	}

	// Then check score thresholds
	switch {
	case node.Health.Score >= healthyThreshold:
		node.Health.State = NodeStateHealthy
	case node.Health.Score >= degradedThreshold:
		node.Health.State = NodeStateDegraded
	default:
		node.Health.State = NodeStateUnhealthy
	}
}

// RecordSuccess records a successful request to a node.
// BUG FIX: Also ensures Score is at least 0.5 (neutral) to prevent SelectNode
// from rejecting a working node due to stale Score. Full score recalculation
// happens every 10 seconds in checkAllNodes(), but during recovery we need
// the node to be selectable immediately.
func (m *HealthMonitor) RecordSuccess(node *ManagedNode, responseTime time.Duration) {
	node.mu.Lock()
	defer node.mu.Unlock()

	node.Health.LastSuccess = time.Now()
	node.Health.LastError = nil
	node.Health.ConsecutiveFails = 0

	// Update rolling average response time
	m.updateResponseTime(node, responseTime)

	// Update success rate
	m.updateSuccessRate(node, true)

	// BUG FIX: Ensure Score is at least 0.5 (neutral) after a success.
	// This prevents SelectNode from rejecting a working node due to stale
	// low Score from previous failures. The full score recalculation in
	// checkAllNodes() will set the accurate value on the next 10s tick.
	if node.Health.Score < 0.5 {
		node.Health.Score = 0.5
	}
}

// RecordFailure records a failed request to a node.
func (m *HealthMonitor) RecordFailure(node *ManagedNode, err error) {
	node.mu.Lock()
	defer node.mu.Unlock()

	node.Health.LastError = err
	node.Health.ConsecutiveFails++

	// Update success rate
	m.updateSuccessRate(node, false)
}

// updateResponseTime updates the rolling average response time.
func (m *HealthMonitor) updateResponseTime(node *ManagedNode, rt time.Duration) {
	// Exponential moving average with alpha = 0.2
	const alpha = 0.2
	if node.Health.ResponseTimeAvg == 0 {
		node.Health.ResponseTimeAvg = rt
	} else {
		oldAvg := float64(node.Health.ResponseTimeAvg)
		newAvg := alpha*float64(rt) + (1-alpha)*oldAvg
		node.Health.ResponseTimeAvg = time.Duration(newAvg)
	}
}

// updateSuccessRate updates the rolling success rate.
func (m *HealthMonitor) updateSuccessRate(node *ManagedNode, success bool) {
	// Exponential moving average for success rate
	const alpha = 0.02 // Slower decay for more stability

	successVal := 0.0
	if success {
		successVal = 1.0
	}

	if node.Health.SuccessRate == 0 && !success {
		// First failure, start at 0
		node.Health.SuccessRate = 0
	} else if node.Health.SuccessRate == 0 && success {
		// First success, start at 1
		node.Health.SuccessRate = 1.0
	} else {
		node.Health.SuccessRate = alpha*successVal + (1-alpha)*node.Health.SuccessRate
	}
}

// PerformHealthCheck performs a health check on a single node.
func (m *HealthMonitor) PerformHealthCheck(ctx context.Context, node *ManagedNode) *HealthCheckResult {
	start := time.Now()

	result := &HealthCheckResult{
		NodeID:    node.ID,
		CheckedAt: start,
	}

	// Get blockchain info from the node
	bcInfo, err := node.Client.GetBlockchainInfo(ctx)
	result.ResponseTime = time.Since(start)

	if err != nil {
		result.Success = false
		result.Error = err
		m.RecordFailure(node, err)
		return result
	}

	result.Success = true
	result.BlockHeight = bcInfo.Blocks
	result.IsSynced = !bcInfo.InitialBlockDownload
	result.Error = nil

	// Record success
	m.RecordSuccess(node, result.ResponseTime)

	// Get peer count from getnetworkinfo (non-fatal — don't fail health check on error)
	netInfo, netErr := node.Client.GetNetworkInfo(ctx)
	if netErr == nil {
		result.Connections = netInfo.Connections
	}

	// Update node health data
	node.mu.Lock()
	node.Health.BlockHeight = bcInfo.Blocks
	node.Health.IsSynced = !bcInfo.InitialBlockDownload
	node.Health.LastHealthCheck = time.Now()
	if netErr == nil {
		node.Health.Connections = netInfo.Connections
	}
	node.mu.Unlock()

	return result
}
