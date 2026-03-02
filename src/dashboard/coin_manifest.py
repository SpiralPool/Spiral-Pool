#!/usr/bin/env python3
# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""
Coin Manifest Loader for Dashboard

This is a symlink/copy of the sentinel coin_manifest.py module.
Both Sentinel and Dashboard use the same manifest loader.

See src/sentinel/coin_manifest.py for full documentation.
"""

# Import everything from the sentinel version
# In production, this would be a proper shared package
# For now, we duplicate the file for simplicity

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


def find_manifest_path() -> Path:
    """Find the coin manifest file."""
    env_path = os.environ.get("SPIRALPOOL_MANIFEST_PATH")
    if env_path and Path(env_path).exists():
        return Path(env_path)

    paths = [
        Path("/spiralpool/config/coins.manifest.yaml"),
        Path("./config/coins.manifest.yaml"),
        Path("../config/coins.manifest.yaml"),
        Path("../../config/coins.manifest.yaml"),
        Path(__file__).parent.parent.parent / "config" / "coins.manifest.yaml",
    ]

    for p in paths:
        if p.exists():
            return p.resolve()

    return paths[0]


@dataclass
class NetworkConfig:
    rpc_port: int
    p2p_port: int
    zmq_port: int
    shared_node: Optional[str] = None


@dataclass
class AddressConfig:
    p2pkh_version: int
    p2sh_version: int
    bech32_hrp: Optional[str] = None
    supports_cashaddr: bool = False
    collision_warning: Optional[str] = None


@dataclass
class ChainConfig:
    genesis_hash: str
    block_time: int
    coinbase_maturity: int
    supports_segwit: bool
    min_coinbase_script_len: int = 2


@dataclass
class MergeMiningConfig:
    can_be_parent: bool = False
    supported_aux_algorithms: List[str] = field(default_factory=list)
    supports_auxpow: bool = False
    auxpow_start_height: int = 0
    chain_id: int = 0
    version_bit: int = 0
    parent_chain: Optional[str] = None


@dataclass
class DisplayConfig:
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
    symbol: str
    name: str
    algorithm: str
    role: str
    network: NetworkConfig
    address: AddressConfig
    chain: ChainConfig
    merge_mining: Optional[MergeMiningConfig]
    display: DisplayConfig

    @property
    def block_time(self) -> int:
        return self.chain.block_time

    @property
    def is_sha256d(self) -> bool:
        return self.algorithm == "sha256d"

    @property
    def is_scrypt(self) -> bool:
        return self.algorithm == "scrypt"

    @property
    def is_parent(self) -> bool:
        return self.role == "parent"

    @property
    def is_aux(self) -> bool:
        return self.role == "aux"

    @property
    def supports_auxpow(self) -> bool:
        return self.merge_mining is not None and self.merge_mining.supports_auxpow


class CoinManifest:
    """Coin manifest loader and accessor."""

    def __init__(self, path: Optional[Path] = None):
        self.path = path or find_manifest_path()
        self._raw: Dict[str, Any] = {}
        self._coins: Dict[str, CoinDefinition] = {}
        self._schema_version: str = ""
        self.load()

    def load(self) -> None:
        if not self.path.exists():
            raise FileNotFoundError(f"Coin manifest not found at {self.path}")

        with open(self.path, 'r', encoding='utf-8') as f:
            self._raw = yaml.safe_load(f)

        if not self._raw:
            raise ValueError("Empty manifest file")

        self._schema_version = self._raw.get('schema_version', '')
        if self._schema_version != "1.0":
            raise ValueError(f"Unsupported manifest schema version: {self._schema_version}")

        self._coins = {}
        for coin_data in self._raw.get('coins', []):
            coin = self._parse_coin(coin_data)
            self._coins[coin.symbol.upper()] = coin

    def _parse_coin(self, data: Dict[str, Any]) -> CoinDefinition:
        net_data = data.get('network', {})
        network = NetworkConfig(
            rpc_port=net_data.get('rpc_port', 0),
            p2p_port=net_data.get('p2p_port', 0),
            zmq_port=net_data.get('zmq_port', 0),
            shared_node=net_data.get('shared_node'),
        )

        addr_data = data.get('address', {})
        address = AddressConfig(
            p2pkh_version=addr_data.get('p2pkh_version', 0),
            p2sh_version=addr_data.get('p2sh_version', 0),
            bech32_hrp=addr_data.get('bech32_hrp'),
            supports_cashaddr=addr_data.get('supports_cashaddr', False),
            collision_warning=addr_data.get('collision_warning'),
        )

        chain_data = data.get('chain', {})
        chain = ChainConfig(
            genesis_hash=chain_data.get('genesis_hash', ''),
            block_time=chain_data.get('block_time', 0),
            coinbase_maturity=chain_data.get('coinbase_maturity', 100),
            supports_segwit=chain_data.get('supports_segwit', False),
            min_coinbase_script_len=chain_data.get('min_coinbase_script_len', 2),
        )

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
        return self._schema_version

    def get_coin(self, symbol: str) -> Optional[CoinDefinition]:
        return self._coins.get(symbol.upper())

    def list_coins(self, algorithm: Optional[str] = None, role: Optional[str] = None) -> List[str]:
        coins = list(self._coins.values())
        if algorithm:
            coins = [c for c in coins if c.algorithm == algorithm]
        if role:
            coins = [c for c in coins if c.role == role]
        return [c.symbol for c in coins]

    def list_sha256d_coins(self) -> List[str]:
        return self.list_coins(algorithm="sha256d")

    def list_scrypt_coins(self) -> List[str]:
        return self.list_coins(algorithm="scrypt")

    def list_auxpow_coins(self) -> List[str]:
        return [c.symbol for c in self._coins.values() if c.merge_mining and c.merge_mining.supports_auxpow]

    def list_parent_coins(self) -> List[str]:
        return [c.symbol for c in self._coins.values() if c.merge_mining and c.merge_mining.can_be_parent]

    def get_parent_for(self, symbol: str) -> Optional[str]:
        coin = self.get_coin(symbol)
        if coin and coin.merge_mining:
            return coin.merge_mining.parent_chain
        return None

    def get_aux_coins_for_parent(self, parent_symbol: str) -> List[str]:
        parent = self.get_coin(parent_symbol)
        if not parent or not parent.merge_mining or not parent.merge_mining.can_be_parent:
            return []
        return [c.symbol for c in self._coins.values()
                if c.merge_mining and c.merge_mining.supports_auxpow
                and c.merge_mining.parent_chain == parent_symbol.upper()]

    def validate_against_pool(
        self,
        pool_api_url: str,
        timeout: float = 10.0,
        strict: bool = True
    ) -> ValidationResult:
        """
        Validate manifest against the pool's registered coins API.

        Args:
            pool_api_url: Base URL of the pool API (e.g., "http://localhost:4000")
            timeout: Request timeout in seconds
            strict: If True, treat missing coins as errors

        Returns:
            ValidationResult with validation status and details
        """
        result = ValidationResult(valid=True)

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
            result.errors.append(f"Failed to connect to pool API: {e.reason}")
            return result
        except Exception as e:
            result.valid = False
            result.errors.append(f"Unexpected error: {e}")
            return result

        pool_coins = {}
        for coin_data in data.get('coins', []):
            symbol = coin_data.get('symbol', '').upper()
            if symbol:
                pool_coins[symbol] = coin_data
                result.registered_coins.append(symbol)

        manifest_symbols = set(self._coins.keys())
        pool_symbols = set(pool_coins.keys())

        missing = manifest_symbols - pool_symbols
        for symbol in missing:
            coin = self._coins[symbol]
            msg = f"Coin {symbol} ({coin.name}) is in manifest but not in pool"
            if strict:
                result.valid = False
                result.errors.append(msg)
            else:
                result.warnings.append(msg)
            result.missing_in_pool.append(symbol)

        extra = pool_symbols - manifest_symbols
        for symbol in extra:
            result.extra_in_pool.append(symbol)
            result.warnings.append(f"Coin {symbol} in pool but not in manifest")

        # Validate consensus values match
        for symbol, coin in self._coins.items():
            if symbol not in pool_coins:
                continue
            pool_coin = pool_coins[symbol]
            pool_algo = pool_coin.get('algorithm', '')
            if pool_algo and pool_algo != coin.algorithm:
                result.valid = False
                result.errors.append(f"{symbol}: Algorithm mismatch")

        logger.info(f"Manifest validation: {len(result.errors)} errors")
        return result

    def coin_count(self) -> int:
        return len(self._coins)

    def to_api_response(self) -> List[Dict[str, Any]]:
        """Convert manifest to API response format for dashboard."""
        result = []
        for coin in self._coins.values():
            item = {
                'symbol': coin.symbol,
                'name': coin.name,
                'algorithm': coin.algorithm,
                'role': coin.role,
                'block_time': coin.block_time,
                'supports_segwit': coin.chain.supports_segwit,
                'display': {
                    'full_name': coin.display.full_name,
                    'short_name': coin.display.short_name,
                    'coingecko_id': coin.display.coingecko_id,
                    'explorer_url': coin.display.explorer_url,
                },
                'network': {
                    'rpc_port': coin.network.rpc_port,
                    'p2p_port': coin.network.p2p_port,
                    'zmq_port': coin.network.zmq_port,
                },
            }
            if coin.merge_mining:
                item['merge_mining'] = {
                    'can_be_parent': coin.merge_mining.can_be_parent,
                    'supports_auxpow': coin.merge_mining.supports_auxpow,
                    'parent_chain': coin.merge_mining.parent_chain,
                    'chain_id': coin.merge_mining.chain_id,
                }
            result.append(item)
        return result

    def __contains__(self, symbol: str) -> bool:
        return symbol.upper() in self._coins

    def __iter__(self):
        return iter(self._coins.values())

    def __len__(self) -> int:
        return len(self._coins)


# Global singleton
_manifest: Optional[CoinManifest] = None


def get_manifest(reload: bool = False) -> CoinManifest:
    global _manifest
    if _manifest is None or reload:
        _manifest = CoinManifest()
    return _manifest


def get_coin(symbol: str) -> Optional[CoinDefinition]:
    return get_manifest().get_coin(symbol)


def is_sha256d(symbol: str) -> bool:
    coin = get_coin(symbol)
    return coin is not None and coin.is_sha256d


def is_scrypt(symbol: str) -> bool:
    coin = get_coin(symbol)
    return coin is not None and coin.is_scrypt


def get_block_time(symbol: str) -> int:
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
