// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runVIP(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	fs := flag.NewFlagSet("vip", flag.ExitOnError)
	address := fs.String("address", "", "Virtual IP address (e.g., 192.168.1.200)")
	iface := fs.String("interface", "", "Network interface (e.g., ens33)")
	netmask := fs.Int("netmask", 32, "CIDR netmask")
	priority := fs.Int("priority", 0, "Node priority (0=auto, lower = higher priority)")
	autoPriority := fs.Bool("auto-priority", true, "Auto-assign priority based on join order")
	token := fs.String("token", "", "Cluster token (auto-generated if not provided)")
	canBeMaster := fs.Bool("master", true, "Allow this node to become master")
	statusPort := fs.Int("status-port", 5354, "HTTP status API port")
	discoveryPort := fs.Int("discovery-port", 5363, "UDP discovery port")

	if len(args) < 1 {
		printVIPUsage()
		return nil
	}

	action := args[0]
	if len(args) > 1 {
		_ = fs.Parse(args[1:]) // #nosec G104
	}

	switch action {
	case "enable":
		return enableVIP(*address, *iface, *netmask, *priority, *autoPriority, *token, *canBeMaster, *statusPort, *discoveryPort)
	case "disable":
		return disableVIP()
	case "status":
		return vipStatus()
	case "join":
		return joinCluster(*token, *priority)
	case "failover":
		return forceVIPFailover()
	case "rotate-token":
		return rotateClusterToken()
	default:
		printVIPUsage()
		return fmt.Errorf("unknown action: %s", action)
	}
}

func printVIPUsage() {
	fmt.Println("Usage: spiralctl vip <action> [options]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  enable       Enable VIP (Virtual IP) for miner failover")
	fmt.Println("  disable      Disable VIP")
	fmt.Println("  status       Show VIP cluster status")
	fmt.Println("  join         Join an existing VIP cluster")
	fmt.Println("  failover     Force VIP failover (trigger election)")
	fmt.Println("  rotate-token Rotate the cluster token (rolling update)")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --address <ip>       Virtual IP address (required for enable)")
	fmt.Println("  --interface <name>   Network interface (auto-detected if not provided)")
	fmt.Println("  --netmask <cidr>     CIDR netmask (default: 32)")
	fmt.Println("  --priority <num>     Node priority, lower = higher priority (default: 100)")
	fmt.Println("  --token <token>      Cluster token (auto-generated for master)")
	fmt.Println("  --master             Allow this node to become master (default: true)")
	fmt.Println("  --status-port <port> HTTP status port (default: 5354)")
	fmt.Println("  --discovery-port     UDP discovery port (default: 5363)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Enable as first node (master)")
	fmt.Println("  spiralctl vip enable --address 192.168.1.200 --interface ens33")
	fmt.Println()
	fmt.Println("  # Join existing cluster as backup")
	fmt.Println("  spiralctl vip join --token <cluster-token> --priority 200")
	fmt.Println()
	fmt.Println("  # Check cluster status")
	fmt.Println("  spiralctl vip status")
	fmt.Println()
	fmt.Println("  # Force failover (trigger election)")
	fmt.Println("  spiralctl vip failover")
	fmt.Println()
	fmt.Println("  # Rotate cluster token (security maintenance)")
	fmt.Println("  spiralctl vip rotate-token")
	fmt.Println()
	fmt.Printf("%sNote:%s All nodes in the VIP cluster must use the same VIP address\n", ColorYellow, ColorReset)
	fmt.Println("and cluster token. Miners connect to the VIP, and the master node")
	fmt.Println("holds the VIP. On master failure, a backup automatically takes over.")
	fmt.Println()
}

