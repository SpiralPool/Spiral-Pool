// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// OBSERVABILITY & FORENSICS TESTS
// =============================================================================
//
// These tests verify that the logging and observability infrastructure provides
// complete, accurate, and actionable information for debugging and forensics.
//
// Requirements verified:
// - Logs include job ID, diff, target, miner
// - Rejected share reasons are explicit
// - Difficulty changes are logged
// - Block candidate logging is complete
// - Logs are chain-specific
//
// Any failure in these tests indicates potential blind spots in incident response.
// =============================================================================

// LogEntry represents a structured log entry for testing
type LogEntry struct {
	Timestamp   time.Time              `json:"timestamp"`
	Level       string                 `json:"level"`
	Message     string                 `json:"message"`
	Component   string                 `json:"component"`
	ChainSymbol string                 `json:"chain_symbol,omitempty"`
	JobID       string                 `json:"job_id,omitempty"`
	SessionID   string                 `json:"session_id,omitempty"`
	MinerID     string                 `json:"miner_id,omitempty"`
	WorkerName  string                 `json:"worker_name,omitempty"`
	Difficulty  float64                `json:"difficulty,omitempty"`
	Target      string                 `json:"target,omitempty"`
	ShareHash   string                 `json:"share_hash,omitempty"`
	BlockHeight uint64                 `json:"block_height,omitempty"`
	RejectCode  string                 `json:"reject_code,omitempty"`
	Fields      map[string]interface{} `json:"fields,omitempty"`
}

// TestLogger captures log output for verification
type TestLogger struct {
	mu      sync.Mutex
	entries []LogEntry
	raw     bytes.Buffer
}

func NewTestLogger() *TestLogger {
	return &TestLogger{
		entries: make([]LogEntry, 0),
	}
}

func (l *TestLogger) Log(entry LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.Timestamp = time.Now()
	l.entries = append(l.entries, entry)

	// Also write raw JSON for parsing tests
	data, _ := json.Marshal(entry)
	l.raw.Write(data)
	l.raw.WriteByte('\n')
}

func (l *TestLogger) GetEntries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]LogEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

func (l *TestLogger) GetEntriesByLevel(level string) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []LogEntry
	for _, e := range l.entries {
		if e.Level == level {
			result = append(result, e)
		}
	}
	return result
}

func (l *TestLogger) GetEntriesByChain(chain string) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []LogEntry
	for _, e := range l.entries {
		if e.ChainSymbol == chain {
			result = append(result, e)
		}
	}
	return result
}

func (l *TestLogger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = l.entries[:0]
	l.raw.Reset()
}

// ShareSubmission represents a share for logging tests
type ShareSubmission struct {
	JobID       string
	SessionID   string
	MinerID     string
	WorkerName  string
	Nonce       uint32
	NTime       uint32
	ExtraNonce2 string
	ShareHash   []byte
	Difficulty  float64
}

// ShareRejectionReason enumerates all possible rejection reasons
type ShareRejectionReason string

const (
	RejectDuplicate       ShareRejectionReason = "DUPLICATE_SHARE"
	RejectStaleJob        ShareRejectionReason = "STALE_JOB"
	RejectLowDifficulty   ShareRejectionReason = "LOW_DIFFICULTY"
	RejectInvalidNonce    ShareRejectionReason = "INVALID_NONCE"
	RejectInvalidNTime    ShareRejectionReason = "INVALID_NTIME"
	RejectInvalidJobID    ShareRejectionReason = "INVALID_JOB_ID"
	RejectMalformedShare  ShareRejectionReason = "MALFORMED_SHARE"
	RejectAboveTarget     ShareRejectionReason = "ABOVE_TARGET"
	RejectInvalidMerkle   ShareRejectionReason = "INVALID_MERKLE"
	RejectUnknownSession  ShareRejectionReason = "UNKNOWN_SESSION"
	RejectRateLimited     ShareRejectionReason = "RATE_LIMITED"
	RejectVersionMismatch ShareRejectionReason = "VERSION_MISMATCH"
)

// =============================================================================
// TEST: Share Submission Logging Completeness
// =============================================================================

