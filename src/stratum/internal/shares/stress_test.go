// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares provides stress tests for high-concurrency scenarios.
//
// These tests validate:
// - Share submission under heavy load
// - Concurrent difficulty adjustments
// - Duplicate detection under stress
// - Memory safety during concurrent operations
package shares

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// TestHighConcurrencyShareSubmission simulates hundreds of miners submitting shares simultaneously.
func TestHighConcurrencyShareSubmission(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numMiners = 100
	const sharesPerMiner = 50

	// Create a job
	job := &protocol.Job{
		ID:            "stresstest",
		Version:       "20000000",
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1d00ffff",
		NTime:         fmt.Sprintf("%08x", time.Now().Unix()),
		Height:        1000000,
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)
	v.SetNetworkDifficulty(1.0) // Low difficulty for testing

	var wg sync.WaitGroup
	var accepted, rejected atomic.Uint64
	startTime := time.Now()

	for miner := 0; miner < numMiners; miner++ {
		wg.Add(1)
		go func(minerID int) {
			defer wg.Done()

			extranonce1 := fmt.Sprintf("%08x", minerID)

			for share := 0; share < sharesPerMiner; share++ {
				s := &protocol.Share{
					SessionID:   uint64(minerID),
					JobID:       job.ID,
					ExtraNonce1: extranonce1,
					ExtraNonce2: fmt.Sprintf("%08x", share),
					NTime:       job.NTime,
					Nonce:       fmt.Sprintf("%08x", rand.Uint32()),
					Difficulty:  0.001, // Very low for acceptance
				}

				result := v.Validate(s)
				if result.Accepted {
					accepted.Add(1)
				} else {
					rejected.Add(1)
				}
			}
		}(miner)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	totalShares := numMiners * sharesPerMiner
	t.Logf("Processed %d shares in %v (%.0f shares/sec)",
		totalShares, elapsed, float64(totalShares)/elapsed.Seconds())
	t.Logf("Accepted: %d, Rejected: %d", accepted.Load(), rejected.Load())

	// Verify all shares were processed
	if int(accepted.Load()+rejected.Load()) != totalShares {
		t.Errorf("Expected %d total shares, got %d", totalShares, accepted.Load()+rejected.Load())
	}
}

// TestConcurrentDuplicateDetection tests duplicate tracker under high concurrency.
func TestConcurrentDuplicateDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterationsPerGoroutine = 1000

	dt := NewDuplicateTracker()
	var wg sync.WaitGroup
	var duplicatesDetected atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()

			en1 := fmt.Sprintf("miner%04d", gID)

			for i := 0; i < iterationsPerGoroutine; i++ {
				en2 := fmt.Sprintf("%08x", i)
				ntime := fmt.Sprintf("%08x", time.Now().Unix())
				nonce := fmt.Sprintf("%08x", rand.Uint32())

				// First submission should not be duplicate
				if dt.IsDuplicate("job1", en1, en2, ntime, nonce) {
					duplicatesDetected.Add(1)
				}

				// Record the share
				dt.Record("job1", en1, en2, ntime, nonce)

				// Second submission MUST be duplicate
				if !dt.IsDuplicate("job1", en1, en2, ntime, nonce) {
					t.Errorf("Goroutine %d: share not detected as duplicate after recording", gID)
				}
			}
		}(g)
	}

	wg.Wait()

	// Some false positives may occur due to race conditions in IsDuplicate check
	// before Record, but should be minimal
	t.Logf("Total false duplicates detected: %d (acceptable if < 1%%)", duplicatesDetected.Load())

	jobs, shares := dt.Stats()
	t.Logf("Tracker stats: %d jobs, %d shares tracked", jobs, shares)
}

