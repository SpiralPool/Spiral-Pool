#!/usr/bin/env python3

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
END-TO-END VERIFICATION AGENT
=============================
Cross-system truth validation for Spiral Pool mining operations.

PURPOSE: Compare Sentinel internal state, Pool API data, and Dashboard UI
         to identify inconsistencies, race windows, and UI misrepresentations.

GOAL: Confirm "What you see is what is actually happening"

DELIVERABLE: Source-of-truth reconciliation report
"""

import json
import time
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Any, Tuple
from enum import Enum
from datetime import datetime, timedelta
from collections import defaultdict


# ═══════════════════════════════════════════════════════════════════════════════
# DATA SOURCE ABSTRACTIONS
# ═══════════════════════════════════════════════════════════════════════════════

@dataclass
class MinerDataPoint:
    """Unified miner data from any source."""
    name: str
    hashrate_ghs: float
    temperature: float
    status: str
    uptime_seconds: int
    shares_accepted: int
    shares_rejected: int
    last_update: float


@dataclass
class PoolDataPoint:
    """Pool-level data from API."""
    connected_miners: int
    total_hashrate_ghs: float
    blocks_found: int
    block_height: int
    network_hashrate_phs: float
    difficulty: float
    last_update: float


@dataclass
class AlertDataPoint:
    """Alert state data."""
    alert_type: str
    miner_name: Optional[str]
    timestamp: float
    was_sent: bool
    suppression_reason: Optional[str]


class DataSource(Enum):
    """Data sources for comparison."""
    SENTINEL = "sentinel"
    POOL_API = "pool_api"
    DASHBOARD_UI = "dashboard"
    MINER_DIRECT = "miner_direct"


# ═══════════════════════════════════════════════════════════════════════════════
# SIMULATED DATA SOURCES (For testing without live system)
# ═══════════════════════════════════════════════════════════════════════════════

class SimulatedSentinelSource:
    """Simulates Sentinel internal state."""

    def __init__(self):
        self.miners: Dict[str, MinerDataPoint] = {}
        self.alerts: List[AlertDataPoint] = []
        self.state = {
            "startup_time": time.time(),
            "last_report_hour": None,
            "maintenance_mode": False,
            "blocks_found": 0,
            "pool_mode": "solo",
            "active_coin": "DGB"
        }

    def setup_test_data(self):
        """Create test data for verification."""
        for i in range(5):
            self.miners[f"Miner-{i+1:02d}"] = MinerDataPoint(
                name=f"Miner-{i+1:02d}",
                hashrate_ghs=500 + (i * 10),
                temperature=45 + i,
                status="online",
                uptime_seconds=3600 * (i + 1),
                shares_accepted=1000 * (i + 1),
                shares_rejected=i * 5,
                last_update=time.time()
            )

    def get_miner_data(self, name: str) -> Optional[MinerDataPoint]:
        return self.miners.get(name)

    def get_all_miners(self) -> Dict[str, MinerDataPoint]:
        return self.miners.copy()

    def get_total_hashrate(self) -> float:
        return sum(m.hashrate_ghs for m in self.miners.values() if m.status == "online")

    def get_alerts(self) -> List[AlertDataPoint]:
        return self.alerts.copy()


class SimulatedPoolAPISource:
    """Simulates Pool API responses."""

    def __init__(self):
        self.pool_data = PoolDataPoint(
            connected_miners=5,
            total_hashrate_ghs=2500,
            blocks_found=10,
            block_height=1000000,
            network_hashrate_phs=50.0,
            difficulty=1e9,
            last_update=time.time()
        )
        self.miner_stats: Dict[str, Dict] = {}

    def setup_test_data(self):
        """Create test data matching Sentinel expectations."""
        for i in range(5):
            self.miner_stats[f"Miner-{i+1:02d}"] = {
                "hashrate": 500 + (i * 10),
                "sharesPerSecond": 0.5,
                "validSharesCount": 1000 * (i + 1),
                "invalidSharesCount": i * 5,
            }

    def get_pool_stats(self) -> Dict:
        return {
            "poolStats": {
                "connectedMiners": self.pool_data.connected_miners,
                "poolHashrate": self.pool_data.total_hashrate_ghs * 1e9,
                "blockHeight": self.pool_data.block_height,
            },
            "networkStats": {
                "networkHashrate": self.pool_data.network_hashrate_phs * 1e15,
                "networkDifficulty": self.pool_data.difficulty,
            }
        }

    def get_miner_stats(self, miner_id: str) -> Optional[Dict]:
        return self.miner_stats.get(miner_id)

    def get_all_miner_stats(self) -> Dict[str, Dict]:
        return self.miner_stats.copy()


class SimulatedDashboardSource:
    """Simulates Dashboard UI state."""

    def __init__(self):
        self.displayed_miners: Dict[str, Dict] = {}
        self.displayed_stats: Dict = {}
        self.displayed_alerts: List[Dict] = []
        self.cache_age_seconds: float = 0

    def setup_test_data(self, sentinel_source: SimulatedSentinelSource):
        """Create dashboard data based on Sentinel state."""
        for name, miner in sentinel_source.get_all_miners().items():
            self.displayed_miners[name] = {
                "name": name,
                "hashrate": f"{miner.hashrate_ghs:.1f} GH/s",
                "hashrate_raw": miner.hashrate_ghs,
                "temperature": miner.temperature,
                "status": miner.status,
                "status_emoji": "🟢" if miner.status == "online" else "🔴",
                "uptime": miner.uptime_seconds,
            }

        total_hr = sentinel_source.get_total_hashrate()
        self.displayed_stats = {
            "fleet_hashrate": f"{total_hr / 1000:.2f} TH/s",
            "fleet_hashrate_raw": total_hr,
            "miners_online": len([m for m in sentinel_source.miners.values() if m.status == "online"]),
            "miners_total": len(sentinel_source.miners),
            "active_coin": sentinel_source.state["active_coin"],
        }

    def get_displayed_miner(self, name: str) -> Optional[Dict]:
        return self.displayed_miners.get(name)

    def get_displayed_stats(self) -> Dict:
        return self.displayed_stats.copy()


# ═══════════════════════════════════════════════════════════════════════════════
# VERIFICATION CHECKS
# ═══════════════════════════════════════════════════════════════════════════════

@dataclass
class VerificationResult:
    """Result of a single verification check."""
    check_name: str
    passed: bool
    source_a: str
    source_b: str
    value_a: Any
    value_b: Any
    discrepancy: Optional[str] = None
    severity: str = "info"  # info, warning, error, critical
    recommendation: Optional[str] = None


class E2EVerificationAgent:
    """
    Performs cross-system verification to ensure data consistency
    across Sentinel, Pool API, and Dashboard.
    """

    def __init__(self):
        self.sentinel = SimulatedSentinelSource()
        self.pool_api = SimulatedPoolAPISource()
        self.dashboard = SimulatedDashboardSource()
        self.verification_results: List[VerificationResult] = []
        self.reconciliation_issues: List[Dict] = []

        # Tolerance thresholds
        self.hashrate_tolerance_pct = 5.0  # 5% tolerance for hashrate differences
        self.temp_tolerance_c = 2.0  # 2°C tolerance
        self.share_tolerance_pct = 1.0  # 1% tolerance for share counts

    def setup_test_environment(self):
        """Initialize all data sources with test data."""
        self.sentinel.setup_test_data()
        self.pool_api.setup_test_data()
        self.dashboard.setup_test_data(self.sentinel)

    def run_all_verifications(self) -> Dict:
        """Execute all verification checks and return report."""
        self.verification_results.clear()
        self.reconciliation_issues.clear()

        # Run verification categories
        self._verify_miner_data_consistency()
        self._verify_hashrate_totals()
        self._verify_share_counts()
        self._verify_alert_state()
        self._verify_dashboard_accuracy()
        self._verify_pool_api_accuracy()
        self._detect_race_conditions()
        self._verify_no_ui_lies()

        return self._generate_reconciliation_report()

    # ═══════════════════════════════════════════════════════════════════════════
    # VERIFICATION METHODS
    # ═══════════════════════════════════════════════════════════════════════════

    def _verify_miner_data_consistency(self):
        """Verify miner data matches across all sources."""
        sentinel_miners = self.sentinel.get_all_miners()
        pool_miners = self.pool_api.get_all_miner_stats()
        dashboard_miners = self.dashboard.displayed_miners

        for name in sentinel_miners:
            sentinel_data = sentinel_miners[name]
            pool_data = pool_miners.get(name, {})
            dashboard_data = dashboard_miners.get(name, {})

            # Hashrate consistency (Sentinel vs Pool)
            sentinel_hr = sentinel_data.hashrate_ghs
            pool_hr = pool_data.get("hashrate", 0)
            hr_diff_pct = abs(sentinel_hr - pool_hr) / sentinel_hr * 100 if sentinel_hr > 0 else 0

            self.verification_results.append(VerificationResult(
                check_name=f"miner_hashrate_sentinel_vs_pool_{name}",
                passed=hr_diff_pct <= self.hashrate_tolerance_pct,
                source_a="sentinel",
                source_b="pool_api",
                value_a=sentinel_hr,
                value_b=pool_hr,
                discrepancy=f"{hr_diff_pct:.1f}% difference" if hr_diff_pct > self.hashrate_tolerance_pct else None,
                severity="warning" if hr_diff_pct > self.hashrate_tolerance_pct else "info"
            ))

            # Dashboard accuracy
            dashboard_hr = dashboard_data.get("hashrate_raw", 0)
            dashboard_diff_pct = abs(sentinel_hr - dashboard_hr) / sentinel_hr * 100 if sentinel_hr > 0 else 0

            self.verification_results.append(VerificationResult(
                check_name=f"miner_hashrate_sentinel_vs_dashboard_{name}",
                passed=dashboard_diff_pct <= self.hashrate_tolerance_pct,
                source_a="sentinel",
                source_b="dashboard",
                value_a=sentinel_hr,
                value_b=dashboard_hr,
                discrepancy=f"{dashboard_diff_pct:.1f}% difference" if dashboard_diff_pct > self.hashrate_tolerance_pct else None,
                severity="error" if dashboard_diff_pct > 10 else "warning" if dashboard_diff_pct > self.hashrate_tolerance_pct else "info"
            ))

    def _verify_hashrate_totals(self):
        """Verify total fleet hashrate matches across sources."""
        sentinel_total = self.sentinel.get_total_hashrate()
        pool_stats = self.pool_api.get_pool_stats()
        pool_total = pool_stats.get("poolStats", {}).get("poolHashrate", 0) / 1e9  # Convert to GH/s
        dashboard_total = self.dashboard.displayed_stats.get("fleet_hashrate_raw", 0)

        # Sentinel vs Pool
        sp_diff_pct = abs(sentinel_total - pool_total) / sentinel_total * 100 if sentinel_total > 0 else 0
        self.verification_results.append(VerificationResult(
            check_name="fleet_hashrate_sentinel_vs_pool",
            passed=sp_diff_pct <= self.hashrate_tolerance_pct,
            source_a="sentinel",
            source_b="pool_api",
            value_a=f"{sentinel_total:.1f} GH/s",
            value_b=f"{pool_total:.1f} GH/s",
            discrepancy=f"{sp_diff_pct:.1f}% difference" if sp_diff_pct > self.hashrate_tolerance_pct else None,
            severity="warning" if sp_diff_pct > self.hashrate_tolerance_pct else "info"
        ))

        # Sentinel vs Dashboard
        sd_diff_pct = abs(sentinel_total - dashboard_total) / sentinel_total * 100 if sentinel_total > 0 else 0
        self.verification_results.append(VerificationResult(
            check_name="fleet_hashrate_sentinel_vs_dashboard",
            passed=sd_diff_pct <= self.hashrate_tolerance_pct,
            source_a="sentinel",
            source_b="dashboard",
            value_a=f"{sentinel_total:.1f} GH/s",
            value_b=f"{dashboard_total:.1f} GH/s",
            discrepancy=f"{sd_diff_pct:.1f}% difference" if sd_diff_pct > self.hashrate_tolerance_pct else None,
            severity="critical" if sd_diff_pct > 20 else "error" if sd_diff_pct > 10 else "info",
            recommendation="UI is misleading operators" if sd_diff_pct > 10 else None
        ))

    def _verify_share_counts(self):
        """Verify share counts match between Sentinel and Pool."""
        sentinel_miners = self.sentinel.get_all_miners()
        pool_miners = self.pool_api.get_all_miner_stats()

        for name in sentinel_miners:
            sentinel_data = sentinel_miners[name]
            pool_data = pool_miners.get(name, {})

            sentinel_shares = sentinel_data.shares_accepted
            pool_shares = pool_data.get("validSharesCount", 0)

            # Allow some tolerance (shares might be in transit)
            diff = abs(sentinel_shares - pool_shares)
            tolerance = max(sentinel_shares * (self.share_tolerance_pct / 100), 10)

            self.verification_results.append(VerificationResult(
                check_name=f"share_count_{name}",
                passed=diff <= tolerance,
                source_a="sentinel",
                source_b="pool_api",
                value_a=sentinel_shares,
                value_b=pool_shares,
                discrepancy=f"{diff} shares difference" if diff > tolerance else None,
                severity="warning" if diff > tolerance else "info"
            ))

    def _verify_alert_state(self):
        """Verify alert state consistency."""
        # Check that suppressed alerts are actually not sent
        for alert in self.sentinel.get_alerts():
            if alert.suppression_reason:
                self.verification_results.append(VerificationResult(
                    check_name=f"alert_suppression_{alert.alert_type}",
                    passed=not alert.was_sent,
                    source_a="sentinel_state",
                    source_b="alert_delivery",
                    value_a=alert.suppression_reason,
                    value_b="suppressed" if not alert.was_sent else "sent",
                    discrepancy="Alert sent despite suppression" if alert.was_sent else None,
                    severity="error" if alert.was_sent else "info"
                ))

    def _verify_dashboard_accuracy(self):
        """Verify dashboard displays accurate real-time data."""
        # Check online/offline status
        sentinel_online = len([m for m in self.sentinel.miners.values() if m.status == "online"])
        dashboard_online = self.dashboard.displayed_stats.get("miners_online", 0)

        self.verification_results.append(VerificationResult(
            check_name="online_miner_count",
            passed=sentinel_online == dashboard_online,
            source_a="sentinel",
            source_b="dashboard",
            value_a=sentinel_online,
            value_b=dashboard_online,
            discrepancy=f"Off by {abs(sentinel_online - dashboard_online)} miners" if sentinel_online != dashboard_online else None,
            severity="critical" if abs(sentinel_online - dashboard_online) > 1 else "info"
        ))

        # Check total miner count
        sentinel_total = len(self.sentinel.miners)
        dashboard_total = self.dashboard.displayed_stats.get("miners_total", 0)

        self.verification_results.append(VerificationResult(
            check_name="total_miner_count",
            passed=sentinel_total == dashboard_total,
            source_a="sentinel",
            source_b="dashboard",
            value_a=sentinel_total,
            value_b=dashboard_total,
            discrepancy=f"Off by {abs(sentinel_total - dashboard_total)} miners" if sentinel_total != dashboard_total else None,
            severity="error" if sentinel_total != dashboard_total else "info"
        ))

    def _verify_pool_api_accuracy(self):
        """Verify Pool API returns accurate data."""
        pool_stats = self.pool_api.get_pool_stats()

        # Verify block height is reasonable
        block_height = pool_stats.get("poolStats", {}).get("blockHeight", 0)
        self.verification_results.append(VerificationResult(
            check_name="block_height_valid",
            passed=block_height > 0,
            source_a="pool_api",
            source_b="validation",
            value_a=block_height,
            value_b="> 0",
            discrepancy="Block height is zero or negative" if block_height <= 0 else None,
            severity="critical" if block_height <= 0 else "info"
        ))

        # Verify network hashrate is positive
        network_hr = pool_stats.get("networkStats", {}).get("networkHashrate", 0)
        self.verification_results.append(VerificationResult(
            check_name="network_hashrate_valid",
            passed=network_hr > 0,
            source_a="pool_api",
            source_b="validation",
            value_a=f"{network_hr / 1e15:.2f} PH/s",
            value_b="> 0",
            discrepancy="Network hashrate is zero" if network_hr <= 0 else None,
            severity="critical" if network_hr <= 0 else "info"
        ))

    def _detect_race_conditions(self):
        """Detect potential race condition windows."""
        # Check for stale data (data age)
        dashboard_cache_age = self.dashboard.cache_age_seconds
        max_acceptable_age = 60  # 1 minute

        self.verification_results.append(VerificationResult(
            check_name="dashboard_cache_freshness",
            passed=dashboard_cache_age <= max_acceptable_age,
            source_a="dashboard_cache",
            source_b="freshness_threshold",
            value_a=f"{dashboard_cache_age}s old",
            value_b=f"max {max_acceptable_age}s",
            discrepancy=f"Cache is {dashboard_cache_age - max_acceptable_age}s too old" if dashboard_cache_age > max_acceptable_age else None,
            severity="warning" if dashboard_cache_age > max_acceptable_age else "info"
        ))

        # Check for update timestamp consistency
        sentinel_miners = self.sentinel.get_all_miners()
        update_times = [m.last_update for m in sentinel_miners.values()]
        if update_times:
            time_spread = max(update_times) - min(update_times)
            max_spread = 30  # 30 seconds max spread

            self.verification_results.append(VerificationResult(
                check_name="miner_update_synchronization",
                passed=time_spread <= max_spread,
                source_a="sentinel_timestamps",
                source_b="sync_threshold",
                value_a=f"{time_spread:.1f}s spread",
                value_b=f"max {max_spread}s",
                discrepancy=f"Updates desynchronized by {time_spread - max_spread:.1f}s" if time_spread > max_spread else None,
                severity="warning" if time_spread > max_spread else "info"
            ))

    def _verify_no_ui_lies(self):
        """Verify UI doesn't mislead operators."""
        issues = []

        # Check: Offline miner shown as online
        for name, miner in self.sentinel.miners.items():
            dashboard_data = self.dashboard.displayed_miners.get(name, {})
            if miner.status == "offline" and dashboard_data.get("status") == "online":
                issues.append({
                    "type": "offline_shown_online",
                    "miner": name,
                    "actual": "offline",
                    "displayed": "online",
                    "severity": "critical"
                })
                self.verification_results.append(VerificationResult(
                    check_name=f"ui_honesty_{name}_status",
                    passed=False,
                    source_a="sentinel",
                    source_b="dashboard",
                    value_a="offline",
                    value_b="online (displayed)",
                    discrepancy="UI showing offline miner as online!",
                    severity="critical",
                    recommendation="Operator may think miner is healthy when it's down"
                ))

        # Check: Hashrate shown significantly higher than actual
        sentinel_total = self.sentinel.get_total_hashrate()
        dashboard_total = self.dashboard.displayed_stats.get("fleet_hashrate_raw", 0)
        if dashboard_total > sentinel_total * 1.2:  # >20% inflated
            issues.append({
                "type": "hashrate_inflated",
                "actual": sentinel_total,
                "displayed": dashboard_total,
                "inflation_pct": (dashboard_total / sentinel_total - 1) * 100,
                "severity": "error"
            })

        # Check: Temperature not shown when high
        for name, miner in self.sentinel.miners.items():
            if miner.temperature >= 75:  # Warning threshold
                dashboard_temp = self.dashboard.displayed_miners.get(name, {}).get("temperature", 0)
                if abs(dashboard_temp - miner.temperature) > self.temp_tolerance_c:
                    issues.append({
                        "type": "temp_mismatch_critical",
                        "miner": name,
                        "actual": miner.temperature,
                        "displayed": dashboard_temp,
                        "severity": "warning"
                    })

        self.reconciliation_issues.extend(issues)

    # ═══════════════════════════════════════════════════════════════════════════
    # REPORT GENERATION
    # ═══════════════════════════════════════════════════════════════════════════

    def _generate_reconciliation_report(self) -> Dict:
        """Generate comprehensive source-of-truth reconciliation report."""
        total_checks = len(self.verification_results)
        passed_checks = len([r for r in self.verification_results if r.passed])
        failed_checks = total_checks - passed_checks

        # Categorize by severity
        by_severity = defaultdict(list)
        for result in self.verification_results:
            by_severity[result.severity].append(result)

        # Identify critical issues
        critical_issues = [r for r in self.verification_results if r.severity == "critical" and not r.passed]
        error_issues = [r for r in self.verification_results if r.severity == "error" and not r.passed]

        report = {
            "summary": {
                "total_checks": total_checks,
                "passed": passed_checks,
                "failed": failed_checks,
                "pass_rate": (passed_checks / total_checks * 100) if total_checks > 0 else 0,
                "timestamp": datetime.now().isoformat(),
            },
            "source_comparisons": {
                "sentinel_vs_pool": self._get_comparison_summary("sentinel", "pool_api"),
                "sentinel_vs_dashboard": self._get_comparison_summary("sentinel", "dashboard"),
                "pool_vs_dashboard": self._get_comparison_summary("pool_api", "dashboard"),
            },
            "data_consistency": {
                "hashrate_consistent": all(
                    r.passed for r in self.verification_results
                    if "hashrate" in r.check_name
                ),
                "miner_counts_match": all(
                    r.passed for r in self.verification_results
                    if "miner_count" in r.check_name
                ),
                "share_counts_match": all(
                    r.passed for r in self.verification_results
                    if "share_count" in r.check_name
                ),
            },
            "race_condition_analysis": {
                "cache_freshness_ok": all(
                    r.passed for r in self.verification_results
                    if "cache" in r.check_name.lower()
                ),
                "data_synchronization_ok": all(
                    r.passed for r in self.verification_results
                    if "sync" in r.check_name.lower()
                ),
                "potential_race_windows": [
                    r.check_name for r in self.verification_results
                    if not r.passed and ("cache" in r.check_name.lower() or "sync" in r.check_name.lower())
                ]
            },
            "ui_honesty": {
                "operator_can_trust_ui": len(critical_issues) == 0 and len(error_issues) == 0,
                "misleading_situations": [
                    {
                        "check": r.check_name,
                        "issue": r.discrepancy,
                        "recommendation": r.recommendation
                    }
                    for r in self.verification_results
                    if not r.passed and r.severity in ["critical", "error"]
                ],
            },
            "detailed_results": {
                "critical": [self._result_to_dict(r) for r in by_severity.get("critical", [])],
                "error": [self._result_to_dict(r) for r in by_severity.get("error", [])],
                "warning": [self._result_to_dict(r) for r in by_severity.get("warning", [])],
                "info": [self._result_to_dict(r) for r in by_severity.get("info", [])],
            },
            "reconciliation_issues": self.reconciliation_issues,
            "overall_verdict": self._compute_verdict(critical_issues, error_issues),
        }

        return report

    def _get_comparison_summary(self, source_a: str, source_b: str) -> Dict:
        """Get summary of comparisons between two sources."""
        relevant = [
            r for r in self.verification_results
            if (r.source_a == source_a and r.source_b == source_b) or
               (r.source_a == source_b and r.source_b == source_a)
        ]
        passed = len([r for r in relevant if r.passed])
        total = len(relevant)

        return {
            "total_comparisons": total,
            "passed": passed,
            "failed": total - passed,
            "agreement_rate": (passed / total * 100) if total > 0 else 100,
            "discrepancies": [r.check_name for r in relevant if not r.passed]
        }

    def _result_to_dict(self, result: VerificationResult) -> Dict:
        """Convert VerificationResult to dictionary."""
        return {
            "check": result.check_name,
            "passed": result.passed,
            "sources": f"{result.source_a} vs {result.source_b}",
            "values": f"{result.value_a} vs {result.value_b}",
            "discrepancy": result.discrepancy,
            "recommendation": result.recommendation,
        }

    def _compute_verdict(self, critical: List, errors: List) -> Dict:
        """Compute overall verification verdict."""
        if critical:
            return {
                "status": "FAIL",
                "trustworthy": False,
                "message": f"CRITICAL: {len(critical)} critical issues detected. Review before relying on this data.",
                "action_required": True,
                "issues": [r.check_name for r in critical]
            }
        elif errors:
            return {
                "status": "WARNING",
                "trustworthy": False,
                "message": f"WARNING: {len(errors)} errors detected. Data may be inaccurate.",
                "action_required": True,
                "issues": [r.check_name for r in errors]
            }
        else:
            return {
                "status": "PASS",
                "trustworthy": True,
                "message": "All data sources are consistent. No discrepancies detected.",
                "action_required": False,
                "issues": []
            }


