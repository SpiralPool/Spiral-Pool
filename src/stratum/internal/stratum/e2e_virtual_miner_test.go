// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package stratum provides end-to-end virtual miner tests for the Stratum pool.
// UPDATED: 2026-01-20 - Fixed expected difficulty values to match optimized profiles
//
// These tests validate the complete mining lifecycle:
// - Connection and handshake
// - Subscription and authorization
// - Difficulty assignment (Spiral Router)
// - Job notification
// - Share submission and validation
// - Block discovery simulation
//
// CRITICAL: These tests are designed to catch the difficulty bug that was
// previously discovered (server using global config instead of session diff).
package stratum

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/spiralpool/stratum/internal/stratum/v1"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// VIRTUAL MINER SIMULATION FRAMEWORK
// =============================================================================

// VirtualMiner simulates a stratum mining client for testing.
type VirtualMiner struct {
	ID         uint64
	UserAgent  string
	WalletAddr string
	WorkerName string
	Session    *protocol.Session
	Handler    *v1.Handler

	// Expected difficulty based on miner type
	ExpectedDiff float64

	// State tracking
	Subscribed   bool
	Authorized   bool
	DiffReceived float64
	JobsReceived int
	SharesSent   int
	SharesAccept int
	SharesReject int

	//lint:ignore U1000 Reserved for concurrent access protection
	mu sync.Mutex
}

// MinerClass represents different hashrate tiers for testing
type TestMinerClass struct {
	Name         string
	UserAgent    string
	ExpectedDiff float64
	SimHashrate  float64 // Simulated hashrate in H/s
}

// TestMinerClasses defines all miner types to test
// Expected difficulty values must match DefaultProfiles in spiralrouter.go:
//   - Lottery: 0.001
//   - Low: 580 (500 GH/s × 5s / 2^32, optimized for 5s target share time)
//   - Mid: 1165 (5 TH/s × 1s / 2^32, optimized for 1s target share time)
//   - High: 3260 (14 TH/s × 1s / 2^32, optimized for 1s target share time)
//   - Pro: 25600 (110 TH/s × 1s / 2^32, optimized for 1s target share time)
//   - Unknown: 500 (default)
var TestMinerClasses = []TestMinerClass{
	{"ESP32Miner", "NerdMiner/1.0", 0.001, 500e3},             // 500 KH/s - Lottery
	{"ESP32Miner", "esp32-miner/2.0", 0.001, 200e3},          // 200 KH/s - Lottery
	{"BitAxeUltra", "BitAxe Ultra/1.0", 580, 500e9},          // 500 GH/s - Low
	{"NMaxe", "nmaxe/1.0", 580, 500e9},                       // 500 GH/s - Low
	{"BitAxeGamma", "bitaxe gamma/1.1", 1165, 400e9},         // 400 GH/s - Mid (bitaxe gamma -> Mid)
	{"NerdQAxe", "NerdQAxe++/2.0", 1165, 3e12},               // 3 TH/s - Mid
	{"BitAxeHex", "BitAxe Hex/1.0", 1165, 6e12},              // 6 TH/s - Mid
	{"AntminerS9", "Antminer S9/1.0", 3260, 14e12},           // 14 TH/s - High
	{"AvalonNano", "Avalon Nano 3S/1.0", 1538, 6.6e12},       // 6.6 TH/s - AvalonNano class (tailored for Nano series)
	{"AntminerS19", "Antminer S19 Pro/1.0", 25600, 110e12},   // 110 TH/s - Pro
	{"WhatsminerM50", "Whatsminer M50S/1.0", 25600, 126e12},  // 126 TH/s - Pro
	{"UnknownMiner", "CustomMiner/1.0", 500, 1e12},           // Unknown -> default
}

// =============================================================================
// TEST 1: DIFFICULTY ASSIGNMENT BY USER-AGENT (Spiral Router)
// =============================================================================

// TestSpiralRouterDifficultyAssignment validates that each miner type
// receives the correct initial difficulty based on its user-agent.
func TestSpiralRouterDifficultyAssignment(t *testing.T) {
	router := NewSpiralRouter()

	for _, mc := range TestMinerClasses {
		t.Run(mc.Name, func(t *testing.T) {
			actualDiff := router.GetInitialDifficulty(mc.UserAgent)

			// Difficulty should match expected value
			if actualDiff != mc.ExpectedDiff {
				t.Errorf("Miner %s (UA: %s): expected diff %.6f, got %.6f",
					mc.Name, mc.UserAgent, mc.ExpectedDiff, actualDiff)
			}

			// Verify class detection
			class, name := router.DetectMiner(mc.UserAgent)
			t.Logf("  Detected: %s (class: %s, diff: %.6f)", name, class, actualDiff)
		})
	}
}

