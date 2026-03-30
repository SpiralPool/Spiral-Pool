// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package cmd implements the spiralctl command-line interface.
package cmd

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// coinMetadata contains metadata for each supported coin
type coinMetadata struct {
	name      string
	algorithm string
	config    string
	service   string
	cliCmd    string
	rpcPort   int
}

// coinRegistry maps coin symbols to their metadata
var coinRegistry = map[string]coinMetadata{
	// SHA-256d coins
	"btc":  {"Bitcoin", "sha256d", DefaultBTCConfig, "bitcoind", "bitcoin-cli", 8332},
	"bch":  {"Bitcoin Cash", "sha256d", DefaultBCHConfig, "bitcoind-bch", "bitcoin-cli-bch", 8432},
	"dgb":  {"DigiByte", "sha256d", DefaultDGBConfig, "digibyted", "digibyte-cli", 14022},
	"bc2":  {"Bitcoin II", "sha256d", DefaultBC2Config, "bitcoiniid", "bitcoinii-cli", 8339},
	"nmc":  {"Namecoin", "sha256d", DefaultNMCConfig, "namecoind", "namecoin-cli", 8336},
	"sys":  {"Syscoin", "sha256d", DefaultSYSConfig, "syscoind", "syscoin-cli", 8370},
	"xmy":  {"Myriad", "sha256d", DefaultXMYConfig, "myriadcoind", "myriadcoin-cli", 10889},
	"fbtc": {"Fractal Bitcoin", "sha256d", DefaultFBTCConfig, "fractald", "fractal-cli", 8340},
	"qbx":  {"Q-BitX", "sha256d", DefaultQBXConfig, "qbitxd", "qbitx-cli", 8344},

	// Scrypt coins
	"ltc":        {"Litecoin", "scrypt", DefaultLTCConfig, "litecoind", "litecoin-cli", 9332},
	"doge":       {"Dogecoin", "scrypt", DefaultDOGEConfig, "dogecoind", "dogecoin-cli", 22555},
	"dgb-scrypt": {"DigiByte-Scrypt", "scrypt", DefaultDGBScryptConfig, "digibyted-scrypt", "digibyte-cli", 14022},
	"pep":        {"PepeCoin", "scrypt", DefaultPEPConfig, "pepecoind", "pepecoin-cli", 33873},
	"cat":        {"Catcoin", "scrypt", DefaultCATConfig, "catcoind", "catcoin-cli", 9932},
}

// mergeMiningConfig defines valid parent/aux combinations per algorithm
// SHA-256d: BTC or DGB can merge-mine NMC, SYS, XMY, FBTC
// Scrypt:   LTC can merge-mine DOGE, PEP
type mergeMiningDef struct {
	parents   []string // Valid parent coins for this algorithm
	auxChains []string // All supported aux chains for this algorithm
}

var mergeMiningPairs = map[string]mergeMiningDef{
	"sha256d": {
		parents:   []string{"btc", "dgb"},
		auxChains: []string{"nmc", "sys", "xmy", "fbtc"},
	},
	"scrypt": {
		parents:   []string{"ltc"},
		auxChains: []string{"doge", "pep"},
	},
}

// isValidParent checks if a coin is a valid merge mining parent for a given definition
func (d mergeMiningDef) isValidParent(coin string) bool {
	for _, p := range d.parents {
		if p == coin {
			return true
		}
	}
	return false
}

// syncRequirements contains disk/time estimates for user guidance
var syncRequirements = map[string]struct {
	diskGB   int
	syncDays string
}{
	"btc":  {600, "3-7 days"},
	"bch":  {350, "2-4 days"},
	"dgb":  {45, "1-2 days"},
	"bc2":  {5, "< 1 day"},
	"nmc":  {12, "1-2 days"},
	"sys":  {85, "1-2 days"},
	"xmy":  {6, "< 1 day"},
	"fbtc": {50, "< 1 day"}, // Fractal Bitcoin: 30-second blocks, fast sync
	"qbx":  {5, "< 1 day"},  // Q-BitX: SHA-256d standalone
	"ltc":  {180, "2-4 days"},
	"doge": {75, "1-2 days"},
	"pep":  {2, "< 1 day"},
	"cat":       {1, "< 1 day"},
	"dgb-scrypt": {45, "1-2 days"}, // Shares DGB blockchain
}

// MergeMiningConfig represents the merge mining section in config.yaml
type MergeMiningConfig struct {
	Enabled         bool             `yaml:"enabled"`
	RefreshInterval string           `yaml:"refreshInterval"`
	AuxChains       []AuxChainConfig `yaml:"auxChains"`
}

// AuxChainConfig represents an auxiliary chain configuration
type AuxChainConfig struct {
	Symbol  string       `yaml:"symbol"`
	Enabled bool         `yaml:"enabled"`
	Address string       `yaml:"address"`
	Daemon  DaemonConfig `yaml:"daemon"`
}

// DaemonConfig represents daemon connection settings
type DaemonConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// MultiPortConfig represents the multi_port section in config.yaml
type MultiPortConfig struct {
	Enabled       bool                       `yaml:"enabled"`
	Port          int                        `yaml:"port"`
	Coins         map[string]CoinRouteConfig `yaml:"coins"`
	CheckInterval string                     `yaml:"check_interval,omitempty"`
	PreferCoin    string                     `yaml:"prefer_coin,omitempty"`
	MinTimeOnCoin string                     `yaml:"min_time_on_coin,omitempty"`
	Timezone      string                     `yaml:"timezone,omitempty"`
}

// CoinRouteConfig holds per-coin routing weight
type CoinRouteConfig struct {
	Weight int `yaml:"weight"`
}

// ExtendedConfig extends Config with merge mining support
type ExtendedConfig struct {
	Version     int                    `yaml:"version"`
	Global      GlobalConfig           `yaml:"global"`
	Database    DatabaseConfig         `yaml:"database"`
	VIP         VIPConfig              `yaml:"vip"`
	HA          HAConfig               `yaml:"ha"`
	Coins       map[string]interface{} `yaml:"coins,omitempty"`
	Pool        PoolConfig             `yaml:"pool"`
	MergeMining *MergeMiningConfig     `yaml:"mergeMining,omitempty"`
	MultiPort   *MultiPortConfig       `yaml:"multi_port,omitempty"`
}

// runMining handles the mining subcommand
func runMining(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	fs := flag.NewFlagSet("mining", flag.ExitOnError)
	yesFlag := fs.Bool("yes", false, "Skip confirmation prompts")
	_ = fs.Bool("y", false, "Skip confirmation prompts (short form)")

	if len(args) < 1 {
		printMiningUsage()
		return nil
	}

	action := args[0]

	// Parse remaining args for flags
	if len(args) > 1 {
		// Find where flags start vs positional args
		flagStart := 1
		for i := 1; i < len(args); i++ {
			if strings.HasPrefix(args[i], "-") {
				flagStart = i
				break
			}
			flagStart = i + 1
		}
		if flagStart < len(args) {
			_ = fs.Parse(args[flagStart:]) // #nosec G104
		}
	}

	// Check global yes flag too
	autoYes := *yesFlag || globalYesFlag

	switch action {
	case "status":
		return miningStatus()
	case "solo":
		if len(args) < 2 {
			return fmt.Errorf("coin symbol required. Usage: spiralctl mining solo <coin>")
		}
		return switchToSolo(strings.ToLower(args[1]), autoYes)
	case "multi":
		if len(args) < 2 {
			return fmt.Errorf("coins required. Usage: spiralctl mining multi <coin1,coin2,...>")
		}
		coins := strings.Split(strings.ToLower(args[1]), ",")
		return switchToMulti(coins, autoYes)
	case "merge":
		if len(args) < 2 {
			return fmt.Errorf("action required. Usage: spiralctl mining merge <enable|disable> [aux_coins]")
		}
		switch args[1] {
		case "enable":
			// Optional: specify aux chains (e.g., "spiralctl mining merge enable doge,pep")
			var auxCoins []string
			if len(args) >= 3 && !strings.HasPrefix(args[2], "-") {
				auxCoins = strings.Split(strings.ToLower(args[2]), ",")
			}
			return enableMergeMining(auxCoins, autoYes)
		case "disable":
			return disableMergeMining(autoYes)
		default:
			return fmt.Errorf("unknown merge action: %s. Use 'enable' or 'disable'", args[1])
		}
	case "multiport":
		if len(args) < 2 {
			return multiportStatus()
		}
		switch args[1] {
		case "status":
			return multiportStatus()
		case "enable":
			// Interactive wizard or direct spec
			// spiralctl mining multiport enable              → interactive wizard
			// spiralctl mining multiport enable dgb:80,btc:20 → direct
			if len(args) >= 3 && !strings.HasPrefix(args[2], "-") {
				return multiportEnable(args[2], autoYes)
			}
			return multiportWizard(autoYes)
		case "disable":
			return multiportDisable(autoYes)
		case "weights":
			// spiralctl mining multiport weights dgb:80,btc:20
			if len(args) < 3 || strings.HasPrefix(args[2], "-") {
				return fmt.Errorf("coin weights required. Usage: spiralctl mining multiport weights <coin:weight,...>")
			}
			return multiportWeights(args[2], autoYes)
		default:
			return fmt.Errorf("unknown multiport action: %s. Use 'status', 'enable', 'disable', or 'weights'", args[1])
		}
	default:
		printMiningUsage()
		return fmt.Errorf("unknown action: %s", action)
	}
}

