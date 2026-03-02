// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"github.com/spiralpool/stratum/internal/crypto"
	"go.uber.org/zap"
)

// =============================================================================
// G8: AuxPoW Manager Function Tests
// =============================================================================
// Tests NewManager validation, IsHealthy, MinAuxBlockTime, and GetAuxCoin.

// mockParentCoin implements coin.Coin + coin.ParentChainCoin for testing.
type mockParentCoin struct {
	symbol    string
	algorithm string
	canParent map[string]bool // auxAlgorithm -> canBeParent
}

func (m *mockParentCoin) Symbol() string                              { return m.symbol }
func (m *mockParentCoin) Name() string                                { return m.symbol }
func (m *mockParentCoin) Algorithm() string                           { return m.algorithm }
func (m *mockParentCoin) ValidateAddress(addr string) error           { return nil }
func (m *mockParentCoin) DecodeAddress(addr string) ([]byte, coin.AddressType, error) {
	return nil, coin.AddressTypeUnknown, nil
}
func (m *mockParentCoin) BuildCoinbaseScript(params coin.CoinbaseParams) ([]byte, error) {
	return nil, nil
}
func (m *mockParentCoin) SerializeBlockHeader(header *coin.BlockHeader) []byte { return nil }
func (m *mockParentCoin) HashBlockHeader(serialized []byte) []byte             { return nil }
func (m *mockParentCoin) TargetFromBits(bits uint32) *big.Int                  { return big.NewInt(0) }
func (m *mockParentCoin) DifficultyFromTarget(target *big.Int) float64         { return 0 }
func (m *mockParentCoin) ShareDifficultyMultiplier() float64                   { return 1.0 }
func (m *mockParentCoin) DefaultRPCPort() int                                  { return 8332 }
func (m *mockParentCoin) DefaultP2PPort() int                                  { return 8333 }
func (m *mockParentCoin) P2PKHVersionByte() byte                               { return 0x00 }
func (m *mockParentCoin) P2SHVersionByte() byte                                { return 0x05 }
func (m *mockParentCoin) Bech32HRP() string                                    { return "bc" }
func (m *mockParentCoin) SupportsSegWit() bool                                 { return true }
func (m *mockParentCoin) BlockTime() int                                       { return 600 }
func (m *mockParentCoin) MinCoinbaseScriptLen() int                            { return 2 }
func (m *mockParentCoin) CoinbaseMaturity() int                                { return 100 }
func (m *mockParentCoin) GenesisBlockHash() string                             { return "" }
func (m *mockParentCoin) VerifyGenesisBlock(hash string) error                 { return nil }
func (m *mockParentCoin) CanBeParentFor(auxAlgorithm string) bool {
	return m.canParent[auxAlgorithm]
}
func (m *mockParentCoin) CoinbaseAuxMarker() []byte  { return AuxMarker }
func (m *mockParentCoin) MaxCoinbaseAuxSize() int     { return 100 }

// mockAuxCoin implements coin.AuxPowCoin for testing.
type mockAuxCoin struct {
	symbol           string
	algorithm        string
	supportsAuxPow   bool
	auxPowStartHeight uint64
	chainID          int32
	blockTime        int
}