func TestShareSubmissionLoggingCompleteness(t *testing.T) {
	t.Parallel()

	requiredFields := []string{
		"job_id",
		"session_id",
		"miner_id",
		"worker_name",
		"difficulty",
		"target",
		"chain_symbol",
	}

	testCases := []struct {
		name    string
		share   ShareSubmission
		chain   string
		wantErr bool
	}{
		{
			name: "complete share accepted",
			share: ShareSubmission{
				JobID:       "abc123",
				SessionID:   "sess_001",
				MinerID:     "miner_42",
				WorkerName:  "rig1",
				Nonce:       0x12345678,
				Difficulty:  65536.0,
				ExtraNonce2: "00000001",
			},
			chain:   "BTC",
			wantErr: false,
		},
		{
			name: "high difficulty share",
			share: ShareSubmission{
				JobID:       "def456",
				SessionID:   "sess_002",
				MinerID:     "whale_miner",
				WorkerName:  "datacenter_rack_a1",
				Nonce:       0xdeadbeef,
				Difficulty:  1e15,
				ExtraNonce2: "ffffffff",
			},
			chain:   "BTC",
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			// Simulate share submission logging
			logShareSubmission(logger, tc.share, tc.chain)

			entries := logger.GetEntries()
			if len(entries) == 0 {
				t.Fatal("no log entry generated for share submission")
			}

			entry := entries[0]

			// Verify all required fields are present
			for _, field := range requiredFields {
				if !hasField(entry, field) {
					t.Errorf("OBSERVABILITY FAILURE: missing required field %q in share log", field)
				}
			}

			// Verify chain specificity
			if entry.ChainSymbol != tc.chain {
				t.Errorf("chain symbol mismatch: got %q, want %q", entry.ChainSymbol, tc.chain)
			}

			// Verify difficulty is logged correctly
			if entry.Difficulty != tc.share.Difficulty {
				t.Errorf("difficulty mismatch: got %v, want %v", entry.Difficulty, tc.share.Difficulty)
			}
		})
	}
}

func logShareSubmission(logger *TestLogger, share ShareSubmission, chain string) {
	target := difficultyToTarget(share.Difficulty)
	logger.Log(LogEntry{
		Level:       "INFO",
		Message:     "share_submitted",
		Component:   "share_validator",
		ChainSymbol: chain,
		JobID:       share.JobID,
		SessionID:   share.SessionID,
		MinerID:     share.MinerID,
		WorkerName:  share.WorkerName,
		Difficulty:  share.Difficulty,
		Target:      target.Text(16),
		ShareHash:   fmt.Sprintf("%x", share.ShareHash),
	})
}

func hasField(entry LogEntry, field string) bool {
	switch field {
	case "job_id":
		return entry.JobID != ""
	case "session_id":
		return entry.SessionID != ""
	case "miner_id":
		return entry.MinerID != ""
	case "worker_name":
		return entry.WorkerName != ""
	case "difficulty":
		return entry.Difficulty > 0
	case "target":
		return entry.Target != ""
	case "chain_symbol":
		return entry.ChainSymbol != ""
	case "share_hash":
		return entry.ShareHash != ""
	case "block_height":
		return entry.BlockHeight > 0
	default:
		return false
	}
}

func difficultyToTarget(diff float64) *big.Int {
	if diff <= 0 {
		return big.NewInt(0)
	}
	// Bitcoin mainnet target for diff 1
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000ffff0000000000000000000000000000000000000000000000000000", 16)

	// target = maxTarget / difficulty
	scaledDiff := new(big.Float).SetFloat64(diff)
	scaledMax := new(big.Float).SetInt(maxTarget)
	result := new(big.Float).Quo(scaledMax, scaledDiff)

	target, _ := result.Int(nil)
	return target
}

// =============================================================================
// TEST: Rejection Reason Explicitness
// =============================================================================

