// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package cmd implements the spiralctl command-line interface.
package cmd

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// ExternalAccessConfig holds external access settings.
// This is stored separately from the main stratum config.
type ExternalAccessConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Mode     string `yaml:"mode"` // "port-forward" or "tunnel"

	// Port forward settings
	PortForward struct {
		PublicHost string `yaml:"publicHost"`
		PublicPort int    `yaml:"publicPort"`
		LocalPort  int    `yaml:"localPort"`
	} `yaml:"portForward"`

	// Cloudflare Tunnel settings
	Tunnel struct {
		Name            string `yaml:"name"`
		ConfigPath      string `yaml:"configPath"`
		CredentialsPath string `yaml:"credentialsPath"`
		Hostname        string `yaml:"hostname"`
		BinaryPath      string `yaml:"binaryPath"`
	} `yaml:"tunnel"`

	// Security hardening when external enabled
	Security struct {
		HardenOnEnable       bool   `yaml:"hardenOnEnable"`
		RequireTLS           bool   `yaml:"requireTLS"`
		MaxConnectionsPerIP  int    `yaml:"maxConnectionsPerIP"`
		SharesPerSecond      int    `yaml:"sharesPerSecond"`
		BanThreshold         int    `yaml:"banThreshold"`
		BanDuration          string `yaml:"banDuration"`
		OriginalMaxConnPerIP int    `yaml:"originalMaxConnPerIP,omitempty"`
		OriginalSharesPerSec int    `yaml:"originalSharesPerSec,omitempty"`
		OriginalBanThreshold int    `yaml:"originalBanThreshold,omitempty"`
		OriginalBanDuration  string `yaml:"originalBanDuration,omitempty"`
	} `yaml:"security"`
}

// RateLimitingConfig mirrors the stratum config structure for rate limiting
type RateLimitingConfig struct {
	MaxConnectionsPerIP  int    `yaml:"maxConnectionsPerIP"`
	MaxConnectionsPerMin int    `yaml:"maxConnectionsPerMin"`
	MaxSharesPerSecond   int    `yaml:"maxSharesPerSecond"`
	BanThreshold         int    `yaml:"banThreshold"`
	BanDuration          string `yaml:"banDuration"`
}

// tunnelNameRegex validates tunnel names (alphanumeric, hyphen, underscore only)
var tunnelNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// hostnameRegex validates hostnames
var hostnameRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

func runExternal(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	if len(args) < 1 {
		printExternalUsage()
		return nil
	}

	switch args[0] {
	case "setup":
		return externalSetup(args[1:])
	case "enable":
		return externalEnable(args[1:])
	case "disable":
		return externalDisable()
	case "status":
		return externalStatus()
	case "test":
		return externalTest(args[1:])
	case "help", "-h", "--help":
		printExternalUsage()
		return nil
	default:
		printExternalUsage()
		return fmt.Errorf("unknown action: %s", args[0])
	}
}

func printExternalUsage() {
	fmt.Println("Usage: spiralctl external <action> [options]")
	fmt.Println()
	fmt.Printf("%sActions:%s\n", ColorBold, ColorReset)
	fmt.Println("  setup      Interactive wizard to configure external access")
	fmt.Println("  enable     Enable external access using saved configuration")
	fmt.Println("  disable    Disable external access")
	fmt.Println("  status     Show current external access status")
	fmt.Println("  test       Test external connectivity")
	fmt.Println()
	fmt.Printf("%sSetup Options:%s\n", ColorBold, ColorReset)
	fmt.Println("  --mode <port-forward|tunnel>  Access mode")
	fmt.Println("  --port <number>               Stratum port to expose (default: 3333)")
	fmt.Println("  --hostname <domain>           Public hostname (DDNS or tunnel domain)")
	fmt.Println("  --tunnel-name <name>          Cloudflare tunnel name")
	fmt.Println()
	fmt.Printf("%sExamples:%s\n", ColorBold, ColorReset)
	fmt.Println("  spiralctl external setup")
	fmt.Println("  spiralctl external setup --mode tunnel")
	fmt.Println("  spiralctl external enable")
	fmt.Println("  spiralctl external disable")
	fmt.Println("  spiralctl external status")
	fmt.Println("  spiralctl external test")
	fmt.Println()
	fmt.Printf("%sNotes:%s\n", ColorBold, ColorReset)
	fmt.Println("  Port Forward Mode:")
	fmt.Println("    - Requires router/firewall port forwarding configuration")
	fmt.Println("    - You configure your router, spiralctl validates connectivity")
	fmt.Println()
	fmt.Println("  Tunnel Mode (Cloudflare):")
	fmt.Println("    - No router configuration needed")
	fmt.Println("    - Requires cloudflared binary and Cloudflare account")
	fmt.Println("    - Traffic routed via Cloudflare's network")
	fmt.Println()
	fmt.Printf("%sWARNING:%s Exposing your pool to the internet increases attack surface.\n", ColorYellow, ColorReset)
	fmt.Println("  Security hardening is applied automatically when enabled.")
	fmt.Println()
}

