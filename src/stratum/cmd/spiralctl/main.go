// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Spiral Pool Configuration Tool (spiralctl)
//
// A unified command-line tool for configuring and managing Spiral Pool installations.
// Consolidates all configuration operations into a single, easy-to-use interface.
//
// Usage:
//
//	spiralctl <command> [options]
//
// Commands:
//
//	status      Show current pool status and configuration
//	mining      Mining mode management (solo/multi/merge)
//	coin        View coin status and disable coins
//	node        Manage blockchain nodes (start/stop/restart/logs/status)
//	tor         Enable/disable Tor routing for blockchain nodes
//	ha          Enable/disable High Availability (database failover)
//	vip         Enable/disable VIP (Virtual IP) for miner failover
//	config      Configuration management (validate)
//	pool        Pool statistics and management
//	external    External access for hashrate rental services
//	version     Show version information
//	help        Show help message
//
// External Access Subcommands:
//
//	external setup      Interactive wizard to configure external access
//	external enable     Enable external access using saved configuration
//	external disable    Disable external access
//	external status     Show current external access status
//	external test       Test external connectivity
//
// Supported Coins:
//
//	SHA-256d: btc, bch, dgb, bc2, nmc, sys, xmy, fbtc, qbx
//	Scrypt:   ltc, doge, dgb-scrypt, pep, cat
//
// Merge Mining (AuxPoW):
//
//	SHA-256d: BTC can merge-mine NMC, SYS, XMY, FBTC
//	Scrypt:   LTC can merge-mine DOGE, PEP
//
// Examples:
//
//	spiralctl status
//	spiralctl mining solo dgb
//	spiralctl mining multi btc,bch,dgb
//	spiralctl mining merge enable nmc,sys,fbtc
//	spiralctl tor enable --node btc
//	spiralctl ha enable --primary 192.168.1.10 --replica 192.168.1.11
//	spiralctl vip enable --address 192.168.1.200 --interface ens33
//	spiralctl config validate
//	spiralctl pool stats
//	spiralctl node restart all
//
// External Access (Hashrate Rental Services):
//
//	spiralctl external setup                 # Configure external access
//	spiralctl external setup --mode tunnel   # Use Cloudflare tunnel
//	spiralctl external enable                # Enable external access
//	spiralctl external disable               # Disable external access
//	spiralctl external status                # Show external access status
//	spiralctl external test                  # Test external connectivity
//
// See LICENSE file for full BSD-3-Clause license terms.
package main

import (
	"fmt"
	"os"

	"github.com/spiralpool/stratum/cmd/spiralctl/cmd"
)

var (
	Version   = "1.1.1"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	// Set version info for subcommands
	cmd.Version = Version
	cmd.BuildTime = BuildTime
	cmd.GitCommit = GitCommit

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