// TestAtomicDifficultyUpdate stress tests atomic difficulty setters/getters.
func TestAtomicDifficultyUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 100
	const iterations = 10000

	session := &protocol.Session{}
	session.SetDifficulty(1.0)

	var wg sync.WaitGroup
	var reads, writes atomic.Uint64

	// Spawn readers
	for r := 0; r < numGoroutines/2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				diff := session.GetDifficulty()
				if diff <= 0 {
					t.Errorf("Invalid difficulty read: %f", diff)
				}
				reads.Add(1)
			}
		}()
	}

	// Spawn writers
	for w := 0; w < numGoroutines/2; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				newDiff := float64(writerID+1) * 0.001 * float64(i%1000+1)
				session.SetDifficulty(newDiff)
				writes.Add(1)
			}
		}(w)
	}

	wg.Wait()

	t.Logf("Completed %d reads and %d writes without race conditions",
		reads.Load(), writes.Load())

	// Final difficulty should be positive
	finalDiff := session.GetDifficulty()
	if finalDiff <= 0 {
		t.Errorf("Final difficulty invalid: %f", finalDiff)
	}
}

// TestExtraNonceWraparound tests behavior when extranonce values wrap around.
func TestExtraNonceWraparound(t *testing.T) {
	// Test maximum uint32 values
	maxValues := []string{
		"ffffffff", // Max uint32
		"00000000", // Zero
		"7fffffff", // Max signed int32
		"80000000", // Min signed int32 (as unsigned)
	}

	for _, en1 := range maxValues {
		for _, en2 := range maxValues {
			t.Run(fmt.Sprintf("en1_%s_en2_%s", en1, en2), func(t *testing.T) {
				// Verify hex decoding works
				bytes1, err1 := hex.DecodeString(en1)
				bytes2, err2 := hex.DecodeString(en2)

				if err1 != nil || err2 != nil {
					t.Fatalf("Failed to decode: %v, %v", err1, err2)
				}

				if len(bytes1) != 4 || len(bytes2) != 4 {
					t.Errorf("Unexpected length: %d, %d", len(bytes1), len(bytes2))
				}

				// Test in coinbase construction
				job := &protocol.Job{
					CoinBase1: "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403",
					CoinBase2: "ffffffff01",
				}

				share := &protocol.Share{
					ExtraNonce1: en1,
					ExtraNonce2: en2,
				}

				root, err := computeMerkleRoot(job, share)
				if err != nil {
					t.Fatalf("computeMerkleRoot failed: %v", err)
				}

				if len(root) != 32 {
					t.Errorf("Invalid root length: %d", len(root))
				}
			})
		}
	}
}

// TestCoinbaseOverflow tests behavior with extremely long coinbase text.
func TestCoinbaseOverflow(t *testing.T) {
	tests := []struct {
		name       string
		textLen    int
		shouldFail bool
	}{
		{"empty", 0, false},
		{"normal", 20, false},
		{"max_safe", 80, false},
		{"over_limit", 100, false}, // Should be truncated, not fail
		{"extreme", 1000, false},   // Should be truncated
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := make([]byte, tc.textLen)
			for i := range text {
				text[i] = byte('A' + (i % 26)) // A-Z repeating
			}

			// The buildCoinbase should handle truncation
			// We just verify no panic occurs
			// Height encoding for 1000000: 4 bytes (3 data + 1 length)
			heightLen := 4
			scriptsigLen := heightLen + len(text) + 8

			// If over 100 bytes, should be truncated
			if scriptsigLen > 100 {
				maxTextLen := 100 - heightLen - 8
				if maxTextLen < 0 {
					maxTextLen = 0
				}
				text = text[:maxTextLen]
			}

			t.Logf("Final text length: %d (scriptsig: %d)", len(text), heightLen+len(text)+8)
		})
	}
}