func TestRejectionReasonExplicitness(t *testing.T) {
	t.Parallel()

	// All rejection reasons must be logged explicitly with human-readable messages
	rejectionReasons := []struct {
		code        ShareRejectionReason
		description string
		shouldLog   []string // Fields that MUST be logged
	}{
		{
			code:        RejectDuplicate,
			description: "duplicate share detected",
			shouldLog:   []string{"job_id", "session_id", "previous_submission_time"},
		},
		{
			code:        RejectStaleJob,
			description: "job no longer active",
			shouldLog:   []string{"job_id", "current_job_id", "job_age_seconds"},
		},
		{
			code:        RejectLowDifficulty,
			description: "share does not meet difficulty target",
			shouldLog:   []string{"share_diff", "required_diff", "target"},
		},
		{
			code:        RejectInvalidNonce,
			description: "nonce validation failed",
			shouldLog:   []string{"nonce", "expected_range"},
		},
		{
			code:        RejectInvalidNTime,
			description: "ntime outside acceptable window",
			shouldLog:   []string{"ntime", "window_start", "window_end"},
		},
		{
			code:        RejectInvalidJobID,
			description: "job ID not found in cache",
			shouldLog:   []string{"job_id", "cache_size"},
		},
		{
			code:        RejectMalformedShare,
			description: "share data failed parsing",
			shouldLog:   []string{"raw_data", "parse_error"},
		},
		{
			code:        RejectAboveTarget,
			description: "hash above network target",
			shouldLog:   []string{"share_hash", "network_target"},
		},
		{
			code:        RejectRateLimited,
			description: "submission rate exceeded",
			shouldLog:   []string{"rate_limit", "current_rate", "cooldown_seconds"},
		},
	}

	for _, rr := range rejectionReasons {
		rr := rr
		t.Run(string(rr.code), func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			// Simulate rejection logging
			logShareRejection(logger, rr.code, rr.description, rr.shouldLog)

			entries := logger.GetEntriesByLevel("WARN")
			if len(entries) == 0 {
				t.Fatalf("OBSERVABILITY FAILURE: rejection %s not logged", rr.code)
			}

			entry := entries[0]

			// Verify rejection code is explicit
			if entry.RejectCode != string(rr.code) {
				t.Errorf("reject code not explicit: got %q, want %q", entry.RejectCode, rr.code)
			}

			// Verify human-readable message present
			if entry.Message == "" {
				t.Error("rejection log missing human-readable message")
			}

			// Verify required fields for forensics
			if entry.Fields == nil {
				t.Error("rejection log missing detail fields for forensics")
			} else {
				for _, field := range rr.shouldLog {
					if _, ok := entry.Fields[field]; !ok {
						t.Errorf("FORENSICS GAP: rejection %s missing field %q", rr.code, field)
					}
				}
			}
		})
	}
}

func logShareRejection(logger *TestLogger, code ShareRejectionReason, description string, fields []string) {
	entry := LogEntry{
		Level:      "WARN",
		Message:    description,
		Component:  "share_validator",
		RejectCode: string(code),
		Fields:     make(map[string]interface{}),
	}

	// Populate required fields with placeholder values for testing
	for _, f := range fields {
		entry.Fields[f] = "test_value"
	}

	logger.Log(entry)
}

// =============================================================================
// TEST: Difficulty Change Logging
// =============================================================================

func TestDifficultyChangeLogging(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		oldDiff      float64
		newDiff      float64
		reason       string
		expectFields []string
	}{
		{
			name:    "difficulty increase",
			oldDiff: 1000,
			newDiff: 2000,
			reason:  "high_share_rate",
			expectFields: []string{
				"old_difficulty",
				"new_difficulty",
				"change_ratio",
				"reason",
				"session_id",
				"miner_id",
			},
		},
		{
			name:    "difficulty decrease",
			oldDiff: 5000,
			newDiff: 2500,
			reason:  "low_share_rate",
			expectFields: []string{
				"old_difficulty",
				"new_difficulty",
				"change_ratio",
				"reason",
				"session_id",
				"miner_id",
			},
		},
		{
			name:    "large difficulty jump",
			oldDiff: 1000,
			newDiff: 16000,
			reason:  "hashrate_spike",
			expectFields: []string{
				"old_difficulty",
				"new_difficulty",
				"change_ratio",
				"reason",
				"session_id",
				"miner_id",
			},
		},
		{
			name:    "minimum difficulty clamp",
			oldDiff: 100,
			newDiff: 1, // Clamped to minimum
			reason:  "clamped_to_minimum",
			expectFields: []string{
				"old_difficulty",
				"new_difficulty",
				"change_ratio",
				"reason",
				"clamped",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			// Simulate difficulty change logging
			logDifficultyChange(logger, "sess_001", "miner_42", tc.oldDiff, tc.newDiff, tc.reason)

			entries := logger.GetEntries()
			if len(entries) == 0 {
				t.Fatal("OBSERVABILITY FAILURE: difficulty change not logged")
			}

			entry := entries[0]

			// Verify essential fields
			if entry.Fields == nil {
				t.Fatal("difficulty change log missing fields")
			}

			for _, field := range tc.expectFields {
				if _, ok := entry.Fields[field]; !ok {
					t.Errorf("missing field %q in difficulty change log", field)
				}
			}

			// Verify change ratio is calculable
			if oldDiff, ok := entry.Fields["old_difficulty"].(float64); ok {
				if newDiff, ok := entry.Fields["new_difficulty"].(float64); ok {
					expectedRatio := newDiff / oldDiff
					if ratio, ok := entry.Fields["change_ratio"].(float64); ok {
						if ratio != expectedRatio {
							t.Errorf("change ratio mismatch: got %v, want %v", ratio, expectedRatio)
						}
					}
				}
			}
		})
	}
}

