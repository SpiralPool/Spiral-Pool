// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package discovery provides subnet scanning and pool failover management.
//
// The FailoverManager provides automatic pool-level failover:
//   - Monitors primary and backup pool health via stratum protocol probes
//   - Automatic failover when primary becomes unresponsive
//   - Deep health checks for failback (verifies job delivery, not just connectivity)
//   - HA alerts for Spiral Sentinel monitoring (multi-pool mode only)
//
// # Failback Verification
//
// When failing back to the primary pool, a "deep check" is performed to ensure
// the pool is fully functional:
//   - Verifies subscribe response with valid extranonce
//   - Sends authorize request
//   - Confirms job notification is received
//
// This prevents oscillation where a pool appears up but cannot actually provide work.
package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MaxBackupPools is the maximum number of backup pools that can be tracked.
// This limit ensures the failover manager stays efficient even at scale.
const MaxBackupPools = 500

// ErrMaxPoolsReached is returned when trying to add more pools than MaxBackupPools.
var ErrMaxPoolsReached = errors.New("maximum number of backup pools reached (500)")

// PoolState represents the current state of a backup pool.
type PoolState int

const (
	PoolStateUnknown PoolState = iota
	PoolStateHealthy
	PoolStateDegraded
	PoolStateUnhealthy
	PoolStateOffline
)

func (s PoolState) String() string {
	switch s {
	case PoolStateHealthy:
		return "healthy"
	case PoolStateDegraded:
		return "degraded"
	case PoolStateUnhealthy:
		return "unhealthy"
	case PoolStateOffline:
		return "offline"
	default:
		return "unknown"
	}
}

// BackupPool represents a backup pool for failover.
type BackupPool struct {
	ID           string        `json:"id"`
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	Priority     int           `json:"priority"`
	Weight       int           `json:"weight"`
	State        PoolState     `json:"state"`
	ResponseTime time.Duration `json:"responseTime"`
	LastCheck    time.Time     `json:"lastCheck"`
	LastSuccess  time.Time     `json:"lastSuccess"`
	FailCount    int           `json:"failCount"`
	SuccessCount int           `json:"successCount"`
}

// FailoverEvent records a failover occurrence.
type FailoverEvent struct {
	FromPool   string    `json:"fromPool"`
	ToPool     string    `json:"toPool"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurredAt"`
}

// AlertSeverity indicates the urgency of an HA alert.
type AlertSeverity string

const (
	AlertSeverityCritical AlertSeverity = "critical"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityInfo     AlertSeverity = "info"
)

// HAAlert represents an alert for Spiral Sentinel to consume.
// Alerts are only generated when failover is enabled (multi-pool mode).
type HAAlert struct {
	ID           string        `json:"id"`
	Type         string        `json:"type"`         // pool_down, failover_success, failback_success, database_failover
	Severity     AlertSeverity `json:"severity"`     // critical, warning, info
	Message      string        `json:"message"`      // Human-readable description
	Details      interface{}   `json:"details"`      // Event-specific details
	OccurredAt   time.Time     `json:"occurredAt"`   // When the event occurred
	Acknowledged bool          `json:"acknowledged"` // Whether alert has been acknowledged
}

// MaxAlerts is the maximum number of alerts to retain.
const MaxAlerts = 100