func printMiningUsage() {
	printBanner()
	fmt.Printf("%s=== MINING MODE MANAGEMENT ===%s\n\n", ColorBold, ColorReset)
	fmt.Println("Usage: spiralctl mining <action> [options]")
	fmt.Println()
	fmt.Printf("%sActions:%s\n", ColorBold, ColorReset)
	fmt.Printf("  %sstatus%s           Show current mining mode and configuration\n", ColorCyan, ColorReset)
	fmt.Printf("  %ssolo <coin>%s      Switch to solo mining mode with specified coin\n", ColorCyan, ColorReset)
	fmt.Printf("  %smulti <coins>%s    Switch to multi-coin mode (comma-separated)\n", ColorCyan, ColorReset)
	fmt.Printf("  %smerge enable%s     Enable merge mining (AuxPoW)\n", ColorCyan, ColorReset)
	fmt.Printf("  %smerge disable%s    Disable merge mining\n", ColorCyan, ColorReset)
	fmt.Printf("  %smultiport%s        Show multi-coin smart port status\n", ColorCyan, ColorReset)
	fmt.Printf("  %smultiport enable%s Interactive wizard to set up smart port\n", ColorCyan, ColorReset)
	fmt.Printf("  %smultiport enable <spec>%s\n", ColorCyan, ColorReset)
	fmt.Println("                     Enable with coin:weight pairs (must sum to 100)")
	fmt.Printf("  %smultiport disable%s Disable smart port\n", ColorCyan, ColorReset)
	fmt.Printf("  %smultiport weights <spec>%s\n", ColorCyan, ColorReset)
	fmt.Println("                     Update coin weight allocation (must sum to 100)")
	fmt.Println()
	fmt.Printf("%sOptions:%s\n", ColorBold, ColorReset)
	fmt.Println("  --yes, -y        Skip confirmation prompts (for automation)")
	fmt.Println()
	fmt.Printf("%sSupported Coins (SHA-256d):%s\n", ColorBold, ColorReset)
	fmt.Println("  btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, qbx")
	fmt.Println()
	fmt.Printf("%sSupported Coins (Scrypt):%s\n", ColorBold, ColorReset)
	fmt.Println("  ltc, doge, dgb-scrypt, pep, cat")
	fmt.Println()
	fmt.Printf("%sMerge Mining (AuxPoW):%s\n", ColorBold, ColorReset)
	fmt.Println("  SHA-256d: Bitcoin (BTC) can merge-mine: NMC, SYS, XMY, FBTC")
	fmt.Println("  Scrypt:   Litecoin (LTC) can merge-mine: DOGE, PEP")
	fmt.Println()
	fmt.Printf("%sExamples:%s\n", ColorBold, ColorReset)
	fmt.Println("  spiralctl mining status")
	fmt.Println("  spiralctl mining solo dgb")
	fmt.Println("  spiralctl mining multi btc,bch")
	fmt.Println("  spiralctl mining merge enable                # Enable with default aux chain")
	fmt.Println("  spiralctl mining merge enable nmc           # Enable with specific aux chain")
	fmt.Println("  spiralctl mining merge enable nmc,sys,fbtc  # Enable multiple aux chains (SHA-256d)")
	fmt.Println("  spiralctl mining merge enable doge,pep      # Enable multiple aux chains (Scrypt)")
	fmt.Println("  spiralctl mining solo ltc --yes")
	fmt.Println("  spiralctl mining multiport enable dgb:80,bch:15,btc:5")
	fmt.Println("  spiralctl mining multiport weights dgb:50,btc:50")
	fmt.Println("  spiralctl mining multiport disable")
	fmt.Println()
	fmt.Printf("%sNotes:%s\n", ColorBold, ColorReset)
	fmt.Println("  • Multi-coin mode requires all coins to use the same algorithm")
	fmt.Println("  • Merge mining requires parent chain to be synced first")
	fmt.Println("  • Multi-coin smart port uses weighted 24h UTC scheduling (port 16180)")
	fmt.Println("  • Switching modes will restart the stratum service")
	fmt.Println()
}

