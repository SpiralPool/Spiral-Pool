// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares provides comprehensive tests for share validation.
//
// These tests cover:
// - Block header construction
// - Merkle root computation
// - VarInt encoding
// - Difficulty calculations
// - Duplicate detection
package shares

import (
	"encoding/hex"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/crypto"
	"github.com/spiralpool/stratum/pkg/protocol"
)

// TestEncodeVarInt validates Bitcoin/DigiByte CompactSize encoding.
func TestEncodeVarInt(t *testing.T) {
	tests := []struct {
		name     string
		value    uint64
		expected []byte
	}{
		// Single byte (0-252)
		{"0", 0, []byte{0x00}},
		{"1", 1, []byte{0x01}},
		{"100", 100, []byte{0x64}},
		{"252", 252, []byte{0xfc}},

		// Three bytes (253-65535)
		{"253", 253, []byte{0xfd, 0xfd, 0x00}},
		{"254", 254, []byte{0xfd, 0xfe, 0x00}},
		{"255", 255, []byte{0xfd, 0xff, 0x00}},
		{"256", 256, []byte{0xfd, 0x00, 0x01}},
		{"1000", 1000, []byte{0xfd, 0xe8, 0x03}},
		{"65535", 65535, []byte{0xfd, 0xff, 0xff}},

		// Five bytes (65536-4294967295)
		{"65536", 65536, []byte{0xfe, 0x00, 0x00, 0x01, 0x00}},
		{"1000000", 1000000, []byte{0xfe, 0x40, 0x42, 0x0f, 0x00}},

		// Transaction counts (realistic values)
		{"100 txs", 100, []byte{0x64}},
		{"500 txs", 500, []byte{0xfd, 0xf4, 0x01}},
		{"2000 txs", 2000, []byte{0xfd, 0xd0, 0x07}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := crypto.EncodeVarInt(tt.value)
			if !bytesEqual(result, tt.expected) {
				t.Errorf("crypto.EncodeVarInt(%d) = %x, want %x", tt.value, result, tt.expected)
			}
		})
	}
}

// TestReverseBytes validates byte reversal for endianness conversion.
func TestReverseBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{"empty", []byte{}, []byte{}},
		{"single", []byte{0x01}, []byte{0x01}},
		{"two bytes", []byte{0x01, 0x02}, []byte{0x02, 0x01}},
		{"four bytes", []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x04, 0x03, 0x02, 0x01}},
		{"32 bytes (hash)", make32Bytes(1), reverse32Bytes(make32Bytes(1))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reverseBytes(tt.input)
			if !bytesEqual(result, tt.expected) {
				t.Errorf("reverseBytes(%x) = %x, want %x", tt.input, result, tt.expected)
			}
		})
	}
}

// TestDifficultyToTarget validates difficulty to target conversion.
func TestDifficultyToTarget(t *testing.T) {
	tests := []struct {
		name       string
		difficulty float64
	}{
		{"difficulty 1", 1.0},
		{"difficulty 10", 10.0},
		{"difficulty 100", 100.0},
		{"difficulty 1000", 1000.0},
		{"difficulty 0.001", 0.001},
		{"difficulty 0.1", 0.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := difficultyToTarget(tt.difficulty)
			if target == nil {
				t.Error("difficultyToTarget returned nil")
				return
			}
			// Target should be positive
			if target.Sign() <= 0 {
				t.Error("Target should be positive")
			}
		})
	}

	// Test edge cases - invalid difficulties return zero target as security measure
	t.Run("zero difficulty", func(t *testing.T) {
		target := difficultyToTarget(0)
		// Should return zero target (security: rejects all shares)
		if target == nil {
			t.Error("difficultyToTarget(0) returned nil")
		}
		if target.Sign() != 0 {
			t.Errorf("difficultyToTarget(0) should return zero target, got %v", target)
		}
	})

	t.Run("negative difficulty", func(t *testing.T) {
		target := difficultyToTarget(-1)
		// Should return zero target (security: rejects all shares)
		if target == nil {
			t.Error("difficultyToTarget(-1) returned nil")
		}
		if target.Sign() != 0 {
			t.Errorf("difficultyToTarget(-1) should return zero target, got %v", target)
		}
	})
}

