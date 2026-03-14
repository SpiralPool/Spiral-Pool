// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package daemon provides a client for communicating with cryptocurrency daemons.
//
// This implementation communicates with Bitcoin-compatible daemons via JSON-RPC,
// following the standard Bitcoin Core RPC API specification.
package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"go.uber.org/zap"
)

// RetryConfig controls retry behavior for RPC calls.
type RetryConfig struct {
	MaxRetries     int           // Maximum number of retry attempts
	InitialBackoff time.Duration // Initial backoff duration
	MaxBackoff     time.Duration // Maximum backoff duration
	BackoffFactor  float64       // Multiplier for exponential backoff
}

// DefaultRetryConfig returns sensible defaults for production use.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     10 * time.Second,
		BackoffFactor:  2.0,
	}
}

// Client communicates with a cryptocurrency daemon via JSON-RPC.
type Client struct {
	cfg    *config.DaemonConfig
	logger *zap.SugaredLogger
	client *http.Client

	// Request ID counter (atomic for thread safety)
	requestID atomic.Uint64

	// Retry configuration for self-healing
	retryConfig RetryConfig

	// Connection health tracking
	consecutiveFailures atomic.Int32
	lastSuccessTime     atomic.Int64

	// V35 FIX: Track last seen block height for regression detection
	lastSeenHeight atomic.Uint64

	// Multi-algo parameter for getblocktemplate (DGB: "sha256d", "scrypt", etc.)
	multiAlgoParam string

	// GBT rules for getblocktemplate (default: ["segwit"], LTC: ["mweb", "segwit"])
	gbtRules []string

	// Cached data
	blockTemplateMu  sync.RWMutex
	blockTemplate    *BlockTemplate
	lastTemplateTime time.Time

	// Network info cache
	networkInfoMu sync.RWMutex
	networkInfo   *NetworkInfo
}

// RPCRequest represents a JSON-RPC request.
type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      uint64        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// RPCResponse represents a JSON-RPC response.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

// RPCError represents a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

// BlockTemplate represents the data returned by getblocktemplate.
type BlockTemplate struct {
	Version                  uint32      `json:"version"`
	PreviousBlockHash        string      `json:"previousblockhash"`
	Transactions             []TxData    `json:"transactions"`
	CoinbaseAux              CoinbaseAux `json:"coinbaseaux"`
	CoinbaseValue            int64       `json:"coinbasevalue"`
	Target                   string      `json:"target"`
	MinTime                  int64       `json:"mintime"`
	Mutable                  []string    `json:"mutable"`
	NonceRange               string      `json:"noncerange"`
	SigOpLimit               int         `json:"sigoplimit"`
	SizeLimit                int         `json:"sizelimit"`
	CurTime                  int64       `json:"curtime"`
	Bits                     string      `json:"bits"`
	Height                   uint64      `json:"height"`
	DefaultWitnessCommitment string      `json:"default_witness_commitment,omitempty"`
}

// TxData represents transaction data in a block template.
type TxData struct {
	Data   string `json:"data"`
	TxID   string `json:"txid"`
	Hash   string `json:"hash"`
	Fee    int64  `json:"fee"`
	SigOps int    `json:"sigops"`
	Weight int    `json:"weight"`
}

// CoinbaseAux contains auxiliary data for the coinbase.
type CoinbaseAux struct {
	Flags string `json:"flags"`
}

// NetworkInfo represents network status information.
type NetworkInfo struct {
	Connections int    `json:"connections"`
	Version     int    `json:"version"`
	SubVersion  string `json:"subversion"`
}

// BlockchainInfo represents blockchain status.
type BlockchainInfo struct {
	Chain                string  `json:"chain"`
	Blocks               uint64  `json:"blocks"`
	Headers              uint64  `json:"headers"`
	BestBlockHash        string  `json:"bestblockhash"`
	Difficulty           float64 `json:"difficulty"`
	MedianTime           int64   `json:"mediantime"`
	VerificationProgress float64 `json:"verificationprogress"`
	InitialBlockDownload bool    `json:"initialblockdownload"`
	Pruned               bool    `json:"pruned"`
}

// NewClient creates a new daemon client with default retry configuration.
func NewClient(cfg *config.DaemonConfig, logger *zap.Logger) *Client {
	return NewClientWithRetry(cfg, logger, DefaultRetryConfig())
}

// NewClientWithRetry creates a new daemon client with custom retry configuration.
func NewClientWithRetry(cfg *config.DaemonConfig, logger *zap.Logger, retryConfig RetryConfig) *Client {
	sugar := logger.Sugar()

	// SECURITY: Log RPC transport mode for awareness
	// HTTP is common and acceptable for localhost/internal networks since most nodes don't support HTTPS
	// Only warn if using HTTP to a non-localhost address
	endpoint := cfg.RPCEndpoint()
	if !strings.HasPrefix(endpoint, "https://") {
		// Check if this is a localhost connection (safe over HTTP)
		isLocalhost := strings.Contains(endpoint, "127.0.0.1") ||
			strings.Contains(endpoint, "localhost") ||
			strings.Contains(endpoint, "[::1]")

		if isLocalhost {
			sugar.Infow("Daemon RPC using HTTP on localhost (acceptable - most nodes don't support HTTPS)",
				"endpoint", endpoint,
			)
		} else {
			// Non-localhost HTTP - higher risk, warn the operator
			sugar.Warnw("SECURITY: Daemon RPC using HTTP to non-localhost address",
				"endpoint", endpoint,
				"risk", "RPC credentials could be intercepted on untrusted networks",
				"mitigation", "Use a VPN, private network, or SSH tunnel for remote daemon connections",
			)
		}
	}

	c := &Client{
		cfg:         cfg,
		logger:      sugar,
		retryConfig: retryConfig,
		client: &http.Client{
			// 120 second timeout to handle large blocks (up to 4MB) on slow connections
			// Block submission should complete within this time even under adverse conditions
			Timeout: 120 * time.Second,
		},
	}
	c.lastSuccessTime.Store(time.Now().Unix())
	return c
}

