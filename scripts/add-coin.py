#!/usr/bin/env python3

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
Coin Onboarding Script for Spiral Pool

This script automates adding new cryptocurrency support with MINIMAL user interaction.
It automatically:
1. Fetches chain parameters from the coin's GitHub repository (chainparams.cpp)
2. Queries CoinGecko API for metadata (name, coingecko_id, explorer)
3. Detects the mining algorithm (SHA256d vs Scrypt) from source code
4. Auto-detects AuxPoW support and extracts chain_id, version_bit
5. Assigns unique port numbers (auto-increments from existing manifest)
6. Generates a complete Go implementation file
7. Generates a manifest entry ready for YAML insertion
8. Saves parameters to JSON for reproducibility

The goal is ZERO manual data entry when possible - just provide the symbol and GitHub URL.

Usage:
    # Fully automated (recommended)
    python scripts/add-coin.py --symbol FOO --github https://github.com/foocoin/foocoin

    # With algorithm hint (if auto-detection fails)
    python scripts/add-coin.py --symbol BAR --github https://github.com/barcoin/bar --algorithm scrypt

    # Interactive mode (for manual overrides)
    python scripts/add-coin.py --symbol BAZ --interactive

    # From saved parameters
    python scripts/add-coin.py --symbol QUX --from-json qux_params.json

Examples:
    python scripts/add-coin.py -s DOGE -g https://github.com/dogecoin/dogecoin
    python scripts/add-coin.py -s LTC -g https://github.com/litecoin-project/litecoin --algorithm scrypt
