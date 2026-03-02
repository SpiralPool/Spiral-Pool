// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package pool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// =============================================================================
// SOLO BUILD FAILURE TEST SUITE: No Valid Solved Block Lost on buildFullBlock Failure
// =============================================================================
//
// These tests verify that when buildFullBlock fails during solo mining, the WAL
// preserves all raw block components so that an operator can manually reconstruct
// and submit the block. This prevents permanent loss of solved blocks.
//
// Coins tested (13 total):
//   SHA-256d: BTC(600s), BCH(600s), DGB(15s), BC2(600s), NMC(600s,aux),
//             SYS(60s,aux), XMY(60s,aux), FBTC(30s,aux)
//   Scrypt:   LTC(150s), DOGE(60s,aux), DGB-SCRYPT(15s),
//             PEP(60s,aux), CAT(600s)
//
// Risk vectors:
//   1. BlockHex empty but raw components populated - manual rebuild possible
//   2. "build_failed" status with raw components preserved in WAL
//   3. Recovery shows build_failed entries with raw components intact
//   4. Multiple build failures across different coins, all raw components preserved
//   5. Partial block data (some raw components missing) - verify which fields present
//   6. build_failed followed by successful rebuild from WAL components

// coinConfig holds inline configuration for each testable coin.
type coinConfig struct {
	Coin          string
	Algorithm     string
	BlockInterval int // seconds
	AuxPow        bool
	MinerAddress  string
	Height        uint64
	CoinbaseValue int64  // satoshis
	NBits         string // difficulty encoding
}

// allCoins returns the full set of 13 coins with inline configuration.
func allCoins() []coinConfig {
	return []coinConfig{
		// SHA-256d coins
		{Coin: "BTC", Algorithm: "SHA-256d", BlockInterval: 600, AuxPow: false, MinerAddress: "bc1qsolominer", Height: 880001, CoinbaseValue: 312500000, NBits: "1703255b"},
		{Coin: "BCH", Algorithm: "SHA-256d", BlockInterval: 600, AuxPow: false, MinerAddress: "bitcoincash:qpsolominer", Height: 880002, CoinbaseValue: 312500000, NBits: "18034287"},
		{Coin: "DGB", Algorithm: "SHA-256d", BlockInterval: 15, AuxPow: false, MinerAddress: "dgb1qsolominer", Height: 20000001, CoinbaseValue: 27700000000, NBits: "1a0377ae"},
		{Coin: "BC2", Algorithm: "SHA-256d", BlockInterval: 600, AuxPow: false, MinerAddress: "bc2solo1qminer", Height: 100001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
		{Coin: "NMC", Algorithm: "SHA-256d", BlockInterval: 600, AuxPow: true, MinerAddress: "N1soloMiner", Height: 700001, CoinbaseValue: 5000000000, NBits: "1a06b4be"},
		{Coin: "SYS", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true, MinerAddress: "sys1qsolominer", Height: 1500001, CoinbaseValue: 606060606, NBits: "1c00d2d1"},
		{Coin: "XMY", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true, MinerAddress: "MsoloMiner1addr", Height: 3000001, CoinbaseValue: 250000000, NBits: "1c0d3142"},
		{Coin: "FBTC", Algorithm: "SHA-256d", BlockInterval: 30, AuxPow: true, MinerAddress: "FsoloMiner1addr", Height: 200001, CoinbaseValue: 2500000000, NBits: "1d00ffff"},

		// Scrypt coins
		{Coin: "LTC", Algorithm: "Scrypt", BlockInterval: 150, AuxPow: false, MinerAddress: "ltc1qsolominer", Height: 2800001, CoinbaseValue: 625000000, NBits: "1a01cc46"},
		{Coin: "DOGE", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true, MinerAddress: "DSoloMinerAddr1", Height: 5200001, CoinbaseValue: 1000000000000, NBits: "1a063051"},
		{Coin: "DGB-SCRYPT", Algorithm: "Scrypt", BlockInterval: 15, AuxPow: false, MinerAddress: "dgb1qscryptminer", Height: 20000002, CoinbaseValue: 27700000000, NBits: "1a0377ae"},
		{Coin: "PEP", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true, MinerAddress: "PsoloMiner1addr", Height: 400001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
		{Coin: "CAT", Algorithm: "Scrypt", BlockInterval: 600, AuxPow: false, MinerAddress: "9soloMinerCATaddr", Height: 300001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
	}
}

// buildFailedEntry constructs a BlockWALEntry simulating a buildFullBlock failure
// for a specific coin. BlockHex is empty; all raw components are populated.
func buildFailedEntry(cc coinConfig, jobSuffix string) *BlockWALEntry {
	return &BlockWALEntry{
		Height:        cc.Height,
		BlockHash:     fmt.Sprintf("000000000000000000%s_%s_%016x", cc.Coin, jobSuffix, cc.Height),
		PrevHash:      fmt.Sprintf("000000000000000000prev_%s_%016x", cc.Coin, cc.Height-1),
		BlockHex:      "", // Empty - buildFullBlock failed
		MinerAddress:  cc.MinerAddress,
		WorkerName:    fmt.Sprintf("%s_rig1", strings.ToLower(cc.Coin)),
		JobID:         fmt.Sprintf("job_%s_%s", cc.Coin, jobSuffix),
		CoinbaseValue: cc.CoinbaseValue,
		Status:        "build_failed",
		SubmitError:   fmt.Sprintf("buildFullBlock failed for %s: invalid coinbase assembly", cc.Coin),

		// Raw components for manual reconstruction
		CoinBase1:   fmt.Sprintf("01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403%s00", cc.Coin),
		CoinBase2:   "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		ExtraNonce1: "aabbccdd",
		ExtraNonce2: "11223344",
		Version:     "20000000",
		NBits:       cc.NBits,
		NTime:       "67a2b000",
		Nonce:       "deadbeef",
		TransactionData: []string{
			fmt.Sprintf("0100000001%s_tx1_%064x", cc.Coin, cc.Height),
			fmt.Sprintf("0100000001%s_tx2_%064x", cc.Coin, cc.Height),
		},
	}
}

// assertRawComponentsIntact verifies all raw block components survived WAL roundtrip.
func assertRawComponentsIntact(t *testing.T, label string, original, recovered *BlockWALEntry) {
	t.Helper()

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"CoinBase1", recovered.CoinBase1, original.CoinBase1},
		{"CoinBase2", recovered.CoinBase2, original.CoinBase2},
		{"ExtraNonce1", recovered.ExtraNonce1, original.ExtraNonce1},
		{"ExtraNonce2", recovered.ExtraNonce2, original.ExtraNonce2},
		{"Version", recovered.Version, original.Version},
		{"NBits", recovered.NBits, original.NBits},
		{"NTime", recovered.NTime, original.NTime},
		{"Nonce", recovered.Nonce, original.Nonce},
		{"Status", recovered.Status, original.Status},
		{"SubmitError", recovered.SubmitError, original.SubmitError},
		{"BlockHash", recovered.BlockHash, original.BlockHash},
		{"PrevHash", recovered.PrevHash, original.PrevHash},
		{"MinerAddress", recovered.MinerAddress, original.MinerAddress},
		{"WorkerName", recovered.WorkerName, original.WorkerName},
		{"JobID", recovered.JobID, original.JobID},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("[%s] %s mismatch: got %q, want %q", label, c.name, c.got, c.want)
		}
	}

	if recovered.Height != original.Height {
		t.Errorf("[%s] Height mismatch: got %d, want %d", label, recovered.Height, original.Height)
	}
	if recovered.CoinbaseValue != original.CoinbaseValue {
		t.Errorf("[%s] CoinbaseValue mismatch: got %d, want %d", label, recovered.CoinbaseValue, original.CoinbaseValue)
	}

	if len(recovered.TransactionData) != len(original.TransactionData) {
		t.Errorf("[%s] TransactionData count: got %d, want %d",
			label, len(recovered.TransactionData), len(original.TransactionData))
	} else {
		for i, tx := range recovered.TransactionData {
			if tx != original.TransactionData[i] {
				t.Errorf("[%s] TransactionData[%d] mismatch", label, i)
			}
		}
	}

	// BlockHex should be empty for build_failed entries
	if recovered.BlockHex != "" {
		t.Errorf("[%s] BlockHex should be empty for build_failed, got %q", label, recovered.BlockHex)
	}
}

