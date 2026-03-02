// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"flag"
	"fmt"
	"os/exec"
	"strings"
)

func runCoin(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	fs := flag.NewFlagSet("coin", flag.ExitOnError)

	if len(args) < 1 {
		printCoinUsage()
		return nil
	}

	action := args[0]
	if len(args) > 1 {
		_ = fs.Parse(args[1:]) // #nosec G104
	}

	switch action {
	case "list":
		return listCoins()
	case "disable":
		if len(args) < 2 {
			return fmt.Errorf("coin symbol required")
		}
		return disableCoin(args[1])
	case "status":
		return coinStatus()
	default:
		printCoinUsage()
		return fmt.Errorf("unknown action: %s", action)
	}
}

func printCoinUsage() {
	fmt.Println("Usage: spiralctl coin <action> [coin]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  list       Show available coins and their status")
	fmt.Println("  disable    Disable a coin from multi-coin mode")
	fmt.Println("  status     Show blockchain sync status for all coins")
	fmt.Println()
	fmt.Println("For switching mining modes, use 'spiralctl mining':")
	fmt.Println("  spiralctl mining solo <coin>       Switch to solo mining")
	fmt.Println("  spiralctl mining multi <coins>     Switch to multi-coin mining")
	fmt.Println("  spiralctl mining merge enable      Enable merge mining")
	fmt.Println()
	fmt.Println("Supported Coins (SHA256d):")
	fmt.Println("  btc, bch, dgb, bc2, nmc, xmy, fbtc  (sys = merge-mining only w/ BTC)")
	fmt.Println()
	fmt.Println("Supported Coins (Scrypt):")
	fmt.Println("  ltc, doge, dgb-scrypt, pep, cat")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  spiralctl coin list")
	fmt.Println("  spiralctl coin status")
	fmt.Println("  spiralctl coin disable bch")
	fmt.Println()
}

func listCoins() error {
	printBanner()
	fmt.Printf("%s=== AVAILABLE COINS ===%s\n\n", ColorBold, ColorReset)

	coins := []struct {
		symbol      string
		name        string
		algorithm   string
		configPath  string
		serviceName string
	}{
		// Alphabetically ordered (no coin preference)
		{"BC2", "Bitcoin II", "SHA-256d", DefaultBC2Config, "bitcoiniid"},
		{"BCH", "Bitcoin Cash", "SHA-256d", DefaultBCHConfig, "bitcoind-bch"},
		{"BTC", "Bitcoin", "SHA-256d", DefaultBTCConfig, "bitcoind"},
		{"CAT", "Catcoin", "Scrypt", DefaultCATConfig, "catcoind"},
		{"DGB", "DigiByte", "SHA-256d", DefaultDGBConfig, "digibyted"},
		{"DGB-SCRYPT", "DigiByte-Scrypt", "Scrypt", DefaultDGBScryptConfig, "digibyted-scrypt"},
		{"DOGE", "Dogecoin", "Scrypt", DefaultDOGEConfig, "dogecoind"},
		{"FBTC", "Fractal Bitcoin", "SHA-256d", DefaultFBTCConfig, "fractald"},
		{"LTC", "Litecoin", "Scrypt", DefaultLTCConfig, "litecoind"},
		{"NMC", "Namecoin", "SHA-256d", DefaultNMCConfig, "namecoind"},
		{"PEP", "PepeCoin", "Scrypt", DefaultPEPConfig, "pepecoind"},
		{"SYS", "Syscoin (merge-mine only)", "SHA-256d", DefaultSYSConfig, "syscoind"},
		{"XMY", "Myriad", "SHA-256d", DefaultXMYConfig, "myriadcoind"},
	}

	cfg, _ := loadConfig()

	fmt.Printf("%-8s %-14s %-12s %-12s %-10s\n", "Symbol", "Name", "Algorithm", "Installed", "Status")
	fmt.Println(strings.Repeat("-", 60))

	for _, coin := range coins {
		installed := "No"
		status := "-"

		if fileExists(coin.configPath) {
			installed = "Yes"
			if isServiceRunning(coin.serviceName) {
				status = fmt.Sprintf("%sRunning%s", ColorGreen, ColorReset)
			} else {
				status = fmt.Sprintf("%sStopped%s", ColorRed, ColorReset)
			}
		}

		// Check if enabled in config
		enabledMarker := ""
		if cfg != nil && cfg.Coins != nil {
			if _, ok := cfg.Coins[strings.ToLower(coin.symbol)]; ok {
				enabledMarker = " *"
			}
		}

		fmt.Printf("%-8s %-14s %-12s %-12s %s%s\n",
			coin.symbol+enabledMarker, coin.name, coin.algorithm, installed, status, ColorReset)
	}

	fmt.Println()
	fmt.Println("* = Currently enabled in pool configuration")
	fmt.Println()

	return nil
}

func disableCoin(symbol string) error {
	printBanner()
	fmt.Printf("%s=== DISABLE COIN ===%s\n\n", ColorBold, ColorReset)

	symbol = strings.ToLower(symbol)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Coins == nil {
		printInfo("No coins are currently enabled in multi-coin mode")
		return nil
	}

	if _, ok := cfg.Coins[symbol]; !ok {
		printInfo(fmt.Sprintf("%s is not currently enabled", strings.ToUpper(symbol)))
		return nil
	}

	delete(cfg.Coins, symbol)

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess(fmt.Sprintf("Disabled %s", strings.ToUpper(symbol)))
	printInfo("Restart spiralstratum service for changes to take effect")

	return nil
}