// TestValidateNTime validates time window checking.
func TestValidateNTime(t *testing.T) {
	// Job time: 0x65000000 (a fixed timestamp)
	jobTime := "65000000"

	tests := []struct {
		name      string
		shareTime string
		valid     bool
	}{
		{"same time", "65000000", true},
		{"slightly ahead", "65000100", true},
		{"slightly behind", "64ffff00", true},
		{"too far ahead", "66000000", false},  // > 2 hours ahead
		{"too far behind", "63000000", false}, // > 2 hours behind
		{"invalid hex", "invalid!", false},
		{"wrong length", "6500", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateNTime(tt.shareTime, jobTime)
			if result != tt.valid {
				t.Errorf("validateNTime(%s, %s) = %v, want %v",
					tt.shareTime, jobTime, result, tt.valid)
			}
		})
	}
}

// TestDuplicateTracker validates duplicate share detection.
func TestDuplicateTracker(t *testing.T) {
	dt := NewDuplicateTracker()

	// First submission should not be duplicate
	if dt.IsDuplicate("job1", "en1", "en2", "time", "nonce") {
		t.Error("First submission incorrectly marked as duplicate")
	}

	// Record the share
	dt.Record("job1", "en1", "en2", "time", "nonce")

	// Same share should now be duplicate
	if !dt.IsDuplicate("job1", "en1", "en2", "time", "nonce") {
		t.Error("Duplicate share not detected")
	}

	// Different extranonce1 should NOT be duplicate (different miner)
	if dt.IsDuplicate("job1", "different_en1", "en2", "time", "nonce") {
		t.Error("Different extranonce1 incorrectly marked as duplicate")
	}

	// Different extranonce2 should not be duplicate
	if dt.IsDuplicate("job1", "en1", "different_en2", "time", "nonce") {
		t.Error("Different extranonce2 incorrectly marked as duplicate")
	}

	// Different job should not be duplicate
	if dt.IsDuplicate("job2", "en1", "en2", "time", "nonce") {
		t.Error("Different job incorrectly marked as duplicate")
	}
}

// TestDuplicateTrackerConcurrency validates thread-safety.
func TestDuplicateTrackerConcurrency(t *testing.T) {
	dt := NewDuplicateTracker()
	var wg sync.WaitGroup
	numGoroutines := 100
	sharesPerGoroutine := 100

	// Concurrent recording
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := 0; j < sharesPerGoroutine; j++ {
				jobID := "job1"
				en1 := string(rune('a' + routineID%26))
				en2 := string(rune('0' + j%10))
				dt.Record(jobID, en1, en2, "time", "nonce")
			}
		}(i)
	}

	wg.Wait()

	// Check stats
	jobs, shares := dt.Stats()
	if jobs != 1 {
		t.Errorf("Expected 1 job, got %d", jobs)
	}
	if shares == 0 {
		t.Error("Expected shares to be recorded")
	}
}

// TestDuplicateTrackerCleanup validates old entry cleanup.
func TestDuplicateTrackerCleanup(t *testing.T) {
	dt := NewDuplicateTracker()

	// Record some shares
	dt.Record("job1", "en1", "en2", "time", "nonce")

	jobs, _ := dt.Stats()
	if jobs != 1 {
		t.Errorf("Expected 1 job, got %d", jobs)
	}

	// Cleanup specific job
	dt.CleanupJob("job1")

	jobs, _ = dt.Stats()
	if jobs != 0 {
		t.Errorf("Expected 0 jobs after cleanup, got %d", jobs)
	}
}

// TestBuildBlockHeader validates header construction.
func TestBuildBlockHeader(t *testing.T) {
	job := &protocol.Job{
		Version:               "20000000",
		PrevBlockHash:         "00000000000000000000000000000000" + "00000000000000000000000000000001", // 64 hex chars
		CoinBase1:             "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff",
		CoinBase2:             "ffffffff01",
		NBits:                 "1a0377ae",
		NTime:                 "64000000",
		MerkleBranches:        []string{},
		VersionRollingAllowed: false,
	}

	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	header, err := buildBlockHeader(job, share)
	if err != nil {
		t.Fatalf("buildBlockHeader failed: %v", err)
	}

	// Header should be exactly 80 bytes
	if len(header) != 80 {
		t.Errorf("Header length = %d, want 80", len(header))
	}
}

