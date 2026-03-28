#!/usr/bin/env python3

# SPDX-License-Identifier: BSD-3-Clause
# SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

"""
╔═══════════════════════════════════════════════════════════════════════════════╗
║  Spiral Sentinel - HA Manager Module                                         ║
║  Virtual IP (VIP) Detection and Miner Redirection                            ║
╠═══════════════════════════════════════════════════════════════════════════════╣
║  This module integrates with Spiral Pool's HA/VIP system to:                 ║
║  • Detect when HA mode is enabled                                            ║
║  • Monitor the VIP cluster status                                            ║
║  • Provide the correct stratum address for miners                            ║
║  • Alert on failover events                                                  ║
╠═══════════════════════════════════════════════════════════════════════════════╣
║  The VIP system allows miners to connect to a floating IP address that       ║
║  automatically moves between pool nodes during failover. This is transparent ║
║  to miners - they keep their same connection settings.                       ║
╚═══════════════════════════════════════════════════════════════════════════════╝
"""

import json
import logging
import re
import socket
import subprocess
import threading
import urllib.request
import urllib.error
import time
import os
from dataclasses import dataclass
from typing import Optional, List, Dict, Any

__version__ = "2.0.0-PHI_HASH_REACTOR"

logger = logging.getLogger(__name__)


@dataclass
class CoinPortInfo:
    """Port information for a specific coin."""
    stratum_v1: int
    stratum_v2: int = 0
    stratum_tls: int = 0


@dataclass
class HANode:
    """Represents a node in the HA cluster."""
    id: str
    host: str
    port: int
    role: str  # MASTER, BACKUP, OBSERVER
    priority: int
    stratum_port: int
    is_healthy: bool
    last_seen: Optional[float] = None
    coin_ports: Optional[Dict[str, CoinPortInfo]] = None  # Per-coin ports (key: coin symbol)


@dataclass
class HAStatus:
    """Current HA cluster status."""
    enabled: bool
    state: str  # initializing, running, election, failover, degraded
    vip: str  # Virtual IP address
    vip_interface: str
    master_id: str
    master_host: str
    local_role: str
    local_id: str
    nodes: List[HANode]
    failover_count: int
    last_checked: float
    coin_ports: Optional[Dict[str, CoinPortInfo]] = None  # Local node's coin ports


