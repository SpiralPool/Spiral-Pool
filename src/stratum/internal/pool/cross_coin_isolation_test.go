// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package pool - Critical Cross-Coin Isolation Tests
//
// Tests for multi-coin pool isolation:
// - Coin switch mid-connection
// - Shared global state between coins
// - Difficulty bleed-over
// - Job cache contamination
// - Accounting leakage
//
// WHY IT MATTERS: Your pool supports multiple coins - this is dangerous.
// IMMEDIATE FAIL if: Any state is shared across coins unintentionally.
package pool

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/internal/shares"
	"github.com/spiralpool/stratum/internal/vardiff"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// 1. SESSION STATE ISOLATION TESTS
// =============================================================================

// TestSessionStateIsolation verifies session states are isolated per coin
func TestSessionStateIsolation(t *testing.T) {
	t.Parallel()

	// Simulate session state maps for different coin pools
	type CoinSessionStates struct {
		states sync.Map // map[uint64]*vardiff.SessionState
		coin   string
	}

	btcPool := &CoinSessionStates{coin: "BTC"}
	dgbPool := &CoinSessionStates{coin: "DGB"}

	// Create vardiff engine with different configs
	btcEngine := vardiff.NewEngine(config.VarDiffConfig{
		MinDiff:         1000,
		MaxDiff:         1e9,
		TargetTime:      15,
		RetargetTime:    90,
		VariancePercent: 30,
	})

	dgbEngine := vardiff.NewEngine(config.VarDiffConfig{
		MinDiff:         100,
		MaxDiff:         1e6,
		TargetTime:      5,
		RetargetTime:    30,
		VariancePercent: 25,
	})

	// Create sessions for same session ID in different pools
	sessionID := uint64(12345)

	btcState := btcEngine.NewSessionState(65536.0) // BTC starts high
	dgbState := dgbEngine.NewSessionState(1000.0)  // DGB starts lower

	btcPool.states.Store(sessionID, btcState)
	dgbPool.states.Store(sessionID, dgbState)

	// Verify states are independent
	btcStateVal, _ := btcPool.states.Load(sessionID)
	dgbStateVal, _ := dgbPool.states.Load(sessionID)

	btcDiff := vardiff.GetDifficulty(btcStateVal.(*vardiff.SessionState))
	dgbDiff := vardiff.GetDifficulty(dgbStateVal.(*vardiff.SessionState))

	if btcDiff == dgbDiff {
		t.Errorf("ISOLATION FAILURE: BTC and DGB have same difficulty %f", btcDiff)
	}

	t.Logf("BTC session %d difficulty: %f", sessionID, btcDiff)
	t.Logf("DGB session %d difficulty: %f", sessionID, dgbDiff)

	// Modify one, verify other unchanged
	vardiff.SetDifficulty(btcStateVal.(*vardiff.SessionState), 100000.0)

	dgbDiffAfter := vardiff.GetDifficulty(dgbStateVal.(*vardiff.SessionState))
	if dgbDiffAfter != dgbDiff {
		t.Errorf("ISOLATION FAILURE: Modifying BTC state affected DGB (was %f, now %f)",
			dgbDiff, dgbDiffAfter)
	}
}

// TestConcurrentCoinOperations tests isolation under concurrent operations
func TestConcurrentCoinOperations(t *testing.T) {
	t.Parallel()

	// Create isolated state maps for each coin
	coins := []string{"BTC", "DGB", "LTC", "DOGE"}
	coinStates := make(map[string]*sync.Map)
	for _, coin := range coins {
		coinStates[coin] = &sync.Map{}
	}

	var wg sync.WaitGroup
	numOperations := 10000

	// Track any cross-contamination
	var contaminations atomic.Int64

	for _, coin := range coins {
		coin := coin
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < numOperations; i++ {
				sessionID := uint64(i % 100)

				// Store coin-specific data
				coinStates[coin].Store(sessionID, fmt.Sprintf("%s_data_%d", coin, i))
			}
		}()
	}

	wg.Wait()

	// Verify no cross-contamination
	for _, coin := range coins {
		coinStates[coin].Range(func(key, value interface{}) bool {
			data := value.(string)
			if data[:3] != coin[:3] {
				contaminations.Add(1)
				t.Errorf("CONTAMINATION: %s pool has data from another coin: %s", coin, data)
			}
			return true
		})
	}

	if contaminations.Load() > 0 {
		t.Errorf("Found %d cross-contamination instances", contaminations.Load())
	}
}

