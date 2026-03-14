// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package explorer provides block explorer URL generation for multiple coins.
//
// This package generates clickable links to block explorers for blocks,
// transactions, and addresses. It supports multiple explorers per coin
// with graceful fallback when primary explorers are unavailable.
//
// Security:
// - Explorer responses are treated as untrusted input (never parsed/executed)
// - URLs are constructed from validated inputs only
// - No user input is directly concatenated into URLs
package explorer

import (
	"fmt"
	"regexp"
	"strings"
)

// ExplorerType identifies the block explorer service.
type ExplorerType string

const (
	ExplorerBlockchair   ExplorerType = "blockchair"
	ExplorerDigiExplorer ExplorerType = "digiexplorer"
	ExplorerBlockstream  ExplorerType = "blockstream"
	ExplorerBitcoinCom   ExplorerType = "bitcoin.com"
	ExplorerMempool      ExplorerType = "mempool"
	ExplorerBlockCypher  ExplorerType = "blockcypher"
)

// CoinType identifies the cryptocurrency.
type CoinType string

const (
	CoinDigiByte       CoinType = "digibyte"
	CoinBitcoin        CoinType = "bitcoin"
	CoinBitcoinCash    CoinType = "bitcoincash"
	CoinBitcoinII      CoinType = "bitcoinii"
	CoinLitecoin       CoinType = "litecoin"
	CoinDogecoin       CoinType = "dogecoin"
	CoinDigiByteScrypt CoinType = "digibyte-scrypt"
	CoinPepeCoin       CoinType = "pepecoin"
	CoinCatcoin        CoinType = "catcoin"
	CoinNamecoin       CoinType = "namecoin"
	CoinSyscoin        CoinType = "syscoin"
	CoinMyriad         CoinType = "myriadcoin"
	CoinFractalBTC     CoinType = "fractalbtc"
	CoinQBitX          CoinType = "qbitx"
)

// validBlockHash matches valid block hashes (64 hex chars)
var validBlockHash = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// validTxHash matches valid transaction hashes (64 hex chars)
var validTxHash = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// validAddress matches common cryptocurrency address patterns
// Note: This is a basic check; specific coins have more complex validation
var validAddress = regexp.MustCompile(`^[a-zA-Z0-9]{25,62}$`)

// Explorer represents a block explorer configuration.
type Explorer struct {
	Type        ExplorerType `json:"type" yaml:"type"`
	Name        string       `json:"name" yaml:"name"`
	BaseURL     string       `json:"baseUrl" yaml:"baseUrl"`
	BlockPath   string       `json:"blockPath" yaml:"blockPath"`     // Path template for block (e.g., "/block/{hash}")
	TxPath      string       `json:"txPath" yaml:"txPath"`           // Path template for transaction
	AddressPath string       `json:"addressPath" yaml:"addressPath"` // Path template for address
	HeightPath  string       `json:"heightPath" yaml:"heightPath"`   // Path template for block by height
	Enabled     bool         `json:"enabled" yaml:"enabled"`
	Priority    int          `json:"priority" yaml:"priority"` // Lower = higher priority
}

// ExplorerConfig holds explorer configuration for all coins.
type ExplorerConfig struct {
	DefaultExplorer ExplorerType            `yaml:"defaultExplorer"`
	Coins           map[CoinType][]Explorer `yaml:"coins"`
}

// ExplorerLinks contains generated explorer URLs for a block or transaction.
type ExplorerLinks struct {
	Primary   string            `json:"primary"`             // Primary explorer link
	All       map[string]string `json:"all,omitempty"`       // All available explorers
	BlockHash string            `json:"blockHash,omitempty"` // Block hash if applicable
	TxHash    string            `json:"txHash,omitempty"`    // Transaction hash if applicable
	Height    uint64            `json:"height,omitempty"`    // Block height if applicable
}

// Manager handles block explorer URL generation.
type Manager struct {
	config    *ExplorerConfig
	explorers map[CoinType][]Explorer
}

