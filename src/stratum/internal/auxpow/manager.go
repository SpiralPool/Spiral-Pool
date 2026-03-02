// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package auxpow

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/spiralpool/stratum/internal/coin"
	"go.uber.org/zap"
)

// debugLogAuxBlockFetch logs aux block template fetch for forensic analysis.
func debugLogAuxBlockFetch(symbol string, height uint64, difficulty float64, targetHex string, hash []byte, chainID int32) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_AUXPOW_BLOCK symbol=%s height=%d difficulty=%.8f target=%s hash=%s chain_id=%d\n",
		symbol,
		height,
		difficulty,
		targetHex,
		hex.EncodeToString(hash),
		chainID,
	)
}

// debugLogAuxDifficultyContext logs parent vs child difficulty comparison.
// AUDIT REQUIREMENT: AuxPoW parent diff, child diff for comparison
func debugLogAuxDifficultyContext(parentSymbol, childSymbol string, parentDiff, childDiff float64, parentAlgo, childAlgo string) {
	if !auditDebugEnabled {
		return
	}
	fmt.Printf("AUDIT_AUXPOW_DIFFICULTY parent_symbol=%s child_symbol=%s parent_diff=%.8f child_diff=%.8f parent_algo=%s child_algo=%s\n",
		parentSymbol,
		childSymbol,
		parentDiff,
		childDiff,
		parentAlgo,
		childAlgo,
	)
}

// NewManager creates a new AuxPoW manager for coordinating merge mining.
//
// The manager handles:
//   - Fetching aux block templates from aux chain nodes
//   - Building aux merkle trees for multiple aux chains
//   - Coordinating aux template refresh
//
// Parameters:
//   - parentCoin: The parent chain coin implementation
//   - auxConfigs: Configuration for each auxiliary chain
//   - logger: Logger for operational messages
//
// Returns error if parent coin cannot serve as merge mining parent or
// if aux chains are incompatible.
func NewManager(parentCoin coin.Coin, auxConfigs []AuxChainConfig, logger *zap.Logger) (*Manager, error) {
	log := logger.Sugar()

	// Validate parent can serve as merge mining parent
	parent, ok := parentCoin.(coin.ParentChainCoin)
	if !ok {
		return nil, fmt.Errorf("parent coin %s does not implement ParentChainCoin interface", parentCoin.Symbol())
	}

	// Validate all aux chains are compatible with parent
	for _, auxCfg := range auxConfigs {
		if !auxCfg.Enabled {
			continue
		}

		// Verify parent can mine this aux chain's algorithm
		if !parent.CanBeParentFor(auxCfg.Coin.Algorithm()) {
			return nil, fmt.Errorf("parent %s (algo: %s) cannot merge mine %s (algo: %s)",
				parentCoin.Symbol(), parentCoin.Algorithm(),
				auxCfg.Symbol, auxCfg.Coin.Algorithm())
		}

		// Verify aux coin supports AuxPoW
		if !auxCfg.Coin.SupportsAuxPow() {
			return nil, fmt.Errorf("aux chain %s does not support AuxPoW", auxCfg.Symbol)
		}

		log.Infow("Aux chain configured for merge mining",
			"symbol", auxCfg.Symbol,
			"chainID", auxCfg.Coin.ChainID(),
			"auxPowStartHeight", auxCfg.Coin.AuxPowStartHeight(),
		)
	}

	m := &Manager{
		parentCoin:    parentCoin,
		auxConfigs:    auxConfigs,
		prevAuxHashes: make(map[string]prevAuxState),
		logger:        log,
	}

	return m, nil
}