func externalSetup(args []string) error {
	// Set up signal handling for cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Handle interrupts gracefully
	go func() {
		<-sigChan
		fmt.Println()
		printWarning("Setup interrupted. No changes were saved.")
		os.Exit(1)
	}()

	printBanner()
	fmt.Printf("%s=== EXTERNAL ACCESS SETUP ===%s\n\n", ColorBold, ColorReset)

	fs := flag.NewFlagSet("external-setup", flag.ContinueOnError)
	modeFlag := fs.String("mode", "", "Access mode (port-forward or tunnel)")
	portFlag := fs.Int("port", 3333, "Stratum port to expose")
	hostnameFlag := fs.String("hostname", "", "Public hostname")
	tunnelNameFlag := fs.String("tunnel-name", "", "Cloudflare tunnel name")

	// SEC-4: Check and report flag parsing errors
	if len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return fmt.Errorf("invalid flags: %w", err)
		}
	}

	fmt.Println("This wizard will configure your pool to accept connections from")
	fmt.Println("external miners (e.g., hashrate rental services).")
	fmt.Println()
	fmt.Printf("%sWARNING:%s Exposing your pool to the internet increases attack surface.\n", ColorYellow, ColorReset)
	fmt.Println("Security hardening will be applied automatically.")
	fmt.Println()

	// Determine mode
	mode := *modeFlag
	if mode == "" {
		fmt.Println("Select access mode:")
		fmt.Printf("  %s1)%s Port Forward - You configure your router, we validate connectivity\n", ColorCyan, ColorReset)
		fmt.Printf("  %s2)%s Cloudflare Tunnel - No router config needed, traffic routed via Cloudflare\n", ColorCyan, ColorReset)
		fmt.Println()
		fmt.Print("Choice [1-2]: ")

		reader := bufio.NewReader(os.Stdin)
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		switch choice {
		case "1":
			mode = "port-forward"
		case "2":
			mode = "tunnel"
		default:
			return fmt.Errorf("invalid choice: %s", choice)
		}
	}

	// Validate mode
	if mode != "port-forward" && mode != "tunnel" {
		return fmt.Errorf("invalid mode: %s (must be 'port-forward' or 'tunnel')", mode)
	}

	// Load existing config or create new (used for validation)
	_, err := loadConfig()
	if err != nil {
		// Config doesn't exist yet, that's OK
	}

	// Initialize external config
	var extCfg ExternalAccessConfig
	extCfg.Mode = mode
	extCfg.PortForward.LocalPort = *portFlag
	extCfg.PortForward.PublicPort = *portFlag
	extCfg.Security.HardenOnEnable = true
	extCfg.Security.MaxConnectionsPerIP = 50
	extCfg.Security.SharesPerSecond = 50
	extCfg.Security.BanThreshold = 5
	extCfg.Security.BanDuration = "60m"

	fmt.Println()

	switch mode {
	case "port-forward":
		if err := setupPortForward(&extCfg, *hostnameFlag); err != nil {
			return err
		}
	case "tunnel":
		if err := setupTunnel(&extCfg, *hostnameFlag, *tunnelNameFlag); err != nil {
			return err
		}
	}

	// Ask about security hardening
	fmt.Println()
	fmt.Printf("%s--- Security Hardening ---%s\n", ColorBold, ColorReset)
	fmt.Println()
	if confirmAction("Apply security hardening? (Recommended)") {
		extCfg.Security.HardenOnEnable = true
		fmt.Println()
		fmt.Printf("  %s->%s Reducing maxConnectionsPerIP: 100 -> 50\n", ColorCyan, ColorReset)
		fmt.Printf("  %s->%s Reducing sharesPerSecond: 100 -> 50\n", ColorCyan, ColorReset)
		fmt.Printf("  %s->%s Reducing banThreshold: 10 -> 5\n", ColorCyan, ColorReset)
		fmt.Printf("  %s->%s Increasing banDuration: 30m -> 60m\n", ColorCyan, ColorReset)
	} else {
		extCfg.Security.HardenOnEnable = false
	}

	// Save configuration
	if err := saveExternalConfig(&extCfg); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Println()
	printSuccess("Configuration saved")
	fmt.Println()
	fmt.Println("To enable external access, run:")
	fmt.Printf("  %sspiralctl external enable%s\n", ColorCyan, ColorReset)
	fmt.Println()

	return nil
}

func setupPortForward(cfg *ExternalAccessConfig, hostname string) error {
	fmt.Printf("%s--- Port Forward Setup ---%s\n", ColorBold, ColorReset)
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Get public hostname or IP
	publicHost := hostname
	if publicHost == "" {
		fmt.Print("Enter your public hostname or IP (e.g., mypool.duckdns.org): ")
		input, _ := reader.ReadString('\n')
		publicHost = strings.TrimSpace(input)
	}
	if publicHost == "" {
		return fmt.Errorf("public hostname is required")
	}

	// SEC-6: Validate hostname format
	if !isValidHostnameOrIP(publicHost) {
		return fmt.Errorf("invalid hostname format: %s", publicHost)
	}
	cfg.PortForward.PublicHost = publicHost

	// Get external port
	fmt.Printf("Enter external port [%d]: ", cfg.PortForward.PublicPort)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != "" {
		port, err := strconv.Atoi(input)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port: %s (must be 1-65535)", input)
		}
		cfg.PortForward.PublicPort = port
	}

	// Get local port
	fmt.Printf("Enter local stratum port [%d]: ", cfg.PortForward.LocalPort)
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != "" {
		port, err := strconv.Atoi(input)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port: %s (must be 1-65535)", input)
		}
		cfg.PortForward.LocalPort = port
	}

	// Detect VIP address for internal target if HA+VIP is enabled
	internalTarget := "<your-local-ip>"
	if poolCfg, loadErr := loadConfig(); loadErr == nil && poolCfg.VIP.Enabled && poolCfg.VIP.Address != "" {
		internalTarget = poolCfg.VIP.Address
		fmt.Printf("  %sVIP detected — using VIP address %s as internal target for failover support%s\n",
			ColorCyan, poolCfg.VIP.Address, ColorReset)
	}

	fmt.Println()
	fmt.Printf("%s--- Router Configuration Required ---%s\n", ColorBold, ColorReset)
	fmt.Println()
	fmt.Println("Configure your router to forward:")
	fmt.Printf("  External: %s%s:%d%s (TCP)\n", ColorCyan, publicHost, cfg.PortForward.PublicPort, ColorReset)
	fmt.Printf("  Internal: %s%s:%d%s\n", ColorCyan, internalTarget, cfg.PortForward.LocalPort, ColorReset)
	fmt.Println()
	fmt.Println("Common router instructions:")
	fmt.Println("  - Login to router admin (usually 192.168.1.1)")
	fmt.Println("  - Find 'Port Forwarding' or 'NAT' settings")
	fmt.Printf("  - Add rule: External %d -> Internal %s:%d TCP\n",
		cfg.PortForward.PublicPort, internalTarget, cfg.PortForward.LocalPort)
	fmt.Println()

	return nil
}

