// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - Coordinator manages multiple CoinPools for V2 multi-coin support.
//
// The Coordinator is the top-level orchestrator that:
// - Creates and manages CoinPool instances for each enabled coin
// - Handles database initialization and V2 migrations
// - Provides unified stats and health monitoring
// - Coordinates graceful startup and shutdown
package pool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/spiralpool/stratum/internal/api"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/scheduler"
	"github.com/spiralpool/stratum/internal/ha"
	"github.com/spiralpool/stratum/internal/metrics"
	"github.com/spiralpool/stratum/internal/payments"
	"github.com/spiralpool/stratum/internal/shares"
	"go.uber.org/zap"
)

// StartupConfig controls how the coordinator handles partial node readiness during startup.
// This is designed to work seamlessly with HA mode - when the server restarts (standalone
// or in a cluster), the coordinator will wait for nodes to become available rather than
// failing immediately, which prevents boot loops.
//
// In HA mode:
//   - BACKUP nodes can start with partial coins and wait for all nodes
//   - MASTER election happens at the network level (VIP), not here
//   - Database failover is triggered by VIP ownership changes
//
// In standalone mode:
//   - Same behavior: partial startup is allowed
//   - No external coordination needed
type StartupConfig struct {
	// GracePeriod is the time to wait for nodes to become available before giving up
	// This should be longer than typical daemon startup time (30-60 seconds for most chains)
	GracePeriod time.Duration
	// RetryInterval is how often to retry failed coin pools during the grace period
	RetryInterval time.Duration
	// RequireAllCoins determines if startup fails when any coin fails (strict mode)
	// If false, allows partial operation with available coins
	// Set to true for HA mode where all coins should be available
	RequireAllCoins bool
}

// DefaultStartupConfig returns sensible defaults for startup behavior.
// These defaults are designed to prevent boot loops when blockchain daemons
// are still initializing after a server restart.
func DefaultStartupConfig() StartupConfig {
	return StartupConfig{
		GracePeriod:     30 * time.Minute, // Wait up to 30 minutes for nodes (aligned with Sentinel alert suppression)
		RetryInterval:   30 * time.Second, // Retry every 30 seconds
		RequireAllCoins: false,            // Allow partial operation (graceful degradation)
	}
}

// Coordinator manages multiple coin pools.
type Coordinator struct {
	cfg    *config.ConfigV2
	logger *zap.SugaredLogger

	// Startup configuration
	startupCfg StartupConfig

	// Database (shared across all coin pools)
	db *database.PostgresDB

	// API server (V2 multi-coin aware)
	apiServer *api.ServerV2

	// Prometheus metrics server (shared across all coin pools)
	metricsServer *metrics.Metrics

	// HA components (shared across all CoinPools)
	vipManager         *ha.VIPManager
	dbManager          *database.DatabaseManager
	replicationManager *database.ReplicationManager
	slotMonitor        *ha.ReplicationSlotMonitor
	redisDedupTracker  *shares.RedisDedupTracker // AUDIT FIX (ISSUE-3): Shared Redis dedup for HA block dedup

	// API Sentinel instance (internal pool health monitor)
	sentinel *Sentinel

	// Per-coin payment processors (one per coin with payments enabled)
	// Each processor uses its coin's own daemon client and pool-scoped DB,
	// so a single coin's daemon failure doesn't block all maturity tracking.
	paymentProcessors  map[string]*payments.Processor // keyed by pool ID
	paymentProcessorMu sync.RWMutex                   // AUDIT FIX (H-2): protects paymentProcessors map

	// Multi coin smart port (v2.1)
	diffMonitor *scheduler.Monitor
	coinSelector *scheduler.Selector
	multiServer *scheduler.MultiServer

	// Coin pools indexed by pool ID
	pools   map[string]*CoinPool
	poolsMu sync.RWMutex

	// Failed coin configs for retry (coins that couldn't start initially)
	failedCoins   []*config.CoinPoolConfig
	failedCoinsMu sync.Mutex

	// Lifecycle
	wg       sync.WaitGroup
	cancel   context.CancelFunc
	running  bool
	runMu    sync.Mutex
	startErr error
}

