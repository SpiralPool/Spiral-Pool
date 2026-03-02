// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
//
// Package v1 implements Stratum V1 (JSON-RPC over TCP) protocol handling.
//
// The protocol follows the standard Stratum mining protocol specification
// as originally developed for Bitcoin pooled mining, with extensions for
// version rolling (BIP320) and other modern features.
package v1

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// SECURITY: Input validation constants
const (
	maxWorkerNameLen = 256 // Maximum length for worker name (address.worker)
	maxUserAgentLen  = 256 // Maximum length for user-agent string
)

// validWorkerName matches safe worker name characters
// HASHRATE RENTAL COMPATIBLE: Relaxed to allow most printable ASCII characters
// Blocks: control chars, quotes, backslashes, semicolons (SQL), angle brackets (XSS)
// Allows: alphanumeric, dots, dashes, underscores, colons, plus, equals, spaces
var validWorkerName = regexp.MustCompile(`^[a-zA-Z0-9._\-:+=@ ]+$`)

// RED-TEAM: JSON structure limits to prevent CPU exhaustion attacks
const (
	maxJSONDepth    = 32  // Maximum nesting depth
	maxArrayLen     = 100 // Maximum array elements
	maxObjectKeys   = 50  // Maximum object keys
)

// validateJSONStructure performs a quick scan of JSON to detect malicious structures
// before full parsing. This prevents CPU exhaustion from deeply nested JSON.
func validateJSONStructure(data []byte) error {
	depth := 0
	inString := false
	escaped := false

	// Per-scope tracking: fixed-size stacks (no heap allocation).
	// scopeType: 'a' = array, 'o' = object
	var scopeType [maxJSONDepth + 1]byte
	var commaCount [maxJSONDepth + 1]int

	for i := 0; i < len(data); i++ {
		b := data[i]

		if escaped {
			escaped = false
			continue
		}

		if b == '\\' && inString {
			escaped = true
			continue
		}

		if b == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch b {
		case '{':
			depth++
			if depth > maxJSONDepth {
				return fmt.Errorf("JSON nesting too deep (max %d)", maxJSONDepth)
			}
			scopeType[depth] = 'o'
			commaCount[depth] = 0
		case '[':
			depth++
			if depth > maxJSONDepth {
				return fmt.Errorf("JSON nesting too deep (max %d)", maxJSONDepth)
			}
			scopeType[depth] = 'a'
			commaCount[depth] = 0
		case '}', ']':
			if depth <= 0 {
				return fmt.Errorf("JSON structure invalid: unexpected closing bracket")
			}
			depth--
		case ',':
			if depth > 0 {
				commaCount[depth]++
				if scopeType[depth] == 'a' && commaCount[depth] > maxArrayLen {
					return fmt.Errorf("JSON array too large (max %d elements)", maxArrayLen)
				}
				if scopeType[depth] == 'o' && commaCount[depth] > maxObjectKeys {
					return fmt.Errorf("JSON object too large (max %d keys)", maxObjectKeys)
				}
			}
		}
	}

	return nil
}

// Handler processes Stratum V1 JSON-RPC messages.
type Handler struct {
	// Callbacks
	onShare           func(*protocol.Share) *protocol.ShareResult
	getDiffForMiner   func(userAgent string) float64             // Optional: returns difficulty based on miner type
	getDiffForMinerIP func(userAgent, remoteAddr string) float64 // Optional: returns difficulty with IP lookup
	allowShare        func(remoteAddr string) (bool, string)     // SECURITY: Rate limiting callback (SEC-05)

	// Configuration
	initialDifficulty float64
	versionRolling    bool
	versionMask       uint32
}

