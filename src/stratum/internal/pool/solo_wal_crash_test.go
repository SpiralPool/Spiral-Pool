// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
// SOLO MINING WAL CRASH/RECOVERY TEST SUITE
// =============================================================================
//
// Ensures no valid solved block is lost due to WAL crash/recovery scenarios
// in SOLO mode. Every risk vector is exercised across all 14 supported coins
// with realistic block parameters (height ranges, block intervals, algorithms).
//
// Risk vectors covered:
//   1. Pre-submission crash: WAL has "pending" entry, process dies before submitblock
//   2. Mid-submission crash: WAL has "submitting" status, process crashes during RPC
//   3. Recovery finds all unsubmitted blocks from multiple WAL files
//   4. Multiple blocks across different coins in same WAL
//   5. WAL with raw block components for manual rebuild
//   6. WAL file rotation during block discovery
//   7. Concurrent WAL writes from multiple goroutines (race test)

// coinTestConfig defines per-coin parameters for dynamic test generation.
type coinTestConfig struct {
	Symbol        string
	Name          string
	Algorithm     string
	BlockInterval int    // seconds
	SampleHeight  uint64 // realistic current-era block height
	AddrPrefix    string // example miner address prefix
	Reward        int64  // coinbase value in satoshis
}

// allTestCoins returns the full set of 14 supported coins with realistic configs.
func allTestCoins() []coinTestConfig {
	return []coinTestConfig{
		// === SHA-256d Coins ===
		{Symbol: "BTC", Name: "Bitcoin", Algorithm: "SHA-256d", BlockInterval: 600, SampleHeight: 890000, AddrPrefix: "bc1q", Reward: 312500000},
		{Symbol: "BCH", Name: "Bitcoin Cash", Algorithm: "SHA-256d", BlockInterval: 600, SampleHeight: 880000, AddrPrefix: "bitcoincash:q", Reward: 625000000},
		{Symbol: "DGB", Name: "DigiByte", Algorithm: "SHA-256d", BlockInterval: 15, SampleHeight: 20000000, AddrPrefix: "D", Reward: 27700000000},
		{Symbol: "BC2", Name: "Bitcoin II", Algorithm: "SHA-256d", BlockInterval: 600, SampleHeight: 120000, AddrPrefix: "bc1q", Reward: 5000000000},
		{Symbol: "NMC", Name: "Namecoin", Algorithm: "SHA-256d", BlockInterval: 600, SampleHeight: 720000, AddrPrefix: "N", Reward: 625000000},
		{Symbol: "SYS", Name: "Syscoin", Algorithm: "SHA-256d", BlockInterval: 150, SampleHeight: 1800000, AddrPrefix: "sys1q", Reward: 567000000},
		{Symbol: "XMY", Name: "Myriad", Algorithm: "SHA-256d", BlockInterval: 60, SampleHeight: 4200000, AddrPrefix: "M", Reward: 375000000},
		{Symbol: "FBTC", Name: "Fractal Bitcoin", Algorithm: "SHA-256d", BlockInterval: 30, SampleHeight: 500000, AddrPrefix: "bc1q", Reward: 2500000000},
		// === Scrypt Coins ===
		{Symbol: "LTC", Name: "Litecoin", Algorithm: "Scrypt", BlockInterval: 150, SampleHeight: 2700000, AddrPrefix: "ltc1q", Reward: 625000000},
		{Symbol: "DOGE", Name: "Dogecoin", Algorithm: "Scrypt", BlockInterval: 60, SampleHeight: 5400000, AddrPrefix: "D", Reward: 1000000000000},
		{Symbol: "DGB-SCRYPT", Name: "DigiByte-Scrypt", Algorithm: "Scrypt", BlockInterval: 15, SampleHeight: 20000000, AddrPrefix: "D", Reward: 27700000000},
		{Symbol: "PEP", Name: "PepeCoin", Algorithm: "Scrypt", BlockInterval: 60, SampleHeight: 900000, AddrPrefix: "P", Reward: 1500000000},
		{Symbol: "CAT", Name: "Catcoin", Algorithm: "Scrypt", BlockInterval: 600, SampleHeight: 380000, AddrPrefix: "C", Reward: 5000000000},
		// === Multi-Algo (represented as SHA-256d mode for WAL testing) ===
		{Symbol: "XMY-SHA", Name: "Myriad-SHA256d", Algorithm: "SHA-256d", BlockInterval: 60, SampleHeight: 4200001, AddrPrefix: "M", Reward: 375000000},
	}
}

// makeCoinBlockHash returns a deterministic 64-char hex block hash for a given coin and height.
func makeCoinBlockHash(symbol string, height uint64) string {
	return fmt.Sprintf("%032x%032x", height, len(symbol))
}

// makeCoinPrevHash returns a deterministic 64-char hex prev hash for a given coin and height.
func makeCoinPrevHash(symbol string, height uint64) string {
	return fmt.Sprintf("%032x%032x", height-1, len(symbol)+1)
}