// miningStatus displays the current mining configuration
func miningStatus() error {
	printBanner()
	fmt.Printf("%s=== CURRENT MINING STATUS ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadExtendedConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Determine mode
	mode := "solo"
	if cfg.Coins != nil && len(cfg.Coins) > 0 {
		mode = "multi-coin"
	}

	fmt.Printf("%-20s %s%s%s\n", "Mining Mode:", ColorCyan, mode, ColorReset)

	// Show coins
	if mode == "solo" {
		coin := cfg.Pool.Coin
		if coin == "" {
			coin = "not configured"
		}
		coinMeta, ok := coinRegistry[strings.ToLower(coin)]
		if ok {
			fmt.Printf("%-20s %s (%s) - %s\n", "Coin:", strings.ToUpper(coin), coinMeta.name, coinMeta.algorithm)

			// Check sync status
			synced, progress, _ := isCoinSynced(strings.ToLower(coin))
			if synced {
				fmt.Printf("%-20s %s%.2f%% (SYNCED)%s\n", "Sync Status:", ColorGreen, progress, ColorReset)
			} else {
				fmt.Printf("%-20s %s%.2f%%%s\n", "Sync Status:", ColorYellow, progress, ColorReset)
			}
		} else {
			fmt.Printf("%-20s %s\n", "Coin:", coin)
		}
	} else {
		fmt.Printf("%-20s\n", "Enabled Coins:")
		for coinSymbol := range cfg.Coins {
			coinMeta, ok := coinRegistry[strings.ToLower(coinSymbol)]
			if ok {
				synced, progress, _ := isCoinSynced(strings.ToLower(coinSymbol))
				syncStatus := fmt.Sprintf("%s%.2f%%%s", ColorYellow, progress, ColorReset)
				if synced {
					syncStatus = fmt.Sprintf("%s%.2f%% (SYNCED)%s", ColorGreen, progress, ColorReset)
				}
				fmt.Printf("  • %s (%s) - %s - %s\n", strings.ToUpper(coinSymbol), coinMeta.name, coinMeta.algorithm, syncStatus)
			} else {
				fmt.Printf("  • %s\n", strings.ToUpper(coinSymbol))
			}
		}
	}

	fmt.Println()

	// Show merge mining status — check both V1 (root-level) and V2 (per-coin)
	fmt.Printf("%s--- Merge Mining ---%s\n", ColorBold, ColorReset)
	mmFound := false

	// V1 check: root-level mergeMining
	if cfg.MergeMining != nil && cfg.MergeMining.Enabled {
		mmFound = true
		fmt.Printf("%-20s %sEnabled%s\n", "Status:", ColorGreen, ColorReset)
		for _, aux := range cfg.MergeMining.AuxChains {
			if aux.Enabled {
				synced, progress, _ := isCoinSynced(strings.ToLower(aux.Symbol))
				syncStatus := fmt.Sprintf("%s%.2f%%%s", ColorYellow, progress, ColorReset)
				if synced {
					syncStatus = fmt.Sprintf("%s%.2f%% (SYNCED)%s", ColorGreen, progress, ColorReset)
				}
				fmt.Printf("  • Aux Chain: %s - %s\n", aux.Symbol, syncStatus)
			}
		}
	}

	// V2 check: mergeMining inside coin sections (uses raw YAML to detect)
	if !mmFound && cfg.Coins != nil {
		for _, coinData := range cfg.Coins {
			if coinMap, ok := coinData.(map[string]interface{}); ok {
				if mm, exists := coinMap["mergeMining"]; exists {
					if mmMap, ok := mm.(map[string]interface{}); ok {
						if enabled, ok := mmMap["enabled"].(bool); ok && enabled {
							mmFound = true
							fmt.Printf("%-20s %sEnabled%s\n", "Status:", ColorGreen, ColorReset)
							if auxChains, ok := mmMap["auxChains"].([]interface{}); ok {
								for _, ac := range auxChains {
									if acMap, ok := ac.(map[string]interface{}); ok {
										symbol, _ := acMap["symbol"].(string)
										acEnabled, _ := acMap["enabled"].(bool)
										if acEnabled && symbol != "" {
											synced, progress, _ := isCoinSynced(strings.ToLower(symbol))
											syncStatus := fmt.Sprintf("%s%.2f%%%s", ColorYellow, progress, ColorReset)
											if synced {
												syncStatus = fmt.Sprintf("%s%.2f%% (SYNCED)%s", ColorGreen, progress, ColorReset)
											}
											fmt.Printf("  • Aux Chain: %s - %s\n", symbol, syncStatus)
										}
									}
								}
							}
							break
						}
					}
				}
			}
		}
	}

	if !mmFound {
		fmt.Printf("%-20s %sDisabled%s\n", "Status:", ColorRed, ColorReset)
	}

	fmt.Println()

	// Show multi-port status
	fmt.Printf("%s--- Multi-Coin Smart Port ---%s\n", ColorBold, ColorReset)
	if cfg.MultiPort != nil && cfg.MultiPort.Enabled {
		fmt.Printf("%-20s %sEnabled%s (port %d)\n", "Status:", ColorGreen, ColorReset, cfg.MultiPort.Port)
		if len(cfg.MultiPort.Coins) > 0 {
			// Calculate total weight for percentage display
			totalWeight := 0
			for _, rc := range cfg.MultiPort.Coins {
				totalWeight += rc.Weight
			}
			fmt.Printf("%-20s\n", "Schedule:")
			cumulativeFrac := 0.0
			for coin, rc := range cfg.MultiPort.Coins {
				frac := float64(rc.Weight) / float64(totalWeight)
				startH := cumulativeFrac * 24
				cumulativeFrac += frac
				endH := cumulativeFrac * 24
				pct := float64(rc.Weight) * 100.0 / float64(totalWeight)
				fmt.Printf("  • %-6s  %5.0f%%   %05.1fh – %05.1fh UTC\n", strings.ToUpper(coin), pct, startH, endH)
			}
		}
		if cfg.MultiPort.PreferCoin != "" {
			fmt.Printf("%-20s %s\n", "Default Coin:", strings.ToUpper(cfg.MultiPort.PreferCoin))
		}
	} else {
		fmt.Printf("%-20s %sDisabled%s\n", "Status:", ColorRed, ColorReset)
		fmt.Printf("  %sEnable with: spiralctl mining multiport enable dgb:80,bch:15,btc:5%s\n", ColorYellow, ColorReset)
	}

	fmt.Println()

	// Show service status
	fmt.Printf("%s--- Service Status ---%s\n", ColorBold, ColorReset)
	if isServiceRunning("spiralstratum") {
		fmt.Printf("%-20s %sRunning%s\n", "Stratum Pool:", ColorGreen, ColorReset)
	} else {
		fmt.Printf("%-20s %sStopped%s\n", "Stratum Pool:", ColorRed, ColorReset)
	}

	fmt.Println()
	return nil
}

// switchToSolo switches to solo mining mode
func switchToSolo(coin string, autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== SWITCH TO SOLO MINING MODE ===%s\n\n", ColorBold, ColorReset)

	// Validate coin symbol
	coinMeta, ok := coinRegistry[coin]
	if !ok {
		return fmt.Errorf("unknown coin: %s. Run 'spiralctl mining' for supported coins", coin)
	}

	printInfo(fmt.Sprintf("Target coin: %s (%s) - Algorithm: %s", strings.ToUpper(coin), coinMeta.name, coinMeta.algorithm))

	// Check if blockchain is installed
	if !isCoinInstalled(coin) {
		printWarning(fmt.Sprintf("%s blockchain node is not installed", coinMeta.name))
		printInfo("Installing blockchain node...")

		if err := installCoinNode(coin); err != nil {
			return fmt.Errorf("failed to install %s node: %w", coin, err)
		}
		printSuccess(fmt.Sprintf("%s node installation triggered", coinMeta.name))
	}

	// Check if blockchain is synced (STRICT)
	synced, progress, err := isCoinSynced(coin)
	if err != nil {
		// Service might not be running - try to start it
		printInfo(fmt.Sprintf("Starting %s service...", coinMeta.name))
		if startErr := startCoinService(coin); startErr != nil {
			return fmt.Errorf("failed to start %s service: %w", coinMeta.name, startErr)
		}
		// Re-check sync status
		synced, progress, err = isCoinSynced(coin)
	}

	if err != nil {
		return fmt.Errorf("cannot determine sync status for %s: %w", coin, err)
	}

	if !synced {
		printError(fmt.Sprintf("Cannot switch to %s - blockchain not synced (current: %.2f%%)", strings.ToUpper(coin), progress))
		if req, ok := syncRequirements[coin]; ok {
			printInfo(fmt.Sprintf("Estimated sync: ~%d GB disk, %s to complete", req.diskGB, req.syncDays))
		}
		printInfo("Wait for sync to complete or check 'spiralctl coin status'")
		return fmt.Errorf("blockchain not synced")
	}

	printSuccess(fmt.Sprintf("%s blockchain is synced (%.2f%%)", coinMeta.name, progress))

	// Confirm with user
	if !autoYes {
		fmt.Println()
		fmt.Printf("This will switch to solo mining mode with %s.\n", strings.ToUpper(coin))
		fmt.Println("Current configuration will be backed up.")
		fmt.Println("The stratum service will be restarted.")
		fmt.Println()
		if !confirmAction("Proceed with switch to solo mode?") {
			printInfo("Operation cancelled")
			return nil
		}
	}

	// Load and update config
	cfg, err := loadExtendedConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Update for solo mode
	cfg.Pool.Coin = coin
	cfg.Coins = nil // Clear multi-coin config

	// Handle merge mining when switching coins
	if cfg.MergeMining != nil && cfg.MergeMining.Enabled {
		// Check if this coin can be a merge mining parent
		canMerge := false
		var validMmDef mergeMiningDef
		for _, pair := range mergeMiningPairs {
			if pair.isValidParent(coin) {
				canMerge = true
				validMmDef = pair
				break
			}
		}

		if !canMerge {
			printInfo("Disabling merge mining (not applicable for this coin)")
			cfg.MergeMining.Enabled = false
		} else {
			// CRITICAL: Validate existing aux chains are compatible with new parent's algorithm
			// This prevents cross-algorithm contamination when switching e.g. BTC -> LTC
			validAuxSet := make(map[string]bool)
			for _, aux := range validMmDef.auxChains {
				validAuxSet[strings.ToUpper(aux)] = true
			}

			var compatibleAux []AuxChainConfig
			var removedAux []string
			for _, auxCfg := range cfg.MergeMining.AuxChains {
				if validAuxSet[strings.ToUpper(auxCfg.Symbol)] {
					compatibleAux = append(compatibleAux, auxCfg)
				} else {
					removedAux = append(removedAux, auxCfg.Symbol)
				}
			}

			if len(removedAux) > 0 {
				printWarning(fmt.Sprintf("Removing incompatible aux chains: %s (wrong algorithm for %s)",
					strings.Join(removedAux, ", "), strings.ToUpper(coin)))
				cfg.MergeMining.AuxChains = compatibleAux
			}

			// Disable merge mining if no compatible aux chains remain
			if len(compatibleAux) == 0 {
				printInfo("Disabling merge mining (no compatible aux chains)")
				cfg.MergeMining.Enabled = false
			}
		}
	}

	// Save config
	if err := saveExtendedConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("Configuration updated for solo mode")

	// Restart service
	if isServiceRunning("spiralstratum") {
		printInfo("Restarting stratum service...")
		if err := restartService("spiralstratum"); err != nil {
			printWarning(fmt.Sprintf("Failed to restart service: %v", err))
			printInfo("Run: sudo systemctl restart spiralstratum")
		} else {
			printSuccess("Stratum service restarted")
		}
	} else {
		printInfo("Stratum service is not running. Start with: sudo systemctl start spiralstratum")
	}

	fmt.Println()
	printSuccess(fmt.Sprintf("Switched to solo mining mode with %s (%s)", strings.ToUpper(coin), coinMeta.name))

	return nil
}

// switchToMulti switches to multi-coin mining mode
func switchToMulti(coins []string, autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== SWITCH TO MULTI-COIN MINING MODE ===%s\n\n", ColorBold, ColorReset)

	if len(coins) < 2 {
		return fmt.Errorf("multi-coin mode requires at least 2 coins")
	}

	// Validate all coins and check algorithm consistency
	var algo string
	var validCoins []coinMetadata
	for _, coin := range coins {
		meta, ok := coinRegistry[coin]
		if !ok {
			return fmt.Errorf("unknown coin: %s", coin)
		}
		if algo == "" {
			algo = meta.algorithm
		} else if meta.algorithm != algo {
			return fmt.Errorf("cannot mix algorithms: %s uses %s but %s uses %s",
				coins[0], coinRegistry[coins[0]].algorithm, coin, meta.algorithm)
		}
		validCoins = append(validCoins, meta)
	}

	printInfo(fmt.Sprintf("Algorithm: %s", algo))
	printInfo(fmt.Sprintf("Coins: %s", strings.Join(coins, ", ")))
	fmt.Println()

	// Check each coin's status
	var uninstalled, unsynced []string
	for i, coin := range coins {
		meta := validCoins[i]

		if !isCoinInstalled(coin) {
			uninstalled = append(uninstalled, coin)
			fmt.Printf("  %s %s: %sNot installed%s\n", ColorYellow, strings.ToUpper(coin), ColorRed, ColorReset)
			continue
		}

		synced, progress, err := isCoinSynced(coin)
		if err != nil {
			// Try starting service
			_ = startCoinService(coin)
			synced, progress, _ = isCoinSynced(coin)
		}

		if synced {
			fmt.Printf("  %s✓%s %s (%s): %.2f%% %s(SYNCED)%s\n", ColorGreen, ColorReset, strings.ToUpper(coin), meta.name, progress, ColorGreen, ColorReset)
		} else {
			unsynced = append(unsynced, coin)
			fmt.Printf("  %s•%s %s (%s): %.2f%% %s(syncing)%s\n", ColorYellow, ColorReset, strings.ToUpper(coin), meta.name, progress, ColorYellow, ColorReset)
		}
	}

	fmt.Println()

	// Handle uninstalled coins
	if len(uninstalled) > 0 {
		printWarning(fmt.Sprintf("The following coins need to be installed: %s", strings.Join(uninstalled, ", ")))

		if !autoYes && !confirmAction("Install missing coins now?") {
			printInfo("Operation cancelled")
			return nil
		}

		for _, coin := range uninstalled {
			printInfo(fmt.Sprintf("Installing %s...", strings.ToUpper(coin)))
			if err := installCoinNode(coin); err != nil {
				return fmt.Errorf("failed to install %s: %w", coin, err)
			}
			printSuccess(fmt.Sprintf("%s installation triggered", strings.ToUpper(coin)))
		}

		printInfo("Blockchain nodes are being installed and will begin syncing.")
		printInfo("Run 'spiralctl mining status' to check progress.")
		return nil
	}

	// Handle unsynced coins (STRICT)
	if len(unsynced) > 0 {
		printError(fmt.Sprintf("Cannot switch to multi-coin mode - blockchains not synced: %s", strings.Join(unsynced, ", ")))
		printInfo("Wait for all blockchains to sync before enabling multi-coin mode.")
		return fmt.Errorf("blockchains not synced")
	}

	// All coins are installed and synced
	printSuccess("All blockchains are synced")

	// Confirm with user
	if !autoYes {
		fmt.Println()
		fmt.Println("This will switch to multi-coin mining mode.")
		fmt.Println("Current configuration will be backed up.")
		fmt.Println("The stratum service will be restarted.")
		fmt.Println()
		if !confirmAction("Proceed with switch to multi-coin mode?") {
			printInfo("Operation cancelled")
			return nil
		}
	}

	// Load and update config
	cfg, err := loadExtendedConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Update for multi-coin mode
	cfg.Pool.Coin = "" // Clear solo coin
	cfg.Coins = make(map[string]interface{})
	for _, coin := range coins {
		cfg.Coins[coin] = map[string]interface{}{"enabled": true}
	}

	// Handle merge mining - check if any of the new coins can be a merge parent
	// and validate existing aux chains are compatible
	if cfg.MergeMining != nil && cfg.MergeMining.Enabled {
		// Find if any coin in the new set is a valid merge parent
		var newParent string
		var validMmDef mergeMiningDef
		for _, coin := range coins {
			for _, pair := range mergeMiningPairs {
				if pair.isValidParent(coin) {
					newParent = coin
					validMmDef = pair
					break
				}
			}
			if newParent != "" {
				break
			}
		}

		if newParent == "" {
			printInfo("Disabling merge mining (no merge-capable parent in coin set)")
			cfg.MergeMining.Enabled = false
		} else {
			// CRITICAL: Validate existing aux chains are compatible with new parent's algorithm
			// This prevents cross-algorithm contamination
			validAuxSet := make(map[string]bool)
			for _, aux := range validMmDef.auxChains {
				validAuxSet[strings.ToUpper(aux)] = true
			}

			var compatibleAux []AuxChainConfig
			var removedAux []string
			for _, auxCfg := range cfg.MergeMining.AuxChains {
				if validAuxSet[strings.ToUpper(auxCfg.Symbol)] {
					compatibleAux = append(compatibleAux, auxCfg)
				} else {
					removedAux = append(removedAux, auxCfg.Symbol)
				}
			}

			if len(removedAux) > 0 {
				printWarning(fmt.Sprintf("Removing incompatible aux chains: %s (wrong algorithm for %s)",
					strings.Join(removedAux, ", "), strings.ToUpper(newParent)))
				cfg.MergeMining.AuxChains = compatibleAux
			}

			// Disable merge mining if no compatible aux chains remain
			if len(compatibleAux) == 0 {
				printInfo("Disabling merge mining (no compatible aux chains)")
				cfg.MergeMining.Enabled = false
			}
		}
	}

	// Save config
	if err := saveExtendedConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("Configuration updated for multi-coin mode")

	// Restart service
	if isServiceRunning("spiralstratum") {
		printInfo("Restarting stratum service...")
		if err := restartService("spiralstratum"); err != nil {
			printWarning(fmt.Sprintf("Failed to restart service: %v", err))
		} else {
			printSuccess("Stratum service restarted")
		}
	}

	fmt.Println()
	printSuccess(fmt.Sprintf("Switched to multi-coin mode with: %s", strings.Join(coins, ", ")))

	return nil
}

// enableMergeMining enables merge mining with specified aux chains
// If auxCoins is empty, prompts user to select from available aux chains
func enableMergeMining(auxCoins []string, autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== ENABLE MERGE MINING (AUXPOW) ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadExtendedConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Determine parent coin
	parentCoin := cfg.Pool.Coin
	if parentCoin == "" {
		// Check multi-coin config for merge-capable parent
		for coin := range cfg.Coins {
			for _, pair := range mergeMiningPairs {
				if pair.isValidParent(strings.ToLower(coin)) {
					parentCoin = strings.ToLower(coin)
					break
				}
			}
			if parentCoin != "" {
				break
			}
		}
	}

	if parentCoin == "" {
		return fmt.Errorf("no merge mining parent configured. First configure BTC, DGB, or LTC as parent chain")
	}

	parentCoin = strings.ToLower(parentCoin)
	parentMeta, ok := coinRegistry[parentCoin]
	if !ok {
		return fmt.Errorf("unknown parent coin: %s", parentCoin)
	}

	// Find merge mining definition for this algorithm
	mmDef, ok := mergeMiningPairs[parentMeta.algorithm]
	if !ok {
		return fmt.Errorf("no merge mining available for algorithm: %s", parentMeta.algorithm)
	}

	if !mmDef.isValidParent(parentCoin) {
		return fmt.Errorf("merge mining requires one of %v as parent, but got %s", mmDef.parents, strings.ToUpper(parentCoin))
	}

	printInfo(fmt.Sprintf("Parent chain: %s (%s)", strings.ToUpper(parentCoin), parentMeta.name))
	printInfo(fmt.Sprintf("Algorithm: %s", parentMeta.algorithm))
	fmt.Println()

	// Show available aux chains for this algorithm
	fmt.Printf("%sAvailable auxiliary chains:%s\n", ColorBold, ColorReset)
	for _, aux := range mmDef.auxChains {
		auxMeta := coinRegistry[aux]
		fmt.Printf("  • %s (%s)\n", strings.ToUpper(aux), auxMeta.name)
	}
	fmt.Println()

	// If no aux coins specified, use default (first one) or prompt
	if len(auxCoins) == 0 {
		if autoYes {
			// Default to first aux chain when using --yes
			auxCoins = []string{mmDef.auxChains[0]}
			printInfo(fmt.Sprintf("Auto-selecting default aux chain: %s", strings.ToUpper(auxCoins[0])))
		} else {
			// Prompt user to select aux chains
			fmt.Printf("Enter aux chains to enable (comma-separated, e.g., %s): ", strings.Join(mmDef.auxChains, ","))
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))
			if input == "" {
				// Default to first aux chain
				auxCoins = []string{mmDef.auxChains[0]}
			} else {
				auxCoins = strings.Split(input, ",")
			}
		}
	}

	// Validate aux coins are valid for this algorithm (STRICT algorithm boundary check)
	validAuxSet := make(map[string]bool)
	for _, aux := range mmDef.auxChains {
		validAuxSet[aux] = true
	}

	for _, aux := range auxCoins {
		aux = strings.TrimSpace(aux)

		// First check: is this a known coin?
		auxMeta, exists := coinRegistry[aux]
		if !exists {
			return fmt.Errorf("unknown coin: %s", aux)
		}

		// CRITICAL: Algorithm boundary enforcement - prevent cross-algorithm contamination
		// SHA-256d parent (BTC) can ONLY merge-mine SHA-256d aux chains (NMC, SYS, XMY, FBTC)
		// Scrypt parent (LTC) can ONLY merge-mine Scrypt aux chains (DOGE, PEP)
		if auxMeta.algorithm != parentMeta.algorithm {
			return fmt.Errorf("ALGORITHM MISMATCH: cannot merge-mine %s (%s) with %s (%s) parent. "+
				"Merge mining requires matching algorithms",
				strings.ToUpper(aux), auxMeta.algorithm,
				strings.ToUpper(parentCoin), parentMeta.algorithm)
		}

		// Second check: is this aux chain in the allowed list for this parent?
		if !validAuxSet[aux] {
			return fmt.Errorf("invalid aux chain '%s' for %s merge mining. Valid options: %s",
				aux, strings.ToUpper(parentCoin), strings.Join(mmDef.auxChains, ", "))
		}
	}

	// Check parent chain sync
	parentSynced, parentProgress, err := isCoinSynced(parentCoin)
	if err != nil || !parentSynced {
		printError(fmt.Sprintf("Parent chain %s is not synced (%.2f%%)", strings.ToUpper(parentCoin), parentProgress))
		printInfo("Merge mining requires the parent chain to be fully synced first.")
		return fmt.Errorf("parent chain not synced")
	}
	printSuccess(fmt.Sprintf("Parent chain %s is synced (%.2f%%)", strings.ToUpper(parentCoin), parentProgress))
	fmt.Println()

	// Check each aux chain
	var readyAux []string
	var needInstall []string
	var needSync []string

	for _, auxCoin := range auxCoins {
		auxCoin = strings.TrimSpace(auxCoin)
		auxMeta := coinRegistry[auxCoin]

		fmt.Printf("%sChecking %s (%s)...%s\n", ColorCyan, strings.ToUpper(auxCoin), auxMeta.name, ColorReset)

		// Check if installed
		if !isCoinInstalled(auxCoin) {
			needInstall = append(needInstall, auxCoin)
			fmt.Printf("  %s✗ Not installed%s\n", ColorRed, ColorReset)
			if req, ok := syncRequirements[auxCoin]; ok {
				fmt.Printf("    Requires: ~%d GB disk, %s to sync\n", req.diskGB, req.syncDays)
			}
			continue
		}

		// Start service if not running
		if !isServiceRunning(auxMeta.service) {
			printInfo(fmt.Sprintf("  Starting %s service...", auxMeta.name))
			_ = startCoinService(auxCoin)
		}

		// Check sync status
		synced, progress, err := isCoinSynced(auxCoin)
		if err != nil {
			fmt.Printf("  %s⚠ Cannot check sync status%s\n", ColorYellow, ColorReset)
			needSync = append(needSync, auxCoin)
			continue
		}

		if !synced {
			needSync = append(needSync, auxCoin)
			fmt.Printf("  %s• Syncing: %.2f%%%s\n", ColorYellow, progress, ColorReset)
			if req, ok := syncRequirements[auxCoin]; ok {
				fmt.Printf("    Requires: ~%d GB disk, %s to sync\n", req.diskGB, req.syncDays)
			}
		} else {
			readyAux = append(readyAux, auxCoin)
			fmt.Printf("  %s✓ Synced: %.2f%%%s\n", ColorGreen, progress, ColorReset)
		}
	}

	fmt.Println()

	// Handle uninstalled aux chains
	if len(needInstall) > 0 {
		printWarning(fmt.Sprintf("The following aux chains need to be installed: %s", strings.Join(needInstall, ", ")))

		if !autoYes && !confirmAction("Install missing aux chains now?") {
			printInfo("Operation cancelled")
			return nil
		}

		for _, auxCoin := range needInstall {
			printInfo(fmt.Sprintf("Installing %s...", strings.ToUpper(auxCoin)))
			if err := installCoinNode(auxCoin); err != nil {
				printWarning(fmt.Sprintf("Failed to install %s: %v", auxCoin, err))
			} else {
				printSuccess(fmt.Sprintf("%s installation triggered", strings.ToUpper(auxCoin)))
			}
		}

		printInfo("Blockchains are being installed and will begin syncing.")
		printInfo("Run 'spiralctl mining merge enable' again when synced.")
		return nil
	}

	// Handle unsynced aux chains (STRICT)
	if len(needSync) > 0 {
		printError(fmt.Sprintf("Cannot enable merge mining - aux chains not synced: %s", strings.Join(needSync, ", ")))
		printInfo("Merge mining requires ALL aux chains to be fully synced.")
		printInfo("Run 'spiralctl mining status' to check progress.")
		return fmt.Errorf("auxiliary chains not synced")
	}

	// All aux chains are ready
	if len(readyAux) == 0 {
		return fmt.Errorf("no aux chains ready for merge mining")
	}

	printSuccess(fmt.Sprintf("Ready aux chains: %s", strings.Join(readyAux, ", ")))

	// Confirm with user
	if !autoYes {
		fmt.Println()
		fmt.Printf("This will enable merge mining: %s + %s\n", strings.ToUpper(parentCoin), strings.ToUpper(strings.Join(readyAux, ", ")))
		fmt.Println("The stratum service will be restarted.")
		fmt.Println()
		if !confirmAction("Enable merge mining?") {
			printInfo("Operation cancelled")
			return nil
		}
	}

	// Build merge mining config
	mmCfg := &MergeMiningConfig{
		Enabled:         true,
		RefreshInterval: "5s",
		AuxChains:       make([]AuxChainConfig, 0, len(readyAux)),
	}
	for _, auxCoin := range readyAux {
		auxMeta := coinRegistry[auxCoin]
		mmCfg.AuxChains = append(mmCfg.AuxChains, AuxChainConfig{
			Symbol:  strings.ToUpper(auxCoin),
			Enabled: true,
			Address: "CONFIGURE_YOUR_ADDRESS",
			Daemon: DaemonConfig{
				Host:     "127.0.0.1",
				Port:     auxMeta.rpcPort,
				User:     fmt.Sprintf("spiral%s", auxCoin),
				Password: "CONFIGURE_RPC_PASSWORD",
			},
		})
	}

	// Save config — V2-aware (places mergeMining inside coin section)
	if err := saveMergeMiningConfig(mmCfg, parentCoin); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("Configuration updated with merge mining enabled")
	printWarning("IMPORTANT: Update aux chain addresses and RPC passwords in config.yaml!")

	// Restart service
	if isServiceRunning("spiralstratum") {
		printInfo("Restarting stratum service...")
		if err := restartService("spiralstratum"); err != nil {
			printWarning(fmt.Sprintf("Failed to restart service: %v", err))
		} else {
			printSuccess("Stratum service restarted")
		}
	}

	fmt.Println()
	printSuccess(fmt.Sprintf("Merge mining enabled: %s + %s", strings.ToUpper(parentCoin), strings.ToUpper(strings.Join(readyAux, ", "))))

	return nil
}

