#!/usr/bin/env python3

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
Coin Onboarding Script for Spiral Pool — NET NEW COINS ONLY

This script is for adding coins that are NOT natively supported by Spiral Pool.
The following coins ship with Spiral Pool and should be installed via the installer
(sudo bash install.sh → "Add coins to existing installation"):

    SHA-256d: BTC, BCH, BC2, DGB, FBTC, NMC, QBX, SYS, XMY
    Scrypt:   LTC, DOGE, DGB-SCRYPT, PEP, CAT

Use this script only for coins outside the above list.

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

IMPORTANT — READ BEFORE USE:
    This script is MOSTLY AUTOMATED but the output REQUIRES TESTING before deployment.
    Coin implementations can differ in subtle ways even among SHA256d coins — block
    header serialization, version bits, AuxPoW chain IDs, address encoding, and RPC
    behaviour all vary by fork. The generated Go code is a template, not a finished
    implementation.

    Before deploying a generated coin:
      1. Review the generated Go file and manifest entry carefully
      2. Run the Spiral Pool test suite against the new coin
      3. Test against the coin's actual daemon (devnet/testnet if available)
      4. Understand how the coin's consensus rules differ from Bitcoin/DigiByte
      5. Verify address validation, block template construction, and share checking

    AI assistance (Claude, GPT-4, etc.) can help interpret chainparams.cpp, understand
    fork-specific differences, and debug generated code — recommended for unfamiliar coins.

    Do not deploy to production until you have confirmed correct share submission,
    block discovery, and coinbase payout on a live or test node.

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
    python scripts/add-coin.py -s FOO -g https://github.com/foocoin/foocoin
    python scripts/add-coin.py -s BAR -g https://github.com/barcoin/bar --algorithm scrypt

NOTE: Do not use this script for natively supported coins (BTC, BCH, LTC, DOGE, DGB,
      BC2, FBTC, NMC, QBX, SYS, XMY, PEP, CAT, DGB-SCRYPT). Those are installed via
      the Spiral Pool installer: sudo bash install.sh
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
    "XMY", "MYRIAD", "QBX", "QBITX"
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
        if base > 65535:
            raise RuntimeError("No available stratum port range (all ports > 65535)")


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

def auto_onboard_coin(symbol: str, github_url: str, algorithm_hint: Optional[str] = None) -> Tuple[CoinParams, List[str], str]:
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
        Tuple of (CoinParams, warnings_list, chainparams_source_content)
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
        import hashlib
        params.rpc_port = 8000 + (int(hashlib.md5(symbol.encode()).hexdigest(), 16) % 1000)
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

    return params, warnings, source_content


# ═══════════════════════════════════════════════════════════════════════════════
# GITHUB RELEASE DETECTION
# ═══════════════════════════════════════════════════════════════════════════════

def try_detect_release_url(github_url: str, symbol: str) -> Optional[str]:
    """
    Try to detect a Linux binary download URL from GitHub releases.

    Hits the releases/latest API and scans assets for Linux x86_64 binaries,
    preferring tar.gz archives.  Returns the download URL if found, else None.
    """
    url = github_url.rstrip('/')
    if url.endswith('.git'):
        url = url[:-4]

    match = re.search(r'github\.com/([^/]+/[^/]+?)(?:\.git)?$', url)
    if not match:
        return None

    owner_repo = match.group(1)
    api_url = f"https://api.github.com/repos/{owner_repo}/releases/latest"

    try:
        req = Request(api_url, headers={
            "User-Agent": "SpiralPool-CoinOnboarding/1.0",
            "Accept": "application/vnd.github+json",
        })
        with urlopen(req, timeout=15.0) as resp:
            data = json.loads(resp.read().decode('utf-8'))
    except (URLError, HTTPError, json.JSONDecodeError):
        return None

    assets = data.get("assets", [])
    if not assets:
        return None

    priority_keywords = [
        ["x86_64-linux-gnu", ".tar.gz"],
        ["x86_64-linux", ".tar.gz"],
        ["linux-amd64", ".tar.gz"],
        ["amd64", "linux", ".tar.gz"],
        ["linux", "x86_64", ".tar.gz"],
        ["linux", ".tar.gz"],
        ["linux", ".zip"],
    ]

    name_url_pairs = [(a["name"].lower(), a["browser_download_url"]) for a in assets]
    for keywords in priority_keywords:
        for name_lower, dl_url in name_url_pairs:
            if all(kw in name_lower for kw in keywords):
                return dl_url

    return None


# ═══════════════════════════════════════════════════════════════════════════════
# DNS SEED DETECTION
# ═══════════════════════════════════════════════════════════════════════════════

def detect_dns_seeds(chainparams_content: str) -> List[str]:
    """
    Parse chainparams.cpp for DNS seed entries from the vSeeds array.
    Returns up to 4 seed addresses.
    """
    seeds: List[str] = []

    seed_patterns = [
        r'vSeeds\.emplace_back\s*\(\s*"([^"]+)"',
        r'vSeeds\.push_back\s*\(\s*["\']([^"\']+)["\']',
        r'CDNSSeedData\s*\([^,]+,\s*"([^"]+)"',
    ]

    for pattern in seed_patterns:
        for m in re.findall(pattern, chainparams_content, re.IGNORECASE):
            if re.match(r'^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z]{2,})+$', m):
                if m not in seeds:
                    seeds.append(m)
        if len(seeds) >= 4:
            break

    return seeds[:4]


# ═══════════════════════════════════════════════════════════════════════════════
# DOCKER FILE GENERATORS
# ═══════════════════════════════════════════════════════════════════════════════

