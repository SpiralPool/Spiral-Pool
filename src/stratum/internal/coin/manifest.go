// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package coin - Manifest loading and validation for single coin manifest architecture.
//
// The manifest (coins.manifest.yaml) is the canonical source for coin metadata.
// Consensus-critical logic (hashing, serialization) remains in Go code.
// Manifest values are validated against Go constants at startup.
package coin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ═══════════════════════════════════════════════════════════════════════════════
// MANIFEST TYPES
// ═══════════════════════════════════════════════════════════════════════════════

// Manifest represents the parsed coins.manifest.yaml
type Manifest struct {
	SchemaVersion string         `yaml:"schema_version"`
	Global        GlobalConfig   `yaml:"global"`
	Coins         []CoinDef      `yaml:"coins"`
	coinsBySymbol map[string]int // index lookup, populated after load
}

// GlobalConfig contains global manifest settings
type GlobalConfig struct {
	SupportedAlgorithms []string `yaml:"supported_algorithms"`
}

// CoinDef represents a single coin definition in the manifest
type CoinDef struct {
	Symbol      string             `yaml:"symbol"`
	Name        string             `yaml:"name"`
	Algorithm   string             `yaml:"algorithm"`
	Role        string             `yaml:"role"` // parent, aux, standalone
	Network     NetworkConfig      `yaml:"network"`
	Address     AddressConfig      `yaml:"address"`
	Chain       ChainConfig        `yaml:"chain"`
	MergeMining *MergeMiningConfig `yaml:"merge_mining,omitempty"`
	Display     DisplayConfig      `yaml:"display"`
}

// NetworkConfig contains network port defaults
type NetworkConfig struct {
	RPCPort    int    `yaml:"rpc_port"`
	P2PPort    int    `yaml:"p2p_port"`
	ZMQPort    int    `yaml:"zmq_port"`
	SharedNode string `yaml:"shared_node,omitempty"` // If this coin shares a node with another
}

// AddressConfig contains address encoding parameters
type AddressConfig struct {
	P2PKHVersion     uint8  `yaml:"p2pkh_version"`
	P2SHVersion      uint8  `yaml:"p2sh_version"`
	Bech32HRP        string `yaml:"bech32_hrp,omitempty"`
	SupportsCashAddr bool   `yaml:"supports_cashaddr,omitempty"`
	CollisionWarning string `yaml:"collision_warning,omitempty"`
}

// ChainConfig contains chain parameters
type ChainConfig struct {
	GenesisHash          string `yaml:"genesis_hash"`
	BlockTime            int    `yaml:"block_time"`
	CoinbaseMaturity     int    `yaml:"coinbase_maturity"`
	SupportsSegWit       bool   `yaml:"supports_segwit"`
	MinCoinbaseScriptLen int    `yaml:"min_coinbase_script_len"`
}

// MergeMiningConfig contains AuxPoW / merge-mining configuration
type MergeMiningConfig struct {
	// For parent chains
	CanBeParent              bool     `yaml:"can_be_parent,omitempty"`
	SupportedAuxAlgorithms   []string `yaml:"supported_aux_algorithms,omitempty"`

	// For auxiliary chains
	SupportsAuxPow    bool   `yaml:"supports_auxpow,omitempty"`
	AuxPowStartHeight uint64 `yaml:"auxpow_start_height,omitempty"`
	ChainID           int32  `yaml:"chain_id,omitempty"`     // CONSENSUS-CRITICAL
	VersionBit        uint32 `yaml:"version_bit,omitempty"`  // CONSENSUS-CRITICAL
	ParentChain       string `yaml:"parent_chain,omitempty"` // e.g., "LTC" for DOGE
}

// DisplayConfig contains UI/display metadata
type DisplayConfig struct {
	FullName    string  `yaml:"full_name"`
	ShortName   string  `yaml:"short_name"`
	CoingeckoID *string `yaml:"coingecko_id"` // nil if not listed
	ExplorerURL *string `yaml:"explorer_url"` // nil if none
}

// ═══════════════════════════════════════════════════════════════════════════════
// MANIFEST LOADING
// ═══════════════════════════════════════════════════════════════════════════════

var (
	loadedManifest *Manifest
	manifestMu     sync.RWMutex
)