// disableMergeMining disables merge mining
func disableMergeMining(autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== DISABLE MERGE MINING ===%s\n\n", ColorBold, ColorReset)

	// Read raw config to check current state
	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Quick check if merge mining is even present
	if !strings.Contains(string(data), "mergeMining:") {
		printInfo("Merge mining is not configured")
		return nil
	}

	// Load structured config to show status
	cfg, err := loadExtendedConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.MergeMining == nil || !cfg.MergeMining.Enabled {
		// Also check V2 coins for merge mining
		mmFound := false
		if cfg.Coins != nil {
			for _, coinData := range cfg.Coins {
				if coinMap, ok := coinData.(map[string]interface{}); ok {
					if mm, exists := coinMap["mergeMining"]; exists {
						if mmMap, ok := mm.(map[string]interface{}); ok {
							if enabled, ok := mmMap["enabled"].(bool); ok && enabled {
								mmFound = true
								break
							}
						}
					}
				}
			}
		}
		if !mmFound {
			printInfo("Merge mining is already disabled")
			return nil
		}
	}

	// Show current merge mining config
	if cfg.MergeMining != nil {
		for _, aux := range cfg.MergeMining.AuxChains {
			if aux.Enabled {
				printInfo(fmt.Sprintf("Currently merge mining: %s", aux.Symbol))
			}
		}
	}

	// Confirm with user
	if !autoYes {
		fmt.Println()
		fmt.Println("This will disable merge mining.")
		fmt.Println("Auxiliary chain blocks will no longer be mined.")
		fmt.Println()
		if !confirmAction("Disable merge mining?") {
			printInfo("Operation cancelled")
			return nil
		}
	}

	// Disable merge mining — V2-aware
	if err := disableMergeMiningConfig(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("Configuration updated - merge mining disabled")

	// Restart service
	if isServiceRunning("spiralstratum") {
		printInfo("Restarting stratum service...")
		if err := restartService("spiralstratum"); err != nil {
			printWarning(fmt.Sprintf("Failed to restart service: %v", err))
		} else {
			printSuccess("Stratum service restarted")
		}
	}

	fmt.Println()
	printSuccess("Merge mining disabled")

	return nil
}

// Helper functions

// isCoinInstalled checks if a coin's blockchain node is installed
func isCoinInstalled(coin string) bool {
	meta, ok := coinRegistry[coin]
	if !ok {
		return false
	}
	return fileExists(meta.config)
}

// isCoinSynced checks if a coin's blockchain is synced
// Returns (synced bool, progress float64, error)
func isCoinSynced(coin string) (bool, float64, error) {
	meta, ok := coinRegistry[coin]
	if !ok {
		return false, 0, fmt.Errorf("unknown coin: %s", coin)
	}

	// Check if service is running
	if !isServiceRunning(meta.service) {
		return false, 0, fmt.Errorf("service %s is not running", meta.service)
	}

	// Get blockchain info via RPC
	// #nosec G204 - cliCmd comes from trusted coinRegistry
	output, err := exec.Command(meta.cliCmd, "-conf="+meta.config, "getblockchaininfo").Output()
	if err != nil {
		return false, 0, fmt.Errorf("failed to get blockchain info: %w", err)
	}

	// Parse JSON response
	var info struct {
		Blocks               int     `json:"blocks"`
		Headers              int     `json:"headers"`
		InitialBlockDownload bool    `json:"initialblockdownload"`
		VerificationProgress float64 `json:"verificationprogress"`
	}

	if err := json.Unmarshal(output, &info); err != nil {
		return false, 0, fmt.Errorf("failed to parse blockchain info: %w", err)
	}

	// Calculate progress
	progress := float64(0)
	if info.Headers > 0 {
		progress = float64(info.Blocks) / float64(info.Headers) * 100
	}

	// Check if synced
	// Primary: initialblockdownload=false is authoritative
	// Secondary: verificationprogress >= 0.9999 or blocks/headers >= 99.9%
	synced := !info.InitialBlockDownload ||
		info.VerificationProgress >= 0.9999 ||
		progress >= 99.9

	return synced, progress, nil
}

// startCoinService starts a blockchain service
func startCoinService(coin string) error {
	meta, ok := coinRegistry[coin]
	if !ok {
		return fmt.Errorf("unknown coin: %s", coin)
	}

	// #nosec G204 - service name comes from trusted coinRegistry
	cmd := exec.Command("systemctl", "start", meta.service)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start %s: %w\n%s", meta.service, err, output)
	}

	return nil
}

