#!/usr/bin/env python3

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
Manifest Validation Script

Validates the coin manifest structure and consistency.
This is a quick Python validation that can be run before Go tests.

Usage:
    python scripts/validate-manifest.py
    python scripts/validate-manifest.py --verbose
    python scripts/validate-manifest.py --path /custom/path/coins.manifest.yaml
"""

import sys
import argparse
from pathlib import Path

# Add parent directories to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent / "src" / "sentinel"))
sys.path.insert(0, str(Path(__file__).parent.parent / "src" / "dashboard"))

try:
    import yaml
except ImportError:
    print("ERROR: PyYAML not installed. Run: pip install pyyaml")
    sys.exit(1)


def find_manifest_path() -> Path:
    """Find the manifest file."""
    script_dir = Path(__file__).parent
    paths = [
        script_dir.parent / "config" / "coins.manifest.yaml",
        script_dir.parent / "RELEASE-V1.1.1-PHI_FORGE" / "config" / "coins.manifest.yaml",
        Path("/spiralpool/config/coins.manifest.yaml"),
    ]
    for p in paths:
        if p.exists():
            return p
    return paths[0]


def validate_manifest(path: Path, verbose: bool = False) -> tuple[bool, list[str], list[str]]:
    """
    Validate manifest structure.

    Returns:
        (success, errors, warnings)
    """
    errors = []
    warnings = []

    # Load manifest
    if not path.exists():
        return False, [f"Manifest not found at {path}"], []

    try:
        with open(path) as f:
            manifest = yaml.safe_load(f)
    except yaml.YAMLError as e:
        return False, [f"YAML parse error: {e}"], []

    if not manifest:
        return False, ["Empty manifest"], []

    # Check schema version
    schema_version = manifest.get("schema_version")
    if not schema_version:
        errors.append("Missing schema_version")
    elif schema_version != "1.0":
        errors.append(f"Unsupported schema_version: {schema_version} (expected 1.0)")

    # Check coins
    coins = manifest.get("coins", [])
    if not coins:
        errors.append("No coins defined")
        return False, errors, warnings

    seen_symbols = set()
    seen_rpc_ports = {}
    seen_p2p_ports = {}
    seen_zmq_ports = {}

    required_fields = ["symbol", "name", "algorithm", "role"]
    required_network = ["rpc_port", "p2p_port", "zmq_port"]
    required_address = ["p2pkh_version", "p2sh_version"]
    required_chain = ["genesis_hash", "block_time", "coinbase_maturity", "supports_segwit"]

    valid_algorithms = ["sha256d", "scrypt"]
    valid_roles = ["parent", "aux", "standalone"]

    for i, coin in enumerate(coins):
        prefix = f"Coin[{i}]"
        symbol = coin.get("symbol", f"UNKNOWN_{i}")

        # Required fields
        for field in required_fields:
            if field not in coin:
                errors.append(f"{prefix} ({symbol}): missing required field '{field}'")

        # Symbol uniqueness
        if symbol in seen_symbols:
            errors.append(f"{prefix}: duplicate symbol '{symbol}'")
        seen_symbols.add(symbol)

        # Algorithm validation
        algo = coin.get("algorithm")
        if algo and algo not in valid_algorithms:
            errors.append(f"{prefix} ({symbol}): invalid algorithm '{algo}' (must be: {valid_algorithms})")

        # Role validation
        role = coin.get("role")
        if role and role not in valid_roles:
            errors.append(f"{prefix} ({symbol}): invalid role '{role}' (must be: {valid_roles})")

        # Network section
        network = coin.get("network", {})
        shared_node = network.get("shared_node")

        for field in required_network:
            if field not in network:
                errors.append(f"{prefix} ({symbol}): missing network.{field}")

        # Port uniqueness (skip if shared node)
        if not shared_node:
            rpc_port = network.get("rpc_port")
            p2p_port = network.get("p2p_port")
            zmq_port = network.get("zmq_port")

            if rpc_port:
                if rpc_port in seen_rpc_ports:
                    errors.append(f"{prefix} ({symbol}): duplicate RPC port {rpc_port} (also used by {seen_rpc_ports[rpc_port]})")
                seen_rpc_ports[rpc_port] = symbol

            if p2p_port:
                if p2p_port in seen_p2p_ports:
                    errors.append(f"{prefix} ({symbol}): duplicate P2P port {p2p_port} (also used by {seen_p2p_ports[p2p_port]})")
                seen_p2p_ports[p2p_port] = symbol

            if zmq_port:
                if zmq_port in seen_zmq_ports:
                    errors.append(f"{prefix} ({symbol}): duplicate ZMQ port {zmq_port} (also used by {seen_zmq_ports[zmq_port]})")
                seen_zmq_ports[zmq_port] = symbol

        # Address section
        address = coin.get("address", {})
        for field in required_address:
            if field not in address:
                errors.append(f"{prefix} ({symbol}): missing address.{field}")

        # Chain section
        chain = coin.get("chain", {})
        for field in required_chain:
            if field not in chain:
                errors.append(f"{prefix} ({symbol}): missing chain.{field}")

        genesis = chain.get("genesis_hash", "")
        if genesis and len(genesis) != 64:
            warnings.append(f"{prefix} ({symbol}): genesis_hash should be 64 hex characters (got {len(genesis)})")

        block_time = chain.get("block_time", 0)
        if block_time <= 0:
            errors.append(f"{prefix} ({symbol}): block_time must be positive")

        # Role-specific validation
        if role == "aux":
            mm = coin.get("merge_mining", {})
            if not mm.get("supports_auxpow"):
                errors.append(f"{prefix} ({symbol}): role=aux but merge_mining.supports_auxpow is not true")
            if not mm.get("parent_chain"):
                errors.append(f"{prefix} ({symbol}): role=aux but merge_mining.parent_chain is not set")
            if not mm.get("chain_id"):
                errors.append(f"{prefix} ({symbol}): role=aux but merge_mining.chain_id is not set")
            if not mm.get("version_bit"):
                errors.append(f"{prefix} ({symbol}): role=aux but merge_mining.version_bit is not set")

        if role == "parent":
            mm = coin.get("merge_mining", {})
            if not mm.get("can_be_parent"):
                errors.append(f"{prefix} ({symbol}): role=parent but merge_mining.can_be_parent is not true")

        # Check merge-mining algorithm boundaries
        mm = coin.get("merge_mining", {})
        parent_chain = mm.get("parent_chain")
        if parent_chain:
            # Find parent coin
            parent_coin = None
            for pc in coins:
                if pc.get("symbol") == parent_chain:
                    parent_coin = pc
                    break

            if not parent_coin:
                errors.append(f"{prefix} ({symbol}): parent_chain '{parent_chain}' not found in manifest")
            elif parent_coin.get("algorithm") != algo:
                errors.append(f"{prefix} ({symbol}): algorithm mismatch with parent - "
                            f"{symbol} is {algo}, parent {parent_chain} is {parent_coin.get('algorithm')}")

    # Summary
    if verbose:
        print(f"\nManifest: {path}")
        print(f"Schema version: {schema_version}")
        print(f"Total coins: {len(coins)}")

        sha256d = [c["symbol"] for c in coins if c.get("algorithm") == "sha256d"]
        scrypt = [c["symbol"] for c in coins if c.get("algorithm") == "scrypt"]
        parents = [c["symbol"] for c in coins if c.get("role") == "parent"]
        aux = [c["symbol"] for c in coins if c.get("role") == "aux"]

        print(f"SHA-256d coins ({len(sha256d)}): {sha256d}")
        print(f"Scrypt coins ({len(scrypt)}): {scrypt}")
        print(f"Parent chains ({len(parents)}): {parents}")
        print(f"AuxPoW coins ({len(aux)}): {aux}")

    success = len(errors) == 0
    return success, errors, warnings


def main():
    parser = argparse.ArgumentParser(description="Validate coin manifest")
    parser.add_argument("--path", type=str, help="Path to manifest file")
    parser.add_argument("--verbose", "-v", action="store_true", help="Verbose output")
    args = parser.parse_args()

    path = Path(args.path) if args.path else find_manifest_path()

    print(f"Validating manifest: {path}")
    success, errors, warnings = validate_manifest(path, verbose=args.verbose)

    if warnings:
        print(f"\nWarnings ({len(warnings)}):")
        for w in warnings:
            print(f"  WARNING: {w}")

    if errors:
        print(f"\nErrors ({len(errors)}):")
        for e in errors:
            print(f"  ERROR: {e}")
        print(f"\nValidation FAILED with {len(errors)} error(s)")
        return 1

    print(f"\nValidation PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