class HAManager:
    """
    Manages HA/VIP detection and miner redirection for Spiral Sentinel.

    The HAManager queries the pool's HA status endpoint to determine:
    - Whether HA mode is active
    - The current VIP address for miners to use
    - The cluster health and failover status

    Example usage:
        ha = HAManager()
        if ha.is_ha_enabled():
            stratum_addr = ha.get_stratum_address()
            print(f"Miners should connect to: {stratum_addr}")
    """

    # Default HA status port (matches VIP manager in Go)
    DEFAULT_HA_PORT = 5354

    # How often to check HA status (seconds)
    CHECK_INTERVAL = 30

    # Valid HA role values from the API
    VALID_ROLES = {"MASTER", "BACKUP", "OBSERVER", "UNKNOWN"}

    # Maximum HTTP response size (1 MB — more than enough for HA status)
    MAX_RESPONSE_SIZE = 1024 * 1024

    def __init__(self, pool_host: str = "localhost", ha_port: int = None, state_file: str = None):
        """
        Initialize HAManager.

        Args:
            pool_host: The pool server hostname/IP (default: localhost)
            ha_port: The HA status API port (default: 5354)
            state_file: Path to persist HA role state (default: $SPIRALPOOL_INSTALL_DIR/data/ha_state.json)
        """
        # SECURITY: Validate pool_host (prevents SSRF via URL injection)
        if not re.match(r'^[a-zA-Z0-9._:-]+$', pool_host):
            raise ValueError(f"Invalid pool_host: {pool_host!r}")
        self.pool_host = pool_host
        self.ha_port = ha_port or self.DEFAULT_HA_PORT
        self._status: Optional[HAStatus] = None
        self._last_check: float = 0
        self._cache_ttl = self.CHECK_INTERVAL
        self._lock = threading.Lock()

        # Callbacks for events
        self._on_failover_callbacks: List[callable] = []
        self._on_ha_enabled_callbacks: List[callable] = []
        self._on_ha_disabled_callbacks: List[callable] = []

        # R-18 FIX: Persist role state across restarts
        if state_file:
            self._state_file = state_file
        else:
            install_dir = os.environ.get("SPIRALPOOL_INSTALL_DIR", "/spiralpool")
            self._state_file = os.path.join(install_dir, "data", "ha_state.json")

        # Service control
        self._previous_role: Optional[str] = self._load_saved_role()
        self._service_control_enabled: bool = True
        self._service_control_script: str = "/usr/local/bin/spiralpool-ha-service"

    # =========================================================================
    # State Persistence (R-18)
    # =========================================================================

    def _load_saved_role(self) -> Optional[str]:
        """Load previously saved HA role from disk."""
        try:
            if os.path.exists(self._state_file):
                with open(self._state_file, 'r') as f:
                    data = json.load(f)
                role = data.get("previous_role")
                if role:
                    logger.info("Loaded saved HA role from %s: %s", self._state_file, role)
                return role
        except (json.JSONDecodeError, OSError, KeyError) as e:
            logger.warning("Failed to load HA state from %s: %s", self._state_file, e)
        return None

    def _save_role(self, role: str):
        """Persist current HA role to disk for restart recovery."""
        try:
            state_dir = os.path.dirname(self._state_file)
            if state_dir and not os.path.isdir(state_dir):
                os.makedirs(state_dir, exist_ok=True)
            tmp_path = self._state_file + ".tmp"
            with open(tmp_path, 'w') as f:
                json.dump({"previous_role": role, "timestamp": time.time()}, f)
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp_path, self._state_file)
        except OSError as e:
            logger.warning("Failed to save HA state to %s: %s", self._state_file, e)

    # =========================================================================
    # Service Control Methods
    # =========================================================================

    def set_service_control_enabled(self, enabled: bool):
        """
        Enable or disable automatic service control on role changes.

        Args:
            enabled: True to enable service control, False to disable
        """
        self._service_control_enabled = enabled

    def is_service_control_enabled(self) -> bool:
        """Check if automatic service control is enabled."""
        return self._service_control_enabled

    def _call_service_control(self, action: str, reason: str = "HA role change") -> bool:
        """
        Call the service control script.

        Args:
            action: "promote" or "demote"
            reason: Reason for the action (for logging)

        Returns:
            True if successful, False otherwise
        """
        if not self._service_control_enabled:
            return True

        if action not in ("promote", "demote"):
            logger.error("Invalid service control action: %s", action)
            return False

        script = self._service_control_script

        # Try alternate locations if primary doesn't exist
        if not os.path.exists(script):
            alt_locations = [
                "/spiralpool/scripts/linux/ha-service-control.sh",
                "/opt/spiralpool/scripts/linux/ha-service-control.sh",
            ]
            for alt in alt_locations:
                if os.path.exists(alt):
                    script = alt
                    break
            else:
                # Script not found - cannot perform service control
                logger.error("Service control script not found at %s or alternate locations — %s FAILED", script, action)
                return False

        try:
            # SECURITY: Sanitize reason for safe shell script argument
            safe_reason = re.sub(r'[^a-zA-Z0-9 _\->:.]', '', reason)[:200]
            cmd = [script, action, safe_reason]
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=30
            )
            return result.returncode == 0
        except (subprocess.TimeoutExpired, FileNotFoundError, PermissionError) as e:
            logger.error("Service control '%s' failed: %s", action, e)
            return False

    def promote_services(self, reason: str = "Manual promote") -> bool:
        """
        Manually promote this node - start Sentinel and Dashboard services.

        Args:
            reason: Reason for the promotion

        Returns:
            True if successful
        """
        return self._call_service_control("promote", reason)

    def demote_services(self, reason: str = "Manual demote") -> bool:
        """
        Manually demote this node - stop Sentinel and Dashboard services.

        Args:
            reason: Reason for the demotion

        Returns:
            True if successful
        """
        return self._call_service_control("demote", reason)

    def _handle_role_change(self, old_role: Optional[str], new_role: str):
        """
        Handle a role change by starting/stopping services as needed.

        Args:
            old_role: Previous role (None if first check)
            new_role: New role
        """
        if not self._service_control_enabled:
            return

        if old_role == new_role:
            return  # No change

        logger.info("HA role change: %s -> %s", old_role or "UNKNOWN", new_role)

        # Determine action based on role transition
        if new_role == "MASTER":
            # Became MASTER - start services
            success = self._call_service_control("promote", f"Role change: {old_role or 'UNKNOWN'} -> MASTER")
            if not success:
                logger.warning("SERVICE CONTROL FAILED: promote after role change %s -> MASTER. "
                               "Services may not be running. Check script availability and permissions.",
                               old_role or "UNKNOWN")
        else:
            # Any non-MASTER destination (BACKUP, OBSERVER, UNKNOWN) - ensure services stopped
            # This covers MASTER->BACKUP, MASTER->OBSERVER, and BACKUP<->OBSERVER transitions.
            # Even if services should already be stopped, demote is idempotent and safe.
            success = self._call_service_control("demote", f"Role change: {old_role or 'UNKNOWN'} -> {new_role}")
            if not success:
                logger.warning("SERVICE CONTROL FAILED: demote after role change %s -> %s. "
                               "Services may still be running on non-MASTER node.",
                               old_role or "UNKNOWN", new_role)

    # =========================================================================
    # Status Fetching Methods
    # =========================================================================

    def _fetch_status(self) -> Optional[Dict[str, Any]]:
        """Fetch HA status from the pool's status endpoint."""
        url = f"http://{self.pool_host}:{self.ha_port}/status"

        try:
            req = urllib.request.Request(
                url,
                headers={"User-Agent": f"SpiralSentinel-HA/{__version__}"}
            )
            with urllib.request.urlopen(req, timeout=5) as resp:
                # SECURITY: Limit response size to prevent memory exhaustion
                raw = resp.read(self.MAX_RESPONSE_SIZE + 1)
                if len(raw) > self.MAX_RESPONSE_SIZE:
                    logger.error("HA status response exceeds %d bytes — ignoring", self.MAX_RESPONSE_SIZE)
                    return None
                return json.loads(raw.decode())
        except (urllib.error.URLError, urllib.error.HTTPError,
                socket.timeout, json.JSONDecodeError, OSError) as e:
            # Connection refused or timeout - HA likely not enabled
            logger.warning("HA status fetch failed from %s: %s", url, e)
            return None

    def _parse_coin_ports(self, raw_ports: Optional[Dict[str, Any]]) -> Optional[Dict[str, CoinPortInfo]]:
        """Parse raw coinPorts JSON into CoinPortInfo dict."""
        if not raw_ports:
            return None
        result = {}
        for coin, port_data in raw_ports.items():
            if isinstance(port_data, dict):
                result[coin] = CoinPortInfo(
                    stratum_v1=port_data.get("stratumV1", 0),
                    stratum_v2=port_data.get("stratumV2", 0),
                    stratum_tls=port_data.get("stratumTLS", 0)
                )
        return result if result else None

    @staticmethod
    def _sanitize_host(host: str) -> str:
        """Sanitize host value from API response (prevent log/embed injection)."""
        if not host or not re.match(r'^[a-zA-Z0-9._:-]+$', host):
            return "<invalid>" if host else ""
        return host

    def _parse_status(self, data: Dict[str, Any]) -> HAStatus:
        """Parse raw status data into HAStatus object."""
        nodes = []
        for n in (data.get("nodes") or []):
            # SECURITY: Validate role values from external API
            node_role = n.get("role", "UNKNOWN")
            if node_role not in self.VALID_ROLES:
                logger.warning("Unknown HA node role from API: %r, treating as UNKNOWN", node_role)
                node_role = "UNKNOWN"
            nodes.append(HANode(
                id=str(n.get("id", ""))[:64],
                host=self._sanitize_host(n.get("host", "")),
                port=n.get("port", 0),
                role=node_role,
                priority=n.get("priority", 999),
                stratum_port=n.get("stratumPort", 0),  # No default - must come from config
                is_healthy=n.get("isHealthy", False),
                last_seen=n.get("lastSeen"),
                coin_ports=self._parse_coin_ports(n.get("coinPorts"))
            ))

        # SECURITY: Validate localRole against known values
        local_role = data.get("localRole", "UNKNOWN")
        if local_role not in self.VALID_ROLES:
            logger.warning("Unknown HA localRole from API: %r, treating as UNKNOWN", local_role)
            local_role = "UNKNOWN"

        return HAStatus(
            enabled=data.get("enabled", False),
            state=data.get("state", "unknown"),
            vip=data.get("vip", ""),
            vip_interface=data.get("vipInterface", ""),
            master_id=str(data.get("masterId", ""))[:64],
            master_host=self._sanitize_host(data.get("masterHost", "")),
            local_role=local_role,
            local_id=str(data.get("localId", ""))[:64],
            nodes=nodes,
            failover_count=data.get("failoverCount", 0),
            last_checked=time.time(),
            coin_ports=self._parse_coin_ports(data.get("coinPorts"))
        )

    def refresh_status(self, force: bool = False) -> Optional[HAStatus]:
        """
        Refresh HA status from the pool.

        Args:
            force: Force refresh even if cache is still valid

        Returns:
            HAStatus or None if HA is not available
        """
        now = time.time()

        # Quick cache check without lock (read-only, safe for cache hit)
        if not force and self._status and (now - self._last_check) < self._cache_ttl:
            return self._status

        with self._lock:
            # Re-check under lock (another thread may have refreshed)
            now = time.time()
            if not force and self._status and (now - self._last_check) < self._cache_ttl:
                return self._status

            data = self._fetch_status()
            self._last_check = now

            if data is None:
                # HA endpoint not available - probably not enabled
                old_enabled = self._status.enabled if self._status else False
                self._status = None

                # Trigger callback if HA was previously enabled
                if old_enabled:
                    for cb in self._on_ha_disabled_callbacks:
                        try:
                            cb()
                        except Exception as e:
                            logger.error("HA disabled callback failed: %s", e, exc_info=True)

                return None

            old_status = self._status
            try:
                self._status = self._parse_status(data)
            except (TypeError, KeyError, ValueError) as e:
                logger.error("Failed to parse HA status response: %s", e)
                self._last_check = 0  # Force retry on next call
                return self._status  # Keep previous status on parse failure

            # Check for state changes and trigger callbacks
            if old_status is None and self._status.enabled:
                for cb in self._on_ha_enabled_callbacks:
                    try:
                        cb(self._status)
                    except Exception as e:
                        logger.error("HA enabled callback failed: %s", e, exc_info=True)

            if old_status and self._status.failover_count > old_status.failover_count:
                logger.warning("Failover detected: count %d -> %d",
                               old_status.failover_count, self._status.failover_count)
                for cb in self._on_failover_callbacks:
                    try:
                        cb(self._status, old_status)
                    except Exception as e:
                        logger.error("Failover callback failed: %s", e, exc_info=True)

            # Check for role changes and control services accordingly
            new_role = self._status.local_role if self._status else None
            if new_role and new_role != self._previous_role:
                self._handle_role_change(self._previous_role, new_role)
                self._previous_role = new_role
                self._save_role(new_role)

            return self._status

    def is_ha_enabled(self) -> bool:
        """
        Check if HA mode is enabled on the pool.

        Returns:
            True if HA is enabled and cluster is operational
        """
        status = self.refresh_status()
        return status is not None and status.enabled

    def is_local_master(self) -> bool:
        """
        Check if this local node is the MASTER.

        Returns:
            True if this node is MASTER, False otherwise.
            Returns True if HA is disabled (single-node mode = default master).
        """
        status = self.refresh_status()
        if status is None or not status.enabled:
            # HA not enabled - single node mode, we are the default master
            return True
        return status.local_role == "MASTER"

    def get_local_role(self) -> str:
        """
        Get the local node's current role in the HA cluster.

        Returns:
            Role string: "MASTER", "BACKUP", "OBSERVER", or "STANDALONE" if HA disabled
        """
        status = self.refresh_status()
        if status is None or not status.enabled:
            return "STANDALONE"
        return status.local_role

    def get_status(self) -> Optional[HAStatus]:
        """
        Get the current HA status.

        Returns:
            HAStatus object or None if HA is not available
        """
        return self.refresh_status()

    def get_vip(self) -> Optional[str]:
        """
        Get the Virtual IP address for miners to connect to.

        Returns:
            VIP address string or None if HA is not enabled
        """
        status = self.refresh_status()
        if status and status.enabled and status.vip:
            return status.vip
        return None

    def get_stratum_address(self, port: int = None) -> str:
        """
        Get the stratum address for miners to use.

        In HA mode, returns the VIP address.
        In non-HA mode, returns the pool_host.

        Args:
            port: Stratum port (required - no default to avoid coin-specific assumptions)

        Returns:
            Stratum address in format "host:port" or just "host" if no port provided
        """
        vip = self.get_vip()
        host = vip if vip else self.pool_host
        if port:
            return f"{host}:{port}"
        return host

    def get_stratum_port_for_coin(self, coin: str) -> Optional[int]:
        """
        Get the stratum V1 port for a specific coin from the HA status.

        Args:
            coin: Coin symbol (e.g., "DGB", "BTC", "BCH", "BC2")

        Returns:
            Port number or None if not configured
        """
        status = self.refresh_status()
        if not status or not status.coin_ports:
            return None
        coin_upper = coin.upper()
        if coin_upper in status.coin_ports:
            return status.coin_ports[coin_upper].stratum_v1
        return None

    def get_coin_ports(self) -> Optional[Dict[str, CoinPortInfo]]:
        """
        Get all configured coin ports from the HA status.

        Returns:
            Dict mapping coin symbol to CoinPortInfo, or None if not available
        """
        status = self.refresh_status()
        if not status:
            return None
        return status.coin_ports

    def get_pool_mode(self) -> str:
        """
        Determine if pool is in solo or multi-coin mode.

        Checks the coin_ports to determine how many coins are configured.

        Returns:
            "solo" if 1 coin, "multi" if 2+ coins, "unknown" if unavailable
        """
        coin_ports = self.get_coin_ports()
        if not coin_ports:
            return "unknown"

        # Count coins with valid stratum ports
        configured_coins = [c for c, p in coin_ports.items() if p.stratum_v1 > 0]
        if len(configured_coins) == 0:
            return "unknown"
        elif len(configured_coins) == 1:
            return "solo"
        else:
            return "multi"

    def get_configured_coins(self) -> List[str]:
        """
        Get list of configured coin symbols.

        Returns:
            List of coin symbols (e.g., ["DGB", "BTC"]) or empty list
        """
        coin_ports = self.get_coin_ports()
        if not coin_ports:
            return []

        return [c for c, p in coin_ports.items() if p.stratum_v1 > 0]

    def get_stratum_address_for_coin(self, coin: str) -> Optional[str]:
        """
        Get the full stratum address for a specific coin.

        Args:
            coin: Coin symbol (e.g., "DGB", "BTC", "BCH", "BC2")

        Returns:
            Stratum address in format "host:port" or None if not configured
        """
        port = self.get_stratum_port_for_coin(coin)
        if not port:
            return None

        vip = self.get_vip()
        host = vip if vip else self.pool_host
        return f"{host}:{port}"

    def get_all_stratum_addresses(self) -> Dict[str, str]:
        """
        Get stratum addresses for all configured coins.

        Returns:
            Dict mapping coin symbol to stratum address
        """
        result = {}
        coins = self.get_configured_coins()
        for coin in coins:
            addr = self.get_stratum_address_for_coin(coin)
            if addr:
                result[coin] = addr
        return result

    def get_master_node(self) -> Optional[HANode]:
        """
        Get the current master node in the cluster.

        Returns:
            HANode for the master or None
        """
        status = self.refresh_status()
        if not status or not status.enabled:
            return None

        for node in status.nodes:
            if node.role == "MASTER":
                return node
        return None

    def get_healthy_nodes(self) -> List[HANode]:
        """
        Get all healthy nodes in the cluster.

        Returns:
            List of healthy HANode objects
        """
        status = self.refresh_status()
        if not status or not status.enabled:
            return []

        return [n for n in status.nodes if n.is_healthy]

    def get_cluster_size(self) -> int:
        """
        Get the number of nodes in the HA cluster.

        Returns:
            Number of nodes or 0 if HA is not enabled
        """
        status = self.refresh_status()
        if not status or not status.enabled:
            return 0
        return len(status.nodes)

    def get_failover_count(self) -> int:
        """
        Get the total number of failover events since cluster started.

        Returns:
            Failover count or 0
        """
        status = self.refresh_status()
        if not status:
            return 0
        return status.failover_count

    def is_cluster_healthy(self) -> bool:
        """
        Check if the HA cluster is in a healthy state.

        Returns:
            True if cluster is running normally
        """
        status = self.refresh_status()
        if not status or not status.enabled:
            return False
        return status.state == "running"

    def is_failover_in_progress(self) -> bool:
        """
        Check if a failover is currently in progress.

        Returns:
            True if cluster is in failover or election state
        """
        status = self.refresh_status()
        if not status:
            return False
        return status.state in ("election", "failover")

    def on_failover(self, callback: callable):
        """
        Register a callback for failover events.

        The callback receives (new_status, old_status) arguments.
        """
        self._on_failover_callbacks.append(callback)

    def on_ha_enabled(self, callback: callable):
        """
        Register a callback for when HA becomes enabled.

        The callback receives (status) argument.
        """
        self._on_ha_enabled_callbacks.append(callback)

    def on_ha_disabled(self, callback: callable):
        """
        Register a callback for when HA becomes disabled.

        The callback receives no arguments.
        """
        self._on_ha_disabled_callbacks.append(callback)

    def format_status_report(self) -> str:
        """
        Format a human-readable status report.

        Returns:
            Formatted status string
        """
        status = self.refresh_status()

        if not status:
            return "HA Mode: DISABLED (single-node mode)"

        if not status.enabled:
            return "HA Mode: DISABLED"

        pool_mode = self.get_pool_mode()
        configured_coins = self.get_configured_coins()

        lines = [
            f"HA Mode: ENABLED",
            f"State: {status.state.upper()}",
            f"VIP: {status.vip}",
            f"Pool Mode: {pool_mode.upper()}",
            f"Coins: {', '.join(configured_coins) if configured_coins else 'None detected'}",
            f"Master: {status.master_host} ({status.master_id})",
            f"Cluster Size: {len(status.nodes)} nodes",
            f"Healthy Nodes: {len(self.get_healthy_nodes())}",
            f"Failover Count: {status.failover_count}",
            "",
            "Nodes:",
        ]

        for node in sorted(status.nodes, key=lambda n: n.priority):
            health = "✓" if node.is_healthy else "✗"
            port_display = f":{node.stratum_port}" if node.stratum_port and node.stratum_port > 0 else ":<unknown>"
            lines.append(f"  [{health}] {node.host}{port_display} - {node.role} (P{node.priority})")

        # Add coin stratum addresses
        if configured_coins:
            lines.append("")
            lines.append("Stratum Addresses:")
            for coin in configured_coins:
                addr = self.get_stratum_address_for_coin(coin)
                if addr:
                    lines.append(f"  {coin}: stratum+tcp://{addr}")

        return "\n".join(lines)

    def to_dict(self) -> Dict[str, Any]:
        """
        Export current status as a dictionary.

        Returns:
            Dictionary with HA status data
        """
        status = self.refresh_status()

        if not status:
            return {"enabled": False, "available": False}

        pool_mode = self.get_pool_mode()
        configured_coins = self.get_configured_coins()

        return {
            "enabled": status.enabled,
            "available": True,
            "state": status.state,
            "vip": status.vip,
            "master_host": status.master_host,
            "master_id": status.master_id,
            "node_count": len(status.nodes),
            "healthy_nodes": len(self.get_healthy_nodes()),
            "failover_count": status.failover_count,
            "stratum_address": self.get_stratum_address(),
            "pool_mode": pool_mode,
            "configured_coins": configured_coins,
            "stratum_addresses": self.get_all_stratum_addresses(),
        }


