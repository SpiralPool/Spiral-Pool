// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxResponseSize limits API response body size to prevent memory exhaustion (10MB)
const maxResponseSize = 10 * 1024 * 1024

// secureHTTPClient creates an HTTP client with appropriate timeouts and security settings.
// Accepts self-signed TLS certificates because spiralctl only connects to localhost
// where the dashboard uses a self-signed cert generated during installation.
func secureHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // #nosec G402 — localhost only, self-signed dashboard cert
			},
		},
		// Disable redirects to prevent SSRF - we only connect to localhost
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// limitedReader wraps a reader with size limits
func limitedReader(r io.Reader) io.Reader {
	return io.LimitReader(r, maxResponseSize)
}

func runPool(args []string) error {
	if len(args) < 1 {
		printPoolUsage()
		return nil
	}

	switch args[0] {
	case "stats":
		return runPoolStats(args[1:])
	case "help", "-h", "--help":
		printPoolUsage()
		return nil
	default:
		return fmt.Errorf("unknown pool command: %s", args[0])
	}
}

func printPoolUsage() {
	fmt.Println("Usage: spiralctl pool <command>")
	fmt.Println()
	fmt.Printf("%sCommands:%s\n", ColorBold, ColorReset)
	fmt.Printf("  %sstats%s        Show pool statistics\n", ColorCyan, ColorReset)
	fmt.Println()
	fmt.Printf("%sExamples:%s\n", ColorBold, ColorReset)
	fmt.Println("  spiralctl pool stats")
	fmt.Println("  spiralctl pool stats --json")
	fmt.Println()
}

// PoolStats represents the pool statistics from the API
type PoolStats struct {
	PoolHashrate    float64 `json:"pool_hashrate"`
	ConnectedMiners int     `json:"connected_miners"`
	SharesPerSecond float64 `json:"shares_per_second"`
	BlocksFound     int     `json:"blocks_found"`
	NetworkDiff     float64 `json:"network_difficulty"`
	LastBlockTime   int64   `json:"last_block_time"`
	LastBlockHeight int64   `json:"last_block_height"`
	LastBlockFinder string  `json:"last_block_finder"`
}

// MinersTotals from the miners API
type MinersTotals struct {
	HashrateTHS    float64 `json:"hashrate_ths"`
	PowerWatts     float64 `json:"power_watts"`
	AcceptedShares int64   `json:"accepted_shares"`
	RejectedShares int64   `json:"rejected_shares"`
	BlocksFound    int     `json:"blocks_found"`
	OnlineCount    int     `json:"online_count"`
	TotalCount     int     `json:"total_count"`
}

// MinersAPIResponse from /api/miners
type MinersAPIResponse struct {
	Miners          map[string]interface{} `json:"miners"`
	Totals          MinersTotals           `json:"totals"`
	NetworkDiff     float64                `json:"network_difficulty"`
	LastBlockFinder string                 `json:"last_block_finder"`
	LastBlockHeight int64                  `json:"last_block_height"`
	LastBlockTime   interface{}            `json:"last_block_time"`
	PoolHashrate    float64                `json:"pool_hashrate"`
	Coin            string                 `json:"coin"`
	CoinName        string                 `json:"coin_name"`
}

// ETBResponse from /api/etb
type ETBResponse struct {
	Success bool `json:"success"`
	ETB     struct {
		EstimatedSeconds   *float64 `json:"estimated_seconds"`
		EstimatedFormatted string   `json:"estimated_formatted"`
		CurrentHashrateTHS float64  `json:"current_hashrate_ths"`
		NetworkDifficulty  float64  `json:"network_difficulty"`
	} `json:"etb"`
	Probability struct {
		Day24 float64 `json:"24h"`
		Day7  float64 `json:"7d"`
		Day30 float64 `json:"30d"`
	} `json:"probability"`
}