func setupTunnel(cfg *ExternalAccessConfig, hostname, tunnelName string) error {
	fmt.Printf("%s--- Cloudflare Tunnel Setup ---%s\n", ColorBold, ColorReset)
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Check prerequisites
	fmt.Println("Checking prerequisites...")
	fmt.Println()

	// Check cloudflared binary
	binaryPath := "/usr/local/bin/cloudflared"
	cfg.Tunnel.BinaryPath = binaryPath

	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		printError("cloudflared binary not found at " + binaryPath)
		fmt.Println()
		fmt.Println("Install cloudflared first:")
		fmt.Println("  curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared")
		fmt.Println("  chmod +x /usr/local/bin/cloudflared")
		fmt.Println()
		return fmt.Errorf("cloudflared not installed")
	}
	printSuccess("cloudflared binary found at " + binaryPath)

	// Get tunnel name
	name := tunnelName
	if name == "" {
		fmt.Print("Enter tunnel name: ")
		input, _ := reader.ReadString('\n')
		name = strings.TrimSpace(input)
	}
	if name == "" {
		return fmt.Errorf("tunnel name is required")
	}

	// SEC-3: Validate tunnel name format to prevent systemd injection
	if !tunnelNameRegex.MatchString(name) {
		return fmt.Errorf("invalid tunnel name: %s (must be alphanumeric with hyphens/underscores, starting with alphanumeric)", name)
	}
	if len(name) > 63 {
		return fmt.Errorf("tunnel name too long: %d chars (max 63)", len(name))
	}
	cfg.Tunnel.Name = name

	// Check if tunnel exists
	tunnelExists, err := checkTunnelExists(binaryPath, name)
	if err != nil {
		printWarning("Could not verify tunnel existence: " + err.Error())
	} else if !tunnelExists {
		fmt.Println()
		printWarning(fmt.Sprintf("Tunnel '%s' not found", name))
		fmt.Println()
		fmt.Println("Create a tunnel first:")
		fmt.Printf("  cloudflared tunnel create %s\n", name)
		fmt.Println()
		fmt.Println("Then configure DNS in Cloudflare dashboard.")
		fmt.Println()
		return fmt.Errorf("tunnel does not exist")
	} else {
		printSuccess(fmt.Sprintf("Tunnel '%s' found", name))
	}

	// Get hostname
	host := hostname
	if host == "" {
		fmt.Print("Enter public hostname (e.g., stratum.mydomain.com): ")
		input, _ := reader.ReadString('\n')
		host = strings.TrimSpace(input)
	}
	if host == "" {
		return fmt.Errorf("hostname is required")
	}

	// SEC-6: Validate hostname format
	if !hostnameRegex.MatchString(host) {
		return fmt.Errorf("invalid hostname format: %s", host)
	}
	cfg.Tunnel.Hostname = host

	// Look for credentials file
	credPath := findCredentialsPath(name)
	if credPath != "" {
		printSuccess("Credentials file found: " + credPath)
		cfg.Tunnel.CredentialsPath = credPath
	} else {
		fmt.Print("Enter path to credentials JSON: ")
		input, _ := reader.ReadString('\n')
		credPath = strings.TrimSpace(input)
		if credPath == "" {
			return fmt.Errorf("credentials path is required")
		}
		// E-2: Validate resolved path doesn't escape expected directories.
		// Resolve to absolute path and verify it stays within allowed locations.
		absCredPath, err := filepath.Abs(credPath)
		if err != nil {
			return fmt.Errorf("invalid credentials path: %w", err)
		}
		allowedPrefixes := []string{"/etc/spiralpool/", "/root/.cloudflared/", os.Getenv("HOME") + "/.cloudflared/"}
		pathAllowed := false
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(absCredPath, prefix) {
				pathAllowed = true
				break
			}
		}
		if !pathAllowed {
			return fmt.Errorf("invalid credentials path: must be within /etc/spiralpool/, ~/.cloudflared/, or /root/.cloudflared/")
		}
		if _, err := os.Stat(absCredPath); os.IsNotExist(err) {
			return fmt.Errorf("credentials file not found: %s", absCredPath)
		}
		cfg.Tunnel.CredentialsPath = absCredPath
	}

	fmt.Println()
	printSuccess("Tunnel configuration validated")

	return nil
}

