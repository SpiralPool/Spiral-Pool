// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func runHA(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	fs := flag.NewFlagSet("ha", flag.ExitOnError)
	primaryHost := fs.String("primary", "", "Primary database host")
	replicaHost := fs.String("replica", "", "Replica database host")
	dbPort := fs.Int("port", 5432, "Database port")
	dbUser := fs.String("user", "spiralstratum", "Database user")
	dbName := fs.String("database", "spiralstratum", "Database name")

	// SECURITY: Password is read from environment variable to avoid exposure in process listings
	// The --password flag is removed - use SPIRAL_DATABASE_PASSWORD environment variable instead

	if len(args) < 1 {
		printHAUsage()
		return nil
	}

	action := args[0]
	if len(args) > 1 {
		_ = fs.Parse(args[1:]) // #nosec G104
	}

	// Get password from environment variable (secure - not visible in process listing)
	dbPass := os.Getenv("SPIRAL_DATABASE_PASSWORD")

	switch action {
	case "enable":
		return enableHA(*primaryHost, *replicaHost, *dbPort, *dbUser, dbPass, *dbName)
	case "disable":
		return disableHA()
	case "status":
		return haStatus()
	case "failover":
		return forceFailover()
	default:
		printHAUsage()
		return fmt.Errorf("unknown action: %s", action)
	}
}

func printHAUsage() {
	fmt.Println("Usage: spiralctl ha <action> [options]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  enable     Enable High Availability (database failover)")
	fmt.Println("  disable    Disable High Availability")
	fmt.Println("  status     Show HA status")
	fmt.Println("  failover   Force failover to replica")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --primary <host>     Primary database host (required for enable)")
	fmt.Println("  --replica <host>     Replica database host (required for enable)")
	fmt.Println("  --port <port>        Database port (default: 5432)")
	fmt.Println("  --user <user>        Database user (default: spiralstratum)")
	fmt.Println("  --database <name>    Database name (default: spiralstratum)")
	fmt.Println()
	fmt.Printf("%sEnvironment Variables:%s\n", ColorBold, ColorReset)
	fmt.Println("  SPIRAL_DATABASE_PASSWORD   Database password (required, for security)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  export SPIRAL_DATABASE_PASSWORD='your-password'")
	fmt.Println("  spiralctl ha enable --primary 192.168.1.10 --replica 192.168.1.11")
	fmt.Println("  spiralctl ha disable")
	fmt.Println("  spiralctl ha status")
	fmt.Println("  spiralctl ha failover")
	fmt.Println()
	fmt.Printf("%sNote:%s HA requires PostgreSQL streaming replication configured\n", ColorYellow, ColorReset)
	fmt.Println("between primary and replica servers.")
	fmt.Println()
}

func enableHA(primary, replica string, port int, user, password, dbName string) error {
	printBanner()
	fmt.Printf("%s=== ENABLE HIGH AVAILABILITY ===%s\n\n", ColorBold, ColorReset)

	if primary == "" || replica == "" {
		return fmt.Errorf("both --primary and --replica are required")
	}

	// SECURITY: Password must be provided via environment variable
	if password == "" {
		return fmt.Errorf("SPIRAL_DATABASE_PASSWORD environment variable is required")
	}

	printInfo(fmt.Sprintf("Primary DB:  %s:%d", primary, port))
	printInfo(fmt.Sprintf("Replica DB:  %s:%d", replica, port))
	fmt.Println()

	// Verify connectivity to both databases
	printInfo("Verifying database connectivity...")

	if !testDBConnection(primary, port, user, password, dbName) {
		return fmt.Errorf("cannot connect to primary database at %s:%d", primary, port)
	}
	printSuccess("Primary database connection OK")

	if !testDBConnection(replica, port, user, password, dbName) {
		return fmt.Errorf("cannot connect to replica database at %s:%d", replica, port)
	}
	printSuccess("Replica database connection OK")

	// Load and update config
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	cfg.HA.Enabled = true
	cfg.HA.PrimaryHost = primary
	cfg.HA.ReplicaHost = replica
	cfg.HA.CheckInterval = "5s"
	cfg.HA.FailoverTimeout = "30s"

	cfg.Database.Host = primary
	cfg.Database.Port = port
	cfg.Database.User = user
	cfg.Database.Password = password
	cfg.Database.Database = dbName

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("High Availability enabled")
	printInfo("Restart spiralstratum service for changes to take effect")

	return nil
}

