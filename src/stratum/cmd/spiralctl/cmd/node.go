// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runNode(args []string) error {
	if err := ensureRoot(); err != nil {
		return err
	}

	fs := flag.NewFlagSet("node", flag.ExitOnError)

	if len(args) < 1 {
		printNodeUsage()
		return nil
	}

	action := args[0]
	if len(args) > 1 {
		_ = fs.Parse(args[1:]) // #nosec G104
	}

	switch action {
	case "start":
		if len(args) < 2 {
			return fmt.Errorf("node symbol required. Usage: spiralctl node start <btc|bch|dgb|bc2|nmc|sys|xmy|fbtc|ltc|doge|dgb-scrypt|pep|cat|all>")
		}
		return startNode(args[1])
	case "stop":
		if len(args) < 2 {
			return fmt.Errorf("node symbol required. Usage: spiralctl node stop <btc|bch|dgb|bc2|nmc|sys|xmy|fbtc|ltc|doge|dgb-scrypt|pep|cat|all>")
		}
		return stopNode(args[1])
	case "restart":
		if len(args) < 2 {
			return fmt.Errorf("node symbol required. Usage: spiralctl node restart <btc|bch|dgb|bc2|nmc|sys|xmy|fbtc|ltc|doge|dgb-scrypt|pep|cat|all>")
		}
		return restartNode(args[1])
	case "logs":
		if len(args) < 2 {
			return fmt.Errorf("node symbol required. Usage: spiralctl node logs <btc|bch|dgb|bc2|nmc|sys|xmy|fbtc|ltc|doge|dgb-scrypt|pep|cat>")
		}
		return showNodeLogs(args[1])
	case "status":
		return nodeStatus()
	default:
		printNodeUsage()
		return fmt.Errorf("unknown action: %s", action)
	}
}

func printNodeUsage() {
	fmt.Println("Usage: spiralctl node <action> [node]")
	fmt.Println()
	fmt.Println("Actions:")
	fmt.Println("  start    Start a blockchain node service")
	fmt.Println("  stop     Stop a blockchain node service")
	fmt.Println("  restart  Restart a blockchain node service")
	fmt.Println("  logs     View logs for a blockchain node")
	fmt.Println("  status   Show status of all installed nodes")
	fmt.Println()
	fmt.Println("Nodes (SHA-256d):")
	fmt.Println("  btc        Bitcoin Knots")
	fmt.Println("  bch        Bitcoin Cash Node")
	fmt.Println("  dgb        DigiByte")
	fmt.Println("  bc2        Bitcoin II")
	fmt.Println("  nmc        Namecoin (AuxPoW)")
	fmt.Println("  sys        Syscoin (AuxPoW)")
	fmt.Println("  xmy        Myriad (AuxPoW)")
	fmt.Println("  fbtc       Fractal Bitcoin (AuxPoW)")
	fmt.Println()
	fmt.Println("Nodes (Scrypt):")
	fmt.Println("  ltc        Litecoin")
	fmt.Println("  doge       Dogecoin (AuxPoW)")
	fmt.Println("  dgb-scrypt DigiByte (Scrypt)")
	fmt.Println("  pep        PepeCoin (AuxPoW)")
	fmt.Println("  cat        Catcoin")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  all        All installed nodes")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  spiralctl node start btc")
	fmt.Println("  spiralctl node restart all")
	fmt.Println("  spiralctl node logs dgb")
	fmt.Println("  spiralctl node status")
	fmt.Println()
}

type nodeService struct {
	symbol  string
	name    string
	service string
	config  string
}