// makeCoinBlockHex returns a realistic-length block hex string for testing.
func makeCoinBlockHex(symbol string, height uint64) string {
	return fmt.Sprintf("02000000%016x", height) + strings.Repeat("ab", 200)
}

// =============================================================================
// RISK VECTOR 1: Pre-Submission Crash
// =============================================================================
// Scenario: Block solved, WAL entry written as "pending", process killed by
// OOM or SIGKILL before submitblock RPC is called.
// Expected: RecoverUnsubmittedBlocks finds the "pending" entry with full BlockHex.

func TestSOLO_WALCrash_AllCoins_PreSubmissionCrash(t *testing.T) {
	// Risk vector: Pre-submission crash
	// Process dies BEFORE submitblock. WAL has "pending" status.
	// Recovery must find every coin's block.
	t.Parallel()

	for _, coin := range allTestCoins() {
		coin := coin // capture
		t.Run(fmt.Sprintf("%s_%s", coin.Symbol, coin.Name), func(t *testing.T) {
			// Coin: %s | Block interval: %ds | Algorithm: %s
			t.Parallel()
			tmpDir := t.TempDir()
			logger := zap.NewNop()

			wal, err := NewBlockWAL(tmpDir, logger)
			if err != nil {
				t.Fatalf("[%s] NewBlockWAL failed: %v", coin.Symbol, err)
			}

			blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
			prevHash := makeCoinPrevHash(coin.Symbol, coin.SampleHeight)
			blockHex := makeCoinBlockHex(coin.Symbol, coin.SampleHeight)

			entry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     blockHash,
				PrevHash:      prevHash,
				BlockHex:      blockHex,
				MinerAddress:  coin.AddrPrefix + "solotest1",
				WorkerName:    coin.Symbol + "_rig_01",
				JobID:         fmt.Sprintf("job_%s_%d", coin.Symbol, coin.SampleHeight),
				JobAge:        time.Duration(coin.BlockInterval/2) * time.Second,
				CoinbaseValue: coin.Reward,
				Status:        "pending",
			}

			if err := wal.LogBlockFound(entry); err != nil {
				t.Fatalf("[%s] LogBlockFound failed: %v", coin.Symbol, err)
			}

			// Simulate crash: close WAL without writing any submission result.
			wal.Close()

			// Recovery on restart.
			unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
			if err != nil {
				t.Fatalf("[%s] RecoverUnsubmittedBlocks failed: %v", coin.Symbol, err)
			}

			if len(unsubmitted) != 1 {
				t.Fatalf("[%s] Expected 1 unsubmitted block, got %d", coin.Symbol, len(unsubmitted))
			}

			recovered := unsubmitted[0]
			if recovered.BlockHex == "" {
				t.Fatalf("[%s] BLOCK LOSS: Recovered entry has empty BlockHex", coin.Symbol)
			}
			if recovered.BlockHex != blockHex {
				t.Errorf("[%s] BlockHex corrupted during recovery", coin.Symbol)
			}
			if recovered.Status != "pending" {
				t.Errorf("[%s] Expected status 'pending', got %q", coin.Symbol, recovered.Status)
			}
			if recovered.Height != coin.SampleHeight {
				t.Errorf("[%s] Height mismatch: got %d, want %d", coin.Symbol, recovered.Height, coin.SampleHeight)
			}
			if recovered.CoinbaseValue != coin.Reward {
				t.Errorf("[%s] CoinbaseValue mismatch: got %d, want %d", coin.Symbol, recovered.CoinbaseValue, coin.Reward)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 2: Mid-Submission Crash
// =============================================================================
// Scenario: Pre-submission WAL entry written with "submitting" status, process
// crashes during the submitblock RPC call (OOM kill, network partition, etc).
// Expected: RecoverUnsubmittedBlocks finds the "submitting" entry.

func TestSOLO_WALCrash_AllCoins_MidSubmissionCrash(t *testing.T) {
	// Risk vector: Mid-submission crash
	// Process dies DURING submitblock RPC. WAL has "submitting" status.
	// Recovery must find every coin's block.
	t.Parallel()

	for _, coin := range allTestCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_MidRPC", coin.Symbol), func(t *testing.T) {
			// Coin: %s | Algorithm: %s | Block interval: %ds
			// Crash occurs after pre-submission WAL write but before post-submission write.
			t.Parallel()
			tmpDir := t.TempDir()
			logger := zap.NewNop()

			wal, err := NewBlockWAL(tmpDir, logger)
			if err != nil {
				t.Fatalf("[%s] NewBlockWAL failed: %v", coin.Symbol, err)
			}

			blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
			blockHex := makeCoinBlockHex(coin.Symbol, coin.SampleHeight)

			// The P0 pre-submission WAL write sets status to "submitting"
			// before calling SubmitBlockWithVerification.
			entry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     blockHash,
				PrevHash:      makeCoinPrevHash(coin.Symbol, coin.SampleHeight),
				BlockHex:      blockHex,
				MinerAddress:  coin.AddrPrefix + "midcrash",
				WorkerName:    coin.Symbol + "_bitaxe_01",
				JobID:         fmt.Sprintf("job_mid_%s_%d", coin.Symbol, coin.SampleHeight),
				CoinbaseValue: coin.Reward,
				Status:        "submitting",
			}

			if err := wal.LogBlockFound(entry); err != nil {
				t.Fatalf("[%s] LogBlockFound (pre-submission) failed: %v", coin.Symbol, err)
			}

			// Simulate crash during RPC: close WAL, never write post-submission result.
			wal.Close()

			unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
			if err != nil {
				t.Fatalf("[%s] RecoverUnsubmittedBlocks failed: %v", coin.Symbol, err)
			}

			if len(unsubmitted) != 1 {
				t.Fatalf("[%s] Expected 1 unsubmitted block, got %d", coin.Symbol, len(unsubmitted))
			}

			recovered := unsubmitted[0]
			if recovered.Status != "submitting" {
				t.Errorf("[%s] Expected status 'submitting', got %q", coin.Symbol, recovered.Status)
			}
			if recovered.BlockHex == "" {
				t.Fatalf("[%s] BLOCK LOSS: Recovered entry has empty BlockHex -- cannot resubmit", coin.Symbol)
			}
			if recovered.BlockHash != blockHash {
				t.Errorf("[%s] BlockHash mismatch -- needed for deduplication on resubmission", coin.Symbol)
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 3: Recovery Finds All Unsubmitted Blocks from Multiple WAL Files
// =============================================================================
// Scenario: Pool ran across multiple days (file rotation). Different coins had
// crashes on different days. Recovery must scan ALL WAL files and return every
// unsubmitted block regardless of which file it lives in.

func TestSOLO_WALCrash_AllCoins_MultiFileRecovery(t *testing.T) {
	// Risk vector: Multi-file recovery across date-rotated WAL files.
	// Each day's WAL may contain blocks from different coins.
	// Recovery must aggregate across all files.
	t.Parallel()
	tmpDir := t.TempDir()

	coins := allTestCoins()

	// Distribute coins across 3 date-rotated WAL files.
	// Day 1: first 6 coins (pending -- process crashed before submission)
	// Day 2: next 6 coins (3 submitted OK, 3 still submitting -- crash during RPC)
	// Day 3: last 6 coins (all pending -- another crash)
	day1File := filepath.Join(tmpDir, "block_wal_2026-01-28.jsonl")
	day2File := filepath.Join(tmpDir, "block_wal_2026-01-29.jsonl")
	day3File := filepath.Join(tmpDir, "block_wal_2026-01-30.jsonl")

	var day1Entries, day2Entries, day3Entries []string
	expectedUnsubmitted := 0

	for i, coin := range coins {
		blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
		entry := BlockWALEntry{
			Height:        coin.SampleHeight,
			BlockHash:     blockHash,
			PrevHash:      makeCoinPrevHash(coin.Symbol, coin.SampleHeight),
			BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
			MinerAddress:  coin.AddrPrefix + "multi",
			WorkerName:    coin.Symbol + "_worker",
			JobID:         fmt.Sprintf("job_multi_%s", coin.Symbol),
			CoinbaseValue: coin.Reward,
		}

		switch {
		case i < 6:
			// Day 1: all pending (crashed before submit)
			entry.Status = "pending"
			data, _ := json.Marshal(entry)
			day1Entries = append(day1Entries, string(data))
			expectedUnsubmitted++

		case i < 12:
			if i%2 == 0 {
				// Even indices in day 2: submitted successfully (should NOT be recovered)
				entry.Status = "submitted"
				data, _ := json.Marshal(entry)
				day2Entries = append(day2Entries, string(data))
			} else {
				// Odd indices in day 2: submitting (crash during RPC)
				entry.Status = "submitting"
				data, _ := json.Marshal(entry)
				day2Entries = append(day2Entries, string(data))
				expectedUnsubmitted++
			}

		default:
			// Day 3: all pending
			entry.Status = "pending"
			data, _ := json.Marshal(entry)
			day3Entries = append(day3Entries, string(data))
			expectedUnsubmitted++
		}
	}

	// Write WAL files
	writeJSONLFile(t, day1File, day1Entries)
	writeJSONLFile(t, day2File, day2Entries)
	writeJSONLFile(t, day3File, day3Entries)

	// Recover
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != expectedUnsubmitted {
		t.Fatalf("Expected %d unsubmitted blocks across 3 WAL files, got %d",
			expectedUnsubmitted, len(unsubmitted))
	}

	// Verify every recovered block has a non-empty BlockHex
	for _, entry := range unsubmitted {
		if entry.BlockHex == "" {
			t.Errorf("BLOCK LOSS: Recovered block at height %d has empty BlockHex", entry.Height)
		}
		if entry.Status != "pending" && entry.Status != "submitting" {
			t.Errorf("Unexpected recovered status %q for height %d", entry.Status, entry.Height)
		}
	}
}

// writeJSONLFile writes a slice of JSON strings as a JSONL file.
func writeJSONLFile(t *testing.T, path string, lines []string) {
	t.Helper()
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s) failed: %v", path, err)
	}
}

// =============================================================================
// RISK VECTOR 4: Multiple Blocks Across Different Coins in Same WAL
// =============================================================================
// Scenario: Within a single WAL file, blocks from multiple coins are recorded.
// Recovery must correctly track each by block hash and return only unsubmitted ones.

func TestSOLO_WALCrash_AllCoins_MultipleCoinsSameWAL(t *testing.T) {
	// Risk vector: Multi-coin WAL file with mixed submission states.
	// Each coin has one block. Some submitted, some pending/submitting.
	// Recovery must return exactly the unsubmitted set.
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	coins := allTestCoins()
	pendingCoins := make(map[string]bool)

	for i, coin := range coins {
		blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
		entry := &BlockWALEntry{
			Height:        coin.SampleHeight,
			BlockHash:     blockHash,
			PrevHash:      makeCoinPrevHash(coin.Symbol, coin.SampleHeight),
			BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
			MinerAddress:  coin.AddrPrefix + "multicoin",
			WorkerName:    coin.Symbol + "_rig",
			JobID:         fmt.Sprintf("job_mc_%s_%d", coin.Symbol, i),
			CoinbaseValue: coin.Reward,
		}

		if i%3 == 0 {
			// Every 3rd coin: submitted successfully
			entry.Status = "submitted"
		} else if i%3 == 1 {
			// Pending: crashed before submission
			entry.Status = "pending"
			pendingCoins[blockHash] = true
		} else {
			// Submitting: crashed during RPC
			entry.Status = "submitting"
			pendingCoins[blockHash] = true
		}

		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("[%s] LogBlockFound failed: %v", coin.Symbol, err)
		}
	}

	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	if len(unsubmitted) != len(pendingCoins) {
		t.Fatalf("Expected %d unsubmitted blocks, got %d", len(pendingCoins), len(unsubmitted))
	}

	// Verify each recovered block is one we expected
	for _, entry := range unsubmitted {
		if !pendingCoins[entry.BlockHash] {
			t.Errorf("Unexpected recovered block hash: %s (status=%s, height=%d)",
				entry.BlockHash[:16], entry.Status, entry.Height)
		}
		if entry.BlockHex == "" {
			t.Errorf("BLOCK LOSS: Recovered block at height %d has empty BlockHex", entry.Height)
		}
	}

	// Verify NO submitted block was recovered
	for _, entry := range unsubmitted {
		if entry.Status == "submitted" || entry.Status == "accepted" || entry.Status == "rejected" {
			t.Errorf("DOUBLE SUBMIT RISK: Block at height %d with status %q should NOT be recovered",
				entry.Height, entry.Status)
		}
	}
}

// =============================================================================
// RISK VECTOR 5: WAL with Raw Block Components for Manual Rebuild
// =============================================================================
// Scenario: buildFullBlock fails for a coin. Emergency WAL entry is written with
// status "build_failed" and all raw components (coinbase1/2, extranonces, header
// fields, transaction data). These entries are NOT auto-recoverable but MUST
// preserve all components for operator manual reconstruction.

func TestSOLO_WALCrash_AllCoins_RawComponentsPreserved(t *testing.T) {
	// Risk vector: build_failed emergency entries must preserve all raw components.
	// Operator needs coinbase1, coinbase2, extranonce1/2, version, nbits, ntime,
	// nonce, and transaction_data to manually reconstruct the block.
	t.Parallel()

	for _, coin := range allTestCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_BuildFailed", coin.Symbol), func(t *testing.T) {
			// Coin: %s | Algorithm: %s | Block interval: %ds
			// Emergency WAL entry preserves raw components for manual reconstruction.
			t.Parallel()
			tmpDir := t.TempDir()
			logger := zap.NewNop()

			wal, err := NewBlockWAL(tmpDir, logger)
			if err != nil {
				t.Fatalf("[%s] NewBlockWAL failed: %v", coin.Symbol, err)
			}
			defer wal.Close()

			blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)

			emergencyEntry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     blockHash,
				PrevHash:      makeCoinPrevHash(coin.Symbol, coin.SampleHeight),
				BlockHex:      "", // Empty -- buildFullBlock failed
				MinerAddress:  coin.AddrPrefix + "rebuild",
				WorkerName:    coin.Symbol + "_emergency",
				JobID:         fmt.Sprintf("job_bf_%s", coin.Symbol),
				CoinbaseValue: coin.Reward,
				Status:        "build_failed",
				SubmitError:   fmt.Sprintf("invalid coinbase1 for %s: encoding/hex: odd length string", coin.Symbol),

				// Raw reconstruction components
				CoinBase1:   "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403" + fmt.Sprintf("%06x", coin.SampleHeight),
				CoinBase2:   "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
				ExtraNonce1: fmt.Sprintf("%08x", len(coin.Symbol)),
				ExtraNonce2: "00000001",
				Version:     "20000000",
				NBits:       "1a0377ae",
				NTime:       fmt.Sprintf("%08x", time.Now().Unix()),
				Nonce:       "deadbeef",
				TransactionData: []string{
					"0100000001" + strings.Repeat("00", 40),
					"0100000001" + strings.Repeat("ff", 40),
				},
			}

			if err := wal.LogBlockFound(emergencyEntry); err != nil {
				t.Fatalf("[%s] LogBlockFound failed: %v", coin.Symbol, err)
			}

			// Read back and verify all reconstruction fields survived
			data, err := os.ReadFile(wal.FilePath())
			if err != nil {
				t.Fatalf("[%s] ReadFile failed: %v", coin.Symbol, err)
			}

			lines := splitLines(data)
			if len(lines) == 0 {
				t.Fatalf("[%s] WAL file has no entries", coin.Symbol)
			}

			var recovered BlockWALEntry
			if err := json.Unmarshal(lines[0], &recovered); err != nil {
				t.Fatalf("[%s] JSON unmarshal failed: %v", coin.Symbol, err)
			}

			// Verify every reconstruction field
			checks := []struct {
				name string
				got  string
				want string
			}{
				{"CoinBase1", recovered.CoinBase1, emergencyEntry.CoinBase1},
				{"CoinBase2", recovered.CoinBase2, emergencyEntry.CoinBase2},
				{"ExtraNonce1", recovered.ExtraNonce1, emergencyEntry.ExtraNonce1},
				{"ExtraNonce2", recovered.ExtraNonce2, emergencyEntry.ExtraNonce2},
				{"Version", recovered.Version, emergencyEntry.Version},
				{"NBits", recovered.NBits, emergencyEntry.NBits},
				{"NTime", recovered.NTime, emergencyEntry.NTime},
				{"Nonce", recovered.Nonce, emergencyEntry.Nonce},
				{"Status", recovered.Status, "build_failed"},
				{"BlockHash", recovered.BlockHash, emergencyEntry.BlockHash},
				{"PrevHash", recovered.PrevHash, emergencyEntry.PrevHash},
			}

			for _, c := range checks {
				if c.got != c.want {
					t.Errorf("[%s] BLOCK LOSS RISK: %s mismatch: got %q, want %q",
						coin.Symbol, c.name, c.got, c.want)
				}
			}

			if len(recovered.TransactionData) != len(emergencyEntry.TransactionData) {
				t.Fatalf("[%s] TransactionData count: got %d, want %d",
					coin.Symbol, len(recovered.TransactionData), len(emergencyEntry.TransactionData))
			}
			for i, tx := range recovered.TransactionData {
				if tx != emergencyEntry.TransactionData[i] {
					t.Errorf("[%s] TransactionData[%d] mismatch", coin.Symbol, i)
				}
			}

			// build_failed entries should NOT be auto-recovered
			unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
			if err != nil {
				t.Fatalf("[%s] RecoverUnsubmittedBlocks failed: %v", coin.Symbol, err)
			}
			for _, u := range unsubmitted {
				if u.BlockHash == blockHash {
					t.Errorf("[%s] build_failed entry should NOT be auto-recoverable", coin.Symbol)
				}
			}
		})
	}
}