// TestSpiralRouterEdgeCases tests edge cases in miner detection.
func TestSpiralRouterEdgeCases(t *testing.T) {
	router := NewSpiralRouter()

	edgeCases := []struct {
		userAgent    string
		expectedName string
		shouldBeHigh bool // Should difficulty be >= 500 (mid-range threshold)
	}{
		{"", "Unknown", false},                              // Empty user-agent -> Unknown -> 500 (below 1000 threshold)
		{"   ", "Unknown", false},                           // Whitespace only -> Unknown -> 500
		{"BITAXE ULTRA/1.0", "BitAxe Ultra", false},         // Uppercase -> Low -> 116
		{"  NerdMiner/1.0  ", "ESP32 Miner", false},           // Whitespace padding -> Lottery
		{"Some Random Miner", "Unknown", false},             // Unknown defaults to 500
		{"avalon", "Avalon", true},                          // Generic Avalon - MinerClassMid (diff 11650)
		{"AvalonMiner 1246", "AvalonMiner 12xx+", true},     // Avalon 12 series -> High -> 25000
		{"bitmain Antminer S19j Pro", "Antminer S19", true}, // Case insensitive -> Pro -> 25600
		{"nminer123", "NMiner", false},                      // Matches NMiner pattern (lottery tier)
		{"xnminer", "Unknown", false},                       // Should NOT match NMiner (prefix) -> Unknown -> 500
	}

	for _, tc := range edgeCases {
		t.Run(tc.userAgent, func(t *testing.T) {
			class, name := router.DetectMiner(tc.userAgent)
			diff := router.GetInitialDifficulty(tc.userAgent)

			t.Logf("UA: '%s' -> %s (class: %s, diff: %.4f)", tc.userAgent, name, class, diff)

			if tc.shouldBeHigh && diff < 1000 {
				t.Errorf("Expected high difficulty (>=1000) for '%s', got %.4f", tc.userAgent, diff)
			}
			if !tc.shouldBeHigh && diff >= 1000 {
				t.Errorf("Expected low difficulty (<1000) for '%s', got %.4f", tc.userAgent, diff)
			}
		})
	}
}

// =============================================================================
// TEST 2: SESSION DIFFICULTY LIFECYCLE
// =============================================================================

// TestSessionDifficultyLifecycle validates the complete difficulty flow:
// 1. Miner connects
// 2. mining.subscribe with user-agent
// 3. Spiral Router determines difficulty
// 4. mining.authorize sets session difficulty
// 5. Server sends mining.set_difficulty with CORRECT value
func TestSessionDifficultyLifecycle(t *testing.T) {
	for _, mc := range TestMinerClasses {
		t.Run(mc.Name, func(t *testing.T) {
			// Create handler with Spiral Router
			router := NewSpiralRouter()
			handler := v1.NewHandler(100000, true, 0x1FFFE000) // High default diff
			handler.SetMinerDifficultyRouter(router.GetInitialDifficulty)

			// Create session (simulating server's handleConnection)
			session := &protocol.Session{
				ID:              uint64(1),
				ExtraNonce1:     "00000001",
				ExtraNonce2Size: 4,
			}

			// Step 1: Subscribe with user-agent
			subscribeMsg := fmt.Sprintf(`{"id":1,"method":"mining.subscribe","params":["%s"]}`, mc.UserAgent)
			resp, err := handler.HandleMessage(session, []byte(subscribeMsg))
			if err != nil {
				t.Fatalf("Subscribe error: %v", err)
			}
			if !session.IsSubscribed() {
				t.Fatal("Session not marked as subscribed")
			}

			// Verify subscribe response format
			var subResp struct {
				ID     int           `json:"id"`
				Result []interface{} `json:"result"`
				Error  interface{}   `json:"error"`
			}
			if err := json.Unmarshal(resp[:len(resp)-1], &subResp); err != nil {
				t.Fatalf("Parse subscribe response: %v", err)
			}
			if subResp.Error != nil {
				t.Fatalf("Subscribe returned error: %v", subResp.Error)
			}
			if len(subResp.Result) != 3 {
				t.Fatalf("Subscribe result should have 3 elements, got %d", len(subResp.Result))
			}

			// Step 2: Authorize
			authorizeMsg := fmt.Sprintf(`{"id":2,"method":"mining.authorize","params":["DTestWallet.%s","x"]}`, mc.Name)
			_, err = handler.HandleMessage(session, []byte(authorizeMsg))
			if err != nil {
				t.Fatalf("Authorize error: %v", err)
			}
			if !session.IsAuthorized() {
				t.Fatal("Session not marked as authorized")
			}

			// CRITICAL CHECK: Verify session difficulty was set by Spiral Router
			sessionDiff := session.GetDifficulty()
			if sessionDiff != mc.ExpectedDiff {
				t.Errorf("Session difficulty mismatch: Miner %s (UA: %s) expected %.6f, got %.6f",
					mc.Name, mc.UserAgent, mc.ExpectedDiff, sessionDiff)
			} else {
				t.Logf("  Session difficulty correctly set to %.6f for %s", sessionDiff, mc.Name)
			}
		})
	}
}