// SetMultiAlgoParam configures the algorithm parameter for getblocktemplate.
// On multi-algo coins like DigiByte, getblocktemplate requires a second
// parameter specifying which algorithm's difficulty target to use.
func (c *Client) SetMultiAlgoParam(param string) {
	c.multiAlgoParam = param
	c.logger.Infow("Multi-algo GBT parameter configured", "algo", param)
}

// SetGBTRules configures the rules array for getblocktemplate.
// Most coins use ["segwit"], but some require additional rules:
// - Litecoin requires ["mweb", "segwit"] for MimbleWimble Extension Blocks
func (c *Client) SetGBTRules(rules []string) {
	c.gbtRules = rules
	c.logger.Infow("GBT rules configured", "rules", rules)
}

// IsHealthy returns true if the client has had recent successful communication.
func (c *Client) IsHealthy() bool {
	failures := c.consecutiveFailures.Load()
	lastSuccess := time.Unix(c.lastSuccessTime.Load(), 0)
	// Healthy if fewer than 3 consecutive failures and success within last 2 minutes
	return failures < 3 && time.Since(lastSuccess) < 2*time.Minute
}

// ConsecutiveFailures returns the number of consecutive failed calls.
func (c *Client) ConsecutiveFailures() int {
	return int(c.consecutiveFailures.Load())
}

// recordSuccess resets failure tracking on successful call.
func (c *Client) recordSuccess() {
	c.consecutiveFailures.Store(0)
	c.lastSuccessTime.Store(time.Now().Unix())
}

// recordFailure increments failure counter.
func (c *Client) recordFailure() {
	c.consecutiveFailures.Add(1)
}

// call executes a JSON-RPC call with automatic retry and exponential backoff.
func (c *Client) call(ctx context.Context, method string, params []interface{}) (*RPCResponse, error) {
	return c.callWithRetry(ctx, method, params, true)
}

// callNoRetry executes a JSON-RPC call without retry (for time-critical operations).
func (c *Client) callNoRetry(ctx context.Context, method string, params []interface{}) (*RPCResponse, error) {
	return c.callWithRetry(ctx, method, params, false)
}

// callWithRetry executes a JSON-RPC call with optional retry logic.
func (c *Client) callWithRetry(ctx context.Context, method string, params []interface{}, enableRetry bool) (*RPCResponse, error) {
	var lastErr error
	backoff := c.retryConfig.InitialBackoff
	maxAttempts := 1
	if enableRetry {
		maxAttempts = c.retryConfig.MaxRetries + 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check context before each attempt
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Wait before retry (skip first attempt)
		if attempt > 0 {
			c.logger.Debugw("Retrying RPC call",
				"method", method,
				"attempt", attempt+1,
				"backoff", backoff)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}

			// Exponential backoff
			backoff = time.Duration(float64(backoff) * c.retryConfig.BackoffFactor)
			if backoff > c.retryConfig.MaxBackoff {
				backoff = c.retryConfig.MaxBackoff
			}
		}

		resp, err := c.doCall(ctx, method, params)
		if err == nil {
			// Success - reset failure tracking
			c.recordSuccess()
			return resp, nil
		}

		lastErr = err
		c.recordFailure()

		// Don't retry on context cancellation or RPC errors (only network errors)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// RPC errors (from daemon) shouldn't be retried
		if _, isRPCError := err.(*RPCError); isRPCError {
			return nil, err
		}

		// Log retry attempt
		if attempt < maxAttempts-1 {
			c.logger.Warnw("RPC call failed, will retry",
				"method", method,
				"attempt", attempt+1,
				"error", err)
		}
	}

	c.logger.Errorw("RPC call failed after all retries",
		"method", method,
		"attempts", maxAttempts,
		"error", lastErr)

	return nil, fmt.Errorf("RPC call failed after %d attempts: %w", maxAttempts, lastErr)
}

// doCall executes a single JSON-RPC call without retry.
func (c *Client) doCall(ctx context.Context, method string, params []interface{}) (*RPCResponse, error) {
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      c.requestID.Add(1),
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.cfg.RPCEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.SetBasicAuth(c.cfg.User, c.cfg.Password)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("RPC request failed: %w", err)
	}
	defer resp.Body.Close()

	// AUDIT FIX (DC-H2): Check HTTP status code before parsing JSON.
	// Without this, 401 Unauthorized returns HTML which fails JSON parse with a
	// confusing "invalid character '<'" error, causing the retry logic to retry
	// credential errors instead of fast-failing.
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, &RPCError{Code: -32001, Message: fmt.Sprintf("HTTP %d: daemon authentication failed (check RPC credentials)", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Allow 500 through — Bitcoin Core returns 500 for some RPC errors with valid JSON body.
		// Only block non-500 error codes that definitely won't have JSON-RPC responses.
		if resp.StatusCode != 500 {
			return nil, fmt.Errorf("daemon returned HTTP %d (expected 200)", resp.StatusCode)
		}
	}

	// Limit RPC response reads to 64MB to prevent OOM from malicious/misconfigured daemons.
	// Normal RPC responses (getblocktemplate, submitblock) are under 4MB.
	const maxRPCResponseSize = 64 << 20 // 64 MiB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRPCResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var rpcResp RPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return &rpcResp, nil
}

// GetBlockTemplate fetches a new block template from the daemon.
func (c *Client) GetBlockTemplate(ctx context.Context) (*BlockTemplate, error) {
	// Use coin-specific rules or default to ["segwit"]
	// IMPORTANT: Check for nil, not len()==0, because coins without SegWit
	// (DOGE, BCH, etc.) explicitly return []string{} to disable SegWit rules
	rules := c.gbtRules
	if rules == nil {
		rules = []string{"segwit"}
	}

	params := []interface{}{
		map[string]interface{}{
			"rules": rules,
		},
	}
	// Multi-algo coins (DGB) require algorithm parameter for correct difficulty
	if c.multiAlgoParam != "" {
		params = append(params, c.multiAlgoParam)
	}

	resp, err := c.call(ctx, "getblocktemplate", params)
	if err != nil {
		return nil, err
	}

	var template BlockTemplate
	if err := json.Unmarshal(resp.Result, &template); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block template: %w", err)
	}

	// Validate critical template fields to catch daemon issues early
	if err := c.validateBlockTemplate(&template); err != nil {
		return nil, fmt.Errorf("invalid block template: %w", err)
	}

	// Cache the template
	c.blockTemplateMu.Lock()
	c.blockTemplate = &template
	c.lastTemplateTime = time.Now()
	c.blockTemplateMu.Unlock()

	return &template, nil
}

