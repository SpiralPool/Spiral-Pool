// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package pool orchestrates all mining pool components.
//
// This is the main coordinator that ties together stratum server, job manager,
// share pipeline, payment processor, and API server into a unified mining pool.
package pool

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/spiralpool/stratum/internal/api"
	"github.com/spiralpool/stratum/internal/auxpow"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/discovery"
	"github.com/spiralpool/stratum/internal/ha"
	"github.com/spiralpool/stratum/internal/jobs"
	"github.com/spiralpool/stratum/internal/metrics"
	"github.com/spiralpool/stratum/internal/payments"
	"github.com/spiralpool/stratum/internal/shares"
	"github.com/spiralpool/stratum/internal/stratum"
	"github.com/spiralpool/stratum/internal/vardiff"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// Pool is the main mining pool coordinator.
type Pool struct {
	cfg    *config.Config
	logger *zap.SugaredLogger

	// Core components
	stratumServer  *stratum.Server
	jobManager     *jobs.Manager
	sharePipeline  *shares.Pipeline
	shareValidator *shares.ValidatorV2
	vardiffEngine  *vardiff.Engine
	daemonClient   *daemon.Client
	zmqListener    *daemon.ZMQListener
	db             database.Database
	apiServer      *api.Server
	metricsServer  *metrics.Metrics
	coinImpl       coin.Coin // Coin implementation for algorithm-specific validation
	submitTimeouts *daemon.SubmitTimeouts // Coin-aware block submission timeouts

	// Merge mining (AuxPoW) support
	auxManager   *auxpow.Manager   // Optional: manages aux chain templates
	auxSubmitter *auxpow.Submitter // Optional: submits found aux blocks

	// Block write-ahead log for crash recovery
	// Maintains a durable record to prevent block loss
	blockWAL *BlockWAL

	// Dedicated non-sampled block logger
	// Standard zap sampling can drop logs under load - block events are too critical
	blockLogger *BlockLogger

	// Payment processor (optional - enabled when payments.enabled=true)
	paymentProcessor *payments.Processor

	// Aux chain payment processors (V1 FIX: status tracking for merge-mined blocks)
	// Maps auxPoolID (e.g., "btc_sha256_1_nmc") to its payment processor.
	// Without these, aux blocks are recorded in DB but never progress past "pending".
	auxPaymentProcessors map[string]*payments.Processor

	// HA components (optional - initialized when HA/failover is enabled)
	dbManager          *database.DatabaseManager    // Database HA manager
	failoverManager    *discovery.FailoverManager   // Pool failover manager
	vipManager         *ha.VIPManager               // VIP manager for miner failover
	replicationManager *database.ReplicationManager // PostgreSQL replication manager
	slotMonitor        *ha.ReplicationSlotMonitor   // Replication slot WAL retention monitor
	redisDedupTracker  *shares.RedisDedupTracker    // Redis dedup tracker for HA block dedup

	// Session VARDIFF state
	sessionStates     sync.Map // map[uint64]*vardiff.SessionState
	sessionStateCount int64    // V29 FIX: Track count for bounded growth (atomic)

	// Block notification mode
	// Strategy: Start with RPC polling (reliable), promote to ZMQ once stable
	// CRITICAL FIX: Added mutex to prevent race conditions on polling state
	pollingMu     sync.Mutex    // Protects usePolling, zmqPromoted, pollingTicker, pollingStopCh
	usePolling    bool          // true when using RPC polling for block notifications
	zmqPromoted   bool          // true once ZMQ has proven stable and taken over
	pollingTicker *time.Ticker  // polling ticker
	pollingStopCh chan struct{} // channel to stop polling

	// Cached network stats (updated by statsLoop)
	cachedBlockHeight       uint64
	cachedNetworkDifficulty float64
	cachedHashrate          float64
	cachedSharesPerSecond   float64
	cachedNetworkHashrate   float64
	cachedBlocksFound       int64
	cachedBlockReward       float64
	cachedPoolEffort        float64
	lastBlockFoundAt        time.Time
	statsMu                 sync.RWMutex

	// Server start time (for accurate hashrate calculation)
	startTime time.Time

	// Block celebration state (announces to miners when block is found)
	celebrationEndTime   time.Time  // When the celebration ends (default 2 hours after block found)
	celebrationSessionID uint64     // Session ID of the miner who found the block
	celebrationMu        sync.Mutex // Protects celebrationEndTime and celebrationSessionID
	lastCelebrationMsg   time.Time  // Last time we sent a celebration message

	// HA role tracking and in-flight submission cancellation (V2 PARITY FIX).
	// Without these, a V1 backup node could submit blocks post-demotion because
	// in-flight goroutines would continue using their original context.
	// roleCancel() on demotion cancels all in-flight block/aux submissions that
	// use roleCtx as their parent context.
	//
	// AF-1 FIX: haRole uses atomic.Int32 to eliminate the data race between
	// handleRoleChange (writer) and handleBlock/handleAuxBlocks (readers).
	// Stores ha.Role cast to int32. Zero value = ha.RoleUnknown.
	haRole     atomic.Int32 // stores ha.Role as int32; use .Store()/.Load()
	walRecoveryRunning atomic.Bool // re-entrancy guard for recoverWALAfterPromotion
	roleCtx    context.Context
	roleCancel context.CancelFunc
	roleMu     sync.Mutex

	// Shutdown coordination
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New creates a new Pool instance.
func New(cfg *config.Config, logger *zap.Logger) (*Pool, error) {
	log := logger.Sugar()

	// Initialize daemon client
	daemonClient := daemon.NewClient(&cfg.Daemon, logger)

	// Test daemon connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := daemonClient.Ping(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	cancel()
	log.Info("Connected to daemon")

	// CRITICAL STARTUP VALIDATIONS
	// These checks prevent catastrophic operator errors that would cause mining to nowhere

	// 1. Validate pool address against daemon
	log.Infow("Validating pool address...", "address", cfg.Pool.Address)
	addrCtx, addrCancel := context.WithTimeout(context.Background(), 10*time.Second)
	addrValid, err := daemonClient.ValidateAddress(addrCtx, cfg.Pool.Address)
	addrCancel()
	if err != nil {
		return nil, fmt.Errorf("🚨 CRITICAL: Failed to validate pool address against daemon: %w", err)
	}
	if !addrValid {
		return nil, fmt.Errorf("🚨 CRITICAL: Pool address '%s' is INVALID on this daemon - blocks would be rejected! Check your address format and network (mainnet vs testnet)", cfg.Pool.Address)
	}
	log.Infow("✅ Pool address validated successfully", "address", cfg.Pool.Address)

	// 2. Check network (mainnet vs testnet) matches expected configuration
	netCtx, netCancel := context.WithTimeout(context.Background(), 10*time.Second)
	bcInfo, err := daemonClient.GetBlockchainInfo(netCtx)
	netCancel()
	if err != nil {
		return nil, fmt.Errorf("🚨 CRITICAL: Failed to get blockchain info: %w", err)
	}
	log.Infow("Connected to blockchain network",
		"chain", bcInfo.Chain,
		"blocks", bcInfo.Blocks,
		"difficulty", bcInfo.Difficulty,
	)
	// Warn if on testnet (but don't block - some operators intentionally use testnet)
	if bcInfo.Chain != "main" {
		log.Warnw("⚠️ WARNING: NOT running on mainnet - ensure this is intentional!",
			"chain", bcInfo.Chain,
			"note", "Mining on testnet has no real value",
		)
	}

	// V38 FIX: Check daemon version and log for compatibility awareness.
	// Running against an incompatible daemon version causes cryptic RPC errors.
	// We don't block startup, but we log the version for operator awareness.
	netCtx2, netCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	netInfo, netErr := daemonClient.GetNetworkInfo(netCtx2)
	netCancel2()
	if netErr != nil {
		log.Warnw("V38: Could not retrieve daemon version info (non-fatal)",
			"error", netErr,
		)
	} else {
		log.Infow("Daemon version info",
			"version", netInfo.Version,
			"subVersion", netInfo.SubVersion,
			"connections", netInfo.Connections,
		)
		if netInfo.Connections == 0 {
			log.Warnw("⚠️ WARNING: Daemon has ZERO peer connections — blocks may not propagate!",
				"recommendation", "Check daemon network connectivity and peer configuration",
			)
		}
	}

	// V21 FIX: Warn if daemon is running in pruned mode.
	// A pruned node discards old block data, which means deep reorg detection
	// (verifyConfirmedBlocks) cannot verify blocks older than the pruned depth.
	// This is NOT fatal — mining works fine — but historical verification is limited.
	if bcInfo.Pruned {
		log.Warnw("⚠️ WARNING: Daemon is running in PRUNED mode!",
			"impact", "Deep reorg detection cannot verify blocks older than pruned depth",
			"recommendation", "Use a full (non-pruned) node for maximum block-loss protection",
			"note", "Mining and block submission are NOT affected — only historical verification is limited",
		)
	}

	// Initialize database schema (auto-creates tables if needed)
	// This eliminates the need for manual PostgreSQL setup
	migrator := database.NewMigrator(&cfg.Database, cfg.Pool.ID, logger)
	initCtx, initCancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := migrator.Initialize(initCtx); err != nil {
		initCancel()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	initCancel()
	log.Info("Database schema initialized")

	// Determine coin algorithm early for database hashrate calculations
	coinAlgorithm := coin.AlgorithmFromCoinSymbol(cfg.Pool.Coin)

	// Connect to database (HA mode or standard single-node)
	var db database.Database
	var dbMgr *database.DatabaseManager
	if cfg.IsHAEnabled() {
		// HA mode: use DatabaseManager with failover
		nodes := cfg.GetDatabaseNodes()
		dbNodeConfigs := make([]database.DBNodeConfig, len(nodes))
		for i, n := range nodes {
			dbNodeConfigs[i] = database.DBNodeConfig(n)
		}
		var err error
		dbMgr, err = database.NewDatabaseManager(dbNodeConfigs, cfg.Pool.ID, coinAlgorithm, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create HA database manager: %w", err)
		}
		db = dbMgr
		log.Infow("✅ Connected to database (HA mode)",
			"nodes", len(nodes),
			"activeNode", dbMgr.GetActiveNode().ID,
		)
	} else {
		// Standard single-node mode
		singleDB, err := database.NewPostgresDB(&cfg.Database, cfg.Pool.ID, coinAlgorithm, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to database: %w", err)
		}
		db = singleDB
		log.Info("Connected to database")
	}

	// Initialize pool failover manager if enabled
	var failoverMgr *discovery.FailoverManager
	if cfg.Failover.Enabled {
		// Get stratum port from config
		stratumPort := cfg.GetStratumPort()

		// Build backup pool configs
		backupPools := make([]discovery.BackupPoolConfig, 0, len(cfg.Failover.BackupPools))
		for _, bp := range cfg.Failover.BackupPools {
			backupPools = append(backupPools, discovery.BackupPoolConfig{
				ID:       bp.ID,
				Host:     bp.Host,
				Port:     bp.Port,
				Priority: bp.Priority,
				Weight:   bp.Weight,
			})
		}

		failoverCfg := discovery.FailoverConfig{
			PrimaryHost:         cfg.Daemon.Host, // Use daemon host as primary pool host
			PrimaryPort:         stratumPort,
			BackupPools:         backupPools,
			HealthCheckInterval: cfg.Failover.HealthCheckInterval,
			FailoverThreshold:   cfg.Failover.FailoverThreshold,
			RecoveryThreshold:   cfg.Failover.RecoveryThreshold,
		}

		failoverMgr = discovery.NewFailoverManager(failoverCfg, logger)
		log.Infow("✅ Pool failover manager initialized",
			"backupPools", len(backupPools),
			"healthCheckInterval", cfg.Failover.HealthCheckInterval,
		)
	}

	// Initialize components
	stratumServer := stratum.NewServer(&cfg.Stratum, logger)

	// Configure Spiral Router with blockchain's block time for proper share rate targeting
	// This ensures miners submit appropriate number of shares per block
	if coinInfo, err := cfg.GetCoinInfo(); err == nil && coinInfo.BlockTime > 0 {
		stratumServer.SetBlockTime(coinInfo.BlockTime)
	}

	jobManager, err := jobs.NewManager(&cfg.Pool, &cfg.Stratum, daemonClient, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize job manager: %w", err)
	}
	sharePipeline := shares.NewPipeline(&cfg.Database, db, logger)

	// CRITICAL: Create coin implementation for algorithm-specific share validation
	// Without this, Scrypt coins (LTC, DOGE, etc.) would be validated with SHA256d,
	// causing all blocks to be rejected with incorrect hashes.
	coinImpl, err := coin.Create(cfg.Pool.Coin)
	if err != nil {
		return nil, fmt.Errorf("unsupported coin '%s': %w", cfg.Pool.Coin, err)
	}
	log.Infow("Coin implementation loaded",
		"symbol", coinImpl.Symbol(),
		"name", coinImpl.Name(),
		"algorithm", coinImpl.Algorithm(),
	)

	// Configure Spiral Router with coin's mining algorithm for correct difficulty profiles.
	// Scrypt coins (LTC, DOGE, PEP, CAT, DGB-SCRYPT) use ~1000x lower difficulty than SHA-256d.
	// Must be called AFTER SetBlockTime so the correct scaled profiles are active.
	stratumServer.SetAlgorithm(coinImpl.Algorithm())

	// ORPHAN FIX #5: Genesis block verification to prevent mining on wrong network
	// This catches catastrophic misconfiguration where daemon is on wrong chain
	// (e.g., testnet daemon with mainnet config, or wrong coin entirely).
	// Without this check, miner could find valid blocks worth nothing.
	genesisCtx, genesisCancel := context.WithTimeout(context.Background(), 10*time.Second)
	nodeGenesisHash, err := daemonClient.GetBlockHash(genesisCtx, 0)
	genesisCancel()
	if err != nil {
		return nil, fmt.Errorf("🚨 CRITICAL: Failed to fetch genesis block hash from daemon: %w", err)
	}
	if cfg.Pool.SkipGenesisVerify {
		log.Warnw("Genesis block verification SKIPPED (skipGenesisVerify=true) — regtest/testnet mode",
			"coin", coinImpl.Symbol(),
			"daemonGenesis", nodeGenesisHash,
		)
	} else if err := coinImpl.VerifyGenesisBlock(nodeGenesisHash); err != nil {
		return nil, fmt.Errorf("🚨 CRITICAL: Genesis block mismatch - WRONG CHAIN!\n"+
			"  Expected: %s genesis\n"+
			"  Daemon returned: %s\n"+
			"  This usually means:\n"+
			"    1. Daemon is running on wrong network (testnet vs mainnet)\n"+
			"    2. Daemon is running a different coin than configured\n"+
			"    3. Configuration mismatch between pool and daemon\n"+
			"  Error: %w", coinImpl.Symbol(), nodeGenesisHash, err)
	} else {
		// Safe hash preview for logging
		genesisPreview := nodeGenesisHash
		if len(genesisPreview) > 16 {
			genesisPreview = genesisPreview[:16] + "..."
		}
		log.Infow("Genesis block verified - correct chain confirmed",
			"coin", coinImpl.Symbol(),
			"genesisHash", genesisPreview,
		)
	}

	// Use ValidatorV2 with coin-aware hashing (SHA256d or Scrypt based on coin)
	shareValidator := shares.NewValidatorWithCoin(jobManager.GetJob, coinImpl)

	// Create vardiff engine with validation to catch config errors at startup
	// SECURITY: Zero MinDiff would cause division by zero and allow trivial shares
	vardiffEngine, err := vardiff.NewEngineWithValidation(cfg.Stratum.Difficulty.VarDiff)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize vardiff engine: %w", err)
	}
	vardiffEngine.SetAlgorithm(coinImpl.Algorithm())

	// Initialize ZMQ if enabled
	var zmqListener *daemon.ZMQListener
	if cfg.Daemon.ZMQ.Enabled {
		zmqListener = daemon.NewZMQListener(&cfg.Daemon.ZMQ, logger)
	}

	// Initialize VIP manager if enabled
	var vipMgr *ha.VIPManager
	if cfg.IsVIPEnabled() {
		vipCfg := ha.Config{
			Enabled:           true,
			NodeID:            "", // Auto-generated
			Priority:          cfg.VIP.Priority,
			AutoPriority:      cfg.VIP.AutoPriority,
			VIPAddress:        cfg.VIP.Address,
			VIPInterface:      cfg.VIP.Interface,
			VIPNetmask:        cfg.VIP.Netmask,
			DiscoveryPort:     cfg.VIP.DiscoveryPort,
			StatusPort:        cfg.VIP.StatusPort,
			StratumPort:       cfg.GetStratumPort(),
			HeartbeatInterval: cfg.VIP.HeartbeatInterval,
			FailoverTimeout:   cfg.VIP.FailoverTimeout,
			ClusterToken:      cfg.VIP.ClusterToken,
			CanBecomeMaster:   cfg.VIP.CanBecomeMaster,
		}

		var err error
		vipMgr, err = ha.NewVIPManager(vipCfg, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create VIP manager: %w", err)
		}

		// Configure multi-coin port routing
		configPorts := cfg.GetCoinPorts()
		if len(configPorts) > 0 {
			haPorts := make(map[string]*ha.CoinPortInfo)
			for symbol, portInfo := range configPorts {
				haPorts[symbol] = &ha.CoinPortInfo{
					StratumV1:  portInfo.StratumV1,
					StratumV2:  portInfo.StratumV2,
					StratumTLS: portInfo.StratumTLS,
				}
			}
			vipMgr.SetCoinPorts(haPorts)
		}

		// Configure pool payout addresses for HA validation across nodes
		// CRITICAL: All HA nodes MUST have the same payout address or funds will split!
		if cfg.Pool.Address != "" {
			// Use canonical symbol (e.g., "DGB") not raw coin name (e.g., "DIGIBYTE")
			coinKey := strings.ToUpper(cfg.Pool.Coin)
			if info, ok := config.SupportedCoins[strings.ToLower(cfg.Pool.Coin)]; ok {
				coinKey = info.Symbol
			}
			poolAddrs := map[string]string{
				coinKey: cfg.Pool.Address,
			}
			// Include merge-mined aux chain addresses for HA sync
			for _, aux := range cfg.MergeMining.AuxChains {
				if aux.Enabled && aux.Address != "" {
					poolAddrs[strings.ToUpper(aux.Symbol)] = aux.Address
				}
			}
			vipMgr.SetPoolAddresses(poolAddrs)
			log.Infow("Pool address configured for HA validation",
				"coin", cfg.Pool.Coin,
				"address", cfg.Pool.Address,
				"totalAddresses", len(poolAddrs),
			)
		}

		log.Infow("✅ VIP manager initialized",
			"vip", cfg.VIP.Address,
			"interface", cfg.VIP.Interface,
			"priority", cfg.VIP.Priority,
			"coinPorts", len(configPorts),
		)
	}

	// Initialize replication manager if HA is enabled with database failover
	var replMgr *database.ReplicationManager
	if cfg.IsHAEnabled() && dbMgr != nil {
		replCfg := database.DefaultReplicationConfig()
		replCfg.Enabled = true
		replCfg.PostgresPort = cfg.Database.Port

		var err error
		replMgr, err = database.NewReplicationManager(replCfg, dbMgr, logger)
		if err != nil {
			log.Warnw("Failed to create replication manager (continuing without PostgreSQL replication)",
				"error", err,
			)
		} else {
			log.Info("✅ PostgreSQL replication manager initialized")

			// Wire up VIP to replication manager + database manager for automatic DB failover.
			// Chain both: replication handler (PostgreSQL promote/demote via Patroni) and
			// database manager handler (activeNodeIdx switch for connection pool routing).
			if vipMgr != nil {
				replHandler := replMgr.VIPRoleChangeHandler()
				dbHandler := dbMgr.VIPFailoverCallback()
				vipMgr.SetDatabaseFailoverHandler(func(isMaster bool) {
					replHandler(isMaster)
					dbHandler(isMaster)
				})
				log.Info("✅ VIP and database replication wired together for automatic failover")
			}
		}
	}

	// ORPHAN FIX #6: Initialize block write-ahead log for crash recovery
	// WAL directory: ./data/wal/ relative to working directory
	// This ensures block data survives crashes for manual recovery
	walDir := filepath.Join("data", "wal")
	blockWAL, err := NewBlockWAL(walDir, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize block WAL: %w", err)
	}
	log.Infow("✅ Block write-ahead log initialized",
		"walFile", blockWAL.FilePath(),
	)

	// Check for unsubmitted blocks from previous crash
	unsubmittedBlocks, err := RecoverUnsubmittedBlocks(walDir)
	if err != nil {
		log.Warnw("Failed to check for unsubmitted blocks", "error", err)
	} else if len(unsubmittedBlocks) > 0 {
		log.Warnw("⚠️ CRASH RECOVERY: Found unsubmitted blocks from previous session!",
			"count", len(unsubmittedBlocks),
		)

		// P1 AUDIT FIX: Auto-replay WAL blocks on startup
		// Instead of just logging, attempt to verify/resubmit unsubmitted blocks.
		// This prevents block loss from crashes during submission.
		for _, block := range unsubmittedBlocks {
			log.Infow("🔄 WAL RECOVERY: Checking unsubmitted block",
				"height", block.Height,
				"hash", block.BlockHash,
				"miner", block.MinerAddress,
				"timestamp", block.Timestamp,
			)

			// Step 1: Check if block is already in the chain (maybe submission succeeded but status wasn't updated)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			chainHash, err := daemonClient.GetBlockHash(ctx, block.Height)
			cancel()

			if err == nil && chainHash == block.BlockHash {
				// Block is already in chain! Update WAL status
				log.Infow("✅ WAL RECOVERY: Block already accepted by network - no resubmission needed",
					"height", block.Height,
					"hash", block.BlockHash,
				)
				// Update WAL entry to "submitted" status
				block.Status = "submitted"
				block.SubmitError = "recovered_on_startup: block already in chain"
				blockWAL.LogSubmissionResult(&block)

				// V22 FIX: Ensure block exists in database for payment processing.
				// If the process crashed between WAL write and DB InsertBlock, the block
				// is on-chain but has no DB record. Without this, the miner is never paid.
				// InsertBlock is idempotent (checks for existing hash before inserting).
				dbBlock := &database.Block{
					Height:                      block.Height,
					Hash:                        block.BlockHash,
					Miner:                       block.MinerAddress,
					Reward:                      float64(block.CoinbaseValue) / 1e8,
					Status:                      "pending",
					Type:                        "block",
					ConfirmationProgress:        0,
					TransactionConfirmationData: block.BlockHash,
					Created:                     block.Timestamp,
				}
				insertCtx, insertCancel := context.WithTimeout(context.Background(), 10*time.Second)
				if insertErr := db.InsertBlock(insertCtx, dbBlock); insertErr != nil {
					log.Errorw("V22 CRITICAL: Failed to ensure block in database during WAL recovery",
						"height", block.Height,
						"hash", block.BlockHash,
						"error", insertErr,
					)
				} else {
					log.Infow("V22 FIX: Ensured block record exists in database",
						"height", block.Height,
						"hash", block.BlockHash,
						"miner", block.MinerAddress,
					)
				}
				insertCancel()
				continue
			}

			// Step 2: Block not in chain at expected height - check if it's a valid resubmission candidate
			// Only attempt resubmission if the block is recent (within last 100 blocks)
			ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
			chainInfo, infoErr := daemonClient.GetBlockchainInfo(ctx2)
			cancel2()

			if infoErr == nil {
				currentHeight := chainInfo.Blocks
				blockAge := currentHeight - block.Height

				if blockAge > 100 {
					// Block is too old - chain has moved on, mark as stale
					log.Warnw("⚠️ WAL RECOVERY: Block too old for resubmission - marking as stale",
						"height", block.Height,
						"hash", block.BlockHash,
						"blockAge", blockAge,
						"currentHeight", currentHeight,
					)
					block.Status = "rejected"
					block.SubmitError = "recovered_stale: block too old (chain moved " + fmt.Sprintf("%d", blockAge) + " blocks ahead)"
					blockWAL.LogSubmissionResult(&block)
					continue
				}

				// Block is recent enough - attempt resubmission
				if block.BlockHex != "" {
					log.Infow("🚀 WAL RECOVERY: Attempting resubmission of recent block",
						"height", block.Height,
						"hash", block.BlockHash,
						"blockAge", blockAge,
					)

					ctx3, cancel3 := context.WithTimeout(context.Background(), 60*time.Second)
					submitErr := daemonClient.SubmitBlock(ctx3, block.BlockHex)
					cancel3()

					if submitErr == nil {
						log.Infow("✅ WAL RECOVERY: Block resubmitted successfully!",
							"height", block.Height,
							"hash", block.BlockHash,
						)
						block.Status = "submitted"
						block.SubmitError = "recovered_resubmitted_on_startup"

						// V22 FIX: Ensure resubmitted block exists in database.
						dbBlock := &database.Block{
							Height:                      block.Height,
							Hash:                        block.BlockHash,
							Miner:                       block.MinerAddress,
							Reward:                      float64(block.CoinbaseValue) / 1e8,
							Status:                      "pending",
							Type:                        "block",
							ConfirmationProgress:        0,
							TransactionConfirmationData: block.BlockHash,
							Created:                     block.Timestamp,
						}
						insertCtx2, insertCancel2 := context.WithTimeout(context.Background(), 10*time.Second)
						if insertErr := db.InsertBlock(insertCtx2, dbBlock); insertErr != nil {
							log.Errorw("V22 CRITICAL: Failed to ensure resubmitted block in database",
								"height", block.Height,
								"hash", block.BlockHash,
								"error", insertErr,
							)
						} else {
							log.Infow("V22 FIX: Ensured resubmitted block exists in database",
								"height", block.Height,
								"hash", block.BlockHash,
								"miner", block.MinerAddress,
							)
						}
						insertCancel2()
					} else {
						log.Warnw("⚠️ WAL RECOVERY: Block resubmission failed",
							"height", block.Height,
							"hash", block.BlockHash,
							"error", submitErr,
						)
						block.Status = "rejected"
						block.SubmitError = "recovered_resubmit_failed: " + submitErr.Error()
					}
					blockWAL.LogSubmissionResult(&block)

				} else {
					log.Warnw("⚠️ WAL RECOVERY: Block hex missing - cannot resubmit",
						"height", block.Height,
						"hash", block.BlockHash,
					)
					block.Status = "rejected"
					block.SubmitError = "recovered_no_hex: block data not available for resubmission"
					blockWAL.LogSubmissionResult(&block)
				}
			}
		}
	}

	// V25 FIX: WAL-DB reconciliation at startup.
	// Scan WAL entries with terminal success status ("submitted"/"accepted") and
	// ensure each has a corresponding database record. This catches the edge case
	// where a block was submitted to the daemon and WAL was updated, but the DB
	// insert failed (e.g., due to a transient PostgreSQL error or process crash
	// between WAL write and DB insert). InsertBlock is idempotent — if the record
	// already exists, it's a no-op.
	submittedBlocks, reconcileErr := RecoverSubmittedBlocks(walDir)
	if reconcileErr != nil {
		log.Warnw("V25: Failed to read WAL for reconciliation", "error", reconcileErr)
	} else if len(submittedBlocks) > 0 {
		reconciledCount := 0
		for _, block := range submittedBlocks {
			dbBlock := &database.Block{
				Height:                      block.Height,
				Hash:                        block.BlockHash,
				Miner:                       block.MinerAddress,
				Reward:                      float64(block.CoinbaseValue) / 1e8,
				Status:                      "pending",
				Type:                        "block",
				ConfirmationProgress:        0,
				TransactionConfirmationData: block.BlockHash,
				Created:                     block.Timestamp,
			}
			rCtx, rCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if insertErr := db.InsertBlock(rCtx, dbBlock); insertErr != nil {
				log.Errorw("V25 CRITICAL: Failed to reconcile WAL entry with database",
					"height", block.Height,
					"hash", block.BlockHash,
					"error", insertErr,
				)
			} else {
				reconciledCount++
			}
			rCancel()
		}
		log.Infow("V25 FIX: WAL-DB reconciliation complete",
			"submittedEntries", len(submittedBlocks),
			"reconciled", reconciledCount,
		)
	}

	// WAL RETENTION: Clean up old WAL files after recovery is complete.
	// Both RecoverUnsubmittedBlocks and RecoverSubmittedBlocks have already
	// processed all entries, so old files can be safely removed.
	if cleaned, cleanErr := CleanupOldWALFiles(walDir, DefaultWALRetentionDays); cleanErr != nil {
		log.Warnw("WAL cleanup failed", "error", cleanErr)
	} else if cleaned > 0 {
		log.Infow("WAL cleanup complete", "filesRemoved", cleaned, "retentionDays", DefaultWALRetentionDays)
	}

	// ORPHAN FIX #7: Initialize dedicated non-sampled block logger
	// This ensures block events are NEVER dropped due to log sampling
	logDir := filepath.Join("data", "logs")
	blockLogger, err := NewBlockLogger(logDir)
	if err != nil {
		log.Warnw("Failed to initialize dedicated block logger - falling back to standard logger",
			"error", err,
		)
		// Continue without block logger - not fatal, but reduces forensics capability
	} else {
		log.Infow("✅ Dedicated block logger initialized (no sampling)",
			"logFile", blockLogger.FilePath(),
		)
	}

	// V2 PARITY FIX: Initialize role-scoped context for HA in-flight cancellation.
	// This context is the parent for all block/aux submission goroutines. On
	// demotion, roleCancel() cancels it, aborting any in-flight submissions.
	roleCtx, roleCancel := context.WithCancel(context.Background())

	pool := &Pool{
		cfg:                cfg,
		logger:             log,
		stratumServer:      stratumServer,
		jobManager:         jobManager,
		sharePipeline:      sharePipeline,
		shareValidator:     shareValidator,
		vardiffEngine:      vardiffEngine,
		daemonClient:       daemonClient,
		zmqListener:        zmqListener,
		db:                 db,
		dbManager:          dbMgr,
		failoverManager:    failoverMgr,
		vipManager:         vipMgr,
		replicationManager: replMgr,
		coinImpl:           coinImpl,
		submitTimeouts:     daemon.NewSubmitTimeouts(coinImpl.BlockTime()),
		blockWAL:           blockWAL,
		blockLogger:        blockLogger,
		roleCtx:            roleCtx,
		roleCancel:         roleCancel,
	}

	// Default haRole to RoleMaster (standalone mode — this node IS the master).
	// When HA is configured, the vipManager block below overrides to RoleBackup.
	// Without this default, the M7 block submission gate (!= RoleMaster) blocks
	// submission in standalone mode where haRole stays at 0 (RoleUnknown).
	pool.haRole.Store(int32(ha.RoleMaster))

	log.Infow("Block submission timeouts computed from block time",
		"coin", coinImpl.Symbol(),
		"blockTimeSec", coinImpl.BlockTime(),
		"submitTimeout", pool.submitTimeouts.SubmitTimeout,
		"verifyTimeout", pool.submitTimeouts.VerifyTimeout,
		"preciousTimeout", pool.submitTimeouts.PreciousTimeout,
		"totalBudget", pool.submitTimeouts.TotalBudget,
		"retryTimeout", pool.submitTimeouts.RetryTimeout,
		"maxRetries", pool.submitTimeouts.MaxRetries,
		"submitDeadline", pool.submitTimeouts.SubmitDeadline,
	)

	// ═══════════════════════════════════════════════════════════════════════════
	// MERGE MINING (AuxPoW) AUTO-INITIALIZATION
	// ═══════════════════════════════════════════════════════════════════════════
	// When merge mining is configured, automatically set up aux chain connections
	// and wire everything together. No manual intervention needed.
	// ═══════════════════════════════════════════════════════════════════════════
	if cfg.IsMergeMiningEnabled() {
		log.Info("Initializing merge mining (AuxPoW) support...")

		auxConfigs := make([]auxpow.AuxChainConfig, 0)
		for _, auxCfg := range cfg.GetEnabledAuxChains() {
			// Create aux chain coin implementation
			auxCoinBase, err := coin.Create(auxCfg.Symbol)
			if err != nil {
				return nil, fmt.Errorf("failed to create aux coin %s: %w", auxCfg.Symbol, err)
			}
			// Verify it implements AuxPowCoin interface
			auxCoin, ok := auxCoinBase.(coin.AuxPowCoin)
			if !ok {
				return nil, fmt.Errorf("coin %s does not support AuxPoW (doesn't implement AuxPowCoin interface)", auxCfg.Symbol)
			}

			// Create daemon client for aux chain
			auxDaemonCfg := &config.DaemonConfig{
				Host:     auxCfg.Daemon.Host,
				Port:     auxCfg.Daemon.Port,
				User:     auxCfg.Daemon.User,
				Password: auxCfg.Daemon.Password,
			}
			auxDaemonClient := daemon.NewClient(auxDaemonCfg, logger)

			// Test connection to aux daemon
			auxCtx, auxCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := auxDaemonClient.Ping(auxCtx); err != nil {
				auxCancel()
				return nil, fmt.Errorf("failed to connect to aux daemon for %s: %w", auxCfg.Symbol, err)
			}
			auxCancel()
			log.Infow("✅ Connected to aux chain daemon",
				"symbol", auxCfg.Symbol,
				"host", auxCfg.Daemon.Host,
				"chainID", auxCoin.ChainID(),
			)

			// V40 FIX: Reject if aux chain uses same payout address as parent chain.
			// Coins with identical address formats (BC2/BTC, FBTC/BTC) can accept
			// addresses from either chain. If the same address is configured for both,
			// payments for different coins may go to the wrong chain wallet.
			if auxCfg.Address == cfg.Pool.Address {
				return nil, fmt.Errorf("🚨 V40: Aux chain %s uses the SAME payout address as parent chain (%s) — "+
					"this will cause cross-chain payment confusion. "+
					"Generate a separate address on the %s chain for aux mining rewards",
					auxCfg.Symbol, auxCfg.Address, auxCfg.Symbol)
			}

			auxConfigs = append(auxConfigs, auxpow.AuxChainConfig{
				Symbol:       auxCfg.Symbol,
				Enabled:      auxCfg.Enabled,
				Coin:         auxCoin,
				DaemonClient: auxDaemonClient,
				Address:      auxCfg.Address,
			})
		}

		// Create AuxPoW manager
		auxManager, err := auxpow.NewManager(coinImpl, auxConfigs, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create AuxPoW manager: %w", err)
		}

		// Wire up aux manager to pool (this also wires it to validator)
		pool.SetAuxManager(auxManager)

		// Wire aux manager to job manager for template generation
		jobManager.SetAuxManager(auxManager)

		log.Infow("✅ Merge mining initialized successfully",
			"auxChainCount", auxManager.AuxChainCount(),
			"parentAlgorithm", coinImpl.Algorithm(),
		)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PAYMENT PROCESSOR INITIALIZATION
	// ═══════════════════════════════════════════════════════════════════════════
	// Create and wire the payment processor when payments are enabled.
	// SetPaymentProcessor handles HA wiring: advisory locks for split-brain
	// prevention, and master-only processing when VIP is enabled.
	// ═══════════════════════════════════════════════════════════════════════════
	if cfg.Payments.Enabled {
		// Both PostgresDB and DatabaseManager implement BlockStore (via delegation)
		blockStore, ok := db.(payments.BlockStore)
		if !ok {
			return nil, fmt.Errorf("database does not implement BlockStore interface for payment processing")
		}
		proc := payments.NewProcessor(&cfg.Payments, &cfg.Pool, blockStore, daemonClient, logger)
		if pool.metricsServer != nil {
			proc.SetMetrics(pool.metricsServer)
		}
		pool.SetPaymentProcessor(proc)
		log.Infow("✅ Payment processor initialized",
			"scheme", cfg.Payments.Scheme,
			"interval", cfg.Payments.Interval,
			"haEnabled", pool.vipManager != nil,
			"advisoryLock", pool.dbManager != nil || db != nil,
		)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// AUX CHAIN PAYMENT PROCESSOR INITIALIZATION (V1 MERGE MINING FIX)
	// ═══════════════════════════════════════════════════════════════════════════
	// V1 Gap Fix: Create per-aux-chain payment processors so merge-mined blocks
	// get proper status tracking (pending → confirmed → orphaned). Without these,
	// aux blocks are recorded in DB but never progress past "pending" status.
	// Mirrors the V2 coordinator pattern (coordinator.go:590-637).
	// ═══════════════════════════════════════════════════════════════════════════
	if cfg.Payments.Enabled && cfg.MergeMining.Enabled && pool.auxManager != nil {
		pool.auxPaymentProcessors = make(map[string]*payments.Processor)
		for _, auxCfg := range pool.auxManager.GetAuxChainConfigs() {
			if !auxCfg.Enabled || auxCfg.DaemonClient == nil {
				continue
			}

			auxPoolID := cfg.Pool.ID + "_" + strings.ToLower(auxCfg.Symbol)

			// Use parent config's blockMaturity if set, otherwise aux coin's default
			auxMaturity := auxCfg.Coin.CoinbaseMaturity()
			if cfg.Payments.BlockMaturity > 0 {
				auxMaturity = cfg.Payments.BlockMaturity
			}

			auxPaymentsCfg := &config.PaymentsConfig{
				Enabled:        true,
				Scheme:         cfg.Payments.Scheme,
				MinimumPayment: cfg.Payments.MinimumPayment,
				BlockTime:      auxCfg.Coin.BlockTime(),
				BlockMaturity:  auxMaturity,
				Interval:       cfg.Payments.Interval,
			}
			auxPoolCfg := &config.PoolConfig{
				ID:      auxPoolID,
				Coin:    auxCfg.Symbol,
				Address: auxCfg.Address,
			}

			// Get scoped DB for aux pool ID (table: blocks_{auxPoolID})
			var auxBlockStore payments.BlockStore
			if pdb, ok := db.(*database.PostgresDB); ok {
				auxBlockStore = pdb.WithPoolID(auxPoolID)
			} else if dbMgr != nil {
				if activeDB := dbMgr.GetActiveDB(); activeDB != nil {
					auxBlockStore = activeDB.WithPoolID(auxPoolID)
				}
			}
			if auxBlockStore == nil {
				log.Warnw("Cannot create aux payment processor: no scoped DB available",
					"auxPoolId", auxPoolID,
				)
				continue
			}

			auxProc := payments.NewProcessor(auxPaymentsCfg, auxPoolCfg, auxBlockStore, auxCfg.DaemonClient, logger)
			if pool.metricsServer != nil {
				auxProc.SetMetrics(pool.metricsServer)
			}
			if pool.vipManager != nil {
				auxProc.SetHAEnabled(true)
				// BUG FIX (M9): Default isMaster to false when HA configured.
				auxProc.SetMasterRole(false)
			}
			if pool.dbManager != nil {
				auxProc.SetAdvisoryLocker(pool.dbManager)
			} else if postgresDB, ok := db.(*database.PostgresDB); ok {
				auxProc.SetAdvisoryLocker(postgresDB)
			}

			pool.auxPaymentProcessors[auxPoolID] = auxProc
			log.Infow("✅ Aux chain payment processor initialized",
				"auxPoolId", auxPoolID,
				"auxSymbol", auxCfg.Symbol,
				"scheme", auxPaymentsCfg.Scheme,
				"blockMaturity", auxMaturity,
			)
		}
		if len(pool.auxPaymentProcessors) > 0 {
			log.Infow("V1 aux payment processors ready",
				"count", len(pool.auxPaymentProcessors),
			)
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// REDIS DEDUP TRACKER INITIALIZATION
	// ═══════════════════════════════════════════════════════════════════════════
	// Create Redis dedup tracker for cross-node block submission deduplication.
	// Activation is gated by REDIS_DEDUP_ENABLED=true environment variable.
	// When Redis is unavailable, falls back to local in-memory tracking.
	// ═══════════════════════════════════════════════════════════════════════════
	redisDedupCfg := shares.DefaultRedisDedupConfig()
	redisDedupTracker := shares.NewRedisDedupTracker(redisDedupCfg, log)
	if redisDedupTracker != nil {
		pool.SetRedisDedupTracker(redisDedupTracker)
	}

	// Wire up callbacks
	pool.setupCallbacks()

	// Initialize API server if enabled
	if cfg.API.Enabled {
		// Get PostgresDB for API server (works with both single-node and HA mode)
		var postgresDB *database.PostgresDB
		if dbMgr != nil {
			// HA mode - get the active node's DB
			postgresDB = dbMgr.GetActiveDB()
		} else if pdb, ok := db.(*database.PostgresDB); ok {
			// Single-node mode
			postgresDB = pdb
		}

		if postgresDB != nil {
			apiServer := api.NewServer(&cfg.API, &cfg.Pool, postgresDB, logger)
			apiServer.SetStatsProvider(pool)
			apiServer.SetStratumConfig(&cfg.Stratum)
			apiServer.SetRouterProvider(pool)
			apiServer.SetPipelineProvider(pool)
			apiServer.SetPaymentProvider(pool)
			apiServer.SetConnectionProvider(pool)
			apiServer.SetMergeMiningConfig(&cfg.MergeMining)
			if dbMgr != nil {
				apiServer.SetDatabaseManager(dbMgr)
			}
			if failoverMgr != nil {
				apiServer.SetFailoverManager(failoverMgr)
			}
			pool.apiServer = apiServer
			log.Infow("API server initialized", "listen", cfg.API.Listen)
		} else {
			log.Warn("API server not initialized: could not get PostgresDB reference")
		}
	}

	// Initialize Prometheus metrics server if enabled
	if cfg.Metrics.Enabled {
		pool.metricsServer = metrics.New(&cfg.Metrics, logger)
		log.Infow("Metrics server initialized", "listen", cfg.Metrics.Listen)

		// V26 FIX: Wire database status guard rejections to Prometheus metric.
		// This callback is invoked by postgres.go when a V12 status guard blocks
		// a block status update, making silent rejections observable and alertable.
		ms := pool.metricsServer // capture for closure safety
		database.SetOnStatusGuardRejection(func() {
			ms.BlockStatusGuardRejections.Inc()
		})

		// BUG FIX: Wire metrics to payment processor if it was initialized before
		// the metrics server (initialization order dependency).
		if pool.paymentProcessor != nil {
			pool.paymentProcessor.SetMetrics(pool.metricsServer)
		}
	}

	return pool, nil
}

// setupCallbacks wires up event handlers between components.
func (p *Pool) setupCallbacks() {
	// Job manager broadcasts to stratum server
	p.jobManager.SetJobCallback(func(job *protocol.Job) {
		p.stratumServer.BroadcastJob(job)
	})

	// Stratum server sends shares to pool for processing
	p.stratumServer.SetShareHandler(p.handleShare)

	// Track new connections - create placeholder vardiff state
	// This will be replaced with proper per-miner state when classified
	// CRITICAL: Use NewSessionStateWithProfile with reasonable MaxDiff cap instead of
	// NewSessionState which falls back to engine default (1 trillion MaxDiff).
	// This prevents runaway difficulty if MinerClassifiedHandler isn't called.
	p.stratumServer.SetConnectHandler(func(session *protocol.Session) {
		// V29 FIX: Cap sessionStates growth to prevent OOM under sustained connection churn.
		// 100k sessions is generous (each ~200 bytes = ~20MB max). Beyond this, something
		// is wrong (botnet, pool-hopping storm) and we log but still accept the connection.
		const MaxSessionStates = 100000
		count := atomic.LoadInt64(&p.sessionStateCount)
		if count >= MaxSessionStates {
			p.logger.Warnw("⚠️ V29: sessionStates at capacity — possible connection churn attack",
				"count", count,
				"max", MaxSessionStates,
				"sessionId", session.ID,
			)
		}

		// Use reasonable defaults: MinDiff=0.001 (lottery), MaxDiff=150000 (low-class cap)
		// This is safe because MinerClassifiedHandler will replace with proper values
		initialDiff := p.cfg.Stratum.Difficulty.Initial
		minDiff := 0.001
		maxDiff := 150000.0

		// CRITICAL FIX: Use Spiral Router's scaled target time based on blockchain's block time
		// GetDefaultTargetTime() returns the scaled value from MinerClassLow profile
		// Automatically scales for ANY coin: DGB(15s)->3s, DOGE(60s)->5s, LTC(150s)->5s, BTC(600s)->5s
		targetTime := p.stratumServer.GetDefaultTargetTime()

		state := p.vardiffEngine.NewSessionStateWithProfile(
			initialDiff,
			minDiff,
			maxDiff,
			targetTime,
		)
		p.sessionStates.Store(session.ID, state)
		atomic.AddInt64(&p.sessionStateCount, 1)

		// TRACE: Log initial vardiff state (BEFORE miner classification)
		p.logger.Infow("VARDIFF initial state (pre-classification)",
			"sessionId", session.ID,
			"initialDiff", initialDiff,
			"minDiff", minDiff,
			"maxDiff", maxDiff,
			"targetTime", targetTime,
			"note", "Will be replaced by MinerClassifiedHandler after authorize",
		)
	})

	// Cleanup on disconnect
	p.stratumServer.SetDisconnectHandler(func(session *protocol.Session) {
		// FIX G-1: Use LoadAndDelete to only decrement counter if state existed.
		// Sessions rejected at MaxSessionStates never had state stored, so
		// unconditional decrement caused counter drift (going negative over time,
		// eventually making the capacity guard permanently ineffective).
		if _, existed := p.sessionStates.LoadAndDelete(session.ID); existed {
			atomic.AddInt64(&p.sessionStateCount, -1)
		}
		// Update metrics for worker disconnect (independent of vardiff state)
		if p.metricsServer != nil && session.MinerAddress != "" {
			p.metricsServer.RecordWorkerDisconnection(session.MinerAddress, session.WorkerName)
		}
	})

	// CRITICAL: Create per-miner vardiff state when Spiral Router classifies the miner
	// This provides per-session TargetShareTime, MinDiff, MaxDiff based on miner class
	// An ESP32 miner (lottery) gets 60s target, while S21 (pro) gets 2s target
	p.stratumServer.SetMinerClassifiedHandler(func(sessionID uint64, profile stratum.MinerProfile) {
		// DEFENSIVE: Ensure MaxDiff is valid - if 0, something is wrong with profile lookup
		// This prevents falling back to engine default (1 trillion) which causes runaway difficulty
		maxDiff := profile.MaxDiff
		if maxDiff <= 0 {
			p.logger.Errorw("SAFEGUARD: Profile MaxDiff is zero or negative, using class default",
				"sessionId", sessionID,
				"class", profile.Class.String(),
				"profileMaxDiff", profile.MaxDiff,
			)
			// Use reasonable defaults based on class to prevent 1 trillion fallback
			// Values match DefaultProfiles in spiralrouter.go
			switch profile.Class.String() {
			case "lottery":
				maxDiff = 100
			case "low":
				maxDiff = 150000
			case "mid":
				maxDiff = 50000
			case "high":
				maxDiff = 100000
			case "pro":
				maxDiff = 500000
			// Avalon-specific classes
			case "avalon_nano":
				maxDiff = 500
			case "avalon_legacy_low":
				maxDiff = 3000
			case "avalon_legacy_mid":
				maxDiff = 6000
			case "avalon_mid":
				maxDiff = 25000
			case "avalon_high":
				maxDiff = 40000
			case "avalon_pro":
				maxDiff = 60000
			case "avalon_home":
				maxDiff = 30000
			default:
				maxDiff = 50000 // Safe default for unknown
			}
		}

		// Create new vardiff state with miner-specific settings
		state := p.vardiffEngine.NewSessionStateWithProfile(
			profile.InitialDiff,
			profile.MinDiff,
			maxDiff,
			float64(profile.TargetShareTime), // Convert int seconds to float64
		)
		p.sessionStates.Store(sessionID, state)
		p.logger.Infow("VARDIFF configured with miner profile",
			"sessionId", sessionID,
			"class", profile.Class.String(),
			"initialDiff", profile.InitialDiff,
			"minDiff", profile.MinDiff,
			"maxDiff", maxDiff,
			"targetShareTime", profile.TargetShareTime,
		)
		// Update metrics for worker connection (miner is now fully authorized and classified)
		if p.metricsServer != nil {
			if session, ok := p.stratumServer.GetSession(sessionID); ok {
				p.metricsServer.RecordWorkerConnection(session.MinerAddress, session.WorkerName)
			}
		}
	})

	// Legacy handler for backward compatibility (sync difficulty if profile handler not triggered)
	p.stratumServer.SetDifficultyChangeHandler(func(sessionID uint64, difficulty float64) {
		if stateVal, ok := p.sessionStates.Load(sessionID); ok {
			state := stateVal.(*vardiff.SessionState)
			vardiff.SetDifficulty(state, difficulty)
		}
	})

	// ZMQ block notifications trigger job refresh
	// FIX D-6: Pass the block hash to enable immediate same-height reorg detection
	if p.zmqListener != nil {
		p.zmqListener.SetBlockHandler(func(blockHash []byte) {
			// Use 20 second timeout for block notifications
			// Increased from 10s to handle daemon load during rapid block succession
			// on multi-algo coins like DGB where blocks can come every 3 seconds
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// Convert block hash to hex string for AdvanceWithTip
			tipHash := ""
			if len(blockHash) > 0 {
				tipHash = fmt.Sprintf("%x", blockHash)
			}
			p.jobManager.OnBlockNotificationWithHash(ctx, tipHash)
		})

		// ZMQ status change logging and metrics (promotion/fallback managed by zmqPromotionLoop)
		p.zmqListener.SetStatusChangeHandler(func(status daemon.ZMQStatus) {
			p.logger.Infow("ZMQ status update",
				"status", status.String(),
			)
			// Update Prometheus metric for Sentinel monitoring
			if p.metricsServer != nil {
				p.metricsServer.SetZMQHealthStatus(int(status))
			}
		})

		// Note: Fallback handler not used - promotion logic in zmqPromotionLoop handles
		// switching between RPC polling and ZMQ based on stability monitoring
	}
}

// handleShare processes a submitted share.
func (p *Pool) handleShare(share *protocol.Share) *protocol.ShareResult {
	// Get session VARDIFF state
	stateVal, ok := p.sessionStates.Load(share.SessionID)
	if !ok {
		return &protocol.ShareResult{
			Accepted:     false,
			RejectReason: "session-not-found",
		}
	}
	state := stateVal.(*vardiff.SessionState)

	// NOTE: share.Difficulty is already set by the handler from session.GetDifficulty()
	// This represents the difficulty the miner was TOLD to work at (last sent via mining.set_difficulty)
	// Do NOT override with vardiff.GetDifficulty(state) as that may be a newer internal value
	// that hasn't been sent to the miner yet - causing valid shares to be rejected as low-difficulty

	// Validate share using coin-specific algorithm (SHA256d for BTC/DGB, Scrypt for LTC/DOGE)
	result := p.shareValidator.ValidateWithCoin(share)

	// Record share to Prometheus metrics
	// For silent duplicates: record as duplicate in metrics (not accepted) for monitoring,
	// but we'll still return Accepted=true to the miner to prevent retry floods.
	if p.metricsServer != nil {
		if result.SilentDuplicate {
			// Track as rejected duplicate for monitoring purposes
			p.metricsServer.RecordShare(false, result.RejectReason)
		} else {
			p.metricsServer.RecordShare(result.Accepted, result.RejectReason)
			// Track best share difficulty using actual hash difficulty (not assigned difficulty)
			// This shows the true "best" share based on how hard the hash was to find
			if result.Accepted && result.ActualDifficulty > 0 {
				p.metricsServer.UpdateBestShareDiff(result.ActualDifficulty)
			}
		}
	}

	// Skip processing for silent duplicates - we told miner it's accepted but don't credit
	if result.SilentDuplicate {
		// Trace logging for silent duplicate handling
		p.logger.Debugw("silent_duplicate_trace",
			"sessionId", share.SessionID,
			"jobId", share.JobID,
			"nonce", share.Nonce,
			"note", "Duplicate share silently accepted but NOT credited",
		)
		return result
	}

	if result.Accepted {
		// CRITICAL: Check for block FIRST, before ANY I/O operations
		// Block submission is the most time-sensitive operation - every millisecond counts!
		if result.IsBlock {
			p.handleBlock(share, result)
		}

		// Submit to pipeline for DB persistence (after block handling)
		p.sharePipeline.Submit(share)

		// Update VARDIFF
		// CRITICAL: Only run vardiff when enabled in config. When disabled (e.g. regtest),
		// keep fixed initial difficulty. Without this guard, difficulty ramps to impossible
		// levels for CPU miners even though config says varDiff: enabled: false.
		if p.cfg.Stratum.Difficulty.VarDiff.Enabled {
		stats := p.vardiffEngine.GetStats(state)

		// Trace logging for hashrate estimation
		estimatedHashrate := p.vardiffEngine.EstimateSessionHashrate(state)
		p.logger.Debugw("hashrate_trace",
			"sessionId", share.SessionID,
			"assignedDiff", share.Difficulty,
			"actualDiff", result.ActualDifficulty,
			"targetTime", state.TargetTime(),
			"estimatedHashrateGHs", estimatedHashrate/1e9,
			"totalShares", stats.TotalShares,
			"userAgent", share.UserAgent,
		)
		var newDiff float64
		var changed bool

		// Get shares since last retarget (NOT total shares) for accurate rate calculation.
		// Uses window-based counting to ensure elapsedSec matches share count period.
		sharesSinceRetarget := state.SharesSinceRetarget()

		// Check if we need aggressive retargeting:
		// 1. During initial ramp-up (first 10 shares), OR
		// 2. When share rate is way off target (>2x deviation)
		// Aggressive mode is limited to early session to prevent oscillation.
		needsAggressive := stats.TotalShares <= 10 || p.vardiffEngine.ShouldAggressiveRetarget(state)

		if needsAggressive && sharesSinceRetarget >= 2 {
			elapsedSec := time.Since(stats.LastRetargetTime).Seconds()

			// MINER-SPECIFIC COOLDOWN: cgminer-based miners (Avalon) need longer cooldown
			// because cgminer doesn't apply new difficulty to work-in-progress.
			// It can take 10-30+ seconds before cgminer starts using new difficulty.
			// - cgminer/Avalon: 30 second cooldown (allows difficulty to actually apply)
			// - Other miners (NMAxe, NerdQAxe++, BFGMiner): 5 second cooldown
			// Uses configurable patterns via SpiralRouter.IsSlowDiffApplier()
			minRetargetInterval := 5.0
			if p.stratumServer.IsSlowDiffApplier(share.UserAgent) {
				minRetargetInterval = 30.0
			}

			// EXPONENTIAL BACKOFF: If difficulty is already optimal (consecutive no-changes),
			// increase cooldown to prevent rapid retarget attempts. This stabilizes sessions
			// that have converged to their optimal difficulty.
			// Backoff multiplier: 1x, 2x, 3x, 4x (capped at 4x = 120s for cgminer, 20s for others)
			backoffCount := state.ConsecutiveNoChange()
			if backoffCount > 0 {
				backoffMultiplier := float64(backoffCount + 1)
				if backoffMultiplier > 4.0 {
					backoffMultiplier = 4.0
				}
				minRetargetInterval *= backoffMultiplier
			}

			if elapsedSec > minRetargetInterval {
				oldDiff := stats.CurrentDifficulty
				newDiff, changed = p.vardiffEngine.AggressiveRetarget(state, sharesSinceRetarget, elapsedSec)
				if changed {
					p.logger.Infow("VARDIFF retarget",
						"sessionId", share.SessionID,
						"totalShares", stats.TotalShares,
						"sharesSinceRetarget", sharesSinceRetarget,
						"elapsedSec", elapsedSec,
						"oldDiff", oldDiff,
						"newDiff", newDiff,
						"factor", newDiff/oldDiff,
						"sessionMaxDiff", state.MaxDiff(),
						"sessionMinDiff", state.MinDiff(),
						"sessionTargetTime", state.TargetTime(),
					)
				}
			}
		}

		// Normal vardiff when not in aggressive mode
		if !changed {
			newDiff, changed = p.vardiffEngine.RecordShare(state)
			if changed {
				p.logger.Debugw("VARDIFF adjusted",
					"sessionId", share.SessionID,
					"newDiff", newDiff,
				)
			}
		}

		// Send new difficulty to miner (block already handled at start of Accepted block)
		if changed {
			if session, ok := p.stratumServer.GetSession(share.SessionID); ok {
				if err := p.stratumServer.SendDifficulty(session, newDiff); err != nil {
					p.logger.Warnw("Failed to send difficulty update",
						"sessionId", share.SessionID,
						"newDiff", newDiff,
						"error", err,
					)
				}
			}
		}
		} // end if varDiff.Enabled
	}

	// ══════════════════════════════════════════════════════════════════════════════
	// MERGE MINING: Check for aux blocks OUTSIDE of Accepted check
	// ══════════════════════════════════════════════════════════════════════════════
	// CRITICAL: Aux blocks must be processed even when the parent share is REJECTED.
	// A share may not meet parent chain's minimum difficulty but still meet an aux
	// chain's (typically lower) difficulty. This is the fundamental benefit of
	// merge mining - we can earn rewards on aux chains even when shares are too
	// weak for the parent chain.
	// ══════════════════════════════════════════════════════════════════════════════
	if len(result.AuxResults) > 0 {
		p.handleAuxBlocks(share, result.AuxResults)
	}

	return result
}

// handleBlock processes a found block.
// CRITICAL: Block submission is TIME-SENSITIVE. Every millisecond of delay
// increases the chance of another miner finding a block first.
// We submit FIRST, then do logging/WAL AFTER.
func (p *Pool) handleBlock(share *protocol.Share, result *protocol.ShareResult) {
	foundTime := time.Now()

	// SUBMIT BLOCK IMMEDIATELY - BEFORE ANY LOGGING OR WAL WRITES
	// This is the most time-critical operation in the entire pool
	blockStatus := "pending"
	var lastErr error
	var submitTime time.Time

	// STALE RACE CHECK: Re-verify job state before submission.
	// Between share validation and now, a ZMQ notification may have invalidated
	// this job (another miner found a block). Submitting a block with a stale
	// prevBlockHash guarantees a "prev-blk-not-found" rejection from the daemon.
	job, jobFound := p.jobManager.GetJob(share.JobID)
	jobState := protocol.JobStateActive // default if job not found
	if jobFound {
		jobState = job.GetState()
	}
	if jobState == protocol.JobStateInvalidated {
		blockStatus = "orphaned"
		lastErr = fmt.Errorf("job invalidated before submission (stale race)")
		submitTime = time.Now()
		p.logger.Warnw("⚠️ Block found but job invalidated before submission (stale race)",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"jobId", share.JobID,
			"jobAge", result.JobAge,
			"miner", share.MinerAddress,
			"worker", share.WorkerName,
		)
		if p.metricsServer != nil {
			p.metricsServer.BlockStaleRace.Inc()
			p.metricsServer.BlockRejectionsByReason.WithLabelValues("stale_race").Inc()
		}
	} else if jobState == protocol.JobStateSolved {
		// RACE CONDITION: Multiple shares from the same job met the network target.
		// The first was submitted; this is a duplicate. Submitting again wastes an RPC call.
		blockStatus = "orphaned"
		lastErr = fmt.Errorf("job already solved (duplicate block candidate)")
		submitTime = time.Now()
		p.logger.Warnw("RACE: Block found but job already solved (duplicate block candidate)",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"jobId", share.JobID,
			"jobState", jobState.String(),
			"miner", share.MinerAddress,
			"worker", share.WorkerName,
		)
		if p.metricsServer != nil {
			p.metricsServer.BlockRejectionsByReason.WithLabelValues("duplicate_candidate").Inc()
		}
	} else if jobFound && job.RawPrevBlockHash != "" && job.RawPrevBlockHash != p.jobManager.GetLastBlockHash() {
		// DIRECT STALE CHECK: Compare the job's prev block hash against the chain tip.
		// This catches stale jobs even when ZMQ invalidation hasn't propagated yet.
		// Zero latency — reads in-memory value already updated by template refresh.
		blockStatus = "orphaned"
		lastErr = fmt.Errorf("job prev hash doesn't match current chain tip (stale)")
		submitTime = time.Now()
		p.logger.Warnw("⚠️ Block found but chain tip has moved (direct stale check)",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"jobId", share.JobID,
			"jobAge", result.JobAge,
			"jobPrevHash", job.RawPrevBlockHash,
			"currentTip", p.jobManager.GetLastBlockHash(),
			"miner", share.MinerAddress,
			"worker", share.WorkerName,
		)
		if p.metricsServer != nil {
			p.metricsServer.BlockStaleRace.Inc()
			p.metricsServer.BlockRejectionsByReason.WithLabelValues("chain_tip_moved").Inc()
		}
	} else if result.BlockHex == "" {
		// CRITICAL RECOVERY: Block was solved but serialization failed in validator.
		// Attempt in-situ rebuild from raw job/share data before giving up.
		p.logger.Errorw("🚨 Block found but BlockHex is empty - attempting recovery rebuild!",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"buildError", result.BlockBuildError,
		)

		rebuilt := false
		if jobFound && job != nil {
			rebuildHex, rebuildErr := shares.RebuildBlockHex(job, share)
			if rebuildErr == nil && rebuildHex != "" {
				p.logger.Infow("✅ Block recovery rebuild succeeded!",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
				)
				result.BlockHex = rebuildHex
				rebuilt = true
			} else {
				p.logger.Errorw("🚨 Block recovery rebuild ALSO FAILED",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"rebuildError", rebuildErr,
					"originalError", result.BlockBuildError,
				)
			}
		}

		if !rebuilt {
			// LAST RESORT: Write emergency WAL entry with ALL raw components
			// so an operator can manually reconstruct and submit via bitcoin-cli.
			blockStatus = "orphaned"
			if p.metricsServer != nil {
				p.metricsServer.BlockRejectionsByReason.WithLabelValues("build_failed").Inc()
			}

			if p.blockWAL != nil {
				emergencyEntry := &BlockWALEntry{
					Height:        share.BlockHeight,
					BlockHash:     result.BlockHash,
					PrevHash:      result.PrevBlockHash,
					MinerAddress:  share.MinerAddress,
					WorkerName:    share.WorkerName,
					JobID:         share.JobID,
					JobAge:        result.JobAge,
					CoinbaseValue: result.CoinbaseValue,
					Status:        "build_failed",
					SubmitError:   result.BlockBuildError,
				}

				// Populate raw reconstruction components from job
				if jobFound && job != nil {
					emergencyEntry.CoinBase1 = job.CoinBase1
					emergencyEntry.CoinBase2 = job.CoinBase2
					emergencyEntry.Version = job.Version
					emergencyEntry.NBits = job.NBits
					emergencyEntry.NTime = share.NTime
					emergencyEntry.Nonce = share.Nonce
					emergencyEntry.TransactionData = job.TransactionData
				}
				emergencyEntry.ExtraNonce1 = share.ExtraNonce1
				emergencyEntry.ExtraNonce2 = share.ExtraNonce2

				if walErr := p.blockWAL.LogBlockFound(emergencyEntry); walErr != nil {
					p.logger.Errorw("🚨🚨 CRITICAL: Failed to write emergency WAL entry for unserialized block!",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"walError", walErr,
					)
				} else {
					p.logger.Errorw("📝 Emergency WAL entry written with raw block components for manual reconstruction",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"walFile", p.blockWAL.FilePath(),
					)
				}
			}
		}
	}

	// If recovery rebuild succeeded, fall through to normal submission
	if result.BlockHex != "" && blockStatus == "pending" {
		// P0 AUDIT FIX: Write WAL entry BEFORE submission to close the crash-loss window.
		// Previously, the WAL write happened AFTER SubmitBlockWithVerification(). If the
		// process was OOM-killed during the RPC call, the block hex existed only in memory
		// and the WAL had no record — a P0 block loss scenario.
		//
		// Now we write a "submitting" entry before the RPC call. On crash recovery,
		// RecoverUnsubmittedBlocks() treats "submitting" like "pending" and attempts
		// resubmission. The post-submission WAL write (below) appends a final status
		// entry, which takes precedence in the recovery map (latest entry per hash wins).
		if p.blockWAL != nil {
			preSubmitEntry := &BlockWALEntry{
				Height:        share.BlockHeight,
				BlockHash:     result.BlockHash,
				PrevHash:      result.PrevBlockHash,
				BlockHex:      result.BlockHex,
				MinerAddress:  share.MinerAddress,
				WorkerName:    share.WorkerName,
				JobID:         share.JobID,
				JobAge:        result.JobAge,
				CoinbaseValue: result.CoinbaseValue,
				Status:        "submitting",
			}
			if walErr := p.blockWAL.LogBlockFound(preSubmitEntry); walErr != nil {
				p.logger.Errorw("Failed to write pre-submission WAL entry (continuing with submission)",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"error", walErr,
				)
				// Continue anyway — submission is more important than WAL durability.
				// The post-submission WAL write will still attempt to record the final state.
			}
		}

		// HA ROLE GATE: Backup nodes must not submit blocks to the daemon (V2 PARITY FIX).
		// The master node handles all block submissions. Without this explicit check,
		// a backup node could issue duplicate RPC calls during VIP transitions when a
		// miner's TCP connection persists to the old master. WAL and DB entries are still
		// recorded above for crash recovery if this node is later promoted.
		// BUG FIX (M7): Check != RoleMaster instead of == RoleBackup.
		// RoleObserver (and RoleUnknown) must also be blocked from submitting.
		if ha.Role(p.haRole.Load()) != ha.RoleMaster {
			p.logger.Warnw("Skipping block submission (not master node)",
				"height", share.BlockHeight,
				"hash", result.BlockHash,
			)
			// BUG FIX (P0 BLOCK LOSS): Must use "pending", NOT "submitted".
			// "submitted" causes RecoverUnsubmittedBlocks to SKIP this block after
			// HA failover — it only recovers "pending"/"submitting"/"aux_submitting".
			// If the master crashes before submitting, the promoted backup's WAL has
			// "submitted" for a block nobody actually submitted → block lost forever.
			// coinpool.go already fixed this (see coinpool.go:1180-1184).
			blockStatus = "pending" // Trust master, but recoverable if master crashes
			submitTime = time.Now()
			if jobFound && job != nil {
				job.SetState(protocol.JobStateSolved, "block found on backup node — master will submit")
			}
			goto blockLogging
		}

		// HA: Cross-node block submission dedup via Redis SETNX.
		// In an HA cluster, multiple nodes may find the same block simultaneously.
		// Redis SETNX ensures only the first node to claim the block hash submits it,
		// preventing duplicate daemon RPC calls. Failures fall through to normal submission
		// (the daemon also rejects duplicates natively, so this is defense-in-depth).
		if p.redisDedupTracker != nil && p.redisDedupTracker.IsEnabled() {
			redisClient := p.redisDedupTracker.Client()
			if redisClient != nil {
				dedupKey := fmt.Sprintf("spiralpool:%s:block_submit:%s", p.cfg.Pool.ID, result.BlockHash)
				dedupCtx, dedupCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				claimed, dedupErr := redisClient.SetNX(dedupCtx, dedupKey, p.cfg.Pool.ID, 5*time.Minute).Result()
				dedupCancel()
				if dedupErr == nil && !claimed {
					// Another node already claimed this block submission
					p.logger.Infow("Block already being submitted by another HA node (skipping duplicate RPC)",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
					)
					// BUG FIX (P0 BLOCK LOSS): Must use "pending", NOT "submitted".
					// Same WAL recovery issue as the backup gate above — "submitted"
					// entries are skipped by RecoverUnsubmittedBlocks. If the peer
					// node crashes before submitting, this block is unrecoverable.
					blockStatus = "pending" // Trust peer, but recoverable if peer crashes
					submitTime = time.Now()
					if jobFound && job != nil {
						job.SetState(protocol.JobStateSolved, "block submitted by peer HA node")
					}
					goto blockLogging
				}
				// On Redis error or successful claim, proceed with normal submission
			}
		}

		// SUBMIT IMMEDIATELY — unified submit + verify + preciousblock
		// Coin-aware timeouts ensure the entire submit cycle fits within one block period.
		// HeightContext cancels automatically if the chain tip advances (new block found),
		// preventing stale RPC calls on a guaranteed-rejected block.
		// V2 PARITY FIX: Use roleCtx as parent so HA demotion cancels in-flight submissions.
		baseCtx := p.snapshotRoleCtx()
		heightCtx, heightCancel := p.jobManager.HeightContext(baseCtx)
		submitCtx, submitCancel := context.WithTimeout(heightCtx, p.submitTimeouts.TotalBudget)
		sr := p.daemonClient.SubmitBlockWithVerification(submitCtx, result.BlockHex, result.BlockHash, share.BlockHeight, p.submitTimeouts)
		submitCancel()
		heightCancel()
		submitTime = time.Now()

		// Record submission latency metric
		if p.metricsServer != nil {
			p.metricsServer.BlockSubmitLatency.Observe(float64(submitTime.Sub(foundTime).Milliseconds()))
		}

		if sr.Submitted || sr.Verified {
			blockStatus = "submitted"
			if p.metricsServer != nil {
				p.metricsServer.BlocksSubmitted.Inc()
			}
			// Transition job to Solved (terminal state) to prevent duplicate block
			// submissions if another share from the same job also meets the network target.
			if jobFound && job != nil {
				job.SetState(protocol.JobStateSolved, "block submitted successfully")
			}

			// Track false rejections (submit failed but chain verification passed)
			if sr.Verified && !sr.Submitted && p.metricsServer != nil {
				p.metricsServer.BlocksFalseRejection.Inc()
			}

			logFields := []interface{}{
				"height", share.BlockHeight,
				"hash", result.BlockHash,
				"miner", share.MinerAddress,
				"worker", share.WorkerName,
				"submissionLatencyMs", submitTime.Sub(foundTime).Milliseconds(),
				"totalLatencyMs", submitTime.Sub(share.SubmittedAt).Milliseconds(),
				"reward", float64(result.CoinbaseValue) / 1e8,
				"submitOk", sr.Submitted,
				"verified", sr.Verified,
				"chainHash", sr.ChainHash,
			}

			if sr.Submitted && sr.Verified {
				p.logger.Infow("✅ Block submitted and verified in chain!", logFields...)
			} else if sr.Verified && !sr.Submitted {
				p.logger.Infow("✅ Block accepted despite submission error (verified in chain)!",
					append(logFields, "submitError", sr.SubmitErr)...)
			} else {
				p.logger.Infow("✅ Block submitted (awaiting chain verification)", logFields...)
			}

			if sr.PreciousErr != nil {
				p.logger.Debugw("preciousblock hint failed (non-critical)", "error", sr.PreciousErr)
			}
		} else {
			lastErr = sr.SubmitErr
			blockStatus = "rejected"
			// Classify rejection reason for metrics
			if p.metricsServer != nil {
				if sr.SubmitErr != nil {
					p.metricsServer.BlockRejectionsByReason.WithLabelValues(classifyRejectionMetric(sr.SubmitErr.Error())).Inc()
				} else {
					p.metricsServer.BlockRejectionsByReason.WithLabelValues("unknown").Inc()
				}
			}
			p.logger.Warnw("Block submission failed and not verified in chain",
				"height", share.BlockHeight,
				"hash", result.BlockHash,
				"submitError", sr.SubmitErr,
				"verifyError", sr.VerifyErr,
				"chainHash", sr.ChainHash,
				"submissionLatencyMs", submitTime.Sub(foundTime).Milliseconds(),
				"jobAge", result.JobAge,
				"jobId", share.JobID,
			)
		}
	}

blockLogging:
	// NOW do all the logging and WAL writes AFTER submission attempt.
	// Differentiate accepted blocks from candidates that were rejected/orphaned
	// to prevent false positive alerts in log monitoring systems.
	if blockStatus == "pending" || blockStatus == "submitted" {
		p.logger.Infow("🎉 BLOCK FOUND!",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"miner", share.MinerAddress,
			"worker", share.WorkerName,
			"jobId", share.JobID,
			"status", blockStatus,
			"shareSubmittedAt", share.SubmittedAt.Format(time.RFC3339Nano),
			"blockDetectedAt", foundTime.Format(time.RFC3339Nano),
			"networkDiff", share.NetworkDiff,
			"shareDiff", share.Difficulty,
		)

		// AUDIT FIX (ISSUE-1): Increment BlocksFound metric for every block detection.
		if p.metricsServer != nil {
			p.metricsServer.RecordBlock()
		}

		// Update API stats: increment blocks found, reset effort timer
		p.statsMu.Lock()
		p.cachedBlocksFound++
		p.lastBlockFoundAt = time.Now()
		p.cachedPoolEffort = 0
		p.statsMu.Unlock()

		// Block logger — only log accepted blocks as "found"
		if p.blockLogger != nil {
			p.blockLogger.LogBlockFound(
				share.BlockHeight,
				result.BlockHash,
				share.MinerAddress,
				share.WorkerName,
				share.JobID,
				share.NetworkDiff,
				share.Difficulty,
				float64(result.CoinbaseValue)/1e8,
			)
		}
	} else {
		p.logger.Infow("Block candidate not accepted",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"miner", share.MinerAddress,
			"worker", share.WorkerName,
			"jobId", share.JobID,
			"status", blockStatus,
			"reason", lastErr,
			"jobAge", result.JobAge,
		)
	}

	// WAL write (post-submission) — appends final status entry for this block.
	// This updates the pre-submission "submitting" entry written above.
	// RecoverUnsubmittedBlocks uses the latest entry per BlockHash, so this
	// final status takes precedence over the "submitting" entry in the recovery map.
	walEntry := &BlockWALEntry{
		Height:        share.BlockHeight,
		BlockHash:     result.BlockHash,
		PrevHash:      result.PrevBlockHash,
		BlockHex:      result.BlockHex,
		MinerAddress:  share.MinerAddress,
		WorkerName:    share.WorkerName,
		JobID:         share.JobID,
		JobAge:        result.JobAge,
		CoinbaseValue: result.CoinbaseValue,
		Status:        blockStatus,
	}
	if p.blockWAL != nil {
		if err := p.blockWAL.LogBlockFound(walEntry); err != nil {
			p.logger.Errorw("Failed to write block to WAL",
				"height", share.BlockHeight,
				"hash", result.BlockHash,
				"error", err,
			)
		}
	}

	// Handle submission result
	if blockStatus == "submitted" {
		blockStatus = "pending" // Awaiting confirmations
		// Log successful submission
		if p.blockLogger != nil {
			p.blockLogger.LogBlockSubmitted(
				share.BlockHeight,
				result.BlockHash,
				share.MinerAddress,
				1,
				submitTime.Sub(share.SubmittedAt).Milliseconds(),
			)
		}
		// Update WAL
		if p.blockWAL != nil {
			walEntry.Status = "submitted"
			p.blockWAL.LogSubmissionResult(walEntry)
		}
	} else if blockStatus == "rejected" && lastErr != nil {
		// Check if error is permanent (stale, invalid) - NO RETRIES for permanent errors
		errStr := lastErr.Error()
		if isPermanentRejection(errStr) {
			// DUPLICATE/ALREADY SAFETY CHECK: "duplicate" and "already" mean the daemon
			// already has this block in its block index. The block may be in the active
			// chain — the inline getblockhash verification in SubmitBlockWithVerification
			// may have failed due to timeout or cs_main contention. Without this check,
			// a valid accepted block could be permanently marked orphaned with no recovery
			// path (DB status guard blocks orphaned→pending, confirmation checker only
			// queries pending blocks, WAL recovery only processes pending/submitting).
			// verifyBlockAcceptance uses context.Background() with a 30s window — immune
			// to all context cancellation — giving the daemon time to respond.
			errLower := strings.ToLower(errStr)
			isDuplicate := strings.Contains(errLower, "duplicate") || strings.Contains(errLower, "already")
			if isDuplicate && p.verifyBlockAcceptance(result.BlockHash, share.BlockHeight) {
				// Block IS in the active chain — treat as success.
				blockStatus = "pending"
				if p.metricsServer != nil {
					p.metricsServer.BlocksSubmitted.Inc()
				}
				p.logger.Infow("✅ Block verified in chain despite daemon 'duplicate' response",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"daemonResponse", errStr,
				)
				if jobFound && job != nil {
					job.SetState(protocol.JobStateSolved, "block verified in chain (daemon returned duplicate)")
				}
				if p.blockWAL != nil {
					walEntry.Status = "submitted"
					walEntry.SubmitError = "duplicate_verified_in_chain"
					p.blockWAL.LogSubmissionResult(walEntry)
				}
				if p.blockLogger != nil {
					p.blockLogger.LogBlockSubmitted(
						share.BlockHeight,
						result.BlockHash,
						share.MinerAddress,
						1,
						time.Since(share.SubmittedAt).Milliseconds(),
					)
				}
			} else {
				// Block was stale, invalid, or duplicate-but-not-in-active-chain.
				// Retrying won't help.
				rejectionReason := classifyRejection(errStr)
				blockStatus = "orphaned"
				if p.metricsServer != nil {
					p.metricsServer.BlockRejectionsByReason.WithLabelValues(classifyRejectionMetric(errStr)).Inc()
				}

				if isDuplicate {
					p.logger.Warnw("Block 'duplicate' in daemon but NOT in active chain — orphaning",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"rejectionType", rejectionReason,
						"orphanReason", "duplicate_not_in_active_chain",
					)
				} else {
					p.logger.Errorw("🚫 BLOCK REJECTED (permanent - no retry)",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"error", lastErr,
						"rejectionType", rejectionReason,
						"orphanReason", "permanent_rejection",
						"jobAgeMs", result.JobAge.Milliseconds(),
						"submissionLatencyMs", submitTime.Sub(foundTime).Milliseconds(),
						"prevBlockHash", result.PrevBlockHash,
					)
				}

				// Update WAL
				if p.blockWAL != nil {
					walEntry.Status = "rejected"
					walEntry.RejectReason = rejectionReason
					walEntry.SubmitError = errStr
					p.blockWAL.LogSubmissionResult(walEntry)
				}

				// Log to block logger
				if p.blockLogger != nil {
					p.blockLogger.LogBlockRejected(
						share.BlockHeight,
						result.BlockHash,
						share.MinerAddress,
						rejectionReason,
						errStr,
						result.PrevBlockHash,
						result.JobAge.Milliseconds(),
					)
				}
			}
		} else {
			// Transient error (network issue) - retry with deadline-driven loop.
			// The loop runs until EITHER:
			//   1. SubmitDeadline expires (BlockTime × 0.30)
			//   2. Height advances (new block found — HeightContext cancels)
			//   3. MaxRetries safety cap reached
			// HeightContext ensures retries abort immediately if chain tip moves.
			retryStartTime := time.Now()
			// V2 PARITY FIX: Use roleCtx as parent so demotion cancels retry loop.
			retryBaseCtx := p.snapshotRoleCtx()
			retryHeightCtx, retryHeightCancel := p.jobManager.HeightContext(retryBaseCtx)
			deadlineCtx, deadlineCancel := context.WithTimeout(retryHeightCtx, p.submitTimeouts.SubmitDeadline)
			maxRetries := p.submitTimeouts.MaxRetries
			attempt := 2
			for deadlineCtx.Err() == nil && attempt <= maxRetries+1 {
				if p.metricsServer != nil {
					p.metricsServer.BlockSubmitRetries.Inc()
				}
				time.Sleep(p.submitTimeouts.RetrySleep)

				if deadlineCtx.Err() != nil {
					break // deadline or height change during sleep
				}

				retryCtx, retryCancel := context.WithTimeout(deadlineCtx, p.submitTimeouts.RetryTimeout)
				sr := p.daemonClient.SubmitBlockWithVerification(retryCtx, result.BlockHex, result.BlockHash, share.BlockHeight, p.submitTimeouts)
				retryCancel()

				if sr.Submitted || sr.Verified {
					blockStatus = "pending"
					if jobFound && job != nil {
						job.SetState(protocol.JobStateSolved, "block submitted on retry")
					}
					p.logger.Infow("✅ Block submitted on retry!",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"attempt", attempt,
						"submitOk", sr.Submitted,
						"verified", sr.Verified,
					)
					if p.blockWAL != nil {
						walEntry.Status = "submitted"
						p.blockWAL.LogSubmissionResult(walEntry)
					}
					if p.blockLogger != nil {
						p.blockLogger.LogBlockSubmitted(
							share.BlockHeight,
							result.BlockHash,
							share.MinerAddress,
							attempt,
							time.Since(share.SubmittedAt).Milliseconds(),
						)
					}
					break
				}

				lastErr = sr.SubmitErr
				if lastErr != nil && isPermanentRejection(lastErr.Error()) {
					// DUPLICATE/ALREADY SAFETY CHECK (retry path): Same protection as
					// the initial submission path. "duplicate"/"already" means the daemon
					// has the block — verify it's in the active chain before orphaning.
					retryErrLower := strings.ToLower(lastErr.Error())
					isRetryDuplicate := strings.Contains(retryErrLower, "duplicate") || strings.Contains(retryErrLower, "already")
					if isRetryDuplicate && p.verifyBlockAcceptance(result.BlockHash, share.BlockHeight) {
						blockStatus = "pending"
						if p.metricsServer != nil {
							p.metricsServer.BlocksSubmitted.Inc()
						}
						p.logger.Infow("✅ Block verified in chain on retry despite 'duplicate' response",
							"height", share.BlockHeight,
							"hash", result.BlockHash,
							"attempt", attempt,
						)
						if jobFound && job != nil {
							job.SetState(protocol.JobStateSolved, "block verified in chain on retry (daemon returned duplicate)")
						}
						if p.blockWAL != nil {
							walEntry.Status = "submitted"
							walEntry.SubmitError = "duplicate_verified_in_chain_on_retry"
							p.blockWAL.LogSubmissionResult(walEntry)
						}
						if p.blockLogger != nil {
							p.blockLogger.LogBlockSubmitted(
								share.BlockHeight,
								result.BlockHash,
								share.MinerAddress,
								attempt,
								time.Since(share.SubmittedAt).Milliseconds(),
							)
						}
						break
					}
					blockStatus = "orphaned"
					if p.metricsServer != nil {
						p.metricsServer.BlockRejectionsByReason.WithLabelValues(classifyRejectionMetric(lastErr.Error())).Inc()
					}
					retryRejectionReason := classifyRejection(lastErr.Error())
					retryOrphanReason := "permanent_rejection"
					if isRetryDuplicate {
						retryOrphanReason = "duplicate_not_in_active_chain"
						p.logger.Warnw("Block 'duplicate' on retry but NOT in active chain — orphaning",
							"height", share.BlockHeight,
							"hash", result.BlockHash,
							"attempt", attempt,
							"orphanReason", retryOrphanReason,
						)
					} else {
						p.logger.Errorw("🚫 BLOCK REJECTED on retry (permanent)",
							"height", share.BlockHeight,
							"hash", result.BlockHash,
							"error", lastErr,
							"attempt", attempt,
							"chainHash", sr.ChainHash,
							"orphanReason", retryOrphanReason,
						)
					}
					// AUDIT FIX: Write WAL and BlockLogger here with the correct rejection
					// reason. Previously this deferred to the final fallback block, which
					// unconditionally used "retry_exhausted_and_unverified" — wrong for
					// permanent rejections that were definitively classified.
					if p.blockWAL != nil {
						walEntry.Status = "rejected"
						walEntry.RejectReason = retryRejectionReason
						walEntry.SubmitError = lastErr.Error()
						p.blockWAL.LogSubmissionResult(walEntry)
					}
					if p.blockLogger != nil {
						p.blockLogger.LogBlockRejected(
							share.BlockHeight,
							result.BlockHash,
							share.MinerAddress,
							retryRejectionReason,
							lastErr.Error(),
							result.PrevBlockHash,
							result.JobAge.Milliseconds(),
						)
					}
					break
				}
				attempt++
			}
			deadlineCancel()
			retryHeightCancel()

			// Classify abort reason if loop exited due to context cancellation.
			// V2 PARITY FIX: Do NOT set blockStatus to "orphaned" here — the post-timeout
			// verification below must run first. If the daemon accepted the block but the
			// RPC response was lost due to timeout, prematurely orphaning would be a false
			// negative. The "Final fallback" block below handles the orphaned assignment
			// if post-timeout verification also fails.
			if blockStatus != "pending" && blockStatus != "orphaned" {
				if deadlineCtx.Err() != nil {
					// Deadline, height change, or HA demotion cancelled the context —
					// log but do NOT orphan yet (post-timeout verification runs below).
					elapsed := time.Since(retryStartTime)
					deadlineUsage := float64(elapsed) / float64(p.submitTimeouts.SubmitDeadline)
					if p.metricsServer != nil {
						p.metricsServer.BlockDeadlineUsage.Observe(deadlineUsage)
					}

					// Classify the abort cause. Order matters: check roleCtx (demotion) FIRST
					// because HA demotion cancels the parent, which propagates to all children.
					// Without this check, demotion is misclassified as "height advanced" since
					// retryHeightCtx.Err() is also non-nil when its parent is cancelled.
					roleDemoted := retryBaseCtx.Err() != nil
					abortReason := "submit deadline expired"
					if roleDemoted {
						abortReason = "HA demotion (roleCtx cancelled)"
						if p.metricsServer != nil {
							p.metricsServer.BlockDeadlineAborts.Inc()
						}
					} else if retryHeightCtx.Err() != nil && deadlineCtx.Err() == context.Canceled {
						abortReason = "height advanced (new block found)"
						if p.metricsServer != nil {
							p.metricsServer.BlockHeightAborts.Inc()
						}
					} else {
						if p.metricsServer != nil {
							p.metricsServer.BlockDeadlineAborts.Inc()
						}
					}
					p.logger.Warnw("Block submission aborted: "+abortReason,
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"attempts", attempt-1,
						"deadline", p.submitTimeouts.SubmitDeadline,
						"deadlineUsage", deadlineUsage,
						"elapsedMs", elapsed.Milliseconds(),
						"abortReason", abortReason,
						"roleDemoted", roleDemoted,
					)
				}
			}

			// If still failed after retries/deadline, attempt post-timeout verification
			// before giving up. The daemon may have accepted the block but the RPC
			// response was lost due to timeout. Without this check, we would
			// permanently mark an accepted block as orphaned (V2 PARITY FIX).
			if blockStatus != "pending" && blockStatus != "orphaned" {
				if p.verifyBlockAcceptance(result.BlockHash, share.BlockHeight) {
					p.logger.Infow("Post-timeout verification: block found in chain — marking pending",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
					)
					blockStatus = "pending"
					if jobFound && job != nil {
						job.SetState(protocol.JobStateSolved, "block verified in chain after timeout")
					}
					if p.blockWAL != nil {
						walEntry.Status = "submitted"
						p.blockWAL.LogSubmissionResult(walEntry)
					}
					if p.blockLogger != nil {
						p.blockLogger.LogBlockSubmitted(
							share.BlockHeight,
							result.BlockHash,
							share.MinerAddress,
							attempt,
							time.Since(share.SubmittedAt).Milliseconds(),
						)
					}
				}
			}

			// Final fallback: if still not pending after all attempts + verification.
			// AUDIT FIX: Also skip if already "orphaned" — the retry-loop permanent
			// rejection path writes WAL/BlockLogger with the correct reason (e.g.
			// "permanent_rejection" or "duplicate_not_in_active_chain"). Without this
			// guard, the fallback would overwrite those with "retry_exhausted_and_unverified".
			if blockStatus != "pending" && blockStatus != "orphaned" {
				blockStatus = "orphaned"
				p.logger.Warnw("Block orphaned after all retries + post-timeout verification failed",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"lastError", lastErr,
					"orphanReason", "retry_exhausted_and_unverified",
				)
				// AUDIT FIX (ISSUE-2): Increment BlocksSubmissionFailed metric.
				if p.metricsServer != nil {
					p.metricsServer.RecordBlockSubmissionFailed()
				}
				if p.blockWAL != nil {
					walEntry.Status = "failed"
					walEntry.SubmitError = fmt.Sprintf("retry_exhausted_and_unverified: %v", lastErr)
					p.blockWAL.LogSubmissionResult(walEntry)
				}
				if p.blockLogger != nil {
					p.blockLogger.LogBlockOrphaned(
						share.BlockHeight,
						result.BlockHash,
						share.MinerAddress,
						"retry_exhausted_and_unverified",
					)
				}
			}
		}
	}

	// Record block in database
	// Convert satoshis to coin units (e.g., BTC/DGB) for storage
	rewardCoins := float64(result.CoinbaseValue) / 1e8

	// P1 AUDIT FIX: Verify block is actually in blockchain BEFORE recording
	// This prevents false "pending" status for blocks that were never accepted.
	// Double-check the chain state right before DB insert for maximum accuracy.
	if blockStatus == "pending" {
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
		chainHash, err := p.daemonClient.GetBlockHash(verifyCtx, share.BlockHeight)
		verifyCancel()
		if err != nil || chainHash != result.BlockHash {
			p.logger.Warnw("P1 AUDIT: Pre-DB verification failed - block may not be in chain",
				"height", share.BlockHeight,
				"ourHash", result.BlockHash,
				"chainHash", chainHash,
				"error", err,
			)
			// Don't change status yet - let the block confirmation checker handle it
			// This just logs the discrepancy for debugging
		} else {
			p.logger.Debugw("P1 AUDIT: Pre-DB verification passed - block confirmed in chain",
				"height", share.BlockHeight,
				"hash", result.BlockHash,
			)
		}
	}

	block := &database.Block{
		Height:            share.BlockHeight,
		NetworkDifficulty: share.NetworkDiff,
		Status:            blockStatus,
		Type:              "block",
		Miner:             share.MinerAddress,
		Source:            share.WorkerName,
		Reward:            rewardCoins,
		Hash:              result.BlockHash,
		Created:           time.Now(),
	}

	// Use a fresh context for DB insert - handleBlock is called synchronously from share handler
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()
	if err := p.db.InsertBlock(dbCtx, block); err != nil {
		p.logger.Errorw("CRITICAL: Failed to record block in database — payment at risk",
			"height", block.Height,
			"hash", block.Hash,
			"miner", block.Miner,
			"reward", block.Reward,
			"status", block.Status,
			"error", err,
		)
		// Retry once after short delay — this block is on-chain, losing the DB record means no payment
		time.Sleep(2 * time.Second)
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if retryErr := p.db.InsertBlock(retryCtx, block); retryErr != nil {
			p.logger.Errorw("CRITICAL: Block DB insert retry FAILED — block on-chain but NOT in payment pipeline. Manual reconciliation required.",
				"height", block.Height,
				"hash", block.Hash,
				"miner", block.Miner,
				"reward", block.Reward,
				"error", retryErr,
			)
		} else {
			p.logger.Infow("Block DB insert retry succeeded", "height", block.Height, "hash", block.Hash)
		}
		retryCancel()
	}

	// Only log reward and celebrate if block was actually accepted by the daemon.
	// Orphaned/rejected blocks must NOT trigger celebration or reward messaging.
	if blockStatus == "pending" {
		p.logger.Infow("Block reward will be sent to configured pool address",
			"note", "SOLO mining - reward goes directly to your wallet",
		)
		p.startCelebration(share.SessionID, share.BlockHeight, share.MinerAddress, share.WorkerName, rewardCoins, p.cfg.Pool.Coin)
	}
}

// startCelebration sends a block found message to all miners when a block is found.
// The finder gets a direct message, ALL miners get a broadcast, and Avalon LEDs are triggered.
func (p *Pool) startCelebration(sessionID uint64, height uint64, miner, worker string, reward float64, coinSymbol string) {
	// Check if celebration is enabled in config
	if !p.cfg.Celebration.Enabled {
		p.logger.Debugw("Celebration disabled, skipping block announcement", "height", height)
		return
	}

	// Calculate celebration duration from config (default 2 hours)
	durationHours := p.cfg.Celebration.DurationHours
	if durationHours <= 0 {
		durationHours = 2
	}

	p.celebrationMu.Lock()
	p.celebrationEndTime = time.Now().Add(time.Duration(durationHours) * time.Hour)
	p.celebrationSessionID = sessionID
	endTime := p.celebrationEndTime
	p.celebrationMu.Unlock()

	msg := fmt.Sprintf("BLOCK FOUND! %s Height %d by %s - Reward: %.8f - CELEBRATING!", coinSymbol, height, worker, reward)

	if p.stratumServer != nil {
		// Send to the miner who found the block
		p.stratumServer.SendMessageToSession(sessionID, msg)

		// Broadcast to ALL miners for visual awareness on displays
		p.stratumServer.BroadcastMessage(msg)
	}

	// Trigger full RGB LED celebration via block-celebrate.sh
	// The script handles Avalon LED discovery, 12-phase color sequences, and state restore
	go func(hours int) {
		scriptPath := "/spiralpool/scripts/block-celebrate.sh"
		if _, err := os.Stat(scriptPath); err != nil {
			p.logger.Debugw("Celebration script not found, skipping LED celebration", "path", scriptPath)
			return
		}
		durationSecs := strconv.Itoa(hours * 3600)
		cmd := exec.Command(scriptPath, "--duration", durationSecs)
		if err := cmd.Start(); err != nil {
			p.logger.Warnw("Failed to start celebration script", "error", err)
			return
		}
		p.logger.Infow("LED celebration script started",
			"pid", cmd.Process.Pid,
			"coin", coinSymbol,
			"height", height,
			"durationSecs", durationSecs,
		)
		// Wait in background to reap child process (prevents zombie)
		if err := cmd.Wait(); err != nil {
			p.logger.Debugw("Celebration script exited with error", "error", err)
		}
	}(durationHours)

	p.logger.Infow("Block celebration started",
		"coin", coinSymbol,
		"sessionId", sessionID,
		"height", height,
		"miner", miner,
		"worker", worker,
		"durationHours", durationHours,
		"celebrationEnds", endTime.Format(time.RFC3339),
	)
}

// celebrationLoop periodically sends celebration messages while in celebration mode.
// Sends to the finder and optionally all miners in extravagant mode.
func (p *Pool) celebrationLoop(ctx context.Context) {
	defer p.wg.Done()

	// Skip if celebration is disabled
	if !p.cfg.Celebration.Enabled {
		return
	}

	ticker := time.NewTicker(5 * time.Minute) // Send celebration message every 5 minutes
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.celebrationMu.Lock()
			endTime := p.celebrationEndTime
			sessionID := p.celebrationSessionID
			p.celebrationMu.Unlock()

			if time.Now().Before(endTime) && p.stratumServer != nil && sessionID != 0 {
				remaining := time.Until(endTime).Round(time.Minute)
				msg := fmt.Sprintf("BLOCK CELEBRATION! %v remaining - Keep mining!", remaining)

				// Send to the miner who found the block
				p.stratumServer.SendMessageToSession(sessionID, msg)

				// Broadcast to ALL miners for visual awareness
				p.stratumServer.BroadcastMessage(msg)
			}
		}
	}
}

// walDBReconciliationLoop periodically verifies that all blocks recorded in the WAL
// as successfully submitted also exist in the database. This catches the edge case
// where the daemon accepted a block but the subsequent InsertBlock() call failed
// (e.g., transient PostgreSQL error). Without this, such blocks would only be
// recovered on restart (V25 startup fix). Runs every 5 minutes (V2 PARITY FIX).
func (p *Pool) walDBReconciliationLoop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := p.reconcileWALWithDB(reconcileCtx); err != nil {
				p.logger.Warnw("Periodic WAL-DB reconciliation had errors",
					"error", err,
				)
			}
			cancel()
		}
	}
}