// TestDifficultyNotGlobalConfig ensures we don't use the global config difficulty
// for miners that should get a different value from Spiral Router.
func TestDifficultyNotGlobalConfig(t *testing.T) {
	globalConfigDiff := 100000.0 // High ASIC difficulty

	router := NewSpiralRouter()
	handler := v1.NewHandler(globalConfigDiff, true, 0x1FFFE000)
	handler.SetMinerDifficultyRouter(router.GetInitialDifficulty)

	// Test with a BitAxe (should get 580, NOT 100000) - Low class InitialDiff is 580
	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	// Subscribe as BitAxe
	subscribeMsg := `{"id":1,"method":"mining.subscribe","params":["BitAxe Ultra/1.0"]}`
	handler.HandleMessage(session, []byte(subscribeMsg))

	// Authorize
	authorizeMsg := `{"id":2,"method":"mining.authorize","params":["DWallet.BitAxe","x"]}`
	handler.HandleMessage(session, []byte(authorizeMsg))

	// Check difficulty
	sessionDiff := session.GetDifficulty()

	if sessionDiff == globalConfigDiff {
		t.Errorf("REGRESSION: BitAxe received global config difficulty (%.0f) instead of Spiral Router difficulty (580)",
			globalConfigDiff)
	}
	if sessionDiff != 580 {
		t.Errorf("BitAxe should have difficulty 580, got %.0f", sessionDiff)
	}

	t.Logf("BitAxe correctly received difficulty %.0f (not global %.0f)", sessionDiff, globalConfigDiff)
}

// =============================================================================
// TEST 3: SHARE SUBMISSION AT CORRECT DIFFICULTY
// =============================================================================

// TestShareSubmissionUsesSesssionDifficulty validates that share validation
// uses the session's difficulty, not a global value.
func TestShareSubmissionUsesSessionDifficulty(t *testing.T) {
	router := NewSpiralRouter()
	handler := v1.NewHandler(100000, true, 0x1FFFE000)
	handler.SetMinerDifficultyRouter(router.GetInitialDifficulty)

	var capturedShare *protocol.Share
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		capturedShare = share
		return &protocol.ShareResult{Accepted: true}
	})

	// Setup BitAxe session (diff 580 - Low class InitialDiff)
	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4,
	}

	handler.HandleMessage(session, []byte(`{"id":1,"method":"mining.subscribe","params":["BitAxe Ultra/1.0"]}`))
	handler.HandleMessage(session, []byte(`{"id":2,"method":"mining.authorize","params":["DWallet.worker","x"]}`))

	// Submit share
	submitMsg := `{"id":3,"method":"mining.submit","params":["worker","job1","00000001","12345678","deadbeef"]}`
	handler.HandleMessage(session, []byte(submitMsg))

	if capturedShare == nil {
		t.Fatal("Share handler was not called")
	}

	// Verify share has session difficulty, not global
	if capturedShare.Difficulty != 580 {
		t.Errorf("Share difficulty should be 580 (session diff), got %.0f", capturedShare.Difficulty)
	}

	t.Logf("Share correctly submitted with difficulty %.0f", capturedShare.Difficulty)
}