func disableHA() error {
	printBanner()
	fmt.Printf("%s=== DISABLE HIGH AVAILABILITY ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !cfg.HA.Enabled {
		printInfo("HA is already disabled")
		return nil
	}

	cfg.HA.Enabled = false

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess("High Availability disabled")
	printInfo("Restart spiralstratum service for changes to take effect")

	return nil
}

func haStatus() error {
	printBanner()
	fmt.Printf("%s=== HIGH AVAILABILITY STATUS ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		printWarning(fmt.Sprintf("Could not load config: %v", err))
		return nil
	}

	if !cfg.HA.Enabled {
		fmt.Printf("HA Status:     %sDISABLED%s\n", ColorYellow, ColorReset)
		fmt.Println()
		fmt.Println("To enable HA, run:")
		fmt.Println("  spiralctl ha enable --primary <host> --replica <host>")
		return nil
	}

	fmt.Printf("HA Status:     %sENABLED%s\n", ColorGreen, ColorReset)
	fmt.Printf("Primary DB:    %s\n", cfg.HA.PrimaryHost)
	fmt.Printf("Replica DB:    %s\n", cfg.HA.ReplicaHost)
	fmt.Printf("Check Interval: %s\n", cfg.HA.CheckInterval)
	fmt.Printf("Failover Timeout: %s\n", cfg.HA.FailoverTimeout)
	fmt.Println()

	// Check current connection
	fmt.Println("Connection Status:")
	if testDBConnection(cfg.HA.PrimaryHost, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.Database) {
		fmt.Printf("  Primary:     %sONLINE%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("  Primary:     %sOFFLINE%s\n", ColorRed, ColorReset)
	}

	if testDBConnection(cfg.HA.ReplicaHost, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.Database) {
		fmt.Printf("  Replica:     %sONLINE%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("  Replica:     %sOFFLINE%s\n", ColorRed, ColorReset)
	}

	fmt.Println()
	return nil
}

func forceFailover() error {
	printBanner()
	fmt.Printf("%s=== FORCE FAILOVER ===%s\n\n", ColorBold, ColorReset)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if !cfg.HA.Enabled {
		return fmt.Errorf("HA is not enabled. Enable it first with: spiralctl ha enable")
	}

	printWarning("This will force a failover from primary to replica database.")
	printWarning("Only use this if the primary is confirmed to be down.")
	fmt.Println()

	if !confirmAction("Force failover to replica?") {
		printInfo("Failover cancelled")
		return nil
	}

	// Swap primary and replica
	oldPrimary := cfg.HA.PrimaryHost
	cfg.HA.PrimaryHost = cfg.HA.ReplicaHost
	cfg.HA.ReplicaHost = oldPrimary
	cfg.Database.Host = cfg.HA.PrimaryHost

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printSuccess(fmt.Sprintf("Failover complete. New primary: %s", cfg.HA.PrimaryHost))
	printInfo("Restart spiralstratum service for changes to take effect")

	// Try to restart the service
	if isServiceRunning("spiralstratum") {
		printInfo("Restarting spiralstratum service...")
		_ = exec.Command("systemctl", "restart", "spiralstratum").Run() // #nosec G104
	}

	return nil
}

func testDBConnection(host string, port int, user, password, dbName string) bool {
	// SECURITY: Use environment variable for password to prevent command injection
	// and exposure in process listings. psql respects PGPASSWORD env var.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "psql",
		"-h", host,
		"-p", fmt.Sprintf("%d", port),
		"-U", user,
		"-d", dbName,
		"-c", "SELECT 1",
		"--no-password", // Don't prompt, use PGPASSWORD
	)

	// Pass password via environment variable (not visible in process listing)
	cmd.Env = append(cmd.Environ(), "PGPASSWORD="+password)

	return cmd.Run() == nil
}