// TestBuildBlockHeaderInvalidInputs validates error handling.
func TestBuildBlockHeaderInvalidInputs(t *testing.T) {
	tests := []struct {
		name      string
		job       *protocol.Job
		share     *protocol.Share
		expectErr bool
	}{
		{
			name: "invalid version",
			job: &protocol.Job{
				Version:       "invalid",
				PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
			},
			share:     &protocol.Share{NTime: "64000000", Nonce: "12345678"},
			expectErr: true,
		},
		{
			name: "invalid prevhash length",
			job: &protocol.Job{
				Version:       "20000000",
				PrevBlockHash: "0000", // Too short
			},
			share:     &protocol.Share{NTime: "64000000", Nonce: "12345678"},
			expectErr: true,
		},
		{
			name: "invalid ntime",
			job: &protocol.Job{
				Version:       "20000000",
				PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
			},
			share:     &protocol.Share{NTime: "invalid!", Nonce: "12345678"},
			expectErr: true,
		},
		{
			name: "invalid nonce",
			job: &protocol.Job{
				Version:       "20000000",
				PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
				NBits:         "1a0377ae",
			},
			share:     &protocol.Share{NTime: "64000000", Nonce: "invalid!"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildBlockHeader(tt.job, tt.share)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// TestPrevHashAllZerosRejected validates all-zeros prevhash rejection.
func TestPrevHashAllZerosRejected(t *testing.T) {
	job := &protocol.Job{
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
	}

	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	_, err := buildBlockHeader(job, share)
	if err == nil {
		t.Error("Expected error for all-zeros prevhash, got nil")
	}
}

// TestBuildFullBlock validates complete block construction.
func TestBuildFullBlock(t *testing.T) {
	job := &protocol.Job{
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		TransactionData: []string{},
	}

	header := make([]byte, 80)
	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	// Decode and validate
	block, err := hex.DecodeString(blockHex)
	if err != nil {
		t.Fatalf("Invalid block hex: %v", err)
	}

	// Should have: 80 byte header + varint(1) + coinbase tx
	if len(block) <= 80 {
		t.Error("Block too short")
	}

	// First 80 bytes should be header
	if !bytesEqual(block[:80], header) {
		t.Error("Header mismatch in block")
	}

	// Transaction count should be 1 (VarInt encoded)
	if block[80] != 0x01 {
		t.Errorf("Transaction count = 0x%02x, want 0x01", block[80])
	}
}

// TestBuildFullBlockWithTransactions validates block with multiple txs.
func TestBuildFullBlockWithTransactions(t *testing.T) {
	// A minimal valid transaction hex
	txHex := "0100000001" + // version + input count
		"0000000000000000000000000000000000000000000000000000000000000000" + // prevout hash
		"00000000" + // prevout index
		"00" + // script length
		"ffffffff" + // sequence
		"01" + // output count
		"0000000000000000" + // value
		"00" + // script length
		"00000000" // locktime

	job := &protocol.Job{
		CoinBase1:       "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:       "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		TransactionData: []string{txHex, txHex}, // 2 transactions
	}

	header := make([]byte, 80)
	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	blockHex, err := buildFullBlock(job, share, header)
	if err != nil {
		t.Fatalf("buildFullBlock failed: %v", err)
	}

	block, _ := hex.DecodeString(blockHex)

	// Transaction count should be 3 (coinbase + 2 txs)
	if block[80] != 0x03 {
		t.Errorf("Transaction count = 0x%02x, want 0x03", block[80])
	}
}

// Helper functions

func make32Bytes(seed byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}

func reverse32Bytes(b []byte) []byte {
	result := make([]byte, 32)
	for i := 0; i < 32; i++ {
		result[i] = b[31-i]
	}
	return result
}

// Benchmark tests

func BenchmarkDuplicateCheck(b *testing.B) {
	dt := NewDuplicateTracker()
	// Pre-populate with some data
	for i := 0; i < 1000; i++ {
		dt.Record("job1", "en1", string(rune(i)), "time", "nonce")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dt.IsDuplicate("job1", "en1", "nonexistent", "time", "nonce")
	}
}

func BenchmarkDuplicateRecord(b *testing.B) {
	dt := NewDuplicateTracker()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dt.Record("job1", "en1", string(rune(i%1000)), "time", "nonce")
	}
}

func BenchmarkBuildBlockHeader(b *testing.B) {
	job := &protocol.Job{
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
	}

	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
		NTime:       "64000000",
		Nonce:       "12345678",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildBlockHeader(job, share)
	}
}

// TestMerkleRootComputation validates merkle root calculation.
func TestMerkleRootComputation(t *testing.T) {
	job := &protocol.Job{
		CoinBase1:      "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:      "ffffffff01",
		MerkleBranches: []string{},
	}

	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	root, err := computeMerkleRoot(job, share)
	if err != nil {
		t.Fatalf("computeMerkleRoot failed: %v", err)
	}

	// Root should be 32 bytes
	if len(root) != 32 {
		t.Errorf("Merkle root length = %d, want 32", len(root))
	}
}

// TestMerkleRootWithBranches validates merkle computation with branches.
func TestMerkleRootWithBranches(t *testing.T) {
	// Create a fake 32-byte hash for merkle branch
	branchHash := make([]byte, 32)
	for i := range branchHash {
		branchHash[i] = byte(i)
	}

	job := &protocol.Job{
		CoinBase1:      "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:      "ffffffff01",
		MerkleBranches: []string{hex.EncodeToString(branchHash)},
	}

	share := &protocol.Share{
		ExtraNonce1: "00000001",
		ExtraNonce2: "00000002",
	}

	root, err := computeMerkleRoot(job, share)
	if err != nil {
		t.Fatalf("computeMerkleRoot failed: %v", err)
	}

	if len(root) != 32 {
		t.Errorf("Merkle root length = %d, want 32", len(root))
	}
}

// TestValidatorStats validates statistics tracking.
func TestValidatorStats(t *testing.T) {
	getJob := func(id string) (*protocol.Job, bool) {
		return nil, false // Always return not found
	}

	v := NewValidator(getJob)

	// Submit a share that will be rejected (job not found)
	share := &protocol.Share{
		JobID: "nonexistent",
	}
	result := v.Validate(share)

	if result.Accepted {
		t.Error("Share should be rejected")
	}

	stats := v.Stats()
	if stats.Validated != 1 {
		t.Errorf("Validated = %d, want 1", stats.Validated)
	}
	if stats.Rejected != 1 {
		t.Errorf("Rejected = %d, want 1", stats.Rejected)
	}
}

// Add time-based test for cleanup (basic version)
func TestDuplicateTrackerTimeBasedCleanupBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-based test in short mode")
	}

	dt := NewDuplicateTracker()

	// Record a share
	dt.Record("old_job", "en1", "en2", "time", "nonce")

	// Verify it's tracked
	jobs, _ := dt.Stats()
	if jobs != 1 {
		t.Fatalf("Expected 1 job, got %d", jobs)
	}

	// Manually trigger cleanup by setting lastCleanup far in the past
	dt.lastCleanup = time.Now().Unix() - 120 // 2 minutes ago

	// Record another share to trigger cleanup check
	dt.Record("new_job", "en1", "en2", "time", "nonce")

	// Note: The old job won't be cleaned up yet because it's not old enough
	// This test mainly validates the cleanup logic runs without crashing
	jobs, _ = dt.Stats()
	if jobs < 1 {
		t.Error("Jobs should still be present")
	}
}