// reconcileWALWithDB scans WAL entries with terminal success status ("submitted"/"accepted")
// and ensures each has a corresponding database record. InsertBlock is idempotent —
// if the record already exists, this is a no-op. This is the V1 equivalent of V2's
// reconcileSubmittingBlocks: V2 reconciles DB "submitting" rows (because V2 pre-inserts
// into DB before submission), while V1 reconciles WAL→DB gaps (because V1 only inserts
// into DB after submission with the final status).
func (p *Pool) reconcileWALWithDB(ctx context.Context) error {
	if p.blockWAL == nil {
		return nil
	}

	walDir := filepath.Dir(p.blockWAL.FilePath())
	submittedBlocks, err := RecoverSubmittedBlocks(walDir)
	if err != nil {
		return fmt.Errorf("failed to read WAL for reconciliation: %w", err)
	}

	if len(submittedBlocks) == 0 {
		return nil
	}

	var reconcileErrors []error
	reconciledCount := 0
	for _, block := range submittedBlocks {
		// V1 FIX: Aux blocks need Type "auxpow" and must be inserted into
		// the aux pool table (blocks_{poolID}_{symbol}), not the parent table.
		blockType := "block"
		if block.AuxSymbol != "" {
			blockType = "auxpow"
		}

		dbBlock := &database.Block{
			Height:                      block.Height,
			Hash:                        block.BlockHash,
			Miner:                       block.MinerAddress,
			Reward:                      float64(block.CoinbaseValue) / 1e8,
			Status:                      "pending",
			Type:                        blockType,
			ConfirmationProgress:        0,
			TransactionConfirmationData: block.BlockHash,
			Created:                     block.Timestamp,
		}

		var insertErr error
		if block.AuxSymbol != "" {
			// Aux block: insert into scoped aux table
			auxPoolID := p.cfg.Pool.ID + "_" + strings.ToLower(block.AuxSymbol)
			if pdb, ok := p.db.(*database.PostgresDB); ok {
				insertErr = pdb.InsertBlockForPool(ctx, auxPoolID, dbBlock)
			} else if dbMgr, ok := p.db.(*database.DatabaseManager); ok {
				if activeDB := dbMgr.GetActiveDB(); activeDB != nil {
					insertErr = activeDB.InsertBlockForPool(ctx, auxPoolID, dbBlock)
				} else {
					insertErr = fmt.Errorf("no active database available for aux reconciliation")
				}
			} else {
				insertErr = fmt.Errorf("cannot insert aux block: DB type does not support InsertBlockForPool")
			}
		} else {
			// Parent block: insert into main pool table
			insertErr = p.db.InsertBlock(ctx, dbBlock)
		}

		if insertErr != nil {
			p.logger.Errorw("WAL-DB reconciliation: failed to ensure block in database",
				"height", block.Height,
				"hash", block.BlockHash,
				"auxSymbol", block.AuxSymbol,
				"error", insertErr,
			)
			reconcileErrors = append(reconcileErrors, insertErr)
		} else {
			reconciledCount++
		}
	}

	if len(reconcileErrors) > 0 {
		return fmt.Errorf("WAL-DB reconciliation completed with %d errors out of %d blocks",
			len(reconcileErrors), len(submittedBlocks))
	}

	return nil
}