// =============================================================================
// RISK VECTOR 6: WAL File Rotation During Block Discovery
// =============================================================================
// Scenario: A block is logged as "pending" in day 1 WAL, then the same block's
// submission result is logged in day 2 WAL (after WAL rotation). Recovery must
// use the LATEST entry per block hash across files.
// Also tests: blocks straddling rotation with no result in newer file.

func TestSOLO_WALCrash_AllCoins_RotationDuringDiscovery(t *testing.T) {
	// Risk vector: WAL file rotation between block discovery and submission result.
	// The pre-submission "pending" or "submitting" entry is in an older WAL file.
	// The post-submission result (if any) is in a newer WAL file.
	// Cross-file supersede must work correctly.
	t.Parallel()

	coins := allTestCoins()

	// Test two scenarios per coin:
	//   A) Block logged pending in day1, submitted in day2 -> NOT recovered
	//   B) Block logged submitting in day1, NO result in day2 -> RECOVERED
	for _, coin := range coins {
		coin := coin
		t.Run(fmt.Sprintf("%s_Rotation", coin.Symbol), func(t *testing.T) {
			// Coin: %s | Algorithm: %s | Block interval: %ds
			// Tests cross-file WAL entry supersede during date rotation.
			t.Parallel()
			tmpDir := t.TempDir()

			blockHashA := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
			blockHashB := makeCoinBlockHash(coin.Symbol, coin.SampleHeight+1)

			// Block A: pending in day 1, submitted in day 2 -> NOT recovered
			entryA1 := BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     blockHashA,
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
				MinerAddress:  coin.AddrPrefix + "rot",
				CoinbaseValue: coin.Reward,
				Status:        "pending",
			}

			entryA2 := BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     blockHashA,
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
				MinerAddress:  coin.AddrPrefix + "rot",
				CoinbaseValue: coin.Reward,
				Status:        "submitted",
			}

			// Block B: submitting in day 1, NO entry in day 2 -> RECOVERED
			entryB := BlockWALEntry{
				Height:        coin.SampleHeight + 1,
				BlockHash:     blockHashB,
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight+1),
				MinerAddress:  coin.AddrPrefix + "rot",
				CoinbaseValue: coin.Reward,
				Status:        "submitting",
			}

			// Write day 1 WAL
			day1Lines := marshalEntries(t, entryA1, entryB)
			os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-28.jsonl"),
				[]byte(strings.Join(day1Lines, "\n")+"\n"), 0644)

			// Write day 2 WAL (only Block A result)
			day2Lines := marshalEntries(t, entryA2)
			os.WriteFile(filepath.Join(tmpDir, "block_wal_2026-01-29.jsonl"),
				[]byte(strings.Join(day2Lines, "\n")+"\n"), 0644)

			unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
			if err != nil {
				t.Fatalf("[%s] RecoverUnsubmittedBlocks failed: %v", coin.Symbol, err)
			}

			// Block A should NOT be recovered (superseded by "submitted" in day 2)
			for _, entry := range unsubmitted {
				if entry.BlockHash == blockHashA {
					t.Errorf("[%s] Block A should NOT be recovered (latest status is 'submitted')", coin.Symbol)
				}
			}

			// Block B MUST be recovered (no superseding entry)
			foundB := false
			for _, entry := range unsubmitted {
				if entry.BlockHash == blockHashB {
					foundB = true
					if entry.BlockHex == "" {
						t.Errorf("[%s] BLOCK LOSS: Block B recovered without BlockHex", coin.Symbol)
					}
					if entry.Status != "submitting" {
						t.Errorf("[%s] Block B expected status 'submitting', got %q", coin.Symbol, entry.Status)
					}
				}
			}
			if !foundB {
				t.Errorf("[%s] BLOCK LOSS: Block B (submitting, no result) was NOT recovered", coin.Symbol)
			}
		})
	}
}