// recoverAllBuildFailed reads all WAL files and returns entries with "build_failed" status.
// Unlike RecoverUnsubmittedBlocks (which only returns pending/submitting), this
// returns build_failed entries that require manual operator intervention.
func recoverAllBuildFailed(t *testing.T, dataDir string) []BlockWALEntry {
	t.Helper()

	entries, err := recoverAllEntries(t, dataDir)
	if err != nil {
		t.Fatalf("recoverAllEntries failed: %v", err)
	}

	var buildFailed []BlockWALEntry
	// Deduplicate by block hash, keeping the latest entry per hash.
	seen := make(map[string]*BlockWALEntry)
	for i := range entries {
		seen[entries[i].BlockHash] = &entries[i]
	}
	for _, entry := range seen {
		if entry.Status == "build_failed" {
			buildFailed = append(buildFailed, *entry)
		}
	}
	return buildFailed
}

// recoverAllEntries reads all WAL JSONL files from the directory and returns
// every parseable entry (all statuses).
func recoverAllEntries(t *testing.T, dataDir string) ([]BlockWALEntry, error) {
	t.Helper()

	pattern := filepath.Join(dataDir, "block_wal_*.jsonl")
	// Use filepath.Glob-compatible approach
	files, err := doubleStarGlob(dataDir)
	if err != nil {
		return nil, fmt.Errorf("glob %s failed: %w", pattern, err)
	}

	var all []BlockWALEntry
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		lines := splitLines(data)
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			var entry BlockWALEntry
			if err := json.Unmarshal(line, &entry); err != nil {
				continue
			}
			all = append(all, entry)
		}
	}
	return all, nil
}

// doubleStarGlob finds all block_wal_*.jsonl files in a directory.
func doubleStarGlob(dataDir string) ([]string, error) {
	dirEntries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, de := range dirEntries {
		if !de.IsDir() && strings.HasPrefix(de.Name(), "block_wal_") && strings.HasSuffix(de.Name(), ".jsonl") {
			matches = append(matches, filepath.Join(dataDir, de.Name()))
		}
	}
	return matches, nil
}

// =============================================================================
// RISK VECTOR 1: BlockHex Empty, Raw Components Populated - Manual Rebuild
// =============================================================================

// TestSOLO_BuildFailure_BTC_EmptyBlockHexRawComponentsIntact tests that when
// buildFullBlock fails for a BTC solo block, the WAL entry has empty BlockHex
// but all raw components (CoinBase1, CoinBase2, ExtraNonce1, ExtraNonce2,
// Version, NBits, NTime, Nonce, TransactionData) are preserved for manual
// reconstruction via: bitcoin-cli submitblock <rebuilt_hex>
//
// Risk vector: #1 - BlockHex empty but raw components populated
// Coin: BTC, Block interval: 600s, Algorithm: SHA-256d
func TestSOLO_BuildFailure_BTC_EmptyBlockHexRawComponentsIntact(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "BTC", Algorithm: "SHA-256d", BlockInterval: 600,
		MinerAddress: "bc1qsolominer", Height: 880001,
		CoinbaseValue: 312500000, NBits: "1703255b",
	}
	entry := buildFailedEntry(cc, "rv1_btc")

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	// Reopen via recovery - read raw file
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL file has no entries after build_failed write")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Core assertion: BlockHex is empty but raw components are present
	if recovered.BlockHex != "" {
		t.Errorf("Expected empty BlockHex for build_failed entry, got %q", recovered.BlockHex)
	}

	assertRawComponentsIntact(t, "BTC/RV1", entry, &recovered)

	// Verify the raw components are sufficient for manual rebuild
	if recovered.CoinBase1 == "" {
		t.Error("BLOCK LOSS: CoinBase1 missing - cannot rebuild coinbase transaction")
	}
	if recovered.CoinBase2 == "" {
		t.Error("BLOCK LOSS: CoinBase2 missing - cannot rebuild coinbase transaction")
	}
	if recovered.ExtraNonce1 == "" {
		t.Error("BLOCK LOSS: ExtraNonce1 missing - cannot rebuild coinbase transaction")
	}
	if recovered.ExtraNonce2 == "" {
		t.Error("BLOCK LOSS: ExtraNonce2 missing - cannot rebuild coinbase transaction")
	}
	if recovered.Version == "" {
		t.Error("BLOCK LOSS: Version missing - cannot rebuild block header")
	}
	if recovered.NBits == "" {
		t.Error("BLOCK LOSS: NBits missing - cannot rebuild block header")
	}
	if recovered.NTime == "" {
		t.Error("BLOCK LOSS: NTime missing - cannot rebuild block header")
	}
	if recovered.Nonce == "" {
		t.Error("BLOCK LOSS: Nonce missing - cannot rebuild block header")
	}
}