// ══════════════════════════════════════════════════════════════════════════════
// MERGE MINING (AuxPoW) SUPPORT
// ══════════════════════════════════════════════════════════════════════════════
// These methods enable merge mining where the parent chain (BTC/LTC) can also
// mine aux chains (DOGE/NMC/etc) simultaneously. Critically, aux blocks can be
// found and paid INDEPENDENTLY of parent blocks - the hash just needs to meet
// the aux chain's difficulty, not the parent's.
// ══════════════════════════════════════════════════════════════════════════════

// SetAuxManager configures merge mining support for this pool.
// This must be called before Run() if merge mining is desired.
//
// The auxManager provides:
//   - Aux block template fetching and refresh
//   - Aux coin implementations for proof serialization
//   - Submitter for sending aux blocks to aux daemons
//
// CRITICAL: This wires up the validator to check aux targets independently
// of the parent chain, enabling the core merge mining benefit.
func (p *Pool) SetAuxManager(mgr *auxpow.Manager) {
	p.auxManager = mgr
	p.auxSubmitter = auxpow.NewSubmitter(mgr, p.logger.Desugar())

	// Wire the aux manager to the validator so it can check aux targets
	p.shareValidator.SetAuxManager(mgr)

	p.logger.Infow("Merge mining enabled",
		"auxChainCount", mgr.AuxChainCount(),
	)
}

