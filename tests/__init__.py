# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
Spiral Pool Comprehensive Test Suite
=====================================
Dynamic, time-based, scenario-driven simulations that mirror real mining operations.

This package contains:
- Scenario Simulation Agent (Agent 7): Validates behavior over time under realistic conditions
- End-to-End Verification Agent (Agent 8): Cross-system truth validation
- Comprehensive Test Runner: Executes all tests and generates final report

Usage:
    python -m tests.comprehensive_test_runner
"""

from .scenario_simulation_agent import (
    ScenarioSimulationAgent,
    ScenarioRunner,
    BaselineHealthScenarios,
    MinerFailureRecoveryScenarios,
    ThermalPerformanceScenarios,
    NetworkHashrateScenarios,
    BlockEarningsScenarios,
    ReportingTimeScenarios,
    QuietHoursMaintenanceScenarios,
    MultiCoinProfileScenarios,
)

from .e2e_verification_agent import (
    E2EVerificationAgent,
    VerificationResult,
)

from .comprehensive_test_runner import ComprehensiveTestRunner

__all__ = [
    'ScenarioSimulationAgent',
    'ScenarioRunner',
    'E2EVerificationAgent',
    'VerificationResult',
    'ComprehensiveTestRunner',
    'BaselineHealthScenarios',
    'MinerFailureRecoveryScenarios',
    'ThermalPerformanceScenarios',
    'NetworkHashrateScenarios',
    'BlockEarningsScenarios',
    'ReportingTimeScenarios',
    'QuietHoursMaintenanceScenarios',
    'MultiCoinProfileScenarios',
]