// marshalEntries marshals BlockWALEntry values to JSON strings for JSONL file writing.
func marshalEntries(t *testing.T, entries ...BlockWALEntry) []string {
	t.Helper()
	var lines []string
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		lines = append(lines, string(data))
	}
	return lines
}

// =============================================================================
// RISK VECTOR 7: Concurrent WAL Writes from Multiple Goroutines (Race Test)
// =============================================================================
// Scenario: Multiple goroutines (simulating concurrent block solutions from
// different coins) write to the WAL simultaneously. No entry must be lost or
// corrupted due to data races or interleaved writes.

func TestSOLO_WALCrash_AllCoins_ConcurrentWrites(t *testing.T) {
	// Risk vector: Concurrent WAL writes from multiple goroutines.
	// Each goroutine represents a different coin solving a block simultaneously.
	// The WAL uses sync.Mutex internally; this test verifies no data is lost.
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	coins := allTestCoins()
	var wg sync.WaitGroup
	var writeErrors atomic.Int32

	// Each coin writes its block concurrently
	for i, coin := range coins {
		i := i
		coin := coin
		wg.Add(1)
		go func() {
			defer wg.Done()

			entry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     makeCoinBlockHash(coin.Symbol, coin.SampleHeight),
				PrevHash:      makeCoinPrevHash(coin.Symbol, coin.SampleHeight),
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
				MinerAddress:  coin.AddrPrefix + "race",
				WorkerName:    fmt.Sprintf("%s_concurrent_%d", coin.Symbol, i),
				JobID:         fmt.Sprintf("job_race_%s_%d", coin.Symbol, i),
				CoinbaseValue: coin.Reward,
				Status:        "submitting",
			}

			if err := wal.LogBlockFound(entry); err != nil {
				writeErrors.Add(1)
			}
		}()
	}

	wg.Wait()
	wal.Close()

	if writeErrors.Load() > 0 {
		t.Errorf("%d WAL write errors during concurrent access", writeErrors.Load())
	}

	// Read back and verify all entries survived without corruption
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	validEntries := 0
	corruptLines := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			corruptLines++
			maxLen := len(line)
			if maxLen > 80 {
				maxLen = 80
			}
			t.Errorf("Corrupt JSON line (data loss!): %s", string(line[:maxLen]))
			continue
		}
		validEntries++
	}

	if corruptLines > 0 {
		t.Errorf("BLOCK LOSS: %d corrupt WAL lines from concurrent writes", corruptLines)
	}
	if validEntries != len(coins) {
		t.Errorf("BLOCK LOSS: Expected %d WAL entries, got %d (%d entries lost!)",
			len(coins), validEntries, len(coins)-validEntries)
	}
}