// NewCoordinator creates a new multi-coin pool coordinator.
func NewCoordinator(cfg *config.ConfigV2, logger *zap.Logger) (*Coordinator, error) {
	log := logger.Sugar()

	if len(cfg.Coins) == 0 {
		return nil, fmt.Errorf("no coins configured")
	}

	// Count enabled coins
	enabledCount := 0
	for _, coin := range cfg.Coins {
		if coin.Enabled {
			enabledCount++
		}
	}
	if enabledCount == 0 {
		return nil, fmt.Errorf("no enabled coins configured")
	}

	log.Infow("Creating multi-coin coordinator",
		"totalCoins", len(cfg.Coins),
		"enabledCoins", enabledCount,
	)

	// Initialize database
	log.Info("Initializing database...")

	log.Debugw("Database config from YAML",
		"host", cfg.Database.Host,
		"port", cfg.Database.Port,
		"sslMode", cfg.Database.SSLMode,
	)

	dbCfg := &config.DatabaseConfig{
		Host:           cfg.Database.Host,
		Port:           cfg.Database.Port,
		User:           cfg.Database.User,
		Password:       cfg.Database.Password,
		Database:       cfg.Database.Database,
		MaxConnections: cfg.Database.MaxConnections,
		Batching:       cfg.Database.Batching,
		SSLMode:        cfg.Database.SSLMode,     // BUG FIX: Was missing - caused TLS errors
		SSLRootCert:    cfg.Database.SSLRootCert, // Also copy for verify-ca/verify-full modes
	}

	log.Debugw("Database connection configured",
		"connStr", dbCfg.SafeConnectionString(),
	)

	// Use first pool ID and algorithm for initial migration and shared DB (V1 compatibility)
	firstPoolID := cfg.Coins[0].PoolID
	firstAlgorithm := coin.AlgorithmFromCoinSymbol(cfg.Coins[0].Symbol)
	migrator := database.NewMigrator(dbCfg, firstPoolID, logger)

	initCtx, initCancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := migrator.Initialize(initCtx); err != nil {
		initCancel()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	initCancel()
	log.Info("Database schema initialized")

	// Connect to database pool
	db, err := database.NewPostgresDB(dbCfg, firstPoolID, firstAlgorithm, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	log.Info("Connected to database")

	// Run V2 migrations
	log.Info("Running V2 migrations...")
	migratorV2 := database.NewMigratorV2(db.Pool(), logger)
	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := migratorV2.RunV2Migrations(migrateCtx); err != nil {
		migrateCancel()
		if closeErr := db.Close(); closeErr != nil {
			log.Errorw("Error closing database after migration failure", "error", closeErr)
		}
		return nil, fmt.Errorf("failed to run V2 migrations: %w", err)
	}
	migrateCancel()
	log.Info("V2 migrations complete")

	// Create per-coin pool tables
	for _, coinCfg := range cfg.Coins {
		if !coinCfg.Enabled {
			continue
		}

		log.Infow("Creating pool tables", "poolId", coinCfg.PoolID, "coin", coinCfg.Symbol)
		tablesCtx, tablesCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := migratorV2.CreatePoolTablesV2(tablesCtx, coinCfg.PoolID, coinCfg.Symbol); err != nil {
			tablesCancel()
			if closeErr := db.Close(); closeErr != nil {
				log.Errorw("Error closing database after table creation failure", "error", closeErr)
			}
			return nil, fmt.Errorf("failed to create pool tables for %s: %w", coinCfg.PoolID, err)
		}
		tablesCancel()

		// AUDIT FIX: Create tables for aux chain pool IDs (merge mining).
		// Without this, InsertBlockForPool for aux blocks fails because the
		// aux-specific tables (blocks_{poolid}_{symbol}, etc.) don't exist.
		if coinCfg.IsMergeMiningEnabled() {
			for _, auxCfg := range coinCfg.GetEnabledAuxChains() {
				auxPoolID := coinCfg.PoolID + "_" + strings.ToLower(auxCfg.Symbol)
				log.Infow("Creating aux pool tables for merge mining",
					"auxPoolId", auxPoolID,
					"parentPoolId", coinCfg.PoolID,
					"auxSymbol", auxCfg.Symbol,
				)
				auxTablesCtx, auxTablesCancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := migratorV2.CreatePoolTablesV2(auxTablesCtx, auxPoolID, auxCfg.Symbol); err != nil {
					auxTablesCancel()
					// Non-fatal: log warning but continue startup.
					// Aux block DB inserts will fail, but parent mining is unaffected.
					log.Warnw("Failed to create aux pool tables (aux block recording will fail)",
						"auxPoolId", auxPoolID,
						"auxSymbol", auxCfg.Symbol,
						"error", err,
					)
					continue
				}
				auxTablesCancel()
			}
		}
	}

	coord := &Coordinator{
		cfg:        cfg,
		logger:     log,
		startupCfg: DefaultStartupConfig(),
		db:         db,
		pools:      make(map[string]*CoinPool),
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// HA COMPONENT INITIALIZATION (V2 Multi-Coin)
	// ═══════════════════════════════════════════════════════════════════════════
	// When HA is enabled, create DatabaseManager, VIPManager, and ReplicationManager.
	// These are shared across all CoinPools and provide:
	//   - Database failover with circuit breaker and block queue
	//   - VIP-based miner failover with election protocol
	//   - PostgreSQL streaming replication management
	// ═══════════════════════════════════════════════════════════════════════════
	if cfg.IsHAEnabled() {
		nodes := cfg.GetDatabaseNodes()
		dbNodeConfigs := make([]database.DBNodeConfig, len(nodes))
		for i, n := range nodes {
			dbNodeConfigs[i] = database.DBNodeConfig(n)
		}
		dbMgr, err := database.NewDatabaseManager(dbNodeConfigs, firstPoolID, firstAlgorithm, logger)
		if err != nil {
			log.Warnw("Failed to create HA database manager (continuing without HA DB failover)",
				"error", err,
			)
		} else {
			coord.dbManager = dbMgr
			log.Infow("✅ V2 HA database manager initialized", "nodes", len(nodes))
		}
	}

	if cfg.IsVIPEnabled() {
		vipCfg := ha.Config{
			Enabled:           true,
			Priority:          cfg.VIP.Priority,
			AutoPriority:      cfg.VIP.AutoPriority,
			VIPAddress:        cfg.VIP.Address,
			VIPInterface:      cfg.VIP.Interface,
			VIPNetmask:        cfg.VIP.Netmask,
			DiscoveryPort:     cfg.VIP.DiscoveryPort,
			StatusPort:        cfg.VIP.StatusPort,
			StratumPort:       cfg.GetFirstStratumPort(),
			HeartbeatInterval: cfg.VIP.HeartbeatInterval,
			FailoverTimeout:   cfg.VIP.FailoverTimeout,
			ClusterToken:      cfg.VIP.ClusterToken,
			CanBecomeMaster:   cfg.VIP.CanBecomeMaster,
		}

		vipMgr, err := ha.NewVIPManager(vipCfg, logger)
		if err != nil {
			log.Warnw("Failed to create VIP manager (continuing without VIP failover)",
				"error", err,
			)
		} else {
			coord.vipManager = vipMgr

			// Configure multi-coin port routing
			coinPorts := make(map[string]*ha.CoinPortInfo)
			for _, coinCfg := range cfg.Coins {
				if coinCfg.Enabled {
					coinPorts[strings.ToUpper(coinCfg.Symbol)] = &ha.CoinPortInfo{
						StratumV1: coinCfg.Stratum.Port,
					}
				}
			}
			if len(coinPorts) > 0 {
				vipMgr.SetCoinPorts(coinPorts)
			}

			// Configure pool payout addresses for HA validation across nodes
			// CRITICAL: All HA nodes MUST have the same payout addresses or funds will split!
			poolAddrs := make(map[string]string)
			for _, coinCfg := range cfg.Coins {
				if !coinCfg.Enabled {
					continue
				}
				if coinCfg.Address != "" {
					poolAddrs[strings.ToUpper(coinCfg.Symbol)] = coinCfg.Address
				}
				// Include merge-mined aux chain addresses for HA sync
				for _, aux := range coinCfg.MergeMining.AuxChains {
					if aux.Enabled && aux.Address != "" {
						poolAddrs[strings.ToUpper(aux.Symbol)] = aux.Address
					}
				}
			}
			if len(poolAddrs) > 0 {
				vipMgr.SetPoolAddresses(poolAddrs)
				log.Infow("Pool addresses configured for HA validation", "coins", len(poolAddrs))
			}

			log.Infow("✅ V2 VIP manager initialized",
				"vip", cfg.VIP.Address,
				"interface", cfg.VIP.Interface,
				"coinPorts", len(coinPorts),
			)
		}
	}

	// Wire replication manager if both HA DB and VIP are available
	if cfg.IsHAEnabled() && coord.dbManager != nil {
		replCfg := database.DefaultReplicationConfig()
		replCfg.Enabled = true
		replCfg.PostgresPort = cfg.Database.Port

		replMgr, err := database.NewReplicationManager(replCfg, coord.dbManager, logger)
		if err != nil {
			log.Warnw("Failed to create replication manager (continuing without replication)",
				"error", err,
			)
		} else {
			coord.replicationManager = replMgr
			log.Info("✅ V2 PostgreSQL replication manager initialized")

			// Wire VIP to replication manager + database manager for automatic DB failover.
			// Chain both: replication handler (PostgreSQL promote/demote) and
			// database manager handler (connection pool node switching).
			if coord.vipManager != nil {
				replHandler := replMgr.VIPRoleChangeHandler()
				dbHandler := coord.dbManager.VIPFailoverCallback()
				coord.vipManager.SetDatabaseFailoverHandler(func(isMaster bool) {
					replHandler(isMaster)
					dbHandler(isMaster)
				})
				log.Info("✅ V2 VIP and database replication wired together")
			}
		}
	}

	// Initialize Prometheus metrics server
	metricsCfg := &config.MetricsConfig{
		Enabled:   true,
		Listen:    fmt.Sprintf("0.0.0.0:%d", cfg.Global.MetricsPort),
		AuthToken: cfg.Global.MetricsAuthToken,
	}
	coord.metricsServer = metrics.New(metricsCfg, logger)
	log.Infow("Prometheus metrics server initialized", "port", cfg.Global.MetricsPort)

	// Initialize V2 API server if enabled
	if cfg.Global.APIEnabled {
		log.Infow("Initializing V2 API server", "port", cfg.Global.APIPort)
		coord.apiServer = api.NewServerV2(cfg, db, logger)
	}

	// Create coin pools with graceful handling of partial failures
	// This allows the pool to start even if some blockchain nodes aren't ready yet
	var failedCoins []*config.CoinPoolConfig

	for _, coinCfg := range cfg.Coins {
		if !coinCfg.Enabled {
			log.Infow("Skipping disabled coin", "coin", coinCfg.Symbol, "poolId", coinCfg.PoolID)
			continue
		}

		log.Infow("Creating coin pool", "coin", coinCfg.Symbol, "poolId", coinCfg.PoolID)

		coinCfgCopy := coinCfg // Copy for closure safety
		pool, err := NewCoinPool(&CoinPoolConfig{
			CoinConfig:        &coinCfgCopy,
			CelebrationConfig: &cfg.Global.Celebration,
			DBPool:            db,
			Logger:            logger,
			MetricsServer:     coord.metricsServer,
		})
		if err != nil {
			log.Warnw("Failed to create coin pool (will retry during startup)",
				"coin", coinCfg.Symbol,
				"poolId", coinCfg.PoolID,
				"error", err,
			)
			// Store for retry instead of failing immediately
			failedCopy := coinCfg
			failedCoins = append(failedCoins, &failedCopy)
			continue
		}

		coord.pools[coinCfg.PoolID] = pool
		log.Infow("Coin pool created", "coin", coinCfg.Symbol, "poolId", coinCfg.PoolID)
	}

	coord.failedCoins = failedCoins

	// ═══════════════════════════════════════════════════════════════════════════
	// AUDIT FIX (ISSUE-3): Wire Redis dedup tracker for HA block submission dedup.
	// V1 creates a RedisDedupTracker and calls pool.SetRedisDedupTracker(). V2 was
	// missing this entirely — the CoinPool.redisDedupTracker field was declared and
	// used in handleBlock() but never set, leaving it always nil.
	// ═══════════════════════════════════════════════════════════════════════════
	if coord.vipManager != nil {
		redisDedupCfg := shares.DefaultRedisDedupConfig()
		redisDedupTracker := shares.NewRedisDedupTracker(redisDedupCfg, log)
		if redisDedupTracker != nil {
			coord.redisDedupTracker = redisDedupTracker
			for poolID, pool := range coord.pools {
				pool.SetRedisDedupTracker(redisDedupTracker)
				log.Infow("Redis dedup tracker wired to coin pool", "poolId", poolID)
			}
		} else {
			log.Warn("Redis dedup tracker not available — HA block dedup disabled")
		}

		// BUG FIX: Wire sync status callback for HA master election.
		// CoinPool's sync gate reports progress to VIPManager for master election.
		for _, pool := range coord.pools {
			pool.SetSyncStatusCallback(coord.vipManager.UpdateCoinSyncStatus)
		}
	}

	// Wire Prometheus metrics to HA components and share pipeline
	if coord.metricsServer != nil {
		if coord.vipManager != nil {
			coord.vipManager.SetHAMetrics(coord.metricsServer)
		}
		if coord.dbManager != nil {
			coord.dbManager.SetDBMetrics(coord.metricsServer)
		}
		if coord.redisDedupTracker != nil {
			coord.redisDedupTracker.SetMetrics(coord.metricsServer)
		}
		for _, pool := range coord.pools {
			if p := pool.GetSharePipeline(); p != nil {
				p.SetMetrics(coord.metricsServer)
			}
		}
	}

	// Check if we have at least one coin ready
	if len(coord.pools) == 0 {
		if len(failedCoins) > 0 {
			// No pools ready yet, but we have coins to retry
			log.Infow("No coin pools ready yet, will retry during startup grace period",
				"failedCoins", len(failedCoins),
				"gracePeriod", coord.startupCfg.GracePeriod,
			)
		} else {
			if closeErr := db.Close(); closeErr != nil {
				log.Errorw("Error closing database after pool creation failure", "error", closeErr)
			}
			return nil, fmt.Errorf("no coin pools could be created and no coins configured for retry")
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PAYMENT PROCESSOR INITIALIZATION (V2 Multi-Coin — Per-Coin Isolation)
	// ═══════════════════════════════════════════════════════════════════════════
	// AUDIT FIX: Create one payment processor per coin with payments enabled.
	// Previously a single shared processor used the first coin's daemon client,
	// meaning if that daemon went down, maturity tracking for ALL coins stopped.
	// Now each processor uses its own coin's daemon client and pool-scoped DB.
	// Payment processing is fenced by HA (master-only) + advisory locks.
	// ═══════════════════════════════════════════════════════════════════════════
	coord.paymentProcessors = make(map[string]*payments.Processor)

	coord.poolsMu.RLock()
	for poolID, coinPool := range coord.pools {
		// Find the config for this pool
		var coinCfg *config.CoinPoolConfig
		for i := range cfg.Coins {
			if cfg.Coins[i].PoolID == poolID && cfg.Coins[i].Enabled {
				coinCfg = &cfg.Coins[i]
				break
			}
		}
		if coinCfg == nil || !coinCfg.Payments.Enabled {
			continue
		}

		// Get this coin's daemon client
		var daemonRPC payments.DaemonRPC
		if coinPool.nodeManager != nil {
			primary := coinPool.nodeManager.GetPrimary()
			if primary != nil && primary.Client != nil {
				daemonRPC = primary.Client
			}
		}
		if daemonRPC == nil {
			log.Warnw("Payment processor skipped for coin — no daemon client available",
				"coin", coinCfg.Symbol,
				"poolId", poolID,
			)
			continue
		}

		// Use config's blockMaturity if explicitly set (> 0), otherwise use coin's default.
		// This allows regtest configs to override maturity for fast testing (e.g. blockMaturity: 10).
		effectiveMaturity := coinPool.coin.CoinbaseMaturity()
		if coinCfg.Payments.BlockMaturity > 0 {
			effectiveMaturity = coinCfg.Payments.BlockMaturity
		}
		paymentsCfg := &config.PaymentsConfig{
			Enabled:        coinCfg.Payments.Enabled,
			Interval:       coinCfg.Payments.Interval,
			MinimumPayment: coinCfg.Payments.MinimumPayment,
			Scheme:         coinCfg.Payments.Scheme,
			BlockMaturity:  effectiveMaturity,
			BlockTime:      coinPool.coin.BlockTime(),
		}
		poolCfgForProc := &config.PoolConfig{
			ID:      coinCfg.PoolID,
			Coin:    coinCfg.Symbol,
			Address: coinCfg.Address,
		}

		// Create pool-scoped block store so this processor queries only this coin's blocks table
		scopedDB := db.WithPoolID(poolID)

		proc := payments.NewProcessor(paymentsCfg, poolCfgForProc, scopedDB, daemonRPC, logger)

		// Wire Prometheus metrics
		if coord.metricsServer != nil {
			proc.SetMetrics(coord.metricsServer)
		}

		// Wire HA fencing
		if coord.vipManager != nil {
			proc.SetHAEnabled(true)
			// BUG FIX (M9): Default isMaster to false when HA is configured.
			// NewProcessor sets isMaster=true (for standalone mode). Without this,
			// the first processCycle runs unguarded before VIP election completes,
			// allowing both nodes to attempt payments simultaneously.
			proc.SetMasterRole(false)
		}
		// Wire advisory locker for split-brain prevention.
		// CRITICAL: Use per-processor scopedDB (not shared db/dbManager) so each
		// processor has independent advisoryConn tracking. Sharing a single advisoryConn
		// across multiple processors (parent + aux) causes connection overwrite: the second
		// processor's lock acquisition overwrites the first's connection reference, leaking
		// the first connection and permanently deadlocking its advisory lock.
		proc.SetAdvisoryLocker(scopedDB)

		coord.paymentProcessors[poolID] = proc
		log.Infow("Per-coin payment processor initialized",
			"coin", coinCfg.Symbol,
			"poolId", poolID,
			"scheme", paymentsCfg.Scheme,
			"interval", paymentsCfg.Interval,
			"haEnabled", coord.vipManager != nil,
		)
	}
	coord.poolsMu.RUnlock()

	// AUDIT FIX: Create payment processors for aux chains (merge mining).
	// Without this, aux blocks are recorded in the database but never tracked
	// for maturity confirmation — they stay "pending" forever.
	coord.poolsMu.RLock()
	for poolID, coinPool := range coord.pools {
		if coinPool.GetAuxManager() == nil {
			continue
		}
		// Find parent coin config for payment settings
		var parentCoinCfg *config.CoinPoolConfig
		for i := range cfg.Coins {
			if cfg.Coins[i].PoolID == poolID && cfg.Coins[i].Enabled {
				parentCoinCfg = &cfg.Coins[i]
				break
			}
		}
		if parentCoinCfg == nil || !parentCoinCfg.Payments.Enabled {
			continue
		}

		for _, auxCfg := range coinPool.GetAuxManager().GetAuxChainConfigs() {
			if !auxCfg.Enabled || auxCfg.DaemonClient == nil {
				continue
			}

			auxPoolID := poolID + "_" + strings.ToLower(auxCfg.Symbol)
			// Use parent config's blockMaturity if set, otherwise aux coin's default
			auxMaturity := auxCfg.Coin.CoinbaseMaturity()
			if parentCoinCfg.Payments.BlockMaturity > 0 {
				auxMaturity = parentCoinCfg.Payments.BlockMaturity
			}
			auxPaymentsCfg := &config.PaymentsConfig{
				Enabled:        true,
				Scheme:         parentCoinCfg.Payments.Scheme,
				MinimumPayment: parentCoinCfg.Payments.MinimumPayment,
				BlockTime:      auxCfg.Coin.BlockTime(),
				BlockMaturity:  auxMaturity,
				Interval:       parentCoinCfg.Payments.Interval, // Inherit parent's interval for aux blocks
			}
			auxPoolCfg := &config.PoolConfig{
				ID:      auxPoolID,
				Coin:    auxCfg.Symbol,
				Address: auxCfg.Address,
			}

			auxScopedDB := db.WithPoolID(auxPoolID)
			auxProc := payments.NewProcessor(auxPaymentsCfg, auxPoolCfg, auxScopedDB, auxCfg.DaemonClient, logger)

			if coord.metricsServer != nil {
				auxProc.SetMetrics(coord.metricsServer)
			}
			if coord.vipManager != nil {
				auxProc.SetHAEnabled(true)
				// BUG FIX: Match parent processor M9 fix — default isMaster to false
				// when HA is configured. Without this, aux payment processors start as
				// isMaster=true on both nodes until the first VIP election completes.
				auxProc.SetMasterRole(false)
			}
			// Use per-processor scoped DB for advisory locking (see parent processor comment)
			auxProc.SetAdvisoryLocker(auxScopedDB)

			coord.paymentProcessors[auxPoolID] = auxProc
			log.Infow("Aux chain payment processor initialized",
				"auxPoolId", auxPoolID,
				"parentPoolId", poolID,
				"auxSymbol", auxCfg.Symbol,
				"scheme", auxPaymentsCfg.Scheme,
			)
		}
	}
	coord.poolsMu.RUnlock()

	if len(coord.paymentProcessors) > 0 {
		log.Infow("Payment processors initialized",
			"count", len(coord.paymentProcessors),
			"advisoryLock", coord.dbManager != nil || db != nil,
		)
	}

	log.Infow("Coordinator created",
		"readyPools", len(coord.pools),
		"pendingPools", len(failedCoins),
	)
	return coord, nil
}

// Run starts all coin pools and blocks until shutdown.
func (c *Coordinator) Run(ctx context.Context) error {
	c.runMu.Lock()
	if c.running {
		c.runMu.Unlock()
		return fmt.Errorf("coordinator already running")
	}
	c.running = true
	c.runMu.Unlock()

	ctx, c.cancel = context.WithCancel(ctx)

	// Start HA components if configured
	if c.dbManager != nil {
		c.dbManager.Start(ctx)
		c.logger.Info("Database HA manager started")
	}
	if c.vipManager != nil {
		// Set supported coins for VIP election awareness
		coins := make([]string, 0)
		for _, coin := range c.cfg.Coins {
			if coin.Enabled {
				coins = append(coins, strings.ToUpper(coin.Symbol))
			}
		}
		c.vipManager.SetSupportedCoins(coins)

		// Wire role change handler
		c.vipManager.SetRoleChangeHandler(func(oldRole, newRole ha.Role) {
			c.handleRoleChange(oldRole, newRole)
		})

		// Wire address mismatch handler — alerts when HA peer has different
		// payout address for same coin (would cause split payouts on failover)
		c.vipManager.SetAddressMismatchHandler(func(coin, localAddr, remoteAddr, remoteNodeID string) {
			c.logger.Errorw("CRITICAL: HA payout address mismatch detected — failover will send funds to different address",
				"coin", coin,
				"localAddress", localAddr,
				"remoteAddress", remoteAddr,
				"remoteNode", remoteNodeID,
			)
		})

		if err := c.vipManager.Start(ctx); err != nil {
			c.logger.Errorw("Failed to start VIP manager", "error", err)
		} else {
			c.logger.Info("VIP manager started")
		}
	}
	// AUDIT FIX (ISSUE-3): Start Redis dedup health monitoring (V1 PARITY).
	// Enables graceful degradation to local fallback when Redis is unavailable.
	if c.redisDedupTracker != nil {
		c.redisDedupTracker.StartHealthCheck(ctx)
		c.logger.Info("Redis dedup health check started")
	}
	if c.replicationManager != nil {
		if err := c.replicationManager.Start(ctx); err != nil {
			c.logger.Errorw("Failed to start replication manager", "error", err)
		} else {
			c.logger.Info("PostgreSQL replication manager started")
		}
	}
	// Start replication slot monitor (WAL retention / orphan detection)
	if c.dbManager != nil && c.slotMonitor == nil {
		activeNode := c.dbManager.GetActiveNode()
		if activeNode != nil && activeNode.Pool != nil {
			slotCfg := ha.DefaultReplicationSlotMonitorConfig()
			c.slotMonitor = ha.NewReplicationSlotMonitor(slotCfg, stdlib.OpenDBFromPool(activeNode.Pool), c.logger.Desugar())
			if err := c.slotMonitor.Start(ctx); err != nil {
				c.logger.Errorw("Failed to start replication slot monitor", "error", err)
			} else {
				c.logger.Info("Replication slot monitor started")
			}
		}
	}

	// BUG FIX: Pre-initialize all pools to BACKUP when HA is configured.
	// haRole starts at 0 (RoleUnknown), and the block submission gate only checks
	// for RoleBackup. During the startup window before VIP election completes,
	// RoleUnknown passes the gate — both nodes could submit blocks. Setting
	// RoleBackup here ensures blocks are gated until the VIP election fires the
	// real onRoleChange callback (MASTER or BACKUP).
	if c.vipManager != nil {
		c.poolsMu.RLock()
		for _, p := range c.pools {
			p.OnHARoleChange(ha.RoleUnknown, ha.RoleBackup)
		}
		c.poolsMu.RUnlock()
	}

	// Start all ready coin pools
	c.poolsMu.RLock()
	poolCount := len(c.pools)
	c.poolsMu.RUnlock()

	if poolCount > 0 {
		c.logger.Infow("Starting coin pools", "count", poolCount)

		type startFailure struct {
			poolID string
			err    error
		}
		failedStarts := make(chan startFailure, poolCount)
		var startWg sync.WaitGroup

		c.poolsMu.RLock()
		for poolID, pool := range c.pools {
			startWg.Add(1)
			go func(id string, p *CoinPool) {
				defer startWg.Done()
				c.logger.Infow("Starting coin pool", "poolId", id, "coin", p.Symbol())
				if err := p.Start(ctx); err != nil {
					failedStarts <- startFailure{poolID: id, err: err}
				}
			}(poolID, pool)
		}
		c.poolsMu.RUnlock()

		// Wait for all pools to start
		startWg.Wait()
		close(failedStarts)

		// Move failed-start pools to the retry list so retryFailedCoinsLoop
		// can recover them when the underlying issue (e.g., daemon unavailable,
		// port conflict) resolves. Without this, a transient startup error
		// leaves the pool permanently dead until coordinator restart.
		for sf := range failedStarts {
			c.logger.Warnw("Pool startup error (will retry during grace period)",
				"poolId", sf.poolID, "error", sf.err)

			c.poolsMu.Lock()
			failedPool, exists := c.pools[sf.poolID]
			if exists {
				delete(c.pools, sf.poolID)
				// Best-effort stop to release any resources the pool acquired
				if stopErr := failedPool.Stop(); stopErr != nil {
					c.logger.Warnw("Error stopping failed pool", "poolId", sf.poolID, "error", stopErr)
				}
				// Find the coin config for this pool and add to retry list
				for i := range c.cfg.Coins {
					if c.cfg.Coins[i].PoolID == sf.poolID {
						cfgCopy := c.cfg.Coins[i]
						c.failedCoinsMu.Lock()
						c.failedCoins = append(c.failedCoins, &cfgCopy)
						c.failedCoinsMu.Unlock()
						break
					}
				}
			}
			c.poolsMu.Unlock()
		}
	}

	// Count running pools
	c.poolsMu.RLock()
	runningCount := 0
	for _, p := range c.pools {
		if p.IsRunning() {
			runningCount++
		}
	}
	c.poolsMu.RUnlock()

	c.failedCoinsMu.Lock()
	pendingCount := len(c.failedCoins)
	c.failedCoinsMu.Unlock()
	c.logger.Infow("Initial startup complete",
		"runningPools", runningCount,
		"pendingPools", pendingCount,
	)

	// Register running pools with API server
	if c.apiServer != nil {
		c.poolsMu.RLock()
		for poolID, pool := range c.pools {
			c.apiServer.RegisterPool(poolID, pool)

			// BUG FIX: Register aux chain pools with API server.
			// Without this, Sentinel's discover_active_aux_pools() can't find aux pools
			// via /api/pools, and /api/pools/{aux_pool_id}/blocks returns 404.
			// Aux blocks are recorded in separate DB tables but were invisible to the API.
			if pool.GetAuxManager() != nil {
				for _, auxCfg := range pool.GetAuxManager().GetAuxChainConfigs() {
					if !auxCfg.Enabled {
						continue
					}
					auxPoolID := poolID + "_" + strings.ToLower(auxCfg.Symbol)
					c.apiServer.RegisterPool(auxPoolID, &auxPoolProvider{
						parent: pool,
						symbol: strings.ToLower(auxCfg.Symbol),
						poolID: auxPoolID,
					})
				}
			}
		}
		c.poolsMu.RUnlock()

		// Start API server
		if err := c.apiServer.Start(ctx); err != nil {
			c.logger.Errorw("Failed to start V2 API server", "error", err)
		} else {
			c.logger.Infow("V2 API server started", "port", c.cfg.Global.APIPort)
		}
	}

	// Start Prometheus metrics server
	if c.metricsServer != nil {
		if err := c.metricsServer.Start(ctx); err != nil {
			c.logger.Errorw("Failed to start metrics server", "error", err)
		} else {
			c.logger.Infow("Prometheus metrics server started", "port", c.cfg.Global.MetricsPort)
		}
	}

	// Start per-coin payment processors
	c.paymentProcessorMu.RLock()
	for poolID, proc := range c.paymentProcessors {
		if err := proc.Start(ctx); err != nil {
			c.logger.Errorw("Failed to start payment processor",
				"poolId", poolID,
				"error", err,
			)
		} else {
			c.logger.Infow("Payment processor started",
				"poolId", poolID,
				"haEnabled", c.vipManager != nil,
			)
		}
	}
	c.paymentProcessorMu.RUnlock()

	// If we have failed coins, start the retry loop
	if len(c.failedCoins) > 0 {
		c.wg.Add(1)
		go c.retryFailedCoinsLoop(ctx)
	}

	// Print summary (will update as more pools come online)
	c.printStartupSummary()

	// Start Multi coin smart port (multi-port) if configured
	if c.cfg.MultiPort.Enabled {
		if err := c.startMultiPort(ctx); err != nil {
			return fmt.Errorf("failed to start multi-port server: %w", err)
		}
	}

	// Start coordinator health monitoring
	c.wg.Add(1)
	go c.healthLoop(ctx)

	// M15: Warn when stratum rate limiting is not configured for any enabled coin.
	// Without rate limiting, the pool is more vulnerable to connection-flooding attacks.
	// Rate limiting is disabled by default for hashrate marketplace compatibility,
	// but operators running private pools should enable it.
	allRateLimitingDisabled := true
	for _, coin := range c.cfg.Coins {
		if coin.Enabled && coin.Stratum.Banning.Enabled {
			allRateLimitingDisabled = false
			break
		}
	}
	if allRateLimitingDisabled {
		c.logger.Warnw("SECURITY: No stratum banning/rate limiting is enabled for any coin. "+
			"This is expected for hashrate marketplace compatibility but increases exposure to "+
			"connection-flooding attacks. For private pools, enable banning in stratum config.",
		)
	}

	// Start API Sentinel monitoring (internal pool health alerts exposed via API)
	if c.cfg.Global.Sentinel.Enabled {
		c.sentinel = NewSentinel(c, &c.cfg.Global.Sentinel, c.metricsServer, c.logger.Desugar())
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.sentinel.Run(ctx)
		}()

		// Wire sentinel alerts to API server
		if c.apiServer != nil {
			c.apiServer.SetSentinelProvider(&sentinelAlertAdapter{coord: c})
		}

		c.logger.Infow("API Sentinel started",
			"checkInterval", c.cfg.Global.Sentinel.CheckInterval,
			"alertBufferSize", maxRecentAlerts,
		)
	}

	// Wait for shutdown signal
	<-ctx.Done()

	return c.shutdown()
}

// retryFailedCoinsLoop periodically attempts to create and start failed coin pools.
func (c *Coordinator) retryFailedCoinsLoop(ctx context.Context) {
	defer c.wg.Done()

	startTime := time.Now()
	ticker := time.NewTicker(c.startupCfg.RetryInterval)
	defer ticker.Stop()

	c.logger.Infow("Starting failed coin retry loop",
		"pendingCoins", len(c.failedCoins),
		"gracePeriod", c.startupCfg.GracePeriod,
		"retryInterval", c.startupCfg.RetryInterval,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.failedCoinsMu.Lock()
			if len(c.failedCoins) == 0 {
				c.failedCoinsMu.Unlock()
				c.logger.Info("All coin pools now online, stopping retry loop")
				return
			}

			// Check if we're still within the grace period
			elapsed := time.Since(startTime)
			if elapsed > c.startupCfg.GracePeriod {
				remaining := len(c.failedCoins)
				c.failedCoinsMu.Unlock()
				c.logger.Warnw("Startup grace period expired, some coins remain offline",
					"offlineCoins", remaining,
					"gracePeriod", c.startupCfg.GracePeriod,
				)
				// Continue running with available coins (don't stop the retry loop entirely
				// as nodes may come online later)
				ticker.Reset(5 * time.Minute) // Slow down retries after grace period
				continue
			}

			// Try to create and start each failed coin.
			// BUG FIX: Snapshot the failed list and release the lock before doing
			// any work that requires poolsMu, eliminating the unsafe unlock/re-lock
			// pattern that could corrupt the iterator.
			snapshot := make([]*config.CoinPoolConfig, len(c.failedCoins))
			copy(snapshot, c.failedCoins)
			c.failedCoinsMu.Unlock()

			var stillFailed []*config.CoinPoolConfig
			type startedPool struct {
				poolID string
				pool   *CoinPool
				cfg    *config.CoinPoolConfig
			}
			var succeeded []startedPool

			for _, coinCfg := range snapshot {
				c.logger.Infow("Retrying coin pool creation",
					"coin", coinCfg.Symbol,
					"poolId", coinCfg.PoolID,
					"elapsed", elapsed.Round(time.Second),
				)

				pool, err := NewCoinPool(&CoinPoolConfig{
					CoinConfig:        coinCfg,
					CelebrationConfig: &c.cfg.Global.Celebration,
					DBPool:            c.db,
					Logger:            c.logger.Desugar(),
					MetricsServer:     c.metricsServer,
				})
				if err != nil {
					c.logger.Warnw("Coin pool creation still failing",
						"coin", coinCfg.Symbol,
						"error", err,
						"nextRetry", c.startupCfg.RetryInterval,
					)
					stillFailed = append(stillFailed, coinCfg)
					continue
				}

				// Pool created, now start it
				if err := pool.Start(ctx); err != nil {
					c.logger.Warnw("Coin pool start failed",
						"coin", coinCfg.Symbol,
						"error", err,
					)
					if stopErr := pool.Stop(); stopErr != nil {
						c.logger.Errorw("Error stopping failed coin pool", "coin", coinCfg.Symbol, "error", stopErr)
					}
					stillFailed = append(stillFailed, coinCfg)
					continue
				}

				// AUDIT FIX (ISSUE-3): Wire Redis dedup tracker to late-created pools
				if c.redisDedupTracker != nil {
					pool.SetRedisDedupTracker(c.redisDedupTracker)
				}

				// Wire sync status callback to late-created pools for HA master election
				if c.vipManager != nil {
					pool.SetSyncStatusCallback(c.vipManager.UpdateCoinSyncStatus)
				}

				succeeded = append(succeeded, startedPool{poolID: coinCfg.PoolID, pool: pool, cfg: coinCfg})
			}

			// Add all succeeded pools under poolsMu (no lock ordering issue)
			if len(succeeded) > 0 {
				c.poolsMu.Lock()
				for _, s := range succeeded {
					c.pools[s.poolID] = s.pool
				}
				c.poolsMu.Unlock()

				// BUG FIX: Set haRole on late-started pools to match current VIP state.
				// By this point the VIP election has completed, so IsMaster() is valid.
				// Without this, late pools start with haRole=RoleUnknown, bypassing the
				// block submission gate until the next role change event.
				if c.vipManager != nil {
					if c.vipManager.IsMaster() {
						for _, s := range succeeded {
							s.pool.OnHARoleChange(ha.RoleUnknown, ha.RoleMaster)
						}
					} else {
						for _, s := range succeeded {
							s.pool.OnHARoleChange(ha.RoleUnknown, ha.RoleBackup)
						}
					}
				}

				// Register with API server and create payment processors outside of lock
				for _, s := range succeeded {
					if c.apiServer != nil {
						c.apiServer.RegisterPool(s.poolID, s.pool)

						// BUG FIX: Register aux chain pools for late-started pools.
						// Mirrors the init-time aux registration above.
						if s.pool.GetAuxManager() != nil {
							for _, auxCfg := range s.pool.GetAuxManager().GetAuxChainConfigs() {
								if !auxCfg.Enabled {
									continue
								}
								auxPoolID := s.poolID + "_" + strings.ToLower(auxCfg.Symbol)
								c.apiServer.RegisterPool(auxPoolID, &auxPoolProvider{
									parent: s.pool,
									symbol: strings.ToLower(auxCfg.Symbol),
									poolID: auxPoolID,
								})
							}
						}
					}

					// Create payment processor for late-started pool (mirrors init-time logic at lines 424-499)
					if s.cfg.Payments.Enabled {
						var daemonRPC payments.DaemonRPC
						if s.pool.nodeManager != nil {
							primary := s.pool.nodeManager.GetPrimary()
							if primary != nil && primary.Client != nil {
								daemonRPC = primary.Client
							}
						}
						if daemonRPC != nil {
							// Use config's blockMaturity if explicitly set, otherwise coin default
							lateMaturity := s.pool.coin.CoinbaseMaturity()
							if s.cfg.Payments.BlockMaturity > 0 {
								lateMaturity = s.cfg.Payments.BlockMaturity
							}
							paymentsCfg := &config.PaymentsConfig{
								Enabled:        s.cfg.Payments.Enabled,
								Interval:       s.cfg.Payments.Interval,
								MinimumPayment: s.cfg.Payments.MinimumPayment,
								Scheme:         s.cfg.Payments.Scheme,
								BlockMaturity:  lateMaturity,
								BlockTime:      s.pool.coin.BlockTime(),
							}
							poolCfgForProc := &config.PoolConfig{
								ID:      s.cfg.PoolID,
								Coin:    s.cfg.Symbol,
								Address: s.cfg.Address,
							}
							scopedDB := c.db.WithPoolID(s.poolID)
							proc := payments.NewProcessor(paymentsCfg, poolCfgForProc, scopedDB, daemonRPC, c.logger.Desugar())
							if c.metricsServer != nil {
								proc.SetMetrics(c.metricsServer)
							}
							if c.vipManager != nil {
								proc.SetHAEnabled(true)
								// AUDIT FIX (PF-1): NewProcessor defaults isMaster=true.
								// Late-started processors bypass HA fencing because
								// demoteToBackup() only iterates existing processors.
								if !c.vipManager.IsMaster() {
									proc.SetMasterRole(false)
								}
							}
							// Use per-processor scoped DB for advisory locking (see initial path comment)
							proc.SetAdvisoryLocker(scopedDB)
							if err := proc.Start(ctx); err != nil {
								c.logger.Errorw("Failed to start payment processor for late-started pool",
									"coin", s.cfg.Symbol, "poolId", s.poolID, "error", err,
								)
							} else {
								c.paymentProcessorMu.Lock()
								c.paymentProcessors[s.poolID] = proc
								c.paymentProcessorMu.Unlock()
								c.logger.Infow("Payment processor started for late-started pool",
									"coin", s.cfg.Symbol, "poolId", s.poolID,
								)
							}
						} else {
							c.logger.Warnw("Payment processor skipped for late-started pool — no daemon client",
								"coin", s.cfg.Symbol, "poolId", s.poolID,
							)
						}
					}

					// AUDIT FIX (M-1): Create aux chain payment processors for late-started pools.
					// Without this, merge-mined aux blocks stay "pending" forever.
					if s.cfg.Payments.Enabled && s.pool.GetAuxManager() != nil {
						for _, auxCfg := range s.pool.GetAuxManager().GetAuxChainConfigs() {
							if !auxCfg.Enabled || auxCfg.DaemonClient == nil {
								continue
							}
							auxPoolID := s.poolID + "_" + strings.ToLower(auxCfg.Symbol)
							auxMaturity := auxCfg.Coin.CoinbaseMaturity()
							if s.cfg.Payments.BlockMaturity > 0 {
								auxMaturity = s.cfg.Payments.BlockMaturity
							}
							auxPaymentsCfg := &config.PaymentsConfig{
								Enabled:        true,
								Scheme:         s.cfg.Payments.Scheme,
								MinimumPayment: s.cfg.Payments.MinimumPayment,
								BlockTime:      auxCfg.Coin.BlockTime(),
								BlockMaturity:  auxMaturity,
								Interval:       s.cfg.Payments.Interval,
							}
							auxPoolCfg := &config.PoolConfig{
								ID:      auxPoolID,
								Coin:    auxCfg.Symbol,
								Address: auxCfg.Address,
							}
							auxScopedDB := c.db.WithPoolID(auxPoolID)
							auxProc := payments.NewProcessor(auxPaymentsCfg, auxPoolCfg, auxScopedDB, auxCfg.DaemonClient, c.logger.Desugar())
							if c.metricsServer != nil {
								auxProc.SetMetrics(c.metricsServer)
							}
							if c.vipManager != nil {
								auxProc.SetHAEnabled(true)
								// AUDIT FIX (PF-1): Same fix as parent processor above.
								if !c.vipManager.IsMaster() {
									auxProc.SetMasterRole(false)
								}
							}
							// Use per-processor scoped DB for advisory locking (see initial path comment)
							auxProc.SetAdvisoryLocker(auxScopedDB)
							if err := auxProc.Start(ctx); err != nil {
								c.logger.Errorw("Failed to start aux payment processor for late-started pool",
									"auxPoolId", auxPoolID, "parentPoolId", s.poolID, "error", err,
								)
							} else {
								c.paymentProcessorMu.Lock()
								c.paymentProcessors[auxPoolID] = auxProc
								c.paymentProcessorMu.Unlock()
								c.logger.Infow("Aux chain payment processor started for late-started pool",
									"auxPoolId", auxPoolID, "parentPoolId", s.poolID, "auxSymbol", auxCfg.Symbol,
								)
							}
						}
					}

					// Register late-started pool with multi-port infrastructure (if running).
					// Without this, multi-port miners are never routed to recovered coins.
					if c.multiServer != nil {
						c.multiServer.RegisterCoinPool(s.pool)
					}
					if c.diffMonitor != nil {
						coinImpl, _ := coin.Create(s.pool.Symbol())
						if coinImpl != nil {
							blockTime := float64(coinImpl.BlockTime())
							c.diffMonitor.RegisterCoin(s.pool, blockTime)
						}
					}

					c.logger.Infow("Coin pool now online!",
						"coin", s.cfg.Symbol,
						"poolId", s.poolID,
						"port", s.cfg.Stratum.Port,
					)
				}
			}

			// Update failedCoins under its own lock
			c.failedCoinsMu.Lock()
			c.failedCoins = stillFailed
			c.failedCoinsMu.Unlock()

			if len(stillFailed) == 0 {
				c.logger.Info("All coin pools now online!")
				return
			}
		}
	}
}

// shutdown gracefully shuts down all components.
func (c *Coordinator) shutdown() error {
	c.logger.Info("Shutting down coordinator...")

	// Stop API server first
	if c.apiServer != nil {
		if err := c.apiServer.Stop(); err != nil {
			c.logger.Errorw("Error stopping V2 API server", "error", err)
		} else {
			c.logger.Info("V2 API server stopped")
		}
	}

	// Stop metrics server
	if c.metricsServer != nil {
		if err := c.metricsServer.Stop(); err != nil {
			c.logger.Errorw("Error stopping metrics server", "error", err)
		} else {
			c.logger.Info("Metrics server stopped")
		}
	}

	// Wait for coordinator goroutines first (retry loop may be writing to paymentProcessors)
	// AUDIT FIX (M-4): wg.Wait before payment iteration prevents concurrent map access
	c.wg.Wait()

	// Stop all payment processors before pools (prevent payment processing during shutdown)
	c.paymentProcessorMu.RLock()
	for poolID, proc := range c.paymentProcessors {
		proc.Stop()
		c.logger.Infow("Payment processor stopped", "poolId", poolID)
	}
	c.paymentProcessorMu.RUnlock()

	// Stop multi-coin server and difficulty monitor
	if c.multiServer != nil {
		if err := c.multiServer.Stop(); err != nil {
			c.logger.Errorw("Error stopping multi-coin server", "error", err)
		} else {
			c.logger.Info("Multi-coin server stopped")
		}
	}
	if c.diffMonitor != nil {
		c.diffMonitor.Stop()
		c.logger.Info("Difficulty monitor stopped")
	}

	// Stop all pools
	c.stopAllPools()

	// Stop HA components (VIP first to release VIP before database stops)
	if c.vipManager != nil {
		if err := c.vipManager.Stop(); err != nil {
			c.logger.Errorw("Error stopping VIP manager", "error", err)
		} else {
			c.logger.Info("VIP manager stopped")
		}
	}
	if c.slotMonitor != nil {
		c.slotMonitor.Stop()
		c.logger.Info("Replication slot monitor stopped")
	}
	if c.replicationManager != nil {
		c.replicationManager.Stop()
		c.logger.Info("Replication manager stopped")
	}
	if c.dbManager != nil {
		c.dbManager.Stop()
		c.logger.Info("Database HA manager stopped")
	}

	// Close database
	if err := c.db.Close(); err != nil {
		c.logger.Errorw("Error closing database", "error", err)
	}

	c.runMu.Lock()
	c.running = false
	c.runMu.Unlock()

	c.logger.Info("Coordinator shutdown complete")
	return nil
}

// stopAllPools stops all coin pools.
func (c *Coordinator) stopAllPools() {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()

	var wg sync.WaitGroup
	for poolID, pool := range c.pools {
		wg.Add(1)
		go func(id string, p *CoinPool) {
			defer wg.Done()
			c.logger.Infow("Stopping coin pool", "poolId", id)
			if err := p.Stop(); err != nil {
				c.logger.Errorw("Error stopping coin pool", "poolId", id, "error", err)
			}
		}(poolID, pool)
	}
	wg.Wait()
}

// printStartupSummary prints a summary of started pools.
func (c *Coordinator) printStartupSummary() {
	c.poolsMu.RLock()
	runningPools := len(c.pools)
	c.poolsMu.RUnlock()

	c.failedCoinsMu.Lock()
	pendingPools := len(c.failedCoins)
	c.failedCoinsMu.Unlock()

	c.logger.Info("=== Spiral Pool V2 Started ===")

	c.poolsMu.RLock()
	for poolID, pool := range c.pools {
		c.logger.Infow("Pool running",
			"poolId", poolID,
			"coin", pool.Symbol(),
			"port", pool.cfg.Stratum.Port,
			"address", pool.cfg.Address,
		)
	}
	c.poolsMu.RUnlock()

	// Show pending pools if any
	if pendingPools > 0 {
		c.failedCoinsMu.Lock()
		for _, coinCfg := range c.failedCoins {
			c.logger.Warnw("Pool pending (node not ready)",
				"coin", coinCfg.Symbol,
				"poolId", coinCfg.PoolID,
				"retryInterval", c.startupCfg.RetryInterval,
			)
		}
		c.failedCoinsMu.Unlock()
		c.logger.Infow("Some pools are waiting for nodes to become ready",
			"running", runningPools,
			"pending", pendingPools,
			"gracePeriod", c.startupCfg.GracePeriod,
		)
	}

	c.logger.Info("==============================")
}

// healthLoop monitors overall coordinator health.
func (c *Coordinator) healthLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.logHealthStatus()
		}
	}
}