func (m *mockAuxCoin) Symbol() string                              { return m.symbol }
func (m *mockAuxCoin) Name() string                                { return m.symbol }
func (m *mockAuxCoin) Algorithm() string                           { return m.algorithm }
func (m *mockAuxCoin) ValidateAddress(addr string) error           { return nil }
func (m *mockAuxCoin) DecodeAddress(addr string) ([]byte, coin.AddressType, error) {
	return nil, coin.AddressTypeUnknown, nil
}
func (m *mockAuxCoin) BuildCoinbaseScript(params coin.CoinbaseParams) ([]byte, error) {
	return nil, nil
}
func (m *mockAuxCoin) SerializeBlockHeader(header *coin.BlockHeader) []byte { return nil }
func (m *mockAuxCoin) HashBlockHeader(serialized []byte) []byte             { return nil }
func (m *mockAuxCoin) TargetFromBits(bits uint32) *big.Int                  { return big.NewInt(0) }
func (m *mockAuxCoin) DifficultyFromTarget(target *big.Int) float64         { return 1.0 }
func (m *mockAuxCoin) ShareDifficultyMultiplier() float64                   { return 1.0 }
func (m *mockAuxCoin) DefaultRPCPort() int                                  { return 8334 }
func (m *mockAuxCoin) DefaultP2PPort() int                                  { return 8335 }
func (m *mockAuxCoin) P2PKHVersionByte() byte                               { return 0x34 }
func (m *mockAuxCoin) P2SHVersionByte() byte                                { return 0x05 }
func (m *mockAuxCoin) Bech32HRP() string                                    { return "" }
func (m *mockAuxCoin) SupportsSegWit() bool                                 { return false }
func (m *mockAuxCoin) BlockTime() int                                       { return m.blockTime }
func (m *mockAuxCoin) MinCoinbaseScriptLen() int                            { return 2 }
func (m *mockAuxCoin) CoinbaseMaturity() int                                { return 100 }
func (m *mockAuxCoin) GenesisBlockHash() string                             { return "" }
func (m *mockAuxCoin) VerifyGenesisBlock(hash string) error                 { return nil }
func (m *mockAuxCoin) SupportsAuxPow() bool                                 { return m.supportsAuxPow }
func (m *mockAuxCoin) AuxPowStartHeight() uint64                            { return m.auxPowStartHeight }
func (m *mockAuxCoin) ChainID() int32                                       { return m.chainID }
func (m *mockAuxCoin) AuxPowVersionBit() uint32                             { return 0x100 }
func (m *mockAuxCoin) ParseAuxBlockResponse(response map[string]interface{}) (*coin.AuxBlock, error) {
	return nil, nil
}
func (m *mockAuxCoin) SerializeAuxPowProof(proof *coin.AuxPowProof) ([]byte, error) {
	return nil, nil
}

// TestNewManager_ValidParentAndAux verifies that NewManager succeeds with
// a valid parent coin (BTC/SHA256d) and a compatible aux coin (NMC/SHA256d).
func TestNewManager_ValidParentAndAux(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	auxCfg := AuxChainConfig{
		Symbol:  "NMC",
		Coin:    &mockAuxCoin{symbol: "NMC", algorithm: "sha256d", supportsAuxPow: true, chainID: 1, blockTime: 600},
		Enabled: true,
	}

	mgr, err := NewManager(parent, []AuxChainConfig{auxCfg}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager should not be nil")
	}
}

// TestNewManager_NonParentCoin verifies that NewManager rejects a coin
// that doesn't implement ParentChainCoin.
func TestNewManager_NonParentCoin(t *testing.T) {
	t.Parallel()

	// A plain Coin (not ParentChainCoin) should be rejected.
	nonParent := &mockAuxCoin{
		symbol:         "DOGE",
		algorithm:      "scrypt",
		supportsAuxPow: true,
		chainID:        98,
		blockTime:      60,
	}

	auxCfg := AuxChainConfig{
		Symbol:  "NMC",
		Coin:    &mockAuxCoin{symbol: "NMC", algorithm: "scrypt", supportsAuxPow: true, chainID: 1, blockTime: 600},
		Enabled: true,
	}

	_, err := NewManager(nonParent, []AuxChainConfig{auxCfg}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for non-ParentChainCoin")
	}
}

// TestNewManager_IncompatibleAlgorithm verifies that NewManager rejects
// an aux chain whose algorithm doesn't match the parent.
func TestNewManager_IncompatibleAlgorithm(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true}, // Only SHA256d
	}
	auxCfg := AuxChainConfig{
		Symbol:  "DOGE",
		Coin:    &mockAuxCoin{symbol: "DOGE", algorithm: "scrypt", supportsAuxPow: true, chainID: 98, blockTime: 60},
		Enabled: true,
	}

	_, err := NewManager(parent, []AuxChainConfig{auxCfg}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for incompatible algorithm (BTC/SHA256d + DOGE/Scrypt)")
	}
}

// TestNewManager_AuxDoesNotSupportAuxPow verifies that NewManager rejects
// an aux coin that doesn't support AuxPoW.
func TestNewManager_AuxDoesNotSupportAuxPow(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	auxCfg := AuxChainConfig{
		Symbol:  "BCH",
		Coin:    &mockAuxCoin{symbol: "BCH", algorithm: "sha256d", supportsAuxPow: false, chainID: 0, blockTime: 600},
		Enabled: true,
	}

	_, err := NewManager(parent, []AuxChainConfig{auxCfg}, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for aux coin that doesn't support AuxPoW")
	}
}

