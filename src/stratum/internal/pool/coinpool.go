// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - CoinPool provides per-coin pool instance isolation for V2.
//
// CoinPool wraps all coin-specific mining operations: stratum server, job manager,
// share pipeline, vardiff, and node management. Each coin runs independently
// with its own connections, jobs, and failover logic.
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

	"github.com/spiralpool/stratum/internal/auxpow"
	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/daemon"
	"github.com/spiralpool/stratum/internal/database"
	"github.com/spiralpool/stratum/internal/ha"
	"github.com/spiralpool/stratum/internal/jobs"
	"github.com/spiralpool/stratum/internal/metrics"
	"github.com/spiralpool/stratum/internal/nodemanager"
	"github.com/spiralpool/stratum/internal/shares"
	"github.com/spiralpool/stratum/internal/stratum"
	stratumv2 "github.com/spiralpool/stratum/internal/stratum/v2"
	"github.com/spiralpool/stratum/internal/vardiff"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// CoinPool manages mining operations for a single coin.
// It provides complete isolation between coins in a multi-coin pool setup.
type CoinPool struct {
	cfg    *config.CoinPoolConfig
	coin   coin.Coin
	logger *zap.SugaredLogger

	// Pool identification
	poolID     string
	coinSymbol string

	// Core components
	stratumServer  *stratum.Server
	jobManager     coinPoolJobManager
	sharePipeline  *shares.Pipeline
	shareValidator *shares.ValidatorV2
	vardiffEngine  *vardiff.Engine
	nodeManager    coinPoolNodeManager
	db             coinPoolDB

	// Shared metrics server (from coordinator)
	metricsServer *metrics.Metrics

	// Coin-aware block submission timeouts
	submitTimeouts *daemon.SubmitTimeouts

	// Merge mining (AuxPoW) support
	auxManager   *auxpow.Manager   // Optional: manages aux chain templates
	auxSubmitter *auxpow.Submitter // Optional: submits found aux blocks

	// Block write-ahead log for crash recovery (per-coin isolation)
	// Maintains a durable record to prevent block loss
	blockWAL *BlockWAL

	// Dedicated non-sampled block logger (per-coin)
	// Standard zap sampling can drop logs under load - block events are too critical
	blockLogger *BlockLogger

	// HA components (optional - set by Coordinator when HA is enabled)
	redisDedupTracker *shares.RedisDedupTracker // Redis dedup tracker for HA block dedup

	// Sync status callback (optional - set by Coordinator for HA VIP awareness)
	// Called with (coinSymbol, syncPct, blockHeight) when sync status changes.
	// This allows the VIPManager to track which coins are synced for master election.
	onSyncStatusChange func(coin string, syncPct float64, height int64)

	// Session VARDIFF state
	sessionStates     sync.Map // map[uint64]*vardiff.SessionState
	sessionStateCount int64    // Track count for bounded growth (atomic)

	// Block notification mode (same strategy as V1)
	// CRITICAL FIX: Added mutex to prevent race conditions on polling state
	pollingMu     sync.Mutex // Protects usePolling, zmqPromoted, pollingTicker, pollingStopCh
	usePolling    bool
	zmqPromoted   bool
	pollingTicker *time.Ticker
	pollingStopCh chan struct{}

	// Block celebration state (announces to miners when block is found)
	celebrationEndTime    time.Time                  // When the celebration ends
	celebrationSessionID  uint64                     // Session ID of the miner who found the block
	celebrationConfig     *config.CelebrationConfig  // Celebration display settings
	celebrationMu         sync.Mutex                 // Protects celebration state

	// HA role tracking (set by OnHARoleChange, read by handleAuxBlocks/handleBlock).
	// AF-1 FIX: Uses atomic.Int32 to eliminate data race between OnHARoleChange
	// (writer on HA goroutine) and handleBlock/handleAuxBlocks (readers on miner
	// connection goroutines). Stores ha.Role cast to int32; zero = ha.RoleUnknown.
	haRole atomic.Int32

	// AUDIT FIX: Role-scoped context for in-flight block submission cancellation.
	// When demoted to Backup, roleCancel() cancels all in-flight block/aux submissions
	// that use roleCtx as their parent, preventing a demoted node from completing
	// stale RPC calls after losing master role.
	roleCtx    context.Context
	roleCancel context.CancelFunc
	roleMu     sync.Mutex

	// V1 PARITY FIX (F-6): Re-entrancy guard for post-promotion WAL recovery.
	// Prevents concurrent recovery from rapid demotion-promotion cycles.
	walRecoveryRunning atomic.Bool

	// Server start time (for accurate hashrate calculation)
	startTime time.Time

	// V2 Stratum (optional, enabled when PortV2 > 0)
	v2Server         *stratumv2.Server
	v2JobAdapter     *stratumv2.JobManagerAdapter
	v2ShareValidator *stratumv2.ShareValidator

	// Lifecycle
	wg       sync.WaitGroup
	cancel   context.CancelFunc
	running  bool
	runMu    sync.Mutex
	startErr error
}

// CoinPoolConfig holds configuration for creating a CoinPool.
type CoinPoolConfig struct {
	CoinConfig        *config.CoinPoolConfig
	CelebrationConfig *config.CelebrationConfig // Global celebration settings
	DBPool            *database.PostgresDB
	Logger            *zap.Logger
	MetricsServer     *metrics.Metrics // Shared metrics server from coordinator
}

