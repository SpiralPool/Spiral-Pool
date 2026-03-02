// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// validLogLevels defines allowed log level values
var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
	"":      true, // empty is allowed (uses default)
}

// validPortRange checks if a port number is in valid range
func validPortRange(port int) bool {
	return port >= 1 && port <= 65535
}

// safeFilePath validates and sanitizes file paths to prevent path traversal
func safeFilePath(path string) (string, error) {
	// Clean the path
	cleaned := filepath.Clean(path)

	// Check for path traversal attempts
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("path traversal not allowed: %s", path)
	}

	// Ensure it's an absolute path starting with expected prefix
	if !strings.HasPrefix(cleaned, "/spiralpool/") && !strings.HasPrefix(cleaned, "/home/") {
		return "", fmt.Errorf("path must be within /spiralpool/ or /home/: %s", path)
	}

	return cleaned, nil
}

func runConfig(args []string) error {
	if len(args) < 1 {
		printConfigUsage()
		return nil
	}

	switch args[0] {
	case "validate":
		return runConfigValidate(args[1:])
	case "help", "-h", "--help":
		printConfigUsage()
		return nil
	default:
		return fmt.Errorf("unknown config command: %s", args[0])
	}
}

func printConfigUsage() {
	fmt.Println("Usage: spiralctl config <command>")
	fmt.Println()
	fmt.Printf("%sCommands:%s\n", ColorBold, ColorReset)
	fmt.Printf("  %svalidate%s     Validate configuration files\n", ColorCyan, ColorReset)
	fmt.Println()
	fmt.Printf("%sExamples:%s\n", ColorBold, ColorReset)
	fmt.Println("  spiralctl config validate")
	fmt.Println("  spiralctl config validate --verbose")
	fmt.Println()
}

func runConfigValidate(args []string) error {
	printBanner()
	fmt.Printf("%s=== CONFIGURATION VALIDATION ===%s\n\n", ColorBold, ColorReset)

	verbose := false
	for _, arg := range args {
		if arg == "--verbose" || arg == "-v" {
			verbose = true
		}
	}

	issues := []string{}
	warnings := []string{}
	validations := 0

	// 1. Check main config file exists
	if !fileExists(DefaultConfigFile) {
		issues = append(issues, fmt.Sprintf("Main config file not found: %s", DefaultConfigFile))
	} else {
		validations++
		if verbose {
			printSuccess(fmt.Sprintf("Config file exists: %s", DefaultConfigFile))
		}

		// Load and validate main config
		cfgIssues, cfgWarnings := validateMainConfig(verbose)
		issues = append(issues, cfgIssues...)
		warnings = append(warnings, cfgWarnings...)
		if len(cfgIssues) == 0 {
			validations++
		}
	}

	// 2. Check node configs
	nodes := []struct {
		name   string
		config string
		coin   string
	}{
		// Alphabetically ordered (no coin preference)
		{"Bitcoin II", DefaultBC2Config, "bc2"},
		{"Bitcoin Cash", DefaultBCHConfig, "bch"},
		{"Bitcoin Knots", DefaultBTCConfig, "btc"},
		{"Catcoin", DefaultCATConfig, "cat"},
		{"DigiByte", DefaultDGBConfig, "dgb"},
		{"DigiByte-Scrypt", DefaultDGBScryptConfig, "dgb-scrypt"},
		{"Dogecoin", DefaultDOGEConfig, "doge"},
		{"Fractal Bitcoin", DefaultFBTCConfig, "fbtc"},
		{"Litecoin", DefaultLTCConfig, "ltc"},
		{"Namecoin", DefaultNMCConfig, "nmc"},
		{"PepeCoin", DefaultPEPConfig, "pep"},
		{"Syscoin", DefaultSYSConfig, "sys"},
		{"Myriad", DefaultXMYConfig, "xmy"},
	}

	for _, node := range nodes {
		if fileExists(node.config) {
			nodeIssues, nodeWarnings := validateNodeConfig(node.name, node.config, verbose)
			issues = append(issues, nodeIssues...)
			warnings = append(warnings, nodeWarnings...)
			validations++
		}
	}

	// 3. Check Docker .env if present
	dockerEnv := "/spiralpool/docker/.env"
	if fileExists(dockerEnv) {
		envIssues := validateDockerEnv(dockerEnv, verbose)
		issues = append(issues, envIssues...)
		validations++
	}

	// Print summary
	fmt.Println()
	fmt.Printf("%s=== VALIDATION SUMMARY ===%s\n\n", ColorBold, ColorReset)

	if len(issues) == 0 && len(warnings) == 0 {
		printSuccess(fmt.Sprintf("All %d validations passed!", validations))
		fmt.Println()
		return nil
	}

	if len(warnings) > 0 {
		fmt.Printf("%sWarnings (%d):%s\n", ColorYellow, len(warnings), ColorReset)
		for _, w := range warnings {
			printWarning(w)
		}
		fmt.Println()
	}

	if len(issues) > 0 {
		fmt.Printf("%sErrors (%d):%s\n", ColorRed, len(issues), ColorReset)
		for _, i := range issues {
			printError(i)
		}
		fmt.Println()
		return fmt.Errorf("configuration validation failed with %d error(s)", len(issues))
	}

	printSuccess(fmt.Sprintf("Validation passed with %d warning(s)", len(warnings)))
	return nil
}