func coinStatus() error {
	printBanner()
	fmt.Printf("%s=== BLOCKCHAIN SYNC STATUS ===%s\n\n", ColorBold, ColorReset)

	coins := []struct {
		symbol  string
		name    string
		config  string
		service string
		rpcPort int
		cliCmd  string
	}{
		// SHA-256d coins
		{"BTC", "Bitcoin Knots", DefaultBTCConfig, "bitcoind", 8332, "bitcoin-cli"},
		{"BCH", "Bitcoin Cash", DefaultBCHConfig, "bitcoind-bch", 8432, "bitcoin-cli-bch"},
		{"DGB", "DigiByte", DefaultDGBConfig, "digibyted", 14022, "digibyte-cli"},
		{"BC2", "Bitcoin II", DefaultBC2Config, "bitcoiniid", 8339, "bitcoinii-cli"},
		{"NMC", "Namecoin", DefaultNMCConfig, "namecoind", 8336, "namecoin-cli"},
		{"SYS", "Syscoin", DefaultSYSConfig, "syscoind", 8370, "syscoin-cli"},
		{"XMY", "Myriad", DefaultXMYConfig, "myriadcoind", 10889, "myriadcoin-cli"},
		{"FBTC", "Fractal Bitcoin", DefaultFBTCConfig, "fractald", 8340, "fractal-cli"},
		// Scrypt coins
		{"LTC", "Litecoin", DefaultLTCConfig, "litecoind", 9332, "litecoin-cli"},
		{"DOGE", "Dogecoin", DefaultDOGEConfig, "dogecoind", 22555, "dogecoin-cli"},
		{"DGB-SCRYPT", "DigiByte-Scrypt", DefaultDGBScryptConfig, "digibyted-scrypt", 14022, "digibyte-cli"},
		{"PEP", "PepeCoin", DefaultPEPConfig, "pepecoind", 33873, "pepecoin-cli"},
		{"CAT", "Catcoin", DefaultCATConfig, "catcoind", 9932, "catcoin-cli"},
	}

	for _, coin := range coins {
		if !fileExists(coin.config) {
			continue
		}

		fmt.Printf("%s[%s - %s]%s\n", ColorCyan, coin.symbol, coin.name, ColorReset)

		if !isServiceRunning(coin.service) {
			fmt.Printf("  Status: %sService not running%s\n", ColorRed, ColorReset)
			fmt.Println()
			continue
		}

		// Get blockchain info via RPC
		info := getBlockchainInfo(coin.cliCmd, coin.config)
		if info != nil {
			fmt.Printf("  Status:         %sRunning%s\n", ColorGreen, ColorReset)
			fmt.Printf("  Chain:          %s\n", info.chain)
			fmt.Printf("  Blocks:         %d\n", info.blocks)
			fmt.Printf("  Headers:        %d\n", info.headers)

			if info.headers > 0 {
				progress := float64(info.blocks) / float64(info.headers) * 100
				if progress >= 99.9 {
					fmt.Printf("  Sync Progress:  %s%.2f%% (SYNCED)%s\n", ColorGreen, progress, ColorReset)
				} else {
					fmt.Printf("  Sync Progress:  %s%.2f%%%s\n", ColorYellow, progress, ColorReset)
				}
			}

			if info.connections > 0 {
				fmt.Printf("  Connections:    %d\n", info.connections)
			}
		} else {
			fmt.Printf("  Status:         %sCould not get info%s\n", ColorYellow, ColorReset)
		}

		fmt.Println()
	}

	return nil
}

type blockchainInfo struct {
	chain       string
	blocks      int
	headers     int
	connections int
}

func getBlockchainInfo(cliCmd, configPath string) *blockchainInfo {
	// Try to get blockchain info
	// G204: cliCmd is validated against known CLI tools (bitcoin-cli, digibyte-cli)
	output, err := exec.Command(cliCmd, "-conf="+configPath, "getblockchaininfo").Output() // #nosec G204
	if err != nil {
		return nil
	}

	// Parse JSON output (simple parsing)
	info := &blockchainInfo{}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"chain\":") {
			info.chain = extractJSONString(line)
		} else if strings.HasPrefix(line, "\"blocks\":") {
			_, _ = fmt.Sscanf(line, "\"blocks\": %d", &info.blocks) // #nosec G104
		} else if strings.HasPrefix(line, "\"headers\":") {
			_, _ = fmt.Sscanf(line, "\"headers\": %d", &info.headers) // #nosec G104
		}
	}

	// Get network info for connections
	// G204: cliCmd is validated against known CLI tools (bitcoin-cli, digibyte-cli)
	netOutput, _ := exec.Command(cliCmd, "-conf="+configPath, "getnetworkinfo").Output() // #nosec G204
	for _, line := range strings.Split(string(netOutput), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"connections\":") {
			_, _ = fmt.Sscanf(line, "\"connections\": %d", &info.connections) // #nosec G104
		}
	}

	return info
}

func extractJSONString(line string) string {
	// Extract value from "key": "value",
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	value := strings.TrimSpace(parts[1])
	value = strings.Trim(value, "\",")
	return value
}
