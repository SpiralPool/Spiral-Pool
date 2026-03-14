// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runStatus(args []string) error {
	printBanner()
	fmt.Printf("%s=== SPIRAL POOL STATUS ===%s\n\n", ColorBold, ColorReset)

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		printWarning(fmt.Sprintf("Could not load config: %v", err))
		cfg = &Config{} // Use empty config
	}

	// Pool Status
	printPoolStatus(cfg)

	// Node Status
	printNodeStatus()

	// HA Status
	printHAStatus(cfg)

	// VIP Status
	printVIPStatus(cfg)

	// Tor Status
	printTorStatus()

	return nil
}

func printPoolStatus(cfg *Config) {
	fmt.Printf("%s[Pool]%s\n", ColorCyan, ColorReset)

	// Check if pool service is running
	poolRunning := isServiceRunning("spiralstratum")

	if poolRunning {
		fmt.Printf("  Status:        %sRUNNING%s\n", ColorGreen, ColorReset)

		// Try to get API health
		if cfg.Global.APIEnabled {
			apiPort := cfg.Global.APIPort
			if apiPort == 0 {
				apiPort = 4000
			}
			health := getAPIHealth(apiPort)
			if health != "" {
				fmt.Printf("  API Health:    %s%s%s\n", ColorGreen, health, ColorReset)
			}
		}
	} else {
		fmt.Printf("  Status:        %sSTOPPED%s\n", ColorRed, ColorReset)
	}

	if cfg.Pool.Coin != "" {
		fmt.Printf("  Active Coin:   %s\n", strings.ToUpper(cfg.Pool.Coin))
	}

	// Count enabled coins
	if len(cfg.Coins) > 0 {
		enabledCoins := []string{}
		for coin := range cfg.Coins {
			enabledCoins = append(enabledCoins, strings.ToUpper(coin))
		}
		fmt.Printf("  Coins:         %s\n", strings.Join(enabledCoins, ", "))
	}

	fmt.Println()
}

func printNodeStatus() {
	fmt.Printf("%s[Blockchain Nodes]%s\n", ColorCyan, ColorReset)

	nodes := []struct {
		name    string
		service string
		config  string
	}{
		// Alphabetically ordered (no coin preference)
		{"Bitcoin II", "bitcoiniid", DefaultBC2Config},
		{"Bitcoin Cash", "bitcoind-bch", DefaultBCHConfig},
		{"Bitcoin Knots", "bitcoind", DefaultBTCConfig},
		{"Catcoin", "catcoind", DefaultCATConfig},
		{"DigiByte", "digibyted", DefaultDGBConfig},
		{"DigiByte-Scrypt", "digibyted-scrypt", DefaultDGBScryptConfig},
		{"Dogecoin", "dogecoind", DefaultDOGEConfig},
		{"Fractal Bitcoin", "fractald", DefaultFBTCConfig},
		{"Litecoin", "litecoind", DefaultLTCConfig},
		{"Myriad", "myriadcoind", DefaultXMYConfig},
		{"Namecoin", "namecoind", DefaultNMCConfig},
		{"PepeCoin", "pepecoind", DefaultPEPConfig},
		{"Q-BitX", "qbitxd", DefaultQBXConfig},
		{"Syscoin", "syscoind", DefaultSYSConfig},
	}

	for _, node := range nodes {
		if !fileExists(node.config) {
			continue
		}

		status := "STOPPED"
		statusColor := ColorRed

		if isServiceRunning(node.service) {
			status = "RUNNING"
			statusColor = ColorGreen
		}

		// Check Tor status
		torStatus := ""
		if isTorEnabled(node.config) {
			torStatus = fmt.Sprintf(" %s[TOR]%s", ColorMagenta, ColorReset)
		}

		fmt.Printf("  %-14s %s%s%s%s\n", node.name+":", statusColor, status, ColorReset, torStatus)
	}

	fmt.Println()
}

