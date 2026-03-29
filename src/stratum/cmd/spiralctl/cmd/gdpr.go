// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Audit Recommendation #10: GDPR/CCPA data deletion command

package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// JSON data files that may contain personal data
var gdprDataFiles = []string{
	"/spiralpool/data/bans.json",
	"/spiralpool/data/device_hints.json",
	"/spiralpool/data/fleet.json",
}

// Additional data stores that may contain personal data (M1: GDPR completeness)
var gdprAdditionalStores = []struct {
	Name        string
	Description string
}{
	{"Redis dedup keys (share:dedup:*)", "Per-miner share deduplication keys keyed by wallet/worker"},
	{"Block WAL files (/spiralpool/wal/)", "Write-ahead log entries may reference miner addresses"},
	{"Prometheus metrics", "Label cardinality may include wallet addresses or worker names"},
}

func runGDPRDelete(args []string) error {
	fs := flag.NewFlagSet("gdpr-delete", flag.ExitOnError)
	walletFlag := fs.String("wallet", "", "Wallet address to delete")
	ipFlag := fs.String("ip", "", "IP address to delete")
	yesFlag := fs.Bool("yes", false, "Skip confirmation prompt")

	fs.Usage = func() {
		fmt.Println("Usage: spiralctl gdpr-delete [options]")
		fmt.Println()
		fmt.Println("Delete miner data for GDPR Article 17 / CCPA compliance.")
		fmt.Println("Removes records matching the specified wallet address or IP from the database.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  spiralctl gdpr-delete --wallet DAddress123... --yes")
		fmt.Println("  spiralctl gdpr-delete --ip 192.168.1.100 --yes")
		fmt.Println()
		fmt.Println("IMPORTANT: This operation is irreversible. Back up your database first.")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *walletFlag == "" && *ipFlag == "" {
		fs.Usage()
		return fmt.Errorf("at least one of --wallet or --ip is required")
	}

	// Use global --yes flag if set
	if globalYesFlag {
		*yesFlag = true
	}

	// Load config for database credentials
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Database.Host == "" {
		return fmt.Errorf("database.host not configured in %s", DefaultConfigFile)
	}

	// Display what will be deleted
	fmt.Printf("%s╔═══════════════════════════════════════════════════════╗%s\n", ColorYellow, ColorReset)
	fmt.Printf("%s║           GDPR / CCPA DATA DELETION                   ║%s\n", ColorYellow, ColorReset)
	fmt.Printf("%s╚═══════════════════════════════════════════════════════╝%s\n", ColorYellow, ColorReset)
	fmt.Println()

	if *walletFlag != "" {
		fmt.Printf("  Wallet address: %s%s%s\n", ColorCyan, *walletFlag, ColorReset)
	}
	if *ipFlag != "" {
		fmt.Printf("  IP address:     %s%s%s\n", ColorCyan, *ipFlag, ColorReset)
	}

	fmt.Println()
	fmt.Printf("  Database: %s@%s:%d/%s\n", cfg.Database.User, cfg.Database.Host, cfg.Database.Port, cfg.Database.Database)
	fmt.Println()

	// List JSON data files that may need manual review
	fmt.Printf("%sJSON data files that may contain matching data:%s\n", ColorBold, ColorReset)
	for _, f := range gdprDataFiles {
		if _, err := os.Stat(f); err == nil {
			fmt.Printf("  %s• %s%s (exists - review manually)\n", ColorYellow, f, ColorReset)
		} else {
			fmt.Printf("  • %s (not found)\n", f)
		}
	}
	fmt.Println()

	// Additional data stores (M1: GDPR completeness)
	fmt.Printf("%sAdditional data stores that may contain matching data:%s\n", ColorBold, ColorReset)
	for _, store := range gdprAdditionalStores {
		fmt.Printf("  %s• %s%s — %s\n", ColorYellow, store.Name, ColorReset, store.Description)
	}
	fmt.Println()

	// Log files
	fmt.Printf("%sLog directories that may contain matching data:%s\n", ColorBold, ColorReset)
	fmt.Println("  • /spiralpool/logs/ (review and rotate manually)")
	fmt.Println()

	if !*yesFlag {
		fmt.Printf("%s⚠ WARNING: This operation is IRREVERSIBLE.%s\n", ColorRed, ColorReset)
		fmt.Println("  Run with --yes to confirm deletion.")
		return nil
	}

	// Build parameterized SQL queries (SECURITY: prevents SQL injection)
	type paramQuery struct {
		sql  string
		args []any
		desc string
	}

	var queries []paramQuery

	dbPort := cfg.Database.Port
	if dbPort == 0 {
		dbPort = 5432
	}

	// Pool-specific table names (shares_{poolID}, blocks_{poolID})
	poolID := cfg.Pool.ID
	if poolID == "" {
		return fmt.Errorf("pool.id not configured in %s — cannot determine table names", DefaultConfigFile)
	}
	// SECURITY: Validate poolID to prevent SQL injection via table name interpolation
	validPoolIDRe := regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
	if !validPoolIDRe.MatchString(poolID) {
		return fmt.Errorf("invalid pool ID in config: %q — must be alphanumeric/underscores, max 63 chars", poolID)
	}
	sharesTable := "shares_" + poolID
	blocksTable := "blocks_" + poolID

	if *walletFlag != "" {
		wallet := *walletFlag
		queries = append(queries,
			paramQuery{
				sql:  fmt.Sprintf("DELETE FROM %s WHERE miner = $1", sharesTable),
				args: []any{wallet},
				desc: fmt.Sprintf("Delete shares for wallet %s", wallet),
			},
			paramQuery{
				sql:  fmt.Sprintf("DELETE FROM %s WHERE miner = $1", blocksTable),
				args: []any{wallet},
				desc: fmt.Sprintf("Delete blocks for wallet %s", wallet),
			},
			paramQuery{
				sql:  "DELETE FROM miners WHERE address = $1 AND poolid = $2",
				args: []any{wallet, poolID},
				desc: fmt.Sprintf("Delete miner records for wallet %s", wallet),
			},
		)
	}

	if *ipFlag != "" {
		ip := *ipFlag
		queries = append(queries,
			paramQuery{
				sql:  fmt.Sprintf("DELETE FROM %s WHERE ipaddress = $1", sharesTable),
				args: []any{ip},
				desc: fmt.Sprintf("Delete shares for IP %s", ip),
			},
		)
	}

	fmt.Printf("%sExecuting database deletions...%s\n", ColorBold, ColorReset)

	// Connect to PostgreSQL using pgx (SECURITY: parameterized queries prevent SQL injection)
	connStr := buildGDPRConnString(cfg, dbPort)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer conn.Close(ctx)

	totalDeleted := 0
	for _, q := range queries {
		fmt.Printf("  → %s... ", q.desc)

		tag, err := conn.Exec(ctx, q.sql, q.args...)
		if err != nil {
			fmt.Printf("%sFAILED%s\n", ColorRed, ColorReset)
			printError(fmt.Sprintf("  SQL error: %s", err))
			continue
		}

		fmt.Printf("%sOK%s", ColorGreen, ColorReset)
		if tag.RowsAffected() > 0 {
			fmt.Printf(" (%d rows)", tag.RowsAffected())
		}
		fmt.Println()
		totalDeleted++
	}

	fmt.Println()
	if totalDeleted == len(queries) {
		printSuccess(fmt.Sprintf("All %d database operations completed successfully", totalDeleted))
	} else {
		printWarning(fmt.Sprintf("%d/%d database operations completed", totalDeleted, len(queries)))
	}

	// Attempt automated purge of additional data stores (M1: GDPR completeness)
	fmt.Println()
	fmt.Printf("%sAutomated purge of additional data stores:%s\n", ColorBold, ColorReset)

	// Redis dedup key purge
	if *walletFlag != "" {
		purgeRedisKeys(*walletFlag)
	}
	if *ipFlag != "" {
		purgeRedisKeys(*ipFlag)
	}

	// WAL file scan
	purgeWALReferences(*walletFlag, *ipFlag)

	// Prometheus metric deletion
	purgePrometheusMetrics(*walletFlag, *ipFlag)

	// Remind about manual cleanup
	fmt.Println()
	fmt.Printf("%sManual cleanup still required:%s\n", ColorYellow, ColorReset)
	fmt.Println("  1. Review and edit JSON data files listed above")
	fmt.Println("  2. Rotate or redact log files in /spiralpool/logs/")
	fmt.Println("  3. Clear any Discord/Telegram message history if applicable")
	fmt.Println("  4. Purge Prometheus/Grafana metrics if applicable")
	fmt.Println("  5. Verify Redis dedup keys removed (share:dedup:* matching identifier)")
	fmt.Println("  6. Review WAL files in /spiralpool/wal/ for miner references")

	// Log the GDPR action to a dedicated file for compliance audit trail
	logGDPRAction(*walletFlag, *ipFlag)

	return nil
}

// buildGDPRConnString constructs a PostgreSQL connection string with properly escaped credentials.
// M2: Reads SSLMode from config instead of hardcoding sslmode=disable; defaults to "require" when empty.
func buildGDPRConnString(cfg *Config, port int) string {
	sslMode := cfg.Database.SSLMode
	if sslMode == "" {
		sslMode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(cfg.Database.User),
		url.QueryEscape(cfg.Database.Password),
		cfg.Database.Host,
		port,
		url.QueryEscape(cfg.Database.Database),
		url.QueryEscape(sslMode),
	)
}

// generateErasureSalt creates a random 16-byte salt for GDPR-compliant audit hashing.
// M1: Using a per-erasure salt prevents rainbow table attacks against the audit log
// and ensures hashes cannot be correlated across separate erasure requests.
func generateErasureSalt() string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		// Fallback to timestamp-based salt if crypto/rand fails
		return fmt.Sprintf("salt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(salt)
}

// hashForAudit returns a hex-encoded salted SHA-256 hash for audit logging, or "none" if the input is empty.
// SECURITY: Prevents PII from being stored in audit logs. The per-erasure salt prevents
// correlation of hashes across separate deletion requests and defeats rainbow table attacks.
func hashForAudit(s string, salt string) string {
	if s == "" {
		return "none"
	}
	h := sha256.Sum256([]byte(salt + ":" + s))
	return hex.EncodeToString(h[:])
}

// logGDPRAction logs the deletion request to a dedicated audit file.
// SECURITY: Logs salted hashed identifiers instead of raw PII to protect data subjects.
// M1: Per-erasure salt prevents correlation across separate deletion requests.
func logGDPRAction(wallet, ip string) {
	logDir := "/spiralpool/logs"
	logFile := filepath.Join(logDir, "gdpr-audit.log")

	// Best effort - don't fail the command if logging fails
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()

	salt := generateErasureSalt()
	entry := fmt.Sprintf("[%s] GDPR-DELETE salt=%s wallet_hash=%s ip_hash=%s\n",
		time.Now().UTC().Format(time.RFC3339), salt, hashForAudit(wallet, salt), hashForAudit(ip, salt))
	_, _ = f.WriteString(entry) // #nosec G104
}

// purgeRedisKeys attempts to delete Redis dedup keys matching the given identifier.
// M1: Best-effort — logs result but does not fail the GDPR command.
func purgeRedisKeys(identifier string) {
	// SECURITY: Escape Redis glob metacharacters in identifier to prevent pattern expansion
	escaped := strings.NewReplacer("*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`).Replace(identifier)
	pattern := fmt.Sprintf("share:dedup:*%s*", escaped)
	fmt.Printf("  → Scanning Redis for keys matching %s... ", pattern)

	// Use SCAN instead of KEYS to avoid blocking Redis in production
	// SCAN cursor [MATCH pattern] [COUNT hint] — iterates without blocking
	var allKeys []string
	cursor := "0"
	for {
		// #nosec G204 - pattern is escaped, cursor is numeric
		cmd := exec.Command("redis-cli", "--no-auth-warning", "SCAN", cursor, "MATCH", pattern, "COUNT", "100")
		output, err := cmd.Output()
		if err != nil {
			fmt.Printf("%sSKIPPED%s (redis-cli not available or Redis not running)\n", ColorYellow, ColorReset)
			return
		}
		lines := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)
		if len(lines) < 1 {
			break
		}
		cursor = strings.TrimSpace(lines[0])
		if len(lines) > 1 {
			for _, key := range strings.Split(strings.TrimSpace(lines[1]), "\n") {
				key = strings.TrimSpace(key)
				if key != "" {
					allKeys = append(allKeys, key)
				}
			}
		}
		if cursor == "0" {
			break
		}
	}

	if len(allKeys) == 0 {
		fmt.Printf("%sNONE FOUND%s\n", ColorGreen, ColorReset)
		return
	}

	// Delete found keys
	for _, key := range allKeys {
		// #nosec G204 - key comes from redis-cli SCAN output
		delCmd := exec.Command("redis-cli", "--no-auth-warning", "DEL", key)
		_ = delCmd.Run()
	}
	fmt.Printf("%sDELETED %d keys%s\n", ColorGreen, len(allKeys), ColorReset)
}

// purgeWALReferences scans WAL directory for files referencing the given identifiers.
// M1: Best-effort advisory — lists matching files for manual review.
func purgeWALReferences(wallet, ip string) {
	walDir := "/spiralpool/wal"
	if _, err := os.Stat(walDir); os.IsNotExist(err) {
		return
	}

	fmt.Printf("  → Scanning WAL directory for references... ")

	entries, err := os.ReadDir(walDir)
	if err != nil {
		fmt.Printf("%sSKIPPED%s (cannot read directory)\n", ColorYellow, ColorReset)
		return
	}

	matchCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(walDir, entry.Name())
		// G304: Path constructed from known directory, not untrusted
		data, err := os.ReadFile(path) // #nosec G304
		if err != nil {
			continue
		}
		content := string(data)
		if (wallet != "" && strings.Contains(content, wallet)) ||
			(ip != "" && strings.Contains(content, ip)) {
			matchCount++
			fmt.Printf("\n    %s⚠ %s%s contains matching data (review manually)", ColorYellow, entry.Name(), ColorReset)
		}
	}

	if matchCount == 0 {
		fmt.Printf("%sNO MATCHES%s\n", ColorGreen, ColorReset)
	} else {
		fmt.Printf("\n    %s%d WAL files contain matching references%s\n", ColorYellow, matchCount, ColorReset)
	}
}