func getNodeServices() []nodeService {
	return []nodeService{
		// Alphabetically ordered (no coin preference)
		{"bc2", "Bitcoin II", "bitcoiniid", DefaultBC2Config},
		{"bch", "Bitcoin Cash", "bitcoind-bch", DefaultBCHConfig},
		{"btc", "Bitcoin Knots", "bitcoind", DefaultBTCConfig},
		{"cat", "Catcoin", "catcoind", DefaultCATConfig},
		{"dgb", "DigiByte", "digibyted", DefaultDGBConfig},
		{"dgb-scrypt", "DigiByte-Scrypt", "digibyted-scrypt", DefaultDGBScryptConfig},
		{"doge", "Dogecoin", "dogecoind", DefaultDOGEConfig},
		{"fbtc", "Fractal Bitcoin", "fractald", DefaultFBTCConfig},
		{"ltc", "Litecoin", "litecoind", DefaultLTCConfig},
		{"nmc", "Namecoin", "namecoind", DefaultNMCConfig},
		{"pep", "PepeCoin", "pepecoind", DefaultPEPConfig},
		{"sys", "Syscoin", "syscoind", DefaultSYSConfig},
		{"xmy", "Myriad", "myriadcoind", DefaultXMYConfig},
	}
}

func getNodeBySymbol(symbol string) *nodeService {
	symbol = strings.ToLower(symbol)
	for _, n := range getNodeServices() {
		if n.symbol == symbol {
			return &n
		}
	}
	return nil
}

func startNode(symbol string) error {
	printBanner()
	fmt.Printf("%s=== START NODE ===%s\n\n", ColorBold, ColorReset)

	if symbol == "all" {
		return startAllNodes()
	}

	node := getNodeBySymbol(symbol)
	if node == nil {
		return fmt.Errorf("unknown node: %s. Valid options: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, ltc, doge, dgb-scrypt, pep, cat, all", symbol)
	}

	if !fileExists(node.config) {
		return fmt.Errorf("%s is not installed", node.name)
	}

	printInfo(fmt.Sprintf("Starting %s...", node.name))

	cmd := exec.Command("systemctl", "start", node.service)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start %s: %w\n%s", node.name, err, output)
	}

	printSuccess(fmt.Sprintf("%s started", node.name))
	return nil
}

func startAllNodes() error {
	for _, node := range getNodeServices() {
		if !fileExists(node.config) {
			continue
		}

		printInfo(fmt.Sprintf("Starting %s...", node.name))
		cmd := exec.Command("systemctl", "start", node.service)
		if output, err := cmd.CombinedOutput(); err != nil {
			printError(fmt.Sprintf("Failed to start %s: %v\n%s", node.name, err, output))
		} else {
			printSuccess(fmt.Sprintf("%s started", node.name))
		}
	}
	return nil
}

func stopNode(symbol string) error {
	printBanner()
	fmt.Printf("%s=== STOP NODE ===%s\n\n", ColorBold, ColorReset)

	if symbol == "all" {
		return stopAllNodes()
	}

	node := getNodeBySymbol(symbol)
	if node == nil {
		return fmt.Errorf("unknown node: %s. Valid options: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, ltc, doge, dgb-scrypt, pep, cat, all", symbol)
	}

	if !fileExists(node.config) {
		return fmt.Errorf("%s is not installed", node.name)
	}

	printInfo(fmt.Sprintf("Stopping %s...", node.name))

	cmd := exec.Command("systemctl", "stop", node.service)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stop %s: %w\n%s", node.name, err, output)
	}

	printSuccess(fmt.Sprintf("%s stopped", node.name))
	return nil
}

func stopAllNodes() error {
	for _, node := range getNodeServices() {
		if !fileExists(node.config) {
			continue
		}

		printInfo(fmt.Sprintf("Stopping %s...", node.name))
		cmd := exec.Command("systemctl", "stop", node.service)
		if output, err := cmd.CombinedOutput(); err != nil {
			printError(fmt.Sprintf("Failed to stop %s: %v\n%s", node.name, err, output))
		} else {
			printSuccess(fmt.Sprintf("%s stopped", node.name))
		}
	}
	return nil
}