// GetAuxManager returns the AuxPoW manager (nil if merge mining is disabled).
func (p *Pool) GetAuxManager() *auxpow.Manager {
	return p.auxManager
}

// handleAuxBlocks processes aux blocks found in the share result.
// This is called for EVERY accepted share, independent of whether it's a parent block.
//
// CRITICAL: An aux block can be found even when the share doesn't meet the parent
// chain's difficulty. This is the fundamental benefit of merge mining - you get
// paid on aux chains "for free" while mining the parent.
func (p *Pool) handleAuxBlocks(share *protocol.Share, auxResults []protocol.AuxBlockResult) {
	if p.auxSubmitter == nil {
		return
	}

	// HA ROLE GATE: Backup nodes must not submit aux blocks (V2 PARITY FIX).
	// Without this check, a backup node could submit aux blocks to the daemon,
	// causing duplicate submissions or conflicts with the master node.
	// BUG FIX (M7): Check != RoleMaster instead of == RoleBackup.
	if ha.Role(p.haRole.Load()) != ha.RoleMaster {
		p.logger.Debugw("Skipping aux block submission (not master node)",
			"auxBlocks", len(auxResults),
		)
		return
	}

	for _, auxResult := range auxResults {
		if !auxResult.IsBlock {
			// Not a block for this aux chain (didn't meet target)
			if auxResult.Error != "" {
				p.logger.Warnw("Aux block validation error",
					"symbol", auxResult.Symbol,
					"height", auxResult.Height,
					"error", auxResult.Error,
				)
			}
			continue
		}

		// AUDIT FIX (ISSUE-8): Log aux block found to dedicated block logger.
		if p.blockLogger != nil {
			p.blockLogger.LogAuxBlockFound(
				auxResult.Symbol,
				auxResult.Height,
				auxResult.BlockHash,
				share.MinerAddress,
				float64(auxResult.CoinbaseValue)/1e8,
			)
		}

		// SUBMIT AUX BLOCK IMMEDIATELY - before any logging
		// Height-locked context: aux proof is built on the parent block's coinbase,
		// so if the parent chain tip advances, the aux proof is stale.
		submitResult := &auxpow.AuxBlockResult{
			Symbol:        auxResult.Symbol,
			ChainID:       auxResult.ChainID,
			Height:        auxResult.Height,
			IsBlock:       true,
			BlockHash:     auxResult.BlockHash,
			AuxPowHex:     auxResult.AuxPowHex,
			CoinbaseValue: auxResult.CoinbaseValue,
		}

		// HeightContext: aux proof is built on parent coinbase, so if parent tip advances,
		// the aux proof is stale. Cancel submission immediately on height change.
		// V2 PARITY FIX: Use roleCtx as parent so demotion cancels in-flight aux submissions.
		auxBaseCtx := p.snapshotRoleCtx()
		auxHeightCtx, auxHeightCancel := p.jobManager.HeightContext(auxBaseCtx)
		auxDeadlineCtx, auxDeadlineCancel := context.WithTimeout(auxHeightCtx, p.submitTimeouts.SubmitDeadline)

		// V1 FIX: WAL pre-submission write for aux blocks (crash recovery).
		// Without this, a crash during aux submission loses the block entirely.
		// Mirrors V2/coinpool.go pattern for aux WAL entries.
		if p.blockWAL != nil {
			auxPreEntry := &BlockWALEntry{
				Timestamp:     time.Now(),
				Height:        auxResult.Height,
				BlockHash:     auxResult.BlockHash,
				MinerAddress:  share.MinerAddress,
				CoinbaseValue: auxResult.CoinbaseValue,
				Status:        "aux_submitting",
				AuxSymbol:     auxResult.Symbol,
			}
			if walErr := p.blockWAL.LogBlockFound(auxPreEntry); walErr != nil {
				p.logger.Errorw("Failed to write aux pre-submission WAL entry",
					"error", walErr,
					"symbol", auxResult.Symbol,
					"height", auxResult.Height,
				)
			}
		}

		submitCtx, submitCancel := context.WithTimeout(auxDeadlineCtx, p.submitTimeouts.SubmitTimeout)
		err := p.auxSubmitter.SubmitAuxBlock(submitCtx, submitResult)
		submitCancel()

		// Handle submission result with retry for transient errors
		auxSubmitted := false
		if err == nil {
			auxSubmitted = true
		} else {
			// Check if permanent rejection
			errStr := err.Error()
			if isPermanentRejection(errStr) {
				p.logger.Errorw("🚫 AUX BLOCK REJECTED (permanent - no retry)",
					"symbol", auxResult.Symbol,
					"height", auxResult.Height,
					"hash", auxResult.BlockHash,
					"chainId", auxResult.ChainID,
					"error", err,
					"miner", share.MinerAddress,
				)
			} else {
				// Transient error - deadline-driven retry loop (same pattern as parent block)
				attempt := 2
				for auxDeadlineCtx.Err() == nil && attempt <= p.submitTimeouts.MaxRetries+1 {
					time.Sleep(p.submitTimeouts.RetrySleep)
					if auxDeadlineCtx.Err() != nil {
						break
					}

					retryCtx, retryCancel := context.WithTimeout(auxDeadlineCtx, p.submitTimeouts.RetryTimeout)
					retryErr := p.auxSubmitter.SubmitAuxBlock(retryCtx, submitResult)
					retryCancel()

					if retryErr == nil {
						p.logger.Infow("✅ Aux block submitted on retry!",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"attempt", attempt,
						)
						auxSubmitted = true
						break
					}

					if isPermanentRejection(retryErr.Error()) {
						p.logger.Errorw("🚫 AUX BLOCK REJECTED on retry (permanent)",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"error", retryErr,
							"attempt", attempt,
						)
						break
					}
					attempt++
				}

				if !auxSubmitted {
					// Classify abort reason
					if auxDeadlineCtx.Err() != nil {
						p.logger.Warnw("Aux block submission aborted: deadline expired",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"hash", auxResult.BlockHash,
							"deadline", p.submitTimeouts.SubmitDeadline,
						)
					} else {
						p.logger.Errorw("🚫 AUX BLOCK FOUND BUT SUBMISSION FAILED after retries!",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"hash", auxResult.BlockHash,
							"chainId", auxResult.ChainID,
							"error", err,
							"miner", share.MinerAddress,
						)
					}
				}
			}
		}
		auxDeadlineCancel()
		auxHeightCancel()

		if auxSubmitted {
			p.logger.Infow("🎉 AUX BLOCK FOUND AND SUBMITTED!",
				"symbol", auxResult.Symbol,
				"height", auxResult.Height,
				"hash", auxResult.BlockHash,
				"chainId", auxResult.ChainID,
				"reward", float64(auxResult.CoinbaseValue)/1e8,
				"miner", share.MinerAddress,
				"worker", share.WorkerName,
			)

			// Celebrate merge-mined block (same light show as parent blocks)
			p.startCelebration(share.SessionID, auxResult.Height, share.MinerAddress, share.WorkerName, float64(auxResult.CoinbaseValue)/1e8, auxResult.Symbol)

			// AUDIT FIX (ISSUE-6): Increment AuxBlocksSubmitted metric.
			if p.metricsServer != nil {
				p.metricsServer.RecordAuxBlockSubmitted(auxResult.Symbol)
			}
			// AUDIT FIX (ISSUE-8): Log aux block submission to dedicated block logger.
			if p.blockLogger != nil {
				p.blockLogger.LogAuxBlockSubmitted(auxResult.Symbol, auxResult.Height, auxResult.BlockHash, true, "")
			}

			// V1 FIX: Update WAL entry from "aux_submitting" to "aux_pending" after successful submission.
			// Without this, the WAL entry stays in "aux_submitting" forever and crash recovery
			// would incorrectly try to resubmit an already-accepted block.
			if p.blockWAL != nil {
				auxPostEntry := &BlockWALEntry{
					Timestamp:    time.Now(),
					Height:       auxResult.Height,
					BlockHash:    auxResult.BlockHash,
					MinerAddress: share.MinerAddress,
					AuxSymbol:    auxResult.Symbol,
					Status:       "aux_pending",
				}
				if walErr := p.blockWAL.LogSubmissionResult(auxPostEntry); walErr != nil {
					p.logger.Errorw("Failed to write aux post-submission WAL entry",
						"error", walErr,
						"symbol", auxResult.Symbol,
						"height", auxResult.Height,
					)
				}
			}

			// Record aux block in database using a separate aux pool ID
			// AUDIT FIX: Use InsertBlockForPool with aux-specific pool ID to isolate
			// aux blocks from parent blocks. Use underscore separator and lowercase
			// symbol to match the validPoolID regex ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$.
			if p.db != nil {
				auxBlock := &database.Block{
					Height:            auxResult.Height,
					NetworkDifficulty: 0, // Aux chain difficulty not tracked; parent chain validates PoW
					Status:            "pending",
					Type:              "auxpow",
					Miner:             share.MinerAddress,
					Source:            share.WorkerName,
					Reward:            float64(auxResult.CoinbaseValue) / 1e8,
					Hash:              auxResult.BlockHash,
					Created:           time.Now(),
				}

				auxPoolID := p.cfg.Pool.ID + "_" + strings.ToLower(auxResult.Symbol)
				// insertAuxBlock attempts the DB insert via the appropriate path
				insertAuxBlock := func() error {
					insertCtx, insertCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer insertCancel()
					if pdb, ok := p.db.(*database.PostgresDB); ok {
						return pdb.InsertBlockForPool(insertCtx, auxPoolID, auxBlock)
					} else if dbMgr, ok := p.db.(*database.DatabaseManager); ok {
						if activeDB := dbMgr.GetActiveDB(); activeDB != nil {
							return activeDB.InsertBlockForPool(insertCtx, auxPoolID, auxBlock)
						}
						return fmt.Errorf("no active database available")
					}
					return p.db.InsertBlock(insertCtx, auxBlock)
				}

				if err := insertAuxBlock(); err != nil {
					p.logger.Errorw("CRITICAL: Failed to record aux block in database — merge-mined reward at risk",
						"symbol", auxResult.Symbol,
						"height", auxResult.Height,
						"hash", auxResult.BlockHash,
						"auxPoolID", auxPoolID,
						"reward", float64(auxResult.CoinbaseValue)/1e8,
						"error", err,
					)
					// Retry once — this aux block is on-chain, losing DB record = lost reward
					time.Sleep(2 * time.Second)
					if retryErr := insertAuxBlock(); retryErr != nil {
						p.logger.Errorw("CRITICAL: Aux block DB insert retry FAILED — block on-chain but NOT in payment pipeline. Manual reconciliation required.",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"hash", auxResult.BlockHash,
							"auxPoolID", auxPoolID,
							"reward", float64(auxResult.CoinbaseValue)/1e8,
							"error", retryErr,
						)
					} else {
						p.logger.Infow("Aux block DB insert retry succeeded", "symbol", auxResult.Symbol, "height", auxResult.Height)
					}
				}
			}
		} else {
			// AUDIT FIX (ISSUE-6): Increment AuxBlocksFailed metric on submission failure.
			if p.metricsServer != nil {
				p.metricsServer.RecordAuxBlockFailed(auxResult.Symbol, "submission_failed")
			}
			// AUDIT FIX (ISSUE-8): Log aux block rejection to dedicated block logger.
			if p.blockLogger != nil {
				reason := "submission_failed_after_retries"
				if err != nil {
					reason = err.Error()
				}
				p.blockLogger.LogAuxBlockSubmitted(auxResult.Symbol, auxResult.Height, auxResult.BlockHash, false, reason)
			}
		}
	}
}