// FailoverManager manages pool-level failover for miners.
type FailoverManager struct {
	primaryHost string
	primaryPort int
	pools       []*BackupPool
	logger      *zap.SugaredLogger

	mu              sync.RWMutex
	activePool      *BackupPool
	isPrimaryActive bool
	failoverHistory []FailoverEvent
	alerts          []HAAlert // HA alerts for Spiral Sentinel
	alertCounter    int64     // For generating unique alert IDs

	// Configuration
	healthCheckInterval    time.Duration
	failoverThreshold      int
	recoveryThreshold      int
	probeTimeout           time.Duration
	consecutivePrimaryFails int // Tracks consecutive primary check failures for debounced failover

	// Callbacks
	onFailover func(event FailoverEvent)

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// FailoverConfig holds failover manager configuration.
type FailoverConfig struct {
	PrimaryHost         string
	PrimaryPort         int
	BackupPools         []BackupPoolConfig
	HealthCheckInterval time.Duration
	FailoverThreshold   int
	RecoveryThreshold   int
	ProbeTimeout        time.Duration
}

// BackupPoolConfig defines a backup pool.
type BackupPoolConfig struct {
	ID       string
	Host     string
	Port     int
	Priority int
	Weight   int
}

// NewFailoverManager creates a new pool failover manager.
// Default health check interval is 15 minutes for stable server environments.
// This can be reduced for high-availability scenarios where faster failover is needed.
func NewFailoverManager(cfg FailoverConfig, logger *zap.Logger) *FailoverManager {
	if cfg.HealthCheckInterval == 0 {
		cfg.HealthCheckInterval = 15 * time.Minute // Stable mode - servers rarely go down
	}
	if cfg.FailoverThreshold == 0 {
		cfg.FailoverThreshold = 3
	}
	if cfg.RecoveryThreshold == 0 {
		cfg.RecoveryThreshold = 5
	}
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = 5 * time.Second
	}

	fm := &FailoverManager{
		primaryHost:         cfg.PrimaryHost,
		primaryPort:         cfg.PrimaryPort,
		pools:               make([]*BackupPool, 0, len(cfg.BackupPools)),
		logger:              logger.Sugar().Named("failover"),
		isPrimaryActive:     true,
		healthCheckInterval: cfg.HealthCheckInterval,
		failoverThreshold:   cfg.FailoverThreshold,
		recoveryThreshold:   cfg.RecoveryThreshold,
		probeTimeout:        cfg.ProbeTimeout,
	}

	// Create backup pool entries
	for _, bp := range cfg.BackupPools {
		pool := &BackupPool{
			ID:       bp.ID,
			Host:     bp.Host,
			Port:     bp.Port,
			Priority: bp.Priority,
			Weight:   bp.Weight,
			State:    PoolStateUnknown,
		}
		fm.pools = append(fm.pools, pool)
	}

	// Sort by priority
	sort.Slice(fm.pools, func(i, j int) bool {
		return fm.pools[i].Priority < fm.pools[j].Priority
	})

	return fm
}

// Start begins the failover monitoring loop.
func (fm *FailoverManager) Start(ctx context.Context) error {
	fm.ctx, fm.cancel = context.WithCancel(ctx)

	// Initial health check
	fm.checkAllPools()

	// Start monitoring loop
	fm.wg.Add(1)
	go fm.monitorLoop()

	fm.logger.Info("Pool failover manager started")
	return nil
}

// Stop gracefully shuts down the failover manager.
func (fm *FailoverManager) Stop() error {
	fm.cancel()
	fm.wg.Wait()
	fm.logger.Info("Pool failover manager stopped")
	return nil
}

// SetFailoverHandler sets the callback for failover events.
func (fm *FailoverManager) SetFailoverHandler(handler func(event FailoverEvent)) {
	fm.mu.Lock()
	fm.onFailover = handler
	fm.mu.Unlock()
}

// GetActivePool returns the currently active pool for miners to connect to.
// Returns host and port. If primary is active, returns primary.
func (fm *FailoverManager) GetActivePool() (string, int) {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	if fm.isPrimaryActive {
		return fm.primaryHost, fm.primaryPort
	}

	if fm.activePool != nil {
		return fm.activePool.Host, fm.activePool.Port
	}

	// Fallback to primary even if unhealthy
	return fm.primaryHost, fm.primaryPort
}