// installCoinNode triggers installation of a blockchain node
func installCoinNode(coin string) error {
	// Use pool-mode.sh to install the coin
	script := "/spiralpool/scripts/pool-mode.sh"
	if !fileExists(script) {
		return fmt.Errorf("installation script not found: %s", script)
	}

	// #nosec G204 - coin comes from validated coinRegistry key
	cmd := exec.Command("bash", script, "--add", coin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// restartService restarts a systemd service
func restartService(service string) error {
	cmd := exec.Command("systemctl", "restart", service)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to restart %s: %w\n%s", service, err, output)
	}
	return nil
}

// loadExtendedConfig loads config with merge mining support
func loadExtendedConfig() (*ExtendedConfig, error) {
	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg ExtendedConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

// saveExtendedConfig saves config with merge mining support.
// Uses round-trip-safe approach to preserve unknown config sections
// (stratum, logging, rateLimiting, api, metrics, etc.) not modeled by ExtendedConfig.
func saveExtendedConfig(cfg *ExtendedConfig) error {
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning(fmt.Sprintf("Failed to backup config: %v", err))
	}

	// Read existing file to preserve unknown sections
	existing, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return fmt.Errorf("failed to read config file for round-trip: %w", err)
	}

	var fullCfg map[string]interface{}
	if err := yaml.Unmarshal(existing, &fullCfg); err != nil {
		return fmt.Errorf("failed to parse config file for round-trip: %w", err)
	}
	if fullCfg == nil {
		fullCfg = make(map[string]interface{})
	}

	// Merge only the sections ExtendedConfig manages
	fullCfg["version"] = cfg.Version
	mergeStructField(fullCfg, "global", cfg.Global)
	mergeStructField(fullCfg, "database", cfg.Database)
	mergeStructField(fullCfg, "vip", cfg.VIP)
	mergeStructField(fullCfg, "ha", cfg.HA)
	mergeStructField(fullCfg, "pool", cfg.Pool)
	if cfg.Coins != nil {
		fullCfg["coins"] = cfg.Coins
	}
	if cfg.MergeMining != nil {
		mergeStructField(fullCfg, "mergeMining", cfg.MergeMining)
	}
	if cfg.MultiPort != nil {
		mergeStructField(fullCfg, "multi_port", cfg.MultiPort)
	}

	data, err := yaml.Marshal(fullCfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// SECURITY: Config file contains credentials, use 0600
	if err := os.WriteFile(DefaultConfigFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// isV2Config checks whether the YAML document uses V2 format (has a "coins:" key)
func isV2Config(doc *yaml.Node) bool {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "coins" {
			return true
		}
	}
	return false
}

// findCoinNodeInV2 finds a coin's mapping node within the V2 coins array.
// It searches for a coin entry whose "symbol" value matches (case-insensitive).
func findCoinNodeInV2(doc *yaml.Node, coinSymbol string) *yaml.Node {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}

	// Find the "coins" key
	var coinsNode *yaml.Node
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "coins" {
			coinsNode = root.Content[i+1]
			break
		}
	}
	if coinsNode == nil || coinsNode.Kind != yaml.SequenceNode {
		return nil
	}

	target := strings.ToUpper(coinSymbol)
	for _, item := range coinsNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j < len(item.Content)-1; j += 2 {
			if item.Content[j].Value == "symbol" && strings.ToUpper(item.Content[j+1].Value) == target {
				return item
			}
		}
	}
	return nil
}