// TestSOLO_BuildFailure_LTC_EmptyBlockHexRawComponentsIntact tests the same
// scenario for Scrypt-based LTC solo mining.
//
// Risk vector: #1 - BlockHex empty but raw components populated
// Coin: LTC, Block interval: 150s, Algorithm: Scrypt
func TestSOLO_BuildFailure_LTC_EmptyBlockHexRawComponentsIntact(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "LTC", Algorithm: "Scrypt", BlockInterval: 150,
		MinerAddress: "ltc1qsolominer", Height: 2800001,
		CoinbaseValue: 625000000, NBits: "1a01cc46",
	}
	entry := buildFailedEntry(cc, "rv1_ltc")

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL file has no entries after build_failed write")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if recovered.BlockHex != "" {
		t.Errorf("Expected empty BlockHex for build_failed entry, got %q", recovered.BlockHex)
	}
	assertRawComponentsIntact(t, "LTC/RV1", entry, &recovered)
}

// TestSOLO_BuildFailure_DOGE_EmptyBlockHexRawComponentsIntact tests an AuxPoW
// Scrypt coin. DOGE is merge-mined; buildFullBlock failure must still preserve
// all raw components.
//
// Risk vector: #1 - BlockHex empty but raw components populated
// Coin: DOGE, Block interval: 60s, Algorithm: Scrypt (AuxPoW)
func TestSOLO_BuildFailure_DOGE_EmptyBlockHexRawComponentsIntact(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "DOGE", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true,
		MinerAddress: "DSoloMinerAddr1", Height: 5200001,
		CoinbaseValue: 1000000000000, NBits: "1a063051",
	}
	entry := buildFailedEntry(cc, "rv1_doge")

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL file empty")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if recovered.BlockHex != "" {
		t.Errorf("Expected empty BlockHex, got %q", recovered.BlockHex)
	}
	assertRawComponentsIntact(t, "DOGE/RV1", entry, &recovered)
}

// =============================================================================
// RISK VECTOR 2: build_failed Status with Raw Components Preserved in WAL
// =============================================================================

// TestSOLO_BuildFailure_AllSHA256d_StatusAndComponentsPreserved writes a
// build_failed WAL entry for every SHA-256d coin and verifies that the status
// and all raw block components are faithfully persisted.
//
// Risk vector: #2 - build_failed status with raw components preserved
// Coins: BTC, BCH, DGB, BC2, NMC(aux), SYS(aux), XMY(aux), FBTC(aux)
// Algorithm: SHA-256d
func TestSOLO_BuildFailure_AllSHA256d_StatusAndComponentsPreserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	sha256dCoins := []coinConfig{
		{Coin: "BTC", Algorithm: "SHA-256d", BlockInterval: 600, MinerAddress: "bc1qsolominer", Height: 880001, CoinbaseValue: 312500000, NBits: "1703255b"},
		{Coin: "BCH", Algorithm: "SHA-256d", BlockInterval: 600, MinerAddress: "bitcoincash:qpsolominer", Height: 880002, CoinbaseValue: 312500000, NBits: "18034287"},
		{Coin: "DGB", Algorithm: "SHA-256d", BlockInterval: 15, MinerAddress: "dgb1qsolominer", Height: 20000001, CoinbaseValue: 27700000000, NBits: "1a0377ae"},
		{Coin: "BC2", Algorithm: "SHA-256d", BlockInterval: 600, MinerAddress: "bc2solo1qminer", Height: 100001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
		{Coin: "NMC", Algorithm: "SHA-256d", BlockInterval: 600, AuxPow: true, MinerAddress: "N1soloMiner", Height: 700001, CoinbaseValue: 5000000000, NBits: "1a06b4be"},
		{Coin: "SYS", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true, MinerAddress: "sys1qsolominer", Height: 1500001, CoinbaseValue: 606060606, NBits: "1c00d2d1"},
		{Coin: "XMY", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true, MinerAddress: "MsoloMiner1addr", Height: 3000001, CoinbaseValue: 250000000, NBits: "1c0d3142"},
		{Coin: "FBTC", Algorithm: "SHA-256d", BlockInterval: 30, AuxPow: true, MinerAddress: "FsoloMiner1addr", Height: 200001, CoinbaseValue: 2500000000, NBits: "1d00ffff"},
	}

	entries := make([]*BlockWALEntry, len(sha256dCoins))
	for i, cc := range sha256dCoins {
		entries[i] = buildFailedEntry(cc, "rv2")
		if err := wal.LogBlockFound(entries[i]); err != nil {
			t.Fatalf("LogBlockFound for %s failed: %v", cc.Coin, err)
		}
	}
	wal.Close()

	// Read back and verify each coin's entry
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	nonEmpty := 0
	for _, line := range lines {
		if len(line) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty != len(sha256dCoins) {
		t.Fatalf("Expected %d WAL entries, got %d", len(sha256dCoins), nonEmpty)
	}

	lineIdx := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var recovered BlockWALEntry
		if err := json.Unmarshal(line, &recovered); err != nil {
			t.Fatalf("JSON unmarshal failed at line %d: %v", lineIdx, err)
		}

		original := entries[lineIdx]
		label := fmt.Sprintf("%s/RV2", sha256dCoins[lineIdx].Coin)

		if recovered.Status != "build_failed" {
			t.Errorf("[%s] Expected status 'build_failed', got %q", label, recovered.Status)
		}
		assertRawComponentsIntact(t, label, original, &recovered)
		lineIdx++
	}
}

// TestSOLO_BuildFailure_AllScrypt_StatusAndComponentsPreserved writes a
// build_failed WAL entry for every Scrypt coin and verifies persistence.
//
// Risk vector: #2 - build_failed status with raw components preserved
// Coins: LTC, DOGE(aux), DGB-SCRYPT, PEP(aux), CAT
// Algorithm: Scrypt
func TestSOLO_BuildFailure_AllScrypt_StatusAndComponentsPreserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	scryptCoins := []coinConfig{
		{Coin: "LTC", Algorithm: "Scrypt", BlockInterval: 150, MinerAddress: "ltc1qsolominer", Height: 2800001, CoinbaseValue: 625000000, NBits: "1a01cc46"},
		{Coin: "DOGE", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true, MinerAddress: "DSoloMinerAddr1", Height: 5200001, CoinbaseValue: 1000000000000, NBits: "1a063051"},
		{Coin: "DGB-SCRYPT", Algorithm: "Scrypt", BlockInterval: 15, MinerAddress: "dgb1qscryptminer", Height: 20000002, CoinbaseValue: 27700000000, NBits: "1a0377ae"},
		{Coin: "PEP", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true, MinerAddress: "PsoloMiner1addr", Height: 400001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
		{Coin: "CAT", Algorithm: "Scrypt", BlockInterval: 600, MinerAddress: "9soloMinerCATaddr", Height: 300001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
	}

	entries := make([]*BlockWALEntry, len(scryptCoins))
	for i, cc := range scryptCoins {
		entries[i] = buildFailedEntry(cc, "rv2")
		if err := wal.LogBlockFound(entries[i]); err != nil {
			t.Fatalf("LogBlockFound for %s failed: %v", cc.Coin, err)
		}
	}
	wal.Close()

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	lineIdx := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var recovered BlockWALEntry
		if err := json.Unmarshal(line, &recovered); err != nil {
			t.Fatalf("JSON unmarshal failed at line %d: %v", lineIdx, err)
		}

		original := entries[lineIdx]
		label := fmt.Sprintf("%s/RV2", scryptCoins[lineIdx].Coin)

		if recovered.Status != "build_failed" {
			t.Errorf("[%s] Expected status 'build_failed', got %q", label, recovered.Status)
		}
		assertRawComponentsIntact(t, label, original, &recovered)
		lineIdx++
	}
}

