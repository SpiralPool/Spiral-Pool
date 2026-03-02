// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package cmd implements the spiralctl command-line interface.
//
// LEGAL DISCLAIMER - TOR FUNCTIONALITY:
// The Tor functionality in this software is provided for legitimate privacy
// purposes only. Users are solely responsible for ensuring their use of Tor
// complies with all applicable local, state, national, and international laws
// and regulations. The authors and contributors of this software:
//   - Make no representations about the legality of Tor in any jurisdiction
//   - Accept no liability for any illegal use of this software
//   - Strongly advise users to consult local laws before enabling Tor
//   - Provide this feature AS-IS with no warranty of any kind
//
// By using the Tor features, you acknowledge that you have reviewed and
// understand the legal implications in your jurisdiction.
package cmd

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const torConfig = `# +-----------------------------------------------------------------------------+
# |               TOR CLIENT-ONLY CONFIGURATION FOR SPIRAL POOL                |
# +-----------------------------------------------------------------------------+
# | This configuration ensures Tor operates ONLY as a client:                  |
# |   > NO relay traffic is forwarded                                          |
# |   > NO exit traffic is handled                                             |
# |   > NO bridge functionality                                                |
# |   > ONLY outbound SOCKS5 proxy for blockchain nodes                        |
# +-----------------------------------------------------------------------------+
# | LEGAL: Ensure Tor usage complies with your local laws and regulations.     |
# | The software authors accept NO LIABILITY for misuse of this feature.       |
# +-----------------------------------------------------------------------------+

# SOCKS5 proxy for local applications only
SocksPort 9050
SocksPolicy accept 127.0.0.1
SocksPolicy reject *

# Disable ALL relay/exit/bridge functionality
ORPort 0
ExitRelay 0
BridgeRelay 0
PublishServerDescriptor 0

# Don't act as a directory server
DirPort 0

# Don't publish to the Tor network
AssumeReachable 0

# Logging
Log notice file /var/log/tor/notices.log

# Data directory
DataDirectory /var/lib/tor

# Disable unused features
DisableDebuggerAttachment 1
`

func runTor(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	fs := flag.NewFlagSet("tor", flag.ExitOnError)
	nodeFlag := fs.String("node", "", "Specific node (btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, ltc, doge, dgb-scrypt, pep, cat) or 'all'")
	allFlag := fs.Bool("all", false, "Apply to all nodes")

	if len(args) < 1 {
		printTorUsage()
		return nil
	}

	action := args[0]
	if len(args) > 1 {
		_ = fs.Parse(args[1:]) // #nosec G104
	}

	switch action {
	case "enable":
		return enableTor(*nodeFlag, *allFlag)
	case "disable":
		return disableTor(*nodeFlag, *allFlag)
	case "status":
		return torStatus()
	default:
		printTorUsage()
		return fmt.Errorf("unknown action: %s", action)
	}
}

func printTorUsage() {
	fmt.Println("Usage: spiralctl tor <action> [options]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  enable     Enable Tor routing for blockchain nodes")
	fmt.Println("  disable    Disable Tor routing (use clearnet)")
	fmt.Println("  status     Show Tor status for all nodes")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --node <coin>  Apply to specific node")
	fmt.Println("                 SHA-256d: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc")
	fmt.Println("                 Scrypt:   ltc, doge, dgb-scrypt, pep, cat")
	fmt.Println("  --all          Apply to all installed nodes")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  spiralctl tor enable --node btc")
	fmt.Println("  spiralctl tor enable --all")
	fmt.Println("  spiralctl tor disable --node dgb")
	fmt.Println("  spiralctl tor status")
	fmt.Println()
	fmt.Printf("%sWARNING:%s Enabling Tor will significantly slow down blockchain sync!\n", ColorYellow, ColorReset)
	fmt.Println("  Initial sync may take 1-2 weeks instead of 1-3 days.")
	fmt.Println("  However, your IP address will be hidden from peers.")
	fmt.Println()
	fmt.Printf("%sLEGAL DISCLAIMER:%s\n", ColorRed, ColorReset)
	fmt.Println("  Before enabling Tor, ensure compliance with all applicable laws and")
	fmt.Println("  regulations in your jurisdiction. The use of Tor may be restricted or")
	fmt.Println("  prohibited in some countries. The authors of this software accept NO")
	fmt.Println("  LIABILITY for any illegal use. By enabling Tor, you acknowledge that")
	fmt.Println("  you have reviewed and understand the legal implications.")
	fmt.Println()
}