// removeMergeMiningFromNode removes the "mergeMining" key from a mapping node.
func removeMergeMiningFromNode(node *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if node.Content[i].Value == "mergeMining" {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// buildMergeMiningNode creates a yaml.Node tree for the mergeMining config.
func buildMergeMiningNode(mmCfg *MergeMiningConfig) (*yaml.Node, *yaml.Node) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "mergeMining"}

	// Marshal the config to YAML, then decode as Node to get proper structure
	data, err := yaml.Marshal(mmCfg)
	if err != nil {
		// Fallback: simple enabled: false
		valNode := &yaml.Node{Kind: yaml.MappingNode}
		return keyNode, valNode
	}

	var valNode yaml.Node
	if err := yaml.Unmarshal(data, &valNode); err != nil {
		valNode = yaml.Node{Kind: yaml.MappingNode}
		return keyNode, &valNode
	}

	// Unmarshal wraps in a document node
	if valNode.Kind == yaml.DocumentNode && len(valNode.Content) > 0 {
		return keyNode, valNode.Content[0]
	}
	return keyNode, &valNode
}

// saveMergeMiningConfig saves merge mining config with V1/V2 awareness.
// For V1 configs, writes mergeMining at root level.
// For V2 configs, writes mergeMining inside the parent coin's section.
func saveMergeMiningConfig(mmCfg *MergeMiningConfig, parentCoin string) error {
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning(fmt.Sprintf("Failed to backup config: %v", err))
	}

	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	mmKey, mmVal := buildMergeMiningNode(mmCfg)

	if isV2Config(&doc) {
		// V2: place mergeMining inside the parent coin section
		coinNode := findCoinNodeInV2(&doc, parentCoin)
		if coinNode == nil {
			return fmt.Errorf("parent coin %s not found in V2 config coins array", strings.ToUpper(parentCoin))
		}

		// Remove existing mergeMining from this coin node (if any)
		removeMergeMiningFromNode(coinNode)

		// Also remove any stale root-level mergeMining
		if doc.Content[0].Kind == yaml.MappingNode {
			removeMergeMiningFromNode(doc.Content[0])
		}

		// Append mergeMining to the coin node
		coinNode.Content = append(coinNode.Content, mmKey, mmVal)
	} else {
		// V1: place mergeMining at root level
		root := doc.Content[0]

		// Remove existing root-level mergeMining
		removeMergeMiningFromNode(root)

		// Append at root
		root.Content = append(root.Content, mmKey, mmVal)
	}

	// Encode back to YAML
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("failed to close encoder: %w", err)
	}

	// SECURITY: Config file contains credentials, use 0600
	if err := os.WriteFile(DefaultConfigFile, []byte(buf.String()), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// disableMergeMiningConfig disables merge mining in config with V1/V2 awareness.
func disableMergeMiningConfig() error {
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning(fmt.Sprintf("Failed to backup config: %v", err))
	}

	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	if isV2Config(&doc) {
		// V2: set enabled=false in each coin's mergeMining section
		root := doc.Content[0]
		var coinsNode *yaml.Node
		for i := 0; i < len(root.Content)-1; i += 2 {
			if root.Content[i].Value == "coins" {
				coinsNode = root.Content[i+1]
				break
			}
		}
		if coinsNode != nil && coinsNode.Kind == yaml.SequenceNode {
			for _, coin := range coinsNode.Content {
				if coin.Kind != yaml.MappingNode {
					continue
				}
				for j := 0; j < len(coin.Content)-1; j += 2 {
					if coin.Content[j].Value == "mergeMining" {
						mmNode := coin.Content[j+1]
						if mmNode.Kind == yaml.MappingNode {
							for k := 0; k < len(mmNode.Content)-1; k += 2 {
								if mmNode.Content[k].Value == "enabled" {
									mmNode.Content[k+1].Value = "false"
									mmNode.Content[k+1].Tag = "!!bool"
								}
							}
						}
					}
				}
			}
		}
		// Also remove any stale root-level mergeMining
		removeMergeMiningFromNode(root)
	} else {
		// V1: set enabled=false at root-level mergeMining
		root := doc.Content[0]
		for i := 0; i < len(root.Content)-1; i += 2 {
			if root.Content[i].Value == "mergeMining" {
				mmNode := root.Content[i+1]
				if mmNode.Kind == yaml.MappingNode {
					for k := 0; k < len(mmNode.Content)-1; k += 2 {
						if mmNode.Content[k].Value == "enabled" {
							mmNode.Content[k+1].Value = "false"
							mmNode.Content[k+1].Tag = "!!bool"
						}
					}
				}
			}
		}
	}

	// Encode back to YAML
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("failed to close encoder: %w", err)
	}

	// SECURITY: Config file contains credentials, use 0600
	if err := os.WriteFile(DefaultConfigFile, []byte(buf.String()), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// confirmActionMining prompts user for confirmation
// Respects globalYesFlag for automation
func confirmActionMining(prompt string) bool {
	if globalYesFlag {
		fmt.Printf("%s [y/N]: y (auto-confirmed with --yes)\n", prompt)
		return true
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// ── Multi-coin smart port commands ──────────────────────────────────────────

// parseCoinWeights parses "dgb:80,bch:15,btc:5" into a map
// parseCoinWeights parses a coin weight spec string.
// Supports two formats:
//   - Percentage: "dgb:80,btc:20" (must sum to 100)
//   - Hours:      "dgb:19.2h,btc:4.8h" (must sum to 24)
//
// If any value ends in 'h', all values are treated as hours and converted to weights.
func parseCoinWeights(spec string) (map[string]int, error) {
	pairs := strings.Split(spec, ",")
	type entry struct {
		coin string
		raw  string
	}
	var entries []entry
	hoursMode := false

	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid spec %q — expected coin:value (e.g. dgb:80 or dgb:19.2h)", pair)
		}
		coin := strings.TrimSpace(strings.ToLower(parts[0]))
		if _, ok := coinRegistry[coin]; !ok {
			return nil, fmt.Errorf("unknown coin: %s", coin)
		}
		meta := coinRegistry[coin]
		if meta.algorithm != "sha256d" {
			return nil, fmt.Errorf("%s is %s — only SHA-256d coins can participate in the smart port", strings.ToUpper(coin), meta.algorithm)
		}
		val := strings.TrimSpace(parts[1])
		if strings.HasSuffix(val, "h") || strings.HasSuffix(val, "H") {
			hoursMode = true
		}
		entries = append(entries, entry{coin: coin, raw: val})
	}

	if len(entries) < 2 {
		return nil, fmt.Errorf("at least 2 SHA-256d coins required for multi-port (got %d)", len(entries))
	}

	if hoursMode {
		// Parse as hours, convert to weights
		coinHours := make(map[string]float64)
		var totalH float64
		var coinOrder []string
		for _, e := range entries {
			val := strings.TrimSuffix(strings.TrimSuffix(e.raw, "h"), "H")
			var h float64
			if _, err := fmt.Sscanf(val, "%f", &h); err != nil || h < 0 {
				return nil, fmt.Errorf("invalid hours for %s: %s", strings.ToUpper(e.coin), e.raw)
			}
			coinHours[strings.ToUpper(e.coin)] = h
			coinOrder = append(coinOrder, strings.ToUpper(e.coin))
			totalH += h
		}
		totalH = math.Round(totalH*10) / 10
		if totalH != 24 {
			return nil, fmt.Errorf("hours must sum to 24, got %.1f", totalH)
		}
		// Convert to integer weights summing to 100
		weights := make(map[string]int)
		weightSum := 0
		for i, sym := range coinOrder {
			if i == len(coinOrder)-1 {
				weights[sym] = 100 - weightSum
			} else {
				w := int(math.Round(coinHours[sym] / 24.0 * 100))
				weights[sym] = w
				weightSum += w
			}
		}
		return weights, nil
	}

	// Parse as percentage weights
	weights := make(map[string]int)
	for _, e := range entries {
		var w int
		if _, err := fmt.Sscanf(e.raw, "%d", &w); err != nil || w < 0 {
			return nil, fmt.Errorf("invalid weight for %s: %s (must be a non-negative integer)", strings.ToUpper(e.coin), e.raw)
		}
		weights[strings.ToUpper(e.coin)] = w
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	if total != 100 {
		return nil, fmt.Errorf("weights must sum to 100, got %d", total)
	}
	return weights, nil
}

// multiportStatus shows current multi-port configuration
func multiportStatus() error {
	printBanner()
	fmt.Printf("%s=== MULTI-COIN SMART PORT STATUS ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadExtendedConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.MultiPort == nil || !cfg.MultiPort.Enabled {
		fmt.Printf("%-20s %sDisabled%s\n", "Status:", ColorRed, ColorReset)
		fmt.Println()
		fmt.Println("Enable with:")
		fmt.Printf("  spiralctl mining multiport enable dgb:80,bch:15,btc:5\n")
		return nil
	}

	fmt.Printf("%-20s %sEnabled%s\n", "Status:", ColorGreen, ColorReset)
	fmt.Printf("%-20s %d\n", "Port:", cfg.MultiPort.Port)
	if cfg.MultiPort.PreferCoin != "" {
		fmt.Printf("%-20s %s\n", "Default Coin:", strings.ToUpper(cfg.MultiPort.PreferCoin))
	}
	if cfg.MultiPort.CheckInterval != "" {
		fmt.Printf("%-20s %s\n", "Check Interval:", cfg.MultiPort.CheckInterval)
	}
	if cfg.MultiPort.MinTimeOnCoin != "" {
		fmt.Printf("%-20s %s\n", "Min Time On Coin:", cfg.MultiPort.MinTimeOnCoin)
	}

	fmt.Println()
	if len(cfg.MultiPort.Coins) > 0 {
		totalWeight := 0
		for _, rc := range cfg.MultiPort.Coins {
			totalWeight += rc.Weight
		}
		// Sort coins for deterministic display
		sortedCoins := make([]string, 0, len(cfg.MultiPort.Coins))
		for c := range cfg.MultiPort.Coins {
			sortedCoins = append(sortedCoins, c)
		}
		sort.Strings(sortedCoins)

		tz := cfg.MultiPort.Timezone
		if tz == "" {
			tz = readSentinelTimezone()
		}
		fmt.Printf("%-20s %s\n", "Timezone:", tz)
		fmt.Printf("%s24-Hour %s Schedule:%s\n", ColorBold, tz, ColorReset)
		cumulativeFrac := 0.0
		for _, coin := range sortedCoins {
			rc := cfg.MultiPort.Coins[coin]
			frac := float64(rc.Weight) / float64(totalWeight)
			startH := cumulativeFrac * 24
			cumulativeFrac += frac
			endH := cumulativeFrac * 24
			pct := float64(rc.Weight) * 100.0 / float64(totalWeight)
			fmt.Printf("  %-6s  %5.0f%%   %05.1fh – %05.1fh %s\n", strings.ToUpper(coin), pct, startH, endH, tz)
		}
	}

	// Try to get live stats from API
	fmt.Println()
	apiURL := fmt.Sprintf("http://127.0.0.1:%d/api/multiport", cfg.Global.APIPort)
	resp, err := httpGetJSON(apiURL)
	if err == nil && resp != nil {
		fmt.Printf("%sLive Stats:%s\n", ColorBold, ColorReset)
		if sessions, ok := resp["active_sessions"].(float64); ok {
			fmt.Printf("  Active Sessions:  %.0f\n", sessions)
		}
		if switches, ok := resp["total_switches"].(float64); ok {
			fmt.Printf("  Total Switches:   %.0f\n", switches)
		}
		if dist, ok := resp["coin_distribution"].(map[string]interface{}); ok {
			fmt.Printf("  Distribution:     ")
			first := true
			for coin, count := range dist {
				if !first {
					fmt.Printf(", ")
				}
				fmt.Printf("%s=%.0f", coin, count)
				first = false
			}
			fmt.Println()
		}
	}

	fmt.Println()
	return nil
}

// multiportWizard runs an interactive wizard to set up the multi-coin smart port.
// It discovers installed SHA-256d coins, lets the user toggle which ones participate,
// offers to install missing coins, and collects weights that must sum to 100.
func multiportWizard(autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== MULTI-COIN SMART PORT WIZARD ===%s\n\n", ColorBold, ColorReset)
	tz := readSentinelTimezone()
	fmt.Println("This wizard will set up the multi-coin smart port on port 16180.")
	fmt.Println("Miners connect once and the pool rotates them between SHA-256d coins")
	fmt.Printf("on a 24-hour %s schedule based on the hours you assign.\n", tz)
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// 1. Discover available SHA-256d coins
	sha256dCoins := []string{"btc", "bch", "dgb", "bc2", "qbx"}
	type coinState struct {
		symbol    string
		name      string
		installed bool
		selected  bool
	}

	var coins []coinState
	for _, sym := range sha256dCoins {
		meta := coinRegistry[sym]
		installed := isCoinInstalled(sym)
		coins = append(coins, coinState{
			symbol:    sym,
			name:      meta.name,
			installed: installed,
			selected:  installed, // pre-select installed coins
		})
	}

	// Count installed
	installedCount := 0
	for _, c := range coins {
		if c.installed {
			installedCount++
		}
	}

	// 2. Show coin selection
	fmt.Printf("%sAvailable SHA-256d Coins:%s\n\n", ColorBold, ColorReset)
	for i, c := range coins {
		status := fmt.Sprintf("%sInstalled%s", ColorGreen, ColorReset)
		if !c.installed {
			status = fmt.Sprintf("%sNot installed%s", ColorYellow, ColorReset)
		}
		sel := "[ ]"
		if c.selected {
			sel = fmt.Sprintf("%s[✓]%s", ColorGreen, ColorReset)
		}
		fmt.Printf("  %s %d) %-20s %s\n", sel, i+1, fmt.Sprintf("%s (%s)", c.name, strings.ToUpper(c.symbol)), status)
	}
	fmt.Println()
	fmt.Println("Toggle coins with their number, 'd' when done.")
	fmt.Println()

	for {
		fmt.Printf("Toggle (1-%d), 'd'=done: ", len(coins))
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "d" || input == "D" {
			// Count selected
			selectedCount := 0
			for _, c := range coins {
				if c.selected {
					selectedCount++
				}
			}
			if selectedCount < 2 {
				fmt.Printf("  %sSelect at least 2 coins.%s\n", ColorRed, ColorReset)
				continue
			}
			break
		}

		var idx int
		if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 1 || idx > len(coins) {
			fmt.Printf("  %sInvalid choice.%s\n", ColorRed, ColorReset)
			continue
		}
		idx-- // 0-based
		coins[idx].selected = !coins[idx].selected

		// Re-display
		fmt.Printf("\033[%dA\033[J", len(coins)+3) // clear previous display
		for i, c := range coins {
			status := fmt.Sprintf("%sInstalled%s", ColorGreen, ColorReset)
			if !c.installed {
				status = fmt.Sprintf("%sNot installed%s", ColorYellow, ColorReset)
			}
			sel := "[ ]"
			if c.selected {
				sel = fmt.Sprintf("%s[✓]%s", ColorGreen, ColorReset)
			}
			fmt.Printf("  %s %d) %-20s %s\n", sel, i+1, fmt.Sprintf("%s (%s)", c.name, strings.ToUpper(c.symbol)), status)
		}
		fmt.Println()
		fmt.Println("Toggle coins with their number, 'd' when done.")
		fmt.Println()
	}

	// 3. Check for uninstalled selected coins — offer to install
	var selected []coinState
	for _, c := range coins {
		if c.selected {
			selected = append(selected, c)
		}
	}

	for i, c := range selected {
		if c.installed {
			continue
		}
		fmt.Println()
		fmt.Printf("  %s%s (%s) is not installed.%s\n", ColorYellow, c.name, strings.ToUpper(c.symbol), ColorReset)
		fmt.Printf("  Install it now? [y/N]: ")
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))

		if resp == "y" || resp == "yes" {
			printInfo(fmt.Sprintf("Installing %s node...", c.name))
			if err := installCoinNode(c.symbol); err != nil {
				printWarning(fmt.Sprintf("Failed to install %s: %v", c.name, err))
				fmt.Printf("  Remove %s from the smart port? [Y/n]: ", strings.ToUpper(c.symbol))
				resp2, _ := reader.ReadString('\n')
				resp2 = strings.TrimSpace(strings.ToLower(resp2))
				if resp2 != "n" && resp2 != "no" {
					selected[i].selected = false
				}
			} else {
				printSuccess(fmt.Sprintf("%s node installation started", c.name))
				selected[i].installed = true
			}
		} else {
			fmt.Printf("  Remove %s from the smart port? [Y/n]: ", strings.ToUpper(c.symbol))
			resp2, _ := reader.ReadString('\n')
			resp2 = strings.TrimSpace(strings.ToLower(resp2))
			if resp2 != "n" && resp2 != "no" {
				selected[i].selected = false
			}
		}
	}

	// Rebuild selected list after possible removals
	var finalCoins []string
	for _, c := range selected {
		if c.selected {
			finalCoins = append(finalCoins, c.symbol)
		}
	}
	if len(finalCoins) < 2 {
		return fmt.Errorf("need at least 2 coins — only %d remaining after setup", len(finalCoins))
	}

	// 4. Collect hours — must sum to 24
	fmt.Println()
	fmt.Printf("%sAssign hours per day (must sum to 24):%s\n\n", ColorBold, ColorReset)

	coinHours := make(map[string]float64)
	remainingHours := 24.0
	for i, sym := range finalCoins {
		isLast := i == len(finalCoins)-1
		meta := coinRegistry[sym]

		if isLast {
			// Last coin gets the remainder
			fmt.Printf("  %s (%s): %s%.1fh%s (remaining)\n", meta.name, strings.ToUpper(sym), ColorCyan, remainingHours, ColorReset)
			coinHours[strings.ToUpper(sym)] = remainingHours
			break
		}

		defaultH := remainingHours / float64(len(finalCoins)-i)
		fmt.Printf("  %s (%s) — %.1fh remaining [default: %.1f]: ", meta.name, strings.ToUpper(sym), remainingHours, defaultH)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		h := defaultH
		if input != "" {
			var parsed float64
			if _, err := fmt.Sscanf(input, "%f", &parsed); err != nil || parsed < 0 {
				fmt.Printf("  %sInvalid, using %.1f%s\n", ColorYellow, defaultH, ColorReset)
			} else {
				h = parsed
			}
		}
		if h > remainingHours {
			fmt.Printf("  %sCapped to %.1fh (remaining)%s\n", ColorYellow, remainingHours, ColorReset)
			h = remainingHours
		}

		coinHours[strings.ToUpper(sym)] = h
		remainingHours -= h
	}

	// Convert hours to integer percentage weights (sum=100)
	weights := make(map[string]int)
	weightSum := 0
	coinList := make([]string, 0, len(coinHours))
	for sym := range coinHours {
		coinList = append(coinList, sym)
	}
	// Sort for deterministic ordering
	sort.Strings(coinList)
	for i, sym := range coinList {
		if i == len(coinList)-1 {
			weights[sym] = 100 - weightSum
		} else {
			w := int(math.Round(coinHours[sym] / 24.0 * 100))
			weights[sym] = w
			weightSum += w
		}
	}

	// Verify sum=100
	total := 0
	for _, w := range weights {
		total += w
	}
	if total != 100 {
		return fmt.Errorf("internal error: weights sum to %d, not 100", total)
	}

	// 5. Show schedule and confirm
	fmt.Println()
	printSchedule(weights)

	if !autoYes && !confirmActionMining("Enable multi-coin smart port with this schedule?") {
		fmt.Println("Cancelled.")
		return nil
	}

	// 6. Apply — reuse the same config writing logic as multiportEnable
	return applyMultiPortConfig(weights)
}