// DefaultManifestPath returns the default path for the coin manifest
func DefaultManifestPath() string {
	// Check environment variable first
	if path := os.Getenv("SPIRALPOOL_MANIFEST_PATH"); path != "" {
		return path
	}
	// Default locations in order of preference
	paths := []string{
		"/spiralpool/config/coins.manifest.yaml",
		"./config/coins.manifest.yaml",
		"../config/coins.manifest.yaml",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0] // Return first path as default even if not found
}

// LoadManifest loads and validates the coin manifest from the given path.
// This should be called once at startup.
func LoadManifest(path string) (*Manifest, error) {
	manifestMu.Lock()
	defer manifestMu.Unlock()

	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest at %s: %w", path, err)
	}

	// Parse YAML
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Validate schema version
	if manifest.SchemaVersion == "" {
		return nil, fmt.Errorf("manifest missing schema_version")
	}
	if manifest.SchemaVersion != "1.0" {
		return nil, fmt.Errorf("unsupported manifest schema version: %s (expected 1.0)", manifest.SchemaVersion)
	}

	// Build symbol index
	manifest.coinsBySymbol = make(map[string]int, len(manifest.Coins))
	for i, coin := range manifest.Coins {
		symbol := strings.ToUpper(coin.Symbol)
		if _, exists := manifest.coinsBySymbol[symbol]; exists {
			return nil, fmt.Errorf("duplicate coin symbol in manifest: %s", symbol)
		}
		manifest.coinsBySymbol[symbol] = i
	}

	// Validate against Go registry
	validator := &ManifestValidator{}
	if err := validator.Validate(&manifest); err != nil {
		return nil, err
	}

	// Store globally
	loadedManifest = &manifest

	return &manifest, nil
}

// MustLoadManifest loads the manifest and panics on error.
// Use this for startup where manifest is required.
func MustLoadManifest(path string) *Manifest {
	manifest, err := LoadManifest(path)
	if err != nil {
		panic(fmt.Sprintf("FATAL: Manifest validation failed: %v", err))
	}
	return manifest
}

// GetManifest returns the loaded manifest, or nil if not loaded.
func GetManifest() *Manifest {
	manifestMu.RLock()
	defer manifestMu.RUnlock()
	return loadedManifest
}

// GetCoinDef returns the manifest definition for a coin by symbol.
// Returns nil if manifest not loaded or coin not found.
func GetCoinDef(symbol string) *CoinDef {
	manifestMu.RLock()
	defer manifestMu.RUnlock()

	if loadedManifest == nil {
		return nil
	}

	symbol = strings.ToUpper(symbol)
	if idx, ok := loadedManifest.coinsBySymbol[symbol]; ok {
		return &loadedManifest.Coins[idx]
	}
	return nil
}

// ListManifestCoins returns all coin symbols in the manifest.
func ListManifestCoins() []string {
	manifestMu.RLock()
	defer manifestMu.RUnlock()

	if loadedManifest == nil {
		return nil
	}

	symbols := make([]string, len(loadedManifest.Coins))
	for i, coin := range loadedManifest.Coins {
		symbols[i] = coin.Symbol
	}
	return symbols
}

// ListManifestCoinsByAlgorithm returns coins filtered by algorithm.
func ListManifestCoinsByAlgorithm(algorithm string) []string {
	manifestMu.RLock()
	defer manifestMu.RUnlock()

	if loadedManifest == nil {
		return nil
	}

	var symbols []string
	for _, coin := range loadedManifest.Coins {
		if coin.Algorithm == algorithm {
			symbols = append(symbols, coin.Symbol)
		}
	}
	return symbols
}

// ListAuxPowCoins returns all coins that support AuxPoW (merge-mining).
func ListAuxPowCoins() []string {
	manifestMu.RLock()
	defer manifestMu.RUnlock()

	if loadedManifest == nil {
		return nil
	}

	var symbols []string
	for _, coin := range loadedManifest.Coins {
		if coin.MergeMining != nil && coin.MergeMining.SupportsAuxPow {
			symbols = append(symbols, coin.Symbol)
		}
	}
	return symbols
}

// ListParentChainCoins returns all coins that can serve as merge-mining parents.
func ListParentChainCoins() []string {
	manifestMu.RLock()
	defer manifestMu.RUnlock()

	if loadedManifest == nil {
		return nil
	}

	var symbols []string
	for _, coin := range loadedManifest.Coins {
		if coin.MergeMining != nil && coin.MergeMining.CanBeParent {
			symbols = append(symbols, coin.Symbol)
		}
	}
	return symbols
}