func enableVIP(address, iface string, netmask, priority int, autoPriority bool, token string, canBeMaster bool, statusPort, discoveryPort int) error {
	printBanner()
	fmt.Printf("%s=== ENABLE VIRTUAL IP (VIP) ===%s\n\n", ColorBold, ColorReset)

	if address == "" {
		return fmt.Errorf("--address is required")
	}

	// Validate IP address
	if net.ParseIP(address) == nil {
		return fmt.Errorf("invalid IP address: %s", address)
	}

	// Auto-detect interface if not provided
	if iface == "" {
		detected := detectInterface()
		if detected == "" {
			return fmt.Errorf("could not auto-detect interface. Please specify with --interface")
		}
		iface = detected
		printInfo(fmt.Sprintf("Auto-detected interface: %s", iface))
	}

	// Validate interface exists
	if !interfaceExists(iface) {
		return fmt.Errorf("interface %s does not exist", iface)
	}

	// Generate token if not provided
	if token == "" {
		generated, err := generateClusterToken()
		if err != nil {
			return fmt.Errorf("failed to generate cluster token: %w", err)
		}
		token = generated
		fmt.Println()
		fmt.Printf("%s+---------------------------------------------------------------+%s\n", ColorYellow, ColorReset)
		fmt.Printf("%s|                    CLUSTER TOKEN GENERATED                    |%s\n", ColorYellow, ColorReset)
		fmt.Printf("%s+---------------------------------------------------------------+%s\n", ColorYellow, ColorReset)
		fmt.Println()
		fmt.Printf("  > Token: %s%s%s\n", ColorGreen, token, ColorReset)
		fmt.Println()
		fmt.Printf("  %s! SAVE THIS TOKEN!%s You'll need it to add backup nodes.\n", ColorRed, ColorReset)
		fmt.Println()
	}

	// Display configuration
	fmt.Println("VIP Configuration:")
	fmt.Printf("  Address:        %s/%d\n", address, netmask)
	fmt.Printf("  Interface:      %s\n", iface)
	if autoPriority && priority == 0 {
		fmt.Printf("  Priority:       auto (100 for master, 101+ for backups)\n")
	} else {
		fmt.Printf("  Priority:       %d\n", priority)
	}
	fmt.Printf("  Auto-Priority:  %v\n", autoPriority)
	fmt.Printf("  Can Be Master:  %v\n", canBeMaster)
	fmt.Printf("  Status Port:    %d\n", statusPort)
	fmt.Printf("  Discovery Port: %d\n", discoveryPort)
	fmt.Println()

	if !confirmAction("Enable VIP with these settings?") {
		printInfo("Operation cancelled")
		return nil
	}

	// Load and update config
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate and set priority
	effectivePriority := priority
	if autoPriority && priority == 0 {
		// Auto-priority: 100 for first node (master)
		effectivePriority = 100
	} else if priority < 100 {
		// Enforce minimum priority of 100 to prevent abuse
		printWarning("Priority cannot be below 100 (reserved for master). Setting to 100.")
		effectivePriority = 100
	} else if priority > 999 {
		// Cap at 999 to prevent unreasonable values
		printWarning("Priority capped at 999.")
		effectivePriority = 999
	}

	cfg.VIP.Enabled = true
	cfg.VIP.Address = address
	cfg.VIP.Interface = iface
	cfg.VIP.Netmask = netmask
	cfg.VIP.Priority = effectivePriority
	cfg.VIP.AutoPriority = autoPriority
	cfg.VIP.ClusterToken = token
	cfg.VIP.CanBecomeMaster = canBeMaster
	cfg.VIP.StatusPort = statusPort
	cfg.VIP.DiscoveryPort = discoveryPort
	cfg.VIP.HeartbeatInterval = "30s"
	cfg.VIP.FailoverTimeout = "90s"

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("VIP enabled successfully")
	printInfo("Restart spiralstratum service for changes to take effect")
	fmt.Println()

	// Generate the virtual MAC address for display
	vipMAC := generateVIPMAC(address)

	// Network configuration reminder
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println("|                     NETWORK CONFIGURATION                     |")
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println()
	fmt.Println("  VIP uses a dedicated virtual MAC address for DHCP reservation.")
	fmt.Println()
	fmt.Printf("    > VIP Address: %s%s%s\n", ColorGreen, address, ColorReset)
	fmt.Printf("    > VIP MAC:     %s%s%s\n", ColorCyan, vipMAC, ColorReset)
	fmt.Println()
	fmt.Printf("  %s⚠ ROUTER DHCP RESERVATION:%s\n", ColorYellow, ColorReset)
	fmt.Println("    Add a DHCP reservation in your router with:")
	fmt.Printf("      > MAC Address: %s\n", vipMAC)
	fmt.Printf("      > IP Address:  %s\n", address)
	fmt.Println()
	fmt.Println("  This MAC is auto-generated from the VIP and will be the same")
	fmt.Println("  on all cluster nodes, ensuring seamless failover.")
	fmt.Println()

	// Firewall reminder
	fmt.Println("Firewall Configuration:")
	fmt.Printf("  Ensure ports %d (UDP) and %d (TCP) are open between cluster nodes.\n", discoveryPort, statusPort)
	fmt.Println()

	return nil
}