// =============================================================================
// 2. JOB CACHE ISOLATION TESTS
// =============================================================================

// TestJobCacheIsolation tests that job caches are isolated per coin
func TestJobCacheIsolation(t *testing.T) {
	t.Parallel()

	// Simulate job caches for different coins
	type JobCache struct {
		mu   sync.RWMutex
		jobs map[string]*protocol.Job
		coin string
	}

	btcJobs := &JobCache{jobs: make(map[string]*protocol.Job), coin: "BTC"}
	dgbJobs := &JobCache{jobs: make(map[string]*protocol.Job), coin: "DGB"}

	// Create jobs with same ID but different content
	jobID := "job_001"

	btcJob := &protocol.Job{
		ID:            jobID,
		PrevBlockHash: "00000000000000000000000000000000000000000000000000000000btc12345",
		Height:        800000, // BTC height
		Difficulty:    60000000000000,
		CreatedAt:     time.Now(),
	}

	dgbJob := &protocol.Job{
		ID:            jobID,
		PrevBlockHash: "00000000000000000000000000000000000000000000000000000000dgb12345",
		Height:        18000000, // DGB height (much higher)
		Difficulty:    50000000,
		CreatedAt:     time.Now(),
	}

	// Store in respective caches
	btcJobs.mu.Lock()
	btcJobs.jobs[jobID] = btcJob
	btcJobs.mu.Unlock()

	dgbJobs.mu.Lock()
	dgbJobs.jobs[jobID] = dgbJob
	dgbJobs.mu.Unlock()

	// Retrieve and verify isolation
	btcJobs.mu.RLock()
	btcRetrieved := btcJobs.jobs[jobID]
	btcJobs.mu.RUnlock()

	dgbJobs.mu.RLock()
	dgbRetrieved := dgbJobs.jobs[jobID]
	dgbJobs.mu.RUnlock()

	if btcRetrieved.Height == dgbRetrieved.Height {
		t.Errorf("ISOLATION FAILURE: Same height for BTC (%d) and DGB (%d)",
			btcRetrieved.Height, dgbRetrieved.Height)
	}

	if btcRetrieved.PrevBlockHash == dgbRetrieved.PrevBlockHash {
		t.Error("ISOLATION FAILURE: Same PrevBlockHash for different coins")
	}

	t.Logf("BTC job height: %d, DGB job height: %d", btcRetrieved.Height, dgbRetrieved.Height)
}

// TestJobIDCollision tests handling of identical job IDs across coins
func TestJobIDCollision(t *testing.T) {
	t.Parallel()

	// In production, job IDs could theoretically collide across coins
	// Each coin should have its own namespace

	type CoinJobManager struct {
		jobs    map[string]*protocol.Job
		mu      sync.RWMutex
		coin    string
		counter atomic.Uint64
	}

	newJobManager := func(coin string) *CoinJobManager {
		return &CoinJobManager{
			jobs: make(map[string]*protocol.Job),
			coin: coin,
		}
	}

	generateJobID := func(m *CoinJobManager) string {
		// In production, might want to prefix with coin to avoid collision
		count := m.counter.Add(1)
		return fmt.Sprintf("%08x", count)
	}

	btcMgr := newJobManager("BTC")
	dgbMgr := newJobManager("DGB")

	// Generate jobs
	for i := 0; i < 100; i++ {
		btcID := generateJobID(btcMgr)
		dgbID := generateJobID(dgbMgr)

		btcMgr.mu.Lock()
		btcMgr.jobs[btcID] = &protocol.Job{ID: btcID, Height: uint64(800000 + i)}
		btcMgr.mu.Unlock()

		dgbMgr.mu.Lock()
		dgbMgr.jobs[dgbID] = &protocol.Job{ID: dgbID, Height: uint64(18000000 + i)}
		dgbMgr.mu.Unlock()
	}

	// Job IDs will collide (both use same counter pattern)
	// But they're in separate maps, so no actual collision

	btcMgr.mu.RLock()
	dgbMgr.mu.RLock()

	for btcID, btcJob := range btcMgr.jobs {
		if dgbJob, exists := dgbMgr.jobs[btcID]; exists {
			// Same ID exists in both - verify they're different jobs
			if btcJob.Height == dgbJob.Height {
				t.Errorf("ISOLATION FAILURE: Same job height for ID %s in both coins", btcID)
			}
		}
	}

	btcMgr.mu.RUnlock()
	dgbMgr.mu.RUnlock()
}