def generate_node_dockerfile(params: CoinParams, release_url: Optional[str] = None) -> str:
    """
    Generate Dockerfile for the coin's full node.

    Uses pre-built binary from release_url if provided; otherwise compiles
    from source using the standard Bitcoin Core build system.
    """
    coinlower = params.symbol.lower()
    symbol_upper = params.symbol.upper()
    algo_desc = "Scrypt" if params.algorithm == "scrypt" else "SHA-256d"

    header = f"""# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

# ╔════════════════════════════════════════════════════════════════════════════╗
# ║                                                                            ║
# ║                    {symbol_upper} CORE - DOCKER IMAGE{' ' * (44 - len(symbol_upper))}║
# ║                                                                            ║
# ║   Separate container for {params.name} node ({algo_desc} mining){' ' * max(0, 28 - len(params.name) - len(algo_desc))}║
# ║   This allows independent scaling and management                           ║
# ║                                                                            ║
# ╚════════════════════════════════════════════════════════════════════════════╝

FROM ubuntu:24.04

LABEL maintainer="Spiral Miner"
LABEL description="{params.name} Node for Spiral Pool"

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=UTC
"""

    if release_url:
        install_section = f"""
# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \\
    curl \\
    wget \\
    ca-certificates \\
    libzmq3-dev \\
    gosu \\
    gettext-base \\
    && rm -rf /var/lib/apt/lists/*

# Create {coinlower} user
RUN useradd -r -m -s /bin/bash {coinlower} \\
    && mkdir -p /home/{coinlower}/.{coinlower} \\
    && chown -R {coinlower}:{coinlower} /home/{coinlower}

# Download and install {params.name}
# WARNING: ARM64 (aarch64) support is EXPERIMENTAL and UNTESTED.
WORKDIR /tmp
RUN ARCHIVE="$(basename "{release_url}")" \\
    && wget -q --max-redirect=5 "{release_url}" -O "$ARCHIVE" \\
    && tar -xzf "$ARCHIVE" \\
    && find /tmp -maxdepth 4 \\( -name "{coinlower}d" -o -name "{coinlower}-cli" -o -name "{coinlower}-tx" \\) \\
         -exec install -m 755 {{}} /usr/local/bin/ \\; \\
    && rm -rf /tmp/*
"""
    else:
        install_section = f"""
# Install build dependencies (compile from source — no pre-built binary detected)
# TODO: Set GITHUB_REPO_URL below to the actual repository before building
RUN apt-get update && apt-get install -y --no-install-recommends \\
    curl wget ca-certificates libzmq3-dev libzmq5-dev gosu gettext-base \\
    build-essential autoconf libtool pkg-config git \\
    libboost-all-dev libssl-dev libevent-dev libdb5.3++-dev \\
    && rm -rf /var/lib/apt/lists/*

# Create {coinlower} user
RUN useradd -r -m -s /bin/bash {coinlower} \\
    && mkdir -p /home/{coinlower}/.{coinlower} \\
    && chown -R {coinlower}:{coinlower} /home/{coinlower}

# TODO: Replace GITHUB_REPO_URL with the actual repository URL before building
WORKDIR /tmp
RUN git clone --depth=1 GITHUB_REPO_URL /tmp/{coinlower}-src \\
    && cd /tmp/{coinlower}-src \\
    && ./autogen.sh \\
    && ./configure --without-gui --disable-tests \\
    && make -j$(nproc) \\
    && make install \\
    && rm -rf /tmp/{coinlower}-src
"""

    # Build entrypoint using echo with \n\ continuation (matches existing Dockerfile pattern)
    ep_lines = [
        "#!/bin/bash",
        "set -e",
        "",
        "# Process config template (replace env vars)",
        f"if [ -f /config/{coinlower}.conf.template ]; then",
        f"    envsubst < /config/{coinlower}.conf.template > /home/{coinlower}/.{coinlower}/{coinlower}.conf",
        "fi",
        "",
        "# Ensure proper ownership of data directory",
        f"chown -R {coinlower}:{coinlower} /home/{coinlower}/.{coinlower}",
        "",
        f"# Start {params.name} daemon",
        f'exec gosu {coinlower} {coinlower}d -printtoconsole "$@"',
    ]
    ep_body = "\\n\\\n".join(ep_lines)

    tail = f"""
# Create entrypoint script
RUN echo '{ep_body}\\n\\
' > /entrypoint.sh && chmod +x /entrypoint.sh

# Expose ports
# P2P
EXPOSE {params.p2p_port}
# RPC
EXPOSE {params.rpc_port}
# ZMQ
EXPOSE {params.zmq_port}

# Data volume
VOLUME ["/home/{coinlower}/.{coinlower}"]

USER root
ENTRYPOINT ["/entrypoint.sh"]
"""
    return header + install_section + tail


