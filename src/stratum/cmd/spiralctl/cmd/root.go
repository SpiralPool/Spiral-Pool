// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package cmd implements the spiralctl command-line interface.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Version information (set by main.go)
var (
	Version   = "2.0.0"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

// Global flags (shared across all commands)
var globalYesFlag bool // --yes flag to skip confirmation prompts

// Default paths
const (
	DefaultConfigDir      = "/spiralpool/config"
	DefaultConfigFile     = "/spiralpool/config/config.yaml"
	// SHA-256d coins
	DefaultBTCConfig      = "/spiralpool/btc/bitcoin.conf"
	DefaultBCHConfig      = "/spiralpool/bch/bitcoin.conf"
	DefaultDGBConfig      = "/spiralpool/dgb/digibyte.conf"
	DefaultBC2Config      = "/spiralpool/bc2/bitcoinii.conf"
	DefaultNMCConfig      = "/spiralpool/nmc/namecoin.conf"
	DefaultSYSConfig      = "/spiralpool/sys/syscoin.conf"
	DefaultXMYConfig      = "/spiralpool/xmy/myriadcoin.conf"
	DefaultFBTCConfig     = "/spiralpool/fbtc/fractal.conf"
	DefaultQBXConfig      = "/spiralpool/qbx/qbitx.conf"
	// Scrypt coins
	DefaultLTCConfig       = "/spiralpool/ltc/litecoin.conf"
	DefaultDOGEConfig      = "/spiralpool/doge/dogecoin.conf"
	DefaultDGBScryptConfig = "/spiralpool/dgb/digibyte.conf" // Shares DGB daemon
	DefaultPEPConfig       = "/spiralpool/pep/pepecoin.conf"
	DefaultCATConfig       = "/spiralpool/cat/catcoin.conf"
)

// Colors for terminal output
const (
	ColorReset   = "\033[0m"
	ColorRed     = "\033[0;31m"
	ColorGreen   = "\033[0;32m"
	ColorYellow  = "\033[1;33m"
	ColorBlue    = "\033[0;34m"
	ColorCyan    = "\033[0;36m"
	ColorMagenta = "\033[0;35m"
	ColorBold    = "\033[1m"
)

// Execute runs the root command
func Execute() error {
	// Parse global --yes flag before command dispatch
	parseGlobalFlags()

	if len(os.Args) < 2 {
		printUsage()
		return nil
	}

	command := os.Args[1]

	switch command {
	case "status":
		return runStatus(os.Args[2:])
	case "tor":
		return runTor(os.Args[2:])
	case "ha":
		return runHA(os.Args[2:])
	case "vip":
		return runVIP(os.Args[2:])
	case "coin":
		return runCoin(os.Args[2:])
	case "node":
		return runNode(os.Args[2:])
	case "config":
		return runConfig(os.Args[2:])
	case "pool":
		return runPool(os.Args[2:])
	case "mining":
		return runMining(os.Args[2:])
	case "external":
		return runExternal(os.Args[2:])
	case "gdpr-delete":
		return runGDPRDelete(os.Args[2:])
	case "version", "-v", "--version":
		printVersion()
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command: %s\nRun 'spiralctl help' for usage", command)
	}
}

// parseGlobalFlags extracts global flags like --yes before command parsing
func parseGlobalFlags() {
	newArgs := []string{os.Args[0]}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--yes" || arg == "-y" {
			globalYesFlag = true
		} else {
			newArgs = append(newArgs, arg)
		}
	}
	os.Args = newArgs
}

func printBanner() {
	fmt.Printf("%s", ColorMagenta)
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println("|                                                               |")
	fmt.Println("|                     S P I R A L   P O O L                     |")
	fmt.Println("|                     --- CONFIGURATION ---                     |")
	fmt.Println("|                                                               |")
	fmt.Println("+---------------------------------------------------------------+")
	versionStr := fmt.Sprintf("v%s", Version)
	pad := (63 - len(versionStr)) / 2
	fmt.Printf("|%*s%-*s|\n", pad+len(versionStr), versionStr, 63-pad-len(versionStr), "")
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Printf("%s\n", ColorReset)
}

func printVersion() {
	fmt.Printf("spiralctl v%s\n", Version)
	fmt.Printf("Build Time: %s\n", BuildTime)
	fmt.Printf("Git Commit: %s\n", GitCommit)
}