func externalEnable(args []string) error {
	// Set up signal handling for cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Track what we've done for rollback
	var tunnelConfigCreated bool
	var serviceInstalled bool
	var hardeningApplied bool

	// Rollback function for partial failure
	rollback := func(reason string) {
		printError("Enable failed: " + reason)
		fmt.Println("Rolling back changes...")

		if serviceInstalled {
			_ = exec.Command("systemctl", "stop", "cloudflared-spiralpool").Run()
			_ = exec.Command("systemctl", "disable", "cloudflared-spiralpool").Run()
			_ = os.Remove("/etc/systemd/system/cloudflared-spiralpool.service")
			_ = exec.Command("systemctl", "daemon-reload").Run()
		}

		if tunnelConfigCreated {
			_ = os.Remove("/etc/spiralpool/cloudflared/config.yml")
		}

		if hardeningApplied {
			extCfg, err := loadExternalConfig()
			if err == nil {
				_ = revertSecurityHardening(&extCfg)
			}
		}
	}

	// Handle interrupts gracefully
	go func() {
		<-sigChan
		fmt.Println()
		rollback("interrupted by user")
		os.Exit(1)
	}()

	printBanner()
	fmt.Printf("%s=== ENABLE EXTERNAL ACCESS ===%s\n\n", ColorBold, ColorReset)

	// Load external config
	extCfg, err := loadExternalConfig()
	if err != nil {
		return fmt.Errorf("no configuration found. Run 'spiralctl external setup' first")
	}

	if extCfg.Mode == "" {
		return fmt.Errorf("external access not configured. Run 'spiralctl external setup' first")
	}

	// Idempotency check
	if extCfg.Enabled {
		printInfo("External access is already enabled")
		fmt.Println()
		fmt.Println("Current configuration:")
		switch extCfg.Mode {
		case "port-forward":
			fmt.Printf("  Endpoint: stratum+tcp://%s:%d\n", extCfg.PortForward.PublicHost, extCfg.PortForward.PublicPort)
		case "tunnel":
			fmt.Printf("  Endpoint: stratum+tcp://%s:443\n", extCfg.Tunnel.Hostname)
		}
		fmt.Println()
		fmt.Println("To reconfigure, run 'spiralctl external disable' first.")
		return nil
	}

	fmt.Printf("Mode: %s%s%s\n", ColorCyan, extCfg.Mode, ColorReset)
	fmt.Println()

	// Apply security hardening if enabled
	// SEC-1 FIX: Actually apply hardening to main config
	if extCfg.Security.HardenOnEnable {
		if err := applySecurityHardening(&extCfg); err != nil {
			printWarning("Failed to apply security hardening: " + err.Error())
		} else {
			hardeningApplied = true
			printSuccess("Security hardening applied to stratum configuration")
		}
	}

	switch extCfg.Mode {
	case "port-forward":
		if err := enablePortForward(&extCfg); err != nil {
			if hardeningApplied {
				_ = revertSecurityHardening(&extCfg)
			}
			return err
		}
	case "tunnel":
		configPath, err := generateTunnelConfig(&extCfg)
		if err != nil {
			rollback(err.Error())
			return fmt.Errorf("failed to generate tunnel config: %w", err)
		}
		extCfg.Tunnel.ConfigPath = configPath
		tunnelConfigCreated = true

		fmt.Println("Installing cloudflared service...")
		if err := installCloudflaredService(&extCfg); err != nil {
			rollback(err.Error())
			return fmt.Errorf("failed to install service: %w", err)
		}
		serviceInstalled = true

		fmt.Println()
		printSuccess("External access ENABLED via Cloudflare Tunnel")
		fmt.Println()
		fmt.Println("For hashrate rental services, use this pool URL:")
		fmt.Printf("  %sstratum+tcp://%s:443%s\n", ColorGreen, extCfg.Tunnel.Hostname, ColorReset)
		fmt.Println()
	default:
		rollback("unknown mode: " + extCfg.Mode)
		return fmt.Errorf("unknown mode: %s", extCfg.Mode)
	}

	// Mark as enabled
	extCfg.Enabled = true
	if err := saveExternalConfig(&extCfg); err != nil {
		rollback(err.Error())
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	return nil
}

// enablePortForward validates and displays port-forward configuration.
// Note: This function is read-only with respect to cfg — it does not modify any
// config values. Security hardening is applied/reverted separately in externalEnable.
func enablePortForward(cfg *ExternalAccessConfig) error {
	fmt.Printf("Public Endpoint: %sstratum+tcp://%s:%d%s\n",
		ColorGreen, cfg.PortForward.PublicHost, cfg.PortForward.PublicPort, ColorReset)
	fmt.Println()

	// Test connectivity.
	// NOTE: This is a best-effort reachability check. There is an inherent TOCTOU
	// gap: the port may become unreachable (or reachable) between this check and
	// actual miner connections. The check is advisory, not a guarantee.
	fmt.Println("Testing external connectivity...")
	if err := testExternalPort(cfg.PortForward.PublicHost, cfg.PortForward.PublicPort); err != nil {
		printWarning("External connectivity test failed: " + err.Error())
		fmt.Println()
		fmt.Println("This could mean:")
		fmt.Println("  - Router port forwarding is not configured")
		fmt.Println("  - Firewall is blocking the port")
		fmt.Println("  - DNS has not propagated yet")
		fmt.Println()
		if !confirmAction("Continue anyway?") {
			return fmt.Errorf("connectivity test failed")
		}
	} else {
		printSuccess("External port is reachable")
	}

	fmt.Println()
	printSuccess("External access ENABLED")
	fmt.Println()
	fmt.Println("For hashrate rental services, use this pool URL:")
	fmt.Printf("  %sstratum+tcp://%s:%d%s\n", ColorGreen, cfg.PortForward.PublicHost, cfg.PortForward.PublicPort, ColorReset)
	fmt.Println()

	return nil
}

func externalDisable() error {
	printBanner()
	fmt.Printf("%s=== DISABLE EXTERNAL ACCESS ===%s\n\n", ColorBold, ColorReset)

	// Load external config
	extCfg, err := loadExternalConfig()
	if err != nil {
		printInfo("External access is not configured")
		return nil
	}

	if !extCfg.Enabled {
		printInfo("External access is already disabled")
		return nil
	}

	// Revert security hardening if it was applied
	// SEC-1 FIX: Revert main config changes
	if extCfg.Security.HardenOnEnable {
		if err := revertSecurityHardening(&extCfg); err != nil {
			printWarning("Failed to revert security hardening: " + err.Error())
		} else {
			printInfo("Security hardening reverted in stratum configuration")
		}
	}

	// Stop tunnel if running
	if extCfg.Mode == "tunnel" {
		fmt.Println("Stopping cloudflared service...")
		_ = exec.Command("systemctl", "stop", "cloudflared-spiralpool").Run()
		_ = exec.Command("systemctl", "disable", "cloudflared-spiralpool").Run()
		printSuccess("Cloudflared service stopped")
	}

	// Mark as disabled
	extCfg.Enabled = false
	if err := saveExternalConfig(&extCfg); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Println()
	printSuccess("External access DISABLED")
	fmt.Println()

	return nil
}

func externalStatus() error {
	printBanner()
	fmt.Printf("%s=== EXTERNAL ACCESS STATUS ===%s\n\n", ColorBold, ColorReset)

	// Load external config
	extCfg, err := loadExternalConfig()
	if err != nil {
		fmt.Printf("Status:   %sNOT CONFIGURED%s\n", ColorYellow, ColorReset)
		fmt.Println()
		fmt.Println("Run 'spiralctl external setup' to configure external access.")
		return nil
	}

	// Mode
	fmt.Printf("Mode:     %s%s%s\n", ColorCyan, extCfg.Mode, ColorReset)

	// Status
	if extCfg.Enabled {
		fmt.Printf("Status:   %sENABLED%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("Status:   %sDISABLED%s\n", ColorYellow, ColorReset)
	}

	// Endpoint
	switch extCfg.Mode {
	case "port-forward":
		fmt.Printf("Endpoint: stratum+tcp://%s:%d\n",
			extCfg.PortForward.PublicHost, extCfg.PortForward.PublicPort)
	case "tunnel":
		fmt.Printf("Endpoint: stratum+tcp://%s:443\n", extCfg.Tunnel.Hostname)
	}
	fmt.Println()

	// Mode-specific details
	switch extCfg.Mode {
	case "port-forward":
		fmt.Printf("%sPort Forward Details:%s\n", ColorBold, ColorReset)
		fmt.Printf("  Public Host:  %s\n", extCfg.PortForward.PublicHost)
		fmt.Printf("  Public Port:  %d\n", extCfg.PortForward.PublicPort)
		fmt.Printf("  Local Port:   %d\n", extCfg.PortForward.LocalPort)
	case "tunnel":
		fmt.Printf("%sTunnel Details:%s\n", ColorBold, ColorReset)
		fmt.Printf("  Name:         %s\n", extCfg.Tunnel.Name)
		fmt.Printf("  Hostname:     %s\n", extCfg.Tunnel.Hostname)

		// Check service status
		if isServiceRunning("cloudflared-spiralpool") {
			fmt.Printf("  Process:      %sRunning%s\n", ColorGreen, ColorReset)
		} else {
			fmt.Printf("  Process:      %sNot Running%s\n", ColorRed, ColorReset)
		}
	}
	fmt.Println()

	// Security status
	fmt.Printf("%sSecurity:%s\n", ColorBold, ColorReset)
	if extCfg.Security.HardenOnEnable && extCfg.Enabled {
		fmt.Printf("  Hardening:         %sActive%s\n", ColorGreen, ColorReset)
		fmt.Printf("  Connections/IP:    %d (hardened from %d)\n",
			extCfg.Security.MaxConnectionsPerIP, extCfg.Security.OriginalMaxConnPerIP)
		fmt.Printf("  Shares/sec:        %d (hardened from %d)\n",
			extCfg.Security.SharesPerSecond, extCfg.Security.OriginalSharesPerSec)
		fmt.Printf("  Ban threshold:     %d (hardened from %d)\n",
			extCfg.Security.BanThreshold, extCfg.Security.OriginalBanThreshold)
		fmt.Printf("  Ban duration:      %s (hardened from %s)\n",
			extCfg.Security.BanDuration, extCfg.Security.OriginalBanDuration)
	} else if extCfg.Security.HardenOnEnable {
		fmt.Printf("  Hardening:         %sPending (will apply on enable)%s\n", ColorYellow, ColorReset)
	} else {
		fmt.Printf("  Hardening:         %sDisabled%s\n", ColorYellow, ColorReset)
	}
	fmt.Println()

	return nil
}

func externalTest(args []string) error {
	printBanner()
	fmt.Printf("%s=== EXTERNAL CONNECTIVITY TEST ===%s\n\n", ColorBold, ColorReset)

	// Load external config
	extCfg, err := loadExternalConfig()
	if err != nil {
		return fmt.Errorf("no configuration found. Run 'spiralctl external setup' first")
	}

	var host string
	var port int

	switch extCfg.Mode {
	case "port-forward":
		host = extCfg.PortForward.PublicHost
		port = extCfg.PortForward.PublicPort
	case "tunnel":
		host = extCfg.Tunnel.Hostname
		port = 443
	default:
		return fmt.Errorf("unknown mode: %s", extCfg.Mode)
	}

	fmt.Printf("Testing connectivity to %s%s:%d%s...\n", ColorCyan, host, port, ColorReset)
	fmt.Println()

	// DNS resolution test
	fmt.Print("DNS Resolution... ")
	ips, err := net.LookupIP(host)
	if err != nil {
		fmt.Printf("%sFAILED%s\n", ColorRed, ColorReset)
		printError("Could not resolve hostname: " + err.Error())
		return fmt.Errorf("DNS resolution failed")
	}
	fmt.Printf("%sOK%s (%s)\n", ColorGreen, ColorReset, ips[0].String())

	// TCP connection test
	fmt.Print("TCP Connection... ")
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 10*time.Second)
	if err != nil {
		fmt.Printf("%sFAILED%s\n", ColorRed, ColorReset)
		printError("Could not connect: " + err.Error())
		fmt.Println()
		fmt.Println("This could mean:")
		fmt.Println("  - Port forwarding is not configured correctly")
		fmt.Println("  - Firewall is blocking the connection")
		fmt.Println("  - The service is not listening on the port")
		return fmt.Errorf("TCP connection failed")
	}
	conn.Close()
	fmt.Printf("%sOK%s\n", ColorGreen, ColorReset)

	// Try external port checker service
	fmt.Print("External Verification... ")
	if err := checkPortViaExternalService(host, port); err != nil {
		fmt.Printf("%sUNAVAILABLE%s (using direct test only)\n", ColorYellow, ColorReset)
	} else {
		fmt.Printf("%sOK%s\n", ColorGreen, ColorReset)
	}

	fmt.Println()
	printSuccess(fmt.Sprintf("Port %d is OPEN and reachable", port))
	fmt.Println()

	return nil
}

// Helper functions

// SEC-1 FIX: applySecurityHardening now modifies the main stratum config
func applySecurityHardening(cfg *ExternalAccessConfig) error {
	// Load main stratum config
	mainCfg, err := loadStratumRateLimitConfig()
	if err != nil {
		// Use defaults if config doesn't exist
		cfg.Security.OriginalMaxConnPerIP = 100
		cfg.Security.OriginalSharesPerSec = 100
		cfg.Security.OriginalBanThreshold = 10
		cfg.Security.OriginalBanDuration = "30m"
	} else {
		// Store original values before hardening
		cfg.Security.OriginalMaxConnPerIP = mainCfg.MaxConnectionsPerIP
		cfg.Security.OriginalSharesPerSec = mainCfg.MaxSharesPerSecond
		cfg.Security.OriginalBanThreshold = mainCfg.BanThreshold
		cfg.Security.OriginalBanDuration = mainCfg.BanDuration
	}

	// Apply hardened values to external config
	cfg.Security.MaxConnectionsPerIP = 50
	cfg.Security.SharesPerSecond = 50
	cfg.Security.BanThreshold = 5
	cfg.Security.BanDuration = "60m"

	// SEC-1 FIX: Write hardened values to main stratum config
	if err := updateStratumRateLimitConfig(cfg.Security.MaxConnectionsPerIP,
		cfg.Security.SharesPerSecond, cfg.Security.BanThreshold, cfg.Security.BanDuration); err != nil {
		return fmt.Errorf("failed to update stratum config: %w", err)
	}

	// Signal stratum server to reload config (if running)
	signalStratumReload()

	// Save the external config with original values backup
	return saveExternalConfig(cfg)
}

// SEC-1 FIX: revertSecurityHardening now restores the main stratum config
func revertSecurityHardening(cfg *ExternalAccessConfig) error {
	// Restore original values in external config
	cfg.Security.MaxConnectionsPerIP = cfg.Security.OriginalMaxConnPerIP
	cfg.Security.SharesPerSecond = cfg.Security.OriginalSharesPerSec
	cfg.Security.BanThreshold = cfg.Security.OriginalBanThreshold
	cfg.Security.BanDuration = cfg.Security.OriginalBanDuration

	// SEC-1 FIX: Write original values back to main stratum config
	if err := updateStratumRateLimitConfig(cfg.Security.OriginalMaxConnPerIP,
		cfg.Security.OriginalSharesPerSec, cfg.Security.OriginalBanThreshold, cfg.Security.OriginalBanDuration); err != nil {
		return fmt.Errorf("failed to revert stratum config: %w", err)
	}

	// Signal stratum server to reload config (if running)
	signalStratumReload()

	return saveExternalConfig(cfg)
}

// loadStratumRateLimitConfig reads rate limiting config from the main stratum config
func loadStratumRateLimitConfig() (*RateLimitingConfig, error) {
	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return nil, err
	}

	// Parse as generic map to find stratum.rateLimiting section
	var rawCfg map[string]interface{}
	if err := yaml.Unmarshal(data, &rawCfg); err != nil {
		return nil, err
	}

	stratumSection, ok := rawCfg["stratum"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("stratum section not found")
	}

	rateLimitSection, ok := stratumSection["rateLimiting"].(map[string]interface{})
	if !ok {
		// Return defaults if section doesn't exist
		return &RateLimitingConfig{
			MaxConnectionsPerIP:  100,
			MaxConnectionsPerMin: 30,
			MaxSharesPerSecond:   100,
			BanThreshold:         10,
			BanDuration:          "30m",
		}, nil
	}

	// Extract values with float64 fallback. YAML v3 typically decodes numbers as int,
	// but JSON and some edge cases may produce float64 for numeric values in interface{} maps.
	cfg := &RateLimitingConfig{
		MaxConnectionsPerIP:  100,
		MaxConnectionsPerMin: 30,
		MaxSharesPerSecond:   100,
		BanThreshold:         10,
		BanDuration:          "30m",
	}

	cfg.MaxConnectionsPerIP = yamlIntValue(rateLimitSection, "maxConnectionsPerIP", cfg.MaxConnectionsPerIP)
	cfg.MaxConnectionsPerMin = yamlIntValue(rateLimitSection, "maxConnectionsPerMin", cfg.MaxConnectionsPerMin)
	cfg.MaxSharesPerSecond = yamlIntValue(rateLimitSection, "maxSharesPerSecond", cfg.MaxSharesPerSecond)
	cfg.BanThreshold = yamlIntValue(rateLimitSection, "banThreshold", cfg.BanThreshold)
	if v, ok := rateLimitSection["banDuration"].(string); ok {
		cfg.BanDuration = v
	}

	return cfg, nil
}