// DefaultConfig returns the default explorer configuration.
func DefaultConfig() *ExplorerConfig {
	return &ExplorerConfig{
		DefaultExplorer: ExplorerBlockchair,
		Coins: map[CoinType][]Explorer{
			CoinDigiByte: {
				{
					Type:        ExplorerDigiExplorer,
					Name:        "DigiExplorer",
					BaseURL:     "https://digiexplorer.info",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0, // Primary for DGB
				},
				{
					Type:        ExplorerBlockchair,
					Name:        "Blockchair",
					BaseURL:     "https://blockchair.com/digibyte",
					BlockPath:   "/block/{hash}",
					TxPath:      "/transaction/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    1,
				},
			},
			CoinBitcoin: {
				{
					Type:        ExplorerMempool,
					Name:        "Mempool.space",
					BaseURL:     "https://mempool.space",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0, // Primary for BTC
				},
				{
					Type:        ExplorerBlockchair,
					Name:        "Blockchair",
					BaseURL:     "https://blockchair.com/bitcoin",
					BlockPath:   "/block/{hash}",
					TxPath:      "/transaction/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    1,
				},
				{
					Type:        ExplorerBlockstream,
					Name:        "Blockstream",
					BaseURL:     "https://blockstream.info",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    2,
				},
			},
			CoinBitcoinCash: {
				{
					Type:        ExplorerBitcoinCom,
					Name:        "Bitcoin.com",
					BaseURL:     "https://explorer.bitcoin.com/bch",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0, // Primary for BCH
				},
				{
					Type:        ExplorerBlockchair,
					Name:        "Blockchair",
					BaseURL:     "https://blockchair.com/bitcoin-cash",
					BlockPath:   "/block/{hash}",
					TxPath:      "/transaction/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    1,
				},
			},
			CoinBitcoinII: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Bitcoin II Explorer",
					BaseURL:     "https://explorer.bitcoin-ii.org",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0, // Primary for BC2
				},
			},
			CoinLitecoin: {
				{
					Type:        ExplorerBlockchair,
					Name:        "Blockchair",
					BaseURL:     "https://blockchair.com/litecoin",
					BlockPath:   "/block/{hash}",
					TxPath:      "/transaction/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0, // Primary for LTC
				},
			},
			CoinDogecoin: {
				{
					Type:        ExplorerBlockchair,
					Name:        "Blockchair",
					BaseURL:     "https://blockchair.com/dogecoin",
					BlockPath:   "/block/{hash}",
					TxPath:      "/transaction/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0, // Primary for DOGE
				},
			},
			CoinDigiByteScrypt: {
				// DGB-Scrypt uses the same explorers as DGB
				{
					Type:        ExplorerDigiExplorer,
					Name:        "DigiExplorer",
					BaseURL:     "https://digiexplorer.info",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinPepeCoin: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "PepeCoin Explorer",
					BaseURL:     "https://explorer.pepecoin.org",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinCatcoin: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Catcoin Explorer",
					BaseURL:     "https://chainz.cryptoid.info/cat",
					BlockPath:   "/block.dws?{hash}.htm",
					TxPath:      "/tx.dws?{hash}.htm",
					AddressPath: "/address.dws?{address}.htm",
					HeightPath:  "/block.dws?{height}.htm",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinNamecoin: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Namecoin Explorer",
					BaseURL:     "https://www.namecoin.org/explorer",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinSyscoin: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Syscoin Explorer",
					BaseURL:     "https://sys1.bcfn.ca",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinMyriad: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Myriad Explorer",
					BaseURL:     "https://explorer.myriadcoin.org",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinFractalBTC: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Fractal Explorer",
					BaseURL:     "https://explorer.fractalbitcoin.io",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
			CoinQBitX: {
				{
					Type:        ExplorerBlockCypher,
					Name:        "Q-BitX Explorer",
					BaseURL:     "https://explorer.qbitx.org",
					BlockPath:   "/block/{hash}",
					TxPath:      "/tx/{hash}",
					AddressPath: "/address/{address}",
					HeightPath:  "/block/{height}",
					Enabled:     true,
					Priority:    0,
				},
			},
		},
	}
}

// NewManager creates a new explorer manager.
func NewManager(config *ExplorerConfig) *Manager {
	if config == nil {
		config = DefaultConfig()
	}

	m := &Manager{
		config:    config,
		explorers: make(map[CoinType][]Explorer),
	}

	// Copy and sort explorers by priority
	for coin, explorers := range config.Coins {
		enabled := make([]Explorer, 0, len(explorers))
		for _, e := range explorers {
			if e.Enabled {
				enabled = append(enabled, e)
			}
		}
		// Sort by priority (already expected to be sorted in config)
		m.explorers[coin] = enabled
	}

	return m
}

// GetBlockURL returns the primary explorer URL for a block.
func (m *Manager) GetBlockURL(coin CoinType, blockHash string) string {
	if !validBlockHash.MatchString(blockHash) {
		return ""
	}

	explorers := m.explorers[coin]
	if len(explorers) == 0 {
		return ""
	}

	return m.buildURL(explorers[0], "block", blockHash, 0)
}

// GetBlockURLByHeight returns the primary explorer URL for a block by height.
func (m *Manager) GetBlockURLByHeight(coin CoinType, height uint64) string {
	explorers := m.explorers[coin]
	if len(explorers) == 0 {
		return ""
	}

	return m.buildURL(explorers[0], "height", "", height)
}