// verifyBlockAcceptance checks if a block was accepted by the daemon.
// This is used for post-timeout recovery to detect if a block submission
// succeeded but the response was lost due to network issues.
//
// POST-TIMEOUT VERIFICATION (Audit Recommendation #3):
// After submitblock timeout/error, verify if the block was actually accepted.
// The daemon may have accepted the block but we lost the response due to
// network timeout or connection drop. This prevents false "orphaned" status.
//
// ENHANCED (Audit Recommendation #7): Increased verification window to 30s total
// with 3 retry attempts at 5s, 10s, 15s intervals. This handles slow daemon
// processing/indexing that could cause false negatives with short timeouts.
func (p *Pool) verifyBlockAcceptance(blockHash string, blockHeight uint64) bool {
	// Retry intervals: 5s, 10s, 15s (total 30s verification window)
	retryIntervals := []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}

	for attempt, interval := range retryIntervals {
		ctx, cancel := context.WithTimeout(context.Background(), interval)

		// Method 1: Try to get the block hash at the expected height
		chainHash, err := p.daemonClient.GetBlockHash(ctx, blockHeight)
		if err == nil && chainHash == blockHash {
			cancel()
			// V47 FIX: Elevated from Debug — post-timeout recovery is an exceptional event
			p.logger.Infow("Post-timeout verification: Block found at expected height",
				"height", blockHeight,
				"hash", blockHash,
				"attempt", attempt+1,
			)
			return true
		}

		// Method 2: The hash at height doesn't match, but our block might still
		// be in the chain (just at a different position due to reorg timing).
		// Try to get our block directly by hash.
		if err == nil {
			blockInfo, err := p.daemonClient.GetBlock(ctx, blockHash)
			if err == nil && blockInfo != nil {
				// AUDIT FIX: Check confirmations field. Blocks on stale forks return
				// confirmations=-1 from the daemon (Bitcoin/DigiByte convention).
				// Only treat as "in active chain" if confirmations >= 0.
				// JSON numbers unmarshal as float64 in Go's encoding/json.
				if confs, ok := blockInfo["confirmations"].(float64); ok && confs < 0 {
					cancel()
					p.logger.Warnw("Post-timeout verification: Block exists but on stale fork (negative confirmations)",
						"hash", blockHash,
						"confirmations", confs,
						"attempt", attempt+1,
					)
					continue // Retry — reorg may resolve this
				}
				cancel()
				// V47 FIX: Elevated from Debug — post-timeout recovery is an exceptional event
				p.logger.Infow("Post-timeout verification: Block found in chain by hash",
					"hash", blockHash,
					"confirmations", blockInfo["confirmations"],
					"attempt", attempt+1,
				)
				return true
			}
		}

		cancel()

		// Log progress for debugging
		if attempt < len(retryIntervals)-1 {
			// V47 FIX: Elevated from Debug — block not found during recovery is concerning
			p.logger.Warnw("Post-timeout verification: Block not found yet, will retry",
				"height", blockHeight,
				"hash", blockHash,
				"attempt", attempt+1,
				"nextRetryIn", retryIntervals[attempt+1],
			)
			// Brief sleep before next attempt to let daemon process
			time.Sleep(1 * time.Second)
		}
	}

	// V47 FIX: Elevated from Debug — block not found after all retries is a serious concern
	p.logger.Warnw("⚠️ Post-timeout verification: Block not found after all retries",
		"height", blockHeight,
		"hash", blockHash,
		"totalAttempts", len(retryIntervals),
	)
	return false
}

