// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package config - V2 configuration with multi-node failover support
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// validPoolID matches valid PostgreSQL identifiers (alphanumeric and underscores, 1-63 chars)
// Used to validate pool_id before reaching the database layer for better error messages.
var validPoolID = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// truncateAddress truncates an address to fit within maxLen characters,
// adding "..." if truncated. Used for display formatting.
func truncateAddress(addr string, maxLen int) string {
	if len(addr) <= maxLen {
		return addr
	}
	if maxLen <= 3 {
		return addr[:maxLen]
	}
	return addr[:maxLen-3] + "..."
}

// ConfigV2 represents the V2 pool configuration with multi-coin and multi-node support.
type ConfigV2 struct {
	Version   int              `yaml:"version"`  // Config version (2)
	Global    GlobalConfig     `yaml:"global"`   // Global settings
	Database  DatabaseConfig   `yaml:"database"` // Shared database
	Coins     []CoinPoolConfig `yaml:"coins"`    // Per-coin pool configurations
	MultiPort MultiPortConfig  `yaml:"multi_port,omitempty"` // Multi-coin smart port (v2.1)
	VIP       VIPConfig        `yaml:"vip,omitempty"`        // VIP (Virtual IP) for miner failover
	HA        HAConfig         `yaml:"ha,omitempty"`         // High Availability coordination
}

// MultiPortConfig configures the multi-coin smart port.
// When enabled, miners connect to a single port and the pool distributes
// mining time across SHA-256d coins on a 24-hour UTC schedule based on
// configured weights. For example, DGB:80 + BTC:20 = 19.2h DGB + 4.8h BTC.
type MultiPortConfig struct {
	Enabled       bool          `yaml:"enabled"`
	Port          int           `yaml:"port"`                    // Multi-port stratum port (default: 16180)
	TLSPort       int           `yaml:"tls_port,omitempty"`      // Optional TLS port
	Coins         map[string]CoinRouteConfig `yaml:"coins"`     // Coin symbol → routing config (weight)
	CheckInterval time.Duration `yaml:"check_interval,omitempty"` // How often to check schedule (default 30s)
	PreferCoin    string        `yaml:"prefer_coin,omitempty"`   // Default coin / tie-breaker
	MinTimeOnCoin time.Duration `yaml:"min_time_on_coin,omitempty"` // Minimum time before switch (default 60s)
	Timezone      string        `yaml:"timezone,omitempty"`      // IANA timezone for schedule (default: use display_timezone from sentinel config, fallback UTC)
}

// CoinRouteConfig holds per-coin routing parameters.
type CoinRouteConfig struct {
	Weight int `yaml:"weight"` // Percentage of daily mining time (0-100, must sum to 100)
}

// CoinSymbols returns the list of coin symbols configured for multi-port.
func (m *MultiPortConfig) CoinSymbols() []string {
	symbols := make([]string, 0, len(m.Coins))
	for sym := range m.Coins {
		symbols = append(symbols, sym)
	}
	return symbols
}

// GlobalConfig contains settings that apply across all coins.
type GlobalConfig struct {
	LogLevel    string `yaml:"log_level"`    // debug, info, warn, error
	LogFormat   string `yaml:"log_format"`   // json, text
	MetricsPort      int    `yaml:"metrics_port"`       // Prometheus metrics port
	MetricsAuthToken string `yaml:"metrics_auth_token,omitempty"` // SECURITY: Bearer token for /metrics endpoint
	APIPort          int    `yaml:"api_port"`           // REST API port
	APIBindAddress   string `yaml:"api_bind_address"`   // Bind address for API server (default: "0.0.0.0", use "127.0.0.1" for local-only)
	APIEnabled       bool   `yaml:"api_enabled"`
	AdminAPIKey      string `yaml:"admin_api_key"`      // SECURITY: API key for admin endpoints (device hints, etc.)
	Sentinel    SentinelConfig `yaml:"sentinel,omitempty"` // API Sentinel monitoring (internal pool health)

	// Celebration settings - display messages on miner screens when blocks are found
	Celebration CelebrationConfig `yaml:"celebration,omitempty"`
}

// CelebrationConfig configures the block found celebration display on miners.
// When a block is found, the pool sends messages to miner displays (Avalon/cgminer).
type CelebrationConfig struct {
	// Enabled controls whether celebration messages are sent.
	// When true, block found messages are sent to the finder AND broadcast to all miners.
	// This provides a visual indicator on all Avalon devices when the pool finds a block.
	Enabled bool `yaml:"enabled"`

	// DurationHours specifies how long to display periodic celebration messages.
	// Default: 4 hours. Periodic "keep mining!" reminders are sent every 5 minutes.
	DurationHours int `yaml:"duration_hours,omitempty"`
}

// SentinelConfig configures the API Sentinel alerting subsystem.
// APISentinel runs inside the Coordinator, checks all CoinPools at a configurable
// interval, evaluates alert conditions, logs alerts, updates metrics, and exposes
// them via /api/sentinel/alerts for Spiral Sentinel (Python) to consume.
type SentinelConfig struct {
	Enabled               bool          `yaml:"enabled"`
	CheckInterval         time.Duration `yaml:"check_interval,omitempty"`          // Default: 60s
	WALStuckThreshold     time.Duration `yaml:"wal_stuck_threshold,omitempty"`     // Default: 10m
	BlockDroughtHours     int           `yaml:"block_drought_hours,omitempty"`     // Default: 0 (disabled)
	DisconnectDropPercent int           `yaml:"disconnect_drop_percent,omitempty"` // Default: 30
	HashrateDropPercent   int           `yaml:"hashrate_drop_percent,omitempty"`   // Default: 30
	WALDiskSpaceWarningMB int           `yaml:"wal_disk_space_warning_mb,omitempty"` // Default: 500
	WALMaxFiles           int           `yaml:"wal_max_files,omitempty"`           // Default: 60
	FalseRejectionThreshold float64     `yaml:"false_rejection_threshold,omitempty"` // Default: 0.10
	RetryRateThreshold    int           `yaml:"retry_rate_threshold,omitempty"`    // Default: 5 per hour
	AlertCooldown         time.Duration `yaml:"alert_cooldown,omitempty"`          // Default: 15m
	PaymentStallChecks    int           `yaml:"payment_stall_checks,omitempty"`    // Default: 5 (checks without progress before CRITICAL)
	GoroutineLimit        int           `yaml:"goroutine_limit,omitempty"`         // Default: 10000 (absolute warning threshold)
	NodeHealthThreshold   float64       `yaml:"node_health_threshold,omitempty"`   // Default: 0.5 (per-node health score warning)
	HAFlapWindow          time.Duration `yaml:"ha_flap_window,omitempty"`          // Default: 10m (window for flap detection)
	HAFlapThreshold       int           `yaml:"ha_flap_threshold,omitempty"`       // Default: 3 (max role changes in window)
	OrphanRateThreshold   float64       `yaml:"orphan_rate_threshold,omitempty"`   // Default: 0.20 (20% orphan rate warning)
	ChainTipStallMinutes  int           `yaml:"chain_tip_stall_minutes,omitempty"` // Default: 30 (alert if daemon height unchanged for this long)
	MinPeerCount          int           `yaml:"min_peer_count,omitempty"`          // Default: 3 (alert if daemon peer count drops below this)
	MaturityStallHours    int           `yaml:"maturity_stall_hours,omitempty"`    // Default: 6 (alert if found block pending for this long)
	HostnameOverride      string        `yaml:"hostname_override,omitempty"`       // M13: Override os.Hostname() in webhook payloads (for NAT/container environments)
	// Deprecated: Webhooks are no longer fired directly by API Sentinel.
	// Alerts are now exposed via /api/sentinel/alerts and consumed by the Python
	// Spiral Sentinel which handles all external notifications (Discord/Telegram).
	// This field is kept for backward compatibility — existing configs won't break.
	Webhooks              []WebhookConfig `yaml:"webhooks,omitempty"`
}