// updateStratumRateLimitConfig updates the rate limiting values in the main stratum config
func updateStratumRateLimitConfig(maxConnPerIP, maxSharesPerSec, banThreshold int, banDuration string) error {
	// Backup the config first
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning("Failed to backup config: " + err.Error())
	}

	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return err
	}

	// Parse as ordered structure to preserve formatting
	var rawCfg yaml.Node
	if err := yaml.Unmarshal(data, &rawCfg); err != nil {
		return err
	}

	// Find and update stratum.rateLimiting section
	updated := updateRateLimitingInNode(&rawCfg, maxConnPerIP, maxSharesPerSec, banThreshold, banDuration)
	if !updated {
		return fmt.Errorf("could not find stratum.rateLimiting section in config")
	}

	// Marshal back
	output, err := yaml.Marshal(&rawCfg)
	if err != nil {
		return err
	}

	return os.WriteFile(DefaultConfigFile, output, 0600)
}

// updateRateLimitingInNode recursively finds and updates the rateLimiting section
func updateRateLimitingInNode(node *yaml.Node, maxConnPerIP, maxSharesPerSec, banThreshold int, banDuration string) bool {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return updateRateLimitingInNode(node.Content[0], maxConnPerIP, maxSharesPerSec, banThreshold, banDuration)
	}

	if node.Kind != yaml.MappingNode {
		return false
	}

	for i := 0; i < len(node.Content)-1; i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]

		if key.Value == "stratum" && value.Kind == yaml.MappingNode {
			// Found stratum section, look for rateLimiting
			for j := 0; j < len(value.Content)-1; j += 2 {
				subKey := value.Content[j]
				subValue := value.Content[j+1]

				if subKey.Value == "rateLimiting" && subValue.Kind == yaml.MappingNode {
					// Found rateLimiting, update values
					for k := 0; k < len(subValue.Content)-1; k += 2 {
						rlKey := subValue.Content[k]
						rlValue := subValue.Content[k+1]

						switch rlKey.Value {
						case "maxConnectionsPerIP":
							rlValue.Value = strconv.Itoa(maxConnPerIP)
						case "maxSharesPerSecond":
							rlValue.Value = strconv.Itoa(maxSharesPerSec)
						case "banThreshold":
							rlValue.Value = strconv.Itoa(banThreshold)
						case "banDuration":
							rlValue.Value = banDuration
						}
					}
					return true
				}
			}
		}
	}

	return false
}