// logHealthStatus logs health status of all pools.
func (c *Coordinator) logHealthStatus() {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()

	for poolID, pool := range c.pools {
		stats := pool.Stats()
		c.logger.Infow("Pool health",
			"poolId", poolID,
			"coin", stats.Coin,
			"connections", stats.Connections,
			"acceptedShares", stats.AcceptedShares,
			"rejectedShares", stats.RejectedShares,
			"healthyNodes", stats.NodeStats.HealthyNodes,
			"totalNodes", stats.NodeStats.TotalNodes,
			"primaryNode", stats.NodeStats.PrimaryNodeID,
		)
	}
}

// Stats returns statistics for all pools.
func (c *Coordinator) Stats() CoordinatorStats {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()

	stats := CoordinatorStats{
		Pools: make(map[string]CoinPoolStats),
	}

	for poolID, pool := range c.pools {
		poolStats := pool.Stats()
		stats.Pools[poolID] = poolStats
		stats.TotalConnections += poolStats.Connections
		stats.TotalShares += poolStats.TotalShares
		stats.TotalAccepted += poolStats.AcceptedShares
		stats.TotalRejected += poolStats.RejectedShares
	}

	stats.PoolCount = len(c.pools)
	return stats
}

// CoordinatorStats represents aggregate statistics.
type CoordinatorStats struct {
	PoolCount        int
	TotalConnections int64
	TotalShares      uint64
	TotalAccepted    uint64
	TotalRejected    uint64
	Pools            map[string]CoinPoolStats
}