// validateBlockTemplate checks critical fields in a block template.
// This catches daemon bugs or malformed responses before mining starts.
func (c *Client) validateBlockTemplate(t *BlockTemplate) error {
	// Validate block height (must be positive)
	if t.Height == 0 {
		return fmt.Errorf("block height is 0 (invalid or daemon not synced)")
	}

	// Validate previous block hash (must be 64 hex chars = 32 bytes)
	if len(t.PreviousBlockHash) != 64 {
		return fmt.Errorf("previous block hash has invalid length: %d (expected 64)", len(t.PreviousBlockHash))
	}

	// Validate coinbase value (block reward)
	// Must be positive and not exceed theoretical maximum (21M BTC = 2.1 quadrillion satoshis)
	const maxCoinbaseValue int64 = 21_000_000 * 100_000_000 // 21M coins in satoshis
	if t.CoinbaseValue < 0 {
		return fmt.Errorf("negative coinbase value: %d satoshis", t.CoinbaseValue)
	}
	if t.CoinbaseValue > maxCoinbaseValue {
		return fmt.Errorf("coinbase value exceeds max supply: %d satoshis", t.CoinbaseValue)
	}
	if t.CoinbaseValue == 0 {
		c.logger.Warnw("Block template has zero coinbase value (fees-only block?)",
			"height", t.Height)
	}

	// Validate bits field (compact difficulty target)
	if len(t.Bits) != 8 {
		return fmt.Errorf("bits field has invalid length: %d (expected 8)", len(t.Bits))
	}

	// Validate target field (256-bit hex string from GBT).
	// NOTE: The daemon validates submitblock against compact bits (nBits), NOT this
	// target. On multi-algo coins (DGB), the GBT target can be more permissive than
	// compact bits. The validator uses min(gbt, bits) to prevent high-hash rejections.
	// Missing target degrades block detection to compact bits only.
	if t.Target == "" {
		c.logger.Warnw("Block template missing 'target' field — block detection will use compact bits fallback",
			"height", t.Height, "bits", t.Bits)
	} else if len(t.Target) != 64 {
		c.logger.Warnw("Block template 'target' field has unexpected length — will use compact bits fallback",
			"height", t.Height, "targetLen", len(t.Target), "expected", 64)
		t.Target = "" // Clear invalid target so downstream uses fallback
	}

	// Cross-check GBT target against compact bits for consistency.
	// Bitcoin Core's CheckProofOfWork() validates submitted blocks against compact
	// bits (nBits), NOT the GBT target. On multi-algo coins (DGB), these can
	// differ — the GBT target may be more permissive, causing high-hash rejections.
	//
	// INCIDENT: 2026-01-27 04:49 UTC, DGB height 22870718 — GBT target was more
	// permissive than compact bits. Pool accepted hash, daemon rejected as high-hash.
	//
	// FIX: When GBT target is more permissive than compact bits, override it with
	// the compact bits expansion. The validator also enforces min(gbt, bits) as a
	// second line of defense.
	if t.Target != "" && len(t.Target) == 64 && len(t.Bits) == 8 {
		gbtTarget := new(big.Int)
		if _, ok := gbtTarget.SetString(t.Target, 16); ok && gbtTarget.Sign() > 0 {
			bitsTarget := bitsToTarget256(t.Bits)
			if bitsTarget != nil && bitsTarget.Sign() > 0 && gbtTarget.Cmp(bitsTarget) != 0 {
				if gbtTarget.Cmp(bitsTarget) > 0 {
					// GBT target is MORE permissive — override with compact bits.
					// This prevents high-hash rejections at the template level.
					c.logger.Warnw("GBT target MORE permissive than compact bits — overriding with compact bits (daemon validates against nBits)",
						"height", t.Height,
						"gbtTarget", t.Target,
						"bitsTarget", fmt.Sprintf("%064x", bitsTarget),
						"bits", t.Bits,
					)
					t.Target = fmt.Sprintf("%064x", bitsTarget)
				} else {
					// GBT target is stricter — safe, just log for awareness
					c.logger.Debugw("GBT target stricter than compact bits (safe)",
						"height", t.Height,
						"gbtTarget", t.Target,
						"bitsTarget", fmt.Sprintf("%064x", bitsTarget),
						"bits", t.Bits,
					)
				}
			}
		}
	}

	// Validate timestamp (should be recent, not in distant future)
	now := time.Now().Unix()
	if t.CurTime < now-7200 { // More than 2 hours old
		c.logger.Warnw("Block template timestamp is old",
			"templateTime", t.CurTime, "now", now, "age", now-t.CurTime)
	}
	if t.CurTime > now+7200 { // More than 2 hours in future
		return fmt.Errorf("block template timestamp too far in future: %d (now: %d)", t.CurTime, now)
	}

	return nil
}

// GetCachedBlockTemplate returns the cached block template if fresh enough.
func (c *Client) GetCachedBlockTemplate(maxAge time.Duration) *BlockTemplate {
	c.blockTemplateMu.RLock()
	defer c.blockTemplateMu.RUnlock()

	if c.blockTemplate == nil {
		return nil
	}
	// maxAge of 0 means always expired (useful for forcing fresh fetch)
	if maxAge == 0 || time.Since(c.lastTemplateTime) > maxAge {
		return nil
	}
	return c.blockTemplate
}