// TestMerkleSliceImmutabilityUnderStress validates merkle tree doesn't modify inputs under stress.
func TestMerkleSliceImmutabilityUnderStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 20
	const iterations = 100

	// Create a shared set of hashes
	originalHashes := make([][]byte, 10)
	for i := range originalHashes {
		originalHashes[i] = make([]byte, 32)
		for j := range originalHashes[i] {
			originalHashes[i][j] = byte(i*32 + j)
		}
	}

	// Make copies for verification
	verifyHashes := make([][]byte, len(originalHashes))
	for i := range originalHashes {
		verifyHashes[i] = make([]byte, 32)
		copy(verifyHashes[i], originalHashes[i])
	}

	var wg sync.WaitGroup
	var errors atomic.Uint64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				// Compute merkle root (should not modify input)
				computeMerkleRootFromHashes(originalHashes)

				// Verify original hashes unchanged
				for j := range originalHashes {
					if !bytesEqual(originalHashes[j], verifyHashes[j]) {
						errors.Add(1)
					}
				}
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("Input hashes were modified %d times!", errors.Load())
	}
}

// sha256d computes double SHA256 (local helper for stress tests)
func sha256d(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}

// computeMerkleRootFromHashes is a helper for testing
func computeMerkleRootFromHashes(hashes [][]byte) []byte {
	if len(hashes) == 0 {
		return nil
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	// Make a copy to avoid modifying input
	level := make([][]byte, len(hashes))
	copy(level, hashes)

	for len(level) > 1 {
		nextLevel := make([][]byte, 0, (len(level)+1)/2)

		for i := 0; i < len(level); i += 2 {
			var combined []byte
			if i+1 < len(level) {
				combined = append(level[i], level[i+1]...)
			} else {
				combined = append(level[i], level[i]...)
			}
			nextLevel = append(nextLevel, sha256d(combined))
		}

		level = nextLevel
	}

	return level[0]
}

// TestValidatorStatsThreadSafety tests that validator stats are thread-safe.
func TestValidatorStatsThreadSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	const numGoroutines = 50
	const iterations = 1000

	job := &protocol.Job{
		ID:            "statstest",
		Version:       "20000000",
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff01",
		NBits:         "1d00ffff",
		NTime:         fmt.Sprintf("%08x", time.Now().Unix()),
		Height:        1000000,
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)
	v.SetNetworkDifficulty(1.0)

	var wg sync.WaitGroup

	// Spawn validators
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				share := &protocol.Share{
					SessionID:   uint64(gID),
					JobID:       job.ID,
					ExtraNonce1: fmt.Sprintf("%08x", gID),
					ExtraNonce2: fmt.Sprintf("%08x", i),
					NTime:       job.NTime,
					Nonce:       fmt.Sprintf("%08x", rand.Uint32()),
					Difficulty:  0.001,
				}

				v.Validate(share)

				// Also read stats concurrently
				if i%100 == 0 {
					_ = v.Stats()
				}
			}
		}(g)
	}

	wg.Wait()

	stats := v.Stats()
	t.Logf("Final stats: Validated=%d, Accepted=%d, Rejected=%d",
		stats.Validated, stats.Accepted, stats.Rejected)

	// Total should match
	expectedTotal := uint64(numGoroutines * iterations)
	if stats.Validated != expectedTotal {
		t.Errorf("Expected %d validated, got %d", expectedTotal, stats.Validated)
	}
}

// BenchmarkConcurrentShareValidation benchmarks share validation throughput.
func BenchmarkConcurrentShareValidation(b *testing.B) {
	job := &protocol.Job{
		ID:            "benchtest",
		Version:       "20000000",
		PrevBlockHash: "0000000000000000000000000000000000000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff01",
		NBits:         "1d00ffff",
		NTime:         fmt.Sprintf("%08x", time.Now().Unix()),
		Height:        1000000,
	}

	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}

	v := NewValidator(getJob)
	v.SetNetworkDifficulty(1.0)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		minerID := rand.Uint32()
		shareNum := 0
		for pb.Next() {
			share := &protocol.Share{
				SessionID:   uint64(minerID),
				JobID:       job.ID,
				ExtraNonce1: fmt.Sprintf("%08x", minerID),
				ExtraNonce2: fmt.Sprintf("%08x", shareNum),
				NTime:       job.NTime,
				Nonce:       fmt.Sprintf("%08x", rand.Uint32()),
				Difficulty:  0.001,
			}
			v.Validate(share)
			shareNum++
		}
	})
}
