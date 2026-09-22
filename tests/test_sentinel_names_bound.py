# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors
"""ALL_MINER_TYPES must be assigned before it is read in the same function.

The monitor loop assigns ALL_MINER_TYPES partway through. The rejection-spike
stratum kick read it earlier in that loop, so on a first spike Python raised
UnboundLocalError: the kick never ran and the rest of that monitoring pass was
lost. The kick now looks the miner up with find_miner(), like the zombie path.

Run: python -m pytest tests/test_sentinel_names_bound.py -v
"""
import ast
import os

SENTINEL = os.path.join(os.path.dirname(__file__), "..", "src", "sentinel", "SpiralSentinel.py")


def test_all_miner_types_is_assigned_before_it_is_read():
    with open(SENTINEL, encoding="utf-8") as f:
        tree = ast.parse(f.read())

    checked = 0
    for func in ast.walk(tree):
        if not isinstance(func, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        names = [n for n in ast.walk(func) if isinstance(n, ast.Name) and n.id == "ALL_MINER_TYPES"]
        stores = [(n.lineno, n.col_offset) for n in names if isinstance(n.ctx, ast.Store)]
        if not stores:
            continue
        checked += 1
        first_store = min(stores)
        early = sorted(n.lineno for n in names
                       if isinstance(n.ctx, ast.Load) and (n.lineno, n.col_offset) < first_store)
        assert not early, (f"{func.name} reads ALL_MINER_TYPES on line(s) {early} "
                           f"before assigning it on line {first_store[0]}")
    assert checked, "no function assigns ALL_MINER_TYPES any more; update this test"