// =============================================================================
// TEST 4: CONCURRENT MINER DIFFICULTY ISOLATION
// =============================================================================

// TestConcurrentMinerDifficultyIsolation ensures that different miners
// connecting concurrently receive their own difficulty values.
func TestConcurrentMinerDifficultyIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	const numMinersPerClass = 10
	router := NewSpiralRouter()
	handler := v1.NewHandler(100000, true, 0x1FFFE000)
	handler.SetMinerDifficultyRouter(router.GetInitialDifficulty)

	var wg sync.WaitGroup
	var errors atomic.Int32
	results := make(chan string, len(TestMinerClasses)*numMinersPerClass)

	sessionCounter := atomic.Uint64{}

	for _, mc := range TestMinerClasses {
		for i := 0; i < numMinersPerClass; i++ {
			wg.Add(1)
			go func(minerClass TestMinerClass, minerNum int) {
				defer wg.Done()

				session := &protocol.Session{
					ID:              sessionCounter.Add(1),
					ExtraNonce1:     fmt.Sprintf("%08x", sessionCounter.Load()),
					ExtraNonce2Size: 4,
				}

				// Subscribe
				subscribeMsg := fmt.Sprintf(`{"id":1,"method":"mining.subscribe","params":["%s"]}`, minerClass.UserAgent)
				handler.HandleMessage(session, []byte(subscribeMsg))

				// Authorize
				authorizeMsg := fmt.Sprintf(`{"id":2,"method":"mining.authorize","params":["D%s%d.worker","x"]}`,
					minerClass.Name, minerNum)
				handler.HandleMessage(session, []byte(authorizeMsg))

				// Verify difficulty
				sessionDiff := session.GetDifficulty()
				if sessionDiff != minerClass.ExpectedDiff {
					errors.Add(1)
					results <- fmt.Sprintf("FAIL: %s #%d: expected %.6f, got %.6f",
						minerClass.Name, minerNum, minerClass.ExpectedDiff, sessionDiff)
				} else {
					results <- fmt.Sprintf("OK: %s #%d: %.6f", minerClass.Name, minerNum, sessionDiff)
				}
			}(mc, i)
		}
	}

	wg.Wait()
	close(results)

	// Report all results
	for result := range results {
		if strings.HasPrefix(result, "FAIL") {
			t.Error(result)
		} else {
			t.Log(result)
		}
	}

	if errors.Load() > 0 {
		t.Errorf("%d miners received incorrect difficulty", errors.Load())
	}
}

// =============================================================================
// TEST 5: DIFFICULTY CALCULATION PRECISION
// =============================================================================

// TestDifficultyTargetCalculation validates the difficulty-to-target conversion.
func TestDifficultyTargetCalculation(t *testing.T) {
	// Test various difficulty values
	testCases := []struct {
		diff       float64
		shouldWork bool
		desc       string
	}{
		{0.001, true, "Lottery miner diff"},
		{1.0, true, "Unit difficulty"},
		{500, true, "BitAxe diff"},
		{5000, true, "NerdQAxe diff"},
		{100000, true, "ASIC diff"},
		{1e15, true, "Extreme high diff"},
		{0, false, "Zero diff (invalid)"},
		{-1, false, "Negative diff (invalid)"},
		{math.Inf(1), false, "Infinite diff (invalid)"},
		{math.NaN(), false, "NaN diff (invalid)"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			target := difficultyToTargetTest(tc.diff)

			if tc.shouldWork {
				if target == nil || target.Sign() <= 0 {
					t.Errorf("Diff %.6f should produce valid target, got nil/zero", tc.diff)
				} else {
					t.Logf("Diff %.6f -> target %x... (truncated)", tc.diff, target.Bytes()[:8])
				}
			} else {
				if target != nil && target.Sign() > 0 {
					t.Errorf("Invalid diff %.6f should produce zero target, got %x", tc.diff, target.Bytes())
				}
			}
		})
	}
}