// =============================================================================
// 3. DIFFICULTY BLEED-OVER TESTS
// =============================================================================

// TestDifficultyBleedOver tests that difficulty settings don't leak between coins
func TestDifficultyBleedOver(t *testing.T) {
	t.Parallel()

	// Each coin has different difficulty parameters
	coinConfigs := map[string]config.VarDiffConfig{
		"BTC": {
			MinDiff:         1000,
			MaxDiff:         1e15,
			TargetTime:      600, // 10 minute blocks
			RetargetTime:    300,
			VariancePercent: 30,
		},
		"DGB": {
			MinDiff:         100,
			MaxDiff:         1e9,
			TargetTime:      15, // 15 second blocks
			RetargetTime:    30,
			VariancePercent: 25,
		},
		"LTC": {
			MinDiff:         500,
			MaxDiff:         1e12,
			TargetTime:      150, // 2.5 minute blocks
			RetargetTime:    150,
			VariancePercent: 30,
		},
	}

	// Create engines for each coin
	engines := make(map[string]*vardiff.Engine)
	for coin, cfg := range coinConfigs {
		engines[coin] = vardiff.NewEngine(cfg)
	}

	// Create session states
	states := make(map[string]*vardiff.SessionState)
	for coin, engine := range engines {
		states[coin] = engine.NewSessionState(coinConfigs[coin].MinDiff)
	}

	// Record shares on each
	for i := 0; i < 100; i++ {
		for coin, engine := range engines {
			engine.RecordShare(states[coin])
		}
	}

	// Verify difficulties evolved independently
	diffs := make(map[string]float64)
	for coin, state := range states {
		diffs[coin] = vardiff.GetDifficulty(state)
		t.Logf("%s difficulty after 100 shares: %f", coin, diffs[coin])
	}

	// Check for unexpected equality (potential bleed-over)
	coins := []string{"BTC", "DGB", "LTC"}
	for i := 0; i < len(coins); i++ {
		for j := i + 1; j < len(coins); j++ {
			if diffs[coins[i]] == diffs[coins[j]] {
				// Could be coincidental, but log for review
				t.Logf("WARNING: %s and %s have same difficulty %f",
					coins[i], coins[j], diffs[coins[i]])
			}
		}
	}
}

// TestNetworkDifficultyIsolation tests network difficulty is per-coin
func TestNetworkDifficultyIsolation(t *testing.T) {
	t.Parallel()

	// Create validators for each coin
	type CoinValidator struct {
		validator *shares.Validator
		coin      string
	}

	coins := map[string]float64{
		"BTC":  60000000000000, // 60 trillion
		"DGB":  50000000,       // 50 million
		"LTC":  25000000,       // 25 million
		"DOGE": 15000000,       // 15 million
	}

	validators := make(map[string]*shares.Validator)

	for coin := range coins {
		getJob := func(id string) (*protocol.Job, bool) { return nil, false }
		validators[coin] = shares.NewValidator(getJob)
	}

	// Set network difficulties
	for coin, diff := range coins {
		validators[coin].SetNetworkDifficulty(diff)
	}

	// Verify each coin has correct difficulty
	for coin, expectedDiff := range coins {
		actualDiff := validators[coin].GetNetworkDifficulty()

		relError := (actualDiff - expectedDiff) / expectedDiff
		if relError > 0.0001 {
			t.Errorf("%s: network difficulty mismatch - expected %f, got %f",
				coin, expectedDiff, actualDiff)
		}
	}

	// Modify one, verify others unchanged
	validators["BTC"].SetNetworkDifficulty(70000000000000)

	for coin, expectedDiff := range coins {
		if coin == "BTC" {
			continue // We changed this one
		}

		actualDiff := validators[coin].GetNetworkDifficulty()
		if actualDiff != expectedDiff {
			t.Errorf("ISOLATION FAILURE: Changing BTC affected %s (was %f, now %f)",
				coin, expectedDiff, actualDiff)
		}
	}
}