// printSchedule displays the 24h UTC time allocation for a set of weights
func printSchedule(weights map[string]int) {
	tz := readSentinelTimezone()
	fmt.Printf("Port: %s16180%s\n", ColorCyan, ColorReset)
	fmt.Printf("Schedule (24h %s cycle):\n", tz)

	// Sort coins for deterministic output
	coins := make([]string, 0, len(weights))
	for c := range weights {
		coins = append(coins, c)
	}
	sort.Strings(coins)

	cumulativeFrac := 0.0
	for _, coin := range coins {
		w := weights[coin]
		frac := float64(w) / 100.0
		startH := cumulativeFrac * 24
		cumulativeFrac += frac
		endH := cumulativeFrac * 24
		fmt.Printf("  %-6s  %3d%%   %05.1fh – %05.1fh %s\n", coin, w, startH, endH, tz)
	}
	fmt.Println()
}

// applyMultiPortConfig writes the multi_port section to config.yaml, updates coins.env, and restarts stratum
func applyMultiPortConfig(weights map[string]int) error {
	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	mpNode := buildMultiPortNode(weights)

	root := doc.Content[0]
	found := false
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "multi_port" {
			root.Content[i+1] = mpNode
			found = true
			break
		}
	}
	if !found {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "multi_port"},
			mpNode,
		)
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	_ = enc.Close()

	if err := os.WriteFile(DefaultConfigFile, []byte(buf.String()), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	updateCoinsEnvMultiPort(weights)

	printSuccess("Multi-coin smart port enabled on port 16180")
	printInfo("Restarting stratum service...")

	if err := restartService("spiralstratum"); err != nil {
		printWarning(fmt.Sprintf("Failed to restart stratum: %v", err))
		printInfo("Restart manually: sudo systemctl restart spiralstratum")
	} else {
		printSuccess("Stratum service restarted")
	}

	return nil
}