// signalStratumReload notifies the user that a restart is needed to apply config changes.
// Note: The stratum server handles SIGHUP gracefully (logs and ignores) but does not
// support live config reload. A full service restart is required.
func signalStratumReload() {
	printWarning("Config changes written to disk. Restart the stratum service to apply: systemctl restart spiralpool")
}

func testExternalPort(host string, port int) error {
	// Try to connect to the port from the outside perspective
	// First, try DNS resolution
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("DNS resolution failed: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no IP addresses found for %s", host)
	}

	// Try TCP connection
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("TCP connection failed: %w", err)
	}
	conn.Close()

	return nil
}

func checkPortViaExternalService(host string, port int) error {
	// SEC-7 FIX: URL-encode the hostname to prevent injection
	encodedHost := url.QueryEscape(host)
	checkURL := fmt.Sprintf("https://portchecker.co/check?port=%d&host=%s", port, encodedHost)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
	if err != nil {
		return err
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Read response (limited)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
	if err != nil {
		return err
	}

	// Check if response indicates port is open
	bodyLower := strings.ToLower(string(body))
	if strings.Contains(bodyLower, "open") ||
		strings.Contains(bodyLower, "reachable") ||
		strings.Contains(bodyLower, "success") {
		return nil
	}

	return fmt.Errorf("port appears closed")
}

func checkTunnelExists(binaryPath, name string) (bool, error) {
	cmd := exec.Command(binaryPath, "tunnel", "list")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	// Look for exact tunnel name match to avoid partial matches
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return true, nil
		}
	}

	return false, nil
}