// =============================================================================
// RISK VECTOR 3: Recovery Shows build_failed Entries with Raw Components Intact
// =============================================================================

// TestSOLO_BuildFailure_BTC_RecoveryShowsBuildFailedWithComponents verifies that
// after WAL close and reopen, build_failed entries can be recovered and their
// raw components are intact for manual reconstruction.
//
// Risk vector: #3 - Recovery shows build_failed entries with raw components
// Coin: BTC, Block interval: 600s, Algorithm: SHA-256d
func TestSOLO_BuildFailure_BTC_RecoveryShowsBuildFailedWithComponents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	// Phase 1: Write build_failed entry and close
	wal1, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL (phase 1) failed: %v", err)
	}

	cc := coinConfig{
		Coin: "BTC", Algorithm: "SHA-256d", BlockInterval: 600,
		MinerAddress: "bc1qsolominer", Height: 880099,
		CoinbaseValue: 312500000, NBits: "1703255b",
	}
	original := buildFailedEntry(cc, "rv3_btc")

	if err := wal1.LogBlockFound(original); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close (phase 1) failed: %v", err)
	}

	// Phase 2: Reopen a new WAL (simulating restart) and read all entries
	wal2, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL (phase 2) failed: %v", err)
	}
	defer wal2.Close()

	// RecoverUnsubmittedBlocks does NOT return build_failed entries
	// (they need manual operator intervention). Verify this.
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == original.BlockHash {
			t.Error("build_failed entry should NOT appear in RecoverUnsubmittedBlocks")
		}
	}

	// But the build_failed entry IS in the WAL file and can be found by
	// scanning for build_failed status (operator tooling).
	buildFailed := recoverAllBuildFailed(t, tmpDir)
	found := false
	for _, entry := range buildFailed {
		if entry.BlockHash == original.BlockHash {
			found = true
			assertRawComponentsIntact(t, "BTC/RV3/recovery", original, &entry)
		}
	}
	if !found {
		t.Error("BLOCK LOSS: build_failed entry not found after WAL reopen")
	}
}

// TestSOLO_BuildFailure_DGB_RecoveryShowsBuildFailedWithComponents tests
// recovery for DGB, which has a short 15s block interval making timely
// recovery critical.
//
// Risk vector: #3 - Recovery shows build_failed entries with raw components
// Coin: DGB, Block interval: 15s, Algorithm: SHA-256d
func TestSOLO_BuildFailure_DGB_RecoveryShowsBuildFailedWithComponents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "DGB", Algorithm: "SHA-256d", BlockInterval: 15,
		MinerAddress: "dgb1qsolominer", Height: 20000099,
		CoinbaseValue: 27700000000, NBits: "1a0377ae",
	}
	original := buildFailedEntry(cc, "rv3_dgb")

	if err := wal.LogBlockFound(original); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	buildFailed := recoverAllBuildFailed(t, tmpDir)
	found := false
	for _, entry := range buildFailed {
		if entry.BlockHash == original.BlockHash {
			found = true
			assertRawComponentsIntact(t, "DGB/RV3/recovery", original, &entry)
		}
	}
	if !found {
		t.Error("BLOCK LOSS: DGB build_failed entry not recoverable from WAL")
	}
}

// =============================================================================
// RISK VECTOR 4: Multiple Build Failures Across Different Coins
// =============================================================================

// TestSOLO_BuildFailure_AllCoins_MultipleBuildFailuresPreserved writes build_failed
// WAL entries for all 13 coins in a single WAL and verifies that every single
// entry's raw components survive the roundtrip. This is the most comprehensive
// test: if a miner running multiple coins has buildFullBlock fail for several
// coins simultaneously, no solved block is lost.
//
// Risk vector: #4 - Multiple build failures across different coins
// Coins: All 13 (BTC, BCH, DGB, BC2, NMC, SYS, XMY, FBTC,
//         LTC, DOGE, DGB-SCRYPT, PEP, CAT)
// Algorithms: SHA-256d, Scrypt
func TestSOLO_BuildFailure_AllCoins_MultipleBuildFailuresPreserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	coins := allCoins()
	entries := make([]*BlockWALEntry, len(coins))

	for i, cc := range coins {
		entries[i] = buildFailedEntry(cc, "rv4_all")
		if err := wal.LogBlockFound(entries[i]); err != nil {
			t.Fatalf("LogBlockFound for %s failed: %v", cc.Coin, err)
		}
	}
	wal.Close()

	// Recover and verify every coin's entry
	buildFailed := recoverAllBuildFailed(t, tmpDir)

	if len(buildFailed) != len(coins) {
		t.Fatalf("Expected %d build_failed entries, got %d (BLOCK LOSS for %d coins)",
			len(coins), len(buildFailed), len(coins)-len(buildFailed))
	}

	// Index recovered entries by block hash for lookup
	recoveredByHash := make(map[string]*BlockWALEntry)
	for i := range buildFailed {
		recoveredByHash[buildFailed[i].BlockHash] = &buildFailed[i]
	}

	for i, cc := range coins {
		original := entries[i]
		recovered, ok := recoveredByHash[original.BlockHash]
		if !ok {
			t.Errorf("BLOCK LOSS: %s entry not found in recovered build_failed entries", cc.Coin)
			continue
		}
		label := fmt.Sprintf("%s[%s,%ds]/RV4", cc.Coin, cc.Algorithm, cc.BlockInterval)
		assertRawComponentsIntact(t, label, original, recovered)
	}
}