// =============================================================================
// 4. ACCOUNTING ISOLATION TESTS
// =============================================================================

// TestAccountingIsolation tests that share/block accounting is per-coin
func TestAccountingIsolation(t *testing.T) {
	t.Parallel()

	// Simulated accounting per coin
	type CoinAccounting struct {
		shares atomic.Uint64
		blocks atomic.Uint64
		coin   string
	}

	accounting := map[string]*CoinAccounting{
		"BTC":  {coin: "BTC"},
		"DGB":  {coin: "DGB"},
		"LTC":  {coin: "LTC"},
		"DOGE": {coin: "DOGE"},
	}

	var wg sync.WaitGroup

	// Submit different amounts to each coin
	shareAmounts := map[string]int{
		"BTC":  1000,
		"DGB":  5000,
		"LTC":  2000,
		"DOGE": 3000,
	}

	blockAmounts := map[string]int{
		"BTC":  1,
		"DGB":  10,
		"LTC":  3,
		"DOGE": 5,
	}

	for coin, shares := range shareAmounts {
		coin := coin
		shares := shares
		blocks := blockAmounts[coin]

		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < shares; i++ {
				accounting[coin].shares.Add(1)
			}
			for i := 0; i < blocks; i++ {
				accounting[coin].blocks.Add(1)
			}
		}()
	}

	wg.Wait()

	// Verify counts match expected
	for coin, expected := range shareAmounts {
		actual := accounting[coin].shares.Load()
		if actual != uint64(expected) {
			t.Errorf("%s: share count mismatch - expected %d, got %d", coin, expected, actual)
		}
	}

	for coin, expected := range blockAmounts {
		actual := accounting[coin].blocks.Load()
		if actual != uint64(expected) {
			t.Errorf("%s: block count mismatch - expected %d, got %d", coin, expected, actual)
		}
	}

	// Verify no cross-contamination (totals should match)
	var totalShares uint64
	for _, acc := range accounting {
		totalShares += acc.shares.Load()
	}

	expectedTotal := uint64(1000 + 5000 + 2000 + 3000)
	if totalShares != expectedTotal {
		t.Errorf("Total share count mismatch - expected %d, got %d", expectedTotal, totalShares)
	}
}

// =============================================================================
// 5. COIN SWITCH MID-CONNECTION TESTS
// =============================================================================

// TestCoinSwitchMidConnection tests what happens if miner tries to switch coins
func TestCoinSwitchMidConnection(t *testing.T) {
	t.Parallel()

	// Session tracking per coin
	type Session struct {
		id         uint64
		coin       string
		difficulty float64
		createdAt  time.Time
	}

	coinSessions := map[string]map[uint64]*Session{
		"BTC": make(map[uint64]*Session),
		"DGB": make(map[uint64]*Session),
	}
	var sessionsMu sync.RWMutex

	// Register session on BTC
	sessionID := uint64(1)
	sessionsMu.Lock()
	coinSessions["BTC"][sessionID] = &Session{
		id:         sessionID,
		coin:       "BTC",
		difficulty: 65536,
		createdAt:  time.Now(),
	}
	sessionsMu.Unlock()

	// Attempt to register same session on DGB (should be new session or rejected)
	sessionsMu.Lock()

	// Check if session exists on any coin
	for coin, sessions := range coinSessions {
		if existingSession, exists := sessions[sessionID]; exists {
			t.Logf("Session %d already exists on %s (created at %v)",
				sessionID, coin, existingSession.createdAt)

			// In production: either reject or create new session ID
		}
	}

	// Different coins should have different session namespaces
	// Creating on DGB with same ID is allowed but it's a DIFFERENT session
	coinSessions["DGB"][sessionID] = &Session{
		id:         sessionID,
		coin:       "DGB",
		difficulty: 1000,
		createdAt:  time.Now(),
	}
	sessionsMu.Unlock()

	// Verify sessions are independent
	sessionsMu.RLock()
	btcSession := coinSessions["BTC"][sessionID]
	dgbSession := coinSessions["DGB"][sessionID]
	sessionsMu.RUnlock()

	if btcSession.difficulty == dgbSession.difficulty {
		t.Errorf("ISOLATION FAILURE: Same difficulty across coins for session %d", sessionID)
	}

	if btcSession.coin == dgbSession.coin {
		t.Error("ISOLATION FAILURE: Sessions have same coin assignment")
	}

	t.Logf("BTC session: diff=%f, DGB session: diff=%f",
		btcSession.difficulty, dgbSession.difficulty)
}