func findCredentialsPath(tunnelName string) string {
	// Common locations for credentials files
	paths := []string{
		filepath.Join("/etc/spiralpool/cloudflared", tunnelName+".json"),
		filepath.Join("/root/.cloudflared", tunnelName+".json"),
	}

	home := os.Getenv("HOME")
	if home != "" {
		paths = append(paths, filepath.Join(home, ".cloudflared", tunnelName+".json"))
	}

	// Check each path
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Search directories for any credentials file
	dirs := []string{
		"/etc/spiralpool/cloudflared",
		"/root/.cloudflared",
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".cloudflared"))
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				// Check if this looks like a credentials file
				path := filepath.Join(dir, entry.Name())
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				// Credentials files have an AccountTag field
				if strings.Contains(string(data), "AccountTag") {
					return path
				}
			}
		}
	}

	return ""
}

func generateTunnelConfig(cfg *ExternalAccessConfig) (string, error) {
	// Create config directory
	configDir := "/etc/spiralpool/cloudflared"
	if err := os.MkdirAll(configDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yml")

	// Get local stratum port from main config
	localPort := cfg.PortForward.LocalPort
	if localPort == 0 {
		localPort = 3333
	}

	// Build config content
	content := fmt.Sprintf(`# Cloudflared tunnel configuration for Spiral Pool
# Generated by spiralctl external setup

tunnel: %s
credentials-file: %s

ingress:
  # Stratum protocol routing
  - hostname: %s
    service: tcp://localhost:%d
  # Catch-all for unmatched requests
  - service: http_status:404
`, cfg.Tunnel.Name, cfg.Tunnel.CredentialsPath, cfg.Tunnel.Hostname, localPort)

	// Write config file
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("failed to write config: %w", err)
	}

	return configPath, nil
}