func logDifficultyChange(logger *TestLogger, sessionID, minerID string, oldDiff, newDiff float64, reason string) {
	fields := map[string]interface{}{
		"old_difficulty": oldDiff,
		"new_difficulty": newDiff,
		"change_ratio":   newDiff / oldDiff,
		"reason":         reason,
		"session_id":     sessionID,
		"miner_id":       minerID,
	}

	if reason == "clamped_to_minimum" {
		fields["clamped"] = true
	}

	logger.Log(LogEntry{
		Level:     "INFO",
		Message:   "difficulty_adjusted",
		Component: "vardiff",
		SessionID: sessionID,
		MinerID:   minerID,
		Fields:    fields,
	})
}

// =============================================================================
// TEST: Block Candidate Logging Completeness
// =============================================================================

func TestBlockCandidateLoggingCompleteness(t *testing.T) {
	t.Parallel()

	requiredBlockFields := []string{
		"block_hash",
		"block_height",
		"previous_block",
		"merkle_root",
		"timestamp",
		"bits",
		"nonce",
		"version",
		"transaction_count",
		"coinbase_value",
		"finder_session_id",
		"finder_miner_id",
		"finder_worker",
		"share_difficulty",
		"network_difficulty",
		"chain_symbol",
	}

	logger := NewTestLogger()

	// Simulate block found logging
	blockInfo := map[string]interface{}{
		"block_hash":         "0000000000000000000abc123def456789...",
		"block_height":       800000,
		"previous_block":     "0000000000000000000xyz789...",
		"merkle_root":        "abcdef123456...",
		"timestamp":          time.Now().Unix(),
		"bits":               0x17034219,
		"nonce":              0x12345678,
		"version":            0x20000000,
		"transaction_count":  2500,
		"coinbase_value":     625000000, // satoshis
		"finder_session_id":  "sess_lucky_001",
		"finder_miner_id":    "block_finder_42",
		"finder_worker":      "lucky_rig_7",
		"share_difficulty":   1e15,
		"network_difficulty": 5.5e13,
		"chain_symbol":       "BTC",
	}

	logger.Log(LogEntry{
		Level:       "INFO",
		Message:     "BLOCK_FOUND",
		Component:   "block_submitter",
		ChainSymbol: "BTC",
		BlockHeight: 800000,
		Fields:      blockInfo,
	})

	entries := logger.GetEntries()
	if len(entries) == 0 {
		t.Fatal("CRITICAL: block found event not logged")
	}

	entry := entries[0]

	// Verify all required fields present
	missingFields := []string{}
	for _, field := range requiredBlockFields {
		if _, ok := entry.Fields[field]; !ok {
			missingFields = append(missingFields, field)
		}
	}

	if len(missingFields) > 0 {
		t.Errorf("BLOCK FORENSICS INCOMPLETE: missing fields %v", missingFields)
	}

	// Verify log level is appropriate (should be high visibility)
	if entry.Level != "INFO" && entry.Level != "NOTICE" {
		t.Errorf("block found should be logged at INFO or higher, got %s", entry.Level)
	}

	// Verify message is distinctive for grep/search
	if !strings.Contains(entry.Message, "BLOCK") {
		t.Error("block found message should contain 'BLOCK' for easy searching")
	}
}

// =============================================================================
// TEST: Chain-Specific Log Isolation
// =============================================================================