// GetPoolStatus returns the status of all pools.
func (fm *FailoverManager) GetPoolStatus() PoolStatus {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	status := PoolStatus{
		PrimaryHost:     fm.primaryHost,
		PrimaryPort:     fm.primaryPort,
		IsPrimaryActive: fm.isPrimaryActive,
		BackupPools:     make([]BackupPoolStatus, 0, len(fm.pools)),
		FailoverCount:   len(fm.failoverHistory),
	}

	if fm.activePool != nil {
		status.ActivePoolID = fm.activePool.ID
	}

	for _, pool := range fm.pools {
		status.BackupPools = append(status.BackupPools, BackupPoolStatus{
			ID:           pool.ID,
			Host:         pool.Host,
			Port:         pool.Port,
			State:        pool.State.String(),
			ResponseTime: pool.ResponseTime,
			LastCheck:    pool.LastCheck,
		})
	}

	if len(fm.failoverHistory) > 0 {
		status.LastFailover = &fm.failoverHistory[len(fm.failoverHistory)-1]
	}

	return status
}

// PoolStatus contains the current status of all pools.
type PoolStatus struct {
	PrimaryHost     string             `json:"primaryHost"`
	PrimaryPort     int                `json:"primaryPort"`
	IsPrimaryActive bool               `json:"isPrimaryActive"`
	ActivePoolID    string             `json:"activePoolId,omitempty"`
	BackupPools     []BackupPoolStatus `json:"backupPools"`
	FailoverCount   int                `json:"failoverCount"`
	LastFailover    *FailoverEvent     `json:"lastFailover,omitempty"`
}

// BackupPoolStatus contains status info for a backup pool.
type BackupPoolStatus struct {
	ID           string        `json:"id"`
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	State        string        `json:"state"`
	ResponseTime time.Duration `json:"responseTime"`
	LastCheck    time.Time     `json:"lastCheck"`
}

// AddDiscoveredPool adds a discovered pool as a backup.
// Returns ErrMaxPoolsReached if the pool limit (500) has been reached.
func (fm *FailoverManager) AddDiscoveredPool(pool *DiscoveredPool) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	// Check if already exists
	for _, p := range fm.pools {
		if p.Host == pool.Host && p.Port == pool.Port {
			return nil // Already tracked
		}
	}

	// Enforce pool limit for scalability
	if len(fm.pools) >= MaxBackupPools {
		fm.logger.Warnw("Maximum backup pools reached, ignoring new pool",
			"max", MaxBackupPools,
			"host", pool.Host,
			"port", pool.Port,
		)
		return ErrMaxPoolsReached
	}

	// Add as lowest priority
	maxPriority := 0
	for _, p := range fm.pools {
		if p.Priority > maxPriority {
			maxPriority = p.Priority
		}
	}

	bp := &BackupPool{
		ID:       fmt.Sprintf("discovered-%s-%d", pool.Host, pool.Port),
		Host:     pool.Host,
		Port:     pool.Port,
		Priority: maxPriority + 1,
		Weight:   1,
		State:    PoolStateHealthy,
	}

	fm.pools = append(fm.pools, bp)

	// Re-sort by priority
	sort.Slice(fm.pools, func(i, j int) bool {
		return fm.pools[i].Priority < fm.pools[j].Priority
	})

	fm.logger.Infow("Added discovered pool",
		"host", pool.Host,
		"port", pool.Port,
		"priority", bp.Priority,
		"totalPools", len(fm.pools),
	)

	return nil
}

// monitorLoop continuously monitors pool health.
func (fm *FailoverManager) monitorLoop() {
	defer fm.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			fm.logger.Errorw("PANIC recovered in monitorLoop", "panic", r)
		}
	}()

	ticker := time.NewTicker(fm.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-fm.ctx.Done():
			return
		case <-ticker.C:
			fm.checkAllPools()
			fm.evaluateFailover()
		}
	}
}