// GetPool returns a specific coin pool by ID.
func (c *Coordinator) GetPool(poolID string) (*CoinPool, bool) {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()
	pool, ok := c.pools[poolID]
	return pool, ok
}

// GetPoolBySymbol returns a coin pool by coin symbol. Test-only.
func (c *Coordinator) GetPoolBySymbol(symbol string) (*CoinPool, bool) {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()

	for _, pool := range c.pools {
		if pool.Symbol() == symbol {
			return pool, true
		}
	}
	return nil, false
}

// ListPools returns all pool IDs.
func (c *Coordinator) ListPools() []string {
	c.poolsMu.RLock()
	defer c.poolsMu.RUnlock()

	ids := make([]string, 0, len(c.pools))
	for id := range c.pools {
		ids = append(ids, id)
	}
	return ids
}

// GetSentinelAlerts returns recent alerts from the API Sentinel since the given time.
// Returns nil if the sentinel is not running.
func (c *Coordinator) GetSentinelAlerts(since time.Time) []SentinelAlert {
	if c.sentinel == nil {
		return nil
	}
	return c.sentinel.GetRecentAlerts(since)
}

// sentinelAlertAdapter adapts Coordinator.GetSentinelAlerts to the api.SentinelAlertProvider
// interface, converting pool.SentinelAlert → api.SentinelAlert to avoid circular imports.
type sentinelAlertAdapter struct {
	coord *Coordinator
}