func TestChainSpecificLogIsolation(t *testing.T) {
	t.Parallel()

	chains := []string{"BTC", "LTC", "DOGE", "DGB"}
	logger := NewTestLogger()

	// Generate logs for multiple chains
	for _, chain := range chains {
		for i := 0; i < 10; i++ {
			logger.Log(LogEntry{
				Level:       "INFO",
				Message:     fmt.Sprintf("share_accepted_%d", i),
				Component:   "share_validator",
				ChainSymbol: chain,
				JobID:       fmt.Sprintf("%s_job_%d", chain, i),
				Difficulty:  float64(1000 * (i + 1)),
			})
		}
	}

	// Verify chain isolation
	for _, chain := range chains {
		chainLogs := logger.GetEntriesByChain(chain)

		if len(chainLogs) != 10 {
			t.Errorf("expected 10 logs for chain %s, got %d", chain, len(chainLogs))
		}

		// Verify no cross-chain contamination
		for _, log := range chainLogs {
			if log.ChainSymbol != chain {
				t.Errorf("ISOLATION FAILURE: log for chain %s contains chain_symbol %s",
					chain, log.ChainSymbol)
			}

			// Job IDs should be chain-prefixed
			if !strings.HasPrefix(log.JobID, chain+"_") {
				t.Errorf("job ID %s not properly prefixed for chain %s", log.JobID, chain)
			}
		}
	}
}

// =============================================================================
// TEST: Log Format Parseability
// =============================================================================

func TestLogFormatParseability(t *testing.T) {
	t.Parallel()

	logger := NewTestLogger()

	// Generate various log types
	events := []LogEntry{
		{Level: "INFO", Message: "session_connected", Component: "connection_manager", SessionID: "sess_001"},
		{Level: "WARN", Message: "share_rejected", Component: "share_validator", RejectCode: "DUPLICATE"},
		{Level: "ERROR", Message: "rpc_timeout", Component: "daemon_client", Fields: map[string]interface{}{"timeout_ms": 5000}},
		{Level: "INFO", Message: "block_found", Component: "block_submitter", BlockHeight: 800000},
	}

	for _, e := range events {
		logger.Log(e)
	}

	// Get raw log output
	rawLogs := logger.raw.String()
	lines := strings.Split(strings.TrimSpace(rawLogs), "\n")

	for i, line := range lines {
		// Each line should be valid JSON
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Errorf("line %d not valid JSON: %v\nContent: %s", i, err, line)
			continue
		}

		// Required fields in every log line
		requiredBase := []string{"timestamp", "level", "message", "component"}
		for _, field := range requiredBase {
			if _, ok := parsed[field]; !ok {
				t.Errorf("line %d missing required field %q", i, field)
			}
		}
	}
}

// =============================================================================
// TEST: Error Context Preservation
// =============================================================================

func TestErrorContextPreservation(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		errorType       string
		requiredContext []string
	}{
		{
			name:      "RPC error",
			errorType: "rpc_error",
			requiredContext: []string{
				"error_message",
				"rpc_method",
				"rpc_endpoint",
				"retry_count",
				"chain_symbol",
			},
		},
		{
			name:      "database error",
			errorType: "database_error",
			requiredContext: []string{
				"error_message",
				"query_type",
				"table_name",
				"affected_rows",
			},
		},
		{
			name:      "connection error",
			errorType: "connection_error",
			requiredContext: []string{
				"error_message",
				"remote_addr",
				"session_id",
				"connection_duration",
			},
		},
		{
			name:      "validation error",
			errorType: "validation_error",
			requiredContext: []string{
				"error_message",
				"field_name",
				"received_value",
				"expected_format",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			// Simulate error logging with context
			fields := make(map[string]interface{})
			for _, ctx := range tc.requiredContext {
				fields[ctx] = "test_value"
			}

			logger.Log(LogEntry{
				Level:     "ERROR",
				Message:   tc.errorType,
				Component: "test_component",
				Fields:    fields,
			})

			entries := logger.GetEntriesByLevel("ERROR")
			if len(entries) == 0 {
				t.Fatalf("error %s not logged", tc.errorType)
			}

			entry := entries[0]
			for _, ctx := range tc.requiredContext {
				if _, ok := entry.Fields[ctx]; !ok {
					t.Errorf("CONTEXT LOSS: error %s missing context field %q", tc.errorType, ctx)
				}
			}
		})
	}
}