# ═══════════════════════════════════════════════════════════════════════════════
# INCONSISTENCY INJECTION (For testing detection capabilities)
# ═══════════════════════════════════════════════════════════════════════════════

class InconsistencyInjector:
    """Injects intentional inconsistencies to test detection."""

    def __init__(self, agent: E2EVerificationAgent):
        self.agent = agent

    def inject_hashrate_mismatch(self, miner_name: str, discrepancy_pct: float):
        """Make dashboard show different hashrate than Sentinel."""
        if miner_name in self.agent.dashboard.displayed_miners:
            sentinel_hr = self.agent.sentinel.miners[miner_name].hashrate_ghs
            inflated_hr = sentinel_hr * (1 + discrepancy_pct / 100)
            self.agent.dashboard.displayed_miners[miner_name]["hashrate_raw"] = inflated_hr

    def inject_status_mismatch(self, miner_name: str, dashboard_status: str):
        """Make dashboard show different status than Sentinel."""
        if miner_name in self.agent.dashboard.displayed_miners:
            self.agent.dashboard.displayed_miners[miner_name]["status"] = dashboard_status

    def inject_stale_cache(self, age_seconds: float):
        """Simulate stale dashboard cache."""
        self.agent.dashboard.cache_age_seconds = age_seconds

    def inject_share_count_drift(self, miner_name: str, drift: int):
        """Make pool show different share count than Sentinel."""
        if miner_name in self.agent.pool_api.miner_stats:
            self.agent.pool_api.miner_stats[miner_name]["validSharesCount"] += drift