func (a *sentinelAlertAdapter) GetSentinelAlerts(since time.Time) []api.SentinelAlert {
	poolAlerts := a.coord.GetSentinelAlerts(since)
	if poolAlerts == nil {
		return nil
	}
	result := make([]api.SentinelAlert, len(poolAlerts))
	for i, pa := range poolAlerts {
		result[i] = api.SentinelAlert{
			AlertType: pa.AlertType,
			Severity:  pa.Severity,
			Coin:      pa.Coin,
			PoolID:    pa.PoolID,
			Message:   pa.Message,
			Timestamp: pa.Timestamp,
		}
	}
	return result
}

// IsRunning returns whether the coordinator is running.
func (c *Coordinator) IsRunning() bool {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.running
}

// auxPoolProvider wraps a parent CoinPool to expose aux chain pools in the API.
// Aux chains share the parent's stratum infrastructure but have separate block databases.
// This enables Sentinel to discover aux pools via /api/pools and query their blocks
// via /api/pools/{aux_pool_id}/blocks.
type auxPoolProvider struct {
	parent *CoinPool
	symbol string // lowercase aux coin symbol (e.g., "nmc")
	poolID string // aux pool ID (e.g., "btc_sha256_1_nmc")
}

func (a *auxPoolProvider) Symbol() string               { return a.symbol }
func (a *auxPoolProvider) PoolID() string               { return a.poolID }
func (a *auxPoolProvider) GetConnections() int64         { return a.parent.GetConnections() }
func (a *auxPoolProvider) GetHashrate() float64          { return a.parent.GetHashrate() }
func (a *auxPoolProvider) GetSharesPerSecond() float64   { return a.parent.GetSharesPerSecond() }
func (a *auxPoolProvider) GetBlockHeight() uint64        { return 0 }
func (a *auxPoolProvider) GetNetworkDifficulty() float64 { return 0 }
func (a *auxPoolProvider) GetNetworkHashrate() float64   { return 0 }
func (a *auxPoolProvider) GetBlocksFound() int64         { return 0 }
func (a *auxPoolProvider) GetBlockReward() float64       { return 0 }
func (a *auxPoolProvider) GetPoolEffort() float64        { return 0 }
func (a *auxPoolProvider) GetStratumPort() int           { return 0 }
func (a *auxPoolProvider) GetActiveConnections() []api.WorkerConnection {
	return a.parent.GetActiveConnections()
}
func (a *auxPoolProvider) GetRouterProfiles() []api.RouterProfile {
	return a.parent.GetRouterProfiles()
}
func (a *auxPoolProvider) GetWorkersByClass() map[string]int {
	return a.parent.GetWorkersByClass()
}
func (a *auxPoolProvider) GetPipelineStats() api.PipelineStats {
	return a.parent.GetPipelineStats()
}
func (a *auxPoolProvider) GetPaymentStats() (*api.PaymentStats, error) {
	return &api.PaymentStats{}, nil // Aux chains don't have independent payment processing
}
func (a *auxPoolProvider) KickWorkerByIP(ip string) int {
	return a.parent.KickWorkerByIP(ip) // Workers are shared with parent
}