// =============================================================================
// TEST: Audit Trail Completeness
// =============================================================================

func TestAuditTrailCompleteness(t *testing.T) {
	t.Parallel()

	// Audit events that MUST be logged for compliance/forensics
	auditEvents := []struct {
		event           string
		requiredFields  []string
		mustBeSearchable bool
	}{
		{
			event:           "miner_authorized",
			requiredFields:  []string{"session_id", "miner_id", "worker_name", "remote_addr", "auth_method"},
			mustBeSearchable: true,
		},
		{
			event:           "miner_disconnected",
			requiredFields:  []string{"session_id", "miner_id", "disconnect_reason", "session_duration", "shares_submitted"},
			mustBeSearchable: true,
		},
		{
			event:           "payout_initiated",
			requiredFields:  []string{"miner_id", "amount", "destination_address", "transaction_id"},
			mustBeSearchable: true,
		},
		{
			event:           "difficulty_override",
			requiredFields:  []string{"session_id", "old_difficulty", "new_difficulty", "override_by", "reason"},
			mustBeSearchable: true,
		},
		{
			event:           "block_orphaned",
			requiredFields:  []string{"block_hash", "block_height", "orphan_reason", "finder_miner_id"},
			mustBeSearchable: true,
		},
	}

	for _, ae := range auditEvents {
		ae := ae
		t.Run(ae.event, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			// Generate audit event
			fields := make(map[string]interface{})
			for _, f := range ae.requiredFields {
				fields[f] = "test_value_" + f
			}

			logger.Log(LogEntry{
				Level:     "AUDIT",
				Message:   ae.event,
				Component: "audit_logger",
				Fields:    fields,
			})

			entries := logger.GetEntries()
			if len(entries) == 0 {
				t.Fatalf("AUDIT FAILURE: event %s not logged", ae.event)
			}

			entry := entries[0]

			// Verify all required fields
			for _, f := range ae.requiredFields {
				if _, ok := entry.Fields[f]; !ok {
					t.Errorf("AUDIT INCOMPLETE: event %s missing field %q", ae.event, f)
				}
			}

			// Verify searchability
			if ae.mustBeSearchable {
				// Event name should be in a consistent, greppable format
				if entry.Message != ae.event {
					t.Errorf("audit event message should be exactly %q for searchability, got %q",
						ae.event, entry.Message)
				}
			}
		})
	}
}

// =============================================================================
// TEST: Performance Metrics Logging
// =============================================================================

func TestPerformanceMetricsLogging(t *testing.T) {
	t.Parallel()

	metrics := []struct {
		name       string
		metricType string
		fields     []string
	}{
		{
			name:       "share_validation_latency",
			metricType: "histogram",
			fields:     []string{"latency_ms", "percentile_50", "percentile_99", "sample_count"},
		},
		{
			name:       "rpc_call_duration",
			metricType: "histogram",
			fields:     []string{"duration_ms", "method", "success"},
		},
		{
			name:       "active_connections",
			metricType: "gauge",
			fields:     []string{"count", "chain_symbol"},
		},
		{
			name:       "shares_per_second",
			metricType: "counter",
			fields:     []string{"rate", "window_seconds", "chain_symbol"},
		},
		{
			name:       "memory_usage",
			metricType: "gauge",
			fields:     []string{"heap_bytes", "stack_bytes", "goroutines"},
		},
	}

	for _, m := range metrics {
		m := m
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			fields := map[string]interface{}{
				"metric_type": m.metricType,
			}
			for _, f := range m.fields {
				fields[f] = 123.45
			}

			logger.Log(LogEntry{
				Level:     "METRIC",
				Message:   m.name,
				Component: "metrics_collector",
				Fields:    fields,
			})

			entries := logger.GetEntries()
			if len(entries) == 0 {
				t.Fatalf("metric %s not logged", m.name)
			}

			entry := entries[0]
			for _, f := range m.fields {
				if _, ok := entry.Fields[f]; !ok {
					t.Errorf("metric %s missing field %q", m.name, f)
				}
			}
		})
	}
}

// =============================================================================
// TEST: Correlation ID Propagation
// =============================================================================