// TestNewManager_EmptyAuxConfigs verifies that NewManager succeeds with
// empty aux configs (parent-only mining, no merge mining).
func TestNewManager_EmptyAuxConfigs(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}

	mgr, err := NewManager(parent, []AuxChainConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager should not be nil with empty aux configs")
	}
}

// TestNewManager_DisabledAuxChain verifies that disabled aux chains are
// skipped during validation (incompatible disabled chains don't cause error).
func TestNewManager_DisabledAuxChain(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	auxCfg := AuxChainConfig{
		Symbol:  "DOGE",
		Coin:    &mockAuxCoin{symbol: "DOGE", algorithm: "scrypt", supportsAuxPow: true, chainID: 98, blockTime: 60},
		Enabled: false, // Disabled - should be skipped
	}

	mgr, err := NewManager(parent, []AuxChainConfig{auxCfg}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error for disabled incompatible aux chain: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager should not be nil")
	}
}

// TestNewManager_LitecoinParent_DogeAux verifies the LTC+DOGE merge mining
// combination (both Scrypt).
func TestNewManager_LitecoinParent_DogeAux(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "LTC",
		algorithm: "scrypt",
		canParent: map[string]bool{"scrypt": true},
	}
	auxCfg := AuxChainConfig{
		Symbol:  "DOGE",
		Coin:    &mockAuxCoin{symbol: "DOGE", algorithm: "scrypt", supportsAuxPow: true, chainID: 98, blockTime: 60},
		Enabled: true,
	}

	mgr, err := NewManager(parent, []AuxChainConfig{auxCfg}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error for LTC+DOGE: %v", err)
	}
	if mgr == nil {
		t.Fatal("manager should not be nil")
	}
}

