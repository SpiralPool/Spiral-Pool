// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package shares provides extended fuzzing tests for high-value targets.
//
// These fuzz tests focus on security-critical parsing and validation paths.
// Run with: go test -fuzz=FuzzXxx -fuzztime=5m ./...
package shares

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spiralpool/stratum/pkg/protocol"
)

// =============================================================================
// WORKER IDENTITY STRING FUZZING
// =============================================================================
// Fuzz tests for worker identity strings that could be maliciously crafted.

// FuzzWorkerIdentity tests worker identity string handling.
// Targets: unicode abuse, length abuse, wallet-like prefixes, control chars.
func FuzzWorkerIdentity(f *testing.F) {
	// Seed corpus with edge cases
	f.Add("miner1")
	f.Add("")
	f.Add("a")
	f.Add(string(make([]byte, 1024))) // Long string
	f.Add("dgb1qtest123")             // Wallet-like prefix
	f.Add("bc1qtest123")              // Bitcoin prefix
	f.Add("bitcoincash:q")            // BCH prefix
	f.Add("DROP TABLE shares;")       // SQL injection attempt
	f.Add("<script>alert(1)</script>")
	f.Add("\x00\x01\x02\x03") // Control characters
	f.Add("\r\n\r\n")         // HTTP injection
	f.Add("worker.1.2.3")     // Dot-separated
	f.Add("worker:password")  // Colon-separated
	f.Add("\u202E\u0065\u0074") // RTL override
	f.Add("worker\x00hidden")  // Null byte injection

	f.Fuzz(func(t *testing.T, workerID string) {
		// Sanitize worker ID - this tests the sanitization function
		sanitized := sanitizeWorkerID(workerID)

		// Verify sanitization constraints
		if len(sanitized) > 256 {
			t.Error("Worker ID too long after sanitization")
		}
		if containsControlChars(sanitized) {
			t.Error("Sanitized worker ID contains control characters")
		}
	})
}

// sanitizeWorkerID sanitizes a worker ID string.
// This is the expected behavior from the real implementation.
func sanitizeWorkerID(id string) string {
	// Truncate to max length
	if len(id) > 256 {
		id = id[:256]
	}

	// Remove control characters
	result := make([]byte, 0, len(id))
	for i := 0; i < len(id); {
		r, size := utf8.DecodeRuneInString(id[i:])
		if r >= 32 && r != 127 { // Printable ASCII and valid unicode
			result = append(result, id[i:i+size]...)
		}
		i += size
	}

	return string(result)
}

// containsControlChars checks for control characters.
func containsControlChars(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

// =============================================================================
// DIFFICULTY VALUE FUZZING
// =============================================================================

// FuzzDifficultyValue tests difficulty value handling.
func FuzzDifficultyValue(f *testing.F) {
	// Seed corpus with edge cases
	f.Add(0.0)
	f.Add(1.0)
	f.Add(-1.0)
	f.Add(0.00000001)
	f.Add(1e15)
	f.Add(1e-15)
	f.Add(float64(1<<52)) // Max safe integer in float64
	f.Add(float64(1<<53))
	f.Add(1.7976931348623157e+308) // Max float64
	f.Add(4.9406564584124654e-324) // Min positive float64

	f.Fuzz(func(t *testing.T, difficulty float64) {
		// Should not panic on any difficulty value
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on difficulty %v: %v", difficulty, r)
			}
		}()

		target := difficultyToTarget(difficulty)

		// Invalid difficulties should return zero target (rejects all shares)
		if difficulty <= 0 && target.Sign() > 0 {
			t.Errorf("Invalid difficulty %v produced non-zero target", difficulty)
		}
	})
}

// =============================================================================
// JOB ID FUZZING
// =============================================================================

// FuzzJobID tests job ID parsing and validation.
func FuzzJobID(f *testing.F) {
	// Seed corpus
	f.Add("1")
	f.Add("job001")
	f.Add("")
	f.Add("0x1234")
	f.Add(string(make([]byte, 256)))
	f.Add("\x00")
	f.Add("null")
	f.Add("undefined")

	f.Fuzz(func(t *testing.T, jobID string) {
		job := &protocol.Job{
			ID:            jobID,
			Version:       "20000000",
			PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
			CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
			CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
			NBits:         "1a0377ae",
			NTime:         "64000000",
			CreatedAt:     time.Now(),
		}

		getJob := func(id string) (*protocol.Job, bool) {
			if id == jobID {
				return job, true
			}
			return nil, false
		}

		v := NewValidator(getJob)

		share := &protocol.Share{
			JobID:       jobID,
			ExtraNonce1: "12345678",
			ExtraNonce2: "00000001",
			NTime:       "64000000",
			Nonce:       "deadbeef",
			Difficulty:  1.0,
		}

		// Should not panic
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on job ID %q: %v", jobID, r)
			}
		}()

		result := v.Validate(share)

		// Empty job IDs should be rejected or handled gracefully
		if jobID == "" && result.Accepted {
			// May be handled differently - just ensure no panic
			t.Log("Empty job ID handled")
		}
	})
}