// Run starts all pool components and blocks until shutdown.
func (p *Pool) Run(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)

	// Record server start time for accurate hashrate calculation
	p.startTime = time.Now()

	// SYNC GATE: Wait for daemon to be fully synced before accepting miners
	// This prevents miners from wasting hashrate on stale blocks during IBD
	if err := p.waitForSync(ctx); err != nil {
		return fmt.Errorf("sync gate failed: %w", err)
	}

	// CLEANUP: Remove stale shares from previous sessions
	// This prevents inflated hashrate calculations from old data
	if err := p.cleanupStaleShares(ctx); err != nil {
		p.logger.Warnw("Failed to cleanup stale shares (non-fatal)", "error", err)
	}

	// Initialize block stats from database for API (blocksFound, effort)
	p.initBlockStats(ctx)

	// Start share pipeline
	if err := p.sharePipeline.Start(ctx); err != nil {
		return fmt.Errorf("failed to start share pipeline: %w", err)
	}
	p.logger.Info("Share pipeline started")

	// Start job manager
	if err := p.jobManager.Start(ctx); err != nil {
		return fmt.Errorf("failed to start job manager: %w", err)
	}
	p.logger.Info("Job manager started")

	// Initialize block notifications (RPC polling primary, ZMQ promoted when stable)
	if err := p.initBlockNotifications(ctx); err != nil {
		return fmt.Errorf("failed to initialize block notifications: %w", err)
	}

	// Pre-initialize haRole to RoleBackup if HA is configured.
	// Must happen BEFORE stratumServer.Start() to prevent both nodes
	// from submitting blocks during the startup window before VIP election.
	if p.vipManager != nil {
		p.haRole.Store(int32(ha.RoleBackup))
	}

	// Start stratum server
	if err := p.stratumServer.Start(ctx); err != nil {
		return fmt.Errorf("failed to start stratum server: %w", err)
	}
	p.logger.Infow("Stratum server started",
		"listen", p.cfg.Stratum.Listen,
	)

	// Start HA components if enabled
	if p.dbManager != nil {
		p.dbManager.Start(ctx)
		p.logger.Info("Database HA manager started")
	}
	if p.failoverManager != nil {
		if err := p.failoverManager.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start failover manager", "error", err)
		} else {
			p.logger.Info("Pool failover manager started")
		}
	}
	if p.replicationManager != nil {
		if err := p.replicationManager.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start replication manager", "error", err)
		} else {
			p.logger.Info("PostgreSQL replication manager started")
		}
	}
	// Start replication slot monitor (WAL retention / orphan detection)
	if p.dbManager != nil && p.slotMonitor == nil {
		activeNode := p.dbManager.GetActiveNode()
		if activeNode != nil && activeNode.Pool != nil {
			slotCfg := ha.DefaultReplicationSlotMonitorConfig()
			p.slotMonitor = ha.NewReplicationSlotMonitor(slotCfg, stdlib.OpenDBFromPool(activeNode.Pool), p.logger.Desugar())
			if err := p.slotMonitor.Start(ctx); err != nil {
				p.logger.Errorw("Failed to start replication slot monitor", "error", err)
			} else {
				p.logger.Info("Replication slot monitor started")
			}
		}
	}
	if p.vipManager != nil {
		// haRole already pre-initialized to RoleBackup above (before stratumServer.Start).
		// No need to store again — VIP election will set the final role via handleRoleChange.

		// Set supported coins for VIP election awareness
		p.vipManager.SetSupportedCoins([]string{strings.ToUpper(p.cfg.Pool.Coin)})

		// Wire role change handler for HA component lifecycle management
		p.vipManager.SetRoleChangeHandler(p.handleRoleChange)

		if err := p.vipManager.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start VIP manager", "error", err)
		} else {
			p.logger.Infow("VIP manager started",
				"vip", p.cfg.VIP.Address,
				"role", "starting",
			)

			// Start coin sync status feed for VIP election decisions
			p.wg.Add(1)
			go p.syncStatusLoop(ctx)
		}
	}

	// Start Redis dedup health monitoring if configured.
	// This enables graceful degradation to local fallback when Redis is unavailable,
	// and automatic re-enabling when connectivity is restored.
	if p.redisDedupTracker != nil {
		p.redisDedupTracker.StartHealthCheck(ctx)
		p.logger.Info("Redis dedup health check started")
	}

	// Start payment processor if enabled.
	// This begins the payment processing loop which checks block maturity,
	// detects orphans, and processes payouts according to the configured scheme.
	// In HA mode, payment processing is fenced by isMaster + advisory locks.
	if p.paymentProcessor != nil {
		if err := p.paymentProcessor.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start payment processor", "error", err)
		} else {
			p.logger.Infow("Payment processor started",
				"scheme", p.cfg.Payments.Scheme,
				"haEnabled", p.vipManager != nil,
			)
		}
	}

	// Start aux chain payment processors (V1 merge mining fix)
	for auxPoolID, auxProc := range p.auxPaymentProcessors {
		if err := auxProc.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start aux payment processor", "auxPoolId", auxPoolID, "error", err)
		} else {
			p.logger.Infow("Aux payment processor started", "auxPoolId", auxPoolID)
		}
	}

	// Start API server if enabled
	if p.apiServer != nil {
		if err := p.apiServer.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start API server", "error", err)
		} else {
			p.logger.Infow("API server started", "listen", p.cfg.API.Listen)
		}
	}

	// Start Prometheus metrics server if enabled
	if p.metricsServer != nil {
		// V45 FIX: Wire real health check that probes daemon and DB
		// AUDIT FIX (SF-1): Expanded to cover database, circuit breaker, share pipeline,
		// and payment processor. The original check only covered daemon RPC + template
		// staleness — blind to the exact failure mode that caused the 0 TH/s incident
		// (DB down → circuit breaker OPEN → shares only to WAL → pool appears healthy).
		p.metricsServer.SetHealthCheck(func() (bool, string) {
			healthy := true
			var problems []string

			// Check daemon RPC health
			if p.jobManager.IsCritical() {
				healthy = false
				problems = append(problems, "daemon RPC critical failure")
			} else if p.jobManager.IsDegraded() {
				problems = append(problems, "daemon RPC degraded")
			}

			// Check template staleness
			if p.jobManager.IsTemplateStale() {
				healthy = false
				problems = append(problems, "block templates stale")
			}

			// Check share pipeline / circuit breaker / DB health
			if p.sharePipeline != nil {
				_, degraded, critical, _, circuitState := p.sharePipeline.DBHealthStatus()
				if critical {
					healthy = false
					problems = append(problems, "database critical (share pipeline)")
				} else if degraded {
					problems = append(problems, "database degraded (share pipeline)")
				}
				if circuitState == "open" {
					healthy = false
					problems = append(problems, "circuit breaker OPEN — shares not reaching DB")
				}
			}

			// Check payment processor health
			if p.paymentProcessor != nil {
				failedCycles := p.paymentProcessor.ConsecutiveFailedCycles()
				if failedCycles >= 5 {
					healthy = false
					problems = append(problems, fmt.Sprintf("payment processor failing (%d consecutive cycles)", failedCycles))
				}
			}

			// V44: Check if metrics server itself failed
			if p.metricsServer.IsServerFailed() {
				healthy = false
				problems = append(problems, "metrics server failed")
			}

			if len(problems) == 0 {
				return true, "all subsystems operational"
			}
			return healthy, strings.Join(problems, "; ")
		})

		if err := p.metricsServer.Start(ctx); err != nil {
			p.logger.Errorw("Failed to start metrics server", "error", err)
		} else {
			p.logger.Infow("Metrics server started", "listen", p.cfg.Metrics.Listen)
		}
	}

	// V33 FIX: Stagger periodic goroutines with random jitter to prevent thundering herd
	// on startup. Without jitter, all tickers fire simultaneously on restart, creating
	// burst load against PostgreSQL and daemon RPC.

	// Start stats updater (jitter 0-10s)
	p.wg.Add(1)
	go func() {
		jitter := time.Duration(rand.Intn(10000)) * time.Millisecond
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			p.wg.Done()
			return
		}
		p.statsLoop(ctx)
	}()

	// Start network difficulty updater (jitter 0-10s)
	p.wg.Add(1)
	go func() {
		jitter := time.Duration(rand.Intn(10000)) * time.Millisecond
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			p.wg.Done()
			return
		}
		p.difficultyLoop(ctx)
	}()

	// H-4: Start session cleanup loop to remove orphaned VARDIFF states
	// This handles cases where miners disconnect abruptly without calling disconnect handler
	p.wg.Add(1)
	go func() {
		jitter := time.Duration(rand.Intn(15000)) * time.Millisecond
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			p.wg.Done()
			return
		}
		p.sessionCleanupLoop(ctx)
	}()

	// Start celebration loop for block found announcements
	p.wg.Add(1)
	go p.celebrationLoop(ctx)

	// V2 PARITY FIX: Start periodic WAL-DB reconciliation loop.
	// Ensures blocks accepted by daemon but missing from DB (due to transient
	// InsertBlock failure) are recovered without waiting for a full restart.
	// V2 has reconciliationLoop for DB "submitting" rows; V1 equivalent
	// reconciles WAL→DB gaps since V1 inserts with final status only.
	if p.blockWAL != nil {
		p.wg.Add(1)
		go func() {
			jitter := time.Duration(rand.Intn(15000)) * time.Millisecond
			select {
			case <-time.After(jitter):
			case <-ctx.Done():
				p.wg.Done()
				return
			}
			p.walDBReconciliationLoop(ctx)
		}()
	}

	p.logger.Infow("Pool started successfully",
		"poolId", p.cfg.Pool.ID,
		"coin", p.cfg.Pool.Coin,
		"address", p.cfg.Pool.Address,
		"dbHA", p.dbManager != nil,
		"poolFailover", p.failoverManager != nil,
		"vipEnabled", p.vipManager != nil,
		"replicationEnabled", p.replicationManager != nil,
		"apiEnabled", p.apiServer != nil,
		"metricsEnabled", p.metricsServer != nil,
	)

	// Wait for shutdown
	<-ctx.Done()

	return p.shutdown()
}

// shutdown gracefully shuts down all components.
func (p *Pool) shutdown() error {
	p.logger.Info("Shutting down pool...")

	// Stop accepting new connections
	if err := p.stratumServer.Stop(); err != nil {
		p.logger.Errorw("Error stopping stratum server", "error", err)
	}

	// Stop API server
	if p.apiServer != nil {
		if err := p.apiServer.Stop(); err != nil {
			p.logger.Errorw("Error stopping API server", "error", err)
		} else {
			p.logger.Info("API server stopped")
		}
	}

	// Stop metrics server
	if p.metricsServer != nil {
		if err := p.metricsServer.Stop(); err != nil {
			p.logger.Errorw("Error stopping metrics server", "error", err)
		} else {
			p.logger.Info("Metrics server stopped")
		}
	}

	// Stop ZMQ and polling
	if p.zmqListener != nil {
		if err := p.zmqListener.Stop(); err != nil {
			p.logger.Errorw("Error stopping ZMQ listener", "error", err)
		}
	}
	p.stopPollingFallback()

	// Stop HA components (VIP first to release VIP before database stops)
	if p.vipManager != nil {
		if err := p.vipManager.Stop(); err != nil {
			p.logger.Errorw("Error stopping VIP manager", "error", err)
		} else {
			p.logger.Info("VIP manager stopped")
		}
	}
	if p.slotMonitor != nil {
		p.slotMonitor.Stop()
		p.logger.Info("Replication slot monitor stopped")
	}
	if p.replicationManager != nil {
		p.replicationManager.Stop()
		p.logger.Info("Replication manager stopped")
	}
	if p.failoverManager != nil {
		if err := p.failoverManager.Stop(); err != nil {
			p.logger.Errorw("Error stopping failover manager", "error", err)
		}
	}

	// P-1 FIX: Enforce shutdown deadline to prevent SIGKILL.
	// Budget: 60s goroutine wait + ~40s pipeline flush + ~5s cleanup = ~105s total.
	// SystemD TimeoutStopSec=150 gives 45s margin beyond our internal 105s target.
	deadline := time.Now().Add(105 * time.Second)

	// ORPHAN FIX #8: Extended shutdown timeout for in-flight block submissions
	// Block submission can take up to 5 retries with exponential backoff (1+2+4+8+16=31s)
	// plus 60s timeout per attempt. 60s allows most submissions to complete.
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	shutdownTimeout := 60 * time.Second
	select {
	case <-done:
		p.logger.Info("Background goroutines stopped cleanly")
	case <-time.After(shutdownTimeout):
		p.logger.Warnw("Timeout waiting for background goroutines - possible in-flight block submission interrupted!",
			"timeout", shutdownTimeout,
			"action", "Check WAL for unsubmitted blocks on next startup",
		)
	}

	// Stop share pipeline with deadline (P-1 FIX: prevents indefinite wg.Wait blocking)
	if err := p.sharePipeline.StopWithDeadline(deadline); err != nil {
		p.logger.Errorw("Error stopping share pipeline", "error", err)
	}

	// Close block WAL (ensures final sync)
	if p.blockWAL != nil {
		if err := p.blockWAL.Close(); err != nil {
			p.logger.Errorw("Error closing block WAL", "error", err)
		} else {
			p.logger.Info("Block WAL closed")
		}
	}

	// Close block logger
	if p.blockLogger != nil {
		if err := p.blockLogger.Close(); err != nil {
			p.logger.Errorw("Error closing block logger", "error", err)
		} else {
			p.logger.Info("Block logger closed")
		}
	}

	// Stop aux chain payment processors (V1 merge mining fix)
	for auxPoolID, auxProc := range p.auxPaymentProcessors {
		if err := auxProc.Stop(); err != nil {
			p.logger.Errorw("Error stopping aux payment processor", "auxPoolId", auxPoolID, "error", err)
		} else {
			p.logger.Infow("Aux payment processor stopped", "auxPoolId", auxPoolID)
		}
	}

	// Close database (this also stops dbManager if it's HA mode)
	if err := p.db.Close(); err != nil {
		p.logger.Errorw("Error closing database", "error", err)
	}

	p.logger.Info("Pool shutdown complete")
	return nil
}

// SetPaymentProcessor sets the payment processor reference for HA role management.
// When HA role changes, the processor is notified to enable/disable payment cycles.
func (p *Pool) SetPaymentProcessor(proc *payments.Processor) {
	p.paymentProcessor = proc
	// If HA is enabled, configure the processor for fenced operation
	if p.vipManager != nil {
		proc.SetHAEnabled(true)
		// BUG FIX (M9): Default isMaster to false when HA configured. See coordinator.go.
		proc.SetMasterRole(false)
	}

	// Wire advisory locker for defense-in-depth payment fencing.
	// The advisory lock prevents double-payment even in split-brain scenarios
	// where both nodes believe they are master (VIP fencing failure).
	// In HA mode, use DatabaseManager (follows failover automatically).
	// In standalone mode, use PostgresDB directly.
	if p.dbManager != nil {
		proc.SetAdvisoryLocker(p.dbManager)
		p.logger.Info("Payment processor: advisory lock wired via HA database manager")
	} else if postgresDB, ok := p.db.(*database.PostgresDB); ok {
		proc.SetAdvisoryLocker(postgresDB)
		p.logger.Info("Payment processor: advisory lock wired via standalone PostgresDB")
	}
}

// SetRedisDedupTracker sets the Redis dedup tracker for HA block submission coordination.
// When set, block submissions use Redis SETNX to prevent duplicate daemon RPC calls
// across HA nodes during failover windows.
func (p *Pool) SetRedisDedupTracker(tracker *shares.RedisDedupTracker) {
	p.redisDedupTracker = tracker
}

// ============================================================================
// HA: Coin Sync Status Feed, Role Change Handler, Graceful Transitions
// ============================================================================

// syncStatusLoop periodically queries the daemon for blockchain sync status
// and feeds it to the VIP manager for election decisions.
// Only fully-synced nodes should win elections to avoid serving stale work.
func (p *Pool) syncStatusLoop(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			infoCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			info, err := p.daemonClient.GetBlockchainInfo(infoCtx)
			cancel()
			if err != nil {
				p.logger.Debugw("Sync status feed: failed to query daemon", "error", err)
				continue
			}
			if p.vipManager != nil {
				coinSymbol := strings.ToUpper(p.cfg.Pool.Coin)
				p.vipManager.UpdateCoinSyncStatus(coinSymbol, info.VerificationProgress*100, int64(info.Blocks))
			}
		}
	}
}

// snapshotRoleCtx returns a snapshot of the current roleCtx under roleMu.
// All submission contexts (block and aux) MUST use this helper instead of reading
// p.roleCtx directly. This ensures the snapshot is always taken under the mutex,
// preventing race conditions with concurrent promoteToMaster/demoteToBackup calls.
// The returned context will be cancelled if this node is demoted (roleCancel() fires),
// which propagates cancellation to all child contexts (HeightContext, submitCtx, etc.).
func (p *Pool) snapshotRoleCtx() context.Context {
	p.roleMu.Lock()
	ctx := p.roleCtx
	p.roleMu.Unlock()
	return ctx
}

// handleRoleChange is called by the VIP manager when this node's HA role changes.
// It coordinates enabling/disabling subsystems based on the new role.
func (p *Pool) handleRoleChange(oldRole, newRole ha.Role) {
	// V2 PARITY FIX: Set haRole FIRST, before any subsystem transitions,
	// so that concurrent handleBlock/handleAuxBlocks goroutines see the
	// new role immediately and respect the HA gate.
	// AF-1 FIX: Atomic store eliminates data race with concurrent readers.
	p.haRole.Store(int32(newRole))

	p.logger.Infow("HA role changed",
		"from", oldRole.String(),
		"to", newRole.String(),
	)

	switch newRole {
	case ha.RoleMaster:
		p.promoteToMaster()
	case ha.RoleBackup, ha.RoleObserver:
		// BUG FIX (M8): RoleObserver must also demote. See coordinator.go.
		p.demoteToBackup()
	}

	// Update HA cluster state metric — any successful role resolution means cluster is running
	if p.metricsServer != nil {
		p.metricsServer.SetHAClusterState("running")
	}
}

// promoteToMaster enables all subsystems for active operation.
// Called when this node becomes the HA master.
//
// Promotion order is important:
//  1. Verify database is writable (blocks on replication catchup)
//  2. Flush block WAL to ensure no pending entries are lost
//  3. Enable payment processing (single-writer guarantee)
//  4. Update metrics
func (p *Pool) promoteToMaster() {
	p.logger.Info("Beginning MASTER promotion sequence...")

	// 1. Verify database is writable (if HA DB is in use)
	if p.dbManager != nil {
		activeNode := p.dbManager.GetActiveNode()
		if activeNode != nil && activeNode.State != database.DBNodeHealthy {
			p.logger.Warnw("MASTER promotion: active DB node not healthy, waiting for recovery",
				"nodeID", activeNode.ID,
				"state", activeNode.State.String(),
			)
			// Brief wait for DB promotion triggered by replication manager
			time.Sleep(5 * time.Second)
		}
	}

	// 2. Create fresh role context for the new master session.
	// AF-3 FIX: Cancel the old roleCtx before replacing it to prevent context leak.
	// On initial startup (constructor context) or after demotion (already cancelled),
	// this is a no-op or double-cancel (both safe). The critical case is a direct
	// re-promotion where the old context was never cancelled.
	p.roleMu.Lock()
	if p.roleCancel != nil {
		p.roleCancel()
	}
	p.roleCtx, p.roleCancel = context.WithCancel(context.Background())
	p.roleMu.Unlock()

	// 3. Flush any pending block WAL entries
	if p.blockWAL != nil {
		if err := p.blockWAL.FlushToDisk(); err != nil {
			p.logger.Warnw("MASTER promotion: failed to flush block WAL", "error", err)
		}
	}

	// 4. Enable payment processing
	if p.paymentProcessor != nil {
		p.paymentProcessor.SetMasterRole(true)
	}

	// 4b. V1 FIX: Enable aux chain payment processing
	for auxPoolID, auxProc := range p.auxPaymentProcessors {
		auxProc.SetMasterRole(true)
		p.logger.Debugw("Aux payment processor promoted to master", "auxPoolId", auxPoolID)
	}

	// 5. Update metrics
	if p.metricsServer != nil {
		p.metricsServer.SetHANodeRole("master")
	}

	// 6. AF-7 FIX: Immediately recover any blocks that were in-flight during
	// a rapid demotion-promotion cycle. Without this, blocks stuck in "submitting"
	// state in the WAL would not be recovered until the next 5-minute reconciliation
	// cycle or process restart. Run in a goroutine to avoid blocking promotion.
	// AUDIT FIX (SP-5): Track goroutine with wg.Add and recover panics.
	// Without wg.Add, Stop() could return while WAL recovery is still running,
	// causing a crash-on-shutdown race.
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				p.logger.Errorw("Panic in post-promotion WAL recovery", "panic", r)
			}
		}()
		p.recoverWALAfterPromotion()
	}()

	p.logger.Info("MASTER promotion complete - all subsystems active")
}