// TestVersionRollingMask validates BIP320 version rolling behavior.
func TestVersionRollingMask(t *testing.T) {
	tests := []struct {
		name        string
		baseVersion string // hex, big-endian
		versionBits uint32
		mask        uint32
		expectedLE  string // hex, little-endian result in header
	}{
		{
			name:        "no rolling",
			baseVersion: "20000000",
			versionBits: 0,
			mask:        0x1FFFE000,
			expectedLE:  "00000020", // reversed
		},
		{
			name:        "basic rolling",
			baseVersion: "20000000",
			versionBits: 0x00002000, // bit 13 set
			mask:        0x1FFFE000,
			expectedLE:  "00200020", // 0x20002000 little-endian
		},
		{
			name:        "max mask bits",
			baseVersion: "20000000",
			versionBits: 0x1FFFE000, // all mask bits set
			mask:        0x1FFFE000,
			expectedLE:  "00e0ff3f", // 0x3fffe000 little-endian
		},
		{
			name:        "version bits outside mask ignored",
			baseVersion: "20000000",
			versionBits: 0xFFFFFFFF, // all bits set
			mask:        0x1FFFE000,
			expectedLE:  "00e0ff3f", // only mask bits applied
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			version, _ := hex.DecodeString(tc.baseVersion)
			expected, _ := hex.DecodeString(tc.expectedLE)

			// Apply version rolling like buildBlockHeader does
			var result [4]byte
			if tc.versionBits != 0 && tc.mask != 0 {
				v := uint32(version[0])<<24 | uint32(version[1])<<16 | uint32(version[2])<<8 | uint32(version[3])
				v = (v &^ tc.mask) | (tc.versionBits & tc.mask)
				// Store as little-endian
				result[0] = byte(v)
				result[1] = byte(v >> 8)
				result[2] = byte(v >> 16)
				result[3] = byte(v >> 24)
			} else {
				// No rolling, just reverse
				result[0] = version[3]
				result[1] = version[2]
				result[2] = version[1]
				result[3] = version[0]
			}

			if !bytesEqual(result[:], expected) {
				t.Errorf("version rolling result:\n  got:  %x\n  want: %x", result[:], expected)
			}
		})
	}
}