// RefreshAuxBlocks fetches new aux block templates from all enabled aux chains.
//
// This should be called periodically and before generating new jobs to ensure
// aux templates are fresh.
//
// Returns the current aux block data for all successfully fetched chains.
// Chains that fail to fetch are logged but don't cause the entire refresh to fail.
func (m *Manager) RefreshAuxBlocks(ctx context.Context) ([]AuxBlockData, error) {
	m.auxMu.Lock()
	defer m.auxMu.Unlock()

	auxBlocks := make([]AuxBlockData, 0, len(m.auxConfigs))
	var lastErr error

	for _, auxCfg := range m.auxConfigs {
		if !auxCfg.Enabled {
			continue
		}

		if auxCfg.DaemonClient == nil {
			m.logger.Warnw("No daemon client for aux chain", "chain", auxCfg.Symbol)
			continue
		}

		// Fetch aux block template via getauxblock or createauxblock RPC
		// Fractal Bitcoin uses createauxblock(address) instead of getauxblock
		var (
			response map[string]interface{}
			err      error
		)
		if cab, ok := auxCfg.Coin.(coin.CreateAuxBlockCoin); ok && cab.UseCreateAuxBlock() {
			response, err = auxCfg.DaemonClient.CreateAuxBlockWithAddress(ctx, auxCfg.Address)
		} else {
			response, err = auxCfg.DaemonClient.GetAuxBlock(ctx)
		}
		if err != nil {
			m.logger.Errorw("Failed to fetch aux block",
				"chain", auxCfg.Symbol,
				"error", err,
			)
			lastErr = err
			continue
		}

		// Parse the response using coin-specific parser
		auxBlock, err := auxCfg.Coin.ParseAuxBlockResponse(response)
		if err != nil {
			m.logger.Errorw("Failed to parse aux block response",
				"chain", auxCfg.Symbol,
				"error", err,
			)
			lastErr = err
			continue
		}

		// Calculate difficulty for logging
		difficulty := auxCfg.Coin.DifficultyFromTarget(auxBlock.Target)

		auxBlockData := AuxBlockData{
			Symbol:            auxCfg.Symbol,
			ChainID:           auxBlock.ChainID,
			Hash:              auxBlock.Hash,
			Target:            auxBlock.Target,
			Height:            auxBlock.Height,
			CoinbaseValue:     auxBlock.CoinbaseValue,
			ChainIndex:        auxBlock.ChainIndex,
			Difficulty:        difficulty,
			PreviousBlockHash: auxBlock.PreviousBlockHash,
			Bits:              auxBlock.Bits,
			FetchedAt:         time.Now(),
		}

		auxBlocks = append(auxBlocks, auxBlockData)

		// AUDIT: Log aux block fetch (single-line, structured, deterministic)
		targetHex := ""
		if auxBlock.Target != nil {
			targetHex = fmt.Sprintf("%064x", auxBlock.Target)
		}
		debugLogAuxBlockFetch(
			auxCfg.Symbol,
			auxBlock.Height,
			difficulty,
			targetHex,
			auxBlock.Hash,
			auxBlock.ChainID,
		)

		m.logger.Debugw("Fetched aux block",
			"chain", auxCfg.Symbol,
			"height", auxBlock.Height,
			"difficulty", fmt.Sprintf("%.2f", difficulty),
			"hash", fmt.Sprintf("%x", auxBlock.Hash[:8]),
		)

		// AUDIT FIX: Detect aux chain reorgs by comparing against previous state.
		// If the block hash changes at the same or lower height, the aux chain
		// has reorganized. This is important to log because stale aux templates
		// from the old chain tip would produce rejected blocks.
		if prev, ok := m.prevAuxHashes[auxCfg.Symbol]; ok {
			if auxBlock.Height <= prev.Height && !bytes.Equal(auxBlock.Hash, prev.Hash) {
				m.logger.Warnw("Aux chain reorg detected",
					"chain", auxCfg.Symbol,
					"prevHeight", prev.Height,
					"newHeight", auxBlock.Height,
					"prevHash", fmt.Sprintf("%x", prev.Hash[:8]),
					"newHash", fmt.Sprintf("%x", auxBlock.Hash[:8]),
				)
			}
		}
		m.prevAuxHashes[auxCfg.Symbol] = prevAuxState{
			Hash:   auxBlock.Hash,
			Height: auxBlock.Height,
		}
	}

	// Store atomically
	m.currentAux.Store(&auxBlocks)
	m.lastRefresh = time.Now()

	if len(auxBlocks) == 0 && lastErr != nil {
		m.refreshErrors++
		return nil, fmt.Errorf("failed to fetch any aux blocks: %w", lastErr)
	}

	m.refreshErrors = 0
	return auxBlocks, nil
}