def generate_conf_template(params: CoinParams, dns_seeds: Optional[List[str]] = None) -> str:
    """
    Generate node configuration template for the coin.
    Follows the litecoin.conf.template / catcoin.conf.template pattern.
    """
    symbol_upper = params.symbol.upper()
    zmq_port = params.zmq_port if params.zmq_port else 28000 + (params.rpc_port % 1000)
    dbcache = 8192 if params.algorithm == "sha256d" else 4096

    conf = f"""# ═══════════════════════════════════════════════════════════════════════════════
# {symbol_upper} CORE - SPIRAL POOL CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════
# Optimized for fast initial blockchain sync and reliable mining operations

# ═══════════════════════════════════════════════════════════════════════════════
# NETWORK SETTINGS
# ═══════════════════════════════════════════════════════════════════════════════
listen=1
port={params.p2p_port}

# Maximum peer connections
maxconnections=125

# ═══════════════════════════════════════════════════════════════════════════════
# RPC CONFIGURATION
# ═══════════════════════════════════════════════════════════════════════════════
server=1
rpcuser=${{RPC_USER}}
rpcpassword=${{RPC_PASSWORD}}
rpcallowip=127.0.0.1
rpcallowip=172.17.0.0/16
rpcallowip=10.0.0.0/8
rpcallowip=192.168.0.0/16
rpcbind=0.0.0.0
rpcport={params.rpc_port}

# RPC work queue for mining
rpcworkqueue=64
rpcthreads=4

# ═══════════════════════════════════════════════════════════════════════════════
# ZMQ BLOCK NOTIFICATIONS (for instant job updates)
# ═══════════════════════════════════════════════════════════════════════════════
zmqpubhashblock=tcp://0.0.0.0:{zmq_port}
zmqpubrawtx=tcp://0.0.0.0:{zmq_port}
zmqpubrawblock=tcp://0.0.0.0:{zmq_port}

# ═══════════════════════════════════════════════════════════════════════════════
# PERFORMANCE TUNING
# ═══════════════════════════════════════════════════════════════════════════════
# Database cache size in MB
dbcache={dbcache}

# Script verification threads (0 = auto-detect CPU cores)
par=0

# ═══════════════════════════════════════════════════════════════════════════════
# WALLET (required for solo mining)
# ═══════════════════════════════════════════════════════════════════════════════
disablewallet=0

# ═══════════════════════════════════════════════════════════════════════════════
# LOGGING
# ═══════════════════════════════════════════════════════════════════════════════
printtoconsole=1
debug=0
logtimestamps=1
shrinkdebugfile=1

# ═══════════════════════════════════════════════════════════════════════════════
# STORAGE (Full node - no pruning for mining)
# ═══════════════════════════════════════════════════════════════════════════════
prune=0
txindex=1
"""

    if dns_seeds:
        conf += "\n# ═══════════════════════════════════════════════════════════════════════════════\n"
        conf += "# DNS SEEDS (extracted from chainparams.cpp)\n"
        conf += "# ═══════════════════════════════════════════════════════════════════════════════\n"
        conf += "dnsseed=1\n"
        for seed in dns_seeds:
            conf += f"addnode={seed}\n"

    conf += "\n# ═══════════════════════════════════════════════════════════════════════════════\n"
    conf += "# MINING SPECIFIC\n"
    conf += "# ═══════════════════════════════════════════════════════════════════════════════\n"
    conf += "blockmaxweight=4000000\n"
    conf += "minrelaytxfee=0.00001\n"

    # AuxPoW / merge mining — only present if the coin explicitly supports it
    # and has a confirmed BTC or LTC parent chain.
    # NOTE: No node-level merge mining config is needed. The node mines normally.
    # Spiral Pool's stratum handles AuxPoW proof construction and submission
    # automatically when the coin's role is "aux".
    if params.supports_auxpow and params.parent_chain in ("BTC", "LTC"):
        conf += f"\n# ═══════════════════════════════════════════════════════════════════════════════\n"
        conf += f"# MERGE MINING (AuxPoW)\n"
        conf += f"# ═══════════════════════════════════════════════════════════════════════════════\n"
        conf += f"# This coin supports AuxPoW merge mining with {params.parent_chain}.\n"
        conf += f"# Chain ID: {params.chain_id}  |  Parent: {params.parent_chain}  |  Algorithm: {params.algorithm}\n"
        conf += f"# No special node config is required — AuxPoW is handled by the stratum layer.\n"
        conf += f"# Ensure the {params.parent_chain} node is running and reachable before mining.\n"

    return conf


# ═══════════════════════════════════════════════════════════════════════════════
# DOCKER COMPOSE PATCHER
# ═══════════════════════════════════════════════════════════════════════════════