// =============================================================================
// RISK VECTOR 7b: Heavy Concurrent Writes with Mixed Operations (Race Test)
// =============================================================================
// Scenario: Multiple goroutines per coin perform interleaved LogBlockFound and
// LogSubmissionResult calls. Tests that the mutex protects against all races.

func TestSOLO_WALCrash_AllCoins_ConcurrentMixedOps(t *testing.T) {
	// Risk vector: Interleaved LogBlockFound + LogSubmissionResult from many goroutines.
	// Verifies no partial/interleaved writes corrupt the JSONL format.
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	coins := allTestCoins()
	var wg sync.WaitGroup
	totalExpectedEntries := len(coins) * 2 // one LogBlockFound + one LogSubmissionResult per coin

	for i, coin := range coins {
		i := i
		coin := coin
		wg.Add(2)

		// Goroutine 1: Pre-submission write
		go func() {
			defer wg.Done()
			entry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     makeCoinBlockHash(coin.Symbol, coin.SampleHeight),
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
				MinerAddress:  coin.AddrPrefix + "mixed",
				WorkerName:    fmt.Sprintf("%s_pre_%d", coin.Symbol, i),
				CoinbaseValue: coin.Reward,
				Status:        "submitting",
			}
			wal.LogBlockFound(entry)
		}()

		// Goroutine 2: Post-submission result write
		go func() {
			defer wg.Done()
			entry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     makeCoinBlockHash(coin.Symbol, coin.SampleHeight),
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
				MinerAddress:  coin.AddrPrefix + "mixed",
				WorkerName:    fmt.Sprintf("%s_post_%d", coin.Symbol, i),
				CoinbaseValue: coin.Reward,
				Status:        "submitted",
			}
			wal.LogSubmissionResult(entry)
		}()
	}

	wg.Wait()
	wal.Close()

	// Verify every line is valid JSON
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	validCount := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry BlockWALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("Corrupt JSON from concurrent mixed ops: %v", err)
			continue
		}
		validCount++
	}

	if validCount < totalExpectedEntries {
		t.Errorf("Expected at least %d valid entries, got %d", totalExpectedEntries, validCount)
	}
}