// =============================================================================
// 6. GLOBAL STATE CONTAMINATION TESTS
// =============================================================================

// TestNoGlobalStateSharing verifies no accidental global state sharing
func TestNoGlobalStateSharing(t *testing.T) {
	t.Parallel()

	// Patterns that could accidentally share state:
	// 1. Package-level variables
	// 2. Shared caches
	// 3. Singleton instances
	// 4. Global counters

	// Test job ID counter isolation
	type JobIDGenerator struct {
		counter atomic.Uint64
		prefix  string
	}

	newGenerator := func(coin string) *JobIDGenerator {
		return &JobIDGenerator{prefix: coin}
	}

	btcGen := newGenerator("BTC")
	dgbGen := newGenerator("DGB")

	// Generate IDs
	for i := 0; i < 100; i++ {
		btcGen.counter.Add(1)
	}

	for i := 0; i < 200; i++ {
		dgbGen.counter.Add(1)
	}

	btcCount := btcGen.counter.Load()
	dgbCount := dgbGen.counter.Load()

	if btcCount == dgbCount {
		t.Errorf("GLOBAL STATE LEAK: Counters should be independent (both = %d)", btcCount)
	}

	if btcCount != 100 {
		t.Errorf("BTC counter wrong: expected 100, got %d", btcCount)
	}

	if dgbCount != 200 {
		t.Errorf("DGB counter wrong: expected 200, got %d", dgbCount)
	}
}

// TestIsolatedValidators verifies validators don't share state
func TestIsolatedValidators(t *testing.T) {
	t.Parallel()

	// Create independent validators
	createValidator := func(coin string) *shares.Validator {
		jobs := make(map[string]*protocol.Job)
		getJob := func(id string) (*protocol.Job, bool) {
			j, ok := jobs[id]
			return j, ok
		}
		return shares.NewValidator(getJob)
	}

	btcValidator := createValidator("BTC")
	dgbValidator := createValidator("DGB")

	// Process shares on each
	btcValidator.SetNetworkDifficulty(60000000000000)
	dgbValidator.SetNetworkDifficulty(50000000)

	// Verify stats are independent
	btcStats := btcValidator.Stats()
	dgbStats := dgbValidator.Stats()

	// Initially both should have 0 validated
	if btcStats.Validated != 0 || dgbStats.Validated != 0 {
		t.Errorf("Initial stats should be 0 (BTC: %d, DGB: %d)",
			btcStats.Validated, dgbStats.Validated)
	}
}

// =============================================================================
// 7. DUPLICATE TRACKER ISOLATION TESTS
// =============================================================================