// GetCurrentAuxBlocks returns the current aux block templates.
// This is safe for concurrent access.
func (m *Manager) GetCurrentAuxBlocks() []AuxBlockData {
	ptr := m.currentAux.Load()
	if ptr == nil {
		return nil
	}
	return *ptr
}

// GetAuxBlockBySymbol returns the aux block for a specific chain.
func (m *Manager) GetAuxBlockBySymbol(symbol string) *AuxBlockData {
	blocks := m.GetCurrentAuxBlocks()
	for i := range blocks {
		if blocks[i].Symbol == symbol {
			return &blocks[i]
		}
	}
	return nil
}

// BuildAuxMerkleData builds the aux merkle root and branches for job construction.
//
// For a single aux chain, returns the block hash as root with no branches.
// For multiple chains, constructs a proper merkle tree.
//
// Returns:
//   - root: 32-byte aux merkle root for coinbase embedding
//   - branches: Merkle branches for each aux block (for proof construction)
//   - treeSize: Number of leaves in the merkle tree
func (m *Manager) BuildAuxMerkleData(auxBlocks []AuxBlockData) (root []byte, branches [][]byte, treeSize uint32) {
	if len(auxBlocks) == 0 {
		return nil, nil, 0
	}

	root, branches = BuildAuxMerkleRoot(auxBlocks)
	treeSize = uint32(MerkleTreeSize(len(auxBlocks)))

	return root, branches, treeSize
}

// GetAuxCoin returns the coin implementation for an aux chain by symbol.
func (m *Manager) GetAuxCoin(symbol string) (coin.AuxPowCoin, error) {
	for _, cfg := range m.auxConfigs {
		if cfg.Symbol == symbol {
			return cfg.Coin, nil
		}
	}
	return nil, fmt.Errorf("aux chain not found: %s", symbol)
}

// GetAuxDaemonClient returns the daemon client for an aux chain by symbol.
func (m *Manager) GetAuxDaemonClient(symbol string) (*coin.Coin, error) {
	for _, cfg := range m.auxConfigs {
		if cfg.Symbol == symbol {
			var c coin.Coin = cfg.Coin
			return &c, nil
		}
	}
	return nil, fmt.Errorf("aux chain not found: %s", symbol)
}

// LastRefreshTime returns when aux blocks were last refreshed.
func (m *Manager) LastRefreshTime() time.Time {
	m.auxMu.RLock()
	defer m.auxMu.RUnlock()
	return m.lastRefresh
}

// RefreshErrorCount returns the number of consecutive refresh errors.
func (m *Manager) RefreshErrorCount() int {
	m.auxMu.RLock()
	defer m.auxMu.RUnlock()
	return m.refreshErrors
}

// IsHealthy returns true if aux templates are fresh and accessible.
func (m *Manager) IsHealthy() bool {
	m.auxMu.RLock()
	defer m.auxMu.RUnlock()

	// Check if we have any aux blocks
	ptr := m.currentAux.Load()
	if ptr == nil || len(*ptr) == 0 {
		return false
	}

	// Check if templates are stale (> 5 minutes old)
	if time.Since(m.lastRefresh) > 5*time.Minute {
		return false
	}

	// Check for consecutive errors
	if m.refreshErrors > 5 {
		return false
	}

	return true
}