func installCloudflaredService(cfg *ExternalAccessConfig) error {
	// SEC-3: Tunnel name already validated by tunnelNameRegex in setupTunnel
	// Double-check here for defense in depth
	if !tunnelNameRegex.MatchString(cfg.Tunnel.Name) {
		return fmt.Errorf("invalid tunnel name for service: %s", cfg.Tunnel.Name)
	}

	// Create systemd service file.
	// NOTE: cloudflared runs as root for simplicity during install. In production,
	// consider creating a dedicated "cloudflared" user with minimal privileges and
	// updating User= below. The tunnel credentials file must be readable by that user.
	serviceContent := fmt.Sprintf(`[Unit]
Description=Cloudflare Tunnel for Spiral Pool
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s tunnel --config %s run %s
Restart=on-failure
RestartSec=10
User=root

[Install]
WantedBy=multi-user.target
`, cfg.Tunnel.BinaryPath, cfg.Tunnel.ConfigPath, cfg.Tunnel.Name)

	servicePath := "/etc/systemd/system/cloudflared-spiralpool.service"
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	// Reload systemd
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// Enable and start service
	if err := exec.Command("systemctl", "enable", "cloudflared-spiralpool").Run(); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	if err := exec.Command("systemctl", "start", "cloudflared-spiralpool").Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	printSuccess("Cloudflared service installed and started")
	return nil
}

// yamlIntValue extracts an integer from a YAML-decoded map[string]interface{}.
// YAML v3 typically decodes numbers as int, but JSON and some edge cases may
// produce float64. This handles both type assertions gracefully.
func yamlIntValue(m map[string]interface{}, key string, defaultVal int) int {
	v, exists := m[key]
	if !exists {
		return defaultVal
	}
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	default:
		return defaultVal
	}
}

// Validation helpers

func isValidHostnameOrIP(s string) bool {
	// Check if it's a valid IP address
	if net.ParseIP(s) != nil {
		return true
	}
	// Check if it's a valid hostname
	return hostnameRegex.MatchString(s)
}

// Config persistence

const externalConfigPath = "/etc/spiralpool/external.yaml"

func loadExternalConfig() (ExternalAccessConfig, error) {
	var cfg ExternalAccessConfig

	data, err := os.ReadFile(externalConfigPath)
	if err != nil {
		return cfg, err
	}

	// SEC-5 FIX: Use proper YAML library instead of custom parser
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse external config: %w", err)
	}

	return cfg, nil
}

func saveExternalConfig(cfg *ExternalAccessConfig) error {
	// Ensure directory exists
	dir := filepath.Dir(externalConfigPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}

	// SEC-8 FIX: Backup existing config before overwriting
	if fileExists(externalConfigPath) {
		if err := backupFile(externalConfigPath); err != nil {
			printWarning("Failed to backup external config: " + err.Error())
		}
	}

	// SEC-5 FIX: Use proper YAML library
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Add header comment
	header := "# External Access Configuration for Spiral Pool\n# Generated by spiralctl external setup\n\n"
	content := append([]byte(header), data...)

	// Write with secure permissions
	return os.WriteFile(externalConfigPath, content, 0600)
}

// Note: isServiceRunning is defined in status.go