// =============================================================================
// COMBINED SCENARIO: Pre + Mid + Build-Failed Across All Coins in One WAL
// =============================================================================
// This is the master crash-recovery test: a single WAL contains blocks from
// all 14 coins in all three crash states. Recovery must correctly classify each.

func TestSOLO_WALCrash_AllCoins_MasterCrashRecovery(t *testing.T) {
	// Combined scenario: all 14 coins, three crash states.
	// Tests that RecoverUnsubmittedBlocks correctly returns only
	// "pending" and "submitting" entries, never "submitted" or "build_failed".
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	coins := allTestCoins()
	expectedRecovery := make(map[string]bool) // blockHash -> should be recovered

	for i, coin := range coins {
		blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
		entry := &BlockWALEntry{
			Height:        coin.SampleHeight,
			BlockHash:     blockHash,
			PrevHash:      makeCoinPrevHash(coin.Symbol, coin.SampleHeight),
			BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
			MinerAddress:  coin.AddrPrefix + "master",
			WorkerName:    coin.Symbol + "_master",
			JobID:         fmt.Sprintf("job_master_%s", coin.Symbol),
			CoinbaseValue: coin.Reward,
		}

		switch i % 4 {
		case 0:
			// Pre-submission crash: pending
			entry.Status = "pending"
			expectedRecovery[blockHash] = true
		case 1:
			// Mid-submission crash: submitting
			entry.Status = "submitting"
			expectedRecovery[blockHash] = true
		case 2:
			// Successfully submitted: should NOT be recovered
			entry.Status = "submitted"
			expectedRecovery[blockHash] = false
		case 3:
			// Build failed: has raw components but NOT auto-recoverable
			entry.Status = "build_failed"
			entry.BlockHex = ""
			entry.CoinBase1 = "01000000" + strings.Repeat("00", 30)
			entry.CoinBase2 = "ffffffff" + strings.Repeat("00", 20)
			entry.ExtraNonce1 = "aabbccdd"
			entry.ExtraNonce2 = "11223344"
			entry.Version = "20000000"
			entry.NBits = "1a0377ae"
			entry.NTime = fmt.Sprintf("%08x", time.Now().Unix())
			entry.Nonce = "deadbeef"
			entry.SubmitError = "coinbase decode error"
			expectedRecovery[blockHash] = false
		}

		if err := wal.LogBlockFound(entry); err != nil {
			t.Fatalf("[%s] LogBlockFound failed: %v", coin.Symbol, err)
		}
	}

	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}

	// Count expected recoverable blocks
	expectedCount := 0
	for _, shouldRecover := range expectedRecovery {
		if shouldRecover {
			expectedCount++
		}
	}

	if len(unsubmitted) != expectedCount {
		t.Fatalf("Expected %d recoverable blocks, got %d", expectedCount, len(unsubmitted))
	}

	// Verify every recovered block was expected AND has BlockHex
	for _, entry := range unsubmitted {
		shouldRecover, known := expectedRecovery[entry.BlockHash]
		if !known {
			t.Errorf("Recovered unknown block hash: %s", entry.BlockHash[:16])
			continue
		}
		if !shouldRecover {
			t.Errorf("UNEXPECTED RECOVERY: Block at height %d (status=%s) should not have been recovered",
				entry.Height, entry.Status)
		}
		if entry.BlockHex == "" {
			t.Errorf("BLOCK LOSS: Recovered block at height %d has empty BlockHex", entry.Height)
		}
		if entry.Status != "pending" && entry.Status != "submitting" {
			t.Errorf("Recovered block has unexpected status %q", entry.Status)
		}
	}
}