// NewCoinPool creates a new per-coin pool instance.
func NewCoinPool(cfg *CoinPoolConfig) (*CoinPool, error) {
	log := cfg.Logger.Sugar().With("coin", cfg.CoinConfig.Symbol, "poolId", cfg.CoinConfig.PoolID)

	// Get coin implementation
	coinImpl, err := coin.Create(cfg.CoinConfig.Symbol)
	if err != nil {
		return nil, fmt.Errorf("unsupported coin %s: %w", cfg.CoinConfig.Symbol, err)
	}

	// Convert config to nodemanager NodeConfigs
	nodeConfigs := make([]nodemanager.NodeConfig, len(cfg.CoinConfig.Nodes))
	for i, n := range cfg.CoinConfig.Nodes {
		nodeConfigs[i] = nodemanager.NodeConfig{
			ID:       n.ID,
			Host:     n.Host,
			Port:     n.Port,
			User:     n.User,
			Password: n.Password,
			Priority: n.Priority,
			Weight:   n.Weight,
		}
		if n.ZMQ != nil {
			nodeConfigs[i].ZMQ = &nodemanager.ZMQConfig{
				Enabled:             n.ZMQ.Enabled,
				Endpoint:            n.ZMQ.Endpoint,
				ReconnectInitial:    n.ZMQ.ReconnectInitial,
				ReconnectMax:        n.ZMQ.ReconnectMax,
				ReconnectFactor:     n.ZMQ.ReconnectFactor,
				FailureThreshold:    n.ZMQ.FailureThreshold,
				StabilityPeriod:     n.ZMQ.StabilityPeriod,
				HealthCheckInterval: n.ZMQ.HealthCheckInterval,
			}
		}
	}

	// Create node manager
	nodeMgr, err := nodemanager.NewManagerFromConfigs(cfg.CoinConfig.Symbol, nodeConfigs, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create node manager: %w", err)
	}

	// Test connection to at least one node
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := nodeMgr.Ping(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect to any daemon node: %w", err)
	}
	cancel()
	log.Info("Connected to daemon nodes")

	// Create stratum config from coin config
	stratumCfg := &config.StratumConfig{
		Listen:         fmt.Sprintf("0.0.0.0:%d", cfg.CoinConfig.Stratum.Port),
		Difficulty:     cfg.CoinConfig.Stratum.Difficulty,
		Banning:        cfg.CoinConfig.Stratum.Banning,
		Connection:     cfg.CoinConfig.Stratum.Connection,
		VersionRolling: cfg.CoinConfig.Stratum.VersionRolling,
		JobRebroadcast: cfg.CoinConfig.Stratum.JobRebroadcast,
		RateLimiting: config.StratumRateLimitConfig{
			PreAuthMessageLimit: 20,
			PreAuthTimeout:      10 * time.Second,
			BanThreshold:        10,
			BanDuration:         30 * time.Minute,
		},
	}

	// Wire TLS config from V2 CoinPoolConfig → V1 StratumConfig
	if cfg.CoinConfig.Stratum.PortTLS > 0 {
		stratumCfg.TLS = config.TLSConfig{
			Enabled:    true,
			ListenTLS:  fmt.Sprintf("0.0.0.0:%d", cfg.CoinConfig.Stratum.PortTLS),
			CertFile:   cfg.CoinConfig.Stratum.TLS.CertFile,
			KeyFile:    cfg.CoinConfig.Stratum.TLS.KeyFile,
			MinVersion: cfg.CoinConfig.Stratum.TLS.MinVersion,
		}
	}

	// Create pool config for job manager
	poolCfg := &config.PoolConfig{
		ID:           cfg.CoinConfig.PoolID,
		Coin:         cfg.CoinConfig.Symbol,
		Address:      cfg.CoinConfig.Address,
		CoinbaseText: cfg.CoinConfig.CoinbaseText,
	}

	// Initialize components
	stratumServer := stratum.NewServer(stratumCfg, cfg.Logger)

	// Configure Spiral Router with blockchain's block time for proper share rate targeting
	// This ensures miners submit appropriate number of shares per block
	// Look up block time from VarDiff config or SupportedCoins
	blockTime := cfg.CoinConfig.Stratum.Difficulty.VarDiff.BlockTime
	if blockTime == 0 {
		// Look up from SupportedCoins by symbol
		coinSymbol := strings.ToLower(cfg.CoinConfig.Symbol)
		for name, info := range config.SupportedCoins {
			if strings.EqualFold(info.Symbol, cfg.CoinConfig.Symbol) || strings.EqualFold(name, coinSymbol) {
				blockTime = info.BlockTime
				break
			}
		}
	}
	if blockTime > 0 {
		stratumServer.SetBlockTime(blockTime)
	}

	// Configure Spiral Router with coin's mining algorithm for correct difficulty profiles.
	// Scrypt coins (LTC, DOGE, PEP, CAT, DGB-SCRYPT) use ~1000x lower difficulty than SHA-256d.
	// Must be called AFTER SetBlockTime so the correct scaled profiles are active.
	stratumServer.SetAlgorithm(coinImpl.Algorithm())

	jobManager, err := jobs.NewManagerV2(poolCfg, stratumCfg, nodeMgr, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create job manager: %w", err)
	}
	sharePipeline := shares.NewPipelineForPool(cfg.CoinConfig.PoolID, cfg.DBPool, cfg.Logger)
	shareValidator := shares.NewValidatorWithCoin(jobManager.GetJob, coinImpl)
	vardiffEngine := vardiff.NewEngine(cfg.CoinConfig.Stratum.Difficulty.VarDiff)
	vardiffEngine.SetAlgorithm(coinImpl.Algorithm())

	submitTimeouts := daemon.NewSubmitTimeouts(coinImpl.BlockTime())
	log.Infow("Block submission timeouts computed from block time",
		"coin", coinImpl.Symbol(),
		"blockTimeSec", coinImpl.BlockTime(),
		"submitTimeout", submitTimeouts.SubmitTimeout,
		"totalBudget", submitTimeouts.TotalBudget,
		"maxRetries", submitTimeouts.MaxRetries,
		"submitDeadline", submitTimeouts.SubmitDeadline,
	)

	// ═══════════════════════════════════════════════════════════════════════════
	// STARTUP VALIDATIONS (V2 parity with V1)
	// ═══════════════════════════════════════════════════════════════════════════

	// Validate pool address against daemon
	log.Infow("Validating pool address...", "address", cfg.CoinConfig.Address)
	addrCtx, addrCancel := context.WithTimeout(context.Background(), 10*time.Second)
	addrValid, addrErr := nodeMgr.ValidateAddress(addrCtx, cfg.CoinConfig.Address)
	addrCancel()
	if addrErr != nil {
		return nil, fmt.Errorf("CRITICAL: Failed to validate pool address against daemon: %w", addrErr)
	}
	if !addrValid {
		return nil, fmt.Errorf("CRITICAL: Pool address '%s' is INVALID on this daemon - blocks would be rejected! Check address format and network (mainnet vs testnet)", cfg.CoinConfig.Address)
	}
	log.Infow("Pool address validated successfully", "address", cfg.CoinConfig.Address)

	// Genesis block verification to prevent mining on wrong network
	genesisCtx, genesisCancel := context.WithTimeout(context.Background(), 10*time.Second)
	nodeGenesisHash, genesisErr := nodeMgr.GetBlockHash(genesisCtx, 0)
	genesisCancel()
	if genesisErr != nil {
		return nil, fmt.Errorf("CRITICAL: Failed to fetch genesis block hash from daemon: %w", genesisErr)
	}
	if cfg.CoinConfig.SkipGenesisVerify {
		log.Warnw("Genesis block verification SKIPPED (skip_genesis_verify=true) — regtest/testnet mode",
			"coin", coinImpl.Symbol(),
			"daemonGenesis", nodeGenesisHash,
		)
	} else if verifyErr := coinImpl.VerifyGenesisBlock(nodeGenesisHash); verifyErr != nil {
		return nil, fmt.Errorf("CRITICAL: Genesis block mismatch - WRONG CHAIN! Expected %s genesis, daemon returned %s: %w",
			coinImpl.Symbol(), nodeGenesisHash, verifyErr)
	} else {
		genesisPreview := nodeGenesisHash
		if len(genesisPreview) > 16 {
			genesisPreview = genesisPreview[:16] + "..."
		}
		log.Infow("Genesis block verified - correct chain confirmed",
			"coin", coinImpl.Symbol(),
			"genesisHash", genesisPreview,
		)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// BLOCK WAL INITIALIZATION (V2 parity with V1)
	// ═══════════════════════════════════════════════════════════════════════════
	// Per-coin WAL directory: data/wal/{poolID}/
	walDir := filepath.Join("data", "wal", cfg.CoinConfig.PoolID)
	blockWAL, walErr := NewBlockWAL(walDir, cfg.Logger)
	if walErr != nil {
		return nil, fmt.Errorf("failed to initialize block WAL for %s: %w", cfg.CoinConfig.PoolID, walErr)
	}
	log.Infow("Block write-ahead log initialized",
		"walFile", blockWAL.FilePath(),
		"poolId", cfg.CoinConfig.PoolID,
	)

	// BLOCK LOGGER INITIALIZATION
	logDir := filepath.Join("data", "logs", cfg.CoinConfig.PoolID)
	blockLogger, blErr := NewBlockLogger(logDir)
	var blockLoggerPtr *BlockLogger
	if blErr != nil {
		log.Warnw("Failed to initialize dedicated block logger - falling back to standard logger",
			"error", blErr,
		)
	} else {
		blockLoggerPtr = blockLogger
		log.Infow("Dedicated block logger initialized (no sampling)",
			"logFile", blockLogger.FilePath(),
		)
	}

	cp := &CoinPool{
		cfg:               cfg.CoinConfig,
		coin:              coinImpl,
		logger:            log,
		poolID:            cfg.CoinConfig.PoolID,
		coinSymbol:        cfg.CoinConfig.Symbol,
		stratumServer:     stratumServer,
		jobManager:        jobManager,
		sharePipeline:     sharePipeline,
		shareValidator:    shareValidator,
		vardiffEngine:     vardiffEngine,
		nodeManager:       nodeMgr,
		db:                cfg.DBPool,
		metricsServer:     cfg.MetricsServer,
		submitTimeouts:    submitTimeouts,
		blockWAL:          blockWAL,
		blockLogger:       blockLoggerPtr,
		celebrationConfig: cfg.CelebrationConfig,
	}

	// AUDIT FIX: Initialize role context for HA in-flight cancellation
	cp.roleCtx, cp.roleCancel = context.WithCancel(context.Background())

	// Default haRole to RoleMaster (standalone mode — this node IS the master).
	// When HA is configured, the Coordinator calls OnHARoleChange which overrides
	// this to the correct role (typically RoleBackup before VIP election completes).
	// Without this default, the M7 block submission gate (!= RoleMaster) blocks
	// submission in standalone mode where haRole stays at 0 (RoleUnknown).
	cp.haRole.Store(int32(ha.RoleMaster))

	// ═══════════════════════════════════════════════════════════════════════════
	// MERGE MINING (AuxPoW) AUTO-INITIALIZATION
	// ═══════════════════════════════════════════════════════════════════════════
	// When merge mining is configured, automatically set up aux chain connections
	// and wire everything together. No manual intervention needed.
	// ═══════════════════════════════════════════════════════════════════════════
	if cfg.CoinConfig.IsMergeMiningEnabled() {
		log.Info("Initializing merge mining (AuxPoW) support...")

		auxConfigs := make([]auxpow.AuxChainConfig, 0)
		for _, auxCfg := range cfg.CoinConfig.GetEnabledAuxChains() {
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
			auxDaemonClient := daemon.NewClient(auxDaemonCfg, cfg.Logger)

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
			if auxCfg.Address == cfg.CoinConfig.Address {
				return nil, fmt.Errorf("V40: Aux chain %s uses the SAME payout address as parent chain (%s) — "+
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
		auxManager, err := auxpow.NewManager(coinImpl, auxConfigs, cfg.Logger)
		if err != nil {
			return nil, fmt.Errorf("failed to create AuxPoW manager: %w", err)
		}

		// Wire up aux manager to pool (this also wires it to validator)
		cp.SetAuxManager(auxManager)

		// Wire aux manager to job manager for template generation
		jobManager.SetAuxManager(auxManager)

		log.Infow("✅ Merge mining initialized successfully",
			"auxChainCount", auxManager.AuxChainCount(),
			"parentAlgorithm", coinImpl.Algorithm(),
		)
	}

	// Wire up callbacks
	cp.setupCallbacks()

	// V26 FIX: Wire database status guard rejections to Prometheus metric.
	// This callback is invoked by postgres.go when a V12 status guard blocks
	// a block status update, making silent rejections observable and alertable.
	if cp.metricsServer != nil {
		database.SetOnStatusGuardRejection(func() {
			cp.metricsServer.BlockStatusGuardRejections.Inc()
		})
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// V2 STRATUM SERVER (optional — enabled when port_v2 > 0)
	// ═══════════════════════════════════════════════════════════════════════════
	if cfg.CoinConfig.Stratum.PortV2 > 0 {
		v2Cfg := stratumv2.DefaultServerConfig()
		v2Cfg.Port = cfg.CoinConfig.Stratum.PortV2
		v2Cfg.ListenAddr = "0.0.0.0"
		// Derive initial V2 share target from configured initial difficulty
		// This ensures V2 miners get the same starting difficulty as V1 miners
		if cfg.CoinConfig.Stratum.Difficulty.Initial > 0 {
			v2Cfg.DefaultTargetNBits = stratumv2.DifficultyToNBits(cfg.CoinConfig.Stratum.Difficulty.Initial)
		}

		v2Srv, v2Err := stratumv2.NewServer(v2Cfg, log)
		if v2Err != nil {
			return nil, fmt.Errorf("failed to create V2 server: %w", v2Err)
		}

		// Create V2 job adapter (talks to same primary daemon independently)
		primaryNode := nodeMgr.GetPrimary()
		if primaryNode == nil {
			return nil, fmt.Errorf("V2 server requires at least one configured node, but GetPrimary() returned nil")
		}
		v2JobAdapt := stratumv2.NewJobManagerAdapter(poolCfg, stratumCfg, primaryNode.Client, cfg.Logger)

		// Create V2 share validator with coin-specific algorithm
		algorithm := coinImpl.Algorithm() // "sha256d" or "scrypt"
		v2ShareVal := stratumv2.NewShareValidator(v2JobAdapt, primaryNode.Client, algorithm, cfg.Logger)

		// Wire interfaces
		v2Srv.SetJobProvider(v2JobAdapt)
		v2Srv.SetShareHandler(v2ShareVal)

		// Wire V1 rate limiter to V2 server for consistent DDoS protection
		// Both protocols share the same per-IP rate limiter so bans are unified
		if rl := cp.stratumServer.GetRateLimiter(); rl != nil {
			v2Srv.SetRateLimiter(rl)
		}

		cp.v2Server = v2Srv
		cp.v2JobAdapter = v2JobAdapt
		cp.v2ShareValidator = v2ShareVal

		log.Infow("V2 Stratum server configured",
			"port", cfg.CoinConfig.Stratum.PortV2,
			"algorithm", algorithm,
		)
	}

	return cp, nil
}

// setupCallbacks wires up event handlers between components.
func (cp *CoinPool) setupCallbacks() {
	// Job manager broadcasts to stratum server
	cp.jobManager.SetJobCallback(func(job *protocol.Job) {
		cp.stratumServer.BroadcastJob(job)
	})

	// Stratum server sends shares to pool for processing
	cp.stratumServer.SetShareHandler(cp.handleShare)

	// Track new connections - create placeholder vardiff state
	// This will be replaced with proper per-miner state when classified
	// CRITICAL: Use NewSessionStateWithProfile with reasonable MaxDiff cap instead of
	// NewSessionState which falls back to engine default (1 trillion MaxDiff).
	// This prevents runaway difficulty if MinerClassifiedHandler isn't called.
	cp.stratumServer.SetConnectHandler(func(session *protocol.Session) {
		// V29 FIX: Cap sessionStates growth to prevent OOM under sustained connection churn.
		// 100k sessions is generous (each ~200 bytes = ~20MB max). Beyond this, something
		// is wrong (botnet, pool-hopping storm) and we MUST reject the connection.
		const MaxSessionStates = 100000
		count := atomic.LoadInt64(&cp.sessionStateCount)
		if count >= MaxSessionStates {
			cp.logger.Errorw("V29: sessionStates at capacity — REJECTING connection (possible attack)",
				"count", count,
				"max", MaxSessionStates,
				"sessionId", session.ID,
			)
			// FIX: Actually close the connection to free the TCP slot.
			// Just returning here leaves the session registered in the stratum server
			// (it was added at server.go:443 before this callback). Closing the conn
			// triggers the defer cleanup in handleSession, which calls closeSession
			// and the disconnect handler. We do NOT store state, so the disconnect
			// handler's LoadAndDelete will return false and skip the counter decrement.
			if session.Conn != nil {
				_ = session.Conn.Close()
			}
			return
		}

		// Use reasonable defaults: MinDiff=0.001 (lottery), MaxDiff=150000 (low-class cap)
		// This is safe because MinerClassifiedHandler will replace with proper values
		initialDiff := cp.cfg.Stratum.Difficulty.Initial
		minDiff := 0.001
		maxDiff := 150000.0

		// CRITICAL FIX: Use Spiral Router's scaled target time based on blockchain's block time
		// GetDefaultTargetTime() returns the scaled value from MinerClassLow profile
		// Automatically scales for ANY coin: DGB(15s)->3s, DOGE(60s)->5s, LTC(150s)->5s, BTC(600s)->5s
		// For multi-coin pools, each CoinPool has its own correctly scaled target time
		targetTime := cp.stratumServer.GetDefaultTargetTime()

		state := cp.vardiffEngine.NewSessionStateWithProfile(
			initialDiff,
			minDiff,
			maxDiff,
			targetTime,
		)
		cp.sessionStates.Store(session.ID, state)
		atomic.AddInt64(&cp.sessionStateCount, 1)

		// TRACE: Log initial vardiff state (BEFORE miner classification)
		cp.logger.Infow("VARDIFF initial state (pre-classification)",
			"sessionId", session.ID,
			"initialDiff", initialDiff,
			"minDiff", minDiff,
			"maxDiff", maxDiff,
			"targetTime", targetTime,
			"note", "Will be replaced by MinerClassifiedHandler after authorize",
		)
	})

	// Cleanup on disconnect
	cp.stratumServer.SetDisconnectHandler(func(session *protocol.Session) {
		// FIX: Use LoadAndDelete to only decrement counter if state existed.
		// Sessions rejected at MaxSessionStates never had state stored, so
		// unconditional decrement caused counter drift (going negative over time,
		// eventually making the 100k guard permanently ineffective).
		if _, existed := cp.sessionStates.LoadAndDelete(session.ID); !existed {
			// Session had no vardiff state — was rejected at capacity or never initialized.
			// Do NOT decrement counter since it was never incremented for this session.
			return
		}
		atomic.AddInt64(&cp.sessionStateCount, -1)
		// Update metrics for worker disconnect
		if cp.metricsServer != nil && session.MinerAddress != "" {
			cp.metricsServer.RecordWorkerDisconnection(session.MinerAddress, session.WorkerName)
		}
	})

	// CRITICAL: Create per-miner vardiff state when Spiral Router classifies the miner
	// This provides per-session TargetShareTime, MinDiff, MaxDiff based on miner class
	cp.stratumServer.SetMinerClassifiedHandler(func(sessionID uint64, profile stratum.MinerProfile) {
		// DEFENSIVE: Ensure MaxDiff is valid - if 0, something is wrong with profile lookup
		// This prevents falling back to engine default (1 trillion) which causes runaway difficulty
		maxDiff := profile.MaxDiff
		if maxDiff <= 0 {
			cp.logger.Errorw("SAFEGUARD: Profile MaxDiff is zero or negative, using class default",
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
				maxDiff = 80000
			case "avalon_home":
				maxDiff = 30000
			default:
				maxDiff = 50000 // Safe default for unknown
			}
		}

		state := cp.vardiffEngine.NewSessionStateWithProfile(
			profile.InitialDiff,
			profile.MinDiff,
			maxDiff,
			float64(profile.TargetShareTime),
		)
		cp.sessionStates.Store(sessionID, state)
		cp.logger.Infow("VARDIFF configured with miner profile",
			"sessionId", sessionID,
			"class", profile.Class.String(),
			"initialDiff", profile.InitialDiff,
			"minDiff", profile.MinDiff,
			"maxDiff", maxDiff,
			"targetShareTime", profile.TargetShareTime,
		)
		// Update metrics for worker connection (miner is now fully authorized and classified)
		if session, ok := cp.stratumServer.GetSession(sessionID); ok {
			if cp.metricsServer != nil {
				cp.metricsServer.RecordWorkerConnection(session.MinerAddress, session.WorkerName)
			}

			// SOLO MINING: Set miner's wallet address for direct coinbase routing
			// This enables trustless SOLO mining where block rewards go directly to
			// the miner's wallet address (extracted from their stratum username).
			// Format: "WalletAddress.WorkerName" -> MinerAddress = "WalletAddress"
			if session.MinerAddress != "" {
				if err := cp.jobManager.SetSoloMinerAddress(session.MinerAddress); err != nil {
					cp.logger.Warnw("SOLO mining: invalid miner address, using pool address",
						"minerAddress", session.MinerAddress,
						"error", err,
						"note", "Miner should connect with valid wallet address as username",
					)
				} else {
					// CRITICAL: Force a new job with the miner's address
					// Without this, the miner continues working on the OLD job
					// which has the pool's address in the coinbase output.
					cp.logger.Infow("SOLO mining: forcing job refresh with miner's address",
						"minerAddress", session.MinerAddress,
					)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					if err := cp.jobManager.RefreshJob(ctx, true); err != nil {
						cp.logger.Warnw("SOLO mining: failed to refresh job",
							"error", err,
						)
					}
					cancel()
				}
			}
		}
	})

	// Legacy handler for backward compatibility
	cp.stratumServer.SetDifficultyChangeHandler(func(sessionID uint64, difficulty float64) {
		if stateVal, ok := cp.sessionStates.Load(sessionID); ok {
			state := stateVal.(*vardiff.SessionState)
			vardiff.SetDifficulty(state, difficulty)
		}
	})

	// Node manager ZMQ callbacks (if primary has ZMQ)
	// FIX D-6: Pass the block hash to enable immediate same-height reorg detection
	cp.nodeManager.SetBlockHandler(func(blockHash []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Convert block hash to hex string for AdvanceWithTip
		tipHash := ""
		if len(blockHash) > 0 {
			tipHash = fmt.Sprintf("%x", blockHash)
		}
		cp.jobManager.OnBlockNotificationWithHash(ctx, tipHash)
	})

	// ZMQ status change handler for Prometheus metrics
	cp.nodeManager.SetZMQStatusHandler(func(status daemon.ZMQStatus) {
		cp.logger.Infow("ZMQ status update",
			"status", status.String(),
		)
		// Update Prometheus metric for Sentinel monitoring
		if cp.metricsServer != nil {
			cp.metricsServer.SetZMQHealthStatus(int(status))
		}
	})
}

// handleShare processes a submitted share.
func (cp *CoinPool) handleShare(share *protocol.Share) *protocol.ShareResult {
	// Get session VARDIFF state
	stateVal, ok := cp.sessionStates.Load(share.SessionID)
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
	_ = state // Keep reference for VARDIFF updates below

	// Validate share using coin-specific algorithm (SHA256d or Scrypt)
	result := cp.shareValidator.ValidateWithCoin(share)

	// Log rejected shares for debugging (excludes silent duplicates which are expected)
	if !result.Accepted && !result.SilentDuplicate {
		cp.logger.Warnw("Share rejected",
			"sessionId", share.SessionID,
			"jobId", share.JobID,
			"reason", result.RejectReason,
			"shareDiff", share.Difficulty,
			"actualDiff", result.ActualDifficulty,
		)
	}

	// Record share to Prometheus metrics
	// For silent duplicates: record as duplicate in metrics (not accepted) for monitoring,
	// but we'll still return Accepted=true to the miner to prevent retry floods.
	if cp.metricsServer != nil {
		if result.SilentDuplicate {
			// Track as rejected duplicate for monitoring purposes
			cp.metricsServer.RecordShare(false, result.RejectReason)
		} else {
			cp.metricsServer.RecordShare(result.Accepted, result.RejectReason)
			// Track best share difficulty using actual hash difficulty (not assigned difficulty)
			// This shows the true "best" share based on how hard the hash was to find
			if result.Accepted && result.ActualDifficulty > 0 {
				cp.metricsServer.UpdateBestShareDiff(result.ActualDifficulty)
			}
		}
	}

	// Skip processing for silent duplicates - we told miner it's accepted but don't credit
	if result.SilentDuplicate {
		// Trace logging for silent duplicate handling
		cp.logger.Debugw("silent_duplicate_trace",
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
			cp.handleBlock(share, result)
		}

		// Submit to pipeline for DB persistence (after block handling)
		cp.sharePipeline.Submit(share)

		// Update VARDIFF - use aggressive retarget for ramp-up and deviation correction
		// CRITICAL: Only run vardiff when enabled in config. When disabled (e.g. regtest),
		// keep fixed initial difficulty. Without this guard, difficulty ramps to impossible
		// levels for CPU miners even though config says varDiff: enabled: false.
		if cp.cfg.Stratum.Difficulty.VarDiff.Enabled {
		stats := cp.vardiffEngine.GetStats(state)

		// Trace logging for hashrate estimation
		estimatedHashrate := cp.vardiffEngine.EstimateSessionHashrate(state)
		cp.logger.Debugw("hashrate_trace",
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

		// Get shares since last retarget for accurate rate calculation
		sharesSinceRetarget := state.SharesSinceRetarget()

		// Check if we need aggressive retargeting:
		// 1. During initial ramp-up (first 10 shares), OR
		// 2. When share rate is way off target (asymmetric: 2x fast, 3x slow)
		needsAggressive := stats.TotalShares <= 10 || cp.vardiffEngine.ShouldAggressiveRetarget(state)

		if needsAggressive && sharesSinceRetarget >= 2 {
			elapsedSec := time.Since(stats.LastRetargetTime).Seconds()

			// MINER-SPECIFIC COOLDOWN: cgminer-based miners (Avalon) need longer cooldown
			// because cgminer doesn't apply new difficulty to work-in-progress.
			// It can take 10-30+ seconds before cgminer starts using new difficulty.
			// - cgminer/Avalon: 30 second cooldown (allows difficulty to actually apply)
			// - Other miners (NMAxe, NerdQAxe++, BFGMiner): 5 second cooldown
			// Uses configurable patterns via SpiralRouter.IsSlowDiffApplier()
			minRetargetInterval := 5.0
			if cp.stratumServer.IsSlowDiffApplier(share.UserAgent) {
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
				newDiff, changed = cp.vardiffEngine.AggressiveRetarget(state, sharesSinceRetarget, elapsedSec)
				if changed {
					cp.logger.Infow("VARDIFF retarget",
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

		// Normal vardiff after ramp-up period
		if !changed {
			newDiff, changed = cp.vardiffEngine.RecordShare(state)
			if changed {
				cp.logger.Debugw("VARDIFF adjusted",
					"sessionId", share.SessionID,
					"newDiff", newDiff,
				)
			}
		}

		// Send new difficulty to miner (block already handled at start of Accepted block)
		if changed {
			if session, ok := cp.stratumServer.GetSession(share.SessionID); ok {
				if err := cp.stratumServer.SendDifficulty(session, newDiff); err != nil {
					cp.logger.Warnw("Failed to send difficulty update",
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
		cp.handleAuxBlocks(share, result.AuxResults)
	}

	return result
}

// handleBlock processes a found block.
// CRITICAL: Block submission is TIME-SENSITIVE. Every millisecond of delay
// increases the chance of another miner finding a block first.
//
// CRASH SAFETY FIX: We now record block with status="submitting" BEFORE the submit
// attempt. This ensures reconcileSubmittingBlocks() can recover blocks that were
// accepted by the daemon but not confirmed in DB due to a crash during submission.
//
// Flow:
// 1. Insert block with status="submitting" (fast, ~2ms typical)
// 2. Submit to daemon (time-critical)
// 3. Update block status to "pending" or "orphaned"
func (cp *CoinPool) handleBlock(share *protocol.Share, result *protocol.ShareResult) {
	foundTime := time.Now()

	// Convert satoshis to coin units (e.g., BTC/DGB) for storage
	rewardCoins := float64(result.CoinbaseValue) / 1e8

	// ═══════════════════════════════════════════════════════════════════════════
	// STALE RACE CHECK: Re-verify job state before submission.
	// Between share validation and now, a ZMQ notification may have invalidated
	// this job. Submitting a block with a stale prevBlockHash guarantees rejection.
	// ═══════════════════════════════════════════════════════════════════════════
	finalStatus := "pending"
	rejectReason := ""  // V1 PARITY: for WAL RejectReason field
	orphanReason := ""  // V1 PARITY: for WAL/log structured reason
	var lastErr error
	var submitTime time.Time

	job, jobFound := cp.jobManager.GetJob(share.JobID)
	jobState := protocol.JobStateActive
	if jobFound {
		jobState = job.GetState()
	}
	if jobState == protocol.JobStateInvalidated {
		finalStatus = "orphaned"
		lastErr = fmt.Errorf("job invalidated before submission (stale race)")
		submitTime = time.Now()
		cp.logger.Warnw("Block found but job invalidated before submission (stale race)",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"jobId", share.JobID,
			"jobAge", result.JobAge,
			"miner", share.MinerAddress,
		)
		if cp.metricsServer != nil {
			cp.metricsServer.BlockStaleRace.Inc()
			cp.metricsServer.BlockRejectionsByReason.WithLabelValues("stale_race").Inc()
		}
		// AUDIT FIX: Log pre-submission orphan to dedicated block logger.
		if cp.blockLogger != nil {
			cp.blockLogger.LogBlockOrphaned(share.BlockHeight, result.BlockHash, share.MinerAddress, "stale_race")
		}
	} else if jobState == protocol.JobStateSolved {
		// RACE CONDITION: Multiple shares from the same job met the network target.
		finalStatus = "orphaned"
		lastErr = fmt.Errorf("job already solved (duplicate block candidate)")
		submitTime = time.Now()
		cp.logger.Warnw("RACE: Block found but job already solved (duplicate block candidate)",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"jobId", share.JobID,
		)
		if cp.metricsServer != nil {
			cp.metricsServer.BlockRejectionsByReason.WithLabelValues("duplicate_candidate").Inc()
		}
		// AUDIT FIX: Log pre-submission orphan to dedicated block logger.
		if cp.blockLogger != nil {
			cp.blockLogger.LogBlockOrphaned(share.BlockHeight, result.BlockHash, share.MinerAddress, "duplicate_candidate")
		}
	} else if jobFound && job.RawPrevBlockHash != "" && job.RawPrevBlockHash != cp.jobManager.GetLastBlockHash() {
		// DIRECT STALE CHECK: Compare the job's prev block hash against the chain tip.
		finalStatus = "orphaned"
		lastErr = fmt.Errorf("job prev hash doesn't match current chain tip (stale)")
		submitTime = time.Now()
		cp.logger.Warnw("Block found but chain tip has moved (direct stale check)",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"jobPrevHash", job.RawPrevBlockHash,
			"currentTip", cp.jobManager.GetLastBlockHash(),
		)
		if cp.metricsServer != nil {
			cp.metricsServer.BlockStaleRace.Inc()
			cp.metricsServer.BlockRejectionsByReason.WithLabelValues("chain_tip_moved").Inc()
		}
		// AUDIT FIX: Log pre-submission orphan to dedicated block logger.
		if cp.blockLogger != nil {
			cp.blockLogger.LogBlockOrphaned(share.BlockHeight, result.BlockHash, share.MinerAddress, "chain_tip_moved")
		}
	} else if result.BlockHex == "" {
		// ═══════════════════════════════════════════════════════════════════════════
		// BLOCK HEX REBUILD: Attempt recovery when serialization failed in validator
		// ═══════════════════════════════════════════════════════════════════════════
		cp.logger.Errorw("Block found but BlockHex is empty - attempting recovery rebuild!",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"buildError", result.BlockBuildError,
		)

		rebuilt := false
		if jobFound && job != nil {
			rebuildHex, rebuildErr := shares.RebuildBlockHex(job, share)
			if rebuildErr == nil && rebuildHex != "" {
				cp.logger.Infow("Block recovery rebuild succeeded!",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
				)
				result.BlockHex = rebuildHex
				rebuilt = true
			} else {
				cp.logger.Errorw("Block recovery rebuild ALSO FAILED",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"rebuildError", rebuildErr,
					"originalError", result.BlockBuildError,
				)
			}
		}

		if !rebuilt {
			// LAST RESORT: Write emergency WAL entry with ALL raw components
			finalStatus = "orphaned"
			if cp.metricsServer != nil {
				cp.metricsServer.BlockRejectionsByReason.WithLabelValues("build_failed").Inc()
			}

			if cp.blockWAL != nil {
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

				if walErr := cp.blockWAL.LogBlockFound(emergencyEntry); walErr != nil {
					cp.logger.Errorw("CRITICAL: Failed to write emergency WAL entry!",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"walError", walErr,
					)
				} else {
					cp.logger.Errorw("Emergency WAL entry written with raw block components for manual reconstruction",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"walFile", cp.blockWAL.FilePath(),
					)
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// BLOCK SUBMISSION (only if we have valid BlockHex and not already rejected)
	// ═══════════════════════════════════════════════════════════════════════════
	if result.BlockHex != "" && finalStatus == "pending" {
		// P0 AUDIT FIX: Write WAL entry BEFORE submission to close the crash-loss window.
		if cp.blockWAL != nil {
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
			if walErr := cp.blockWAL.LogBlockFound(preSubmitEntry); walErr != nil {
				cp.logger.Errorw("Failed to write pre-submission WAL entry (continuing with submission)",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"error", walErr,
				)
			}
		}

		// CRASH SAFETY: Record block with status="submitting" in DB BEFORE submit
		block := &database.Block{
			Height:            share.BlockHeight,
			NetworkDifficulty: share.NetworkDiff,
			Status:            "submitting",
			Type:              "block",
			Miner:             share.MinerAddress,
			Source:            share.WorkerName,
			Reward:            rewardCoins,
			Hash:              result.BlockHash,
			Created:           time.Now(),
		}

		preSubmitCtx, preSubmitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := cp.db.InsertBlockForPool(preSubmitCtx, cp.poolID, block); err != nil {
			preSubmitCancel()
			cp.logger.Warnw("Failed to record block as 'submitting' before submit (continuing anyway)",
				"height", share.BlockHeight,
				"hash", result.BlockHash,
				"error", err,
			)
		} else {
			preSubmitCancel()
		}

		// HA: Cross-node block submission dedup via Redis SETNX.
		skipSubmission := false
		if cp.redisDedupTracker != nil && cp.redisDedupTracker.IsEnabled() {
			redisClient := cp.redisDedupTracker.Client()
			if redisClient != nil {
				dedupKey := fmt.Sprintf("spiralpool:%s:block_submit:%s", cp.poolID, result.BlockHash)
				dedupCtx, dedupCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				claimed, dedupErr := redisClient.SetNX(dedupCtx, dedupKey, cp.poolID, 5*time.Minute).Result()
				dedupCancel()
				if dedupErr == nil && !claimed {
					cp.logger.Infow("Block already being submitted by another HA node (skipping duplicate RPC)",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
					)
					finalStatus = "pending" // Trust the other node
					submitTime = time.Now()
					skipSubmission = true
					// V1 PARITY FIX (F-7): Mark job as Solved to prevent duplicate submission attempts
					if jobFound && job != nil {
						job.SetState(protocol.JobStateSolved, "block submitted by peer HA node")
					}
				}
			}
		}

		// HA ROLE GATE: Backup nodes must not submit blocks to the daemon.
		// The master node handles all block submissions. Without this explicit check,
		// a backup node could issue duplicate RPC calls. WAL and DB entries are still
		// recorded above for crash recovery if this node is promoted mid-flight.
		// BUG FIX (M7): Check != RoleMaster instead of == RoleBackup.
		// RoleObserver (and RoleUnknown) must also be blocked from submitting.
		if !skipSubmission && ha.Role(cp.haRole.Load()) != ha.RoleMaster {
			cp.logger.Warnw("Skipping block submission (not master node)",
				"coin", cp.coinSymbol,
				"height", share.BlockHeight,
				"hash", result.BlockHash,
			)
			skipSubmission = true
			// BUG FIX: "submitted" is not a valid status in statusPriority map!
			// Must use "pending" to trust master's submission, otherwise block stays
			// stuck in "submitting" status forever and UpdateBlockStatusForPool fails
			// with "invalid block status 'submitted': not in statusPriority map"
			finalStatus = "pending" // Trust master to handle submission
			submitTime = time.Now()
			// V1 PARITY FIX (F-7): Mark job as Solved to prevent duplicate submission attempts
			if jobFound && job != nil {
				job.SetState(protocol.JobStateSolved, "block found on backup node — master will submit")
			}
		}

		if !skipSubmission {
			// SUBMIT IMMEDIATELY — unified submit + verify + preciousblock (V1 PARITY)
			// HeightContext cancels automatically if the chain tip advances (new block found),
			// preventing stale RPC calls on a guaranteed-rejected block.
			// AUDIT FIX: Use roleCtx as parent so demotion cancels in-flight submissions.
			cp.roleMu.Lock()
			baseCtx := cp.roleCtx
			cp.roleMu.Unlock()
			heightCtx, heightCancel := cp.jobManager.HeightContext(baseCtx)
			submitCtx, submitCancel := context.WithTimeout(heightCtx, cp.submitTimeouts.TotalBudget)
			sr := cp.nodeManager.SubmitBlockWithVerification(submitCtx, result.BlockHex, result.BlockHash, share.BlockHeight, cp.submitTimeouts)
			submitCancel()
			heightCancel()
			submitTime = time.Now()

			// V1 PARITY FIX: Record submission latency metric (F-2)
			if cp.metricsServer != nil {
				cp.metricsServer.BlockSubmitLatency.Observe(float64(submitTime.Sub(foundTime).Milliseconds()))
			}

			if sr.Submitted || sr.Verified {
				cp.logger.Infow("Block submitted successfully!",
					"height", share.BlockHeight,
					"hash", result.BlockHash,
					"miner", share.MinerAddress,
					"submissionLatencyMs", submitTime.Sub(foundTime).Milliseconds(),
					"submitted", sr.Submitted,
					"verified", sr.Verified,
					"chainHash", sr.ChainHash,
				)
				finalStatus = "pending"
				// V1 PARITY FIX: Increment BlocksSubmitted counter on initial success (F-1)
				if cp.metricsServer != nil {
					cp.metricsServer.RecordBlockSubmittedForCoin(cp.coinSymbol)
				}
				// V1 PARITY FIX: Transition job to Solved to prevent duplicate block
				// submissions if another share from the same job also meets the network target (F-7)
				if jobFound && job != nil {
					job.SetState(protocol.JobStateSolved, "block submitted successfully")
				}

				// V1 PARITY FIX: Track false rejections (submit failed but chain verification passed) (F-3)
				if sr.Verified && !sr.Submitted {
					cp.logger.Infow("Block accepted despite submission error (verified in chain)",
						"height", share.BlockHeight,
						"hash", result.BlockHash,
						"submitError", sr.SubmitErr,
					)
					if cp.metricsServer != nil {
						cp.metricsServer.BlocksFalseRejection.Inc()
					}
				}
				if sr.PreciousErr != nil {
					cp.logger.Debugw("preciousblock hint failed (non-critical)", "error", sr.PreciousErr)
				}
				// AUDIT FIX: Log successful submission to dedicated block logger (V1 parity).
				if cp.blockLogger != nil {
					cp.blockLogger.LogBlockSubmitted(
						share.BlockHeight,
						result.BlockHash,
						share.MinerAddress,
						1,
						submitTime.Sub(foundTime).Milliseconds(),
					)
				}
			} else {
				lastErr = sr.SubmitErr
				// Check for permanent rejection (V1 PARITY: duplicate/already safety check)
				if sr.SubmitErr != nil && isPermanentRejection(sr.SubmitErr.Error()) {
					errLower := strings.ToLower(sr.SubmitErr.Error())
					isDuplicate := strings.Contains(errLower, "duplicate") || strings.Contains(errLower, "already")
					if isDuplicate && cp.verifyBlockAcceptance(result.BlockHash, share.BlockHeight) {
						// Block IS in the active chain — treat as success
						finalStatus = "pending"
						if cp.metricsServer != nil {
							cp.metricsServer.RecordBlockSubmittedForCoin(cp.coinSymbol)
						}
						// V1 PARITY FIX (F-7): Mark job as Solved
						if jobFound && job != nil {
							job.SetState(protocol.JobStateSolved, "block verified in chain (daemon returned duplicate)")
						}
						cp.logger.Infow("Block verified in chain despite daemon 'duplicate' response",
							"coin", cp.coinSymbol,
							"height", share.BlockHeight,
							"hash", result.BlockHash,
							"daemonResponse", sr.SubmitErr.Error(),
						)
						if cp.blockLogger != nil {
							cp.blockLogger.LogBlockSubmitted(
								share.BlockHeight,
								result.BlockHash,
								share.MinerAddress,
								1,
								submitTime.Sub(foundTime).Milliseconds(),
							)
						}
					} else {
						// Block was stale, invalid, or duplicate-but-not-in-active-chain
						rejectReason = classifyRejection(sr.SubmitErr.Error())
						finalStatus = "orphaned"
						if cp.metricsServer != nil {
							cp.metricsServer.BlockRejectionsByReason.WithLabelValues(classifyRejectionMetric(sr.SubmitErr.Error())).Inc()
						}
						if isDuplicate {
							orphanReason = "duplicate_not_in_active_chain"
							cp.logger.Warnw("Block 'duplicate' in daemon but NOT in active chain — orphaning",
								"coin", cp.coinSymbol,
								"height", share.BlockHeight,
								"hash", result.BlockHash,
								"rejectionType", rejectReason,
								"orphanReason", orphanReason,
							)
						} else {
							orphanReason = "permanent_rejection"
							cp.logger.Errorw("BLOCK REJECTED (permanent - no retry)",
								"coin", cp.coinSymbol,
								"height", share.BlockHeight,
								"hash", result.BlockHash,
								"error", sr.SubmitErr,
								"rejectionType", rejectReason,
								"orphanReason", orphanReason,
							)
						}
						if cp.blockLogger != nil {
							cp.blockLogger.LogBlockRejected(
								share.BlockHeight,
								result.BlockHash,
								share.MinerAddress,
								rejectReason,
								sr.SubmitErr.Error(),
								result.PrevBlockHash,
								result.JobAge.Milliseconds(),
							)
						}
					}
				} else {
					// Transient error - deadline-driven retry loop with HeightContext (V1 PARITY)
					// The loop runs until EITHER:
					//   1. SubmitDeadline expires (BlockTime × 0.30)
					//   2. Height advances (new block found — HeightContext cancels)
					//   3. MaxRetries safety cap reached
					// AUDIT FIX: Use roleCtx as parent so demotion cancels retry loop.
					retryStartTime := time.Now()
					cp.roleMu.Lock()
					retryBaseCtx := cp.roleCtx
					cp.roleMu.Unlock()
					retryHeightCtx, retryHeightCancel := cp.jobManager.HeightContext(retryBaseCtx)
					deadlineCtx, deadlineCancel := context.WithTimeout(retryHeightCtx, cp.submitTimeouts.SubmitDeadline)
					maxRetries := cp.submitTimeouts.MaxRetries
					attempt := 2
					retrySucceeded := false
					permanentlyRejected := false

					for deadlineCtx.Err() == nil && attempt <= maxRetries+1 {
						// V1 PARITY FIX: Track retry count metric (F-4)
						if cp.metricsServer != nil {
							cp.metricsServer.BlockSubmitRetries.Inc()
						}
						time.Sleep(cp.submitTimeouts.RetrySleep)
						if deadlineCtx.Err() != nil {
							break
						}

						retryCtx, retryCancel := context.WithTimeout(deadlineCtx, cp.submitTimeouts.RetryTimeout)
						retrySr := cp.nodeManager.SubmitBlockWithVerification(retryCtx, result.BlockHex, result.BlockHash, share.BlockHeight, cp.submitTimeouts)
						retryCancel()

						if retrySr.Submitted || retrySr.Verified {
							cp.logger.Infow("Block submitted on retry!",
								"height", share.BlockHeight,
								"hash", result.BlockHash,
								"attempt", attempt,
								"submitted", retrySr.Submitted,
								"verified", retrySr.Verified,
							)
							finalStatus = "pending"
							// V1 PARITY FIX (F-7): Mark job as Solved on retry success
							if jobFound && job != nil {
								job.SetState(protocol.JobStateSolved, "block submitted on retry")
							}
							// FIX: Record block submission metric on retry success path.
							// Previously only recorded on retry-duplicate path.
							if cp.metricsServer != nil {
								cp.metricsServer.RecordBlockSubmittedForCoin(cp.coinSymbol)
							}
							retrySucceeded = true
							// AUDIT FIX: Log retry success to dedicated block logger.
							if cp.blockLogger != nil {
								cp.blockLogger.LogBlockSubmitted(
									share.BlockHeight,
									result.BlockHash,
									share.MinerAddress,
									attempt,
									time.Since(foundTime).Milliseconds(),
								)
							}
							break
						}

						lastErr = retrySr.SubmitErr
						if retrySr.SubmitErr != nil && isPermanentRejection(retrySr.SubmitErr.Error()) {
							// V1 PARITY: duplicate/already safety check on retry path
							retryErrLower := strings.ToLower(retrySr.SubmitErr.Error())
							isRetryDuplicate := strings.Contains(retryErrLower, "duplicate") || strings.Contains(retryErrLower, "already")
							if isRetryDuplicate && cp.verifyBlockAcceptance(result.BlockHash, share.BlockHeight) {
								finalStatus = "pending"
								if cp.metricsServer != nil {
									cp.metricsServer.RecordBlockSubmittedForCoin(cp.coinSymbol)
								}
								// V1 PARITY FIX (F-7): Mark job as Solved on retry duplicate verify
								if jobFound && job != nil {
									job.SetState(protocol.JobStateSolved, "block verified in chain on retry (daemon returned duplicate)")
								}
								cp.logger.Infow("Block verified in chain on retry despite 'duplicate' response",
									"coin", cp.coinSymbol,
									"height", share.BlockHeight,
									"hash", result.BlockHash,
									"attempt", attempt,
								)
								if cp.blockLogger != nil {
									cp.blockLogger.LogBlockSubmitted(
										share.BlockHeight,
										result.BlockHash,
										share.MinerAddress,
										attempt,
										time.Since(foundTime).Milliseconds(),
									)
								}
								retrySucceeded = true
								break
							}
							// Not verified in active chain — orphan with classified reason
							rejectReason = classifyRejection(retrySr.SubmitErr.Error())
							finalStatus = "orphaned"
							if cp.metricsServer != nil {
								cp.metricsServer.BlockRejectionsByReason.WithLabelValues(classifyRejectionMetric(retrySr.SubmitErr.Error())).Inc()
							}
							if isRetryDuplicate {
								orphanReason = "duplicate_not_in_active_chain"
								cp.logger.Warnw("Block 'duplicate' on retry but NOT in active chain — orphaning",
									"coin", cp.coinSymbol,
									"height", share.BlockHeight,
									"hash", result.BlockHash,
									"attempt", attempt,
									"orphanReason", orphanReason,
								)
							} else {
								orphanReason = "permanent_rejection"
								cp.logger.Errorw("BLOCK REJECTED on retry (permanent)",
									"coin", cp.coinSymbol,
									"height", share.BlockHeight,
									"hash", result.BlockHash,
									"error", retrySr.SubmitErr,
									"attempt", attempt,
									"orphanReason", orphanReason,
								)
							}
							if cp.blockLogger != nil {
								cp.blockLogger.LogBlockRejected(
									share.BlockHeight,
									result.BlockHash,
									share.MinerAddress,
									rejectReason,
									retrySr.SubmitErr.Error(),
									result.PrevBlockHash,
									result.JobAge.Milliseconds(),
								)
							}
							permanentlyRejected = true
							break
						}
						attempt++
					}
					deadlineCancel()
					retryHeightCancel()

					// Classify abort reason (V1 PARITY)
					if !retrySucceeded && !permanentlyRejected {
						if deadlineCtx.Err() != nil {
							elapsed := time.Since(retryStartTime)
							// AUDIT FIX (ISSUE-7): V1 PARITY — record deadline usage ratio.
							// Values near 1.0 indicate the deadline is too tight for this chain.
							if cp.metricsServer != nil {
								deadlineUsage := float64(elapsed) / float64(cp.submitTimeouts.SubmitDeadline)
								cp.metricsServer.BlockDeadlineUsage.Observe(deadlineUsage)
							}
							roleDemoted := retryBaseCtx.Err() != nil
							abortReason := "submit deadline expired"
							if roleDemoted {
								abortReason = "HA demotion (roleCtx cancelled)"
								if cp.metricsServer != nil {
									cp.metricsServer.BlockDeadlineAborts.Inc()
								}
							} else if retryHeightCtx.Err() != nil && deadlineCtx.Err() == context.Canceled {
								abortReason = "height advanced (new block found)"
								if cp.metricsServer != nil {
									cp.metricsServer.BlockHeightAborts.Inc()
								}
							} else {
								if cp.metricsServer != nil {
									cp.metricsServer.BlockDeadlineAborts.Inc()
								}
							}
							cp.logger.Warnw("Block submission aborted: "+abortReason,
								"coin", cp.coinSymbol,
								"height", share.BlockHeight,
								"hash", result.BlockHash,
								"attempts", attempt-1,
								"deadline", cp.submitTimeouts.SubmitDeadline,
								"elapsedMs", elapsed.Milliseconds(),
								"abortReason", abortReason,
								"roleDemoted", roleDemoted,
							)
						}
					}

					// Post-timeout: enhanced multi-method verification (safety net)
					if !retrySucceeded && !permanentlyRejected {
						if cp.verifyBlockAcceptance(result.BlockHash, share.BlockHeight) {
							cp.logger.Infow("Block verified in daemon after timeout - marking pending",
								"height", share.BlockHeight,
								"hash", result.BlockHash,
							)
							finalStatus = "pending"
							// V1 PARITY FIX (F-7): Mark job as Solved on post-timeout verify
							if jobFound && job != nil {
								job.SetState(protocol.JobStateSolved, "block verified in chain after timeout")
							}
						} else {
							orphanReason = "retry_exhausted_and_unverified"
							cp.logger.Warnw("Block orphaned after all retries + post-timeout verification failed",
								"coin", cp.coinSymbol,
								"height", share.BlockHeight,
								"hash", result.BlockHash,
								"lastError", lastErr,
								"orphanReason", orphanReason,
							)
							finalStatus = "orphaned"
							// AUDIT FIX (ISSUE-2): Increment BlocksSubmissionFailed metric.
							// Any non-zero value indicates potential block loss — alert immediately.
							if cp.metricsServer != nil {
								cp.metricsServer.RecordBlockSubmissionFailedForCoin(cp.coinSymbol)
							}
							if cp.blockLogger != nil {
								cp.blockLogger.LogBlockOrphaned(share.BlockHeight, result.BlockHash, share.MinerAddress, orphanReason)
							}
						}
					}
				}
			}
		}

		// Post-submission WAL status update
		if cp.blockWAL != nil {
			postEntry := &BlockWALEntry{
				Height:        share.BlockHeight,
				BlockHash:     result.BlockHash,
				MinerAddress:  share.MinerAddress,
				CoinbaseValue: result.CoinbaseValue,
				Status:        finalStatus,
				RejectReason:  rejectReason,
				SubmittedAt:   submitTime.Format(time.RFC3339),
			}
			if lastErr != nil {
				postEntry.SubmitError = lastErr.Error()
			}
			if orphanReason != "" && postEntry.SubmitError == "" {
				postEntry.SubmitError = orphanReason
			}
			if walErr := cp.blockWAL.LogSubmissionResult(postEntry); walErr != nil {
				cp.logger.Errorw("Failed to write post-submission WAL entry",
					"height", share.BlockHeight,
					"error", walErr,
				)
			}
		}
	}

	// Log block found
	cp.logger.Infow("BLOCK FOUND!",
		"height", share.BlockHeight,
		"hash", result.BlockHash,
		"miner", share.MinerAddress,
		"worker", share.WorkerName,
		"status", finalStatus,
	)

	// AUDIT FIX (ISSUE-1 + ISSUE-4): Increment BlocksFound metric for every block detection.
	// Uses per-coin variant for V2 multi-coin observability.
	if cp.metricsServer != nil {
		cp.metricsServer.RecordBlockForCoin(cp.coinSymbol)
	}

	// V1 PARITY FIX (F-5): Log accepted blocks to dedicated block logger.
	// Only log blocks that were actually accepted (not orphaned/rejected)
	// to prevent false positive alerts in log monitoring systems.
	if finalStatus == "pending" && cp.blockLogger != nil {
		cp.blockLogger.LogBlockFound(
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

	// Update block status in database (from "submitting" to final status)
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := cp.db.UpdateBlockStatusForPool(dbCtx, cp.poolID, share.BlockHeight, result.BlockHash, finalStatus, 0); err != nil {
		dbCancel()
		cp.logger.Errorw("Failed to update block status in database",
			"height", share.BlockHeight,
			"hash", result.BlockHash,
			"status", finalStatus,
			"error", err,
		)
	} else {
		dbCancel()
	}

	_ = lastErr // Used in WAL entry above

	// Only celebrate and log reward messaging if block was actually accepted.
	// Orphaned/rejected blocks must NOT trigger celebration or reward messaging
	// (V1 PARITY FIX — V1 gates this with blockStatus=="pending").
	if finalStatus == "pending" {
		cp.logger.Infow("Block reward will be sent to configured pool address",
			"note", "SOLO mining - reward goes directly to your wallet",
		)

		// AUDIT FIX: Wire TargetSourceUsed metric to track which target source
		// detected the block (gbt, compact_bits, float64_diff, etc.)
		if cp.metricsServer != nil && result.TargetSource != "" {
			cp.metricsServer.TargetSourceUsed.WithLabelValues(result.TargetSource).Inc()
		}

		// Send block found message only to the miner who found it
		cp.startCelebration(share.SessionID, share.BlockHeight, share.MinerAddress, share.WorkerName, rewardCoins, cp.coinSymbol)
	}
}

// startCelebration sends a block found message to all miners when a block is found.
// The finder gets a direct message, ALL miners get a broadcast, and Avalon LEDs are triggered.
func (cp *CoinPool) startCelebration(sessionID uint64, height uint64, miner, worker string, reward float64, coinSymbol string) {
	// Check if celebration is enabled in config
	if cp.celebrationConfig == nil || !cp.celebrationConfig.Enabled {
		cp.logger.Debugw("Celebration disabled, skipping block announcement",
			"coin", coinSymbol, "height", height)
		return
	}

	// Calculate celebration duration from config (default 2 hours)
	durationHours := cp.celebrationConfig.DurationHours
	if durationHours <= 0 {
		durationHours = 2
	}

	cp.celebrationMu.Lock()
	cp.celebrationEndTime = time.Now().Add(time.Duration(durationHours) * time.Hour)
	cp.celebrationSessionID = sessionID
	cp.celebrationMu.Unlock()

	msg := fmt.Sprintf("BLOCK FOUND! %s Height %d by %s - Reward: %.8f - CELEBRATING!", coinSymbol, height, worker, reward)

	if cp.stratumServer != nil {
		// Send to the miner who found the block
		cp.stratumServer.SendMessageToSession(sessionID, msg)

		// Broadcast to ALL miners for visual awareness on displays
		cp.stratumServer.BroadcastMessage(msg)
	}

	// Trigger full RGB LED celebration via block-celebrate.sh
	// The script handles Avalon LED discovery, 12-phase color sequences, and state restore
	go func(hours int) {
		scriptPath := "/spiralpool/scripts/block-celebrate.sh"
		if _, err := os.Stat(scriptPath); err != nil {
			cp.logger.Debugw("Celebration script not found, skipping LED celebration", "path", scriptPath)
			return
		}
		durationSecs := strconv.Itoa(hours * 3600)
		cmd := exec.Command(scriptPath, "--duration", durationSecs)
		if err := cmd.Start(); err != nil {
			cp.logger.Warnw("Failed to start celebration script", "error", err)
			return
		}
		cp.logger.Infow("LED celebration script started",
			"pid", cmd.Process.Pid,
			"coin", coinSymbol,
			"height", height,
			"durationSecs", durationSecs,
		)
		// Wait in background to reap child process (prevents zombie)
		if err := cmd.Wait(); err != nil {
			cp.logger.Debugw("Celebration script exited with error", "error", err)
		}
	}(durationHours)

	cp.logger.Infow("Block celebration started",
		"coin", coinSymbol,
		"sessionId", sessionID,
		"height", height,
		"miner", miner,
		"worker", worker,
		"durationHours", durationHours,
		"celebrationEnds", cp.celebrationEndTime.Format(time.RFC3339),
	)
}

// celebrationLoop periodically sends celebration messages while in celebration mode.
// Sends to the finder and broadcasts to all miners.
func (cp *CoinPool) celebrationLoop(ctx context.Context) {
	defer cp.wg.Done()

	// Skip if celebration is disabled
	if cp.celebrationConfig == nil || !cp.celebrationConfig.Enabled {
		return
	}

	ticker := time.NewTicker(5 * time.Minute) // Send celebration message every 5 minutes
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cp.celebrationMu.Lock()
			endTime := cp.celebrationEndTime
			sessionID := cp.celebrationSessionID
			cp.celebrationMu.Unlock()

			if time.Now().Before(endTime) && cp.stratumServer != nil && sessionID != 0 {
				remaining := time.Until(endTime).Round(time.Minute)
				msg := fmt.Sprintf("%s BLOCK CELEBRATION! %v remaining - Keep mining!", cp.coinSymbol, remaining)

				// Send to the miner who found the block
				cp.stratumServer.SendMessageToSession(sessionID, msg)

				// Broadcast to ALL miners for visual awareness
				cp.stratumServer.BroadcastMessage(msg)
			}
		}
	}
}

// reconciliationLoop periodically checks for blocks stuck in "submitting" state.
// FIX D-9: Handles cases where daemon was unreachable at startup or reconciliation
// failed during a previous cycle. Runs every 5 minutes.
func (cp *CoinPool) reconciliationLoop(ctx context.Context) {
	defer cp.wg.Done()

	// Run every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := cp.reconcileSubmittingBlocks(reconcileCtx); err != nil {
				cp.logger.Warnw("Periodic block reconciliation had errors",
					"error", err,
				)
			}
			cancel()
		}
	}
}

// reconcileSubmittingBlocks checks for blocks stuck in "submitting" state from a previous crash
// and updates their status based on whether they exist in the daemon's blockchain.
// CRITICAL: Designed to prevent block accounting loss after a crash during submission.
func (cp *CoinPool) reconcileSubmittingBlocks(ctx context.Context) error {
	// Get blocks stuck in "submitting" state
	submittingBlocks, err := cp.db.GetBlocksByStatus(ctx, "submitting")
	if err != nil {
		return fmt.Errorf("failed to query submitting blocks: %w", err)
	}

	if len(submittingBlocks) == 0 {
		return nil
	}

	cp.logger.Infow("Reconciling blocks stuck in 'submitting' state",
		"count", len(submittingBlocks),
	)

	var reconcileErrors []error
	for _, block := range submittingBlocks {
		// Check if daemon has this block at this height
		daemonHash, err := cp.nodeManager.GetBlockHash(ctx, block.Height)
		if err != nil {
			cp.logger.Warnw("Failed to get block hash from daemon during reconciliation",
				"height", block.Height,
				"error", err,
			)
			reconcileErrors = append(reconcileErrors, err)
			continue
		}

		var newStatus string
		if daemonHash == block.Hash {
			// Block is in the chain - daemon accepted it
			newStatus = "pending"
			cp.logger.Infow("Reconciled block: daemon accepted (now pending confirmation)",
				"height", block.Height,
				"hash", block.Hash,
			)
		} else {
			// Different hash at this height - our block was orphaned or never accepted
			newStatus = "orphaned"
			cp.logger.Warnw("Reconciled block: orphaned (different hash at height)",
				"height", block.Height,
				"ourHash", block.Hash,
				"daemonHash", daemonHash,
			)
		}

		// Update block status using pool-specific method with hash for safety
		if err := cp.db.UpdateBlockStatusForPool(ctx, cp.poolID, block.Height, block.Hash, newStatus, 0); err != nil {
			cp.logger.Errorw("Failed to update reconciled block status",
				"height", block.Height,
				"hash", block.Hash,
				"newStatus", newStatus,
				"error", err,
			)
			reconcileErrors = append(reconcileErrors, err)
		}
	}

	if len(reconcileErrors) > 0 {
		return fmt.Errorf("reconciliation completed with %d errors", len(reconcileErrors))
	}

	cp.logger.Infow("Block reconciliation completed successfully",
		"reconciledCount", len(submittingBlocks),
	)
	return nil
}

// Start starts all coin pool components.
func (cp *CoinPool) Start(ctx context.Context) error {
	cp.runMu.Lock()
	defer cp.runMu.Unlock()

	if cp.running {
		return fmt.Errorf("coin pool already running")
	}

	ctx, cp.cancel = context.WithCancel(ctx)

	// Record server start time for accurate hashrate calculation
	cp.startTime = time.Now()

	// Start node manager health monitoring
	if err := cp.nodeManager.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node manager: %w", err)
	}
	cp.logger.Info("Node manager started")

	// ═══════════════════════════════════════════════════════════════════════════
	// SYNC GATE: Wait for daemon to be fully synced before accepting miners
	// ═══════════════════════════════════════════════════════════════════════════
	if err := cp.waitForSync(ctx); err != nil {
		return fmt.Errorf("sync gate failed: %w", err)
	}

	// CLEANUP: Remove stale shares from previous sessions
	if err := cp.cleanupStaleShares(ctx); err != nil {
		cp.logger.Warnw("Failed to cleanup stale shares (non-fatal)", "error", err)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// WAL RECOVERY: Recover unsubmitted blocks from previous crash
	// ═══════════════════════════════════════════════════════════════════════════
	if cp.blockWAL != nil {
		walDir := filepath.Dir(cp.blockWAL.FilePath())
		cp.recoverWALBlocks(ctx, walDir)
	}

	// CRITICAL: Reconcile any blocks stuck in "submitting" state from previous crash
	// This ensures blocks that were accepted by daemon but not confirmed in DB are recovered
	if err := cp.reconcileSubmittingBlocks(ctx); err != nil {
		cp.logger.Warnw("Block reconciliation had errors (non-fatal)", "error", err)
	}

	// Start share pipeline
	if err := cp.sharePipeline.Start(ctx); err != nil {
		return fmt.Errorf("failed to start share pipeline: %w", err)
	}
	cp.logger.Info("Share pipeline started")

	// Start job manager
	if err := cp.jobManager.Start(ctx); err != nil {
		return fmt.Errorf("failed to start job manager: %w", err)
	}
	cp.logger.Info("Job manager started")

	// Initialize block notifications
	if err := cp.initBlockNotifications(ctx); err != nil {
		return fmt.Errorf("failed to initialize block notifications: %w", err)
	}

	// Start stratum server
	if err := cp.stratumServer.Start(ctx); err != nil {
		return fmt.Errorf("failed to start stratum server: %w", err)
	}
	cp.logger.Infow("Stratum server started", "port", cp.cfg.Stratum.Port)

	// Start V2 stratum server (if configured)
	if cp.v2Server != nil {
		if err := cp.v2JobAdapter.Start(ctx); err != nil {
			return fmt.Errorf("failed to start V2 job adapter: %w", err)
		}
		if err := cp.v2Server.Start(ctx); err != nil {
			return fmt.Errorf("failed to start V2 server: %w", err)
		}
		cp.logger.Infow("V2 Stratum server started", "port", cp.v2Server.Port())
	}

	// V33 FIX: Stagger periodic goroutines with random jitter to prevent thundering herd
	// on startup. Without jitter, all tickers fire simultaneously on restart.

	// Start stats updater (jitter 0-10s)
	cp.wg.Add(1)
	go func() {
		jitter := time.Duration(rand.Intn(10000)) * time.Millisecond
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			cp.wg.Done()
			return
		}
		cp.statsLoop(ctx)
	}()

	// Start network difficulty updater (jitter 0-10s)
	cp.wg.Add(1)
	go func() {
		jitter := time.Duration(rand.Intn(10000)) * time.Millisecond
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			cp.wg.Done()
			return
		}
		cp.difficultyLoop(ctx)
	}()

	// Start celebration loop for block found announcements
	cp.wg.Add(1)
	go cp.celebrationLoop(ctx)

	// FIX D-9: Start periodic reconciliation loop for blocks stuck in "submitting"
	// This handles cases where daemon was unreachable at startup or blocks were
	// recorded but daemon failed mid-session
	cp.wg.Add(1)
	go cp.reconciliationLoop(ctx)

	// H-4: Start session cleanup loop to remove orphaned VARDIFF states
	cp.wg.Add(1)
	go func() {
		jitter := time.Duration(rand.Intn(15000)) * time.Millisecond
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			cp.wg.Done()
			return
		}
		cp.sessionCleanupLoop(ctx)
	}()

	cp.running = true
	cp.logger.Infow("Coin pool started successfully",
		"poolId", cp.poolID,
		"coin", cp.coinSymbol,
		"address", cp.cfg.Address,
	)

	return nil
}

// Stop gracefully shuts down the coin pool.
func (cp *CoinPool) Stop() error {
	cp.runMu.Lock()
	defer cp.runMu.Unlock()

	if !cp.running {
		return nil
	}

	cp.logger.Info("Shutting down coin pool...")

	// Cancel context
	if cp.cancel != nil {
		cp.cancel()
	}

	// Stop V2 server first (if running)
	if cp.v2Server != nil {
		if err := cp.v2Server.Stop(); err != nil {
			cp.logger.Errorw("Error stopping V2 stratum server", "error", err)
		}
	}

	// Stop accepting new connections
	if err := cp.stratumServer.Stop(); err != nil {
		cp.logger.Errorw("Error stopping stratum server", "error", err)
	}

	// Stop polling
	cp.stopPollingFallback()

	// Stop node manager
	if err := cp.nodeManager.Stop(); err != nil {
		cp.logger.Errorw("Error stopping node manager", "error", err)
	}

	// P-1 FIX: Enforce shutdown deadline to prevent SIGKILL.
	// Budget: 60s goroutine wait + ~40s pipeline flush + ~5s cleanup = ~105s total.
	deadline := time.Now().Add(105 * time.Second)

	// Wait for background goroutines with timeout
	done := make(chan struct{})
	go func() {
		cp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		cp.logger.Info("Background goroutines stopped cleanly")
	case <-time.After(60 * time.Second):
		// ORPHAN FIX #8 parity: Extended shutdown timeout for in-flight block submissions.
		// Reduced from 90s to 60s to leave room for pipeline flush within deadline.
		cp.logger.Warn("Timeout waiting for background goroutines, proceeding with shutdown")
	}

	// Stop share pipeline with deadline (P-1 FIX: prevents indefinite wg.Wait blocking)
	if err := cp.sharePipeline.StopWithDeadline(deadline); err != nil {
		cp.logger.Errorw("Error stopping share pipeline", "error", err)
	}

	// Close block WAL (flush and release file lock)
	if cp.blockWAL != nil {
		if err := cp.blockWAL.Close(); err != nil {
			cp.logger.Errorw("Error closing block WAL", "error", err)
		} else {
			cp.logger.Info("Block WAL closed")
		}
	}

	// Close block logger
	if cp.blockLogger != nil {
		if err := cp.blockLogger.Close(); err != nil {
			cp.logger.Errorw("Error closing block logger", "error", err)
		}
	}

	cp.running = false
	cp.logger.Info("Coin pool shutdown complete")
	return nil
}

// initBlockNotifications sets up block notifications.
func (cp *CoinPool) initBlockNotifications(ctx context.Context) error {
	// Start with RPC polling (reliable)
	cp.logger.Info("Starting RPC polling for block notifications")
	cp.startPollingFallback()

	// Check if any node has ZMQ enabled
	if cp.nodeManager.HasZMQ() {
		cp.logger.Info("ZMQ available, will promote when stable")
		cp.wg.Add(1)
		go cp.zmqPromotionLoop(ctx)
	}

	return nil
}

// startPollingFallback starts RPC polling for block notifications.
// CRITICAL FIX: Uses mutex to prevent race conditions and double-start
func (cp *CoinPool) startPollingFallback() {
	cp.pollingMu.Lock()
	defer cp.pollingMu.Unlock()

	if cp.usePolling {
		return
	}

	cp.usePolling = true
	cp.pollingStopCh = make(chan struct{})
	cp.pollingTicker = time.NewTicker(1 * time.Second)

	cp.wg.Add(1)
	go cp.pollingLoop()

	cp.logger.Info("Started RPC polling for block notifications (1s interval)")
}

// stopPollingFallback stops RPC polling.
// CRITICAL FIX: Uses mutex to prevent race conditions and double-close panic
func (cp *CoinPool) stopPollingFallback() {
	cp.pollingMu.Lock()
	defer cp.pollingMu.Unlock()

	if !cp.usePolling {
		return
	}

	cp.usePolling = false

	// CRITICAL FIX: Only close if channel exists and is not nil
	// This prevents panic from double-close
	if cp.pollingStopCh != nil {
		close(cp.pollingStopCh)
		cp.pollingStopCh = nil // Prevent double-close
	}

	if cp.pollingTicker != nil {
		cp.pollingTicker.Stop()
		cp.pollingTicker = nil
	}

	cp.logger.Info("Stopped RPC polling fallback")
}

// pollingLoop polls for new blocks.
//
// FIX: Daemon recovery detection. When RPC transitions from failed→success
// (e.g., daemon restart), force an immediate template refresh regardless of
// whether the block hash changed. Without this, a daemon restart at the same
// height leaves the pool serving stale work indefinitely — miners get no new
// jobs and all shares are rejected as "stale". This was observed in production
// as a 7+ minute outage after daemon restart.
func (cp *CoinPool) pollingLoop() {
	defer cp.wg.Done()

	// RACE FIX: Capture both ticker and stop channel locally under the mutex.
	// stopPollingFallback() closes pollingStopCh then sets both fields to nil.
	// Without local capture, the select re-reads cp.pollingStopCh each iteration;
	// if it reads nil (after stopPollingFallback nils the field), the nil channel
	// case blocks forever → goroutine leak. Local copies keep the original channel
	// object alive so close() still signals this goroutine.
	cp.pollingMu.Lock()
	ticker := cp.pollingTicker
	stopCh := cp.pollingStopCh
	cp.pollingMu.Unlock()

	if ticker == nil || stopCh == nil {
		return
	}

	var lastBlockHash string
	rpcWasDown := false // Track daemon connectivity for recovery detection

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

			bcInfo, err := cp.nodeManager.GetBlockchainInfo(ctx)
			cancel()

			if err != nil {
				if !rpcWasDown {
					cp.logger.Warnw("Polling: daemon RPC unreachable", "error", err)
				}
				rpcWasDown = true
				continue
			}

			// FIX: Daemon recovery — force template refresh on RPC reconnection.
			// When daemon restarts at the same height, BestBlockHash is unchanged
			// so the hash comparison below won't trigger. Force a refresh to ensure
			// miners get fresh work immediately after daemon recovery.
			if rpcWasDown {
				cp.logger.Infow("Polling: daemon RPC recovered — forcing template refresh",
					"height", bcInfo.Blocks,
					"hash", truncateHash(bcInfo.BestBlockHash),
				)
				rpcWasDown = false
				lastBlockHash = bcInfo.BestBlockHash

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := cp.jobManager.RefreshJob(ctx, true); err != nil {
					cp.logger.Warnw("Polling: forced refresh after recovery failed", "error", err)
				}
				cancel()
				continue
			}

			if bcInfo.BestBlockHash != lastBlockHash && lastBlockHash != "" {
				cp.logger.Infow("Polling: detected new block",
					"height", bcInfo.Blocks,
					"hash", truncateHash(bcInfo.BestBlockHash),
				)

				// FIX: Use OnBlockNotificationWithHash to pass the new tip hash
				// This enables same-height reorg detection via AdvanceWithTip
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				cp.jobManager.OnBlockNotificationWithHash(ctx, bcInfo.BestBlockHash)
				cancel()
			}

			lastBlockHash = bcInfo.BestBlockHash
		}
	}
}

// zmqPromotionLoop monitors ZMQ and promotes when stable.
// CRITICAL FIX: Use 10-second interval to match Pool v1 behavior
// The original 1-minute delay could cause missed block notifications on startup
func (cp *CoinPool) zmqPromotionLoop(ctx context.Context) {
	defer cp.wg.Done()

	// CRITICAL FIX: Changed from 1 minute to 10 seconds
	// This matches Pool v1 behavior and ensures faster ZMQ promotion
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	zmqWasDown := false // Track ZMQ failure state for recovery detection

	// Helper function to check ZMQ status
	// CRITICAL FIX: Uses mutex for thread-safe access to polling state
	checkZMQ := func() {
		// Check if already promoted (with mutex)
		cp.pollingMu.Lock()
		isPromoted := cp.zmqPromoted
		isPolling := cp.usePolling
		cp.pollingMu.Unlock()

		if isPromoted {
			// Monitor for ZMQ failures
			if cp.nodeManager.IsZMQFailed() {
				cp.logger.Warn("ZMQ failed, falling back to RPC polling")
				zmqWasDown = true
				cp.pollingMu.Lock()
				cp.zmqPromoted = false
				needPolling := !cp.usePolling
				cp.pollingMu.Unlock()
				if needPolling {
					cp.startPollingFallback()
				}
				// FIX: Force template refresh on ZMQ→polling fallback.
				// ZMQ failure means we may have missed block notifications.
				// Refresh immediately so miners don't get stale work.
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := cp.jobManager.RefreshJob(refreshCtx, true); err != nil {
					cp.logger.Warnw("Failed to refresh template after ZMQ fallback", "error", err)
				} else {
					cp.logger.Info("Template refreshed after ZMQ→polling fallback")
				}
				refreshCancel()
			}
			return
		}

		// Check if ZMQ is stable
		if cp.nodeManager.IsZMQStable() {
			cp.logger.Info("ZMQ stability confirmed! Promoting to primary")
			cp.pollingMu.Lock()
			cp.zmqPromoted = true
			cp.pollingMu.Unlock()
			cp.stopPollingFallback()
			cp.logger.Info("Block notifications now using ZMQ (low-latency mode)")

			// FIX: Force template refresh on ZMQ recovery after failure.
			// When ZMQ reconnects after daemon restart at the same height,
			// no block notification is sent (hash unchanged). Without this,
			// miners continue mining stale work indefinitely.
			if zmqWasDown {
				zmqWasDown = false
				cp.logger.Info("ZMQ recovered from failure — forcing template refresh")
				refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := cp.jobManager.RefreshJob(refreshCtx, true); err != nil {
					cp.logger.Warnw("Failed to refresh template after ZMQ recovery", "error", err)
				} else {
					cp.logger.Info("Template refreshed after ZMQ recovery")
				}
				refreshCancel()
			}
		}
		_ = isPolling // Silence unused variable warning
	}

	// CRITICAL FIX: Check immediately on startup - don't wait for first tick
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

// statsLoop periodically updates pool statistics.
func (cp *CoinPool) statsLoop(ctx context.Context) {
	defer cp.wg.Done()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cp.updateStats(ctx)
		}
	}
}

// updateStats updates pool statistics.
func (cp *CoinPool) updateStats(ctx context.Context) {
	stratumStats := cp.stratumServer.Stats()
	pipelineStats := cp.sharePipeline.Stats()

	bcInfo, err := cp.nodeManager.GetBlockchainInfo(ctx)
	if err != nil {
		cp.logger.Warnw("Failed to get blockchain info", "error", err)
		return
	}

	hashrate, hrErr := cp.db.GetPoolHashrateForPool(ctx, cp.poolID, 10, cp.coin.Algorithm())
	if hrErr != nil {
		cp.logger.Warnw("Failed to calculate pool hashrate", "error", hrErr)
	}

	// Use the algorithm-specific difficulty from the share validator (set by difficultyLoop
	// via getdifficulty RPC) instead of bcInfo.Difficulty from getblockchaininfo.
	// For multi-algo coins like DigiByte, getblockchaininfo returns a generic difficulty
	// that doesn't match the SHA-256d difficulty needed for accurate ETB calculations.
	networkDiff := cp.shareValidator.GetNetworkDifficulty()
	if networkDiff <= 0 {
		// Fallback to bcInfo.Difficulty if validator hasn't been initialized yet
		networkDiff = bcInfo.Difficulty
	}

	stats := &database.PoolStats{
		ConnectedMiners:      int(stratumStats.ActiveConnections),
		PoolHashrate:         hashrate,
		SharesPerSecond:      float64(pipelineStats.Processed) / 60.0,
		NetworkDifficulty:    networkDiff,
		BlockHeight:          bcInfo.Blocks,
		LastNetworkBlockTime: time.Unix(bcInfo.MedianTime, 0),
	}

	// Update Prometheus metrics — only update if DB query succeeded
	if cp.metricsServer != nil && hrErr == nil {
		cp.metricsServer.UpdateHashrate(hashrate)
	}

	if err := cp.db.UpdatePoolStatsForPool(ctx, cp.poolID, stats); err != nil {
		cp.logger.Warnw("Failed to update pool stats", "error", err)
	}
}

// difficultyLoop periodically updates network difficulty.
func (cp *CoinPool) difficultyLoop(ctx context.Context) {
	defer cp.wg.Done()

	// Fetch immediately on startup (don't wait 30 seconds)
	diff, err := cp.nodeManager.GetDifficulty(ctx)
	if err != nil {
		cp.logger.Warnw("Failed to get initial network difficulty", "error", err)
	} else {
		cp.shareValidator.SetNetworkDifficulty(diff)
		cp.logger.Infow("Initial network difficulty set", "difficulty", diff, "coin", cp.coinSymbol)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			diff, err := cp.nodeManager.GetDifficulty(ctx)
			if err != nil {
				cp.logger.Warnw("Failed to get network difficulty", "error", err)
				continue
			}
			cp.shareValidator.SetNetworkDifficulty(diff)
			cp.logger.Debugw("Network difficulty updated", "difficulty", diff, "coin", cp.coinSymbol)
		}
	}
}

// Stats returns current coin pool statistics.
func (cp *CoinPool) Stats() CoinPoolStats {
	stratumStats := cp.stratumServer.Stats()
	pipelineStats := cp.sharePipeline.Stats()
	validatorStats := cp.shareValidator.Stats()
	nodeStats := cp.nodeManager.Stats()

	return CoinPoolStats{
		PoolID:         cp.poolID,
		Coin:           cp.coinSymbol,
		Connections:    stratumStats.ActiveConnections,
		TotalShares:    validatorStats.Validated,
		AcceptedShares: validatorStats.Accepted,
		RejectedShares: validatorStats.Rejected,
		SharesInBuffer: pipelineStats.BufferCurrent,
		SharesWritten:  pipelineStats.Written,
		NodeStats:      nodeStats,
	}
}

// CoinPoolStats represents statistics for a coin pool.
type CoinPoolStats struct {
	PoolID         string
	Coin           string
	Connections    int64
	TotalShares    uint64
	AcceptedShares uint64
	RejectedShares uint64
	SharesInBuffer int
	SharesWritten  uint64
	NodeStats      nodemanager.ManagerStats
}

// Symbol returns the coin symbol.
func (cp *CoinPool) Symbol() string {
	return cp.coinSymbol
}

// PoolID returns the pool ID.
func (cp *CoinPool) PoolID() string {
	return cp.poolID
}

// IsRunning returns whether the coin pool is running.
func (cp *CoinPool) IsRunning() bool {
	cp.runMu.Lock()
	defer cp.runMu.Unlock()
	return cp.running
}

// CoinPoolProvider interface implementation for V2 API server.
// These methods expose pool stats for the REST API.

// GetConnections returns the number of active miner connections.
func (cp *CoinPool) GetConnections() int64 {
	return cp.stratumServer.Stats().ActiveConnections
}

// GetHashrate returns the pool's current hashrate.
// This returns the hashrate calculated from the database (shares over time window).
func (cp *CoinPool) GetHashrate() float64 {
	if cp.db == nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use 10-minute window for hashrate calculation
	hashrate, err := cp.db.GetPoolHashrate(ctx, 10)
	if err != nil {
		cp.logger.Warnw("Failed to get pool hashrate from database", "error", err)
		return 0
	}

	return hashrate
}

// GetSharesPerSecond returns the current share submission rate.
func (cp *CoinPool) GetSharesPerSecond() float64 {
	// Calculate from validator stats - shares per time period
	stats := cp.shareValidator.Stats()
	// Return a rough estimate based on accepted shares
	// Real-time calculation would require tracking timestamps
	return float64(stats.Accepted) / 3600.0 // Rough hourly rate
}

// GetBlockHeight returns the current block height from the node.
func (cp *CoinPool) GetBlockHeight() uint64 {
	// Get from the current job which has the block height
	if cp.jobManager != nil {
		job := cp.jobManager.GetCurrentJob()
		if job != nil {
			return job.Height
		}
	}
	return 0
}

// GetNetworkDifficulty returns the current network difficulty.
func (cp *CoinPool) GetNetworkDifficulty() float64 {
	return cp.shareValidator.GetNetworkDifficulty()
}

// GetNetworkHashrate returns the computed network hashrate for this coin.
func (cp *CoinPool) GetNetworkHashrate() float64 {
	netDiff := cp.shareValidator.GetNetworkDifficulty()
	bt := getAlgoBlockTime(cp.coinSymbol)
	if netDiff > 0 && bt > 0 {
		return netDiff * math.Pow(2, 32) / bt
	}
	return 0
}

// GetBlocksFound returns blocks found (V2 placeholder - returns 0).
func (cp *CoinPool) GetBlocksFound() int64 {
	return 0
}

// GetBlockReward returns the current block reward from the job template.
func (cp *CoinPool) GetBlockReward() float64 {
	if cp.jobManager != nil {
		job := cp.jobManager.GetCurrentJob()
		if job != nil {
			return float64(job.CoinbaseValue) / 1e8
		}
	}
	return 0
}

// GetPoolEffort returns pool effort (V2 placeholder - returns 0).
func (cp *CoinPool) GetPoolEffort() float64 {
	return 0
}

// GetStratumPort returns the stratum port for this coin pool.
func (cp *CoinPool) GetStratumPort() int {
	return cp.cfg.Stratum.Port
}

// BlockWALDir returns the directory containing this coin's WAL files.
// Returns empty string if WAL is not initialized.
func (cp *CoinPool) BlockWALDir() string {
	if cp.blockWAL == nil {
		return ""
	}
	return filepath.Dir(cp.blockWAL.FilePath())
}

// GetNodeStats returns the current node manager statistics.
func (cp *CoinPool) GetNodeStats() nodemanager.ManagerStats {
	return cp.nodeManager.Stats()
}

// IsWALRecoveryRunning returns true if WAL recovery is currently in progress.
func (cp *CoinPool) IsWALRecoveryRunning() bool {
	return cp.walRecoveryRunning.Load()
}

// GetSharePipeline returns the share pipeline for metrics wiring.
func (cp *CoinPool) GetSharePipeline() *shares.Pipeline {
	return cp.sharePipeline
}

// ══════════════════════════════════════════════════════════════════════════════
// MERGE MINING (AuxPoW) SUPPORT
// ══════════════════════════════════════════════════════════════════════════════
// These methods enable merge mining where the parent chain (BTC/LTC) can also
// mine aux chains (DOGE/NMC/etc) simultaneously. Critically, aux blocks can be
// found and paid INDEPENDENTLY of parent blocks - the hash just needs to meet
// the aux chain's difficulty, not the parent's.
// ══════════════════════════════════════════════════════════════════════════════

// SetAuxManager configures merge mining support for this coin pool.
// This must be called before Start() if merge mining is desired.
//
// The auxManager provides:
//   - Aux block template fetching and refresh
//   - Aux coin implementations for proof serialization
//   - Submitter for sending aux blocks to aux daemons
//
// CRITICAL: This wires up the validator to check aux targets independently
// of the parent chain, enabling the core merge mining benefit.
func (cp *CoinPool) SetAuxManager(mgr *auxpow.Manager) {
	cp.auxManager = mgr
	cp.auxSubmitter = auxpow.NewSubmitter(mgr, cp.logger.Desugar())

	// Wire the aux manager to the validator so it can check aux targets
	cp.shareValidator.SetAuxManager(mgr)

	cp.logger.Infow("Merge mining enabled",
		"auxChainCount", mgr.AuxChainCount(),
	)
}

// GetAuxManager returns the AuxPoW manager (nil if merge mining is disabled).
func (cp *CoinPool) GetAuxManager() *auxpow.Manager {
	return cp.auxManager
}

// SetRedisDedupTracker sets the Redis dedup tracker for HA block submission coordination.
// AUDIT FIX (ISSUE-3): V1 PARITY — when set, block submissions use Redis SETNX to prevent
// duplicate daemon RPC calls across HA nodes during failover windows.
func (cp *CoinPool) SetRedisDedupTracker(tracker *shares.RedisDedupTracker) {
	cp.redisDedupTracker = tracker
}

// SetSyncStatusCallback sets the callback for reporting sync status changes.
// This is used by the Coordinator to report sync status to the VIPManager
// for HA master election decisions.
func (cp *CoinPool) SetSyncStatusCallback(fn func(coin string, syncPct float64, height int64)) {
	cp.onSyncStatusChange = fn
}

// handleAuxBlocks processes aux blocks found in the share result.
// This is called for EVERY accepted share, independent of whether it's a parent block.
//
// CRITICAL: An aux block can be found even when the share doesn't meet the parent
// chain's difficulty. This is the fundamental benefit of merge mining - you get
// paid on aux chains "for free" while mining the parent.
func (cp *CoinPool) handleAuxBlocks(share *protocol.Share, auxResults []protocol.AuxBlockResult) {
	if cp.auxSubmitter == nil {
		return
	}

	// HA ROLE GATE: Backup nodes must not submit aux blocks.
	// Without this check, a backup node could submit aux blocks to the daemon,
	// causing duplicate submissions or conflicts with the master node.
	// BUG FIX (M7): Check != RoleMaster instead of == RoleBackup.
	if ha.Role(cp.haRole.Load()) != ha.RoleMaster {
		cp.logger.Debugw("Skipping aux block submission (not master node)",
			"coin", cp.coinSymbol,
			"auxBlocks", len(auxResults),
		)
		return
	}

	for _, auxResult := range auxResults {
		if !auxResult.IsBlock {
			// Not a block for this aux chain (didn't meet target)
			if auxResult.Error != "" {
				cp.logger.Warnw("Aux block validation error",
					"symbol", auxResult.Symbol,
					"height", auxResult.Height,
					"error", auxResult.Error,
				)
			}
			continue
		}

		// Found an aux block! Log it prominently
		cp.logger.Infow("🎉 AUX BLOCK FOUND!",
			"symbol", auxResult.Symbol,
			"height", auxResult.Height,
			"hash", auxResult.BlockHash,
			"chainId", auxResult.ChainID,
			"reward", float64(auxResult.CoinbaseValue)/1e8,
			"miner", share.MinerAddress,
			"worker", share.WorkerName,
			"note", "Found via merge mining - independent of parent block!",
		)

		// AUDIT FIX (ISSUE-8): Log aux block found to dedicated block logger.
		// This is critical for non-sampled block event auditing.
		if cp.blockLogger != nil {
			cp.blockLogger.LogAuxBlockFound(
				auxResult.Symbol,
				auxResult.Height,
				auxResult.BlockHash,
				share.MinerAddress,
				float64(auxResult.CoinbaseValue)/1e8,
			)
		}

		// Convert to auxpow.AuxBlockResult for submission
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
		// AUDIT FIX: Use roleCtx as parent so demotion cancels in-flight aux submissions.
		cp.roleMu.Lock()
		auxBaseCtx := cp.roleCtx
		cp.roleMu.Unlock()
		auxHeightCtx, auxHeightCancel := cp.jobManager.HeightContext(auxBaseCtx)
		auxDeadlineCtx, auxDeadlineCancel := context.WithTimeout(auxHeightCtx, cp.submitTimeouts.SubmitDeadline)

		// AUDIT FIX: WAL pre-submission write for aux blocks (crash recovery).
		// Without this, a crash during aux submission loses the block entirely.
		if cp.blockWAL != nil {
			auxPreEntry := &BlockWALEntry{
				Timestamp:    time.Now(),
				Height:       auxResult.Height,
				BlockHash:    auxResult.BlockHash,
				MinerAddress: share.MinerAddress,
				CoinbaseValue: auxResult.CoinbaseValue,
				Status:       "aux_submitting",
				AuxSymbol:    auxResult.Symbol, // FIX: Use dedicated field instead of SubmitError
			}
			if walErr := cp.blockWAL.LogBlockFound(auxPreEntry); walErr != nil {
				cp.logger.Errorw("Failed to write aux pre-submission WAL entry",
					"error", walErr,
					"symbol", auxResult.Symbol,
					"height", auxResult.Height,
				)
			}
		}

		submitCtx, submitCancel := context.WithTimeout(auxDeadlineCtx, cp.submitTimeouts.SubmitTimeout)
		err := cp.auxSubmitter.SubmitAuxBlock(submitCtx, submitResult)
		submitCancel()

		auxSubmitted := false
		if err == nil {
			auxSubmitted = true
		} else {
			errStr := err.Error()
			if isPermanentRejection(errStr) {
				cp.logger.Errorw("AUX BLOCK REJECTED (permanent - no retry)",
					"symbol", auxResult.Symbol,
					"height", auxResult.Height,
					"hash", auxResult.BlockHash,
					"error", err,
				)
			} else {
				// Transient error - deadline-driven retry loop (same pattern as parent block)
				attempt := 2
				for auxDeadlineCtx.Err() == nil && attempt <= cp.submitTimeouts.MaxRetries+1 {
					time.Sleep(cp.submitTimeouts.RetrySleep)
					if auxDeadlineCtx.Err() != nil {
						break
					}

					retryCtx, retryCancel := context.WithTimeout(auxDeadlineCtx, cp.submitTimeouts.RetryTimeout)
					retryErr := cp.auxSubmitter.SubmitAuxBlock(retryCtx, submitResult)
					retryCancel()

					if retryErr == nil {
						cp.logger.Infow("Aux block submitted on retry!",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"attempt", attempt,
						)
						auxSubmitted = true
						break
					}

					if isPermanentRejection(retryErr.Error()) {
						cp.logger.Errorw("AUX BLOCK REJECTED on retry (permanent)",
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
					if auxDeadlineCtx.Err() != nil {
						cp.logger.Warnw("Aux block submission aborted: deadline expired or height changed",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"hash", auxResult.BlockHash,
						)
					} else {
						cp.logger.Errorw("AUX BLOCK FOUND BUT SUBMISSION FAILED after retries!",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"hash", auxResult.BlockHash,
							"error", err,
						)
					}
				}
			}
		}
		auxDeadlineCancel()
		auxHeightCancel()

		if auxSubmitted {
			cp.logger.Infow("✅ Aux block submitted successfully!",
				"symbol", auxResult.Symbol,
				"height", auxResult.Height,
				"hash", auxResult.BlockHash,
				"reward", float64(auxResult.CoinbaseValue)/1e8,
			)

			// Celebrate merge-mined block (same light show as parent blocks)
			cp.startCelebration(share.SessionID, auxResult.Height, share.MinerAddress, share.WorkerName, float64(auxResult.CoinbaseValue)/1e8, auxResult.Symbol)

			// FIX: Update WAL entry from "aux_submitting" to "aux_pending" after successful submission.
			// Without this, the WAL entry stays in "aux_submitting" forever and crash recovery
			// would incorrectly try to resubmit an already-accepted block.
			if cp.blockWAL != nil {
				auxPostEntry := &BlockWALEntry{
					Timestamp:    time.Now(),
					Height:       auxResult.Height,
					BlockHash:    auxResult.BlockHash,
					MinerAddress: share.MinerAddress,
					AuxSymbol:    auxResult.Symbol,
					Status:       "aux_pending",
				}
				if walErr := cp.blockWAL.LogSubmissionResult(auxPostEntry); walErr != nil {
					cp.logger.Errorw("Failed to write aux post-submission WAL entry",
						"error", walErr,
						"symbol", auxResult.Symbol,
						"height", auxResult.Height,
					)
				}
			}

			// AUDIT FIX (ISSUE-6): Increment AuxBlocksSubmitted metric.
			if cp.metricsServer != nil {
				cp.metricsServer.RecordAuxBlockSubmitted(auxResult.Symbol)
			}
			// AUDIT FIX (ISSUE-8): Log aux block submission to dedicated block logger.
			if cp.blockLogger != nil {
				cp.blockLogger.LogAuxBlockSubmitted(auxResult.Symbol, auxResult.Height, auxResult.BlockHash, true, "")
			}

			// Record aux block in database
			if cp.db != nil {
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

				dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
				// Use a separate pool ID for aux blocks to track them separately
				// AUDIT FIX: Use underscore separator and lowercase symbol.
				// The validPoolID regex ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$ rejects hyphens,
				// so using "-" caused every aux block DB insert to silently fail.
				auxPoolID := cp.poolID + "_" + strings.ToLower(auxResult.Symbol)
				insertErr := cp.db.InsertBlockForPool(dbCtx, auxPoolID, auxBlock)
				dbCancel()
				if insertErr != nil {
					cp.logger.Errorw("CRITICAL: Failed to record aux block in database — merge-mined reward at risk",
						"symbol", auxResult.Symbol,
						"height", auxResult.Height,
						"hash", auxResult.BlockHash,
						"auxPoolID", auxPoolID,
						"reward", float64(auxResult.CoinbaseValue)/1e8,
						"error", insertErr,
					)
					// AUDIT FIX (BF-1): Retry once after 2s — matching V1 pattern (pool.go).
					// This aux block is on-chain; losing the DB record = lost reward.
					time.Sleep(2 * time.Second)
					retryCtx, retryCancel := context.WithTimeout(context.Background(), 10*time.Second)
					retryErr := cp.db.InsertBlockForPool(retryCtx, auxPoolID, auxBlock)
					retryCancel()
					if retryErr != nil {
						cp.logger.Errorw("CRITICAL: Aux block DB insert retry FAILED — manual reconciliation required",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
							"hash", auxResult.BlockHash,
							"auxPoolID", auxPoolID,
							"error", retryErr,
						)
					} else {
						cp.logger.Infow("Aux block DB insert succeeded on retry",
							"symbol", auxResult.Symbol,
							"height", auxResult.Height,
						)
					}
				}
			}
		} else {
			// AUDIT FIX (ISSUE-6): Increment AuxBlocksFailed metric on submission failure.
			// Non-zero rate means aux chain blocks are being lost.
			if cp.metricsServer != nil {
				cp.metricsServer.RecordAuxBlockFailed(auxResult.Symbol, "submission_failed")
			}
			// AUDIT FIX (ISSUE-8): Log aux block rejection to dedicated block logger.
			if cp.blockLogger != nil {
				reason := "submission_failed_after_retries"
				if err != nil {
					reason = err.Error()
				}
				cp.blockLogger.LogAuxBlockSubmitted(auxResult.Symbol, auxResult.Height, auxResult.BlockHash, false, reason)
			}
		}
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// HA ROLE CHANGE SUPPORT
// ══════════════════════════════════════════════════════════════════════════════
// These methods handle HA role transitions for multi-coin V2 deployments.
// The Coordinator propagates role changes to each CoinPool so per-coin
// components can react appropriately.
// ══════════════════════════════════════════════════════════════════════════════

// OnHARoleChange is called by the Coordinator when the HA role changes.
// Each CoinPool updates its per-coin components: metrics and logging.
// Payment processing is handled at the Coordinator/Pool level, not per-CoinPool.
func (cp *CoinPool) OnHARoleChange(oldRole, newRole ha.Role) {
	cp.haRole.Store(int32(newRole))

	cp.logger.Infow("CoinPool HA role changed",
		"coin", cp.coinSymbol,
		"from", oldRole.String(),
		"to", newRole.String(),
	)

	// Flush block WAL on any role change to ensure durability
	if cp.blockWAL != nil {
		if err := cp.blockWAL.FlushToDisk(); err != nil {
			cp.logger.Warnw("HA role change: failed to flush block WAL",
				"coin", cp.coinSymbol,
				"error", err,
			)
		}
	}

	switch newRole {
	case ha.RoleMaster:
		cp.logger.Infow("CoinPool promoted to MASTER - full operation",
			"coin", cp.coinSymbol,
		)
		// AUDIT FIX: Create fresh role context for the new master session.
		// AF-3 PARITY: Cancel old roleCtx before replacing to prevent context leak.
		cp.roleMu.Lock()
		if cp.roleCancel != nil {
			cp.roleCancel()
		}
		cp.roleCtx, cp.roleCancel = context.WithCancel(context.Background())
		cp.roleMu.Unlock()

		// V1 PARITY FIX (F-6): Immediately recover any blocks that were in-flight
		// during a rapid demotion-promotion cycle. Without this, blocks stuck in
		// "submitting" state in the WAL would not be recovered until the next
		// 5-minute reconciliation cycle or process restart.
		cp.wg.Add(1)
		go func() {
			defer cp.wg.Done()
			// BUG FIX (H3): Add panic recovery to match V1 pool.go parity (AUDIT FIX SP-5).
			// Without this, a panic in WAL recovery (nil pointer, unexpected file format)
			// crashes the entire stratum process during HA promotion.
			defer func() {
				if r := recover(); r != nil {
					cp.logger.Errorw("PANIC recovered in post-promotion WAL recovery", "panic", r)
				}
			}()
			cp.recoverWALAfterPromotion()
		}()
	case ha.RoleBackup, ha.RoleObserver:
		// BUG FIX (M8): RoleObserver must also cancel submissions and create new context.
		cp.logger.Infow("CoinPool demoted - stratum continues, no block submissions",
			"coin", cp.coinSymbol,
			"role", newRole.String(),
		)
		// AUDIT FIX: Cancel in-flight block submissions immediately on demotion.
		// Any handleBlock/handleAuxBlocks goroutine using roleCtx as parent will
		// have its context cancelled, aborting stale RPC calls.
		cp.roleMu.Lock()
		if cp.roleCancel != nil {
			cp.roleCancel()
		}
		cp.roleCtx, cp.roleCancel = context.WithCancel(context.Background())
		cp.roleMu.Unlock()
	}

	// Update per-coin metrics if metrics server is available
	if cp.metricsServer != nil {
		cp.metricsServer.SetHANodeRole(strings.ToLower(newRole.String()))
	}
}

// recoverWALAfterPromotion recovers blocks stuck in WAL after a rapid demotion-promotion cycle.
// V1 PARITY FIX (F-6): Mirrors V1 Pool.recoverWALAfterPromotion() (pool.go:3011).
// Uses recoverWALBlocks (Phase 1: unsubmitted blocks) and reconcileSubmittingBlocks
// (Phase 2: DB reconciliation) which together cover the same recovery as V1.
func (cp *CoinPool) recoverWALAfterPromotion() {
	if cp.blockWAL == nil {
		return
	}

	// Re-entrancy guard: prevent concurrent recovery from rapid demotion-promotion cycles.
	if !cp.walRecoveryRunning.CompareAndSwap(false, true) {
		cp.logger.Warnw("Post-promotion WAL recovery already in progress, skipping duplicate",
			"coin", cp.coinSymbol,
		)
		return
	}
	defer cp.walRecoveryRunning.Store(false)

	// Brief delay to let daemon connections stabilize after promotion
	time.Sleep(2 * time.Second)

	cp.logger.Infow("Post-promotion WAL recovery starting...",
		"coin", cp.coinSymbol,
	)

	// Phase 1: Recover unsubmitted blocks from WAL
	walDir := filepath.Dir(cp.blockWAL.FilePath())
	recoveryCtx, recoveryCancel := context.WithTimeout(cp.roleCtx, 60*time.Second)
	cp.recoverWALBlocks(recoveryCtx, walDir)
	recoveryCancel()

	// Phase 2: Reconcile blocks stuck in "submitting" state in DB
	if cp.db != nil {
		reconcileCtx, reconcileCancel := context.WithTimeout(cp.roleCtx, 30*time.Second)
		if err := cp.reconcileSubmittingBlocks(reconcileCtx); err != nil {
			cp.logger.Warnw("Post-promotion block reconciliation had errors",
				"coin", cp.coinSymbol,
				"error", err,
			)
		}
		reconcileCancel()
	}

	cp.logger.Infow("Post-promotion WAL recovery complete",
		"coin", cp.coinSymbol,
	)
}

// ══════════════════════════════════════════════════════════════════════════════
// LIFECYCLE HELPERS
// ══════════════════════════════════════════════════════════════════════════════
// These methods implement startup gates, cleanup, crash recovery, and periodic
// maintenance loops that mirror V1 pool.go functionality for full parity.
// ══════════════════════════════════════════════════════════════════════════════

// waitForSync blocks until the daemon is fully synchronized.
// This prevents miners from wasting hashrate on stale blocks during IBD.
// Mirrors V1 Pool.waitForSync() with identical thresholds and behavior.
func (cp *CoinPool) waitForSync(ctx context.Context) error {
	const (
		syncThreshold = 0.990 // 99.0% - realistic threshold that accounts for verificationProgress drift
		checkInterval = 10 * time.Second
		warnAfter     = 30 * time.Minute  // Warn after 30 minutes
		warnInterval  = 15 * time.Minute  // Repeat warning every 15 minutes
	)

	cp.logger.Infow("SYNC GATE: Waiting for daemon to complete synchronization...",
		"coin", cp.coinSymbol,
	)
	cp.logger.Infow("Sync requirements",
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
			if elapsed > warnAfter && (lastWarnTime.IsZero() || time.Since(lastWarnTime) > warnInterval) {
				cp.logger.Warnw("SYNC GATE: Still waiting for node to sync - this is normal after improper shutdown",
					"coin", cp.coinSymbol,
					"elapsed", elapsed.Round(time.Minute),
					"hint", "Node may be reindexing/resyncing. HDD systems can take 1+ hours. Check node logs for progress.",
				)
				lastWarnTime = time.Now()
			}

			// Query daemon sync status via node manager
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			bcInfo, err := cp.nodeManager.GetBlockchainInfo(checkCtx)
			cancel()

			if err != nil {
				cp.logger.Warnw("Sync check failed, retrying...",
					"coin", cp.coinSymbol,
					"error", err,
					"elapsed", elapsed.Round(time.Second),
					"hint", "Node may still be starting up or RPC not ready yet",
				)
				continue
			}

			// Log progress
			cp.logger.Infow("Sync status",
				"coin", cp.coinSymbol,
				"blocks", bcInfo.Blocks,
				"headers", bcInfo.Headers,
				"progress", fmt.Sprintf("%.4f%%", bcInfo.VerificationProgress*100),
				"ibd", bcInfo.InitialBlockDownload,
				"elapsed", elapsed.Round(time.Second),
			)

			// Report sync status to VIPManager for HA master election decisions
			if cp.onSyncStatusChange != nil {
				cp.onSyncStatusChange(cp.coinSymbol, bcInfo.VerificationProgress*100, int64(bcInfo.Blocks))
			}

			// Check sync criteria
			// On regtest, skip IBD check entirely — regtest is a private test network
			// with no peers, so there's no risk of mining on stale blocks. This allows
			// the pool to start on a fresh chain (height 0) where IBD is always true.
			if bcInfo.Chain == "regtest" || (!bcInfo.InitialBlockDownload && bcInfo.VerificationProgress >= syncThreshold) {
				cp.logger.Infow("SYNC GATE PASSED: Daemon is fully synced",
					"coin", cp.coinSymbol,
					"blocks", bcInfo.Blocks,
					"progress", fmt.Sprintf("%.4f%%", bcInfo.VerificationProgress*100),
					"chain", bcInfo.Chain,
					"waitTime", elapsed.Round(time.Second),
				)
				// Report 100% sync to VIPManager - critical for HA master election
				// On regtest, VerificationProgress may be 0 but we consider it synced
				if cp.onSyncStatusChange != nil {
					cp.onSyncStatusChange(cp.coinSymbol, 100.0, int64(bcInfo.Blocks))
				}
				return nil
			}

			// Still syncing - show helpful status with ETA estimate
			if bcInfo.Headers > 0 && bcInfo.Blocks < bcInfo.Headers {
				blocksRemaining := bcInfo.Headers - bcInfo.Blocks
				var etaStr string
				if bcInfo.Blocks > 0 && elapsed.Seconds() > 60 {
					blocksPerSecond := float64(bcInfo.Blocks) / elapsed.Seconds()
					if blocksPerSecond > 0 {
						etaSeconds := float64(blocksRemaining) / blocksPerSecond
						etaStr = (time.Duration(etaSeconds) * time.Second).Round(time.Minute).String()
					}
				}
				cp.logger.Infow("Still syncing...",
					"coin", cp.coinSymbol,
					"blocksRemaining", blocksRemaining,
					"percentComplete", fmt.Sprintf("%.2f%%", bcInfo.VerificationProgress*100),
					"estimatedTimeRemaining", etaStr,
				)
			}
		}
	}
}

// cleanupStaleShares removes old shares from the database on startup.
// This prevents inflated hashrate calculations from stale data left by
// a previous instance crash or unclean shutdown.
func (cp *CoinPool) cleanupStaleShares(ctx context.Context) error {
	if cp.db == nil {
		return nil
	}

	// Clean up shares older than 15 minutes (1.5x the hashrate window)
	retentionMinutes := 15

	deleted, err := cp.db.CleanupStaleShares(ctx, retentionMinutes)
	if err != nil {
		return err
	}

	if deleted > 0 {
		cp.logger.Infow("Cleaned up stale shares from previous session",
			"coin", cp.coinSymbol,
			"deleted", deleted,
			"retention_minutes", retentionMinutes,
		)
	}

	return nil
}

// recoverWALBlocks recovers unsubmitted blocks from the WAL after a crash.
// It checks each block against the daemon to determine if resubmission is needed,
// and performs WAL-DB reconciliation to ensure every accepted block has a DB record.
// Mirrors V1 Pool WAL recovery logic in New() with identical verification strategy.
func (cp *CoinPool) recoverWALBlocks(ctx context.Context, walDir string) {
	// ═══════════════════════════════════════════════════════════════════════════
	// PHASE 1: Recover unsubmitted blocks (status "pending" or "submitting")
	// ═══════════════════════════════════════════════════════════════════════════
	unsubmittedBlocks, err := RecoverUnsubmittedBlocks(walDir)
	if err != nil {
		cp.logger.Warnw("Failed to check for unsubmitted blocks", "coin", cp.coinSymbol, "error", err)
	} else if len(unsubmittedBlocks) > 0 {
		cp.logger.Warnw("CRASH RECOVERY: Found unsubmitted blocks from previous session!",
			"coin", cp.coinSymbol,
			"count", len(unsubmittedBlocks),
		)

		for _, block := range unsubmittedBlocks {
			cp.logger.Infow("WAL RECOVERY: Checking unsubmitted block",
				"coin", cp.coinSymbol,
				"height", block.Height,
				"hash", block.BlockHash,
				"miner", block.MinerAddress,
				"timestamp", block.Timestamp,
				"auxSymbol", block.AuxSymbol,
			)

			// BUG FIX: Aux blocks need different recovery — they use submitauxblock
			// (not submitblock), their proof data isn't stored in WAL, and they must
			// be verified against the AUX chain daemon (not the parent chain).
			// Without this, aux "aux_submitting" WAL entries were checked against the
			// parent chain (wrong heights/hashes), then marked "rejected" because
			// BlockHex is empty. The block would also be inserted into the wrong table
			// with the wrong type. Mirrors pool.go V1 aux recovery (pool.go:3523-3608).
			if block.AuxSymbol != "" {
				var auxDaemonClient *daemon.Client
				if cp.auxManager != nil {
					for _, auxCfg := range cp.auxManager.GetAuxChainConfigs() {
						if strings.EqualFold(auxCfg.Symbol, block.AuxSymbol) && auxCfg.Enabled {
							auxDaemonClient = auxCfg.DaemonClient
							break
						}
					}
				}

				if auxDaemonClient == nil {
					cp.logger.Warnw("WAL RECOVERY: cannot verify aux block — no daemon client found",
						"coin", cp.coinSymbol,
						"symbol", block.AuxSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
					)
					continue
				}

				// Verify if aux block was accepted by the aux chain daemon
				verifyCtx, verifyCancel := context.WithTimeout(ctx, 10*time.Second)
				auxHash, hashErr := auxDaemonClient.GetBlockHash(verifyCtx, block.Height)
				verifyCancel()

				if hashErr == nil && auxHash == block.BlockHash {
					cp.logger.Infow("WAL RECOVERY: aux block confirmed in chain",
						"coin", cp.coinSymbol,
						"symbol", block.AuxSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
					)

					block.Status = "submitted"
					block.SubmitError = "recovered_on_startup: aux block found in chain"
					if cp.blockWAL != nil {
						cp.blockWAL.LogSubmissionResult(&block)
					}

					// Ensure DB record exists in the correct aux pool table
					if cp.db != nil {
						auxPoolID := cp.poolID + "_" + strings.ToLower(block.AuxSymbol)
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
						insertCtx, insertCancel := context.WithTimeout(ctx, 10*time.Second)
						if insertErr := cp.db.InsertBlockForPool(insertCtx, auxPoolID, dbBlock); insertErr != nil {
							cp.logger.Errorw("CRITICAL: Failed to ensure aux block in DB during WAL recovery",
								"coin", cp.coinSymbol,
								"symbol", block.AuxSymbol,
								"auxPoolID", auxPoolID,
								"height", block.Height,
								"hash", block.BlockHash,
								"error", insertErr,
							)
						} else {
							cp.logger.Infow("Ensured aux block record exists in database",
								"coin", cp.coinSymbol,
								"symbol", block.AuxSymbol,
								"auxPoolID", auxPoolID,
								"height", block.Height,
							)
						}
						insertCancel()
					}
				} else {
					// Aux block not in chain — cannot resubmit (proof data not stored in WAL).
					// The aux proof is built from the parent coinbase and is ephemeral.
					cp.logger.Warnw("WAL RECOVERY: aux block NOT in chain — proof data unavailable, cannot resubmit",
						"coin", cp.coinSymbol,
						"symbol", block.AuxSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
					)
					block.Status = "failed"
					block.SubmitError = "recovered_on_startup: aux block not in chain and proof data unavailable"
					if cp.blockWAL != nil {
						cp.blockWAL.LogSubmissionResult(&block)
					}
				}
				continue
			}

			// Step 1: Check if block is already in the chain
			checkCtx, checkCancel := context.WithTimeout(ctx, 30*time.Second)
			chainHash, hashErr := cp.nodeManager.GetBlockHash(checkCtx, block.Height)
			checkCancel()

			if hashErr == nil && chainHash == block.BlockHash {
				// Block is already in chain — update WAL status
				cp.logger.Infow("WAL RECOVERY: Block already accepted by network - no resubmission needed",
					"coin", cp.coinSymbol,
					"height", block.Height,
					"hash", block.BlockHash,
				)
				block.Status = "submitted"
				block.SubmitError = "recovered_on_startup: block already in chain"
				if cp.blockWAL != nil {
					cp.blockWAL.LogSubmissionResult(&block)
				}

				// V22 FIX: Ensure block exists in database for payment processing
				if cp.db != nil {
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
					insertCtx, insertCancel := context.WithTimeout(ctx, 10*time.Second)
					if insertErr := cp.db.InsertBlockForPool(insertCtx, cp.poolID, dbBlock); insertErr != nil {
						cp.logger.Errorw("CRITICAL: Failed to ensure block in database during WAL recovery",
							"coin", cp.coinSymbol,
							"height", block.Height,
							"hash", block.BlockHash,
							"error", insertErr,
						)
					} else {
						cp.logger.Infow("Ensured block record exists in database",
							"coin", cp.coinSymbol,
							"height", block.Height,
							"hash", block.BlockHash,
							"miner", block.MinerAddress,
						)
					}
					insertCancel()
				}
				continue
			}

			// Step 2: Block not at expected height — check if resubmission is viable
			infoCtx, infoCancel := context.WithTimeout(ctx, 10*time.Second)
			chainInfo, infoErr := cp.nodeManager.GetBlockchainInfo(infoCtx)
			infoCancel()

			if infoErr == nil {
				currentHeight := chainInfo.Blocks
				blockAge := currentHeight - block.Height

				if blockAge > 100 {
					// Block is too old — chain has moved on
					cp.logger.Warnw("WAL RECOVERY: Block too old for resubmission - marking as stale",
						"coin", cp.coinSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
						"blockAge", blockAge,
						"currentHeight", currentHeight,
					)
					block.Status = "rejected"
					block.SubmitError = "recovered_stale: block too old (chain moved " + fmt.Sprintf("%d", blockAge) + " blocks ahead)"
					if cp.blockWAL != nil {
						cp.blockWAL.LogSubmissionResult(&block)
					}
					continue
				}

				// Block is recent enough — attempt resubmission
				if block.BlockHex != "" {
					cp.logger.Infow("WAL RECOVERY: Attempting resubmission of recent block",
						"coin", cp.coinSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
						"blockAge", blockAge,
					)

					submitCtx, submitCancel := context.WithTimeout(ctx, 60*time.Second)
					submitErr := cp.nodeManager.SubmitBlock(submitCtx, block.BlockHex)
					submitCancel()

					if submitErr == nil {
						cp.logger.Infow("WAL RECOVERY: Block resubmitted successfully!",
							"coin", cp.coinSymbol,
							"height", block.Height,
							"hash", block.BlockHash,
						)
						block.Status = "submitted"
						block.SubmitError = "recovered_resubmitted_on_startup"

						// Ensure resubmitted block exists in database
						if cp.db != nil {
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
							insertCtx, insertCancel := context.WithTimeout(ctx, 10*time.Second)
							if insertErr := cp.db.InsertBlockForPool(insertCtx, cp.poolID, dbBlock); insertErr != nil {
								cp.logger.Errorw("CRITICAL: Failed to ensure resubmitted block in database",
									"coin", cp.coinSymbol,
									"height", block.Height,
									"hash", block.BlockHash,
									"error", insertErr,
								)
							} else {
								cp.logger.Infow("Ensured resubmitted block exists in database",
									"coin", cp.coinSymbol,
									"height", block.Height,
									"hash", block.BlockHash,
									"miner", block.MinerAddress,
								)
							}
							insertCancel()
						}
					} else {
						cp.logger.Warnw("WAL RECOVERY: Block resubmission failed",
							"coin", cp.coinSymbol,
							"height", block.Height,
							"hash", block.BlockHash,
							"error", submitErr,
						)
						block.Status = "rejected"
						block.SubmitError = "recovered_resubmit_failed: " + submitErr.Error()
					}
					if cp.blockWAL != nil {
						cp.blockWAL.LogSubmissionResult(&block)
					}

				} else {
					cp.logger.Warnw("WAL RECOVERY: Block hex missing - cannot resubmit",
						"coin", cp.coinSymbol,
						"height", block.Height,
						"hash", block.BlockHash,
					)
					block.Status = "rejected"
					block.SubmitError = "recovered_no_hex: block data not available for resubmission"
					if cp.blockWAL != nil {
						cp.blockWAL.LogSubmissionResult(&block)
					}
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PHASE 2: WAL-DB reconciliation (V25 FIX)
	// Ensure every successfully submitted block has a corresponding DB record.
	// This catches the edge case where a block was submitted to the daemon and
	// WAL was updated, but the DB insert failed (transient PG error or crash).
	// InsertBlockForPool is idempotent — if the record already exists, it's a no-op.
	// ═══════════════════════════════════════════════════════════════════════════
	submittedBlocks, reconcileErr := RecoverSubmittedBlocks(walDir)
	if reconcileErr != nil {
		cp.logger.Warnw("Failed to read WAL for reconciliation",
			"coin", cp.coinSymbol,
			"error", reconcileErr,
		)
	} else if len(submittedBlocks) > 0 && cp.db != nil {
		reconciledCount := 0
		for _, block := range submittedBlocks {
			// BUG FIX: Aux blocks must use the correct pool ID and type.
			// Without this, aux blocks were inserted into the parent pool table
			// with type "block" instead of the aux pool table with type "auxpow".
			targetPoolID := cp.poolID
			blockType := "block"
			if block.AuxSymbol != "" {
				targetPoolID = cp.poolID + "_" + strings.ToLower(block.AuxSymbol)
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
			rCtx, rCancel := context.WithTimeout(ctx, 10*time.Second)
			if insertErr := cp.db.InsertBlockForPool(rCtx, targetPoolID, dbBlock); insertErr != nil {
				cp.logger.Errorw("CRITICAL: Failed to reconcile WAL entry with database",
					"coin", cp.coinSymbol,
					"height", block.Height,
					"hash", block.BlockHash,
					"auxSymbol", block.AuxSymbol,
					"targetPoolID", targetPoolID,
					"error", insertErr,
				)
			} else {
				reconciledCount++
			}
			rCancel()
		}
		cp.logger.Infow("WAL-DB reconciliation complete",
			"coin", cp.coinSymbol,
			"submittedEntries", len(submittedBlocks),
			"reconciled", reconciledCount,
		)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PHASE 3: WAL retention cleanup
	// Both RecoverUnsubmittedBlocks and RecoverSubmittedBlocks have already
	// processed all entries, so old files can be safely removed.
	// ═══════════════════════════════════════════════════════════════════════════
	if cleaned, cleanErr := CleanupOldWALFiles(walDir, DefaultWALRetentionDays); cleanErr != nil {
		cp.logger.Warnw("WAL cleanup failed", "coin", cp.coinSymbol, "error", cleanErr)
	} else if cleaned > 0 {
		cp.logger.Infow("WAL cleanup complete",
			"coin", cp.coinSymbol,
			"filesRemoved", cleaned,
			"retentionDays", DefaultWALRetentionDays,
		)
	}
}

// sessionCleanupLoop periodically removes orphaned VARDIFF session states
// that are no longer associated with active stratum connections.
// H-4 FIX: Without this, leaked session states cause unbounded memory growth.
func (cp *CoinPool) sessionCleanupLoop(ctx context.Context) {
	defer cp.wg.Done()

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
			cp.cleanupStaleSessions(staleThreshold)
			// Evict stale nonce tracking entries for uncleanly disconnected sessions
			if cp.shareValidator != nil {
				cp.shareValidator.CleanupNonceTracker()
			}
		}
	}
}

// cleanupStaleSessions removes VARDIFF states for sessions that haven't
// submitted shares recently and are no longer actively connected.
func (cp *CoinPool) cleanupStaleSessions(staleThreshold time.Duration) {
	now := time.Now()
	var staleCount, activeCount int

	// Get list of currently active session IDs from stratum server
	activeSessions := cp.stratumServer.GetActiveSessionIDs()
	activeSet := make(map[uint64]bool, len(activeSessions))
	for _, id := range activeSessions {
		activeSet[id] = true
	}

	// Iterate through all session states
	cp.sessionStates.Range(func(key, value interface{}) bool {
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
			cp.sessionStates.Delete(sessionID)
			atomic.AddInt64(&cp.sessionStateCount, -1)
			staleCount++
		}

		return true
	})

	if staleCount > 0 {
		cp.logger.Infow("Cleaned up stale VARDIFF session states",
			"coin", cp.coinSymbol,
			"staleRemoved", staleCount,
			"activeRemaining", activeCount,
		)
	}
}

// verifyBlockAcceptance performs enhanced post-timeout block verification.
// When block submission times out, this method uses multiple verification
// strategies to determine if the daemon actually accepted the block.
// V1 parity: uses hash-at-height verification with retry intervals.
// Now also uses GetBlock-by-hash (Method 2) to detect blocks at different heights
// or during reorg timing. The retry strategy compensates for propagation delay.
func (cp *CoinPool) verifyBlockAcceptance(blockHash string, blockHeight uint64) bool {
	// Retry intervals: 5s, 10s, 15s (total 30s verification window)
	retryIntervals := []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second}

	for attempt, interval := range retryIntervals {
		ctx, cancel := context.WithTimeout(context.Background(), interval)

		// Method 1: Get the block hash at the expected height
		chainHash, err := cp.nodeManager.GetBlockHash(ctx, blockHeight)
		if err == nil && chainHash == blockHash {
			cancel()
			cp.logger.Infow("Post-timeout verification: Block found at expected height",
				"coin", cp.coinSymbol,
				"height", blockHeight,
				"hash", blockHash,
				"attempt", attempt+1,
			)
			return true
		}

		// Method 2: Block may be in chain at different height (reorg timing)
		if err == nil {
			blockInfo, blkErr := cp.nodeManager.GetBlock(ctx, blockHash)
			if blkErr == nil && blockInfo != nil {
				if confs, ok := blockInfo["confirmations"].(float64); ok && confs < 0 {
					cancel()
					cp.logger.Warnw("Post-timeout verification: Block exists but on stale fork",
						"coin", cp.coinSymbol, "hash", blockHash,
						"confirmations", confs, "attempt", attempt+1,
					)
					continue
				}
				cancel()
				cp.logger.Infow("Post-timeout verification: Block found in chain by hash",
					"coin", cp.coinSymbol, "hash", blockHash,
					"confirmations", blockInfo["confirmations"], "attempt", attempt+1,
				)
				return true
			}
		}

		if err != nil {
			// Primary node failed — the retry on next interval will re-query
			// through nodeManager's automatic failover
			cp.logger.Debugw("Post-timeout verification: GetBlockHash failed on this attempt",
				"coin", cp.coinSymbol,
				"height", blockHeight,
				"error", err,
				"attempt", attempt+1,
			)
		}

		cancel()

		// Log progress for debugging
		if attempt < len(retryIntervals)-1 {
			cp.logger.Warnw("Post-timeout verification: Block not found yet, will retry",
				"coin", cp.coinSymbol,
				"height", blockHeight,
				"hash", blockHash,
				"attempt", attempt+1,
				"nextRetryIn", retryIntervals[attempt+1],
			)
			// Brief sleep before next attempt to let daemon process
			time.Sleep(1 * time.Second)
		}
	}

	cp.logger.Warnw("Post-timeout verification: Block not found after all retries",
		"coin", cp.coinSymbol,
		"height", blockHeight,
		"hash", blockHash,
		"totalAttempts", len(retryIntervals),
	)
	return false
}
