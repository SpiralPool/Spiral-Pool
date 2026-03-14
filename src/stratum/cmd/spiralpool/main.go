// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Spiral Stratum Pool - High-Performance Mining Pool
//
// A Go implementation of a cryptocurrency mining pool supporting Stratum V1/V2
// protocols for SHA256d and Scrypt coins.
//
// See LICENSE file for full BSD-3-Clause license terms.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/pool"
	"go.uber.org/zap"
)

var (
	Version   = "1.0.0"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	// Command-line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Show version information")
	forceV1 := flag.Bool("v1", false, "Force V1 single-coin mode (ignore V2 config)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Spiral Stratum Pool %s\n", Version)
		fmt.Printf("Build Time: %s\n", BuildTime)
		fmt.Printf("Git Commit: %s\n", GitCommit)
		fmt.Printf("\nFeatures:\n")
		fmt.Printf("  - Multi-coin support (BTC, BCH, BC2, DGB, DGB-SCRYPT, NMC, SYS, XMY, FBTC, QBX, LTC, DOGE, PEP, CAT)\n")
		fmt.Printf("  - Multi-node failover per coin\n")
		fmt.Printf("  - Health-based automatic failover\n")
		fmt.Printf("  - ZMQ block notifications with RPC fallback\n")
		os.Exit(0)
	}

	// Initialize logger
	// V46 FIX: Disable zap production sampling to prevent critical log lines from being dropped.
	// zap.NewProduction() enables sampling by default (100 initial, then 1-in-100),
	// which can silently drop block submission, reorg, and payment failure logs
	// during high-activity periods — exactly when they matter most.
	zapCfg := zap.NewProductionConfig()
	zapCfg.Sampling = nil // Disable sampling — block events must never be dropped
	logger, err := zapCfg.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	log := logger.Sugar()
	log.Infow("Starting Spiral Stratum Pool",
		"version", Version,
		"config", *configPath,
	)

	// Try to load as V2 config first (unless forced V1)
	if !*forceV1 {
		cfgV2, v2Err := config.LoadV2(*configPath)
		if v2Err == nil {
			log.Infow("Loaded V2 configuration",
				"coins", len(cfgV2.Coins),
			)
			runV2(cfgV2, logger, log)
			return
		}
		log.Debugw("V2 config loading failed, falling back to V1",
			"error", v2Err,
			"config", *configPath,
		)
	}

	// Fall back to V1 config loading
	log.Info("Loading V1 configuration...")
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalw("Failed to load configuration", "error", err)
	}

	runV1(cfg, logger, log)
}

// runV1 runs the pool in V1 single-coin mode.
func runV1(cfg *config.Config, logger *zap.Logger, log *zap.SugaredLogger) {
	log.Infow("Running in V1 mode (single coin)",
		"coin", cfg.Pool.Coin,
		"poolId", cfg.Pool.ID,
	)

	// Create and start pool
	p, err := pool.New(cfg, logger)
	if err != nil {
		log.Fatalw("Failed to create pool", "error", err)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Start pool in background
	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Run(ctx)
	}()

	// Wait for shutdown signal or error
	for {
		select {
		case sig := <-sigChan:
			if sig == syscall.SIGHUP {
				log.Infow("Received SIGHUP — live config reload is not supported, restart the service to apply config changes")
				continue
			}
			log.Infow("Received shutdown signal", "signal", sig)
			cancel()
			if err := <-errChan; err != nil {
				log.Errorw("Pool shutdown with error", "error", err)
			}
			goto shutdown
		case err := <-errChan:
			if err != nil {
				log.Fatalw("Pool exited with error", "error", err)
			}
			goto shutdown
		}
	}
shutdown:

	log.Info("Spiral Stratum Pool stopped")
}

// runV2 runs the pool in V2 multi-coin mode.
func runV2(cfg *config.ConfigV2, logger *zap.Logger, log *zap.SugaredLogger) {
	log.Infow("Running in V2 mode (multi-coin)",
		"coins", len(cfg.Coins),
	)

	// List enabled coins
	for _, coin := range cfg.Coins {
		if coin.Enabled {
			log.Infow("Coin configured",
				"symbol", coin.Symbol,
				"poolId", coin.PoolID,
				"nodes", len(coin.Nodes),
				"port", coin.Stratum.Port,
			)
		}
	}

	// Create coordinator
	coord, err := pool.NewCoordinator(cfg, logger)
	if err != nil {
		log.Fatalw("Failed to create coordinator", "error", err)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Start coordinator in background
	errChan := make(chan error, 1)
	go func() {
		errChan <- coord.Run(ctx)
	}()

	// Wait for shutdown signal or error
	for {
		select {
		case sig := <-sigChan:
			if sig == syscall.SIGHUP {
				log.Infow("Received SIGHUP — live config reload is not supported, restart the service to apply config changes")
				continue
			}
			log.Infow("Received shutdown signal", "signal", sig)
			cancel()
			if err := <-errChan; err != nil {
				log.Errorw("Coordinator shutdown with error", "error", err)
			}
			goto shutdownV2
		case err := <-errChan:
			if err != nil {
				log.Fatalw("Coordinator exited with error", "error", err)
			}
			goto shutdownV2
		}
	}
shutdownV2:

	log.Info("Spiral Stratum Pool stopped")
}