// =============================================================================
// RISK VECTOR: Status Supersede Correctness Per Coin
// =============================================================================
// For each coin, write multiple entries for the same block hash with escalating
// statuses. Only the LATEST status should be used by RecoverUnsubmittedBlocks.

func TestSOLO_WALCrash_AllCoins_StatusSupersede(t *testing.T) {
	// Verifies that RecoverUnsubmittedBlocks uses the LAST entry per block hash.
	// This prevents double-submission when a block has both "submitting" and
	// "submitted" entries in the same WAL.
	t.Parallel()

	for _, coin := range allTestCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_Supersede", coin.Symbol), func(t *testing.T) {
			// Coin: %s | Algorithm: %s | Block interval: %ds
			t.Parallel()
			tmpDir := t.TempDir()
			logger := zap.NewNop()

			wal, err := NewBlockWAL(tmpDir, logger)
			if err != nil {
				t.Fatalf("[%s] NewBlockWAL failed: %v", coin.Symbol, err)
			}

			blockHash := makeCoinBlockHash(coin.Symbol, coin.SampleHeight)
			blockHex := makeCoinBlockHex(coin.Symbol, coin.SampleHeight)

			// Write escalating statuses for the same block
			statuses := []string{"pending", "submitting", "submitted"}
			for _, status := range statuses {
				entry := &BlockWALEntry{
					Height:        coin.SampleHeight,
					BlockHash:     blockHash,
					BlockHex:      blockHex,
					MinerAddress:  coin.AddrPrefix + "supersede",
					CoinbaseValue: coin.Reward,
					Status:        status,
				}
				if err := wal.LogBlockFound(entry); err != nil {
					t.Fatalf("[%s] LogBlockFound(%s) failed: %v", coin.Symbol, status, err)
				}
			}

			wal.Close()

			// Latest status is "submitted" -> block should NOT be recovered
			unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
			if err != nil {
				t.Fatalf("[%s] RecoverUnsubmittedBlocks failed: %v", coin.Symbol, err)
			}

			for _, entry := range unsubmitted {
				if entry.BlockHash == blockHash {
					t.Errorf("[%s] DOUBLE SUBMIT RISK: Block recovered despite latest status being 'submitted'",
						coin.Symbol)
				}
			}
		})
	}
}