// MinAuxBlockTime returns the shortest block time (in seconds) among all
// enabled aux chains. This is used by the job manager to determine how
// frequently aux templates should be refreshed.
// V18 FIX: When the parent chain is slow (BTC=600s) but aux chains are fast
// (DGB=15s), aux templates can go stale between parent job rebroadcasts.
// This method enables the job manager to scale its refresh interval to the
// fastest aux chain, preventing stale aux template mining.
// Returns 0 if no enabled aux chains exist.
func (m *Manager) MinAuxBlockTime() int {
	minTime := 0
	for _, cfg := range m.auxConfigs {
		if !cfg.Enabled {
			continue
		}
		bt := cfg.Coin.BlockTime()
		if bt > 0 && (minTime == 0 || bt < minTime) {
			minTime = bt
		}
	}
	return minTime
}

// GetAuxChainConfigs returns the aux chain configurations.
// AUDIT FIX: Exposes aux chain configs so the coordinator can create
// per-aux-chain payment processors with correct daemon clients.
func (m *Manager) GetAuxChainConfigs() []AuxChainConfig {
	return m.auxConfigs
}

// AuxChainCount returns the number of enabled aux chains.
func (m *Manager) AuxChainCount() int {
	count := 0
	for _, cfg := range m.auxConfigs {
		if cfg.Enabled {
			count++
		}
	}
	return count
}

// Submitter handles submitting valid aux blocks to aux chain nodes.
type Submitter struct {
	manager *Manager
	logger  *zap.SugaredLogger
	mu      sync.Mutex
}

// NewSubmitter creates a new aux block submitter.
func NewSubmitter(manager *Manager, logger *zap.Logger) *Submitter {
	return &Submitter{
		manager: manager,
		logger:  logger.Sugar(),
	}
}

// SubmitAuxBlock submits a valid aux block to the aux chain node.
//
// Uses the submitauxblock RPC with the block hash and AuxPoW proof.
//
// Parameters:
//   - ctx: Context for cancellation
//   - result: The aux block result containing block hash and proof
//
// Returns error if submission fails.
func (s *Submitter) SubmitAuxBlock(ctx context.Context, result *AuxBlockResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find the aux chain config
	var auxCfg *AuxChainConfig
	for i := range s.manager.auxConfigs {
		if s.manager.auxConfigs[i].Symbol == result.Symbol {
			auxCfg = &s.manager.auxConfigs[i]
			break
		}
	}

	if auxCfg == nil {
		result.Error = "aux chain not configured"
		return fmt.Errorf("aux chain %s not configured", result.Symbol)
	}

	if auxCfg.DaemonClient == nil {
		result.Error = "no daemon client"
		return fmt.Errorf("no daemon client for aux chain %s", result.Symbol)
	}

	// Submit using submitauxblock RPC
	// Log proof details for debugging
	auxPowLen := len(result.AuxPowHex) / 2 // hex string = 2 chars per byte
	auxPowPreview := result.AuxPowHex
	if len(auxPowPreview) > 128 {
		auxPowPreview = auxPowPreview[:128] + "..."
	}
	s.logger.Infow("Submitting aux block",
		"chain", result.Symbol,
		"height", result.Height,
		"hash", result.BlockHash,
		"auxPowBytes", auxPowLen,
		"auxPowPreview", auxPowPreview,
	)

	err := auxCfg.DaemonClient.SubmitAuxBlock(ctx, result.BlockHash, result.AuxPowHex)
	result.Submitted = true

	if err != nil {
		result.Accepted = false
		result.RejectReason = err.Error()
		s.logger.Errorw("Aux block submission failed",
			"chain", result.Symbol,
			"height", result.Height,
			"hash", result.BlockHash,
			"error", err,
		)
		return err
	}

	result.Accepted = true
	s.logger.Infow("Aux block accepted!",
		"chain", result.Symbol,
		"height", result.Height,
		"hash", result.BlockHash,
		"reward", result.CoinbaseValue,
	)

	return nil
}