"""

import argparse
import json
import os
import re
import sys
import time
from dataclasses import dataclass, field, asdict
from pathlib import Path
from typing import Optional, List, Dict, Any, Tuple
from urllib.request import urlopen, Request
from urllib.error import URLError, HTTPError

# ═══════════════════════════════════════════════════════════════════════════════
# CONSTANTS
# ═══════════════════════════════════════════════════════════════════════════════

# Known Scrypt coins for algorithm detection fallback
KNOWN_SCRYPT_COINS = {
    "LTC", "LITECOIN", "DOGE", "DOGECOIN",
    "PEP", "PEPECOIN", "CAT", "CATCOIN",
    "VIA", "VIACOIN", "FTC", "FEATHERCOIN",
    "MONA", "MONACOIN", "NYAN", "NYANCOIN"
}

# Known SHA256d coins for algorithm detection fallback
KNOWN_SHA256D_COINS = {
    "BTC", "BITCOIN", "BCH", "BITCOINCASH", "DGB", "DIGIBYTE",
    "BC2", "BITCOINII", "NMC", "NAMECOIN", "SYS", "SYSCOIN",
    "XMY", "MYRIAD"
}

# CoinGecko API base URL
COINGECKO_API = "https://api.coingecko.com/api/v3"

# ═══════════════════════════════════════════════════════════════════════════════
# COIN PARAMETERS DATACLASS
# ═══════════════════════════════════════════════════════════════════════════════

@dataclass
class CoinParams:
    """All parameters needed to generate coin support."""
    # Basic identity
    symbol: str = ""
    name: str = ""
    algorithm: str = "sha256d"  # sha256d or scrypt
    role: str = "standalone"    # standalone, parent, or aux

    # Network ports (blockchain node)
    rpc_port: int = 0
    p2p_port: int = 0
    zmq_port: int = 0

    # Mining ports (stratum pool)
    stratum_port: int = 0       # Stratum V1 port
    stratum_v2_port: int = 0    # Stratum V2 binary protocol (V1 + 1)
    stratum_tls_port: int = 0   # Stratum TLS encrypted (V1 + 2)
    firewall_profiles: List[str] = field(default_factory=lambda: ["private", "domain"])

    # Address encoding
    p2pkh_version: int = 0      # Pay-to-PubKey-Hash version byte
    p2sh_version: int = 0       # Pay-to-Script-Hash version byte
    bech32_hrp: str = ""        # Bech32 human-readable part (empty if no segwit)

    # Chain parameters
    genesis_hash: str = ""
    block_time: int = 60        # Target block time in seconds
    coinbase_maturity: int = 100
    supports_segwit: bool = False

    # Merge mining (for AuxPoW coins)
    supports_auxpow: bool = False
    auxpow_start_height: int = 0
    chain_id: int = 0
    version_bit: int = 0x00000100
    parent_chain: str = ""      # BTC or LTC

    # Display info
    full_name: str = ""
    coingecko_id: str = ""
    explorer_url: str = ""

    def validate(self) -> List[str]:
        """Validate parameters, return list of errors."""
        errors = []

        if not self.symbol:
            errors.append("Symbol is required")
        elif not re.match(r'^[A-Z0-9]{2,10}$', self.symbol.upper()):
            errors.append("Symbol must be 2-10 alphanumeric characters")

        if not self.name:
            errors.append("Name is required")

        if self.algorithm not in ("sha256d", "scrypt"):
            errors.append(f"Algorithm must be 'sha256d' or 'scrypt', got '{self.algorithm}'")

        if self.role not in ("standalone", "parent", "aux"):
            errors.append(f"Role must be 'standalone', 'parent', or 'aux', got '{self.role}'")

        if self.rpc_port <= 0 or self.rpc_port > 65535:
            errors.append(f"Invalid RPC port: {self.rpc_port}")

        if self.p2p_port <= 0 or self.p2p_port > 65535:
            errors.append(f"Invalid P2P port: {self.p2p_port}")

        if not self.genesis_hash:
            errors.append("Genesis hash is required")
        elif len(self.genesis_hash) != 64:
            errors.append(f"Genesis hash must be 64 hex characters, got {len(self.genesis_hash)}")

        if self.block_time <= 0:
            errors.append(f"Block time must be positive, got {self.block_time}")

        if self.role == "aux":
            if not self.supports_auxpow:
                errors.append("Auxiliary coins must have supports_auxpow=true")
            if not self.parent_chain:
                errors.append("Auxiliary coins must specify parent_chain (BTC or LTC)")
            if self.chain_id <= 0:
                errors.append("Auxiliary coins must have a positive chain_id")

        if self.supports_auxpow and self.parent_chain:
            # Validate algorithm matches parent
            if self.parent_chain == "BTC" and self.algorithm != "sha256d":
                errors.append("BTC parent requires sha256d algorithm")
            if self.parent_chain == "LTC" and self.algorithm != "scrypt":
                errors.append("LTC parent requires scrypt algorithm")

        return errors


# ═══════════════════════════════════════════════════════════════════════════════
# GITHUB CHAINPARAMS PARSER
# ═══════════════════════════════════════════════════════════════════════════════

def fetch_url(url: str, timeout: float = 30.0) -> str:
    """Fetch URL content."""
    req = Request(url, headers={
        "User-Agent": "SpiralPool-CoinOnboarding/1.0",
        "Accept": "text/plain, application/json"
    })
    with urlopen(req, timeout=timeout) as response:
        return response.read().decode('utf-8')


def find_chainparams_url(github_url: str) -> Optional[str]:
    """Find the chainparams.cpp file in a GitHub repo."""
    # Normalize GitHub URL
    github_url = github_url.rstrip('/')
    if github_url.endswith('.git'):
        github_url = github_url[:-4]

    # Common paths for chainparams
    paths = [
        "src/chainparams.cpp",
        "src/kernel/chainparams.cpp",
        "src/chainparamsbase.cpp",
    ]

    # Try each path
    for path in paths:
        # Convert to raw GitHub URL
        raw_url = github_url.replace("github.com", "raw.githubusercontent.com")
        # Try main branch first, then master
        for branch in ["main", "master", "develop"]:
            url = f"{raw_url}/{branch}/{path}"
            try:
                content = fetch_url(url)
                if "chainparams" in content.lower() or "genesis" in content.lower():
                    return url
            except (URLError, HTTPError):
                continue

    return None


def parse_chainparams(content: str) -> Dict[str, Any]:
    """
    Parse chainparams.cpp to extract coin parameters.

    This is a best-effort parser - it will extract what it can find
    and leave other values empty for manual input.
    """
    params = {}

    # Extract genesis hash - look for various patterns
    genesis_patterns = [
        r'(?:genesis|hashGenesisBlock)\s*=\s*uint256\w*\s*\(\s*["\']?([0-9a-fA-F]{64})["\']?\s*\)',
        r'(?:genesis|hashGenesisBlock)\s*=\s*["\']([0-9a-fA-F]{64})["\']',
        r'consensus\.hashGenesisBlock\s*==\s*uint256\w*\s*\(\s*["\']?([0-9a-fA-F]{64})["\']?\s*\)',
        r'0x([0-9a-fA-F]{64}).*genesis',
    ]
    for pattern in genesis_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['genesis_hash'] = match.group(1).lower()
            break

    # Extract RPC port
    rpc_patterns = [
        r'nRPCPort\s*=\s*(\d+)',
        r'rpcport\s*=\s*(\d+)',
        r'DEFAULT_RPC_PORT\s*=\s*(\d+)',
    ]
    for pattern in rpc_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['rpc_port'] = int(match.group(1))
            break

    # Extract P2P port
    p2p_patterns = [
        r'nDefaultPort\s*=\s*(\d+)',
        r'nPort\s*=\s*(\d+)',
        r'DEFAULT_PORT\s*=\s*(\d+)',
    ]
    for pattern in p2p_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['p2p_port'] = int(match.group(1))
            break

    # Extract address version bytes
    p2pkh_patterns = [
        r'base58Prefixes\[PUBKEY_ADDRESS\]\s*=\s*(?:std::vector<unsigned char>\s*\(\s*1\s*,\s*)?(\d+)',
        r'PUBKEY_ADDRESS.*?(\d+)',
    ]
    for pattern in p2pkh_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['p2pkh_version'] = int(match.group(1))
            break

    p2sh_patterns = [
        r'base58Prefixes\[SCRIPT_ADDRESS\]\s*=\s*(?:std::vector<unsigned char>\s*\(\s*1\s*,\s*)?(\d+)',
        r'SCRIPT_ADDRESS.*?(\d+)',
    ]
    for pattern in p2sh_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['p2sh_version'] = int(match.group(1))
            break

    # Extract bech32 HRP
    bech32_patterns = [
        r'bech32_hrp\s*=\s*["\'](\w+)["\']',
        r'Bech32HRP\s*=\s*["\'](\w+)["\']',
    ]
    for pattern in bech32_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['bech32_hrp'] = match.group(1)
            params['supports_segwit'] = True
            break

    # Extract block time
    blocktime_patterns = [
        r'nPowTargetSpacing\s*=\s*(\d+)',
        r'nTargetSpacing\s*=\s*(\d+)',
        r'TARGET_SPACING\s*=\s*(\d+)',
    ]
    for pattern in blocktime_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['block_time'] = int(match.group(1))
            break

    # Extract coinbase maturity
    maturity_patterns = [
        r'COINBASE_MATURITY\s*=\s*(\d+)',
        r'nCoinbaseMaturity\s*=\s*(\d+)',
    ]
    for pattern in maturity_patterns:
        match = re.search(pattern, content, re.IGNORECASE)
        if match:
            params['coinbase_maturity'] = int(match.group(1))
            break

    # Check for AuxPoW support
    if 'auxpow' in content.lower() or 'BLOCK_VERSION_AUXPOW' in content:
        params['supports_auxpow'] = True

        # Try to find chain ID
        chainid_patterns = [
            r'nAuxpowChainId\s*=\s*(\d+)',
            r'AUXPOW_CHAIN_ID\s*=\s*(\d+)',
            r'chainId\s*=\s*(\d+)',
        ]
        for pattern in chainid_patterns:
            match = re.search(pattern, content, re.IGNORECASE)
            if match:
                params['chain_id'] = int(match.group(1))
                break

        # Try to find AuxPoW start height
        auxstart_patterns = [
            r'nAuxpowStartHeight\s*=\s*(\d+)',
            r'AUXPOW_START_HEIGHT\s*=\s*(\d+)',
        ]
        for pattern in auxstart_patterns:
            match = re.search(pattern, content, re.IGNORECASE)
            if match:
                params['auxpow_start_height'] = int(match.group(1))
                break

    return params


# ═══════════════════════════════════════════════════════════════════════════════
# ALGORITHM DETECTION
# ═══════════════════════════════════════════════════════════════════════════════

def detect_algorithm_from_source(content: str, symbol: str = "") -> Optional[str]:
    """
    Detect mining algorithm from source code.

    Checks multiple indicators:
    1. Explicit scrypt references in code
    2. N/r/p parameters (Scrypt-specific)
    3. Known coin symbols
    4. POW_SCRYPT or similar constants
    """
    content_lower = content.lower()

    # Check for explicit Scrypt indicators
    scrypt_indicators = [
        "scrypt",
        "scrypt_1024_1_1",
        "scrypthash",
        "scrypt_hash",
        "n = 1024",  # Scrypt N parameter
        "scrypt-jane",
        "pow_scrypt",
    ]

    scrypt_score = sum(1 for indicator in scrypt_indicators if indicator in content_lower)

    # Check for SHA256d indicators
    sha256_indicators = [
        "sha256d",
        "sha256_hash",
        "double sha256",
        "pow_sha256",
        "hashblock = sha256",
    ]

    sha256_score = sum(1 for indicator in sha256_indicators if indicator in content_lower)

    # Use symbol as fallback
    symbol_upper = symbol.upper()
    if symbol_upper in KNOWN_SCRYPT_COINS:
        scrypt_score += 2
    if symbol_upper in KNOWN_SHA256D_COINS:
        sha256_score += 2

    if scrypt_score > sha256_score:
        return "scrypt"
    elif sha256_score > scrypt_score:
        return "sha256d"

    # Default based on symbol if no clear winner
    if symbol_upper in KNOWN_SCRYPT_COINS:
        return "scrypt"

    return None  # Couldn't determine


def detect_auxpow_from_source(content: str) -> Dict[str, Any]:
    """
    Detect AuxPoW (merge mining) support from source code.

    Extracts:
    - Whether AuxPoW is supported
    - Chain ID
    - Version bit
    - AuxPoW start height
    """
    result = {
        "supports_auxpow": False,
        "chain_id": 0,
        "version_bit": 0x100,
        "auxpow_start_height": 0,
    }

    content_lower = content.lower()

    # Check for AuxPoW support
    auxpow_indicators = [
        "auxpow", "auxblock", "aux_pow", "merge_mining", "merged mining",
        "block_version_auxpow", "version_auxpow"
    ]

    if any(indicator in content_lower for indicator in auxpow_indicators):
        result["supports_auxpow"] = True

        # Try to extract chain ID - multiple patterns
        chainid_patterns = [
            r'nAuxpowChainId\s*=\s*(\d+)',
            r'AUXPOW_CHAIN_ID\s*=\s*(\d+)',
            r'nChainId\s*=\s*(\d+)',
            r'pchAuxpowChainId\s*=\s*(\d+)',
            r'consensus\.nAuxpowChainId\s*=\s*(\d+)',
            r'chainid\s*=\s*0x([0-9a-fA-F]+)',
            r'chainid\s*=\s*(\d+)',
            # Hex format
            r'nAuxpowChainId\s*=\s*0x([0-9a-fA-F]+)',
        ]

        for pattern in chainid_patterns:
            match = re.search(pattern, content, re.IGNORECASE)
            if match:
                value = match.group(1)
                if pattern.endswith('([0-9a-fA-F]+)'):
                    result["chain_id"] = int(value, 16)
                else:
                    result["chain_id"] = int(value)
                break

        # Try to extract version bit
        versionbit_patterns = [
            r'BLOCK_VERSION_AUXPOW\s*=\s*0x([0-9a-fA-F]+)',
            r'nAuxpowVersionBit\s*=\s*0x([0-9a-fA-F]+)',
            r'VERSION_AUXPOW\s*=\s*0x([0-9a-fA-F]+)',
            r'AUXPOW_VERSION\s*=\s*0x([0-9a-fA-F]+)',
        ]

        for pattern in versionbit_patterns:
            match = re.search(pattern, content, re.IGNORECASE)
            if match:
                result["version_bit"] = int(match.group(1), 16)
                break

        # Try to extract start height
        startht_patterns = [
            r'nAuxpowStartHeight\s*=\s*(\d+)',
            r'AUXPOW_START_HEIGHT\s*=\s*(\d+)',
            r'consensus\.nAuxpowStartHeight\s*=\s*(\d+)',
        ]

        for pattern in startht_patterns:
            match = re.search(pattern, content, re.IGNORECASE)
            if match:
                result["auxpow_start_height"] = int(match.group(1))
                break

    return result


# ═══════════════════════════════════════════════════════════════════════════════
# COINGECKO API INTEGRATION
# ═══════════════════════════════════════════════════════════════════════════════

def fetch_coingecko_data(symbol: str) -> Dict[str, Any]:
    """
    Fetch coin metadata from CoinGecko API.

    Returns:
        Dict with: name, coingecko_id, explorer_url, or empty dict if not found.
    """
    result = {}

    try:
        # Search for the coin by symbol
        search_url = f"{COINGECKO_API}/search?query={symbol.lower()}"
        req = Request(search_url, headers={
            "User-Agent": "SpiralPool-CoinOnboarding/1.0",
            "Accept": "application/json"
        })

        with urlopen(req, timeout=15.0) as response:
            data = json.loads(response.read().decode('utf-8'))

        # Find matching coin by symbol
        coins = data.get("coins", [])
        for coin in coins:
            if coin.get("symbol", "").upper() == symbol.upper():
                result["coingecko_id"] = coin.get("id", "")
                result["name"] = coin.get("name", "")
                break

        # If we found a coingecko_id, get detailed info for explorer URL
        if result.get("coingecko_id"):
            time.sleep(1.5)  # Rate limiting
            detail_url = f"{COINGECKO_API}/coins/{result['coingecko_id']}"
            req = Request(detail_url, headers={
                "User-Agent": "SpiralPool-CoinOnboarding/1.0",
                "Accept": "application/json"
            })

            with urlopen(req, timeout=15.0) as response:
                detail_data = json.loads(response.read().decode('utf-8'))

            # Get block explorer URL
            links = detail_data.get("links", {})
            explorers = links.get("blockchain_site", [])
            for explorer in explorers:
                if explorer:  # First non-empty explorer
                    result["explorer_url"] = explorer
                    break

    except (URLError, HTTPError, json.JSONDecodeError, KeyError) as e:
        print(f"  Note: CoinGecko lookup failed: {e}")

    return result


# ═══════════════════════════════════════════════════════════════════════════════
# MANIFEST PORT ALLOCATION
# ═══════════════════════════════════════════════════════════════════════════════

def get_next_available_ports(manifest_path: Path) -> Dict[str, int]:
    """
    Read existing manifest and determine next available ports.

    Port allocation scheme:
    - Stratum V1: 3333+ (increment by 1000 per algorithm group)
    - Stratum V2: V1 + 1
    - Stratum TLS: V1 + 2
    - RPC: based on coin defaults
    - ZMQ: 28000 + (RPC % 1000)
    """
    used_stratum_ports = set()

    if manifest_path.exists():
        try:
            import yaml
            with open(manifest_path) as f:
                manifest = yaml.safe_load(f)

            if manifest and "coins" in manifest:
                for coin in manifest["coins"]:
                    # Extract stratum ports from 'mining' section (new schema)
                    mining = coin.get("mining", {})
                    if "stratum_port" in mining:
                        used_stratum_ports.add(mining["stratum_port"])
                    # Also check legacy 'network' section for backwards compatibility
                    network = coin.get("network", {})
                    for key in ["stratum_v1_port", "stratum_port"]:
                        if key in network:
                            used_stratum_ports.add(network[key])
        except Exception:
            pass

    # Find next available base port (in thousands)
    # SHA256d: 3333, 4333, 5333, 6333, 14335-17337
    # Scrypt: 7333, 8335, 9335, 10335-13336

    # Start from 18335 for new coins (after all existing allocations)
    base = 18335
    while base in used_stratum_ports or (base + 1) in used_stratum_ports:
        base += 1000

    return {
        "stratum_v1": base,
        "stratum_v2": base + 1,
        "stratum_tls": base + 2,
    }


# ═══════════════════════════════════════════════════════════════════════════════
# GO CODE GENERATOR
# ═══════════════════════════════════════════════════════════════════════════════

def generate_go_code(params: CoinParams) -> str:
    """Generate Go implementation file for the coin."""

    symbol_lower = params.symbol.lower()
    symbol_upper = params.symbol.upper()
    name_title = params.name.title().replace(" ", "")

    # Determine base coin to embed based on algorithm
    if params.algorithm == "scrypt":
        base_import = ""
        base_embed = ""
        hash_func = "scryptHash"
        hash_import = '"github.com/spiralpool/stratum/internal/crypto/scrypt"'
    else:
        base_import = ""
        base_embed = ""
        hash_func = "sha256dHash"
        hash_import = '"crypto/sha256"'

    # Build AuxPoW interface implementation if needed
    auxpow_methods = ""
    auxpow_interface = ""
    if params.supports_auxpow:
        auxpow_interface = f"""