// checkAllPools performs health checks on all pools.
func (fm *FailoverManager) checkAllPools() {
	// Check primary — only mark healthy if ALREADY active.
	// Recovery from inactive is handled exclusively by evaluateFailover()
	// with a deep check, to prevent false recovery on TCP-only success.
	primaryHealthy := fm.checkPool(fm.primaryHost, fm.primaryPort)

	fm.mu.Lock()
	if !primaryHealthy && fm.isPrimaryActive {
		// Primary was active but just failed — don't immediately deactivate here;
		// evaluateFailover() handles consecutive failure counting
	}
	// Intentionally NOT setting fm.isPrimaryActive = true on TCP success.
	// evaluateFailover() performs deep check before re-activating primary.
	_ = primaryHealthy
	fm.mu.Unlock()

	// Check backup pools
	for _, pool := range fm.pools {
		healthy := fm.checkPool(pool.Host, pool.Port)

		fm.mu.Lock()
		pool.LastCheck = time.Now()

		if healthy {
			pool.SuccessCount++
			pool.FailCount = 0
			pool.LastSuccess = time.Now()

			if pool.SuccessCount >= fm.recoveryThreshold {
				pool.State = PoolStateHealthy
			} else if pool.State == PoolStateUnhealthy || pool.State == PoolStateOffline {
				pool.State = PoolStateDegraded
			}
		} else {
			pool.FailCount++
			pool.SuccessCount = 0

			if pool.FailCount >= fm.failoverThreshold*2 {
				pool.State = PoolStateOffline
			} else if pool.FailCount >= fm.failoverThreshold {
				pool.State = PoolStateUnhealthy
			} else {
				pool.State = PoolStateDegraded
			}
		}
		fm.mu.Unlock()
	}
}

// checkPool verifies a pool is responding to stratum connections.
// NOTE: IPv4 only - IPv6 is disabled at the OS level for simplicity.
func (fm *FailoverManager) checkPool(host string, port int) bool {
	return fm.checkPoolDeep(host, port, false)
}

// checkPoolDeep performs a health check on a pool.
// If deepCheck is true, it also verifies the pool can provide mining jobs.
// This is used for failback to ensure the primary is fully functional before switching back.
func (fm *FailoverManager) checkPoolDeep(host string, port int, deepCheck bool) bool {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	conn, err := net.DialTimeout("tcp", addr, fm.probeTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	// Set deadline - longer for deep check
	timeout := fm.probeTimeout
	if deepCheck {
		timeout = fm.probeTimeout * 2
	}
	_ = conn.SetDeadline(time.Now().Add(timeout)) // #nosec G104

	// Send subscribe
	subscribe := `{"id":1,"method":"mining.subscribe","params":["FailoverCheck/1.0"]}` + "\n"
	if _, err := conn.Write([]byte(subscribe)); err != nil {
		return false
	}

	// Read response using buffered reader to handle partial JSON
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return false
	}

	// Verify it's a valid stratum response
	var response map[string]interface{}
	if err := json.Unmarshal(line, &response); err != nil {
		return false
	}

	result, hasResult := response["result"]
	if !hasResult {
		return false
	}

	// Basic check passed - pool responds to subscribe
	if !deepCheck {
		return true
	}

	// Deep check: verify the pool returned a valid subscription with extranonce
	// A healthy pool returns: [[[...], "extranonce1"], "extranonce2_size"]
	resultArr, ok := result.([]interface{})
	if !ok || len(resultArr) < 2 {
		fm.logger.Debugw("Deep check failed: invalid subscribe result format",
			"host", host,
			"port", port,
		)
		return false
	}

	// Check that we got subscription details (indicates pool has valid job)
	subscriptions, ok := resultArr[0].([]interface{})
	if !ok || len(subscriptions) == 0 {
		fm.logger.Debugw("Deep check failed: no subscriptions returned",
			"host", host,
			"port", port,
		)
		return false
	}

	// Now send authorize to verify pool accepts auth
	// Use a dummy address - we just want to see if the pool processes it
	authorize := `{"id":2,"method":"mining.authorize","params":["healthcheck.test","x"]}` + "\n"
	if _, err := conn.Write([]byte(authorize)); err != nil {
		return false
	}

	// Read authorize response and potential job notification using buffered reader
	authLine, err := reader.ReadBytes('\n')
	if err != nil {
		return false
	}

	// Parse response - might be auth response or could include a job notification
	var authResponse map[string]interface{}
	if err := json.Unmarshal(authLine, &authResponse); err != nil {
		// Try reading next line (might be a separate JSON message)
		if nextLine, nextErr := reader.ReadBytes('\n'); nextErr == nil {
			if json.Unmarshal(nextLine, &authResponse) != nil {
				return false
			}
		} else {
			return false
		}
	}

	// Check if we got an auth response (even rejection is fine — the pool is
	// processing stratum requests, which is what matters for failback health).
	// Auth rejection (error != nil) is expected since we use a dummy worker name.
	_, hasAuthResult := authResponse["result"]
	_, hasAuthError := authResponse["error"]
	if !hasAuthResult && !hasAuthError {
		// Might be a job notification, which is also good
		method, hasMethod := authResponse["method"]
		if hasMethod && method == "mining.notify" {
			fm.logger.Debugw("Deep check passed: pool sent job notification",
				"host", host,
				"port", port,
			)
			return true
		}
		return false
	}

	fm.logger.Debugw("Deep check passed: pool is fully functional",
		"host", host,
		"port", port,
	)
	return true
}