// ============================================================================
// HA: VIP Manager, Database Manager, and Role Change Handling
// ============================================================================

// SetVIPManager sets the VIP manager for the coordinator.
// Call this before Run() to enable HA VIP failover for all coin pools.
func (c *Coordinator) SetVIPManager(vipMgr *ha.VIPManager) {
	c.vipManager = vipMgr
}

// SetDatabaseManager sets the HA database manager for the coordinator.
func (c *Coordinator) SetDatabaseManager(dbMgr *database.DatabaseManager) {
	c.dbManager = dbMgr
}

// SetReplicationManager sets the replication manager for the coordinator.
func (c *Coordinator) SetReplicationManager(replMgr *database.ReplicationManager) {
	c.replicationManager = replMgr
}

// handleRoleChange is called by the VIP manager when the HA role changes.
// It coordinates enabling/disabling subsystems based on the new role,
// then propagates the role change to all running coin pools.
//
// AUDIT FIX (H-1): CoinPools must see the new role BEFORE payment processor transitions.
// V1 sets haRole as its VERY FIRST action (pool.go handleRoleChange) so concurrent
// handleBlock goroutines immediately respect the HA gate. V2 must do the same:
// notify CoinPools first, then enable/disable payments.
//
// Promotion order: notify pools (haRole) ➜ verify DB ➜ enable payments ➜ metrics
// Demotion order: notify pools (haRole) ➜ disable payments ➜ metrics
func (c *Coordinator) handleRoleChange(oldRole, newRole ha.Role) {
	c.logger.Infow("Coordinator HA role changed",
		"from", oldRole.String(),
		"to", newRole.String(),
	)

	// 1. Notify all running coin pools FIRST — sets haRole atomically on each CoinPool
	// so concurrent block submissions immediately see the new role.
	c.poolsMu.RLock()
	for poolID, pool := range c.pools {
		c.logger.Infow("Notifying coin pool of HA role change",
			"poolId", poolID,
			"coin", pool.Symbol(),
			"newRole", newRole.String(),
		)
		pool.OnHARoleChange(oldRole, newRole)
	}
	c.poolsMu.RUnlock()

	// 2. Enable/disable payment processing after CoinPools have updated their roles
	switch newRole {
	case ha.RoleMaster:
		c.promoteToMaster()
	case ha.RoleBackup, ha.RoleObserver:
		// BUG FIX (M8): RoleObserver must also demote payments and cancel submissions.
		// Without this, transitioning from Master to Observer leaves payment processors
		// in isMaster=true state and block submissions continue.
		c.demoteToBackup()
	}

	// 3. Update metrics
	if c.metricsServer != nil {
		c.metricsServer.SetHANodeRole(strings.ToLower(newRole.String()))
		c.metricsServer.SetHAClusterState("running")
	}
}