func restartNode(symbol string) error {
	printBanner()
	fmt.Printf("%s=== RESTART NODE ===%s\n\n", ColorBold, ColorReset)

	if symbol == "all" {
		return restartAllNodes()
	}

	node := getNodeBySymbol(symbol)
	if node == nil {
		return fmt.Errorf("unknown node: %s. Valid options: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, ltc, doge, dgb-scrypt, pep, cat, all", symbol)
	}

	if !fileExists(node.config) {
		return fmt.Errorf("%s is not installed", node.name)
	}

	printInfo(fmt.Sprintf("Restarting %s...", node.name))

	cmd := exec.Command("systemctl", "restart", node.service)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to restart %s: %w\n%s", node.name, err, output)
	}

	printSuccess(fmt.Sprintf("%s restarted", node.name))
	return nil
}

func restartAllNodes() error {
	for _, node := range getNodeServices() {
		if !fileExists(node.config) {
			continue
		}

		printInfo(fmt.Sprintf("Restarting %s...", node.name))
		cmd := exec.Command("systemctl", "restart", node.service)
		if output, err := cmd.CombinedOutput(); err != nil {
			printError(fmt.Sprintf("Failed to restart %s: %v\n%s", node.name, err, output))
		} else {
			printSuccess(fmt.Sprintf("%s restarted", node.name))
		}
	}
	return nil
}

func showNodeLogs(symbol string) error {
	node := getNodeBySymbol(symbol)
	if node == nil {
		return fmt.Errorf("unknown node: %s. Valid options: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, ltc, doge, dgb-scrypt, pep, cat", symbol)
	}

	if !fileExists(node.config) {
		return fmt.Errorf("%s is not installed", node.name)
	}

	printInfo(fmt.Sprintf("Showing logs for %s (Ctrl+C to exit)...", node.name))
	fmt.Println()

	cmd := exec.Command("journalctl", "-u", node.service, "-f", "--no-pager", "-n", "50")
	// FIX: Connect child process output to terminal. Setting nil sends to /dev/null.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func nodeStatus() error {
	printBanner()
	fmt.Printf("%s=== NODE STATUS ===%s\n\n", ColorBold, ColorReset)

	fmt.Printf("%-18s %-12s %-10s %-10s\n", "Node", "Status", "PID", "Memory")
	fmt.Println(strings.Repeat("-", 55))

	for _, node := range getNodeServices() {
		if !fileExists(node.config) {
			continue
		}

		status := "Stopped"
		statusColor := ColorRed
		pid := "-"
		memory := "-"

		if isServiceRunning(node.service) {
			status = "Running"
			statusColor = ColorGreen

			// Get PID
			// G204: node.service is from internal getNodeServices() - validated values only
			output, _ := exec.Command("systemctl", "show", node.service, "--property=MainPID", "--value").Output() // #nosec G204
			pid = strings.TrimSpace(string(output))

			// Get memory usage
			if pid != "" && pid != "0" {
				// G204: pid is from systemctl output, not user input
				memOutput, _ := exec.Command("ps", "-p", pid, "-o", "rss=").Output() // #nosec G204
				memKB := strings.TrimSpace(string(memOutput))
				if memKB != "" {
					var memMB int
					_, _ = fmt.Sscanf(memKB, "%d", &memMB) // #nosec G104
					memMB = memMB / 1024
					if memMB > 1024 {
						memory = fmt.Sprintf("%.1f GB", float64(memMB)/1024)
					} else {
						memory = fmt.Sprintf("%d MB", memMB)
					}
				}
			}
		}

		fmt.Printf("%-18s %s%-12s%s %-10s %-10s\n",
			node.name, statusColor, status, ColorReset, pid, memory)
	}

	fmt.Println()

	// Show pool status too
	fmt.Printf("%-18s ", "Spiral Pool")
	if isServiceRunning("spiralstratum") {
		fmt.Printf("%sRunning%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("%sStopped%s\n", ColorRed, ColorReset)
	}

	fmt.Println()
	return nil
}