// TestSOLO_BuildFailure_AuxPowCoins_MultipleBuildFailuresPreserved specifically
// tests all AuxPoW (merge-mined) coins where build failures are especially
// costly because the parent chain block may also be lost.
//
// Risk vector: #4 - Multiple build failures across AuxPoW coins
// Coins: NMC(aux), SYS(aux), XMY(aux), FBTC(aux), DOGE(aux), PEP(aux)
// Algorithms: SHA-256d, Scrypt
func TestSOLO_BuildFailure_AuxPowCoins_MultipleBuildFailuresPreserved(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	auxCoins := []coinConfig{
		{Coin: "NMC", Algorithm: "SHA-256d", BlockInterval: 600, AuxPow: true, MinerAddress: "N1soloMiner", Height: 700001, CoinbaseValue: 5000000000, NBits: "1a06b4be"},
		{Coin: "SYS", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true, MinerAddress: "sys1qsolominer", Height: 1500001, CoinbaseValue: 606060606, NBits: "1c00d2d1"},
		{Coin: "XMY", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true, MinerAddress: "MsoloMiner1addr", Height: 3000001, CoinbaseValue: 250000000, NBits: "1c0d3142"},
		{Coin: "FBTC", Algorithm: "SHA-256d", BlockInterval: 30, AuxPow: true, MinerAddress: "FsoloMiner1addr", Height: 200001, CoinbaseValue: 2500000000, NBits: "1d00ffff"},
		{Coin: "DOGE", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true, MinerAddress: "DSoloMinerAddr1", Height: 5200001, CoinbaseValue: 1000000000000, NBits: "1a063051"},
		{Coin: "PEP", Algorithm: "Scrypt", BlockInterval: 60, AuxPow: true, MinerAddress: "PsoloMiner1addr", Height: 400001, CoinbaseValue: 5000000000, NBits: "1d00ffff"},
	}

	entries := make([]*BlockWALEntry, len(auxCoins))
	for i, cc := range auxCoins {
		entries[i] = buildFailedEntry(cc, "rv4_aux")
		if err := wal.LogBlockFound(entries[i]); err != nil {
			t.Fatalf("LogBlockFound for %s(aux) failed: %v", cc.Coin, err)
		}
	}
	wal.Close()

	buildFailed := recoverAllBuildFailed(t, tmpDir)
	if len(buildFailed) != len(auxCoins) {
		t.Fatalf("Expected %d AuxPoW build_failed entries, got %d", len(auxCoins), len(buildFailed))
	}

	recoveredByHash := make(map[string]*BlockWALEntry)
	for i := range buildFailed {
		recoveredByHash[buildFailed[i].BlockHash] = &buildFailed[i]
	}

	for i, cc := range auxCoins {
		original := entries[i]
		recovered, ok := recoveredByHash[original.BlockHash]
		if !ok {
			t.Errorf("BLOCK LOSS: %s(aux) entry not found in recovery", cc.Coin)
			continue
		}
		label := fmt.Sprintf("%s[aux,%s,%ds]/RV4", cc.Coin, cc.Algorithm, cc.BlockInterval)
		assertRawComponentsIntact(t, label, original, recovered)
	}
}

// =============================================================================
// RISK VECTOR 5: Partial Block Data - Some Raw Components Missing
// =============================================================================

// TestSOLO_BuildFailure_BTC_PartialComponents_VerifyWhichFieldsPresent tests the
// scenario where buildFullBlock fails but only some raw components were captured
// before the failure. The WAL must faithfully record whichever fields are present
// and leave the rest empty, so an operator can assess what's available.
//
// Risk vector: #5 - Partial block data, verify which fields present
// Coin: BTC, Block interval: 600s, Algorithm: SHA-256d
func TestSOLO_BuildFailure_BTC_PartialComponents_VerifyWhichFieldsPresent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	// Simulate partial failure: CoinBase1 and ExtraNonce1 captured, but
	// CoinBase2, ExtraNonce2, and TransactionData are missing.
	entry := &BlockWALEntry{
		Height:        880050,
		BlockHash:     "000000000000000000rv5_BTC_partial_components_test_hash_00000000",
		PrevHash:      "000000000000000000rv5_BTC_partial_prev_hash_test_000000000000",
		BlockHex:      "", // buildFullBlock failed
		MinerAddress:  "bc1qsolominer",
		WorkerName:    "btc_rig1",
		JobID:         "job_BTC_rv5_partial",
		CoinbaseValue: 312500000,
		Status:        "build_failed",
		SubmitError:   "buildFullBlock failed: coinbase2 unavailable",

		// Only partial components available
		CoinBase1:   "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403BTC00",
		CoinBase2:   "",         // Missing
		ExtraNonce1: "aabbccdd",
		ExtraNonce2: "",         // Missing
		Version:     "20000000",
		NBits:       "1703255b",
		NTime:       "67a2b000",
		Nonce:       "deadbeef",
		// TransactionData intentionally omitted (nil)
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL has no entries")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Verify present fields are intact
	if recovered.CoinBase1 != entry.CoinBase1 {
		t.Errorf("CoinBase1 (present) mismatch: got %q, want %q", recovered.CoinBase1, entry.CoinBase1)
	}
	if recovered.ExtraNonce1 != entry.ExtraNonce1 {
		t.Errorf("ExtraNonce1 (present) mismatch: got %q, want %q", recovered.ExtraNonce1, entry.ExtraNonce1)
	}
	if recovered.Version != entry.Version {
		t.Errorf("Version (present) mismatch: got %q, want %q", recovered.Version, entry.Version)
	}
	if recovered.NBits != entry.NBits {
		t.Errorf("NBits (present) mismatch: got %q, want %q", recovered.NBits, entry.NBits)
	}
	if recovered.NTime != entry.NTime {
		t.Errorf("NTime (present) mismatch: got %q, want %q", recovered.NTime, entry.NTime)
	}
	if recovered.Nonce != entry.Nonce {
		t.Errorf("Nonce (present) mismatch: got %q, want %q", recovered.Nonce, entry.Nonce)
	}

	// Verify missing fields are faithfully empty
	if recovered.CoinBase2 != "" {
		t.Errorf("CoinBase2 should be empty (missing), got %q", recovered.CoinBase2)
	}
	if recovered.ExtraNonce2 != "" {
		t.Errorf("ExtraNonce2 should be empty (missing), got %q", recovered.ExtraNonce2)
	}
	if len(recovered.TransactionData) != 0 {
		t.Errorf("TransactionData should be nil/empty (missing), got %d entries", len(recovered.TransactionData))
	}

	// Verify metadata
	if recovered.Status != "build_failed" {
		t.Errorf("Status mismatch: got %q, want %q", recovered.Status, "build_failed")
	}
	if recovered.SubmitError != entry.SubmitError {
		t.Errorf("SubmitError mismatch: got %q, want %q", recovered.SubmitError, entry.SubmitError)
	}
}