// GetBlockchainInfo returns current blockchain status.
func (c *Client) GetBlockchainInfo(ctx context.Context) (*BlockchainInfo, error) {
	resp, err := c.call(ctx, "getblockchaininfo", nil)
	if err != nil {
		return nil, err
	}

	// Check for null result (daemon returned no data)
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, fmt.Errorf("daemon returned null result for getblockchaininfo")
	}

	var info BlockchainInfo
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal blockchain info: %w", err)
	}

	// V34 FIX: Validate critical fields are non-empty after unmarshal.
	// A proxy error, truncated JSON, or misconfigured daemon can return a
	// structurally valid but semantically empty response. Mining on empty
	// state causes downstream failures in GetBlock("") and template generation.
	if info.BestBlockHash == "" {
		return nil, fmt.Errorf("daemon returned empty bestblockhash (check RPC proxy or daemon health)")
	}
	if info.Chain == "" {
		return nil, fmt.Errorf("daemon returned empty chain identifier (daemon may not be fully initialized)")
	}

	// V35 FIX: Detect height regression (daemon returning lower height than previously seen).
	// This catches: daemon restart to earlier snapshot, corrupt chainstate, or
	// load balancer hitting different nodes at different heights.
	// Small regressions (<=10) are normal reorgs. Large regressions indicate misconfiguration.
	lastHeight := c.lastSeenHeight.Load()
	if lastHeight > 0 && info.Blocks > 0 && info.Blocks < lastHeight {
		regression := lastHeight - info.Blocks
		if regression > 10 {
			c.logger.Errorw("🚨 V35: LARGE HEIGHT REGRESSION DETECTED — daemon may be misconfigured!",
				"lastSeenHeight", lastHeight,
				"reportedHeight", info.Blocks,
				"regression", regression,
				"threshold", 10,
				"possibleCauses", "daemon restarted from old snapshot, corrupt chainstate, RPC load balancer hitting wrong node",
			)
		} else {
			c.logger.Warnw("⚠️ V35: Height regression detected (possible reorg)",
				"lastSeenHeight", lastHeight,
				"reportedHeight", info.Blocks,
				"depth", regression,
			)
		}
	}
	if info.Blocks > 0 {
		c.lastSeenHeight.Store(info.Blocks)
	}

	return &info, nil
}

// GetNetworkInfo returns network status.
func (c *Client) GetNetworkInfo(ctx context.Context) (*NetworkInfo, error) {
	resp, err := c.call(ctx, "getnetworkinfo", nil)
	if err != nil {
		return nil, err
	}

	var info NetworkInfo
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal network info: %w", err)
	}

	c.networkInfoMu.Lock()
	c.networkInfo = &info
	c.networkInfoMu.Unlock()

	return &info, nil
}

// SubmitBlock submits a solved block to the network.
// CRITICAL: Uses callNoRetry because block submission is time-critical.
// Any retry delay risks the block going stale (another miner finds a block first).
// The pool layer handles retries with minimal delays if needed.
func (c *Client) SubmitBlock(ctx context.Context, blockHex string) error {
	resp, err := c.callNoRetry(ctx, "submitblock", []interface{}{blockHex})
	if err != nil {
		return err
	}

	// Check for rejection
	if len(resp.Result) > 0 && string(resp.Result) != "null" {
		var reason string
		if err := json.Unmarshal(resp.Result, &reason); err != nil {
			return fmt.Errorf("failed to parse block rejection reason: %w", err)
		}
		if reason != "" {
			return fmt.Errorf("block rejected: %s", reason)
		}
	}

	return nil
}