// WebhookConfig defines an external webhook endpoint for Sentinel alerts.
type WebhookConfig struct {
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers,omitempty"`
	ChatID  string            `yaml:"chat_id,omitempty"` // Required for Telegram bots
	Token   string            `yaml:"token,omitempty"`   // M4: Separated bot token (e.g., Telegram bot token via SPIRAL_TELEGRAM_BOT_TOKEN)
}

// SetSentinelDefaults applies default values to SentinelConfig.
func (s *SentinelConfig) SetSentinelDefaults() {
	if s.CheckInterval == 0 {
		s.CheckInterval = 60 * time.Second
	}
	if s.WALStuckThreshold == 0 {
		s.WALStuckThreshold = 10 * time.Minute
	}
	if s.DisconnectDropPercent == 0 {
		s.DisconnectDropPercent = 30
	}
	if s.HashrateDropPercent == 0 {
		s.HashrateDropPercent = 30
	}
	if s.WALDiskSpaceWarningMB == 0 {
		s.WALDiskSpaceWarningMB = 500
	}
	if s.WALMaxFiles == 0 {
		s.WALMaxFiles = 60
	}
	if s.FalseRejectionThreshold == 0 {
		s.FalseRejectionThreshold = 0.10
	}
	if s.RetryRateThreshold == 0 {
		s.RetryRateThreshold = 5
	}
	if s.AlertCooldown == 0 {
		s.AlertCooldown = 15 * time.Minute
	}
	if s.PaymentStallChecks == 0 {
		s.PaymentStallChecks = 5
	}
	if s.GoroutineLimit == 0 {
		s.GoroutineLimit = 10000
	}
	if s.NodeHealthThreshold == 0 {
		s.NodeHealthThreshold = 0.5
	}
	if s.HAFlapWindow == 0 {
		s.HAFlapWindow = 10 * time.Minute
	}
	if s.HAFlapThreshold == 0 {
		s.HAFlapThreshold = 3
	}
	if s.OrphanRateThreshold == 0 {
		s.OrphanRateThreshold = 0.20
	}
	if s.ChainTipStallMinutes == 0 {
		s.ChainTipStallMinutes = 30
	}
	if s.MinPeerCount == 0 {
		s.MinPeerCount = 3
	}
	if s.MaturityStallHours == 0 {
		s.MaturityStallHours = 6
	}
}

// CoinPoolConfig defines the complete configuration for mining a single coin.
type CoinPoolConfig struct {
	Symbol  string `yaml:"symbol"`  // Coin symbol: DGB, BTC, BCH
	PoolID  string `yaml:"pool_id"` // Unique pool identifier
	Enabled bool   `yaml:"enabled"` // Whether this coin pool is active
	Address string `yaml:"address"` // Pool wallet address for this coin

	// Coinbase settings
	CoinbaseText string `yaml:"coinbase_text,omitempty"` // Pool tag in coinbase

	// Stratum settings for this coin
	Stratum CoinStratumConfig `yaml:"stratum"`

	// Daemon nodes (multiple for failover)
	Nodes []NodeConfig `yaml:"nodes"`

	// Payment settings for this coin
	Payments CoinPaymentConfig `yaml:"payments,omitempty"`

	// Merge mining (AuxPoW) configuration for this coin pool
	MergeMining MergeMiningConfig `yaml:"mergeMining,omitempty"`

	// SkipGenesisVerify disables genesis block hash verification at startup.
	// USE ONLY FOR REGTEST/TESTNET — regtest has a different genesis hash than mainnet.
	SkipGenesisVerify bool `yaml:"skip_genesis_verify,omitempty"`
}

// CoinStratumConfig defines stratum settings for a specific coin.
// NOTE: V2 uses automatic miner routing via user-agent detection.
// All miners connect to the same port; difficulty is auto-set based on miner type.
type CoinStratumConfig struct {
	Port           int              `yaml:"port"`               // Stratum V1 port (REQUIRED)
	PortV2         int              `yaml:"port_v2,omitempty"`  // Stratum V2 port (optional — 0 = disabled)
	PortTLS        int              `yaml:"port_tls,omitempty"` // TLS stratum port (optional — 0 = disabled)
	TLS            CoinTLSConfig    `yaml:"tls,omitempty"`      // TLS certificate/key config (required when port_tls > 0)
	Difficulty     DifficultyConfig `yaml:"difficulty"`
	Banning        BanningConfig    `yaml:"banning,omitempty"`
	Connection     ConnectionConfig `yaml:"connection,omitempty"`
	VersionRolling VersionRolling   `yaml:"version_rolling,omitempty"`
	JobRebroadcast time.Duration    `yaml:"job_rebroadcast,omitempty"`
}

// CoinTLSConfig defines TLS settings for encrypted V1 stratum connections.
// Required when port_tls > 0. Provides stratum+ssl:// listener for V1 miners.
type CoinTLSConfig struct {
	CertFile   string `yaml:"cert_file"`              // Path to TLS certificate (PEM)
	KeyFile    string `yaml:"key_file"`               // Path to TLS private key (PEM)
	MinVersion string `yaml:"min_version,omitempty"`  // Minimum TLS version: "1.2" or "1.3" (default: "1.2")
}

// NodeConfig defines a single daemon node with optional ZMQ.
type NodeConfig struct {
	ID       string         `yaml:"id"`            // Unique node identifier (e.g., "primary", "backup-1")
	Host     string         `yaml:"host"`          // Hostname or IP
	Port     int            `yaml:"port"`          // RPC port
	User     string         `yaml:"user"`          // RPC username
	Password string         `yaml:"password"`      // RPC password
	Priority int            `yaml:"priority"`      // Lower = preferred (0 = highest)
	Weight   int            `yaml:"weight"`        // Load balancing weight (higher = more traffic)
	ZMQ      *NodeZMQConfig `yaml:"zmq,omitempty"` // Optional ZMQ configuration
}

// NodeZMQConfig defines ZMQ settings for a node.
// CRITICAL: Timing parameters must be tuned per-coin based on block time.
// Default values are auto-calculated in SetDefaults() based on the coin's block time.
// - Fast coins (DGB 15s blocks): aggressive timing (1s initial, 15s max)
// - Slow coins (BTC 600s blocks): relaxed timing (5s initial, 120s max)
type NodeZMQConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"` // e.g., "tcp://127.0.0.1:28332"

	// Reconnection settings with exponential backoff
	// These are auto-calculated from block time if not specified
	ReconnectInitial    time.Duration `yaml:"reconnect_initial,omitempty"`     // Initial retry delay
	ReconnectMax        time.Duration `yaml:"reconnect_max,omitempty"`         // Maximum retry delay (should be <= block time)
	ReconnectFactor     float64       `yaml:"reconnect_factor,omitempty"`      // Backoff multiplier (default 1.5 for fast coins, 2.0 for slow)
	FailureThreshold    time.Duration `yaml:"failure_threshold,omitempty"`     // How long to fail before polling fallback
	StabilityPeriod     time.Duration `yaml:"stability_period,omitempty"`      // How long healthy before disabling polling
	HealthCheckInterval time.Duration `yaml:"health_check_interval,omitempty"` // Health check frequency
}

// CoinPaymentConfig defines payment settings for a coin.
type CoinPaymentConfig struct {
	Enabled        bool          `yaml:"enabled"`
	Interval       time.Duration `yaml:"interval"`
	MinimumPayment float64       `yaml:"minimum_payment"`
	Scheme         string        `yaml:"scheme"`                   // SOLO only (currently)
	BlockMaturity  int           `yaml:"blockMaturity,omitempty"` // Override coin default (0 = use coin's CoinbaseMaturity)
	BlockTime      int           `yaml:"blockTime,omitempty"`     // Block time in seconds (auto-populated from SupportedCoins)
	DeepReorgMaxAge uint64       `yaml:"deepReorgMaxAge,omitempty"` // Deep reorg verification depth (auto-populated)
}