func printHAStatus(cfg *Config) {
	fmt.Printf("%s[High Availability]%s\n", ColorCyan, ColorReset)

	if !cfg.HA.Enabled {
		fmt.Printf("  Status:        %sDISABLED%s\n", ColorYellow, ColorReset)
	} else {
		fmt.Printf("  Status:        %sENABLED%s\n", ColorGreen, ColorReset)
		fmt.Printf("  Primary DB:    %s\n", cfg.HA.PrimaryHost)
		fmt.Printf("  Replica DB:    %s\n", cfg.HA.ReplicaHost)
	}

	fmt.Println()
}

func printVIPStatus(cfg *Config) {
	fmt.Printf("%s[Virtual IP (VIP)]%s\n", ColorCyan, ColorReset)

	if !cfg.VIP.Enabled {
		fmt.Printf("  Status:        %sDISABLED%s\n", ColorYellow, ColorReset)
	} else {
		fmt.Printf("  Status:        %sENABLED%s\n", ColorGreen, ColorReset)
		fmt.Printf("  VIP Address:   %s\n", cfg.VIP.Address)
		fmt.Printf("  Interface:     %s\n", cfg.VIP.Interface)
		fmt.Printf("  Priority:      %d\n", cfg.VIP.Priority)

		// Try to get VIP cluster status
		statusPort := cfg.VIP.StatusPort
		if statusPort == 0 {
			statusPort = 5354
		}

		clusterStatus := getVIPClusterStatus(statusPort)
		if clusterStatus != nil {
			fmt.Printf("  Role:          %s%s%s\n", ColorGreen, clusterStatus.LocalRole, ColorReset)
			if clusterStatus.MasterID != "" {
				fmt.Printf("  Master:        %s\n", clusterStatus.MasterID)
			}
			fmt.Printf("  Cluster Nodes: %d\n", len(clusterStatus.Nodes))
		}
	}

	fmt.Println()
}

func printTorStatus() {
	fmt.Printf("%s[Tor Privacy]%s\n", ColorCyan, ColorReset)

	torRunning := isServiceRunning("tor")
	if torRunning {
		fmt.Printf("  Tor Service:   %sRUNNING%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("  Tor Service:   %sSTOPPED%s\n", ColorRed, ColorReset)
	}

	// Check each node's Tor status
	nodes := []struct {
		name   string
		config string
	}{
		// Alphabetically ordered (no coin preference)
		{"BC2", DefaultBC2Config},
		{"BCH", DefaultBCHConfig},
		{"BTC", DefaultBTCConfig},
		{"CAT", DefaultCATConfig},
		{"DGB", DefaultDGBConfig},
		{"DGB-SCRYPT", DefaultDGBScryptConfig},
		{"DOGE", DefaultDOGEConfig},
		{"FBTC", DefaultFBTCConfig},
		{"LTC", DefaultLTCConfig},
		{"NMC", DefaultNMCConfig},
		{"PEP", DefaultPEPConfig},
		{"QBX", DefaultQBXConfig},
		{"SYS", DefaultSYSConfig},
		{"XMY", DefaultXMYConfig},
	}

	for _, node := range nodes {
		if !fileExists(node.config) {
			continue
		}

		if isTorEnabled(node.config) {
			fmt.Printf("  %-14s %sENABLED%s\n", node.name+":", ColorGreen, ColorReset)
		} else {
			fmt.Printf("  %-14s %sCLEARNET%s\n", node.name+":", ColorYellow, ColorReset)
		}
	}

	fmt.Println()
}

// Helper functions

func isServiceRunning(service string) bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", service)
	return cmd.Run() == nil
}

func isTorEnabled(configPath string) bool {
	// G304: Path is derived from known daemon config locations, not untrusted input
	data, err := os.ReadFile(configPath) // #nosec G304
	if err != nil {
		return false
	}

	content := string(data)
	return strings.Contains(content, "proxy=127.0.0.1:9050")
}

func getAPIHealth(port int) string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return "OK"
	}
	return fmt.Sprintf("HTTP %d", resp.StatusCode)
}

type VIPClusterStatus struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	LocalRole string `json:"localRole"`
	MasterID  string `json:"masterId"`
	Nodes     []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"nodes"`
}

func getVIPClusterStatus(port int) *VIPClusterStatus {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/status", port))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var status VIPClusterStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil
	}

	return &status
}
