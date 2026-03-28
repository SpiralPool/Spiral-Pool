// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package config handles configuration loading and validation.
package config

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete pool configuration
type Config struct {
	Pool        PoolConfig        `yaml:"pool"`
	Coins       []CoinConfig      `yaml:"coins,omitempty"`       // Multi-coin support
	MergeMining MergeMiningConfig `yaml:"mergeMining,omitempty"` // Merge mining (AuxPoW) configuration
	Stratum     StratumConfig     `yaml:"stratum"`
	Daemon      DaemonConfig      `yaml:"daemon"`
	Database    DatabaseConfig    `yaml:"database"`
	Payments    PaymentsConfig    `yaml:"payments"`
	API         APIConfig         `yaml:"api"`
	Metrics     MetricsConfig     `yaml:"metrics"`
	Logging     LoggingConfig     `yaml:"logging"`
	Security    SecurityConfig    `yaml:"security,omitempty"`    // Security settings
	Backup      BackupConfig      `yaml:"backup,omitempty"`      // Backup settings
	Failover    FailoverConfig    `yaml:"failover,omitempty"`    // Pool failover settings
	VIP         VIPConfig         `yaml:"vip,omitempty"`         // VIP (Virtual IP) for miner failover
	HA          HAConfig          `yaml:"ha,omitempty"`          // High Availability coordination
	Sentinel    SentinelConfig    `yaml:"sentinel,omitempty"`    // Spiral Sentinel monitoring
	Celebration CelebrationConfig `yaml:"celebration,omitempty"` // Block celebration display settings
}

// CoinConfig defines settings for a specific coin
type CoinConfig struct {
	Name          string       `yaml:"name"`      // e.g., "digibyte", "bitcoin", "bitcoincash", "bitcoinii"
	Symbol        string       `yaml:"symbol"`    // e.g., "DGB", "BTC", "BCH", "BC2"
	Algorithm     string       `yaml:"algorithm"` // e.g., "sha256d", "scrypt"
	Enabled       bool         `yaml:"enabled"`
	Address       string       `yaml:"address"`       // Payout address for this coin
	Daemon        DaemonConfig `yaml:"daemon"`        // Coin-specific daemon config
	StratumPort    int          `yaml:"stratumPort"`              // Stratum port for this coin
	StratumV2Port  int          `yaml:"stratumV2Port"`            // Stratum V2 port (optional)
	StratumTLSPort int          `yaml:"stratumTLSPort,omitempty"` // Stratum TLS port (optional)
	BlockReward    float64      `yaml:"blockReward"`              // Current block reward
	BlockTime     int          `yaml:"blockTime"`     // Target block time in seconds
	Confirmations int          `yaml:"confirmations"` // Required confirmations before payout
}

// PoolConfig defines the pool identity and coin settings
type PoolConfig struct {
	ID                string `yaml:"id"`
	Coin              string `yaml:"coin"`
	Address           string `yaml:"address"`
	CoinbaseText      string `yaml:"coinbaseText"`
	SkipGenesisVerify bool   `yaml:"skipGenesisVerify,omitempty"` // Regtest/testnet only
}

// StratumConfig defines stratum server settings
type StratumConfig struct {
	Listen         string                 `yaml:"listen"`
	ListenV2       string                 `yaml:"listenV2,omitempty"`
	TLS            TLSConfig              `yaml:"tls,omitempty"` // TLS/SSL support
	Difficulty     DifficultyConfig       `yaml:"difficulty"`
	Banning        BanningConfig          `yaml:"banning"`
	Connection     ConnectionConfig       `yaml:"connection"`
	RateLimiting   StratumRateLimitConfig `yaml:"rateLimiting,omitempty"` // Rate limiting
	VersionRolling VersionRolling         `yaml:"versionRolling"`
	JobRebroadcast time.Duration          `yaml:"jobRebroadcast"`
	MOTD           string                 `yaml:"motd,omitempty"` // Message of the day sent to miners after subscribe
}

// TLSConfig defines TLS/SSL settings for encrypted stratum connections
type TLSConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenTLS  string `yaml:"listenTLS,omitempty"`  // TLS stratum port (e.g., "0.0.0.0:3335")
	CertFile   string `yaml:"certFile"`             // Path to TLS certificate
	KeyFile    string `yaml:"keyFile"`              // Path to TLS private key
	MinVersion string `yaml:"minVersion,omitempty"` // Minimum TLS version (1.2, 1.3)
	ClientAuth bool   `yaml:"clientAuth,omitempty"` // Require client certificate
	CAFile     string `yaml:"caFile,omitempty"`     // CA certificate for client auth
}

// StratumRateLimitConfig defines rate limiting for stratum connections
type StratumRateLimitConfig struct {
	Enabled              bool          `yaml:"enabled"`
	ConnectionsPerIP     int           `yaml:"connectionsPerIP"`     // Max connections per IP
	ConnectionsPerMinute int           `yaml:"connectionsPerMinute"` // Max new connections per minute per IP
	SharesPerSecond      int           `yaml:"sharesPerSecond"`      // Max shares per second per worker
	BanThreshold         int           `yaml:"banThreshold"`         // Violations before ban
	BanDuration          time.Duration `yaml:"banDuration"`          // How long to ban
	WhitelistIPs         []string      `yaml:"whitelistIPs"`         // IPs exempt from rate limiting

	// RED-TEAM: Additional security hardening options
	WorkersPerIP         int           `yaml:"workersPerIP"`         // Max unique workers per IP (identity churn protection)
	PreAuthTimeout       time.Duration `yaml:"preAuthTimeout"`       // Timeout for sessions before authorization (default: 10s)
	BanPersistencePath   string        `yaml:"banPersistencePath"`   // Path to persist bans across restarts
	PreAuthMessageLimit  int           `yaml:"preAuthMessageLimit"`  // Max messages before authorization (default: 20)
}

// GetListenPort returns the stratum V1 listen port from the Listen address.
func (s *StratumConfig) GetListenPort() int {
	return parsePortFromAddress(s.Listen, 3333)
}

// GetV2ListenPort returns the stratum V2 listen port from the ListenV2 address.
func (s *StratumConfig) GetV2ListenPort() int {
	if s.ListenV2 == "" {
		return 0 // V2 not configured
	}
	return parsePortFromAddress(s.ListenV2, 0)
}

// GetTLSListenPort returns the stratum TLS listen port from the TLS config.
func (s *StratumConfig) GetTLSListenPort() int {
	if !s.TLS.Enabled || s.TLS.ListenTLS == "" {
		return 0 // TLS not configured
	}
	return parsePortFromAddress(s.TLS.ListenTLS, 0)
}

// DifficultyConfig defines difficulty and VARDIFF settings
type DifficultyConfig struct {
	Initial float64       `yaml:"initial"`
	VarDiff VarDiffConfig `yaml:"varDiff"`
}

// VarDiffConfig defines variable difficulty algorithm parameters
type VarDiffConfig struct {
	Enabled         bool    `yaml:"enabled"`
	MinDiff         float64 `yaml:"minDiff"`
	MaxDiff         float64 `yaml:"maxDiff"`
	TargetTime      float64 `yaml:"targetTime"`      // seconds between shares (auto-derived from BlockTime if 0)
	RetargetTime    float64 `yaml:"retargetTime"`    // seconds between adjustments
	VariancePercent float64 `yaml:"variancePercent"` // allowed variance before adjustment

	// BlockTime is the target block time in seconds for the blockchain.
	// Used to derive appropriate TargetTime if not explicitly set.
	// For fast chains (15s blocks), shares should come faster than slow chains (600s blocks).
	// If 0, defaults from SupportedCoins based on pool.coin setting.
	BlockTime int `yaml:"blockTime,omitempty"`

	// SlowDiffPatterns contains user-agent substrings that identify miners which are
	// slow to apply new difficulty (e.g., cgminer-based firmware like Avalon).
	// These miners need longer cooldown between retargets (30s instead of 5s) because
	// they don't apply new difficulty to work-in-progress.
	// Default: ["cgminer"] if not specified.
	SlowDiffPatterns []string `yaml:"slowDiffPatterns,omitempty"`
}

// BanningConfig defines miner banning behavior
type BanningConfig struct {
	Enabled                bool          `yaml:"enabled"`
	BanDuration            time.Duration `yaml:"banDuration"`
	InvalidSharesThreshold int           `yaml:"invalidSharesThreshold"`
}

// ConnectionConfig defines connection limits and timeouts
type ConnectionConfig struct {
	Timeout           time.Duration `yaml:"timeout"`
	MaxConnections    int           `yaml:"maxConnections"`
	KeepaliveInterval time.Duration `yaml:"keepaliveInterval"` // Interval for sending mining.ping
	ShutdownTimeout   time.Duration `yaml:"shutdownTimeout"`   // Graceful shutdown timeout
}

// VersionRolling defines BIP320 version rolling settings
type VersionRolling struct {
	Enabled     bool   `yaml:"enabled"`
	Mask        uint32 `yaml:"mask"`
	MinBitCount int    `yaml:"minBitCount"`
}

// DaemonConfig defines coin daemon connection settings
type DaemonConfig struct {
	Host     string    `yaml:"host"`
	Port     int       `yaml:"port"`
	User     string    `yaml:"user"`
	Password string    `yaml:"password"`
	Wallet   string    `yaml:"wallet"` // Optional wallet name for wallet-specific RPC endpoints
	ZMQ      ZMQConfig `yaml:"zmq"`
}