// GetTransactionURL returns the primary explorer URL for a transaction.
func (m *Manager) GetTransactionURL(coin CoinType, txHash string) string {
	if !validTxHash.MatchString(txHash) {
		return ""
	}

	explorers := m.explorers[coin]
	if len(explorers) == 0 {
		return ""
	}

	return m.buildURL(explorers[0], "tx", txHash, 0)
}

// GetAddressURL returns the primary explorer URL for an address.
func (m *Manager) GetAddressURL(coin CoinType, address string) string {
	if !validAddress.MatchString(address) {
		return ""
	}

	explorers := m.explorers[coin]
	if len(explorers) == 0 {
		return ""
	}

	return m.buildURL(explorers[0], "address", address, 0)
}

// GetBlockLinks returns links to all configured explorers for a block.
func (m *Manager) GetBlockLinks(coin CoinType, blockHash string, height uint64) *ExplorerLinks {
	if blockHash != "" && !validBlockHash.MatchString(blockHash) {
		return nil
	}

	explorers := m.explorers[coin]
	if len(explorers) == 0 {
		return nil
	}

	links := &ExplorerLinks{
		BlockHash: blockHash,
		Height:    height,
		All:       make(map[string]string),
	}

	for i, e := range explorers {
		var url string
		if blockHash != "" {
			url = m.buildURL(e, "block", blockHash, 0)
		} else {
			url = m.buildURL(e, "height", "", height)
		}

		if url != "" {
			links.All[e.Name] = url
			if i == 0 {
				links.Primary = url
			}
		}
	}

	return links
}

// GetTransactionLinks returns links to all configured explorers for a transaction.
func (m *Manager) GetTransactionLinks(coin CoinType, txHash string) *ExplorerLinks {
	if !validTxHash.MatchString(txHash) {
		return nil
	}

	explorers := m.explorers[coin]
	if len(explorers) == 0 {
		return nil
	}

	links := &ExplorerLinks{
		TxHash: txHash,
		All:    make(map[string]string),
	}

	for i, e := range explorers {
		url := m.buildURL(e, "tx", txHash, 0)
		if url != "" {
			links.All[e.Name] = url
			if i == 0 {
				links.Primary = url
			}
		}
	}

	return links
}

// buildURL constructs an explorer URL from the template.
func (m *Manager) buildURL(e Explorer, urlType, hash string, height uint64) string {
	var path string

	switch urlType {
	case "block":
		path = strings.ReplaceAll(e.BlockPath, "{hash}", hash)
	case "tx":
		path = strings.ReplaceAll(e.TxPath, "{hash}", hash)
	case "address":
		path = strings.ReplaceAll(e.AddressPath, "{address}", hash)
	case "height":
		path = strings.ReplaceAll(e.HeightPath, "{height}", fmt.Sprintf("%d", height))
	default:
		return ""
	}

	return e.BaseURL + path
}

// GetSupportedCoins returns a list of coins with configured explorers.
func (m *Manager) GetSupportedCoins() []CoinType {
	coins := make([]CoinType, 0, len(m.explorers))
	for coin := range m.explorers {
		coins = append(coins, coin)
	}
	return coins
}

// GetExplorersForCoin returns the list of explorers configured for a coin.
func (m *Manager) GetExplorersForCoin(coin CoinType) []Explorer {
	return m.explorers[coin]
}

// CoinFromString converts a string to CoinType.
// Note: Bitcoin II must be checked before Bitcoin since "bitcoinii" contains "bitcoin"
func CoinFromString(s string) CoinType {
	switch strings.ToLower(s) {
	case "digibyte", "dgb":
		return CoinDigiByte
	case "bitcoinii", "bitcoin-ii", "bitcoin2", "bc2", "bcii":
		return CoinBitcoinII
	case "bitcoincash", "bitcoin-cash", "bch":
		return CoinBitcoinCash
	case "bitcoin", "btc":
		return CoinBitcoin
	case "litecoin", "ltc":
		return CoinLitecoin
	case "dogecoin", "doge":
		return CoinDogecoin
	case "digibyte-scrypt", "dgb-scrypt":
		return CoinDigiByteScrypt
	case "pepecoin", "pep":
		return CoinPepeCoin
	case "catcoin", "cat":
		return CoinCatcoin
	case "namecoin", "nmc":
		return CoinNamecoin
	case "syscoin", "sys":
		return CoinSyscoin
	case "myriadcoin", "myriad", "xmy":
		return CoinMyriad
	case "fractalbtc", "fractal", "fbtc":
		return CoinFractalBTC
	case "qbitx", "q-bitx", "qbx":
		return CoinQBitX
	default:
		return CoinType(strings.ToLower(s))
	}
}