func validateMainConfig(verbose bool) (issues []string, warnings []string) {
	fmt.Printf("%s[Main Configuration]%s\n", ColorCyan, ColorReset)

	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		issues = append(issues, fmt.Sprintf("Cannot read config: %v", err))
		return
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		issues = append(issues, fmt.Sprintf("Invalid YAML: %v", err))
		return
	}

	if verbose {
		printSuccess("YAML syntax is valid")
	}

	// Check required fields
	if cfg.Database.Host == "" {
		issues = append(issues, "Database host not configured")
	} else if verbose {
		printSuccess(fmt.Sprintf("Database host: %s", cfg.Database.Host))
	}

	if cfg.Database.Port == 0 {
		warnings = append(warnings, "Database port not set (will use default 5432)")
	} else if !validPortRange(cfg.Database.Port) {
		issues = append(issues, fmt.Sprintf("Invalid database port: %d", cfg.Database.Port))
	} else if verbose {
		printSuccess(fmt.Sprintf("Database port: %d", cfg.Database.Port))
	}

	if cfg.Database.User == "" {
		issues = append(issues, "Database user not configured")
	}

	if cfg.Database.Database == "" {
		issues = append(issues, "Database name not configured")
	}

	// Validate API port if enabled
	if cfg.Global.APIEnabled {
		if cfg.Global.APIPort == 0 {
			warnings = append(warnings, "API port not set (will use default 4000)")
		} else if !validPortRange(cfg.Global.APIPort) {
			issues = append(issues, fmt.Sprintf("Invalid API port: %d", cfg.Global.APIPort))
		} else if verbose {
			printSuccess(fmt.Sprintf("API port: %d", cfg.Global.APIPort))
		}
	}

	// Validate VIP if enabled
	if cfg.VIP.Enabled {
		if cfg.VIP.Address == "" {
			issues = append(issues, "VIP enabled but address not configured")
		} else {
			ip := net.ParseIP(cfg.VIP.Address)
			if ip == nil {
				issues = append(issues, fmt.Sprintf("Invalid VIP address: %s", cfg.VIP.Address))
			} else if verbose {
				printSuccess(fmt.Sprintf("VIP address: %s", cfg.VIP.Address))
			}
		}

		if cfg.VIP.Interface == "" {
			issues = append(issues, "VIP enabled but interface not configured")
		} else if verbose {
			printSuccess(fmt.Sprintf("VIP interface: %s", cfg.VIP.Interface))
		}
	}

	// Validate HA if enabled
	if cfg.HA.Enabled {
		if cfg.HA.PrimaryHost == "" {
			issues = append(issues, "HA enabled but primary host not configured")
		} else if verbose {
			printSuccess(fmt.Sprintf("HA primary: %s", cfg.HA.PrimaryHost))
		}

		if cfg.HA.ReplicaHost == "" {
			issues = append(issues, "HA enabled but replica host not configured")
		} else if verbose {
			printSuccess(fmt.Sprintf("HA replica: %s", cfg.HA.ReplicaHost))
		}
	}

	// Check log level using the package-level map
	if !validLogLevels[strings.ToLower(cfg.Global.LogLevel)] {
		warnings = append(warnings, fmt.Sprintf("Unknown log level: %s (should be debug, info, warn, or error)", cfg.Global.LogLevel))
	}

	fmt.Println()
	return
}