def patch_docker_compose(params: CoinParams, compose_path: Path) -> None:
    """
    Patch docker-compose.yml in-place to add support for the new coin.

    Uses unique string anchors to insert content at the correct locations.
    Idempotent: skips gracefully if the coin is already present.
    """
    coinlower = params.symbol.lower()
    symbol_upper = params.symbol.upper()
    zmq_port = params.zmq_port if params.zmq_port else 28000 + (params.rpc_port % 1000)
    stratum_v1 = params.stratum_port
    stratum_tls = params.stratum_tls_port
    algo_upper = params.algorithm.upper()

    # AuxPoW coins require a parent chain — they cannot run standalone.
    # Standalone and parent coins get added to "multi"; aux coins do not.
    is_aux = params.role == "aux" or params.supports_auxpow
    if is_aux:
        node_profiles = f'["{coinlower}"]'
        if params.parent_chain:
            print(f"  AuxPoW coin: profile will be [{coinlower}] only (requires {params.parent_chain} parent, not added to 'multi')")
    else:
        node_profiles = f'["{coinlower}", "multi"]'

    if not compose_path.exists():
        print(f"  [SKIP] docker-compose.yml not found at {compose_path}")
        return

    content = compose_path.read_text(encoding='utf-8')

    # Idempotency guard
    if f'container_name: spiralpool-{coinlower}' in content:
        print(f"  [SKIP] {symbol_upper} already in docker-compose.yml")
        return

    # Port conflict detection
    existing_ports = set(re.findall(r'"(\d+):\d+"', content))
    for port, label in [(stratum_v1, "Stratum V1"), (stratum_tls, "Stratum TLS")]:
        if str(port) in existing_ports:
            print(f"  [WARN] {label} port {port} already in docker-compose.yml")

    # AuxPoW comment in service header
    auxpow_comment = ""
    if is_aux and params.parent_chain:
        auxpow_comment = f"\n  # NOTE: AuxPoW — merge-mined with {params.parent_chain}. Start alongside the {params.parent_chain.lower()} profile."

    # ── Anchor 1: Insert coin service block before PostgreSQL ─────────────────
    service_block = f"""
  # {params.name} ({symbol_upper}) - {algo_upper}{auxpow_comment}
  # NOTE: no-new-privileges NOT set - gosu requires setuid for privilege drop
  {coinlower}:
    profiles: {node_profiles}
    build:
      context: ..
      dockerfile: docker/Dockerfile.{coinlower}
    container_name: spiralpool-{coinlower}
    restart: unless-stopped
    logging: *default-logging
    networks:
      - spiralpool
    ports:
      - "{params.p2p_port}:{params.p2p_port}"   # P2P
      - "127.0.0.1:{params.rpc_port}:{params.rpc_port}"   # RPC — localhost only
    environment:
      - RPC_USER=${{{symbol_upper}_RPC_USER:-spiral{coinlower}}}
      - RPC_PASSWORD=${{{symbol_upper}_RPC_PASSWORD:?{symbol_upper}_RPC_PASSWORD must be set}}
      - ZMQ_PORT={zmq_port}
    volumes:
      - ${{{symbol_upper}_DATA_DIR:-{coinlower}-data}}:/home/{coinlower}/.{coinlower}
      - ./config/{coinlower}.conf.template:/config/{coinlower}.conf.template:ro
    deploy:
      resources:
        limits:
          cpus: '2'
          memory: 4G
        reservations:
          memory: 1G
    ulimits:
      nofile:
        soft: 65535
        hard: 65535
    healthcheck:
      test: ["CMD", "{coinlower}-cli", "-conf=/home/{coinlower}/.{coinlower}/{coinlower}.conf", "getblockchaininfo"]
      interval: 30s
      timeout: 10s
      retries: 5
      start_period: 120s

"""
    pg_anchor = '  # ═══════════════════════════════════════════════════════════════════════════\n  # POSTGRESQL DATABASE'
    if pg_anchor in content:
        content = content.replace(pg_anchor, service_block + pg_anchor, 1)
    else:
        print(f"  [WARN] PostgreSQL anchor not found — service block not inserted")

    # ── Anchor 2: Add symbol to all shared service profiles ───────────────────
    # The coin's own profile is always added to shared services (postgres, stratum,
    # dashboard, sentinel, prometheus, grafana) so that `--profile {coinlower}` starts
    # the full stack.  "multi" stays at the end of these lists regardless of AuxPoW
    # status — "multi" inclusion in the COIN NODE service itself is what controls
    # whether the coin starts with `--profile multi` (handled above via node_profiles).
    old_prof = '"pep", "cat", "multi"]'
    new_prof = f'"pep", "cat", "{coinlower}", "multi"]'
    if old_prof in content:
        content = content.replace(old_prof, new_prof)
    else:
        print(f"  [WARN] Profile anchor not found — shared profiles not updated")

    # ── Anchor 3: Add stratum port pair before API port ───────────────────────
    port_entry = (
        f'      - "{stratum_v1}:{stratum_v1}"   # {symbol_upper} Stratum V1\n'
        f'      - "{stratum_tls}:{stratum_tls}"   # {symbol_upper} Stratum TLS\n'
    )
    api_anchor = '      # API\n      - "4000:4000"'
    if api_anchor in content:
        content = content.replace(api_anchor, port_entry + api_anchor, 1)
    else:
        print(f"  [WARN] API port anchor not found — stratum ports not added")

    # ── Anchor 4: Add depends_on entry after catcoin ──────────────────────────
    cat_dep = '      catcoin:\n        condition: service_healthy\n        required: false'
    new_dep = (
        f'      catcoin:\n        condition: service_healthy\n        required: false\n'
        f'      {coinlower}:\n        condition: service_healthy\n        required: false'
    )
    if cat_dep in content:
        content = content.replace(cat_dep, new_dep, 1)
    else:
        print(f"  [WARN] catcoin depends_on anchor not found")

    # ── Anchor 5: Add stratum RPC env vars after CAT credentials ──────────────
    cat_rpc = '      - CAT_RPC_USER=${CAT_RPC_USER:-spiralcat}\n      - CAT_RPC_PASSWORD=${CAT_RPC_PASSWORD:-}'
    new_rpc = (
        f'      - CAT_RPC_USER=${{CAT_RPC_USER:-spiralcat}}\n'
        f'      - CAT_RPC_PASSWORD=${{CAT_RPC_PASSWORD:-}}\n'
        f'      - {symbol_upper}_RPC_USER=${{{symbol_upper}_RPC_USER:-spiral{coinlower}}}\n'
        f'      - {symbol_upper}_RPC_PASSWORD=${{{symbol_upper}_RPC_PASSWORD:-}}'
    )
    if '      - CAT_RPC_USER=${CAT_RPC_USER:-spiralcat}' in content:
        content = content.replace(cat_rpc, new_rpc, 1)
    else:
        print(f"  [WARN] CAT RPC anchor not found — stratum env vars not added")

    # ── Anchor 6: Add sentinel wallet env var after CAT ───────────────────────
    cat_wallet = '      - CAT_WALLET_ADDRESS=${CAT_POOL_ADDRESS:-}'
    new_wallet = (
        f'      - CAT_WALLET_ADDRESS=${{CAT_POOL_ADDRESS:-}}\n'
        f'      - {symbol_upper}_WALLET_ADDRESS=${{{symbol_upper}_POOL_ADDRESS:-}}'
    )
    if cat_wallet in content:
        content = content.replace(cat_wallet, new_wallet, 1)
    else:
        print(f"  [WARN] CAT wallet anchor not found — sentinel env var not added")

    # ── Anchor 7: Add volume before shared service volumes ────────────────────
    shared_anchor = '  # Shared service volumes'
    new_vol = f'  {coinlower}-data:\n    name: spiralpool-{coinlower}-data\n  # Shared service volumes'
    if shared_anchor in content:
        content = content.replace(shared_anchor, new_vol, 1)
    else:
        print(f"  [WARN] Shared volumes anchor not found — volume not added")

    # ── Anchor 8: Add port comment in file header ─────────────────────────────
    cat_comment = '#     CAT:                           12335   12337   9932    9933'
    if cat_comment in content:
        pad = ' ' * max(1, 35 - len(symbol_upper))
        new_line = (
            f'{cat_comment}\n'
            f'#     {symbol_upper}:{pad}{stratum_v1}   {stratum_tls}   {params.rpc_port}    {params.p2p_port}'
        )
        content = content.replace(cat_comment, new_line, 1)

    compose_path.write_text(content, encoding='utf-8')
    print(f"  Updated: {compose_path}")