// TestMerkleRootWithRandomTxHashes validates merkle computation with random hashes.
func TestMerkleRootWithRandomTxHashes(t *testing.T) {
	// Test various transaction counts
	txCounts := []int{0, 1, 2, 3, 4, 5, 7, 8, 15, 16, 31, 32, 100}

	for _, count := range txCounts {
		t.Run(string(rune(count))+"_txs", func(t *testing.T) {
			// Generate random merkle branches
			branches := make([]string, 0)
			if count > 0 {
				// Approximate number of branches needed
				numBranches := 0
				for n := count + 1; n > 1; n = (n + 1) / 2 {
					numBranches++
				}
				for i := 0; i < numBranches; i++ {
					branch := make([]byte, 32)
					for j := range branch {
						branch[j] = byte((i*32 + j) % 256)
					}
					branches = append(branches, hex.EncodeToString(branch))
				}
			}

			job := &protocol.Job{
				CoinBase1:      "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
				CoinBase2:      "ffffffff01",
				MerkleBranches: branches,
			}

			share := &protocol.Share{
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000002",
			}

			root, err := computeMerkleRoot(job, share)
			if err != nil {
				t.Fatalf("computeMerkleRoot failed: %v", err)
			}

			// Root must be 32 bytes
			if len(root) != 32 {
				t.Errorf("Merkle root length = %d, want 32", len(root))
			}

			// Root must be deterministic
			root2, _ := computeMerkleRoot(job, share)
			if !bytesEqual(root, root2) {
				t.Error("Merkle root is not deterministic")
			}
		})
	}
}

// TestBlockHeaderConstruction80Bytes validates header is exactly 80 bytes.
func TestBlockHeaderConstruction80Bytes(t *testing.T) {
	// Various valid job configurations
	jobs := []*protocol.Job{
		{
			Version:       "20000000",
			PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
			CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
			CoinBase2:     "ffffffff01",
			NBits:         "1a0377ae",
			NTime:         "64000000",
		},
		{
			Version:       "3fff0000",
			PrevBlockHash: "ffffffffffffffffffffffffffffffff" + "fffffffffffffffffffffffffffffffe",
			CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
			CoinBase2:     "ffffffff01",
			NBits:         "1d00ffff",
			NTime:         "ffffffff",
		},
	}

	for i, job := range jobs {
		t.Run("job_"+string(rune(i)), func(t *testing.T) {
			share := &protocol.Share{
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000002",
				NTime:       job.NTime,
				Nonce:       "12345678",
			}

			header, err := buildBlockHeader(job, share)
			if err != nil {
				t.Fatalf("buildBlockHeader failed: %v", err)
			}

			if len(header) != 80 {
				t.Errorf("Header length = %d, want 80", len(header))
			}
		})
	}
}