func validateNodeConfig(name, configPath string, verbose bool) (issues []string, warnings []string) {
	fmt.Printf("%s[%s Configuration]%s\n", ColorCyan, name, ColorReset)

	// G304: Path is derived from known daemon config locations, not untrusted input
	data, err := os.ReadFile(configPath) // #nosec G304
	if err != nil {
		issues = append(issues, fmt.Sprintf("Cannot read %s config: %v", name, err))
		return
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Check for required settings
	hasRPCUser := false
	hasRPCPass := false
	hasServer := false
	hasZMQ := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "rpcuser=") {
			hasRPCUser = true
			if verbose {
				printSuccess("RPC user configured")
			}
		}
		if strings.HasPrefix(line, "rpcpassword=") {
			hasRPCPass = true
			if verbose {
				printSuccess("RPC password configured")
			}
		}
		if strings.HasPrefix(line, "server=1") {
			hasServer = true
			if verbose {
				printSuccess("Server mode enabled")
			}
		}
		if strings.HasPrefix(line, "zmqpub") {
			hasZMQ = true
			if verbose {
				printSuccess("ZMQ notifications configured")
			}
		}
	}

	if !hasRPCUser {
		issues = append(issues, fmt.Sprintf("%s: rpcuser not set", name))
	}
	if !hasRPCPass {
		issues = append(issues, fmt.Sprintf("%s: rpcpassword not set", name))
	}
	if !hasServer {
		issues = append(issues, fmt.Sprintf("%s: server=1 not set (required for RPC)", name))
	}
	if !hasZMQ {
		warnings = append(warnings, fmt.Sprintf("%s: ZMQ not configured (block notifications will use polling)", name))
	}

	// Check for common issues
	if strings.Contains(content, "rpcpassword=password") ||
		strings.Contains(content, "rpcpassword=changeme") ||
		strings.Contains(content, "rpcpassword=test") {
		warnings = append(warnings, fmt.Sprintf("%s: RPC password appears to be a weak default value", name))
	}

	fmt.Println()
	return
}

func validateDockerEnv(envPath string, verbose bool) (issues []string) {
	fmt.Printf("%s[Docker Environment]%s\n", ColorCyan, ColorReset)

	// G304: Path is derived from known Docker config locations, not untrusted input
	data, err := os.ReadFile(envPath) // #nosec G304
	if err != nil {
		issues = append(issues, fmt.Sprintf("Cannot read Docker .env: %v", err))
		return
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	hasPoolAddress := false
	hasDBPassword := false
	hasRPCPassword := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "POOL_ADDRESS":
			hasPoolAddress = true
			if value == "" {
				issues = append(issues, "POOL_ADDRESS is empty (required)")
			} else if verbose {
				printSuccess(fmt.Sprintf("Pool address: %s...", value[:min(16, len(value))]))
			}
		case "DB_PASSWORD":
			hasDBPassword = true
			if value == "" {
				issues = append(issues, "DB_PASSWORD is empty (required)")
			} else if verbose {
				printSuccess("Database password configured")
			}
		case "RPC_PASSWORD":
			hasRPCPassword = true
			if value == "" {
				issues = append(issues, "RPC_PASSWORD is empty (required)")
			} else if verbose {
				printSuccess("RPC password configured")
			}
		}
	}

	if !hasPoolAddress {
		issues = append(issues, "POOL_ADDRESS not found in Docker .env")
	}
	if !hasDBPassword {
		issues = append(issues, "DB_PASSWORD not found in Docker .env")
	}
	if !hasRPCPassword {
		issues = append(issues, "RPC_PASSWORD not found in Docker .env")
	}

	fmt.Println()
	return
}