// purgePrometheusMetrics attempts to delete matching time series from Prometheus via admin API.
// M1: Requires Prometheus admin API to be enabled (--web.enable-admin-api).
func purgePrometheusMetrics(wallet, ip string) {
	fmt.Printf("  → Attempting Prometheus metric purge... ")

	// Try common Prometheus address
	prometheusURL := "http://127.0.0.1:9090"

	identifiers := []string{}
	if wallet != "" {
		identifiers = append(identifiers, wallet)
	}
	if ip != "" {
		identifiers = append(identifiers, ip)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	deleted := 0

	for _, id := range identifiers {
		// Prometheus delete series API: POST /api/v1/admin/tsdb/delete_series
		reqURL := fmt.Sprintf("%s/api/v1/admin/tsdb/delete_series?match[]={__name__=~\".+\",instance=~\".*%s.*\"}", prometheusURL, url.QueryEscape(regexp.QuoteMeta(id)))
		req, err := http.NewRequest(http.MethodPost, reqURL, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("%sSKIPPED%s (Prometheus not reachable or admin API disabled)\n", ColorYellow, ColorReset)
			return
		}
		resp.Body.Close()
		if resp.StatusCode == 204 || resp.StatusCode == 200 {
			deleted++
		}
	}

	if deleted > 0 {
		// Also trigger a clean tombstones request
		req, _ := http.NewRequest(http.MethodPost, prometheusURL+"/api/v1/admin/tsdb/clean_tombstones", nil)
		if req != nil {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
		fmt.Printf("%sDONE%s (%d series deletion requests sent)\n", ColorGreen, ColorReset, deleted)
	} else {
		fmt.Printf("%sSKIPPED%s (no matching series or admin API disabled)\n", ColorYellow, ColorReset)
	}
}