def create_ha_alert_embed(ha_manager: HAManager, event_type: str = "failover") -> Dict[str, Any]:
    """
    Create a Discord embed for HA events.

    Args:
        ha_manager: HAManager instance
        event_type: Type of event (failover, ha_enabled, ha_disabled, degraded)

    Returns:
        Discord embed dictionary
    """
    # Lazy import to avoid circular dependency (SpiralSentinel imports ha_manager)
    try:
        from SpiralSentinel import theme as _theme
    except ImportError:
        _theme = lambda key, **kw: key  # Fallback to raw key if theme unavailable

    status = ha_manager.get_status()

    colors = {
        "failover": 0xFFA500,  # Orange - warning
        "ha_enabled": 0x00FF00,  # Green - good
        "ha_disabled": 0xFF0000,  # Red - alert
        "degraded": 0xFFFF00,  # Yellow - caution
    }

    # Theme-aware titles
    titles = {
        "failover": _theme("ha.failover.title"),
        "ha_enabled": _theme("ha.enabled.title"),
        "ha_disabled": _theme("ha.disabled.title"),
        "degraded": _theme("ha.degraded.title"),
    }

    embed = {
        "title": titles.get(event_type, "HA Event"),
        "color": colors.get(event_type, 0x808080),
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "fields": [],
    }

    if status and status.enabled:
        embed["fields"].extend([
            {"name": "VIP Address", "value": status.vip or "N/A", "inline": True},
            {"name": "Master", "value": f"{status.master_host}", "inline": True},
            {"name": "Cluster State", "value": status.state.upper(), "inline": True},
            {"name": "Nodes", "value": f"{len(status.nodes)} total, {len(ha_manager.get_healthy_nodes())} healthy", "inline": True},
            {"name": "Failover Count", "value": str(status.failover_count), "inline": True},
        ])

        if event_type == "failover":
            embed["description"] = _theme("ha.failover.body") + f" Miners connected to the VIP ({status.vip}) will automatically reconnect to the new master."
        elif event_type == "degraded":
            embed["description"] = _theme("ha.degraded.body")
        elif event_type == "ha_enabled":
            embed["description"] = _theme("ha.enabled.body")
    else:
        embed["description"] = _theme("ha.disabled.body")

    embed["footer"] = {"text": f"Spiral Sentinel HA v{__version__}"}

    return embed


# Quick test if run directly
if __name__ == "__main__":
    print("=" * 60)
    print("Spiral Sentinel HA Manager - Status Check")
    print("=" * 60)

    ha = HAManager()

    print("\n" + ha.format_status_report())

    if ha.is_ha_enabled():
        print(f"\n🎯 Miners should connect to: stratum+tcp://{ha.get_stratum_address()}")
    else:
        print("\n📍 HA mode not detected. Pool is running in single-node mode.")

    print("\n" + "=" * 60)