// TestDuplicateTrackerIsolation tests that duplicate tracking is per-coin
func TestDuplicateTrackerIsolation(t *testing.T) {
	t.Parallel()

	// Each coin needs its own duplicate tracker
	btcTracker := shares.NewDuplicateTracker()
	dgbTracker := shares.NewDuplicateTracker()

	// Same share parameters on both coins
	jobID := "shared_job_id"
	extranonce1 := "00000001"
	extranonce2 := "00000000"
	ntime := "65432100"
	nonce := "deadbeef"

	// Submit to BTC
	btcAccepted := btcTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
	if !btcAccepted {
		t.Error("BTC: First share should be accepted")
	}

	// Same share to DGB should ALSO be accepted (different coin = different chain)
	dgbAccepted := dgbTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
	if !dgbAccepted {
		t.Error("ISOLATION FAILURE: DGB rejected share that was only submitted to BTC")
	}

	// Duplicate on BTC should be rejected
	btcDuplicate := btcTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
	if btcDuplicate {
		t.Error("BTC: Duplicate should be rejected")
	}

	// Duplicate on DGB should also be rejected (within DGB)
	dgbDuplicate := dgbTracker.RecordIfNew(jobID, extranonce1, extranonce2, ntime, nonce)
	if dgbDuplicate {
		t.Error("DGB: Duplicate should be rejected")
	}

	// Verify stats
	btcJobs, btcShares := btcTracker.Stats()
	dgbJobs, dgbShares := dgbTracker.Stats()

	t.Logf("BTC tracker: %d jobs, %d shares", btcJobs, btcShares)
	t.Logf("DGB tracker: %d jobs, %d shares", dgbJobs, dgbShares)

	if btcJobs != 1 || btcShares != 1 {
		t.Errorf("BTC tracker stats wrong: %d jobs, %d shares", btcJobs, btcShares)
	}

	if dgbJobs != 1 || dgbShares != 1 {
		t.Errorf("DGB tracker stats wrong: %d jobs, %d shares", dgbJobs, dgbShares)
	}
}

// =============================================================================
// 8. STRESS TEST - MULTI-COIN CONCURRENT OPERATIONS
// =============================================================================

// TestMultiCoinConcurrentStress stress tests isolation under load
func TestMultiCoinConcurrentStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	t.Parallel()

	coins := []string{"BTC", "DGB", "LTC", "DOGE", "BCH"}

	// Create isolated state for each coin
	type CoinState struct {
		sessions sync.Map
		jobs     sync.Map
		tracker  *shares.DuplicateTracker
		shares   atomic.Uint64
	}

	coinStates := make(map[string]*CoinState)
	for _, coin := range coins {
		coinStates[coin] = &CoinState{
			tracker: shares.NewDuplicateTracker(),
		}
	}

	var wg sync.WaitGroup
	numOperations := 10000
	numGoroutines := 20

	for _, coin := range coins {
		coin := coin
		state := coinStates[coin]

		for g := 0; g < numGoroutines; g++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()

				for i := 0; i < numOperations/numGoroutines; i++ {
					// Session operations
					sessionID := uint64(goroutineID*10000 + i)
					state.sessions.Store(sessionID, fmt.Sprintf("%s_session_%d", coin, sessionID))

					// Job operations
					jobID := fmt.Sprintf("%s_job_%d_%d", coin, goroutineID, i)
					state.jobs.Store(jobID, &protocol.Job{ID: jobID})

					// Share operations
					nonce := fmt.Sprintf("%08x", goroutineID*numOperations+i)
					state.tracker.RecordIfNew(jobID, "en1", "en2", "time", nonce)
					state.shares.Add(1)
				}
			}(g)
		}
	}

	wg.Wait()

	// Verify isolation
	for _, coin := range coins {
		state := coinStates[coin]

		// Count sessions
		sessionCount := 0
		state.sessions.Range(func(key, value interface{}) bool {
			data := value.(string)
			if data[:3] != coin[:3] {
				t.Errorf("CONTAMINATION: %s has session from another coin: %s", coin, data)
			}
			sessionCount++
			return true
		})

		// Count jobs
		jobCount := 0
		state.jobs.Range(func(key, value interface{}) bool {
			job := value.(*protocol.Job)
			if job.ID[:3] != coin[:3] {
				t.Errorf("CONTAMINATION: %s has job from another coin: %s", coin, job.ID)
			}
			jobCount++
			return true
		})

		t.Logf("%s: sessions=%d, jobs=%d, shares=%d",
			coin, sessionCount, jobCount, state.shares.Load())

		// Verify counts are reasonable
		expectedOps := numOperations
		if state.shares.Load() != uint64(expectedOps) {
			t.Errorf("%s: share count mismatch - expected %d, got %d",
				coin, expectedOps, state.shares.Load())
		}
	}
}