# ═══════════════════════════════════════════════════════════════════════════════
# TEST SCENARIOS
# ═══════════════════════════════════════════════════════════════════════════════

def test_clean_verification():
    """Test verification with clean, consistent data."""
    print("TEST: Clean Verification (all data consistent)")
    print("-" * 50)

    agent = E2EVerificationAgent()
    agent.setup_test_environment()
    report = agent.run_all_verifications()

    print(f"  Total Checks: {report['summary']['total_checks']}")
    print(f"  Passed: {report['summary']['passed']}")
    print(f"  Pass Rate: {report['summary']['pass_rate']:.1f}%")
    print(f"  Verdict: {report['overall_verdict']['status']}")
    print()

    return report['overall_verdict']['status'] == "PASS"


def test_hashrate_mismatch_detection():
    """Test detection of hashrate discrepancies."""
    print("TEST: Hashrate Mismatch Detection")
    print("-" * 50)

    agent = E2EVerificationAgent()
    agent.setup_test_environment()

    # Inject 25% hashrate inflation on dashboard
    injector = InconsistencyInjector(agent)
    injector.inject_hashrate_mismatch("Miner-01", 25)

    report = agent.run_all_verifications()

    # Should detect the mismatch
    hashrate_issues = [
        r for r in report['detailed_results']['error'] + report['detailed_results']['warning']
        if "hashrate" in r['check'].lower() and "Miner-01" in r['check']
    ]

    detected = len(hashrate_issues) > 0
    print(f"  Mismatch Detected: {'Yes' if detected else 'No'}")
    print(f"  Verdict: {report['overall_verdict']['status']}")
    print()

    return detected