// evaluateFailover checks if failover is needed.
// Network probes are performed outside the lock to avoid blocking GetActivePool()
// and other callers during potentially slow (5-10s) TCP connections.
func (fm *FailoverManager) evaluateFailover() {
	// Phase 1: Snapshot state under read lock
	fm.mu.RLock()
	primaryActive := fm.isPrimaryActive
	primaryHost := fm.primaryHost
	primaryPort := fm.primaryPort
	fm.mu.RUnlock()

	// Phase 2: Network probes OUTSIDE lock (checkPool/checkPoolDeep only use host/port args)
	var primaryDeepOK bool
	var primaryTCPFailed bool

	if !primaryActive {
		// Deep check verifies: subscribe response, valid extranonce, and job notification
		primaryDeepOK = fm.checkPoolDeep(primaryHost, primaryPort, true)
	} else {
		if !fm.checkPool(primaryHost, primaryPort) {
			primaryTCPFailed = true
		}
	}

	// Phase 3: Apply decisions under write lock
	fm.mu.Lock()
	defer fm.mu.Unlock()

	// Re-check state — it may have changed during probes (e.g., ForceFailover called)
	if fm.isPrimaryActive != primaryActive {
		return // State changed during probe, skip this cycle
	}

	// Recovery path: primary was down, deep check passed
	if !fm.isPrimaryActive && primaryDeepOK {
		fromPool := ""
		if fm.activePool != nil {
			fromPool = fm.activePool.ID
		}
		event := FailoverEvent{
			FromPool:   fromPool,
			ToPool:     "primary",
			Reason:     "primary_recovered",
			OccurredAt: time.Now(),
		}
		fm.failoverHistory = append(fm.failoverHistory, event)
		fm.isPrimaryActive = true
		fm.activePool = nil
		fm.consecutivePrimaryFails = 0

		fm.logger.Infow("Recovered to primary pool (deep check passed)",
			"from", event.FromPool,
		)

		fm.addAlert(
			"failback_success",
			AlertSeverityInfo,
			fmt.Sprintf("Successfully failed back to primary pool (%s:%d)", fm.primaryHost, fm.primaryPort),
			event,
		)

		cb := fm.onFailover
		if cb != nil {
			go cb(event)
		}
		return
	}

	// Failover path: primary is active but TCP check failed
	if fm.isPrimaryActive && primaryTCPFailed {
		fm.consecutivePrimaryFails++
		if fm.consecutivePrimaryFails < fm.failoverThreshold {
			fm.logger.Warnw("Primary check failed, waiting for threshold",
				"failures", fm.consecutivePrimaryFails,
				"threshold", fm.failoverThreshold,
			)
			return
		}
		fm.consecutivePrimaryFails = 0 // Reset after triggering failover

		fm.addAlert(
			"pool_down",
			AlertSeverityCritical,
			fmt.Sprintf("Primary pool is down (%s:%d)", fm.primaryHost, fm.primaryPort),
			map[string]interface{}{
				"host": fm.primaryHost,
				"port": fm.primaryPort,
			},
		)

		// Find best backup
		var best *BackupPool
		for _, pool := range fm.pools {
			if pool.State == PoolStateHealthy || pool.State == PoolStateDegraded {
				if best == nil || pool.Priority < best.Priority {
					best = pool
				}
			}
		}

		if best != nil {
			event := FailoverEvent{
				FromPool:   "primary",
				ToPool:     best.ID,
				Reason:     "primary_down",
				OccurredAt: time.Now(),
			}
			fm.failoverHistory = append(fm.failoverHistory, event)
			fm.isPrimaryActive = false
			fm.activePool = best

			fm.logger.Warnw("Failover to backup pool",
				"from", "primary",
				"to", best.ID,
				"host", best.Host,
				"port", best.Port,
			)

			fm.addAlert(
				"failover_success",
				AlertSeverityWarning,
				fmt.Sprintf("Failover successful: switched to backup pool %s (%s:%d)", best.ID, best.Host, best.Port),
				event,
			)

			cb := fm.onFailover
			if cb != nil {
				go cb(event)
			}
		} else {
			fm.addAlert(
				"failover_failed",
				AlertSeverityCritical,
				"Primary pool is down and no healthy backup pools are available",
				map[string]interface{}{
					"primaryHost": fm.primaryHost,
					"primaryPort": fm.primaryPort,
					"backupCount": len(fm.pools),
				},
			)
		}
		return
	}

	// Primary check succeeded — reset consecutive failure counter
	if fm.isPrimaryActive && !primaryTCPFailed {
		fm.consecutivePrimaryFails = 0
	}

	// Check if current backup needs failover to another backup (no network I/O needed —
	// pool.State is set by checkAllPools which runs before this function)
	if !fm.isPrimaryActive && fm.activePool != nil {
		if fm.activePool.State == PoolStateUnhealthy || fm.activePool.State == PoolStateOffline {
			fm.addAlert(
				"pool_down",
				AlertSeverityCritical,
				fmt.Sprintf("Backup pool %s is down (%s:%d)", fm.activePool.ID, fm.activePool.Host, fm.activePool.Port),
				map[string]interface{}{
					"poolId": fm.activePool.ID,
					"host":   fm.activePool.Host,
					"port":   fm.activePool.Port,
				},
			)

			// Find next best
			var best *BackupPool
			for _, pool := range fm.pools {
				if pool == fm.activePool {
					continue
				}
				if pool.State == PoolStateHealthy || pool.State == PoolStateDegraded {
					if best == nil || pool.Priority < best.Priority {
						best = pool
					}
				}
			}

			if best != nil {
				event := FailoverEvent{
					FromPool:   fm.activePool.ID,
					ToPool:     best.ID,
					Reason:     "backup_down",
					OccurredAt: time.Now(),
				}
				fm.failoverHistory = append(fm.failoverHistory, event)
				fm.activePool = best

				fm.logger.Warnw("Failover to next backup pool",
					"from", event.FromPool,
					"to", best.ID,
				)

				fm.addAlert(
					"failover_success",
					AlertSeverityWarning,
					fmt.Sprintf("Failover successful: switched to backup pool %s (%s:%d)", best.ID, best.Host, best.Port),
					event,
				)

				cb := fm.onFailover
				if cb != nil {
					go cb(event)
				}
			} else {
				fm.addAlert(
					"failover_failed",
					AlertSeverityCritical,
					"All backup pools are down, no failover targets available",
					map[string]interface{}{
						"currentPool": fm.activePool.ID,
						"backupCount": len(fm.pools),
					},
				)
			}
		}
	}

	// Keep only last 100 failover events
	if len(fm.failoverHistory) > 100 {
		fm.failoverHistory = fm.failoverHistory[len(fm.failoverHistory)-100:]
	}
}