func enableTor(node string, all bool) error {
	printBanner()
	fmt.Printf("%s=== ENABLE TOR PRIVACY ===%s\n\n", ColorBold, ColorReset)

	// Display legal disclaimer first
	fmt.Printf("%s┌─────────────────────────────────────────────────────────────────┐%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│                      LEGAL DISCLAIMER                          │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s├─────────────────────────────────────────────────────────────────┤%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│ Before enabling Tor, you MUST ensure compliance with all       │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│ applicable laws and regulations in your jurisdiction.          │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│                                                               │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│ The use of Tor may be restricted or prohibited in some         │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│ countries. The authors of this software accept NO LIABILITY    │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│ for any illegal use of this feature.                           │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│                                                               │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│ By proceeding, you acknowledge that:                           │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│   1. You have reviewed applicable laws in your jurisdiction    │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│   2. You accept full responsibility for your use of Tor        │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s│   3. You release the software authors from all liability       │%s\n", ColorRed, ColorReset)
	fmt.Printf("%s└─────────────────────────────────────────────────────────────────┘%s\n", ColorRed, ColorReset)
	fmt.Println()

	if !confirmAction("I acknowledge the legal disclaimer and accept responsibility") {
		printInfo("Operation cancelled - legal disclaimer not accepted")
		return nil
	}
	fmt.Println()

	// Install Tor if not present
	if err := installTor(); err != nil {
		return err
	}

	// Determine which nodes to configure
	nodes := getTargetNodes(node, all)
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes specified. Use --node <coin> or --all")
	}

	fmt.Printf("%sWARNING:%s Enabling Tor will significantly slow blockchain sync.\n", ColorYellow, ColorReset)
	fmt.Println("  - Initial sync: 1-2 weeks (instead of 1-3 days)")
	fmt.Println("  - Your IP will be hidden from peers")
	fmt.Println()

	if !confirmAction("Continue with Tor enablement?") {
		printInfo("Operation cancelled")
		return nil
	}

	// Enable Tor for each node
	for _, n := range nodes {
		if err := enableTorForNode(n); err != nil {
			printError(fmt.Sprintf("Failed to enable Tor for %s: %v", n.name, err))
		} else {
			printSuccess(fmt.Sprintf("Tor enabled for %s", n.name))
		}
	}

	return nil
}

func disableTor(node string, all bool) error {
	printBanner()
	fmt.Printf("%s=== DISABLE TOR (USE CLEARNET) ===%s\n\n", ColorBold, ColorReset)

	nodes := getTargetNodes(node, all)
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes specified. Use --node <coin> or --all")
	}

	for _, n := range nodes {
		if err := disableTorForNode(n); err != nil {
			printError(fmt.Sprintf("Failed to disable Tor for %s: %v", n.name, err))
		} else {
			printSuccess(fmt.Sprintf("Tor disabled for %s (now using clearnet)", n.name))
		}
	}

	return nil
}

func torStatus() error {
	printBanner()
	fmt.Printf("%s=== TOR STATUS ===%s\n\n", ColorBold, ColorReset)

	// Check Tor service
	torRunning := isServiceRunning("tor")
	if torRunning {
		fmt.Printf("Tor Service:  %sRUNNING%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("Tor Service:  %sSTOPPED%s\n", ColorRed, ColorReset)
	}
	fmt.Println()

	// Check each node
	fmt.Println("Node Status:")
	nodes := getAllNodes()
	for _, n := range nodes {
		if !fileExists(n.config) {
			continue
		}

		if isTorEnabled(n.config) {
			fmt.Printf("  %-18s %sTOR ENABLED%s\n", n.name+":", ColorGreen, ColorReset)
		} else {
			fmt.Printf("  %-18s %sCLEARNET%s\n", n.name+":", ColorYellow, ColorReset)
		}
	}
	fmt.Println()

	return nil
}

type nodeInfo struct {
	name    string
	symbol  string
	config  string
	service string
}

func getAllNodes() []nodeInfo {
	return []nodeInfo{
		// Alphabetically ordered (no coin preference)
		{"Bitcoin II", "bc2", DefaultBC2Config, "bitcoiniid"},
		{"Bitcoin Cash", "bch", DefaultBCHConfig, "bitcoind-bch"},
		{"Bitcoin Knots", "btc", DefaultBTCConfig, "bitcoind"},
		{"Catcoin", "cat", DefaultCATConfig, "catcoind"},
		{"DigiByte", "dgb", DefaultDGBConfig, "digibyted"},
		{"DigiByte-Scrypt", "dgb-scrypt", DefaultDGBScryptConfig, "digibyted-scrypt"},
		{"Dogecoin", "doge", DefaultDOGEConfig, "dogecoind"},
		{"Fractal Bitcoin", "fbtc", DefaultFBTCConfig, "fractald"},
		{"Litecoin", "ltc", DefaultLTCConfig, "litecoind"},
		{"Myriad", "xmy", DefaultXMYConfig, "myriadcoind"},
		{"Namecoin", "nmc", DefaultNMCConfig, "namecoind"},
		{"PepeCoin", "pep", DefaultPEPConfig, "pepecoind"},
		{"Syscoin", "sys", DefaultSYSConfig, "syscoind"},
	}
}

func getTargetNodes(node string, all bool) []nodeInfo {
	allNodes := getAllNodes()

	if all {
		// Return only installed nodes
		var installed []nodeInfo
		for _, n := range allNodes {
			if fileExists(n.config) {
				installed = append(installed, n)
			}
		}
		return installed
	}

	if node == "" {
		return nil
	}

	node = strings.ToLower(node)
	for _, n := range allNodes {
		if n.symbol == node {
			if fileExists(n.config) {
				return []nodeInfo{n}
			}
			printWarning(fmt.Sprintf("%s is not installed", n.name))
			return nil
		}
	}

	return nil
}

func installTor() error {
	if isCommandAvailable("tor") {
		printInfo("Tor is already installed")
		return nil
	}

	printInfo("Installing Tor...")

	// Try different package managers
	if isCommandAvailable("apt-get") {
		cmd := exec.Command("apt-get", "update", "-qq")
		_ = cmd.Run() // #nosec G104
		cmd = exec.Command("apt-get", "install", "-y", "tor")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install Tor: %w", err)
		}
	} else if isCommandAvailable("dnf") {
		cmd := exec.Command("dnf", "install", "-y", "tor")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install Tor: %w", err)
		}
	} else if isCommandAvailable("yum") {
		cmd := exec.Command("yum", "install", "-y", "tor")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to install Tor: %w", err)
		}
	} else {
		return fmt.Errorf("could not detect package manager. Please install Tor manually")
	}

	// Configure Tor as client-only
	// SECURITY: Use 0600 for config files (G306 fix)
	if err := os.WriteFile("/etc/tor/torrc", []byte(torConfig), 0600); err != nil {
		return fmt.Errorf("failed to write Tor config: %w", err)
	}

	// Enable and start Tor
	_ = exec.Command("systemctl", "enable", "tor").Run() // #nosec G104
	_ = exec.Command("systemctl", "restart", "tor").Run() // #nosec G104

	printSuccess("Tor installed and configured as client-only")
	return nil
}