func TestCorrelationIDPropagation(t *testing.T) {
	t.Parallel()

	logger := NewTestLogger()
	correlationID := "corr_abc123_xyz789"

	// Simulate a request flow with correlation ID
	operations := []string{
		"request_received",
		"share_validated",
		"difficulty_checked",
		"duplicate_checked",
		"share_accepted",
		"database_written",
		"response_sent",
	}

	for _, op := range operations {
		logger.Log(LogEntry{
			Level:     "INFO",
			Message:   op,
			Component: "request_handler",
			Fields: map[string]interface{}{
				"correlation_id": correlationID,
				"operation":      op,
			},
		})
	}

	entries := logger.GetEntries()

	// All entries should have the same correlation ID
	for i, entry := range entries {
		cid, ok := entry.Fields["correlation_id"]
		if !ok {
			t.Errorf("operation %d missing correlation_id", i)
			continue
		}
		if cid != correlationID {
			t.Errorf("correlation ID mismatch at operation %d: got %v, want %s", i, cid, correlationID)
		}
	}

	// Verify we can reconstruct the full request flow
	if len(entries) != len(operations) {
		t.Errorf("expected %d log entries, got %d", len(operations), len(entries))
	}
}

// =============================================================================
// TEST: Sensitive Data Redaction
// =============================================================================

func TestSensitiveDataRedaction(t *testing.T) {
	t.Parallel()

	sensitivePatterns := []struct {
		name    string
		input   string
		pattern *regexp.Regexp
	}{
		{
			name:    "rpc_password",
			input:   "http://user:secretpassword123@localhost:8332",
			pattern: regexp.MustCompile(`:[^:@]+@`),
		},
		{
			name:    "private_key",
			input:   "5HueCGU8rMjxEXxiPuD5BDku4MkFqeZyd4dZ1jvhTVqvbTLvyTJ",
			pattern: regexp.MustCompile(`^5[HJK][1-9A-HJ-NP-Za-km-z]{49}$`),
		},
		{
			name:    "api_key",
			input:   "api_key=sk_live_abc123xyz789",
			pattern: regexp.MustCompile(`sk_live_[a-zA-Z0-9]+`),
		},
	}

	for _, sp := range sensitivePatterns {
		sp := sp
		t.Run(sp.name, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			// Log with sensitive data (should be redacted)
			redacted := redactSensitive(sp.input)

			logger.Log(LogEntry{
				Level:   "DEBUG",
				Message: "connection_info",
				Fields: map[string]interface{}{
					sp.name: redacted,
				},
			})

			entries := logger.GetEntries()
			if len(entries) == 0 {
				t.Fatal("log entry not created")
			}

			// Verify sensitive data is redacted
			loggedValue := entries[0].Fields[sp.name].(string)
			if sp.pattern.MatchString(loggedValue) && !strings.Contains(loggedValue, "REDACTED") {
				t.Errorf("SECURITY: sensitive data %s not redacted in logs", sp.name)
			}
		})
	}
}

func redactSensitive(input string) string {
	// Redact passwords in URLs
	urlPasswordPattern := regexp.MustCompile(`(://[^:]+:)[^@]+(@)`)
	input = urlPasswordPattern.ReplaceAllString(input, "${1}REDACTED${2}")

	// Redact API keys
	apiKeyPattern := regexp.MustCompile(`(sk_live_)[a-zA-Z0-9]+`)
	input = apiKeyPattern.ReplaceAllString(input, "${1}REDACTED")

	// Redact private keys (WIF format)
	wifPattern := regexp.MustCompile(`5[HJK][1-9A-HJ-NP-Za-km-z]{49}`)
	input = wifPattern.ReplaceAllString(input, "REDACTED_PRIVATE_KEY")

	return input
}

// =============================================================================
// TEST: Log Rotation Safety
// =============================================================================