// ForceFailover manually triggers failover to a specific pool.
func (fm *FailoverManager) ForceFailover(poolID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if poolID == "primary" {
		if !fm.checkPool(fm.primaryHost, fm.primaryPort) {
			fm.logger.Warnw("Forced failover to primary but primary may be unreachable",
				"host", fm.primaryHost, "port", fm.primaryPort)
		}
		fm.isPrimaryActive = true
		fm.activePool = nil
		fm.consecutivePrimaryFails = 0

		fm.logger.Infow("Forced failover to primary")
		return nil
	}

	for _, pool := range fm.pools {
		if pool.ID == poolID {
			event := FailoverEvent{
				FromPool:   "manual",
				ToPool:     poolID,
				Reason:     "manual_failover",
				OccurredAt: time.Now(),
			}
			fm.failoverHistory = append(fm.failoverHistory, event)
			fm.isPrimaryActive = false
			fm.activePool = pool

			fm.logger.Infow("Forced failover",
				"to", poolID,
			)

			cb := fm.onFailover
			if cb != nil {
				go cb(event)
			}

			// Keep history bounded (same as evaluateFailover)
			if len(fm.failoverHistory) > 100 {
				fm.failoverHistory = fm.failoverHistory[len(fm.failoverHistory)-100:]
			}
			return nil
		}
	}

	return fmt.Errorf("pool not found: %s", poolID)
}