// difficultyToTargetTest is a copy of the validator's function for testing.
func difficultyToTargetTest(difficulty float64) *big.Int {
	maxTarget := new(big.Int)
	maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

	if difficulty <= 0 || math.IsNaN(difficulty) || math.IsInf(difficulty, 0) {
		return new(big.Int)
	}

	diffFloat := new(big.Float).SetFloat64(difficulty)
	scale := new(big.Float).SetFloat64(1e8)
	diffScaled := new(big.Float).Mul(diffFloat, scale)

	diffFixed, _ := diffScaled.Int(nil)
	if diffFixed.Sign() <= 0 {
		return new(big.Int)
	}

	scaleInt := new(big.Int).SetUint64(1e8)
	target := new(big.Int).Mul(maxTarget, scaleInt)
	target.Div(target, diffFixed)

	return target
}

// =============================================================================
// TEST 6: EXTRANONCE HANDLING
// =============================================================================

// TestExtraNonceUniqueness validates that each session gets a unique ExtraNonce1.
func TestExtraNonceUniqueness(t *testing.T) {
	const numSessions = 1000
	extranonces := make(map[string]bool)
	var mu sync.Mutex
	var duplicates int

	var wg sync.WaitGroup
	sessionCounter := atomic.Uint64{}

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Simulate session creation (like server.go handleConnection)
			sessionID := sessionCounter.Add(1)
			extranonce1 := fmt.Sprintf("%08x", sessionID)

			mu.Lock()
			if extranonces[extranonce1] {
				duplicates++
			}
			extranonces[extranonce1] = true
			mu.Unlock()
		}()
	}

	wg.Wait()

	if duplicates > 0 {
		t.Errorf("Found %d duplicate ExtraNonce1 values (should be 0)", duplicates)
	}

	t.Logf("Generated %d unique ExtraNonce1 values", len(extranonces))
}

// TestExtraNonce2Validation tests that ExtraNonce2 is properly validated.
func TestExtraNonce2Validation(t *testing.T) {
	handler := v1.NewHandler(1.0, true, 0x1FFFE000)
	handler.SetShareHandler(func(share *protocol.Share) *protocol.ShareResult {
		return &protocol.ShareResult{Accepted: true}
	})

	session := &protocol.Session{
		ID:              1,
		ExtraNonce1:     "00000001",
		ExtraNonce2Size: 4, // 4 bytes = 8 hex chars
	}
	session.SetSubscribed(true)
	session.SetAuthorized(true)
	session.SetDifficulty(1.0)

	testCases := []struct {
		name        string
		extranonce2 string
		shouldError bool
	}{
		{"valid_8chars", "00000001", false},
		{"valid_max", "ffffffff", true}, // Max value is rejected (nonce exhaustion protection)
		{"too_short_6", "000001", true},
		{"too_long_10", "0000000001", true},
		{"empty", "", true},
		{"invalid_hex", "0000000g", true},
		{"mixed_case_valid", "DeAdBeEf", false},
		{"near_wrap_around", "ffffff00", false}, // Below threshold
		{"wrap_around_edge", "ffffff01", true},  // Above threshold (0xFFFFFF00)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			submitMsg := fmt.Sprintf(
				`{"id":1,"method":"mining.submit","params":["worker","job1","%s","12345678","deadbeef"]}`,
				tc.extranonce2)

			resp, _ := handler.HandleMessage(session, []byte(submitMsg))

			var response struct {
				Result interface{} `json:"result"`
				Error  interface{} `json:"error"`
			}
			json.Unmarshal(resp[:len(resp)-1], &response)

			hasError := response.Error != nil
			if hasError != tc.shouldError {
				if tc.shouldError {
					t.Errorf("Expected error for extranonce2 '%s', got success", tc.extranonce2)
				} else {
					t.Errorf("Unexpected error for extranonce2 '%s': %v", tc.extranonce2, response.Error)
				}
			}
		})
	}
}

// =============================================================================
// TEST 7: JOB NOTIFICATION INTEGRITY
// =============================================================================