// TestSOLO_BuildFailure_LTC_PartialComponents_OnlyHeaderFields tests partial
// data for LTC where only header fields (Version, NBits, NTime, Nonce) were
// captured but no coinbase components.
//
// Risk vector: #5 - Partial block data, verify which fields present
// Coin: LTC, Block interval: 150s, Algorithm: Scrypt
func TestSOLO_BuildFailure_LTC_PartialComponents_OnlyHeaderFields(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	entry := &BlockWALEntry{
		Height:        2800050,
		BlockHash:     "000000000000000000rv5_LTC_partial_header_only_test_hash_0000",
		PrevHash:      "000000000000000000rv5_LTC_partial_header_only_prev_hash_000",
		BlockHex:      "",
		MinerAddress:  "ltc1qsolominer",
		WorkerName:    "ltc_rig1",
		JobID:         "job_LTC_rv5_header_only",
		CoinbaseValue: 625000000,
		Status:        "build_failed",
		SubmitError:   "buildFullBlock failed: coinbase components not available",

		// Only header fields - no coinbase data
		CoinBase1:   "",
		CoinBase2:   "",
		ExtraNonce1: "",
		ExtraNonce2: "",
		Version:     "20000000",
		NBits:       "1a01cc46",
		NTime:       "67a2b100",
		Nonce:       "cafebabe",
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL has no entries")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Header fields present
	if recovered.Version != "20000000" {
		t.Errorf("Version should be preserved: got %q", recovered.Version)
	}
	if recovered.NBits != "1a01cc46" {
		t.Errorf("NBits should be preserved: got %q", recovered.NBits)
	}
	if recovered.NTime != "67a2b100" {
		t.Errorf("NTime should be preserved: got %q", recovered.NTime)
	}
	if recovered.Nonce != "cafebabe" {
		t.Errorf("Nonce should be preserved: got %q", recovered.Nonce)
	}

	// Coinbase fields absent
	if recovered.CoinBase1 != "" {
		t.Errorf("CoinBase1 should be empty: got %q", recovered.CoinBase1)
	}
	if recovered.CoinBase2 != "" {
		t.Errorf("CoinBase2 should be empty: got %q", recovered.CoinBase2)
	}
	if recovered.ExtraNonce1 != "" {
		t.Errorf("ExtraNonce1 should be empty: got %q", recovered.ExtraNonce1)
	}
	if recovered.ExtraNonce2 != "" {
		t.Errorf("ExtraNonce2 should be empty: got %q", recovered.ExtraNonce2)
	}

	// An operator can at least see what's available
	if recovered.Status != "build_failed" {
		t.Errorf("Status: got %q, want 'build_failed'", recovered.Status)
	}
}

// TestSOLO_BuildFailure_NMC_PartialComponents_CoinbaseOnlyNoHeader tests
// partial data for NMC (AuxPoW) where coinbase was captured but header
// nonce was not (e.g., failure occurred before nonce was applied).
//
// Risk vector: #5 - Partial block data, verify which fields present
// Coin: NMC, Block interval: 600s, Algorithm: SHA-256d (AuxPoW)
func TestSOLO_BuildFailure_NMC_PartialComponents_CoinbaseOnlyNoHeader(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	entry := &BlockWALEntry{
		Height:        700050,
		BlockHash:     "000000000000000000rv5_NMC_partial_coinbase_only_hash_00000",
		PrevHash:      "000000000000000000rv5_NMC_partial_coinbase_only_prev_00000",
		BlockHex:      "",
		MinerAddress:  "N1soloMiner",
		WorkerName:    "nmc_rig1",
		JobID:         "job_NMC_rv5_cb_only",
		CoinbaseValue: 5000000000,
		Status:        "build_failed",
		SubmitError:   "buildFullBlock failed: nonce not applied before failure",

		// Coinbase present, header nonce missing
		CoinBase1:   "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff0403NMC00",
		CoinBase2:   "ffffffff0100f2052a010000001976a914000000000000000000000000000000000000000088ac00000000",
		ExtraNonce1: "aabbccdd",
		ExtraNonce2: "11223344",
		Version:     "", // Missing
		NBits:       "", // Missing
		NTime:       "", // Missing
		Nonce:       "", // Missing - the critical piece
		TransactionData: []string{
			"0100000001NMC_tx1_data_for_manual_reconstruction",
		},
	}

	if err := wal.LogBlockFound(entry); err != nil {
		t.Fatalf("LogBlockFound failed: %v", err)
	}
	wal.Close()

	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL has no entries")
	}

	var recovered BlockWALEntry
	if err := json.Unmarshal(lines[0], &recovered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Coinbase fields present
	if recovered.CoinBase1 != entry.CoinBase1 {
		t.Errorf("CoinBase1 should be preserved: got %q, want %q", recovered.CoinBase1, entry.CoinBase1)
	}
	if recovered.CoinBase2 != entry.CoinBase2 {
		t.Errorf("CoinBase2 should be preserved: got %q, want %q", recovered.CoinBase2, entry.CoinBase2)
	}
	if recovered.ExtraNonce1 != entry.ExtraNonce1 {
		t.Errorf("ExtraNonce1 should be preserved: got %q", recovered.ExtraNonce1)
	}
	if recovered.ExtraNonce2 != entry.ExtraNonce2 {
		t.Errorf("ExtraNonce2 should be preserved: got %q", recovered.ExtraNonce2)
	}
	if len(recovered.TransactionData) != 1 {
		t.Errorf("TransactionData should have 1 entry, got %d", len(recovered.TransactionData))
	}

	// Header fields absent
	if recovered.Version != "" {
		t.Errorf("Version should be empty: got %q", recovered.Version)
	}
	if recovered.NBits != "" {
		t.Errorf("NBits should be empty: got %q", recovered.NBits)
	}
	if recovered.NTime != "" {
		t.Errorf("NTime should be empty: got %q", recovered.NTime)
	}
	if recovered.Nonce != "" {
		t.Errorf("Nonce should be empty: got %q", recovered.Nonce)
	}
}

// =============================================================================
// RISK VECTOR 6: build_failed Followed by Successful Rebuild from WAL Components
// =============================================================================

// TestSOLO_BuildFailure_BTC_RebuildFromWALComponents simulates the full
// lifecycle: buildFullBlock fails, WAL captures raw components, operator reads
// WAL, reconstructs block, and updates WAL status to "submitted". The block
// must not be flagged as lost after the status update.
//
// Risk vector: #6 - build_failed followed by successful rebuild
// Coin: BTC, Block interval: 600s, Algorithm: SHA-256d
func TestSOLO_BuildFailure_BTC_RebuildFromWALComponents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "BTC", Algorithm: "SHA-256d", BlockInterval: 600,
		MinerAddress: "bc1qsolominer", Height: 880200,
		CoinbaseValue: 312500000, NBits: "1703255b",
	}
	original := buildFailedEntry(cc, "rv6_btc")

	// Phase 1: Log build_failed entry
	if err := wal.LogBlockFound(original); err != nil {
		t.Fatalf("LogBlockFound (build_failed) failed: %v", err)
	}

	// Phase 2: Simulate operator manual reconstruction.
	// The operator reads the WAL, extracts raw components, rebuilds the block
	// using: header + varint(1+len(TxData)) + coinbase1+en1+en2+coinbase2 + txdata
	// Then submits via bitcoin-cli submitblock <hex>
	rebuiltHex := "020000" + original.Version + original.PrevHash +
		"merkle_root_placeholder" + original.NTime + original.NBits + original.Nonce +
		"coinbase_and_tx_data_placeholder"

	// Phase 3: Log successful submission after manual rebuild
	submitEntry := &BlockWALEntry{
		Height:    original.Height,
		BlockHash: original.BlockHash,
		BlockHex:  rebuiltHex,
		Status:    "submitted",
	}
	if err := wal.LogSubmissionResult(submitEntry); err != nil {
		t.Fatalf("LogSubmissionResult (manual rebuild) failed: %v", err)
	}
	wal.Close()

	// Phase 4: Verify recovery does NOT flag this block
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == original.BlockHash {
			t.Errorf("Block should NOT be in unsubmitted list after manual rebuild and submission (status=%s)", entry.Status)
		}
	}

	// Phase 5: Verify the build_failed entry's raw components are still in the WAL
	// for audit trail purposes (even though latest status is "submitted")
	allEntries, err := recoverAllEntries(t, tmpDir)
	if err != nil {
		t.Fatalf("recoverAllEntries failed: %v", err)
	}

	foundBuildFailed := false
	foundSubmitted := false
	for _, entry := range allEntries {
		if entry.BlockHash == original.BlockHash {
			if entry.Status == "build_failed" {
				foundBuildFailed = true
				// Original raw components should still be in this entry
				if entry.CoinBase1 != original.CoinBase1 {
					t.Error("build_failed entry CoinBase1 corrupted in audit trail")
				}
			}
			if entry.Status == "submitted" {
				foundSubmitted = true
			}
		}
	}
	if !foundBuildFailed {
		t.Error("build_failed entry missing from WAL audit trail")
	}
	if !foundSubmitted {
		t.Error("submitted entry missing from WAL audit trail")
	}
}