// ValidateAddress validates a cryptocurrency address.
func (c *Client) ValidateAddress(ctx context.Context, address string) (bool, error) {
	resp, err := c.call(ctx, "validateaddress", []interface{}{address})
	if err != nil {
		return false, err
	}

	var result struct {
		IsValid bool `json:"isvalid"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return false, err
	}

	return result.IsValid, nil
}

// GetBalance returns the wallet balance.
func (c *Client) GetBalance(ctx context.Context) (float64, error) {
	resp, err := c.call(ctx, "getbalance", nil)
	if err != nil {
		return 0, err
	}

	var balance float64
	if err := json.Unmarshal(resp.Result, &balance); err != nil {
		return 0, err
	}

	return balance, nil
}

// SendMany sends payments to multiple addresses via the node's RPC interface.
// SECURITY (M-09): All addresses are validated before payment execution.
//
// RPC COMMAND EXPLANATION:
// "sendmany" is the standard Bitcoin Core RPC command for batch payments.
// The "many" refers to multiple recipient addresses in a single transaction,
// NOT multiple cryptocurrencies. This is standard pool software practice:
// instead of creating 100 separate transactions to pay 100 miners (expensive,
// slow, high fees), one sendmany call pays all pending miners in a single
// efficient transaction. The function name matches the underlying RPC command.
//
// NON-CUSTODIAL ARCHITECTURE NOTICE:
// This function interfaces directly with the cryptocurrency node (bitcoind, etc.)
// running on the operator's infrastructure. Payments are:
//   - Executed by the node, NOT by this software
//   - Sent from the node's wallet to miner-provided payout addresses
//   - Never held, controlled, or custodied by this pool software
//   - Subject to the node's own validation and consensus rules
//
// The pool software acts as a coordination layer that instructs the operator's
// node to execute transactions. At no point does this software take custody of,
// control, or have independent access to user funds. All cryptocurrency flows
// directly between the blockchain and the miner's designated wallet address.
//
// This is a SOLO/non-custodial mining architecture by design.
func (c *Client) SendMany(ctx context.Context, payments map[string]float64) (string, error) {
	// SECURITY: Validate all addresses before sending payments
	for address, amount := range payments {
		// Basic validation
		if address == "" {
			return "", fmt.Errorf("empty address in payment list")
		}
		if amount <= 0 {
			return "", fmt.Errorf("invalid payment amount for %s: %f", address, amount)
		}

		// Validate address with daemon
		valid, err := c.ValidateAddress(ctx, address)
		if err != nil {
			return "", fmt.Errorf("failed to validate address %s: %w", address, err)
		}
		if !valid {
			return "", fmt.Errorf("invalid payment address: %s", address)
		}
	}

	resp, err := c.call(ctx, "sendmany", []interface{}{"", payments})
	if err != nil {
		return "", err
	}

	var txid string
	if err := json.Unmarshal(resp.Result, &txid); err != nil {
		return "", err
	}

	return txid, nil
}

// GetDifficulty returns the current network difficulty.
// For single-algo coins (Bitcoin, Bitcoin Cash), this returns the network difficulty directly.
// For multi-algo coins (DigiByte), this returns the difficulty for the configured algorithm.
// The algorithm is determined by multiAlgoParam (set via SetMultiAlgoParam), defaulting to sha256d.
//
// DigiByte getdifficulty response formats:
//
//	v8.22+: {"difficulties": {"sha256d": 12345.67, "scrypt": 234.56, ...}}
//	older:  {"sha256d": 12345.67, "scrypt": 234.56, ...}
func (c *Client) GetDifficulty(ctx context.Context) (float64, error) {
	resp, err := c.call(ctx, "getdifficulty", nil)
	if err != nil {
		return 0, err
	}

	// Try parsing as a simple float64 first (Bitcoin, Bitcoin Cash)
	var diff float64
	if err := json.Unmarshal(resp.Result, &diff); err == nil {
		if diff <= 0 {
			return 0, fmt.Errorf("invalid network difficulty: %f (must be positive)", diff)
		}
		return diff, nil
	}

	// Build algorithm key list based on configured multi-algo parameter.
	// When multiAlgoParam is set (e.g., "scrypt"), look for that algorithm.
	// Otherwise default to sha256d for backwards compatibility.
	var algoKeys []string
	if c.multiAlgoParam != "" {
		algo := c.multiAlgoParam
		algoKeys = []string{algo, strings.ToLower(algo), strings.ToUpper(algo)}
	} else {
		algoKeys = []string{"sha256d", "sha-256d", "SHA256D", "SHA-256D", "Sha256d"}
	}
	algoLabel := "sha256d"
	if c.multiAlgoParam != "" {
		algoLabel = c.multiAlgoParam
	}

	// DigiByte v8.22+ format: {"difficulties": {"sha256d": N, "scrypt": N, ...}}
	type DifficultiesWrapper struct {
		Difficulties map[string]float64 `json:"difficulties"`
	}
	var wrapped DifficultiesWrapper
	if err := json.Unmarshal(resp.Result, &wrapped); err == nil && len(wrapped.Difficulties) > 0 {
		for _, key := range algoKeys {
			if diffVal, ok := wrapped.Difficulties[key]; ok && diffVal > 0 {
				return diffVal, nil
			}
		}
		availableAlgos := make([]string, 0, len(wrapped.Difficulties))
		for algo := range wrapped.Difficulties {
			availableAlgos = append(availableAlgos, algo)
		}
		c.logger.Warnw("Algorithm not found in difficulties wrapper",
			"wanted", algoLabel,
			"availableAlgorithms", availableAlgos)
	}

	// DigiByte older format with nested objects per algorithm:
	// {"sha256d": {"difficulty": N, ...}, "scrypt": {...}, ...}
	type AlgoDifficulty struct {
		Difficulty        float64 `json:"difficulty"`
		DifficultyAverage float64 `json:"difficulty_average"`
	}
	var nestedDiff map[string]AlgoDifficulty
	if err := json.Unmarshal(resp.Result, &nestedDiff); err == nil && len(nestedDiff) > 0 {
		for _, key := range algoKeys {
			if algoDiff, ok := nestedDiff[key]; ok {
				if algoDiff.Difficulty > 0 {
					return algoDiff.Difficulty, nil
				}
			}
		}
		availableAlgos := make([]string, 0, len(nestedDiff))
		for algo := range nestedDiff {
			availableAlgos = append(availableAlgos, algo)
		}
		c.logger.Warnw("Algorithm not found in nested difficulty response",
			"wanted", algoLabel,
			"availableAlgorithms", availableAlgos)
	}

	// Legacy DigiByte format (older versions): {"sha256d": 123.45, ...}
	var flatDiff map[string]float64
	if err := json.Unmarshal(resp.Result, &flatDiff); err == nil {
		for _, key := range algoKeys {
			if algoDiff, ok := flatDiff[key]; ok {
				if algoDiff > 0 {
					return algoDiff, nil
				}
			}
		}
	}

	// Generic fallback: try map[string]interface{} for unexpected formats
	var genericDiff map[string]interface{}
	if err := json.Unmarshal(resp.Result, &genericDiff); err == nil {
		for _, key := range algoKeys {
			if val, ok := genericDiff[key]; ok {
				// Handle nested object {"difficulty": N}
				if nested, isMap := val.(map[string]interface{}); isMap {
					if diffVal, hasDiff := nested["difficulty"]; hasDiff {
						if f, isFloat := diffVal.(float64); isFloat && f > 0 {
							return f, nil
						}
					}
				}
				// Handle direct float
				if f, isFloat := val.(float64); isFloat && f > 0 {
					return f, nil
				}
				// Handle string
				if s, isString := val.(string); isString {
					var parsed float64
					if _, err := fmt.Sscanf(s, "%f", &parsed); err == nil && parsed > 0 {
						return parsed, nil
					}
				}
			}
		}

		// Log available keys for debugging
		keys := make([]string, 0, len(genericDiff))
		for k := range genericDiff {
			keys = append(keys, k)
		}
		c.logger.Warnw("Could not extract algorithm difficulty",
			"wanted", algoLabel,
			"availableKeys", keys,
			"rawResponse", string(resp.Result))
	}

	// Final fallback: log and return error
	c.logger.Errorw("Failed to parse difficulty response",
		"wanted", algoLabel,
		"rawResponse", string(resp.Result))

	return 0, fmt.Errorf("failed to parse %s difficulty: raw=%s", algoLabel, string(resp.Result))
}

// Ping checks if the daemon is responsive.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.call(ctx, "getblockchaininfo", nil)
	return err
}

// GetBlockHash returns the block hash at a given height.
// Used for verifying blocks are still in the main chain (orphan detection).
func (c *Client) GetBlockHash(ctx context.Context, height uint64) (string, error) {
	resp, err := c.call(ctx, "getblockhash", []interface{}{height})
	if err != nil {
		return "", err
	}

	var hash string
	if err := json.Unmarshal(resp.Result, &hash); err != nil {
		return "", err
	}

	return hash, nil
}

// GetBlock retrieves block information by hash.
// Used for post-timeout verification to check if a block was accepted.
//
// POST-TIMEOUT VERIFICATION (Audit Recommendation #3):
// After submitblock timeout/error, we need to verify if the block was actually
// accepted. This method allows checking if a block exists in the chain by hash.
func (c *Client) GetBlock(ctx context.Context, blockHash string) (map[string]interface{}, error) {
	// verbosity=1 returns JSON object with block info (not raw hex)
	resp, err := c.call(ctx, "getblock", []interface{}{blockHash, 1})
	if err != nil {
		return nil, err
	}

	// Check for null result (block not found)
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, nil
	}

	var blockInfo map[string]interface{}
	if err := json.Unmarshal(resp.Result, &blockInfo); err != nil {
		return nil, fmt.Errorf("failed to parse getblock response: %w", err)
	}

	return blockInfo, nil
}

// PreciousBlock hints the node to treat this block as if it were received before
// any competing blocks at the same height. This gives our submitted block an
// advantage during block propagation races. Non-critical — failures are safe to ignore.
// Retries once on transient failure since this is a best-effort optimization.
func (c *Client) PreciousBlock(ctx context.Context, blockHash string) error {
	_, err := c.callNoRetry(ctx, "preciousblock", []interface{}{blockHash})
	if err != nil {
		// Single retry for transient errors (network glitch, node busy)
		_, err = c.callNoRetry(ctx, "preciousblock", []interface{}{blockHash})
	}
	return err
}

// BlockSubmitResult contains the outcome of a block submission with verification.
type BlockSubmitResult struct {
	Submitted    bool   // SubmitBlock RPC returned success
	Verified     bool   // GetBlockHash confirmed our hash at target height
	ChainHash    string // The hash actually at the target height (from getblockhash)
	SubmitErr    error  // Error from SubmitBlock (nil on success)
	VerifyErr    error  // Error from GetBlockHash (nil on success)
	PreciousErr  error  // Error from PreciousBlock (nil on success, non-critical)
}

// SubmitTimeouts contains coin-aware timeouts for block submission.
// On fast blockchains (DGB 15s), we can't afford to wait 30s for a single RPC call —
// the block will be stale. On slow blockchains (BTC 600s), generous timeouts are fine.
//
// All timeouts are computed from block time to ensure the entire submit+verify+retry
// cycle completes within a single block period.
type SubmitTimeouts struct {
	SubmitTimeout   time.Duration // Max time to wait for submitblock RPC
	VerifyTimeout   time.Duration // Max time to wait for getblockhash verification
	PreciousTimeout time.Duration // Max time to wait for preciousblock hint
	TotalBudget     time.Duration // Outer context timeout for entire submit+verify+precious
	RetryTimeout    time.Duration // Per-retry timeout (shorter than initial)
	MaxRetries      int           // Safety cap: hard limit on retry count (defense-in-depth)
	RetrySleep      time.Duration // Sleep between retries
	SubmitDeadline  time.Duration // Total deadline for all submission work (BlockTime × 0.30)
}

// DefaultSubmitTimeouts returns BTC-tier timeouts (normal chain, 5 retries).
// Used by tests that don't care about coin-specific timing.
func DefaultSubmitTimeouts() *SubmitTimeouts {
	return NewSubmitTimeouts(600)
}

// NewSubmitTimeouts computes coin-aware timeouts from block time in seconds.
//
// submitblock/getblockhash/preciousblock are local daemon RPC calls. Industry data
// (CKPool, NOMP) shows normal latency of 100ms–2s, with cs_main lock contention
// spikes up to 10–15s on BTC (documented: 12.2s in a CKPool incident — concurrent
// GBT held cs_main for 10.9s while processing a 5MB block's mempool).
//
// Fast chains (< 30s blocks) get tighter RPC timeouts because their blocks are small
// (15s of transactions), so validation and cs_main hold times are proportionally shorter.
// The documented 10–15s contention spikes are BTC-specific (large mempools, big blocks).
// A DGB daemon processing a 15s block will not hold cs_main for 10s.
//
// SubmitDeadline = BlockTime × 0.30 is the total time budget for all submission work
// (initial attempt + retries). The height-locked context in pool.go enforces this deadline
// and also cancels if the chain tip advances (stale block). MaxRetries is kept as a
// hard safety cap to prevent runaway retries on long-block coins.
//
// All 13 supported coins grouped by block time tier:
//
//	15s  (DGB, DGB-SCRYPT):                submit=3s  deadline=4.5s   retries=2  propagation=10.5s
//	30s  (FBTC):                           submit=5s  deadline=9s     retries=3  propagation=21s
//	60s  (DOGE, XMY, PEP):                 submit=5s  deadline=18s    retries=5  propagation=42s
//	150s (LTC, SYS):                       submit=5s  deadline=45s    retries=5  propagation=105s
//	600s (BTC, BCH, BC2, CAT, NMC, QBX):   submit=5s  deadline=180s   retries=5  propagation=420s
func NewSubmitTimeouts(blockTimeSec int) *SubmitTimeouts {
	const retrySleep = 10 * time.Millisecond

	// Two tiers of RPC timeouts based on block time.
	// Fast chains have smaller blocks → shorter validation → shorter cs_main holds.
	var submit, verify, precious, total, retryTO time.Duration

	if blockTimeSec < 30 {
		// Fast chains (DGB 15s, DGB-SCRYPT 15s): tight timeouts.
		// 15s of transactions = small block, fast validation, minimal cs_main hold.
		submit = 3 * time.Second   // submitblock: small block, fast validation
		verify = 1 * time.Second   // getblockhash: single DB lookup
		precious = 500 * time.Millisecond // preciousblock: best-effort hint
		total = 5 * time.Second    // budget: covers submit+verify+precious sequentially
		retryTO = 2 * time.Second  // retry: tight, just re-submit
	} else {
		// Normal/slow chains (FBTC 30s through BTC 600s): generous timeouts.
		// Larger blocks, deeper mempools, real cs_main contention risk.
		submit = 5 * time.Second   // submitblock: handles moderate contention
		verify = 3 * time.Second   // getblockhash: single DB lookup
		precious = 2 * time.Second // preciousblock: best-effort hint
		total = 10 * time.Second   // budget: covers all steps under contention
		retryTO = 3 * time.Second  // retry: submit + verify
	}

	// Compute retries from remaining budget.
	// Reserve 1/3 of block time for network propagation.
	bt := time.Duration(blockTimeSec) * time.Second
	propagation := bt / 3
	availableForRetries := bt - propagation - total
	maxRetries := 0
	if availableForRetries > retryTO {
		maxRetries = int(availableForRetries / (retrySleep + retryTO))
	}
	if maxRetries > 5 {
		maxRetries = 5 // Cap: diminishing returns beyond 5 retries
	}

	// SubmitDeadline: 30% of block time for all submission work.
	// Remaining 70% is propagation margin. The deadline is enforced by a
	// context.WithTimeout in pool.go, layered under the height-locked context.
	submitDeadline := bt * 3 / 10 // 0.30 × blockTime
	if submitDeadline < 3*time.Second {
		submitDeadline = 3 * time.Second // Floor: at least 3s for any coin
	}

	return &SubmitTimeouts{
		SubmitTimeout:   submit,
		VerifyTimeout:   verify,
		PreciousTimeout: precious,
		TotalBudget:     total,
		RetryTimeout:    retryTO,
		MaxRetries:      maxRetries,
		RetrySleep:      retrySleep,
		SubmitDeadline:  submitDeadline,
	}
}

// SubmitBlockWithVerification combines submitblock + preciousblock + getblockhash verification.
// This is the industry-standard approach: submit the block, hint the node, then verify acceptance.
// Returns a comprehensive result so the caller can make informed decisions about block status.
//
// Timeouts are coin-aware: on DGB (15s blocks) the entire cycle fits in ~15s,
// while on BTC (600s blocks) we allow up to 60s. Pass nil for default (BTC-tuned) timeouts.
func (c *Client) SubmitBlockWithVerification(ctx context.Context, blockHex string, blockHash string, height uint64, timeouts *SubmitTimeouts) *BlockSubmitResult {
	if timeouts == nil {
		timeouts = DefaultSubmitTimeouts()
	}
	result := &BlockSubmitResult{}

	// 1. Submit the block — most time-critical operation
	submitCtx, submitCancel := context.WithTimeout(ctx, timeouts.SubmitTimeout)
	result.SubmitErr = c.SubmitBlock(submitCtx, blockHex)
	submitCancel()
	result.Submitted = result.SubmitErr == nil

	// 2. Hint the node to prefer our block (non-blocking best-effort)
	if result.Submitted && blockHash != "" {
		pCtx, pCancel := context.WithTimeout(ctx, timeouts.PreciousTimeout)
		result.PreciousErr = c.PreciousBlock(pCtx, blockHash)
		pCancel()
	}

	// 3. Verify the block is actually in the chain
	// Do this regardless of SubmitBlock result — the block may have been accepted
	// despite a timeout or error response (network-level vs application-level)
	verifyCtx, verifyCancel := context.WithTimeout(ctx, timeouts.VerifyTimeout)
	chainHash, verifyErr := c.GetBlockHash(verifyCtx, height)
	verifyCancel()
	result.VerifyErr = verifyErr
	result.ChainHash = chainHash
	if verifyErr == nil && chainHash == blockHash {
		result.Verified = true
		// If submit failed but block is in chain, call preciousblock anyway
		if !result.Submitted && blockHash != "" {
			pCtx, pCancel := context.WithTimeout(ctx, timeouts.PreciousTimeout)
			result.PreciousErr = c.PreciousBlock(pCtx, blockHash)
			pCancel()
		}
	}

	return result
}

// bitsToTarget256 converts compact "bits" to a 256-bit big.Int target.
// This is a minimal copy of the logic from shares.compactBitsToTarget,
// used only for cross-validation in validateBlockTemplate to avoid a
// circular dependency on the shares package.
func bitsToTarget256(bits string) *big.Int {
	if len(bits) != 8 {
		return nil
	}
	bitsBytes, err := hex.DecodeString(bits)
	if err != nil {
		return nil
	}
	compact := binary.BigEndian.Uint32(bitsBytes)
	exponent := compact >> 24
	mantissa := compact & 0x007FFFFF
	if mantissa == 0 || mantissa&0x00800000 != 0 {
		return nil
	}
	var target big.Int
	target.SetUint64(uint64(mantissa))
	if exponent <= 3 {
		target.Rsh(&target, uint(8*(3-exponent)))
	} else {
		target.Lsh(&target, uint(8*(exponent-3)))
	}
	if target.Sign() <= 0 {
		return nil
	}
	return &target
}

// ═══════════════════════════════════════════════════════════════════════════════
// AUXILIARY PROOF OF WORK (MERGE MINING) RPC METHODS
// ═══════════════════════════════════════════════════════════════════════════════
//
// These methods are used for merge mining with auxiliary chains like Dogecoin.
// The parent chain (e.g., Litecoin) embeds a commitment to the aux block in its
// coinbase, and the aux chain uses the parent's proof-of-work.
//
// RPC Methods:
//   - getauxblock: Get aux block template from aux chain node
//   - submitauxblock: Submit solved aux block with AuxPoW proof
// ═══════════════════════════════════════════════════════════════════════════════

// GetAuxBlock fetches an auxiliary block template from the daemon.
//
// This RPC is used for merge mining - the aux chain provides a block hash that
// should be embedded in the parent chain's coinbase transaction.
//
// The response format varies by implementation but typically includes:
//
//	{
//	  "hash": "aux block header hash (hex, big-endian)",
//	  "chainid": 98,  // Dogecoin = 98
//	  "previousblockhash": "previous block hash (hex)",
//	  "coinbasevalue": 10000000000000,  // satoshis
//	  "bits": "1b3cc366",  // compact target
//	  "height": 12345678,
//	  "target": "000000000003c366..."  // optional full target
//	}
//
// If called with a hash parameter, it acts as createauxblock instead.
func (c *Client) GetAuxBlock(ctx context.Context) (map[string]interface{}, error) {
	resp, err := c.call(ctx, "getauxblock", nil)
	if err != nil {
		return nil, fmt.Errorf("getauxblock failed: %w", err)
	}

	// Check for null result
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, fmt.Errorf("daemon returned null result for getauxblock")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal aux block: %w", err)
	}

	// CRITICAL FIX: Validate ALL required fields to prevent nil dereferences in callers
	// Without this, callers accessing missing fields will panic on type assertion
	requiredFields := []string{"hash", "chainid", "previousblockhash", "coinbasevalue", "bits", "height"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			return nil, fmt.Errorf("aux block response missing required '%s' field", field)
		}
	}

	return result, nil
}

// SubmitAuxBlock submits a completed auxiliary block with AuxPoW proof.
//
// This is the counterpart to GetAuxBlock - it submits the solved aux block
// along with the proof that the parent block's work is valid.
//
// Parameters:
//   - hash: The aux block hash from getauxblock (hex, big-endian)
//   - auxpow: The serialized AuxPoW proof (hex)
//
// The AuxPoW proof contains:
//   - Parent coinbase transaction
//   - Coinbase merkle branch (path to parent merkle root)
//   - Aux merkle branch (path to aux merkle root)
//   - Parent block header
//
// Returns nil on success, error on failure.
// Common failure reasons:
//   - "stale": Aux block template has changed (new block found)
//   - "duplicate": Block already known to the network
//   - "invalid": AuxPoW proof validation failed
//   - "high-hash": Hash doesn't meet target difficulty
func (c *Client) SubmitAuxBlock(ctx context.Context, hash string, auxpow string) error {
	// CRITICAL FIX: Use submitauxblock RPC method, not getauxblock
	// getauxblock is a read-only method that retrieves aux block templates
	// submitauxblock is the write method that submits solved aux blocks
	// CRITICAL: Use callNoRetry - aux block submission is time-critical, no backoff delays
	resp, err := c.callNoRetry(ctx, "submitauxblock", []interface{}{hash, auxpow})
	if err != nil {
		return fmt.Errorf("submitauxblock failed: %w", err)
	}

	// Check response - Dogecoin returns true on success, error otherwise
	if len(resp.Result) > 0 {
		// Try to parse as boolean first
		var success bool
		if err := json.Unmarshal(resp.Result, &success); err == nil {
			if success {
				return nil
			}
			return fmt.Errorf("aux block rejected (returned false)")
		}

		// Try to parse as string (error message)
		var reason string
		if err := json.Unmarshal(resp.Result, &reason); err == nil && reason != "" {
			return fmt.Errorf("aux block rejected: %s", reason)
		}
	}

	// Empty result or null typically means success for some implementations
	return nil
}

// CreateAuxBlock is an alias for GetAuxBlock with a hash parameter.
// Some implementations use separate RPC methods for getting templates vs submitting.
// This provides the getauxblock(hash) variant used by some daemons.
func (c *Client) CreateAuxBlock(ctx context.Context, hashHex string) (map[string]interface{}, error) {
	resp, err := c.call(ctx, "getauxblock", []interface{}{hashHex})
	if err != nil {
		return nil, fmt.Errorf("createauxblock failed: %w", err)
	}

	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, fmt.Errorf("daemon returned null result for createauxblock")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal createauxblock response: %w", err)
	}

	// AUDIT FIX (C-2): Validate required fields, matching GetAuxBlock validation.
	// Without this, callers accessing missing fields will panic on type assertion.
	requiredFields := []string{"hash", "chainid", "previousblockhash", "coinbasevalue", "bits", "height"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			return nil, fmt.Errorf("createauxblock response missing required '%s' field", field)
		}
	}

	return result, nil
}

// CreateAuxBlockWithAddress uses the Bitcoin Core-style createauxblock RPC.
//
// This is the modern AuxPoW API used by Fractal Bitcoin and other Bitcoin Core-based
// merge-mined coins. Unlike the older getauxblock RPC, this takes an address parameter
// for the coinbase payout.
//
// Parameters:
//   - address: The address to receive the coinbase reward (pool payout address)
//
// Returns the same structure as getauxblock:
//
//	{
//	  "hash": "block header hash (hex, big-endian display order)",
//	  "chainid": 8228,          // for Fractal Bitcoin
//	  "previousblockhash": "...",
//	  "coinbasevalue": 2500000000,
//	  "bits": "1b3cc366",
//	  "height": 12345678,
//	  "target": "..." (optional)
//	}
//
// Use submitauxblock to submit the solved block.
func (c *Client) CreateAuxBlockWithAddress(ctx context.Context, address string) (map[string]interface{}, error) {
	resp, err := c.call(ctx, "createauxblock", []interface{}{address})
	if err != nil {
		return nil, fmt.Errorf("createauxblock failed: %w", err)
	}

	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, fmt.Errorf("daemon returned null result for createauxblock")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal createauxblock response: %w", err)
	}

	// AUDIT FIX (C-2): Validate required fields, matching GetAuxBlock validation.
	// Without this, callers accessing missing fields will panic on type assertion.
	requiredFields := []string{"hash", "chainid", "previousblockhash", "coinbasevalue", "bits", "height"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			return nil, fmt.Errorf("createauxblock response missing required '%s' field", field)
		}
	}

	return result, nil
}