// TestCoinbaseConstruction validates coinbase transaction building.
func TestCoinbaseConstruction(t *testing.T) {
	job := &protocol.Job{
		CoinBase1: "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403",
		CoinBase2: "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
	}

	share := &protocol.Share{
		ExtraNonce1: "deadbeef",
		ExtraNonce2: "cafebabe",
	}

	// Build coinbase
	cb1, _ := hex.DecodeString(job.CoinBase1)
	en1, _ := hex.DecodeString(share.ExtraNonce1)
	en2, _ := hex.DecodeString(share.ExtraNonce2)
	cb2, _ := hex.DecodeString(job.CoinBase2)

	coinbase := make([]byte, 0, len(cb1)+len(en1)+len(en2)+len(cb2))
	coinbase = append(coinbase, cb1...)
	coinbase = append(coinbase, en1...)
	coinbase = append(coinbase, en2...)
	coinbase = append(coinbase, cb2...)

	// Verify extranonce appears in correct position
	expectedPos := len(cb1)
	if !bytesEqual(coinbase[expectedPos:expectedPos+4], en1) {
		t.Errorf("ExtraNonce1 not at expected position %d", expectedPos)
	}
	if !bytesEqual(coinbase[expectedPos+4:expectedPos+8], en2) {
		t.Errorf("ExtraNonce2 not at expected position %d", expectedPos+4)
	}

	// Verify coinbase2 follows extranonces
	if !bytesEqual(coinbase[expectedPos+8:expectedPos+8+len(cb2)], cb2) {
		t.Error("CoinBase2 not at expected position")
	}
}

// TestShareValidationFlow validates the complete share validation flow.
func TestShareValidationFlow(t *testing.T) {
	// Create a mock job
	job := &protocol.Job{
		ID:            "testjob",
		Version:       "20000000",
		PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
		CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		NBits:         "1a0377ae",
		NTime:         "64000000",
		Height:        1000000,
		CreatedAt:     time.Now(), // Prevent job from being considered stale
	}

	// Create validator with job lookup
	getJob := func(id string) (*protocol.Job, bool) {
		if id == job.ID {
			return job, true
		}
		return nil, false
	}
	v := NewValidator(getJob)
	v.SetNetworkDifficulty(1000000.0) // High difficulty

	// Test cases
	tests := []struct {
		name         string
		share        *protocol.Share
		expectAccept bool
		expectReason string
	}{
		{
			name: "job not found",
			share: &protocol.Share{
				JobID:       "nonexistent",
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000002",
				NTime:       "64000000",
				Nonce:       "12345678",
				Difficulty:  0.001, // Very low for acceptance
			},
			expectAccept: false,
			expectReason: protocol.RejectReasonInvalidJob,
		},
		{
			name: "invalid ntime (too far)",
			share: &protocol.Share{
				JobID:       "testjob",
				ExtraNonce1: "00000001",
				ExtraNonce2: "00000002",
				NTime:       "74000000", // Way too far ahead
				Nonce:       "12345678",
				Difficulty:  0.001,
			},
			expectAccept: false,
			expectReason: protocol.RejectReasonInvalidTime,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := v.Validate(tc.share)

			if result.Accepted != tc.expectAccept {
				t.Errorf("Accepted = %v, want %v", result.Accepted, tc.expectAccept)
			}
			if !tc.expectAccept && result.RejectReason != tc.expectReason {
				t.Errorf("RejectReason = %q, want %q", result.RejectReason, tc.expectReason)
			}
		})
	}
}

// TestDuplicateKeyWithExtraNonce1 validates that ExtraNonce1 is part of duplicate key.
func TestDuplicateKeyWithExtraNonce1(t *testing.T) {
	dt := NewDuplicateTracker()

	// Miner A submits a share
	dt.Record("job1", "minerA_en1", "en2", "time", "nonce")

	// Same share params from miner A should be duplicate
	if !dt.IsDuplicate("job1", "minerA_en1", "en2", "time", "nonce") {
		t.Error("Same miner duplicate not detected")
	}

	// Same en2/time/nonce from miner B (different en1) should NOT be duplicate
	if dt.IsDuplicate("job1", "minerB_en1", "en2", "time", "nonce") {
		t.Error("Different miner incorrectly marked as duplicate")
	}

	// Record miner B's share
	dt.Record("job1", "minerB_en1", "en2", "time", "nonce")

	// Now miner B's share should be duplicate
	if !dt.IsDuplicate("job1", "minerB_en1", "en2", "time", "nonce") {
		t.Error("Miner B duplicate not detected after recording")
	}
}