func disableVIP() error {
	printBanner()
	fmt.Printf("%s=== DISABLE VIP ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !cfg.VIP.Enabled {
		printInfo("VIP is already disabled")
		return nil
	}

	cfg.VIP.Enabled = false

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Release VIP if we hold it
	if cfg.VIP.Address != "" && cfg.VIP.Interface != "" {
		vipCIDR := fmt.Sprintf("%s/%d", cfg.VIP.Address, cfg.VIP.Netmask)
		_ = exec.Command(findIPBin(), "addr", "del", vipCIDR, "dev", cfg.VIP.Interface).Run() // #nosec G104
		printInfo("VIP released from interface")
	}

	printSuccess("VIP disabled")
	printInfo("Restart spiralstratum service for changes to take effect")

	return nil
}

func vipStatus() error {
	printBanner()
	fmt.Printf("%s=== VIP CLUSTER STATUS ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		printWarning(fmt.Sprintf("Could not load config: %v", err))
		return nil
	}

	if !cfg.VIP.Enabled {
		fmt.Printf("VIP Status:  %sDISABLED%s\n", ColorYellow, ColorReset)
		fmt.Println()
		fmt.Println("To enable VIP, run:")
		fmt.Println("  spiralctl vip enable --address <ip> --interface ens33")
		return nil
	}

	vipMAC := generateVIPMAC(cfg.VIP.Address)

	fmt.Printf("VIP Status:  %sENABLED%s\n", ColorGreen, ColorReset)
	fmt.Printf("VIP Address: %s/%d\n", cfg.VIP.Address, cfg.VIP.Netmask)
	fmt.Printf("VIP MAC:     %s%s%s (for DHCP reservation)\n", ColorCyan, vipMAC, ColorReset)
	fmt.Printf("Interface:   %s\n", cfg.VIP.Interface)
	fmt.Printf("Priority:    %d\n", cfg.VIP.Priority)
	fmt.Println()

	// Check if VIP is on this interface
	hasVIP := checkVIPOnInterface(cfg.VIP.Address, cfg.VIP.Interface)
	if hasVIP {
		fmt.Printf("VIP Held:    %sYES (This node is MASTER)%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("VIP Held:    %sNO%s\n", ColorYellow, ColorReset)
	}
	fmt.Println()

	// Try to get cluster status from API
	statusPort := cfg.VIP.StatusPort
	if statusPort == 0 {
		statusPort = 5354
	}

	clusterStatus := getVIPClusterStatus(statusPort)
	if clusterStatus != nil && clusterStatus.Enabled {
		fmt.Println("Cluster Status:")
		fmt.Printf("  State:      %s\n", clusterStatus.State)
		fmt.Printf("  Local Role: %s%s%s\n", ColorCyan, clusterStatus.LocalRole, ColorReset)
		if clusterStatus.MasterID != "" {
			fmt.Printf("  Master ID:  %s\n", clusterStatus.MasterID)
		}
		fmt.Println()

		if len(clusterStatus.Nodes) > 0 {
			fmt.Println("Cluster Nodes:")
			for _, node := range clusterStatus.Nodes {
				roleColor := ColorYellow
				if node.Role == "MASTER" {
					roleColor = ColorGreen
				}
				fmt.Printf("  - %s: %s%s%s\n", node.ID, roleColor, node.Role, ColorReset)
			}
		}
	} else {
		printWarning("Could not fetch cluster status from API")
	}

	fmt.Println()
	return nil
}

func joinCluster(token string, priority int) error {
	printBanner()
	fmt.Printf("%s=== JOIN VIP CLUSTER ===%s\n\n", ColorBold, ColorReset)

	if token == "" {
		return fmt.Errorf("--token is required. Get the token from the master node")
	}

	// Load existing config
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Check if VIP is already configured
	if cfg.VIP.Address == "" {
		return fmt.Errorf("VIP address not configured. Run 'spiralctl vip enable' first or configure the VIP address")
	}

	// Enforce minimum priority of 100 (same as enableVIP)
	if priority < 100 {
		priority = 100
	}
	if priority > 999 {
		priority = 999
	}

	cfg.VIP.Enabled = true
	cfg.VIP.ClusterToken = token
	cfg.VIP.Priority = priority
	cfg.VIP.CanBecomeMaster = true

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess(fmt.Sprintf("Configured to join cluster with priority %d", priority))
	printInfo("Restart spiralstratum service to join the cluster")

	return nil
}

// Helper functions

func detectInterface() string {
	output, err := exec.Command(findIPBin(), "route", "get", "8.8.8.8").Output()
	if err != nil {
		return ""
	}

	// Parse output like: 8.8.8.8 via 192.168.1.1 dev eth0 src 192.168.1.100
	parts := strings.Fields(string(output))
	for i, part := range parts {
		if part == "dev" && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return ""
}

func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// findIPBin resolves the absolute path to the 'ip' command.
// Falls back to common locations if PATH doesn't include /usr/sbin.
func findIPBin() string {
	if p, err := exec.LookPath("ip"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/ip", "/sbin/ip", "/usr/bin/ip"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "ip" // last resort: let exec.Command try PATH at runtime
}

func generateClusterToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "spiral-" + hex.EncodeToString(bytes), nil
}

// generateVIPMAC generates a deterministic MAC address from the VIP.
// Uses locally administered unicast format (02:xx:xx:xx:xx:xx).
// The last 4 bytes are derived from the VIP so all nodes generate the same MAC.
func generateVIPMAC(vipAddress string) string {
	ip := net.ParseIP(vipAddress)
	if ip == nil {
		return "02:53:00:00:00:00"
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "02:53:00:00:00:00"
	}

	// Locally administered unicast: bit 1 of first byte = 1, bit 0 = 0
	// Format: 02:53:AA:BB:CC:DD where AA.BB.CC.DD is the VIP
	// "53" = 0x53 (S for Spiral)
	return fmt.Sprintf("02:53:%02x:%02x:%02x:%02x", ip4[0], ip4[1], ip4[2], ip4[3])
}

func checkVIPOnInterface(vip, iface string) bool {
	intf, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}

	addrs, err := intf.Addrs()
	if err != nil {
		return false
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if ipnet.IP.String() == vip {
				return true
			}
		}
	}

	return false
}