// TestSOLO_BuildFailure_BCH_RebuildFromWALComponents tests the rebuild lifecycle
// for BCH.
//
// Risk vector: #6 - build_failed followed by successful rebuild
// Coin: BCH, Block interval: 600s, Algorithm: SHA-256d
func TestSOLO_BuildFailure_BCH_RebuildFromWALComponents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "BCH", Algorithm: "SHA-256d", BlockInterval: 600,
		MinerAddress: "bitcoincash:qpsolominer", Height: 880200,
		CoinbaseValue: 312500000, NBits: "18034287",
	}
	original := buildFailedEntry(cc, "rv6_bch")

	if err := wal.LogBlockFound(original); err != nil {
		t.Fatalf("LogBlockFound (build_failed) failed: %v", err)
	}

	// Simulate successful manual rebuild
	original.Status = "submitted"
	original.SubmitError = ""
	original.BlockHex = "rebuilt_bch_block_hex_data"
	if err := wal.LogSubmissionResult(original); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}
	wal.Close()

	// Block should NOT appear in unsubmitted
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == original.BlockHash {
			t.Error("BCH block should not be in unsubmitted list after rebuild")
		}
	}
}

// TestSOLO_BuildFailure_SYS_RebuildFromWALComponents tests the rebuild lifecycle
// for SYS (AuxPoW, 60s interval).
//
// Risk vector: #6 - build_failed followed by successful rebuild
// Coin: SYS, Block interval: 60s, Algorithm: SHA-256d (AuxPoW)
func TestSOLO_BuildFailure_SYS_RebuildFromWALComponents(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	cc := coinConfig{
		Coin: "SYS", Algorithm: "SHA-256d", BlockInterval: 60, AuxPow: true,
		MinerAddress: "sys1qsolominer", Height: 1500200,
		CoinbaseValue: 606060606, NBits: "1c00d2d1",
	}
	original := buildFailedEntry(cc, "rv6_sys")

	if err := wal.LogBlockFound(original); err != nil {
		t.Fatalf("LogBlockFound (build_failed) failed: %v", err)
	}

	// Read back the build_failed entry to verify components before rebuild
	data, err := os.ReadFile(wal.FilePath())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	lines := splitLines(data)
	if len(lines) == 0 {
		t.Fatal("BLOCK LOSS: WAL has no entries")
	}
	var preRebuild BlockWALEntry
	if err := json.Unmarshal(lines[0], &preRebuild); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}
	if preRebuild.CoinBase1 == "" || preRebuild.Nonce == "" {
		t.Error("Pre-rebuild entry missing critical raw components")
	}

	// Simulate rebuild and submission
	rebuildEntry := &BlockWALEntry{
		Height:    original.Height,
		BlockHash: original.BlockHash,
		BlockHex:  "rebuilt_sys_auxpow_block",
		Status:    "submitted",
	}
	if err := wal.LogSubmissionResult(rebuildEntry); err != nil {
		t.Fatalf("LogSubmissionResult failed: %v", err)
	}
	wal.Close()

	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	for _, entry := range unsubmitted {
		if entry.BlockHash == original.BlockHash {
			t.Error("SYS block should not be in unsubmitted list after rebuild")
		}
	}
}

// =============================================================================
// COMBINED: All 13 Coins End-to-End Build Failure and Recovery
// =============================================================================