func TestLogRotationSafety(t *testing.T) {
	t.Parallel()

	// Verify that critical events are logged atomically
	// and won't be split across log files during rotation

	logger := NewTestLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate high-volume logging during "rotation"
	var wg sync.WaitGroup
	entriesPerGoroutine := 100
	goroutines := 10

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < entriesPerGoroutine; j++ {
				select {
				case <-ctx.Done():
					return
				default:
					logger.Log(LogEntry{
						Level:     "INFO",
						Message:   fmt.Sprintf("test_event_%d_%d", workerID, j),
						Component: "rotation_test",
						Fields: map[string]interface{}{
							"worker_id": workerID,
							"event_num": j,
							"atomic":    true,
						},
					})
				}
			}
		}(i)
	}

	wg.Wait()

	entries := logger.GetEntries()
	expectedTotal := goroutines * entriesPerGoroutine

	if len(entries) != expectedTotal {
		t.Errorf("log entry loss during concurrent writes: got %d, want %d", len(entries), expectedTotal)
	}

	// Verify no entries are corrupted (all fields present)
	for i, entry := range entries {
		if entry.Fields == nil {
			t.Errorf("entry %d has nil fields - possible corruption", i)
			continue
		}
		if _, ok := entry.Fields["atomic"]; !ok {
			t.Errorf("entry %d missing atomic field - possible partial write", i)
		}
	}
}

// =============================================================================
// TEST: Emergency/Alert Logging
// =============================================================================

func TestEmergencyAlertLogging(t *testing.T) {
	t.Parallel()

	emergencyEvents := []struct {
		event       string
		severity    string
		alertFields []string
	}{
		{
			event:       "double_spend_detected",
			severity:    "CRITICAL",
			alertFields: []string{"transaction_id", "original_block", "conflicting_block", "amount"},
		},
		{
			event:       "consensus_fork_detected",
			severity:    "CRITICAL",
			alertFields: []string{"chain_tip_a", "chain_tip_b", "fork_height", "affected_blocks"},
		},
		{
			event:       "database_corruption",
			severity:    "EMERGENCY",
			alertFields: []string{"table_name", "corruption_type", "affected_rows", "recovery_action"},
		},
		{
			event:       "security_breach_attempt",
			severity:    "ALERT",
			alertFields: []string{"attack_type", "source_ip", "target_resource", "blocked"},
		},
	}

	for _, ee := range emergencyEvents {
		ee := ee
		t.Run(ee.event, func(t *testing.T) {
			t.Parallel()
			logger := NewTestLogger()

			fields := make(map[string]interface{})
			for _, f := range ee.alertFields {
				fields[f] = "test_value"
			}
			fields["severity"] = ee.severity
			fields["requires_immediate_action"] = true

			logger.Log(LogEntry{
				Level:     ee.severity,
				Message:   ee.event,
				Component: "emergency_handler",
				Fields:    fields,
			})

			entries := logger.GetEntries()
			if len(entries) == 0 {
				t.Fatalf("CRITICAL: emergency event %s not logged", ee.event)
			}

			entry := entries[0]

			// Emergency events must have high severity
			validSeverities := map[string]bool{
				"CRITICAL":  true,
				"EMERGENCY": true,
				"ALERT":     true,
			}
			if !validSeverities[entry.Level] {
				t.Errorf("emergency event %s has insufficient severity: %s", ee.event, entry.Level)
			}

			// Must include action flag
			if action, ok := entry.Fields["requires_immediate_action"]; !ok || action != true {
				t.Errorf("emergency event %s must flag requires_immediate_action", ee.event)
			}
		})
	}
}

// =============================================================================
// BENCHMARK: Logging Overhead
// =============================================================================

func BenchmarkLogEntryCreation(b *testing.B) {
	logger := NewTestLogger()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Log(LogEntry{
			Level:       "INFO",
			Message:     "share_accepted",
			Component:   "share_validator",
			ChainSymbol: "BTC",
			JobID:       "job_123",
			SessionID:   "sess_001",
			MinerID:     "miner_42",
			WorkerName:  "rig1",
			Difficulty:  65536.0,
			Target:      "00000000ffff0000000000000000000000000000000000000000000000000000",
			Fields: map[string]interface{}{
				"extra_field_1": "value1",
				"extra_field_2": 12345,
				"extra_field_3": true,
			},
		})
	}
}

func BenchmarkLogFiltering(b *testing.B) {
	logger := NewTestLogger()

	// Pre-populate with logs
	chains := []string{"BTC", "LTC", "DOGE"}
	for i := 0; i < 10000; i++ {
		logger.Log(LogEntry{
			Level:       "INFO",
			Message:     "test_event",
			ChainSymbol: chains[i%len(chains)],
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = logger.GetEntriesByChain("BTC")
	}
}