// TestManager_IsHealthy_NoBlocks verifies that IsHealthy returns false
// when no aux blocks have been loaded.
func TestManager_IsHealthy_NoBlocks(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	mgr, err := NewManager(parent, []AuxChainConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mgr.IsHealthy() {
		t.Error("should be unhealthy with no aux blocks loaded")
	}
}

// TestManager_IsHealthy_StaleTemplates verifies that IsHealthy returns false
// when templates are older than 5 minutes.
func TestManager_IsHealthy_StaleTemplates(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	mgr, err := NewManager(parent, []AuxChainConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Store some blocks but with a stale refresh time
	blocks := []AuxBlockData{{Symbol: "NMC"}}
	mgr.currentAux.Store(&blocks)
	mgr.lastRefresh = time.Now().Add(-10 * time.Minute) // 10 min ago = stale

	if mgr.IsHealthy() {
		t.Error("should be unhealthy with stale templates (>5 min)")
	}
}

// TestManager_IsHealthy_ConsecutiveErrors verifies that IsHealthy returns
// false when there are more than 5 consecutive refresh errors.
func TestManager_IsHealthy_ConsecutiveErrors(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	mgr, err := NewManager(parent, []AuxChainConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	blocks := []AuxBlockData{{Symbol: "NMC"}}
	mgr.currentAux.Store(&blocks)
	mgr.lastRefresh = time.Now()
	mgr.refreshErrors = 6 // > 5 threshold

	if mgr.IsHealthy() {
		t.Error("should be unhealthy with 6 consecutive errors (threshold is 5)")
	}
}

// TestManager_IsHealthy_Healthy verifies that IsHealthy returns true
// when blocks are loaded, templates are fresh, and errors are low.
func TestManager_IsHealthy_Healthy(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}
	mgr, err := NewManager(parent, []AuxChainConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	blocks := []AuxBlockData{{Symbol: "NMC"}}
	mgr.currentAux.Store(&blocks)
	mgr.lastRefresh = time.Now()
	mgr.refreshErrors = 0

	if !mgr.IsHealthy() {
		t.Error("should be healthy: fresh blocks, no errors")
	}
}

// TestManager_MinAuxBlockTime verifies that MinAuxBlockTime returns
// the shortest block time among enabled aux chains.
func TestManager_MinAuxBlockTime(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}

	auxConfigs := []AuxChainConfig{
		{
			Symbol:  "NMC",
			Coin:    &mockAuxCoin{symbol: "NMC", algorithm: "sha256d", supportsAuxPow: true, chainID: 1, blockTime: 600},
			Enabled: true,
		},
		{
			Symbol:  "FBTC",
			Coin:    &mockAuxCoin{symbol: "FBTC", algorithm: "sha256d", supportsAuxPow: true, chainID: 2, blockTime: 120},
			Enabled: true,
		},
		{
			Symbol:  "SYS",
			Coin:    &mockAuxCoin{symbol: "SYS", algorithm: "sha256d", supportsAuxPow: true, chainID: 3, blockTime: 60},
			Enabled: true,
		},
	}

	mgr, err := NewManager(parent, auxConfigs, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	minTime := mgr.MinAuxBlockTime()
	if minTime != 60 {
		t.Errorf("MinAuxBlockTime: got %d, want 60 (SYS)", minTime)
	}
}

// TestManager_MinAuxBlockTime_NoEnabled verifies that MinAuxBlockTime
// returns 0 when no enabled aux chains exist.
func TestManager_MinAuxBlockTime_NoEnabled(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}

	mgr, err := NewManager(parent, []AuxChainConfig{}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if minTime := mgr.MinAuxBlockTime(); minTime != 0 {
		t.Errorf("MinAuxBlockTime with no chains: got %d, want 0", minTime)
	}
}

// TestManager_GetAuxCoin verifies that GetAuxCoin returns the correct
// aux coin for a given symbol.
func TestManager_GetAuxCoin(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}

	nmcCoin := &mockAuxCoin{symbol: "NMC", algorithm: "sha256d", supportsAuxPow: true, chainID: 1, blockTime: 600}
	auxConfigs := []AuxChainConfig{
		{Symbol: "NMC", Coin: nmcCoin, Enabled: true},
	}

	mgr, err := NewManager(parent, auxConfigs, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find NMC
	got, err := mgr.GetAuxCoin("NMC")
	if err != nil {
		t.Fatalf("GetAuxCoin(NMC): %v", err)
	}
	if got.Symbol() != "NMC" {
		t.Errorf("GetAuxCoin(NMC): got %s", got.Symbol())
	}

	// Should return error for unknown
	_, err = mgr.GetAuxCoin("UNKNOWN")
	if err == nil {
		t.Error("expected error for unknown aux coin")
	}
}

// TestManager_AuxChainCount verifies that AuxChainCount returns the count
// of enabled aux chains.
func TestManager_AuxChainCount(t *testing.T) {
	t.Parallel()

	parent := &mockParentCoin{
		symbol:    "BTC",
		algorithm: "sha256d",
		canParent: map[string]bool{"sha256d": true},
	}

	auxConfigs := []AuxChainConfig{
		{Symbol: "NMC", Coin: &mockAuxCoin{symbol: "NMC", algorithm: "sha256d", supportsAuxPow: true, chainID: 1, blockTime: 600}, Enabled: true},
		{Symbol: "FBTC", Coin: &mockAuxCoin{symbol: "FBTC", algorithm: "sha256d", supportsAuxPow: true, chainID: 2, blockTime: 120}, Enabled: false}, // Disabled
		{Symbol: "SYS", Coin: &mockAuxCoin{symbol: "SYS", algorithm: "sha256d", supportsAuxPow: true, chainID: 3, blockTime: 60}, Enabled: true},
	}

	mgr, err := NewManager(parent, auxConfigs, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count := mgr.AuxChainCount(); count != 2 {
		t.Errorf("AuxChainCount: got %d, want 2 (NMC + SYS, FBTC disabled)", count)
	}
}

// =============================================================================
// G9: VerifyAuxPowProof Real Hash Tests
// =============================================================================
// Tests VerifyAuxPowProof with actual SHA256d-computed hashes instead of
// precomputed constants, ensuring the verification pipeline works end-to-end.

// TestVerifyAuxPowProof_RealHashes_SingleChain verifies end-to-end proof
// construction and verification with real SHA256d hashes for a single aux chain.
func TestVerifyAuxPowProof_RealHashes_SingleChain(t *testing.T) {
	t.Parallel()

	// 1. Create a fake aux block hash (32 bytes)
	auxBlockHash := crypto.SHA256d([]byte("test-aux-block-nmc-height-12345"))

	// 2. For single chain: aux root = aux block hash
	auxRoot := auxBlockHash

	// 3. Build coinbase with embedded aux commitment
	commitment := BuildAuxCommitment(auxRoot, 1, 0)
	if commitment == nil {
		t.Fatal("BuildAuxCommitment returned nil")
	}
	if len(commitment) != AuxDataSize {
		t.Fatalf("commitment size: got %d, want %d", len(commitment), AuxDataSize)
	}

	// Build a parent coinbase: prefix + commitment + suffix
	coinbasePrefix := []byte("coinbase-prefix-data-")
	coinbaseSuffix := []byte("-coinbase-suffix")
	parentCoinbase := make([]byte, 0, len(coinbasePrefix)+len(commitment)+len(coinbaseSuffix))
	parentCoinbase = append(parentCoinbase, coinbasePrefix...)
	parentCoinbase = append(parentCoinbase, commitment...)
	parentCoinbase = append(parentCoinbase, coinbaseSuffix...)

	// 4. Compute coinbase hash
	coinbaseHash := crypto.SHA256d(parentCoinbase)

	// 5. Build a fake parent header (80 bytes) with merkle root at bytes 36-68.
	// For single-tx block, merkle root = coinbase hash.
	parentHeader := make([]byte, 80)
	parentHeader[0] = 0x01 // version
	copy(parentHeader[36:68], coinbaseHash) // merkle root = coinbase hash

	// 5a. Create parent block hash
	parentHash := crypto.SHA256d(parentHeader)

	// 6. Build proof using BuildAuxPowProof
	auxBlock := &AuxBlockData{
		Symbol:     "NMC",
		ChainID:    1,
		Hash:       auxBlockHash,
		Height:     12345,
		ChainIndex: 0,
	}

	proof, err := BuildAuxPowProof(
		parentCoinbase,
		nil, // No coinbase merkle branch (single tx)
		parentHeader,
		parentHash,
		auxBlock,
		nil, // No aux merkle branch (single chain)
	)
	if err != nil {
		t.Fatalf("BuildAuxPowProof: %v", err)
	}

	// 7. Verify the proof
	err = VerifyAuxPowProof(proof, auxBlockHash, auxRoot)
	if err != nil {
		t.Fatalf("VerifyAuxPowProof: %v", err)
	}
}

// TestVerifyAuxPowProof_RealHashes_TwoChains verifies proof construction and
// verification with two aux chains using real SHA256d hashes.
func TestVerifyAuxPowProof_RealHashes_TwoChains(t *testing.T) {
	t.Parallel()

	// Two aux chain hashes
	auxHash0 := crypto.SHA256d([]byte("aux-chain-0-nmc"))
	auxHash1 := crypto.SHA256d([]byte("aux-chain-1-fbtc"))

	// Build 2-leaf merkle tree
	// Root = SHA256d(hash0 || hash1)
	combined := append(auxHash0, auxHash1...)
	auxRoot := crypto.SHA256d(combined)

	// Build commitment and coinbase
	commitment := BuildAuxCommitment(auxRoot, 2, 0)
	coinbasePrefix := []byte("parent-coinbase-")
	parentCoinbase := append(coinbasePrefix, commitment...)

	coinbaseHash := crypto.SHA256d(parentCoinbase)

	// Build parent header
	parentHeader := make([]byte, 80)
	parentHeader[0] = 0x02
	copy(parentHeader[36:68], coinbaseHash)

	// Create parent block hash
	parentHash := crypto.SHA256d(parentHeader)

	// Build proof for chain 0 (index 0)
	// Branch for index 0 in a 2-leaf tree: [hash1]
	auxBlock0 := &AuxBlockData{
		Symbol:     "NMC",
		ChainID:    1,
		Hash:       auxHash0,
		Height:     100,
		ChainIndex: 0,
	}

	proof0, err := BuildAuxPowProof(
		parentCoinbase,
		nil, // Single-tx coinbase
		parentHeader,
		parentHash,
		auxBlock0,
		[][]byte{auxHash1}, // Aux merkle branch
	)
	if err != nil {
		t.Fatalf("BuildAuxPowProof for chain 0: %v", err)
	}

	err = VerifyAuxPowProof(proof0, auxHash0, auxRoot)
	if err != nil {
		t.Fatalf("VerifyAuxPowProof for chain 0: %v", err)
	}

	// Build proof for chain 1 (index 1)
	auxBlock1 := &AuxBlockData{
		Symbol:     "FBTC",
		ChainID:    2,
		Hash:       auxHash1,
		Height:     200,
		ChainIndex: 1,
	}

	proof1, err := BuildAuxPowProof(
		parentCoinbase,
		nil,
		parentHeader,
		parentHash,
		auxBlock1,
		[][]byte{auxHash0}, // Aux merkle branch (sibling)
	)
	if err != nil {
		t.Fatalf("BuildAuxPowProof for chain 1: %v", err)
	}

	err = VerifyAuxPowProof(proof1, auxHash1, auxRoot)
	if err != nil {
		t.Fatalf("VerifyAuxPowProof for chain 1: %v", err)
	}
}

// TestVerifyAuxPowProof_WrongCoinbaseHash verifies that verification fails
// when the coinbase hash in the proof doesn't match the actual coinbase.
func TestVerifyAuxPowProof_WrongCoinbaseHash(t *testing.T) {
	t.Parallel()

	auxBlockHash := crypto.SHA256d([]byte("test-aux-block"))
	auxRoot := auxBlockHash

	commitment := BuildAuxCommitment(auxRoot, 1, 0)
	parentCoinbase := append([]byte("prefix-"), commitment...)
	coinbaseHash := crypto.SHA256d(parentCoinbase)

	parentHeader := make([]byte, 80)
	copy(parentHeader[36:68], coinbaseHash)

	// Tamper with the coinbase after hash computation
	tamperedCoinbase := make([]byte, len(parentCoinbase))
	copy(tamperedCoinbase, parentCoinbase)
	tamperedCoinbase[0] = 0xFF // Modify first byte

	// Build proof with tampered coinbase but correct coinbase hash
	proof := &coin.AuxPowProof{
		ParentCoinbase:     tamperedCoinbase,
		ParentCoinbaseHash: crypto.SHA256d(tamperedCoinbase), // Hash of tampered data
		ParentHeader:       parentHeader,
		AuxMerkleIndex:     0,
	}

	// Verification should fail because the coinbase hash won't match
	// the merkle root in the parent header
	err := VerifyAuxPowProof(proof, auxBlockHash, auxRoot)
	if err == nil {
		t.Error("expected verification to fail with wrong coinbase hash")
	}
}

// TestVerifyAuxPowProof_ExtractAuxRoot_RoundTrip verifies that
// BuildAuxCommitment and ExtractAuxRootFromCoinbase are consistent.
func TestVerifyAuxPowProof_ExtractAuxRoot_RoundTrip(t *testing.T) {
	t.Parallel()

	// Create a real aux root via SHA256d
	auxRoot := crypto.SHA256d([]byte("merkle-root-of-aux-chains"))

	// Build commitment
	commitment := BuildAuxCommitment(auxRoot, 4, 0)
	if commitment == nil {
		t.Fatal("nil commitment")
	}

	// Build coinbase containing the commitment
	coinbase := append([]byte("coinbase-prefix-data-"), commitment...)
	coinbase = append(coinbase, []byte("-suffix")...)

	// Extract and verify round-trip
	extracted := ExtractAuxRootFromCoinbase(coinbase)
	if extracted == nil {
		t.Fatal("failed to extract aux root from coinbase")
	}

	if !bytes.Equal(extracted, auxRoot) {
		t.Errorf("extracted root %x != original %x", extracted, auxRoot)
	}
}

// TestVerifyAuxPowProof_NilProof verifies graceful handling of nil proof.
func TestVerifyAuxPowProof_NilProof(t *testing.T) {
	t.Parallel()

	err := VerifyAuxPowProof(nil, nil, nil)
	if err == nil {
		t.Error("expected error for nil proof")
	}
}