func forceVIPFailover() error {
	printBanner()
	fmt.Printf("%s=== FORCE VIP FAILOVER ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !cfg.VIP.Enabled {
		return fmt.Errorf("VIP is not enabled. Enable it first with: spiralctl vip enable")
	}

	if cfg.VIP.ClusterToken == "" {
		return fmt.Errorf("no cluster token configured")
	}

	statusPort := cfg.VIP.StatusPort
	if statusPort == 0 {
		statusPort = 5354
	}

	printWarning("This will trigger a VIP election.")
	printWarning("The node with the best priority that is fully synced will become master.")
	fmt.Println()

	if !confirmAction("Trigger VIP failover election?") {
		printInfo("Failover cancelled")
		return nil
	}

	// Create HTTP request with authentication
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/failover", statusPort), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.VIP.ClusterToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to VIP manager: %w. Is spiralstratum running?", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(limitedReader(resp.Body))

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed - cluster token mismatch")
	}

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("VIP manager is not running. Is spiralstratum service started?")
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failover failed: %s", errResp.Error)
		}
		return fmt.Errorf("failover failed with status %d", resp.StatusCode)
	}

	var result struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		printSuccess(fmt.Sprintf("Failover %s: %s", result.Status, result.Message))
	} else {
		printSuccess("Failover initiated")
	}

	fmt.Println()
	printInfo("Run 'spiralctl vip status' to check the election result")

	return nil
}