// ═══════════════════════════════════════════════════════════════════════════════
// MANIFEST VALIDATION
// ═══════════════════════════════════════════════════════════════════════════════

// ManifestValidator validates manifest against Go registry
type ManifestValidator struct {
	errors   []string
	warnings []string
}

// Validate checks the manifest against the Go coin registry.
// Returns error if any critical validation fails.
func (v *ManifestValidator) Validate(manifest *Manifest) error {
	for _, coinDef := range manifest.Coins {
		v.validateCoin(&coinDef)
	}

	// Log warnings
	for _, warn := range v.warnings {
		fmt.Printf("MANIFEST WARNING: %s\n", warn)
	}

	// Return errors
	if len(v.errors) > 0 {
		return fmt.Errorf("manifest validation failed with %d errors:\n  - %s",
			len(v.errors), strings.Join(v.errors, "\n  - "))
	}

	return nil
}

func (v *ManifestValidator) validateCoin(def *CoinDef) {
	symbol := strings.ToUpper(def.Symbol)

	// 1. Coin must exist in Go registry
	goCoin, err := Create(symbol)
	if err != nil {
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: not found in Go registry (manifest has it, Go doesn't)", symbol))
		return
	}

	// 2. Algorithm must match
	if def.Algorithm != goCoin.Algorithm() {
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: algorithm mismatch (manifest=%s, go=%s)",
			symbol, def.Algorithm, goCoin.Algorithm()))
	}

	// 3. Block time must match
	if def.Chain.BlockTime != goCoin.BlockTime() {
		v.warnings = append(v.warnings, fmt.Sprintf(
			"Coin %s: block_time mismatch (manifest=%d, go=%d)",
			symbol, def.Chain.BlockTime, goCoin.BlockTime()))
	}

	// 4. Address version bytes must match
	if def.Address.P2PKHVersion != uint8(goCoin.P2PKHVersionByte()) {
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: P2PKH version mismatch (manifest=0x%02x, go=0x%02x)",
			symbol, def.Address.P2PKHVersion, goCoin.P2PKHVersionByte()))
	}

	if def.Address.P2SHVersion != uint8(goCoin.P2SHVersionByte()) {
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: P2SH version mismatch (manifest=0x%02x, go=0x%02x)",
			symbol, def.Address.P2SHVersion, goCoin.P2SHVersionByte()))
	}

	// 5. Bech32 HRP must match (if provided)
	if def.Address.Bech32HRP != "" && def.Address.Bech32HRP != goCoin.Bech32HRP() {
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: bech32_hrp mismatch (manifest=%s, go=%s)",
			symbol, def.Address.Bech32HRP, goCoin.Bech32HRP()))
	}

	// 6. Genesis hash must match
	if !strings.EqualFold(def.Chain.GenesisHash, goCoin.GenesisBlockHash()) {
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: genesis_hash mismatch (manifest=%s, go=%s)",
			symbol, def.Chain.GenesisHash, goCoin.GenesisBlockHash()))
	}

	// 7. AuxPoW parameters must match (CONSENSUS-CRITICAL)
	v.validateAuxPow(def, goCoin)

	// 8. Validate role consistency
	v.validateRole(def, goCoin)
}