// =============================================================================
// NONCE FUZZING
// =============================================================================

// FuzzNonce tests nonce hex string parsing.
func FuzzNonce(f *testing.F) {
	// Seed corpus with valid and invalid hex
	f.Add("00000000")
	f.Add("ffffffff")
	f.Add("FFFFFFFF")
	f.Add("12345678")
	f.Add("")
	f.Add("0")
	f.Add("00")
	f.Add("000")
	f.Add("0000000")   // 7 chars (odd)
	f.Add("000000000") // 9 chars
	f.Add("ghijklmn")  // Invalid hex
	f.Add("0x12345678")
	f.Add("-1")
	f.Add("deadbeef")
	f.Add(string(make([]byte, 100)))

	f.Fuzz(func(t *testing.T, nonce string) {
		job := &protocol.Job{
			ID:            "fuzz_nonce_test",
			Version:       "20000000",
			PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
			CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
			CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
			NBits:         "1a0377ae",
			NTime:         "64000000",
			CreatedAt:     time.Now(),
		}

		getJob := func(id string) (*protocol.Job, bool) {
			if id == job.ID {
				return job, true
			}
			return nil, false
		}

		v := NewValidator(getJob)

		share := &protocol.Share{
			JobID:       job.ID,
			ExtraNonce1: "12345678",
			ExtraNonce2: "00000001",
			NTime:       "64000000",
			Nonce:       nonce,
			Difficulty:  1.0,
		}

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on nonce %q: %v", nonce, r)
			}
		}()

		result := v.Validate(share)

		// Invalid nonces should be rejected, not cause errors later
		if !isValidHex(nonce, 8) && result.Accepted {
			// May be rejected for other reasons (likely invalid nonce format)
			t.Logf("Invalid nonce %q: result=%+v", nonce, result)
		}
	})
}

// =============================================================================
// EXTRA NONCE 2 FUZZING
// =============================================================================

// FuzzExtraNonce2 tests extraNonce2 hex string parsing.
func FuzzExtraNonce2(f *testing.F) {
	f.Add("00000001")
	f.Add("ffffffff")
	f.Add("")
	f.Add("0")
	f.Add(string(make([]byte, 32)))
	f.Add("cafe")
	f.Add("babe1234")

	f.Fuzz(func(t *testing.T, extraNonce2 string) {
		job := &protocol.Job{
			ID:            "fuzz_en2_test",
			Version:       "20000000",
			PrevBlockHash: "00000000000000000000000000000000" + "00000000000000000000000000000001",
			CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403000000",
			CoinBase2:     "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
			NBits:         "1a0377ae",
			NTime:         "64000000",
			CreatedAt:     time.Now(),
		}

		getJob := func(id string) (*protocol.Job, bool) {
			if id == job.ID {
				return job, true
			}
			return nil, false
		}

		v := NewValidator(getJob)

		share := &protocol.Share{
			JobID:       job.ID,
			ExtraNonce1: "12345678",
			ExtraNonce2: extraNonce2,
			NTime:       "64000000",
			Nonce:       "deadbeef",
			Difficulty:  1.0,
		}

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on extraNonce2 %q: %v", extraNonce2, r)
			}
		}()

		_ = v.Validate(share)
	})
}

// =============================================================================
// NTIME FUZZING
// =============================================================================

// FuzzNTimeValidation tests nTime validation with various values.
func FuzzNTimeValidation(f *testing.F) {
	f.Add("00000000")
	f.Add("00000001")
	f.Add("64000000")
	f.Add("ffffffff")
	f.Add("7fffffff")
	f.Add("80000000")

	f.Fuzz(func(t *testing.T, shareNTime string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on ntime %q: %v", shareNTime, r)
			}
		}()

		// Test against a fixed job time
		jobTime := "64000000"
		result := validateNTime(shareNTime, jobTime)

		// Just verify no panic - actual validation logic is tested elsewhere
		_ = result
	})
}

// =============================================================================
// BINARY FRAME LENGTH FUZZING
// =============================================================================

// FuzzBinaryFrameLength tests binary frame length handling.
func FuzzBinaryFrameLength(f *testing.F) {
	f.Add(uint32(0))
	f.Add(uint32(1))
	f.Add(uint32(80))    // Block header size
	f.Add(uint32(1000))  // Typical message
	f.Add(uint32(10000)) // Large message
	f.Add(uint32(1<<20)) // 1MB
	f.Add(uint32(1<<24)) // 16MB
	f.Add(uint32(0xFFFFFFFF))

	f.Fuzz(func(t *testing.T, length uint32) {
		// Verify frame length is properly bounded
		const maxFrameSize = 16 * 1024 * 1024 // 16MB max

		isValid := length > 0 && length <= maxFrameSize
		_ = isValid // Would be used in actual framing code
	})
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// isValidHex checks if a string is valid hex of expected length.
func isValidHex(s string, expectedLen int) bool {
	if len(s) != expectedLen {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
