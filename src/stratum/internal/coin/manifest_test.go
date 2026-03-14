// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Manifest validation tests
package coin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findManifestPath finds the manifest file for testing
func findManifestPath(t *testing.T) string {
	// Try various relative paths from test location
	paths := []string{
		"../../../../config/coins.manifest.yaml",
		"../../../config/coins.manifest.yaml",
		"../../config/coins.manifest.yaml",
		"../config/coins.manifest.yaml",
		"./config/coins.manifest.yaml",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	// Check environment variable
	if p := os.Getenv("SPIRALPOOL_MANIFEST_PATH"); p != "" {
		return p
	}

	t.Skip("Manifest file not found - skipping manifest tests")
	return ""
}

func TestManifestSchemaVersion(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	if manifest.SchemaVersion != "1.0" {
		t.Errorf("Expected schema version 1.0, got %s", manifest.SchemaVersion)
	}
}

func TestManifestHasAllRegisteredCoins(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	// Every coin in Go registry should be in manifest
	registeredCoins := ListRegistered()

	// Known aliases that are registered but not primary symbols in manifest
	// These map to primary symbols: FB->FBTC, MEME->PEP
	knownAliases := map[string]bool{
		"FB":   true, // Alias for FBTC (Fractal Bitcoin)
		"MEME": true, // Alias for PEP (PepeCoin)
	}

	for _, symbol := range registeredCoins {
		// Skip aliases (BITCOIN -> BTC, etc.) - longer names are typically aliases
		if len(symbol) > 4 {
			continue
		}
		// Skip known short aliases
		if knownAliases[symbol] {
			continue
		}

		found := false
		for _, def := range manifest.Coins {
			if strings.EqualFold(def.Symbol, symbol) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Coin %s is in Go registry but not in manifest", symbol)
		}
	}
}

func TestManifestCoinsExistInRegistry(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	// Every coin in manifest should be in Go registry
	for _, def := range manifest.Coins {
		if !IsRegistered(def.Symbol) {
			t.Errorf("Coin %s is in manifest but not in Go registry", def.Symbol)
		}
	}
}

func TestManifestRequiredFields(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	for _, def := range manifest.Coins {
		// Required fields
		if def.Symbol == "" {
			t.Error("Coin missing symbol")
		}
		if def.Name == "" {
			t.Errorf("Coin %s missing name", def.Symbol)
		}
		if def.Algorithm == "" {
			t.Errorf("Coin %s missing algorithm", def.Symbol)
		}
		if def.Algorithm != "sha256d" && def.Algorithm != "scrypt" {
			t.Errorf("Coin %s has invalid algorithm: %s (must be sha256d or scrypt)", def.Symbol, def.Algorithm)
		}
		if def.Role == "" {
			t.Errorf("Coin %s missing role", def.Symbol)
		}
		if def.Role != "parent" && def.Role != "aux" && def.Role != "standalone" {
			t.Errorf("Coin %s has invalid role: %s (must be parent, aux, or standalone)", def.Symbol, def.Role)
		}
		if def.Chain.GenesisHash == "" {
			t.Errorf("Coin %s missing genesis_hash", def.Symbol)
		}
		if def.Chain.BlockTime <= 0 {
			t.Errorf("Coin %s has invalid block_time: %d", def.Symbol, def.Chain.BlockTime)
		}
	}
}

func TestManifestAlgorithmMatchesGo(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	for _, def := range manifest.Coins {
		goCoin, err := Create(def.Symbol)
		if err != nil {
			t.Errorf("Coin %s: failed to create from Go registry: %v", def.Symbol, err)
			continue
		}

		if def.Algorithm != goCoin.Algorithm() {
			t.Errorf("Coin %s: algorithm mismatch - manifest=%s, go=%s",
				def.Symbol, def.Algorithm, goCoin.Algorithm())
		}
	}
}

func TestManifestVersionBytesMatchGo(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	for _, def := range manifest.Coins {
		goCoin, err := Create(def.Symbol)
		if err != nil {
			continue // Already tested in other test
		}

		if def.Address.P2PKHVersion != uint8(goCoin.P2PKHVersionByte()) {
			t.Errorf("Coin %s: P2PKH version mismatch - manifest=0x%02x, go=0x%02x",
				def.Symbol, def.Address.P2PKHVersion, goCoin.P2PKHVersionByte())
		}

		if def.Address.P2SHVersion != uint8(goCoin.P2SHVersionByte()) {
			t.Errorf("Coin %s: P2SH version mismatch - manifest=0x%02x, go=0x%02x",
				def.Symbol, def.Address.P2SHVersion, goCoin.P2SHVersionByte())
		}
	}
}

func TestManifestGenesisHashMatchesGo(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	for _, def := range manifest.Coins {
		goCoin, err := Create(def.Symbol)
		if err != nil {
			continue
		}

		if !strings.EqualFold(def.Chain.GenesisHash, goCoin.GenesisBlockHash()) {
			t.Errorf("Coin %s: genesis_hash mismatch - manifest=%s, go=%s",
				def.Symbol, def.Chain.GenesisHash, goCoin.GenesisBlockHash())
		}
	}
}

func TestManifestAuxPowChainIDsMatchGo(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	for _, def := range manifest.Coins {
		if def.MergeMining == nil || !def.MergeMining.SupportsAuxPow {
			continue
		}

		goCoin, err := Create(def.Symbol)
		if err != nil {
			t.Errorf("AuxPoW coin %s not in Go registry", def.Symbol)
			continue
		}

		auxCoin, ok := goCoin.(AuxPowCoin)
		if !ok {
			t.Errorf("Coin %s marked as AuxPoW in manifest but Go doesn't implement AuxPowCoin",
				def.Symbol)
			continue
		}

		// CRITICAL: Chain ID must match exactly
		if int32(def.MergeMining.ChainID) != auxCoin.ChainID() {
			t.Errorf("CRITICAL: Coin %s chain_id mismatch - manifest=%d, go=%d (network consensus will fail!)",
				def.Symbol, def.MergeMining.ChainID, auxCoin.ChainID())
		}

		// CRITICAL: Version bit must match exactly
		if def.MergeMining.VersionBit != auxCoin.AuxPowVersionBit() {
			t.Errorf("CRITICAL: Coin %s version_bit mismatch - manifest=0x%08x, go=0x%08x (network consensus will fail!)",
				def.Symbol, def.MergeMining.VersionBit, auxCoin.AuxPowVersionBit())
		}
	}
}

func TestManifestAlgorithmBoundaries(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	sha256dCoins := []string{}
	scryptCoins := []string{}

	for _, def := range manifest.Coins {
		switch def.Algorithm {
		case "sha256d":
			sha256dCoins = append(sha256dCoins, def.Symbol)
		case "scrypt":
			scryptCoins = append(scryptCoins, def.Symbol)
		}

		// Verify merge-mining respects algorithm boundaries
		if def.MergeMining != nil && def.MergeMining.ParentChain != "" {
			parentDef := GetCoinDef(def.MergeMining.ParentChain)
			if parentDef == nil {
				t.Errorf("Coin %s references non-existent parent chain %s",
					def.Symbol, def.MergeMining.ParentChain)
				continue
			}
			if def.Algorithm != parentDef.Algorithm {
				t.Errorf("CRITICAL: Coin %s (%s) cannot merge-mine with %s (%s) - algorithm mismatch!",
					def.Symbol, def.Algorithm, def.MergeMining.ParentChain, parentDef.Algorithm)
			}
		}
	}

	t.Logf("SHA-256d coins (%d): %v", len(sha256dCoins), sha256dCoins)
	t.Logf("Scrypt coins (%d): %v", len(scryptCoins), scryptCoins)
}

func TestManifestRoleConsistency(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	parentCoins := []string{}
	auxCoins := []string{}
	standaloneCoins := []string{}

	for _, def := range manifest.Coins {
		switch def.Role {
		case "parent":
			parentCoins = append(parentCoins, def.Symbol)
			// Parent must have can_be_parent = true
			if def.MergeMining == nil || !def.MergeMining.CanBeParent {
				t.Errorf("Coin %s has role=parent but merge_mining.can_be_parent is not true",
					def.Symbol)
			}
		case "aux":
			auxCoins = append(auxCoins, def.Symbol)
			// Aux must have supports_auxpow = true
			if def.MergeMining == nil || !def.MergeMining.SupportsAuxPow {
				t.Errorf("Coin %s has role=aux but merge_mining.supports_auxpow is not true",
					def.Symbol)
			}
			// Aux must have parent_chain
			if def.MergeMining == nil || def.MergeMining.ParentChain == "" {
				t.Errorf("Coin %s has role=aux but merge_mining.parent_chain is empty",
					def.Symbol)
			}
		case "standalone":
			standaloneCoins = append(standaloneCoins, def.Symbol)
		}
	}

	t.Logf("Parent chains (%d): %v", len(parentCoins), parentCoins)
	t.Logf("Aux chains (%d): %v", len(auxCoins), auxCoins)
	t.Logf("Standalone coins (%d): %v", len(standaloneCoins), standaloneCoins)

	// We should have at least one parent per algorithm
	sha256dParent := false
	scryptParent := false
	for _, p := range parentCoins {
		def := GetCoinDef(p)
		if def.Algorithm == "sha256d" {
			sha256dParent = true
		}
		if def.Algorithm == "scrypt" {
			scryptParent = true
		}
	}

	if !sha256dParent {
		t.Error("No SHA-256d parent chain defined (expected BTC)")
	}
	if !scryptParent {
		t.Error("No Scrypt parent chain defined (expected LTC)")
	}
}

func TestManifestNoDuplicateSymbols(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	seen := make(map[string]bool)
	for _, def := range manifest.Coins {
		symbol := strings.ToUpper(def.Symbol)
		if seen[symbol] {
			t.Errorf("Duplicate coin symbol: %s", symbol)
		}
		seen[symbol] = true
	}
}

func TestManifestNoDuplicatePorts(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	rpcPorts := make(map[int]string)
	p2pPorts := make(map[int]string)
	zmqPorts := make(map[int]string)

	for _, def := range manifest.Coins {
		// Skip shared nodes (they legitimately share ports)
		if def.Network.SharedNode != "" {
			continue
		}

		// Check RPC port
		if existing, exists := rpcPorts[def.Network.RPCPort]; exists {
			t.Errorf("Duplicate RPC port %d: %s and %s", def.Network.RPCPort, existing, def.Symbol)
		}
		rpcPorts[def.Network.RPCPort] = def.Symbol

		// Check P2P port
		if existing, exists := p2pPorts[def.Network.P2PPort]; exists {
			t.Errorf("Duplicate P2P port %d: %s and %s", def.Network.P2PPort, existing, def.Symbol)
		}
		p2pPorts[def.Network.P2PPort] = def.Symbol

		// Check ZMQ port
		if existing, exists := zmqPorts[def.Network.ZMQPort]; exists {
			t.Errorf("Duplicate ZMQ port %d: %s and %s", def.Network.ZMQPort, existing, def.Symbol)
		}
		zmqPorts[def.Network.ZMQPort] = def.Symbol
	}
}

func TestManifestCoinCount(t *testing.T) {
	path := findManifestPath(t)
	manifest, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	// We expect 14 coins total (9 SHA256d + 5 Scrypt)
	// Dead coins purged: XVG, JKC, LKY
	expectedCount := 14
	if len(manifest.Coins) != expectedCount {
		t.Errorf("Expected %d coins in manifest, got %d", expectedCount, len(manifest.Coins))
	}

	// Count by algorithm
	sha256dCount := 0
	scryptCount := 0
	for _, def := range manifest.Coins {
		switch def.Algorithm {
		case "sha256d":
			sha256dCount++
		case "scrypt":
			scryptCount++
		}
	}

	// Expected: 9 SHA256d (BTC, BCH, DGB, BC2, NMC, SYS, XMY, FBTC, QBX), 5 Scrypt (LTC, DOGE, DGB-SCRYPT, PEP, CAT)
	if sha256dCount != 9 {
		t.Errorf("Expected 9 SHA-256d coins, got %d", sha256dCount)
	}
	if scryptCount != 5 {
		t.Errorf("Expected 5 Scrypt coins, got %d", scryptCount)
	}
}

func TestGetCoinDef(t *testing.T) {
	path := findManifestPath(t)
	_, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	// Test getting a coin
	btc := GetCoinDef("BTC")
	if btc == nil {
		t.Fatal("GetCoinDef(BTC) returned nil")
	}
	if btc.Symbol != "BTC" {
		t.Errorf("Expected symbol BTC, got %s", btc.Symbol)
	}
	if btc.Algorithm != "sha256d" {
		t.Errorf("Expected algorithm sha256d, got %s", btc.Algorithm)
	}

	// Test case insensitivity
	doge := GetCoinDef("doge")
	if doge == nil {
		t.Fatal("GetCoinDef(doge) returned nil")
	}
	if doge.Symbol != "DOGE" {
		t.Errorf("Expected symbol DOGE, got %s", doge.Symbol)
	}

	// Test non-existent coin
	unknown := GetCoinDef("NOTACOIN")
	if unknown != nil {
		t.Errorf("GetCoinDef(NOTACOIN) should return nil, got %v", unknown)
	}
}

func TestListManifestCoinsByAlgorithm(t *testing.T) {
	path := findManifestPath(t)
	_, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	sha256dCoins := ListManifestCoinsByAlgorithm("sha256d")
	if len(sha256dCoins) == 0 {
		t.Error("ListManifestCoinsByAlgorithm(sha256d) returned empty")
	}

	// BTC should be in SHA256d list
	found := false
	for _, c := range sha256dCoins {
		if c == "BTC" {
			found = true
			break
		}
	}
	if !found {
		t.Error("BTC not found in SHA256d coins list")
	}

	scryptCoins := ListManifestCoinsByAlgorithm("scrypt")
	if len(scryptCoins) == 0 {
		t.Error("ListManifestCoinsByAlgorithm(scrypt) returned empty")
	}

	// DOGE should be in Scrypt list
	found = false
	for _, c := range scryptCoins {
		if c == "DOGE" {
			found = true
			break
		}
	}
	if !found {
		t.Error("DOGE not found in Scrypt coins list")
	}
}

func TestListAuxPowCoins(t *testing.T) {
	path := findManifestPath(t)
	_, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	auxCoins := ListAuxPowCoins()
	if len(auxCoins) == 0 {
		t.Error("ListAuxPowCoins returned empty")
	}

	// DOGE should be in AuxPoW list
	found := false
	for _, c := range auxCoins {
		if c == "DOGE" {
			found = true
			break
		}
	}
	if !found {
		t.Error("DOGE not found in AuxPoW coins list")
	}

	// BTC should NOT be in AuxPoW list (it's a parent, not aux)
	for _, c := range auxCoins {
		if c == "BTC" {
			t.Error("BTC should not be in AuxPoW coins list (it's a parent chain)")
		}
	}
}

func TestListParentChainCoins(t *testing.T) {
	path := findManifestPath(t)
	_, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("Failed to load manifest: %v", err)
	}

	parents := ListParentChainCoins()
	if len(parents) == 0 {
		t.Error("ListParentChainCoins returned empty")
	}

	// BTC and LTC should be in parent list
	btcFound := false
	ltcFound := false
	for _, c := range parents {
		if c == "BTC" {
			btcFound = true
		}
		if c == "LTC" {
			ltcFound = true
		}
	}
	if !btcFound {
		t.Error("BTC not found in parent chain list")
	}
	if !ltcFound {
		t.Error("LTC not found in parent chain list")
	}
}