// TestSOLO_BuildFailure_All13Coins_EndToEndRecovery is the comprehensive test
// that writes build_failed entries for all 13 coins, closes the WAL, reopens
// it, and verifies every coin's raw components are intact for manual
// reconstruction. This is the "no block left behind" test.
//
// Risk vectors: #1 through #4 combined
// Coins: All 13 (SHA-256d + Scrypt, with and without AuxPoW)
func TestSOLO_BuildFailure_All13Coins_EndToEndRecovery(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	// Phase 1: Write all build_failed entries
	wal1, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL (write phase) failed: %v", err)
	}

	coins := allCoins()
	entries := make([]*BlockWALEntry, len(coins))
	for i, cc := range coins {
		entries[i] = buildFailedEntry(cc, "e2e")
		if err := wal1.LogBlockFound(entries[i]); err != nil {
			t.Fatalf("LogBlockFound for %s failed: %v", cc.Coin, err)
		}
	}
	if err := wal1.Close(); err != nil {
		t.Fatalf("Close (write phase) failed: %v", err)
	}

	// Phase 2: Reopen WAL (simulating process restart)
	wal2, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL (recovery phase) failed: %v", err)
	}
	defer wal2.Close()

	// Phase 3: Recover build_failed entries
	buildFailed := recoverAllBuildFailed(t, tmpDir)
	if len(buildFailed) != len(coins) {
		// This is a CRITICAL failure: blocks are being lost
		missing := len(coins) - len(buildFailed)
		t.Fatalf("BLOCK LOSS: Expected %d build_failed entries, got %d (%d coins' blocks LOST)",
			len(coins), len(buildFailed), missing)
	}

	// Phase 4: Verify every coin's raw components
	recoveredByHash := make(map[string]*BlockWALEntry)
	for i := range buildFailed {
		recoveredByHash[buildFailed[i].BlockHash] = &buildFailed[i]
	}

	for i, cc := range coins {
		original := entries[i]
		recovered, ok := recoveredByHash[original.BlockHash]
		if !ok {
			t.Errorf("BLOCK LOSS: %s (height=%d) entry not found after recovery", cc.Coin, cc.Height)
			continue
		}

		auxLabel := ""
		if cc.AuxPow {
			auxLabel = ",aux"
		}
		label := fmt.Sprintf("%s[%s,%ds%s]/E2E", cc.Coin, cc.Algorithm, cc.BlockInterval, auxLabel)

		assertRawComponentsIntact(t, label, original, recovered)

		// Additional per-coin sanity checks
		if recovered.MinerAddress != cc.MinerAddress {
			t.Errorf("[%s] MinerAddress mismatch: got %q, want %q", label, recovered.MinerAddress, cc.MinerAddress)
		}
		if recovered.CoinbaseValue != cc.CoinbaseValue {
			t.Errorf("[%s] CoinbaseValue mismatch: got %d, want %d", label, recovered.CoinbaseValue, cc.CoinbaseValue)
		}
		if recovered.NBits != cc.NBits {
			t.Errorf("[%s] NBits mismatch: got %q, want %q", label, recovered.NBits, cc.NBits)
		}
	}

	// Phase 5: Verify RecoverUnsubmittedBlocks does NOT return build_failed entries
	unsubmitted, err := RecoverUnsubmittedBlocks(tmpDir)
	if err != nil {
		t.Fatalf("RecoverUnsubmittedBlocks failed: %v", err)
	}
	if len(unsubmitted) != 0 {
		t.Errorf("RecoverUnsubmittedBlocks should return 0 for build_failed-only WAL, got %d", len(unsubmitted))
	}
}

// TestSOLO_BuildFailure_FastBlockCoins_TimelinessOfRawCapture verifies that
// coins with short block intervals (DGB 15s, DGB-SCRYPT 15s, FBTC 30s)
// have their raw components captured identically to slower coins. With short
// intervals, there is less time for manual intervention, making the completeness
// of raw component capture even more critical.
//
// Risk vectors: #1, #2 - emphasis on fast-block coins
// Coins: DGB(15s), DGB-SCRYPT(15s), FBTC(30s,aux)
// Algorithms: SHA-256d, Scrypt
func TestSOLO_BuildFailure_FastBlockCoins_TimelinessOfRawCapture(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	logger := zap.NewNop()

	wal, err := NewBlockWAL(tmpDir, logger)
	if err != nil {
		t.Fatalf("NewBlockWAL failed: %v", err)
	}

	fastCoins := []coinConfig{
		{Coin: "DGB", Algorithm: "SHA-256d", BlockInterval: 15, MinerAddress: "dgb1qsolominer", Height: 20000001, CoinbaseValue: 27700000000, NBits: "1a0377ae"},
		{Coin: "DGB-SCRYPT", Algorithm: "Scrypt", BlockInterval: 15, MinerAddress: "dgb1qscryptminer", Height: 20000002, CoinbaseValue: 27700000000, NBits: "1a0377ae"},
		{Coin: "FBTC", Algorithm: "SHA-256d", BlockInterval: 30, AuxPow: true, MinerAddress: "FsoloMiner1addr", Height: 200001, CoinbaseValue: 2500000000, NBits: "1d00ffff"},
	}

	entries := make([]*BlockWALEntry, len(fastCoins))
	for i, cc := range fastCoins {
		entries[i] = buildFailedEntry(cc, "fast")
		if err := wal.LogBlockFound(entries[i]); err != nil {
			t.Fatalf("LogBlockFound for %s failed: %v", cc.Coin, err)
		}
	}
	wal.Close()

	buildFailed := recoverAllBuildFailed(t, tmpDir)
	if len(buildFailed) != len(fastCoins) {
		t.Fatalf("Expected %d fast-block entries, got %d", len(fastCoins), len(buildFailed))
	}

	recoveredByHash := make(map[string]*BlockWALEntry)
	for i := range buildFailed {
		recoveredByHash[buildFailed[i].BlockHash] = &buildFailed[i]
	}

	for i, cc := range fastCoins {
		original := entries[i]
		recovered, ok := recoveredByHash[original.BlockHash]
		if !ok {
			t.Errorf("BLOCK LOSS: Fast-block coin %s (%ds interval) entry lost!", cc.Coin, cc.BlockInterval)
			continue
		}

		label := fmt.Sprintf("%s[%s,%ds]/FastBlock", cc.Coin, cc.Algorithm, cc.BlockInterval)
		assertRawComponentsIntact(t, label, original, recovered)

		// For fast-block coins, verify ALL reconstruction components are present
		// (not just some) since the operator has very little time to act
		if recovered.CoinBase1 == "" || recovered.CoinBase2 == "" ||
			recovered.ExtraNonce1 == "" || recovered.ExtraNonce2 == "" ||
			recovered.Version == "" || recovered.NBits == "" ||
			recovered.NTime == "" || recovered.Nonce == "" {
			t.Errorf("[%s] INCOMPLETE: Fast-block coin missing raw components - operator cannot rebuild in time", label)
		}
		if len(recovered.TransactionData) == 0 {
			t.Errorf("[%s] INCOMPLETE: Fast-block coin missing TransactionData", label)
		}
	}
}