// recoverWALAfterPromotion checks for blocks that were in-flight during a
// demotion-promotion cycle and ensures they are properly recorded. This handles
// the scenario where roleCancel() aborted an in-flight submission, leaving a
// WAL entry with status "submitting"/"pending" but no DB record or daemon
// confirmation. Runs asynchronously after promotion completes.
func (p *Pool) recoverWALAfterPromotion() {
	if p.blockWAL == nil {
		return
	}

	// Re-entrancy guard: prevent concurrent recovery from rapid demotion-promotion cycles.
	// CompareAndSwap returns false if another goroutine is already running recovery.
	if !p.walRecoveryRunning.CompareAndSwap(false, true) {
		p.logger.Warn("Post-promotion WAL recovery already in progress, skipping duplicate")
		return
	}
	defer p.walRecoveryRunning.Store(false)

	// Brief delay to let daemon connections stabilize after promotion
	time.Sleep(2 * time.Second)

	p.logger.Info("Post-promotion WAL recovery starting...")

	// Phase 1: WAL-DB reconciliation — ensure all submitted WAL entries have DB records.
	// This catches blocks where the daemon accepted the block but DB insert failed
	// (e.g., the demotion cancelled the context mid-insert).
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := p.reconcileWALWithDB(reconcileCtx); err != nil {
		p.logger.Warnw("Post-promotion WAL-DB reconciliation had errors", "error", err)
	}
	reconcileCancel()

	// Phase 2: Check for unsubmitted blocks (status "submitting" or "pending" in WAL).
	// These are blocks where submission was interrupted by demotion context cancellation.
	walDir := filepath.Dir(p.blockWAL.FilePath())
	unsubmitted, err := RecoverUnsubmittedBlocks(walDir)
	if err != nil {
		p.logger.Warnw("Post-promotion WAL recovery: failed to read unsubmitted blocks", "error", err)
		return
	}

	if len(unsubmitted) == 0 {
		p.logger.Info("Post-promotion WAL recovery complete — no unsubmitted blocks found")
		return
	}

	p.logger.Infow("Post-promotion WAL recovery: found unsubmitted blocks",
		"count", len(unsubmitted),
	)

	for _, block := range unsubmitted {
		// V1 FIX: Aux blocks need different recovery — they use submitauxblock
		// (not submitblock) and their proof data isn't stored in WAL.
		// We can only verify if the aux daemon already accepted it.
		if block.AuxSymbol != "" {
			var auxDaemonClient *daemon.Client
			if p.auxManager != nil {
				for _, auxCfg := range p.auxManager.GetAuxChainConfigs() {
					if strings.EqualFold(auxCfg.Symbol, block.AuxSymbol) && auxCfg.Enabled {
						auxDaemonClient = auxCfg.DaemonClient
						break
					}
				}
			}

			if auxDaemonClient == nil {
				p.logger.Warnw("Post-promotion WAL recovery: cannot verify aux block — no daemon client found",
					"symbol", block.AuxSymbol,
					"height", block.Height,
					"hash", block.BlockHash,
				)
				continue
			}

			// Verify if aux block was accepted by the aux daemon
			verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
			auxHash, hashErr := auxDaemonClient.GetBlockHash(verifyCtx, block.Height)
			verifyCancel()

			if hashErr == nil && auxHash == block.BlockHash {
				p.logger.Infow("Post-promotion WAL recovery: aux block confirmed in chain",
					"symbol", block.AuxSymbol,
					"height", block.Height,
					"hash", block.BlockHash,
				)

				block.Status = "aux_pending"
				block.SubmitError = "recovered_after_promotion: aux block found in chain"
				p.blockWAL.LogSubmissionResult(&block)

				// Ensure DB record exists in aux pool table
				auxPoolID := p.cfg.Pool.ID + "_" + strings.ToLower(block.AuxSymbol)
				dbBlock := &database.Block{
					Height:               block.Height,
					Hash:                 block.BlockHash,
					Miner:                block.MinerAddress,
					Reward:               float64(block.CoinbaseValue) / 1e8,
					Status:               "pending",
					Type:                 "auxpow",
					ConfirmationProgress: 0,
					Created:              block.Timestamp,
				}
				dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
				var auxInsertErr error
				if pdb, ok := p.db.(*database.PostgresDB); ok {
					auxInsertErr = pdb.InsertBlockForPool(dbCtx, auxPoolID, dbBlock)
				} else if dbMgr, ok := p.db.(*database.DatabaseManager); ok {
					if activeDB := dbMgr.GetActiveDB(); activeDB != nil {
						auxInsertErr = activeDB.InsertBlockForPool(dbCtx, auxPoolID, dbBlock)
					} else {
						auxInsertErr = fmt.Errorf("no active database for aux recovery")
					}
				}
				dbCancel()
				if auxInsertErr != nil {
					p.logger.Errorw("Post-promotion WAL recovery: failed to ensure aux block in DB",
						"symbol", block.AuxSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
						"auxPoolID", auxPoolID,
						"error", auxInsertErr,
					)
				}
			} else {
				// Aux block not in chain — cannot resubmit (proof data not stored in WAL).
				// The aux proof is built from the parent coinbase and is ephemeral.
				p.logger.Warnw("Post-promotion WAL recovery: aux block NOT in chain — proof data unavailable, cannot resubmit",
					"symbol", block.AuxSymbol,
					"height", block.Height,
					"hash", block.BlockHash,
				)
				block.Status = "failed"
				block.SubmitError = "recovered_after_promotion: aux block not in chain and proof data unavailable"
				p.blockWAL.LogSubmissionResult(&block)
			}
			continue
		}

		// Check if block is already in chain (submission may have succeeded before cancellation)
		if p.verifyBlockAcceptance(block.BlockHash, block.Height) {
			p.logger.Infow("Post-promotion WAL recovery: block already in chain",
				"height", block.Height,
				"hash", block.BlockHash,
			)

			// Update WAL to reflect accepted status
			block.Status = "submitted"
			block.SubmitError = "recovered_after_promotion: block found in chain"
			p.blockWAL.LogSubmissionResult(&block)

			// Ensure DB record exists
			dbBlock := &database.Block{
				Height:                      block.Height,
				Hash:                        block.BlockHash,
				Miner:                       block.MinerAddress,
				Reward:                      float64(block.CoinbaseValue) / 1e8,
				Status:                      "pending",
				Type:                        "block",
				ConfirmationProgress:        0,
				TransactionConfirmationData: block.BlockHash,
				Created:                     block.Timestamp,
			}
			dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if insertErr := p.db.InsertBlock(dbCtx, dbBlock); insertErr != nil {
				p.logger.Errorw("Post-promotion WAL recovery: failed to ensure block in DB",
					"height", block.Height,
					"hash", block.BlockHash,
					"error", insertErr,
				)
			}
			dbCancel()
			continue
		}

		// Block not in chain — check if recent enough for resubmission
		if block.BlockHex == "" {
			p.logger.Warnw("Post-promotion WAL recovery: block hex missing, cannot resubmit",
				"height", block.Height,
				"hash", block.BlockHash,
			)
			continue
		}

		infoCtx, infoCancel := context.WithTimeout(context.Background(), 10*time.Second)
		chainInfo, infoErr := p.daemonClient.GetBlockchainInfo(infoCtx)
		infoCancel()
		if infoErr != nil {
			p.logger.Warnw("Post-promotion WAL recovery: cannot check chain height",
				"error", infoErr,
			)
			continue
		}

		blockAge := chainInfo.Blocks - block.Height
		if blockAge > 100 {
			p.logger.Warnw("Post-promotion WAL recovery: block too old for resubmission",
				"height", block.Height,
				"hash", block.BlockHash,
				"blockAge", blockAge,
			)
			block.Status = "rejected"
			block.SubmitError = fmt.Sprintf("recovered_stale_after_promotion: chain moved %d blocks ahead", blockAge)
			p.blockWAL.LogSubmissionResult(&block)
			continue
		}

		// Attempt resubmission
		p.logger.Infow("Post-promotion WAL recovery: attempting resubmission",
			"height", block.Height,
			"hash", block.BlockHash,
			"blockAge", blockAge,
		)

		submitCtx, submitCancel := context.WithTimeout(context.Background(), 60*time.Second)
		submitErr := p.daemonClient.SubmitBlock(submitCtx, block.BlockHex)
		submitCancel()

		if submitErr == nil {
			p.logger.Infow("Post-promotion WAL recovery: block resubmitted successfully",
				"height", block.Height,
				"hash", block.BlockHash,
			)
			block.Status = "submitted"
			block.SubmitError = "recovered_resubmitted_after_promotion"
			p.blockWAL.LogSubmissionResult(&block)

			// Ensure DB record exists
			dbBlock := &database.Block{
				Height:                      block.Height,
				Hash:                        block.BlockHash,
				Miner:                       block.MinerAddress,
				Reward:                      float64(block.CoinbaseValue) / 1e8,
				Status:                      "pending",
				Type:                        "block",
				ConfirmationProgress:        0,
				TransactionConfirmationData: block.BlockHash,
				Created:                     block.Timestamp,
			}
			dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if insertErr := p.db.InsertBlock(dbCtx, dbBlock); insertErr != nil {
				p.logger.Errorw("Post-promotion WAL recovery: failed to ensure resubmitted block in DB",
					"height", block.Height,
					"hash", block.BlockHash,
					"error", insertErr,
				)
			}
			dbCancel()
		} else {
			p.logger.Warnw("Post-promotion WAL recovery: resubmission failed",
				"height", block.Height,
				"hash", block.BlockHash,
				"error", submitErr,
			)
		}
	}

	p.logger.Info("Post-promotion WAL recovery complete")
}

// demoteToBackup disables write-path subsystems for standby operation.
// Called when this node becomes an HA backup.
//
// Demotion order is important:
//  1. Disable payment processing FIRST (prevent split-brain payments)
//  2. Flush block WAL
//  3. Update metrics
func (p *Pool) demoteToBackup() {
	p.logger.Info("Beginning BACKUP demotion sequence...")

	// 1. Disable payment processing FIRST to prevent split-brain payments
	if p.paymentProcessor != nil {
		p.paymentProcessor.SetMasterRole(false)
	}

	// 1b. V1 FIX: Disable aux chain payment processing
	for auxPoolID, auxProc := range p.auxPaymentProcessors {
		auxProc.SetMasterRole(false)
		p.logger.Debugw("Aux payment processor demoted to backup", "auxPoolId", auxPoolID)
	}

	// 2. V2 PARITY FIX: Cancel in-flight block submissions immediately on demotion.
	// Any handleBlock/handleAuxBlocks goroutine using roleCtx as parent will
	// have its context cancelled, aborting stale RPC calls. Then create a fresh
	// context for any future submissions if this node is re-promoted.
	p.roleMu.Lock()
	if p.roleCancel != nil {
		p.roleCancel()
	}
	p.roleCtx, p.roleCancel = context.WithCancel(context.Background())
	p.roleMu.Unlock()

	// 3. Sync block WAL
	if p.blockWAL != nil {
		if err := p.blockWAL.FlushToDisk(); err != nil {
			p.logger.Warnw("BACKUP demotion: failed to flush block WAL", "error", err)
		}
	}

	// 4. Update metrics
	if p.metricsServer != nil {
		p.metricsServer.SetHANodeRole("backup")
	}

	// Note: Stratum server stays running - VIP ensures traffic doesn't reach us.
	// If a miner is already connected, it can still submit shares (accepted),
	// but the HA role gate in handleBlock/handleAuxBlocks prevents submission.

	p.logger.Info("BACKUP demotion complete - payments paused, in-flight submissions cancelled")
}

// statsLoop periodically updates pool statistics.
func (p *Pool) statsLoop(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.updateStats(ctx)
		}
	}
}

// updateStats updates pool statistics in the database.
func (p *Pool) updateStats(ctx context.Context) {
	stratumStats := p.stratumServer.Stats()
	pipelineStats := p.sharePipeline.Stats()

	// Get network info
	bcInfo, err := p.daemonClient.GetBlockchainInfo(ctx)
	if err != nil {
		p.logger.Warnw("Failed to get blockchain info", "error", err)
		return
	}

	// Calculate pool hashrate from recent shares
	hashrate, err := p.db.GetPoolHashrate(ctx, 10)
	if err != nil {
		p.logger.Warnw("Failed to calculate pool hashrate", "error", err)
	}

	sharesPerSecond := float64(pipelineStats.Processed) / 60.0

	// Use the algorithm-specific difficulty from the share validator (set by difficultyLoop
	// via getdifficulty RPC) instead of bcInfo.Difficulty from getblockchaininfo.
	// For multi-algo coins like DigiByte, getblockchaininfo returns a generic difficulty
	// that doesn't match the SHA-256d difficulty needed for accurate ETB calculations.
	networkDiff := p.shareValidator.GetNetworkDifficulty()
	if networkDiff <= 0 {
		// Fallback to bcInfo.Difficulty if validator hasn't been initialized yet
		networkDiff = bcInfo.Difficulty
	}

	stats := &database.PoolStats{
		ConnectedMiners:      int(stratumStats.ActiveConnections),
		PoolHashrate:         hashrate,
		SharesPerSecond:      sharesPerSecond,
		NetworkDifficulty:    networkDiff,
		BlockHeight:          bcInfo.Blocks,
		LastNetworkBlockTime: time.Unix(bcInfo.MedianTime, 0),
	}

	// Cache stats for StatsProvider interface
	// Only update hashrate cache if DB query succeeded — prevents caching false zeros
	p.statsMu.Lock()
	p.cachedBlockHeight = bcInfo.Blocks
	p.cachedNetworkDifficulty = networkDiff
	if err == nil {
		p.cachedHashrate = hashrate
	}
	p.cachedSharesPerSecond = sharesPerSecond

	// Network hashrate: difficulty * 2^32 / algo_block_time
	algoBlockTime := getAlgoBlockTime(p.cfg.Pool.Coin)
	if networkDiff > 0 && algoBlockTime > 0 {
		p.cachedNetworkHashrate = networkDiff * math.Pow(2, 32) / algoBlockTime
	}

	// Block reward from current job template (satoshis -> coins)
	if job := p.jobManager.GetCurrentJob(); job != nil {
		p.cachedBlockReward = float64(job.CoinbaseValue) / 1e8
	}

	// Pool effort: (actual_time / expected_time) * 100%
	// expected_time = netDiff * 2^32 / poolHashrate (seconds to find one block)
	if !p.lastBlockFoundAt.IsZero() && networkDiff > 0 && p.cachedHashrate > 0 {
		timeSinceBlock := time.Since(p.lastBlockFoundAt).Seconds()
		expectedTime := (networkDiff * math.Pow(2, 32)) / p.cachedHashrate
		p.cachedPoolEffort = (timeSinceBlock / expectedTime) * 100
	}

	p.statsMu.Unlock()

	// Update Prometheus metrics — only update if DB query succeeded
	if p.metricsServer != nil && err == nil {
		p.metricsServer.UpdateHashrate(hashrate)
	}

	if err := p.db.UpdatePoolStats(ctx, stats); err != nil {
		p.logger.Warnw("Failed to update pool stats", "error", err)
	}
}

// difficultyLoop periodically updates the network difficulty for share validation.
func (p *Pool) difficultyLoop(ctx context.Context) {
	defer p.wg.Done()

	// Fetch immediately on startup (don't wait 30 seconds)
	diff, err := p.daemonClient.GetDifficulty(ctx)
	if err != nil {
		p.logger.Warnw("Failed to get initial network difficulty", "error", err)
	} else {
		p.shareValidator.SetNetworkDifficulty(diff)
		p.logger.Infow("Initial network difficulty set", "difficulty", diff, "algo", "sha256d")
		if p.metricsServer != nil {
			p.metricsServer.UpdateNetworkInfo(diff, 0)
		}
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// V37 FIX: If daemon RPC just recovered from degraded/critical failure,
			// force immediate stats refresh to invalidate stale pre-outage cached data.
			// Without this, cached network difficulty and stats would remain stale for
			// up to 60 seconds (statsLoop interval) after daemon recovery.
			if p.jobManager.DrainRPCRecovery() {
				p.logger.Infow("V37: RPC recovery detected — forcing immediate stats + difficulty refresh")
				p.updateStats(ctx)
			}

			diff, err := p.daemonClient.GetDifficulty(ctx)
			if err != nil {
				p.logger.Warnw("Failed to get network difficulty", "error", err)
				continue
			}
			p.shareValidator.SetNetworkDifficulty(diff)
			p.logger.Debugw("Network difficulty updated", "difficulty", diff)

			// Update Prometheus metric for dashboard/API access
			if p.metricsServer != nil {
				p.metricsServer.UpdateNetworkInfo(diff, 0) // Network hashrate updated separately
			}
		}
	}
}

// sessionCleanupLoop periodically cleans up orphaned VARDIFF session states.
// H-4 fix: This handles cases where miners disconnect abruptly (network failure,
// power loss, etc.) without triggering the disconnect handler, which would leave
// stale entries in the sessionStates map consuming memory.
func (p *Pool) sessionCleanupLoop(ctx context.Context) {
	defer p.wg.Done()

	// Run cleanup every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Stale session threshold: 30 minutes without any share activity
	const staleThreshold = 30 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.cleanupStaleSessions(staleThreshold)
			// Evict stale nonce tracking entries for uncleanly disconnected sessions
			if p.shareValidator != nil {
				p.shareValidator.CleanupNonceTracker()
			}
		}
	}
}

// cleanupStaleSessions removes VARDIFF states for sessions that haven't
// submitted shares recently and are no longer actively connected.
func (p *Pool) cleanupStaleSessions(staleThreshold time.Duration) {
	now := time.Now()
	var staleCount, activeCount int

	// Get list of currently active session IDs from stratum server
	activeSessions := p.stratumServer.GetActiveSessionIDs()
	activeSet := make(map[uint64]bool, len(activeSessions))
	for _, id := range activeSessions {
		activeSet[id] = true
	}

	// Iterate through all session states
	p.sessionStates.Range(func(key, value interface{}) bool {
		sessionID := key.(uint64)
		state := value.(*vardiff.SessionState)

		// Skip if session is still actively connected
		if activeSet[sessionID] {
			activeCount++
			return true
		}

		// Check if session is stale (no shares for staleThreshold duration)
		lastShareTime := time.Unix(0, state.LastShareNano())
		if now.Sub(lastShareTime) > staleThreshold {
			p.sessionStates.Delete(sessionID)
			atomic.AddInt64(&p.sessionStateCount, -1)
			staleCount++
		}

		return true
	})

	if staleCount > 0 {
		p.logger.Infow("Cleaned up stale VARDIFF session states",
			"staleRemoved", staleCount,
			"activeRemaining", activeCount,
		)
	}
}

// Stats returns current pool statistics.
func (p *Pool) Stats() PoolStats {
	stratumStats := p.stratumServer.Stats()
	pipelineStats := p.sharePipeline.Stats()
	validatorStats := p.shareValidator.Stats()

	return PoolStats{
		Connections:    stratumStats.ActiveConnections,
		TotalShares:    validatorStats.Validated,
		AcceptedShares: validatorStats.Accepted,
		RejectedShares: validatorStats.Rejected,
		SharesInBuffer: pipelineStats.BufferCurrent,
		SharesWritten:  pipelineStats.Written,
	}
}

// PoolStats represents current pool statistics.
type PoolStats struct {
	Connections    int64
	TotalShares    uint64
	AcceptedShares uint64
	RejectedShares uint64
	SharesInBuffer int
	SharesWritten  uint64
}

// StatsProvider interface implementation for API server.
// These methods provide real-time stats for the REST API.

// GetConnections returns the number of active stratum connections.
func (p *Pool) GetConnections() int64 {
	return p.stratumServer.Stats().ActiveConnections
}

// GetHashrate returns the current pool hashrate (from cached stats).
func (p *Pool) GetHashrate() float64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedHashrate
}

// GetSharesPerSecond returns the current shares per second rate.
func (p *Pool) GetSharesPerSecond() float64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedSharesPerSecond
}

// GetBlockHeight returns the current blockchain height.
func (p *Pool) GetBlockHeight() uint64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedBlockHeight
}

// GetNetworkDifficulty returns the current network difficulty.
func (p *Pool) GetNetworkDifficulty() float64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedNetworkDifficulty
}

// GetNetworkHashrate returns the computed network hashrate.
func (p *Pool) GetNetworkHashrate() float64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedNetworkHashrate
}

// GetBlocksFound returns the total number of blocks found by the pool.
func (p *Pool) GetBlocksFound() int64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedBlocksFound
}

// GetBlockReward returns the current block reward in coins.
func (p *Pool) GetBlockReward() float64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedBlockReward
}

// GetPoolEffort returns the current mining effort percentage.
func (p *Pool) GetPoolEffort() float64 {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.cachedPoolEffort
}

// getAlgoBlockTime returns the per-algorithm target block time in seconds.
// For multi-algo coins like DGB (5 algos, 15s chain time), this returns
// the per-algo time (75s) since difficulty is calibrated per-algorithm.
func getAlgoBlockTime(symbol string) float64 {
	switch strings.ToUpper(symbol) {
	case "DGB", "DGB-SCRYPT":
		return 75 // 15s chain time * 5 algorithms
	case "BTC", "BCH", "BC2", "NMC", "CAT":
		return 600
	case "SYS", "LTC":
		return 150
	case "XMY", "DOGE", "PEP":
		return 60
	case "FBTC":
		return 30
	default:
		return 600
	}
}

// initBlockStats initializes block statistics from the database on startup.
// This ensures blocksFound and pool effort are accurate across restarts.
func (p *Pool) initBlockStats(ctx context.Context) {
	postgresDB, ok := p.db.(*database.PostgresDB)
	if !ok {
		return
	}

	// Load total blocks found
	blockStats, err := postgresDB.GetBlockStats(ctx)
	if err != nil {
		p.logger.Warnw("Failed to load block stats on startup", "error", err)
	} else {
		total := int64(blockStats.Pending + blockStats.Confirmed + blockStats.Orphaned + blockStats.Paid)
		p.statsMu.Lock()
		p.cachedBlocksFound = total
		p.statsMu.Unlock()
		p.logger.Infow("Loaded block stats from database", "total", total,
			"pending", blockStats.Pending, "confirmed", blockStats.Confirmed,
			"orphaned", blockStats.Orphaned, "paid", blockStats.Paid)
	}

	// Load last block found time for effort calculation
	lastTime, err := postgresDB.GetLastBlockFoundTime(ctx)
	if err != nil {
		p.logger.Debugw("No previous blocks found in database (effort starts from 0)")
	} else {
		p.statsMu.Lock()
		p.lastBlockFoundAt = lastTime
		p.statsMu.Unlock()
		p.logger.Infow("Loaded last block time from database", "lastBlock", lastTime)
	}
}

// cleanupStaleShares removes old shares from the database on startup.
// This prevents inflated hashrate calculations from stale data.
func (p *Pool) cleanupStaleShares(ctx context.Context) error {
	// Clean up shares older than 15 minutes (1.5x the hashrate window)
	// This is generous to avoid losing recent valid data while removing stale shares
	retentionMinutes := 15

	var deleted int64
	var err error

	// Use HA manager if available, otherwise use direct DB
	if p.dbManager != nil {
		deleted, err = p.dbManager.CleanupStaleShares(ctx, retentionMinutes)
	} else if postgresDB, ok := p.db.(*database.PostgresDB); ok {
		deleted, err = postgresDB.CleanupStaleShares(ctx, retentionMinutes)
	} else {
		p.logger.Debug("Skipping stale share cleanup (database type not supported)")
		return nil
	}

	if err != nil {
		return err
	}

	if deleted > 0 {
		p.logger.Infow("Cleaned up stale shares from previous session",
			"deleted", deleted,
			"retention_minutes", retentionMinutes,
		)
	}

	return nil
}

// startPollingFallback starts RPC polling for block notifications when ZMQ fails.
// CRITICAL FIX: Uses mutex to prevent race conditions and double-start
func (p *Pool) startPollingFallback() {
	p.pollingMu.Lock()
	defer p.pollingMu.Unlock()

	if p.usePolling {
		return // Already polling
	}

	p.usePolling = true
	p.pollingStopCh = make(chan struct{})
	// ORPHAN FIX #1: Reduced polling interval from 1s to 250ms
	// A 1-second polling window created a race condition where:
	// - Miner finds block at T=0
	// - Network finds competing block at T=0.5s
	// - Pool detects new block at T=1s (too late)
	// - Pool submits block with stale prevhash → "prev-blk-not-found"
	// Reducing to 250ms shrinks this race window by 75%
	p.pollingTicker = time.NewTicker(250 * time.Millisecond)

	p.wg.Add(1)
	go p.pollingLoop()

	p.logger.Info("Started RPC polling fallback for block notifications (250ms interval)")
}

// stopPollingFallback stops the RPC polling fallback.
// CRITICAL FIX: Uses mutex to prevent race conditions and double-close panic
func (p *Pool) stopPollingFallback() {
	p.pollingMu.Lock()
	defer p.pollingMu.Unlock()

	if !p.usePolling {
		return
	}

	p.usePolling = false

	// CRITICAL FIX: Only close if channel exists and is not nil
	// This prevents panic from double-close
	if p.pollingStopCh != nil {
		close(p.pollingStopCh)
		p.pollingStopCh = nil // Prevent double-close
	}

	if p.pollingTicker != nil {
		p.pollingTicker.Stop()
		p.pollingTicker = nil
	}

	p.logger.Info("Stopped RPC polling fallback")
}