def test_offline_shown_online_detection():
    """Test detection of offline miner shown as online."""
    print("TEST: Offline Miner Shown as Online Detection")
    print("-" * 50)

    agent = E2EVerificationAgent()
    agent.setup_test_environment()

    # Make miner offline in Sentinel but online in dashboard
    agent.sentinel.miners["Miner-02"].status = "offline"
    # Dashboard still shows online (injector not needed, just don't update)

    report = agent.run_all_verifications()

    # Check reconciliation issues
    ui_lies = report['ui_honesty']['misleading_situations']
    detected = any("status" in issue['check'] or "offline" in str(issue).lower() for issue in ui_lies)

    print(f"  UI Lie Detected: {'Yes' if detected else 'No'}")
    print(f"  Critical Issues: {len(report['detailed_results']['critical'])}")
    print(f"  Verdict: {report['overall_verdict']['status']}")
    print()

    return detected or report['overall_verdict']['status'] != "PASS"


def test_stale_cache_detection():
    """Test detection of stale dashboard cache."""
    print("TEST: Stale Cache Detection")
    print("-" * 50)

    agent = E2EVerificationAgent()
    agent.setup_test_environment()

    # Inject stale cache (2 minutes old)
    injector = InconsistencyInjector(agent)
    injector.inject_stale_cache(120)

    report = agent.run_all_verifications()

    race_window_issues = report['race_condition_analysis']['potential_race_windows']
    detected = len(race_window_issues) > 0

    print(f"  Race Window Detected: {'Yes' if detected else 'No'}")
    print(f"  Issues: {race_window_issues}")
    print()

    return detected