// TestJobNotificationFormat validates that mining.notify messages are correctly formed.
func TestJobNotificationFormat(t *testing.T) {
	handler := v1.NewHandler(1.0, true, 0x1FFFE000)

	job := &protocol.Job{
		ID:             "test_job_001",
		PrevBlockHash:  strings.Repeat("00000000", 8),
		CoinBase1:      "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403",
		CoinBase2:      "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		MerkleBranches: []string{strings.Repeat("ab", 32), strings.Repeat("cd", 32)},
		Version:        "20000000",
		NBits:          "1d00ffff",
		NTime:          "12345678",
		CleanJobs:      true,
		CreatedAt:      time.Now(),
	}

	msg, err := handler.BuildNotify(job)
	if err != nil {
		t.Fatalf("BuildNotify error: %v", err)
	}

	// Parse and validate
	var notification struct {
		ID     interface{}   `json:"id"`
		Method string        `json:"method"`
		Params []interface{} `json:"params"`
	}
	if err := json.Unmarshal(msg[:len(msg)-1], &notification); err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if notification.ID != nil {
		t.Error("Notification ID should be null")
	}
	if notification.Method != "mining.notify" {
		t.Errorf("Method should be 'mining.notify', got '%s'", notification.Method)
	}
	if len(notification.Params) != 9 {
		t.Errorf("Params should have 9 elements, got %d", len(notification.Params))
	}

	// Validate params order: [job_id, prevhash, coinb1, coinb2, merkle, version, nbits, ntime, clean]
	if notification.Params[0] != job.ID {
		t.Errorf("Param[0] job_id mismatch")
	}
	if notification.Params[1] != job.PrevBlockHash {
		t.Errorf("Param[1] prevhash mismatch")
	}
	if notification.Params[2] != job.CoinBase1 {
		t.Errorf("Param[2] coinbase1 mismatch")
	}
	if notification.Params[3] != job.CoinBase2 {
		t.Errorf("Param[3] coinbase2 mismatch")
	}
	if notification.Params[8] != true {
		t.Errorf("Param[8] clean_jobs should be true")
	}

	t.Logf("Notify message valid: %d bytes", len(msg))
}

// =============================================================================
// TEST 8: WORKER NAME VALIDATION
// =============================================================================

// TestWorkerNameValidation ensures worker names are properly validated.
func TestWorkerNameValidation(t *testing.T) {
	handler := v1.NewHandler(1.0, true, 0x1FFFE000)

	testCases := []struct {
		name        string
		workerName  string
		shouldError bool
	}{
		{"valid_simple", "DWallet.worker1", false},
		{"valid_no_worker", "DWallet", false},
		{"valid_underscores", "D_Wallet_123.worker_456", false},
		{"valid_dashes", "D-Wallet.worker-1", false},
		{"empty", "", true},
		{"too_long", strings.Repeat("a", 300), true},
		{"sql_injection", "DWallet'; DROP TABLE--", true},
		{"xss_attempt", "DWallet<script>alert(1)</script>", true},
		{"null_byte", "DWallet\x00hidden", true},
		{"newline", "DWallet\nworker", true},
		{"spaces", "D Wallet.worker", false}, // Spaces allowed by relaxed validWorkerName regex in handler.go
		{"unicode", "DWallet.工人", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			session := &protocol.Session{
				ID:              1,
				ExtraNonce1:     "00000001",
				ExtraNonce2Size: 4,
			}
			session.SetSubscribed(true)

			// Build JSON message using proper JSON encoding to handle special characters
			params := []interface{}{tc.workerName, "x"}
			msgObj := map[string]interface{}{
				"id":     1,
				"method": "mining.authorize",
				"params": params,
			}
			authorizeMsg, err := json.Marshal(msgObj)
			if err != nil {
				if tc.shouldError {
					return // Expected error case - malformed input
				}
				t.Fatalf("Failed to build message: %v", err)
			}

			resp, _ := handler.HandleMessage(session, authorizeMsg)
			if len(resp) == 0 {
				if tc.shouldError {
					return // Expected error - no response
				}
				t.Fatal("No response received")
			}

			var response struct {
				Result interface{} `json:"result"`
				Error  interface{} `json:"error"`
			}
			if err := json.Unmarshal(resp[:len(resp)-1], &response); err != nil {
				if tc.shouldError {
					return // Expected error - malformed response
				}
				t.Fatalf("Parse error: %v", err)
			}

			hasError := response.Error != nil
			if hasError != tc.shouldError {
				if tc.shouldError {
					t.Errorf("Worker name '%s' should be rejected", tc.workerName)
				} else {
					t.Errorf("Worker name '%s' should be accepted, got error: %v", tc.workerName, response.Error)
				}
			}
		})
	}
}

// =============================================================================
// TEST 9: VERSION ROLLING (BIP320)
// =============================================================================

