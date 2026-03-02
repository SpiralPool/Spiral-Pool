#!/usr/bin/env python3
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""
Coin Manifest Loader for Python Components (Sentinel, Dashboard)

This module provides access to the single coin manifest (coins.manifest.yaml)
for Python-based pool components. It mirrors the Go manifest loader's
functionality while providing Pythonic access patterns.

Usage:
    from coin_manifest import get_manifest, CoinManifest

    # Get loaded manifest
    manifest = get_manifest()

    # List all coins
    all_coins = manifest.list_coins()

    # Get specific coin
    doge = manifest.get_coin("DOGE")
    print(f"Dogecoin block time: {doge.block_time}s")

    # Filter by algorithm
    sha256d_coins = manifest.list_coins(algorithm="sha256d")
    scrypt_coins = manifest.list_coins(algorithm="scrypt")

    # List merge-mineable coins
    auxpow_coins = manifest.list_auxpow_coins()

    # List parent chains
    parent_coins = manifest.list_parent_coins()

    # Validate against pool API (ensures Go registry has all coins)
    result = manifest.validate_against_pool("http://localhost:4000")
    if not result.valid:
        print(f"Validation errors: {result.errors}")
"""

import os
import json
import logging
import yaml
from pathlib import Path
from typing import Dict, List, Optional, Any, Tuple
from dataclasses import dataclass, field
from urllib.request import urlopen, Request
from urllib.error import URLError, HTTPError

logger = logging.getLogger(__name__)


# ═══════════════════════════════════════════════════════════════════════════════
# MANIFEST PATH RESOLUTION
# ═══════════════════════════════════════════════════════════════════════════════

def find_manifest_path() -> Path:
    """
    Find the coin manifest file.

    Search order:
    1. SPIRALPOOL_MANIFEST_PATH environment variable
    2. /spiralpool/config/coins.manifest.yaml (production)
    3. Relative paths from current directory
    """
    # Check environment variable first
    env_path = os.environ.get("SPIRALPOOL_MANIFEST_PATH")
    if env_path and Path(env_path).exists():
        return Path(env_path)

    # Check standard paths
    paths = [
        Path("/spiralpool/config/coins.manifest.yaml"),
        Path("./config/coins.manifest.yaml"),
        Path("../config/coins.manifest.yaml"),
        Path("../../config/coins.manifest.yaml"),
        # Relative to this file
        Path(__file__).parent.parent.parent / "config" / "coins.manifest.yaml",
    ]

    for p in paths:
        if p.exists():
            return p.resolve()

    # Default to first path even if not found
    return paths[0]


# ═══════════════════════════════════════════════════════════════════════════════
# DATA CLASSES
# ═══════════════════════════════════════════════════════════════════════════════

@dataclass
class NetworkConfig:
    """Network port configuration for a coin."""
    rpc_port: int
    p2p_port: int
    zmq_port: int
    shared_node: Optional[str] = None


@dataclass
class AddressConfig:
    """Address encoding configuration for a coin."""
    p2pkh_version: int
    p2sh_version: int
    bech32_hrp: Optional[str] = None
    supports_cashaddr: bool = False
    collision_warning: Optional[str] = None


@dataclass
class ChainConfig:
    """Chain parameters for a coin."""
    genesis_hash: str
    block_time: int
    coinbase_maturity: int
    supports_segwit: bool
    min_coinbase_script_len: int = 2


@dataclass
class MergeMiningConfig:
    """Merge-mining (AuxPoW) configuration."""
    # For parent chains
    can_be_parent: bool = False
    supported_aux_algorithms: List[str] = field(default_factory=list)

    # For auxiliary chains
    supports_auxpow: bool = False
    auxpow_start_height: int = 0
    chain_id: int = 0           # CONSENSUS-CRITICAL
    version_bit: int = 0        # CONSENSUS-CRITICAL
    parent_chain: Optional[str] = None


@dataclass
class DisplayConfig:
    """Display/UI metadata for a coin."""
    full_name: str
    short_name: str
    coingecko_id: Optional[str] = None
    explorer_url: Optional[str] = None


@dataclass
class ValidationResult:
    """Result of validating manifest against pool API."""
    valid: bool
    errors: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)
    registered_coins: List[str] = field(default_factory=list)
    missing_in_pool: List[str] = field(default_factory=list)
    extra_in_pool: List[str] = field(default_factory=list)


@dataclass
class CoinDefinition:
    """Complete coin definition from manifest."""
    symbol: str
    name: str
    algorithm: str
    role: str  # parent, aux, standalone
    network: NetworkConfig
    address: AddressConfig
    chain: ChainConfig
    merge_mining: Optional[MergeMiningConfig]
    display: DisplayConfig

    @property
    def block_time(self) -> int:
        """Convenience accessor for block time."""
        return self.chain.block_time

    @property
    def is_sha256d(self) -> bool:
        """Check if coin uses SHA-256d algorithm."""
        return self.algorithm == "sha256d"

    @property
    def is_scrypt(self) -> bool:
        """Check if coin uses Scrypt algorithm."""
        return self.algorithm == "scrypt"

    @property
    def is_parent(self) -> bool:
        """Check if coin can serve as merge-mining parent."""
        return self.role == "parent"

    @property
    def is_aux(self) -> bool:
        """Check if coin is a merge-minable auxiliary chain."""
        return self.role == "aux"

    @property
    def supports_auxpow(self) -> bool:
        """Check if coin supports AuxPoW."""
        return self.merge_mining is not None and self.merge_mining.supports_auxpow


# ═══════════════════════════════════════════════════════════════════════════════
# MANIFEST CLASS
# ═══════════════════════════════════════════════════════════════════════════════

class CoinManifest:
    """
    Coin manifest loader and accessor.

    Provides access to all coin definitions from the canonical manifest file.
    Validates basic structure but does NOT validate against Go constants
    (that validation happens in the Go code at pool startup).
    """

    def __init__(self, path: Optional[Path] = None):
        """
        Initialize and load the manifest.

        Args:
            path: Optional path to manifest file. If None, auto-discovers.
        """
        self.path = path or find_manifest_path()
        self._raw: Dict[str, Any] = {}
        self._coins: Dict[str, CoinDefinition] = {}
        self._schema_version: str = ""
        self.load()

    def load(self) -> None:
        """Load manifest from disk."""
        if not self.path.exists():
            raise FileNotFoundError(f"Coin manifest not found at {self.path}")

        with open(self.path, 'r', encoding='utf-8') as f:
            self._raw = yaml.safe_load(f)

        if not self._raw:
            raise ValueError("Empty manifest file")

        self._schema_version = self._raw.get('schema_version', '')
        if self._schema_version != "1.0":
            raise ValueError(f"Unsupported manifest schema version: {self._schema_version}")

        # Parse all coins
        self._coins = {}
        for coin_data in self._raw.get('coins', []):
            coin = self._parse_coin(coin_data)
            self._coins[coin.symbol.upper()] = coin

    def _parse_coin(self, data: Dict[str, Any]) -> CoinDefinition:
        """Parse a coin definition from raw YAML data."""
        # Parse network config
        net_data = data.get('network', {})
        network = NetworkConfig(
            rpc_port=net_data.get('rpc_port', 0),
            p2p_port=net_data.get('p2p_port', 0),
            zmq_port=net_data.get('zmq_port', 0),
            shared_node=net_data.get('shared_node'),
        )

        # Parse address config
        addr_data = data.get('address', {})
        address = AddressConfig(
            p2pkh_version=addr_data.get('p2pkh_version', 0),
            p2sh_version=addr_data.get('p2sh_version', 0),
            bech32_hrp=addr_data.get('bech32_hrp'),
            supports_cashaddr=addr_data.get('supports_cashaddr', False),
            collision_warning=addr_data.get('collision_warning'),
        )

        # Parse chain config
        chain_data = data.get('chain', {})
        chain = ChainConfig(
            genesis_hash=chain_data.get('genesis_hash', ''),
            block_time=chain_data.get('block_time', 0),
            coinbase_maturity=chain_data.get('coinbase_maturity', 100),
            supports_segwit=chain_data.get('supports_segwit', False),
            min_coinbase_script_len=chain_data.get('min_coinbase_script_len', 2),
        )

        # Parse merge-mining config (optional)
        mm_data = data.get('merge_mining')
        merge_mining = None
        if mm_data:
            merge_mining = MergeMiningConfig(
                can_be_parent=mm_data.get('can_be_parent', False),
                supported_aux_algorithms=mm_data.get('supported_aux_algorithms', []),
                supports_auxpow=mm_data.get('supports_auxpow', False),
                auxpow_start_height=mm_data.get('auxpow_start_height', 0),
                chain_id=mm_data.get('chain_id', 0),
                version_bit=mm_data.get('version_bit', 0),
                parent_chain=mm_data.get('parent_chain'),
            )

        # Parse display config
        disp_data = data.get('display', {})
        display = DisplayConfig(
            full_name=disp_data.get('full_name', data.get('name', '')),
            short_name=disp_data.get('short_name', data.get('symbol', '')),
            coingecko_id=disp_data.get('coingecko_id'),
            explorer_url=disp_data.get('explorer_url'),
        )

        return CoinDefinition(
            symbol=data.get('symbol', ''),
            name=data.get('name', ''),
            algorithm=data.get('algorithm', ''),
            role=data.get('role', 'standalone'),
            network=network,
            address=address,
            chain=chain,
            merge_mining=merge_mining,
            display=display,
        )

    @property
    def schema_version(self) -> str:
        """Get manifest schema version."""
        return self._schema_version

    def get_coin(self, symbol: str) -> Optional[CoinDefinition]:
        """
        Get coin definition by symbol.

        Args:
            symbol: Coin symbol (case-insensitive)

        Returns:
            CoinDefinition or None if not found
        """
        return self._coins.get(symbol.upper())

    def list_coins(self, algorithm: Optional[str] = None, role: Optional[str] = None) -> List[str]:
        """
        List coin symbols, optionally filtered.

        Args:
            algorithm: Filter by algorithm (sha256d, scrypt)
            role: Filter by role (parent, aux, standalone)

        Returns:
            List of coin symbols
        """
        coins = list(self._coins.values())

        if algorithm:
            coins = [c for c in coins if c.algorithm == algorithm]
        if role:
            coins = [c for c in coins if c.role == role]

        return [c.symbol for c in coins]

    def list_sha256d_coins(self) -> List[str]:
        """List all SHA-256d coins."""
        return self.list_coins(algorithm="sha256d")

    def list_scrypt_coins(self) -> List[str]:
        """List all Scrypt coins."""
        return self.list_coins(algorithm="scrypt")

    def list_auxpow_coins(self) -> List[str]:
        """List all coins that support AuxPoW (merge-mining as auxiliary)."""
        return [
            c.symbol for c in self._coins.values()
            if c.merge_mining and c.merge_mining.supports_auxpow
        ]

    def list_parent_coins(self) -> List[str]:
        """List all coins that can serve as merge-mining parents."""
        return [
            c.symbol for c in self._coins.values()
            if c.merge_mining and c.merge_mining.can_be_parent
        ]

    def get_parent_for(self, symbol: str) -> Optional[str]:
        """
        Get the parent chain for a merge-minable coin.

        Args:
            symbol: Auxiliary coin symbol

        Returns:
            Parent chain symbol or None
        """
        coin = self.get_coin(symbol)
        if coin and coin.merge_mining:
            return coin.merge_mining.parent_chain
        return None

    def get_aux_coins_for_parent(self, parent_symbol: str) -> List[str]:
        """
        Get all auxiliary coins that can be merge-mined with a parent.

        Args:
            parent_symbol: Parent chain symbol (BTC, LTC)

        Returns:
            List of auxiliary coin symbols
        """
        parent = self.get_coin(parent_symbol)
        if not parent or not parent.merge_mining or not parent.merge_mining.can_be_parent:
            return []

        return [
            c.symbol for c in self._coins.values()
            if c.merge_mining
            and c.merge_mining.supports_auxpow
            and c.merge_mining.parent_chain == parent_symbol.upper()
        ]

    def validate_against_pool(
        self,
        pool_api_url: str,
        timeout: float = 10.0,
        strict: bool = True
    ) -> ValidationResult:
        """
        Validate manifest against the pool's registered coins API.

        This ensures that every coin in the manifest has a corresponding
        Go implementation registered in the pool. Prevents partial coin
        support where Sentinel/Dashboard show coins the pool can't mine.

        Args:
            pool_api_url: Base URL of the pool API (e.g., "http://localhost:4000")
            timeout: Request timeout in seconds
            strict: If True, treat missing coins as errors; if False, as warnings

        Returns:
            ValidationResult with validation status and details

        Example:
            result = manifest.validate_against_pool("http://localhost:4000")
            if not result.valid:
                for err in result.errors:
                    print(f"ERROR: {err}")
                sys.exit(1)
        """
        result = ValidationResult(valid=True)

        # Fetch registered coins from pool API
        coins_url = f"{pool_api_url.rstrip('/')}/api/coins"
        try:
            req = Request(coins_url, headers={"Accept": "application/json"})
            with urlopen(req, timeout=timeout) as response:
                data = json.loads(response.read().decode('utf-8'))
        except HTTPError as e:
            result.valid = False
            result.errors.append(f"Pool API returned HTTP {e.code}: {e.reason}")
            return result
        except URLError as e:
            result.valid = False
            result.errors.append(f"Failed to connect to pool API at {coins_url}: {e.reason}")
            return result
        except json.JSONDecodeError as e:
            result.valid = False
            result.errors.append(f"Invalid JSON response from pool API: {e}")
            return result
        except Exception as e:
            result.valid = False
            result.errors.append(f"Unexpected error fetching pool coins: {e}")
            return result

        # Parse registered coins from API response
        pool_coins = {}
        for coin_data in data.get('coins', []):
            symbol = coin_data.get('symbol', '').upper()
            if symbol:
                pool_coins[symbol] = coin_data
                result.registered_coins.append(symbol)

        # Check each manifest coin exists in pool registry
        manifest_symbols = set(self._coins.keys())
        pool_symbols = set(pool_coins.keys())

        # Find coins in manifest but not in pool
        missing = manifest_symbols - pool_symbols
        for symbol in missing:
            # Some coins register with aliases (e.g., DOGECOIN -> DOGE)
            # Check if the primary symbol exists
            coin = self._coins[symbol]
            msg = f"Coin {symbol} ({coin.name}) is in manifest but not registered in pool"
            if strict:
                result.valid = False
                result.errors.append(msg)
            else:
                result.warnings.append(msg)
            result.missing_in_pool.append(symbol)

        # Find coins in pool but not in manifest (informational)
        extra = pool_symbols - manifest_symbols
        # Filter out aliases (coins may register multiple times)
        for symbol in extra:
            pool_coin = pool_coins[symbol]
            # Check if this is an alias for a manifest coin
            primary = pool_coin.get('symbol', '').upper()
            if primary not in manifest_symbols:
                result.extra_in_pool.append(symbol)
                result.warnings.append(
                    f"Coin {symbol} is registered in pool but not in manifest"
                )

        # Validate consensus-critical values match
        for symbol, coin in self._coins.items():
            if symbol not in pool_coins:
                continue  # Already reported as missing

            pool_coin = pool_coins[symbol]

            # Validate algorithm matches
            pool_algo = pool_coin.get('algorithm', '')
            if pool_algo and pool_algo != coin.algorithm:
                result.valid = False
                result.errors.append(
                    f"{symbol}: Algorithm mismatch - manifest={coin.algorithm}, pool={pool_algo}"
                )

            # Validate ChainID for AuxPoW coins
            if coin.merge_mining and coin.merge_mining.supports_auxpow:
                pool_mm = pool_coin.get('mergeMining', {})
                pool_chain_id = pool_mm.get('chainId', 0)
                if pool_chain_id and pool_chain_id != coin.merge_mining.chain_id:
                    result.valid = False
                    result.errors.append(
                        f"{symbol}: ChainID mismatch - manifest={coin.merge_mining.chain_id}, pool={pool_chain_id}"
                    )

            # Validate genesis hash
            pool_chain = pool_coin.get('chain', {})
            pool_genesis = pool_chain.get('genesisHash', '')
            if pool_genesis and pool_genesis.lower() != coin.chain.genesis_hash.lower():
                result.valid = False
                result.errors.append(
                    f"{symbol}: Genesis hash mismatch - manifest does not match pool"
                )

        logger.info(
            f"Manifest validation: {len(manifest_symbols)} manifest coins, "
            f"{len(pool_symbols)} pool coins, "
            f"{len(result.errors)} errors, {len(result.warnings)} warnings"
        )

        return result

    def coin_count(self) -> int:
        """Get total number of coins in manifest."""
        return len(self._coins)

    def __contains__(self, symbol: str) -> bool:
        """Check if coin symbol is in manifest."""
        return symbol.upper() in self._coins

    def __iter__(self):
        """Iterate over coin definitions."""
        return iter(self._coins.values())

    def __len__(self) -> int:
        """Get number of coins."""
        return len(self._coins)


# ═══════════════════════════════════════════════════════════════════════════════
# GLOBAL SINGLETON
# ═══════════════════════════════════════════════════════════════════════════════

_manifest: Optional[CoinManifest] = None


def get_manifest(reload: bool = False) -> CoinManifest:
    """
    Get the loaded coin manifest (singleton).

    Args:
        reload: Force reload from disk

    Returns:
        CoinManifest instance
    """
    global _manifest
    if _manifest is None or reload:
        _manifest = CoinManifest()
    return _manifest


def get_coin(symbol: str) -> Optional[CoinDefinition]:
    """
    Convenience function to get a coin definition.

    Args:
        symbol: Coin symbol

    Returns:
        CoinDefinition or None
    """
    return get_manifest().get_coin(symbol)


def is_sha256d(symbol: str) -> bool:
    """Check if coin uses SHA-256d algorithm."""
    coin = get_coin(symbol)
    return coin is not None and coin.is_sha256d


def is_scrypt(symbol: str) -> bool:
    """Check if coin uses Scrypt algorithm."""
    coin = get_coin(symbol)
    return coin is not None and coin.is_scrypt


def get_block_time(symbol: str) -> int:
    """Get block time for a coin in seconds."""
    coin = get_coin(symbol)
    return coin.block_time if coin else 0


def validate_manifest_against_pool(
    pool_api_url: str,
    timeout: float = 10.0,
    strict: bool = True
) -> ValidationResult:
    """
    Convenience function to validate manifest against pool API.

    Args:
        pool_api_url: Base URL of the pool API
        timeout: Request timeout in seconds
        strict: If True, treat missing coins as errors

    Returns:
        ValidationResult
    """
    return get_manifest().validate_against_pool(pool_api_url, timeout, strict)


# ═══════════════════════════════════════════════════════════════════════════════
# CLI INTERFACE
# ═══════════════════════════════════════════════════════════════════════════════

def main():
    """CLI interface for testing manifest loading."""
    import argparse

    parser = argparse.ArgumentParser(description="Coin Manifest Loader")
    parser.add_argument("--list", action="store_true", help="List all coins")
    parser.add_argument("--coin", type=str, help="Show details for specific coin")
    parser.add_argument("--algorithm", type=str, choices=["sha256d", "scrypt"],
                        help="Filter by algorithm")
    parser.add_argument("--auxpow", action="store_true", help="List AuxPoW coins")
    parser.add_argument("--parents", action="store_true", help="List parent chains")
    parser.add_argument("--path", type=str, help="Path to manifest file")
    parser.add_argument("--validate", type=str, metavar="POOL_URL",
                        help="Validate manifest against pool API (e.g., http://localhost:4000)")
    parser.add_argument("--strict", action="store_true", default=True,
                        help="Strict validation (missing coins are errors)")

    args = parser.parse_args()

    try:
        if args.path:
            manifest = CoinManifest(Path(args.path))
        else:
            manifest = get_manifest()

        print(f"Manifest loaded: {manifest.path}")
        print(f"Schema version: {manifest.schema_version}")
        print(f"Total coins: {manifest.coin_count()}")
        print()

        if args.validate:
            # Validate manifest against pool API
            print(f"Validating manifest against pool API: {args.validate}")
            result = manifest.validate_against_pool(args.validate, strict=args.strict)

            print(f"\nPool registered coins ({len(result.registered_coins)}): {result.registered_coins}")

            if result.missing_in_pool:
                print(f"\nMissing in pool ({len(result.missing_in_pool)}):")
                for symbol in result.missing_in_pool:
                    coin = manifest.get_coin(symbol)
                    print(f"  ERROR: {symbol} ({coin.name}) - not registered in pool")

            if result.extra_in_pool:
                print(f"\nExtra in pool ({len(result.extra_in_pool)}):")
                for symbol in result.extra_in_pool:
                    print(f"  WARNING: {symbol} - in pool but not in manifest")

            if result.warnings:
                print(f"\nWarnings ({len(result.warnings)}):")
                for w in result.warnings:
                    print(f"  WARNING: {w}")

            if result.errors:
                print(f"\nErrors ({len(result.errors)}):")
                for e in result.errors:
                    print(f"  ERROR: {e}")

            if result.valid:
                print(f"\nValidation PASSED - all manifest coins registered in pool")
                return 0
            else:
                print(f"\nValidation FAILED with {len(result.errors)} error(s)")
                return 1

        elif args.coin:
            coin = manifest.get_coin(args.coin)
            if coin:
                print(f"Symbol: {coin.symbol}")
                print(f"Name: {coin.name}")
                print(f"Algorithm: {coin.algorithm}")
                print(f"Role: {coin.role}")
                print(f"Block time: {coin.block_time}s")
                print(f"SegWit: {coin.chain.supports_segwit}")
                if coin.merge_mining:
                    if coin.merge_mining.supports_auxpow:
                        print(f"AuxPoW: Yes (start height: {coin.merge_mining.auxpow_start_height})")
                        print(f"Chain ID: {coin.merge_mining.chain_id}")
                        print(f"Parent chain: {coin.merge_mining.parent_chain}")
                    if coin.merge_mining.can_be_parent:
                        print(f"Can be parent: Yes")
                        aux = manifest.get_aux_coins_for_parent(coin.symbol)
                        print(f"Aux chains: {aux}")
            else:
                print(f"Coin not found: {args.coin}")

        elif args.auxpow:
            coins = manifest.list_auxpow_coins()
            print(f"AuxPoW coins ({len(coins)}):")
            for c in coins:
                coin = manifest.get_coin(c)
                print(f"  {c}: parent={coin.merge_mining.parent_chain}, chain_id={coin.merge_mining.chain_id}")

        elif args.parents:
            coins = manifest.list_parent_coins()
            print(f"Parent chains ({len(coins)}):")
            for c in coins:
                coin = manifest.get_coin(c)
                aux = manifest.get_aux_coins_for_parent(c)
                print(f"  {c} ({coin.algorithm}): can merge-mine {aux}")

        elif args.list or args.algorithm:
            coins = manifest.list_coins(algorithm=args.algorithm)
            algo_label = f" ({args.algorithm})" if args.algorithm else ""
            print(f"Coins{algo_label} ({len(coins)}):")
            for c in coins:
                coin = manifest.get_coin(c)
                role_label = f" [{coin.role}]" if coin.role != "standalone" else ""
                print(f"  {c}: {coin.name} ({coin.algorithm}){role_label}")

        else:
            # Default: summary
            sha256d = manifest.list_sha256d_coins()
            scrypt = manifest.list_scrypt_coins()
            auxpow = manifest.list_auxpow_coins()
            parents = manifest.list_parent_coins()

            print(f"SHA-256d coins ({len(sha256d)}): {sha256d}")
            print(f"Scrypt coins ({len(scrypt)}): {scrypt}")
            print(f"AuxPoW coins ({len(auxpow)}): {auxpow}")
            print(f"Parent chains ({len(parents)}): {parents}")

    except Exception as e:
        print(f"Error: {e}")
        return 1

    return 0


if __name__ == "__main__":
    exit(main())