def test_share_count_drift_detection():
    """Test detection of share count discrepancies."""
    print("TEST: Share Count Drift Detection")
    print("-" * 50)

    agent = E2EVerificationAgent()
    agent.setup_test_environment()

    # Inject significant share count drift
    injector = InconsistencyInjector(agent)
    injector.inject_share_count_drift("Miner-03", 500)

    report = agent.run_all_verifications()

    share_issues = [
        r for r in report['detailed_results']['warning']
        if "share_count" in r['check']
    ]

    detected = len(share_issues) > 0
    print(f"  Drift Detected: {'Yes' if detected else 'No'}")
    print()

    return detected


# ═══════════════════════════════════════════════════════════════════════════════
# MAIN EXECUTION
# ═══════════════════════════════════════════════════════════════════════════════

def run_all_e2e_tests():
    """Execute all E2E verification tests."""
    print("=" * 80)
    print("  END-TO-END VERIFICATION AGENT - Test Suite")
    print("  Cross-system truth validation for Spiral Pool")
    print("=" * 80)
    print()

    results = {
        "clean_verification": test_clean_verification(),
        "hashrate_mismatch": test_hashrate_mismatch_detection(),
        "offline_shown_online": test_offline_shown_online_detection(),
        "stale_cache": test_stale_cache_detection(),
        "share_drift": test_share_count_drift_detection(),
    }

    print("=" * 80)
    print("  SUMMARY")
    print("=" * 80)
    print()

    passed = sum(1 for v in results.values() if v)
    total = len(results)

    for test_name, result in results.items():
        status = "✓ PASS" if result else "✗ FAIL"
        print(f"  {status} - {test_name}")

    print()
    print(f"  Total: {passed}/{total} tests passed")
    print()

    # Generate final comprehensive report
    print("=" * 80)
    print("  GENERATING FULL VERIFICATION REPORT")
    print("=" * 80)

    agent = E2EVerificationAgent()
    agent.setup_test_environment()
    full_report = agent.run_all_verifications()

    print()
    print("SOURCE-OF-TRUTH RECONCILIATION:")
    print(f"  Sentinel vs Pool API: {full_report['source_comparisons']['sentinel_vs_pool']['agreement_rate']:.1f}% agreement")
    print(f"  Sentinel vs Dashboard: {full_report['source_comparisons']['sentinel_vs_dashboard']['agreement_rate']:.1f}% agreement")
    print()
    print("OPERATOR TRUST ASSESSMENT:")
    print(f"  Can Trust UI: {'Yes' if full_report['ui_honesty']['operator_can_trust_ui'] else 'No'}")
    print(f"  Misleading Situations: {len(full_report['ui_honesty']['misleading_situations'])}")
    print()
    print(f"OVERALL VERDICT: {full_report['overall_verdict']['status']}")
    print(f"  {full_report['overall_verdict']['message']}")
    print()

    return full_report


if __name__ == "__main__":
    run_all_e2e_tests()