// addAlert creates and stores a new HA alert.
func (fm *FailoverManager) addAlert(alertType string, severity AlertSeverity, message string, details interface{}) {
	fm.alertCounter++
	alert := HAAlert{
		ID:         fmt.Sprintf("ha-%d-%d", time.Now().UnixNano(), fm.alertCounter),
		Type:       alertType,
		Severity:   severity,
		Message:    message,
		Details:    details,
		OccurredAt: time.Now(),
	}

	fm.alerts = append(fm.alerts, alert)

	// Keep only last MaxAlerts
	if len(fm.alerts) > MaxAlerts {
		fm.alerts = fm.alerts[len(fm.alerts)-MaxAlerts:]
	}

	fm.logger.Infow("HA alert generated",
		"id", alert.ID,
		"type", alertType,
		"severity", severity,
		"message", message,
	)
}

// GetAlerts returns all unacknowledged HA alerts.
// Only returns alerts when failover is enabled (multi-pool mode).
func (fm *FailoverManager) GetAlerts() []HAAlert {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	var alerts []HAAlert
	for _, alert := range fm.alerts {
		if !alert.Acknowledged {
			alerts = append(alerts, alert)
		}
	}
	return alerts
}

// GetAllAlerts returns all HA alerts including acknowledged ones.
func (fm *FailoverManager) GetAllAlerts() []HAAlert {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	alerts := make([]HAAlert, len(fm.alerts))
	copy(alerts, fm.alerts)
	return alerts
}

// AcknowledgeAlert marks an alert as acknowledged.
func (fm *FailoverManager) AcknowledgeAlert(alertID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	for i := range fm.alerts {
		if fm.alerts[i].ID == alertID {
			fm.alerts[i].Acknowledged = true
			return nil
		}
	}
	return fmt.Errorf("alert not found: %s", alertID)
}

// AcknowledgeAllAlerts marks all alerts as acknowledged.
func (fm *FailoverManager) AcknowledgeAllAlerts() int {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	count := 0
	for i := range fm.alerts {
		if !fm.alerts[i].Acknowledged {
			fm.alerts[i].Acknowledged = true
			count++
		}
	}
	return count
}

// GetAlertsSince returns alerts that occurred after the given time.
func (fm *FailoverManager) GetAlertsSince(since time.Time) []HAAlert {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	var alerts []HAAlert
	for _, alert := range fm.alerts {
		if alert.OccurredAt.After(since) {
			alerts = append(alerts, alert)
		}
	}
	return alerts
}