// Request represents a JSON-RPC request.
type Request struct {
	ID     interface{}   `json:"id"`
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

// Response represents a JSON-RPC response.
type Response struct {
	ID     interface{} `json:"id"`
	Result interface{} `json:"result"`
	Error  interface{} `json:"error"`
}

// Notification represents a JSON-RPC notification (no ID).
type Notification struct {
	ID     interface{}   `json:"id"`
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

// NewHandler creates a new V1 protocol handler.
func NewHandler(initialDiff float64, versionRolling bool, versionMask uint32) *Handler {
	return &Handler{
		initialDifficulty: initialDiff,
		versionRolling:    versionRolling,
		versionMask:       versionMask,
	}
}

// HandleMessage processes a JSON-RPC message.
func (h *Handler) HandleMessage(session *protocol.Session, data []byte) ([]byte, error) {
	// RED-TEAM: Validate JSON structure before parsing to prevent CPU exhaustion
	// from deeply nested JSON or massive arrays
	if err := validateJSONStructure(data); err != nil {
		return nil, fmt.Errorf("malformed JSON structure: %w", err)
	}

	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	switch req.Method {
	case protocol.Methods.Subscribe:
		return h.handleSubscribe(session, &req)
	case protocol.Methods.Authorize:
		return h.handleAuthorize(session, &req)
	case protocol.Methods.Configure:
		return h.handleConfigure(session, &req)
	case protocol.Methods.SuggestDifficulty:
		return h.handleSuggestDifficulty(session, &req)
	case protocol.Methods.ExtranonceSubscribe:
		return h.handleExtranonceSubscribe(session, &req)
	case protocol.Methods.Ping:
		return h.handlePing(session, &req)
	case protocol.Methods.Submit:
		return h.handleSubmit(session, &req)
	default:
		return h.errorResponse(req.ID, 20, "Unknown method", nil)
	}
}

// handleSubscribe processes mining.subscribe.
func (h *Handler) handleSubscribe(session *protocol.Session, req *Request) ([]byte, error) {
	// Parse user agent if provided
	if len(req.Params) > 0 {
		if ua, ok := req.Params[0].(string); ok {
			// SECURITY: Truncate user-agent to prevent memory exhaustion and log injection
			if len(ua) > maxUserAgentLen {
				ua = ua[:maxUserAgentLen]
			}
			session.UserAgent = ua
		}
	}

	// SECURITY: Use atomic method to prevent race conditions
	session.SetSubscribed(true)

	// Build subscription response
	// Format: [[["mining.set_difficulty", "subscription_id"], ["mining.notify", "subscription_id"]], extranonce1, extranonce2_size]
	subscriptions := [][]interface{}{
		{"mining.set_difficulty", fmt.Sprintf("%x", session.ID)},
		{"mining.notify", fmt.Sprintf("%x", session.ID)},
	}

	result := []interface{}{
		subscriptions,
		session.ExtraNonce1,
		session.ExtraNonce2Size,
	}

	return h.successResponse(req.ID, result)
}

// handleAuthorize processes mining.authorize.
func (h *Handler) handleAuthorize(session *protocol.Session, req *Request) ([]byte, error) {
	// RED-TEAM: FSM enforcement - must subscribe before authorize
	// Without subscription, session has no ExtraNonce1 and shares would be invalid
	if !session.IsSubscribed() {
		return h.errorResponse(req.ID, 25, "Not subscribed", nil)
	}

	// SECURITY: Prevent re-authorization — worker identity locked after first auth.
	// Return success for client compatibility (cgminer retries) but preserve original identity.
	// FIX STR-H1: Without this, an attacker could hijack a session by re-authorizing
	// with a different worker name after the initial authorization.
	if session.IsAuthorized() {
		return h.successResponse(req.ID, true)
	}

	if len(req.Params) < 1 {
		return h.errorResponse(req.ID, 21, "Missing worker name", nil)
	}

	workerName, ok := req.Params[0].(string)
	if !ok {
		return h.errorResponse(req.ID, 21, "Invalid worker name", nil)
	}

	// SECURITY: Validate worker name length to prevent memory exhaustion
	if len(workerName) == 0 {
		return h.errorResponse(req.ID, 21, "Empty worker name", nil)
	}
	if len(workerName) > maxWorkerNameLen {
		return h.errorResponse(req.ID, 21, "Worker name too long", nil)
	}

	// SECURITY: Validate worker name characters to prevent injection attacks
	// Allows: alphanumeric, dots (for address.worker), dashes, underscores
	if !validWorkerName.MatchString(workerName) {
		return h.errorResponse(req.ID, 21, "Invalid characters in worker name", nil)
	}

	// Parse worker name (format: address.worker or just address)
	session.MinerAddress, session.WorkerName = parseWorkerName(workerName)
	// SECURITY: Use atomic method to prevent race conditions
	session.SetAuthorized(true)

	// Set initial difficulty based on miner type (IP-based hints preferred, fallback to user-agent)
	initialDiff := h.initialDifficulty

	// Try IP-aware router first (uses device hints from Sentinel)
	if h.getDiffForMinerIP != nil && session.RemoteAddr != "" {
		if routedDiff := h.getDiffForMinerIP(session.UserAgent, session.RemoteAddr); routedDiff > 0 {
			initialDiff = routedDiff
		}
	} else if h.getDiffForMiner != nil && session.UserAgent != "" {
		// Fall back to user-agent only detection
		if routedDiff := h.getDiffForMiner(session.UserAgent); routedDiff > 0 {
			initialDiff = routedDiff
		}
	}
	session.SetDifficulty(initialDiff)

	return h.successResponse(req.ID, true)
}

// handleSubmit processes mining.submit.
func (h *Handler) handleSubmit(session *protocol.Session, req *Request) ([]byte, error) {
	// RED-TEAM: FSM enforcement - must be both subscribed AND authorized
	// Defense-in-depth: subscription sets ExtraNonce1, authorization allows shares
	if !session.IsSubscribed() {
		return h.errorResponse(req.ID, 25, "Not subscribed", nil)
	}
	// SECURITY: Use atomic method to prevent race conditions
	if !session.IsAuthorized() {
		return h.errorResponse(req.ID, 24, "Unauthorized", nil)
	}

	// SECURITY: Check share rate limiting (SEC-05 fix)
	if h.allowShare != nil {
		allowed, reason := h.allowShare(session.RemoteAddr)
		if !allowed {
			return h.errorResponse(req.ID, 25, reason, nil)
		}
	}

	// Parse share params: [worker, jobId, extranonce2, ntime, nonce, (optional) version_bits]
	if len(req.Params) < 5 {
		return h.errorResponse(req.ID, 21, "Invalid share parameters", nil)
	}

	share := &protocol.Share{
		SessionID:    session.ID,
		MinerAddress: session.MinerAddress,
		WorkerName:   session.WorkerName,
		IPAddress:    session.RemoteAddr,
		UserAgent:    session.UserAgent,
		SubmittedAt:  time.Now(),
		// CRITICAL: Include session's ExtraNonce1 for block construction
		// Without this, the coinbase transaction will be invalid and blocks will be rejected
		ExtraNonce1: session.ExtraNonce1,
	}

	// Extract params with STRICT validation
	// SECURITY: All params must be present and correct type - reject if missing/wrong type
	// This prevents silent failures that could cause invalid block submissions

	// JobID (required)
	jobID, ok := req.Params[1].(string)
	if !ok || jobID == "" {
		return h.errorResponse(req.ID, 21, "Missing or invalid job_id", nil)
	}
	share.JobID = jobID

	// ExtraNonce2 (required)
	en2, ok := req.Params[2].(string)
	if !ok {
		return h.errorResponse(req.ID, 21, "Missing or invalid extranonce2", nil)
	}
	// Validate extranonce2 length and format
	expectedLen := session.ExtraNonce2Size * 2 // hex chars (4 bytes = 8 hex chars)
	if len(en2) != expectedLen {
		return h.errorResponse(req.ID, 21,
			fmt.Sprintf("Invalid extranonce2 length: got %d, expected %d", len(en2), expectedLen),
			nil)
	}
	en2Bytes, err := hex.DecodeString(en2)
	if err != nil {
		return h.errorResponse(req.ID, 21, "Invalid extranonce2 hex", nil)
	}
	// SECURITY: Validate extranonce2 length matches expected size
	if len(en2Bytes) != session.ExtraNonce2Size {
		return h.errorResponse(req.ID, 21, "Extranonce2 length mismatch", nil)
	}
	// SECURITY: Validate extranonce2 is within valid range to prevent nonce space manipulation
	// Reject values in the last 256 positions of the nonce space to prevent wrap-around attacks
	switch session.ExtraNonce2Size {
	case 8:
		// CRITICAL FIX: Added missing 8-byte validation case
		// 8-byte extranonce2 used by some ASIC miners for larger nonce space
		en2Val := uint64(en2Bytes[0])<<56 | uint64(en2Bytes[1])<<48 | uint64(en2Bytes[2])<<40 | uint64(en2Bytes[3])<<32 |
			uint64(en2Bytes[4])<<24 | uint64(en2Bytes[5])<<16 | uint64(en2Bytes[6])<<8 | uint64(en2Bytes[7])
		if en2Val > 0xFFFFFFFFFFFFFF00 {
			return h.errorResponse(req.ID, 21, "Extranonce2 value out of valid range", nil)
		}
	case 4:
		en2Val := uint32(en2Bytes[0])<<24 | uint32(en2Bytes[1])<<16 | uint32(en2Bytes[2])<<8 | uint32(en2Bytes[3])
		if en2Val > 0xFFFFFF00 {
			return h.errorResponse(req.ID, 21, "Extranonce2 value out of valid range", nil)
		}
	case 2:
		en2Val := uint16(en2Bytes[0])<<8 | uint16(en2Bytes[1])
		if en2Val > 0xFF00 {
			return h.errorResponse(req.ID, 21, "Extranonce2 value out of valid range", nil)
		}
	case 1:
		if en2Bytes[0] > 0xF0 {
			return h.errorResponse(req.ID, 21, "Extranonce2 value out of valid range", nil)
		}
	default:
		// SECURITY: Reject unsupported extranonce2 sizes
		return h.errorResponse(req.ID, 21, "Unsupported extranonce2 size", nil)
	}
	share.ExtraNonce2 = en2

	// NTime (required)
	ntime, ok := req.Params[3].(string)
	if !ok {
		return h.errorResponse(req.ID, 21, "Missing or invalid ntime", nil)
	}
	// Basic ntime validation (8 hex chars = 4 bytes)
	if len(ntime) != 8 {
		return h.errorResponse(req.ID, 21, "Invalid ntime length", nil)
	}
	if _, err := hex.DecodeString(ntime); err != nil {
		return h.errorResponse(req.ID, 21, "Invalid ntime hex", nil)
	}
	share.NTime = ntime

	// Nonce (required)
	nonce, ok := req.Params[4].(string)
	if !ok {
		return h.errorResponse(req.ID, 21, "Missing or invalid nonce", nil)
	}
	// Validate nonce (8 hex chars = 4 bytes)
	if len(nonce) != 8 {
		return h.errorResponse(req.ID, 21, "Invalid nonce length", nil)
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return h.errorResponse(req.ID, 21, "Invalid nonce hex", nil)
	}
	share.Nonce = nonce

	// Optional version bits (BIP320)
	// SECURITY: Validate length BEFORE hex.DecodeString to prevent DoS via memory exhaustion
	// A malicious miner could send a multi-MB string that gets allocated during decode
	if len(req.Params) > 5 {
		if vb, ok := req.Params[5].(string); ok && len(vb) == 8 { // Must be exactly 8 hex chars (4 bytes)
			if vbBytes, err := hex.DecodeString(vb); err == nil && len(vbBytes) == 4 {
				share.VersionBits = uint32(vbBytes[0])<<24 | uint32(vbBytes[1])<<16 |
					uint32(vbBytes[2])<<8 | uint32(vbBytes[3])
			}
		}
	}

	// Get difficulty for share validation using job-based tracking.
	// Shares for jobs issued before a difficulty change use the old difficulty.
	// This prevents rejecting shares that were in flight when difficulty changed.
	// Parse job ID from hex string to uint64 for comparison.
	// If parsing fails, use current difficulty directly (don't pass jobIDNum=0
	// which could falsely match pre-change jobs).
	if jobIDNum, err := strconv.ParseUint(jobID, 16, 64); err == nil {
		share.Difficulty = session.GetDifficultyForJob(jobIDNum)
	} else {
		share.Difficulty = session.GetDifficulty()
	}

	// Set MinDifficulty for fallback validation during vardiff transitions.
	// This handles cgminer/Avalon which doesn't immediately apply new difficulty
	// to work-in-progress. The validator will accept shares at MinDifficulty
	// if they don't meet the expected difficulty.
	share.MinDifficulty = session.GetMinDifficultyInWindow()

	// Validate share via callback
	var result *protocol.ShareResult
	if h.onShare != nil {
		result = h.onShare(share)
	} else {
		result = &protocol.ShareResult{Accepted: true}
	}

	// Update session stats (using atomic methods)
	if result.Accepted {
		session.IncrementValidShares()
		session.SetLastShareTime(time.Now())
		return h.successResponse(req.ID, true)
	}

	// Handle rejection (using atomic methods)
	switch result.RejectReason {
	case protocol.RejectReasonStale:
		session.IncrementStaleShares()
	default:
		session.IncrementInvalidShares()
	}

	return h.errorResponse(req.ID, 23, result.RejectReason, nil)
}

// BuildNotify creates a mining.notify message.
func (h *Handler) BuildNotify(job *protocol.Job) ([]byte, error) {
	notification := Notification{
		ID:     nil,
		Method: protocol.Methods.Notify,
		Params: []interface{}{
			job.ID,
			job.PrevBlockHash,
			job.CoinBase1,
			job.CoinBase2,
			job.MerkleBranches,
			job.Version,
			job.NBits,
			job.NTime,
			job.CleanJobs,
		},
	}

	data, err := json.Marshal(notification)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// BuildSetDifficulty creates a mining.set_difficulty message.
func (h *Handler) BuildSetDifficulty(difficulty float64) ([]byte, error) {
	notification := Notification{
		ID:     nil,
		Method: protocol.Methods.SetDifficulty,
		Params: []interface{}{difficulty},
	}

	data, err := json.Marshal(notification)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// BuildSetExtranonce creates a mining.set_extranonce message.
// Hashrate rental: Used for dynamic extranonce assignment to multiplexed miners.
// Params: [extranonce1, extranonce2_size]
func (h *Handler) BuildSetExtranonce(extranonce1 string, extranonce2Size int) ([]byte, error) {
	notification := Notification{
		ID:     nil,
		Method: protocol.Methods.SetExtranonce,
		Params: []interface{}{extranonce1, extranonce2Size},
	}

	data, err := json.Marshal(notification)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// BuildShowMessage creates a client.show_message notification.
// This sends a message to display on the miner's screen/LED.
// Supported by cgminer, Avalon, and other stratum-compatible miners.
func (h *Handler) BuildShowMessage(message string) ([]byte, error) {
	notification := Notification{
		ID:     nil,
		Method: protocol.Methods.ShowMessage,
		Params: []interface{}{message},
	}

	data, err := json.Marshal(notification)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// successResponse creates a success response.
func (h *Handler) successResponse(id interface{}, result interface{}) ([]byte, error) {
	resp := Response{
		ID:     id,
		Result: result,
		Error:  nil,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// errorResponse creates an error response.
func (h *Handler) errorResponse(id interface{}, code int, message string, data interface{}) ([]byte, error) {
	resp := Response{
		ID:     id,
		Result: nil,
		Error:  []interface{}{code, message, data},
	}
	respData, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	return append(respData, '\n'), nil
}

// SetShareHandler sets the share callback.
func (h *Handler) SetShareHandler(handler func(*protocol.Share) *protocol.ShareResult) {
	h.onShare = handler
}

// SetMinerDifficultyRouter sets the callback for auto-detecting miner difficulty from user-agent.
// This enables single-port operation with automatic difficulty routing for different miner types.
func (h *Handler) SetMinerDifficultyRouter(router func(userAgent string) float64) {
	h.getDiffForMiner = router
}

// SetMinerDifficultyRouterWithIP sets the callback for auto-detecting miner difficulty using IP hints.
// This enables IP-based device classification from Spiral Sentinel's HTTP API discovery.
// When set, this takes priority over the user-agent only router.
func (h *Handler) SetMinerDifficultyRouterWithIP(router func(userAgent, remoteAddr string) float64) {
	h.getDiffForMinerIP = router
}

// SetShareRateLimiter sets the callback for share rate limiting (SEC-05 fix).
// The callback receives the remote address string and returns (allowed, reason).
func (h *Handler) SetShareRateLimiter(limiter func(remoteAddr string) (bool, string)) {
	h.allowShare = limiter
}

// parseWorkerName splits "address.worker" into components.
func parseWorkerName(name string) (address, worker string) {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i], name[i+1:]
		}
	}
	return name, "default"
}

// handleConfigure processes mining.configure (BIP310/BIP320 version rolling).
func (h *Handler) handleConfigure(session *protocol.Session, req *Request) ([]byte, error) {
	result := make(map[string]interface{})

	if len(req.Params) >= 1 {
		extensions, ok := req.Params[0].([]interface{})
		if ok {
			for _, ext := range extensions {
				extName, ok := ext.(string)
				if !ok {
					continue
				}

				switch extName {
				case "version-rolling":
					if h.versionRolling {
						session.VersionRollingMask = h.versionMask
						result["version-rolling"] = true
						result["version-rolling.mask"] = fmt.Sprintf("%08x", h.versionMask)
					} else {
						result["version-rolling"] = false
					}
				case "minimum-difficulty":
					result["minimum-difficulty"] = true
				case "subscribe-extranonce":
					result["subscribe-extranonce"] = true
				default:
					result[extName] = false
				}
			}
		}
	}

	return h.successResponse(req.ID, result)
}

// handleSuggestDifficulty processes mining.suggest_difficulty from miners.
// Antminers and other ASICs use this to suggest their preferred difficulty.
// We acknowledge the suggestion but use our own vardiff algorithm.
func (h *Handler) handleSuggestDifficulty(session *protocol.Session, req *Request) ([]byte, error) {
	// Parse suggested difficulty if provided
	if len(req.Params) > 0 {
		if diff, ok := req.Params[0].(float64); ok && diff > 0 {
			// Log the suggestion but don't override our vardiff
			// The miner will receive proper difficulty via mining.set_difficulty
			_ = diff // Acknowledged but not used
		}
	}
	// Return success - miner's suggestion is noted
	return h.successResponse(req.ID, true)
}

// handleExtranonceSubscribe processes mining.extranonce.subscribe.
// This allows miners to receive extranonce updates mid-session.
func (h *Handler) handleExtranonceSubscribe(session *protocol.Session, req *Request) ([]byte, error) {
	// We support extranonce subscription
	return h.successResponse(req.ID, true)
}

// handlePing processes mining.ping keep-alive messages.
func (h *Handler) handlePing(session *protocol.Session, req *Request) ([]byte, error) {
	return h.successResponse(req.ID, "pong")
}