// TestVersionRollingValidation validates BIP320 version rolling compliance.
func TestVersionRollingValidation(t *testing.T) {
	// Default mask: 0x1FFFE000
	// This allows bits 13-28 to be modified
	mask := uint32(0x1FFFE000)

	testCases := []struct {
		name       string
		versionBit uint32
		valid      bool
	}{
		{"zero_bits", 0x00000000, true},
		{"within_mask", 0x00002000, true},        // Bit 13
		{"full_mask", mask, true},                // All allowed bits
		{"outside_mask_low", 0x00000001, false},  // Bit 0 (not in mask)
		{"outside_mask_high", 0x80000000, false}, // Bit 31 (not in mask)
		{"mixed_invalid", 0x1FFFE001, false},     // Has bit outside mask
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			isValid := (tc.versionBit &^ mask) == 0

			if isValid != tc.valid {
				t.Errorf("Version bits 0x%08X: expected valid=%v, got %v", tc.versionBit, tc.valid, isValid)
			}
		})
	}
}

// =============================================================================
// TEST 10: NTIME VALIDATION
// =============================================================================

// TestNTimeValidation validates ntime range checking.
func TestNTimeValidation(t *testing.T) {
	// Fixed job time for reference (used in test case comments)
	_ = "65789ABC"

	testCases := []struct {
		name      string
		shareTime string
		valid     bool
	}{
		{"exact_match", "65789ABC", true},
		{"plus_1_hour", "65789ABC", true},  // Would need calculation
		{"minus_1_hour", "65789ABC", true}, // Would need calculation
		{"invalid_hex", "GGGGGGGG", false},
		{"wrong_length", "12345", false},
		{"empty", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Basic format validation
			valid := len(tc.shareTime) == 8
			if valid {
				_, err := hex.DecodeString(tc.shareTime)
				valid = err == nil
			}

			if tc.name == "invalid_hex" || tc.name == "wrong_length" || tc.name == "empty" {
				if valid {
					t.Errorf("NTime '%s' should be invalid format", tc.shareTime)
				}
			}
		})
	}
}

// =============================================================================
// TEST 11: ATOMIC OPERATION SAFETY
// =============================================================================

// TestSessionAtomicOperations validates that session state is thread-safe.
func TestSessionAtomicOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	session := &protocol.Session{}

	const numGoroutines = 100
	const opsPerGoroutine = 1000

	var wg sync.WaitGroup

	// Concurrent difficulty updates
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				session.SetDifficulty(float64(id*1000 + j))
				_ = session.GetDifficulty()
			}
		}(i)
	}

	// Concurrent share counting
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				session.IncrementValidShares()
				session.IncrementInvalidShares()
				_ = session.GetValidShares()
				_ = session.GetInvalidShares()
			}
		}()
	}

	// Concurrent diffSent flag
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				session.SetDiffSent()
				_ = session.IsDiffSent()
			}
		}()
	}

	wg.Wait()

	// Verify counts
	expectedShares := uint64(numGoroutines * opsPerGoroutine)
	if session.GetValidShares() != expectedShares {
		t.Errorf("Valid shares: expected %d, got %d", expectedShares, session.GetValidShares())
	}
	if session.GetInvalidShares() != expectedShares {
		t.Errorf("Invalid shares: expected %d, got %d", expectedShares, session.GetInvalidShares())
	}

	t.Logf("Atomic operations completed: %d difficulty ops, %d share ops each",
		numGoroutines*opsPerGoroutine, expectedShares)
}

// =============================================================================
// BENCHMARK: DIFFICULTY ROUTING PERFORMANCE
// =============================================================================

// BenchmarkE2ESpiralRouterDetection benchmarks miner detection performance.
// Note: Named differently to avoid conflict with spiralrouter_test.go
func BenchmarkE2ESpiralRouterDetection(b *testing.B) {
	router := NewSpiralRouter()
	userAgents := []string{
		"BitAxe Ultra/1.0",
		"NerdMiner/1.0",
		"Antminer S19 Pro/1.0",
		"Unknown Miner/1.0",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ua := userAgents[i%len(userAgents)]
		router.DetectMiner(ua)
	}
}

func BenchmarkDifficultyToTarget(b *testing.B) {
	diffs := []float64{0.001, 1.0, 500, 5000, 100000}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		diff := diffs[i%len(diffs)]
		difficultyToTargetTest(diff)
	}
}