// rotateClusterToken performs a graceful cluster token rotation.
// This is a multi-step process that requires updating all nodes.
func rotateClusterToken() error {
	printBanner()
	fmt.Printf("%s=== ROTATE CLUSTER TOKEN ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !cfg.VIP.Enabled {
		return fmt.Errorf("VIP is not enabled. Enable it first with: spiralctl vip enable")
	}

	oldToken := cfg.VIP.ClusterToken
	if oldToken == "" {
		return fmt.Errorf("no cluster token configured")
	}

	// Get cluster status to show current nodes
	statusPort := cfg.VIP.StatusPort
	if statusPort == 0 {
		statusPort = 5354
	}

	clusterStatus := getVIPClusterStatus(statusPort)
	nodeCount := 1
	if clusterStatus != nil && len(clusterStatus.Nodes) > 0 {
		nodeCount = len(clusterStatus.Nodes)
	}

	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println("|                    CLUSTER TOKEN ROTATION                     |")
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println("| Token rotation is a security maintenance operation.           |")
	fmt.Println("|                                                               |")
	fmt.Println("| This process requires a ROLLING UPDATE of all nodes:          |")
	fmt.Println("|   1. Generate new token on this node                          |")
	fmt.Println("|   2. Update config and restart this node                      |")
	fmt.Println("|   3. Update each backup node with new token                   |")
	fmt.Println("|   4. Restart each backup node                                 |")
	fmt.Println("|                                                               |")
	fmt.Printf("| %s! WARNING:%s During rotation, nodes with old token will be      |\n", ColorRed, ColorReset)
	fmt.Println("|   rejected from the cluster until updated.                    |")
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println()

	if len(oldToken) >= 16 {
		fmt.Printf("Current Token: %s...%s\n", oldToken[:12], oldToken[len(oldToken)-4:])
	} else {
		fmt.Printf("Current Token: %s\n", oldToken)
	}
	fmt.Printf("Cluster Nodes: %d\n", nodeCount)
	fmt.Println()

	printWarning("This will generate a NEW cluster token.")
	printWarning("You must update ALL backup nodes with the new token.")
	fmt.Println()

	if !confirmAction("Generate new cluster token?") {
		printInfo("Token rotation cancelled")
		return nil
	}

	// Generate new token
	newToken, err := generateClusterToken()
	if err != nil {
		return fmt.Errorf("failed to generate new token: %w", err)
	}

	// Update config with new token
	cfg.VIP.ClusterToken = newToken

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println("|                      NEW TOKEN GENERATED                      |")
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println()
	fmt.Printf("  > New Token: %s%s%s\n", ColorGreen, newToken, ColorReset)
	fmt.Println()
	fmt.Printf("  > Old Token: %s%s%s (no longer valid after restart)\n", ColorRed, oldToken, ColorReset)
	fmt.Println()

	printSuccess("New token saved to config.yaml")
	fmt.Println()

	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println("|                     NEXT STEPS (REQUIRED)                     |")
	fmt.Println("+---------------------------------------------------------------+")
	fmt.Println()
	fmt.Println("  Complete the rolling update in this order:")
	fmt.Println()
	fmt.Println("  1. Restart this node:")
	fmt.Println("     systemctl restart spiralstratum")
	fmt.Println()
	fmt.Println("  2. On EACH backup node, run:")
	fmt.Printf("     spiralctl vip join --token %s\n", newToken)
	fmt.Println("     systemctl restart spiralstratum")
	fmt.Println()
	fmt.Println("  3. Verify cluster status:")
	fmt.Println("     spiralctl vip status")
	fmt.Println()

	if nodeCount > 1 {
		fmt.Printf("%sIMPORTANT:%s You have %d nodes in the cluster.\n", ColorYellow, ColorReset, nodeCount)
		fmt.Println("Update backup nodes BEFORE restarting this node to minimize downtime.")
		fmt.Println()
	}

	// Offer to save token to a file for easy distribution
	fmt.Println("Would you like to save the new token to a file for easy distribution?")
	if confirmAction("Save token to /run/spiralpool/spiral-new-token.txt?") {
		// SECURITY: Use /run/spiralpool (mode 700) instead of world-writable /tmp
		_ = os.MkdirAll("/run/spiralpool", 0700)
		tokenFile := "/run/spiralpool/spiral-new-token.txt"
		tokenContent := fmt.Sprintf("# Spiral Pool HA Cluster - New Token\n# Generated: %s\n# \n# Run on each backup node:\n#   spiralctl vip join --token %s\n#   systemctl restart spiralstratum\n#\nTOKEN=%s\n",
			time.Now().Format(time.RFC3339),
			newToken,
			newToken,
		)
		if err := writeTokenFile(tokenFile, tokenContent); err != nil {
			printWarning(fmt.Sprintf("Could not write token file: %v", err))
		} else {
			printSuccess(fmt.Sprintf("Token saved to %s", tokenFile))
			printWarning("Delete this file after updating all nodes!")
		}
	}

	return nil
}

// writeTokenFile writes the token to a file with restricted permissions.
func writeTokenFile(path, content string) error {
	// Write file with owner read/write only (0600)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}