// multiportEnable enables the multi-port with specified coin weights (non-interactive)
func multiportEnable(spec string, autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== ENABLE MULTI-COIN SMART PORT ===%s\n\n", ColorBold, ColorReset)

	weights, err := parseCoinWeights(spec)
	if err != nil {
		return err
	}

	printSchedule(weights)

	if !autoYes && !confirmActionMining("Enable multi-coin smart port? This will restart the stratum service") {
		fmt.Println("Cancelled.")
		return nil
	}

	return applyMultiPortConfig(weights)
}

// multiportDisable disables the multi-port
func multiportDisable(autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== DISABLE MULTI-COIN SMART PORT ===%s\n\n", ColorBold, ColorReset)

	if !autoYes && !confirmActionMining("Disable multi-coin smart port?") {
		fmt.Println("Cancelled.")
		return nil
	}

	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Find multi_port and set enabled=false
	root := doc.Content[0]
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "multi_port" {
			mp := root.Content[i+1]
			for j := 0; j < len(mp.Content)-1; j += 2 {
				if mp.Content[j].Value == "enabled" {
					mp.Content[j+1].Value = "false"
					mp.Content[j+1].Tag = "!!bool"
					break
				}
			}
			break
		}
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	_ = enc.Close()

	if err := os.WriteFile(DefaultConfigFile, []byte(buf.String()), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Update coins.env
	updateCoinsEnvLine("MULTIPORT_ENABLED", "false")

	printSuccess("Multi-coin smart port disabled")
	printInfo("Restarting stratum service...")

	if err := restartService("spiralstratum"); err != nil {
		printWarning(fmt.Sprintf("Failed to restart stratum: %v", err))
	} else {
		printSuccess("Stratum service restarted")
	}

	return nil
}

// multiportWeights updates the coin weight allocation (non-interactive)
func multiportWeights(spec string, autoYes bool) error {
	printBanner()
	fmt.Printf("%s=== UPDATE SMART PORT WEIGHTS ===%s\n\n", ColorBold, ColorReset)

	weights, err := parseCoinWeights(spec)
	if err != nil {
		return err
	}

	fmt.Printf("New ")
	printSchedule(weights)

	if !autoYes && !confirmActionMining("Update weights? This will restart the stratum service") {
		fmt.Println("Cancelled.")
		return nil
	}

	return applyMultiPortConfig(weights)
}

// buildMultiPortNode creates the YAML node for multi_port config
func buildMultiPortNode(weights map[string]int) *yaml.Node {
	mp := &yaml.Node{Kind: yaml.MappingNode}

	// enabled: true
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "enabled"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "true", Tag: "!!bool"},
	)
	// port: 16180
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "port"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "16180", Tag: "!!int"},
	)
	// coins
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "coins"},
		buildCoinsNode(weights),
	)
	// check_interval: 30s
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "check_interval"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "30s"},
	)
	// prefer_coin: highest-weight coin (deterministic)
	preferCoin := ""
	maxW := 0
	for coin, w := range weights {
		uc := strings.ToUpper(coin)
		if w > maxW || (w == maxW && (preferCoin == "" || uc < preferCoin)) {
			maxW = w
			preferCoin = uc
		}
	}
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "prefer_coin"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: preferCoin},
	)
	// min_time_on_coin: 60s
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "min_time_on_coin"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "60s"},
	)
	// timezone: from sentinel config (user's display_timezone)
	tz := readSentinelTimezone()
	mp.Content = append(mp.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "timezone"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: tz},
	)

	return mp
}

// readSentinelTimezone reads the display_timezone from sentinel config.
// Falls back to "UTC" if unreadable.
func readSentinelTimezone() string {
	paths := []string{
		"/spiralpool/config/sentinel/config.json",
		os.ExpandEnv("$HOME/.spiralsentinel/config.json"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if tz, ok := cfg["display_timezone"].(string); ok && tz != "" {
			return tz
		}
	}
	return "UTC"
}

// buildCoinsNode creates the YAML mapping for coins with weights
func buildCoinsNode(weights map[string]int) *yaml.Node {
	// Sort for deterministic YAML output
	sorted := make([]string, 0, len(weights))
	for c := range weights {
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)

	coins := &yaml.Node{Kind: yaml.MappingNode}
	for _, coin := range sorted {
		w := weights[coin]
		coinNode := &yaml.Node{Kind: yaml.MappingNode}
		coinNode.Content = append(coinNode.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "weight"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", w), Tag: "!!int"},
		)
		coins.Content = append(coins.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: strings.ToUpper(coin)},
			coinNode,
		)
	}
	return coins
}

// updateCoinsEnvMultiPort updates coins.env with multi-port settings
func updateCoinsEnvMultiPort(weights map[string]int) {
	// Sort for deterministic output (coins and weights must align positionally)
	sorted := make([]string, 0, len(weights))
	for c := range weights {
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)

	var coinStrs, weightStrs []string
	preferCoin := ""
	maxW := 0
	for _, coin := range sorted {
		w := weights[coin]
		coinStrs = append(coinStrs, strings.ToUpper(coin))
		weightStrs = append(weightStrs, fmt.Sprintf("%d", w))
		uc := strings.ToUpper(coin)
		if w > maxW || (w == maxW && (preferCoin == "" || uc < preferCoin)) {
			maxW = w
			preferCoin = uc
		}
	}

	updateCoinsEnvLine("MULTIPORT_ENABLED", "true")
	updateCoinsEnvLine("MULTIPORT_COINS", strings.Join(coinStrs, ","))
	updateCoinsEnvLine("MULTIPORT_WEIGHTS", strings.Join(weightStrs, ","))
	updateCoinsEnvLine("MULTIPORT_PREFER_COIN", preferCoin)
}

// updateCoinsEnvLine updates or appends a key=value in coins.env
func updateCoinsEnvLine(key, value string) {
	coinsEnv := "/spiralpool/config/coins.env"
	data, err := os.ReadFile(coinsEnv)
	if err != nil {
		return // coins.env may not exist in all setups
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}

	_ = os.WriteFile(coinsEnv, []byte(strings.Join(lines, "\n")), 0644)
}

// httpGetJSON fetches JSON from a URL and returns the decoded map
func httpGetJSON(url string) (map[string]interface{}, error) {
	cmd := exec.Command("curl", "-sf", "--max-time", "3", url)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	return result, nil
}
