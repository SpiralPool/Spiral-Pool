// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package cmd implements the spiralctl command-line interface.
package cmd

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
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
// SHA-256d: BTC can merge-mine NMC, SYS, XMY, FBTC
// Scrypt:   LTC can merge-mine DOGE, PEP
type mergeMiningDef struct {
	parent    string
	auxChains []string // All supported aux chains for this algorithm
}

var mergeMiningPairs = map[string]mergeMiningDef{
	"sha256d": {
		parent:    "btc",
		auxChains: []string{"nmc", "sys", "xmy", "fbtc"},
	},
	"scrypt": {
		parent:    "ltc",
		auxChains: []string{"doge", "pep"},
	},
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
	fmt.Println()
	fmt.Printf("%sNotes:%s\n", ColorBold, ColorReset)
	fmt.Println("  • Multi-coin mode requires all coins to use the same algorithm")
	fmt.Println("  • Merge mining requires parent chain to be synced first")
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
			if pair.parent == coin {
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
				if pair.parent == coin {
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
				if pair.parent == strings.ToLower(coin) {
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
		return fmt.Errorf("no merge mining parent configured. First configure BTC or LTC as parent chain")
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

	if mmDef.parent != parentCoin {
		return fmt.Errorf("merge mining requires %s as parent, but got %s", strings.ToUpper(mmDef.parent), strings.ToUpper(parentCoin))
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

// saveExtendedConfig saves config with merge mining support
func saveExtendedConfig(cfg *ExtendedConfig) error {
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning(fmt.Sprintf("Failed to backup config: %v", err))
	}

	data, err := yaml.Marshal(cfg)
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