// promoteToMaster enables all subsystems for active operation.
// Called when this node becomes the HA master.
func (c *Coordinator) promoteToMaster() {
	c.logger.Info("Beginning MASTER promotion sequence...")

	// 1. Verify database is writable
	if c.dbManager != nil {
		activeNode := c.dbManager.GetActiveNode()
		if activeNode != nil && activeNode.State != database.DBNodeHealthy {
			c.logger.Warnw("MASTER promotion: active DB node not healthy, waiting for recovery",
				"nodeID", activeNode.ID,
				"state", activeNode.State.String(),
			)
			time.Sleep(5 * time.Second)
			// FIX Issue 26: Re-check DB health after sleep. Advisory lock prevents
			// double-payment regardless, but logging recovery status gives operators
			// visibility into whether Patroni failover completed in time.
			activeNode = c.dbManager.GetActiveNode()
			if activeNode != nil && activeNode.State != database.DBNodeHealthy {
				c.logger.Warnw("MASTER promotion: DB still not healthy after 5s wait — enabling payments (advisory lock provides safety)",
					"nodeID", activeNode.ID,
					"state", activeNode.State.String(),
				)
			} else {
				c.logger.Info("MASTER promotion: DB recovered during wait")
			}
		}
	}

	// 2. Enable payment processing on all per-coin processors
	c.paymentProcessorMu.RLock()
	for poolID, proc := range c.paymentProcessors {
		proc.SetMasterRole(true)
		c.logger.Infow("Payment processor enabled (master)", "poolId", poolID)
	}
	c.paymentProcessorMu.RUnlock()

	c.logger.Info("MASTER promotion complete - all subsystems active")
}

// demoteToBackup disables write-path subsystems for standby operation.
// Called when this node becomes an HA backup.
func (c *Coordinator) demoteToBackup() {
	c.logger.Info("Beginning BACKUP demotion sequence...")

	// 1. Disable payment processing FIRST on all processors (prevent split-brain payments)
	c.paymentProcessorMu.RLock()
	for poolID, proc := range c.paymentProcessors {
		proc.SetMasterRole(false)
		c.logger.Infow("Payment processor paused (backup)", "poolId", poolID)
	}
	c.paymentProcessorMu.RUnlock()

	c.logger.Info("BACKUP demotion complete - payments paused")
}

// ══════════════════════════════════════════════════════════════════════════════
// SCHEDULING PORT (v2.1)
// ══════════════════════════════════════════════════════════════════════════════