func printUsage() {
	printBanner()
	fmt.Println("Usage: spiralctl <command> [options]")
	fmt.Println()
	fmt.Printf("%sGlobal Options:%s\n", ColorBold, ColorReset)
	fmt.Println("  --yes, -y    Skip confirmation prompts (for automation)")
	fmt.Println()
	fmt.Printf("%sCommands:%s\n", ColorBold, ColorReset)
	fmt.Printf("  %sstatus%s      Show current pool status and configuration\n", ColorCyan, ColorReset)
	fmt.Printf("  %smining%s      Mining mode management (solo/multi/merge)\n", ColorCyan, ColorReset)
	fmt.Printf("  %stor%s         Enable/disable Tor routing for blockchain nodes\n", ColorCyan, ColorReset)
	fmt.Printf("  %sha%s          Enable/disable High Availability (database failover)\n", ColorCyan, ColorReset)
	fmt.Printf("  %svip%s         Enable/disable VIP (Virtual IP) for miner failover\n", ColorCyan, ColorReset)
	fmt.Printf("  %scoin%s        List coins and blockchain sync status\n", ColorCyan, ColorReset)
	fmt.Printf("  %snode%s        Manage blockchain nodes (install, update, restart)\n", ColorCyan, ColorReset)
	fmt.Printf("  %sconfig%s      Configuration management (validate, etc.)\n", ColorCyan, ColorReset)
	fmt.Printf("  %spool%s        Pool statistics and management\n", ColorCyan, ColorReset)
	fmt.Printf("  %sexternal%s    External access for hashrate rental services\n", ColorCyan, ColorReset)
	fmt.Printf("  %sgdpr-delete%s Delete miner data for GDPR/CCPA compliance\n", ColorCyan, ColorReset)
	fmt.Printf("  %sversion%s     Show version information\n", ColorCyan, ColorReset)
	fmt.Printf("  %shelp%s        Show this help message\n", ColorCyan, ColorReset)
	fmt.Println()
	fmt.Printf("%sExamples:%s\n", ColorBold, ColorReset)
	fmt.Println("  spiralctl status                            # Show pool status")
	fmt.Println("  spiralctl mining status                     # Show mining configuration")
	fmt.Println("  spiralctl mining solo dgb                   # Mine DigiByte only")
	fmt.Println("  spiralctl mining multi btc,bch              # Mine multiple coins")
	fmt.Println("  spiralctl mining merge enable nmc,sys,fbtc  # Enable merge mining")
	fmt.Println("  spiralctl coin list                         # List all supported coins")
	fmt.Println("  spiralctl coin status                       # Show blockchain sync status")
	fmt.Println("  spiralctl node restart all                  # Restart all nodes")
	fmt.Println("  spiralctl node logs btc                     # View Bitcoin node logs")
	fmt.Println("  spiralctl tor enable --all                  # Enable Tor for all nodes")
	fmt.Println("  spiralctl ha enable --primary 192.168.1.10 --replica 192.168.1.11")
	fmt.Println("  spiralctl vip enable --address 192.168.1.200 --interface ens33")
	fmt.Println()
	fmt.Printf("%sExternal Access (Hashrate Rental Services):%s\n", ColorBold, ColorReset)
	fmt.Println("  spiralctl external setup                    # Configure external access")
	fmt.Println("  spiralctl external setup --mode tunnel      # Use Cloudflare tunnel")
	fmt.Println("  spiralctl external setup --mode port-forward # Use port forwarding")
	fmt.Println("  spiralctl external enable                   # Enable external access")
	fmt.Println("  spiralctl external disable                  # Disable external access")
	fmt.Println("  spiralctl external status                   # Show external access status")
	fmt.Println("  spiralctl external test                     # Test external connectivity")
	fmt.Println()
	fmt.Printf("%sSupported Coins:%s\n", ColorBold, ColorReset)
	fmt.Println("  SHA-256d: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, qbx")
	fmt.Println("  Scrypt:   ltc, doge, dgb-scrypt, pep, cat")
	fmt.Println()
	fmt.Printf("%sMerge Mining (AuxPoW):%s\n", ColorBold, ColorReset)
	fmt.Println("  SHA-256d: BTC can merge-mine NMC, SYS, XMY, FBTC")
	fmt.Println("  Scrypt:   LTC can merge-mine DOGE, PEP")
	fmt.Println()
	fmt.Printf("%sConfiguration Files:%s\n", ColorBold, ColorReset)
	fmt.Printf("  Pool Config:       %s\n", DefaultConfigFile)
	fmt.Println("  SHA-256d Coins:")
	fmt.Printf("    Bitcoin Knots:     %s\n", DefaultBTCConfig)
	fmt.Printf("    Bitcoin Cash:      %s\n", DefaultBCHConfig)
	fmt.Printf("    DigiByte:          %s\n", DefaultDGBConfig)
	fmt.Printf("    Bitcoin II:        %s\n", DefaultBC2Config)
	fmt.Printf("    Namecoin:          %s\n", DefaultNMCConfig)
	fmt.Printf("    Syscoin:           %s\n", DefaultSYSConfig)
	fmt.Printf("    Myriad:            %s\n", DefaultXMYConfig)
	fmt.Printf("    Fractal Bitcoin:   %s\n", DefaultFBTCConfig)
	fmt.Printf("    Q-BitX:            %s\n", DefaultQBXConfig)
	fmt.Println("  Scrypt Coins:")
	fmt.Printf("    Litecoin:          %s\n", DefaultLTCConfig)
	fmt.Printf("    Dogecoin:          %s\n", DefaultDOGEConfig)
	fmt.Printf("    DigiByte-Scrypt:   %s\n", DefaultDGBScryptConfig)
	fmt.Printf("    PepeCoin:          %s\n", DefaultPEPConfig)
	fmt.Printf("    Catcoin:           %s\n", DefaultCATConfig)
	fmt.Println()
}