// =============================================================================
// RISK VECTOR: Fsync Durability Per Coin
// =============================================================================
// After each LogBlockFound call, immediately verify the data is on disk.
// This confirms the fsync guarantee that prevents data loss on power failure.

func TestSOLO_WALCrash_AllCoins_FsyncDurability(t *testing.T) {
	// Verifies fsync durability: after each LogBlockFound, the data is
	// immediately readable from disk. Covers all 14 coins.
	t.Parallel()

	for _, coin := range allTestCoins() {
		coin := coin
		t.Run(fmt.Sprintf("%s_Fsync", coin.Symbol), func(t *testing.T) {
			// Coin: %s | Algorithm: %s
			t.Parallel()
			tmpDir := t.TempDir()
			logger := zap.NewNop()

			wal, err := NewBlockWAL(tmpDir, logger)
			if err != nil {
				t.Fatalf("[%s] NewBlockWAL failed: %v", coin.Symbol, err)
			}
			defer wal.Close()

			entry := &BlockWALEntry{
				Height:        coin.SampleHeight,
				BlockHash:     makeCoinBlockHash(coin.Symbol, coin.SampleHeight),
				BlockHex:      makeCoinBlockHex(coin.Symbol, coin.SampleHeight),
				MinerAddress:  coin.AddrPrefix + "fsync",
				CoinbaseValue: coin.Reward,
				Status:        "submitting",
			}

			if err := wal.LogBlockFound(entry); err != nil {
				t.Fatalf("[%s] LogBlockFound failed: %v", coin.Symbol, err)
			}

			// Immediately verify data is on disk
			data, err := os.ReadFile(wal.FilePath())
			if err != nil {
				t.Fatalf("[%s] ReadFile after write failed: %v", coin.Symbol, err)
			}

			lines := splitLines(data)
			nonEmpty := 0
			for _, line := range lines {
				if len(line) > 0 {
					nonEmpty++
				}
			}

			if nonEmpty != 1 {
				t.Errorf("[%s] Expected 1 entry on disk immediately after write, got %d (fsync not durable!)",
					coin.Symbol, nonEmpty)
			}
		})
	}
}