func enableTorForNode(n nodeInfo) error {
	// Backup config
	if err := backupFile(n.config); err != nil {
		printWarning(fmt.Sprintf("Failed to backup: %v", err))
	}

	// Read current config
	data, err := os.ReadFile(n.config)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Remove existing Tor settings
	content = removeTorSettings(content)

	// Add Tor configuration
	torSettings := `
# === TOR NETWORK - PRIVACY MODE ===
# Route all connections through Tor for IP privacy
# Note: Initial sync will be SLOW (1-2 weeks) but privacy is maximized
# This node is a Tor CLIENT ONLY - no relay/exit traffic
# Added by spiralctl

# Use Tor SOCKS5 proxy for all outbound connections
proxy=127.0.0.1:9050

# Route .onion peer connections through Tor
onion=127.0.0.1:9050

# Allow both onion and clearnet peers (via Tor proxy)
onlynet=onion
onlynet=ipv4

# Do NOT listen for incoming connections (hidden node)
listen=0

# Do NOT try to detect external IP
discover=0
# === END TOR ===
`

	content += torSettings

	// Write updated config
	// SECURITY: Use 0600 for config files (G306 fix)
	if err := os.WriteFile(n.config, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Restart service if running
	if isServiceRunning(n.service) {
		printInfo(fmt.Sprintf("Restarting %s...", n.service))
		_ = exec.Command("systemctl", "restart", n.service).Run() // G104: error intentionally ignored
	}

	return nil
}

func disableTorForNode(n nodeInfo) error {
	// Backup config
	if err := backupFile(n.config); err != nil {
		printWarning(fmt.Sprintf("Failed to backup: %v", err))
	}

	// Read current config
	data, err := os.ReadFile(n.config)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	content := string(data)

	// Remove Tor settings
	content = removeTorSettings(content)

	// Ensure listen=1 is set
	if !strings.Contains(content, "listen=1") {
		content += "\n# Normal networking (Tor disabled)\nlisten=1\n"
	}

	// Write updated config
	// SECURITY: Use 0600 for config files (G306 fix)
	if err := os.WriteFile(n.config, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Restart service if running
	if isServiceRunning(n.service) {
		printInfo(fmt.Sprintf("Restarting %s...", n.service))
		_ = exec.Command("systemctl", "restart", n.service).Run() // G104: error intentionally ignored
	}

	return nil
}

func removeTorSettings(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inTorBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "# === TOR NETWORK") {
			inTorBlock = true
			continue
		}
		if strings.HasPrefix(trimmed, "# === END TOR") {
			inTorBlock = false
			continue
		}
		if inTorBlock {
			continue
		}

		// Remove individual Tor settings
		if strings.HasPrefix(trimmed, "proxy=127.0.0.1:9050") ||
			strings.HasPrefix(trimmed, "onion=127.0.0.1:9050") ||
			strings.HasPrefix(trimmed, "onlynet=onion") ||
			strings.HasPrefix(trimmed, "discover=0") {
			continue
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

func isCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func confirmAction(prompt string) bool {
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