// Helper functions

func printSuccess(msg string) {
	fmt.Printf("%s✓ %s%s\n", ColorGreen, msg, ColorReset)
}

func printError(msg string) {
	fmt.Printf("%s✗ %s%s\n", ColorRed, msg, ColorReset)
}

func printWarning(msg string) {
	fmt.Printf("%s⚠ %s%s\n", ColorYellow, msg, ColorReset)
}

func printInfo(msg string) {
	fmt.Printf("%s→ %s%s\n", ColorCyan, msg, ColorReset)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ensureRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("this command requires root privileges. Please run with sudo")
	}
	return nil
}

func backupFile(path string) error {
	if !fileExists(path) {
		return nil
	}

	backupDir := "/spiralpool/backups"
	// SECURITY: Use 0750 for directory permissions (G301 fix)
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	// G304: Path comes from known config file locations, not untrusted input
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return fmt.Errorf("failed to read file for backup: %w", err)
	}

	backupPath := filepath.Join(backupDir, filepath.Base(path)+".backup")
	// SECURITY: Backup files may contain sensitive data, use 0600 (G306 fix)
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write backup: %w", err)
	}

	return nil
}

// Config represents the main pool configuration
type Config struct {
	Version  int                    `yaml:"version"`
	Global   GlobalConfig           `yaml:"global"`
	Database DatabaseConfig         `yaml:"database"`
	VIP      VIPConfig              `yaml:"vip"`
	HA       HAConfig               `yaml:"ha"`
	Coins    map[string]interface{} `yaml:"coins"`
	Pool     PoolConfig             `yaml:"pool"`
}

type GlobalConfig struct {
	LogLevel    string `yaml:"log_level"`
	LogFormat   string `yaml:"log_format"`
	MetricsPort int    `yaml:"metrics_port"`
	APIPort     int    `yaml:"api_port"`
	APIEnabled  bool   `yaml:"api_enabled"`
}

type DatabaseConfig struct {
	Type     string `yaml:"type"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	SSLMode  string `yaml:"sslMode,omitempty"`
}

type VIPConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Address           string `yaml:"address"`
	Interface         string `yaml:"interface"`
	Netmask           int    `yaml:"netmask"`
	Priority          int    `yaml:"priority"`
	AutoPriority      bool   `yaml:"autoPriority"`
	ClusterToken      string `yaml:"clusterToken"`
	CanBecomeMaster   bool   `yaml:"canBecomeMaster"`
	DiscoveryPort     int    `yaml:"discoveryPort"`
	StatusPort        int    `yaml:"statusPort"`
	HeartbeatInterval string `yaml:"heartbeatInterval"`
	FailoverTimeout   string `yaml:"failoverTimeout"`
}

type HAConfig struct {
	Enabled         bool   `yaml:"enabled"`
	PrimaryHost     string `yaml:"primaryHost"`
	ReplicaHost     string `yaml:"replicaHost"`
	CheckInterval   string `yaml:"checkInterval"`
	FailoverTimeout string `yaml:"failoverTimeout"`
}

type PoolConfig struct {
	ID   string `yaml:"id"`
	Coin string `yaml:"coin"`
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning(fmt.Sprintf("Failed to backup config: %v", err))
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// SECURITY: Config file contains credentials, use 0600 (G306 fix)
	if err := os.WriteFile(DefaultConfigFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