// pollingLoop polls the daemon for new blocks when ZMQ is unavailable.
//
// FIX: Daemon recovery detection. When RPC transitions from failed→success
// (e.g., daemon restart), force an immediate template refresh regardless of
// whether the block hash changed. Without this, a daemon restart at the same
// height leaves the pool serving stale work indefinitely.
func (p *Pool) pollingLoop() {
	defer p.wg.Done()

	var lastBlockHash string
	rpcWasDown := false // Track daemon connectivity for recovery detection

	// RACE FIX: Capture both ticker and stop channel locally under the mutex.
	// stopPollingFallback() closes pollingStopCh then sets both fields to nil.
	// Without local capture, the select re-reads p.pollingStopCh each iteration;
	// if it reads nil (after stopPollingFallback nils the field), the nil channel
	// case blocks forever → goroutine leak. Local copies keep the original channel
	// object alive so close() still signals this goroutine.
	p.pollingMu.Lock()
	ticker := p.pollingTicker
	stopCh := p.pollingStopCh
	p.pollingMu.Unlock()

	if ticker == nil || stopCh == nil {
		return
	}

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

			// Get current best block hash
			bcInfo, err := p.daemonClient.GetBlockchainInfo(ctx)
			cancel()

			if err != nil {
				if !rpcWasDown {
					p.logger.Warnw("Polling: daemon RPC unreachable", "error", err)
				}
				rpcWasDown = true
				continue
			}

			// FIX: Daemon recovery — force template refresh on RPC reconnection.
			// When daemon restarts at the same height, BestBlockHash is unchanged
			// so the hash comparison below won't trigger. Force a refresh to ensure
			// miners get fresh work immediately after daemon recovery.
			if rpcWasDown {
				hashPreview := bcInfo.BestBlockHash
				if len(hashPreview) > 16 {
					hashPreview = hashPreview[:16] + "..."
				}
				p.logger.Infow("Polling: daemon RPC recovered — forcing template refresh",
					"height", bcInfo.Blocks,
					"hash", hashPreview,
				)
				rpcWasDown = false
				lastBlockHash = bcInfo.BestBlockHash

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := p.jobManager.RefreshJob(ctx, true); err != nil {
					p.logger.Warnw("Polling: forced refresh after recovery failed", "error", err)
				}
				cancel()
				continue
			}

			// Check if block changed
			if bcInfo.BestBlockHash != lastBlockHash && lastBlockHash != "" {
				// Safe hash preview for logging
				hashPreview := bcInfo.BestBlockHash
				if len(hashPreview) > 16 {
					hashPreview = hashPreview[:16] + "..."
				}
				p.logger.Infow("Polling: detected new block",
					"height", bcInfo.Blocks,
					"hash", hashPreview,
				)

				// Trigger job refresh with 10 second timeout - faster is better for block notifications
				// CRITICAL: 30 seconds was too long and could cause stale work
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				p.jobManager.OnBlockNotification(ctx)
				cancel()
			}

			lastBlockHash = bcInfo.BestBlockHash
		}
	}
}

// initBlockNotifications sets up block notifications with RPC polling as primary.
// Strategy: Always start with RPC polling (reliable, works immediately).
// ZMQ is started in background and monitored - once stable for 5 minutes,
// it takes over as primary and polling is disabled.
// This approach handles daemon warmup (10-15 min) gracefully and avoids
// any timing-sensitive checks that could cause premature failures.
func (p *Pool) initBlockNotifications(ctx context.Context) error {
	// ALWAYS start with RPC polling - it's reliable and works immediately
	p.logger.Info("Starting RPC polling for block notifications (primary)")
	p.startPollingFallback()

	// If ZMQ is disabled, we're done
	if p.zmqListener == nil {
		p.logger.Info("ZMQ disabled in config, using RPC polling only")
		return nil
	}

	// Start ZMQ in background - it will be tested and promoted when ready
	p.logger.Info("Initializing ZMQ for future promotion...")
	p.wg.Add(1)
	go p.zmqPromotionLoop(ctx)

	p.logger.Infow("Block notification strategy: RPC polling (primary), ZMQ testing in background",
		"pollingInterval", "1s",
		"zmqEndpoint", p.cfg.Daemon.ZMQ.Endpoint,
		"zmqTestInterval", "1m",
		"zmqStabilityThreshold", "5m",
	)

	return nil
}

// zmqPromotionLoop tests ZMQ and promotes it when stable.
// This runs in background and handles daemon warmup gracefully.
// ORPHAN FIX #2: Accelerated from 10s to 2s for faster failover detection.
// When ZMQ fails, every second of delay before falling back to RPC polling
// is another second where block notifications can be missed, leading to
// stale jobs being mined and orphaned blocks on submission.
func (p *Pool) zmqPromotionLoop(ctx context.Context) {
	defer p.wg.Done()

	// ORPHAN FIX #2: Reduced from 10s to 2s for faster ZMQ failure detection
	// and quicker promotion when ZMQ becomes available
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	zmqWasDown := false // Track ZMQ failure state for recovery detection

	// Helper function to check and start ZMQ
	// CRITICAL FIX: Uses mutex for thread-safe access to polling state
	checkZMQ := func() {
		// Check if already promoted (with mutex)
		p.pollingMu.Lock()
		isPromoted := p.zmqPromoted
		isPolling := p.usePolling
		p.pollingMu.Unlock()

		if isPromoted {
			// Already promoted, monitor for failures
			if p.zmqListener.IsFailed() {
				zmqWasDown = true
				p.logger.Warn("ZMQ failed, falling back to RPC polling")
				p.pollingMu.Lock()
				p.zmqPromoted = false
				needPolling := !p.usePolling
				p.pollingMu.Unlock()
				if needPolling {
					p.startPollingFallback()
				}
				// FIX: Force template refresh on ZMQ→polling fallback.
				// ZMQ failure means we may have missed block notifications.
				// Refresh immediately so miners don't get stale work.
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := p.jobManager.RefreshJob(refreshCtx, true); err != nil {
					p.logger.Warnw("Failed to refresh template after ZMQ fallback", "error", err)
				} else {
					p.logger.Info("Template refreshed after ZMQ→polling fallback")
				}
				refreshCancel()
			}
			return
		}

		// Not yet promoted - try to start/check ZMQ
		if !p.zmqListener.IsRunning() {
			p.logger.Info("Testing ZMQ connection...")
			if err := p.zmqListener.TestConnection(10 * time.Second); err != nil {
				p.logger.Debugw("ZMQ not ready yet", "error", err)
				return
			}

			p.logger.Info("ZMQ connection successful, starting listener...")
			if err := p.zmqListener.Start(ctx); err != nil {
				p.logger.Warnw("Failed to start ZMQ listener", "error", err)
				return
			}
			p.logger.Info("ZMQ listener started, monitoring for stability...")
		}

		// Check if ZMQ has proven stable (5 minutes of healthy operation)
		stats := p.zmqListener.Stats()
		if stats.StabilityReached {
			p.logger.Infow("ZMQ stability confirmed! Promoting to primary, disabling RPC polling",
				"healthyDuration", stats.HealthyDuration.Round(time.Second),
				"messagesReceived", stats.MessagesReceived,
			)
			p.pollingMu.Lock()
			p.zmqPromoted = true
			p.pollingMu.Unlock()
			p.stopPollingFallback()
			if zmqWasDown {
				zmqWasDown = false
				p.logger.Info("ZMQ recovered from failure — forcing template refresh")
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := p.jobManager.RefreshJob(refreshCtx, true); err != nil {
					p.logger.Warnw("Failed to refresh template after ZMQ recovery", "error", err)
				}
				refreshCancel()
			}
			p.logger.Info("Block notifications now using ZMQ (low-latency mode)")
		} else if stats.HealthyDuration > 0 {
			p.logger.Debugw("ZMQ healthy but not yet stable",
				"healthyDuration", stats.HealthyDuration.Round(time.Second),
				"stabilityThreshold", "5m",
			)
		}
		_ = isPolling // Silence unused variable warning
	}

	// CRITICAL: Try immediately on startup - don't wait for first tick
	checkZMQ()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkZMQ()
		}
	}
}

// contains checks if s contains substr (case-insensitive helper for error checking)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(substr) == 0 ||
		(len(s) > 0 && containsLower(strings.ToLower(s), strings.ToLower(substr))))
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// isPermanentRejection checks if a block rejection error is permanent (not worth retrying).
// BIP22 defines standard rejection reasons that indicate the block will never be accepted.
// Reference: https://github.com/bitcoin/bips/blob/master/bip-0022.mediawiki
func isPermanentRejection(errStr string) bool {
	errLower := strings.ToLower(errStr)

	// Permanent rejection patterns from BIP22 and coin-specific implementations
	permanentPatterns := []string{
		// Stale/timing - block is outdated
		"prev-blk-not-found", "bad-prevblk", "stale-prevblk", "stale-work", "stale",
		"time-too-old", "time-too-new", "time-invalid",

		// Duplicate - already accepted
		"duplicate", "already",

		// PoW validation failures
		"high-hash", "bad-diffbits",

		// Coinbase errors (BIP22)
		"bad-cb-missing", "bad-cb-multiple", "bad-cb-height", "bad-cb-length",
		"bad-cb-prefix", "bad-cb-flag",

		// Merkle/transaction errors
		"bad-txnmrklroot", "bad-txns", "bad-txns-nonfinal",

		// Block structure errors
		"bad-version", "bad-blk-sigops",

		// Witness errors (SegWit)
		"bad-witness-nonce-size", "bad-witness-merkle-match",

		// Work/identity errors
		"unknown-work", "unknown-user",

		// DigiByte-specific
		"invalidchainfound", "setbestchaininner", "addtoblockindex",

		// Generic rejections
		"rejected", "invalid", "block-validation-failed",

		// Catch-all for any "bad-" prefix errors
		"bad-",
	}

	for _, pattern := range permanentPatterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}

	// "inconclusive" is a special case - the network couldn't determine validity
	// This is permanent because the block state is unknown and retrying won't help
	if strings.Contains(errLower, "inconclusive") {
		return true
	}

	return false
}

// sanitizeDaemonError strips control characters from daemon error messages.
// Defense-in-depth against CWE-117 (log injection), though zap's structured
// logging already provides protection via JSON encoding.
func sanitizeDaemonError(s string) string {
	// Replace control characters (0x00-0x1F, 0x7F) with space
	// This prevents log injection via newlines, carriage returns, etc.
	var result strings.Builder
	result.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			result.WriteRune(' ')
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// classifyRejection returns a human-readable reason for block rejection.
// Covers BIP22 standard reasons plus coin-specific extensions (DGB, BCH).
// Reference: https://github.com/bitcoin/bips/blob/master/bip-0022.mediawiki
func classifyRejection(errStr string) string {
	errLower := strings.ToLower(sanitizeDaemonError(errStr))
	switch {
	// === Stale/timing-related (most common for solo mining) ===
	case strings.Contains(errLower, "prev-blk-not-found") || strings.Contains(errLower, "bad-prevblk"):
		return "Stale job - parent block no longer exists (another miner found a block first)"
	case strings.Contains(errLower, "stale-prevblk"):
		return "Previous block no longer chain tip (stale)"
	case strings.Contains(errLower, "stale-work"):
		return "Work is no longer valid (job expired)"
	case strings.Contains(errLower, "time-too-old") || strings.Contains(errLower, "time-invalid"):
		return "Block timestamp too old (stale job)"
	case strings.Contains(errLower, "time-too-new"):
		return "Block timestamp too far in future (clock sync issue)"

	// === Duplicate ===
	case strings.Contains(errLower, "duplicate"):
		return "Block already accepted by network"

	// === PoW validation ===
	case strings.Contains(errLower, "high-hash"):
		return "Hash doesn't meet network difficulty (should never happen - bug in difficulty calc)"
	case strings.Contains(errLower, "bad-diffbits"):
		return "Wrong difficulty bits (bug in template handling)"

	// === Coinbase errors (BIP22) ===
	case strings.Contains(errLower, "bad-cb-missing"):
		return "Missing coinbase transaction (bug in block construction)"
	case strings.Contains(errLower, "bad-cb-multiple"):
		return "Multiple coinbase transactions (bug in block construction)"
	case strings.Contains(errLower, "bad-cb-height"):
		return "Wrong height in coinbase (BIP34 encoding bug)"
	case strings.Contains(errLower, "bad-cb-length"):
		return "Coinbase script too long (exceeds 100 bytes)"
	case strings.Contains(errLower, "bad-cb-prefix"):
		return "Coinbase modified beyond allowed appending"
	case strings.Contains(errLower, "bad-cb-flag"):
		return "Disallowed feature-signaling flag in coinbase"

	// === Merkle/transaction errors ===
	case strings.Contains(errLower, "bad-txnmrklroot"):
		return "Merkle root mismatch (bug in merkle calculation)"
	case strings.Contains(errLower, "bad-txns"):
		return "Invalid transaction content"
	case strings.Contains(errLower, "bad-txns-nonfinal"):
		return "Non-final transaction in block"

	// === Block structure errors ===
	case strings.Contains(errLower, "bad-version"):
		return "Block version incorrect"
	case strings.Contains(errLower, "bad-blk-sigops"):
		return "Too many signature operations in block"

	// === Witness errors (SegWit) ===
	case strings.Contains(errLower, "bad-witness-nonce-size"):
		return "Invalid witness nonce size"
	case strings.Contains(errLower, "bad-witness-merkle-match"):
		return "Witness merkle root mismatch"

	// === Work/identity errors ===
	case strings.Contains(errLower, "unknown-work"):
		return "Unknown work/template ID"
	case strings.Contains(errLower, "unknown-user"):
		return "Unknown submitting user"

	// === DigiByte-specific ===
	case strings.Contains(errLower, "invalidchainfound"):
		return "DigiByte: Invalid chain found (stale/orphan)"
	case strings.Contains(errLower, "setbestchaininner failed"):
		return "DigiByte: Chain reorganization in progress"
	case strings.Contains(errLower, "addtoblockindex failed"):
		return "DigiByte: Failed to add block to index"

	// === Generic/fallback ===
	case strings.Contains(errLower, "stale"):
		return "Block is stale"
	case strings.Contains(errLower, "inconclusive"):
		return "Network couldn't determine validity (try resubmitting)"
	case strings.Contains(errLower, "rejected"):
		return "Block rejected (no specific reason given)"
	case strings.Contains(errLower, "block-validation-failed"):
		return "Block failed validation checks"
	default:
		return "Unknown rejection reason"
	}
}

// classifyRejectionMetric returns a short, stable label for Prometheus metrics.
// Unlike classifyRejection (human-readable), these labels must never change
// to avoid breaking Grafana dashboards and alerts.
func classifyRejectionMetric(errStr string) string {
	errLower := strings.ToLower(errStr)
	switch {
	case strings.Contains(errLower, "prev-blk-not-found") || strings.Contains(errLower, "bad-prevblk") || strings.Contains(errLower, "stale"):
		return "stale"
	case strings.Contains(errLower, "high-hash") || strings.Contains(errLower, "bad-diffbits"):
		return "high_hash"
	case strings.Contains(errLower, "duplicate") || strings.Contains(errLower, "already"):
		return "duplicate"
	case strings.Contains(errLower, "bad-cb") || strings.Contains(errLower, "bad-txnmrklroot") || strings.Contains(errLower, "bad-txns"):
		return "invalid_block"
	case strings.Contains(errLower, "timeout") || strings.Contains(errLower, "context deadline"):
		return "timeout"
	default:
		return "unknown"
	}
}

// waitForSync blocks until the daemon is fully synced with the network.
// This is a critical safety gate - miners should NEVER receive jobs from an unsynced node
// as their shares would be wasted on stale blocks.
//
// Sync criteria:
// - InitialBlockDownload must be false (not in IBD mode)
// - VerificationProgress must be >= 0.990 (99.0% synced) - lowered to prevent floating point drift on smaller chains
//
// NOTE: After improper shutdown, nodes may need to resync which can take 1+ hours
// depending on disk speed (HDD vs SSD). This function waits indefinitely but logs
// warnings after extended periods to help operators diagnose issues.
func (p *Pool) waitForSync(ctx context.Context) error {
	const (
		syncThreshold = 0.990 // 99.0% - realistic threshold that accounts for verificationProgress drift
		checkInterval = 10 * time.Second
		warnAfter     = 30 * time.Minute  // Warn after 30 minutes
		warnInterval  = 15 * time.Minute  // Repeat warning every 15 minutes
	)

	p.logger.Info("SYNC GATE: Waiting for daemon to complete synchronization...")
	p.logger.Infow("Sync requirements",
		"ibdMustBeFalse", true,
		"minVerificationProgress", syncThreshold,
	)

	startTime := time.Now()
	lastWarnTime := time.Time{}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			elapsed := time.Since(startTime)

			// Warn if taking too long (doesn't fail - keeps waiting)
			// This helps operators know something may be wrong without crashing the pool
			if elapsed > warnAfter && (lastWarnTime.IsZero() || time.Since(lastWarnTime) > warnInterval) {
				p.logger.Warnw("⚠️ SYNC GATE: Still waiting for node to sync - this is normal after improper shutdown",
					"elapsed", elapsed.Round(time.Minute),
					"hint", "Node may be reindexing/resyncing. HDD systems can take 1+ hours. Check node logs for progress.",
				)
				lastWarnTime = time.Now()
			}

			// Query daemon sync status
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			bcInfo, err := p.daemonClient.GetBlockchainInfo(checkCtx)
			cancel()

			if err != nil {
				p.logger.Warnw("Sync check failed, retrying...",
					"error", err,
					"elapsed", elapsed.Round(time.Second),
					"hint", "Node may still be starting up or RPC not ready yet",
				)
				continue
			}

			// Log progress
			p.logger.Infow("Sync status",
				"blocks", bcInfo.Blocks,
				"headers", bcInfo.Headers,
				"progress", fmt.Sprintf("%.4f%%", bcInfo.VerificationProgress*100),
				"ibd", bcInfo.InitialBlockDownload,
				"elapsed", elapsed.Round(time.Second),
			)

			// Check sync criteria
			// On regtest, skip IBD check entirely — regtest is a private test network
			// with no peers, so there's no risk of mining on stale blocks. This allows
			// the pool to start on a fresh chain (height 0) where IBD is always true.
			if bcInfo.Chain == "regtest" || (!bcInfo.InitialBlockDownload && bcInfo.VerificationProgress >= syncThreshold) {
				p.logger.Infow("✅ SYNC GATE PASSED: Daemon is fully synced",
					"blocks", bcInfo.Blocks,
					"progress", fmt.Sprintf("%.4f%%", bcInfo.VerificationProgress*100),
					"chain", bcInfo.Chain,
					"waitTime", elapsed.Round(time.Second),
				)
				return nil
			}

			// Still syncing - show helpful status with ETA estimate
			if bcInfo.Headers > 0 && bcInfo.Blocks < bcInfo.Headers {
				blocksRemaining := bcInfo.Headers - bcInfo.Blocks
				// Estimate time remaining based on progress so far
				var etaStr string
				if bcInfo.Blocks > 0 && elapsed.Seconds() > 60 {
					blocksPerSecond := float64(bcInfo.Blocks) / elapsed.Seconds()
					if blocksPerSecond > 0 {
						etaSeconds := float64(blocksRemaining) / blocksPerSecond
						etaStr = (time.Duration(etaSeconds) * time.Second).Round(time.Minute).String()
					}
				}
				p.logger.Infow("Still syncing...",
					"blocksRemaining", blocksRemaining,
					"percentComplete", fmt.Sprintf("%.2f%%", bcInfo.VerificationProgress*100),
					"estimatedTimeRemaining", etaStr,
				)
			}
		}
	}
}

// GetProfiles implements api.RouterProfilesProvider.
// Returns Spiral Router difficulty profiles for API exposure.
func (p *Pool) GetProfiles() []api.RouterProfile {
	if p.stratumServer == nil {
		return nil
	}

	stratumProfiles := p.stratumServer.GetRouterProfiles()
	result := make([]api.RouterProfile, len(stratumProfiles))
	for i, sp := range stratumProfiles {
		result[i] = api.RouterProfile{
			Class:           sp.Class,
			InitialDiff:     sp.InitialDiff,
			MinDiff:         sp.MinDiff,
			MaxDiff:         sp.MaxDiff,
			TargetShareTime: sp.TargetShareTime,
		}
	}
	return result
}

// GetWorkersByClass implements api.RouterProfilesProvider.
// Returns count of connected workers by their detected miner class.
func (p *Pool) GetWorkersByClass() map[string]int {
	if p.stratumServer == nil {
		return nil
	}
	return p.stratumServer.GetWorkersByClass()
}

// GetPipelineStats implements api.PipelineStatsProvider.
// Returns share pipeline statistics for API exposure.
func (p *Pool) GetPipelineStats() api.PipelineStats {
	if p.sharePipeline == nil {
		return api.PipelineStats{}
	}

	stats := p.sharePipeline.Stats()
	return api.PipelineStats{
		Processed:      stats.Processed,
		Written:        stats.Written,
		Dropped:        stats.Dropped,
		BufferCurrent:  stats.BufferCurrent,
		BufferCapacity: stats.BufferCapacity,
	}
}

// GetPaymentStats implements api.PaymentStatsProvider.
// Returns payment and block maturity statistics.
func (p *Pool) GetPaymentStats() (*api.PaymentStats, error) {
	// Get database connection - need PostgresDB for GetBlockStats
	postgresDB, ok := p.db.(*database.PostgresDB)
	if !ok {
		return &api.PaymentStats{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	blockStats, err := postgresDB.GetBlockStats(ctx)
	if err != nil {
		return nil, err
	}

	// Get block maturity from config (default 100)
	blockMaturity := 100
	if p.cfg.Payments.BlockMaturity > 0 {
		blockMaturity = p.cfg.Payments.BlockMaturity
	}

	return &api.PaymentStats{
		PendingBlocks:   blockStats.Pending,
		ConfirmedBlocks: blockStats.Confirmed,
		OrphanedBlocks:  blockStats.Orphaned,
		PaidBlocks:      blockStats.Paid,
		BlockMaturity:   blockMaturity,
		TotalPaid:       0, // FUTURE: Sum from payments table
	}, nil
}

// GetActiveConnections implements api.ConnectionStatsProvider.
// Returns real-time connection status for all active workers.
// SECURITY: This endpoint is protected by adminAuthMiddleware (API key required).
// Full IP addresses are returned since admin access is already authenticated —
// masking would break Sentinel's ESP32 device matching (pool-based monitoring
// for devices without HTTP API relies on IP correlation).
func (p *Pool) GetActiveConnections() []api.WorkerConnection {
	sessions := p.stratumServer.GetActiveConnections()
	connections := make([]api.WorkerConnection, 0, len(sessions))

	for _, s := range sessions {
		connections = append(connections, api.WorkerConnection{
			SessionID:    s.ID,
			WorkerName:   s.WorkerName,
			MinerAddress: s.MinerAddress,
			UserAgent:    s.UserAgent,
			RemoteAddr:   s.RemoteAddr,
			ConnectedAt:  s.ConnectedAt,
			LastActivity: s.GetLastActivity(), // SECURITY: Use atomic accessor
			Difficulty:   s.GetDifficulty(),
			ShareCount:   s.GetShareCount(), // SECURITY: Use atomic accessor
		})
	}

	return connections
}

// maskIPAddress masks an IP address for privacy protection.
// For IPv4: 192.168.1.100:3333 -> 192.168.1.xxx:3333
// For IPv6: [2001:db8::1]:3333 -> [2001:db8::xxx]:3333
// SECURITY: Prevents full IP exposure while allowing subnet identification.
func maskIPAddress(addr string) string {
	if addr == "" {
		return ""
	}

	// Handle IPv4 with port (host:port)
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		host := addr[:idx]
		port := addr[idx:]

		// Check if this is IPv6 (starts and ends with brackets)
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			// IPv6: [2001:db8::1] -> [2001:db8::xxx]
			inner := host[1 : len(host)-1]
			// Find last segment
			if lastColon := strings.LastIndex(inner, ":"); lastColon != -1 {
				return "[" + inner[:lastColon] + ":xxx]" + port
			}
			return host + port
		}

		// IPv4: 192.168.1.100 -> 192.168.1.xxx
		if lastDot := strings.LastIndex(host, "."); lastDot != -1 {
			return host[:lastDot] + ".xxx" + port
		}
		return host + port
	}

	// No port - just mask the IP
	if lastDot := strings.LastIndex(addr, "."); lastDot != -1 {
		return addr[:lastDot] + ".xxx"
	}
	return addr
}