func runPoolStats(args []string) error {
	jsonOutput := false
	for _, arg := range args {
		if arg == "--json" || arg == "-j" {
			jsonOutput = true
		}
	}

	// Load config to get API port
	cfg, err := loadConfig()
	if err != nil {
		cfg = &Config{} // Use defaults
	}

	apiPort := cfg.Global.APIPort
	if apiPort == 0 {
		apiPort = 4000
	}
	// SECURITY: Validate port range to prevent unexpected behavior
	if apiPort < 1 || apiPort > 65535 {
		return fmt.Errorf("invalid API port in config: %d (must be 1-65535)", apiPort)
	}

	// Also try dashboard port for stats
	dashboardPort := 1618
	// Note: dashboardPort is hardcoded and known-valid, no need to validate

	// Fetch pool stats from APIs
	stats, err := fetchPoolStats(apiPort, dashboardPort)
	if err != nil {
		return fmt.Errorf("failed to fetch pool stats: %w", err)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(stats, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Pretty print the stats
	printPoolStats(stats)
	return nil
}

// dashboardBaseURL returns the dashboard URL, trying HTTPS first (self-signed cert),
// falling back to HTTP. Only connects to localhost (127.0.0.1).
func dashboardBaseURL(port int) string {
	client := secureHTTPClient()
	httpsURL := fmt.Sprintf("https://127.0.0.1:%d/api/health/live", port)
	resp, err := client.Get(httpsURL)
	if err == nil {
		resp.Body.Close()
		return fmt.Sprintf("https://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func fetchPoolStats(apiPort, dashboardPort int) (*PoolStatsResult, error) {
	client := secureHTTPClient()
	result := &PoolStatsResult{}

	// Try to get stats from dashboard API (more comprehensive)
	// Only connect to localhost (127.0.0.1) to prevent SSRF
	// Auto-detects HTTPS (self-signed cert) vs HTTP
	dashBase := dashboardBaseURL(dashboardPort)
	dashURL := fmt.Sprintf("%s/api/miners", dashBase)
	resp, err := client.Get(dashURL)
	if err == nil {
		var miners MinersAPIResponse
		// Use limited reader to prevent memory exhaustion from malicious response
		if json.NewDecoder(limitedReader(resp.Body)).Decode(&miners) == nil {
			result.HashrateTHS = miners.Totals.HashrateTHS
			result.AcceptedShares = miners.Totals.AcceptedShares
			result.RejectedShares = miners.Totals.RejectedShares
			result.BlocksFound = miners.Totals.BlocksFound
			result.OnlineMiners = miners.Totals.OnlineCount
			result.TotalMiners = miners.Totals.TotalCount
			result.PowerWatts = miners.Totals.PowerWatts
			result.NetworkDifficulty = miners.NetworkDiff
			result.LastBlockFinder = miners.LastBlockFinder
			result.LastBlockHeight = miners.LastBlockHeight
			result.PoolHashrate = miners.PoolHashrate
			result.Coin = miners.Coin
			result.CoinName = miners.CoinName

			// Handle last_block_time which could be int or string
			switch v := miners.LastBlockTime.(type) {
			case float64:
				result.LastBlockTime = int64(v)
			case int64:
				result.LastBlockTime = v
			}
		}
		resp.Body.Close()
	}

	// Try to get ETB stats
	etbURL := fmt.Sprintf("%s/api/etb", dashBase)
	resp, err = client.Get(etbURL)
	if err == nil {
		var etb ETBResponse
		if json.NewDecoder(limitedReader(resp.Body)).Decode(&etb) == nil && etb.Success {
			result.ETBFormatted = etb.ETB.EstimatedFormatted
			result.Probability24h = etb.Probability.Day24
			result.Probability7d = etb.Probability.Day7
			result.Probability30d = etb.Probability.Day30
		}
		resp.Body.Close()
	}

	// Try stratum API for additional stats
	poolURL := fmt.Sprintf("http://127.0.0.1:%d/api/pools", apiPort)
	resp, err = client.Get(poolURL)
	if err == nil {
		var pools []map[string]interface{}
		if json.NewDecoder(limitedReader(resp.Body)).Decode(&pools) == nil && len(pools) > 0 {
			pool := pools[0]
			if result.PoolHashrate == 0 {
				if hr, ok := pool["hashrate"].(float64); ok {
					result.PoolHashrate = hr
				}
			}
			if workers, ok := pool["workers"].(float64); ok && result.OnlineMiners == 0 {
				result.OnlineMiners = int(workers)
			}
			if blocks, ok := pool["blocks"].(float64); ok && result.BlocksFound == 0 {
				result.BlocksFound = int(blocks)
			}
		}
		resp.Body.Close()
	}

	return result, nil
}

// PoolStatsResult combines all pool statistics
type PoolStatsResult struct {
	// Pool Info
	Coin     string `json:"coin"`
	CoinName string `json:"coin_name"`

	// Hashrate
	HashrateTHS  float64 `json:"hashrate_ths"`
	PoolHashrate float64 `json:"pool_hashrate"`

	// Miners
	OnlineMiners int `json:"online_miners"`
	TotalMiners  int `json:"total_miners"`

	// Shares
	AcceptedShares int64 `json:"accepted_shares"`
	RejectedShares int64 `json:"rejected_shares"`

	// Blocks
	BlocksFound     int    `json:"blocks_found"`
	LastBlockTime   int64  `json:"last_block_time"`
	LastBlockHeight int64  `json:"last_block_height"`
	LastBlockFinder string `json:"last_block_finder"`

	// Network
	NetworkDifficulty float64 `json:"network_difficulty"`

	// Power
	PowerWatts float64 `json:"power_watts"`

	// ETB (Estimated Time to Block)
	ETBFormatted   string  `json:"etb_formatted"`
	Probability24h float64 `json:"probability_24h"`
	Probability7d  float64 `json:"probability_7d"`
	Probability30d float64 `json:"probability_30d"`
}

func printPoolStats(stats *PoolStatsResult) {
	printBanner()
	fmt.Printf("%s=== POOL STATISTICS ===%s\n\n", ColorBold, ColorReset)

	// Pool Info
	fmt.Printf("%s[Pool Information]%s\n", ColorCyan, ColorReset)
	if stats.CoinName != "" {
		fmt.Printf("  Coin:              %s (%s)\n", stats.CoinName, strings.ToUpper(stats.Coin))
	} else if stats.Coin != "" {
		fmt.Printf("  Coin:              %s\n", strings.ToUpper(stats.Coin))
	}
	fmt.Println()

	// Hashrate
	fmt.Printf("%s[Hashrate]%s\n", ColorCyan, ColorReset)
	if stats.HashrateTHS > 0 {
		if stats.HashrateTHS >= 1000 {
			fmt.Printf("  Farm Hashrate:     %s%.2f PH/s%s\n", ColorGreen, stats.HashrateTHS/1000, ColorReset)
		} else {
			fmt.Printf("  Farm Hashrate:     %s%.2f TH/s%s\n", ColorGreen, stats.HashrateTHS, ColorReset)
		}
	}
	if stats.PoolHashrate > 0 {
		poolHR := stats.PoolHashrate
		if poolHR >= 1e15 {
			fmt.Printf("  Pool Hashrate:     %.2f PH/s\n", poolHR/1e15)
		} else if poolHR >= 1e12 {
			fmt.Printf("  Pool Hashrate:     %.2f TH/s\n", poolHR/1e12)
		} else if poolHR >= 1e9 {
			fmt.Printf("  Pool Hashrate:     %.2f GH/s\n", poolHR/1e9)
		} else {
			fmt.Printf("  Pool Hashrate:     %.2f H/s\n", poolHR)
		}
	}
	fmt.Println()

	// Miners
	fmt.Printf("%s[Miners]%s\n", ColorCyan, ColorReset)
	if stats.TotalMiners > 0 {
		fmt.Printf("  Online:            %s%d%s / %d\n", ColorGreen, stats.OnlineMiners, ColorReset, stats.TotalMiners)
	} else {
		fmt.Printf("  Connected:         %d\n", stats.OnlineMiners)
	}
	if stats.PowerWatts > 0 {
		if stats.PowerWatts >= 1000 {
			fmt.Printf("  Power Usage:       %.2f kW\n", stats.PowerWatts/1000)
		} else {
			fmt.Printf("  Power Usage:       %.0f W\n", stats.PowerWatts)
		}
		if stats.HashrateTHS > 0 {
			efficiency := stats.PowerWatts / stats.HashrateTHS
			fmt.Printf("  Efficiency:        %.2f W/TH\n", efficiency)
		}
	}
	fmt.Println()

	// Shares
	fmt.Printf("%s[Shares]%s\n", ColorCyan, ColorReset)
	totalShares := stats.AcceptedShares + stats.RejectedShares
	if totalShares > 0 {
		acceptRate := float64(stats.AcceptedShares) / float64(totalShares) * 100
		fmt.Printf("  Accepted:          %s%s%s\n", ColorGreen, formatLargeNumber(stats.AcceptedShares), ColorReset)
		fmt.Printf("  Rejected:          %s%s%s\n", ColorRed, formatLargeNumber(stats.RejectedShares), ColorReset)
		fmt.Printf("  Accept Rate:       %.2f%%\n", acceptRate)
	} else {
		fmt.Printf("  Accepted:          0\n")
		fmt.Printf("  Rejected:          0\n")
	}
	fmt.Println()

	// Blocks
	fmt.Printf("%s[Blocks]%s\n", ColorCyan, ColorReset)
	fmt.Printf("  Blocks Found:      %s%d%s\n", ColorMagenta, stats.BlocksFound, ColorReset)
	if stats.LastBlockTime > 0 {
		blockTime := time.Unix(stats.LastBlockTime, 0)
		timeAgo := time.Since(blockTime)
		fmt.Printf("  Last Block:        %s ago\n", formatDuration(timeAgo))
		if stats.LastBlockHeight > 0 {
			fmt.Printf("  Last Block Height: #%d\n", stats.LastBlockHeight)
		}
		if stats.LastBlockFinder != "" {
			fmt.Printf("  Found By:          %s\n", stats.LastBlockFinder)
		}
	}
	fmt.Println()

	// ETB / Probability
	if stats.ETBFormatted != "" {
		fmt.Printf("%s[Estimated Time to Block]%s\n", ColorCyan, ColorReset)
		fmt.Printf("  ETB:               %s%s%s\n", ColorYellow, stats.ETBFormatted, ColorReset)
		if stats.Probability24h > 0 {
			fmt.Printf("  24h Probability:   %.2f%%\n", stats.Probability24h)
		}
		if stats.Probability7d > 0 {
			fmt.Printf("  7d Probability:    %.2f%%\n", stats.Probability7d)
		}
		if stats.Probability30d > 0 {
			fmt.Printf("  30d Probability:   %.2f%%\n", stats.Probability30d)
		}
		fmt.Println()
	}

	// Network
	if stats.NetworkDifficulty > 0 {
		fmt.Printf("%s[Network]%s\n", ColorCyan, ColorReset)
		fmt.Printf("  Difficulty:        %.2e\n", stats.NetworkDifficulty)
		fmt.Println()
	}
}

func formatLargeNumber(n int64) string {
	if n >= 1000000000 {
		return fmt.Sprintf("%.2fB", float64(n)/1000000000)
	}
	if n >= 1000000 {
		return fmt.Sprintf("%.2fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.2fK", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if days < 7 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}