// LoadV2 loads a V2 configuration file.
// This function only loads configs that explicitly have version: 2.
// V1 configs are NOT migrated - use Load() for V1 configs.
func LoadV2(path string) (*ConfigV2, error) {
	// G304: Path is provided by administrator via CLI flag, not untrusted input
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Check version first
	var versionCheck struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &versionCheck); err != nil {
		return nil, fmt.Errorf("failed to parse config version: %w", err)
	}

	// Accept version 1 or 2 for multi-coin format
	// Version 1 with coins array = multi-coin mode
	// Version 2 with coins array = multi-coin mode
	if versionCheck.Version != 1 && versionCheck.Version != 2 {
		return nil, fmt.Errorf("unsupported config version: %d (expected 1 or 2)", versionCheck.Version)
	}

	var cfg ConfigV2
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse V2 config: %w", err)
	}

	// AUDIT FIX (CB-1): Resolve credentials BEFORE validation.
	// V1 order: ResolveCredentials → ValidateCredentials → Validate → SetDefaults
	// Previous V2 order was wrong: Validate → SetDefaults → ResolveCredentials
	// This caused placeholder password detection to run against YAML values instead
	// of env-resolved values, and empty env vars could silently blank passwords.
	cfg.ResolveCredentials()
	cfg.ResolveWebhookCredentials()

	// AUDIT FIX (CB-2): Validate credentials are non-empty after resolution.
	// This was missing entirely from V2, making it vulnerable to the same
	// "0 TH/s hashrate" production incident that was fixed for V1 (C-1 fix).
	if err := cfg.ValidateCredentials(); err != nil {
		return nil, fmt.Errorf("credential validation failed: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	cfg.SetDefaults()
	return &cfg, nil
}

// Validate checks the configuration for required fields.
func (c *ConfigV2) Validate() error {
	if c.Version != 1 && c.Version != 2 {
		return fmt.Errorf("expected version 1 or 2, got %d", c.Version)
	}

	if len(c.Coins) == 0 {
		return fmt.Errorf("at least one coin must be configured")
	}

	// Validate database
	if c.Database.Host == "" {
		return fmt.Errorf("database.host is required")
	}

	// Track used ports and pool IDs to detect conflicts
	usedPorts := make(map[int]string)
	usedPoolIDs := make(map[string]int) // pool_id -> coin index

	// Track DGB and DGB-SCRYPT addresses for consistency warning
	var dgbAddress, dgbScryptAddress string
	var dgbIndex, dgbScryptIndex int = -1, -1

	for i, coin := range c.Coins {
		if coin.Symbol == "" {
			return fmt.Errorf("coins[%d].symbol is required", i)
		}

		if coin.PoolID == "" {
			return fmt.Errorf("coins[%d].pool_id is required", i)
		}

		// Validate pool_id format early to give clear error messages
		// Pool IDs must be valid PostgreSQL identifiers (alphanumeric/underscore, 1-63 chars)
		// Common mistake: using hyphens (dgb-sha256) instead of underscores (dgb_sha256)
		if !validPoolID.MatchString(coin.PoolID) {
			return fmt.Errorf("coins[%d].pool_id %q is invalid: must start with letter/underscore, contain only letters/digits/underscores, and be 1-63 chars. Hint: use underscores instead of hyphens (e.g., dgb_sha256 not dgb-sha256)", i, coin.PoolID)
		}

		// Check for pool_id uniqueness across all coins
		if existingIdx, exists := usedPoolIDs[coin.PoolID]; exists {
			return fmt.Errorf("coins[%d].pool_id %q conflicts with coins[%d]: pool IDs must be unique", i, coin.PoolID, existingIdx)
		}
		usedPoolIDs[coin.PoolID] = i

		// Track DGB and DGB-SCRYPT for address consistency check
		if coin.Enabled {
			switch strings.ToUpper(coin.Symbol) {
			case "DGB":
				dgbAddress = coin.Address
				dgbIndex = i
			case "DGB-SCRYPT", "DGB_SCRYPT":
				dgbScryptAddress = coin.Address
				dgbScryptIndex = i
			}
		}

		if coin.Enabled {
			if coin.Address == "" {
				return fmt.Errorf("coins[%d].address is required when enabled", i)
			}
			if coin.Address == "PENDING_GENERATION" {
				return fmt.Errorf("coins[%d].address is set to 'PENDING_GENERATION' - wallet not yet created for %s. "+
					"Run 'systemctl restart spiralstratum' after blockchain sync completes, "+
					"or manually set the address in config.yaml", i, coin.Symbol)
			}

			// BC2/BTC ADDRESS COLLISION WARNING (Audit Recommendation #5)
			// Bitcoin II uses identical address formats to Bitcoin (same P2PKH/P2SH/Bech32 prefixes).
			// There is NO way to programmatically distinguish a BC2 address from a BTC address.
			// Emit a prominent warning to ensure operators verify their address was generated
			// by Bitcoin II Core, not Bitcoin Core.
			if strings.ToUpper(coin.Symbol) == "BC2" {
				fmt.Println("")
				fmt.Println("╔═══════════════════════════════════════════════════════════════════════════╗")
				fmt.Println("║  ⚠️  CRITICAL WARNING: Bitcoin II (BC2) Address Verification Required     ║")
				fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
				fmt.Println("║  BC2 uses IDENTICAL address formats to Bitcoin (BTC).                     ║")
				fmt.Println("║  There is NO way to programmatically verify which chain an address        ║")
				fmt.Println("║  belongs to - a BTC address will APPEAR valid for BC2!                    ║")
				fmt.Println("║                                                                           ║")
				fmt.Printf("║  Your configured BC2 address: %-43s ║\n", truncateAddress(coin.Address, 43))
				fmt.Println("║                                                                           ║")
				fmt.Println("║  VERIFY: This address was generated by Bitcoin II Core wallet,            ║")
				fmt.Println("║          NOT by Bitcoin Core. Rewards sent to a BTC address on the        ║")
				fmt.Println("║          BC2 network will be PERMANENTLY LOST!                            ║")
				fmt.Println("╚═══════════════════════════════════════════════════════════════════════════╝")
				fmt.Println("")
			}

			if len(coin.Nodes) == 0 {
				return fmt.Errorf("coins[%d].nodes: at least one node required", i)
			}

			// Validate nodes
			nodeIDs := make(map[string]bool)
			for j, node := range coin.Nodes {
				if node.ID == "" {
					return fmt.Errorf("coins[%d].nodes[%d].id is required", i, j)
				}
				if nodeIDs[node.ID] {
					return fmt.Errorf("coins[%d].nodes[%d]: duplicate node id '%s'", i, j, node.ID)
				}
				nodeIDs[node.ID] = true

				if node.Host == "" {
					return fmt.Errorf("coins[%d].nodes[%d].host is required", i, j)
				}
				// Validate node credentials - warn about missing but don't fail
				// (credentials may come from SPIRAL_ADMIN_API_KEY env var or config.yaml)
				if node.User == "" || node.Password == "" {
					// Soft check - credentials MUST be in config.yaml or env vars.
					// The pool does NOT read daemon .conf files directly.
				}
			}

			// STRATUM PORT VALIDATION (Audit Recommendation #6)
			// A missing or zero stratum port means miners cannot connect to this coin.
			// This is a hard error for enabled coins to prevent silent configuration failures.
			port := coin.Stratum.Port
			if port <= 0 {
				return fmt.Errorf("coins[%d].stratum.port is required for enabled coin %s: "+
					"miners cannot connect without a valid stratum port", i, coin.Symbol)
			}
			if port > 65535 {
				return fmt.Errorf("coins[%d].stratum.port %d is invalid for %s: "+
					"port must be between 1 and 65535", i, port, coin.Symbol)
			}
			if existingCoin, exists := usedPorts[port]; exists {
				return fmt.Errorf("port %d conflict: used by both %s and %s",
					port, existingCoin, coin.Symbol)
			}
			usedPorts[port] = coin.Symbol

			// V2 PORT VALIDATION
			if coin.Stratum.PortV2 > 0 {
				if coin.Stratum.PortV2 > 65535 {
					return fmt.Errorf("coins[%d].stratum.port_v2 %d is invalid for %s: "+
						"port must be between 1 and 65535", i, coin.Stratum.PortV2, coin.Symbol)
				}
				if existingCoin, exists := usedPorts[coin.Stratum.PortV2]; exists {
					return fmt.Errorf("port %d conflict: V2 port for %s conflicts with %s",
						coin.Stratum.PortV2, coin.Symbol, existingCoin)
				}
				usedPorts[coin.Stratum.PortV2] = coin.Symbol + " (V2)"
			}

			// TLS PORT + CONFIG VALIDATION
			if coin.Stratum.PortTLS > 0 {
				if coin.Stratum.PortTLS > 65535 {
					return fmt.Errorf("coins[%d].stratum.port_tls %d is invalid for %s: "+
						"port must be between 1 and 65535", i, coin.Stratum.PortTLS, coin.Symbol)
				}
				if existingCoin, exists := usedPorts[coin.Stratum.PortTLS]; exists {
					return fmt.Errorf("port %d conflict: TLS port for %s conflicts with %s",
						coin.Stratum.PortTLS, coin.Symbol, existingCoin)
				}
				usedPorts[coin.Stratum.PortTLS] = coin.Symbol + " (TLS)"

				// TLS cert/key are required when TLS port is configured
				if coin.Stratum.TLS.CertFile == "" {
					return fmt.Errorf("coins[%d] (%s): stratum.tls.cert_file is required when port_tls is set",
						i, coin.Symbol)
				}
				if coin.Stratum.TLS.KeyFile == "" {
					return fmt.Errorf("coins[%d] (%s): stratum.tls.key_file is required when port_tls is set",
						i, coin.Symbol)
				}
			}
		}
	}

	// Validate merge mining configuration for each enabled coin
	for i, coin := range c.Coins {
		if !coin.Enabled || !coin.MergeMining.Enabled {
			continue
		}
		if len(coin.MergeMining.AuxChains) == 0 {
			return fmt.Errorf("coins[%d] (%s): mergeMining.enabled is true but no auxChains configured",
				i, coin.Symbol)
		}
		for j, aux := range coin.MergeMining.AuxChains {
			if aux.Symbol == "" {
				return fmt.Errorf("coins[%d] (%s) auxChains[%d].symbol is required", i, coin.Symbol, j)
			}
			if aux.Address == "" {
				return fmt.Errorf("coins[%d] (%s) auxChains[%d].address is required for %s",
					i, coin.Symbol, j, aux.Symbol)
			}
			if aux.Daemon.Host == "" {
				return fmt.Errorf("coins[%d] (%s) auxChains[%d].daemon.host is required for %s",
					i, coin.Symbol, j, aux.Symbol)
			}
		}
	}

	// Validate payment scheme — only SOLO is currently supported
	for i, coin := range c.Coins {
		if !coin.Enabled {
			continue
		}
		scheme := strings.ToUpper(coin.Payments.Scheme)
		if scheme != "" && scheme != "SOLO" {
			return fmt.Errorf("coins[%d] (%s): unsupported payout scheme '%s': only SOLO is currently implemented",
				i, coin.Symbol, coin.Payments.Scheme)
		}
	}

	// Validate coin symbols against SupportedCoins registry
	for i, coin := range c.Coins {
		if !coin.Enabled {
			continue
		}
		coinName := symbolToCoinName(coin.Symbol)
		if _, ok := SupportedCoins[coinName]; !ok {
			return fmt.Errorf("coins[%d]: unknown symbol '%s'. Supported: BTC, BCH, BC2, DGB, DGB-SCRYPT, "+
				"LTC, DOGE, PEP, CAT, NMC, XMY, FBTC, QBX (SYS is merge-mining only via BTC parent)", i, coin.Symbol)
		}
	}

	// Validate DGB and DGB-SCRYPT use the same address (they share the same blockchain)
	// This is a hard error because using different addresses would cause fund loss
	if dgbIndex >= 0 && dgbScryptIndex >= 0 {
		if dgbAddress != dgbScryptAddress {
			return fmt.Errorf("coins[%d] (DGB) and coins[%d] (DGB-SCRYPT) must use the same address "+
				"because they share the same DigiByte blockchain. "+
				"DGB address: %s, DGB-SCRYPT address: %s",
				dgbIndex, dgbScryptIndex, dgbAddress, dgbScryptAddress)
		}
	}

	// SECURITY (Audit #1): Detect and reject placeholder passwords from example configs
	// This prevents operators from accidentally deploying with insecure defaults.
	// Matches the same validation already present in V1 config (config.go).
	placeholderPasswords := []string{
		"your-database-password",
		"your-password",
		"your_password",
		"rpcpassword",
		"changeme",
		"password123",
		"YOUR_PASSWORD_HERE",
		"CHANGE_ME",
		"CHANGE_THIS_TO_A_STRONG_PASSWORD",
	}

	// Check database password
	if c.Database.Password != "" {
		dbPassLower := strings.ToLower(c.Database.Password)
		for _, placeholder := range placeholderPasswords {
			if dbPassLower == strings.ToLower(placeholder) {
				return fmt.Errorf("SECURITY: database.password appears to be a placeholder value. " +
					"Please set a secure password or use SPIRAL_DATABASE_PASSWORD environment variable")
			}
		}
	}

	// Check node passwords for each enabled coin
	for i, coin := range c.Coins {
		if !coin.Enabled {
			continue
		}
		for j, node := range coin.Nodes {
			if node.Password == "" {
				continue
			}
			nodePassLower := strings.ToLower(node.Password)
			for _, placeholder := range placeholderPasswords {
				if nodePassLower == strings.ToLower(placeholder) {
					return fmt.Errorf("SECURITY: coins[%d].nodes[%d].password (%s/%s) appears to be a placeholder value. "+
						"Please set a secure RPC password", i, j, coin.Symbol, node.ID)
				}
			}
		}
	}

	// M3: Validate admin API key minimum length when set
	if c.Global.AdminAPIKey != "" && len(c.Global.AdminAPIKey) < 32 {
		return fmt.Errorf("SECURITY: global.admin_api_key is too short (%d chars). "+
			"Minimum 32 characters required for adequate security. "+
			"Generate one with: openssl rand -hex 32", len(c.Global.AdminAPIKey))
	}

	// M10: Validate webhook URLs use HTTPS (warn on non-HTTPS)
	if c.Global.Sentinel.Enabled {
		for i, wh := range c.Global.Sentinel.Webhooks {
			if wh.URL != "" && !strings.HasPrefix(strings.ToLower(wh.URL), "https://") {
				// Non-HTTPS webhook URLs risk leaking alert payloads (including hostnames,
				// coin addresses, pool IDs) over plaintext to third-party APIs.
				fmt.Printf("⚠ SECURITY WARNING: sentinel.webhooks[%d].url does not use HTTPS. "+
					"Alert payloads may be transmitted in plaintext. "+
					"URL: %s\n", i, wh.URL[:min(len(wh.URL), 40)]+"...")
			}
		}
	}

	// Reject negative numeric values that would bypass SetDefaults' == 0 checks
	if c.Global.MetricsPort < 0 {
		return fmt.Errorf("global.metrics_port cannot be negative")
	}
	if c.Global.APIPort < 0 {
		return fmt.Errorf("global.api_port cannot be negative")
	}
	if c.Database.Port < 0 {
		return fmt.Errorf("database.port cannot be negative")
	}
	if c.Database.MaxConnections < 0 {
		return fmt.Errorf("database.max_connections cannot be negative")
	}
	for i, coin := range c.Coins {
		if coin.Stratum.Difficulty.Initial < 0 {
			return fmt.Errorf("coins[%d] (%s): stratum.difficulty.initial cannot be negative", i, coin.Symbol)
		}
		if coin.Stratum.Difficulty.VarDiff.MinDiff < 0 {
			return fmt.Errorf("coins[%d] (%s): stratum.difficulty.vardiff.min_diff cannot be negative", i, coin.Symbol)
		}
		if coin.Stratum.Difficulty.VarDiff.MaxDiff < 0 {
			return fmt.Errorf("coins[%d] (%s): stratum.difficulty.vardiff.max_diff cannot be negative", i, coin.Symbol)
		}
		if coin.Stratum.Connection.MaxConnections < 0 {
			return fmt.Errorf("coins[%d] (%s): stratum.connection.max_connections cannot be negative", i, coin.Symbol)
		}
		if coin.Stratum.Banning.InvalidSharesThreshold < 0 {
			return fmt.Errorf("coins[%d] (%s): stratum.banning.invalid_shares_threshold cannot be negative", i, coin.Symbol)
		}
	}

	return nil
}

// ResolveWebhookCredentials resolves webhook-related credentials from environment variables.
// M4: Separates sensitive tokens (Telegram bot tokens, Discord webhook URLs) from config files.
// Environment variables:
//   - SPIRAL_DISCORD_WEBHOOK_URL - Overrides Discord webhook URL
//   - SPIRAL_TELEGRAM_BOT_TOKEN - Overrides Telegram bot token
//   - SPIRAL_TELEGRAM_CHAT_ID - Overrides Telegram chat ID
func (c *ConfigV2) ResolveWebhookCredentials() {
	if !c.Global.Sentinel.Enabled {
		return
	}

	discordURL := os.Getenv("SPIRAL_DISCORD_WEBHOOK_URL")
	telegramToken := os.Getenv("SPIRAL_TELEGRAM_BOT_TOKEN")
	telegramChatID := os.Getenv("SPIRAL_TELEGRAM_CHAT_ID")

	for i := range c.Global.Sentinel.Webhooks {
		wh := &c.Global.Sentinel.Webhooks[i]

		// Override Discord webhook URL from env
		if discordURL != "" && isDiscordWebhookURL(wh.URL) {
			wh.URL = discordURL
		}

		// Override Telegram bot token from env
		if telegramToken != "" && isTelegramWebhookURL(wh.URL) {
			wh.Token = telegramToken
			// Reconstruct Telegram URL with new token
			wh.URL = "https://api.telegram.org/bot" + telegramToken + "/sendMessage"
		}

		// Override Telegram chat ID from env
		if telegramChatID != "" && isTelegramWebhookURL(wh.URL) {
			wh.ChatID = telegramChatID
		}
	}
}

// ResolveCredentials resolves database, daemon, and API credentials from environment variables.
// Mirrors V1's ResolveCredentials() for consistency.
func (c *ConfigV2) ResolveCredentials() {
	// Database credentials
	if envUser := os.Getenv("SPIRAL_DATABASE_USER"); envUser != "" {
		c.Database.User = envUser
	}
	if envPass := os.Getenv("SPIRAL_DATABASE_PASSWORD"); envPass != "" {
		c.Database.Password = envPass
	}

	// Admin API key
	if envKey := os.Getenv("SPIRAL_ADMIN_API_KEY"); envKey != "" {
		c.Global.AdminAPIKey = envKey
	}

	// Metrics authentication token
	if envToken := os.Getenv("SPIRAL_METRICS_TOKEN"); envToken != "" {
		c.Global.MetricsAuthToken = envToken
	}

	// Per-coin daemon credentials
	for i := range c.Coins {
		coinUpper := strings.ToUpper(c.Coins[i].Symbol)
		envUser := os.Getenv("SPIRAL_" + coinUpper + "_DAEMON_USER")
		envPass := os.Getenv("SPIRAL_" + coinUpper + "_DAEMON_PASSWORD")
		for j := range c.Coins[i].Nodes {
			if envUser != "" {
				c.Coins[i].Nodes[j].User = envUser
			}
			if envPass != "" {
				c.Coins[i].Nodes[j].Password = envPass
			}
		}
	}
}

// ValidateCredentials checks that all required credentials are non-empty after resolution.
// AUDIT FIX (CB-2): Ported from V1 Config.ValidateCredentials() to prevent the same
// "0 TH/s hashrate" incident where empty env vars silently blank passwords.
func (c *ConfigV2) ValidateCredentials() error {
	var errs []string

	// Check database credentials (required for share persistence)
	if c.Database.Host != "" {
		if c.Database.User == "" {
			errs = append(errs, "database.user is empty - set in config or via SPIRAL_DATABASE_USER env var")
		}
		if c.Database.Password == "" {
			errs = append(errs, "database.password is empty - set in config or via SPIRAL_DATABASE_PASSWORD env var")
		}
	}

	// Check per-coin daemon node credentials (enabled coins only)
	for i, coin := range c.Coins {
		if !coin.Enabled {
			continue
		}
		coinUpper := strings.ToUpper(coin.Symbol)
		for j, node := range coin.Nodes {
			if node.Host != "" {
				if node.User == "" {
					errs = append(errs, fmt.Sprintf("coins[%d].nodes[%d].user is empty for %s - set in config or via SPIRAL_%s_DAEMON_USER env var",
						i, j, coin.Symbol, coinUpper))
				}
				if node.Password == "" {
					errs = append(errs, fmt.Sprintf("coins[%d].nodes[%d].password is empty for %s - set in config or via SPIRAL_%s_DAEMON_PASSWORD env var",
						i, j, coin.Symbol, coinUpper))
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("missing credentials:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// isDiscordWebhookURL checks if a URL is a Discord webhook.
func isDiscordWebhookURL(u string) bool {
	return strings.Contains(u, "discord.com/api/webhooks/") ||
		strings.Contains(u, "discordapp.com/api/webhooks/")
}

// isTelegramWebhookURL checks if a URL is a Telegram Bot API URL.
func isTelegramWebhookURL(u string) bool {
	return strings.Contains(u, "api.telegram.org/bot")
}

// SetDefaults applies default values to V2 configuration.
func (c *ConfigV2) SetDefaults() {
	// Global defaults
	if c.Global.LogLevel == "" {
		c.Global.LogLevel = "info"
	}
	if c.Global.LogFormat == "" {
		c.Global.LogFormat = "json"
	}
	if c.Global.MetricsPort == 0 {
		c.Global.MetricsPort = 9100
	}
	if c.Global.APIPort == 0 {
		c.Global.APIPort = 4000
	}
	if c.Global.APIBindAddress == "" {
		c.Global.APIBindAddress = "0.0.0.0"
	}

	// Sentinel defaults
	if c.Global.Sentinel.Enabled {
		c.Global.Sentinel.SetSentinelDefaults()
	}

	// Celebration defaults
	if c.Global.Celebration.Enabled && c.Global.Celebration.DurationHours == 0 {
		c.Global.Celebration.DurationHours = 2 // Default 2 hours of celebration
	}

	// Database defaults
	if c.Database.Port == 0 {
		c.Database.Port = 5432
	}
	if c.Database.MaxConnections == 0 {
		c.Database.MaxConnections = 200 // Sized for concurrent miner connections + HA + burst share processing
	}
	if c.Database.Batching.Size == 0 {
		c.Database.Batching.Size = 1000
	}
	if c.Database.Batching.Interval == 0 {
		c.Database.Batching.Interval = 5 * time.Second
	}

	// Per-coin defaults
	for i := range c.Coins {
		coin := &c.Coins[i]

		// TLS defaults
		if coin.Stratum.PortTLS > 0 && coin.Stratum.TLS.MinVersion == "" {
			coin.Stratum.TLS.MinVersion = "1.2"
		}

		// Stratum defaults
		if coin.Stratum.Difficulty.Initial == 0 {
			coin.Stratum.Difficulty.Initial = 50000
		}
		if coin.Stratum.Difficulty.VarDiff.MinDiff == 0 {
			coin.Stratum.Difficulty.VarDiff.MinDiff = 0.001
		}
		if coin.Stratum.Difficulty.VarDiff.MaxDiff == 0 {
			// MaxDiff must be high enough for large ASICs on high-difficulty chains
			// S21 (200 TH/s) on BTC needs ~93B diff for 2-second shares
			coin.Stratum.Difficulty.VarDiff.MaxDiff = 1000000000000 // 1 trillion
		}
		if coin.Stratum.Difficulty.VarDiff.TargetTime == 0 {
			// Derive TargetTime from block time: BlockTime/4, capped 2-30s
			// This ensures appropriate share rates for different block times
			coinSymbol := strings.ToLower(coin.Symbol)
			symbolToCoin := map[string]string{
				"dgb":        "digibyte",
				"dgb-scrypt": "digibyte-scrypt",
				"dgb_scrypt": "digibyte-scrypt",
				"btc":        "bitcoin",
				"bch":        "bitcoincash",
				"bc2":        "bitcoinii",
				"ltc":        "litecoin",
				"doge":       "dogecoin",
				"nmc":        "namecoin",
				"pep":        "pepecoin",
				"cat":        "catcoin",
				"sys":        "syscoin",
				"xmy":        "myriadcoin",
				"fbtc":       "fractalbitcoin",
				"qbx":        "qbitx",
			}
			coinName := symbolToCoin[coinSymbol]
			if coinName == "" {
				coinName = coinSymbol
			}
			if coinInfo, ok := SupportedCoins[coinName]; ok && coinInfo.BlockTime > 0 {
				targetTime := float64(coinInfo.BlockTime) / 4.0
				if targetTime < 2 {
					targetTime = 2
				}
				if targetTime > 30 {
					targetTime = 30
				}
				coin.Stratum.Difficulty.VarDiff.TargetTime = targetTime
			} else {
				coin.Stratum.Difficulty.VarDiff.TargetTime = 4 // fallback default
			}
		}
		if coin.Stratum.Difficulty.VarDiff.RetargetTime == 0 {
			coin.Stratum.Difficulty.VarDiff.RetargetTime = 60
		}
		if coin.Stratum.Difficulty.VarDiff.VariancePercent == 0 {
			coin.Stratum.Difficulty.VarDiff.VariancePercent = 30
		}

		// Connection defaults
		if coin.Stratum.Connection.Timeout == 0 {
			coin.Stratum.Connection.Timeout = 600 * time.Second
		}
		if coin.Stratum.Connection.MaxConnections == 0 {
			coin.Stratum.Connection.MaxConnections = 10000
		}
		if coin.Stratum.Connection.KeepaliveInterval == 0 {
			coin.Stratum.Connection.KeepaliveInterval = 60 * time.Second
		}

		// Banning defaults
		if coin.Stratum.Banning.BanDuration == 0 {
			coin.Stratum.Banning.BanDuration = 600 * time.Second
		}
		if coin.Stratum.Banning.InvalidSharesThreshold == 0 {
			coin.Stratum.Banning.InvalidSharesThreshold = 5
		}

		// Version rolling defaults
		if coin.Stratum.VersionRolling.Mask == 0 {
			coin.Stratum.VersionRolling.Mask = 0x1FFFE000
		}

		if coin.Stratum.JobRebroadcast == 0 {
			// Set coin-aware default: 1/3 of block time, minimum 5 seconds
			// For DGB: 15s blocks -> 5s rebroadcast
			// For BTC/BCH: 600s blocks -> 60s rebroadcast (capped)
			coinSymbol := strings.ToLower(coin.Symbol)
			// Map symbol to coin name for lookup in SupportedCoins
			symbolToCoin := map[string]string{
				"dgb":        "digibyte",
				"dgb-scrypt": "digibyte-scrypt",
				"dgb_scrypt": "digibyte-scrypt",
				"btc":        "bitcoin",
				"bch":        "bitcoincash",
				"bc2":        "bitcoinii",
				"ltc":        "litecoin",
				"doge":       "dogecoin",
				"nmc":        "namecoin",
				"pep":        "pepecoin",
				"cat":        "catcoin",
				"sys":        "syscoin",
				"xmy":        "myriadcoin",
				"fbtc":       "fractalbitcoin",
				"qbx":        "qbitx",
			}
			coinName := symbolToCoin[coinSymbol]
			if coinName == "" {
				coinName = coinSymbol
			}
			if coinInfo, ok := SupportedCoins[coinName]; ok && coinInfo.BlockTime > 0 {
				interval := coinInfo.BlockTime / 3
				if interval < 5 {
					interval = 5
				}
				if interval > 60 {
					interval = 60
				}
				coin.Stratum.JobRebroadcast = time.Duration(interval) * time.Second
			} else {
				coin.Stratum.JobRebroadcast = 30 * time.Second
			}
		}

		// Payment defaults
		if coin.Payments.Interval == 0 {
			coin.Payments.Interval = 600 * time.Second
		}
		if coin.Payments.MinimumPayment == 0 {
			coin.Payments.MinimumPayment = 1.0
		}
		if coin.Payments.Scheme == "" {
			coin.Payments.Scheme = "SOLO"
		}
		// Auto-populate BlockTime from SupportedCoins for payment interval scaling
		if coin.Payments.BlockTime == 0 {
			coin.Payments.BlockTime = getBlockTimeForCoin(coin.Symbol)
		}
		// Auto-populate DeepReorgMaxAge based on block time
		if coin.Payments.DeepReorgMaxAge == 0 {
			bt := coin.Payments.BlockTime
			if bt > 0 && bt <= 30 {
				coin.Payments.DeepReorgMaxAge = 5000 // Fast chains: deeper verification
			} else if bt <= 60 {
				coin.Payments.DeepReorgMaxAge = 2000
			} else {
				coin.Payments.DeepReorgMaxAge = 1000 // Standard chains
			}
		}

		// Node defaults
		for j := range coin.Nodes {
			node := &coin.Nodes[j]
			if node.Weight == 0 {
				node.Weight = 1
			}
			// Set default port based on coin
			if node.Port == 0 {
				node.Port = getDefaultPortForCoin(coin.Symbol)
			}

			// ZMQ timing defaults - CRITICAL: must be tuned per-coin based on block time
			// Fast coins (15s blocks) need aggressive timing to avoid missing blocks
			// Slow coins (600s blocks) can use relaxed timing
			if node.ZMQ != nil && node.ZMQ.Enabled {
				blockTime := getBlockTimeForCoin(coin.Symbol)
				setZMQTimingDefaults(node.ZMQ, blockTime)
			}
		}

		// Merge mining defaults
		if coin.MergeMining.Enabled {
			if coin.MergeMining.RefreshInterval == 0 {
				coin.MergeMining.RefreshInterval = 5 * time.Second
			}
			for k := range coin.MergeMining.AuxChains {
				aux := &coin.MergeMining.AuxChains[k]
				if aux.Daemon.Port == 0 {
					for _, info := range SupportedCoins {
						if strings.EqualFold(info.Symbol, aux.Symbol) {
							aux.Daemon.Port = info.DefaultPort
							break
						}
					}
				}
			}
		}

		// ShutdownTimeout default
		if coin.Stratum.Connection.ShutdownTimeout == 0 {
			coin.Stratum.Connection.ShutdownTimeout = 10 * time.Second
		}
	}

	// VIP defaults (only applied if VIP is enabled)
	if c.VIP.Enabled {
		if c.VIP.Netmask == 0 {
			c.VIP.Netmask = 32
		}
		if c.VIP.Priority == 0 {
			c.VIP.Priority = 100
		}
		if c.VIP.DiscoveryPort == 0 {
			c.VIP.DiscoveryPort = 5363
		}
		if c.VIP.StatusPort == 0 {
			c.VIP.StatusPort = 5354
		}
		if c.VIP.HeartbeatInterval == 0 {
			c.VIP.HeartbeatInterval = 30 * time.Second
		}
		if c.VIP.FailoverTimeout == 0 {
			c.VIP.FailoverTimeout = 90 * time.Second
		}
	}

	// HA defaults (only applied if HA is enabled)
	if c.HA.Enabled {
		if c.HA.CheckInterval == 0 {
			c.HA.CheckInterval = 5 * time.Second
		}
		if c.HA.FailoverTimeout == 0 {
			c.HA.FailoverTimeout = 30 * time.Second
		}
	}
}

// getBlockTimeForCoin returns the block time in seconds for a coin.
// Used to calculate appropriate ZMQ timing defaults.
func getBlockTimeForCoin(symbol string) int {
	switch strings.ToUpper(symbol) {
	case "DGB", "DIGIBYTE", "DGB-SCRYPT", "DGB_SCRYPT", "DIGIBYTE-SCRYPT":
		return 15 // 15 second blocks
	case "BTC", "BITCOIN":
		return 600 // 10 minute blocks
	case "BCH", "BITCOINCASH", "BITCOIN-CASH":
		return 600 // 10 minute blocks
	case "BC2", "BCII", "BITCOINII", "BITCOIN-II", "BITCOIN2":
		return 600 // 10 minute blocks
	case "LTC", "LITECOIN":
		return 150 // 2.5 minute blocks
	case "DOGE", "DOGECOIN":
		return 60 // 1 minute blocks
	case "PEP", "PEPECOIN":
		return 60 // 1 minute blocks
	case "CAT", "CATCOIN":
		return 600 // 10 minute blocks (Bitcoin-like)
	case "NMC", "NAMECOIN":
		return 600 // 10 minute blocks
	case "SYS", "SYSCOIN":
		return 150 // 2.5 minute blocks
	case "XMY", "MYRIAD":
		return 60 // 1 minute blocks
	case "FBTC", "FRACTALBTC":
		return 30 // 30 second blocks
	case "QBX", "QBITX":
		return 600 // 10 minute blocks
	default:
		return 600 // Default to Bitcoin-like 10 minute blocks
	}
}

// setZMQTimingDefaults sets ZMQ timing defaults with a hybrid approach.
//
// KEY UNDERSTANDING:
// - ZMQ timing reflects socket/node health, NOT blockchain consensus timing
// - ZMQ tells you: "This node observed an event and emitted it over this socket"
// - Different chains change event volume and load, not ZMQ semantics
//
// HYBRID APPROACH:
// Some parameters are pure SOCKET behavior (same for all coins):
// - ReconnectInitial, ReconnectMax, ReconnectFactor, HealthCheckInterval
//
// Other parameters affect the POLLING/ZMQ TRADEOFF and should scale with block time:
// - FailureThreshold: How long before falling back to polling (don't miss blocks!)
// - StabilityPeriod: How long before trusting ZMQ enough to reduce polling
//
// For fast-block coins (DGB 15s), we need quick failover to polling.
// For slow-block coins (BTC 600s), we can be more patient.
//
// Calculated values:
// | Coin | Block | ReconnInit | ReconnMax | HealthCheck | FailThresh | Stability |
// |------|-------|------------|-----------|-------------|------------|-----------|
// | DGB  | 15s   | 1s         | 30s       | 5s          | 30s        | 1 min     |
// | FBTC | 30s   | 1s         | 30s       | 10s         | 1 min      | 2 min     |
// | DOGE | 60s   | 1s         | 30s       | 10s         | 2 min      | 4 min     |
// | LTC  | 150s  | 1s         | 30s       | 10s         | 2 min (cap)| 5 min (cap)|
// | BTC  | 600s  | 1s         | 30s       | 10s         | 2 min (cap)| 5 min (cap)|
func setZMQTimingDefaults(zmq *NodeZMQConfig, blockTimeSeconds int) {
	blockTime := time.Duration(blockTimeSeconds) * time.Second

	// === SOCKET BEHAVIOR (same for all coins) ===

	// ReconnectInitial: 1 second - fast first retry after disconnect
	if zmq.ReconnectInitial == 0 {
		zmq.ReconnectInitial = 1 * time.Second
	}

	// ReconnectMax: 30 seconds - cap exponential backoff
	if zmq.ReconnectMax == 0 {
		zmq.ReconnectMax = 30 * time.Second
	}

	// ReconnectFactor: 2.0 (standard exponential backoff)
	if zmq.ReconnectFactor == 0 {
		zmq.ReconnectFactor = 2.0
	}

	// === HEALTH MONITORING (scales with block time for fast coins) ===

	// HealthCheckInterval: blockTime / 3, min 5s, max 10s
	// Fast-block coins need faster detection of silent ZMQ failures.
	// - DGB (15s): 5s health checks (detect failure within ~1/3 block)
	// - BTC (600s): 10s health checks (capped, no need for faster)
	if zmq.HealthCheckInterval == 0 {
		healthCheck := blockTime / 3
		if healthCheck < 5*time.Second {
			healthCheck = 5 * time.Second
		}
		if healthCheck > 10*time.Second {
			healthCheck = 10 * time.Second
		}
		zmq.HealthCheckInterval = healthCheck
	}

	// === POLLING/ZMQ TRADEOFF (scales with block time) ===

	// FailureThreshold: blockTime × 2, min 30s, max 2 min
	// How long before falling back to polling. We don't want to miss more than ~2 blocks.
	// - DGB (15s): 30s = 2 blocks max without notification
	// - BTC (600s): 2 min cap (much less than 1 block, but reasonable timeout)
	if zmq.FailureThreshold == 0 {
		threshold := blockTime * 2
		if threshold < 30*time.Second {
			threshold = 30 * time.Second
		}
		if threshold > 2*time.Minute {
			threshold = 2 * time.Minute
		}
		zmq.FailureThreshold = threshold
	}

	// StabilityPeriod: blockTime × 4, min 1 min, max 5 min
	// How long ZMQ must work before we trust it (can reduce polling frequency).
	// We want to see ~4 blocks of proven stability.
	// - DGB (15s): 1 min = 4 blocks of stability
	// - BTC (600s): 5 min cap (reasonable, not 40 minutes!)
	if zmq.StabilityPeriod == 0 {
		stability := blockTime * 4
		if stability < 1*time.Minute {
			stability = 1 * time.Minute
		}
		if stability > 5*time.Minute {
			stability = 5 * time.Minute
		}
		zmq.StabilityPeriod = stability
	}
}

// symbolToCoinName maps a coin symbol to its SupportedCoins map key.
func symbolToCoinName(symbol string) string {
	m := map[string]string{
		"DGB": "digibyte", "DGB-SCRYPT": "digibyte-scrypt", "DGB_SCRYPT": "digibyte-scrypt",
		"BTC": "bitcoin", "BCH": "bitcoincash", "BC2": "bitcoinii",
		"LTC": "litecoin", "DOGE": "dogecoin", "PEP": "pepecoin",
		"CAT": "catcoin", "NMC": "namecoin", "SYS": "syscoin",
		"XMY": "myriadcoin", "FBTC": "fractalbitcoin", "QBX": "qbitx",
	}
	if name, ok := m[strings.ToUpper(symbol)]; ok {
		return name
	}
	return strings.ToLower(symbol)
}

// getDefaultPortForCoin returns the default RPC port for a coin.
// Note: Bitcoin II must be checked before Bitcoin since "BITCOINII" contains "BITCOIN"
func getDefaultPortForCoin(symbol string) int {
	switch strings.ToUpper(symbol) {
	case "DGB", "DIGIBYTE", "DGB-SCRYPT", "DGB_SCRYPT", "DIGIBYTE-SCRYPT":
		return 14022
	case "BC2", "BCII", "BITCOINII", "BITCOIN-II", "BITCOIN2":
		return 8339 // Bitcoin II uses port 8339
	case "BCH", "BITCOINCASH", "BITCOIN-CASH":
		return 8432 // Uses 8432 to avoid conflict with BTC (8332)
	case "BTC", "BITCOIN":
		return 8332
	case "LTC", "LITECOIN":
		return 9332
	case "DOGE", "DOGECOIN":
		return 22555
	case "PEP", "PEPECOIN":
		return 33873
	case "CAT", "CATCOIN":
		return 9932
	case "NMC", "NAMECOIN":
		return 8336 // FIX: Was 8334 (P2P port). RPC port is 8336.
	case "SYS", "SYSCOIN":
		return 8370 // FIX: Was 8369 (P2P port). RPC port is 8370.
	case "XMY", "MYRIAD", "MYRIADCOIN":
		return 10889 // FIX: Was 10888 (P2P port). RPC port is 10889.
	case "FBTC", "FRACTALBTC", "FRACTAL-BTC":
		return 8340 // FIX: Was 8341 (P2P port). RPC port is 8340.
	case "QBX", "QBITX", "Q-BITX":
		return 8344
	default:
		return 8332
	}
}

// GetCoinConfig returns the configuration for a specific coin.
func (c *ConfigV2) GetCoinConfig(symbol string) (*CoinPoolConfig, error) {
	symbol = strings.ToUpper(symbol)
	for i := range c.Coins {
		if strings.ToUpper(c.Coins[i].Symbol) == symbol {
			return &c.Coins[i], nil
		}
	}
	return nil, fmt.Errorf("coin not configured: %s", symbol)
}

// GetEnabledCoins returns all enabled coin configurations.
func (c *ConfigV2) GetEnabledCoins() []CoinPoolConfig {
	enabled := make([]CoinPoolConfig, 0)
	for _, coin := range c.Coins {
		if coin.Enabled {
			enabled = append(enabled, coin)
		}
	}
	return enabled
}

// ToNodeManagerConfigs converts coin node configs to nodemanager format.
func (coin *CoinPoolConfig) ToNodeManagerConfigs() []struct {
	ID       string
	Host     string
	Port     int
	User     string
	Password string
	Priority int
	Weight   int
	ZMQ      *NodeZMQConfig
} {
	result := make([]struct {
		ID       string
		Host     string
		Port     int
		User     string
		Password string
		Priority int
		Weight   int
		ZMQ      *NodeZMQConfig
	}, len(coin.Nodes))

	for i, node := range coin.Nodes {
		result[i].ID = node.ID
		result[i].Host = node.Host
		result[i].Port = node.Port
		result[i].User = node.User
		result[i].Password = node.Password
		result[i].Priority = node.Priority
		result[i].Weight = node.Weight
		result[i].ZMQ = node.ZMQ
	}

	return result
}

// IsMergeMiningEnabled returns true if merge mining (AuxPoW) is enabled
// for this coin pool with at least one active auxiliary chain.
func (c *CoinPoolConfig) IsMergeMiningEnabled() bool {
	if !c.MergeMining.Enabled {
		return false
	}
	for _, aux := range c.MergeMining.AuxChains {
		if aux.Enabled {
			return true
		}
	}
	return false
}

// GetConfigWarnings returns a list of non-fatal configuration warnings for V2 configs.
// These should be logged by the caller but don't prevent startup.
func (c *ConfigV2) GetConfigWarnings() []string {
	var warnings []string

	// Check for privileged ports (< 1024) which require root/admin
	checkPrivilegedPort := func(port int, service string) {
		if port > 0 && port < 1024 {
			warnings = append(warnings,
				fmt.Sprintf("SECURITY: %s uses privileged port %d (requires root/admin privileges)", service, port))
		}
	}

	// Check global ports
	checkPrivilegedPort(c.Global.APIPort, "global.api_port")
	checkPrivilegedPort(c.Global.MetricsPort, "global.metrics_port")

	// Check per-coin stratum ports
	for i, coin := range c.Coins {
		if coin.Enabled {
			checkPrivilegedPort(coin.Stratum.Port, fmt.Sprintf("coins[%d].stratum.port (%s)", i, coin.Symbol))
			checkPrivilegedPort(coin.Stratum.PortV2, fmt.Sprintf("coins[%d].stratum.port_v2 (%s)", i, coin.Symbol))
			checkPrivilegedPort(coin.Stratum.PortTLS, fmt.Sprintf("coins[%d].stratum.port_tls (%s)", i, coin.Symbol))
		}
	}

	return warnings
}

// GetEnabledAuxChains returns all enabled auxiliary chains for merge mining.
func (c *CoinPoolConfig) GetEnabledAuxChains() []AuxChainConfig {
	if !c.MergeMining.Enabled {
		return nil
	}
	enabled := make([]AuxChainConfig, 0, len(c.MergeMining.AuxChains))
	for _, aux := range c.MergeMining.AuxChains {
		if aux.Enabled {
			enabled = append(enabled, aux)
		}
	}
	return enabled
}

// ============================================================================
// V2 HA Helpers
// ============================================================================

// IsHAEnabled returns true if HA database mode is enabled in V2 config.
func (c *ConfigV2) IsHAEnabled() bool {
	return c.Database.HA.Enabled && len(c.Database.HA.Nodes) > 0
}

// IsVIPEnabled returns true if VIP failover is enabled in V2 config.
func (c *ConfigV2) IsVIPEnabled() bool {
	return c.VIP.Enabled && c.VIP.Address != "" && c.VIP.ClusterToken != ""
}

// GetDatabaseNodes returns all database node configs for HA mode in V2.
// The primary database is always included as priority 0.
// Returns nil if HA mode is not enabled.
func (c *ConfigV2) GetDatabaseNodes() []DatabaseNodeConfig {
	if !c.IsHAEnabled() {
		return nil
	}

	// Start with primary as priority 0
	nodes := make([]DatabaseNodeConfig, 0, len(c.Database.HA.Nodes)+1)
	nodes = append(nodes, DatabaseNodeConfig{
		Host:           c.Database.Host,
		Port:           c.Database.Port,
		User:           c.Database.User,
		Password:       c.Database.Password,
		Database:       c.Database.Database,
		MaxConnections: c.Database.MaxConnections,
		Priority:       0,
		ReadOnly:       false,
	})

	// Add HA nodes
	for i, node := range c.Database.HA.Nodes {
		nodeCopy := node
		if nodeCopy.Priority == 0 {
			nodeCopy.Priority = i + 1
		}
		nodes = append(nodes, nodeCopy)
	}

	return nodes
}

// GetFirstStratumPort returns the stratum port of the first enabled coin.
// Used as the primary stratum port for VIP configuration.
func (c *ConfigV2) GetFirstStratumPort() int {
	for _, coin := range c.Coins {
		if coin.Enabled {
			return coin.Stratum.Port
		}
	}
	return 3333 // default
}