# ═══════════════════════════════════════════════════════════════════════════════
# NATIVE INSTALL SCRIPT GENERATOR
# ═══════════════════════════════════════════════════════════════════════════════

def generate_native_install_script(params: CoinParams, release_url: Optional[str] = None) -> str:
    """
    Generate scripts/install-{SYMBOL}.sh — a self-contained bash script for
    native Linux installation, following the install_catcoin() pattern.
    """
    coinlower = params.symbol.lower()
    symbol_upper = params.symbol.upper()
    zmq_port = params.zmq_port if params.zmq_port else 28000 + (params.rpc_port % 1000)
    dbcache = 8192 if params.algorithm == "sha256d" else 4096

    auxpow_note = ""
    # Only emit AuxPoW guidance when the coin explicitly declares it AND the parent
    # chain is a supported one (BTC or LTC).  Do not add merge mining notes for
    # standalone coins or coins where detection was ambiguous.
    confirmed_auxpow = (
        params.supports_auxpow
        and params.parent_chain in ("BTC", "LTC")
        and params.chain_id > 0
    )
    if confirmed_auxpow:
        auxpow_note = (
            f"\n# ── AuxPoW / Merge Mining ───────────────────────────────────────────────────\n"
            f"# {params.name} supports merge mining (AuxPoW) with {params.parent_chain}.\n"
            f"# Chain ID : {params.chain_id}\n"
            f"# Algorithm: {params.algorithm} (matches {params.parent_chain} parent)\n"
            f"#\n"
            f"# The {params.parent_chain} parent node MUST be installed and fully synced before\n"
            f"# this coin can produce merge-mined blocks.  Spiral Pool's stratum handles\n"
            f"# AuxPoW proof construction automatically — no extra node config is needed.\n"
            f"# ─────────────────────────────────────────────────────────────────────────────\n"
        )

    if release_url:
        binary_section = f"""    echo "[1/7] Downloading {params.name} binaries..."
    ARCHIVE="$(basename "{release_url}")"
    cd /tmp
    wget -q --show-progress --max-redirect=5 "{release_url}" -O "$ARCHIVE"
    if [ ! -f "$ARCHIVE" ]; then
        echo "ERROR: Download failed: {release_url}"
        echo "  Try setting {symbol_upper}_SOURCE_URL and re-running for compile-from-source."
        exit 1
    fi
    tar -xzf "$ARCHIVE"
    find /tmp -maxdepth 4 \\( -name "{coinlower}d" -o -name "{coinlower}-cli" -o -name "{coinlower}-tx" \\) \\
        -exec install -m 755 {{}} /usr/local/bin/ \\;
    rm -rf /tmp/"$ARCHIVE" /tmp/{coinlower}* 2>/dev/null || true
    if ! command -v {coinlower}d &>/dev/null; then
        echo "ERROR: {coinlower}d not found after extraction. Binary names may differ."
        echo "  Release URL: {release_url}"
        exit 1
    fi
    echo "  Installed: $(command -v {coinlower}d)"
"""
    else:
        binary_section = f"""    # No pre-built binary detected — compile from source
    SOURCE_URL="${{{symbol_upper}_SOURCE_URL:-}}"
    if [ -z "$SOURCE_URL" ]; then
        echo "ERROR: No binary URL available and {symbol_upper}_SOURCE_URL is not set."
        echo "  Set {symbol_upper}_SOURCE_URL=https://github.com/owner/repo and re-run."
        exit 1
    fi
    echo "[1/7] Compiling {params.name} from source (may take 20-40 minutes)..."
    apt-get install -y --no-install-recommends \\
        build-essential autoconf libtool pkg-config git \\
        libboost-all-dev libssl-dev libevent-dev libzmq5-dev libdb5.3++-dev >/dev/null 2>&1
    cd /tmp
    git clone --depth=1 "$SOURCE_URL" {coinlower}-src
    cd {coinlower}-src
    ./autogen.sh
    ./configure --without-gui --disable-tests
    make -j$(nproc)
    make install
    cd /
    rm -rf /tmp/{coinlower}-src
    echo "  Compiled and installed {coinlower}d"
"""

    # Build pre-flight check block only for confirmed AuxPoW coins with a valid parent
    if confirmed_auxpow:
        parent_lower = params.parent_chain.lower()
        auxpow_preflight = (
            f"# ─── AuxPoW pre-flight check ────────────────────────────────────────────────\n"
            f"# {params.name} requires {params.parent_chain} for merge mining.\n"
            f"if ! command -v {parent_lower}d &>/dev/null; then\n"
            f"    echo \"WARN: {params.parent_chain} ({parent_lower}d) not found.\"\n"
            f"    echo \"  Merge mining will not work until {params.parent_chain} is installed and synced.\"\n"
            f"fi"
        )
    else:
        auxpow_preflight = ""

    script = f"""#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
#
# {symbol_upper} NATIVE INSTALL SCRIPT FOR SPIRAL POOL
# Generated by: scripts/add-coin.py
#
# Installs {params.name} ({symbol_upper}) as a systemd service for Spiral Pool.
#
# Usage:
#   sudo bash scripts/install-{symbol_upper}.sh
#
# Prerequisites:
#   - Ubuntu 22.04+ or Debian 12+
#   - Root/sudo access
#   - Spiral Pool at $INSTALL_DIR
#{auxpow_note}
set -euo pipefail

# ─── Environment defaults ────────────────────────────────────────────────────
POOL_USER="${{POOL_USER:-spiraluser}}"
INSTALL_DIR="${{INSTALL_DIR:-/spiralpool}}"
DATA_DIR="${{DATA_DIR:-$INSTALL_DIR/{coinlower}}}"
CONFIG_FILE="$DATA_DIR/{coinlower}.conf"
SERVICE_NAME="{coinlower}d"
WALLET_NAME="wallet-{symbol_upper}"

# ─── Sanity checks ───────────────────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
    echo "ERROR: Run as root: sudo bash $0"
    exit 1
fi

echo ""
echo "Installing {params.name} ({symbol_upper}) for Spiral Pool..."
echo ""
{auxpow_preflight}
install_{coinlower}() {{
{binary_section}
    # ─── User and directory setup ─────────────────────────────────────────────
    echo "[2/7] Setting up user and directories..."
    if ! id -u "$POOL_USER" &>/dev/null; then
        useradd -r -m -s /bin/bash "$POOL_USER"
        echo "  Created pool user: $POOL_USER"
    fi
    mkdir -p "$DATA_DIR/wallets/$WALLET_NAME"
    # Enforce ownership and permissions on the data directory and all contents
    chown -R "$POOL_USER:$POOL_USER" "$DATA_DIR"
    chmod 750 "$DATA_DIR"
    # Wallet dirs need execute bit for directory traversal
    find "$DATA_DIR" -type d -exec chmod 750 {{}} \;
    # Wallet files are sensitive — restrict to owner only
    find "$DATA_DIR" -type f -exec chmod 640 {{}} \;

    # ─── Generate config ──────────────────────────────────────────────────────
    echo "[3/7] Generating configuration..."
    RPC_PASSWORD=$(openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32)
    RPC_USER="spiral{coinlower}"
    cat > "$CONFIG_FILE" << COINCONF
# {params.name} ({symbol_upper}) - Spiral Pool config
listen=1
port={params.p2p_port}
maxconnections=125
server=1
rpcuser=$RPC_USER
rpcpassword=$RPC_PASSWORD
rpcallowip=127.0.0.1
rpcbind=127.0.0.1
rpcport={params.rpc_port}
rpcworkqueue=64
rpcthreads=4
zmqpubhashblock=tcp://127.0.0.1:{zmq_port}
zmqpubrawtx=tcp://127.0.0.1:{zmq_port}
zmqpubrawblock=tcp://127.0.0.1:{zmq_port}
dbcache={dbcache}
par=0
disablewallet=0
walletdir=$DATA_DIR/wallets
prune=0
txindex=1
printtoconsole=0
logtimestamps=1
shrinkdebugfile=1
COINCONF
    chown "$POOL_USER:$POOL_USER" "$CONFIG_FILE"
    chmod 600 "$CONFIG_FILE"
    echo "  Config: $CONFIG_FILE"

    # ─── Firewall ─────────────────────────────────────────────────────────────
    echo "[4/7] Opening firewall ports..."
    if command -v ufw &>/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow {params.p2p_port}/tcp comment "{symbol_upper} P2P" 2>/dev/null || true
        ufw allow {params.stratum_port}/tcp comment "{symbol_upper} Stratum V1" 2>/dev/null || true
        ufw allow {params.stratum_tls_port}/tcp comment "{symbol_upper} Stratum TLS" 2>/dev/null || true
        echo "  UFW: opened {params.p2p_port} (P2P), {params.stratum_port} (V1), {params.stratum_tls_port} (TLS)"
    elif command -v firewall-cmd &>/dev/null; then
        firewall-cmd --permanent --add-port={params.p2p_port}/tcp 2>/dev/null || true
        firewall-cmd --permanent --add-port={params.stratum_port}/tcp 2>/dev/null || true
        firewall-cmd --permanent --add-port={params.stratum_tls_port}/tcp 2>/dev/null || true
        firewall-cmd --reload 2>/dev/null || true
        echo "  firewalld: opened {params.p2p_port}, {params.stratum_port}, {params.stratum_tls_port}"
    else
        echo "  [WARN] No active firewall detected — open ports manually:"
        echo "    {params.p2p_port}/tcp  P2P"
        echo "    {params.stratum_port}/tcp  Stratum V1"
        echo "    {params.stratum_tls_port}/tcp  Stratum TLS"
    fi

    # ─── Systemd service ──────────────────────────────────────────────────────
    echo "[5/7] Installing systemd service..."
    cat > "/etc/systemd/system/$SERVICE_NAME.service" << SVCFILE
[Unit]
Description={params.name} daemon for Spiral Pool
After=network.target

[Service]
Type=forking
User=$POOL_USER
Group=$POOL_USER
ExecStart=/usr/local/bin/{coinlower}d -conf=$CONFIG_FILE -daemon
ExecStop=/usr/local/bin/{coinlower}-cli -conf=$CONFIG_FILE stop
Restart=on-failure
RestartSec=30
TimeoutStartSec=120
TimeoutStopSec=60
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=read-only

[Install]
WantedBy=multi-user.target
SVCFILE
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"

    # ─── Start and wait ───────────────────────────────────────────────────────
    echo "[6/7] Starting $SERVICE_NAME..."
    systemctl start "$SERVICE_NAME"
    MAX_WAIT=120
    WAITED=0
    until {coinlower}-cli -conf="$CONFIG_FILE" getblockchaininfo &>/dev/null; do
        if [ $WAITED -ge $MAX_WAIT ]; then
            echo "  ERROR: daemon did not start within $MAX_WAIT seconds"
            echo "  Check: journalctl -u $SERVICE_NAME -n 50"
            exit 1
        fi
        echo "  Waiting for {coinlower}d... ($WAITED / $MAX_WAIT s)"
        sleep 5
        WAITED=$((WAITED + 5))
    done

    # ─── Wallet and address ───────────────────────────────────────────────────
    echo "[7/7] Creating wallet and mining address..."
    CLI="{coinlower}-cli -conf=$CONFIG_FILE"
    $CLI createwallet "$WALLET_NAME" 2>/dev/null || true
    POOL_ADDRESS=$($CLI -rpcwallet="$WALLET_NAME" getnewaddress "pool" "legacy")
    echo "  Pool address: $POOL_ADDRESS"

    # Re-apply permissions after wallet creation (daemon may have created new files)
    chown -R "$POOL_USER:$POOL_USER" "$DATA_DIR"
    find "$DATA_DIR" -type d -exec chmod 750 {{}} \;
    find "$DATA_DIR" -type f -exec chmod 640 {{}} \;

    ENV_FILE="$INSTALL_DIR/.env"
    touch "$ENV_FILE"
    for PAIR in "{symbol_upper}_POOL_ADDRESS=$POOL_ADDRESS" "{symbol_upper}_RPC_PASSWORD=$RPC_PASSWORD" "{symbol_upper}_RPC_USER=$RPC_USER"; do
        KEY="${{PAIR%%=*}}"
        VAL="${{PAIR#*=}}"
        if grep -q "^$KEY=" "$ENV_FILE" 2>/dev/null; then
            sed -i "s|^$KEY=.*|$KEY=$VAL|" "$ENV_FILE"
        else
            echo "$KEY=$VAL" >> "$ENV_FILE"
        fi
    done
    echo "  Credentials saved to $ENV_FILE"

    CONFIG_YAML="$INSTALL_DIR/config/config.yaml"
    if [ -f "$CONFIG_YAML" ] && grep -q "^  pool_address:" "$CONFIG_YAML"; then
        sed -i "s|^  pool_address:.*|  pool_address: $POOL_ADDRESS  # {symbol_upper}|" "$CONFIG_YAML"
    fi

    WALLET_BIN="/usr/local/bin/spiralpool-wallet"
    if [ -f "$WALLET_BIN" ] && ! grep -q "^    {coinlower})" "$WALLET_BIN"; then
        sed -i "/^esac$/i\\\\    {coinlower})\\\\n        CONF=\\"$CONFIG_FILE\\"\\\\n        WALLET=\\"$WALLET_NAME\\"\\\\n        ;;" "$WALLET_BIN"
    fi

    echo ""
    echo "  {params.name} ({symbol_upper}) install complete!"
    echo "  Pool address : $POOL_ADDRESS"
    echo "  Stratum V1   : {params.stratum_port}"
    echo "  Stratum TLS  : {params.stratum_tls_port}"
    echo "  RPC port     : {params.rpc_port} (localhost only)"
    echo "  Service      : systemctl status $SERVICE_NAME"
    echo ""
    echo "  Connection   : stratum+tcp://YOUR_IP:{params.stratum_port}"
    echo "  Sync status  : $CLI getblockchaininfo"
    echo ""
    echo "  IMPORTANT: Blockchain sync needed before mining (hours to days)."
}}

install_{coinlower}
"""
    return script


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
    parser.add_argument("--binary-url", metavar="URL",
                        help="Override auto-detected binary download URL for Dockerfile/installer")
    parser.add_argument("--skip-docker", action="store_true",
                        help="Skip Docker file generation (Dockerfile + conf.template + compose patch)")
    parser.add_argument("--skip-native", action="store_true",
                        help="Skip native install script generation")

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
    chainparams_content = ""

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
        params, warnings, chainparams_content = auto_onboard_coin(args.symbol, args.github, args.algorithm)

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

    # Ensure stratum ports are set on params (used by docker/install generators)
    if params.stratum_port == 0:
        params.stratum_port = stratum_ports["stratum_v1"]
    if params.stratum_v2_port == 0:
        params.stratum_v2_port = stratum_ports["stratum_v2"]
    if params.stratum_tls_port == 0:
        params.stratum_tls_port = stratum_ports["stratum_tls"]
    if params.zmq_port == 0:
        params.zmq_port = 28000 + (params.rpc_port % 1000)

    # Generate core files
    go_code = generate_go_code(params)
    manifest_entry = generate_manifest_entry(params, stratum_ports)

    # Step A: Binary URL detection
    symbol_lower = params.symbol.lower()
    release_url: Optional[str] = None
    if getattr(args, 'binary_url', None):
        release_url = args.binary_url
        print(f"\n  Using provided binary URL: {release_url}")
    elif args.github and not getattr(args, 'skip_docker', False) and not getattr(args, 'skip_native', False):
        print(f"\n[A] Detecting release binary URL...")
        release_url = try_detect_release_url(args.github, params.symbol)
        if release_url:
            print(f"  Found: {release_url}")
        else:
            print(f"  [!] No Linux binary found — Dockerfile will use compile-from-source")

    # Step B–C: Docker files
    dockerfile_content = ""
    conf_content = ""
    if not getattr(args, 'skip_docker', False):
        dns_seeds = detect_dns_seeds(chainparams_content) if chainparams_content else []
        dockerfile_content = generate_node_dockerfile(params, release_url)
        conf_content = generate_conf_template(params, dns_seeds)

    # Step D: Native install script
    install_script = ""
    if not getattr(args, 'skip_native', False):
        install_script = generate_native_install_script(params, release_url)

    # ── File paths ────────────────────────────────────────────────────────────
    docker_dir = output_dir / "docker"
    dockerfile_path = docker_dir / f"Dockerfile.{symbol_lower}"
    conf_template_path = docker_dir / "config" / f"{symbol_lower}.conf.template"
    compose_path = docker_dir / "docker-compose.yml"
    install_script_path = output_dir / "scripts" / f"install-{params.symbol.upper()}.sh"
    params_file = output_dir / "scripts" / f"{symbol_lower}_params.json"

    print("\n" + "=" * 70)
    print("GENERATED OUTPUT")
    print("=" * 70)

    if args.dry_run:
        print(f"\n[DRY RUN] Would write: {go_file}")
        print(f"[DRY RUN] Would append: {manifest_file}")
        if dockerfile_content:
            print(f"[DRY RUN] Would write: {dockerfile_path}")
            print(f"[DRY RUN] Would write: {conf_template_path}")
            print(f"[DRY RUN] Would patch: {compose_path}")
        if install_script:
            print(f"[DRY RUN] Would write: {install_script_path}")
        print("\n--- Go stub (first 1000 chars) ---")
        print(go_code[:1000] + "\n...(truncated)")
    else:
        # Write Go file
        go_dir.mkdir(parents=True, exist_ok=True)
        with open(go_file, 'w') as f:
            f.write(go_code)
        print(f"\n  Go implementation:  {go_file}")

        # Append to manifest
        if manifest_file.exists():
            with open(manifest_file, 'a') as f:
                f.write("\n" + manifest_entry)
            print(f"  Manifest entry:     {manifest_file}")
        else:
            print(f"  [WARN] Manifest not found: {manifest_file}")
            print(manifest_entry)

        # Write Dockerfile
        if dockerfile_content:
            docker_dir.mkdir(parents=True, exist_ok=True)
            (docker_dir / "config").mkdir(parents=True, exist_ok=True)
            with open(dockerfile_path, 'w') as f:
                f.write(dockerfile_content)
            print(f"  Dockerfile:         {dockerfile_path}")

            with open(conf_template_path, 'w') as f:
                f.write(conf_content)
            print(f"  Node config:        {conf_template_path}")

            # Patch docker-compose.yml
            print(f"  Patching compose...")
            patch_docker_compose(params, compose_path)

        # Write native install script
        if install_script:
            with open(install_script_path, 'w') as f:
                f.write(install_script)
            try:
                import stat
                install_script_path.chmod(install_script_path.stat().st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
            except Exception:
                pass
            print(f"  Native installer:   {install_script_path}")

        # Save params to JSON
        params_file.parent.mkdir(parents=True, exist_ok=True)
        with open(params_file, 'w') as f:
            json.dump(asdict(params), f, indent=2)
        print(f"  Parameters JSON:    {params_file}")

    # Summary
    print("\n" + "=" * 70)
    print("NEXT STEPS")
    print("=" * 70)

    # Parseable output for spiralpool-add-coin.bat (Windows Firewall configuration)
    print(f"\n__STRATUM_PORTS__:{stratum_ports['stratum_v1']}:{stratum_ports['stratum_v2']}:{stratum_ports['stratum_tls']}")

    if warnings:
        print("\nWARNINGS to address:")
        for warn in warnings:
            print(f"  ⚠ {warn}")

    if not args.dry_run:
        if dockerfile_content:
            print(f"""
  Docker deployment:
    docker compose --profile {symbol_lower} build --no-cache
    docker compose --profile {symbol_lower} up -d
""")
        if install_script:
            print(f"""  Native deployment:
    sudo bash {install_script_path}
""")

    print(f"""
1. REVIEW generated files:
   - Go implementation: {go_file}
   - Manifest entry: {manifest_file}
   {"- Dockerfile:        " + str(dockerfile_path) if dockerfile_content else ""}
   {"- Node config:       " + str(conf_template_path) if conf_content else ""}
   {"- Native installer:  " + str(install_script_path) if install_script else ""}

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
""")

    return 0


if __name__ == "__main__":
    sys.exit(main())