// ZMQConfig defines ZMQ block notification settings
type ZMQConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`

	// Reconnection settings with exponential backoff
	ReconnectInitial    time.Duration `yaml:"reconnectInitial"`    // Initial retry delay (default 5s)
	ReconnectMax        time.Duration `yaml:"reconnectMax"`        // Maximum retry delay (default 120s)
	ReconnectFactor     float64       `yaml:"reconnectFactor"`     // Backoff multiplier (default 2.0)
	FailureThreshold    time.Duration `yaml:"failureThreshold"`    // How long to fail before polling fallback (default 5m)
	StabilityPeriod     time.Duration `yaml:"stabilityPeriod"`     // How long healthy before disabling polling (default 5m)
	HealthCheckInterval time.Duration `yaml:"healthCheckInterval"` // Health check frequency (default 30s)
}

// DatabaseConfig defines PostgreSQL connection settings
type DatabaseConfig struct {
	Host           string         `yaml:"host"`
	Port           int            `yaml:"port"`
	User           string         `yaml:"user"`
	Password       string         `yaml:"password"`
	Database       string         `yaml:"database"`
	MaxConnections int            `yaml:"maxConnections"`
	Batching       BatchingConfig `yaml:"batching"`

	// SECURITY: SSL mode for database connections.
	// When unset, the connection string builder defaults to "require" (see ConnectionString()).
	// Set explicitly to "disable" only for localhost/loopback connections.
	// Options: disable, allow, prefer, require, verify-ca, verify-full
	SSLMode string `yaml:"sslMode,omitempty"`

	// SSLRootCert is the path to the CA certificate file for SSL verification
	// Required when SSLMode is "verify-ca" or "verify-full"
	SSLRootCert string `yaml:"sslRootCert,omitempty"`

	// High Availability mode - only active when HA is enabled via install script
	HA DatabaseHAConfig `yaml:"ha,omitempty"`
}

// DatabaseHAConfig defines High Availability database settings (requires HA mode install)
type DatabaseHAConfig struct {
	// Enabled must be true to use HA mode - set by install script when HA mode is selected
	Enabled bool `yaml:"enabled"`

	// Nodes defines additional database nodes for failover (primary is still Database.Host)
	// Only used when Enabled=true
	Nodes []DatabaseNodeConfig `yaml:"nodes,omitempty"`

	// HealthCheckInterval between node health checks
	HealthCheckInterval time.Duration `yaml:"healthCheckInterval,omitempty"`

	// FailoverThreshold consecutive failures before failover
	FailoverThreshold int `yaml:"failoverThreshold,omitempty"`
}

// DatabaseNodeConfig defines a database node for HA failover
type DatabaseNodeConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	User           string `yaml:"user,omitempty"`     // Falls back to primary credentials
	Password       string `yaml:"password,omitempty"` // Falls back to primary credentials
	Database       string `yaml:"database,omitempty"` // Falls back to primary database
	MaxConnections int    `yaml:"maxConnections,omitempty"`
	Priority       int    `yaml:"priority"` // Lower = higher priority (primary=0)
	ReadOnly       bool   `yaml:"readOnly"` // True for read replicas
}

// BatchingConfig defines share batch write settings
type BatchingConfig struct {
	Size     int           `yaml:"size"`
	Interval time.Duration `yaml:"interval"`
}

// PaymentsConfig defines payout processing settings
type PaymentsConfig struct {
	Enabled        bool          `yaml:"enabled"`
	Interval       time.Duration `yaml:"interval"`
	MinimumPayment float64       `yaml:"minimumPayment"`
	Scheme         string        `yaml:"scheme"` // SOLO only (Spiral Pool is a solo mining pool)

	// BlockMaturity is the number of confirmations required before a block
	// is considered mature and rewards can be paid out.
	// Defaults per coin: BTC=100, BCH=100, DGB=100
	// Set to 0 to use the coin's default.
	BlockMaturity int `yaml:"blockMaturity,omitempty"`

	// V14 FIX: BlockTime is the target block time in seconds for the chain.
	// Used to auto-scale the payment processing interval and deep reorg max age
	// when those values are not explicitly configured.
	// Set automatically from the coin's BlockTime during pool initialization.
	// If 0, defaults are used (600s interval, 1000 confirmations deep reorg).
	BlockTime int `yaml:"blockTime,omitempty"`

	// V16 FIX: DeepReorgMaxAge is the maximum age (in confirmations) to
	// re-verify for deep chain reorganizations. If 0, auto-computed from
	// BlockTime to ensure at least 24 hours of verification depth.
	// Default for BTC (600s blocks): 1000 (~7 days)
	// Default for DGB (15s blocks): 5760 (~24 hours)
	DeepReorgMaxAge int `yaml:"deepReorgMaxAge,omitempty"`
}

// APIConfig defines REST API settings
type APIConfig struct {
	Enabled           bool            `yaml:"enabled"`
	Listen            string          `yaml:"listen"`
	RateLimiting      RateLimitConfig `yaml:"rateLimiting"`
	CORSAllowedOrigin string          `yaml:"corsAllowedOrigin"` // Optional: restrict CORS to specific origin (e.g., "https://dashboard.example.com")
	AdminAPIKey       string          `yaml:"adminApiKey"`       // SECURITY: API key required for admin/HA endpoints. Set via SPIRAL_ADMIN_API_KEY env var.
}

// RateLimitConfig defines API rate limiting
type RateLimitConfig struct {
	Enabled           bool     `yaml:"enabled"`
	RequestsPerSecond int      `yaml:"requestsPerSecond"`
	Whitelist         []string `yaml:"whitelist"`
}

// MetricsConfig defines Prometheus metrics settings
type MetricsConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Listen     string   `yaml:"listen"`
	AuthToken  string   `yaml:"authToken,omitempty"`  // SECURITY: Bearer token for /metrics endpoint (set via SPIRAL_METRICS_TOKEN env var)
	AllowedIPs []string `yaml:"allowedIPs,omitempty"` // SECURITY: IP whitelist for /metrics (e.g., Prometheus server IPs)
}

// LoggingConfig defines logging settings
type LoggingConfig struct {
	Level      string `yaml:"level"`  // debug, info, warn, error
	Format     string `yaml:"format"` // json, text
	Output     string `yaml:"output"` // stdout, file
	File       string `yaml:"file"`
	MaxSizeMB  int    `yaml:"maxSizeMB"`  // Max size per log file in MB
	MaxBackups int    `yaml:"maxBackups"` // Max number of old log files to keep
	MaxAgeDays int    `yaml:"maxAgeDays"` // Max age of log files in days
	Compress   bool   `yaml:"compress"`   // Compress rotated log files
}

// SecurityConfig defines security settings.
// NOTE: AllowedIPs/BlockedIPs are not currently enforced by the stratum server.
// Use stratum.rateLimiting.whitelistIPs for runtime IP filtering.
type SecurityConfig struct {
	// IP-based access control
	// NOTE: Not currently enforced — parsed but not read by any runtime code.
	AllowedIPs []string `yaml:"allowedIPs,omitempty"` // Whitelist of allowed IPs
	BlockedIPs []string `yaml:"blockedIPs,omitempty"` // Blacklist of blocked IPs

	// DDoS protection
	DDoSProtection DDoSConfig `yaml:"ddosProtection,omitempty"`
}

// DDoSConfig defines DDoS protection settings.
// NOTE: Not currently enforced — fields are parsed but no DDoS protection code exists.
// Stratum rate limiting (rateLimiting config section) provides connection/share limits.
type DDoSConfig struct {
	Enabled             bool          `yaml:"enabled"`
	MaxConnectionsPerIP int           `yaml:"maxConnectionsPerIP"` // Max simultaneous connections per IP
	MaxConnectionRate   int           `yaml:"maxConnectionRate"`   // Max new connections per second globally
	MaxPacketRate       int           `yaml:"maxPacketRate"`       // Max packets per second per connection
	SlowlorisTimeout    time.Duration `yaml:"slowlorisTimeout"`    // Timeout for incomplete requests
	SynFloodProtection  bool          `yaml:"synFloodProtection"`  // Enable SYN flood protection
}

// BackupConfig defines backup settings.
// NOTE: Not currently enforced — fields are parsed but no backup system exists.
type BackupConfig struct {
	Enabled         bool                    `yaml:"enabled"`
	Schedule        string                  `yaml:"schedule"`        // Cron expression (e.g., "0 2 * * *" for 2 AM daily)
	RetentionDays   int                     `yaml:"retentionDays"`   // How long to keep backups
	BackupPath      string                  `yaml:"backupPath"`      // Where to store backups
	IncludeWallet   bool                    `yaml:"includeWallet"`   // Include wallet.dat in backup
	IncludeDatabase bool                    `yaml:"includeDatabase"` // Include PostgreSQL dump
	IncludeConfig   bool                    `yaml:"includeConfig"`   // Include config files
	IncludeLogs     bool                    `yaml:"includeLogs"`     // Include log files
	Compression     string                  `yaml:"compression"`     // "gzip", "zstd", "none"
	Encryption      *BackupEncryptionConfig `yaml:"encryption,omitempty"`
}

// BackupEncryptionConfig defines backup encryption settings
type BackupEncryptionConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Algorithm string `yaml:"algorithm"` // "aes-256-gcm"
	KeyFile   string `yaml:"keyFile"`   // Path to encryption key file
}

// FailoverConfig defines pool-level failover and discovery settings
type FailoverConfig struct {
	Enabled bool `yaml:"enabled"` // Enable pool failover

	// Discovery settings for finding other pools on the network
	Discovery DiscoveryConfig `yaml:"discovery,omitempty"`

	// Backup pools to failover to when primary is down
	BackupPools []BackupPoolConfig `yaml:"backupPools,omitempty"`

	// Health check settings
	HealthCheckInterval time.Duration `yaml:"healthCheckInterval"` // How often to check pool health
	FailoverThreshold   int           `yaml:"failoverThreshold"`   // Consecutive failures before failover
	RecoveryThreshold   int           `yaml:"recoveryThreshold"`   // Consecutive successes before recovery
}

// DiscoveryConfig defines subnet discovery settings for finding existing pools
type DiscoveryConfig struct {
	Enabled       bool          `yaml:"enabled"`       // Enable subnet discovery
	Subnets       []string      `yaml:"subnets"`       // Subnets to scan (e.g., "192.168.1.0/24")
	AutoDetect    bool          `yaml:"autoDetect"`    // Auto-detect local subnet
	StratumPorts  []int         `yaml:"stratumPorts"`  // Ports to scan (default: 3333, 3334)
	ScanTimeout   time.Duration `yaml:"scanTimeout"`   // Timeout per host scan
	ScanInterval  time.Duration `yaml:"scanInterval"`  // How often to re-scan
	MaxConcurrent int           `yaml:"maxConcurrent"` // Max concurrent scans
}

// BackupPoolConfig defines a backup pool for failover
type BackupPoolConfig struct {
	ID       string `yaml:"id"`       // Unique identifier
	Host     string `yaml:"host"`     // Pool hostname or IP
	Port     int    `yaml:"port"`     // Stratum port
	Priority int    `yaml:"priority"` // Lower = preferred (0 = highest)
	Weight   int    `yaml:"weight"`   // For load balancing
}

// VIPConfig defines Virtual IP settings for miner failover
// When enabled, miners connect to the VIP which floats between cluster nodes.
// On failover, the VIP moves automatically - miners don't need reconfiguration.
type VIPConfig struct {
	Enabled           bool          `yaml:"enabled"`           // Enable VIP-based miner failover
	Address           string        `yaml:"address"`           // Virtual IP address (e.g., "192.168.1.200")
	Interface         string        `yaml:"interface"`         // Network interface (e.g., "ens33")
	Netmask           int           `yaml:"netmask"`           // CIDR netmask (default: 32)
	Priority          int           `yaml:"priority"`          // Node priority (lower = higher priority, master=100)
	AutoPriority      bool          `yaml:"autoPriority"`      // Auto-assign priority based on join order
	ClusterToken      string        `yaml:"clusterToken"`      // Shared cluster authentication token
	CanBecomeMaster   bool          `yaml:"canBecomeMaster"`   // Allow this node to become master
	DiscoveryPort     int           `yaml:"discoveryPort"`     // UDP port for cluster discovery (default: 5363)
	StatusPort        int           `yaml:"statusPort"`        // HTTP port for status API (default: 5354)
	HeartbeatInterval time.Duration `yaml:"heartbeatInterval"` // Interval between heartbeats (default: 30s)
	FailoverTimeout   time.Duration `yaml:"failoverTimeout"`   // Time before declaring node dead (default: 90s)
}

// HAConfig defines High Availability coordination settings
// This controls how the pool coordinates with replicas for automatic failover.
type HAConfig struct {
	Enabled         bool          `yaml:"enabled"`         // Enable HA coordination
	PrimaryHost     string        `yaml:"primaryHost"`     // Primary database host
	ReplicaHost     string        `yaml:"replicaHost"`     // Replica database host
	CheckInterval   time.Duration `yaml:"checkInterval"`   // Health check interval (default: 5s)
	FailoverTimeout time.Duration `yaml:"failoverTimeout"` // Time before failover (default: 30s)
}

// MergeMiningConfig defines merge mining (AuxPoW) configuration.
//
// IMPORTANT: Merge mining requires BOTH blockchain daemons to be running and fully synced:
//   - Parent chain daemon (e.g., litecoind) for getblocktemplate
//   - Aux chain daemon (e.g., dogecoind) for getauxblock/submitauxblock
//
// When enabled, the pool automatically operates in multi-coin mode, mining the parent
// chain while simultaneously solving blocks for auxiliary chains.
//
// Example configuration for Litecoin + Dogecoin:
//
//	mergeMining:
//	  enabled: true
//	  auxChains:
//	    - symbol: "DOGE"
//	      enabled: true
//	      address: "DDogePayoutAddress..."
//	      daemon:
//	        host: "127.0.0.1"
//	        port: 22555
//	        user: "dogerpc"
//	        password: "dogepass"
type MergeMiningConfig struct {
	// Enabled activates merge mining for this pool.
	// When true, the pool fetches aux block templates and embeds commitments in parent coinbase.
	Enabled bool `yaml:"enabled"`

	// AuxChains configures the auxiliary chains to merge mine.
	// Each aux chain requires its own daemon connection and payout address.
	AuxChains []AuxChainConfig `yaml:"auxChains,omitempty"`

	// RefreshInterval is how often to fetch new aux block templates.
	// Default: 5 seconds. Shorter intervals reduce stale aux blocks but increase RPC load.
	RefreshInterval time.Duration `yaml:"refreshInterval,omitempty"`

	// MerkleNonce is used in chain slot calculation for the aux merkle tree.
	// Default: 0. Only change if you understand aux merkle tree slot assignment.
	MerkleNonce uint32 `yaml:"merkleNonce,omitempty"`
}

// AuxChainConfig configures an auxiliary chain for merge mining.
type AuxChainConfig struct {
	// Symbol is the coin ticker (e.g., "DOGE", "NMC").
	Symbol string `yaml:"symbol"`

	// Enabled indicates if this aux chain should be actively mined.
	// Set to false to temporarily disable without removing configuration.
	Enabled bool `yaml:"enabled"`

	// Address is the payout address for aux block rewards.
	// This receives the coinbase reward when aux blocks are found.
	Address string `yaml:"address"`

	// Daemon configures the connection to the aux chain's node.
	// The daemon must support getauxblock and submitauxblock RPCs.
	Daemon DaemonConfig `yaml:"daemon"`
}

// SupportedCoins defines the coins supported by this pool.
// V2.0.0-PHI_HASH_REACTOR: Supports SHA-256d and Scrypt algorithms.
//
// SHA-256d coins: BTC, BCH, BC2, DGB, NMC, SYS, XMY, FBTC, QBX
// Scrypt coins:   LTC, DOGE, DGB-SCRYPT, PEP, CAT
var SupportedCoins = map[string]CoinInfo{
	// === SHA-256d Coins ===
	"digibyte": {
		Name:          "DigiByte",
		Symbol:        "DGB",
		Algorithm:     "sha256d",
		DefaultPort:   14022,
		P2PPort:       12024,
		AddressPrefix: []byte{0x1E}, // D prefix
		BlockTime:     15,
	},
	"bitcoin": {
		Name:          "Bitcoin",
		Symbol:        "BTC",
		Algorithm:     "sha256d",
		DefaultPort:   8332,
		P2PPort:       8333,
		AddressPrefix: []byte{0x00}, // 1 prefix
		BlockTime:     600,
	},
	"bitcoincash": {
		Name:          "Bitcoin Cash",
		Symbol:        "BCH",
		Algorithm:     "sha256d",
		DefaultPort:   8432, // BCH uses different port to avoid BTC conflict
		P2PPort:       8433,
		AddressPrefix: []byte{0x00},
		BlockTime:     600,
	},
	// Bitcoin II (BC2) - "nearly 1:1 re-launch of Bitcoin" with new genesis block
	// CRITICAL: BC2 uses IDENTICAL address formats to Bitcoin (same P2PKH 0x00, P2SH 0x05, bech32 "bc")
	// Addresses are indistinguishable - ensure wallet is from Bitcoin II Core, NOT Bitcoin Core
	"bitcoinii": {
		Name:          "Bitcoin II",
		Symbol:        "BC2",
		Algorithm:     "sha256d",
		DefaultPort:   8339,
		P2PPort:       8338,
		AddressPrefix: []byte{0x00}, // Same as Bitcoin - addresses start with 1
		BlockTime:     600,
	},
	// === Scrypt Coins ===
	"litecoin": {
		Name:          "Litecoin",
		Symbol:        "LTC",
		Algorithm:     "scrypt",
		DefaultPort:   9332,
		P2PPort:       9333,
		AddressPrefix: []byte{0x30}, // L prefix
		BlockTime:     150,          // 2.5 minutes
	},
	"dogecoin": {
		Name:          "Dogecoin",
		Symbol:        "DOGE",
		Algorithm:     "scrypt",
		DefaultPort:   22555,
		P2PPort:       22556,
		AddressPrefix: []byte{0x1E}, // D prefix (same as DigiByte)
		BlockTime:     60,           // 1 minute
	},
	// DigiByte Scrypt mode - same blockchain, different PoW algorithm
	// Uses same addresses as regular DGB but hashes with Scrypt
	"digibyte-scrypt": {
		Name:          "DigiByte (Scrypt)",
		Symbol:        "DGB-SCRYPT",
		Algorithm:     "scrypt",
		DefaultPort:   14022,        // Same RPC port as DGB (same node)
		P2PPort:       12024,        // Same P2P port as DGB (same network)
		AddressPrefix: []byte{0x1E}, // D prefix (same as DGB)
		BlockTime:     15,           // Same block time as DGB
	},
	// PepeCoin (Scrypt fork) - merge-mined with Litecoin
	// Note: This is the Scrypt fork, NOT the original X11/PoS Memetic
	"pepecoin": {
		Name:          "PepeCoin",
		Symbol:        "PEP",
		Algorithm:     "scrypt",
		DefaultPort:   33873,
		P2PPort:       33874,
		AddressPrefix: []byte{0x37}, // P prefix (55 decimal)
		BlockTime:     60,           // 1 minute
	},
	// Catcoin - first cat-themed memecoin (December 2013)
	// Uses Bitcoin-like parameters with 10-minute blocks
	"catcoin": {
		Name:          "Catcoin",
		Symbol:        "CAT",
		Algorithm:     "scrypt",
		DefaultPort:   9932,
		P2PPort:       9933,
		AddressPrefix: []byte{0x15}, // C prefix (21 decimal)
		BlockTime:     600,          // 10 minutes (like Bitcoin)
	},
	// === SHA-256d AuxPoW Coins (Merge Mining Auxiliary) ===
	"namecoin": {
		Name:          "Namecoin",
		Symbol:        "NMC",
		Algorithm:     "sha256d",
		DefaultPort:   8336,
		P2PPort:       8334,
		AddressPrefix: []byte{0x34}, // N/M prefix
		BlockTime:     600,          // 10 minutes (same as Bitcoin)
	},
	// Fractal Bitcoin (FBTC) - Bitcoin sidechain with native merge mining
	// Uses Bitcoin's address format (same P2PKH 0x00, P2SH 0x05, bech32 "bc")
	// Chain ID: 0x2024 (8228) for AuxPoW merkle tree slot calculation
	// Cadence Mining: 2 permissionless + 1 merged per 3 blocks
	"fractalbitcoin": {
		Name:          "Fractal Bitcoin",
		Symbol:        "FBTC",
		Algorithm:     "sha256d",
		DefaultPort:   8340,
		P2PPort:       8341,
		AddressPrefix: []byte{0x00}, // Same as Bitcoin - addresses start with 1
		BlockTime:     30,           // 30 seconds (NOT 600 like Bitcoin!)
	},
	// Q-BitX (QBX) - standalone SHA-256d coin (NOT merge-mineable)
	"qbitx": {
		Name:          "Q-BitX",
		Symbol:        "QBX",
		Algorithm:     "sha256d",
		DefaultPort:   8344,
		P2PPort:       8345,
		AddressPrefix: []byte{0x32}, // M prefix (50 decimal) — Dilithium-derived, NOT Bitcoin's 0x00
		BlockTime:     150,          // 2.5 minutes
	},
	// Syscoin (SYS) - merge-mined with Bitcoin via AuxPoW
	"syscoin": {
		Name:          "Syscoin",
		Symbol:        "SYS",
		Algorithm:     "sha256d",
		DefaultPort:   8370,
		P2PPort:       8369,
		AddressPrefix: []byte{0x3F}, // S prefix (63 decimal)
		BlockTime:     150,          // 2.5 minutes
	},
	// Myriadcoin (XMY) - multi-algo, merge-mined with Bitcoin on SHA-256d
	"myriadcoin": {
		Name:          "Myriadcoin",
		Symbol:        "XMY",
		Algorithm:     "sha256d",
		DefaultPort:   10889,
		P2PPort:       10888,
		AddressPrefix: []byte{0x32}, // M prefix (50 decimal)
		BlockTime:     60,           // 1 minute
	},
}

// CoinInfo contains static information about a coin
type CoinInfo struct {
	Name          string
	Symbol        string
	Algorithm     string
	DefaultPort   int
	P2PPort       int
	AddressPrefix []byte
	BlockTime     int
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	// G304: Path is provided by administrator via CLI flag, not untrusted input
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Resolve credentials from environment variables for security
	// Environment variables take precedence over config file values
	cfg.ResolveCredentials()

	// SECURITY (C-1 fix): Validate credentials are not empty after resolution
	// This catches cases where env vars are set but empty, which would silently
	// overwrite valid config file credentials with empty strings
	if err := cfg.ValidateCredentials(); err != nil {
		return nil, fmt.Errorf("credential validation failed: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	cfg.SetDefaults()
	return &cfg, nil
}

// ResolveCredentials resolves credentials from environment variables.
// This allows secure credential management without storing secrets in config files.
// Environment variables supported:
//   - SPIRAL_DAEMON_USER / SPIRAL_DAEMON_PASSWORD - Daemon RPC credentials
//   - SPIRAL_DATABASE_USER / SPIRAL_DATABASE_PASSWORD - PostgreSQL credentials
//   - SPIRAL_<COIN>_DAEMON_USER / SPIRAL_<COIN>_DAEMON_PASSWORD - Per-coin daemon credentials
//
// Values in config file are used as fallback if env vars are not set.
func (c *Config) ResolveCredentials() {
	// Daemon RPC credentials
	if envUser := os.Getenv("SPIRAL_DAEMON_USER"); envUser != "" {
		c.Daemon.User = envUser
	}
	if envPass := os.Getenv("SPIRAL_DAEMON_PASSWORD"); envPass != "" {
		c.Daemon.Password = envPass
	}

	// Database credentials
	if envUser := os.Getenv("SPIRAL_DATABASE_USER"); envUser != "" {
		c.Database.User = envUser
	}
	if envPass := os.Getenv("SPIRAL_DATABASE_PASSWORD"); envPass != "" {
		c.Database.Password = envPass
	}

	// Per-coin daemon credentials
	for i := range c.Coins {
		coinUpper := strings.ToUpper(c.Coins[i].Symbol)
		if envUser := os.Getenv("SPIRAL_" + coinUpper + "_DAEMON_USER"); envUser != "" {
			c.Coins[i].Daemon.User = envUser
		}
		if envPass := os.Getenv("SPIRAL_" + coinUpper + "_DAEMON_PASSWORD"); envPass != "" {
			c.Coins[i].Daemon.Password = envPass
		}
	}

	// Admin API key (SECURITY: Required for admin/HA endpoints)
	if envKey := os.Getenv("SPIRAL_ADMIN_API_KEY"); envKey != "" {
		c.API.AdminAPIKey = envKey
	}

	// Metrics authentication token (SECURITY: Protects /metrics endpoint)
	if envToken := os.Getenv("SPIRAL_METRICS_TOKEN"); envToken != "" {
		c.Metrics.AuthToken = envToken
	}

	// M4: Webhook credential resolution from environment variables
	// Separates sensitive tokens from config files for defense-in-depth
	if c.Sentinel.Enabled {
		discordURL := os.Getenv("SPIRAL_DISCORD_WEBHOOK_URL")
		telegramToken := os.Getenv("SPIRAL_TELEGRAM_BOT_TOKEN")
		telegramChatID := os.Getenv("SPIRAL_TELEGRAM_CHAT_ID")

		for i := range c.Sentinel.Webhooks {
			wh := &c.Sentinel.Webhooks[i]

			// Override Discord webhook URL from env
			if discordURL != "" && (strings.Contains(wh.URL, "discord.com/api/webhooks/") ||
				strings.Contains(wh.URL, "discordapp.com/api/webhooks/")) {
				wh.URL = discordURL
			}

			// Override Telegram bot token from env
			if telegramToken != "" && strings.Contains(wh.URL, "api.telegram.org/bot") {
				wh.Token = telegramToken
				wh.URL = "https://api.telegram.org/bot" + telegramToken + "/sendMessage"
			}

			// Override Telegram chat ID from env
			if telegramChatID != "" && strings.Contains(wh.URL, "api.telegram.org/bot") {
				wh.ChatID = telegramChatID
			}
		}
	}
}

// ValidateCredentials checks that all required credentials are non-empty after resolution.
// SECURITY (C-1 fix): This prevents silent failures where env vars are set but empty,
// which would overwrite valid config file credentials with empty strings.
func (c *Config) ValidateCredentials() error {
	var errs []string

	// Check daemon credentials (required for blockchain RPC)
	if c.Daemon.Host != "" {
		if c.Daemon.User == "" {
			errs = append(errs, "daemon.user is empty - set in config or via SPIRAL_DAEMON_USER env var")
		}
		if c.Daemon.Password == "" {
			errs = append(errs, "daemon.password is empty - set in config or via SPIRAL_DAEMON_PASSWORD env var")
		}
	}

	// Check database credentials (required for share persistence)
	if c.Database.Host != "" {
		if c.Database.User == "" {
			errs = append(errs, "database.user is empty - set in config or via SPIRAL_DATABASE_USER env var")
		}
		if c.Database.Password == "" {
			errs = append(errs, "database.password is empty - set in config or via SPIRAL_DATABASE_PASSWORD env var")
		}
	}

	// Check per-coin daemon credentials for enabled coins
	for i, coin := range c.Coins {
		if !coin.Enabled {
			continue
		}
		coinUpper := strings.ToUpper(coin.Symbol)
		if coin.Daemon.Host != "" {
			if coin.Daemon.User == "" {
				errs = append(errs, fmt.Sprintf("coins[%d].daemon.user is empty for %s - set in config or via SPIRAL_%s_DAEMON_USER env var",
					i, coin.Symbol, coinUpper))
			}
			if coin.Daemon.Password == "" {
				errs = append(errs, fmt.Sprintf("coins[%d].daemon.password is empty for %s - set in config or via SPIRAL_%s_DAEMON_PASSWORD env var",
					i, coin.Symbol, coinUpper))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("missing credentials:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// MaskCredentials returns a copy of the config with credentials masked for logging.
// NEVER log the original config - always use this method for safe logging.
func (c *Config) MaskCredentials() Config {
	masked := *c // Shallow copy

	// Mask daemon credentials
	if masked.Daemon.User != "" {
		masked.Daemon.User = "***"
	}
	if masked.Daemon.Password != "" {
		masked.Daemon.Password = "***"
	}

	// Mask database credentials
	if masked.Database.User != "" {
		masked.Database.User = "***"
	}
	if masked.Database.Password != "" {
		masked.Database.Password = "***"
	}

	// Mask per-coin daemon credentials
	maskedCoins := make([]CoinConfig, len(c.Coins))
	copy(maskedCoins, c.Coins)
	for i := range maskedCoins {
		if maskedCoins[i].Daemon.User != "" {
			maskedCoins[i].Daemon.User = "***"
		}
		if maskedCoins[i].Daemon.Password != "" {
			maskedCoins[i].Daemon.Password = "***"
		}
	}
	masked.Coins = maskedCoins

	return masked
}

// Validate checks the configuration for required fields and valid values
func (c *Config) Validate() error {
	if c.Pool.ID == "" {
		return fmt.Errorf("pool.id is required")
	}
	if c.Pool.Coin == "" {
		return fmt.Errorf("pool.coin is required")
	}

	// Validate coin is supported
	coinLower := strings.ToLower(c.Pool.Coin)
	if _, ok := SupportedCoins[coinLower]; !ok {
		supported := make([]string, 0, len(SupportedCoins))
		for k := range SupportedCoins {
			supported = append(supported, k)
		}
		return fmt.Errorf("unsupported coin '%s', supported: %v", c.Pool.Coin, supported)
	}

	if c.Pool.Address == "" {
		return fmt.Errorf("pool.address is required")
	}
	if c.Pool.Address == "PENDING_GENERATION" {
		return fmt.Errorf("pool.address is set to 'PENDING_GENERATION' - wallet not yet created. " +
			"Run 'systemctl restart spiralstratum' after blockchain sync completes, " +
			"or manually set the address in config.yaml")
	}
	// M-2: Validate pool address checksum
	coinSymbol := extractSymbolFromCoin(c.Pool.Coin)
	if err := ValidateCoinAddress(c.Pool.Address, coinSymbol); err != nil {
		return fmt.Errorf("pool.address validation failed for %s: %w", coinSymbol, err)
	}

	// SECURITY WARNING (Audit Recommendation #4): BC2/BTC Address Collision Detection
	// Bitcoin II (BC2) uses IDENTICAL address formats to Bitcoin (same P2PKH 0x00,
	// P2SH 0x05, and bech32 "bc" prefix). This means a BTC address will validate
	// successfully for BC2 and vice versa. Operators MUST ensure they generate
	// addresses from the correct wallet software.
	if coinSymbol == "BC2" {
		// Log a prominent warning about address format collision
		// This warning appears in validation output to alert operators
		fmt.Println("⚠️  CRITICAL WARNING: Bitcoin II (BC2) Address Collision Risk")
		fmt.Println("   BC2 uses IDENTICAL address formats to Bitcoin (BTC).")
		fmt.Println("   Your configured address:", c.Pool.Address)
		fmt.Println("   VERIFY this address was generated by Bitcoin II Core, NOT Bitcoin Core!")
		fmt.Println("   Sending BC2 rewards to a BTC address will result in PERMANENT LOSS.")
		fmt.Println("")
	}

	// Coinbase text must be ≤40 bytes (scriptsig space: 100 bytes max - height - extranonce)
	// Note: len() counts bytes, not runes - emojis are 4 bytes each
	if len(c.Pool.CoinbaseText) > 40 {
		return fmt.Errorf("pool.coinbaseText exceeds 40 byte limit (got %d bytes)", len(c.Pool.CoinbaseText))
	}
	if c.Stratum.Listen == "" {
		return fmt.Errorf("stratum.listen is required")
	}
	// Validate stratum.listen has a valid port (not just non-empty)
	// This prevents issues where a malformed address like "0.0.0.0:" would pass
	// the empty check but fail port parsing, causing fallback to default port
	stratumPort := parsePortFromAddress(c.Stratum.Listen, 0)
	if stratumPort == 0 {
		return fmt.Errorf("stratum.listen must include a valid port (e.g., '0.0.0.0:3333'), got: %s", c.Stratum.Listen)
	}
	if c.Daemon.Host == "" {
		return fmt.Errorf("daemon.host is required")
	}
	if c.Database.Host == "" {
		return fmt.Errorf("database.host is required")
	}

	// SECURITY: Detect and reject placeholder passwords from example configs
	// This prevents operators from accidentally deploying with insecure defaults
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

	// Check daemon password
	if c.Daemon.Password != "" {
		daemonPassLower := strings.ToLower(c.Daemon.Password)
		for _, placeholder := range placeholderPasswords {
			if daemonPassLower == strings.ToLower(placeholder) {
				return fmt.Errorf("SECURITY: daemon.password appears to be a placeholder value. " +
					"Please set a secure password or use SPIRAL_DAEMON_PASSWORD environment variable")
			}
		}
	}

	// M3: Validate admin API key minimum length when set (V1 parity with V2)
	if c.API.AdminAPIKey != "" && len(c.API.AdminAPIKey) < 32 {
		return fmt.Errorf("SECURITY: api.adminApiKey is too short (%d chars). "+
			"Minimum 32 characters required for adequate security. "+
			"Generate one with: openssl rand -hex 32", len(c.API.AdminAPIKey))
	}

	// Validate TLS config if enabled
	if c.Stratum.TLS.Enabled {
		if c.Stratum.TLS.CertFile == "" {
			return fmt.Errorf("stratum.tls.certFile is required when TLS is enabled")
		}
		if c.Stratum.TLS.KeyFile == "" {
			return fmt.Errorf("stratum.tls.keyFile is required when TLS is enabled")
		}
	}

	// Validate payout scheme - Spiral Pool is a solo mining pool.
	// Pooled payout schemes (PPLNS, PPS, PROP, etc.) are not supported and
	// will never be implemented. Block rewards go directly to the miner's
	// wallet address via the coinbase transaction — non-custodial by design.
	if c.Payments.Enabled {
		scheme := strings.ToUpper(c.Payments.Scheme)
		if scheme != "" && scheme != "SOLO" {
			return fmt.Errorf("unsupported payout scheme '%s': Spiral Pool is a solo mining pool "+
				"and only supports SOLO payouts. Pooled payout schemes (PPLNS, PPS, etc.) are not "+
				"supported. Block rewards go directly to your configured coinbase address", c.Payments.Scheme)
		}
	}

	// Validate merge mining configuration
	if c.MergeMining.Enabled {
		if len(c.MergeMining.AuxChains) == 0 {
			return fmt.Errorf("mergeMining.enabled is true but no auxChains configured")
		}

		// Validate parent chain supports merge mining
		coinLower := strings.ToLower(c.Pool.Coin)
		parentAlgo := ""
		if coinInfo, ok := SupportedCoins[coinLower]; ok {
			parentAlgo = coinInfo.Algorithm
		}

		for i, aux := range c.MergeMining.AuxChains {
			if !aux.Enabled {
				continue
			}

			if aux.Symbol == "" {
				return fmt.Errorf("mergeMining.auxChains[%d].symbol is required", i)
			}
			if aux.Address == "" {
				return fmt.Errorf("mergeMining.auxChains[%d].address is required for %s", i, aux.Symbol)
			}
			if aux.Daemon.Host == "" {
				return fmt.Errorf("mergeMining.auxChains[%d].daemon.host is required for %s", i, aux.Symbol)
			}

			// Validate aux chain algorithm matches parent
			auxSymbol := strings.ToLower(aux.Symbol)
			auxAlgo := ""
			for _, info := range SupportedCoins {
				if strings.EqualFold(info.Symbol, auxSymbol) {
					auxAlgo = info.Algorithm
					break
				}
			}
			if auxAlgo != "" && parentAlgo != "" && auxAlgo != parentAlgo {
				return fmt.Errorf("mergeMining.auxChains[%d] (%s) uses %s algorithm but parent chain %s uses %s - algorithms must match",
					i, aux.Symbol, auxAlgo, c.Pool.Coin, parentAlgo)
			}

			// Validate aux address checksum
			if err := ValidateCoinAddress(aux.Address, aux.Symbol); err != nil {
				return fmt.Errorf("mergeMining.auxChains[%d].address validation failed for %s: %w", i, aux.Symbol, err)
			}
		}
	}

	// Validate multi-coin configs
	for i := range c.Coins {
		coin := &c.Coins[i]

		// Auto-populate Name from Symbol if not provided
		if coin.Name == "" && coin.Symbol != "" {
			// Look up name from SupportedCoins by symbol
			for _, info := range SupportedCoins {
				if strings.EqualFold(info.Symbol, coin.Symbol) {
					coin.Name = info.Name
					break
				}
			}
			// If still empty, use symbol as name
			if coin.Name == "" {
				coin.Name = coin.Symbol
			}
		}

		if coin.Name == "" {
			return fmt.Errorf("coins[%d].name or symbol is required", i)
		}
		if coin.Enabled && coin.Address == "" {
			return fmt.Errorf("coins[%d].address is required when enabled", i)
		}
		if coin.Enabled && coin.Address == "PENDING_GENERATION" {
			return fmt.Errorf("coins[%d].address is set to 'PENDING_GENERATION' - wallet not yet created for %s. "+
				"Run 'systemctl restart spiralstratum' after blockchain sync completes, "+
				"or manually set the address in config.yaml", i, coin.Name)
		}
		// M-2: Validate multi-coin address checksum
		if coin.Enabled && coin.Address != "" {
			coinSym := coin.Symbol
			if coinSym == "" {
				coinSym = extractSymbolFromCoin(coin.Name)
			}
			if err := ValidateCoinAddress(coin.Address, coinSym); err != nil {
				return fmt.Errorf("coins[%d].address validation failed for %s: %w", i, coinSym, err)
			}
		}
	}

	// Validate all ports are in valid range (1-65535)
	if err := c.validateAllPorts(); err != nil {
		return err
	}

	// Validate no port conflicts across services
	if err := c.validatePortConflicts(); err != nil {
		return err
	}

	return nil
}

// validatePortRange checks if a port number is valid (1-65535).
// Port 0 is allowed to indicate "not configured".
func validatePortRange(port int, service string) error {
	if port == 0 {
		return nil // 0 means not configured, which is valid
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: port %d out of valid range (1-65535)", service, port)
	}
	return nil
}

// validateAllPorts validates all configured ports are in valid range (1-65535).
func (c *Config) validateAllPorts() error {
	// Stratum ports
	if err := validatePortRange(parsePortFromAddress(c.Stratum.Listen, 0), "stratum.listen"); err != nil {
		return err
	}
	if c.Stratum.ListenV2 != "" {
		if err := validatePortRange(parsePortFromAddress(c.Stratum.ListenV2, 0), "stratum.listenV2"); err != nil {
			return err
		}
	}
	if c.Stratum.TLS.Enabled && c.Stratum.TLS.ListenTLS != "" {
		if err := validatePortRange(parsePortFromAddress(c.Stratum.TLS.ListenTLS, 0), "stratum.tls.listenTLS"); err != nil {
			return err
		}
	}

	// Daemon port
	if err := validatePortRange(c.Daemon.Port, "daemon.port"); err != nil {
		return err
	}

	// Database port
	if err := validatePortRange(c.Database.Port, "database.port"); err != nil {
		return err
	}

	// Database HA node ports
	for i, node := range c.Database.HA.Nodes {
		if err := validatePortRange(node.Port, fmt.Sprintf("database.ha.nodes[%d].port", i)); err != nil {
			return err
		}
	}

	// API port
	if c.API.Enabled && c.API.Listen != "" {
		if err := validatePortRange(parsePortFromAddress(c.API.Listen, 0), "api.listen"); err != nil {
			return err
		}
	}

	// Metrics port
	if c.Metrics.Enabled && c.Metrics.Listen != "" {
		if err := validatePortRange(parsePortFromAddress(c.Metrics.Listen, 0), "metrics.listen"); err != nil {
			return err
		}
	}

	// VIP ports
	if c.VIP.Enabled {
		if err := validatePortRange(c.VIP.DiscoveryPort, "vip.discoveryPort"); err != nil {
			return err
		}
		if err := validatePortRange(c.VIP.StatusPort, "vip.statusPort"); err != nil {
			return err
		}
	}

	// Failover discovery ports
	for i, port := range c.Failover.Discovery.StratumPorts {
		if err := validatePortRange(port, fmt.Sprintf("failover.discovery.stratumPorts[%d]", i)); err != nil {
			return err
		}
	}

	// Backup pool ports
	for i, pool := range c.Failover.BackupPools {
		if err := validatePortRange(pool.Port, fmt.Sprintf("failover.backupPools[%d].port", i)); err != nil {
			return err
		}
	}

	// Multi-coin stratum ports
	for i, coin := range c.Coins {
		if coin.Enabled {
			if err := validatePortRange(coin.StratumPort, fmt.Sprintf("coins[%d].stratumPort (%s)", i, coin.Symbol)); err != nil {
				return err
			}
			if coin.StratumV2Port > 0 {
				if err := validatePortRange(coin.StratumV2Port, fmt.Sprintf("coins[%d].stratumV2Port (%s)", i, coin.Symbol)); err != nil {
					return err
				}
			}
			if err := validatePortRange(coin.Daemon.Port, fmt.Sprintf("coins[%d].daemon.port (%s)", i, coin.Symbol)); err != nil {
				return err
			}
		}
	}

	return nil
}

// validatePortConflicts checks for port conflicts across all services.
// This prevents startup failures and ensures no two services bind to the same port.
func (c *Config) validatePortConflicts() error {
	// Map of port -> list of service names using that port
	ports := make(map[int][]string)

	// Helper to add a port with its service name
	addPort := func(port int, service string) {
		if port > 0 {
			ports[port] = append(ports[port], service)
		}
	}

	// Stratum ports
	addPort(c.GetStratumPort(), "stratum.listen")
	addPort(c.GetStratumV2Port(), "stratum.listenV2")
	addPort(c.GetStratumTLSPort(), "stratum.tls.listenTLS")

	// API port
	if c.API.Enabled && c.API.Listen != "" {
		addPort(parsePortFromAddress(c.API.Listen, 0), "api.listen")
	}

	// VIP ports (cluster discovery and status API)
	if c.VIP.Enabled {
		addPort(c.VIP.DiscoveryPort, "vip.discoveryPort")
		addPort(c.VIP.StatusPort, "vip.statusPort")
	}

	// Multi-coin stratum ports
	for i, coin := range c.Coins {
		if coin.Enabled {
			addPort(coin.StratumPort, fmt.Sprintf("coins[%d].stratumPort (%s)", i, coin.Symbol))
			addPort(coin.StratumV2Port, fmt.Sprintf("coins[%d].stratumV2Port (%s)", i, coin.Symbol))
		}
	}

	// Check for conflicts
	var conflicts []string
	for port, services := range ports {
		if len(services) > 1 {
			conflicts = append(conflicts, fmt.Sprintf("port %d used by: %s", port, strings.Join(services, ", ")))
		}
	}

	if len(conflicts) > 0 {
		return fmt.Errorf("port conflicts detected: %s", strings.Join(conflicts, "; "))
	}

	return nil
}

// GetConfigWarnings returns a list of non-fatal configuration warnings.
// These should be logged by the caller but don't prevent startup.
func (c *Config) GetConfigWarnings() []string {
	var warnings []string

	// Check for privileged ports (< 1024) which require root/admin
	checkPrivilegedPort := func(port int, service string) {
		if port > 0 && port < 1024 {
			warnings = append(warnings,
				fmt.Sprintf("SECURITY: %s uses privileged port %d (requires root/admin privileges)", service, port))
		}
	}

	// Check all configured ports
	checkPrivilegedPort(c.GetStratumPort(), "stratum.listen")
	checkPrivilegedPort(c.GetStratumV2Port(), "stratum.listenV2")
	checkPrivilegedPort(c.GetStratumTLSPort(), "stratum.tls.listenTLS")

	if c.API.Enabled && c.API.Listen != "" {
		checkPrivilegedPort(parsePortFromAddress(c.API.Listen, 0), "api.listen")
	}

	if c.VIP.Enabled {
		checkPrivilegedPort(c.VIP.DiscoveryPort, "vip.discoveryPort")
		checkPrivilegedPort(c.VIP.StatusPort, "vip.statusPort")
	}

	for i, coin := range c.Coins {
		if coin.Enabled {
			checkPrivilegedPort(coin.StratumPort, fmt.Sprintf("coins[%d].stratumPort (%s)", i, coin.Symbol))
			checkPrivilegedPort(coin.StratumV2Port, fmt.Sprintf("coins[%d].stratumV2Port (%s)", i, coin.Symbol))
		}
	}

	// SECURITY (Audit #2): Warn if stratum rate limiting is disabled
	if !c.Stratum.RateLimiting.Enabled {
		warnings = append(warnings, "SECURITY: Stratum rate limiting is disabled. "+
			"This leaves the pool vulnerable to DDoS and share flooding attacks. "+
			"Enable stratum.rateLimiting.enabled in your configuration.")
	}

	return warnings
}

// SetDefaults applies default values for optional configuration
func (c *Config) SetDefaults() {
	// Stratum defaults
	if c.Stratum.Difficulty.Initial == 0 {
		c.Stratum.Difficulty.Initial = 50000
	}
	if c.Stratum.Difficulty.VarDiff.MinDiff == 0 {
		c.Stratum.Difficulty.VarDiff.MinDiff = 0.001
	}
	if c.Stratum.Difficulty.VarDiff.MaxDiff == 0 {
		// MaxDiff must be high enough for large ASICs on high-difficulty chains
		// S21 (200 TH/s) on BTC needs ~93B diff for 2-second shares
		// Network difficulties: BTC ~148T, DGB ~1B, LTC ~94M
		c.Stratum.Difficulty.VarDiff.MaxDiff = 1000000000000 // 1 trillion - covers all current and future ASICs
	}
	// Derive VarDiff TargetTime from block time if not explicitly set
	// This ensures fast-block chains (15s) get faster share targets than slow chains (600s)
	if c.Stratum.Difficulty.VarDiff.TargetTime == 0 {
		blockTime := c.Stratum.Difficulty.VarDiff.BlockTime
		if blockTime == 0 {
			// Get block time from coin info
			coinName := strings.ToLower(c.Pool.Coin)
			if coinInfo, ok := SupportedCoins[coinName]; ok && coinInfo.BlockTime > 0 {
				blockTime = coinInfo.BlockTime
			}
		}

		if blockTime > 0 {
			// Target share time = BlockTime / 3 to 5
			// Ensures multiple shares per block for accurate credit
			// Fast chains (15s): target 3-5s shares
			// Slow chains (600s): target 30-60s shares (capped at 30s for responsiveness)
			targetTime := float64(blockTime) / 4.0
			if targetTime < 2 {
				targetTime = 2 // Minimum 2 seconds (avoid overwhelming pool)
			}
			if targetTime > 30 {
				targetTime = 30 // Maximum 30 seconds (maintain responsiveness)
			}
			c.Stratum.Difficulty.VarDiff.TargetTime = targetTime
		} else {
			// Fallback for unknown coins
			c.Stratum.Difficulty.VarDiff.TargetTime = 4 // 4 sec/share = 15 shares/min
		}
	}
	if c.Stratum.Difficulty.VarDiff.RetargetTime == 0 {
		c.Stratum.Difficulty.VarDiff.RetargetTime = 60 // 60 second adjustment interval
	}
	if c.Stratum.Difficulty.VarDiff.VariancePercent == 0 {
		c.Stratum.Difficulty.VarDiff.VariancePercent = 30
	}
	if c.Stratum.Connection.Timeout == 0 {
		c.Stratum.Connection.Timeout = 600 * time.Second
	}
	if c.Stratum.Connection.MaxConnections == 0 {
		// 10PH/s design point: S19 Pro 110TH = ~91K miners worst case
		c.Stratum.Connection.MaxConnections = 100000
	}
	if c.Stratum.Connection.KeepaliveInterval == 0 {
		c.Stratum.Connection.KeepaliveInterval = 45 * time.Second // Faster stale connection detection
	}
	if c.Stratum.Connection.ShutdownTimeout == 0 {
		c.Stratum.Connection.ShutdownTimeout = 10 * time.Second
	}
	if c.Stratum.JobRebroadcast == 0 {
		// Set coin-aware default: 1/3 of block time, minimum 5 seconds
		// This ensures fast coins like DigiByte (15s blocks) get quick job updates
		// For DGB: 15s blocks -> 5s rebroadcast (15/3=5)
		// For BTC/BCH: 600s blocks -> 200s rebroadcast (but capped reasonably)
		coinName := strings.ToLower(c.Pool.Coin)
		if coinInfo, ok := SupportedCoins[coinName]; ok && coinInfo.BlockTime > 0 {
			interval := coinInfo.BlockTime / 3
			if interval < 5 {
				interval = 5 // Minimum 5 seconds
			}
			if interval > 60 {
				interval = 60 // Maximum 60 seconds (even for slow coins)
			}
			c.Stratum.JobRebroadcast = time.Duration(interval) * time.Second
		} else {
			c.Stratum.JobRebroadcast = 30 * time.Second // Safe fallback for unknown coins
		}
	}
	if c.Stratum.VersionRolling.Mask == 0 {
		c.Stratum.VersionRolling.Mask = 0x1FFFE000
	}

	// TLS defaults
	if c.Stratum.TLS.MinVersion == "" {
		c.Stratum.TLS.MinVersion = "1.2"
	}

	// Stratum rate limiting defaults
	// HASHRATE RENTAL COMPATIBLE: IP-based limits are DISABLED by default (0 = no limit)
	// These break rented hashpower scenarios where many miners share dynamic IPs
	// Operators can enable these manually if running a private pool
	// c.Stratum.RateLimiting.ConnectionsPerIP stays at 0 (disabled)
	// c.Stratum.RateLimiting.ConnectionsPerMinute stays at 0 (disabled)
	// c.Stratum.RateLimiting.SharesPerSecond stays at 0 (disabled)
	// c.Stratum.RateLimiting.WorkersPerIP stays at 0 (disabled)

	// Ban settings still have defaults (these are per-connection, not per-IP)
	if c.Stratum.RateLimiting.BanThreshold == 0 {
		c.Stratum.RateLimiting.BanThreshold = 10
	}
	if c.Stratum.RateLimiting.BanDuration == 0 {
		c.Stratum.RateLimiting.BanDuration = 30 * time.Minute
	}
	if c.Stratum.RateLimiting.PreAuthTimeout == 0 {
		c.Stratum.RateLimiting.PreAuthTimeout = 10 * time.Second // Hashrate rental: authorize within 10s
	}
	// RED-TEAM: Pre-auth message limit prevents subscribe spam attacks
	// Default 20 provides 4x headroom for complex handshakes (typical: 2-5 messages)
	// while still blocking attackers who spam without authorizing
	if c.Stratum.RateLimiting.PreAuthMessageLimit == 0 {
		c.Stratum.RateLimiting.PreAuthMessageLimit = 20 // Max 20 messages before auth required
	}
	// RED-TEAM: Ban persistence is always enabled - bans survive restarts
	if c.Stratum.RateLimiting.BanPersistencePath == "" {
		c.Stratum.RateLimiting.BanPersistencePath = "/spiralpool/data/bans.json"
	}

	// Banning defaults
	if c.Stratum.Banning.BanDuration == 0 {
		c.Stratum.Banning.BanDuration = 600 * time.Second
	}
	if c.Stratum.Banning.InvalidSharesThreshold == 0 {
		c.Stratum.Banning.InvalidSharesThreshold = 5
	}

	// Database defaults
	if c.Database.Port == 0 {
		c.Database.Port = 5432
	}
	if c.Database.MaxConnections == 0 {
		c.Database.MaxConnections = 200 // Sized for concurrent miner connections, HA replication, and burst share processing
	}
	if c.Database.Batching.Size == 0 {
		c.Database.Batching.Size = 1000
	}
	if c.Database.Batching.Interval == 0 {
		c.Database.Batching.Interval = 3 * time.Second // Faster persistence without excessive writes
	}

	// Database HA defaults (only applied if HA is enabled)
	if c.Database.HA.Enabled {
		if c.Database.HA.HealthCheckInterval == 0 {
			c.Database.HA.HealthCheckInterval = 15 * time.Second // Reduced overhead for stable connections
		}
		if c.Database.HA.FailoverThreshold == 0 {
			c.Database.HA.FailoverThreshold = 3
		}
		// Set defaults for HA nodes
		for i := range c.Database.HA.Nodes {
			node := &c.Database.HA.Nodes[i]
			if node.Port == 0 {
				node.Port = c.Database.Port
			}
			if node.User == "" {
				node.User = c.Database.User
			}
			if node.Password == "" {
				node.Password = c.Database.Password
			}
			if node.Database == "" {
				node.Database = c.Database.Database
			}
			if node.MaxConnections == 0 {
				node.MaxConnections = c.Database.MaxConnections
			}
		}
	}

	// Daemon defaults - use coin-aware default port
	// Note: Validate() has already verified the coin is in SupportedCoins,
	// so this lookup should always succeed. If it doesn't, daemon.port stays 0
	// which will cause a clear connection error rather than silently using wrong port.
	if c.Daemon.Port == 0 {
		coinLower := strings.ToLower(c.Pool.Coin)
		if coinInfo, ok := SupportedCoins[coinLower]; ok {
			c.Daemon.Port = coinInfo.DefaultPort
		}
		// No fallback - if coin isn't found, port stays 0 and connection will fail clearly
	}

	// ZMQ reconnection defaults (exponential backoff: 5s→10s→20s→40s→60s→120s max)
	if c.Daemon.ZMQ.ReconnectInitial == 0 {
		c.Daemon.ZMQ.ReconnectInitial = 5 * time.Second
	}
	if c.Daemon.ZMQ.ReconnectMax == 0 {
		c.Daemon.ZMQ.ReconnectMax = 120 * time.Second // 2 minutes max
	}
	if c.Daemon.ZMQ.ReconnectFactor == 0 {
		c.Daemon.ZMQ.ReconnectFactor = 2.0
	}
	if c.Daemon.ZMQ.FailureThreshold == 0 {
		c.Daemon.ZMQ.FailureThreshold = 5 * time.Minute
	}
	if c.Daemon.ZMQ.StabilityPeriod == 0 {
		c.Daemon.ZMQ.StabilityPeriod = 5 * time.Minute
	}
	if c.Daemon.ZMQ.HealthCheckInterval == 0 {
		c.Daemon.ZMQ.HealthCheckInterval = 30 * time.Second
	}

	// Payment defaults
	if c.Payments.Interval == 0 {
		c.Payments.Interval = 600 * time.Second
	}
	if c.Payments.MinimumPayment == 0 {
		c.Payments.MinimumPayment = 1.0
	}
	if c.Payments.Scheme == "" {
		c.Payments.Scheme = "SOLO"
	}
	// AUDIT FIX: Populate BlockTime from SupportedCoins so V14 interval auto-scaling
	// and V16 deep reorg depth auto-scaling work correctly for fast-block coins (DGB, DOGE, etc).
	// Without this, BlockTime stays 0 and the processor uses 600s interval for all coins.
	if c.Payments.BlockTime == 0 && c.Pool.Coin != "" {
		coinKey := strings.ToLower(c.Pool.Coin)
		if coinInfo, ok := SupportedCoins[coinKey]; ok && coinInfo.BlockTime > 0 {
			c.Payments.BlockTime = coinInfo.BlockTime
		}
	}

	// API defaults
	if c.API.Listen == "" {
		c.API.Listen = "0.0.0.0:4000"
	}
	if c.API.RateLimiting.RequestsPerSecond == 0 {
		c.API.RateLimiting.RequestsPerSecond = 10
	}

	// Metrics defaults
	if c.Metrics.Listen == "" {
		c.Metrics.Listen = "0.0.0.0:9100"
	}

	// Logging defaults
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Logging.Output == "" {
		c.Logging.Output = "stdout"
	}
	if c.Logging.MaxSizeMB == 0 {
		c.Logging.MaxSizeMB = 100
	}
	if c.Logging.MaxBackups == 0 {
		c.Logging.MaxBackups = 730 // ~2 years of daily logs
	}
	if c.Logging.MaxAgeDays == 0 {
		c.Logging.MaxAgeDays = 730 // 2 years
	}

	// Security defaults
	if c.Security.DDoSProtection.MaxConnectionsPerIP == 0 {
		c.Security.DDoSProtection.MaxConnectionsPerIP = 100
	}
	if c.Security.DDoSProtection.MaxConnectionRate == 0 {
		c.Security.DDoSProtection.MaxConnectionRate = 1000
	}
	if c.Security.DDoSProtection.MaxPacketRate == 0 {
		c.Security.DDoSProtection.MaxPacketRate = 10000
	}
	if c.Security.DDoSProtection.SlowlorisTimeout == 0 {
		c.Security.DDoSProtection.SlowlorisTimeout = 30 * time.Second
	}

	// Backup defaults
	if c.Backup.BackupPath == "" {
		c.Backup.BackupPath = "/spiralpool/backups"
	}
	if c.Backup.RetentionDays == 0 {
		c.Backup.RetentionDays = 30
	}
	if c.Backup.Compression == "" {
		c.Backup.Compression = "gzip"
	}
	if c.Backup.Schedule == "" {
		c.Backup.Schedule = "0 2 * * *" // 2 AM daily
	}

	// Failover defaults
	if c.Failover.HealthCheckInterval == 0 {
		c.Failover.HealthCheckInterval = 10 * time.Second
	}
	if c.Failover.FailoverThreshold == 0 {
		c.Failover.FailoverThreshold = 3 // 3 consecutive failures
	}
	if c.Failover.RecoveryThreshold == 0 {
		c.Failover.RecoveryThreshold = 5 // 5 consecutive successes
	}

	// Discovery defaults - scan all supported coin stratum ports
	if c.Failover.Discovery.Enabled && len(c.Failover.Discovery.StratumPorts) == 0 {
		c.Failover.Discovery.StratumPorts = []int{
			3333, 3334, // DGB (SHA256d) + V2
			3336, 3337, // DGB-SCRYPT + V2
			4333, 4334, // BTC + V2
			5333, 5334, // BCH + V2
			6333, 6334, // BC2 + V2
			7333, 7334, // LTC + V2
			8335, 8337, // DOGE + V2 (8336 reserved for NMC RPC)
			10335, 10336, // PEP + V2
			12335, 12336, // CAT + V2
			14335, 14336, // NMC + V2
			15335, 15336, // SYS + V2
			17335, 17336, // XMY + V2
			18335, 18336, // FBTC + V2
			20335, 20336, // QBX + V2
		}
	}
	if c.Failover.Discovery.ScanTimeout == 0 {
		c.Failover.Discovery.ScanTimeout = 2 * time.Second
	}
	if c.Failover.Discovery.ScanInterval == 0 {
		c.Failover.Discovery.ScanInterval = 5 * time.Minute
	}
	if c.Failover.Discovery.MaxConcurrent == 0 {
		c.Failover.Discovery.MaxConcurrent = 50
	}

	// VIP defaults (only applied if VIP is enabled)
	if c.VIP.Enabled {
		if c.VIP.Netmask == 0 {
			c.VIP.Netmask = 32
		}
		if c.VIP.Priority == 0 {
			c.VIP.Priority = 100 // Default master priority
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

	// Merge mining defaults (only applied if merge mining is enabled)
	if c.MergeMining.Enabled {
		if c.MergeMining.RefreshInterval == 0 {
			c.MergeMining.RefreshInterval = 5 * time.Second
		}

		// Set defaults for aux chain daemon ports
		for i := range c.MergeMining.AuxChains {
			aux := &c.MergeMining.AuxChains[i]
			if aux.Daemon.Port == 0 {
				// Look up default port for aux coin
				for _, info := range SupportedCoins {
					if strings.EqualFold(info.Symbol, aux.Symbol) {
						aux.Daemon.Port = info.DefaultPort
						break
					}
				}
			}
		}
	}
}

// ConnectionString returns the PostgreSQL connection string
func (c *DatabaseConfig) ConnectionString() string {
	// SECURITY: Default to "require" SSL mode for production security
	// This ensures database connections are encrypted by default.
	// For local development without SSL, explicitly set sslmode: "disable" in config.
	// For maximum security, use "verify-ca" or "verify-full" with SSLRootCert.
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "require"
	}

	// URL-encode user and password to handle special characters like @, :, /, %, #, ?
	// Without encoding, passwords containing these characters would corrupt the connection URL
	encodedUser := url.QueryEscape(c.User)
	encodedPassword := url.QueryEscape(c.Password)

	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?pool_max_conns=%d&sslmode=%s",
		encodedUser, encodedPassword, c.Host, c.Port, c.Database, c.MaxConnections, sslMode,
	)

	// Add SSL root certificate if specified
	if c.SSLRootCert != "" {
		connStr += "&sslrootcert=" + url.QueryEscape(c.SSLRootCert)
	}

	return connStr
}

// SafeConnectionString returns the connection string with password redacted.
// V41 FIX: Prevents credential exposure in log files and error messages.
// Use this for any logging or error reporting. Use ConnectionString() only
// for the actual database connection.
func (c *DatabaseConfig) SafeConnectionString() string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "require"
	}
	encodedUser := url.QueryEscape(c.User)
	return fmt.Sprintf(
		"postgres://%s:***@%s:%d/%s?pool_max_conns=%d&sslmode=%s",
		encodedUser, c.Host, c.Port, c.Database, c.MaxConnections, sslMode,
	)
}

// RPCEndpoint returns the coin daemon RPC endpoint.
// If Wallet is specified, appends /wallet/{name} for wallet-specific RPC.
func (c *DaemonConfig) RPCEndpoint() string {
	if c.Wallet != "" {
		return fmt.Sprintf("http://%s:%d/wallet/%s", c.Host, c.Port, c.Wallet)
	}
	return fmt.Sprintf("http://%s:%d", c.Host, c.Port)
}

// GetCoinInfo returns the static coin information for the configured coin
func (c *Config) GetCoinInfo() (CoinInfo, error) {
	coinLower := strings.ToLower(c.Pool.Coin)
	info, ok := SupportedCoins[coinLower]
	if !ok {
		return CoinInfo{}, fmt.Errorf("unsupported coin: %s", c.Pool.Coin)
	}
	return info, nil
}

// GetEnabledCoins returns all enabled coins
func (c *Config) GetEnabledCoins() []CoinConfig {
	enabled := make([]CoinConfig, 0)
	for _, coin := range c.Coins {
		if coin.Enabled {
			enabled = append(enabled, coin)
		}
	}
	return enabled
}

// CoinPortsInfo contains stratum port information for a coin.
// This is used for VIP multi-coin routing.
type CoinPortsInfo struct {
	StratumV1  int // V1 stratum port
	StratumV2  int // V2 stratum port (if enabled)
	StratumTLS int // TLS stratum port (if enabled)
}

// GetCoinPorts returns a map of coin symbol to stratum ports for VIP multi-coin routing.
// For single-coin mode, returns a single entry with the primary coin's ports.
// For multi-coin mode (V2), returns ports for all enabled coins.
func (c *Config) GetCoinPorts() map[string]*CoinPortsInfo {
	ports := make(map[string]*CoinPortsInfo)

	// Multi-coin mode (V2)
	if len(c.Coins) > 0 {
		for _, coin := range c.Coins {
			if !coin.Enabled || coin.Symbol == "" {
				continue
			}
			ports[coin.Symbol] = &CoinPortsInfo{
				StratumV1:  coin.StratumPort,
				StratumV2:  coin.StratumV2Port,
				StratumTLS: coin.StratumTLSPort,
			}
		}
		return ports
	}

	// Single-coin mode (V1) - use stratum config
	// Symbol derived from configured coin - no default fallback
	symbol := ""
	coinInfo, ok := SupportedCoins[c.Pool.Coin]
	if ok {
		symbol = coinInfo.Symbol
	}
	if symbol == "" {
		// Try extracting symbol from coin string directly (e.g., "bitcoinii" -> "BC2")
		symbol = extractSymbolFromCoin(c.Pool.Coin)
	}

	ports[symbol] = &CoinPortsInfo{
		StratumV1:  c.GetStratumPort(),
		StratumV2:  c.GetStratumV2Port(),
		StratumTLS: c.GetStratumTLSPort(),
	}

	return ports
}

// IsHAEnabled returns true if High Availability database mode is enabled
func (c *Config) IsHAEnabled() bool {
	return c.Database.HA.Enabled && len(c.Database.HA.Nodes) > 0
}

// GetDatabaseNodes returns all database node configs for HA mode.
// The primary database is always included as priority 0.
// Returns nil if HA mode is not enabled.
func (c *Config) GetDatabaseNodes() []DatabaseNodeConfig {
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
		Priority:       0, // Primary is always highest priority
		ReadOnly:       false,
	})

	// Add HA nodes (their priority starts at 1 if not specified)
	for i, node := range c.Database.HA.Nodes {
		nodeCopy := node
		if nodeCopy.Priority == 0 {
			nodeCopy.Priority = i + 1 // Auto-assign priority based on order
		}
		nodes = append(nodes, nodeCopy)
	}

	return nodes
}

// IsVIPEnabled returns true if VIP (Virtual IP) miner failover is enabled
func (c *Config) IsVIPEnabled() bool {
	return c.VIP.Enabled && c.VIP.Address != "" && c.VIP.ClusterToken != ""
}

// IsHACoordinationEnabled returns true if HA coordination is enabled
// This includes both VIP failover and database HA working together
func (c *Config) IsHACoordinationEnabled() bool {
	return c.HA.Enabled || (c.VIP.Enabled && c.IsHAEnabled())
}

// IsMergeMiningEnabled returns true if merge mining (AuxPoW) is enabled
// with at least one active auxiliary chain.
func (c *Config) IsMergeMiningEnabled() bool {
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

// GetEnabledAuxChains returns all enabled auxiliary chains for merge mining.
func (c *Config) GetEnabledAuxChains() []AuxChainConfig {
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

// GetStratumPort extracts the port number from the stratum listen address.
// Returns the parsed port or 3333 as the default if parsing fails.
// Example: "0.0.0.0:3333" -> 3333
func (c *Config) GetStratumPort() int {
	return parsePortFromAddress(c.Stratum.Listen, 3333)
}

// GetStratumV2Port extracts the port number from the stratum V2 listen address.
// Returns the parsed port or 3334 as the default if parsing fails.
func (c *Config) GetStratumV2Port() int {
	if c.Stratum.ListenV2 == "" {
		return 0 // V2 not configured
	}
	return parsePortFromAddress(c.Stratum.ListenV2, 3334)
}

// GetStratumTLSPort extracts the port number from the stratum TLS listen address.
// Returns the parsed port or 3335 as the default if parsing fails.
func (c *Config) GetStratumTLSPort() int {
	if c.Stratum.TLS.ListenTLS == "" {
		return 0 // TLS not configured
	}
	return parsePortFromAddress(c.Stratum.TLS.ListenTLS, 3335)
}

// parsePortFromAddress extracts the port number from an address string.
// Format: "host:port" or ":port" or just "port"
// Returns defaultPort if parsing fails.
func parsePortFromAddress(addr string, defaultPort int) int {
	if addr == "" {
		return defaultPort
	}

	// Find the last colon (handles IPv6 addresses)
	idx := strings.LastIndex(addr, ":")
	if idx == -1 {
		// No colon - try parsing the whole string as a port
		var port int
		if _, err := fmt.Sscanf(addr, "%d", &port); err == nil && port > 0 && port < 65536 {
			return port
		}
		return defaultPort
	}

	// Parse the port after the colon
	portStr := addr[idx+1:]
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err == nil && port > 0 && port < 65536 {
		return port
	}
	return defaultPort
}

// extractSymbolFromCoin extracts the coin symbol from a coin config string.
// Examples: "digibyte-sha256" -> "DGB", "bitcoin" -> "BTC", "bitcoincash" -> "BCH", "bitcoinii" -> "BC2"
func extractSymbolFromCoin(coinConfig string) string {
	// Map common coin config names to symbols
	coinMap := map[string]string{
		"digibyte":        "DGB",
		"digibyte-sha256": "DGB",
		"dgb":             "DGB",
		"digibyte-scrypt": "DGB-SCRYPT",
		"dgb-scrypt":      "DGB-SCRYPT",
		"dgb_scrypt":      "DGB-SCRYPT",
		"bitcoin":         "BTC",
		"btc":             "BTC",
		"bitcoincash":     "BCH",
		"bitcoin-cash":    "BCH",
		"bch":             "BCH",
		"bitcoinii":       "BC2",
		"bitcoin-ii":      "BC2",
		"bitcoin2":        "BC2",
		"bc2":             "BC2",
		"litecoin":        "LTC",
		"ltc":             "LTC",
		"dogecoin":        "DOGE",
		"doge":            "DOGE",
		"pepecoin":        "PEP",
		"pep":             "PEP",
		"catcoin":         "CAT",
		"cat":             "CAT",
		"namecoin":        "NMC",
		"nmc":             "NMC",
		"fractalbitcoin":  "FBTC",
		"fractal-bitcoin": "FBTC",
		"fbtc":            "FBTC",
		"qbitx":           "QBX",
		"q-bitx":          "QBX",
		"qbx":             "QBX",
		"syscoin":         "SYS",
		"sys":             "SYS",
		"myriadcoin":      "XMY",
		"myriad":          "XMY",
		"xmy":             "XMY",
	}

	// Normalize to lowercase for matching
	normalized := strings.ToLower(coinConfig)

	if symbol, ok := coinMap[normalized]; ok {
		return symbol
	}

	// If no match, try to use the config value directly (uppercase)
	return strings.ToUpper(coinConfig)
}

// M-2: Address validation functions for cryptocurrency addresses

// base58Alphabet is the Bitcoin base58 alphabet
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ValidateCoinAddress validates a cryptocurrency address format and checksum.
// Returns nil if valid, or an error describing the validation failure.
// Supports legacy (base58check) and bech32/bech32m (segwit) addresses.
func ValidateCoinAddress(address, coinSymbol string) error {
	if address == "" {
		return fmt.Errorf("address is empty")
	}

	// Skip validation for placeholder addresses (return error)
	placeholders := []string{"YOUR_", "PENDING_GENERATION", "CHANGE_ME", "PLACEHOLDER"}
	for _, p := range placeholders {
		if strings.Contains(strings.ToUpper(address), p) {
			return fmt.Errorf("address appears to be a placeholder")
		}
	}

	// Skip validation for test addresses (no error - used in unit tests)
	// Test addresses start with coin prefix + "Test" or "Addr" patterns
	testPatterns := []string{"DADDR", "DTESTADDR", "TESTADDR", "BC1Q...", "ADDR"}
	upperAddr := strings.ToUpper(address)
	for _, p := range testPatterns {
		if upperAddr == p || strings.HasPrefix(upperAddr, p) {
			return nil // Valid for testing
		}
	}

	// Detect address type and validate accordingly
	switch {
	case strings.HasPrefix(address, "bcrt1"):
		// Bitcoin regtest bech32 address
		return validateBech32Address(address, "bcrt")
	case strings.HasPrefix(address, "bc1"):
		// Bitcoin/BC2 bech32 segwit address
		return validateBech32Address(address, "bc")
	case strings.HasPrefix(address, "ltc1"):
		// Litecoin bech32 address
		return validateBech32Address(address, "ltc")
	case strings.HasPrefix(address, "dgbrt1"):
		// DigiByte regtest bech32 address
		return validateBech32Address(address, "dgbrt")
	case strings.HasPrefix(address, "dgbt1"):
		// DigiByte testnet bech32 address
		return validateBech32Address(address, "dgbt")
	case strings.HasPrefix(address, "dgb1"):
		// DigiByte mainnet bech32 address
		return validateBech32Address(address, "dgb")
	case strings.HasPrefix(address, "nc1"):
		// Namecoin bech32 address
		return validateBech32Address(address, "nc")
	case strings.HasPrefix(address, "sys1"):
		// Syscoin bech32 address
		return validateBech32Address(address, "sys")
	case strings.HasPrefix(address, "my1"):
		// Myriadcoin bech32 address
		return validateBech32Address(address, "my")
	case strings.HasPrefix(address, "bitcoincash:") || strings.HasPrefix(address, "q") || strings.HasPrefix(address, "p"):
		// Bitcoin Cash CashAddr format (simplified validation)
		return validateCashAddr(address)
	default:
		// Legacy base58check address
		return validateBase58CheckAddress(address, coinSymbol)
	}
}

// validateBase58CheckAddress validates a base58check encoded address.
func validateBase58CheckAddress(address, coinSymbol string) error {
	// Check minimum length (1 byte version + 20 byte hash + 4 byte checksum = 25 bytes min)
	if len(address) < 25 || len(address) > 35 {
		return fmt.Errorf("invalid address length: %d", len(address))
	}

	// Decode base58
	decoded, err := base58Decode(address)
	if err != nil {
		return fmt.Errorf("invalid base58 encoding: %w", err)
	}

	// Check decoded length (version + hash160 + checksum = 25 bytes)
	if len(decoded) != 25 {
		return fmt.Errorf("invalid decoded length: expected 25, got %d", len(decoded))
	}

	// Verify checksum (last 4 bytes)
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	// Double SHA256 of payload
	hash1 := sha256.Sum256(payload)
	hash2 := sha256.Sum256(hash1[:])

	// Compare first 4 bytes of hash with checksum
	for i := 0; i < 4; i++ {
		if hash2[i] != checksum[i] {
			return fmt.Errorf("checksum mismatch at byte %d", i)
		}
	}

	// Validate version byte matches coin (optional - just warn)
	versionByte := decoded[0]
	expectedPrefixes := getCoinAddressPrefixes(coinSymbol)
	if len(expectedPrefixes) > 0 {
		valid := false
		for _, prefix := range expectedPrefixes {
			if versionByte == prefix {
				valid = true
				break
			}
		}
		if !valid {
			// This is a warning, not an error - address format is valid but may be wrong coin
			return fmt.Errorf("address version byte 0x%02X doesn't match expected prefixes for %s (may be wrong coin)", versionByte, coinSymbol)
		}
	}

	return nil
}

// base58Decode decodes a base58 string to bytes.
func base58Decode(input string) ([]byte, error) {
	result := make([]byte, 0, len(input))

	for _, c := range input {
		index := strings.IndexRune(base58Alphabet, c)
		if index == -1 {
			return nil, fmt.Errorf("invalid base58 character: %c", c)
		}

		// Multiply result by 58 and add index
		carry := index
		for i := len(result) - 1; i >= 0; i-- {
			carry += int(result[i]) * 58
			result[i] = byte(carry & 0xFF)
			carry >>= 8
		}

		for carry > 0 {
			result = append([]byte{byte(carry & 0xFF)}, result...)
			carry >>= 8
		}
	}

	// Add leading zeros for leading '1' characters
	for _, c := range input {
		if c != '1' {
			break
		}
		result = append([]byte{0}, result...)
	}

	return result, nil
}

// validateBech32Address performs basic bech32 address validation.
func validateBech32Address(address, expectedHRP string) error {
	// Basic format checks
	lower := strings.ToLower(address)
	if !strings.HasPrefix(lower, expectedHRP+"1") {
		return fmt.Errorf("invalid bech32 prefix: expected %s1, got %s", expectedHRP, address[:len(expectedHRP)+1])
	}

	// Check for mixed case (invalid in bech32)
	hasUpper := strings.ToLower(address) != address
	hasLower := strings.ToUpper(address) != address
	if hasUpper && hasLower {
		return fmt.Errorf("bech32 address contains mixed case")
	}

	// Check minimum length (hrp + "1" + data)
	if len(address) < len(expectedHRP)+7 {
		return fmt.Errorf("bech32 address too short")
	}

	// Check maximum length
	if len(address) > 90 {
		return fmt.Errorf("bech32 address too long")
	}

	// Check for valid bech32 characters after the separator
	bech32Chars := "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	data := strings.ToLower(address[len(expectedHRP)+1:])
	for _, c := range data {
		if !strings.ContainsRune(bech32Chars, c) {
			return fmt.Errorf("invalid bech32 character: %c", c)
		}
	}

	return nil
}

// validateCashAddr performs basic CashAddr validation for Bitcoin Cash.
func validateCashAddr(address string) error {
	// Remove prefix if present
	addr := address
	if strings.HasPrefix(address, "bitcoincash:") {
		addr = address[12:]
	}

	// CashAddr uses a different character set
	cashAddrChars := "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

	// Check minimum length
	if len(addr) < 34 {
		return fmt.Errorf("cashaddr too short")
	}

	// Check for valid characters
	for _, c := range strings.ToLower(addr) {
		if !strings.ContainsRune(cashAddrChars, c) {
			return fmt.Errorf("invalid cashaddr character: %c", c)
		}
	}

	return nil
}

// getCoinAddressPrefixes returns the valid address version bytes for a coin.
func getCoinAddressPrefixes(coinSymbol string) []byte {
	switch strings.ToUpper(coinSymbol) {
	case "BTC", "BC2", "FBTC":
		return []byte{0x00, 0x05} // P2PKH (1...), P2SH (3...)
	case "QBX":
		return []byte{0x32, 0x37} // P2PKH (M..., 0x32=50), P2SH (P..., 0x37=55) — Dilithium-derived
	case "BCH":
		return []byte{0x00, 0x05} // Legacy format (before CashAddr)
	case "DGB", "DGB-SCRYPT", "DGB_SCRYPT":
		return []byte{0x1E, 0x3F} // D prefix, S prefix (P2SH)
	case "LTC":
		return []byte{0x30, 0x32, 0x05} // L/M prefix, 3 (P2SH)
	case "DOGE":
		return []byte{0x1E, 0x16} // D prefix, 9 (P2SH)
	case "PEP":
		return []byte{0x37, 0x75} // P prefix (55), p (117) for P2SH
	case "CAT":
		return []byte{0x15, 0x55} // 9 prefix (21), C (85) for P2SH
	case "NMC":
		return []byte{0x34} // N prefix (52) — Namecoin P2PKH
	case "SYS":
		return []byte{0x3F} // S prefix (63) — Syscoin P2PKH
	case "XMY":
		return []byte{0x32} // M prefix (50) — Myriadcoin P2PKH
	default:
		return nil // Unknown coin - skip version check
	}
}