// startMultiPort initializes and starts the Multi coin smart port.
// It creates the DifficultyMonitor, CoinSelector, and MultiServer, wiring
// them to the running CoinPools that match the configured allowed coins.
func (c *Coordinator) startMultiPort(ctx context.Context) error {
	mpCfg := c.cfg.MultiPort
	coinSymbols := mpCfg.CoinSymbols()

	c.logger.Infow("Starting multi coin smart port",
		"port", mpCfg.Port,
		"coins", mpCfg.Coins,
	)

	// Validate: at least 2 coins needed for routing to make sense
	if len(coinSymbols) < 2 {
		return fmt.Errorf("multi_port requires at least 2 coins, got %d", len(coinSymbols))
	}

	// Validate: all configured coins must be enabled and SHA-256d
	c.poolsMu.RLock()
	for _, sym := range coinSymbols {
		found := false
		for _, pool := range c.pools {
			if strings.EqualFold(pool.Symbol(), sym) {
				found = true
				// Verify algorithm is SHA-256d (multi-port only works for same-algo coins)
				coinImpl, err := coin.Create(pool.Symbol())
				if err == nil && coinImpl.Algorithm() != "sha256d" {
					c.poolsMu.RUnlock()
					return fmt.Errorf("multi_port coin %s uses algorithm %s, only sha256d is supported",
						sym, coinImpl.Algorithm())
				}
				break
			}
		}
		if !found {
			c.poolsMu.RUnlock()
			return fmt.Errorf("multi_port coin %s is not configured or not running", sym)
		}
	}
	c.poolsMu.RUnlock()

	// Validate: weights must sum to exactly 100
	totalWeight := 0
	for _, coinCfg := range mpCfg.Coins {
		if coinCfg.Weight < 0 {
			return fmt.Errorf("multi_port coin weights cannot be negative")
		}
		totalWeight += coinCfg.Weight
	}
	if totalWeight != 100 {
		return fmt.Errorf("multi_port coin weights must sum to 100, got %d", totalWeight)
	}

	// 1. Create DifficultyMonitor (monitors coin availability for failover)
	c.diffMonitor = scheduler.NewMonitor(scheduler.MonitorConfig{
		PollInterval: mpCfg.CheckInterval,
		Logger:       c.logger.Desugar(),
	})

	// Register each multi-port coin with the monitor
	c.poolsMu.RLock()
	for _, sym := range coinSymbols {
		for _, pool := range c.pools {
			if strings.EqualFold(pool.Symbol(), sym) {
				coinImpl, err := coin.Create(pool.Symbol())
				if err != nil {
					c.poolsMu.RUnlock()
					return fmt.Errorf("multi_port: failed to create coin %q: %w", sym, err)
				}
				blockTime := float64(coinImpl.BlockTime())
				c.diffMonitor.RegisterCoin(pool, blockTime)
				break
			}
		}
	}
	c.poolsMu.RUnlock()

	c.diffMonitor.Start(ctx)

	// 2. Build coin weights from config (sorted for deterministic schedule)
	sort.Strings(coinSymbols)
	var coinWeights []scheduler.CoinWeight
	for _, sym := range coinSymbols {
		routeCfg := mpCfg.Coins[sym]
		coinWeights = append(coinWeights, scheduler.CoinWeight{
			Symbol: sym,
			Weight: routeCfg.Weight,
		})
	}

	// 3. Create CoinSelector
	// Load the user's timezone so the 24h schedule aligns with their local day.
	scheduleLoc := time.UTC
	if mpCfg.Timezone != "" {
		loc, err := time.LoadLocation(mpCfg.Timezone)
		if err != nil {
			c.logger.Warnf("invalid multi_port timezone %q, falling back to UTC: %v", mpCfg.Timezone, err)
		} else {
			scheduleLoc = loc
		}
	}
	c.coinSelector = scheduler.NewSelector(scheduler.SelectorConfig{
		Monitor:       c.diffMonitor,
		AllowedCoins:  coinSymbols,
		CoinWeights:   coinWeights,
		PreferCoin:    mpCfg.PreferCoin,
		MinTimeOnCoin: mpCfg.MinTimeOnCoin,
		Location:      scheduleLoc,
		Logger:        c.logger.Desugar(),
	})

	// 4. Build a stratum config for the multi port (inherit from first coin's settings)
	var stratumCfg *config.StratumConfig
	for _, coinCfg := range c.cfg.Coins {
		if coinCfg.Enabled {
			stratumCfg = &config.StratumConfig{
				Listen:         fmt.Sprintf("0.0.0.0:%d", mpCfg.Port),
				Difficulty:     coinCfg.Stratum.Difficulty,
				Banning:        coinCfg.Stratum.Banning,
				Connection:     coinCfg.Stratum.Connection,
				VersionRolling: coinCfg.Stratum.VersionRolling,
				JobRebroadcast: coinCfg.Stratum.JobRebroadcast,
				TLS: config.TLSConfig{
					Enabled:    coinCfg.Stratum.PortTLS > 0,
					CertFile:   coinCfg.Stratum.TLS.CertFile,
					KeyFile:    coinCfg.Stratum.TLS.KeyFile,
					MinVersion: coinCfg.Stratum.TLS.MinVersion,
				},
			}
			break
		}
	}
	if stratumCfg == nil {
		return fmt.Errorf("no enabled coin found to inherit stratum config from")
	}

	// 5. Create and start MultiServer
	c.multiServer = scheduler.NewMultiServer(scheduler.MultiServerConfig{
		Port:          mpCfg.Port,
		TLSPort:       mpCfg.TLSPort,
		CheckInterval: mpCfg.CheckInterval,
		AllowedCoins:  coinSymbols,
		CoinWeights:   coinWeights,
		PreferCoin:    mpCfg.PreferCoin,
		MinTimeOnCoin: mpCfg.MinTimeOnCoin,
		WalletMap:     mpCfg.WalletMap,
		Stratum:       stratumCfg,
		Logger:        c.logger.Desugar(),
	}, c.diffMonitor, c.coinSelector)

	// Register coin pools with the multi server
	c.poolsMu.RLock()
	for _, sym := range coinSymbols {
		for _, pool := range c.pools {
			if strings.EqualFold(pool.Symbol(), sym) {
				c.multiServer.RegisterCoinPool(pool)
				break
			}
		}
	}
	c.poolsMu.RUnlock()

	if err := c.multiServer.Start(ctx); err != nil {
		c.diffMonitor.Stop()
		return fmt.Errorf("failed to start multi-port server: %w", err)
	}

	// Wire multi-port stats to API server
	if c.apiServer != nil {
		c.apiServer.SetMultiPortProvider(&multiPortAdapter{coord: c})
	}

	c.logger.Infow("Multi coin smart port started",
		"port", mpCfg.Port,
		"coins", coinSymbols,
	)
	return nil
}

// GetMultiServer returns the multi-coin server (nil if not enabled).
func (c *Coordinator) GetMultiServer() *scheduler.MultiServer {
	return c.multiServer
}

// GetDifficultyMonitor returns the difficulty monitor (nil if not enabled).
func (c *Coordinator) GetDifficultyMonitor() *scheduler.Monitor {
	return c.diffMonitor
}

// multiPortAdapter implements api.MultiPortStatsProvider for the coordinator.
type multiPortAdapter struct {
	coord *Coordinator
}

func (a *multiPortAdapter) GetMultiPortStats() *api.MultiPortStats {
	ms := a.coord.multiServer
	if ms == nil {
		return nil
	}
	stats := ms.Stats()
	apiStats := &api.MultiPortStats{
		Enabled:          true,
		Port:             stats.Port,
		ActiveSessions:   stats.ActiveSessions,
		TotalSwitches:    stats.TotalSwitches,
		CoinDistribution: stats.CoinDistribution,
		AllowedCoins:     stats.AllowedCoins,
	}
	if len(stats.CoinWeights) > 0 {
		apiStats.CoinWeights = stats.CoinWeights
	}
	return apiStats
}

func (a *multiPortAdapter) GetMultiPortSwitchHistory(limit int) []api.MultiPortSwitchEvent {
	ms := a.coord.multiServer
	if ms == nil {
		return nil
	}
	events := ms.GetSwitchHistory(limit)
	result := make([]api.MultiPortSwitchEvent, len(events))
	for i, e := range events {
		result[i] = api.MultiPortSwitchEvent{
			SessionID:  e.SessionID,
			WorkerName: e.WorkerName,
			MinerClass: e.MinerClass,
			FromCoin:   e.FromCoin,
			ToCoin:     e.ToCoin,
			Reason:     e.Reason,
			Timestamp:  e.Timestamp,
		}
	}
	return result
}

func (a *multiPortAdapter) GetMultiPortDifficultyStates() map[string]api.MultiPortDiffState {
	mon := a.coord.diffMonitor
	if mon == nil {
		return nil
	}
	states := mon.GetAllStates()
	result := make(map[string]api.MultiPortDiffState, len(states))
	for sym, s := range states {
		result[sym] = api.MultiPortDiffState{
			Symbol:      s.Symbol,
			NetworkDiff: s.NetworkDiff,
			BlockTime:   s.BlockTime,
			Available:   s.Available,
			LastUpdated: s.LastUpdated,
		}
	}
	return result
}