// Ensure {name_title}Coin implements AuxPowCoin interface
var _ AuxPowCoin = (*{name_title}Coin)(nil)
"""
        auxpow_methods = f'''
// ═══════════════════════════════════════════════════════════════════════════════
// AUXPOW INTERFACE IMPLEMENTATION
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) SupportsAuxPow() bool {{
	return true
}}

func (c *{name_title}Coin) AuxPowStartHeight() uint64 {{
	return {params.auxpow_start_height}
}}

func (c *{name_title}Coin) ChainID() int32 {{
	return {hex(params.chain_id)} // {params.chain_id} decimal
}}

func (c *{name_title}Coin) AuxPowVersionBit() uint32 {{
	return {hex(params.version_bit)}
}}

func (c *{name_title}Coin) ParseAuxBlockResponse(response map[string]interface{{}}) (*AuxBlock, error) {{
	// TEMPLATE STUB: Requires manual implementation based on coin's getauxblock RPC response format.
	// This is a code generation template - the generated file must be completed before use.
	// Refer to existing coin implementations (e.g., namecoin.go) for reference.
	return nil, fmt.Errorf("{symbol_upper} AuxPoW parsing requires implementation - this is a generated template")
}}

func (c *{name_title}Coin) SerializeAuxPowProof(proof *AuxPowProof) ([]byte, error) {{
	// TEMPLATE STUB: Requires manual implementation for AuxPoW proof serialization.
	// This is a code generation template - the generated file must be completed before use.
	// Refer to existing coin implementations (e.g., namecoin.go) for reference.
	return nil, fmt.Errorf("{symbol_upper} AuxPoW serialization requires implementation - this is a generated template")
}}
'''

    # Build parent chain methods if this is a parent
    parent_methods = ""
    if params.role == "parent":
        parent_methods = f'''
// ═══════════════════════════════════════════════════════════════════════════════
// PARENT CHAIN INTERFACE IMPLEMENTATION
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) CanBeParentFor(auxAlgorithm string) bool {{
	return auxAlgorithm == "{params.algorithm}"
}}

func (c *{name_title}Coin) CoinbaseAuxMarker() []byte {{
	return []byte{{0xfa, 0xbe, 0x6d, 0x6d}} // "fabe mm"
}}

func (c *{name_title}Coin) MaxCoinbaseAuxSize() int {{
	return 100 // Adjust based on coin's coinbase script size limits
}}
'''

    # Generate the Go file
    code = f'''// Package coin provides {params.name} ({symbol_upper}) implementation.
//
// GENERATED BY: scripts/add-coin.py
// REVIEW REQUIRED: This is a template - verify all values against official sources.
//
// Chain Parameters:
//   - Algorithm: {params.algorithm}
//   - Block Time: {params.block_time} seconds
//   - Default RPC Port: {params.rpc_port}
//   - Default P2P Port: {params.p2p_port}
{"//   - AuxPoW Chain ID: " + str(params.chain_id) if params.supports_auxpow else ""}
package coin

import (
	"encoding/binary"
	"fmt"
	"math/big"
	{hash_import}
)
{auxpow_interface}
// {name_title}Coin implements the Coin interface for {params.name}.
type {name_title}Coin struct{{}}

// New{name_title}Coin creates a new {params.name} coin instance.
func New{name_title}Coin() *{name_title}Coin {{
	return &{name_title}Coin{{}}
}}

// ═══════════════════════════════════════════════════════════════════════════════
// COIN IDENTITY
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) Symbol() string {{
	return "{symbol_upper}"
}}

func (c *{name_title}Coin) Name() string {{
	return "{params.name}"
}}

// ═══════════════════════════════════════════════════════════════════════════════
// NETWORK PARAMETERS
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) DefaultRPCPort() int {{
	return {params.rpc_port}
}}

func (c *{name_title}Coin) DefaultP2PPort() int {{
	return {params.p2p_port}
}}

func (c *{name_title}Coin) P2PKHVersionByte() byte {{
	return {hex(params.p2pkh_version)} // {params.p2pkh_version} decimal
}}

func (c *{name_title}Coin) P2SHVersionByte() byte {{
	return {hex(params.p2sh_version)} // {params.p2sh_version} decimal
}}

func (c *{name_title}Coin) Bech32HRP() string {{
	return "{params.bech32_hrp}"
}}

// ═══════════════════════════════════════════════════════════════════════════════
// MINING CHARACTERISTICS
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) Algorithm() string {{
	return "{params.algorithm}"
}}

func (c *{name_title}Coin) SupportsSegWit() bool {{
	return {str(params.supports_segwit).lower()}
}}

func (c *{name_title}Coin) BlockTime() int {{
	return {params.block_time}
}}

func (c *{name_title}Coin) MinCoinbaseScriptLen() int {{
	return 2 // BIP34 minimum
}}

func (c *{name_title}Coin) CoinbaseMaturity() int {{
	return {params.coinbase_maturity}
}}

// ═══════════════════════════════════════════════════════════════════════════════
// CHAIN VERIFICATION
// ═══════════════════════════════════════════════════════════════════════════════

const {name_title}GenesisBlockHash = "{params.genesis_hash}"

func (c *{name_title}Coin) GenesisBlockHash() string {{
	return {name_title}GenesisBlockHash
}}

func (c *{name_title}Coin) VerifyGenesisBlock(nodeGenesisHash string) error {{
	if nodeGenesisHash != {name_title}GenesisBlockHash {{
		return fmt.Errorf("{symbol_upper} genesis block mismatch: expected %s, got %s",
			{name_title}GenesisBlockHash, nodeGenesisHash)
	}}
	return nil
}}

// ═══════════════════════════════════════════════════════════════════════════════
// ADDRESS VALIDATION
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) ValidateAddress(address string) error {{
	// TEMPLATE STUB: Basic length validation only - full validation requires manual implementation.
	// This is a code generation template - enhance with proper address format validation.
	// Refer to existing coin implementations (e.g., bitcoin.go, digibyte.go) for reference.
	if len(address) < 26 || len(address) > 62 {{
		return fmt.Errorf("invalid {symbol_upper} address length: %d", len(address))
	}}
	return nil
}}

func (c *{name_title}Coin) DecodeAddress(address string) ([]byte, AddressType, error) {{
	// TEMPLATE STUB: Requires manual implementation for address decoding.
	// This is a code generation template - the generated file must be completed before use.
	// Refer to existing coin implementations (e.g., bitcoin.go, digibyte.go) for reference.
	return nil, AddressTypeUnknown, fmt.Errorf("{symbol_upper} address decoding requires implementation - this is a generated template")
}}

// ═══════════════════════════════════════════════════════════════════════════════
// COINBASE CONSTRUCTION
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) BuildCoinbaseScript(params CoinbaseParams) ([]byte, error) {{
	// TEMPLATE STUB: Requires manual implementation for coinbase script construction.
	// This is a code generation template - the generated file must be completed before use.
	// Refer to existing coin implementations (e.g., bitcoin.go, digibyte.go) for reference.
	return nil, fmt.Errorf("{symbol_upper} coinbase construction requires implementation - this is a generated template")
}}

// ═══════════════════════════════════════════════════════════════════════════════
// BLOCK HEADER OPERATIONS
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) SerializeBlockHeader(header *BlockHeader) []byte {{
	buf := make([]byte, 80)
	binary.LittleEndian.PutUint32(buf[0:4], header.Version)
	copy(buf[4:36], header.PreviousBlockHash)
	copy(buf[36:68], header.MerkleRoot)
	binary.LittleEndian.PutUint32(buf[68:72], header.Timestamp)
	binary.LittleEndian.PutUint32(buf[72:76], header.Bits)
	binary.LittleEndian.PutUint32(buf[76:80], header.Nonce)
	return buf
}}

func (c *{name_title}Coin) HashBlockHeader(serialized []byte) []byte {{
	{"// Scrypt hash for " + params.algorithm + " coins" if params.algorithm == "scrypt" else "// SHA256d hash"}
	{"return scrypt.Hash(serialized)" if params.algorithm == "scrypt" else "h := sha256.Sum256(serialized)\n\th2 := sha256.Sum256(h[:])\n\treturn h2[:]"}
}}

// ═══════════════════════════════════════════════════════════════════════════════
// DIFFICULTY CALCULATIONS
// ═══════════════════════════════════════════════════════════════════════════════

func (c *{name_title}Coin) TargetFromBits(bits uint32) *big.Int {{
	// Standard Bitcoin compact target format
	exponent := bits >> 24
	mantissa := bits & 0x007fffff

	if exponent <= 3 {{
		mantissa >>= 8 * (3 - exponent)
		return big.NewInt(int64(mantissa))
	}}

	target := big.NewInt(int64(mantissa))
	target.Lsh(target, uint(8*(exponent-3)))
	return target
}}

func (c *{name_title}Coin) DifficultyFromTarget(target *big.Int) float64 {{
	if target.Sign() == 0 {{
		return 0
	}}

	// diff1 target for {params.algorithm}
	diff1 := new(big.Int)
	{"diff1.SetString(\"00000000ffff0000000000000000000000000000000000000000000000000000\", 16)" if params.algorithm == "sha256d" else "diff1.SetString(\"0000ffff00000000000000000000000000000000000000000000000000000000\", 16)"}

	diff1Float := new(big.Float).SetInt(diff1)
	targetFloat := new(big.Float).SetInt(target)

	difficulty := new(big.Float).Quo(diff1Float, targetFloat)
	result, _ := difficulty.Float64()
	return result
}}

func (c *{name_title}Coin) ShareDifficultyMultiplier() float64 {{
	return 1.0
}}
{auxpow_methods}{parent_methods}
// ═══════════════════════════════════════════════════════════════════════════════
// REGISTRATION
// ═══════════════════════════════════════════════════════════════════════════════

func init() {{
	Register("{symbol_upper}", func() Coin {{ return New{name_title}Coin() }})
}}
'''

    return code


# ═══════════════════════════════════════════════════════════════════════════════
# MANIFEST ENTRY GENERATOR
# ═══════════════════════════════════════════════════════════════════════════════

def generate_manifest_entry(params: CoinParams, stratum_ports: Dict[str, int]) -> str:
    """Generate YAML manifest entry for the coin."""

    # Calculate ZMQ port if not set (convention: 28000 + last 3 digits of RPC port)
    zmq_port = params.zmq_port if params.zmq_port else 28000 + (params.rpc_port % 1000)

    # Use provided stratum ports or calculate from params
    stratum_v1 = params.stratum_port if params.stratum_port else stratum_ports["stratum_v1"]
    stratum_v2 = params.stratum_v2_port if params.stratum_v2_port else stratum_ports["stratum_v2"]
    stratum_tls = params.stratum_tls_port if params.stratum_tls_port else stratum_ports["stratum_tls"]

    # Format firewall profiles
    firewall_profiles_yaml = ""
    if params.firewall_profiles and params.firewall_profiles != ["private", "domain"]:
        # Only include if different from default
        profiles_list = "\n".join(f"        - {p}" for p in params.firewall_profiles)
        firewall_profiles_yaml = f"\n      firewall_profiles:\n{profiles_list}"

    entry = f'''  # ─────────────────────────────────────────────────────────────────────────────
  # {params.name} ({params.symbol.upper()})
  # GENERATED BY: scripts/add-coin.py
  # REVIEW REQUIRED: Verify all values against official sources
  # ─────────────────────────────────────────────────────────────────────────────

  - symbol: {params.symbol.upper()}
    name: {params.name}
    algorithm: {params.algorithm}
    role: {params.role}

    network:
      rpc_port: {params.rpc_port}
      p2p_port: {params.p2p_port}
      zmq_port: {zmq_port}

    mining:
      stratum_port: {stratum_v1}
      stratum_v2_port: {stratum_v2}
      stratum_tls_port: {stratum_tls}{firewall_profiles_yaml}

    address:
      p2pkh_version: {hex(params.p2pkh_version)}  # {params.p2pkh_version} decimal
      p2sh_version: {hex(params.p2sh_version)}  # {params.p2sh_version} decimal
      bech32_hrp: {f'"{params.bech32_hrp}"' if params.bech32_hrp else 'null'}

    chain:
      genesis_hash: "{params.genesis_hash}"
      block_time: {params.block_time}
      coinbase_maturity: {params.coinbase_maturity}
      supports_segwit: {str(params.supports_segwit).lower()}
      min_coinbase_script_len: 2
'''

    if params.supports_auxpow:
        entry += f'''
    merge_mining:
      supports_auxpow: true
      auxpow_start_height: {params.auxpow_start_height}
      chain_id: {params.chain_id}              # CONSENSUS-CRITICAL ({hex(params.chain_id)})
      version_bit: {hex(params.version_bit)}     # CONSENSUS-CRITICAL
      parent_chain: {params.parent_chain}
'''
    elif params.role == "parent":
        entry += f'''
    merge_mining:
      can_be_parent: true
      supported_aux_algorithms:
        - {params.algorithm}
'''

    entry += f'''
    display:
      full_name: "{params.full_name or params.name}"
      short_name: "{params.symbol.upper()}"
      coingecko_id: {f'"{params.coingecko_id}"' if params.coingecko_id else 'null'}
      explorer_url: {f'"{params.explorer_url}"' if params.explorer_url else 'null'}
'''

    return entry


# ═══════════════════════════════════════════════════════════════════════════════
# INTERACTIVE MODE
# ═══════════════════════════════════════════════════════════════════════════════

def prompt(text: str, default: Any = None, required: bool = False) -> str:
    """Prompt user for input with optional default."""
    if default is not None:
        text = f"{text} [{default}]: "
    else:
        text = f"{text}: "

    while True:
        value = input(text).strip()
        if not value and default is not None:
            return str(default)
        if not value and required:
            print("  This field is required.")
            continue
        return value


def prompt_int(text: str, default: Optional[int] = None, required: bool = False) -> int:
    """Prompt for integer input."""
    while True:
        value = prompt(text, default, required)
        if not value and not required:
            return 0
        try:
            return int(value)
        except ValueError:
            print("  Please enter a valid number.")


def prompt_bool(text: str, default: bool = False) -> bool:
    """Prompt for yes/no input."""
    default_str = "Y/n" if default else "y/N"
    value = prompt(f"{text} ({default_str})", "").lower()
    if not value:
        return default
    return value in ('y', 'yes', 'true', '1')


def prompt_hex(text: str, default: Optional[int] = None) -> int:
    """Prompt for hex or decimal input."""
    while True:
        value = prompt(text, hex(default) if default else None)
        if not value:
            return default or 0
        try:
            if value.startswith('0x'):
                return int(value, 16)
            return int(value)
        except ValueError:
            print("  Please enter a valid number (decimal or 0x hex).")


def interactive_mode(params: CoinParams) -> CoinParams:
    """Interactively prompt for all coin parameters."""

    print("\n" + "=" * 70)
    print("COIN ONBOARDING - Interactive Mode")
    print("=" * 70)
    print("\nEnter coin parameters. Press Enter to accept defaults in [brackets].\n")

    # Basic identity
    print("── Basic Identity ──")
    params.symbol = prompt("Symbol (e.g., FOO)", params.symbol, required=True).upper()
    params.name = prompt("Full name (e.g., FooCoin)", params.name, required=True)
    params.algorithm = prompt("Algorithm (sha256d/scrypt)", params.algorithm or "sha256d")
    params.role = prompt("Role (standalone/parent/aux)", params.role or "standalone")

    # Network ports
    print("\n── Network Ports ──")
    params.rpc_port = prompt_int("RPC port", params.rpc_port or 8332, required=True)
    params.p2p_port = prompt_int("P2P port", params.p2p_port or 8333, required=True)
    params.zmq_port = prompt_int("ZMQ port", params.zmq_port or (28000 + params.rpc_port % 1000))

    # Address encoding
    print("\n── Address Encoding ──")
    params.p2pkh_version = prompt_hex("P2PKH version byte", params.p2pkh_version or 0)
    params.p2sh_version = prompt_hex("P2SH version byte", params.p2sh_version or 5)
    params.supports_segwit = prompt_bool("Supports SegWit?", params.supports_segwit)
    if params.supports_segwit:
        params.bech32_hrp = prompt("Bech32 HRP (e.g., 'bc', 'ltc')", params.bech32_hrp)

    # Chain parameters
    print("\n── Chain Parameters ──")
    params.genesis_hash = prompt("Genesis block hash (64 hex chars)", params.genesis_hash, required=True)
    params.block_time = prompt_int("Target block time (seconds)", params.block_time or 60, required=True)
    params.coinbase_maturity = prompt_int("Coinbase maturity (blocks)", params.coinbase_maturity or 100)

    # Merge mining
    if params.role == "aux" or prompt_bool("\nSupports AuxPoW (merge mining)?", params.supports_auxpow):
        print("\n── Merge Mining (AuxPoW) ──")
        params.supports_auxpow = True
        params.role = "aux" if params.role != "parent" else params.role
        params.chain_id = prompt_int("Chain ID (CONSENSUS-CRITICAL)", params.chain_id, required=True)
        params.auxpow_start_height = prompt_int("AuxPoW start height", params.auxpow_start_height)
        params.version_bit = prompt_hex("Version bit", params.version_bit or 0x100)

        if params.algorithm == "sha256d":
            params.parent_chain = "BTC"
        elif params.algorithm == "scrypt":
            params.parent_chain = "LTC"
        else:
            params.parent_chain = prompt("Parent chain (BTC/LTC)", params.parent_chain)

    # Display info
    print("\n── Display Information ──")
    params.full_name = prompt("Display name", params.full_name or params.name)
    params.coingecko_id = prompt("CoinGecko ID (optional)", params.coingecko_id)
    params.explorer_url = prompt("Block explorer URL (optional)", params.explorer_url)

    return params


# ═══════════════════════════════════════════════════════════════════════════════
# COMPREHENSIVE AUTOMATED ONBOARDING
# ═══════════════════════════════════════════════════════════════════════════════

def auto_onboard_coin(symbol: str, github_url: str, algorithm_hint: Optional[str] = None) -> Tuple[CoinParams, List[str]]:
    """
    Fully automated coin onboarding with minimal user interaction.

    This function:
    1. Fetches chainparams.cpp from GitHub
    2. Parses all available parameters
    3. Detects algorithm automatically
    4. Detects AuxPoW support and extracts chain_id
    5. Fetches metadata from CoinGecko
    6. Returns complete CoinParams and list of warnings

    Args:
        symbol: Coin ticker symbol (e.g., "DOGE")
        github_url: GitHub repository URL
        algorithm_hint: Optional algorithm override ("sha256d" or "scrypt")

    Returns:
        Tuple of (CoinParams, warnings_list)
    """
    params = CoinParams(symbol=symbol.upper())
    warnings = []
    source_content = ""

    print(f"\n{'='*70}")
    print(f"  AUTOMATED ONBOARDING: {symbol.upper()}")
    print(f"{'='*70}")

    # Step 1: Fetch chainparams.cpp
    print(f"\n[1/5] Fetching chainparams from GitHub...")
    url = find_chainparams_url(github_url)
    if url:
        print(f"      Found: {url}")
        source_content = fetch_url(url)
        extracted = parse_chainparams(source_content)
        print(f"      Extracted {len(extracted)} parameters from chainparams.cpp")
        for key, value in extracted.items():
            setattr(params, key, value)
            print(f"        {key}: {value}")
    else:
        warnings.append("Could not find chainparams.cpp - some values may need manual entry")
        print(f"      WARNING: Could not find chainparams.cpp")

    # Step 2: Detect algorithm
    print(f"\n[2/5] Detecting mining algorithm...")
    if algorithm_hint:
        params.algorithm = algorithm_hint
        print(f"      Using provided algorithm: {algorithm_hint}")
    elif source_content:
        detected = detect_algorithm_from_source(source_content, symbol)
        if detected:
            params.algorithm = detected
            print(f"      Auto-detected: {detected}")
        else:
            params.algorithm = "sha256d"
            warnings.append("Could not auto-detect algorithm, defaulting to sha256d")
            print(f"      WARNING: Could not detect, defaulting to sha256d")
    else:
        # Use symbol-based detection
        if symbol.upper() in KNOWN_SCRYPT_COINS:
            params.algorithm = "scrypt"
            print(f"      Known Scrypt coin: {symbol}")
        else:
            params.algorithm = "sha256d"
            print(f"      Defaulting to sha256d")

    # Step 3: Detect AuxPoW
    print(f"\n[3/5] Checking for AuxPoW (merge mining) support...")
    if source_content:
        auxpow_data = detect_auxpow_from_source(source_content)
        if auxpow_data["supports_auxpow"]:
            params.supports_auxpow = True
            params.role = "aux"
            params.chain_id = auxpow_data["chain_id"]
            params.version_bit = auxpow_data["version_bit"]
            params.auxpow_start_height = auxpow_data["auxpow_start_height"]
            params.parent_chain = "LTC" if params.algorithm == "scrypt" else "BTC"
            print(f"      AuxPoW DETECTED!")
            print(f"        chain_id: {params.chain_id}")
            print(f"        version_bit: {hex(params.version_bit)}")
            print(f"        start_height: {params.auxpow_start_height}")
            print(f"        parent_chain: {params.parent_chain}")
            if params.chain_id == 0:
                warnings.append("CRITICAL: chain_id=0 detected - this is likely incorrect, verify manually!")
        else:
            print(f"      No AuxPoW support detected")
    else:
        print(f"      Skipped (no source code available)")

    # Step 4: Fetch CoinGecko metadata
    print(f"\n[4/5] Fetching metadata from CoinGecko...")
    cg_data = fetch_coingecko_data(symbol)
    if cg_data:
        if cg_data.get("name") and not params.name:
            params.name = cg_data["name"]
            print(f"      Name: {params.name}")
        if cg_data.get("coingecko_id"):
            params.coingecko_id = cg_data["coingecko_id"]
            print(f"      CoinGecko ID: {params.coingecko_id}")
        if cg_data.get("explorer_url"):
            params.explorer_url = cg_data["explorer_url"]
            print(f"      Explorer: {params.explorer_url}")
    else:
        print(f"      Not found on CoinGecko (this is OK for smaller coins)")

    # Step 5: Set defaults for missing values
    print(f"\n[5/5] Setting defaults for missing values...")
    if not params.name:
        params.name = symbol.title() + "coin"
        print(f"      Name: {params.name} (generated)")

    if not params.full_name:
        params.full_name = params.name

    if params.rpc_port == 0:
        # Generate a default RPC port based on symbol hash
        params.rpc_port = 8000 + (hash(symbol) % 1000)
        warnings.append(f"RPC port auto-generated ({params.rpc_port}) - verify against official docs")
        print(f"      RPC port: {params.rpc_port} (auto-generated)")

    if params.p2p_port == 0:
        params.p2p_port = params.rpc_port + 1
        print(f"      P2P port: {params.p2p_port} (auto-generated)")

    if params.zmq_port == 0:
        params.zmq_port = 28000 + (params.rpc_port % 1000)
        print(f"      ZMQ port: {params.zmq_port} (auto-generated)")

    if params.block_time == 0:
        params.block_time = 60  # Default to 1 minute
        warnings.append("Block time defaulted to 60s - verify against official docs")

    if params.coinbase_maturity == 0:
        params.coinbase_maturity = 100

    # Validate and report
    print(f"\n{'='*70}")
    print(f"  EXTRACTION SUMMARY")
    print(f"{'='*70}")

    errors = params.validate()
    if errors:
        print(f"\n  VALIDATION ERRORS (must fix before proceeding):")
        for err in errors:
            print(f"    - {err}")
    elif warnings:
        print(f"\n  WARNINGS (review recommended):")
        for warn in warnings:
            print(f"    - {warn}")
    else:
        print(f"\n  All parameters extracted successfully!")

    return params, warnings


# ═══════════════════════════════════════════════════════════════════════════════
# MAIN
# ═══════════════════════════════════════════════════════════════════════════════

def main():
    parser = argparse.ArgumentParser(
        description="Add new coin support to Spiral Pool (fully automated)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
╔══════════════════════════════════════════════════════════════════════════════╗
║  FULLY AUTOMATED MODE (recommended)                                          ║
╠══════════════════════════════════════════════════════════════════════════════╣
║  Just provide the symbol and GitHub URL - the script handles everything:     ║
║                                                                              ║
║    python scripts/add-coin.py -s DOGE \\                                      ║
║      -g https://github.com/dogecoin/dogecoin                                ║
║                                                                              ║
║  The script will automatically:                                              ║
║    1. Download and parse chainparams.cpp                                     ║
║    2. Detect the mining algorithm (SHA256d vs Scrypt)                        ║
║    3. Detect AuxPoW support and extract chain_id                             ║
║    4. Fetch metadata from CoinGecko (name, explorer URL)                     ║
║    5. Generate complete Go implementation                                    ║
║    6. Generate manifest YAML entry                                           ║
╚══════════════════════════════════════════════════════════════════════════════╝

Examples:
  %(prog)s --symbol FOO --github https://github.com/foocoin/foocoin
  %(prog)s --symbol BAR --github https://github.com/barcoin/bar --algorithm scrypt
  %(prog)s --symbol BAZ --interactive
  %(prog)s --symbol QUX --from-json qux_params.json
        """
    )

    parser.add_argument("--symbol", "-s", required=True, help="Coin symbol (e.g., FOO)")
    parser.add_argument("--name", "-n", help="Coin name (e.g., FooCoin) - auto-detected if not provided")
    parser.add_argument("--algorithm", "-a", choices=["sha256d", "scrypt"],
                        help="Mining algorithm - auto-detected from source if not provided")
    parser.add_argument("--github", "-g", help="GitHub repository URL (enables full automation)")
    parser.add_argument("--interactive", "-i", action="store_true",
                        help="Interactive mode for manual parameter entry")
    parser.add_argument("--from-json", "-j", help="Load parameters from previously saved JSON file")
    parser.add_argument("--output-dir", "-o", help="Output directory (default: auto-detect)")
    parser.add_argument("--dry-run", action="store_true",
                        help="Preview output without writing files")
    parser.add_argument("--skip-coingecko", action="store_true",
                        help="Skip CoinGecko API lookup")
    parser.add_argument("--force", "-f", action="store_true",
                        help="Overwrite existing files without prompting")

    args = parser.parse_args()

    # Determine output directory early (needed for port allocation)
    script_dir = Path(__file__).parent
    if args.output_dir:
        output_dir = Path(args.output_dir)
    else:
        output_dir = script_dir.parent

    manifest_file = output_dir / "config" / "coins.manifest.yaml"

    # Initialize params based on mode
    params = None
    warnings = []

    if args.from_json:
        # Load from JSON
        print(f"Loading parameters from {args.from_json}...")
        with open(args.from_json) as f:
            data = json.load(f)
        params = CoinParams()
        for key, value in data.items():
            if hasattr(params, key):
                setattr(params, key, value)

    elif args.github:
        # Fully automated mode
        params, warnings = auto_onboard_coin(args.symbol, args.github, args.algorithm)

        # Override with command line args if provided
        if args.name:
            params.name = args.name
            params.full_name = args.name

    elif args.interactive:
        # Interactive mode
        params = CoinParams(symbol=args.symbol.upper())
        if args.name:
            params.name = args.name
        if args.algorithm:
            params.algorithm = args.algorithm
        params = interactive_mode(params)

    else:
        # No GitHub URL and not interactive - try CoinGecko at minimum
        print(f"\nNo GitHub URL provided. Attempting minimal automation...")
        params = CoinParams(symbol=args.symbol.upper())

        if args.name:
            params.name = args.name
        if args.algorithm:
            params.algorithm = args.algorithm

        if not args.skip_coingecko:
            cg_data = fetch_coingecko_data(args.symbol)
            if cg_data.get("name") and not params.name:
                params.name = cg_data["name"]
            if cg_data.get("coingecko_id"):
                params.coingecko_id = cg_data["coingecko_id"]
            if cg_data.get("explorer_url"):
                params.explorer_url = cg_data["explorer_url"]

        # Fall back to interactive mode
        print("\nInsufficient data for full automation. Entering interactive mode...")
        params = interactive_mode(params)

    # Set final defaults
    if not params.name:
        params.name = params.symbol.title() + "coin"
    if not params.full_name:
        params.full_name = params.name

    # Validate
    errors = params.validate()
    if errors:
        print("\n" + "=" * 70)
        print("VALIDATION ERRORS - Cannot proceed")
        print("=" * 70)
        for err in errors:
            print(f"  - {err}")
        print("\nUse --interactive mode to fix these issues.")
        return 1

    # Determine file paths
    go_dir = output_dir / "src" / "stratum" / "internal" / "coin"
    config_dir = output_dir / "config"

    go_file = go_dir / f"{params.symbol.lower()}.go"

    # Check for existing files
    if not args.dry_run and not args.force:
        if go_file.exists():
            print(f"\nWARNING: {go_file} already exists!")
            response = input("Overwrite? [y/N]: ").strip().lower()
            if response != 'y':
                print("Aborted.")
                return 1

    # Get stratum ports for the new coin
    stratum_ports = get_next_available_ports(manifest_file)

    # Generate files
    go_code = generate_go_code(params)
    manifest_entry = generate_manifest_entry(params, stratum_ports)

    print("\n" + "=" * 70)
    print("GENERATED OUTPUT")
    print("=" * 70)

    if args.dry_run:
        print(f"\n[DRY RUN] Would write to: {go_file}")
        print("-" * 70)
        print(go_code[:3000] + "\n... (truncated)" if len(go_code) > 3000 else go_code)
        print("-" * 70)
        print(f"\n[DRY RUN] Would append to: {manifest_file}")
        print("-" * 70)
        print(manifest_entry)
        print("-" * 70)
    else:
        # Write Go file
        go_dir.mkdir(parents=True, exist_ok=True)
        with open(go_file, 'w') as f:
            f.write(go_code)
        print(f"\nWrote Go implementation: {go_file}")

        # Append to manifest (if file exists)
        if manifest_file.exists():
            with open(manifest_file, 'a') as f:
                f.write("\n" + manifest_entry)
            print(f"Appended manifest entry: {manifest_file}")
        else:
            print(f"Manifest file not found: {manifest_file}")
            print("Manifest entry to add manually:")
            print(manifest_entry)

    # Save params to JSON for reference
    params_file = output_dir / "scripts" / f"{params.symbol.lower()}_params.json"
    if not args.dry_run:
        params_file.parent.mkdir(parents=True, exist_ok=True)
        with open(params_file, 'w') as f:
            json.dump(asdict(params), f, indent=2)
        print(f"Parameters saved to: {params_file}")

    # Summary
    print("\n" + "=" * 70)
    print("NEXT STEPS")
    print("=" * 70)

    # Output stratum ports in a parseable format for Windows batch script
    # This line is parsed by spiralpool-add-coin.bat to configure Windows Firewall
    print(f"\n__STRATUM_PORTS__:{stratum_ports['stratum_v1']}:{stratum_ports['stratum_v2']}:{stratum_ports['stratum_tls']}")

    if warnings:
        print("\nWARNINGS to address:")
        for warn in warnings:
            print(f"  ⚠ {warn}")

    print(f"""
1. REVIEW generated files:
   - Go implementation: {go_file}
   - Manifest entry: {manifest_file}
   - Saved parameters: {params_file}

2. VERIFY consensus-critical values:
   - genesis_hash: {params.genesis_hash[:16]}... (verify against official sources)
   - p2pkh_version: {hex(params.p2pkh_version)} (verify address prefix)
   - p2sh_version: {hex(params.p2sh_version)} (verify address prefix)
   {"- chain_id: " + str(params.chain_id) + " (CONSENSUS-CRITICAL for merge mining)" if params.supports_auxpow else ""}

3. IMPLEMENT remaining methods in Go file:
   - ValidateAddress() - full address validation
   - DecodeAddress() - address decoding
   - BuildCoinbaseScript() - coinbase construction
   {"- ParseAuxBlockResponse() - AuxPoW RPC parsing" if params.supports_auxpow else ""}
   {"- SerializeAuxPowProof() - AuxPoW proof serialization" if params.supports_auxpow else ""}

4. RUN validation tests:
   cd {output_dir}/src/stratum
   go test -v ./internal/coin/... -run TestManifest

5. UPDATE documentation (if this is a production coin):
   - docs/MULTI-COIN.md (add to port table)
   - README.md (add to supported coins list)
   - config/config.example.yaml (add example configuration)
""")

    return 0


if __name__ == "__main__":
    sys.exit(main())