// TestTargetCalculationPrecision validates difficulty to target conversion precision.
func TestTargetCalculationPrecision(t *testing.T) {
	// Test basic properties of the difficulty to target conversion
	// Note: Very high difficulties may hit precision limits due to fixed-point conversion
	tests := []struct {
		difficulty float64
		desc       string
	}{
		{1.0, "diff 1"},
		{0.001, "very low diff"},
		{1000.0, "medium diff"},
		{1000000.0, "high diff"},
		{0.0001, "micro diff"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			target := difficultyToTarget(tc.difficulty)
			if target == nil {
				t.Fatal("Target is nil")
			}

			// Target should be positive
			if target.Sign() <= 0 {
				t.Error("Target should be positive")
			}

			// Higher difficulty = lower target (harder to meet)
			// Verify this relationship - use a larger ratio to avoid precision issues
			lowerDiff := tc.difficulty / 10
			lowerTarget := difficultyToTarget(lowerDiff)
			if lowerTarget.Cmp(target) <= 0 {
				t.Errorf("Lower difficulty (%.2e) should give higher target than %.2e", lowerDiff, tc.difficulty)
			}
		})
	}

	// Special test: verify extremes - invalid difficulties return zero target (security measure)
	// Zero target means all shares are rejected, preventing invalid difficulty exploitation
	t.Run("zero returns zero target (security)", func(t *testing.T) {
		target := difficultyToTarget(0)
		if target == nil {
			t.Error("Zero difficulty should return non-nil target")
		}
		// Zero target is intentional - it rejects all shares for invalid difficulty
		if target.Sign() != 0 {
			t.Errorf("Zero difficulty should return zero target (security), got %v", target)
		}
	})

	t.Run("negative returns zero target (security)", func(t *testing.T) {
		target := difficultyToTarget(-1)
		if target == nil {
			t.Error("Negative difficulty should return non-nil target")
		}
		// Zero target is intentional - it rejects all shares for invalid difficulty
		if target.Sign() != 0 {
			t.Errorf("Negative difficulty should return zero target (security), got %v", target)
		}
	})

	// Test that difficulty 1 gives approximately maxTarget
	t.Run("diff 1 approximates maxTarget", func(t *testing.T) {
		target := difficultyToTarget(1.0)
		// Construct maxTarget directly since difficultyToTarget(0) returns zero for security
		maxTarget := new(big.Int)
		maxTarget.SetString("00000000FFFF0000000000000000000000000000000000000000000000000000", 16)

		// Difficulty 1 target should be equal to maxTarget
		if target.Cmp(maxTarget) != 0 {
			// Allow for small precision differences
			ratio := new(big.Float).Quo(
				new(big.Float).SetInt(maxTarget),
				new(big.Float).SetInt(target),
			)
			ratioF, _ := ratio.Float64()
			if ratioF > 1.01 || ratioF < 0.99 {
				t.Errorf("Difficulty 1 target should be close to maxTarget, ratio: %.4f", ratioF)
			}
		}
	})
}

// FuzzDuplicateTracker fuzz tests the duplicate tracker.
func FuzzDuplicateTracker(f *testing.F) {
	f.Add("job1", "en1", "en2", "time", "nonce")
	f.Add("", "", "", "", "")
	f.Add("a", "b", "c", "d", "e")

	f.Fuzz(func(t *testing.T, jobID, en1, en2, ntime, nonce string) {
		dt := NewDuplicateTracker()

		// First check should not be duplicate
		isDup := dt.IsDuplicate(jobID, en1, en2, ntime, nonce)
		if isDup {
			t.Error("Fresh share marked as duplicate")
		}

		// Record and check again
		dt.Record(jobID, en1, en2, ntime, nonce)

		// Now should be duplicate
		isDup = dt.IsDuplicate(jobID, en1, en2, ntime, nonce)
		if !isDup {
			t.Error("Recorded share not detected as duplicate")
		}
	})
}