func (v *ManifestValidator) validateAuxPow(def *CoinDef, goCoin Coin) {
	symbol := def.Symbol

	// Check if Go coin supports AuxPoW
	auxCoin, isAuxCoin := goCoin.(AuxPowCoin)

	if def.MergeMining != nil && def.MergeMining.SupportsAuxPow {
		// Manifest says AuxPoW supported
		if !isAuxCoin {
			v.errors = append(v.errors, fmt.Sprintf(
				"Coin %s: manifest says supports_auxpow=true but Go doesn't implement AuxPowCoin",
				symbol))
			return
		}

		if !auxCoin.SupportsAuxPow() {
			v.errors = append(v.errors, fmt.Sprintf(
				"Coin %s: manifest says supports_auxpow=true but Go.SupportsAuxPow() returns false",
				symbol))
			return
		}

		// CRITICAL: Chain ID must match exactly
		if int32(def.MergeMining.ChainID) != auxCoin.ChainID() {
			v.errors = append(v.errors, fmt.Sprintf(
				"CRITICAL: Coin %s: chain_id mismatch (manifest=%d, go=%d) - THIS WILL CAUSE AUX BLOCKS TO BE REJECTED",
				symbol, def.MergeMining.ChainID, auxCoin.ChainID()))
		}

		// CRITICAL: Version bit must match exactly
		if def.MergeMining.VersionBit != auxCoin.AuxPowVersionBit() {
			v.errors = append(v.errors, fmt.Sprintf(
				"CRITICAL: Coin %s: version_bit mismatch (manifest=0x%08x, go=0x%08x) - THIS WILL CAUSE AUX BLOCKS TO BE REJECTED",
				symbol, def.MergeMining.VersionBit, auxCoin.AuxPowVersionBit()))
		}

		// AuxPoW start height must match
		if def.MergeMining.AuxPowStartHeight != auxCoin.AuxPowStartHeight() {
			v.warnings = append(v.warnings, fmt.Sprintf(
				"Coin %s: auxpow_start_height mismatch (manifest=%d, go=%d)",
				symbol, def.MergeMining.AuxPowStartHeight, auxCoin.AuxPowStartHeight()))
		}
	} else if isAuxCoin && auxCoin.SupportsAuxPow() {
		// Go says AuxPoW supported but manifest doesn't have it
		v.warnings = append(v.warnings, fmt.Sprintf(
			"Coin %s: Go.SupportsAuxPow() returns true but manifest missing merge_mining config",
			symbol))
	}

	// Check parent chain capability
	parentCoin, isParentCoin := goCoin.(ParentChainCoin)
	if def.MergeMining != nil && def.MergeMining.CanBeParent {
		if !isParentCoin {
			v.errors = append(v.errors, fmt.Sprintf(
				"Coin %s: manifest says can_be_parent=true but Go doesn't implement ParentChainCoin",
				symbol))
		}
	} else if isParentCoin {
		// Check if Go can be parent for any algorithm
		canBeParent := parentCoin.CanBeParentFor("sha256d") || parentCoin.CanBeParentFor("scrypt")
		if canBeParent && (def.MergeMining == nil || !def.MergeMining.CanBeParent) {
			v.warnings = append(v.warnings, fmt.Sprintf(
				"Coin %s: Go implements ParentChainCoin but manifest missing can_be_parent=true",
				symbol))
		}
	}
}

func (v *ManifestValidator) validateRole(def *CoinDef, goCoin Coin) {
	symbol := def.Symbol

	switch def.Role {
	case "parent":
		_, isParent := goCoin.(ParentChainCoin)
		if !isParent {
			v.errors = append(v.errors, fmt.Sprintf(
				"Coin %s: manifest role=parent but Go doesn't implement ParentChainCoin",
				symbol))
		}
	case "aux":
		auxCoin, isAux := goCoin.(AuxPowCoin)
		if !isAux || !auxCoin.SupportsAuxPow() {
			v.errors = append(v.errors, fmt.Sprintf(
				"Coin %s: manifest role=aux but Go doesn't support AuxPoW",
				symbol))
		}
	case "standalone":
		// No special requirements
	default:
		v.errors = append(v.errors, fmt.Sprintf(
			"Coin %s: invalid role '%s' (must be parent, aux, or standalone)",
			symbol, def.Role))
	}
}

// Errors returns validation errors
func (v *ManifestValidator) Errors() []string {
	return v.errors
}

// Warnings returns validation warnings
func (v *ManifestValidator) Warnings() []string {
	return v.warnings
}

// ═══════════════════════════════════════════════════════════════════════════════
// HELPER FUNCTIONS
// ═══════════════════════════════════════════════════════════════════════════════

// GetManifestPath returns the absolute path to the manifest file.
// Useful for other systems that need to locate the manifest.
func GetManifestPath() string {
	path := DefaultManifestPath()
	absPath, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absPath
}

// IsManifestLoaded returns true if the manifest has been loaded.
func IsManifestLoaded() bool {
	manifestMu.RLock()
	defer manifestMu.RUnlock()
	return loadedManifest != nil
}

// CoinCount returns the number of coins in the loaded manifest.
func CoinCount() int {
	manifestMu.RLock()
	defer manifestMu.RUnlock()
	if loadedManifest == nil {
		return 0
	}
	return len(loadedManifest.Coins)
}
